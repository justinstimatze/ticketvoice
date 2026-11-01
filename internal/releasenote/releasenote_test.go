package releasenote

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stored = `## See when each source was published

(Alpha)
Each source shows its date.
A page with no date says so.

## Smoother attachments

(Alpha)
Attachments got easier.
Fixes & Minor Updates
A file you attach also appears in My files.
* The sign-in code field shows a placeholder.

---

## For the editor (not for publishing)
**Owners to confirm each entry:** ` + "x " + `
`

func TestEntriesReadsStoriesAndMinorLinesAsLinearStoresThem(t *testing.T) {
	got := Entries(stored)
	want := []Entry{
		{Title: "See when each source was published", Text: "Each source shows its date. A page with no date says so."},
		{Title: "Smoother attachments", Text: "A file you attach also appears in My files.", Minor: true},
		{Title: "Smoother attachments", Text: "The sign-in code field shows a placeholder.", Minor: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBudgetFlagsTheOneStoryThatOutgrowsTheOthers(t *testing.T) {
	long := strings.Repeat("word ", StoryBudget+1)
	flags := BudgetFlags([]Entry{
		{Title: "Short", Text: "Each source shows its date."},
		{Title: "Long", Text: long},
		{Title: "x", Text: strings.Repeat("w ", MinorBudget+1), Minor: true},
	})
	if len(flags) != 2 || !strings.Contains(flags[0], `story "Long" is 91 words against 90`) || !strings.Contains(flags[1], "minor line") {
		t.Fatalf("flags = %q", flags)
	}
}

func writeNote(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckPassesTheEmbeddedCardAndNamesAScorerThatCouldNotRun(t *testing.T) {
	var copeArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		if name == "cope-gate" {
			copeArgs = args
			card, err := os.ReadFile(args[1])
			if err != nil || string(card) != string(Card) {
				t.Errorf("cope-gate was not handed the embedded card: %v", err)
			}
			return []byte(`{"path":"x","violations":[{"rule_id":"praise","matched":"powerful","why":"praise"}]}` + "\n"), nil
		}
		return nil, exec.ErrNotFound
	}
	res, err := Check(writeNote(t, stored), run)
	if err != nil {
		t.Fatal(err)
	}
	if copeArgs[0] != "-rules" || copeArgs[2] != "-check" {
		t.Errorf("cope-gate args = %q", copeArgs)
	}
	if len(res.Flags) != 1 || !strings.Contains(res.Flags[0], `cope praise: "powerful"`) {
		t.Errorf("flags = %q", res.Flags)
	}
	if len(res.NotRun) != 1 || res.NotRun[0] != "basanite (not installed)" {
		t.Errorf("notRun = %q", res.NotRun)
	}
}

func TestCheckNeverReadsAFailedCopeAsClean(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name == "cope-gate" {
			return []byte(`{"error":"open x: no such file"}`), nil
		}
		return []byte("basanite check: x — clean, nothing flagged\n"), nil
	}
	res, err := Check(writeNote(t, stored), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotRun) != 1 || !strings.HasPrefix(res.NotRun[0], "cope-gate") || len(res.Flags) != 0 {
		t.Errorf("res = %+v", res)
	}
}

// The card is real input to a real binary: if cope-gate is installed, it must load the card and
// flag what the card exists to flag.
func TestTheEmbeddedCardLoadsInCopeAndFlagsPraise(t *testing.T) {
	if _, err := exec.LookPath("cope-gate"); err != nil {
		t.Skip("cope-gate not installed")
	}
	res, err := Check(writeNote(t, "## New\n\n(Alpha)\nWe are excited to ship this powerful feature!\nIt works.\n"), ExecRunner)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Flags, "\n")
	for _, id := range []string{"praise", "team_feeling", "exclamation"} {
		if !strings.Contains(joined, "cope "+id) {
			t.Errorf("missing %s in %q (notRun %q)", id, joined, res.NotRun)
		}
	}
}
