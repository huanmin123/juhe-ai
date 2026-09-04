package statsverify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// StoreMode mirrors runtimeConfig.databaseDriver: the jobs can run against
// the local SQLite files (Go-owned layout) or the externally provisioned
// PostgreSQL schemas.
type StoreMode string

const (
	StoreSQLite   StoreMode = "sqlite"
	StorePostgres StoreMode = "postgres"
)

// UsageStatsTimezoneCacheTTL mirrors usageStatsTimezoneCacheTtlMs
// (usage-stats-helpers.ts line 13, 60_000).
const UsageStatsTimezoneCacheTTL = 60 * time.Second

// Store owns the three jobs' storage facts. PostgreSQL writes target the
// externally provisioned juhe_stats/juhe_business/juhe_usage schemas and are
// never migrated by this package; SQLite is the Go-owned local layout with
// separate business and stats database files mirroring Node's
// getBusinessDatabase()/getStatsDatabase() split.
type Store struct {
	mode     StoreMode
	db       *sql.DB // stats DB (SQLite) or shared PostgreSQL pool
	business *sql.DB // business DB (SQLite only)
	pool     *pgpool.Handle

	writeMu sync.Mutex // serializes multi-statement SQLite write paths

	tzMu        sync.Mutex
	tzValue     string
	tzExpiresAt time.Time

	dirtyMu        sync.Mutex
	dirtyClientIP  map[string]struct{} // mirrors clientIpRangeWindowDirtyIpHashes
	dirtyAccountIP map[string]struct{} // mirrors clientIpAccountRangeWindowDirtyIpHashes
}

type StoreConfig struct {
	Mode                 StoreMode
	SQLiteStatsPath      string
	SQLiteBusinessPath   string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
}

func OpenStore(config StoreConfig) (*Store, error) {
	switch config.Mode {
	case StoreSQLite:
		statsPath := strings.TrimSpace(config.SQLiteStatsPath)
		businessPath := strings.TrimSpace(config.SQLiteBusinessPath)
		if statsPath == "" || businessPath == "" {
			return nil, errors.New("statsverify sqlite 缺少 stats 或 business 数据库路径")
		}
		statsDSN, err := sqliteDSN(statsPath)
		if err != nil {
			return nil, err
		}
		businessDSN, err := sqliteDSN(businessPath)
		if err != nil {
			return nil, err
		}
		statsDB, err := sql.Open("sqlite", statsDSN)
		if err != nil {
			return nil, err
		}
		statsDB.SetMaxOpenConns(1)
		statsDB.SetMaxIdleConns(1)
		businessDB, err := sql.Open("sqlite", businessDSN)
		if err != nil {
			_ = statsDB.Close()
			return nil, err
		}
		businessDB.SetMaxOpenConns(1)
		businessDB.SetMaxIdleConns(1)
		if _, err := statsDB.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
			_ = statsDB.Close()
			_ = businessDB.Close()
			return nil, fmt.Errorf("配置 statsverify stats sqlite 失败: %w", err)
		}
		if _, err := businessDB.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
			_ = statsDB.Close()
			_ = businessDB.Close()
			return nil, fmt.Errorf("配置 statsverify business sqlite 失败: %w", err)
		}
		store := &Store{mode: StoreSQLite, db: statsDB, business: businessDB}
		if err := store.EnsureSchema(context.Background()); err != nil {
			_ = statsDB.Close()
			_ = businessDB.Close()
			return nil, err
		}
		return store, nil
	case StorePostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("statsverify postgres 缺少连接 URL")
		}
		maxOpen := config.PostgresMaxOpenConns
		if maxOpen == 0 {
			maxOpen = 1000
		}
		maxIdle := config.PostgresMaxIdleConns
		if maxIdle == 0 {
			maxIdle = 1000
		}
		if maxOpen < 1 || maxIdle < 1 || maxIdle > maxOpen {
			return nil, fmt.Errorf("statsverify postgres max open/idle 必须满足 1 <= idle <= open，实际为 %d/%d", maxOpen, maxIdle)
		}
		pool := config.PostgresPool
		if pool == nil {
			registry := pgpool.NewRegistry()
			var err error
			pool, err = registry.Acquire("pgx", config.PostgresURL, "statsverify-store", maxOpen, maxIdle)
			if err != nil {
				return nil, err
			}
		}
		return &Store{mode: StorePostgres, db: pool.DB(), pool: pool}, nil
	default:
		return nil, errors.New("statsverify store mode 必须为 sqlite 或 postgres")
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.mode == StoreSQLite {
		err := s.db.Close()
		if s.business != nil {
			if businessErr := s.business.Close(); err == nil {
				err = businessErr
			}
		}
		return err
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

// EnsureSchema creates the Go-owned SQLite layout. PostgreSQL is externally
// provisioned: the check fails closed instead of issuing DDL, matching the
// Node migration contract.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("statsverify store 未初始化")
	}
	if s.mode == StorePostgres {
		return s.checkPostgresSchema(ctx)
	}
	if _, err := s.db.ExecContext(ctx, sqliteStatsSchema); err != nil {
		return fmt.Errorf("初始化 statsverify stats sqlite schema 失败: %w", err)
	}
	if _, err := s.business.ExecContext(ctx, sqliteBusinessSchema); err != nil {
		return fmt.Errorf("初始化 statsverify business sqlite schema 失败: %w", err)
	}
	return nil
}

func (s *Store) checkPostgresSchema(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT table_schema, table_name FROM information_schema.tables WHERE (table_schema = ANY($1) OR table_schema = ANY($2)) AND table_name = ANY($3)`,
		[]string{"juhe_stats"}, []string{"juhe_business", "juhe_usage"}, postgresRequiredTables)
	if err != nil {
		return fmt.Errorf("读取 statsverify postgres tables 失败: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(postgresRequiredTables))
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			return fmt.Errorf("读取 statsverify postgres table 名称失败: %w", err)
		}
		seen[schema+"."+table] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 statsverify postgres tables 失败: %w", err)
	}
	missing := make([]string, 0)
	for _, qualified := range postgresRequiredTables {
		if _, ok := seen[qualified]; !ok {
			missing = append(missing, qualified)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("statsverify PostgreSQL 缺少外部预置表: %s", strings.Join(missing, ", "))
	}
	return nil
}

// LoadUsageStatsTimezone mirrors usageStatsTimezoneAsync
// (usage-stats-helpers.ts): reads system_settings key 'usageStatsTimezone'
// for the sys_admin system account, JSON-decodes the value, validates the
// IANA timezone, and caches it for 60 seconds.
func (s *Store) LoadUsageStatsTimezone(ctx context.Context, now time.Time) (string, error) {
	s.tzMu.Lock()
	if s.tzValue != "" && now.Before(s.tzExpiresAt) {
		value := s.tzValue
		s.tzMu.Unlock()
		return value, nil
	}
	s.tzMu.Unlock()

	query := `SELECT value_json FROM system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
	if s.mode == StorePostgres {
		query = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
	}
	var rawValue sql.NullString
	if err := s.queryRowContext(ctx, query).Scan(&rawValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("系统设置缺少 usageStatsTimezone")
		}
		return "", fmt.Errorf("读取 usageStatsTimezone 失败: %w", err)
	}
	if !rawValue.Valid || rawValue.String == "" {
		return "", errors.New("系统设置缺少 usageStatsTimezone")
	}
	var value string
	if err := json.Unmarshal([]byte(rawValue.String), &value); err != nil {
		return "", fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	timezone := strings.TrimSpace(value)
	if timezone == "" {
		return "", errors.New("统计时区必须是非空字符串")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("统计时区不存在：%s", timezone)
	}
	_ = location
	s.tzMu.Lock()
	s.tzValue = timezone
	s.tzExpiresAt = now.Add(UsageStatsTimezoneCacheTTL)
	s.tzMu.Unlock()
	return timezone, nil
}

// LoadUsageStatsLocation is LoadUsageStatsTimezone with the parsed
// *time.Location attached.
func (s *Store) LoadUsageStatsLocation(ctx context.Context, now time.Time) (*time.Location, string, error) {
	timezone, err := s.LoadUsageStatsTimezone(ctx, now)
	if err != nil {
		return nil, "", err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, "", fmt.Errorf("统计时区不存在：%s", timezone)
	}
	return location, timezone, nil
}

// rememberDirtyIPHashes mirrors the in-process dirty sets
// clientIpRangeWindowDirtyIpHashes/clientIpAccountRangeWindowDirtyIpHashes
// (client-ip-usage-range-windows.repository.ts). Node keeps module-level
// Sets; jobs scope them per Store so tests stay isolated.
func (s *Store) rememberDirtyIPHashes(ipHashes []string) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	if s.dirtyClientIP == nil {
		s.dirtyClientIP = make(map[string]struct{})
		s.dirtyAccountIP = make(map[string]struct{})
	}
	for _, ipHash := range ipHashes {
		s.dirtyClientIP[ipHash] = struct{}{}
		s.dirtyAccountIP[ipHash] = struct{}{}
	}
}

func (s *Store) forgetDirtyIPHashes(ipHashes []string) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	for _, ipHash := range ipHashes {
		delete(s.dirtyClientIP, ipHash)
		delete(s.dirtyAccountIP, ipHash)
	}
}

func (s *Store) hasInMemoryDirtyIPHashes() bool {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	return len(s.dirtyClientIP) > 0 || len(s.dirtyAccountIP) > 0
}

// queryRowContext picks the database handle for cross-domain reads
// (system_settings lives in the business database on SQLite).
func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if s.mode == StoreSQLite && isBusinessTableQuery(query) {
		return s.business.QueryRowContext(ctx, query, args...)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

func isBusinessTableQuery(query string) bool {
	return strings.Contains(query, "system_settings")
}

// dialect helpers -----------------------------------------------------------

func (s *Store) placeholder(index int) string {
	if s.mode == StorePostgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (s *Store) placeholders(count int) string {
	parts := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		parts = append(parts, s.placeholder(index))
	}
	return strings.Join(parts, ", ")
}

// placeholdersFrom renders `count` placeholders starting at parameter index
// `start`, so literal values (like current_concurrency's 0) can sit between
// two placeholder runs on PostgreSQL.
func (s *Store) placeholdersFrom(start, count int) string {
	parts := make([]string, 0, count)
	for index := start; index < start+count; index++ {
		parts = append(parts, s.placeholder(index))
	}
	return strings.Join(parts, ", ")
}

func (s *Store) statsTable(name string) string {
	if s.mode == StorePostgres {
		return "juhe_stats." + name
	}
	return name
}

func (s *Store) businessTable(name string) string {
	if s.mode == StorePostgres {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) usageTable(name string) string {
	if s.mode == StorePostgres {
		return "juhe_usage." + name
	}
	return name
}

// greatest renders the two-arg maximum: GREATEST on PostgreSQL, MAX(a,b)
// scalar form on SQLite.
func (s *Store) greatest(left, right string) string {
	if s.mode == StorePostgres {
		return fmt.Sprintf("GREATEST(%s, %s)", left, right)
	}
	return fmt.Sprintf("MAX(%s, %s)", left, right)
}

func sqliteDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析 statsverify sqlite 路径失败: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}).String(), nil
}

var postgresRequiredTables = []string{
	"juhe_usage.usage_records",
	"juhe_stats.stats_job_state",
	"juhe_stats.client_ip_registry",
	"juhe_stats.client_ip_stats_daily",
	"juhe_stats.client_ip_account_stats_daily",
	"juhe_stats.client_ip_usage_range_windows",
	"juhe_stats.client_ip_account_usage_range_windows",
	"juhe_stats.client_ip_range_window_dirty_ips",
	"juhe_stats.client_ip_account_range_window_dirty_ips",
	"juhe_stats.group_account_stats",
	"juhe_stats.usage_stats_daily",
	"juhe_stats.usage_stats_hourly",
	"juhe_business.system_settings",
	"juhe_business.groups",
	"juhe_business.group_accounts",
	"juhe_business.accounts",
	"juhe_business.resource_authorizations",
	"juhe_business.group_account_stats_dirty",
}

// SQLite layout -------------------------------------------------------------
//
// Column sets mirror the Node tables; the usage_records table here is the
// Go-owned local aggregation source (Node's SQLite driver fans across
// usage-record shard files, which do not exist in the Go layout).

const sqliteStatsSchema = `
CREATE TABLE IF NOT EXISTS usage_records (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	traffic_source TEXT NOT NULL,
	client_ip TEXT,
	api_key_id TEXT,
	group_id TEXT,
	account_id TEXT,
	model TEXT,
	status_code INTEGER,
	success INTEGER NOT NULL,
	first_token_ms INTEGER,
	duration_ms INTEGER,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_read_tokens INTEGER,
	cache_read_cost_usd REAL,
	cache_write_tokens INTEGER,
	cache_write_1h_tokens INTEGER,
	cache_write_cost_usd REAL,
	thinking_tokens INTEGER,
	input_image_tokens INTEGER,
	output_image_tokens INTEGER,
	cost_usd REAL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_statsverify_usage_records_cursor ON usage_records(created_at, id);
CREATE TABLE IF NOT EXISTS stats_job_state (
	scope_type TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	job_name TEXT NOT NULL,
	cursor_created_at TEXT,
	cursor_id TEXT,
	last_success_at TEXT,
	last_error_message TEXT,
	lag_seconds INTEGER,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (scope_type, scope_id, job_name)
);
CREATE TABLE IF NOT EXISTS client_ip_registry (
	ip_hash TEXT PRIMARY KEY,
	bucket_no INTEGER NOT NULL,
	aggregate_ip_key TEXT NOT NULL,
	client_ip TEXT NOT NULL,
	ip_version INTEGER NOT NULL,
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS client_ip_stats_daily (
	ip_hash TEXT NOT NULL,
	stat_date TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	duration_ms_sum INTEGER NOT NULL,
	duration_ms_count INTEGER NOT NULL,
	duration_ms_max INTEGER NOT NULL,
	first_token_ms_sum INTEGER NOT NULL,
	first_token_ms_count INTEGER NOT NULL,
	last_used_at TEXT,
	last_error_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (ip_hash, stat_date)
);
CREATE TABLE IF NOT EXISTS client_ip_account_stats_daily (
	ip_hash TEXT NOT NULL,
	account_id TEXT NOT NULL,
	stat_date TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	duration_ms_sum INTEGER NOT NULL,
	duration_ms_count INTEGER NOT NULL,
	duration_ms_max INTEGER NOT NULL,
	first_token_ms_sum INTEGER NOT NULL,
	first_token_ms_count INTEGER NOT NULL,
	last_used_at TEXT,
	last_error_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (ip_hash, account_id, stat_date)
);
CREATE TABLE IF NOT EXISTS client_ip_usage_range_windows (
	ip_hash TEXT NOT NULL,
	start_date TEXT NOT NULL,
	end_date TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	duration_ms_sum INTEGER NOT NULL,
	duration_ms_count INTEGER NOT NULL,
	duration_ms_max INTEGER NOT NULL,
	average_duration_ms REAL,
	first_token_ms_sum INTEGER NOT NULL,
	first_token_ms_count INTEGER NOT NULL,
	average_first_token_ms REAL,
	active_days INTEGER NOT NULL,
	last_used_at TEXT,
	last_error_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (ip_hash, start_date, end_date)
);
CREATE TABLE IF NOT EXISTS client_ip_account_usage_range_windows (
	ip_hash TEXT NOT NULL,
	account_id TEXT NOT NULL,
	start_date TEXT NOT NULL,
	end_date TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	duration_ms_sum INTEGER NOT NULL,
	duration_ms_count INTEGER NOT NULL,
	duration_ms_max INTEGER NOT NULL,
	average_duration_ms REAL,
	first_token_ms_sum INTEGER NOT NULL,
	first_token_ms_count INTEGER NOT NULL,
	average_first_token_ms REAL,
	active_days INTEGER NOT NULL,
	last_used_at TEXT,
	last_error_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (ip_hash, account_id, start_date, end_date)
);
CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (
	ip_hash TEXT PRIMARY KEY,
	generation INTEGER NOT NULL,
	first_dirty_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips (
	ip_hash TEXT PRIMARY KEY,
	generation INTEGER NOT NULL,
	first_dirty_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_account_stats (
	system_account_id TEXT NOT NULL,
	group_id TEXT PRIMARY KEY,
	total INTEGER NOT NULL,
	available INTEGER NOT NULL,
	active INTEGER NOT NULL,
	disabled INTEGER NOT NULL,
	error INTEGER NOT NULL,
	rate_limited INTEGER NOT NULL,
	current_concurrency INTEGER NOT NULL,
	concurrency_limit INTEGER NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_stats_daily (
	system_account_id TEXT NOT NULL,
	scope_type TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	stat_date TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
);
CREATE TABLE IF NOT EXISTS usage_stats_hourly (
	system_account_id TEXT NOT NULL,
	scope_type TEXT NOT NULL,
	scope_id TEXT NOT NULL,
	stat_hour TEXT NOT NULL,
	request_count INTEGER NOT NULL,
	success_count INTEGER NOT NULL,
	error_count INTEGER NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read_tokens INTEGER NOT NULL,
	cache_read_cost_usd REAL NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	cache_write_cost_usd REAL NOT NULL,
	thinking_tokens INTEGER NOT NULL,
	input_image_tokens INTEGER NOT NULL,
	output_image_tokens INTEGER NOT NULL,
	total_cost_usd REAL NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
);
`

const sqliteBusinessSchema = `
CREATE TABLE IF NOT EXISTS system_settings (
	system_account_id TEXT NOT NULL,
	key TEXT NOT NULL,
	value_json TEXT NOT NULL,
	PRIMARY KEY (system_account_id, key)
);
CREATE TABLE IF NOT EXISTS groups (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_accounts (
	group_id TEXT NOT NULL,
	account_id TEXT NOT NULL,
	account_authorization_id TEXT,
	enabled INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (group_id, account_id)
);
CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	status TEXT NOT NULL,
	schedulable INTEGER NOT NULL DEFAULT 1,
	cooldown_until TEXT,
	concurrency_limit INTEGER NOT NULL DEFAULT 0,
	deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS resource_authorizations (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	expires_at TEXT
);
CREATE TABLE IF NOT EXISTS group_account_stats_dirty (
	group_id TEXT PRIMARY KEY,
	reason TEXT,
	updated_at TEXT NOT NULL
);
`
