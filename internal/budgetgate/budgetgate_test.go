package budgetgate

import "testing"

func TestHeaderCountSkipsFencedHashes(t *testing.T) {
	if got := HeaderCount("## Real\n\n```sh\n# not a header\n```\n\n### Also real"); got != 2 {
		t.Fatalf("want 2 headers, got %d", got)
	}
}

// Classify is what keeps ticketvoice's hook-side match and gh-write's own backstop scoring the
// same body the same way — a drift here is a drift between the two enforcement points.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		object, verb, wantKind string
		wantBudget             int
	}{
		{"issue", "create", "issue description", IssueBudget},
		{"issue", "edit", "issue description", IssueBudget},
		{"pr", "create", "PR description", IssueBudget},
		{"pr", "edit", "PR description", IssueBudget},
		{"issue", "comment", "issue comment", CommentBudget},
		{"pr", "comment", "pr comment", CommentBudget},
	} {
		kind, budget := Classify(tc.object, tc.verb)
		if kind != tc.wantKind || budget != tc.wantBudget {
			t.Fatalf("Classify(%q, %q) = (%q, %d), want (%q, %d)", tc.object, tc.verb, kind, budget, tc.wantKind, tc.wantBudget)
		}
	}
}

// ViolationIDs is what internal/attemptstate compares attempt to attempt, so it has to survive
// both siblings' real tally shapes: cope's "id×count" (pretool.go, no space) and basanite's
// "word ×count" (main.go's writecheck output, one space).
func TestViolationIDs(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, note string
		want               []string
	}{
		{"empty note", "cope", "", nil},
		{"cope tally line, no space before ×", "cope",
			"description: 2 violation(s) — dangling_end×1 clause_symmetry×1", []string{"cope:clause_symmetry", "cope:dangling_end"}},
		{"basanite tally line, one space before ×", "basanite",
			"basanite — words you lean on:\n  substrate ×3 → layer\n  arm ×1 (no clean substitute)", []string{"basanite:arm", "basanite:substrate"}},
		{"note with no tally at all", "cope", "dangling_end: 1 violation(s)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ViolationIDs(tc.prefix, tc.note)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func TestAllViolationIDsMergesEveryGroup(t *testing.T) {
	got := AllViolationIDs([]string{"cope:dangling_end"}, []string{"impact:missing"}, nil, []string{"cite:ticket:ABC-999"})
	want := []string{"cite:ticket:ABC-999", "cope:dangling_end", "impact:missing"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestCombinedViolationIDsMergesAndSorts(t *testing.T) {
	cope := Judgment{Flagged: true, Note: "description: 1 violation(s) — dangling_end×1"}
	basanite := Judgment{Flagged: true, Note: "  substrate ×2 → layer"}
	got := CombinedViolationIDs(cope, basanite)
	want := []string{"basanite:substrate", "cope:dangling_end"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}
