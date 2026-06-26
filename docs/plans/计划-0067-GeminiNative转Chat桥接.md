# PLAN-0067 Gemini Native 转 Chat 桥接

## 基本信息

- 编号：PLAN-0067
- 状态：已完成
- 创建时间：2026-06-25
- 更新时间：2026-06-25
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 前端 / 网关 / 供应商驱动 / 模型映射 / Gemini native / OpenAI Chat bridge / GLM / Mock 回归 / 文档 / 验证

## 需求目标

- 背景：Gemini CLI / Gemini SDK 使用 Gemini native `generateContent` / `streamGenerateContent` 协议。如果用户的可用上游实际是 GLM、DeepSeek、OpenAI-compatible 或 Gemini OpenAI Chat 这类 Chat Completions 能力，旧规则下 Gemini native 下游无法通过模型映射使用这些模型。
- 目标：新增显式 `generate_content|stream_generate_content -> chat_completions` 模型映射能力，让 Gemini native 下游请求可以被真实 Chat 上游承接，并保持下游 Gemini JSON / SSE 响应形态。
- 交付物：后端 bridge、供应商 driver 接入、前后端模型映射校验与 UI、mockai 回归、真实账户联调、文档和计划更新。

## 范围边界

### 本次包含

- [x] 后端模型映射 endpoint family 扩展：左侧支持 `generate_content`、`stream_generate_content`，右侧只允许 `chat_completions`。
- [x] Gemini native 请求到 OpenAI Chat 请求的共享 bridge：文本、图片 URL / inline data、systemInstruction、generationConfig、functionDeclarations、toolConfig、functionCall / functionResponse。
- [x] Chat JSON / SSE 响应还原为 Gemini `GenerateContentResponse` JSON / SSE。
- [x] GPT API Key、OpenAI-compatible、DeepSeek、GLM driver 接入 Gemini native 到 Chat bridge。
- [x] Gemini 形态 guidance：不支持的 Gemini native 能力返回 Gemini JSON / SSE guidance，不返回 500。
- [x] 前端模型映射 UI：Gemini GenerateContent / StreamGenerateContent 左侧来源、Gemini 模型池和保存前校验。
- [x] mockai 覆盖 Gemini native 到 GLM Chat JSON / SSE。

### 本次不包含

- 不做 OpenAI Chat / Responses 到 Gemini native。
- 不做 Gemini native 到 Responses 或 Anthropic Messages。
- 不做 `countTokens`、`embedContent`、Files、Cached Contents、Batch、Live 到 Chat 的桥接。
- 不做无显式模型映射的自动转换，也不按模型名前缀猜测上游协议。
- 不在运行时代码中写旧字段兼容或历史数据迁移。

## 关联文档

- 架构总览：`docs/architecture/架构总览.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- Gemini 协议兼容：`docs/functions/Gemini协议兼容设计.md`
- Gemini 账号接入：`docs/functions/Gemini账号接入.md`
- AI 账户导入协议：`docs/functions/AI账户导入协议.md`
- 长期协议矩阵：`docs/plans/计划-0062-协议互转矩阵与模型映射约束.md`
- 测试说明：`docs/develop/测试与验证说明.md`

## 方案概述

- 协议规则：`sourceEndpointFamily = generate_content` 或 `stream_generate_content`，`upstreamEndpointFamily = chat_completions`；左侧模型来自 Gemini 协议模型池，右侧模型来自目标 Chat 上游账号所属供应商目录或 OpenAI 协议模型池。
- 调度规则：Gemini native 入口先按路径解析下游协议和模型；在当前 API Key 已绑定账号池内按 `sourceModel + sourceEndpointFamily` 查显式账号模型映射；命中后进入目标 Chat 上游，否则走 Gemini native 直连候选。
- 请求转换：Gemini `contents` / `systemInstruction` / `generationConfig` / `tools.functionDeclarations` / `toolConfig` 转 OpenAI Chat `messages` / `tools` / `tool_choice` / 采样参数；`cachedContent` 和非 JSON 响应 MIME 等不支持能力返回 Gemini guidance。
- 响应转换：Chat JSON / SSE 还原为 Gemini JSON / SSE；usage 映射到 `usageMetadata`，finish reason 映射到 Gemini `finishReason`。
- URL / header：桥接时移除 Gemini native 的 `alt` 和本地 `key` query，使用目标 Chat 上游账号自己的认证头；通用 OpenAI-compatible 根地址按 OpenAI v1 规则拼接 `/v1/chat/completions`，GLM 专用 OpenAI v1 档案要求 `base_url` 显式填到可接受的 OpenAI v1 根，例如 `https://vsllm.com/v1`。

## 执行拆解

- [x] 扩展后端 domain type、请求 schema、导入 / 草稿 / repository / normalization 校验。
- [x] 扩展 OpenAI v1 模型映射 path/query helper。
- [x] 新增 Gemini native 到 OpenAI Chat bridge helper。
- [x] 接入 GLM / DeepSeek / OpenAI-compatible / GPT driver。
- [x] 扩展 Gemini guidance response。
- [x] 扩展前端类型、模型池、账号表单、保存 payload 校验和展示文案。
- [x] 扩展 `gemini-gateway-mock-ai-regression` 和 `account-model-mapping-regression`。
- [x] 更新架构、功能、导入协议、测试说明和计划文档。
- [x] 使用用户提供真实账户执行真实 GLM / Gemini native bridge 验证。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | TypeScript 通过 | 已通过 | 2026-06-25 已通过 |
| 类型检查 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | Vue / TypeScript 通过 | 已通过 | 2026-06-25 已通过 |
| Mock 回归 | Gemini native 到 GLM Chat | `pnpm --filter juhe-ai-backend test:gemini-gateway-mock-ai` | `generateContent` / `streamGenerateContent` 命中 GLM `/chat/completions`，返回 Gemini JSON / SSE | 已通过 | 覆盖 query `alt` / `key` 清理、Bearer 上游认证、tools / tool_choice / usage |
| Mock 回归 | 模型映射保存校验 | `pnpm --filter juhe-ai-backend test:account-model-mapping` | Gemini source 只允许映射到 Chat，非法 Responses 被拒绝 | 已通过 | 2026-06-25 已通过 |
| 前端回归 | 账号编辑保存流程 | `pnpm --filter juhe-ai-frontend test:account-edit-save-flow` | 前端保存前协议矩阵与后端一致 | 已通过 | 2026-06-25 已通过 |
| 前端回归 | 导入协议说明 | `pnpm --filter juhe-ai-frontend test:account-import-protocol` | 前端内置导入协议文案与规则通过断言 | 已通过 | 2026-06-25 已通过 |
| 真实联调 | Gemini native 到真实 Chat 上游 | `pnpm --filter juhe-ai-backend test:gemini-native-chat-bridge-real` | Gemini CLI / SDK 请求能通过显式映射命中真实 Chat 上游 | 已通过 | 2026-06-25 已用真实 `vsllm.com` 账号覆盖 OpenAI-compatible 根地址 `https://vsllm.com` 和 GLM 专用 `/v1` 地址 `https://vsllm.com/v1`，JSON / SSE 均返回 Gemini 形态响应 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-25 | 进行中 | AI | 用户指出 Gemini CLI / Codex CLI 这类客户端画像在上游模型映射到 GLM Chat 时会遇到协议不匹配问题，要求先用 mock AI 验证后再用真实账户。 |
| 2026-06-25 | 前置完成 | AI | 已完成后端 bridge、供应商 driver、前端模型映射 UI / 保存校验、mock 回归和文档更新，进入真实账户验证前置状态。 |
| 2026-06-25 | 已完成 | AI | 已使用用户提供的真实 NewAPI 账号验证 Gemini native `generateContent` / `streamGenerateContent` 显式映射到 `glm-5.2` Chat 上游。OpenAI-compatible 根地址 `https://vsllm.com` 通过；GLM 专用档案使用 `https://vsllm.com/v1` 通过。GLM 专用档案直接填根地址会命中上游 `/chat/completions` HTML 根站，不作为有效配置。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-25 | 只放开 Gemini native 生成类下游到 Chat 上游 | `generateContent` / `streamGenerateContent` 可以合理映射到 Chat 文本生成；countTokens、Files、Live 等语义差异过大 | 映射矩阵只允许 `generate_content|stream_generate_content -> chat_completions` |
| 2026-06-25 | 必须显式账号模型映射触发 | 防止按模型名或客户端画像猜测协议导致错路由和排障困难 | 没有映射时仍按 Gemini native 直连调度 |
| 2026-06-25 | 上游 endpoint mode 保存真实 Chat 能力 | 桥接不能伪造上游 native 能力 | 目标账号仍填 `chat_json` / `chat_sse`，不填 Gemini endpoint mode |
| 2026-06-25 | 不支持能力返回 Gemini guidance | 客户端 agent 能理解下游协议形态，避免 500 或伪成功 | `cachedContent`、非 JSON MIME 等进入 guidance-first |

## 验收标准

- [x] 模型映射 UI 和保存校验允许 Gemini GenerateContent / StreamGenerateContent 作为左侧来源。
- [x] 后端保存 / 导入 / 草稿测试拒绝 Gemini source 到 Responses / Messages。
- [x] Gemini native JSON 请求经映射命中 Chat 上游并返回 Gemini JSON。
- [x] Gemini native SSE 请求经映射命中 Chat SSE 上游并返回 Gemini SSE。
- [x] mockai 和类型检查通过。
- [x] 真实账户验证通过。

## 验证记录

- 已通过：`pnpm --filter juhe-ai-backend typecheck`
- 已通过：`pnpm --filter juhe-ai-frontend typecheck`
- 已通过：`pnpm --filter juhe-ai-backend test:gemini-gateway-mock-ai`
- 已通过：`pnpm --filter juhe-ai-backend test:account-model-mapping`
- 已通过：`pnpm --filter juhe-ai-frontend test:account-edit-save-flow`
- 已通过：`pnpm --filter juhe-ai-frontend test:account-import-protocol`
- 已通过：`pnpm --filter juhe-ai-backend test:gemini-native-chat-bridge-real`，环境变量选择 `JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_PROVIDER=openai`、`JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_BASE_URL=https://vsllm.com`，验证 `gemini-3.5-flash -> glm-5.2` 的 Gemini JSON / SSE 到 Chat bridge。
- 已通过：`pnpm --filter juhe-ai-backend test:gemini-native-chat-bridge-real`，环境变量选择 `JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_PROVIDER=glm`、`JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_BASE_URL=https://vsllm.com/v1`，验证 GLM 专用档案下的 Gemini JSON / SSE 到 Chat bridge。
- 已诊断：`JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_PROVIDER=glm` 且 `JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_BASE_URL=https://vsllm.com` 会访问 `https://vsllm.com/chat/completions`，真实上游返回 HTML 根站内容；该配置应改为 GLM `/v1` 地址或改用通用 OpenAI-compatible 档案。

## 风险与注意事项

- Gemini native 工具、图片、response MIME 和 safety 行为无法与所有 Chat 上游完全等价；不支持能力必须返回 guidance，不伪造成功。
- Chat 上游不一定支持图片、工具或 JSON response_format；真实账户需要按模型能力逐项抽样。
- 第三方中转可能对 `/chat/completions`、SSE 或 tool call 格式有私有差异；NewAPI 根地址应按通用 OpenAI-compatible 导入，专用 GLM 档案则要填到该中转实际 OpenAI v1 根路径。

## 完成总结

- 完成时间：2026-06-25。
- 实际完成内容：Gemini native 到 Chat bridge、供应商 driver 接入、前后端模型映射规则、mock 回归、真实 NewAPI 账号联调和文档同步。
- 后续建议：后续如需扩大真实覆盖，再补工具调用、图片输入和不同 Chat 上游模型的抽样。
