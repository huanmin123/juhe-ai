# Codex Responses 转 Chat 协议转换设计

## 范围

本文记录项目内“Codex 客户端 Responses 请求 -> 上游 OpenAI-compatible Chat Completions”的通用转换层。该能力不是 GLM 专属：GLM Coding Plan 是第一批启用者，后续 DeepSeek、Kimi 或其他只支持 Chat Completions 的 OpenAI-compatible 上游如需承接 Codex 客户端，也应复用同一转换层，再在供应商 driver 内按自身协议细节做少量配置。

当前实现落点：

- 通用转换层：`backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`
- GLM 启用点：`backend/src/modules/providers/drivers/glm/driver.ts`
- DeepSeek 启用点：`backend/src/modules/providers/drivers/deepseek/driver.ts`

## 设计原则

- 协议转换是共享基础设施，不写死在单个供应商目录下。
- 供应商 driver 只负责判断是否启用、默认模型、模型映射、上游路径和供应商级细节。
- 账户能力仍按真实上游能力保存。只支持 Chat 的上游账户不伪装成 `responses_sse`，桥接层只是让特定客户端请求在调度时要求账户具备 `chat_sse`。
- 当前 bridge 只承接 Codex 客户端的流式 `/v1/responses` 主路径，不实现完整 OpenAI Responses API。
- 原生 Responses / OpenAI OAuth Codex 路径和 Chat-only bridge 是两条不同能力线：前者可以透传 `/responses/compact` 等原生能力，后者必须由网关维护 `previous_response_id` 状态和自有 compact envelope，不能要求 Chat 上游认识 Responses 状态。

## 启用条件

通用桥接层只有在供应商 driver 显式开启时才生效。当前启用范围：

- GLM Coding：`profile_glm_coding_openai_v1`。
- DeepSeek OpenAI v1：`profile_deepseek_openai_v1`。

共同启用条件：

- 账户 `client_compatibility` 显式保存为 `codex_responses`；如果保存为 `openai_standard`，即使供应商 driver 支持 bridge 也不启用桥接。
- 下游请求是 `POST /responses` 或 `POST /v1/responses`。
- 请求被客户端画像识别为 `codex_responses`。
- 下游请求是流式请求，即 `stream=true` 且 `Accept: text/event-stream` 语义成立。
- 账户真实 endpoint mode 支持 `chat_sse`。

通用 GLM API 档案不启用桥接；GLM Coding / DeepSeek 账户保存为 OpenAI 标准时不启用桥接；普通 OpenAI-compatible 客户端发起 `/v1/responses` 也不进入桥接。

## 请求转换

下游 Responses 请求会被改写为 Chat Completions SSE 请求：

| Responses 字段 | Chat 字段 | 当前规则 |
| --- | --- | --- |
| `model` | `model` | 先用账户模型映射结果，再用请求模型，最后用供应商默认模型 |
| `instructions` | `messages[0].role=system` | 非空时注入 system 消息 |
| `input` 字符串 | `messages[].role=user` | 直接作为用户消息 |
| `input[].type=message` | `messages[]` | `user/assistant/system/developer` 映射为 Chat role，`developer` 降级为 `system`；`role=tool` 不作为普通 message 透传 |
| `input[].type=reasoning` | DeepSeek `reasoning_content` | 仅 DeepSeek bridge 启用；GLM 等普通 Chat 上游不透传该专有字段 |
| `input[].type=function_call` | assistant `tool_calls` | 用于把 Codex 上一轮工具调用历史还原给 Chat 上游；连续调用会合并为同一个 assistant tool_calls 消息 |
| `input[].type=function_call_output` | `role=tool` | 用 `call_id` 关联工具结果；结构化 output 只抽取文本类 content item |
| `content[].type=input_image` | Chat `image_url` content part | 保留 `image_url` / data URL 和 `detail`；实际是否可用取决于上游模型视觉能力 |
| `tools[].type=function` | Chat `tools[].type=function` | 首版只透传 function tools |
| `tool_choice` | `tool_choice` | 支持字符串和指定 function |
| `parallel_tool_calls` | `parallel_tool_calls` | 布尔值原样透传 |
| `max_output_tokens` | `max_tokens` | 同时兼容 `max_completion_tokens` |

历史工具调用会先做 Chat 消息不变量归一化：一个 assistant `tool_calls` 消息后面必须紧跟对应 `tool` 输出，夹在 `function_call` 和 `function_call_output` 之间的 `developer/message` 会延后到工具输出之后；并行工具调用即使出现 `output A -> developer -> output B` 交错，也会等已登记工具输出归齐后再写入 Chat history。没有输出的 dangling `function_call`、找不到调用的 orphan `function_call_output` 和普通 `message role=tool` 会丢弃，避免把上游很容易 400 的非法 Chat history 暴露给客户端。

当前实现不透传 `web_search`、namespace tool、custom tool、MCP tool、tool search、local shell call、image generation、Responses `include`、`store`、`truncation` 和 `context_management` 等原生 Responses 语义。`previous_response_id` 已由 Chat-only bridge 的网关状态层在服务端消费：首轮成功后保存 response 状态，后续同 API Key、同分组、同供应商 profile 的请求可恢复历史并追加本轮 input；找不到、过期或跨边界时受控失败，不命中上游。`/responses/compact` 已支持网关摘要压缩：网关还原上下文后，在当前分组、当前供应商内发起内部 Chat Completions 摘要请求，再返回 Codex 可反序列化的 `compaction_summary`。该能力不是上游原生 Responses compact，也不能跨网关或跨供应商消费。

请求头会清理 Codex / OpenAI 专属元数据，例如 `openai-beta`、`originator`、`session-id`、`thread-id`、`x-client-request-id`、`x-codex-*`，并把上游 `accept` 固定为 `text/event-stream`。

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
- 最终发出 `response.output_item.done`，item 为 `type=function_call`。
- 不依赖 `response.function_call_arguments.delta`。

文本输出策略：

- Chat `delta.content` 和 `delta.refusal` 转为 `response.output_text.delta`。
- Chat `delta.reasoning_content` / `delta.reasoning` 转为 Codex 可消费的 `reasoning` output item 和 `response.reasoning_summary_text.delta`，不混入普通文本。
- 当前不生成 `custom_tool_call`、`local_shell_call`、`web_search_call`、`image_generation_call`、`compaction` 等 output item。

usage 策略：

- Chat `usage.prompt_tokens` -> Responses `usage.input_tokens`
- Chat `usage.completion_tokens` -> Responses `usage.output_tokens`
- Chat `usage.total_tokens` -> Responses `usage.total_tokens`
- Chat `usage.completion_tokens_details.reasoning_tokens` -> Responses `usage.output_tokens_details.reasoning_tokens`
- 无 usage 时桥接层会基于请求和输出文本生成估算 usage，避免 Codex completed 事件缺少基础用量对象。

## 供应商自定义点

后续供应商接入同一桥接层时，只应在 driver 中配置：

- 是否启用桥接，例如仅 Coding 档案启用，通用 API 不启用。
- 默认模型，例如 GLM 使用 `glm-5.2`。
- 模型映射后的上游模型。
- Base URL 和路径拼接，例如 `/responses` 改为 `/chat/completions` 时是否追加 `/v1`。
- 供应商特有 role、参数、usage 和错误语义。
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
- Chat-only bridge 当前实现：由网关托管 `previous_response_id` 对应的 Responses input/output 增量状态；状态关系索引写入 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` 下的 Codex Responses 上下文索引 shard，完整上下文 payload 按 session/hour 追加到 `JUHE_AI_CODEX_CONTEXT_ROOT` 下的 gzip segment，不进入业务库。compact 时先还原完整上下文，再在当前分组和当前供应商内调度非流式 Chat Completions 摘要请求，保存 compact snapshot，并返回 `type=compaction_summary`、`encrypted_content=juhecmp.v2.<compact_id>.<digest>` 的网关自有 envelope。后续请求带回该 item 时，bridge 校验边界、TTL 和 digest 后读取 snapshot，再恢复为 Chat system summary。
- Chat-only compact snapshot 仍然是网关自有 compact，不等价于上游原生 Responses compact；它只能在本网关、同 API Key / 分组 / 供应商档案边界内恢复。
- 供应商自有压缩能力：如果供应商提供非 Responses 的 Chat 侧 `/compact` 或等价摘要接口，可以作为 Chat-only gateway compact 的摘要来源，但返回结果仍要落到网关 compact snapshot；通用兜底必须走 Chat Completions，即 OpenAI-compatible 语义下的 `/v1/chat/completions`，不能把已转换为 Chat 的压缩请求继续发给上游 `/v1/responses/compact`。只有供应商明确提供可被后续原生 `/responses` 直接消费的 compact output，才属于原生 Responses compact。

### `previous_response_id` 目标处理

Chat 上游通常不认识 `previous_response_id`。目标设计中，该字段只在网关内消费：

1. 首轮 bridge `/responses` 成功后，网关保存 `response_id`、授权边界、供应商 profile、Responses input/output items、归一化 Chat messages、工具状态和 TTL。
2. 后续请求带 `previous_response_id` 时，网关校验 API Key / team / group / provider profile 是否一致，再恢复历史并追加本轮 input。
3. 恢复后的完整上下文重新进入 Responses -> Chat 转换，Chat 上游只收到普通 `messages`、`tools` 和供应商允许的字段。
4. 状态找不到、过期、跨分组、跨供应商或工具状态不完整时，返回受控错误，不把缺失历史的请求发给上游。

### Gateway summary compact 目标处理

Chat-only compact 的摘要模型只能在当前请求授权边界内选择：

- 当前 API Key 绑定的当前分组。
- 当前请求命中的当前供应商和 provider profile。
- 同一套候选账号筛选、账号冷却、切号、统计、审计和错误处理。
- 通用摘要请求固定使用 Chat Completions endpoint family；上游不能收到 `/responses/compact`。
- 内部摘要请求必须设置 `disableCompact = true`，避免递归 compact。

摘要模型可以是供应商非 Responses 的专用 compact endpoint、专用摘要模型或普通 Chat 模型。当前实现使用普通非流式 Chat Completions，并校验返回摘要必须是非空文本。客户端只看到 Codex 可识别的 `compaction_summary` item；后续请求带回该 item 时，网关按 compact id 读取 snapshot 并恢复为 Chat summary。

## 已验证结论

- 已用 Codex 源码确认：普通 function call 应通过最终 `response.output_item.done` 的 `type=function_call` item 交给 Codex；`response.function_call_arguments.delta` 当前不被普通 function call 路径消费。
- 已用 Codex 源码确认：Codex input / output item 覆盖 `function_call_output`、`custom_tool_call_output`、`input_image`、`reasoning`、`web_search_call`、`image_generation_call`、`compaction` 等多个形态；Chat bridge 只覆盖其中能无损或低风险映射到 Chat Completions 的子集。
- GLM mock AI 已覆盖显式 `client_compatibility=codex_responses` 时 `/v1/responses` 改写到 `/chat/completions`、function tools 透传、web_search 丢弃、历史 `function_call/function_call_output` 成组归一化、交错工具输出保留、裸 `message role=tool` / dangling / orphan 工具历史丢弃、`previous_response_id` 成功续链、未知 previous 受控拒绝、`/responses/compact` 走内部 Chat Completions 摘要并返回 `compaction_summary`、compact item 后续回灌、Chat text delta 转 Responses text delta、Chat tool_calls 转 Responses function_call item；同时覆盖 GLM Coding 账号选择 `openai_standard` 时拒绝 Codex bridge 且不命中上游。
- DeepSeek mock AI 已覆盖 Codex bridge 的文本、request history `reasoning` -> DeepSeek `reasoning_content`、历史 `function_call/function_call_output` 成组归一化、交错工具输出保留、裸 `message role=tool` / dangling / orphan 工具历史丢弃、`previous_response_id` 成功续链、未知 previous 受控拒绝、`/responses/compact` 走内部 Chat Completions 摘要并返回 `compaction_summary`、compact item 后续回灌、function tool、input_image data URL 和 usage 映射。
- 本地真实 CLI 回归已覆盖 Claude Code、Codex CLI、opencode -> 本地网关 -> 上游 mock 的基础链路，但仍只是基础文本链路，不代表完整 Codex 协议面。
- 真实 vsllm.com 已验证 `glm-5.2`、`glm-5-turbo`、`deepseek-v4-flash` 和 `deepseek-v4-pro`：Chat 和 Codex bridge 主路径均可通。

## 当前限制

- 非 2xx Chat 上游错误当前仍走现有网关错误处理和统一失败响应，不在桥接层主动包装成 Responses `response.failed`。
- 首版只支持流式 Codex 请求，不承接非流式 `/v1/responses`。
- 当前不支持 Responses namespace tools、web_search、custom tool、MCP tool、tool search、local shell call、image generation 和 computer use。
- `previous_response_id` 当前只在 Chat-only bridge 的本网关 file-backed 状态层内有效；跨网关、跨 API Key、跨分组、跨供应商或 7 天未使用过期后都会受控失败。
- Gateway summary compact 当前已使用本网关 compact snapshot；它只能恢复为 Chat summary，不能宣称为原生 Responses opaque compact，也不能跨网关、跨 API Key、跨分组或跨供应商使用。
- 当前只把 `reasoning_content` 映射成 reasoning summary；不支持 encrypted reasoning 的生成，也不承诺和原生 OpenAI reasoning state 等价。
- 如果模型频繁只输出 reasoning 而没有 `content`，Codex bridge 会只有 reasoning item、没有普通 assistant 文本；需要用模型参数、提示词或供应商策略处理。
- input image 只做 data URL / URL 到 Chat `image_url` content part 的结构转换；没有覆盖 Codex App 图片视图、image generation output item 或非视觉模型的受控降级。

## 后续计划

- 把 bridge 标记写入审计摘要，便于排障区分下游 `/responses` 和上游 `/chat/completions`。
- 补充 bridge 标记的结构化审计字段和后台探针筛选条件，避免只靠路径和日志文本判断是否走了桥接。
- 补充 Codex bridge 的真实 CLI 工具调用场景，覆盖真实 Codex CLI 产生 `function_call_output` 后的下一轮请求。
- 为 gateway summary compact 增加更严格的结构化摘要 schema 校验、最近未压缩历史保留、内部请求 purpose 标记和完整用量记录。
- 增加 Chat JSON -> Responses JSON 的非流式桥接，前提是有明确客户端需求和真实验证。
