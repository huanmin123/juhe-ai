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

- [x] 新增长期能力矩阵，定义原生映射、上游能力适配、本地模拟、受控拒绝和显式降级等级。
- [x] 新增桥接能力策略层，统一分类 OpenAI 字段、工具、thinking、图片和 structured output。
- [x] 将非 function hosted tools 从“泛化不支持”改成逐类策略：web_search、file_search、image_generation、code_interpreter、computer、MCP、Codex tool_search / shell / skills；Responses web_search 已在配置本地 HTTP search executor 时支持预取模拟。
- [x] 补齐 Chat web_search / 搜索模型路径：配置本地 HTTP search executor 时做一次本地预取，Chat JSON 返回 `message.annotations`，Chat SSE 保持标准 chunk 和正文引用标记。
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
- Responses 压缩：`docs/functions/Responses上下文压缩落地方案.md`
- 请求处理分层：`docs/functions/请求处理分层设计.md`
- 模型映射：`docs/functions/自定义模型与模型映射设计.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：先分类再转换。每个 OpenAI 字段和工具必须落到 `native_map`、`upstream_feature`、`local_emulation`、`best_effort_degrade` 或 `reject` 之一。
- 数据变化：本阶段优先不新增数据库 schema；策略决策先写入网关审计 metadata。如后续需要按能力筛选统计，再新增显式字段和索引。
- 接口变化：OpenAI 下游接口不新增客户端必填字段；错误仍按 Chat / Responses 形态渲染。
- 前端变化：本阶段默认不改前端页面；如后续要展示账号级 bridge capability，再单独补配置 UI。
- 后端变化：增强 `openai-anthropic-bridge`，补能力分类、结构化输出、thinking、文件边界、Chat / Responses web_search 本地预取、compact 专项状态和测试。
- 数据处理策略：不做旧结构兼容；真实凭据只走临时环境变量。

## 执行拆解

- [x] 新增 `docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`。
- [x] 创建本计划并纳入计划索引。
- [x] 更新基础桥接设计文档，明确 PLAN-0058 的“不做范围”已由本计划接续。
- [x] 梳理当前 bridge 代码缺口并标注第一批实现点。
- [x] 实现桥接能力策略层。
- [x] 实现 hosted tool 分类与 OpenAI 形态错误。
- [x] 实现 Chat web_search / 搜索模型本地预取模拟。
- [x] 实现 strict structured output 的合成工具路径。
- [x] 实现 JSON schema 本地二次校验和受控失败。
- [x] 实现 thinking 输入 / 输出安全映射。
- [x] 实现图片文件边界和 `file_id` 未配置受控失败。
- [x] 实现 Chat / Responses inline PDF / text 文件转 Anthropic document block，保留 `file_id` resolver 缺失的受控失败。
- [x] 补齐 compact / previous_response_id 专项 mock 回归。
- [x] 对齐 `/responses/compact` 官方外形、`compaction` item 输入恢复和 compact snapshot 跨 API Key 拒绝回归。
- [x] 扩展真实账户 E2E，记录上游真实支持和不支持事实。
- [x] 跑完整类型检查、mock 回归、真实账户验证和凭据扫描。
- [x] 更新验证记录和进度记录。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --dir backend typecheck` | 后端 TypeScript 类型检查通过 | 已通过 | 已通过 |
| 命令类验证 | 前端类型检查 | `pnpm --dir frontend typecheck` | 前端类型检查不因共享类型变化回归 | 未通过 | 失败在 `frontend/src/scripts/regression/account-import-protocol-regression.ts` 的 Node 类型声明 / `node:*` 模块解析，属既有前端回归脚本环境问题；本轮后端 bridge 未改前端类型 |
| Mock 回归 | 基础四入口回归 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Chat / Responses JSON / SSE 保持通过 | 已通过 | 已通过 |
| Mock 回归 | Hosted tool 策略 | 扩展 bridge mock 脚本 | web_search / image_generation / file_search 等按矩阵映射或受控失败 | 已通过 | 已覆盖 Responses web_search 本地预取 JSON/SSE、Chat web_search JSON/SSE、Chat 搜索模型路径、`file_search` 本地运行时成功和边界失败；其他 hosted tools 共享分类函数 |
| Mock 回归 | Chat web_search 本地预取 | 扩展 bridge mock 脚本 | Chat JSON/SSE 配置本地执行器后成功，未配置执行器受控失败，且不把 web_search 发送给 Anthropic | 已通过 | 已覆盖显式 Chat `web_search` JSON、`gpt-5-search-api` 搜索模型 SSE、执行器缺失受控失败 |
| Mock 回归 | Structured outputs | 扩展 bridge mock 脚本 | json_schema strict 成功时输出合法 JSON；失败时受控错误 | 已通过 | 已覆盖合成工具成功、本地 schema 二次校验、Chat refusal 和 Responses 503 错误码保留 |
| Mock 回归 | Thinking | 扩展 bridge mock 脚本 | thinking 不混入普通文本，Responses reasoning / usage 正确 | 已通过 | 已覆盖 JSON 和 SSE |
| Mock 回归 | 图片与文件 | 扩展 bridge mock 脚本 | 图片 URL / data URL、inline PDF / text 文件和 Responses PDF URL 成功，`file_id` 未配置返回 OpenAI 形态错误 | 已通过 | 已覆盖 Chat data URL、Responses URL、Chat `file_data` PDF、Responses `file_data` text、Responses PDF `file_url`、Responses `file_id` 受控失败 |
| Mock 回归 | Compact | 扩展 bridge mock 脚本 | `compaction` / `compaction_summary` 在 Anthropic bridge 下恢复为 system context；`/responses/compact` 返回 `response.compaction` 且不透传 Anthropic；跨 API Key snapshot 被拒绝 | 已通过 | 已覆盖官方 `compaction` item、历史 `compaction_summary` alias、compact endpoint 输出、恢复到 Anthropic system、`juhecmp.v2` 不上游透传和跨 API Key snapshot 拒绝 |
| 回归场景 | Anthropic native | `pnpm --dir backend test:anthropic-gateway-mock-ai` | 原生 `/v1/messages` 不受 bridge 策略影响 | 已通过 | 已通过 |
| 回归场景 | 既有 OpenAI-compatible bridge | `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai` | 既有 `responses -> chat_completions` 不回归 | 已通过 | 已通过 |
| 真实联调 | 真实账户高兼容 E2E | 临时环境变量运行真实脚本 | 真实支持项成功，真实不支持项按 OpenAI 形态错误返回 | 已通过 | 核心四入口、structured output、Chat / Responses text `file_data`、Responses thinking、Responses file_search 本地运行时和 Responses compact 通过；Chat web_search 当前真实脚本记录为未配置执行器下的受控 unsupported；Chat image data URL 可选探针超时 |
| 安全检查 | 凭据扫描 | `rg` 固定 key 前缀 | 仓库无真实 key 命中 | 已通过 | 已通过 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 用户要求创建目标并继续做完整长期兼容；已先创建高兼容矩阵和 PLAN-0059，后续再进入实现。 |
| 2026-06-24 | 进行中 | AI | 已完成第一批增强：hosted tool 逐类受控错误、strict JSON schema 合成工具、thinking 输入 / 输出映射、图片 URL / data URL mock、`file_id` 受控失败和真实 E2E 探针。 |
| 2026-06-24 | 进行中 | AI | 已补 JSON schema 本地二次校验，覆盖 Chat / Responses JSON 和 SSE 的结构化输出失败路径；Chat JSON 失败按 `message.refusal` 返回，Responses JSON 失败会被 response-inspection 改写为 503 并保留 `openai_anthropic_bridge_structured_output_schema_mismatch`。 |
| 2026-06-24 | 进行中 | AI | 已补 Anthropic bridge compact 专项 mock：`compaction_summary` 的 `juhecmp.v1` envelope 会恢复为 Anthropic system 上下文，上游不接收 compact envelope。 |
| 2026-06-24 | 进行中 | AI | 已补 Responses web_search 本地预取模拟：配置 `JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT` 后，桥接层先调用本地 HTTP search executor，再注入 Anthropic system，并在 Responses JSON/SSE 中还原 `web_search_call` 与 `url_citation`。 |
| 2026-06-24 | 进行中 | AI | 已确认文件输入下一批范围：OpenAI Chat `file.file_data`、Responses `input_file.file_data` 和 Responses PDF `file_url` 可映射到 Anthropic document block；OpenAI `file_id` 不能直接复用 Anthropic `file_id`，仍需要本地 Files resolver。 |
| 2026-06-24 | 进行中 | AI | 已完成 inline 文件输入子集：Chat `file_data` PDF、Responses `file_data` text 和 Responses PDF `file_url` 在 mock 中转为 Anthropic document block；真实 E2E 的 Chat / Responses text file_data 探针通过。 |
| 2026-06-24 | 进行中 | AI | 官方文档确认 Chat Completions web search 是专用搜索模型路径，搜索模型会先检索再答复；本计划新增 Chat web_search 本地预取模拟，按 Chat 协议返回文本和 annotations，不输出 Responses hosted item。 |
| 2026-06-24 | 进行中 | AI | 已完成 Chat web_search 本地预取模拟：显式 Chat `web_search` 和 `gpt-5-search-api` 搜索模型路径在 mock 中通过；真实 E2E 的 Chat web_search 本地预取探针通过。 |
| 2026-06-24 | 进行中 | AI | 已将 `file_id` resolver、OpenAI 兼容 Files、Vector Store 和 `file_search` 本地运行时拆分到 `PLAN-0060`，避免把新一阶段长期运行时塞进已完成的高兼容首批增强。后续 `PLAN-0060` 已完成首批本地运行时和回归验证，`PLAN-0059` 的剩余缺口不再包含 `file_id` / `file_search` 首批闭环。 |
| 2026-06-24 | 进行中 | AI | 官方文档确认 `/responses/compact` 返回 CompactResource，典型外形为 `object=response.compaction` 且 `output` 内包含 `type=compaction` item；本轮将网关 summary compact 从历史 `compaction_summary` 输出对齐到官方外形，同时继续兼容历史别名输入。 |
| 2026-06-24 | 进行中 | AI | 已完成 `/responses/compact` 官方外形落地：网关返回 `response.compaction` 和 `compaction` item，后续 `/responses` 输入可恢复 snapshot 到 Anthropic system；mock 已覆盖同 API Key 恢复、跨 API Key 拒绝和不把 compact envelope 透传上游。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | 用能力等级替代笼统“完全兼容” | OpenAI 和 Anthropic 高级能力不是字段同构 | 后续每个工具和字段必须显式分类、测试和审计 |
| 2026-06-24 | 默认不静默删除 unsupported hosted tools | 静默删除会让客户端误判工具可用，产生错误结果 | 无适配器时返回 OpenAI 形态错误；只有显式降级配置才允许 L5 |
| 2026-06-24 | strict structured output 优先走合成工具路径 | 不依赖 Anthropic structured output beta，便于统一校验 | 需要在桥接层维护合成 tool 和输出反渲染逻辑 |
| 2026-06-24 | thinking 只输出安全 summary / reasoning item | 防止把 hidden thinking 混入普通文本 | Chat 默认不暴露 thinking；Responses 按可消费 contract 渲染 |
| 2026-06-24 | image_generation 不纳入本批本地模拟 | 当前项目有图像权限和流式检测，但没有可供 Anthropic bridge 调用的本地图像生成 provider | 强制 `image_generation` 仍由权限层或 bridge 受控失败；不让模型用文本假装生成图片 |
| 2026-06-24 | Chat strict schema 失败使用 `message.refusal` | Chat Completions JSON 没有 Responses `status=failed` 结构，顶层 `{error}` 会被现有 response-inspection 当作协议错误覆盖 | 客户端拿到合法 Chat Completion，`content=null`，`refusal` 中带 bridge schema mismatch code |
| 2026-06-24 | Anthropic bridge 不直接承接 `/responses/compact` | Anthropic Messages 没有 OpenAI Responses compact endpoint；直接转发会伪造上游能力 | 只恢复 `compaction_summary` 输入；真正 compact 继续走网关托管 summary compact |
| 2026-06-24 | 网关托管 compact 输出采用官方 `compaction` item | OpenAI CompactResource 的长期外形是 `response.compaction` + `compaction` output item；继续输出历史别名会让更严格的 Responses 客户端误判 | `/responses/compact` 输出改为 `compaction`；输入侧继续接受 `compaction` 和 `compaction_summary`，Anthropic 上游只接收恢复后的 system summary |
| 2026-06-24 | Responses web_search 先采用本地预取模拟 | Anthropic bridge 目前没有上游工具调用后再续请求的 continuation 钩子；本地 HTTP search executor 已存在且可审计 | 支持 JSON/SSE 的 `web_search_call` 和 citation；模型自主改写查询、多次 search/open_page/find_in_page 后续再做 |
| 2026-06-24 | 文件输入先做 inline / URL 子集，`file_id` resolver 拆入 PLAN-0060 | OpenAI `file_id` 是 OpenAI 文件存储引用，不能直接发给 Anthropic；inline PDF / text 与 PDF URL 两边协议都有 document 表达；本地 Files / Vector Store 是独立运行时 | PLAN-0060 已落地本地 Files resolver；客户端传 inline 文件、PDF URL 或本地 `/v1/files` 上传后的 `file_id` 均可走当前桥接路径，未知或跨边界 `file_id` 仍本地失败 |
| 2026-06-24 | Chat web_search 复用本地预取但不复用 Responses 输出形态 | Chat Completions 搜索是专用搜索模型路径，不产生 Responses `web_search_call` item；Chat SSE chunk 也不适合塞 Responses typed event | Chat JSON 用 `message.annotations` 暴露 citation；Chat SSE 只流式输出正文和 `[n]` 标记，完整 sources 仍属 Responses 能力 |

## 验收标准

- [x] 高兼容能力矩阵和计划文档已同步到索引。
- [x] 所有非 function hosted tools 都有明确策略：映射、模拟、降级或受控拒绝。
- [x] Chat web_search / 搜索模型路径在配置本地执行器时可成功桥接，未配置时受控失败。
- [x] JSON schema strict 不再只靠提示词；已通过合成工具强制输出，并补本地 schema 二次校验。
- [x] Reasoning / thinking 不泄露隐藏思考，且 usage / Responses item 有明确映射。
- [x] 图片 URL / data URL 继续成功；Chat / Responses inline PDF / text 文件和 Responses PDF URL 成功；`file_id` 未配置时返回稳定 OpenAI 错误。
- [x] Compact / previous_response_id 在 Anthropic bridge 下有专项 mock 覆盖。
- [x] Anthropic native 和既有 OpenAI-compatible bridge 不回归。
- [x] 真实账户验证完成，凭据不落盘且扫描无命中。

## 验证记录

- 类型检查：2026-06-24 已复跑并通过 `pnpm --dir backend typecheck`；`pnpm --dir frontend typecheck` 仍失败在 `frontend/src/scripts/regression/account-import-protocol-regression.ts` 的 Node 类型声明 / `node:*` 模块解析，属既有前端回归脚本环境问题。
- Mock 回归：2026-06-24 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖四入口、function tools、图片 URL / data URL、Chat `file_data` PDF、Responses `file_data` text、Responses PDF `file_url`、strict JSON schema 合成工具、本地 schema mismatch 失败、thinking JSON / SSE、Responses web_search 本地预取 JSON/SSE、Chat web_search 本地预取 JSON/SSE、Chat 搜索模型路径、hosted tool 受控失败、`file_id` 受控失败、`compaction` / `compaction_summary` 恢复、`/responses/compact` 官方外形、跨 API Key snapshot 拒绝和 Codex `previous_response_id`。
- 回归验证：2026-06-24 已复跑并通过 `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai`；先前已通过 `pnpm --dir backend test:anthropic-gateway-mock-ai`、`pnpm --dir backend test:codex-client-strategy`、`pnpm --dir backend test:hybrid-gateway-mock-ai`。其中 DeepSeek / GLM 回归继续覆盖既有 Chat-only bridge web_search 工具循环，确认本次 Anthropic bridge 预取模拟未破坏原路径。
- 真实联调：2026-06-24 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-real`；真实上游 `https://vsllm.com`、模型 `claude-sonnet-4-6`、源模型 `gpt-5.5`。结果：核心四入口、`file_search` 本地受控错误、Chat structured output、Chat `file_data` text、Responses `file_data` text、Responses thinking、Responses file_search 本地运行时和 Responses compact 通过；本次 Chat image data URL 可选探针因请求超时失败，Chat web_search 当前真实脚本记录为未配置执行器下的受控 unsupported。真实联调不测外部 PDF URL，避免把上游外网下载稳定性并入协议回归。
- 凭据检查：2026-06-24 已用固定 key 前缀扫描 `backend`、`frontend`、`docs`、`package.json`、`pnpm-lock.yaml`，无命中；`git diff --check` 只有 CRLF 提示，无 whitespace error。
- 未验证项：image_generation provider、MCP / computer / code execution 运行时仍未实现；`/responses/compact` endpoint 本身不走 Anthropic Messages 直转，而是网关托管 compact 状态层；Chat 搜索模型路径已由 mock 覆盖，真实联调当前未配置本地 web search executor。

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
