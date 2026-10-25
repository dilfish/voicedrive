DATABASE_URL ?= postgres://voicedrive:voicedrive@localhost:5432/voicedrive?sslmode=disable
.DEFAULT_GOAL := build

.PHONY: build test db db-down run test-agent test-mock

test-agent:
	go test ./internal/agenteval ./cmd/agent-eval

build:
	go build ./...

test:
	go test ./...

db:
	docker compose up -d postgres

db-down:
	docker compose down

run:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/server

test-mock:
	go test -race -count=1 -timeout=60s ./internal/mock ./cmd/linx-mock ./internal/scriptmock ./cmd/linx-scriptmock
