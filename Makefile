DATABASE_URL ?= postgres://voicedrive:voicedrive@localhost:5432/voicedrive?sslmode=disable

.PHONY: build test db db-down run test-mock

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
	go test -count=1 -timeout=60s ./internal/mock ./cmd/linx-mock
