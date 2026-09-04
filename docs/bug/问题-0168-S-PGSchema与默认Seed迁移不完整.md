# BUG-0168 S-PG Schema 与默认 Seed 迁移不完整

## 基本信息

- 编号：BUG-0168
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 存储 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：S-PG 提交 `aa76acaaf` 提供 `EnsurePostgres`/`EnsurePostgresSeeds`，但仅有测试调用，maintenance main 没有通用 PG schema/seed CLI；默认 seed 还明确省略 Node 实际执行的多类数据。
- 期望：Go maintenance 独立完成 PostgreSQL schema、全部默认 seed 和幂等升级，覆盖 provider model catalog stale disable、默认 route strategy/API key/chat key、external integration token 等 Node seed 行为。
- 实际：`EnsurePostgresSeeds` 注释/代码不移植上述 bulk upsert、stale disable 和默认数据；PG smoke 依赖 env opt-in，默认可跳过。Go 无法独立替代 Node 的 fresh/upgrade 初始化。
- `PLAN.md` 虽将 S-PG 标为 archived，却仍注明 PG smoke 依赖隔离环境；工作包要求的 `maintenance --ensure-schema` 通用命令在当前 maintenance main 中不存在。
- 影响范围：新库或升级库缺少模型目录、默认策略/密钥和外部集成默认数据，网关路由与管理页面结果偏离。

## 根因与证据

- Go：`backend-go/projects/maintenance/internal/schema/pg_schema.go`、maintenance main。
- Node：`backend/src/storage/database.ts` 的 `seedPostgresDefaults`。
- 现有测试不等于生产 owner；未见通用 CLI/preflight 和非 opt-in PG smoke。

## 修复与验证

- 修改点：补齐所有 seed 语义及 stale disable，提供 maintenance CLI/preflight，执行 fresh/upgrade/幂等和真实 PostgreSQL 回归。
- 当前验证：包内测试通过；真实 PG 未执行或被环境门禁跳过。
- 结论：S-PG 不是完整 schema+seed 接管，不能标记 archived。
