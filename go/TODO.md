# Go TODO

## Remaining Tasks

- [ ] Export `LINEAR_API_KEY` and run `make -C go e2e` against a safe real project with real dispatch enabled.
- [ ] Capture any additional deviations that only appear under live-issue execution.
- [ ] Keep the repo docs aligned if the recommended validation posture changes.

## Current Context

- Authenticated startup, polling, `/`, `/api/v1/state`, and `/api/v1/refresh` were validated against `elixir/WORKFLOW.md`.
- The authenticated validation used a no-match `LINEAR_ASSIGNEE` override to avoid dispatching real work on a shared project.
- A Go-owned live-E2E harness now exists in `agent/live_e2e_test.go`, and `make -C go e2e` is the supported entrypoint for the remaining real Linear/Codex validation.
- In the current shell, `make -C go e2e` stops immediately because `LINEAR_API_KEY` is absent.
- The repo-owned workflow still assumes outbound GitHub access for `git clone` during `after_create`.
- `mise` and `mix` are still needed if the real `before_remove` hook path is exercised in this environment.
- Outside the live runtime, Go keeps a cached last-known-good workflow on access, whereas Elixir falls back to direct loads when the workflow-store process is not running.
