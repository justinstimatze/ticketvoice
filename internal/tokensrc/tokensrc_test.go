package tokensrc

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateHome points os.UserHomeDir() (env var HOME on unix) at an empty temp dir, so a test
// asserting "nothing resolves" isn't silently made true or false by whatever this developer's
// actual ~/.config/ticketvoice/.env happens to contain.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestResolveEnvVarWins(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "from-env")
	if got := Resolve("", "TESTSRC_TOKEN"); got != "from-env" {
		t.Fatalf("want the env var value, got %q", got)
	}
}

func TestResolveReturnsEmptyWhenNothingSet(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "")
	if got := Resolve("", "TESTSRC_TOKEN"); got != "" {
		t.Fatalf("want empty with nothing set anywhere, got %q", got)
	}
}

func TestResolveStripsQuotes(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte(`TESTSRC_TOKEN="quoted-value"`+"\n"), 0o600)
	if got := Resolve(dir, "TESTSRC_TOKEN"); got != "quoted-value" {
		t.Fatalf("want quotes stripped, got %q", got)
	}
}

// The global ~/.config/ticketvoice/.env fallback is what lets the same credential resolve no
// matter which project's cwd a hook is currently handling a call for.
func TestResolveFallsBackToGlobalConfigFile(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "")
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".config", "ticketvoice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`TESTSRC_TOKEN="from-global"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Resolve(filepath.Join(t.TempDir(), "some", "project", "subdir"), "TESTSRC_TOKEN")
	if got != "from-global" {
		t.Fatalf("want the global config file to resolve from an unrelated cwd, got %q", got)
	}
}

// A .env found by walking up from cwd takes priority over the global fallback — a project-local
// override should win where one exists.
func TestResolvePrefersCwdEnvOverGlobalConfigFile(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "")
	home := os.Getenv("HOME")
	globalDir := filepath.Join(home, ".config", "ticketvoice")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(globalDir, ".env"), []byte("TESTSRC_TOKEN=global\n"), 0o600)

	project := t.TempDir()
	sub := filepath.Join(project, "sub", "dir")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(project, ".env"), []byte("TESTSRC_TOKEN=local\n"), 0o600)

	got := Resolve(sub, "TESTSRC_TOKEN")
	if got != "local" {
		t.Fatalf("want the project-local .env to win, got %q", got)
	}
}

// A different env-var name must resolve independently — proves Resolve is generic, not hardcoded
// to one credential.
func TestResolveIsGenericOverEnvVarName(t *testing.T) {
	isolateHome(t)
	t.Setenv("TESTSRC_TOKEN", "")
	t.Setenv("TESTSRC_OTHER", "other-value")
	if got := Resolve("", "TESTSRC_OTHER"); got != "other-value" {
		t.Fatalf("want the other env var to resolve independently, got %q", got)
	}
	if got := Resolve("", "TESTSRC_TOKEN"); got != "" {
		t.Fatalf("want TESTSRC_TOKEN to stay empty, got %q", got)
	}
}
