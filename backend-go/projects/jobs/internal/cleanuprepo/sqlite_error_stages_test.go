package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// SQLite 链路的「按阶段注入失败」测试：以装饰器包装 modernc/sqlite 真实
// 连接，对命中子串的语句注入错误，覆盖各 SQLite 主链的错误分支。种数据
// 与建表都走普通句柄，只有被测 store 使用注入句柄。

// ---- SQLite 失败注入装饰器 ----

type kitFailingSQLiteConn struct {
	raw    driver.Conn
	exec   driver.ExecerContext
	query  driver.QueryerContext
	failOn string
}

func (c *kitFailingSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	return c.raw.Prepare(query)
}
func (c *kitFailingSQLiteConn) Close() error              { return c.raw.Close() }
func (c *kitFailingSQLiteConn) Begin() (driver.Tx, error) { return c.raw.Begin() }

// PrepareContext 让 PrepareContext 路径的语句（如 tx.PrepareContext）同样
// 经过注入检查。
func (c *kitFailingSQLiteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if strings.Contains(query, c.failOn) {
		return nil, fmt.Errorf("kitFailingSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	if prep, ok := c.raw.(driver.ConnPrepareContext); ok {
		return prep.PrepareContext(ctx, query)
	}
	return c.raw.Prepare(query)
}

func (c *kitFailingSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, c.failOn) {
		return nil, fmt.Errorf("kitFailingSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	if c.exec == nil {
		return nil, driver.ErrSkip
	}
	return c.exec.ExecContext(ctx, query, args)
}

func (c *kitFailingSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, c.failOn) {
		return nil, fmt.Errorf("kitFailingSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	if c.query == nil {
		return nil, driver.ErrSkip
	}
	return c.query.QueryContext(ctx, query, args)
}

type kitFailingSQLiteConnector struct {
	dsn    string
	failOn string
}

func (c kitFailingSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	conn := &kitFailingSQLiteConn{raw: raw, failOn: c.failOn}
	if exec, ok := raw.(driver.ExecerContext); ok {
		conn.exec = exec
	}
	if query, ok := raw.(driver.QueryerContext); ok {
		conn.query = query
	}
	return conn, nil
}

func (c kitFailingSQLiteConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func openKitFailingSQLite(path, failOn string) *sql.DB {
	db := sql.OpenDB(kitFailingSQLiteConnector{dsn: path, failOn: failOn})
	db.SetMaxOpenConns(1)
	return db
}

func openKitFailingSQLiteDB(t *testing.T, path, failOn string) *DB {
	t.Helper()
	db := openKitFailingSQLite(path, failOn)
	t.Cleanup(func() { _ = db.Close() })
	return &DB{DB: db}
}

func runSQLiteStages(t *testing.T, stages []pgStage, build func(t *testing.T, stage pgStage) error) {
	t.Helper()
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			if err := build(t, stage); err == nil {
				t.Fatalf("阶段 %s（failOn=%s）应产生错误", stage.name, stage.failOn)
			}
		})
	}
}

// ---- stage fixture：三库（dataset/stats/catalog）双句柄 + 分片 opener ----

type kitSQLiteStageFixture struct {
	dir                     string
	dataset, stats, catalog *DB
	seedDataset, seedStats  *DB
	seedCatalog             *DB
	root                    string
}

func newKitSQLiteStageFixture(t *testing.T, failOn string) *kitSQLiteStageFixture {
	t.Helper()
	f := &kitSQLiteStageFixture{dir: t.TempDir()}
	seedPaths := map[string]string{}
	mk := func(name string, schema func(*testing.T, *sql.DB)) *DB {
		path := filepath.Join(f.dir, name+".sqlite3")
		seedDB, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open seed %s: %v", name, err)
		}
		t.Cleanup(func() { _ = seedDB.Close() })
		seedDB.SetMaxOpenConns(1)
		seedPaths[name] = path
		schema(t, seedDB)
		return &DB{DB: seedDB}
	}
	f.seedDataset = mk("dataset", func(t *testing.T, db *sql.DB) { createKitTargetsSchema(t, db) })
	f.seedStats = mk("stats", func(t *testing.T, db *sql.DB) { createKitStatsChainSchema(t, db) })
	f.seedCatalog = mk("catalog", func(t *testing.T, db *sql.DB) { createKitUsageCatalogSchema(t, db) })
	// 对应的失败注入句柄（同一文件，独立连接）。
	f.dataset = openKitFailingSQLiteDB(t, seedPaths["dataset"], failOn)
	f.stats = openKitFailingSQLiteDB(t, seedPaths["stats"], failOn)
	f.catalog = openKitFailingSQLiteDB(t, seedPaths["catalog"], failOn)
	f.root = filepath.Join(f.dir, "shards")
	return f
}

// addKitStageShard 通过注入 opener 的分片库种一条记录与目录。
func (f *kitSQLiteStageFixture) addKitStageShard(t *testing.T, shardKey, fileKey string, withCursor bool) {
	f.addKitStageShardWithID(t, shardKey, fileKey, withCursor, "rec-1")
}

func (f *kitSQLiteStageFixture) addKitStageShardWithID(t *testing.T, shardKey, fileKey string, withCursor bool, recordID string) {
	t.Helper()
	path := shardFilePathForTest(f.root, fileKey, 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shardDB := createKitUsageShardDB(t, path)
	seedKitUsageRecord(t, shardDB, recordID, "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z")
	seedKitShard(t, f.seedCatalog, shardKey, "2026-01-05", 1, path)
	mustExecKit(t, f.seedCatalog, `INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key)
    VALUES ('key-1','sys-1',?)`, shardKey)
	mustExecKit(t, f.seedCatalog, `INSERT INTO usage_record_account_shards (account_id, shard_key)
    VALUES ('acc-1',?)`, shardKey)
	mustExecKit(t, f.seedCatalog, `INSERT INTO usage_record_shard_entries
    (usage_id, shard_key, system_account_id, api_key_id, account_id, created_at)
    VALUES (?, ?, 'sys-1', 'key-1', 'acc-1', '2026-01-05T01:00:00.000Z')`, recordID, shardKey)
	if withCursor {
		for _, jobName := range usageRecordCleanupRequiredCursorJobNames {
			mustExecKit(t, f.seedStats, `INSERT INTO stats_job_state
        (scope_type, scope_id, job_name, cursor_created_at, cursor_id)
        VALUES ('usage_shard', ?, ?, '2026-01-05T03:00:00.000Z', 'rec-000')`, shardKey, jobName)
		}
	}
}

func kitStageStore(t *testing.T, f *kitSQLiteStageFixture) *RecordCleanupStore {
	store := &RecordCleanupStore{
		Dataset:      f.dataset,
		Stats:        f.stats,
		UsageCatalog: f.catalog,
		Shards:       NewShardStore(f.root),
		Now:          kitNow,
		Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	// Windows 下注入句柄若不关闭会锁住分片文件导致 t.TempDir 清理失败。
	t.Cleanup(func() { _ = store.Shards.Close() })
	return store
}

// ---- statssubtract.go：stats writer 结算链 ----

func TestStatsSubtractSQLiteErrorStages(t *testing.T) {
	row := kitFailedRow()
	rows := []map[string]any{statsaggRowToMap(row, "shard-kit-a")}
	stages := []pgStage{
		{"ledger insert", "INSERT INTO usage_record_cleanup_deductions"},
		{"ledger select", "SELECT stats_subtracted_at"},
		{"totals update", "UPDATE usage_stats_totals"},
		{"totals empty delete", "DELETE FROM usage_stats_totals"},
		{"minute update", "UPDATE usage_stats_minute"},
		{"hourly update", "UPDATE usage_stats_hourly"},
		{"hourly delete", "DELETE FROM usage_stats_hourly"},
		{"daily update", "UPDATE usage_stats_daily"},
		{"daily delete", "DELETE FROM usage_stats_daily"},
		{"weekly update", "UPDATE usage_stats_weekly"},
		{"weekly delete", "DELETE FROM usage_stats_weekly"},
		{"monthly update", "UPDATE usage_stats_monthly"},
		{"monthly delete", "DELETE FROM usage_stats_monthly"},
		{"minute delete", "DELETE FROM usage_stats_minute"},
		{"latency minute", "UPDATE usage_latency_minute"},
		{"latency minute delete", "DELETE FROM usage_latency_minute"},
		{"latency hourly", "UPDATE usage_latency_hourly"},
		{"latency hourly delete", "DELETE FROM usage_latency_hourly"},
		{"latency daily", "UPDATE usage_latency_daily"},
		{"latency daily delete", "DELETE FROM usage_latency_daily"},
		{"latency weekly", "UPDATE usage_latency_weekly"},
		{"latency weekly delete", "DELETE FROM usage_latency_weekly"},
		{"latency monthly", "UPDATE usage_latency_monthly"},
		{"latency monthly delete", "DELETE FROM usage_latency_monthly"},
		{"auth user", "authorization_user_usage_summary_daily"},
		{"auth team", "authorization_team_usage_summary_daily"},
		{"model", "UPDATE usage_model_minute"},
		{"model hourly", "UPDATE usage_model_hourly"},
		{"model daily", "UPDATE usage_model_daily"},
		{"model weekly", "UPDATE usage_model_weekly"},
		{"model monthly", "UPDATE usage_model_monthly"},
		{"error bucket", "UPDATE usage_error_minute"},
		{"error hourly", "UPDATE usage_error_hourly"},
		{"error daily", "UPDATE usage_error_daily"},
		{"error weekly", "UPDATE usage_error_weekly"},
		{"error monthly", "UPDATE usage_error_monthly"},
		{"quality update", "UPDATE account_quality_minute_stats"},
		{"quality dirty", "INSERT INTO account_quality_dirty_accounts"},
		{"health", "DELETE FROM account_health_hourly"},
		{"mark deleted", "UPDATE usage_record_cleanup_deductions\n      SET shard_deleted_at"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		seedKitTotals(t, f.seedStats, "sys-1", "system_account", "sys-1", 5, 4, 100)
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(path string) (*sql.DB, error) {
			return openKitFailingSQLite(path, stage.failOn), nil
		})
		return store.CleanupAPIKeyRecordStatsData(context.Background(),
			retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
			rows, kitUpdatedAt, true, kitZone())
	})
	// 空行集的 scope 清理阶段。
	scopeStages := []pgStage{
		{"scope totals", "DELETE FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = 'api_key'"},
		{"scope minute", "DELETE FROM usage_stats_minute WHERE system_account_id = ? AND scope_type = 'api_key'"},
		{"scope rank", "DELETE FROM usage_rank_snapshots WHERE system_account_id = ? AND scope_type = 'api_key'"},
		{"scope quota", "DELETE FROM usage_quota_hourly_windows WHERE system_account_id = ? AND scope_type = 'api_key'"},
		{"scope range", "DELETE FROM usage_scope_range_windows WHERE system_account_id = ? AND scope_type = 'api_key'"},
		{"scope cursor", "DELETE FROM stats_job_state WHERE scope_type = 'api_key'"},
		{"scope ledger", "DELETE FROM usage_record_cleanup_deductions WHERE api_key_id"},
	}
	runSQLiteStages(t, scopeStages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		store := kitStageStore(t, f)
		return store.CleanupAPIKeyRecordStatsData(context.Background(),
			retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
			nil, kitUpdatedAt, false, kitZone())
	})
	// account 变体的台账/标记分支（同注入清单的代表性子集）。
	accountStages := []pgStage{
		{"account ledger insert", "INSERT INTO usage_record_cleanup_deductions"},
		{"account ledger select", "SELECT stats_subtracted_at FROM usage_record_cleanup_deductions"},
		{"account mark deleted", "SET shard_deleted_at = COALESCE"},
	}
	runSQLiteStages(t, accountStages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		seedKitTotals(t, f.seedStats, "sys-1", "account", "acc-1", 5, 4, 100)
		store := kitStageStore(t, f)
		return store.CleanupAccountRecordStatsData(context.Background(),
			retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"},
			rows, kitUpdatedAt, true, kitZone())
	})
	// 派生窗口刷新失败 → 结算失败。
	f := newKitSQLiteStageFixture(t, "")
	store := kitStageStore(t, f)
	store.DerivedWindows = &kitDerivedWindows{err: errors.New("刷新注入失败")}
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		nil, kitUpdatedAt, false, kitZone()); err == nil {
		t.Fatalf("派生窗口失败应报错")
	}
}

// ---- recordcleanup.go：SQLite 关联清理链 ----

func TestRecordCleanupSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"target upsert", "INSERT INTO api_key_record_cleanup_targets"},
		{"shard locations", "usage_record_api_key_shards c"},
		{"shard cursor", "FROM stats_job_state"},
		{"uncovered check", "SELECT id FROM usage_records"},
		{"rows select", "AND id <= ?))"},
		{"rows delete", "DELETE FROM usage_records WHERE id = ?"},
		{"shard entries delete", "DELETE FROM usage_record_shard_entries WHERE usage_id"},
		{"target mark", "UPDATE api_key_record_cleanup_targets"},
		{"target clear", "DELETE FROM api_key_record_cleanup_targets"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		// sk-1 覆盖（有游标、有记录）；deferred 阶段另有 sk-2（无游标、不同
		// usage id）阻塞，避免同 id 目录条目被连带删除。
		f.addKitStageShard(t, "sk-1", "20260105", true)
		if stage.name == "target mark" {
			f.addKitStageShardWithID(t, "sk-2", "20260106", false, "rec-2")
		}
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(path string) (*sql.DB, error) {
			return openKitFailingSQLite(path, stage.failOn), nil
		})
		_, err := store.CleanupAPIKeyRelatedSQLite(context.Background(), "key-1", "sys-1", &kitStatsWriter{})
		return err
	})
}

// ---- codexcontext.go：SQLite 过期清理与结算 ----

func TestCodexSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"sessions select", "FROM codex_context_sessions"},
		{"responses select", "FROM codex_context_responses"},
		{"compacts select", "FROM codex_context_compacts"},
		{"queue enqueue", "INSERT INTO codex_context_storage_cleanup_queue"},
		{"responses delete", "DELETE FROM codex_context_responses"},
		{"compacts delete", "DELETE FROM codex_context_compacts"},
		{"remaining select", "SELECT session_id, MAX(expires_at)"},
		{"session refresh", "UPDATE codex_context_sessions"},
		{"session delete", "DELETE FROM codex_context_sessions"},
		{"pending select", "FROM codex_context_storage_cleanup_queue"},
		{"referenced delete", "DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-old", "2026-09-01T00:00:00.000Z")
		seedKitCodexSession(t, store, 0, "s-mixed", "2026-09-02T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-old", "k-old", "2026-09-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-2", "s-mixed", "k-mixed", "2026-09-02T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-3", "s-mixed", "k-keep", "2026-12-01T00:00:00.000Z")
		shardDB, err := store.shard(0)
		if err != nil {
			t.Fatalf("shard: %v", err)
		}
		mustExecKit(t, shardDB, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES
      ('k-mixed', ?, ?, ?), ('k-keep', ?, ?, ?)`,
			kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt)
		// 以注入句柄替换缓存分片（先关闭旧句柄，测试结束再关新句柄）。
		if cached, ok := store.shards[0]; ok {
			_ = cached.Close()
		}
		failingShard := openKitFailingSQLite(store.shardPath(0), stage.failOn)
		t.Cleanup(func() { _ = failingShard.Close() })
		store.shards[0] = failingShard
		_, err = store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 10)
		return err
	})
	// 缓存分片句柄整体关闭 → 各函数的 BeginTx 错误分支。
	closedStore := newKitCodexStore(t, 1)
	closedShard, err := closedStore.shard(0)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	if err := closedShard.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := closedStore.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 10); err == nil {
		t.Fatalf("关闭分片应报错")
	}
	settleStages := []pgStage{
		{"attempt select", "SELECT attempt_count"},
		{"defer update", "UPDATE codex_context_storage_cleanup_queue"},
		{"ack delete", "DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key"},
	}
	runSQLiteStages(t, settleStages, func(t *testing.T, stage pgStage) error {
		store := newKitCodexStore(t, 1)
		shardDB, err := store.shard(0)
		if err != nil {
			t.Fatalf("shard: %v", err)
		}
		mustExecKit(t, shardDB, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at, attempt_count) VALUES
      ('k-1', ?, ?, ?, 0), ('k-2', ?, ?, ?, 1)`,
			kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt)
		if cached, ok := store.shards[0]; ok {
			_ = cached.Close()
		}
		failingShard := openKitFailingSQLite(store.shardPath(0), stage.failOn)
		t.Cleanup(func() { _ = failingShard.Close() })
		store.shards[0] = failingShard
		_, err = store.SettleStorageCleanup(context.Background(), Settlement{
			SucceededStorageKeys: []string{"k-1"},
			Failures:             []SettlementFailure{{StorageKey: "k-2", Error: "boom"}},
			Now:                  kitUpdatedAt,
		})
		return err
	})
}

// ---- UpsertAccountUsageSnapshots：句柄/语句级失败 ----

func TestUpsertSnapshotsSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"owner query", "FROM accounts WHERE id"},
		{"snapshot upsert", "INSERT INTO account_usage_snapshots"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		mustExecKit(t, f.seedDataset, `CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT)`)
		mustExecKit(t, f.seedDataset, `INSERT INTO accounts (id, system_account_id) VALUES ('acc-1','owner-1')`)
		store := &RecordCleanupStore{Stats: f.stats}
		business := openKitFailingSQLiteDB(t, filepath.Join(f.dir, "dataset.sqlite3"), stage.failOn)
		return store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
			{AccountID: "acc-1", Kind: "openai_codex", Snapshot: map[string]any{"requests": 1.0}},
		})
	})
	// Stats BeginTx 失败（关闭句柄）。
	f := newKitSQLiteStageFixture(t, "")
	if err := f.stats.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store := &RecordCleanupStore{Stats: f.stats}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), f.seedDataset, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex"},
	}); err == nil {
		t.Fatalf("BeginTx 失败应报错")
	}
}

// ---- dataretention.go：非业务数据与使用记录 SQLite 链 ----

func TestDataRetentionSQLiteErrorStages(t *testing.T) {
	t.Run("non business dataset", func(t *testing.T) {
		stages := []pgStage{
			{"public api logs", `DELETE FROM "public_api_logs"`},
			{"usage records cursor", "FROM stats_job_state"},
			{"catalog account shards", "DELETE FROM usage_record_account_shards"},
		}
		runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
			f := newKitSQLiteStageFixture(t, stage.failOn)
			createKitMinimalTable(t, f.seedDataset.DB, "public_api_logs", "created_at")
			mustExecKit(t, f.seedDataset, `INSERT INTO public_api_logs (created_at) VALUES ('2026-01-01T00:00:00.000Z')`)
			f.addKitStageShard(t, "sk-nb", "20260105", true)
			shards := NewShardStore(f.root)
			t.Cleanup(func() { _ = shards.Close() })
			shards.SetOpener(func(path string) (*sql.DB, error) {
				return openKitFailingSQLite(path, stage.failOn), nil
			})
			store := &NonBusinessDatasetStore{
				Dataset:      f.dataset,
				UsageCatalog: f.catalog,
				Stats:        f.stats,
				UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: shards},
				Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
			}
			_, err := store.CleanupBefore(context.Background(), kitUpdatedAt, 10)
			return err
		})
	})
	t.Run("usage records", func(t *testing.T) {
		stages := []pgStage{
			{"cursor", "FROM stats_job_state"},
			{"rows select", "JOIN usage_record_shards s"},
			{"covered check", "HAVING COUNT(DISTINCT job_name)"},
			{"existing ids", "SELECT id FROM usage_records WHERE id IN"},
			{"rows delete", "DELETE FROM usage_records WHERE id IN"},
			{"entries delete", "DELETE FROM usage_record_shard_entries WHERE usage_id"},
		}
		runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
			f := newKitSQLiteStageFixture(t, stage.failOn)
			f.addKitStageShard(t, "sk-1", "20260105", true)
			shards := NewShardStore(f.root)
			t.Cleanup(func() { _ = shards.Close() })
			shards.SetOpener(func(path string) (*sql.DB, error) {
				return openKitFailingSQLite(path, stage.failOn), nil
			})
			store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: shards}
			_, err := store.CleanupProcessedBefore(context.Background(), kitUpdatedAt, 10)
			return err
		})
	})
}

// ---- chat.go：CleanupRetention 主链 ----

func TestChatRetentionSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"stale select", "active_turn_id IS NOT NULL"},
		{"assistant select", "status = 'streaming'"},
		{"message fail", "status = 'failed', storage_reserved_bytes = 0"},
		{"window release", "reserved_bytes = reserved_bytes - ?"},
		{"window empty delete", "DELETE FROM chat_user_storage_windows WHERE system_account_id"},
		{"conversation revision", "message_revision = message_revision + ?"},
		{"expired select", "HAVING MAX(expires_at) <= ?"},
		{"turn messages", "SELECT created_at, expires_at, content_bytes"},
		{"window bucket", "content_bytes = CASE WHEN"},
		{"idempotency turn", "DELETE FROM chat_message_idempotency WHERE conversation_id"},
		{"messages delete", "DELETE FROM chat_messages WHERE conversation_id"},
		{"active clear", "AND active_turn_id = ?"},
		{"revision bump", "SET message_revision = message_revision + 1"},
		{"idempotency bulk", "DELETE FROM chat_message_idempotency WHERE expires_at"},
		{"window cleanup", "AS storage_window"},
		{"empty conversation", "AND NOT EXISTS (SELECT 1 FROM chat_messages WHERE conversation_id"},
		{"stale titles", "title_source_message_id IS NOT NULL"},
		{"first user", "ORDER BY sequence_no ASC"},
		{"title set", "SET title = ?"},
		{"stale compactions", "context_state = 'compacting'"},
		{"compaction update", "context_state = 'compact_failed'"},
		{"checkpoint select", "FROM chat_context_checkpoints"},
		{"checkpoint detach", "active_checkpoint_id = NULL"},
		{"checkpoint delete", "DELETE FROM chat_context_checkpoints WHERE id IN"},
		{"asset select", "FROM chat_assets"},
		{"asset claim", "cleanup_status = 'claimed'"},
		{"claimed rows", "SELECT * FROM chat_assets WHERE cleanup_claim_id"},
		{"asset delete", "DELETE FROM chat_assets WHERE id = ?"},
		{"usage update", "UPDATE chat_user_asset_usage"},
		{"usage empty delete", "DELETE FROM chat_user_asset_usage WHERE system_account_id"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		chatPath := filepath.Join(t.TempDir(), "chat_stages.sqlite3")
		seedDB, err := sql.Open("sqlite", chatPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = seedDB.Close() })
		seedDB.SetMaxOpenConns(1)
		createKitChatSchema(t, seedDB)
		// 中断轮次（含助手消息与容量预留）。
		seedKitConversation(t, &DB{DB: seedDB}, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
		seedKitMessage(t, &DB{DB: seedDB}, "msg-1", "conv-1", "turn-1", "assistant", "streaming",
			"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, ChatAssistantStorageReservationBytes)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
      VALUES ('sys-1','2026-09-09',10,?, '2026-09-09T01:00:00.000Z')`, ChatAssistantStorageReservationBytes)
		// 过期轮次（含幂等键与容量窗口）。
		seedKitConversation(t, &DB{DB: seedDB}, "conv-2", "", "")
		seedKitMessage(t, &DB{DB: seedDB}, "msg-2", "conv-2", "turn-old", "user", "completed",
			"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 100, 30)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_message_idempotency (idempotency_key, conversation_id, turn_id, expires_at)
      VALUES ('idem-1','conv-2','turn-old','2026-09-09T00:00:00.000Z')`)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
      VALUES ('sys-1','2026-09-01',100,30,'2026-09-01T01:00:00.000Z')`)
		// 压缩中的会话、检查点、资产与配额（供压缩/检查点/资产阶段注入）。
		seedKitConversation(t, &DB{DB: seedDB}, "conv-c", "", "")
		mustExecKit(t, &DB{DB: seedDB}, `UPDATE chat_conversations SET context_state = 'compacting',
      context_claimed_at = '2026-09-09T00:00:00.000Z' WHERE id = 'conv-c'`)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_context_checkpoints (id, conversation_id, status, expires_at)
      VALUES ('cp-1','conv-c','expired','2026-09-09T00:00:00.000Z')`)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_assets (
      id, system_account_id, storage_key, quota_bytes, expires_at, cleanup_status,
      cleanup_claim_id, cleanup_attempt_count, updated_at)
      VALUES ('asset-1','sys-1','missing.bin',50,'2026-09-09T00:00:00.000Z','claimed','claim-1',1,'2026-09-01T00:00:00.000Z')`)
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
      VALUES ('sys-1',50,1,'2026-09-01T00:00:00.000Z')`)
		// 标题回退会话。
		mustExecKit(t, &DB{DB: seedDB}, `INSERT INTO chat_conversations (id, system_account_id, title, title_source_message_id, created_at, updated_at)
      VALUES ('conv-3','sys-1','旧标题','msg-gone','2026-09-09T00:00:00.000Z','2026-09-09T00:00:00.000Z')`)
		seedKitMessage(t, &DB{DB: seedDB}, "msg-u1", "conv-3", "turn-2", "user", "completed",
			"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, 0)

		store := &ChatStore{DB: openKitFailingSQLiteDB(t, chatPath, stage.failOn), Now: kitNow}
		_, err = store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
			Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
		})
		return err
	})
}

// ---- deleteaccount.go：物理删除链 ----

func TestDeleteAccountSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"group accounts", "DELETE FROM group_accounts WHERE account_id IN"},
		{"supported models", "DELETE FROM account_supported_models"},
		{"model mappings", "DELETE FROM account_model_mappings"},
		{"tag bindings", "DELETE FROM account_tag_bindings"},
		{"scope bindings scope", "DELETE FROM request_quota_hourly_window_scope_bindings WHERE scope_type"},
		{"scope bindings grant", "DELETE FROM request_quota_hourly_window_scope_bindings WHERE source_type"},
		{"sources", "DELETE FROM resource_authorization_sources WHERE authorization_id"},
		{"grants", "DELETE FROM resource_authorization_grants WHERE id IN"},
		{"related accounts", "DELETE FROM accounts WHERE id IN"},
		{"root account", "DELETE FROM accounts WHERE id = ?"},
		{"authorizations", "DELETE FROM resource_authorizations WHERE id IN"},
	}
	seed := func(t *testing.T, f *kitSQLiteStageFixture) {
		business := f.seedDataset
		createKitBusinessSchemaOn(t, business.DB)
		mustExecKit(t, business, `CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, status TEXT DEFAULT 'active',
      schedulable INTEGER DEFAULT 1, cooldown_until TEXT, provider_code TEXT DEFAULT '',
      type TEXT DEFAULT '', deleted_at TEXT, deleted_by TEXT,
      authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT,
      config_revision INTEGER DEFAULT 0, dispatch_revision INTEGER DEFAULT 0,
      created_at TEXT DEFAULT '', updated_at TEXT DEFAULT '')`)
		// 复用 business schema 的其余表。
		createKitBusinessSchemaOn(t, business.DB)
		seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
		mustExecKit(t, business, `INSERT INTO accounts (
      id, system_account_id, status, deleted_at, authorization_instance_authorization_id,
      authorization_instance_source_account_id, created_at, updated_at)
    VALUES ('acc-2','sys-2','disabled', NULL, 'auth-9', 'acc-1', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
		mustExecKit(t, business, `INSERT INTO resource_authorizations
      (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status)
      VALUES ('auth-9','account','acc-1','sys-1','owner-2','active')`)
		mustExecKit(t, business, `INSERT INTO resource_authorization_grants
      (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, status)
      VALUES ('g-1','account','acc-1','owner-2','system_account','sys-1','active')`)
		mustExecKit(t, business, `INSERT INTO group_accounts (id, group_id, account_id, account_authorization_id)
      VALUES ('ga-1','grp-1','acc-1', NULL)`)
		for _, table := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings"} {
			mustExecKit(t, business, `INSERT INTO `+table+` (id, account_id) VALUES ('x-1','acc-1')`)
		}
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		seed(t, f)
		business := openKitFailingSQLiteDB(t, filepath.Join(f.dir, "dataset.sqlite3"), stage.failOn)
		records := &RecordCleanupStore{Dataset: f.dataset, Stats: f.stats, UsageCatalog: f.catalog}
		store := &DeletedAccountStore{Business: business, Records: records, Now: kitNow}
		summary, err := store.CleanupExpired(context.Background())
		if err != nil {
			return err
		}
		// 物理删除失败计入 Failed 与 LastTargetError，不中止整轮。
		if summary.Failed != 1 {
			t.Fatalf("注入失败应计入 Failed：%+v", summary)
		}
		if !strings.Contains(store.LastTargetError, stage.failOn) {
			t.Fatalf("LastTargetError = %q", store.LastTargetError)
		}
		// 断言通过：注入已被观察（runner 以 nil 表示未覆盖）。
		return errors.New("covered")
	})
}

// createKitBusinessSchemaOn：business schema 变体（dataset 库文件复用为
// business 库，schema 与 deleteaccount 测试一致）。
func createKitBusinessSchemaOn(t *testing.T, db *sql.DB) {
	t.Helper()
	createKitBusinessSchema(t, db)
}
