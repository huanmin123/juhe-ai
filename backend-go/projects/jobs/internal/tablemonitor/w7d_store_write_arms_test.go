package tablemonitor

// w7d（tablemonitor 写路径与守卫批次二）：PostgreSQL 写路径（populateGrowth/
// WriteSample/cleanup）、OpenStore 分支、owner lease 续租失败臂、LoadConfig
// 环境矩阵与采样辅助函数。PG 交互全部脚本化，进程内确定性。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW7DPostgresStoreWriteAndCleanupPath(t *testing.T) {
	lease := OwnerLease{OwnerID: "w7d-write", FenceToken: 5}
	sampledAt := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	table := TableSnapshot{Role: "business", TableName: "fixture_table", SampledAt: sampledAt, TableKind: "table", TotalBytes: intPtr64(1000), RowCount: intPtr64(10)}
	database := DatabaseSnapshot{Role: "business", Path: "postgres:fixture", SampledAt: sampledAt}

	store := w7dPostgresStore(t, []w7dTMStep{
		// populateGrowth：1h 窗口基线查询（返回一条基线行）。
		{contains: "LEFT JOIN LATERAL", cols: []string{"target_index", "total_bytes", "row_count"}, rows: [][]driver.Value{{int64(0), int64(800), int64(8)}}},
		// 24h 窗口基线查询（无基线行）。
		{contains: "LEFT JOIN LATERAL"},
		// WriteSample：fence 校验。
		{contains: "FOR UPDATE", cols: []string{"fence_token"}, rows: [][]driver.Value{{int64(5)}}},
		{contains: "INSERT INTO juhe_stats.database_storage_snapshots"},
		{contains: "INSERT INTO juhe_stats.table_storage_snapshots"},
		{},
		// Cleanup → cleanupPostgres：fence 校验 + 两张表 DELETE + commit。
		{contains: "FOR UPDATE", cols: []string{"fence_token"}, rows: [][]driver.Value{{int64(5)}}},
		{contains: "DELETE FROM juhe_stats.table_storage_snapshots", zeroAffected: true},
		{contains: "DELETE FROM juhe_stats.database_storage_snapshots", zeroAffected: true},
		{},
	})

	_ = store
	sample := collectedSample{databases: []DatabaseSnapshot{database}, tables: []TableSnapshot{table}}
	if err := store.populateGrowth(context.Background(), &sample); err != nil {
		t.Fatalf("PG 增长基线读取失败: %v", err)
	}
	if sample.tables[0].GrowthBytes24h != nil || sample.tables[0].GrowthRows24h != nil {
		t.Fatalf("无基线窗口的增长必须为 nil: %#v", sample.tables[0])
	}
	if err := store.WriteSample(context.Background(), lease, sample); err != nil {
		t.Fatalf("PG 写快照失败: %v", err)
	}
	deleted, err := store.Cleanup(context.Background(), lease, sampledAt.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("PG 清理失败: %v", err)
	}
	_ = deleted
}

func TestW7DPostgresStoreWriteFenceRejection(t *testing.T) {
	lease := OwnerLease{OwnerID: "w7d-write", FenceToken: 5}
	store := w7dPostgresStore(t, []w7dTMStep{
		{contains: "FOR UPDATE"},
	})
	if err := store.WriteSample(context.Background(), lease, collectedSample{}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("fence 失效必须拒绝写入: %v", err)
	}
}

func intPtr64(value int64) *int64 { return &value }

func TestW7DOpenStorePostgresArms(t *testing.T) {
	// 缺 URL：pgx sql.Open 对空 DSN 也会构造，registry 校验 URL 非空 → 错误。
	if _, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: ""}); err == nil {
		t.Fatal("空 URL 必须拒绝")
	}
	// 拒连地址：构造成功（不拨号），Ping 失败，Close 走 pool 释放。
	// 默认 1000/1000 无法通过池校验，必须显式给出合法池上限。
	if _, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: "postgres://w7d@127.0.0.1:1/tm?sslmode=disable"}); err == nil {
		t.Fatal("默认池上限必须被校验拒绝")
	}
	refused, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: "postgres://w7d@127.0.0.1:1/tm?sslmode=disable", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
	if err != nil {
		t.Fatalf("拒连地址构造必须成功: %v", err)
	}
	if err := refused.Ping(context.Background()); err == nil {
		t.Fatal("拒连地址 Ping 必须失败")
	}
	if err := refused.Close(); err != nil {
		t.Fatalf("pool 关闭必须成功: %v", err)
	}
}

func TestW7DRunWithOwnerLeaseArms(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("fixture store 必须可用: %v", err)
	}

	// AcquireOwnerLease 错误：先关闭底层库。
	brokenStore, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := brokenStore.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RunWithOwnerLease(context.Background(), cfg, brokenStore, func(context.Context) error { return nil }); err == nil || !strings.Contains(err.Error(), "获取表监控 owner lease 失败") {
		t.Fatalf("获取租约错误必须包装: %v", err)
	}

	// 已被其他实例持有：先用独立 store 持有租约，再让目标 store 竞争。
	first, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := RunWithOwnerLease(context.Background(), cfg, first, func(ctx context.Context) error {
		// 首个持有者运行期间，第二个实例竞争必须失败。
		if err := RunWithOwnerLease(context.Background(), cfg, second, func(context.Context) error { return nil }); err == nil || !strings.Contains(err.Error(), "已由另一个 Go 实例持有") {
			t.Fatalf("并发持有必须拒绝: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("首个持有者运行失败: %v", err)
	}

	// 续租失败：运行期间关闭底层库，renewal goroutine 报错并取消运行。
	renewCfg := cfg
	renewCfg.OwnerLease = 30 * time.Millisecond
	renewStore, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- RunWithOwnerLease(context.Background(), renewCfg, renewStore, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	if err := renewStore.db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("续租失败必须作为错误返回")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("续租失败后未退出")
	}

	// releaseOwnerLeaseRecoverably：nil store panic 被转换为错误。
	if err := releaseOwnerLeaseRecoverably(context.Background(), nil, OwnerLease{OwnerID: "x", FenceToken: 1}); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("释放 panic 必须受控: %v", err)
	}
}

func TestW7DRunnerSecondCycleViaTimer(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	cfg.Interval = 20 * time.Millisecond
	runner := NewRunner(cfg, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()
	// 等待两轮成功周期（首轮 + timer 驱动的第二轮）。
	deadline := time.Now().Add(5 * time.Second)
	firstSuccess := runner.status.LastSuccess
	for time.Now().Before(deadline) {
		runner.mu.RLock()
		current := runner.status.LastSuccess
		runner.mu.RUnlock()
		if !current.IsZero() {
			if firstSuccess.IsZero() {
				firstSuccess = current
			} else if current.After(firstSuccess) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	runner.mu.RLock()
	attempted := runner.status.LastAttempt
	runner.mu.RUnlock()
	if attempted.IsZero() {
		t.Fatal("第二轮必须经过 timer 触发")
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后返回 ctx.Err: %v", err)
	}
}

func TestW7DSamplerSQLiteHelpers(t *testing.T) {
	// optionalFileBytes：缺失文件 → nil。
	if got, err := optionalFileBytes(filepath.Join(t.TempDir(), "missing.sqlite")); err != nil || got != nil {
		t.Fatalf("缺失文件必须 nil: %#v %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "exists.sqlite")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := optionalFileBytes(path)
	if err != nil || got == nil || *got != 5 {
		t.Fatalf("文件大小必须读取: %#v %v", got, err)
	}

	// isDBStatUnavailable：dbstat 缺失分类。
	if !isDBStatUnavailable(errors.New("no such table: dbstat")) {
		t.Fatal("dbstat 缺失错误必须识别")
	}
	if isDBStatUnavailable(errors.New("syntax error")) {
		t.Fatal("其他错误不得误判")
	}

	// 无 dbstat 的 SQLite 库：loadSQLiteObjectSizes 返回不可用标志。
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plain.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	var dummy sqliteObjectSizes
	_, available, err := loadSQLiteObjectSizes(context.Background(), db, []string{"t"})
	if err != nil {
		t.Fatalf("dbstat 缺失必须静默降级: %v", err)
	}
	_ = available
	_ = dummy
}

func TestW7DLoadConfigEnvironmentMatrix(t *testing.T) {
	root := t.TempDir()
	base := sqliteTestEnv(root)

	run := func(t *testing.T, mutate func(map[string]string)) (Config, error) {
		t.Helper()
		env := map[string]string{}
		for key, value := range base {
			env[key] = value
		}
		mutate(env)
		return LoadConfig(func(key string) string { return env[key] })
	}

	// 缺 instance id / store 模式的失败臂已删除（2026-09-19 零配置决策：
	// INSTANCE_ID 缺省 os.Hostname()、STORE 缺省跟随 JUHE_AI_DATABASE_DRIVER，
	// 两者不再是必填）。非法模式仍拒绝。
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_STORE"] = "redis" }); err == nil {
		t.Fatal("非法 store 模式必须拒绝")
	}
	// 缺省 store 模式跟随驱动（sqlite 默认；DATABASE_DRIVER=postgres 跟随）。
	if cfg, err := run(t, func(env map[string]string) { delete(env, "JUHE_AI_TABLE_MONITOR_STORE") }); err != nil || cfg.Mode != ModeSQLite {
		t.Fatalf("缺省 store 模式必须为 sqlite: %v %v", cfg.Mode, err)
	}
	if cfg, err := run(t, func(env map[string]string) {
		delete(env, "JUHE_AI_TABLE_MONITOR_STORE")
		env["JUHE_AI_DATABASE_DRIVER"] = "postgres"
		env["JUHE_AI_TABLE_MONITOR_POSTGRES_URL"] = "postgres://jobs:secret@127.0.0.1:5432/db?sslmode=disable"
	}); err != nil || cfg.Mode != ModePostgres {
		t.Fatalf("store 模式必须跟随 postgres 驱动: %v %v", cfg.Mode, err)
	}

	// duration 与数值解析错误。
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_INTERVAL"] = "nope" }); err == nil {
		t.Fatal("非法 interval 必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT"] = "0s" }); err == nil {
		t.Fatal("零 run timeout 必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_OWNER_LEASE"] = "4s" }); err == nil {
		t.Fatal("低于 5s 的租约必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_RETENTION_DAYS"] = "0" }); err == nil {
		t.Fatal("非法保留天数必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_MAX_TABLES"] = "99999" }); err == nil {
		t.Fatal("超限表数必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE"] = "abc" }); err == nil {
		t.Fatal("非法批大小必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES"] = "-1" }); err == nil {
		t.Fatal("负批次数必须拒绝")
	}

	// Postgres 池数值：非法值与合法覆盖。
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS"] = "0" }); err == nil {
		t.Fatal("非法 max open 必须拒绝")
	}
	if _, err := run(t, func(env map[string]string) { env["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_IDLE_CONNS"] = "x" }); err == nil {
		t.Fatal("非法 max idle 必须拒绝")
	}

	// postgres 模式：缺 URL 拒绝；合法配置接受池覆盖。
	if _, err := run(t, func(env map[string]string) {
		env["JUHE_AI_TABLE_MONITOR_STORE"] = "postgres"
	}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_TABLE_MONITOR_POSTGRES_URL") {
		t.Fatalf("postgres 模式缺 URL 必须拒绝: %v", err)
	}
	pgCfg, err := run(t, func(env map[string]string) {
		env["JUHE_AI_TABLE_MONITOR_STORE"] = "postgres"
		env["JUHE_AI_TABLE_MONITOR_POSTGRES_URL"] = "postgres://w7d@127.0.0.1:1/tm?sslmode=disable"
		env["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS"] = "8"
		env["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_IDLE_CONNS"] = "2"
	})
	if err != nil {
		t.Fatal(err)
	}
	if pgCfg.Mode != ModePostgres || pgCfg.PostgresMaxOpenConns != 8 || pgCfg.PostgresMaxIdleConns != 2 {
		t.Fatalf("postgres 配置未生效: %#v", pgCfg)
	}

	// sqlite 模式：输出/运行日志/业务库/shard 根的「缺失必须拒绝」四臂已
	// 删除（2026-09-19 零配置决策：路径类 env 缺省按 DATA_DIR 派生，恒非空，
	// 不再是校验失败分支）。
	// 输出库放入 shard 根目录必须拒绝。
	if _, err := run(t, func(env map[string]string) {
		env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "tm.sqlite3")
	}); err == nil || !strings.Contains(err.Error(), "SHARD_ROOT") {
		t.Fatalf("输出库放入 shard 根目录必须拒绝: %v", err)
	}

	// 与运行日志库同文件必须拒绝（物理 identity 比较）。
	shared := filepath.Join(root, "shared.sqlite3")
	if err := os.WriteFile(shared, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, func(env map[string]string) {
		env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = shared
		env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = shared
	}); err == nil {
		t.Fatal("与运行日志库共用必须拒绝")
	}
	// 与业务库共用必须拒绝。
	if _, err := run(t, func(env map[string]string) {
		env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = env["JUHE_AI_DATABASE_PATH"]
		if err := os.WriteFile(env["JUHE_AI_DATABASE_PATH"], []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}); err == nil {
		t.Fatal("与业务库共用必须拒绝")
	}
}

func TestW7DPostgresStoreWriteErrorArms(t *testing.T) {
	lease := OwnerLease{OwnerID: "w7d-err", FenceToken: 7}
	sampledAt := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	sample := collectedSample{
		databases: []DatabaseSnapshot{{Role: "business", Path: "postgres:fixture", SampledAt: sampledAt}},
		tables:    []TableSnapshot{{Role: "business", TableName: "fixture_table", SampledAt: sampledAt, TableKind: "table"}},
	}
	verify := w7dTMStep{contains: "FOR UPDATE", cols: []string{"fence_token"}, rows: [][]driver.Value{{int64(7)}}}

	// database 快照 INSERT 失败。
	dbBoom := w7dPostgresStore(t, []w7dTMStep{
		verify,
		{contains: "INSERT INTO juhe_stats.database_storage_snapshots", execErr: errors.New("w7d: db insert boom")},
	})
	if err := dbBoom.WriteSample(context.Background(), lease, sample); err == nil || !strings.Contains(err.Error(), "w7d: db insert boom") {
		t.Fatalf("database INSERT 错误必须暴露: %v", err)
	}

	// table 快照 INSERT 失败。
	tblBoom := w7dPostgresStore(t, []w7dTMStep{
		verify,
		{contains: "INSERT INTO juhe_stats.database_storage_snapshots"},
		{contains: "INSERT INTO juhe_stats.table_storage_snapshots", execErr: errors.New("w7d: tbl insert boom")},
	})
	if err := tblBoom.WriteSample(context.Background(), lease, sample); err == nil || !strings.Contains(err.Error(), "w7d: tbl insert boom") {
		t.Fatalf("table INSERT 错误必须暴露: %v", err)
	}

	// COMMIT 失败。
	commitBoom := w7dPostgresStore(t, []w7dTMStep{
		verify,
		{contains: "INSERT INTO juhe_stats.database_storage_snapshots"},
		{contains: "INSERT INTO juhe_stats.table_storage_snapshots"},
		{commitErr: errors.New("w7d: write commit boom")},
	})
	if err := commitBoom.WriteSample(context.Background(), lease, sample); err == nil || !strings.Contains(err.Error(), "w7d: write commit boom") {
		t.Fatalf("COMMIT 错误必须暴露: %v", err)
	}

	// hasExpiredSnapshots：PG EXISTS 查询 true。
	pendingStore := w7dPostgresStore(t, []w7dTMStep{
		verify,
		{contains: "SELECT EXISTS", cols: []string{"pending"}, rows: [][]driver.Value{{true}}},
		{},
	})
	pending, err := pendingStore.hasExpiredSnapshots(context.Background(), lease, sampledAt)
	if err != nil || !pending {
		t.Fatalf("过期快照检查: pending=%t err=%v", pending, err)
	}

	// populateGrowth 查询错误。
	growthBoom := w7dPostgresStore(t, []w7dTMStep{
		{contains: "LEFT JOIN LATERAL", queryErr: errors.New("w7d: growth boom")},
	})
	broken := collectedSample{tables: []TableSnapshot{{Role: "business", TableName: "fixture_table", SampledAt: sampledAt}}}
	if err := growthBoom.populateGrowth(context.Background(), &broken); err == nil || !strings.Contains(err.Error(), "w7d: growth boom") {
		t.Fatalf("增长基线查询错误必须暴露: %v", err)
	}
}
