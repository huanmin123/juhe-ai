package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// 本文件是 cleanuprepo 新增测试的共享基建：
//   - SQLite 真实内存/文件库句柄与 schema 装配（与 statssubtractpostgres_test.go
//     的 SQLite 互证模式一致，但覆盖完整表家族）；
//   - PG 录制驱动的「按子串注入失败」变体（复用既有 pgRecorder，不重复实现）；
//   - retention.StatsWriter / DerivedWindowRefresher 的脚本化 fake
//     （参考 internal/retention/mocks_test.go 的调用记录模式）。
// 约定：断言只用标准库；helper 首行 t.Helper()；不引入新依赖。

// ---- 测试固定值 ----

const kitUpdatedAt = "2026-09-10T00:00:00.000Z"

func kitNow() time.Time {
	return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
}

func kitZone() *time.Location {
	return time.FixedZone("KITTZ", 8*3600)
}

// ---- SQLite 基建 ----

func openKitSQLite(t *testing.T, name string) *DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name+".sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	return &DB{DB: db}
}

func mustExecKit(t *testing.T, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) sql.Result {
	t.Helper()
	result, err := q.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", oneLineSQL(query), err)
	}
	return result
}

func mustQueryCountKit(t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) int64 {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", oneLineSQL(query), err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Fatalf("query %q: %v", oneLineSQL(query), err)
		}
		t.Fatalf("query %q: 未返回计数行", oneLineSQL(query))
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	return count
}

func oneLineSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

// normalizeKitSQL 逐行 trim 后拼接，用于对生产 raw string SQL 做空白无关
// 的逐字符内容比对（保留 token 序列）。
func normalizeKitSQL(query string) string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(query, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

func execKitSchema(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema %q: %v", oneLineSQL(statement), err)
		}
	}
}

// ---- stats schema 装配 ----

// createKitScopeStatsTable 建立 scope 统计家族的通用表形状（totals 与五个时间
// 桶共用；列与 Node usage_stats_* schema 对齐的最小集合）。
func createKitScopeStatsTable(t *testing.T, db *sql.DB, table, statColumn string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      %s TEXT NOT NULL DEFAULT '',
      request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
      input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
      cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
      cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
      cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
      input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
      total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
      duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
      first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
      first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
      PRIMARY KEY (system_account_id, scope_type, scope_id, %s))`, table, statColumn, statColumn))
}

func createKitModelBucketTable(t *testing.T, db *sql.DB, table, statColumn string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      system_account_id TEXT NOT NULL, %s TEXT NOT NULL DEFAULT '',
      provider_code TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
      request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
      input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
      cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
      cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
      cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
      input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
      total_cost_usd REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (system_account_id, %s, provider_code, model))`, table, statColumn, statColumn))
}

func createKitErrorBucketTable(t *testing.T, db *sql.DB, table, statColumn string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      system_account_id TEXT NOT NULL, %s TEXT NOT NULL DEFAULT '',
      error_group TEXT NOT NULL DEFAULT '', provider_code TEXT NOT NULL DEFAULT '',
      error_code TEXT NOT NULL DEFAULT '', status_code REAL DEFAULT 0,
      request_count REAL DEFAULT 0, error_count REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (system_account_id, %s, error_group, provider_code, error_code, status_code))`,
		table, statColumn, statColumn))
}

func createKitLatencyTable(t *testing.T, db *sql.DB, table, statColumn string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      %s TEXT NOT NULL DEFAULT '', metric_type TEXT NOT NULL DEFAULT '',
      bucket_upper_bound_ms INTEGER NOT NULL DEFAULT 0,
      sample_count REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (system_account_id, scope_type, scope_id, %s, metric_type, bucket_upper_bound_ms))`,
		table, statColumn, statColumn))
}

func createKitAuthSummaryTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      system_account_id TEXT NOT NULL, stat_date TEXT NOT NULL,
      resource_filter_type TEXT NOT NULL DEFAULT '', resource_filter_id TEXT NOT NULL DEFAULT '',
      team_filter_id TEXT NOT NULL DEFAULT '', grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
      request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
      input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
      cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
      cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
      cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
      input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
      total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
      duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
      first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
      first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
      PRIMARY KEY (system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id, grantee_filter_system_account_id))`, table))
}

// createKitStatsChainSchema 装配 stats 扣减/清理链涉及的完整 SQLite 表家族
// （statssubtract.go / recordcleanup.go / dataretention.go 的 SQLite 路径所需）。
func createKitStatsChainSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range apiKeyScopeStatsTables {
		switch table {
		case "usage_rank_snapshots":
			execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_rank_snapshots (
      system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      snapshot_at TEXT NOT NULL DEFAULT '', request_count REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (system_account_id, scope_type, scope_id, snapshot_at))`)
		case "usage_quota_hourly_windows":
			execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_quota_hourly_windows (
      system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      window_start TEXT DEFAULT '', updated_at TEXT DEFAULT '',
      PRIMARY KEY (system_account_id, scope_type, scope_id, window_start))`)
		case "usage_scope_range_windows":
			execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_scope_range_windows (
      system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      end_date TEXT NOT NULL DEFAULT '', updated_at TEXT,
      PRIMARY KEY (system_account_id, scope_type, scope_id, end_date))`)
		case "usage_latency_minute", "usage_latency_hourly", "usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly":
			statColumn := map[string]string{
				"usage_latency_minute": "stat_minute", "usage_latency_hourly": "stat_hour",
				"usage_latency_daily": "stat_date", "usage_latency_weekly": "stat_week",
				"usage_latency_monthly": "stat_month",
			}[table]
			createKitLatencyTable(t, db, table, statColumn)
		default:
			statColumn := "stat_minute"
			switch {
			case strings.Contains(table, "totals"):
				statColumn = "stat_minute"
			case strings.Contains(table, "hourly"):
				statColumn = "stat_hour"
			case strings.Contains(table, "daily"):
				statColumn = "stat_date"
			case strings.Contains(table, "weekly"):
				statColumn = "stat_week"
			case strings.Contains(table, "monthly"):
				statColumn = "stat_month"
			}
			createKitScopeStatsTable(t, db, table, statColumn)
		}
	}
	for _, bucket := range usageStatsBucketDefs {
		createKitScopeStatsTable(t, db, bucket.TableName, bucket.ColumnName)
	}
	for _, bucket := range usageModelBucketDefs {
		createKitModelBucketTable(t, db, bucket.TableName, bucket.ColumnName)
	}
	for _, bucket := range usageErrorBucketDefs {
		createKitErrorBucketTable(t, db, bucket.TableName, bucket.ColumnName)
	}
	for _, bucket := range usageLatencyBucketDefs {
		createKitLatencyTable(t, db, bucket.TableName, bucket.ColumnName)
	}
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_quality_minute_stats (
      account_id TEXT NOT NULL, stat_minute TEXT NOT NULL,
      request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
      first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
      last_sample_at TEXT, last_success_at TEXT, last_error_at TEXT, last_error_message TEXT, updated_at TEXT,
      PRIMARY KEY (account_id, stat_minute))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_quality_dirty_accounts (
      account_id TEXT PRIMARY KEY, first_dirty_at TEXT, updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_quality_scores (
      account_id TEXT PRIMARY KEY, score REAL DEFAULT 0, updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_health_hourly (
      account_id TEXT NOT NULL, stat_hour TEXT NOT NULL DEFAULT '',
      last_record_id TEXT DEFAULT '', request_count REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (account_id, stat_hour))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_record_cleanup_deductions (
      usage_id TEXT NOT NULL, source_shard_key TEXT NOT NULL,
      api_key_id TEXT, account_id TEXT, system_account_id TEXT, record_json TEXT,
      stats_subtracted_at TEXT, shard_deleted_at TEXT, created_at TEXT, updated_at TEXT,
      PRIMARY KEY (usage_id, source_shard_key))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS stats_job_state (
      scope_type TEXT NOT NULL, scope_id TEXT NOT NULL, job_name TEXT NOT NULL,
      cursor_created_at TEXT, cursor_id TEXT, updated_at TEXT,
      PRIMARY KEY (scope_type, scope_id, job_name))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_usage_snapshots (
      system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, kind TEXT NOT NULL,
      source TEXT DEFAULT '', snapshot_json TEXT DEFAULT '', refresh_status TEXT DEFAULT '',
      last_success_at TEXT, last_error_message TEXT, updated_at TEXT, created_at TEXT)`)
	execKitSchema(t, db, `CREATE UNIQUE INDEX IF NOT EXISTS account_usage_snapshots_kit_pk
      ON account_usage_snapshots (system_account_id, account_id, kind)`)
	createKitAuthSummaryTable(t, db, "authorization_team_usage_summary_daily")
	createKitAuthSummaryTable(t, db, "authorization_user_usage_summary_daily")
	for _, table := range accountAuthorizationReportTables {
		if strings.Contains(table, "range_windows") {
			execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
      scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
      resource_filter_type TEXT NOT NULL DEFAULT '', resource_filter_id TEXT NOT NULL DEFAULT '',
      end_date TEXT NOT NULL DEFAULT '', updated_at TEXT,
      PRIMARY KEY (scope_type, scope_id, resource_filter_type, resource_filter_id, end_date))`, table))
		}
	}
}

// ---- usage catalog / shard schema ----

func createKitUsageCatalogSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_record_shards (
      shard_key TEXT PRIMARY KEY, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL,
      file_path TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', last_seen_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
      usage_id TEXT NOT NULL, shard_key TEXT NOT NULL, system_account_id TEXT NOT NULL,
      api_key_id TEXT, account_id TEXT, created_at TEXT NOT NULL,
      PRIMARY KEY (usage_id, shard_key))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
      api_key_id TEXT NOT NULL, system_account_id TEXT NOT NULL, shard_key TEXT NOT NULL,
      first_created_at TEXT DEFAULT '', last_seen_at TEXT,
      PRIMARY KEY (api_key_id, system_account_id, shard_key))`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_record_account_shards (
      account_id TEXT NOT NULL, shard_key TEXT NOT NULL,
      first_created_at TEXT DEFAULT '', last_seen_at TEXT,
      PRIMARY KEY (account_id, shard_key))`)
}

// createKitUsageShardDB 建立一个分片库文件（usage_records 41 列，列序与
// usageStatsRecordSelectColumns 一致）。
func createKitUsageShardDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open shard sqlite %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, trace_id TEXT DEFAULT '',
      traffic_source TEXT DEFAULT '', client_ip TEXT, api_key_id TEXT, group_id TEXT,
      account_id TEXT, endpoint TEXT, provider_code TEXT, provider_protocol_profile_id TEXT,
      model TEXT, status_code REAL, success REAL DEFAULT 0, failure_attribution TEXT,
      first_token_ms REAL, duration_ms REAL, input_tokens REAL, output_tokens REAL,
      cache_read_tokens REAL, cache_read_cost_usd REAL, cache_write_tokens REAL,
      cache_write_1h_tokens REAL, cache_write_cost_usd REAL, thinking_tokens REAL,
      input_image_tokens REAL, output_image_tokens REAL, cost_usd REAL,
      error_code TEXT, error_message TEXT, account_owner_system_account_id TEXT,
      group_owner_system_account_id TEXT, account_access_type TEXT, group_access_type TEXT,
      account_authorization_id TEXT, account_authorization_source_type TEXT,
      account_authorization_source_team_id TEXT, group_authorization_id TEXT,
      group_authorization_source_type TEXT, group_authorization_source_team_id TEXT,
      created_at TEXT NOT NULL)`)
	return db
}

// seedKitUsageRecord 往分片库插入一行最小使用记录（可覆盖维度字段）。
func seedKitUsageRecord(t *testing.T, shardDB *sql.DB, id, systemAccountID, apiKeyID, accountID, createdAt string) {
	t.Helper()
	mustExecKit(t, shardDB, `INSERT INTO usage_records (
      id, system_account_id, api_key_id, account_id, provider_code, model, success,
      input_tokens, output_tokens, cost_usd, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1, 10, 5, 0.01, ?)`,
		id, systemAccountID, apiKeyID, accountID, "openai", "gpt-kit", createdAt)
}

// ---- dataset targets schema ----

func createKitTargetsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
      api_key_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
      attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT,
      last_error_message TEXT, created_at TEXT, updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
      account_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
      related_account_ids_json TEXT DEFAULT '[]', authorization_ids_json TEXT DEFAULT '[]',
      team_scope_ids_json TEXT DEFAULT '[]',
      attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT,
      last_error_message TEXT, created_at TEXT, updated_at TEXT)`)
}

// ---- business schema（deleteaccount 物理清理链）----

func createKitBusinessSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, status TEXT DEFAULT 'active',
      schedulable INTEGER DEFAULT 1, cooldown_until TEXT, provider_code TEXT DEFAULT '',
      type TEXT DEFAULT '', deleted_at TEXT, deleted_by TEXT,
      authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT,
      config_revision INTEGER DEFAULT 0, dispatch_revision INTEGER DEFAULT 0,
      created_at TEXT DEFAULT '', updated_at TEXT DEFAULT '')`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
      grantee_system_account_id TEXT DEFAULT '', resource_owner_system_account_id TEXT DEFAULT '',
      status TEXT DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT,
      revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, last_source_changed_at TEXT,
      updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS resource_authorization_sources (
      id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT DEFAULT 'manual',
      source_team_id TEXT, status TEXT DEFAULT 'active', ended_at TEXT, ended_reason TEXT,
      revoked_by TEXT, revoked_at TEXT, updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS resource_authorization_grants (
      id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT DEFAULT '', grantee_type TEXT DEFAULT 'system_account',
      grantee_system_account_id TEXT DEFAULT '', status TEXT DEFAULT 'active',
      revoked_by TEXT, revoked_at TEXT, updated_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS group_accounts (
      id TEXT PRIMARY KEY, group_id TEXT DEFAULT '', account_id TEXT, account_authorization_id TEXT)`)
	for _, table := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings"} {
		execKitSchema(t, db, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (id TEXT PRIMARY KEY, account_id TEXT)`, table))
	}
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
      id TEXT PRIMARY KEY, scope_type TEXT DEFAULT '', scope_id TEXT DEFAULT '',
      source_type TEXT DEFAULT '', source_id TEXT DEFAULT '')`)
	for _, table := range []string{"account_name_search_terms", "account_name_search_documents"} {
		execKitSchema(t, db, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (id TEXT PRIMARY KEY, account_id TEXT)`, table))
	}
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL, reserved_at TEXT)`)
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (
      event_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, input_version INTEGER NOT NULL,
      event_kind TEXT NOT NULL, reason TEXT NOT NULL, config_revision INTEGER DEFAULT 0,
      dispatch_revision INTEGER DEFAULT 0, status TEXT DEFAULT 'pending',
      available_at TEXT, created_at TEXT, updated_at TEXT)`)
}

// ---- PG 录制驱动失败注入 ----

// kitFailingPGConn 复用 pgRecorder，仅对命中子串的语句注入错误（失败路径
// 断言用；不改变录制行为）。
type kitFailingPGConn struct {
	*recorderConn
	failOn []string
}

func (c *kitFailingPGConn) shouldFail(query string) bool {
	for _, needle := range c.failOn {
		if strings.Contains(query, needle) {
			return true
		}
	}
	return false
}

func (c *kitFailingPGConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.shouldFail(query) {
		return nil, fmt.Errorf("kitFailingPGConn: 注入失败：%s", oneLineSQL(query))
	}
	return c.recorderConn.ExecContext(ctx, query, args)
}

func (c *kitFailingPGConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.shouldFail(query) {
		return nil, fmt.Errorf("kitFailingPGConn: 注入失败：%s", oneLineSQL(query))
	}
	return c.recorderConn.QueryContext(ctx, query, args)
}

type kitFailingPGConnector struct {
	rec    *pgRecorder
	failOn []string
}

func (c kitFailingPGConnector) Connect(context.Context) (driver.Conn, error) {
	return &kitFailingPGConn{recorderConn: &recorderConn{rec: c.rec}, failOn: c.failOn}, nil
}

func (c kitFailingPGConnector) Driver() driver.Driver { return recorderDriver{rec: c.rec} }

// openKitFailingRecorderPG 打开命中 failOn 子串即报错的 PG 录制句柄。
func openKitFailingRecorderPG(rec *pgRecorder, failOn ...string) *DB {
	return &DB{DB: sql.OpenDB(kitFailingPGConnector{rec: rec, failOn: failOn}), Postgres: true}
}

// ---- retention.StatsWriter / DerivedWindowRefresher fake ----

type kitAPIKeyStatsCall struct {
	Target      retention.APIKeyCleanupTarget
	Rows        []map[string]any
	UpdatedAt   string
	ShardDeleted bool
}

type kitAccountStatsCall struct {
	Target       retention.ExpiredDeletedAccountTarget
	Rows         []map[string]any
	UpdatedAt    string
	ShardDeleted bool
}

// kitStatsWriter 记录 CleanupDeletedApiKeyRecordStats / CleanupDeletedAccountRecordStats
// 调用，并可脚本化返回错误（Node stats cleanup callback 的 fake）。
type kitStatsWriter struct {
	mu           sync.Mutex
	apiKeyCalls  []kitAPIKeyStatsCall
	accountCalls []kitAccountStatsCall
	apiKeyErr    error
	accountErr   error
}

func (f *kitStatsWriter) recordAPIKey(call kitAPIKeyStatsCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiKeyCalls = append(f.apiKeyCalls, call)
}

func (f *kitStatsWriter) recordAccount(call kitAccountStatsCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accountCalls = append(f.accountCalls, call)
}

func (f *kitStatsWriter) CleanupUsageStatsRetention(context.Context, retention.UsageStatsRetentionInput) (retention.UsageStatsRetentionCounts, error) {
	return retention.UsageStatsRetentionCounts{}, nil
}

func (f *kitStatsWriter) CleanupSystemMetricsRetention(context.Context, retention.SystemMetricsRetentionInput) (retention.SystemMetricsRetentionCounts, error) {
	return retention.SystemMetricsRetentionCounts{}, nil
}

func (f *kitStatsWriter) CleanupNonBusinessStatsData(_ context.Context, cutoffAt string, _ int) (retention.NonBusinessDataCleanupCounts, error) {
	return retention.NonBusinessDataCleanupCounts{CutoffAt: cutoffAt, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}, nil
}

func (f *kitStatsWriter) CleanupDeletedApiKeyRecordStats(_ context.Context, input retention.DeletedApiKeyRecordStatsCleanupInput) error {
	f.recordAPIKey(kitAPIKeyStatsCall{Target: input.Target, Rows: input.Rows, UpdatedAt: input.UpdatedAt, ShardDeleted: input.ShardDeleted})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiKeyErr
}

func (f *kitStatsWriter) CleanupDeletedAccountRecordStats(_ context.Context, input retention.DeletedAccountRecordStatsCleanupInput) error {
	f.recordAccount(kitAccountStatsCall{Target: input.Target, Rows: input.Rows, UpdatedAt: input.UpdatedAt, ShardDeleted: input.ShardDeleted})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountErr
}

func (f *kitStatsWriter) UpsertAccountUsageSnapshots(context.Context, []retention.AccountUsageSnapshotUpsertInput) error {
	return nil
}

func (f *kitStatsWriter) apiKeyCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.apiKeyCalls)
}

type kitDerivedWindows struct {
	mu         sync.Mutex
	quotaCalls int
	rankCalls  int
	err        error
}

func (f *kitDerivedWindows) RefreshQuotaHourlyWindows(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotaCalls++
	return f.err
}

func (f *kitDerivedWindows) RefreshRankSnapshots(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rankCalls++
	return f.err
}

// ---- cleanuprepo.go 基础 helper 单元测试 ----

func TestKitDBTableAndBind(t *testing.T) {
	sqlite := &DB{}
	pg := &DB{Postgres: true}
	if got := sqlite.Table("juhe_chat", "chat_messages"); got != "chat_messages" {
		t.Fatalf("SQLite Table = %q", got)
	}
	if got := pg.Table("juhe_chat", "chat_messages"); got != "juhe_chat.chat_messages" {
		t.Fatalf("PG Table = %q", got)
	}
	if got := sqlite.Bind("WHERE a = ? AND b = ?"); got != "WHERE a = ? AND b = ?" {
		t.Fatalf("SQLite Bind = %q", got)
	}
	if got := pg.Bind("WHERE a = ? AND b = ?"); got != "WHERE a = $1 AND b = $2" {
		t.Fatalf("PG Bind = %q", got)
	}
	// BindIn 基于 TrimSuffix(Repeat("?,", n), ",")，占位符间无空格。
	if got := pg.BindIn(3); got != "$1,$2,$3" {
		t.Fatalf("PG BindIn(3) = %q", got)
	}
	if got := sqlite.BindIn(2); got != "?,?" {
		t.Fatalf("SQLite BindIn(2) = %q", got)
	}
}

func TestKitChunkValues(t *testing.T) {
	if got := chunkValues(nil, 3); len(got) != 0 {
		t.Fatalf("chunkValues(nil) = %v", got)
	}
	values := []string{"a", "b", "c", "d", "e"}
	chunks := chunkValues(values, 2)
	if len(chunks) != 3 || strings.Join(chunks[0], ",") != "a,b" ||
		strings.Join(chunks[1], ",") != "c,d" || strings.Join(chunks[2], ",") != "e" {
		t.Fatalf("chunkValues 分片错误：%v", chunks)
	}
	// size <= 0 回落 900。
	fallback := chunkValues(values, 0)
	if len(fallback) != 1 || len(fallback[0]) != 5 {
		t.Fatalf("chunkValues size=0 应回落 900：%v", fallback)
	}
}

func TestKitUniqueNonEmpty(t *testing.T) {
	got := uniqueNonEmpty([]string{" a ", "", "a", "b", " b", "  "})
	if strings.Join(got, "|") != "a|b" {
		t.Fatalf("uniqueNonEmpty = %v", got)
	}
}

func TestKitLimits(t *testing.T) {
	if positiveLimit(5) != 5 || positiveLimit(0) != 10000 || positiveLimit(-3) != 10000 {
		t.Fatalf("positiveLimit 回落语义错误")
	}
	if batchLimit(0) != 1 || batchLimit(-1) != 1 || batchLimit(7) != 7 {
		t.Fatalf("batchLimit 回落语义错误")
	}
}

type kitFakeResult struct{ affected int64 }

func (r kitFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r kitFakeResult) RowsAffected() (int64, error) { return r.affected, nil }

type kitFailingResult struct{}

func (kitFailingResult) LastInsertId() (int64, error) { return 0, nil }
func (kitFailingResult) RowsAffected() (int64, error) {
	return 0, errors.New("kitFailingResult: RowsAffected 注入失败")
}

func TestKitChangesAndExecChanged(t *testing.T) {
	affected, err := changes(kitFakeResult{affected: 3})
	if err != nil || affected != 3 {
		t.Fatalf("changes = %d, %v", affected, err)
	}
	if _, err := changes(kitFailingResult{}); err == nil {
		t.Fatalf("RowsAffected 失败应透传")
	}
	db := openKitSQLite(t, "exec_changed")
	execKitSchema(t, db.DB, `CREATE TABLE t (v TEXT)`)
	affected, err = execChanged(context.Background(), db, `INSERT INTO t (v) VALUES ('x')`)
	if err != nil || affected != 1 {
		t.Fatalf("execChanged = %d, %v", affected, err)
	}
	if _, err := execChanged(context.Background(), db, `INSERT INTO missing VALUES (1)`); err == nil {
		t.Fatalf("SQL 错误应透传")
	}
}

func TestKitNowISOAndParseInstant(t *testing.T) {
	if got := nowISO(func() time.Time { return kitNow() }); got != kitUpdatedAt {
		t.Fatalf("nowISO(注入时钟) = %q", got)
	}
	parsed := nowISO(nil)
	if _, ok := parseInstant(parsed); !ok {
		t.Fatalf("nowISO(nil) 应产出可解析 RFC3339：%q", parsed)
	}
	if _, ok := parseInstant(" 2026-09-10T00:00:00.000Z "); !ok {
		t.Fatalf("parseInstant 应容忍首尾空白")
	}
	if _, ok := parseInstant("2026-09-10 00:00:00"); ok {
		t.Fatalf("非 RFC3339 输入应拒绝")
	}
}

func TestKitCryptoReadAndJSONMarshal(t *testing.T) {
	buffer := make([]byte, 16)
	n, err := cryptoRead(buffer)
	if err != nil || n != 16 {
		t.Fatalf("cryptoRead = %d, %v", n, err)
	}
	data, err := jsonMarshal(map[string]string{"k": "v"})
	if err != nil || string(data) != `{"k":"v"}` {
		t.Fatalf("jsonMarshal = %s, %v", data, err)
	}
}
