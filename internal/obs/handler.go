package obs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"

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
// Handler tambahan (misalnya pengirim log OTLP ke OpenTelemetry Collector)
// menerima record yang sama setelah diperkaya dan diredaksi.
func NewJSONHandler(w io.Writer, opts *slog.HandlerOptions, extra ...slog.Handler) *Handler {
	var inner slog.Handler = slog.NewJSONHandler(w, opts)
	if len(extra) > 0 {
		inner = fanout(append([]slog.Handler{inner}, extra...))
	}
	return &Handler{inner: inner}
}

// fanout meneruskan setiap record ke beberapa handler.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
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
		return basicAttr(a.Key, val)
	default:
		return a
	}
}

// basicAttr mengubah nilai bertipe bernama dengan tipe dasar string atau
// angka (misalnya queue.Status) menjadi tipe dasarnya. Bridge otelslog
// tidak mengenal tipe custom dan akan menuliskannya sebagai "unhandled".
func basicAttr(key string, v any) slog.Attr {
	if v == nil {
		return slog.Any(key, v)
	}
	if _, ok := v.(error); ok {
		return slog.Any(key, v)
	}
	if _, ok := v.(fmt.Stringer); ok {
		return slog.Any(key, v)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return slog.String(key, RedactString(rv.String()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return slog.Int64(key, rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return slog.Uint64(key, rv.Uint())
	case reflect.Bool:
		return slog.Bool(key, rv.Bool())
	case reflect.Float32, reflect.Float64:
		return slog.Float64(key, rv.Float())
	}
	return slog.Any(key, v)
}

func anySliceToAny(attrs []slog.Attr) []any {
	res := make([]any, len(attrs))
	for i, a := range attrs {
		res[i] = a
	}
	return res
}
