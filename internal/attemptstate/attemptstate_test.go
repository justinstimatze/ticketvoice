package attemptstate

import "testing"

// Two tickets that share a session, tool, and kind must not share a file just because Anchor is
// the one field that differs — that's the whole reason Anchor exists.
func TestKeyFileNameDistinguishesAnchors(t *testing.T) {
	a := Key{SessionID: "s", Tool: "mcp__linear__save_comment", Kind: "comment", Anchor: "ABC-1"}
	b := Key{SessionID: "s", Tool: "mcp__linear__save_comment", Kind: "comment", Anchor: "ABC-2"}
	if a.fileName() == b.fileName() {
		t.Fatalf("different anchors must not collide: %q", a.fileName())
	}
}

func TestLoadSaveClearRoundTrip(t *testing.T) {
	t.Setenv("TICKETVOICE_STATE_DIR", t.TempDir())
	k := Key{SessionID: "s1", Tool: "mcp__linear__save_issue", Kind: "issue description", Anchor: "ABC-9"}

	if r := Load(k); r.Attempts != 0 || r.Prior != nil {
		t.Fatalf("want a zero record for a fresh key, got %+v", r)
	}

	Save(k, Record{Attempts: 2, Prior: []string{"cope:dangling_end"}})
	got := Load(k)
	if got.Attempts != 2 || len(got.Prior) != 1 || got.Prior[0] != "cope:dangling_end" {
		t.Fatalf("want the saved record back, got %+v", got)
	}

	Clear(k)
	if r := Load(k); r.Attempts != 0 {
		t.Fatalf("want a cleared key to read as fresh, got %+v", r)
	}
}

// A session id that could escape the state directory as a path component must never persist —
// same convention basanite's own validSessionID enforces.
func TestInvalidKeyIsANoOp(t *testing.T) {
	t.Setenv("TICKETVOICE_STATE_DIR", t.TempDir())
	k := Key{SessionID: "../evil", Tool: "mcp__linear__save_issue"}
	Save(k, Record{Attempts: 5})
	if r := Load(k); r.Attempts != 0 {
		t.Fatalf("an invalid session id must never persist, got %+v", r)
	}
}
