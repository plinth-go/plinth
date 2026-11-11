# Plinth

[English](README.md) | [简体中文](README.zh-CN.md)

<p>
  <a href="https://github.com/plinth-go/plinth/actions/workflows/ci.yml"><img src="https://github.com/plinth-go/plinth/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/plinth-go/plinth"><img src="https://pkg.go.dev/badge/github.com/plinth-go/plinth.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License: Apache-2.0"></a>
  <a href="https://github.com/plinth-go/plinth/releases/latest"><img src="https://img.shields.io/github/v/release/plinth-go/plinth?display_name=tag&amp;sort=semver" alt="Latest release"></a>
</p>

## Project

`plinth` /plɪnθ/ is the base of a building. The name reflects the project's role: it does not build the walls and roof for your application; it provides a stable, thin, replaceable foundation.

Plinth is a **Go 1.27 application kernel**. It uses the standard library `net/http.ServeMux` as its HTTP router and provides typed handlers, parameter binding, validation, Problem responses, OpenAPI, process lifecycle, and a small set of optional infrastructure adapters. Business models, authorization policy, storage choices, and deployment topology stay in the application's own repository.

Plinth is not a complete web framework like Gin, Echo, Fiber, or Huma:

- It does not own business code structure or require a particular domain model.
- It does not hide the established semantics of `net/http`, `go-playground/validator`, `pgx`, `goose`, or `go-redis`.
- Optional integrations live in separate subpackages; capabilities you do not import do not enter the application's dependency closure.

## Features

| Layer | What it provides |
|---|---|
| HTTP | typed handlers, `path`/`query`/`header`/`body` binding, validator rules, slug generation, RFC 9457 Problem responses, dynamic headers, SSE, CSV/file responses |
| Docs | OpenAPI 3.1 generated from Go comments and tags, embedded Swagger UI, startup contract validation |
| Runtime | ordered component start and reverse-order shutdown, HTTP server, TLS/HTTP2, readiness/liveness, structured logging |
| App shell | `app.Main`, `serve`/`check`/`env`/`help`, env configuration, custom subcommands, replaceable static-file handler |
| Optional platform | Bearer/JWT/OIDC, authorization, CORS, CSRF, rate limiting, sessions, idempotency, uploads, WebSocket, Postgres, Redis, job queues, migrations, object storage, Prometheus/OpenTelemetry |

Capabilities that need external infrastructure live in separate subpackages:

- Auth and security: `kernel/auth`, `kernel/authz`, `kernel/cors`, `platform/csrf`, `platform/ratelimit`, `platform/session`
- Data and jobs: `platform/pgdb`, `platform/jobs`, `platform/pgjobs`, `platform/migrate`, `platform/pgmigrate`, `platform/redisclient`
- Other integrations: `kernel/wshub`, `kernel/uploads`, `platform/objectstore`, `platform/observability`

The core packages `app`, `kernel`, `runtime`, `logging`, `utils`, and `webui` do not depend on these optional packages.

## Examples

### Minimal service

```sh
go run ./examples/hello
curl http://127.0.0.1:8080/hello/Ada
# {"message":"hello, Ada"}
```

`examples/hello` depends only on the standard library and Plinth itself, and runs as the smallest complete example.

### Typed endpoint

Inputs, outputs, and validation are ordinary Go types. Binding, Problem responses, and OpenAPI are prepared at registration time.

```go
type helloInput struct {
	kernel.Meta `route:"GET /hello/{name}"`

	Name string `path:"name" validate:"required,min=1,max=40"`
}

type helloOutput struct {
	Message string `json:"message"`
}

func hello(_ context.Context, in helloInput) (helloOutput, error) {
	return helloOutput{Message: "hello, " + in.Name}, nil
}

router := kernel.NewRouter(logger, "Plinth", "dev")
router.Use(
	kernel.RequestID(),
	kernel.AccessLog(logger),
	kernel.Recover(logger),
)
router.Handle(hello)
```

Use `kernel.Raw` for SSE, WebSocket, file downloads, or endpoints that need direct control of `http.ResponseWriter`, and declare the corresponding OpenAPI behavior explicitly.

### Reference application

```sh
docker compose up -d
cp .env.example .env
make run
```

- `http://localhost:8080/`: capability dashboard
- `http://localhost:8080/docs/`: Swagger UI
- `http://localhost:8080/openapi.json`: OpenAPI 3.1
- `http://localhost:8080/readyz`: dependency and background-component readiness

### Example index

| To learn about | Start here |
|---|---|
| TLS + HTTP/2 deployment | [`examples/tls-http2`](examples/tls-http2) |
| Typed binding, parameters, and error responses | [`examples/minimal-http.md`](examples/minimal-http.md) |
| Configuration, logging, lifecycle, and subcommands | [`examples/app-shell.md`](examples/app-shell.md) |
| Security middleware ordering | [`examples/security.md`](examples/security.md) |
| Postgres, Redis, jobs, migrations, object storage | [`examples/optional-platform.md`](examples/optional-platform.md) |
| Testing business handlers | [`examples/testing.md`](examples/testing.md) |
| All examples | [`examples/README.md`](examples/README.md) |

## Development and validation

```sh
make generate   # refresh generated files
make fmt
make check      # docs/API/config/route contracts
make test
make vet
make build
```

Confirm the public API and dependency boundaries before committing:

```sh
make api-check
make license-check
```

Run the full gate before a release:

```sh
make release-check
```

## Commits and pull requests

Every commit must include a DCO `Signed-off-by` matching the commit author. Enable the repository's automatic sign-off hook after cloning:

```sh
make hooks
```

After rewriting history, reapply the sign-off and run the same check as CI:

```sh
git rebase --signoff origin/main
DCO_BASE=origin/main make dco-check
```

Keep pull requests focused. Explain the problem, approach, and compatibility impact, and pass the four required checks: `Go 1.27.0 (minimum)`, macOS, Windows, and `Release gate`. See [`CONTRIBUTING.md`](CONTRIBUTING.md) and the PR template for the full rules.

## Agent guide

- `AGENTS.md` is committed and contains only project-wide, durable AI collaboration rules. Do not put personal preferences or machine paths there.
- `AGENTS.override.md` is gitignored and is for personal toolchains, workflow habits, and local differences. It is not committed.
- In the same directory, `AGENTS.override.md` replaces `AGENTS.md`; to have both apply, put personal rules in user-level `~/.codex/AGENTS.md` or place the override in a deeper working directory.
- Keep both files short and actionable: exact commands and boundaries, with details linked from README, CONTRIBUTING, or examples instead of duplicated long-form docs.

## Version and license

Published versions use Semantic Versioning with Go module tags; the current stable version is **v1.2.0**. See [`CHANGELOG.md`](CHANGELOG.md) for history, [`RELEASING.md`](RELEASING.md) for compatibility rules, and [`ROADMAP.md`](ROADMAP.md) for future direction.

Report security vulnerabilities privately according to [`SECURITY.md`](SECURITY.md); do not open a public issue.

Plinth is licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for copyright and attribution, and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party licenses.
