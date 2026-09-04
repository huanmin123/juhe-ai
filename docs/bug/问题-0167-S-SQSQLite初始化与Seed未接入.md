# BUG-0167 S-SQ SQLite 初始化与 Seed 未接入

## 基本信息

- 编号：BUG-0167
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 存储 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：S-SQ 提交 `a48cda9c4` 新增 SQLite 六库 schema/测试，但 `HEAD` 中没有生产 `EnsureSQLite*` 或 seedDefaults 调用，maintenance CLI 也没有通用 SQLite schema/seed apply 入口。
- 期望：Go maintenance/gateway 启动应能在 fresh SQLite 上创建完整业务、Chat、Dataset、Usage、Stats、Codex schema 并幂等写入默认 seed，替代 Node `database.ts` 的初始化职责。
- 实际：Go 仅提供 DDL 代码和包内测试；Node 仍是实际 schema/seed owner。fresh 环境启动无法由 Go 独立完成。
- `sqlite_schema.go` 文件头还明确保留未移植的 Node-only 条件 ALTER（例如 accounts CHECK 重建）；同时没有 SQLite seed 函数，无法满足工作包要求的“六库 ensure-schema + seeds”。
- 影响范围：迁移后 fresh SQLite、隔离环境和恢复流程可能缺表/缺默认数据，后续管理/网关切片无法可靠运行。

## 根因与证据

- Go：`backend-go/projects/maintenance/internal/schema/sqlite_schema.go`、对应测试。
- Node：`backend/src/storage/database.ts` 仍调用各库 schema 与 `seedDefaults`。
- `HEAD` 全仓检索未发现生产 `EnsureSQLite*`/seed 调用或 CLI flags。

## 修复与验证

- 修改点：在 maintenance 中提供显式、幂等、可审计的 SQLite schema/seed 命令，并接入 gateway/jobs 启动 preflight；覆盖 fresh/restart/partial failure。
- 当前验证：仅包内 schema 测试通过，未执行 fresh CLI/启动验证。
- 结论：S-SQ 不是完整 schema owner 迁移，不能标记 archived。
