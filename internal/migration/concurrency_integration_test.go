package migration

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestExecuteConcurrent(t *testing.T) {
	ctx := context.Background()

	platformDB := testutil.PlatformDB(t)
	targetDB, _ := testutil.TargetDB(t)

	user := testutil.MustUser(t, platformDB, "concurrent_user", false)
	dbID := testutil.MustDBConn(t, platformDB, "concurrent_target")

	az := &authz.Static{
		Levels: map[authz.Key]domain.AccessLevel{
			{UserID: user.ID, DBID: dbID}: domain.AccessEdit,
		},
	}

	store := &Store{
		DB:    platformDB,
		Authz: az,
		Targets: dbmanager.NewStatic(map[int64]*sql.DB{
			dbID: targetDB,
		}),
	}

	m, err := store.Create(
		ctx,
		user,
		dbID,
		"Concurrency Test",
		"CREATE TABLE concurrency_demo (id INT PRIMARY KEY)",
	)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Execute(ctx, user, dbID, m.ID)
			results <- err
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	success := 0
	conflict := 0

	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrConflict):
			conflict++
		default:
			t.Fatalf("Error tidak diharapkan: %v", err)
		}
	}

	if success != 1 || conflict != 1 {
		t.Fatalf(
			"Harus 1 sukses dan 1 conflict; dapat sukses=%d conflict=%d",
			success, conflict,
		)
	}

	var exists bool
	err = targetDB.QueryRowContext(ctx, `
		SELECT to_regclass('concurrency_demo') IS NOT NULL
	`).Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("Tabel tidak ditemukan: %v", err)
	}

	runs, err := store.Runs(ctx, dbID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != StatusSuccess {
		t.Fatalf("Riwayat eksekusi tidak sesuai: %+v", runs)
	}
}
