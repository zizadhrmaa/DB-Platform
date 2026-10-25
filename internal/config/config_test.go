package config

import (
	"os"
	"testing"
)

func TestConfigLoadValidation(t *testing.T) {
	origDB := os.Getenv("DATABASE_URL")
	origKey := os.Getenv("APP_SECRET_KEY")
	origEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("DATABASE_URL", origDB)
		os.Setenv("APP_SECRET_KEY", origKey)
		os.Setenv("APP_ENV", origEnv)
	}()

	os.Setenv("DATABASE_URL", "")
	os.Setenv("APP_SECRET_KEY", "")
	os.Setenv("APP_ENV", "development")

	_, err := Load()
	if err == nil {
		t.Fatal("Load tanpa DATABASE_URL dan APP_SECRET_KEY harus error")
	}

	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("APP_SECRET_KEY", "dummy-secret-key-1234567890123456")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load config valid gagal: %v", err)
	}
	if cfg.IsProduction() {
		t.Error("development tidak boleh IsProduction")
	}
	if cfg.CookieSecure() {
		t.Error("CookieSecure harus false di development")
	}

	os.Setenv("APP_ENV", "production")
	os.Setenv("ADMIN_PASSWORD", "short")
	_, err = Load()
	if err == nil {
		t.Fatal("ADMIN_PASSWORD pendek di production harus error")
	}
}
