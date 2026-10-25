package auth

import "time"

// SetClock hanya ada saat test, supaya timeout dan rotasi bisa dites tanpa
// menunggu waktu sungguhan.
func (m *DBSessionManager) SetClock(now func() time.Time) { m.now = now }
