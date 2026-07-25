# Gemini 协议兼容设计

> 2026-06-27 路由分层更新：本文旧段落里提到的 API Key 显式桥接配置只作为历史背景；当前目标是 API Key 只绑定策略路由，策略路由负责分组和模型调度，Gemini native 与 OpenAI / Anthropic 之间的跨协议转换落到混合供应商账户。
> 当前代码已移除 API Key / 策略路由层的显式跨协议桥接入口；本文后续如果仍出现“API Key 显式混合路由”，均表示待迁移历史设计，不得作为新增实现、测试断言或页面配置依据。跨协议承接统一迁移到混合供应商账户。

## 范围

本文说明 Gemini native、Gemini 官方 OpenAI compatibility、`gemini-cli`、普通 OpenAI SDK 和本项目现有协议桥接之间的边界。本文不重复账号创建字段，账号接入见 [Gemini 账号接入](Gemini账号接入.md)。

核心决策：

- Gemini native 是独立协议 `gemini/v1beta`，使用自己的请求体、路径模型、SSE chunk、usage 和错误结构；Interactions 是同一协议下独立的 `interactions` endpoint family，不复用 GenerateContent 语义。
- Gemini 官方 OpenAI compatibility 是 OpenAI Chat Completions 兼容入口，在本项目中落为 `profile_gemini_openai_chat_v1beta`，不是本项目自研协议转换。
- OpenAI Chat / Responses 和 Anthropic Messages 如需桥接到 Gemini native 生成类上游，必须命中混合供应商账户；混合账户右侧只暴露 `generate_content`，运行时按下游请求是否流式动态选择 `:generateContent` 或 `:streamGenerateContent?alt=sse`。
- Gemini native `generateContent` / `streamGenerateContent` 可以作为混合供应商账户的下游来源，桥接到 OpenAI Chat Completions 上游；下游响应仍渲染为 Gemini JSON / SSE。
- Gemini native `generateContent` / `streamGenerateContent` 也可以通过混合供应商账户桥接到 Anthropic Messages 上游；下游响应仍渲染为 Gemini JSON / SSE，不合成 Anthropic 响应给 Gemini 客户端。
- 客户端使用什么协议，网关先按什么协议解析：Gemini native 请求默认走 Gemini native 档案；如果命中混合供应商账户中的 `generate_content|stream_generate_content -> chat_completions` 或 `generate_content|stream_generate_content -> messages` 配置，则由该混合账户的真实 Chat 或 Anthropic Messages 上游承接；OpenAI / Anthropic 请求默认走各自协议候选，只有命中混合供应商账户中的 `chat_completions|responses|messages -> generate_content` 配置时才进入 Gemini native 真实上游。

## 协议形态对比

| 维度 | Gemini native | Gemini OpenAI compatibility | 本项目处理 |
| --- | --- | --- | --- |
| 下游路径 | `/v1beta/models/{model}:generateContent` | `/v1/chat/completions` 或 OpenAI SDK 等价路径 | 按路径识别协议，不按模型名猜 |
| Interactions 路径 | `/v1beta/interactions`、`/v1beta/interactions/{id}`、`/v1beta/interactions/{id}/cancel` | 不适用 | 按 `interactions_json` / `interactions_sse` 独立调度 |
| 上游默认 Base URL | `https://generativelanguage.googleapis.com` | `https://generativelanguage.googleapis.com/v1beta/openai` | 分属不同协议档案 |
| 模型字段 | 路径 `{model}` | 请求体 `model` | 各自协议适配器提取 |
| 请求内容 | `contents[].parts[]`、`systemInstruction`、`tools`、`generationConfig` | `messages[]`、`tools`、`tool_choice` 等 OpenAI Chat 字段 | 默认不互转；只有混合供应商账户会在 Gemini native 与 OpenAI / Anthropic 之间重构生成类请求 |
| 流式 | `:streamGenerateContent?alt=sse`，`data: GenerateContentResponse` | OpenAI Chat SSE chunk | 保持各自原始事件 |
| Interactions 流式 | POST body `stream: true` 或 GET query `stream=true`，并使用 `Accept: text/event-stream` | 不适用 | 从 `data:` JSON 的 `event_type` 识别事件，不追加 `alt=sse`，不合成 GenerateContent chunk |
| usage | `usageMetadata` | OpenAI compatible `usage` | 单独 usage semantic |
| 错误 | Google API error shape | OpenAI error shape | 按下游协议渲染本地错误 |
| 全功能覆盖 | Gemini 原生能力最完整 | OpenAI Chat 子集 / 兼容层 | Gemini native 是默认深度适配目标 |

## 路由原则

运行时必须按“请求协议 + endpoint family + model route + API Key 所选路由策略分组”共同收敛：

1. 先由路径识别下游协议和 endpoint family。
2. Gemini native 路径解析为 `protocolCode = gemini`、`endpointFamily = generate_content|stream_generate_content|count_tokens|interactions|...`；GenerateContent 从路径提取模型，Interactions 从请求体 / Interaction 资源语义读取目标模型。
3. Gemini native `generate_content` / `stream_generate_content` 默认走 Gemini native 档案候选；如果策略路由最终命中混合供应商账户，则由该账户配置决定是否进入 OpenAI Chat 或 Anthropic Messages 真实上游。
4. OpenAI Chat / Responses 路径先按 OpenAI 协议解析 source family；命中混合供应商账户中的 `chat_completions|responses -> generate_content` 配置时，才进入 Gemini native 真实上游。
5. Anthropic Messages 路径先按 Anthropic 协议解析 source family；命中混合供应商账户中的 `messages -> generate_content` 配置时，才进入 Gemini native 真实上游。
6. 模型名只在已识别协议和显式映射范围内参与路由，不跨协议猜测。
7. 当前 API Key 所选路由策略没有绑定目标供应商分组或目标映射账号不可用时，返回本地错误，不全局兜底。

禁止行为：

- 看到 `model = gemini-*` 就把 OpenAI Chat 请求发往 Gemini native。
- 看到 `/v1beta/models/*` 就在没有命中混合供应商账户时把 Gemini native 请求转换成 OpenAI Chat。
- 把 Gemini native 账号的 `supported_endpoint_modes` 伪装成 `chat_json`、`responses_json` 或 `messages_json`。
- 在非 Gemini native 档案账号上配置右侧 `generate_content`。
- 把右侧 `stream_generate_content` 暴露为独立目标协议，导致用户为同一模型重复维护 JSON / SSE 两条映射。
- 在账号模型映射中新增 `generate_content|stream_generate_content -> responses`、`count_tokens -> chat_completions/messages` 或 `embed_content -> chat_completions/messages`。
- 为了让 Codex / OpenAI SDK 使用 Gemini，临时合成 Responses 状态机。

允许行为：

- 用户创建 Gemini OpenAI Chat 账号，协议档案为 `profile_gemini_openai_chat_v1beta`，Base URL 默认为 `https://generativelanguage.googleapis.com/v1beta/openai`。
- OpenAI Chat 客户端直连该档案的 Chat Completions。
- Codex / OpenAI Responses 客户端通过普通 Gemini OpenAI Chat 账户配置 `sourceEndpointFamily = responses`、`upstreamEndpointFamily = chat_completions`，上游模型选择 Gemini 模型。
- OpenAI Chat / Responses 客户端通过混合供应商账户配置 `sourceEndpointFamily = chat_completions` 或 `responses`、`upstreamEndpointFamily = generate_content`，上游模型选择 Gemini native 模型。
- Anthropic Messages 客户端通过混合供应商账户配置 `sourceEndpointFamily = messages`、`upstreamEndpointFamily = generate_content`，上游模型选择 Gemini native 模型。
- Gemini native 客户端通过混合供应商账户配置 `sourceEndpointFamily = generate_content` 或 `stream_generate_content`、`upstreamEndpointFamily = chat_completions`，上游模型选择 GLM、DeepSeek、OpenAI-compatible、GPT API Key 或 Gemini OpenAI Chat 等真实 Chat 模型。
- Gemini native 客户端通过混合供应商账户配置 `sourceEndpointFamily = generate_content` 或 `stream_generate_content`、`upstreamEndpointFamily = messages`，上游模型选择 Claude / GLM Coding Anthropic / DeepSeek Claude Code 等真实 Anthropic Messages 模型。

第三方 NewAPI / OpenAI-compatible 聚合入口如果给的是根地址，例如 `https://vsllm.com`，Gemini OpenAI Chat 档案仍按 OpenAI-compatible 规则拼接 `/v1/chat/completions`；只有官方形态 `.../v1beta/openai` 会去掉本地下游 `/v1` 前缀后拼接 `/chat/completions`。真实账号导入时不要靠模型名猜测协议，必须按该账号实际暴露的 Base URL 填写。

同一 NewAPI 根地址如果按通用 OpenAI-compatible 账号导入，可以保留 `https://vsllm.com`；如果按 GLM 专用 OpenAI v1 档案导入，应填写 `https://vsllm.com/v1` 这类真实 OpenAI v1 根路径。否则 GLM 专用档案会按其协议规则访问 `/chat/completions`，在只暴露 OpenAI-compatible 根地址的中转站上可能命中 HTML 根站。

### 模型映射边界

Gemini 相关模型映射只允许下面这些生成类方向：

| 客户端协议 | Gemini 上游档案 | 上游协议 | 是否支持 | 说明 |
| --- | --- | --- | --- | --- |
| OpenAI Chat Completions | `profile_gemini_openai_chat_v1beta` | Chat Completions | 支持 | 直连，不需要协议转换 |
| OpenAI Responses / Codex | `profile_gemini_openai_chat_v1beta` | Chat Completions | 支持 | 必须在普通 Gemini OpenAI Chat 账户配置 `responses -> chat_completions` |
| OpenAI Chat Completions | `profile_gemini_native_v1beta` | Gemini native GenerateContent | 支持范围内 | 必须在混合供应商账户配置 `chat_completions -> generate_content`，响应还原为 Chat Completion JSON / SSE |
| OpenAI Responses / Codex | `profile_gemini_native_v1beta` | Gemini native GenerateContent | 支持范围内 | 必须在混合供应商账户配置 `responses -> generate_content`，Responses 状态和 hosted tools 不可保真时走 guidance-first |
| Anthropic Messages | `profile_gemini_native_v1beta` | Gemini native GenerateContent | 支持范围内 | 必须在混合供应商账户配置 `messages -> generate_content`，响应还原为 Anthropic Messages JSON / SSE |
| Gemini native | `profile_gemini_native_v1beta` | Gemini native | 支持 | 原生直连，不进入模型映射协议转换 |
| Gemini native GenerateContent / StreamGenerateContent | OpenAI 协议 Chat 上游档案 | Chat Completions | 支持范围内 | 必须在混合供应商账户配置 `generate_content|stream_generate_content -> chat_completions`，响应还原为 Gemini JSON / SSE |
| Gemini native GenerateContent / StreamGenerateContent | Anthropic 协议 Messages 上游档案 | Anthropic Messages | 支持范围内 | 必须在混合供应商账户配置 `generate_content|stream_generate_content -> messages`，响应还原为 Gemini JSON / SSE |
| Anthropic Messages | `profile_gemini_openai_chat_v1beta` | OpenAI Chat compatibility | 不支持 | 不做 `messages -> chat_completions` 到 Gemini OpenAI Chat，避免绕过 Anthropic 到 Chat 桥接矩阵 |
| Gemini native CountTokens / EmbedContent / Files / Cache / Live / Interactions | OpenAI / Anthropic 上游 | 任意 | 不支持 | Interactions 保留为 Gemini 原生端点；不把它转换成 Chat / Messages |

## Gemini native 端点族

| endpoint family | 端点 | 下游响应 | 实现要求 |
| --- | --- | --- | --- |
| `generate_content` | `POST /v1beta/models/{model}:generateContent` | Gemini JSON | JSON / usage / safety / function call 语义帧 |
| `stream_generate_content` | `POST /v1beta/models/{model}:streamGenerateContent?alt=sse` | Gemini SSE | 增量 SSE parser，坏 chunk 受控错误 |
| `interactions` | `POST /v1beta/interactions`、`GET/DELETE /v1beta/interactions/{id}`、`POST /v1beta/interactions/{id}/cancel` | Interaction JSON / SSE / 空成功响应 | `interactions_json` / `interactions_sse`，解析 `steps`、JSON `event_type`、完成 / 失败状态和 `metadata.total_usage` |
| `count_tokens` | `POST /v1beta/models/{model}:countTokens` | Gemini JSON | 透传 request，记录 token count 审计 |
| `models` | `GET /v1beta/models`、`GET /v1beta/models/{model}` | Gemini model list / model | 本地目录渲染 |
| `embed_content` | `POST /v1beta/models/{model}:embedContent` | Embedding JSON | usage / 向量响应不进入文本语义 |
| `files` | `/upload/v1beta/files`、`/v1beta/files/*` | Gemini file object | 上传必须流式，列表分页 |
| `cached_contents` | `/v1beta/cachedContents/*` | Cached content object | cache token 进入 usage 语义 |
| `batch` | batch / operations | operation object | 不能在热路径轮询大任务 |
| `live` | WebSocket | 双向事件 | 后续专项，不混入 HTTP 网关 |

第一阶段 mockai 必须覆盖 `generate_content`、`stream_generate_content`、`count_tokens`、`interactions` 和 `models`。Files / cache / embeddings 可以先完成协议落点和 endpoint mode，但真实验收前不能在 UI 宣称“全部已验证”。

## 请求兼容细节

### 路径模型

Gemini native 的模型来自路径：

```text
/v1beta/models/gemini-3.5-flash:generateContent
```

ProtocolDriver 必须能提取：

- `protocol_version = v1beta`
- `endpoint_family = generate_content`
- `model = gemini-3.5-flash`

模型名可能包含 `/`、`.`、`-` 等字符；路径 helper 必须只解析 `/models/` 和冒号动作之间的部分，不用简单 `split(':')` 误切 Windows 路径或 URL scheme。上游 URL 构造必须编码模型路径片段，避免模型名中的特殊字符破坏路径。

### Query 参数

允许：

- `alt=sse`
- `$fields`
- 官方 paging 参数，例如 `pageSize`、`pageToken`

特殊处理：

- `key` 只作为本地认证来源；认证成功后从上游 query 中移除。
- 上游认证优先使用 `x-goog-api-key`，避免把上游 Key 写进 URL。

### Header 过滤

转发上游时保留：

- `Content-Type`
- `User-Agent`，但允许按现有安全过滤策略裁剪
- Gemini SDK 需要的普通 tracing / request id header，必须先经过 header 安全白名单
- `x-goog-api-client` 可作为 SDK 诊断 header 透传候选

必须移除或替换：

- 本地 `Authorization`
- 本地 `x-goog-api-key`
- query `key`
- Cookie
- hop-by-hop header
- OpenAI / Anthropic / Codex / Claude Code 专属本地画像 header
- 代理链路 header

账户认证例外：API Key 账户在上游写入 `x-goog-api-key`；`google_oauth` 账户由 ProviderDriver 获取 / 刷新 Google OAuth access token 后写入 `Authorization: Bearer <access_token>`，并按非敏感配置写入 `x-goog-user-project`。客户端传入的本地 `Authorization`、`x-goog-api-key` 和 query `key` 都先移除，不能直接透传。

### 请求体透传

Gemini native 直连请求体字段扩展较快，默认保持 raw passthrough。ProtocolDriver 只解析轻量元数据，不重构未知字段。

需要识别的顶层字段：

- `contents`
- `systemInstruction`
- `tools`
- `toolConfig`
- `safetySettings`
- `generationConfig`
- `cachedContent`
- `labels`

如果命中 Gemini native 直连账号模型别名，只改 URL 中的 `{model}`，不改请求体。如果命中 混合供应商账户里的 `generate_content|stream_generate_content -> chat_completions` 或 `generate_content|stream_generate_content -> messages` 桥接规则，才由共享 bridge 将 `contents`、`systemInstruction`、`generationConfig`、`tools.functionDeclarations` 和 `toolConfig` 转为目标上游请求；不支持的 Gemini native 能力返回 Gemini 形态的 agent guidance。

反向的 OpenAI Chat / Responses -> Gemini native 桥接会把 `reasoning_effort` 或 `reasoning.effort` 转为 `generationConfig.thinkingConfig.thinkingLevel`，并把 `service_tier=default|priority|flex` 分别转为 Gemini 官方请求体字段 `service_tier=standard|priority|flex`。只有模型目录明确声明对应能力时，AI 问答才提供这些选项。

## 响应兼容细节

### JSON

`GenerateContentResponse` 语义帧提取：

| Gemini 字段 | 语义 |
| --- | --- |
| `candidates[].content.parts[].text` | 可见文本 |
| `candidates[].content.parts[].functionCall` | 工具调用请求 |
| `candidates[].content.parts[].functionResponse` | 工具调用结果 |
| `candidates[].content.parts[].inlineData` | 内联二进制 / 多模态片段 |
| `candidates[].content.parts[].fileData` | 文件引用 |
| `candidates[].finishReason` | 完成原因 |
| `candidates[].safetyRatings` | 安全评级 |
| `promptFeedback` | prompt 拦截 / 安全反馈 |
| `usageMetadata` | usage |

语义帧用于响应检查、使用记录、审计和账号错误策略输入；下游仍收到 Gemini 原生 JSON，不渲染成 OpenAI。

### Interactions JSON / SSE

Interactions JSON 以官方 `interaction` 对象为根，保留 `id`、`status`、`steps`、`model` 和 `metadata.total_usage`。文本步骤从 `steps[].content` 提取为可见输出，工具 / 模型步骤只作为语义摘要和审计元数据，不改写为 GenerateContent `candidates`。

Interactions 创建流由 POST body `stream: true` 与 `Accept: text/event-stream` 协商，读取既有资源的流由 `GET /v1beta/interactions/{id}?stream=true` 与同一 Accept 协商；两者都不得追加 GenerateContent 专用的 `alt=sse`。每个 SSE `data:` JSON 的 `event_type` 决定 `step.delta`、`interaction.completed` 或 `interaction.failed`，payload 的 `type` 保持原值。流式检查器必须在完成 / 失败事件后结束下游响应，即使上游连接没有立即 EOF；空心跳或未闭合事件不计为可见输出。`metadata.total_usage` 中 input/output/thought/cache token 作为累计快照归一到 Gemini usage semantic，后帧覆盖相同累计维度，不能逐帧求和。

Interaction 资源支持 `POST /v1beta/interactions/{id}/cancel`。`DELETE /v1beta/interactions/{id}` 的 2xx 空 body（包括 204）由通用响应最终化按 endpoint + method + status 精准识别，记录零 usage 成功结果并删除亲和映射。GET / HEAD 等无副作用读取的完整响应继续保持 `generic_gemini` 透明；Interactions 创建、cancel、DELETE 和其他副作用动作一旦派发即保持 at-most-once。其完整 `2xx` 成功响应可正常交付；完整非 `2xx` 或结果未知且下游未提交时返回网关稳定的 `503/upstream_outcome_unknown`，已提交时结束或断流，供应商原始错误只进有界诊断。精确客户端策略可以解释声明的协议结构或执行状态动作，但 retry 动作不能切 Key / 账户重放已派发的 Interaction。

### SSE

Gemini streaming 使用 SSE。每个有效事件的 `data:` 是 Gemini response chunk JSON。实现要求：

- 按增量读取，不拼接完整响应体。
- 支持多行 `data:` 合并。
- 空行表示一个 SSE event 结束。
- 注释 / keep-alive 行只刷新活跃时间，不当作可见输出。
- `generic_gemini` 的坏 JSON chunk 原样转发，不触发切号或账户副作用；只有允许上游语义解释的精确 `gemini_cli` 策略才把坏 chunk 作为协议错误，并且仅在当前请求为可安全重放文本、下游尚未写出可见输出且预算允许时尝试切号。Interactions 和其他副作用请求派发后不重放，写出后按当前流式失败规则处理。
- 不要求 `[DONE]`。
- 不把 Gemini chunk 改成 `chat.completion.chunk`。

### 本地错误

Gemini native 本地错误统一渲染为 Google API error shape：

```json
{
  "error": {
    "code": 400,
    "message": "模型不可用或当前 API Key 的路由策略未绑定 Gemini 分组",
    "status": "INVALID_ARGUMENT",
    "details": []
  }
}
```

错误 status 建议：

| 场景 | status | HTTP |
| --- | --- | --- |
| 缺少本地 API Key | `UNAUTHENTICATED` | 401 |
| 本地 API Key 无效 | `UNAUTHENTICATED` | 401 |
| 模型不可见 / 不可路由 | `INVALID_ARGUMENT` | 400 |
| 当前 API Key 的路由策略未绑定 Gemini 分组 | `FAILED_PRECONDITION` | 400 |
| 额度已用完 | `RESOURCE_EXHAUSTED` | 429 |
| 无可用账号 | `UNAVAILABLE` | 503 |
| 请求体过大 | `RESOURCE_EXHAUSTED` | 413 |
| 上游协议坏形态 | `BAD_GATEWAY` 或 `INTERNAL` | 502 |

本地错误文案仍使用中文；`status` 是机器字段，不翻译。

## OpenAI compatibility 边界

Gemini 官方 OpenAI compatibility 允许 OpenAI Chat Completions 客户端直接调用 Gemini，但这是另一个直连 surface。

本项目策略：

- 不在 Gemini native adapter 中做无映射的 `messages[] -> contents[]` 自动转换。
- 只有显式 `generate_content|stream_generate_content -> chat_completions/messages` 桥接实现 `contents[] -> messages[]`，只有显式 `chat_completions|responses|messages -> generate_content` 桥接实现 OpenAI / Anthropic 请求到 Gemini native；两边都不做无映射自动转换。
- Codex Responses 使用 Gemini 时优先走普通 Gemini OpenAI Chat 账号的 `responses -> chat_completions` 模型别名；只有用户在混合供应商账户显式配置 `responses -> generate_content` 时才进入 Gemini native，且不可保真的 Responses 状态 / hosted tools / compact 走 guidance-first。
- 不把 Anthropic Messages 请求桥接到 Gemini OpenAI Chat。
- 不在显式桥接之外转换工具、usage 或响应形态。

如果用户要让 OpenAI SDK 使用 Gemini，上游使用 Gemini 官方 OpenAI compatibility endpoint：

```text
profile_gemini_openai_chat_v1beta
providerCode = gemini
protocolCode = openai
protocolVersion = v1
baseUrl = https://generativelanguage.googleapis.com/v1beta/openai
endpointFamilies = [chat_completions]
```

该档案只是 Gemini 供应商下的 OpenAI Chat 直连 surface，可通过普通账号模型别名显式承接 OpenAI Responses -> Chat Completions。OpenAI / Anthropic 到 Gemini native 目标必须通过混合供应商账户指向 `profile_gemini_native_v1beta` 分组；Gemini native 到 Chat / Messages 的方向也只能通过混合供应商账户触发。

## gemini-cli 请求矩阵

| gemini-cli 模式 | 是否本次支持 | 说明 |
| --- | --- | --- |
| `GEMINI_API_KEY + GOOGLE_GEMINI_BASE_URL` | 支持 | 主验收路径，自动化脚本使用临时 settings 选择 `gemini-api-key`，同时用 Base URL 指向本项目 |
| `GEMINI_API_KEY` 直连官方 | 不经过本项目 | 用于对照排障 |
| `GOOGLE_GENAI_API_VERSION=v1beta` | 支持 | 本地和上游版本一致 |
| `GEMINI_API_KEY_AUTH_MECHANISM=bearer` | 支持 | 本地认证读取 `Authorization` |
| 默认 `x-goog-api-key` | 支持 | 本地认证读取 `x-goog-api-key` |
| Google 登录 / OAuth | 不支持 | 走 Code Assist 内部接口，不是 Gemini API Key native |
| Vertex AI | 不支持 | 后续单独供应商档案 |
| `--output-format json` | 支持 | 非流式 E2E |
| `--output-format stream-json` | 支持 | 流式 E2E |

`gemini-cli` `0.47.0` 在 headless 模式下如果只靠 `GOOGLE_GEMINI_BASE_URL` 推断 `AuthType.GATEWAY`，非交互认证校验会拒绝 Gateway。真实验收脚本应在临时 HOME 下写入 `.gemini/settings.json`，将 `security.auth.selectedType` 设为 `gemini-api-key`；不要写用户全局 Gemini CLI 配置。

## gemini-cli 客户端画像与响应策略

`gemini-cli` 是下游客户端画像，不是供应商、协议档案或账号能力。服务端识别只用于本地响应策略和审计，不改变上游认证方式，不把本地画像 header 透传上游。

识别规则：

- 显式本地 header：Gemini native 请求携带 `x-juhe-client-profile: gemini_cli` 时识别为 `gemini_cli`。
- 自动签名：请求必须是 Gemini native `generateContent` 或 `streamGenerateContent`，并且 `User-Agent` 包含 `GeminiCLI` 或 `proxy_client=geminicli`，同时存在 Gemini 本地认证信号（`Authorization`、`x-goog-api-key`、`x-api-key` 或 query `key`）。
- 未命中以上条件时，Gemini native 请求统一识别为 `generic_gemini`。
- `x-goog-api-client` 来自 `@google/genai` SDK，不是 `gemini-cli` 专属信号，不能单独用于升级客户端画像。

响应策略：

- `generic_gemini` 的完整 JSON / SSE 响应不解释失败语义：`200 + error` 和普通流内 error 保持 payload 透明；可安全重放文本 `generateContent` 的完整非 `2xx` 只按 `response.ok=false` 做内容无关的请求级 Key/账号接管，不写共享状态。副作用型 Interactions/资源创建派发后保持唯一 attempt，按上文稳定中性终态处理。协议解析器仍可有界提取 usage 和 Interactions 资源 ID，但不得借此升级失败语义。
- `gemini_cli` 专属默认规则只在 `clientProfiles = ['gemini_cli']` 时匹配 Google canonical `error.status`：`RESOURCE_EXHAUSTED`、`UNAVAILABLE`、`DEADLINE_EXCEEDED`、`INTERNAL`、`CANCELLED`。
- 该专属规则动作为 `retry_next_account`，只在可安全重放文本 GenerateContent 的写出前窗口表达重放意图；Interactions、资源和其他副作用请求不进入该白名单。普通 `generic_gemini`、OpenAI、Anthropic 客户端不会继承。
- 不伪造 `TOS_VIOLATION`、`VALIDATION_REQUIRED` 等 Google 特定 reason；这些会触发 `gemini-cli` 账号封禁或验证流程语义。
- 对安全拦截、空输出和坏 SSE，优先返回可解析的 Gemini error shape 或带文本的正常 Gemini 响应，不返回空成功 chunk。

真实 E2E 前置条件：

- 本项目启动本地网关。
- 已创建 Gemini API Key 账户、默认 Gemini 分组和本地 API Key。
- 本地 API Key 绑定的路由策略包含 Gemini 分组。
- 本机已安装 `gemini` CLI。
- 用户提供真实 Gemini API Key。

## mockai 覆盖矩阵

正式实现时先用 mockai 覆盖：

| 场景 | 输入 | 预期 |
| --- | --- | --- |
| 非流式文本 | `generateContent` | 返回 Gemini JSON，usage 写入 |
| 流式文本 | `streamGenerateContent?alt=sse` | 客户端收到 Gemini SSE chunks |
| function call | `tools.functionDeclarations` | 语义帧识别 functionCall |
| function response | `contents.parts.functionResponse` | 透传上游 |
| safety block | mock `promptFeedback` | 本地错误 / 语义检查按 Gemini shape |
| no candidates | `candidates=[]` | 上游协议异常，未写出时可切号 |
| malformed SSE chunk | 坏 `data:` | 受控协议错误 |
| countTokens | `countTokens` | 返回 token count，不写生成文本 |
| models list | `GET /v1beta/models` | 本地目录 shape |
| auth header | `x-goog-api-key` / bearer / query key | 均可本地认证，均不泄漏上游 |
| gemini-cli 客户端画像 | `GeminiCLI` User-Agent + Gemini 认证信号 | 识别为 `gemini_cli`，审计写入客户端画像 |
| 通用 Gemini 隔离 | 普通 SDK / curl 请求 | 识别为 `generic_gemini`，不继承 CLI 专属切号 |
| CLI 专属可重试错误 | `gemini_cli` + 可安全重放文本 GenerateContent + `error.status=UNAVAILABLE` | 写出前请求下一个账号，成功后返回 Gemini native 响应；Interactions 不重放 |
| OpenAI 路径隔离 | `/v1/chat/completions` 且无 `chat_completions -> generate_content` 映射 | 不进入 Gemini native |
| Gemini OpenAI Chat | `/v1/chat/completions` | 命中 `/v1beta/openai/chat/completions`，使用账号 Bearer Key |
| Codex / Responses 到 Gemini OpenAI Chat | `/v1/responses` + 普通账号模型别名 `responses -> chat_completions` | 命中 Gemini OpenAI Chat，不命中 Gemini native |
| OpenAI Chat 到 Gemini native | `/v1/chat/completions` + API Key 规则 `chat_completions -> generate_content` | 命中 Gemini native `generateContent` / `streamGenerateContent`，返回 Chat 形态 |
| OpenAI Responses 到 Gemini native | `/v1/responses` + API Key 规则 `responses -> generate_content` | 命中 Gemini native，返回 Responses 形态；不可保真能力 guidance-first |
| Anthropic Messages 到 Gemini native | `/v1/messages` + API Key 规则 `messages -> generate_content` | 命中 Gemini native，返回 Anthropic Messages 形态 |
| Gemini native 到 Chat | `/v1beta/models/{model}:generateContent` + API Key 规则 `generate_content -> chat_completions` | 命中目标 Chat 上游，返回 Gemini JSON |
| Gemini native 流式到 Chat | `/v1beta/models/{model}:streamGenerateContent?alt=sse` + API Key 规则 `stream_generate_content -> chat_completions` | 命中目标 Chat SSE 上游，返回 Gemini SSE，并移除上游 URL 中的 `alt` / `key` |
| Gemini native 到 Anthropic Messages | `/v1beta/models/{model}:generateContent` + API Key 规则 `generate_content -> messages` | 命中目标 Anthropic Messages 上游，返回 Gemini JSON |
| Gemini native 流式到 Anthropic Messages | `/v1beta/models/{model}:streamGenerateContent?alt=sse` + API Key 规则 `stream_generate_content -> messages` | 命中目标 Anthropic Messages SSE 上游，返回 Gemini SSE，并移除上游 URL 中的 `alt` / `key` |
| Anthropic 映射禁用 | `messages -> chat_completions` | Gemini OpenAI Chat 档案保存失败 |

真实账号补测再覆盖：

- `gemini-cli --output-format json`
- `gemini-cli --output-format stream-json`
- 官方 curl native JSON / SSE
- countTokens
- 文件输入或图片输入
- function calling
- 错误 Key / 额度 / 限流

## 非目标

- 不实现无映射的自动跨协议转换；OpenAI / Anthropic 到 Gemini native、Gemini native 到 OpenAI / Anthropic 都只能通过 混合供应商账户触发。
- 不实现 Gemini OAuth、Code Assist、Vertex AI 或 Workspace 账号。
- 不实现 Live API WebSocket 代理。
- 不让 Gemini native 直连账号进入 OpenAI Chat / Responses 调度。
- 不让 OpenAI Chat 账号在没有显式 `generate_content|stream_generate_content -> chat_completions` 映射时进入 Gemini native 调度。
- 不暴露右侧 `stream_generate_content` 作为单独模型映射目标。
- 不把 `gemini-*` 模型名作为跨协议自动路由依据。

## 自审清单

- Gemini native 是否有独立 `ProtocolDriver`。
- 请求模型是否从路径提取，而不是从 JSON body `model` 提取。
- 本地 `x-goog-api-key` / query `key` 是否被替换，不能泄漏到上游。
- SSE 是否保持 Gemini 原生 chunk，而不是 OpenAI chunk。
- 本地错误是否按 Gemini / Google API error shape 返回。
- usage 是否使用 `usage_semantic = gemini`。
- OpenAI / Anthropic 到 Gemini native 是否只通过 混合供应商账户中的 `chat_completions|responses|messages -> generate_content` 规则触发。
- Gemini native 到 Chat / Anthropic Messages 是否只通过 混合供应商账户触发，并保持下游 Gemini JSON / SSE 形态。
- `gemini-cli` 真实验证是否使用 `GOOGLE_GEMINI_BASE_URL`，而不是 Google 登录。
