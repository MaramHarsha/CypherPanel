# CypherPanel — build / test / dev orchestration.
# The Go workspace (go.work) ties the three modules together; most targets
# iterate them so `go build ./...` semantics apply across the whole tree.

MODULES := pkg core agent
GOBIN   := $(shell go env GOPATH)/bin

DEV_DATABASE_URL ?= postgres://cypherpanel:devpassword@localhost:5432/cypherpanel?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ── Generation ─────────────────────────────────────────────────────────────

.PHONY: generate
generate: proto sqlc ## Regenerate all generated code (proto + sqlc)

.PHONY: proto
proto: ## Generate Go stubs from proto/ (buf)
	buf lint proto
	buf generate

.PHONY: sqlc
sqlc: ## Generate the type-safe store from core/store/queries
	cd core && sqlc generate

## ── Build & quality ────────────────────────────────────────────────────────

.PHONY: build
build: ## Build every module for the host platform
	@for m in $(MODULES); do echo "build $$m"; (cd $$m && go build ./...) || exit 1; done

.PHONY: build-crosscheck
build-crosscheck: ## Cross-compile the binaries for linux/arm64 (catch ARM breakage in CI)
	cd core  && GOOS=linux GOARCH=arm64 go build ./cmd/cypherd
	cd agent && GOOS=linux GOARCH=arm64 go build ./cmd/cypher-agent

.PHONY: test
test: ## Run unit tests across all modules
	@for m in $(MODULES); do echo "test $$m"; (cd $$m && go test ./...) || exit 1; done

.PHONY: test-race
test-race: ## Run tests with the race detector (as CI does)
	@for m in $(MODULES); do echo "test -race $$m"; (cd $$m && go test -race ./...) || exit 1; done

.PHONY: vet
vet: ## go vet across all modules
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

.PHONY: fmt
fmt: ## Format all Go code
	@for m in $(MODULES); do (cd $$m && gofmt -w .); done

.PHONY: lint
lint: ## Run golangci-lint across the workspace
	golangci-lint run

.PHONY: tidy
tidy: ## go mod tidy every module
	@for m in $(MODULES); do echo "tidy $$m"; (cd $$m && go mod tidy) || exit 1; done

.PHONY: check
check: proto build vet test ## Fast local pre-commit gate

## ── Dev environment ────────────────────────────────────────────────────────

.PHONY: dev-up
dev-up: ## Start local Postgres
	docker compose -f docker-compose.dev.yml up -d

.PHONY: dev-down
dev-down: ## Stop local Postgres (keeps the volume)
	docker compose -f docker-compose.dev.yml down

.PHONY: run-plane
run-plane: ## Run cypherd from source against local Postgres
	cd core && CYPHERD_DATABASE_URL="$(DEV_DATABASE_URL)" go run ./cmd/cypherd

.PHONY: clean
clean: ## Remove build output and local dev state
	rm -rf bin dist .dev
	@for m in $(MODULES); do (cd $$m && go clean); done
