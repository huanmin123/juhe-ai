// Package advisorylock 集中管理跨服务共享的 PostgreSQL advisory 事务锁键。
// 键值一旦随数据形态发布即不可变更（只读用途）。
package advisorylock

import (
	"context"
	"database/sql"
	"fmt"
)

// AccountListDirty 序列化 juhe_business.account_list_availability_dirty 的
// 全部写入方：gateway 脏标记、jobs 投影维护（Enqueue*/ClaimDirty/Apply*/
// ReleaseForReplay，4 路并行 batch worker）、J3a proxy 回写与 J1 账户健康
// 投影（BUG-0237 补入），以及 proxy_profiles/accounts 等触发表 UPDATE 经
// 库端触发器汇聚函数的隐式 dirty 写入。这些事务对 dirty 行的加锁顺序互
// 不相同，并发交叉会形成 40P01 死锁环（问题-0184/0192/0237 活体取证确认）。
//
// 锁序契约：能控制事务结构的应用侧写事务，必须把本键的
// pg_advisory_xact_lock 放在事务首条取锁语句（先取锁后取行锁；其前仅允许
// lock_timeout 等事务级配置语句，见 LockAccountListDirty），且只
// 在 PG 方言执行；事务再持有其他 advisory 键时（如 J1 的 health-projection
// 键）本键必须最先取。注意库端触发器路径物理上只能在事务中途取本键（函数体
// 内），因此"首语句"约定只对应用侧成立——未持本键先拿行锁的应用事务与
// 持本键事务仍可能反序成环（BUG-0237），新增触发表写事务时必须评估是否
// 先取本键，详见 docs/bug/问题-0237。
const AccountListDirty int64 = 7001001

// UpgradeOrchestration 串行化升级编排结构变更：`maintenance upgrade` 全程持有
// 会话级 pg_advisory_lock（非事务锁，跨多个迁移事务），并与 `--ensure-schema` /
// `--seed`（runStorageBootstrap 开期为同一把锁）互斥，防止发布脚本与人工命令
// 同时改结构或写账本（升级兼容与迁移账本设计 §9.5）。不覆盖 15 个旧 flag 迁移
// 命令——与 upgrade 并行执行它们属操作纪律禁止项（升级指南披露）。
// 键值一经发布不可变更；与 AccountListDirty 同段分配，避开 J3a/J3b 的
// 732946110271044015/…016。
const UpgradeOrchestration int64 = 7001002

// ListDirtyLockWaitTimeout 是应用侧投影写事务取 AccountListDirty 锁的等待
// 上限（经事务级 lock_timeout 生效）。BUG-0313：主机资源饱和时持锁事务可
// 被饿死在事务中间不放手（生产曾观测单个事务持锁 24 分钟、37+ 事务排队），
// 等待方超过本上限即以 55P03 失败本轮。该失败对投影写是安全的：dirty 行/
// claim 持久留在表内，任务下一轮自然补上（用户裁决：投影误差可接受、
// 重试自愈即可）；与 F1/F4 的 2s lock_timeout 快失败设计（BUG-0254）同口径。
const ListDirtyLockWaitTimeout = "2s"

// TxExecContext 是事务内取锁所需的最小执行端口（*sql.Tx 与仓储侧 txLike
// 接口均满足）。
type TxExecContext interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// LockAccountListDirty 在事务内取 AccountListDirty 序列化锁，取锁前先以
// SELECT set_config('lock_timeout', …, true) 限定本轮等待（is_local=true 与
// SET LOCAL 等价，只作用于当前事务、提交/回滚自动还原；取函数形式是为了让
// SQLite 测试夹具可经 no-op 标量注册放行，PG 语义不变）。调用方须保证本调
// 用处于事务首条取锁语句位置（锁序契约见 AccountListDirty），且仅在 PG 方言
// 调用；等待超过 ListDirtyLockWaitTimeout 返回包裹 55P03 的错误，由调用方按
// 既有重试路径自愈。
func LockAccountListDirty(ctx context.Context, tx TxExecContext) error {
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', '`+ListDirtyLockWaitTimeout+`', true)`); err != nil {
		return fmt.Errorf("设置 AccountListDirty 取锁等待上限失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, AccountListDirty); err != nil {
		return fmt.Errorf("获取 AccountListDirty 序列化锁失败（等待超过 %s，本轮放弃待下轮重试）: %w", ListDirtyLockWaitTimeout, err)
	}
	return nil
}
