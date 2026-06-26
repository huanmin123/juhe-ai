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
4. 既不能映射也不能模拟的 hosted/native 能力，返回 OpenAI 形态的 agent guidance；请求本身非法、权限或状态边界问题才返回受控错误。

“完全兼容”的验收口径不是所有 OpenAI 字段都保证同等语义，而是客户端看到的协议形态、错误形态、流式事件、工具生命周期、usage 和状态续链都保持 OpenAI 风格；不可承接能力必须稳定、可诊断、可配置。

### 2.1 能力缺口 guidance-first 规则

- 上游供应商、当前模型或账号 profile 未声明支持的 hosted/native 能力，一律视为能力缺口，不视为网关服务端异常。
- 能力缺口默认返回符合下游协议的正常 agent guidance；不请求不支持的上游，不伪造 tool call / tool result / citation / 图像结果，不返回 500。
- guidance 必须给客户端 agent 可执行线索：缺少的能力、当前上游 / 模型事实、可选修复方向，例如配置本地 MCP / 工具执行器、切换支持该能力的模型或移除相关 tool。
- guidance 文案保持通用，不写具体客户端名称。后续是否查找本地 MCP、切换工具或改写下一轮请求交给客户端 agent 自行处理。
- 只有请求非法、权限 / 归属 / 状态边界、schema 校验或安全策略命中时，才返回本地协议错误。

## 3. 兼容等级

| 等级 | 名称 | 说明 | 默认策略 |
| --- | --- | --- | --- |
| L1 | 原生字段映射 | Anthropic Messages 有等价表达，例如 text、function tool、tool result、URL / data URL 图片输入 | 直接转换并纳入 mock / real E2E |
| L2 | 上游能力适配 | Anthropic 有相近能力，但依赖模型、beta header、profile 或服务端工具，例如 extended thinking、web search、computer use、code execution、structured outputs | 只有账号能力显式可用时启用；否则进入 L4 |
| L3 | 网关本地模拟 | Anthropic 没有等价协议，但网关可以托管状态或执行工具，例如 Responses compact、file_id 解析、OpenAI file_search、image_generation、code interpreter | 必须有本地运行时、权限、审计和用量策略；未配置时进入 L4 |
| L4 | Agent guidance / 受控不执行 | 当前不能可靠承接，例如 encrypted reasoning 原文验证、跨供应商 compact snapshot、未授权 MCP server | hosted/native tool 无执行器时返回正常 agent guidance；非法历史、权限或状态边界问题返回对应协议错误；都不请求不支持的上游 |
| L5 | 显式降级 | 不影响客户端正确性的优化字段，例如部分 metadata、detail hint、非关键 include 字段 | 记录审计 metadata；默认不用于会改变语义的能力 |

默认禁止把 L4 伪装成 L5。只要字段会改变模型可访问的信息、工具执行能力、推理状态或客户端后续流程，就必须映射、模拟、返回 agent guidance，或在请求本身非法时拒绝。

## 4. 当前实现状态

| 能力 | 当前状态 | 主要缺口 |
| --- | --- | --- |
| Chat JSON / SSE 文本 | 已支持 | `n>1`、`logprobs=true` / `top_logprobs>0`、音频输出 `modalities` / `audio` 在 Anthropic bridge 下本地拒绝，不静默返回单候选、无 logprobs 或纯文本假成功 |
| Responses JSON / SSE 文本 | 已支持 | hosted tool 事件仍未完整覆盖；`include=reasoning.encrypted_content`、`include=message.output_text.logprobs`、`include=web_search_call.action.sources`、`include=computer_call_output.output.image_url`、`include=message.input_image.image_url` 和 `top_logprobs>0` 在 Anthropic bridge 下本地拒绝，不静默忽略；`include=code_interpreter_call.outputs` 在 Responses `code_interpreter` mock runtime 和 local_runtime worker 首段下返回 logs，local_runtime 会附带文件产物 `file_id` 和元数据摘要，其他模式仍拒绝 |
| Function tools | 已支持 | `tool_choice` 的 `auto` / `none` / `required` / 指定 function / Responses `allowed_tools` function 子集已按 Anthropic tool_choice 映射；reasoning / thinking 与强制工具调用冲突时本地拒绝 |
| Tool result / function_call_output | 已支持 | 已在本地拒绝缺少 call id、缺少匹配 tool call 的 orphan tool result / function_call_output，以及同一调用 ID 的重复工具结果，防止工具历史不闭合后才到上游失败 |
| 图片 / 文件输入 | 图片 URL / data URL、Chat / Responses inline PDF / text、Responses PDF `file_url` 已支持 | `file_id` 由 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 的 Files resolver 承接；Office / CSV 等非 text/PDF 文件需要本地抽取 |
| JSON object / JSON schema | `json_object` 为提示词 best-effort；`json_schema` 已用合成 Anthropic tool 强制输出，并做本地 JSON schema 子集二次校验 | 失败自动重试尚未实现；当前按下游协议受控失败 |
| Reasoning / thinking | 已把 OpenAI `reasoning.effort` 映射到 Anthropic `thinking`，并把 thinking 输出渲染为 Responses reasoning item | Chat 形态默认不暴露 thinking；`none` 不启用 Anthropic thinking；`minimal` / `low` / `medium` / `high` 映射固定 budget；`xhigh` 和未知 effort 当前本地拒绝，避免静默降级；`reasoning.summary=none` 抑制 Responses reasoning item，`auto` / `concise` / `detailed` 接受但不承诺精确控制 Anthropic summary 细节；模型不支持 thinking 时仍按真实上游错误处理；Anthropic thinking 与强制 `tool_choice` 不兼容，本地 OpenAI 错误拒绝，不请求上游 |
| Responses `previous_response_id` | Codex SSE 和普通 Responses JSON / SSE 场景已支持 | 状态由网关本地保存并按 API Key / 分组 / 供应商档案边界恢复；未知 id 本地 404、跨 API Key 续链本地 403，均不请求 Anthropic；跨 profile / 深链路压力继续按状态层专项验证 |
| `/responses/compact` | 不作为 Anthropic Messages 直转 endpoint；继续由网关托管 compact preflight / summary compact 承接 | Anthropic bridge 已覆盖 `compaction_summary` 输入恢复；`juhecmp.v2` snapshot 需要同时校验授权边界和摘要 digest，跨 API Key 或 digest 篡改均本地拒绝且不请求 Anthropic；统一非原生 Responses 上游 compact 状态层待后续抽象 |
| OpenAI hosted tools | 已逐类返回正常 agent guidance 或本地承接；`web_search` 不再走本地预取模拟；`file_search`、`image_generation`、Responses `tool_search` + `namespace` function 本地展开、Responses `code_interpreter` mock runtime、Responses `code_interpreter` local_runtime Python worker 首段、Responses `computer` mock runtime、Responses `computer` local_runtime adapter 首段、Responses `computer` HTTP sandbox adapter bridge 首段、Responses `mcp` mock proxy 和 MCP `local_runtime` allowlist executor 第一段已有路径 | `tool_search` 本地展开只处理请求内已声明的 namespace function，并恢复 Responses `function_call.namespace`，不伪造 `tool_search_call` / `tool_search_output`；`code_interpreter` mock runtime 只在显式 mock 模式下固定 `code_interpreter_call` JSON / SSE 外形，不执行代码、不请求 Anthropic、不代表真实沙箱；`code_interpreter` local_runtime 首段会把 Responses `code_interpreter` 转为 Anthropic `python` client tool，由网关独立 Python 子进程执行后回灌 `tool_result`，支持 stdout / stderr logs、超时、输出截断、安全 env 不泄漏、文件产物元数据摘要、符合上限产物写入本地 Files 后的 `/v1/files/{file_id}/content` 下载，以及 `/v1/containers/{container_id}/files` / `.../{file_id}/content` 兼容壳；该首段仍不是容器 / VM 等价隔离，也不支持完整 create/upload/delete container 生命周期；`computer` mock runtime 只在显式 mock 模式下固定 `computer_call` JSON / SSE 外形，并在收到 `computer_call_output` 后正常收口，不启动浏览器、不访问网页、不请求 Anthropic、不回显截图正文；`computer` local_runtime 首段已具备 adapter 接口、未配置 adapter 本地 503、测试 adapter JSON / SSE `computer_call`、`computer_call_output` 收口、会话 / 动作 metadata 裁剪和截图正文省略；HTTP sandbox adapter bridge 只有显式配置 `JUHE_AI_COMPUTER_BROWSER_ADAPTER_*` 时启用，由外部受控浏览器 / container browser 服务执行真实动作，网关负责调用、超时 / 响应体限制、JSON 归一化和敏感字段脱敏；该首段仍不代表网关内置完整 Playwright / container browser 会话管理；`mcp` mock proxy 只在显式 mock 模式下命中固定 `mock-mcp` allowlist，返回 `mcp_list_tools`、`mcp_approval_request` 或 `mcp_call`，不连接远程 MCP、不回显 authorization、不代表真实 MCP proxy；MCP `local_runtime` 已具备 allowlist executor 第一段，JSON 非流式免批路径会先请求 Anthropic 让模型选择导入后的 MCP client tool，再执行 MCP JSON-RPC `initialize` / `tools/list` / `tools/call`，并把 `tool_result` 回灌给 Anthropic 生成最终回答，支持 authorization 白名单透传、`allowed_tools` 过滤、输出截断、有限重试、重定向拒绝诊断和 HTTP-SSE JSON-RPC result frame 解析；默认 approval 或非免批工具本地返回 `mcp_approval_request`，不上游、不调用 `tools/call`；`mcp_approval_response.approval_request_id` 已按业务库 pending 记录、server/tool/arguments、当前 API Key / 分组 scope 和 TTL 校验，错误 ID、跨 scope、过期、非 pending 和 replay 本地拒绝；MCP 工具循环已支持受限多轮并有轮次上限，仍未完成真实第三方 HTTP-SSE 长连接联调、人工审批 UI 和 execution record 查询；`image_generation` 首批支持 Responses generate JSON / SSE，provider 可选择 OpenAI Images API 或 OpenAI Responses API 图像工具形态；provider 未配置时返回 OpenAI 形态 guidance 且不命中 Anthropic；provider 审核失败返回 Responses `status=failed` 并保留稳定错误码；Responses `computer` / `code_interpreter` / `mcp` 与 Chat `code_interpreter` 的无 adapter guidance 已有 mock 覆盖；code_interpreter 容器级隔离、computer 完整内置浏览器 runtime 和 MCP 完整真实运行时仍需后续推进；Chat 搜索模型路径不提供 Responses 的完整工具控制和 sources 能力 |
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
| `temperature`、`top_p`、`stop`、`max_*_tokens` | Anthropic 同名或等价字段 | L1/L4 | `temperature` 在 Anthropic 原生范围内正常映射；OpenAI 允许但 Anthropic 无法等价承接的 `temperature>1` 本地拒绝；其他无等价字段按 L5 审计或 L4 拒绝 |
| `metadata` / `user` / `safety_identifier` / `prompt_cache_key` | Anthropic `metadata.user_id`、网关本地亲和或审计 metadata | L1/L5 | `safety_identifier` 优先映射为 Anthropic `metadata.user_id`，否则使用 `user`；`prompt_cache_key` 只作为本地亲和 / 审计信号，不承诺 Anthropic prompt cache；业务 `metadata` 不扩散到 Anthropic；mock 固定 safety_identifier 优先级、user fallback 和业务 metadata 不透传 |
| `service_tier` | OpenAI 服务档位，无 Anthropic Messages 等价字段 | L4/L5 | 省略、`null`、`auto` 或 `default` 视为默认服务路径；`flex`、`priority` 或未知值本地 OpenAI 错误拒绝，避免客户端误判已获得 OpenAI 服务档位 |
| `prompt_cache_retention` | OpenAI prompt cache 保留策略，无 Anthropic 等价字段 | L4 | 字段存在且非 `null` 时本地 OpenAI 错误拒绝；不能把 OpenAI 24h / in_memory 保留策略静默降级为普通 Anthropic 请求 |
| Responses `prompt` 模板 | OpenAI Prompt Template 引用和变量 | L4 | 当前 bridge 不解析 OpenAI 托管 prompt template；字段存在且非 `null` 时本地 OpenAI 错误拒绝，不把缺少 prompt 内容的请求继续发给 Anthropic |
| Chat / Responses 顶层 `moderation` | OpenAI 输入 / 输出审核配置，无 Anthropic bridge 等价策略 | L4 | 顶层 `moderation` 存在且非 `null` 时本地 OpenAI 错误拒绝；注意 `image_generation` tool 内部 `moderation` 由图像 provider 路径单独承接 |
| Chat / Responses `store` | OpenAI 响应存储、distillation / evals 或后续检索语义 | L3/L4/L5 | 省略、`null` 或 `false` 正常无状态桥接；`true` 在 Chat 下代表存储输出用于 OpenAI 后续产品，在 Responses 下代表可后续 API retrieve / 状态复用，当前 Anthropic bridge 没有等价存储、TTL、隐私和授权检索策略，必须本地 OpenAI 错误拒绝 |
| `presence_penalty` / `frequency_penalty` / `logit_bias` / `seed` | Anthropic Messages 无等价采样控制 | L4 | 默认值或空 `logit_bias` 视为未请求；非默认惩罚、非空 `logit_bias` 或 `seed` 本地 OpenAI 错误拒绝，不请求 Anthropic |
| Chat / Responses `prediction` | OpenAI Predicted Outputs，无 Anthropic Messages 等价结构和 usage 语义 | L4 | 本地 OpenAI 错误拒绝；不能忽略 prediction 后返回普通生成结果 |
| Chat / Responses `verbosity` | OpenAI 专用输出详细度控制，无 Anthropic 等价字段 | L4 | top-level `verbosity` 或 Responses `text.verbosity` 存在时本地 OpenAI 错误拒绝 |
| Chat `n` | Anthropic Messages 单次请求只返回一个候选 | L1/L4 | `n` 省略或 `1` 正常桥接；`n>1` 本地 OpenAI 错误拒绝且不请求 Anthropic，避免客户端误判为完整多候选响应 |
| Chat `logprobs` / `top_logprobs` | Anthropic Messages 无 OpenAI token logprobs 等价响应字段 | L4 | `logprobs=true` 或 `top_logprobs>0` 时本地 OpenAI 错误拒绝；`logprobs=false` 和 `top_logprobs=0` 视为未请求 |
| Responses `include=message.output_text.logprobs` / `top_logprobs` | Anthropic Messages 无 OpenAI output_text logprobs 等价响应字段 | L4 | `include=message.output_text.logprobs` 或 `top_logprobs>0` 时本地 OpenAI 错误拒绝且不请求 Anthropic；不能返回缺少 logprobs 的成功响应 |
| Responses 其他输出扩展 `include` | 按本地工具运行时补齐，或本地拒绝 | L2/L3/L4 | `file_search_call.results` 已由本地 File Search 运行时承接；`code_interpreter_call.outputs` 在 `code_interpreter=mock` 时返回固定 logs，在 `code_interpreter=local_runtime` worker 首段返回真实 stdout / stderr logs、文件产物 `file_id` 和元数据摘要；`web_search_call.action.sources`、`computer_call_output.output.image_url`、`message.input_image.image_url` 当前没有等价输出结构或运行时，命中时本地 OpenAI 错误拒绝，不静默返回缺少 include 数据的成功响应 |
| Chat `modalities` / `audio` 输出 | Anthropic Messages 不能返回 OpenAI Chat audio object | L4 | `modalities` 省略或仅包含 `text` 正常桥接；包含 `audio`、未知输出模态或提供 `audio` 配置时本地 OpenAI 错误拒绝，不请求 Anthropic |
| Chat / Responses audio content block | Anthropic Messages 当前无 OpenAI `input_audio` 等价输入块 | L4 | 命中 `input_audio` / audio content 时本地 OpenAI 错误拒绝；不能静默丢弃音频输入后让模型只看文本 |
| Chat / Responses 未知 content block | 无明确等价字段 | L4 | 未分类 content block 本地 OpenAI 错误拒绝，后续新增支持时先更新本矩阵和 mock；禁止静默跳过 |

### 5.2 工具

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Chat `tools[].type=function` | Anthropic client tool `input_schema` | L1 | 已支持，补 schema / strict 测试 |
| Responses `tools[].type=function` | Anthropic client tool `input_schema` | L1 | 已支持；`tool_choice.type=allowed_tools` 的 function 子集通过过滤工具列表承接，`mode=required` 映射为 Anthropic `any` |
| `parallel_tool_calls=false` | Anthropic `tool_choice.disable_parallel_tool_use=true` | L1 | 仅在存在工具且 Anthropic `tool_choice` 为 `auto` / `any` / 指定 `tool` 时设置；`tool_choice=none` 不附加该字段，因为不会调用工具且 Anthropic 不接受 `none.disable_parallel_tool_use` |
| Responses `max_tool_calls` | Anthropic Messages 无等价工具调用次数上限 | L4 | 字段存在且非 `null` 时本地 OpenAI 错误拒绝，不请求 Anthropic；不能忽略后让模型调用超过客户端限制的工具 |
| Chat `tool_calls` | assistant `tool_use` block | L1 | 已支持；`function.arguments` 为 JSON object 时原样转 Anthropic `input`，为 JSON 非对象或非法 JSON 时包成 `openai_arguments` / `openai_arguments_text`，避免静默丢历史；多工具顺序仍需继续补测 |
| Chat `role=tool` | user `tool_result` block | L1 | 已支持；缺少 `tool_call_id`、缺少匹配 `tool_call_id` 或同一 `tool_call_id` 重复返回时，本地 OpenAI 错误拒绝，不请求 Anthropic |
| Responses `function_call` | assistant `tool_use` block | L1 | 已支持；`arguments` 为 JSON object 时原样转 Anthropic `input`，为 JSON 非对象或非法 JSON 时包成 `openai_arguments` / `openai_arguments_text`，避免静默丢历史 |
| Responses `function_call_output` | user `tool_result` block | L1 | 已支持；缺少 `call_id`、缺少匹配 `call_id` 或同一 `call_id` 重复返回时，本地 OpenAI 错误拒绝，不请求 Anthropic |
| Chat 搜索模型 / `web_search` / `web_search_preview` | Anthropic 原生 server tool / web search 等价能力 | L2/L4 | 当前 bridge 不做本地预取模拟；没有上游原生等价能力时返回正常 agent guidance，不请求 Anthropic、不伪造 citation 或 hosted item |
| `file_search` | 网关本地 Files / Vector Store 检索后注入 Anthropic system context | L3 | 已按 [OpenAI 兼容 Files 与 File Search 本地运行时设计](OpenAI兼容Files与FileSearch本地运行时设计.md) 实现本地 Files、Vector Store、文本 chunk、keyword retrieval 和 Responses JSON / SSE `file_search_call`；Chat 入口返回 `message.annotations`；vector store file 支持 `in_progress` / `completed` / `failed` 轮询生命周期，未找到、未授权或未就绪时本地 OpenAI 错误 |
| `code_interpreter` / container | Anthropic code execution tool 或网关本地沙箱；mockai 阶段可返回固定 Responses `code_interpreter_call` | L2/L3/L4 | 必须隔离文件、网络、时间和资源；没有沙箱时返回 agent guidance；Responses `code_interpreter=mock` 已覆盖 JSON / SSE、`include=code_interpreter_call.outputs` 固定 logs、不请求 Anthropic、不执行代码；`code_interpreter=local_runtime` 已完成 Python worker 首段：未配置 Python executor 时本地 503，配置后由 Anthropic 选择 `python` client tool，网关独立子进程执行并回灌 `tool_result`，返回 `code_interpreter_call.outputs` logs、退出码、超时、截断 metadata、文件产物元数据摘要、符合上限产物的本地 Files `file_id` 下载路径和 container files 兼容壳；Chat 入口仍 guidance；容器 / VM 级隔离、container create/upload/delete 和生命周期按 [OpenAI 托管工具运行时设计](OpenAI托管工具运行时设计.md) 后续推进 |
| `computer` | Anthropic computer use 或网关本地 computer tool adapter | L2/L3 | 需要屏幕状态、动作协议和权限确认；不能用纯文本模拟；无 adapter 时返回 agent guidance；Responses 无 adapter guidance 已有 mock 覆盖且不请求 Anthropic；`computer=mock` 已覆盖固定 `computer_call` JSON / SSE、`computer_call_output` 收口和截图正文不回显；`computer=local_runtime` 首段已覆盖 adapter gate、未配置 503、测试 adapter `computer_call` JSON / SSE、`computer_call_output` 收口、会话 / 动作 metadata 裁剪和截图正文省略；HTTP sandbox adapter bridge 已覆盖显式 endpoint、本机 mock adapter 调用、`computer_call` / message 归一化、metadata `adapter=http_browser`、响应体上限和敏感字段脱敏；完整 Playwright / container browser 会话管理按 [OpenAI 托管工具运行时设计](OpenAI托管工具运行时设计.md) 继续推进，分为 `native_bridge` 和 `local_runtime` 两条路 |
| `mcp` / remote MCP | Anthropic MCP connector 或网关 MCP proxy；mockai 阶段可走固定 allowlist 的本地 mock proxy；`local_runtime` 已进入 allowlist executor 第一段 | L2/L3/L4 | 需要 server allowlist、auth、approval 和审计映射；默认返回 agent guidance；Responses 无 adapter guidance 已有 mock 覆盖且不请求 Anthropic；Responses `mcp=mock` 已覆盖固定 `mock-mcp` allowlist、`allowed_tools` 过滤、approval request、approval id 绑定校验、`mcp_call` JSON / SSE、authorization 不回显和未授权 server 本地拒绝；Responses `mcp=local_runtime` 未配置 executor 时本地 503，`connector_id` 本地 400；配置 allowlist executor 后，JSON 非流式免批路径会把 MCP tools/list 导入 Anthropic client tools，由 Anthropic `tool_use` 触发 MCP JSON-RPC `tools/call`，再把 `tool_result` 回灌给 Anthropic 生成最终回答，authorization 仅入 allowlist server，并支持输出截断、有限重试、重定向拒绝诊断和 HTTP-SSE JSON-RPC result frame；Responses SSE 免批路径会先缓冲首轮 Anthropic SSE，再执行同一套多轮工具循环，并以 typed SSE 输出 `mcp_list_tools`、`mcp_call_arguments.delta/done`、`mcp_call.in_progress`、成功 `mcp_call` 或失败 `mcp_call.failed`、最终 assistant message；当前 MCP arguments delta 是完整参数已知后的单片段兼容事件，不是 OpenAI 原生远程 MCP 的实时逐片段执行流；默认 approval 或非免批工具本地返回 `mcp_approval_request`，不上游、不调用 `tools/call`；approval response 的 `approval_request_id` 必须命中业务库 pending 记录，且匹配当前 server/tool/arguments、当前 API Key / 分组 scope 和 TTL，错误 ID / scope / 状态本地 400 / 403；真实 MCP proxy 首批只承接 `server_url` 远程 MCP，OpenAI `connector_id` 需要独立 connector adapter，不能用 remote proxy 伪装；多轮工具循环已支持受限轮次上限，execution record 首段已进入业务库并提供管理侧 / 用户侧摘要查询 API，仍需补人工审批 UI、server 管理、execution record 查询页面和真实第三方 HTTP-SSE 长连接联调 |
| Responses `tool_search` + `namespace` function | 请求内 namespace function 展开为 Anthropic client tool，回包恢复 Responses `function_call.namespace` | L1/L3/L4 | 已支持 JSON / SSE 首批本地展开；`tool_choice` 指定 namespace function 时映射到展开后的 Anthropic tool name；不输出 `tool_search_call` / `tool_search_output`，需要完整 hosted search 生命周期时改用原生 Responses 或后续本地检索 runtime；无可展开 function 时返回 guidance |
| Codex `shell` / `skills` / 本地 `tool_search` | 网关本地工具运行时 | L3 | 只有明确引入本地执行器后启用；模型侧不能凭空执行；真实运行时按 [OpenAI 托管工具运行时设计](OpenAI托管工具运行时设计.md) 推进 |
| `image_generation` | 本地图像生成 provider 或独立图像 API | L3 | Anthropic Messages 不产生图像；首批由 Anthropic 生成 revised prompt，再调用本地图像 provider，provider 可为 OpenAI Images API `/v1/images/generations` 或 OpenAI Responses API `/v1/responses` 图像工具；Responses JSON / SSE 返回 `image_generation_call`；无 provider 时返回 guidance 且不命中 Anthropic；provider `moderation_blocked`、非 JSON / 缺失结果、超大响应体、超时返回 Responses failed object；edit / mask / 历史图像复用已覆盖 guidance |

默认策略：列出 hosted tool 但没有可用适配器时，不删除该 tool 后继续请求上游。只有明确配置“可降级的 optional hosted tool”并写审计时，才允许 L5；否则返回正常 agent guidance。

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
| Chat `image_url.url` data URL | Anthropic base64 image source | L1 | 已支持；仅接受 `image/jpeg` / `image/png` / `image/gif` / `image/webp` 且 base64 合法，其他 MIME 或非法 base64 本地 OpenAI 错误拒绝，不请求 Anthropic |
| Responses `input_image.image_url` | Anthropic image source | L1 | URL 已支持，data URL 仅接受 `image/jpeg` / `image/png` / `image/gif` / `image/webp` 且 base64 合法，其他 MIME 或非法 base64 本地拒绝 |
| Chat `content[].type=file` + `file_data` | Anthropic document block | L2 | 仅支持 inline PDF / text 数据；`file_id` 转换入口已接 resolver，未配置本地 Files runtime 时 L4 |
| Responses `input_image.file_id` | 本地 Files resolver 读取后转 image source | L3 | 已支持本地 `/v1/files` 上传后解析为 Anthropic image；未知或无权 file id 返回本地错误，不请求上游 |
| Responses `input_file.file_data` | Anthropic document block | L2 | 支持 PDF base64 / text/plain；不支持的 MIME 返回本地错误，不静默忽略 |
| Responses `input_file.file_url` | Anthropic document URL source | L2 | 仅按 PDF URL 子集桥接；其他文件格式需要本地抽取或 Files resolver |
| Responses `input_file.file_id` | 本地 Files resolver 读取后转 document source | L3 | 已支持本地 `/v1/files` 上传后解析 PDF / text；OpenAI file id 不直接交给 Anthropic；未知或无权 file id 本地失败 |
| 输出图片 / `image_generation_call` | 本地图像生成 provider 后渲染 OpenAI image item | L3 | 与 Messages 文本响应分离；provider 成功路径输出 base64 `result` 和 `revised_prompt`；provider 失败路径不输出图片，保留 OpenAI 风格 `error.type` / `error.code` / `moderation_details`；审计不保存非流式 `result` 或流式 `partial_image_b64` 正文；流式支持 completed 事件和最终 `response.completed`，当 provider 返回 Images SSE 或 Responses SSE 图像 partial 时透出 `response.image_generation_call.partial_image`；真实账户 Images API 401，但 Responses API 图像工具已验证可返回真实 `image_generation_call.result` |
| 图片 `detail` | Anthropic 无完全等价字段 | L5 | 只作审计或提示，不影响图像本体 |

### 5.5 Reasoning / Thinking

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Responses / Chat `reasoning.effort` / `reasoning_effort` | Anthropic `thinking` / `budget_tokens` 或禁用 thinking | L2/L4 | `none` 表示不启用 Anthropic thinking；`minimal` / `low` / `medium` / `high` 分别映射为 1024 / 2048 / 4096 / 8192 `budget_tokens`；`xhigh` 需要模型和 profile 明确支持更大预算前保持本地 OpenAI 错误拒绝；未知 effort 本地拒绝，避免客户端误以为高级推理已生效 |
| Responses `reasoning.summary` | Responses reasoning item 渲染策略 | L2/L4 | `auto` / `concise` / `detailed` 接受并尽力把 Anthropic thinking block 渲染为 Responses reasoning summary；当前不承诺精确控制摘要详细度；`none` 抑制 reasoning item，但 usage 的 reasoning tokens 仍按上游字段映射；未知 summary 本地拒绝 |
| Anthropic `thinking` 输出 | Responses `reasoning` item 或审计字段 | L2 | 不把隐藏思考混入普通 `output_text`；只输出允许暴露的 summary |
| Anthropic `signature_delta` | 审计 / 状态校验 | L2 | 不发给 OpenAI 普通客户端，除非有明确 Responses reasoning contract |
| OpenAI `include=reasoning.encrypted_content` / reasoning item `encrypted_content` | 受控拒绝或网关本地状态恢复 | L4 | Anthropic 不能生成或验证 OpenAI encrypted reasoning；`include=reasoning.encrypted_content` 和历史 `type=reasoning` item 只带 `encrypted_content` 时，当前 bridge 本地返回 OpenAI 形态错误并且不请求 Anthropic，不能静默省略；网关自有 compact envelope 只能走 `compaction` / `compaction_summary` 状态恢复路径 |
| Reasoning token usage | Responses `output_tokens_details.reasoning_tokens` 和使用记录 | L2 | 有上游字段时映射；无字段时不编造 |

安全规则：桥接层不能泄露模型不应暴露的完整 chain-of-thought。即使 Anthropic 返回 thinking block，也只按下游协议允许的 reasoning summary 或审计摘要处理。

### 5.6 状态、压缩和上下文

| OpenAI 能力 | Anthropic 承接方式 | 等级 | 长期策略 |
| --- | --- | --- | --- |
| Responses `previous_response_id` | 网关本地状态恢复后重建 Messages history | L3 | 已覆盖 Codex SSE 两轮续链、普通 Responses JSON / SSE 两轮续链、未知 id 404 和跨 API Key 403；恢复后会删除下游 `previous_response_id`，只把重建后的历史 input/output 发送到 Anthropic |
| Responses `conversation` | 网关本地 Conversation 状态恢复后重建 Messages history | L3/L4 | 当前 Anthropic bridge 不直接消费 OpenAI conversation 对象或 ID；字段存在且非 `null` 时本地 OpenAI 错误拒绝，直到 Conversation 状态层完成授权边界、历史恢复和写回策略 |
| Responses `background` | OpenAI 后台响应任务语义 | L4 | 省略、`null` 或 `false` 正常同步桥接；`true` 需要异步任务、轮询和取消语义，当前本地 OpenAI 错误拒绝，不请求 Anthropic |
| `/responses/compact` | 网关本地 summary compact snapshot | L3 | 不透传 Anthropic Messages；摘要必须绑定授权边界；返回 OpenAI CompactResource 兼容外形，`object=response.compaction`，`output` 中保留 1 个 `type=compaction` item；`encrypted_content` 使用 `juhecmp.v2.<compactId>.<summaryDigest>` 外壳 |
| `compaction` / `compaction_summary` item | 网关 snapshot 或 inline summary 解析为 system summary | L3 | 同时接受官方 `compaction` 和 Codex 兼容别名 `compaction_summary`；`juhecmp.v1` inline summary 直接恢复到 Anthropic system context；`juhecmp.v2` snapshot 必须先由状态层校验边界、TTL 和 digest 后恢复，跨授权边界返回 403，digest 篡改或 snapshot 缺失返回 404，均不请求不支持 compact 的 Anthropic 上游 |
| Responses `store` | 网关本地状态策略 | L3/L4/L5 | 省略、`null` 或 `false` 正常无状态桥接；`true` 表示响应需要后续 API retrieve / 状态复用，当前 Anthropic bridge 没有通用 Responses 存储、TTL、隐私和授权检索层，必须本地 OpenAI 错误拒绝，不返回 `store:false` 假成功 |
| Responses `truncation` / `context_management` | 本地策略或 Anthropic 原生字段 | L2/L3/L4 | `truncation` 省略、`null` 或 `disabled` 正常桥接；`truncation=auto` 需要状态层按 OpenAI 规则丢弃最早对话项，当前本地拒绝；`context_management` 存在且非 `null` 时本地拒绝，直到网关实现可审计的上下文管理策略 |

## 6. 响应和流式矩阵

| Anthropic 输出 | Chat JSON / SSE | Responses JSON / SSE | 策略 |
| --- | --- | --- | --- |
| text | `message.content` / `delta.content` | `message.output_text` / `response.output_text.delta` | 已支持，补多 block 顺序 |
| tool_use | `tool_calls` / `delta.tool_calls` | `function_call` item | 已支持；Chat SSE 工具 index 必须按 OpenAI tool call 序号连续递增，不能直接暴露 Anthropic content block index；Responses SSE 必须把 Anthropic `input_json_delta.partial_json` 转成 `response.function_call_arguments.delta`，并在工具块结束时输出 `response.function_call_arguments.done` 和最终 `response.output_item.done` |
| thinking | 默认不进 content | `reasoning` item 或审计 summary | 已实现基础映射，必须防止泄露隐藏思考 |
| web_search result / citation | 不输出本地模拟 citation | 不输出 `web_search_call` | 当前无 Anthropic 原生等价映射；命中 `web_search` 时返回 agent guidance |
| file_search result / citation | Chat JSON `message.annotations`；Chat SSE 正文保留 `[F1]` / `[F2]` 类引用标记 | Responses `file_search_call` + `file_citation` annotations / include results | 本地预检索模拟覆盖 Responses JSON/SSE 和 Chat JSON；结果来自本地 Vector Store keyword retrieval，不承诺 OpenAI 托管语义完全等价 |
| image_generation result | 不适用于 Chat text | `image_generation_call` / image output item | 已支持 Responses generate JSON / SSE 的本地 provider 首批路径 |
| error event | Chat SSE error / 断流策略 | `response.failed` | 已有基础，补 hosted tool 和 reasoning 场景 |
| usage | Chat `usage` / Chat SSE `stream_options.include_usage` usage chunk | Responses `usage` | Chat 非流式已映射；Chat SSE 默认 chunk 保持 `usage:null`，仅在 `stream_options.include_usage=true` 时在 `[DONE]` 前追加 `choices=[]` 的 usage chunk；Responses SSE 在 `response.completed` snapshot 中带 usage；补 cache write、本地工具成本 |

## 7. 策略层要求

后续实现必须新增或收敛出桥接策略层，用于在请求转换前分类：

- `native_map`：直接映射。
- `upstream_feature`：需要 Anthropic profile / beta / 模型支持。
- `local_emulation`：需要网关本地运行时。
- `best_effort_degrade`：显式允许且不改变核心语义的降级。
- `guidance`：本地 OpenAI 形态 guidance，供调用方 agent 自行选择本地 MCP / 工具 / 其他供应商。
- `reject`：本地 OpenAI 形态错误，仅用于请求非法、权限、状态链或 schema 校验等不能继续的边界。

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
| 输出形态边界 | Chat `n>1`、Chat `logprobs=true` / `top_logprobs>0`、Responses `include=message.output_text.logprobs` / `top_logprobs>0`、Responses 非等价输出扩展 include、Chat audio output、非等价采样控制、prediction、verbosity 本地 OpenAI 错误拒绝且不命中上游 |
| 输入内容边界 | Chat / Responses `input_audio` 和未知 content block 本地 OpenAI 错误拒绝且不命中上游 |
| Function tools | 多工具 JSON/SSE、指定工具、required、none、`parallel_tool_calls=false` 映射、Responses `max_tool_calls` 本地拒绝、Responses `allowed_tools` function 子集、非对象 / 非法 JSON arguments 保留、tool result、missing call id / orphan / duplicate tool result、thinking + 强制 tool_choice 冲突拒绝、流式 `input_json_delta` 参数分片 |
| Hosted tools | Responses / Chat `web_search` guidance 且不命中上游、Responses `computer` / `code_interpreter` / `mcp` guidance 且不命中上游、Chat `code_interpreter` guidance 且不命中上游、Responses `code_interpreter=mock` JSON / SSE 本地 `code_interpreter_call` 且不执行代码不上游、Responses `code_interpreter=local_runtime` Python worker 成功 / stderr / 截断 / 超时 / env 不泄漏、Responses `computer=mock` JSON / SSE 本地 `computer_call`、`computer_call_output` 收口且不回显截图正文不上游、Responses `computer=local_runtime` 未配置 adapter 503、测试 adapter JSON / SSE `computer_call`、`computer_call_output` 收口、metadata 裁剪和截图正文省略不上游、Responses `mcp=mock` JSON / SSE 本地 `mcp_list_tools` / `mcp_approval_request` / `mcp_call` 且不连接远程 MCP 不上游、Responses `mcp=local_runtime` JSON 免批模型选择工具 + MCP `tools/call` + 输出截断、Responses `mcp=local_runtime` 默认 approval 本地 `mcp_approval_request`、approval pending / approve / reject / expired / replay / 跨 API Key scope、Responses `mcp=local_runtime` SSE 缓冲式多轮终态输出、Responses `mcp=local_runtime` transport retry / redirect blocked、Responses `tool_search` + `namespace` JSON / SSE 本地展开、image_generation 无 provider guidance、image_generation Images provider JSON / completed-only SSE / partial SSE 成功路径、image_generation Responses provider JSON / SSE 成功路径、provider `moderation_blocked` / 非 JSON / 超大响应体 / timeout failed response、edit / mask / 历史复用 guidance、file_search 未知 vector store 本地失败、file_search 本地检索成功路径、vector store file `in_progress` / `failed` 生命周期 |
| Structured outputs | json_object 合法输出、json_schema strict 成功、schema 失败受控错误、Chat refusal、Responses mismatch code |
| Thinking | reasoning.effort 映射、`reasoning_effort` Chat 入口映射、`none` 不启用 thinking、`xhigh` / 未知 effort 本地拒绝、`reasoning.summary` 的 `auto` / `concise` / `detailed` / `none` 边界、thinking 输出不混入文本、reasoning usage 映射、`include=reasoning.encrypted_content` 和 reasoning item `encrypted_content` 本地拒绝 |
| 图片 / 文件 | 图片 URL、图片 data URL、图片 data URL MIME / base64 拒绝边界、Chat/Responses inline PDF / text 文件、Responses PDF URL、`/v1/files` 上传后 Chat/Responses `file_id` 成功路径、未知 `file_id` 受控失败 |
| 状态 / Compact | `previous_response_id` Codex SSE / 普通 JSON / 普通 SSE 续链、`previous_response_id` 未知 id / 跨 API Key 拒绝、`conversation` 本地拒绝、`background=true` 本地拒绝、`store=true` 本地拒绝、`truncation=auto` 本地拒绝、`context_management` 本地拒绝、`compaction` / `compaction_summary` 恢复、`/responses/compact` 返回 `response.compaction`、不直转 Anthropic、跨边界 snapshot 拒绝、`juhecmp.v2` digest 篡改拒绝且不上游 |
| SSE | text delta、tool input_json_delta、thinking_delta、Chat `stream_options.include_usage` usage chunk、上游 `event:error` |
| 回归 | Anthropic native `/v1/messages`、OpenAI-compatible `responses -> chat_completions`、混合路由 |

真实账户验证至少覆盖：

- 四入口基础成功。
- function tool 往返。
- 图片 URL 或 data URL。
- reasoning / thinking 请求，如果当前模型和上游 profile 支持。
- web_search 或 code execution，如果当前上游明确支持。
- 一个不可支持 hosted tool 的 agent guidance 响应。
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
- [Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create)
- [Chat Completions create reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)

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
