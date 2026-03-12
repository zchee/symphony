# Symphony Go

This directory contains the in-progress Go port of the Symphony reference implementation.

The current milestone is bootstrap plus the first real runtime slices:

- canonical Go module under `go/`
- implementation-oriented package map and Elixir parity ledger
- CLI port for workflow-path selection, guardrail acknowledgement, `--logs-root`, and `--port`
- process-local runtime configuration store used by the CLI
- workflow loader and typed config layer with baseline tests and validation
- strict prompt rendering for the current Liquid-style workflow template subset
- workspace lifecycle and safety checks with hook execution support
- normalized issue domain type plus tracker abstraction and in-memory fake
- first-pass Linear GraphQL client with normalization, pagination, and error mapping
- tracker adapter selection that now routes `memory` vs `linear`
- first Codex app-server handshake client with cwd guards, startup handshake, and approval/input-required handling
- first orchestrator state/coordination layer with dispatch, reconciliation, retry timing, snapshots, and Codex update aggregation
- first shared observability presenter plus JSON/HTTP API surface over orchestrator snapshots
- first agent runner loop tying workspace, prompt, tracker refresh, and Codex turns together
- first live service/runtime wiring in `internal/app` and `internal/cli.RuntimeDependencies()`
- first terminal dashboard formatter in `internal/dashboard` backed by the checked-in fixture evidence
- richer Codex telemetry and dashboard humanization, including turn lifecycle events, long-line buffering, stderr/non-JSON capture, and broader human-readable Codex status text
- live Codex-owned `session_started`, `startup_failed`, and `turn_ended_with_error` lifecycle emission, plus basic Codex lifecycle and non-JSON stream logging
- broader runtime lifecycle logging in `internal/agent` and `internal/app` for agent start/completion/failure, dispatch, retry, worker exit, and non-active issue transitions
- stamp-based workflow cache and last-known-good reload retention in `internal/workflow`, now used by `internal/config` and `internal/prompt`
- broader smoke coverage for the live service path in `internal/app`, including the real agent loop, fake Codex transport, and HTTP observability surface
- richer HTML dashboard rendering in `internal/httpui` with CSS, metric cards, running/retry tables, JSON detail links, refresh affordance, and unavailable-state handling
- background workflow polling support in `internal/workflow`, now started by the live runtime for closer parity with Elixir’s 1-second workflow-store refresh model
- Go replacement for the `workspace.before_remove` helper via `symphony workspace-before-remove`

The Go port has now been validated against the repo-owned workflow with real Linear auth for the startup/polling/HTTP surface. The main remaining work is documenting the few intentional or environment-shaped deviations that remain.

## Final Parity Notes

Validated scope:
- Authenticated startup against the repo-owned `elixir/WORKFLOW.md`
- Linear-backed polling and refresh behavior
- HTML dashboard and JSON observability endpoints
- Workflow reload, background polling, and last-known-good retention
- CLI/helper behavior including `workspace-before-remove`

Recommended live-project validation posture:
- Keep using a no-match `LINEAR_ASSIGNEE` override when validating against a real shared project unless you explicitly intend to let Symphony dispatch live work. That exercises auth, polling, and operator surfaces safely without mutating real tickets.

Known remaining deviations:
- The authenticated repo-workflow validation intentionally used a no-match `LINEAR_ASSIGNEE`, so live issue execution against a real project was not exercised in this environment.
- The repo-owned workflow still assumes outbound GitHub access for `git clone` during `after_create`, and `mise`/`mix` are still needed if the `before_remove` hook path is exercised in this environment.
- Outside the live runtime, Go still keeps a cached last-known-good workflow on access, whereas Elixir falls back to direct file loads when the workflow-store process is not running. The active-runtime path now matches Elixir more closely via immediate reload and background polling.

## Current Commands

```bash
cd go && go build ./cmd/symphony
cd go && go test ./...
```

## Immediate Next Targets

1. If desired, run a live-issue validation pass on a non-production or otherwise safe Linear project with real dispatch enabled.
2. Capture any additional deviations that only appear under that live-issue execution mode.
3. Keep the repo docs aligned if the recommended validation posture changes.

## Source of Truth

- [`../SPEC.md`](../SPEC.md) for language-agnostic service behavior
- [`../elixir/`](../elixir/) for current runnable implementation details
- [`../.agent/PLANS.md`](../.agent/PLANS.md) for the living execution plan
