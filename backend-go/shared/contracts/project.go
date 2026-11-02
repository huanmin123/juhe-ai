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
// 表达；upgrade 依据 dbRevision < SchemaRevision 触发加法相补列。当前值 14。
const SchemaRevision int64 = 14

type ProjectID string

const (
	ProjectGateway     ProjectID = "gateway"
	ProjectJobs        ProjectID = "jobs"
	ProjectMaintenance ProjectID = "maintenance"
)
