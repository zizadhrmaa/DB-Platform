package dbmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

func TestLoggedAttrsAndRedaction(t *testing.T) {
	var buf bytes.Buffer
	handler := obs.NewJSONHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	ldb := NewLoggedDB(nil, 42, "kampus")

	ctx := context.Background()
	ctx = obs.WithRequestID(ctx, "req-sql-001")
	ctx = obs.WithActor(ctx, "reysha")

	attrs := ldb.baseAttrs(ctx, "sql_start", "SELECT * FROM users WHERE password = ?")
	attrs = append(attrs, "params", obs.RedactArgs([]any{"secret_123"}))

	slog.InfoContext(ctx, "sql_start", attrs...)

	out := buf.String()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output bukan JSON: %s, err: %v", out, err)
	}

	if m["database"] != "kampus" {
		t.Errorf("database = %v; want kampus", m["database"])
	}
	if m["event"] != "sql_start" {
		t.Errorf("event = %v; want sql_start", m["event"])
	}
	if m["actor"] != "reysha" {
		t.Errorf("actor = %v; want reysha", m["actor"])
	}
	if m["request_id"] != "req-sql-001" {
		t.Errorf("request_id = %v; want req-sql-001", m["request_id"])
	}
}

func TestExtractSQLState(t *testing.T) {
	if s := extractSQLState(nil); s != "" {
		t.Errorf("extractSQLState(nil) = %q; want empty", s)
	}
}
