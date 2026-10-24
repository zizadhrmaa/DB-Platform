// Package config membaca konfigurasi dari environment variable.
package config

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	Env           string // development atau production
	Addr          string
	DatabaseURL   string
	SecretKeyB64  string // key AES untuk password DB target
	AdminUsername string
	AdminPassword string
	DevAuthUserID int64 // 0 berarti nonaktif
}

func (c Config) IsProduction() bool { return c.Env == "production" }

// Browser tidak mengirim cookie Secure lewat http://localhost, jadi hanya
// dinyalakan di production.
func (c Config) CookieSecure() bool { return c.IsProduction() }

func Load() (Config, error) {
	c := Config{
		Env:           getenv("APP_ENV", "development"),
		Addr:          getenv("APP_ADDR", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		SecretKeyB64:  os.Getenv("APP_SECRET_KEY"),
		AdminUsername: getenv("ADMIN_USERNAME", "admin"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
	}
	if v := os.Getenv("DEV_AUTH_USER_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return c, errors.New("config: DEV_AUTH_USER_ID tidak valid")
		}
		c.DevAuthUserID = id
	}

	var errs []error
	if c.Env != "development" && c.Env != "production" {
		errs = append(errs, errors.New("APP_ENV harus development atau production"))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL wajib diisi"))
	}
	if c.SecretKeyB64 == "" {
		errs = append(errs, errors.New("APP_SECRET_KEY wajib diisi (openssl rand -base64 32)"))
	}
	if c.IsProduction() && c.DevAuthUserID != 0 {
		errs = append(errs, errors.New("DEV_AUTH_USER_ID tidak boleh dipakai di production"))
	}
	if c.IsProduction() && len(c.AdminPassword) > 0 && len(c.AdminPassword) < 12 {
		errs = append(errs, errors.New("ADMIN_PASSWORD minimal 12 karakter di production"))
	}
	return c, errors.Join(errs...)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}