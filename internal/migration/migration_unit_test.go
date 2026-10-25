package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestErrorMessage(t *testing.T) {
	myErr := &mysql.MySQLError{
		Number:  1064,
		Message: "syntax error near line 1",
	}
	msg := errorMessage(myErr)
	if !strings.Contains(msg, "1064") || !strings.Contains(msg, "syntax error") {
		t.Errorf("pesan error mysql salah: %s", msg)
	}

	timeoutMsg := errorMessage(context.DeadlineExceeded)
	if !strings.Contains(timeoutMsg, "batas waktu") {
		t.Errorf("pesan timeout salah: %s", timeoutMsg)
	}

	genericMsg := errorMessage(errors.New("connection reset by peer"))
	if !strings.Contains(genericMsg, "terputus") {
		t.Errorf("pesan generic salah: %s", genericMsg)
	}
}

func TestValidateSQLMySQL(t *testing.T) {
	valid := []string{
		"CREATE TABLE mahasiswa (nim CHAR(8) PRIMARY KEY, nama VARCHAR(255));",
		"ALTER TABLE mahasiswa ADD COLUMN angkatan INT;",
		"INSERT INTO mahasiswa VALUES ('13524001', 'Budi', 2023);",
		"UPDATE mahasiswa SET nama = 'Budi Baru' WHERE nim = '13524001';",
	}
	for _, sql := range valid {
		if err := ValidateSQL(sql); err != nil {
			t.Errorf("SQL valid %q ditolak: %v", sql, err)
		}
	}

	invalid := []string{
		"BEGIN; INSERT INTO test VALUES (1); COMMIT;",
		"START TRANSACTION; SELECT 1;",
		"CREATE DATABASE new_db;",
		"DROP DATABASE old_db;",
		"",
		"   ; ;  ",
	}
	for _, sql := range invalid {
		if err := ValidateSQL(sql); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("SQL tidak valid %q harus ditolak, got %v", sql, err)
		}
	}
}
