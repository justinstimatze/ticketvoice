package main

import (
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// The UserPromptSubmit half of the freshness check. The Stop hook catches a reply that names an
// unread ticket only after the reply is written; by then a whole design can rest on a compaction
// summary. This one nudges before the reply: if the branch's ticket (or its parent) hasn't been
// fully read since the newest compaction, it adds context telling the agent to read them first.

// ticketFromBranch returns the ticket id a branch name carries, such as
// "venusjustin/cur-3875-reattach-keeps-the-answer" -> "CUR-3875", using the freshKeys team keys.
func ticketFromBranch(branch string) string {
	keys := freshKeys()
	if len(keys) == 0 {
		return ""
	}
	re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])((?:` + strings.Join(keys, "|") + `)-\d+)\b`)
	m := re.FindStringSubmatch(branch)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

func gitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		return "" // detached
	}
	return b
}

var parentRe = regexp.MustCompile(`"parent"\s*:\s*\{[^{}]*?"identifier"\s*:\s*"([A-Za-z]+-\d+)"`)

// resultText flattens a tool_result's content to text.
func resultText(raw json.RawMessage) string {
	var parts []contentBlock
	if json.Unmarshal(raw, &parts) == nil && len(parts) > 0 {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// parentOf reads a get_issue result: the parent's id, and whether the result already carries the
// parent's plan (a non-empty parent_plan field), which counts as reading the parent.
func parentOf(text string) (parent string, hasPlan bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &m) == nil {
		if pp, ok := m["parent_plan"]; ok {
			s := strings.TrimSpace(string(pp))
			hasPlan = s != "" && s != "null" && s != `""` && s != "{}"
		}
		var p struct {
			Identifier string `json:"identifier"`
		}
		if json.Unmarshal(m["parent"], &p) == nil {
			parent = strings.ToUpper(p.Identifier)
		}
		return parent, hasPlan
	}
	if mm := parentRe.FindStringSubmatch(text); mm != nil {
		parent = strings.ToUpper(mm[1])
	}
	return parent, strings.Contains(text, `"parent_plan"`) && !strings.Contains(text, `"parent_plan":null`)
}

// promptNudge returns the context to add for the ticket on a branch, or "" when nothing is owed.
// An unreadable transcript returns "": a nudge that can't read must stay quiet.
func promptNudge(transcriptPath, ticket string) string {
	if ticket == "" || transcriptPath == "" {
		return ""
	}
	entries, ok := entriesSinceCompaction(transcriptPath)
	if !ok {
		return ""
	}
	idRe := regexp.MustCompile(`\b[A-Za-z]+-\d+\b`)
	read := map[string]bool{}
	pending := map[string]string{} // tool_use id -> ticket it fully reads
	var parent string
	var planRead bool // the newest full read of the ticket carried parent_plan
	for _, e := range entries {
		bs, _ := blocksOf(e.Message.Content)
		for _, b := range bs {
			switch {
			case e.Type == "assistant" && b.Type == "tool_use":
				if field, ok := fullReadTools[b.Name]; ok {
					var in map[string]any
					if json.Unmarshal(b.Input, &in) == nil {
						if v, ok := in[field].(string); ok {
							v = strings.ToUpper(v)
							read[v] = true
							pending[b.ID] = v
						}
					}
				}
				if b.Name == "Bash" && strings.Contains(string(b.Input), "ticket-facts") {
					for _, id := range idRe.FindAllString(string(b.Input), -1) {
						read[strings.ToUpper(id)] = true
					}
				}
			case e.Type == "user" && b.Type == "tool_result":
				if id, ok := pending[b.ToolUseID]; ok && id == ticket {
					parent, planRead = parentOf(resultText(b.Content))
				}
			}
		}
	}
	if !read[ticket] {
		return "This branch's ticket " + ticket + " has not been read in full since the last compaction. " +
			"Read it with linear-strict get_issue before answering about its design or status."
	}
	if parent != "" && !planRead && !read[parent] {
		return "This branch's ticket " + ticket + " was read, but its parent " + parent + " has not been read in full " +
			"since the last compaction. Read " + parent + " with linear-strict get_issue before answering about the design or status."
	}
	return ""
}

// runPrompt is the UserPromptSubmit hook. It fails quiet on every error (no stdin, no git, no
// branch id, unreadable transcript): a nudge that breaks must never get in the way of the prompt.
func runPrompt(stdin io.Reader, stdout io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	var in struct {
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return 0
	}
	ticket := ticketFromBranch(gitBranch(in.Cwd))
	msg := promptNudge(in.TranscriptPath, ticket)
	if msg == "" {
		return 0
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":     "UserPromptSubmit",
		"additionalContext": msg,
	}})
	return 0
}
