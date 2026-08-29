// Command gh-write wraps `gh issue`/`gh pr` writes, forcing body text through stdin (a
// quoted heredoc, e.g. `gh-write issue create --title T <<'EOF' ... EOF`) instead of a
// --body or --body-file flag.
//
// The reason is ticketvoice, not gh-write itself. ticketvoice's PreToolUse hook gates
// prose against a word budget and forwards it to cope/basanite, but for a Bash call it
// only ever sees the raw command string, not a shell's parsed argv — an inline
// --body "..." is shell-quoted and not reliably extractable from that string (nested
// quotes, $() expansion, multi-paragraph text). A heredoc's content is literal text
// between two markers in that same string, which a plain string search finds without a
// shell tokenizer. gh-write is what makes that convention the only way to write a body,
// rather than a discipline someone has to remember on every call.
//
// Everything gh-write doesn't recognize is passed straight through to gh, so `--repo`,
// `--title`, `--label`, `--base`, `--draft`, and the rest work exactly as they do on gh
// itself.
//
// gh-write also runs the word-budget/cope/basanite gate itself (gateBody, below), on the real
// body bytes it just read off its own stdin — not a reimplementation of ticketvoice's hook, the
// same check via internal/budgetgate. ticketvoice's hook can only see a heredoc or a `< file`
// redirect in the Bash command string it's watching; a pipe-sourced body (`... | gh-write ...`)
// defeats that entirely. gh-write doesn't have that limit, since by the time it runs it already
// holds the actual bytes regardless of how they arrived — so this is the backstop that closes
// the gap the hook structurally can't, at the cost of a CLI failure instead of a human "ask".
//
// Every body gh-write sends is also prefixed with an agent tag (🤖) by default, since it's
// posted under the operator's own GitHub account — set TICKETVOICE_NO_AGENT_TAG to turn that off.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
)

var bodyFlags = map[string]bool{
	"--body": true, "-b": true, "--body-file": true, "-F": true,
}

func rejectsBody(arg string) bool {
	return bodyFlags[arg] || strings.HasPrefix(arg, "--body=") || strings.HasPrefix(arg, "--body-file=")
}

// validate checks the object/verb/flags shape and returns the gh args to run (object, verb,
// and whatever's left of args), or a usage error to print instead. Split out of run so the
// exec/exit-code plumbing there isn't tangled up with argument checking.
func validate(args []string) (ghArgs []string, usageErr string) {
	if len(args) < 2 {
		return nil, "usage: gh-write <issue|pr> <create|comment|edit> [id] [gh flags...]"
	}
	object, verb := args[0], args[1]
	if object != "issue" && object != "pr" {
		return nil, fmt.Sprintf("gh-write: unsupported object %q (want issue or pr)", object)
	}
	if verb != "create" && verb != "comment" && verb != "edit" {
		return nil, fmt.Sprintf("gh-write: unsupported verb %q (want create, comment, or edit)", verb)
	}
	for _, a := range args[2:] {
		if rejectsBody(a) {
			return nil, fmt.Sprintf("gh-write: %s is not accepted — pipe or heredoc the body on stdin instead, so it lands in the Bash command text ticketvoice reads", a)
		}
	}
	return args, ""
}

// gateBody runs the same word-budget and sibling-scorer check ticketvoice's own PreToolUse hook
// runs, against the real body bytes gh-write just read off its own stdin — the one thing a hook
// watching the Bash command string can never see regardless of whether that body arrived as a
// heredoc, a `< file` redirect, or a pipe. Empty reason means clean.
func gateBody(object, verb, text string) (blocked bool, reason string) {
	kind, budget := budgetgate.Classify(object, verb)
	budget = budgetgate.BudgetFor(budget)
	over, budgetReason := budgetgate.Evaluate(text, kind, budget)

	payload := budgetgate.LinearPayload(kind, text)
	cope := budgetgate.JudgeCope(payload)
	basanite := budgetgate.JudgeBasanite(payload)
	if !over && !cope.Flagged && !basanite.Flagged {
		return false, ""
	}

	if over {
		reason = budgetReason
	} else {
		reason = fmt.Sprintf("gh-write: this %s is inside the %d-word budget, but a sibling scorer flagged it.", kind, budget)
	}
	if cope.Flagged {
		reason += fmt.Sprintf("\n\ncope flagged this:\n\n%s", cope.Note)
	}
	if basanite.Flagged {
		reason += fmt.Sprintf("\n\nbasanite flagged this:\n\n%s", basanite.Note)
	}
	reason += "\n\nCut it and call gh-write again with less."
	return true, reason
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ghArgs, usageErr := validate(args)
	if usageErr != "" {
		fmt.Fprintln(stderr, usageErr)
		return 2
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "gh-write: reading stdin:", err)
		return 1
	}
	text := string(body)

	if blocked, reason := gateBody(ghArgs[0], ghArgs[1], text); blocked {
		fmt.Fprintln(stderr, reason)
		return 1
	}

	if budgetgate.AgentTagEnabled() {
		text = budgetgate.AgentTag + text
	}

	cmd := exec.Command("gh", append(ghArgs, "--body-file", "-")...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(stderr, "gh-write:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
