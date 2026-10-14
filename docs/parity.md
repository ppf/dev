# Parity checklist

The plan's checklist (`docs/plan.md`), ticked where a test verifies it. Side-by-side tests run the bash
script and `dev` on identical isolated tmux servers and compare the result; they live in
`internal/integration` (build tag `integration`). Unit tests are named `package.TestName`.

Unticked items need a person. Later phases add their sections.

## Hook (Phase 1)

- [x] Gated on `TMUX_PANE`; no-op without it: `TestState`, `cmd.TestState`.
- [x] Skip working/tool-failed when the top-level `agent_id` field is present: `TestState` (same result
  as the bash hook), `agent.TestResolve`.
- [x] `is_interrupt: true` → idle; otherwise tool-failed → working: `TestState`.
- [x] Only top-level fields count (deliberate fix: bash matched substrings anywhere): `TestState`.
- [x] Malformed JSON → no-op with a stderr message, exit 0: `TestState`, `cmd.TestState`.
- [x] `@agent_state` and `@agent_cmd` (the pane's current command) set in one call; `clear` unsets both:
  `TestState`.
- [ ] Live test with a real Claude session (idle → working → idle, subagent, waiting, crash leftover)
  after switching `settings.json`. Phase 1b, needs a person.

## Known differences from bash

- `dev state` rejects an unknown state (exit 1); the bash hook recorded any word.
