# Responses 转 Chat Completions 账户适配方案

## 文档边界

本文记录 `POST /v1/responses` 在选中具体 API Key 账户后，按账户配置降级转发到上游 `POST /v1/chat/completions` 的落地方案、已落地边界和待核对细节。

实现状态：第一版已于 2026-06-09 落地，但 2026-06-09 已决定放弃继续推进 Responses 转 Chat Completions 的完整兼容方向。本文保留为调研和已实现边界记录；本地 `previous_response_id` 上下文账本、摘要 compact、长会话兼容和复杂 Responses 内置工具不再作为后续实现目标。

方向调整：

- 推荐路线回到原生 Responses passthrough：需要 Responses / Codex 长会话的上游应优先选择真实支持 `/responses` 的账号。
- `chat_completions_bridge` 如果保留，只能作为短任务、基础文本和简单 tool call 的受控降级能力；不继续扩展本地会话状态、摘要压缩和完整 Responses 仿真。
- 后续如果决定彻底移除 bridge，应单独拆除账户字段、前端配置、导入导出字段、网关转换器和相关测试，而不是继续补复杂兼容层。

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
| `previous_response_id` | 不支持 | 当前 bridge 不维护本地上下文账本；收到非空 `previous_response_id` 时返回本地 `400 responses_chat_bridge_previous_response_id_unsupported`，不请求上游、不写账号失败。 |
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

## 已放弃的本地 `previous_response_id` 上下文续链方案

> 2026-06-09 方向调整：本节方案已放弃，不进入实现。保留以下内容仅作为复杂度评估留档；实际策略是不在 bridge 中维护本地 `previous_response_id` 上下文账本，不做服务端摘要 compact，也不生成 `resp_bridge_...` 续链 ID。当前 Chat bridge 收到非空 `previous_response_id` 时直接返回本地 `400 responses_chat_bridge_previous_response_id_unsupported`。

### 目标与边界

该方案只服务 `openai_responses_upstream_mode = chat_completions_bridge`。原生 `passthrough` Responses 账户仍把 `previous_response_id` 交给上游处理，不读取本地 bridge 上下文。

已放弃计划的核心目标：

- 客户端继续按 Responses API 使用 `previous_response_id`。
- 网关在本地用 `previous_response_id` 找到对应会话链，把历史 turn 还原为 Chat Completions `messages`，再追加当前请求输入。
- 会话按活跃时间短期保留，默认 24 小时无活跃即不可继续。
- 过期或找不到时返回明确本地错误，不静默丢弃 `previous_response_id`。

非目标：

- 不实现完整 Responses 服务端状态、`conversation` 对象、后台任务、文件、MCP、computer use 或永久记忆。
- 不把本地 `response_id` 拿到 OpenAI 原生 `/responses/{id}` 查询。bridge 生成的 ID 是本地 ID，上游 Chat Completions 不认识。
- 不在请求链路扫描审计、usage、日志或历史明细来拼上下文。
- 不做专用摘要模型配置，不开放自定义压缩 prompt，不把本地摘要伪装成上游原生 opaque compact。该计划曾评估 bridge 摘要 compact 作为内置降级能力，只使用当前会话同一账号和同一模型；当前实现不包含该能力。

### 已放弃计划中的 ID 与调度规则

如果启用这项已放弃的本地续链计划，bridge 生成的 Responses ID 曾计划使用本地可识别前缀，例如 `resp_bridge_...`，不要继续把上游 Chat completion id 直接包装成 `resp_<chat_id>` 作为主 ID。当前实现不生成这类续链 ID。

已放弃计划中的调度规则：

- 新请求没有 `previous_response_id`：按当前候选逻辑选择可承接的 bridge 账户，成功后创建本地 bridge 会话。
- 请求携带本地已知的 `previous_response_id`：先按 `response_id` 主键解析本地会话，再只允许可承接 `chat_completions_bridge` 的账户继续；原生 `passthrough` 账户不能承接本地 bridge ID。
- 请求携带未知 `previous_response_id`：bridge 账户返回 `responses_bridge_previous_response_not_found`；passthrough 账户仍可把它当作原生 Responses ID 交给上游。
- 解析到本地会话后，会话亲和应使用稳定的本地 `bridge_session_id`，不能继续用每轮变化的 `previous_response_id` 当亲和 key，否则第三轮以后容易失去同一会话排序优势。
- 会话亲和只影响排序，不绕过 API Key、分组、授权、模型限制、账号状态、冷却、并发和 endpoint 能力过滤。

### 已放弃计划中的存储归属

本地上下文属于短期、可过期、可丢弃的协议兼容状态，不属于长期业务事实，也不属于使用统计。存储形态参考原始审计 payload：SQLite 只保存会话索引、顺序关系、文件引用和保留状态；完整上下文 payload 落本地文件，到期后删除文件。

落点：

- SQLite 元数据放统计数据集目录库，由 DB service 提供主键读取和短事务写入。
- 完整 payload 文件放本地数据目录，默认建议 `backend/data/responses-bridge/payloads/`，也可以后续用本地配置指向其他目录。
- 不放进业务库，避免短期 prompt / tool output payload 膨胀核心业务备份。
- 不放进 usage shard，避免和使用记录分片定位规则混在一起。
- 不能只放进进程内存；热路径可以用进程内会话缓存吸收连续请求，但重启恢复、过期清理和冷会话加载仍依赖 SQLite 索引与本地 payload 文件。
- 不复用 `audit_payload_*` 表；审计是排障原文保全，bridge context 是可回放会话状态，两者保留期、权限和写入时机不同。
- 不引入 Redis、Kafka、对象存储或外部分布式依赖。

建议新增三张元数据表：

```sql
CREATE TABLE responses_chat_bridge_sessions (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT NOT NULL,
  root_response_id TEXT NOT NULL,
  latest_response_id TEXT NOT NULL,
  status TEXT NOT NULL,
  turn_count INTEGER NOT NULL DEFAULT 0,
  compaction_count INTEGER NOT NULL DEFAULT 0,
  compacted_until_sequence INTEGER,
  payload_bytes INTEGER NOT NULL DEFAULT 0,
  replay_bytes INTEGER NOT NULL DEFAULT 0,
  chat_body_estimated_bytes INTEGER NOT NULL DEFAULT 0,
  last_account_id TEXT,
  last_group_id TEXT,
  client_session_key TEXT,
  created_at TEXT NOT NULL,
  last_active_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  expired_at TEXT
);

CREATE TABLE responses_chat_bridge_response_index (
  response_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  previous_response_id TEXT,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE responses_chat_bridge_turn_payloads (
  id TEXT PRIMARY KEY,
  response_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  payload_kind TEXT NOT NULL,
  storage_key TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  raw_size_bytes INTEGER NOT NULL,
  stored_size_bytes INTEGER NOT NULL,
  replay_size_bytes INTEGER NOT NULL,
  chat_body_estimated_bytes INTEGER NOT NULL,
  compacted_from_sequence INTEGER,
  compacted_to_sequence INTEGER,
  source_replay_size_bytes INTEGER,
  summary_model TEXT,
  summary_account_id TEXT,
  compression TEXT NOT NULL,
  content_type TEXT NOT NULL,
  payload_status TEXT NOT NULL,
  model TEXT,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
```

必要索引：

- `responses_chat_bridge_sessions(status, expires_at, id)`：后台按过期时间批量清理。
- `responses_chat_bridge_sessions(system_account_id, api_key_id, status, expires_at)`：按调用作用域定位活跃会话。
- `responses_chat_bridge_response_index(session_id, sequence)`：按会话顺序读取 response 索引。
- `responses_chat_bridge_response_index(status, expires_at, response_id)`：清理过期索引和墓碑。
- `responses_chat_bridge_turn_payloads(response_id)`：按 response 找到文件引用。
- `responses_chat_bridge_turn_payloads(session_id, sequence)`：按会话顺序窗口读取文件引用。
- `responses_chat_bridge_turn_payloads(expires_at, id)`：后台批量删除过期 payload 文件和引用。

安全作用域：

- `response_id` 查询命中后，必须校验 `system_account_id + api_key_id` 与当前调用者一致。
- `response_id` 不是认证凭据，不能凭 ID 跨 API Key、跨系统账户读取上下文。
- 上下文文件包含用户输入、assistant 输出、function call 和 tool output，默认不在管理端展示，不写操作日志；排障只记录 ID、文件大小、状态和错误码。

### 已放弃计划中的热会话缓存与异步落表

Responses bridge 会话是连续请求形态，不能每一轮都先查 SQLite、再写 SQLite。SQLite 单 writer 会阻塞，连续 Codex / SDK 请求并发时如果每轮同步落表，会把 DB service 写锁拖成瓶颈。

运行时增加进程内热会话缓存：

- `response_id -> session_id` 热索引，用于命中 `previous_response_id` 时优先定位 session。
- `session_id -> hot session`，保存作用域、状态、最新 response、sequence、累计大小、payload 引用、当前 replay messages 和待 flush 变更。
- 缓存 key 必须包含 `system_account_id + api_key_id + session_id`，跨系统账户或 API Key 不能命中。
- 热缓存只服务当前 server 进程，不承诺多进程共享；服务重启后通过 SQLite 索引和 payload 文件冷加载。

已放弃计划中的读取策略：

1. 请求携带 `previous_response_id` 时，先查热缓存里的 `response_id -> session_id`。
2. 热缓存命中且作用域、状态、TTL、大小元数据都有效时，不访问 SQLite，直接用缓存中的 replay messages 组装本轮 Chat 请求。
3. 热缓存未命中时，才通过 DB service 按 `response_id` 主键查 SQLite 索引，再按 `session_id + sequence` 有界窗口加载 payload 引用和必要文件，加载成功后回填热缓存。
4. SQLite 未命中、状态不可继续、过期或作用域不匹配时，按对应本地错误返回，不请求上游。

已放弃计划中的写入策略：

- 成功 turn 先写完整 payload 文件并更新热缓存；返回给客户端的 `response_id` 必须已经存在于热缓存，后续连续请求不依赖 SQLite 立即可续链。
- SQLite 落表改为脏队列批量 flush：同一 session 的多轮变更合并，按空闲、短间隔或批量阈值写入 `responses_chat_bridge_sessions`、`responses_chat_bridge_response_index` 和 `responses_chat_bridge_turn_payloads`。
- flush 间隔应短而有界，例如 `1s..5s` 或达到 `N` 个 dirty sessions / `M` 个 dirty turns 时触发；不能等到 24 小时 TTL 或进程退出才落表。
- 单次 flush 固定小批次，失败后保留 dirty 标记并退避重试；不能在请求线程里因为 SQLite busy 等待长事务。
- 同一 session 的 flush 必须按 sequence 顺序提交；可以跨 session 合批，但不能让后提交的 turn 先落表造成 response 链断裂。

已放弃计划中的可靠性边界：

- 如果只依赖热缓存，进程崩溃可能丢失尚未 flush 到 SQLite 的 response 索引；客户端随后带这个 `previous_response_id` 会因为冷启动查不到而失败。为降低这个窗口，建议实现一个轻量本地 session journal：payload 文件写入成功后，同步追加一条小 JSONL journal 记录 `session_id / response_id / sequence / storage_key / size / status`，SQLite flush 成功后推进 journal 水位；进程启动时先按 journal 重放未落表索引。
- journal 只保存索引和文件引用，不保存正文；正文仍在 payload 文件里。
- 如果不实现 journal，必须接受“进程崩溃会丢失最近未落表会话索引”的语义，并在实现说明和错误日志中标记为 `responses_bridge_context_not_flushed` 风险。

已放弃计划中的缓存容量与淘汰：

- 热缓存必须有会话数、response 索引数、replay bytes 和 dirty bytes 上限，防止恶意并发把内存打满。
- clean 且过期的 session 可以直接淘汰；dirty session 淘汰前必须先尝试 flush。
- dirty session 如果因为 DB 长时间不可用、队列满或内存压力无法 flush，不能静默淘汰；应保留 dirty 标记、暂停新 turn 写入或把后续续链转为明确本地错误。payload 文件清理由后台 job 统一处理，请求链路不直接删除文件。
- 单个 API Key 或系统账户应有热会话上限，避免一个疯狂用户把全局 cache 占满。
- 缓存命中只减少 DB 读写，不绕过权限、TTL、大小、会话状态和 endpoint 能力判断。

### 已放弃计划中的完整 payload 文件内容

已放弃计划要求每个成功普通 turn 写一个完整上下文 payload 文件，不截断成“伪完整”。摘要 compact 成功时额外写一个 `payload_kind = summary_snapshot` 的完整摘要 payload，用它替换较早历史前缀参与后续 replay；被摘要覆盖的旧 turn 文件不在请求链路删除，等待后台 cleanup job 按引用状态和 TTL 清理。文件建议保存为 JSON，可按审计 payload 的经验对 JSON / 文本使用 gzip 压缩；超过单次读取窗口或需要高频按 offset 读的内容才保留 plain。本地续链重放时，系统按文件元数据先校验大小，再读取文件。

文件内容建议包含：

- 下游 `/responses` 请求体完整原文。
- 转换后的上游 Chat 请求体完整 JSON。
- 当前请求转换后的 Chat `user` / `tool` messages。
- 上游 Chat 返回的完整 assistant / tool call payload；非流式保存 message 原文，流式保存聚合后的完整文本、tool call 参数和必要的 chunk 终态摘要。
- 当前有效的 instructions / developer / system 快照。
- 转换后的 Responses output 摘要，用于后续 `/responses/{id}` 或排障扩展。
- 模型、上游账号、分组、创建时间、sequence、usage 和 payload 字节数等诊断字段。
- 如果是 summary snapshot，还需要包含摘要来源 sequence 范围、摘要提示版本、摘要模型、摘要账号、源 replay 字节数、摘要 replay 字节数和保留的最近原始 turn 范围。

不保存：

- OAuth token、API Key 明文、代理密码或本地 Bearer Token。
- 客户端认证 header、上游 Authorization header、代理认证信息。

已放弃计划中的边界：

- 如果单个 turn 的完整 payload 超过本地 bridge context 硬上限，当前 turn 应明确失败，不能写半截文件后返回成功。
- 如果累计会话重放大小或最终 Chat body 预估大小接近系统文本 lane 高水位，已放弃计划曾允许 bridge 先做一次内置摘要 compact；摘要后仍超过系统文本 lane 上限时，本次续链返回 `responses_bridge_context_too_large`。请求链路不删除 payload 文件，由后台 job 后续统一标记和清理。
- 文件写入必须使用临时文件 + fsync / rename 这类原子提交思路；SQLite 引用只在文件提交成功后写入。清理时先把引用标记为过期 / 删除中，再异步删除文件，失败后可按 `storage_key` 重试。

instructions 处理建议：

- 会话保存当前有效 instructions 快照。
- 当前请求显式传入 `instructions` 时更新快照。
- 组装 Chat 请求时只在消息最前面放一份当前有效 instructions，不把每一轮历史 instructions 重复插入，避免系统消息膨胀和语义重复。

### 已放弃计划中的本地摘要 compact 与客户端压缩边界

这里的会话压缩指模型上下文摘要、裁剪或 compact，不是 HTTP `Content-Encoding` 传输压缩。

已放弃计划曾评估 bridge 第一版内置本地摘要 compact：

- 不新增用户配置，不做专用摘要模型。摘要调用使用当前 bridge 会话同一账号、同一上游模型、同一代理、同一并发占用和同一错误处理边界。
- `/responses/compact`、`compaction_trigger` 或明确要求 Responses compaction 的 `context_management` 在 bridge 模式下触发本地摘要 compact，不再返回“不支持”。
- 自动触发只发生在续链预估接近系统文本 lane 高水位时。建议内置默认高水位为 `80%`，摘要目标为 `50%`，最近原始 turn 保留不少于 `6` 轮；这些先作为后端常量，不开放前端配置。
- 摘要只压缩较早历史前缀，保留最近原始 messages、当前请求 input、未闭合 tool call、最近 tool output 和当前有效 instructions。
- 摘要输出作为普通历史摘要 message 参与 replay，不能写入 system / developer instructions。
- 摘要成功后写入 `summary_snapshot` payload，更新 hot session 的 `compacted_until_sequence`、`replay_bytes`、`chat_body_estimated_bytes` 和最新 response id；后续续链默认从 summary snapshot + 最近原始 turns 继续。
- 单次用户请求最多触发一次摘要。摘要调用失败、摘要内容为空、摘要后仍超限或摘要 payload 持久化失败时，不输出可续链成功状态。

已放弃计划中，客户端自行摘要曾按“显式替换”和“普通追加”分开处理：

- 如果客户端压缩后的摘要要替代旧历史，应不携带 `previous_response_id`，让 bridge 按新会话处理。旧 bridge session 不在请求链路主动删除，只等待 TTL 和后台 cleanup job 清理。
- 如果客户端压缩后仍携带旧 `previous_response_id`，当前实现不做普通续链，直接返回本地 `400 responses_chat_bridge_previous_response_id_unsupported`。
- 不从自然语言、metadata 备注或消息文本里猜测“这是摘要替换请求”；请求路径不读取旧 payload 文件来做语义归并，也不改写旧 payload。

### 请求处理流程

已放弃计划中的新会话：

1. 客户端请求 `/responses`，没有 `previous_response_id`。
2. 命中 bridge 账户后，把当前 Responses input 转为 Chat messages。
3. 请求上游 `/chat/completions`。
4. 上游成功后，生成 `resp_bridge_...` 和 `bridge_session_id`。
5. 写入当前 turn 的完整 payload 文件，保存 session、response index 和 payload 文件引用。
6. 返回转换后的 Responses JSON 或 SSE。

已放弃计划中的续链会话：

1. 客户端请求 `/responses`，携带 `previous_response_id = resp_bridge_...`。
2. 先查热缓存里的 `response_id -> session_id`，命中后直接取 hot session；未命中才通过 DB service 按 `response_id` 主键查询 SQLite 索引。
3. 校验调用作用域、会话状态、`expires_at` 和缓存/DB 元数据。
4. 热缓存命中时先用缓存中的累计字段和当前请求大小判断是否达到摘要高水位或超过系统文本 lane 上限；冷加载时读取该 session 中 `sequence <= previous.sequence` 的索引和 payload 文件引用窗口，先用 SQLite 元数据里的 `replay_size_bytes`、`chat_body_estimated_bytes`、session 累计字段和当前请求大小判断，不为了判断大小读取全部历史 payload。
5. 如果命中摘要高水位且存在可压缩前缀，按 `storage_key` 有界读取待压缩前缀，调用当前账号 / 当前模型生成 summary snapshot；摘要成功后更新热缓存和 dirty session，再重新估算本轮 Chat body。
6. 摘要后仍超过系统文本 lane 上限，或没有可压缩前缀时，返回本地 `400 responses_bridge_context_too_large`；不请求业务上游、不删除 payload 文件，由后台 job 后续统一标记和清理。
7. 通过大小判断后按顺序读取当前有效 replay payload，拼接 summary snapshot、最近原始历史和当前请求 input 转换出的 Chat messages，并回填热缓存。
8. 请求上游 Chat，成功后写入新 payload 文件，更新热缓存里的 response index / payload 引用 / 累计字段 / `latest_response_id / last_active_at / expires_at`，并把 session 标记为 dirty，等待异步批量 flush 到 SQLite。

已放弃计划中的不可继续会话：

1. 请求命中的 session `status = context_too_large` 或其他不可继续状态。
2. 返回本地 `400 responses_bridge_context_invalidated`，错误信息说明“本地 Responses bridge 上下文已失效，需要不带 previous_response_id 开启新会话”。
3. 不读取 payload 文件，不请求上游，不写账号失败，不触发账户错误处理策略。

已放弃计划中的过期会话：

1. 请求命中的 session `expires_at < now` 或 `status = expired`。
2. 返回本地 `400 responses_bridge_previous_response_expired`。
3. 不请求上游，不写账号失败，不触发账户错误处理策略。

已放弃计划中的未知 ID：

- 已放弃计划中，本地查不到 `response_id` 时，bridge 模式返回 `400 responses_bridge_previous_response_not_found`。当前实现会在非空 `previous_response_id` 阶段直接返回 `responses_chat_bridge_previous_response_id_unsupported`，不会进入本地 response index 查询。
- 如果本轮最终选择的是原生 passthrough 账户，则不做本地错误，由上游原生 Responses 决定该 ID 是否存在。

### 已放弃计划中的 TTL 与清理

已放弃计划中的默认保留策略：

- 活跃 TTL：`24` 小时。每次成功续链后刷新 `last_active_at` 和 `expires_at`。
- Payload 清理：会话过期后删除 `responses_chat_bridge_turn_payloads` 对应的本地文件和引用元数据。
- 超限清理：续链大小超过系统文本 lane 上限时，请求链路只返回 `responses_bridge_context_too_large`。后台 cleanup job 按 session / turn 元数据识别超限会话，批量删除 payload 文件和引用元数据，并把 session / response index 标记为 `context_too_large`；这条会话在 job 处理后不可再继续。
- 墓碑保留：保留 session 和 response index 的轻量墓碑，状态为 `expired` 或后台 job 标记的 `context_too_large`，用于把旧 `previous_response_id` 返回成明确错误。
- 墓碑保留期：建议再保留 `24` 小时；超过后可以物理删除。删除后同一 ID 再请求会变成 `not_found`，仍然是明确错误。

已放弃计划中的清理任务：

- 由 `data-retention-cleanup` 或独立 bridge context cleanup worker 在后台执行。
- 每轮固定小批次，例如每类表最多 `1000` 条、最多 `2` 批，沿用现有清理上限心智。
- 只按 `status + expires_at` 索引推进，不 `COUNT(*)`，不展开全表排序。
- 删除文件不能靠扫描 payload 目录兜底；必须以 SQLite 里的 `storage_key` 为待删清单，异步文件接口按固定并发窗口推进。
- 清理失败只记录运行日志和任务状态，不阻塞正常请求；请求路径遇到过期行时仍按 `expires_at` 即时拒绝。

### 已放弃计划中的大小上限与性能保护

已放弃计划曾要求上下文重放上限不单独定义新系统配置，必须跟随当前网关文本请求上限：

- `responsesBridgeContextTtlHours = 24`
- `responsesBridgeContextTombstoneHours = 24`
- `responsesBridgeContextMaxTurns = 200`
- `responsesBridgeSummaryCompactHighWaterRatio = 0.8`
- `responsesBridgeSummaryCompactTargetRatio = 0.5`
- `responsesBridgeSummaryCompactKeepRecentTurns = 6`
- 单 turn 完整 payload、会话累计重放内容和最终组装出的 Chat 请求体都不能超过系统设置 `gatewayTextRawBodyLimitMegabytes` 对应的文本 lane 上限。
- 最终 Chat body 还要为当前输入、协议 envelope、JSON 转义和模型字段预留余量；建议按当前文本 lane 上限的 90% 作为 bridge 上下文可用阈值，避免估算误差导致实际上游请求体超限。

TTL、turn 数和摘要触发比例曾计划先作为后端内置默认值，不开放前端配置。大小类配置不另起一套，避免管理面显示一个文本上限、bridge 实际使用另一个上限。

已放弃计划中的性能要求：

- 只有 bridge 模式且请求携带本地 `previous_response_id` 时才读上下文表。
- 热缓存命中时不访问 SQLite；热缓存 miss 才按 `response_id` 主键和 `(session_id, sequence)` 有界窗口读取 SQLite 索引和 payload 引用。
- 新会话只同步写一个本地 payload 文件并更新热缓存；SQLite session / response index / payload 引用由 dirty 队列批量落表。
- 不能在请求链路扫描同一 API Key 的全部会话、全部 response、payload 目录或审计 payload。
- 读取历史文件前必须先用元数据判断：热缓存或 SQLite 中的 session 级 `replay_bytes / chat_body_estimated_bytes`、turn 级 `replay_size_bytes / chat_body_estimated_bytes`、当前请求估算大小和系统文本 lane 上限。达到摘要高水位时只读取可压缩前缀窗口；摘要后仍超限才返回错误。不会读完一批大文件后才发现超限，也不会在请求链路删除文件。
- 摘要调用必须复用当前 bridge 账号和模型，不走专用摘要模型、不切到其他供应商账号、不绕过并发和额度。
- 摘要调用产生真实用量，使用记录和审计 metadata 标记 `traffic_source = responses_bridge_summary_compact`，并关联到触发它的用户请求。
- 摘要 prompt 必须是后端固定模板，输出长度有上限；不能允许用户自定义摘要 prompt。
- 每次写入新 turn 时必须同步更新热缓存里的 session 级累计字段；如果写文件成功但热缓存更新失败，要清理本次文件并返回持久化失败，不能留下不可控的大小账。SQLite 累计字段由后续 flush 对齐。
- 文件内容保存完整原始 payload 和转换后的 replay messages，续链时优先使用 replay messages，不为了历史 turn 重新做复杂 Responses 转换。
- 流式转换继续按 chunk 增量输出，只累计当前 turn 需要持久化的 assistant message 和 tool call 参数，不能缓存完整上游流后再返回。
- 发生持久化失败时不能伪造 `response.completed`。非流式应在返回前失败；流式若已经输出可见内容，只能以 `response.failed` 结束并记录 `responses_bridge_context_persist_failed`，否则客户端会拿到一个无法续链的成功 response。

已放弃计划的成本影响：

- 如果实现本地 Chat bridge 续链，历史 messages 会每次重新发给上游，token 成本随会话增长，这是 Chat Completions 形态的固有限制。
- 如果实现本地摘要 compact，可以降低后续续链 token，但触发当轮会增加一次摘要调用成本和延迟。该成本曾计划按同一 API Key / 同一账号使用记录透明计入。

### 已放弃计划中的错误码留档

当前实现只使用 `responses_chat_bridge_previous_response_id_unsupported` 表示 Chat bridge 不支持非空 `previous_response_id`。下表其他本地上下文、payload 和摘要 compact 错误码属于已放弃计划留档。

| 错误码 | 状态码 | 场景 |
| --- | --- | --- |
| `responses_chat_bridge_previous_response_id_unsupported` | `400` | Chat bridge 当前不支持本地上下文续链时收到非空 `previous_response_id`。 |
| `responses_bridge_previous_response_not_found` | `400` | bridge 模式下找不到本地 `previous_response_id`。 |
| `responses_bridge_previous_response_expired` | `400` | 会话已过 24 小时活跃 TTL 或已被清理为过期。 |
| `responses_bridge_previous_response_scope_mismatch` | `403` | ID 存在但不属于当前 `system_account_id + api_key_id`。 |
| `responses_bridge_context_too_large` | `400` | 历史 turn、payload 字节数或最终 Chat body 超过上限；请求链路只返回错误，后台 job 后续统一清理。 |
| `responses_bridge_context_invalidated` | `400` | 会话此前已被后台 job 标记为过期、超限或其他不可继续状态。 |
| `responses_bridge_context_persist_failed` | `500` | 上游已成功但本地上下文保存失败，不能安全返回可续链成功状态。 |
| `responses_bridge_summary_compact_unavailable` | `400` | bridge 模式收到 compact 信号，但找不到可压缩本地 session、没有可压缩前缀或作用域不匹配。 |
| `responses_bridge_summary_compact_failed` | `500` / `503` | 本地摘要 compact 调用、解析或持久化失败，不能安全生成新的可续链 response。 |

## `/responses/compact` 策略

> 2026-06-09 方向调整：bridge 本地摘要 compact 已放弃。需要 compact 的客户端应走原生 Responses passthrough / OAuth Codex adapter 能力；Chat bridge 不继续补本地摘要兼容。

Codex 长会话可能调用 `/responses/compact`。Chat Completions 没有等价端点。

当前策略：

- `chat_completions_bridge` 不承接 `/responses/compact`，候选过滤会跳过 bridge 账户或返回本地不支持错误。
- `compaction_trigger` 不转换为 Chat summarization。
- 不生成本地 `resp_bridge_...` compact response id，不维护 summary snapshot，不做本地 session replay。
- 客户端需要 compact 或长会话续链时，应选择原生 Responses passthrough / OAuth Codex adapter；如果自行摘要，应不携带旧 `previous_response_id` 开启新请求。

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
  - `chat_completions_bridge` 不可承接；本地过滤会跳过该账户或返回“不支持 Responses compact”。

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
- 响应体捕获继续使用现有有界捕获和截断策略；bridge context 文件不替代审计日志，也不能要求审计链路额外保存一份完整 payload。
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

- bridge 模式账户只承接普通 `/responses`；`/responses/compact`、`compaction_trigger` 和非空 `previous_response_id` 都在本地拒绝或候选过滤阶段处理。
- 这些本地不支持场景不算上游账号普通业务失败，不触发冷却或账户错误策略。
- 授权实例从来源账户读取 bridge 模式；被授权用户不能单独改写来源账户的上游模式。
- API Key 多分组 fallback 仍按分组顺序处理，不因为 bridge 模式跨分组混排账号。
- 会话亲和命中 bridge 账户时仍要检查 endpoint 能力；如果本轮 endpoint 不支持，不能为了粘性强行使用。
- 网关运行时缓存需要把 `openai_responses_upstream_mode` 纳入账号快照和缓存失效范围，账户编辑后必须清理候选缓存。

### 前端与用户体验

前端不能把该能力表达成“完整支持 Responses”。

建议文案：

- 字段：`Responses 上游模式`
- 选项：`透传 Responses`、`转为 Chat Completions`
- 帮助：`仅用于上游不支持 /responses 的 API Key 账户。开启后网关会把普通 Responses 请求转换到 Chat Completions；不支持 compact、本地 previous_response_id 续链、复杂内置工具和部分 reasoning 字段。`

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

## 当前代码落点与放弃方案留档

当前已落地：

- `domain/types.ts`：账户 DTO 字段包含 Responses 上游模式。
- `storage` schema / repository：`accounts.openai_responses_upstream_mode` 已纳入读写、导入导出、公开接口和授权实例来源。
- `modules/accounts/accounts.routes.ts`：创建、编辑、草稿测试 schema 校验 Responses 上游模式。
- `modules/gateway/openai-gateway-route-helpers.ts`：根据账号模式构造上游 URL。
- `modules/gateway/openai-gateway-upstream.ts`：在 `buildUpstreamRequestParts` 中调用 Responses -> Chat 请求转换。
- `modules/gateway/openai-responses-chat-bridge.ts`：实现普通 `/responses` 请求转换、非流式响应转换、SSE 事件转换；非空 `previous_response_id` 返回本地 `400 responses_chat_bridge_previous_response_id_unsupported`。
- `modules/gateway/openai-gateway-account-capability-filter.ts`：按 endpoint family 和账号模式过滤候选，Chat bridge 不承接 `/responses/compact`。
- `modules/accounts/account-test.service.ts`：账户测试覆盖 bridge 模式，优先测试 `/v1/responses` 下游形态。
- `frontend/src/types/domain/accounts.ts`、`frontend/src/views/accounts/accountFormTypes.ts`、`accountFormDefaults.ts`、`accountCredentials.ts`、`accountSavePayload.ts`、`AccountStrategySection.vue`、`AccountTestModal.vue`：前端表单、payload 和测试结果展示 Responses 上游模式。
- 本文、`OpenAI账号接入.md`、`请求处理分层设计.md`、`接口契约与权限矩阵.md`、`SQLite存储说明.md`、`AI账户导入协议.md`：同步当前账户能力、接口字段和导入导出字段。

已放弃方案留档，不属于当前实现：

- `responses_chat_bridge_sessions`、`responses_chat_bridge_response_index`、`responses_chat_bridge_turn_payloads` 等本地 bridge 上下文表。
- bridge context payload store、context service、session cache、context flush service、可选 journal。
- 本地 `previous_response_id` 解析、上下文 replay、dirty session flush、payload 文件过期清理和墓碑保留。
- `/responses/compact`、`compaction_trigger` 或高水位自动摘要触发的本地 Chat summarization。
- 基于稳定 `bridge_session_id` 的本地 bridge 续链亲和。

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
- `/responses/compact` 在 bridge 模式下不会请求上游 Chat Completions；候选过滤返回 `responses_compact_not_supported_by_chat_bridge`。
- `compaction_trigger` 在 bridge 模式下不转成 Chat summarization。
- bridge 模式收到非空 `previous_response_id` 时返回 `responses_chat_bridge_previous_response_id_unsupported`，不静默忽略、不请求上游、不写账号失败。
- 本地 bridge session、response index、payload store、摘要 compact 和 replay 缓存均为已放弃方案留档，不作为当前验收项。
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
- Codex 对 `/responses/compact` 和 `compaction_trigger` 的实际返回格式要求；当前 Chat bridge 不承接该能力，只作为后续原生 Responses passthrough / OAuth Codex adapter 联调项。
- 国内目标上游对 `developer` role、`reasoning_effort`、`parallel_tool_calls`、`response_format`、stream usage 的兼容差异。
- 是否需要账户级细分选项：`chatMaxTokensField = max_tokens | max_completion_tokens`、`developerRoleMode = passthrough | system`、`reasoningMode = passthrough | remove`。
- 是否需要在审计日志 metadata 中固定记录 `upstreamEndpointFamily = chat_completions` 和 `downstreamEndpointFamily = responses`。
- CC Switch 对 `previous_response_id` 的具体本地历史策略仍需读源码核对；当前 Chat bridge 明确拒绝非空 `previous_response_id`。
