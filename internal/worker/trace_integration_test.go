package worker_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

// Trace dari request API (disimpan di jobs.trace_context saat enqueue)
// dilanjutkan worker, sehingga API → queue → worker berada di satu trace.
func TestWorkerContinuesTraceFromQueue(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceparent := "00-" + traceID + "-00f067aa0ba902b7-01"

	e := setup(t)
	m, err := e.migs.Create(context.Background(), e.user, e.dbA, "m", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.queue.Enqueue(context.Background(), queue.EnqueueParams{
		DBID: e.dbA, MigrationID: m.ID, RequestedBy: e.user.ID, IdempotencyKey: "trace", TraceContext: traceparent,
	}); err != nil {
		t.Fatal(err)
	}

	var got trace.SpanContext
	w := e.worker(worker.Config{})
	w.Revalidate = func(ctx context.Context, _ *queue.Job, _ *domain.User) error {
		got = trace.SpanContextFromContext(ctx)
		return nil
	}
	processNext(t, w)

	if got.TraceID().String() != traceID {
		t.Fatalf("worker harus melanjutkan trace %s, dapat %q", traceID, got.TraceID())
	}
}
