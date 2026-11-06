# Nginx 入口部署（替代 compose 内置 Caddy）

> 适用：希望用宿主机 Nginx 或独立 Nginx 容器终止 TLS、统一接入多个站点的环境。默认形态仍是 compose 内置 Caddy（见[部署指南](../部署指南.md)与 [HTTPS/反向代理](../https/README.md)）；本文只写 Nginx 形态的差异与完整配置。
>
> 域名、证书路径、上游地址均使用占位符；生产实例事实只在 `.local/project-resources/prod/` 留档。

## 0. 先选形态

| 形态 | 适用 | 关键差异 |
| --- | --- | --- |
| 内置 Caddy（默认） | 单机、只需本项目 | 自动 ACME，零额外配置；不能与其他入口共用 80/443 |
| 宿主机 Nginx | 已有 Nginx、需要统一入口/多站点/WAF | 需停用 compose caddy，并把 gateway 的 3000 发布到宿主回环 |
| Nginx 容器 | 希望入口也由 compose 管理 | 加入 `juhe-ai_default` 网络，直接反代固定 IP `172.18.0.10:3000`，无需发布 gateway 3000 |

不推荐 **Nginx → Caddy → gateway** 的双层反代：多一层缓冲和超时，SSE 与客户端交接的故障面都会放大；确需保留 Caddy 时，应让它只监听内网 HTTP 端口，不能同时占用 80/443。

## 1. 前置条件

- 域名 A 记录已指向本机公网 IP；安全组/防火墙放行 `80`、`443`（TCP），需要 HTTP/3 时再加 `443/udp`（Nginx 需另编译 QUIC 模块，本文不展开）。
- 已按[部署指南](../部署指南.md)完成应用栈部署，`gateway` healthy。
- Nginx ≥ 1.24；本文配置使用 `http2 on;`（1.25.1+），更老版本把 listen 行改成 `listen 443 ssl http2;`。
- 宿主机安装 `nginx`、`certbot`；Debian/Ubuntu：

```sh
apt update
apt install -y nginx certbot python3-certbot-nginx
nginx -v
systemctl enable --now nginx
```

## 2. 宿主机 Nginx 形态

### 2.1 停用 compose 内置 Caddy

内置 caddy 会占用 80/443，必须先停。推荐给 caddy 服务加 profile，保留随时回退能力：

```yaml
# docker/single-server/compose.yml 的 caddy 服务
  caddy:
    profiles: ["caddy"]   # 默认 up 不再启动；需要回退时 docker compose --profile caddy up -d caddy
```

```sh
cd /opt/juhe-ai
docker compose stop caddy
docker compose rm -f caddy
ss -lntp | grep -E ':(80|443)\b' || echo 'ports free'
```

不要在 Nginx 运行时再执行 `docker compose --profile caddy up -d caddy`，端口会冲突。`deploy.sh` 的 `docker compose up -d` 不会启动带 profile 的服务，日常发布不受影响。

> **与 `deploy.sh` 的关系**：`deploy.sh` 会把本地 `docker/single-server/compose.yml` 同步到服务器（md5 漂移即覆盖）。Nginx 形态必须在**本地仓库副本**里同样保留 `profiles: ["caddy"]` 与 gateway 的 `127.0.0.1:3000:3000` 映射，否则下一次发布会把服务器上的改动覆盖回去。只想手工发布时，按[部署指南 §9.3](../部署指南.md) 执行，不经过配置同步。

### 2.2 把 gateway 发布到宿主回环

compose 默认只发布健康端口 `127.0.0.1:3306`，宿主 Nginx 需要访问 `3000`。在 gateway 的 `ports` 增加一行：

```yaml
    ports:
      - "127.0.0.1:3306:3306"
      - "127.0.0.1:3000:3000"   # 仅供宿主 Nginx，禁止改成 0.0.0.0
```

```sh
docker compose up -d gateway
curl -fsS http://127.0.0.1:3000/__aisys__/health
```

### 2.3 先拿证书（HTTP-only 引导）

创建 `/etc/nginx/conf.d/juhe-ai.conf` 的临时 HTTP 配置，只做 ACME 校验与跳转：

```nginx
server {
    listen 80;
    server_name <domain>;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}
```

```sh
mkdir -p /var/www/certbot
nginx -t && systemctl reload nginx
certbot certonly --webroot -w /var/www/certbot \
  -d <domain> --email <email> --agree-tos --no-eff-email
```

证书位于 `/etc/letsencrypt/live/<domain>/`。已有证书体系时跳过本节，直接改下面的证书路径。

### 2.4 完整 TLS 配置

把 `/etc/nginx/conf.d/juhe-ai.conf` 替换为：

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      '';
}

upstream juhe_ai_gateway {
    server 127.0.0.1:3000;
    keepalive 32;
}

server {
    listen 80;
    server_name <domain>;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;                     # Nginx < 1.25.1 改为 listen 443 ssl http2;
    server_name <domain>;

    ssl_certificate     /etc/letsencrypt/live/<domain>/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/<domain>/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:SSL:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    server_tokens off;
    # HSTS（2026-11-06 安全基线）：与内置 Caddy 同款，不加 includeSubDomains。
    add_header Strict-Transport-Security "max-age=31536000" always;
    # 网关原始请求体硬上限：text 16MB、image/audio 64MB、/v1/files 512MB。
    # Nginx 默认 1m 会提前 413；520m 为 512MiB 文件 + multipart 边界/字段留余量。
    client_max_body_size 520m;
    client_body_timeout  300s;
    keepalive_timeout    75s;

    gzip on;
    gzip_min_length 1024;
    gzip_proxied any;
    gzip_types application/json application/javascript text/css text/plain application/xml;

    access_log /var/log/nginx/juhe-ai-access.log;
    error_log  /var/log/nginx/juhe-ai-error.log warn;

    # 健康检查：不打访问日志，保持短超时
    location = /__aisys__/health {
        proxy_pass http://juhe_ai_gateway;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        access_log off;
    }

    # OpenAI 兼容入口：SSE / WebSocket / 大请求体
    location /v1/ {
        proxy_pass http://juhe_ai_gateway;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Port $server_port;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        # SSE：禁止缓冲与缓存，超时覆盖网关整请求墙钟预算（默认 270s）
        proxy_buffering off;
        proxy_cache off;
        proxy_request_buffering off;
        gzip off;
        proxy_connect_timeout 10s;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;

        # 单上游，禁止把非幂等 POST 重放到其他实例
        proxy_next_upstream off;
    }

    # WebSocket 长连接单独放宽读超时
    location /v1/realtime {
        proxy_pass http://juhe_ai_gateway;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_buffering off;
        gzip off;
    }

    # 管理 SPA 与 /__aisys__/api
    location / {
        proxy_pass http://juhe_ai_gateway;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

```sh
nginx -t && systemctl reload nginx
```

证书续期后需要 reload：

```sh
mkdir -p /etc/letsencrypt/renewal-hooks/deploy
cat >/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh <<'EOF'
#!/bin/sh
systemctl reload nginx
EOF
chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
systemctl enable --now certbot.timer 2>/dev/null || true
```

没有 `certbot.timer` 的发行版，把 `certbot renew --quiet` 加入 root cron（每日一次即可）。

## 3. Nginx 容器形态

不发布 gateway `3000`，让 Nginx 加入应用 compose 网络，直接反代固定 IP：

```yaml
  nginx:
    image: nginx:1.27-alpine
    container_name: juhe-ai-nginx
    restart: unless-stopped
    depends_on:
      gateway:
        condition: service_healthy
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx/juhe-ai.conf:/etc/nginx/conf.d/default.conf:ro
      - /etc/letsencrypt:/etc/letsencrypt:ro
      - /var/www/certbot:/var/www/certbot:ro
    healthcheck:
      # HTTP 80 会 301 到 HTTPS；这里直接走 HTTPS 并绕过 127.0.0.1 的证书域名校验
      test: ["CMD-SHELL", "wget --no-check-certificate -q -O /dev/null --header 'Host: <domain>' https://127.0.0.1/__aisys__/health"]
      interval: 10s
      timeout: 5s
      retries: 12
```

- Nginx 配置里的 `upstream` 改成 `server 172.18.0.10:3000;`，其余 location 不变。
- 证书由宿主机 certbot 签发，容器只读挂载；续期 hook 改为 `docker exec juhe-ai-nginx nginx -s reload`。
- caddy 仍按 §2.1 加 profile 并停止。

## 4. 关键参数与原因

| 参数 | 值 | 原因 |
| --- | --- | --- |
| `proxy_http_version` | `1.1` | 客户端交接依赖 `http.Hijacker` 先提交 200 头再断开；h2c upstream 不支持 Hijack，会退化成 503 |
| `proxy_buffering` | `/v1/` 为 `off` | SSE 必须实时下发；缓冲会攒包 |
| `proxy_request_buffering` | `/v1/` 为 `off` | 避免 Nginx 先把 64MB 图片/音频、512MB 文件请求完整落盘再转发 |
| `proxy_read_timeout` | `600s`（realtime `3600s`） | 网关整请求预算 270s，流式连接可能持续数分钟；Nginx 默认 60s 会截断 |
| `client_max_body_size` | `520m` | 网关 text 上限 16MB、image/audio 64MB、`/v1/files` 512MB；multipart 边界有少量开销，故留余量；Nginx 默认 1m 会提前 413 |
| `X-Forwarded-For` | `$proxy_add_x_forwarded_for` | 在客户端自带 XFF 后追加真实来源；网关 `TRUST_PROXY=true` 取最后一跳，防止伪造 |
| `Upgrade` / `Connection` | map 后透传 | `/v1/realtime` WebSocket 握手 |
| `proxy_next_upstream` | `off` | 单上游；重放非幂等 POST 会重复计费/执行 |
| `gzip` | `/v1/` 为 `off` | 避免 SSE 被压缩缓冲；JSON 管理接口可压缩 |
| client-side HTTP/2 | `http2 on` | 客户端可协商 h2；upstream 仍必须 HTTP/1.1 |

### 4.1 真实客户端 IP

- Nginx 直接作为边缘时，`$remote_addr` 就是客户端 IP；`$proxy_add_x_forwarded_for` 会把它追加到 XFF 末尾。gateway `JUHE_AI_TRUST_PROXY=true` 取最后一跳，得到真实 IP。
- Nginx 前面还有 CDN/负载均衡时，`$remote_addr` 是 CDN 地址，必须启用 `ngx_http_realip_module`：

```nginx
set_real_ip_from <CDN回源网段>;
real_ip_header X-Forwarded-For;
real_ip_recursive on;
```

  启用后 `$remote_addr` 被还原为真实客户端，再按上面的 XFF 规则转发。
- 不要把 `proxy_set_header X-Forwarded-For $http_x_forwarded_for;` 原样透传：客户端可以自带伪造 XFF，gateway 取最后一跳会拿到伪造链。
- 验证：登录管理面或查 gateway 日志中的来源 IP，应为公网客户端地址，而不是 `127.0.0.1`、`172.18.0.x` 或 CDN 节点地址。

## 5. 验证

```sh
# 配置与进程
nginx -t
systemctl status nginx --no-pager

# 入口
curl -fsS -o /dev/null -w '%{http_code}\n' https://<domain>/__aisys__/health   # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://<domain>/v1/models            # 401（无 Key）

# 证书
echo | openssl s_client -servername <domain> -connect <domain>:443 2>/dev/null \
  | openssl x509 -noout -dates

# SSE：应逐块输出，而不是等结束后一次性返回
curl -N -sS https://<domain>/v1/chat/completions \
  -H 'Authorization: Bearer <key>' -H 'Content-Type: application/json' \
  -d '{"model":"<model>","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# 网关日志确认上游是 HTTP/1.1、无 502/504
docker logs --tail=100 juhe-ai-go-gateway 2>&1 | grep -E 'ERROR|502|504|panic' || true
```

面板侧：Grafana `juhe-ai 应用总览` 的 QPS/时延应正常；若 QPS 为 0 但入口有流量，先查 Nginx `proxy_pass` 与 gateway 端口。

## 6. 常见问题

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 502 Bad Gateway | gateway 未监听 `127.0.0.1:3000`（未加端口映射）或容器不 healthy | 检查 `docker compose ps`、`curl 127.0.0.1:3000/__aisys__/health`、Nginx error_log |
| 504 Gateway Timeout | `proxy_read_timeout` 太短，长流式请求被截断 | `/v1/` 设为 `600s`，realtime 设 `3600s` |
| SSE 一次性返回/卡住 | `proxy_buffering on` 或 gzip 缓冲 | `/v1/` 关闭 `proxy_buffering`、`proxy_cache`、`gzip` |
| 413 Request Entity Too Large | `client_max_body_size` 小于网关上限 | 设为 `520m`，不要小于网关（text 16MB / image+audio 64MB / files 512MB） |
| WebSocket 400/无法升级 | 未透传 `Upgrade`/`Connection` | 使用本文 map 与两个 header |
| 所有请求来源 IP 相同 | 未设置 `X-Forwarded-For`，或 `TRUST_PROXY` 配置错误 | 设置 XFF；gateway `JUHE_AI_TRUST_PROXY=true`；反向代理后不要信任客户端自带头 |
| 登录 401/CORS 失败 | `ALLOWED_ORIGINS` 不是实际 HTTPS Origin，或 cookie 非 secure | `.env` 改 `https://<domain>`、`JUHE_AI_COOKIE_SECURE=true` |
| `upstream prematurely closed connection` | 网关客户端交接（锁死/预算耗尽/临时隔离）先 200 再断开 | **预期行为**，不要按该日志告警；未提交响应的崩溃才由 Nginx 合成 502 |
| 证书续期后仍旧证书 | 未 reload | 配置 deploy hook 或 `systemctl reload nginx` |
| 80/443 被占用 | compose caddy 仍在运行 | 按 §2.1 停用 caddy；`ss -lntp` 确认 |

## 7. 回退到 Caddy

```sh
systemctl stop nginx
systemctl disable nginx
cd /opt/juhe-ai
# 删除 caddy 服务的 profiles 行（恢复默认启动）
docker compose up -d caddy
# 可选：移除 gateway 的 127.0.0.1:3000 映射
```

回退前确认 80/443 已释放；caddy 数据卷保留，证书可复用。

## 8. 安全边界

- Nginx 只反代 gateway `3000`；不要把 `3306/3305`、PostgreSQL、Redis 放进任何 `location`。
- gateway `3000` 只发布到 `127.0.0.1`，不要改成 `0.0.0.0`。
- `JUHE_AI_TRUST_PROXY=true` 仅在本机可信反代之后启用；不要把 gateway 直接暴露公网。
- HSTS 只在确认全站 HTTPS 后启用；启用后回退 HTTP 会有较长不可用期。
- 证书私钥权限 `600`，仅 root/certbot 可读；不要把 `/etc/letsencrypt` 复制进仓库或镜像。
- Nginx 访问日志含客户端 IP 与路径，按日志保留策略管理，不要写入请求正文或 Authorization。

## 9. 关联文档

- [部署指南](../部署指南.md)：整体部署流程、首次部署、升级、验证。
- [HTTPS / 反向代理](../https/README.md)：Caddy 默认形态与外部 TLS 通用要求。
- [排障速查](../troubleshooting/README.md)：容器、数据库、用量与入口问题。
- [监控接入](../monitoring/README.md)：入口与应用的指标、面板与告警。
