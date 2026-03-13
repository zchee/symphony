# Symphony Go

This directory contains the in-progress Go port of the Symphony reference implementation.

The current milestone is bootstrap plus the first real runtime slices:

- canonical Go module under `go/`
- implementation-oriented package map and Elixir parity ledger
- CLI port for workflow-path selection, guardrail acknowledgement, `--logs-root`, and `--port`
- process-local runtime configuration store used by the CLI
- workflow loader and typed config layer with baseline tests, validation, raw remote workspace-root handling, worker settings, and runtime sandbox-policy resolution for local vs SSH workers
- strict prompt rendering for the current Liquid-style workflow template subset
- workspace lifecycle and safety checks with hook execution support for both local and SSH-backed workers
- normalized issue domain type plus tracker abstraction and in-memory fake
- Linear GraphQL client with normalization, polling pagination, by-id pagination/order preservation, and error mapping
- tracker adapter selection that now routes `memory` vs `linear`
- Codex app-server handshake client with cwd guards, startup handshake, approval/input-required handling, and SSH-backed remote launch support
- orchestrator state/coordination with dispatch, reconciliation, retry timing, worker-host-aware snapshots, per-host SSH worker caps, and Codex update aggregation
- shared observability presenter plus JSON/HTTP API surface over worker-aware orchestrator snapshots
- agent runner tying workspace, prompt, tracker refresh, Codex turns, worker-host fallback, and runtime metadata reporting together
- first live service/runtime wiring in `app` and `cli.RuntimeDependencies()`
- first terminal dashboard formatter in `dashboard` backed by the checked-in fixture evidence
- richer Codex telemetry and dashboard humanization, including turn lifecycle events, long-line buffering, stderr/non-JSON capture, and broader human-readable Codex status text
- live Codex-owned `session_started`, `startup_failed`, and `turn_ended_with_error` lifecycle emission, plus basic Codex lifecycle and non-JSON stream logging
- broader runtime lifecycle logging in `agent` and `app` for agent start/completion/failure, dispatch, retry, worker exit, and non-active issue transitions
- stamp-based workflow cache and last-known-good reload retention in `workflow`, now used by `config` and `prompt`
- broader smoke coverage for the live service path in `app`, including the real agent loop, fake Codex transport, and HTTP observability surface
- richer HTML dashboard rendering in `httpui` with CSS, metric cards, running/retry tables, JSON detail links, refresh affordance, and unavailable-state handling
- background workflow polling support in `workflow`, now started by the live runtime for closer parity with Elixir’s 1-second workflow-store refresh model
- Go replacement for the `workspace.before_remove` helper via `symphony workspace-before-remove`
- shared local-path canonicalization in `pathsafety` and SSH transport helpers in `ssh`
- direct tests for SSH target parsing, remote workspace lifecycle, remote Codex launch, worker-host selection, worker-aware observability payloads, and Linear by-id pagination
- an opt-in Go live-E2E harness in `agent/live_e2e_test.go` plus `make e2e` for real Linear/Codex validation when credentials are available

The Go port has now been validated against the repo-owned workflow with real Linear auth for the startup/polling/HTTP surface. The main remaining work is documenting the few intentional or environment-shaped deviations that remain.

## Final Parity Notes

Validated scope:
- Authenticated startup against the repo-owned `elixir/WORKFLOW.md`
- Linear-backed polling and refresh behavior, including by-id reconciliation pagination
- HTML dashboard and JSON observability endpoints, including worker-host/workspace metadata
- Workflow reload, background polling, and last-known-good retention
- CLI/helper behavior including `workspace-before-remove`
- SSH worker dispatch primitives, remote workspace lifecycle, and remote Codex app-server startup via direct Go tests

Recommended live-project validation posture:
- Keep using a no-match `LINEAR_ASSIGNEE` override when validating against a real shared project unless you explicitly intend to let Symphony dispatch live work. That exercises auth, polling, and operator surfaces safely without mutating real tickets.

Known remaining deviations:
- The authenticated repo-workflow validation intentionally used a no-match `LINEAR_ASSIGNEE`, so live issue execution against a real project was not exercised in this environment.
- The Go live-E2E harness now exists, but `make e2e` is still blocked in this shell until `LINEAR_API_KEY` is present.
- The repo-owned workflow still assumes outbound GitHub access for `git clone` during `after_create`, and `mise`/`mix` are still needed if the `before_remove` hook path is exercised in this environment.
- Outside the live runtime, Go still keeps a cached last-known-good workflow on access, whereas Elixir falls back to direct file loads when the workflow-store process is not running. The active-runtime path now matches Elixir more closely via immediate reload and background polling.

## Current Commands

```bash
cd go && go build ./cmd/symphony
cd go && go test ./...
cd go && go test -race ./...
make -C go e2e
```

## Immediate Next Targets

1. Export `LINEAR_API_KEY` and run `make -C go e2e` against a non-production or otherwise safe Linear target.
2. If SSH workers are part of the intended validation path, also set `SYMPHONY_LIVE_SSH_WORKER_HOSTS` before rerunning `make -C go e2e`.
3. Capture any deviations that only appear under that live-issue execution mode and update the docs if the recommended posture changes.

## Source of Truth

- [`../SPEC.md`](../SPEC.md) for language-agnostic service behavior
- [`../elixir/`](../elixir/) for current runnable implementation details
- [`../.agent/PLANS.md`](../.agent/PLANS.md) for the living execution plan
