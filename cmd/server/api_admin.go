package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

func (app *App) syncApprovals(ctx context.Context) {
	if app.Approvals == nil {
		return
	}
	if err := app.Approvals.SyncOpen(ctx); err != nil {
		slog.ErrorContext(ctx, "sinkronisasi approval gagal", "event", "approval_sync", "status", "failed", "err", err)
	}
}

type groupRefDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type adminUserDTO struct {
	ID              int64         `json:"id"`
	Username        string        `json:"username"`
	IsPlatformAdmin bool          `json:"is_platform_admin"`
	IsActive        bool          `json:"is_active"`
	CreatedAt       time.Time     `json:"created_at"`
	GroupCount      int           `json:"group_count"`
	Groups          []groupRefDTO `json:"groups"`
}

func toUserDTO(u admin.User) adminUserDTO {
	groups := make([]groupRefDTO, len(u.Groups))
	for i, g := range u.Groups {
		groups[i] = groupRefDTO{ID: g.ID, Name: g.Name}
	}
	return adminUserDTO{
		ID:              u.ID,
		Username:        u.Username,
		IsPlatformAdmin: u.IsPlatformAdmin,
		IsActive:        u.IsActive,
		CreatedAt:       u.CreatedAt,
		GroupCount:      u.GroupCount,
		Groups:          groups,
	}
}

func (app *App) apiAdminUsers(w http.ResponseWriter, r *http.Request) {
	list, err := app.Admin.ListUsers(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]adminUserDTO, len(list))
	for i, u := range list {
		out[i] = toUserDTO(u)
	}
	httpx.JSON(w, http.StatusOK, out)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"is_admin"`
}

func (app *App) apiAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id, err := app.Admin.CreateUser(r.Context(), strings.TrimSpace(req.Username), req.Password, req.IsAdmin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (app *App) apiAdminUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, err := app.Admin.GetUser(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserDTO(*u))
}

type patchUserRequest struct {
	IsActive *bool   `json:"is_active"`
	IsAdmin  *bool   `json:"is_admin"`
	Password *string `json:"password"`
}

func (app *App) apiAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req patchUserRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.IsActive == nil && req.IsAdmin == nil && req.Password == nil {
		httpx.Fail(w, r, httpx.Invalid("Tidak ada perubahan yang dikirim."))
		return
	}
	if req.Password != nil {
		if err := admin.ValidatePassword(*req.Password); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	actor, _ := auth.CurrentUser(ctx)
	if req.IsActive != nil || req.IsAdmin != nil {
		cur, err := app.Admin.GetUser(ctx, id)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		active, isAdmin := cur.IsActive, cur.IsPlatformAdmin
		if req.IsActive != nil {
			active = *req.IsActive
		}
		if req.IsAdmin != nil {
			isAdmin = *req.IsAdmin
		}
		if err := app.Admin.UpdateUser(ctx, actor.ID, id, active, isAdmin); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !active {
			if err := app.Sessions.RevokeAllForUser(ctx, id); err != nil {
				httpx.Fail(w, r, err)
				return
			}
			app.syncApprovals(ctx)
		}
	}
	if req.Password != nil {
		if err := app.Admin.ResetPassword(ctx, id, *req.Password); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := app.Sessions.RevokeAllForUser(ctx, id); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	u, err := app.Admin.GetUser(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserDTO(*u))
}

func (app *App) apiAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	if err := app.Admin.DeleteUser(r.Context(), actor.ID, id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

type groupDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	MemberCount  int    `json:"member_count"`
	AccessCount  int    `json:"access_count"`
	LeaderUserID *int64 `json:"leader_user_id"`
}

type memberDTO struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	IsActive bool   `json:"is_active"`
}

type accessDTO struct {
	DBID     int64  `json:"database_id"`
	DBName   string `json:"database_name"`
	DBActive bool   `json:"database_active"`
	Level    string `json:"level"`
}

type dbOptionDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

type groupDetailDTO struct {
	groupDTO
	Members    []memberDTO   `json:"members"`
	Access     []accessDTO   `json:"access"`
	Candidates []memberDTO   `json:"candidates"`
	Databases  []dbOptionDTO `json:"databases"`
}

func toMemberDTOs(list []admin.Member) []memberDTO {
	out := make([]memberDTO, len(list))
	for i, m := range list {
		out[i] = memberDTO{UserID: m.UserID, Username: m.Username, IsActive: m.IsActive}
	}
	return out
}

func (app *App) groupDetail(ctx context.Context, id int64) (*groupDetailDTO, error) {
	g, err := app.Admin.GetGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	leader, err := app.Admin.Leader(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &groupDetailDTO{
		groupDTO: groupDTO{
			ID:           g.ID,
			Name:         g.Name,
			Description:  g.Description,
			MemberCount:  g.MemberCount,
			AccessCount:  g.AccessCount,
			LeaderUserID: leader,
		},
		Members:    toMemberDTOs(g.Members),
		Candidates: toMemberDTOs(g.Candidates),
		Access:     make([]accessDTO, len(g.Access)),
		Databases:  make([]dbOptionDTO, len(g.Databases)),
	}
	for i, a := range g.Access {
		d.Access[i] = accessDTO{DBID: a.DBID, DBName: a.DBName, DBActive: a.DBActive, Level: a.Level.String()}
	}
	for i, o := range g.Databases {
		d.Databases[i] = dbOptionDTO{ID: o.ID, Name: o.Name, IsActive: o.IsActive}
	}
	return d, nil
}

func (app *App) apiAdminGroups(w http.ResponseWriter, r *http.Request) {
	list, err := app.Admin.ListGroups(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	leaders, err := app.Admin.Leaders(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]groupDTO, len(list))
	for i, g := range list {
		out[i] = groupDTO{ID: g.ID, Name: g.Name, Description: g.Description, MemberCount: g.MemberCount, AccessCount: g.AccessCount}
		if uid, ok := leaders[g.ID]; ok {
			v := uid
			out[i].LeaderUserID = &v
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (app *App) apiAdminCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id, err := app.Admin.CreateGroup(r.Context(), req.Name, req.Description)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (app *App) apiAdminGroup(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := app.groupDetail(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

type patchGroupRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (app *App) apiAdminUpdateGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req patchGroupRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cur, err := app.Admin.GetGroup(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	name, description := cur.Name, cur.Description
	if req.Name != nil {
		name = *req.Name
	}
	if req.Description != nil {
		description = *req.Description
	}
	if err := app.Admin.UpdateGroup(ctx, id, name, description); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := app.groupDetail(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (app *App) apiAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := app.Admin.DeleteGroup(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

type leaderRequest struct {
	UserID json.RawMessage `json:"user_id"`
}

func (app *App) apiAdminSetLeader(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req leaderRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.UserID) == 0 {
		httpx.Fail(w, r, httpx.Invalid("Field user_id wajib ada, isi null untuk menghapus leader."))
		return
	}
	if string(req.UserID) == "null" {
		err = app.Admin.ClearLeader(r.Context(), id)
	} else {
		var userID int64
		if jsonErr := json.Unmarshal(req.UserID, &userID); jsonErr != nil || userID <= 0 {
			httpx.Fail(w, r, httpx.Invalid("User leader tidak valid."))
			return
		}
		err = app.Admin.SetLeader(r.Context(), id, userID)
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

type memberRequest struct {
	UserID int64 `json:"user_id"`
}

func (app *App) apiAdminAddMember(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req memberRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.UserID <= 0 {
		httpx.Fail(w, r, httpx.Invalid("Pilih user yang akan ditambahkan."))
		return
	}
	added, err := app.Admin.AddMember(r.Context(), id, req.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !added {
		httpx.Fail(w, r, httpx.Conflict("User tersebut sudah menjadi anggota group ini."))
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

func (app *App) apiAdminRemoveMember(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	userID, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := app.Admin.RemoveMember(r.Context(), id, userID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

type accessRequest struct {
	Level string `json:"level"`
}

func (app *App) apiAdminGrant(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req accessRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	level, err := domain.ParseAccessLevel(req.Level)
	if err != nil {
		httpx.Fail(w, r, httpx.Invalid("Level akses harus view atau edit."))
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	if err := app.Admin.Grant(r.Context(), id, dbID, level, actor.ID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

func (app *App) apiAdminRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := app.Admin.Revoke(r.Context(), id, dbID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.syncApprovals(r.Context())
	httpx.JSON(w, http.StatusNoContent, nil)
}

type connectionDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	DBType   string `json:"db_type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	SSLMode  string `json:"sslmode"`
	IsActive bool   `json:"is_active"`
}

func toConnectionDTO(c dbmanager.ConnectionInfo) connectionDTO {
	return connectionDTO{
		ID:       c.ID,
		Name:     c.Name,
		DBType:   c.DBType,
		Host:     c.Host,
		Port:     c.Port,
		Database: c.Database,
		Username: c.Username,
		SSLMode:  c.SSLMode,
		IsActive: c.IsActive,
	}
}

func (app *App) apiAdminConnections(w http.ResponseWriter, r *http.Request) {
	list, err := app.Connections.List(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]connectionDTO, len(list))
	for i, c := range list {
		out[i] = toConnectionDTO(c)
	}
	httpx.JSON(w, http.StatusOK, out)
}

type connectionRequest struct {
	Name     string `json:"name"`
	DBType   string `json:"db_type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	SSLMode  string `json:"sslmode"`
	IsActive bool   `json:"is_active"`
}

func (app *App) apiAdminCreateConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	form := admin.ConnectionForm{
		Name:     req.Name,
		DBType:   req.DBType,
		Host:     req.Host,
		Database: req.Database,
		Username: req.Username,
		SSLMode:  req.SSLMode,
		IsActive: req.IsActive,
	}
	if req.Port != 0 {
		form.Port = strconv.Itoa(req.Port)
	}
	actor, _ := auth.CurrentUser(r.Context())
	id, err := app.Connections.Create(r.Context(), form, req.Password, actor.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (app *App) apiAdminConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c, err := app.Connections.Get(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConnectionDTO(*c))
}

type patchConnectionRequest struct {
	Name     *string `json:"name"`
	DBType   *string `json:"db_type"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Database *string `json:"database"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	SSLMode  *string `json:"sslmode"`
	IsActive *bool   `json:"is_active"`
}

func (app *App) apiAdminUpdateConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req patchConnectionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cur, err := app.Connections.Get(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	form := formFromInfo(cur)
	if req.Name != nil {
		form.Name = *req.Name
	}
	if req.DBType != nil {
		form.DBType = *req.DBType
	}
	if req.Host != nil {
		form.Host = *req.Host
	}
	if req.Port != nil {
		form.Port = strconv.Itoa(*req.Port)
	}
	if req.Database != nil {
		form.Database = *req.Database
	}
	if req.Username != nil {
		form.Username = *req.Username
	}
	if req.SSLMode != nil {
		form.SSLMode = *req.SSLMode
	}
	if req.IsActive != nil {
		form.IsActive = *req.IsActive
	}
	password := ""
	if req.Password != nil {
		password = *req.Password
	}
	if err := app.Connections.Update(ctx, id, form, password); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.Pools.Invalidate(id)
	c, err := app.Connections.Get(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConnectionDTO(*c))
}

func (app *App) apiAdminDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := app.Connections.Delete(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.Pools.Invalidate(id)
	httpx.JSON(w, http.StatusNoContent, nil)
}

func (app *App) apiAdminTestConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := app.Connections.Test(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"message": "Koneksi gagal. Periksa host, port, nama database, username, dan password.",
		})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Koneksi berhasil."})
}
