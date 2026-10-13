package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The freshness half of the Stop hook. A ticket named in a reply was too often described from a
// compaction summary or an earlier read rather than from the ticket: a parked ticket reported red,
// three finished ones reported as open work (jupiter, 2026-10-06). A rule to re-read before citing
// was already written down and was not followed, so this check reads the transcript instead: every
// ticket id the agent wrote this turn must have been fully read since the newest compaction, or the
// turn can't end. A list or search result doesn't count, since a title and a state are exactly
// what went stale.

// freshKeys are the Linear team keys whose ids the check looks for. TICKETVOICE_FRESH_KEYS
// overrides it with a comma-separated list; an empty value turns the check off.
func freshKeys() []string {
	v, set := os.LookupEnv("TICKETVOICE_FRESH_KEYS")
	if !set {
		return []string{"CUR"}
	}
	var keys []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, regexp.QuoteMeta(k))
		}
	}
	return keys
}

// fullReadTools return a whole ticket. A tool not listed here (list_issues, search) proves only
// that a title went past.
var fullReadTools = map[string]string{
	"mcp__linear-strict__get_issue":         "issue",
	"mcp__linear-official__get_issue":       "id",
	"mcp__linear-full__linear_getIssueById": "id",
}

// createTools return the ticket they made, so an id first seen in their result counts as read.
var createTools = map[string]bool{
	"mcp__linear-strict__create_issue": true,
}

type transcriptEntry struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func blocksOf(raw json.RawMessage) ([]contentBlock, string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return nil, s
	}
	var bs []contentBlock
	_ = json.Unmarshal(raw, &bs)
	return bs, ""
}

// entriesSinceCompaction decodes the transcript's user and assistant lines after the newest
// compaction boundary. ok is false when the file can't be opened.
func entriesSinceCompaction(transcriptPath string) ([]transcriptEntry, bool) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	// A long session's transcript runs to hundreds of thousands of lines, and decoding all of them
	// took 7.6 s against the hook's 5 s timeout. Only lines after the newest compaction matter, and
	// a byte search finds it without decoding anything before it.
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.Contains(line, []byte(`"compact_boundary"`)) {
			var e transcriptEntry
			if json.Unmarshal(line, &e) == nil && e.Type == "system" && e.Subtype == "compact_boundary" {
				lines = lines[:0]
				continue
			}
		}
		if !bytes.Contains(line, []byte(`"type":"user"`)) && !bytes.Contains(line, []byte(`"type":"assistant"`)) {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	var entries []transcriptEntry
	for _, line := range lines {
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil {
			continue // a torn line is skipped, not fatal
		}
		entries = append(entries, e)
	}
	return entries, true
}

// staleTicketMentions returns, sorted, each ticket id the agent wrote in this turn's text that has
// no full read after the newest compaction boundary. An unreadable transcript yields nothing: the
// check fails open, because a Stop hook that can't read must not hold the session.
func staleTicketMentions(transcriptPath string) []string {
	keys := freshKeys()
	if transcriptPath == "" || len(keys) == 0 {
		return nil
	}
	idRe := regexp.MustCompile(`\b(?:` + strings.Join(keys, "|") + `)-\d+\b`)
	entries, ok := entriesSinceCompaction(transcriptPath)
	if !ok {
		return nil
	}

	boundary, turnStart := -1, -1
	for i, e := range entries {
		if e.Type == "user" && !e.IsMeta {
			bs, s := blocksOf(e.Message.Content)
			if s != "" {
				turnStart = i
				continue
			}
			for _, b := range bs {
				if b.Type == "text" {
					turnStart = i
					break
				}
			}
		}
	}

	read := map[string]bool{}
	pendingCreate := map[string]bool{}
	mentioned := map[string]bool{}
	for i := boundary + 1; i < len(entries); i++ {
		e := entries[i]
		bs, _ := blocksOf(e.Message.Content)
		for _, b := range bs {
			switch {
			case e.Type == "assistant" && b.Type == "tool_use":
				if field, ok := fullReadTools[b.Name]; ok {
					var in map[string]any
					if json.Unmarshal(b.Input, &in) == nil {
						if v, ok := in[field].(string); ok {
							read[v] = true
						}
					}
				}
				if b.Name == "Bash" && strings.Contains(string(b.Input), "ticket-facts") {
					for _, id := range idRe.FindAllString(string(b.Input), -1) {
						read[id] = true
					}
				}
				if createTools[b.Name] {
					pendingCreate[b.ID] = true
				}
			case e.Type == "user" && b.Type == "tool_result" && pendingCreate[b.ToolUseID]:
				var out struct {
					Identifier string `json:"identifier"`
				}
				text := string(b.Content)
				var parts []contentBlock
				if json.Unmarshal(b.Content, &parts) == nil && len(parts) > 0 {
					text = parts[0].Text
				} else {
					var s string
					if json.Unmarshal(b.Content, &s) == nil {
						text = s
					}
				}
				if json.Unmarshal([]byte(text), &out) == nil && out.Identifier != "" {
					read[out.Identifier] = true
				}
			case e.Type == "assistant" && b.Type == "text" && i > turnStart:
				for _, id := range idRe.FindAllString(b.Text, -1) {
					mentioned[id] = true
				}
			}
		}
	}

	var stale []string
	for id := range mentioned {
		if !read[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale
}

func freshReason(stale []string) string {
	return fmt.Sprintf("This turn names %s without a full read of %s since the last compaction. "+
		"Whatever you said about %s came from memory or a summary, which is how parked tickets got "+
		"reported as red and finished ones as open work. Read each with linear-strict get_issue "+
		"(parallel calls are fine), or for status run `node web/scripts/ticket-facts.mjs %s`, then "+
		"correct anything your reply got wrong before ending the turn.",
		strings.Join(stale, ", "), plural(len(stale), "it", "them"), plural(len(stale), "it", "them"),
		strings.Join(stale, " "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
