package migration

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans memasang TracerProvider dengan SpanRecorder selama test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func spanAttr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// Job batch menghasilkan span lock (lock session) dan satu span
// migration.batch per batch, semuanya di trace yang sama dengan pemanggil
// (job.process di worker). Batch yang gagal mencatat error di span-nya.
func TestBatchTracingLockAndPerBatchSpans(t *testing.T) {
	rec := recordSpans(t)
	env := newExecuteEnv(t)
	env.seedBig(t, 3000)
	m := env.batchDraft(t, upAdd, downSub, 1000)
	if _, err := env.target.Exec(`UPDATE big SET v = 999999 WHERE id = 2500`); err != nil {
		t.Fatal(err)
	}

	ctx, parent := otel.Tracer("test").Start(context.Background(), "job.process")
	r, err := env.store.ExecuteBatch(ctx, env.user, env.dbID, m.ID, BatchOptions{JobID: 41, Action: BatchRun})
	parent.End()
	if err != nil || r.Status != BatchPartial {
		t.Fatalf("persiapan: batch 3 harus gagal (partial): %+v err=%v", r, err)
	}

	traceID := parent.SpanContext().TraceID()
	var lock sdktrace.ReadOnlySpan
	var batches []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanContext().TraceID() != traceID {
			continue
		}
		switch s.Name() {
		case "migration.lock":
			lock = s
		case "migration.batch":
			batches = append(batches, s)
		}
	}
	if lock == nil || lock.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("span migration.lock harus anak job.process: %+v", lock)
	}
	if v, _ := spanAttr(lock, "lock.scope"); v.AsString() != "session" {
		t.Fatalf("span lock mode batch harus lock.scope=session, dapat %q", v.AsString())
	}
	if len(batches) != 3 {
		t.Fatalf("harus ada 3 span migration.batch, dapat %d", len(batches))
	}
	for i, s := range batches {
		no, _ := spanAttr(s, "batch.no")
		rows, _ := spanAttr(s, "batch.rows")
		if no.AsInt64() != int64(i+1) {
			t.Fatalf("urutan span batch: span ke-%d punya batch.no=%d", i, no.AsInt64())
		}
		if i < 2 && (rows.AsInt64() != 1000 || s.Status().Code == codes.Error) {
			t.Fatalf("batch %d sukses harus 1000 row tanpa error: rows=%d status=%v", i+1, rows.AsInt64(), s.Status())
		}
	}
	if last := batches[2]; last.Status().Code != codes.Error || len(last.Events()) == 0 {
		t.Fatalf("span batch yang gagal harus berstatus error dan mencatat exception: %v", last.Status())
	}
}
