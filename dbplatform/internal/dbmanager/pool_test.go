package dbmanager_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func newBox(t *testing.T) *dbmanager.SecretBox {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	box, err := dbmanager.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// register menyimpan koneksi ke server test di db_connections.
func register(t *testing.T, platform *sql.DB, box *dbmanager.SecretBox, name string, port int, password string, active bool) int64 {
	t.Helper()
	srv := testutil.ServerParams(t)
	if port == 0 {
		port = srv.Port
	}
	cipher, err := box.Encrypt(password)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = platform.QueryRow(`
		INSERT INTO db_connections (name, host, port, database_name, username, password_ciphertext, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		name, srv.Host, port, srv.Database, srv.User, cipher, active).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// closedPort mengembalikan port lokal yang dipastikan tidak sedang dipakai.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestPoolManager(t *testing.T) {
	platform := testutil.PlatformDB(t)
	box := newBox(t)
	srv := testutil.ServerParams(t)
	ctx := context.Background()
	m := dbmanager.NewPoolManager(platform, box)
	t.Cleanup(func() { m.Close() })

	ok := register(t, platform, box, "ok", 0, srv.Password, true)
	inactive := register(t, platform, box, "inactive", 0, srv.Password, false)
	wrongPass := register(t, platform, box, "salah", 0, "password-salah-sekali", true)
	down := register(t, platform, box, "mati", closedPort(t), srv.Password, true)

	db, err := m.Conn(ctx, ok)
	if err != nil {
		t.Fatalf("koneksi valid gagal: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := m.Conn(ctx, ok); again != db {
		t.Error("pool tidak dipakai ulang")
	}

	m.Invalidate(ok)
	if err := db.PingContext(ctx); err == nil {
		t.Error("pool lama masih terbuka setelah Invalidate")
	}
	if fresh, err := m.Conn(ctx, ok); err != nil || fresh == db {
		t.Errorf("setelah Invalidate seharusnya pool baru: err=%v", err)
	}

	if _, err := m.Conn(ctx, inactive); !errors.Is(err, dbmanager.ErrConnInactive) || !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("koneksi nonaktif: err=%v", err)
	}
	if _, err := m.Conn(ctx, 99999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("koneksi tidak ada: err=%v", err)
	}

	for name, id := range map[string]int64{"password salah": wrongPass, "server mati": down} {
		_, err := m.Conn(ctx, id)
		if !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: err=%v, want ErrUnavailable", name, err)
			continue
		}
		for _, secret := range []string{"password-salah-sekali", srv.Password, srv.User, srv.Host} {
			if secret != "" && strings.Contains(err.Error(), secret) {
				t.Errorf("%s: pesan error memuat %q", name, secret)
			}
		}
	}
	// Koneksi yang gagal tidak disimpan, jadi percobaan berikutnya mencoba lagi.
	if _, err := m.Conn(ctx, down); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("percobaan kedua: err=%v", err)
	}
}

func TestPoolManagerWrongKey(t *testing.T) {
	platform := testutil.PlatformDB(t)
	srv := testutil.ServerParams(t)
	id := register(t, platform, newBox(t), "ok", 0, srv.Password, true)

	// APP_SECRET_KEY berganti: password lama tidak bisa dibuka.
	m := dbmanager.NewPoolManager(platform, newBox(t))
	t.Cleanup(func() { m.Close() })
	if _, err := m.Conn(context.Background(), id); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("key salah: err=%v, want ErrUnavailable", err)
	}
}

func TestPoolManagerClose(t *testing.T) {
	platform := testutil.PlatformDB(t)
	box := newBox(t)
	srv := testutil.ServerParams(t)
	m := dbmanager.NewPoolManager(platform, box)
	db, err := m.Conn(context.Background(), register(t, platform, box, "ok", 0, srv.Password, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err == nil {
		t.Error("pool masih terbuka setelah Close")
	}
}
