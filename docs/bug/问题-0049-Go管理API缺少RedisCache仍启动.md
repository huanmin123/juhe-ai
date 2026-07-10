# BUG-0049 Go 管理 API 缺少 Redis cache 仍启动

## 基本信息

- 编号：BUG-0049
- 状态：已修复（待真实环境验证）
- 严重程度：P1
- 发现时间：2026-07-11
- 发现方式：PLAN-0081 生产切流门禁审计
- 模块：后端 / Go / 管理接口 / Redis cache / 网关缓存失效 / 部署
- 关联计划：[PLAN-0081 Node 转 Go 渐进减法迁移](../plans/计划-0081-Node转Go渐进减法迁移.md)
- 关联 bug：[BUG-0040](问题-0040-Go公开APIKey写入未触发网关缓存失效.md)
- 责任人：主 agent / 维护者

## 问题概述

- 现象：开启 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 时，配置层只要求 Redis state、Redis queue 和稳定密钥；未配置 Redis cache 时 server 仅打印告警，仍继续挂载包含写接口的完整管理 API。
- 期望：完整管理 API 只有在独立 Redis cache、state、queue 和稳定密钥全部可用时才允许启动；缺少、格式无效或无法连接 Redis cache 都必须在 HTTP 监听前失败。
- 实际：缺少 Redis cache 时仍会构造只有 Redis state 的 gateway invalidator，部分管理写操作无法刷新共享 cache version。
- 影响范围：已迁移的系统账户、代理、分组、设置、账户标签、自定义模型、团队与统一授权等管理写接口；可能导致 Node 网关或其他进程继续读取旧的 API Key 校验、系统设置或 gateway lookup 缓存。

## 根因分析

- public API 已把 Redis cache 作为必需依赖，但管理 API 的配置校验沿用了更早只要求 state / queue 的灰度基线。
- `newGatewaySystemAccountInvalidator` 为管理 API 保留了“缺 cache 仅告警”的降级分支，与迁移文档“不允许绕过 gateway cache invalidation 缺口”的切流门禁冲突。
- session-only 窄开关和完整管理 API 共用部分配置校验，收紧时必须避免误要求 session-only 配置 Redis cache / queue。

## 修复方案

- `validateManagementAPIConfig` 在完整管理 API 开启时新增 `JUHE_AI_REDIS_CACHE_URL` 必填校验。
- gateway invalidator 装配层同步移除告警降级；管理或 public API 任一开启且 cache URL 缺失时直接返回错误。
- 保留 `JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED=true` 且完整管理开关关闭时只要求 Redis state 的窄开关行为。
- 继续保留 Redis URL 格式、不同 DB 和启动 Ping 校验；不增加内存 cache 或本地降级分支。

## 验证记录

| 验证类型 | 命令 / 步骤 | 实际结果 | 状态 |
| --- | --- | --- | --- |
| 定向测试 | `go test ./internal/config ./internal/app -count=1` | 通过 | 已通过 |
| Go 全量 | `go test ./... -count=1` | 通过 | 已通过 |
| 静态检查 | `go vet ./...`、`go mod tidy -diff` | 通过 | 已通过 |
| 配置边界 | 完整管理 API 分别缺少 cache/state/queue/secret；session-only 仅配置 state | 完整管理 API 均 fail-fast，session-only 保持可用 | 已通过 |
| 真实 Redis | cache URL 不可达时启动失败；三类 Redis 可用时启动成功 | 当前环境未提供真实 Redis | 待真实环境验证 |

## 下次遇到

- 新增 opt-in 管理写接口时，不只检查路由是否默认关闭，还要核对其跨进程缓存、运行态和队列依赖是否在配置层与装配层同时 fail-fast。
- “写后 best-effort”只适用于事务提交后的单次失效发布失败，不代表进程可以在启动时缺少整个 cache driver。
- session-only 等窄开关必须单独列依赖矩阵，避免完整管理 API 的依赖要求被错误放宽或反向扩散。

## 完成总结

- 完成时间：2026-07-11。
- 结论：完整 Go 管理 API 现在要求 Redis cache/state/queue 和稳定密钥齐备；缺少 Redis cache 不再以告警方式继续启动。
