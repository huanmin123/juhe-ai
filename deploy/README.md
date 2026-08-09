# 发布包快速运行说明

> 这是发布包根目录内置的快速说明。构建发布包见 `docs/deploy/构建指南.md`。完整部署先按场景选择：服务器看 `docs/deploy/scenarios/服务器部署方案.md`，家庭宽带反代看 `docs/deploy/scenarios/家庭宽带反向代理方案.md`；通用启动、HTTPS 证书、状态检测、常驻运行、反向代理、备份迁移和排障再看 `docs/deploy/部署指南.md`。

## 启动脚本

| 目标平台 | 启动命令 |
| --- | --- |
| Windows | `pwsh ./start.ps1` |
| macOS | `bash ./start.sh` |
| Linux | `bash ./start.sh` |


发布包可以来自 Windows、macOS 或 Linux 任一打包平台。不要跨系统复制 `node_modules`；日志搜索 `grep 模式` 只使用后端生产依赖 `@vscode/ripgrep` 安装的 `rg`，目标机器启动时会按当前平台和架构安装对应二进制。发布包同时包含目标平台的 `backend-go/juhe-ai-runtime-log-indexer` 与 `backend-go/juhe-ai-table-monitor`（Windows 为 `.exe`）；Go 原生 grep 仍要求目标机器提供系统 `rg`，或配置 `JUHE_AI_RG_PATH`。

## 部署前检查

- Node.js 必须使用官方 LTS，当前支持 22.x LTS（>=22.13.0）或 24.x LTS（>=24.11.0），且内置 SQLite 必须支持 FTS5 / trigram tokenizer。
- `pnpm` 可用，或 `corepack` 可用且能启用 `pnpm`。
- 默认端口 `3000` 没有冲突；冲突时修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 当前目录有写入权限和足够磁盘空间。

## 配置

发布包带有 `backend/.env.example`。可以在正式启动前复制并编辑 `backend/.env`：

```powershell
Copy-Item .\backend\.env.example .\backend\.env -ErrorAction SilentlyContinue
notepad .\backend\.env
```

如果没有手动创建，`start.ps1` / `start.sh` 首次启动会自动从 example 创建 `backend/.env`，生成稳定随机 `JUHE_AI_SECRET` 写回文件，并填入本机默认 `JUHE_AI_ALLOWED_ORIGINS`。公网 IP、域名或反向代理部署后，仍要把 `JUHE_AI_ALLOWED_ORIGINS` 改成实际后台访问 Origin，并备份 `backend/.env`。公网 HTTPS 默认优先使用 Caddy 自动申请和续期免费证书，详见 `docs/deploy/https/Caddy自动HTTPS部署指南.md`。

最低配置：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DB_SERVICE_HTTP_HOST=127.0.0.1
JUHE_AI_DB_SERVICE_HTTP_PORT=0
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_DATASET_DATABASE_PATH=./data/juhe-ai-dataset.sqlite3
JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/juhe-ai-runtime-log.sqlite3
JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/juhe-ai-usage-catalog.sqlite3
JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3
JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/juhe-ai-table-monitor.sqlite3
JUHE_AI_TABLE_MONITOR_INSTANCE_ID=juhe-ai-table-monitor
JUHE_AI_USAGE_SHARD_ROOT=./data/usage-shards
JUHE_AI_USAGE_SHARD_COUNT=16
JUHE_AI_SECRET=可留空由启动脚本首次生成，或换成自己保存的强随机密钥
JUHE_AI_ALLOWED_ORIGINS=http://localhost:3000,http://127.0.0.1:3000
JUHE_AI_OAUTH_PROXY_URL=
```

新部署可以使用启动脚本生成的 `JUHE_AI_SECRET`，也可以改成自己保存的强随机值；如上线窗口已离线处理并保留当前 schema 数据，必须沿用原 `JUHE_AI_SECRET` 解密敏感字段。项目运行时不承担旧数据迁移或旧结构兼容。`JUHE_AI_DATABASE_PATH` 保存业务配置和资源关系；审计、操作日志、公开接口日志、模型检测和清理目标在数据集目录库；Go F1 运行日志索引在 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH`；usage shard 注册表、列表筛选目录和账号 / API Key scope catalog 在使用记录目录库；新写入的使用记录保存在 usage shard 目录；统计缓存和窗口表保存在统计结果库；Go F2 表监控快照在 `JUHE_AI_TABLE_MONITOR_DATABASE_PATH`。六个 SQLite 文件路径必须互不相同，usage shard 根目录也要与这些文件区分。原始审计正文捕获固定开启，不再通过环境变量关闭。

启动脚本会独立启动 Go `juhe-ai-runtime-log-indexer`，并把稳定随机的 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID` 首次写入 `backend/.env`。它不是 Node/Go owner switch：Node 只继续写 JSONL 和只读查询，Go 是运行日志索引、cursor、facet 与保留清理的唯一 writer，不使用队列或 Node worker。Go 使用与 Node 相同的环境来源（进程环境、`backend/.env`、可选 `JUHE_AI_ENV_FILE` / `.env.capacity`）；SQLite 读取 `JUHE_AI_DATABASE_PATH` 与独立的 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH`，PostgreSQL 读取 `JUHE_AI_POSTGRES_URL`。相对的 SQLite / 日志目录路径会按 `backend/` 解析成同一绝对位置。启动期间先等待 `/__aisys__/api/health` 确认 DB service 已就绪，才启动 indexer；它在 `backend/runtime/juhe-ai-runtime-log-indexer.pid` 跟踪进程，并把输出写入 `backend/logs/juhe-ai-runtime-log-indexer.log`；Web/API 进程或 indexer 任一退出时，启动脚本会停止另一方，避免留下无主 writer。

启动脚本随后独立启动 Go `juhe-ai-table-monitor`。F2 是表存储监控采样、快照写入和快照保留清理的唯一 owner；Node 只读 F2 产物，不注册 Node scheduler、stats writer 或 retention。`JUHE_AI_TABLE_MONITOR_INSTANCE_ID` 必须在 `backend/.env` 或更高优先级环境中保持非空且稳定；SQLite 使用专用 `JUHE_AI_TABLE_MONITOR_DATABASE_PATH`，PostgreSQL 使用 `JUHE_AI_POSTGRES_URL` 写入 `juhe_stats`。它同样在 API health 就绪后启动，在 `backend/runtime/juhe-ai-table-monitor.pid` 和 `backend/logs/juhe-ai-table-monitor.log` 留下受控进程状态；Web/API、F1 或 F2 任一退出时，启动脚本会收尾其他进程，避免留下无主 writer。

## 启动与验证

Windows：

```powershell
pwsh .\start.ps1
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/
```

macOS/Linux：

```bash
bash ./start.sh
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
```

## 备份

备份固定分为两类，并分别只保留最近 3 次：

- `project-*`：升级前当前运行 release 的干净可部署压缩包、清单和校验值；目标迁移脚本只能作为附加证据。它必须与同一时间戳的业务备份组成恢复点，不得用待上线新包替代当前代码快照；不含真实 env、`data/`、日志、数据库、`node_modules` 或链接。源码历史由 Git 保存，项目备份保存的是可部署产物。
- `business-*`：核心业务库、恢复加密凭据所需的有效 `JUHE_AI_SECRET` / `backend.env`、schema / 清单和校验值。

不得备份数据集目录库、使用记录目录库、usage shard、统计结果库、Codex context、审计 payload、日志或 Redis。同一时间戳的项目备份与业务备份都完成完整性校验并原子发布后，立即分别删除同类第 4 份及更早目录。完整规则见 `docs/deploy/部署指南.md`。

部署方式先看 `docs/deploy/scenarios/`。状态检测和自动恢复看 `docs/deploy/watchdog/README.md`。HTTPS 证书、常驻运行、反向代理、端口开放、数据迁移和常见排障请继续查看 `docs/deploy/部署指南.md`。

## 统计重建

统计重建不是每次上线动作。只有统计表、统计口径、授权消耗规则或额度窗口规则变化时，才在停掉 Web 和 background worker 后执行：

Windows：

```powershell
node .\backend\dist\scripts\maintenance\rebuild-usage-stats.js
```

macOS/Linux：

```bash
node ./backend/dist/scripts/maintenance/rebuild-usage-stats.js
```
