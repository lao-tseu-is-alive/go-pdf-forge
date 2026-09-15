GO ?= go
BUF ?= buf

VERSION_PKG := github.com/lao-tseu-is-alive/go-pdf-forge/internal/version
VERSION := $(shell sed -n 's/^[[:space:]]*Version = "\([0-9][0-9.]*\)"/\1/p' internal/version/version.go)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(VERSION_PKG).Commit=$(GIT_COMMIT) -X $(VERSION_PKG).Date=$(BUILD_DATE)

.PHONY: build changelog-check check fmt generate generated-check proto-check release release-check release-prepare scripts-check test version-check vet

generate:
	$(BUF) generate

generated-check:
	$(BUF) generate
	@git diff --exit-code -- gen/go web/src/gen

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

check: proto-check test vet
	@test -z "$$(gofmt -l $$(rg --files -g '*.go'))"
	@git diff --check

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mail-poc ./cmd/mail-poc

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

release-check: check generated-check version-check changelog-check scripts-check build
	@./bin/mail-poc --version | grep -q "^go-pdf-forge v$(VERSION) (commit $(GIT_COMMIT), built "
	@echo "release-check: v$(VERSION) OK"

release-prepare: release-check
	@echo "Release v$(VERSION) is ready to commit."
	@echo "After reviewing: stage only reviewed files, inspect the index, then commit."
	@echo "Suggested message: chore(release): prepare v$(VERSION)"
	@echo "Then publish with: CONFIRM_RELEASE=v$(VERSION) make release"

release:
	@./scripts/release.sh
