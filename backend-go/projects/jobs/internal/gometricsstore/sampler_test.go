package gometricsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "modernc.org/sqlite"
)

func TestTrendHandlerReturnsPersistedRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "trend.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 2, 3, 0, 0, 0, time.UTC)
	if _, err := store.InsertSnapshot(context.Background(), gometrics.RuntimeSnapshot{SampledAt: when, ProcessPID: 1, Service: "juhe-ai", Role: "jobs", Goroutines: 3}); err != nil {
		t.Fatal(err)
	}
	sampler, err := NewSampler(gometrics.New("juhe-ai", "jobs"), store, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := httptest.NewRecorder()
	sampler.TrendHandler().ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/go-runtime-trend?from=2026-09-02T00:00:00Z&to=2026-09-03T00:00:00Z", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", record.Code, record.Body.String())
	}
	var payload struct {
		RuntimeKind string                      `json:"runtimeKind"`
		Items       []gometrics.WindowAggregate `json:"items"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RuntimeKind != "go" || len(payload.Items) != 1 || payload.Items[0].SampleCount != 1 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestSamplerEstablishesCPUBaselineBeforePersisting(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "baseline.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	sampler, err := NewSampler(gometrics.New("juhe-ai", "jobs"), store, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sampler.write(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryTrend(context.Background(), "juhe-ai", "jobs", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("baseline must not persist a partial sample: %#v", rows)
	}
	if err := sampler.write(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err = store.QueryTrend(context.Background(), "juhe-ai", "jobs", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SampleCount != 1 {
		t.Fatalf("second sample should persist: %#v", rows)
	}
}
