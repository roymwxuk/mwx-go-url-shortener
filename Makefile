LOCAL_DB_URL ?= postgres://shortener:shortener@localhost:5432/shortener?sslmode=disable

.PHONY: run run-pg db-up db-down test test-pg build

run:            ## Run with in-memory store
	go run ./cmd/server

db-up:          ## Start local Postgres
	docker compose up -d --wait postgres

db-down:
	docker compose down

run-pg: db-up   ## Run against local Postgres
	DATABASE_URL="$(LOCAL_DB_URL)" go run ./cmd/server

test:           ## Unit + concurrency tests with the race detector (in-memory store)
	go test -race ./...

test-pg: db-up  ## Same, plus the store concurrency tests against real Postgres
	TEST_DATABASE_URL="$(LOCAL_DB_URL)" go test -race -count=1 ./...

build:
	go build -o bin/server ./cmd/server
