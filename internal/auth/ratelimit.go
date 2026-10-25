package auth

import (
	"strings"
	"sync"
	"time"
)

const (
	MaxFailsPerUsername = 5
	MaxFailsPerIP       = 20
	LoginFailWindow     = 15 * time.Minute
)

// LoginLimiter membatasi percobaan login yang gagal per username dan per
// IP. Datanya hanya di memori, jadi hilang kalau server restart.
type LoginLimiter struct {
	user *attemptLimiter
	ip   *attemptLimiter
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		user: newAttemptLimiter(MaxFailsPerUsername, LoginFailWindow),
		ip:   newAttemptLimiter(MaxFailsPerIP, LoginFailWindow),
	}
}

// Wait mengembalikan berapa lama lagi login boleh dicoba. Nol berarti
// boleh sekarang.
func (l *LoginLimiter) Wait(username, ip string) time.Duration {
	return max(l.user.wait(normalizeUsername(username)), l.ip.wait(ip))
}

func (l *LoginLimiter) Failed(username, ip string) {
	l.user.fail(normalizeUsername(username))
	l.ip.fail(ip)
}

func (l *LoginLimiter) Succeeded(username string) {
	l.user.reset(normalizeUsername(username))
}

func normalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
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

func (a *attemptLimiter) wait(key string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	recent := a.recent(key, now)
	if len(recent) < a.max {
		return 0
	}
	// Terkunci sampai percobaan tertua di jendela ini kedaluwarsa.
	return recent[len(recent)-a.max].Add(a.window).Sub(now)
}

func (a *attemptLimiter) fail(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	recent := append(a.recent(key, now), now)
	if len(recent) > a.max {
		recent = recent[len(recent)-a.max:]
	}
	a.fails[key] = recent
	a.sweep(now)
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
