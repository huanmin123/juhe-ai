# 发布包快速运行说明

> 这是发布包根目录内置的快速说明。构建发布包见 `docs/deploy/构建指南.md`。完整部署先按场景选择：服务器看 `docs/deploy/scenarios/服务器部署方案.md`，家庭宽带反代看 `docs/deploy/scenarios/家庭宽带反向代理方案.md`；通用启动、HTTPS 证书、状态检测、常驻运行、反向代理、备份迁移和排障再看 `docs/deploy/部署指南.md`。

## 启动脚本

| 目标平台 | 启动命令 |
| --- | --- |
| Windows | `pwsh ./start.ps1` |
| macOS | `bash ./start.sh` |
| Linux | `bash ./start.sh` |


发布包可以来自 Windows、macOS 或 Linux 任一打包平台。不要跨系统复制 `node_modules`；日志搜索 `grep 模式` 只使用后端生产依赖 `@vscode/ripgrep` 安装的 `rg`，目标机器启动时会按当前平台和架构安装对应二进制。

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
JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/juhe-ai-usage-catalog.sqlite3
JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3
JUHE_AI_USAGE_SHARD_ROOT=./data/usage-shards
JUHE_AI_USAGE_SHARD_COUNT=16
JUHE_AI_SECRET=可留空由启动脚本首次生成，或换成自己保存的强随机密钥
JUHE_AI_ALLOWED_ORIGINS=http://localhost:3000,http://127.0.0.1:3000
JUHE_AI_OAUTH_PROXY_URL=
```

新部署可以使用启动脚本生成的 `JUHE_AI_SECRET`，也可以改成自己保存的强随机值；如上线窗口已离线处理并保留当前 schema 数据，必须沿用原 `JUHE_AI_SECRET` 解密敏感字段。项目运行时不承担旧数据迁移或旧结构兼容。`JUHE_AI_DATABASE_PATH` 保存业务配置和资源关系；审计、操作日志、运行日志索引、模型检测和清理目标在数据集目录库；usage shard 注册表、列表筛选目录和账号 / API Key scope catalog 在使用记录目录库；新写入的使用记录保存在 usage shard 目录；统计缓存和窗口表保存在统计结果库。四个 SQLite 文件路径必须互不相同，usage shard 根目录也要与这些文件区分。原始审计正文捕获固定开启，不再通过环境变量关闭。

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

必须备份：

```text
backend/.env
核心业务表数据
```

默认备份只需要 `.env` 和业务表导出；数据集目录库、使用记录目录库、usage shard 目录和统计结果库通常可丢弃、清空或重建。只有做离线取证、迁移完整历史明细或保留审计 payload 时，才停服务后额外备份 `backend/data/juhe-ai-dataset.sqlite3`、`backend/data/juhe-ai-usage-catalog.sqlite3`、`backend/data/usage-shards/` 或 `JUHE_AI_USAGE_SHARD_ROOT` 指向的目录，以及 `backend/data/juhe-ai-stats.sqlite3`。

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
