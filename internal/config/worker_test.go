package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_SECRET_KEY", "dummy-secret-key-1234567890123456")
	t.Setenv("ADMIN_PASSWORD", "")
}

func TestWorkerConfigDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("WORKER_ENABLED", "")
	t.Setenv("WORKER_CONCURRENCY", "")
	t.Setenv("JOB_TIMEOUT", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.WorkerEnabled || c.WorkerConcurrency != 2 || c.JobTimeout != 30*time.Minute {
		t.Fatalf("default worker: enabled=%v concurrency=%d timeout=%v", c.WorkerEnabled, c.WorkerConcurrency, c.JobTimeout)
	}
}

func TestWorkerConfigFromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("WORKER_ENABLED", "false")
	t.Setenv("WORKER_CONCURRENCY", "4")
	t.Setenv("JOB_TIMEOUT", "2h")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerEnabled || c.WorkerConcurrency != 4 || c.JobTimeout != 2*time.Hour {
		t.Fatalf("worker dari env: enabled=%v concurrency=%d timeout=%v", c.WorkerEnabled, c.WorkerConcurrency, c.JobTimeout)
	}
}

func TestWorkerConfigRejectsInvalidValues(t *testing.T) {
	for _, c := range []struct{ key, val string }{
		{"WORKER_ENABLED", "mungkin"},
		{"WORKER_CONCURRENCY", "0"},
		{"WORKER_CONCURRENCY", "dua"},
		{"JOB_TIMEOUT", "-1m"},
		{"JOB_TIMEOUT", "lama"},
	} {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			setRequired(t)
			t.Setenv(c.key, c.val)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("%s=%q harus ditolak dengan pesan yang menyebut variabelnya, dapat %v", c.key, c.val, err)
			}
		})
	}
}
