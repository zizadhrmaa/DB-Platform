package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/zizadhrmaa/DB-Platform/internal/approval"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

type workerDeps struct {
	DB         *sql.DB
	Queue      *queue.Store
	Migrations *migration.Store
	Approvals  *approval.Store
}

// newWorker merakit worker antrean migration. Sebelum SQL dijalankan,
// worker memeriksa ulang request approval job tersebut.
func newWorker(d workerDeps, cfg worker.Config) *worker.Worker {
	return &worker.Worker{
		Queue:      d.Queue,
		Users:      d.DB,
		Exec:       d.Migrations,
		Verify:     d.Migrations,
		Revalidate: approvalRevalidator(d.Approvals),
		Config:     cfg,
	}
}

// approvalRevalidator memastikan job berasal dari request yang approval-nya
// masih lengkap, SQL-nya belum berubah, dan pengajunya masih berhak.
func approvalRevalidator(a *approval.Store) worker.Revalidator {
	return func(ctx context.Context, j *queue.Job, u *domain.User) error {
		if j.RequestID == nil {
			return errors.New("job tidak berasal dari request perubahan yang disetujui")
		}
		// Melanjutkan atau membatalkan batch tidak memerlukan approval ulang
		// (keputusan tim), tetapi pengaju tetap harus aktif dan berhak.
		resume := j.Mode == queue.ModeBatch && (j.Action == queue.ActionRevert || j.BatchesCommitted > 0 || j.Attempt > 1)
		return a.Revalidate(ctx, *j.RequestID, u.ID, resume)
	}
}
