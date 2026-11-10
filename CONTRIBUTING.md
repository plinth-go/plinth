# Contributing to Plinth

Plinth is a stdlib-first Go application kernel. Contributions are welcome when
they preserve the framework's explicit, thin, and composable boundaries.

## License and sign-off

Plinth is licensed under the Apache License 2.0. By submitting a contribution,
you agree that it may be distributed under the same license. Sign off every
commit with the Developer Certificate of Origin:

```sh
git commit -s
```

Use your real name and a reachable email address in the sign-off. Do not add a
separate copyright header unless a file already has one.

CI verifies the sign-off rule on pull requests with `scripts/check-dco.sh`.
Merge commits are skipped; bot commits must still include a sign-off. To run
the same check locally before opening a PR:

```sh
git fetch origin main
DCO_BASE=origin/main make dco-check
```

## Code of conduct

Participation in this project is governed by `CODE_OF_CONDUCT.md`. Report
unacceptable behavior privately to the maintainers at `vip.mail.lx@gmail.com`.

## Toolchain

Use the Go version declared in `go.mod`. Do not hardcode a local toolchain path.

```sh
go version
```

## Local checks

Run the narrowest relevant checks while developing, then the full gate before a
pull request:

```sh
make generate
make fmt
make check
make test
make vet
make build
make license-check
```

Before a release, run:

```sh
make release-check
```

Pull requests run the minimum supported Go toolchain on Ubuntu, macOS, and
Windows. The platform jobs run `go mod tidy -diff`, `go test ./...`,
`go vet ./...`, and `go build ./...`; service-backed tests still skip unless
their `PLINTH_TEST_*` environment variables are configured. The required checks
are `Go 1.27.0 (minimum)` (Ubuntu), `Go 1.27.0 (macOS)`,
`Go 1.27.0 (Windows)`, and the Ubuntu `Release gate`.

`make release-check` includes API/documentation freshness checks, third-party
license compatibility, GitHub Actions workflow linting, the regular test suite,
`go vet`, both binary builds, the full race suite, both fuzz targets, and
`govulncheck`.

## Dependency updates and generated files

Dependency changes must not leave generated files stale. After any `go.mod` or
`go.sum` change, run:

```sh
make license-update
git add THIRD_PARTY_NOTICES.md
git commit -s -m "chore(deps): refresh generated files"
```

Do not edit `THIRD_PARTY_NOTICES.md` by hand.

For Dependabot pull requests, the `Dependabot autofix` workflow handles this
automatically. When the `Release gate` fails, it runs `make license-update` on
the PR branch, commits as `github-actions[bot]`, and dispatches CI for the new
commit. Pushes made with `GITHUB_TOKEN` do not emit a new `pull_request` event,
so the workflow copies the real CI job results back to the PR commit as the
required `Go 1.27.0 (minimum)` and `Release gate` commit statuses. The workflow
is limited to branches owned by `dependabot[bot]` in this repository. If it
cannot push, run the commands manually on the PR branch before merging.

## Published-module smoke

The `Consumer smoke` workflow runs weekly and can also be dispatched manually.
It creates a clean temporary module, resolves `github.com/plinth-go/plinth@latest`,
builds a minimal application, and sends a real HTTP request to it. This catches
tag, module-path, and proxy regressions that source-tree tests cannot detect.
It tests the latest published release rather than a pull request, so it is not
part of the required PR checks.

The `Extended fuzz` workflow runs both kernel fuzz targets weekly for one minute
each by default. Maintainers can dispatch it manually with a different Go
duration. Failures upload `fuzz-results/` and Go's generated
`kernel/testdata/fuzz/` corpus so the input can be reproduced locally. The pull
request release gate keeps the fast five-second fuzz checks.

The `Benchmark report` workflow runs the representative routing and binding
benchmarks weekly and compares them with the previous retained artifact using
`benchstat`. It is diagnostic and deliberately does not block pull requests.
For a request hot-path change, run `make bench` before and after the change on
the same machine, then use `make bench-compare`. The full benchmark set, noise
policy, and investigation threshold are documented in `README.md`.

Use the issue templates for bug reports and feature proposals. Keep pull
requests focused, include the validation that was run, and call out any public
API or behavior-contract impact. Directional work is tracked in `ROADMAP.md`.

## Design constraints

- Plinth is an application kernel, not a complete web framework.
- Use `net/http` and `http.ServeMux`; do not add Gin, Echo, Fiber, Huma, Viper,
  a DI container, or a second router.
- Keep optional integrations in their own packages. Core packages must not
  import SCS, gorilla, pgx, go-redis, Goose, JWT/OIDC, AWS S3, Prometheus,
  OpenTelemetry, or optional `platform/*` packages.
- Add a dependency only when it removes a real maintenance burden and cannot be
  replaced by a small, well-tested stdlib implementation.
- Prefer explicit configuration and registration. Do not add implicit global
  behavior or a second configuration system.
- Preserve the public API guarantees described in `RELEASING.md`; breaking
  changes require a major-version decision.
- Business models belong in the consuming application, not in the framework or
  the reference `internal/api` tree.
- Most files under `examples/` are documentation. `examples/hello` is the
  dependency-free quickstart and `examples/tls-http2` is the runnable TLS/HTTP2
  deployment example; keep both covered by `go test ./...`.

## Tests and compatibility

- Add tests for behavior changes, not only for coverage.
- HTTP contract changes need real-server coverage when the behavior depends on
  `net/http` (streaming, shutdown, HEAD/OPTIONS, connection reuse, TLS).
- Keep allocation budgets in `kernel/budget_test.go` meaningful; do not relax a
  budget just to make a test pass.
- Run `go mod tidy` after dependency changes, and keep `go.sum` consistent.
- Refresh `THIRD_PARTY_NOTICES.md` with `make license-update` after dependency
  changes; Dependabot PRs are handled by the autofix workflow, but the result
  must still be green before merging.

## Security

Do not open a public issue for an unfixed vulnerability. Follow `SECURITY.md`
and use GitHub Private Vulnerability Reporting.

## Local-only notes

`AGENTS.md`, `HANDOFF.md`, `*.local.md`, and other local maintainer notes are
gitignored. Do not commit machine-specific paths, credentials, or session
handoff notes. Generic contributor guidance belongs in this file.
