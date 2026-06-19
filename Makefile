# ledger-wallet — common dev commands. Run `make help` to list them.
DB_URL      ?= postgres://ledger:ledger@localhost:5432/ledger
TEST_DB_URL ?= postgres://ledger:ledger@localhost:5432/ledger_test

.PHONY: help test test-pg db-up pgadmin db-down db-reset db-logs run run-pg vet build tidy

help: ## list available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

test: ## run unit tests (in-memory; Postgres tests skip)
	go test ./...

test-pg: ## run ALL tests incl. Postgres integration (needs: make db-up)
	TEST_DATABASE_URL=$(TEST_DB_URL) go test ./... -v

db-up: ## start Postgres in Docker (background)
	docker compose up -d

pgadmin: ## start Postgres + pgAdmin GUI at http://localhost:5050
	docker compose --profile tools up -d
	@echo "pgAdmin: http://localhost:5050  (no login needed; DB password: ledger)"

db-down: ## stop Postgres, keep the data volume
	docker compose down

db-reset: ## stop Postgres AND wipe its data volume
	docker compose down -v

db-logs: ## tail Postgres logs
	docker compose logs -f db

run: ## run the server with the in-memory backend
	go run ./cmd/server

run-pg: ## run the server against the Docker Postgres
	DATABASE_URL=$(DB_URL) go run ./cmd/server

vet: ## go vet
	go vet ./...

build: ## go build
	go build ./...

tidy: ## go mod tidy
	go mod tidy
