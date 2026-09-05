package impactline

import "testing"

func TestJudgeFlagsMissingImpactLine(t *testing.T) {
	j := Judge("This is a normal ticket body with no impact statement at all, several sentences long.")
	if !j.Flagged {
		t.Fatal("want flagged when no Impact: line is present")
	}
}

func TestJudgeAcceptsUserFacingImpact(t *testing.T) {
	text := "The mechanism is X.\n\nImpact: users on the map page see load times drop from ~4s to under 1s.\n"
	if j := Judge(text); j.Flagged {
		t.Fatalf("want clean, got flagged: %s", j.Note)
	}
}

func TestJudgeAcceptsNoneForMaintenance(t *testing.T) {
	text := "Refactor internal logging.\n\nImpact: none — internal maintenance, no user-facing change.\n"
	if j := Judge(text); j.Flagged {
		t.Fatalf("want clean, got flagged: %s", j.Note)
	}
}

func TestJudgeRequiresContentAfterColon(t *testing.T) {
	text := "Some body text.\n\nImpact:\n\nMore text."
	if j := Judge(text); !j.Flagged {
		t.Fatal("want flagged when Impact: has no content following it")
	}
}

func TestJudgeIsCaseInsensitiveAndMidDocument(t *testing.T) {
	text := "para one\n\npara two\n\nIMPACT: dashboards load faster.\n\npara four"
	if j := Judge(text); j.Flagged {
		t.Fatalf("want clean regardless of case or position, got flagged: %s", j.Note)
	}
}
