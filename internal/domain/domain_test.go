package domain

import (
	"errors"
	"testing"
)

func TestAccessLevel(t *testing.T) {
	if AccessNone.String() != "none" || AccessNone.Label() != "Tidak ada akses" {
		t.Errorf("AccessNone salah: %s, %s", AccessNone.String(), AccessNone.Label())
	}
	if AccessView.String() != "view" || AccessView.Label() != "View Only" {
		t.Errorf("AccessView salah: %s, %s", AccessView.String(), AccessView.Label())
	}
	if AccessEdit.String() != "edit" || AccessEdit.Label() != "View + Edit" {
		t.Errorf("AccessEdit salah: %s, %s", AccessEdit.String(), AccessEdit.Label())
	}

	lvl, err := ParseAccessLevel("view")
	if err != nil || lvl != AccessView {
		t.Errorf("ParseAccessLevel(view) = %v, %v", lvl, err)
	}

	lvl, err = ParseAccessLevel("edit")
	if err != nil || lvl != AccessEdit {
		t.Errorf("ParseAccessLevel(edit) = %v, %v", lvl, err)
	}

	_, err = ParseAccessLevel("none")
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ParseAccessLevel(none) harus mengembalikan ErrInvalidInput, got %v", err)
	}

	_, err = ParseAccessLevel("unknown")
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ParseAccessLevel(unknown) harus mengembalikan ErrInvalidInput, got %v", err)
	}
}
