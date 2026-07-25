# Codex Responses 转 Chat 协议转换设计

> 2026-06-27 路由分层更新：本文描述的是可复用协议转换能力和历史桥接测试背景，不表示 API Key 或策略路由可以保存显式跨协议规则。当前目标是 API Key 只绑定策略路由，策略路由只负责分组和模型调度；OpenAI v1 普通账号可通过 `modelMappings` 显式声明 `responses -> chat_completions`，其他跨协议承接落到混合供应商账户。

## 范围

本文记录项目内“Codex / OpenAI Responses 请求 -> 上游 OpenAI-compatible Chat Completions”的通用转换层。该能力不是 GLM 专属：GLM Coding Plan、DeepSeek、Gemini OpenAI Chat、Kimi 或其他只支持 Chat Completions 的 OpenAI-compatible 上游如需承接 Responses 客户端，都应复用同一转换层，再在供应商 driver 内按自身协议细节做少量配置。

OpenAI Chat / Responses 到 Anthropic Messages 是另一条桥接线，不复用本文的 Chat-only 结论；它需要同时覆盖 Chat JSON、Chat SSE、Responses JSON、Responses SSE 四类下游入口，设计见 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md)。

当前实现落点：

- 通用转换层：`backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`
- GLM 启用点：`backend/src/modules/providers/drivers/glm/driver.ts`
- DeepSeek 启用点：`backend/src/modules/providers/drivers/deepseek/driver.ts`
- 普通账号模型映射判断：`backend/src/modules/gateway/protocols/openai-v1/model-mapping.ts`

## 设计原则

- 协议转换是共享基础设施，不写死在单个供应商目录下。
- 供应商 driver 只负责判断是否启用、默认模型、模型映射、上游路径和供应商级细节。
- 账户能力仍按真实上游能力保存。只支持 Chat 的上游账户不伪装成 `responses_sse`，桥接层只是让特定客户端请求在调度时要求账户具备 `chat_sse`。
- 当前 bridge 只承接显式映射命中的流式 `/v1/responses` 主路径，不实现完整 OpenAI Responses API。
- 原生 Responses / OpenAI OAuth Codex 路径和 Chat-only bridge 是两条不同能力线：前者可以透传 `/responses/compact` 等原生能力，后者必须由网关维护 `previous_response_id` 状态和自有 compact envelope，不能要求 Chat 上游认识 Responses 状态。
- Anthropic Messages bridge 不属于本文的 `responses -> chat_completions` 范围；它的上游真实 endpoint family 是 `messages`，返回侧必须按下游 Chat / Responses 协议重新渲染。

### 上游能力缺口处理规则

- 协议转换只适配上游供应商和当前模型真实支持的能力。没有真实上游能力或本地执行器时，不能靠字段改名伪造 `web_search_call`、`mcp_call`、`computer_call`、`image_generation_call`、`code_interpreter_call` 或工具结果。
- 供应商 / 模型不支持某项工具或 hosted/native 能力时，默认返回正常 agent guidance 消息，HTTP 和下游协议形态保持可被客户端继续消费；guidance 需要说明不支持的能力类型、当前上游协议和建议动作，例如配置本地 MCP / 工具执行器、换用支持该能力的模型或移除该工具。
- guidance 文案只描述通用“客户端 agent / 调用方 agent”，不写死具体客户端名称。后续是否查找本地 MCP、切换工具或改写下一轮请求交给客户端 agent 自行处理。
- 只有请求本身非法、权限或文件归属越界、状态链缺失 / 跨边界、schema 校验失败、请求体格式错误等不能安全继续的场景才返回本地协议错误。能力缺口不能返回 500，也不能把上游不支持包装成账号不可用。

## 启用条件

通用桥接层只有在供应商 driver 和账号模型映射共同满足条件时才生效。当前启用范围：

- GLM Coding：`profile_glm_coding_openai_v1`。
- DeepSeek OpenAI v1：`profile_deepseek_openai_v1`。
- 通用 OpenAI-compatible / Gemini OpenAI Chat 等 OpenAI v1 Chat-only 档案在显式 `responses -> chat_completions` 映射命中时复用同一转换层。

共同启用条件：

- 下游请求是 `POST /responses` 或 `POST /v1/responses`。
- 请求命中普通 OpenAI v1 账号的显式 `responses -> chat_completions` 模型映射；旧 `runtimeSource=explicit_hybrid_route` 注入不再生效。
- 下游请求是流式请求，即 `stream=true` 且 `Accept: text/event-stream` 语义成立。
- 账户真实 endpoint mode 支持 `chat_sse`。

未配置模型映射的通用 GLM / DeepSeek / OpenAI-compatible Chat-only 账号不启用桥接；普通 OpenAI-compatible 客户端发起 `/v1/responses` 时，只有模型映射命中才进入桥接。

## 请求转换

下游 Responses 请求会被改写为 Chat Completions SSE 请求：

| Responses 字段 | Chat 字段 | 当前规则 |
| --- | --- | --- |
| `model` | `model` | 先用账户模型映射结果，再用请求模型，最后用供应商默认模型 |
| `instructions` | `messages[0].role=system` | 非空时注入 system 消息 |
| `input` 字符串 | `messages[].role=user` | 直接作为用户消息 |
| `input[].type=message` | `messages[]` | `user/assistant/system/developer` 映射为 Chat role，`developer` 降级为 `system`；`role=tool` 不作为普通 message 透传 |
| `input[].type=reasoning` | 供应商 `reasoning_content` | DeepSeek 与 GLM bridge 均启用；用于满足 thinking + tool call 后续请求必须回传思考内容的供应商要求，不混入普通 `content` |
| `input[].type=function_call` | assistant `tool_calls` | 用于把 Codex 上一轮工具调用历史还原给 Chat 上游；连续调用会合并为同一个 assistant tool_calls 消息 |
| `input[].type=function_call_output` | `role=tool` | 用 `call_id` 关联工具结果；结构化 output 只抽取文本类 content item |
| `input[].type=custom_tool_call` | assistant `tool_calls` | 还原为 `custom__<name>` Chat function wrapper，并把自由文本 input 包到 `{"input": ...}` |
| `input[].type=custom_tool_call_output` | `role=tool` | 用 `call_id` 关联 custom tool 结果，供 Chat 上游继续多轮 |
| `content[].type=input_image` | Chat `image_url` content part | 保留 `image_url` / data URL 和 `detail`；实际是否可用取决于上游模型视觉能力 |
| `tools[].type=function` | Chat `tools[].type=function` | 透传为 Chat function；namespace function 会展平成合法 Chat tool name，回流时恢复 namespace |
| `tools[].type=custom` | Chat `tools[].type=function` | 包装成 `custom__<name>` Chat function，schema 固定为 `{ input: string }`；回流时恢复为 Codex `custom_tool_call` |
| `tools[].type=web_search` | 不透传 | Chat-only bridge 不代执行搜索；如果供应商没有原生 Responses web_search 等价能力，返回正常 agent guidance 响应并不命中上游 |
| `tools[].type=MCP/image_generation/computer/...` | 不透传 | Chat-only bridge 不能靠字段转换伪造 Responses 托管工具；没有真实执行器或上游等价能力时返回正常 agent guidance 响应 |
| `tool_choice` | `tool_choice` | 支持字符串、指定 function、指定 custom；强制选择无法代执行的原生托管工具时返回 guidance，而不是转发到不支持的 Chat 上游 |
| `parallel_tool_calls` | `parallel_tool_calls` | 布尔值原样透传 |
| `max_output_tokens` | `max_tokens` | 同时兼容 `max_completion_tokens` |

历史工具调用会先做 Chat 消息不变量归一化：一个 assistant `tool_calls` 消息后面必须紧跟对应 `tool` 输出，夹在 `function_call` 和 `function_call_output` 之间的 `developer/message` 会延后到工具输出之后；并行工具调用即使出现 `output A -> developer -> output B` 交错，也会等已登记工具输出归齐后再写入 Chat history。没有输出的 dangling `function_call`、找不到调用的 orphan `function_call_output` 和普通 `message role=tool` 会丢弃，避免把上游很容易 400 的非法 Chat history 暴露给客户端。

当前实现不透传 MCP tool、tool search、local shell call、image generation、computer use、Responses `include`、`store`、`truncation` 和 `context_management` 等原生 Responses 托管语义。`function`、`custom` 和可展开为 function/custom 的 namespace tool 会转换到 Chat 工具层；`web_search`、MCP、image generation、computer use 等需要上游原生等价能力或调用方本地 agent / MCP 配置承接的工具不会由 Chat-only bridge 伪造。无法由 Chat 上游原生支持的托管工具不会静默丢弃，当前在 `auto` 和强制 `tool_choice` 场景都会返回正常 agent guidance 响应且不命中上游。GLM 搜索等能力应由调用方按供应商官方 MCP 或本地等价工具配置完成，不由本中转层实现。`previous_response_id` 已由 Chat-only bridge 的网关状态层在服务端消费：首轮成功后保存 response 状态，后续同 API Key、同分组、同供应商 profile 的请求可恢复历史并追加本轮 input；找不到、过期或跨边界时受控失败，不命中上游。`/responses/compact` 已支持网关摘要压缩：网关还原上下文后，在当前分组、当前供应商内发起内部 Chat Completions 摘要请求，再返回可反序列化的 `compaction_summary`。该能力不是上游原生 Responses compact，也不能跨网关或跨供应商消费。

### 托管工具边界

Chat-only bridge 只做可验证的协议字段转换，不把 `web_search`、MCP、image generation、computer use 等托管工具转换成伪 tool result。只有当某个供应商上游提供原生等价能力并在 driver 中明确声明转换策略时，才能单独打开对应适配；否则统一返回正常 agent guidance 响应。即使同一个请求同时带有可转换的 function / custom tools，只要 `tool_choice` 是默认、`auto` 或 `required`，且工具列表里包含不可执行的 hosted/native tool，也必须 guidance，不把剩余 function tools 透传给 Chat 上游；只有 `tool_choice=none` 或显式选中可转换 function/custom 时，才能忽略未选中的 hosted/native tool。模型能够按普通 Chat function 名称输出 `web_search` 参数，只能证明它会调用一个客户端函数，不能证明上游具备联网搜索能力。

请求头会清理 Codex / OpenAI 专属元数据，例如 `openai-beta`、`originator`、`session-id`、`thread-id`、`x-client-request-id`、`x-codex-*`，并把上游 `accept` 固定为 `text/event-stream`。

供应商级请求策略：

- GLM bridge 会补 `stream_options.include_usage=true`、`thinking.type=disabled`，并在存在 function tools 时补 `tool_stream=true`、强制 `parallel_tool_calls=false`。真实验证中 GLM 默认 thinking 容易把输出预算耗在 `reasoning_content`，`parallel_tool_calls=true` 在 vsllm GLM SSE 上出现过长时间不结束；Codex 可以通过多轮串行工具调用完成任务，不要求上游一次并行吐出多个工具。
- GLM 与 DeepSeek bridge 都会把 Codex 历史 `reasoning` 回灌到 Chat assistant `reasoning_content`，避免 thinking + tool call 后续请求因缺失推理内容而被上游拒绝或降低连续性。
- DeepSeek bridge 会补 `stream_options.include_usage=true`，用于尽量拿到真实流式 usage；缺失时仍按估算 usage 兜底。

## 响应转换

上游 Chat Completions SSE 会被转换回 Codex 可消费的 Responses SSE。根据本次检查的 Codex 源码，当前 Codex parser 实际消费的关键事件为：

- `response.created`
- `response.output_item.added`
- `response.output_text.delta`
- `response.output_item.done`
- `response.completed`
- `response.failed`
- `response.reasoning_*` 事件
- `response.custom_tool_call_input.delta`

普通 function call 不依赖 `response.function_call_arguments.delta`。Codex 当前会忽略该事件，并通过最终 `response.output_item.done` 内的 `type=function_call` item 执行工具。因此桥接层对 Chat `delta.tool_calls` 的输出策略是：

- 累积 Chat 工具调用的 `id/name/arguments`。
- 只消费当前 Chat Completions `delta.tool_calls[]`；旧式 `delta.function_call` / `delta.function_calls[]` 不再兼容。
- 最终发出 `response.output_item.done`，item 为 `type=function_call`。
- 如果 Chat tool name 命中 custom wrapper，则最终 item 为 `type=custom_tool_call`，并从 wrapper arguments 的 `input` 字段恢复自由文本。
- 不依赖 `response.function_call_arguments.delta`。

文本输出策略：

- Chat `delta.content` 和 `delta.refusal` 转为 `response.output_text.delta`。
- Chat `delta.reasoning_content` / `delta.reasoning` 转为 Codex 可消费的 `reasoning` output item 和 `response.reasoning_summary_text.delta`，不混入普通文本。
- `response.completed.response.output` 按 output index 顺序保存，避免上游先 reasoning 后文本时 completed 快照与流事件顺序不一致。
- 当前只会从 Chat tool_calls 生成 `function_call`、`custom_tool_call`；不会生成 `web_search_call`，也不会伪造 `mcp_call`、`image_generation_call`、`computer_call`、`local_shell_call` 等需要独立 runtime 或上游原生能力的 output item。

usage 策略：

- Chat `usage.prompt_tokens` -> Responses `usage.input_tokens`
- Chat `usage.completion_tokens` -> Responses `usage.output_tokens`
- Chat `usage.total_tokens` -> Responses `usage.total_tokens`
- Chat `usage.completion_tokens_details.reasoning_tokens` -> Responses `usage.output_tokens_details.reasoning_tokens`
- 无 usage 时桥接层会基于请求和输出文本生成估算 usage，避免 Codex completed 事件缺少基础用量对象。

finish_reason 策略：

- 任意完整 Chat JSON / SSE `finish_reason` 都是不可信的供应商协议字段；直通 Chat 响应原样保留，Responses-to-Chat bridge 只把它视为流终止，不按具体字符串合成业务失败。
- `tool_calls` 按普通 function/custom 输出给下游；`web_search` 等 Responses 原生托管工具会在请求转换前按 agent guidance 分类，不进入网关工具执行循环。
- DeepSeek `insufficient_system_resource`、GLM `sensitive` / `network_error` / `model_context_window_exceeded` 与其他未知值等价，系统不把它们解释成可重试、内容过滤或上下文错误。只有用户显式响应检查策略命中时，才按用户配置执行对应动作；状态变更边界以 [AI 账户错误语义与状态变更边界](AI账户错误语义与状态变更边界.md) 为准。

## 供应商自定义点

后续供应商接入同一桥接层时，只应在 driver 中配置：

- 是否启用桥接，例如仅 Coding 档案启用，通用 API 不启用。
- 默认模型，例如 GLM 使用 `glm-5.2`。
- 模型映射后的上游模型。
- Base URL 和路径拼接，例如 `/responses` 改为 `/chat/completions` 时是否追加 `/v1`。
- 供应商特有 role、参数、usage 和响应结构；不得在 driver 内写死错误业务语义。
- 是否需要额外清理或保留特定请求头。

不要为每个供应商复制一份完整 Responses-to-Chat 转换代码。

## 统计与审计

桥接请求下游表现为 `/v1/responses`，上游实际命中 `/chat/completions`。使用记录、审计尝试和排障视图应同时保留：

- `providerCode`
- `providerProtocolProfileId`
- 下游端点族：Responses
- 上游实际端点族：Chat Completions
- 下游模型和模型映射后的实际上游模型
- 是否启用了 Codex bridge

成本估算继续按实际上游模型和供应商价格目录计算。账户 endpoint mode 仍保存真实能力 `chat_sse`，不要因为桥接而把账户永久标记为 `responses_sse`。

## 压缩与 compact 边界

这里的“压缩”指 Codex / Responses 上下文压缩，不是 HTTP gzip / br 传输压缩。当前分层如下：

- 原生 Responses / OpenAI OAuth Codex 路径：可以按账号能力承接 `POST /responses/compact` 和 Codex Remote Compaction V2 的 `compaction_trigger`。网关不生成 compact 内容，只透传请求，并在返回侧做 Codex compact 契约检查。
- Codex compact 契约检查：识别 `/responses/compact` 或带 `compaction_trigger` 的 Codex `/responses` 请求后，SSE 返回必须在完成时恰好包含 1 个 Codex 可反序列化的 `compaction` / `compaction_summary` item，且 `encrypted_content` 必须是字符串；否则在写给客户端前拦截并触发服务端换号或返回 Codex 可重试失败。
- Chat-only bridge 当前实现：由网关托管 `previous_response_id` 对应的 Responses input/output 增量状态；状态关系索引写入 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` 下的 Responses 桥接状态索引 shard，完整上下文 payload 按 session/hour 追加到 `JUHE_AI_CODEX_CONTEXT_ROOT` 下的 gzip segment，不进入业务库。compact 时先还原完整上下文，再在当前分组和当前供应商内调度非流式 Chat Completions 摘要请求，保存 compact snapshot，并返回 `type=compaction_summary`、`encrypted_content=juhecmp.v2.<compact_id>.<digest>` 的网关自有 envelope。后续请求带回该 item 时，bridge 校验边界、TTL 和 digest 后读取 snapshot，再恢复为 Chat system summary。
- Chat-only compact snapshot 仍然是网关自有 compact，不等价于上游原生 Responses compact；它只能在本网关、同 API Key / 分组 / 供应商档案边界内恢复。
- 供应商自有压缩能力：如果供应商提供非 Responses 的 Chat 侧 `/compact` 或等价摘要接口，可以作为 Chat-only gateway compact 的摘要来源，但返回结果仍要落到网关 compact snapshot；通用兜底必须走 Chat Completions，即 OpenAI-compatible 语义下的 `/v1/chat/completions`，不能把已转换为 Chat 的压缩请求继续发给上游 `/v1/responses/compact`。只有供应商明确提供可被后续原生 `/responses` 直接消费的 compact output，才属于原生 Responses compact。

### `previous_response_id` 目标处理

Chat 上游通常不认识 `previous_response_id`。目标设计中，该字段只在网关内消费：

1. 首轮 bridge `/responses` 成功后，网关保存 `response_id`、授权边界、供应商 profile、Responses input/output items、归一化 Chat messages、工具状态和 TTL。
2. 后续请求带 `previous_response_id` 时，网关校验 API Key / team / group / provider profile 是否一致，再恢复历史并追加本轮 input。
3. 恢复后的完整上下文重新进入 Responses -> Chat 转换，Chat 上游只收到普通 `messages`、`tools` 和供应商允许的字段。
4. 状态找不到、过期、跨分组、跨供应商或工具状态不完整时，返回受控错误，不把缺失历史的请求发给上游。

### 混合候选账号的请求渲染边界

同一分组可能同时包含原生 Responses 账号和显式 `responses -> chat_completions` 映射账号。此时不能在账号筛选前按“分组里存在桥接账号”提前改写请求体，否则桥接状态、网关自有 compact 摘要或已删除的 `previous_response_id` 会污染随后选中的原生 Responses 上游。

请求处理固定分为两阶段：

1. 预检阶段只解析规范请求体、解析 `juhecmp.v2` 引用并识别 `previous_response_id` 来源，不修改网关请求体。
2. 每次实际派发前，按本次选中的账号重新从规范请求体渲染上游请求；切号重试时不得复用上一账号改写后的 body。

规范请求体不是“最早预检快照”。图像权限、路由模型选择等公共预检在首次派发前产生的合法改写必须合入派发基线；质量检查重试等在派发期间追加的修复内容也必须更新下一次渲染基线。账号专属渲染结果本身不能反向覆盖基线，只有账号渲染之后出现的新公共改写才允许同步。

按实际账号的渲染规则：

- 显式 Responses -> Chat 映射账号：恢复网关内部状态，消费内部 `previous_response_id`，把网关 compact 摘要展开为 Chat history；成功完成后才续写桥接状态。
- 原生 Responses 账号：保留原生 Responses item；如果请求里包含网关内部 `juhecmp.v1` 摘要，则转成普通 `developer` 文本消息，禁止把网关私有 envelope 发送给原生上游。
- 原生上游返回的 opaque `encrypted_content` 不属于网关 compact，不解码、不改写。
- 网关已识别的内部桥接 `previous_response_id` 不发送给原生上游；无法识别为本地桥接状态的外部 `previous_response_id` 保留给原生 Responses 上游，并排除显式 Chat bridge 账号。
- `clientCompatibility=codex_responses` 只是客户端兼容画像，不能替代账号的显式模型映射，也不能单独触发 Chat bridge 状态处理。

`POST /responses/compact` 也必须先区分状态来源：内部 bridge `previous_response_id` 只进入网关摘要链路且摘要候选只包含显式 Chat bridge 账号；外部原生 `previous_response_id` 只进入原生 Responses 账号。请求没有 previous id 且候选池同时存在原生与 bridge 账号时优先原生 compact，不能因为池中“存在一个 bridge”就全局截获。

内部桥接 response id 只识别共享 Responses -> Chat 转换层当前生成的固定前缀。不能把所有 `resp_*` 都当成本地状态，因为原生 OpenAI Responses 的合法 id 使用同一公共前缀。

### Gateway summary compact 目标处理

Chat-only compact 的摘要模型只能在当前请求授权边界内选择：

- 当前 API Key 绑定的当前分组。
- 当前请求命中的当前供应商和 provider profile。
- 同一套候选账号筛选、账号冷却、切号、统计、审计和错误处理。
- 通用摘要请求固定使用 Chat Completions endpoint family；上游不能收到 `/responses/compact`。
- 内部摘要请求的模型别名按原始 Responses compact 请求的 `Responses` 源协议匹配；命中后只改写 Chat Completions 请求体里的 `model` 和统计 / 审计上游模型口径，不把上游路径改成 `/responses`。
- 内部摘要请求必须设置 `disableCompact = true`，避免递归 compact。

摘要模型可以是供应商非 Responses 的专用 compact endpoint、专用摘要模型或普通 Chat 模型。当前实现使用普通非流式 Chat Completions，并校验返回摘要必须是非空文本。客户端只看到 Codex 可识别的 `compaction_summary` item；后续请求带回该 item 时，网关按 compact id 读取 snapshot 并恢复为 Chat summary。

## 已验证结论

- 已用 Codex 源码确认：普通 function call 应通过最终 `response.output_item.done` 的 `type=function_call` item 交给 Codex；`response.function_call_arguments.delta` 当前不被普通 function call 路径消费。
- 已用 Codex 源码确认：Codex input / output item 覆盖 `function_call_output`、`custom_tool_call_output`、`input_image`、`reasoning`、`web_search_call`、`image_generation_call`、`compaction` 等多个形态；Chat bridge 只覆盖其中能无损或低风险映射到 Chat Completions 的子集。
- 当前 mock AI 覆盖未配置映射的普通 Chat-only 账号拒绝 `/v1/responses`、普通 OpenAI v1 账号显式 `responses -> chat_completions` bridge、旧显式混合映射不会把 Responses 改写到 Chat；共享 bridge 的请求 / 响应转换细节也保留为混合供应商账户复用能力，包括 `stream_options.include_usage`、function tools、历史 `reasoning` / `function_call` 归一化、`previous_response_id`、`/responses/compact`、Chat reasoning/text delta 到 Responses 事件和错误事件转换。
- DeepSeek mock AI 已覆盖 bridge 的文本、`stream_options.include_usage`、request history `reasoning` -> DeepSeek `reasoning_content`、历史 `function_call/function_call_output` 成组归一化、交错工具输出保留、裸 `message role=tool` / dangling / orphan 工具历史丢弃、auto `web_search` guidance 且不命中上游、`previous_response_id` 成功续链、未知 previous 受控拒绝、`/responses/compact` 走内部 Chat Completions 摘要并返回 `compaction_summary`、compact item 后续回灌、function tool、input_image data URL、usage 映射，以及 `insufficient_system_resource` 在 Chat JSON / SSE 原样透传、在 bridge 中中性完成且不合成失败。
- 本地真实 CLI 回归已覆盖 Claude Code、Codex CLI、opencode -> 本地网关 -> 上游 mock 的基础链路。2026-06-24 新增 Codex CLI -> 本地网关 `/v1/responses` -> DeepSeek driver -> vsllm `deepseek-v4-flash` 的真实 marker 链路，Codex 携带 `x-codex-turn-metadata`，下游模型保留为 `gpt-5.3-codex`，上游映射到 Chat Completions 后成功返回并被 Codex 消费。
- 2026-06-24 真实 Codex CLI 工具链路验证：DeepSeek `deepseek-v4-flash` 编程任务中，Codex 成功识别 bridge 输出的 function call，并进入 `command_execution` 工具调用；最终任务失败是因为模型生成的 PowerShell 写文件命令被本地 Codex policy 拒绝，临时项目测试仍停留在 TODO。这证明协议层工具调用可被 Codex 识别，但不证明该上游模型具备稳定完成 Codex 编程任务的质量。
- 2026-06-24 真实 vsllm.com 调研：`/v1/models` 可列出 `glm-5.2`、`glm-5-turbo`、`glm-4.7-flash`、`deepseek-v4-flash`、`deepseek-v4-pro`、`deepseek-ai-v4-flash` 等模型。第一组通道下 `glm-5.2` / `glm-5-turbo` 返回上游鉴权失败，`deepseek-v4-flash` 返回欠费类错误，`deepseek-v4-pro` 返回无可用 provider 或限流；可用成功样本为 `glm-4.7-flash` 和 `deepseek-ai-v4-flash`。第二组通道下 `glm-5.2` / `glm-5-turbo` 仍返回上游鉴权失败，`deepseek-v4-flash` 基础 Chat 成功并返回 `content` + `reasoning_content`，流式工具调用成功且按多个 `delta.tool_calls[index].function.arguments` 分片输出参数，最终 `finish_reason=stop`；`deepseek-v4-pro` / `deepseek-ai-v4-flash` 返回当天额度耗尽。GLM `glm-4.7-flash` Chat JSON 返回 `reasoning_content` 和 `tool_calls`，GLM SSE 在默认 thinking 下先输出 `delta.reasoning_content`，补 `thinking.disabled + tool_stream=true` 后返回 `delta.tool_calls` 与 `finish_reason=tool_calls`。本次补充用第二组通道直连 `glm-4.7-flash` 强制 `custom__apply_patch`，真实 SSE 返回 `delta.tool_calls[0].function.name=custom__apply_patch`、arguments 为 `{"input":"print(\"vsllm custom bridge\")"}`、`finish_reason=tool_calls` 和 usage；同一账号下 `deepseek-v4-flash` 返回 Arrearage，`deepseek-ai-v4-flash` / `deepseek-v4-pro` 返回当天额度耗尽。
- 2026-06-24 使用当前 vsllm 账号直连 Chat function wrapper 探测：`glm-4.7-flash` 和 `deepseek-v4-flash` 在强制 `tool_choice=function:web_search` 时均返回 `finish_reason=tool_calls`、`tool_name=web_search` 和 JSON `query` 参数。该探测只证明模型能按普通 Chat function 形式请求一个名为 `web_search` 的客户端工具，不证明上游具备 Codex / Responses 原生联网搜索能力；网关不再把它包装成 Codex `web_search` 兼容能力。
- 2026-06-24 GLM Codex CLI 真实验证：本地 mock 上游已证明 GLM driver 输出的 Responses SSE 能被 Codex CLI 消费，完整上游 Chat body 也可以直打 vsllm 成功返回 marker；但真实网关经 vsllm GLM `glm-4.7-flash` 多次出现流式错误或 `429 code=1305`（模型访问量过大）。因此 GLM 当前只能认为协议策略已适配、mock 可重复验证通过，不能对该 vsllm 通道宣称真实 Codex CLI 稳定可用。
- 2026-06-24 guidance 修订复测：`test:glm-real-gateway-e2e` 使用当前 vsllm 账号和 `glm-4.7-flash` 两次复测，第一次 Chat JSON 成功但 bridge 在首个可见输出前收到 `upstream_retryable_error`，第二次 bridge 成功返回 `通过` 但 Chat JSON 返回上游 `503 service_unavailable` / 无可用账户；归类为 vsllm GLM 通道流式与路由不稳定，不是本地 guidance 或 bridge 协议回归。`test:deepseek-real-gateway-e2e` 使用当前 vsllm 账号和 `deepseek-v4-flash`，本地模型目录通过，Chat JSON 与 Chat SSE 均在 60 秒窗口内超时，Codex bridge 成功；因此 DeepSeek 本轮真实失败集中在普通 Chat 上游超时，不是 Responses -> Chat bridge 或 `web_search` guidance 回归。
- 2026-06-24 真实 Codex CLI guidance 演练：`test:codex-openai-compatible-glm-real-e2e` 新增 `JUHE_REAL_CODEX_EXPECT_GUIDANCE=1` 模式，使用当前 vsllm 账号分别验证 GLM `glm-4.7-flash`、DeepSeek `deepseek-v4-flash` 和通用 OpenAI-compatible `gpt-5.5`。Codex CLI 实际请求 `/v1/responses`，工具列表包含 `shell_command`、`update_plan`、`request_user_input`、`apply_patch`、`view_image`、`tool_search` 和 `web_search`，`tool_choice=auto`。三类 profile 均返回正常 agent guidance，Codex CLI 以 `item.completed` / `turn.completed` 消费 guidance，没有 `turn.failed`、`response.failed` 或旧 `unsupported_codex_native_tool`，且没有产生上游成功 usage；这证明 guidance 方案可以被真实本地 agent 消费。
- 2026-06-25 使用当前 vsllm 真实账户和 Codex CLI `0.141.0` 复测：`test:codex-openai-compatible-glm-real-e2e` 以 `gpt-5.3-codex -> gpt-5.4-mini` 路径通过 guidance 验收，Codex 请求仍携带 `shell_command`、`update_plan`、`request_user_input`、`apply_patch`、`view_image`、`tool_search` 和 `web_search`，客户端以 completed turn 消费 guidance。`test:cli-local-gateway` 的 marker mock 场景已显式禁用非 Chat 兼容工具，避免 Codex 在简单回显用例中触发 hosted tool guidance，从而继续覆盖 Responses -> Chat mock 上游 marker 链路。
- 2026-06-25 使用当前 vsllm 真实账户和 Codex CLI `0.141.0` 复测编程任务：`JUHE_REAL_CODEX_PROGRAMMING_TASK=1`、`JUHE_REAL_CODEX_PROGRAMMING_TASK_KIND=snake_html`、`JUHE_REAL_CODEX_PROGRAMMING_TEXT_ARTIFACT_FALLBACK=1`、`JUHE_REAL_CODEX_UPSTREAM_MODEL=gpt-5.5` 通过；Codex 请求本地 `/v1/responses`，网关转真实 Chat 上游，最终生成 `index.html`、`styles.css`、`game.js` 并通过本地源码检查。真实测试暴露两类工程边界并已修复：文本产物 parser 需要兼容 `apply_patch` 文本和 `--- filename ---` 标题分隔格式；真实上游偶发 SSE 在输出前断开，因此脚本新增 `JUHE_REAL_CODEX_REQUEST_MAX_RETRIES` / `JUHE_REAL_CODEX_STREAM_MAX_RETRIES`，默认仍为 0，真实联调可显式设为 1。

## 当前限制

- 非 2xx Chat 上游错误当前仍走现有网关错误处理和统一失败响应，不在桥接层主动包装成 Responses `response.failed`。
- 首版只支持流式 Codex 请求，不承接非流式 `/v1/responses`。
- 当前支持能映射到 Chat function tool 的 `function/custom/namespace function`。`web_search`、MCP tool、tool search、local shell call、image generation 和 computer use 必须由上游原生能力或调用方本地 agent / MCP 配置承接；Chat-only bridge 不会在网关内伪造这些结果，缺少真实能力时返回正常 agent guidance。
- `previous_response_id` 当前只在 Chat-only bridge 的本网关 file-backed 状态层内有效；跨网关、跨 API Key、跨分组、跨供应商或 7 天未使用过期后都会受控失败。
- Gateway summary compact 当前已使用本网关 compact snapshot；它只能恢复为 Chat summary，不能宣称为原生 Responses opaque compact，也不能跨网关、跨 API Key、跨分组或跨供应商使用。
- 当前只把 `reasoning_content` 映射成 reasoning summary；不支持 encrypted reasoning 的生成，也不承诺和原生 OpenAI reasoning state 等价。
- 如果模型频繁只输出 reasoning 而没有 `content`，Codex bridge 会只有 reasoning item、没有普通 assistant 文本；需要用模型参数、提示词或供应商策略处理。
- GLM 在 vsllm 通道上的真实 Codex CLI 可用性受上游路由、限流和流式错误影响较大；即使协议体可直打成功，网关实测仍可能收到上游 SSE error 或 `429 code=1305`。
- input image 只做 data URL / URL 到 Chat `image_url` content part 的结构转换；没有覆盖 Codex App 图片视图、image generation output item 或非视觉模型的受控降级。

## 后续计划

- 把 bridge 标记写入审计摘要，便于排障区分下游 `/responses` 和上游 `/chat/completions`。
- 补充 bridge 标记的结构化审计字段和后台探针筛选条件，避免只靠路径和日志文本判断是否走了桥接。
- 补充 Codex bridge 的真实 CLI 工具调用场景，覆盖真实 Codex CLI 产生 `function_call_output` 后的下一轮请求。
- 为 gateway summary compact 增加更严格的结构化摘要 schema 校验、最近未压缩历史保留、内部请求 purpose 标记和完整用量记录。
- 增加 Chat JSON -> Responses JSON 的非流式桥接，前提是有明确客户端需求和真实验证。
