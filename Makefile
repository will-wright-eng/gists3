#* Variables
# SHELL := /usr/bin/env bash
REPO_ROOT := $(shell git rev-parse --show-toplevel)
export REPO_ROOT

# Astro 7 needs Node >=22.12 but this box runs Node 20; Bun's runtime reports
# Node-compat 24, so every Astro command goes through Bun. The package.json
# scripts already wrap Astro in `bun --bun ...`, so `bun run <script>` is enough.

#* Setup
.PHONY: $(shell sed -n -e '/^$$/ { n ; /^[^ .\#][^ ]*:/ { s/:.*$$// ; p ; } ; }' $(MAKEFILE_LIST))
.DEFAULT_GOAL := help

help: ## list make commands
	@echo ${MAKEFILE_LIST}
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## gofmt all packages
	gofmt -w .

fmt-check: ## fail if any file needs gofmt
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi

vet: ## go vet, including integration-tagged files
	go vet ./...
	go vet -tags integration ./...

lint: ## staticcheck
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

test: ## hermetic unit tests
	go test ./...

race: ## unit tests with the race detector
	go test -race ./...

cover: ## engine coverage; tests live in internal/gists3test, so -coverpkg names the packages under test
	go test -cover -coverpkg=./internal/gists3,./internal/gistapi ./internal/gists3test

integration: ## live-API tests; requires GIST_TOKEN (gist scope) or an authenticated gh CLI
	go test -tags integration -run Integration -count=1 -v ./...

example: ## run the end-to-end example; requires GIST_TOKEN or an authenticated gh CLI
	go run ./cmd/example

build: ## compile all packages; g3 binary lands in dist/
	go build ./...
	@mkdir -p dist
	go build -o dist/ ./cmd/g3

install: build ## copy the g3 binary into $$HOME/go/bin
	@mkdir -p $(HOME)/go/bin
	install dist/g3 $(HOME)/go/bin/g3

check: ## everything CI runs: fmt-check, vet, lint, race tests, build
	$(MAKE) fmt-check vet lint race build

clean: ## remove test caches and build artifacts
	go clean -testcache
	go clean ./...
	rm -rf dist
