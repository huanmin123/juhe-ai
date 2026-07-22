# HTTPS 部署示例

## 示例目标

- 域名：`ai.example.com`。
- juhe-ai 发布包运行在同一台 Linux 服务器。
- juhe-ai 监听：`127.0.0.1:3000`。
- Caddy 监听公网 `80/443`，自动申请和续期免费证书。
- 后台和客户端都通过 `https://ai.example.com` 访问。

## 步骤

确认 DNS 已指向服务器：

```bash
dig ai.example.com +short
```

修改 `backend/.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_COOKIE_SAME_SITE=lax
JUHE_AI_TRUST_PROXY=true
```

启动 juhe-ai 后先确认本机端口可用：

```bash
curl -i http://127.0.0.1:3000/__aisys__/health
```

写入 `/etc/caddy/Caddyfile`：

```caddyfile
ai.example.com {
    encode zstd gzip

    reverse_proxy 127.0.0.1:3000 {
        flush_interval -1
    }
}
```

启动 Caddy：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl enable --now caddy
sudo systemctl reload caddy
```

验证 HTTPS：

```bash
curl -I https://ai.example.com/__aisys__/
curl -i https://ai.example.com/__aisys__/health
curl -i https://ai.example.com/v1/models -H 'Authorization: Bearer 本地APIKey'
```

客户端配置：

```text
Base URL: https://ai.example.com/v1
API Key : juhe-ai 后台生成的本地 sk-... 密钥
```

如果 `/v1` 流式响应中途断开，先看 Caddy 日志和 juhe-ai 运行日志，再确认没有在 Caddy 前面叠加会缓冲 SSE 的 CDN、WAF 或额外反向代理。
