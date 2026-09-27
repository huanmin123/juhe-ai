package statreads

// 去跨进程战役第三刀契约测试：go-runtime-trend 改为进程内直查共享
// gometrics Store（gateway+jobs 两 role），覆盖 roles 分组包络
// （samplingEnabled + 每 role 一组 {role, items}）、90 天钳位与 store
// 未启用/无数据的空 items 200 降级。

import (
	"context"
	"database/sql"
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

func callGoRuntimeTrend(t *testing.T, deps *Deps, query string) (map[string]any, []any, bool, *int) {
	t.Helper()
	record := invoke(t, deps.goRuntimeTrendHandler, http.MethodGet, "/__aisys__/api/stats/system-metrics/go-runtime-trend"+query, adminAuth(""))
	if record.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", record.Code, record.Body.String())
	}
	data := dataMap(t, decodeBody(t, record))
	roles, ok := data["roles"].([]any)
	if !ok {
		t.Fatalf("roles must decode to a JSON array (never null): %#v", data["roles"])
	}
	samplingEnabled, _ := data["samplingEnabled"].(bool)
	var rng *int
	if rawRange, ok := data["range"].(map[string]any); ok {
		if days, ok := rawRange["days"].(float64); ok {
			value := int(days)
			rng = &value
		}
	}
	return data, roles, samplingEnabled, rng
}

func goRuntimeTrendRoleItems(t *testing.T, roles []any, role string) []any {
	t.Helper()
	for _, entry := range roles {
		group, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("role group must decode to an object: %#v", entry)
		}
		if group["role"] == role {
			items, ok := group["items"].([]any)
			if !ok {
				t.Fatalf("%s items must decode to a JSON array (never null): %#v", role, group["items"])
			}
			return items
		}
	}
	t.Fatalf("role group %s missing from: %#v", role, roles)
	return nil
}

func TestGoRuntimeTrendMergesGatewayAndJobsRoles(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, true)
	store := deps.GoRuntimeMetrics
	hour := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	seedGoRuntimeSample(t, store, "gateway", hour)
	seedGoRuntimeSample(t, store, "jobs", hour.Add(time.Minute))
	// Out-of-range row must not leak into the requested window.
	seedGoRuntimeSample(t, store, "gateway", time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC))

	data, roles, samplingEnabled, _ := callGoRuntimeTrend(t, deps, "?startDate=2026-09-03&endDate=2026-09-04")
	if data["runtimeKind"] != "go" || data["service"] != "juhe-ai" || data["timezone"] != "UTC" {
		t.Fatalf("unexpected envelope: %#v", data)
	}
	if data["role"] != nil || data["items"] != nil {
		t.Fatalf("outer aggregate role/flat items must be gone: %#v", data)
	}
	if !samplingEnabled {
		t.Fatal("wired store must set samplingEnabled=true")
	}
	if len(roles) != 2 {
		t.Fatalf("envelope must answer the fixed two-role family: %#v", roles)
	}
	gatewayItems := goRuntimeTrendRoleItems(t, roles, "gateway")
	jobsItems := goRuntimeTrendRoleItems(t, roles, "jobs")
	if len(gatewayItems) != 1 || len(jobsItems) != 1 {
		t.Fatalf("each role group must carry its own in-window row: gateway=%#v jobs=%#v", gatewayItems, jobsItems)
	}
	first, _ := gatewayItems[0].(map[string]any)
	second, _ := jobsItems[0].(map[string]any)
	// Each item keeps its own service/role identity inside its group.
	if first["role"] != "gateway" || second["role"] != "jobs" {
		t.Fatalf("items must stay inside their role groups: %#v %#v", first, second)
	}
	if first["service"] != "juhe-ai" || first["runtimeKind"] != "go" {
		t.Fatalf("item identity fields missing: %#v", first)
	}
	if first["windowStart"] != "2026-09-04T10:00:00Z" || first["windowStart"] != second["windowStart"] {
		t.Fatalf("unexpected hourly windows: %#v %#v", first["windowStart"], second["windowStart"])
	}
	if first["sampleCount"] != float64(1) || second["sampleCount"] != float64(1) {
		t.Fatalf("unexpected sample counts: %#v %#v", gatewayItems, jobsItems)
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
	data, roles, samplingEnabled, days := callGoRuntimeTrend(t, deps, "?startDate=2026-01-01&endDate=2026-09-04")
	if !samplingEnabled {
		t.Fatal("wired store must set samplingEnabled=true")
	}
	if got := len(goRuntimeTrendRoleItems(t, roles, "gateway")) + len(goRuntimeTrendRoleItems(t, roles, "jobs")); got != 2 {
		t.Fatalf("seeded rows must survive range normalization: %#v", roles)
	}
	if days == nil || *days != 31 {
		t.Fatalf("range must follow the system-metrics 31-day contract: %#v", data["range"])
	}
}

func TestGoRuntimeTrendEmptyStoreAnswersEmptyItemsOK(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, true)
	_, roles, samplingEnabled, _ := callGoRuntimeTrend(t, deps, "?startDate=2026-09-04&endDate=2026-09-04")
	if !samplingEnabled {
		t.Fatal("wired but empty store must set samplingEnabled=true")
	}
	gatewayItems := goRuntimeTrendRoleItems(t, roles, "gateway")
	jobsItems := goRuntimeTrendRoleItems(t, roles, "jobs")
	if len(gatewayItems) != 0 || len(jobsItems) != 0 {
		t.Fatalf("empty store must answer empty items per role: gateway=%#v jobs=%#v", gatewayItems, jobsItems)
	}
}

func TestGoRuntimeTrendDisabledStoreAnswersEmptyItemsOK(t *testing.T) {
	deps := newGoRuntimeTrendFixture(t, false)
	data, roles, samplingEnabled, _ := callGoRuntimeTrend(t, deps, "?startDate=2026-09-04&endDate=2026-09-04")
	if samplingEnabled {
		t.Fatalf("nil store must set samplingEnabled=false: %#v", data)
	}
	if len(roles) != 2 {
		t.Fatalf("disabled store keeps the fixed two-role family: %#v", roles)
	}
	for _, role := range []string{"gateway", "jobs"} {
		if items := goRuntimeTrendRoleItems(t, roles, role); len(items) != 0 {
			t.Fatalf("disabled store must answer empty %s items: %#v", role, items)
		}
	}
}
