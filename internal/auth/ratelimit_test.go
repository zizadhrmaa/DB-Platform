package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLimiter(start time.Time) (*LoginLimiter, *time.Time) {
	now := start
	l := NewLoginLimiter()
	l.pair.now = func() time.Time { return now }
	l.ip.now = func() time.Time { return now }
	return l, &now
}

func TestLoginLimiterPerUsernameAndIP(t *testing.T) {
	l, now := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))

	for i := 0; i < MaxFailsPerUsername; i++ {
		if w := l.Attempt("alice", "10.0.0.1"); w != 0 {
			t.Fatalf("percobaan ke-%d sudah dikunci (%v)", i+1, w)
		}
		*now = now.Add(time.Minute)
	}

	// Huruf besar/kecil dianggap username yang sama.
	w := l.Attempt("ALICE", "10.0.0.1")
	if w <= 0 {
		t.Fatal("username tidak dikunci setelah 5 kali gagal dari IP yang sama")
	}
	// Percobaan pertama di menit 0, jadi terbuka lagi di menit 15.
	if want := 10 * time.Minute; w != want {
		t.Errorf("waktu tunggu %v, want %v", w, want)
	}
	if l.Attempt("bob", "10.0.0.1") != 0 {
		t.Error("username lain ikut terkunci")
	}
	// Pemilik akun yang login dari tempat lain tidak ikut terkunci.
	if l.Attempt("alice", "10.0.0.2") != 0 {
		t.Error("percobaan dari IP lain bisa mengunci akun orang lain")
	}

	*now = now.Add(w)
	if l.Attempt("alice", "10.0.0.1") != 0 {
		t.Error("masih terkunci setelah waktu tunggu habis")
	}
}

func TestLoginLimiterPerIP(t *testing.T) {
	l, _ := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))

	// Tiap username hanya dicoba sekali, tapi dari IP yang sama.
	for i := 0; i < MaxFailsPerIP; i++ {
		l.Attempt(fmt.Sprintf("user%d", i), "10.0.0.1")
	}
	if l.Attempt("username-baru", "10.0.0.1") == 0 {
		t.Fatalf("IP tidak dikunci setelah %d kali gagal", MaxFailsPerIP)
	}
	if l.Attempt("username-baru", "10.0.0.2") != 0 {
		t.Error("IP lain ikut terkunci")
	}
}

func TestLoginLimiterSuccessIsNotCounted(t *testing.T) {
	l, _ := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	for i := 0; i < MaxFailsPerUsername-1; i++ {
		l.Attempt("alice", "10.0.0.1")
	}
	l.Attempt("alice", "10.0.0.1")
	l.Succeeded("alice", "10.0.0.1")
	if l.Attempt("alice", "10.0.0.1") != 0 {
		t.Error("hitungan gagal tidak di-reset setelah login berhasil")
	}

	// Banyak user yang berhasil login dari satu IP (misalnya satu jaringan
	// kampus) tidak boleh mengunci IP itu.
	for i := 0; i < 2*MaxFailsPerIP; i++ {
		u := fmt.Sprintf("mahasiswa%d", i)
		l.Attempt(u, "10.0.0.9")
		l.Succeeded(u, "10.0.0.9")
	}
	if l.Attempt("satu-lagi", "10.0.0.9") != 0 {
		t.Error("login yang berhasil ikut dihitung sebagai kegagalan IP")
	}
}

func TestLoginLimiterCancel(t *testing.T) {
	l, _ := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 2*MaxFailsPerUsername; i++ {
		l.Attempt("alice", "10.0.0.1")
		l.Cancel("alice", "10.0.0.1")
	}
	if l.Attempt("alice", "10.0.0.1") != 0 {
		t.Error("percobaan yang dibatalkan ikut dihitung")
	}
}

func TestLoginLimiterConcurrentBurst(t *testing.T) {
	l := NewLoginLimiter()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Attempt("alice", "10.0.0.1") == 0 {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := allowed.Load(); n != MaxFailsPerUsername {
		t.Fatalf("%d dari 50 percobaan serentak lolos, want %d", n, MaxFailsPerUsername)
	}
}

func TestLoginLimiterForgetsOldEntries(t *testing.T) {
	l, now := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 100; i++ {
		l.Attempt(fmt.Sprintf("user%d", i), "10.0.0.1")
	}
	*now = now.Add(LoginFailWindow + time.Second)
	l.Attempt("terakhir", "10.0.0.2")

	l.pair.mu.Lock()
	defer l.pair.mu.Unlock()
	if n := len(l.pair.fails); n != 1 {
		t.Errorf("masih ada %d username di memori, want 1", n)
	}
}
