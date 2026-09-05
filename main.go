// ticketvoice is a Claude Code PreToolUse hook that gates Linear ticket prose through cope,
// basanite, and a word budget before it posts.
//
// A memory saying "write like a pragmatic staff engineer" held, and tickets still ran long anyway —
// a memory is a taste, and a taste can be talked past mid-generation without ever registering as a
// violation. cope and basanite replace the taste with an actual read of the prose; the word budget
// below catches what neither of them scores: sheer length.
//
// Silent when the body clears all three. Otherwise it returns permissionDecision "deny" — the reason
// goes to Claude, not a human, so Claude retries on its own instead of paging anyone.
// TICKETVOICE_MAX_WORDS is the only override, set ahead of time, not decided per ticket.
//
// Two sibling tools watch the same Linear writes — basanite (vocabulary tics) and cope (voicing and
// structure) — and both, independently, chose never to block on their own: additionalContext only.
// This hook closes that by calling both directly, forwarding the exact bytes it received to
// cope-gate -pretool and basanite writecheck -no-dedup and gating on their verdicts too, not just
// the budget — see judgeCope and judgeBasanite.
//
// A denial that never changes is a loop, not a gate: internal/attemptstate tracks how many times
// in a row the same session has had the same write denied on a flagged-but-in-budget body, and if
// three attempts in a row show no shrink in what's flagged, the third one lets the write through
// with a note rather than denying it again. Being over budget is exempt from this — it's the one
// check meant to be a hard limit, not talked past by attempt count. See CHANGELOG.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/justinstimatze/ticketvoice/internal/attemptstate"
	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
	"github.com/justinstimatze/ticketvoice/internal/citecheck"
	"github.com/justinstimatze/ticketvoice/internal/impactline"
	"github.com/justinstimatze/ticketvoice/internal/linearclient"
)

var version = "dev"

// buildVersion resolves the version from ldflags, then the module's own build info, then vcs
// metadata, then "dev". See the go-cli-versioning note: the git tag is the source of truth and a
// hand-maintained const drifts from it.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		return rev + "-dirty"
	}
	return rev
}

type hookInput struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

type linearInput struct {
	Description string `json:"description"`
	Body        string `json:"body"`
	Patch       []struct {
		NewString string `json:"new_string"`
		Text      string `json:"text"`
	} `json:"patch"`
}

type bashInput struct {
	Command string `json:"command"`
}

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string          `json:"hookEventName"`
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
		UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
		AdditionalContext        string          `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// prose pulls the human-readable field for the tool being called, and the budget that applies to it.
// A patch is counted on its inserted text only: the rest of the body is not being rewritten.
func prose(tool string, raw json.RawMessage) (text string, budget int, kind string) {
	var in linearInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", 0, ""
	}
	switch tool {
	case "mcp__linear__save_issue":
		if in.Description != "" {
			return in.Description, budgetgate.IssueBudget, "issue description"
		}
		var b strings.Builder
		for _, p := range in.Patch {
			b.WriteString(p.NewString + " " + p.Text + " ")
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s, budgetgate.IssueBudget, "issue description patch"
		}
	case "mcp__linear__save_comment":
		if in.Body != "" {
			return in.Body, budgetgate.CommentBudget, "comment"
		}
	case "mcp__linear__save_diff_comment":
		if in.Body != "" {
			return in.Body, budgetgate.CommentBudget, "diff comment"
		}
	case "mcp__linear__submit_diff_review":
		if in.Body != "" {
			return in.Body, budgetgate.CommentBudget, "diff review"
		}
	}
	return "", 0, ""
}

var (
	ghWriteInvoke = regexp.MustCompile(`\bgh-write\s+(issue|pr)\s+(create|comment|edit)\b`)
	heredocOpener = regexp.MustCompile(`<<-?\s*(['"]?)(\w+)['"]?[ \t]*\r?\n`)
)

// maxRedirectBytes caps how much of a `< file` redirect target ghWriteProse will read. A ticket
// body is words, not megabytes; anything bigger isn't a body this gate needs to look at.
const maxRedirectBytes = 2 << 20

// redirectPathByte is the literal-path alphabet ghWriteProse trusts for a `< file` redirect: no
// $VAR, no backtick, no glob, no process substitution — the same "text, not shell semantics"
// limit that keeps the heredoc match honest without a real tokenizer.
func redirectPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '/', '.', '_', '-':
		return true
	}
	return false
}

// findRedirectPath looks for a plain `< path` input redirect in a gh-write invocation's tail —
// the case the heredoc marker doesn't cover. `<<` (heredoc) and `<(` (process substitution) are
// skipped, not matched. Anything after `<` that isn't a literal path falls through to ok=false.
func findRedirectPath(rest string) (path string, ok bool) {
	for i := 0; i < len(rest); i++ {
		if rest[i] != '<' {
			continue
		}
		if i+1 < len(rest) && (rest[i+1] == '<' || rest[i+1] == '(') {
			i++
			continue
		}
		j := i + 1
		for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t') {
			j++
		}
		k := j
		for k < len(rest) && redirectPathByte(rest[k]) {
			k++
		}
		if k > j {
			return rest[j:k], true
		}
	}
	return "", false
}

// ghWriteProse finds a gh-write invocation (see cmd/gh-write) in a Bash command string and
// extracts the body that follows it — a heredoc, or a `< file` redirect — with the budget kind
// that applies. gh-write refuses --body/--body-file, so an inline body is never a quoted, escaped
// argument: it's either literal heredoc text this can string-search for, or a plain file this can
// read, both without a shell tokenizer. cwd resolves a relative redirect path; pass "" when it's
// unknown and only an absolute path will be followed. Returns ok=false for any Bash command that
// isn't a gh-write call, or whose body this can't find, and the caller must treat that as
// "nothing to check," not "block" — same as an unparseable Linear call. A pipe-sourced body
// (`... | gh-write ...`) is deliberately not handled here: seeing what a pipe's upstream stage
// would produce means running it, and a text matcher has no business doing that — see gh-write's
// own budgetgate call for the backstop that covers this case instead.
func ghWriteProse(command, cwd string) (text, kind string, budget int, ok bool) {
	inv := ghWriteInvoke.FindStringSubmatchIndex(command)
	if inv == nil {
		return "", "", 0, false
	}
	object := command[inv[2]:inv[3]]
	verb := command[inv[4]:inv[5]]
	kind, budget = budgetgate.Classify(object, verb)
	rest := command[inv[1]:]

	if open := heredocOpener.FindStringSubmatchIndex(rest); open != nil {
		delim := rest[open[4]:open[5]]
		bodyStart := open[1]
		closer := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(delim) + `[ \t]*$`)
		loc := closer.FindStringIndex(rest[bodyStart:])
		if loc == nil {
			return "", "", 0, false
		}
		text = strings.TrimSuffix(rest[bodyStart:bodyStart+loc[0]], "\n")
		return text, kind, budget, true
	}

	if path, found := findRedirectPath(rest); found {
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Size() > maxRedirectBytes {
			return "", "", 0, false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", 0, false
		}
		return string(data), kind, budget, true
	}

	return "", "", 0, false
}

// extractProse dispatches on the calling tool: Linear's MCP tools carry a structured
// description/body field (prose, above), a Bash call carries an opaque command string
// that only a gh-write invocation makes readable (ghWriteProse, above). Any other tool
// yields no prose to check.
func extractProse(tool string, raw json.RawMessage, cwd string) (text string, budget int, kind string) {
	if tool == "Bash" {
		var b bashInput
		if json.Unmarshal(raw, &b) != nil {
			return "", 0, ""
		}
		t, k, bud, ok := ghWriteProse(b.Command, cwd)
		if !ok {
			return "", 0, ""
		}
		return t, bud, k
	}
	return prose(tool, raw)
}

// The budget check, sibling forwarding, and their word-counting live in internal/budgetgate now
// — gh-write's own backstop needs the exact same gate, not a second copy of it. These names stay
// as thin aliases so the hook logic below and the tests that exercise it don't have to spell the
// package out at every call site.
const (
	defaultIssueBudget   = budgetgate.IssueBudget
	defaultCommentBudget = budgetgate.CommentBudget
)

func proseWords(s string) int { return budgetgate.ProseWords(s) }
func budgetFor(base int) int  { return budgetgate.BudgetFor(base) }
func evaluate(text, kind string, budget int) (bool, string) {
	return budgetgate.Evaluate(text, kind, budget)
}
func judgeCope(rawStdin []byte) budgetgate.Judgment     { return budgetgate.JudgeCope(rawStdin) }
func judgeBasanite(rawStdin []byte) budgetgate.Judgment { return budgetgate.JudgeBasanite(rawStdin) }

// linearIdentity pulls whatever pre-existing id a Linear tool_input already carries — "id" for an
// issue edit (save_issue), "issueId" for a comment attached to one — so attemptstate.Key can tell
// "the same ticket, retried" from "a different ticket that happens to trip the same rules." A
// fresh save_issue create has neither field and returns "" — see ghWriteIdentity for the same
// boundary case on the Bash surface, and attemptstate.Key's doc comment for what an empty anchor
// means.
func linearIdentity(raw json.RawMessage) string {
	var in struct {
		ID      string `json:"id"`
		IssueID string `json:"issueId"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	if in.ID != "" {
		return in.ID
	}
	return in.IssueID
}

// ghWriteTargetID matches the numeric id gh-write's own CLI grammar puts after comment/edit — the
// issue or PR the call is already about, not one it's about to create.
var ghWriteTargetID = regexp.MustCompile(`\b(?:issue|pr)\s+(?:comment|edit)\s+(\d+)\b`)

// ghWriteIdentity mirrors linearIdentity for a Bash gh-write call. A create call has no target
// yet, so it returns "" the same way a fresh Linear issue does.
func ghWriteIdentity(command string) string {
	if m := ghWriteTargetID.FindStringSubmatch(command); m != nil {
		return m[1]
	}
	return ""
}

// linearTagField names the tool_input field taggedLinearInput should rewrite for a given Linear
// tool call and prose kind, or "" when there's none: a Bash call (GitHub's tag is gh-write's own
// job, and "description"/"body" mean nothing on a command string) or a patch (a diff against an
// existing description, not a fresh post — there's no single field a prefix belongs on).
func linearTagField(tool, kind string) string {
	if !strings.HasPrefix(tool, "mcp__linear__") {
		return ""
	}
	switch kind {
	case "issue description":
		return "description"
	case "comment", "diff comment", "diff review":
		return "body"
	}
	return ""
}

// taggedLinearInput returns the original Linear tool_input with the agent tag prepended to the
// one field that carries prose — as a full replacement object, since Claude Code's updatedInput
// replaces the whole input rather than merging, so every other field (id, teamId, title, whatever
// else the real schema carries that this hook never parses) has to round-trip untouched. Returns
// nil when there's nothing to tag (see linearTagField), the tag is disabled, or the input can't
// be read back as a plain object.
func taggedLinearInput(tool string, raw json.RawMessage, kind string) json.RawMessage {
	if !budgetgate.AgentTagEnabled() {
		return nil
	}
	field := linearTagField(tool, kind)
	if field == "" {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	rawVal, ok := obj[field]
	if !ok {
		return nil
	}
	var val string
	if json.Unmarshal(rawVal, &val) != nil || strings.HasPrefix(val, budgetgate.AgentTag) {
		return nil
	}
	tagged, err := json.Marshal(budgetgate.AgentTag + val)
	if err != nil {
		return nil
	}
	obj[field] = tagged
	out, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return out
}

// retryNowLine replaces the old "Cut it and call again," which told the model what to do but not
// what to refrain from. The observed failure was Claude choosing to ask the operator to rewrite
// the ticket by hand instead of retrying itself, so the line has to name that choice, not just the
// fix.
const retryNowLine = "Revise it and call again now — asking the operator to do the rewrite is the failure this reason exists to prevent."

// deltaNote reports what changed in the violation set since the last denial of this same
// sequence, or "" on the first attempt (prev empty). Naming what's gone, what's still there, and
// what's new is what lets a rewrite be judged instead of repeated blind.
func deltaNote(prev, cur []string) string {
	if len(prev) == 0 {
		return ""
	}
	curSet := make(map[string]bool, len(cur))
	for _, id := range cur {
		curSet[id] = true
	}
	prevSet := make(map[string]bool, len(prev))
	var gone, stayed []string
	for _, id := range prev {
		prevSet[id] = true
		if curSet[id] {
			stayed = append(stayed, id)
		} else {
			gone = append(gone, id)
		}
	}
	var added []string
	for _, id := range cur {
		if !prevSet[id] {
			added = append(added, id)
		}
	}

	var parts []string
	if len(gone) > 0 {
		parts = append(parts, "cleared: "+strings.Join(gone, ", "))
	}
	if len(stayed) > 0 {
		parts = append(parts, "still flagged: "+strings.Join(stayed, ", "))
	}
	if len(added) > 0 {
		parts = append(parts, "newly flagged: "+strings.Join(added, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Since the last attempt — " + strings.Join(parts, "; ") + "."
}

// stalledNote is the additionalContext a stalled sequence gets instead of a fourth denial — see
// runHookWithInput's escalation branch. It still names what's flagged, so letting the write
// through isn't the same as staying silent about it. citations never reaches here — it's exempt
// from escalation (see the escalation guard) — so it isn't a parameter.
func stalledNote(kind string, attempt int, cope, basanite, impact budgetgate.Judgment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This %s went through on attempt %d with the same hit(s) still flagged. A deny that "+
		"never shrinks is a loop, not a gate.", kind, attempt)
	if cope.Flagged {
		fmt.Fprintf(&b, "\n\ncope still flags this:\n\n%s", cope.Note)
	}
	if basanite.Flagged {
		fmt.Fprintf(&b, "\n\nbasanite still flags this:\n\n%s", basanite.Note)
	}
	if impact.Flagged {
		fmt.Fprintf(&b, "\n\n%s", impact.Note)
	}
	return b.String()
}

// runHookWithInput is the hook's decision logic, taking the raw bytes so the same bytes can be
// forwarded to the siblings unmodified and so this runs without a subprocess in tests. Returns nil
// when the call should proceed with no hook involvement at all — untagged, since there was nothing
// to tag (a Bash call, a patch, or the tag disabled) as well as unflagged.
//
// Being over budget always denies, attempt count or not — it's the one check meant to be a hard
// limit, not a register a rewrite can talk its way past (see CHANGELOG.md). cope and basanite are
// the opposite case: a flagged-but-in-budget write tracks its own retry sequence in
// internal/attemptstate, keyed on session, tool, and kind, and on the third attempt in a row whose
// violation set hasn't shrunk since the one before it, this lets the write through with a note
// instead of denying a fourth time — a deny that never shrinks is a loop, not a gate. See
// stalledNote/deltaNote above for what the model actually sees at each step.
func runHookWithInput(raw []byte) *hookOutput {
	var in hookInput
	// A hook that cannot parse its input must not block the call it was watching.
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	text, rawBudget, kind := extractProse(in.ToolName, in.ToolInput, in.Cwd)
	if text == "" {
		return nil
	}
	budget := budgetFor(rawBudget)
	over, budgetReason := evaluate(text, kind, budget)

	linear, _ := linearclient.New() // nil, ok=false when TICKETVOICE_LINEAR_TOKEN is unset — every
	// caller below already treats a nil client as "skip this check," the same fail-open posture a
	// missing cope-gate/basanite binary gets.

	// cope, basanite, and citecheck each make their own external call (a subprocess, a subprocess,
	// and up to a few HTTP/git calls respectively) and used to run one after another — this is the
	// hook's first concurrency, so none of that time simply adds up. Each already bounds itself
	// internally (judgeSibling's 3s context, linearclient's 4s context, citecheck's own git
	// timeouts), so wg.Wait() here is already bounded by the slowest of those, not unbounded — an
	// additional outer timeout would either never fire or, if it somehow did, read cope/basanite/
	// citations/citeIDs while a goroutine was still writing them, which is a real data race for no
	// real benefit given every leaf already has its own ceiling.
	var cope, basanite, citations budgetgate.Judgment
	var citeIDs []string
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); cope = judgeCope(raw) }()
	go func() { defer wg.Done(); basanite = judgeBasanite(raw) }()
	go func() {
		defer wg.Done()
		citations, citeIDs = citecheck.Judge(context.Background(), linear, in.Cwd, text)
	}()
	wg.Wait()

	// impactline only applies to the ticket's own description, not a comment on it, and needs no
	// network call — it runs synchronously, outside the goroutine group above.
	var impact budgetgate.Judgment
	if kind == "issue description" {
		impact = impactline.Judge(text)
	}

	updatedInput := taggedLinearInput(in.ToolName, in.ToolInput, kind)

	var identity string
	if in.ToolName == "Bash" {
		var b bashInput
		if json.Unmarshal(in.ToolInput, &b) == nil {
			identity = ghWriteIdentity(b.Command)
		}
	} else {
		identity = linearIdentity(in.ToolInput)
	}
	key := attemptstate.Key{SessionID: in.SessionID, Tool: in.ToolName, Kind: kind, Anchor: identity}

	if !over && !cope.Flagged && !basanite.Flagged && !impact.Flagged && !citations.Flagged {
		attemptstate.Clear(key)
		if updatedInput == nil {
			return nil
		}
		var out hookOutput
		out.HookSpecificOutput.HookEventName = "PreToolUse"
		out.HookSpecificOutput.PermissionDecision = "allow"
		out.HookSpecificOutput.UpdatedInput = updatedInput
		return &out
	}

	var impactIDs []string
	if impact.Flagged {
		impactIDs = []string{impactline.ViolationID}
	}
	curIDs := budgetgate.AllViolationIDs(
		budgetgate.ViolationIDs("cope", cope.Note),
		budgetgate.ViolationIDs("basanite", basanite.Note),
		impactIDs,
		citeIDs,
	)
	rec := attemptstate.Load(key)
	attempt := rec.Attempts + 1

	// citations is exempt from escalation: a nonexistent ticket, file, or SHA doesn't become real
	// by attempt 3, so it always denies regardless of attempt count, the same as being over budget.
	if !over && !citations.Flagged && attempt >= 3 && len(curIDs) >= len(rec.Prior) {
		attemptstate.Clear(key)
		var out hookOutput
		out.HookSpecificOutput.HookEventName = "PreToolUse"
		out.HookSpecificOutput.PermissionDecision = "allow"
		out.HookSpecificOutput.AdditionalContext = stalledNote(kind, attempt, cope, basanite, impact)
		out.HookSpecificOutput.UpdatedInput = updatedInput
		return &out
	}

	var reason string
	if over {
		reason = budgetReason
	} else {
		reason = fmt.Sprintf("This %s is inside the %d-word budget, but a sibling scorer flagged it on the way out.", kind, budget)
	}
	if cope.Flagged {
		reason += fmt.Sprintf("\n\ncope flagged this:\n\n%s", cope.Note)
	}
	if basanite.Flagged {
		reason += fmt.Sprintf("\n\nbasanite flagged this:\n\n%s", basanite.Note)
	}
	if impact.Flagged {
		reason += "\n\n" + impact.Note
	}
	if citations.Flagged {
		reason += "\n\n" + citations.Note
	}
	if delta := deltaNote(rec.Prior, curIDs); delta != "" {
		reason += "\n\n" + delta
	}
	reason += "\n\n" + retryNowLine

	attemptstate.Save(key, attemptstate.Record{Attempts: attempt, Prior: curIDs})

	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = reason
	return &out
}

func runHook() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	out := runHookWithInput(raw)
	if out == nil {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// runCheck runs prose through the same gate as the hook, outside a tool call — for gating this
// project's own docs the way cope gates README.md through cope-gate and effigy writes its README
// through its own Layer 2. ticketvoice doesn't generate prose, so the analogue is narrower: check a
// slice of prose against the same budget a Linear write would face, as if it were a ticket entry.
func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	field := fs.String("field", "issue", `budget to check against: "issue" or "comment"`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ticketvoice --check <file|-> [-field issue|comment]")
		return 2
	}

	kind, budget := "issue description", defaultIssueBudget
	if *field == "comment" {
		kind, budget = "comment", defaultCommentBudget
	}
	budget = budgetFor(budget)

	path := fs.Arg(0)
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	over, reason := evaluate(string(data), kind, budget)
	if !over {
		fmt.Printf("%s: %d words against a %d-word %s budget — within budget.\n", path, proseWords(string(data)), budget, kind)
		return 0
	}
	fmt.Println(reason + "\n\nCut it and check again.")
	return 1
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version":
			fmt.Println(buildVersion())
			return
		case "--check":
			os.Exit(runCheck(os.Args[2:]))
		}
	}
	runHook()
}
