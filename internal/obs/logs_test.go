package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// Handler tambahan (jalur OTLP) menerima record yang sama dengan stdout:
// sudah berisi trace_id dan atribut context, dan sudah diredaksi.
func TestJSONHandlerFanoutSendsRedactedRecordToExtraHandler(t *testing.T) {
	var stdout, extra bytes.Buffer
	logger := slog.New(NewJSONHandler(&stdout, nil, slog.NewJSONHandler(&extra, nil)))
	if _, _, err := InitTracer(context.Background(), "test-service"); err != nil {
		t.Fatal(err)
	}
	ctx, span := StartSpan(WithActor(context.Background(), "alice"), "uji")
	logger.With("job_id", 7).InfoContext(ctx, "sql_start", "password", "rahasia", "dsn", "postgres://u:pw123@h/db")
	span.End()

	for name, buf := range map[string]*bytes.Buffer{"stdout": &stdout, "otlp": &extra} {
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("%s: bukan JSON: %q", name, buf.String())
		}
		if m["trace_id"] != span.SpanContext().TraceID().String() || m["actor"] != "alice" || m["job_id"] != float64(7) {
			t.Fatalf("%s: atribut trace/context hilang: %v", name, m)
		}
		if m["password"] != RedactedPlaceholder || strings.Contains(buf.String(), "pw123") || strings.Contains(buf.String(), "rahasia") {
			t.Fatalf("%s: data sensitif tidak diredaksi: %s", name, buf.String())
		}
	}
}

// Tanpa OTEL_LOGS_EXPORTER=otlp tidak ada handler tambahan.
func TestInitLogsDisabledByDefault(t *testing.T) {
	t.Setenv("OTEL_LOGS_EXPORTER", "")
	h, shutdown, err := InitLogs(context.Background(), "x")
	if h != nil || shutdown != nil || err != nil {
		t.Fatalf("InitLogs harus nonaktif: h=%v err=%v", h, err)
	}
}

type statusUji string

// Nilai bertipe string bernama (misalnya queue.Status) dikirim sebagai
// string biasa, bukan "unhandled: (...)" di jalur OTLP.
func TestNamedStringTypeBecomesPlainString(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewJSONHandler(&buf, nil)).Info("x", "status", statusUji("succeeded"))
	a := basicAttr("status", statusUji("succeeded"))
	if a.Value.Kind() != slog.KindString || a.Value.String() != "succeeded" {
		t.Fatalf("harus string biasa: %v (%v)", a.Value, a.Value.Kind())
	}
	if !strings.Contains(buf.String(), `"status":"succeeded"`) {
		t.Fatalf("output: %s", buf.String())
	}
}
