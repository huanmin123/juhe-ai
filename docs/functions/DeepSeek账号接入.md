# DeepSeek 账号接入

## 范围

本文记录 DeepSeek 供应商的接入方案、账户创建类型、协议档案、网关透传边界、模型目录、验证结果和后续实现注意事项。当前代码已落地 DeepSeek OpenAI-compatible 独立供应商，支持 Chat Completions JSON / SSE，并为 Codex 客户端提供显式的 Responses -> Chat SSE 桥接；同时新增 DeepSeek Anthropic v1 Messages 档案用于 Claude Code / Anthropic 客户端画像直连。真实上游验证结果见本文“验证记录”。

DeepSeek 对外 hosted API 提供 OpenAI-compatible surface 和 Anthropic-compatible surface。OpenAI-compatible 默认地址为 `https://api.deepseek.com`，部分 beta 能力走 `https://api.deepseek.com/beta`；Anthropic v1 Messages 默认地址为 `https://api.deepseek.com/anthropic`，由本地 Anthropic v1 URL helper 拼接为 `/anthropic/v1/messages` 或 `/anthropic/v1/models`。第三方 NewAPI 代理可能直接把 Anthropic surface 挂在站点根路径，例如 `https://vsllm.com/v1/messages`，因此代理场景的 `base_url` 应按实际代理根地址填写，不由代码硬猜。

DeepSeek hosted API 没有需要本项目优先接入的独立 native 网关协议；当前更准确的理解是“OpenAI-compatible Chat Completions + DeepSeek 供应商扩展字段”。Codex 兼容由本地桥接完成，不把 DeepSeek 声明成原生 OpenAI Responses 供应商。

结论：

- 一个供应商：`deepseek`
- 两个当前协议档案：`profile_deepseek_openai_v1`、`profile_deepseek_anthropic_v1`
- 两个当前账户创建类型：DeepSeek OpenAI-compatible API Key、DeepSeek Claude Code API Key
- 底层账户类型仍为 `api_key`
- OpenAI-compatible 是默认接入类型，默认只声明 Chat Completions JSON / SSE 能力
- Codex 客户端兼容通过 `clientCompatibility = codex_responses` 显式启用，账户底层 endpoint modes 仍保存真实上游能力 `chat_json`、`chat_sse`
- 当前不声明 DeepSeek 原生 OpenAI Responses、DeepSeek native 协议、`message_token_counting`、MCP 或代码执行能力；Anthropic v1 档案默认只声明 `messages_json`、`messages_sse`

## 落地状态

截至 `2026-06-20`：

- 后端已新增 `deepseek` provider、`profile_deepseek_openai_v1`、默认 DeepSeek 分组、DeepSeek provider driver、credential driver、模型价格目录和 usage / response 语义解析。
- 前端已新增 DeepSeek 供应商展示、账户表单能力、导入协议示例、模型目录分类和模型检测入口隔离。
- 已落地部分支持 OpenAI-compatible Chat Completions JSON / SSE；当前不进入 Responses 模型检测。
- 已落地 DeepSeek Codex Responses -> Chat SSE 桥接，覆盖 Codex `/v1/responses` 入站、上游 `/v1/chat/completions` 改写、function tools、`input_image` data URL、`reasoning_content`、截断失败和 SSE error 事件。
- DeepSeek Anthropic v1 已新增 `profile_deepseek_anthropic_v1`、默认 DeepSeek Claude Code 分组、前端接入类型、凭据归一化和 Anthropic 协议 driver 分支；默认只启用 Messages JSON / SSE，不启用 Count Tokens。
- 本地 mock AI 回归已覆盖 Chat JSON、Chat SSE、模型映射、非 Codex `/responses` 拒绝、Codex bridge、cache usage、`reasoning_content`、`insufficient_system_resource` 和上游 `Content-Length`。
- 真实 `https://vsllm.com` 验证中，`deepseek-ai-v4-flash` JSON / SSE 通过；`deepseek-ai-v4-pro` JSON 返回上游 429 当日配额超限，SSE 返回上游连接超时，待更换 Key、额度恢复或上游稳定后补测。

## 供应商与协议档案

已新增供应商：

```ts
type ProviderCode = 'deepseek'
```

显示名称为 `DeepSeek`。`deepseek` 是独立供应商，不能复用 `openai`、`gpt` 或其账号池、分组和价格目录。`providerCode=openai` 仍表示通用 OpenAI-compatible 聚合目录，但 DeepSeek 不进入该聚合目录；DeepSeek 自身账号池、分组、模型价格和测试策略都归属于 `deepseek`。

当前协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_deepseek_openai_v1` | `deepseek` | `openai/v1` | `https://api.deepseek.com` | DeepSeek OpenAI-compatible API Key | `chat_json`、`chat_sse` |
| `profile_deepseek_anthropic_v1` | `deepseek` | `anthropic/v1` | `https://api.deepseek.com/anthropic` | DeepSeek Claude Code API Key | `messages_json`、`messages_sse` |

OpenAI-compatible 档案当前只声明 Chat Completions 语义，不把 Responses 当成默认能力。Codex bridge 是该档案下的客户端兼容适配：只在账号显式配置 `clientCompatibility = codex_responses` 且请求被识别为 Codex Responses SSE 时生效，底层仍要求 `chat_sse`。`/beta` 下的 Chat Prefix Completion 和 FIM Completion 作为 DeepSeek 供应商 beta 能力记录，不扩展成通用 OpenAI Responses 档案。

DeepSeek Anthropic-compatible 必须保留 `providerCode=deepseek`、独立 Base URL、独立默认分组、独立模型价格和独立账户测试策略，不能复用官方 `anthropic` 供应商账号池。`message_token_counting` 在真实验证前不进入默认 endpoint modes。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

选择 `DeepSeek` 后展示 OpenAI-compatible API Key 和 Claude Code API Key 两个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 | 默认客户端兼容 |
| --- | --- | --- | --- | --- | --- |
| DeepSeek OpenAI-compatible API Key | `api_key` | `profile_deepseek_openai_v1` | `api_key`、`base_url` | `deepseek-v4-flash` | `openai_standard` |
| DeepSeek Claude Code API Key | `api_key` | `profile_deepseek_anthropic_v1` | `api_key`、`base_url` | `deepseek-v4-flash` | Anthropic API / Claude Code |

这里的“接入类型”是产品表单概念，不是新增 OAuth 类型。后端保存时仍加密保存 `credentials.api_key`，并根据接入类型写入对应 `provider_protocol_profile_id`。

保存规则：

- 新建 DeepSeek 账户默认写入 `pending_test`，测试通过后才允许正常调度
- `profile_deepseek_openai_v1` 的 `base_url` 默认填充 `https://api.deepseek.com`，允许用户修改为同协议的代理地址或专属部署地址，但必须继续通过 SSRF 防护和 OpenAI-compatible base URL 校验
- `profile_deepseek_openai_v1` 的 `credentials.supported_endpoint_modes` 省略时默认启用 `chat_json`、`chat_sse`
- `profile_deepseek_anthropic_v1` 的 `base_url` 默认填充 `https://api.deepseek.com/anthropic`，允许用户修改为同协议的代理地址或专属部署地址；使用 NewAPI 类代理时可以填写代理根地址，例如 `https://vsllm.com`
- `profile_deepseek_anthropic_v1` 的 `credentials.supported_endpoint_modes` 省略时默认启用 `messages_json`、`messages_sse`，不启用 `message_token_counting`
- DeepSeek 账户不显示 GPT OAuth 字段，不显示 Refresh Token、Access Token；OpenAI-compatible 档案可选择 `openai_standard` 或 `codex_responses` 客户端兼容
- `client_compatibility` 不能单独决定上游协议；它只能在已确定的协议档案下描述普通 OpenAI SDK、普通 Anthropic SDK、Claude Code 等客户端画像
- 同一个 DeepSeek 上游 Key 如需同时承担普通 OpenAI Chat 和 Codex bridge，可创建两个本地账户记录并分别绑定不同分组 / 客户端兼容配置，避免统计、恢复和 endpoint modes 混杂

## 网关请求边界

DeepSeek OpenAI v1 档案优先复用现有 OpenAI v1 Chat Completions 协议适配器，但必须有独立供应商策略。

请求路径：

- 客户端可请求 `/chat/completions` 或 `/v1/chat/completions`
- beta 能力按 `https://api.deepseek.com/beta` 单独拼接，只能由 DeepSeek beta endpoint mode 或账户能力显式启用，不能靠客户端路径自动猜测
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求 DeepSeek 上游模型列表
- 普通 OpenAI SDK / Responses 请求不由 DeepSeek 承接；只有显式 `clientCompatibility = codex_responses` 的 DeepSeek 账户，在请求被识别为 Codex Responses SSE 时，才接收 `/responses` 或 `/v1/responses` 并改写到上游 `/chat/completions`
- Codex bridge 必须使用流式 Responses 入站和上游 Chat SSE；账号 endpoint modes 仍保存 `chat_json`、`chat_sse`，不能为此写入 `responses_json` 或 `responses_sse`
- DeepSeek 官方 List Models 接口可用于模型目录人工校验或后续后台刷新，不进入网关热路径
- DeepSeek 官方 Balance 接口只作为后续人工诊断或账户页辅助信息候选，第一阶段不做余额轮询、不把余额快照接入额度判断，也不在请求链路调用

请求头：

- 上游统一写入 `Authorization: Bearer <DeepSeek API Key>`
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩相关头、SDK / tracing / 部署平台噪声头
- 不从账号配置生成 `OpenAI-Organization`、`OpenAI-Project` 或 GPT / Codex 专属 header
- 对 Buffer / string 请求体统一补准确 `Content-Length`；真实 `vsllm.com` 验证显示 chunked JSON 请求体可能导致非流式 Chat 返回 HTTP 200 但 `choices:null`

## DeepSeek Anthropic-compatible 边界

DeepSeek Anthropic-compatible 档案面向“客户端发送 Anthropic Messages，本地转发到 DeepSeek Anthropic-compatible 上游”的场景。它应复用项目已有 `anthropic/v1` 协议适配器和 usage 解析语义，但供应商驱动、Base URL、模型映射、价格和账户池必须归属 `deepseek`。

请求路径：

- 客户端可请求 `/messages` 或 `/v1/messages`
- 上游 URL 基于账户 `base_url` 拼接，默认是 `https://api.deepseek.com/anthropic`，最终请求应命中 `/anthropic/v1/messages`
- URL helper 必须同时处理 `https://api.deepseek.com/anthropic` 和 `https://api.deepseek.com/anthropic/v1` 两种输入，避免漏拼 `/v1` 或重复拼 `/v1/v1`
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求 DeepSeek 上游模型列表
- `/v1/messages/count_tokens` 不作为默认能力；只有真实验证 DeepSeek Anthropic-compatible 支持并补齐 endpoint mode 后才能开放

请求头：

- 上游统一写入 `x-api-key: <DeepSeek API Key>`
- `anthropic-version` 和 `anthropic-beta` 在 DeepSeek 官方兼容面中会被忽略，因此不作为 DeepSeek 账户配置字段，也不能作为能力开关依据
- Claude Code、Anthropic SDK 或其他客户端带来的版本 / beta header 可以进入审计摘要，但 DeepSeek 调度不能依赖这些 header 判断真实能力

模型映射：

- DeepSeek 模型 ID，例如 `deepseek-v4-flash`、`deepseek-v4-pro`，应作为上游真实模型和计价模型保留
- Claude 模型别名需要本地显式映射，不能完全依赖 DeepSeek 上游自动 fallback：
  - `claude-opus*` -> `deepseek-v4-pro`
  - `claude-haiku*`、`claude-sonnet*` -> `deepseek-v4-flash`
- DeepSeek 官方说明未知 Anthropic 模型名会自动映射到 `deepseek-v4-flash`；本项目不应静默接受任意未知 Claude 模型，否则使用记录、价格和用户预期会被上游 fallback 掩盖
- 如果请求已经由分组或客户端入口明确命中 `profile_deepseek_anthropic_v1`，可以把未知 `claude-*` 按显式配置 fallback 到 `deepseek-v4-flash`，但必须记录 `model_mapping_source = deepseek_anthropic_fallback`；全局模型路由不能仅凭未知 Claude 名称自动选择 DeepSeek
- 使用记录必须保留下游模型、上游模型、计价模型、模型映射来源和目标 `provider_protocol_profile_id`

内容与能力限制：

- 默认只声明 `messages_json`、`messages_sse`
- 文本消息、`system`、`temperature`、`top_p`、`max_tokens`、`stop_sequences`、`stream` 等基础 Messages 能力可按 Anthropic 协议层处理
- `tools`、`tool_choice`、`tool_use`、`tool_result` 可以作为协议字段透传候选，但不能默认标记为已验证能力；`2026-06-20` 真实验证中，`https://vsllm.com` + `deepseek-ai-v4-flash` 的 OpenAI tool calls 返回 `choices:null`，Anthropic tool use 返回空 content，需要按上游和模型单独验证后再开放为稳定能力
- `image`、`document`、MCP、code execution、container upload、部分 search / server tool 扩展和 content block cache control 不作为默认支持能力
- DeepSeek 兼容面中 `container`、`mcp_servers`、`service_tier`、`top_k`、`anthropic-beta`、`anthropic-version` 等字段会被忽略，文档和 UI 不能宣称这些字段对 DeepSeek 生效
- `thinking` 可以透传，但 `budget_tokens` 会被忽略；`output_config` 只按官方确认的 `effort` 范围处理

统计、错误与恢复：

- usage 结构按 `anthropic/v1` 协议层解析，成本按 `providerCode=deepseek` 和 DeepSeek 计价模型计算
- DeepSeek Anthropic-compatible 请求失败仍进入统一网关错误流水线、账号切换、运行态屏障、半开恢复和后台复测，不新增一套 Anthropic 专属状态
- 官方 Anthropic 供应商的 `profile_anthropic_anthropic_v1` 不接收 DeepSeek Anthropic-compatible 账户；DeepSeek 账户也不能进入官方 Anthropic 默认分组
- Claude Code 只作为下游客户端画像或兼容 profile，不能让系统把 DeepSeek 账户伪装成官方 Anthropic 账户
- Claude Desktop / Claude Code 类客户端需要单独覆盖模型列表、Messages JSON / SSE、工具调用、thinking、`anthropic-beta` / `anthropic-version` header 和错误事件语义；未通过真实或 mock 客户端测试前，只能标记为目标兼容，不标记为已验证

## DeepSeek 特殊参数与官方限制

DeepSeek 大部分 Chat Completions 参数沿用 OpenAI-compatible 形态，例如 `messages`、`model`、`stream`、`temperature`、`top_p`、`max_tokens`、`stop`、`response_format`、`tools`、`tool_choice`、`logprobs` 和 `top_logprobs`。以下参数和返回字段需要作为 DeepSeek 供应商扩展处理，不能写成全局 OpenAI v1 固定语义。

### 用户标识 user_id

DeepSeek Chat Completions 支持 `user_id` 字段，用于帮助上游监控和滥用检测。

本项目处理策略：

- 允许客户端显式传入 `user_id` 并按 raw passthrough 透传
- 第一阶段不由网关默认生成 `user_id`
- 后续如需生成，应只在 DeepSeek 供应商策略中实现，并使用不可逆、不可关联到真实本地 API Key / 账号 ID 的派生值
- 不把本地 API Key、系统账户 ID、AI 账户 ID、用户邮箱或真实来源 IP 直接写入 `user_id`
- `user_id` 只用于上游滥用检测辅助，不参与本地会话亲和、统计归属、账号切换或额度判断

### 采样参数与废弃字段

DeepSeek 官方文档中 `frequency_penalty` 和 `presence_penalty` 已标记为 deprecated；thinking mode 开启时，`temperature`、`top_p`、`frequency_penalty` 和 `presence_penalty` 不生效。

本项目处理策略：

- 不主动注入 `frequency_penalty`、`presence_penalty` 或采样参数
- 客户端显式提交时可以透传，避免破坏 OpenAI-compatible SDK 请求
- 在账户测试、模型检测和默认请求样例中不使用已废弃 penalty 字段
- thinking mode 下不要把采样参数变化误判为模型、账号或网关未生效

### Thinking Mode

DeepSeek 支持思考模式开关和推理强度控制：

- `thinking`: 形如 `{ "type": "enabled" }` 或 `{ "type": "disabled" }`；官方 OpenAI SDK 示例通过 `extra_body` 传入
- `reasoning_effort`: `high` 或 `max`
- `low`、`medium` 会按官方兼容规则映射为 `high`，`xhigh` 会映射为 `max`
- thinking mode 默认开启；常规请求默认 effort 为 `high`，复杂 agent 请求官方可能自动提升为 `max`

本项目处理策略：

- 首轮透传客户端显式传入的 `thinking` 和 `reasoning_effort`
- 不在通用 OpenAI v1 层默认注入这些字段
- 如后续做账户或模型级默认值，只能落在 DeepSeek 供应商策略，不影响 GPT、OpenAI、GLM 或其他 OpenAI-compatible 供应商
- thinking mode 下采样字段可能无效，账户测试和模型检测不能用“温度变化是否改变输出”作为 DeepSeek 健康判断

### Reasoning Content

DeepSeek 返回中可能包含 `reasoning_content`：

- 非流式：读取 `choices[].message.reasoning_content`
- 流式：读取 `choices[].delta.reasoning_content`
- 多轮请求可以把上一轮 assistant message 的 `reasoning_content` 连同 `content`、`tool_calls` 一起传回上游

本项目处理策略：

- 可见文本仍以 `content` 为主
- `reasoning_content` 可进入诊断、审计摘要或扩展字段，但不要拼入普通 `content`
- 统计和调试可以保留 reasoning token / reasoning text 边界，但不得把它推导成所有 OpenAI-compatible 模型都具备的字段
- 原始审计保留时必须沿用现有容量、截断、敏感字段保护和保留期策略，默认列表页不展示完整 reasoning 内容

### JSON Output 与 Tool Calls

DeepSeek 支持 OpenAI-compatible 的 JSON Output 和 Tool Calls：

- JSON Output 使用 `response_format: { "type": "json_object" }`
- Tool Calls 使用 `tools` 和 `tool_choice`
- Strict Mode 属于 beta 能力，需要 beta base URL；当前只应按官方支持范围用于 `deepseek-v4` 系列

本项目处理策略：

- 这些能力可以按 Chat Completions 透传
- 如果上游返回工具调用，仍按 OpenAI v1 `choices[].message.tool_calls` / `choices[].delta.tool_calls` 解析
- 不把 DeepSeek 声明为原生 GPT / OpenAI Responses 工具协议；Codex bridge 只做 function tool call 的 Responses -> Chat / Chat -> Responses 结构转换
- Strict Tool Calls 不作为默认账户能力；如果后续支持，必须作为 DeepSeek beta 子能力并配套 beta base URL 校验

### Chat Prefix Completion Beta

DeepSeek 的 Chat Prefix Completion 是 beta 能力：

- 需要使用 `https://api.deepseek.com/beta`
- 最后一条 message 必须是 `assistant`
- 最后一条 assistant message 需要带 `prefix: true`

本项目处理策略：

- 不作为默认 `chat_json` / `chat_sse` 能力自动开启
- 如后续支持，应作为 DeepSeek beta 子能力或账户能力开关
- `prefix` 字段只在 DeepSeek beta Chat Completions 透传，不进入通用 OpenAI v1 请求体白名单

### FIM Completion Beta

DeepSeek 的 FIM Completion 使用 Completions 形态，是 beta 能力：

- 需要使用 `https://api.deepseek.com/beta`
- 走 `/completions`
- 请求字段包含 `prompt`、可选 `suffix` 和 `max_tokens`
- 官方说明 FIM max tokens 为 `4K`

本项目处理策略：

- 第一阶段不把 `/completions` / FIM 纳入默认账号测试和默认调度能力
- 如果后续要支持代码补全客户端，应新增 DeepSeek beta endpoint mode，而不是把它当成 Chat Completions

### Context Caching Usage

DeepSeek Context Caching 默认启用，调用方不需要额外请求参数。响应 `usage` 中可能包含：

- `prompt_cache_hit_tokens`
- `prompt_cache_miss_tokens`

本项目处理策略：

- usage 解析时保留这两个字段，便于后续成本、缓存命中率和排障统计
- 成本估算仍以 DeepSeek 官方价格口径和本地模型目录为准
- 不把 DeepSeek 的缓存字段写成所有供应商通用 usage 字段
- 缓存命中 token 已经包含在输入 token 语义中时，统计总 token 不能重复相加；成本拆分要按 DeepSeek 官方价格页的 cache hit / miss 口径计算

### Finish Reason

DeepSeek Chat Completions 的 `finish_reason` 除常见值外，还可能出现 `insufficient_system_resource`。该值应进入响应语义检查和上游错误诊断，不要被误判为本地网关错误。

本项目处理策略：

- `insufficient_system_resource` 是上游成功响应内的异常完成语义，不是 HTTP 非 `2xx`
- 如果没有产生可接受输出，可按响应语义检查策略触发可行的服务端重试或诊断记录
- 如果已经写出可见输出，仍遵守现有流式失败边界，不能静默拼接第二个上游回答
- 不因为单次该 finish reason 直接把账号写成持久异常；账号副作用仍走运行态屏障和确认流程

### Keep-alive 与超时

DeepSeek 官方文档说明，长请求期间流式响应可能每 2 秒发送一次 SSE comment 作为保活，非流式响应也可能每 2 秒返回空行保活；请求在服务端最长可能持续约 10 分钟。

本项目处理策略：

- SSE 解析器必须忽略 comment / keep-alive 事件，不能把它当成业务 delta、错误事件或缺少终止事件
- 非流式 JSON 聚合必须容忍响应体中的空行保活，最终仍按有界 JSON 解析
- 上游首包等待、空闲超时和总耗时诊断要能解释 DeepSeek 长请求，不要把正常保活误判为无响应
- 仍要遵守本地请求体、响应体捕获、审计和下游背压上限，不能因为 10 分钟上游窗口无限缓存响应

## 返回与 usage

DeepSeek OpenAI-compatible 返回应优先按 OpenAI v1 Chat 语义解析：

- JSON 响应读取 `choices[].message.content`、`choices[].message.tool_calls`、`choices[].message.reasoning_content`、`finish_reason` 和 `usage`
- 流式响应读取 `choices[].delta.content`、`choices[].delta.tool_calls`、`choices[].delta.reasoning_content` 和最后 usage chunk
- `usage.prompt_cache_hit_tokens`、`usage.prompt_cache_miss_tokens` 作为 DeepSeek 扩展 usage 字段保留
- 流式 keep-alive comment 和非流式空行只作为传输保活处理，不写入可见输出、usage 或错误摘要

成本与统计：

- DeepSeek 模型成本按本地模型目录和官方价格口径估算
- DeepSeek hosted API 是 API-key-only，不存在 OAuth 专属额度进度
- DeepSeek Balance 接口不等价于本地 API Key / 统一授权额度；第一阶段不接入自动额度快照
- 如果后续把 DeepSeek 自托管权重接入为本地 OpenAI-compatible 上游，应作为独立自托管供应商或自定义供应商处理，不和 DeepSeek 云 API 混淆

## 模型目录

DeepSeek 模型目录必须单独维护在 `deepseek` 供应商下，不要混进 GPT 价格文件。

当前目录，列表顺序同时作为账户测试和模型下拉框的默认优先顺序；同代模型先展示 DeepSeek 官方模型 ID，再展示第三方上游别名，最后展示退役日前可用的历史兼容名：

- `deepseek-v4-flash`
- `deepseek-v4-pro`
- `deepseek-ai-v4-flash`：第三方上游别名，映射到 DeepSeek V4 Flash 价格口径
- `deepseek-ai-v4-pro`：第三方上游别名，映射到 DeepSeek V4 Pro 价格口径
- `deepseek-chat`：官方历史兼容名，退役日前可见，计价沿用官方 legacy 价格口径
- `deepseek-reasoner`：官方历史兼容名，退役日前可见，计价沿用官方 legacy 价格口径

截至 `2026-06-22`，官方 V4 价格和窗口按 DeepSeek Pricing 文档记录如下：

| 模型 | 上下文窗口 | 最大输出 | Cache hit 输入 / 1M tokens | Cache miss / 常规输入 / 1M tokens | 输出 / 1M tokens |
| --- | --- | --- | --- | --- | --- |
| `deepseek-v4-flash` / `deepseek-ai-v4-flash` | 1,000,000 | 384,000 | USD 0.0028 | USD 0.14 | USD 0.28 |
| `deepseek-v4-pro` / `deepseek-ai-v4-pro` | 1,000,000 | 384,000 | USD 0.003625 | USD 0.435 | USD 0.87 |

旧别名 `deepseek-chat` 和 `deepseek-reasoner` 已被官方标记为将于 `2026-07-24 15:59 UTC` 退役。当前在目录中可以作为历史兼容别名保留，但不应作为新建账户或新默认测试模型的首选。

模型目录字段要求：

- `providerCode = deepseek`
- `model` 使用 DeepSeek 官方模型 ID，小写和连字符按官方写法保留
- `supportedApiProtocols` 至少覆盖 OpenAI-compatible 档案对应的 `chat_completions`；新增 Anthropic-compatible 后应补 `anthropic_messages` 或等价本地协议标识，但仍归属 `providerCode=deepseek`
- `contextWindowTokens` 和 `releaseDate` 以官方模型页或官方文档为准；同一发布日期的模型使用内置目录顺序字段保持下拉框稳定
- 价格只采信 DeepSeek 官方文档或官方 API 文档；第三方价格库和社区表格只能作为线索

DeepSeek 开源模型，例如 `DeepSeek-R1`、`DeepSeek-V3` 以及相关公开仓库和权重，可以作为自托管、vLLM、SGLang 或 Transformers 接入参考，但不等于 DeepSeek 云 API 的模型 ID、上下文窗口或价格。自托管 DeepSeek 如果通过 OpenAI-compatible vLLM / SGLang 暴露，应作为自定义 OpenAI-compatible 上游或独立自托管供应商处理。

## 与现有系统能力的对齐清单

### 供应商驱动

- DeepSeek 必须作为独立供应商接入 provider driver registry，不能复用 GPT driver 或通用 `openai` 聚合供应商的账号创建类型
- `providerCode=openai` 仍只表达通用 OpenAI-compatible 聚合目录；`providerCode=deepseek` 表达 DeepSeek 自身账号、模型、价格、测试和展示，DeepSeek 不进入 `openai` 聚合模型目录
- DeepSeek driver 负责按 `profile_deepseek_openai_v1` 选择默认 base URL、默认测试模型、默认 endpoint modes、Codex bridge、特殊字段说明、导入导出 round-trip、模型检测入口隔离和供应商局部响应语义
- DeepSeek Anthropic-compatible 复用 `anthropic/v1` 协议适配器的 URL、请求体和响应语义，但不能复用官方 Anthropic 供应商账号池；DeepSeek 的 Base URL、模型映射、headers、账户测试和价格归属都归属 `deepseek`
- PLAN-0050 的供应商驱动化重构尚未完全关闭时，DeepSeek 实现必须沿用新 driver 边界，不把新逻辑硬写回 GPT / OpenAI-compatible 旧分支

### 模型路由与模型映射

- DeepSeek 内置模型 ID 必须在全系统维度唯一，避免后续跨供应商 API Key 用 `model` 路由时发生歧义
- `deepseek-v4-flash` 和 `deepseek-v4-pro` 作为新建账户和账户测试优先模型；不要默认使用 `deepseek-chat`、`deepseek-reasoner`
- DeepSeek Anthropic-compatible 的 Claude 模型名必须在本地显式映射到 DeepSeek V4 模型，避免上游未知模型自动 fallback 掩盖路由和计价错误
- 账号模型映射仍按一跳精确匹配执行：下游模型用于路由和候选过滤，选中 DeepSeek 账号后才改写上游 `model`
- 使用记录必须同时保留下游模型、上游模型、计价模型、是否命中映射和目标 `provider_protocol_profile_id`
- 成本估算优先按 DeepSeek 上游实际模型或其 `pricingModel` 计算，不能按 GPT 同名或相似模型价格兜底

### 账户、分组与 API Key 路由

- DeepSeek 账户只允许加入自身 `provider_protocol_profile_id` 对应分组；OpenAI-compatible 账户进入 `profile_deepseek_openai_v1` 分组，Claude Code 账户进入 `profile_deepseek_anthropic_v1` 分组
- 默认 DeepSeek 分组承接 DeepSeek Chat Completions 请求；当账号显式 `clientCompatibility = codex_responses` 且请求为 Codex Responses SSE 时，可通过本地 bridge 承接 Codex `/responses`
- DeepSeek OpenAI-compatible 分组不承接 Anthropic Messages、普通 OpenAI Responses、FIM 或 Chat Prefix beta；DeepSeek Claude Code 分组只承接 Anthropic v1 Messages / Models，不承接 OpenAI Chat 或 Count Tokens
- 一个 API Key 可以同时绑定 DeepSeek 与其他供应商分组；每次请求优先由模型目录、协议档案和客户端兼容定位到目标 DeepSeek 账号池，再只在该 Key 已绑定的目标档案分组内调度
- 会话亲和、IP 级账号回避、本地账号屏蔽、上游桶避让和高并发队列继续使用现有运行态，不为 DeepSeek 另起一套调度状态
- 如果后续 DeepSeek API Key 账户支持同一账户内多个上游 Key，应复用账户内 Key 故障隔离能力：只摘除当前失败 Key，不因单个 Key 的认证失败、余额不足或限流直接打坏整个账户；第一阶段如果只开放单 Key 表单，则不展示 Key 池配置
- 账户测试、批量测试、模型检测、后台恢复探活和真实网关请求都必须走账号绑定代理；不能只让真实请求走代理、测试请求直连

### 错误处理、切号与恢复

- HTTP 非 `2xx`、连接失败、超时、EOF 和流式中断全部进入统一网关错误流水线
- DeepSeek 错误码、错误类型、错误文案和 `finish_reason` 只作为诊断、响应语义检查和账户错误处理策略输入，不写死余额不足、限流或账号坏等分支
- `insufficient_system_resource` 可作为供应商层响应语义条件观察或重试，但持久账号状态仍必须经运行态屏障、半开探测或事前确认
- Keep-alive comment / 空行不能触发流式缺终止事件错误，也不能被记录成可见输出
- DeepSeek OpenAI-compatible 暂不需要请求级兼容恢复策略；Codex bridge 只由 Codex 客户端画像触发，不能用恢复策略把普通 OpenAI Responses 请求改写到 DeepSeek

### 统计、价格与审计

- `prompt_cache_hit_tokens` 和 `prompt_cache_miss_tokens` 需要进入 DeepSeek usage 扩展解析和成本拆分，统计总 token 不能重复叠加缓存命中 token
- DeepSeek OpenAI-compatible usage 解析按 OpenAI Chat Completions 语义并补充 cache hit / miss、reasoning 等供应商扩展；DeepSeek Anthropic-compatible usage 解析按 Anthropic v1 语义，成本仍按 DeepSeek 模型价格且统计表必须区分不同 `provider_protocol_profile_id`
- 成本和统计仍由 `usage_records` 明细和后台预聚合表驱动，API 路由和 repository 热路径不能为 DeepSeek 扫描明细表实时汇总
- `reasoning_content`、工具调用参数和原始错误体都可能包含敏感内容，只能按现有审计容量、截断、加密 / 保留策略处理
- DeepSeek Balance 接口不参与本地额度扣减；本地 API Key / 统一授权额度仍按使用记录成本消耗

### 账户测试、模型检测与前端

- OpenAI-compatible 账户测试使用真实网关链路和 `/v1/chat/completions`，默认模型 `deepseek-v4-flash`
- `clientCompatibility = codex_responses` 的 DeepSeek 账户测试从 `/v1/responses` 入口进入本地 bridge，并由 bridge 改写到上游 `/v1/chat/completions`
- 当前已落地的 Responses 模型检测不选择 DeepSeek；后续如果新增 DeepSeek 模型检测，应按协议档案分别使用 Chat Completions 或 Messages，不启用 beta prefix、FIM、Responses、count_tokens 或多模态能力
- 前端供应商、账户类型、分组、模型目录、账号测试、导入预览、公开推送和错误提示都必须使用中文文案
- DeepSeek 创建页展示 API Key、base URL、接入类型和客户端兼容选择；不展示 OAuth、Anthropic version 或 Anthropic beta
- Claude Code / Anthropic-compatible 客户端兼容只在 `profile_deepseek_anthropic_v1` 账户表单里展示，不应出现在 DeepSeek OpenAI-compatible 账户表单里
- 导入导出协议要支持 `providerCode = deepseek`；省略 `providerProtocolProfileId` 时按 DeepSeek 默认档案 `profile_deepseek_openai_v1` 解析，导出时必须能 round-trip 回实际 `provider_protocol_profile_id`

## 易遗漏风险矩阵

| 风险点 | 易出错表现 | 约束 |
| --- | --- | --- |
| 协议误扩散 | DeepSeek 账户进入官方 Anthropic 分组，或官方 Anthropic 账户进入 DeepSeek 分组 | `profile_deepseek_openai_v1` 和 `profile_deepseek_anthropic_v1` 必须分别隔离；DeepSeek Anthropic 仍归属 `providerCode=deepseek` |
| 客户端兼容误当协议 | 只改 `client_compatibility` 就切换 Base URL 或 endpoint modes | 上游协议由 `provider_protocol_profile_id` 决定，客户端兼容只在档案内生效 |
| endpoint mode 误放开 | 普通 `/responses`、FIM、Prefix beta、Images、count_tokens 被路由到 DeepSeek | OpenAI 档案默认只开 `chat_json`、`chat_sse`；Codex bridge 只在 Codex 客户端画像 + `codex_responses` 账号下接收 `/responses` |
| 通用 OpenAI 层丢字段 | `thinking` 被 sanitizer 删除，`reasoning_content` 或 cache usage 解析不到 | DeepSeek 扩展字段由供应商层保留 |
| Anthropic header 误配置 | 把 `anthropic-version`、`anthropic-beta` 做成 DeepSeek 账户必填项或能力开关 | DeepSeek 官方会忽略这些 header，账户不配置、不依赖 |
| Claude 模型名静默 fallback | 任意未知 `claude-*` 被上游自动映射到 flash | 本地显式映射已知前缀，未知模型拒绝或按配置映射并记录 |
| Anthropic 多模态误宣称 | image/document/MCP/code execution 被 UI 或公开推送标为支持 | DeepSeek Anthropic 当前只声明 Messages JSON/SSE；未验证能力不得标为支持 |
| reasoning 误入正文 | 把 `reasoning_content` 拼入普通 `content` 或列表页完整展示 | 可审计 / 摘要，不进入普通回答文本 |
| keep-alive 误判 | SSE comment 被当成可见输出，非流式空行被当成非法 JSON | 只作为传输保活刷新活跃时间 |
| 模型混池 | DeepSeek 模型进入 GPT 目录或 GPT 账号兜底承接 DeepSeek 请求 | `providerCode = deepseek` 独立目录和价格 |
| 跨供应商切号 | OpenAI/GPT/GLM 账号失败后切到 DeepSeek，或反向切换 | 按 `provider_protocol_profile_id` 和模型路由过滤 |
| 旧别名默认化 | 新账号默认测试 `deepseek-chat` / `deepseek-reasoner` | 新默认使用 `deepseek-v4-flash` |
| 错误硬编码 | 401/403/402/429/5xx 或余额文案直接写死账号状态 | 只进诊断和账户错误处理策略输入，持久状态需确认 |
| `insufficient_system_resource` 误判 | 当成本地网关错误或立即打坏账号 | 作为响应语义异常完成，可观察 / 重试，不能直接写持久状态 |
| cache 成本漏算 | 只保存 prompt/completion tokens，丢 cache hit/miss | 保留 `prompt_cache_hit_tokens`、`prompt_cache_miss_tokens` |
| Balance 误用 | 请求前查余额或用余额替代本地额度 | Balance 仅后续诊断候选，不进热路径 |
| 代理路径不一致 | 真实请求走代理，账户测试 / 恢复探活直连 | 所有真实上游模型调用都走账户绑定代理 |
| 导入/公开推送猜测 | 只靠 `base_url` 判断 DeepSeek、协议档案或 beta 能力 | 必须显式 `providerCode` / `providerProtocolProfileId`，或使用 DeepSeek 默认档案 |

## 分组、授权与 API Key 路由

DeepSeek 目标创建两个默认分组：

- 默认 DeepSeek 分组：绑定 `profile_deepseek_openai_v1`
- 默认 DeepSeek Anthropic 分组：绑定 `profile_deepseek_anthropic_v1`

账户只能加入相同 `provider_protocol_profile_id` 的分组。

API Key 多分组绑定允许跨供应商协议档案；一个本地 API Key 可以同时绑定 DeepSeek OpenAI、DeepSeek Anthropic 和其他供应商分组，但每次请求必须先用入口协议与 `model` 命中目标 `provider_protocol_profile_id`，再只在该 Key 已绑定的目标档案分组内调度。

## 账号测试

DeepSeek 账户测试必须复用真实网关链路：

- OpenAI-compatible 测试路径使用 `/v1/chat/completions`
- Anthropic-compatible 测试路径使用 `/v1/messages`
- 默认测试模型建议优先使用 `deepseek-v4-flash`
- 测试请求不发送不相关的 OpenAI / GPT 专属字段
- 测试默认不启用 beta prefix、FIM、Responses、count_tokens、多模态、MCP 或 code execution 能力
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则

## 开源与官方资料

官方来源：

- API 文档：<https://api-docs.deepseek.com/>
- Chat Completions：<https://api-docs.deepseek.com/api/create-chat-completion>
- Anthropic API：<https://api-docs.deepseek.com/guides/anthropic_api>
- Thinking Mode：<https://api-docs.deepseek.com/guides/thinking_mode>
- Tool Calls：<https://api-docs.deepseek.com/guides/tool_calls>
- Chat Prefix Completion Beta：<https://api-docs.deepseek.com/guides/chat_prefix_completion>
- FIM Completion Beta：<https://api-docs.deepseek.com/guides/fim_completion>
- Context Caching：<https://api-docs.deepseek.com/guides/kv_cache>
- List Models：<https://api-docs.deepseek.com/api/list-models>
- Get User Balance：<https://api-docs.deepseek.com/api/get-user-balance>
- Error Codes：<https://api-docs.deepseek.com/quick_start/error_codes>
- Pricing：<https://api-docs.deepseek.com/quick_start/pricing>
- DeepSeek-V3 官方仓库：<https://github.com/deepseek-ai/DeepSeek-V3>
- DeepSeek-R1 官方仓库：<https://github.com/deepseek-ai/DeepSeek-R1>

## 实施清单

- 新增 `deepseek` 供应商种子
- 新增 `profile_deepseek_openai_v1`
- 新增 `profile_deepseek_anthropic_v1`
- 新增默认 DeepSeek 分组和默认 DeepSeek Claude Code 分组
- 前端账户创建在选择 `DeepSeek` 后展示 `DeepSeek OpenAI-compatible API Key`
- 前端账户创建在选择 `DeepSeek` 后展示 `DeepSeek Claude Code API Key`
- 后端账户创建、编辑、导入和公开推送接口按协议档案解析 DeepSeek 接入类型
- DeepSeek OpenAI 档案默认只启用 Chat JSON/SSE
- DeepSeek Anthropic 档案默认只启用 Messages JSON/SSE，不默认启用 Count Tokens
- DeepSeek 供应商策略保留 `user_id`、`thinking`、`reasoning_effort`、`reasoning_content`、cache usage、keep-alive 和 beta 能力边界
- DeepSeek Codex bridge 保留 Codex `/v1/responses` 入站形态、function tools、`input_image` data URL、reasoning 和上游 Chat SSE 错误事件语义
- DeepSeek 模型目录和价格目录单独建文件，成本估算按 `providerCode=deepseek` 查找
- DeepSeek 响应语义检查补充 `reasoning_content`、cache usage、`insufficient_system_resource`、SSE comment / 非流式空行保活
- DeepSeek 账户错误处理、账号切换、半开恢复、后台复测全部复用现有统一链路
- DeepSeek 导入导出、公开推送、前端供应商能力、模型检测入口隔离和账户测试补齐回归测试
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗和测试说明同步更新

## 验证要求

当前文档落地后至少覆盖：

- DeepSeek 作为独立供应商，不复用 GPT 供应商编码
- DeepSeek OpenAI-compatible 账户保存后落到 `profile_deepseek_openai_v1`
- DeepSeek Claude Code 账户保存后落到 `profile_deepseek_anthropic_v1`
- DeepSeek 默认分组绑定 `profile_deepseek_openai_v1`
- DeepSeek Claude Code 默认分组绑定 `profile_deepseek_anthropic_v1`
- OpenAI-compatible 面只声明 Chat Completions；Codex bridge 只作为显式客户端兼容适配
- 不创建 DeepSeek native 协议档案
- 不把普通 `/responses`、`/completions`、FIM 或 beta prefix 作为默认测试能力
- DeepSeek Anthropic-compatible 不进入官方 Anthropic 账号池、默认分组或价格目录；真实客户端验收必须覆盖 Claude Code / Anthropic SDK 请求，不能只看官方兼容文档
- `user_id`、`thinking`、`reasoning_effort`、`reasoning_content`、keep-alive 和 cache usage 只作为 DeepSeek 供应商扩展
- `GET /v1/models` 继续由本地模型目录返回，不请求 DeepSeek 上游
- 旧别名 `deepseek-chat`、`deepseek-reasoner` 不作为新默认模型
- DeepSeek Balance 接口不参与网关请求链路或本地额度判断

## 验证记录

`2026-06-20` 已执行：

- `pnpm --filter juhe-ai-backend typecheck`
- `pnpm --filter juhe-ai-frontend typecheck`
- `pnpm --filter juhe-ai-backend test:deepseek-gateway-mock-ai`
- `pnpm --filter juhe-ai-backend test:usage-pricing`
- `pnpm --filter juhe-ai-backend test:model-catalog`
- `pnpm --filter juhe-ai-backend test:openai-endpoint-modes`
- `pnpm --filter juhe-ai-backend test:default-group-current-contract`
- `pnpm --filter juhe-ai-backend test:model-check-trusted-comparison`
- `pnpm --filter juhe-ai-backend test:account-model-mapping`
- `pnpm --filter juhe-ai-backend test:api-key-group-route-capability`
- `pnpm --filter juhe-ai-backend test:openai-compatible-gateway-e2e`
- `pnpm --filter juhe-ai-backend test:account-test-responses-contract`
- `pnpm --filter juhe-ai-backend test:upstream-request-failure`
- `pnpm --filter juhe-ai-backend test:usage-stats-batch-statement`
- `pnpm --filter juhe-ai-backend test:provider-boundary-source`
- `pnpm --filter juhe-ai-backend test:cooldown-retest-recovery`
- `pnpm --filter juhe-ai-backend test:account-health-check`
- `pnpm --filter juhe-ai-backend test:openai-api-key-passthrough`
- `pnpm --filter juhe-ai-backend test:account-pending-test`
- `pnpm --filter juhe-ai-backend test:account-test-local-restore`
- `pnpm --filter juhe-ai-backend test:account-test-task-boundary`
- `pnpm --filter juhe-ai-backend test:account-api-key-draft-activation`
- `pnpm --filter juhe-ai-backend test:account-api-key-gateway-mock-ai`
- `pnpm --filter juhe-ai-backend test:external-source-auth`
- `pnpm --filter juhe-ai-frontend test:account-import-protocol`
- `pnpm --filter juhe-ai-frontend test:model-check-provider-capabilities`
- `pnpm --filter juhe-ai-frontend test:provider-model-formatters`

早前真实上游验证：

- `https://vsllm.com` + `deepseek-ai-v4-flash`：历史本地模型目录、Chat JSON、Chat SSE 曾通过。`2026-06-20` 追加回归发现该 NewAPI 代理在非流式 Chat JSON 下可能返回 HTTP 200 + `choices:null`；网关已补通用协议结构守卫，把该坏形态转成受控 `upstream_protocol_error`，避免原样暴露给客户端。
- `https://vsllm.com` + `deepseek-ai-v4-pro`：Chat JSON 返回上游 429，错误语义为当日 `deepseek-ai/DeepSeek-V4-Pro` 配额超限；Chat SSE 返回上游 500，错误码为 `bad_response_status_code`，上游消息为 `net_exception_connect_timeout`。这不是本地网关协议或 DeepSeek driver 失败。
- 已额外直连排查 `Content-Length`：不带 `Content-Length` 的原生 `https.request` 会复现 `choices:null`，带准确 `Content-Length` 返回正常 `choices`；网关已修复并在 mock 回归中断言。
- 敏感信息检查：已用 `rg` 扫描 `backend`、`frontend`、`docs` 和 `package.json`，未发现真实 DeepSeek Key 或 `JUHE_REAL_DEEPSEEK_API_KEY` 赋值落盘。

`2026-06-20` 追加真实兼容验证：

- 直接请求 `https://vsllm.com/v1/models` 成功返回模型列表，并包含 `deepseek-ai-v4-flash`、`deepseek-v4-pro-free`、`deepseek-ai-v4-pro`。
- 直接请求 OpenAI-compatible `POST https://vsllm.com/v1/chat/completions`：
  - `deepseek-ai-v4-flash` JSON / SSE 通过。
  - `deepseek-ai-v4-pro` JSON / SSE 返回上游配额 429，不作为本地协议失败。
  - `deepseek-v4-pro-free` JSON / SSE 连接提前结束，需要后续更换时段或上游确认。
- 直接请求 Anthropic-compatible `POST https://vsllm.com/v1/messages`：
  - `deepseek-ai-v4-flash` JSON / SSE 通过，usage 返回 Anthropic 形态的 `input_tokens`、`output_tokens` 和 cache 字段。
  - `deepseek-ai-v4-pro` JSON 返回上游认证 / ModelScope token 错误，SSE 返回配额 429，不作为本地协议失败。
  - `deepseek-v4-pro-free` JSON / SSE 连接提前结束，需要后续更换时段或上游确认。
- `POST https://vsllm.com/v1/messages/count_tokens` 返回 404 `Invalid URL`，验证 DeepSeek / NewAPI Anthropic-compatible 账号不能默认启用 `message_token_counting`。
- 直接工具调用验证未通过稳定输出：OpenAI-compatible tool calls 返回 `choices:null`，Anthropic-compatible tool use 返回空 content。因此工具调用不能作为 DeepSeek / NewAPI Anthropic-compatible 默认已验证能力，后续必须按模型和上游单独验收。
- `https://vsllm.com/anthropic/v1/messages` 返回站点 HTML，不是该 NewAPI 代理的 Anthropic API 路径。因此官方 DeepSeek 直连默认 Base URL 仍是 `https://api.deepseek.com/anthropic`；使用 `https://vsllm.com` 这类 NewAPI 代理时，DeepSeek Anthropic-compatible 账户的 `base_url` 应配置为 `https://vsllm.com`，由网关拼接 `/v1/messages`。
- 本机 Claude Code `2.1.62` 直连验证通过：`ANTHROPIC_BASE_URL=https://vsllm.com`、模型 `claude-sonnet-4-6`、非交互 `--print` 能返回预期 marker，说明 DeepSeek / NewAPI Anthropic-compatible 可被 Claude Code 类客户端消费。
- 本机 opencode `1.1.11` 验证受客户端本地模型表阻断：内置 OpenAI provider 不认识 `deepseek-ai-v4-flash`，当前安装版本也未暴露 `anthropic` provider ID；未写入自定义 provider 配置或凭据文件。后续如要验证 opencode，需要使用一次性配置目录定义 DeepSeek / NewAPI provider，并在测试后清理凭据。
- 本地网关真实 E2E 已补 `deepseek-ai-v4-flash` 单模型回归：
  - `test:deepseek-real-gateway-e2e` 使用 `https://vsllm.com` 时，本地 `/v1/models` 通过。
  - Chat JSON 如果上游返回正常 `choices`，要求输出非空；如果上游返回 HTTP 200 + `choices:null`，要求本地网关转成 502 `upstream_protocol_error`，且不得把 `choices:null` 原样返回给客户端。
  - Chat SSE 通过，收到非空输出和 `[DONE]`。
  - DeepSeek Codex bridge 通过：本地 `/v1/responses` + Codex turn metadata 进入桥接，上游走 Chat SSE，下游输出 `response.created`、输出 delta 和 `response.completed`，且不泄漏 `chat.completion.chunk`。
  - `test:anthropic-real-gateway-e2e` 使用 `https://vsllm.com` + `deepseek-ai-v4-flash` 时，账号落库、runtime 选号、本地 `/v1/models` 通过，但 Messages JSON marker 输出为空；直接请求上游 `/v1/messages` 正常。说明现有官方 Anthropic provider driver 可以验证部分协议栈，但还不能代表 DeepSeek Anthropic-compatible 已完成。

后续补测命令：

```powershell
$env:JUHE_REAL_DEEPSEEK_API_KEY = '<DeepSeek 或代理上游 Key>'
$env:JUHE_REAL_DEEPSEEK_BASE_URL = 'https://vsllm.com'
$env:JUHE_REAL_DEEPSEEK_MODELS = 'deepseek-ai-v4-flash,deepseek-ai-v4-pro'
pnpm --filter juhe-ai-backend test:deepseek-real-gateway-e2e
Remove-Item Env:\JUHE_REAL_DEEPSEEK_API_KEY, Env:\JUHE_REAL_DEEPSEEK_BASE_URL, Env:\JUHE_REAL_DEEPSEEK_MODELS -ErrorAction SilentlyContinue
```
