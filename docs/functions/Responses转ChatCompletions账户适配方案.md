# Responses 转 Chat Completions 账户适配方案

## 文档边界

本文记录 `POST /v1/responses` 在选中具体 API Key 账户后，按账户配置降级转发到上游 `POST /v1/chat/completions` 的落地方案、已落地边界和待核对细节。

实现状态：第一版已于 2026-06-09 落地。当前支持账户级 `chat_completions_bridge`、基础 JSON / SSE 转换、导入导出、公开推送和前端配置；真实国内模型长会话、`/responses/compact` 和更复杂 Responses 内置工具仍需后续补测或扩展。

不在本文范围内：

- 把 OAuth / Codex backend 的 `/responses` 改成 `/chat/completions`。
- 把 `/chat/completions` 下游请求反向转换成 `/responses`。
- 完整实现 Responses API 的所有状态、会话、内置工具、文件、MCP、computer use 或后台任务能力。
- 在账户上开放任意 header、body patch、脚本转换或用户自定义协议改写。

## 调研结论

OpenAI 官方文档把 Responses API 作为 Chat Completions 的演进接口，但两者请求和响应对象并不相同：Responses 使用 `input`、`instructions`、output items、`previous_response_id` 和 Responses SSE 事件；Chat Completions 使用 `messages`、`choices` 和 chat completion chunk。

公开实现里已有两类可参考方案：

- LiteLLM 提供 `/responses` 到 `/chat/completions` 的桥接，用于 vLLM、llama.cpp、LM Studio 等只实现 Chat Completions 的 OpenAI-compatible 上游。它的重点是显式 opt-in，不把所有 Responses 请求默认降级。
- CC Switch 面向 Codex 做本地代理。Codex 侧仍配置 `wire_api = "responses"`，本地代理按 provider 的真实上游格式把 `/responses` 转到 `/chat/completions`，再把 Chat 回包重建为 Responses SSE。已安装的 CC Switch 3.16.1 二进制里可以确认存在 `transform_responses.rs`、`streaming_responses.rs`、`streaming_codex_chat.rs`、`transform_codex_chat.rs`，并包含 `previous_response_id`、`reasoning_content`、`tool_calls`、`response.output_text.delta`、`response.function_call_arguments.delta` 和 `fallback to chat/completions` 等关键路径。

论文层面，ToolLLM、Gorilla 和 LLM-Rosetta 这类工作共同说明：工具调用和跨 API 形态转换需要稳定中间表示、状态管理和明确降级边界，不能只做字段重命名。因此本项目第一版只做可控子集，并把不支持能力显式拒绝或标记为降级。

参考资料：

- [OpenAI Responses API](https://platform.openai.com/docs/api-reference/responses/create)
- [OpenAI Chat Completions API](https://platform.openai.com/docs/api-reference/chat/create)
- [OpenAI Responses 迁移说明](https://platform.openai.com/docs/guides/migrate-to-responses)
- [OpenAI Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/)
- [LiteLLM Response API](https://docs.litellm.com.cn/docs/response_api)
- [CC Switch GitHub](https://github.com/farion1231/cc-switch)
- [ToolLLM](https://arxiv.org/abs/2307.16789)
- [Gorilla](https://arxiv.org/abs/2305.15334)
- [LLM-Rosetta](https://arxiv.org/abs/2604.09360)

## 设计目标

- 让 Codex、OpenAI SDK 或其他固定请求 `/v1/responses` 的客户端，可以通过 `juhe-ai` 使用只支持 Chat Completions 的 OpenAI-compatible 上游账号。
- 保持下游协议仍是 Responses。客户端看不到上游被降级成 Chat Completions。
- 配置落在账户维度，因为上游 endpoint 能力和工具调用质量由具体账号、`base_url` 和模型服务决定。
- 只在 OpenAI v1 协议档案内生效，不引入跨协议重型网关。
- 默认保持透传，只有显式配置的 API Key 账户才启用转换。

## 字段设计

新增账户字段建议：

```ts
type OpenAIResponsesUpstreamMode =
  | 'passthrough'
  | 'chat_completions_bridge'
```

建议数据库字段：

```sql
openai_responses_upstream_mode TEXT NOT NULL DEFAULT 'passthrough'
```

字段含义：

| 值 | 含义 |
| --- | --- |
| `passthrough` | 默认值。`/responses` 请求按当前逻辑透传到上游 `/responses`。 |
| `chat_completions_bridge` | 命中该账号且请求为 `POST /responses` / `POST /v1/responses` 时，上游请求改为 `/chat/completions`，响应再转换回 Responses。 |

字段边界：

- 只允许 `protocol_code = openai`、`protocol_version = v1` 的 API Key 账户配置。
- OAuth 账户固定使用现有 `openai_oauth_codex` adapter，不读取该字段。
- 授权实例运行时从来源账户读取该字段，和凭据、`base_url`、代理、并发一样属于来源资源事实。
- 字段不是敏感字段，可在账户列表、详情、导入导出和操作日志摘要中展示。
- 字段不放进 `credentials`，避免把协议能力和敏感凭据混在一起。

前端展示建议：

- 表单区域：`请求策略`
- 字段名：`Responses 上游模式`
- 选项：
  - `透传 Responses`
  - `转为 Chat Completions`
- Tooltip：`用于上游只支持 /chat/completions 但客户端固定请求 /responses 的账号。开启后只转换 POST /responses。`

## 与现有配置的关系

`clientCompatibility` 描述下游客户端画像和请求习惯：

- `openai_standard`：普通 OpenAI-compatible 客户端。
- `codex_responses`：Codex Responses 客户端兼容处理，当前会补齐 Codex 请求体和流式请求头。

`openai_responses_upstream_mode` 描述选中账户后的上游 endpoint 能力：

- `passthrough`：上游支持 `/responses`。
- `chat_completions_bridge`：上游只支持或优先使用 `/chat/completions`。

两者可以组合：

| clientCompatibility | openai_responses_upstream_mode | 说明 |
| --- | --- | --- |
| `openai_standard` | `passthrough` | 当前默认 OpenAI v1 透传。 |
| `codex_responses` | `passthrough` | Codex 请求体归一化后打上游 `/responses`。 |
| `openai_standard` | `chat_completions_bridge` | 普通 Responses 客户端走 Chat 上游。 |
| `codex_responses` | `chat_completions_bridge` | Codex 请求体归一化后再转 Chat 上游，是访问国内 Chat-only 模型的主要目标形态。 |

## 请求转换

仅当以下条件同时满足时启用：

- 当前账号是 API Key 账户。
- 当前账号 `openai_responses_upstream_mode = chat_completions_bridge`。
- 当前请求是 `POST /responses` 或 `POST /v1/responses`。
- 请求体是 JSON 对象。

上游 URL：

- 下游 `/responses` 和 `/v1/responses` 都转为上游 `/chat/completions`。
- 查询参数原则上不透传到 Chat 上游，除非后续确认有明确兼容字段。

请求字段映射：

| Responses 请求字段 | Chat Completions 请求字段 | 第一版策略 |
| --- | --- | --- |
| `model` | `model` | 原样传入，之后仍可被账号模型映射改写。 |
| `input: string` | `messages: [{ role: "user", content }]` | 支持。 |
| `input: message[]` | `messages[]` | 支持 message item 子集。 |
| `instructions` | 前置 system/developer message | 支持；默认用 `system`，Codex 归一后的 developer 保留为 `developer` 或按上游能力降级为 `system`。 |
| `stream` | `stream` | 支持；Codex 模式通常强制为 `true`。 |
| `max_output_tokens` | `max_tokens` 或 `max_completion_tokens` | 第一版建议默认 `max_tokens`，后续按账号兼容选项扩展。 |
| `temperature` / `top_p` | 同名字段 | 仅当原请求存在时透传；Codex compatibility 当前会删除这些字段。 |
| `tools` 中 `type = function` | `tools[].function` | 支持函数工具子集。 |
| `tool_choice` | `tool_choice` | 支持 `auto` / `none` / `required` 和 function name 子集。 |
| `parallel_tool_calls` | `parallel_tool_calls` | 原样透传，若上游报错由账户错误策略处理。 |
| `text.format` | `response_format` | 支持 JSON schema / JSON object 子集，需核对上游兼容性。 |
| `reasoning.effort` | `reasoning_effort` 或移除 | 默认可配置，第一版建议透传到 `reasoning_effort`，上游不支持时允许账户级关闭。 |
| `include` | 无稳定对应 | 默认移除；`reasoning.encrypted_content` 不应伪造。 |
| `store` | 无稳定对应 | 移除。 |
| `previous_response_id` | 无稳定对应 | 第一版不支持完整续链；可用本地会话缓存作为后续增强。 |
| `metadata` / `user` | `metadata` / `user` | 默认移除或仅保留无敏感元数据，避免污染上游。 |

输入 item 子集：

- `message`：转 Chat message。
- `function_call_output`：转 `role = tool` 的 Chat message，需要 `tool_call_id`。
- `reasoning`：不转为可见文本；可在后续作为上下文摘要策略单独设计。
- `custom_tool_call`、`tool_search_call`、`web_search_call`、`file_search_call`、`computer_call`：第一版不支持，命中时返回本地 `400` 或网关兼容错误。

内容 part 子集：

- `input_text` / `output_text`：转文本内容。
- `input_image`：只有上游 Chat Completions 兼容 image_url 时才转，否则拒绝。
- `input_file`、audio、computer screenshot 等复杂类型第一版不支持。

## 非流式响应转换

Chat Completions 非流式响应转换为 Responses response 对象。

基础结构：

```json
{
  "id": "resp_local_xxx",
  "object": "response",
  "created_at": 1710000000,
  "status": "completed",
  "model": "model-name",
  "output": [],
  "output_text": "",
  "usage": {}
}
```

映射规则：

- `choices[0].message.content` 转为 `output[0].type = "message"`、`role = "assistant"`、`content[0].type = "output_text"`。
- `choices[0].message.tool_calls[].function` 转为 Responses `function_call` output item。
- `finish_reason = stop` 转 `status = completed`。
- `finish_reason = length` 转 `status = incomplete`，并写 `incomplete_details.reason = "max_output_tokens"`。
- `finish_reason = tool_calls` 仍可 `completed`，由客户端继续提交工具结果。
- `usage.prompt_tokens` 转 `usage.input_tokens`。
- `usage.completion_tokens` 转 `usage.output_tokens`。
- `usage.total_tokens` 转 `usage.total_tokens`。
- `usage.prompt_tokens_details.cached_tokens` 转 `usage.input_tokens_details.cached_tokens`。

## 流式响应转换

如果下游请求是流式，网关对上游 Chat SSE 做状态化转换，客户端仍收到 Responses SSE。

建议事件顺序：

1. `response.created`
2. `response.in_progress`
3. 首次文本时发送：
   - `response.output_item.added`
   - `response.content_part.added`
4. 每段文本：
   - `response.output_text.delta`
5. 文本完成：
   - `response.output_text.done`
   - `response.content_part.done`
   - `response.output_item.done`
6. 工具调用参数：
   - `response.output_item.added`
   - `response.function_call_arguments.delta`
   - `response.function_call_arguments.done`
   - `response.output_item.done`
7. 结束：
   - `response.completed`

流式状态需要维护：

- response id。
- output item index。
- content part index。
- Chat `tool_calls[index].id` 到 Responses `call_id` 的映射。
- function arguments 增量拼接。
- 累计 output text。
- usage 尾包。
- 是否已经向下游输出可见内容，用于沿用现有流式失败处理。

失败处理：

- 上游非 2xx：按当前网关上游失败链路处理，不进入 SSE 转换。
- 上游 `data: [DONE]` 前连接中断：沿用现有流式中断策略。
- 转换器解析到 Chat error event：转 `response.failed`，同时进入账号副作用队列。
- 可见输出后失败不做服务端重放。

## `/responses/compact` 策略

Codex 长会话可能调用 `/responses/compact`。Chat Completions 没有等价端点。

第一版建议：

- `chat_completions_bridge` 不支持 `/responses/compact`。
- 命中时返回本地 `400`，错误信息说明“当前账户使用 Chat Completions 上游，不支持 Responses compact”。
- 国内 Chat-only 上游通常通过客户端截断、额外 Chat summarization、RAG 回填或长上下文模型来控制上下文，不提供官方 Responses compact 等价能力。
- 只有在真实验证 Codex 长会话必须依赖 compact 且可接受摘要语义时，再设计第二版：新增显式实验策略，把 compact 请求转换为一次 Chat summarization，并返回降级响应。

不建议第一版伪造 compact 成功，因为官方 `/responses/compact` 产出的是 Responses compaction item；Chat 明文摘要不能等价替代。强行包装会破坏 Codex 上下文管理语义，把明确不支持变成隐性上下文漂移。

## 候选与调度影响

账户能力过滤需要加入 endpoint 兼容判断：

- 请求 `/responses` 时：
  - `passthrough` API Key 账户可承接。
  - `chat_completions_bridge` API Key 账户可承接。
  - OAuth 账户继续按现有 Codex adapter 路径判断。
- 请求 `/chat/completions` 时：
  - `chat_completions_bridge` 不产生额外影响，仍按原路径透传。
- 请求 `/responses/compact` 时：
  - OAuth 账户可承接。
  - `passthrough` API Key 账户是否可承接取决于上游原生支持。
  - `chat_completions_bridge` 第一版不可承接。

如果某账号因 endpoint 能力不匹配被跳过，不计为账号失败，不触发本地屏蔽、冷却或账户错误策略。

## 对现有能力影响与保护边界

### 错误处理链路

Responses 转 Chat 不能绕过现有上游异常重试、账号运行态屏障、账户错误处理策略和流式失败副作用。

需要固定的错误归属：

| 场景 | 错误归属 | 处理策略 |
| --- | --- | --- |
| 请求体不是 JSON 对象 | 本地请求错误 | 返回本地 `400`，不命中账号，不写账号失败。 |
| 请求包含第一版不支持的 Responses 内置工具或 item | 本地协议降级错误 | 返回本地 `400` 或 OpenAI-compatible 错误，不写账号失败；可记录审计用于排障。 |
| 请求转换失败，原因是实现 bug 或转换器状态异常 | 网关错误 | 返回本地 `500` / `503`，不应把具体账号标记为异常。 |
| 上游 Chat 返回非 2xx | 上游账号失败 | 进入现有 `gateway_upstream_response_failed`、账户错误处理策略、本地屏障和后续账号尝试。 |
| 上游 Chat SSE 解析失败且未输出可见内容 | 上游或转换链路失败 | 按当前流式失败规则处理；如果命中 Codex profile 且满足既有条件，才允许写 Codex 可重试事件。 |
| 上游 Chat SSE 已输出可见内容后中断 | 上游流式失败 | 不重放、不切号续写；进入账号运行态屏障和流式失败副作用。 |
| 转换后的 Responses SSE 被现有流式拦截策略命中 | 响应侧策略命中 | 继续走现有流式拦截链路，不能因为来源是 Chat 上游而跳过。 |

错误码和日志需要区分下游协议与上游协议，建议审计 metadata 记录：

- `downstreamEndpointFamily = responses`
- `upstreamEndpointFamily = chat_completions`
- `upstreamBridgeMode = chat_completions_bridge`
- `bridgeFailurePhase = request_transform | upstream_response_transform | stream_transform`

这样排查时可以区分“上游模型失败”和“网关转换失败”，避免把转换器问题误判成账号质量问题。

### 性能与内存

该能力会增加一次 JSON 请求转换和一次响应转换，必须遵守项目大文件性能底线。

请求侧要求：

- 不能在普通透传路径额外完整解析请求体；只有命中 `chat_completions_bridge` 且路径为 `/responses` 时才解析。
- 小 JSON 使用现有内联解析阈值，大 JSON 继续走 `openai-gateway-json-worker`，不能在主事件循环直接解析大 body。
- 转换结果 body 仍受当前文本 lane 上限约束；不为 bridge 单独放宽 raw body hard limit。
- 不支持的复杂 item 应在解析后尽早失败，不继续构造候选或请求上游。

流式侧要求：

- SSE 转换器必须按 chunk / event 增量处理，禁止缓存完整上游流后再输出。
- 只维护必要状态：response id、item 索引、tool call id 映射、当前 function arguments 拼接、累计 usage 和有限诊断摘要。
- function arguments 拼接需要有明确上限；超过上限时应终止转换并按流式失败处理，避免工具参数异常导致内存无界增长。
- 响应体捕获继续使用现有有界捕获和截断策略；审计正文不能因为转换需要而保存两份完整 payload。
- 非流式上游响应转换也要走有界读取；如果上游返回异常大 JSON，应按现有响应捕获上限和超限错误处理。

性能验证需要覆盖：

- 大请求体进入 worker 解析，不阻塞主进程。
- 长流式输出不产生线性内存增长。
- 大 tool call arguments 达到上限后的失败行为。
- bridge 关闭时默认透传路径没有额外解析开销。

### 使用记录、统计与额度

使用记录仍以“客户端实际请求”和“最终命中账号”为事实源，但需要保留转换信息：

- 下游路径记录为 `/responses`。
- 上游实际路径可在诊断字段或审计 metadata 记录为 `/chat/completions`。
- 模型记录继续保留下游模型、上游模型和计价模型；账号模型映射仍在选中账号后生效。
- usage 解析优先使用转换后的 Responses usage；如果只有 Chat usage，按 `prompt_tokens -> input_tokens`、`completion_tokens -> output_tokens` 映射。
- API Key 额度、统一授权额度、账户质量和统计 worker 只读取转换后的标准 usage 字段，不在 API 路由实时聚合或回扫明细。
- `manual_account_test` 和 `cooldown_retest` 的统计归属仍按现有 traffic source 规则，不因 bridge 模式混入真实业务统计。

如果上游 Chat 不返回 usage：

- 使用记录仍写请求事实，token / cost 为空或按现有估算策略处理。
- 授权额度判断不能在当前请求链路实时扫描历史记录补算。
- 不能为了 bridge 在前端或 API route 做临时 token 汇总。

### 审计与日志

原始审计需要能还原双协议链路，但不能扩大无界捕获：

- 请求审计保留下游原始 `/responses` 请求体的有界正文。
- 上游审计可以记录转换后的 Chat 请求体有界正文，必须继续遵守正文压缩、截断、采样和保全策略。
- 失败链路需要标明是 request transform、upstream request、upstream response transform 还是 stream transform。
- 操作日志只记录账户配置变更，例如 `Responses 上游模式：透传 Responses -> 转为 Chat Completions`，不记录敏感凭据。
- 运行日志文案使用中文，但协议字段、SSE event、header 和错误码保持原文。

### 流式拦截与 Codex 重试

转换后的下游事件是 Responses SSE，因此现有 Responses 流式检查仍应工作。

需要保持的边界：

- `clientProfile = codex` 的判断仍来自下游请求和 Codex headers，不来自上游 Chat 事件。
- Codex 可重试 `response.failed/upstream_retryable_error` 仍只能在既有条件满足时写出，不能因为 bridge 模式放宽。
- 可见输出前失败和可见输出后失败的副作用边界不变。
- 账户追加流式规则、供应商流式拦截策略和全局策略应看到转换后的 Responses event；如果需要识别上游 Chat 原始异常，只能在转换器 metadata 中补充，不改变策略匹配输入。

### 账号候选、授权与缓存

候选过滤要在请求进入上游前完成：

- bridge 模式账户可以承接 `/responses`，但不能承接第一版不支持的 `/responses/compact`。
- 因 `/responses/compact` 不支持而跳过的 bridge 账户不算失败。
- 授权实例从来源账户读取 bridge 模式；被授权用户不能单独改写来源账户的上游模式。
- API Key 多分组 fallback 仍按分组顺序处理，不因为 bridge 模式跨分组混排账号。
- 会话亲和命中 bridge 账户时仍要检查 endpoint 能力；如果本轮 endpoint 不支持，不能为了粘性强行使用。
- 网关运行时缓存需要把 `openai_responses_upstream_mode` 纳入账号快照和缓存失效范围，账户编辑后必须清理候选缓存。

### 前端与用户体验

前端不能把该能力表达成“完整支持 Responses”。

建议文案：

- 字段：`Responses 上游模式`
- 选项：`透传 Responses`、`转为 Chat Completions`
- 帮助：`仅用于上游不支持 /responses 的 API Key 账户。开启后 Codex 短任务和普通文本更容易兼容，但内置工具、compact 和部分 reasoning 字段可能不可用。`

账户测试结果需要展示：

- 实际兼容：`Codex Responses` / `OpenAI 标准`
- Responses 上游模式：`透传 Responses` / `转为 Chat Completions`
- 实际上游路径：`/chat/completions`
- 如果失败是转换不支持，应明确显示“本地转换不支持该 Responses 能力”，不要显示成“上游账号异常”。

### 发布与回滚

第一版建议默认关闭，只对显式选择的账户生效。

回滚策略：

- 将账户字段改回 `passthrough` 即恢复当前行为。
- 如果实现出现转换器级问题，可以通过后端临时全局禁用开关禁止 `chat_completions_bridge` 生效，并返回本地错误；该开关只作为运维保护，不替代账户字段。
- 不做旧 schema 运行时兼容；上线时由用户按当前 schema 同步字段。

## 代码落点

请求侧：

- `domain/types.ts`：新增枚举类型和账户 DTO 字段。
- `storage` schema / repository：新增 `accounts.openai_responses_upstream_mode`，读写、导入导出、公开接口和授权实例来源补齐同步。
- `modules/accounts/accounts.routes.ts`：创建、编辑、草稿测试 schema 校验。
- `modules/gateway/openai-gateway-route-helpers.ts`：根据账号模式构造上游 URL。
- `modules/gateway/openai-gateway-upstream.ts`：在 `buildUpstreamRequestParts` 中调用 Responses->Chat 请求转换。
- 新增 `modules/gateway/openai-responses-chat-bridge.ts`：请求转换、非流式响应转换、SSE 事件转换的纯函数和状态机。
- `modules/gateway/openai-gateway-account-capability-filter.ts`：按 endpoint family 和账号模式过滤候选。
- `modules/accounts/account-test.service.ts`：账户测试需要覆盖 bridge 模式，优先测试 `/v1/responses` 下游形态。

返回侧：

- `modules/gateway/openai-gateway-stream.ts`：在复制上游 Chat SSE 前插入转换器，输出 Responses SSE。
- `modules/gateway/openai-gateway-response-finalization.ts` 或现有非流式响应路径：将 Chat JSON 转 Responses JSON。
- `modules/gateway/openai-gateway-stream-events.ts`：复用 Responses 事件分类和 usage 解析，必要时补 Chat chunk 到 Responses event 后的分类。

前端：

- `frontend/src/types/domain/accounts.ts`：新增类型字段。
- `frontend/src/views/accounts/accountFormTypes.ts`、`accountFormDefaults.ts`、`accountCredentials.ts`、`accountSavePayload.ts`：表单与 payload。
- `frontend/src/views/accounts/AccountStrategySection.vue`：新增中文配置项。
- `AccountTestModal.vue`：测试结果里展示实际上游模式。

文档：

- 本文。
- `OpenAI账号接入.md`：补账户能力和页面字段。
- `请求处理分层设计.md`：补请求准备层职责。
- `接口契约与权限矩阵.md`：落地后补 API 字段。
- `SQLite存储说明.md`：落地后补字段。
- `AI账户导入协议.md`：落地后补导入导出字段。

## 验证清单

实际验证结果见 [PLAN-0039 Responses 转 Chat Completions 账户适配](../plans/计划-0039-Responses转ChatCompletions账户适配.md)。本节保留作为后续扩展和真实上游联调清单。

单元测试：

- Responses string input 转 Chat messages。
- Responses message item 转 Chat messages。
- instructions 合并顺序。
- function tools 和 tool_choice 转换。
- function_call_output 转 tool message。
- 不支持内置工具时返回明确本地错误。
- Chat 非流式 content 转 Responses output_text。
- Chat 非流式 tool_calls 转 Responses function_call。
- Chat usage 转 Responses usage。
- Chat SSE content delta 转 Responses SSE。
- Chat SSE tool_calls arguments delta 转 Responses SSE。
- Chat SSE 尾包 usage 转 Responses completed usage。
- 上游错误和解析错误不产生半截伪成功。

回归测试：

- 默认 `passthrough` 行为不变。
- OAuth 账户不受新字段影响。
- `/chat/completions` 原路径不受 bridge 字段影响。
- `/responses/compact` 在 bridge 模式下按第一版策略失败。
- bridge 模式不支持的 Responses 内置工具返回本地错误，不写账号失败。
- 请求转换失败不触发账户错误处理策略。
- 上游 Chat 非 2xx 仍触发现有上游失败链路。
- 上游 Chat SSE 可见输出前 / 后失败沿用现有流式失败边界。
- bridge 关闭时不解析普通透传请求体。
- 大 JSON 请求体在 worker 中解析。
- 长流式输出和长工具参数不造成无界内存增长。
- 账号模型映射在转换前后仍记录下游模型、上游模型和计价模型。
- 授权实例从来源账户读取 bridge 模式。
- 会话亲和不能绕过 bridge endpoint 能力过滤。
- 账户测试可验证 bridge 账号。
- 使用记录、审计和流式失败账号副作用仍能记录最终上游路径、下游 endpoint 和转换模式。

手动联调：

- Codex + Chat-only OpenAI-compatible 上游的短任务。
- Codex 工具调用：至少覆盖 shell / apply_patch 这类 function call 参数流式输出。
- 长输出流式中断。
- 国内模型不支持 `reasoning_effort`、`parallel_tool_calls`、`developer` role 时的降级错误表现。

## 待核对项

- Codex 当前版本对 Responses SSE 事件的最低事件集合要求：是否必须有 `response.content_part.added/done`，还是 `response.output_text.delta` 足够。
- Codex 对 function call output item 的字段要求：`call_id`、`item_id`、`output_index` 是否必须稳定复用。
- Codex 对 `/responses/compact` 的触发频率和失败后行为：短会话是否可接受第一版不支持。
- 国内目标上游对 `developer` role、`reasoning_effort`、`parallel_tool_calls`、`response_format`、stream usage 的兼容差异。
- 国内 Chat-only 上游的上下文压缩默认不走 `/responses/compact`；如需支持，只能单独评估 `chat_summary_compact_experimental` 这类显式降级策略。
- 是否需要账户级细分选项：`chatMaxTokensField = max_tokens | max_completion_tokens`、`developerRoleMode = passthrough | system`、`reasoningMode = passthrough | remove`。
- 是否需要在审计日志 metadata 中固定记录 `upstreamEndpointFamily = chat_completions` 和 `downstreamEndpointFamily = responses`。
- CC Switch 对 `previous_response_id` 的具体本地历史策略仍需读源码核对；当前只能确认它有相关处理路径。
