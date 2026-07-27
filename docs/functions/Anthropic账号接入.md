# Anthropic 账号接入

> 2026-06-27 路由分层更新：本文旧段落里提到的 API Key 显式桥接配置只作为历史背景，不得作为新增实现、测试断言或页面配置依据。当前目标是 API Key 只绑定策略路由，策略路由负责分组和模型调度，OpenAI / Anthropic 跨协议转换落到混合供应商账户。

## 范围

本文记录 Anthropic / Claude 供应商的当前接入方案、协议档案、账户创建类型、网关透传边界、模型目录、用量统计和实现注意事项。本文按当前目标一次性完整落地描述，运行事实以当前代码、schema 和本文契约为准。

本次调研结论同时参考了 Anthropic 官方文档、官方 SDK / GitHub 生态、Claude Code OAuth 认证说明、DeepSeek / GLM / Kimi 等 Anthropic-compatible 入口说明，以及本地参考项目 `sub2api`、`new-api` 的已落地拆分方式。参考项目只用于抽象分层经验，不复制其无边界深度协议互转、全供应商大网关范围或尚未真实验证的 OAuth / Claude Code token 账号链路。

Anthropic 与 GPT 账户、测试、网关、统计和前端的全链路差异见 [Anthropic 与 GPT 全链路能力对比](Anthropic与GPT全链路能力对比.md)。

结论：

- 直连 Anthropic 必须作为独立 `anthropic/v1` 原生协议档案接入，核心端点是 `POST /v1/messages`。
- Anthropic 官方确实提供 OpenAI SDK compatibility，但官方定位主要是测试和对比，不适合作为本项目直连接入的主协议。
- “Anthropic-compatible” 和 “Claude 官方模型”不是一回事。DeepSeek、GLM Coding、Kimi 等国内入口即使暴露 Anthropic Messages 形态，也应归到各自供应商，不应显示为官方 Anthropic。
- 当前 Anthropic 原生目标完整落地 Anthropic API Key 直连、官方 OAuth Access Token 导入，以及 Messages / Models / Count Tokens 的轻量闭环，并单独支持 Claude Code 作为下游客户端画像。Workload Identity Federation（WIF）、Claude.ai / Claude Code 订阅 OAuth 浏览器换码、Bedrock、Vertex 暂不开放。OpenAI Chat / Responses 到 Anthropic Messages 不属于原生 raw passthrough，而是独立的显式桥接能力，设计见 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md)。

## 当前落地状态（2026-07-18）

已落地并通过 mock 回归的能力：

- Anthropic 官方 API Key 账户走 `profile_anthropic_anthropic_v1`，本地网关支持 `POST /v1/messages`、`POST /v1/messages/count_tokens` 和本地 `GET /v1/models`。
- 截至当前实现，Anthropic 官方 OAuth 账户也走 `profile_anthropic_anthropic_v1`；创建方式同时支持站内官方 OAuth / PKCE 浏览器换码、Refresh Token 创建和直接录入 `access_token`。上游认证固定为 `Authorization: Bearer <access_token>`；若保存了 `refresh_token`，支持手动刷新与重新授权。
- 本地认证支持 `Authorization: Bearer <本地 API Key>` 和 Anthropic SDK 常用的 `x-api-key: <本地 API Key>`；上游认证始终替换为命中账号的 `x-api-key`。
- `anthropic-version` 默认 `2023-06-01`；客户端显式传入时透传客户端值。普通 Anthropic 请求的 `anthropic-beta` 只透传客户端显式 header，不作为账号配置保存；`claude_code` 客户端画像下会按真实 Claude Code 请求特征补齐缺失的 `anthropic-beta`、`User-Agent`、`x-claude-code-session-id` 和 `?beta=true`。
- Anthropic 账户模型映射已支持顶层 `model` 改写，`messages`、`system`、`tools`、`thinking`、`stream`、`metadata` 等其他原生字段保持透传。
- Anthropic 多 API Key 已启用 Key 级故障隔离，单个 Key 失败后只摘除当前 Key；全部 Key 不可用时才跳过账户。
- Anthropic Messages JSON / SSE 已接入统一响应语义帧，覆盖 `content[].text`、`thinking`、`tool_use` 摘要、`stop_reason`、usage、JSON error object 和 SSE `event: error`。
- 响应检查策略支持 `protocolCode = anthropic`，策略按协议层 / 供应商层隔离；默认 Anthropic error object 规则只形成响应检查输入，不直接写账号死亡状态。
- Anthropic native 本地错误已按 Anthropic error shape 返回，认证、端点能力、模型路由、调度失败等本地错误不再暴露 OpenAI error payload。
- Anthropic WIF 暂不注册账户类型、凭据写入或运行时 token exchange。仅保存一个管理员手工填写的 IdP JWT 无法形成可信自动轮换 source，JWT 到期后会永久失败，因此不能伪装为可用生产能力。
- Claude Code 只作为下游客户端画像，支持显式 `x-juhe-client-profile: claude_code` 和官方 CLI 多信号识别；本地画像 header 不透传上游。命中该画像时，原生 Anthropic 请求准备层只补齐 Claude Code 请求头和 `beta=true` 查询参数，不伪造 Claude Code system prompt、工具 schema、thinking body 或 OAuth 字段。显式 OpenAI -> Anthropic Messages bridge 命中 `claude_code` 时，会额外补最小 Claude Code-compatible body envelope，用于兼容会校验 Claude Code body 形态的第三方上游。
- 前端账户表单当前支持 Anthropic API Key 和 Anthropic OAuth Access Token 两种接入类型，均可配置 Base URL 和 Messages 端点能力；`Anthropic-Version` / `Anthropic-Beta` 不作为账号配置项。
- Anthropic 模型价格目录已按 `providerCode = anthropic` 接入，当前成本估算覆盖 input、output、cache read、cache write 和 1h cache write，并补充 Claude Code 模型别名与常见 Antigravity 兼容别名。
- Anthropic 使用记录明细已保存 `usage_semantic=anthropic`、cache write、1h cache 和 thinking tokens；使用记录页和成本明细可展示这些字段。

仍保持为后续独立需求的边界：

- 不支持 Claude.ai / Claude Code 订阅 OAuth 浏览器换码、Setup Token、Claude 订阅账号中转或 WIF；WIF 是 Anthropic 官方组织级身份联邦，不属于订阅 OAuth，但仍需要独立的可信 assertion source 才能开放。当前支持官方 OAuth / PKCE 浏览器换码、Refresh Token 创建，以及直接导入已有 Access Token。
- 不在 Anthropic 原生 `/v1/messages` 透传路径里隐式做 OpenAI Chat / Responses 转换；OpenAI-compatible 下游请求 Claude 模型必须走显式 OpenAI -> Anthropic Messages bridge，且覆盖范围、失败策略和测试矩阵以 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md) 为准。
- stats staged / window / summary 表、授权消耗报表和统计大盘暂未按 cache write、1h cache、thinking token 扩维；后续必须继续由 worker 增量聚合，不在 API 路由临时扫描明细。
- Anthropic-compatible 国产入口必须按各自供应商建档，不并入官方 `anthropic` 账号池、价格目录和默认分组。

新增进行中需求：

- `2026-06-24` 已建立 PLAN-0058，目标是让 OpenAI Chat JSON、Chat SSE、Responses JSON、Responses SSE 四类下游入口通过显式桥接命中 Anthropic Messages 上游。
- 该桥接不改变 Anthropic 账号真实 endpoint modes；账号仍声明 `messages_json`、`messages_sse`、`message_token_counting`。
- 该桥接要求模型能通过目标模型或账号模型映射明确定位到 Anthropic Messages 档案，不允许仅凭客户端画像或模型名猜测全局账号池。

真实联网验证补充：

- 已新增 `pnpm --filter juhe-ai-backend test:anthropic-real-gateway-e2e`，用于用真实 Anthropic 或 Anthropic-compatible Base URL 创建临时本地网关、临时账号、临时分组和临时本地 API Key 后跑完整 E2E。
- 使用用户提供的 `https://vsllm.com` 执行时，`/v1/models` 成功返回 69 个模型；`claude-opus-4-8` 直连 JSON 能返回 thinking 与文本；`claude-fake-5` 已完成本项目临时网关轻量 E2E，覆盖本地账号落库、runtime 选号、本地 `/v1/models`、Messages JSON、Messages SSE、Claude Code 画像请求和工具调用。
- `https://vsllm.com` 的 `/v1/messages/count_tokens` 当前返回 404，因此第三方 Anthropic-compatible 账号要允许按账号 `supported_endpoint_modes` 关闭 `message_token_counting`；不能因为官方 Anthropic 支持 Count Tokens，就假设所有兼容网关都支持。当前网关会把 Anthropic `message_token_counting` 的 404 / 405 视为端点能力失败，不写 API Key 故障、账号本地屏蔽、预检失败或 IP 避让。
- 真实联调期间还观察到部分模型返回 403 余额 / 额度错误、429 限流、503 模型认证不可用、SSE 对非 Anthropic 模型返回 500、以及上游对缺少 `max_tokens` 的请求过度宽松。这些都只能进入诊断、审计和账户错误策略输入，不能在协议适配层写死状态判断。
- 该类真实错误只作为真实联调诊断、审计和账户错误策略输入，不在协议适配层直接映射为账号死亡、限流或临时不可调用。
- 真实 SSE 联调暴露了 Anthropic `message_stop` 后第三方兼容网关可能继续保持连接的情况；当前流式管道已在 Anthropic 协议终止事件写出后主动结束下游响应，不要求继续等待上游 EOF。

## 协议与供应商定义

新增官方直连供应商：

```ts
type ProviderCode = 'anthropic'
type ProtocolCode = 'anthropic'
type ProtocolVersion = 'v1'
type ProviderProtocolProfileId = 'profile_anthropic_anthropic_v1'
type EndpointFamily = 'messages' | 'models' | 'message_token_counting'
type AccountSupportedEndpointMode = 'messages_json' | 'messages_sse' | 'message_token_counting'
type AnthropicAccountType = 'api_key' | 'oauth'
```

建议显示名称为 `Anthropic Claude`。`anthropic` 是官方直连供应商，表示账号池、分组、模型目录、价格目录和响应策略都归属于 Anthropic 官方 Claude API。它不能复用 `openai`、`gpt`、`glm`、`deepseek` 或第三方代理供应商的账号池。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_anthropic_anthropic_v1` | `anthropic` | `anthropic/v1` | `https://api.anthropic.com` | API Key / OAuth Access Token | `messages_json`、`messages_sse`、`message_token_counting` |

端点族边界：

- `messages`：`POST /v1/messages`，当前核心转发端点。
- `models`：`GET /v1/models`，由本地模型目录按 Anthropic 形态返回，不默认请求上游。
- `message_token_counting`：`POST /v1/messages/count_tokens`，作为当前目标内的原生 Count Tokens 端点透传能力。
- `message_batches`、`files`、`skills`、`managed_agents`、`sessions` 不纳入当前目标。需要时必须作为新的明确需求单独设计，避免把轻量中转扩展成 Anthropic 全平台管理面。

## 官方直连与兼容入口区分

系统必须明确区分三类资源：

| 类型 | 示例 | `providerCode` 建议 | `protocolCode` | 是否官方 Claude |
| --- | --- | --- | --- | --- |
| 官方直连 Anthropic | `https://api.anthropic.com` | `anthropic` | `anthropic` | 是 |
| 云平台托管 Claude | Bedrock、Vertex AI、Microsoft Foundry | 独立 `bedrock` / `vertex` / `foundry` | 云厂商专属或 `anthropic` 变体 | 是，但不是 Anthropic 账号体系 |
| Anthropic-compatible 第三方 / 国产入口 | DeepSeek `/anthropic`、GLM Coding `/api/anthropic`、Kimi `/anthropic` | 各自供应商，例如 `deepseek`、`glm`、`kimi` | `anthropic` | 不一定，通常不是 |

直连 Anthropic 的识别条件必须同时满足：

- `providerCode = anthropic`
- `providerProtocolProfileId = profile_anthropic_anthropic_v1`
- `base_url` 默认或明确指向 Anthropic 官方 API 根地址
- 凭据来自 Anthropic Console API Key
- `modelSource = anthropic_first_party`

国产或第三方兼容入口即使支持 `POST /v1/messages`，也只能描述为“Anthropic Messages 兼容协议”。UI、文档、模型目录和授权说明不得把这类入口称为“Claude 官方直连”。例如：

- DeepSeek Anthropic API 是 DeepSeek 模型的 Anthropic-compatible surface，已有独立文档见 [DeepSeek 账号接入](DeepSeek账号接入.md)。
- 智谱 GLM Coding Plan 的 Anthropic Messages 端点已使用独立 `profile_glm_coding_anthropic_v1`，不能混进官方 `anthropic` 供应商。
- Kimi / Moonshot、UCloud、OpenRouter、AiHubMix 等入口应按真实传输方和模型来源建立独立供应商或代理供应商档案。

## 账户创建类型

前端创建流程仍按“供应商 -> 账户类型 -> 凭据与调度配置”展开。

选择 `Anthropic Claude` 后当前展示 API Key 与 OAuth 两种接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认检查模型 |
| --- | --- | --- | --- | --- |
| Anthropic API Key | `api_key` | `profile_anthropic_anthropic_v1` | `api_key`、`base_url`、`supported_endpoint_modes` | `claude-opus-4-8` |
| Anthropic OAuth | `oauth` | `profile_anthropic_anthropic_v1` | 站内授权回调 URL、`refresh_token` 或 `access_token`，以及 `base_url`、`supported_endpoint_modes` | `claude-opus-4-8` |

保存规则：

- API Key 账户加密保存 `api_key`；列表不展示，编辑弹窗可查看和修改。
- OAuth 账户加密保存 `access_token`；列表不展示，编辑弹窗可查看和修改。
- API Key 账户的 `base_url` 默认 `https://api.anthropic.com`，允许用户修改为明确的 Anthropic-compatible 代理地址；前端不对非官方域名做额外警告，和 GPT / OpenAI-compatible API Key 的 Base URL 体验保持一致。如需严控官方直连，必须通过独立需求增加只允许官方域名的档案。
- OAuth 账户的 `base_url` 默认 `https://api.anthropic.com/v1`；允许用户修改为同协议代理地址，但上游仍只走 Anthropic Messages / Models / Count Tokens 原生语义。
- `Anthropic-Version` 不是账号字段；客户端请求带 `anthropic-version` 时透传客户端值，否则由上游 adapter 固定补齐当前协议默认 `2023-06-01`。
- `credentials.supported_endpoint_modes` 省略时，API Key 与 OAuth 账户都默认 `['messages_json', 'messages_sse', 'message_token_counting']`。
- 新建账户默认写入 `pending_test` 且不可调度，测试通过后才恢复正常。
- Anthropic API Key 账户不显示 OpenAI Organization、OpenAI Project、Codex Responses、GPT 客户端兼容能力等字段；Anthropic OAuth 展示独立的官方授权链接、回调 URL、Refresh Token 和直接录入 Access Token 三种入口。
- WIF 后续若重新开放，必须由服务端受控的可信 assertion provider 自动获取和轮换 JWT，校验 `exp`，不得允许账户提交任意文件路径或 source URL；token exchange 还必须使用账户代理、独立超时、请求时 DNS SSRF 校验和有界 JSON 读取。只重读数据库里的同一 `identity_token` 不视为轮换能力。

可选高级字段：

- `request_timeout_seconds`：可沿用当前账户或网关超时配置，不为 Anthropic 单独新增重型超时体系。
- `service_tier`、`inference_geo`、`speed` 等 Anthropic 原生字段默认由请求体透传。当前模型目录不把 `auto` / `standard_only` 或 `fast` 映射为本站 `priority` / `flex`，因此不提供 Anthropic 账户服务等级选项；模型明确声明 effort 时，用户显式配置的账户思考级别覆盖是唯一现有请求体改写例外，会写入 `output_config.effort`。

### Anthropic OAuth 托管与直贴模式

- 当前支持三种入口：
  - 站内生成官方 OAuth 授权链接，浏览器授权后粘贴完整回调 URL；
  - 直接提供 `refresh_token` 由后端换取 Access Token；
  - 直接导入已有官方 OAuth Access Token，前端仍推荐粘贴 `CLAUDE_CODE_OAUTH_TOKEN` 或 `ANTHROPIC_AUTH_TOKEN`。
- 直接导入模式下 `access_token` 为必填；若同时提供 `refresh_token`，会一并保存，并可用于后续手动刷新或重新授权。
- Anthropic OAuth 账户的真实上游认证始终是 `Authorization: Bearer <access_token>`；它不复用 OpenAI OAuth 的刷新、预热或后台批量保活链路。
- Anthropic OAuth 的手动刷新、Refresh Token 创建与重新授权只复用 Anthropic 自己的路由和 token endpoint，不共享 OpenAI OAuth 的 token endpoint、错误码语义或文案。
- Claude Code 只作为下游客户端画像和 Bearer Token 来源之一，不等于项目内要复刻 Claude.ai / Claude Code 订阅登录流程。

### 站内浏览器授权式订阅 OAuth 不纳入当前目标

`sub2api`、`new-api` 等参考项目实现的 Claude OAuth / SetupToken 链路更接近 Claude 订阅 / Claude Code token 账号链路，不是普通 Anthropic Console API Key，也不是组织级 WIF。当前目标已经接入官方 Anthropic OAuth / PKCE 浏览器换码与官方 token endpoint，但仍不接入 Claude 订阅账号、Setup Token、Cookie / sessionKey 或 Claude Code 私有登录链路，避免把订阅账号风控和会话限制误写进官方 API 账户主链路。

如果重新提出 OAuth 需求，必须先完成真实链路验证：

- 用真实 Claude / Claude Code 订阅 OAuth 凭据验证 `POST /v1/messages`、流式 SSE、`/v1/messages/count_tokens` 和 `/v1/models` 是否能通过本项目中转；WIF 不属于本项验证。
- 验证本地 API Key 作为下游认证时，上游 `Authorization: Bearer <access_token>` 不会被 Anthropic 判定为第三方异常或拒绝。
- 验证 OAuth 账号链路是否还需要额外 Claude Code OAuth 专属 header；API Key 路径只允许在 `claude_code` 客户端画像下补齐已验证的 Claude Code header / query。
- 验证 access token 刷新、setup token 有效期、账号额度、会话限制、RPM 和风控失败是否能稳定归入现有账户错误处理策略。
- 完整 HTTP 状态、错误类型和正文不能作为系统自动死亡证据；安全文本请求只做 opaque 请求级接管。只有本地 transport failure 才进入统一确认、半开、冷却复测和并发归零流程，用户显式策略另按声明动作执行。

在新需求完成真实验证并重新设计前，前端不展示 Anthropic 订阅网页登录 OAuth 或 WIF。当前项目已开放官方 Anthropic OAuth / PKCE 浏览器换码、Refresh Token 创建以及 Bearer Token 型 `oauth` 账户导入与创建，但仍不新增订阅网页登录、Setup Token、Cookie / sessionKey 或其他私有登录代理运行路径。

## 本地网关入口

当前系统主入口仍是 OpenAI-compatible，但 Anthropic native 接入需要新增本地 Anthropic 形态入口。入口不应复用 OpenAI v1 Chat / Responses 的字段路径。

当前支持：

| 本地路径 | 方法 | 语义 | 上游路径 |
| --- | --- | --- | --- |
| `/v1/messages` | `POST` | Anthropic Messages | `<base_url>/v1/messages` |
| `/messages` | `POST` | 根路径容错入口 | `<base_url>/v1/messages` |
| `/v1/messages/count_tokens` | `POST` | Anthropic Token Counting | `<base_url>/v1/messages/count_tokens` |
| `/messages/count_tokens` | `POST` | 根路径容错入口 | `<base_url>/v1/messages/count_tokens` |
| `/v1/models` | `GET` | 本地 Anthropic 模型目录 | 不请求上游 |
| `/models` | `GET` | 本地 Anthropic 模型目录 | 不请求上游 |

本地认证：

- OpenAI-compatible 客户端继续使用 `Authorization: Bearer <本地 API Key>`。
- Anthropic SDK / Claude-compatible 客户端通常使用 `x-api-key`，因此本地网关应允许 `x-api-key: <本地 API Key>` 作为本地调用凭据。
- 如果同时存在 `Authorization` 和 `x-api-key`，后端必须有确定优先级。建议优先读取 `Authorization`，仅当缺失时读取 `x-api-key`。
- 本地 API Key 通过后，进入该 Key 绑定路由策略下的分组和账户调度；上游请求必须替换为命中账户的 Anthropic API Key，不能把本地 `x-api-key` 透传给上游。

模型路由：

- Anthropic native 请求的目标模型来自请求体 `model`。
- 本地 API Key 选择的路由策略可以同时绑定 OpenAI、Anthropic、GLM、DeepSeek 等分组，但每次请求必须先用 `model` 命中目标供应商，再只在该策略绑定的目标供应商分组内调度；账户 `provider_protocol_profile_id` 继续约束候选账户。
- `GET /v1/models` 的响应形态应按请求协议渲染。Anthropic native 客户端应看到 Anthropic Models API 形态；OpenAI-compatible 客户端继续看到 OpenAI list 形态。

## 分层落点与兼容边界

Anthropic 接入必须复用项目现有网关流水线，不能新增一套按状态码或错误类型直接判死账号的状态机。各层职责固定如下：

| 层级 | Anthropic 职责 | 禁止事项 |
| --- | --- | --- |
| 全局网关层 | 客户端入口保护、raw body 上限、JSON 非法标记、认证前 / 认证后来源保护、本地 API Key 认证 | 不认识 Anthropic 错误类型，不写账号状态，不按地区、状态码或错误码熔断 |
| 协议层 `protocols/anthropic-v1` | 识别 `/v1/messages`、`/v1/messages/count_tokens`、`/v1/models`，抽取 `model`、`stream` 等轻量元数据，定义 Messages JSON / SSE 语义帧、usage 解析和 Anthropic Models 响应形态 | 不判断官方直连还是第三方兼容，不写供应商价格，不复用 OpenAI `choices` / Responses 字段路径 |
| 官方供应商适配层 `adapters/anthropic` | 官方 Anthropic API Key 上游 URL 拼接、`x-api-key` 写入；`anthropic-version` 默认补齐、普通客户端 `anthropic-beta` 透传、`claude_code` 画像下 Claude Code header / query 补齐和官方直连诊断字段 | 不生成 OpenAI 组织 / 项目 / Beta 头，不把第三方兼容入口归为官方 Anthropic，不实现 WIF、订阅 OAuth / Setup Token / Claude Code token 账号链路，不把 Anthropic header 做成账号配置，不在原生 Anthropic 透传路径伪造 Claude Code 请求体 |
| 第三方兼容供应商适配层 | DeepSeek、GLM、Kimi 等 Anthropic-compatible 入口按各自 `providerCode` 建 adapter，可复用 `protocolCode=anthropic` 的 Messages 协议适配器 | 不共用官方 `anthropic` 账号池、模型目录、价格目录、默认分组和错误策略 |
| 客户端画像层 | 默认普通 Anthropic native / Anthropic SDK 类客户端语义；显式 `x-juhe-client-profile: claude_code` 或官方 Claude Code 多信号命中时识别为 Claude Code 客户端画像 | 不把单个 User-Agent、模型名或某个错误码临时推断成 Claude Code，不把 Codex 可重试语义扩散给 Anthropic native |
| 候选账号筛选层 | 用 `provider_protocol_profile_id`、endpoint mode、模型路由和账户模型限制过滤候选账号 | 因模型或端点不匹配被跳过的账号不算失败，不进入账户错误处理策略 |
| 调度运行态层 | 复用本地账号短期屏蔽、半开探测、事前确认、IP 级账号回避、上游桶避让和分组 fallback | 不把短 TTL 运行态写成持久健康事实，不按 `authentication_error`、`rate_limit_error` 等类型直接冷却 |
| 返回侧协议适配层 | 把 Anthropic JSON / SSE 解析为统一响应语义帧，提取可见输出、工具调用、thinking、完成状态、usage 和错误事件；显式 OpenAI -> Anthropic bridge 命中时按下游 OpenAI 协议渲染 | 不决定账号是否永久不可用，不在未命中显式 bridge 时把 Anthropic 事件伪装成 OpenAI chunk |
| 统计与审计层 | 保存 `usage_semantic=anthropic`、缓存读写 token、thinking token、`request-id` 和受限错误摘要 | 不在 API 路由实时扫描 usage 明细或审计 payload，不用上游错误文案覆盖账号状态原因 |

当前兼容策略收敛为“原生透传 + API Key 上游认证 + 显式本地边界”：

- 官方 Anthropic 直连请求默认 raw passthrough，只做本地认证、路径归一、header 过滤、上游认证替换和 Base URL 拼接；账户显式配置且最终模型支持的思考级别覆盖会在发送前改写 `output_config.effort`。
- 账户测试由后端生成无工具 Claude Code Messages 健康探针；Anthropic native 真实客户端请求不自动补齐 `max_tokens`、不自动移动 system message。OpenAI Chat / Responses 请求转成 Messages 只允许在显式 bridge 命中时发生。
- `anthropic-version` 默认值和 `anthropic-beta` 客户端 header 透传属于上游请求准备，不属于失败后的请求改写。
- WIF、Claude.ai / Claude Code 订阅 OAuth、Setup Token / Claude Code token 账号链路不进入请求准备、调度和返回侧流程；不能在运行时通过状态码或错误类型临时推断并切到这些认证行为。Claude Code 下游客户端画像只通过显式本地 header 或官方 CLI 多信号识别，不改变 Anthropic API Key 上游认证。
- Anthropic 官方 OpenAI SDK compatibility 只作为用户迁移线索。本项目让 OpenAI-compatible 客户端调用 Claude 模型时，必须使用显式 OpenAI -> Anthropic adapter 和响应渲染器，不隐藏在官方直连透传链路里。

## 上游请求边界

Anthropic API Key 账户按原生 Messages API 透传，不走 OpenAI v1 adapter。

请求体：

- 除账户模型映射改写顶层 `model`，以及用户显式配置、最终模型明确支持时将账户思考级别覆盖写入 `output_config.effort` 外，默认 raw body passthrough，保留 Anthropic 原生字段：`max_tokens`、`messages`、`system`、`tools`、`tool_choice`、`thinking`、`stop_sequences`、`stream`、`metadata`、`service_tier`、`container`、`context_management`、`mcp_servers`、`output_format`、`output_config`、`cache_control` 等。
- 模型映射必须复用现有大 body JSON 解析边界，只在 JSON body 顶层 `model` 命中映射时改写；不解析或改写 `messages`、`tools`、`thinking` 等嵌套字段。
- Anthropic native 客户端应按 Anthropic 格式提交；只有 OpenAI -> Anthropic bridge 命中时，才把 OpenAI `messages[].role = system/developer` 转成 Anthropic 顶层 `system`。
- `/v1/chat/completions`、`/v1/responses` 不由普通 Anthropic native 账号承接；只有命中混合供应商账户且该账户声明 OpenAI -> Anthropic Messages 转换时才改写为 `/v1/messages`。如果 API Key 绑定的策略路由只进入 Anthropic native 普通分组，应在本地请求能力过滤阶段返回明确不兼容错误，错误码为 `anthropic_native_group_openai_compatible_request`，并提示改用混合供应商账户或 Anthropic `/v1/messages`。
- OpenAI-compatible 客户端如需通过混合供应商 bridge 调用要求 Claude Code 请求形态的 Anthropic-compatible 上游，应显式发送 `x-juhe-client-profile: claude_code`。opencode 1.1.11 的自定义 provider 需要把该 header 写在 `provider.<id>.options.headers`，顶层 `headers` 不会透传。命中后网关会把上游目标改为 `/v1/messages?beta=true`，改写 Claude Code header，并补 `system` 数组、`metadata.user_id`、`thinking`、`output_config` 的最小 body envelope；该模式保留下游 opencode / OpenAI function tools，工具继续按普通 bridge 转成 Anthropic tools，上游不接受时返回真实上游错误。普通 bridge 不默认补这些字段。
- `max_tokens` 是 Anthropic Messages 必填字段之一。账户测试的无工具 Claude Code 健康探针会显式传入；对真实客户端请求，默认让上游返回真实错误，除非后续明确要做本地兼容补齐。

请求头：

- API Key 账户上游认证写入 `x-api-key: <Anthropic API Key>`。
- 上游写入 `anthropic-version`：优先客户端请求头；客户端未传时由 adapter 补当前默认 `2023-06-01`。
- `anthropic-beta`：普通 Anthropic 请求只保留客户端显式值，多个值按逗号去重合并。系统不从账号凭据追加 beta，不默认追加 `oauth` 或账号级 CLI 模拟相关 beta。`claude_code` 客户端画像下，上游请求准备层会合并 `claude-code-20250219`、`interleaved-thinking-2025-05-14` 与 `effort-2025-11-24`，保留客户端已有 beta 并去重。
- `?beta=true` 查询参数按请求 URL 原样拼接到上游路径。官方 Claude Code 和 new-api 兼容实现都可能使用该形态；本项目不把它转换成账户状态或失败规则。
- `x-claude-code-session-id`、`x-claude-code-agent-id` 等官方 CLI 请求头属于客户端请求事实，默认跟随安全 header 过滤规则透传；`claude_code` 画像缺少 `x-claude-code-session-id` 时由网关为本次请求补齐，显式 OpenAI -> Anthropic bridge 的 `metadata.user_id.session_id` 与该 header 保持一致；本项目内部 `x-juhe-client-profile` 仍必须过滤。
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩协商头、内部 tracing 头和会导致上游误判的本地网关头。
- 不生成 `OpenAI-Organization`、`OpenAI-Project`、`OpenAI-Beta` 或 GPT / Codex 专属 header。
- `request-id`、`anthropic-organization-id` 等响应头只作为上游响应元数据捕获，不作为请求头注入。

Base URL 规则：

- 官方默认地址保存为 `https://api.anthropic.com`，拼接上游路径时归一到 `/v1/messages` 等版本路径。
- 允许填写 `https://api.anthropic.com/v1`，保存或拼接时需要避免重复 `/v1/v1`。
- 禁止填写具体接口路径作为 base URL，例如 `/v1/messages`、`/v1/messages/count_tokens`。
- WIF token exchange、订阅 OAuth 授权、token refresh 和 Claude Code 专属端点不进入当前 Base URL 规则。
- 继续使用现有 SSRF 防护、协议限制、用户密码禁止、查询参数禁止、片段禁止、路径规整等 base URL 校验。

## 返回、流式与响应语义

Anthropic 返回侧必须新增协议适配器，不能复用 OpenAI v1 的 `choices[].message`、`choices[].delta` 或 Responses 事件路径。

非流式 JSON 成功响应示例语义：

```json
{
  "id": "msg_...",
  "type": "message",
  "role": "assistant",
  "model": "claude-opus-4-8",
  "content": [
    { "type": "text", "text": "..." }
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 25,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  }
}
```

响应语义帧抽取规则：

- 可见文本来自 `content[].type = text` 的 `text`。
- 工具调用来自 `content[].type = tool_use`，字段包括 `id`、`name`、`input`。
- 工具结果由下一次请求中的 `messages[].content[].type = tool_result` 表达；网关只负责透传和审计，不执行工具。
- Thinking 内容来自 `content[].type = thinking` 或流式 `thinking_delta`；是否写给下游取决于客户端协议和安全策略，但原始审计和 usage 统计必须保留足够事实。
- 完成状态来自 `stop_reason`，常见值包括 `end_turn`、`max_tokens`、`stop_sequence`、`tool_use`、`pause_turn`、`refusal`。
- 错误来自非 2xx JSON 或 200 SSE 中的 `event: error`。

这些语义帧只说明“上游响应表达了什么”，不直接决定账号状态。响应检查策略、账户错误处理策略和客户端最终渲染必须在后续处理层级执行。

流式 SSE：

- `stream: true` 时上游返回 Anthropic SSE。
- 关键事件包括 `message_start`、`content_block_start`、`content_block_delta`、`content_block_stop`、`message_delta`、`message_stop`、`ping`、`error`。
- `content_block_delta.delta.type` 可能是 `text_delta`、`input_json_delta`、`thinking_delta`、`signature_delta`。解析器必须允许未来新增事件和 delta 类型。
- `message_delta.usage` 是累计 token 计数，不能按每个 delta 累加；最终用量应取最后一个可用累计值。
- 流内 `event: error` 可能发生在 HTTP 200 之后，必须进入流式失败和账号副作用链路，而不是只按 HTTP 状态判断成功。
- SSE 解析必须按增量窗口处理完整事件，不能为了常规解析拼接完整大响应体。

流式提交边界：

- 只有在尚未向下游写出任何 SSE body 时，网关才可以隐藏当前上游失败并服务端切换到其他账号 / 其他分组。
- `ping`、`message_start`、`content_block_start` 等不算可见模型输出，但只要已经实际写给下游，就不能再静默替换成另一条上游流。
- `text_delta`、`input_json_delta`、`thinking_delta`、`signature_delta`、工具调用参数和任何会改变 assistant 内容 / 工具状态的事件都属于有状态输出；写出后失败只能走客户端可见失败或断流，不做服务端拼接。
- 未知事件和未知 delta 类型默认允许透传和有限审计；不能因为事件类型未知就直接判定账号异常。

下游渲染：

- Anthropic native 客户端默认原样返回 Anthropic JSON / SSE 事件。
- OpenAI-compatible 客户端请求 Claude 模型时，必须命中显式 OpenAI -> Anthropic bridge，由桥接层把 Anthropic JSON / SSE 渲染回 Chat JSON / Chat SSE / Responses JSON / Responses SSE。
- 参考 `new-api` 的 Claude channel 和 `sub2api` 的 protocol converter 可以证明这类互转复杂度高，必须单独测试工具调用、图片、PDF、thinking、cache、流内错误和 usage，不能顺手实现或静默降级。

## 错误处理

Anthropic 非流式错误结构：

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "..."
  },
  "request_id": "req_..."
}
```

常见错误类型：

- `invalid_request_error`
- `authentication_error`
- `billing_error`
- `permission_error`
- `not_found_error`
- `request_too_large`
- `rate_limit_error`
- `api_error`
- `timeout_error`
- `overloaded_error`

处理要求：

- HTTP 状态码、`error.type`、错误文案和 `request_id` 只作为使用记录、审计、诊断摘要和账户错误处理策略输入。
- 代码不得内置 `authentication_error = 账号异常`、`rate_limit_error = 账号限流`、`overloaded_error = 临时不可调用` 这类固定映射。
- 通用 Anthropic 客户端不解释完整上游非 `2xx` 的状态或错误体；安全文本 Messages 请求只按 `response.ok=false` 做内容无关的请求级 Key/账号接管，不写共享状态。Claude Code 精确画像才允许额外按声明的协议结构处理，用户显式策略按配置执行。transport / timeout 失败仍按“独立确认 -> 切换其他账号 / 分组 -> 后台事前确认”的统一流程处理。
- 账户错误处理策略可以匹配 Anthropic 的状态码、错误类型、错误码或文案；这是账户所有者的显式管理意图，命中后按配置的 `retry_next`、TTL 避让或状态动作执行并保留来源，不走系统默认 transport 分类。账户状态动作与当前请求切号彼此独立：所有请求在未交付可用结果且下游尚未语义提交时均按统一规则切换候选。
- Anthropic `event: error`、缺少 `message_stop`、上游 EOF 或流式中断按流式失败流水线处理；是否写持久账号状态仍由确认阶段决定。
- 最终返回给 Anthropic native 客户端的本地错误使用 Anthropic error shape，而不是 OpenAI error shape；所有候选账号耗尽时返回本地统一错误语义，不透传最后一个上游错误体作为权威结论。
- `request-id` 响应头和 body 内 `request_id` 都应写入审计元数据，便于排障。
- 本地请求错误、模型路由失败、端点不支持、API Key 额度不足、分组无可承接账号等发生在命中上游账号之前的失败不写 Anthropic 账号状态。
- 不在代码里硬编码 Anthropic 错误文案作为限流、余额不足或账号异常判断；账户副作用仍通过可配置账户错误处理策略和统一上游失败流程落地。

## Usage、成本与统计

Anthropic usage 与 OpenAI usage 不同，必须单独维护语义。

当前使用记录明细已落地 input / output / cache read / cache write / 1h cache / thinking token 口径，并保存 `usage_semantic = anthropic`。统计预聚合窗口、授权消耗和大盘卡片仍只使用现有通用维度，后续如需展示 cache write、1h cache 或 thinking 聚合，必须扩展 worker 和窗口表，不在请求链路临时扫描明细。

需要解析和保存的字段：

| 字段 | 含义 | 统计要求 |
| --- | --- | --- |
| `input_tokens` | 非缓存输入 token | 计入输入 token |
| `output_tokens` | 输出 token 总数 | 计入输出 token |
| `cache_creation_input_tokens` | 创建 prompt cache 的输入 token | 单独计入缓存写入 token |
| `cache_read_input_tokens` | 从 prompt cache 读取的输入 token | 单独计入缓存读取 token |
| `cache_creation.ephemeral_5m_input_tokens` | 5 分钟 TTL 缓存写入 token | 使用记录明细保留在缓存写入 token 中，并按普通 cache write 价格估算 |
| `cache_creation.ephemeral_1h_input_tokens` | 1 小时 TTL 缓存写入 token | 使用记录明细单独保存为 1h cache write，并按 1h cache write 价格估算 |
| `output_tokens_details.thinking_tokens` | thinking 相关输出 token | 单独计入 thinking 输出 token |
| `inference_geo` | 推理区域 | 仅作诊断和审计字段 |

统计口径：

- `input_tokens` 不包含缓存读取和缓存写入。计算上下文长度或长上下文价格档位时，应使用 `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。
- 缓存读写 token 不能并入普通输入 token 后丢失来源，否则 prompt cache 成本、命中率和授权消耗口径会失真。
- `thinking_tokens` 属于输出 token 的分解字段，`output_tokens` 仍是计费权威总数；统计展示可额外显示 thinking 占比。
- 使用记录应保存 `usage_semantic = anthropic` 或等价协议字段，避免统计 worker 用 OpenAI 口径解释 Anthropic usage。
- 业务统计、授权额度、趋势和 TopN 仍必须由 worker 增量写入预聚合表；不能在接口请求路径临时扫描 usage 明细做 Anthropic 汇总。

价格口径：

- Anthropic 官方价格直接维护在 `providerCode=anthropic + model` 对应模型行，由管理员修改当前价格；现有 usage cost breakdown 保存请求当次实际单价和成本，后续改价不重算历史记录。
- Long context、prompt cache 读写、1h cache、batch 折扣等价格差异必须有明确模型价格字段支撑；没有字段前只记录 token，不编造成本。`thinking_tokens` 是 `output_tokens` 的分解字段，不单独重复计费。
- Anthropic-compatible 国产入口的价格按对应供应商维护，不能套用 Anthropic 官方价格。

## 模型目录

Anthropic 模型目录必须单独维护在 `anthropic` 供应商下。

模型目录从官方 Models API / 模型文档同步，优先覆盖当前可调用 Claude 文本模型。目录维护应能跟随官方 Models API 返回，不把具体型号写成长期硬编码依赖；列表顺序同时作为账户测试和模型下拉框的默认优先顺序，按官方 Models API“新模型在前”的口径维护。

截至 `2026-06-22`，当前可见官方 / Claude Code 兼容模型顺序：

- `claude-fable-5`
- `claude-mythos-5`
- `claude-mythos-preview`（`2026-06-30` 前可见）
- `claude-opus-4-8`
- `claude-opus-4-7`
- `claude-opus-4-6`
- `claude-opus-4-6-thinking`
- `claude-opus-4-5`
- `claude-opus-4-5-20251101`
- `claude-opus-4-1`（`2026-08-05` 前可见）
- `claude-opus-4-1-20250805`（`2026-08-05` 前可见）
- `claude-sonnet-4-6`
- `claude-sonnet-4-6-thinking`
- `claude-sonnet-4-5`
- `claude-sonnet-4-5-20250929`
- `claude-haiku-4-5`
- `claude-haiku-4-5-20251001`
- `claude-sonnet-5`
- `best`
- `fable`
- `opus`
- `opus[1m]`
- `opusplan`
- `sonnet`
- `sonnet[1m]`
- `haiku`

模型目录字段要求：

- `providerCode = anthropic`
- `model` 优先使用 Anthropic 官方模型 ID，保留官方大小写和连字符写法；Claude Code 官方模型配置别名必须通过显式模型别名或账户模型映射落到 `providerCode=anthropic + model`，兼容网关 / 代理专用别名不能混入官方 Anthropic 模型发现目录。
- 当前内置目录补充保留官方 current / dated ID：`claude-mythos-preview`、`claude-haiku-4-5-20251001`、`claude-sonnet-4-5-20250929`、`claude-opus-4-5-20251101`、`claude-opus-4-1`、`claude-opus-4-1-20250805`。其中 `claude-opus-4-1*` 已有 `shutdown_date = 2026-08-05`，到期后自动不再展示和计价。
- Claude Code 模型配置别名进入目录：`best`、`fable`、`opus`、`opus[1m]`、`opusplan`、`sonnet`、`sonnet[1m]`、`haiku`。`default` 只是 Claude Code 用来清除模型覆盖并回到推荐模型的控制值，不是模型别名，不进入目录。
- `claude-opus-4-6-thinking`、`claude-sonnet-4-6-thinking` 如进入模型发现目录，必须各自拥有明确的 `providerCode + model` 模型行；管理员可以在价格编辑表单中复制同族模型当前价格作为录入起点，但保存的是该模型行自己的完整当前价格，运行时不能按名称寻找同族价格。Antigravity 前缀 / 后缀 / `google-antigravity` 等兼容代理写法不进入官方 Anthropic 模型发现目录。
- Antigravity 的 `-low`、`-medium`、`-high`、`-max` 等 effort 后缀不作为单独模型展示；可信 driver 必须在计价前将其显式规范化为最终上游模型和思考级别，再按 `providerCode + model` 查当前价格，不允许字符串价格回落。旧的 `claude-opus-4-5-thinking` / `google/antigravity-claude-opus-4-5-thinking` 不收录。以上 Antigravity 名称不代表 Anthropic 官方直连模型 ID。
- `claude-fake-5` 是本项目真实 Anthropic-compatible 代理联调用过的模型 ID；如需继续使用，应保存为明确的供应商模型行，由管理员直接维护当前价格，或保持 `unpriced`。它不能按名称继承 Claude 族价格，也不进入官方 Anthropic 模型发现目录；后续如果该代理拆成独立供应商，应迁移到对应供应商目录。
- 已退休或 shutdown date 已过的模型不进入目录，例如 `claude-opus-4-20250514`、`claude-sonnet-4-20250514`、`claude-3-7-sonnet-20250219`、`claude-3-5-haiku-20241022`。
- `supportedApiProtocols` 填 Anthropic Messages 对应能力。
- 服务等级和思考级别只复用模型行现有 `supportedServiceTiers`、`supportedReasoningEfforts` 和账户现有 `service_tier_override`、`reasoning_effort_override`；只填写已核验值，没有可登记值时保持空数组，由 Anthropic driver 负责目标协议字段转换，模型能力继续直接维护在当前模型行。
- `contextWindowTokens`、`releaseDate`、`displayName` 以官方 Models API 或官方模型文档为准；不确定时留空，不为了排序编造；同一代或无精确发布日期的模型使用内置目录顺序字段保持下拉框从新到旧。
- `ownedBy` 或等价展示字段应明确为 Anthropic，不跟第三方 Anthropic-compatible 模型混在一起。
- `GET /v1/models` 对 Anthropic native 客户端返回本地 Anthropic 模型目录，不请求上游；如需增加“同步上游模型”能力，必须限制响应体大小并按当前性能底线分块读取。

国产 Anthropic-compatible 模型目录：

- DeepSeek Anthropic-compatible 使用 DeepSeek 模型目录和价格，见 [DeepSeek 账号接入](DeepSeek账号接入.md)。
- GLM Coding Anthropic-compatible 如果接入，使用 GLM 模型目录和价格，见 [智谱 GLM 账号接入](智谱GLM账号接入.md)。
- Kimi / Moonshot 等如果接入，应新增对应供应商文档和模型目录，不进入 `anthropic` 官方目录。

## 分组、授权与 API Key 路由

官方 Anthropic 档案应创建独立默认分组：

- 默认 Anthropic 分组：绑定 `providerCode = anthropic`

规则：

- Anthropic 账户只能加入相同 `provider_code` 的分组；`provider_protocol_profile_id` 仍保留账户真实协议档案。
- Anthropic 官方账户不能加入 DeepSeek、GLM 或其他第三方供应商分组。
- 授权实例账户继承来源账户的官方 Anthropic 资源事实，但被授权侧实例状态、分组绑定、冷却和用量归属仍保持独立。
- 路由策略多分组绑定允许跨供应商。跨供应商请求必须先按请求 `model` 定位目标供应商，再进入当前 API Key 所选路由策略已绑定的对应供应商分组调度；账户协议档案继续约束候选账户。
- Anthropic native 请求的会话连续性主要来自客户端请求内容和 prompt cache；当前不新增跨请求会话亲和特殊规则。如果 Claude SDK / Claude Code 需要按会话稳定命中同一账号，必须通过新需求复用当前会话亲和机制扩展协议字段来源。

## 账号测试

Anthropic 账户测试必须复用真实网关链路。

测试请求使用无工具 Claude Code 健康探针，核心字段如下：

```json
{
  "model": "claude-opus-4-8",
  "max_tokens": 32000,
  "system": [
    { "type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.201.eb7; cc_entrypoint=sdk-cli;" },
    { "type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK." }
  ],
  "thinking": { "type": "adaptive" },
  "output_config": { "effort": "high" },
  "tools": [],
  "messages": [
    { "role": "user", "content": [{ "type": "text", "text": "请回复 OK" }] }
  ]
}
```

测试要求：

- 测试路径使用 `/v1/messages`。
- 新建账户的检查模型按“个人默认 > 管理员系统默认 > 协议档案默认”初始化，保存后严格读取账户自己的 `healthCheckModel`。新增和编辑表单测试固定使用当前表单检查模型；列表单项测试按需读取当前作用域可见的启用文本模型目录。
- 测试请求必须带 `anthropic-version: 2023-06-01`，并通过 `x-juhe-client-profile: claude_code` 触发上游 Claude Code `User-Agent`、`anthropic-beta`、`x-claude-code-session-id` 和 `?beta=true` 补齐。
- 人工单账户测试无论成功或失败都不保存模型选择，也不改写账户、授权实例、账户内 Key、状态、调度、冷却和最近错误；真实网关失败和后台检查继续维护生产运行态。
- `pending_test` 账户在人工测试成功或失败后都继续保持待检查，不因测试结果自动进入正常调度。
- 停用账户测试只保留诊断，不自动恢复停用状态。
- 账户页面不提供多账户批量测试。
- 测试返回的完整上游请求 URL、响应头、响应体、`request-id` 和原始错误只对所有者或管理员开放；授权用户只看脱敏摘要。

## 导入导出协议

账户导入协议新增 Anthropic 示例时，应使用项目自定义 JSON，不兼容 Anthropic Console 导出格式。

推荐写法：

```json
{
  "name": "Anthropic Claude 账号 1",
  "providerCode": "anthropic",
  "providerProtocolProfileId": "profile_anthropic_anthropic_v1",
  "type": "api_key",
  "status": "pending_test",
  "groupName": "默认 Anthropic 分组",
  "credentials": {
    "api_key": "sk-ant-...",
    "base_url": "https://api.anthropic.com",
    "supported_endpoint_modes": ["messages_json", "messages_sse", "message_token_counting"]
  }
}
```

Bearer Token 型 OAuth 导入示例：

```json
{
  "name": "Anthropic Claude OAuth 账号 1",
  "providerCode": "anthropic",
  "providerProtocolProfileId": "profile_anthropic_anthropic_v1",
  "type": "oauth",
  "status": "pending_test",
  "groupName": "默认 Anthropic 分组",
  "credentials": {
    "access_token": "sk-ant-oauth-...",
    "refresh_token": "optional-refresh-token",
    "base_url": "https://api.anthropic.com",
    "supported_endpoint_modes": ["messages_json", "messages_sse", "message_token_counting"]
  }
}
```

`providerProtocolProfileId` 是导入协议层唯一协议档案入口。当前 Anthropic 官方账户固定使用 `profile_anthropic_anthropic_v1`；导入协议接受 `type = api_key` 和 Bearer Token 型 `type = oauth`，但仍不接受旧接入类型别名、`setup_token`、WIF、Cookie / sessionKey 或云平台托管 Claude 字段。

## 参考实现观察

本地参考项目给出的可复用经验：

- `new-api` 将 Claude / Anthropic 作为独立 channel，单独维护 DTO、请求头、`/v1/messages` URL、SSE 解析和 OpenAI chunk 转换。这说明 Anthropic 返回侧不能硬套 OpenAI `choices` 路径。
- `new-api` 的 DTO 同时覆盖 text、image、tool_use、tool_result、thinking、signature、cache_control 等 Anthropic block，说明本项目也应在协议适配器层保留 block 结构，不要过早压成纯文本。
- `new-api` 对 Claude channel 会保留 `?beta=true`、透传客户端 `anthropic-beta`、补默认 `anthropic-version: 2023-06-01`，并用 `x-api-key` 写上游认证；这部分与本项目当前 adapter 一致，适合继续参考。
- `new-api` 针对 `claude-opus-4-6/4-7/4-8` 的 effort 后缀、`thinking.type=adaptive`、`output_config.effort` 和温度类字段裁剪有专门逻辑；这属于模型能力 / 供应商兼容策略，后续若做模型后缀或非 Anthropic 模型兼容，应在显式 provider profile 下实现，不应污染当前官方 API Key raw passthrough。
- `sub2api` 对 Anthropic API Key direct、Claude OAuth / SetupToken、Vertex Anthropic、Bedrock Claude、OpenAI Responses 互转分别建请求构造和转换路径。这说明“官方直连 API Key”“OAuth 账号”“云平台托管”“协议兼容代理”应分层处理。
- `sub2api` 的 Anthropic API Key 使用 `x-api-key`，OAuth / SetupToken 使用 `Authorization: Bearer <access_token>`，并由 Claude token provider 做 access token 缓存和刷新。这说明 OAuth 如果后续恢复立项，必须作为独立账号链路真实验证，不能混进 API Key adapter。
- `sub2api` 同时包含 Claude Code header / beta 模拟、5h 窗口、会话数量、RPM、TLS 指纹和上下文处理等重型 OAuth 专属能力。本项目当前只在 `claude_code` 客户端画像下补齐必要 header / query，不落 OAuth、TLS 指纹、会话窗口或订阅账号逻辑；这些重型能力仅作为新需求真实验证清单，不作为当前范围。
- `sub2api` 和 `new-api` 都有 OpenAI <-> Claude 深度互转代码，但这类能力涉及工具调用、图片、PDF、thinking、cache、usage 和流式状态机。本项目只按 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md) 的受控子集落地，不复制无边界转换。Claude Code-compatible body envelope 只服务显式 bridge 与兼容代理风控，不代表引入 Claude Code OAuth 或订阅账号链路。
- LiteLLM 的 Claude Code 指南说明 Claude Code 可以通过网关使用 Anthropic 或非 Anthropic 模型，但它要求模型映射、用量追踪和兼容边界显式配置。适合本项目参考的是“非 Anthropic 模型必须进入独立供应商 / 档案”，不是把所有 Claude Code 请求混进官方 Anthropic 账号池。
- CLIProxyAPI 对 `metadata.user_id`、`X-Client-Request-Id`、会话哈希和 Claude Code cloaking / CCH signing 有完整链路。这些可作为未来会话亲和与订阅账号链路的调研材料；当前 API Key 目标不采用 cloaking、签名或 OAuth 伪装。

不采用的参考实现范围：

- 不把 Claude Code OAuth 的全量伪装逻辑当成 Anthropic API Key 或通用 Anthropic 协议能力。
- 不对普通 API Key 请求默认追加 Claude Code CLI 专属 `anthropic-beta` 和 `User-Agent`；只允许在 `claude_code` 客户端画像下补齐。OpenAI -> Anthropic bridge 的 Claude Code-compatible body envelope 同样只能由显式画像触发。
- 不在当前目标新增 WIF、Anthropic 订阅 OAuth / Setup Token 创建、导入、刷新或上游请求路径。
- 不把第三方 Anthropic-compatible 入口合并到官方 `anthropic` 供应商。
- 不在 Anthropic 原生透传目标里实现 OpenAI Chat / Responses 到 Anthropic Messages 的隐式互转；显式桥接按 PLAN-0058 单独实现和验证。

## 官方资料与开源生态

官方资料：

- Claude API overview：<https://platform.claude.com/docs/en/api/overview>
- Claude Code authentication：<https://code.claude.com/docs/en/authentication>
- Claude Code model configuration：<https://docs.anthropic.com/en/docs/claude-code/model-config>
- Claude Code legal and credential use：<https://docs.anthropic.com/en/docs/claude-code/legal-and-compliance>
- Models overview：<https://platform.claude.com/docs/en/about-claude/models/overview>
- Choosing a model：<https://platform.claude.com/docs/en/about-claude/models/choosing-a-model>
- Model IDs and versioning：<https://platform.claude.com/docs/en/about-claude/models/model-ids-and-versions>
- Introducing Claude Fable 5 and Claude Mythos 5：<https://platform.claude.com/docs/en/about-claude/models/introducing-claude-fable-5-and-claude-mythos-5>
- Messages API：<https://platform.claude.com/docs/en/api/messages>
- Using the Messages API：<https://platform.claude.com/docs/en/build-with-claude/working-with-messages>
- Streaming messages：<https://platform.claude.com/docs/en/build-with-claude/streaming>
- Errors：<https://platform.claude.com/docs/en/api/errors>
- OpenAI SDK compatibility：<https://platform.claude.com/docs/en/api/openai-sdk>
- Tool use：<https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview>
- Structured outputs：<https://platform.claude.com/docs/en/build-with-claude/structured-outputs>
- Prompt caching：<https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
- Extended thinking：<https://platform.claude.com/docs/en/build-with-claude/extended-thinking>

官方 GitHub / 生态：

- TypeScript SDK：<https://github.com/anthropics/anthropic-sdk-typescript>
- Python SDK：<https://github.com/anthropics/anthropic-sdk-python>
- Claude Cookbooks：<https://github.com/anthropics/claude-cookbooks>
- Claude Quickstarts：<https://github.com/anthropics/claude-quickstarts>
- Claude Agent SDK demos：<https://github.com/anthropics/claude-agent-sdk-demos>
- MCP 规范：<https://github.com/modelcontextprotocol/modelcontextprotocol>

MCP 是工具 / 上下文协议，不是本项目的上游模型网关协议。如果要支持 Claude 的 `mcp_servers` 请求字段，应作为 Anthropic Messages 的请求体透传能力处理，不把 MCP 当成新的供应商协议。

## 实施清单

- 新增 `anthropic` 供应商种子。
- 新增 `protocolCode = anthropic`、`protocolVersion = v1` 和 `messages` / `models` / `message_token_counting` 端点族。
- 新增 `profile_anthropic_anthropic_v1`。
- 新增默认 Anthropic 分组。
- 前端账户创建在选择 `Anthropic Claude` 后展示 `Anthropic API Key` 与 `Anthropic OAuth`。
- 后端账户创建、编辑、导入和公开推送接口按 `providerCode=anthropic + providerProtocolProfileId=profile_anthropic_anthropic_v1` 校验官方直连档案。
- 网关本地认证支持 Anthropic native 客户端常用的 `x-api-key` 本地 API Key 入口。
- 新增 Anthropic native 请求适配器，负责路径拼接、请求头过滤、上游认证替换和 raw body 透传；上游认证写 `x-api-key`。
- 新增 Anthropic native 顶层 `model` 映射，映射只改写请求体 `model`，不改变其他 Anthropic 原生字段。
- Anthropic 多 API Key 启用 Key 级运行态隔离，单 Key 失败不直接冷却整个账户。
- 新增 Claude Code 客户端画像识别，显式 `x-juhe-client-profile: claude_code` 或官方 CLI 多信号影响本地画像、审计和上游请求头 / query 补齐；本地画像 header 不透传上游。
- 新增 Anthropic native 返回适配器，负责 JSON / SSE 响应语义帧抽取、usage 解析、流内错误识别和本地错误渲染。
- Anthropic 不新增协议专属账号状态机：generic HTTP 状态、错误体和正文中断不产生共享账户/Key 状态；只有可重放文本主派发已开始、响应头前 transport failure 可以消费整请求安全原地重试 token，兄弟 Key 先行且不占 token。账户状态动作只来自用户显式策略或带独立 generation 的后台 transport 证据，半开探测和冷却复测继续复用通用流程。
- Anthropic 使用记录明细当前落地 input / output / cache read / cache write / 1h cache / thinking tokens 和 `usage_semantic`；统计预聚合和授权消耗的新增维度需要后续 schema 与 worker 扩展。
- Anthropic 模型目录和价格目录单独维护，成本估算按 `providerCode=anthropic` 查找。
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗、模型价格和测试说明同步更新。

## 验证要求

当前代码落地必须覆盖：

- 创建 Anthropic API Key 账户，保存后落到 `profile_anthropic_anthropic_v1`。
- 新账户默认 `supported_endpoint_modes = messages_json/messages_sse/message_token_counting`，不伪装成 OpenAI Chat / Responses 原生能力；只有请求命中显式 OpenAI -> Anthropic bridge 时，才参与 OpenAI `/v1/chat/completions` 或 `/v1/responses` 调度。
- Anthropic 账户只能绑定 Anthropic 档案分组，不能加入 DeepSeek / GLM Anthropic-compatible 分组。
- 本地 `POST /v1/messages` 使用 `x-api-key` 或 `Authorization` 都能通过本地 API Key 认证。
- 本地 `POST /v1/messages` 带 `x-juhe-client-profile: claude_code`，或官方 Claude Code 多信号至少命中两个时识别为 Claude Code 客户端画像；未命中时仍是通用 Anthropic native。
- 上游请求命中 `https://api.anthropic.com/v1/messages`，上游认证头为账户 `x-api-key`，不会泄漏本地 API Key。
- 上游请求不会收到本地 `x-juhe-client-profile`，OpenAI / GPT 请求即使带该 header 也不能进入 Claude Code 画像。
- 客户端未传 `anthropic-version` 时由 adapter 使用协议默认 `2023-06-01`。
- 普通 Anthropic API Key 请求不会注入 OAuth、Claude Code CLI 专属 `anthropic-beta` 或 `User-Agent`；`claude_code` 画像请求会补齐缺失的 Claude Code `User-Agent`、`anthropic-beta` 和 `?beta=true`。显式 OpenAI -> Anthropic Messages bridge + `claude_code` 还应补最小 body envelope，未带画像 header 的普通 bridge 不应补。
- Anthropic 账户模型映射只改写顶层 `model`，不破坏 `messages`、`system`、`tools`、`thinking`、`stream`、`metadata` 等原生字段。
- Anthropic 多 API Key 中单个 Key 失败后只摘除当前 Key，不直接冷却整个账户；全部 Key 不可用时才跳过账户。
- 非流式响应能解析 `content[].text`、`stop_reason` 和 usage。
- 流式响应能按 `message_start -> content_block_* -> message_delta -> message_stop` 增量转发，并正确处理 `ping` 和未知事件。
- 流内 `event: error` 能进入统一上游失败链路。
- `authentication_error`、`rate_limit_error`、`overloaded_error` 等 Anthropic 错误类型只进入审计和策略匹配输入，不能被代码直接写成异常、限流或临时不可调用。
- 账户错误处理策略命中 Anthropic 错误类型后，按用户显式 provenance / generation / CAS 直接执行配置动作，不等待系统自动 confirmation；未命中策略的完整 HTTP / 正文只作请求级 opaque 诊断。只有独立后台 transport 证据才进入自动确认；当前请求是否继续候选不依赖错误类型或策略动作，只依赖是否尚未交付可用结果、是否尚未语义提交及请求预算。
- 尚未写出任何 SSE body 的流式失败可以服务端切号；已经写出任意 Anthropic SSE body 后不做静默拼接。
- 模型不匹配、端点不支持、本地认证失败、额度不足和分组无可承接账号不写 Anthropic 账号状态。
- 本地认证失败、分组无账号、端点能力不匹配和模型不匹配时返回 Anthropic error shape。
- `GET /v1/models` 对 Anthropic native 请求返回本地 Anthropic 模型目录，不请求上游。
- Prompt cache read、cache creation、1h cache 和 thinking usage 能写入使用记录明细；统计 worker 不在请求链路临时聚合，新统计维度需单独扩展窗口表。
- DeepSeek / GLM / Kimi 等 Anthropic-compatible 入口不会显示为官方 Anthropic 直连。
