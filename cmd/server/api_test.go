package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func (b *browser) keepCookie(rr *httptest.ResponseRecorder) {
	for _, c := range rr.Result().Cookies() {
		if c.Name == "dbp_session" {
			if c.MaxAge < 0 {
				b.cookie = ""
			} else {
				b.cookie = c.Value
			}
		}
	}
}

func (b *browser) csrfToken() string {
	b.t.Helper()
	rr := b.api(http.MethodGet, "/api/auth/me", nil)
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &me); err != nil || me.CSRFToken == "" {
		b.t.Fatalf("token CSRF tidak ditemukan (status %d)", rr.Code)
	}
	return me.CSRFToken
}

func (b *browser) api(method, path string, body any) *httptest.ResponseRecorder {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, origin+path, reader)
	req.Header.Set("Origin", origin)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "dbp_session", Value: b.cookie})
		if method != http.MethodGet {
			req.Header.Set("X-CSRF-Token", b.csrfToken())
		}
	}
	rr := httptest.NewRecorder()
	b.h.ServeHTTP(rr, req)
	b.keepCookie(rr)
	return rr
}

func decodeBody[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("body bukan JSON yang diharapkan: %v (%s)", err, rr.Body.String())
	}
	return v
}

func idOf(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(query, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAPIAdminRoutesRejectNonAdmin(t *testing.T) {
	db := testutil.PlatformDB(t)
	root := testutil.MustUser(t, db, "root", true)
	testutil.MustUser(t, db, "bob", false)
	gid := testutil.MustGroup(t, db, "viewers")
	toko := testutil.MustDBConn(t, db, "toko")
	h := newHandler(t, db)
	bob := loginAs(t, h, "bob", testutil.TestPassword)
	anon := &browser{t: t, h: h}

	empty := map[string]any{}
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/admin/users", nil},
		{"POST", "/api/admin/users", empty},
		{"GET", fmt.Sprintf("/api/admin/users/%d", root.ID), nil},
		{"PATCH", fmt.Sprintf("/api/admin/users/%d", root.ID), empty},
		{"DELETE", fmt.Sprintf("/api/admin/users/%d", root.ID), nil},
		{"GET", "/api/admin/groups", nil},
		{"POST", "/api/admin/groups", empty},
		{"GET", fmt.Sprintf("/api/admin/groups/%d", gid), nil},
		{"PATCH", fmt.Sprintf("/api/admin/groups/%d", gid), empty},
		{"DELETE", fmt.Sprintf("/api/admin/groups/%d", gid), nil},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/leader", gid), empty},
		{"POST", fmt.Sprintf("/api/admin/groups/%d/members", gid), empty},
		{"DELETE", fmt.Sprintf("/api/admin/groups/%d/members/%d", gid, root.ID), nil},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/access/%d", gid, toko), empty},
		{"DELETE", fmt.Sprintf("/api/admin/groups/%d/access/%d", gid, toko), nil},
		{"GET", "/api/admin/databases", nil},
		{"POST", "/api/admin/databases", empty},
		{"GET", fmt.Sprintf("/api/admin/databases/%d", toko), nil},
		{"PATCH", fmt.Sprintf("/api/admin/databases/%d", toko), empty},
		{"DELETE", fmt.Sprintf("/api/admin/databases/%d", toko), nil},
		{"POST", fmt.Sprintf("/api/admin/databases/%d/test", toko), nil},
	}
	for _, rt := range routes {
		if rr := bob.api(rt.method, rt.path, rt.body); rr.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s: status %d, want 403", rt.method, rt.path, rr.Code)
		}
		if rr := anon.api(rt.method, rt.path, rt.body); rr.Code != http.StatusUnauthorized {
			t.Errorf("anonim %s %s: status %d, want 401", rt.method, rt.path, rr.Code)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM users`); n != 2 {
		t.Errorf("jumlah user %d, want 2", n)
	}
	if n := count(t, db, `SELECT count(*) FROM groups`); n != 1 {
		t.Errorf("jumlah group %d, want 1", n)
	}
}

func TestAPIAdminHappyPath(t *testing.T) {
	db := testutil.PlatformDB(t)
	rootUser := testutil.MustUser(t, db, "root", true)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)

	created := func(rr *httptest.ResponseRecorder, what string) int64 {
		t.Helper()
		if rr.Code != http.StatusCreated {
			t.Fatalf("%s: status %d (%s)", what, rr.Code, rr.Body.String())
		}
		return decodeBody[struct {
			ID int64 `json:"id"`
		}](t, rr).ID
	}
	wantStatus := func(rr *httptest.ResponseRecorder, want int, what string) {
		t.Helper()
		if rr.Code != want {
			t.Fatalf("%s: status %d, want %d (%s)", what, rr.Code, want, rr.Body.String())
		}
	}

	carolID := created(root.api(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "carol", "password": "rahasia123"}), "buat user")
	erinID := created(root.api(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "erin", "password": "rahasia123"}), "buat user kedua")
	wantStatus(root.api(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "CAROL", "password": "rahasia123"}), http.StatusBadRequest, "username dobel")
	wantStatus(root.api(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "dave", "password": "123"}), http.StatusBadRequest, "password pendek")
	users := decodeBody[[]struct {
		Username string `json:"username"`
	}](t, root.api(http.MethodGet, "/api/admin/users", nil))
	if len(users) != 3 {
		t.Fatalf("jumlah user = %d, want 3", len(users))
	}

	gid := created(root.api(http.MethodPost, "/api/admin/groups",
		map[string]any{"name": "tim", "description": "uji"}), "buat group")
	wantStatus(root.api(http.MethodPost, "/api/admin/groups",
		map[string]any{"name": "TIM"}), http.StatusBadRequest, "nama group dobel")
	members := fmt.Sprintf("/api/admin/groups/%d/members", gid)
	wantStatus(root.api(http.MethodPost, members, map[string]any{"user_id": carolID}), http.StatusNoContent, "tambah anggota")
	wantStatus(root.api(http.MethodPost, members, map[string]any{"user_id": carolID}), http.StatusConflict, "anggota dobel")
	leader := fmt.Sprintf("/api/admin/groups/%d/leader", gid)
	wantStatus(root.api(http.MethodPut, leader, map[string]any{"user_id": erinID}), http.StatusBadRequest, "leader bukan anggota")
	wantStatus(root.api(http.MethodPut, leader, map[string]any{"user_id": carolID}), http.StatusNoContent, "set leader")

	conn := map[string]any{
		"name": "toko", "db_type": "postgres", "host": "localhost", "port": 5432,
		"database": "toko", "username": "u", "password": "p", "sslmode": "disable", "is_active": true,
	}
	dbID := created(root.api(http.MethodPost, "/api/admin/databases", conn), "buat koneksi")
	wantStatus(root.api(http.MethodPost, "/api/admin/databases", conn), http.StatusBadRequest, "nama koneksi dobel")

	access := fmt.Sprintf("/api/admin/groups/%d/access/%d", gid, dbID)
	wantStatus(root.api(http.MethodPut, access, map[string]any{"level": "admin"}), http.StatusBadRequest, "level tidak dikenal")
	wantStatus(root.api(http.MethodPut, fmt.Sprintf("/api/admin/groups/%d/access/99999", gid), map[string]any{"level": "view"}),
		http.StatusNotFound, "database tidak ada")
	wantStatus(root.api(http.MethodPut, access, map[string]any{"level": "edit"}), http.StatusNoContent, "grant edit")

	detail := decodeBody[struct {
		LeaderUserID *int64 `json:"leader_user_id"`
		Access       []struct {
			Level string `json:"level"`
		} `json:"access"`
	}](t, root.api(http.MethodGet, fmt.Sprintf("/api/admin/groups/%d", gid), nil))
	if detail.LeaderUserID == nil || *detail.LeaderUserID != carolID {
		t.Fatalf("leader = %v, want %d", detail.LeaderUserID, carolID)
	}
	if len(detail.Access) != 1 || detail.Access[0].Level != "edit" {
		t.Fatalf("akses = %+v, want satu baris edit", detail.Access)
	}

	carol := &browser{t: t, h: h}
	wantStatus(carol.api(http.MethodPost, "/api/auth/login",
		map[string]any{"username": "carol", "password": "rahasia123"}), http.StatusOK, "login carol")
	wantStatus(carol.api(http.MethodGet, "/api/auth/me", nil), http.StatusOK, "me carol")
	userPath := fmt.Sprintf("/api/admin/users/%d", carolID)
	wantStatus(root.api(http.MethodPatch, userPath, map[string]any{"is_active": false}), http.StatusOK, "nonaktifkan")
	wantStatus(carol.api(http.MethodGet, "/api/auth/me", nil), http.StatusUnauthorized, "me setelah nonaktif")
	if n := count(t, db, `SELECT count(*) FROM groups WHERE id = $1 AND leader_user_id IS NULL`, gid); n != 1 {
		t.Fatal("leader tidak dicabut saat user dinonaktifkan")
	}

	wantStatus(root.api(http.MethodPatch, userPath, map[string]any{"password": "baru12345"}), http.StatusOK, "reset password")
	wantStatus(root.api(http.MethodPatch, userPath, map[string]any{"is_active": true}), http.StatusOK, "aktifkan lagi")
	again := &browser{t: t, h: h}
	wantStatus(again.api(http.MethodPost, "/api/auth/login",
		map[string]any{"username": "carol", "password": "rahasia123"}), http.StatusUnauthorized, "password lama")
	wantStatus(again.api(http.MethodPost, "/api/auth/login",
		map[string]any{"username": "carol", "password": "baru12345"}), http.StatusOK, "password baru")

	wantStatus(root.api(http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", rootUser.ID), nil),
		http.StatusConflict, "hapus akun sendiri")
	wantStatus(root.api(http.MethodDelete, access, nil), http.StatusNoContent, "cabut akses")
	wantStatus(root.api(http.MethodDelete, access, nil), http.StatusNotFound, "cabut akses dua kali")
	wantStatus(root.api(http.MethodDelete, userPath, nil), http.StatusNoContent, "hapus user")
	wantStatus(root.api(http.MethodGet, userPath, nil), http.StatusNotFound, "user terhapus")
	wantStatus(root.api(http.MethodDelete, fmt.Sprintf("/api/admin/groups/%d", gid), nil), http.StatusNoContent, "hapus group")
	connPath := fmt.Sprintf("/api/admin/databases/%d", dbID)
	wantStatus(root.api(http.MethodDelete, connPath, nil), http.StatusNoContent, "hapus koneksi")
	wantStatus(root.api(http.MethodGet, connPath, nil), http.StatusNotFound, "koneksi terhapus")
}

func TestAPIMutationNeedsCSRFHeader(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)

	req := httptest.NewRequest(http.MethodPost, origin+"/api/admin/groups", strings.NewReader(`{"name":"tanpa-token"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.AddCookie(&http.Cookie{Name: "dbp_session", Value: root.cookie})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("tanpa header CSRF: status %d, want 403", rr.Code)
	}
	if n := count(t, db, `SELECT count(*) FROM groups`); n != 0 {
		t.Fatal("group terbuat tanpa token CSRF")
	}
	if rr := root.api(http.MethodPost, "/api/admin/groups", map[string]string{"name": "dengan-token"}); rr.Code != http.StatusCreated {
		t.Fatalf("dengan header CSRF: status %d, want 201", rr.Code)
	}
}

func TestAPILeaderRequiresUserIDField(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	bob := testutil.MustUser(t, db, "bob", false)
	gid := testutil.MustGroup(t, db, "tim")
	testutil.MustAddMember(t, db, gid, bob.ID)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)
	path := fmt.Sprintf("/api/admin/groups/%d/leader", gid)

	if rr := root.api(http.MethodPut, path, map[string]int64{"user_id": bob.ID}); rr.Code != http.StatusNoContent {
		t.Fatalf("set leader: status %d", rr.Code)
	}
	if rr := root.api(http.MethodPut, path, map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Fatalf("body kosong: status %d, want 400", rr.Code)
	}
	if n := count(t, db, `SELECT count(*) FROM groups WHERE leader_user_id = $1`, bob.ID); n != 1 {
		t.Fatal("leader terhapus oleh body kosong")
	}
	if rr := root.api(http.MethodPut, path, map[string]any{"user_id": nil}); rr.Code != http.StatusNoContent {
		t.Fatalf("hapus leader: status %d", rr.Code)
	}
	if n := count(t, db, `SELECT count(*) FROM groups WHERE leader_user_id IS NULL`); n != 1 {
		t.Fatal("leader tidak terhapus")
	}
}

func TestAPILoginFlow(t *testing.T) {
	b := newLoginServer(t)
	credentials := func(password string) map[string]string {
		return map[string]string{"username": "alice", "password": password}
	}

	if rr := b.api(http.MethodGet, "/api/auth/me", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("me tanpa login: status %d, want 401", rr.Code)
	}
	rr := b.api(http.MethodPost, "/api/auth/login", credentials("salah-banget"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("password salah: status %d, want 401", rr.Code)
	}
	if e := decodeBody[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, rr); e.Error.Code != "invalid_credentials" {
		t.Fatalf("kode error %q", e.Error.Code)
	}
	if b.cookie != "" {
		t.Fatal("login gagal memasang cookie")
	}

	if rr := b.api(http.MethodPost, "/api/auth/login", credentials(testutil.TestPassword)); rr.Code != http.StatusOK {
		t.Fatalf("login benar: status %d", rr.Code)
	}
	me := decodeBody[meResponse](t, b.api(http.MethodGet, "/api/auth/me", nil))
	if me.Username != "alice" || me.CSRFToken == "" || me.IsPlatformAdmin {
		t.Fatalf("me tidak sesuai: %+v", me)
	}

	if rr := b.api(http.MethodPost, "/api/auth/logout", nil); rr.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d, want 204", rr.Code)
	}
	if rr := b.api(http.MethodGet, "/api/auth/me", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("me setelah logout: status %d, want 401", rr.Code)
	}
}

func TestAPILoginRateLimited(t *testing.T) {
	b := newLoginServer(t)
	for i := 0; i < auth.MaxFailsPerUsername; i++ {
		rr := b.api(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": "salah-banget"})
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("percobaan ke-%d: status %d, want 401", i+1, rr.Code)
		}
	}
	rr := b.api(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": testutil.TestPassword})
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, want 429 dengan Retry-After", rr.Code)
	}
	if b.cookie != "" {
		t.Fatal("login yang terkunci memasang cookie")
	}
}

func TestAPIChangeRequestFlow(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	otherID := strings.Split(e.other, "/")[2]

	v1 := createMigration(t, bob, e.base, "satu", "CREATE TABLE satu (id INT)")
	migID := v1[strings.LastIndex(v1, "/")+1:]
	createPath := fmt.Sprintf("/api/databases/%s/migrations/%s/requests", dbID, migID)
	body := map[string]any{"kind": "up"}

	if rr := carol.api(http.MethodPost, createPath, body); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only membuat request: status %d, want 403", rr.Code)
	}
	if rr := dave.api(http.MethodPost, createPath, body); rr.Code != http.StatusNotFound {
		t.Fatalf("tanpa akses membuat request: status %d, want 404", rr.Code)
	}

	rr := bob.api(http.MethodPost, createPath, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("buat request: status %d (%s)", rr.Code, rr.Body.String())
	}
	created := decodeBody[struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}](t, rr)
	if created.Status != "draft" {
		t.Fatalf("status awal %q", created.Status)
	}
	base := fmt.Sprintf("/api/databases/%s/requests/%d", dbID, created.ID)

	if rr := bob.api(http.MethodPost, base+"/enqueue", nil); rr.Code != http.StatusConflict {
		t.Fatalf("enqueue draft: status %d, want 409", rr.Code)
	}
	rr = bob.api(http.MethodPost, base+"/submit", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: status %d", rr.Code)
	}
	submitted := decodeBody[struct {
		Status    string `json:"status"`
		Approvals []any  `json:"approvals"`
	}](t, rr)
	if submitted.Status != "pending_review" || len(submitted.Approvals) != 2 {
		t.Fatalf("setelah submit: %+v", submitted)
	}

	if rr := bob.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("approve sebelum ada leader: status %d, want 403", rr.Code)
	}
	var viewers int64
	if err := e.db.QueryRow(`SELECT id FROM groups WHERE name = 'viewers'`).Scan(&viewers); err != nil {
		t.Fatal(err)
	}
	var bobID, carolID int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'bob'`).Scan(&bobID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = 'carol'`).Scan(&carolID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, e.group, bobID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, viewers, carolID); err != nil {
		t.Fatal(err)
	}

	if rr := dave.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("approve tanpa akses: status %d, want 404", rr.Code)
	}
	rr = bob.api(http.MethodPost, base+"/approve", nil)
	if rr.Code != http.StatusOK || decodeBody[struct {
		Status string `json:"status"`
	}](t, rr).Status != "pending_review" {
		t.Fatalf("approve pertama: status %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := bob.api(http.MethodPost, base+"/enqueue", nil); rr.Code != http.StatusConflict {
		t.Fatalf("enqueue sebelum semua approve: status %d, want 409", rr.Code)
	}
	rr = carol.api(http.MethodPost, base+"/approve", nil)
	if rr.Code != http.StatusOK || decodeBody[struct {
		Status string `json:"status"`
	}](t, rr).Status != "approved" {
		t.Fatalf("approve kedua: status %d (%s)", rr.Code, rr.Body.String())
	}

	if rr := bob.post(v1, url.Values{"title": {"baru"}, "sql": {"CREATE TABLE satu (id INT, x INT)"}}); rr.Code != http.StatusConflict {
		t.Fatalf("edit SQL setelah approved: status %d, want 409", rr.Code)
	}

	rr = bob.api(http.MethodPost, base+"/enqueue", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue: status %d (%s)", rr.Code, rr.Body.String())
	}
	job := decodeBody[struct {
		JobID int64 `json:"job_id"`
	}](t, rr)
	if job.JobID == 0 {
		t.Fatal("job_id kosong")
	}
	again := decodeBody[struct {
		JobID int64 `json:"job_id"`
	}](t, bob.api(http.MethodPost, base+"/enqueue", nil))
	if again.JobID != job.JobID {
		t.Fatalf("enqueue ulang harus idempoten: %d dan %d", job.JobID, again.JobID)
	}

	if rr := bob.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/requests/%d", otherID, created.ID), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("request lewat database lain: status %d, want 404", rr.Code)
	}
	if rr := carol.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/requests", dbID), nil); rr.Code != http.StatusOK {
		t.Fatalf("View Only melihat daftar: status %d", rr.Code)
	}
}

func setLeaders(t *testing.T, e *migrationEnv) {
	t.Helper()
	bobID := idOf(t, e.db, `SELECT id FROM users WHERE username = 'bob'`)
	carolID := idOf(t, e.db, `SELECT id FROM users WHERE username = 'carol'`)
	viewers := idOf(t, e.db, `SELECT id FROM groups WHERE name = 'viewers'`)
	for _, c := range [][2]int64{{e.group, bobID}, {viewers, carolID}} {
		if _, err := e.db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func createRequest(t *testing.T, b *browser, e *migrationEnv, title string) (int64, string) {
	t.Helper()
	dbID := strings.Split(e.base, "/")[2]
	loc := createMigration(t, b, e.base, title, "CREATE TABLE "+title+" (id INT)")
	migID, err := strconv.ParseInt(loc[strings.LastIndex(loc, "/")+1:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	rr := b.api(http.MethodPost, fmt.Sprintf("/api/databases/%s/migrations/%d/requests", dbID, migID), map[string]any{"kind": "up"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("buat request %s: status %d (%s)", title, rr.Code, rr.Body.String())
	}
	id := decodeBody[struct {
		ID int64 `json:"id"`
	}](t, rr).ID
	return migID, fmt.Sprintf("/api/databases/%s/requests/%d", dbID, id)
}

func submitRequest(t *testing.T, b *browser, e *migrationEnv, title string) (int64, string) {
	t.Helper()
	migID, base := createRequest(t, b, e, title)
	if rr := b.api(http.MethodPost, base+"/submit", nil); rr.Code != http.StatusOK {
		t.Fatalf("submit %s: status %d (%s)", title, rr.Code, rr.Body.String())
	}
	return migID, base
}

func requestIDOf(base string) int64 {
	id, _ := strconv.ParseInt(base[strings.LastIndex(base, "/")+1:], 10, 64)
	return id
}

func TestAPIChangeRequestRejectAndCancel(t *testing.T) {
	e := newMigrationEnv(t)
	setLeaders(t, e)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]

	migID, base := submitRequest(t, bob, e, "t_reject")
	reject := map[string]string{"reason": "SQL belum benar"}

	if rr := dave.api(http.MethodPost, base+"/reject", reject); rr.Code != http.StatusNotFound {
		t.Fatalf("reject tanpa akses: status %d, want 404", rr.Code)
	}
	if rr := bob.api(http.MethodPost, base+"/reject", map[string]string{"reason": "  "}); rr.Code != http.StatusBadRequest {
		t.Fatalf("reject tanpa alasan: status %d, want 400", rr.Code)
	}
	rr := carol.api(http.MethodPost, base+"/reject", reject)
	if rr.Code != http.StatusOK {
		t.Fatalf("reject: status %d (%s)", rr.Code, rr.Body.String())
	}
	rejected := decodeBody[struct {
		Status       string `json:"status"`
		RejectReason string `json:"reject_reason"`
	}](t, rr)
	if rejected.Status != "rejected" || rejected.RejectReason != "SQL belum benar" {
		t.Fatalf("setelah reject: %+v", rejected)
	}
	if rr := bob.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusConflict {
		t.Fatalf("approve setelah reject: status %d, want 409", rr.Code)
	}
	if rr := bob.api(http.MethodPost, base+"/cancel", nil); rr.Code != http.StatusConflict {
		t.Fatalf("cancel setelah reject: status %d, want 409", rr.Code)
	}
	again := fmt.Sprintf("/api/databases/%s/migrations/%d/requests", dbID, migID)
	if rr := bob.api(http.MethodPost, again, map[string]any{"kind": "up"}); rr.Code != http.StatusCreated {
		t.Fatalf("request baru setelah reject: status %d, want 201", rr.Code)
	}

	_, draft := createRequest(t, bob, e, "t_cancel")
	rr = bob.api(http.MethodPost, draft+"/cancel", nil)
	if rr.Code != http.StatusOK || decodeBody[struct {
		Status string `json:"status"`
	}](t, rr).Status != "cancelled" {
		t.Fatalf("cancel draft: status %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := bob.api(http.MethodPost, draft+"/cancel", nil); rr.Code != http.StatusConflict {
		t.Fatalf("cancel dua kali: status %d, want 409", rr.Code)
	}
	if rr := bob.api(http.MethodPost, draft+"/submit", nil); rr.Code != http.StatusConflict {
		t.Fatalf("submit setelah cancel: status %d, want 409", rr.Code)
	}
}

func TestAPIChangeRequestStaleRevision(t *testing.T) {
	e := newMigrationEnv(t)
	setLeaders(t, e)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)

	migID, base := submitRequest(t, bob, e, "t_stale")
	if _, err := e.db.Exec(`UPDATE migrations SET revision = revision + 1 WHERE id = $1`, migID); err != nil {
		t.Fatal(err)
	}
	if rr := bob.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusConflict {
		t.Fatalf("approve request stale: status %d, want 409", rr.Code)
	}
	detail := decodeBody[struct {
		Stale bool `json:"stale"`
	}](t, bob.api(http.MethodGet, base, nil))
	if !detail.Stale {
		t.Fatal("request seharusnya ditandai stale")
	}
}

func TestAPIApprovalRevokedImmediatelyWhenLeaderChanges(t *testing.T) {
	e := newMigrationEnv(t)
	setLeaders(t, e)
	testutil.MustUser(t, e.db, "root", true)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	root := loginAs(t, e.h, "root", testutil.TestPassword)

	_, base := submitRequest(t, bob, e, "t_revoke")
	if rr := bob.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusOK {
		t.Fatalf("approve bob: status %d", rr.Code)
	}
	if rr := carol.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusOK {
		t.Fatalf("approve carol: status %d", rr.Code)
	}
	reqID := requestIDOf(base)
	status := func() string {
		var st string
		if err := e.db.QueryRow(`SELECT status FROM change_requests WHERE id = $1`, reqID).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := status(); st != "approved" {
		t.Fatalf("status sebelum perubahan leader = %s, want approved", st)
	}

	path := fmt.Sprintf("/api/admin/groups/%d/leader", e.group)
	if rr := root.api(http.MethodPut, path, map[string]any{"user_id": nil}); rr.Code != http.StatusNoContent {
		t.Fatalf("hapus leader: status %d", rr.Code)
	}
	if st := status(); st != "pending_review" {
		t.Fatalf("status setelah leader dihapus = %s, want pending_review", st)
	}
	if n := count(t, e.db, `SELECT count(*) FROM change_request_approvals WHERE request_id = $1 AND status = 'pending'`, reqID); n != 1 {
		t.Fatalf("approval pending = %d, want 1 (group tanpa leader)", n)
	}
	if rr := bob.api(http.MethodPost, base+"/enqueue", nil); rr.Code != http.StatusConflict {
		t.Fatalf("enqueue setelah approval dicabut: status %d, want 409", rr.Code)
	}
}

func TestAPIJobAndHistoryRoutesAreAuthorized(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	anon := &browser{t: t, h: e.h}
	dbID := strings.Split(e.base, "/")[2]

	cases := []struct {
		method, path               string
		bob, carol, dave, anonCode int
	}{
		{"GET", "/jobs", 200, 200, 404, 401},
		{"GET", "/jobs/1", 404, 404, 404, 401}, // job 1 tidak ada
		{"GET", "/jobs?status=aneh", 400, 400, 404, 401},
		{"GET", "/history", 200, 200, 404, 401},
		{"POST", "/jobs/1/resume", 501, 403, 404, 401},
		{"POST", "/jobs/1/retry", 501, 403, 404, 401},
		{"POST", "/jobs/1/revert", 501, 403, 404, 401},
	}
	for _, c := range cases {
		path := "/api/databases/" + dbID + c.path
		for who, want := range map[string]int{"bob": c.bob, "carol": c.carol, "dave": c.dave, "anon": c.anonCode} {
			b := map[string]*browser{"bob": bob, "carol": carol, "dave": dave, "anon": anon}[who]
			if rr := b.api(c.method, path, nil); rr.Code != want {
				t.Errorf("%s %s %s: status %d, want %d", who, c.method, c.path, rr.Code, want)
			}
		}
	}
}
