package obs

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const TracerName = "github.com/zizadhrmaa/DB-Platform"

// Tracer mengembalikan global OpenTelemetry Tracer untuk DB-Platform.
func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

// InitTracer menginisialisasi TracerProvider dan Propagator standar W3C TraceContext.
// Default exporter adalah console (stdout), atau discard jika OTEL_TRACES_EXPORTER=none.
func InitTracer(ctx context.Context, serviceName string) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	var exporter sdktrace.SpanExporter
	var err error

	exporterType := os.Getenv("OTEL_TRACES_EXPORTER")
	if exporterType == "none" {
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(io.Discard))
	} else {
		// Default: stdout trace exporter
		exporter, err = stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
			stdouttrace.WithoutTimestamps(),
		)
	}
	if err != nil {
		return nil, nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}

	return tp, shutdown, nil
}

// StartSpan memulai child span baru dari context.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// RecordError mencatat error ke dalam span dan mengubah status span menjadi Error.
func RecordError(span trace.Span, err error) {
	if err != nil && span != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// InjectTraceParent mengekspor trace context dari context menjadi string W3C traceparent
// (contoh: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
// yang disimpan di jobs.trace_context saat enqueue.
func InjectTraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ExtractTraceParent mengekstrak trace context dari string W3C traceparent ke dalam context baru,
// dipakai oleh worker saat memproses job.
func ExtractTraceParent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
