package auth

import (
	"testing"
	"time"
)

func newTestLimiter(start time.Time) (*LoginLimiter, *time.Time) {
	now := start
	l := NewLoginLimiter()
	l.user.now = func() time.Time { return now }
	l.ip.now = func() time.Time { return now }
	return l, &now
}

func TestLoginLimiterPerUsername(t *testing.T) {
	l, now := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))

	for i := 0; i < MaxFailsPerUsername; i++ {
		if w := l.Wait("alice", "10.0.0.1"); w != 0 {
			t.Fatalf("percobaan ke-%d sudah dikunci (%v)", i+1, w)
		}
		l.Failed("alice", "10.0.0.1")
		*now = now.Add(time.Minute)
	}

	// Huruf besar/kecil dianggap username yang sama.
	w := l.Wait("ALICE", "10.0.0.2")
	if w <= 0 {
		t.Fatal("username tidak dikunci setelah 5 kali gagal")
	}
	// Percobaan pertama di menit 0, jadi terbuka lagi di menit 15.
	if want := 10 * time.Minute; w != want {
		t.Errorf("waktu tunggu %v, want %v", w, want)
	}
	if l.Wait("bob", "10.0.0.2") != 0 {
		t.Error("username lain ikut terkunci")
	}

	*now = now.Add(w)
	if l.Wait("alice", "10.0.0.1") != 0 {
		t.Error("username masih terkunci setelah waktu tunggu habis")
	}
}

func TestLoginLimiterPerIP(t *testing.T) {
	l, _ := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))

	// Tiap username hanya dicoba sekali, tapi dari IP yang sama.
	for i := 0; i < MaxFailsPerIP; i++ {
		l.Failed(string(rune('a'+i))+"user", "10.0.0.1")
	}
	if l.Wait("username-baru", "10.0.0.1") == 0 {
		t.Fatal("IP tidak dikunci setelah 20 kali gagal")
	}
	if l.Wait("username-baru", "10.0.0.2") != 0 {
		t.Error("IP lain ikut terkunci")
	}
}

func TestLoginLimiterResetOnSuccess(t *testing.T) {
	l, _ := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	for i := 0; i < MaxFailsPerUsername-1; i++ {
		l.Failed("alice", "10.0.0.1")
	}
	l.Succeeded("alice")
	l.Failed("alice", "10.0.0.1")
	if l.Wait("alice", "10.0.0.1") != 0 {
		t.Error("hitungan gagal tidak di-reset setelah login berhasil")
	}
}

func TestLoginLimiterForgetsOldEntries(t *testing.T) {
	l, now := newTestLimiter(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 100; i++ {
		l.Failed(string(rune('a'+i%26))+string(rune('a'+i/26)), "10.0.0.1")
	}
	*now = now.Add(LoginFailWindow + time.Second)
	l.Failed("terakhir", "10.0.0.2")

	l.user.mu.Lock()
	defer l.user.mu.Unlock()
	if n := len(l.user.fails); n != 1 {
		t.Errorf("masih ada %d username di memori, want 1", n)
	}
}
