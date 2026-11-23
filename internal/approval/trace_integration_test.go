package approval

import (
	"context"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Enqueue membuat span queue.enqueue (anak span request API) dan
// traceparent yang disimpan di job menunjuk ke span itu, sehingga span
// job.process di worker menjadi anak queue.enqueue: API -> queue -> worker.
func TestEnqueueSpanCarriesTraceContextToJob(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	f := newApprovedFixture(t)
	ctx, api := otel.Tracer("test").Start(context.Background(), "POST /api/.../enqueue")
	job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	api.End()
	if err != nil {
		t.Fatal(err)
	}

	var enqueue sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "queue.enqueue" && s.SpanContext().TraceID() == api.SpanContext().TraceID() {
			enqueue = s
		}
	}
	if enqueue == nil || enqueue.Parent().SpanID() != api.SpanContext().SpanID() {
		t.Fatalf("span queue.enqueue harus anak span request API: %+v", enqueue)
	}
	stored, err := f.qs.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("00-%s-%s-01", enqueue.SpanContext().TraceID(), enqueue.SpanContext().SpanID())
	if stored.TraceContext != want {
		t.Fatalf("traceparent job = %q, want %q (menunjuk span queue.enqueue)", stored.TraceContext, want)
	}
}
