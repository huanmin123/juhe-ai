package mockdata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// autofillTables 给「自动补全集合」里的每张表插入 1..3 行结构合法的占位行。
//
// 目的：本地联调的页面/接口只要按表读取，就不该出现「明明跑完造数却是空表」
// 的噪声；真正有业务语义的行由各造数域写，本函数只保证表结构上可读。
//
// 为什么必须运行时枚举：schema 由 maintenance 的 DDL 与 jobs/gateway 各自
// 的 ensure 共同决定，编译期清单会随任何一侧新增表而失效；sqlite_master +
// PRAGMA table_info 拿到的是这次实际打开的库的真实形状。
//
// 失败降级：单表插入失败只记原因（返回值第二项）并继续下一张表——样例数据
// 不该让整个造数失败，但失败原因必须可查，不能静默吞掉。
func autofillTables(ctx context.Context, e *env) (map[string]int, map[string]string, error) {
	inserted := map[string]int{}
	skipped := map[string]string{}
	now := e.options.clock()
	for _, target := range e.stores() {
		tables, err := e.tableNames(ctx, target.Name)
		if err != nil {
			return inserted, skipped, fmt.Errorf("枚举 %s 表清单: %w", target.Name, err)
		}
		for _, table := range tables {
			if reason, skip := autofillSkipReason(target.Name, table); skip {
				skipped[target.Name+"."+table] = reason
				continue
			}
			rows, err := autofillTable(ctx, e, target, table, now)
			if err != nil {
				skipped[target.Name+"."+table] = err.Error()
				continue
			}
			inserted[target.Name+"."+table] = rows
		}
	}
	return inserted, skipped, nil
}

// autofillTable 给单张表插入占位行，返回插入行数。
func autofillTable(ctx context.Context, e *env, target store, table string, now time.Time) (int, error) {
	db, err := e.open(target.Name)
	if err != nil {
		return 0, err
	}
	columns, err := queryTableColumns(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, fmt.Errorf("表 %s 没有可读列信息", table)
	}
	checks, err := queryCheckInValues(ctx, db, table)
	if err != nil {
		return 0, err
	}
	rowCount := autofillRowCount(table)
	for rowIndex := 0; rowIndex < rowCount; rowIndex++ {
		values := map[string]any{}
		autoIncrement := hasIntegerPrimaryKey(columns)
		for _, column := range columns {
			if autoIncrement && column.PrimaryKey > 0 {
				continue
			}
			if !column.NotNull && column.DefaultValue != nil && !isPrimaryKey(column) {
				continue
			}
			if !column.NotNull && !isPrimaryKey(column) {
				// 可空且无默认值：留空比塞占位值更安全（不会撞 CHECK/UNIQUE）。
				continue
			}
			values[column.Name] = autofillValue(table, column, checks[column.Name], rowIndex, now)
		}
		if len(values) == 0 {
			return 0, fmt.Errorf("表 %s 没有必填列，跳过以免插入无意义空行", table)
		}
		if err := e.insertMap(ctx, target.Name, table, values); err != nil {
			return 0, fmt.Errorf("插入占位行失败: %w", err)
		}
	}
	return rowCount, nil
}

// autofillRowCount 用表名的稳定哈希决定 1..3 行：同一张表每次运行行数一致，
// 让报告与断言可复现。
func autofillRowCount(table string) int {
	sum := sha256.Sum256([]byte(table))
	return 1 + int(sum[0])%3
}

func isPrimaryKey(column tableColumn) bool {
	return column.PrimaryKey > 0
}

// hasIntegerPrimaryKey 判断表是否有 INTEGER PRIMARY KEY（rowid 别名）：
// 这类主键必须省略，交给 SQLite 自增，显式给值会与既有行撞 UNIQUE。
func hasIntegerPrimaryKey(columns []tableColumn) bool {
	single := 0
	var pk tableColumn
	for _, column := range columns {
		if column.PrimaryKey > 0 {
			single++
			pk = column
		}
	}
	return single == 1 && strings.Contains(strings.ToUpper(pk.Type), "INT")
}

// autofillValue 按列名与声明类型给出一个结构合法的占位值。
func autofillValue(table string, column tableColumn, checkValues []string, rowIndex int, now time.Time) any {
	lower := strings.ToLower(column.Name)
	// CHECK IN 约束优先：能解析出允许值时取第一个，避免直接撞约束。
	if len(checkValues) > 0 {
		return checkValues[0]
	}
	upperType := strings.ToUpper(strings.TrimSpace(column.Type))
	switch {
	case lower == "id" || strings.HasSuffix(lower, "_id"):
		return autofillID(table, column.Name, rowIndex)
	case strings.HasSuffix(lower, "_at") || lower == "time" || strings.HasSuffix(lower, "_time"):
		return now.Add(-time.Duration(rowIndex) * time.Minute).UTC().Format(isoMillisLayout)
	case strings.HasSuffix(lower, "_json"):
		return "{}"
	case strings.Contains(lower, "sha256") || strings.Contains(lower, "hash") || strings.Contains(lower, "digest") || strings.Contains(lower, "hmac") || strings.Contains(lower, "fingerprint") || strings.Contains(lower, "checksum"):
		sum := sha256.Sum256([]byte(table + "." + column.Name + "." + strconv.Itoa(rowIndex)))
		return hex.EncodeToString(sum[:])
	case lower == "client_ip" || strings.HasSuffix(lower, "_ip"):
		return "10.10.0.1"
	case strings.Contains(lower, "email"):
		return CleanupIDPrefix + "autofill@example.invalid"
	case strings.Contains(lower, "url") || strings.Contains(lower, "endpoint"):
		return "https://example.invalid/" + CleanupTracePrefix + "autofill"
	}
	switch {
	case strings.Contains(upperType, "INT"):
		return 1
	case strings.Contains(upperType, "REAL") || strings.Contains(upperType, "FLOA") || strings.Contains(upperType, "DOUB"):
		return 1.0
	case strings.Contains(upperType, "BLOB"):
		return nil
	default:
		return CleanupIDPrefix + "autofill_" + table + "_" + column.Name + "_" + strconv.Itoa(rowIndex)
	}
}

// isoMillisLayout 与 Node nowIso() 的毫秒精度一致：下游按字符串比较时间。
const isoMillisLayout = "2006-01-02T15:04:05.000Z"

// autofillID 生成行内唯一的标识值，并带清理标识（下次运行的清理能删掉它）。
func autofillID(table, column string, rowIndex int) string {
	return CleanupIDPrefix + "autofill_" + table + "_" + column + "_" + strconv.Itoa(rowIndex)
}

// checkInPattern 匹配 CREATE TABLE 文本里的 `CHECK (col IN ('a','b'))`。
// PRAGMA table_info 不返回 CHECK 约束，只能回到 sqlite_master 的建表文本。
var checkInPattern = regexp.MustCompile(`(?is)CHECK\s*\(\s*["` + "`" + `\[]?(\w+)["` + "`" + `\]]?\s+IN\s*\(([^()]*)\)`)

var singleQuotedLiteral = regexp.MustCompile(`'((?:[^']|'')*)'`)

// queryCheckInValues 解析表的 CHECK IN 约束，返回「列名 → 允许值（按书写顺序）」。
// 解析不出来不是错误：调用方退回按类型取值，插入失败会作为跳过原因记录。
func queryCheckInValues(ctx context.Context, db *sql.DB, table string) (map[string][]string, error) {
	var ddl sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&ddl); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if !ddl.Valid {
		return nil, nil
	}
	values := map[string][]string{}
	for _, match := range checkInPattern.FindAllStringSubmatch(ddl.String, -1) {
		if len(match) < 3 {
			continue
		}
		column := match[1]
		if _, ok := values[column]; ok {
			continue
		}
		var allowed []string
		for _, literal := range singleQuotedLiteral.FindAllStringSubmatch(match[2], -1) {
			allowed = append(allowed, strings.ReplaceAll(literal[1], "''", "'"))
		}
		if len(allowed) > 0 {
			values[column] = allowed
		}
	}
	return values, nil
}

// autofillSkipPrefixes 是「派生聚合族」前缀：这些表的行由既有聚合器从明细
// 重建，塞占位行会污染统计口径（页面上会看到凭空多出来的聚合点）。
var autofillSkipPrefixes = []string{
	"usage_stats_",
	"usage_model_",
	"usage_error_",
	"usage_latency_",
	"account_quality_",
	"client_ip_",
	"system_metrics_",
	"process_event_loop_",
	"go_runtime_metrics_",
	"usage_overview_",
	"usage_quota_hourly_",
	"usage_scope_",
	"ai_performance_summary_",
	"model_token_integrity_",
	"model_trust_",
	"model_paired_similarity_",
	"model_identity_",
	"model_account_trust_",
}

// autofillSkipSuffixes 是同上规则里以「族后缀」表达的部分。
var autofillSkipSuffixes = []string{
	"_windows",
	"_owner_leases",
	"_key_cursors",
	"_projection_cursors",
}

// autofillSkipExactTables 是按表名精确登记的跳过项，分三类：
//
//  1. 派生聚合族里无法用前缀/后缀表达的单表（rank_snapshots、hourly 健康状态、
//     分组账户统计）。
//  2. owner 状态与租约族：这些表的行代表「当前持有者 / 游标位置」，占位行会
//     让本机后端误以为某个 owner 或某段游标有效。
//  3. 造数域会显式写入的业务表：本包五个域各自负责它们的语义，占位行会在同
//     一次运行的域写入之后盖上去，导致页面读到假数据。域实现者新增会写的表时
//     必须同步登记到这里。
var autofillSkipExactTables = map[string]string{
	// —— 派生聚合族。
	"usage_rank_snapshots":                         "派生聚合族：由既有排行重建路径写入，占位行会污染排行",
	"account_health_hourly":                        "派生聚合族：由用量聚合器重建，占位行会污染健康状态条",
	"group_account_stats":                          "派生聚合族：分组账户统计缓存，由聚合器重建",
	"authorization_team_usage_summary_daily":       "派生聚合族：授权用量统计，由聚合器重建",
	"authorization_user_usage_summary_daily":       "派生聚合族：授权用量统计，由聚合器重建",
	"authorization_team_usage_range_windows":       "派生聚合族：授权用量窗口，由聚合器重建",
	"authorization_user_usage_range_windows":       "派生聚合族：授权用量窗口，由聚合器重建",
	"model_trust_latest_dirty_accounts":            "派生队列：可信度脏账户由聚合器维护",
	"model_trust_observation_receipts":             "派生聚合族：可信度 observation 回执",
	"model_trust_window_sources":                   "派生聚合族：可信度窗口来源",
	"model_token_integrity_rounds":                 "派生聚合族：Token 完整轮次",
	"model_identity_baseline_versions":             "派生聚合族：身份基线",
	"model_identity_source_features":               "派生聚合族：身份来源特征",
	"model_token_intercept_baseline_versions":      "派生聚合族：Token 拦截基线",
	"ai_performance_summary_dirty_system_accounts": "派生队列：AI 性能摘要脏账户",
	// —— owner 状态 / 租约 / 游标族。
	"background_job_leases":               "owner 租约族：占位行会让本机后端误判任务持有者",
	"stats_job_state":                     "owner 状态族：统计任务游标，由运行时写者维护",
	"account_health_current_state":        "owner 状态族：账户健康当前态，由探针 owner 维护",
	"usage_range_window_requests":         "瞬时队列：自定义范围请求由运行时写入，设计文档明确允许为空",
	"codex_context_storage_cleanup_queue": "瞬时清理队列：由 storage 清理任务消费",
	// —— 域显式写入的业务表（business 库）。
	"system_accounts":                             "域业务表：seedBusiness 写入配套用户与 admin",
	"system_sessions":                             "域业务表：seedBusiness 写入会话样本",
	"system_teams":                                "域业务表：seedBusiness 写入团队样本",
	"system_team_members":                         "域业务表：seedBusiness 写入团队成员",
	"groups":                                      "域业务表：seedBusiness 写入分组",
	"group_accounts":                              "域业务表：seedBusiness 写入分组-账户绑定",
	"accounts":                                    "域业务表：seedBusiness 写入 AI 账户",
	"account_api_key_runtime_states":              "域业务表：seedBusiness 写入账户内 Key 运行态",
	"account_supported_models":                    "域业务表：seedBusiness 写入账户支持模型",
	"account_model_mappings":                      "域业务表：seedBusiness 写入账号模型映射",
	"account_tags":                                "域业务表：seedBusiness 写入账户标签",
	"account_tag_bindings":                        "域业务表：seedBusiness 写入标签绑定",
	"account_circuit_incidents":                   "域业务表：seedBusiness 写入熔断事件样本",
	"api_keys":                                    "域业务表：seedBusiness 写入 API Key 与明文本地 Key",
	"route_strategies":                            "域业务表：seedBusiness 写入路由策略",
	"route_strategy_groups":                       "域业务表：seedBusiness 写入策略分组绑定",
	"resource_authorizations":                     "域业务表：seedBusiness 写入授权样本",
	"resource_authorization_grants":               "域业务表：seedBusiness 写入授权授予",
	"resource_authorization_sources":              "域业务表：seedBusiness 写入授权来源",
	"group_authorization_settings":                "域业务表：seedBusiness 写入授权分组设置",
	"oauth_clients":                               "域业务表：seedBusiness 写入第三方 OAuth/OIDC Client",
	"announcements":                               "域业务表：seedBusiness 写入公告",
	"announcement_reads":                          "域业务表：seedBusiness 写入公告已读",
	"external_integration_sources":                "域业务表：seedBusiness 写入外部来源系统",
	"external_integration_source_tokens":          "域业务表：seedBusiness 写出来源 Token",
	"response_inspection_policies":                "域业务表：seedBusiness 写入响应检查策略",
	"custom_provider_models":                      "域业务表：seedBusiness 写入自定义模型目录",
	"provider_default_health_check_models":        "域业务表：seedBusiness 写入账户级默认体检模型",
	"provider_system_default_health_check_models": "域业务表：seedBusiness 写入系统级默认体检模型",
	"proxy_profiles":                              "域业务表：seedBusiness 写入代理样本",
	"model_quality_policies":                      "域业务表：seedBusiness 写入模型质量策略",
	"model_quality_schedules":                     "域业务表：seedBusiness 写入模型质量计划",
	"account_quality_enforcements":                "域业务表：seedBusiness 写入质量隔离样本",
	"model_check_question_bank":                   "域业务表：seedBusiness 写入模型检测题库",
	"account_test_tasks":                          "域业务表：seedBusiness 写入账户测试任务",
	"account_test_sessions":                       "域业务表：seedBusiness 写入账户测试会话",
	"account_test_session_tasks":                  "域业务表：seedBusiness 写入账户测试会话任务",
	"account_schedule_status_events":              "域业务表：seedBusiness 写入账户时间计划事件",
	"api_key_schedule_status_events":              "域业务表：seedBusiness 写入 Key 时间计划事件",
	"openai_compatible_files":                     "域业务表：seedBusiness 写入 OpenAI 兼容文件",
	"openai_compatible_vector_stores":             "域业务表：seedBusiness 写入向量库",
	"openai_compatible_vector_store_files":        "域业务表：seedBusiness 写入向量库文件",
	"openai_compatible_vector_store_chunks":       "域业务表：seedBusiness 写入向量库分块",
	// —— 域显式写入的业务表（usage / stats / observability / chat-codex-model-check）。
	"usage_records":               "域业务表：seedUsage 写入使用记录",
	"usage_record_shards":         "域业务表：seedUsage 写入分片登记",
	"usage_record_shard_entries":  "域业务表：seedUsage 写入分片条目",
	"usage_record_account_shards": "域业务表：seedUsage 写入账户分片索引",
	"usage_record_api_key_shards": "域业务表：seedUsage 写入 Key 分片索引",
	// usage_record_cleanup_deductions 是 jobs 清理任务的扣减台账（行语义是「该
	// 记录已从分片删除并已扣减统计」），域按裁决不伪造，见 domain_stats.go 不写清单。
	"usage_record_cleanup_deductions": "运行时台账：记录清理扣减由 jobs 清理任务结算，伪造行会让重试任务结算不存在的记录",
	"account_usage_snapshots":         "域业务表：seedStatsRaw 写入账户用量快照",
	"client_ip_registry":              "域业务表：seedStatsRaw 写入 IP 登记",
	"client_ip_policies":              "域业务表：seedStatsRaw 写入 IP 封禁策略",
	"client_ip_policy_hits":           "域业务表：seedStatsRaw 写入策略命中",
	"background_task_runs":            "域业务表：seedStatsRaw 写入后台任务运行",
	"system_metrics_samples":          "域业务表：seedStatsRaw 写入系统指标样本",
	"process_event_loop_samples":      "域业务表：seedStatsRaw 写入事件循环样本",
	"public_api_logs":                 "域业务表：seedObservability 写入公开接口日志",
	"audit_logs":                      "域业务表：seedObservability 写入审计日志",
	"operation_logs":                  "域业务表：seedObservability 写入操作日志",
	"runtime_logs":                    "域业务表：seedObservability 写入运行日志",
	"runtime_log_facet_summary":       "域业务表：seedObservability 写入运行日志分面",
	"runtime_log_level_facets":        "域业务表：seedObservability 写入日志级别分面",
	"runtime_log_event_facets":        "域业务表：seedObservability 写入日志事件分面",
	"table_storage_snapshots":         "域业务表：seedObservability 写入表空间监控快照",
	"record_maintenance_jobs":         "域业务表：seedObservability 写入记录清理任务",
	"account_record_cleanup_targets":  "域业务表：seedObservability 写入账户清理目标",
	"api_key_record_cleanup_targets":  "域业务表：seedObservability 写入 Key 清理目标",
	"chat_conversations":              "域业务表：seedChatCodexModelCheck 写入会话",
	"chat_messages":                   "域业务表：seedChatCodexModelCheck 写入消息",
	"chat_context_checkpoints":        "域业务表：seedChatCodexModelCheck 写入上下文检查点",
	"chat_context_entries":            "域业务表：seedChatCodexModelCheck 写入上下文条目",
	"chat_message_idempotency":        "域业务表：seedChatCodexModelCheck 写入幂等键",
	"chat_image_generations":          "域业务表：seedChatCodexModelCheck 写入图像生成",
	"chat_asset_references":           "域业务表：seedChatCodexModelCheck 写入资产引用",
	"chat_assets":                     "域业务表：seedChatCodexModelCheck 写入聊天资产",
	"codex_context_sessions":          "域业务表：seedChatCodexModelCheck 写入 codex 会话",
	"codex_context_responses":         "域业务表：seedChatCodexModelCheck 写入 codex 响应状态",
	"codex_context_compacts":          "域业务表：seedChatCodexModelCheck 写入 codex 摘要",
	"model_check_runs":                "域业务表：seedChatCodexModelCheck 写入 J3b 运行",
	"model_check_items":               "域业务表：seedChatCodexModelCheck 写入 J3b 检查项",
	"model_check_observations":        "域业务表：seedChatCodexModelCheck 写入受控 observation",
	"model_check_scheduler_tasks":     "域业务表：seedChatCodexModelCheck 写入 J3b 调度任务",
	"account_quality_health_hourly":   "域业务表：seedChatCodexModelCheck 写入质量健康小时",
	// 以下是 J1（账户健康）owner 运行态表，由 jobs 健康owner/业务变更运行时
	// 维护；域按裁决不伪造，登记在这里把「为什么是空的」写清楚而不是误标为域表。
	"account_health_outcomes":                  "owner 运行态：J1 探针结果由 jobs 健康owner 写入，域不伪造",
	"account_health_direct_input_suppressions": "owner 运行态：直连输入抑制由 jobs 健康owner 维护",
	"account_health_jobs_input_versions":       "owner 运行态：J1 输入版本由业务变更运行时预留快照 epoch",
	"account_health_jobs_input_outbox":         "owner 运行态：J1 输入 intent outbox 由运行时发布器消费",
	"account_health_projection_cursors":        "owner 游标族：账户健康投影游标",
	// probe_request_outbox 是 gateway 写入、jobs J1 drain 消费的交接 outbox。
	// 占位行的 source_fence 是非 JSON 文本，会让 J1 每周期 claim 整体失败、
	// owner lease 反复释放（2026-09-23 实测毒丸），绝不能补占位行。
	"account_health_probe_request_outbox": "运行时 outbox：探针请求由 gateway 写入、jobs J1 drain 消费，域不伪造",
	// —— 2026-09 收尾盘点新增（各域实现者报告后逐条核实）：
	// 域显式写入的业务表不补占位行，否则占位行会叠加在域数据之上。
	"chat_user_asset_usage":                                  "域业务表：seedChatCodexModelCheck 写入用户资产用量汇总",
	"chat_user_storage_windows":                              "域业务表：seedChatCodexModelCheck 写入用户存储窗口，同族不补占位",
	"group_account_stats_dirty":                              "派生队列：seedStatsRaw 写入分组统计脏标记，聚合器重建 group_account_stats",
	"oauth_signing_keys":                                     "域业务表：seedBusiness 写入 OIDC 签名密钥样本",
	"oauth_grants":                                           "域业务表：seedBusiness 写入 OAuth grant 样本",
	"oauth_authorization_transactions":                       "域业务表：seedBusiness 写入授权事务样本",
	"oauth_authorization_codes":                              "域业务表：seedBusiness 写入授权码样本",
	"oauth_authorization_code_oidc_contexts":                 "域业务表：seedBusiness 写入授权码 OIDC 上下文",
	"oauth_access_tokens":                                    "域业务表：seedBusiness 写入访问令牌样本",
	"oauth_device_authorizations":                            "域业务表：seedBusiness 写入设备授权样本",
	"account_name_search_documents":                          "域业务表：seedBusiness 写入账户名搜索索引文档",
	"account_name_search_terms":                              "域业务表：seedBusiness 写入账户名搜索词条",
	"account_circuit_outbox":                                 "域业务表：seedBusiness 写入熔断发件箱样本",
	"account_lock_states":                                    "域业务表：seedBusiness 写入账户锁状态样本",
	"account_list_availability_projection_dependency_health": "域业务表：seedBusiness 写入依赖健康行（CHECK 锁定 dependency_name='runtime_state'，占位值必违约）",
	// 运行时投影/游标族：行由常驻进程（jobs circuitstore、健康投影写者、探测
	// 游标推进器）重建与消费，占位行会让本机后端读到伪造的投影状态。
	"account_api_key_pool_probe_cursors":                "运行时游标族：账户 Key 池探测游标由常驻进程推进",
	"account_health_projection_receipts":                "运行时投影族：J1 投影回执由常驻投影写者消费",
	"account_list_availability_projections":             "运行时投影族：管理列表可用性投影由 jobs circuitstore 重建",
	"account_list_availability_projection_index":        "运行时投影族：可用性投影索引由 jobs circuitstore 重建",
	"account_list_availability_projection_tags":         "运行时投影族：可用性投影标签由 jobs circuitstore 重建",
	"account_list_availability_projection_search_terms": "运行时投影族：可用性投影搜索词条由 jobs circuitstore 重建",
	"account_list_availability_runtime_overlays":        "运行时对账族：可用性并发 overlay 由常驻对账写回",
}

// autofillSkipReason 判断一张表是否跳过自动补全，并给出原因。
func autofillSkipReason(storeName, table string) (string, bool) {
	lower := strings.ToLower(table)
	if reason, ok := autofillSkipExactTables[lower]; ok {
		return reason, true
	}
	for _, prefix := range autofillSkipPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "派生聚合族前缀 " + prefix + "：由既有聚合器重建", true
		}
	}
	for _, suffix := range autofillSkipSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return "运行时状态族后缀 " + suffix + "：由 owner/游标维护", true
		}
	}
	if storeName == StoreModelCheck || storeName == StoreTaskRuns || storeName == StoreAccountHealth {
		return "J3b/任务/健康专库的表由对应域或 owner 写入", true
	}
	if storeName == StoreRuntimeLog || storeName == StoreAuditLog || storeName == StoreOperationLog || storeName == StoreTableMonitor {
		return "日志/监控专库的表由可观测域写入", true
	}
	return "", false
}
