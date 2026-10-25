package main

import (
	"database/sql"
	"io/fs"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

// App menyimpan dependensi bersama yang dipakai handler.
type App struct {
	DB           *sql.DB
	View         *view.Renderer
	Admin        *admin.Store
	Authz        authz.Authorizer
	Sessions     auth.SessionManager
	LoginLimiter *auth.LoginLimiter
	Migration    *migration.Handler
}

func (app *App) routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	login := auth.RequireLogin
	adminOnly := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
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

	// Opsional (QnA 5 & 7): query console read-only. 
	mux.Handle("GET /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("form query read-only")))
	mux.Handle("POST /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("jalankan query read-only")))
	
	// Migration
	if app.Migration != nil {
		mux.Handle("GET /databases/{dbID}/migrations",
			db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.List)))
		mux.Handle("GET /databases/{dbID}/migrations/new",
			db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.New)))
		mux.Handle("POST /databases/{dbID}/migrations",
			db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.Create)))
		mux.Handle("GET /databases/{dbID}/migrations/{migrationID}",
			db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.Detail)))
		mux.Handle("GET /databases/{dbID}/migrations/{migrationID}/edit",
			db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Edit)))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}",
			db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Update)))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/execute",
			db(authz.ActionExecuteMigration, http.HandlerFunc(app.Migration.Execute)))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/discard",
			db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Discard)))
	} else {
		mux.Handle("GET /databases/{dbID}/migrations",
			db(authz.ActionViewMigrations, app.todo("riwayat migration")))
		mux.Handle("GET /databases/{dbID}/migrations/new",
			db(authz.ActionCreateMigration, app.todo("form migration baru")))
		mux.Handle("POST /databases/{dbID}/migrations",
			db(authz.ActionCreateMigration, app.todo("simpan migration baru")))
		mux.Handle("GET /databases/{dbID}/migrations/{migrationID}",
			db(authz.ActionViewMigrations, app.todo("detail migration")))
		mux.Handle("GET /databases/{dbID}/migrations/{migrationID}/edit",
			db(authz.ActionEditMigration, app.todo("form edit migration")))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}",
			db(authz.ActionEditMigration, app.todo("update migration")))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/execute",
			db(authz.ActionExecuteMigration, app.todo("execute migration")))
		mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/discard",
			db(authz.ActionEditMigration, app.todo("discard migration")))
	}

	// Platform Admin (user, group, akses)
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
