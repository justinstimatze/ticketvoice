# Changelog

## Impact line, and ground-truth citation checks — 2026-09-05

Two new checks, from an adversarial panel review of 60 real Linear tickets against this gate:
neither a PM/exec reader nor whatever turns Linear search results into public release notes had a
reliable signal for "is this user-facing, and why," and nothing verified that a cited ticket id,
file:line, or SHA was real rather than stale or hallucinated.

The impact check went through a real design change before it shipped. The first draft asked a
ticket to declare its project in a fenced `yaml` block, matched against Linear's real
`project`/`projectMilestone` fields. That mechanism only existed to make the restatement
checkable — and stopped mattering once it was clear which project a ticket belongs to is already
tracked natively by Linear, readable directly by anything that wants it (a release-note agent
included), so the second draft dropped the whole restatement-and-match idea. What shipped is
narrower: an issue description (not a comment) needs one plain-language `Impact:` line, checked
for presence only, no fencing and no Linear lookup at all.

Ground-truth citations (`internal/citecheck`) verify a ticket id against Linear, a `file:line`
against the local filesystem, and a commit SHA against the local git repo — existence only, never
whether a *narrative* claim built on that citation is true, which needs real code-reading
judgment and is out of scope for a synchronous hook the same way it was ruled out of scope for the
impact line's own truth. Two things were verified live, not assumed, before either check's deny
path was written:

- Linear's actual not-found response: a bogus issue id comes back `data: null` with an `errors`
  entry whose `extensions.code` is `INPUT_ERROR` — a clean, distinguishable signal, not something
  that could be confused with an auth or rate-limit error, which get a different code and must
  never be read as "confirmed nonexistent."
- `git cat-file -e <sha>^{commit}` exits 128 both when `cwd` isn't a git repo at all, and when
  `cwd` is a repo but the cited SHA genuinely doesn't exist in it — indistinguishable from the
  exit code alone. Fixed by checking `git rev-parse --is-inside-work-tree` once per call first;
  only once that confirms a real repo does a `cat-file` failure mean "confirmed nonexistent."

A ticket-id citation also had to be told apart from ordinary jargon matching the same
`[A-Z]{2,10}-\d+` shape — `UTF-8`, `SHA-256`, `GPT-4`, `RFC-2119`, `COVID-19` all match it, and
Linear will honestly report `null` for every one of them, which would have denied a legitimate
write over incidental hyphen-and-digit text. Fixed by gating extraction on the workspace's real,
cached team keys (confirmed live against a real Linear workspace during development — a small,
stable set) — a candidate only counts if its prefix matches one of those.

Both checks fold into the existing `Judgment`/escalation machinery from the entry below: citations
are ground-truth facts, so — like the word budget — they're exempt from the 3-attempt escalation
and always deny; the impact line, being fixable by adding a sentence, stays inside the normal
escalation pool. This is also the hook's first real concurrency: `cope`, `basanite`, and the
citation checks' network/subprocess calls used to run one after another and now run together,
since none of that time needs to add up once each already bounds itself independently.

## Escalate a stalled deny instead of denying it forever — 2026-09-04

"Deny, and Claude rewrites and retries on its own" (below) held for the first retry and then didn't:
in practice Claude would sometimes give up after one denial and ask the operator to rewrite the
ticket by hand instead — the exact human-in-the-loop outcome the ask→deny change was meant to
close. Two changes, both scoped to the hook, not gh-write's own backstop (it never sees a session
id, only the body on its stdin, so it has nothing to key a retry sequence on):

- The reason's closing line named the fix but not the failure — "Cut it and call again" doesn't
  say what to refrain from. It now reads "Revise it and call again now — asking the operator to do
  the rewrite is the failure this reason exists to prevent," naming the choice that was actually
  going wrong.
- A flagged-but-in-budget write now tracks its own retry sequence — session, tool, and prose kind —
  in the new `internal/attemptstate` package, one small JSON file per sequence since a fresh hook
  process has no memory between calls. Attempt 2 names what changed since attempt 1 (cleared, still
  flagged, newly flagged, by cope/basanite rule or word id — see `budgetgate.ViolationIDs`, which
  parses those ids out of each sibling's own tally line). Attempt 3, if that set hasn't shrunk since
  attempt 2, lets the write through with an `additionalContext` note instead of denying a fourth
  time; a set that did shrink keeps denying, and the same check runs again at attempt 4. Being over
  budget is exempt — it's a hard limit, not a register a rewrite can talk its way past, so it always
  denies regardless of attempt count. See [When a deny repeats](README.md#when-a-deny-repeats).

The retry sequence's key was originally just session, tool, and kind — two unrelated tickets
posted back-to-back with the same tool in one session would have shared a counter, so a fresh
ticket's very first attempt could misread as a stalled one and get waved through early. plancheck's
`gate.go` had already hit the mirror-image version of this (resetting too eagerly on any plan-hash
change, looping an agent 6-8 times on an already-converged plan) and fixed it with a `PlanHash`
that anchors the reset to what actually changed. `attemptstate.Key` now carries the same kind of
anchor — a Linear `id`/`issueId`, or a gh-write target number parsed off the Bash command — so two
different tickets never share one sequence. A fresh create has no id yet on either surface, so it
stays in the shared, unprotected bucket the fix can't reach — the same boundary plancheck has for a
plan with nothing yet to hash.

## Deny instead of ask on a flagged or over-budget write — 2026-08-29

`permissionDecisionReason` for `"ask"` is shown to the user, not Claude — confirmed against the
primary hooks doc before changing anything, not assumed. That made every flagged or over-budget
write a human's decision, forever: the reason for `"ask"` never reaches Claude at all. `"deny"`
reverses it — the reason goes to Claude, not a human — so a flagged Linear write now rewrites and
retries on its own, the same way gh-write's own refusal already worked. Both surfaces do the same
thing now, just at different points in the call's lifecycle: the hook's `deny` stops a Linear write
or a Bash-visible gh-write call before it runs; gh-write's own refusal (a pipe body, or anything the
hook couldn't see ahead of time) happens after, since gh-write is what the call already invoked.

Consequence: the tag on a flagged/over-budget write, added earlier today, no longer applies — a
denied call never executes, so there's nothing for `updatedInput` to tag. The tag now only appears
on the eventual `allow`, once a retry actually clears the gate. `TICKETVOICE_MAX_WORDS` is the only
override left; there's no per-ticket human approval anymore.

## Tag Linear writes too, via updatedInput — 2026-08-29

The agent tag (🤖) added to gh-write earlier today only covered GitHub — Linear writes went out
untagged, since ticketvoice's hook never had a place to inject anything: it could only allow, deny,
or ask, and none of those touch the content that actually reaches Linear. Verified against the
primary source (the actual hooks reference, not a summary) rather than assume either way:
`PreToolUse` supports
`updatedInput`, which replaces the tool's entire input object before it runs — not a merge, the
whole thing, so every field the hook doesn't parse (`id`, `teamId`, `priority`, whatever else a
real Linear call carries) has to round-trip untouched or the write breaks. That distinction is why
this wasn't a small change: tagging now means parsing `tool_input` as a generic
`map[string]json.RawMessage`, rewriting only the one field that carries prose, and re-marshaling
the rest byte-for-byte, rather than reconstructing a input from the two fields (`description`,
`body`) this hook already knew about.

Consequence: a clean, under-budget Linear write is no longer silent — it now returns
`permissionDecision: "allow"` with the tagged `updatedInput`, where before it returned nothing at
all. A flagged or over-budget write carries the tag in its `ask` response too, so what a human
approves is what actually gets tagged when they say yes. A patch (editing an existing description)
stays untagged — it's a diff against prose already tagged once, not a fresh post. `TICKETVOICE_NO_AGENT_TAG`
fully restores the old silent-on-clean behavior, not just the tag.

Verified against the reinstalled binary with a realistic payload carrying `id`/`teamId`/`title`/
`priority`/`labelIds` alongside `description` — every field but `description` came back byte-for-byte
identical in `updatedInput`.

Factored `AgentTag`/`AgentTagEnabled` into `internal/budgetgate` so gh-write's stdin rewrite and the
hook's `updatedInput` rewrite share one constant and one opt-out check instead of two.

## Cover Linear's diff-comment and diff-review tools — 2026-08-29

Raised while checking whether GitHub and Linear go through the same gates: this project isn't
connected to `mcp.linear.app`, so its live tool schema had to be checked from a project that is.
Two tools write prose in the same genre this hook already
claims to cover — issue/comment-adjacent — under names the two-tool matcher never learned:
`mcp__linear__save_diff_comment` (a comment on Linear's PR-review-thread object, distinct from
`save_comment`) and `mcp__linear__submit_diff_review` (review-summary prose on the same object).
Both carry the body in a `body` field, same shape as `save_comment`, so covering them was two
`prose()` cases plus two names added to the `~/.claude/settings.json` matcher, not new parsing.
Verified against the reinstalled hook binary: a 130-word body through each tool now asks, where
before it shipped silent.

Five more tools write prose Linear-side and stay uncovered on purpose, not by omission:
`save_status_update`, `save_document`, `save_project`/`save_initiative`, `save_milestone`,
`save_release`/`save_release_note`. Same reasoning as GitHub's release notes: a different genre of
writing than a ticket body, named explicitly in README.md rather than left implicit.

## Redirect support, a gh-write backstop, and an agent tag — 2026-08-29

Closes the second bypass named in the entry below, plus one that entry didn't even try to name.
`ghWriteProse` now also reads a `< file` redirect the same way it reads a heredoc — literal text,
no shell tokenizer, a path restricted to a safe literal alphabet (no `$VAR`, backtick, glob, or
process substitution) so an unresolvable target fails open instead of guessing. That closes the
redirect half of the old gap outright.

The pipe half (`cat notes.txt | gh-write issue create ...`) can't be closed the same way: seeing
what a pipe's upstream stage would produce means running it, and a text-matching `PreToolUse` hook
has no business doing that. So it isn't closed there — it's closed one layer down. `gh-write`
itself now runs the exact same word-budget/cope/basanite check the hook runs (`internal/budgetgate`,
a new package factored out of `main.go` so there's one implementation, not two kept in sync by
hand), on the real body bytes it just read off its own stdin, before it ever calls `gh`. That check
doesn't care how the bytes arrived, so it's the actual backstop for all three forms — heredoc,
redirect, pipe — not just the two the hook can see ahead of time. The tradeoff: the hook can return
`permissionDecision: "ask"` and let a human approve or edit before anything runs; gh-write finding
the same problem after the Bash call already executed can only refuse and exit non-zero, for Claude
to read and retry shorter. Verified against the real installed binaries, not just `go test` — an
over-budget body run through the built `gh-write` was rejected before `gh` was ever invoked, and the
rejection carried a real finding from the actual `cope-gate` on this machine, not a stub.

Also: every body gh-write sends is now prefixed with 🤖 by default, since it posts under the
operator's own GitHub account and a reader shouldn't have to already know that to tell. Off via
`TICKETVOICE_NO_AGENT_TAG`.

README.md's "the heredoc is the only path, not one convention among several" claim — the thing the
entry below declined to rewrite unilaterally — is gone; replaced with what's actually enforced now
that there's no remaining unenforced path.

## Close the &&-chain gap in gh-write matching — 2026-08-29

`ghWriteInvoke` required `gh-write` at the start of a physical line: `(?m)^\s*gh-write\s+...`.
Verified against the built binary, not just the regex read: `cd /some/repo && gh-write issue
create --title X <<'EOF'` with a 220-word body — a correct heredoc, an ordinary shell habit, no
evasion intent — got zero check. `ghWriteInvoke` is now anchored on `\b` instead of line start, so
a preceding `&&`/`;`/`|` chain no longer hides the call. `TestGhWriteProseExtractsHeredocBody` gained
the chained case as a regression test.

This closes one of two verified bypasses, not both. The other is structural, not a regex bug:
`gh-write`'s own `validate()` blocks the `--body`/`--body-file` flags, but a subprocess cannot tell
a heredoc from a `<` redirect or a `|` pipe on its own stdin — bash implements all three the same
way on fd 0. `gh-write issue create --title X < body.txt` runs fine and stays invisible to
`ghWriteProse`, which only recognizes the heredoc-marker pattern. README.md's claim that the
heredoc is "the only path, not one convention among several" is true of the CLI flags gh-write
checks and not true of stdin's actual origin — named here rather than rewritten unilaterally,
since the fix (teach `ghWriteProse` to also read a referenced file or the preceding pipe stage) is
real new parsing surface, not a one-line change like this entry's. Both closed two entries later.

The hook binary is wired globally in `~/.claude/settings.json`, not scoped to any one project, so
this was live on every project's Bash calls, not just the one where it was found.

## GitHub coverage, via a new gh-write wrapper — 2026-08-28

Raised while working on a project tracked on GitHub rather than Linear: everything this repo
covered was Linear-only, so a `gh issue create`/`gh pr create` call carrying an over-length or
tic-flagged body shipped with nobody watching it at all.

Not a matcher addition. `PreToolUse` for a Bash call hands this hook `tool_input.command` — the
raw shell string, not parsed argv — and a `gh issue create --title "..." --body "..."` line can't
be reliably read back out of that: `--body`/`--body-file`/`--notes`/`--notes-file` differ across
`issue`/`pr`/`release` and `create`/`comment`/`edit`, each with its own quoting, and a wrong
extraction doesn't fail open the way an unparseable Linear call does — it can grab the wrong span
(a `--title` instead of a `--body`) and report a plausible, wrong verdict.

Added `cmd/gh-write` instead: a thin wrapper over `gh issue`/`gh pr` that refuses `--body`,
`-b`, `--body-file`, `-F` outright and takes the body from stdin only — a quoted heredoc in
practice, e.g. `gh-write issue create --title T <<'EOF' ... EOF`. Everything else passes straight
through to `gh` unchanged. That turns "find a body in an arbitrary shell command" into "find a
fixed CLI grammar followed by a heredoc," which is a plain string search
(`ghWriteProse`/`extractProse` in `main.go`) — no shell tokenizer, and the failure mode for a
Bash command that isn't a gh-write call, or whose heredoc this can't find, is "nothing to check,"
matching Linear's existing unparseable-input behavior.

Covers `issue`/`pr` × `create`/`comment`/`edit` — matching what this hook already covers on
Linear (issues and comments), not release notes, which read as a changelog rather than a ticket
and aren't shaped for a 150-word prose budget.

Wired: `ticketvoice`'s own `PreToolUse` matcher gained `|Bash` (`~/.claude/settings.json`) — cheap
for every other Bash call, since `ghWriteProse` returns immediately when the command isn't a
gh-write invocation. `Makefile`'s `build`/`install` now also build `gh-write`. Verified against the
real `PreToolUse` JSON shape, not just the exported functions: an over-budget `gh-write issue
create` call through `ticketvoice`'s stdin path correctly returns `permissionDecision: "ask"`; an
under-budget one and an unrelated `ls -la` both return nothing.

## Basanite wired the same way — 2026-08-28

The previous entry left basanite out: `writecheck` dedupes against a per-session seen-set file, so a
second caller checking the same call right after Claude Code's own dispatch to basanite's registered
hook would find every word already marked seen and get nothing back. Raised with basanite's
maintainer directly rather than worked around; they agreed the same day. Commit
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

Written because the register note alone didn't hold — "concise, pragmatic staff engineer" was in
force, and ticket bodies still ran long anyway, more than once. A register is a taste, and a taste
can be talked past; a word count is a limit, and it can't.
