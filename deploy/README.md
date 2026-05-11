# 发布包快速运行说明

> 这是发布包根目录内置的快速说明。构建发布包见 `docs/deploy/构建指南.md`，完整部署、常驻运行、反向代理、备份迁移和排障见 `docs/deploy/部署指南.md`。

## 启动脚本

| 目标平台 | 启动命令 |
| --- | --- |
| Windows | `pwsh ./start.ps1` |
| macOS | `bash ./start.sh` |
| Linux | `bash ./start.sh` |

发布包可以来自 Windows、macOS 或 Linux 任一打包平台。不要跨系统复制 `node_modules`；首次启动会安装目标平台自己的生产依赖。

## 部署前检查

- Node.js 支持 `node:sqlite`，建议 Node.js 22.5+ 或更新 LTS。
- `pnpm` 可用，或 `corepack` 可用且能启用 `pnpm`。
- 默认端口 `3000` 没有冲突；冲突时修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 当前目录有写入权限和足够磁盘空间。

## 必须配置

正式启动前复制并编辑 `backend/.env`：

```powershell
Copy-Item .\backend\.env.example .\backend\.env -ErrorAction SilentlyContinue
notepad .\backend\.env
```

最低配置：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=换成一串足够长且固定保存的随机密钥
JUHE_AI_OAUTH_PROXY_URL=
```

新部署必须修改 `JUHE_AI_SECRET`。迁移旧数据必须沿用旧 `JUHE_AI_SECRET`，否则敏感字段无法解密。

## 启动与验证

Windows：

```powershell
pwsh .\start.ps1
Invoke-WebRequest http://127.0.0.1:3000/health
Invoke-WebRequest http://127.0.0.1:3000/api/health
Invoke-WebRequest http://127.0.0.1:3000/
```

macOS/Linux：

```bash
bash ./start.sh
curl -i http://127.0.0.1:3000/health
curl -i http://127.0.0.1:3000/api/health
curl -I http://127.0.0.1:3000/
```

## 备份

必须备份：

```text
backend/.env
backend/data/juhe-ai.sqlite3
```

常驻运行、反向代理、端口开放、数据迁移和常见排障请继续查看 `docs/deploy/部署指南.md`。
