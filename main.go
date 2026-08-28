// ticketvoice is a Claude Code PreToolUse hook that holds Linear ticket prose to a word budget.
//
// It exists because the register note alone did not hold. A memory saying "write like a pragmatic
// staff engineer" was in force on 2026-07-20 and a 238-word ticket body still shipped on 2026-08-04,
// because a register is a taste and not a limit. A word count is a limit, and it can fail.
//
// Silent when the body is inside the budget. Over budget it returns permissionDecision "ask", so the
// human decides — a ticket that genuinely needs to be long stays possible, and the assistant cannot
// wave its own gate through.
//
// Two sibling tools watch the same Linear writes — basanite (vocabulary tics) and cope (voicing and
// structure) — and both, independently, chose never to block on their own: additionalContext only.
// Word count alone left a real hole: a within-budget ticket carrying a flagged tic shipped with
// nobody forced to look. This hook closes it by calling both directly, forwarding the exact bytes
// it received to cope-gate -pretool and basanite writecheck -no-dedup and gating on their verdicts
// too, not just the budget — see judgeCope and judgeBasanite. See CHANGELOG.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

// buildVersion resolves the version from ldflags, then the module's own build info, then vcs
// metadata, then "dev". See the go-cli-versioning note: the git tag is the source of truth and a
// hand-maintained const drifts from it.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		return rev + "-dirty"
	}
	return rev
}

// Budgets in words of prose, fenced code excluded. An issue body carries a mechanism, its evidence,
// the exposure and the fix; a comment carries one of those. Override with TICKETVOICE_MAX_WORDS.
const (
	defaultIssueBudget   = 150
	defaultCommentBudget = 120
	// Below this, section headers cost more lines than the structure they buy.
	headerFloor = 200
)

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type linearInput struct {
	Description string `json:"description"`
	Body        string `json:"body"`
	Patch       []struct {
		NewString string `json:"new_string"`
		Text      string `json:"text"`
	} `json:"patch"`
}

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

var (
	fencedCode = regexp.MustCompile("(?s)```.*?```")
	wordish    = regexp.MustCompile(`\p{L}`)
	headerLine = regexp.MustCompile(`(?m)^#{1,6}\s+\S`)
)

// proseWords counts words outside fenced code blocks. Code is the part of a ticket that should be
// long; only the prose around it is being budgeted.
func proseWords(s string) int {
	n := 0
	for _, f := range strings.Fields(fencedCode.ReplaceAllString(s, " ")) {
		if wordish.MatchString(f) {
			n++
		}
	}
	return n
}

func headerCount(s string) int {
	return len(headerLine.FindAllString(fencedCode.ReplaceAllString(s, " "), -1))
}

// prose pulls the human-readable field for the tool being called, and the budget that applies to it.
// A patch is counted on its inserted text only: the rest of the body is not being rewritten.
func prose(tool string, raw json.RawMessage) (text string, budget int, kind string) {
	var in linearInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", 0, ""
	}
	switch tool {
	case "mcp__linear__save_issue":
		if in.Description != "" {
			return in.Description, defaultIssueBudget, "issue description"
		}
		var b strings.Builder
		for _, p := range in.Patch {
			b.WriteString(p.NewString + " " + p.Text + " ")
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s, defaultIssueBudget, "issue description patch"
		}
	case "mcp__linear__save_comment":
		if in.Body != "" {
			return in.Body, defaultCommentBudget, "comment"
		}
	}
	return "", 0, ""
}

const slots = `Four slots, in this order:
  1. The mechanism, one paragraph — what is broken, and why nothing catches it.
  2. Evidence it is real — a SHA, a log line, a failing assertion. One sentence.
  3. What is still exposed — file:line, not a description of the file.
  4. The fix, as a code block, plus one line on how to prove it can go red.
SHAs and file:line carry the detail; do not narrate what the reader can open.`

// budgetFor applies the TICKETVOICE_MAX_WORDS override, shared by the hook path and --check.
func budgetFor(base int) int {
	if v := os.Getenv("TICKETVOICE_MAX_WORDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return base
}

// evaluate is the one gate both the hook and `--check` run through: same budget math, same reason
// text. over is false and reason is empty when the text is within budget.
func evaluate(text, kind string, budget int) (over bool, reason string) {
	words := proseWords(text)
	if words <= budget {
		return false, ""
	}
	reason = fmt.Sprintf("This %s is %d words of prose against a %d-word budget — %d over.\n\n%s",
		kind, words, budget, words-budget, slots)
	if h := headerCount(text); h > 0 && words < headerFloor {
		reason += fmt.Sprintf("\n\nIt also carries %d section header(s) under %d words, which cost two lines each and imply more document than there is.",
			h, headerFloor)
	}
	reason += "\n\nCut it and call again, or approve to send as written."
	return true, reason
}

// judgment is what a sibling scorer found. flagged false and note "" both mean "nothing to add" —
// the same shape whether the sibling is clean or unreachable, since the two must not be told apart.
type judgment struct {
	flagged bool
	note    string
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

// judgeSibling runs a sibling's own PreToolUse-shaped entry on the exact bytes this hook
// received — not a reimplementation of its scoring, the same binary, same input, called directly.
// Both cope-gate -pretool and basanite writecheck -no-dedup answer in the same shape
// (hookSpecificOutput.additionalContext), which is what makes one caller here work for both.
//
// Fails open in every direction: a missing binary, a timeout, or output that doesn't parse all mean
// "this sibling found nothing," never "block."
func judgeSibling(bin string, args []string, rawStdin []byte) judgment {
	if bin == "" {
		return judgment{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(rawStdin)
	out, err := cmd.Output()
	if err != nil {
		return judgment{}
	}
	var resp struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &resp) != nil {
		return judgment{}
	}
	note := strings.TrimSpace(resp.HookSpecificOutput.AdditionalContext)
	return judgment{flagged: note != "", note: note}
}

// judgeCope calls cope-gate -pretool. It writes no session state (pretool.go: "Per-write feedback
// repeats. That is the correct amount."), so a second caller here alongside cope's own registered
// hook lands on the same answer independently — no race.
func judgeCope(rawStdin []byte) judgment {
	return judgeSibling(binPath("TICKETVOICE_COPE_GATE", "cope-gate"), []string{"-pretool"}, rawStdin)
}

// judgeBasanite calls basanite writecheck -no-dedup — the flag basanite added specifically for
// this caller (see CHANGELOG.md), because the plain writecheck path dedupes against a per-session
// seen-set file that a second, independent caller would otherwise race.
func judgeBasanite(rawStdin []byte) judgment {
	return judgeSibling(binPath("TICKETVOICE_BASANITE", "basanite"), []string{"writecheck", "-no-dedup"}, rawStdin)
}

// runHookWithInput is the hook's decision logic, taking the raw bytes so the same bytes can be
// forwarded to the siblings unmodified and so this runs without a subprocess in tests. Returns nil
// when the call should proceed silently.
func runHookWithInput(raw []byte) *hookOutput {
	var in hookInput
	// A hook that cannot parse its input must not block the call it was watching.
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	text, rawBudget, kind := prose(in.ToolName, in.ToolInput)
	if text == "" {
		return nil
	}
	budget := budgetFor(rawBudget)
	over, budgetReason := evaluate(text, kind, budget)

	cope := judgeCope(raw)
	basanite := judgeBasanite(raw)
	if !over && !cope.flagged && !basanite.flagged {
		return nil
	}

	var reason string
	if over {
		reason = budgetReason
	} else {
		reason = fmt.Sprintf("This %s is inside the %d-word budget, but a sibling scorer flagged it on the way out.", kind, budget)
	}
	if cope.flagged {
		reason += fmt.Sprintf("\n\ncope flagged this:\n\n%s", cope.note)
	}
	if basanite.flagged {
		reason += fmt.Sprintf("\n\nbasanite flagged this:\n\n%s", basanite.note)
	}
	if !over {
		reason += "\n\nCut it and call again, or approve to send as written."
	}

	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "ask"
	out.HookSpecificOutput.PermissionDecisionReason = reason
	return &out
}

func runHook() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	out := runHookWithInput(raw)
	if out == nil {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// runCheck runs prose through the same gate as the hook, outside a tool call — for gating this
// project's own docs the way cope gates README.md through cope-gate and effigy writes its README
// through its own Layer 2. ticketvoice doesn't generate prose, so the analogue is narrower: check a
// slice of prose against the same budget a Linear write would face, as if it were a ticket entry.
func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	field := fs.String("field", "issue", `budget to check against: "issue" or "comment"`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ticketvoice --check <file|-> [-field issue|comment]")
		return 2
	}

	kind, budget := "issue description", defaultIssueBudget
	if *field == "comment" {
		kind, budget = "comment", defaultCommentBudget
	}
	budget = budgetFor(budget)

	path := fs.Arg(0)
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	over, reason := evaluate(string(data), kind, budget)
	if !over {
		fmt.Printf("%s: %d words against a %d-word %s budget — within budget.\n", path, proseWords(string(data)), budget, kind)
		return 0
	}
	fmt.Println(reason)
	return 1
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version":
			fmt.Println(buildVersion())
			return
		case "--check":
			os.Exit(runCheck(os.Args[2:]))
		}
	}
	runHook()
}
