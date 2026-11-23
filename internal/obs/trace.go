package obs

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const TracerName = "github.com/zizadhrmaa/DB-Platform"

// Tracer mengembalikan global OpenTelemetry Tracer untuk DB-Platform.
func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

// serviceResource memberi nama service pada trace dan log yang dikirim.
func serviceResource(serviceName string) *resource.Resource {
	return resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName))
}

// InitTracer menginisialisasi TracerProvider dan Propagator standar W3C TraceContext.
// Exporter dipilih lewat OTEL_TRACES_EXPORTER:
//   - otlp    : OTLP/HTTP ke OpenTelemetry Collector (alamat dari
//     OTEL_EXPORTER_OTLP_ENDPOINT, default http://localhost:4318)
//   - none    : span tidak diekspor
//   - lainnya : console (stdout), default
func InitTracer(ctx context.Context, serviceName string) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	var exporter sdktrace.SpanExporter
	var err error

	switch os.Getenv("OTEL_TRACES_EXPORTER") {
	case "none":
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(io.Discard))
	case "otlp":
		exporter, err = otlptracehttp.New(ctx)
	default:
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
		sdktrace.WithResource(serviceResource(serviceName)),
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
