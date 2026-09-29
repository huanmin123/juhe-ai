// Package advisorylock 集中管理跨服务共享的 PostgreSQL advisory 事务锁键。
// 键值一旦随数据形态发布即不可变更（只读用途）。
package advisorylock

// AccountListDirty 序列化 juhe_business.account_list_availability_dirty 的
// 全部写入方：gateway 脏标记、jobs 投影维护（Enqueue*/ClaimDirty/Apply*/
// ReleaseForReplay，4 路并行 batch worker）、J3a proxy 回写与 J1 账户健康
// 投影（BUG-0237 补入），以及 proxy_profiles/accounts 等触发表 UPDATE 经
// 库端触发器汇聚函数的隐式 dirty 写入。这些事务对 dirty 行的加锁顺序互
// 不相同，并发交叉会形成 40P01 死锁环（问题-0184/0192/0237 活体取证确认）。
//
// 锁序契约：能控制事务结构的应用侧写事务，必须把本键的
// pg_advisory_xact_lock 放在事务首条语句（先取锁后取行锁），且只在 PG
// 方言执行；事务再持有其他 advisory 键时（如 J1 的 health-projection 键）
// 本键必须最先取。注意库端触发器路径物理上只能在事务中途取本键（函数体
// 内），因此"首语句"约定只对应用侧成立——未持本键先拿行锁的应用事务与
// 持本键事务仍可能反序成环（BUG-0237），新增触发表写事务时必须评估是否
// 先取本键，详见 docs/bug/问题-0237。
const AccountListDirty int64 = 7001001
