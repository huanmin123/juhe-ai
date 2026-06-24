# OpenAI 到 Anthropic 高兼容能力矩阵

## 1. 背景

`PLAN-0058` 已完成 OpenAI Chat / Responses 到 Anthropic Messages 的基础桥接，覆盖四类入口：

- Chat JSON
- Chat SSE
- Responses JSON
- Responses SSE

基础桥接解决了“请求能不能发通、返回能不能按下游协议渲染”的问题，但还没有覆盖 OpenAI 客户端常见的高级能力：hosted tools、structured outputs、reasoning / thinking、图片文件、Responses 状态和上下文压缩。用户侧目标是尽可能做到客户端无感知，客户端仍按 OpenAI 协议使用，混合路由选中 Anthropic 上游时不需要手工改请求。

本矩阵是后续长期维护依据。实现前必须先按本文件判断能力等级，不能继续把不可映射字段静默丢弃，也不能把 Anthropic 不支持的能力包装成“完全等价成功”。

## 2. 总体结论

客户端协议层可以做到高度无感：OpenAI Chat / Responses 请求进入网关后，由桥接层把请求转换为 Anthropic Messages，并把响应转换回 OpenAI Chat / Responses。

供应商能力层不能承诺 100% 语义无损。OpenAI hosted tools、Responses encrypted reasoning、Files / Vector Stores、image generation、computer use、code interpreter、MCP approval 等能力不是简单字段同构。长期方案必须是：

1. 能原生映射的字段直接映射。
2. Anthropic 有等价能力但需要 profile / beta / 模型支持的字段，按账号能力显式启用。
3. Anthropic 没有等价能力但网关可以实现的能力，由本地工具运行时或本地状态层模拟。
4. 既不能映射也不能模拟的能力，返回 OpenAI 形态的受控错误，不静默丢弃。

“完全兼容”的验收口径不是所有 OpenAI 字段都保证同等语义，而是客户端看到的协议形态、错误形态、流式事件、工具生命周期、usage 和状态续链都保持 OpenAI 风格；不可承接能力必须稳定、可诊断、可配置。

## 3. 兼容等级

| 等级 | 名称 | 说明 | 默认策略 |
| --- | --- | --- | --- |
| L1 | 原生字段映射 | Anthropic Messages 有等价表达，例如 text、function tool、tool result、URL / data URL 图片输入 | 直接转换并纳入 mock / real E2E |
| L2 | 上游能力适配 | Anthropic 有相近能力，但依赖模型、beta header、profile 或服务端工具，例如 extended thinking、web search、computer use、code execution、structured outputs | 只有账号能力显式可用时启用；否则进入 L4 |
| L3 | 网关本地模拟 | Anthropic 没有等价协议，但网关可以托管状态或执行工具，例如 Responses compact、file_id 解析、OpenAI file_search、image_generation、code interpreter | 必须有本地运行时、权限、审计和用量策略；未配置时进入 L4 |
| L4 | 受控拒绝 | 当前不能可靠承接，例如 encrypted reasoning 原文验证、跨供应商 compact snapshot、未授权 MCP server | 返回 OpenAI error / Responses failed event，不请求上游 |
| L5 | 显式降级 | 不影响客户端正确性的优化字段，例如部分 metadata、detail hint、非关键 include 字段 | 记录审计 metadata；默认不用于会改变语义的能力 |

默认禁止把 L4 伪装成 L5。只要字段会改变模型可访问的信息、工具执行能力、推理状态或客户端后续流程，就必须映射、模拟或拒绝。

## 4. 当前实现状态

| 能力 | 当前状态 | 主要缺口 |
| --- | --- | --- |
| Chat JSON / SSE 文本 | 已支持 | 需要补更多字段策略审计 |
| Responses JSON / SSE 文本 | 已支持 | reasoning item 和 hosted tool 事件仍未覆盖 |
| Function tools | 已支持 | 需要覆盖 strict schema 合成工具与更多 tool_choice 组合 |
| Tool result / function_call_output | 已支持 | 需要校验 orphan tool result，防止工具历史不闭合 |
| 图片 / 文件输入 | 图片 URL / data URL、Chat / Responses inline PDF / text、Responses PDF `file_url` 已支持 | `file_id` 由 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 的 Files resolver 承接；Office / CSV 等非 text/PDF 文件需要本地抽取 |
| JSON object / JSON schema | `json_object` 为提示词 best-effort；`json_schema` 已用合成 Anthropic tool 强制输出，并做本地 JSON schema 子集二次校验 | 失败自动重试尚未实现；当前按下游协议受控失败 |
| Reasoning / thinking | 已把 OpenAI `reasoning.effort` 映射到 Anthropic `thinking`，并把 thinking 输出渲染为 Responses reasoning item | Chat 形态默认不暴露 thinking；模型不支持 thinking 时仍按真实上游错误处理 |
| Responses `previous_response_id` | Codex 场景已支持 | 普通 Responses 状态、跨 profile 边界和 compact 场景需要补测 |
| `/responses/compact` | 不作为 Anthropic Messages 直转 endpoint；继续由网关托管 compact preflight / summary compact 承接 | Anthropic bridge 已覆盖 `compaction_summary` 输入恢复；统一非原生 Responses 上游 compact 状态层待后续抽象 |
| OpenAI hosted tools | 已逐类返回 OpenAI 形态受控错误；Responses `web_search` 在配置本地 HTTP search executor 后可做预取模拟；Chat web search 进入同一 L3 本地预取模拟路径；不会再用泛化错误或静默丢弃 | file_search / image_generation / code_interpreter / computer / MCP 需要对应运行时后才能启用；Chat 搜索模型路径不提供 Responses 的完整工具控制和 sources 能力 |
| 用量映射 | 已映射基本 input / output / cache read 和 thinking tokens | cache write、服务端工具用量和本地工具成本需要补充 |

## 5. 请求能力矩阵

### 5.1 基础消息

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Chat `messages[].role=user/assistant` 文本 | `messages[].content[].type=text` | L1 | 保持已实现逻辑，补边界测试 |
| Chat `system` / `developer` | 顶层 `system` 合并 | L1 | 保留来源顺序，不写入工具结果 |
| Responses `instructions` | 顶层 `system` | L1 | 与 input 中 system/developer 合并 |
| Responses `input` 字符串 | 单条 user text message | L1 | 保持已实现逻辑 |
| Responses `input[].type=message` | Messages user / assistant / system | L1 | 对未知 content block 进入能力分类，不静默吞掉 |
| `temperature`、`top_p`、`stop`、`max_*_tokens` | Anthropic 同名或等价字段 | L1 | 无等价字段时按 L5 审计或 L4 拒绝 |
| `metadata` / `user` | Anthropic `metadata.user_id` 或审计 metadata | L1/L5 | 不把敏感 metadata 扩散到上游 |

### 5.2 工具

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Chat `tools[].type=function` | Anthropic client tool `input_schema` | L1 | 已支持，补 schema / strict 测试 |
| Responses `tools[].type=function` | Anthropic client tool `input_schema` | L1 | 已支持，补 `tool_choice` 全组合 |
| Chat `tool_calls` | assistant `tool_use` block | L1 | 已支持，补多工具顺序测试 |
| Chat `role=tool` | user `tool_result` block | L1 | 已支持，补 orphan 拒绝 |
| Responses `function_call` | assistant `tool_use` block | L1 | 已支持，补参数 JSON 非对象处理 |
| Responses `function_call_output` | user `tool_result` block | L1 | 已支持，补历史闭合校验 |
| Chat 搜索模型 / `web_search` / `web_search_preview` | Anthropic server tool 或网关本地 web search tool | L2/L3 | Responses 已支持配置本地 HTTP search executor 后的预取模拟；Chat 按专用搜索模型语义处理为“强制预取一次”：网关执行搜索、把结果注入 Anthropic system，非流式 Chat 返回 `message.annotations`；流式 Chat 保留正文 `[n]` 引用标记，不伪造 Responses hosted item；未配置执行器时仍 L4 |
| `file_search` | 网关本地 Files / Vector Store 检索后注入 Anthropic system context | L3 | 已按 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 实现本地 Files、Vector Store、文本 chunk、keyword retrieval 和 Responses JSON / SSE `file_search_call`；Chat 入口返回 `message.annotations`；vector store file 支持 `in_progress` / `completed` / `failed` 轮询生命周期，未找到、未授权或未就绪时本地 OpenAI 错误 |
| `code_interpreter` / container | Anthropic code execution tool 或网关本地沙箱 | L2/L3 | 必须隔离文件、网络、时间和资源；没有沙箱时 L4 |
| `computer` | Anthropic computer use 或网关本地 computer tool adapter | L2/L3 | 需要屏幕状态、动作协议和权限确认；不能用纯文本模拟 |
| `mcp` / remote MCP | Anthropic MCP connector 或网关 MCP proxy | L2/L3 | 需要 server allowlist、auth、approval 和审计映射；默认 L4 |
| Codex `shell` / `skills` / `tool_search` | 网关本地工具运行时 | L3 | 只有明确引入本地执行器后启用；模型侧不能凭空执行 |
| `image_generation` | 本地图像生成 provider 或独立图像 API | L3 | Anthropic Messages 不产生图像；无图像运行时时必须 L4 |

默认策略：列出 hosted tool 但没有可用适配器时，不删除该 tool 后继续请求上游。只有明确配置“可降级的 optional hosted tool”并写审计时，才允许 L5；否则返回 OpenAI 形态错误。

### 5.3 结构化输出

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Chat `response_format.type=json_object` | 系统约束 + 输出 JSON 校验 | L1/L3 | 先校验模型输出，不合法时按策略重试或失败 |
| Chat `response_format.type=json_schema` | 合成 Anthropic tool 强制输出，或 Anthropic structured output 能力 | L2/L3 | 不再只靠提示词；成功输出必须能通过 JSON schema 校验；失败时返回合法 Chat Completion，`message.refusal` 携带 bridge mismatch code |
| Responses `text.format.type=json_schema` | 同上 | L2/L3 | 返回 Responses `output_text` 时必须是 schema JSON 字符串；失败时生成 Responses failed 语义，非流式会被 response-inspection 改写为 503 且保留 mismatch code |
| `strict=true` | schema 校验 + 必要时强制工具调用 | L3 | 不满足 strict 时返回受控错误，不能宣称成功；当前不做自动重试 |

实现优先级：先使用“合成工具 + 强制 `tool_choice` + JSON schema 校验”的通用路径，因为它不依赖 Anthropic structured output beta；后续再按账号能力切到 Anthropic 原生 structured output。

### 5.4 图片、文件和多模态

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Chat `image_url.url` 普通 URL | Anthropic image URL source | L1 | 已支持，补真实联调 |
| Chat `image_url.url` data URL | Anthropic base64 image source | L1 | 已支持，补大小 / MIME 校验 |
| Responses `input_image.image_url` | Anthropic image source | L1 | 已支持，补真实联调 |
| Chat `content[].type=file` + `file_data` | Anthropic document block | L2 | 仅支持 inline PDF / text 数据；`file_id` 转换入口已接 resolver，未配置本地 Files runtime 时 L4 |
| Responses `input_image.file_id` | 本地 Files resolver 读取后转 image source | L3 | 已支持本地 `/v1/files` 上传后解析为 Anthropic image；未知或无权 file id 返回本地错误，不请求上游 |
| Responses `input_file.file_data` | Anthropic document block | L2 | 支持 PDF base64 / text/plain；不支持的 MIME 返回本地错误，不静默忽略 |
| Responses `input_file.file_url` | Anthropic document URL source | L2 | 仅按 PDF URL 子集桥接；其他文件格式需要本地抽取或 Files resolver |
| Responses `input_file.file_id` | 本地 Files resolver 读取后转 document source | L3 | 已支持本地 `/v1/files` 上传后解析 PDF / text；OpenAI file id 不直接交给 Anthropic；未知或无权 file id 本地失败 |
| 输出图片 / `image_generation_call` | 本地图像生成 provider 后渲染 OpenAI image item | L3 | 与 Messages 文本响应分离；无 provider 时 L4 |
| 图片 `detail` | Anthropic 无完全等价字段 | L5 | 只作审计或提示，不影响图像本体 |

### 5.5 Reasoning / Thinking

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Responses `reasoning.effort` | Anthropic `thinking` / `budget_tokens` 或模型后缀策略 | L2 | 仅在账号 profile 和模型支持 thinking 时启用 |
| Anthropic `thinking` 输出 | Responses `reasoning` item 或审计字段 | L2 | 不把隐藏思考混入普通 `output_text`；只输出允许暴露的 summary |
| Anthropic `signature_delta` | 审计 / 状态校验 | L2 | 不发给 OpenAI 普通客户端，除非有明确 Responses reasoning contract |
| OpenAI `encrypted_content` | 网关本地状态恢复或受控拒绝 | L3/L4 | Anthropic 不能验证 OpenAI encrypted reasoning；不能直接转发 |
| Reasoning token usage | Responses `output_tokens_details.reasoning_tokens` 和使用记录 | L2 | 有上游字段时映射；无字段时不编造 |

安全规则：桥接层不能泄露模型不应暴露的完整 chain-of-thought。即使 Anthropic 返回 thinking block，也只按下游协议允许的 reasoning summary 或审计摘要处理。

### 5.6 状态、压缩和上下文

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Responses `previous_response_id` | 网关本地状态恢复后重建 Messages history | L3 | 已覆盖 Codex 场景，继续补普通 Responses 和跨边界测试 |
| `/responses/compact` | 网关本地 summary compact snapshot | L3 | 不透传 Anthropic Messages；摘要必须绑定授权边界；返回 OpenAI CompactResource 兼容外形，`object=response.compaction`，`output` 中保留 1 个 `type=compaction` item |
| `compaction` / `compaction_summary` item | 网关 snapshot 或 inline summary 解析为 system summary | L3 | 同时接受官方 `compaction` 和 Codex 兼容别名 `compaction_summary`；`juhecmp.v1` inline summary 直接恢复到 Anthropic system context；`juhecmp.v2` snapshot 必须先由状态层校验边界、TTL 和 digest 后恢复，未恢复时 L4 |
| OpenAI `store` | 网关本地状态策略 | L3/L5 | 对 Anthropic 不透传；需要明确 TTL 和隐私边界 |
| `truncation` / `context_management` | 本地策略或 Anthropic 原生字段 | L2/L3/L5 | Codex bridge 下不能把未知上下文策略直接交给 Anthropic |

## 6. 响应和流式矩阵

| Anthropic 输出 | Chat JSON / SSE | Responses JSON / SSE | 策略 |
| --- | --- | --- | --- |
| text | `message.content` / `delta.content` | `message.output_text` / `response.output_text.delta` | 已支持，补多 block 顺序 |
| tool_use | `tool_calls` / `delta.tool_calls` | `function_call` item | 已支持，补参数分片和多工具 |
| thinking | 默认不进 content | `reasoning` item 或审计 summary | 已实现基础映射，必须防止泄露隐藏思考 |
| web_search result / citation | Chat JSON `message.annotations`；Chat SSE 正文保留 `[n]` 标记 | Responses `web_search_call` + `annotations` / hosted tool item | 本地预取模拟覆盖 Responses JSON/SSE 和 Chat JSON/SSE；Chat 不输出 `web_search_call` hosted item |
| file_search result / citation | Chat JSON `message.annotations`；Chat SSE 正文保留 `[F1]` / `[F2]` 类引用标记 | Responses `file_search_call` + `file_citation` annotations / include results | 本地预检索模拟覆盖 Responses JSON/SSE 和 Chat JSON；结果来自本地 Vector Store keyword retrieval，不承诺 OpenAI 托管语义完全等价 |
| image_generation result | 不适用于 Chat text | `image_generation_call` / image output item | 依赖本地图像 provider |
| error event | Chat SSE error / 断流策略 | `response.failed` | 已有基础，补 hosted tool 和 reasoning 场景 |
| usage | Chat `usage` | Responses `usage` | 补 thinking、cache write、本地工具成本 |

## 7. 策略层要求

后续实现必须新增或收敛出桥接策略层，用于在请求转换前分类：

- `native_map`：直接映射。
- `upstream_feature`：需要 Anthropic profile / beta / 模型支持。
- `local_emulation`：需要网关本地运行时。
- `best_effort_degrade`：显式允许且不改变核心语义的降级。
- `reject`：本地 OpenAI 形态错误。

策略层需要输出审计 metadata：

- `bridgeType = openai_to_anthropic_messages`
- `sourceEndpointFamily`
- `upstreamEndpointFamily = messages`
- `capabilityDecisions[]`
- `unsupportedTools[]`
- `degradedFields[]`
- `localEmulationUsed[]`
- `thinkingMode`
- `structuredOutputMode`
- `compactMode`

## 8. 验证矩阵

mock 回归至少覆盖：

| 类别 | 必测场景 |
| --- | --- |
| 基础四入口 | Chat JSON、Chat SSE、Responses JSON、Responses SSE |
| Function tools | 多工具、指定工具、required、none、tool result、orphan tool result |
| Hosted tools | Responses / Chat web_search 配置执行器成功、web_search 不可用、image_generation required 受控失败、file_search 未知 vector store 本地失败、file_search 本地检索成功路径、vector store file `in_progress` / `failed` 生命周期 |
| Structured outputs | json_object 合法输出、json_schema strict 成功、schema 失败受控错误、Chat refusal、Responses mismatch code |
| Thinking | reasoning.effort 映射、thinking 输出不混入文本、reasoning usage 映射 |
| 图片 / 文件 | 图片 URL、图片 data URL、Chat/Responses inline PDF / text 文件、Responses PDF URL、`/v1/files` 上传后 Chat/Responses `file_id` 成功路径、未知 `file_id` 受控失败 |
| Compact | `compaction` / `compaction_summary` 恢复、`/responses/compact` 返回 `response.compaction`、不直转 Anthropic、跨边界 snapshot 拒绝 |
| SSE | text delta、tool input_json_delta、thinking_delta、上游 `event:error` |
| 回归 | Anthropic native `/v1/messages`、OpenAI-compatible `responses -> chat_completions`、混合路由 |

真实账户验证至少覆盖：

- 四入口基础成功。
- function tool 往返。
- 图片 URL 或 data URL。
- reasoning / thinking 请求，如果当前模型和上游 profile 支持。
- web_search 或 code execution，如果当前上游明确支持。
- 一个不可支持 hosted tool 的 OpenAI 形态错误。
- compact / previous_response_id 续链。

真实凭据只能通过临时环境变量传入，不写入仓库、文档、脚本默认值、日志或测试快照。

## 9. 官方资料

OpenAI：

- [Using tools](https://developers.openai.com/api/docs/guides/tools)
- [Images and vision](https://developers.openai.com/api/docs/guides/images-vision)
- [Files API](https://developers.openai.com/api/docs/api-reference/files)
- [File inputs](https://developers.openai.com/api/docs/guides/file-inputs)
- [File search](https://developers.openai.com/api/docs/guides/tools-file-search)
- [Retrieval and Vector Stores](https://developers.openai.com/api/docs/guides/retrieval)
- [Image generation](https://developers.openai.com/api/docs/guides/image-generation)
- [Migrate to the Responses API](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [Conversation state and compaction](https://developers.openai.com/api/docs/guides/conversation-state)
- [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs)

Anthropic：

- [Tool use overview](https://docs.anthropic.com/en/docs/build-with-claude/tool-use/overview)
- [Vision](https://docs.anthropic.com/en/docs/build-with-claude/vision)
- [PDF support](https://docs.anthropic.com/en/docs/build-with-claude/pdf-support)
- [Extended thinking](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking)
- [Structured outputs](https://docs.anthropic.com/en/docs/build-with-claude/structured-outputs)
- [Web search tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool)
- [Computer use tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/computer-use-tool)
- [Code execution tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/code-execution-tool)
- [MCP connector](https://docs.anthropic.com/en/docs/agents-and-tools/mcp-connector)

## 10. 不做范围

- 不做 Anthropic Messages 到 OpenAI Chat / Responses 的反向转换。
- 不把无法执行的 hosted tool 静默删除后继续请求上游。
- 不绕过 API Key、分组、供应商档案和模型映射边界执行本地工具。
- 不把 OpenAI encrypted reasoning 直接交给 Anthropic 上游。
- 不把完整 hidden thinking 混入普通文本输出。
- 不为支持高级兼容引入分布式依赖；本地状态和工具运行时仍按当前轻量部署边界设计。
