# BUG-0040 Go 公开 API Key 写入未触发网关缓存失效

## 基本信息

- 编号：BUG-0040
- 状态：已修复（待真实环境验证）
- 严重程度：P1
- 发现时间：2026-07-10
- 发现方式：Node 转 Go 契约对照 / 子 agent 审计
- 模块：后端 / Go / 公开接口 / API Key / Redis cache / Redis runtime state / 网关额度
- 关联计划：[PLAN-0081 Node 转 Go 渐进减法迁移](../plans/计划-0081-Node转Go渐进减法迁移.md)
- 关联 bug：[BUG-0019 授权配额快照跨进程失效缺失](问题-0019-授权配额快照跨进程失效缺失.md)
- 责任人：主 agent / 维护者

## 问题概述

- 现象：Go W1b 公开 API Key create/update/delete 成功写入 PostgreSQL 后，没有刷新 Node 网关 runtime cache、API Key validation shared cache version 或 API Key quota cache。
- 期望：对齐 Node 当前写后顺序与错误边界：
  - create：提交后依次发布 `gateway_runtime_cache` 和带 `apiKeyId` 的 `api_key_quota_cache`，两项 best-effort，不刷新 validation version。
  - update/delete：提交后先刷新 `gateway:api-key-validation` shared cache version，失败向调用方返回；成功后依次 best-effort 发布 runtime 和 quota 失效。
- 实际：旧 Go 事务成功后直接返回，Node gateway 可能继续使用旧状态、路由、额度或已删除 Key 的缓存结果。
- 影响范围：Go opt-in `/__aipublic__/api-key/add`、`update`、`delete` 与 Node gateway 跨进程缓存一致性。

## 复现步骤

1. 让 Node gateway 缓存某个 API Key 的 validation 或 quota 结果。
2. 通过 Go W1b 接口更新或删除该 Key。
3. 旧 Go 只提交 PostgreSQL，不改变 shared cache version，也不发布 runtime/quota topic。
4. Node gateway 在缓存 TTL 内继续读取旧结果。

## 环境信息

- 分支 / 版本：`feature/20260706-go`，W1b public API Key CRUD Go opt-in 阶段。
- 数据状态：PostgreSQL、Redis cache/state/queue 使用同一部署 namespace。
- 浏览器 / 系统 / Node 版本：不适用；跨运行时写后缓存一致性问题。
- 是否稳定复现：是。

## 根因分析

- 表象：API Key 接口返回成功，但网关行为没有及时变化。
- 真实根因：
  - `publicapikeys.Service` 没有 invalidator 依赖。
  - `gatewaycache.SystemAccountInvalidator` 的 validation cache clear 和 runtime publisher 没有公开为 API Key service 可复用接口。
  - Go public API 生产装配只注入 store/transactor，没有注入共享 cache/state invalidator。
- 为什么会发生：W1b 纵切面最初只迁移 API Key 业务表 CRUD，没有迁移 Node repository 的事务后 validation/runtime/quota 副作用。

## 修复方案

- 修改点：
  - `gatewaycache` 暴露 `InvalidateAPIKeyValidationCache` 和 `InvalidateGatewayRuntime`，继续返回真实 Redis 错误。
  - `publicapikeys.Options` 新增可选 `APIKeyGatewayCacheInvalidator`。
  - create 提交后按 runtime -> quota 调用，错误 best-effort。
  - update/delete 提交后按 validation -> runtime -> quota 调用；validation 失败按 Node 语义传播并停止后续通知，runtime/quota 错误 best-effort。
  - 所有成功 update 都触发失效，不按字段差异短路。
  - production server 在 management API 或 public API 任一启用时构造共享 invalidator，并将其注入 public API Key service。
  - `JUHE_AI_PUBLIC_API_ENABLED=true` 新增 Redis cache fail-fast 依赖，避免启用后无法刷新 validation shared cache version。
  - W1b maintenance smoke 增加真实 Redis cache 连接和 invalidator 装配；当前 smoke 仍只调用 group list，不把 API Key 失效记为真实端到端通过。
- 行为影响：
  - create/update/delete 后 Node gateway 能通过 runtime state topic 和 shared cache version 观察变化。
  - update/delete 如果 validation version 写入失败，数据库已经提交但接口返回错误；这是 Node 当前精确语义，调用方不得盲目重试，应先查询最终状态。
- 发布异常处理：Redis cache/state 不可用时禁止启用 Go public API；已提交写入但 validation 刷新失败时，先关闭 `JUHE_AI_PUBLIC_API_ENABLED`，恢复 Redis 后核对数据库最终状态和 cache version。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| Service 定向测试 | create/update/delete 调用顺序与错误语义 | `go test ./internal/modules/publicapikeys -count=1` | transaction commit 后按 Node 顺序调用；validation 传播；runtime/quota best-effort | 通过 | 已通过 |
| Gateway cache 定向测试 | validation version 与 runtime payload | `go test ./internal/modules/gatewaycache -count=1` | Node 兼容 key、TTL、topic、reason 和错误透传 | 通过 | 已通过 |
| App/config/maintenance | 生产注入与 Redis cache fail-fast | `go test ./internal/config ./internal/app ./internal/maintenance ./internal/httpapi -count=1` | public API 要求 cache/state/queue，生产和 smoke 注入 invalidator | 通过 | 已通过 |
| 目标 race | cache、service、app、config 并发回归 | `go test -race ./internal/modules/gatewaycache ./internal/modules/publicapikeys ./internal/app ./internal/config -count=1` | 无 race | 通过 | 已通过 |
| Go 全量测试 | 相邻模块回归 | `go test ./... -count=1` | 全部通过 | 通过 | 已通过 |
| 静态与编译 | vet、tidy、integration 编译 | `go vet ./...`、`go mod tidy -diff`、`go test -tags=integration ./internal/testkit/integration -run '^$' -count=1` | 通过且无依赖 diff | 通过 | 已通过 |
| Node 契约 | runtime state 与 Redis 边界 | `pnpm --filter juhe-ai-backend test:gateway-cache-invalidation-runtime-state`、`pnpm --filter juhe-ai-backend test:performance-redis-boundary` | 当前 Node topic/key 边界通过 | 通过 | 已通过 |
| 真实 PG / Redis / Node gateway | 更新或删除已缓存 API Key | W1b API Key 写入后观察 validation version、runtime topic 和 quota topic | Node gateway 立即观察新状态 | 未执行 | 待真实环境验证 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：数据库事务提交后是否还有 validation、runtime、quota、统计或日志副作用。
- 重点看什么：通知顺序、`apiKeyId` 是否保留、哪些错误是 best-effort、哪些错误必须传播。
- 如何避免误判：CRUD 表数据正确不代表网关立即一致；必须检查 shared cache version 和 runtime state topic。

## 完成总结

- 完成时间：2026-07-10。
- 结论：Go W1b public API Key CRUD 已补齐 Node validation/runtime/quota 失效顺序、错误边界和生产装配。
- 后续建议：在真实 PG/Redis/Node gateway 环境执行已缓存 Key 的 update/delete smoke，并记录 validation version 前后值和三个 topic payload。
