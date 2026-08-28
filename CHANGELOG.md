# Changelog

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
