package main

import (
	"crypto/subtle"
	"database/sql"
	"io/fs"
	"net/http"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/approval"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

type App struct {
	DB            *sql.DB
	View          *view.Renderer
	Admin         *admin.Store
	Connections   *admin.Connections
	Pools         dbmanager.Manager
	Authz         authz.Authorizer
	Sessions      auth.SessionManager
	LoginLimiter  *auth.LoginLimiter
	Migration     *migration.Handler
	Approvals     *approval.Store
	Jobs          *queue.Store
	MetricsToken  string
	MetricsStrict bool
	DirectExecute bool
}

func (app *App) metrics() http.Handler {
	next := obs.MetricsHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.MetricsToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(app.MetricsToken)) != 1 {
				http.NotFound(w, r)
				return
			}
		} else if app.MetricsStrict {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (app *App) directExecute() http.Handler {
	if app.DirectExecute {
		return http.HandlerFunc(app.Migration.Execute)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.ErrorRenderer(w, r, http.StatusGone,
			"Eksekusi langsung dinonaktifkan. Ajukan request perubahan lalu jalankan setelah semua leader menyetujui.")
	})
}

func (app *App) routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /metrics", app.metrics())

	login := auth.RequireLogin
	adminOnly := func(h http.Handler) http.Handler { return login(authz.RequireAdmin(h)) }
	db := func(a authz.Action, h http.Handler) http.Handler { return login(authz.RequireDB(app.Authz, a, h)) }

	mux.HandleFunc("GET /login", app.loginPage)
	mux.HandleFunc("POST /login", app.login)
	mux.Handle("POST /logout", login(http.HandlerFunc(app.logout)))

	mux.Handle("GET /{$}", login(http.RedirectHandler("/app/", http.StatusSeeOther)))
	mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusPermanentRedirect))
	mux.Handle("GET /app/", spaHandler())
	app.registerAPIRoutes(mux)

	mux.Handle("GET /databases", login(http.HandlerFunc(app.databaseList)))
	mux.Handle("GET /databases/{dbID}", db(authz.ActionViewSchema, http.HandlerFunc(app.databaseShow)))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}", db(authz.ActionViewSchema, http.HandlerFunc(app.tableShow)))
	mux.Handle("GET /databases/{dbID}/tables/{schema}/{table}/data", db(authz.ActionViewData, http.HandlerFunc(app.tableData)))

	mux.Handle("GET /api/databases", login(http.HandlerFunc(app.apiDatabaseList)))
	mux.Handle("GET /api/databases/{dbID}", db(authz.ActionViewSchema, http.HandlerFunc(app.apiDatabaseShow)))
	mux.Handle("GET /api/databases/{dbID}/tables/{schema}/{table}", db(authz.ActionViewSchema, http.HandlerFunc(app.apiTableShow)))
	mux.Handle("GET /api/databases/{dbID}/tables/{schema}/{table}/data", db(authz.ActionViewData, http.HandlerFunc(app.apiTableData)))

	mux.Handle("GET /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("form query read-only")))
	mux.Handle("POST /databases/{dbID}/query", db(authz.ActionRunReadQuery, app.todo("jalankan query read-only")))

	mux.Handle("GET /databases/{dbID}/migrations", db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.List)))
	mux.Handle("GET /databases/{dbID}/migrations/new", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.New)))
	mux.Handle("POST /databases/{dbID}/migrations", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.Create)))
	mux.Handle("GET /databases/{dbID}/migrations/diff", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.DiffForm)))
	mux.Handle("POST /databases/{dbID}/migrations/diff", db(authz.ActionCreateMigration, http.HandlerFunc(app.Migration.Diff)))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}", db(authz.ActionViewMigrations, http.HandlerFunc(app.Migration.Detail)))
	mux.Handle("GET /databases/{dbID}/migrations/{migrationID}/edit", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Edit)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Update)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/execute", db(authz.ActionExecuteMigration, app.directExecute()))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/discard", db(authz.ActionEditMigration, http.HandlerFunc(app.Migration.Discard)))
	mux.Handle("POST /databases/{dbID}/migrations/{migrationID}/explain", db(authz.ActionExecuteMigration, http.HandlerFunc(app.Migration.Explain)))

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

func (app *App) todo(route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.View.Render(w, r, http.StatusNotImplemented, "placeholder", view.PageData{
			Title: "Belum diimplementasikan",
			Data:  map[string]string{"Route": route},
		})
	})
}
