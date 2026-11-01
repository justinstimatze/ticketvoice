// Package releasenote checks a product release note: each entry against a word budget of its own,
// and the whole note through cope-gate with the release-note card and through basanite.
//
// A release note is a list of entries a reader compares with one another, so the budget is per
// entry rather than per document. One story at four times the length of the next reads as four
// times as important, whatever its content, and that unevenness is what the budget holds down. The
// card (release_note.effigy) carries the register and gates off the built-in rules that read a
// conversation's shape, paragraph_uniformity among them: even entries are the goal here.
//
// The note's shape is the one a release-note drafter writes into Linear and Linear stores: a "## " heading per
// entry, a "(Area)" line, a one-line summary and body paragraphs, and "Fixes & Minor Updates"
// sections whose lines are the minor entries.
package releasenote

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed release_note.effigy
var Card []byte

// Budgets in words. A story is its summary line and body together; a minor entry is one line.
const (
	StoryBudget  = 90
	MinorBudget  = 30
	MinorHeading = "Fixes & Minor Updates"
)

// Entry is one story or one minor line, as parsed from the note.
type Entry struct {
	Title string // the story's heading; for a minor line, the heading of its section
	Text  string // summary and body for a story, the line itself for a minor entry
	// Summary is a story's first line, the one under its area; empty for a minor entry.
	Summary string
	Minor   bool
}

// Result is what a check found. NotRun names each scorer that could not run, so a missing binary
// is never read as a clean pass.
type Result struct {
	Flags  []string `json:"flags"`
	NotRun []string `json:"notRun"`
}

var (
	areaLine = regexp.MustCompile(`^\\?\(.+?\\?\)$`)
	bullet   = regexp.MustCompile(`^[-*]\s+`)
)

// Entries splits the published part of a note (everything above a "---" line) into its entries.
func Entries(note string) []Entry {
	published, _, _ := strings.Cut(note, "\n---\n")
	var out []Entry
	for _, section := range strings.Split("\n"+published, "\n## ")[1:] {
		lines := strings.Split(section, "\n")
		title := strings.TrimSpace(lines[0])
		var body []string
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" && !areaLine.MatchString(l) {
				body = append(body, l)
			}
		}
		minorAt := -1
		if title == MinorHeading {
			minorAt = 0
			body = append([]string{MinorHeading}, body...)
		} else {
			for i, l := range body {
				if l == MinorHeading {
					minorAt = i
					break
				}
			}
		}
		story := body
		if minorAt >= 0 {
			story = body[:minorAt]
			for _, l := range body[minorAt+1:] {
				out = append(out, Entry{Title: title, Text: bullet.ReplaceAllString(l, ""), Minor: true})
			}
		}
		// A story whose only text before its minor list is the one-line lead-in is a container for
		// the list, not a story of its own.
		if minorAt >= 0 && len(story) <= 1 {
			story = nil
		}
		if len(story) > 0 && title != MinorHeading {
			out = append(out, Entry{Title: title, Text: strings.Join(story, " "), Summary: story[0]})
		}
	}
	return out
}

// BudgetFlags names every entry over its budget.
func BudgetFlags(entries []Entry) []string {
	var flags []string
	for _, e := range entries {
		budget, what := StoryBudget, fmt.Sprintf("story %q", e.Title)
		if e.Minor {
			budget, what = MinorBudget, fmt.Sprintf("minor line %q", clip(e.Text, 60))
		}
		if n := len(strings.Fields(e.Text)); n > budget {
			flags = append(flags, fmt.Sprintf("budget: %s is %d words against %d; cut it to the change and what the reader can now do", what, n, budget))
		}
	}
	return flags
}

// A body sentence restates its story's summary when at least RestateShare of its content words
// are already in the summary, and at least RestateMin of them are. The floor keeps a short
// sentence that shares one name with the summary ("It works in Alpha.") from counting.
const (
	RestateShare = 0.6
	RestateMin   = 3
)

var (
	sentenceEnd = regexp.MustCompile(`([.!?])\s+`)
	wordRe      = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}.'’-]*[\p{L}\p{N}]|[\p{L}\p{N}]`)
	// Function words carry no claim, so sharing them says nothing about a repeat.
	stopwords = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields(`a an and are as at be by can each for from has have in is it its
		now of on or so that the their them then there this to under up was we were when which who will
		with you your`) {
		stopwords[w] = true
	}
}

// contentWords lowercases, drops function words and a plural "s", so "answers" and "answer" match.
func contentWords(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if stopwords[w] {
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = w[:len(w)-1]
		}
		out = append(out, w)
	}
	return out
}

// RestateFlags names every story body sentence that says again what the summary already said. A
// story has room for its summary and two or three sentences; one spent repeating the summary
// leaves the reader with less, and reads as padding beside entries that didn't repeat themselves.
func RestateFlags(entries []Entry) []string {
	var flags []string
	for _, e := range entries {
		if e.Minor || e.Summary == "" {
			continue
		}
		have := wordSet(e.Summary)
		body := strings.TrimSpace(strings.TrimPrefix(e.Text, e.Summary))
		for _, sent := range strings.Split(sentenceEnd.ReplaceAllString(body, "$1\n"), "\n") {
			if restates(sent, have) {
				flags = append(flags, fmt.Sprintf("restate: story %q says again in its body what its summary says (%q); cut the sentence or say what the summary left out", e.Title, clip(strings.TrimSpace(sent), 80)))
			}
		}
	}
	return flags
}

func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range contentWords(s) {
		set[w] = true
	}
	return set
}

// restates reports whether enough of sent's content words are already in have.
func restates(sent string, have map[string]bool) bool {
	words := contentWords(sent)
	shared := 0
	for _, w := range words {
		if have[w] {
			shared++
		}
	}
	return shared >= RestateMin && float64(shared) >= RestateShare*float64(len(words))
}

// Runner runs a scorer: its name, its arguments, and the file it reads. Tests replace it.
type Runner func(name string, args ...string) (stdout []byte, err error)

// ExecRunner runs the binary from PATH, or from the TICKETVOICE_COPE_GATE / TICKETVOICE_BASANITE
// override, with a timeout.
func ExecRunner(name string, args ...string) ([]byte, error) {
	bin := name
	if v := os.Getenv(map[string]string{"cope-gate": "TICKETVOICE_COPE_GATE", "basanite": "TICKETVOICE_BASANITE"}[name]); v != "" {
		bin = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Output()
}

// Check runs the budget, cope-gate with the release-note card, and basanite over the note at path.
func Check(path string, run Runner) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	entries := Entries(string(data))
	res := Result{Flags: append(BudgetFlags(entries), RestateFlags(entries)...), NotRun: []string{}}

	dir, err := os.MkdirTemp("", "ticketvoice-release-note-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	card := filepath.Join(dir, "release_note.effigy")
	if err := os.WriteFile(card, Card, 0o600); err != nil {
		return Result{}, err
	}

	if out, err := run("cope-gate", "-rules", card, "-check", path, "-json", "-log", ""); err != nil {
		res.NotRun = append(res.NotRun, "cope-gate ("+why(err)+")")
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			var v struct {
				Error      string `json:"error"`
				Violations []struct {
					RuleID  string `json:"rule_id"`
					Matched string `json:"matched"`
					Why     string `json:"why"`
				} `json:"violations"`
			}
			if line == "" {
				continue
			}
			if json.Unmarshal([]byte(line), &v) != nil || v.Error != "" {
				res.NotRun = append(res.NotRun, "cope-gate (unreadable output)")
				break
			}
			for _, x := range v.Violations {
				res.Flags = append(res.Flags, fmt.Sprintf("cope %s: %q: %s", x.RuleID, x.Matched, x.Why))
			}
		}
	}

	// basanite scores against a report built from this machine's transcripts; without one it exits
	// non-zero and is named as not run, never as clean.
	if out, err := run("basanite", "check", path); err != nil {
		res.NotRun = append(res.NotRun, "basanite ("+why(err)+")")
	} else {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, l := range lines[min(1, len(lines)):] {
			if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "clean, nothing flagged") {
				res.Flags = append(res.Flags, "basanite: "+l)
			}
		}
	}
	return res, nil
}

func why(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			first, _, _ := strings.Cut(msg, "\n")
			return first
		}
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "not installed"
	}
	return err.Error()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
