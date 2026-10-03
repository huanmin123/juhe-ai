# 问题-0274：Codex Responses↔Chat 桥状态在生产从未装配（含 retention segments 根错位）

- 编号：BUG-0274
- 状态：已修复（2026-10-03），待发布
- 发现：BUG-0269 后的全量排查（接线断线审计）。本缺陷代码注释自证："2026-09-28 哲学裁定补齐（功能默认开启）……迁移漏装配，与终局波 TrafficRuntimeMigrator 同类断线"——补齐只写了消费侧，组合根装配从未落地。

## 现象

`chainRuntimeDeps.CodexBridge` / `CodexContextRoot` / `CodexContextStateStore`（`chain_compose.go:135-148`）全仓无任何赋值点，gateway 不读 `JUHE_AI_CODEX_CONTEXT_ROOT`（jobs/retention 读，gateway 只把 `codex-context` 当目录名常量）→ `newChainCodexBridgePreflight` 永不构造，`codexPreflightAdapter` 恒 no-op。后果：chat-only 桥账户上 Codex `/responses` 多轮请求的 `previous_response_id` 上下文还原与 compact 前置校验全部缺席。既有 `TestW1CodexBridge*` 手工注入 deps，对生产断线不敏感（装配前全绿）。

## 修复

- `runtimeConfig` 新增 `CodexContextRoot`，派生 `datadir.Path(getenv, dataDir, "JUHE_AI_CODEX_CONTEXT_ROOT", datadir.CodexContextRoot)`（显式 env 优先、缺省 `<DATA_DIR>/codex-context`，与 jobs/文档默认一致）。
- 新文件 `codex_context_store.go`：`newChainCodexContextStateStore`——空根 (nil,nil) 保持既有 no-op 退出；postgres 分支 `Acquire` 业务池（同 role/参数复用）+ `NewPostgresContextStateStore` + `SELECT 1 FROM juhe_codex_context.codex_context_sessions LIMIT 0` 探针，**失败启动 fail-fast**（生产 schema 由 maintenance ensure-schema 预置，缺失属部署错误必须启动期暴露）；sqlite 分支 `NewSQLiteShardContextStateStore`（分片 schema 由组合根前置预检）。
- `compose.go`：deps 注入两键；sqlite 行存储注册 shutdown（LIFO 防句柄锁）。
- **配套缺陷（同批）**：jobs retention 删除根错位——`worker_retention.go` 用 `store.ShardRoot`（state-shards 根）解析 segments 相对路径，永远 miss 且"文件不存在=成功"把删除按成功键落账 → segment 文件永不清理（存储泄漏）。修复：jobs `CodexContextRoot` 同款 datadir 派生，`codexStorageProcessor` 删除根改用 segments 根，"不存在=成功"语义保持。

## 部署契约同步

`docker/single-server/README.md`（env 契约：gateway 新增读取 `JUHE_AI_CODEX_CONTEXT_ROOT`，可选、缺省 `<数据根>/codex-context`、双进程必须同值；PG 模式要求 juhe_codex_context schema）、`docs/deploy/部署指南.md`、`docs/functions/SQLite存储说明.md`、`docs/develop/测试与验证说明.md` 隔离 env 块。

## 测试

`TestComposeSystemAPIWiresCodexBridgeState`（生产组合根 sqlite 装配断言 bridge/compact/registry 非 nil，装配前必红）；`TestNewChainCodexContextStateStoreArms`（空根降级、PG nil 池 fail-fast）；jobs `TestCodexStorageProcessorDeletesUnderSegmentsRoot`（异源 shardRoot 下修复前必红，"不存在=成功"保持）。

## 残余项

- compact 合成摘要 dispatcher 仍缺席（`chain_ports.go` 注释，compact 摘要上游交换会显式报"未配置"，属独立能力缺口）。
- PG 探针 fail-fast 的真实缺 schema 场景未做运行断言（生产前置为 ensure-schema）。
