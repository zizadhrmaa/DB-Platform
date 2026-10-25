package testutil

import "testing"

func TestRandomHex(t *testing.T) {
	s1 := randomHex(8)
	s2 := randomHex(8)
	if len(s1) != 16 || len(s2) != 16 {
		t.Errorf("panjang randomHex salah: %d, %d", len(s1), len(s2))
	}
	if s1 == s2 {
		t.Errorf("randomHex menghasilkan nilai sama: %s", s1)
	}
}
