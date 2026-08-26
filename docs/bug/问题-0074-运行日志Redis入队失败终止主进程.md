# BUG-0074 运行日志 Redis 入队失败终止主进程

## 基本信息

- 编号：BUG-0074
- 状态：已修复（待生产验证）
- 严重程度：P1
- 发现时间：2026-07-14
- 发现方式：生产日志与 macOS unified log 联合诊断
- 模块：后端 / 运行日志 / Redis Stream / 进程稳定性
- 关联计划：2026-07-14 新需求批次上线
- 关联 bug：BUG-0072

## 问题概述

- 现象：生产主进程在 watchdog 未执行终止、系统无 OOM 的情况下以 `exit(1)` 退出并被 launchd 拉起。
- 期望：运行日志索引是可从文件增量重建的派生数据；单条 Redis Stream 入队失败应被记录和计数，不能中断业务请求服务。
- 实际：`enqueueRuntimeLogLine` 的 fire-and-forget Promise 使用 `scheduleProcessFatalError`，一次 `XADD` 失败会在 next tick 抛出未捕获异常。
- 影响范围：server、DB service 或 worker 任一运行日志生产者都可能因 Redis 瞬时错误退出。

## 根因分析

- 2026-07-14 04:08:58，生产 `launchd.err` 先出现 `runtime-log-index` XADD 失败；04:09:00，系统明确记录主进程以 `exit(1)` 退出。
- 同时段 Redis 没有 OOM、淘汰或拒绝连接，watchdog 也没有发出终止，因此排除“Redis 内存上限”和“watchdog 误杀”。
- 队列统一边界测试错误地把所有 Redis Stream 生产者都定义为 fail-fast，忽略运行日志索引可重建、非业务事实的属性。
- 第一轮非 fatal 修复只接住了 `XADD` Promise 拒绝；node-redis 在已经连接后断线时默认会把新命令放入 offline queue。若 Redis 长时间不可用，运行日志生产者仍可能累积无界 pending Promise 和 payload，形成新的内存与事件循环压力。

## 修复方案

- 运行日志索引仍禁止失败后回退 IPC 或本地队列，避免形成双事实源。
- 单次 XADD 失败改为丢弃该条派生索引，累计 `redisEnqueueFailureCount`、`droppedCount`、最后错误时间和脱敏后的非空错误信息。
- 错误直接写入 stderr，避免通过应用 logger 再次触发运行日志索引形成递归。
- runtime-log 使用独立 Redis producer client，固定 `disableOfflineQueue=true`、`commandsQueueMaxLength=64` 和 1 秒连接超时；共享 Redis client 与使用记录、审计、操作日志等业务事实队列保持原行为。
- 应用层同时限制最多 64 条、4 MiB 在途 XADD，并设置 1.5 秒命令 deadline。连接未 ready、容量饱和或命令超时立即按原因计数并丢弃派生索引；命令超时会销毁专用连接，避免底层 Promise 继续滞留。
- 失败日志只输出前 10 次及之后每 100 次，持续断线时保持可观测但不形成 stderr 写放大。
- 使用记录、审计日志、操作日志等业务事实队列的失败策略不随本修复改变。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 边界回归 | 空消息且带错误 code 的 XADD 失败 | `pnpm --filter juhe-ai-backend test:runtime-log-redis-enqueue-failure-boundary` | 计数和错误可见，不触发 fatal | 通过 | 已通过 |
| 断线与容量 | pending Promise、连接未 ready、XADD 超时 | 同上 | offline queue 关闭；数量和字节有界；断线 / 饱和 / 超时分类丢弃 | 通过 | 已通过 |
| Redis 契约 | 高性能队列边界 | `pnpm --filter juhe-ai-backend test:performance-redis-boundary`、`test:redis-stream-queue` | 不回退本地队列，仅运行日志允许非 fatal | 通过 | 已通过 |
| 类型与构建 | 全工作区 | `pnpm typecheck`、`pnpm build` | 通过 | 通过 | 已通过 |
| 生产观察 | 真实流量固定 PID | 连续观察 20 至 30 分钟 | 单次运行日志入队错误不导致进程退出 | 待执行 | 待验证 |

## 下次遇到

- 先同时核对 watchdog、launchd/unified log、Redis OOM/eviction 和应用 stderr，区分外部终止、系统回收与主动 `exit(1)`。
- 派生索引与不可丢业务事实必须分别定义失败策略，不能以“队列统一”为由共享进程级 fatal。
- Redis 错误日志即使上游 message 为空，也必须保留错误类型、code、计数和时间。
- fire-and-forget 外层 `.catch()` 只能处理最终拒绝，不能约束依赖库内部 offline queue；派生队列必须同时约束客户端配置、应用在途数量、在途字节和命令 deadline。

## 完成总结

- 完成时间：2026-07-14
- 结论：根因已在运行日志 Redis Stream 生产者边界修复，待本批次生产长窗口验证。
- 后续建议：单独评估 runtime-log Stream 写删与 AOF rewrite 频率，不能用提高内存上限代替业务流量治理。
