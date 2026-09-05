# ticketvoice

[![ci](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml/badge.svg)](https://github.com/justinstimatze/ticketvoice/actions/workflows/ci.yml)

A Claude Code `PreToolUse` hook that gates ticket prose through
[cope](https://github.com/justinstimatze/cope) (voicing and structure) and
[basanite](https://github.com/justinstimatze/basanite) (vocabulary tics) before it posts. Behind
both sits a word budget — 150 words for an issue or PR description, 120 for a comment, fenced code
excluded — as a narrower backstop: neither cope nor basanite is built to score sheer length,
independent of register or vocabulary. Any of the checks flagging a body returns
`permissionDecision: "deny"` — the reason goes to Claude, not a human, so it rewrites and retries on
its own instead of paging anyone. No prompt when a body clears every check — on Linear it still tags
the body as agent-authored before letting it through; see [Agent tag](#agent-tag).

Two more checks, both first-party (built here, not delegated to a sibling binary): an issue
description must state its user-facing impact in plain language — see [Impact
line](#impact-line) — and any ticket-id, file:line, or commit SHA a ticket cites gets verified
against Linear, the local filesystem, and the local git repo, not trusted at face value — see
[Ground-truth citations](#ground-truth-citations).

Covers two surfaces: Linear, via its MCP tools' structured `description`/`body` fields — issues,
comments, and PR-review-thread ("diff") comments and reviews — and GitHub issues/PRs, via
[`gh-write`](#gh-write-github-issues-and-prs) — a thin wrapper this repo also builds, which is the
only way this hook can see a GitHub body at all (see that section for why a plain
`gh issue create --body "..."` can't be gated).

Deliberately not covered on the Linear side: project/initiative descriptions, status updates,
documents, milestones, release notes. Same reasoning as GitHub's release notes below — a different
genre of writing than a ticket, not a gap that was missed.

## Why

A memory saying "write like a pragmatic staff engineer" held, and ticket bodies still ran long
anyway — a recurring habit, not a one-off: a memory is a taste, and a taste can be talked past
mid-generation without ever registering as a violation. cope replaces the taste with an actual read
of the prose — the same voicing and structure check a human reviewer would run, just automatic.
basanite adds the vocabulary-tic layer a voicing check alone misses.

Neither one judges length on its own. That's the one habit they don't catch, and the budget below
is what holds the line on it instead.

## What the denial looks like

Claude sees this as the reason the write was blocked — nothing is shown to you unless Claude
surfaces it in chat on its own. Over budget:

```
This issue description is 238 words of prose against a 150-word budget — 88 over.

Four slots, in this order:
  1. The mechanism, one paragraph — what is broken, and why nothing catches it.
  2. Evidence it is real — a SHA, a log line, a failing assertion. One sentence.
  3. What is still exposed — file:line, not a description of the file.
  4. The fix, as a code block, plus one line on how to prove it can go red.
SHAs and file:line carry the detail; do not narrate what the reader can open.

Revise it and call again now — asking the operator to do the rewrite is the failure this reason
exists to prevent.
```

Inside budget but flagged by cope or basanite:

```
This comment is inside the 120-word budget, but a sibling scorer flagged it on the way out.

cope flagged this:

clause_symmetry: 1 violation(s)

Revise it and call again now — asking the operator to do the rewrite is the failure this reason
exists to prevent.
```

## When a deny repeats

A deny that comes back unchanged three times in a row is a loop, not a gate — the model rewrote
blind because nothing told it whether the rewrite helped. A flagged-but-in-budget write tracks that
in `internal/attemptstate`, one small state file per session, tool, and prose kind, since a fresh
hook process has no memory of its own between calls:

- **Attempt 1** denies as above.
- **Attempt 2** denies again, and names what changed since attempt 1 — cleared, still flagged, or
  newly flagged, by cope or basanite's own rule or word id.
- **Attempt 3**, if that set hasn't shrunk since attempt 2, lets the write through instead of
  denying a fourth time, with `additionalContext` naming what's still flagged rather than going
  quiet about it. A set that did shrink denies again, and the same check runs at attempt 4.

Being over budget is exempt from all of this: it always denies, at any attempt count, since it's
meant to be a hard limit, not a register a rewrite can talk its way past.

gh-write's own backstop (below) never gets this: it only ever sees the body on its stdin, never a
session id, so it has nothing to key a retry sequence on — every gh-write refusal stays attempt 1.

`TICKETVOICE_STATE_DIR` overrides where the attempt state lives; see
[Configuration](#configuration).

## Impact line

An issue description (not a comment — a comment isn't the ticket) must say what a customer or
exec would notice, in plain language, or say plainly that nobody would:

```
Impact: users on the map page see load times drop from ~4s to under 1s.
```

```
Impact: none — internal maintenance, no user-facing change.
```

Missing entirely denies:

```
This issue description has no impact line. Add one, in plain language a PM or exec would understand:

Impact: users on the map page see load times drop from ~4s to under 1s.

or, for work nobody outside engineering would notice:

Impact: none — internal maintenance, no user-facing change.

Revise it and call again now — asking the operator to do the rewrite is the failure this reason
exists to prevent.
```

This never checks whether the stated impact is *true* — that's a narrative judgment call, out of
scope for a fast synchronous hook (see [Ground-truth citations](#ground-truth-citations) for the
line drawn the other way, on citations). It also deliberately doesn't ask a ticket to restate
which project or initiative it belongs to — that's already a native, structured Linear field
(`project`/`projectMilestone`), visible in Linear's own UI and readable directly by anything that
wants it, including whatever turns Linear search results into release notes. Impact has no such
field, which is the only reason it's worth requiring here at all.

`TICKETVOICE_NO_IMPACT_CHECK` turns this off.

## Ground-truth citations

A ticket citing another ticket id, a `file:line`, or a commit SHA is verified, not trusted at face
value. Each check fails open on anything it can't determine and flags only a citation it can
positively confirm is wrong:

- **Ticket ids** (`ABC-550`-shaped) are checked against Linear directly. Ordinary jargon that
  matches the same shape — `UTF-8`, `SHA-256`, `GPT-4`, `RFC-2119`, `COVID-19` — never reaches
  Linear at all: a citation only counts as a candidate if its letter prefix matches one of the
  workspace's real team keys, fetched and cached once a day. Up to `TICKETVOICE_LINEAR_CITE_CAP`
  (default 5) distinct ids are checked per call.
- **`` `path/to/file.go:123` `` or `` `path/to/file.go:100-200` ``** must exist, at that line
  count, relative to the call's `cwd`. A path with no extension (`Makefile`, `Dockerfile`) isn't
  matched — a known v1 gap, not a silent misread.
- **`` `<sha>` ``** (7-40 hex characters) must be a real commit in the repo at `cwd`. Requires
  `git` and a real repository; skipped entirely otherwise.

```
ticket reference(s) don't exist: ABC-99999999
nope.go doesn't exist
`deadbeefcafe` isn't a commit in this repo
```

This stops at existence, not truth. Verifying a *narrative* claim — "already fixed in ABC-777,"
"the file already exists" — needs real code-reading judgment, which means an LLM call, not a fast
deterministic check; that's out of scope for a synchronous `PreToolUse` hook. Checking that a
cited id, path, or SHA is real needs no judgment at all, which is what keeps it in scope.

Unlike the impact line and the escalation in [When a deny repeats](#when-a-deny-repeats), a
citation check is exempt from the 3-attempt escalation: a nonexistent SHA doesn't become real by
attempt 3, so this always denies regardless of attempt count, the same as being over budget.

Requires `TICKETVOICE_LINEAR_TOKEN` for the ticket-id check only — unset, that one check skips
(file:line and SHA checks don't need Linear at all). `TICKETVOICE_NO_CITATION_CHECK` turns off all
three.

## Install

```bash
git clone https://github.com/justinstimatze/ticketvoice
cd ticketvoice
make install   # builds ticketvoice and gh-write to $(go env GOPATH)/bin, version from git describe
```

Then wire `ticketvoice` into `~/.claude/settings.json` as a `PreToolUse` hook on the four Linear
write tools and on `Bash` (for `gh-write` calls — see below). The path has to be absolute — hooks
run in whatever environment Claude Code was launched from, which may not have your Go bin
directory on `PATH`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "mcp__linear__save_issue|mcp__linear__save_comment|mcp__linear__save_diff_comment|mcp__linear__submit_diff_review|Bash",
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
are refused outright, so a body can only arrive on stdin — as a heredoc, a `< file` redirect, or a
pipe.

Every body gh-write sends is also prefixed with an agent tag — see [Agent tag](#agent-tag).

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
heredoc or a `< file` redirect — both literal text, no shell escaping to resolve, extractable
without a tokenizer (`ghWriteProse` in `main.go`). Covers issue and PR create/comment/edit,
matching the Linear surface this hook already covers (issues and comments) — not release notes,
which are a different genre (a changelog, not a ticket) that this gate isn't shaped for.

A pipe-sourced body (`cat notes.txt | gh-write issue create ...`) defeats even that: seeing what a
pipe's upstream stage would produce means running it, and a `PreToolUse` hook has no business doing
that. So the hook doesn't try — instead, gh-write runs the same cope/basanite/word-budget check
itself, on the real bytes it just read off its own stdin, before it ever calls `gh`. That check
doesn't care how the body arrived, which means it's the actual backstop for all three forms, not
just the two the hook can see ahead of time. What differs is only when each one catches it: the
hook's `deny` stops the Bash call before it ever runs, while gh-write's own refusal happens after —
gh-write is the thing that call invoked, so by the time it can check, the call has already started.
Either way Claude gets the same reason text and no result but "retry shorter," with nobody paged.

## Configuration

`TICKETVOICE_MAX_WORDS` overrides the length backstop for the current call — issue or comment; it
has no effect on cope or basanite's own verdicts. Set it in the hook's environment:
`TICKETVOICE_MAX_WORDS=200`.

`TICKETVOICE_COPE_GATE` and `TICKETVOICE_BASANITE` point at those binaries if they aren't on `PATH`.
Missing or unreachable is not an error for either — the call just isn't scored against that sibling's
rules that time.

`TICKETVOICE_STATE_DIR` overrides where the retry-attempt state from
[When a deny repeats](#when-a-deny-repeats) lives. Unset, it follows the same
`$XDG_STATE_HOME`/`~/.local/state/ticketvoice` convention cope and basanite already use for their
own state.

`TICKETVOICE_NO_AGENT_TAG` turns off the agent tag below. Unset (the default) means tagged.

`TICKETVOICE_LINEAR_TOKEN` is a Linear personal API key (`lin_api_...`), sent raw with no `Bearer`
prefix — the same kind of token an operator already has for Linear's own MCP server, just read
from its own env var so the two never collide. Missing everywhere, the ticket-id half of
[Ground-truth citations](#ground-truth-citations) skips. Resolved the same way hindcast resolves
`ANTHROPIC_API_KEY`: the env var first, then a `.env` file found by walking up from the call's
`cwd`, then a global fallback at `~/.config/ticketvoice/.env` (one `TICKETVOICE_LINEAR_TOKEN=...`
line, `#`-comments allowed, quotes optional) — the global file is what lets one hook wired into
every project's `settings.json` resolve a token regardless of which project's `cwd` it's currently
handling a call for, worktrees included. `TICKETVOICE_LINEAR_ENDPOINT` overrides the GraphQL
endpoint (mainly for tests). `TICKETVOICE_LINEAR_CITE_CAP` (default `5`) caps how many distinct
ticket ids get checked per call.

`TICKETVOICE_NO_IMPACT_CHECK` and `TICKETVOICE_NO_CITATION_CHECK` disable
[Impact line](#impact-line) and [Ground-truth citations](#ground-truth-citations) independently of
each other and of the Linear token.

## What it counts

Prose only — ticketvoice strips fenced code before counting, since code is the part of a ticket
that's supposed to be long. A patch-based edit is counted on its inserted text alone; it leaves the
rest of the body alone. Below 200 words, a body with section headers gets an extra line in the
reason: headers cost two lines each and imply more document than there is.

## Agent tag

Every issue and comment this hook or gh-write lets through is prefixed with 🤖 by default, since
it's posted under the operator's own Linear or GitHub account and a reader shouldn't have to
already know to ask whether an agent wrote it. `TICKETVOICE_NO_AGENT_TAG` turns it off.

The two surfaces apply it differently, because they have different amounts of control over the
write. gh-write owns the actual bytes it sends to `gh`, so it just prepends the tag to its own
stdin before exec'ing. The hook doesn't own the write at all — a Linear MCP call goes straight from
Claude to `mcp.linear.app`, and a `PreToolUse` hook can only allow, deny, ask, or, via Claude Code's
`updatedInput`, replace the tool's entire input before it runs. So a clean Linear write returns
`permissionDecision: "allow"` with `updatedInput` set to the original input, verbatim, except the
one field carrying prose gets the tag prepended — every other field (`id`, `teamId`, whatever else
the real schema carries that this hook never parses) round-trips untouched, since `updatedInput`
replaces the whole object rather than merging into it. A patch (`save_issue` editing an existing
description) isn't tagged: it's a diff against prose already tagged once, not a fresh post.

## Where this sits next to cope and basanite

[cope](https://github.com/justinstimatze/cope) and
[basanite](https://github.com/justinstimatze/basanite) are the actual read on the prose — see
[Why](#why) — but neither blocks on its own: they answer with `additionalContext`, after the call
already went out, and only on Linear's own `PreToolUse` matchers. Ticketvoice is what turns their
verdict into a gate: it forwards its own stdin to `cope-gate -pretool` and
`basanite writecheck -no-dedup` directly and denies on either flag, whether or not the body is over
budget — so a within-budget ticket carrying a flagged tic gets sent back to Claude the same as an
over-length one. See [CHANGELOG.md](CHANGELOG.md) for how basanite's dedup state made this need a
new flag on its side.

Neither sibling's own matcher reaches `Bash`, so a GitHub write is never scored through their
independently-registered hooks the way a Linear call is. It's scored twice over by two other
callers instead: ticketvoice's hook forwards its own stdin the moment it can find a heredoc or
`< file` body ahead of the `gh` call, and gh-write forwards the real body bytes itself right
before sending, regardless of how they arrived — see the gh-write section above. Same two
binaries, same verdict shape, just two different callers covering what the other can't see.

[Impact line](#impact-line) and [Ground-truth citations](#ground-truth-citations) are a different
shape from cope and basanite: first-party, not delegated to a sibling binary, and neither reads on
register or vocabulary — one checks a plain-language line is present, the other checks a citation
against ground truth. The impact check never touches the network at all — see [Impact
line](#impact-line) for why it never asks a ticket to restate data (the project/initiative link)
Linear already tracks natively. The citation check's ticket-id half does read from Linear, but only
to confirm an id resolves to something, the same read-only existence check it runs against a local
file or a local git commit.

## Development

`git config core.hooksPath hooks` once, after cloning, activates the tracked pre-commit hook —
gofmt, vet, test, `make check-readme`, plus a non-blocking CodeScene delta check when `cs` is on
`PATH`.

`make check-readme` runs the tool's own gate against the Why section above, as if it were a Linear
issue description — the one passage in this file written in ticket-body register, so it's the
only fair target. Same convention as cope's `make check-readme` (`cope-gate --check README.md`) and
[effigy](https://github.com/justinstimatze/effigy)'s `generate_readme.py`, narrowed to what this
tool does: it doesn't generate prose, so there's nothing to write through it, only something to
check.

## License

MIT. See `LICENSE`.
