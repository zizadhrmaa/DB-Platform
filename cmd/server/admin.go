package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
)

// Pesan sukses dikirim lewat ?ok=<kode> setelah redirect. Yang ditampilkan
// hanya teks dari map ini, jadi isi query string tidak pernah dirender.
var adminFlash = map[string]string{
	"user-created":   "User berhasil dibuat.",
	"user-updated":   "Perubahan user disimpan.",
	"password-reset": "Password berhasil diganti dan semua session user ini sudah dicabut.",
	"user-deleted":   "User berhasil dihapus.",
	"group-created":  "Group berhasil dibuat.",
	"group-updated":  "Perubahan group disimpan.",
	"group-deleted":  "Group berhasil dihapus.",
	"member-added":   "Anggota berhasil ditambahkan.",
	"member-exists":  "User tersebut sudah menjadi anggota group ini.",
	"member-removed": "Anggota berhasil dikeluarkan.",
	"access-saved":   "Akses database disimpan.",
	"access-revoked": "Akses database dicabut.",
}

func flashFrom(r *http.Request) string {
	return adminFlash[r.URL.Query().Get("ok")]
}

func redirectOK(w http.ResponseWriter, r *http.Request, path, code string) {
	http.Redirect(w, r, path+"?ok="+code, http.StatusSeeOther)
}

// formErrorStatus mengembalikan pesan untuk form kalau err adalah
// admin.FormError.
func formErrorStatus(err error) (string, int, bool) {
	var fe *admin.FormError
	if !errors.As(err, &fe) {
		return "", 0, false
	}
	if errors.Is(err, domain.ErrConflict) {
		return fe.Msg, http.StatusConflict, true
	}
	return fe.Msg, http.StatusUnprocessableEntity, true
}

func formID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PostFormValue(name), 10, 64)
	return id, err == nil && id > 0
}

func (app *App) adminHome(w http.ResponseWriter, r *http.Request) {
	c, err := app.Admin.Counts(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "admin/index", view.PageData{Title: "Platform Admin", Data: c})
}

// User

type userForm struct {
	Username string
	IsAdmin  bool
}

type userPage struct {
	User   *admin.User
	IsSelf bool
}

func (app *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	list, err := app.Admin.ListUsers(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "admin/users", view.PageData{
		Title: "User",
		Flash: flashFrom(r),
		Data:  list,
	})
}

func (app *App) adminNewUser(w http.ResponseWriter, r *http.Request) {
	app.View.Render(w, r, http.StatusOK, "admin/user_new", view.PageData{Title: "User baru", Data: userForm{}})
}

func (app *App) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	form := userForm{
		Username: strings.TrimSpace(r.PostFormValue("username")),
		IsAdmin:  r.PostFormValue("is_admin") == "on",
	}
	id, err := app.Admin.CreateUser(r.Context(), form.Username, r.PostFormValue("password"), form.IsAdmin)
	if msg, status, ok := formErrorStatus(err); ok {
		app.View.Render(w, r, status, "admin/user_new", view.PageData{Title: "User baru", Error: msg, Data: form})
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	redirectOK(w, r, fmt.Sprintf("/admin/users/%d", id), "user-created")
}

func (app *App) adminUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.renderUser(w, r, http.StatusOK, id, flashFrom(r), "")
}

func (app *App) renderUser(w http.ResponseWriter, r *http.Request, status int, id int64, flash, errMsg string) {
	u, err := app.Admin.GetUser(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	app.View.Render(w, r, status, "admin/user", view.PageData{
		Title: "User " + u.Username,
		Flash: flash,
		Error: errMsg,
		Data:  userPage{User: u, IsSelf: actor.ID == u.ID},
	})
}

// adminUpdateUser menangani dua form di halaman detail user: status
// (aktif/admin) dan reset password.
func (app *App) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, _ := auth.CurrentUser(r.Context())

	var code string
	switch r.PostFormValue("aksi") {
	case "status":
		isActive := r.PostFormValue("is_active") == "on"
		err = app.Admin.UpdateUser(r.Context(), actor.ID, id, isActive, r.PostFormValue("is_admin") == "on")
		if err == nil && !isActive {
			err = app.Sessions.RevokeAllForUser(r.Context(), id)
		}
		code = "user-updated"
	case "password":
		err = app.Admin.ResetPassword(r.Context(), id, r.PostFormValue("password"))
		if err == nil {
			err = app.Sessions.RevokeAllForUser(r.Context(), id)
		}
		code = "password-reset"
	default:
		err = fmt.Errorf("aksi tidak dikenal: %w", domain.ErrInvalidInput)
	}

	if msg, status, ok := formErrorStatus(err); ok {
		app.renderUser(w, r, status, id, "", msg)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	redirectOK(w, r, fmt.Sprintf("/admin/users/%d", id), code)
}

func (app *App) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "userID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if r.PostFormValue("confirm") != "on" {
		app.renderUser(w, r, http.StatusUnprocessableEntity, id, "", "Centang konfirmasi sebelum menghapus user.")
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	err = app.Admin.DeleteUser(r.Context(), actor.ID, id)
	if msg, status, ok := formErrorStatus(err); ok {
		app.renderUser(w, r, status, id, "", msg)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	redirectOK(w, r, "/admin/users", "user-deleted")
}

// Group

type groupForm struct {
	Name        string
	Description string
}

type groupsPage struct {
	Groups []admin.Group
	Form   groupForm
}

func (app *App) adminGroups(w http.ResponseWriter, r *http.Request) {
	app.renderGroups(w, r, http.StatusOK, groupForm{}, flashFrom(r), "")
}

func (app *App) renderGroups(w http.ResponseWriter, r *http.Request, status int, form groupForm, flash, errMsg string) {
	list, err := app.Admin.ListGroups(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, status, "admin/groups", view.PageData{
		Title: "Group",
		Flash: flash,
		Error: errMsg,
		Data:  groupsPage{Groups: list, Form: form},
	})
}

func (app *App) adminCreateGroup(w http.ResponseWriter, r *http.Request) {
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	form := groupForm{Name: r.PostFormValue("name"), Description: r.PostFormValue("description")}
	id, err := app.Admin.CreateGroup(r.Context(), form.Name, form.Description)
	if msg, status, ok := formErrorStatus(err); ok {
		app.renderGroups(w, r, status, form, "", msg)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	redirectOK(w, r, fmt.Sprintf("/admin/groups/%d", id), "group-created")
}

func (app *App) adminGroup(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "groupID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.renderGroup(w, r, http.StatusOK, id, flashFrom(r), "")
}

func (app *App) renderGroup(w http.ResponseWriter, r *http.Request, status int, id int64, flash, errMsg string) {
	g, err := app.Admin.GetGroup(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, status, "admin/group", view.PageData{
		Title: "Group " + g.Name,
		Flash: flash,
		Error: errMsg,
		Data:  g,
	})
}

// groupAction membaca groupID dari path dan mem-parse form, lalu
// menjalankan fn. FormError dirender di halaman group, error lain lewat
// httpx.Error.
func (app *App) groupAction(fn func(r *http.Request, groupID int64) (redirect, code string, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r, "groupID")
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
			httpx.Error(w, r, err)
			return
		}
		redirect, code, err := fn(r, id)
		if msg, status, ok := formErrorStatus(err); ok {
			app.renderGroup(w, r, status, id, "", msg)
			return
		}
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		redirectOK(w, r, redirect, code)
	}
}

func groupPath(id int64) string { return fmt.Sprintf("/admin/groups/%d", id) }

func (app *App) adminUpdateGroup(r *http.Request, id int64) (string, string, error) {
	err := app.Admin.UpdateGroup(r.Context(), id, r.PostFormValue("name"), r.PostFormValue("description"))
	return groupPath(id), "group-updated", err
}

func (app *App) adminDeleteGroup(r *http.Request, id int64) (string, string, error) {
	if r.PostFormValue("confirm") != "on" {
		return "", "", &admin.FormError{Msg: "Centang konfirmasi sebelum menghapus group.", Err: domain.ErrInvalidInput}
	}
	return "/admin/groups", "group-deleted", app.Admin.DeleteGroup(r.Context(), id)
}

func (app *App) adminAddMember(r *http.Request, id int64) (string, string, error) {
	userID, ok := formID(r, "user_id")
	if !ok {
		return "", "", &admin.FormError{Msg: "Pilih user yang akan ditambahkan.", Err: domain.ErrInvalidInput}
	}
	added, err := app.Admin.AddMember(r.Context(), id, userID)
	if !added {
		return groupPath(id), "member-exists", err
	}
	return groupPath(id), "member-added", err
}

func (app *App) adminRemoveMember(r *http.Request, id int64) (string, string, error) {
	userID, err := httpx.PathID(r, "userID")
	if err != nil {
		return "", "", err
	}
	return groupPath(id), "member-removed", app.Admin.RemoveMember(r.Context(), id, userID)
}

func (app *App) adminGrant(r *http.Request, id int64) (string, string, error) {
	dbID, ok := formID(r, "db_id")
	if !ok {
		return "", "", &admin.FormError{Msg: "Pilih database yang akan diberi akses.", Err: domain.ErrInvalidInput}
	}
	level, err := domain.ParseAccessLevel(r.PostFormValue("level"))
	if err != nil {
		return "", "", &admin.FormError{Msg: "Pilih level akses View Only atau View + Edit.", Err: domain.ErrInvalidInput}
	}
	actor, _ := auth.CurrentUser(r.Context())
	return groupPath(id), "access-saved", app.Admin.Grant(r.Context(), id, dbID, level, actor.ID)
}

func (app *App) adminRevoke(r *http.Request, id int64) (string, string, error) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		return "", "", err
	}
	return groupPath(id), "access-revoked", app.Admin.Revoke(r.Context(), id, dbID)
}
