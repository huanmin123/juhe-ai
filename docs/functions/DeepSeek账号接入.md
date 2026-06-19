# DeepSeek 账号接入

## 范围

本文记录 DeepSeek 供应商的目标接入方案、账户创建类型、协议档案、网关透传边界、模型目录和后续实现注意事项。本文是实施前的目标方案文档；代码尚未落地时，运行事实仍以当前系统实际内置供应商为准。

DeepSeek 对外 hosted API 当前以 OpenAI-compatible 为主线，默认地址为 `https://api.deepseek.com`，部分 beta 能力走 `https://api.deepseek.com/beta`。DeepSeek 官方也提供 Anthropic-compatible surface，但兼容能力不完整，当前项目不接入 DeepSeek Anthropic-compatible，不新增 `profile_deepseek_anthropic_v1`。

结论：

- 一个供应商：`deepseek`
- 一个协议档案：`profile_deepseek_openai_v1`
- 一个账户创建类型：DeepSeek OpenAI API Key
- 底层账户类型仍为 `api_key`
- 网关默认只声明 OpenAI-compatible Chat Completions JSON / SSE 能力
- 当前不声明 OpenAI Responses、Anthropic Messages 或 DeepSeek native 协议

## 供应商与协议档案

建议新增供应商：

```ts
type ProviderCode = 'deepseek'
```

建议显示名称为 `DeepSeek`。`deepseek` 是独立供应商，不能复用 `openai`、`gpt` 或其账号池、分组和价格目录。`providerCode=openai` 仍表示通用 OpenAI-compatible 聚合目录；DeepSeek 自身账号池、分组、模型价格和测试策略都归属于 `deepseek`。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_deepseek_openai_v1` | `deepseek` | `openai/v1` | `https://api.deepseek.com` | DeepSeek OpenAI API Key | `chat_json`、`chat_sse` |

OpenAI-compatible 档案当前只声明 Chat Completions 语义，不把 Responses 当成默认能力。`/beta` 下的 Chat Prefix Completion 和 FIM Completion 作为 DeepSeek 供应商 beta 能力记录，不扩展成通用 OpenAI Responses 档案。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

选择 `DeepSeek` 后只展示一个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 |
| --- | --- | --- | --- | --- |
| DeepSeek OpenAI API Key | `api_key` | `profile_deepseek_openai_v1` | `api_key`、`base_url` | `deepseek-v4-flash` |

这里的“接入类型”是产品表单概念，不是新增 OAuth 类型。后端保存时仍加密保存 `credentials.api_key`，并写入 `provider_protocol_profile_id = profile_deepseek_openai_v1`。

保存规则：

- 新建 DeepSeek 账户默认写入 `pending_test`，测试通过后才允许正常调度
- `base_url` 默认填充 `https://api.deepseek.com`，允许用户修改为同协议的代理地址或专属部署地址，但必须继续通过 SSRF 防护和 OpenAI-compatible base URL 校验
- `credentials.supported_endpoint_modes` 省略时默认启用 `chat_json`、`chat_sse`
- DeepSeek 账户不显示 GPT OAuth 字段，不显示 Refresh Token、Access Token、Codex Responses 账号兼容切换
- DeepSeek 账户默认走普通 OpenAI-compatible 透传思路，不引入 GPT 专属的客户端兼容策略

## 网关请求边界

DeepSeek OpenAI v1 档案优先复用现有 OpenAI v1 Chat Completions 协议适配器，但必须有独立供应商策略。

请求路径：

- 客户端可请求 `/chat/completions` 或 `/v1/chat/completions`
- beta 能力按 `https://api.deepseek.com/beta` 单独拼接
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求 DeepSeek 上游模型列表
- 当前不把 `/responses` 作为 DeepSeek 档案默认候选；如果后续官方能力和本地适配都确认，再单独放开

请求头：

- 上游统一写入 `Authorization: Bearer <DeepSeek API Key>`
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩相关头、SDK / tracing / 部署平台噪声头
- 不从账号配置生成 `OpenAI-Organization`、`OpenAI-Project` 或 GPT / Codex 专属 header

## DeepSeek 特殊参数

DeepSeek 大部分 Chat Completions 参数沿用 OpenAI-compatible 形态，例如 `messages`、`model`、`stream`、`temperature`、`top_p`、`max_tokens`、`stop`、`response_format`、`tools`、`tool_choice`、`logprobs` 和 `top_logprobs`。以下参数和返回字段需要作为 DeepSeek 供应商扩展处理，不能写成全局 OpenAI v1 固定语义。

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

### Reasoning Content

DeepSeek 返回中可能包含 `reasoning_content`：

- 非流式：读取 `choices[].message.reasoning_content`
- 流式：读取 `choices[].delta.reasoning_content`
- 多轮请求可以把上一轮 assistant message 的 `reasoning_content` 连同 `content`、`tool_calls` 一起传回上游

本项目处理策略：

- 可见文本仍以 `content` 为主
- `reasoning_content` 可进入诊断、审计摘要或扩展字段，但不要拼入普通 `content`
- 统计和调试可以保留 reasoning token / reasoning text 边界，但不得把它推导成所有 OpenAI-compatible 模型都具备的字段

### JSON Output 与 Tool Calls

DeepSeek 支持 OpenAI-compatible 的 JSON Output 和 Tool Calls：

- JSON Output 使用 `response_format: { "type": "json_object" }`
- Tool Calls 使用 `tools` 和 `tool_choice`

本项目处理策略：

- 这些能力可以按 Chat Completions 透传
- 如果上游返回工具调用，仍按 OpenAI v1 `choices[].message.tool_calls` / `choices[].delta.tool_calls` 解析
- 不为 DeepSeek 开启 GPT / Codex Responses 工具语义

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

### Finish Reason

DeepSeek Chat Completions 的 `finish_reason` 除常见值外，还可能出现 `insufficient_system_resource`。该值应进入响应语义检查和上游错误诊断，不要被误判为本地网关错误。

## 返回与 usage

DeepSeek OpenAI-compatible 返回应优先按 OpenAI v1 Chat 语义解析：

- JSON 响应读取 `choices[].message.content`、`choices[].message.tool_calls`、`choices[].message.reasoning_content`、`finish_reason` 和 `usage`
- 流式响应读取 `choices[].delta.content`、`choices[].delta.tool_calls`、`choices[].delta.reasoning_content` 和最后 usage chunk
- `usage.prompt_cache_hit_tokens`、`usage.prompt_cache_miss_tokens` 作为 DeepSeek 扩展 usage 字段保留

成本与统计：

- DeepSeek 模型成本按本地模型目录和官方价格口径估算
- DeepSeek hosted API 是 API-key-only，不存在 OAuth 专属额度进度
- 如果后续把 DeepSeek 自托管权重接入为本地 OpenAI-compatible 上游，应作为独立自托管供应商或自定义供应商处理，不和 DeepSeek 云 API 混淆

## 模型目录

DeepSeek 模型目录必须单独维护在 `deepseek` 供应商下，不要混进 GPT 价格文件。

初始目录建议：

- `deepseek-v4-flash`
- `deepseek-v4-pro`

旧别名 `deepseek-chat` 和 `deepseek-reasoner` 已被官方标记为将于 `2026-07-24 15:59 UTC` 退役。当前在目录中可以作为历史兼容别名保留，但不应作为新建账户或新默认测试模型的首选。

模型目录字段要求：

- `providerCode = deepseek`
- `model` 使用 DeepSeek 官方模型 ID，小写和连字符按官方写法保留
- `supportedApiProtocols` 当前只放 OpenAI-compatible 档案对应的 `chat_completions`
- `contextWindowTokens` 和 `releaseDate` 以官方模型页或官方文档为准
- 价格只采信 DeepSeek 官方文档或官方 API 文档；第三方价格库和社区表格只能作为线索

DeepSeek 开源模型，例如 `DeepSeek-R1`、`DeepSeek-V3` 以及相关公开仓库和权重，可以作为自托管、vLLM、SGLang 或 Transformers 接入参考，但不等于 DeepSeek 云 API 的模型 ID、上下文窗口或价格。自托管 DeepSeek 如果通过 OpenAI-compatible vLLM / SGLang 暴露，应作为自定义 OpenAI-compatible 上游或独立自托管供应商处理。

## 分组、授权与 API Key 路由

DeepSeek 只创建一个默认分组：

- 默认 DeepSeek OpenAI 分组：绑定 `profile_deepseek_openai_v1`

账户只能加入相同 `provider_protocol_profile_id` 的分组。

当前 API Key 多分组绑定仍以同一供应商协议档案为硬边界；后续如果模型路由落地，一个本地 API Key 可以同时绑定 DeepSeek 和其他供应商分组，但每次请求必须先用 `model` 命中目标 `provider_protocol_profile_id`，再只在该档案的分组内调度。

## 账号测试

DeepSeek 账户测试必须复用真实网关链路：

- 测试路径使用 `/v1/chat/completions`
- 默认测试模型建议优先使用 `deepseek-v4-flash`
- 测试请求不发送不相关的 OpenAI / GPT 专属字段
- 测试默认不启用 beta prefix、FIM 或 Responses 能力
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则

## 开源与官方资料

官方来源：

- API 文档：<https://api-docs.deepseek.com/>
- Chat Completions：<https://api-docs.deepseek.com/api/create-chat-completion>
- Thinking Mode：<https://api-docs.deepseek.com/guides/thinking_mode>
- Chat Prefix Completion Beta：<https://api-docs.deepseek.com/guides/chat_prefix_completion>
- FIM Completion Beta：<https://api-docs.deepseek.com/guides/fim_completion>
- Context Caching：<https://api-docs.deepseek.com/guides/kv_cache>
- DeepSeek-V3 官方仓库：<https://github.com/deepseek-ai/DeepSeek-V3>
- DeepSeek-R1 官方仓库：<https://github.com/deepseek-ai/DeepSeek-R1>

## 实施清单

- 新增 `deepseek` 供应商种子
- 新增 `profile_deepseek_openai_v1`
- 新增默认 DeepSeek OpenAI 分组
- 前端账户创建在选择 `DeepSeek` 后只展示 `DeepSeek OpenAI API Key`
- 后端账户创建、编辑、导入和公开推送接口按协议档案解析 DeepSeek 接入类型
- DeepSeek OpenAI 档案默认只启用 Chat JSON/SSE
- DeepSeek 供应商策略保留 `thinking`、`reasoning_effort`、`reasoning_content`、cache usage 和 beta 能力边界
- DeepSeek 模型目录和价格目录单独建文件，成本估算按 `providerCode=deepseek` 查找
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗和测试说明同步更新

## 验证要求

当前文档落地后至少覆盖：

- DeepSeek 作为独立供应商，不复用 GPT 供应商编码
- DeepSeek 账户保存后落到 `profile_deepseek_openai_v1`
- DeepSeek 默认分组绑定 `profile_deepseek_openai_v1`
- OpenAI-compatible 面只声明 Chat Completions
- 不创建 DeepSeek Anthropic-compatible 协议档案
- 不把 `/responses`、`/completions`、FIM 或 beta prefix 作为默认测试能力
- `thinking`、`reasoning_effort`、`reasoning_content` 和 cache usage 只作为 DeepSeek 供应商扩展
- `GET /v1/models` 继续由本地模型目录返回，不请求 DeepSeek 上游
- 旧别名 `deepseek-chat`、`deepseek-reasoner` 不作为新默认模型
