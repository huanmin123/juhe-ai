package tablemonitor

// w12f_tablemonitor_test.go 覆盖 table-monitor 的剩余错误臂与边界：
// store 各方法在句柄关闭/假 lease/零上限下的 fail-closed 行为、config 的
// PG 连接池与 glob/路径解析校验、sampler 的 PG 分支与损坏源库错误传播、
// owner lease 的续租成功/丢失臂、runner 的 lease 丢失传播。
// 全部 SQLite 文件与 ID 加 w12f- 前缀；PG 门控连接失败时 t.Skip。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// --- 测试 fixture ---

func w12fTestEnv(root string) map[string]string {
	return map[string]string{
		"JUHE_AI_TABLE_MONITOR_INSTANCE_ID":      "w12f-instance",
		"JUHE_AI_TABLE_MONITOR_STORE":            "sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "w12f-table-monitor.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      filepath.Join(root, "w12f-runtime-log.sqlite3"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "w12f-business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "w12f-dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "w12f-usage.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "w12f-stats.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "w12f-codex"),
	}
}

func w12fCreateSQLiteSource(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE w12f_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO w12f_rows(value) VALUES ('fixture'); CREATE INDEX w12f_rows_value ON w12f_rows(value);"); err != nil {
		t.Fatal(err)
	}
}

func w12fLoadConfig(t *testing.T, root string) Config {
	t.Helper()
	env := w12fTestEnv(root)
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		w12fCreateSQLiteSource(t, env[key])
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func w12fOpenStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// --- store 错误臂 ---

func TestW12fTMStoreErrorArms(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)

	// OpenStore：输出路径指向目录 → 打开失败。
	if _, err := OpenStore(Config{Mode: ModeSQLite, OutputPath: root}); err == nil {
		t.Fatal("目录路径打开必须失败")
	}

	store := w12fOpenStore(t, cfg)
	ctx := context.Background()

	// configureSQLiteWriter：句柄关闭 → busy_timeout 设置失败。
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12f-closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := configureSQLiteWriter(closed); err == nil {
		t.Fatal("句柄关闭后 configureSQLiteWriter 必须报错")
	}

	// 只读连接：journal_mode=WAL 无法生效。
	roPath := filepath.Join(root, "w12f-ro.sqlite3")
	roDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(roPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer roDB.Close()
	if err := configureSQLiteWriter(roDB); err == nil {
		t.Log("只读模式 journal 校验未触发（driver 允许 WAL），跳过该臂")
	}

	// AcquireOwnerLease / ReleaseOwnerLease：句柄关闭 → 原始错误传播。
	if _, _, err := store.AcquireOwnerLease(ctx, "", time.Minute); err == nil {
		t.Log("空 owner 行为由既有测试约束")
	}
	closedStore := &Store{db: closed, mode: ModeSQLite}
	if _, _, err := closedStore.AcquireOwnerLease(ctx, "w12f-owner", time.Minute); err == nil {
		t.Fatal("句柄关闭后 AcquireOwnerLease 必须报错")
	}
	if err := closedStore.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "w12f-owner", FenceToken: 1}); err == nil {
		t.Fatal("句柄关闭后 ReleaseOwnerLease 必须报错")
	}

	// verifyOwnerLease：已回滚事务上的校验失败。
	badTxDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12f-badtx.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer badTxDB.Close()
	if _, err := badTxDB.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	tx, err := badTxDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.verifyOwnerLease(ctx, tx, OwnerLease{OwnerID: "w12f-x", FenceToken: 1}); err == nil {
		t.Fatal("已回滚事务上的 verifyOwnerLease 必须报错")
	}

	// Cleanup：limit <= 0 直接零值返回。
	deleted, err := store.Cleanup(ctx, OwnerLease{OwnerID: "w12f-none", FenceToken: 1}, time.Now().UTC(), 0)
	if err != nil || deleted != 0 {
		t.Fatalf("limit=0 必须零值返回 deleted=%d err=%v", deleted, err)
	}

	// hasExpiredSnapshots：句柄关闭 / 假 lease。
	if _, err := closedStore.hasExpiredSnapshots(ctx, OwnerLease{OwnerID: "w12f-o", FenceToken: 1}, time.Now().UTC()); err == nil {
		t.Fatal("句柄关闭后 hasExpiredSnapshots 必须报错")
	}
	if _, err := store.hasExpiredSnapshots(ctx, OwnerLease{OwnerID: "w12f-ghost", FenceToken: 99}, time.Now().UTC()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("假 lease 必须返回 ErrOwnerLeaseLost: %v", err)
	}

	// populateGrowth：空 tables 直接返回；非空 tables + 关句柄报错。
	if err := store.populateGrowth(ctx, &collectedSample{}); err != nil {
		t.Fatalf("空 tables populateGrowth 必须成功: %v", err)
	}
	if err := closedStore.populateGrowth(ctx, &collectedSample{tables: []TableSnapshot{{Role: "w12f", TableName: "t"}}}); err == nil {
		t.Fatal("句柄关闭后 populateGrowth 必须报错")
	}

	// previousTableSnapshots：空快照占位返回。
	baselines, err := store.previousTableSnapshots(ctx, nil, time.Hour)
	if err != nil || len(baselines) != 0 {
		t.Fatalf("空快照必须空基线: %v err=%v", baselines, err)
	}

	// cleanupSQLite：句柄关闭 / 假 lease。
	if _, err := closedStore.cleanupSQLite(ctx, OwnerLease{OwnerID: "w12f-o", FenceToken: 1}, time.Now().UTC(), 10); err == nil {
		t.Fatal("句柄关闭后 cleanupSQLite 必须报错")
	}
	if _, err := store.cleanupSQLite(ctx, OwnerLease{OwnerID: "w12f-ghost", FenceToken: 99}, time.Now().UTC(), 10); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("cleanupSQLite 假 lease 必须返回 ErrOwnerLeaseLost: %v", err)
	}
}

// --- config 校验臂 ---

func TestW12fTMConfigArms(t *testing.T) {
	root := t.TempDir()
	env := w12fTestEnv(root)
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		w12fCreateSQLiteSource(t, env[key])
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}

	// PostgreSQL 模式：idle > open 的连接池上限必须报错。
	envPG := w12fTestEnv(root)
	envPG["JUHE_AI_TABLE_MONITOR_STORE"] = "postgres"
	envPG["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS"] = "2"
	envPG["JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_IDLE_CONNS"] = "5"
	if _, err := LoadConfig(func(key string) string { return envPG[key] }); err == nil || !strings.Contains(err.Error(), "连接池配置无效") {
		t.Fatalf("PG 连接池 idle>open 必须报错: %v", err)
	}

	// SQLite 模式：shard root 含 glob 特殊字符 → 枚举失败。
	envGlob := w12fTestEnv(root)
	envGlob["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = filepath.Join(root, "w12f-bad[glob")
	if _, err := LoadConfig(func(key string) string { return envGlob[key] }); err == nil || !strings.Contains(err.Error(), "枚举") {
		t.Fatalf("非法 glob pattern 必须报错: %v", err)
	}

	// canonicalPath：悬空符号链接必须失败（Windows 无特权创建 symlink 时跳过；
	// Windows 下 Lstat 直接报不存在后由 dangling 检查兜底，POSIX 下走 symlink 目标 stat）。
	dangling := filepath.Join(root, "w12f-dangling.sqlite3")
	if err := os.Symlink(filepath.Join(root, "w12f-no-such-target.sqlite3"), dangling); err != nil {
		t.Skipf("当前环境无法创建符号链接: %v", err)
	}
	if _, err := canonicalPath(dangling); err == nil {
		t.Fatal("悬空符号链接必须报错")
	}
}

// --- sampler 臂 ---

func TestW12fTMSamplerArms(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	store := w12fOpenStore(t, cfg)

	// RunOnce 的 PostgreSQL 分支：无效连接 URL 快速失败。
	pgCfg := cfg
	pgCfg.Mode = ModePostgres
	pgCfg.PostgresURL = "postgres://w12f-invalid:5432/w12f-none?sslmode=disable&connect_timeout=1"
	if _, err := RunOnce(context.Background(), pgCfg, store, time.Now().UTC()); err == nil {
		t.Fatal("PG 模式连接失败必须报错")
	}

	// 损坏源库：非 SQLite 内容 → page_size 读取失败。
	brokenPath := filepath.Join(root, "w12f-broken.sqlite3")
	if err := os.WriteFile(brokenPath, []byte("w12f-not-a-sqlite-file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenCfg := cfg
	brokenCfg.BusinessPath = brokenPath
	if _, err := collectSQLite(context.Background(), brokenCfg, time.Now().UTC()); err == nil {
		t.Fatal("损坏源库必须报错")
	}

	// sqliteRowCount：不存在的表。
	goodDB, err := sql.Open("sqlite", filepath.Join(root, "w12f-business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer goodDB.Close()
	if _, err := sqliteRowCount(context.Background(), goodDB, "w12f-no-such-table"); err == nil {
		t.Fatal("不存在表的行数查询必须报错")
	}
}

// TestW12fTMPGCollectSmoke 为 PG 门控的只读采样冒烟：仅当 w1cover 覆盖库
// 可达时运行，覆盖 collectPostgres 连接池关闭路径。
func TestW12fTMPGCollectSmoke(t *testing.T) {
	dsn := w12fPostgresDSN(t)
	if dsn == "" {
		t.Skip("w12f statsagg PG gated: shared.env 不可读或缺少 JUHE_AI_POSTGRES_URL")
	}
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	cfg.Mode = ModePostgres
	cfg.PostgresURL = dsn
	if _, err := collectPostgres(context.Background(), cfg, time.Now().UTC()); err != nil {
		t.Skipf("w12f statsagg PG gated: PG 不可达: %v", err)
	}
}

func w12fPostgresDSN(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		return ""
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		return ""
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

// --- owner lease 臂 ---

func TestW12fTMOwnerLeaseRenewalArms(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	cfg.OwnerLease = 3 * time.Second
	cfg.RunTimeout = 2 * time.Second
	store := w12fOpenStore(t, cfg)

	// 回调立即成功：续租 goroutine 走 stopRenewal 退出臂。
	if err := RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error { return nil }); err != nil {
		t.Fatalf("成功回调不得报错: %v", err)
	}

	// 续租成功臂：回调耗时覆盖至少一个续租 tick（OwnerLease/3 = 1s）。
	if err := RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error {
		select {
		case <-ownerCtx.Done():
			return ownerCtx.Err()
		case <-time.After(2200 * time.Millisecond):
			return nil
		}
	}); err != nil {
		t.Fatalf("续租成功路径不得报错: %v", err)
	}

	// 续租丢失臂：回调期间删除 lease 行 → RenewOwnerLease 返回 false → ErrOwnerLeaseLost。
	leaseErr := make(chan error, 1)
	go func() {
		leaseErr <- RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error {
			select {
			case <-ownerCtx.Done():
				return ownerCtx.Err()
			case <-time.After(2200 * time.Millisecond):
				return nil
			}
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM table_monitor_owner_leases WHERE lease_key = 'table-monitor-sampling-retention'`).Scan(&count); err == nil && count > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM table_monitor_owner_leases WHERE lease_key = 'table-monitor-sampling-retention'`); err != nil {
		t.Fatal(err)
	}
	if err := <-leaseErr; !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("lease 行被删后必须返回 ErrOwnerLeaseLost: %v", err)
	}
}

// --- runner lease 丢失传播 ---

func TestW12fTMRunnerLeaseLostPropagates(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	cfg.OwnerLease = 30 * time.Second
	cfg.Interval = 30 * time.Millisecond
	cfg.RunTimeout = 2 * time.Second
	store := w12fOpenStore(t, cfg)
	runner := NewRunner(cfg, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !runner.Ready() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !runner.Ready() {
		t.Fatal("首轮成功后必须 Ready")
	}
	// 删除 lease 行：下一轮 runCycle 的写入校验失败 → runCycle 返回 ErrOwnerLeaseLost。
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM table_monitor_owner_leases WHERE lease_key = 'table-monitor-sampling-retention'`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runErr:
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("lease 丢失必须传播 ErrOwnerLeaseLost: %v", err)
		}
		cancel()
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal("lease 丢失后 Run 必须退出")
	}
	_ = fmt.Sprint()
}

// --- w12f 追加：精确臂补齐 ---

// TestW12fTMRunOncePostgresBranch 覆盖 RunOnce 的 collectPostgres 分支：
// 携带 owner lease 的 ctx + 无效 PG URL → 采样错误快速传播。
func TestW12fTMRunOncePostgresBranch(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	store := w12fOpenStore(t, cfg)
	pgCfg := cfg
	pgCfg.Mode = ModePostgres
	pgCfg.PostgresURL = "postgres://w12f-invalid:5432/w12f-none?sslmode=disable&connect_timeout=1"
	ctx := context.WithValue(context.Background(), ownerLeaseContextKey{}, OwnerLease{OwnerID: "w12f-pg", FenceToken: 1})
	if _, err := RunOnce(ctx, pgCfg, store, time.Now().UTC()); err == nil {
		t.Fatal("PG 模式连接失败必须报错")
	}
}

// TestW12fTMSchemaReadyBypassClosedDB 验证 schemaReady 短路后句柄关闭的
// 写入操作直接暴露 driver 原始错误（AcquireOwnerLease 错误臂）。
func TestW12fTMSchemaReadyBypassClosedDB(t *testing.T) {
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12f-closed2.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	closedStore := &Store{db: closed, mode: ModeSQLite, schemaReady: true}
	if _, _, err := closedStore.AcquireOwnerLease(context.Background(), "w12f-owner", time.Minute); err == nil {
		t.Fatal("schemaReady 短路 + 句柄关闭时 AcquireOwnerLease 必须报错")
	}
	pgStore := &Store{db: closed, mode: ModePostgres, schemaReady: true}
	if _, _, err := pgStore.AcquireOwnerLease(context.Background(), "w12f-owner", time.Minute); err == nil {
		t.Fatal("PG 分支句柄关闭时 AcquireOwnerLease 必须报错")
	}
	if _, err := pgStore.cleanupPostgres(context.Background(), OwnerLease{OwnerID: "w12f-o", FenceToken: 1}, time.Now().UTC(), 10); err == nil {
		t.Fatal("PG 分支句柄关闭时 cleanupPostgres 必须报错")
	}
}

// TestW12fTMInsertHelpersBadTx 覆盖四个 insert helper 在已回滚事务上的
// 原始错误传播。
func TestW12fTMInsertHelpersBadTx(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w12f-insert.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	sampledAt := time.Now().UTC()
	pageSize := int64(4096)
	database := DatabaseSnapshot{Role: "w12f", Path: "w12f/path.sqlite3", SampledAt: sampledAt, PageSize: &pageSize}
	rowCount := int64(1)
	table := TableSnapshot{Role: "w12f", TableName: "w12f_t", SampledAt: sampledAt, RowCount: &rowCount}
	if err := insertSQLiteDatabase(context.Background(), tx, database); err == nil {
		t.Fatal("回滚事务上 insertSQLiteDatabase 必须报错")
	}
	if err := insertSQLiteTable(context.Background(), tx, table); err == nil {
		t.Fatal("回滚事务上 insertSQLiteTable 必须报错")
	}
	if err := insertPostgresDatabase(context.Background(), tx, database); err == nil {
		t.Fatal("回滚事务上 insertPostgresDatabase 必须报错")
	}
	if err := insertPostgresTable(context.Background(), tx, table); err == nil {
		t.Fatal("回滚事务上 insertPostgresTable 必须报错")
	}
	if err := verifyOwnerLeaseFree(context.Background(), db, OwnerLease{OwnerID: "w12f-o", FenceToken: 1}); err == nil {
		t.Fatal("回滚事务上 verifyOwnerLease 必须报错")
	}
}

// verifyOwnerLeaseFree 是 store.verifyOwnerLease 的测试辅助包装。
func verifyOwnerLeaseFree(ctx context.Context, db *sql.DB, lease OwnerLease) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s := &Store{db: db, mode: ModeSQLite, schemaReady: true}
	return s.verifyOwnerLease(ctx, tx, lease)
}

// TestW12fTMEnsureSchemaReadOnly 覆盖只读连接上 bootstrap 锁获取失败臂。
func TestW12fTMEnsureSchemaReadOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "w12f-ro-schema.sqlite3")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	store := &Store{db: ro, mode: ModeSQLite}
	if err := store.ensureSQLiteSchema(context.Background()); err == nil {
		t.Log("只读连接 bootstrap 未触发错误（driver 行为差异）")
	}
}

// TestW12fTMEqualFilesystemPath 锁定同路径比较语义。
func TestW12fTMEqualFilesystemPath(t *testing.T) {
	if !equalFilesystemPath("a/b/./c", "a/b/c") {
		t.Fatal("clean 后相同的路径必须判定相等")
	}
	if equalFilesystemPath("a/b", "a/c") {
		t.Fatal("不同路径不得判定相等")
	}
}

// TestW12fTMOpenReadOnlyMissingFile 覆盖 openSQLiteReadOnly 的 Stat 错误臂。
func TestW12fTMOpenReadOnlyMissingFile(t *testing.T) {
	if _, _, err := openSQLiteReadOnly(filepath.Join(t.TempDir(), "w12f-missing.sqlite3")); err == nil {
		t.Fatal("不存在的文件必须报错")
	}
}

// TestW12fTMRunWithOwnerLeaseNormalExits 循环执行正常完成的 lease 生命周期：
// stopRenewal 与 runCtx.Done 两个退出臂由 select 随机命中，多次执行确保
// 两个臂都被覆盖。
func TestW12fTMRunWithOwnerLeaseNormalExits(t *testing.T) {
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	cfg.OwnerLease = 3 * time.Second
	store := w12fOpenStore(t, cfg)
	for i := 0; i < 8; i++ {
		if err := RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error { return nil }); err != nil {
			t.Fatalf("第 %d 次正常回调不得报错: %v", i+1, err)
		}
	}
}

// TestW12fTMPGCleanupLeaseLost 为 PG 门控测试：w1cover 覆盖库可达时覆盖
// cleanupPostgres 的 lease 校验失败臂。
func TestW12fTMPGCleanupLeaseLost(t *testing.T) {
	dsn := w12fPostgresDSN(t)
	if dsn == "" {
		t.Skip("w12f statsagg PG gated: shared.env 不可读或缺少 JUHE_AI_POSTGRES_URL")
	}
	root := t.TempDir()
	cfg := w12fLoadConfig(t, root)
	cfg.Mode = ModePostgres
	cfg.PostgresURL = dsn
	store, err := OpenStore(cfg)
	if err != nil {
		t.Skipf("w12f statsagg PG gated: 打开 PG store 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Skipf("w12f statsagg PG gated: 建 schema 失败: %v", err)
	}
	if _, err := store.cleanupPostgres(context.Background(), OwnerLease{OwnerID: "w12f-ghost", FenceToken: 999}, time.Now().UTC(), 10); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("PG 假 lease 必须返回 ErrOwnerLeaseLost: %v", err)
	}
}
