package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
)

// noSiblings points cope/basanite at a path that can't exist, so gateBody fails open and every
// test not specifically exercising the gate isn't at the mercy of whatever's on this machine's
// PATH.
func noSiblings(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("TICKETVOICE_COPE_GATE", missing)
	t.Setenv("TICKETVOICE_BASANITE", missing)
}

// fakeSiblingBinary writes a stand-in for cope-gate or basanite that answers the way the real
// one does — see main_test.go's identical helper in the ticketvoice package; duplicated here
// because the two are separate `package main`s with no shared test-support package to hold it.
func fakeSiblingBinary(t *testing.T, name, additionalContext string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	body := "#!/bin/sh\n"
	if additionalContext != "" {
		var resp struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		resp.HookSpecificOutput.HookEventName = "PreToolUse"
		resp.HookSpecificOutput.AdditionalContext = additionalContext
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		payload := filepath.Join(dir, "payload.json")
		if err := os.WriteFile(payload, data, 0o644); err != nil {
			t.Fatal(err)
		}
		body += "cat \"$(dirname \"$0\")/payload.json\"\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestRunRejectsUnsupportedObject(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"discussion", "create"}, strings.NewReader(""), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "unsupported object") {
		t.Fatalf("want exit 2 and an unsupported-object message, got code=%d stderr=%q", code, errb.String())
	}
}

func TestRunRejectsUnsupportedVerb(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"issue", "close"}, strings.NewReader(""), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "unsupported verb") {
		t.Fatalf("want exit 2 and an unsupported-verb message, got code=%d stderr=%q", code, errb.String())
	}
}

// The whole point of gh-write is that a body never reaches it as a flag — see the package
// doc. --body, -b, --body-file, -F and their --flag=value forms must all be refused.
func TestRunRejectsBodyFlags(t *testing.T) {
	for _, flag := range []string{"--body", "-b", "--body-file", "-F", "--body=hi", "--body-file=notes.md"} {
		t.Run(flag, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run([]string{"issue", "create", "--title", "T", flag, "x"}, strings.NewReader(""), &out, &errb)
			if code != 2 || !strings.Contains(errb.String(), "not accepted") {
				t.Fatalf("want exit 2 and a rejection message for %s, got code=%d stderr=%q", flag, code, errb.String())
			}
		})
	}
}

func TestRunTooFewArgs(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"issue"}, strings.NewReader(""), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("want exit 2 and a usage message, got code=%d stderr=%q", code, errb.String())
	}
}

// Builds a fake `gh` on PATH that just echoes its argv and stdin, so the real gh binary
// (or network) is never involved: this checks what gh-write invokes gh WITH, not what gh
// does with it.
func fakeGhOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'ARGS:%s\\n' \"$*\"\nprintf 'STDIN:'\ncat\n"
	path := filepath.Join(dir, "gh")
	if runtime.GOOS == "windows" {
		t.Skip("fake gh shell script is POSIX-only")
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunForwardsFlagsAndStdinToGh(t *testing.T) {
	noSiblings(t)
	fakeGhOnPath(t)
	var out, errb bytes.Buffer
	code := run([]string{"issue", "create", "--title", "Bug: X", "--repo", "wovim/wovim"},
		strings.NewReader("body text here"), &out, &errb)
	if code != 0 {
		t.Fatalf("want exit 0, got %d stderr=%q", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "ARGS:issue create --title Bug: X --repo wovim/wovim --body-file -") {
		t.Fatalf("gh-write did not forward the expected args: %q", got)
	}
	if !strings.Contains(got, "STDIN:"+budgetgate.AgentTag+"body text here") {
		t.Fatalf("gh-write did not tag and forward stdin: %q", got)
	}
}

func TestRunForwardsPositionalIDForCommentAndEdit(t *testing.T) {
	noSiblings(t)
	fakeGhOnPath(t)
	var out, errb bytes.Buffer
	code := run([]string{"pr", "comment", "42"}, strings.NewReader("lgtm"), &out, &errb)
	if code != 0 {
		t.Fatalf("want exit 0, got %d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "ARGS:pr comment 42 --body-file -") {
		t.Fatalf("gh-write did not forward the comment id positionally: %q", out.String())
	}
}

func words(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }

// The whole point of the backstop (see the package doc) is that it works no matter how the body
// reached gh-write's stdin — the caller here doesn't matter, only the bytes that arrived.
func TestRunRefusesOverBudgetBody(t *testing.T) {
	noSiblings(t)
	fakeGhOnPath(t)
	var out, errb bytes.Buffer
	code := run([]string{"issue", "create", "--title", "T"}, strings.NewReader(words(200)), &out, &errb)
	if code == 0 {
		t.Fatalf("over-budget body must not exit 0")
	}
	if strings.Contains(out.String(), "ARGS:") {
		t.Fatalf("gh must never be invoked for a refused body, got stdout %q", out.String())
	}
	if !strings.Contains(errb.String(), "200 words") {
		t.Fatalf("stderr must name the overage: %q", errb.String())
	}
}

func TestRunRefusesWhenCopeFlagsAnUnderBudgetBody(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "clause_symmetry: 1 violation(s)"))
	t.Setenv("TICKETVOICE_BASANITE", filepath.Join(t.TempDir(), "does-not-exist"))
	fakeGhOnPath(t)
	var out, errb bytes.Buffer
	code := run([]string{"issue", "create", "--title", "T"}, strings.NewReader(words(20)), &out, &errb)
	if code == 0 {
		t.Fatalf("cope-flagged body must not exit 0")
	}
	if strings.Contains(out.String(), "ARGS:") {
		t.Fatalf("gh must never be invoked for a refused body, got stdout %q", out.String())
	}
	if !strings.Contains(errb.String(), "clause_symmetry") {
		t.Fatalf("stderr must carry cope's finding: %q", errb.String())
	}
}

// The tag is on by default and off only when the operator explicitly says so.
func TestRunOmitsAgentTagWhenDisabled(t *testing.T) {
	noSiblings(t)
	fakeGhOnPath(t)
	t.Setenv("TICKETVOICE_NO_AGENT_TAG", "1")
	var out, errb bytes.Buffer
	code := run([]string{"issue", "create", "--title", "T"}, strings.NewReader("body text here"), &out, &errb)
	if code != 0 {
		t.Fatalf("want exit 0, got %d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "STDIN:body text here") {
		t.Fatalf("TICKETVOICE_NO_AGENT_TAG must suppress the tag: %q", out.String())
	}
}
