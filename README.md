# ticketvoice

[![ci](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml/badge.svg)](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml)

A Claude Code `PreToolUse` hook that holds ticket prose to a word budget: 150 words for an issue
or PR description, 120 for a comment, fenced code excluded. Over budget it returns
`permissionDecision: "ask"`, so a human decides — a ticket that needs the length stays possible,
and the assistant can't wave its own ticket through. Silent when the body is inside the budget.

Covers two surfaces: Linear, via its MCP tools' structured `description`/`body` fields, and
GitHub issues/PRs, via [`gh-write`](#gh-write-github-issues-and-prs) — a thin wrapper this repo
also builds, which is the only way this hook can see a GitHub body at all (see that section for
why a plain `gh issue create --body "..."` can't be gated).

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
make install   # builds ticketvoice and gh-write to $(go env GOPATH)/bin, version from git describe
```

Then wire `ticketvoice` into `~/.claude/settings.json` as a `PreToolUse` hook on the two Linear
write tools and on `Bash` (for `gh-write` calls — see below). The path has to be absolute — hooks
run in whatever environment Claude Code was launched from, which may not have your Go bin
directory on `PATH`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "mcp__linear__save_issue|mcp__linear__save_comment|Bash",
        "hooks": [
          { "type": "command", "command": "/home/you/go/bin/ticketvoice" }
        ]
      }
    ]
  }
}
```

There's no installer subcommand — this is a plain hook binary, wired by hand once. Matching on
`Bash` runs ticketvoice on every Bash call, but it's a fast regex check that returns immediately
for anything that isn't a `gh-write` invocation — see [Development](#development) for the cost.

## gh-write: GitHub issues and PRs

`gh-write` is a companion binary this repo also builds — `gh issue`/`gh pr`, but the body always
comes from stdin instead of a `--body`/`--body-file` flag:

```bash
gh-write issue create --title "Bug: X" --repo you/repo <<'EOF'
Whatever the body is. No shell escaping to think about — it's a heredoc, not a quoted argument.
EOF

gh-write pr comment 42 <<'EOF'
lgtm
EOF
```

Everything gh-write doesn't recognize (`--repo`, `--label`, `--base`, `--draft`, ...) passes
straight through to `gh`, unchanged. `--body`, `-b`, `--body-file`, `-F`, and their `=value` forms
are refused outright — gh-write's whole purpose is making the heredoc the only path, not one
convention among several.

**Why this exists**, and why it isn't as simple as pointing ticketvoice's matcher at `gh` itself:
ticketvoice reads a Bash `PreToolUse` call's `tool_input.command` — the same opaque shell string
Claude submitted, not a parsed argv. Linear's MCP tools hand it a clean JSON `description`/`body`
field; a raw `gh issue create --title "..." --body "..."` hands it one shell-quoted line, and
`--body`, `--body-file`, `--notes`/`--notes-file` differ across `issue`/`pr`/`release` and
`create`/`comment`/`edit`, each with its own escaping and heredoc/file-path variants. Reliably
pulling prose out of that would need a real shell tokenizer, and a tokenizer that gets it wrong
doesn't fail open the way an unparseable Linear call does — it can match the wrong span (a
`--title` instead of a `--body`) and report a plausible, wrong verdict, which is worse than no
gate at all.

gh-write turns that into a much narrower problem: it owns a single, fixed CLI grammar, so the
only thing ticketvoice has to find is `gh-write (issue|pr) (create|comment|edit)` followed by a
heredoc — literal text between two markers, no shell escaping inside it, extractable with a plain
string search (`ghWriteProse` in `main.go`). Covers issue and PR create/comment/edit, matching the
Linear surface this hook already covers (issues and comments) — not release notes, which are a
different genre (a changelog, not a ticket) that this gate isn't shaped for.

## Configuration

`TICKETVOICE_MAX_WORDS` overrides the budget for the current call — issue or comment. Set it in the
hook's environment: `TICKETVOICE_MAX_WORDS=200`.

`TICKETVOICE_COPE_GATE` and `TICKETVOICE_BASANITE` point at those binaries if they aren't on `PATH`.
Missing or unreachable is not an error for either — the call just isn't scored against that sibling's
rules that time.

## What it counts

Prose only — ticketvoice strips fenced code before counting, since code is the part of a ticket
that's supposed to be long. A patch-based edit is counted on its inserted text alone; it leaves the
rest of the body alone. Below 200 words, a body with section headers gets an extra line in the
reason: headers cost two lines each and imply more document than there is.

## Where this sits next to cope and basanite

Two other tools watch the same writes — [basanite](https://github.com/justinstimatze/basanite)
for vocabulary tics, [cope](https://github.com/justinstimatze/cope) for voicing and structure — and
neither blocks on its own: `additionalContext`, after the call already went out. Ticketvoice does
block, and it isn't judging on word count alone: it forwards its own stdin to `cope-gate -pretool`
and `basanite writecheck -no-dedup` directly and asks on either verdict too, so a within-budget
ticket carrying a flagged tic gets held for a human the same as an over-length one. See
[CHANGELOG.md](CHANGELOG.md) for how basanite's dedup state made this need a new flag on its side.

Their own `PreToolUse` matchers are still Linear-only — cope's `-pretool` and basanite's
`writecheck` aren't wired to `Bash`, so a `gh-write` call only gets scored on the way in, through
ticketvoice's own forwarding above, not through their independently-registered hooks the way a
Linear call is. That's fine: ticketvoice forwards the exact bytes either sibling would have
scored, so the verdict is the same either path.

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
