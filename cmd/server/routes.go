package main

import (
	"database/sql"
	"io/fs"
	"net/http"

<<<<<<< HEAD
	"github.com/zizadhrmaa/DB-Platform/internal/admin"
=======
>>>>>>> 06b0836237af9f29768cd684bcea3083116a34fc
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

// App menyimpan dependensi bersama yang dipakai handler.
type App struct {
	DB           *sql.DB
	View         *view.Renderer
<<<<<<< HEAD
	Admin        *admin.Store
=======
>>>>>>> 06b0836237af9f29768cd684bcea3083116a34fc
	Authz        authz.Authorizer
	Sessions     auth.SessionManager
	LoginLimiter *auth.LoginLimiter
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
<<<<<<< HEAD
	adminOnly := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
=======
	admin := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
>>>>>>> 06b0836237af9f29768cd684bcea3083116a34fc
	db := func(a authz.Action, h http.Handler) http.Handler { return login(authz.RequireDB(app.Authz, a, h)) }

	// Authentication & Session
	mux.HandleFunc("GET /login", app.loginPage)
	mux.HandleFunc("POST /login", app.login)
	mux.Handle("POST /logout", login(http.HandlerFunc(app.logout)))

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
<<<<<<< HEAD
	mux.Handle("GET /admin", adminOnly(http.HandlerFunc(app.adminHome)))
	mux.Handle("GET /admin/users", adminOnly(http.HandlerFunc(app.adminUsers)))
	mux.Handle("GET /admin/users/new", adminOnly(http.HandlerFunc(app.adminNewUser)))
	mux.Handle("POST /admin/users", adminOnly(http.HandlerFunc(app.adminCreateUser)))
	mux.Handle("GET /admin/users/{userID}", adminOnly(http.HandlerFunc(app.adminUser)))
	mux.Handle("POST /admin/users/{userID}", adminOnly(http.HandlerFunc(app.adminUpdateUser)))
	mux.Handle("POST /admin/users/{userID}/delete", adminOnly(http.HandlerFunc(app.adminDeleteUser)))
	mux.Handle("GET /admin/groups", adminOnly(http.HandlerFunc(app.adminGroups)))
	mux.Handle("POST /admin/groups", adminOnly(http.HandlerFunc(app.adminCreateGroup)))
	mux.Handle("GET /admin/groups/{groupID}", adminOnly(http.HandlerFunc(app.adminGroup)))
	mux.Handle("POST /admin/groups/{groupID}", adminOnly(app.groupAction(app.adminUpdateGroup)))
	mux.Handle("POST /admin/groups/{groupID}/delete", adminOnly(app.groupAction(app.adminDeleteGroup)))
	mux.Handle("POST /admin/groups/{groupID}/members", adminOnly(app.groupAction(app.adminAddMember)))
	mux.Handle("POST /admin/groups/{groupID}/members/{userID}/delete", adminOnly(app.groupAction(app.adminRemoveMember)))
	mux.Handle("POST /admin/groups/{groupID}/access", adminOnly(app.groupAction(app.adminGrant)))
	mux.Handle("POST /admin/groups/{groupID}/access/{dbID}/delete", adminOnly(app.groupAction(app.adminRevoke)))

	// Koneksi database (oleh admin)
	mux.Handle("GET /admin/databases", adminOnly(app.todo("daftar koneksi DB")))
	mux.Handle("GET /admin/databases/new", adminOnly(app.todo("form koneksi baru")))
	mux.Handle("POST /admin/databases", adminOnly(app.todo("simpan koneksi (password dienkripsi)")))
	mux.Handle("GET /admin/databases/{dbID}", adminOnly(app.todo("edit koneksi (password tidak ditampilkan)")))
	mux.Handle("POST /admin/databases/{dbID}", adminOnly(app.todo("update koneksi + Invalidate pool")))
	mux.Handle("POST /admin/databases/{dbID}/test", adminOnly(app.todo("uji koneksi")))
	mux.Handle("POST /admin/databases/{dbID}/delete", adminOnly(app.todo("hapus/nonaktifkan koneksi")))
=======
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
>>>>>>> 06b0836237af9f29768cd684bcea3083116a34fc

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

func (app *App) databaseList(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.CurrentUser(r.Context())
	list, err := app.Authz.AccessibleDatabases(r.Context(), u)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "databases/list", view.PageData{Title: "Database", Data: list})
}
