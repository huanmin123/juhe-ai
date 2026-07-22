# CLIProxyAPI 本地 Sidecar 接入

> 状态：已落地为本地联调方案。

## 结论

当前项目不在仓库内复制 CLIProxyAPI 的 OAuth 内核、固定第三方 client identity 或 TLS 伪装逻辑。要复用 CLIProxyAPI 已支持的 Claude / Codex / Gemini / Kimi / xAI / Antigravity 等认证，当前推荐做法是把 CLIProxyAPI 作为本地 sidecar 运行，再把 juhe-ai 的普通上游账户指向这个 sidecar。

这样做的边界是：

- 认证、刷新和账号池调度仍由 CLIProxyAPI 负责。
- juhe-ai 负责本地分组、路由、配额、审计和二次网关能力。
- juhe-ai 看到的是 sidecar 暴露出来的协议入口，而不是 sidecar 内部每一条真实 OAuth 账户。

## 协议映射

| CLIProxyAPI 暴露入口 | juhe-ai 账户类型 | Base URL | 凭据写法 |
| --- | --- | --- | --- |
| OpenAI-compatible `/v1/*` | `openai` / `gpt` / `xai` / 其他 OpenAI v1 `api_key` 账户 | `http://127.0.0.1:<port>/v1` | `credentials.api_key = <CLIProxyAPI access key>` |
| Anthropic-compatible `/v1/messages*` | `anthropic` `api_key` 账户 | `http://127.0.0.1:<port>/v1` | `credentials.api_key = <CLIProxyAPI access key>` |
| Gemini native `/v1beta/*` | `gemini` `api_key` 账户 | `http://127.0.0.1:<port>/v1beta` | `credentials.api_key = <CLIProxyAPI access key>` |

CLIProxyAPI 自身入口统一校验 `Authorization: Bearer <key>`。因此 juhe-ai 现有 `api_key` 账户模型可以直接对接，不需要新增 sidecar 专用凭据结构。

## 本地联调步骤

1. 启动 CLIProxyAPI，并确认：
   - 监听端口，例如 `3456`
   - 访问 key
   - 需要的 OAuth 账户已经在 CLIProxyAPI 内完成授权

2. 在 juhe-ai 后端环境显式放行该 loopback origin：
   - 推荐：`JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST=http://127.0.0.1:3456`
   - 临时全开：`JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true`

3. 在 juhe-ai 创建普通 `api_key` 账户：
   - OpenAI / Codex / xAI / Kimi / Antigravity 等走 OpenAI v1 语义的入口：`http://127.0.0.1:3456/v1`
   - Claude / Claude Code 走 Anthropic Messages：`http://127.0.0.1:3456/v1`
   - Gemini native：`http://127.0.0.1:3456/v1beta`
   - API Key 填 CLIProxyAPI access key

4. 支持模型按 sidecar 实际暴露的模型填写，不要沿用官方默认目录硬猜。

## 当前适用场景

- 需要立即复用 CLIProxyAPI 已支持的认证能力。
- 本地或灰度环境希望继续使用 juhe-ai 的路由、审计、配额和管理面。
- 不打算在 juhe-ai 内直接维护 Claude / Codex / Gemini 等订阅 OAuth 生命周期。

## 当前限制

- juhe-ai 只能把 sidecar 看作一个上游端点，不能直接感知 sidecar 内部每个真实 OAuth 账户的独立运行态。
- CLIProxyAPI 的多账号轮询、冷却、刷新和供应商特定兼容逻辑都留在 sidecar 内部，不会自动下沉为 juhe-ai 的单账户可见明细。
- 如果需要“juhe-ai 自己管理每一条 Claude/Codex OAuth 账户”，那是另一条实现路线，不属于本方案。

## 关联文档

- [供应商订阅认证接入安全设计](供应商订阅认证接入安全设计.md)
- [Anthropic 账号接入](Anthropic账号接入.md)
- [OpenAI 账号接入](OpenAI账号接入.md)
- [Gemini 账号接入](Gemini账号接入.md)
- [CLIProxyAPI 本地 sidecar 接入计划](../plans/计划-0158-20260722T155636479Z-CLIProxyAPI本地Sidecar接入.md)
