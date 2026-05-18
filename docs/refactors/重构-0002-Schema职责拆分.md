# REFACTOR-0002 Schema 职责拆分

## 基本信息

- 编号：REFACTOR-0002
- 状态：已完成
- 创建时间：2026-05-18
- 更新时间：2026-05-18
- 关联计划：无
- 关联模块：后端 / 存储 / 文档 / 脚本

## 重构目标

- 将 `backend/src/storage/schema.ts` 从完整大 SQL 文件收敛为兼容入口，降低单文件阅读和审查成本。
- 按业务库 schema、记录库 schema、默认种子数据三个职责拆分，保持启动入口和外部导入路径不变。
- 保持建表 SQL、索引顺序、默认数据写入逻辑和预上线 schema 演进规则不变。

## 重构前问题

- `schema.ts` 超过 1600 行，同时包含业务库表结构、记录库表结构、索引、默认管理员、默认供应商、默认分组和系统设置。
- 记录库表持续增加后，审查业务库授权 / 分组结构时需要跨过大量日志、审计、统计和监控表 SQL。
- 回归脚本直接读取 `schema.ts` 检查旧字段，拆分后如果不更新读取范围，会误以为真实 schema 已被检查。

## 拆分设计

- `backend/src/storage/schema.ts`
  - 只作为兼容门面导出 `applyBusinessSchema`、`applyRecordSchema` 和 `seedDefaults`。
  - 不承载实际建表 SQL 和默认数据写入逻辑。
- `backend/src/storage/schema/business-schema.ts`
  - 负责业务库 PRAGMA、业务表和业务索引。
  - 不负责记录库表、日志审计统计表和默认数据种子。
- `backend/src/storage/schema/record-schema.ts`
  - 负责记录库 PRAGMA、使用记录、审计、操作日志、运行日志、统计缓存、系统监控和表监控表 / 索引。
  - 不负责业务库核心资源表和默认管理员 / 默认供应商写入。
- `backend/src/storage/schema/seed-defaults.ts`
  - 负责默认管理员、全局设置、OpenAI 供应商、默认 OpenAI 分组和系统设置种子。
  - 不负责 schema DDL。

## 变更范围

- 后端存储：`backend/src/storage/schema.ts`、`backend/src/storage/schema/business-schema.ts`、`backend/src/storage/schema/record-schema.ts`、`backend/src/storage/schema/seed-defaults.ts`
- 回归脚本：`backend/src/scripts/regression/usage-pricing-regression.ts`
- 文档：`docs/architecture/backend/README.md`、`docs/functions/SQLite存储说明.md`、`docs/functions/操作日志设计.md`、`docs/functions/幂等与唯一约束设计.md`、`docs/refactors/README.md`

## 行为兼容

- `database.ts` 继续从 `./schema.js` 导入三个函数，运行时入口不变。
- 建表 SQL、索引 SQL、默认数据写入顺序和默认值保持不变，只移动文件位置。
- `usage-pricing-regression` 继续检查源码中不再出现 `last_probe_at`，检查范围扩展到拆分后的 schema 文件。
- 不新增迁移、补列、旧字段兼容或运行时代码分支。

## 验证记录

| 测试类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 状态 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 已通过 | 验证拆分导入边界 |
| 命令类验证 | 后端构建 | `pnpm --filter juhe-ai-backend build` | 通过 | 已通过 | 验证 ESM 编译产物路径 |
| 命令类验证 | SQLite 运行时 preflight | `pnpm --filter juhe-ai-backend check:runtime` | 通过 | 已通过 | 验证当前 Node 内置 SQLite 能力 |
| 回归脚本 | usage-pricing 回归 | `pnpm --filter juhe-ai-backend test:usage-pricing` | 通过 | 已通过 | 覆盖拆分后 schema 源码检查 |

## 风险与后续

- `record-schema.ts` 仍然较长，后续如果记录库表继续扩张，可以再按 usage / audit / logs / metrics 拆分，但需要先有更细的测试覆盖。
- 文档和计划中的历史路径仍可能提到 `schema.ts`，保留入口文件后这些历史引用不影响运行；新增文档应优先写清 `schema.ts` 是入口，实际 SQL 在 `storage/schema/` 下。

## 完成总结

- 完成 schema 大文件三段职责拆分，保留原导入入口。
- 同步修正直接读取源码的回归脚本，避免拆分后检查失效。
- 已通过后端类型检查、构建、运行时 preflight 和 usage-pricing 回归。
