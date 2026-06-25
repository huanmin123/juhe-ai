# Anthropic Messages 转 Chat Completions 协议转换设计

## 定位

本文记录 Anthropic v1 `/messages` 下游请求桥接到 OpenAI v1 `/chat/completions` 上游的长期设计。它用于 Anthropic-compatible / Claude Code 类客户端显式访问真实 Chat Completions 上游，例如 DeepSeek OpenAI-compatible、GLM OpenAI Chat、GPT API Key 或通用 OpenAI-compatible 账号。

该能力只通过账号模型映射显式触发，不作为自动猜测路由：

```text
sourceModel + sourceEndpointFamily=messages
  -> upstreamModel + upstreamEndpointFamily=chat_completions
```

## 调研依据

- OpenAI Chat Completions 官方契约：`POST /v1/chat/completions` 使用 `messages`、`tools`、`tool_choice`，流式返回 `chat.completion.chunk`。
- Anthropic Messages 官方契约：`POST /v1/messages` 使用顶层 `system`、`messages[].content` content blocks、`tools`、`tool_choice`，流式返回 `message_start`、`content_block_start`、`content_block_delta`、`message_delta`、`message_stop`。
- Anthropic tool use 契约：模型工具调用为 assistant `tool_use` content block，工具结果由 user `tool_result` content block 传回。

官方参考：

- [OpenAI Chat Completions API](https://api.openai.com/v1/chat/completions)
- [Anthropic Messages API](https://docs.anthropic.com/en/api/messages)
- [Anthropic tool use](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/overview)

## 范围边界

### 支持

- Anthropic Messages JSON 请求转上游 Chat Completions JSON。
- Anthropic Messages SSE 请求转上游 Chat Completions SSE，并把 Chat SSE 还原为 Anthropic SSE。
- 文本、多轮 user / assistant、顶层 `system`。
- Anthropic image content block 转 Chat `image_url`，包含 base64 data URL 和 URL source。
- Anthropic client tool：`tools[].name/description/input_schema` 转 Chat function tool。
- Anthropic `tool_choice` 的 `auto`、`any`、`tool`、`none` 转 Chat `tool_choice`。
- assistant `tool_use` 历史转 Chat `tool_calls`。
- user `tool_result` 历史转 Chat `tool` message。
- Chat JSON / SSE 的文本、function tool call、usage、finish reason 还原为 Anthropic message / stream。

### 不支持但必须受控处理

- `messages -> responses` 不支持，不保存、不路由、不合成 Responses。
- Anthropic `top_k`、`thinking`、`mcp_servers`、`container`、`context_management` 没有 Chat Completions 等价语义，不伪造。
- Anthropic `cache_control` 依赖原生 prompt caching 语义，Chat Completions 桥接不能静默丢弃；命中时返回 guidance，引导客户端移除或切换原生支持上游。
- Anthropic server tool、web search、code execution、computer use、MCP 运行时不能靠字段转换实现。若客户端需要这些能力，应由客户端 agent 自行寻找本地 MCP / 工具执行器、切换到原生支持的上游，或移除该能力。
- `document`、`audio`、未知 content block、未知 image source 不发送给 Chat 上游。
- Chat 多候选 `n > 1` 不还原为多个 Anthropic message；Messages 语义只返回单条 assistant message。

## 触发条件

请求必须同时满足：

1. 下游请求是 Anthropic native `POST /v1/messages`。
2. 选中账号使用 OpenAI 协议档案，且账号模型映射命中 `sourceEndpointFamily = messages`。
3. 映射目标为 `upstreamEndpointFamily = chat_completions`。
4. JSON 请求要求账号真实 endpoint mode 包含 `chat_json`；SSE 请求要求包含 `chat_sse`。

不满足这些条件时不尝试桥接。Anthropic 原生账号继续走 `messages -> messages` native 透传；Chat-only 账号只有显式映射才承接 Messages 请求。

## 请求字段映射

| Anthropic Messages | Chat Completions | 说明 |
| --- | --- | --- |
| `model` | `model` | 优先使用账号映射的 `upstreamModel`。 |
| `system` | 首条 `system` message | 字符串直接转；text block 拼接；其他 block 走 guidance。 |
| `messages[].role=user` | `role=user` / `role=tool` | 普通文本 / 图片留在 user；`tool_result` 转 Chat tool message。 |
| `messages[].role=assistant` | `role=assistant` | text 转 `content`；`tool_use` 转 `tool_calls`。 |
| text block | text content | 多文本 block 用换行拼接。 |
| image block | `image_url` content part | base64 转 `data:<media_type>;base64,...`；URL 直接透传。 |
| `tool_result` | `role=tool` | `tool_use_id` 转 `tool_call_id`。 |
| `max_tokens` | `max_tokens` | 保持 Chat-compatible 通用字段。 |
| `stop_sequences` | `stop` | 保持数组或字符串。 |
| `temperature` / `top_p` / `stream` | 同名字段 | 原样转。 |
| `metadata.user_id` | `user` | 仅传稳定 user id。 |
| `tools[]` | `tools[].type=function` | `input_schema` 转 `parameters`。 |
| `tool_choice.auto` | `auto` | Chat 允许模型自行决定。 |
| `tool_choice.any` | `required` | Chat 要求至少一个工具调用。 |
| `tool_choice.tool` | 指定 function | 指定函数名。 |
| `tool_choice.none` | `none` | 禁止工具。 |
| `tool_choice.disable_parallel_tool_use` | `parallel_tool_calls=false` | Chat 支持时关闭并行工具调用。 |

## 响应字段映射

### JSON

| Chat Completions | Anthropic Messages | 说明 |
| --- | --- | --- |
| `id` | `id` | 若缺失则生成 `msg_chat_bridge_*`。 |
| `model` | `model` | 使用上游响应模型；缺失时回退映射目标模型。 |
| `choices[0].message.content` | `content[].text` | 空字符串也保留为空文本块。 |
| `choices[0].message.refusal` | `content[].text` + `stop_reason=refusal` | 当 Chat 上游用 refusal 字段承载拒答文本时转为 Anthropic 文本块。 |
| `choices[0].message.tool_calls[]` | `content[].tool_use` | `function.arguments` 解析为对象；无法解析时放入 `_raw`。 |
| `finish_reason=stop` | `stop_reason=end_turn` | 标准结束。 |
| `finish_reason=length` | `stop_reason=max_tokens` | 输出上限。 |
| `finish_reason=tool_calls` | `stop_reason=tool_use` | 工具调用。 |
| `usage.prompt_tokens` | `usage.input_tokens` | 缺失按 0。 |
| `usage.completion_tokens` | `usage.output_tokens` | 缺失按 0。 |
| `prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` | 仅有值时保留。 |

### SSE

Chat SSE 转 Anthropic SSE 的顺序固定为：

1. `message_start`
2. 首次文本 delta 时发 `content_block_start`，随后发 `content_block_delta` / `text_delta`
3. 首次 tool call delta 时发 `content_block_start`，参数增量发 `content_block_delta` / `input_json_delta`
4. Chat finish chunk 到达时记录 `stop_reason`，继续接收可能位于尾部的 `stream_options.include_usage` usage chunk
5. Chat `[DONE]` 或上游正常结束时关闭所有 content block，发送 `message_delta`
6. 最后发送 `message_stop`

Chat `[DONE]` 不透传给 Anthropic 客户端；它只作为上游流结束信号。如果上游 SSE error 在尚未完成时出现，转换为 Anthropic `event: error`，不伪装成成功 message。非流式上游如果返回非法 JSON 或过大 body，桥接层返回稳定 Anthropic error JSON，不让 body 转换异常扩散为网关 500。

## 不支持能力的返回策略

不支持的上游能力缺口不能变成 500，也不能假装执行成功。桥接层按两类处理：

- 请求结构非法、JSON 无效、必填字段缺失、role 顺序不合法：返回下游协议错误。
- 上游不具备等价能力，例如 `thinking`、`cache_control`、`mcp_servers`、server tool、未知 content block：返回 Anthropic message / stream 形态的正常 guidance，文本说明当前上游不支持该能力，并引导客户端 agent 改用本地工具执行器、MCP 或切换支持该能力的上游。

guidance 文本不写死具体客户端名称，只描述能力缺口和可执行下一步，便于任意 agent 消费。

## 模型映射约束

- `sourceEndpointFamily` 允许 `messages`。
- `messages -> chat_completions` 只允许 OpenAI 协议档案账号。
- `messages -> responses` 禁止。
- `messages -> messages` 不作为账号映射保存；Anthropic 原生直连由协议档案承接。
- `sourceModel` 为 Anthropic 协议客户端可见模型池中的模型。
- `upstreamModel` 为当前 OpenAI 协议账号可用模型池中的模型；通用 OpenAI-compatible 账号可从 OpenAI 协议聚合模型池选择。

## 验证要求

新增或修改该桥接时至少验证：

- JSON 文本请求：system、user、assistant 转 Chat body，Chat JSON 转 Anthropic JSON。
- SSE 文本流：Chat chunks 转 `message_start/content_block_delta/message_delta/message_stop`。
- 工具调用：Anthropic `tools/tool_choice/tool_result` 与 Chat `tools/tool_calls/tool` 互转。
- 图片输入：Anthropic base64 / URL image 转 Chat `image_url`。
- error/guidance：`thinking`、`cache_control`、`top_k`、`mcp_servers`、未知 content block、非法 Chat JSON body 返回受控 guidance 或协议错误，不返回 500。
- 路由与能力：OpenAI-compatible、DeepSeek、GLM、GPT Chat 上游在显式映射下可承接 `/v1/messages`；无映射时仍不误接。
- 回归：既有 `responses -> chat_completions`、`chat_completions -> responses`、`chat_completions|responses -> messages` 不受影响。

当前专项回归入口：

- `pnpm --dir backend test:anthropic-openai-chat-bridge-mock`：验证 bridge helper 层的请求 / 响应 / SSE / guidance 转换。
- `pnpm --dir backend test:anthropic-openai-chat-gateway-mock`：验证 OpenAI-compatible、GPT、DeepSeek、GLM 四类 OpenAI 协议账号通过显式映射承接 `/v1/messages`，并检查上游路径、headers、usage / audit 和 guidance。
- `pnpm --dir backend test:anthropic-openai-chat-real`：使用真实 OpenAI-compatible 上游抽样验证 Messages JSON、Messages SSE、强制 function tool、unsupported `thinking` guidance、usage 和 audit；图片真实用例需确认上游模型支持后通过 `JUHE_REAL_ANTHROPIC_OPENAI_CHAT_RUN_IMAGE=1` 单独开启。
