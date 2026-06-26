# Linux 部署指南

## 1. 推荐路径

| 场景 | 推荐方式 | 入口 |
| --- | --- | --- |
| 单机轻量部署 | 发布包 + `systemd` | [部署指南](../部署指南.md) |
| 单机容器部署 | Docker Compose 单容器 | [Docker 部署指南](../Docker部署指南.md) |
| 高并发部署 | Docker Compose + PostgreSQL + Redis | [高性能模式部署指南](../高性能模式部署指南.md) |
| 公网 HTTPS | Caddy 自动 HTTPS | [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) |
| 自动恢复 | systemd + watchdog | [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md) |
| 上游需要代理 | sing-box 本机代理 + 后台代理管理 | [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) |

## 2. 部署前检查

```bash
uname -a
df -h .
free -h
node -v
node -p "process.version + ' LTS=' + (process.release.lts || '非LTS')"
corepack --version || true
pnpm -v || true
ss -lntp | grep ':3000 ' || true
curl -I https://registry.npmjs.org/ || true
```

要求：

- Node.js 使用官方 LTS，当前支持 `22.x >= 22.13.0` 或 `24.x >= 24.11.0`。
- 端口 `3000` 未被占用，或已准备修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 如果服务器无法直连 npm、Docker Hub 或上游模型 API，先配置系统代理或 sing-box。
- 生产上只对公网开放 `80/443`；不要把 PostgreSQL、Redis、DB service、sing-box 或 juhe-ai `3000` 端口暴露到公网。

## 3. 发布包部署

```bash
tar -xzf juhe-ai-release.tar.gz
cd juhe-ai-release
cp -n backend/.env.example backend/.env
nano backend/.env
bash ./start.sh
```

生产目录建议使用固定路径和共享数据目录，避免升级时覆盖 `.env` 和数据：

```text
/opt/juhe-ai-lite/
  current -> releases/某次发布/juhe-ai-release
  releases/
  shared/backend.env
  shared/data/
  backups/
  logs/
  bin/run.sh
```

首次部署后把配置和数据移到 `shared/`，后续新 release 只切换 `current` 软链接：

```bash
sudo useradd --system --home /opt/juhe-ai-lite --shell /usr/sbin/nologin juhe || true
sudo install -d -o juhe -g juhe /opt/juhe-ai-lite/shared/data /opt/juhe-ai-lite/releases /opt/juhe-ai-lite/bin
cd /opt/juhe-ai-lite/releases/时间戳/juhe-ai-release
sudo cp -n backend/.env.example /opt/juhe-ai-lite/shared/backend.env
sudo chown juhe:juhe /opt/juhe-ai-lite/shared/backend.env
sudo nano /opt/juhe-ai-lite/shared/backend.env
ln -sfn /opt/juhe-ai-lite/shared/backend.env backend/.env
rm -rf backend/data
ln -sfn /opt/juhe-ai-lite/shared/data backend/data
ln -sfn /opt/juhe-ai-lite/releases/时间戳/juhe-ai-release /opt/juhe-ai-lite/current
```

`systemd` 推荐只调用固定包装脚本，不直接写死某次 release 路径：

```bash
sudo tee /opt/juhe-ai-lite/bin/run.sh >/dev/null <<'EOF'
#!/usr/bin/env bash
set -e
export NODE_ENV=production
cd /opt/juhe-ai-lite/current
exec bash ./start.sh
EOF
sudo chmod +x /opt/juhe-ai-lite/bin/run.sh
```

常驻运行推荐 `systemd`。写入服务文件：

```bash
sudo tee /etc/systemd/system/juhe-ai.service >/dev/null <<'EOF'
[Unit]
Description=Juhe AI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/juhe-ai-lite/current
ExecStart=/usr/bin/env bash /opt/juhe-ai-lite/bin/run.sh
Restart=always
RestartSec=5
User=juhe
Environment=NODE_ENV=production

[Install]
WantedBy=multi-user.target
EOF
```

常用命令：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now juhe-ai
sudo journalctl -u juhe-ai -f
```

发布切换：

```bash
sudo ln -sfn /opt/juhe-ai-lite/releases/新版本/juhe-ai-release /opt/juhe-ai-lite/current
sudo systemctl restart juhe-ai
sudo systemctl status juhe-ai --no-pager
```

当前新版本由主进程看护 DB service 和 worker，不要把 `worker.js` 或 `db-service.js` 单独注册成 systemd 服务。只有强杀、系统崩溃或持续 SQLite `database is locked` 时，再检查是否有脱离当前 server 的旧子进程。

## 4. Docker 差异

Linux Docker Engine 不一定内置 `host.docker.internal`。如果 juhe-ai 容器需要访问宿主机上的 sing-box 代理，可以二选一：

- 在 Compose override 中给 `juhe-ai` 增加 `extra_hosts: ["host.docker.internal:host-gateway"]`，后台代理 Host 填 `host.docker.internal`。
- 让 sing-box 监听宿主机内网 IP 或 Docker bridge 可达地址，后台代理 Host 填该 IP。

不要把 sing-box 的本地代理端口无鉴权暴露到公网；如果必须监听 `0.0.0.0`，用系统防火墙限制来源只允许本机、Docker bridge 或应用服务器。

如果宿主机 Caddy 反代 Docker 容器，`docker/.env` 建议：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

如果只是局域网 HTTP 临时验证，才把 `JUHE_AI_PUBLIC_BIND=0.0.0.0`，并保持 `JUHE_AI_TRUST_PROXY=false`。

## 5. HTTPS 和反向代理

Linux 生产默认推荐 Caddy 做 HTTPS：域名解析到服务器后，Caddy 会自动申请和续期免费证书。完整步骤见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

如果已经使用 Nginx，可以继续用 Nginx + Certbot；必须关闭 `/v1/*` 流式接口 buffering，并设置足够长的读写超时。备选示例见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

发布包 `backend/.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

只有 Caddy、Nginx 或可信负载均衡能访问 `127.0.0.1:3000` 时才开启 `JUHE_AI_TRUST_PROXY=true`。如果后端端口直接对公网开放，必须保持 `false`。

## 6. 端口和防火墙

公网服务器建议只放行：

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw deny 3000/tcp
sudo ufw deny 7890/tcp
```

使用 firewalld 时：

```bash
sudo firewall-cmd --permanent --add-service=http
sudo firewall-cmd --permanent --add-service=https
sudo firewall-cmd --reload
```

PostgreSQL、PgBouncer、Redis 和 sing-box 如果确实需要跨机器访问，只允许内网、VPN 或安全组白名单；不要用 `0.0.0.0/0` 放行。

真实客户端 IP 验证：

- 从两个不同网络访问 `https://ai.example.com/v1`。
- 在后台“使用记录”或“公开接口日志”确认 `clientIp` 不再全部是 `127.0.0.1`、Caddy 内网地址或负载均衡地址。
- 如果前面还有 CDN / 负载均衡，按 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) 的 `trusted_proxies` 配置可信来源。

## 7. 上游网络代理

juhe-ai 中转请求上游时，推荐在后台“代理管理”中新增代理，然后绑定到 AI 账户：

| 部署形态 | 代理类型 | Host | 端口 |
| --- | --- | --- | --- |
| 裸机同机 sing-box | `socks5h` | `127.0.0.1` | `7890` |
| Linux Docker + host-gateway | `socks5h` | `host.docker.internal` | `7890` |
| sing-box 独立代理机 | `socks5h` | 代理机内网 IP | `7890` |

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理；普通上游模型请求应优先使用账号绑定代理。

## 8. 状态检测和自动恢复

Linux 生产建议使用 systemd 守护主服务，并额外配置 watchdog 检查本机 `/__aisys__/health` 和 `/__aisys__/api/health`。watchdog 连续失败 2-3 次后再 `systemctl restart juhe-ai`，并设置启动宽限、冷却时间和窗口限频，避免配置错误或网络抖动导致重启风暴。完整策略见 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
