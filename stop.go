package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/justinstimatze/ticketvoice/internal/attemptstate"
)

// maxStops is how many turn ends one denied write may block before the Stop hook gives up on it
// and logs it as dropped. Three matches the PreToolUse escalation and plancheck's gate: a block the
// session has walked past three times is a loop, and a loop holding the session hostage is worse
// than one write that never landed and says so in dropped.jsonl.
const maxStops = 3

// runStop is the Stop hook. A denied Linear write used to be easy to walk away from: the deny
// reached the agent mid-turn, the agent moved on or asked the operator to rewrite it, and the
// write never landed. While this session has a denied write it hasn't rewritten, the turn can't
// end: the hook blocks it and names each write, so the agent revises and posts it, or drops it on
// purpose with `ticketvoice drop`. Every other outcome, including a parse failure, allows.
func runStop(stdin io.Reader, stdout io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	var in struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return 0
	}
	var live []attemptstate.Pending
	for _, p := range attemptstate.PendingFor(in.SessionID) {
		// A record with no label predates this hook (2026-10-06) and carries no reason to show.
		if p.Record.Label == "" {
			continue
		}
		if p.Record.Stops >= maxStops {
			attemptstate.LogDropped(in.SessionID, p, fmt.Sprintf("Stop hook gave up after %d blocked turn ends", maxStops))
			p.Remove()
			continue
		}
		live = append(live, p)
	}
	if len(live) == 0 {
		// A turn this hook already held once may end: the agent has seen the freshness block, and
		// holding it again would loop on an id it can't read (a deleted ticket, a typo).
		if in.StopHookActive {
			return 0
		}
		if stale := staleTicketMentions(in.TranscriptPath); len(stale) > 0 {
			_ = json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": freshReason(stale)})
		}
		return 0
	}

	var b strings.Builder
	fmt.Fprintf(&b, "ticketvoice denied %d Linear write(s) this session that never landed. Rewrite each one "+
		"yourself against its reason and post it now; don't end the turn on it or hand it to the operator.\n", len(live))
	for _, p := range live {
		r := p.Record
		r.Stops++
		p.Update(r)

		fmt.Fprintf(&b, "\n[%s] %s — block %d of %d\n%s\n", p.ID, r.Label, r.Stops, maxStops, r.Reason)
	}
	b.WriteString("\nIf a write is no longer wanted (superseded, or posted another way), drop it on the record: " +
		"`ticketvoice drop <id> --reason \"<why>\"`.")
	_ = json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": b.String()})
	return 0
}

// runDrop clears one pending write by its short id and logs why, for a write the author decided
// not to post. A reason is required: a silent drop is the failure the Stop hook exists to catch.
func runDrop(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("drop", flag.ContinueOnError)
	why := fs.String("reason", "", "why this write is no longer wanted")
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	}
	if id == "" || strings.TrimSpace(*why) == "" {
		fmt.Fprintln(stdout, `usage: ticketvoice drop <id> --reason "<why>"`)
		return 2
	}
	p, ok := attemptstate.FindByID(id)
	if !ok {
		fmt.Fprintf(stdout, "no pending write with id %s\n", id)
		return 1
	}
	attemptstate.LogDropped("", p, "dropped by author: "+*why)
	p.Remove()
	fmt.Fprintf(stdout, "dropped %s: %s\n", id, p.Record.Label)
	return 0
}
