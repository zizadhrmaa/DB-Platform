package admin_test

import (
	"errors"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestConnectionFormParse(t *testing.T) {
	tests := []struct {
		name       string
		form       admin.ConnectionForm
		wantDBType string
		wantPort   int
		wantErr    bool
	}{
		{
			name: "default postgres port",
			form: admin.ConnectionForm{
				Name:     "PG Staging",
				Host:     "localhost",
				Database: "postgres",
				Username: "postgres",
			},
			wantDBType: dbmanager.DBTypePostgres,
			wantPort:   5432,
			wantErr:    false,
		},
		{
			name: "default mysql port",
			form: admin.ConnectionForm{
				Name:     "MySQL Staging",
				DBType:   "mysql",
				Host:     "localhost",
				Database: "kampus",
				Username: "root",
			},
			wantDBType: dbmanager.DBTypeMySQL,
			wantPort:   3306,
			wantErr:    false,
		},
		{
			name: "custom mysql port",
			form: admin.ConnectionForm{
				Name:     "MySQL Custom",
				DBType:   "mysql",
				Host:     "localhost",
				Port:     "3307",
				Database: "kampus",
				Username: "root",
			},
			wantDBType: dbmanager.DBTypeMySQL,
			wantPort:   3307,
			wantErr:    false,
		},
		{
			name: "unsupported db_type",
			form: admin.ConnectionForm{
				Name:     "Oracle DB",
				DBType:   "oracle",
				Host:     "localhost",
				Database: "xe",
				Username: "system",
			},
			wantErr: true,
		},
		{
			name: "empty name",
			form: admin.ConnectionForm{
				Name:     "   ",
				DBType:   "mysql",
				Host:     "localhost",
				Database: "db",
				Username: "user",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := tc.form.Parse()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, domain.ErrInvalidInput) {
					t.Fatalf("expected ErrInvalidInput, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.DBType != tc.wantDBType {
				t.Errorf("DBType = %s, want %s", info.DBType, tc.wantDBType)
			}
			if info.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d", info.Port, tc.wantPort)
			}
		})
	}
}
