package obs

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PoolStatProvider menyediakan statistik pool koneksi database.
type PoolStatProvider interface {
	Stats() map[int64]sql.DBStats
}

var (
	poolProviderMu sync.RWMutex
	poolProvider   PoolStatProvider
)

// RegisterPoolProvider mendaftarkan provider statistik pool koneksi untuk metrik Prometheus.
func RegisterPoolProvider(p PoolStatProvider) {
	poolProviderMu.Lock()
	defer poolProviderMu.Unlock()
	poolProvider = p
}

// Registry metrik Prometheus sederhana tanpa dependensi eksternal berat.
type MetricsRegistry struct {
	mu sync.Mutex

	// Queue / Migration metrics
	jobsTotal       map[string]int64 // key: db|status
	jobRetriesTotal map[string]int64 // key: db
	batchesTotal    map[string]int64 // key: db
	rowsTotal       map[string]int64 // key: db

	// Duration counters & sums for histograms/summaries
	queueWaitSecondsSum  atomic.Int64 // microseconds
	queueWaitCount       atomic.Int64
	lockWaitSecondsSum   atomic.Int64 // microseconds
	lockWaitCount        atomic.Int64
	migrationDurationSum atomic.Int64 // microseconds
	migrationCount       atomic.Int64
}

var DefaultMetrics = NewMetricsRegistry()

func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		jobsTotal:       make(map[string]int64),
		jobRetriesTotal: make(map[string]int64),
		batchesTotal:    make(map[string]int64),
		rowsTotal:       make(map[string]int64),
	}
}

// RecordJobStatus mencatat total job berdasarkan status dan database (label berkardinalitas rendah).
func RecordJobStatus(db, status string) {
	DefaultMetrics.mu.Lock()
	defer DefaultMetrics.mu.Unlock()
	key := fmt.Sprintf("%s|%s", sanitizeLabel(db), sanitizeLabel(status))
	DefaultMetrics.jobsTotal[key]++
}

// RecordJobRetry mencatat retry job pada database tertentu.
func RecordJobRetry(db string) {
	DefaultMetrics.mu.Lock()
	defer DefaultMetrics.mu.Unlock()
	DefaultMetrics.jobRetriesTotal[sanitizeLabel(db)]++
}

// RecordQueueWait mencatat waktu tunggu dalam antrean queue.
func RecordQueueWait(d time.Duration) {
	DefaultMetrics.queueWaitSecondsSum.Add(d.Microseconds())
	DefaultMetrics.queueWaitCount.Add(1)
}

// RecordLockWait mencatat waktu tunggu perolehan lock database.
func RecordLockWait(d time.Duration) {
	DefaultMetrics.lockWaitSecondsSum.Add(d.Microseconds())
	DefaultMetrics.lockWaitCount.Add(1)
}

// RecordMigrationDuration mencatat durasi penyelesaian migration.
func RecordMigrationDuration(d time.Duration) {
	DefaultMetrics.migrationDurationSum.Add(d.Microseconds())
	DefaultMetrics.migrationCount.Add(1)
}

// RecordBatchProcessed mencatat batch dan jumlah baris yang berhasil di-commit.
func RecordBatchProcessed(db string, rows int) {
	DefaultMetrics.mu.Lock()
	defer DefaultMetrics.mu.Unlock()
	d := sanitizeLabel(db)
	DefaultMetrics.batchesTotal[d]++
	DefaultMetrics.rowsTotal[d] += int64(rows)
}

func sanitizeLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// MetricsHandler mengembalikan http.Handler yang menyajikan metrik dalam format eksposisi Prometheus.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var sb strings.Builder

		sb.WriteString("# HELP dbp_jobs_total Jumlah total job migration berdasarkan database dan status.\n")
		sb.WriteString("# TYPE dbp_jobs_total counter\n")
		DefaultMetrics.mu.Lock()
		for k, v := range DefaultMetrics.jobsTotal {
			parts := strings.Split(k, "|")
			if len(parts) == 2 {
				fmt.Fprintf(&sb, "dbp_jobs_total{db=\"%s\",status=\"%s\"} %d\n", parts[0], parts[1], v)
			}
		}

		sb.WriteString("\n# HELP dbp_job_retries_total Jumlah percobaan ulang (retry) eksekusi job.\n")
		sb.WriteString("# TYPE dbp_job_retries_total counter\n")
		for k, v := range DefaultMetrics.jobRetriesTotal {
			fmt.Fprintf(&sb, "dbp_job_retries_total{db=\"%s\"} %d\n", k, v)
		}

		sb.WriteString("\n# HELP dbp_batches_processed_total Jumlah batch migration yang berhasil diproses.\n")
		sb.WriteString("# TYPE dbp_batches_processed_total counter\n")
		for k, v := range DefaultMetrics.batchesTotal {
			fmt.Fprintf(&sb, "dbp_batches_processed_total{db=\"%s\"} %d\n", k, v)
		}

		sb.WriteString("\n# HELP dbp_rows_processed_total Jumlah row data yang berhasil diproses dalam mode batch.\n")
		sb.WriteString("# TYPE dbp_rows_processed_total counter\n")
		for k, v := range DefaultMetrics.rowsTotal {
			fmt.Fprintf(&sb, "dbp_rows_processed_total{db=\"%s\"} %d\n", k, v)
		}
		DefaultMetrics.mu.Unlock()

		// Queue wait summary
		qCount := DefaultMetrics.queueWaitCount.Load()
		qSumSec := float64(DefaultMetrics.queueWaitSecondsSum.Load()) / 1e6
		sb.WriteString("\n# HELP dbp_queue_wait_seconds Durasi waktu tunggu job dalam queue sebelum dieksekusi.\n")
		sb.WriteString("# TYPE dbp_queue_wait_seconds summary\n")
		fmt.Fprintf(&sb, "dbp_queue_wait_seconds_sum %f\n", qSumSec)
		fmt.Fprintf(&sb, "dbp_queue_wait_seconds_count %d\n", qCount)

		// Lock wait summary
		lCount := DefaultMetrics.lockWaitCount.Load()
		lSumSec := float64(DefaultMetrics.lockWaitSecondsSum.Load()) / 1e6
		sb.WriteString("\n# HELP dbp_lock_wait_seconds Durasi waktu tunggu perolehan lock database.\n")
		sb.WriteString("# TYPE dbp_lock_wait_seconds summary\n")
		fmt.Fprintf(&sb, "dbp_lock_wait_seconds_sum %f\n", lSumSec)
		fmt.Fprintf(&sb, "dbp_lock_wait_seconds_count %d\n", lCount)

		// Migration duration summary
		mCount := DefaultMetrics.migrationCount.Load()
		mSumSec := float64(DefaultMetrics.migrationDurationSum.Load()) / 1e6
		sb.WriteString("\n# HELP dbp_migration_duration_seconds Durasi total eksekusi migration.\n")
		sb.WriteString("# TYPE dbp_migration_duration_seconds summary\n")
		fmt.Fprintf(&sb, "dbp_migration_duration_seconds_sum %f\n", mSumSec)
		fmt.Fprintf(&sb, "dbp_migration_duration_seconds_count %d\n", mCount)

		// Database Connection Pool Stats
		poolProviderMu.RLock()
		prov := poolProvider
		poolProviderMu.RUnlock()

		if prov != nil {
			statsMap := prov.Stats()
			sb.WriteString("\n# HELP dbp_pool_open_connections Jumlah koneksi terbuka ke database target.\n")
			sb.WriteString("# TYPE dbp_pool_open_connections gauge\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_open_connections{db_id=\"%d\"} %d\n", id, st.OpenConnections)
			}

			sb.WriteString("\n# HELP dbp_pool_in_use_connections Jumlah koneksi aktif yang sedang dipakai.\n")
			sb.WriteString("# TYPE dbp_pool_in_use_connections gauge\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_in_use_connections{db_id=\"%d\"} %d\n", id, st.InUse)
			}

			sb.WriteString("\n# HELP dbp_pool_idle_connections Jumlah koneksi idle di pool.\n")
			sb.WriteString("# TYPE dbp_pool_idle_connections gauge\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_idle_connections{db_id=\"%d\"} %d\n", id, st.Idle)
			}

			sb.WriteString("\n# HELP dbp_pool_max_open_connections Konfigurasi batas maksimal koneksi terbuka.\n")
			sb.WriteString("# TYPE dbp_pool_max_open_connections gauge\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_max_open_connections{db_id=\"%d\"} %d\n", id, st.MaxOpenConnections)
			}

			sb.WriteString("\n# HELP dbp_pool_wait_count_total Jumlah antrean menunggu koneksi database.\n")
			sb.WriteString("# TYPE dbp_pool_wait_count_total counter\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_wait_count_total{db_id=\"%d\"} %d\n", id, st.WaitCount)
			}

			sb.WriteString("\n# HELP dbp_pool_wait_duration_seconds_total Total waktu menunggu koneksi tersedia.\n")
			sb.WriteString("# TYPE dbp_pool_wait_duration_seconds_total counter\n")
			for id, st := range statsMap {
				fmt.Fprintf(&sb, "dbp_pool_wait_duration_seconds_total{db_id=\"%d\"} %f\n", id, st.WaitDuration.Seconds())
			}
		}

		w.Write([]byte(sb.String()))
	})
}
