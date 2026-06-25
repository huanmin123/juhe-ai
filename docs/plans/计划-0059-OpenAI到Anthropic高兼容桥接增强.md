# PLAN-0059 OpenAI 到 Anthropic 高兼容桥接增强

## 基本信息

- 编号：PLAN-0059
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-24
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 网关 / 供应商驱动 / 模型映射 / Responses 状态 / 工具 / 审计 / 文档 / 验证

## 需求目标

- 背景：`PLAN-0058` 已完成 OpenAI Chat / Responses 到 Anthropic Messages 的基础桥接，但只覆盖基础消息、四入口、function tools 和部分状态续链。用户要求继续做到更完整的长期兼容，尽可能让 OpenAI / Codex 客户端无感知。
- 目标：在桥接层新增高兼容策略矩阵，逐步补齐工具、thinking、图片文件、结构化输出和压缩能力；不能支持的能力按 OpenAI 协议形态稳定报错，避免协议不兼容或静默丢字段。
- 交付物：高兼容能力矩阵、计划记录、后端策略层和增强实现、mock 回归、真实账户联调、凭据不落盘检查和最终验证记录。

## 范围边界

### 本次包含

- [x] 新增长期能力矩阵，定义原生映射、上游能力适配、本地模拟、agent guidance / 受控不执行和显式降级等级。
- [x] 新增桥接能力策略层，统一分类 OpenAI 字段、工具、thinking、图片和 structured output。
- [x] 将非 function hosted tools 从“泛化不支持”改成逐类策略：web_search、file_search、image_generation、code_interpreter、computer、MCP、tool_search / shell / skills；`web_search` 本地预取模拟已撤销，当前无上游原生等价能力时返回 guidance；`image_generation` 首批 generate 路径已由 PLAN-0061 本地 provider 承接。
- [x] 补齐 Chat web_search / 搜索模型路径的边界：不做本地 HTTP search executor 预取，不伪造 Chat `annotations` 或 Responses hosted item。
- [x] 补齐 JSON object / JSON schema 的强制结构化输出路径：已支持合成 Anthropic tool、本地 JSON schema 二次校验和受控失败。
- [x] 补齐 reasoning / thinking 策略：OpenAI `reasoning.effort` 到 Anthropic thinking 的受控映射、Anthropic thinking 输出到 Responses reasoning / 审计的安全渲染。
- [x] 补齐图片和文件边界：图片 URL / data URL 明确测试，`file_id` 没有本地 resolver 时返回 OpenAI 形态错误。
- [x] 补齐 inline 文件输入子集：Chat / Responses `file_data` 的 PDF / text 转 Anthropic document block，Responses PDF `file_url` 转 document URL。
- [x] 补齐 Responses compact / previous_response_id 在 Anthropic bridge 下的专项 mock 回归：`previous_response_id` 已覆盖；`compaction_summary` 输入恢复到 Anthropic system context 已覆盖。`/responses/compact` 仍由网关托管 compact preflight 承接，不作为 Anthropic Messages 原生转发。
- [x] 补齐 `/responses/compact` 网关托管 compact 的 OpenAI CompactResource 外形：输出统一为 `object=response.compaction` + 1 个 `type=compaction` item，同时保留对历史 `compaction_summary` 输入的消费兼容。
- [x] 补充真实账户 E2E，使用用户提供的上游账号通过临时环境变量验证可用能力和真实错误形态。
- [x] 同步相关功能文档中的新增边界和测试结果。

### 本次不包含

- 不做 Anthropic Messages 到 OpenAI Chat / Responses 的反向转换。
- 不把尚未实现的 hosted tool 静默删除后继续请求上游。
- 不引入 Redis、Kafka、对象存储或分布式会话依赖。
- 不把用户提供的真实 API Key 写入仓库、文档、默认脚本参数、日志或测试快照。
- 不承诺 OpenAI 和 Anthropic 在供应商能力层 100% 语义无损；本计划承诺客户端协议形态、错误形态和可诊断策略稳定。

## 关联文档

- 架构文档：`docs/architecture/架构总览.md`
- 基础桥接设计：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- 高兼容矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- Anthropic 接入：`docs/functions/Anthropic账号接入.md`
- Anthropic 与 GPT 对比：`docs/functions/Anthropic与GPT全链路能力对比.md`
- Files / File Search 本地运行时：`docs/functions/OpenAI兼容Files与FileSearch本地运行时设计.md`
- Hosted runtime 运行时：`docs/functions/OpenAI托管工具运行时设计.md`
- Responses 压缩：`docs/functions/Responses上下文压缩落地方案.md`
- 请求处理分层：`docs/functions/请求处理分层设计.md`
- 模型映射：`docs/functions/自定义模型与模型映射设计.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：先分类再转换。每个 OpenAI 字段和工具必须落到 `native_map`、`upstream_feature`、`local_emulation`、`best_effort_degrade` 或 `reject` 之一。
- 数据变化：本阶段优先不新增数据库 schema；策略决策先写入网关审计 metadata。如后续需要按能力筛选统计，再新增显式字段和索引。
- 接口变化：OpenAI 下游接口不新增客户端必填字段；错误仍按 Chat / Responses 形态渲染。
- 前端变化：本阶段默认不改前端页面；如后续要展示账号级 bridge capability，再单独补配置 UI。
- 后端变化：增强 `openai-anthropic-bridge`，补能力分类、结构化输出、thinking、文件边界、Chat / Responses `web_search` guidance、compact 专项状态和测试。
- 数据处理策略：不做旧结构兼容；真实凭据只走临时环境变量。

## 执行拆解

- [x] 新增 `docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`。
- [x] 创建本计划并纳入计划索引。
- [x] 更新基础桥接设计文档，明确 PLAN-0058 的“不做范围”已由本计划接续。
- [x] 梳理当前 bridge 代码缺口并标注第一批实现点。
- [x] 实现桥接能力策略层。
- [x] 实现 hosted tool 分类与 OpenAI 形态 guidance。
- [x] 补齐工具历史闭合校验：Chat `role=tool` 和 Responses `function_call_output` 缺少匹配 tool call 或重复提交同一调用结果时本地 OpenAI 错误拒绝，不请求 Anthropic。
- [x] 补齐 function `tool_choice` 边界：Responses `allowed_tools` function 子集过滤到 Anthropic tools；OpenAI reasoning / Anthropic thinking 与强制工具调用冲突时本地拒绝。
- [x] 补齐 function 并行工具约束：OpenAI `parallel_tool_calls=false` 映射到 Anthropic `tool_choice.disable_parallel_tool_use=true`，`tool_choice=none` 时不附加非法字段。
- [x] 补齐 Responses `max_tool_calls` 边界：Anthropic Messages 无等价工具调用次数上限，字段存在且非 `null` 时本地 OpenAI 错误拒绝，不请求上游。
- [x] 补齐历史 function call 参数保留：Chat `tool_calls[].function.arguments` 和 Responses `function_call.arguments` 为 JSON 非对象或非法 JSON 时不再静默转为空对象。
- [x] 补齐 Anthropic `tool_use` 流式反渲染边界：Chat SSE `tool_calls[].index` 按 OpenAI 工具序号连续递增，Responses SSE 多 function_call item 和 `input_json_delta` 参数分片可消费。
- [x] 补齐 encrypted reasoning include 边界：Responses `include=reasoning.encrypted_content` 在 Anthropic bridge 下本地 OpenAI 错误拒绝，不请求上游，不静默省略。
- [x] 补齐 encrypted reasoning input item 边界：Responses 历史 `type=reasoning` item 如果只带 `encrypted_content`，本地 OpenAI 错误拒绝且不请求 Anthropic；compact envelope 仍只由 `compaction` / `compaction_summary` 恢复。
- [x] 补齐 Responses 非等价输出扩展 include 边界：`web_search_call.action.sources`、`code_interpreter_call.outputs`、`computer_call_output.output.image_url`、`message.input_image.image_url` 本地拒绝，`file_search_call.results` 保持允许。
- [x] 补齐 OpenAI 输出形态边界：Chat `n>1`、Chat `logprobs=true` / `top_logprobs>0`、Responses `include=message.output_text.logprobs` / `top_logprobs>0` 在 Anthropic bridge 下本地拒绝，不静默返回少候选或缺少 logprobs 的假成功。
- [x] 补齐输出模态和内容块边界：Chat audio output、Chat / Responses `input_audio` 和未知 content block 在 Anthropic bridge 下本地拒绝，不静默丢输入或返回纯文本假成功。
- [x] 补齐 Chat SSE usage 形态：默认流式 chunk 保持 `usage:null`，仅在 `stream_options.include_usage=true` 时追加 `choices=[]` 的 usage chunk。
- [x] 补齐 OpenAI 采样 / 预测输出控制边界：`temperature>1`、非默认 `presence_penalty` / `frequency_penalty`、非空 `logit_bias`、`seed`、`prediction`、`verbosity` 在 Anthropic bridge 下本地拒绝。
- [x] 补齐 OpenAI 请求级控制边界：`safety_identifier` 映射到 Anthropic metadata，`prompt_cache_key` 仅本地亲和；`service_tier=priority/flex`、`prompt_cache_retention`、Responses `prompt`、顶层 `moderation` 本地拒绝。
- [x] 撤销 Chat web_search / 搜索模型本地预取模拟，改为无上游原生等价能力时返回 guidance。
- [x] 实现 strict structured output 的合成工具路径。
- [x] 实现 JSON schema 本地二次校验和受控失败。
- [x] 实现 thinking 输入 / 输出安全映射。
- [x] 补齐 reasoning 请求侧枚举边界：`none` 不启用 thinking，`minimal` / `low` / `medium` / `high` 映射固定 budget，`xhigh` / 未知 effort 本地拒绝；`reasoning.summary=none` 抑制 reasoning item，未知 summary 本地拒绝。
- [x] 实现图片文件边界和 `file_id` 未配置受控失败。
- [x] 补齐图片 data URL 输入校验：仅允许 Anthropic 支持的图片 MIME 和合法 base64，非法输入本地拒绝且不请求上游。
- [x] 实现 Chat / Responses inline PDF / text 文件转 Anthropic document block，保留 `file_id` resolver 缺失的受控失败。
- [x] 补齐 compact / previous_response_id 专项 mock 回归。
- [x] 对齐 `/responses/compact` 官方外形、`compaction` item 输入恢复和 compact snapshot 跨 API Key 拒绝回归。
- [x] 补齐 Responses 状态 / 上下文控制边界：`conversation`、`background=true`、`truncation=auto`、`context_management` 在 Anthropic bridge 下本地拒绝，不静默忽略。
- [x] 补齐 Chat / Responses `store=true` 存储边界：在没有通用存储、distillation / evals、retrieve / TTL / 授权检索层前，本地拒绝，不返回 `store:false` 假成功。
- [x] 扩展真实账户 E2E，记录上游真实支持和不支持事实。
- [x] 跑完整类型检查、mock 回归、真实账户验证和凭据扫描。
- [x] 更新验证记录和进度记录。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --dir backend typecheck` | 后端 TypeScript 类型检查通过 | 已通过 | 已通过 |
| 命令类验证 | 前端类型检查 | `pnpm --dir frontend typecheck` | 前端类型检查不因共享类型变化回归 | 已通过 | 2026-06-24 复跑通过 |
| Mock 回归 | 基础四入口回归 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses JSON / SSE 保持通过 | 已通过 | 已通过 |
| Mock 回归 | 工具历史闭合 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses orphan 与 duplicate 工具结果返回本地 OpenAI 错误，且不命中 Anthropic | 已通过 | 2026-06-24 复跑通过，Chat / Responses orphan / duplicate 工具结果均本地 400 且不请求 Anthropic |
| Mock 回归 | Function tool_choice | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Responses `allowed_tools` function 子集只发送允许工具；reasoning + 强制工具调用本地 400 且不请求 Anthropic | 已通过 | 2026-06-24 复跑通过，Anthropic 上游只收到 allowed function tool；thinking + required tool_choice 本地拒绝且不上游 |
| Mock 回归 | Function 并行工具约束 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses `parallel_tool_calls=false` 映射为 Anthropic `tool_choice.disable_parallel_tool_use=true`；`tool_choice=none` 不附加该字段 | 已通过 | 2026-06-24 复跑通过，Chat auto 映射 `{type:auto, disable_parallel_tool_use:true}`，Responses required 映射 `{type:any, disable_parallel_tool_use:true}`，Chat none 保持 `{type:none}` |
| Mock 回归 | Function 工具调用次数边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Responses `max_tool_calls` 存在且非 `null` 时本地 400，且不请求 Anthropic | 已通过 | 2026-06-25 复跑通过，返回 `openai_anthropic_bridge_max_tool_calls_unsupported` 且不命中 Anthropic |
| Mock 回归 | Function arguments 保留 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses 历史 function call 的 JSON 非对象和非法 JSON arguments 不被吞成 `{}` | 已通过 | 2026-06-24 复跑通过，Chat JSON 数组 arguments 保留为 `openai_arguments`，Responses 非法 JSON arguments 保留为 `openai_arguments_text` |
| Mock 回归 | Tool use 流式反渲染 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Anthropic 多 tool_use / input_json_delta 转 Chat SSE 和 Responses SSE 时工具顺序、index 与 arguments 正确 | 已通过 | 2026-06-24 复跑通过；修复空 `input:{}` 与 `input_json_delta` 拼接成 `{}{...}` 的问题，Chat SSE 工具 index 按 0/1 连续递增 |
| Mock 回归 | Encrypted reasoning include | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Responses `include=reasoning.encrypted_content` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-24 复跑通过，返回 `openai_anthropic_bridge_encrypted_reasoning_unsupported` 且不命中 Anthropic |
| Mock 回归 | Encrypted reasoning input item | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Responses 历史 `type=reasoning` item 只带 `encrypted_content` 时本地 400，且不请求 Anthropic；`compaction` 路径不回归 | 已通过 | 2026-06-25 复跑通过，返回 `openai_anthropic_bridge_encrypted_reasoning_input_unsupported` 且不命中 Anthropic；既有 `compaction` / `compaction_summary` 恢复用例继续通过 |
| Mock 回归 | Reasoning 请求枚举边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `none` 不启用 Anthropic thinking，`xhigh` / 未知 effort 本地拒绝，`summary=none` 抑制 reasoning item，未知 summary 本地拒绝 | 已通过 | 2026-06-25 复跑通过，覆盖 Responses / Chat `none` 不发送 Anthropic `thinking`、Responses / Chat `xhigh` 本地 400、Responses `summary=none` JSON / SSE 抑制 reasoning item、未知 summary 本地 400 |
| Mock 回归 | Responses 非等价输出扩展 include | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `file_search_call.results` 保持允许；`web_search_call.action.sources`、`code_interpreter_call.outputs`、`computer_call_output.output.image_url`、`message.input_image.image_url` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-25 复跑通过，`file_search_call.results` 既有成功路径继续通过，四类非等价 include 返回 `openai_anthropic_bridge_include_unsupported` 且不命中 Anthropic |
| Mock 回归 | 输出形态边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat `n>1`、Chat `logprobs=true` / `top_logprobs>0`、Responses `include=message.output_text.logprobs` / `top_logprobs>0` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-24 复跑通过，返回 `openai_anthropic_bridge_multiple_choices_unsupported` / `openai_anthropic_bridge_logprobs_unsupported` 且不命中 Anthropic |
| Mock 回归 | 输出模态和内容块边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat audio output、Chat / Responses `input_audio` 和未知 content block 本地 400，且不请求 Anthropic | 已通过 | 2026-06-24 复跑通过，返回 `openai_anthropic_bridge_output_modality_unsupported` / `openai_anthropic_bridge_audio_input_unsupported` / `openai_anthropic_bridge_unsupported_content_part` 且不命中 Anthropic |
| Mock 回归 | Chat SSE usage 形态 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 默认 Chat SSE 不输出真实 usage；`stream_options.include_usage=true` 时在 `[DONE]` 前输出 `choices=[]` usage chunk | 已通过 | 2026-06-24 复跑通过，默认流式无真实 usage chunk；include_usage 流式输出 `prompt_tokens` / `completion_tokens` / `total_tokens` |
| Mock 回归 | 采样 / 预测输出控制边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `temperature>1`、非默认 penalty、非空 `logit_bias`、`seed`、`prediction`、`verbosity` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-25 复跑通过，返回 `openai_anthropic_bridge_sampling_control_unsupported` / `openai_anthropic_bridge_prediction_unsupported` / `openai_anthropic_bridge_verbosity_unsupported` 且不命中 Anthropic |
| Mock 回归 | 请求级控制边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `safety_identifier` 映射 Anthropic metadata，`prompt_cache_key` 不透传；`service_tier=priority/flex`、`prompt_cache_retention`、Responses `prompt`、顶层 `moderation` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-25 复跑通过，`safety_identifier` 映射 `metadata.user_id`，`prompt_cache_key` / `service_tier=default` 不透传；`priority` / `flex` 服务档位、`prompt_cache_retention`、Responses `prompt`、顶层 `moderation` 本地 400 且不上游 |
| Mock 回归 | Hosted tool 策略 | 扩展 bridge mock 脚本 | web_search / code_interpreter / computer / MCP / tool_search namespace / image_generation / file_search 等按矩阵映射、guidance 或受控失败 | 已通过 | 已覆盖 Responses / Chat `web_search` guidance 且不请求 Anthropic、Responses `computer` / `code_interpreter` / `mcp` guidance 且不请求 Anthropic、Chat `code_interpreter` guidance 且不请求 Anthropic、Responses `tool_search` + `namespace` JSON / SSE 本地展开、`image_generation` 无 provider guidance、provider JSON / completed-only SSE / partial SSE 成功路径、provider `moderation_blocked` failed response、edit / mask / 历史复用 guidance、provider 非 JSON / 超大响应体 / timeout failed response、`file_search` 本地运行时成功和边界失败 |
| Mock 回归 | Chat web_search 边界 | 扩展 bridge mock 脚本 | Chat JSON/SSE 不做本地执行器预取，`web_search` 返回 guidance，且不把 web_search 发送给 Anthropic | 已通过 | 已覆盖显式 Chat `web_search` guidance 和不命中上游 |
| Mock 回归 | Structured outputs | 扩展 bridge mock 脚本 | json_schema strict 成功时输出合法 JSON；失败时受控错误 | 已通过 | 已覆盖合成工具成功、本地 schema 二次校验、Chat refusal 和 Responses 503 错误码保留 |
| Mock 回归 | Thinking | 扩展 bridge mock 脚本 | thinking 不混入普通文本，Responses reasoning / usage 正确 | 已通过 | 已覆盖 JSON 和 SSE |
| Mock 回归 | 图片与文件 | 扩展 bridge mock 脚本 | 图片 URL / data URL、inline PDF / text 文件和 Responses PDF URL 成功，`file_id` 未配置返回 OpenAI 形态错误 | 已通过 | 已覆盖 Chat data URL、Responses URL、Chat `file_data` PDF、Responses `file_data` text、Responses PDF `file_url`、Responses `file_id` 受控失败 |
| Mock 回归 | 图片 data URL 校验 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses 图片 data URL 非图片 MIME 或非法 base64 本地 400，且不请求 Anthropic | 已通过 | 2026-06-24 复跑通过，非图片 MIME 返回 `openai_anthropic_bridge_unsupported_image_media_type`，非法 base64 返回 `openai_anthropic_bridge_invalid_image_base64` |
| Mock 回归 | Compact | 扩展 bridge mock 脚本 | `compaction` / `compaction_summary` 在 Anthropic bridge 下恢复为 system context；`/responses/compact` 返回 `response.compaction` 且不透传 Anthropic；跨 API Key snapshot 被拒绝 | 已通过 | 已覆盖官方 `compaction` item、历史 `compaction_summary` alias、compact endpoint 输出、恢复到 Anthropic system、`juhecmp.v2` 不上游透传和跨 API Key snapshot 拒绝 |
| Mock 回归 | Responses 状态 / 上下文控制边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `conversation`、`background=true`、`truncation=auto`、`context_management` 本地 400，且不请求 Anthropic；`background=false` 和 `truncation=disabled` 正常桥接 | 已通过 | 2026-06-25 复跑通过，覆盖 `background=false` / `truncation=disabled` 正常桥接，`conversation` / `background=true` / `truncation=auto` / `context_management` 本地 400 且不命中 Anthropic |
| Mock 回归 | Chat / Responses `store=true` 存储边界 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `store=false` 正常无状态桥接；Chat / Responses `store=true` 本地 400，且不请求 Anthropic | 已通过 | 2026-06-25 复跑通过，Responses `store=false` 不透传 Anthropic 且正常桥接，Chat / Responses `store=true` 返回 `openai_anthropic_bridge_store_unsupported` 且不命中上游 |
| 回归场景 | Anthropic native | `pnpm --dir backend test:anthropic-gateway-mock-ai` | 原生 `/v1/messages` 不受 bridge 策略影响 | 已通过 | 已通过 |
| 回归场景 | 既有 OpenAI-compatible bridge | `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai` | 既有 `responses -> chat_completions` 不回归 | 已通过 | 已通过 |
| 真实联调 | 真实账户高兼容 E2E | 临时环境变量运行真实脚本 | 真实支持项成功，真实不支持项按 OpenAI 形态 guidance 或错误返回 | 已通过 | 核心四入口、structured output、Chat / Responses text `file_data`、Responses thinking、Responses file_search 本地运行时和 Responses compact 通过；Chat web_search 当前真实脚本记录为本地 guidance；Chat image data URL 可选探针超时 |
| 安全检查 | 凭据扫描 | `rg` 固定 key 前缀 | 仓库无真实 key 命中 | 已通过 | 已通过 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 用户要求创建目标并继续做完整长期兼容；已先创建高兼容矩阵和 PLAN-0059，后续再进入实现。 |
| 2026-06-24 | 进行中 | AI | 已完成第一批增强：hosted tool 逐类 guidance / 本地承接、strict JSON schema 合成工具、thinking 输入 / 输出映射、图片 URL / data URL mock、`file_id` 受控失败和真实 E2E 探针。 |
| 2026-06-24 | 进行中 | AI | 已补 JSON schema 本地二次校验，覆盖 Chat / Responses JSON 和 SSE 的结构化输出失败路径；Chat JSON 失败按 `message.refusal` 返回，Responses JSON 失败会被 response-inspection 改写为 503 并保留 `openai_anthropic_bridge_structured_output_schema_mismatch`。 |
| 2026-06-24 | 进行中 | AI | 已补 Anthropic bridge compact 专项 mock：`compaction_summary` 的 `juhecmp.v1` envelope 会恢复为 Anthropic system 上下文，上游不接收 compact envelope。 |
| 2026-06-24 | 已撤销 | AI | 曾尝试 Responses web_search 本地预取模拟；当前策略已撤销该方案，桥接层不再读取本地 search executor 配置，不注入 Anthropic system，也不还原 `web_search_call` 与 `url_citation`，无真实上游能力时返回 guidance。 |
| 2026-06-24 | 进行中 | AI | 已确认文件输入下一批范围：OpenAI Chat `file.file_data`、Responses `input_file.file_data` 和 Responses PDF `file_url` 可映射到 Anthropic document block；OpenAI `file_id` 不能直接复用 Anthropic `file_id`，仍需要本地 Files resolver。 |
| 2026-06-24 | 进行中 | AI | 已完成 inline 文件输入子集：Chat `file_data` PDF、Responses `file_data` text 和 Responses PDF `file_url` 在 mock 中转为 Anthropic document block；真实 E2E 的 Chat / Responses text file_data 探针通过。 |
| 2026-06-24 | 已撤销 | AI | Chat Completions web search 是专用搜索模型或上游原生工具语义；本计划不再用本地预取模拟该能力，Chat 协议不伪造 annotations。 |
| 2026-06-24 | 已撤销 | AI | 已撤销 Chat web_search 本地预取模拟；显式 Chat `web_search` 当前应返回本地 guidance，真实 E2E 验证该引导路径。 |
| 2026-06-24 | 进行中 | AI | 已将 `file_id` resolver、OpenAI 兼容 Files、Vector Store 和 `file_search` 本地运行时拆分到 `PLAN-0060`，避免把新一阶段长期运行时塞进已完成的高兼容首批增强。后续 `PLAN-0060` 已完成首批本地运行时和回归验证，`PLAN-0059` 的剩余缺口不再包含 `file_id` / `file_search` 首批闭环。 |
| 2026-06-24 | 进行中 | AI | 官方文档确认 `/responses/compact` 返回 CompactResource，典型外形为 `object=response.compaction` 且 `output` 内包含 `type=compaction` item；本轮将网关 summary compact 从历史 `compaction_summary` 输出对齐到官方外形，同时继续兼容历史别名输入。 |
| 2026-06-24 | 进行中 | AI | 已完成 `/responses/compact` 官方外形落地：网关返回 `response.compaction` 和 `compaction` item，后续 `/responses` 输入可恢复 snapshot 到 Anthropic system；mock 已覆盖同 API Key 恢复、跨 API Key 拒绝和不把 compact envelope 透传上游。 |
| 2026-06-24 | 进行中 | AI | 已补 hosted tools guidance mock：Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter` 在无 adapter 时返回 OpenAI 形态 guidance，且不请求 Anthropic 上游。 |
| 2026-06-24 | 进行中 | AI | 已补 `image_generation` provider partial SSE：请求带 `partial_images` 时，provider OpenAI Images SSE partial 会转为 Responses `response.image_generation_call.partial_image`；mock 已通过，真实 provider 仍需可用 Images API key。 |
| 2026-06-24 | 进行中 | AI | 已将 MCP / computer / code execution / Codex 本地工具真实运行时拆分到 `PLAN-0063`，当前仍保持无 adapter guidance；后续必须先完成 Runtime Registry、沙箱 / allowlist / approval / 审计和 mock 回归。 |
| 2026-06-24 | 进行中 | AI | PLAN-0063 已落地 Runtime Registry 首批骨架：`code_interpreter`、`computer`、`mcp`、`shell`、`skills`、`tool_search` 支持 `guidance` / `reject` 两种保守模式，mock 已覆盖 `code_interpreter=reject` 不上游。 |
| 2026-06-24 | 进行中 | AI | 已补工具历史闭合策略：Chat `role=tool` 和 Responses `function_call_output` 如果找不到前文 tool call / function_call，或同一调用结果重复提交，本地返回 OpenAI 形态错误并阻止请求 Anthropic。 |
| 2026-06-24 | 进行中 | AI | 已补 function `tool_choice` 边界：Responses `allowed_tools` function 子集会过滤 Anthropic tools，`mode=required` 映射为 `any`；Anthropic thinking 与 `tool_choice=any/tool` 冲突时本地拒绝。 |
| 2026-06-24 | 进行中 | AI | 已明确 `parallel_tool_calls=false` 映射策略：有工具且允许工具调用时设置 Anthropic `disable_parallel_tool_use=true`，`tool_choice=none` 不加该字段。 |
| 2026-06-24 | 进行中 | AI | 已补 `parallel_tool_calls=false` 映射和 mock：Chat auto、Responses required 会附加 `disable_parallel_tool_use=true`；Chat `tool_choice=none` 不附加该字段，避免 Anthropic 非法请求。 |
| 2026-06-25 | 进行中 | AI | 已明确 Responses `max_tool_calls` 边界：Anthropic Messages 没有等价工具调用次数上限，忽略该字段可能让模型调用超过客户端限制的工具，必须本地拒绝。 |
| 2026-06-25 | 进行中 | AI | 已补 Responses `max_tool_calls` 前置拒绝和 mock：字段存在且非 `null` 时返回 `openai_anthropic_bridge_max_tool_calls_unsupported`，不请求 Anthropic。 |
| 2026-06-25 | 进行中 | AI | 已明确 Responses 状态 / 上下文控制边界：`conversation` 需要状态层恢复并写回；`background=true` 需要异步任务语义；`truncation=auto` 和 `context_management` 需要上下文管理策略；当前 Anthropic bridge 不能静默忽略。 |
| 2026-06-25 | 进行中 | AI | 已补 Responses 状态 / 上下文控制边界实现和 mock：`conversation`、`background=true`、`truncation=auto`、`context_management` 返回稳定错误码且不上游；`background=false` / `truncation=disabled` 正常桥接。 |
| 2026-06-25 | 进行中 | AI | 已明确 Chat / Responses `store=true` 边界：Chat 代表存储输出用于 OpenAI 后续产品，Responses 代表可后续 API retrieve / 状态复用；Anthropic bridge 当前没有等价存储和授权检索层，不能返回 `store:false` 假成功。 |
| 2026-06-25 | 进行中 | AI | 已补 Chat / Responses `store=true` 本地拒绝和 mock：Responses `store=false` 正常无状态桥接且不透传 Anthropic；Chat / Responses `store=true` 返回稳定错误码且不上游。 |
| 2026-06-24 | 进行中 | AI | 已补历史 function call arguments 保留：JSON 非对象包入 `openai_arguments`，非法 JSON 包入 `openai_arguments_text`，避免桥接到 Anthropic 时静默丢历史。 |
| 2026-06-24 | 进行中 | AI | 已补 tool_use 流式反渲染：Chat SSE 使用独立 tool ordinal 作为 `tool_calls[].index`；空 `input:{}` 不再与后续 `input_json_delta` 拼接成非法 arguments；mock 覆盖 Chat / Responses 多工具参数分片。 |
| 2026-06-24 | 进行中 | AI | 已补 Responses `include=reasoning.encrypted_content` 本地拒绝：Anthropic Messages 不能生成或验证 OpenAI encrypted reasoning，当前返回稳定 OpenAI 错误且不请求上游。 |
| 2026-06-25 | 进行中 | AI | 已明确 Responses 非等价输出扩展 include 边界：除已承接的 `file_search_call.results` 外，web search sources、code interpreter outputs、computer output image URL 和 input image URL 回显都不能在当前 Anthropic bridge 中静默忽略。 |
| 2026-06-25 | 进行中 | AI | 已补 Responses 非等价输出扩展 include 本地拒绝和 mock：`web_search_call.action.sources`、`code_interpreter_call.outputs`、`computer_call_output.output.image_url`、`message.input_image.image_url` 返回统一错误码且不上游；`file_search_call.results` 成功路径保持通过。 |
| 2026-06-24 | 进行中 | AI | 已补图片 data URL 校验：Chat / Responses 仅接受 Anthropic 支持的 JPEG / PNG / GIF / WEBP 和合法 base64；非图片 MIME 或非法 base64 本地拒绝且不请求上游。 |
| 2026-06-24 | 进行中 | AI | 已明确下一批输出形态边界：Chat `n>1`、Chat token logprobs 和 Responses output_text logprobs 在 Anthropic Messages 下没有等价返回结构，必须本地拒绝而不是静默降级；`top_logprobs=0` 视为未请求。 |
| 2026-06-24 | 进行中 | AI | 已补输出形态边界实现和 mock：Chat `n>1` 本地返回 `openai_anthropic_bridge_multiple_choices_unsupported`；Chat / Responses logprobs 请求本地返回 `openai_anthropic_bridge_logprobs_unsupported`；这些错误均不上游。 |
| 2026-06-24 | 进行中 | AI | 已明确输出模态和内容块边界：Anthropic Messages 不能等价返回 OpenAI Chat audio output，也不能消费 OpenAI `input_audio`；未知 content block 不能静默丢弃，必须先分类再支持。 |
| 2026-06-24 | 进行中 | AI | 已补输出模态和内容块边界实现及 mock：Chat audio output、Chat / Responses `input_audio` 和未知 content block 均本地 400 且不上游。 |
| 2026-06-24 | 进行中 | AI | 已确认 Chat SSE usage 形态需要请求感知：默认流式不应输出真实 usage，只有 `stream_options.include_usage=true` 才追加 `choices=[]` usage chunk。 |
| 2026-06-24 | 进行中 | AI | 已补 Chat SSE usage 请求感知：request plan 记录 `stream_options.include_usage`，默认终止 chunk `usage:null`，include_usage 时在 `[DONE]` 前追加 `choices=[]` usage chunk。 |
| 2026-06-25 | 进行中 | AI | 已明确 OpenAI 采样 / 预测输出控制边界：Anthropic Messages 没有 `presence_penalty`、`frequency_penalty`、`logit_bias`、`seed`、Predicted Outputs 和 verbosity 的等价语义，不能静默忽略。 |
| 2026-06-25 | 进行中 | AI | 已补采样 / 预测输出控制前置校验和 mock：`temperature>1`、非默认 penalty、非空 `logit_bias`、`seed`、Chat / Responses `prediction`、Chat / Responses verbosity 均本地 400 且不上游。 |
| 2026-06-25 | 进行中 | AI | 已明确 OpenAI 请求级控制边界：`safety_identifier` 可承接为 Anthropic metadata；`prompt_cache_key` 仅作为本地亲和 / 审计信号；`service_tier=priority/flex`、`prompt_cache_retention`、Responses `prompt` 和顶层 `moderation` 不能静默忽略。 |
| 2026-06-25 | 进行中 | AI | 已补 OpenAI 请求级控制实现和 mock：`safety_identifier` 优先映射 Anthropic `metadata.user_id`，`prompt_cache_key` 不透传；非默认服务档位、prompt cache retention、Responses prompt template 和顶层 moderation 返回稳定错误码且不上游。 |
| 2026-06-25 | 进行中 | AI | 已明确 reasoning 请求侧边界：未知 effort 不能静默忽略；`xhigh` 在 Anthropic profile 没有明确大预算支持前本地拒绝；`summary=none` 需要真实抑制 reasoning item，未知 summary 本地拒绝。 |
| 2026-06-25 | 进行中 | AI | 已补 reasoning 请求枚举边界实现和 mock：`none` 不启用 Anthropic thinking，`minimal` / `low` / `medium` / `high` 继续映射 budget，`xhigh` / 未知 effort 本地拒绝；`summary=none` 抑制 Responses reasoning item 但保留 reasoning token usage。 |
| 2026-06-25 | 进行中 | AI | 已明确 encrypted reasoning input item 边界：`type=reasoning` 的 OpenAI `encrypted_content` 不是网关 compact envelope，Anthropic bridge 不能验证或恢复，不能静默丢弃后继续请求上游。 |
| 2026-06-25 | 进行中 | AI | 已补 encrypted reasoning input item 本地拒绝和 mock：Responses 历史 `type=reasoning` item 携带 `encrypted_content` 时返回 `openai_anthropic_bridge_encrypted_reasoning_input_unsupported`，且不上游；compact 恢复路径不受影响。 |
| 2026-06-25 | 进行中 | AI | 已补 Responses `tool_search` + `namespace` function 本地展开：请求内 namespace function 展开为 Anthropic client tool，强制 `tool_choice` 映射展开名，Responses JSON / SSE 回包恢复 `function_call.namespace`；不伪造 hosted `tool_search_call` / `tool_search_output`。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | 用能力等级替代笼统“完全兼容” | OpenAI 和 Anthropic 高级能力不是字段同构 | 后续每个工具和字段必须显式分类、测试和审计 |
| 2026-06-24 | 默认不静默删除 unsupported hosted tools | 静默删除会让客户端误判工具可用，产生错误结果 | 无适配器时返回 OpenAI 形态 guidance；只有显式降级配置才允许 L5 |
| 2026-06-24 | strict structured output 优先走合成工具路径 | 不依赖 Anthropic structured output beta，便于统一校验 | 需要在桥接层维护合成 tool 和输出反渲染逻辑 |
| 2026-06-24 | thinking 只输出安全 summary / reasoning item | 防止把 hidden thinking 混入普通文本 | Chat 默认不暴露 thinking；Responses 按可消费 contract 渲染 |
| 2026-06-24 | image_generation 首批改由本地 provider 承接 generate 路径 | PLAN-0061 已补 runtime 配置、OpenAI Images JSON executor、Responses JSON / SSE 渲染和 mock provider 成功回归 | 无 provider 时返回 guidance 且不命中 Anthropic；配置 provider 后 Anthropic 只生成 revised prompt，图像结果由本地 provider 返回 |
| 2026-06-24 | image_generation provider 错误与不支持编辑路径补齐 mock | provider `moderation_blocked` 保留 OpenAI 风格错误码、类型和审核详情；`action=edit` / `input_image_mask` 在首批不支持时返回 guidance | 本地生成的图像 provider failed response 不再被响应检查改写为 503；真实 provider 联调仍在 PLAN-0061 后续项 |
| 2026-06-24 | image_generation provider 边界错误继续补齐 mock | 历史 `image_generation_call` 复用返回 guidance；provider 非 JSON、超大响应体、请求超时返回 OpenAI Responses failed object；provider partial image SSE 已由 mock 覆盖 | 图像 provider 首批 generate 路径的低级错误和 partial streaming 已由 mock 截住；真实 provider 联调仍在 PLAN-0061 后续项 |
| 2026-06-24 | 图像响应审计正文省略补齐 | 流式 `partial_image_b64` 和非流式 `image_generation_call.result` 均不写入审计 payload body | 客户端仍收到完整响应；审计保留 omission 元数据，避免保存图片 base64 |
| 2026-06-24 | Chat strict schema 失败使用 `message.refusal` | Chat Completions JSON 没有 Responses `status=failed` 结构，顶层 `{error}` 会被现有 response-inspection 当作协议错误覆盖 | 客户端拿到合法 Chat Completion，`content=null`，`refusal` 中带 bridge schema mismatch code |
| 2026-06-24 | Anthropic bridge 不直接承接 `/responses/compact` | Anthropic Messages 没有 OpenAI Responses compact endpoint；直接转发会伪造上游能力 | 只恢复 `compaction_summary` 输入；真正 compact 继续走网关托管 summary compact |
| 2026-06-24 | 网关托管 compact 输出采用官方 `compaction` item | OpenAI CompactResource 的长期外形是 `response.compaction` + `compaction` output item；继续输出历史别名会让更严格的 Responses 客户端误判 | `/responses/compact` 输出改为 `compaction`；输入侧继续接受 `compaction` 和 `compaction_summary`，Anthropic 上游只接收恢复后的 system summary |
| 2026-06-24 | 撤销 Responses web_search 本地预取模拟 | 本地预取会把“普通搜索结果注入”伪装成 OpenAI hosted tool 语义，容易让 OpenAI-compatible 客户端误判上游能力 | 无上游原生等价能力时返回 guidance；不输出 `web_search_call` 和 citation |
| 2026-06-24 | 文件输入先做 inline / URL 子集，`file_id` resolver 拆入 PLAN-0060 | OpenAI `file_id` 是 OpenAI 文件存储引用，不能直接发给 Anthropic；inline PDF / text 与 PDF URL 两边协议都有 document 表达；本地 Files / Vector Store 是独立运行时 | PLAN-0060 已落地本地 Files resolver；客户端传 inline 文件、PDF URL 或本地 `/v1/files` 上传后的 `file_id` 均可走当前桥接路径，未知或跨边界 `file_id` 仍本地失败 |
| 2026-06-24 | Chat web_search 不做本地预取 | Chat Completions 搜索是专用搜索模型或上游原生工具语义，不能靠 Anthropic system 注入无损模拟 | Chat / Responses 命中 `web_search` 均返回 guidance，除非后续 driver 接入上游原生等价能力 |
| 2026-06-25 | reasoning 请求枚举不能静默降级 | OpenAI 客户端会依据 `effort` / `summary` 判断推理预算和输出形态；未知值若被忽略，会造成“请求成功但语义未生效”的假成功 | 只接受当前能稳定承接的 `none` / `minimal` / `low` / `medium` / `high` 和 `summary` 枚举；`xhigh` 与未知枚举在没有 profile 能力前本地拒绝 |
| 2026-06-25 | Responses 非等价输出扩展 include 先本地拒绝 | 官方 include 表示客户端要求响应体带额外输出数据；当前 bridge 若忽略会产生“请求成功但缺字段”的假成功 | 只允许 `file_search_call.results` 这类已由本地运行时填充的 include；logprobs / encrypted reasoning 走专用错误码，其余非等价 include 统一本地拒绝 |
| 2026-06-25 | 请求级控制字段按语义分层 | 部分字段只影响本地亲和或安全标识，部分字段会改变上游服务档位、缓存保留、prompt 模板或审核策略；全部忽略会让客户端误判 | `safety_identifier` 映射 metadata，`prompt_cache_key` 本地使用；无法等价承接的 `service_tier=priority/flex`、`prompt_cache_retention`、Responses `prompt`、顶层 `moderation` 本地拒绝 |
| 2026-06-25 | Responses `max_tool_calls` 先本地拒绝 | Anthropic Messages 只能约束是否并行工具调用，不能按 OpenAI Responses 的调用次数预算裁剪整个工具循环 | 不做静默忽略；后续如要支持，需要在网关工具循环层实现可审计的调用计数和终止策略 |
| 2026-06-25 | Responses 状态 / 上下文控制字段先本地拒绝 | `conversation`、`background=true`、`truncation=auto`、`context_management` 都会改变执行模式、历史恢复或上下文裁剪；Anthropic Messages 直转没有等价协议 | 只允许省略、`background=false`、`truncation=disabled` 这类等价同步路径；其余进入本地 OpenAI 错误，等待状态层或上下文管理实现 |
| 2026-06-25 | Chat / Responses `store=true` 先本地拒绝 | Chat `store=true` 代表输出会进入 OpenAI distillation / evals 等后续产品，Responses `store=true` 代表生成响应需要后续 API retrieve / 状态复用；当前 Anthropic bridge 返回 `store:false` 会让客户端误判请求已按存储语义成功 | 只允许省略、`null` 或 `false` 的无状态路径；后续如要支持，需要先实现通用存储策略、TTL、隐私、授权边界和 retrieve API |

## 验收标准

- [x] 高兼容能力矩阵和计划文档已同步到索引。
- [x] 所有非 function hosted tools 都有明确策略：映射、模拟、降级、guidance 或受控拒绝。
- [x] Chat web_search / 搜索模型路径不再配置本地执行器；当前无上游原生等价能力时返回 guidance。
- [x] JSON schema strict 不再只靠提示词；已通过合成工具强制输出，并补本地 schema 二次校验。
- [x] Reasoning / thinking 不泄露隐藏思考，且 usage / Responses item 有明确映射。
- [x] 图片 URL / data URL 继续成功；Chat / Responses inline PDF / text 文件和 Responses PDF URL 成功；`file_id` 未配置时返回稳定 OpenAI 错误。
- [x] Compact / previous_response_id 在 Anthropic bridge 下有专项 mock 覆盖。
- [x] Anthropic native 和既有 OpenAI-compatible bridge 不回归。
- [x] 真实账户验证完成，凭据不落盘且扫描无命中。

## 验证记录

- 类型检查：2026-06-25 已复跑并通过 `pnpm --dir backend typecheck`；2026-06-25 已复跑并通过 `pnpm --dir frontend typecheck`。
- Mock 回归：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖四入口、Chat `n>1` 本地拒绝、Chat `logprobs=true` / `top_logprobs>0` 本地拒绝、Responses `include=message.output_text.logprobs` / `top_logprobs>0` 本地拒绝、Responses 非等价输出扩展 include 本地拒绝、Chat audio output 本地拒绝、Chat / Responses `input_audio` 本地拒绝、Chat / Responses 未知 content block 本地拒绝、Chat SSE 默认无真实 usage chunk、Chat SSE `stream_options.include_usage` 输出 `choices=[]` usage chunk、OpenAI 采样 / 预测输出控制本地拒绝、OpenAI 请求级控制边界、function tools、Responses `allowed_tools` function 子集、`parallel_tool_calls=false` 映射、Responses `max_tool_calls` 本地拒绝、Responses 状态 / 上下文控制边界本地拒绝、Chat / Responses `store=true` 本地拒绝、非对象 / 非法 JSON arguments 保留、Anthropic 多 tool_use / `input_json_delta` 到 Chat SSE / Responses SSE 的反渲染、thinking + 强制 tool_choice 本地拒绝、Responses / Chat `reasoning.effort=none` 不启用 Anthropic thinking、Responses / Chat `xhigh` 本地拒绝、Responses `reasoning.summary=none` JSON / SSE 抑制 reasoning item、未知 summary 本地拒绝、Responses `include=reasoning.encrypted_content` 本地拒绝、Responses 历史 reasoning item `encrypted_content` 本地拒绝、Chat / Responses orphan / duplicate tool result 本地拒绝、图片 URL / data URL、图片 data URL MIME / base64 拒绝边界、Chat `file_data` PDF、Responses `file_data` text、Responses PDF `file_url`、strict JSON schema 合成工具、本地 schema mismatch 失败、thinking JSON / SSE、Responses / Chat `web_search` guidance 且不命中上游、Responses `computer` / `code_interpreter` / `mcp` guidance 且不命中上游、Chat `code_interpreter` guidance 且不命中上游、`image_generation` 无 provider guidance、`image_generation` provider JSON / completed-only SSE / partial SSE 成功路径、provider `moderation_blocked` failed response、edit / mask / 历史复用 guidance、provider 非 JSON / 超大响应体 / timeout failed response、`file_id` 受控失败、`compaction` / `compaction_summary` 恢复、`/responses/compact` 官方外形、跨 API Key snapshot 拒绝和 Codex `previous_response_id`。
- Tool search namespace 专项：2026-06-25 mock 覆盖 Responses `tool_search` + `namespace` function JSON / SSE 本地展开，确认 Anthropic 上游只收到展开后的 `namespace__function` client tool，Responses 回包恢复 `function_call.name` 和 `function_call.namespace`，且不伪造 hosted `tool_search_call` / `tool_search_output`。
- 审计回归：2026-06-24 已复跑并通过 `pnpm --dir backend test:gateway-audit-payload-storage`，覆盖流式图像 `partial_image_b64` 和非流式图像 `image_generation_call.result` 审计正文省略。
- 回归验证：2026-06-24 已复跑并通过 `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai`；先前已通过 `pnpm --dir backend test:anthropic-gateway-mock-ai`、`pnpm --dir backend test:codex-client-strategy`、`pnpm --dir backend test:hybrid-gateway-mock-ai`。其中 DeepSeek / GLM 回归覆盖 Chat-only bridge `web_search` guidance，确认不再进入网关工具循环。
- 真实联调：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-real`；真实上游 `https://vsllm.com`、模型 `claude-sonnet-4-6`、源模型 `gpt-5.5`。结果：核心四入口、`file_search` 本地受控错误、Chat web_search guidance、Chat structured output、Chat image data URL、Chat `file_data` text、Responses `file_data` text、Responses thinking、Responses file_search 本地运行时、Responses `tool_search` + `namespace` 本地展开和 Responses compact 通过。真实联调不测外部 PDF URL，避免把上游外网下载稳定性并入协议回归。
- 凭据检查：2026-06-25 已用固定 key 前缀扫描 `backend`、`frontend`、`docs`、`package.json`、`pnpm-lock.yaml`，无命中；`git diff --check` 只有 CRLF 提示，无 whitespace error。
- 未验证项：image_generation provider 真实 Images API E2E 仍需要可用的 OpenAI Images API 兼容 endpoint / key；2026-06-25 真实账户调用 `/v1/images/generations` 返回 401 `invalid_api_key`，错误来自上游代理到官方 OpenAI 的 `sk-proj-*` key，因此 Images provider 真实 partial streaming 仍未验证。同一真实账户调用 `/v1/responses` 时，`gpt-image-2-chat` 和 `gpt-5.5` 均可返回真实 `image_generation_call.result`；PLAN-0061 已实现 `JUHE_AI_IMAGE_GENERATION_PROVIDER_API=responses`，并用真实网关 E2E 跑通 JSON 与 SSE 图像 provider 路径。MCP / computer / code execution 真实运行时仍未实现，其无 adapter guidance 已由 mock 覆盖；`/responses/compact` endpoint 本身不走 Anthropic Messages 直转，而是网关托管 compact 状态层；Chat 搜索模型路径已由 mock 覆盖，真实联调当前未配置本地 web search executor。

## 风险与注意事项

- Anthropic server tools、computer use、code execution、MCP connector 可能需要 beta header、模型支持或平台权限；真实账号不支持时只能记录为上游能力事实，不能写死为协议规则。
- OpenAI image generation 不是 Anthropic Messages 原生能力；没有本地图像 provider 时必须失败，不能让模型用文字假装生成了图片。
- Structured output strict 一旦启用校验，可能导致模型原本能回答的请求变成失败；必须给错误码和审计 metadata。
- Thinking 处理有安全边界，不能为追求“无感知”把隐藏推理全文发给普通 OpenAI 客户端。
- 发布异常处理：如高兼容策略引发异常，先关闭对应模型映射或 hosted tool adapter；基础四入口 bridge 和 Anthropic native 路径应保持可用。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
