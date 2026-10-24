// Package domain berisi tipe dan error yang dipakai semua modul.
package domain

import (
	"errors"
	"fmt"
)

// User dimuat ulang dari database tiap request, jadi perubahan role atau
// status aktif langsung berlaku.
type User struct {
	ID              int64
	Username        string
	IsPlatformAdmin bool
	IsActive        bool
}

// AccessLevel terurut, jadi bisa dibandingkan langsung dengan < dan >.
type AccessLevel int

const (
	AccessNone AccessLevel = iota
	AccessView
	AccessEdit
)

// String mengembalikan nilai kolom group_db_access.level.
func (l AccessLevel) String() string {
	switch l {
	case AccessView:
		return "view"
	case AccessEdit:
		return "edit"
	default:
		return "none"
	}
}

func (l AccessLevel) Label() string {
	switch l {
	case AccessView:
		return "View Only"
	case AccessEdit:
		return "View + Edit"
	default:
		return "Tidak ada akses"
	}
}

// ParseAccessLevel sengaja menolak "none". Mencabut akses dilakukan dengan
// menghapus baris grant.
func ParseAccessLevel(s string) (AccessLevel, error) {
	switch s {
	case "view":
		return AccessView, nil
	case "edit":
		return AccessEdit, nil
	default:
		return AccessNone, fmt.Errorf("%w: level akses tidak dikenal %q", ErrInvalidInput, s)
	}
}

// Bungkus dengan %w supaya httpx.StatusFor bisa memetakan ke status HTTP.
var (
	ErrUnauthenticated = errors.New("belum login atau session berakhir")
	ErrForbidden       = errors.New("akses ditolak")
	ErrNotFound        = errors.New("tidak ditemukan")
	ErrInvalidInput    = errors.New("input tidak valid")
	ErrConflict        = errors.New("konflik dengan data saat ini")
	ErrUnavailable     = errors.New("layanan tidak tersedia")
)
