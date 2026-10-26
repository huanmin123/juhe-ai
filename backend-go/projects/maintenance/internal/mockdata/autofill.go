package mockdata

// autofill（自动补全）阶段：五域 seed 之后，为「maintenance schema 内、域没有
// 写入且当前为空」的表补 1..3 行结构合法的占位行，保证
// --verify-mockdata-coverage 不因未覆盖表为空而失败（exit 3）。
//
// 为什么必须运行时枚举：maintenance schema 的表集合由 EnsurePostgres 与
// gateway / jobs 运行时的 ensure 共同决定（属主表族落在 juhe_dataset 等
// maintenance schema 内），编译期清单会随任何一侧新增表而失效；
// pg_class + information_schema 拿到的是这次实际打开的库的真实形状。
//
// 边界（docs/functions/Mockdata造数设计.md）：
//   - 只补空表：已有行（域写入或运行时写入）的表一律不动，重复 Run 不叠加；
//   - 只枚举 maintenanceSchemas：属主 schema（juhe_j3b / juhe_jobs）不参与，
//     分区子表随父表、分区父表的行由域经日分区写入；
//   - 运行态 / 队列 / 游标 / 派生聚合 / 域写入面按清单跳过，跳过原因显式进
//     报告（AutofillSkipped），不静默吞掉；
//   - 外键感知：引用列必须取父表真实行，父表无行且引用列不可空时整表跳过，
//     绝不编值产生悬挂引用；
//   - 失败语义：单表失败只记原因并继续下一张表——样例数据不该让整个造数
//     失败，但原始错误必须可查，不得转写或吞掉。

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

// autofillTables 是 autofill 阶段的入口：逐 maintenance schema 枚举空表并补
// 占位行，返回「schema.table → 插入行数」与「schema.table → 跳过原因」。
func autofillTables(ctx context.Context, e *env) (map[string]int, map[string]string, error) {
	inserted := map[string]int{}
	skipped := map[string]string{}
	now := e.options.clock()
	enums, err := loadAutofillEnumLabels(ctx, e.db)
	if err != nil {
		return inserted, skipped, err
	}
	for _, schemaName := range maintenanceSchemas {
		tables, err := autofillEnumerateTables(ctx, e.db, schemaName)
		if err != nil {
			return inserted, skipped, err
		}
		columnsByTable, err := loadAutofillSchemaColumns(ctx, e.db, schemaName)
		if err != nil {
			return inserted, skipped, err
		}
		for _, meta := range tables {
			key := qualified(schemaName, meta.Name)
			if reason, skip := autofillSkipReason(meta.Name); skip {
				skipped[key] = reason
				continue
			}
			if meta.RelKind == "p" {
				// 分区父表：插入必须落到具体分区，行由域经日分区写入，
				// 父表补行会撞 no-partition 错误。
				skipped[key] = "分区父表：行由域经日分区写入，不补占位行"
				continue
			}
			columns, ok := columnsByTable[meta.Name]
			if !ok || len(columns) == 0 {
				skipped[key] = "information_schema 没有可读列信息"
				continue
			}
			rows, fillErr := e.autofillTable(ctx, schemaName, meta.Name, columns, enums, now)
			if fillErr != nil {
				skipped[key] = fillErr.Error()
				continue
			}
			if rows > 0 {
				inserted[key] = rows
			}
		}
	}
	return inserted, skipped, nil
}

// autofillTable 给单张空表插入 1..3 行占位行，返回插入行数。任何一步失败都
// 返回携带原始错误的 error，由调用方记入 AutofillSkipped（表级事务：失败不留
// 半批行）。
func (e *env) autofillTable(ctx context.Context, schemaName, table string, columns []autofillColumn, enums map[string][]string, now time.Time) (int, error) {
	schemaTable := qualified(schemaName, table)
	// 空表才补：已有行（域写入、seed、运行时写入）的表一律不动，重复 Run
	// 不叠加。
	existing, err := e.queryCount(ctx, schemaTable)
	if err != nil {
		return 0, err
	}
	if existing > 0 {
		return 0, nil
	}
	foreignKeys, err := loadAutofillForeignKeys(ctx, e.db, schemaName, table)
	if err != nil {
		return 0, err
	}
	resolved, unresolved, err := e.resolveAutofillForeignKeys(ctx, schemaName, table, columns, foreignKeys)
	if err != nil {
		return 0, err
	}
	for _, item := range unresolved {
		if !item.allFromColumnsNullable {
			return 0, fmt.Errorf("外键父表 %s 没有可引用行，跳过 %s 占位行以免产生悬挂引用", item.parentQualified, schemaTable)
		}
	}
	checkDefs, err := loadAutofillCheckDefinitions(ctx, e.db, schemaName, table)
	if err != nil {
		return 0, err
	}
	checkValues := parseAutofillCheckValues(checkDefs)
	jsonShapes := e.sampleAutofillJSONShapes(ctx, schemaName, table, columns)
	pkColumns, err := e.primaryKeyColumns(ctx, e.db, schemaName, table)
	if err != nil {
		return 0, err
	}
	pkSet := map[string]bool{}
	for _, column := range pkColumns {
		pkSet[column] = true
	}
	rowCount := autofillRowCount(table)
	inserted := 0
	if err := e.tx(ctx, func(tx *sql.Tx) error {
		for rowIndex := 0; rowIndex < rowCount; rowIndex++ {
			values, err := buildAutofillRow(schemaName, table, columns, pkSet, checkValues, enums, jsonShapes, resolved, rowIndex, now)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				return fmt.Errorf("表 %s 没有必填列（全部列可空、有默认值或由序列生成），跳过以免插入无意义空行", schemaTable)
			}
			if err := e.insertMapTx(ctx, tx, schemaTable, values); err != nil {
				return err
			}
			inserted++
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return inserted, nil
}

// buildAutofillRow 组装一行占位值：先按列元数据生成（NOT NULL 且无默认值的列
// 必须有值；identity ALWAYS / 序列默认值交给数据库；可空列留空），再用外键父
// 表真实行覆盖引用列（真实父行天然满足外键，优先级高于类型启发与 CHECK 解析）。
func buildAutofillRow(
	schemaName, table string,
	columns []autofillColumn,
	pkSet map[string]bool,
	checkValues map[string][]string,
	enums map[string][]string,
	jsonShapes map[string]string,
	resolved []resolvedAutofillForeignKey,
	rowIndex int,
	now time.Time,
) (map[string]any, error) {
	values := map[string]any{}
	byName := map[string]autofillColumn{}
	for _, column := range columns {
		byName[column.name] = column
		if column.identityAlways() || column.hasSequenceDefault() {
			// identity ALWAYS 必须省略（显式值要 OVERRIDING SYSTEM VALUE），
			// 序列默认值交给数据库自增。
			continue
		}
		if column.nullable {
			// 可空列：留空比塞占位值更安全（不会撞 CHECK / UNIQUE）。
			// 唯一例外是下面的外键覆盖：父表有真实行时补上引用让行更完整。
			continue
		}
		value, err := autofillGeneratedValue(schemaName, table, column, pkSet[column.name], checkValues[column.name], enums[column.udtName], jsonShapes[column.name], rowIndex, now)
		if err != nil {
			return nil, err
		}
		values[column.name] = value
	}
	for _, fk := range resolved {
		if len(fk.parentRows) == 0 {
			continue
		}
		picked := fk.parentRows[rowIndex%len(fk.parentRows)]
		for i, column := range fk.fromColumns {
			// identity ALWAYS 列不能显式插值（PG 拒绝非 DEFAULT 写入），保持
			// 序列生成；序列默认值（serial）允许显式值，照常覆盖。
			if meta := byName[column]; meta.identityAlways() {
				continue
			}
			values[column] = picked[i]
		}
	}
	return values, nil
}

// ---------------------------------------------------------------------------
// 运行态 / 队列 / 游标 / 派生聚合 / 域写入面跳过清单
// ---------------------------------------------------------------------------

// autofillSkipPrefixes 是「派生聚合族」前缀：这些表的行由既有聚合器从使用
// 明细重建，塞占位行会污染统计口径（页面上会看到凭空多出来的聚合点）。
var autofillSkipPrefixes = []string{
	"usage_stats_",
	"usage_model_",
	"usage_error_",
	"usage_latency_",
	"account_quality_",
	"client_ip_",
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

// autofillSkipExactTables 是按表名精确登记的跳过项，分类与理由：
//
//  1. 派生聚合族里无法用前缀/后缀表达的单表（排行快照、小时健康、分组账户
//     统计等）。
//  2. owner 状态 / 租约 / 游标族：这些表的行代表「当前持有者 / 游标位置 /
//     瞬时队列」，占位行会让本机后端误以为某个 owner、某段游标或某条队列
//     消息有效。
//  3. 造数域显式写入的业务表：占位行会在同一次运行的域写入之上叠加假数据；
//     域实现者新增会写的表时必须同步登记到这里。
//  4. 可观测属主表族（sqlite 时代按整库跳过，PG 单库多 schema 后逐表登记）：
//     由 seedObservability 在属主哨兵表存在时写入，或由 gateway / jobs 运行时
//     维护，占位行会污染日志面或毒丸化运行时消费。
var autofillSkipExactTables = map[string]string{
	// —— 派生聚合族。
	"usage_rank_snapshots":                         "派生聚合族：由既有排行重建路径写入，占位行会污染排行",
	"account_health_hourly":                        "派生聚合族：由用量聚合器重建，占位行会污染健康状态条",
	"group_account_stats":                          "派生聚合族：分组账户统计缓存，由聚合器重建",
	"authorization_team_usage_summary_daily":       "派生聚合族：授权用量统计，由聚合器重建",
	"authorization_user_usage_summary_daily":       "派生聚合族：授权用量统计，由聚合器重建",
	"model_trust_latest_dirty_accounts":            "派生队列：可信度脏账户由聚合器维护",
	"model_trust_observation_receipts":             "派生聚合族：可信度 observation 回执",
	"model_trust_window_sources":                   "派生聚合族：可信度窗口来源",
	"model_token_integrity_rounds":                 "派生聚合族：Token 完整轮次",
	"model_identity_baseline_versions":             "派生聚合族：身份基线",
	"model_identity_source_features":               "派生聚合族：身份来源特征",
	"model_token_intercept_baseline_versions":      "派生聚合族：Token 拦截基线",
	"ai_performance_summary_dirty_system_accounts": "派生队列：AI 性能摘要脏账户",
	// —— owner 状态 / 租约 / 游标 / 瞬时队列族。
	"background_job_leases":               "owner 租约族：占位行会让本机后端误判任务持有者",
	"stats_job_state":                     "owner 状态族：统计任务游标，由运行时写者维护",
	"account_health_current_state":        "owner 状态族：账户健康当前态，由探针 owner 维护",
	"usage_range_window_requests":         "瞬时队列：自定义范围请求由运行时写入，设计文档明确允许为空",
	"codex_context_storage_cleanup_queue": "瞬时清理队列：由 storage 清理任务消费",
	"backfill_progress":                   "一次性回填完成标记：由显式运维动作 --apply-success-cost-backfill 写入，造数不伪造",
	"model_check_question_tests":          "运行时诊断记录：题库提交门禁测试由 gateway 运行时写入",
	"chat_user_tool_preferences":          "用户级工具默认绑定：列均可空（空 = 未设默认），用户未配置时为空是正确状态",
	// —— 域显式写入的业务表（business 库）。
	"system_accounts":                            "域业务表：seedBusiness 写入配套用户与 admin",
	"system_sessions":                            "域业务表：seedBusiness 写入会话样本",
	"system_teams":                               "域业务表：seedBusiness 写入团队样本",
	"system_team_members":                        "域业务表：seedBusiness 写入团队成员",
	"groups":                                     "域业务表：seedBusiness 写入分组",
	"super_group_members":                        "域业务表：seedBusiness 写入超级分组绑定",
	"group_accounts":                             "域业务表：seedBusiness 写入分组-账户绑定",
	"accounts":                                   "域业务表：seedBusiness 写入 AI 账户",
	"account_connections":                        "域业务表：seedBusiness 写入账户连接面",
	"account_endpoint_modes":                     "域业务表：seedBusiness 写入账户连接 mode 集合",
	"account_api_key_runtime_states":             "域业务表：seedBusiness 写入账户内 Key 运行态",
	"account_supported_models":                   "域业务表：seedBusiness 写入账户支持模型",
	"account_entry_mappings":                     "域业务表：seedBusiness 写入账户入口映射",
	"account_tags":                               "域业务表：seedBusiness 写入账户标签",
	"account_tag_bindings":                       "域业务表：seedBusiness 写入标签绑定",
	"account_circuit_incidents":                  "域业务表：seedBusiness 写入熔断事件样本",
	"account_circuit_outbox":                     "域业务表：seedBusiness 写入熔断发件箱样本",
	"account_lock_states":                        "域业务表：seedBusiness 写入账户锁状态样本",
	"api_keys":                                   "域业务表：seedBusiness 写入 API Key 与明文本地 Key",
	"route_strategies":                           "域业务表：seedBusiness 写入路由策略",
	"route_strategy_groups":                      "域业务表：seedBusiness 写入策略分组绑定",
	"resource_authorizations":                    "域业务表：seedBusiness 写入授权样本",
	"resource_authorization_grants":              "域业务表：seedBusiness 写入授权授予",
	"resource_authorization_sources":             "域业务表：seedBusiness 写入授权来源",
	"group_authorization_settings":               "域业务表：seedBusiness 写入授权分组设置",
	"announcements":                              "域业务表：seedBusiness 写入公告",
	"announcement_reads":                         "域业务表：seedBusiness 写入公告已读",
	"external_integration_sources":               "域业务表：seedBusiness 写入外部来源系统",
	"external_integration_source_tokens":         "域业务表：seedBusiness 写出来源 Token",
	"response_inspection_policies":               "域业务表：seedBusiness 写入响应检查策略",
	"models":                                     "域业务表：seedBusiness 写入模型目录样本",
	"proxy_profiles":                             "域业务表：seedBusiness 写入代理样本",
	"model_quality_policies":                     "域业务表：seedBusiness 写入模型质量策略",
	"model_quality_schedules":                    "域业务表：seedBusiness 写入模型质量计划",
	"account_quality_enforcements":               "域业务表：seedBusiness 写入质量隔离样本",
	"model_check_question_bank":                  "域业务表：seedBusiness 写入模型检测题库",
	"account_test_tasks":                         "域业务表：seedBusiness 写入账户测试任务",
	"account_test_sessions":                      "域业务表：seedBusiness 写入账户测试会话",
	"account_test_session_tasks":                 "域业务表：seedBusiness 写入账户测试会话任务",
	"account_schedule_status_events":             "域业务表：seedBusiness 写入账户时间计划事件",
	"api_key_schedule_status_events":             "域业务表：seedBusiness 写入 Key 时间计划事件",
	"media_jobs":                                 "域业务表：seedBusiness 写入媒体任务样本",
	"request_quota_hourly_window_scope_bindings": "域业务表：seedBusiness 写入小时配额窗口 scope 绑定",
	"openai_compatible_files":                    "域业务表：seedBusiness 写入 OpenAI 兼容文件",
	"openai_compatible_vector_stores":            "域业务表：seedBusiness 写入向量库",
	"openai_compatible_vector_store_files":       "域业务表：seedBusiness 写入向量库文件",
	"openai_compatible_vector_store_chunks":      "域业务表：seedBusiness 写入向量库分块",
	"account_name_search_documents":              "域业务表：seedBusiness 写入账户名搜索索引文档",
	"account_name_search_terms":                  "域业务表：seedBusiness 写入账户名搜索词条",
	"account_list_availability_projection_dependency_health": "域业务表：seedBusiness 写入依赖健康行（CHECK 锁定 dependency_name='runtime_state'，占位值必违约）",
	// —— 域显式写入的业务表（usage / stats / observability / chat-codex-model-check）。
	"usage_records":               "域业务表：seedUsage 写入使用记录（经日分区）",
	"usage_record_shards":         "域业务表：seedUsage 写入分片登记",
	"usage_record_shard_entries":  "域业务表：seedUsage 写入分片条目",
	"usage_record_account_shards": "域业务表：seedUsage 写入账户分片索引",
	"usage_record_api_key_shards": "域业务表：seedUsage 写入 Key 分片索引",
	// usage_record_cleanup_deductions 是 jobs 清理任务的扣减台账（行语义是「该
	// 记录已从分片删除并已扣减统计」），域按裁决不伪造。
	"usage_record_cleanup_deductions":    "运行时台账：记录清理扣减由 jobs 清理任务结算，伪造行会让重试任务结算不存在的记录",
	"account_usage_snapshots":            "域业务表：seedStatsRaw 写入账户用量快照",
	"client_ip_registry":                 "域业务表：seedStatsRaw 写入 IP 登记",
	"client_ip_policies":                 "域业务表：seedStatsRaw 写入 IP 封禁策略",
	"client_ip_policy_hits":              "域业务表：seedStatsRaw 写入策略命中",
	"background_task_runs":               "域业务表：seedStatsRaw 写入后台任务运行",
	"group_account_stats_dirty":          "派生队列：seedStatsRaw 写入分组统计脏标记，聚合器重建 group_account_stats",
	"public_api_logs":                    "域业务表：seedObservability 写入公开接口日志",
	"audit_logs":                         "属主表族：seedObservability 写入审计日志（gateway 运行时建表）",
	"audit_log_attempts":                 "属主表族：seedObservability 写入审计尝试样本",
	"audit_error_groups":                 "属主表族：seedObservability 写入审计错误分组",
	"audit_payload_blobs":                "属主表族：seedObservability 按内容寻址 upsert payload blob",
	"audit_payload_refs":                 "属主表族：seedObservability 写入 payload 引用",
	"audit_payload_blob_gc":              "运行时回收队列：audit payload blob 回收由 gateway 运行时维护",
	"operation_logs":                     "属主表族：seedObservability 写入操作日志",
	"operation_log_targets":              "属主表族：seedObservability 写入操作目标",
	"operation_log_viewers":              "属主表族：seedObservability 写入查看者",
	"operation_log_summary_search_terms": "属主表族：seedObservability 写入摘要搜索词条",
	"runtime_logs":                       "属主表族：seedObservability 写入运行日志",
	"runtime_log_facet_summary":          "属主表族：seedObservability 写入运行日志分面",
	"runtime_log_level_facets":           "属主表族：seedObservability 写入日志级别分面",
	"runtime_log_event_facets":           "属主表族：seedObservability 写入日志事件分面",
	"runtime_log_file_cursors":           "owner 游标族：文件读取进度由 jobs runtimelog 运行时维护；PG 造数无日志目录契约、不写物理日志文件，不伪造游标",
	"record_maintenance_jobs":            "运行时队列：记录维护任务由 jobs 消费后为空，造数不伪造",
	"account_record_cleanup_targets":     "域业务表：seedObservability 写入账户清理目标",
	"api_key_record_cleanup_targets":     "域业务表：seedObservability 写入 Key 清理目标",
	"chat_conversations":                 "域业务表：seedChatCodexModelCheck 写入会话",
	"chat_messages":                      "域业务表：seedChatCodexModelCheck 写入消息（经日分区）",
	"chat_context_checkpoints":           "域业务表：seedChatCodexModelCheck 写入上下文检查点",
	"chat_context_entries":               "域业务表：seedChatCodexModelCheck 写入上下文条目",
	"chat_message_idempotency":           "域业务表：seedChatCodexModelCheck 写入幂等键",
	"chat_asset_references":              "域业务表：seedChatCodexModelCheck 写入资产引用",
	"chat_assets":                        "域业务表：seedChatCodexModelCheck 写入聊天资产",
	"chat_image_generations":             "域业务表：媒体样本写入图像生成任务",
	"chat_user_asset_usage":              "域业务表：seedChatCodexModelCheck 写入用户资产用量汇总",
	"chat_user_storage_windows":          "域业务表：seedChatCodexModelCheck 写入用户存储窗口",
	"codex_context_sessions":             "域业务表：seedChatCodexModelCheck 写入 codex 会话",
	"codex_context_responses":            "域业务表：seedChatCodexModelCheck 写入 codex 响应状态",
	"codex_context_compacts":             "域业务表：seedChatCodexModelCheck 写入 codex 摘要",
	"model_check_runs":                   "域业务表：J3b bootstrap 后写入运行",
	"model_check_items":                  "域业务表：J3b bootstrap 后写入检查项",
	"model_check_observations":           "域业务表：J3b bootstrap 后写入受控 observation",
	"model_check_scheduler_tasks":        "域业务表：J3b bootstrap 后写入调度任务",
	"account_quality_health_hourly":      "域业务表：seedChatCodexModelCheck 写入质量健康小时",
	// —— J1（账户健康）owner 运行态表，由 jobs 健康owner / 业务变更运行时维护；
	// 域按裁决不伪造，登记在这里把「为什么是空的」写清楚而不是误标为域表。
	"account_health_outcomes":                  "owner 运行态：J1 探针结果由 jobs 健康owner 写入，域不伪造",
	"account_health_direct_input_suppressions": "owner 运行态：直连输入抑制由 jobs 健康owner 维护",
	"account_health_jobs_input_versions":       "owner 运行态：J1 输入版本由业务变更运行时预留快照 epoch",
	"account_health_jobs_input_outbox":         "owner 运行态：J1 输入 intent outbox 由运行时发布器消费",
	"account_health_projection_cursors":        "owner 游标族：账户健康投影游标",
	// account_health_probe_request_outbox 是 gateway 写入、jobs J1 drain 消费的
	// 交接 outbox。占位行的 source_fence 是非 JSON 文本，会让 J1 每周期 claim
	// 整体失败、owner lease 反复释放（2026-09-23 实测毒丸），绝不能补占位行。
	"account_health_probe_request_outbox": "运行时 outbox：探针请求由 gateway 写入、jobs J1 drain 消费，域不伪造",
	// —— 运行时投影 / 游标 / 触发器族：行由常驻进程（jobs circuitstore、健康
	// 投影写者、探测游标推进器）或表触发器重建与消费，占位行会让本机后端读到
	// 伪造的投影状态。
	"account_api_key_pool_probe_cursors":                 "运行时游标族：账户 Key 池探测游标由常驻进程推进",
	"account_health_projection_receipts":                 "运行时投影族：J1 投影回执由常驻投影写者消费",
	"account_list_availability_projections":              "运行时投影族：管理列表可用性投影由 jobs circuitstore 重建",
	"account_list_availability_projection_index":         "运行时投影族：可用性投影索引由 jobs circuitstore 重建",
	"account_list_availability_projection_tags":          "运行时投影族：可用性投影标签由 jobs circuitstore 重建",
	"account_list_availability_projection_search_terms":  "运行时投影族：可用性投影搜索词条由 jobs circuitstore 重建",
	"account_list_availability_projection_viewer_health": "运行时投影族：查看者健康行由表触发器随访问维护",
	"account_list_availability_runtime_overlays":         "运行时对账族：可用性并发 overlay 由常驻对账写回",
	"account_list_availability_dirty":                    "运行时脏队列：可用性脏标记由触发器写入、jobs circuitstore 消费后即空",
	"proxy_latency_projection_receipts":                  "运行时回执族：由 jobs 代理延迟投影写者消费，造数不伪造",
}

// autofillSkipReason 判断一张表是否跳过自动补全，并给出原因。
func autofillSkipReason(table string) (string, bool) {
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
	return "", false
}

// ---------------------------------------------------------------------------
// 元数据枚举（pg_class / information_schema）
// ---------------------------------------------------------------------------

// autofillEnumerateTables 枚举一个 schema 下 autofill 可评估的用户表：排除分
// 区子表（行随父表语义），保留分区父表与普通表；分区父表由调用方按 relkind
// 跳过（插入必须落到具体分区，父表补行会撞 no-partition）。按名字排序保证
// 报告与补行顺序稳定。
func autofillEnumerateTables(ctx context.Context, db queryer, schemaName string) ([]autofillTableMeta, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, c.relkind
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'p')
		  AND c.relispartition = false
		ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("枚举 schema %s 表清单: %w", schemaName, err)
	}
	defer rows.Close()
	var tables []autofillTableMeta
	for rows.Next() {
		var meta autofillTableMeta
		if err := rows.Scan(&meta.Name, &meta.RelKind); err != nil {
			return nil, fmt.Errorf("枚举 schema %s 表清单: %w", schemaName, err)
		}
		tables = append(tables, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("枚举 schema %s 表清单: %w", schemaName, err)
	}
	return tables, nil
}

// autofillTableMeta 是一张候选表的枚举元数据。
type autofillTableMeta struct {
	Name    string
	RelKind string // 'r' 普通表；'p' 分区父表
}

// autofillColumn 是 autofill 视角的一列元数据（information_schema.columns）。
type autofillColumn struct {
	name         string
	dataType     string
	udtName      string
	nullable     bool
	defaultValue *string
	identity     bool
	// identityGeneration 是 'ALWAYS'、'BY DEFAULT' 或空（非 identity 列）。
	identityGeneration string
	maxLength          int // character_maximum_length；0 表示不限
}

// identityAlways 判断是否 GENERATED ALWAYS AS IDENTITY：显式插入需要
// OVERRIDING SYSTEM VALUE，autofill 一律省略、交给序列。
func (c autofillColumn) identityAlways() bool {
	return c.identity && c.identityGeneration == "ALWAYS"
}

// hasSequenceDefault 判断默认值是否为序列（serial / nextval）：显式给值会与
// 序列后续分配冲突，交给数据库生成。
func (c autofillColumn) hasSequenceDefault() bool {
	return c.defaultValue != nil && strings.Contains(strings.ToLower(*c.defaultValue), "nextval(")
}

// isTextual 判断列是否按文本生成值（含 citext 这类 USER-DEFINED 文本类型）。
func (c autofillColumn) isTextual() bool {
	switch c.dataType {
	case "text", "character varying", "character", "citext":
		return true
	}
	return false
}

// loadAutofillSchemaColumns 一次查询装载一个 schema 的全部列元数据（按表
// 分组、列序保持 ordinal_position）。
func loadAutofillSchemaColumns(ctx context.Context, db queryer, schemaName string) (map[string][]autofillColumn, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, udt_name, is_nullable,
		       column_default, is_identity, identity_generation, character_maximum_length
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("装载 schema %s 列清单: %w", schemaName, err)
	}
	defer rows.Close()
	byTable := map[string][]autofillColumn{}
	for rows.Next() {
		var (
			tableName, columnName, dataType, udtName string
			isNullable, isIdentity                   string
			columnDefault                            sql.NullString
			identityGeneration                       sql.NullString
			maxLength                                sql.NullInt32
		)
		if err := rows.Scan(&tableName, &columnName, &dataType, &udtName, &isNullable, &columnDefault, &isIdentity, &identityGeneration, &maxLength); err != nil {
			return nil, fmt.Errorf("装载 schema %s 列清单: %w", schemaName, err)
		}
		column := autofillColumn{
			name:      columnName,
			dataType:  dataType,
			udtName:   udtName,
			nullable:  isNullable == "YES",
			identity:  isIdentity == "YES",
			maxLength: int(maxLength.Int32),
		}
		if columnDefault.Valid {
			value := columnDefault.String
			column.defaultValue = &value
		}
		if identityGeneration.Valid {
			column.identityGeneration = identityGeneration.String
		}
		byTable[tableName] = append(byTable[tableName], column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("装载 schema %s 列清单: %w", schemaName, err)
	}
	return byTable, nil
}

// loadAutofillEnumLabels 装载全库枚举类型的允许标签（typname → 标签，按
// enumsortorder）。枚举列的占位值取第一个标签，天然满足类型约束。
func loadAutofillEnumLabels(ctx context.Context, db queryer) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.typname, e.enumlabel
		FROM pg_type t
		JOIN pg_enum e ON e.enumtypid = t.oid
		ORDER BY t.typname, e.enumsortorder`)
	if err != nil {
		return nil, fmt.Errorf("装载枚举类型词表: %w", err)
	}
	defer rows.Close()
	labels := map[string][]string{}
	for rows.Next() {
		var typname, enumlabel string
		if err := rows.Scan(&typname, &enumlabel); err != nil {
			return nil, fmt.Errorf("装载枚举类型词表: %w", err)
		}
		labels[typname] = append(labels[typname], enumlabel)
	}
	return labels, rows.Err()
}

// loadAutofillCheckDefinitions 读表的 CHECK 约束定义文本（pg_get_constraintdef）。
// 表不存在或没有 CHECK 约束返回空切片。
func loadAutofillCheckDefinitions(ctx context.Context, db queryer, schemaName, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE contype = 'c'
		  AND conrelid = to_regclass($1)`, qualified(schemaName, table))
	if err != nil {
		return nil, fmt.Errorf("读取 %s CHECK 约束: %w", qualified(schemaName, table), err)
	}
	defer rows.Close()
	var defs []string
	for rows.Next() {
		var def sql.NullString
		if err := rows.Scan(&def); err != nil {
			return nil, fmt.Errorf("读取 %s CHECK 约束: %w", qualified(schemaName, table), err)
		}
		if def.Valid && strings.TrimSpace(def.String) != "" {
			defs = append(defs, def.String)
		}
	}
	return defs, rows.Err()
}

// ---------------------------------------------------------------------------
// CHECK 约束解析
// ---------------------------------------------------------------------------

// autofillCheckLiteralPattern 匹配单引号字符串字面量（” 转义）。
var autofillCheckLiteralPattern = regexp.MustCompile(`'((?:[^']|'')*)'`)

// autofillCheckIdentifierPattern 匹配约束定义文本里的标识符。
var autofillCheckIdentifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// autofillCheckKeywords 是从约束文本提取列名时需要排除的 SQL 关键字、函数名与
// 类型名（它们出现在第一个字面量之前，但不是被约束的列）。
var autofillCheckKeywords = map[string]bool{
	"check": true, "and": true, "or": true, "not": true, "null": true,
	"is": true, "in": true, "between": true, "like": true, "similar": true,
	"exists": true, "any": true, "all": true, "array": true, "cast": true,
	"length": true, "char_length": true, "character_length": true,
	"octet_length": true, "position": true, "substring": true, "substr": true,
	"trim": true, "upper": true, "lower": true, "abs": true, "mod": true,
	"gcd": true, "now": true, "current_date": true, "current_timestamp": true,
	"text": true, "bpchar": true, "varchar": true, "character": true,
	"varying": true, "char": true, "int": true, "int2": true, "int4": true,
	"int8": true, "integer": true, "bigint": true, "smallint": true,
	"numeric": true, "decimal": true, "real": true, "double": true,
	"precision": true, "bool": true, "boolean": true, "date": true,
	"timestamp": true, "timestamptz": true, "interval": true, "uuid": true,
	"json": true, "jsonb": true,
}

// parseAutofillCheckValues 从 CHECK 约束定义文本解析「列名 → 允许值（按书写
// 顺序）」：只处理「单列 + 字符串字面量集合」的形态（等值与 IN / ANY(ARRAY[...])
// 都会落到这里）；多列约束或无数值的约束解析不出来，调用方退回按类型取值，
// 插入失败会作为跳过原因记录（携带原始错误）。
func parseAutofillCheckValues(defs []string) map[string][]string {
	values := map[string][]string{}
	for _, def := range defs {
		firstLiteral := autofillCheckLiteralPattern.FindStringIndex(def)
		head := def
		if firstLiteral != nil {
			head = def[:firstLiteral[0]]
		}
		candidates := map[string]bool{}
		for _, match := range autofillCheckIdentifierPattern.FindAllString(head, -1) {
			if autofillCheckKeywords[strings.ToLower(match)] {
				continue
			}
			candidates[match] = true
		}
		if len(candidates) != 1 {
			continue
		}
		var column string
		for name := range candidates {
			column = name
		}
		if _, ok := values[column]; ok {
			continue
		}
		var allowed []string
		for _, match := range autofillCheckLiteralPattern.FindAllStringSubmatch(def, -1) {
			allowed = append(allowed, strings.ReplaceAll(match[1], "''", "'"))
		}
		if len(allowed) > 0 {
			values[column] = allowed
		}
	}
	return values
}

// ---------------------------------------------------------------------------
// 外键解析
// ---------------------------------------------------------------------------

// autofillForeignKey 是 information_schema 外键图里的一条边（组合外键按约束名
// 聚合，from/to 列按 ordinal_position 对齐）。
type autofillForeignKey struct {
	constraintName string
	parentSchema   string
	parentTable    string
	fromColumns    []string
	toColumns      []string
}

// resolvedAutofillForeignKey 是一条可引用的外键：parentRows 是从父表取到的真
// 实行（按 to 列序对齐 from 列序）。自引用父表在只补空表的规则下必然无可引用
// 行，按 unresolved 处理。
type resolvedAutofillForeignKey struct {
	fromColumns []string
	parentRows  [][]any
}

// unresolvedAutofillForeignKey 记录父表暂无可引用行的外键：from 列全部可空时
// 占位行留空即可；否则必须整表跳过，绝不编值。
type unresolvedAutofillForeignKey struct {
	parentQualified        string
	allFromColumnsNullable bool
}

// loadAutofillForeignKeys 读一张表的全部外键边（含跨 schema 父表）。
func loadAutofillForeignKeys(ctx context.Context, db queryer, schemaName, table string) ([]autofillForeignKey, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT tc.constraint_name, kcu.column_name,
		       ccu.table_schema, ccu.table_name, ccu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.constraint_schema = kcu.constraint_schema
		 AND tc.table_name = kcu.table_name
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name
		 AND rc.constraint_schema = tc.constraint_schema
		JOIN information_schema.key_column_usage ccu
		  ON ccu.constraint_name = rc.unique_constraint_name
		 AND ccu.constraint_schema = rc.unique_constraint_schema
		 AND ccu.ordinal_position = kcu.ordinal_position
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY tc.constraint_name, kcu.ordinal_position`, schemaName, table)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 外键: %w", qualified(schemaName, table), err)
	}
	defer rows.Close()
	byName := map[string]*autofillForeignKey{}
	var order []string
	for rows.Next() {
		var constraintName, fromColumn, parentSchema, parentTable, toColumn string
		if err := rows.Scan(&constraintName, &fromColumn, &parentSchema, &parentTable, &toColumn); err != nil {
			return nil, fmt.Errorf("读取 %s 外键: %w", qualified(schemaName, table), err)
		}
		edge := byName[constraintName]
		if edge == nil {
			edge = &autofillForeignKey{constraintName: constraintName, parentSchema: parentSchema, parentTable: parentTable}
			byName[constraintName] = edge
			order = append(order, constraintName)
		}
		edge.fromColumns = append(edge.fromColumns, fromColumn)
		edge.toColumns = append(edge.toColumns, toColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s 外键: %w", qualified(schemaName, table), err)
	}
	edges := make([]autofillForeignKey, 0, len(order))
	for _, name := range order {
		edges = append(edges, *byName[name])
	}
	return edges, nil
}

// resolveAutofillForeignKeys 把外键边分成「可引用」（父表有真实行）与「父表暂
// 无行」两组。自引用在「只补空表」规则下父表必然为空，直接按 unresolved 处理。
func (e *env) resolveAutofillForeignKeys(ctx context.Context, schemaName, table string, columns []autofillColumn, foreignKeys []autofillForeignKey) ([]resolvedAutofillForeignKey, []unresolvedAutofillForeignKey, error) {
	nullable := map[string]bool{}
	for _, column := range columns {
		nullable[column.name] = column.nullable
	}
	var resolved []resolvedAutofillForeignKey
	var unresolved []unresolvedAutofillForeignKey
	for _, fk := range foreignKeys {
		parentQualified := qualified(fk.parentSchema, fk.parentTable)
		allNullable := true
		for _, column := range fk.fromColumns {
			if !nullable[column] {
				allNullable = false
				break
			}
		}
		if fk.parentSchema == schemaName && fk.parentTable == table {
			// 自引用：本表此刻为空（只补空表），无可引用行。
			unresolved = append(unresolved, unresolvedAutofillForeignKey{parentQualified: parentQualified, allFromColumnsNullable: allNullable})
			continue
		}
		parentRows, err := fetchAutofillParentRows(ctx, e.db, fk.parentSchema, fk.parentTable, fk.toColumns)
		if err != nil {
			return nil, nil, fmt.Errorf("取样外键父表 %s: %w", parentQualified, err)
		}
		if len(parentRows) == 0 {
			unresolved = append(unresolved, unresolvedAutofillForeignKey{parentQualified: parentQualified, allFromColumnsNullable: allNullable})
			continue
		}
		resolved = append(resolved, resolvedAutofillForeignKey{fromColumns: fk.fromColumns, parentRows: parentRows})
	}
	return resolved, unresolved, nil
}

// fetchAutofillParentRows 取父表 to 列的前 limit 行真实值（过滤 to 列含 NULL
// 的行：可空唯一键不能作为引用样本）。
func fetchAutofillParentRows(ctx context.Context, db queryer, parentSchema, parentTable string, toColumns []string) ([][]any, error) {
	quoted := make([]string, 0, len(toColumns))
	for _, column := range toColumns {
		quoted = append(quoted, quotePGIdentifier(column))
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s LIMIT 64",
		strings.Join(quoted, ", "), quotePGIdentifier(parentSchema)+"."+quotePGIdentifier(parentTable)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		raw := make([]any, len(toColumns))
		dest := make([]any, len(toColumns))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		hasNull := false
		for _, value := range raw {
			if value == nil {
				hasNull = true
				break
			}
		}
		if hasNull {
			continue
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 值生成
// ---------------------------------------------------------------------------

// autofillRowCount 用表名的稳定哈希决定 1..3 行：同一张表每次运行行数一致，
// 让报告与断言可复现。
func autofillRowCount(table string) int {
	sum := sha256.Sum256([]byte(table))
	return 1 + int(sum[0])%3
}

// autofillGeneratedValue 按列名与列类型给出一个结构合法的确定性占位值。
//
// 优先级：CHECK 允许值 → 枚举标签 → 列名语义（仅文本列）→ 声明类型。
// 生成值都携带清理标识（CleanupIDPrefix / CleanupTracePrefix），下次运行的
// 通用标识扫描能删掉它们。
func autofillGeneratedValue(schemaName, table string, column autofillColumn, isPrimaryKey bool, checkValues []string, enumLabels []string, jsonShape string, rowIndex int, now time.Time) (any, error) {
	lower := strings.ToLower(column.name)
	// CHECK IN 允许值优先：能解析出字面量时取第一个（CHECK 字面量只对文本列
	// 有意义；数值约束解析不出字面量，自然走类型路径）。
	if len(checkValues) > 0 && column.isTextual() {
		return autofillTruncate(checkValues[0], column.maxLength), nil
	}
	// 枚举类型：取第一个标签，天然满足类型约束。
	if len(enumLabels) > 0 {
		return enumLabels[0], nil
	}
	switch column.dataType {
	case "smallint", "integer", "bigint":
		return rowIndex + 1, nil
	case "numeric", "real", "double precision":
		return float64(rowIndex + 1), nil
	case "boolean":
		return true, nil
	case "date", "timestamp without time zone", "timestamp with time zone",
		"time without time zone", "time with time zone":
		return now.Add(-time.Duration(rowIndex) * time.Minute).UTC(), nil
	case "json", "jsonb":
		return autofillJSONPlaceholder(jsonShape), nil
	case "uuid":
		return autofillUUID(table, column.name, rowIndex), nil
	case "bytea":
		return []byte(CleanupTracePrefix + "autofill"), nil
	case "inet":
		return "10.10.0.1", nil
	case "ARRAY":
		return autofillArrayValue(table, column, rowIndex)
	}
	if column.isTextual() {
		value := autofillTextValue(table, column, lower, isPrimaryKey, jsonShape, rowIndex, now)
		return autofillTruncate(value, column.maxLength), nil
	}
	return nil, fmt.Errorf("autofill 无法为 %s.%s 列 %s 生成 %s 类型的合法值", schemaName, table, column.name, column.dataType)
}

// autofillTextValue 生成文本列的占位值：列名语义优先（时间 / 哈希 / IP / 邮箱 /
// URL），主键用表级确定性 ID，其余用带列名的确定性标记。
func autofillTextValue(table string, column autofillColumn, lower string, isPrimaryKey bool, jsonShape string, rowIndex int, now time.Time) string {
	switch {
	case strings.HasSuffix(lower, "_json"):
		// *_json 形态跟随同列既有数据（见 sampleAutofillJSONShapes）：数组列
		// 必须占位 "[]"，只有无样本可循时才退回对象占位。
		return autofillJSONPlaceholder(jsonShape)
	case strings.HasSuffix(lower, "_at") || lower == "time" || strings.HasSuffix(lower, "_time"):
		return now.Add(-time.Duration(rowIndex) * time.Minute).UTC().Format(isoMillisLayout)
	case strings.Contains(lower, "sha256") || strings.Contains(lower, "hash") || strings.Contains(lower, "digest") || strings.Contains(lower, "hmac") || strings.Contains(lower, "fingerprint") || strings.Contains(lower, "checksum"):
		sum := sha256.Sum256([]byte(table + "." + column.name + "." + strconv.Itoa(rowIndex)))
		return hex.EncodeToString(sum[:])
	case strings.HasSuffix(lower, "_ip"):
		return "10.10.0.1"
	case strings.Contains(lower, "email"):
		return CleanupIDPrefix + "autofill@example.invalid"
	case strings.Contains(lower, "url") || strings.Contains(lower, "endpoint"):
		return "https://example.invalid/" + CleanupTracePrefix + "autofill"
	case isPrimaryKey:
		return CleanupIDPrefix + "autofill_" + table + "_" + strconv.Itoa(rowIndex)
	default:
		return CleanupIDPrefix + "autofill_" + table + "_" + column.name + "_" + strconv.Itoa(rowIndex)
	}
}

// autofillArrayValue 为数组列生成单元素占位值（按元素类型）。
func autofillArrayValue(table string, column autofillColumn, rowIndex int) (any, error) {
	switch column.udtName {
	case "_text", "_varchar", "_bpchar", "_citext":
		return []string{CleanupTracePrefix + "autofill"}, nil
	case "_int4":
		return []int32{int32(rowIndex + 1)}, nil
	case "_int8":
		return []int64{int64(rowIndex + 1)}, nil
	case "_int2":
		return []int16{int16(rowIndex + 1)}, nil
	case "_uuid":
		return []string{autofillUUID(table, column.name, rowIndex)}, nil
	case "_json", "_jsonb":
		return []string{"{}"}, nil
	case "_bool":
		return []bool{true}, nil
	default:
		return nil, fmt.Errorf("autofill 无法为列 %s 生成 %s 元素的数组占位值", column.name, column.udtName)
	}
}

// autofillJSONPlaceholder 按采样形态返回合法 JSON 占位：数组形态必须占位
// "[]"（消费方按字符串数组反序列化，写 "{}" 会整体反序列化失败），无样本时
// 退回对象占位 "{}"。
func autofillJSONPlaceholder(shape string) string {
	if shape == "array" {
		return "[]"
	}
	return "{}"
}

// autofillUUID 由表名 + 列名 + 行号派生确定性 UUID（v4 形态：版本位与变体位
// 按规范置位），同表同列每次运行得到相同值，冲突时幂等。
func autofillUUID(table, columnName string, rowIndex int) string {
	sum := sha256.Sum256([]byte(table + "." + columnName + "." + strconv.Itoa(rowIndex)))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// autofillTruncate 把生成值截到 character_maximum_length 以内（生成值全部是
// ASCII，按字节截断不会拆开多字节字符）。
func autofillTruncate(value string, maxLength int) string {
	if maxLength > 0 && len(value) > maxLength {
		return value[:maxLength]
	}
	return value
}

// sampleAutofillJSONShapes 对 *_json / JSON 类型的列取既有数据样本，按其 JSON
// 形态返回「列名 → "array"|"object"」。本表为空（只补空表）时按「同 schema 内
// 同名列」取样；无样本或解析失败的列不出现在结果里，由 autofillJSONPlaceholder
// 取对象占位。取样失败不阻塞补行（形态只是启发，不是硬约束）。
func (e *env) sampleAutofillJSONShapes(ctx context.Context, schemaName, table string, columns []autofillColumn) map[string]string {
	shapes := map[string]string{}
	for _, column := range columns {
		if !autofillIsJSONColumn(column) {
			continue
		}
		// 同表取样（表非空的场景：分区父表之外的并发写入，防御性保留）。
		if shape := sampleJSONShapeFromTable(ctx, e.db, schemaName, table, column.name); shape != "" {
			shapes[column.name] = shape
			continue
		}
		// 同 schema 同名列取样。
		for _, candidate := range autofillJSONSampleCandidates(ctx, e.db, schemaName, column.name, table) {
			if shape := sampleJSONShapeFromTable(ctx, e.db, schemaName, candidate, column.name); shape != "" {
				shapes[column.name] = shape
				break
			}
		}
	}
	return shapes
}

// autofillIsJSONColumn 判断一列是否按 JSON 占位（json/jsonb 类型或 *_json
// 命名的文本列）。
func autofillIsJSONColumn(column autofillColumn) bool {
	if column.dataType == "json" || column.dataType == "jsonb" {
		return true
	}
	return column.isTextual() && strings.HasSuffix(strings.ToLower(column.name), "_json")
}

// autofillJSONSampleCandidates 枚举同 schema 内拥有同名列的其他表（按名字
// 排序，保证取样确定性），最多返回 8 个候选。
func autofillJSONSampleCandidates(ctx context.Context, db queryer, schemaName, columnName, excludeTable string) []string {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT table_name
		FROM information_schema.columns
		WHERE table_schema = $1 AND column_name = $2 AND table_name <> $3
		ORDER BY table_name
		LIMIT 8`, schemaName, columnName, excludeTable)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var candidates []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil
		}
		candidates = append(candidates, name)
	}
	return candidates
}

// sampleJSONShapeFromTable 读一列的一个非空样本并判定 JSON 形态："array"、
// "object" 或 ""（无样本 / 解析失败）。
func sampleJSONShapeFromTable(ctx context.Context, db queryer, schemaName, table, columnName string) string {
	quotedColumn := quotePGIdentifier(columnName)
	quotedTable := quotePGIdentifier(schemaName) + "." + quotePGIdentifier(table)
	var sample sql.NullString
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s IS NOT NULL LIMIT 1", quotedColumn, quotedTable, quotedColumn)
	if err := db.QueryRowContext(ctx, query).Scan(&sample); err != nil || !sample.Valid {
		return ""
	}
	trimmed := strings.TrimSpace(sample.String)
	switch {
	case strings.HasPrefix(trimmed, "["):
		return "array"
	case strings.HasPrefix(trimmed, "{"):
		return "object"
	}
	return ""
}
