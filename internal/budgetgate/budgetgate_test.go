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
