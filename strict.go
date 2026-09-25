package main

// linear-strict (github.com/justinstimatze/linear-strict) writes a ticket as named description
// sections patched one at a time, plus typed comments. Its write tools carry prose in a different
// shape from the official server's: set_state {issue, patch: [{section, body}]}, comment {issue,
// body, patch}, create_issue {sections: [{section, body}]}. A section is judged as that section,
// against its own budget and advice, and a rewrite goes back into the section it came from. A
// comment's own body is judged as a comment. set_status carries no prose and is left alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/ticketvoice/internal/attemptstate"
	"github.com/justinstimatze/ticketvoice/internal/autorewrite"
	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
	"github.com/justinstimatze/ticketvoice/internal/citecheck"
	"github.com/justinstimatze/ticketvoice/internal/linearclient"
)

var strictTools = map[string]string{"set_state": "patch", "comment": "patch", "create_issue": "sections"}

// strictTool reports the linear-strict tool a call names. "comment" and "set_state" are generic
// words, so the server segment must also say linear.
func strictTool(tool string) (name string, ok bool) {
	rest, found := strings.CutPrefix(tool, "mcp__")
	if !found {
		return "", false
	}
	i := strings.LastIndex(rest, "__")
	if i < 0 {
		return "", false
	}
	name = rest[i+2:]
	_, known := strictTools[name]
	return name, known && strings.Contains(strings.ToLower(rest[:i]), "linear")
}

// Evidence sections are lists of short structured lines, and a ticket's evidence grows as it
// lands, so the section as a whole has no budget. Each line has one instead, since a line that runs
// past it has started narrating.
const strictLineBudget = 40

// strictCommentBudget is higher than CommentBudget: a strict comment is the log entry for evidence
// whose detail already lives in the description. On the canary, drafts cut to fit 120 kept every
// citation but cost a retry on one comment in ten (2026-09-25).
const strictCommentBudget = 150

type sectionRule struct {
	lines    bool // judged line by line against strictLineBudget
	ticks    bool // a ticked item's text and its citation are judged apart
	guidance string
}

var sectionRules = map[string]sectionRule{
	"observed":       {lines: true, guidance: `One line per observation: YYYY-MM-DD · source · result. The result is what was seen, in a clause. What it means belongs under Cause, and a second observation gets its own line.`},
	"done when":      {lines: true, ticks: true, guidance: `One checkable item per line: the command or observation that will show it is done. Why it matters belongs under Cause or Fix.`},
	"open questions": {lines: true, guidance: `One question per line, each one a single person can answer.`},
	"cause":          {guidance: `One paragraph: what is broken, and why nothing catches it. If it is not proven, say "Not established" and name the leading account. SHAs and file:line carry the detail; do not narrate what the reader can open.`},
	"fix":            {guidance: `The change and where it goes (file:line), then how to prove it can go red. Code goes in a fenced block, which the budget does not count.`},
}

const (
	defaultSectionGuidance = `Keep what this section is for and cut the rest. SHAs and file:line carry the detail; do not narrate what the reader can open.`
	strictCommentGuidance  = `A comment carries one thing: the evidence, the correction, the question or the answer. SHAs, file:line and run ids carry the detail. What changed about the ticket belongs in the description patch, not in the comment.`
)

// strictUnit is one piece of prose in a strict call, and where a rewrite of it goes.
type strictUnit struct {
	label   string // "comment", "Cause section"
	text    string
	rule    sectionRule
	comment bool
	budget  int
	set     func(string)
}

func strictUnits(input map[string]any, field string) []strictUnit {
	var units []strictUnit
	if body, ok := input["body"].(string); ok && strings.TrimSpace(body) != "" {
		units = append(units, strictUnit{label: "comment", text: body, comment: true, budget: strictCommentBudget,
			rule: sectionRule{guidance: strictCommentGuidance}, set: func(s string) { input["body"] = s }})
	}
	list, _ := input[field].([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		section, _ := entry["section"].(string)
		body, _ := entry["body"].(string)
		key := strings.ToLower(strings.TrimSpace(section))
		// The Impact line is one line, checked for presence on the whole description by the caller.
		if key == "" || key == "impact" || strings.TrimSpace(body) == "" {
			continue
		}
		rule, known := sectionRules[key]
		if !known {
			rule = sectionRule{guidance: defaultSectionGuidance}
		}
		units = append(units, strictUnit{label: section + " section", text: body, rule: rule, budget: defaultCommentBudget,
			set: func(s string) { entry["body"] = s }})
	}
	return units
}

// evaluate is the budget half of the judgment: the whole text for prose, each line for lists.
func (u strictUnit) evaluate() (over bool, reason string) {
	if !u.rule.lines {
		return budgetgate.EvaluateWith(u.text, u.label, budgetFor(u.budget), u.rule.guidance)
	}
	var long []string
	for _, line := range strings.Split(u.text, "\n") {
		if u.rule.ticks {
			if item, citation, ok := tickedParts(line); ok {
				for _, part := range []struct{ name, text string }{{"item", item}, {"citation", citation}} {
					if n := proseWords(part.text); n > strictLineBudget {
						long = append(long, fmt.Sprintf("  - %q (its %s is %d words)", strings.TrimSpace(line), part.name, n))
					}
				}
				continue
			}
		}
		if n := proseWords(line); n > strictLineBudget {
			long = append(long, fmt.Sprintf("  - %q (%d words)", strings.TrimSpace(line), n))
		}
	}
	if len(long) == 0 {
		return false, ""
	}
	return true, fmt.Sprintf("This %s has %d line(s) over the %d-word line budget:\n%s\n\n%s",
		u.label, len(long), strictLineBudget, strings.Join(long, "\n"), u.rule.guidance)
}

// tickedRe and citationSep match linear-strict's reading of a ticked item: the citation starts at
// the first " · " or " — " after the item's text.
var (
	tickedRe    = regexp.MustCompile(`^\s*[-*]\s*\[[xX]\]\s*(.*)$`)
	citationSep = regexp.MustCompile(`\s(?:·|—)\s`)
)

// tickedParts splits a ticked item into its text and its citation. The item's text was written
// when the item was, and linear-strict treats rewording it as dropping the item, so a citation
// added at tick time must not push a line that already passed over the budget.
func tickedParts(line string) (item, citation string, ok bool) {
	m := tickedRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	loc := citationSep.FindStringIndex(m[1])
	if loc == nil {
		return "", "", false
	}
	return m[1][:loc[0]], m[1][loc[1]:], true
}

// siblingPayload is what cope and basanite read: the unit alone, in the official shape they know.
func (u strictUnit) siblingPayload() []byte {
	if u.comment {
		return budgetgate.LinearPayload("comment", u.text)
	}
	return budgetgate.LinearPayload("issue description", u.text)
}

type strictVerdict struct {
	deny      string
	rewritten string
	note      string
}

// A list section skips cope and basanite. Both read paragraphs, and on evidence lines they join one
// line to the next and score the pair as a sentence (canary replay, 2026-09-25: every sibling hit
// on an Observed section spanned a line break). Its citations are still checked.
func judgeAll(u strictUnit, text, cwd string, linear *linearclient.Client) (cope, basanite, citations budgetgate.Judgment, citeIDs []string) {
	if u.rule.lines {
		citations, citeIDs = citecheck.Judge(context.Background(), linear, cwd, text)
		return
	}
	payload := strictUnit{text: text, comment: u.comment}.siblingPayload()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); cope = judgeCope(payload) }()
	go func() { defer wg.Done(); basanite = judgeBasanite(payload) }()
	go func() { defer wg.Done(); citations, citeIDs = citecheck.Judge(context.Background(), linear, cwd, text) }()
	wg.Wait()
	return
}

// judgeStrictUnit runs one unit through the same checks and retry rules as a whole body: over
// budget or a bad citation always denies; a cope or basanite hit is rewritten when a rewrite comes
// back clean, and lets the write through with a note on the third attempt that shrinks nothing.
func judgeStrictUnit(in hookInput, anchor string, u strictUnit, linear *linearclient.Client, rewriter *autorewrite.Client) strictVerdict {
	over, budgetReason := u.evaluate()
	cope, basanite, citations, citeIDs := judgeAll(u, u.text, in.Cwd, linear)
	key := attemptstate.Key{SessionID: in.SessionID, Tool: in.ToolName, Kind: u.label, Anchor: anchor}
	if !over && !cope.Flagged && !basanite.Flagged && !citations.Flagged {
		attemptstate.Clear(key)
		return strictVerdict{}
	}

	// List sections are left to the author: a rewrite of a dated evidence line can break the
	// format the server enforces, or quietly change what was observed.
	if rewriter != nil && !u.rule.lines && !citations.Flagged {
		if candidate, ok := strictRewrite(in.Cwd, u, over, budgetReason, cope, basanite, linear, rewriter); ok {
			attemptstate.Clear(key)
			return strictVerdict{rewritten: candidate}
		}
	}

	ids := budgetgate.AllViolationIDs(
		budgetgate.ViolationIDs("cope", cope.Note),
		budgetgate.ViolationIDs("basanite", basanite.Note),
		citeIDs,
	)
	rec := attemptstate.Load(key)
	attempt := rec.Attempts + 1
	if !over && !citations.Flagged && attempt >= 3 && len(ids) >= len(rec.Prior) {
		attemptstate.Clear(key)
		return strictVerdict{note: stalledNote(u.label, attempt, cope, basanite, budgetgate.Judgment{})}
	}

	reason := budgetReason
	if !over {
		reason = "Inside its budget, but a sibling scorer flagged it."
	}
	if cope.Flagged {
		reason += "\n\ncope flagged this:\n\n" + cope.Note
	}
	if basanite.Flagged {
		reason += "\n\nbasanite flagged this:\n\n" + basanite.Note
	}
	if citations.Flagged {
		reason += "\n\n" + citations.Note
	}
	if delta := deltaNote(rec.Prior, ids); delta != "" {
		reason += "\n\n" + delta
	}
	attemptstate.Save(key, attemptstate.Record{Attempts: attempt, Prior: ids})
	return strictVerdict{deny: reason}
}

func strictRewrite(cwd string, u strictUnit, over bool, budgetReason string, cope, basanite budgetgate.Judgment,
	linear *linearclient.Client, rewriter *autorewrite.Client) (string, bool) {
	var violations []string
	if over {
		violations = append(violations, budgetReason)
	}
	if cope.Flagged {
		violations = append(violations, cope.Note)
	}
	if basanite.Flagged {
		violations = append(violations, basanite.Note)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	candidate, err := rewriter.Rewrite(ctx, u.label+" of a Linear ticket", u.text, violations)
	if err != nil {
		return "", false
	}
	// The server heads what it writes itself; a tag inside a section would sit in the ticket text.
	candidate = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(candidate), agentTagRune))
	if candidate == "" || !sameEvidence(u.text, candidate) {
		return "", false
	}
	if newOver, _ := (strictUnit{text: candidate, label: u.label, rule: u.rule, budget: u.budget}).evaluate(); newOver {
		return "", false
	}
	newCope, newBasanite, newCitations, _ := judgeAll(u, candidate, cwd, linear)
	if newCope.Flagged || newBasanite.Flagged || newCitations.Flagged {
		return "", false
	}
	return candidate, true
}

// runStrict judges every unit in a strict call on its own. Any deny refuses the call, naming each
// unit that failed; otherwise each rewrite goes into its own field and is disclosed.
func runStrict(in hookInput, field string) *hookOutput {
	var input map[string]any
	if json.Unmarshal(in.ToolInput, &input) != nil {
		return nil
	}
	units := strictUnits(input, field)
	if len(units) == 0 {
		return nil
	}
	linear, _ := linearclient.New(in.Cwd)
	var rewriter *autorewrite.Client
	if os.Getenv("TICKETVOICE_NO_AUTOREWRITE") == "" {
		rewriter, _ = autorewrite.New(in.Cwd)
	}
	anchor, _ := input["issue"].(string)

	var denies, notes []string
	rewrote := false
	for _, u := range units {
		v := judgeStrictUnit(in, anchor, u, linear, rewriter)
		switch {
		case v.deny != "":
			denies = append(denies, fmt.Sprintf("[%s] %s", u.label, v.deny))
		case v.rewritten != "":
			u.set(v.rewritten)
			rewrote = true
			notes = append(notes, fmt.Sprintf("ticketvoice rewrote the %s before saving it (flagged for length or voice). "+
				"What was stored:\n\n%s\n\nIf that lost or changed anything you meant, write your own revision over it.", u.label, v.rewritten))
		case v.note != "":
			notes = append(notes, v.note)
		}
	}

	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	if len(denies) > 0 {
		out.HookSpecificOutput.PermissionDecision = "deny"
		out.HookSpecificOutput.PermissionDecisionReason = strings.Join(denies, "\n\n") + "\n\n" + retryNowLine
		return &out
	}
	if !rewrote && len(notes) == 0 {
		return nil
	}
	if rewrote {
		updated, err := json.Marshal(input)
		if err != nil {
			return nil
		}
		out.HookSpecificOutput.PermissionDecision = "allow"
		out.HookSpecificOutput.UpdatedInput = updated
	}
	out.HookSpecificOutput.AdditionalContext = strings.Join(notes, "\n\n")
	return &out
}
