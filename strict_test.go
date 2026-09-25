package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// strictEnv stubs the siblings clean, isolates retry state, and keeps any real Linear or Anthropic
// credential on this machine out of the test.
func strictEnv(t *testing.T) {
	t.Helper()
	clean(t)
	freshState(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func strictCall(t *testing.T, tool string, input map[string]any) *hookOutput {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"tool_name": tool, "session_id": "s1", "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	return runHookWithInput(raw)
}

func observedLine(n int) string {
	return "- 2026-09-25 · `npm test` · " + words(n)
}

func TestStrictToolNeedsALinearServer(t *testing.T) {
	for tool, want := range map[string]bool{
		"mcp__linear-strict__set_state":  true,
		"mcp__linear-strict__comment":    true,
		"mcp__linear-strict__set_status": false,
		"mcp__github__comment":           false,
		"mcp__linear__save_comment":      false,
	} {
		if _, ok := strictTool(tool); ok != want {
			t.Errorf("strictTool(%q) = %v, want %v", tool, ok, want)
		}
	}
}

// Evidence accumulates: six short dated lines are well past a comment's budget together, and each
// is fine on its own.
func TestStrictObservedIsBudgetedPerLine(t *testing.T) {
	strictEnv(t)
	lines := make([]string, 6)
	for i := range lines {
		lines[i] = observedLine(25)
	}
	out := strictCall(t, "mcp__linear-strict__set_state", map[string]any{
		"issue": "ENG-1", "patch": []any{map[string]any{"section": "Observed", "mode": "append", "body": strings.Join(lines, "\n")}},
	})
	if out != nil {
		t.Fatalf("six in-budget Observed lines must pass, got %+v", out.HookSpecificOutput)
	}

	out = strictCall(t, "mcp__linear-strict__set_state", map[string]any{
		"issue": "ENG-1", "patch": []any{map[string]any{"section": "Observed", "mode": "append", "body": observedLine(60)}},
	})
	if out == nil || out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("a 60-word Observed line must be refused, got %+v", out)
	}
	reason := out.HookSpecificOutput.PermissionDecisionReason
	for _, want := range []string{"[Observed section]", "line budget", "YYYY-MM-DD · source · result"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, reason)
		}
	}
	if strings.Contains(reason, "Four slots") || strings.Contains(reason, "comment") {
		t.Errorf("a section must not get the comment advice:\n%s", reason)
	}
}

func TestStrictListSectionsSkipTheProseScorers(t *testing.T) {
	strictEnv(t)
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", copeFixture("clause_symmetry")))
	out := strictCall(t, "mcp__linear-strict__set_state", map[string]any{
		"issue": "ENG-1", "patch": []any{
			map[string]any{"section": "Observed", "mode": "append", "body": observedLine(10) + "\n" + observedLine(12)},
			map[string]any{"section": "Done when", "mode": "replace", "body": "- [ ] " + words(8)},
		},
	})
	if out != nil {
		t.Fatalf("cope must not judge evidence lines, got %+v", out.HookSpecificOutput)
	}
}

func TestStrictCommentAllows150Words(t *testing.T) {
	strictEnv(t)
	out := strictCall(t, "mcp__linear-strict__comment", map[string]any{"issue": "ENG-1", "kind": "evidence", "body": words(145)})
	if out != nil {
		t.Fatalf("a 145-word strict comment is inside its budget, got %+v", out.HookSpecificOutput)
	}
}

func TestStrictCauseGetsItsOwnAdvice(t *testing.T) {
	strictEnv(t)
	out := strictCall(t, "mcp__linear-strict__set_state", map[string]any{
		"issue": "ENG-1", "patch": []any{
			map[string]any{"section": "Cause", "mode": "replace", "body": words(150)},
			map[string]any{"section": "Impact", "mode": "replace", "body": "Impact: " + words(300)},
		},
	})
	if out == nil || out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("a 150-word Cause must be refused, got %+v", out)
	}
	reason := out.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "This Cause section is 150 words") || !strings.Contains(reason, "Not established") {
		t.Errorf("reason must name the Cause section and give its advice:\n%s", reason)
	}
	if strings.Contains(reason, "Impact") {
		t.Errorf("the Impact section is checked by the caller, not budgeted here:\n%s", reason)
	}
}

// A comment's text and its patch are judged apart, and the refusal names each one that failed.
func TestStrictCommentAndPatchJudgedApart(t *testing.T) {
	strictEnv(t)
	out := strictCall(t, "mcp__linear-strict__comment", map[string]any{
		"issue": "ENG-1", "kind": "evidence", "body": words(160),
		"patch": []any{map[string]any{"section": "Observed", "mode": "append", "body": observedLine(10)}},
	})
	if out == nil || out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("a 160-word comment must be refused, got %+v", out)
	}
	reason := out.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "[comment] This comment is 160 words of prose against a 150-word budget") || strings.Contains(reason, "[Observed section]") {
		t.Errorf("only the comment should be refused:\n%s", reason)
	}
}

func TestStrictSiblingFlagNamesTheSection(t *testing.T) {
	strictEnv(t)
	t.Setenv("TICKETVOICE_COPE_GATE", fakeSiblingBinary(t, "cope-gate", copeFixture("flip")))
	out := strictCall(t, "mcp__linear-strict__create_issue", map[string]any{
		"title": "t", "team": "ENG", "sections": []any{map[string]any{"section": "Fix", "body": words(20)}},
	})
	if out == nil || out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("a cope hit must be refused, got %+v", out)
	}
	if reason := out.HookSpecificOutput.PermissionDecisionReason; !strings.HasPrefix(reason, "[Fix section] Inside its budget") {
		t.Errorf("reason must lead with the section it is about:\n%s", reason)
	}
}

// A rewrite goes back into the section it came from, untagged, and every other field survives.
func TestStrictRewriteLandsInItsSection(t *testing.T) {
	strictEnv(t)
	isolateAutorewriteEnv(t)
	_, calls := fakeAutorewriteServer(t, "🤖 "+words(40))
	out := strictCall(t, "mcp__linear-strict__set_state", map[string]any{
		"issue": "ENG-1", "patch": []any{
			map[string]any{"section": "Observed", "mode": "append", "body": observedLine(10)},
			map[string]any{"section": "Cause", "mode": "replace", "body": words(150)},
		},
	})
	if out == nil || out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("a clean rewrite must allow, got %+v", out)
	}
	var got struct {
		Issue string `json:"issue"`
		Patch []struct {
			Section, Mode, Body string
		} `json:"patch"`
	}
	if err := json.Unmarshal(out.HookSpecificOutput.UpdatedInput, &got); err != nil {
		t.Fatal(err)
	}
	if got.Issue != "ENG-1" || len(got.Patch) != 2 || got.Patch[0].Body != observedLine(10) || got.Patch[1].Mode != "replace" {
		t.Fatalf("fields outside the rewritten section changed: %+v", got)
	}
	if got.Patch[1].Body != words(40) {
		t.Fatalf("the Cause section must carry the untagged rewrite, got %q", got.Patch[1].Body)
	}
	if *calls != 1 {
		t.Fatalf("want one rewrite call, for Cause only, got %d", *calls)
	}
	if ctx := out.HookSpecificOutput.AdditionalContext; !strings.Contains(ctx, "rewrote the Cause section") {
		t.Errorf("the rewrite must be disclosed, got %q", ctx)
	}
}
