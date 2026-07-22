# PLAN-0055 后台 Worker 三角色收敛

## 基本信息

| 字段 | 内容 |
| --- | --- |
| 编号 | PLAN-0055 |
| 状态 | 已完成 |
| 创建时间 | 2026-06-23 |
| 需求来源 | 用户反馈当前 worker 过多，希望按高实时性和重任务重新收敛 |
| 执行者 | AI |
| 关联模块 | 后端 / 后台任务 / 统计 / 系统监控 / SQLite / 前端统计页 / 文档 / 验证 |

## 需求目标

将当前后台常驻 worker 从旧多角色拓扑收敛为三类：

- `ingest-worker`：热写入和事实落库，优先保障计费、审计和日志事实源。
- `stats-worker`：重统计、窗口刷新、系统采样和表监控。
- `ops-worker`：轻量运维和外部 I/O，包括账号测试、复测、OAuth 保活、代理检测、可用时段同步、授权到期和删除清理协调。

DB service 保持独立。`temporary-maintenance-worker` 只保留为按需历史任务入口，不属于常驻拓扑。

## 范围边界

本次做：

- 更新 worker role、supervisor、IPC 路由、运行态 snapshot、系统监控进程角色和队列健康口径。
- 合并旧 `probe-worker` 和 `maintenance-worker` 的轻量任务到 `ops-worker`。
- 保留重统计在 `stats-worker`，避免单 worker 被统计窗口压住。
- 更新前端统计页和设置页文案 / 类型，去掉旧常驻 worker 展示。
- 更新后台任务架构文档、接口契约和相关回归脚本。

本次不做：

- 不引入外部队列、分布式锁或多实例任务归属。
- 不新增数据库兼容迁移逻辑。
- 不拆分 stats 任务租约或 shard 并行，后续只有在指标证明需要时另立计划。
- 不移除 `temporary-maintenance-worker` 源码入口；它不是当前常驻拓扑。

## 方案

| 类别 | 角色 | 任务 |
| --- | --- | --- |
| 热写入 | `ingest-worker` | usage、audit、operation、public API log、runtime log、runtime log file import、record maintenance、dataset / usage shard 清理 |
| 重统计 | `stats-worker` | system metrics、process event loop、usage aggregation、client IP aggregation、TopN、overview、range、authorization windows、account quality、table monitor、stats cleanup |
| 轻运维 | `ops-worker` | manual account test、account health check、cooldown retest、API Key cooldown retest、OAuth refresh、proxy latency refresh、availability schedule sync、authorization expiry sweep、expired deleted account cleanup orchestration |

执行规则：

- `ops-worker` 内允许外部 I/O 受控并发，但必须有批量上限、超时和批间让出。
- `stats-worker` 内重窗口分阶段短事务执行，不能实时扫描明细表服务页面。
- `ingest-worker` 内维护高优先级写入和低优先级维护队列，避免清理任务压住计费事实。
- 所有非 owner 写库必须经 DB service、dataset writer 或 stats writer typed operation。

## 执行拆解

- [x] 复核当前 worker 拓扑、后台任务 registry、IPC 路由和运行态接口。
- [x] 将 supervisor 常驻角色收敛为 `ingest-worker`、`stats-worker`、`ops-worker`。
- [x] 将旧探测和维护定时任务归入 `ops-worker`。
- [x] 收紧 `BackgroundWorkerProcessRole`、系统指标进程角色和 runtime snapshot 字段。
- [x] 同步前端统计页 / 设置页类型和中文文案。
- [x] 更新后台任务使用说明和 worker 拆分设计文档。
- [x] 更新接口契约、存储说明和计划索引。
- [x] 运行后端类型检查和 worker 相关回归脚本。
- [x] 运行前端类型检查。
- [x] 回填验证记录和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend exec tsc --noEmit` | 无 TypeScript 错误 | 已通过 | 2026-06-23 最终验证通过 |
| 类型检查 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | 无 TypeScript 错误 | 已通过 | 2026-06-23 通过 |
| 回归 | worker 拓扑 | `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke` | 只拉起三类常驻 worker | 已通过 | 通过，roles 为 server / ingest / stats / ops / db-service |
| 回归 | 旧角色退役 | `pnpm --filter juhe-ai-backend test:background-metrics-worker-role` | 旧常驻角色不再出现在代码路径和 registry 默认角色中 | 已通过 | 文件名沿用历史脚本 |
| 回归 | runtime 不可观测契约 | `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract` | 只暴露 ingest / stats / ops 可用性字段 | 已通过 |  |
| 回归 | 进程事件循环角色 | `pnpm --filter juhe-ai-backend test:system-metrics-process-latest` | 角色固定为 server / ingest / stats / ops / db-service | 已通过 |  |
| 回归 | IPC snapshot 当前角色 | `pnpm --filter juhe-ai-backend test:background-ipc-snapshot-current-only` | snapshot 状态不包含旧常驻角色 | 已通过 |  |
| 回归 | 队列健康 | `pnpm --filter juhe-ai-backend test:background-queue-health` | ingest / ops 队列健康口径正确 | 已通过 |  |
| 回归 | worker 本地队列边界 | `pnpm --filter juhe-ai-backend test:worker-local-queue-limit` | record maintenance 仍归 ingest，非 owner 不能本地消费 | 已通过 |  |
| 回归 | SQLite 写 owner | `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary` | 非 owner 写入边界仍受保护 | 已通过 |  |
| 回归 | job registry | `pnpm --filter juhe-ai-backend test:background-job-registry` | 所有 worker IPC 和队列入口已登记 | 已通过 | 补齐 dataset / stats / DB service typed IPC 登记 |
| 回归 | ops 轻运维任务 | `test:account-health-check`、`test:openai-oauth-access-token-refresh`、`test:api-key-availability-schedule`、`test:account-availability-schedule`、`test:resource-authorization-expiry-batch-boundary`、`test:cooldown-retest-recovery`、`test:deleted-account-related-cleanup` | 合并到 ops 后主流程不退化 | 已通过 | 2026-06-23 通过 |

## 进度记录

| 日期 | 状态 | 记录 |
| --- | --- | --- |
| 2026-06-23 | 进行中 | 明确采用三类常驻 worker：热写入 `ingest-worker`、重统计 `stats-worker`、轻运维 `ops-worker`；一个 worker 不可取，因为统计任务仍偏重。 |
| 2026-06-23 | 进行中 | 已完成后端 role、supervisor、IPC、runtime snapshot、系统指标角色、前端类型和统计页文案的主要代码改造；后端 `tsc --noEmit` 已通过一次。 |
| 2026-06-23 | 进行中 | 已更新后台任务使用说明和 worker 拆分设计文档，开始补接口契约和验证记录。 |
| 2026-06-23 | 已完成 | 已补齐 registry typed IPC 登记、ingest worker 退出时 record-maintenance 有限 flush、拓扑 smoke 临时库 schema 初始化，并完成全部验证。 |

## 验收标准

- 生产常驻后台 worker 只有 `ingest-worker`、`stats-worker`、`ops-worker` 三类。
- 旧 `metrics-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` 不再作为常驻 worker 启动、路由或前端展示对象。
- 计费 / 使用记录等热写入仍优先进入 ingest，不被统计或 ops 外部 I/O 阻塞。
- 统计、监控和窗口刷新仍独立在 stats，不和轻运维合并。
- 系统监控接口、前端统计页和队列健康展示字段与三角色拓扑一致。
- 必要类型检查和回归脚本通过，无法执行项有明确说明。

## 风险与注意事项

- `ops-worker` 合并轻运维任务后，需要继续观察账号测试、OAuth 保活和冷却复测队列；如果外部 I/O 积压长期影响恢复速度，再考虑拆分账号测试 / 探测 worker。
- `stats-worker` 仍是最重角色；如果窗口刷新影响系统采样，应优先优化窗口阶段、索引和写事务，再考虑任务租约或拆分。
- `ingest-worker` 关系到计费事实源；低优先级清理任务必须继续让位给 usage、日志和 dataset writer 高优先级写入。
- 旧计划和历史文档可能仍提到旧角色，当前事实以本计划和后台架构文档为准。

## 验证记录

2026-06-23 已通过：

- `pnpm --filter juhe-ai-backend exec tsc --noEmit`
- `pnpm --filter juhe-ai-frontend typecheck`
- `pnpm --filter juhe-ai-backend test:background-metrics-worker-role`
- `pnpm --filter juhe-ai-backend test:system-metrics-process-latest`
- `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract`
- `pnpm --filter juhe-ai-backend test:background-ipc-snapshot-current-only`
- `pnpm --filter juhe-ai-backend test:background-queue-health`
- `pnpm --filter juhe-ai-backend test:worker-local-queue-limit`
- `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary`
- `pnpm --filter juhe-ai-backend test:audit-log-hot-search`
- `pnpm --filter juhe-ai-backend test:background-job-registry`
- `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`
- `pnpm --filter juhe-ai-backend test:background-parent-ipc-event-loop`
- `pnpm --filter juhe-ai-backend test:account-health-check`
- `pnpm --filter juhe-ai-backend test:openai-oauth-access-token-refresh`
- `pnpm --filter juhe-ai-backend test:api-key-availability-schedule`
- `pnpm --filter juhe-ai-backend test:account-availability-schedule`
- `pnpm --filter juhe-ai-backend test:resource-authorization-expiry-batch-boundary`
- `pnpm --filter juhe-ai-backend test:cooldown-retest-recovery`
- `pnpm --filter juhe-ai-backend test:deleted-account-related-cleanup`

旧角色扫描结果：`metrics-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`log-worker`、`usage-ingest-worker` 只保留在退役说明、临时维护入口说明和回归脚本负向断言里；当前运行代码不再使用它们作为常驻 worker。

## 完成总结

已完成后台 worker 三角色收敛：

- 常驻 supervisor 只拉起 `ingest-worker`、`stats-worker`、`ops-worker`。
- `ingest-worker` 承接热写入、运行日志导入和 record maintenance，退出时会等待 usage、record-maintenance、audit 等有限 flush。
- `stats-worker` 承接系统监控、统计聚合和重窗口。
- `ops-worker` 承接手动账号测试、健康检测、冷却复测、OAuth 保活、代理检测、可用时段同步、授权到期和删除清理协调。
- 系统监控接口、DB service runtime snapshot、前端统计页和运行态类型已对齐三类 worker。
- 后台任务使用说明、worker 拆分设计、接口契约、SQLite 存储说明、核心功能设计和计划索引已同步。
