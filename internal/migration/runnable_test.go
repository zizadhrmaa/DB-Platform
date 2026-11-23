package migration

import (
	"errors"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func mig(id int64, version int, st Status) Migration {
	return Migration{ID: id, Version: version, Status: st}
}

func TestCheckRunnable(t *testing.T) {
	irreversible := mig(2, 2, StatusSuccess)
	irreversible.Irreversible = true
	irreversible.IrreversibleReason = "data dihapus"

	cases := []struct {
		name    string
		list    []Migration
		id      int64
		kind    Kind
		wantErr string // kosong = boleh
	}{
		{"up versi berikutnya", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusPending)}, 2, KindUp, ""},
		{"up melompat", []Migration{mig(1, 1, StatusPending), mig(2, 2, StatusPending)}, 2, KindUp, "versi 1"},
		{"rollback aktif terakhir", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusSuccess), mig(3, 3, StatusPending)}, 2, KindRollback, ""},
		{"rollback bukan aktif terakhir", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusSuccess)}, 1, KindRollback, "aktif terakhir"},
		{"rollback setelah yang lebih baru di-rollback", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusRolledBack)}, 1, KindRollback, ""},
		{"rollback migration irreversible", []Migration{mig(1, 1, StatusSuccess), irreversible}, 2, KindRollback, "data dihapus"},
		{"rollback migration pending", []Migration{mig(1, 1, StatusPending)}, 1, KindRollback, "berhasil"},
		{"forward yang di-rollback", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusRolledBack), mig(3, 3, StatusPending)}, 2, KindForward, ""},
		{"forward tidak urut", []Migration{mig(1, 1, StatusRolledBack), mig(2, 2, StatusRolledBack)}, 2, KindForward, "versi 1"},
		{"forward migration sukses", []Migration{mig(1, 1, StatusSuccess)}, 1, KindForward, "di-rollback"},
		{"up tertahan migration rolled_back", []Migration{mig(1, 1, StatusRolledBack), mig(2, 2, StatusPending)}, 2, KindUp, "versi 1"},
		{"up pada migration rolled_back", []Migration{mig(1, 1, StatusRolledBack)}, 1, KindUp, "forward"},
		{"ada yang running", []Migration{mig(1, 1, StatusRunning), mig(2, 2, StatusSuccess)}, 2, KindRollback, "sedang berjalan"},
		{"discarded dilewati", []Migration{mig(1, 1, StatusSuccess), mig(2, 2, StatusDiscarded), mig(3, 3, StatusPending)}, 3, KindUp, ""},
		{"migration tidak ada", []Migration{mig(1, 1, StatusPending)}, 9, KindUp, "tidak ada"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckRunnable(c.list, c.id, c.kind)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("seharusnya boleh, dapat %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("seharusnya ditolak dengan pesan berisi %q, dapat %v", c.wantErr, err)
			}
			if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error harus ErrConflict atau ErrNotFound: %v", err)
			}
		})
	}
	if err := CheckRunnable([]Migration{mig(1, 1, StatusPending)}, 1, "aneh"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("jenis tidak dikenal harus ErrInvalidInput, dapat %v", err)
	}
}

func TestCheckRequestable(t *testing.T) {
	irr := mig(1, 1, StatusSuccess)
	irr.Irreversible, irr.IrreversibleReason = true, "hapus data"
	cases := []struct {
		m    Migration
		kind Kind
		ok   bool
	}{
		{mig(1, 1, StatusPending), KindUp, true},
		{mig(1, 1, StatusFailed), KindUp, true},
		{mig(1, 1, StatusSuccess), KindUp, false},
		{mig(1, 1, StatusSuccess), KindRollback, true},
		{irr, KindRollback, false},
		{mig(1, 1, StatusPending), KindRollback, false},
		{mig(1, 1, StatusRolledBack), KindForward, true},
		{mig(1, 1, StatusSuccess), KindForward, false},
	}
	for _, c := range cases {
		if err := CheckRequestable(c.m, c.kind); (err == nil) != c.ok {
			t.Errorf("%s %s: err=%v, seharusnya boleh=%v", c.kind, c.m.Status, err, c.ok)
		}
	}
}

func TestLatestApplied(t *testing.T) {
	runs := []Run{
		{ID: 5, Kind: KindRollback, Status: StatusSuccess},
		{ID: 4, Kind: KindForward, Status: StatusFailed},
		{ID: 3, Kind: KindForward, Status: StatusSuccess},
		{ID: 1, Kind: KindUp, Status: StatusSuccess},
	}
	if r, ok := LatestApplied(runs); !ok || r.ID != 3 {
		t.Fatalf("eksekusi aktif terakhir harus forward #3, dapat %+v ok=%v", r, ok)
	}
	if _, ok := LatestApplied([]Run{{ID: 1, Kind: KindUp, Status: StatusFailed}}); ok {
		t.Fatal("tidak ada eksekusi sukses")
	}
}

func TestAttemptOutcomeStatus(t *testing.T) {
	cases := []struct {
		kind   Kind
		result Status
		want   Status
	}{
		{KindUp, StatusSuccess, StatusSuccess},
		{KindUp, StatusFailed, StatusFailed},
		{KindForward, StatusSuccess, StatusSuccess},
		{KindForward, StatusFailed, StatusRolledBack}, // forward gagal: tetap di-rollback
		{KindRollback, StatusSuccess, StatusRolledBack},
		{KindRollback, StatusFailed, StatusSuccess}, // rollback gagal tidak mengubah posisi aktif
	}
	for _, c := range cases {
		if got := migrationStatusAfter(c.kind, c.result); got != c.want {
			t.Errorf("%s %s: status migration %s, seharusnya %s", c.kind, c.result, got, c.want)
		}
	}
}

func TestDraftValidation(t *testing.T) {
	ok := []Draft{
		{Title: "a", SQLUp: "CREATE TABLE a (id INT)", SQLDown: "DROP TABLE a"},
		{Title: "b", SQLUp: "DELETE FROM a", Irreversible: true, IrreversibleReason: "data yang dihapus tidak bisa dipulihkan"},
	}
	for _, d := range ok {
		if err := d.validate(); err != nil {
			t.Errorf("%+v seharusnya valid: %v", d, err)
		}
	}
	bad := []Draft{
		{Title: "", SQLUp: "SELECT 1", SQLDown: "SELECT 1"},
		{Title: "x", SQLUp: "SELECT 1"},                                                                   // reversible tanpa down
		{Title: "x", SQLUp: "SELECT 1", Irreversible: true},                                               // irreversible tanpa alasan
		{Title: "x", SQLUp: "SELECT 1", SQLDown: "SELECT 1", Irreversible: true, IrreversibleReason: "r"}, // irreversible tapi ada down
		{Title: "x", SQLUp: "SELECT 1", SQLDown: "COMMIT"},                                                // down tidak lolos validasi
	}
	for _, d := range bad {
		if err := d.validate(); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%+v seharusnya ditolak ErrInvalidInput, dapat %v", d, err)
		}
	}
}
