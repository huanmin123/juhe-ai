# BUG-0129 performance 进程指标有限 SCAN 漏采

## 状态

已修复，待部署。

## 现象

performance 生产拓扑实际运行 3 个 Gateway、对应 DB service、control 和多个后台 worker，但系统性能趋势只出现个别 worker。Gateway 的瞬时事件循环峰值因此无法形成可信的容量判断。

## 根因

每个进程均正常发布带短 TTL 的 Redis 样本。读侧为了限制开销，只执行最多 4 页 `SCAN MATCH ... COUNT 64`。Redis `SCAN` 的 `COUNT` 不是匹配结果数量保证；当注册项散布在大量业务 key 中时，有限页扫描随机漏掉绝大多数注册项。

stats worker 随后的 IPC 补采只能访问本进程直接管理的 worker，无法访问由 launchd 单独托管的 Gateway 和 DB service，所以漏读不能被补齐。

## 修复

- 新增独立、短 TTL 的有序集合活跃索引。
- publisher 以 Redis `TIME` 为统一观测时钟，原子刷新样本 key、索引成员和索引 TTL，同时清理过期成员、限制 512 成员基数并在连续失败时最多退避 10 秒。
- reader 按 Redis 观测时间从索引查询活跃成员，批量读取样本，并清理过期成员；本地 payload 时间只校验格式，最终持久化时间使用 Redis score。
- 删除以有限页全局 `SCAN` 作为正常发现机制的做法。
- 注册表按精确 worker 副本和 Gateway/DB service 同 ID 配对核验完整性；官方 macOS 安装先逐个升级并确认 Gateway/DB service 注册，最后升级 control/worker 并确认完整拓扑。

## 防复发

- 回归测试模拟大量无关业务 key，覆盖默认 14 角色、合法最大 132 角色、新旧双版本 264 成员、600 次实例 churn 与当前拓扑恢复。
- 测试夹具在收到 `SCAN` 时直接失败，保证实现不会静默退回全键空间扫描。
- 授权测试 Redis smoke 真实执行 Lua，验证 20 秒样本 TTL、60 秒索引 TTL、偏差一小时的 payload 由 Redis 时间覆盖、过期成员清理、512 成员上限与清理零残留。
- PostgreSQL 批事务回归验证合法最大 132 个动态角色以 266 条 SQL 在一个事务中完整写入。

## 关联

- [执行计划](../plans/计划-20260727T131815566Z-performance进程指标活跃索引修复.md)
- [核心功能设计](../functions/核心功能设计.md)
