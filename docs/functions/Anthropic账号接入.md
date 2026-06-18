# Anthropic 账号接入

## 范围

本文记录 Anthropic / Claude 供应商的目标接入方案、协议档案、账户创建类型、网关透传边界、模型目录、用量统计和后续实现注意事项。本文是实施前的目标方案文档；代码尚未落地时，运行事实仍以当前系统实际内置供应商为准。

本次调研结论同时参考了 Anthropic 官方文档、官方 SDK / GitHub 生态、Claude Code OAuth 认证说明、DeepSeek / GLM / Kimi 等 Anthropic-compatible 入口说明，以及本地参考项目 `sub2api`、`new-api` 的已落地拆分方式。参考项目只用于抽象分层经验，不复制其深度协议互转、全供应商大网关范围或尚未真实验证的 OAuth / Claude Code 账号链路。

结论：

- 直连 Anthropic 必须作为独立 `anthropic/v1` 原生协议档案接入，核心端点是 `POST /v1/messages`。
- Anthropic 官方确实提供 OpenAI SDK compatibility，但官方定位主要是测试和对比，不适合作为本项目直连接入的主协议。
- “Anthropic-compatible” 和 “Claude 官方模型”不是一回事。DeepSeek、GLM Coding、Kimi 等国内入口即使暴露 Anthropic Messages 形态，也应归到各自供应商，不应显示为官方 Anthropic。
- 第一阶段只做 Anthropic API Key 直连和 Messages / Models / Count Tokens 的轻量闭环。OAuth / Claude Code / Setup Token 账号链路暂缓，必须经过真实账号、真实客户端和真实中转链路验证后再单独立项；不做 Bedrock、Vertex、OpenAI Chat/Responses 到 Anthropic Messages 的自动互转。

## 协议与供应商定义

新增官方直连供应商：

```ts
type ProviderCode = 'anthropic'
type ProtocolCode = 'anthropic'
type ProtocolVersion = 'v1'
type ProviderProtocolProfileId = 'profile_anthropic_anthropic_v1'
type EndpointFamily = 'messages' | 'models' | 'message_token_counting'
type AccountSupportedEndpointMode = 'messages_json' | 'messages_sse'
type AnthropicAccountType = 'api_key'
```

建议显示名称为 `Anthropic Claude`。`anthropic` 是官方直连供应商，表示账号池、分组、模型目录、价格目录和响应策略都归属于 Anthropic 官方 Claude API。它不能复用 `openai`、`gpt`、`glm`、`deepseek` 或第三方代理供应商的账号池。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_anthropic_anthropic_v1` | `anthropic` | `anthropic/v1` | `https://api.anthropic.com` | Anthropic API Key | `messages_json`、`messages_sse` |

端点族边界：

- `messages`：`POST /v1/messages`，第一阶段核心转发端点。
- `models`：`GET /v1/models`，由本地模型目录按 Anthropic 形态返回，不默认请求上游。
- `message_token_counting`：`POST /v1/messages/count_tokens`，可作为账户测试、模型检测或成本预估的后续能力；第一阶段可以先只透传或暂不暴露。
- `message_batches`、`files`、`skills`、`managed_agents`、`sessions` 暂不进入网关主链路。需要时单独立项，避免把轻量中转扩展成 Anthropic 全平台管理面。

## 官方直连与兼容入口区分

系统必须明确区分三类资源：

| 类型 | 示例 | `providerCode` 建议 | `protocolCode` | 是否官方 Claude |
| --- | --- | --- | --- | --- |
| 官方直连 Anthropic | `https://api.anthropic.com` | `anthropic` | `anthropic` | 是 |
| 云平台托管 Claude | Bedrock、Vertex AI、Microsoft Foundry | 后续独立 `bedrock` / `vertex` / `foundry` | 云厂商专属或 `anthropic` 变体 | 是，但不是 Anthropic 账号体系 |
| Anthropic-compatible 第三方 / 国产入口 | DeepSeek `/anthropic`、GLM Coding `/api/anthropic`、Kimi `/anthropic` | 各自供应商，例如 `deepseek`、`glm`、`kimi` | `anthropic` | 不一定，通常不是 |

直连 Anthropic 的识别条件必须同时满足：

- `providerCode = anthropic`
- `providerProtocolProfileId = profile_anthropic_anthropic_v1`
- `base_url` 默认或明确指向 Anthropic 官方 API 根地址
- 凭据来自 Anthropic Console API Key
- `modelSource = anthropic_first_party`

国产或第三方兼容入口即使支持 `POST /v1/messages`，也只能描述为“Anthropic Messages 兼容协议”。UI、文档、模型目录和授权说明不得把这类入口称为“Claude 官方直连”。例如：

- DeepSeek Anthropic API 是 DeepSeek 模型的 Anthropic-compatible surface，已有独立文档见 [DeepSeek 账号接入](DeepSeek账号接入.md)。
- 智谱 GLM Coding Plan 的 Anthropic Messages 端点后续应新增 `profile_glm_coding_anthropic_v1`，不能混进官方 `anthropic` 供应商。
- Kimi / Moonshot、UCloud、OpenRouter、AiHubMix 等入口应按真实传输方和模型来源建立独立供应商或代理供应商档案。

## 账户创建类型

前端创建流程仍按“供应商 -> 账户类型 -> 凭据与调度配置”展开。

选择 `Anthropic Claude` 后第一阶段只展示一个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 |
| --- | --- | --- | --- | --- |
| Anthropic API Key | `api_key` | `profile_anthropic_anthropic_v1` | `api_key`、`base_url`、`anthropic_version` | 本地 Anthropic 目录中最低成本且可用的模型 |

保存规则：

- API Key 账户加密保存 `api_key`；列表不展示，编辑弹窗可查看和修改。
- API Key 账户的 `base_url` 默认 `https://api.anthropic.com`，允许用户修改为明确的 Anthropic-compatible 代理地址，但如果不是官方域名，页面必须提示“该地址不是官方 Anthropic 直连”。后续如果要严控官方直连，可增加只允许官方域名的档案。
- `anthropic_version` 默认 `2023-06-01`，作为账号非敏感配置保存；客户端请求带 `anthropic-version` 时优先使用客户端值，否则使用账户默认值。
- `credentials.supported_endpoint_modes` 省略时默认 `['messages_json', 'messages_sse']`。
- 新建账户默认写入 `pending_test` 且不可调度，测试通过后才恢复正常。
- Anthropic API Key 账户不显示 OAuth、Refresh Token、Access Token、OpenAI Organization、OpenAI Project、Codex Responses、GPT 客户端兼容模式等字段。
- 第一阶段不支持 Anthropic Workload Identity Federation Bearer Token；该能力涉及短期 token、身份联邦和组织级配置，后续单独作为企业认证档案设计。
- 第一阶段不支持 OAuth / Claude Code / Setup Token 账号。参考项目中 OAuth token、Claude Code 伪装 header、客户端版本检测、请求体守卫和 OpenAI <-> Claude 深度互转不进入本项目首期范围。

可选高级字段：

- `anthropic_beta`：账号级追加 beta，默认空。只在用户明确配置或客户端明确传入时透传；系统不默认追加 Claude Code 专属 beta。
- `request_timeout_seconds`：可沿用当前账户或网关超时配置，不为 Anthropic 单独新增重型超时体系。
- `service_tier`、`inference_geo`、`speed` 等 Anthropic 侧字段第一阶段由请求体透传，不做账号级强制注入；如果未来要做平台策略，应作为 Anthropic 供应商层配置，不能写进通用协议层。

### OAuth 暂缓与验证条件

`sub2api` 已经实现 Claude OAuth / SetupToken 相关链路，但该能力更接近 Claude / Claude Code 账号链路，不是普通 Anthropic Console API Key。当前判断是：本项目作为中转服务首期先不接入 OAuth，避免在没有真实验证前把不可稳定中转的账号形态写进产品、schema 和网关主链路。

后续如果重新评估 OAuth，必须先完成真实链路验证：

- 用真实 Claude / Claude Code OAuth 凭据验证 `POST /v1/messages`、流式 SSE、`/v1/messages/count_tokens` 和 `/v1/models` 是否能通过本项目中转。
- 验证本地 API Key 作为下游认证时，上游 `Authorization: Bearer <access_token>` 不会被 Anthropic 判定为第三方异常或拒绝。
- 验证必要 `anthropic-beta`、User-Agent、Claude Code header 是否必须注入；如果必须注入，必须作为 OAuth 专属 client profile，不能污染 API Key 路径。
- 验证 access token 刷新、setup token 有效期、账号额度、会话限制、RPM 和风控失败是否能稳定归入现有账户错误处理策略。
- 验证失败场景不能只靠状态码或错误类型判死账号，仍必须走统一确认、半开、冷却复测和并发归零流程。

验证通过并重新立项前，前端不展示 Anthropic OAuth，导入协议不接受 `connectionType = oauth`，数据库和网关代码不新增 Anthropic OAuth 运行路径。

## 本地网关入口

当前系统主入口仍是 OpenAI-compatible，但 Anthropic native 接入需要新增本地 Anthropic 形态入口。入口不应复用 OpenAI v1 Chat / Responses 的字段路径。

建议第一阶段支持：

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
- 本地 API Key 通过后，进入 API Key 绑定分组和账户调度；上游请求必须替换为命中账户的 Anthropic API Key，不能把本地 `x-api-key` 透传给上游。

模型路由：

- Anthropic native 请求的目标模型来自请求体 `model`。
- 模型路由落地前，一个本地 API Key 仍只能绑定同一供应商协议档案的分组。
- 模型路由落地后，本地 API Key 可以同时绑定 OpenAI、Anthropic、GLM、DeepSeek 等分组，但每次请求必须先用 `model` 命中全系统唯一的 `provider_protocol_profile_id`，再只在该档案的分组内调度。
- `GET /v1/models` 的响应形态应按请求协议渲染。Anthropic native 客户端应看到 Anthropic Models API 形态；OpenAI-compatible 客户端继续看到 OpenAI list 形态。

## 分层落点与兼容边界

Anthropic 接入必须复用项目现有网关流水线，不能新增一套按状态码或错误类型直接判死账号的状态机。各层职责固定如下：

| 层级 | Anthropic 职责 | 禁止事项 |
| --- | --- | --- |
| 全局网关层 | 客户端入口保护、raw body 上限、JSON 非法标记、认证前 / 认证后来源保护、本地 API Key 认证 | 不认识 Anthropic 错误类型，不写账号状态，不按地区、状态码或错误码熔断 |
| 协议层 `protocols/anthropic-v1` | 识别 `/v1/messages`、`/v1/messages/count_tokens`、`/v1/models`，抽取 `model`、`stream` 等轻量元数据，定义 Messages JSON / SSE 语义帧、usage 解析和 Anthropic Models 响应形态 | 不判断官方直连还是第三方兼容，不写供应商价格，不复用 OpenAI `choices` / Responses 字段路径 |
| 官方供应商适配层 `adapters/anthropic` | 官方 Anthropic API Key / OAuth 上游 URL 拼接、`x-api-key` 或 `Authorization: Bearer` 写入、`anthropic-version` 默认值、`anthropic-beta` 合并、官方直连诊断字段 | 不生成 OpenAI 组织 / 项目 / Beta 头，不把第三方兼容入口归为官方 Anthropic，不把 OAuth 专属 header 加到 API Key |
| OAuth token 准备层 | OAuth access token 缓存、过期前刷新、刷新单飞、token 版本校验和刷新失败收口 | 不解析 Anthropic Messages 请求体，不直接写账号持久状态，不影响 API Key 账户 |
| 第三方兼容供应商适配层 | DeepSeek、GLM、Kimi 等 Anthropic-compatible 入口按各自 `providerCode` 建 adapter，可复用 `protocolCode=anthropic` 的 Messages 协议适配器 | 不共用官方 `anthropic` 账号池、模型目录、价格目录、默认分组和错误策略 |
| 客户端画像层 | 第一阶段默认普通 Anthropic native / Anthropic SDK 类客户端语义；OAuth 如需 Claude Code 客户端兼容，必须作为显式 Anthropic OAuth client profile 设计 | 不把客户端 User-Agent、模型名或某个错误码临时推断成 Claude Code，不把 Codex 可重试语义扩散给 Anthropic native |
| 兼容策略与请求恢复层 | 仅在后续明确发现可恢复的协议状态时，基于请求特征和上游错误信号生成一次受控 body/header 变体重试 | 不修改原始 `req.rawBody`，不写账号状态，不开放用户自定义脚本，不做 OpenAI -> Anthropic 自动翻译 |
| 候选账号筛选层 | 用 `provider_protocol_profile_id`、endpoint mode、模型路由和账户模型限制过滤候选账号 | 因模型或端点不匹配被跳过的账号不算失败，不进入账户错误处理策略 |
| 调度运行态层 | 复用本地账号短期屏蔽、半开探测、事前确认、IP 级账号回避、上游桶避让和分组 fallback | 不把短 TTL 运行态写成持久健康事实，不按 `authentication_error`、`rate_limit_error` 等类型直接冷却 |
| 返回侧协议适配层 | 把 Anthropic JSON / SSE 解析为统一响应语义帧，提取可见输出、工具调用、thinking、完成状态、usage 和错误事件 | 不决定账号是否永久不可用，不把 Anthropic 事件伪装成 OpenAI chunk，未定义渲染器前不做跨协议互转 |
| 统计与审计层 | 保存 `usage_semantic=anthropic`、缓存读写 token、thinking token、`request-id` 和受限错误摘要 | 不在 API 路由实时扫描 usage 明细或审计 payload，不用上游错误文案覆盖账号状态原因 |

第一阶段兼容策略默认收敛为“原生透传 + 账号类型专属认证 + 显式本地边界”：

- 官方 Anthropic 直连请求默认 raw passthrough，只做本地认证、路径归一、header 过滤、上游认证替换和 Base URL 拼接。
- 账户测试由后端生成合法最小 Messages 请求；真实客户端请求不自动补齐 `max_tokens`、不自动移动 system message、不自动把 OpenAI Chat / Responses 请求转成 Messages。
- `anthropic-version` 默认值和 `anthropic-beta` 合并属于上游请求准备，不属于失败后的兼容恢复。
- API Key 和 OAuth 的请求头策略在上游请求准备阶段分支，不在协议层、调度层或返回侧用状态码反推账号类型。
- 如果后续需要兼容恢复，必须先补 [网关兼容策略与请求恢复设计](网关兼容策略与请求恢复设计.md) 对应 Anthropic 场景；策略输出只能是“继续默认失败流程”或“一次受控变体重试”，不能直接改账号健康。
- Anthropic 官方 OpenAI SDK compatibility 只作为用户迁移线索。若本项目要让 OpenAI-compatible 客户端调用 Claude 模型，应新增显式 OpenAI -> Anthropic adapter 和响应渲染器，不隐藏在官方直连透传链路里。

## 上游请求边界

Anthropic API Key 和 OAuth 账户都按原生 Messages API 透传，不走 OpenAI v1 adapter。

请求体：

- 默认 raw body passthrough，保留 Anthropic 原生字段：`model`、`max_tokens`、`messages`、`system`、`tools`、`tool_choice`、`thinking`、`stop_sequences`、`stream`、`metadata`、`service_tier`、`container`、`context_management`、`mcp_servers`、`output_format`、`cache_control` 等。
- 不把 OpenAI `messages[].role = system/developer` 自动翻译成 Anthropic 顶层 `system`。Anthropic native 客户端应按 Anthropic 格式提交；OpenAI -> Anthropic 互转后续单独立项。
- 不把 `/v1/chat/completions`、`/v1/responses` 自动改写为 `/v1/messages`。如果 API Key 只绑定 Anthropic native 分组却收到 OpenAI 路径，应返回本地“没有支持该端点的上游账户”类错误。
- `max_tokens` 是 Anthropic Messages 必填字段之一。账户测试由后端生成最小请求时必须显式传入；对真实客户端请求，默认让上游返回真实错误，除非后续明确要做本地兼容补齐。

请求头：

- API Key 账户上游认证写入 `x-api-key: <Anthropic API Key>`。
- OAuth 账户上游认证写入 `Authorization: Bearer <access_token>`；`access_token` 由 OAuth token 准备层读取或刷新。
- 上游写入 `anthropic-version`：优先客户端请求头，其次账户默认值 `2023-06-01`。
- API Key 账户的 `anthropic-beta`：保留客户端显式值，并可追加账户配置的非空值；多个值按逗号去重合并。系统不默认追加 `claude-code`、`oauth` 或 CLI 模拟相关 beta。
- OAuth 账户的 `anthropic-beta`：保留客户端显式值，并合并 OAuth 账号必要 beta 和账户配置；是否包含 `oauth-...`、`claude-code-...` 或其他 Claude Code beta 由 OAuth adapter / client profile 控制，不能在通用 Anthropic 协议层硬编码。
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩协商头、内部 tracing 头和会导致上游误判的本地网关头。
- 不生成 `OpenAI-Organization`、`OpenAI-Project`、`OpenAI-Beta` 或 GPT / Codex 专属 header。
- `request-id`、`anthropic-organization-id` 等响应头只作为上游响应元数据捕获，不作为请求头注入。

Base URL 规则：

- 官方默认地址保存为 `https://api.anthropic.com`，拼接上游路径时归一到 `/v1/messages` 等版本路径。
- 允许填写 `https://api.anthropic.com/v1`，保存或拼接时需要避免重复 `/v1/v1`。
- 禁止填写具体接口路径作为 base URL，例如 `/v1/messages`、`/v1/messages/count_tokens`。
- OAuth 授权、token exchange、token refresh 和 usage 查询使用各自官方端点，不跟 API Key `base_url` 共用字段。
- 继续使用现有 SSRF 防护、协议限制、用户密码禁止、查询参数禁止、片段禁止、路径规整等 base URL 校验。

## 返回、流式与响应语义

Anthropic 返回侧必须新增协议适配器，不能复用 OpenAI v1 的 `choices[].message`、`choices[].delta` 或 Responses 事件路径。

非流式 JSON 成功响应示例语义：

```json
{
  "id": "msg_...",
  "type": "message",
  "role": "assistant",
  "model": "claude-haiku-4-5",
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
- 工具结果由后续请求中的 `messages[].content[].type = tool_result` 表达；网关只负责透传和审计，不执行工具。
- Thinking 内容来自 `content[].type = thinking` 或流式 `thinking_delta`；是否写给下游取决于客户端协议和安全策略，但原始审计和 usage 统计必须保留足够事实。
- 完成状态来自 `stop_reason`，常见值包括 `end_turn`、`max_tokens`、`stop_sequence`、`tool_use`、`pause_turn`、`refusal`。
- 错误来自非 2xx JSON 或 200 SSE 中的 `event: error`。

这些语义帧只说明“上游响应表达了什么”，不直接决定账号状态。响应检查策略、账户错误处理策略和客户端最终渲染必须在后续层级处理。

流式 SSE：

- `stream: true` 时上游返回 Anthropic SSE。
- 关键事件包括 `message_start`、`content_block_start`、`content_block_delta`、`content_block_stop`、`message_delta`、`message_stop`、`ping`、`error`。
- `content_block_delta.delta.type` 可能是 `text_delta`、`input_json_delta`、`thinking_delta`、`signature_delta`。解析器必须允许未来新增事件和 delta 类型。
- `message_delta.usage` 是累计 token 计数，不能按每个 delta 累加；最终用量应取最后一个可用累计值。
- 流内 `event: error` 可能发生在 HTTP 200 之后，必须进入流式失败和账号副作用链路，而不是只按 HTTP 状态判断成功。
- SSE 解析必须按增量窗口处理完整事件，不能为了常规解析拼接完整大响应体。

流式提交边界：

- 只有在尚未向下游写出任何 SSE body 时，网关才可以隐藏当前上游失败并服务端切后续账号 / 后续分组。
- `ping`、`message_start`、`content_block_start` 等不算可见模型输出，但只要已经实际写给下游，就不能再静默替换成另一条上游流。
- `text_delta`、`input_json_delta`、`thinking_delta`、`signature_delta`、工具调用参数和任何会改变 assistant 内容 / 工具状态的事件都属于有状态输出；写出后失败只能走客户端可见失败或断流，不做服务端拼接。
- 未知事件和未知 delta 类型默认允许透传和有限审计；不能因为事件类型未知就直接判定账号异常。

下游渲染：

- Anthropic native 客户端默认原样返回 Anthropic JSON / SSE 事件。
- 后续如果要支持 OpenAI-compatible 客户端请求 Claude 模型，应新增显式的 OpenAI -> Anthropic 请求转换和 Anthropic -> OpenAI 响应渲染，不在本阶段把它藏进透传链路。
- 参考 `new-api` 的 Claude channel 和 `sub2api` 的 protocol converter 可以证明这类互转复杂度高，必须单独测试工具调用、图片、PDF、thinking、cache、流内错误和 usage，不能顺手实现。

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

- HTTP 状态码、`error.type`、错误文案和 `request_id` 只作为使用记录、审计、诊断摘要、请求级兼容恢复输入和账户错误处理策略输入。
- 代码不得内置 `authentication_error = 账号异常`、`rate_limit_error = 账号限流`、`overloaded_error = 临时不可调用` 这类固定映射。
- 上游非 2xx 仍按“显式兼容恢复 -> 同账号原地确认 -> 本地短期屏蔽 -> 切后续账号 / 后续分组 -> 事前确认 / 半开探测 -> 并发归零后才写持久状态”的统一流程处理。
- 账户错误处理策略可以匹配 Anthropic 的状态码、错误类型、错误码或文案，但命中结果只是待确认目标状态；真实网关流量不能绕过确认直接写 `temporary_unavailable`、`rate_limited` 或 `error`。
- Anthropic `event: error`、缺少 `message_stop`、上游 EOF 或流式中断按流式失败流水线处理；是否写持久账号状态仍由确认阶段决定。
- 最终返回给 Anthropic native 客户端的本地错误应优先使用 Anthropic error shape，而不是 OpenAI error shape；所有候选账号耗尽时返回本地统一错误语义，不透传最后一个上游错误体作为权威结论。
- `request-id` 响应头和 body 内 `request_id` 都应写入审计元数据，便于排障。
- 本地请求错误、模型路由失败、端点不支持、API Key 额度不足、分组无可承接账号等发生在命中上游账号之前的失败不写 Anthropic 账号状态。
- 不在代码里硬编码 Anthropic 错误文案作为限流、余额不足或账号异常判断；账户副作用仍通过可配置账户错误处理策略和统一上游失败流程落地。

## Usage、成本与统计

Anthropic usage 与 OpenAI usage 不同，必须单独维护语义。

需要解析和保存的字段：

| 字段 | 含义 | 统计要求 |
| --- | --- | --- |
| `input_tokens` | 非缓存输入 token | 计入输入 token |
| `output_tokens` | 输出 token 总数 | 计入输出 token |
| `cache_creation_input_tokens` | 创建 prompt cache 的输入 token | 单独计入缓存写入 token |
| `cache_read_input_tokens` | 从 prompt cache 读取的输入 token | 单独计入缓存读取 token |
| `cache_creation.ephemeral_5m_input_tokens` | 5 分钟 TTL 缓存写入 token | 后续价格估算需要 |
| `cache_creation.ephemeral_1h_input_tokens` | 1 小时 TTL 缓存写入 token | 后续价格估算需要 |
| `output_tokens_details.thinking_tokens` | thinking 相关输出 token | 单独计入 thinking 输出 token |
| `inference_geo` | 推理区域 | 仅作诊断和审计字段 |

统计口径：

- `input_tokens` 不包含缓存读取和缓存写入。计算上下文长度或长上下文价格档位时，应使用 `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。
- 缓存读写 token 不能并入普通输入 token 后丢失来源，否则 prompt cache 成本、命中率和授权消耗口径会失真。
- `thinking_tokens` 属于输出 token 的分解字段，`output_tokens` 仍是计费权威总数；统计展示可额外显示 thinking 占比。
- 使用记录应保存 `usage_semantic = anthropic` 或等价协议字段，避免后续 worker 用 OpenAI 口径解释 Anthropic usage。
- 业务统计、授权额度、趋势和 TopN 仍必须由 worker 增量写入预聚合表；不能在接口请求路径临时扫描 usage 明细做 Anthropic 汇总。

价格口径：

- 成本估算按 Anthropic 官方模型价格维护在 `providerCode = anthropic` 的模型价格目录下。
- Long context、prompt cache 读写、1h cache、thinking token、batch 折扣等价格差异必须有明确模型价格字段支撑；没有字段前只记录 token，不编造成本。
- Anthropic-compatible 国产入口的价格按对应供应商维护，不能套用 Anthropic 官方价格。

## 模型目录

Anthropic 模型目录必须单独维护在 `anthropic` 供应商下。

初始目录建议从官方 Models API / 模型文档同步，优先覆盖当前可调用 Claude 文本模型。当前设计只要求目录维护能跟随官方 Models API 实时返回，不把具体型号写成长期硬编码依赖；如果后续要展示某个默认测试型号，必须以官方 Models API 返回值和当时可用性为准。

模型目录字段要求：

- `providerCode = anthropic`
- `model` 使用 Anthropic 官方模型 ID，保留官方大小写和连字符写法；具体型号清单以官方 Models API 返回为准。
- `supportedApiProtocols` 第一阶段填 Anthropic Messages 对应能力。
- `contextWindowTokens`、`releaseDate`、`displayName` 以官方 Models API 或官方模型文档为准；不确定时留空，不为了排序编造。
- `ownedBy` 或等价展示字段应明确为 Anthropic，不跟第三方 Anthropic-compatible 模型混在一起。
- `GET /v1/models` 对 Anthropic native 客户端返回本地 Anthropic 模型目录，不请求上游；如后续增加“同步上游模型”能力，必须限制响应体大小并按当前性能底线分块读取。

国产 Anthropic-compatible 模型目录：

- DeepSeek Anthropic-compatible 使用 DeepSeek 模型目录和价格，见 [DeepSeek 账号接入](DeepSeek账号接入.md)。
- GLM Coding Anthropic-compatible 后续使用 GLM 模型目录和价格，见 [智谱 GLM 账号接入](智谱GLM账号接入.md)。
- Kimi / Moonshot 等如果接入，应新增对应供应商文档和模型目录，不进入 `anthropic` 官方目录。

## 分组、授权与 API Key 路由

官方 Anthropic 档案应创建独立默认分组：

- 默认 Anthropic 分组：绑定 `profile_anthropic_anthropic_v1`

规则：

- Anthropic 账户只能加入相同 `provider_protocol_profile_id` 的分组。
- Anthropic 官方账户不能加入 DeepSeek Anthropic-compatible、GLM Anthropic-compatible 或其他第三方兼容档案分组。
- 授权实例账户继承来源账户的官方 Anthropic 资源事实，但被授权侧实例状态、分组绑定、冷却和用量归属仍保持独立。
- 当前 API Key 多分组绑定仍以同一供应商协议档案为硬边界。模型路由落地后，跨供应商 API Key 必须先按请求 `model` 定位目标档案，再进入对应档案分组调度。
- Anthropic native 请求的会话连续性主要来自客户端请求内容和 prompt cache；第一阶段不新增跨请求会话亲和特殊规则。后续如果发现 Claude SDK / Claude Code 需要按会话稳定命中同一账号，再复用当前会话亲和机制扩展协议字段来源。

## 账号测试

Anthropic 账户测试必须复用真实网关链路。

测试请求：

```json
{
  "model": "claude-haiku-4-5",
  "max_tokens": 16,
  "messages": [
    { "role": "user", "content": "请回复 OK" }
  ]
}
```

测试要求：

- 测试路径使用 `/v1/messages`。
- 默认测试模型优先使用本地 Anthropic 目录中最低成本且可用的模型；如果目录不可用，则使用供应商 `default_test_model`。目录落库以官方 Models API 返回值为准。
- 测试请求必须带 `anthropic-version: 2023-06-01`。
- 测试成功后记录 `last_successful_test_model`。
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则。
- `pending_test` 账户测试失败继续保持待测试，测试成功才进入正常调度。
- 停用账户测试只保留诊断，不自动恢复停用状态。
- 测试返回的完整上游请求 URL、响应头、响应体、`request-id` 和原始错误只对所有者或管理员开放；授权用户只看脱敏摘要。

## 导入导出协议

账户导入协议新增 Anthropic 示例时，应使用项目自定义 JSON，不兼容 Anthropic Console 导出格式。

API Key 推荐写法：

```json
{
  "name": "Anthropic Claude 账号 1",
  "providerCode": "anthropic",
  "connectionType": "api_key",
  "type": "api_key",
  "status": "pending_test",
  "groupName": "默认 Anthropic 分组",
  "credentials": {
    "api_key": "sk-ant-...",
    "base_url": "https://api.anthropic.com",
    "anthropic_version": "2023-06-01",
    "supported_endpoint_modes": ["messages_json", "messages_sse"]
  }
}
```

OAuth 推荐写法：

```json
{
  "name": "Anthropic Claude OAuth 账号 1",
  "providerCode": "anthropic",
  "connectionType": "oauth",
  "type": "oauth",
  "status": "pending_test",
  "groupName": "默认 Anthropic 分组",
  "credentials": {
    "oauth_source": "authorization_code",
    "access_token": "sk-ant-oat-...",
    "refresh_token": "...",
    "expires_at": "2026-06-18T12:00:00.000Z",
    "scope": "user:inference user:sessions:claude_code",
    "org_uuid": "...",
    "anthropic_version": "2023-06-01",
    "supported_endpoint_modes": ["messages_json", "messages_sse"]
  }
}
```

`connectionType` 是导入协议层字段，用于和后续 WIF、云平台托管 Claude、第三方兼容入口区分。后端落库时仍以 `providerCode + connectionType` 解析 `provider_protocol_profile_id`，再保存 `accounts.type = api_key` 或 `accounts.type = oauth`。`setup_token` 不作为独立 `type` 导入，统一写 `type = oauth`、`credentials.oauth_source = setup_token`。

## 参考实现观察

本地参考项目给出的可复用经验：

- `new-api` 将 Claude / Anthropic 作为独立 channel，单独维护 DTO、请求头、`/v1/messages` URL、SSE 解析和 OpenAI chunk 转换。这说明 Anthropic 返回侧不能硬套 OpenAI `choices` 路径。
- `new-api` 的 DTO 同时覆盖 text、image、tool_use、tool_result、thinking、signature、cache_control 等 Anthropic block，说明本项目也应在协议适配器层保留 block 结构，不要过早压成纯文本。
- `sub2api` 对 Anthropic API Key direct、Claude OAuth / SetupToken、Vertex Anthropic、Bedrock Claude、OpenAI Responses 互转分别建请求构造和转换路径。这说明“官方直连 API Key”“OAuth 账号”“云平台托管”“协议兼容代理”应分层处理。
- `sub2api` 的 Anthropic API Key 使用 `x-api-key`，OAuth / SetupToken 使用 `Authorization: Bearer <access_token>`，并由 Claude token provider 做 access token 缓存和刷新；这一点应吸收为本项目的账号类型分支。
- `sub2api` 同时包含 Claude Code header / beta 模拟、5h 窗口、会话数量、RPM、TLS 指纹和上下文处理等重型 OAuth 专属能力。本项目第一阶段只落必要 OAuth 凭据、刷新和 native Messages 调用；这些高级能力后续按单独需求增量设计。
- `sub2api` 和 `new-api` 都有 OpenAI <-> Claude 深度互转代码，但这类能力涉及工具调用、图片、PDF、thinking、cache、usage 和流式状态机，不能作为本项目第一阶段默认范围。

不采用的参考实现范围：

- 不把 Claude Code OAuth 的全量伪装逻辑当成 Anthropic API Key 或通用 Anthropic 协议能力。
- 不对 API Key 账户默认追加 Claude Code CLI 专属 `anthropic-beta` 和 `User-Agent`。
- 不把第三方 Anthropic-compatible 入口合并到官方 `anthropic` 供应商。
- 不在第一阶段实现 OpenAI Chat / Responses 到 Anthropic Messages 的自动互转。

## 官方资料与开源生态

官方资料：

- Claude API overview：<https://platform.claude.com/docs/en/api/overview>
- Claude Code authentication：<https://code.claude.com/docs/en/authentication>
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

MCP 是工具 / 上下文协议，不是本项目的上游模型网关协议。后续如果要支持 Claude 的 `mcp_servers` 请求字段，应作为 Anthropic Messages 的请求体透传能力处理，不把 MCP 当成新的供应商协议。

## 实施清单

- 新增 `anthropic` 供应商种子。
- 新增 `protocolCode = anthropic`、`protocolVersion = v1` 和 `messages` / `models` / `message_token_counting` 端点族。
- 新增 `profile_anthropic_anthropic_v1`。
- 新增默认 Anthropic 分组。
- 前端账户创建在选择 `Anthropic Claude` 后展示 `Anthropic API Key` 和 `Anthropic OAuth`。
- 后端账户创建、编辑、导入和公开推送接口按 `providerCode=anthropic + connectionType=api_key/oauth` 解析官方直连档案。
- 新增 Anthropic OAuth token service，负责授权码 / setup token / 手动 token 的凭据保存、access token 缓存、刷新单飞和刷新错误收口。
- 网关本地认证支持 Anthropic native 客户端常用的 `x-api-key` 本地 API Key 入口。
- 新增 Anthropic native 请求适配器，负责路径拼接、请求头过滤、上游认证替换和 raw body 透传；API Key 写 `x-api-key`，OAuth 写 `Authorization: Bearer`。
- 新增 Anthropic native 返回适配器，负责 JSON / SSE 响应语义帧抽取、usage 解析、流内错误识别和本地错误渲染。
- 新增 Anthropic 兼容策略占位，但第一阶段不内置任何按状态码、错误类型或错误文案触发的请求恢复规则。
- Anthropic 上游失败复用现有同账号确认、本地账号短期屏蔽、半开探测、事前确认、冷却复测和账户错误处理策略，不新增协议专属账号状态机。
- Anthropic usage 扩展缓存读取、缓存写入、1h cache、thinking tokens 和 `usage_semantic`。
- Anthropic 模型目录和价格目录单独维护，成本估算按 `providerCode=anthropic` 查找。
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗、模型价格和测试说明同步更新。

## 验证要求

第一阶段代码落地后至少覆盖：

- 创建 Anthropic API Key 账户，保存后落到 `profile_anthropic_anthropic_v1`。
- 创建 Anthropic OAuth 账户，保存后落到 `profile_anthropic_anthropic_v1`，`accounts.type = oauth`，凭据加密保存。
- OAuth 授权码 / setup token / 手动 token 来源都能归并为 OAuth 账户，并能正确记录 `credentials.oauth_source`。
- 新账户默认 `supported_endpoint_modes = messages_json/messages_sse`，不参与 OpenAI `/v1/chat/completions` 或 `/v1/responses` 调度。
- Anthropic 账户只能绑定 Anthropic 档案分组，不能加入 DeepSeek / GLM Anthropic-compatible 分组。
- 本地 `POST /v1/messages` 使用 `x-api-key` 或 `Authorization` 都能通过本地 API Key 认证。
- 上游请求命中 `https://api.anthropic.com/v1/messages`，上游认证头为账户 `x-api-key`，不会泄漏本地 API Key。
- OAuth 账户上游请求命中 `https://api.anthropic.com/v1/messages`，上游认证头为 `Authorization: Bearer <access_token>`，不会泄漏本地 API Key 或 refresh token。
- OAuth access token 即将过期时能请求前刷新，刷新并发通过单飞 / 锁合并；刷新失败不直接写账号持久状态。
- 客户端未传 `anthropic-version` 时使用账户默认 `2023-06-01`。
- API Key 与 OAuth 的 `anthropic-beta` 合并逻辑互不污染；OAuth 必需 beta 不会出现在 API Key 上游请求中。
- 非流式响应能解析 `content[].text`、`stop_reason` 和 usage。
- 流式响应能按 `message_start -> content_block_* -> message_delta -> message_stop` 增量转发，并正确处理 `ping` 和未知事件。
- 流内 `event: error` 能进入统一上游失败链路。
- `authentication_error`、`rate_limit_error`、`overloaded_error` 等 Anthropic 错误类型只进入审计和策略匹配输入，不能被代码直接写成异常、限流或临时不可调用。
- 账户错误处理策略命中 Anthropic 错误类型后，只形成待确认目标；确认失败且当前账号并发归零后才允许写持久状态。
- 尚未写出任何 SSE body 的流式失败可以服务端切号；已经写出任意 Anthropic SSE body 后不做静默拼接。
- 模型不匹配、端点不支持、本地认证失败、额度不足和分组无可承接账号不写 Anthropic 账号状态。
- `GET /v1/models` 对 Anthropic native 请求返回本地 Anthropic 模型目录，不请求上游。
- Prompt cache 和 thinking usage 字段能写入使用记录；统计 worker 不在请求链路临时聚合。
- DeepSeek / GLM / Kimi 等 Anthropic-compatible 入口不会显示为官方 Anthropic 直连。
