package auth

import "time"

// SessionConfig berisi parameter session. Nilainya juga ditulis di
// README.md (bagian Session), jadi kalau diubah, ubah juga di sana.
type SessionConfig struct {
	CookieName       string
	IdleTimeout      time.Duration
	AbsoluteTimeout  time.Duration // dihitung dari login, tidak diperpanjang rotasi
	RotationInterval time.Duration
	GracePeriod      time.Duration // ID lama masih diterima selama ini setelah rotasi
	Secure           bool
}

func DefaultSessionConfig(secure bool) SessionConfig {
	return SessionConfig{
		CookieName:       "dbp_session",
		IdleTimeout:      30 * time.Minute,
		AbsoluteTimeout:  8 * time.Hour,
		RotationInterval: 15 * time.Minute,
		GracePeriod:      30 * time.Second,
		Secure:           secure,
	}
}
