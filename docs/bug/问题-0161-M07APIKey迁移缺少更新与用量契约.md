# BUG-0161 M07 API Key 迁移缺少更新与用量契约

## 基本信息

- 编号：BUG-0161
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 网关 / 前端 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：BUG-0152
- 责任人：待定

## 问题概述

- 现象：M07 Go API Key 包新增列表、详情、secret、创建、刷新和删除，但未覆盖 Node/前端已有的 PATCH 更新接口。
- 期望：Go 接管后，`PATCH /api-keys/:id` 和 `PATCH /my-api-keys/:id`、列表真实 usage、写后 validation cache 失效均保持 Node 行为。
- 实际：Go `routes.go`/`store.go` 没有 PATCH；列表 usage 固定为零值；`NewStore` 允许 `inval == nil`，refresh/delete 在未注入 invalidator 时不会执行 Node 要求的 validation cache 失效。owner manifest 仍将 api-keys 标为 `missing`，gateway main 也未挂载该包。
- 影响范围：管理端无法编辑 API Key；列表用量展示错误；网关可能继续使用旧 validation/可用性缓存。

## 根因与证据

- Node/前端端点：`backend/src/modules/api-keys/api-keys.routes.ts`、`frontend/src/api/domains/apiKeys.ts`。
- Go 路由：`backend-go/projects/gateway/internal/apikeys/routes.go` 只有 list/detail/secret/create/refresh/delete。
- usage：`backend-go/projects/gateway/internal/apikeys/store.go` 返回零值；Node `api-key-list-mappers.ts` 会加载真实 usage summaries。
- invalidation：Go `NewStore` 接受 nil invalidator；Node repository 在 refresh/delete 后始终执行 validation cache invalidation。

## 已确认的功能偏差（本轮审计）

### 1. 未指定排程时区时使用了错误的时区来源

- Node 证据：`backend/src/storage/api-key-availability-schedule.ts` 的 `defaultScheduleTimezone()` 调用 `usageStatsTimezone()`；该函数从 `system_settings` 中 `system_account_id = 'sys_admin'`、`key = 'usageStatsTimezone'` 的 JSON 设置读取业务时区，缺失或无效时抛错。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/schedule.go` 的 `defaultScheduleTimezone()` 直接返回 `time.Local`，只有本地时区名不可解析时才回退 `UTC`，没有读取业务设置。
- 影响：请求未传 `availabilitySchedule.timezone` 时，Node 与 Go 可能按不同本地日期/星期/分钟计算排程。这样会改变创建或更新时的 API Key `status`（`active`/`disabled`）以及 `availability_schedule_next_check_at`，并进一步影响网关是否认为该 Key 当前可用。只要部署主机时区与 `usageStatsTimezone` 不一致即可复现。
- 结论：这是可观察的功能结果偏离，不是实现风格差异；需要让 Go 使用与 Node 相同的业务时区来源，并在设置缺失/无效时保持同等失败语义。

### 2. 排程子字段显式 `null` 被错误当成缺失

- Node 证据：`backend/src/modules/api-keys/api-key-availability-schedule.schema.ts` 将 `dateRange`、`exceptions`、例外项 `windows` 定义为 `.optional()`，没有 `.nullable()`；`apiKeyMutationSchema` 只对整个 `availabilitySchedule` 做 `.nullable()`。因此例如 `{"dateRange":null}`、`{"exceptions":null}`，以及拒绝例外 `{"action":"deny","windows":null}` 都应在路由层返回 400。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/schedule.go` 的 `normalizeScheduleDateRange`、`normalizeScheduleExceptions` 以及拒绝例外分支以 `input == nil` / `object["windows"] != nil` 判断，无法区分 JSON 缺失与显式 `null`；这些输入会被接受并归一化为无日期范围、无例外或无 `windows` 的合法排程。
- 影响：客户端传入的非法 JSON 不再得到 Node 的 400，而会创建/更新 API Key 并改变排程存储结果。该偏差不依赖数据库脏数据，直接通过管理 API 可复现。
- 结论：这是输入校验与最终写入结果的功能偏离，需在 Go 解析层保留字段 presence，并对非 nullable 子字段拒绝显式 `null`。

## 修复与验证

- 修改点：补齐 PATCH 及严格校验/乐观并发，接入真实 usage 投影和必需 invalidator；补 gateway main、Redis/SQLite 双模式、前端端点和写后缓存回归。
- 当前验证：Go 包内测试通过不代表 PATCH/usage/失效契约；未执行真实 gateway listener 验证。
- 结论：M07 不能视为完整 API Key 功能迁移或 Node 可归档。
