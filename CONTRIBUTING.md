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

`make release-check` includes API/documentation freshness checks, third-party
license compatibility, the regular test suite, `go vet`, both binary builds,
the full race suite, and both fuzz targets.

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
the PR branch, commits as `github-actions[bot]`, and re-dispatches CI. The
workflow is limited to branches owned by `dependabot[bot]` in this repository.
If it cannot push, run the commands manually on the PR branch before merging.

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
- Examples under `examples/` are documentation. Keep them short and current;
  they are not a separate runnable product.

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
