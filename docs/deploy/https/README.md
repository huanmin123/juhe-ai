# HTTPS / 反向代理

> 入口只有两种做法：**默认用 compose 内置 Caddy 终止 TLS**（推荐，部署指南已包含）；或**外部自己用 Caddy/Nginx 终止 TLS**，再把请求反代到 gateway。本文只写当前形态的要求与配置要点。

## 1. 默认：compose 内置 Caddy（推荐）

`docker/single-server/compose.yml` 自带 `caddy` 服务，`Caddyfile` 模板见 [部署指南 §5.2](../部署指南.md#52-caddyfile)。要点：

- DNS A 记录指向服务器公网 IP；安全组放行 80/443（及 443/udp，若启用 HTTP/3）。
- Caddy 自动 ACME 申请/续期证书；证书存 Docker 命名卷 `juhe-ai_caddy_data`，丢失会自动重签。
- 80 常驻 308 升级 HTTPS；根路径 301 到 `/__aisys__/`。
- `/v1/*` 必须 `flush_interval -1`（SSE 不缓冲）。
- 上游固定 `172.18.0.10:3000`（compose 固定 IP），不要改成容器名。
- 如果 Caddyfile 与同机其他站点共享，修改前备份、改后 `docker exec juhe-ai-caddy caddy validate --config /etc/caddy/Caddyfile` 再 `caddy reload`。

验证：

```sh
curl -fsS -o /dev/null -w '%{http_code}\n' https://<domain>/__aisys__/health
echo | openssl s_client -servername <domain> -connect <domain>:443 2>/dev/null | openssl x509 -noout -dates
```

## 2. 外部 TLS 终止（Caddy/Nginx 不在 compose 内）

适用于已有统一入口或不能跑 compose caddy 的环境。先确认代理能到达 gateway 的 3000 端口：

- **Docker 单机形态**：compose 默认只在内部网络暴露 gateway `3000`（宿主只发布健康端口 `127.0.0.1:3306`）。宿主机上的 Nginx/Caddy 需要先给 gateway 增加回环端口映射（在 `compose.yml` 的 gateway `ports` 加 `"127.0.0.1:3000:3000"` 后 `docker compose up -d gateway`），或者把代理放进同一 compose 网络并反代 `172.18.0.10:3000`。
- **systemd/裸进程形态**：gateway 默认监听 `127.0.0.1:3000`，下面示例可直接使用。

反代必须满足以下要求：

1. **上游协议为 HTTP/1.1**：客户端交接（锁死 hold、预算耗尽、临时隔离等非终态失败）依赖 `http.Hijacker` 先提交 200 传输头再断开；HTTP/2 upstream（h2c）不支持 Hijack，会把交接退化成 503。
2. **SSE 不缓冲**：`/v1/*` 必须实时转发。网关已发 `X-Accel-Buffering: no`（Nginx 会识别），Nginx 仍建议显式 `proxy_buffering off;`。
3. **超时足够长**：整请求墙钟预算默认 270 秒，流式连接可能持续数分钟；Nginx 默认 `proxy_read_timeout 60s` 会截断，需调到 ≥ 300 秒（如 600s）。
4. **真实客户端 IP**：反代设置 `X-Forwarded-For` / `X-Forwarded-Proto`；gateway 侧保持 `JUHE_AI_TRUST_PROXY=true`。不要在公网入口信任客户端自带的转发头。
5. **WebSocket**：`/v1/realtime` 走 Upgrade，需要转发 `Upgrade` / `Connection` 头。
6. **请求体上限**：按网关自己的 body 限制配置，不要比网关更小导致提前 413。
7. `JUHE_AI_ALLOWED_ORIGINS` 写实际 `https://<domain>`；`JUHE_AI_COOKIE_SECURE=true`。

### 2.1 Nginx 示例

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80;
    server_name <domain>;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    http2 on;
    server_name <domain>;

    ssl_certificate     /etc/letsencrypt/live/<domain>/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/<domain>/privkey.pem;

    client_max_body_size 32m;

    location /v1/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

注意：

- Nginx ≥ 1.25.1 用 `http2 on;`；更旧版本改用 `listen 443 ssl http2;`。
- `proxy_http_version 1.1` 不要改成 h2c；Nginx 每次客户端交接都会在 error 级日志记录 `upstream prematurely closed connection`，这是预期行为，不要按该日志告警。
- 多实例 upstream 时建议 `proxy_next_upstream off;`，避免网关未提交响应就崩溃时把非幂等 POST 重放到另一实例。

### 2.2 外部 Caddy 示例

```caddy
<domain> {
    encode zstd gzip
    redir / /__aisys__/ permanent
    reverse_proxy /v1/* 127.0.0.1:3000 {
        flush_interval -1
    }
    reverse_proxy 127.0.0.1:3000
}
```

Caddy 默认 HTTP/1.1 upstream，满足交接要求；不要配置 `transport http { versions h2c }`。

### 2.3 证书

- 外部 Caddy：自动 ACME，无需额外操作。
- Nginx：使用 certbot（`certbot --nginx -d <domain>`）或已有证书体系；续期后 reload nginx。
- 同一台机器的 80/443 只能有一个监听者：不要把 compose caddy 与外部 Nginx/Caddy 同时启用。

## 3. 真实客户端 IP

- Caddy 默认追加 `X-Forwarded-For`；Nginx 用 `$proxy_add_x_forwarded_for`。
- gateway 保持 `JUHE_AI_TRUST_PROXY=true`，只在可信反代之后启用；不要把 gateway 直接暴露公网并信任任意转发头。
- 客户端 IP 用于来源级限流、封禁与调度避让，配置错误会导致所有请求被视为同一 IP。

## 4. 与上游代理无关

本页只处理“客户端 → juhe-ai”的入口加密。gateway 请求 AI 上游时的出海代理是另一条链路，见 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
