TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable
TEST_MYSQL_DATABASE_URL ?= platform_app:platform_app_pass@tcp(localhost:3306)/kampus?parseTime=true&multiStatements=true

.PHONY: up down run test test-unit test-integration vet fmt keygen fe-dev fe-build obs-up obs-down

up:            ## nyalakan semua database 
	docker compose up -d

down:
	docker compose down

run:           ## jalankan server
	set -a && . ./.env && set +a && go run ./cmd/server

test-unit:     ## unit test saja
	go test ./...

test-integration: ## unit + integration test terhadap test-db dan target-mysql
	docker compose up -d --wait test-db target-mysql
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" TEST_MYSQL_DATABASE_URL="$(TEST_MYSQL_DATABASE_URL)" go test -count=1 ./...

test: test-integration

vet:
	go vet ./...

fmt:
	gofmt -w .

keygen:        ## buat APP_SECRET_KEY baru
	@openssl rand -base64 32

fe-dev:        ## jalankan frontend vite dev server
	cd frontend && npm run dev

fe-build:      ## build frontend ke web/dist
	cd frontend && npm run build

obs-up:        ## jalankan stack observability (otel collector, jaeger, prometheus, grafana)
	docker compose --profile obs up -d

obs-down:      ## matikan stack observability
	docker compose --profile obs down
