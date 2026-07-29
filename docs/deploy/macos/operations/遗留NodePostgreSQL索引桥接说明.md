# 遗留 Node PostgreSQL 索引桥接

本操作只适用于历史 Node PostgreSQL 数据库，目标是补齐已批准的三条索引，而不执行 Goose、迁移 ledger 写入、表定义修改或业务行写入。目录文件 `legacy-node-postgres-index-bridge.catalog.json` 是固定目录；脚本会校验其规范化 SHA-256 指纹，修改目录必须伴随新的评审和脚本指纹更新。

## 范围和边界

- 仅允许 `idx_model_check_runs_quality_health_sync_due`、`idx_model_check_runs_quality_health_sync_invalid_time` 和 `idx_accounts_balance_auto_detect_due`。
- 两条 87 索引只使用最终 canonical 定义和 `CREATE INDEX CONCURRENTLY`；不执行原迁移中的 `DROP INDEX`。
- 93 索引按历史 Node 物理类型使用 `schedulable = 1` 与 `balance_query_enabled = 0`，不能使用布尔字面量。
- `cleanup-invalid` 必须明确指定一个目录内索引，且只删除无效索引；删除后不会自动重建。
- 输出仅包含动作、数据库名、索引名和状态，不输出连接串、用户名、主机、错误原文或其他秘密。

## 前置检查

脚本直接用发布包内 `backend` 依赖的 `pg` 建立单独 PostgreSQL 会话。它要求：

- 连接串只通过环境变量提供，默认 `JUHE_AI_POSTGRES_URL`；不要作为命令行参数传入。
- `--database` 必须与 `current_database()` 完全相同，PostgreSQL 版本至少为 12。
- 数据库不得存在 `goose_db_version`；发现 Goose ledger 时脚本拒绝运行，避免把本桥接误用于 Goose 管理库。
- 当前数据库角色必须拥有目标表与已有目标索引。
- 所有目标表、列、列类型和受控字段默认值必须与固定目录一致；账户的两个历史标志必须为 `integer NOT NULL DEFAULT 1/0`，且已有值只能是 `0` 或 `1`。
- 写操作使用单独会话级 advisory lock；会话设置 30 秒 `lock_timeout` 和 15 分钟 `statement_timeout`。它不会进入事务，因此 `CREATE/DROP INDEX CONCURRENTLY` 始终可用。

## 命令

从发布根目录执行。默认动作是只读 `inspect`；`verify` 同样只读，但在任一索引缺失、无效或定义不符时返回非零。

```bash
node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --database juhe_ai

node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action verify --database juhe_ai

node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action apply --database juhe_ai \
  --catalog-fingerprint fa917ac1afe7ad8bf8475db79f7158d115974099999a6efc4b0a412ea9500d40
```

仅当一次并发建索引中断留下无效的目录内索引时，先确认目标索引名，再显式清理：

```bash
node ./docs/deploy/macos/operations/legacy-node-postgres-index-bridge.mjs \
  --action cleanup-invalid --index idx_accounts_balance_auto_detect_due --database juhe_ai \
  --catalog-fingerprint fa917ac1afe7ad8bf8475db79f7158d115974099999a6efc4b0a412ea9500d40
```

`apply` 与 `cleanup-invalid` 都必须显式确认当前目录指纹；脚本和目录中的固定值必须一致。清理成功后重新执行 `--action apply`，再执行 `--action verify`。任何身份、类型、值、所有权、索引定义或有效性检查失败都必须停止；不要改用 `IF NOT EXISTS`、手工删除非目录索引或运行 Goose 来绕过失败。

## 发布验证

仓库内执行：

```powershell
pnpm.cmd test:legacy-node-index-bridge
pnpm.cmd test:macos-operations
pnpm.cmd test:release-package
```

这些测试只校验固定目录、并发 DDL、allowlist、Goose 禁止边界和发布包复制契约；不会连接 PostgreSQL。生产实际执行后应保留脱敏 JSON 结果，至少记录 `inspect -> apply -> verify` 的动作和退出码。
