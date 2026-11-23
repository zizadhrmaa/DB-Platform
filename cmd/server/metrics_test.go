package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// /metrics memuat gauge dbp_jobs berisi jumlah job saat ini per status,
// dihitung dari tabel jobs (tetap benar setelah restart).
func TestMetricsJobGaugeFromDatabase(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	loc := createMigration(t, bob, e.base, "satu", "CREATE TABLE satu (id INT)")
	migID, _ := strconv.ParseInt(loc[strings.LastIndex(loc, "/")+1:], 10, 64)
	dbID, _ := strconv.ParseInt(strings.Split(e.base, "/")[2], 10, 64)
	bobID := idOf(t, e.db, `SELECT id FROM users WHERE username = 'bob'`)
	qs := &queue.Store{DB: e.db}
	if _, _, err := qs.Enqueue(context.Background(), queue.EnqueueParams{DBID: dbID, MigrationID: migID, IdempotencyKey: "m", RequestedBy: bobID}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "# TYPE dbp_jobs gauge") {
		t.Fatalf("metrics: status %d\n%s", rr.Code, body)
	}
	for _, want := range []string{`dbp_jobs{status="queued"} 1`, `dbp_jobs{status="running"} 0`, `dbp_jobs{status="failed"} 0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrik %q tidak ada:\n%s", want, body)
		}
	}
}
