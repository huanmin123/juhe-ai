package statreads

// 去跨进程战役第三刀契约测试：go-runtime-trend 改为进程内直查共享
// gometrics Store（gateway+jobs 两 role），覆盖双 role 合并、90 天钳位与
// store 未启用/无数据的空 items 200 降级。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "modernc.org/sqlite"
)

func newGoRuntimeTrendFixture(t *testing.T, withStore bool) *Deps {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "go-runtime-trend.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deps := &Deps{
		Business:                db,
		Stats:                   db,
		PGDialect:               false,
		Now:                     func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
		Timezone:                func(context.Context) (string, error) { return "UTC", nil },
		GoRuntimeMetricsService: "juhe-ai",
	}
	if !withStore {
		return deps
	}
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	deps.GoRuntimeMetrics = store
	return deps
}

func seedGoRuntimeSample(t *testing.T, store *gometrics.Store, role string, sampledAt time.Time) {
	t.Helper()
	if _, err := store.InsertSnapshot(context.Background(), gometrics.RuntimeSnapshot{SampledAt: sampledAt, ProcessPID: 11, Service: "juhe-ai", Role: role, Goroutines: 8, HeapAllocBytes: 4096, Threads: 5}); err != nil {
		t.Fatalf("seed %s sample: %v", role, err)
	}
}

func callGoRuntimeTrend(t *testing.T, deps *Deps, query string) (map[string]any, []any, *int) {
	t.Helper()
	record := invoke(t, deps.goRuntimeTrendHandler, http.MethodGet, "/__aisys__/api/stats/system-metrics/go-runtime-trend"+query, adminAuth(""))
	if record.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", record.Code, record.Body.String())
	}
	data := dataMap(t, decodeBody(t, record))
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items must decode to a JSON array (never null): %#v", data["items"])
	}
	var rng *int
	if rawRange, ok := data["range"].(map[string]any); ok {
		if days, ok := rawRange["days"].(float64); ok {
			value := int(days)
			rng = &value
		}
	}
	return data, items, rng
}

func TestGoRuntimeTrendMergesGatewayAndJobsRoles(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, true)
	store := deps.GoRuntimeMetrics
	hour := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	seedGoRuntimeSample(t, store, "gateway", hour)
	seedGoRuntimeSample(t, store, "jobs", hour.Add(time.Minute))
	// Out-of-range row must not leak into the requested window.
	seedGoRuntimeSample(t, store, "gateway", time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC))

	data, items, _ := callGoRuntimeTrend(t, deps, "?startDate=2026-09-03&endDate=2026-09-04")
	if data["runtimeKind"] != "go" || data["service"] != "juhe-ai" || data["role"] != "gateway+jobs" || data["timezone"] != "UTC" {
		t.Fatalf("unexpected envelope: %#v", data)
	}
	if len(items) != 2 {
		t.Fatalf("items must contain both role rows: %#v", items)
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	// Items are grouped by role (gateway block first, jobs block second); each
	// item keeps its own service/role identity.
	if first["role"] != "gateway" || second["role"] != "jobs" {
		t.Fatalf("items must be grouped gateway then jobs: %#v", items)
	}
	if first["service"] != "juhe-ai" || first["runtimeKind"] != "go" {
		t.Fatalf("item identity fields missing: %#v", first)
	}
	if first["windowStart"] != "2026-09-04T10:00:00Z" || first["windowStart"] != second["windowStart"] {
		t.Fatalf("unexpected hourly windows: %#v %#v", first["windowStart"], second["windowStart"])
	}
	if first["sampleCount"] != float64(1) || second["sampleCount"] != float64(1) {
		t.Fatalf("unexpected sample counts: %#v", items)
	}
	if first["goroutinesAvg"] != float64(8) {
		t.Fatalf("unexpected aggregate: %#v", first)
	}
}

func TestGoRuntimeTrendOversizedRangeNeverLeaksStoreRangeError(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, true)
	hour := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	seedGoRuntimeSample(t, deps.GoRuntimeMetrics, "gateway", hour)
	seedGoRuntimeSample(t, deps.GoRuntimeMetrics, "jobs", hour.Add(time.Minute))

	// The system-metrics date contract caps the window (maxDays=31, below the
	// store's 90-day hourly bound), so an oversized request normalizes and
	// answers 200 instead of surfacing the store range error.
	data, items, days := callGoRuntimeTrend(t, deps, "?startDate=2026-01-01&endDate=2026-09-04")
	if data["role"] != "gateway+jobs" {
		t.Fatalf("unexpected envelope: %#v", data)
	}
	if len(items) != 2 {
		t.Fatalf("seeded rows must survive range normalization: %#v", items)
	}
	if days == nil || *days != 31 {
		t.Fatalf("range must follow the system-metrics 31-day contract: %#v", data["range"])
	}
}

func TestGoRuntimeTrendEmptyStoreAnswersEmptyItemsOK(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, true)
	_, items, _ := callGoRuntimeTrend(t, deps, "?startDate=2026-09-04&endDate=2026-09-04")
	if len(items) != 0 {
		t.Fatalf("empty store must answer empty items: %#v", items)
	}
}

func TestGoRuntimeTrendDisabledStoreAnswersEmptyItemsOK(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, false)
	record := invoke(t, deps.goRuntimeTrendHandler, http.MethodGet, "/__aisys__/api/stats/system-metrics/go-runtime-trend?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	if record.Code != http.StatusOK {
		t.Fatalf("disabled store must not fail the route: status=%d body=%s", record.Code, record.Body.String())
	}
	var payload struct {
		Items []any `json:"items"`
	}
	data := dataMap(t, decodeBody(t, record))
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("disabled store must answer empty items: %#v", data)
	}
	if data["role"] != "gateway+jobs" {
		t.Fatalf("disabled store keeps the envelope contract: %#v", data)
	}
}
