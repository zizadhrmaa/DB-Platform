package dbmanager

import (
	"testing"
	"time"
)

func TestMySQLQuoteIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mahasiswa", "`mahasiswa`"},
		{"kampus", "`kampus`"},
		{"table`with`tick", "`table``with``tick`"},
		{"Audit Log", "`Audit Log`"},
	}

	for _, tc := range tests {
		got := mysqlQuoteIdent(tc.input)
		if got != tc.want {
			t.Errorf("mysqlQuoteIdent(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatCellMySQLTypes(t *testing.T) {
	cellNull := formatCell(nil, false)
	if !cellNull.Null {
		t.Errorf("formatCell(nil) harus null, got %+v", cellNull)
	}

	cellText := formatCell("Halo Dunia", false)
	if cellText.Text != "Halo Dunia" || cellText.Null {
		t.Errorf("formatCell string salah: %+v", cellText)
	}

	bin := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	cellBin := formatCell(bin, true)
	if cellBin.Text != `\xdeadbeef` {
		t.Errorf("formatCell binary salah: %s", cellBin.Text)
	}

	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	cellTime := formatCell(now, false)
	if cellTime.Text == "" {
		t.Error("formatCell time kosong")
	}
}

func TestClampPage(t *testing.T) {
	p, s := ClampPage("-1", "0")
	if p != 1 || s != DefaultPageSize {
		t.Errorf("ClampPage(-1, 0) = (%d, %d), want (1, %d)", p, s, DefaultPageSize)
	}

	p, s = ClampPage("5", "500")
	if p != 5 || s != MaxPageSize {
		t.Errorf("ClampPage(5, 500) = (%d, %d), want (5, %d)", p, s, MaxPageSize)
	}
}

func TestIsMySQLNil(t *testing.T) {
	if IsMySQL(nil) {
		t.Error("IsMySQL(nil) harus false")
	}
}
