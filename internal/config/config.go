package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env           string
	Addr          string
	DatabaseURL   string
	SecretKeyB64  string
	AdminUsername string
	AdminPassword string
	DevAuthUserID int64
	MetricsToken  string

	// Worker antrean migration.
	WorkerEnabled     bool          // WORKER_ENABLED, default true
	WorkerConcurrency int           // WORKER_CONCURRENCY, default 2
	JobTimeout        time.Duration // JOB_TIMEOUT, default 30m
}

func (c Config) IsProduction() bool { return c.Env == "production" }

func (c Config) CookieSecure() bool { return c.IsProduction() }

func Load() (Config, error) {
	c := Config{
		Env:           getenv("APP_ENV", "development"),
		Addr:          getenv("APP_ADDR", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		SecretKeyB64:  os.Getenv("APP_SECRET_KEY"),
		AdminUsername: getenv("ADMIN_USERNAME", "admin"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		MetricsToken:  os.Getenv("METRICS_TOKEN"),
	}
	if v := os.Getenv("DEV_AUTH_USER_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return c, errors.New("config: DEV_AUTH_USER_ID tidak valid")
		}
		c.DevAuthUserID = id
	}

	var errs []error
	errs = append(errs, c.loadWorker()...)
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

func (c *Config) loadWorker() []error {
	var errs []error
	c.WorkerEnabled = true
	if v := os.Getenv("WORKER_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, errors.New("WORKER_ENABLED harus true atau false"))
		}
		c.WorkerEnabled = b
	}
	c.WorkerConcurrency = 2
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, errors.New("WORKER_CONCURRENCY harus bilangan bulat minimal 1"))
		}
		c.WorkerConcurrency = n
	}
	c.JobTimeout = 30 * time.Minute
	if v := os.Getenv("JOB_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, errors.New("JOB_TIMEOUT harus durasi positif, misalnya 30m atau 2h"))
		}
		c.JobTimeout = d
	}
	return errs
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
