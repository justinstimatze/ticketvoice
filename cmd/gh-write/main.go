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
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var bodyFlags = map[string]bool{
	"--body": true, "-b": true, "--body-file": true, "-F": true,
}

func rejectsBody(arg string) bool {
	return bodyFlags[arg] || strings.HasPrefix(arg, "--body=") || strings.HasPrefix(arg, "--body-file=")
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: gh-write <issue|pr> <create|comment|edit> [id] [gh flags...]")
		return 2
	}
	object, verb := args[0], args[1]
	if object != "issue" && object != "pr" {
		fmt.Fprintf(stderr, "gh-write: unsupported object %q (want issue or pr)\n", object)
		return 2
	}
	switch verb {
	case "create", "comment", "edit":
	default:
		fmt.Fprintf(stderr, "gh-write: unsupported verb %q (want create, comment, or edit)\n", verb)
		return 2
	}

	rest := args[2:]
	for _, a := range rest {
		if rejectsBody(a) {
			fmt.Fprintf(stderr, "gh-write: %s is not accepted — pipe or heredoc the body on stdin instead, so it lands in the Bash command text ticketvoice reads\n", a)
			return 2
		}
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "gh-write: reading stdin:", err)
		return 1
	}

	ghArgs := append([]string{object, verb}, rest...)
	ghArgs = append(ghArgs, "--body-file", "-")

	cmd := exec.Command("gh", ghArgs...)
	cmd.Stdin = bytes.NewReader(body)
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
