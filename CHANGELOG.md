# Changelog

## Basanite wired the same way — 2026-08-28

The previous entry left basanite out: `writecheck` dedupes against a per-session seen-set file, so a
second caller checking the same call right after Claude Code's own dispatch to basanite's registered
hook would find every word already marked seen and get nothing back. Raised with basanite directly
over dispatch rather than worked around. Basanite's own operator signed off same-day; commit
`157bb8b` adds `writecheck -no-dedup`, which skips the seen-set filter and the seen-set write
entirely — `report.StateDir()` itself is only called on the deduped path now, so `-no-dedup` creates
no state at all. The registered hook's own deduped, once-per-session behavior is unchanged; this is
strictly additive.

`judgeCope` and `judgeBasanite` are now both thin wrappers over one `judgeSibling(bin, args, raw)` —
same wire shape in (raw stdin) and out (`hookSpecificOutput.additionalContext`) for both. Verified
against both real binaries together, not just stubs: a ticket carrying "load-bearing," well inside
the 150-word budget, now correctly gets `permissionDecision: "ask"` carrying findings from *both*
siblings — cope's own `load_bearing` voicing rule and basanite's vocabulary-tic report, independently,
from two different mechanisms. Also verified two consecutive `writecheck -no-dedup` calls against the
same text both report the finding, and confirmed no `written-*` seen-set file appears under
basanite's state dir after either call.

This closes the hole named in the very first entry below: a within-budget ticket carrying a flagged
tic no longer ships with nobody forced to look. Both siblings' own registered hooks keep their
existing behavior unchanged — this hook calling them a second time, directly, changes nothing about
what basanite's or cope's own hook shows the assistant.

## Call cope directly, gate on its verdict too — 2026-08-28

The "Discussed and not built" entry below declined this, on the grounds that reading a sibling's
finding would need a correlation key and a hook-ordering guarantee across three independently-
versioned tools. That objection assumed ticketvoice would have to read cope's or basanite's output
*after* Claude Code dispatched to them separately — file, `additionalContext`, whatever channel,
arriving in whatever order hooks fire in. It doesn't have to. `runHook` now forwards its own raw
stdin — the exact bytes Claude Code sent it — to `cope-gate -pretool` as a subprocess and reads the
verdict back directly, inside its own process, before it decides anything. No correlation key, no
ordering dependency: it's not reading another hook's output, it's running the same binary that hook
runs, on the same input, itself.

This is safe specifically because `cope-gate -pretool` writes no session state — its own doc comment
says so ("Per-write feedback repeats. That is the correct amount."). Two independent callers scoring
the same text land on the same answer. Verified against the real binary, not just a stub: cope's own
`TestExternalLaneKeepsTheVoicingRules` fixture (`"Row loss on cursor reset. The sync drops every row
written after the upstream cursor rewinds..."`) is 34 words, nowhere near the 150-word budget, and
now correctly comes back `permissionDecision: "ask"` carrying cope's `labelled_opening` finding —
the exact case the previous entry named and left open.

Basanite is not wired the same way, and not because the idea doesn't apply — it's a real,
newly-found blocker. `writecheck` dedupes against a per-session seen-set file: the first caller to
run it for a given call marks the words seen, and a second caller checking the same text right after
sees nothing new. Claude Code already dispatches to basanite's own registered hook on this same tool
matcher; ticketvoice calling `writecheck` too would race that hook for the same state file, and
whichever ran second would silently get an empty result. Raised with basanite directly rather than
worked around quietly — a stateless check path resolves it cleanly if basanite wants to add one.

## Gate the docs through the gate — 2026-08-28

Cope generates and gates its own README through cope-gate (`make readme`, `make check-readme` —
`cope-gate --check README.md`). Effigy writes its README through its own Layer 2
(`generate_readme.py`, from a test-fixture card). Neither convention transfers whole: ticketvoice
has no LLM in its loop and nothing to generate, and its rule is a length budget, not a style check —
gating the full README against a 150-word issue budget would fail by design, since a README is
supposed to be longer than a ticket.

What transfers is the check half, narrowed to the one paragraph actually written in ticket-body
register: the Why section. `main.go` gained a `--check <file|-> [-field issue|comment]` mode that
runs the exact `evaluate()` function the hook runs — not a reimplementation — against a file or
stdin. `make check-readme` extracts the Why section between its `## ` markers and pipes it through
`--check -`. Wired into both `hooks/pre-commit` (blocking, same tier as `go test`) and `ci.yml`.

Verified: `main_test.go`'s `TestRunCheckAcceptsReadmeWhySection` reads README.md directly and reruns
the same check the Makefile target runs, so a future edit to the Why section that pushes it over 150
words fails a unit test, not just a pre-commit hook someone could `--no-verify` past.

## Mechanism vs. policy — 2026-08-28

Both sibling tools shipped their own Linear coverage since yesterday's entry. Basanite's dedup fix
(`10bb9c0`, v0.10.0) landed the same day this entry was written. Cope added `cope-gate --pretool`
(`70b319a`), a new `PreToolUse` entry over the same five `mcp__linear__save_*` tools, scoring
voicing and structure the same `internal/scan` path already applies to a chat reply.

Both chose the same posture ticketvoice was built to not have: `additionalContext`, no
`permissionDecision`, and both said so on purpose rather than by default. Cope's own reasoning —
gating a Linear write harder than it gates its own chat output at `Stop`, where `--block` is off,
would invert the tool. Basanite's writecheck reasoning matches. Neither is a gap; both are a
considered stance about what a general-purpose detector should default to for everyone who runs it.

That leaves ticketvoice as the only one of the three that can hold a call for a human to see before
it posts. Not by having more coverage — it still knows nothing but a word count — but by being the
one piece on this surface whose entire job is deciding whether a human needs to look, which is a
different question from whether something is wrong with the prose.

Discussed and not built: ticketvoice reading cope's and basanite's findings directly and deciding
whether to block on them, instead of only on word count. That's the fix for the gap the previous
entry named and declined to patch around — a within-budget ticket carrying a flagged tic still
ships today with nobody forced to look, same as before any of this. It's a real feature, not a
relay: cope and basanite would each need to write a per-call finding somewhere keyed to the specific
tool call, and Claude Code does not currently promise the hook-ordering guarantee ticketvoice would
need to read it before deciding. Three independently-versioned tools would need to agree on that
protocol before any code moves. Not started.

## Considered and declined — 2026-08-27

Basanite's `writecheck` hook dedupes each flagged tic to one mention per session, so a word already
named early in a long session goes silent for the rest of it — including inside a Linear ticket
body. A real case: "load-bearing" was named once by basanite at session start, then a Linear comment
carrying it 40+ turns later got no warning from basanite at all. It was caught only because that
comment also happened to be over this tool's own word budget, so its "ask" reason text put the full
comment in front of a human, who spotted the word by eye.

Considered: read basanite's report inside ticketvoice and list its flagged terms in the "ask" reason
whenever a save_issue/save_comment call is over budget anyway, since a human is already reading that
text at that moment. Declined.

The near-miss only worked because the comment was *also* over budget — coincidence, not design. The
addition would inherit exactly that coincidence: it fires only on the intersection of "over word
budget" and "basanite has this word un-deduped," so the common failure — a within-budget ticket
carrying a stale flagged term — ships exactly as silently as it does today. It decorates one lucky
branch of the bug rather than fixing the mechanism.

It would also buy real coupling for that unreliable payoff: reading basanite's `report.json` and
state dir, replaying its swap-matching (fenced-block skip, inflected-form matching) and its own
per-session "seen" bookkeeping, ties this tool's output to basanite's report schema from then on.
That's the phrase-level awareness this tool's own header disclaims, imported to catch a case it will
usually still miss.

Basanite already covers `mcp__linear__save_issue` and `mcp__linear__save_comment` directly, on its
own PreToolUse registration, independent of ticketvoice — it does not need a relay. What it lacks is
a human-facing channel for those two calls specifically: `writecheck` reports through
`additionalContext` (assistant-facing, invisible to a human), because its `display` hook — the piece
that would put a flagged word in front of a person — only rewrites screen text, and a Linear ticket
goes from `tool_input` straight to Linear with no screen in between. Basanite's own README already
names this gap in its hook table. The fix belongs there: a write with no display-hook equivalent
behind it — a persistent, externally-visible artifact, not an ephemeral chat line — should skip the
per-session dedup and return `permissionDecision: "ask"` the way a first-time flag would. That's a
policy change inside basanite's own dedup logic, not a text-relay job for a sibling tool built to
count words.

No functional change in this repo. Version stays at v0.1.0.

## v0.1.0 — 2026-08-04

First cut. A PreToolUse hook holding Linear ticket prose to a word budget: 150 words for an issue
description, 120 for a comment, fenced code excluded. Over budget it returns `permissionDecision:
"ask"` with the count and the four slots, so a genuinely long ticket stays possible and the
assistant cannot wave its own gate through. Silent when inside the budget.

Written because the register note alone did not hold — "concise, pragmatic staff engineer" was in
force from 2026-07-20 and a 238-word body still shipped on 2026-08-04. A register is a taste; a
word count is a limit, and it can fail.
