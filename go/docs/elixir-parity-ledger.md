# Elixir Parity Ledger

This ledger categorizes current Elixir behavior so the Go port can preserve the right things in the right order.

Use these labels consistently:

- `required-core`: mandatory for spec conformance and safe unattended operation
- `required-operator-surface`: operator-visible behavior already shipped in Elixir and in scope for the Go port
- `helper`: supporting code or repo hygiene that still matters, but is not a primary runtime subsystem
- `intentionally-deferred`: explicitly postponed until a prerequisite subsystem exists; must be revisited before claiming parity

## required-core

| Area | Elixir source anchors | Why it is core |
| --- | --- | --- |
| Workflow loading and parse errors | `elixir/lib/symphony_elixir/workflow.ex` | `WORKFLOW.md` is the repository-owned runtime contract. |
| Workflow cache and reload | `elixir/lib/symphony_elixir/workflow_store.ex` | The runtime must keep operating with the last known good configuration. |
| Typed config defaults and validation | `elixir/lib/symphony_elixir/config.ex` | Dispatch and Codex runtime settings depend on normalized typed values. |
| Prompt rendering | `elixir/lib/symphony_elixir/prompt_builder.ex` | Every agent turn depends on strict issue-driven prompt construction. |
| Linear issue normalization and pagination | `elixir/lib/symphony_elixir/linear/client.ex`, `elixir/lib/symphony_elixir/linear/issue.ex`, `elixir/lib/symphony_elixir/linear/adapter.ex` | Polling, reconciliation, blockers, and routing all depend on normalized issue data. |
| Workspace safety and hooks | `elixir/lib/symphony_elixir/workspace.ex` | Prevents workspace escapes and controls lifecycle scripts. |
| Codex app-server protocol handling | `elixir/lib/symphony_elixir/codex/app_server.ex` | Symphony is fundamentally a Codex session orchestrator. |
| Dynamic tool transport | `elixir/lib/symphony_elixir/codex/dynamic_tool.ex` | Existing workflows rely on `linear_graphql` during unattended runs. |
| Agent execution loop | `elixir/lib/symphony_elixir/agent_runner.ex` | Bridges workspace, prompt, Codex turns, and issue refresh. |
| Polling orchestrator and retry logic | `elixir/lib/symphony_elixir/orchestrator.ex` | Owns concurrency, retries, reconciliation, and cleanup. |
| CLI workflow-path and acknowledgement contract | `elixir/lib/symphony_elixir/cli.ex`, `elixir/test/symphony_elixir/cli_test.exs` | Startup posture is part of the product behavior, not a convenience wrapper. |

## required-operator-surface

| Area | Elixir source anchors | Why it stays in scope |
| --- | --- | --- |
| Terminal status dashboard | `elixir/lib/symphony_elixir/status_dashboard.ex`, `elixir/test/symphony_elixir/status_dashboard_snapshot_test.exs` | Operators already depend on human-readable runtime state. |
| HTTP observability server | `elixir/lib/symphony_elixir/http_server.ex` | Optional HTTP visibility is already part of the implementation surface. |
| JSON observability routes | `elixir/lib/symphony_elixir_web/router.ex`, `elixir/lib/symphony_elixir_web/controllers/observability_api_controller.ex`, `elixir/lib/symphony_elixir_web/presenter.ex` | Route contract and payload shape are user-visible. |
| HTML dashboard and static assets | `elixir/lib/symphony_elixir_web/live/dashboard_live.ex`, `elixir/lib/symphony_elixir_web/static_assets.ex`, `elixir/priv/static/dashboard.css` | The Go port must preserve operator-facing dashboard behavior, even if implementation technology changes. |
| Logging field conventions | `elixir/docs/logging.md`, `elixir/lib/symphony_elixir/log_file.ex` | Stable issue/session context is required for operability. |
| Token-accounting semantics | `elixir/docs/token_accounting.md` | Dashboard and API counters become wrong if these rules drift. |

## helper

| Area | Elixir source anchors | Why it matters |
| --- | --- | --- |
| In-memory tracker test adapter | `elixir/lib/symphony_elixir/tracker/memory.ex` | Needed for deterministic Go tests without hitting Linear. |
| Observability pubsub helper | `elixir/lib/symphony_elixir_web/observability_pubsub.ex` | Useful implementation pattern for change fanout, but not a required API on its own. |
| Mix task for workspace cleanup | `elixir/lib/mix/tasks/workspace.before_remove.ex`, `elixir/test/mix/tasks/workspace_before_remove_test.exs` | Landed: `go/internal/workspacecleanup` plus `symphony workspace-before-remove` in the Go CLI now preserve the helper behavior with a Go command surface. |
| Repo hygiene tasks | `elixir/lib/mix/tasks/specs.check.ex`, `elixir/lib/mix/tasks/pr_body.check.ex`, `elixir/test/mix/tasks/*.exs` | Important for repo quality, though not part of the running Symphony daemon. |

## intentionally-deferred

| Area | Dependency that must land first | Reason for temporary deferral |
| --- | --- | --- |
| Live-issue Codex/Linear validation on a safe real project | Workflow loader, CLI, Linear client, Codex transport, orchestrator | Authenticated startup, polling, and HTTP/operator surfaces are now validated. Remaining deferred scope is live issue dispatch against a safe real project with real external auth. |

## Test Anchors To Port Early

Port these Elixir tests first because they lock down behavior that is both high-value and easy to regress:

- `elixir/test/symphony_elixir/cli_test.exs`
- `elixir/test/symphony_elixir/core_test.exs`
- `elixir/test/symphony_elixir/workspace_and_config_test.exs`
- `elixir/test/symphony_elixir/app_server_test.exs`
- `elixir/test/symphony_elixir/orchestrator_status_test.exs`
- `elixir/test/symphony_elixir/status_dashboard_snapshot_test.exs`

As the Go port advances, mark each row in this ledger with concrete implementation evidence such as package paths, test names, or documented deviations.
