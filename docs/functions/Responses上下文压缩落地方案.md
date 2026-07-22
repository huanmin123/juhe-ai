# Responses 上下文压缩落地方案

## 文档边界

本文定义 `juhe-ai` 对 OpenAI Responses 上下文压缩能力的分层、支持范围、性能边界和失败处理。这里的“压缩”专指模型上下文压缩、裁剪或 compact，不是 HTTP `gzip` / `br` / `deflate` 传输压缩。

不在本文范围内：

- 不做 HTTP 请求体压缩重发。`Content-Encoding: gzip` 只能减少传输字节，不减少模型上下文 token，不能解决 `context_length_exceeded`。
- 不做上游已经返回 `context_length_exceeded` 后的自动 compact retry，不由网关捕获失败后 compact 并重发。
- 不在原生 Responses passthrough 或 OAuth Codex adapter 中额外托管服务端会话状态；这两类路径仍以原生上游语义为准。
- 不把 Chat 摘要宣称为上游原生 Responses opaque compact；Chat-only bridge 只能使用网关自有 compact envelope，并由网关负责恢复。
- 不为未知 OpenAI-compatible 上游默认开启 compact。
- 不在流式响应已经产生可见输出后静默重放或拼接第二条上游流。
- 不开放任意 body patch、任意 header 改写或用户自定义压缩脚本。

## 目标

- 支持原生 Responses 上游的上下文压缩能力，避免可用能力在中转层被删除或屏蔽。
- 为 Chat-only bridge 设计 `previous_response_id` 服务端状态层，避免 Codex 客户端增量上下文丢失。
- 为 Chat-only bridge 设计可控的网关摘要压缩能力，在当前分组和当前供应商内调度摘要模型，避免跨授权边界兜底。
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
| `previous_response_id` 状态层 | 网关服务端 | 把 Codex / Responses 增量请求还原为完整上下文 | 上游 Chat 不认识该字段，必须在网关内解析和恢复 |
| `gateway_summary_compact` | 网关服务端 + Chat 上游 | 调用同分组、同供应商内的 Chat Completions 摘要模型生成结构化摘要，再包装成网关自有 compact envelope | 不等价于上游原生 opaque compact，不能跨网关或跨供应商直接消费；内部摘要请求不能打上游 `/responses/compact` |

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
| 桥接层 | Responses -> Chat bridge | 目标设计中由网关托管 `previous_response_id` 和自有 compact envelope；上游仍只看到 Chat messages |
| 内部摘要调度 | 当前分组 + 当前供应商 | 摘要请求按普通调度链路选可用账户、切号、统计和审计；禁止跨分组、跨供应商和递归 compact |

## 当前范围

当前目标分为两条能力线：

- 原生 Responses compact：修通官方 Responses 上下文压缩链路。原生 passthrough 不做自动 compact 重发，也不在上游已经失败后重放。
- Chat-only gateway compact：为 Responses -> Chat bridge 设计服务端状态和摘要压缩。上游 Chat 只负责生成摘要，协议兼容、状态恢复、compact envelope 和后续展开都由网关负责。

### 能力类型和路由原则

| 能力类型 | 触发入口 | 上游要求 | 网关职责 | 后续请求路由 |
| --- | --- | --- | --- | --- |
| `native_responses_compact` | `/responses/compact` 或原生 Responses compaction | 上游支持 Responses compact，返回 `compaction` / `compaction_summary` 且可被后续 `/responses` 消费 | 透传、契约检查、统计审计 | 必须继续走可消费该 opaque item 的原生 Responses 上游 |
| `chat_bridge_state` | `/responses` 携带 `previous_response_id` | 上游不需要认识 `previous_response_id` | 根据 response id 恢复历史，追加本轮 input，转为 Chat messages | 当前分组和当前供应商内的 Chat bridge 账户 |
| `gateway_summary_compact` | Chat-only bridge 的 `/responses/compact` 或内部压缩触发 | 同分组、同供应商内存在可用 Chat Completions 摘要模型或当前模型可摘要 | 还原完整上下文，通过 Chat Completions 摘要请求生成摘要，保存 compact snapshot，返回网关自有 envelope | 后续必须回到本网关，由网关解包后转 Chat |

不能只按模型名判断 compact 能力。例如 `gpt-5.5-openai-compact` 经实测可以作为 `native_responses_compact` 的上游模型，但它产生的 `encrypted_content` 只适合同类 Responses 上游继续消费，不能直接给 GLM / DeepSeek / Anthropic Chat bridge 使用。

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

- 国内 Chat-only 上游通常没有官方 Responses compact 等价能力，也通常不认识 `previous_response_id`。
- Chat-only bridge 不能把 native Responses compact item 直接交给 Chat 上游，也不能要求 Chat 上游返回 Responses compact item。
- 可行方案是网关先维护 `previous_response_id` 对应的完整上下文，再把需要压缩的上下文转成 Chat messages，通过当前分组和当前供应商内的 Chat Completions 摘要请求交给上游摘要模型。上游返回普通摘要后，由网关保存为 compact snapshot，并包装成 Codex 可识别的 `compaction_summary`。

### Chat-only bridge 服务端状态

`previous_response_id` 是 Chat-only compact 的基础能力。没有状态层时，compact 会退化成无依据摘要，容易丢记忆并诱发幻觉。

目标状态记录：

| 字段 | 用途 |
| --- | --- |
| `responseId` | 对应下游返回给客户端的 Responses id |
| `apiKeyId` / `teamId` / `groupId` | 保证状态只在当前授权边界内可见 |
| `providerCode` / `providerProtocolProfileId` | 保证恢复和摘要只在当前供应商能力范围内进行 |
| `model` / `mappedModel` | 记录下游模型和实际上游模型，供后续恢复和成本口径使用 |
| `payloadRef` / `checkpointRef` | 指向本地 data 文件中的 Responses 语义历史和 Chat checkpoint |
| `toolStateRef` | 指向本地 data 文件中的未闭合工具调用、最近工具结果和必须保留的工具链 |
| `compactSnapshotId` | 如果该状态来自 compact，记录 compact snapshot 来源 |
| `stateHash` / `expiresAt` | 防篡改、去重、TTL 清理和排障 |

存储边界：

- `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` 指向的 Responses 桥接状态索引 shard 只保存关系索引、授权边界、`storage_key`、`storage_offset_bytes`、`raw_size_bytes`、`compressed_size_bytes`、`sha256`、`lastUsedAt` 和 `expiresAt`，不得保存完整用户上下文、完整工具参数、完整模型输出或大段 compact payload；这些表不属于业务库。
- 完整上下文落在 `backend/data/codex-context/`，按 `sessionId` hash 目录和小时分段追加 gzip segment；索引通过 `storage_key + storage_offset_bytes + compressed_size_bytes + sha256` 精确定位单条 payload。
- `responseId -> sessionId -> segment offset` 是恢复主索引；每次成功响应只追加本轮 request input / instructions 和 output items，不为每个 response id 复制一份完整历史文件。
- 文件写入先 gzip 单条 payload，再追加到 session/hour segment；SQLite 事务只在 append 成功和 hash 计算完成后提交引用，避免数据库指向不存在的 payload。
- 恢复读取必须按 SQLite 中记录的 `storage_key`、`storage_offset_bytes`、`compressed_size_bytes` 和 `sha256` 做有界读取，不扫描整个上下文目录，也不从审计日志或使用记录反查历史。
- `lastUsedAt` 在成功读取、续写或 compact 时刷新；超过 7 天没有继续使用的 session 由后台清理删除过期 Responses 桥接状态索引关系。segment 文件只有在没有任何剩余 response / compact 引用时才会删除，避免同 session/hour 内仍有活跃上下文时误删。清理后客户端再携带旧 `previous_response_id` 或 `juhecmp.v2` compact snapshot 时返回受控的状态不存在错误。

状态提交规则：

- 只在上游请求成功完成后提交状态；失败、下游中断、流式未完成时不生成可续接状态。
- `previous_response_id` 找不到、过期、跨 API Key、跨团队、跨分组或跨供应商时，返回受控错误，不继续无状态转换。
- 新一轮 `instructions` / developer 上下文按 Responses 语义以本轮请求为准；旧 instructions 不应无条件继承。
- 工具调用未闭合时，不能只靠摘要保留，必须保留最近完整工具调用链或拒绝 compact。

### Gateway summary compact

Chat-only bridge 的 compact 输出是网关自有 envelope，不是上游原生 opaque compact。当前已落地版本使用 `compact_id + digest + compact snapshot`：

```json
{
  "type": "compaction_summary",
  "encrypted_content": "juhecmp.v2.<compact_id>.<sha256-summary-digest>"
}
```

`encrypted_content` 对 Codex 是不透明字符串；Codex 后续会把它带回请求。当前实现中，网关识别 `juhecmp.v2` 后按 compact id 读取 snapshot，校验 API Key / 分组 / 供应商档案边界、TTL 和摘要 digest，再作为 Chat system summary 展开发给当前分组和当前供应商内的上游。

后续增强应把摘要内容提升为结构化 schema，而不是只依赖一段自由文本：

| 字段 | 要求 |
| --- | --- |
| `durableFacts` | 任务目标、业务事实、长期约束 |
| `currentGoal` | 当前未完成目标和下一步 |
| `recentUserIntent` | 最近用户意图，避免摘要只保留早期信息 |
| `toolState` | 必须保留的工具调用结果、未闭合调用和调用约束 |
| `filesAndArtifacts` | 已读写文件、关键路径、生成物和待验证项 |
| `providerConstraints` | 当前供应商、模型、协议限制和不可跨用的 compact 来源 |
| `risks` | 已知风险、失败原因、不能静默忽略的问题 |

摘要模型调度规则：

- 摘要兜底必须在当前 API Key 绑定的当前分组内进行。
- 摘要兜底必须限定在当前供应商和当前 provider profile 可接受范围内，不跨供应商寻找便宜模型。
- Chat-only bridge 的通用摘要路径固定使用 Chat Completions endpoint family，即 OpenAI-compatible 语义下的 `/v1/chat/completions`；不能在 Responses -> Chat 转换后再向 Chat 上游发送 `/v1/responses/compact`。
- 内部摘要请求走正常候选账号筛选、账号可用性、冷却、切号、统计、审计和错误处理，但候选账号必须支持 Chat Completions 摘要请求。
- 内部摘要请求的模型别名按原始 Responses compact 请求的 `Responses` 源协议匹配；命中后只改写 Chat Completions 请求体里的 `model` 和统计 / 审计上游模型口径，不把上游路径改成 `/responses`。
- 当前内部摘要请求通过合成的非流式 `/v1/chat/completions` 请求进入同分组同供应商调度；后续应补充 `purpose = codex_compaction_summary`、`disableCompact = true` 等内部元数据，避免递归 compact。
- 如果供应商明确提供非 Responses 的 Chat 专用 compact endpoint，可以作为供应商特化摘要入口；否则使用当前分组、当前供应商内配置的 Chat Completions 摘要模型。无论哪种方式，都不能使用上游 `/responses/compact` 作为 Chat-only 摘要兜底。
- 允许摘要模型是专用压缩模型，也允许是普通 Chat 模型。当前实现先校验摘要为非空文本；后续 snapshot 版本再提升为固定 schema 校验。

摘要失败处理：

- 当前摘要为空时返回受控失败；后续结构化 schema 校验失败时可在同一内部摘要链路内重试或切到同分组同供应商的其他摘要账户。
- 后续结构化摘要结果缺少工具状态、当前目标或关键约束时不得返回成功 compact。
- compact 前的完整状态按 TTL 保留，支持后续重新压缩或排障，但不得无限期保存大上下文。
- 当前 compact 成功后，后续请求使用 compact snapshot 中的 summary 作为 Chat system context；后续再增加最近未压缩历史和更严格的工具状态保留。

## 请求处理落点

请求侧落点：

- endpoint family 和 `/responses/compact` 能力判断在候选账号筛选层；不支持该端点的账户不进入候选。
- API Key 原生 Responses 透传时，`context_management` / `truncation` 保留在上游请求准备层。
- `codex_responses` 请求形态的字段保留或删除，也在上游请求准备层。
- Codex V2 `compaction_trigger` 在原生 Responses / Codex adapter 路径中必须作为 Responses input item 透传，不转换为 `context_management`，也不在网关生成该 item。
- Chat-only bridge 的 `previous_response_id` 解析和上下文恢复应在上游请求准备前完成；恢复失败时不进入候选账号派发。
- Chat-only bridge 的内部摘要请求应作为受控内部 Chat Completions 请求重新进入候选账号筛选层，但携带 `purpose = codex_compaction_summary`、`allowCrossGroup = false`、`allowCrossProvider = false`、`disableCompact = true`。该内部请求的上游 endpoint family 是 Chat Completions，不能是 `/responses/compact`。

返回侧落点：

- 普通原生 Responses 上游 SSE 里的 compaction item 仍按 Responses SSE 事件透传，不由网关安装或维护客户端历史。
- 明确识别为 Codex compact 期望的请求，包括 `/responses/compact` 以及带 `compaction_trigger` 的 Codex `/responses`，返回侧必须执行最小契约检查：`response.completed` 前按 Codex Remote Compaction V2 语义统计 `response.output_item.done` 中 Codex 可反序列化的 compact item 数量。
- 可接受 compact item 最小形状为 `type = "compaction"` 或 Codex 接受的别名 `type = "compaction_summary"`，且 `encrypted_content` 必须是字符串；只有类型相同但字段缺失、为 `null` 或非字符串时不能算作合法 compact 输出。
- Codex compact SSE 的 `response.completed.response.id` 必须是字符串；缺失或非字符串会导致 Codex SSE parser 解析失败，因此同样按 compact 契约错误拦截。非流式 `/responses/compact` JSON 只按 `output` 数组校验，不要求外层 `response.id`。
- Codex compact 期望请求在最终确认前不得把暂存的 output item 先写给客户端；如果完成时不是恰好 1 个 Codex 可接受的 compact output item，则生成 `codex_compaction_contract_mismatch` 响应语义错误，交给“响应检查策略”的系统默认规则触发服务端换号重试或最终可重试失败。
- Chat-only gateway compact 只能发生在本轮可见输出开始前；网关可以返回自有 `compaction_summary` envelope，并在后续请求中由网关按 compact snapshot 恢复，不能把它标记为上游原生 compact。

## 性能边界

- 请求路径不得为了 compact 解析普通大 JSON 的完整深层结构；只在必须改写请求体的模式下进入 worker thread 解析。
- 不在响应语义检查路径缓存完整 SSE 流或跨事件全文；Codex compact 契约检查只允许暂存完成前的有限 SSE 事件窗口，超过上限按不可接受响应处理。
- 不在请求链路扫描审计 payload、使用记录或历史请求来找可压缩上下文。
- compact 输出如果需要用于审计或排障摘要，必须有字节上限，超过上限只记录截断摘要。
- Codex V2 `compaction_trigger` 只增加 compact 契约所需的有界完成前暂存，不做全量整流缓存。
- Chat-only 状态恢复只能读取按 `responseId` 直接索引的 compact state，不得反查或扫描历史请求。
- 内部摘要请求的输入需要有 token / 字节上限；超限时先按策略保留最近轮次和工具状态，再摘要旧历史，不能把超大上下文一次性塞给上游。
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
| Chat-only `previous_response_id` 找不到、过期或跨授权边界 | 返回受控错误，不请求上游，不污染账号健康 |
| Chat-only 内部摘要模型不可用 | 在当前分组和当前供应商内按普通调度切号；耗尽后返回 compact 失败 |
| Chat-only 摘要 schema 校验失败 | 可重试或切号；最终失败时不生成 compact envelope |
| compact snapshot 签名无效或来源不匹配 | 视为不可恢复状态，返回受控错误 |

## 审计与使用记录

需要新增或保留的轻量 metadata：

- `contextManagementPresent`
- `truncationMode`
- `compactEndpointRequested`
- `compactionTriggerPresent`
- `contextCompressionAccountMode`
- `previousResponseStateMode`
- `summaryCompactMode`
- `summaryCompactProviderCode`
- `summaryCompactModel`
- `summaryCompactSnapshotId`
- `summaryCompactValidationStatus`

审计正文仍按原始审计保全策略处理：

- 不因为 compact 能力保存更多完整大请求体。
- 不把 compact 输出伪装成客户端原始输入；网关自有 envelope 只记录 compact id、签名状态、摘要模型和摘要校验结果。
- `/responses/compact` 作为独立请求记录，不和普通 `/responses` 请求混在同一上游尝试链路里。
- `compaction_trigger` 作为普通 `/responses` 请求的 input item 轻量标记，不记录完整上下文。

## 前端与配置建议

配置必须遵守授权边界。Chat-only 摘要兜底不是全局直连模型，而是当前分组和当前供应商内的一次内部正常调度请求。

账户、供应商或分组中可以后续增加只读或受控配置：

| 配置 | 当前建议 |
| --- | --- |
| `Responses compact 支持` | 原生 Responses API Key 账户默认“透传客户端请求” |
| `保留 context_management` | 原生 Responses 默认开启；Codex compatibility 当前不开放 |
| `保留 truncation` | 原生 Responses 默认按客户端显式字段透传；Codex compatibility 当前不开放 |
| `Codex Remote Compaction V2` | 当前不做普通开关；仅在 Codex client profile 和账号能力验证后承接 |
| `Chat-only previous_response_id 状态` | 作为 Codex bridge 基础能力设计；开启后按 TTL 保存可续接状态 |
| `Chat-only 摘要压缩模式` | `disabled` / `chat_completions_summary` / `provider_chat_compact`，默认可先保持 `disabled`；通用模式固定走 Chat Completions，不走上游 `/responses/compact` |
| `摘要模型` | 只能选择当前分组、当前供应商内可调度的 Chat Completions 模型；不能配置跨供应商全局模型 |
| `保留最近轮次` | 压缩时始终保留最近 N 轮原文和必要工具链 |
| `compact snapshot TTL` | 控制状态保留周期和清理策略 |

页面文案必须明确：

- “上下文压缩”不是“HTTP 压缩”。
- `truncation:auto` 可能丢弃旧上下文。
- 原生 Responses compact 和 Chat-only gateway summary compact 不是同一种能力。
- Chat-only compact 只在本网关内可恢复，不应承诺跨中转或跨供应商可用。

## 验证计划

当前回归：

- API Key 原生 Responses 透传：客户端传入 `context_management` 时，上游请求保留该字段。
- API Key 原生 Responses 透传：客户端传入 `truncation` 时，上游请求保留该字段。
- `codex_responses` 请求形态：继续删除 `context_management` 和 `truncation`。
- `codex_responses` 请求形态：`/responses/compact` 只在账号能力允许时承接。
- `codex_responses` 请求形态：`compaction_trigger` 作为 Responses input item 透传，不被工具归一化或消息转换误删。
- Codex compact 响应契约：mock 上游返回 2 个非 compaction output item 时，网关不泄露坏 output，并切到下一个 Codex 兼容账号重试。
- OAuth Codex adapter：默认仍按现有字段边界，不被 API Key 调整影响。
- 流式失败处理：`context_length_exceeded` 仍按现有客户端策略处理，不触发 compact。
- 性能：大请求体在不需要改写时仍 raw passthrough，不新增主线程完整解析。
- Chat-only bridge：首轮 `/responses` 成功后生成 `response_id` 状态；第二轮带 `previous_response_id` 时能还原历史并转 Chat。
- Chat-only bridge：`previous_response_id` 跨 API Key、跨分组、跨供应商或过期时受控失败，且不命中上游。
- Chat-only compact：当前分组当前供应商内有摘要账户时，内部摘要请求按普通调度链路切号并记录 usage。
- Chat-only compact：摘要成功后返回 1 个 `compaction_summary` envelope，后续请求带回该 envelope 时能恢复为 Chat summary + 最近历史。
- Chat-only compact：摘要 schema 缺关键字段、工具调用未闭合或内部摘要账户耗尽时不生成 compact envelope。

## 不做范围

- 不把 compact 做成所有 OpenAI-compatible 上游的默认能力。
- 不把 Chat Completions 历史 messages 的摘要伪装成上游原生 opaque compact；只能作为网关自有 envelope。
- 不为普通客户端自动替换上下文。
- 不做跨分组、跨供应商、绕过授权和统计的专用摘要模型直连。
- 不在前端提供“任意压缩 prompt 模板”。
- 不引入 Redis、Kafka、对象存储或外部分布式会话压缩状态。
