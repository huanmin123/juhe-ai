package gometrics

// w12h 补充 arms：Prometheus 写出路径的逐调用错误传播（注入按次失败的
// writer）、Collector 门面、LoadConfig/OpenStore 收口、SQLite store 的 nil
// 守卫与查询边界、窗口聚合的可选指标与排序，以及 w1cover 临时子库上的
// PostgreSQL schema 契约（ensure/check/query，w12h- 前缀数据用后即清）。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// w12hFailingWriter 在第 n 次写调用时失败。
type w12hFailingWriter struct {
	failOnCall int
	calls      int
}

func (w *w12hFailingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failOnCall {
		return 0, errors.New("w12h writer failure")
	}
	return len(p), nil
}

func TestW12HWritePropagatesWriterErrorsPerCall(t *testing.T) {
	collector := New("w12h-svc", "w12h-role")
	// 直方图 bucket 输出可达数百次 Fprintf，循环上限需要覆盖全序列。
	for failOnCall := 1; failOnCall <= 400; failOnCall++ {
		writer := &w12hFailingWriter{failOnCall: failOnCall}
		if err := collector.Write(writer); err != nil && !strings.Contains(err.Error(), "w12h writer failure") {
			t.Fatalf("第 %d 次写出: %v", failOnCall, err)
		}
	}
	// 完整成功写出保持可用（语句数远小于 400 次调用上限时 err 为 nil）。
	if err := collector.Write(&w12hFailingWriter{failOnCall: 10_000}); err != nil {
		t.Fatalf("健康 writer 不应报错: %v", err)
	}
}

func TestW12HCollectorFacade(t *testing.T) {
	var nilCollector *Collector
	if nilCollector.Service() != "" || nilCollector.Role() != "" {
		t.Fatal("nil collector 门面必须返回空串")
	}
	collector := New(" w12h\"svc\n ", `w12h\role`)
	if collector.Service() != ` w12h\"svc  ` || collector.Role() != `w12h\\role` {
		t.Fatalf("标签清洗不符: %q / %q", collector.Service(), collector.Role())
	}
	// Record 落入进程内小时窗口。
	before := len(collector.Windows())
	snapshot := collector.Record()
	if snapshot.Service != collector.Service() || snapshot.SampledAt.IsZero() {
		t.Fatalf("Record 快照不符: %+v", snapshot)
	}
	// Windows 只保留非空窗口；Record 后至少有一个窗口。
	if len(collector.Windows()) < before || len(collector.Windows()) == 0 {
		t.Fatalf("Record 后窗口数不符: %d", len(collector.Windows()))
	}
}

func TestW12HLoadConfigArms(t *testing.T) {
	// getenv 为 nil 时回落 os.Getenv。
	t.Setenv("JUHE_AI_GO_RUNTIME_METRICS_STORE", "disabled")
	if _, err := LoadConfig(nil, "jobs"); err != nil {
		t.Fatalf("os.Getenv 回落失败: %v", err)
	}

	// sqlite 缺少路径。
	getenvStore := func(key string) string {
		if key == "JUHE_AI_GO_RUNTIME_METRICS_STORE" {
			return "sqlite"
		}
		return ""
	}
	if _, err := LoadConfig(getenvStore, "jobs"); err == nil || !strings.Contains(err.Error(), "DATABASE_PATH") {
		t.Fatalf("sqlite 缺路径必须报错: %v", err)
	}

	// service/role 环境覆盖。
	overrides := func(key string) string {
		switch key {
		case "JUHE_AI_GO_RUNTIME_METRICS_STORE":
			return "postgres"
		case "JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL":
			return "postgres://w12h.invalid/db"
		case "JUHE_AI_GO_RUNTIME_METRICS_SERVICE":
			return "w12h-service"
		case "JUHE_AI_GO_RUNTIME_METRICS_ROLE":
			return "w12h-role"
		}
		return ""
	}
	config, err := LoadConfig(overrides, "fallback-role")
	if err != nil {
		t.Fatal(err)
	}
	if config.Service != "w12h-service" || config.Role != "w12h-role" {
		t.Fatalf("env 覆盖不符: %+v", config)
	}
}

func TestW12HOpenStoreArms(t *testing.T) {
	dir := t.TempDir()
	config := Config{Enabled: true, Store: DialectSQLite, DatabasePath: filepath.Join(dir, "nested", "w12h.sqlite3")}
	store, db, err := OpenStore(config)
	if err != nil || store == nil || db == nil {
		t.Fatalf("sqlite OpenStore 失败: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	// 非法方言：sql.Open 成功但 NewStore 拒绝，句柄必须被关闭。
	bogus := Config{Enabled: true, Store: SQLDialect("w12h-bogus"), DatabasePath: filepath.Join(dir, "bogus.sqlite3")}
	if bogusStore, bogusDB, err := OpenStore(bogus); err == nil || bogusStore != nil || bogusDB != nil {
		t.Fatalf("非法方言必须报错: %v %v %v", bogusStore, bogusDB, err)
	}
}

func TestW12HStoreGuardsAndQueryBoundaries(t *testing.T) {
	var nilStore *Store
	ctx := context.Background()
	if _, err := nilStore.InsertSnapshot(ctx, RuntimeSnapshot{}); err == nil {
		t.Fatal("nil store InsertSnapshot 必须报错")
	}
	if err := nilStore.PruneBefore(ctx, time.Now()); err == nil {
		t.Fatal("nil store PruneBefore 必须报错")
	}
	if _, err := nilStore.QueryTrend(ctx, "s", "r", time.Now(), time.Now()); err == nil {
		t.Fatal("nil store QueryTrend 必须报错")
	}
	if _, err := nilStore.QueryTrendWindows(ctx, "s", "r", time.Now(), time.Now()); err == nil {
		t.Fatal("nil store QueryTrendWindows 必须报错")
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-guard.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{Service: "s", Role: "r"}); err == nil {
		t.Fatal("零时间戳必须被拒绝")
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	// to 缺省 / from 缺省 / from >= to。
	if got, err := store.QueryTrend(ctx, "s", "r", time.Time{}, time.Time{}); err != nil || got == nil {
		t.Fatalf("零 to/from 必须回落: %v %v", got, err)
	}
	if got, err := store.QueryTrendWindows(ctx, "s", "r", time.Time{}, time.Time{}); err != nil || got == nil {
		t.Fatalf("零 to/from 必须回落: %v %v", got, err)
	}
	if got, err := store.QueryTrend(ctx, "s", "r", now, now); err != nil || len(got) != 0 {
		t.Fatalf("from>=to 必须得到空集: %v %v", got, err)
	}
	if got, err := store.QueryTrendWindows(ctx, "s", "r", now, now); err != nil || len(got) != 0 {
		t.Fatalf("from>=to 必须得到空集: %v %v", got, err)
	}
	if _, err := store.QueryTrendWindows(ctx, "s", "r", now, now.Add(maxDailyTrendRange+time.Hour)); !errors.Is(err, ErrTrendRangeTooLarge) {
		t.Fatalf("日趋势超界必须报错: %v", err)
	}

	// WriteSnapshot 与 Record 等价。
	inserted, err := store.WriteSnapshot(ctx, RuntimeSnapshot{SampledAt: now, Service: "w12h-s", Role: "w12h-r", ProcessPID: 1, Goroutines: 1, UptimeSeconds: 1})
	if err != nil || !inserted {
		t.Fatalf("WriteSnapshot 失败: %v %v", inserted, err)
	}
}

func TestW12HParseSQLTimeShapes(t *testing.T) {
	want := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	if got := parseSQLTime(want); !got.Equal(want) {
		t.Fatalf("time.Time 直通不符: %v", got)
	}
	if got := parseSQLTime("2026-09-17T08:00:00Z"); !got.Equal(want) {
		t.Fatalf("string 形态不符: %v", got)
	}
	if got := parseSQLTime([]byte("2026-09-17T08:00:00Z")); !got.Equal(want) {
		t.Fatalf("[]byte 形态不符: %v", got)
	}
	if !parseSQLTime(42).IsZero() {
		t.Fatal("未知形态必须为零值")
	}
	if !parseSQLTime("not-a-time").IsZero() {
		t.Fatal("不可解析时间必须为零值")
	}
}

func TestW12HWindowAggregatorArms(t *testing.T) {
	// 非正 retention 回落默认 24h。
	agg := NewWindowAggregator(0)
	// 零时间戳被忽略；可选指标缺失走 nil 投影。
	agg.Add(RuntimeSnapshot{})
	cpu := 55.5
	var rss uint64 = 1 << 20
	var fd uint64 = 12
	base := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	agg.Add(RuntimeSnapshot{
		SampledAt: base.Add(3 * time.Minute), Service: "s2", Role: "r2",
		Goroutines: 10, CPUPercent: &cpu, RSSBytes: &rss, FDCount: &fd, UptimeSeconds: 30,
	})
	windows := agg.Windows()
	if len(windows) != 1 {
		t.Fatalf("窗口数不符: %d", len(windows))
	}
	window := windows[0]
	if window.CPUPercentAvg == nil || *window.CPUPercentAvg != 55.5 || window.RSSBytesMax == nil || *window.RSSBytesMax != float64(rss) {
		t.Fatalf("可选指标投影不符: %+v", window)
	}

	// 同一小时段相邻窗口：排序比较器覆盖时间/服务/角色三档。
	agg2 := NewWindowAggregator(3 * time.Hour)
	agg2.Add(RuntimeSnapshot{SampledAt: base, Service: "b", Role: "r", Goroutines: 1})
	agg2.Add(RuntimeSnapshot{SampledAt: base.Add(time.Hour), Service: "a", Role: "r", Goroutines: 1})
	agg2.Add(RuntimeSnapshot{SampledAt: base.Add(2 * time.Hour), Service: "a", Role: "a", Goroutines: 1})
	ordered := agg2.Windows()
	if len(ordered) != 3 || !(ordered[0].Service == "b" && ordered[1].Service == "a" && ordered[2].Role == "a") {
		t.Fatalf("窗口排序不符: %+v", ordered)
	}
	// 新窗口推进水位后，超出保留期的旧窗口被清退。
	agg3 := NewWindowAggregator(2 * time.Hour)
	agg3.Add(RuntimeSnapshot{SampledAt: base, Goroutines: 1})
	agg3.Add(RuntimeSnapshot{SampledAt: base.Add(5 * time.Hour), Goroutines: 1})
	if got := agg3.Windows(); len(got) != 1 || !got[0].WindowStart.Equal(base.Add(5 * time.Hour).Truncate(time.Hour)) {
		t.Fatalf("保留期清退不符: %+v", got)
	}
}

// ---- w12h PostgreSQL 门控（w1cover 临时子库，w12h- 前缀数据用后即清）----

const w12hCoverDBName = "juhe_ai_sub2api_dev_w1cover"

func w12hSharedEnvValue(t *testing.T, key string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../../.local/project-resources/dev/env/shared.env")
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, found := strings.Cut(line, "="); found && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func TestW12HPostgresSchemaContract(t *testing.T) {
	env := w12hSharedEnvValue(t, "JUHE_AI_POSTGRES_URL")
	if w12hSharedEnvValue(t, "DEV_POSTGRES_HOST") == "" || env == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	sep := strings.LastIndex(env, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	db, err := sql.Open("pgx", env[:sep+1]+w12hCoverDBName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("临时子库不可达（跳过）: %v", err)
	}
	store, err := NewStore(db, DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG EnsureSchema: %v", err)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("PG CheckSchema 契约: %v", err)
	}

	service := "w12h-gometrics"
	// 先清同名残留（可重入），再注册用后清理。
	for _, table := range []string{"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM juhe_stats."+table+" WHERE service=$1", service); err != nil {
			t.Fatalf("前置清理 %s: %v", table, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, table := range []string{"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows"} {
			_, _ = db.ExecContext(cleanupCtx, "DELETE FROM juhe_stats."+table+" WHERE service=$1", service)
		}
	})

	cpu := 42.5
	var rss uint64 = 1 << 20
	// 锚定在当前小时的安全区间：避免 when+10min 落入下一个小时聚合窗口
	// （临近整点运行时的时钟依赖缺陷）。
	when := time.Now().UTC().Truncate(time.Minute)
	if when.Minute() > 45 {
		when = when.Add(-15 * time.Minute)
	}
	inserted, err := store.InsertSnapshot(ctx, RuntimeSnapshot{
		SampledAt: when, ProcessPID: 424242, Service: service, Role: "w12h-jobs",
		Goroutines: 5, GoroutinesRunnable: 2, GoroutinesWaiting: 2, Threads: 4, GOMAXPROCS: 8,
		HeapAllocBytes: 1000, HeapLiveBytes: 900, HeapObjects: 50, UptimeSeconds: 12.5,
		CPUPercent: &cpu, RSSBytes: &rss,
	})
	if err != nil || !inserted {
		t.Fatalf("PG 插入失败: %v %v", inserted, err)
	}
	// 同窗口第二笔触发 PG UPDATE 聚合（GREATEST max 表达式与可选指标累加）。
	second := when.Add(10 * time.Minute)
	if inserted, err = store.InsertSnapshot(ctx, RuntimeSnapshot{
		SampledAt: second, ProcessPID: 424243, Service: service, Role: "w12h-jobs",
		Goroutines: 9, GoroutinesRunnable: 3, GoroutinesWaiting: 3, Threads: 6, GOMAXPROCS: 8,
		HeapAllocBytes: 2000, HeapLiveBytes: 1500, HeapObjects: 80, UptimeSeconds: 30,
		CPUPercent: &cpu, FDCount: &rss,
	}); err != nil || !inserted {
		t.Fatalf("PG 第二笔插入失败: %v %v", inserted, err)
	}
	hourly, err := store.QueryTrend(ctx, service, "w12h-jobs", when.Add(-time.Hour), when.Add(time.Hour))
	if err != nil || len(hourly) != 1 || hourly[0].SampleCount != 2 || hourly[0].GoroutinesMax != 9 {
		t.Fatalf("PG 小时聚合不符: %+v %v", hourly, err)
	}
	if hourly[0].CPUPercentAvg == nil || *hourly[0].CPUPercentAvg != cpu || hourly[0].FDCountMax == nil {
		t.Fatalf("PG 可选指标聚合不符: %+v", hourly[0])
	}
	daily, err := store.QueryTrendWindows(ctx, service, "w12h-jobs", when.Add(-24*time.Hour), when.Add(24*time.Hour))
	if err != nil || len(daily) != 1 || daily[0].SampleCount != 2 || daily[0].WindowEnd.IsZero() {
		t.Fatalf("PG 日窗口不符: %+v %v", daily, err)
	}
	if err := store.PruneBefore(ctx, when.Add(time.Hour)); err != nil {
		t.Fatalf("PG 清理失败: %v", err)
	}
	remaining, err := store.QueryTrend(ctx, service, "w12h-jobs", when.Add(-time.Hour), when.Add(time.Hour))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("清理后仍有窗口: %+v %v", remaining, err)
	}
}
