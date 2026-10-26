package mockdata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// StoreCoverage 是单个存储（schema 或属主存储）的覆盖结果。
type StoreCoverage struct {
	// Name 是存储键：maintenance schema 直接用 schema 名；属主存储用
	// owner:<逻辑名>。
	Name   string          `json:"name"`
	Schema string          `json:"schema"`
	Domain string          `json:"domain"`
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

// CoverageReport 是 --verify-mockdata-coverage 的 stdout JSON。
//
// Ready 语义：Empty 与 Errors 都为空。NotCovered（域未接线 / 属主存储缺失 /
// 断言目标表缺失）不阻塞 Ready——它显式列出「本次没有评估的验证点」，避免
// Ready=true 被误读成「所有验证点都过了」；OwnerStoresPending 同理只记录不
// 阻塞。
type CoverageReport struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
	Ready  bool   `json:"ready"`
	// Schemas 是 maintenance schema 与已存在属主存储的逐表覆盖。
	Schemas []StoreCoverage `json:"schemas"`
	// OwnerStoresPending 是缺失的属主存储（域跳过与 NotCovered 的依据）。
	OwnerStoresPending []OwnerStorePending `json:"ownerStoresPending,omitempty"`
	// Empty 是「存在但为空」且不在白名单里的全限定表（覆盖是 fail-closed 的
	// 全量门：未接线域的空表同样计入，直至对应域接线交付）。
	Empty []string `json:"empty"`
	// Errors 是结构性错误与已接线但未过的关键断言。
	Errors []string `json:"errors"`
	// NotCovered 是本次未能评估的验证点，附未评估原因。
	NotCovered []string `json:"notCovered,omitempty"`
}

// coverageAllowEmptyExactTables 是允许为空的表（键为表名，作用于全部
// maintenance schema）。
//
// 白名单必须给出理由，否则就是在给覆盖要求开后门。
var coverageAllowEmptyExactTables = map[string]string{
	"authorization_team_usage_range_windows": "派生聚合族：写入任务已消灭，聚合器不再产出",
	"authorization_user_usage_range_windows": "派生聚合族：写入任务已消灭，聚合器不再产出",
	// stats 派生聚合族（cleanupAggregateResetTables 同源）：由常驻 jobs 从使用
	// 明细重建，造数重置后、重建完成前为空是正确状态。
	"usage_stats_totals":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_stats_minute":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_stats_hourly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_stats_daily":                                 "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_stats_weekly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_stats_monthly":                               "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_minute":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_hourly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_daily":                                 "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_weekly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_monthly":                               "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_minute":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_hourly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_daily":                                 "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_weekly":                                "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_monthly":                               "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_latency_minute":                              "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_latency_hourly":                              "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_latency_daily":                               "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_latency_weekly":                              "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_latency_monthly":                             "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"authorization_team_usage_summary_daily":            "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"authorization_user_usage_summary_daily":            "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"account_quality_minute_stats":                      "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"account_quality_scores":                            "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"account_health_hourly":                             "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"client_ip_stats_daily":                             "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"client_ip_account_stats_daily":                     "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"client_ip_usage_range_windows":                     "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"client_ip_account_usage_range_windows":             "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_rank_snapshots":                              "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_overview_summary_windows":                    "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_overview_trend_windows":                      "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_model_rank_windows":                          "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_error_rank_windows":                          "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"ai_performance_summary_windows":                    "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_quota_hourly_windows":                        "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_scope_range_windows":                         "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"group_account_stats":                               "派生聚合族：由常驻 jobs 从使用明细重建，造数重置后重建完成前为空是正确状态",
	"usage_quota_hourly_window_dirty_scopes":            "stats 小时配额脏 scope 队列：被 usage-quota-hourly 聚合消费后即空",
	"client_ip_range_window_dirty_ips":                  "client IP 范围窗口脏队列：被 client-ip 聚合消费后即空",
	"client_ip_account_range_window_dirty_ips":          "client IP 账户范围窗口脏队列：被 client-ip 聚合消费后即空",
	"backfill_progress":                                 "一次性回填完成标记：success_cost_usd 历史回填由显式运维动作 --apply-success-cost-backfill 写入，造数不伪造",
	"background_job_leases":                             "owner 租约族：由运行时任务持有，造数不伪造持有者",
	"stats_job_state":                                   "owner 状态族：统计任务游标由运行时维护",
	"account_health_current_state":                      "owner 状态族：账户健康当前态由探针 owner 维护",
	"usage_range_window_requests":                       "设计文档明确允许为空：瞬时自定义范围请求队列",
	"codex_context_storage_cleanup_queue":               "瞬时清理队列：由 storage 清理任务消费",
	"account_health_outcomes":                           "属主运行态：探针结果由 jobs 健康运行时写入（空表走属主 pending 口径）",
	"audit_payload_blob_gc":                             "运行时建表：audit payload blob 回收队列由 gateway 运行时维护",
	"account_health_jobs_input_versions":                "输入版本：业务变更运行时预留快照 epoch（纯运行态）",
	"account_health_jobs_input_outbox":                  "intent outbox：输入快照由运行时发布器消费（纯运行态）",
	"account_api_key_pool_probe_cursors":                "运行时游标族：账户 Key 池探测游标由常驻进程推进",
	"account_health_projection_receipts":                "投影回执：由常驻投影写者消费，造数不伪造",
	"account_list_availability_projections":             "运行时投影族：管理列表可用性投影由 jobs circuitstore 重建",
	"account_list_availability_projection_index":        "运行时投影族：可用性投影索引由 jobs circuitstore 重建",
	"account_list_availability_projection_tags":         "运行时投影族：可用性投影标签由 jobs circuitstore 重建",
	"account_list_availability_projection_search_terms": "运行时投影族：可用性投影搜索词条由 jobs circuitstore 重建",
	"account_list_availability_runtime_overlays":        "运行时对账族：可用性并发 overlay 由常驻对账写回",
	"account_quality_dirty_accounts":                    "stats 脏账户队列：被 account-quality-refresh 消费后即空，重建完成后为空是正确状态",
	"chat_user_tool_preferences":                        "用户级工具默认绑定：列均可空（空 = 未设默认），用户未配置时为空是正确状态",
	"group_account_stats_dirty":                         "business 分组账户统计脏标记队列：被 group-account-stats-refresh 消费后即空",
	"usage_overview_dirty_scopes":                       "stats 用量概览脏 scope 队列：被 overview 聚合消费后即空",
	"ai_performance_summary_dirty_system_accounts":      "stats AI 性能汇总脏账户队列：被性能汇总聚合消费后即空",
	"api_key_record_cleanup_targets":                    "dataset 清理目标队列：被 api-key-record-cleanup-retry 消费后即空",
	"account_record_cleanup_targets":                    "dataset 清理目标队列：被 account-record-cleanup-retry 消费后即空",
	"runtime_log_file_cursors":                          "owner 游标族：文件读取进度由 jobs runtimelog 运行时维护；PG 造数无日志目录契约、不写物理日志文件，不伪造游标",
	"usage_record_cleanup_deductions":                   "stats 清理扣减账本：造数清理会整表重置它，仅当发生真实记录清理时才有行",
	"record_maintenance_jobs":                           "business 运行时队列：记录维护任务由 jobs 消费后为空",
	"account_health_probe_request_outbox":               "business 运行时 outbox：探针请求由 jobs 消费后为空",
	"account_health_direct_input_suppressions":          "运行态：直接输入抑制窗口由账户健康运行时维护",
	"proxy_latency_projection_receipts":                 "运行时回执族：由 jobs 代理延迟投影写者消费，造数不伪造",
	"model_check_question_tests":                        "运行时诊断记录：题库提交门禁测试由 gateway 运行时写入",
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
// 结果不小于 Min 才算满足。Query 里的表名必须全限定 schema。
type coverageAssertion struct {
	// Name 是断言名（报告用）。
	Name string
	// Domain 是断言所属造数域；该域本次未产出数据时断言不评估。
	Domain string
	// OwnerStore 非空表示断言依赖属主存储；属主缺失时按 NotCovered 处理。
	OwnerStore string
	// Table 是断言目标表（全限定）；表不存在时断言按 NotCovered 处理。
	Table string
	// Query 返回单个整数。
	Query string
	// Min 是满足断言所需的最小计数。
	Min int
}

// coverageAssertions 是设计文档「验证点」里可机器判定的部分：只保留能用一个
// 计数回答的断言，页面观感类验证点不在此列。这些断言按域归属：域未接线时它
// 们进 NotCovered，接线后就是硬门槛。
func coverageAssertions() []coverageAssertion {
	id := CleanupIDPrefix + "%"
	name := CleanupNamePrefix + "%"
	b := SchemaBusiness
	return []coverageAssertion{
		// 业务域：账户状态、路由模式、授权状态、账户内 Key 运行态、自定义模型
		// status/scope、题库。
		{Name: "accounts.status.active", Domain: DomainBusiness, Table: qualified(b, "accounts"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = 'active'", qualified(b, "accounts")), Min: 1},
		{Name: "accounts.status.unavailable", Domain: DomainBusiness, Table: qualified(b, "accounts"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status IN ('disabled','pending_test','error','rate_limited','temporary_unavailable','expired')", qualified(b, "accounts")), Min: 1},
		{Name: "route_strategies.mode_distribution", Domain: DomainBusiness, Table: qualified(b, "route_strategies"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT mode) FROM %s", qualified(b, "route_strategies")), Min: 3},
		{Name: "route_strategies.bindable_group", Domain: DomainBusiness, Table: qualified(b, "route_strategy_groups"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT route_strategy_id) FROM %s", qualified(b, "route_strategy_groups")), Min: 1},
		{Name: "resource_authorizations.status.active", Domain: DomainBusiness, Table: qualified(b, "resource_authorizations"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = 'active'", qualified(b, "resource_authorizations")), Min: 1},
		{Name: "resource_authorizations.status.non_active", Domain: DomainBusiness, Table: qualified(b, "resource_authorizations"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status <> 'active'", qualified(b, "resource_authorizations")), Min: 1},
		{Name: "account_api_key_runtime_states.status", Domain: DomainBusiness, Table: qualified(b, "account_api_key_runtime_states"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT status) FROM %s", qualified(b, "account_api_key_runtime_states")), Min: 2},
		{Name: "models.scope.personal", Domain: DomainBusiness, Table: qualified(b, "models"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE scope = 'personal'", qualified(b, "models")), Min: 1},
		{Name: "models.status_distribution", Domain: DomainBusiness, Table: qualified(b, "models"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT status) FROM %s", qualified(b, "models")), Min: 2},
		{Name: "model_check_question_bank", Domain: DomainBusiness, Table: qualified(b, "model_check_question_bank"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(b, "model_check_question_bank")), Min: 1},
		{Name: "api_keys.mock_sample", Domain: DomainBusiness, Table: qualified(b, "api_keys"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id LIKE '%s' OR name LIKE '%s'", qualified(b, "api_keys"), id, name), Min: 1},
		// 业务域媒体任务（媒体契约 §3.4）：kind=video 各状态样本行。
		{Name: "media_jobs.video_status_distribution", Domain: DomainBusiness, Table: qualified(b, "media_jobs"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT status) FROM %s WHERE kind = 'video'", qualified(b, "media_jobs")), Min: 4},
		// 用量域：图片 token、音频计量、模型映射命中、上游响应模型一致/不一致、
		// 网关流量（usage 域接线后成为硬门槛）。
		{Name: "usage_records.image_tokens", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE input_image_tokens IS NOT NULL OR output_image_tokens IS NOT NULL", qualified(SchemaUsage, "usage_records")), Min: 1},
		{Name: "usage_records.audio_metering", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE tts_input_chars > 0 OR audio_input_seconds > 0", qualified(SchemaUsage, "usage_records")), Min: 1},
		{Name: "usage_records.model_mapping_applied", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE model_mapping_applied = 1", qualified(SchemaUsage, "usage_records")), Min: 1},
		{Name: "usage_records.upstream_response_model.match", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE upstream_model IS NOT NULL AND upstream_response_model IS NOT NULL AND upstream_model = upstream_response_model", qualified(SchemaUsage, "usage_records")), Min: 1},
		{Name: "usage_records.upstream_response_model.mismatch", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE upstream_model IS NOT NULL AND upstream_response_model IS NOT NULL AND upstream_model <> upstream_response_model", qualified(SchemaUsage, "usage_records")), Min: 1},
		{Name: "usage_records.traffic_source.gateway", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE traffic_source = 'gateway'", qualified(SchemaUsage, "usage_records")), Min: 1},
		// 用量域 shard 登记：日分区存在、明细全部登记进 catalog、登记行的
		// file_path 是 PG 逻辑定位（与 jobs usagewriter 一致）。
		{Name: "usage_records.daily_partitions", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'juhe_usage' AND c.relkind = 'r' AND c.relispartition AND c.relname LIKE 'usage_records\\_%'", Min: 1},
		{Name: "usage_records.shard_entries_complete", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_records"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s r WHERE NOT EXISTS (SELECT 1 FROM %s e WHERE e.usage_id = r.id)", qualified(SchemaUsage, "usage_records"), qualified(SchemaUsage, "usage_record_shard_entries")), Min: 0},
		{Name: "usage_record_shards.postgres_logical_path", Domain: DomainUsage, Table: qualified(SchemaUsage, "usage_record_shards"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE file_path LIKE 'postgres:juhe_usage.usage_records:%%'", qualified(SchemaUsage, "usage_record_shards")), Min: 1},
		// 统计域：后台任务运行样本、账户用量快照 kind 词表、IP 登记样本。
		{Name: "background_task_runs", Domain: DomainStats, Table: qualified(SchemaStats, "background_task_runs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaStats, "background_task_runs")), Min: 1},
		{Name: "background_task_runs.status_distribution", Domain: DomainStats, Table: qualified(SchemaStats, "background_task_runs"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT status) FROM %s", qualified(SchemaStats, "background_task_runs")), Min: 3},
		{Name: "account_usage_snapshots.kind_distribution", Domain: DomainStats, Table: qualified(SchemaStats, "account_usage_snapshots"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT kind) FROM %s", qualified(SchemaStats, "account_usage_snapshots")), Min: 3},
		{Name: "client_ip_registry.mock_sample", Domain: DomainStats, Table: qualified(SchemaStats, "client_ip_registry"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaStats, "client_ip_registry")), Min: 1},
		// 可观测域：审计 / 操作 / 运行日志（属主存储）、公开接口日志。
		{Name: "audit_logs", Domain: DomainObservability, OwnerStore: "audit_log", Table: qualified(SchemaDataset, "audit_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "audit_logs")), Min: 1},
		{Name: "audit_logs.media_endpoints", Domain: DomainObservability, OwnerStore: "audit_log", Table: qualified(SchemaDataset, "audit_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE path IN ('/v1/audio/speech', '/v1/videos')", qualified(SchemaDataset, "audit_logs")), Min: 1},
		{Name: "audit_error_groups", Domain: DomainObservability, OwnerStore: "audit_log", Table: qualified(SchemaDataset, "audit_error_groups"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "audit_error_groups")), Min: 1},
		{Name: "audit_payload_blobs", Domain: DomainObservability, OwnerStore: "audit_log", Table: qualified(SchemaDataset, "audit_payload_blobs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "audit_payload_blobs")), Min: 1},
		{Name: "audit_payload_refs", Domain: DomainObservability, OwnerStore: "audit_log", Table: qualified(SchemaDataset, "audit_payload_refs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "audit_payload_refs")), Min: 1},
		{Name: "operation_logs", Domain: DomainObservability, OwnerStore: "operation_log", Table: qualified(SchemaDataset, "operation_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "operation_logs")), Min: 1},
		{Name: "operation_log_targets", Domain: DomainObservability, OwnerStore: "operation_log", Table: qualified(SchemaDataset, "operation_log_targets"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "operation_log_targets")), Min: 1},
		{Name: "runtime_logs", Domain: DomainObservability, OwnerStore: "runtime_log", Table: qualified(SchemaDataset, "runtime_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "runtime_logs")), Min: 1},
		{Name: "runtime_log_facet_summary", Domain: DomainObservability, OwnerStore: "runtime_log", Table: qualified(SchemaDataset, "runtime_log_facet_summary"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE bucket_key = '%s' AND total_count > 0", qualified(SchemaDataset, "runtime_log_facet_summary"), observabilityRuntimeFacetBucketKey), Min: 1},
		{Name: "runtime_log_level_facets", Domain: DomainObservability, OwnerStore: "runtime_log", Table: qualified(SchemaDataset, "runtime_log_level_facets"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE bucket_key = '%s' AND count > 0", qualified(SchemaDataset, "runtime_log_level_facets"), observabilityRuntimeFacetBucketKey), Min: 3},
		{Name: "public_api_logs", Domain: DomainObservability, Table: qualified(SchemaDataset, "public_api_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaDataset, "public_api_logs")), Min: 1},
		{Name: "public_api_logs.test_token", Domain: DomainObservability, Table: qualified(SchemaDataset, "public_api_logs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE is_test_token = 1", qualified(SchemaDataset, "public_api_logs")), Min: 1},
		// chat / codex / model-check 域：会话与消息、J3b 运行与 observation
		//（模型检测走属主存储）、账户健康（属主存储）。
		{Name: "chat_conversations", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_conversations"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaChat, "chat_conversations")), Min: 1},
		{Name: "chat_messages", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_messages"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaChat, "chat_messages")), Min: 1},
		{Name: "chat_messages.status.failed", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_messages"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = 'failed'", qualified(SchemaChat, "chat_messages")), Min: 1},
		{Name: "chat_messages.media_blocks", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_messages"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE content_blocks_json LIKE '%%output_media_task%%'", qualified(SchemaChat, "chat_messages")), Min: 1},
		{Name: "chat_assets.samples", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_assets"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE processing_status = 'ready'", qualified(SchemaChat, "chat_assets")), Min: 1},
		{Name: "chat_assets.media_samples", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_assets"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE processed_mime_type IN ('audio/wav','video/mp4')", qualified(SchemaChat, "chat_assets")), Min: 1},
		{Name: "chat_asset_references.integrity", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_asset_references"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s r JOIN %s a ON a.id = r.asset_id AND a.conversation_id = r.conversation_id", qualified(SchemaChat, "chat_asset_references"), qualified(SchemaChat, "chat_assets")), Min: 1},
		{Name: "chat_context_checkpoints", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaChat, "chat_context_checkpoints"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = 'active'", qualified(SchemaChat, "chat_context_checkpoints")), Min: 1},
		{Name: "codex_context_sessions", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaCodexContext, "codex_context_sessions"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaCodexContext, "codex_context_sessions")), Min: 1},
		{Name: "codex_context_responses", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaCodexContext, "codex_context_responses"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaCodexContext, "codex_context_responses")), Min: 1},
		{Name: "codex_context_compacts", Domain: DomainChatCodexModelCheck, Table: qualified(SchemaCodexContext, "codex_context_compacts"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaCodexContext, "codex_context_compacts")), Min: 1},
		{Name: "model_check_runs", Domain: DomainChatCodexModelCheck, OwnerStore: "model_check", Table: qualified(SchemaJ3B, "model_check_runs"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaJ3B, "model_check_runs")), Min: 1},
		{Name: "model_check_runs.status_distribution", Domain: DomainChatCodexModelCheck, OwnerStore: "model_check", Table: qualified(SchemaJ3B, "model_check_runs"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT status) FROM %s", qualified(SchemaJ3B, "model_check_runs")), Min: 4},
		{Name: "model_check_runs.profile_distribution", Domain: DomainChatCodexModelCheck, OwnerStore: "model_check", Table: qualified(SchemaJ3B, "model_check_runs"), Query: fmt.Sprintf("SELECT COUNT(DISTINCT profile) FROM %s", qualified(SchemaJ3B, "model_check_runs")), Min: 2},
		{Name: "model_check_observations", Domain: DomainChatCodexModelCheck, OwnerStore: "model_check", Table: qualified(SchemaJ3B, "model_check_observations"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaJ3B, "model_check_observations")), Min: 1},
		{Name: "model_check_items.quiz", Domain: DomainChatCodexModelCheck, OwnerStore: "model_check", Table: qualified(SchemaJ3B, "model_check_items"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE item_type = 'custom_quiz'", qualified(SchemaJ3B, "model_check_items")), Min: 1},
		{Name: "account_health_outcomes.samples", Domain: DomainChatCodexModelCheck, OwnerStore: "account_health", Table: qualified(SchemaJobs, "account_health_outcomes"), Query: fmt.Sprintf("SELECT COUNT(*) FROM %s", qualified(SchemaJobs, "account_health_outcomes")), Min: 1},
	}
}

// VerifyCoverage 执行严格只读的覆盖校验：整个评估在一个 READ ONLY 事务里
// 完成，不创建任何对象、不写任何行。域接线状态来自 Options.SummaryFile 指向
// 的摘要（无隐式回退；文件缺失时全部域按未接线处理）。
func VerifyCoverage(ctx context.Context, options Options) (CoverageReport, error) {
	if err := options.validate(); err != nil {
		return CoverageReport{}, err
	}
	e, err := newEnv(options, nil)
	if err != nil {
		return CoverageReport{}, err
	}
	defer func() { _ = e.Close() }()
	wired, err := loadWiredDomainSnapshot(options.SummaryFile)
	if err != nil {
		return CoverageReport{}, err
	}
	for _, result := range wired {
		e.recordDomainResult(result)
	}
	return verifyCoverageReadOnly(ctx, e)
}

// verifyCoverageReadOnly 在一个只读事务里完成全部枚举与断言评估。
func verifyCoverageReadOnly(ctx context.Context, e *env) (report CoverageReport, err error) {
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CoverageReport{}, fmt.Errorf("mockdata 覆盖校验只读事务: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !strings.Contains(rollbackErr.Error(), "already") && err == nil {
			err = rollbackErr
		}
	}()
	report, err = verifyCoverageTx(ctx, tx, e)
	if err != nil {
		return CoverageReport{}, err
	}
	normaliseCoverageReport(&report)
	return report, nil
}

// verifyCoverageTx 是覆盖评估的内核：枚举 maintenance schema 与属主存储、
// 逐表计数、评估关键断言。
func verifyCoverageTx(ctx context.Context, tx *sql.Tx, e *env) (CoverageReport, error) {
	report := CoverageReport{Driver: "postgres", DSN: e.options.DSN}
	pending, err := probeOwnerStores(ctx, tx)
	if err != nil {
		return report, err
	}
	report.OwnerStoresPending = pending
	pendingByStore := map[string]OwnerStorePending{}
	for _, item := range pending {
		pendingByStore[item.Store] = item
	}

	// 1. maintenance schema 逐表覆盖：空表（白名单外）一律进 Empty——覆盖是
	//    fail-closed 的全量门，未接线域的空表同样让 Ready=false（与「断言按
	//    域接线评估」不同：空表是客观事实，不随接线状态变化）。
	for _, schema := range maintenanceSchemas {
		domain := schemaDomain(schema)
		coverage := StoreCoverage{Name: schema, Schema: schema, Domain: domain, Exists: true, Tables: []TableCoverage{}}
		tables, err := schemaTables(ctx, tx, schema)
		if err != nil {
			return report, fmt.Errorf("枚举 %s: %w", schema, err)
		}
		if len(tables) == 0 {
			coverage.Errors = append(coverage.Errors, "schema 内没有可读表（schema 未 ensure？）")
			report.Errors = append(report.Errors, schema+": schema 内没有可读表")
		}
		for _, table := range tables {
			rows, err := countTable(ctx, tx, schema, table)
			if err != nil {
				return report, err
			}
			entry := TableCoverage{Name: table, Rows: rows}
			if reason, allowed := coverageAllowEmpty(table); allowed {
				entry.AllowEmpty = true
				entry.AllowEmptyReason = reason
			}
			if rows == 0 && !entry.AllowEmpty {
				report.Empty = append(report.Empty, qualified(schema, table))
			}
			coverage.Tables = append(coverage.Tables, entry)
		}
		report.Schemas = append(report.Schemas, coverage)
	}

	// 2. 属主存储：存在的枚举哨兵表（空表同样进 Empty），缺失的记 Exists=false
	//    由断言评估按「属主缺失」承接——缺失不产生 Empty 条目，因此不阻塞
	//    Ready（OwnerStoresPending 的显式契约）。
	for _, store := range ownerStores {
		if _, isPending := pendingByStore[store.Key]; isPending {
			report.Schemas = append(report.Schemas, StoreCoverage{
				Name: "owner:" + store.Key, Schema: store.Schema, Domain: store.Domain,
				Exists: false, Tables: []TableCoverage{},
			})
			continue
		}
		coverage := StoreCoverage{Name: "owner:" + store.Key, Schema: store.Schema, Domain: store.Domain, Exists: true, Tables: []TableCoverage{}}
		for _, table := range store.Tables {
			rows, err := countTable(ctx, tx, store.Schema, table)
			if err != nil {
				return report, err
			}
			entry := TableCoverage{Name: table, Rows: rows}
			if reason, allowed := coverageAllowEmpty(table); allowed {
				entry.AllowEmpty = true
				entry.AllowEmptyReason = reason
			}
			if rows == 0 && !entry.AllowEmpty {
				report.Empty = append(report.Empty, qualified(store.Schema, table))
			}
			coverage.Tables = append(coverage.Tables, entry)
		}
		report.Schemas = append(report.Schemas, coverage)
	}

	// 3. 关键状态断言。
	notCovered, failures, err := evaluateAssertions(ctx, tx, e, pendingByStore)
	if err != nil {
		return report, err
	}
	report.NotCovered = append(report.NotCovered, notCovered...)
	report.Errors = append(report.Errors, failures...)
	report.Ready = len(report.Empty) == 0 && len(report.Errors) == 0
	return report, nil
}

// countTable 返回全限定表行数。
func countTable(ctx context.Context, tx *sql.Tx, schema, table string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteQualified(qualified(schema, table))).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s.%s: %w", schema, table, err)
	}
	return count, nil
}

// evaluateAssertions 评估关键状态断言，返回未覆盖项与失败项。
//
// 未覆盖（域未接线 / 属主存储缺失 / 目标表缺失）：断言无法评估，进
// NotCovered，不阻塞 Ready。已接线但不满足：进 Errors，直接让 Ready=false。
func evaluateAssertions(ctx context.Context, tx *sql.Tx, e *env, pendingByStore map[string]OwnerStorePending) (notCovered, failures []string, err error) {
	for _, assertion := range coverageAssertions() {
		if !e.domainWired(assertion.Domain) {
			notCovered = append(notCovered, assertion.Name+"（域 "+assertion.Domain+" 未接线：本次运行未产出该域数据）")
			continue
		}
		if assertion.OwnerStore != "" {
			if _, isPending := pendingByStore[assertion.OwnerStore]; isPending {
				notCovered = append(notCovered, assertion.Name+"（属主存储 "+assertion.OwnerStore+" 缺失：属主进程或受控 bootstrap 尚未建表）")
				continue
			}
		}
		schema, table, splitErr := splitQualified(assertion.Table)
		if splitErr != nil {
			return notCovered, failures, splitErr
		}
		exists, existsErr := tableExists(ctx, tx, schema, table)
		if existsErr != nil {
			return notCovered, failures, fmt.Errorf("探测断言目标 %s: %w", assertion.Table, existsErr)
		}
		if !exists {
			notCovered = append(notCovered, assertion.Name+"（目标表 "+assertion.Table+" 不存在）")
			continue
		}
		var count int
		if queryErr := tx.QueryRowContext(ctx, assertion.Query).Scan(&count); queryErr != nil {
			failures = append(failures, assertion.Name+": "+queryErr.Error())
			continue
		}
		if count < assertion.Min {
			failures = append(failures, fmt.Sprintf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, count, assertion.Min))
		}
	}
	return notCovered, failures, nil
}

// normaliseCoverageReport 把 nil 集合补成空切片，保证 JSON 形状稳定。
func normaliseCoverageReport(report *CoverageReport) {
	if report.Schemas == nil {
		report.Schemas = []StoreCoverage{}
	}
	if report.Empty == nil {
		report.Empty = []string{}
	}
	if report.Errors == nil {
		report.Errors = []string{}
	}
	if report.NotCovered == nil {
		report.NotCovered = []string{}
	}
}
