#!/usr/bin/env bash
# Menjalankan stress test k6 di lingkungan terpisah supaya data platform
# utama tidak tersentuh:
#   - platform DB baru  : database k6_platform di container test-db
#   - database target   : k6_a, k6_b, k6_c di container test-db
#   - server            : go run ./cmd/server di port 8081 dengan admin khusus k6
#   - k6                : image grafana/k6 (tidak perlu instal k6)
#
# Pemakaian: bash k6/run.sh            (beban normal x1)
#            SCALE=0.5 bash k6/run.sh  (beban diperkecil)
set -euo pipefail
cd "$(dirname "$0")/.."

K6_IMAGE=${K6_IMAGE:-grafana/k6:0.54.0}
PORT=${K6_APP_PORT:-8081}
ADMIN_PASSWORD=${K6_ADMIN_PASSWORD:-k6-stress-admin-pass}
PSQL="docker compose exec -T test-db psql -U postgres -v ON_ERROR_STOP=1 -q"

docker compose up -d --wait test-db
$PSQL -c "DROP DATABASE IF EXISTS k6_platform WITH (FORCE)" -c "CREATE DATABASE k6_platform"
for db in k6_a k6_b k6_c; do
  $PSQL -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db"
  $PSQL -d "$db" -c "CREATE TABLE k6_items (id BIGSERIAL PRIMARY KEY, note TEXT NOT NULL)" \
        -c "INSERT INTO k6_items (note) SELECT 'awal ' || g FROM generate_series(1, 1000) g"
done

mkdir -p k6/hasil
export APP_ENV=development APP_ADDR=":$PORT" \
  DATABASE_URL="postgres://postgres:postgres@localhost:55432/k6_platform?sslmode=disable" \
  APP_SECRET_KEY="$(head -c 32 /dev/urandom | base64)" \
  ADMIN_USERNAME=admin ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  WORKER_ENABLED=true WORKER_CONCURRENCY=${WORKER_CONCURRENCY:-4} \
  OTEL_TRACES_EXPORTER=none
go build -o k6/hasil/server.exe ./cmd/server
./k6/hasil/server.exe > k6/hasil/server.log 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do
  curl -fs -o /dev/null -w '' "http://localhost:$PORT/api/auth/me" 2>/dev/null && break
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$PORT/api/auth/me")" = 401 ] && break
  sleep 1
done

# MSYS_NO_PATHCONV supaya Git Bash di Windows tidak mengubah path /work.
MSYS_NO_PATHCONV=1 docker run --rm -i \
  -v "$(pwd -W 2>/dev/null || pwd)/k6:/work/k6" -w /work \
  -e BASE_URL="http://host.docker.internal:$PORT" \
  -e ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  -e TARGET_HOST=localhost -e TARGET_PORT=55432 \
  -e SCALE="${SCALE:-1}" -e STAGE_SECONDS="${STAGE_SECONDS:-60}" \
  "$K6_IMAGE" run --quiet k6/stress.js | tee k6/hasil/output.txt
