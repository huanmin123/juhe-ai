# OpenAI 到 Anthropic Messages 协议桥接设计

> 2026-06-27 路由分层更新：本文旧段落里提到的 API Key 显式桥接配置只作为历史背景；当前目标是 API Key 只绑定策略路由，策略路由负责分组和模型调度，OpenAI / Anthropic 跨协议转换落到混合供应商账户。
> 当前代码已移除 API Key / 策略路由层的显式跨协议桥接入口；本文后续如果仍出现“混合供应商账户”，均表示待迁移历史设计，不得作为新增实现、测试断言或页面配置依据。跨协议承接统一迁移到混合供应商账户。

## 1. 背景

当前系统已经具备三类相关能力：

- Anthropic API Key 原生中转：下游按 Anthropic Messages 协议请求 `/v1/messages`，上游按 Anthropic Messages 透传。
- OpenAI 协议内部桥接：Codex `/v1/responses` 可以在显式条件下转为上游 OpenAI-compatible `/v1/chat/completions`。
- 混合供应商账户：真实上游账户自己声明允许的下游协议、上游协议和模型映射，承接跨协议桥接。

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

这四类都能转为 Anthropic 支持的 Messages 请求。限制是：桥接只承诺明确列出的 OpenAI 协议子集，不承诺完整 OpenAI Responses / Chat 全字段无损等价。遇到不能可靠映射的 hosted/native 能力时，必须返回本地可读 guidance；遇到非法请求边界时返回本地可读错误，或在明确不会影响客户端正确性的情况下记录降级，不能静默丢弃导致客户端误判成功。

### 2.1 v1 收口边界

v1 的交付目标是让 OpenAI Chat / Responses 客户端在混合路由命中 Anthropic Messages 上游时保持协议可用，而不是把 Anthropic Messages 扩展成完整 OpenAI Responses 运行时。

v1 必须承接：

- Chat / Responses JSON 和 SSE 四类入口的文本、usage、错误和结束事件外形。
- system / developer / instructions / name、基础生成控制、metadata / user / safety_identifier。
- function tools、工具历史、工具结果、JSON 回包和 SSE delta；Chat legacy `functions/function_call` 不再承接。
- 当前能力矩阵列出的图片、PDF / text 文件、`file_id` resolver、structured output、reasoning summary、`previous_response_id` 和 compact summary。

v1 不强制承接：

- 没有上游原生能力或本地 runtime 的 OpenAI hosted/native tools。
- OpenAI 专有输出扩展、sources / citations、logprobs、audio、prompt template、`store=true`、background、conversation、自动截断、encrypted reasoning。
- 完整 computer browser 会话、完整 code interpreter 容器生命周期、服务端 MCP runtime / proxy / 管理 / 人工审批 UI，以及真实全矩阵抽样。

能力缺口返回的 agent guidance 是符合下游协议的正常响应，不是网关失败；只有请求非法、权限越界、状态链损坏、schema 校验失败或安全策略命中时，才返回本地协议错误。后续如果某能力有真实 Anthropic profile、本地 runtime 或 provider，必须先补能力声明、mock 覆盖和真实抽样，再从 guidance-first 升级为承接路径。

## 3. 设计原则

- 下游协议保持下游可见形态：Chat 请求必须返回 Chat 形态；Responses 请求必须返回 Responses 形态。
- 上游账号真实能力保持 Anthropic Messages：`supported_endpoint_modes` 仍保存 `messages_json`、`messages_sse`、`message_token_counting`，不新增伪造的 `chat_json`、`responses_sse`。
- 桥接必须显式触发：通过混合供应商账户声明 `chat_completions/responses -> messages`，并且 API Key 绑定的策略路由能够调度到该混合账户。
- 路由右侧仍以目标分组供应商为边界：Anthropic 官方账号右侧模型来自 Anthropic 模型目录；DeepSeek / GLM Anthropic-compatible 档案右侧模型来自各自供应商目录。
- 混合智能路由只负责选目标模型和目标分组；目标分组能通过原生协议或本桥接承接当前下游协议时才可进入候选。
- OpenAI 下游本地错误、上游 Anthropic 错误和流内错误都要按下游协议渲染，不能把 Anthropic error shape 直接返回给 OpenAI 客户端。
- 使用记录和审计必须同时记录下游 endpoint family、上游实际 endpoint family、下游模型、实际上游模型、桥接类型和 usage 语义。
- 上游供应商或当前模型不支持的 hosted/native 能力必须走 agent guidance 或受控拒绝，不因为能力缺口返回 500，也不把不支持能力伪装成上游成功。guidance 只写通用客户端 agent 可执行的下一步，不绑定具体客户端名称。

## 4. 触发条件

桥接只在这些条件同时满足时启用：

1. 下游请求是 `POST /v1/chat/completions`、`POST /chat/completions`、`POST /v1/responses` 或 `POST /responses`。
2. 当前 API Key 已授权的候选账号中存在 Anthropic v1 Messages 档案账号。
3. 请求目标模型能通过混合供应商账户明确定位到真实 Anthropic Messages 上游：
   - 混合账户声明 `sourceEndpointFamily = chat_completions|responses` 且 `upstreamEndpointFamily = messages`。
   - API Key 绑定的策略路由必须能调度到包含该混合账户的分组。
   - 该混合账户必须能按 `upstreamModel` 和 `messages_json/messages_sse` 承接请求。
4. 账户 endpoint mode 满足传输要求：
   - 下游非流式 JSON 要求上游 `messages_json`。
   - 下游 SSE 要求上游 `messages_sse`。
5. 请求体 JSON 可解析，且没有命中本设计明确拒绝的 OpenAI 字段组合。

不允许的触发方式：

- 不允许仅凭客户端画像或 API Key 默认画像自动把任意 Anthropic 账号加入 OpenAI 请求候选；必须配置混合供应商账户。
- 不允许 API Key 绑定的策略路由无法调度到目标混合供应商账户时，通过桥接越权访问该账号。
- 不允许模型无法唯一定位时猜测 Anthropic 账号。
- 不允许把 Anthropic native `/v1/messages` 请求反向包装成 OpenAI Chat / Responses。

## 5. 请求转换矩阵

### 5.1 Chat Completions 到 Messages

| Chat 字段 | Anthropic Messages 字段 | 规则 |
| --- | --- | --- |
| `model` | `model` | 使用模型映射或混合路由后的上游模型 |
| `messages[].role=system` | 顶层 `system` | 多条 system 以空行合并 |
| `messages[].role=developer` | 顶层 `system` | 作为系统约束合并，标记来源为 developer |
| `messages[].name` | 对应消息文本前缀 | Anthropic Messages 无 Chat `name` 顶层字段；非空 `name` 以 `参与者: <name>` 前缀保留在该条 user / assistant 文本内容中，不作为上游字段透传 |
| `messages[].role=user` | `messages[].role=user` | text / image content block 转换；`image_url.url` 支持普通 URL / 图片 data URL，非图片 MIME 或非法 base64 本地拒绝且不请求 Anthropic |
| `messages[].role=assistant` | `messages[].role=assistant` | 普通文本转 text block；`tool_calls` 按客户端数组顺序转 tool_use block；`function.arguments` 为 JSON object 时原样进入 Anthropic `input`，数组 / 标量 JSON 进入 `openai_arguments`，非法 JSON 进入 `openai_arguments_text` |
| `messages[].role=tool` | `messages[].role=user` + `tool_result` | 用 `tool_call_id` 关联 Anthropic `tool_use_id`；多条工具结果按消息顺序保留，不能重排到对应 assistant 后或按 call id 排序 |
| `tools[].type=function` | `tools[]` | 转为 Anthropic tool schema |
| `tools[].type=web_search` / 搜索模型 | 不做本地预取模拟 | Anthropic 上游没有在本 bridge 中声明原生等价能力时，返回正常 agent guidance 且不请求上游 |
| `tool_choice` | `tool_choice` | 支持 `auto`、`none`、`required`、指定 function name |
| Chat legacy `functions` / `function_call` / `role=function` / `assistant.function_call` | 本地拒绝 | 这些旧 Chat 函数字段不再做桥接兼容；客户端必须使用 `tools`、`tool_choice`、`assistant.tool_calls` 和 `role=tool` |
| `max_completion_tokens` / `max_tokens` | `max_tokens` | 优先使用 OpenAI 显式值；缺失时由桥接层补默认上限 |
| `temperature`、`top_p`、`stop` | 同名或等价字段 | 能等价表达时透传；Anthropic 无等价语义或范围不一致的控制项按能力矩阵本地拒绝 |
| `metadata` / `user` / `safety_identifier` / `prompt_cache_key` | Anthropic `metadata.user_id` 或网关本地信号 | `safety_identifier` 优先映射为 `metadata.user_id`，缺失时使用 `user`；业务 `metadata` 和 `prompt_cache_key` 不透传到 Anthropic |
| `stream` | `stream` | 根据下游请求保持一致 |

请求级控制字段必须按语义分层处理，不能因为 Anthropic Messages 不认识字段就静默丢弃：`service_tier=auto/default`、`store=false/null` 可作为默认路径进入桥接且不透传；`service_tier=flex/priority/unknown`、`prompt_cache_retention`、`store=true`、Responses `prompt` 模板和 Chat / Responses 顶层 `moderation` 当前没有等价服务档位、缓存保留、存储、托管 prompt 或审核策略，必须本地返回 OpenAI 形态错误且不请求 Anthropic。图像工具内部的 `moderation` 由本地图像 provider 路径单独承接，不属于顶层请求审核配置。

Chat `response_format` 做受控结构化输出适配：

- `json_object` 可以追加系统约束，要求仅输出合法 JSON。
- `json_schema` 使用合成 Anthropic tool 和强制 `tool_choice` 生成结构化输出，再由网关做 JSON schema 子集二次校验。
- `json_schema` 校验通过时，合成工具不会暴露为 Chat `tool_calls`，只把 tool input 反渲染为 `message.content` JSON 字符串。
- `json_schema` 校验失败时，Chat JSON 返回合法 Chat Completion，`choices[0].message.content=null`，`choices[0].message.refusal` 携带 `openai_anthropic_bridge_structured_output_schema_mismatch` 和失败原因；Chat SSE 在流内输出 error frame。
- 不支持 `n > 1`、`logprobs`、音频输出、`modalities` 音频、非默认采样控制、`prediction`、`verbosity` 等等价能力；命中时返回本地受控错误或按能力矩阵明确忽略并写审计。

Chat web search 边界：

- 官方 Chat Completions 搜索路径是专用搜索模型或上游原生托管工具语义；它不是可以靠 Anthropic 文本模型和本地 system 注入无损模拟的通用字段转换。
- 当前 bridge 不再实现本地 HTTP search executor 预取，不把 `web_search` 当 Anthropic client tool 发送，也不向 Chat JSON / SSE 伪造 citation。
- 当 Chat 请求显式携带 `web_search` / `web_search_preview` tool，或模型名命中本 bridge 无法原生承接的搜索模型路径时，返回正常 agent guidance，不请求 Anthropic。
- 只有未来某个 Anthropic profile 明确声明原生 server tool / web search 等价能力，并在 driver 中补齐真实映射和回归测试后，才能单独启用该路径。

### 5.2 Responses 到 Messages

| Responses 字段 | Anthropic Messages 字段 | 规则 |
| --- | --- | --- |
| `model` | `model` | 使用模型映射或混合路由后的上游模型 |
| `instructions` | 顶层 `system` | 作为本轮系统指令 |
| `input` 字符串 | `messages[].role=user` | 作为单条用户消息 |
| `input[].type=message` | `messages[]` / 顶层 `system` | `system/developer` 入 system；`user/assistant` 入 messages |
| `input[].content[].type=input_text` | text block | 保留文本 |
| `input[].content[].type=input_image` | image block | data URL / URL 按 Anthropic 支持的 image source 转换；`file_id` 经本地 Files resolver 转 image source；非图片 MIME、未知 file_id 或非法 base64 本地拒绝且不请求 Anthropic |
| `input[].content[].type=input_file` | document block | 支持 `file_data` inline PDF / text/*、PDF `file_url` 和本地 Files resolver `file_id`；非法 base64、不支持 MIME、未知或无权文件本地失败且不上游 |
| `input[].type=function_call` | assistant `tool_use` | 保留 call id、name、arguments；多个 function_call 按 Responses `input` 数组顺序生成 tool_use；`arguments` 为 JSON object 时原样进入 Anthropic `input`，数组 / 标量 JSON 进入 `openai_arguments`，非法 JSON 进入 `openai_arguments_text` |
| `input[].type=function_call_output` | user `tool_result` | 用 `call_id` 关联工具结果；多条工具结果按 Responses `input` 数组顺序保留，不能重排到对应 function_call 后或按 call id 排序 |
| `tools[].type=function` | Anthropic `tools[]` | function tools 转 Anthropic tool schema |
| `tool_choice` | `tool_choice` | 支持 auto / none / required / 指定函数 |
| `max_output_tokens` | `max_tokens` | 缺失时补默认上限 |
| `text.format` | 结构化输出策略 | `json_schema` 按本设计 5.1 的合成工具和本地 schema 校验处理 |
| `previous_response_id` | 网关本地状态 | 不发给 Anthropic；由网关恢复历史后重新构造 Messages |

Responses 内置工具不能默认直传 Anthropic。当前策略：

- `web_search` / `web_search_preview`：当前不做本地预取模拟。没有 Anthropic 上游原生等价能力时返回正常 agent guidance，不输出 `web_search_call` 或 `url_citation`。
- `file_search`：由 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 承接。未实现或未配置本地 Vector Store / retrieval 时返回受控错误；启用后先做本地预检索，把结果注入 Anthropic system context，并在 Responses 输出中渲染 `file_search_call`、`file_citation` annotations 和可选 `file_search_call.results`。
- `code_interpreter`、`computer`、namespace tool 和 custom tool：没有对应运行时时返回正常 agent guidance，不能静默丢弃。MCP tool 固定返回客户端 / 本地 agent guidance，不在网关服务端建立 runtime、proxy、allowlist、approval 或 execution record。`image_generation` 首批已由本地图像 provider 承接无输入图片的 generate 路径：Anthropic Messages 只生成 revised prompt，图像 provider 返回 OpenAI Images JSON `data[0].b64_json`，桥接层渲染 Responses `image_generation_call.result`；无 provider 时返回 OpenAI 形态 guidance 且不请求 Anthropic。provider `moderation_blocked` 返回 Responses `status=failed` 并保留 `image_generation_user_error`、`moderation_blocked` 和 `moderation_details`；edit / mask 首批返回 guidance，历史图片复用、partial image streaming 继续按 [PLAN-0061](../plans/计划-0061-Responses图像生成本地Provider桥接.md) 后续推进。

Responses `store`、`background`、`conversation`、`include`、`truncation`、`reasoning.encrypted_content`、OpenAI 原生 compact 和 hosted tool 输出不承诺等价。涉及无法执行的 hosted/native 能力时，桥接层必须返回可读 agent guidance；涉及非法历史、权限、状态边界或 schema 校验失败时返回对应协议错误，或在明确不会影响客户端正确性的情况下记录降级。`text.format=json_schema` 校验失败时会生成 Responses failed 语义；当前非流式请求会被 response-inspection 改写为 503，并保留 `openai_anthropic_bridge_structured_output_schema_mismatch`。

### 5.3 文件输入边界

OpenAI 文件输入按“无需 resolver 的 inline / URL 子集”和“必须 resolver 的 `file_id`”分开处理。本地 Files resolver 的长期设计见 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md)。

- Chat `content[].type=file` 承接 `file.file_data` 和经本地 Files resolver 校验后的 `file.file_id`；`file.file_id` 是 OpenAI 文件存储引用，不能直接转 Anthropic `file_id`。Chat `file_url`、未知 `file_id` 或 resolver 返回不支持 MIME 时当前本地拒绝，不请求 Anthropic。
- Responses `input_file.file_data` 承接 PDF base64、`text/plain` 和 `text/*` inline 内容；PDF 转 Anthropic `document.source.type=base64`，文本转 Anthropic `document.source.type=text`。非法 base64 或非 PDF/text MIME 本地拒绝，不请求 Anthropic。
- Responses `input_file.file_url` 只按 PDF URL document source 转发；非 PDF URL 需要本地下载、格式解析和权限审计，本阶段本地拒绝且不静默转发。
- Chat / Responses 的 `file_id`、`input_image.file_id` 由本地 Files resolver 读取文件内容、校验 API Key 归属、MIME、base64 和大小，再转 image / document block；未知、无权、不支持 MIME 或 resolver 内容非法的文件返回 OpenAI 形态本地错误，不请求 Anthropic。
- resolver 不把本地文件路径或 OpenAI `file_id` 交给 Anthropic；Anthropic 上游只能看到已转换的 image / document source。
- resolver 运行路径必须遵守大文件规则：上传和下载流式处理，只有目标 Anthropic block 需要 base64 且文件在受控大小上限内时才读取编码。

## 6. Responses 状态与 compact

Anthropic Messages 上游不认识 OpenAI `previous_response_id`，因此 Responses 状态必须由网关本地托管。

设计规则：

1. 首轮 Responses bridge 成功后，网关保存本次 OpenAI input/output item、转换后的 Anthropic messages、工具调用状态、下游 response id、API Key / 分组 / 供应商档案边界和 TTL。
2. 后续请求带 `previous_response_id` 时，网关先校验 API Key、系统账户、分组、供应商档案和模型映射边界。
3. 校验通过后，读取历史状态并追加本轮 input，再构造新的 Anthropic Messages 请求。
4. 状态缺失、过期、跨分组、跨供应商、工具调用不完整或摘要校验失败时，返回本地受控错误，不把缺失历史的请求发给上游。
5. `/v1/responses/compact` 不能透传给 Anthropic。需要 compact 时，由网关在当前授权边界内执行 summary compact，保存为网关自有 compact snapshot，再返回 OpenAI CompactResource 兼容外形：`object=response.compaction`，`output` 中恰好 1 个 `type=compaction` item，`encrypted_content=juhecmp.v2.<compact_id>.<digest>`。后续 `/v1/responses` 携带该 item 时，状态层先校验 API Key、分组、供应商档案、TTL 和 digest，再恢复为 inline summary。OpenAI 到 Anthropic bridge 同时接受官方 `compaction` 和 Codex 兼容别名 `compaction_summary`，只把已恢复摘要写入 Anthropic 顶层 `system`；不会把 compact envelope 发给上游。缺少 `encrypted_content`、`juhecmp.v1` inline summary 解析失败、解析后没有摘要或未知 compact envelope 时必须本地拒绝且不请求 Anthropic，不能静默丢弃压缩历史后继续生成。

这套机制复用现有 Chat-only Responses bridge 的状态存储思路，并作为“非原生 Responses 上游桥接状态”继续扩展。当前已覆盖 Codex SSE 续链和普通 Responses JSON / SSE 续链；后续如果继续新增跨供应商 Responses 状态能力，命名和文档应从 Chat-only 语义逐步收敛到通用 Responses bridge state。

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
- tool_use block 生成 function_call output item：`content_block_start/tool_use` 先输出 `response.output_item.added`，Anthropic `input_json_delta.partial_json` 必须转成 `response.function_call_arguments.delta`，`content_block_stop` 必须输出 `response.function_call_arguments.done` 和最终 `response.output_item.done`，让客户端可以实时聚合 arguments。
- `message_stop` 后生成所有未完成 item 的 done 事件和 `response.completed`。
- 上游流内错误或桥接内部错误在可见输出前可触发服务端换号；最终失败时对 Codex profile 输出 `response.failed`，普通 Responses 客户端按 Responses 错误事件输出。

Responses SSE 不能复用 Chat SSE chunk handler。OpenAI Responses 是 typed event 流，必须按 output item 生命周期维护状态。

## 8. 错误与降级策略

本地错误和 guidance 格式按下游协议决定：

- 不支持的 hosted/native tool 且没有真实执行器时，返回正常 Chat Completion / Responses completed guidance 消息，HTTP 200，不请求上游。
- 供应商 / 模型不支持某项能力时，优先把它归为能力缺口而不是服务端异常。guidance 必须包含能力类型、当前上游协议或供应商事实、可行动建议，例如配置本地 MCP / 工具执行器、换用支持该能力的模型或移除相关 tool，让客户端 agent 后续自行处理。
- guidance 不写具体客户端品牌名；同一响应应能被任意 OpenAI-compatible / agent 客户端读取并作为下一轮决策依据。
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
- 能力缺口优先 guidance，不直接打死会话；请求非法、权限越界、状态链损坏、schema 校验失败或安全策略命中才返回本地协议错误。
- 只影响上游优化而不影响正确性的字段可以忽略，但必须写审计 metadata。
- 工具调用历史不完整时必须受控拒绝，不能把 orphan tool result 发给 Anthropic。
- JSON schema 严格输出必须走合成 Anthropic tool 或后续账号显式启用的原生 structured output，并通过本地 schema 校验；校验失败不能冒充成功。

## 9. 路由与显式混合规则

跨协议目标通过 混合供应商账户声明：

```json
{
  "id": "openai_responses_to_anthropic_messages",
  "enabled": true,
  "priority": 1,
  "sourceClientProfile": "auto",
  "sourceModel": "gpt-5.5-codex",
  "sourceEndpointFamily": "responses",
  "targetGroupId": "grp_anthropic_messages",
  "upstreamModel": "claude-opus-4-8",
  "upstreamEndpointFamily": "messages",
  "adapterMode": "bridge"
}
```

Chat 入口示例：

```json
{
  "id": "openai_chat_to_anthropic_messages",
  "enabled": true,
  "priority": 2,
  "sourceClientProfile": "auto",
  "sourceModel": "gpt-5.5",
  "sourceEndpointFamily": "chat_completions",
  "targetGroupId": "grp_anthropic_messages",
  "upstreamModel": "claude-sonnet-4-6",
  "upstreamEndpointFamily": "messages",
  "adapterMode": "bridge"
}
```

保存校验：

- `sourceEndpointFamily` 允许 `chat_completions` 或 `responses`。
- `targetGroupId` 必须是当前 API Key 所选路由策略已绑定且启用的目标分组。
- `upstreamEndpointFamily = messages` 只允许目标分组协议档案为 Anthropic v1 Messages。
- Anthropic 官方目标分组的 `upstreamModel` 必须来自 Anthropic 模型目录或目标账号支持模型。
- Anthropic-compatible 第三方目标分组的 `upstreamModel` 必须来自该供应商模型目录或目标账号支持模型。
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
- 是否命中 混合供应商账户。
- 命中的路由规则 ID。
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
| 图片 / 文件输入 | Chat image_url、Responses input_image、Chat/Responses inline PDF / text/* 文件、Responses PDF URL、本地 `/v1/files` 上传后的 file_id 转 Anthropic image / document block；图片 file_id 非图片 MIME / 非法 base64、文件非法 base64、不支持 MIME、Chat file_url、Responses 非 PDF URL、Chat / Responses 未知或不支持 MIME 的 file_id 本地失败且不上游 |
| JSON 输出 | JSON object / JSON schema 的受控支持与不支持错误 |
| 错误转换 | Anthropic JSON error、SSE `event:error` 和本地桥接错误按下游协议渲染 |
| 路由 | API Key 所选路由策略绑定多分组时，OpenAI 请求可命中 Anthropic 映射账号 |
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
- `PLAN-0058` 首版不做 OpenAI 内置 hosted tools 到 Anthropic 的自动仿真；后续 web_search、file_search、image_generation、code_interpreter 和 computer 的承接方式以 [OpenAI 到 Anthropic 高兼容能力矩阵](OpenAI到Anthropic高兼容能力矩阵.md) 和 `PLAN-0059` 为准。
- `PLAN-0058` 首版及当前实现都不把 MCP 当成上游模型协议，也不在网关服务端桥接 MCP tool；MCP 统一返回客户端本地 MCP / 原生上游 guidance。
- `PLAN-0058` 首版不把严格 Structured Outputs 宣称为完全等价；后续 strict schema 必须通过合成工具、Anthropic 原生 structured output 或本地 schema 校验后才能宣称成功。
- 不把 Anthropic 官方账号、DeepSeek Anthropic-compatible、GLM Anthropic-compatible 合并成一个账号池。
