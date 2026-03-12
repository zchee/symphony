# Go TODO

## Remaining Tasks

- [ ] Run live-issue validation on a safe real project with real dispatch enabled.
- [ ] Capture any additional deviations that only appear under live-issue execution.
- [ ] Keep the repo docs aligned if the recommended validation posture changes.

## Current Context

- Authenticated startup, polling, `/`, `/api/v1/state`, and `/api/v1/refresh` were validated against `elixir/WORKFLOW.md`.
- The authenticated validation used a no-match `LINEAR_ASSIGNEE` override to avoid dispatching real work on a shared project.
- The repo-owned workflow still assumes outbound GitHub access for `git clone` during `after_create`.
- `mise` and `mix` are still needed if the real `before_remove` hook path is exercised in this environment.
- Outside the live runtime, Go keeps a cached last-known-good workflow on access, whereas Elixir falls back to direct loads when the workflow-store process is not running.
