GO ?= go
BUF ?= buf

VERSION_PKG := github.com/lao-tseu-is-alive/go-pdf-forge/internal/version
VERSION := $(shell sed -n 's/^[[:space:]]*Version = "\([0-9][0-9.]*\)"/\1/p' internal/version/version.go)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(VERSION_PKG).Commit=$(GIT_COMMIT) -X $(VERSION_PKG).Date=$(BUILD_DATE)

.PHONY: atlas-check build changelog-check check docs-assert docs-check fmt generate generated-check godoc-check proto-check release release-check release-prepare release-traceability-check roadmap-check scripts-check test version-check vet

generate:
	$(BUF) generate

generated-check:
	@snapshot="$$(mktemp -d)"; \
		trap 'rm -rf "$$snapshot"' EXIT; \
		cp -a gen/go "$$snapshot/gen-go"; \
		cp -a web/src/gen "$$snapshot/web-gen"; \
		$(BUF) generate; \
		diff -ru "$$snapshot/gen-go" gen/go; \
		diff -ru "$$snapshot/web-gen" web/src/gen

fmt:
	$(BUF) format -w
	gofmt -w $$(rg --files -g '*.go')

proto-check:
	$(BUF) format -d --exit-code
	$(BUF) lint

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

godoc-check:
	$(GO) run ./cmd/doccheck --scope go

atlas-check:
	$(GO) run ./cmd/doccheck --scope atlas

docs-assert:
	bash scripts/check_documentation_claims.sh

docs-check: godoc-check atlas-check docs-assert

check: proto-check test vet docs-check
	@test -z "$$(gofmt -l $$(rg --files -g '*.go'))"
	@git diff --check

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mail-poc ./cmd/mail-poc
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/pdf-migrate ./cmd/pdf-migrate

version-check:
	@test -n "$(VERSION)" || { echo "version-check: cannot read internal/version/version.go"; exit 1; }
	@grep -q "Current version: \*\*v$(VERSION)\*\*" README.md || { echo "version-check: README does not announce v$(VERSION)"; exit 1; }
	@echo "version-check: v$(VERSION)"

changelog-check:
	@test -f CHANGELOG.md || { echo "changelog-check: CHANGELOG.md is missing"; exit 1; }
	@duplicates="$$(sed -n 's/^## \[\([0-9][0-9.]*\)\].*/\1/p' CHANGELOG.md | sort | uniq -d)"; \
		test -z "$$duplicates" || { echo "changelog-check: duplicate versions: $$duplicates"; exit 1; }
	@grep -q "^## \[$(VERSION)\] - [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$$" CHANGELOG.md || { echo "changelog-check: no dated v$(VERSION) section"; exit 1; }
	@echo "changelog-check: v$(VERSION) documented"

scripts-check:
	bash -n scripts/*.sh

roadmap-check:
	@test -f docs/ROADMAP.md || { echo "roadmap-check: docs/ROADMAP.md is missing"; exit 1; }
	@grep -q "Version suivie : \*\*v$(VERSION)\*\*" docs/ROADMAP.md || { echo "roadmap-check: tracked version does not match v$(VERSION)"; exit 1; }
	@ids="$$(grep -oE 'GPF-[0-9]{3}' docs/ROADMAP.md)"; \
		test -n "$$ids" || { echo "roadmap-check: no task IDs found"; exit 1; }; \
		duplicates="$$(printf '%s\n' "$$ids" | sort | uniq -d)"; \
		test -z "$$duplicates" || { echo "roadmap-check: duplicate task IDs: $$duplicates"; exit 1; }
	@grep -q '^## Prochaine action$$' docs/ROADMAP.md || { echo "roadmap-check: missing next-action section"; exit 1; }
	@echo "roadmap-check: OK"

release-traceability-check:
	@bash scripts/check_release_traceability.sh

release-check: check generated-check version-check changelog-check scripts-check roadmap-check release-traceability-check build
	@./bin/mail-poc --version | grep -q "^go-pdf-forge v$(VERSION) (commit $(GIT_COMMIT), built "
	@./bin/pdf-migrate --version | grep -q "^go-pdf-forge v$(VERSION) (commit $(GIT_COMMIT), built "
	@echo "release-check: v$(VERSION) OK"

release-prepare: release-check
	@echo "Release v$(VERSION) is ready to commit."
	@echo "After reviewing: stage only reviewed files, inspect the index, then commit."
	@echo "Suggested message: chore(release): prepare v$(VERSION)"
	@echo "Then publish with: CONFIRM_RELEASE=v$(VERSION) make release"

release:
	@./scripts/release.sh
