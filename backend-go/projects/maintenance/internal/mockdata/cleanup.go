package mockdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// 清理标识：与 docs/functions/Mockdata造数设计.md「清理策略」一致。造数写出的
// 每一行都必须至少在一个可检索列上带这三个前缀之一，清理才能幂等收敛。
//
//   - CleanupNamePrefix：业务名称（分组、账户、API Key、公告、策略……）
//   - CleanupIDPrefix：统计数据集域 ID 与配套系统用户名
//   - CleanupTracePrefix：使用记录等记录类数据的 trace
//
// 导出这些常量是为了让域实现者与覆盖断言引用同一份定义，避免各域自己拼字符串
// 拼错后清理静默漏行。
const (
	CleanupNamePrefix  = "造数-"
	CleanupIDPrefix    = "mockdata_"
	CleanupTracePrefix = "mockdata-"
)

// cleanupMarkers 是清理扫描使用的全部前缀。
var cleanupMarkers = []string{CleanupNamePrefix, CleanupIDPrefix, CleanupTracePrefix}

// cleanupRule 是一条显式清理语句，处理「通用标识扫描覆盖不到」的场景：
//
//   - 自引用外键要求两段式删除（accounts 的授权实例列先删实例行，一次
//     DELETE 内的行序不可控，PG 的立即外键检查会撞实例列）；
//   - 登记行不带标识、按业务语义回收（usage_record_shards 按「分片内已无
//     条目」删除）。
type cleanupRule struct {
	// Query 是完整 DELETE 语句（$n 占位符），Args 给出实参。
	Query string
	Args  []any
}

// cleanupRules 显式清理规则（键是全限定表名；执行顺序由 cleanupOrder 的外键
// 拓扑决定，同表的多条规则按切片序执行）。
//
// chat / codex / 模型检测域（chat_codex_model_check）的 maintenance schema 表
// 不需要显式规则：chat 家族的子表（chat_asset_references / chat_image_generations
// 对 chat_assets 的复合外键、chat_messages / chat_context_* 对会话的外键）全部
// 在 information_schema 的外键图里，拓扑序保证引用行先于资产行删除；资产文件按
// 「资产 ID + 内容摘要」确定性派生存储键，重跑时同名覆写、无删除序问题。属主
// schema（juhe_j3b / juhe_jobs）不参与本拓扑——它们连通用扫描都不经过，由域在
// 每次 seed 事务里按清理标识自清（domain_chat_codex_modelcheck.go）。
func cleanupRules() map[string][]cleanupRule {
	id := CleanupIDPrefix + "%"
	name := CleanupNamePrefix + "%"
	trace := CleanupTracePrefix + "%"
	return map[string][]cleanupRule{
		qualified(SchemaBusiness, "accounts"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaBusiness, "accounts")) +
					" WHERE authorization_instance_source_account_id IS NOT NULL" +
					" AND (id LIKE $1 OR name LIKE $2)",
				Args: []any{id, name},
			},
		},
		// 可观测域属主哨兵表（juhe_dataset 内，gateway/jobs 运行时建表）：本域
		// 造数的写入面，按 id / trace 直属标识先删——显式规则不依赖通用扫描的
		// 列词表，属主表后续加列也不影响清理收敛；规则执行后通用扫描仍兜底
		// （account_id / api_key_id 等引用列命中的残留行）。audit_payload_blobs
		// 例外：blob 行是内容寻址（id / storage_key 由 sha256 派生、不带清理
		// 标识），按唯一键幂等 upsert 复用，不做删除。
		qualified(SchemaDataset, "audit_logs"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaDataset, "audit_logs")) +
					" WHERE id LIKE $1 OR trace_id LIKE $2",
				Args: []any{id, trace},
			},
		},
		qualified(SchemaDataset, "operation_logs"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaDataset, "operation_logs")) +
					" WHERE id LIKE $1 OR trace_id LIKE $2",
				Args: []any{id, trace},
			},
		},
		qualified(SchemaDataset, "runtime_logs"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaDataset, "runtime_logs")) +
					" WHERE id LIKE $1 OR trace_id LIKE $2",
				Args: []any{id, trace},
			},
		},
		// usage catalog 登记行不带任何造数标识（shard_key 是日期+分片号），
		// 按「分片内已无任何条目」回收；entries / account / api_key 关联表对
		// shards 是 ON DELETE CASCADE，随登记行级联清理。
		qualified(SchemaUsage, "usage_record_shards"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaUsage, "usage_record_shards")) +
					" WHERE shard_key NOT IN (SELECT DISTINCT shard_key FROM " +
					quoteQualified(qualified(SchemaUsage, "usage_record_shard_entries")) + ")",
			},
		},
		// stats 域脏标记（stats 域写入面）：按「持有 mock 账户归属的系统账户 +
		// global 作用域」删除。行键是系统账户 ID（seed admin 的 UUID 与 'global'
		// 都不带造数标识，通用扫描扫不到），用归属关系精确圈定本域写出的行。
		qualified(SchemaStats, "usage_overview_dirty_scopes"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaStats, "usage_overview_dirty_scopes")) +
					" WHERE system_account_id = 'global' OR system_account_id IN (" +
					"SELECT DISTINCT system_account_id FROM " +
					quoteQualified(qualified(SchemaBusiness, "accounts")) + " WHERE id LIKE $1)",
				Args: []any{id},
			},
		},
		qualified(SchemaStats, "ai_performance_summary_dirty_system_accounts"): {
			{
				Query: "DELETE FROM " + quoteQualified(qualified(SchemaStats, "ai_performance_summary_dirty_system_accounts")) +
					" WHERE system_account_id IN (SELECT DISTINCT system_account_id FROM " +
					quoteQualified(qualified(SchemaBusiness, "accounts")) + " WHERE id LIKE $1)",
				Args: []any{id},
			},
		},
	}
}

// cleanupAggregateResetTables 列出 juhe_stats 中由 jobs 聚合 / 窗口任务从使用
// 记录重建的全部派生表。造数在删除上一批使用记录后必须整表重置它们：游标型
// 聚合无法回退被删明细的历史贡献，mockdata 也不写清理扣减账；不重置就改
// --days/--daily-requests 重跑，派生数值会与明细永久漂移。重置后由常驻 jobs
// 或后续波次的一次性重建从明细全量重算，口径与真实写入路径一致。开发数据一律
// 是 mock 数据，整表重置不区分造数/非造数行是安全且确定性的选择。
var cleanupAggregateResetTables = []string{
	// 增量累加族（游标消费 usage_records 镜像）。
	"usage_stats_totals",
	"usage_stats_minute", "usage_stats_hourly", "usage_stats_daily", "usage_stats_weekly", "usage_stats_monthly",
	"usage_model_minute", "usage_model_hourly", "usage_model_daily", "usage_model_weekly", "usage_model_monthly",
	"usage_error_minute", "usage_error_hourly", "usage_error_daily", "usage_error_weekly", "usage_error_monthly",
	"usage_latency_minute", "usage_latency_hourly", "usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly",
	"authorization_team_usage_summary_daily", "authorization_user_usage_summary_daily",
	"account_quality_minute_stats", "account_quality_scores",
	"account_health_hourly",
	"client_ip_registry", "client_ip_stats_daily", "client_ip_account_stats_daily",
	// IP 自定义范围窗口族与脏队列（窗口随明细水位重算，脏队列由聚合消费）：
	// 不重置就会在改 --days 重跑后残留旧明细贡献。
	"client_ip_usage_range_windows", "client_ip_account_usage_range_windows",
	"client_ip_range_window_dirty_ips", "client_ip_account_range_window_dirty_ips",
	// 窗口 / 快照族（按源表水位全量重算，重置只为让「重跑后立即一致」）。
	"usage_rank_snapshots",
	"usage_overview_summary_windows", "usage_overview_trend_windows",
	"usage_model_rank_windows", "usage_error_rank_windows",
	"ai_performance_summary_windows",
	"usage_quota_hourly_windows", "usage_scope_range_windows",
	"authorization_team_usage_range_windows", "authorization_user_usage_range_windows",
	"group_account_stats",
	// 队列与账本：脏队列由域重写；扣减账指向的桶已被重置，残留会让 jobs
	// 清理重试扣减新桶。
	"usage_overview_dirty_scopes", "ai_performance_summary_dirty_system_accounts", "usage_quota_hourly_window_dirty_scopes",
	"usage_record_cleanup_deductions",
	// 游标必须与桶一并清零：只清桶不清游标会让聚合器从旧游标继续，重置前已
	// 消费的明细不会回填；只清游标不清桶则会把全部明细重复累加一遍。
	"stats_job_state",
}

// cleanupAll 按「外键拓扑序：显式规则 → 通用标识扫描」清理全部 maintenance
// schema，再整表重置 stats 派生面，返回「schema.table → 删除行数」。对空库、
// 缺表都是安全 no-op；重复执行不残留旧样本。
func cleanupAll(ctx context.Context, e *env, logger *slog.Logger) (deleted map[string]int, skipped []string, err error) {
	deleted = map[string]int{}
	if err := loadSweepColumnCache(ctx, e); err != nil {
		return deleted, skipped, err
	}
	order, err := cleanupOrder(ctx, e.db)
	if err != nil {
		return deleted, skipped, fmt.Errorf("计算清理顺序: %w", err)
	}
	rules := cleanupRules()
	// deferredTables 记录 FK 冲突（23503）的表：互引环（accounts ↔
	// account_connections）在单轮里可能撞立即外键检查，收集后重试一轮。
	var deferredTables []string
	for _, schemaTable := range order {
		if deferTable, ruleErr := cleanupTable(ctx, e, schemaTable, rules[schemaTable], deleted, &skipped); ruleErr != nil {
			return deleted, skipped, ruleErr
		} else if deferTable {
			deferredTables = append(deferredTables, schemaTable)
		}
	}
	// 重试轮：环成员的引用方此时已删空。
	for _, schemaTable := range deferredTables {
		if deferAgain, ruleErr := cleanupTable(ctx, e, schemaTable, rules[schemaTable], deleted, &skipped); ruleErr != nil {
			return deleted, skipped, ruleErr
		} else if deferAgain {
			return deleted, skipped, fmt.Errorf("清理 %s: 外键冲突在重试轮仍未收敛", schemaTable)
		}
	}
	resetDeleted, resetSkipped, err := resetAggregationState(ctx, e)
	if err != nil {
		return deleted, skipped, err
	}
	for table, rows := range resetDeleted {
		if rows > 0 {
			deleted[table] += rows
		}
	}
	skipped = append(skipped, resetSkipped...)
	sortStrings(skipped)
	if logger != nil {
		for table, rows := range deleted {
			if rows > 0 {
				logger.Info("mockdata 清理", "table", table, "rows", rows)
			}
		}
	}
	return deleted, skipped, nil
}

// cleanupTable 清理单张表：先执行显式规则（如有），再执行通用标识扫描。
// 表不存在按 skipped 记录（指针语义：调用方能看到追加）；FK 冲突返回
// deferTable=true 交给重试轮。
func cleanupTable(ctx context.Context, e *env, schemaTable string, rules []cleanupRule, deleted map[string]int, skipped *[]string) (deferTable bool, err error) {
	schema, table, err := splitQualified(schemaTable)
	if err != nil {
		return false, err
	}
	exists, err := tableExists(ctx, e.db, schema, table)
	if err != nil {
		return false, fmt.Errorf("探测 %s: %w", schemaTable, err)
	}
	if !exists {
		*skipped = append(*skipped, schemaTable+"（表不存在）")
		return false, nil
	}
	for _, rule := range rules {
		count, ruleErr := execCleanupDelete(ctx, e, schemaTable, rule.Query, rule.Args)
		if isForeignKeyViolation(ruleErr) {
			return true, nil
		}
		if ruleErr != nil {
			return false, fmt.Errorf("清理 %s: %w", schemaTable, ruleErr)
		}
		if count > 0 {
			deleted[schemaTable] += count
		}
	}
	query := sweepDeleteQuery(schemaTable)
	if query == "" {
		return false, nil
	}
	count, sweepErr := execCleanupDelete(ctx, e, schemaTable, query, nil)
	if isForeignKeyViolation(sweepErr) {
		return true, nil
	}
	if sweepErr != nil {
		return false, fmt.Errorf("通用清理 %s: %w", schemaTable, sweepErr)
	}
	if count > 0 {
		deleted[schemaTable] += count
	}
	return false, nil
}

// execCleanupDelete 执行一条 DELETE 并返回影响行数。
func execCleanupDelete(ctx context.Context, e *env, schemaTable, query string, args []any) (int, error) {
	result, err := e.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 %s 删除行数: %w", schemaTable, err)
	}
	return int(rows), nil
}

// isForeignKeyViolation 判定错误是否为 PG 外键冲突（23503）。
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// sweepExactColumns 是通用标识扫描参与的列名（标识列本体）。
var sweepExactColumns = map[string]bool{
	"id": true, "name": true, "title": true, "title_norm": true, "username": true,
	"display_name": true, "event_key": true, "remark": true, "trace_id": true,
	"run_id": true, "lease_key": true, "owner_id": true, "job_name": true,
	"model": true, "key": true, "label": true, "storage_key": true,
	"source_name": true, "operation_key": true, "resource_name": true,
	"compact_id": true, "session_id": true, "response_id": true, "item_key": true,
	"requested_model": true, "mapped_upstream_model": true,
}

// sweepColumnSuffixes 是标识列的命名后缀（外键与业务键）。
var sweepColumnSuffixes = []string{"_id", "_key", "_name"}

// sweepTextTypes 是参与扫描的文本列类型（information_schema.data_type 词表）。
var sweepTextTypes = map[string]bool{
	"text": true, "character varying": true, "character": true, "citext": true,
}

// sweepColumnCache 是「schema.table → 列清单」的运行级缓存；cleanupAll 入口
// 统一装载（一次 information_schema 查询覆盖全部 maintenance schema 的文本
// 列），避免每表一查。
var sweepColumnCache = map[string][]sweepColumnInfo{}

// sweepColumnInfo 是参与扫描判定所需的一列元数据。
type sweepColumnInfo struct {
	name     string
	dataType string
}

// loadSweepColumnCache 一次查询装载全部 maintenance schema 的列清单。
func loadSweepColumnCache(ctx context.Context, e *env) error {
	rows, err := e.db.QueryContext(ctx, `
		SELECT table_schema, table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = ANY($1)`, maintenanceSchemas)
	if err != nil {
		return fmt.Errorf("装载清理列清单: %w", err)
	}
	defer rows.Close()
	cache := map[string][]sweepColumnInfo{}
	for rows.Next() {
		var schema, table, column, dataType string
		if err := rows.Scan(&schema, &table, &column, &dataType); err != nil {
			return fmt.Errorf("装载清理列清单: %w", err)
		}
		key := qualified(schema, table)
		cache[key] = append(cache[key], sweepColumnInfo{name: column, dataType: dataType})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("装载清理列清单: %w", err)
	}
	sweepColumnCache = cache
	return nil
}

// sweepDeleteQuery 为一张表拼「任一标识列命中任一清理标识即删」的语句。没有
// 任何可扫描列时返回空串（该表跳过）。表名来自本包 schema 枚举、列名来自
// information_schema 枚举，拼进语句前按标识符引用。
func sweepDeleteQuery(schemaTable string) string {
	columns := sweepColumnCache[schemaTable]
	if len(columns) == 0 {
		return ""
	}
	var conditions []string
	for _, column := range columns {
		if !sweepColumn(column) {
			continue
		}
		quoted := quotePGIdentifier(column.name)
		for _, marker := range cleanupMarkers {
			conditions = append(conditions, quoted+" LIKE '"+marker+"%'")
		}
	}
	if len(conditions) == 0 {
		return ""
	}
	return "DELETE FROM " + quoteQualified(schemaTable) + " WHERE " + strings.Join(conditions, " OR ")
}

// sweepColumn 判断一列是否参与通用标识扫描。
func sweepColumn(column sweepColumnInfo) bool {
	if !sweepTextTypes[strings.ToLower(column.dataType)] {
		return false
	}
	lower := strings.ToLower(column.name)
	if sweepExactColumns[lower] {
		return true
	}
	for _, suffix := range sweepColumnSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// resetAggregationState 整表清空 stats 派生面与游标（cleanupAggregateResetTables）。
// 表不存在按 skipped 记录。
func resetAggregationState(ctx context.Context, e *env) (map[string]int, []string, error) {
	deleted := map[string]int{}
	var skipped []string
	for _, table := range cleanupAggregateResetTables {
		schemaTable := qualified(SchemaStats, table)
		exists, err := tableExists(ctx, e.db, SchemaStats, table)
		if err != nil {
			return deleted, skipped, fmt.Errorf("重置统计聚合状态 %s: %w", schemaTable, err)
		}
		if !exists {
			skipped = append(skipped, schemaTable+"（表不存在）")
			continue
		}
		rows, err := execCleanupDelete(ctx, e, schemaTable, "DELETE FROM "+schemaTable, nil)
		if err != nil {
			return deleted, skipped, fmt.Errorf("重置统计聚合状态 %s: %w", schemaTable, err)
		}
		if rows > 0 {
			deleted[schemaTable] += rows
		}
	}
	return deleted, skipped, nil
}
