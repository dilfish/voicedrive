DATABASE_URL ?= postgres://voicedrive:voicedrive@localhost:5432/voicedrive?sslmode=disable
.DEFAULT_GOAL := build

.PHONY: build test db db-down run test-agent test-mock demo demo-check test-demo fmt-check

test-agent:
	go test ./internal/agenteval ./cmd/agent-eval

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

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

demo:
	go run ./cmd/voicedrive-demo $(DEMO_ARGS)

demo-check:
	go run ./cmd/voicedrive-demo -check $(DEMO_ARGS)

test-demo:
	go test -race -timeout=60s ./internal/demo ./internal/command ./internal/grouptools ./internal/lingxi ./internal/commandapp ./cmd/voicedrive-demo ./cmd/voicedrive-command
