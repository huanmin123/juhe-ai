package gometrics

// w12h 补充 arms（二）：schema 契约破坏性注入（SQLite + w12h 专属一次性
// PostgreSQL 库，用后即删）、采样器错误传播、Handler scrape 错误与
// OpenStore 边界。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func TestW12HCheckSchemaSQLiteArms(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-contract.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 未建表 → table is missing。
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "table is missing") {
		t.Fatalf("缺表必须报错: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	recreate := func(t *testing.T, ddl string) {
		t.Helper()
		if _, err := db.Exec(`DROP TABLE go_runtime_metrics_samples`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	base := `CREATE TABLE go_runtime_metrics_samples (
service TEXT NOT NULL, role TEXT NOT NULL, runtime_kind TEXT NOT NULL DEFAULT 'go',
process_pid INTEGER NOT NULL, sampled_at TIMESTAMP NOT NULL,
goroutines INTEGER NOT NULL, goroutines_runnable INTEGER NOT NULL, goroutines_waiting INTEGER NOT NULL,
threads INTEGER NOT NULL, gomaxprocs INTEGER NOT NULL,
heap_alloc_bytes INTEGER NOT NULL, heap_live_bytes INTEGER NOT NULL, heap_objects INTEGER NOT NULL,
cpu_percent REAL, rss_bytes INTEGER, fd_count INTEGER, uptime_seconds REAL NOT NULL,
PRIMARY KEY(service,role,runtime_kind,process_pid,sampled_at))`

	// 缺列。
	recreate(t, strings.Replace(base, "fd_count INTEGER,", "", 1))
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "column fd_count is missing") {
		t.Fatalf("缺列必须报错: %v", err)
	}
	// 类型漂移。
	recreate(t, strings.Replace(base, "service TEXT NOT NULL", "service INTEGER NOT NULL", 1))
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("类型漂移必须报错: %v", err)
	}
	// 可空列违反契约。
	recreate(t, strings.Replace(base, "goroutines INTEGER NOT NULL", "goroutines INTEGER", 1))
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "must be NOT NULL") {
		t.Fatalf("可空列必须报错: %v", err)
	}
	// 主键同数不同名（5 列非主键 NOT NULL 组合）。
	recreate(t, strings.Replace(base, "PRIMARY KEY(service,role,runtime_kind,process_pid,sampled_at)", "PRIMARY KEY(role,runtime_kind,process_pid,sampled_at,goroutines)", 1))
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "primary key mismatch") {
		t.Fatalf("主键错名必须报错: %v", err)
	}

	// 关闭句柄后 PRAGMA 查询失败。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil {
		t.Fatal("关闭句柄后 CheckSchema 必须报错")
	}
	// 关闭句柄后 EnsureSchema 的建表失败。
	if err := store.EnsureSchema(ctx); err == nil {
		t.Fatal("关闭句柄后 EnsureSchema 必须报错")
	}
}

func TestW12HEnsureSchemaColumnArms(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-cols.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 只把聚合表换成 view：samples 的索引成功，聚合表补列失败 →
	// ensureMetricColumns 的 SQLite ADD COLUMN 错误分支。
	if _, err := db.Exec(`CREATE VIEW go_runtime_metrics_hourly AS SELECT 1 AS x`); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("聚合表 view 占位必须导致补列失败")
	}
}

func TestW12HTrendSortWithMultipleWindows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-sort.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC)
	for i, offset := range []time.Duration{2 * time.Hour, 0, time.Hour} {
		if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{
			SampledAt: base.Add(offset), ProcessPID: i, Service: "w12h-sort", Role: "w12h-r",
			Goroutines: 3, UptimeSeconds: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	trend, err := store.QueryTrend(ctx, "w12h-sort", "w12h-r", base.Add(-time.Hour), base.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 3 || !trend[0].WindowStart.Before(trend[1].WindowStart) || !trend[1].WindowStart.Before(trend[2].WindowStart) {
		t.Fatalf("多窗口排序不符: %+v", trend)
	}

	// 同窗口跨 service：排序比较器落到 service/role 次级键。
	sameHour := NewWindowAggregator(time.Hour)
	sameHour.Add(RuntimeSnapshot{SampledAt: base, Service: "b", Role: "r", Goroutines: 1})
	sameHour.Add(RuntimeSnapshot{SampledAt: base.Add(time.Minute), Service: "a", Role: "r", Goroutines: 1})
	sameHour.Add(RuntimeSnapshot{SampledAt: base.Add(2 * time.Minute), Service: "a", Role: "a", Goroutines: 1})
	ordered := sameHour.Windows()
	if len(ordered) != 3 || ordered[0].Service != "a" || ordered[0].Role != "a" || ordered[1].Service != "a" || ordered[2].Service != "b" {
		t.Fatalf("同窗口次级排序不符: %+v", ordered)
	}
}

func TestW12HInsertSnapshotUpsertErrorArms(t *testing.T) {
	// hourly 聚合表形状破坏 → upsert 插入失败传播。
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-badhourly.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE go_runtime_metrics_hourly`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE go_runtime_metrics_hourly (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{
		SampledAt: time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC), ProcessPID: 1,
		Service: "w12h-s", Role: "w12h-r", Goroutines: 1, UptimeSeconds: 1,
	}); err == nil {
		t.Fatal("hourly 形状破坏必须使 InsertSnapshot 失败")
	}

	// trend_windows 形状破坏 → 日窗口 upsert 失败传播。
	db2, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-badtrend.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	store2, err := NewStore(db2, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store2.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`DROP TABLE go_runtime_metrics_trend_windows`); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`CREATE TABLE go_runtime_metrics_trend_windows (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.InsertSnapshot(ctx, RuntimeSnapshot{
		SampledAt: time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC), ProcessPID: 1,
		Service: "w12h-s", Role: "w12h-r", Goroutines: 1, UptimeSeconds: 1,
	}); err == nil {
		t.Fatal("trend 形状破坏必须使 InsertSnapshot 失败")
	}

	// 聚合列类型污染 → 查询扫描失败。
	db3, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-scan.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	store3, err := NewStore(db3, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store3.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store3.InsertSnapshot(ctx, RuntimeSnapshot{
		SampledAt: time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC), ProcessPID: 1,
		Service: "w12h-s", Role: "w12h-r", Goroutines: 1, UptimeSeconds: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db3.Exec(`UPDATE go_runtime_metrics_hourly SET cpu_sample_count = 'not-a-number'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store3.QueryTrend(ctx, "w12h-s", "w12h-r", time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("聚合列类型污染必须使查询失败")
	}
	_ = db.Close()
	_ = db2.Close()
	_ = db3.Close()
}

func TestW12HSamplerFirstPruneError(t *testing.T) {
	// 首写成功但 trend_windows 缺失 → Run 的首轮 prune 错误收口。
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "w12h-prune-err.sqlite3")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE go_runtime_metrics_trend_windows`); err != nil {
		t.Fatal(err)
	}
	sampler, err := NewSampler(New("w12h-s", "w12h-r"), store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sampler.baseline = true
	if err := sampler.Run(context.Background()); err == nil {
		t.Fatal("首轮 prune 失败必须返回错误")
	}
	_ = db.Close()
}

func TestW12HEnsureSchemaFailuresOnSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-views.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, view := range []string{"go_runtime_metrics_samples", "go_runtime_metrics_hourly"} {
		if _, err := db.Exec("CREATE VIEW " + view + " AS SELECT 1 AS x"); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	// CREATE TABLE IF NOT EXISTS 被 view 挡住后，索引与补列都会失败。
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("view 占位必须导致 EnsureSchema 失败")
	}

	var nilStore *Store
	if err := nilStore.EnsureSchema(context.Background()); err == nil {
		t.Fatal("nil store EnsureSchema 必须报错")
	}
	if _, err := NewStore(nil, DialectSQLite); err == nil {
		t.Fatal("nil db 必须报错")
	}
}

func TestW12HInsertAndQueryErrorPropagation(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// 空 service/role 拒绝。
	if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{SampledAt: time.Now().UTC(), ProcessPID: 1}); err == nil {
		t.Fatal("空 service/role 必须被拒绝")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// 关闭句柄后的错误传播。
	if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{SampledAt: time.Now().UTC(), Service: "s", Role: "r"}); err == nil {
		t.Fatal("关闭句柄后 InsertSnapshot 必须报错")
	}
	if err := store.PruneBefore(ctx, time.Now()); err == nil {
		t.Fatal("关闭句柄后 PruneBefore 必须报错")
	}
	if _, err := store.QueryTrend(ctx, "s", "r", time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Fatal("关闭句柄后 QueryTrend 必须报错")
	}
}

func TestW12HSamplerErrorPropagation(t *testing.T) {
	// interval 非正回落默认（需要有效 collector+store 才能触达该分支）。
	samplerDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12h-default-interval.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer samplerDB.Close()
	storeForDefault, err := NewStore(samplerDB, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	sampler, err := NewSampler(New("s", "r"), storeForDefault, 0)
	if err != nil || sampler.Interval != defaultInterval {
		t.Fatalf("默认 interval 不符: %v %v", sampler, err)
	}
	var nilSampler *Sampler
	if err := nilSampler.Run(context.Background()); err == nil {
		t.Fatal("nil sampler Run 必须报错")
	}

	// 基线完成后首写失败 → Run 立即返回错误。
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "w12h-sampler.sqlite3")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	sampler, err = NewSampler(New("w12h-s", "w12h-r"), store, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	sampler.baseline = true
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sampler.Run(context.Background()); err == nil {
		t.Fatal("首写失败必须返回错误")
	}

	// tick 写失败 → Run 返回错误。
	db2, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "w12h-sampler2.sqlite3")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	store2, err := NewStore(db2, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store2.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	sampler2, err := NewSampler(New("w12h-s", "w12h-r"), store2, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	sampler2.baseline = true
	runErr := make(chan error, 1)
	go func() { runErr <- sampler2.Run(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	_ = db2.Close()
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("tick 写失败必须返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在句柄关闭后退出")
	}
}

// w12hFailingRecorder 让每次 Write 都失败，驱动 Handler 的 scrape 错误分支。
type w12hFailingRecorder struct {
	header http.Header
}

func (w *w12hFailingRecorder) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *w12hFailingRecorder) Write([]byte) (int, error) { return 0, errors.New("w12h scrape failure") }
func (w *w12hFailingRecorder) WriteHeader(int)           {}

func TestW12HHandlerScrapeErrorBody(t *testing.T) {
	collector := New("w12h-svc", "w12h-role")
	handler := collector.Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("正常 scrape 状态码 = %d", recorder.Code)
	}

	failing := &w12hFailingRecorder{}
	handler.ServeHTTP(failing, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
}

func TestW12HOpenStoreArmsExtended(t *testing.T) {
	dir := t.TempDir()
	// pgx 驱动分支（懒连接，不触网）。
	store, db, err := OpenStore(Config{Enabled: true, Store: DialectPostgres, PostgresURL: "postgres://w12h.invalid/db"})
	if err != nil || store == nil || db == nil {
		t.Fatalf("pgx OpenStore 失败: %v", err)
	}
	_ = db.Close()

	// DatabasePath 父级是文件 → MkdirAll 失败。
	blocker := filepath.Join(dir, "w12h-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenStore(Config{Enabled: true, Store: DialectSQLite, DatabasePath: filepath.Join(blocker, "inner", "x.sqlite3")}); err == nil {
		t.Fatal("MkdirAll 失败必须上抛")
	}

	// EnsureReady nil 收口与就绪校验。
	if err := EnsureReady(context.Background(), nil); err == nil {
		t.Fatal("nil store EnsureReady 必须报错")
	}
	readyStore, readyDB, err := OpenStore(Config{Enabled: true, Store: DialectSQLite, DatabasePath: filepath.Join(dir, "w12h-ready.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer readyDB.Close()
	if err := readyStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := EnsureReady(context.Background(), readyStore); err != nil {
		t.Fatalf("就绪校验失败: %v", err)
	}
}

// ---- w12h 专属一次性 PostgreSQL 库上的契约破坏注入（用后即删库）----

func TestW12HPostgresCheckSchemaDriftArms(t *testing.T) {
	host := w12hSharedEnvValue(t, "DEV_POSTGRES_HOST")
	adminUser := w12hSharedEnvValue(t, "DEV_POSTGRES_ADMIN_USERNAME")
	adminPass := w12hSharedEnvValue(t, "DEV_POSTGRES_ADMIN_PASSWORD")
	directPort := w12hSharedEnvValue(t, "DEV_POSTGRES_DIRECT_PORT")
	if host == "" || adminUser == "" || directPort == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	adminURL := "postgres://" + adminUser + ":" + adminPass + "@" + host + ":" + directPort + "/postgres?sslmode=disable"
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	scratch := "juhe_ai_sub2api_dev_w12h_gometrics"
	// dropScratch 终止残留连接并重试删库（FORCE 需要 PG13+，失败回退普通删）。
	dropScratch := func(why string) error {
		var lastErr error
		for attempt := 0; attempt < 10; attempt++ {
			_, _ = admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '`+scratch+`' AND pid <> pg_backend_pid()`)
			if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)"); err == nil {
				return nil
			} else if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+scratch); err == nil {
				return nil
			} else {
				lastErr = err
			}
			time.Sleep(500 * time.Millisecond)
		}
		return lastErr
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dropCancel()
		ctx = dropCtx
		_ = dropScratch("cleanup")
	})
	if err := dropScratch("pre-create"); err != nil {
		t.Fatalf("清理一次性库失败: %v", err)
	}
	owner := w12hSharedEnvValue(t, "DEV_POSTGRES_APP_USERNAME")
	if owner == "" {
		owner = adminUser
	}
	// 一次性库 owner 用管理员：app 角色默认无 CREATE，可注入权限分支。
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+scratch+" OWNER "+adminUser); err != nil {
		t.Fatalf("创建一次性库失败: %v", err)
	}
	env := w12hSharedEnvValue(t, "JUHE_AI_POSTGRES_URL")
	sep := strings.LastIndex(env, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	db, err := sql.Open("pgx", env[:sep+1]+scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}

	// app 无数据库 CREATE → CREATE SCHEMA 失败分支。
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("PG 无权限建 schema 必须失败: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "GRANT CREATE ON DATABASE "+scratch+" TO "+owner); err != nil {
		t.Fatal(err)
	}
	// 管理员连到一次性库预建 schema（owner=admin）：app 无 schema CREATE →
	// CREATE TABLE 失败分支。
	adminScratch, err := sql.Open("pgx", env[:sep+1]+scratch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminScratch.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`); err != nil {
		t.Fatal(err)
	}
	var schemaCreateAllowed bool
	if err := adminScratch.QueryRowContext(ctx, `SELECT has_schema_privilege($1, 'juhe_stats', 'CREATE')`, owner).Scan(&schemaCreateAllowed); err != nil {
		t.Fatal(err)
	}
	if !schemaCreateAllowed {
		if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("PG 无权限建表必须失败: %v", err)
		}
	}
	if _, err := adminScratch.ExecContext(ctx, "GRANT CREATE ON SCHEMA juhe_stats TO "+owner); err != nil {
		t.Fatal(err)
	}
	if err := adminScratch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("授权后 EnsureSchema: %v", err)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("一次性库 CheckSchema 基线: %v", err)
	}

	// 可空漂移（goroutines 是非主键列；主键列隐式 NOT NULL 不可放开）。
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ALTER COLUMN goroutines DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "must be NOT NULL") {
		t.Fatalf("PG 可空漂移必须报错: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ALTER COLUMN goroutines SET NOT NULL`); err != nil {
		t.Fatal(err)
	}
	// 类型漂移。
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ALTER COLUMN service TYPE varchar(64)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("PG 类型漂移必须报错: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ALTER COLUMN service TYPE text`); err != nil {
		t.Fatal(err)
	}
	// 缺列。
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples DROP COLUMN goroutines_runnable`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "column goroutines_runnable is missing") {
		t.Fatalf("PG 缺列必须报错: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ADD COLUMN goroutines_runnable bigint NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	// 缺表（放在 PK 破坏之前：CheckSchema 按表序短路）。
	if _, err := db.ExecContext(ctx, `DROP TABLE juhe_stats.go_runtime_metrics_trend_windows`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "table is missing") {
		t.Fatalf("PG 缺表必须报错: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG 重建 trend_windows: %v", err)
	}
	// 主键缺失 → 数量不符。
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples DROP CONSTRAINT go_runtime_metrics_samples_pkey`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "primary key mismatch") {
		t.Fatalf("PG 主键缺失必须报错: %v", err)
	}
	// 主键同数不同名。
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_stats.go_runtime_metrics_samples ADD PRIMARY KEY (role, runtime_kind, process_pid, sampled_at, goroutines)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "primary key mismatch") {
		t.Fatalf("PG 主键错名必须报错: %v", err)
	}
	// 关闭句柄后的查询错误传播。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil {
		t.Fatal("关闭句柄后 CheckSchema 必须报错")
	}
}
