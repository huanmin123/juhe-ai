# 跨平台部署文档

> 本文只说明如何把发布包部署到 Windows、macOS、Linux。如何构建发布包见 `docs/deploy/build.md`。

## 1. 部署兼容矩阵

同一个发布包可以部署到三类目标机器：

| 部署目标 | 推荐压缩包 | 启动脚本 | 常驻运行建议 |
| --- | --- | --- | --- |
| Windows | `juhe-ai-release.zip` | `pwsh ./start.ps1` | NSSM / PM2 / 任务计划程序 |
| macOS | `juhe-ai-release.tar.gz` | `bash ./start.sh` | launchd / PM2 |
| Linux | `juhe-ai-release.tar.gz` | `bash ./start.sh` | systemd / PM2 |

不要跨系统复制 `node_modules`。目标机器首次启动时会安装本机生产依赖。

## 2. 部署前必须检测

### Windows 目标

```powershell
$PSVersionTable.PSVersion
Get-Location
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version
pnpm -v
netstat -ano | Select-String ':3000'
```

### macOS 目标

```bash
pwd
sw_vers
uname -a
df -h .
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
lsof -iTCP:3000 -sTCP:LISTEN || true
```

### Linux 目标

```bash
pwd
uname -a
df -h .
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
ss -lntp | grep ':3000 ' || true
curl -I https://registry.npmjs.org/ || true
```

必须确认：

- Node.js 支持 `node:sqlite`，建议 Node.js 22.5+。
- pnpm 可用，或 corepack 可用且能启用 pnpm。
- 首次启动能访问 npm registry，或已配置可用代理/缓存。
- 默认端口 `3000` 未被占用；占用时修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 当前部署目录有写入权限和足够磁盘空间。

## 3. 解压发布包

### Windows

推荐 zip：

```powershell
Expand-Archive .\juhe-ai-release.zip -DestinationPath . -Force
Set-Location .\juhe-ai-release
```

如果拿到的是 `tar.gz`：

```powershell
tar -xzf .\juhe-ai-release.tar.gz
Set-Location .\juhe-ai-release
```

### macOS / Linux

推荐 `tar.gz`：

```bash
tar -xzf juhe-ai-release.tar.gz
cd juhe-ai-release
```

如果拿到的是 zip：

```bash
unzip juhe-ai-release.zip
cd juhe-ai-release
```

### 文件检查

Windows：

```powershell
Test-Path .\start.ps1
Test-Path .\start.sh
Test-Path .\backend\dist\server.js
Test-Path .\frontend\dist\index.html
Test-Path .\pnpm-lock.yaml
```

macOS / Linux：

```bash
test -f start.sh
test -f start.ps1
test -f backend/dist/server.js
test -f frontend/dist/index.html
test -f pnpm-lock.yaml
```

## 4. 配置 `backend/.env`

### 创建配置文件

Windows：

```powershell
Copy-Item .\backend\.env.example .\backend\.env -ErrorAction SilentlyContinue
notepad .\backend\.env
```

macOS / Linux：

```bash
cp -n backend/.env.example backend/.env
nano backend/.env
```

### 最低配置

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=换成一串足够长且固定保存的随机密钥
JUHE_AI_OAUTH_PROXY_URL=
```

配置规则：

- 反向代理或本机访问：`JUHE_AI_HOST=127.0.0.1`。
- 局域网/公网直连访问：`JUHE_AI_HOST=0.0.0.0`，并开放系统防火墙或云安全组端口。
- 新部署必须修改 `JUHE_AI_SECRET`，不要使用示例值。
- 迁移旧数据必须沿用旧 `JUHE_AI_SECRET`。
- `JUHE_AI_SECRET` 改错会导致 OAuth token、上游 API Key、代理密码等敏感字段无法解密。

## 5. 启动

### Windows

```powershell
pwsh .\start.ps1
```

### macOS / Linux

```bash
bash ./start.sh
```

启动脚本会：

- 检查 Node.js 和 `node:sqlite`。
- 尝试通过 corepack 启用 pnpm。
- 缺少 `backend/.env` 时自动创建。
- 创建 `backend/data`。
- 首次安装目标平台生产依赖。
- 启动 `node backend/dist/server.js`。

## 6. 验证

### Windows

```powershell
Invoke-WebRequest http://127.0.0.1:3000/health
Invoke-WebRequest http://127.0.0.1:3000/api/health
Invoke-WebRequest http://127.0.0.1:3000/
```

### macOS / Linux

```bash
curl -i http://127.0.0.1:3000/health
curl -i http://127.0.0.1:3000/api/health
curl -I http://127.0.0.1:3000/
```

期望：

- `/health` 返回 `200` 和 `status: ok`。
- `/api/health` 返回 `200` 和 `status: ok`。
- `/` 返回前端页面。

浏览器访问：

```text
http://127.0.0.1:3000/
```

如果使用公网或域名，按实际地址访问。

## 7. 常驻运行

### Windows

推荐：

- NSSM 注册 Windows Service。
- PM2 Windows 服务化方案。
- 任务计划程序开机启动 `pwsh -File start.ps1`。

PM2 示例：

```powershell
npm install -g pm2
pm2 start .\backend\dist\server.js --name juhe-ai --cwd (Get-Location).Path
pm2 save
```

### macOS

推荐：

- launchd。
- PM2。
- Caddy/Nginx 反向代理。

PM2 示例：

```bash
npm install -g pm2
cd /你的路径/juhe-ai-release
pm2 start backend/dist/server.js --name juhe-ai --cwd /你的路径/juhe-ai-release
pm2 save
```

### Linux

推荐：

- systemd。
- PM2。
- Nginx/Caddy 反向代理和 HTTPS。

PM2 示例：

```bash
npm install -g pm2
cd /你的路径/juhe-ai-release
pm2 start backend/dist/server.js --name juhe-ai --cwd /你的路径/juhe-ai-release
pm2 save
pm2 startup
```

systemd 示例：

```ini
[Unit]
Description=Juhe AI
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/juhe-ai-release
ExecStart=/usr/bin/node /opt/juhe-ai-release/backend/dist/server.js
Restart=always
RestartSec=5
User=你的Linux用户
Environment=NODE_ENV=production

[Install]
WantedBy=multi-user.target
```

## 8. Nginx 反向代理示例

Linux/macOS 使用 Nginx 时可参考：

```nginx
server {
    listen 80;
    server_name 你的域名;

    client_max_body_size 20m;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /v1/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## 9. 数据备份和迁移

默认数据库：

```text
backend/data/juhe-ai.sqlite3
```

必须备份：

```text
backend/.env
backend/data/juhe-ai.sqlite3
```

Windows 备份：

```powershell
New-Item -ItemType Directory -Force backups | Out-Null
Copy-Item .\backend\.env ".\backups\backend.env.$(Get-Date -Format yyyyMMddHHmmss)"
Copy-Item .\backend\data\juhe-ai.sqlite3 ".\backups\juhe-ai.sqlite3.$(Get-Date -Format yyyyMMddHHmmss)"
```

macOS / Linux 备份：

```bash
mkdir -p backups
cp backend/.env backups/backend.env.$(date +%Y%m%d%H%M%S)
cp backend/data/juhe-ai.sqlite3 backups/juhe-ai.sqlite3.$(date +%Y%m%d%H%M%S)
```

迁移时：

1. 解压新发布包。
2. 复制旧 `backend/.env` 到新发布包的 `backend/.env`。
3. 复制旧 `backend/data/juhe-ai.sqlite3` 到新发布包同路径。
4. 启动新服务。
5. 验证登录、账户、API Key 和网关。

## 10. 常见排障

- `node:sqlite` 失败：升级 Node.js 到 22.5+ 或更新 LTS。
- `pnpm install` 失败：检查 npm registry 网络或代理配置。
- 页面能开但 API 失败：检查 `/api` 是否到同一后端。
- `/v1` 流式断开：反向代理关闭 buffering，并增大超时。
- 敏感字段无法解密：找回旧 `JUHE_AI_SECRET`。
- 端口外部访问失败：直连用 `JUHE_AI_HOST=0.0.0.0`，并开放防火墙/安全组端口。

## 11. 给 AI 的部署摘要

1. 先判断目标系统：Windows、macOS 或 Linux。
2. 检查 Node.js、`node:sqlite`、pnpm/corepack、端口、网络、磁盘和权限。
3. 按目标系统解压 zip 或 tar.gz。
4. 检查 `start.sh`、`start.ps1`、`backend/dist/server.js`、`frontend/dist/index.html`。
5. 创建并审阅 `backend/.env`。
6. 根据访问方式设置 `JUHE_AI_HOST`。
7. 确认 `JUHE_AI_SECRET`：新部署生成强随机值，迁移部署沿用旧值。
8. Windows 执行 `pwsh ./start.ps1`；macOS/Linux 执行 `bash ./start.sh`。
9. 验证 `/health`、`/api/health`、`/`。
10. 配置目标平台常驻运行。
11. 备份 `.env` 和 SQLite 数据库。
