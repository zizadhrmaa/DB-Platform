package worker_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

// fakeExec mengembalikan hasil yang sudah ditentukan tanpa menyentuh target.
type fakeExec struct {
	run    *migration.Run
	err    error
	called int
}

func (f *fakeExec) ExecuteWith(context.Context, *domain.User, int64, int64, migration.ExecOptions) (*migration.Run, error) {
	f.called++
	return f.run, f.err
}

func TestWorkerOutcomeClassification(t *testing.T) {
	cases := []struct {
		name    string
		run     *migration.Run
		err     error
		want    queue.Status
		holdsDB bool
	}{
		{"sukses", &migration.Run{Status: migration.StatusSuccess}, nil, queue.StatusSucceeded, false},
		{"sql gagal", &migration.Run{Status: migration.StatusFailed, ErrorMessage: "syntax error"}, nil, queue.StatusFailed, false},
		{"target sibuk", nil, migration.ErrTargetBusy, queue.StatusQueued, false},
		{"target tidak terjangkau", nil, dbmanager.ErrConnUnavailable, queue.StatusQueued, false},
		{"terputus sebelum commit", &migration.Run{Status: migration.StatusFailed}, migration.ErrInterrupted, queue.StatusQueued, false},
		{"hasil belum pasti", nil, migration.ErrUncertain, queue.StatusUncertain, true},
		{"urutan salah", nil, fmt.Errorf("%w: jalankan dulu versi 1", domain.ErrConflict), queue.StatusFailed, false},
		{"akses dicabut", nil, domain.ErrForbidden, queue.StatusFailed, false},
		{"error sistem tak dikenal", nil, errors.New("platform db sibuk"), queue.StatusQueued, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			j := e.submit(t, e.dbA, "SELECT 1")
			f := &fakeExec{run: c.run, err: c.err}
			w := e.worker(worker.Config{})
			w.Exec = f
			processNext(t, w)

			got := e.job(t, j.ID)
			if got.Status != c.want {
				t.Fatalf("status = %s, seharusnya %s (%+v)", got.Status, c.want, got)
			}
			if f.called != 1 {
				t.Fatalf("executor dipanggil %d kali", f.called)
			}
			if c.want != queue.StatusSucceeded && got.ErrorMessage == "" {
				t.Fatal("alasan kegagalan harus dicatat")
			}
			if got.Status.HoldsDatabase() != c.holdsDB {
				t.Fatalf("menahan database = %v, seharusnya %v", got.Status.HoldsDatabase(), c.holdsDB)
			}
		})
	}
}

// Pesan error sistem yang tidak dikenal tidak boleh disimpan apa adanya,
// karena bisa memuat host atau kredensial.
func TestWorkerDoesNotStoreRawSystemError(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "SELECT 1")
	w := e.worker(worker.Config{})
	w.Exec = &fakeExec{err: errors.New("dial tcp 10.0.0.5:5432: password=rahasia")}
	processNext(t, w)
	if got := e.job(t, j.ID); got.ErrorMessage == "" || containsAny(got.ErrorMessage, "10.0.0.5", "rahasia") {
		t.Fatalf("pesan error tersimpan tidak aman: %q", got.ErrorMessage)
	}
}

// Kalau lease hilang (pemulihan sudah mengambil alih), worker tidak boleh
// menulis hasil apa pun.
func TestWorkerLeaseLostWritesNothing(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "SELECT 1")
	w := e.worker(worker.Config{})
	w.Exec = &fakeExec{err: fmt.Errorf("catat attempt: %w", queue.ErrLeaseLost)}
	processNext(t, w)
	got := e.job(t, j.ID)
	if got.Status != queue.StatusRunning || got.LeaseOwner != "w-test" || got.ErrorMessage != "" {
		t.Fatalf("job tidak boleh diubah worker yang kehilangan lease: %+v", got)
	}
}

// Jenis atau mode yang belum didukung ditolak tanpa menjalankan SQL.
func TestWorkerRejectsUnsupportedJobs(t *testing.T) {
	for _, p := range []struct {
		kind queue.Kind
		mode queue.Mode
	}{{queue.KindRollback, queue.ModeNormal}, {queue.KindUp, queue.ModeBatch}} {
		t.Run(string(p.kind)+"-"+string(p.mode), func(t *testing.T) {
			e := setup(t)
			m, err := e.migs.Create(context.Background(), e.user, e.dbA, "m", "SELECT 1")
			if err != nil {
				t.Fatal(err)
			}
			j, _, err := e.queue.Enqueue(context.Background(), queue.EnqueueParams{
				DBID: e.dbA, MigrationID: m.ID, RequestedBy: e.user.ID, IdempotencyKey: "x", Kind: p.kind, Mode: p.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			f := &fakeExec{run: &migration.Run{Status: migration.StatusSuccess}}
			w := e.worker(worker.Config{})
			w.Exec = f
			processNext(t, w)
			if got := e.job(t, j.ID); got.Status != queue.StatusFailed || got.ErrorMessage == "" {
				t.Fatalf("job yang belum didukung harus failed dengan alasan: %+v", got)
			}
			if f.called != 0 {
				t.Fatal("SQL tidak boleh dijalankan")
			}
		})
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
