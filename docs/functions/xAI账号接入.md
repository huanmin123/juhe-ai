# xAI / Grok 账号接入

## 范围与结论

本文记录 xAI 官方 API 的账号类型、协议档案、Grok 模型目录、usage / 计价口径和当前限制。xAI 与 OpenAI-compatible 只是接口形态复用关系，供应商、凭据、模型目录和价格必须保持独立。

当前结论：

- xAI 作为独立 `providerCode = xai` 接入，默认档案为 `profile_xai_openai_v1`。
- 当前只接入官方 xAI API Key；不接入 Grok CLI、X / Grok 订阅、第三方 OAuth 或浏览器会话凭据。
- 协议复用 OpenAI v1 Chat Completions 与 Responses：`chat_json`、`chat_sse`、`responses_json`、`responses_sse`。这不是把 xAI 账号并入 `openai` / `gpt` 账号池，也不是新增自研协议。
- 默认 Base URL 为 `https://api.x.ai/v1`；xAI driver 只承接 `POST /chat/completions`、`POST /responses`（带或不带 `/v1` 前缀），上游使用 `Authorization: Bearer <xAI API Key>`。

## 供应商与账户类型

```ts
type ProviderCode = 'xai'
type ProtocolCode = 'openai'
type ProtocolVersion = 'v1'
type ProviderProtocolProfileId = 'profile_xai_openai_v1'
type XaiAccountType = 'api_key'
type AccountSupportedEndpointMode = 'chat_json' | 'chat_sse' | 'responses_json' | 'responses_sse'
```

| 档案 | 默认 Base URL | 账户类型 | 默认模型 | 默认能力 |
| --- | --- | --- | --- | --- |
| `profile_xai_openai_v1` | `https://api.x.ai/v1` | xAI API Key (`api_key`) | `grok-4.3` | Chat JSON/SSE、Responses JSON/SSE |

账户保存规则：

- `credentials.api_key` 加密保存，列表和导入回显不得暴露完整值；账户测试与网关都从命中账户替换本地认证头。
- `credentials.supported_endpoint_modes` 省略时默认四种 JSON / SSE 能力；若上游账号或代理只支持 Chat，应显式限制为 `chat_json` / `chat_sse`。
- xAI API Key 账户默认写入 `pending_test`，由后台检查通过后才允许调度；人工测试只生成诊断、使用记录和审计，不改变账户健康事实。
- xAI 账户只能加入 `providerCode = xai` 的分组。API Key / 路由策略只负责入口和分组调度，不保存 xAI 专属客户端画像或跨协议转换规则。

## 协议边界

| 本地请求 | 上游请求 | 语义 |
| --- | --- | --- |
| `/v1/chat/completions`、`/chat/completions` | `<base_url>/chat/completions` | OpenAI Chat Completions 直连 |
| `/v1/responses`、`/responses` | `<base_url>/responses` | OpenAI Responses 直连 |

请求体默认 raw passthrough；只在账户模型映射、Codex 客户端兼容或 GPT 受控参数覆盖明确命中时做最小 JSON 改写。Chat / Responses 的 usage 复用 OpenAI 语义，`providerCode`、`providerProtocolProfileId` 和真实上游模型仍记录为 xAI 事实。

当前禁止：

- xAI 账号承接 Anthropic Messages、Gemini native、Gemini Interactions 或其他非 OpenAI v1 原生请求。
- 通过模型名把 OpenAI Chat / Responses 自动转换为另一协议；需要跨协议时必须使用混合供应商账户表达真实上游和映射。
- 把 xAI API Key 作为 GPT OAuth、Claude 订阅 OAuth 或 Gemini Google OAuth 凭据处理。

## 模型目录与价格快照

本地内置目录按 `2026-07-18` xAI 官方模型页和价格页快照维护，当前包含：

- 文本模型：`grok-4.5`、`grok-4.3`、`grok-4.20-0309-reasoning`、`grok-4.20-0309-non-reasoning`、`grok-build-0.1`、`grok-4.20-multi-agent-0309`；官方模型页均记录 `Text, Image -> Text`，本地目录按模型保留图片输入能力。
- 图片模型计价项：`grok-imagine-image` 每张 0.02 USD、`grok-imagine-image-quality` 每张 0.05 USD。当前 xAI provider driver 只开放 Chat / Responses 文本主链；账户模型选项和最终保存会按 `profile_xai_openai_v1` 的 Chat / Responses 协议交集过滤，图片专用模型不能进入 `supportedModels` 或 `healthCheckModel`。实际使用图片模型前需补专用 Images driver / 回归。
- 当前没有可靠证据支持为这些模型暴露可选 `reasoning_effort` 枚举或默认档位，因此目录保持空数组，My Chat 不显示思考档位控件；模型名中的 reasoning / non-reasoning 事实不等于可任意猜测请求参数。

文本价格以 USD / 1M token 记录：

| 模型 | Input | Cached input | Output | 上下文 |
| --- | ---: | ---: | ---: | ---: |
| `grok-4.5` | 2.00 | 0.50 | 6.00 | 500K |
| `grok-4.3`、`grok-4.20-*` | 1.25 | 0.20 | 2.50 | 1M |
| `grok-build-0.1` | 1.00 | 0.20 | 2.00 | 256K |

价格驱动同时保存 `priority` 两倍价格、输入达到 200K 阈值时对整次请求应用的长上下文两倍倍率，以及 prompt caching。阈值包含性是逐模型价格元数据：xAI 使用 `>= 200K`，Gemini 仍使用 `> 200K`。历史使用记录在写入时固化成本快照；后续价格变化必须新增快照，不按当前目录重算历史账单。

xAI 官方价格页列出的 Web Search、X Search 和 Code Execution 均按 5 USD / 1K calls 收费。当前项目价格 schema 没有独立的工具调用次数 / 工具名价格维度，因此收费服务端工具不写入公开 `supportedTools`，My Chat 不自动启用，也不能声称已纳入精确美元成本；后续若要对账，应先新增价格维度和 usage 字段，再逐工具接入，不能用 token 或固定倍率临时估算。

## usage 与验证

- Chat Completions 使用 `prompt_tokens`、`completion_tokens`、`prompt_tokens_details.cached_tokens`；Responses 使用 `input_tokens`、`output_tokens` 及缓存细分，统一写入 OpenAI usage semantic。
- `pnpm --filter juhe-ai-backend test:xai-provider` 覆盖 seed、API Key 凭据、Chat / Responses URL、Bearer header 和跨协议拒绝。
- `pnpm --filter juhe-ai-frontend test:xai-account-capability` 覆盖账户创建类型、默认 Base URL、endpoint modes 和中文文案。
- `pnpm --filter juhe-ai-backend test:model-catalog` 覆盖 xAI 模型目录、价格来源和默认模型；真实 xAI E2E 需要用户提供临时 API Key，不能把密钥写入文档、日志或提交。

## 官方资料

- 模型列表：[xAI Models](https://docs.x.ai/developers/models)
- 价格：[xAI Pricing](https://docs.x.ai/developers/pricing)
- API 文档：[xAI API Documentation](https://docs.x.ai/)
