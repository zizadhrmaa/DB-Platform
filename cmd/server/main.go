// Command server menjalankan Database Management Platform.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/config"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/platformdb"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server berhenti", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := platformdb.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := platformdb.Migrate(ctx, db); err != nil {
		return err
	}

	// Cek key sejak start supaya salah konfigurasi langsung ketahuan.
	secrets, err := dbmanager.NewSecretBoxFromBase64(cfg.SecretKeyB64)
	if err != nil {
		return err
	}
	pools := dbmanager.NewPoolManager(db, secrets)
	defer pools.Close()

	if cfg.AdminPassword != "" {
		hash, err := auth.HashPassword(cfg.AdminPassword)
		if err != nil {
			return err
		}
		created, err := platformdb.EnsureBootstrapAdmin(ctx, db, cfg.AdminUsername, hash)
		if err != nil {
			return err
		}
		if created {
			slog.Info("platform admin awal dibuat", "username", cfg.AdminUsername)
		}
	}

	renderer, err := view.New(web.FS)
	if err != nil {
		return err
	}
	httpx.ErrorRenderer = renderer.ErrorPage

	sessions := auth.NewDBSessionManager(db, auth.DefaultSessionConfig(cfg.CookieSecure()))
	go sessions.RunCleanup(ctx, time.Hour)

	az := &authz.Service{DB: db}
	migrations := &migration.Store{DB: db, Targets: pools, Authz: az}
	// Bereskan migration yang tertinggal running kalau server sebelumnya mati.
	if err := migrations.Recover(ctx); err != nil {
		slog.Warn("sebagian migration yang terputus belum bisa dibereskan", "err", err)
	}

	app := &App{
		DB:           db,
		View:         renderer,
		Admin:        &admin.Store{DB: db},
		Connections:  &admin.Connections{DB: db, Secrets: secrets},
		Pools:        pools,
		Authz:        az,
		Sessions:     sessions,
		LoginLimiter: auth.NewLoginLimiter(),
		Migration:    &migration.Handler{Service: migrations, Authz: az, View: renderer},
	}

	// Urutan dari luar: Recover, SecurityHeaders, RequestLog, Session, CSRF.
	mws := []func(http.Handler) http.Handler{httpx.Recover, httpx.SecurityHeaders, httpx.RequestLog, sessions.Middleware}
	if cfg.DevAuthUserID != 0 {
		slog.Warn("DEV_AUTH_USER_ID aktif, semua request login sebagai user ini", "user_id", cfg.DevAuthUserID)
		mws = append(mws, auth.DevAuthMiddleware(db, cfg.DevAuthUserID))
	}
	mws = append(mws, sessions.CSRFMiddleware)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.Chain(app.routes(), mws...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second, // eksekusi migration bisa lama
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server berjalan", "addr", cfg.Addr, "env", cfg.Env)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutdown")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}
