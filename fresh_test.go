package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Transcript lines in the shape Claude Code writes them.
func tUser(text string) string {
	return `{"type":"user","message":{"role":"user","content":` + jsonString(text) + `}}`
}
func tAssistantText(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + jsonString(text) + `}]}}`
}
func tToolUse(id, name, input string) string {
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}
func tToolResult(id, text string) string {
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":[{"type":"text","text":` + jsonString(text) + `}]}]}}`
}

const tCompact = `{"type":"system","subtype":"compact_boundary"}`

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func getIssue(id string) string {
	return tToolUse("tu-"+id, "mcp__linear-strict__get_issue", `{"issue":"`+id+`"}`)
}

func TestFreshBlocksATicketReadOnlyBeforeTheLastCompaction(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t,
		getIssue("CUR-2729"),
		tCompact,
		tUser("rerank our todo list"),
		tAssistantText("3. The guest attach spec is red (CUR-2729)."),
	)
	if got := staleTicketMentions(p); !reflect.DeepEqual(got, []string{"CUR-2729"}) {
		t.Fatalf("a read from before the compaction is a memory, not a read: got %v", got)
	}
}

func TestFreshAllowsATicketFullyReadSinceTheCompaction(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t,
		tCompact,
		tUser("rerank"),
		getIssue("CUR-2729"),
		tAssistantText("CUR-2729 is parked."),
	)
	if got := staleTicketMentions(p); len(got) != 0 {
		t.Fatalf("got %v, want nothing stale", got)
	}
}

func TestFreshDoesNotCountAListResultAsARead(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t,
		tCompact,
		tUser("rerank"),
		tToolUse("l1", "mcp__linear-strict__list_issues", `{"query":"mine"}`),
		tToolResult("l1", `[{"identifier":"CUR-2952","title":"A1 step 18"}]`),
		tAssistantText("CUR-2952 still needs work."),
	)
	if got := staleTicketMentions(p); !reflect.DeepEqual(got, []string{"CUR-2952"}) {
		t.Fatalf("a title in a list is exactly what goes stale: got %v", got)
	}
}

func TestFreshCountsTicketFactsAndATicketThisTurnCreated(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t,
		tCompact,
		tUser("go"),
		tToolUse("b1", "Bash", `{"command":"node web/scripts/ticket-facts.mjs CUR-1 CUR-2"}`),
		tToolUse("c1", "mcp__linear-strict__create_issue", `{"team":"CUR","title":"x"}`),
		tToolResult("c1", `{"identifier":"CUR-3038","title":"x"}`),
		tAssistantText("Filed CUR-3038; CUR-1 and CUR-2 are merged."),
	)
	if got := staleTicketMentions(p); len(got) != 0 {
		t.Fatalf("got %v, want nothing stale", got)
	}
}

func TestFreshOnlyChecksTextWrittenThisTurn(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t,
		tCompact,
		tUser("first"),
		tAssistantText("Earlier I mentioned CUR-9."),
		tUser("second"),
		tAssistantText("Nothing to report."),
	)
	if got := staleTicketMentions(p); len(got) != 0 {
		t.Fatalf("a mention from a finished turn already passed its own check: got %v", got)
	}
}

func TestFreshSkipsATornLineAndFailsOpenOnAMissingFile(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t, tCompact, tUser("go"), `{"type":"assist`, tAssistantText("CUR-5 is done."))
	if got := staleTicketMentions(p); !reflect.DeepEqual(got, []string{"CUR-5"}) {
		t.Fatalf("a torn line must not hide the rest: got %v", got)
	}
	if got := staleTicketMentions(filepath.Join(t.TempDir(), "missing.jsonl")); got != nil {
		t.Fatalf("an unreadable transcript must allow: got %v", got)
	}
}

func TestStopBlocksOnAStaleMentionOnceThenLetsTheTurnEnd(t *testing.T) {
	clean(t)
	freshState(t)
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t, tCompact, tUser("go"), tAssistantText("CUR-7 is merged."))
	run := func(active bool) string {
		in, _ := json.Marshal(map[string]any{"session_id": "fresh", "transcript_path": p, "stop_hook_active": active})
		var out bytes.Buffer
		runStop(bytes.NewReader(in), &out)
		return out.String()
	}
	if got := run(false); !strings.Contains(got, `"block"`) || !strings.Contains(got, "CUR-7") {
		t.Fatalf("a stale mention must hold the turn and name the id: %q", got)
	}
	if got := run(true); got != "" {
		t.Fatalf("a turn the hook already held must be allowed to end: %q", got)
	}
}
