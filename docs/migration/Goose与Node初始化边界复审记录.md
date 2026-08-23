# Goose 与 Node 初始化边界复审记录

## 结论

> 2026-08-23 当前结论：本仓库 `master` 已不包含历史 `backend-go/db/migrations`，当前业务 schema 仍由 Node 的 `applyPostgresSchema()` 管理；生产和演练库缺少 `public.goose_db_version` 时，不得用手工 SQL 补账，也不得把旧 Goose catalog 当作 Node 数据库的历史证明。

- 当前发布契约版本为 `94`，但它是统一的 PostgreSQL schema contract 版本，不等同于当前数据库已经存在 Goose ledger `94`。
- `juhe_business.account_test_tasks.queued_deadline_at` 属于本次 Node-owned contract 94；Node 初始化以幂等 DDL 补列，任务创建时写入截止时间快照，worker sweep 和 API 读取均优先使用该快照。
- Node-owned 部署使用只读 `postgres:schema-preflight` / `enforcePostgresSchemaOwnerGate()`，校验关键关系、列、索引并拒绝混合 Goose ledger；该入口不执行 DDL、不写业务数据、不写 `goose_db_version`。
- Goose-owned 部署只有在正式 migration catalog 恢复、独立 Contract release 和演练验证完成后，才设置 `JUHE_AI_POSTGRES_SCHEMA_OWNER=goose`，继续使用严格的 Goose ledger gate；当前不允许通过此模式接管现有 Node 库。
- `JUHE_AI_OWNER_LOCK_ENABLED=true` 时必须显式配置 `JUHE_AI_POSTGRES_SCHEMA_OWNER=node|goose`；缺失、非法或混合状态一律 fail-closed。

- 本文后续关于 schema `92`、历史 `juhe-ai-maintenance schema-up` 和 fresh Goose catalog 的内容属于历史验收记录，不代表当前 `master` 已恢复该 catalog。
- 历史 `schema-up` 只允许在完整 Goose catalog 上推进 Goose，不是 Node schema adoption 工具；当前 Node-owned 库应先执行只读 preflight，不应执行历史命令。
- 候选 `da9077a4a` 的 Node 初始化改造不进入主线。该方案先把 Goose ledger 推进到当前版本，再执行 `applyPostgresSchema()` 补建未进入 migration catalog 的 Node 对象，会让数据库同时受 Goose catalog 与 Node 生成 DDL 两套 schema 事实源约束。
- 历史 Goose 92 尚未覆盖 Node 全量运行所需的表，因此当时也不把 `postgres:init-schema` 改成 Goose-only；该历史结论避免把“Go 后端当前 schema 可创建”误报为“Node 全应用可在该 schema 运行”，不代表当前 `master` 仍有 92 目录。
- 2026-07-28 的 W7 隔离验收发现 `000077_w2_chat_message_error_diagnostics.sql` 无条件修改尚由 Node 拥有、Goose fresh schema 未创建的 `juhe_chat.chat_messages`，导致 `schema-up` 在 77 中止。修复保留已有 Node Chat 数据库的加法升级，同时在该表不存在时让 Up / Down 显式 no-op；这不表示 Chat schema 已迁入 Go，也不允许 W7 harness 先调用 Node 初始化绕过缺口。
- 同一 fresh schema 继续执行后在 `000080_w7_account_health_check_hourly_defaults.sql` 暴露 `text = jsonb`：Node 与 Goose 的 `system_settings.value_json` 物理契约都是 JSON 文本，migration 不得把该列臆测为 `jsonb`。比较和更新改为精确 JSON 文本字面量；需要 JSON 运算时必须显式 `value_json::jsonb`，不能改变共存期物理类型。

## 数据库边界

| 数据库状态 | 当前动作 |
| --- | --- |
| 当前 Node-owned PostgreSQL（无 Goose ledger） | 设置 `JUHE_AI_POSTGRES_SCHEMA_OWNER=node`，运行 `pnpm --dir backend postgres:schema-preflight`；只读通过后才能进入发布验证 |
| Node-owned 库存在 Goose ledger | 拒绝混合 owner；先停止发布并完成 owner/Contract 切换审查 |
| 完整 Goose-owned fresh PostgreSQL | 仅在正式 catalog、独立 Contract release 和演练证据齐全后运行 `juhe-ai-maintenance schema-up` |
| Goose ledger 低于/等于当前正式 catalog | 由 Goose 官方 Provider 顺序升级并核对最终版本，禁止手工改 ledger |
| 无 ledger 的历史 Node PostgreSQL | 不猜测、不补写版本；继续按 Node-owned preflight 识别，等待正式 Goose adoption 方案 |
| ledger 高于当前 catalog | 拒绝由旧代码继续运行，禁止自动降级 |

历史 `schema-up` 会先检查 migration 文件连续性，再获取 Goose 标准 PostgreSQL session advisory lock；无 ledger 时只有 `juhe_*` 业务对象为零的真 fresh 库可以继续。它不会为已有 Node schema 自动创建 adoption ledger。迁移失败后保留 Goose 已真实提交的进度，重跑从 ledger 继续。当前 Node-owned preflight 与 Goose schema-up 必须分属两个 owner 模式，不能同时运行。

## PostgreSQL boolean 候选结论

Goose 中 `providers.enabled`、协议档案和 endpoint family 的 `enabled` 都是原生 `boolean`；Node 当前完整 PostgreSQL DDL 仍把这些列生成为 `integer`。因此 `5d11bcc12` 的 PostgreSQL `= TRUE` 谓词不能在现有 Node 初始化路径仍可用时单独合入，否则 Node 自建库会反向出现 `integer = boolean`。

同理，把默认数据 seed 改为 boolean 参数和 `TRUE/FALSE` 字面量只适用于 Goose-owned schema；在 Node integer schema 上不安全。provider 谓词、seed boolean 与 Node 初始化退出或离线重建为 Goose schema 必须作为一个原子切换批次，不能作为本次独立 `schema-up` 命令的一部分上线，也不增加 integer/boolean 双运行时兼容。

## 验证边界

- 当前新增回归覆盖：Node-owned schema 匹配且无 ledger 通过；缺关系/缺列/缺索引、存在 Goose ledger、owner 缺失或非法均拒绝；CLI 源码无直接 `INSERT/UPDATE/DELETE goose_db_version`。
- `pnpm --dir backend postgres:schema-preflight` 只做只读契约检查；它不是迁移命令，也不替代正式 Goose Contract release。

- 历史 Go unit test 固定 catalog 连续性、目标版本 92、migration 失败短路、最终版本核对和禁止直接改 Goose ledger；`000077` 的契约测试固定 Up / Down 都必须先检查 Node-owned Chat 表是否存在。
- provider 方言谓词与完整 seed boolean regression 已用于证明候选在 Goose schema 上的方向，但因 Node integer schema 仍存在，本批不把它们列为可独立上线结果。
- 历史 schema 70 批次没有健康真实 PostgreSQL 环境证据。2026-07-28 先后用 W7 fresh PostgreSQL 运行得到 `000077` 和 `000080` 的失败反证；修复后已在全新 PostgreSQL 18.4 从 0 连续升级到 92，`schema-up` 返回 `targetVersion=92/currentVersion=92`，随后 W0、W7 real normal/race 通过。该证据只证明历史 fresh Goose catalog 可执行，不证明当前 master 有该 catalog，也不证明历史无 ledger Node 库可原地接管，更不构成生产切流依据。
