package main

import (
	"io/fs"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

// App menyimpan dependensi bersama yang dipakai handler.
type App struct {
	View  *view.Renderer
	Authz authz.Authorizer
}

// routes mendaftarkan semua route. Ganti app.todo dengan handler asli tanpa
// mengubah path, karena path dipakai template dan test.

// Form HTML cuma bisa GET/POST, jadi hapus/eksekusi pakai POST ke
// sub-path seperti .../delete dan .../execute.
func (app *App) routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	login := auth.RequireLogin
	admin := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
	db := func(a authz.Action, h http.Handler) http.Handler { return login(authz.RequireDB(app.Authz, a, h)) }

	// Authentication & Session
	mux.HandleFunc("GET /login", app.loginPage)
	mux.Handle("POST /login", app.todo("POST /login"))
	mux.Handle("POST /logout", login(app.todo("POST /logout")))

	// Database selection & viewer
	mux.Handle("GET /{$}", login(http.RedirectHandler("/databases", http.StatusSeeOther)))
	mux.Handle("GET /databases", login(http.HandlerFunc(app.databaseList)))
	mux.Handle("GET /databases/{dbID}", db(authz.ActionViewSchema, app.todo("GET /databases/{dbID} (daftar schema & tabel)")))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}", db(authz.ActionViewSchema, app.todo("struktur tabel: column, constraint, index")))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}/data", db(authz.ActionViewData, app.todo("data tabel ?page=&size=")))

	// Opsional (QnA 5 & 7): query console read-only. Hapus jika tidak dipakai.
	mux.Handle("GET /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("form query read-only")))
	mux.Handle("POST /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("jalankan query read-only")))

	// Migration
	mux.Handle("GET /databases/{dbID}/migrations", db(authz.ActionViewMigrations, app.todo("riwayat migration")))
	mux.Handle("GET /databases/{dbID}/migrations/new", db(authz.ActionCreateMigration, app.todo("form migration baru")))
	mux.Handle("POST /databases/{dbID}/migrations", db(authz.ActionCreateMigration, app.todo("simpan migration baru")))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}", db(authz.ActionViewMigrations, app.todo("detail + review SQL + riwayat run")))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}/edit", db(authz.ActionEditMigration, app.todo("form edit migration")))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}", db(authz.ActionEditMigration, app.todo("simpan perubahan migration")))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/execute", db(authz.ActionExecuteMigration, app.todo("eksekusi migration")))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/discard", db(authz.ActionEditMigration, app.todo("batalkan migration")))

	// Platform Admin (user, group, akses)
	mux.Handle("GET /admin", admin(app.todo("dashboard admin")))
	mux.Handle("GET /admin/users", admin(app.todo("daftar user")))
	mux.Handle("GET /admin/users/new", admin(app.todo("form user baru")))
	mux.Handle("POST /admin/users", admin(app.todo("buat user")))
	mux.Handle("GET /admin/users/{userID}", admin(app.todo("detail/edit user")))
	mux.Handle("POST /admin/users/{userID}", admin(app.todo("update user (aktif/admin/password)")))
	mux.Handle("POST /admin/users/{userID}/delete", admin(app.todo("hapus user")))
	mux.Handle("GET /admin/groups", admin(app.todo("daftar group")))
	mux.Handle("POST /admin/groups", admin(app.todo("buat group")))
	mux.Handle("GET /admin/groups/{groupID}", admin(app.todo("detail group: anggota & akses DB")))
	mux.Handle("POST /admin/groups/{groupID}", admin(app.todo("update group")))
	mux.Handle("POST /admin/groups/{groupID}/delete", admin(app.todo("hapus group")))
	mux.Handle("POST /admin/groups/{groupID}/members", admin(app.todo("tambah anggota")))
	mux.Handle("POST /admin/groups/{groupID}/members/{userID}/delete", admin(app.todo("keluarkan anggota")))
	mux.Handle("POST /admin/groups/{groupID}/access", admin(app.todo("grant/ubah level akses DB")))
	mux.Handle("POST /admin/groups/{groupID}/access/{dbID}/delete", admin(app.todo("cabut akses DB")))

	// Koneksi database (oleh admin)
	mux.Handle("GET /admin/databases", admin(app.todo("daftar koneksi DB")))
	mux.Handle("GET /admin/databases/new", admin(app.todo("form koneksi baru")))
	mux.Handle("POST /admin/databases", admin(app.todo("simpan koneksi (password dienkripsi)")))
	mux.Handle("GET /admin/databases/{dbID}", admin(app.todo("edit koneksi (password tidak ditampilkan)")))
	mux.Handle("POST /admin/databases/{dbID}", admin(app.todo("update koneksi + Invalidate pool")))
	mux.Handle("POST /admin/databases/{dbID}/test", admin(app.todo("uji koneksi")))
	mux.Handle("POST /admin/databases/{dbID}/delete", admin(app.todo("hapus/nonaktifkan koneksi")))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.ErrorRenderer(w, r, http.StatusNotFound, httpx.PublicMessage(http.StatusNotFound))
	})
	return mux
}

// todo menampilkan halaman 501 untuk route yang belum dikerjakan.
func (app *App) todo(route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.View.Render(w, r, http.StatusNotImplemented, "placeholder", view.PageData{
			Title: "Belum diimplementasikan",
			Data:  map[string]string{"Route": route},
		})
	})
}

func (app *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.CurrentUser(r.Context()); ok {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	app.View.Render(w, r, http.StatusOK, "login", view.PageData{Title: "Masuk"})
}

func (app *App) databaseList(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.CurrentUser(r.Context())
	list, err := app.Authz.AccessibleDatabases(r.Context(), u)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "databases/list", view.PageData{Title: "Database", Data: list})
}