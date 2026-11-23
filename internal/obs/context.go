package obs

import "context"

type contextKey string

const (
	requestIDKey contextKey = "obs_request_id"
	actorKey     contextKey = "obs_actor"
	databaseKey  contextKey = "obs_database"
	jobIDKey     contextKey = "obs_job_id"
	execIDKey    contextKey = "obs_execution_id"
	migIDKey     contextKey = "obs_migration_id"
	attemptKey   contextKey = "obs_attempt"
	batchKey     contextKey = "obs_batch"
)

// WithRequestID menyimpan ID korelasi / request ID ke dalam context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext mengambil ID korelasi dari context jika ada.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithActor menyimpan username atau actor ke dalam context.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey, actor)
}

// ActorFromContext mengambil actor dari context jika ada.
func ActorFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(actorKey).(string); ok {
		return v
	}
	return ""
}

// WithDatabase menyimpan ID atau nama database ke dalam context.
func WithDatabase(ctx context.Context, db string) context.Context {
	return context.WithValue(ctx, databaseKey, db)
}

// DatabaseFromContext mengambil database dari context jika ada.
func DatabaseFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(databaseKey).(string); ok {
		return v
	}
	return ""
}

// JobContext menyimpan metadata job/migration untuk log SQL dan span.
type JobContext struct {
	JobID       int64
	ExecutionID int64
	MigrationID int64
	Attempt     int
	Batch       int
}

// WithJobContext menyimpan metadata job/migration ke context.
func WithJobContext(ctx context.Context, jc JobContext) context.Context {
	if jc.JobID != 0 {
		ctx = context.WithValue(ctx, jobIDKey, jc.JobID)
	}
	if jc.ExecutionID != 0 {
		ctx = context.WithValue(ctx, execIDKey, jc.ExecutionID)
	}
	if jc.MigrationID != 0 {
		ctx = context.WithValue(ctx, migIDKey, jc.MigrationID)
	}
	if jc.Attempt != 0 {
		ctx = context.WithValue(ctx, attemptKey, jc.Attempt)
	}
	if jc.Batch != 0 {
		ctx = context.WithValue(ctx, batchKey, jc.Batch)
	}
	return ctx
}

// JobContextFrom mengambil metadata job dari context.
func JobContextFrom(ctx context.Context) (jc JobContext, ok bool) {
	if ctx == nil {
		return jc, false
	}
	if v, valid := ctx.Value(jobIDKey).(int64); valid {
		jc.JobID = v
		ok = true
	}
	if v, valid := ctx.Value(execIDKey).(int64); valid {
		jc.ExecutionID = v
		ok = true
	}
	if v, valid := ctx.Value(migIDKey).(int64); valid {
		jc.MigrationID = v
		ok = true
	}
	if v, valid := ctx.Value(attemptKey).(int); valid {
		jc.Attempt = v
		ok = true
	}
	if v, valid := ctx.Value(batchKey).(int); valid {
		jc.Batch = v
		ok = true
	}
	return jc, ok
}
