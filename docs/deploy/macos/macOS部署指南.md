# macOS 部署指南

## 1. 推荐路径

| 场景 | 推荐方式 | 入口 |
| --- | --- | --- |
| 本机轻量部署 | 发布包 + `start.sh` | [部署指南](../部署指南.md) |
| 长期运行 | release 目录 + user `launchd` | 本文第 4 节 |
| Docker 部署 | Docker Desktop + Compose | [Docker 部署指南](../Docker部署指南.md) |
| 公网 HTTPS | 宿主机 Caddy | [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) |
| 自动恢复 | `launchd` + watchdog | [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md) |
| 上游代理 | sing-box + 后台代理绑定 | [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) |

## 2. 部署前检查

```bash
sw_vers
uname -a
df -h .
node -v
node -p "process.version + ' LTS=' + (process.release.lts || '非LTS')"
corepack --version || true
pnpm -v || true
lsof -iTCP:3000 -sTCP:LISTEN || true
```

要求：

- Node.js 使用官方 LTS，当前支持 `22.x >= 22.13.0` 或 `24.x >= 24.11.0`。
- 生产只暴露 Caddy `80/443` 或 edge / 隧道回源端口；不要暴露 juhe-ai `3000`、sing-box `7890`、PostgreSQL 或 Redis。
- 远端 SSH、launchd 和手工预检必须使用同一条 Node LTS PATH。

## 3. 发布包目录

```text
~/juhe-ai-lite/
  current -> releases/某次发布/juhe-ai-release
  releases/
  shared/backend.env
  shared/data/
  backups/
  logs/
  bin/run.sh
  caddy-edge/
```

首次发布：

```bash
mkdir -p ~/juhe-ai-lite/releases/20260627 ~/juhe-ai-lite/shared/data ~/juhe-ai-lite/bin ~/juhe-ai-lite/logs
tar -xzf juhe-ai-release.tar.gz -C ~/juhe-ai-lite/releases/20260627
cd ~/juhe-ai-lite/releases/20260627/juhe-ai-release
cp -n backend/.env.example ~/juhe-ai-lite/shared/backend.env
rm -f backend/.env
ln -s "$HOME/juhe-ai-lite/shared/backend.env" backend/.env
rm -rf backend/data
ln -s "$HOME/juhe-ai-lite/shared/data" backend/data
ln -sfn "$HOME/juhe-ai-lite/releases/20260627/juhe-ai-release" "$HOME/juhe-ai-lite/current"
```

`~/juhe-ai-lite/shared/backend.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
```

## 4. launchd 常驻

写入固定启动脚本：

```bash
cat > ~/juhe-ai-lite/bin/run.sh <<'EOF'
#!/usr/bin/env bash
set -e
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export NODE_ENV=production
cd "$HOME/juhe-ai-lite/current"
exec bash ./start.sh
EOF
chmod +x ~/juhe-ai-lite/bin/run.sh
```

写入用户级 LaunchAgent：

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.juhe-ai.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.juhe-ai</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$HOME/juhe-ai-lite/bin/run.sh</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$HOME/juhe-ai-lite/current</string>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$HOME/juhe-ai-lite/logs/launchd.out.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/juhe-ai-lite/logs/launchd.err.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$UID" ~/Library/LaunchAgents/com.juhe-ai.plist 2>/dev/null || true
launchctl bootstrap "gui/$UID" ~/Library/LaunchAgents/com.juhe-ai.plist
launchctl kickstart -k "gui/$UID/com.juhe-ai"
```

验证：

```bash
launchctl print "gui/$UID/com.juhe-ai" | head
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
```

升级只切 `current` 并重启：

```bash
ln -sfn "$HOME/juhe-ai-lite/releases/新版本/juhe-ai-release" "$HOME/juhe-ai-lite/current"
launchctl kickstart -k "gui/$UID/com.juhe-ai"
sleep 3
launchctl print "gui/$UID/com.juhe-ai" | head
lsof -nP -iTCP:3000 -sTCP:LISTEN
pgrep -af 'node|juhe-ai' | grep 'juhe-ai-lite' || true
```

主进程会看护 DB service 和 worker；不要把 `worker.js` 或 `db-service.js` 单独注册为 launchd 服务。升级后必须确认只有当前 release 的主进程监听 `3000`，没有旧 release 路径下的 node 子进程残留。持续 SQLite `database is locked`、PG 连接数异常或管理接口忽快忽慢时，先查旧子进程残留和端口监听，再查数据库。

## 5. HTTPS 和端口边界

macOS 生产建议宿主机 Caddy 监听 `80/443`，反向代理到 `127.0.0.1:3000`。完整 Caddyfile 见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

边界：

- 家庭路由器或云安全组只转发 `80/443` 到 Caddy。
- juhe-ai 绑定 `127.0.0.1:3000`。
- sing-box mixed / socks 端口只监听本机或受信内网。
- macOS Application Firewall 不是精确端口防火墙；端口边界优先靠绑定地址、路由器映射、Caddy 和隧道白名单。

真实客户端 IP 需要 `JUHE_AI_TRUST_PROXY=true`，且后端端口不能被客户端绕过反代直连。多层 edge / 隧道见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) 的真实 IP 段落。

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

长期运行用 watchdog 检查本机 `/__aisys__/health` 和 `/__aisys__/api/health`。连续失败后终止当前 server，由 launchd `KeepAlive` 拉起；公网域名失败但本机 health 正常时不重启主服务。策略见 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。

## 8. 上游网络代理

裸机同机 sing-box：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理；普通上游请求优先使用账号绑定代理。
