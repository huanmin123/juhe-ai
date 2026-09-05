# Node backend 全量最终归档（final-archive）

本目录是 Node 后端 `backend/` 的**全量物理归档**：Node 后端从工作区物理移除前，按归档当日的 git HEAD 原样快照。归档只用于逻辑找回与对照，不能被构建、测试、部署或运行时加载；规则见[完整功能接管与 Node 归档迁移规则](../../../docs/migration/完整功能接管与Node归档迁移规则.md)。

## 归档事实

| 项目 | 值 |
| --- | --- |
| 归档日期 | 2026-09-04 |
| 来源 commit（HEAD） | `d8bfbc2a7d725cdd619a7c23bbf06e33d4add7ff`（master） |
| 范围 | `git ls-files backend/` 全量（含 `package.json`、`tsconfig*.json`、3 个 `.env.*.example`） |
| 文件数 | 1700（`backend/` 下全部已跟踪文件，无 .gitkeep 等例外文件） |
| 总行数 | 137,807 行 |
| 总大小 | 5,923,245 字节（约 5.65 MB） |
| 校验清单 | [SHA256SUMS](SHA256SUMS)（1700 条，路径相对本目录，`sha256sum -c SHA256SUMS` 校验） |
| 提取方式 | `git archive HEAD backend | tar -x -C migration-backup/node/final-archive/`（工作树与 HEAD 无差异） |

目录结构：`backend/` 按原始相对路径原样保留（`backend/src/...` 1694 个文件 + 顶层 6 个工程文件）。`K2-session-auth.manifest.json` 是 K2 切片（session auth）的既有 manifest，其记录的 6 个文件已包含在本全量归档的 `backend/` 内。

## 用途

- **逻辑找回**：后续 Go 侧发现行为差异或遗漏时，可在此浏览 Node 原始实现（含网关链路、repository、回归脚本）。
- **对照**：Go 实现与 Node 原实现逐文件比对；最大文件如 `src/modules/providers/drivers/_shared/openai-anthropic-bridge.ts`（235,599 字节）、`src/storage/repositories.ts`（176,667 行节）均完整保留。

## 恢复方式

1. **优先 git 历史**：`git show <commit>:<path>`（或 `git checkout <commit> -- backend/`）可精确取回任意历史版本；归档当日版本即 `d8bfbc2a`。
2. **本归档**：git 之外的独立可浏览副本，直接按 `backend/` 相对路径读取；完整性用 `sha256sum -c SHA256SUMS` 验证。
3. **整功能回滚**：按[父目录各切片归档](../)的 manifest 恢复完整 feature（不得 overlay 单文件到新树、不得恢复部分 Node worker、不得 Node/Go 双 owner 并行）。

## 与既有切片增量归档的关系

[父目录](../)下 final-archive 之外的目录是迁移过程中按功能切片的**增量归档**，各自带 `manifest.json`（originalPath → archivePath + sha256 + kind）：

| 切片目录 | 覆盖内容 |
| --- | --- |
| `account-health-probe/` | J1 账号健康探活与冷却重试（Go jobs 接管） |
| `audit-log-write-retention/` | 审计日志写入保留策略 |
| `operation-log-write-retention/` | 操作日志写入保留策略 |
| `runtime-log-index-retention/` | 运行日志索引保留策略 |
| `table-monitor-sampling-retention/` | 表监控采样保留策略 |
| `j3b-model-check/` | J3b 模型检查 |
| `j3a-proxy-latency-cutover-20260824/`、`j3a-proxy-latency-manual-control-cutover-20260826/` | J3a 代理延迟切换与手动控制 |

切片 manifest 与本全量归档可能存在 `.original` 后缀或快照时点差异；**以本 final-archive（HEAD `d8bfbc2a`）为 Node 最终状态权威副本**，切片 manifest 保留各功能接管时的上下文（goReplacement、activePathZero、validation 证据）。切片文件若与本归档同名路径内容不一致，说明该切片归档早于最终 HEAD，以切片 manifest 记录的时点为准。
