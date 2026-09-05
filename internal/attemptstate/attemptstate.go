// Package attemptstate tracks how many times in a row the same session has had the same write
// denied, and what was flagged the last time, so ticketvoice can tell a rewrite that's converging
// from one that's stalled.
//
// A PreToolUse hook is a fresh process per call, so this can't live in memory: it's one small
// file per retry sequence, the same convention as cope's internal/state and basanite's
// internal/report.StateDir — $XDG_STATE_HOME/ticketvoice, or ~/.local/state/ticketvoice.
package attemptstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// validSessionID accepts the shapes Claude Code emits and rejects anything that could escape the
// state directory as a path component — same pattern basanite's own validSessionID uses.
var validSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString

var filenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// Dir resolves the state directory and creates it. TICKETVOICE_STATE_DIR overrides it outright —
// mainly so tests never touch the operator's real state directory; the default otherwise follows
// XDG_STATE_HOME, or ~/.local/state, matching basanite's report.StateDir.
func Dir() (string, error) {
	if v := os.Getenv("TICKETVOICE_STATE_DIR"); v != "" {
		return v, os.MkdirAll(v, 0o755)
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "ticketvoice")
	return dir, os.MkdirAll(dir, 0o755)
}

// Key names one retry sequence: the same session denying the same kind of write. Kind is carried
// separately from Tool because a Bash call's tool name is always "Bash" regardless of whether
// gh-write is posting an issue, a PR, or a comment — without it, unrelated gh-write invocations in
// the same session would share one counter.
//
// Anchor is whatever pre-existing id the write already carries — a Linear issue id, an issueId a
// comment attaches to, a gh-write target number — so two different tickets that happen to share a
// session, tool, and kind don't share a counter either. A fresh create has no id yet and Anchor is
// "", which puts every fresh create in one session+tool+kind bucket: unprotected, the same boundary
// case plancheck's own PlanHash-based reset has for a plan with nothing yet to hash.
type Key struct {
	SessionID string
	Tool      string
	Kind      string
	Anchor    string
}

func (k Key) valid() bool { return validSessionID(k.SessionID) && k.Tool != "" }

func slug(s string) string {
	if s = filenameUnsafe.ReplaceAllString(s, "_"); s != "" {
		return s
	}
	return "_"
}

func (k Key) fileName() string {
	return "attempts-" + k.SessionID + "-" + slug(k.Tool) + "-" + slug(k.Kind) + "-" + slug(k.Anchor) + ".json"
}

// Record is one retry sequence's state: how many times in a row this key has just been denied,
// and which rule or word ids were flagged the most recent time.
type Record struct {
	Attempts int      `json:"attempts"`
	Prior    []string `json:"prior,omitempty"`
}

// Load returns the stored record, or a zero Record when there is none, or when the file is
// missing, corrupt, or unreadable. This is advisory state: losing it costs one attempt of
// escalation, never the gate itself — the same fail-open posture cope's own session state takes.
func Load(k Key) Record {
	if !k.valid() {
		return Record{}
	}
	dir, err := Dir()
	if err != nil {
		return Record{}
	}
	raw, err := os.ReadFile(filepath.Join(dir, k.fileName()))
	if err != nil {
		return Record{}
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil {
		return Record{}
	}
	return r
}

// Save writes the record. A failure to write is silent — the next call on this key just starts
// over at attempt 1, the same outcome a corrupt or missing file produces on Load.
func Save(k Key, r Record) {
	if !k.valid() {
		return
	}
	dir, err := Dir()
	if err != nil {
		return
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, k.fileName()), raw, 0o644)
}

// Clear removes the record — called once a key stops being denied, whether the write finally
// cleared every check or the gate let it through after a stalled sequence. Either way, the next
// call on this key is a fresh attempt 1, not a continuation of the last one.
func Clear(k Key) {
	if !k.valid() {
		return
	}
	dir, err := Dir()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(dir, k.fileName()))
}
