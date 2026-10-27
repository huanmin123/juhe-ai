package statsverify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// UsageStatsTimezoneCacheTTL mirrors usageStatsTimezoneCacheTtlMs
// (usage-stats-helpers.ts line 13, 60_000).
const UsageStatsTimezoneCacheTTL = 60 * time.Second

// Store owns the three jobs' storage facts. PostgreSQL writes target the
// externally provisioned juhe_stats/juhe_business/juhe_usage schemas and are
// never migrated by this package（SQLite 本地布局随存储形态退役删除，本包
// 收敛为 PG 单形态）.
type Store struct {
	db   *sql.DB // shared PostgreSQL pool
	pool *pgpool.Handle

	writeMu sync.Mutex // serializes multi-statement write paths

	tzMu        sync.Mutex
	tzValue     string
	tzExpiresAt time.Time

	dirtyMu        sync.Mutex
	dirtyClientIP  map[string]struct{} // mirrors clientIpRangeWindowDirtyIpHashes
	dirtyAccountIP map[string]struct{} // mirrors clientIpAccountRangeWindowDirtyIpHashes
}

type StoreConfig struct {
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
}

func OpenStore(config StoreConfig) (*Store, error) {
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
	return &Store{db: pool.DB(), pool: pool}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

// EnsureSchema 校验外部供给的 PostgreSQL schema（SQLite 本地布局已随存储
// 形态退役删除）：校验失败 fail-closed 而非下发 DDL，对齐 Node 迁移契约。
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("statsverify store 未初始化")
	}
	return s.checkPostgresSchema(ctx)
}

func (s *Store) checkPostgresSchema(ctx context.Context) error {
	// 匹配限定名（table_schema || '.' || table_name）而非裸表名：
	// postgresRequiredTables 存限定名，裸表名恒不匹配会导致 PG 校验恒失败
	//（w10c PG 门禁取证后修复）。
	rows, err := s.db.QueryContext(ctx, `SELECT table_schema || '.' || table_name AS qualified
		FROM information_schema.tables
		WHERE (table_schema = ANY($1) OR table_schema = ANY($2))
		  AND (table_schema || '.' || table_name) = ANY($3)`,
		[]string{"juhe_stats"}, []string{"juhe_business", "juhe_usage"}, postgresRequiredTables)
	if err != nil {
		return fmt.Errorf("读取 statsverify postgres tables 失败: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(postgresRequiredTables))
	for rows.Next() {
		var qualified string
		if err := rows.Scan(&qualified); err != nil {
			return fmt.Errorf("读取 statsverify postgres table 名称失败: %w", err)
		}
		seen[qualified] = struct{}{}
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
	query = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
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

// queryRowContext 统一 stats 库读句柄（PG 与 stats 同池，业务侧
// system_settings 经 juhe_business 前缀同连接可查）。
func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}

// dialect helpers -----------------------------------------------------------

func (s *Store) placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
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
	return "juhe_stats." + name
}

func (s *Store) businessTable(name string) string {
	return "juhe_business." + name
}

func (s *Store) usageTable(name string) string {
	return "juhe_usage." + name
}

// greatest renders the two-arg maximum (GREATEST).
func (s *Store) greatest(left, right string) string {
	return fmt.Sprintf("GREATEST(%s, %s)", left, right)
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
//
// usage_records 列形状必须覆盖 statsagg 聚合器的 SELECT 全列
// （statsagg aggregate.go usageStatsRecordSelectColumns）：usagewriter 在
// SQLite standalone 模式把每批分片写镜像进本表（store.go mirrorStats…），
// 缺任何聚合列都会让聚合查询以 "no such column" 失败。存量窄形状库文件由
// ensureAggregateColumnBackfills 幂等补列。

// sqliteStatsSchema 及其后的 business schema 常量承接建库；历史错口径行的
// 数据回填唯一入口是 maintenance --apply-success-cost-backfill 一次性子命令
// （计划 PLAN-20261011T225418），ensure 不再执行任何同谓词回填扫描。
