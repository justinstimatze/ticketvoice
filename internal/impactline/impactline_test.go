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

// The agent tag must not hide the impact line. budgetgate prefixes agent-written descriptions with
// "🤖 ", so leading with the impact — what missingReason itself asks for — produced "🤖 Impact: ..."
// and this check called it absent. Measured 2026-09-14: three rejections in a row on descriptions
// that each carried one.
func TestImpactLineSurvivesTheAgentTag(t *testing.T) {
	for _, body := range []string{
		"🤖 Impact: users on the map page see load times drop from ~4s to under 1s.",
		"🤖\n\nImpact: none — internal maintenance, no user-facing change.",
		"🤖 🤖 Impact: a doubled tag is still a tagged body.",
	} {
		if j := Judge(body); j.Flagged {
			t.Errorf("want not flagged, got flagged for %q", body)
		}
	}
}

// Position is not the requirement — that the impact is stated is. A trailing line is as good as a
// leading one, which is how most of this repo's own older tickets are written.
func TestImpactLineAnywhereInTheBody(t *testing.T) {
	body := "Mechanism: the thing broke.\n\nExposed: file.go:12\n\nImpact: nobody outside engineering notices."
	if j := Judge(body); j.Flagged {
		t.Error("want not flagged for a trailing impact line")
	}
}

// Still absent is still flagged — the loosening above must not turn this into a check that passes
// on anything.
func TestBareTagIsNotAnImpactLine(t *testing.T) {
	if j := Judge("🤖 Mechanism: something broke and nobody said what it costs."); !j.Flagged {
		t.Error("want flagged: a tagged body with no impact line")
	}
}
