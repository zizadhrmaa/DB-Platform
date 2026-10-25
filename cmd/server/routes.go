package main

import (
	"database/sql"
	"io/fs"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
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
	Connections  *admin.Connections
	Pools        dbmanager.Manager
	Authz        authz.Authorizer
	Sessions     auth.SessionManager
	LoginLimiter *auth.LoginLimiter
	Migration    *migration.Handler
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
	adminOnly := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
	db := func(a authz.Action, h http.Handler) http.Handler { return login(authz.RequireDB(app.Authz, a, h)) }

	// Authentication & Session
	mux.HandleFunc("GET /login", app.loginPage)
	mux.HandleFunc("POST /login", app.login)
	mux.Handle("POST /logout", login(http.HandlerFunc(app.logout)))

	// Database selection & viewer
	mux.Handle("GET /{$}", login(http.RedirectHandler("/databases", http.StatusSeeOther)))
	mux.Handle("GET /databases", login(http.HandlerFunc(app.databaseList)))
	mux.Handle("GET /databases/{dbID}", db(authz.ActionViewSchema, http.HandlerFunc(app.databaseShow)))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}", db(authz.ActionViewSchema, http.HandlerFunc(app.tableShow)))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}/data", db(authz.ActionViewData, http.HandlerFunc(app.tableData)))

	// Opsional (QnA 5 & 7): query console read-only. Hapus jika tidak dipakai.
	mux.Handle("GET /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("form query read-only")))
	mux.Handle("POST /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("jalankan query read-only")))

	// Migration
	mux.Handle("GET /databases/{dbID}/migrations", db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.List)))
	mux.Handle("GET /databases/{dbID}/migrations/new", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.New)))
	mux.Handle("POST /databases/{dbID}/migrations", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.Create)))
	mux.Handle("GET /databases/{dbID}/migrations/diff", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.DiffForm)))
	mux.Handle("POST /databases/{dbID}/migrations/diff", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.Diff)))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}", db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.Detail)))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}/edit", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Edit)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Update)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/execute", db(authz.ActionExecuteMigration, http.HandlerFunc(app.Migration.Execute)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/discard", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Discard)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/explain", db(authz.ActionExecuteMigration, http.HandlerFunc(app.Migration.Explain)))

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
	mux.Handle("GET /admin/databases", adminOnly(http.HandlerFunc(app.adminConnections)))
	mux.Handle("GET /admin/databases/new", adminOnly(http.HandlerFunc(app.adminNewConnection)))
	mux.Handle("POST /admin/databases", adminOnly(http.HandlerFunc(app.adminCreateConnection)))
	mux.Handle("GET /admin/databases/{dbID}", adminOnly(http.HandlerFunc(app.adminConnection)))
	mux.Handle("POST /admin/databases/{dbID}", adminOnly(http.HandlerFunc(app.adminUpdateConnection)))
	mux.Handle("POST /admin/databases/{dbID}/test", adminOnly(http.HandlerFunc(app.adminTestConnection)))
	mux.Handle("POST /admin/databases/{dbID}/delete", adminOnly(http.HandlerFunc(app.adminDeleteConnection)))

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
