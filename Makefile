GO ?= go
GOFMT ?= gofmt
GO_LICENSES_VERSION ?= v1.6.0
ACTIONLINT_VERSION ?= v1.7.12
GOVULNCHECK_VERSION ?= v1.9.0
ALLOWED_LICENSES ?= Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,BSL-1.0,Unlicense
FUZZTIME ?= 5s
FUZZTIME_EXTENDED ?= 1m

.PHONY: all generate fmt docscheck golden check dco-check test vet build bench fuzz fuzz-extended api-check api-update race license-check license-update lint-workflows vulncheck release-check run clean

all: generate fmt check test vet build

generate:
	$(GO) generate ./...

fmt:
	$(GOFMT) -w app cmd examples internal kernel platform runtime utils webui

docscheck:
	cd internal/api && $(GO) run ../../cmd/plinth-docgen -root ../.. -check -out ../generated/docs_gen.go

golden:
	UPDATE_GOLDEN=1 $(GO) test ./internal/api -run TestOpenAPISnapshot -count=1

check: docscheck api-check
	$(GO) run ./cmd/plinth check
	$(GO) test ./internal/api -run TestOpenAPIUsesGeneratedCommentDocs

dco-check:
	./scripts/check-dco.sh "$${DCO_BASE:-origin/main}" HEAD

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

bench:
	$(GO) test ./kernel -run '^$$' -bench 'BenchmarkHTTP' -benchmem -count=5

fuzz:
	$(GO) test ./kernel -run '^$$' -fuzz '^FuzzStrictJSONBinding$$' -fuzztime=$(FUZZTIME) -count=1
	$(GO) test ./kernel -run '^$$' -fuzz '^FuzzRouterPath$$' -fuzztime=$(FUZZTIME) -count=1

fuzz-extended:
	GO="$(GO)" ./scripts/fuzz-extended.sh "$(FUZZTIME_EXTENDED)"

race:
	$(GO) test -race ./... -count=1

release-check: check lint-workflows test vet build race fuzz license-check vulncheck

api-check:
	$(GO) test ./internal/apicheck -run TestPublicAPISurface -count=1

api-update:
	UPDATE_API=1 $(GO) test ./internal/apicheck -run TestPublicAPISurface -count=1

license-check:
	GO="$(GO)" GO_LICENSES_VERSION="$(GO_LICENSES_VERSION)" ./scripts/go-licenses.sh check --include_tests --allowed_licenses=$(ALLOWED_LICENSES) ./...

license-update:
	GO="$(GO)" GO_LICENSES_VERSION="$(GO_LICENSES_VERSION)" ./scripts/update-third-party-notices.sh

lint-workflows:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION) .github/workflows/*.yml

vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

build: check
	mkdir -p bin
	$(GO) build -trimpath -o bin/plinth ./cmd/plinth
	$(GO) build -trimpath -o bin/plinth-migrate ./cmd/plinth-migrate

run:
	@test -f .env || (cp .env.example .env && echo "created .env from .env.example")
	@set -a; . ./.env; set +a; exec $(GO) run ./cmd/plinth

clean:
	rm -rf bin coverage.out
