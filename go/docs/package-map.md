# Go Package Map

This document maps the current Elixir reference implementation to the proposed Go package layout under `go/`.

The goal is behavioral parity, not file-for-file transliteration. Package boundaries follow runtime responsibilities so concurrency, validation, and transport concerns stay separated.

| Elixir subsystem | Key source files | Proposed Go package | Responsibility |
| --- | --- | --- | --- |
| Application bootstrap and wiring | `elixir/lib/symphony_elixir.ex` | `go/internal/app` | Compose long-running service dependencies and lifecycle, including the live poll loop and optional HTTP server. |
| CLI entrypoint | `elixir/lib/symphony_elixir/cli.ex` | `go/internal/cli`, `go/cmd/symphony` | Parse flags, validate startup posture, select workflow path, and enter runtime startup/shutdown. |
| Workflow parsing | `elixir/lib/symphony_elixir/workflow.ex` | `go/internal/workflow` | Read `WORKFLOW.md`, split front matter and prompt body, surface typed load errors. |
| Workflow cache and reload | `elixir/lib/symphony_elixir/workflow_store.ex` | `go/internal/workflow` | Keep the last known good workflow, detect stamp/path changes, reload safely without dropping a good cached workflow on parse or read failures, and support live-runtime background polling. |
| Typed config and defaults | `elixir/lib/symphony_elixir/config.ex` | `go/internal/config`, `go/internal/runtimeconfig` | `internal/config` now owns typed workflow-backed runtime settings; `internal/runtimeconfig` remains the narrow process-local override store used by the CLI bootstrap. |
| Prompt rendering | `elixir/lib/symphony_elixir/prompt_builder.ex` | `go/internal/prompt` | Render the current strict Liquid-style subset, normalize issue values, and keep workflow/template errors distinct. |
| Tracker abstraction | `elixir/lib/symphony_elixir/tracker.ex` | `go/internal/tracker` | Define tracker-facing interfaces and the adapter selector that chooses memory vs Linear. |
| In-memory tracker fake | `elixir/lib/symphony_elixir/tracker/memory.ex` | `go/internal/tracker/memory` | Provide deterministic tracker behavior for orchestrator tests and local development. |
| Normalized issue model | `elixir/lib/symphony_elixir/linear/issue.ex` | `go/internal/domain` | Hold the shared issue and blocker shapes used across tracker, prompt, and orchestration layers. |
| Linear issue model and client | `elixir/lib/symphony_elixir/linear/*.ex` | `go/internal/linear` | Query Linear GraphQL, normalize issues into `internal/domain`, paginate, map transport errors, and now satisfy the tracker write surface for comments and state updates. |
| Workspace lifecycle and safety | `elixir/lib/symphony_elixir/workspace.ex` | `go/internal/workspace` | Derive deterministic workspace paths, enforce containment, preserve local work on reuse, and run hooks safely. |
| Codex app-server transport | `elixir/lib/symphony_elixir/codex/app_server.ex` | `go/internal/codex` | Launch Codex, manage the thread/turn protocol, cwd guards, approvals, supported dynamic tools, live session lifecycle events, terminal turn events, stderr/non-JSON stream capture and logging, and token/rate-limit telemetry extraction. |
| Dynamic tools | `elixir/lib/symphony_elixir/codex/dynamic_tool.ex` | `go/internal/codex/tools` | Advertise and execute client-side tools such as `linear_graphql`. |
| Agent execution loop | `elixir/lib/symphony_elixir/agent_runner.ex` | `go/internal/agent` | Coordinate one issue attempt across workspace, prompt, Codex turns, and issue refresh, including continuation guidance and max-turn stopping. |
| Orchestrator and retries | `elixir/lib/symphony_elixir/orchestrator.ex` | `go/internal/orchestrator` | Own dispatch ordering, eligibility, reconciliation, retries, snapshots, and aggregate runtime state. |
| Log sink and structured log helpers | `elixir/lib/symphony_elixir/log_file.ex`, `elixir/docs/logging.md` | `go/internal/logging` | Keep operator-facing log context stable and searchable. |
| Terminal status dashboard | `elixir/lib/symphony_elixir/status_dashboard.ex` | `go/internal/dashboard` | Render the runtime snapshot into human-readable terminal output from the shared orchestrator snapshot model, including richer Codex event humanization. |
| Shared observability shaping | `elixir/lib/symphony_elixir_web/presenter.ex`, `elixir/lib/symphony_elixir_web/observability_pubsub.ex` | `go/internal/observability` | Build reusable snapshot payloads and shared presentation helpers for the API and later dashboard surfaces. |
| HTTP server and routes | `elixir/lib/symphony_elixir/http_server.ex`, `elixir/lib/symphony_elixir_web/router.ex`, `elixir/lib/symphony_elixir_web/controllers/*.ex` | `go/internal/httpapi` | Serve `/api/v1/state`, `/api/v1/<issue_identifier>`, `/api/v1/refresh`, `/dashboard.css`, and route root requests into the HTML dashboard renderer. |
| HTML dashboard surface | `elixir/lib/symphony_elixir_web/live/dashboard_live.ex`, `elixir/lib/symphony_elixir_web/static_assets.ex`, `elixir/priv/static/dashboard.css` | `go/internal/httpui` | Render the operator dashboard with cards, tables, refresh affordances, CSS assets, and unavailable-state handling over the shared snapshot model. |
| Repository-local runtime state | Elixir `Application.put_env` usage across CLI/config | `go/internal/runtimeconfig` | Hold process-local startup overrides until the broader config layer takes over. |
| Workspace cleanup helper | `elixir/lib/mix/tasks/workspace.before_remove.ex` | `go/internal/workspacecleanup`, `go/internal/cli` | Provide the Go-side `workspace-before-remove` helper command that closes open PRs for a branch before workspace removal. |
| Test helpers and fixtures | `elixir/test/support/*`, `elixir/test/fixtures/*` | `go/internal/testutil`, `go/testdata` | Support deterministic tests, protocol fixtures, and dashboard snapshots. |

## Notes

- `go/internal/runtimeconfig` exists immediately because the CLI needs a real place to store startup overrides before the workflow/config subsystem lands.
- `go/internal/httpapi` and `go/internal/httpui` are intentionally separate so JSON API behavior and HTML/dashboard behavior can evolve independently without mixing concerns.
- `go/internal/observability` should own snapshot shapes consumed by both the terminal dashboard and HTTP surfaces. That avoids duplicate projection logic.
