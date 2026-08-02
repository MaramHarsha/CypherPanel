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
generate: proto sqlc installsh ## Regenerate all generated code (proto + sqlc + embedded installer)

.PHONY: installsh
installsh: ## Copy the canonical installer into core for go:embed (one home: /install)
	cp install/agent.sh core/api/rest/install-agent.sh

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

.PHONY: build-web
build-web: ## Build the web UI and sync it into the Go embed path (webui)
	cd web && pnpm install --frozen-lockfile && pnpm build
	rm -rf core/api/rest/webui/dist
	cp -r web/dist core/api/rest/webui/dist

.PHONY: generate-web
generate-web: ## Regenerate the web API client from openapi.yaml (orval)
	cd web && pnpm generate:api

.PHONY: build-crosscheck
build-crosscheck: ## Cross-compile the binaries for linux/arm64 (catch ARM breakage in CI)
	cd core  && GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/cypherd
	cd agent && GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/cypher-agent

.PHONY: test
test: ## Run unit tests across all modules
	@for m in $(MODULES); do echo "test $$m"; (cd $$m && go test ./...) || exit 1; done

.PHONY: test-race
test-race: ## Run tests with the race detector (as CI does)
	@for m in $(MODULES); do echo "test -race $$m"; (cd $$m && go test -race ./...) || exit 1; done

.PHONY: test-store
test-store: ## Run the real-Postgres store tests against a throwaway container
	@docker rm -f cypher-store-test-pg >/dev/null 2>&1 || true
	docker run -d --name cypher-store-test-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=cypher_test \
		-p 127.0.0.1:15440:5432 postgres:16-alpine >/dev/null
	@until docker exec cypher-store-test-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	cd core && CYPHERD_TEST_DATABASE_URL="postgres://postgres:pw@127.0.0.1:15440/cypher_test?sslmode=disable" \
		go test ./store/ -run TestStore -v; status=$$?; \
		docker rm -f cypher-store-test-pg >/dev/null; exit $$status

.PHONY: vet
vet: ## go vet across all modules
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

.PHONY: fmt
fmt: ## Format all Go code
	@for m in $(MODULES); do (cd $$m && gofmt -w .); done

.PHONY: lint
lint: ## Run golangci-lint per module (from the root it silently checks nothing)
	@for m in $(MODULES); do echo "lint $$m"; (cd $$m && golangci-lint run) || exit 1; done

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

.PHONY: release-sign
release-sign: ## Rebuild, verify, sign offline and publish a draft release (VERSION=vX.Y.Z)
	@test -n "$(VERSION)" || { echo "usage: make release-sign VERSION=v0.1.0"; exit 1; }
	sh scripts/release-sign.sh "$(VERSION)"
