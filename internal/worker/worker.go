// Package worker menjalankan job migration dari antrean secara asynchronous.
//
// Setiap goroutine worker mengambil job dengan queue.Store.Claim, memegang
// lease job lewat heartbeat, memeriksa ulang pengaju dan approval
// (revalidasi), menjalankan migration lewat migration.ExecuteWith, lalu
// menulis hasilnya dengan compare-and-set terhadap lease. Satu database
// hanya menjalankan satu job karena index unik di tabel jobs dan lock
// advisory di database target.
package worker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
)

// Executor dipenuhi *migration.Store.
type Executor interface {
	ExecuteWith(ctx context.Context, u *domain.User, dbID, id int64, opt migration.ExecOptions) (*migration.Run, error)
}

// Revalidator memeriksa ulang syarat job sebelum SQL dijalankan, misalnya
// approval yang dicabut setelah job masuk antrean. Error apa pun membuat
// job failed tanpa menjalankan SQL; pesannya disimpan di job, jadi jangan
// memuat data sensitif.
type Revalidator func(ctx context.Context, job *queue.Job, u *domain.User) error

type Config struct {
	ID           string        // identitas worker untuk lease; kosong = host-pid-acak
	Concurrency  int           // jumlah job yang boleh berjalan bersamaan; 0 = 2
	Lease        time.Duration // lama lease, heartbeat tiap Lease/3; 0 = 30 detik
	PollInterval time.Duration // jeda saat antrean kosong; 0 = 1 detik
	JobTimeout   time.Duration // batas waktu eksekusi satu job; 0 = 30 menit
	// RecoveryInterval adalah jeda loop pemulihan job milik worker yang mati
	// dan job uncertain; 0 = sama dengan Lease.
	RecoveryInterval time.Duration
}

// Verifier dipenuhi *migration.Store.
type Verifier interface {
	VerifyRun(ctx context.Context, dbID, migrationID, runID int64) (migration.Verdict, string, error)
}

type Worker struct {
	Queue      *queue.Store
	Users      *sql.DB // database platform, untuk memuat ulang pengaju
	Exec       Executor
	Revalidate Revalidator // nil = hanya memeriksa pengaju dan akses
	Verify     Verifier    // nil = Exec kalau Exec juga Verifier
	Config
	Log *slog.Logger

	once sync.Once
	cfg  Config
}

func (w *Worker) config() Config {
	w.once.Do(func() {
		c := w.Config
		if c.ID == "" {
			c.ID = defaultID()
		}
		if c.Concurrency <= 0 {
			c.Concurrency = 2
		}
		if c.Lease <= 0 {
			c.Lease = 30 * time.Second
		}
		if c.PollInterval <= 0 {
			c.PollInterval = time.Second
		}
		if c.JobTimeout <= 0 {
			c.JobTimeout = 30 * time.Minute
		}
		if c.RecoveryInterval <= 0 {
			c.RecoveryInterval = c.Lease
		}
		w.cfg = c
	})
	return w.cfg
}

func (w *Worker) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func defaultID() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b))
}

// Run menjalankan Concurrency goroutine pengambil job dan satu loop
// pemulihan sampai ctx dibatalkan. Setelah dibatalkan, worker berhenti
// mengambil job baru, tetapi job yang sedang berjalan diselesaikan dulu
// (eksekusinya tidak ikut dibatalkan). Run baru kembali setelah semua job
// itu selesai.
func (w *Worker) Run(ctx context.Context) {
	cfg := w.config()
	w.logger().Info("worker mulai", "event", "worker.start", "worker_id", cfg.ID, "concurrency", cfg.Concurrency)
	var wg sync.WaitGroup
	for range cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, cfg)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.recoveryLoop(ctx, cfg)
	}()
	wg.Wait()
	w.logger().Info("worker berhenti", "event", "worker.stop", "worker_id", cfg.ID)
}

func (w *Worker) loop(ctx context.Context, cfg Config) {
	for ctx.Err() == nil {
		ok, err := w.ProcessNext(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger().Warn("gagal mengambil job", "event", "job.claim_error", "worker_id", cfg.ID, "err", errType(err))
		}
		if ok {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.PollInterval):
		}
	}
}

// ProcessNext mengambil satu job lalu memprosesnya sampai selesai. Hasil
// false berarti tidak ada job yang bisa diambil. Pemrosesan tidak ikut
// dibatalkan walaupun ctx dibatalkan di tengah jalan.
func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	cfg := w.config()
	j, err := w.Queue.Claim(ctx, cfg.ID, cfg.Lease)
	if err != nil || j == nil {
		return false, err
	}
	w.process(context.WithoutCancel(ctx), cfg, j)
	return true, nil
}

type action int

const (
	actFinish action = iota // tulis status akhir attempt
	actRetry                // kembalikan ke antrean dengan jeda
	actLost                 // lease hilang, jangan tulis apa pun
)

type outcome struct {
	act    action
	status queue.Status
	msg    string
}

func finish(st queue.Status, msg string) outcome {
	return outcome{act: actFinish, status: st, msg: msg}
}
func fail(msg string) outcome  { return finish(queue.StatusFailed, msg) }
func retry(msg string) outcome { return outcome{act: actRetry, msg: msg} }

func (w *Worker) process(ctx context.Context, cfg Config, j *queue.Job) {
	// Lanjutkan trace dari request API yang memasukkan job ini ke antrean.
	ctx = obs.ExtractTraceParent(ctx, j.TraceContext)
	ctx, span := obs.StartSpan(ctx, "job.process", trace.WithAttributes(
		attribute.Int64("job.id", j.ID),
		attribute.Int64("db.id", j.DBID),
		attribute.Int64("migration.id", j.MigrationID),
		attribute.Int("job.attempt", j.Attempt),
		attribute.String("job.kind", string(j.Kind)),
	))
	defer span.End()
	ctx = obs.WithJobContext(ctx, obs.JobContext{JobID: j.ID, MigrationID: j.MigrationID, Attempt: j.Attempt})

	log := w.logger().With("worker_id", cfg.ID, "job_id", j.ID, "db_id", j.DBID,
		"migration_id", j.MigrationID, "attempt", j.Attempt, "kind", j.Kind, "mode", j.Mode)
	attrs := []any{"event", "job.start"}
	if j.StartedAt != nil && j.Attempt == 1 {
		wait := j.StartedAt.Sub(j.CreatedAt)
		attrs = append(attrs, "queue_wait_ms", wait.Milliseconds())
		obs.RecordQueueWait(wait)
	}
	log.InfoContext(ctx, "job mulai diproses", attrs...)
	begin := time.Now()

	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go w.heartbeat(hbCtx, cfg, j.ID, log, hbDone)

	out := w.run(ctx, cfg, j)

	stopHeartbeat()
	<-hbDone
	w.record(ctx, cfg, j, out, log, time.Since(begin))

	span.SetAttributes(attribute.String("job.result", out.label()))
	if out.act != actFinish || out.status != queue.StatusSucceeded {
		obs.RecordError(span, errors.New(out.label()+": "+out.msg))
	}
}

func (o outcome) label() string {
	switch o.act {
	case actLost:
		return "lease_lost"
	case actRetry:
		return "retry"
	}
	return string(o.status)
}

// run memeriksa ulang syarat job lalu menjalankan migration.
func (w *Worker) run(ctx context.Context, cfg Config, j *queue.Job) outcome {
	if j.Kind != queue.KindUp {
		return fail("Rollback dan forward belum didukung worker.")
	}
	if j.Mode != queue.ModeNormal {
		return fail("Mode batch belum didukung worker.")
	}
	if j.RequestedBy == nil {
		return fail("Pengaju job sudah tidak ada.")
	}
	u, err := loadUser(ctx, w.Users, *j.RequestedBy)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail("Pengaju job sudah tidak ada.")
	case err != nil:
		return retry("Gagal memuat data pengaju; job dijadwalkan ulang.")
	case !u.IsActive:
		return fail("Pengaju job sudah dinonaktifkan.")
	}
	if w.Revalidate != nil {
		if err := w.Revalidate(ctx, j, u); err != nil {
			if errors.Is(err, queue.ErrLeaseLost) {
				return outcome{act: actLost}
			}
			return fail("Revalidasi sebelum eksekusi gagal: " + err.Error())
		}
	}

	run, err := w.Exec.ExecuteWith(ctx, u, j.DBID, j.MigrationID, migration.ExecOptions{
		Timeout: cfg.JobTimeout,
		OnStart: func(ctx context.Context, tx *sql.Tx, runID int64) error {
			return w.Queue.SetRun(ctx, tx, j.ID, cfg.ID, runID)
		},
	})
	return classify(run, err)
}

// classify memetakan hasil eksekusi ke tindakan pada job.
func classify(run *migration.Run, err error) outcome {
	switch {
	case errors.Is(err, queue.ErrLeaseLost):
		return outcome{act: actLost}
	case err == nil && run != nil && run.Status == migration.StatusSuccess:
		return finish(queue.StatusSucceeded, "")
	case err == nil && run != nil:
		msg := run.ErrorMessage
		if msg == "" {
			msg = "Eksekusi SQL gagal."
		}
		return fail(msg)
	case errors.Is(err, migration.ErrUncertain):
		return finish(queue.StatusUncertain, "Hasil commit belum pasti; database ditahan sampai bukti eksekusi diperiksa.")
	case errors.Is(err, migration.ErrTargetBusy):
		return retry("Database target sedang menjalankan migration lain; job dijadwalkan ulang.")
	case errors.Is(err, domain.ErrUnavailable):
		return retry(err.Error())
	case isDomain(err):
		return fail(err.Error())
	default:
		return retry("Terjadi kesalahan sistem saat menjalankan job; job dijadwalkan ulang.")
	}
}

func isDomain(err error) bool {
	for _, d := range []error{domain.ErrUnauthenticated, domain.ErrForbidden, domain.ErrNotFound, domain.ErrInvalidInput, domain.ErrConflict} {
		if errors.Is(err, d) {
			return true
		}
	}
	return false
}

func (w *Worker) record(ctx context.Context, cfg Config, j *queue.Job, out outcome, log *slog.Logger, took time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	log = log.With("duration_ms", took.Milliseconds())
	// Label metrik memakai ID database (jumlahnya terbatas), bukan job ID.
	dbLabel := strconv.FormatInt(j.DBID, 10)
	switch out.act {
	case actLost:
		log.WarnContext(ctx, "lease job hilang, hasil tidak ditulis", "event", "job.lease_lost")
		return
	case actRetry:
		nj, err := w.Queue.Retry(ctx, j.ID, cfg.ID, out.msg)
		if err != nil {
			log.WarnContext(ctx, "gagal menjadwalkan ulang job", "event", "job.retry_error", "err", errType(err))
			return
		}
		obs.RecordJobRetry(dbLabel)
		if nj.Status == queue.StatusFailed {
			obs.RecordJobStatus(dbLabel, string(nj.Status))
		}
		log.WarnContext(ctx, "job dijadwalkan ulang", "event", "job.retry", "status", nj.Status, "next_run_at", nj.NextRunAt, "reason", out.msg)
	case actFinish:
		if err := w.Queue.Finish(ctx, j.ID, cfg.ID, out.status, out.msg); err != nil {
			log.WarnContext(ctx, "gagal menulis hasil job", "event", "job.finish_error", "status", out.status, "err", errType(err))
			return
		}
		obs.RecordJobStatus(dbLabel, string(out.status))
		obs.RecordMigrationDuration(took)
		level := slog.LevelInfo
		if out.status != queue.StatusSucceeded {
			level = slog.LevelWarn
		}
		log.Log(ctx, level, "job selesai", "event", "job.finish", "status", out.status, "reason", out.msg)
	}
}

func (w *Worker) heartbeat(ctx context.Context, cfg Config, id int64, log *slog.Logger, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(cfg.Lease / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := w.Queue.Heartbeat(ctx, id, cfg.ID, cfg.Lease)
		switch {
		case err == nil:
		case errors.Is(err, queue.ErrLeaseLost):
			log.Warn("lease job hilang saat heartbeat", "event", "job.lease_lost")
			return
		case ctx.Err() != nil:
			return
		default:
			log.Warn("heartbeat gagal", "event", "job.heartbeat_error", "err", errType(err))
		}
	}
}

// loadUser memuat ulang pengaju supaya user yang dinonaktifkan setelah job
// masuk antrean langsung ditolak.
func loadUser(ctx context.Context, db *sql.DB, id int64) (*domain.User, error) {
	u := &domain.User{}
	err := db.QueryRowContext(ctx, `SELECT id, username, is_platform_admin, is_active FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// errType tidak menulis isi error ke log karena error koneksi bisa memuat
// host database. Error domain aman ditulis apa adanya.
func errType(err error) string {
	if isDomain(err) || errors.Is(err, domain.ErrUnavailable) || errors.Is(err, queue.ErrLeaseLost) {
		return err.Error()
	}
	return fmt.Sprintf("%T", err)
}
