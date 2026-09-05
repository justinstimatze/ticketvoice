// Package tokensrc resolves a credential the same way for every caller in this codebase: an env
// var first, then a .env file found by walking up from cwd, then a global
// ~/.config/ticketvoice/.env — the global fallback is what lets a hook wired into every project's
// settings.json resolve a credential regardless of which project's cwd it's currently handling a
// call for. Extracted out of internal/linearclient once a second caller (internal/autorewrite)
// needed the identical chain for a different env var name.
package tokensrc

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Resolve returns the first non-empty match for envVar: the environment itself, then a .env found
// by walking up from cwd, then ~/.config/ticketvoice/.env. Returns "" when none is found anywhere.
func Resolve(cwd, envVar string) string {
	if v := os.Getenv(envVar); v != "" {
		return stripQuotes(v)
	}
	for dir := cwd; dir != ""; {
		if v := readEnvFrom(filepath.Join(dir, ".env"), envVar); v != "" {
			return v
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		if v := readEnvFrom(filepath.Join(home, ".config", "ticketvoice", ".env"), envVar); v != "" {
			return v
		}
	}
	return ""
}

func readEnvFrom(path, envVar string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := strings.CutPrefix(line, envVar+"="); ok {
			return stripQuotes(strings.TrimSpace(v))
		}
	}
	return ""
}

// stripQuotes lets `VAR="value"` work the same as an unquoted value.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
