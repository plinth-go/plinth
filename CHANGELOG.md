# Changelog

All notable changes to Plinth are recorded here. Release tags use Semantic
Versioning; the `vNNN` labels in the development notes are internal iteration
numbers, not module versions.

## [1.1.0] - 2026-11-10

This release establishes the compatibility baseline for the project's early,
pre-consumer phase. Plinth had no known external consumers before v1.1.0, so
the public adapter import paths and the jobs/migration contracts were
reorganized without keeping the short-lived v1.0.0 spellings. From v1.1.0
onward, the compatibility rules in [RELEASING.md](RELEASING.md) apply in full.

### Added

- Backend-neutral `platform/jobs` contracts with PostgreSQL storage in
  `platform/pgjobs` and shared adapter helpers in
  `platform/jobs/jobutil`.
- `platform/migrate.Config`/`NewWithConfig` for explicit `database/sql`
  drivers and Goose dialects, plus a `platform/pgmigrate` convenience
  adapter.
- A runnable `examples/hello` quickstart with a real-server HTTP test.
- A scheduled external-consumer smoke workflow that builds and runs the latest
  published module from a clean temporary module.
- Contributor issue/PR templates, a code of conduct, and a v1.2 roadmap tied to
  actionable GitHub issues.
- `actionlint` and `govulncheck` in the required release gate, plus CodeQL
  default setup for Go and GitHub Actions.
- CI, Go Reference, license, and latest-release badges in the README.
- `utils.IfElse`, `utils.IfNilElse`, and `utils.IfEmptyElse` for concise value
  fallbacks without repeated conditional branches.

### Changed

- `jobs.Worker`, `jobs.Scheduler`, and jobs admin handlers now depend on narrow
  store interfaces instead of a PostgreSQL-backed concrete store. Claimed jobs
  expose the lease token through `Job.LeaseToken` so custom stores can implement
  the same fencing contract.
- `platform/migrate` no longer imports pgx. Applications select a driver,
  Goose dialect, and identifier limit directly, or use the PostgreSQL adapter
  to preserve the previous defaults.
- Database-specific adapters now use backend-first two-word package names:
  `platform/pgjobs`, `platform/pgmigrate`, `platform/rediscsrf`,
  `platform/redislimit`, and `platform/redissession`. Vendor-facing packages
  that applications commonly import alongside their upstream library now use
  the same technology-plus-responsibility form: `platform/pgdb`,
  `platform/redisclient`, `platform/jwtauth`, `platform/oidcauth`,
  `platform/objectstore/s3store`, `platform/observability/oteltrace`,
  `platform/observability/prommetrics`, and `kernel/wshub`. The previous
  packages are replaced so callers no longer need routine import aliases.
- Import paths changed as follows: `kernel/websocket` to `kernel/wshub`,
  `platform/csrf/redis` to `platform/rediscsrf`, `platform/jwt` to
  `platform/jwtauth`, `platform/objectstore/s3` to
  `platform/objectstore/s3store`, `platform/observability/otel` to
  `platform/observability/oteltrace`, `platform/observability/prometheus` to
  `platform/observability/prommetrics`, `platform/oidc` to
  `platform/oidcauth`, `platform/postgres` to `platform/pgdb`,
  `platform/ratelimit/redis` to `platform/redislimit`, `platform/redis` to
  `platform/redisclient`, and `platform/session/redis` to
  `platform/redissession`. The PostgreSQL implementation previously embedded
  in `platform/jobs` is now `platform/pgjobs`.
- Post-release documentation now installs the published module with `go get`
  instead of requiring a local `replace` directive.

## [1.0.0] - 2026-11-09

First stable release of the Plinth application kernel.

### Changed

- Lowered the module's minimum Go version from 1.27.3 to 1.27.0. The code still
  requires Go 1.27 features: generic HTTP methods, `encoding/json/v2`, and the
  standard-library `uuid` package.
- Replaced the MPL-2.0 `gosimple/slug` wrapper with an in-tree slug formatter
  backed by the Apache-2.0 `gosimple/unidecode` transliterator. Slug generation
  keeps the existing behavior, including Chinese, accented Latin, and Cyrillic
  transliteration.

### Added

- GitHub Actions CI with minimum-Go and release-gate jobs, plus automated
  third-party license checks and a generated `THIRD_PARTY_NOTICES.md`.
- `envdoc` field tags for generated environment-template comments, including
  validation against nested, ignored, and unexported fields.
- `plinth-migrate`, an independent multi-source migration command with timeout
  and argument validation.
- Failed-job administration: `Store.ListFailed`, `RetryFailed`, `DeleteFailed`,
  and the opt-in `platform/jobs/admin` HTTP handlers.
- `platform/redislock` with token-owned leases, Lua and `WATCH/MULTI/EXEC`
  strategies, and explicit Redis version compatibility checks.
- `platform/idempotency` memory and Redis-backed stores plus replay middleware.
- `kernel/auth`, optional JWT/OIDC verifiers, and Bearer-token middleware with
  RFC 9457 401 responses.
- Declarative route permissions through `kernel.Meta authz` tags and
  `kernel.RequirePermissions`, with all-of `kernel/authz` middleware and
  OpenAPI `x-required-permissions` output.
- Optional explicit-allowlist CORS in `kernel/cors`, including preflight
  handling without adding automatic OPTIONS or CORS behavior to the core
  router.
- Fixed-window rate limiting in `platform/ratelimit`, with a bounded in-memory
  limiter, Redis Lua/command strategies, standard rate-limit headers, explicit
  Redis version validation, and fail-closed middleware.
- CSRF protection in `platform/csrf`, with memory and Redis stores, hashed token
  identifiers, safe/unsafe method policy, and configurable cookie placement.
- Cookie-backed sessions in `platform/session`, wrapping `alexedwards/scs/v2`
  with memory and Redis stores plus explicit Secure/SameSite/Partitioned checks.
- `kernel/uploads` streaming multipart parsing with bounded files, fields,
  total bytes, MIME allowlists, and private temporary-file cleanup.
- `platform/objectstore` with traversal-safe local filesystem and optional S3
  adapters.
- Optional Prometheus HTTP metrics, OpenTelemetry server spans, and the
  backend-neutral `jobs.Observer` hook.
- Framework/example tests for hierarchical env decoding, duplicate final keys,
  cross-field `validate` rules, Redis version/strategy selection, and the new
  optional dependency boundaries.

### Initial stable baseline

These capabilities were present in the first public repository import:

- Typed `net/http` routing over `http.ServeMux`, including path, query, header,
  and strict JSON body binding, validation, RFC 9457 problems, automatic slug
  generation, and encode-before-commit responses.
- OpenAPI 3.1 generation from Go type and handler comments, embedded Swagger UI,
  operational endpoints, and generated-document freshness checks.
- SSE, CSV, attachment, single-file download, embedded static asset, dynamic
  response header, middleware, body-limit, and stream-shutdown helpers.
- Optional integration subpackages: `kernel/websocket`, `platform/jobs`,
  `platform/jobs/migrations`, `platform/postgres`, `platform/redis`, and
  `platform/migrate`. Core HTTP packages do not compile or link gorilla, pgx,
  go-redis, goose, or any `platform/*` package unless the application imports
  the matching optional capability; `platform/jobs` is intentionally
  Postgres-backed.
- Postgres-backed jobs and schedules with lease fencing, retries, backoff,
  stale recovery, graceful interruption, typed payloads, and exactly-once
  dispatch per schedule tick.
- Postgres, Redis, Goose migration, lifecycle, HTTP server policy, TLS, HTTP/2,
  h2c wiring, and structured logging integration through the optional packages.
- Pure shared helpers in `utils` for nil-slice normalization and bounded
  pagination, plus `platform/postgres.IsUniqueViolation` for PostgreSQL error
  mapping.
- `app.Main` with `serve`, `check`, `env`, and `help` built-ins plus validated
  application-owned commands through `urfave/cli/v3`.
- Hierarchical environment configuration with one root prefix and automatic
  per-level segments. `app.EnvTemplate`, `app.check`, and `app.LoadEnv` resolve
  the same final keys, reject duplicate names across parent, child, and sibling
  groups, and run struct `validate` tags after decoding.
- Embeddable `kernel.OpsSettings` exposes common liveness, readiness, OpenAPI,
  and documentation switches and paths under `OPS_*`; `Apply` converts them to
  runtime `kernel.OpsConfig` without moving logger, checks, or handlers into
  environment config. `DefaultOpsSettings` preserves the fully enabled runtime
  default for code-built configurations.
- `platform/jobs.WorkerConfig` and `SchedulerConfig` now own their env and
  validation metadata, so applications embed the framework types directly
  instead of maintaining duplicate job config structs.
- A framework testing guide, a real-router business HTTP test pattern, strict
  JSON and router fuzz targets, a public API snapshot, and a documented security
  boundary.

### Compatibility

- Public packages: `app`, `kernel`, `kernel/auth`, `kernel/authz`,
  `kernel/cors`, `kernel/uploads`, `kernel/websocket`, `logging`,
  `platform/csrf`, `platform/csrf/redis`, `platform/idempotency`,
  `platform/jobs`, `platform/jobs/admin`, `platform/jobs/migrations`,
  `platform/jwt`, `platform/migrate`, `platform/objectstore`,
  `platform/objectstore/localfs`, `platform/objectstore/s3`,
  `platform/observability/otel`, `platform/observability/prometheus`,
  `platform/oidc`, `platform/postgres`, `platform/ratelimit`,
  `platform/ratelimit/redis`, `platform/redis`, `platform/redislock`,
  `platform/session`, `platform/session/redis`, `runtime`, `utils`, and `webui`.
- The exported Go API is covered by `internal/apicheck/testdata/public-api.txt`.
- Go 1.27.0 or newer is required by the current module declaration.

See [RELEASING.md](RELEASING.md) for the compatibility policy and release
procedure.
