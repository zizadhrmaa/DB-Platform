package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
)

type loginForm struct {
	Username string
}

func (app *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.CurrentUser(r.Context()); ok {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	app.View.Render(w, r, http.StatusOK, "login", view.PageData{Title: "Masuk"})
}

func (app *App) login(w http.ResponseWriter, r *http.Request) {
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	form := loginForm{Username: username}

	if username == "" || password == "" {
		app.View.Render(w, r, http.StatusUnprocessableEntity, "login", view.PageData{
			Title: "Masuk",
			Error: "Username dan password wajib diisi.",
			Data:  form,
		})
		return
	}

	ip := clientIP(r)
	if wait := app.LoginLimiter.Wait(username, ip); wait > 0 {
		minutes := int(math.Ceil(wait.Minutes()))
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		app.View.Render(w, r, http.StatusTooManyRequests, "login", view.PageData{
			Title: "Masuk",
			Error: fmt.Sprintf("Terlalu banyak percobaan login. Coba lagi dalam %d menit.", minutes),
			Data:  form,
		})
		return
	}

	u, err := auth.Authenticate(r.Context(), app.DB, username, password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		app.LoginLimiter.Failed(username, ip)
		app.View.Render(w, r, http.StatusUnauthorized, "login", view.PageData{
			Title: "Masuk",
			Error: "Username atau password salah.",
			Data:  form,
		})
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := app.Sessions.Login(w, r, u.ID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.LoginLimiter.Succeeded(username)
	http.Redirect(w, r, "/databases", http.StatusSeeOther)
}

// clientIP sengaja tidak membaca X-Forwarded-For karena header itu bisa
// diisi bebas oleh client. Kalau nanti dipasang di belakang reverse proxy,
// bagian ini perlu disesuaikan.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (app *App) logout(w http.ResponseWriter, r *http.Request) {
	if err := app.Sessions.Logout(w, r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
