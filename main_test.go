package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func words(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }

func TestProseWordsExcludesFencedCode(t *testing.T) {
	body := words(10) + "\n\n```ts\n" + words(500) + "\n```\n\n" + words(5)
	if got := proseWords(body); got != 15 {
		t.Fatalf("fenced code counted: want 15, got %d", got)
	}
}

func TestProseWordsIgnoresBareSymbols(t *testing.T) {
	if got := proseWords("- > | 42 alpha beta"); got != 2 {
		t.Fatalf("want 2 wordish tokens, got %d", got)
	}
}

// The gate must fire for the failure it names — a long issue body — and stay silent otherwise.
func TestProseSelectsFieldAndBudgetPerTool(t *testing.T) {
	for _, tc := range []struct {
		name, tool, raw, want string
		budget                int
	}{
		{"issue description", "mcp__linear__save_issue", `{"description":"hello there"}`, "hello there", defaultIssueBudget},
		{"comment body", "mcp__linear__save_comment", `{"body":"hello there"}`, "hello there", defaultCommentBudget},
		{"patch counts inserted text only", "mcp__linear__save_issue", `{"id":"CUR-1","patch":[{"op":"append","text":"added"}]}`, "added", defaultIssueBudget},
		{"unrelated tool", "mcp__linear__list_issues", `{"description":"hello"}`, "", 0},
		{"issue with no prose", "mcp__linear__save_issue", `{"state":"Done"}`, "", 0},
		{"diff comment body", "mcp__linear__save_diff_comment", `{"body":"hello there"}`, "hello there", defaultCommentBudget},
		{"diff review body", "mcp__linear__submit_diff_review", `{"body":"hello there"}`, "hello there", defaultCommentBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, budget, _ := prose(tc.tool, json.RawMessage(tc.raw))
			if strings.TrimSpace(got) != tc.want || budget != tc.budget {
				t.Fatalf("want (%q, %d), got (%q, %d)", tc.want, tc.budget, strings.TrimSpace(got), budget)
			}
		})
	}
}

// gh-write (cmd/gh-write) is the only thing allowed to put a body in front of this hook as a
// Bash command, and it always does so as a heredoc — see ghWriteProse's doc comment for why.
func TestGhWriteProseExtractsHeredocBody(t *testing.T) {
	for _, tc := range []struct {
		name, command, wantText, wantKind string
		wantBudget                        int
		wantOK                            bool
	}{
		{
			name:       "issue create",
			command:    "gh-write issue create --title 'Bug: X' <<'EOF'\nhello there\nEOF\n",
			wantText:   "hello there",
			wantKind:   "issue description",
			wantBudget: defaultIssueBudget,
			wantOK:     true,
		},
		{
			name:       "pr create",
			command:    "gh-write pr create --title T --base main <<'EOF'\nhello there\nEOF\n",
			wantText:   "hello there",
			wantKind:   "PR description",
			wantBudget: defaultIssueBudget,
			wantOK:     true,
		},
		{
			name:       "issue comment",
			command:    "gh-write issue comment 42 <<'EOF'\nlgtm\nEOF\n",
			wantText:   "lgtm",
			wantKind:   "issue comment",
			wantBudget: defaultCommentBudget,
			wantOK:     true,
		},
		{
			name:       "multi-line body preserved",
			command:    "gh-write pr edit 7 <<'EOF'\nline one\nline two\nEOF\n",
			wantText:   "line one\nline two",
			wantKind:   "PR description",
			wantBudget: defaultIssueBudget,
			wantOK:     true,
		},
		{
			name:    "unrelated bash command",
			command: "gh pr create --title T --body 'inline, unreadable'",
			wantOK:  false,
		},
		{
			name:    "gh-write with no heredoc",
			command: "gh-write issue create --title T --body-file notes.md",
			wantOK:  false,
		},
		{
			name:       "chained with && before it, on the same line",
			command:    "cd /some/repo && gh-write issue create --title T <<'EOF'\nhello there\nEOF\n",
			wantText:   "hello there",
			wantKind:   "issue description",
			wantBudget: defaultIssueBudget,
			wantOK:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, kind, budget, ok := ghWriteProse(tc.command, "")
			if ok != tc.wantOK {
				t.Fatalf("ok: want %v, got %v (text=%q kind=%q budget=%d)", tc.wantOK, ok, text, kind, budget)
			}
			if !ok {
				return
			}
			if text != tc.wantText || kind != tc.wantKind || budget != tc.wantBudget {
				t.Fatalf("want (%q, %q, %d), got (%q, %q, %d)", tc.wantText, tc.wantKind, tc.wantBudget, text, kind, budget)
			}
		})
	}
}

// gh-write's own validate() refuses --body-file, but not a `< file` redirect — that form is a
// plain file read, not the shell-quoted flag validate() blocks, so it's this function's job to
// find it.
func TestGhWriteProseReadsRedirectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.txt")
	if err := os.WriteFile(path, []byte("hello there"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("absolute path", func(t *testing.T) {
		text, kind, budget, ok := ghWriteProse("gh-write issue create --title T < "+path, "")
		if !ok || text != "hello there" || kind != "issue description" || budget != defaultIssueBudget {
			t.Fatalf("want (%q, %q, %d, true), got (%q, %q, %d, %v)", "hello there", "issue description", defaultIssueBudget, text, kind, budget, ok)
		}
	})

	t.Run("relative path resolved against cwd", func(t *testing.T) {
		text, _, _, ok := ghWriteProse("gh-write pr comment 7 < body.txt", dir)
		if !ok || text != "hello there" {
			t.Fatalf("want (%q, true), got (%q, %v)", "hello there", text, ok)
		}
	})

	t.Run("missing file fails open", func(t *testing.T) {
		_, _, _, ok := ghWriteProse("gh-write issue create < nope.txt", dir)
		if ok {
			t.Fatal("a redirect to a nonexistent file must not be treated as a body")
		}
	})

	t.Run("shell-expanded path is not followed", func(t *testing.T) {
		_, _, _, ok := ghWriteProse("gh-write issue create < $HOME/body.txt", "")
		if ok {
			t.Fatal("a redirect path with shell expansion must not be read literally")
		}
	})

	t.Run("process substitution is not a redirect", func(t *testing.T) {
		_, _, _, ok := ghWriteProse("gh-write issue create < <(echo hi)", "")
		if ok {
			t.Fatal("process substitution must not be misread as a file path")
		}
	})
}

// The --body-file rejection is gh-write's job (cmd/gh-write); this only has to confirm that a
// Bash command carrying one instead of a heredoc is *ignored*, not misread as an empty body that
// would pass every budget silently.
func TestExtractProseIgnoresNonGhWriteBash(t *testing.T) {
	text, budget, kind := extractProse("Bash", json.RawMessage(`{"command":"ls -la"}`), "")
	if text != "" || budget != 0 || kind != "" {
		t.Fatalf("want no prose for an unrelated Bash command, got (%q, %d, %q)", text, budget, kind)
	}
}

// Injure the thing it guards: the real over-budget ticket body was 238 words and must trip the
// budget, while the 118-word rewrite must not. A gate nobody has watched fail is not a gate.
func TestBudgetBoundary(t *testing.T) {
	over, budget, _ := prose("mcp__linear__save_issue", json.RawMessage(`{"description":"`+words(238)+`"}`))
	if proseWords(over) <= budget {
		t.Fatalf("238 words did not exceed the %d-word budget", budget)
	}
	under, _, _ := prose("mcp__linear__save_issue", json.RawMessage(`{"description":"`+words(118)+`"}`))
	if proseWords(under) > budget {
		t.Fatalf("118 words tripped the %d-word budget", budget)
	}
}

// evaluate is the single code path both the hook and --check run through; this is the one place
// that would miss a divergence between them.
func TestEvaluateMatchesBudget(t *testing.T) {
	if over, reason := evaluate(words(150), "issue description", defaultIssueBudget); over || reason != "" {
		t.Fatalf("150 words against a 150-word budget must not trip: over=%v reason=%q", over, reason)
	}
	over, reason := evaluate(words(151), "issue description", defaultIssueBudget)
	if !over || !strings.Contains(reason, "151 words") || !strings.Contains(reason, "1 over") {
		t.Fatalf("151 words must trip with a reason naming the overage: over=%v reason=%q", over, reason)
	}
}

// fakeSiblingBinary writes a stand-in for cope-gate or basanite that answers the way the real one
// does — the same hookSpecificOutput.additionalContext shape both use — so the subprocess boundary
// is real but the test doesn't need either sibling built. additionalContext == "" reproduces a
// clean run (both siblings print nothing at all on a clean score).
func fakeSiblingBinary(t *testing.T, name, additionalContext string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	body := "#!/bin/sh\n"
	if additionalContext != "" {
		var resp struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		resp.HookSpecificOutput.HookEventName = "PreToolUse"
		resp.HookSpecificOutput.AdditionalContext = additionalContext
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		payload := filepath.Join(dir, "payload.json")
		if err := os.WriteFile(payload, data, 0o644); err != nil {
			t.Fatal(err)
		}
		body += "cat \"$(dirname \"$0\")/payload.json\"\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// clean stubs both siblings to a clean run, so a test exercising one flagged sibling isn't
// contaminated by whatever cope-gate/basanite happen to be installed (or not) on the machine
// running the test.
func clean(t *testing.T) {
	t.Helper()
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", ""))
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", ""))
}

func TestJudgeCopeMissingBinaryFailsOpen(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", filepath.Join(t.TempDir(), "does-not-exist"))
	if j := judgeCope([]byte(`{}`)); j.Flagged {
		t.Fatalf("missing binary must fail open, got %+v", j)
	}
}

func TestJudgeCopeParsesFlaggedOutput(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "clause_symmetry: 1 violation(s)"))
	j := judgeCope([]byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"x"}}`))
	if !j.Flagged || j.Note != "clause_symmetry: 1 violation(s)" {
		t.Fatalf("want flagged with cope's note, got %+v", j)
	}
}

func TestJudgeCopeCleanOutputNotFlagged(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", ""))
	if j := judgeCope([]byte(`{}`)); j.Flagged {
		t.Fatalf("clean run must not flag, got %+v", j)
	}
}

func TestJudgeBasaniteMissingBinaryFailsOpen(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", filepath.Join(t.TempDir(), "does-not-exist"))
	if j := judgeBasanite([]byte(`{}`)); j.Flagged {
		t.Fatalf("missing binary must fail open, got %+v", j)
	}
}

func TestJudgeBasaniteParsesFlaggedOutput(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", "load-bearing ×1 → supporting"))
	j := judgeBasanite([]byte(`{"session_id":"s","tool_input":{"description":"x"}}`))
	if !j.Flagged || j.Note != "load-bearing ×1 → supporting" {
		t.Fatalf("want flagged with basanite's note, got %+v", j)
	}
}

func TestJudgeBasaniteCleanOutputNotFlagged(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", ""))
	if j := judgeBasanite([]byte(`{}`)); j.Flagged {
		t.Fatalf("clean run must not flag, got %+v", j)
	}
}

// The gap this closes: a within-budget ticket carrying a flagged tic used to ship with nobody
// forced to look. This is the test that would have caught it staying open.
func TestRunHookAsksWhenCopeFlagsAnUnderBudgetTicket(t *testing.T) {
	clean(t)
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "dangling_end: 1 violation(s)"))
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(20) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		t.Fatal("cope-flagged, under-budget ticket must still ask")
	}
	if out.HookSpecificOutput.PermissionDecision != "ask" {
		t.Fatalf("want ask, got %q", out.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "dangling_end") {
		t.Fatalf("reason must carry cope's finding: %q", out.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestRunHookAsksWhenBasaniteFlagsAnUnderBudgetTicket(t *testing.T) {
	clean(t)
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", "load-bearing ×1 → supporting"))
	raw := []byte(`{"session_id":"s","tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(20) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		t.Fatal("basanite-flagged, under-budget ticket must still ask")
	}
	if out.HookSpecificOutput.PermissionDecision != "ask" {
		t.Fatalf("want ask, got %q", out.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "load-bearing") {
		t.Fatalf("reason must carry basanite's finding: %q", out.HookSpecificOutput.PermissionDecisionReason)
	}
}

// A call this hook has no prose to check (wrong tool, no matching field) gets no hook output at
// all — not "allow", not a tag, nothing. That's the one case that must stay truly silent.
func TestRunHookSilentOnUnrelatedTool(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"mcp__linear__list_issues","tool_input":{"description":"` + words(20) + `"}}`)
	if out := runHookWithInput(raw); out != nil {
		t.Fatalf("a tool this hook doesn't check must stay silent, got %+v", out)
	}
}

// A clean, under-budget Linear write isn't silent anymore — it gets "allow" with the tag applied,
// so a reader always sees the ticket is agent-authored, not only when something got flagged.
func TestRunHookTagsCleanLinearWrite(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"id":"CUR-1","teamId":"eng","description":"` + words(20) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		t.Fatal("a clean write must still come back tagged, not nil")
	}
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("want allow, got %q", out.HookSpecificOutput.PermissionDecision)
	}
	var updated map[string]any
	if err := json.Unmarshal(out.HookSpecificOutput.UpdatedInput, &updated); err != nil {
		t.Fatalf("updatedInput must be valid JSON: %v (%s)", err, out.HookSpecificOutput.UpdatedInput)
	}
	if !strings.HasPrefix(updated["description"].(string), "🤖 ") {
		t.Fatalf("description must carry the agent tag, got %+v", updated)
	}
	if updated["id"] != "CUR-1" || updated["teamId"] != "eng" {
		t.Fatalf("unrelated fields must survive the rewrite untouched, got %+v", updated)
	}
}

// updatedInput replaces the whole input object (Claude Code's semantics, not a merge) — this is
// the test that would catch dropping a field the hook never parses, which would corrupt a real
// Linear write rather than just skip the tag.
func TestRunHookTagPreservesUnknownFields(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"mcp__linear__save_comment","tool_input":{"issueId":"CUR-9","priority":2,"body":"` + words(20) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		t.Fatal("want a tagged allow, got nil")
	}
	var updated map[string]any
	if err := json.Unmarshal(out.HookSpecificOutput.UpdatedInput, &updated); err != nil {
		t.Fatalf("updatedInput must be valid JSON: %v", err)
	}
	if updated["issueId"] != "CUR-9" || updated["priority"] != float64(2) {
		t.Fatalf("fields this hook never reads must still round-trip, got %+v", updated)
	}
}

// TICKETVOICE_NO_AGENT_TAG must fully restore the old silent-on-clean behavior, not just skip the
// tag while still returning an output.
func TestRunHookNoTagWhenDisabled(t *testing.T) {
	clean(t)
	t.Setenv("TICKETVOICE_NO_AGENT_TAG", "1")
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(20) + `"}}`)
	if out := runHookWithInput(raw); out != nil {
		t.Fatalf("a clean write with the tag disabled must stay silent, got %+v", out)
	}
}

// A flagged or over-budget Linear write still carries the tag in updatedInput, so what the human
// approves is what actually gets tagged when they say yes.
func TestRunHookTagsFlaggedLinearWriteToo(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "dangling_end: 1 violation(s)"))
	t.Setenv("TICKETVOICE_BASANITE", filepath.Join(t.TempDir(), "does-not-exist"))
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(20) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil || out.HookSpecificOutput.PermissionDecision != "ask" {
		t.Fatalf("want ask with a tagged updatedInput, got %+v", out)
	}
	var updated map[string]any
	if err := json.Unmarshal(out.HookSpecificOutput.UpdatedInput, &updated); err != nil {
		t.Fatalf("updatedInput must be valid JSON: %v", err)
	}
	if !strings.HasPrefix(updated["description"].(string), "🤖 ") {
		t.Fatalf("even the flagged/ask path must carry the tag, got %+v", updated)
	}
}

// A gh-write Bash call already tags itself (cmd/gh-write). The hook must never also try to rewrite
// a Bash command's tool_input — there's no "description"/"body" field on it to begin with.
func TestRunHookNeverTagsBashCalls(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"Bash","tool_input":{"command":"gh-write issue create --title T <<'EOF'\n` + words(20) + `\nEOF\n"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		return
	}
	if out.HookSpecificOutput.UpdatedInput != nil {
		t.Fatalf("a Bash call must never get updatedInput, got %s", out.HookSpecificOutput.UpdatedInput)
	}
}

// A patch is a diff against an existing description, not a fresh post — there's no single field a
// prefix belongs on, so this stays untagged even though it's still gated on word count.
func TestRunHookNeverTagsAPatch(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"id":"CUR-1","patch":[{"op":"append","text":"` + words(20) + `"}]}}`)
	out := runHookWithInput(raw)
	if out != nil && out.HookSpecificOutput.UpdatedInput != nil {
		t.Fatalf("a patch call must never get updatedInput, got %s", out.HookSpecificOutput.UpdatedInput)
	}
}

// The three-way case: over budget and both siblings flag it. Nothing gets dropped picking a
// reason to show.
func TestRunHookReasonCarriesAllThreeFindingsWhenOverBudgetAndBothFlag(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "forked_end: 1 violation(s)"))
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", "load-bearing ×1 → supporting"))
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(200) + `"}}`)
	out := runHookWithInput(raw)
	if out == nil {
		t.Fatal("over-budget ticket must ask")
	}
	reason := out.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "200 words") || !strings.Contains(reason, "forked_end") || !strings.Contains(reason, "load-bearing") {
		t.Fatalf("reason must carry the word count and both siblings' findings: %q", reason)
	}
}

// runCheck is the dogfooding path — README.md's own Why section, gated as if it were a Linear
// issue description, the way `make check-readme` runs it. It must hold, not just compile.
func TestRunCheckAcceptsReadmeWhySection(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	const start, end = "## Why\n", "\n## "
	i := strings.Index(string(data), start)
	if i < 0 {
		t.Fatal("README.md has no ## Why section")
	}
	body := string(data)[i+len(start):]
	if j := strings.Index(body, end); j >= 0 {
		body = body[:j]
	}
	if over, reason := evaluate(body, "issue description", defaultIssueBudget); over {
		t.Fatalf("README's Why section no longer fits its own budget: %s", reason)
	}
}
