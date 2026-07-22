# Goose 与 Node 初始化边界复审记录

## 结论

- 当前 Go migration catalog 与 Node 精确 gate 都以 schema `70` 为准。
- `juhe-ai-maintenance schema-up` 是当前唯一允许推进 Goose schema 和 `goose_db_version` 的入口。命令只调用 Goose，不手工写账本，也不执行 Node DDL。
- 候选 `da9077a4a` 的 Node 初始化改造不进入主线。该方案先把 Goose ledger 推进到当前版本，再执行 `applyPostgresSchema()` 补建未进入 migration catalog 的 Node 对象，会让数据库同时受 Goose catalog 与 Node 生成 DDL 两套 schema 事实源约束。
- Goose 70 尚未覆盖 Node 全量运行所需的表，因此本批也不把现有 `postgres:init-schema` 改成 Goose-only，避免把“Go 后端当前 schema 可创建”误报为“Node 全应用可在该 schema 运行”。

## 数据库边界

| 数据库状态 | 当前动作 |
| --- | --- |
| fresh Go PostgreSQL | 在 `backend-go` 目录运行 `juhe-ai-maintenance schema-up --dir db/migrations` |
| 已有真实 Goose ledger 且版本低于 70 | 通过同一命令顺序升级到 70，并核对最终版本 |
| Goose ledger 已为 70 | 命令幂等完成并返回 70 |
| 无 ledger 的历史 Node PostgreSQL | 不猜测、不补写版本；按当前 schema 离线重建或等待 Node 全量表进入正式 migration |
| ledger 高于当前 catalog | 拒绝由旧代码继续运行，禁止自动降级 |

`schema-up` 会先检查 migration 文件连续性，再在专用连接获取 Goose 标准 PostgreSQL session advisory lock，并在创建 version table 前于同一锁内检查数据库来源。无 ledger 时只有 `juhe_*` 业务对象为零的真 fresh 库可以继续；空 ledger、无 ledger 的历史 Node 库都直接拒绝且不留下新账本。持锁期间由 Goose Provider 提交 migration 和读取最终版本。migration 失败后保留 Goose 已真实提交的进度，重跑从 ledger 继续。运行时不增加旧表、旧字段或双读双写兼容。schema 维护期间仍要求停止不遵守 Goose advisory lock 的其他 DDL 工具。

## PostgreSQL boolean 候选结论

Goose 中 `providers.enabled`、协议档案和 endpoint family 的 `enabled` 都是原生 `boolean`；Node 当前完整 PostgreSQL DDL 仍把这些列生成为 `integer`。因此 `5d11bcc12` 的 PostgreSQL `= TRUE` 谓词不能在现有 Node 初始化路径仍可用时单独合入，否则 Node 自建库会反向出现 `integer = boolean`。

同理，把默认数据 seed 改为 boolean 参数和 `TRUE/FALSE` 字面量只适用于 Goose-owned schema；在 Node integer schema 上不安全。provider 谓词、seed boolean 与 Node 初始化退出或离线重建为 Goose schema 必须作为一个原子切换批次，不能作为本次独立 `schema-up` 命令的一部分上线，也不增加 integer/boolean 双运行时兼容。

## 验证边界

- Go unit test 固定 catalog 连续性、目标版本 70、migration 失败短路、最终版本核对和禁止直接改 Goose ledger。
- provider 方言谓词与完整 seed boolean regression 已用于证明候选在 Goose schema 上的方向，但因 Node integer schema 仍存在，本批不把它们列为可独立上线结果。
- 本批没有健康真实 PostgreSQL 环境证据，不把单元回归描述为 fresh / upgrade 数据库端到端验收或生产切流依据。
