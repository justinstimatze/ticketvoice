// ticketvoice is a Claude Code PreToolUse hook that holds Linear ticket prose to a word budget.
//
// It exists because the register note alone did not hold. A memory saying "write like a pragmatic
// staff engineer" was in force on 2026-07-20 and a 283-word ticket body still shipped on 2026-08-04,
// because a register is a taste and not a limit. A word count is a limit, and it can fail.
//
// Silent when the body is inside the budget. Over budget it returns permissionDecision "ask", so the
// human decides — a ticket that genuinely needs to be long stays possible, and the assistant cannot
// wave its own gate through.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
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

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println(buildVersion())
		return
	}

	var in hookInput
	// A hook that cannot parse its input must not block the call it was watching.
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return
	}
	text, budget, kind := prose(in.ToolName, in.ToolInput)
	if text == "" {
		return
	}
	if v := os.Getenv("TICKETVOICE_MAX_WORDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = n
		}
	}

	words := proseWords(text)
	if words <= budget {
		return
	}

	reason := fmt.Sprintf("This %s is %d words of prose against a %d-word budget — %d over.\n\n%s",
		kind, words, budget, words-budget, slots)
	if h := headerCount(text); h > 0 && words < headerFloor {
		reason += fmt.Sprintf("\n\nIt also carries %d section header(s) under %d words, which cost two lines each and imply more document than there is.",
			h, headerFloor)
	}
	reason += "\n\nCut it and call again, or approve to send as written."

	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "ask"
	out.HookSpecificOutput.PermissionDecisionReason = reason
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
