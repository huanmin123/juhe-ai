# HTTPS / 反向代理

> 入口只有两种做法：**默认用 compose 内置 Caddy 终止 TLS**（推荐，部署指南已包含）；或**外部自己用 Caddy/Nginx 终止 TLS**，再把请求反代到 gateway。本文只写当前形态的要求与配置要点。

## 1. 默认：compose 内置 Caddy（推荐）

`docker/single-server/compose.yml` 自带 `caddy` 服务，`Caddyfile` 模板见[配置参考 §6](../config/README.md)。要点：

- DNS A 记录指向服务器公网 IP；安全组放行 80/443（及 443/udp，若启用 HTTP/3）。
- Caddy 自动 ACME 申请/续期证书；证书存 Docker 命名卷 `juhe-ai_caddy_data`，丢失会自动重签。
- 80 常驻 308 升级 HTTPS；根路径 301 到 `/__aisys__/`。
- 站点返回 `Strict-Transport-Security: max-age=31536000`（2026-11-06 安全基线，不加 `includeSubDomains`）；外部 TLS 终止时由该入口补等价 header。
- `/v1/*` 必须 `flush_interval -1`（SSE 不缓冲）。
- 上游固定 `172.18.0.10:3000`（compose 固定 IP），不要改成容器名。
- 如果 Caddyfile 与同机其他站点共享，修改前备份、改后 `docker exec juhe-ai-caddy caddy validate --config /etc/caddy/Caddyfile` 再 `caddy reload`。

验证：

```sh
curl -fsS -o /dev/null -w '%{http_code}\n' https://<domain>/__aisys__/health
curl -sI https://<domain>/__aisys__/health | grep -i strict-transport-security
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

完整安装、TLS 引导、SSE/WebSocket、超时、真实客户端 IP 与排障见 [Nginx 部署](../nginx/README.md)。最小要求与本文 §2 相同：

- 上游固定 `127.0.0.1:3000`（宿主 Nginx）或 `172.18.0.10:3000`（容器 Nginx）；
- `proxy_http_version 1.1`，不要用 h2c；
- `/v1/` 关闭 `proxy_buffering`/`gzip`，`proxy_read_timeout` ≥ 600s；
- 透传 `Upgrade`/`Connection` 与 `X-Forwarded-For`；
- `client_max_body_size` 不小于网关上限（图片/音频 64MiB、文本 16MiB；建议 `70m`）。原 `/v1/files` 512MiB 车道随 2026-11-09 openai-compatible files 本地运行时退役删除。

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
