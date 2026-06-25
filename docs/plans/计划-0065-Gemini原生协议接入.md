# PLAN-0065 Gemini 原生协议接入

## 基本信息

- 编号：PLAN-0065
- 状态：进行中（待真实账号联调）
- 创建时间：2026-06-25
- 更新时间：2026-06-25
- 需求来源：用户对话
- 执行者：AI
- 关联模块：前端 / 后端 / 存储 / 网关 / 供应商驱动 / Gemini native / gemini-cli / Mock 回归 / 文档 / 验证

## 需求目标

- 背景：系统已接入 OpenAI、Anthropic、GLM、DeepSeek 等供应商，并建立协议 / 供应商 / 协议档案分层。用户要求补充 Gemini 兼容能力：默认使用 Gemini 自家协议；如果客户端使用 OpenAI Chat，则利用 Gemini 官方 OpenAI compatibility 直连，不再为 Gemini 做专属模型映射或协议转换。
- 目标：设计并后续落地 `gemini/v1beta` 原生协议档案，覆盖 Gemini API Key 账号、Gemini native JSON / SSE、usage、模型目录、mockai 回归和真实 `gemini-cli` 调用验证。
- 交付物：设计文档、协议兼容文档、实施计划；后续代码实现、mockai 测试和真实 `gemini-cli` E2E。

## 范围边界

### 本次包含

- [x] 新增 Gemini 账号接入设计文档。
- [x] 新增 Gemini 协议兼容设计文档。
- [x] 更新功能文档索引、架构总览和模型目录边界。
- [x] 新增 `gemini` 供应商、协议档案和默认分组。
- [x] 新增 Gemini OpenAI Chat 兼容档案和默认分组。
- [x] 新增 Gemini native ProtocolDriver 和 ProviderDriver。
- [x] 新增前端 Gemini API Key 账户表单兜底和协议能力识别。
- [x] 新增 Gemini 模型目录、价格目录和 usage semantic。
- [x] 新增 mockai 回归覆盖 Gemini native JSON / SSE / countTokens / models / error。
- [x] 新增 mockai 回归覆盖 Gemini OpenAI Chat 直连、Codex Responses 显式映射到 Chat、Anthropic Messages 映射禁用。
- [ ] 新增真实 `gemini-cli` E2E 脚本。
- [ ] 用户提供真实 Gemini 账号后完成真实调用验证。

### 本次不包含

- 不做 Gemini 专属 OpenAI Chat / Responses -> Gemini native 协议映射。
- 不做 Google 登录 / Code Assist / Workspace / Vertex AI 账号。
- 不做 Live API WebSocket 代理。
- 不把 Gemini native 账号伪装成 OpenAI Chat 或 Responses 上游。

## 关联文档

- 设计文档：`docs/functions/Gemini账号接入.md`
- 协议兼容文档：`docs/functions/Gemini协议兼容设计.md`
- 架构总览：`docs/architecture/架构总览.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- 供应商驱动分层：`docs/architecture/供应商驱动分层规范.md`
- 测试说明：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：Gemini native 是独立 `gemini/v1beta` 协议；OpenAI Chat 客户端走 Gemini 官方 OpenAI compatibility 直连，不做项目内 Gemini bridge。
- 数据变化：新增 `providerCode = gemini`、`protocolCode = gemini`、`profile_gemini_native_v1beta`、`profile_gemini_openai_chat_v1beta`、默认 Gemini 分组、默认 Gemini OpenAI Chat 分组和 `usage_semantic = gemini/openai` 分档案语义。
- 接口变化：新增本地 `/v1beta/models/*:generateContent`、`:streamGenerateContent`、`:countTokens`、`/v1beta/models` 等 Gemini native 入口。
- 前端变化：AI 账户创建页新增 `Google Gemini` 供应商和 `Gemini API Key` 接入类型，中文文案说明 native / OpenAI-compatible 边界。
- 后端变化：新增 Gemini protocol / provider / model catalog / pricing / usage semantic driver；本地认证读取 `Authorization`、`x-goog-api-key` 和 query `key`。
- 数据处理策略：只按当前 schema 落地，不做旧字段兼容；如本地已有手工配置，后续由用户离线同步。

## 执行拆解

- [x] 文档和矩阵先行。
- [x] 存储种子：供应商、协议、endpoint family、profile、默认分组。
- [x] Gemini OpenAI Chat：`protocolCode = openai` 档案、Chat-only endpoint modes、Codex `responses -> chat_completions` 显式映射路径。
- [x] 后端类型：ProviderCode / ProtocolCode / EndpointMode / usage semantic。
- [x] Gemini ProtocolDriver：路径识别、模型提取、错误 shape、JSON / SSE 响应语义、模型列表渲染。
- [x] Gemini ProviderDriver：凭据归一、URL helper、header 过滤、上游认证。
- [x] 模型目录与价格：内置模型、Gemini usage 语义。
- [x] 前端 capability：供应商展示、账号表单兜底、endpoint modes。
- [x] Mock 回归：native JSON、SSE、countTokens、models、auth、错误、usage、协议隔离。
- [ ] 真实 E2E：`gemini-cli` 使用 `GOOGLE_GEMINI_BASE_URL` 调用本地网关。
- [ ] 更新测试说明和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档检查 | Markdown 链接和空白检查 | `git diff --check` | 无新增空白错误 | 通过 | 仅有 Windows LF/CRLF 提示 |
| Mock 回归 | Gemini native 网关 mock | `pnpm --dir backend run test:gemini-gateway-mock-ai` | JSON / SSE / countTokens / models / auth / error 全通过 | 通过 | 覆盖本地 key 清理、上游 `x-goog-api-key`、OpenAI Chat 路径隔离 |
| Mock 回归 | Gemini OpenAI Chat 直连 | `pnpm --dir backend run test:gemini-gateway-mock-ai` | OpenAI Chat 命中 `/v1beta/openai/chat/completions`，使用账号 Bearer Key | 通过 | 新增 `profile_gemini_openai_chat_v1beta` |
| Mock 回归 | Codex Responses 到 Gemini | `pnpm --dir backend run test:gemini-gateway-mock-ai` | 显式 `responses -> chat_completions` 模型映射命中 Gemini OpenAI Chat | 通过 | 上游模型改写为 `gemini-3.5-flash` |
| Mock 回归 | 供应商边界 | `pnpm --dir backend run test:gemini-gateway-mock-ai` | OpenAI Chat 不自动进入 Gemini native，Anthropic Messages 不能映射到 Gemini OpenAI Chat | 通过 | 专项断言 `/v1/chat/completions` 不命中 native；`messages -> chat_completions` 保存失败 |
| 类型检查 | 后端类型检查 | `pnpm --dir backend run typecheck` | 通过 | 通过 | `tsc -p tsconfig.json --noEmit` |
| 类型检查 | 前端类型检查 | `pnpm --dir frontend run typecheck` | 通过 | 通过 | `vue-tsc --noEmit` |
| 前端回归 | 账户协议测试 | `pnpm --dir frontend run test:account-test-protocols` | Gemini v1beta 可测试，未支持协议仍拦截 | 通过 | 已补 Gemini 断言 |
| 前端回归 | 账户基础文案 | `pnpm --dir frontend run test:account-basic-formatters` | 基础 formatter 通过 | 通过 | 已覆盖通用基础文案 |
| 前端体验 | Gemini 账户创建 / 编辑 | 类型检查 + 现有动态表单 | 中文文案、默认值、校验正确 | 通过 | 复用现有 API Key 表单，新增 Gemini fallback provider 与 endpoint modes |
| 真实联调 | `gemini-cli` JSON 输出 | `gemini -m gemini-3.5-flash -p "只回复 JUHE_GEMINI_OK" --output-format json` | 通过本地网关返回 marker | 未执行 | 等真实 Gemini 账号 |
| 真实联调 | `gemini-cli` stream-json 输出 | `gemini -m gemini-3.5-flash -p "分两段输出 JUHE 和 GEMINI" --output-format stream-json` | 通过本地网关收到流式事件 | 未执行 | 等真实 Gemini 账号 |
| 未覆盖说明 | Google 登录 / Vertex / Live API | 不执行 | 明确不属于本计划 | 不适用 | 后续单独立项 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-25 | 进行中 | AI | 创建目标和计划；完成 Gemini native 账号接入与协议兼容设计文档。明确不为 Gemini 做专属 OpenAI 映射，OpenAI 客户端使用官方 OpenAI compatibility 直连。 |
| 2026-06-25 | 进行中 | AI | 完成 Gemini native 后端直连、前端兜底识别、mockai 回归和类型检查；本机已安装 `gemini` CLI `0.47.0`，等待真实 Gemini 账号做 E2E。 |
| 2026-06-25 | 进行中 | AI | 根据用户补充边界收口：新增 Gemini OpenAI Chat 档案和默认分组；Codex 使用 Gemini 时只走 `responses -> chat_completions` 到 Gemini OpenAI Chat；禁止 Anthropic Messages 映射到 Gemini。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-25 | Gemini 默认走 `gemini/v1beta` native | Gemini 自家协议才能暴露完整能力 | 新增独立 ProtocolDriver，不复用 OpenAI / Anthropic 字段路径 |
| 2026-06-25 | 不做 Gemini 专属协议映射 | Gemini 官方已支持 OpenAI Chat compatibility，项目内再转一次会扩大不确定性 | OpenAI Chat 客户端按 OpenAI-compatible 直连档案处理 |
| 2026-06-25 | Gemini OpenAI Chat 档案禁止 Anthropic Messages 来源映射 | 用户明确要求 Gemini 不参与 Anthropic / Responses / native 多向互转，Codex 到 Gemini 只需要现有 Responses-to-Chat bridge | 模型映射保存层和前端 UI 对 `profile_gemini_openai_chat_v1beta` 只保留 Chat / Responses-to-Chat |
| 2026-06-25 | `gemini-cli` 验收使用 `GOOGLE_GEMINI_BASE_URL` | 源码显示这是官方 CLI 的 gateway 模式，能走 `@google/genai` native endpoint | 真实 E2E 用 `GEMINI_API_KEY` 填本地 Key，不用 Google 登录 |

## 验收标准

- [x] 设计文档和协议兼容文档已创建并纳入索引。
- [x] Gemini API Key 账户可创建、绑定分组和调度。
- [x] Gemini native JSON / SSE / countTokens / models 在 mockai 中通过。
- [x] Gemini OpenAI Chat 直连和 Codex Responses 显式映射在 mockai 中通过。
- [x] 本地认证支持 `Authorization`、`x-goog-api-key` 和 query `key`，且不泄漏本地 Key。
- [x] usage 和成本按 Gemini semantic 记录。
- [x] OpenAI Chat 请求不自动进入 Gemini native。
- [ ] 真实 `gemini-cli` 可通过本项目完成 JSON 和 stream-json 调用。

## 验证记录

- 类型检查：`pnpm --dir backend run typecheck` 通过；`pnpm --dir frontend run typecheck` 通过。
- Mock 回归：`pnpm --dir backend run test:gemini-gateway-mock-ai` 通过，覆盖 Gemini native、Gemini OpenAI Chat、Codex Responses 映射和 Anthropic Messages 映射禁用。
- 前端回归：`pnpm --dir frontend run test:account-test-protocols`、`pnpm --dir frontend run test:account-basic-formatters` 通过。
- CLI 环境：本机已安装 `gemini` CLI `0.47.0`。
- 真实联调：未执行，等待用户提供真实 Gemini 账号。

## 风险与注意事项

- Gemini API 和 `gemini-cli` 当前仍在快速变化，`v1beta`、preview 模型、usage 字段和 CLI gateway 行为需要在实现前再次核对官方文档和本地源码。
- Files / cache / Live API 等能力涉及上传、长期资源和 WebSocket，不能为了“全功能”在第一轮把大文件或双向流量塞进普通 JSON 代理。
- Gemini CLI Google 登录走 Code Assist 内部接口，和 Gemini API Key native 不是同一个协议档案；真实验收必须使用 gateway 模式。
- 真实联调受用户账号额度、地区、模型可用性和官方限流影响，失败需要区分本地协议错误与上游账号错误。

## 完成总结

- 完成时间：后端 / 前端 mock 阶段完成于 2026-06-25；真实账号联调待补充。
- 实际完成内容：Gemini native provider / protocol / endpoint modes / usage / error shape / models list / mock 回归；前端新增 Gemini fallback provider、协议识别和 endpoint mode 校验。
- 主要改动位置：`backend/src/domain/`、`backend/src/modules/gateway/protocols/gemini-v1beta/`、`backend/src/modules/providers/drivers/gemini/`、`backend/src/modules/model-pricing/`、`frontend/src/shared/providerProtocol.ts`、`frontend/src/views/accounts/`。
- 验证结果：后端 typecheck、前端 typecheck、Gemini mock 回归、前端协议回归和基础 formatter 回归均通过。
- 后续建议：用户提供真实 Gemini API Key 后，使用已安装的 `gemini` CLI 对本地网关执行 JSON 与 stream-json E2E。
