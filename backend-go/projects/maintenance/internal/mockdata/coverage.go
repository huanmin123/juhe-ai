package mockdata

import (
	"context"
	"fmt"
	"strings"
)

// StoreCoverage 是单个存储的覆盖结果。
type StoreCoverage struct {
	Name   string          `json:"name"`
	Path   string          `json:"path"`
	Exists bool            `json:"exists"`
	Tables []TableCoverage `json:"tables"`
	Errors []string        `json:"errors,omitempty"`
}

// TableCoverage 是单表覆盖结果。AllowEmpty 为真时表可以为 0 行，原因是硬要求
// 之外的白名单（例如 owner 租约表、设计文档明确允许为空的瞬时队列表）。
type TableCoverage struct {
	Name             string `json:"name"`
	Rows             int    `json:"rows"`
	AllowEmpty       bool   `json:"allowEmpty,omitempty"`
	AllowEmptyReason string `json:"allowEmptyReason,omitempty"`
}

// CoverageReport 是 --verify-mockdata-coverage 的输出。
//
// 比骨架契约多一个 NotCovered 字段：域尚未接线时，该域的关键状态断言无法
// 评估（不是失败），但必须显式列出，否则「Ready=true」会被误读成「所有验证点
// 都过了」。
type CoverageReport struct {
	Ready  bool            `json:"ready"`
	Stores []StoreCoverage `json:"stores"`
	// Empty 是「存在但为空」且不在白名单里的表（<存储>.<表>）。
	Empty []string `json:"empty"`
	// Errors 是结构性错误：存储文件缺失、库内没有可读表、枚举失败。
	Errors []string `json:"errors"`
	// NotCovered 是本次未能评估的关键状态断言，附未评估原因。
	NotCovered []string `json:"notCovered,omitempty"`
}

// coverageAllowEmptyExactTables 是允许为空的表（键为表名）。
//
// 白名单必须给出理由，否则就是在给覆盖要求开后门：
//   - owner 租约 / 游标族：行由运行时写者持有，造数不该伪造持有者；
//   - usage_range_window_requests：docs/functions/Mockdata造数设计.md 明确
//     「未发生请求或处理完成后允许为空」，造数不为满足非空断言伪造瞬时队列；
//   - codex_context_storage_cleanup_queue：storage 清理任务消费的瞬时队列。
var coverageAllowEmptyExactTables = map[string]string{
	"background_job_leases":               "owner 租约族：由运行时任务持有，造数不伪造持有者",
	"stats_job_state":                     "owner 状态族：统计任务游标由运行时维护",
	"account_health_current_state":        "owner 状态族：账户健康当前态由探针 owner 维护",
	"usage_range_window_requests":         "设计文档明确允许为空：瞬时自定义范围请求队列",
	"codex_context_storage_cleanup_queue": "瞬时清理队列：由 storage 清理任务消费",
}

// coverageAllowEmptySuffixes 是同上规则里以族后缀表达的部分。
var coverageAllowEmptySuffixes = []string{
	"_owner_leases",
	"_key_cursors",
	"_projection_cursors",
}

// coverageAllowEmpty 判断表是否允许为空。
func coverageAllowEmpty(table string) (string, bool) {
	lower := strings.ToLower(table)
	if reason, ok := coverageAllowEmptyExactTables[lower]; ok {
		return reason, true
	}
	for _, suffix := range coverageAllowEmptySuffixes {
		if strings.HasSuffix(lower, suffix) {
			return "运行时状态族后缀 " + suffix + "：由 owner 持有", true
		}
	}
	return "", false
}

// coverageAssertion 是一条关键状态断言：Query 必须返回一个整数计数，
// 结果不小于 Min 才算满足。
type coverageAssertion struct {
	// Name 是断言名（报告用）。
	Name string
	// Domain 是断言所属造数域；该域本次未产出数据时断言不评估。
	Domain string
	// Store 是目标存储名。
	Store string
	// Query 返回单个整数。
	Query string
	// Min 是满足断言所需的最小计数。
	Min int
}

// coverageAssertions 是设计文档「验证点」里可机器判定的部分：只保留能用一个
// 计数回答的断言，页面观感类验证点不在此列。
//
// 这些断言按域归属：域未接线时它们进 NotCovered，接线后就是硬门槛。
func coverageAssertions() []coverageAssertion {
	id := CleanupIDPrefix + "%"
	name := CleanupNamePrefix + "%"
	return []coverageAssertion{
		// 业务域：账户状态、路由模式、授权状态、账户内 Key 运行态、自定义模型
		// status/scope、题库。
		{Name: "accounts.status.active", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM accounts WHERE status = 'active'", Min: 1},
		{Name: "accounts.status.unavailable", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM accounts WHERE status IN ('disabled','pending_test','error','rate_limited','temporary_unavailable','cooling')", Min: 1},
		{Name: "route_strategies.mode_distribution", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(DISTINCT mode) FROM route_strategies", Min: 3},
		{Name: "route_strategies.bindable_group", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(DISTINCT route_strategy_id) FROM route_strategy_groups", Min: 1},
		{Name: "resource_authorizations.status.active", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'active'", Min: 1},
		{Name: "resource_authorizations.status.non_active", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM resource_authorizations WHERE status <> 'active'", Min: 1},
		{Name: "account_api_key_runtime_states.status", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(DISTINCT status) FROM account_api_key_runtime_states", Min: 2},
		{Name: "custom_provider_models.scope.personal", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM custom_provider_models WHERE scope = 'personal'", Min: 1},
		{Name: "custom_provider_models.status_distribution", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(DISTINCT status) FROM custom_provider_models", Min: 2},
		{Name: "model_check_question_bank", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM model_check_question_bank", Min: 1},
		{Name: "api_keys.mock_sample", Domain: DomainBusiness, Store: StoreBusiness, Query: "SELECT COUNT(*) FROM api_keys WHERE id LIKE '" + id + "' OR name LIKE '" + name + "'", Min: 1},
		// 用量域：图片 token、模型映射命中、上游响应模型一致 / 不一致样本。
		{Name: "usage_records.image_tokens", Domain: DomainUsage, Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE input_image_tokens IS NOT NULL OR output_image_tokens IS NOT NULL", Min: 1},
		{Name: "usage_records.model_mapping_applied", Domain: DomainUsage, Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE model_mapping_applied = 1", Min: 1},
		{Name: "usage_records.upstream_response_model.match", Domain: DomainUsage, Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE upstream_model IS NOT NULL AND upstream_response_model IS NOT NULL AND upstream_model = upstream_response_model", Min: 1},
		{Name: "usage_records.upstream_response_model.mismatch", Domain: DomainUsage, Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE upstream_model IS NOT NULL AND upstream_response_model IS NOT NULL AND upstream_model <> upstream_response_model", Min: 1},
		{Name: "usage_records.traffic_source.gateway", Domain: DomainUsage, Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE traffic_source = 'gateway'", Min: 1},
		// 统计域：后台任务运行样本。
		{Name: "background_task_runs", Domain: DomainStats, Store: StoreStats, Query: "SELECT COUNT(*) FROM background_task_runs", Min: 1},
		// 可观测域：审计 / 操作 / 运行日志、公开接口日志、表空间监控。
		{Name: "audit_logs", Domain: DomainObservability, Store: StoreAuditLog, Query: "SELECT COUNT(*) FROM audit_logs", Min: 1},
		{Name: "operation_logs", Domain: DomainObservability, Store: StoreOperationLog, Query: "SELECT COUNT(*) FROM operation_logs", Min: 1},
		{Name: "runtime_logs", Domain: DomainObservability, Store: StoreRuntimeLog, Query: "SELECT COUNT(*) FROM runtime_logs", Min: 1},
		{Name: "public_api_logs", Domain: DomainObservability, Store: StoreDataset, Query: "SELECT COUNT(*) FROM public_api_logs", Min: 1},
		{Name: "table_storage_snapshots", Domain: DomainObservability, Store: StoreTableMonitor, Query: "SELECT COUNT(*) FROM table_storage_snapshots", Min: 1},
		// chat / codex / model-check 域：会话与消息、J3b 运行与 observation。
		{Name: "chat_conversations", Domain: DomainChatCodexModelCheck, Store: StoreChat, Query: "SELECT COUNT(*) FROM chat_conversations", Min: 1},
		{Name: "chat_messages", Domain: DomainChatCodexModelCheck, Store: StoreChat, Query: "SELECT COUNT(*) FROM chat_messages", Min: 1},
		{Name: "model_check_runs", Domain: DomainChatCodexModelCheck, Store: StoreModelCheck, Query: "SELECT COUNT(*) FROM model_check_runs", Min: 1},
		{Name: "model_check_observations", Domain: DomainChatCodexModelCheck, Store: StoreModelCheck, Query: "SELECT COUNT(*) FROM model_check_observations", Min: 1},
	}
}

// VerifyPathsCoverage 是覆盖校验的库级入口：按 Options 构建存储上下文
// （只读，不创建缺失文件），校验后关闭句柄。
func VerifyPathsCoverage(ctx context.Context, options Options) (CoverageReport, error) {
	if err := options.validate(); err != nil {
		return CoverageReport{}, err
	}
	e := newEnv(options, nil)
	report, err := VerifyCoverage(ctx, e)
	if closeErr := e.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return report, err
}

// VerifyCoverage 枚举全部存储的全部表并要求非空（白名单除外），再评估关键
// 状态断言。它只读不写：不会创建缺失的存储文件。
func VerifyCoverage(ctx context.Context, e *env) (CoverageReport, error) {
	report := CoverageReport{}
	for _, target := range e.stores() {
		storeCoverage := StoreCoverage{Name: target.Name, Path: target.Path}
		exists, err := e.storeExists(target)
		if err != nil {
			return report, err
		}
		storeCoverage.Exists = exists
		if !exists {
			storeCoverage.Errors = append(storeCoverage.Errors, "存储文件不存在（未 bootstrap 或数据根配错）")
			report.Errors = append(report.Errors, target.Name+": 存储文件不存在 "+target.Path)
			report.Stores = append(report.Stores, storeCoverage)
			continue
		}
		tables, err := e.tableNames(ctx, target.Name)
		if err != nil {
			return report, fmt.Errorf("枚举 %s 表清单: %w", target.Name, err)
		}
		if len(tables) == 0 {
			storeCoverage.Errors = append(storeCoverage.Errors, "库内没有可读表（schema 未 ensure？）")
			report.Errors = append(report.Errors, target.Name+": 库内没有可读表")
		}
		for _, table := range tables {
			rows, err := e.queryCount(ctx, target.Name, table)
			if err != nil {
				return report, err
			}
			entry := TableCoverage{Name: table, Rows: rows}
			if reason, allowed := coverageAllowEmpty(table); allowed {
				entry.AllowEmpty = true
				entry.AllowEmptyReason = reason
			}
			if rows == 0 && !entry.AllowEmpty {
				report.Empty = append(report.Empty, target.Name+"."+table)
			}
			storeCoverage.Tables = append(storeCoverage.Tables, entry)
		}
		report.Stores = append(report.Stores, storeCoverage)
	}
	notCovered, assertErrors := evaluateAssertions(ctx, e)
	report.NotCovered = notCovered
	report.Errors = append(report.Errors, assertErrors...)
	report.Ready = len(report.Empty) == 0 && len(report.Errors) == 0
	return report, nil
}

// storeExists 判断存储文件是否已经在磁盘上。
func (e *env) storeExists(target store) (bool, error) {
	db, err := e.openExisting(target.Name)
	if err != nil {
		return false, err
	}
	return db != nil, nil
}

// evaluateAssertions 评估关键状态断言，返回未覆盖项与失败项。
//
// 未覆盖（域未接线）：断言无法评估，进 NotCovered，不阻塞 Ready——骨架阶段
// 五个域都还没接线，把「域还没写」当成覆盖失败会让命令在可用的中间态永远失败。
// 已接线但不满足：进 Errors，直接让 Ready=false。
func evaluateAssertions(ctx context.Context, e *env) ([]string, []string) {
	var notCovered, failures []string
	for _, assertion := range coverageAssertions() {
		if !e.domainWired(assertion.Domain) {
			notCovered = append(notCovered, assertion.Name+"（域 "+assertion.Domain+" 未接线：本次运行未产出该域数据）")
			continue
		}
		count, err := grepAssertionCount(ctx, e, assertion)
		if err != nil {
			failures = append(failures, assertion.Name+": "+err.Error())
			continue
		}
		if count < assertion.Min {
			failures = append(failures, fmt.Sprintf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, count, assertion.Min))
		}
	}
	return notCovered, failures
}

// grepAssertionCount 在断言目标存储上执行计数查询。Store 以 "[" 结尾时表示
// 「该前缀下的全部分片」，统计各分片之和；分片文件缺失按 0 处理（域未接线时
// 本来就没有分片）。
func grepAssertionCount(ctx context.Context, e *env, assertion coverageAssertion) (int, error) {
	storePrefix := strings.TrimSuffix(assertion.Store, "[")
	var targets []store
	if strings.HasSuffix(assertion.Store, "[") {
		for _, item := range e.stores() {
			if strings.HasPrefix(item.Name, storePrefix+"[") {
				targets = append(targets, item)
			}
		}
	} else {
		item, err := e.store(assertion.Store)
		if err != nil {
			return 0, err
		}
		targets = append(targets, item)
	}
	total := 0
	for _, target := range targets {
		db, err := e.openExisting(target.Name)
		if err != nil {
			return 0, err
		}
		if db == nil {
			continue
		}
		exists, err := queryExistsTable(ctx, db, assertionTable(assertion.Query))
		if err != nil {
			return 0, err
		}
		if !exists {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, assertion.Query).Scan(&count); err != nil {
			return 0, fmt.Errorf("存储 %s: %w", target.Name, err)
		}
		total += count
	}
	return total, nil
}

// assertionTable 从断言查询里取出 FROM 后的表名，用于存在性检查（断言在域
// 未接线时不评估，但域接线后表一定存在；这里只在表缺失时按 0 处理而不是报错）。
func assertionTable(query string) string {
	const marker = "FROM "
	index := strings.Index(strings.ToUpper(query), marker)
	if index < 0 {
		return ""
	}
	rest := query[index+len(marker):]
	if end := strings.IndexAny(rest, " \t\r\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
