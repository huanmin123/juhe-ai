package accounthealth

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
)

// SQLiteDirectInputReader 是 sqlite store 模式的 J1 直读适配器：业务 facts 走
// businessDB（gateway 是唯一写入方），统计用量走 statsDB（stats owner 持有写
// 入），两个句柄都必须以只读范式打开（mode=ro + query_only）。候选语义与
// PostgresDirectInputReader 完全同构：扫描/分页/cap/构造/配额判定直接复用
// PG 实现，仅 SQL 做方言翻译（LATERAL→窗口 CTE、时间参数固定毫秒文本）。
type SQLiteDirectInputReader struct {
	businessDB       *sql.DB
	statsDB          *sql.DB
	credentialSecret string
	inputTTL         time.Duration
	now              func() time.Time
	suppression      func(context.Context, time.Time) ([]DirectInputSuppression, error)
	scheduleCache    *directScheduleCache
}

func NewSQLiteDirectInputReader(businessDB, statsDB *sql.DB, credentialSecret string, inputTTL time.Duration, now func() time.Time) (*SQLiteDirectInputReader, error) {
	if businessDB == nil || statsDB == nil || strings.TrimSpace(credentialSecret) == "" || inputTTL < time.Minute || inputTTL > 7*24*time.Hour {
		return nil, fmt.Errorf("SQLite direct input reader 配置无效")
	}
	if now == nil {
		now = time.Now
	}
	return &SQLiteDirectInputReader{businessDB: businessDB, statsDB: statsDB, credentialSecret: credentialSecret, inputTTL: inputTTL, now: now, scheduleCache: &directScheduleCache{ttl: directScheduleCacheTTL, now: now}}, nil
}

// SetSuppressionProvider 与 PG reader 同义：注入 jobs 拥有的重试抑制快照；
// 业务/统计 reader 对 jobs schema 仍然零写入。
func (r *SQLiteDirectInputReader) SetSuppressionProvider(provider func(context.Context, time.Time) ([]DirectInputSuppression, error)) {
	if r != nil {
		r.suppression = provider
	}
}

// CheckContract 只做零行只读校验：business 句柄对 7 张业务表、stats 句柄对
// 5 张统计表逐个探活，settings 六键经 loadDirectScheduleFrom 校验，最后以
// limit=0 实参执行完整候选 SQL。不建表、不修复 schema；schema 不完整必须
// 显式失败（与 PG reader 的契约门禁一致）。
func (r *SQLiteDirectInputReader) CheckContract(ctx context.Context) error {
	btx, err := r.businessDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("开始 SQLite direct input 契约预检事务失败: %w", err)
	}
	defer btx.Rollback()
	stx, err := r.statsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("开始 SQLite direct input 统计库契约预检事务失败: %w", err)
	}
	defer stx.Rollback()
	for _, table := range sqliteDirectInputBusinessRelations {
		rows, err := btx.QueryContext(ctx, "SELECT 1 FROM "+table+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("SQLite direct input 缺少业务只读契约 %s: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("关闭 SQLite direct input 业务契约探活游标失败: %w", err)
		}
	}
	for _, table := range sqliteDirectInputStatsRelations {
		rows, err := stx.QueryContext(ctx, "SELECT 1 FROM "+table+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("SQLite direct input 缺少统计只读契约 %s: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("关闭 SQLite direct input 统计契约探活游标失败: %w", err)
		}
	}
	if _, _, err := loadDirectScheduleFrom(ctx, btx, "system_settings", "SQLite direct input"); err != nil {
		return err
	}
	rows, err := btx.QueryContext(ctx, sqliteDirectInputCandidatesSQL, sqliteDirectTimestamp(r.now()), 0, 0, "", 0)
	if err != nil {
		return fmt.Errorf("验证 SQLite direct input 候选查询失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 SQLite direct input 契约预检游标失败: %w", err)
	}
	if err := btx.Commit(); err != nil {
		return fmt.Errorf("提交 SQLite direct input 契约预检事务失败: %w", err)
	}
	if err := stx.Commit(); err != nil {
		return fmt.Errorf("提交 SQLite direct input 统计库契约预检事务失败: %w", err)
	}
	return nil
}

// sqliteDirectInputBusinessRelations / sqliteDirectInputStatsRelations 是
// CheckContract 的关系清单，与 directInputRequiredRelations 的 business/stats
// 子集一一对应；SQLite 侧业务与统计是两个物理文件，由两个句柄分别持有。
var sqliteDirectInputBusinessRelations = []string{
	"accounts",
	"account_health_jobs_input_versions",
	"group_accounts",
	"proxy_profiles",
	"resource_authorizations",
	"resource_authorization_grants",
	"system_settings",
}

var sqliteDirectInputStatsRelations = []string{
	"usage_stats_totals",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	"usage_quota_hourly_windows",
}

func (r *SQLiteDirectInputReader) LoadDue(ctx context.Context, limit int) ([]Input, error) {
	result, err := r.LoadDueWithFailures(ctx, limit)
	if err == nil && len(result.Failures) > 0 {
		return nil, fmt.Errorf("SQLite direct input 存在 %d 个候选构造失败；请使用 LoadDueWithFailures 处理隔离结果", len(result.Failures))
	}
	return result.Inputs, err
}

func (r *SQLiteDirectInputReader) LoadDueWithFailures(ctx context.Context, limit int) (DirectInputLoadResult, error) {
	return r.load(ctx, limit, false, "")
}

// LoadAccount 与 PG reader 同义：仅供显式请求使用，跳过周期到期谓词但保留
// 其余全部资格守卫。
func (r *SQLiteDirectInputReader) LoadAccount(ctx context.Context, accountID string) ([]Input, error) {
	result, err := r.LoadAccountWithFailures(ctx, accountID)
	if err == nil && len(result.Failures) > 0 {
		return nil, fmt.Errorf("SQLite direct input account=%s 候选构造失败；请使用 LoadAccountWithFailures 处理隔离结果", strings.TrimSpace(accountID))
	}
	return result.Inputs, err
}

func (r *SQLiteDirectInputReader) LoadAccountWithFailures(ctx context.Context, accountID string) (DirectInputLoadResult, error) {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return DirectInputLoadResult{}, fmt.Errorf("SQLite direct input account ID 不能为空")
	}
	return r.load(ctx, 1, true, normalizedAccountID)
}

func (r *SQLiteDirectInputReader) load(ctx context.Context, limit int, ignoreSchedule bool, accountID string) (DirectInputLoadResult, error) {
	if limit < 1 || limit > maxJ1Capacity {
		return DirectInputLoadResult{}, fmt.Errorf("SQLite direct input limit 必须在 1..%d", maxJ1Capacity)
	}
	now := r.now().UTC()
	var suppressions []DirectInputSuppression
	if r.suppression != nil {
		var err error
		suppressions, err = r.suppression(ctx, now)
		if err != nil {
			return DirectInputLoadResult{}, fmt.Errorf("读取 SQLite direct input 重试抑制快照失败: %w", err)
		}
	}
	// 业务与统计是两个物理文件，无法单事务覆盖；统计侧只做有界读数（额度
	// 花费），可接受的最终一致窗口与 PG 版单事务语义等价到统计新鲜度为止。
	btx, err := r.businessDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("开始 SQLite direct input 只读事务失败: %w", err)
	}
	defer btx.Rollback()
	stx, err := r.statsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("开始 SQLite direct input 统计只读事务失败: %w", err)
	}
	defer stx.Rollback()
	schedule, timezone, err := r.scheduleCache.load(ctx, func(fetchCtx context.Context) (Schedule, *time.Location, error) {
		return loadDirectScheduleFrom(fetchCtx, btx, "system_settings", "SQLite direct input")
	})
	if err != nil {
		return DirectInputLoadResult{}, err
	}
	result := DirectInputLoadResult{Inputs: make([]Input, 0, limit), Failures: make([]DirectInputFailure, 0)}
	err = collectDirectCandidatePages(limit, func(offset int) (int, error) {
		candidateSQL, suppressionArgs := sqliteDirectInputCandidatesQuery(suppressions)
		ignoreScheduleInt := 0
		if ignoreSchedule {
			ignoreScheduleInt = 1
		}
		args := []any{sqliteDirectTimestamp(now), limit, ignoreScheduleInt, accountID, offset}
		args = append(args, suppressionArgs...)
		rows, err := btx.QueryContext(ctx, candidateSQL, args...)
		if err != nil {
			return 0, fmt.Errorf("读取 SQLite direct input 候选失败: %w", err)
		}
		pageCount := 0
		candidates := make([]directCandidate, 0, limit)
		for rows.Next() {
			pageCount++
			candidate, err := scanDirectCandidate(rows)
			if err != nil {
				if failure, ok := directInputFailureForCandidate(candidate); ok {
					result.Failures = append(result.Failures, failure)
					continue
				}
				rows.Close()
				return 0, err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Close(); err != nil {
			return 0, fmt.Errorf("关闭 SQLite direct input 候选游标失败: %w", err)
		}
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("遍历 SQLite direct input 候选失败: %w", err)
		}
		for _, candidate := range candidates {
			if len(result.Inputs) >= limit {
				break
			}
			if candidate.authorization != nil {
				eligible, err := sqliteAuthorizationQuotaEligible(ctx, btx, stx, candidate, now, timezone)
				if err != nil {
					return 0, err
				}
				// 超限授权不是坏输入：与 PG 版一致 fail-closed 地剔除该候选，
				// 不让无关账户加载失败，也不探活或落回退输入。
				if !eligible {
					continue
				}
				candidate.authorization.QuotaEligible = eligible
			}
			direct := DirectInput{Account: candidate.account, Authorization: candidate.authorization, Source: candidate.source, Binding: candidate.binding, Proxy: candidate.proxy, InputVersion: candidate.inputVersion, IssuedAt: now, ExpiresAt: now.Add(r.inputTTL), TLSPolicy: "j1-direct-upstream-v1", Schedule: schedule}
			input, failure, err := buildDirectCandidateInput(candidate, direct, r.credentialSecret, now)
			if err != nil {
				return 0, err
			}
			if failure != nil {
				result.Failures = append(result.Failures, *failure)
				if len(result.Inputs) >= limit {
					break
				}
				continue
			}
			result.Inputs = append(result.Inputs, input)
			if len(result.Inputs) >= limit {
				break
			}
		}
		return pageCount, nil
	}, func() int { return len(result.Inputs) })
	if err != nil {
		return DirectInputLoadResult{}, err
	}
	if err := btx.Commit(); err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("提交 SQLite direct input 只读事务失败: %w", err)
	}
	if err := stx.Commit(); err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("提交 SQLite direct input 统计只读事务失败: %w", err)
	}
	return result, nil
}

// sqliteDirectTimestamp 固定毫秒 UTC 文本，与账户主路径写侧（gateway
// accountscore 的 IsoMillis）一致：业务时间列以 TEXT 比较，同格式下字典序
// 即时间序。注意 J1 投影写侧（projection.go）与 authz provision
// （authz/mutations.go）以 RFC3339Nano 写部分时间列（秒内零尾截断），与固定
// 毫秒文本在同秒边界存在 ±1 秒比较误差；对小时级健康周期不可感知，不作为
// 行为分叉依据。
func sqliteDirectTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// EnsureSQLiteDirectInputLayout 在打开只读直读句柄之前幂等兜底 J1 直读所需的
// 两库布局，消除「J1 契约预检早于布局创建」的冷启动时序依赖（旧 files 模式
// 不依赖两库，这是 sqlite 直读缺省化引入的回归）：
//
//  1. statsverify 布局 ensure——与组合根 buildWorkerAssembly 的 stats-verify
//     ensure 同一 DDL、幂等，这里只是前置；statsverify.OpenStore 以 WAL 可写
//     方式打开并自行 EnsureSchema，句柄用完即关，不与后续只读直读句柄共存。
//  2. J1 契约面兜底——statsverify 布局不含 J1 契约要求（含候选 SQL 引用）的
//     usage_stats_totals/usage_stats_weekly/usage_stats_monthly/
//     usage_quota_hourly_windows（stats）与 account_health_jobs_input_versions/
//     proxy_profiles/resource_authorization_grants/account_model_mappings
//     （business），也不播种 J1 settings 六键；常规部署它们由 gateway
//     preflight / maintenance --ensure-schema 创建，这里按权威 schema
//     （maintenance/internal/schema，列集逐列一致）做 CREATE TABLE IF NOT
//     EXISTS 与 INSERT OR IGNORE 缺省兜底，保证「两库文件都不存在」的冷启动
//     不再因契约缺失拉停。
//  3. 窄表列级升级——statsverify 先建出的 accounts/group_accounts/
//     resource_authorizations 为窄列形，候选 SQL 依赖的缺失列按 PRAGMA 差分
//     以可空 ADD COLUMN 幂等补齐（gateway/maintenance canonical 表全部命中
//     既有列，为无操作）。
//
// 只建表、不迁移、不改既有行；gateway 先行自举时本函数全部语句为无操作。
func EnsureSQLiteDirectInputLayout(ctx context.Context, businessPath, statsPath string) error {
	// 段一：statsverify 布局（业务/统计两文件与 Go-owned 本地基线）。
	store, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:               statsverify.StoreSQLite,
		SQLiteStatsPath:    statsPath,
		SQLiteBusinessPath: businessPath,
	})
	if err != nil {
		return err
	}
	if err := store.EnsureSchema(ctx); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	// 段二：J1 契约面兜底（独立短句柄，即开即关）。
	businessDSN, err := sqliteDSN(businessPath)
	if err != nil {
		return err
	}
	statsDSN, err := sqliteDSN(statsPath)
	if err != nil {
		return err
	}
	business, err := sql.Open("sqlite", businessDSN)
	if err != nil {
		return err
	}
	defer business.Close()
	business.SetMaxOpenConns(1)
	stats, err := sql.Open("sqlite", statsDSN)
	if err != nil {
		return err
	}
	defer stats.Close()
	stats.SetMaxOpenConns(1)
	for _, ddl := range sqliteDirectInputBusinessContractDDL {
		if _, err := business.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("兜底 J1 业务契约表失败: %w", err)
		}
	}
	for _, ddl := range sqliteDirectInputStatsContractDDL {
		if _, err := stats.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("兜底 J1 统计契约表失败: %w", err)
		}
	}
	if err := ensureSQLiteDirectInputColumns(ctx, business); err != nil {
		return err
	}
	return seedSQLiteDirectSettings(ctx, business)
}

// sqliteDirectInputNarrowTableColumns 列出 statsverify 窄表缺少、而 J1 候选
// SQL 依赖的列：值即 ALTER TABLE ADD COLUMN 的列定义（全部可空，幂等追加，
// 既有行与 gateway/maintenance 先行建出的 canonical 表均不受影响）。
var sqliteDirectInputNarrowTableColumns = map[string][]string{
	"accounts": {
		"config_revision INTEGER",
		"dispatch_revision INTEGER",
		"provider_code TEXT",
		"provider_protocol_profile_id TEXT",
		"protocol_code TEXT",
		"protocol_version TEXT",
		"type TEXT",
		"client_compatibility TEXT",
		"health_check_endpoint_mode TEXT",
		"health_check_model TEXT",
		"credentials_encrypted TEXT",
		"account_expires_at TEXT",
		"temporary_unavailable_continuous_probe_enabled INTEGER",
		"cooldown_retest_observation_started_at TEXT",
		"cooldown_retest_generation TEXT",
		"authorization_instance_authorization_id TEXT",
		"authorization_instance_source_account_id TEXT",
		"proxy_profile_id TEXT",
		"next_health_check_at TEXT",
		"last_health_check_at TEXT",
		"last_error_code TEXT",
		"created_at TEXT",
		"updated_at TEXT",
	},
	"group_accounts": {
		"system_account_id TEXT",
		"updated_at TEXT",
	},
	"resource_authorizations": {
		"resource_type TEXT",
		"resource_id TEXT",
		"resource_owner_system_account_id TEXT",
		"grantee_system_account_id TEXT",
		"effective_source_team_id TEXT",
		"limits_json TEXT",
	},
}

// ensureSQLiteDirectInputColumns 对 statsverify 窄表做 PRAGMA table_info 差
// 分，把 J1 候选 SQL 依赖但缺失的列以可空 ADD COLUMN 幂等补齐；列已存在或
// 表为 canonical 形状时全部为无操作。
func ensureSQLiteDirectInputColumns(ctx context.Context, business *sql.DB) error {
	for table, columns := range sqliteDirectInputNarrowTableColumns {
		existing := map[string]bool{}
		rows, err := business.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return fmt.Errorf("读取 %s 列清单失败: %w", table, err)
		}
		for rows.Next() {
			var cid, notNull, pk int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			existing[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, definition := range columns {
			name := strings.Fields(definition)[0]
			if existing[name] {
				continue
			}
			if _, err := business.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+definition); err != nil {
				return fmt.Errorf("补齐 J1 契约列 %s.%s 失败: %w", table, name, err)
			}
		}
	}
	return nil
}

// seedSQLiteDirectSettings 播种 J1 settings 六键缺省（值取 maintenance schema
// seed 的 canonical 值，INSERT OR IGNORE 不覆盖既有设置）。system_settings 列
// 形环境相关（statsverify 兜底为 3 列、gateway/maintenance 权威为含 updated_at
// 的 4 列），按 PRAGMA table_info 探测后拼装插入列。
func seedSQLiteDirectSettings(ctx context.Context, business *sql.DB) error {
	hasUpdatedAt := false
	rows, err := business.QueryContext(ctx, `PRAGMA table_info(system_settings)`)
	if err == nil {
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == "updated_at" {
				hasUpdatedAt = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	} else {
		rows.Close()
		return err
	}
	statement := `INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', ?, ?)`
	if hasUpdatedAt {
		statement = `INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, ?)`
	}
	for _, setting := range []struct{ key, value string }{
		{key: "accountHealthCheckIntervalHours", value: "1"},
		{key: "accountHealthCheckJitterMinutes", value: "10"},
		{key: "accountHealthCheckFailureThreshold", value: "3"},
		{key: "defaultTemporaryUnschedulableMinutes", value: "2"},
		{key: "cooldownAccountRetestMaxBackoffHours", value: "12"},
		{key: "usageStatsTimezone", value: `"Asia/Shanghai"`},
	} {
		var err error
		if hasUpdatedAt {
			_, err = business.ExecContext(ctx, statement, setting.key, setting.value, sqliteDirectTimestamp(time.Now()))
		} else {
			_, err = business.ExecContext(ctx, statement, setting.key, setting.value)
		}
		if err != nil {
			return fmt.Errorf("播种 J1 settings 缺省 %s 失败: %w", setting.key, err)
		}
	}
	return nil
}

// sqliteDirectInputBusinessContractDDL / sqliteDirectInputStatsContractDDL 是
// J1 契约面兜底 DDL：列集与约束（含 FK/CHECK/PK）与权威 schema（maintenance/
// internal/schema/sqlite_schema_business.go、sqlite_schema_stats.go）逐列逐条
// 一致，两侧任一方先建表结果相同；修改权威 schema 时必须同步此处（或收敛为
// 共享 schema 包）。FK 声明在父表（providers/system_accounts/system_teams）
// 尚未由 gateway 建出的冷启动库中不生效（SQLite 默认 foreign_keys=OFF，本包
// 与 reader 连接均未开启；statsverify 连接虽开启，但这三张表是 gateway 业务
// 写面，jobs 家族不写），gateway 自举后父表齐备、约束语义与 canonical 一致。
var sqliteDirectInputBusinessContractDDL = []string{
	`CREATE TABLE IF NOT EXISTS proxy_profiles (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT,
  type TEXT NOT NULL,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  username TEXT,
  password_encrypted TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  test_status TEXT NOT NULL DEFAULT 'unknown',
  latency_ms INTEGER,
  outbound_ip TEXT,
  outbound_region TEXT,
  last_test_message TEXT,
  last_tested_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
  account_id TEXT PRIMARY KEY,
  current_version INTEGER NOT NULL CHECK (current_version >= 1),
  reserved_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (
  id TEXT PRIMARY KEY,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  resource_owner_system_account_id TEXT NOT NULL,
  grantee_type TEXT NOT NULL,
  grantee_system_account_id TEXT,
  grantee_team_id TEXT,
  scope TEXT NOT NULL DEFAULT 'use',
  status TEXT NOT NULL DEFAULT 'active',
  remark TEXT,
  expires_at TEXT,
  limits_json TEXT,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  revoked_by TEXT,
  revoked_at TEXT,
  revoked_reason TEXT,
  updated_at TEXT NOT NULL,
  CHECK (
    (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
    OR
    (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
  ),
  FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (grantee_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS account_model_mappings (
  account_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  source_model TEXT NOT NULL,
  source_endpoint_family TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  upstream_endpoint_family TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (account_id, source_model, source_endpoint_family),
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (provider_code) REFERENCES providers(code)
)`,
}

var sqliteDirectInputStatsContractDDL = []string{
	`CREATE TABLE IF NOT EXISTS usage_stats_totals (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  request_count INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_cost_usd REAL NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_cost_usd REAL NOT NULL DEFAULT 0,
  thinking_tokens INTEGER NOT NULL DEFAULT 0,
  input_image_tokens INTEGER NOT NULL DEFAULT 0,
  output_image_tokens INTEGER NOT NULL DEFAULT 0,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  duration_ms_sum INTEGER NOT NULL DEFAULT 0,
  duration_ms_count INTEGER NOT NULL DEFAULT 0,
  duration_ms_max INTEGER NOT NULL DEFAULT 0,
  first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
  first_token_ms_count INTEGER NOT NULL DEFAULT 0,
  first_token_ms_max INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  last_error_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id)
)`,
	`CREATE TABLE IF NOT EXISTS usage_stats_weekly (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  stat_week TEXT NOT NULL,
  request_count INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_cost_usd REAL NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_cost_usd REAL NOT NULL DEFAULT 0,
  thinking_tokens INTEGER NOT NULL DEFAULT 0,
  input_image_tokens INTEGER NOT NULL DEFAULT 0,
  output_image_tokens INTEGER NOT NULL DEFAULT 0,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  duration_ms_sum INTEGER NOT NULL DEFAULT 0,
  duration_ms_count INTEGER NOT NULL DEFAULT 0,
  duration_ms_max INTEGER NOT NULL DEFAULT 0,
  first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
  first_token_ms_count INTEGER NOT NULL DEFAULT 0,
  first_token_ms_max INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  last_error_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week)
)`,
	`CREATE TABLE IF NOT EXISTS usage_stats_monthly (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  stat_month TEXT NOT NULL,
  request_count INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_cost_usd REAL NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_cost_usd REAL NOT NULL DEFAULT 0,
  thinking_tokens INTEGER NOT NULL DEFAULT 0,
  input_image_tokens INTEGER NOT NULL DEFAULT 0,
  output_image_tokens INTEGER NOT NULL DEFAULT 0,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  duration_ms_sum INTEGER NOT NULL DEFAULT 0,
  duration_ms_count INTEGER NOT NULL DEFAULT 0,
  duration_ms_max INTEGER NOT NULL DEFAULT 0,
  first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
  first_token_ms_count INTEGER NOT NULL DEFAULT 0,
  first_token_ms_max INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  last_error_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month)
)`,
	`CREATE TABLE IF NOT EXISTS usage_quota_hourly_windows (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  window_hours INTEGER NOT NULL,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours)
)`,
}

// sqliteDirectInputCandidatesQuery 语义同 directInputCandidatesQuery：无抑制
// 时保持冻结 SQL；有抑制时在最前插入参数化 suppressions VALUES CTE 并在
// LIMIT 之前反连接（坏候选不得消耗有界候选窗口）。modernc 驱动按 $N 数字
// 绑定（与出现顺序无关，见 conn.go bind），抑制参数固定从 $6 起编号。
func sqliteDirectInputCandidatesQuery(suppressions []DirectInputSuppression) (string, []any) {
	if len(suppressions) == 0 {
		return sqliteDirectInputCandidatesSQL, nil
	}
	values := make([]string, 0, len(suppressions))
	args := make([]any, 0, len(suppressions)*5)
	for index, suppression := range suppressions {
		base := 6 + index*5
		values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base, base+1, base+2, base+3, base+4))
		args = append(args, suppression.AccountID, suppression.InputVersion, suppression.ConfigRevision, suppression.DispatchRevision, sqliteDirectTimestamp(suppression.NextDueAt))
	}
	cte := "\nWITH suppressions(account_id, input_version, config_revision, dispatch_revision, next_due_at) AS (VALUES " + strings.Join(values, ",") + "),"
	clause := "\n  AND NOT EXISTS (SELECT 1 FROM suppressions WHERE suppressions.account_id = a.id AND suppressions.input_version = iv.current_version AND suppressions.config_revision = a.config_revision AND suppressions.dispatch_revision = a.dispatch_revision AND suppressions.next_due_at > $1)"
	marker := "\n-- Keep activation work first"
	// 首个 WITH 关键字由 suppressions CTE 承担，原 ranked CTE 变为链式成员。
	query := strings.Replace(sqliteDirectInputCandidatesSQL, "\nWITH ranked_group_bindings", cte+"\nranked_group_bindings", 1)
	query = strings.Replace(query, marker, clause+marker, 1)
	return query, args
}

// sqliteDirectInputCandidatesSQL 以 directInputCandidatesSQL 为唯一语义基准，
// 仅做方言翻译：
//   - 去 juhe_business./juhe_stats. 前缀；时间参数绑定固定毫秒文本。
//   - binding/mapping LATERAL → 窗口 CTE（先例 gateway list.go 与 tablemonitor
//     store.go）。binding 用两层分区还原 PG LATERAL 的恒定单行语义：第一层
//     ranked_group_bindings 按 (account, system_account, auth) 取各授权分区
//     顶行（授权绑定臂：auth=X 账户取 X 分区内顶行）；第二层
//     selected_group_bindings 对 rank=1 集合按 (account, system_account) 再取
//     全局顶行（NULL 授权臂——全局顶必属其自身 auth 分区的 rank=1 集合），
//     外层 join 二选一，杜绝 NULL 授权账户在多 auth 分区下 join 出多行的
//     漂移。授权匹配挪到 join 条件避免 NULL 授权行匹配不上 partition；
//     mapping 的 source_endpoint_family 属于唯一键的一部分，进入 partition
//     才能保持「家族内取顶行」的 LATERAL 语义。
//   - `$3::boolean` → int 0/1；`IS DISTINCT FROM` → `IS NOT`；窗口排序与
//     NULLS FIRST 原样保留（内核 3.53.4 支持窗口函数与 NULLS FIRST）。
const sqliteDirectInputCandidatesSQL = `
WITH ranked_group_bindings AS (
  SELECT ga.account_id, ga.system_account_id, ga.group_id, ga.account_authorization_id, ga.updated_at,
    ROW_NUMBER() OVER (
      PARTITION BY ga.account_id, ga.system_account_id, ga.account_authorization_id
      ORDER BY ga.updated_at DESC, ga.group_id ASC, ga.account_id ASC
    ) AS binding_rank
  FROM group_accounts ga
  WHERE ga.enabled = 1
),
selected_group_bindings AS (
  SELECT account_id, system_account_id, account_authorization_id, group_id,
    ROW_NUMBER() OVER (
      PARTITION BY account_id, system_account_id
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
    ) AS global_rank
  FROM ranked_group_bindings
  WHERE binding_rank = 1
),
ranked_model_mappings AS (
  SELECT mm.account_id, mm.provider_code, mm.source_model, mm.source_endpoint_family, mm.upstream_model, mm.upstream_endpoint_family,
    ROW_NUMBER() OVER (
      PARTITION BY mm.account_id, mm.provider_code, mm.source_model, mm.source_endpoint_family
      ORDER BY mm.updated_at DESC, mm.source_model ASC
    ) AS mapping_rank
  FROM account_model_mappings mm
  WHERE mm.enabled = 1
    AND (mm.upstream_model <> mm.source_model OR mm.upstream_endpoint_family <> mm.source_endpoint_family)
)
SELECT
  a.id, iv.current_version, a.config_revision, a.dispatch_revision, a.provider_code, a.provider_protocol_profile_id, a.protocol_code, a.protocol_version, a.type, a.client_compatibility, a.status, a.schedulable,
  a.health_check_endpoint_mode, a.health_check_model, mapping.upstream_model, mapping.upstream_endpoint_family, a.credentials_encrypted, a.account_expires_at, a.cooldown_until, a.temporary_unavailable_continuous_probe_enabled,
  a.cooldown_retest_observation_started_at, a.cooldown_retest_generation,
  a.system_account_id,
  ra.id, ra.status, ra.expires_at, ra.limits_json, ra.resource_id, ra.resource_owner_system_account_id, ra.effective_source_team_id,
  source.id, source.config_revision, source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, source.type, source.client_compatibility, source.status, source.schedulable,
  source.account_expires_at, source.cooldown_until, source.last_error_code, source.credentials_encrypted,
  binding.group_id, binding.account_authorization_id,
  proxy.id, proxy.enabled, proxy.type, proxy.host, proxy.port, proxy.username, proxy.password_encrypted
FROM accounts a
JOIN account_health_jobs_input_versions iv ON iv.account_id = a.id
LEFT JOIN resource_authorizations ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN accounts source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
LEFT JOIN selected_group_bindings binding
  ON binding.account_id = a.id
  AND binding.system_account_id = a.system_account_id
  AND (
    (a.authorization_instance_authorization_id IS NOT NULL AND binding.account_authorization_id = a.authorization_instance_authorization_id)
    OR
    (a.authorization_instance_authorization_id IS NULL AND binding.global_rank = 1)
  )
LEFT JOIN proxy_profiles proxy ON proxy.id = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.proxy_profile_id ELSE source.proxy_profile_id END
LEFT JOIN ranked_model_mappings mapping
  ON mapping.account_id = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE source.id END
  AND mapping.provider_code = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.provider_code ELSE source.provider_code END
  AND mapping.source_model = a.health_check_model
  AND mapping.source_endpoint_family = CASE
    WHEN a.health_check_endpoint_mode IN ('chat_json', 'chat_sse') THEN 'chat_completions'
    WHEN a.health_check_endpoint_mode IN ('responses_json', 'responses_sse') THEN 'responses'
    WHEN a.health_check_endpoint_mode IN ('messages_json', 'messages_sse') THEN 'messages'
    WHEN a.health_check_endpoint_mode = 'generate_content_json' THEN 'generate_content'
    WHEN a.health_check_endpoint_mode = 'generate_content_sse' THEN 'stream_generate_content'
    ELSE 'interactions'
  END
  AND mapping.mapping_rank = 1
WHERE a.deleted_at IS NULL
  AND a.provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid')
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND (a.provider_protocol_profile_id <> 'profile_hybrid_openai_chat_v1' OR mapping.upstream_model IS NOT NULL)
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'images_json', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('active', 'pending_test', 'temporary_unavailable', 'rate_limited')
  -- Cooldown recovery is the path that restores a temporarily unavailable
  -- account. Legacy rows can retain schedulable=0, so do not let that stale
  -- scheduling bit permanently hide an otherwise due recovery candidate.
  AND (a.status IN ('pending_test', 'temporary_unavailable', 'rate_limited') OR a.schedulable = 1)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $1)
  AND (a.cooldown_until IS NULL OR a.cooldown_until <= $1)
  AND binding.group_id IS NOT NULL
  AND (a.authorization_instance_authorization_id IS NULL OR (
    ra.id IS NOT NULL AND ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $1)
    AND ra.resource_type = 'account' AND ra.resource_id = source.id
    AND ra.resource_owner_system_account_id = source.system_account_id AND ra.grantee_system_account_id = a.system_account_id
    AND source.provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid') AND source.type IN ('api_key', 'oauth', 'google_oauth') AND source.status = 'active' AND source.schedulable = 1
    AND source.deleted_at IS NULL AND source.last_error_code IS NOT 'account_expired'
    AND (source.account_expires_at IS NULL OR source.account_expires_at > $1)
    AND (source.cooldown_until IS NULL OR source.cooldown_until <= $1)
  ))
  -- Image generation is too expensive for a healthy periodic check. Keep
  -- image accounts eligible for activation, cooldown recovery, and explicit
  -- account loads (ignoreSchedule=$3), but never schedule an active image
  -- account merely because next_health_check_at is due.
  AND ($3 OR a.status <> 'active' OR a.health_check_endpoint_mode <> 'images_json')
  AND ($3 OR a.status IN ('temporary_unavailable', 'rate_limited') OR a.next_health_check_at IS NULL OR a.next_health_check_at <= $1 OR (a.status = 'pending_test' AND a.last_health_check_at IS NULL))
  AND ($4 = '' OR a.id = $4)
-- Keep activation work first, then interleave due cooldown recovery and
-- periodic active checks. A plain status tier would let a large cooldown (or
-- active) backlog starve the other class under LIMIT; per-class row numbers
-- give each class a slot while still filling from the other class when one is
-- exhausted. Do not order by updated_at: outcome projection changes it.
ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END,
  CASE WHEN a.status = 'pending_test' THEN
    ROW_NUMBER() OVER (
      PARTITION BY CASE WHEN a.status = 'pending_test' THEN 0 WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 1 ELSE 2 END
      ORDER BY a.next_health_check_at ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)
  ELSE
    ROW_NUMBER() OVER (
      PARTITION BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END
      ORDER BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)
  END,
  CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END,
  CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST,
  a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC
LIMIT $2 OFFSET $5`

// sqliteAuthorizationQuotaEligible 与 authorizationQuotaEligible 保持同步：
// 唯一差异是 grant 查询走 business 事务（配额花费走 stats 事务）、时间参数
// 固定毫秒文本与错误文案前缀；「自身 + team 作用域」额度判定语义不得分叉。
func sqliteAuthorizationQuotaEligible(ctx context.Context, btx *sql.Tx, stx *sql.Tx, candidate directCandidate, now time.Time, location *time.Location) (bool, error) {
	if candidate.authorization == nil {
		return true, nil
	}
	limits, err := ParseDirectQuotaLimits(candidate.authorizationLimits)
	if err != nil {
		return false, err
	}
	costs, err := sqliteLoadDirectQuotaCosts(ctx, stx, candidate.systemAccount, "account_authorization", candidate.authorization.ID, limits, now, location)
	if err != nil {
		return false, err
	}
	if DirectQuotaExceeded(limits, costs) {
		return false, nil
	}
	if strings.TrimSpace(candidate.authorizationTeam) == "" {
		return true, nil
	}
	var grantLimitsRaw sql.NullString
	err = btx.QueryRowContext(ctx, `
SELECT limits_json FROM resource_authorization_grants
WHERE resource_type = 'account' AND resource_id = $1 AND resource_owner_system_account_id = $2
  AND grantee_type = 'team' AND grantee_team_id = $3 AND status = 'active'
  AND (expires_at IS NULL OR expires_at > $4)
ORDER BY updated_at DESC LIMIT 1`, candidate.authorizationResourceID, candidate.authorizationOwner, candidate.authorizationTeam, sqliteDirectTimestamp(now)).Scan(&grantLimitsRaw)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取 SQLite direct input authorization team grant 失败: %w", err)
	}
	grantLimits, err := ParseDirectQuotaLimits(grantLimitsRaw.String)
	if err != nil {
		return false, err
	}
	teamCosts, err := sqliteLoadDirectQuotaCosts(ctx, stx, candidate.systemAccount, "account_authorization_team", candidate.authorization.ID+":"+candidate.authorizationTeam, grantLimits, now, location)
	if err != nil {
		return false, err
	}
	return !DirectQuotaExceeded(grantLimits, teamCosts), nil
}

// sqliteLoadDirectQuotaCosts 与 loadDirectQuotaCosts 保持同步：唯一差异是
// stats 表名不带 schema 前缀、错误文案前缀；时间窗口键（日/周/月/小时窗）
// 生成逻辑复用同一 location 语义。
func sqliteLoadDirectQuotaCosts(ctx context.Context, stx *sql.Tx, systemAccount, scopeType, scopeID string, limits DirectQuotaLimits, now time.Time, location *time.Location) (DirectQuotaCosts, error) {
	local := now.In(location)
	dateKey := local.Format("2006-01-02")
	monthKey := local.Format("2006-01")
	weekday := int(local.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekKey := local.AddDate(0, 0, 1-weekday).Format("2006-01-02")
	result := DirectQuotaCosts{}
	if err := stx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM usage_stats_totals WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3`, systemAccount, scopeType, scopeID).Scan(&result.Total); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 SQLite direct input total quota cost 失败: %w", err)
	}
	if err := stx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM usage_stats_daily WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_date=$4`, systemAccount, scopeType, scopeID, dateKey).Scan(&result.Daily); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 SQLite direct input daily quota cost 失败: %w", err)
	}
	if err := stx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM usage_stats_weekly WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_week=$4`, systemAccount, scopeType, scopeID, weekKey).Scan(&result.Weekly); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 SQLite direct input weekly quota cost 失败: %w", err)
	}
	if err := stx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM usage_stats_monthly WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_month=$4`, systemAccount, scopeType, scopeID, monthKey).Scan(&result.Monthly); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 SQLite direct input monthly quota cost 失败: %w", err)
	}
	if limits.Hourly != nil && limits.Hourly.Enabled {
		if err := stx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM usage_quota_hourly_windows WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND window_hours=$4`, systemAccount, scopeType, scopeID, limits.Hourly.Hours).Scan(&result.Hourly); err != nil && err != sql.ErrNoRows {
			return DirectQuotaCosts{}, fmt.Errorf("读取 SQLite direct input hourly quota cost 失败: %w", err)
		}
	}
	return result, nil
}
