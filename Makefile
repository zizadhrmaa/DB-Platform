TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable
TEST_MYSQL_DATABASE_URL ?= platform_app:platform_app_pass@tcp(localhost:3306)/kampus?parseTime=true&multiStatements=true

.PHONY: up down run test test-unit test-integration vet fmt keygen fe-dev fe-build obs-up obs-down bench-batch k6

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
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" TEST_MYSQL_DATABASE_URL="$(TEST_MYSQL_DATABASE_URL)" go test -p 1 -count=1 ./...

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
	docker compose --profile obs up -d otel-collector loki jaeger prometheus grafana

obs-down:      ## matikan stack observability
	docker compose --profile obs down

BENCH_DATABASE_URL ?= postgres://postgres:postgres@localhost:55432/bench?sslmode=disable
BENCH_ROWS ?= 3000000

bench-batch:   ## dataset uji: UPDATE penuh tanpa batch vs per batch (database bench di test-db)
	docker compose up -d --wait test-db
	docker compose exec -T test-db psql -U postgres -qc "DROP DATABASE IF EXISTS bench" -c "CREATE DATABASE bench"
	BENCH_DATABASE_URL="$(BENCH_DATABASE_URL)" go run ./cmd/benchbatch -rows $(BENCH_ROWS) -sizes 10000,50000,200000

k6:            ## stress test k6 di lingkungan terpisah (lihat k6/run.sh)
	bash k6/run.sh
