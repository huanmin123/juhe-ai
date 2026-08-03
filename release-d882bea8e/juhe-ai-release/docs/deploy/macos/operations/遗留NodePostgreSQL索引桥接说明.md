# 遗留 Node PostgreSQL 索引桥接

本操作只适用于历史 Node PostgreSQL 数据库，目标是在不执行 Goose、迁移 ledger 写入、表定义修改或业务行写入的前提下，核验三条固定目录项，并只补齐该历史 Node 结构实际适用的索引。目录文件 `legacy-node-postgres-index-bridge.catalog.json` 是固定目录；脚本会校验其规范化 SHA-256 指纹，修改目录必须伴随新的评审和脚本指纹更新。

## 范围和边界

- 仅允许 `idx_model_check_runs_quality_health_sync_due`、`idx_model_check_runs_quality_health_sync_invalid_time` 和 `idx_accounts_balance_auto_detect_due`。
- 两条 87 quality 索引只使用最终 canonical 定义和 `CREATE INDEX CONCURRENTLY`；不执行原迁移中的 `DROP INDEX`。纯 Node 历史库中 `quality_health_sync_status` 可以存在，但其余目录声明的 Go 专用 quality 字段必须全部不存在，此时两条结果必须是 `not_applicable` / `pure_node_schema`，不会创建这两条索引。
- 93 索引按历史 Node 物理类型使用 `schedulable = 1` 与 `balance_query_enabled = 0`，不能使用布尔字面量。
- 同名索引必须同时匹配键表达式、predicate、`btree`、所有权、有效性，以及非唯一、非 exclusion、没有 INCLUDE 列的物理属性；其中 `indisunique=false`、`indisexclusion=false`、`indnatts=indnkeyatts` 且键数与目录相符。
- `cleanup-invalid` 必须明确指定一个目录内索引，且只删除无效索引；删除后不会自动重建。
- 输出仅包含动作、数据库名、索引名和状态，不输出连接串、用户名、主机、错误原文或其他秘密。

## 前置检查

脚本直接用发布包内 `backend` 依赖的 `pg` 建立单独 PostgreSQL 会话。它要求：

- 连接串只通过环境变量提供，默认 `JUHE_AI_POSTGRES_URL`；不要作为命令行参数传入。
- `--database` 必须与 `current_database()` 完全相同，PostgreSQL 版本至少为 12。
- 数据库不得存在 `goose_db_version`；发现 Goose ledger 时脚本拒绝运行，避免把本桥接误用于 Goose 管理库。
- 当前数据库角色必须拥有目标表与已有目标索引。
- 所有目标表、列、列类型和受控字段默认值必须与固定目录一致；账户的两个历史标志必须为 `integer NOT NULL DEFAULT 1/0`，且已有值只能是 `0` 或 `1`。
- 对两条 quality 目录项：目录列出的 Go 专用字段全部缺失才是纯 Node 的 `not_applicable`；只出现其中一部分、或出现后其余必需列不完整，脚本以 `partial_goose_schema_detected` 停止，不能把半迁移结构当作不适用。
- 写操作使用单独会话级 advisory lock；会话设置 30 秒 `lock_timeout` 和 15 分钟 `statement_timeout`。它不会进入事务，因此 `CREATE/DROP INDEX CONCURRENTLY` 始终可用。
- 必须在任何 Node `postgres:init-schema`、服务启动或其他可能创建同名账户索引的步骤之前运行本桥接；否则 Node 的同名宽索引会与固定目录的带 `balance_query_next_refresh_at IS NOT NULL` predicate 定义不匹配，桥接将停止。

## 命令

从发布根目录执行。默认动作是只读 `inspect`。纯 Node 发布中的 `verify` 同样只读，且仅当两个 quality 项都是 `not_applicable` / `pure_node_schema`、账户项是 `valid` 时成功；缺失、无效、定义不符或 `partial_goose_schema_detected` 都返回非零。若所有 quality 字段都完整存在，桥接按完整定义继续核验该两条索引；但存在 `goose_db_version` 的 Goose 管理库仍会在任何索引操作前被拒绝。

```bash
node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --database juhe_ai

node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action verify --database juhe_ai

node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action apply --database juhe_ai \
  --catalog-fingerprint 8dddd3067d514bd8fc9afc7067a3587e514374ba65be0d8690aa792f4c9bfc9f
```

仅当一次并发建索引中断留下无效的目录内索引时，先确认目标索引名，再显式清理：

```bash
node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action cleanup-invalid --index idx_accounts_balance_auto_detect_due --database juhe_ai \
  --catalog-fingerprint 8dddd3067d514bd8fc9afc7067a3587e514374ba65be0d8690aa792f4c9bfc9f
```

`apply` 与 `cleanup-invalid` 都必须显式确认当前目录指纹；脚本和目录中的固定值必须一致。清理成功后重新执行 `--action apply`，再执行 `--action verify`。任何身份、类型、值、所有权、索引定义、物理属性、有效性或 schema 完整性检查失败都必须停止；不要改用 `IF NOT EXISTS`、手工删除非目录索引或运行 Goose 来绕过失败。

## 发布验证

仓库内执行：

```powershell
pnpm.cmd test:legacy-node-index-bridge
pnpm.cmd test:macos-operations
pnpm.cmd test:release-package
```

这些测试只校验固定目录、并发 DDL、allowlist、Goose 禁止边界、纯 Node `not_applicable` 语义、索引物理属性和发布包复制契约；不会连接 PostgreSQL。生产实际执行后应保留脱敏 JSON 结果，至少记录逐索引 `inspect -> apply -> verify` 的状态与退出码，不能把 `not_applicable` 写成已创建或笼统的“索引通过”。
