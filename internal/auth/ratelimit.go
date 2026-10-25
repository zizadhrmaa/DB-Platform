package auth

import (
	"strings"
	"sync"
	"time"
)

const (
	MaxFailsPerUsername = 5
	MaxFailsPerIP       = 100
	LoginFailWindow     = 15 * time.Minute
)

// LoginLimiter membatasi percobaan login per pasangan username dan IP, dan
// per IP. Batas per username saja tidak dipakai karena siapa pun bisa
// mengunci akun orang lain cukup dengan salah password lima kali. Datanya
// hanya di memori, jadi hilang kalau server restart.
type LoginLimiter struct {
	pair *attemptLimiter
	ip   *attemptLimiter
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		pair: newAttemptLimiter(MaxFailsPerUsername, LoginFailWindow),
		ip:   newAttemptLimiter(MaxFailsPerIP, LoginFailWindow),
	}
}

// Attempt mencatat satu percobaan login sebelum password dicek, lalu
// mengembalikan berapa lama lagi login boleh dicoba. Nol berarti boleh.
// Percobaan dicatat di awal supaya request serentak tidak bisa melewati
// batas sebelum kegagalannya sempat terhitung.
func (l *LoginLimiter) Attempt(username, ip string) time.Duration {
	key := pairKey(username, ip)
	if w := l.pair.take(key); w > 0 {
		return w
	}
	if w := l.ip.take(ip); w > 0 {
		l.pair.undo(key)
		return w
	}
	return 0
}

// Succeeded menghapus hitungan pasangan ini dan tidak menghitung login yang
// berhasil sebagai kegagalan IP.
func (l *LoginLimiter) Succeeded(username, ip string) {
	l.pair.reset(pairKey(username, ip))
	l.ip.undo(ip)
}

// Cancel membatalkan percobaan yang tidak sempat dicek, misalnya karena
// server sedang sibuk.
func (l *LoginLimiter) Cancel(username, ip string) {
	l.pair.undo(pairKey(username, ip))
	l.ip.undo(ip)
}

func pairKey(username, ip string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "\x00" + ip
}

type attemptLimiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	now       func() time.Time
	fails     map[string][]time.Time
	lastSweep time.Time
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: limit, window: window, now: time.Now, fails: map[string][]time.Time{}}
}

// take mencatat percobaan kalau masih di bawah batas. Kalau sudah penuh,
// tidak ada yang dicatat dan hasilnya waktu tunggu sampai percobaan tertua
// di jendela ini kedaluwarsa.
func (a *attemptLimiter) take(key string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	recent := a.recent(key, now)
	if len(recent) >= a.max {
		return recent[len(recent)-a.max].Add(a.window).Sub(now)
	}
	a.fails[key] = append(recent, now)
	a.sweep(now)
	return 0
}

func (a *attemptLimiter) undo(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.fails[key]
	switch len(list) {
	case 0:
	case 1:
		delete(a.fails, key)
	default:
		a.fails[key] = list[:len(list)-1]
	}
}

func (a *attemptLimiter) reset(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, key)
}

// recent membuang percobaan yang sudah di luar jendela waktu.
func (a *attemptLimiter) recent(key string, now time.Time) []time.Time {
	list := a.fails[key]
	i := 0
	for i < len(list) && !now.Before(list[i].Add(a.window)) {
		i++
	}
	if i == len(list) {
		delete(a.fails, key)
		return nil
	}
	list = list[i:]
	a.fails[key] = list
	return list
}

// sweep menghapus key yang sudah tidak punya percobaan aktif supaya map
// tidak terus membesar. Cukup dijalankan sekali per jendela waktu.
func (a *attemptLimiter) sweep(now time.Time) {
	if now.Sub(a.lastSweep) < a.window {
		return
	}
	a.lastSweep = now
	for key := range a.fails {
		a.recent(key, now)
	}
}
