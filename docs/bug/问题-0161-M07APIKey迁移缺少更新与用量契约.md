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

## 修复与验证

- 修改点：补齐 PATCH 及严格校验/乐观并发，接入真实 usage 投影和必需 invalidator；补 gateway main、Redis/SQLite 双模式、前端端点和写后缓存回归。
- 当前验证：Go 包内测试通过不代表 PATCH/usage/失效契约；未执行真实 gateway listener 验证。
- 结论：M07 不能视为完整 API Key 功能迁移或 Node 可归档。
