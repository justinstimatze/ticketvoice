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

func TestHeaderCountSkipsFencedHashes(t *testing.T) {
	if got := headerCount("## Real\n\n```sh\n# not a header\n```\n\n### Also real"); got != 2 {
		t.Fatalf("want 2 headers, got %d", got)
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, budget, _ := prose(tc.tool, json.RawMessage(tc.raw))
			if strings.TrimSpace(got) != tc.want || budget != tc.budget {
				t.Fatalf("want (%q, %d), got (%q, %d)", tc.want, tc.budget, strings.TrimSpace(got), budget)
			}
		})
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
	if j := judgeCope([]byte(`{}`)); j.flagged {
		t.Fatalf("missing binary must fail open, got %+v", j)
	}
}

func TestJudgeCopeParsesFlaggedOutput(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", "clause_symmetry: 1 violation(s)"))
	j := judgeCope([]byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"x"}}`))
	if !j.flagged || j.note != "clause_symmetry: 1 violation(s)" {
		t.Fatalf("want flagged with cope's note, got %+v", j)
	}
}

func TestJudgeCopeCleanOutputNotFlagged(t *testing.T) {
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", ""))
	if j := judgeCope([]byte(`{}`)); j.flagged {
		t.Fatalf("clean run must not flag, got %+v", j)
	}
}

func TestJudgeBasaniteMissingBinaryFailsOpen(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", filepath.Join(t.TempDir(), "does-not-exist"))
	if j := judgeBasanite([]byte(`{}`)); j.flagged {
		t.Fatalf("missing binary must fail open, got %+v", j)
	}
}

func TestJudgeBasaniteParsesFlaggedOutput(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", "load-bearing ×1 → supporting"))
	j := judgeBasanite([]byte(`{"session_id":"s","tool_input":{"description":"x"}}`))
	if !j.flagged || j.note != "load-bearing ×1 → supporting" {
		t.Fatalf("want flagged with basanite's note, got %+v", j)
	}
}

func TestJudgeBasaniteCleanOutputNotFlagged(t *testing.T) {
	t.Setenv("TICKETVOICE_BASANITE", fakeSiblingBinary(t, "basanite", ""))
	if j := judgeBasanite([]byte(`{}`)); j.flagged {
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

func TestRunHookSilentWhenAllClean(t *testing.T) {
	clean(t)
	raw := []byte(`{"tool_name":"mcp__linear__save_issue","tool_input":{"description":"` + words(20) + `"}}`)
	if out := runHookWithInput(raw); out != nil {
		t.Fatalf("under budget and clean must stay silent, got %+v", out)
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
