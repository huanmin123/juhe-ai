package contracts

// ArchitectureVersion identifies the cross-project contract generation.
// It is deliberately independent from any business module or deployment release.
const ArchitectureVersion = "go-projects-v1"

// SchemaRevision 是二进制认识的最高结构/步骤版本号，与升级迁移步骤的
// revision 共用同一个递增序号空间（见《升级兼容与迁移账本设计》§6.4/§7.4）：
// DDL 语句清单或步骤注册表的任何变化（含修改既有语句文本）都必须在本次交付
// 内分配新 revision 并把本常量提到本次最高值；golden 测试同时钉住 DDL 清单
// digest 与 (revision, step_id, implVersion) 有序对。P1 已交付账本与首批 12
// 个步骤（revision 1..12）；T91-3 追加 revision 13
// （account-schedulable-normalization）。2026-11-03 revision 14
// accounts.rpm_limit 加法列（纯 DDL 空号）：AI账户每分钟请求上限设计 §7.2
// 为纯 DDL 增量，无新数据步骤，按《升级兼容与迁移账本设计》§6.4 占用
// revision 14 空号、不写入 schema_migrations，由 DDL golden 与本常量共同
// 表达；upgrade 依据 dbRevision < SchemaRevision 触发加法相补列。2026-11-03
// revision 15 时间计划闸门（《时间计划闸门与账户状态解耦设计》§3）：accounts
// 与 api_keys 各新增 availability_schedule_gate 加法列，纯 DDL 增量、无数据
// 步骤（存量自愈走同步任务内建，§7），因此同样不写入 schema_migrations。
// 2026-11-03 revision 16 用量统计消费水位（BUG-0345 根治）：
// juhe_usage.usage_records 新增 usage_stats_aggregated_at /
// client_ip_stats_aggregated_at 两列与两条部分索引，纯 DDL 增量；存量行的
// 水位结算由聚合器首次运行自举完成（不是升级步骤，不需要停写窗口），因此同样
// 不写入 schema_migrations。2026-11-05 revision 17 九家真实厂商收口：
// accounts 新增 vendor_code NOT NULL + connection_driver_code = vendor_code
// 的一致性 CHECK（generic_http / vendor NULL 账户退役），纯 DDL 空号；必须
// 先由 --purge-unsupported-vendor-accounts 清退存量 NULL vendor 账户，再由
// ensure-schema/upgrade 添加约束。2026-11-06 revision 18 代理池（代理池设计
// §4.6/§11.2）：proxy_pools / proxy_pool_members / account_proxy_leases 三表、
// 租约生成序列、accounts.proxy_pool_id 与二选一 CHECK、proxy_profiles 三列
// 健康投影及索引/触发器，纯 DDL 加法相；无新数据步骤，不写入
// schema_migrations。2026-11-07 revision 19 API Key 安全策略批A 存储（API
// Key 安全策略与出站内容脱敏设计 §4.1）：juhe_business 新表
// security_policies、api_keys 加列 security_policy_id、juhe_usage.usage_records
// 加列 security_policy_applied / pii_hits_json，纯 DDL 空号；内置安全策略行
// 由 maintenance seed / ensure-schema 补种臂恢复式预置（不是升级步骤），不写
// 入 schema_migrations。2026-11-07 revision 20 跨协议模型映射退役与
// codex_context schema 清退（退役跨协议模型映射与 ChatBridge 清理计划
// PLAN-20261106T162344524Z 批次C）：注册表新增 revision 20 数据/破坏步骤，
// 按序执行三个阶段——legacy endpoint family 归一
// （normalize-legacy-entry-mapping-families）、非合规 account_entry_mappings
// 行删除（retire-cross-protocol-entry-mappings）、juhe_codex_context schema
// 删除（retire-codex-context-schema，4 表 9 索引）；账本契约一个 revision 只
// 能注册一个步骤（schema_migrations.revision 主键 + 注册表唯一校验），三个
// 阶段合入同一 Destructive 步骤、逐阶段计数进 detail_json 与 dry-run 输出，
// 步骤写入 schema_migrations，只在 contract/all 相执行。2026-11-07 revision
// 21 防护次数写后缓存（API Key 安全策略与出站内容脱敏设计 §4.3.1）：
// security_policies 新增 protection_count bigint NOT NULL DEFAULT 0 加法列
// （gateway Redis 增量经进程内刷写任务合并入库），纯 DDL 空号，按《升级兼容
// 与迁移账本设计》§6.4 不写入 schema_migrations，由 DDL golden 与本常量共同
// 表达。2026-11-09 revision 22 高并发分组退役（普通分组可选调度策略设计 §5，
// T195 阶段③）：注册表新增 revision 22 数据/破坏步骤
// （retire-high-concurrency-groups），把 juhe_business.groups 全部行迁为
// personal、16 键旧策略按 --global-max 映射为两键
// （defaultSoftConcurrency / clientIpConcurrencyLimit，两项都默认 → NULL），
// group_authorization_settings 的 group_type/策略列清为 personal/NULL（覆盖
// 退役），超级分组自身行按首成员重算；畸形 JSON/未知键/未知 group_type 行
// needs_review 阻断。步骤写入 schema_migrations，只在 contract/all 相执行；
// 列形态不变（group_type/scheduling_policy_json 列保留，无 DDL 空号）。
// 2026-11-10 revision 23 退役 usage shard 目录四表（用户裁决“没有需求就
// 去掉”）：注册表新增 revision 23 破坏性步骤
// （retire-usage-shard-catalog），在停写窗口内 DROP
// juhe_usage.usage_record_shard_entries / usage_record_account_shards /
// usage_record_api_key_shards / usage_record_shards；生产写入链路只写
// juhe_usage.usage_records，列表与清理均直接读该日分区表，四表无生产读取方。
// 步骤写入 schema_migrations，只在 contract/all 相执行。2026-11-13 revision
// 24 账户熔断 outbox 终态 failed（T207 收口）：juhe_business.account_circuit_
// outbox 的 status 词表 CHECK 与状态不变式 CHECK 放宽 'failed' 终态臂并具名
// （account_circuit_outbox_status_check / account_circuit_outbox_status_
// invariant），新增幂等约束替换 DO 块（account-circuit-outbox-status-failed-
// pg-check）把存量表旧约束按定义文本定位替换；jobs circuitstore 与 gateway
// circuit_control_plane 的 ReleaseForReplay 在 attempt 达上限时写 failed。
// 纯 DDL 空号，无数据步骤，不写入 schema_migrations。2026-11-19 revision 25
// 单 IP 并发限制迁至 API Key（APIKey单IP并发限制设计 §4）：api_keys 新增
// client_ip_concurrency_limit integer NOT NULL DEFAULT -1 加法列，并注册
// migrate-key-client-ip-limit 数据/破坏步骤（revision 25），把存量
// groups.scheduling_policy_json.clientIpConcurrencyLimit 映射到可达 API Key、
// 分组策略重写为单键 defaultSoftConcurrency 或 NULL、超级分组自身行按首成员
// 重算；冲突默认 needs_review/exit 3，显式 --conflict-policy=min 收敛。
// 步骤写入 schema_migrations，只在 contract/all 相执行。2026-11-19 revision 26
// 非对话媒体统一计费（《非对话媒体统一计费设计》§5.1）：models 新增
// media_pricing_json text 加法列与对象形状 CHECK；纯 DDL 空号，无数据步骤，
// 不写入 schema_migrations。2026-11-20 revision 27 向量/重排协议
// （《对外协议面对齐 new-api 设计》§4.4/§4.5/§6.3）：
// account_endpoint_modes.endpoint_mode 与 accounts.health_check_endpoint_mode
// 的 CHECK 词表追加 embeddings_json / rerank_json；新增
// account-endpoint-mode-pg-check 幂等约束替换 DO 块，把存量两个约束按定义
// 文本定位后重建（旧 health 迁移块按 message_token_counting 定位，对 25 值
// 约束会假成功，本批同批修正）。纯 DDL 空号，无数据步骤，不写入
// schema_migrations。2026-11-21 revision 28 授权接收方确认与来源治理
// （《授权接收方确认与来源治理设计》§7/§12）：授权 grant/source/runtime 扩展
// 状态与接受修订字段，新增分组快照、屏蔽、通知、暂停发起、cooldown 表，
// system_accounts/system_teams 逻辑删除；注册
// authz-recipient-consent-governance 数据/回填步骤（legacy 回填、grant_id 匹配、
// 快照建立、约束 VALIDATE、外键收紧、复核通知）。2026-11-21 revision 29 向量/
// 重排独立能力域（《AI账户能力域向量重排拆分设计》§6）：accounts.capability_
// domain CHECK 四值→六值（建表内联、存量守卫 ALTER 与新具名重建 DO 块
// account-capability-domain-vector-rerank-pg-check）；注册
// capability-domain-vector-rerank 数据步骤（阶段记录 ruleVersion 1→2 升级 +
// 存量账户按推导 v2 重推导，混合声明进失败清单人工处理，RequiredBeforeStartup）。
// 2026-11-21 revision 30 五域上游接口能力退役（《AI账户上游接口能力设计》§3）：
// 注册 capability-domain-declaration-retirement 数据步骤（RequiredBeforeStartup，
// 无 DDL 纯数据步骤，写入 schema_migrations）：删除 image/audio/video/embedding/
// rerank 五域账户加密凭据中的 supported_endpoint_modes 键与 account_endpoint_modes
// 中五域账户行，输出清洗报告；chat 域声明校验与 endpoint mode 词表（健康检查 /
// 连通性探测执行面）保留。旧二进制读空声明 = 不受限，清洗后状态向前兼容。
// 2026-11-22 revision 31 授权治理简化（授权治理模型大简化裁决）：revision
// 28/29/30 未上生产仅 dev、无兼容包袱，审批/冷却/快照/复核机制根治退役——
// 照 resource_authorization_blocks 撤销先例，未发布 revision 的 DDL 语句直接
// 删除不留退役语句：resource_authorization_group_accounts、
// authorization_notifications、resource_authorization_offer_cooldowns、
// system_account_authorization_suspensions 四表与 grants/sources/runtime/
// group_authorization_settings 的 29 条退役列（含 settings.enabled，分组授权
// 开关机制取消；含补删批的 runtime/grants limits_json 建表基列——授权额度
// 全链退役，两条 quota_snapshot 部分索引与两个授权 scope binding seed 回填
// 臂同批退役）退出当前 schema；grants/sources/runtime 三个 status CHECK
// 收敛为 7 值词表（pending/active/paused/expired/revoked/returned/declined），
// acceptance_mode/pending_expiry/dormant/accepted_revision/snapshot_mode 等
// 退役 CHECK 与 pending_expiry 索引一并删除。注册 authz-claim-simplification
// 数据/破坏步骤（RequiredBeforeStartup，写入 schema_migrations）：存量 dev 库
// 由该步骤在单事务内 DROP 退役对象、收敛非 7 值状态（pending_* / dormant /
// needs_review → pending，superseded → returned）、DELETE 授权来源 quota
// scope binding 行后重建收敛 CHECK。
// 当前值 31。
const SchemaRevision int64 = 32

// MinRequiredRevision 是本二进制正常运行所需的最低库结构版本（多节点发布
// 设计 §5.1）：有结构变更的发布批次等于本批 SchemaRevision；库低于该值时
// 新二进制必须 fail-fast，先执行本批 expand。旧二进制在 expand 后能否继续
// 运行由库侧 schema_state.min_readable_binary_revision 决定，不由本常量表达。
// 每次发布随 SchemaRevision 同步更新。
const MinRequiredRevision int64 = SchemaRevision

type ProjectID string

const (
	ProjectGateway     ProjectID = "gateway"
	ProjectJobs        ProjectID = "jobs"
	ProjectMaintenance ProjectID = "maintenance"
)
