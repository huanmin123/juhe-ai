# PLAN-0056 ops-worker 外部 I/O 并发控制

## 基本信息

- 编号：PLAN-0056
- 状态：已完成
- 创建时间：2026-06-23
- 更新时间：2026-06-23
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 后台任务 / ops-worker / 账号探测 / 依赖 / 文档 / 验证

## 需求目标

- 背景：后台 worker 已收敛为 `ingest-worker`、`stats-worker`、`ops-worker` 三类常驻角色；用户希望轻量 I/O 任务能在 worker 内受控并发，避免一条一条串行处理，同时不要把重统计混入轻运维。
- 目标：在保持现有队列快照、重试、硬上限和 DB service 写回边界不变的前提下，引入轻量并发门禁，让账号健康检测、账号级 / Key 级冷却复测和质量失败预检具备保守并发能力。
- 交付物：后端依赖、公共队列实现、ops-worker 队列并发设置、新回归脚本、计划与后台任务文档、验证记录。

## 范围边界

### 本次包含

- [x] 引入 `p-limit` 作为公共 `createRetryQueue` 的异步执行门禁。
- [x] 保留 `createRetryQueue` 现有对外接口、snapshot、重试策略、key 去重和动态 `setConcurrency()` 能力。
- [x] 为 `account-health-check`、`cooldown-account-retest`、`account-api-key-cooldown-retest`、`account-quality-failure-precheck` 暴露队列并发 setter。
- [x] 在 `account-probe-jobs` 中按批量大小派生保守并发：普通外部 I/O 上限 10，full diagnostic 预检上限 3。
- [x] 补充 retry queue 并发上限回归脚本，并跑相邻账号探测 / 队列回归。

### 本次不包含

- `p-queue`：暂不引入；项目已有 `createRetryQueue` 承载重试、快照和队列状态，替换为通用队列会丢失本地运行态语义。
- `bottleneck`：暂不引入；当前没有按供应商、账户或 API Key 做令牌桶 / reservoir 限流的需求，后续需要细粒度速率策略时再评估。
- 新增系统设置：暂不新增 UI / schema 字段；本次按现有 batch size 派生并发，避免把运维配置复杂度提前暴露给 1000 人以内轻量部署。
- 拆新 worker：不改变三角色拓扑；统计和热写入仍分别归 `stats-worker`、`ingest-worker`。

## 关联文档

- 后台任务说明：`docs/architecture/backend/后台任务使用说明.md`
- Worker 拆分设计：`docs/architecture/backend/后台Worker多角色拆分设计.md`
- 需求计划：`docs/plans/计划-0055-后台Worker三角色收敛.md`

## 方案概述

- 方案原则：只在公共 retry queue 执行层增加并发门禁，不替换业务队列模型；外部 I/O 可并发，SQLite 写入仍通过既有 owner / DB service 路径。
- 数据变化：无数据库 schema 变化。
- 接口变化：无 HTTP API 变化；新增内部队列并发 setter。
- 后端变化：`createRetryQueue` 使用 `p-limit` 包裹 `runItem`，`setConcurrency()` 同步更新 limiter concurrency；ops 账号探测任务在入队前设置队列并发。
- 数据处理策略：无历史兼容分支，无运行时迁移。

## 执行拆解

- [x] 更新相关长期文档
- [x] 引入后端依赖 `p-limit`
- [x] 实现公共 retry queue 并发门禁
- [x] 实现 ops-worker 账号探测队列并发派生
- [x] 补充回归脚本和 package script
- [x] 运行类型检查和相邻回归
- [x] 更新完成总结

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend exec tsc --noEmit` | TypeScript 类型检查通过 | 已通过 | 通过 |
| 功能主流程 | retry queue 并发门禁 | `pnpm --filter juhe-ai-backend test:retry-queue-concurrency-limit` | 初始并发 2、动态提升 3、snapshot 正确、最终清空 | 已通过 | 通过 |
| 回归场景 | retry queue 线性扫描 | `pnpm --filter juhe-ai-backend test:retry-queue-linear-scan` | 选择队列项和 snapshot 不退化为数组排序 | 已通过 | 通过 |
| 回归场景 | 账号健康检测 | `pnpm --filter juhe-ai-backend test:account-health-check` | 健康检测语义不受并发改造影响 | 已通过 | 通过 |
| 回归场景 | 冷却账户复测 | `pnpm --filter juhe-ai-backend test:cooldown-retest-recovery` | 冷却恢复链路通过 | 已通过 | 通过 |
| 回归场景 | 手动账号测试队列 | `pnpm --filter juhe-ai-backend test:account-test-task-boundary` | 手动测试仍由 worker 队列执行 | 已通过 | 通过 |
| 回归场景 | 账号质量刷新 / 失败预检 | `pnpm --filter juhe-ai-backend test:account-quality-refresh` | 质量刷新和频繁失败预检语义通过 | 已通过 | 通过；同步修正过期源码断言为 DB service typed command |
| 回归场景 | 账户内 API Key 失败保护 | `pnpm --filter juhe-ai-backend test:account-api-key-failure-guard` | Key 级失败保护不受复测队列并发影响 | 已通过 | 通过 |
| 回归场景 | API Key 网关 mock AI | `pnpm --filter juhe-ai-backend test:account-api-key-gateway-mock-ai` | API Key 池调度与失败恢复通过 | 已通过 | 通过 |
| 回归场景 | 后台队列健康 | `pnpm --filter juhe-ai-backend test:background-queue-health` | worker 本地队列和 IPC 健康快照正常 | 已通过 | 通过 |
| 回归场景 | worker 本地队列硬上限 | `pnpm --filter juhe-ai-backend test:worker-local-queue-limit` | 本地队列硬上限仍有效 | 已通过 | 通过 |
| 回归场景 | worker 拓扑 smoke | `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke` | 常驻 worker 仍为 ingest / stats / ops | 已通过 | 通过 |
| 未覆盖说明 | 真实上游网络并发压测 | 真实 OpenAI / 代理凭据环境下另行验证 | 需要真实凭据和网络环境 | 不适用 | 本次只跑 mock / 本地回归 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-23 | 进行中 | AI | 梳理 `createRetryQueue` 使用点，确认 ops-worker 外部 I/O 队列适合在公共队列层加并发门禁。 |
| 2026-06-23 | 进行中 | AI | 引入 `p-limit`，保留项目自有 retry queue 语义；未引入 `p-queue`、`bottleneck`。 |
| 2026-06-23 | 已完成 | AI | 完成实现、文档和验证；相邻回归通过。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-23 | 使用 `p-limit`，不替换 `createRetryQueue` | `createRetryQueue` 已承载运行态 snapshot、key 去重、重试、onSuccess/onFailure 回调和动态并发；`p-limit` 更适合做轻量执行门禁 | 依赖小，改动面小，保留现有队列观测能力 |
| 2026-06-23 | 普通 ops 外部 I/O 并发上限 10，full diagnostic 预检上限 3 | 1000 人以内部署下要利用 I/O 等待，但不能让探测任务压住 OAuth 保活和业务库写回 | 账号恢复速度提升，同时控制上游请求风暴风险 |
| 2026-06-23 | 暂不做设置项 | 当前已有 batch size 可约束每轮规模；新增设置会扩大 UI、存储和运维复杂度 | 后续如果真实运行需要，可再把上限抽成系统设置 |

## 验收标准

- [x] 后端能安装并类型检查通过。
- [x] 公共 retry queue 的 snapshot、运行中计数、待运行计数和动态并发调整保持正确。
- [x] ops-worker 账号健康检测、账号级 / Key 级冷却复测、质量失败预检能从单并发改为保守受控并发。
- [x] 不改变三 worker 拓扑、DB service 写回边界和 SQLite 单写者边界。
- [x] 必要文档和计划已同步。

## 验证记录

- 类型检查：已执行，命令：`pnpm --filter juhe-ai-backend exec tsc --noEmit`
- 单项验证：已执行，命令：`pnpm --filter juhe-ai-backend test:retry-queue-concurrency-limit`
- 相邻回归：已执行，覆盖 retry queue、账号健康检测、冷却复测、手动账号测试、账号质量刷新、API Key 失败保护、后台队列健康、本地队列硬上限和 worker 拓扑 smoke。
- 手动验证：未执行真实上游压测；本次无真实凭据和网络环境要求。
- 未验证项：真实 OpenAI / 代理网络下的高并发延迟与错误率，需要上线观察或独立压测。

## 风险与注意事项

- 上游请求风暴：并发上限当前保守固定，普通 I/O 最多 10，full diagnostic 最多 3；后续如账号量明显增长再引入设置项或供应商级限流。
- DB service 写回压力：并发探测完成后仍会写回业务库；写回路径保持 typed command，避免绕过单写者。
- p-limit 升级：当前使用 `7.3.0`，依赖 Node `>=20`；项目运行环境已高于该要求，后续升级需复跑 retry queue 回归。

## 完成总结

- 完成时间：2026-06-23
- 实际完成内容：后端引入 `p-limit`；公共 `createRetryQueue` 增加执行门禁；ops 账号探测队列改为按批量派生并发；补充并发回归和文档。
- 主要改动位置：`backend/src/shared/retry-queue.ts`、`backend/src/modules/background/account-probe-jobs.ts`、`backend/src/modules/background/*retest*.service.ts`、`backend/src/modules/background/account-health-check.service.ts`、`backend/src/scripts/regression/retry-queue-concurrency-limit-regression.ts`、`backend/package.json`、`pnpm-lock.yaml`。
- 验证结果：后端类型检查和相关回归均通过。
- 后续建议：如果真实运行发现某供应商或某账号池需要更细粒度速率控制，再评估 `bottleneck` 或项目内供应商级限流器；暂不需要 `p-queue`。
