# fynanz dev workflow. Requires: go, air, golang-migrate, golangci-lint.
DB_PATH ?= ./data/fynanz.db

.PHONY: dev build test lint creds migrate-up migrate-down migrate-create fmt

## creds: verify ENABLE_BANKING_APP_ID + private key against the API
creds:
	go run ./cmd/checkcreds

## dev: run the server with hot reload (needs .env with bank creds; run `make creds` first)
dev:
	@test -f .env && air || (echo "missing .env — cp .env.example .env"; exit 1)

## build: compile the server binary
build:
	go build -o bin/fynanz ./cmd/server

## test: run all tests (with race detector)
test:
	go test -race ./...

## lint: golangci-lint over the module
lint:
	golangci-lint run ./...

## migrate-up: apply pending migrations via the CLI (dev/ops)
migrate-up:
	migrate -path migrations -database "sqlite3://$(DB_PATH)" up

## migrate-down: roll back the last migration
migrate-down:
	migrate -path migrations -database "sqlite3://$(DB_PATH)" down 1

## migrate-create: scaffold a new migration, e.g. make migrate-create NAME=add_x
migrate-create:
	@test -n "$(NAME)" || (echo "usage: make migrate-create NAME=..."; exit 1)
	migrate create -ext sql -dir migrations -seq "$(NAME)"

## fmt: gofmt + tidy
fmt:
	gofmt -w .
	go mod tidy
