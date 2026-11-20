package obs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestRedactSensitiveKeys(t *testing.T) {
	tests := []struct {
		key      string
		val      any
		wantMask bool
	}{
		{"password", "supersecret", true},
		{"AdminPassword", "123456", true},
		{"csrf_token", "abc123xyz", true},
		{"session_id", "sess_999", true},
		{"cookie", "dbp_session=xxx", true},
		{"database_dsn", "postgres://u:p@localhost:5432/db", true},
		{"ciphertext", "0123456789abcdef", true},
		{"actor", "budi", false},
		{"database", "toko", false},
		{"query", "SELECT 1", false},
	}

	for _, tt := range tests {
		res := RedactValue(tt.key, tt.val)
		if tt.wantMask && res != RedactedPlaceholder {
			t.Errorf("RedactValue(%q) = %v; want %q", tt.key, res, RedactedPlaceholder)
		}
		if !tt.wantMask && res == RedactedPlaceholder {
			t.Errorf("RedactValue(%q) was masked unexpectedly", tt.key)
		}
	}
}

func TestRedactDSNInString(t *testing.T) {
	dsn := "postgres://user:rahasia123@localhost:5432/dbplatform?sslmode=disable"
	redacted := RedactString(dsn)
	if strings.Contains(redacted, "rahasia123") {
		t.Errorf("DSN password tidak diredaksi: %s", redacted)
	}
	if !strings.Contains(redacted, RedactedPlaceholder) {
		t.Errorf("DSN tidak mengandung placeholder: %s", redacted)
	}
}

func TestJSONHandlerWithTraceAndContext(t *testing.T) {
	var buf bytes.Buffer
	handler := NewJSONHandler(&buf, nil)
	logger := slog.New(handler)

	// Inisialisasi tracer
	_, _, err := InitTracer(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}

	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-xyz-123")
	ctx = WithActor(ctx, "alice")
	ctx = WithDatabase(ctx, "toko")
	ctx = WithJobContext(ctx, JobContext{
		JobID:       10,
		ExecutionID: 20,
		MigrationID: 30,
		Attempt:     1,
		Batch:       2,
	})

	ctx, span := StartSpan(ctx, "test-span")
	defer span.End()

	logger.InfoContext(ctx, "http_request",
		"status", 200,
		"password", "secret_pwd",
		"dsn", "postgres://admin:pass123@host:5432/mydb",
	)

	out := buf.String()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output bukan JSON valid: %s, err: %v", out, err)
	}

	// Cek atribut wajib
	if m["msg"] != "http_request" {
		t.Errorf("msg = %v; want http_request", m["msg"])
	}
	if m["request_id"] != "req-xyz-123" {
		t.Errorf("request_id = %v; want req-xyz-123", m["request_id"])
	}
	if m["actor"] != "alice" {
		t.Errorf("actor = %v; want alice", m["actor"])
	}
	if m["database"] != "toko" {
		t.Errorf("database = %v; want toko", m["database"])
	}
	if m["trace_id"] == nil || m["trace_id"] == "" {
		t.Error("trace_id kosong di log")
	}
	if m["span_id"] == nil || m["span_id"] == "" {
		t.Error("span_id kosong di log")
	}

	// Cek redaksi
	if m["password"] != RedactedPlaceholder {
		t.Errorf("password = %v; want %q", m["password"], RedactedPlaceholder)
	}
	if m["dsn"] != RedactedPlaceholder {
		t.Errorf("dsn = %v; want %q", m["dsn"], RedactedPlaceholder)
	}
}

func TestTraceContextPropagation(t *testing.T) {
	_, _, err := InitTracer(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}

	ctx := context.Background()
	ctx, span := StartSpan(ctx, "root-span")
	defer span.End()

	sc := span.SpanContext()
	if !sc.IsValid() {
		t.Fatal("span context tidak valid")
	}

	// Inject ke format W3C traceparent
	tp := InjectTraceParent(ctx)
	if tp == "" || !strings.HasPrefix(tp, "00-") {
		t.Fatalf("W3C traceparent tidak valid: %q", tp)
	}
	if !strings.Contains(tp, sc.TraceID().String()) {
		t.Errorf("traceparent %q tidak memuat trace_id %s", tp, sc.TraceID().String())
	}

	// Ekstrak di context penerima (misal worker)
	workerCtx := ExtractTraceParent(context.Background(), tp)
	workerSpan := trace.SpanFromContext(workerCtx)
	if workerSpan.SpanContext().TraceID() != sc.TraceID() {
		t.Errorf("trace_id tidak cocok setelah ekstraksi: got %s, want %s",
			workerSpan.SpanContext().TraceID(), sc.TraceID())
	}
}

type mockPoolProvider struct{}

func (m *mockPoolProvider) Stats() map[int64]sql.DBStats {
	return map[int64]sql.DBStats{
		1: {
			OpenConnections:    5,
			InUse:              2,
			Idle:               3,
			MaxOpenConnections: 10,
			WaitCount:          1,
			WaitDuration:       50 * time.Millisecond,
		},
	}
}

func TestMetricsEndpoint(t *testing.T) {
	RegisterPoolProvider(&mockPoolProvider{})
	RecordJobStatus("toko", "running")
	RecordJobStatus("toko", "succeeded")
	RecordJobRetry("toko")
	RecordQueueWait(100 * time.Millisecond)
	RecordLockWait(50 * time.Millisecond)
	RecordMigrationDuration(250 * time.Millisecond)
	RecordBatchProcessed("toko", 1000)

	handler := MetricsHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"dbp_jobs_total{db=\"toko\",status=\"running\"} 1",
		"dbp_jobs_total{db=\"toko\",status=\"succeeded\"} 1",
		"dbp_job_retries_total{db=\"toko\"} 1",
		"dbp_batches_processed_total{db=\"toko\"} 1",
		"dbp_rows_processed_total{db=\"toko\"} 1000",
		"dbp_pool_open_connections{db_id=\"1\"} 5",
		"dbp_pool_in_use_connections{db_id=\"1\"} 2",
		"dbp_pool_idle_connections{db_id=\"1\"} 3",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrik tidak memuat %q\nbody: %s", expected, body)
		}
	}
}
