# ticketvoice

[![ci](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml/badge.svg)](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml)

A Claude Code `PreToolUse` hook that holds Linear ticket prose to a word budget: 150 words for an
issue description, 120 for a comment, fenced code excluded. Over budget it returns
`permissionDecision: "ask"`, so a human decides — a ticket that needs the length stays possible,
and the assistant can't wave its own ticket through. Silent when the body is inside the budget.

## Why

A memory saying "write like a pragmatic staff engineer" held for two weeks and a 238-word ticket
body still shipped anyway, because a register is a taste and a taste can be talked past. A word
count is a limit, and a limit either holds or visibly fails — it can't be quietly reasoned around
mid-generation the way a style note can.

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

`TICKETVOICE_MAX_WORDS` overrides the budget for the current call — issue or comment. Set it in the
hook's environment: `TICKETVOICE_MAX_WORDS=200`.

`TICKETVOICE_COPE_GATE` points at the `cope-gate` binary if it isn't on `PATH`. Missing or
unreachable is not an error — the call just isn't scored against cope's rules that time.

## What it counts

Prose only — ticketvoice strips fenced code before counting, since code is the part of a ticket
that's supposed to be long. A patch-based edit is counted on its inserted text alone; it leaves the
rest of the body alone. Below 200 words, a body with section headers gets an extra line in the
reason: headers cost two lines each and imply more document than there is.

## Where this sits next to cope and basanite

Two other tools watch the same Linear writes — [basanite](https://github.com/justinstimatze/basanite)
for vocabulary tics, [cope](https://github.com/justinstimatze/cope) for voicing and structure — and
neither blocks on its own: `additionalContext`, after the call already went out. Ticketvoice does
block, and as of this version it isn't judging on word count alone: it forwards its own stdin to
`cope-gate -pretool` directly and asks on cope's verdict too, so a within-budget ticket carrying a
flagged tic gets held for a human the same as an over-length one. Basanite isn't wired in yet — its
dedup state makes a second caller unsafe; see [CHANGELOG.md](CHANGELOG.md) for the reason and what
would fix it.

## Development

`git config core.hooksPath hooks` once, after cloning, activates the tracked pre-commit hook —
gofmt, vet, test, `make check-readme`, plus a non-blocking CodeScene delta check when `cs` is on
`PATH`.

`make check-readme` runs the tool's own gate against the Why section above, as if it were a Linear
issue description — the one paragraph in this file written in ticket-body register, so it's the
only fair target. Same convention as cope's `make check-readme` (`cope-gate --check README.md`) and
effigy's `generate_readme.py`, narrowed to what this tool does: it doesn't generate prose, so
there's nothing to write through it, only something to check.

## License

MIT. See `LICENSE`.
