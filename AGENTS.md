# Repository Guidelines

## Project Structure & Module Organization

The repository has two primary layers: the language-agnostic contract in [`SPEC.md`](SPEC.md) and the Elixir reference implementation in [`elixir/`](elixir/). Most code changes land in `elixir/lib/`, with Phoenix dashboard and API modules under `elixir/lib/symphony_elixir_web/`. Tests live in `elixir/test/`, shared helpers in `elixir/test/support/`, operational notes in `elixir/docs/`, and the runtime contract in `elixir/WORKFLOW.md`. CI and PR automation live in `.github/`. Before editing under `elixir/`, read `elixir/AGENTS.md` for local rules.

## Build, Test, and Development Commands

- `cd elixir && mise install && mix setup`: install the toolchain from `elixir/mise.toml` and fetch dependencies.
- `make -C elixir setup`: fetch Elixir dependencies.
- `make -C elixir build`: build the `bin/symphony` escript.
- `make -C elixir test`: run the ExUnit suite.
- `make -C elixir lint`: run `mix specs.check` and `credo --strict`.
- `make -C elixir all`: full CI gate: setup, build, format check, lint, coverage, and dialyzer.
- `cd elixir && mise exec -- ./bin/symphony ./WORKFLOW.md`: start the reference service locally using the checked-in workflow.

## Coding Style & Naming Conventions

Use `mix format`; the formatter is configured in `elixir/.formatter.exs` with a `200` character line length. Keep filenames in `snake_case` and follow existing module names such as `SymphonyElixir.*` and `SymphonyElixirWeb.*`. Public functions in `elixir/lib/` should have adjacent `@spec` annotations unless they are `@impl` callbacks. Route workflow-backed configuration through `SymphonyElixir.Config` instead of ad hoc environment reads, and keep implementation changes aligned with `SPEC.md`.

## Testing Guidelines

Tests use ExUnit and should be named `*_test.exs`. Put reusable helpers in `elixir/test/support/` and keep feature-specific tests near the corresponding area under `elixir/test/symphony_elixir/` or `elixir/test/mix/`. `mix.exs` enforces a `100` percent coverage summary threshold with an explicit ignore list, so add tests with every behavior change. During development, run targeted tests first, then finish with `make -C elixir all`.

## Commit & Pull Request Guidelines

Recent history favors short, imperative commit subjects, for example `Move Elixir observability dashboard to Phoenix (#29)`. Keep commits focused and descriptive. Pull requests must follow `.github/pull_request_template.md` exactly: `Context`, `TL;DR`, `Summary`, `Alternatives`, and `Test Plan`. Before opening a PR, run `cd elixir && mix pr_body.check --file /path/to/pr_body.md` if you edited the description format, and list the exact verification commands in `Test Plan`.

## Configuration & Docs

Keep secrets such as `LINEAR_API_KEY` in environment variables, not committed workflow files. If you change behavior or configuration, update the matching docs in `README.md`, `elixir/README.md`, and `elixir/WORKFLOW.md` in the same change.
