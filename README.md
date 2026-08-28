# ticketvoice

[![ci](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml/badge.svg)](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml)

A Claude Code `PreToolUse` hook that holds Linear ticket prose to a word budget: 150 words for an
issue description, 120 for a comment, fenced code excluded. Over budget it returns
`permissionDecision: "ask"`, so a human decides — a ticket that genuinely needs the length stays
possible, and the assistant cannot approve its own gate. Silent when the body is inside the budget.

## Why

A memory saying "write like a pragmatic staff engineer" was in force for two weeks and a 238-word
ticket body still shipped anyway, because a register is a taste and a taste can be talked past. A
word count is a limit, and a limit either holds or visibly fails — it can't be quietly reasoned
around mid-generation the way a style note can.

## Install

```bash
git clone https://github.com/justinstimatze/ticketvoice
cd ticketvoice
make install   # builds to $(go env GOPATH)/bin/ticketvoice, version from git describe
```

Then wire it into `~/.claude/settings.json` as a `PreToolUse` hook on the two Linear write tools.
The path has to be absolute — hooks run in whatever environment Claude Code was launched from, which
may not have your Go bin directory on `PATH`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "mcp__linear__save_issue|mcp__linear__save_comment",
        "hooks": [
          { "type": "command", "command": "/home/you/go/bin/ticketvoice" }
        ]
      }
    ]
  }
}
```

There's no installer subcommand — this is a plain hook binary, wired by hand once.

## Configuration

`TICKETVOICE_MAX_WORDS` overrides the budget for whichever tool call is in flight (issue or
comment), if you want a different number than the defaults.

## What it counts

Prose only — fenced code blocks are stripped before counting, since code is the part of a ticket
that's supposed to be long. A patch-based edit is counted on its inserted text alone; the rest of
the body isn't being rewritten. Below 200 words, a body carrying section headers gets an extra line
in the reason noting that headers cost two lines each and imply more document than there is.

## What the "ask" looks like

```
This issue description is 238 words of prose against a 150-word budget — 88 over.

Four slots, in this order:
  1. The mechanism, one paragraph — what is broken, and why nothing catches it.
  2. Evidence it is real — a SHA, a log line, a failing assertion. One sentence.
  3. What is still exposed — file:line, not a description of the file.
  4. The fix, as a code block, plus one line on how to prove it can go red.
SHAs and file:line carry the detail; do not narrate what the reader can open.

Cut it and call again, or approve to send as written.
```

## Where this sits next to cope and basanite

Two sibling tools watch the same Linear writes — [basanite](https://github.com/justinstimatze/basanite)
(vocabulary tics) and [cope](https://github.com/justinstimatze/cope) (voicing and structure) — and
both, independently, chose never to block: `additionalContext` only, awareness after the call
already went out. That's not a gap this tool exists to close. They're general detectors tuned for a
low false-positive default across everyone who runs them; ticketvoice is one operator's policy on
one destination, and a policy is allowed to be stricter than the mechanism reporting to it.
Ticketvoice is the only one of the three that holds a call for a human to see before it posts — not
by knowing more (it still knows nothing but a word count), but by being the one piece on this
surface whose job is deciding whether a human needs to look. The fuller case, including what a
tighter integration between the three would actually require, is in [CHANGELOG.md](CHANGELOG.md).

## License

MIT. See `LICENSE`.
