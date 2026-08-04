# Changelog

## v0.1.0 — 2026-08-04

First cut. A PreToolUse hook holding Linear ticket prose to a word budget: 150 words for an issue
description, 120 for a comment, fenced code excluded. Over budget it returns `permissionDecision:
"ask"` with the count and the four slots, so a genuinely long ticket stays possible and the
assistant cannot wave its own gate through. Silent when inside the budget.

Written because the register note alone did not hold — "concise, pragmatic staff engineer" was in
force from 2026-07-20 and a 238-word body still shipped on 2026-08-04. A register is a taste; a
word count is a limit, and it can fail.
