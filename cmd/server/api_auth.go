package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meResponse struct {
	ID              int64  `json:"id"`
	Username        string `json:"username"`
	IsPlatformAdmin bool   `json:"is_platform_admin"`
	CSRFToken       string `json:"csrf_token"`
}

func usernameTag(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return hex.EncodeToString(sum[:6])
}

func (app *App) apiLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		httpx.Fail(w, r, httpx.Invalid("Username dan password wajib diisi."))
		return
	}

	ip := clientIP(r)
	tag := usernameTag(username)
	if wait := app.LoginLimiter.Attempt(username, ip); wait > 0 {
		slog.WarnContext(r.Context(), "login",
			"event", "login_rate_limited", "status", "failed", "username_hash", tag, "client_ip", ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		httpx.WriteErrorJSON(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("Terlalu banyak percobaan login. Coba lagi dalam %d menit.", int(math.Ceil(wait.Minutes()))), nil)
		return
	}

	u, err := auth.Authenticate(r.Context(), app.DB, username, req.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		slog.WarnContext(r.Context(), "login",
			"event", "login_failed", "status", "failed", "username_hash", tag, "client_ip", ip)
		httpx.WriteErrorJSON(w, http.StatusUnauthorized, "invalid_credentials", "Username atau password salah.", nil)
		return
	case errors.Is(err, auth.ErrBusy):
		app.LoginLimiter.Cancel(username, ip)
		w.Header().Set("Retry-After", "5")
		httpx.WriteErrorJSON(w, http.StatusServiceUnavailable, "unavailable", "Server sedang sibuk. Coba lagi beberapa detik lagi.", nil)
		return
	case err != nil:
		app.LoginLimiter.Cancel(username, ip)
		httpx.Error(w, r, err)
		return
	}

	if err := app.Sessions.Login(w, r, u.ID); err != nil {
		app.LoginLimiter.Cancel(username, ip)
		httpx.Error(w, r, err)
		return
	}
	app.LoginLimiter.Succeeded(username, ip)
	slog.InfoContext(r.Context(), "login",
		"event", "login_success", "status", "success", "actor", u.Username, "client_ip", ip)
	httpx.JSON(w, http.StatusOK, meResponse{ID: u.ID, Username: u.Username, IsPlatformAdmin: u.IsPlatformAdmin})
}

func (app *App) apiLogout(w http.ResponseWriter, r *http.Request) {
	if err := app.Sessions.Logout(w, r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

func (app *App) apiMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.CurrentUser(r.Context())
	httpx.JSON(w, http.StatusOK, meResponse{
		ID:              u.ID,
		Username:        u.Username,
		IsPlatformAdmin: u.IsPlatformAdmin,
		CSRFToken:       auth.CSRFToken(r.Context()),
	})
}
