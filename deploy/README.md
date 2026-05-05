# 跨平台发布包运行说明

> 这是发布包内置说明。构建说明见 `docs/deploy/构建指南.md`，部署说明见 `docs/deploy/部署指南.md`。

## 1. 选择启动脚本

| 目标平台 | 启动命令 |
| --- | --- |
| Windows | `pwsh ./start.ps1` |
| macOS | `bash ./start.sh` |
| Linux | `bash ./start.sh` |

发布包可以来自 Windows、macOS 或 Linux 任一打包平台。不要跨系统复制 `node_modules`，首次启动会安装目标平台自己的生产依赖。

## 2. 部署前必须检测

### Windows 目标

```powershell
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version
pnpm -v
netstat -ano | Select-String ':3000'
```

### macOS 目标

```bash
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
lsof -iTCP:3000 -sTCP:LISTEN || true
```

### Linux 目标

```bash
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
ss -lntp | grep ':3000 ' || true
curl -I https://registry.npmjs.org/ || true
```

必须确认：

- Node.js 支持 `node:sqlite`，建议 Node.js 22.5+ 或更新 LTS。
- pnpm 可用，或 corepack 可用且能启用 pnpm。
- 默认端口 `3000` 没有冲突；冲突时修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 当前目录有写入权限和足够磁盘空间。

## 3. 解压发布包

Windows 推荐 zip：

```powershell
Expand-Archive .\juhe-ai-release.zip -DestinationPath . -Force
Set-Location .\juhe-ai-release
```

macOS/Linux 推荐 tar.gz：

```bash
tar -xzf juhe-ai-release.tar.gz
cd juhe-ai-release
```

## 4. 必须配置

正式上线前编辑 `backend/.env`。

Windows：

```powershell
Copy-Item .\backend\.env.example .\backend\.env -ErrorAction SilentlyContinue
notepad .\backend\.env
```

macOS/Linux：

```bash
cp -n backend/.env.example backend/.env
nano backend/.env
```

最低配置：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=换成一串足够长且固定保存的随机密钥
JUHE_AI_OAUTH_PROXY_URL=
```

配置规则：

- 使用反向代理：`JUHE_AI_HOST=127.0.0.1`。
- 需要直连访问：`JUHE_AI_HOST=0.0.0.0`，并开放系统防火墙/安全组端口。
- 新部署必须修改 `JUHE_AI_SECRET`。
- 迁移旧数据必须沿用旧 `JUHE_AI_SECRET`，否则敏感字段无法解密。

## 5. 启动

Windows：

```powershell
pwsh .\start.ps1
```

macOS/Linux：

```bash
bash ./start.sh
```

启动脚本会检查 `node:sqlite`、启用 pnpm、创建 `backend/data`、安装后端生产依赖并启动后端。Web/API 主进程启动后会自动 fork 并看护独立 background worker 进程；后台统计、日志索引、审计落库和清理任务不在主进程事件循环里执行。

## 6. 启动后验证

Windows：

```powershell
Invoke-WebRequest http://127.0.0.1:3000/health
Invoke-WebRequest http://127.0.0.1:3000/api/health
Invoke-WebRequest http://127.0.0.1:3000/
```

macOS/Linux：

```bash
curl -i http://127.0.0.1:3000/health
curl -i http://127.0.0.1:3000/api/health
curl -I http://127.0.0.1:3000/
```

## 7. 常驻运行

- Windows：建议用 NSSM、PM2 或任务计划程序。
- macOS：建议用 launchd 或 PM2。
- Linux：建议用 PM2 或 systemd。

不要只依赖 SSH 或终端前台进程。

## 8. 数据和备份

必须备份：

```text
backend/.env
backend/data/juhe-ai.sqlite3
```

## 9. 常见排障

- `node:sqlite` 失败：升级 Node.js 到 22.5+ 或更新 LTS。
- `pnpm install` 失败：检查 npm registry 网络或代理配置。
- 页面能开但 API 失败：检查 `/api` 是否到同一后端。
- `/v1` 流式断开：反向代理关闭 buffering，并增大超时。
- 敏感字段无法解密：找回旧 `JUHE_AI_SECRET`。
- 端口外部访问失败：直连用 `JUHE_AI_HOST=0.0.0.0`，并开放防火墙/安全组端口。


