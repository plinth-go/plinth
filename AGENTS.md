# Plinth Agent Guide

Project-wide instructions for AI coding agents. Keep this file short and
actionable. Personal or machine-specific rules belong in `AGENTS.override.md`
(gitignored); do not commit them here.

## Project shape

- Plinth is a thin Go 1.27 application kernel built on `net/http`, not a complete web framework.
- Business models, authorization policy, storage choices, and deployment topology belong to consuming applications.
- Use `net/http.ServeMux`. Do not add Gin, Echo, Fiber, Huma, Viper, a DI container, or a second router.
- `go-playground/validator` owns validation, `pgx` owns Postgres, `goose` owns migrations, and `go-redis` owns Redis. Do not reimplement them.
- Core packages are `app`, `kernel`, `logging`, `runtime`, `utils`, and `webui`. Optional integrations live in `kernel/*` and `platform/*` subpackages.
- Core packages must not import optional adapters or their third-party libraries. `internal/depscheck` enforces this boundary.
- Keep `utils` pure and deterministic: no IO, state, HTTP/database policy, or business rules.
- `internal/api`, `internal/config`, and `cmd/plinth` are the reference application, not importable SDK packages.

## HTTP and API

- Use typed `func(context.Context, In) (Out, error)` handlers with `kernel.Meta` and `router.Handle`.
- Give each input field exactly one location tag: `path`, `query`, `header`, or `body`. Required request fields use `validate:"required"`.
- Use `kernel.Raw` only for SSE, WebSocket, streaming, files, or explicit `http.ResponseWriter` control.
- Return expected 4xx errors through Problem helpers; wrap unexpected failures with `kernel.Internal`.
- JSON bodies are strict: reject unknown fields, duplicate names, `null`, and trailing values.
- Generate OpenAPI from real Go types, tags, and comments. Do not maintain a parallel hand-written schema.
- Register docs, middleware, and group tags before routes. Middleware is outermost first; the recommended order is `RequestID`, `AccessLog`, `Recover`, `BodyLimit`.
- Do not add a second registration path or synonymous router methods.

## Runtime and integrations

- Every long-running goroutine must be owned by a `runtime.Component` and stop on its context.
- Components start in insertion order and stop in reverse order. Never start bare background goroutines from handlers.
- Register SSE and WebSocket streams with `kernel.StreamRegistry` so shutdown is bounded.
- Optional integrations stay opt-in: `kernel/auth`, `authz`, `cors`, `uploads`, `wshub`; `platform/pgdb`, `jobs`, `pgjobs`, `migrate`, `pgmigrate`, `redis*`, `csrf`, `idempotency`, `session`, `objectstore`, and `observability`.
- `jobs` and `migrate` core contracts are backend-neutral. Only `pgjobs` and `pgmigrate` may import `pgx`.
- Follow existing adapter naming: technology-plus-responsibility for vendor facades (`pgdb`, `redisclient`, `jwtauth`, `s3store`, `oteltrace`), natural names for framework-native packages.

## Quality

- Add tests for behavior changes. HTTP contract changes need real-server coverage when they depend on `net/http`.
- Do not weaken allocation budgets or skip failing checks to land a change.
- Run the narrowest relevant checks, then `make check`, `make test`, `make vet`, and `make build`; run `make release-check` for release work.
- Regenerate generated files; never hand-edit generated outputs.
- Update `README.md`, `README.zh-CN.md`, examples, or API snapshots when public behavior or usage changes; keep both README translations in sync.

## Commits and pull requests

- Every commit needs a matching DCO `Signed-off-by`. `make hooks` installs the auto-sign hook; `git commit -s` also works.
- After a rebase, run `git rebase --signoff <base>` and verify with `DCO_BASE=origin/main make dco-check`.
- Never push directly to `main`. Use a focused branch and pull request.
- Do not merge with failing required checks, missing sign-offs, or generated-file drift.
- Describe the problem, approach, validation, and any API, wire-contract, security, migration, or dependency-boundary impact.

## Writing agent instructions

- Put project-wide, durable rules here; put personal paths, toolchain locations, workflow habits, and machine quirks in `AGENTS.override.md`.
- Prefer short imperative bullets, exact commands, and file paths. Link to docs instead of duplicating long explanations.
- No secrets, credentials, local-only URLs, or one-off task notes.
- If a rule can be enforced by a test or script, add the test or script and keep the instruction brief.
- Codex reads only one instruction file per directory: a root `AGENTS.override.md` replaces `AGENTS.md` there. Use the user-level `~/.codex/AGENTS.md` or a nested override when shared and personal rules must both apply.
