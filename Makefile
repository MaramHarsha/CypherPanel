# CypherPanel build entrypoints. Works from Windows (Git Bash / make), macOS, and Linux.
# The product targets Linux servers; `make build` cross-compiles Linux binaries from any OS.

MODULE   := github.com/MaramHarsha/CypherPanel
BIN_DIR  := bin
GOFLAGS  := CGO_ENABLED=0

.PHONY: all build build-cli build-local test vet proto tools compose-up compose-down migrate-up migrate-down clean

all: vet test build

## Production binaries (always Linux — that's where CypherPanel runs).
build:
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -o $(BIN_DIR)/linux-amd64/cypher-core ./cmd/core
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -o $(BIN_DIR)/linux-amd64/cypher-agent ./cmd/agent
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -o $(BIN_DIR)/linux-amd64/cypherctl ./cmd/cypherctl
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -trimpath -o $(BIN_DIR)/linux-arm64/cypher-core ./cmd/core
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -trimpath -o $(BIN_DIR)/linux-arm64/cypher-agent ./cmd/agent
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -trimpath -o $(BIN_DIR)/linux-arm64/cypherctl ./cmd/cypherctl

## cypherctl is a client, not a server — operators run it from macOS/Windows too.
build-cli:
	$(GOFLAGS) GOOS=darwin  GOARCH=arm64 go build -trimpath -o $(BIN_DIR)/darwin-arm64/cypherctl ./cmd/cypherctl
	$(GOFLAGS) GOOS=darwin  GOARCH=amd64 go build -trimpath -o $(BIN_DIR)/darwin-amd64/cypherctl ./cmd/cypherctl
	$(GOFLAGS) GOOS=windows GOARCH=amd64 go build -trimpath -o $(BIN_DIR)/windows-amd64/cypherctl.exe ./cmd/cypherctl

## Native binaries for the current dev machine (for running cypher-core locally).
build-local:
	$(GOFLAGS) go build -o $(BIN_DIR)/local/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

## Regenerate gRPC code from proto/ (buf needs no system protoc).
proto: tools
	buf generate

## Regenerate the OpenAPI 3.1 spec from handler annotations.
openapi:
	go run github.com/swaggo/swag/v2/cmd/swag@latest init -g cmd/core/main.go -o docs --v3.1

tools:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

compose-up:
	docker compose up -d --wait

compose-down:
	docker compose down

## Apply migrations against the dev-stack database.
DEV_DB_URL ?= postgres://cypher:cypher-dev-only@localhost:5432/cypherpanel?sslmode=disable
migrate-up:
	go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest -path migrations -database "$(DEV_DB_URL)" up

migrate-down:
	go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest -path migrations -database "$(DEV_DB_URL)" down 1

clean:
	rm -rf $(BIN_DIR)
