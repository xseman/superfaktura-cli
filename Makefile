BINARY  := sf
PKG     := github.com/xseman/superfaktura-cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

## help: list the available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /' | column -t -s ':'

## build: compile the binary to ./bin/sf
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

## install: install the binary into GOBIN
install:
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

## test: run unit tests
test:
	go test ./internal/... ./cmd/...

## test-race: run unit tests with the race detector
test-race:
	go test -race -count=1 ./internal/... ./cmd/...

## test-e2e: run end-to-end tests against a real account (needs .env.test)
test-e2e: build
	go test -count=1 -tags e2e ./e2e/...

## seed: create records on a real account and LEAVE them (needs .env.test)
seed: build
	SF_SEED=1 go test -count=1 -tags e2e ./e2e/ -run TestSeedSandbox -v

## test-run: run one test by name, e.g. make test-run NAME=TestSurfaceSnapshot
test-run:
	go test ./... -run '$(NAME)' -v

# Fuzzing is not part of test or check: go test runs each target's seed and
# regression corpus every time anyway, which is the part that must never
# regress. This target is the search for the next one, and it is open-ended by
# nature — one target at a time, because -fuzz takes exactly one.
#
## fuzz: search one fuzz target, e.g. make fuzz NAME=FuzzWrapText TIME=60s
fuzz:
	@test -n '$(NAME)' || { echo "set NAME, e.g. make fuzz NAME=FuzzWrapText"; exit 1; }
	@pkg=$$(grep -rl 'func $(NAME)(' --include='*_test.go' internal cmd | head -1); \
	if [ -z "$$pkg" ]; then echo "no fuzz target named $(NAME)"; exit 1; fi; \
	go test ./$$(dirname $$pkg)/ -run=NONE -fuzz='^$(NAME)$$' -fuzztime=$(or $(TIME),30s)

## fuzz-list: list the fuzz targets
fuzz-list:
	@grep -rhno 'func Fuzz[A-Za-z0-9_]*' --include='*_test.go' internal cmd | \
		sed 's/.*func //' | sort -u

## cover: report test coverage per package
cover:
	go test -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

## surface-snapshot: regenerate SURFACE.txt after an intentional CLI change
surface-snapshot:
	UPDATE_SURFACE=1 go test ./internal/commands/ -run TestSurfaceSnapshot

## surface-check: fail if the CLI surface drifted from SURFACE.txt
surface-check:
	go test ./internal/commands/ -run TestSurfaceSnapshot

## frames-snapshot: regenerate the golden TUI frames after an intentional layout change
frames-snapshot:
	go test ./internal/tui/ -count=1 -run TestTheBrowserDrawsItsFrames -update

## fmt: format the source
fmt:
	gofmt -w cmd internal e2e

## fmt-check: fail if anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l cmd internal e2e); \
	if [ -n "$$unformatted" ]; then echo "unformatted:"; echo "$$unformatted"; exit 1; fi

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint
lint:
	golangci-lint run

# govulncheck reports only the advisories whose vulnerable *symbols* this
# module actually reaches, so a hit here is a thing to act on rather than a
# list to triage. It is deliberately not in `check`: it downloads the
# vulnerability database from vuln.go.dev on every run, and `check` is what
# runs before every commit, on a laptop that may be on a train. It belongs in
# `security` and in release preflight, where a network round trip is expected.
#
## vuln: scan the dependency tree for known vulnerabilities (govulncheck)
vuln:
	@if ! command -v govulncheck >/dev/null 2>&1; then \
		echo "Skipping govulncheck (not installed — go install golang.org/x/vuln/cmd/govulncheck@latest)"; \
	else \
		govulncheck ./...; \
	fi

# This CLI's whole job is to hold an API key, so a key pasted into a fixture,
# a doc example or a commit message is the failure mode worth a scanner.
# gitleaks works off its own default rules; .gitleaks.toml is only consulted if
# somebody adds one.
#
## secrets: scan the tree and its history for committed credentials (gitleaks)
secrets:
	@if ! command -v gitleaks >/dev/null 2>&1; then \
		echo "Skipping gitleaks (not installed — see https://github.com/gitleaks/gitleaks)"; \
	elif [ -f .gitleaks.toml ]; then \
		gitleaks detect --source . --config .gitleaks.toml --verbose; \
	else \
		gitleaks detect --source . --verbose; \
	fi

## security: vuln + secrets
security: vuln secrets

## tidy-check: fail if go.mod or go.sum would change
tidy-check:
	@cp go.mod go.mod.bak && cp go.sum go.sum.bak
	@go mod tidy
	@if ! diff -q go.mod go.mod.bak >/dev/null || ! diff -q go.sum go.sum.bak >/dev/null; then \
		mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
		echo "go.mod or go.sum is out of date — run 'go mod tidy'"; exit 1; \
	fi
	@rm -f go.mod.bak go.sum.bak

## check: everything CI runs
check: fmt-check vet lint tidy-check test-race

## clean: remove build artifacts
clean:
	rm -rf bin coverage.out

.PHONY: help build install test test-race test-e2e seed test-run fuzz fuzz-list \
	cover surface-snapshot surface-check frames-snapshot fmt fmt-check vet lint \
	vuln secrets security tidy-check check clean
