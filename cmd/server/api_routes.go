package main

import (
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

func apiNotImplemented(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteErrorJSON(w, http.StatusNotImplemented, "not_implemented", feature+" belum diimplementasikan.", nil)
	}
}

func (app *App) registerAPIRoutes(mux *http.ServeMux) {
	login := auth.RequireLogin
	adminRead := func(h http.HandlerFunc) http.Handler {
		return login(authz.RequireAdmin(h))
	}
	adminWrite := func(event string, h http.HandlerFunc) http.Handler {
		return login(authz.RequireAdmin(audit(event, h)))
	}
	dbRead := func(a authz.Action, h http.HandlerFunc) http.Handler {
		return login(authz.RequireDB(app.Authz, a, h))
	}
	dbWrite := func(a authz.Action, event string, h http.HandlerFunc) http.Handler {
		return login(authz.RequireDB(app.Authz, a, audit(event, h)))
	}

	mux.HandleFunc("POST /api/auth/login", app.apiLogin)
	mux.Handle("POST /api/auth/logout", login(audit("auth.logout", http.HandlerFunc(app.apiLogout))))
	mux.Handle("GET /api/auth/me", login(http.HandlerFunc(app.apiMe)))

	mux.Handle("GET /api/admin/users", adminRead(app.apiAdminUsers))
	mux.Handle("POST /api/admin/users", adminWrite("admin.user.create", app.apiAdminCreateUser))
	mux.Handle("GET /api/admin/users/{userID}", adminRead(app.apiAdminUser))
	mux.Handle("PATCH /api/admin/users/{userID}", adminWrite("admin.user.update", app.apiAdminUpdateUser))
	mux.Handle("DELETE /api/admin/users/{userID}", adminWrite("admin.user.delete", app.apiAdminDeleteUser))

	mux.Handle("GET /api/admin/groups", adminRead(app.apiAdminGroups))
	mux.Handle("POST /api/admin/groups", adminWrite("admin.group.create", app.apiAdminCreateGroup))
	mux.Handle("GET /api/admin/groups/{groupID}", adminRead(app.apiAdminGroup))
	mux.Handle("PATCH /api/admin/groups/{groupID}", adminWrite("admin.group.update", app.apiAdminUpdateGroup))
	mux.Handle("DELETE /api/admin/groups/{groupID}", adminWrite("admin.group.delete", app.apiAdminDeleteGroup))
	mux.Handle("PUT /api/admin/groups/{groupID}/leader", adminWrite("admin.group.leader", app.apiAdminSetLeader))
	mux.Handle("POST /api/admin/groups/{groupID}/members", adminWrite("admin.group.member_add", app.apiAdminAddMember))
	mux.Handle("DELETE /api/admin/groups/{groupID}/members/{userID}", adminWrite("admin.group.member_remove", app.apiAdminRemoveMember))
	mux.Handle("PUT /api/admin/groups/{groupID}/access/{dbID}", adminWrite("admin.group.access_grant", app.apiAdminGrant))
	mux.Handle("DELETE /api/admin/groups/{groupID}/access/{dbID}", adminWrite("admin.group.access_revoke", app.apiAdminRevoke))

	mux.Handle("GET /api/admin/databases", adminRead(app.apiAdminConnections))
	mux.Handle("POST /api/admin/databases", adminWrite("admin.connection.create", app.apiAdminCreateConnection))
	mux.Handle("GET /api/admin/databases/{dbID}", adminRead(app.apiAdminConnection))
	mux.Handle("PATCH /api/admin/databases/{dbID}", adminWrite("admin.connection.update", app.apiAdminUpdateConnection))
	mux.Handle("DELETE /api/admin/databases/{dbID}", adminWrite("admin.connection.delete", app.apiAdminDeleteConnection))
	mux.Handle("POST /api/admin/databases/{dbID}/test", adminWrite("admin.connection.test", app.apiAdminTestConnection))

	mux.Handle("POST /api/databases/{dbID}/migrations/{migrationID}/requests",
		dbWrite(authz.ActionRequestChange, "change.create", app.apiCreateChange))
	mux.Handle("GET /api/databases/{dbID}/requests",
		dbRead(authz.ActionViewChanges, app.apiListChanges))
	mux.Handle("GET /api/databases/{dbID}/requests/{requestID}",
		dbRead(authz.ActionViewChanges, app.apiGetChange))
	mux.Handle("POST /api/databases/{dbID}/requests/{requestID}/submit",
		dbWrite(authz.ActionRequestChange, "change.submit", app.apiSubmitChange))
	mux.Handle("POST /api/databases/{dbID}/requests/{requestID}/approve",
		dbWrite(authz.ActionApproveChange, "change.approve", app.apiApproveChange))
	mux.Handle("POST /api/databases/{dbID}/requests/{requestID}/reject",
		dbWrite(authz.ActionApproveChange, "change.reject", app.apiRejectChange))
	mux.Handle("POST /api/databases/{dbID}/requests/{requestID}/enqueue",
		dbWrite(authz.ActionRequestChange, "change.enqueue", app.apiEnqueueChange))
	mux.Handle("POST /api/databases/{dbID}/requests/{requestID}/cancel",
		dbWrite(authz.ActionRequestChange, "change.cancel", app.apiCancelChange))
	mux.Handle("GET /api/databases/{dbID}/jobs",
		dbRead(authz.ActionViewJobs, app.apiListJobs))
	mux.Handle("GET /api/databases/{dbID}/jobs/{jobID}",
		dbRead(authz.ActionViewJobs, app.apiGetJob))
	mux.Handle("POST /api/databases/{dbID}/jobs/{jobID}/resume",
		dbWrite(authz.ActionManageJob, "job.resume", apiNotImplemented("Resume job")))
	mux.Handle("POST /api/databases/{dbID}/jobs/{jobID}/retry",
		dbWrite(authz.ActionManageJob, "job.retry", apiNotImplemented("Retry job")))
	mux.Handle("POST /api/databases/{dbID}/jobs/{jobID}/revert",
		dbWrite(authz.ActionManageJob, "job.revert", apiNotImplemented("Revert job")))
	mux.Handle("GET /api/databases/{dbID}/history",
		dbRead(authz.ActionViewHistory, apiNotImplemented("Riwayat migration")))
}
