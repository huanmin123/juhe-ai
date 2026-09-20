# migration-backup-1 二次伪迁移墓地快照

## 用途

本目录是 Node→Go 迁移**二次伪迁移**（second-pass 全量核对，缺陷总册 BUG-0175）的"墓地"快照：二次核对中判定为 `landed`（已迁移落地）或 `eliminated-by-design`（按设计消除）的 Node 文件，用 `git mv` 从 `migration-backup/` 瘦身快照移入本目录（相对路径结构不变，根为 `migration-backup-1/node/final-archive/backend/src/`）；`partial` / `missing` / 待复核文件留守 `migration-backup/` 作为第二轮修复队列。

- 移入分波执行：第一波约 1090 文件 + 后续改判波次，最终 `SHA256SUMS` 共 1313 条（1313 个内容文件 + 本清单自身，git 跟踪合计 1314 个文件）。
- 执行记录见 `docs/migration/final-migration/second-pass/台账-第一波.md`、`台账-第二波.md`；首轮收官提交为 `63793f367`（2026-09-08，"chore(二次伪迁移): 第一轮全量核对收官"）。
- 移入文件内容与归档时点 HEAD `d8bfbc2a` 一致，只做移动、不做改写。

## 与 migration-backup/node/final-archive 的区别

| | `migration-backup/node/final-archive/` | `migration-backup-1/`（本目录） |
| --- | --- | --- |
| 建立时点 | X02 首轮终局归档（2026-09-04） | 二次伪迁移第一轮起分波移入（2026-09-08 起） |
| 范围 | Node 后端全量终态快照 | 仅二次核对判定 landed / eliminated-by-design 的文件 |
| SHA256SUMS | 自有清单，保留并对移出文件条目前置注记失效原因 | 自有清单 `migration-backup-1/SHA256SUMS`（路径相对本目录根，`sha256sum` 格式） |
| 现状 | 继续保留 partial / missing / 待复核队列（第二轮修复队列） | 墓地：只读封存，不再新增改写 |

两者均为只读历史证据，不参与构建、不被任何运行进程加载。

## 依赖本目录的测试与文档

- `scripts/regression/legacy-node-postgres-index-bridge-contract.test.mjs:12-15`：把本目录的 `backend/src/storage/schema/business-schema.ts` 与 `dataset-schema.ts` 作为遗留 Node PostgreSQL 索引桥接契约（`docs/deploy/macos/operations/` 的 catalog / script / guide）的 schema 对照源，并校验 `sha256` 指纹链。
- `docs/migration/GatewayManagementRouteOwnerManifest.json`：`source_app` 与各 `node_router_file` 字段指向本目录内的 Node 原件（如 `system-api-app.ts`、`accounts.routes.ts`、`route-strategies.routes.ts`）。
- `docs/migration/final-migration/second-pass/台账-第一波.md`、`台账-第二波.md`：移入判定与执行台账。

## 不得随意改动

- 本目录文件受 `SHA256SUMS` 校验（可抽验：`sha256sum -c` 配合调整路径前缀），任何字节改动都会使清单失效。
- 上列契约测试以本目录内容为断言基准，改动会导致回归失败或指纹不匹配。
- 如确需调整（例如再次移入文件），必须同步全量重建 `SHA256SUMS` 并在二次伪迁移台账登记，属高影响操作，须先获得用户明确授权。
