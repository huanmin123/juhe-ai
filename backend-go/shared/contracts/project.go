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
// 不写入 schema_migrations。当前值 16。
const SchemaRevision int64 = 16

type ProjectID string

const (
	ProjectGateway     ProjectID = "gateway"
	ProjectJobs        ProjectID = "jobs"
	ProjectMaintenance ProjectID = "maintenance"
)
