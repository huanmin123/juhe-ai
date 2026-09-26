// Package advisorylock 集中管理跨服务共享的 PostgreSQL advisory 事务锁键。
// 键值一旦随数据形态发布即不可变更（只读用途）。
package advisorylock

// AccountListDirty 序列化 juhe_business.account_list_availability_dirty 的
// 全部写入方：gateway 脏标记、jobs 投影维护（Enqueue*/ClaimDirty/Apply*/
// ReleaseForReplay，4 路并行 batch worker）、以及 proxy_profiles UPDATE
// 经库端触发器 account_list_availability_proxy_dirty_trigger 的隐式 dirty
// 写入（J3a 投影路径）。这些事务对 dirty 行的加锁顺序互不相同，并发交叉
// 会形成 40P01 死锁环（问题-0184 活体取证确认）；各写事务以本键
// pg_advisory_xact_lock 作为首语句即可全局串行化，消除环。
//
// 注意：必须放在各写事务的首条语句（先取锁后取行锁），且只在 PG 方言执行。
const AccountListDirty int64 = 7001001
