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
// 步骤写入 schema_migrations，只在 contract/all 相执行。
// 2026-11-11 revision 24 是时间类型统一改造 Phase 2-1：注册表新增破坏性
// 步骤 instant-timestamptz-small-tables，停写窗口内把 23 张小表 58 个
// instant text 列迁 timestamptz（毫秒/微秒按 inventory）。
// 2026-11-11 revision 25 是 Phase 2-2 CRUD 域：16 张表 44 个 instant text 列
// 迁 timestamptz(3)，含 api_keys/groups/route_strategies/resource_authorization*
// 与 system_team*。
// 2026-11-12 revision 26 是 Phase 2-3 accounts 聚合与投影域：24 张表 73 个
// instant text 列迁 timestamptz（投影/租约列按 inventory 保留微秒）。
// 2026-11-12 revision 27 是 Phase 2-4 dataset/jobs/taskruns 域：17 张表 40 个
// instant text 列迁 timestamptz。2026-11-12 revision 28 是 Phase 3 统计域：
// 53 张表 144 个时间列同表重写（instant → timestamptz、日历键 → date/
// timestamp(0)）。当前值 28。
const SchemaRevision int64 = 34

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
