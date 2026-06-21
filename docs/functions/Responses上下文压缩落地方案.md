# Responses 上下文压缩落地方案

## 文档边界

本文定义 `juhe-ai` 对 OpenAI Responses 上下文压缩能力的分层、支持范围、性能边界和失败处理。这里的“压缩”专指模型上下文压缩、裁剪或 compact，不是 HTTP `gzip` / `br` / `deflate` 传输压缩。

不在本文范围内：

- 不做 HTTP 请求体压缩重发。`Content-Encoding: gzip` 只能减少传输字节，不减少模型上下文 token，不能解决 `context_length_exceeded`。
- 不做上游已经返回 `context_length_exceeded` 后的自动 compact retry，不由网关捕获失败后 compact 并重发。
- 不在原生 Responses passthrough 或 OAuth Codex adapter 中引入服务端会话压缩状态托管。
- 不把 Chat 摘要伪装成上游原生 Responses opaque compact。
- 不为未知 OpenAI-compatible 上游默认开启 compact。
- 不在流式响应已经产生可见输出后静默重放或拼接第二条上游流。
- 不开放任意 body patch、任意 header 改写或用户自定义压缩脚本。

## 目标

- 支持原生 Responses 上游的上下文压缩能力，避免可用能力在中转层被删除或屏蔽。
- 明确协议、供应商、账户模式、客户端和运行阶段的边界，避免全局误用。
- 保持网关热路径轻量，不为了压缩能力缓存完整流、完整响应或全量会话历史。

## 概念区分

| 名称 | 作用层 | 解决什么 | 不解决什么 |
| --- | --- | --- | --- |
| HTTP 压缩 | 传输层 | 减少网络字节和带宽 | 不减少 token，不解决上下文窗口 |
| `truncation: auto` | Responses 请求语义 | 上游自动丢弃开头输入以适配窗口 | 会丢历史，不产出可复用 compact item |
| `context_management` compaction | Responses 请求语义 | 上游在阈值附近压缩上下文，并在响应里返回 compaction item | 依赖上游原生 Responses 支持和客户端后续携带 |
| `/responses/compact` | Responses 独立端点 | 显式把一组 input 压缩成新的上下文 | compact 请求本身仍必须能放进上游窗口 |
| `compaction_trigger` | Codex Responses input item | Codex Remote Compaction V2 通过普通 `/responses` 产出 `compaction` item | 属于 Codex SDK 特性，不是通用 OpenAI-compatible 默认能力 |

## 官方能力口径

当前可依赖的官方 Responses 能力包括：

- `truncation`：`disabled` 时请求超过上下文窗口会失败；`auto` 时上游可以丢弃输入开头来适配窗口。
- `context_management`：可以请求上游在上下文达到阈值后做 compaction，并把结果作为响应 output item 返回给客户端。
- `/responses/compact`：显式发起 compact，返回可用于后续请求的压缩输出。

实现时必须把这三类能力区分开：

- `truncation` 是裁剪，不是压缩。
- `context_management` 是随正常响应产生压缩结果，适合客户端能理解 Responses output item 的场景。
- `/responses/compact` 是单独请求，适合客户端主动 compact 或中转按请求显式透传。
- Codex SDK 当前没有向普通 `ResponsesApiRequest` 写入 `context_management` 或官方 `truncation` 字段；Codex 的 `truncation_policy` 是本地工具输出截断配置，不是 OpenAI Responses 请求字段。

## 分层支持矩阵

上下文压缩必须同时满足协议层、账户模式层和客户端层条件，不能只按供应商全局开启。

| 层级 | 判断项 | 当前策略 |
| --- | --- | --- |
| 协议层 | OpenAI Responses | 只在 `POST /responses`、`POST /v1/responses` 和 `/responses/compact` 相关路径内处理；`compaction_trigger` 仅作为 Codex client profile 能力处理 |
| 供应商层 | `openai` / `gpt` 等 OpenAI v1 供应商 | 供应商层只表达候选范围，不单独决定可用 |
| 账户模式层 | API Key 原生 Responses 透传 | 主要支持对象 |
| 账户模式层 | OAuth Codex adapter / Codex compatibility | 继续保持保守；Codex backend 已知会拒绝部分 Responses 字段，重点验证 `/responses/compact` 和 Codex V2 `compaction_trigger` |
| 客户端层 | Codex / Responses-aware 客户端 | 可以主动调用 `/responses/compact`、发送 Codex V2 `compaction_trigger` 或显式携带官方字段 |
| 客户端层 | 普通 OpenAI-compatible 客户端 | 只透传客户端显式字段，不主动替它维护上下文 |
| 运行阶段 | 请求前 | 原生 Responses 可透传官方字段 |

## 当前范围

当前目标是修通官方 Responses 上下文压缩链路。原生 passthrough 不做自动 compact 重发，也不在上游已经失败后重放。

### API Key 原生 Responses 透传

适用条件：

- 账户类型为 API Key。
- 请求为 `POST /responses` 或 `POST /v1/responses`。
- 当前请求体是 JSON 对象，或者走 raw passthrough 且不需要本地改写。

处理规则：

- 客户端显式传入的 `context_management` 默认保留。
- 客户端显式传入的 `truncation` 默认保留。
- 不默认注入 `context_management`，避免普通客户端在不知情情况下改变上下文语义。
- `/responses/compact` 按原生 Responses 路径透传；能否成功由当前上游决定。Codex SDK 的 compact payload 包含 `model`、`input`、`instructions`、`tools`、`parallel_tool_calls`、`reasoning`、`service_tier`、`prompt_cache_key` 和 `text`，透传或改写时不能误删 `service_tier`、`prompt_cache_key`。
- Codex Remote Compaction V2 会在普通 `/responses` 的 `input` 末尾追加 `{"type":"compaction_trigger"}`，并期望流式响应里恰好出现一个 `compaction` output item；该能力只能按 Codex client profile 和账号能力显式承接。
- 审计和使用记录需要保留请求里是否带有 `context_management`、`truncation`、是否访问 `/responses/compact` 的轻量 metadata，但不得记录完整大上下文副本。

### Codex Responses 请求形态

当前 `requestClientCompatibility = codex_responses` 的请求形态会整理请求体、补齐 Codex 需要的字段，并删除部分字段。参考实现里 `CLIProxyAPI` 的 Codex translator 明确删除 `context_management` 和 `truncation`，并标注 Codex `/responses` 会返回 `Unsupported parameter: context_management`。Codex SDK 本身也没有在普通 `ResponsesApiRequest` 中发送 `context_management`。因此当前不把 `context_management` 当作 Codex `/responses` 可透传字段。

- 继续删除 `context_management` 和 `truncation`，避免 Codex backend 直接拒绝请求。
- 不由兼容层自动生成 compact 配置，除非后续新增明确账户策略。
- `/responses/compact` 是当前必须验证的 Codex compact 入口。
- `compaction_trigger` 是 Codex Remote Compaction V2 入口；该特性在 Codex SDK 中标记为 under development 且默认关闭，当前只作为 client profile 识别和透传兼容项，不作为普通客户端能力开放。
- 继续保留 `stream = true`、`store = false` 和 Codex 必需 include 的当前策略。

### OAuth Codex adapter

OAuth Codex adapter 面向 ChatGPT / Codex backend，不等价于公开 OpenAI API Key 透传。

当前策略：

- 不默认开放 `context_management` 或 `truncation`。
- `/responses/compact` 保持现有可承接路径，并按账号 compact 能力单独筛选。
- `compaction_trigger` 仅在明确识别为 Codex Remote Compaction V2 请求时承接；未知客户端发送同名 item 不触发额外本地语义。
- 如果未来真实验证证明 Codex backend 支持 `context_management`，再以 OAuth Codex adapter 内部能力单独开启；开启前不得影响 API Key 原生 Responses 透传。
- 不把 OAuth 验证结果反推到 API Key 或其他 OpenAI-compatible 上游。

### 国内 Chat-only 上游压缩结论

多数国内 OpenAI v1 兼容模型当前主要承接 `/chat/completions`，上下文压缩通常由客户端或代理自行完成，而不是依赖官方 Responses `/responses/compact`。

常见做法：

- 截断旧 `messages`，只保留最近若干轮。
- 额外调用一次 Chat 模型，把旧历史总结成明文摘要，再作为后续 `system` / `user` 消息携带。
- 通过 RAG / 检索系统按需回填历史、文件和代码片段。
- 选择更长上下文窗口的模型，降低压缩触发频率。
- 使用供应商自有的 session、cache 或 Responses-like 能力；只有该能力明确支持 `/responses/compact` 时才应走原生透传。

落地结论：

- 国内 Chat-only 上游没有官方 Responses compact 等价能力。
- 需要长会话、compact 或稳定 `previous_response_id` 的客户端应选择原生 Responses 上游。

## 请求处理落点

请求侧落点：

- endpoint family 和 `/responses/compact` 能力判断在候选账号筛选层；不支持该端点的账户不进入候选。
- API Key 原生 Responses 透传时，`context_management` / `truncation` 保留在上游请求准备层。
- `codex_responses` 请求形态的字段保留或删除，也在上游请求准备层。
- Codex V2 `compaction_trigger` 在原生 Responses / Codex adapter 路径中必须作为 Responses input item 透传，不转换为 `context_management`，也不在网关生成该 item。

返回侧落点：

- 普通原生 Responses 上游 SSE 里的 compaction item 仍按 Responses SSE 事件透传，不由网关安装或维护客户端历史。
- 明确识别为 Codex compact 期望的请求，包括 `/responses/compact` 以及带 `compaction_trigger` 的 Codex `/responses`，返回侧必须执行最小契约检查：`response.completed` 前按 Codex Remote Compaction V2 语义统计 `response.output_item.done` 中 Codex 可反序列化的 compact item 数量。
- 可接受 compact item 最小形状为 `type = "compaction"` 或 Codex 接受的别名 `type = "compaction_summary"`，且 `encrypted_content` 必须是字符串；只有类型相同但字段缺失、为 `null` 或非字符串时不能算作合法 compact 输出。
- Codex compact SSE 的 `response.completed.response.id` 必须是字符串；缺失或非字符串会导致 Codex SSE parser 解析失败，因此同样按 compact 契约错误拦截。非流式 `/responses/compact` JSON 只按 `output` 数组校验，不要求外层 `response.id`。
- Codex compact 期望请求在最终确认前不得把暂存的 output item 先写给客户端；如果完成时不是恰好 1 个 Codex 可接受的 compact output item，则生成 `codex_compaction_contract_mismatch` 响应语义错误，交给“响应检查策略”的系统默认规则触发服务端换号重试或最终可重试失败。
- 自动摘要只能发生在本轮可见输出开始前；网关不伪造 `compaction` item，也不把普通 output item 转换成 compact 结果。

## 性能边界

- 请求路径不得为了 compact 解析普通大 JSON 的完整深层结构；只在必须改写请求体的模式下进入 worker thread 解析。
- 不在流式拦截器里缓存完整 SSE 流或跨事件全文；Codex compact 契约检查只允许暂存完成前的有限 SSE 事件窗口，超过上限按不可接受响应处理。
- 不在请求链路扫描审计 payload、使用记录或历史请求来找可压缩上下文。
- compact 输出如果需要用于审计或排障摘要，必须有字节上限，超过上限只记录截断摘要。
- Codex V2 `compaction_trigger` 只增加 compact 契约所需的有界完成前暂存，不做全量整流缓存。
- 后续如果重新评估本地摘要能力，必须另开计划并先补有界 payload 读取、成本归属和失败终态设计。
- 所有 compact 相关列表、审计详情或排障接口都按现有 offset / limit / 窗口读取边界处理。
- 大请求体解析继续遵守当前 `/v1` raw body hard limit、文本 lane 上限和 worker 解析阈值。

## 错误处理

| 场景 | 处理 |
| --- | --- |
| 客户端显式 `context_management` 字段格式非法 | 原生透传模式不本地校验，交给上游；本地改写模式需要返回本地 `400` |
| 上游不支持 `context_management` | 按上游失败处理，不标记本地 bug |
| compact 请求自身超上下文 | 返回 compact 失败，不继续重试 |
| Codex compact 期望请求完成时没有恰好 1 个 `compaction` output item | 命中系统默认响应检查规则 `default_codex_compaction_contract`，下游未写出时服务端换号重试；账号耗尽时按 Codex 客户端能力返回可重试失败 |
| `/responses` 请求出现 `context_length_exceeded` | 不触发自动 compact；按现有上游错误或流式失败处理 |

## 审计与使用记录

需要新增或保留的轻量 metadata：

- `contextManagementPresent`
- `truncationMode`
- `compactEndpointRequested`
- `compactionTriggerPresent`
- `contextCompressionAccountMode`

审计正文仍按原始审计保全策略处理：

- 不因为 compact 能力保存更多完整大请求体。
- 不把 compact 输出伪装成客户端原始输入。
- `/responses/compact` 作为独立请求记录，不和普通 `/responses` 请求混在同一上游尝试链路里。
- `compaction_trigger` 作为普通 `/responses` 请求的 input item 轻量标记，不记录完整上下文。

## 前端与配置建议

当前不新增全局系统开关，也不在账户编辑页暴露专用摘要模型配置。

账户编辑中可以后续增加只读或受控配置：

| 配置 | 当前建议 |
| --- | --- |
| `Responses compact 支持` | 原生 Responses API Key 账户默认“透传客户端请求” |
| `保留 context_management` | 原生 Responses 默认开启；Codex compatibility 当前不开放 |
| `保留 truncation` | 原生 Responses 默认按客户端显式字段透传；Codex compatibility 当前不开放 |
| `Codex Remote Compaction V2` | 当前不做普通开关；仅在 Codex client profile 和账号能力验证后承接 |

页面文案必须明确：

- “上下文压缩”不是“HTTP 压缩”。
- `truncation:auto` 可能丢弃旧上下文。
- 需要 compact 的客户端应使用原生 Responses passthrough / OAuth Codex adapter，或自行摘要后开启新会话。

## 验证计划

当前回归：

- API Key 原生 Responses 透传：客户端传入 `context_management` 时，上游请求保留该字段。
- API Key 原生 Responses 透传：客户端传入 `truncation` 时，上游请求保留该字段。
- `codex_responses` 请求形态：继续删除 `context_management` 和 `truncation`。
- `codex_responses` 请求形态：`/responses/compact` 只在账号能力允许时承接。
- `codex_responses` 请求形态：`compaction_trigger` 作为 Responses input item 透传，不被工具归一化或消息转换误删。
- Codex compact 响应契约：mock 上游返回 2 个非 compaction output item 时，网关不泄露坏 output，并切到下一个 Codex 兼容账号重试。
- OAuth Codex adapter：默认仍按现有字段边界，不被 API Key 调整影响。
- 流式拦截：`context_length_exceeded` 仍按现有客户端策略处理，不触发 compact。
- 性能：大请求体在不需要改写时仍 raw passthrough，不新增主线程完整解析。

## 不做范围

- 不把 compact 做成所有 OpenAI-compatible 上游的默认能力。
- 不把 Chat Completions 历史 messages 的摘要伪装成上游原生 opaque compact。
- 不为普通客户端自动替换上下文。
- 不做专用摘要模型配置。
- 不在前端提供“任意压缩 prompt 模板”。
- 不引入 Redis、Kafka、对象存储或外部分布式会话压缩状态。
