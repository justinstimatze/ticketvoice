// Package impactline checks that a Linear issue description states its user-facing impact in
// plain language — the one thing a PM or exec reader needs that isn't already a native Linear
// field. Which project or initiative a ticket belongs to IS already structured Linear data
// (project/milestone), visible in Linear's own UI and readable directly by anything that wants
// it — a ticket restating that in prose would just duplicate what Linear already tracks. Impact
// has no such field, so it's the one thing worth requiring here.
//
// This never touches the network and never judges whether the stated impact is true — "is this
// actually true" is narrative verification, out of scope for a fast synchronous hook (see
// internal/citecheck's doc comment for the same boundary drawn the other way, on citations).
package impactline

import (
	"regexp"

	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
)

// impactLinePattern matches a line starting with "Impact:" followed by real content on that same
// line — the same shape as a "Something:" prefix line elsewhere in ordinary ticket prose, nothing
// fenced or structured about it. The gaps around "impact" and ":" are restricted to [ \t] rather
// than \s so they can't cross a newline and match content on a following line instead — an early
// draft used \s* here and "Impact:\n\nMore text." matched, which is wrong.
var impactLinePattern = regexp.MustCompile(`(?im)^[ \t]*impact[ \t]*:[ \t]*\S`)

const missingReason = `This issue description has no impact line. Add one, in plain language a PM or exec would understand:

Impact: users on the map page see load times drop from ~4s to under 1s.

or, for work nobody outside engineering would notice:

Impact: none — internal maintenance, no user-facing change.`

// Judge checks one issue description's text. Runs identically on a fresh create and an edit —
// nothing here depends on the issue already existing in Linear. Callers should not run this on
// comments; an "Impact:" line belongs to the ticket, not a reply on it.
func Judge(text string) budgetgate.Judgment {
	if impactLinePattern.MatchString(text) {
		return budgetgate.Judgment{}
	}
	return budgetgate.Judgment{Flagged: true, Note: missingReason}
}

// ViolationID is the one id this check ever contributes to a retry sequence's violation set —
// binary present/absent, not a tally, so it doesn't need budgetgate.ViolationIDs' ×N parsing.
const ViolationID = "impact:missing"
