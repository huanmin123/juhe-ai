package mockdata

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// 清理标识：与 docs/functions/Mockdata造数设计.md「清理策略」一致。造数写出的
// 每一行都必须至少在一个可检索列上带这三个前缀之一，清理才能幂等收敛。
//
//   - CleanupNamePrefix：业务名称（分组、账户、API Key、公告、策略……）
//   - CleanupIDPrefix：统计数据集域 ID 与配套系统用户名
//   - CleanupTracePrefix：使用记录的 trace 前缀
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

// cleanupRule 是一条显式清理语句，用于「子表本身不带清理标识、只能靠父键定位」
// 的场景（例如 announcement_reads.announcement_id、group_accounts.group_id）。
//
// 为什么还需要显式清单而不是只靠 sweepCleanupMarkers：外键子表只持有父行
// 主键，自身没有任何标识列；不先按父键删掉它们，父行删除会留下孤儿行。
type cleanupRule struct {
	// Store 是精确存储名；ShardPrefix 非空时改为匹配该前缀下的全部已存在
	// 分片存储（codex context state 分片、usage 分片）。
	Store       string
	ShardPrefix string
	// Table 是目标表，用于存在性检查与跳过记录。
	Table string
	// Query 至少含一个 ? 占位符，Args 给出实参。
	Query string
	Args  []any
}

// cleanupRules 按「子表先删」的外键安全顺序排列。
//
// 顺序依据 Node 归档实现的清理顺序（migration-backup-1/.../mockdata/maintenance/
// cleanup.ts），并补上 Go 侧新增的业务表；任何新增域只要写出带清理标识的行，
// 就必须把「自身无标识但有外键指向被删父行」的子表补进这张表。
func cleanupRules() []cleanupRule {
	name := CleanupNamePrefix + "%"
	id := CleanupIDPrefix + "%"
	trace := CleanupTracePrefix + "%"
	model := CleanupTracePrefix + "%"
	return []cleanupRule{
		// business：先清 OAuth 客户端与公告子表。
		{Store: StoreBusiness, Table: "oauth_clients", Query: "DELETE FROM oauth_clients WHERE client_id LIKE ? OR display_name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "announcement_reads", Query: "DELETE FROM announcement_reads WHERE announcement_id IN (SELECT id FROM announcements WHERE id LIKE ? OR title LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "announcements", Query: "DELETE FROM announcements WHERE id LIKE ? OR title LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "external_integration_source_tokens", Query: "DELETE FROM external_integration_source_tokens WHERE source_ref_id IN (SELECT id FROM external_integration_sources WHERE id LIKE ? OR name LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "external_integration_sources", Query: "DELETE FROM external_integration_sources WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "response_inspection_policies", Query: "DELETE FROM response_inspection_policies WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "provider_system_default_health_check_models", Query: "DELETE FROM provider_system_default_health_check_models WHERE model LIKE ?", Args: []any{model}},
		{Store: StoreBusiness, Table: "provider_default_health_check_models", Query: "DELETE FROM provider_default_health_check_models WHERE model LIKE ? OR system_account_id LIKE ?", Args: []any{model, id}},
		{Store: StoreBusiness, Table: "custom_provider_models", Query: "DELETE FROM custom_provider_models WHERE id LIKE ? OR model LIKE ?", Args: []any{id, model}},
		// 账户测试任务族：链接表 → 会话 → 任务。
		{Store: StoreBusiness, Table: "account_test_session_tasks", Query: "DELETE FROM account_test_session_tasks WHERE task_id IN (SELECT id FROM account_test_tasks WHERE id LIKE ? OR account_name LIKE ? OR status_message LIKE ?)", Args: []any{id, name, name}},
		{Store: StoreBusiness, Table: "account_test_tasks", Query: "DELETE FROM account_test_tasks WHERE id LIKE ? OR account_name LIKE ? OR status_message LIKE ?", Args: []any{id, name, name}},
		{Store: StoreBusiness, Table: "account_test_sessions", Query: "DELETE FROM account_test_sessions WHERE id LIKE ?", Args: []any{id}},
		{Store: StoreBusiness, Table: "account_schedule_status_events", Query: "DELETE FROM account_schedule_status_events WHERE event_key LIKE ?", Args: []any{id}},
		{Store: StoreBusiness, Table: "api_key_schedule_status_events", Query: "DELETE FROM api_key_schedule_status_events WHERE event_key LIKE ?", Args: []any{id}},
		// 授权族：来源 → 授权，授权挂在分组/账户上。
		{Store: StoreBusiness, Table: "resource_authorization_sources", Query: "DELETE FROM resource_authorization_sources WHERE authorization_id IN (SELECT id FROM resource_authorizations WHERE id LIKE ? OR remark LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "group_authorization_settings", Query: "DELETE FROM group_authorization_settings WHERE authorization_id IN (SELECT id FROM resource_authorizations WHERE id LIKE ? OR remark LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "resource_authorization_grants", Query: "DELETE FROM resource_authorization_grants WHERE id LIKE ? OR remark LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "resource_authorizations", Query: "DELETE FROM resource_authorizations WHERE id LIKE ? OR remark LIKE ?", Args: []any{id, name}},
		// 路由族：API Key → 绑定 → 策略。
		{Store: StoreBusiness, Table: "api_keys", Query: "DELETE FROM api_keys WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "route_strategy_groups", Query: "DELETE FROM route_strategy_groups WHERE route_strategy_id IN (SELECT id FROM route_strategies WHERE id LIKE ? OR name LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "route_strategies", Query: "DELETE FROM route_strategies WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		// 账户/分组族：子表 → 账户 → 分组。
		{Store: StoreBusiness, Table: "group_accounts", Query: "DELETE FROM group_accounts WHERE group_id IN (SELECT id FROM groups WHERE id LIKE ? OR name LIKE ?) OR account_id IN (SELECT id FROM accounts WHERE id LIKE ? OR name LIKE ?)", Args: []any{id, name, id, name}},
		{Store: StoreBusiness, Table: "accounts", Query: "DELETE FROM accounts WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "groups", Query: "DELETE FROM groups WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		// 团队族 → 代理 → 会话 → 系统账户（配套用户最后删）。
		{Store: StoreBusiness, Table: "system_team_members", Query: "DELETE FROM system_team_members WHERE team_id IN (SELECT id FROM system_teams WHERE id LIKE ? OR name LIKE ?)", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "system_teams", Query: "DELETE FROM system_teams WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "proxy_profiles", Query: "DELETE FROM proxy_profiles WHERE id LIKE ? OR name LIKE ?", Args: []any{id, name}},
		{Store: StoreBusiness, Table: "system_sessions", Query: "DELETE FROM system_sessions WHERE id LIKE ? OR system_account_id IN (SELECT id FROM system_accounts WHERE id LIKE ? OR username LIKE ?)", Args: []any{id, id, id}},
		{Store: StoreBusiness, Table: "system_accounts", Query: "DELETE FROM system_accounts WHERE id LIKE ? OR username LIKE ?", Args: []any{id, id}},
		// codex context state 分片：压缩摘要 → 响应 → 会话。
		{ShardPrefix: StoreCodexContextShardPrefix, Table: "codex_context_compacts", Query: "DELETE FROM codex_context_compacts WHERE compact_id LIKE ? OR session_id LIKE ? OR source_response_id LIKE ? OR storage_key LIKE ?", Args: []any{id, id, id, id}},
		{ShardPrefix: StoreCodexContextShardPrefix, Table: "codex_context_responses", Query: "DELETE FROM codex_context_responses WHERE response_id LIKE ? OR session_id LIKE ? OR previous_response_id LIKE ? OR storage_key LIKE ?", Args: []any{id, id, id, id}},
		{ShardPrefix: StoreCodexContextShardPrefix, Table: "codex_context_sessions", Query: "DELETE FROM codex_context_sessions WHERE id LIKE ? OR source_response_id LIKE ? OR latest_response_id LIKE ? OR latest_compact_id LIKE ?", Args: []any{id, id, id, id}},
		// usage 分片：使用记录。
		{ShardPrefix: StoreUsageShardPrefix, Table: "usage_records", Query: "DELETE FROM usage_records WHERE id LIKE ? OR trace_id LIKE ?", Args: []any{id, trace}},
		// dataset：记录清理目标（引用账户/API Key）→ 公开接口日志。
		// 这两张表没有 id 列（主键就是 account_id / api_key_id），清理标识落在
		// 外键列与阻塞原因列上。
		{Store: StoreDataset, Table: "account_record_cleanup_targets", Query: "DELETE FROM account_record_cleanup_targets WHERE account_id LIKE ? OR system_account_id LIKE ? OR last_blocked_reason LIKE ? OR last_error_message LIKE ?", Args: []any{id, id, name, name}},
		{Store: StoreDataset, Table: "api_key_record_cleanup_targets", Query: "DELETE FROM api_key_record_cleanup_targets WHERE api_key_id LIKE ? OR system_account_id LIKE ? OR last_blocked_reason LIKE ? OR last_error_message LIKE ?", Args: []any{id, id, name, name}},
		{Store: StoreDataset, Table: "public_api_logs", Query: "DELETE FROM public_api_logs WHERE id LIKE ? OR trace_id LIKE ? OR source_name LIKE ?", Args: []any{id, trace, name}},
		// stats：策略命中 → 策略；脏队列；后台任务。
		// client_ip_policy_hits 的主键是 (ip_hash, stat_date)，没有 id 列：
		// 命中记录只能靠 policy_id 关联到造数策略。
		{Store: StoreStats, Table: "client_ip_policy_hits", Query: "DELETE FROM client_ip_policy_hits WHERE policy_id IN (SELECT id FROM client_ip_policies WHERE id LIKE ? OR reason LIKE ? OR disabled_reason LIKE ?)", Args: []any{id, name, name}},
		{Store: StoreStats, Table: "account_quality_dirty_accounts", Query: "DELETE FROM account_quality_dirty_accounts WHERE account_id LIKE ?", Args: []any{id}},
		{Store: StoreStats, Table: "client_ip_range_window_dirty_ips", Query: "DELETE FROM client_ip_range_window_dirty_ips WHERE ip_hash LIKE ?", Args: []any{id}},
		{Store: StoreStats, Table: "client_ip_account_range_window_dirty_ips", Query: "DELETE FROM client_ip_account_range_window_dirty_ips WHERE ip_hash LIKE ?", Args: []any{id}},
		{Store: StoreStats, Table: "usage_record_cleanup_deductions", Query: "DELETE FROM usage_record_cleanup_deductions WHERE usage_id LIKE ? OR record_json LIKE ?", Args: []any{id, "%" + CleanupIDPrefix + "%"}},
		{Store: StoreStats, Table: "background_job_leases", Query: "DELETE FROM background_job_leases WHERE lease_key LIKE ? OR owner_id LIKE ? OR run_id LIKE ?", Args: []any{id, id, id}},
		{Store: StoreStats, Table: "background_task_runs", Query: "DELETE FROM background_task_runs WHERE run_id LIKE ? OR lease_key LIKE ? OR owner_id LIKE ? OR params_json LIKE ? OR result_json LIKE ?", Args: []any{id, id, id, "%" + CleanupIDPrefix + "%", "%" + CleanupIDPrefix + "%"}},
		{Store: StoreStats, Table: "system_metrics_samples", Query: "DELETE FROM system_metrics_samples WHERE id LIKE ?", Args: []any{id}},
		{Store: StoreStats, Table: "process_event_loop_samples", Query: "DELETE FROM process_event_loop_samples WHERE id LIKE ?", Args: []any{id}},
		// chat：消息 → 会话（消息带 trace 前缀，会话带 ID 前缀）。
		{Store: StoreChat, Table: "chat_messages", Query: "DELETE FROM chat_messages WHERE id LIKE ? OR trace_id LIKE ? OR conversation_id LIKE ?", Args: []any{id, trace, id}},
		{Store: StoreChat, Table: "chat_conversations", Query: "DELETE FROM chat_conversations WHERE id LIKE ?", Args: []any{id}},
	}
}

// cleanupAll 按「显式规则 → 通用标识扫描」两段清理全部存储，返回
// 「存储.表 → 删除行数」。第二次运行必须只删本次新增之外的行，因此整个函数
// 对空库、缺库、缺表都必须是安全 no-op。
func cleanupAll(ctx context.Context, e *env) (map[string]int, []string, error) {
	deleted := map[string]int{}
	var skipped []string
	for _, rule := range cleanupRules() {
		targets := resolveCleanupTargets(e, rule)
		if len(targets) == 0 {
			skipped = append(skipped, rule.Store+rule.ShardPrefix+"."+rule.Table+"（存储不存在）")
			continue
		}
		for _, target := range targets {
			rows, err := deleteIfTableExists(ctx, e, target, rule.Table, rule.Query, rule.Args)
			if err != nil {
				return deleted, skipped, fmt.Errorf("清理 %s.%s: %w", target.Name, rule.Table, err)
			}
			if rows < 0 {
				skipped = append(skipped, target.Name+"."+rule.Table+"（表不存在）")
				continue
			}
			if rows > 0 {
				deleted[target.Name+"."+rule.Table] += rows
			}
		}
	}
	sweepDeleted, err := sweepCleanupMarkers(ctx, e)
	if err != nil {
		return deleted, skipped, err
	}
	for table, rows := range sweepDeleted {
		if rows > 0 {
			deleted[table] += rows
		}
	}
	sort.Strings(skipped)
	return deleted, skipped, nil
}

// resolveCleanupTargets 把规则解析为具体存储；分片前缀规则展开为本次运行前
// 已经存在于文件系统上的全部分片文件。
func resolveCleanupTargets(e *env, rule cleanupRule) []store {
	if rule.ShardPrefix != "" {
		var targets []store
		for _, item := range e.stores() {
			if strings.HasPrefix(item.Name, rule.ShardPrefix+"[") {
				targets = append(targets, item)
			}
		}
		return targets
	}
	item, err := e.store(rule.Store)
	if err != nil {
		return nil
	}
	return []store{item}
}

// deleteIfTableExists 在表存在时执行删除语句。表或存储缺失返回 -1（调用方
// 记为 skipped），不视为错误：首次造数时绝大多数域表还没有数据。
func deleteIfTableExists(ctx context.Context, e *env, target store, table, query string, args []any) (int, error) {
	db, err := e.openExisting(target.Name)
	if err != nil {
		return 0, err
	}
	if db == nil {
		return -1, nil
	}
	exists, err := queryExistsTable(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if !exists {
		return -1, nil
	}
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

// sweepCleanupMarkers 做通用标识扫描：对每个已存在存储的每张表，把「列名像
// 标识列」的 TEXT 列按三个清理标识做前缀匹配删除。
//
// 为什么需要这一层：显式规则清单只覆盖已知表；域实现者新增一张带造数标识的
// 表却忘了登记清理规则时，残留行会让下一次造数叠加旧样本（设计文档要求
// 「可重复执行」）。通用扫描按标识前缀工作，只可能命中造数自己写出的值，
// 因此在语义上是安全的兜底。
func sweepCleanupMarkers(ctx context.Context, e *env) (map[string]int, error) {
	deleted := map[string]int{}
	for _, target := range e.stores() {
		db, err := e.openExisting(target.Name)
		if err != nil {
			return deleted, err
		}
		if db == nil {
			continue
		}
		tables, err := queryTableNames(ctx, db)
		if err != nil {
			return deleted, fmt.Errorf("枚举 %s 表清单: %w", target.Name, err)
		}
		for _, table := range tables {
			columns, err := queryTableColumns(ctx, db, table)
			if err != nil {
				return deleted, err
			}
			query := sweepDeleteQuery(table, columns)
			if query == "" {
				continue
			}
			result, err := db.ExecContext(ctx, query)
			if err != nil {
				return deleted, fmt.Errorf("通用清理 %s.%s: %w", target.Name, table, err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return deleted, err
			}
			if rows > 0 {
				deleted[target.Name+"."+table] += int(rows)
			}
		}
	}
	return deleted, nil
}

// sweepExactColumns 是直接参与扫描的列名（标识列本体）。
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

// sweepColumn 判断一列是否参与通用标识扫描。
func sweepColumn(column tableColumn) bool {
	if !strings.Contains(strings.ToUpper(column.Type), "TEXT") && column.Type != "" {
		return false
	}
	lower := strings.ToLower(column.Name)
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

// sweepDeleteQuery 拼一条「任一标识列命中任一清理标识即删」的语句。没有任何
// 可扫描列时返回空串（表示该表跳过）。
func sweepDeleteQuery(table string, columns []tableColumn) string {
	var conditions []string
	for _, column := range columns {
		if !sweepColumn(column) {
			continue
		}
		for _, marker := range cleanupMarkers {
			conditions = append(conditions, column.Name+" LIKE '"+marker+"%'")
		}
	}
	if len(conditions) == 0 {
		return ""
	}
	return "DELETE FROM " + table + " WHERE " + strings.Join(conditions, " OR ")
}
