GO ?= go
GOFMT ?= gofmt
GO_LICENSES_VERSION ?= v1.6.0
ALLOWED_LICENSES ?= Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,BSL-1.0,Unlicense

.PHONY: all generate fmt docscheck golden check test vet build bench fuzz api-check api-update race license-check license-update release-check run clean

all: generate fmt check test vet build

generate:
	$(GO) generate ./...

fmt:
	$(GOFMT) -w app cmd internal kernel platform runtime utils webui

docscheck:
	cd internal/api && $(GO) run ../../cmd/plinth-docgen -root ../.. -check -out ../generated/docs_gen.go

golden:
	UPDATE_GOLDEN=1 $(GO) test ./internal/api -run TestOpenAPISnapshot -count=1

check: docscheck api-check
	$(GO) run ./cmd/plinth check
	$(GO) test ./internal/api -run TestOpenAPIUsesGeneratedCommentDocs

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

bench:
	$(GO) test ./kernel -run '^$$' -bench 'BenchmarkHTTP' -benchmem -count=5

fuzz:
	$(GO) test ./kernel -run '^$$' -fuzz '^FuzzStrictJSONBinding$$' -fuzztime=5s -count=1
	$(GO) test ./kernel -run '^$$' -fuzz '^FuzzRouterPath$$' -fuzztime=5s -count=1

race:
	$(GO) test -race ./... -count=1

release-check: check test vet build race fuzz license-check

api-check:
	$(GO) test ./internal/apicheck -run TestPublicAPISurface -count=1

api-update:
	UPDATE_API=1 $(GO) test ./internal/apicheck -run TestPublicAPISurface -count=1

license-check:
	GO="$(GO)" GO_LICENSES_VERSION="$(GO_LICENSES_VERSION)" ./scripts/go-licenses.sh check --include_tests --allowed_licenses=$(ALLOWED_LICENSES) ./...

license-update:
	GO="$(GO)" GO_LICENSES_VERSION="$(GO_LICENSES_VERSION)" ./scripts/update-third-party-notices.sh

build: check
	mkdir -p bin
	$(GO) build -trimpath -o bin/plinth ./cmd/plinth
	$(GO) build -trimpath -o bin/plinth-migrate ./cmd/plinth-migrate

run:
	@test -f .env || (cp .env.example .env && echo "created .env from .env.example")
	@set -a; . ./.env; set +a; exec $(GO) run ./cmd/plinth

clean:
	rm -rf bin coverage.out
