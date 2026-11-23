package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
)

// Pemulihan job ketika worker gagal.
//
// Job running yang lease-nya lewat dianggap milik worker yang mati, dan
// job uncertain menunggu kepastian hasil commit. Keduanya diperiksa di
// sini. Lock eksekusi di database target adalah lock advisory level
// transaction; kalau proses worker mati, koneksinya putus dan PostgreSQL
// me-rollback transaction itu sekaligus melepas lock-nya. Jadi pemulihan
// tidak pernah perlu "membuka" lock secara paksa: cukup mencoba mengambil
// lock yang sama. Kalau lock masih dipegang, transaction lama mungkin masih
// berjalan dan bisa saja commit, sehingga hasilnya belum pasti (uncertain)
// dan database tetap ditahan. Kalau lock didapat, bukti di target
// (_dbplatform_history, ditulis dalam transaction yang sama dengan SQL
// migration) menentukan hasilnya.
//
// Setiap keputusan ditulis dengan queue.Store.Resolve yang memeriksa status,
// pemilik lease, attempt, dan run_id yang dilihat di sini. Kalau worker lama
// ternyata masih hidup dan sempat menulis sesuatu, keputusan pemulihan
// tidak berlaku dan job diperiksa lagi di putaran berikutnya.

func (w *Worker) verifier() Verifier {
	if w.Verify != nil {
		return w.Verify
	}
	if v, ok := w.Exec.(Verifier); ok {
		return v
	}
	return nil
}

func (w *Worker) recoveryLoop(ctx context.Context, cfg Config) {
	for {
		if _, err := w.RecoverOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("pemulihan job gagal", "event", "job.recovery_error", "worker_id", cfg.ID, "err", errType(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.RecoveryInterval):
		}
	}
}

// RecoverOnce memeriksa satu putaran job yang perlu dipulihkan dan
// mengembalikan jumlah job yang statusnya diubah.
func (w *Worker) RecoverOnce(ctx context.Context) (int, error) {
	jobs, err := w.Queue.Recoverable(ctx, 50)
	if err != nil {
		return 0, err
	}
	changed := 0
	for i := range jobs {
		if ctx.Err() != nil {
			return changed, ctx.Err()
		}
		ok, err := w.recoverJob(ctx, &jobs[i])
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

func (w *Worker) recoverJob(ctx context.Context, j *queue.Job) (bool, error) {
	cfg := w.config()
	log := w.logger().With("worker_id", cfg.ID, "job_id", j.ID, "db_id", j.DBID,
		"migration_id", j.MigrationID, "attempt", j.Attempt, "from", j.Status)
	exp := queue.Expect{Status: j.Status, LeaseOwner: j.LeaseOwner, Attempt: j.Attempt}

	var (
		to      queue.Status
		msg     string
		verdict = "no_attempt"
	)
	if j.RunID == nil {
		// Worker berhenti sebelum attempt tercatat: SQL belum pernah jalan.
		to, msg = queue.StatusQueued, "Worker berhenti sebelum attempt tercatat; job dijadwalkan ulang."
	} else {
		exp.RunID = *j.RunID
		v := migration.VerdictUnknown
		var detail string
		if vf := w.verifier(); vf != nil {
			var err error
			v, detail, err = vf.VerifyRun(ctx, j.DBID, j.MigrationID, *j.RunID)
			if err != nil {
				log.Warn("bukti eksekusi belum bisa diperiksa", "event", "job.verify_error", "err", errType(err))
				v = migration.VerdictUnknown
			}
		}
		verdict = v.String()
		switch v {
		case migration.VerdictCommitted:
			to, msg = queue.StatusSucceeded, ""
		case migration.VerdictFailed:
			to, msg = queue.StatusFailed, detail
		case migration.VerdictInterrupted:
			to, msg = queue.StatusQueued, "Attempt terputus sebelum commit; perubahan tidak masuk dan job dijadwalkan ulang."
		default:
			to, msg = queue.StatusUncertain, "Hasil attempt belum bisa dipastikan (lock database target masih dipegang atau target tidak terjangkau); database ditahan sampai bukti bisa diperiksa."
		}
	}

	ok, err := w.Queue.Resolve(ctx, j.ID, exp, to, msg)
	if err != nil {
		return false, fmt.Errorf("resolve job %d: %w", j.ID, err)
	}
	if !ok {
		log.Info("keadaan job berubah sebelum pemulihan diterapkan", "event", "job.recovery_skipped", "verdict", verdict)
		return false, nil
	}
	if to == j.Status {
		// Masih uncertain: tidak ada perubahan status.
		return false, nil
	}
	log.Warn("job dipulihkan", "event", "job.recovered", "verdict", verdict, "to", to, "reason", msg)
	return true, nil
}
