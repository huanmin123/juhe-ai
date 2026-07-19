# Caddy 自动 HTTPS 部署指南

## 1. 推荐结论

juhe-ai 公网部署默认推荐 Caddy：

- 免费证书自动申请和自动续期，不需要单独写 cron。
- Caddyfile 配置短，适合单机轻量部署。
- 反向代理支持关闭流式响应缓冲，适合 `/v1` SSE 流式输出。
- Linux、macOS、Windows 都可安装；Docker 和发布包部署都能复用同一套入口思路。

如果你已经有成熟 Nginx 配置和证书续期体系，可以跳到本文第 8 节使用 Nginx + Certbot。

## 2. 前置条件

| 项目 | 要求 |
| --- | --- |
| 域名 | 准备一个域名，例如 `ai.example.com` |
| DNS | `A` / `AAAA` 记录指向 juhe-ai 所在服务器公网 IP |
| 端口 | 服务器安全组和系统防火墙放行 `80/tcp`、`443/tcp` |
| juhe-ai | 后端监听本机端口，例如 `127.0.0.1:3000` |
| 代理冲突 | 同一台机器不能再有其他服务占用 `80/443` |

证书自动签发依赖公网 CA 能访问你的域名。只有 IP、内网域名或未解析到服务器的域名，不能直接签发公开可信证书。

## 3. 安装 Caddy

优先以 Caddy 官方安装文档为准。常见方式如下。

Linux Debian / Ubuntu：

```bash
sudo apt update
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

macOS：

```bash
brew install caddy
```

Windows：

```powershell
winget install CaddyServer.Caddy
```

Windows 如果不用 `winget`，也可以下载官方 release 包，把 `caddy.exe` 放到固定目录后用 NSSM 或任务计划程序守护。

以上标准安装方式适合 Caddy 直接终止 HTTPS 的 HTTP 反向代理。公网 Edge 做 L4 TLS 透传、或回源 listener 接收 PROXY protocol 时，需要对应的 layer4 / proxy_protocol 模块；标准包不保证包含。必须用实际运行的同一个 binary 执行 `caddy list-modules`、`adapt` 和 `validate`。完整边界见 [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md)。

## 4. 配置 juhe-ai 环境变量

发布包部署时修改 `backend/.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_COOKIE_SAME_SITE=lax
JUHE_AI_TRUST_PROXY=true
```

Docker 部署时修改 `docker/.env`：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

`JUHE_AI_PUBLIC_BIND=127.0.0.1` 时，容器端口只发布到宿主机本机地址。Caddy 可以直接反代 `127.0.0.1:3000`，公网只暴露 Caddy 的 `80/443`。

## 5. 写入 Caddyfile

Linux 默认路径：

```bash
sudo nano /etc/caddy/Caddyfile
```

推荐配置：

```caddyfile
ai.example.com {
    encode zstd gzip

    reverse_proxy 127.0.0.1:3000 {
        flush_interval -1
    }
}
```

说明：

- `ai.example.com` 改成你的真实域名。
- `reverse_proxy` 指向 juhe-ai 实际监听地址。
- `flush_interval -1` 用于降低 `/v1` 流式响应被代理缓冲的概率。
- 不需要手动配置证书路径；Caddy 会自动申请、保存和续期证书。

如果 juhe-ai 与 Caddy 不在同一台机器，把 `127.0.0.1:3000` 改成 juhe-ai 内网地址，并确保只允许 Caddy 访问后端端口。

## 6. 启动和验证

Linux：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl enable --now caddy
sudo systemctl reload caddy
sudo journalctl -u caddy -n 100 --no-pager
curl -I https://ai.example.com/__aisys__/
curl -i https://ai.example.com/__aisys__/health
```

macOS：

```bash
caddy validate --config ./Caddyfile
caddy run --config ./Caddyfile
curl -I https://ai.example.com/__aisys__/
```

Windows PowerShell：

```powershell
caddy validate --config .\Caddyfile
caddy run --config .\Caddyfile
Invoke-WebRequest https://ai.example.com/__aisys__/
```

期望：

- 浏览器访问 `https://ai.example.com/__aisys__/` 可以打开后台。
- `https://ai.example.com/v1` 可作为 OpenAI 兼容客户端 Base URL。
- Caddy 日志没有证书签发失败、端口占用或 DNS 校验失败。

## 7. 续期和运维

Caddy 会在证书到期前自动续期。正常情况下不需要写计划任务。

日常只需要关注：

- 域名解析仍然指向当前服务器。
- `80/443` 没有被其他服务占用。
- Caddy 数据目录没有被误删。
- 服务器时间准确。

如果证书签发失败，优先检查：

```bash
dig ai.example.com +short
sudo ss -lntp | grep -E ':80|:443'
sudo journalctl -u caddy -n 200 --no-pager
```

Windows 可用：

```powershell
Resolve-DnsName ai.example.com
netstat -ano | Select-String ':80|:443'
```

## 8. Nginx + Certbot 备选

只有已经在维护 Nginx 时才建议使用这个方案。新部署优先用 Caddy。

Debian / Ubuntu 示例：

```bash
sudo apt update
sudo apt install -y nginx certbot python3-certbot-nginx
```

Nginx 站点示例：

高并发长连接还应在主 `nginx.conf` 中为 worker 设置起步上限，并同步保证 Nginx 服务的实际 `nofile` 不低于 `65536`：

```nginx
worker_processes auto;
worker_rlimit_nofile 65536;

events {
    worker_connections 16384;
}
```

不要无证据开启 `multi_accept on`。

```nginx
server {
    listen 80;
    server_name ai.example.com;

    client_max_body_size 20m;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_cache off;
        proxy_socket_keepalive on;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

签发证书：

```bash
sudo certbot --nginx -d ai.example.com
sudo certbot renew --dry-run
systemctl list-timers | grep certbot || true
```

Certbot 通常会安装 systemd timer 或 cron 来自动续期；上线前必须用 `renew --dry-run` 验证续期链路。

## 9. 家庭宽带 Edge / WireGuard 回源模式

普通服务器部署通常让 Caddy 直接监听公网 `80/443` 并终止 TLS。高并发家庭 / 内网回源使用另一种互斥模式：

```text
公网 Edge Caddy layer4 -> WireGuard -> 家里主机 Caddy 终止 TLS -> Nginx/juhe-ai
```

Edge 不写 HTTP `reverse_proxy`，否则 TLS 会在 Edge 终止并与回源 TLS + PROXY v2 模式冲突。回源 Caddy 的 listener 只绑定精确 WireGuard 地址；wrapper 顺序固定为 `proxy_protocol` 再 `tls`，`allow` 只放 Edge peer `/32`，并使用 `fallback_policy require`。WireGuard、Caddy layer4、五 Edge 正式拓扑、完整示例、系统参数、切换和回滚统一见 [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md)。

## 10. 真实客户端 IP

juhe-ai 后台展示和调度使用的 `clientIp` 来自 Express `req.ip`。只有 `JUHE_AI_TRUST_PROXY=true` 或配置具体代理跳数时，Express 才会从受信任的反向代理头里计算客户端 IP。否则后端只会看到直接连接它的地址，例如 `127.0.0.1`、Caddy 地址、隧道地址或公网 edge 地址。

发布包部署时建议：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_TRUST_PROXY=true
```

Docker 部署时确保容器端口只给本机 Caddy 或可信内网访问，不要把后端端口直接暴露给公网后再开启 `trust proxy`。

Caddy 直接作为公网第一入口时，基础配置即可：

```caddyfile
ai.example.com {
    encode zstd gzip

    reverse_proxy 127.0.0.1:3000 {
        flush_interval -1
    }
}
```

Caddy 会给上游设置或追加 `X-Forwarded-For`，并设置 `X-Forwarded-Proto`、`X-Forwarded-Host`。juhe-ai 信任的是 Caddy 本机连接，不是公网客户端自带的伪造头。

如果 Caddy 前面还有 CDN、公网 edge、云负载均衡或另一台 VPS 反代，必须让 Caddy 只信任这些前置代理的地址段：

```caddyfile
{
    servers {
        trusted_proxies static 203.0.113.10/32 100.64.0.0/10
        trusted_proxies_strict
    }
}

ai.example.com {
    encode zstd gzip

    reverse_proxy 127.0.0.1:3000 {
        flush_interval -1
    }
}
```

把 `203.0.113.10/32`、`100.64.0.0/10` 换成真实可信的前置代理、隧道或内网回源地址段。不要为了省事信任 `0.0.0.0/0`。

如果前置入口通过 PROXY v2 把真实来源传给家庭 Mac 或内网 Caddy，按 [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md) 配置精确 WireGuard listener。不要同时让普通公网请求绕过 PROXY protocol 入口直连 Caddy。

验证方式：

- 从两个不同网络访问 `https://ai.example.com/v1`。
- 在后台“使用记录”或“公开接口日志”查看 `clientIp`。
- 如果所有请求都显示同一个内网、隧道或 edge IP，说明前置代理没有传递真实来源，或 juhe-ai 没有开启 `JUHE_AI_TRUST_PROXY`。

## 11. 与上游网络代理的关系

HTTPS 反向代理只处理客户端到 juhe-ai 的入口安全：

```text
客户端 -> HTTPS/Caddy -> juhe-ai -> 账号代理/sing-box -> 上游模型 API
```

如果服务器无法访问上游模型 API，仍然需要按 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) 部署代理，并在后台“代理管理”绑定到 AI 账户。`JUHE_AI_OAUTH_PROXY_URL` 只影响 OpenAI OAuth token 换取 / 刷新，不是普通模型请求的全局代理。

## 12. 官方参考

- Caddy Automatic HTTPS：[https://caddyserver.com/docs/automatic-https](https://caddyserver.com/docs/automatic-https)
- Caddy 安装文档：[https://caddyserver.com/docs/install](https://caddyserver.com/docs/install)
- Caddy `reverse_proxy` 指令：[https://caddyserver.com/docs/caddyfile/directives/reverse_proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
- Caddy 全局 `trusted_proxies`：[https://caddyserver.com/docs/caddyfile/options#trusted-proxies](https://caddyserver.com/docs/caddyfile/options#trusted-proxies)
- Certbot 使用文档：[https://eff-certbot.readthedocs.io/en/stable/using.html](https://eff-certbot.readthedocs.io/en/stable/using.html)
