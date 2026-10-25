TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable

.PHONY: up down run test test-unit test-integration vet fmt keygen

up:            ## nyalakan semua database 
	docker compose up -d

down:
	docker compose down

run:           ## jalankan server
	set -a && . ./.env && set +a && go run ./cmd/server

test-unit:     ## unit test saja
	go test ./...

test-integration: ## unit + integration test terhadap test-db
	docker compose up -d --wait test-db
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -count=1 ./...

test: test-integration

vet:
	go vet ./...

fmt:
	gofmt -w .

keygen:        ## buat APP_SECRET_KEY baru
	@openssl rand -base64 32
