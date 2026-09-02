package gometricsstore_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreSmoke(t *testing.T) {
	url := os.Getenv("JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_SMOKE_URL")
	if url == "" {
		t.Skip("JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_SMOKE_URL not set")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := gometrics.NewStore(db, gometrics.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	service := "gometrics-smoke-" + time.Now().UTC().Format("20060102150405.000000000")
	when := time.Now().UTC().Truncate(time.Microsecond)
	sample := gometrics.RuntimeSnapshot{SampledAt: when, ProcessPID: 987654, Service: service, Role: "jobs", Goroutines: 3, HeapAllocBytes: 10, HeapLiveBytes: 9, HeapObjects: 2, Threads: 1}
	inserted, err := store.InsertSnapshot(ctx, sample)
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	trend, err := store.QueryTrend(ctx, service, "jobs", when.Add(-time.Hour), when.Add(time.Hour))
	if err != nil || len(trend) != 1 || trend[0].SampleCount != 1 {
		t.Fatalf("trend=%+v err=%v", trend, err)
	}
	for _, table := range []string{"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM juhe_stats."+table+" WHERE service=$1", service); err != nil {
			t.Fatalf("cleanup %s: %v", table, err)
		}
	}
}
