# plane-forge-bridge Makefile.
#
# Run `make` (or `make help`) for the target list. Each target's `## ...`
# trailing comment is what `help` scrapes.

BINARY := plane-forge-bridge
PKG    := ./cmd/$(BINARY)

GO            ?= go
DOCKER        ?= docker
GOLANGCI_LINT ?= golangci-lint

.DEFAULT_GOAL := help

.PHONY: help build run test race cover lint vet vuln tidy image image-test e2e clean

help: ## Show this help.
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build the static binary into bin/.
	$(GO) build -trimpath -ldflags="-s -w" -o bin/$(BINARY) $(PKG)

run: build ## Build and run the binary against ./config.yaml.
	./bin/$(BINARY) --config config.yaml

test: ## Run unit tests.
	$(GO) test ./...

race: ## Run tests with the race detector.
	$(GO) test -race -count=1 ./...

cover: ## Run tests and emit coverage.html.
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

lint: ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

vet: ## Run go vet.
	$(GO) vet ./...

vuln: ## Run govulncheck.
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy: ## Run go mod tidy.
	$(GO) mod tidy

image: ## Build the runtime container image (tag plane-forge-bridge:dev).
	$(DOCKER) buildx build --target=runtime -t $(BINARY):dev .

image-test: ## Build the Dockerfile `test` stage (vet + race + govulncheck).
	$(DOCKER) buildx build --target=test .

e2e: ## Run the e2e-docker harness. Placeholder until the harness exists.
	@echo "TODO: e2e not yet implemented"

clean: ## Remove build + coverage artifacts.
	rm -rf bin/ dist/ coverage.out coverage.html
