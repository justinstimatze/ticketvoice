package main

import (
	"encoding/json"
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
