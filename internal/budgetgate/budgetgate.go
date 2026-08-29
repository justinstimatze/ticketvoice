// Package budgetgate holds the word-budget check and sibling-scorer forwarding shared by
// ticketvoice's PreToolUse hook and gh-write's own backstop. The hook sees a ticket body only
// when it can find one in the tool call it's watching — a Linear field, a heredoc, now a file
// redirect — and a pipe defeats all three, since seeing what a pipe's upstream stage would
// produce means running it, which a PreToolUse hook has no business doing. gh-write doesn't have
// that limit: it already reads the real body bytes off its own stdin, from any source, before
// handing them to gh. Putting the same gate there closes the gap the hook structurally can't.
package budgetgate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AgentTag marks a body as agent-authored even though it's posted under the operator's own
// account — GitHub via gh-write, Linear via the hook's own `updatedInput` rewrite (main.go). One
// constant and one on/off check, so both surfaces agree on the marker and the opt-out.
const AgentTag = "🤖 "

// AgentTagEnabled reports whether AgentTag should be applied. On by default; off only when the
// operator has explicitly set TICKETVOICE_NO_AGENT_TAG — the point is a reader shouldn't have to
// already know to ask, so silence defaults to tagged, not the reverse.
func AgentTagEnabled() bool {
	return os.Getenv("TICKETVOICE_NO_AGENT_TAG") == ""
}

// Budgets in words of prose, fenced code excluded. An issue body carries a mechanism, its
// evidence, the exposure and the fix; a comment carries one of those.
const (
	IssueBudget   = 150
	CommentBudget = 120
	// Below this, section headers cost more lines than the structure they buy.
	headerFloor = 200
)

var (
	fencedCode = regexp.MustCompile("(?s)```.*?```")
	wordish    = regexp.MustCompile(`\p{L}`)
	headerLine = regexp.MustCompile(`(?m)^#{1,6}\s+\S`)
)

// ProseWords counts words outside fenced code blocks. Code is the part of a ticket that should be
// long; only the prose around it is being budgeted.
func ProseWords(s string) int {
	n := 0
	for _, f := range strings.Fields(fencedCode.ReplaceAllString(s, " ")) {
		if wordish.MatchString(f) {
			n++
		}
	}
	return n
}

// HeaderCount counts markdown headers outside fenced code.
func HeaderCount(s string) int {
	return len(headerLine.FindAllString(fencedCode.ReplaceAllString(s, " "), -1))
}

// Classify maps a gh-write object/verb pair to the budget kind and word budget that applies —
// shared so ticketvoice's hook-side match and gh-write's own backstop agree on what counts as an
// issue description, a PR description, or a comment.
func Classify(object, verb string) (kind string, budget int) {
	if verb == "comment" {
		return object + " comment", CommentBudget
	}
	if object == "pr" {
		return "PR description", IssueBudget
	}
	return "issue description", IssueBudget
}

const slots = `Four slots, in this order:
  1. The mechanism, one paragraph — what is broken, and why nothing catches it.
  2. Evidence it is real — a SHA, a log line, a failing assertion. One sentence.
  3. What is still exposed — file:line, not a description of the file.
  4. The fix, as a code block, plus one line on how to prove it can go red.
SHAs and file:line carry the detail; do not narrate what the reader can open.`

// BudgetFor applies the TICKETVOICE_MAX_WORDS override, shared by every caller of this package.
func BudgetFor(base int) int {
	if v := os.Getenv("TICKETVOICE_MAX_WORDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return base
}

// Evaluate is the one check every caller runs through: same budget math, same reason text. over
// is false and reason is empty when the text is within budget. The reason carries the diagnostic
// only — what to do about it differs by caller (a hook can offer a human "ask"; a CLI can only
// refuse and explain), so that line is each caller's own to append.
func Evaluate(text, kind string, budget int) (over bool, reason string) {
	words := ProseWords(text)
	if words <= budget {
		return false, ""
	}
	reason = fmt.Sprintf("This %s is %d words of prose against a %d-word budget — %d over.\n\n%s",
		kind, words, budget, words-budget, slots)
	if h := HeaderCount(text); h > 0 && words < headerFloor {
		reason += fmt.Sprintf("\n\nIt also carries %d section header(s) under %d words, which cost two lines each and imply more document than there is.",
			h, headerFloor)
	}
	return true, reason
}

// Judgment is what a sibling scorer found. Flagged false and Note "" both mean "nothing to add" —
// the same shape whether the sibling is clean or unreachable, since the two must not be told apart.
type Judgment struct {
	Flagged bool
	Note    string
}

// binPath resolves a sibling's binary: the env var overrides, otherwise PATH. Empty means
// unreachable, not an error — judgeSibling treats the two the same.
func binPath(envVar, name string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	p, _ := exec.LookPath(name)
	return p
}

// judgeSibling runs a sibling's own PreToolUse-shaped entry on the exact bytes the caller
// received — not a reimplementation of its scoring, the same binary, same input, called directly.
// Both cope-gate -pretool and basanite writecheck -no-dedup answer in the same shape
// (hookSpecificOutput.additionalContext), which is what makes one caller here work for both.
//
// Fails open in every direction: a missing binary, a timeout, or output that doesn't parse all
// mean "this sibling found nothing," never "block."
func judgeSibling(bin string, args []string, rawStdin []byte) Judgment {
	if bin == "" {
		return Judgment{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(rawStdin)
	out, err := cmd.Output()
	if err != nil {
		return Judgment{}
	}
	var resp struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &resp) != nil {
		return Judgment{}
	}
	note := strings.TrimSpace(resp.HookSpecificOutput.AdditionalContext)
	return Judgment{Flagged: note != "", Note: note}
}

// JudgeCope calls cope-gate -pretool. It writes no session state (pretool.go: "Per-write feedback
// repeats. That is the correct amount."), so a second caller here alongside cope's own registered
// hook lands on the same answer independently — no race.
func JudgeCope(rawStdin []byte) Judgment {
	return judgeSibling(binPath("TICKETVOICE_COPE_GATE", "cope-gate"), []string{"-pretool"}, rawStdin)
}

// JudgeBasanite calls basanite writecheck -no-dedup — the flag basanite added specifically for
// this caller (see CHANGELOG.md), because the plain writecheck path dedupes against a per-session
// seen-set file that a second, independent caller would otherwise race.
func JudgeBasanite(rawStdin []byte) Judgment {
	return judgeSibling(binPath("TICKETVOICE_BASANITE", "basanite"), []string{"writecheck", "-no-dedup"}, rawStdin)
}

// LinearPayload builds a synthetic PreToolUse-shaped payload carrying text under the same
// tool_name/field shape a real Linear MCP write would use, so JudgeCope/JudgeBasanite — which
// only understand that shape and a raw Bash command — have something they can score. For a
// caller that has the real body bytes already (gh-write) rather than a hook's own raw input,
// this is the bridge into the same two binaries the hook forwards to.
func LinearPayload(kind, text string) []byte {
	if strings.HasSuffix(kind, "comment") {
		b, _ := json.Marshal(map[string]any{
			"tool_name":  "mcp__linear__save_comment",
			"tool_input": map[string]string{"body": text},
		})
		return b
	}
	b, _ := json.Marshal(map[string]any{
		"tool_name":  "mcp__linear__save_issue",
		"tool_input": map[string]string{"description": text},
	})
	return b
}
