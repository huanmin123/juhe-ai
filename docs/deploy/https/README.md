# HTTPS 证书部署目录

> 面向公网域名、反向代理和免费证书自动续期部署。
> 这里说明 juhe-ai 推荐的 HTTPS 入口方案、Caddy 自动证书、Docker / 裸机差异和 Nginx + Certbot 备选方案。

## 文档索引

- [Caddy 自动 HTTPS 部署指南](Caddy自动HTTPS部署指南.md)：推荐方案，使用 Caddy 为 juhe-ai 域名自动申请和续期免费证书。
- [HTTPS 部署示例](HTTPS部署示例.md)：一次从域名解析、Caddyfile、环境变量到验证的完整示例。

## 适用边界

- 公网访问后台或 `/v1` 网关时，默认优先使用 Caddy 作为 HTTPS 反向代理。
- 已经有 Nginx 运维体系时，可以使用 Nginx + Certbot；不要同时让 Caddy 和 Nginx 监听同一台机器的 `80/443`。
- HTTPS 入口只负责客户端到 juhe-ai 的访问加密；上游 API 无法直连时，仍按 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) 配置账号级代理。
- 生产环境启用 HTTPS 后，`JUHE_AI_ALLOWED_ORIGINS` 必须写实际 `https://` 域名，`JUHE_AI_COOKIE_SECURE=true`。
