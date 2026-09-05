package citecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/ticketvoice/internal/linearclient"
)

// fakeLinear serves TeamKeys from a fixed set and Exists from a real/missing set, mirroring the
// verified live response shapes.
func fakeLinear(t *testing.T, teamKeys []string, realIDs []string) *linearclient.Client {
	t.Helper()
	real := map[string]bool{}
	for _, id := range realIDs {
		real[id] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(body.Query, "teams"):
			nodes := make([]map[string]string, len(teamKeys))
			for i, k := range teamKeys {
				nodes[i] = map[string]string{"key": k}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"teams": map[string]any{"nodes": nodes}},
			})
		case strings.Contains(body.Query, "issue"):
			id, _ := body.Variables["id"].(string)
			if real[id] {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{"id": "uuid-" + id}}})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"data": nil,
					"errors": []map[string]any{{
						"message":    "Entity not found: Issue",
						"extensions": map[string]any{"code": "INPUT_ERROR"},
					}},
				})
			}
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TICKETVOICE_STATE_DIR", t.TempDir())
	return &linearclient.Client{Token: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestJudgeTicketIDsIgnoresJargonNotMatchingATeamKey(t *testing.T) {
	client := fakeLinear(t, []string{"ABC", "ENG"}, nil)
	text := "Fixed the UTF-8 decoding bug, updated to GPT-4, matches RFC-2119, unrelated to COVID-19 or SHA-256."
	j, ids := Judge(context.Background(), client, "", text)
	if j.Flagged {
		t.Fatalf("jargon matching the ticket-id shape must never reach Linear, got flagged: %s (ids=%v)", j.Note, ids)
	}
}

func TestJudgeTicketIDsFlagsConfirmedNonexistent(t *testing.T) {
	client := fakeLinear(t, []string{"ABC"}, []string{"ABC-1"})
	j, ids := Judge(context.Background(), client, "", "See ABC-1 for context, and ABC-999 which doesn't exist.")
	if !j.Flagged || !strings.Contains(j.Note, "ABC-999") {
		t.Fatalf("want flagged naming ABC-999, got %+v ids=%v", j, ids)
	}
	if strings.Contains(j.Note, "ABC-1 ") || strings.Contains(j.Note, "ABC-1,") {
		t.Fatalf("a real ticket id must not be flagged: %s", j.Note)
	}
}

func TestJudgeTicketIDsCleanWhenAllReal(t *testing.T) {
	client := fakeLinear(t, []string{"ABC"}, []string{"ABC-1", "ABC-2"})
	j, _ := Judge(context.Background(), client, "", "ABC-1 and ABC-2 are both real.")
	if j.Flagged {
		t.Fatalf("want clean when every cited id exists, got %s", j.Note)
	}
}

func TestJudgeSkipsTicketIDsWithNoClient(t *testing.T) {
	j, ids := Judge(context.Background(), nil, "", "ABC-999999 would fail if checked.")
	if j.Flagged || ids != nil {
		t.Fatalf("with no Linear client, ticket-id checking must fully skip, got %+v ids=%v", j, ids)
	}
}

func TestJudgeFileLineFlagsMissingFile(t *testing.T) {
	dir := t.TempDir()
	text := "See `nope.go:10` for the bug."
	j, ids := Judge(context.Background(), nil, dir, text)
	if !j.Flagged || !strings.Contains(j.Note, "nope.go") {
		t.Fatalf("want flagged naming the missing file, got %+v", j)
	}
	if len(ids) != 1 || ids[0] != "cite:file:nope.go" {
		t.Fatalf("want one file citation id, got %v", ids)
	}
}

func TestJudgeFileLineFlagsLineOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.go")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	j, _ := Judge(context.Background(), nil, dir, "See `real.go:99` for the bug.")
	if !j.Flagged || !strings.Contains(j.Note, "3 lines") {
		t.Fatalf("want flagged naming the real line count, got %+v", j)
	}
}

func TestJudgeFileLineCleanWithinRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.go")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	j, _ := Judge(context.Background(), nil, dir, "See `real.go:2` and `real.go:1-3` for the bug.")
	if j.Flagged {
		t.Fatalf("want clean for lines within range, got %s", j.Note)
	}
}

func TestJudgeFileLineSkipsRelativePathWithNoCwd(t *testing.T) {
	j, _ := Judge(context.Background(), nil, "", "See `some/file.go:10`.")
	if j.Flagged {
		t.Fatalf("a relative path with no cwd can't be resolved and must fail open, got %s", j.Note)
	}
}

// git behavior confirmed by direct test before writing this: both "not a repo" and "SHA doesn't
// exist" exit `git cat-file -e <sha>^{commit}` at code 128 — indistinguishable without first
// confirming cwd is actually a repo.
func setupGitRepo(t *testing.T) (dir, realSHA string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "init")
	return dir, run("rev-parse", "HEAD")
}

func TestJudgeSHACleanForRealCommit(t *testing.T) {
	dir, sha := setupGitRepo(t)
	j, _ := Judge(context.Background(), nil, dir, "Fixed in `"+sha[:12]+"`.")
	if j.Flagged {
		t.Fatalf("want clean for a real commit, got %s", j.Note)
	}
}

func TestJudgeSHAFlagsNonexistentCommitInARealRepo(t *testing.T) {
	dir, _ := setupGitRepo(t)
	j, ids := Judge(context.Background(), nil, dir, "Fixed in `deadbeef1234`.")
	if !j.Flagged || !strings.Contains(j.Note, "deadbeef1234") {
		t.Fatalf("want flagged naming the bogus SHA, got %+v", j)
	}
	if len(ids) != 1 || ids[0] != "cite:sha:deadbeef1234" {
		t.Fatalf("want one sha citation id, got %v", ids)
	}
}

func TestJudgeSHASkipsWhenNotAGitRepo(t *testing.T) {
	dir := t.TempDir() // not a git repo
	j, _ := Judge(context.Background(), nil, dir, "Fixed in `deadbeef1234`.")
	if j.Flagged {
		t.Fatalf("outside a git repo, a SHA-shaped citation must fail open, got %s", j.Note)
	}
}

func TestJudgeSHASkipsWithNoCwd(t *testing.T) {
	j, _ := Judge(context.Background(), nil, "", "Fixed in `deadbeef1234`.")
	if j.Flagged {
		t.Fatalf("with no cwd, SHA checking must skip entirely, got %s", j.Note)
	}
}

func TestJudgeCleanWhenNothingCited(t *testing.T) {
	j, ids := Judge(context.Background(), nil, "", "Just an ordinary sentence with no citations at all.")
	if j.Flagged || ids != nil {
		t.Fatalf("want fully clean, got %+v ids=%v", j, ids)
	}
}
