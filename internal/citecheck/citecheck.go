// Package citecheck verifies that a ticket's citations — another ticket id, a file:line, a
// commit SHA — are real, not stale or hallucinated. Every sub-check fails open on anything it
// can't determine (missing Linear token, network failure, no git repo) and flags only a
// citation it can positively confirm is wrong.
//
// This deliberately stops at existence, not truth: verifying a NARRATIVE claim ("already fixed in
// ABC-777," "the file already exists") needs real code-reading judgment, which is an LLM call, not
// a fast deterministic check — out of scope for a synchronous PreToolUse hook. Checking that a
// cited id/path/SHA is real needs no judgment at all, which is what keeps it in scope.
package citecheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/ticketvoice/internal/budgetgate"
	"github.com/justinstimatze/ticketvoice/internal/linearclient"
)

// maxCiteFileBytes caps how much of a cited file this will read to count lines — matches the
// spirit of main.go's maxRedirectBytes: a citation points at a location in a file, not the whole
// file, so anything this large isn't a citation this check needs to open.
const maxCiteFileBytes = 2 << 20

// gitTimeout bounds the local git subprocess calls, matching judgeSibling's 3-second convention
// even though a local git call is normally instant — a hung git process must not hang the hook.
const gitTimeout = 3 * time.Second

var (
	ticketIDPattern = regexp.MustCompile(`\b[A-Z]{2,10}-\d+\b`)
	fileLinePattern = regexp.MustCompile("`([\\w./-]+\\.[A-Za-z0-9]+):(\\d+)(?:-(\\d+))?`")
	shaPattern      = regexp.MustCompile("`([0-9a-fA-F]{7,40})`")
)

// Judge extracts and verifies every citation in text. client may be nil (no
// TICKETVOICE_LINEAR_TOKEN set) — ticket-id verification is skipped entirely in that case; the
// file:line and SHA checks don't need Linear and are unaffected. cwd resolves a relative file
// path and names the repo a SHA is checked against; both checks fail open when cwd is "".
//
// Returns the Judgment plus this call's contribution to the retry sequence's violation-id set —
// one id per confirmed-wrong citation (e.g. "cite:ticket:ABC-550"), so attemptstate's
// attempt-to-attempt delta can name exactly which reference cleared, stayed, or newly appeared.
func Judge(ctx context.Context, client *linearclient.Client, cwd, text string) (budgetgate.Judgment, []string) {
	var notes []string
	var ids []string

	if client != nil {
		missing, truncated := judgeTicketIDs(ctx, client, text)
		if len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("ticket reference(s) don't exist: %s", strings.Join(missing, ", ")))
			for _, m := range missing {
				ids = append(ids, "cite:ticket:"+m)
			}
			if truncated > 0 {
				notes = append(notes, fmt.Sprintf("(%d more ticket reference(s) exceeded the check cap and weren't verified)", truncated))
			}
		}
	}

	if missing, missingIDs := judgeFileLines(cwd, text); len(missing) > 0 {
		notes = append(notes, missing...)
		ids = append(ids, missingIDs...)
	}

	if missing, missingIDs := judgeSHAs(cwd, text); len(missing) > 0 {
		notes = append(notes, missing...)
		ids = append(ids, missingIDs...)
	}

	if len(notes) == 0 {
		return budgetgate.Judgment{}, nil
	}
	sort.Strings(ids)
	return budgetgate.Judgment{Flagged: true, Note: strings.Join(notes, "\n")}, ids
}

// citeCap bounds how many distinct ticket ids get checked per call — TICKETVOICE_LINEAR_CITE_CAP,
// default 5. Bounds latency and Linear rate-limit exposure on a ticket that cites many ids.
func citeCap() int {
	if v := os.Getenv("TICKETVOICE_LINEAR_CITE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// judgeTicketIDs verifies each distinct, team-key-gated ticket id citation in text, in parallel.
// The team-key gate is what keeps ordinary jargon (UTF-8, SHA-256, GPT-4, RFC-2119, COVID-19) —
// which matches the same [A-Z]{2,10}-\d+ shape — from ever being sent to Linear at all: none of
// them share a prefix with a real team key, so they're filtered out before the network call that
// would otherwise, correctly, report them as nonexistent and wrongly deny a legitimate write.
func judgeTicketIDs(ctx context.Context, client *linearclient.Client, text string) (missing []string, truncated int) {
	teamKeys, err := client.TeamKeys(ctx)
	if err != nil || len(teamKeys) == 0 {
		return nil, 0
	}
	keySet := make(map[string]bool, len(teamKeys))
	for _, k := range teamKeys {
		keySet[strings.ToUpper(k)] = true
	}

	seen := map[string]bool{}
	var candidates []string
	for _, m := range ticketIDPattern.FindAllString(text, -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		if i := strings.IndexByte(m, '-'); i > 0 && keySet[strings.ToUpper(m[:i])] {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return nil, 0
	}

	limit := citeCap()
	if len(candidates) > limit {
		truncated = len(candidates) - limit
		candidates = candidates[:limit]
	}

	type result struct {
		id     string
		exists bool
		err    error
	}
	out := make(chan result, len(candidates))
	var wg sync.WaitGroup
	for _, id := range candidates {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			exists, err := client.Exists(ctx, id)
			out <- result{id, exists, err}
		}(id)
	}
	wg.Wait()
	close(out)

	for r := range out {
		if r.err == nil && !r.exists {
			missing = append(missing, r.id)
		}
	}
	sort.Strings(missing)
	return missing, truncated
}

// resolvePath applies the same join-if-relative convention ghWriteProse's redirect handling uses
// in main.go: an absolute path is used as-is, a relative one is resolved against cwd, and a
// relative path with no known cwd can't be resolved at all — that citation is skipped, not
// treated as missing.
func resolvePath(cwd, path string) (resolved string, ok bool) {
	if filepath.IsAbs(path) {
		return path, true
	}
	if cwd == "" {
		return "", false
	}
	return filepath.Join(cwd, path), true
}

// lineCount counts a file's lines the way an editor would: a trailing newline ends the last
// line, it doesn't start a phantom empty one after it. "a\nb\n" is 2 lines, not 3.
func lineCount(data []byte) int {
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// judgeFileLines checks each backtick-fenced `path:line` or `path:start-end` citation. A path
// with no extension (Makefile, Dockerfile) doesn't match fileLinePattern at all — a known,
// accepted v1 gap, not silently mishandled.
func judgeFileLines(cwd, text string) (notes []string, ids []string) {
	for _, m := range fileLinePattern.FindAllStringSubmatch(text, -1) {
		path, lineStr, endStr := m[1], m[2], m[3]
		full, ok := resolvePath(cwd, path)
		if !ok {
			continue
		}
		line, err := strconv.Atoi(lineStr)
		if err != nil {
			continue
		}
		want := line
		if endStr != "" {
			if end, err := strconv.Atoi(endStr); err == nil && end > want {
				want = end
			}
		}

		fi, err := os.Stat(full)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s doesn't exist", path))
			ids = append(ids, "cite:file:"+path)
			continue
		}
		if fi.Size() > maxCiteFileBytes {
			continue // too large to safely read; not this check's job, fail open
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue // fail open on an unreadable file (permissions, race with deletion)
		}
		count := lineCount(data)
		if want > count {
			notes = append(notes, fmt.Sprintf("%s has %d lines, but %s cites line %d", path, count, path, want))
			ids = append(ids, fmt.Sprintf("cite:file:%s:%d", path, line))
		}
	}
	return notes, ids
}

// isGitRepo reports whether cwd is inside a git work tree. This exists because git cat-file -e
// exits 128 both when cwd isn't a repo at all AND when the SHA genuinely doesn't exist —
// confirmed by direct test — so "not a repo" must be ruled out first, once per call, before a
// cat-file failure can be read as "confirmed nonexistent commit."
func isGitRepo(ctx context.Context, cwd string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

func shaExists(ctx context.Context, cwd, sha string) bool {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "cat-file", "-e", sha+"^{commit}")
	return cmd.Run() == nil
}

// judgeSHAs checks each backtick-fenced 7-40 hex-character citation against the local repo at
// cwd. Skips entirely when cwd is unset, git isn't installed, or cwd isn't a repo — a SHA belongs
// to a specific project's history, and this can only check the one the hook is already sitting
// in.
func judgeSHAs(cwd, text string) (notes []string, ids []string) {
	if cwd == "" {
		return nil, nil
	}
	ctx := context.Background()
	if !isGitRepo(ctx, cwd) {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, m := range shaPattern.FindAllStringSubmatch(text, -1) {
		sha := m[1]
		if seen[sha] {
			continue
		}
		seen[sha] = true
		if !shaExists(ctx, cwd, sha) {
			notes = append(notes, fmt.Sprintf("`%s` isn't a commit in this repo", sha))
			ids = append(ids, "cite:sha:"+sha)
		}
	}
	return notes, ids
}
