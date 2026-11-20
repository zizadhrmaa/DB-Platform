package obs

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Handler adalah pembungkus slog.Handler yang menyuntikkan trace context OpenTelemetry
// (trace_id dan span_id), metadata context (request_id, actor, database, job metadata),
// dan secara otomatis meredaksi atribut yang sensitif.
type Handler struct {
	inner slog.Handler
}

var _ slog.Handler = (*Handler)(nil)

// NewJSONHandler membuat slog.Handler berformat JSON dengan tracing dan redaksi otomatis.
func NewJSONHandler(w io.Writer, opts *slog.HandlerOptions) *Handler {
	return &Handler{
		inner: slog.NewJSONHandler(w, opts),
	}
}

// Enabled memeriksa apakah level log diizinkan oleh handler utama.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// WithAttrs mengembalikan handler baru dengan atribut tambahan setelah diredaksi.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = h.redactAttr(a)
	}
	return &Handler{inner: h.inner.WithAttrs(redacted)}
}

// WithGroup mengembalikan handler baru dengan nama group.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

// Handle memproses record log: menyuntikkan trace_id/span_id, context attributes,
// dan meredaksi atribut sensitif sebelum didelegasikan ke JSON handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Ambil OpenTelemetry span jika valid.
	if ctx != nil {
		span := trace.SpanFromContext(ctx)
		if sc := span.SpanContext(); sc.IsValid() {
			r.Add("trace_id", sc.TraceID().String())
			r.Add("span_id", sc.SpanID().String())
		}

		// Atribut wajib dan context metadata.
		if reqID := RequestIDFromContext(ctx); reqID != "" {
			r.Add("request_id", reqID)
		}
		if actor := ActorFromContext(ctx); actor != "" {
			r.Add("actor", actor)
		}
		if db := DatabaseFromContext(ctx); db != "" {
			r.Add("database", db)
		}
		if jc, ok := JobContextFrom(ctx); ok {
			if jc.JobID != 0 {
				r.Add("job_id", jc.JobID)
			}
			if jc.ExecutionID != 0 {
				r.Add("execution_id", jc.ExecutionID)
			}
			if jc.MigrationID != 0 {
				r.Add("migration_id", jc.MigrationID)
			}
			if jc.Attempt != 0 {
				r.Add("attempt", jc.Attempt)
			}
			if jc.Batch != 0 {
				r.Add("batch", jc.Batch)
			}
		}
	}

	// Redaksi atribut pada record.
	var newRecord slog.Record
	newRecord = slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		newRecord.AddAttrs(h.redactAttr(a))
		return true
	})

	return h.inner.Handle(ctx, newRecord)
}

func (h *Handler) redactAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedPlaceholder)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(a.Value.String()))
	case slog.KindGroup:
		attrs := a.Value.Group()
		redacted := make([]slog.Attr, len(attrs))
		for i, sub := range attrs {
			redacted[i] = h.redactAttr(sub)
		}
		return slog.Group(a.Key, anySliceToAny(redacted)...)
	case slog.KindAny:
		val := RedactValue(a.Key, a.Value.Any())
		return slog.Any(a.Key, val)
	default:
		return a
	}
}

func anySliceToAny(attrs []slog.Attr) []any {
	res := make([]any, len(attrs))
	for i, a := range attrs {
		res[i] = a
	}
	return res
}
