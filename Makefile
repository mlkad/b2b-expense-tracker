# Development tasks. Every target is safe to run repeatedly.

SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE      := github.com/mlkad/b2b-expense-tracker
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOBIN       ?= $(shell go env GOPATH)/bin

DEV_DSN     ?= postgres://expense:local_dev_pw@127.0.0.1:5441/expenses?sslmode=disable

export GOOSE_DRIVER        := postgres
export GOOSE_MIGRATION_DIR := db/migrations

.PHONY: help
help: ## List the targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# -----------------------------------------------------------------------------
# Dependencies
# -----------------------------------------------------------------------------

.PHONY: up
up: ## Start PostgreSQL and Redis
	docker compose up -d
	@echo "waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U expense -d expenses >/dev/null 2>&1; do sleep 1; done
	@echo "ready"

.PHONY: down
down: ## Stop the dependencies, keeping their data
	docker compose down

.PHONY: nuke
nuke: ## Stop the dependencies and delete their data
	docker compose down -v

# -----------------------------------------------------------------------------
# Database
# -----------------------------------------------------------------------------

.PHONY: migrate-up
migrate-up: ## Apply migrations (as the owner, not the runtime role)
	GOOSE_DBSTRING="$(DEV_DSN)" $(GOBIN)/goose up

.PHONY: migrate-down
migrate-down: ## Roll back one migration
	GOOSE_DBSTRING="$(DEV_DSN)" $(GOBIN)/goose down

.PHONY: migrate-status
migrate-status: ## Show which migrations are applied
	GOOSE_DBSTRING="$(DEV_DSN)" $(GOBIN)/goose status

.PHONY: migrate-new
migrate-new: ## Create a migration: make migrate-new name=add_widgets
	@test -n "$(name)" || (echo "usage: make migrate-new name=add_widgets" && exit 1)
	$(GOBIN)/goose create $(name) sql

.PHONY: sqlc
sqlc: ## Regenerate the query layer from db/queries
	$(GOBIN)/sqlc generate
	@go build ./... && echo "generated code compiles"

.PHONY: sqlc-vet
sqlc-vet: ## Check the queries parse and type-check against the schema
	$(GOBIN)/sqlc vet

# -----------------------------------------------------------------------------
# Build and run
# -----------------------------------------------------------------------------

.PHONY: build
build: ## Build both binaries
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/api    ./cmd/api
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/worker ./cmd/worker

.PHONY: run
run: ## Run the API against the local dependencies
	set -a && source .env && set +a && go run ./cmd/api

.PHONY: run-worker
run-worker: ## Run the background worker
	set -a && source .env && set +a && go run ./cmd/worker

# -----------------------------------------------------------------------------
# Tests
# -----------------------------------------------------------------------------

.PHONY: test
test: ## Unit tests, race detector on
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Integration tests against a throwaway PostgreSQL container
	go test -tags integration -race -count=1 -timeout 15m ./test/integration/

.PHONY: test-all
test-all: test test-integration ## Everything

# COVERPKG is every package that carries logic. cmd is excluded because it is
# process wiring, and the generated query layer because measuring it would
# report the coverage of sqlc's code generator rather than of this project.
COVERPKG := ./internal/domain/...,./internal/export/...,./internal/service/...,\
./internal/gateway/...,./internal/auth/...,./internal/repository/postgres,\
./internal/platform/...,./internal/config/...,./internal/transport/...,./internal/worker/...

.PHONY: cover
cover: ## One coverage number over unit and integration tests together
	# The integration tag is included on purpose. The persistence and tenancy
	# layers cannot be meaningfully exercised without a database, so a number
	# measured without them describes a different program than the one that
	# ships.
	go test -tags integration -count=1 -timeout 20m \
		-coverpkg=$(COVERPKG) -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

.PHONY: cover-gaps
cover-gaps: ## List functions with no coverage at all
	@go test -tags integration -count=1 -timeout 20m \
		-coverpkg=$(COVERPKG) -coverprofile=coverage.out ./... >/dev/null
	@go tool cover -func=coverage.out | awk '$$3 == "0.0%"' | sed 's#github.com/mlkad/b2b-expense-tracker/##' 

# -----------------------------------------------------------------------------
# Quality
# -----------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Format
	gofmt -w -s .

.PHONY: vet
vet: ## go vet, including the integration build tag
	go vet ./...
	go vet -tags integration ./...

.PHONY: tidy
tidy: ## Tidy and verify the module
	go mod tidy
	go mod verify

.PHONY: check
check: fmt vet test ## Format, vet and unit test - what CI runs first

# -----------------------------------------------------------------------------
# Containers
# -----------------------------------------------------------------------------

.PHONY: images
images: ## Build the api, worker and migrate images
	docker build --target api     --build-arg VERSION=$(VERSION) -t expense-api:$(VERSION)     .
	docker build --target worker  --build-arg VERSION=$(VERSION) -t expense-worker:$(VERSION)  .
	docker build --target migrate --build-arg VERSION=$(VERSION) -t expense-migrate:$(VERSION) .
	@docker images --format '  {{.Repository}}:{{.Tag}}\t{{.Size}}' | grep '^  expense-'

.PHONY: stack
stack: ## Run the whole service in containers against the local dependencies
	docker compose -f docker-compose.yml -f docker-compose.stack.yml up -d --build
	@docker compose -f docker-compose.yml -f docker-compose.stack.yml ps

.PHONY: stack-down
stack-down: ## Stop the containerised service, leaving the dependencies running
	docker compose -f docker-compose.yml -f docker-compose.stack.yml down

.PHONY: tools
tools: ## Install the pinned developer tools
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
