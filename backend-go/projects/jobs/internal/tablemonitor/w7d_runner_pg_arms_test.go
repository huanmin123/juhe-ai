package tablemonitor

// w7d（tablemonitor 覆盖补齐批次，w7d/TestW7D 前缀）：
//   1. Runner 调度生命周期（NewRunner/Run/RunSingleCycle/Ready/HealthHandler）。
//   2. Store 的 PostgreSQL 分支（schema bootstrap、owner lease 三件套），
//      用脚本化 database/sql driver 进程内模拟，不连接真实 PG。
//   3. 采样器 PostgreSQL 目录采集（collectPostgres + 采集目标错误臂）。
//   4. config 路径/数值辅助函数边界。
// SQLite 臂复用既有 sqliteTestEnv fixture；全部进程内确定性。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 脚本化 PostgreSQL database/sql driver
// ---------------------------------------------------------------------------

var w7dTMDriverSeq int64

type w7dTMStep struct {
	contains     string
	cols         []string
	rows         [][]driver.Value
	execErr      error
	queryErr     error
	commitErr    error
	zeroAffected bool
}

type w7dTMScript struct{ steps []w7dTMStep }

func (s *w7dTMScript) next(kind, query string) (*w7dTMStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7d tm scripted 脚本在 %s 处耗尽: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7d tm scripted 步骤不匹配: want %q got %s %s", step.contains, kind, query)
	}
	return step, nil
}

type w7dTMConn struct{ script *w7dTMScript }

func (c *w7dTMConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7d tm: Prepare unsupported")
}
func (c *w7dTMConn) Close() error              { return nil }
func (c *w7dTMConn) Begin() (driver.Tx, error) { return &w7dTMTx{script: c.script}, nil }
func (c *w7dTMConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c *w7dTMConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.queryErr != nil {
		return nil, step.queryErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"value"}
	}
	return &w7dTMRows{cols: cols, values: step.rows}, nil
}
func (c *w7dTMConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	if step.zeroAffected {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

type w7dTMTx struct{ script *w7dTMScript }

func (t *w7dTMTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7dTMTx) Rollback() error { return nil }

type w7dTMRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w7dTMRows) Columns() []string { return r.cols }
func (r *w7dTMRows) Close() error      { return nil }
func (r *w7dTMRows) Err() error        { return nil }
func (r *w7dTMRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}

type w7dTMDriver struct{ script *w7dTMScript }

func (d w7dTMDriver) Open(string) (driver.Conn, error) { return &w7dTMConn{script: d.script}, nil }

func w7dOpenTMDB(t *testing.T, steps []w7dTMStep) *sql.DB {
	t.Helper()
	name := "w7d-tm-scripted-" + fmt.Sprint(atomic.AddInt64(&w7dTMDriverSeq, 1))
	sql.Register(name, w7dTMDriver{script: &w7dTMScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w7dPostgresStore 构造 ModePostgres 的 Store（注入脚本化连接池句柄）。
func w7dPostgresStore(t *testing.T, steps []w7dTMStep) *Store {
	t.Helper()
	db := w7dOpenTMDB(t, steps)
	registry := pgpool.NewRegistry()
	handle, err := registry.AcquireWith(func() (*sql.DB, error) { return db, nil }, "w7d://table-monitor", "store", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return &Store{db: handle.DB(), mode: ModePostgres, pool: handle}
}

// ---------------------------------------------------------------------------
// Store PostgreSQL 分支
// ---------------------------------------------------------------------------

func TestW7DPostgresStoreEnsureSchemaArms(t *testing.T) {
	// schema 已存在：一次 EXISTS 查询为 true 即返回。
	store := w7dPostgresStore(t, []w7dTMStep{
		{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
	})
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema 已存在必须直接返回: %v", err)
	}

	// schema 缺失：advisory lock + 重检 + 全量 DDL + commit。
	bootstrap := w7dPostgresStore(t, []w7dTMStep{
		{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
		{contains: "pg_advisory_xact_lock"},
		{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
		{contains: "CREATE SCHEMA"},
		{contains: "CREATE TABLE"},
		{contains: "CREATE TABLE"},
		{contains: "CREATE TABLE"},
		{contains: "CREATE INDEX"},
		{contains: "CREATE INDEX"},
		{contains: "CREATE INDEX"},
		{},
	})
	if err := bootstrap.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("bootstrap 必须成功: %v", err)
	}

	// schema 检查错误 → 包装暴露。
	boom := w7dPostgresStore(t, []w7dTMStep{
		{contains: "to_regclass", queryErr: errors.New("w7d: exists boom")},
	})
	if err := boom.EnsureSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "检查表监控 PostgreSQL schema 失败") {
		t.Fatalf("schema 检查错误必须包装: %v", err)
	}
}

func TestW7DPostgresStoreOwnerLeaseArms(t *testing.T) {
	lease := OwnerLease{OwnerID: "w7d-owner", FenceToken: 9}

	// Acquire：返回新 fence token。
	acquire := w7dPostgresStore(t, []w7dTMStep{
		{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{contains: "INSERT INTO juhe_stats.table_monitor_owner_leases", cols: []string{"fence_token"}, rows: [][]driver.Value{{int64(9)}}},
	})
	got, acquired, err := acquire.AcquireOwnerLease(context.Background(), "w7d-owner", time.Minute)
	if err != nil || !acquired || got.FenceToken != 9 || got.OwnerID != "w7d-owner" {
		t.Fatalf("PG 获取租约: lease=%#v acquired=%t err=%v", got, acquired, err)
	}

	// Acquire：无行 → 未获取。
	busy := w7dPostgresStore(t, []w7dTMStep{
		{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{contains: "INSERT INTO juhe_stats.table_monitor_owner_leases"},
	})
	if _, acquired, err := busy.AcquireOwnerLease(context.Background(), "w7d-owner", time.Minute); err != nil || acquired {
		t.Fatalf("无行必须未获取: acquired=%t err=%v", acquired, err)
	}

	// Renew：更新命中。
	renew := w7dPostgresStore(t, []w7dTMStep{
		{contains: "UPDATE juhe_stats.table_monitor_owner_leases"},
	})
	if ok, err := renew.RenewOwnerLease(context.Background(), lease, time.Minute); err != nil || !ok {
		t.Fatalf("PG 续租: ok=%t err=%v", ok, err)
	}

	// Release：更新命中。
	release := w7dPostgresStore(t, []w7dTMStep{
		{contains: "UPDATE juhe_stats.table_monitor_owner_leases"},
	})
	if err := release.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatalf("PG 释放: %v", err)
	}

	// Release：未命中 → ErrOwnerLeaseLost。
	lost := w7dPostgresStore(t, []w7dTMStep{
		{contains: "UPDATE juhe_stats.table_monitor_owner_leases", zeroAffected: true},
	})
	if err := lost.ReleaseOwnerLease(context.Background(), lease); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("未命中必须判定 lease 丢失: %v", err)
	}

	// verifyOwnerLease：fence 查询无行 → ErrOwnerLeaseLost（经 WriteSample 触发）。
	verifyLost := w7dPostgresStore(t, []w7dTMStep{
		{contains: "FOR UPDATE"},
	})
	err = verifyLost.WriteSample(context.Background(), lease, collectedSample{})
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("fence 校验无行必须 lease 丢失: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 采样器 PostgreSQL 目录采集
// ---------------------------------------------------------------------------

func TestW7DCollectPostgresSamplesCatalogAndPartialFailure(t *testing.T) {
	schemaRows := func(exists string) w7dTMStep {
		return w7dTMStep{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{exists}}}
	}
	blockSize := w7dTMStep{contains: "SELECT current_setting('block_size')::bigint", cols: []string{"block_size"}, rows: [][]driver.Value{{int64(8192)}}}
	counts := w7dTMStep{contains: "SELECT", cols: []string{"table_count", "index_count"}, rows: [][]driver.Value{{int64(2), int64(3)}}}
	// 目录两行：普通表 + 分区表（父表非 NULL）。
	catalog := w7dTMStep{contains: "WITH index_summary AS (", cols: []string{
		"relname", "kind", "parent", "partition", "n_live_tup", "relpages", "index_pages", "index_count", "relsize", "idxsize", "totalsize",
	}, rows: [][]driver.Value{
		{"plain_table", "table", nil, false, int64(11), int64(4), int64(2), int32(2), int64(32768), int64(16384), int64(49152)},
		{"partitioned", "partitioned_table", "plain_table", true, int64(7), int64(2), int64(1), int32(1), int64(16384), int64(8192), int64(24576)},
	}}

	// 第一个 schema 命中完整目录；后续 schema 不存在 → 部分 + 汇总错误。
	steps := []w7dTMStep{schemaRows("true"), blockSize, counts, catalog}
	for i := 0; i < 4; i++ {
		steps = append(steps, schemaRows("false"))
	}
	db := w7dOpenTMDB(t, steps)
	cfg := Config{
		Mode:                 ModePostgres,
		MaxConcurrentSources: 1,
		MaxTables:            10,
	}
	registry := pgpool.NewRegistry()
	handle, err := registry.AcquireWith(func() (*sql.DB, error) { return db, nil }, "w7d://sampler", "source", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	cfg.PostgresPool = handle

	sampledAt := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	collected, sampleErr := collectPostgres(context.Background(), cfg, sampledAt)
	if sampleErr == nil || !strings.Contains(sampleErr.Error(), "juhe_dataset") {
		t.Fatalf("缺失 schema 必须汇总错误: %v", sampleErr)
	}
	if len(collected.databases) != 1 || collected.databases[0].Role != "business" {
		t.Fatalf("仅首个 schema 产出数据库快照: %#v", collected.databases)
	}
	database := collected.databases[0]
	if database.FileBytes == nil || *database.FileBytes != int64(73728) || database.PageCount == nil || *database.PageCount != 9 {
		t.Fatalf("数据库聚合不正确: %#v", database)
	}
	if len(collected.tables) != 2 {
		t.Fatalf("两个目录行必须产出两个表快照: %d", len(collected.tables))
	}
	partition := collected.tables[1]
	if !partition.IsPartition || partition.ParentTableName == nil || *partition.ParentTableName != "plain_table" {
		t.Fatalf("分区投影不正确: %#v", partition)
	}
	if partition.RowCount == nil || *partition.RowCount != 7 || partition.PageCount == nil || *partition.PageCount != 3 {
		t.Fatalf("分区指标不正确: %#v", partition)
	}

	// schema 存在检查错误。
	boomDB := w7dOpenTMDB(t, []w7dTMStep{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", queryErr: errors.New("w7d: schema boom")},
	})
	boomHandle, err := registry.AcquireWith(func() (*sql.DB, error) { return boomDB, nil }, "w7d://sampler-boom", "source", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boomHandle.Close() })
	cfg.PostgresPool = boomHandle
	if _, err := collectPostgres(context.Background(), cfg, sampledAt); err == nil || !strings.Contains(err.Error(), "w7d: schema boom") {
		t.Fatalf("schema 检查错误必须暴露: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Runner 调度生命周期
// ---------------------------------------------------------------------------

func w7dSQLiteFixture(t *testing.T) (Config, *Store) {
	t.Helper()
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		createSQLiteSource(t, env[key])
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.OwnerLease = 30 * time.Second
	cfg.Interval = 50 * time.Millisecond
	cfg.RunTimeout = 5 * time.Second
	cfg.RetentionDays = 7
	cfg.RetentionBatchSize = 100
	cfg.RetentionMaxBatches = 2
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return cfg, store
}

func TestW7DRunSingleCycleSamplesAndReturnsCounts(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	result, err := RunSingleCycle(context.Background(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if result.DatabaseSnapshots == 0 || result.TableSnapshots == 0 {
		t.Fatalf("单周期必须采样出快照: %#v", result)
	}
}

func TestW7DRunnerRunCyclesThenCancelAndHealthStatus(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	runner := NewRunner(cfg, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()
	// 等首轮成功后取消。
	deadline := time.Now().Add(5 * time.Second)
	for runner.Ready() == false && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !runner.Ready() {
		t.Fatal("首轮成功后必须 Ready")
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后 Run 必须返回 ctx.Err: %v", err)
	}
}

func TestW7DRunnerRunCycleFailureRecordsLastError(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	// 业务库路径指向缺失文件：首轮采样失败但调度继续（LastError 记录）。
	cfg.BusinessPath = filepath.Join(t.TempDir(), "missing.sqlite3")
	runner := NewRunner(cfg, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runner.mu.RLock()
		lastError := runner.status.LastError
		runner.mu.RUnlock()
		if lastError != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	runner.mu.RLock()
	lastError := runner.status.LastError
	lastSuccess := runner.status.LastSuccess
	runner.mu.RUnlock()
	if lastError == "" || lastSuccess.IsZero() == false {
		t.Fatalf("失败周期必须记录 LastError 且无 LastSuccess: err=%q success=%v", lastError, lastSuccess)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后必须返回 ctx.Err: %v", err)
	}
}

func TestW7DRunnerGuardsAndHealthHandler(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	// nil runner / nil store 守卫。
	var nilRunner *Runner
	if nilRunner.Ready() {
		t.Fatal("nil runner 不得 Ready")
	}
	if err := nilRunner.Run(context.Background()); err == nil {
		t.Fatal("nil runner Run 必须拒绝")
	}
	if err := NewRunner(cfg, nil, nil).Run(context.Background()); err == nil {
		t.Fatal("nil store Run 必须拒绝")
	}

	// HealthHandler：ready 200。
	runner := NewRunner(cfg, store, nil)
	runner.setOwnerHeld(true)
	runner.status.LastSuccess = time.Now().UTC()
	runner.status.LastAttempt = time.Now().UTC()
	server := httptest.NewServer(runner.HealthHandler())
	defer server.Close()
	response, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ready":true`) {
		t.Fatalf("ready 健康检查必须 200: %d %s", response.StatusCode, body)
	}

	// 未就绪 → 503。
	emptyRunner := NewRunner(cfg, store, nil)
	emptyServer := httptest.NewServer(emptyRunner.HealthHandler())
	defer emptyServer.Close()
	notReady, err := http.Get(emptyServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = notReady.Body.Close()
	if notReady.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未就绪必须 503: %d", notReady.StatusCode)
	}

	// 非 GET /health → 404。
	missing, err := http.Get(emptyServer.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("未知路径必须 404: %d", missing.StatusCode)
	}
	postResponse, err := http.Post(emptyServer.URL+"/health", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("POST 必须按未找到处理: %d", postResponse.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// config 辅助函数边界
// ---------------------------------------------------------------------------

func TestW7DConfigHelpersBoundaries(t *testing.T) {
	// positiveIntOrDefault。
	if got, err := positiveIntOrDefault("x", "", 7); err != nil || got != 7 {
		t.Fatalf("空值回落: %d %v", got, err)
	}
	if got, err := positiveIntOrDefault("x", " 3 ", 7); err != nil || got != 3 {
		t.Fatalf("合法值: %d %v", got, err)
	}
	if _, err := positiveIntOrDefault("x", "abc", 7); err == nil {
		t.Fatal("非数字必须拒绝")
	}
	if _, err := positiveIntOrDefault("x", "0", 7); err == nil {
		t.Fatal("0 必须拒绝")
	}

	root := t.TempDir()
	inside := filepath.Join(root, "child.sqlite")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// pathWithin。
	if ok, err := pathWithin(root, inside); err != nil || !ok {
		t.Fatalf("子路径必须在根内: %t %v", ok, err)
	}
	if ok, err := pathWithin(inside, root); err != nil || ok {
		t.Fatalf("父路径不得在子路径内: %t %v", ok, err)
	}
	if ok, err := pathWithin(root, root); err != nil || !ok {
		t.Fatalf("根自身必须在根内: %t %v", ok, err)
	}
	if _, err := pathWithin("", "x"); err == nil {
		t.Fatal("空根路径必须拒绝")
	}
	if _, err := pathWithin(root, ""); err == nil {
		t.Fatal("空候选路径必须拒绝")
	}

	// canonicalPath：不存在文件回落真实父目录解析。
	missing := filepath.Join(root, "not-yet.sqlite")
	resolved, err := canonicalPath(missing)
	if err != nil || !strings.EqualFold(filepath.Base(resolved), "not-yet.sqlite") {
		t.Fatalf("不存在路径必须按父目录解析: %q %v", resolved, err)
	}
	if _, err := canonicalPath("   "); err == nil {
		t.Fatal("空路径必须拒绝")
	}

	// danglingSQLiteSymlink：悬空 symlink 判定（环境不支持 symlink 时跳过）。
	dangling := filepath.Join(root, "dangling.sqlite")
	if err := os.Symlink(filepath.Join(root, "missing-target.sqlite"), dangling); err != nil {
		t.Skipf("当前环境不能创建悬空 symlink fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "missing-target.sqlite"), []byte("cleanup"), 0o600)
		_ = os.Remove(dangling)
	})
	if !danglingSQLiteSymlink(dangling) {
		t.Fatal("悬空 symlink 必须判定为真")
	}

	// equalFilesystemPath：大小写不敏感比较（Windows 语义）。
	if !equalFilesystemPath(strings.ToUpper(root), root) {
		t.Fatal("同路径不同大小写必须相等")
	}
	if equalFilesystemPath(root, root+".other") {
		t.Fatal("不同路径不得相等")
	}
}

func TestW7DOwnerLeaseContextGuards(t *testing.T) {
	if _, err := ownerLeaseFromContext(context.Background()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatal("缺少租约必须报错")
	}
	if _, err := ownerLeaseFromContext(context.WithValue(context.Background(), ownerLeaseContextKey{}, OwnerLease{OwnerID: "x", FenceToken: 0})); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatal("非法 fence token 必须报错")
	}
	lease := OwnerLease{OwnerID: "w7d", FenceToken: 3}
	if got, err := ownerLeaseFromContext(context.WithValue(context.Background(), ownerLeaseContextKey{}, lease)); err != nil || got != lease {
		t.Fatalf("合法租约必须透传: %#v %v", got, err)
	}
}
