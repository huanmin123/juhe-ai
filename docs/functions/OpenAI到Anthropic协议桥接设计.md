# OpenAI 到 Anthropic Messages 协议桥接设计

## 1. 背景

当前系统已经具备三类相关能力：

- Anthropic API Key 原生中转：下游按 Anthropic Messages 协议请求 `/v1/messages`，上游按 Anthropic Messages 透传。
- OpenAI 协议内部桥接：Codex `/v1/responses` 可以在显式条件下转为上游 OpenAI-compatible `/v1/chat/completions`。
- 账号级模型映射：在 API Key 已授权账号池内，用 `sourceModel + sourceEndpointFamily` 映射到当前账号真实 `upstreamModel + upstreamEndpointFamily`。

现有缺口是：当下游是 Codex、OpenAI SDK、OpenAI-compatible 客户端或混合智能路由入口，而混合路由最终选中了 Anthropic Messages 账号时，OpenAI Chat / Responses 与 Anthropic Messages 协议不兼容。仅做 `responses -> chat_completions` 不能解决这个问题，因为 Anthropic 原生上游只承接 `/v1/messages`。

本设计把该能力作为长期主线能力处理：新增受控的 OpenAI-compatible 到 Anthropic Messages 桥接层，覆盖 Chat / Responses 的非流式 JSON 和 SSE 流式四类入口。

高级能力如 hosted tools、thinking、图片文件、structured output、MCP、computer、code execution、image_generation 和 compact 的长期策略，统一以 [OpenAI 到 Anthropic 高兼容能力矩阵](OpenAI到Anthropic高兼容能力矩阵.md) 为准。本文件保留基础桥接和四入口协议互转边界。

## 2. 总体结论

OpenAI 到 Anthropic Messages 桥接可以长期支持，但必须按显式桥接能力处理，不能把它伪装成 Anthropic 账号原生支持 OpenAI 协议。

本次目标支持四类下游形态：

| 下游入口 | 下游传输 | 上游实际端点 | 上游传输 | 下游返回 |
| --- | --- | --- | --- | --- |
| `POST /v1/chat/completions` | JSON | `POST /v1/messages` | JSON | Chat Completions JSON |
| `POST /v1/chat/completions` | SSE | `POST /v1/messages` | SSE | Chat Completions SSE |
| `POST /v1/responses` | JSON | `POST /v1/messages` | JSON | Responses JSON |
| `POST /v1/responses` | SSE | `POST /v1/messages` | SSE | Responses SSE |

这四类都能转为 Anthropic 支持的 Messages 请求。限制是：桥接只承诺明确列出的 OpenAI 协议子集，不承诺完整 OpenAI Responses / Chat 全字段无损等价。遇到不能可靠映射的字段，必须返回本地可读错误或明确降级记录，不能静默丢弃导致客户端误判成功。

## 3. 设计原则

- 下游协议保持下游可见形态：Chat 请求必须返回 Chat 形态；Responses 请求必须返回 Responses 形态。
- 上游账号真实能力保持 Anthropic Messages：`supported_endpoint_modes` 仍保存 `messages_json`、`messages_sse`、`message_token_counting`，不新增伪造的 `chat_json`、`responses_sse`。
- 桥接必须显式触发：通过请求目标模型直接定位到 Anthropic Messages 档案，或通过账号模型映射声明 `chat_completions/responses -> messages`。
- 模型映射右侧仍以当前账号为边界：Anthropic 官方账号右侧模型来自 Anthropic 模型目录；DeepSeek / GLM Anthropic-compatible 档案右侧模型来自各自供应商目录。
- 混合智能路由只负责选目标模型和目标分组；目标分组能通过原生协议或本桥接承接当前下游协议时才可进入候选。
- OpenAI 下游本地错误、上游 Anthropic 错误和流内错误都要按下游协议渲染，不能把 Anthropic error shape 直接返回给 OpenAI 客户端。
- 使用记录和审计必须同时记录下游 endpoint family、上游实际 endpoint family、下游模型、实际上游模型、桥接类型和 usage 语义。

## 4. 触发条件

桥接只在这些条件同时满足时启用：

1. 下游请求是 `POST /v1/chat/completions`、`POST /chat/completions`、`POST /v1/responses` 或 `POST /responses`。
2. 当前 API Key 已授权的候选账号中存在 Anthropic v1 Messages 档案账号。
3. 请求目标模型能明确定位到该账号：
   - 账号模型映射显式声明 `sourceEndpointFamily = chat_completions|responses` 且 `upstreamEndpointFamily = messages`。
   - 或混合智能路由已把顶层 `model` 改写为当前 Anthropic Messages 档案可承接的目标模型。
   - 或普通路由下请求模型本身属于当前 Anthropic Messages 档案模型目录，并且 API Key 只在当前已绑定分组范围内命中该档案。
4. 账户 endpoint mode 满足传输要求：
   - 下游非流式 JSON 要求上游 `messages_json`。
   - 下游 SSE 要求上游 `messages_sse`。
5. 请求体 JSON 可解析，且没有命中本设计明确拒绝的 OpenAI 字段组合。

不允许的触发方式：

- 不允许仅凭 `clientCompatibility = codex_responses` 自动把任意 Anthropic 账号加入 OpenAI 请求候选。
- 不允许 API Key 未绑定 Anthropic 目标分组时通过桥接越权访问该账号。
- 不允许模型无法唯一定位时猜测 Anthropic 账号。
- 不允许把 Anthropic native `/v1/messages` 请求反向包装成 OpenAI Chat / Responses。

## 5. 请求转换矩阵

### 5.1 Chat Completions 到 Messages

| Chat 字段 | Anthropic Messages 字段 | 规则 |
| --- | --- | --- |
| `model` | `model` | 使用模型映射或混合路由后的上游模型 |
| `messages[].role=system` | 顶层 `system` | 多条 system 以空行合并 |
| `messages[].role=developer` | 顶层 `system` | 作为系统约束合并，标记来源为 developer |
| `messages[].role=user` | `messages[].role=user` | text / image content block 转换 |
| `messages[].role=assistant` | `messages[].role=assistant` | 普通文本转 text block；tool_calls 转 tool_use block |
| `messages[].role=tool` | `messages[].role=user` + `tool_result` | 用 `tool_call_id` 关联 Anthropic `tool_use_id` |
| `tools[].type=function` | `tools[]` | 转为 Anthropic tool schema |
| `tools[].type=web_search` / 搜索模型 | 本地 web search 预取 + system 上下文 | 配置本地执行器时强制搜索一次；未配置时本地受控失败 |
| `tool_choice` | `tool_choice` | 支持 `auto`、`none`、`required`、指定 function name |
| `max_completion_tokens` / `max_tokens` | `max_tokens` | 优先使用 OpenAI 显式值；缺失时由桥接层补默认上限 |
| `temperature`、`top_p`、`stop`、`metadata` | 同名或等价字段 | 能等价表达时透传 |
| `stream` | `stream` | 根据下游请求保持一致 |

Chat `response_format` 做受控结构化输出适配：

- `json_object` 可以追加系统约束，要求仅输出合法 JSON。
- `json_schema` 使用合成 Anthropic tool 和强制 `tool_choice` 生成结构化输出，再由网关做 JSON schema 子集二次校验。
- `json_schema` 校验通过时，合成工具不会暴露为 Chat `tool_calls`，只把 tool input 反渲染为 `message.content` JSON 字符串。
- `json_schema` 校验失败时，Chat JSON 返回合法 Chat Completion，`choices[0].message.content=null`，`choices[0].message.refusal` 携带 `openai_anthropic_bridge_structured_output_schema_mismatch` 和失败原因；Chat SSE 在流内输出 error frame。
- 不支持 `n > 1`、`logprobs`、音频输出、`modalities` 音频、OpenAI 私有缓存字段的等价能力；命中时返回本地受控错误或明确忽略并写审计，具体策略在实现阶段按风险选择。

Chat web search 做 L3 本地预取模拟：

- 官方 Chat Completions 搜索路径是 `gpt-5-search-api` 等专用搜索模型，语义上会先搜索再回答；它不是 Responses 的可选 hosted tool 循环。
- 当 Chat 请求显式携带 `web_search` / `web_search_preview` tool，或模型名命中搜索模型路径时，如果 `JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT` 配置了本地 HTTP search executor，网关先用最后一条用户文本提取 query 并执行一次搜索。
- 搜索结果作为 Anthropic system 上下文注入，不把 `web_search` 当 Anthropic client tool 发送。
- Chat JSON 返回普通 Chat Completion，并在 `choices[0].message.annotations` 写入 `url_citation`；Chat SSE 保持标准 `delta.content` 流和 `[n]` 文本引用标记，不输出 Responses 的 `web_search_call` item。
- 未配置本地执行器、无法提取 query 或执行器失败时，返回 OpenAI 形态本地错误，不请求 Anthropic。

### 5.2 Responses 到 Messages

| Responses 字段 | Anthropic Messages 字段 | 规则 |
| --- | --- | --- |
| `model` | `model` | 使用模型映射或混合路由后的上游模型 |
| `instructions` | 顶层 `system` | 作为本轮系统指令 |
| `input` 字符串 | `messages[].role=user` | 作为单条用户消息 |
| `input[].type=message` | `messages[]` / 顶层 `system` | `system/developer` 入 system；`user/assistant` 入 messages |
| `input[].content[].type=input_text` | text block | 保留文本 |
| `input[].content[].type=input_image` | image block | data URL / URL 按 Anthropic 支持的 image source 转换 |
| `input[].content[].type=input_file` | document block | 支持 `file_data` inline PDF / text、PDF `file_url` 和本地 Files resolver `file_id`；未知或无权文件本地失败 |
| `input[].type=function_call` | assistant `tool_use` | 保留 call id、name、arguments |
| `input[].type=function_call_output` | user `tool_result` | 用 `call_id` 关联工具结果 |
| `tools[].type=function` | Anthropic `tools[]` | function tools 转 Anthropic tool schema |
| `tool_choice` | `tool_choice` | 支持 auto / none / required / 指定函数 |
| `max_output_tokens` | `max_tokens` | 缺失时补默认上限 |
| `text.format` | 结构化输出策略 | `json_schema` 按本设计 5.1 的合成工具和本地 schema 校验处理 |
| `previous_response_id` | 网关本地状态 | 不发给 Anthropic；由网关恢复历史后重新构造 Messages |

Responses 内置工具不能默认直传 Anthropic。当前策略：

- `web_search` / `web_search_preview`：如果 `JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT` 配置了本地 HTTP search executor，网关会先执行搜索，把结果作为 Anthropic system 上下文注入，再把下游 Responses 输出还原为 `web_search_call` item 和 `url_citation` annotations。该路径是 L3 本地预取模拟，不是 Anthropic 原生 server tool，也不是模型自主多轮工具循环。
- `file_search`：由 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 承接。未实现或未配置本地 Vector Store / retrieval 时返回受控错误；启用后先做本地预检索，把结果注入 Anthropic system context，并在 Responses 输出中渲染 `file_search_call`、`file_citation` annotations 和可选 `file_search_call.results`。
- `code_interpreter`、`computer`、`image_generation`、MCP tool、namespace tool 和 custom tool：没有对应运行时时返回受控错误，不能静默丢弃。

Responses `store`、`background`、`conversation`、`include`、`truncation`、`reasoning.encrypted_content`、OpenAI 原生 compact 和 hosted tool 输出不承诺等价。涉及这些字段时，桥接层必须返回可读错误，或在明确不会影响客户端正确性的情况下记录降级。`text.format=json_schema` 校验失败时会生成 Responses failed 语义；当前非流式请求会被 response-inspection 改写为 503，并保留 `openai_anthropic_bridge_structured_output_schema_mismatch`。

### 5.3 文件输入边界

OpenAI 文件输入按“无需 resolver 的 inline / URL 子集”和“必须 resolver 的 `file_id`”分开处理。本地 Files resolver 的长期设计见 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md)。

- Chat `content[].type=file` 只承接 `file.file_data`。`file.file_id` 是 OpenAI 文件存储引用，不能直接转 Anthropic `file_id`。
- Responses `input_file.file_data` 承接 PDF base64、`text/plain` 和 `text/*` inline 内容；PDF 转 Anthropic `document.source.type=base64`，文本转 Anthropic `document.source.type=text`。
- Responses `input_file.file_url` 只按 PDF URL document source 转发；非 PDF URL 需要本地下载、格式解析和权限审计，本阶段不静默转发。
- Chat / Responses 的 `file_id`、`input_image.file_id` 由本地 Files resolver 读取文件内容、校验 API Key 归属、MIME 和大小，再转 image / document block；未知、无权或不支持的文件返回 OpenAI 形态本地错误，不请求 Anthropic。
- resolver 不把本地文件路径或 OpenAI `file_id` 交给 Anthropic；Anthropic 上游只能看到已转换的 image / document source。
- resolver 运行路径必须遵守大文件规则：上传和下载流式处理，只有目标 Anthropic block 需要 base64 且文件在受控大小上限内时才读取编码。

## 6. Responses 状态与 compact

Anthropic Messages 上游不认识 OpenAI `previous_response_id`，因此 Responses 状态必须由网关本地托管。

设计规则：

1. 首轮 Responses bridge 成功后，网关保存本次 OpenAI input/output item、转换后的 Anthropic messages、工具调用状态、下游 response id、API Key / 分组 / 供应商档案边界和 TTL。
2. 后续请求带 `previous_response_id` 时，网关先校验 API Key、系统账户、分组、供应商档案和模型映射边界。
3. 校验通过后，读取历史状态并追加本轮 input，再构造新的 Anthropic Messages 请求。
4. 状态缺失、过期、跨分组、跨供应商、工具调用不完整或摘要校验失败时，返回本地受控错误，不把缺失历史的请求发给上游。
5. `/v1/responses/compact` 不能透传给 Anthropic。需要 compact 时，由网关在当前授权边界内执行 summary compact，保存为网关自有 compact snapshot，再返回 OpenAI CompactResource 兼容外形：`object=response.compaction`，`output` 中恰好 1 个 `type=compaction` item，`encrypted_content=juhecmp.v2.<compact_id>.<digest>`。后续 `/v1/responses` 携带该 item 时，状态层先校验 API Key、分组、供应商档案、TTL 和 digest，再恢复为 inline summary。OpenAI 到 Anthropic bridge 同时接受官方 `compaction` 和 Codex 兼容别名 `compaction_summary`，只把已恢复摘要写入 Anthropic 顶层 `system`；不会把 compact envelope 发给上游。

这套机制可以复用现有 Chat-only Responses bridge 的状态存储思路，但需要抽象为“非原生 Responses 上游桥接状态”，不能继续命名为只服务 Chat Completions。

## 7. 响应渲染

### 7.1 Messages JSON 到 Chat JSON

Anthropic JSON 响应要渲染为 OpenAI Chat Completions JSON：

- `content[].type=text` 合并为 `choices[0].message.content`。
- `content[].type=thinking` 可进入审计和 usage，默认不混入普通 content；如需可见 reasoning，按当前 OpenAI-compatible reasoning 字段策略输出。
- `content[].type=tool_use` 转为 `choices[0].message.tool_calls[]`。
- `stop_reason=end_turn` 映射 `finish_reason=stop`。
- `stop_reason=max_tokens` 映射 `finish_reason=length`。
- `stop_reason=tool_use` 映射 `finish_reason=tool_calls`。
- Anthropic usage 映射到 Chat `usage`，同时保留 Anthropic cache / thinking 扩展到使用记录。

### 7.2 Messages SSE 到 Chat SSE

Anthropic SSE 事件要渲染为 Chat Completions SSE：

- `message_start` 生成首个 `chat.completion.chunk` 角色 delta。
- `content_block_delta.text_delta` 生成 `delta.content`。
- `content_block_start/tool_use` 和 `input_json_delta` 累积为 Chat `delta.tool_calls` 参数分片。
- `message_delta.usage` 更新 usage。
- `message_stop` 输出最终 finish chunk 和 `[DONE]`。
- `event: error` 在尚未写下游可见事件前允许服务端换号；已写出后按 Chat SSE 错误策略输出或中止。

### 7.3 Messages JSON 到 Responses JSON

Anthropic JSON 响应要渲染为 Responses JSON：

- 文本输出生成 `output[].type=message` + `content[].type=output_text`。
- 工具调用生成 `output[].type=function_call`。
- thinking 可生成 `output[].type=reasoning` 或进入审计，具体以 Codex / Responses 客户端可消费契约为准。
- `usage.input_tokens`、`usage.output_tokens`、`usage.total_tokens` 按 OpenAI Responses usage 字段渲染；Anthropic cache / thinking 分解字段进入使用记录和审计。
- 如果上游返回 tool_use，Responses JSON 的完成状态必须让客户端能继续提交 `function_call_output`。

### 7.4 Messages SSE 到 Responses SSE

Anthropic SSE 事件要渲染为 Responses SSE：

- 请求开始后生成 `response.created` 和必要的 in-progress 事件。
- 文本 block 首次出现时生成 `response.output_item.added`，随后用 `response.output_text.delta` 增量输出。
- reasoning block 生成 Responses reasoning item 或 reasoning summary delta。
- tool_use block 生成 function_call output item，参数增量可累积，最终在 `response.output_item.done` 给出完整 arguments。
- `message_stop` 后生成所有未完成 item 的 done 事件和 `response.completed`。
- 上游流内错误或桥接内部错误在可见输出前可触发服务端换号；最终失败时对 Codex profile 输出 `response.failed`，普通 Responses 客户端按 Responses 错误事件输出。

Responses SSE 不能复用 Chat SSE chunk handler。OpenAI Responses 是 typed event 流，必须按 output item 生命周期维护状态。

## 8. 错误与降级策略

本地错误格式按下游协议决定：

- 下游 Chat JSON 请求：返回 OpenAI Chat-compatible error JSON。
- Chat structured output 校验失败是例外：为避免被网关协议检查当作非法 Chat Completion 覆盖，返回合法 Chat Completion，并在 `message.refusal` 中携带 bridge 错误码和原因。
- 下游 Chat SSE 请求：未提交事件前可返回普通 JSON 错误；已提交后按 Chat SSE 失败策略处理。
- 下游 Responses JSON 请求：返回 Responses-compatible error JSON。
- 下游 Responses SSE 请求：Codex profile 且可确定 turn 时输出 Codex 可重试 `response.failed`；其他情况输出 Responses 失败事件或中止。
- Anthropic native 请求仍返回 Anthropic error shape，不受本桥接影响。

桥接层错误码建议统一前缀：

- `openai_anthropic_bridge_unsupported_field`
- `openai_anthropic_bridge_invalid_tool_history`
- `openai_anthropic_bridge_missing_model`
- `openai_anthropic_bridge_state_not_found`
- `openai_anthropic_bridge_state_boundary_mismatch`
- `openai_anthropic_bridge_upstream_protocol_error`

降级原则：

- 会改变客户端语义的字段必须拒绝，不能静默忽略。
- 只影响上游优化而不影响正确性的字段可以忽略，但必须写审计 metadata。
- 工具调用历史不完整时必须受控拒绝，不能把 orphan tool result 发给 Anthropic。
- JSON schema 严格输出必须走合成 Anthropic tool 或后续账号显式启用的原生 structured output，并通过本地 schema 校验；校验失败不能冒充成功。

## 9. 路由与模型映射

模型映射需要允许新增跨协议目标：

```json
{
  "sourceModel": "gpt-5.5-codex",
  "sourceEndpointFamily": "responses",
  "upstreamModel": "claude-opus-4-8",
  "upstreamEndpointFamily": "messages",
  "enabled": true
}
```

Chat 入口示例：

```json
{
  "sourceModel": "gpt-5.5",
  "sourceEndpointFamily": "chat_completions",
  "upstreamModel": "claude-sonnet-4-6",
  "upstreamEndpointFamily": "messages",
  "enabled": true
}
```

保存校验：

- `sourceEndpointFamily` 允许 `chat_completions` 或 `responses`。
- `upstreamEndpointFamily = messages` 只允许当前账号协议档案为 Anthropic v1 Messages。
- Anthropic 官方账号的 `upstreamModel` 必须来自 Anthropic 模型目录或当前账号支持模型。
- Anthropic-compatible 第三方账号的 `upstreamModel` 必须来自该供应商模型目录或当前账号支持模型。
- `responses -> messages` 的 SSE 请求要求 `messages_sse`；JSON 请求要求 `messages_json`。
- `chat_completions -> messages` 同理按 stream 选择 `messages_sse` 或 `messages_json`。

混合智能路由调整：

- 目标模型规则可以指向 Anthropic / Messages 模型。
- 混合路由选择目标模型后，候选分组筛选必须判断“当前下游 OpenAI 请求是否能被该目标分组通过桥接承接”。
- 原有“目标分组必须能承接当前请求协议和端点”改为“目标分组必须具备原生承接能力或显式桥接承接能力”。

## 10. 使用记录、审计和成本

使用记录必须记录：

- 下游 endpoint family：`chat_completions` 或 `responses`。
- 上游实际 endpoint family：`messages`。
- 下游传输：`json` 或 `sse`。
- 上游传输：`messages_json` 或 `messages_sse`。
- 下游请求模型。
- 实际上游模型。
- 桥接类型：`openai_to_anthropic_messages`。
- 是否命中账号模型映射。
- 是否命中混合智能路由。
- Responses 状态 id / compact id 的摘要引用，不能记录完整敏感 payload。
- Anthropic `usage_semantic = anthropic`，包括 cache read、cache write、1h cache write 和 thinking tokens。

下游可见 usage 按 OpenAI Chat / Responses 字段渲染，计价和统计以实际上游模型和 Anthropic usage 语义为准。业务统计仍由 worker 增量聚合，不在网关请求链路实时扫描明细。

## 11. 实现落点

建议新增共享桥接层：

- `backend/src/modules/providers/drivers/_shared/openai-anthropic-bridge.ts`

它只负责：

- 判断 OpenAI Chat / Responses 请求是否可桥接到 Anthropic Messages。
- 构造 Anthropic Messages 上游 body。
- 清理 OpenAI / Codex 专属 header，补 Anthropic `x-api-key`、`anthropic-version` 和必要 `accept/content-type`。
- 将 Anthropic JSON / SSE 渲染回下游 OpenAI Chat / Responses 形态。
- 维护桥接内部状态机和错误码。

Anthropic / DeepSeek / GLM 的 Anthropic v1 provider driver 只负责：

- 判断当前档案是否允许启用桥接。
- 拼接 `/v1/messages` 上游 URL。
- 注入各自供应商认证方式。
- 根据账号真实 endpoint modes 判断 `messages_json/messages_sse` 能力。
- 把转换后的响应交给网关统一返回侧管线。

响应协议选择必须按“下游请求协议”决定，而不是只按“当前账号协议档案”决定。OpenAI 下游请求命中 Anthropic 桥接时，返回侧 usage、错误渲染、JSON 检查和 SSE 事件都要使用 OpenAI response protocol。

## 12. 测试矩阵

mock 回归必须覆盖：

| 类型 | 场景 |
| --- | --- |
| Chat JSON | OpenAI Chat 非流式请求转 Anthropic Messages JSON，返回 Chat JSON |
| Chat SSE | OpenAI Chat 流式请求转 Anthropic Messages SSE，返回 Chat SSE 和 `[DONE]` |
| Responses JSON | OpenAI Responses 非流式请求转 Anthropic Messages JSON，返回 Responses JSON |
| Responses SSE | OpenAI Responses 流式请求转 Anthropic Messages SSE，返回 Responses typed SSE |
| 工具调用 | Chat tool_calls / tool result 与 Responses function_call / function_call_output 往返 |
| 图片 / 文件输入 | Chat image_url、Responses input_image、Chat/Responses inline 文件、Responses PDF URL、本地 `/v1/files` 上传后的 file_id 转 Anthropic image / document block；未知 file_id 本地失败 |
| JSON 输出 | JSON object / JSON schema 的受控支持与不支持错误 |
| 错误转换 | Anthropic JSON error、SSE `event:error` 和本地桥接错误按下游协议渲染 |
| 路由 | API Key 绑定多分组时，OpenAI 请求可命中 Anthropic 映射账号 |
| 混合路由 | 混合 API Key 选择 Anthropic 目标模型时可通过桥接承接当前 OpenAI 请求 |
| Responses 状态 | `previous_response_id` 成功续链、未知 id 受控拒绝、跨分组拒绝 |
| compact | `/responses/compact` 走本地 Anthropic Messages 摘要，不透传上游 |
| 回归 | Anthropic native `/v1/messages` 原生链路不受影响 |
| 回归 | OpenAI `responses -> chat_completions` 既有 GLM / DeepSeek bridge 不受影响 |

真实账户联调必须使用临时环境变量，不把凭据写入仓库、文档或脚本默认值：

- `JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_API_KEY`
- `JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_BASE_URL`
- `JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_MODEL`
- `JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_SCORING_MODEL`（需要混合路由真实验证时使用）

真实联调至少覆盖四类入口各一次成功、工具调用一次、一个错误样本和一个混合路由样本。真实平台如果某模型或端点返回 403 / 429 / 5xx，只记录为上游稳定性事实，不写死到桥接规则。

## 13. 官方资料

- OpenAI Chat Completions：<https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create>
- OpenAI Responses：<https://developers.openai.com/api/reference/resources/responses/methods/create>
- OpenAI Responses 迁移注意事项：<https://developers.openai.com/api/docs/guides/migrate-to-responses>
- Anthropic Messages API：<https://platform.claude.com/docs/en/api/messages>
- Anthropic Streaming Messages：<https://platform.claude.com/docs/en/build-with-claude/streaming>
- Anthropic Tool Use：<https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview>
- Anthropic Structured Outputs：<https://platform.claude.com/docs/en/build-with-claude/structured-outputs>

## 14. 当前不做

- 不做 Anthropic Messages 到 OpenAI Chat / Responses 的反向转换。
- 不做 OpenAI Chat 到 Responses 或 Responses 到 Chat 的新能力；既有 `responses -> chat_completions` bridge 继续归原文档维护。
- `PLAN-0058` 首版不做 OpenAI 内置 hosted tools 到 Anthropic 的自动仿真；后续 web_search、file_search、image_generation、code_interpreter、computer 和 MCP 的承接方式以 [OpenAI 到 Anthropic 高兼容能力矩阵](OpenAI到Anthropic高兼容能力矩阵.md) 和 `PLAN-0059` 为准。
- `PLAN-0058` 首版不把 MCP 当成上游模型协议；MCP tool 如需桥接必须按高兼容矩阵补 server allowlist、auth、approval 和审计映射。
- `PLAN-0058` 首版不把严格 Structured Outputs 宣称为完全等价；后续 strict schema 必须通过合成工具、Anthropic 原生 structured output 或本地 schema 校验后才能宣称成功。
- 不把 Anthropic 官方账号、DeepSeek Anthropic-compatible、GLM Anthropic-compatible 合并成一个账号池。
