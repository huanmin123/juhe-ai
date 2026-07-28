# xAI / Grok 账号接入

## 范围与结论

本文记录 xAI 官方 API Key、Grok OAuth、Grok Web SSO 转 OAuth、协议档案、Grok 模型目录、usage / 计价口径和当前限制。xAI 与 OpenAI-compatible 只是接口形态复用关系，供应商、凭据、模型目录和价格必须保持独立。

当前结论：

- xAI 作为独立 `providerCode = xai` 接入，默认档案为 `profile_xai_openai_v1`。
- 当前同时接入官方 xAI API Key 和 Grok OAuth。API Key 复用 OpenAI v1 Chat Completions / Responses；OAuth 使用 Grok CLI OAuth 上游，只承接 Responses JSON / SSE。
- Grok OAuth 支持浏览器 PKCE 回调、Refresh Token、直接 Access Token 和 Grok Web SSO Cookie 转 device flow；SSO Cookie 只用于换取 OAuth token，不作为网关长期认证凭据。
- API Key 默认 Base URL 为 `https://api.x.ai/v1`，OAuth 默认 Base URL 为 `https://cli-chat-proxy.grok.com/v1`；两类账户都使用 `Authorization: Bearer`，但能力和请求准备语义按账户类型分开。

## 供应商与账户类型

```ts
type ProviderCode = 'xai'
type ProtocolCode = 'openai'
type ProtocolVersion = 'v1'
type ProviderProtocolProfileId = 'profile_xai_openai_v1'
type XaiAccountType = 'api_key' | 'oauth'
type AccountSupportedEndpointMode = 'chat_json' | 'chat_sse' | 'responses_json' | 'responses_sse'
```

| 档案 | 默认 Base URL | 账户类型 | 默认模型 | 默认能力 |
| --- | --- | --- | --- | --- |
| `profile_xai_openai_v1` | `https://api.x.ai/v1` | xAI API Key (`api_key`) | `grok-4.5` | Chat JSON/SSE、Responses JSON/SSE |
| `profile_xai_openai_v1` | `https://cli-chat-proxy.grok.com/v1` | Grok OAuth (`oauth`) | `grok-4.5` | Responses JSON/SSE |

账户保存规则：

- `credentials.api_key` 加密保存，列表和导入回显不得暴露完整值；账户测试与网关都从命中账户替换本地认证头。
- OAuth 凭据保存 `access_token`、`refresh_token`、`id_token`、`token_type`、`expires_at`、`client_id`、`scope` 及可取得的 `email`、`sub`、`team_id`、`subscription_tier`、`entitlement_status`；刷新响应未轮换 Refresh Token 时保留旧值。
- API Key 的 `credentials.supported_endpoint_modes` 省略时默认四种 JSON / SSE 能力；OAuth 固定为 `responses_json`、`responses_sse`，不能通过表单扩展到 Chat。
- xAI API Key 账户默认写入 `pending_test`，由后台检查通过后才允许调度；人工测试只生成诊断、使用记录和审计，不改变账户健康事实。
- Grok OAuth 创建也写入 `pending_test`，默认并发为 1；请求前按到期时间单飞刷新并持久化轮换后的 token，自定义 `base_url` 在刷新和重新授权后继续保留。
- xAI 账户只能加入 `providerCode = xai` 的分组。API Key / 路由策略只负责入口和分组调度，不保存 xAI 专属客户端画像或跨协议转换规则。

## Grok OAuth 创建与维护

- 浏览器授权使用 xAI OAuth authorization code + PKCE S256，会话保存 `state`、`nonce`、verifier、client、scope 和回调地址，30 分钟内一次性消费。
- 前端可以粘贴完整 callback URL、裸 query、`code#state` 或 Refresh Token 创建账户，也可直接录入当前可用的 Access Token；已有 OAuth 账户支持手动刷新和两种重新授权入口。
- 管理端使用 `/grok-oauth/*`，个人端使用 `/my-grok-oauth/*`；创建、刷新和重新授权都复用账户绑定代理。
- OAuth 上游只在目标主机精确为 `cli-chat-proxy.grok.com` 时补充 `X-XAI-Token-Auth: xai-grok-cli`、`x-grok-client-version: 0.2.93` 和 `xai-grok-workspace/0.2.93` User-Agent；`api.x.ai` 与自定义主机不得携带这些 CLI 身份头。它不继承 GPT/Codex OAuth 的 compact、attestation、Chat 兼容或账户请求覆盖规则。

### Grok Web SSO 转 OAuth

- `POST /grok-oauth/sso-to-oauth` 与 `POST /my-grok-oauth/sso-to-oauth` 接受 `ssoTokens: string[]` 和可选单值 `ssoToken`。每项可为裸 SSO key、`Cookie: ...`、`sso=...` 或 `sso-rw=...`，逗号和换行输入会归一化、去空并去重。
- 后端先访问 `accounts.x.ai` 校验 SSO，再调用 xAI device code、verify、approve 和 token polling；device scope 在普通 OAuth scope 上增加 `conversations:read conversations:write`。只信任 HTTPS `x.ai` 及其子域，响应上限 2 MiB，重定向最多 8 次。
- 批量导入最多 3 路并发并保持输入顺序，返回 `created` 与 `failed` 两组逐项结果。代理作用于完整 device flow 和创建后的账户；单项失败不回滚其他成功项。
- SSO key 不写入账户凭据、响应或操作日志。成功后只保存标准 Grok OAuth token；如果 token 没有 Refresh Token，账户到期时间会收紧到 token 到期时间或用户配置的更早时间。

## 协议边界

| 本地请求 | 上游请求 | 语义 |
| --- | --- | --- |
| API Key：`/v1/chat/completions`、`/chat/completions` | `<base_url>/chat/completions` | OpenAI Chat Completions 直连 |
| API Key：`/v1/responses`、`/responses` | `<base_url>/responses` | OpenAI Responses 直连 |
| OAuth：`/v1/responses`、`/responses` | `<base_url>/responses` | Grok CLI Responses 直连 |

请求体默认 raw passthrough；只在账户模型映射、Codex 客户端兼容或 GPT 受控参数覆盖明确命中时做最小 JSON 改写。Chat / Responses 的 usage 复用 OpenAI 语义，`providerCode`、`providerProtocolProfileId` 和真实上游模型仍记录为 xAI 事实。

当前禁止：

- xAI 账号承接 Anthropic Messages、Gemini native、Gemini Interactions 或其他非 OpenAI v1 原生请求。
- 通过模型名把 OpenAI Chat / Responses 自动转换为另一协议；需要跨协议时必须使用混合供应商账户表达真实上游和映射。
- 让 Grok OAuth 承接 `/chat/completions`，或把它当成 GPT/Codex OAuth 套用 compact、attestation 和客户端兼容逻辑。
- 把 xAI API Key / Grok OAuth 作为 GPT OAuth、Claude 订阅 OAuth 或 Gemini Google OAuth 凭据处理。

## 模型目录与价格快照

本地内置目录按 `2026-07-18` xAI 官方模型页和价格页快照维护，当前包含：

- 文本模型：`grok-4.5`、`grok-4.20-0309-reasoning`、`grok-4.20-0309-non-reasoning`、`grok-build-0.1`、`grok-4.20-multi-agent-0309`；官方模型页均记录 `Text, Image -> Text`，本地目录按模型保留图片输入能力。`grok-4.3` 因无法交叉确认精确首发日期，已从内置目录移除。
- 图片模型计价项：`grok-imagine-image` 每张 0.02 USD、`grok-imagine-image-quality` 每张 0.05 USD。当前 xAI provider driver 只开放 Chat / Responses 文本主链；账户模型选项和最终保存会按 `profile_xai_openai_v1` 的 Chat / Responses 协议交集过滤，图片专用模型不能进入 `supportedModels` 或 `healthCheckModel`。实际使用图片模型前需补专用 Images driver / 回归。
- 当前没有可靠证据支持为这些模型暴露可选 `reasoning_effort` 枚举或默认档位，因此目录保持空数组，My Chat 不显示思考档位控件；模型名中的 reasoning / non-reasoning 事实不等于可任意猜测请求参数。

文本价格以 USD / 1M token 记录：

| 模型 | Input | Cached input | Output | 上下文 |
| --- | ---: | ---: | ---: | ---: |
| `grok-4.5` | 2.00 | 0.50 | 6.00 | 500K |
| `grok-4.20-*` | 1.25 | 0.20 | 2.50 | 1M |
| `grok-build-0.1` | 1.00 | 0.20 | 2.00 | 256K |

价格驱动同时保存 `priority` 两倍价格、输入达到 200K 阈值时对整次请求应用的长上下文两倍倍率，以及 prompt caching。阈值包含性是逐模型价格元数据：xAI 使用 `>= 200K`，Gemini 仍使用 `> 200K`。历史使用记录在写入时固化成本快照；后续价格变化必须新增快照，不按当前目录重算历史账单。

xAI 官方价格页列出的 Web Search、X Search 和 Code Execution 均按 5 USD / 1K calls 收费。当前项目价格 schema 没有独立的工具调用次数 / 工具名价格维度，因此收费服务端工具不写入公开 `supportedTools`，My Chat 不自动启用，也不能声称已纳入精确美元成本；后续若要对账，应先新增价格维度和 usage 字段，再逐工具接入，不能用 token 或固定倍率临时估算。

## usage 与验证

- Chat Completions 使用 `prompt_tokens`、`completion_tokens`、`prompt_tokens_details.cached_tokens`；Responses 使用 `input_tokens`、`output_tokens` 及缓存细分，统一写入 OpenAI usage semantic。
- `pnpm --filter juhe-ai-backend test:xai-provider` 覆盖 seed、API Key / OAuth 凭据、Chat / Responses URL、Grok OAuth Responses-only、Bearer / CLI header 和跨协议拒绝。
- `pnpm --filter juhe-ai-backend test:grok-oauth-protocol-contract` 覆盖 OAuth 端点、PKCE、凭据构建、刷新保留与管理 / 个人路由挂载。
- `pnpm --filter juhe-ai-backend test:grok-sso-device-flow` 覆盖 SSO 归一化、device flow、轮询、批量 3 并发和逐项结果契约。
- `pnpm --filter juhe-ai-backend test:provider-oauth-mock-upstream-e2e` 离线覆盖 Grok authorize、nonce/PKCE、回调换票、刷新轮换和 System API 建号落库；它不证明真实 xAI 订阅资格或线上风控状态。
- `pnpm --filter juhe-ai-frontend test:xai-account-capability` 覆盖 API Key / OAuth 创建类型、默认 Base URL、Responses-only、SSO Cookie 输入和中文文案。
- `pnpm --filter juhe-ai-backend test:model-catalog` 覆盖 xAI 模型目录、价格来源和默认模型；真实 xAI E2E 需要用户提供临时 API Key，不能把密钥写入文档、日志或提交。

## 官方资料

- 模型列表：[xAI Models](https://docs.x.ai/developers/models)
- 价格：[xAI Pricing](https://docs.x.ai/developers/pricing)
- API 文档：[xAI API Documentation](https://docs.x.ai/)
