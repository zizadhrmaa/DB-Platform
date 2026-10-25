// Package authz berisi semua aturan hak akses.

// Modul lain jangan mengecek level sendiri (if level == "edit"), selalu
// lewat package ini.
package authz

import (
	"fmt"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Action string

const (
	ActionViewSchema       Action = "db.schema.view"
	ActionViewData         Action = "db.data.view"
	ActionViewMigrations   Action = "db.migration.view"
	ActionRunReadQuery     Action = "db.query.readonly"
	ActionCreateMigration  Action = "db.migration.create"
	ActionEditMigration    Action = "db.migration.edit" // termasuk discard
	ActionExecuteMigration Action = "db.migration.execute"
)

// Aksi yang tidak ada di sini selalu ditolak.
var requiredLevel = map[Action]domain.AccessLevel{
	ActionViewSchema:       domain.AccessView,
	ActionViewData:         domain.AccessView,
	ActionRunReadQuery:     domain.AccessView,
	ActionViewMigrations:   domain.AccessView,
	ActionCreateMigration:  domain.AccessEdit,
	ActionEditMigration:    domain.AccessEdit,
	ActionExecuteMigration: domain.AccessEdit,
}

func RequiredLevel(a Action) (domain.AccessLevel, bool) {
	l, ok := requiredLevel[a]
	return l, ok
}

// EffectiveLevel mengambil level tertinggi dari semua group. Tidak ada
// grant "deny", jadi masuk ke group baru tidak pernah mengurangi akses.
func EffectiveLevel(levels ...domain.AccessLevel) domain.AccessLevel {
	eff := domain.AccessNone
	for _, l := range levels {
		if l > eff {
			eff = l
		}
	}
	return eff
}

// Decide tidak melakukan I/O. User tanpa akses sama sekali mendapat
// ErrNotFound, bukan ErrForbidden, supaya keberadaan database tidak bocor.
func Decide(u *domain.User, level domain.AccessLevel, action Action) error {
	if u == nil || !u.IsActive {
		return domain.ErrUnauthenticated
	}
	need, ok := requiredLevel[action]
	if !ok {
		return fmt.Errorf("%w: aksi %q tidak terdaftar di kebijakan", domain.ErrForbidden, action)
	}
	if level == domain.AccessNone {
		return fmt.Errorf("%w: tidak ada akses ke database", domain.ErrNotFound)
	}
	if level < need {
		return fmt.Errorf("%w: aksi %s membutuhkan %s", domain.ErrForbidden, action, need.Label())
	}
	return nil
}

// IsPlatformAdmin hanya untuk halaman /admin. Admin tetap butuh grant
// group untuk membuka database.
func IsPlatformAdmin(u *domain.User) bool {
	return u != nil && u.IsActive && u.IsPlatformAdmin
}
