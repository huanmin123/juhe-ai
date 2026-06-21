# Codex Responses 转 Chat 协议转换设计

## 范围

本文记录项目内“Codex 客户端 Responses 请求 -> 上游 OpenAI-compatible Chat Completions”的通用转换层。该能力不是 GLM 专属：GLM Coding Plan 是第一批启用者，后续 DeepSeek、Kimi 或其他只支持 Chat Completions 的 OpenAI-compatible 上游如需承接 Codex 客户端，也应复用同一转换层，再在供应商 driver 内按自身协议细节做少量配置。

当前实现落点：

- 通用转换层：`backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`
- GLM 启用点：`backend/src/modules/providers/drivers/glm/driver.ts`

## 设计原则

- 协议转换是共享基础设施，不写死在单个供应商目录下。
- 供应商 driver 只负责判断是否启用、默认模型、模型映射、上游路径和供应商级细节。
- 账户能力仍按真实上游能力保存。只支持 Chat 的上游账户不伪装成 `responses_sse`，桥接层只是让特定客户端请求在调度时要求账户具备 `chat_sse`。
- 第一版只承接 Codex 客户端的流式 `/v1/responses` 主路径，不实现完整 OpenAI Responses API。

## 启用条件

通用桥接层只有在供应商 driver 显式开启时才生效。当前 GLM 的启用条件为：

- 账户协议档案是 `profile_glm_coding_openai_v1`。
- 账户 `client_compatibility` 显式保存为 `codex_responses`；如果保存为 `openai_standard`，即使是 GLM Coding 档案也不启用桥接。
- 下游请求是 `POST /responses` 或 `POST /v1/responses`。
- 请求被客户端画像识别为 `codex_responses`。
- 下游请求是流式请求，即 `stream=true` 且 `Accept: text/event-stream` 语义成立。
- 账户真实 endpoint mode 支持 `chat_sse`。

通用 GLM API 档案不启用桥接；GLM Coding 账户保存为 OpenAI 标准时不启用桥接；普通 OpenAI-compatible 客户端发起 `/v1/responses` 也不进入桥接。

## 请求转换

下游 Responses 请求会被改写为 Chat Completions SSE 请求：

| Responses 字段 | Chat 字段 | 当前规则 |
| --- | --- | --- |
| `model` | `model` | 先用账户模型映射结果，再用请求模型，最后用供应商默认模型 |
| `instructions` | `messages[0].role=system` | 非空时注入 system 消息 |
| `input` 字符串 | `messages[].role=user` | 直接作为用户消息 |
| `input[].type=message` | `messages[]` | `user/assistant/system/developer/tool` 映射为 Chat role，`developer` 降级为 `system` |
| `input[].type=function_call` | assistant `tool_calls` | 用于把 Codex 上一轮工具调用历史还原给 Chat 上游 |
| `input[].type=function_call_output` | `role=tool` | 用 `call_id` 关联工具结果 |
| `tools[].type=function` | Chat `tools[].type=function` | 首版只透传 function tools |
| `tool_choice` | `tool_choice` | 支持字符串和指定 function |
| `parallel_tool_calls` | `parallel_tool_calls` | 布尔值原样透传 |
| `max_output_tokens` | `max_tokens` | 同时兼容 `max_completion_tokens` |

首版不透传 `web_search`、namespace tool、custom tool、image generation、Responses `include`、`store`、`truncation`、`context_management` 和 `/responses/compact` 语义。这些能力需要单独补字段映射、事件映射和验证矩阵。

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
- Chat `delta.reasoning_content` 首版不转为普通文本，避免把供应商推理字段泄露到 Codex 普通输出。
- 如果后续要展示 reasoning，应新增 Responses `reasoning` item 和 `response.reasoning_text.delta` 映射，并单独验证 Codex UI 展示和审计边界。

usage 策略：

- Chat `usage.prompt_tokens` -> Responses `usage.input_tokens`
- Chat `usage.completion_tokens` -> Responses `usage.output_tokens`
- Chat `usage.total_tokens` -> Responses `usage.total_tokens`
- Chat `usage.completion_tokens_details.reasoning_tokens` -> Responses `usage.output_tokens_details.reasoning_tokens`
- 无 usage 时允许 `usage=null`，由现有 usage 估算和审计链路兜底。

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

## 已验证结论

- 已用 Codex 源码确认：普通 function call 应通过最终 `response.output_item.done` 的 `type=function_call` item 交给 Codex；`response.function_call_arguments.delta` 当前不被普通 function call 路径消费。
- GLM mock AI 已覆盖显式 `client_compatibility=codex_responses` 时 `/v1/responses` 改写到 `/chat/completions`、function tools 透传、web_search 丢弃、Chat text delta 转 Responses text delta、Chat tool_calls 转 Responses function_call item；同时覆盖 GLM Coding 账号选择 `openai_standard` 时拒绝 Codex bridge 且不命中上游。
- 真实 vsllm.com 已验证 `glm-5.2` 和 `glm-5-turbo`：Chat 和 Codex bridge 均可通；`glm-5.2-free` 在 60 秒窗口内 Chat 与 bridge 均超时。

## 当前限制

- 非 2xx Chat 上游错误当前仍走现有网关错误处理和统一失败响应，不在桥接层主动包装成 Responses `response.failed`。
- 首版只支持流式 Codex 请求，不承接非流式 `/v1/responses`。
- 首版不支持 Responses namespace tools、web_search、custom tool、image generation、computer use 和 `/responses/compact`。
- 首版不把 `reasoning_content` 转成 Codex reasoning item，只避免它泄露到普通文本。
- 如果模型频繁只输出 reasoning 而没有 `content`，Codex bridge 会出现空文本；需要通过模型参数、提示词或后续 reasoning item 映射解决。

## 后续计划

- 把 bridge 标记写入审计摘要，便于排障区分下游 `/responses` 和上游 `/chat/completions`。
- 为 DeepSeek 评估同一 bridge 的启用档案、默认模型、reasoning 字段和工具调用差异。
- 增加 provider-level transform option，允许特定供应商把 reasoning 映射成 Responses reasoning item。
- 增加 Chat JSON -> Responses JSON 的非流式桥接，前提是有明确客户端需求和真实验证。
