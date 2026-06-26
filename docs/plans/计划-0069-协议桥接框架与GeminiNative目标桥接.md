# PLAN-0069 协议桥接框架与 Gemini Native 目标桥接

## 基本信息

- 编号：PLAN-0069
- 状态：已完成
- 创建时间：2026-06-26
- 更新时间：2026-06-26
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 前端 / 网关 / 供应商驱动 / 模型映射 / 协议桥接框架 / Gemini native / Mock 回归 / 文档 / 验证

## 需求目标

- 背景：当前协议桥接已经覆盖多个方向，但整体仍偏点对点实现。用户明确要求后续每接入一个独立协议，都要正向和反向打通已有协议；如果缺少统一框架，后续会变成不可维护的 N×N 转换网。
- 目标：先建立长期协议桥接框架文档和落地计划，再新增 OpenAI Chat / OpenAI Responses / Anthropic Messages 到 Gemini native GenerateContent 的显式模型映射桥接能力。
- 交付物：协议桥接框架设计、模型映射矩阵更新、前后端保存限制、Gemini provider driver 接入、OpenAI / Anthropic 到 Gemini native 请求和响应桥接、mockai 全量回归和验证记录。

## 范围边界

### 本次包含

- [x] 新增协议桥接框架设计文档，明确中间表示、能力矩阵、guidance-first 和新协议接入模板。
- [x] 更新长期模型映射矩阵，把 `chat_completions|responses|messages -> generate_content` 纳入计划。
- [x] 后端模型映射矩阵允许 Gemini native 作为右侧生成类目标，并限制为 `profile_gemini_native_v1beta`。
- [x] 前端模型映射 UI 和保存前校验按同一矩阵限制右侧协议。
- [x] Gemini provider driver 识别 OpenAI / Anthropic 来源映射，动态选择 `:generateContent` 或 `:streamGenerateContent`。
- [x] 请求桥接覆盖文本、多轮、system、图片、函数工具、JSON 输出和采样参数。
- [x] 响应桥接覆盖 Gemini JSON / SSE 到 OpenAI Chat、OpenAI Responses 和 Anthropic Messages 的下游形态。
- [x] 不支持字段按下游协议 guidance-first 返回，不伪造成功。
- [x] mockai 覆盖正反方向、非法方向、工具、图片、流式、JSON、guidance 和回归矩阵。

### 本次不包含

- 不支持 `messages -> responses`。
- 不支持 Gemini native 到 Responses。
- 不支持 `countTokens`、`embedContent`、Files、Cached Contents、Batch、Live 的跨协议生成桥接。
- 不把右侧 `stream_generate_content` 暴露为独立映射目标；右侧 `generate_content` 根据下游请求是否流式动态选择 Gemini JSON / SSE 上游端点。
- 不在 mockai 通过前使用真实账户替代验证；真实账户只做代表抽样。

## 关联文档

- 协议桥接框架：`docs/functions/协议桥接框架设计.md`
- 长期协议矩阵：`docs/plans/计划-0062-协议互转矩阵与模型映射约束.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- Gemini 协议兼容：`docs/functions/Gemini协议兼容设计.md`
- 请求处理分层：`docs/functions/请求处理分层设计.md`
- OpenAI 到 Anthropic 桥接：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- Anthropic Messages 转 Chat：`docs/functions/AnthropicMessages转Chat协议转换设计.md`
- Codex Responses 转 Chat：`docs/functions/Codex Responses转Chat协议转换设计.md`
- 测试说明：`docs/develop/测试与验证说明.md`

## 方案概述

- 框架方向：后续不再以点对点转换作为默认模式，而是抽象为 `下游协议 -> CanonicalGenerateRequest / Event -> 上游协议`，由能力矩阵决定 `bridge`、`guidance` 或 `reject`。
- Gemini 目标：右侧协议只新增 `generate_content`，表示 Gemini native 生成类上游目标；非流式走 `:generateContent`，流式走 `:streamGenerateContent?alt=sse`。
- 映射规则：`chat_completions -> generate_content`、`responses -> generate_content`、`messages -> generate_content` 只允许配置在 Gemini native 协议档案账号上。
- 响应规则：下游仍收到原协议形态。OpenAI Chat 下游收到 Chat Completion JSON / SSE；OpenAI Responses 下游收到 Responses JSON / SSE；Anthropic Messages 下游收到 Messages JSON / SSE。
- guidance-first：OpenAI hosted tools、Responses state / compact、Anthropic thinking / cache_control、Gemini safety / cachedContent 等不可保真能力不静默丢弃，按下游协议返回可读引导。

## 执行拆解

- [x] 创建目标并记录架构风险。
- [x] 新增协议桥接框架文档。
- [x] 更新 Gemini / 模型映射 / 架构总览 / 计划索引文档。
- [x] 扩展 `AccountModelMappingUpstreamEndpointFamily` 和前后端接口契约。
- [x] 扩展后端协议互转矩阵和 normalization，允许 Gemini native 右侧模型池。
- [x] 扩展前端协议选项、模型池选择和保存前校验。
- [x] 新增 OpenAI / Anthropic 到 Gemini native bridge helper。
- [x] 接入 Gemini provider driver 的 URL、header、endpoint mode、usage model 和响应转换。
- [x] 扩展 mockai 回归测试。
- [x] 运行专项 mock、整体协议 mock、前端 / 后端 typecheck。
- [x] 记录验证结果和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档验证 | 框架设计一致性 | 人工核对协议桥接框架 / 模型映射 / Gemini 兼容 / 架构总览 | 文档不再把 Gemini native 右侧目标永久禁止，且明确中间表示框架 | 已通过 | 已核对 |
| Mock 回归 | 模型映射保存校验 | `pnpm --dir backend test:account-model-mapping` | `chat_completions|responses|messages -> generate_content` 只允许 Gemini native 档案 | 已通过 | 通过 |
| 前端回归 | 账号编辑保存流程 | `pnpm --dir frontend test:account-edit-save-flow` | UI 只展示当前账号可承接的右侧协议，非法组合保存前拦截 | 已通过 | 通过 |
| Mock 回归 | OpenAI Chat 到 Gemini JSON / SSE | `pnpm --dir backend test:gemini-gateway-mock-ai` | Chat 请求命中 Gemini native 上游并返回 Chat 形态 | 已通过 | 通过 |
| Mock 回归 | OpenAI Responses 到 Gemini JSON / SSE | `pnpm --dir backend test:gemini-gateway-mock-ai` | Responses 请求命中 Gemini native 上游并返回 Responses 形态 | 已通过 | 通过 |
| Mock 回归 | Anthropic Messages 到 Gemini JSON / SSE | `pnpm --dir backend test:gemini-gateway-mock-ai` | Messages 请求命中 Gemini native 上游并返回 Anthropic Messages 形态 | 已通过 | 通过 |
| Mock 回归 | 工具和图片 | `pnpm --dir backend test:gemini-gateway-mock-ai` | 工具声明 / 工具调用 / 工具结果 / 图片输入可保真转换 | 已通过 | 通过 |
| Mock 回归 | guidance-first | `pnpm --dir backend test:gemini-gateway-mock-ai` | 不可保真字段不上游，按下游协议返回 guidance | 已通过 | 通过 |
| 类型检查 | 后端类型检查 | `pnpm --dir backend typecheck` | TypeScript 通过，或记录已有无关失败 | 已通过 | 通过 |
| 类型检查 | 前端类型检查 | `pnpm --dir frontend typecheck` | Vue / TypeScript 通过 | 已通过 | 通过 |
| 整体回归 | 协议矩阵 mockai | PLAN-0062 既有整体 mock 命令集 | 现有 Chat / Responses / Messages / Gemini 桥接不回归 | 已通过 | 已跑 Gemini / Anthropic / OpenAI-Anthropic / DeepSeek / GLM mock |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-26 | 进行中 | AI | 用户补充长期架构要求：每接入一个独立协议都要正反向打通已有协议，但不能退化为点对点灾难。 |
| 2026-06-26 | 进行中 | AI | 决定先落协议桥接框架文档，再实现 OpenAI / Anthropic 到 Gemini native 目标桥接。 |
| 2026-06-26 | 已完成 | AI | 已完成 Gemini native 右侧目标桥接、前后端矩阵限制和 mockai 回归；响应转换统一改为网关 `AsyncIterable` body，修复 Node body 被误当 Web `ReadableStream` 的 503 问题。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-26 | 新协议接入默认走统一中间表示 | N×N 点对点转换不可维护 | 后续 bridge helper 逐步向 adapter / event renderer 收敛 |
| 2026-06-26 | Gemini native 右侧目标只暴露 `generate_content` | 下游是否流式是请求传输形态，不应要求用户配置两条右侧映射 | provider driver 根据请求动态选择 `generateContent` 或 `streamGenerateContent` |
| 2026-06-26 | 不支持能力 guidance-first | 上游不支持不是网关 500，静默丢字段会导致客户端误判 | bridge 必须先判断能力，不可保真时不上游 |

## 验收标准

- [x] 前后端都允许在 Gemini native 账号上配置 `chat_completions|responses|messages -> generate_content`。
- [x] 非 Gemini native 档案不能选择右侧 `generate_content`。
- [x] OpenAI Chat JSON / SSE 能桥接到 Gemini native，并返回 Chat Completion 形态。
- [x] OpenAI Responses JSON / SSE 能桥接到 Gemini native，并返回 Responses 形态。
- [x] Anthropic Messages JSON / SSE 能桥接到 Gemini native，并返回 Messages 形态。
- [x] 工具、图片、JSON 输出、usage、finish reason 和 guidance-first 有 mockai 覆盖。
- [x] 现有 OpenAI -> Anthropic、Anthropic -> Chat、Gemini -> Chat、Gemini -> Messages、Responses -> Chat 回归通过；Chat -> Responses 已由 PLAN-0070 收敛为禁止方向。

## 验证记录

- `pnpm --dir backend test:gemini-gateway-mock-ai`：通过。
- `pnpm --dir backend test:account-model-mapping`：通过。
- `pnpm --dir backend test:openai-anthropic-bridge-mock`：通过。
- `pnpm --dir backend test:anthropic-openai-chat-bridge-mock`：通过。
- `pnpm --dir backend test:anthropic-gateway-mock-ai`：通过。
- `pnpm --dir backend test:deepseek-gateway-mock-ai`：通过。
- `pnpm --dir backend test:glm-gateway-mock-ai`：通过。
- `pnpm --dir backend typecheck`：通过。
- `pnpm --dir frontend typecheck`：通过。
- `pnpm --dir frontend test:account-import-protocol`：通过。
- `pnpm --dir frontend test:account-edit-save-flow`：通过。

## 风险与注意事项

- OpenAI Responses 的状态机、`previous_response_id`、`context_management`、`/responses/compact` 和 hosted tools 不能被 Gemini native 原生承接；只能在本地已有运行时支持时处理，否则 guidance。
- Anthropic thinking、cache_control、server tools 与 Gemini native 不同构，不能伪造保真。
- Gemini native 的 safety settings、cachedContent、Google 原生搜索 / code execution / live 等不是 OpenAI / Anthropic 下游协议的通用能力。
- 右侧 `generate_content` 动态承接 JSON / SSE 时，endpoint mode 判断必须看真实上游账号是否支持 `generate_content_json` 或 `generate_content_sse`。
- 真实账户验证必须在 mockai 通过后进行，且不能把用户提供的真实 Key 写入代码、文档、日志或测试快照。

## 完成总结

- 已建立协议桥接框架文档和 PLAN-0069 计划闭环。
- 已将 `chat_completions|responses|messages -> generate_content` 纳入前后端模型映射矩阵，右侧仅允许 Gemini native 档案，且右侧不暴露 `stream_generate_content` 独立目标。
- Gemini provider driver 已支持根据显式模型映射把 OpenAI Chat、OpenAI Responses 和 Anthropic Messages 请求转为 Gemini native GenerateContent / StreamGenerateContent，上游响应再渲染回原下游协议形态。
- mockai 已覆盖 JSON / SSE、工具、图片、JSON 输出、usage、guidance-first 和既有协议桥接回归。
