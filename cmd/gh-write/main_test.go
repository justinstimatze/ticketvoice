package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	if !strings.Contains(got, "STDIN:body text here") {
		t.Fatalf("gh-write did not forward stdin unchanged: %q", got)
	}
}

func TestRunForwardsPositionalIDForCommentAndEdit(t *testing.T) {
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
