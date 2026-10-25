package admin_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func newConnections(t *testing.T) (*admin.Connections, testutil.PGParams) {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	box, err := dbmanager.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return &admin.Connections{DB: testutil.PlatformDB(t), Secrets: box}, testutil.ServerParams(t)
}

func serverForm(name string, srv testutil.PGParams) admin.ConnectionForm {
	return admin.ConnectionForm{
		Name:     name,
		Host:     srv.Host,
		Port:     fmt.Sprint(srv.Port),
		Database: srv.Database,
		Username: srv.User,
		IsActive: true,
	}
}

func storedCipher(t *testing.T, c *admin.Connections, id int64) []byte {
	t.Helper()
	var cipher []byte
	if err := c.DB.QueryRow(`SELECT password_ciphertext FROM db_connections WHERE id = $1`, id).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestCreateConnection(t *testing.T) {
	c, srv := newConnections(t)
	ctx := context.Background()
	root := testutil.MustUser(t, c.DB, "root", true)

	id, err := c.Create(ctx, serverForm("  Toko  ", srv), "p@ss:w/rd?", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Get(ctx, id)
	if err != nil || info.Name != "Toko" || info.SSLMode != "disable" || !info.IsActive {
		t.Fatalf("data tersimpan: %+v err=%v", info, err)
	}

	cipher := storedCipher(t, c, id)
	if bytes.Contains(cipher, []byte("p@ss")) {
		t.Fatal("password tersimpan dalam bentuk asli")
	}
	if plain, err := c.Secrets.Decrypt(cipher); err != nil || plain != "p@ss:w/rd?" {
		t.Fatalf("password tidak bisa dibuka lagi: %q %v", plain, err)
	}

	base := serverForm("lain", srv)
	for _, tc := range []struct {
		name     string
		edit     func(*admin.ConnectionForm)
		password string
		msg      string
	}{
		{"nama kosong", func(f *admin.ConnectionForm) { f.Name = " " }, "x", "Nama koneksi"},
		{"nama dobel", func(f *admin.ConnectionForm) { f.Name = "TOKO" }, "x", "sudah dipakai"},
		{"host berisi spasi", func(f *admin.ConnectionForm) { f.Host = "a b" }, "x", "Host"},
		{"host berisi kredensial", func(f *admin.ConnectionForm) { f.Host = "user@host" }, "x", "Host"},
		{"port bukan angka", func(f *admin.ConnectionForm) { f.Port = "abc" }, "x", "Port"},
		{"port terlalu besar", func(f *admin.ConnectionForm) { f.Port = "70000" }, "x", "Port"},
		{"database kosong", func(f *admin.ConnectionForm) { f.Database = "" }, "x", "database"},
		{"username kosong", func(f *admin.ConnectionForm) { f.Username = "" }, "x", "Username"},
		{"sslmode aneh", func(f *admin.ConnectionForm) { f.SSLMode = "prefer; drop" }, "x", "SSL"},
		{"password kosong", func(f *admin.ConnectionForm) {}, "", "Password"},
	} {
		f := base
		tc.edit(&f)
		_, err := c.Create(ctx, f, tc.password, root.ID)
		var fe *admin.FormError
		if !errors.As(err, &fe) || !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(fe.Msg, tc.msg) {
			t.Errorf("%s: err=%v, want pesan berisi %q", tc.name, err, tc.msg)
		}
	}
}

func TestUpdateConnectionKeepsPassword(t *testing.T) {
	c, srv := newConnections(t)
	ctx := context.Background()
	root := testutil.MustUser(t, c.DB, "root", true)
	id, err := c.Create(ctx, serverForm("toko", srv), "lama", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := storedCipher(t, c, id)

	f := serverForm("toko baru", srv)
	f.IsActive = false
	if err := c.Update(ctx, id, f, ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, storedCipher(t, c, id)) {
		t.Error("password berubah padahal field password kosong")
	}
	if info, _ := c.Get(ctx, id); info.Name != "toko baru" || info.IsActive {
		t.Errorf("perubahan tidak tersimpan: %+v", info)
	}

	if err := c.Update(ctx, id, f, "baru"); err != nil {
		t.Fatal(err)
	}
	if plain, _ := c.Secrets.Decrypt(storedCipher(t, c, id)); plain != "baru" {
		t.Errorf("password baru tidak tersimpan: %q", plain)
	}

	if err := c.Update(ctx, 99999, f, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("koneksi tidak ada: err=%v", err)
	}
}

func TestTestConnection(t *testing.T) {
	c, srv := newConnections(t)
	ctx := context.Background()
	root := testutil.MustUser(t, c.DB, "root", true)

	good, err := c.Create(ctx, serverForm("benar", srv), srv.Password, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := c.Create(ctx, serverForm("salah", srv), "password-salah", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Test(ctx, good); err != nil {
		t.Errorf("koneksi benar gagal diuji: %v", err)
	}
	if err := c.Test(ctx, bad); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("password salah: err=%v, want ErrUnavailable", err)
	}
	if err := c.Test(ctx, 99999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("koneksi tidak ada: err=%v", err)
	}
}

func TestDeleteConnection(t *testing.T) {
	c, srv := newConnections(t)
	ctx := context.Background()
	root := testutil.MustUser(t, c.DB, "root", true)
	id, err := c.Create(ctx, serverForm("toko", srv), "x", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	gid := testutil.MustGroup(t, c.DB, "viewers")
	testutil.MustGrant(t, c.DB, gid, id, "view")

	if err := c.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("koneksi masih ada: err=%v", err)
	}
	var n int
	if err := c.DB.QueryRow(`SELECT count(*) FROM group_db_access WHERE db_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("grant tidak ikut terhapus: n=%d err=%v", n, err)
	}
	if err := c.Delete(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("hapus dua kali: err=%v", err)
	}
}
