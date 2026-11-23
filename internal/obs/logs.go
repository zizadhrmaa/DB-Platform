package obs

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// InitLogs menyiapkan pengiriman log ke OpenTelemetry Collector lewat
// OTLP/HTTP jika OTEL_LOGS_EXPORTER=otlp (alamat dari
// OTEL_EXPORTER_OTLP_ENDPOINT, default http://localhost:4318). Selain itu
// mengembalikan handler nil: log hanya ditulis ke stdout seperti biasa.
//
// Handler yang dikembalikan dipasang sebagai handler tambahan pada
// NewJSONHandler, sehingga record yang dikirim sudah berisi trace_id,
// atribut context, dan sudah diredaksi. Bridge otelslog juga mengisi
// TraceID/SpanID record OTLP dari context, jadi backend log (Loki) bisa
// menautkan log ke trace di Jaeger.
func InitLogs(ctx context.Context, serviceName string) (slog.Handler, func(context.Context) error, error) {
	if os.Getenv("OTEL_LOGS_EXPORTER") != "otlp" {
		return nil, nil, nil
	}
	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(serviceResource(serviceName)),
	)
	return otelslog.NewHandler(TracerName, otelslog.WithLoggerProvider(lp)), lp.Shutdown, nil
}
