package main

import (
	"strings"
	"testing"
)

func issueResult(id, parent string, extra string) string {
	p := ""
	if parent != "" {
		p = `"parent":{"id":"x","identifier":"` + parent + `","title":"p"},`
	}
	return `{"identifier":"` + id + `",` + p + extra + `"title":"t"}`
}

func readWithResult(id, result string) []string {
	return []string{getIssue(id), tToolResult("tu-"+id, result)}
}

func TestTicketFromBranch(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	for branch, want := range map[string]string{
		"venusjustin/cur-3875-reattach-keeps-the-answer": "CUR-3875",
		"justin/CUR-12-x":    "CUR-12",
		"main":               "",
		"feature/occur-12-x": "",
		"":                   "",
	} {
		if got := ticketFromBranch(branch); got != want {
			t.Errorf("ticketFromBranch(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestPromptNudgesAnUnreadBranchTicket(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	p := writeTranscript(t, tCompact, tUser("design it"))
	if got := promptNudge(p, "CUR-3875"); !strings.Contains(got, "CUR-3875") {
		t.Fatalf("an unread ticket must be named in the nudge: %q", got)
	}
}

func TestPromptSilentWhenReadSinceCompactionWithNoParent(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	lines := append([]string{tCompact, tUser("go")}, readWithResult("CUR-3875", issueResult("CUR-3875", "", ""))...)
	if got := promptNudge(writeTranscript(t, lines...), "CUR-3875"); got != "" {
		t.Fatalf("a fresh read with no parent owes nothing: %q", got)
	}
}

func TestPromptNudgesWhenTheReadPredatesTheNewestCompaction(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	lines := append(readWithResult("CUR-3875", issueResult("CUR-3875", "", "")), tCompact, tUser("go"))
	if got := promptNudge(writeTranscript(t, lines...), "CUR-3875"); !strings.Contains(got, "CUR-3875") {
		t.Fatalf("a read from before the compaction is a memory: %q", got)
	}
}

func TestPromptNamesAnUnreadParent(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	lines := append([]string{tCompact, tUser("go")}, readWithResult("CUR-3875", issueResult("CUR-3875", "CUR-3800", ""))...)
	got := promptNudge(writeTranscript(t, lines...), "CUR-3875")
	if !strings.Contains(got, "parent CUR-3800") {
		t.Fatalf("an unread parent must be named: %q", got)
	}
	lines = append(lines, getIssue("CUR-3800"))
	if got := promptNudge(writeTranscript(t, lines...), "CUR-3875"); got != "" {
		t.Fatalf("parent read since compaction satisfies the check: %q", got)
	}
}

func TestPromptParentPlanFieldCountsAsReadingTheParent(t *testing.T) {
	t.Setenv("TICKETVOICE_FRESH_KEYS", "CUR")
	res := issueResult("CUR-3875", "CUR-3800", `"parent_plan":"step 1",`)
	lines := append([]string{tCompact, tUser("go")}, readWithResult("CUR-3875", res)...)
	if got := promptNudge(writeTranscript(t, lines...), "CUR-3875"); got != "" {
		t.Fatalf("parent_plan in the child's result is the parent's plan: %q", got)
	}
}

func TestRunPromptFailsQuietAndEmitsHookJSON(t *testing.T) {
	var out strings.Builder
	if runPrompt(strings.NewReader("not json"), &out) != 0 || out.String() != "" {
		t.Fatalf("bad stdin must print nothing: %q", out.String())
	}
	out.Reset()
	if runPrompt(strings.NewReader(`{"cwd":"`+t.TempDir()+`"}`), &out) != 0 || out.String() != "" {
		t.Fatalf("no git must print nothing: %q", out.String())
	}
}
