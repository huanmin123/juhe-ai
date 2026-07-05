# Windows 部署指南

## 1. 推荐路径

| 场景 | 推荐方式 | 入口 |
| --- | --- | --- |
| Windows Server 轻量部署 | zip 发布包 + PowerShell 7 | [部署指南](../部署指南.md) |
| 长期运行 | NSSM / 任务计划程序 | 本文第 4 节 |
| Docker 部署 | Docker Desktop + Compose | [Docker 部署指南](../Docker部署指南.md) |
| 公网 HTTPS | 宿主机 Caddy | [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) |
| 自动恢复 | Windows Service + PowerShell watchdog | [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md) |
| 上游代理 | sing-box + 后台代理绑定 | [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) |

## 2. 部署前检查

```powershell
$PSVersionTable.PSVersion
node -v
node -p "process.version + ' LTS=' + (process.release.lts || '非LTS')"
corepack --version
pnpm -v
netstat -ano | Select-String ':3000'
```

要求：

- 用 PowerShell 7 执行部署命令。
- Node.js 使用官方 LTS，当前支持 `22.x >= 22.13.0` 或 `24.x >= 24.11.0`。
- 生产只开放 `80/443`；不要暴露 juhe-ai `3000`、sing-box `7890`、PostgreSQL 或 Redis。

## 3. 发布包目录

```text
C:\juhe-ai-lite\
  current -> releases\某次发布\juhe-ai-release
  releases\
  shared\backend.env
  shared\data\
  backups\
  logs\
  bin\run.ps1
```

首次发布：

```powershell
$Root = 'C:\juhe-ai-lite'
$Release = Join-Path $Root 'releases\20260627'
New-Item -ItemType Directory -Force $Release, "$Root\shared\data", "$Root\bin", "$Root\logs", "$Root\backups" | Out-Null
Expand-Archive .\juhe-ai-release.zip -DestinationPath $Release -Force
$App = Join-Path $Release 'juhe-ai-release'
Copy-Item "$App\backend\.env.example" "$Root\shared\backend.env" -ErrorAction SilentlyContinue
Set-Location $App
if (Test-Path .\backend\data) { Remove-Item -LiteralPath .\backend\data -Recurse -Force }
New-Item -ItemType Junction -Path .\backend\data -Target "$Root\shared\data" | Out-Null
if (Test-Path "$Root\current") { Remove-Item -LiteralPath "$Root\current" -Force }
New-Item -ItemType Junction -Path "$Root\current" -Target $App | Out-Null
notepad "$Root\shared\backend.env"
```

`C:\juhe-ai-lite\shared\backend.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
```

验证：

```powershell
$env:JUHE_AI_ENV_FILE = 'C:\juhe-ai-lite\shared\backend.env'
Set-Location 'C:\juhe-ai-lite\current'
pwsh -NoProfile -ExecutionPolicy Bypass -File .\start.ps1
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
```

## 4. 常驻运行

写入固定启动脚本：

```powershell
@'
$ErrorActionPreference = 'Stop'
$env:NODE_ENV = 'production'
$env:JUHE_AI_ENV_FILE = 'C:\juhe-ai-lite\shared\backend.env'
Set-Location 'C:\juhe-ai-lite\current'
pwsh -NoProfile -ExecutionPolicy Bypass -File .\start.ps1
'@ | Set-Content -Path 'C:\juhe-ai-lite\bin\run.ps1' -Encoding UTF8
```

NSSM：

```powershell
nssm install JuheAI "C:\Program Files\PowerShell\7\pwsh.exe" "-NoProfile -ExecutionPolicy Bypass -File C:\juhe-ai-lite\bin\run.ps1"
nssm set JuheAI AppDirectory "C:\juhe-ai-lite\current"
nssm set JuheAI Start SERVICE_AUTO_START
nssm set JuheAI AppStdout "C:\juhe-ai-lite\logs\service.out.log"
nssm set JuheAI AppStderr "C:\juhe-ai-lite\logs\service.err.log"
nssm start JuheAI
```

升级：

```powershell
$Root = 'C:\juhe-ai-lite'
$App = 'C:\juhe-ai-lite\releases\新版本\juhe-ai-release'
if (Test-Path "$Root\current") { Remove-Item -LiteralPath "$Root\current" -Force }
New-Item -ItemType Junction -Path "$Root\current" -Target $App | Out-Null
nssm restart JuheAI
Start-Sleep -Seconds 3
Get-NetTCPConnection -LocalPort 3000 -State Listen | Select-Object LocalAddress, LocalPort, OwningProcess
Get-CimInstance Win32_Process -Filter "name = 'node.exe'" |
  Where-Object { $_.CommandLine -like '*juhe-ai-lite*' } |
  Select-Object ProcessId, CommandLine
```

常驻服务只守护主进程；不要把 worker 或 DB service 单独注册成服务。升级后必须确认 `3000` 只由当前服务进程监听，`node.exe` 命令行里没有旧 release 路径。

## 5. HTTPS 和防火墙

Caddy for Windows 监听 `80/443`，反向代理到 `127.0.0.1:3000`。Caddyfile 基线见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

Windows Server 入口只放行 `80/443`：

```powershell
New-NetFirewallRule -DisplayName "Juhe AI HTTP" -Direction Inbound -Protocol TCP -LocalPort 80 -Action Allow
New-NetFirewallRule -DisplayName "Juhe AI HTTPS" -Direction Inbound -Protocol TCP -LocalPort 443 -Action Allow
New-NetFirewallRule -DisplayName "Block Juhe AI Backend 3000" -Direction Inbound -Protocol TCP -LocalPort 3000 -Action Block
New-NetFirewallRule -DisplayName "Block sing-box 7890" -Direction Inbound -Protocol TCP -LocalPort 7890 -Action Block
```

真实客户端 IP 需要 `JUHE_AI_TRUST_PROXY=true`，且后端端口不能被客户端绕过 Caddy 直连。多层代理见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

## 6. Docker Desktop 差异

Docker 容器访问宿主机 sing-box：

```text
类型：socks5h
Host：host.docker.internal
端口：7890
```

宿主机 Caddy 反代 Docker 容器时，`docker/.env`：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

## 7. 自动恢复

用 NSSM / Windows Service 守护主服务，再用 watchdog 连续检查 `http://127.0.0.1:3000/__aisys__/health` 和 `/__aisys__/api/health`。两个本机 health 连续失败才重启服务；公网域名失败但本机 health 正常时先查 Caddy、DNS、证书和防火墙。

重启目标：

```powershell
Restart-Service JuheAI
# 或
nssm restart JuheAI
```

完整防抖和限频见 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。

## 8. 上游网络代理

裸机同机 sing-box：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理；普通上游请求优先使用账号绑定代理。
