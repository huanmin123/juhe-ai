# PLAN-0068 Gemini Native 转 Anthropic Messages 桥接

## 基本信息

- 编号：PLAN-0068
- 状态：已完成
- 创建时间：2026-06-26
- 更新时间：2026-06-26
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 前端 / 网关 / 供应商驱动 / 模型映射 / Gemini native / Anthropic Messages / Mock 回归 / 文档 / 验证

## 需求目标

- 背景：混合智能路由中，Gemini native 客户端可能被模型映射到 Anthropic Messages 上游。当前只支持 `generate_content|stream_generate_content -> chat_completions`，因此 Gemini CLI / SDK 请求无法直接使用 Claude / Anthropic Messages 能力。
- 目标：新增显式 `generate_content|stream_generate_content -> messages` 模型映射能力，让 Gemini native 生成类请求可由 Anthropic Messages 上游承接，并保持下游 Gemini JSON / SSE 响应形态。
- 交付物：文档矩阵、前后端模型映射放行、Anthropic driver 接入、Gemini->Anthropic 请求桥接、Anthropic->Gemini 响应还原、mockai 全链路回归和验证记录。

## 范围边界

### 本次包含

- [x] 建立新计划并更新长期协议矩阵。
- [x] 更新 `docs/functions/自定义模型与模型映射设计.md`、`docs/functions/Gemini协议兼容设计.md` 和导入协议说明。
- [x] 后端模型映射校验放开 `generate_content|stream_generate_content -> messages`，继续禁止 Gemini native 到 Responses。
- [x] 前端模型映射 UI / 保存 payload 放开 Gemini native 左侧来源选择右侧 Messages，且只在 Anthropic 协议档案账号上允许。
- [x] Anthropic provider driver 识别 Gemini native 显式映射，目标上游路径固定为 `/v1/messages`。
- [x] 请求转换覆盖文本、图片 inlineData / fileData、systemInstruction、generationConfig、functionDeclarations、toolConfig、functionCall / functionResponse。
- [x] 响应转换覆盖 Anthropic JSON / SSE 到 Gemini JSON / SSE，含 text、tool_use、usage 和 finish reason。
- [x] 不支持字段按 Gemini 形态 guidance-first 返回，不伪造成功。
- [x] mockai 覆盖 JSON / SSE、工具、图片、非法方向、guidance 和回归矩阵。

### 本次不包含

- 不做 Gemini native 到 OpenAI Responses；右侧 `responses` 只允许 `responses -> responses` 原生直连，Chat -> Responses 已由 PLAN-0070 收敛为禁止方向。
- 不做 Anthropic Messages 到 Gemini native 的反向桥接。
- 不做 OpenAI Chat / Responses 到 Gemini native。
- 不做 `countTokens`、`embedContent`、Files、Cached Contents、Batch、Live 到 Anthropic Messages 的桥接。
- 不把 Anthropic 不支持或当前网关无法保真执行的 Gemini native 能力伪造成成功；统一走 guidance-first 或本地受控错误。
- 不使用真实账户替代 mockai 覆盖；真实账户只在 mock 通过后按可用额度做代表抽样。

## 关联文档

- 长期协议矩阵：`docs/plans/计划-0062-协议互转矩阵与模型映射约束.md`
- Gemini Native 转 Chat 历史计划：`docs/plans/计划-0067-GeminiNative转Chat桥接.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- Gemini 协议兼容：`docs/functions/Gemini协议兼容设计.md`
- AI 账户导入协议：`docs/functions/AI账户导入协议.md`
- Anthropic 账号接入：`docs/functions/Anthropic账号接入.md`
- OpenAI 到 Anthropic 高兼容能力矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- 测试说明：`docs/develop/测试与验证说明.md`

## 方案概述

- 协议规则：`sourceEndpointFamily = generate_content` 或 `stream_generate_content`，`upstreamEndpointFamily = messages`；左侧模型来自 Gemini 协议模型池，右侧模型来自当前 Anthropic 协议账号可用模型池。
- 调度规则：Gemini native 入口先按路径解析模型和 endpoint family；命中显式账号映射后进入目标 Anthropic Messages 账号，否则继续按 Gemini native 直连候选。
- 请求转换：Gemini `contents` / `systemInstruction` / `generationConfig` / `tools.functionDeclarations` / `toolConfig` 转 Anthropic `messages` / `system` / `tools` / `tool_choice` / 采样字段。
- 响应转换：Anthropic JSON / SSE 还原为 Gemini `GenerateContentResponse` JSON / SSE；`usage` 映射到 `usageMetadata`，`stop_reason` 映射到 Gemini `finishReason`。
- guidance-first：`cachedContent`、非图片二进制、Gemini 原生搜索 / 代码执行 / cache / live 等无法保真的能力不上游，返回 Gemini 形态 guidance。
- URL / header：桥接时移除 Gemini native 的 `alt` 和本地 `key` query，使用目标 Anthropic 账号自己的认证头和 `anthropic-version`。

## 执行拆解

- [x] 创建计划并更新计划索引。
- [x] 更新长期协议矩阵和 Gemini / 模型映射 / 导入协议文档。
- [x] 扩展后端 `model-mapping` helper，新增 Gemini native -> Messages 判定与上游路径。
- [x] 扩展 repository / normalization / endpoint mode 校验。
- [x] 扩展前端模型映射协议选项和保存前校验。
- [x] 新增 Gemini native -> Anthropic Messages bridge helper。
- [x] 接入 Anthropic provider driver。
- [x] 扩展 `gemini-gateway-mock-ai-regression` 和 `account-model-mapping-regression`。
- [x] 运行专项 mock、整体协议 mock、后端 / 前端 typecheck。
- [x] 根据结果更新验证记录。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档验证 | 协议矩阵一致性 | 人工核对 PLAN-0062 / PLAN-0068 / Gemini 兼容 / 模型映射文档 | Gemini native 到 Messages 允许，Gemini native 到 Responses 禁止 | 已通过 | 2026-06-26 已核对并同步长期文档 |
| Mock 回归 | 模型映射保存校验 | `pnpm --dir backend test:account-model-mapping` | `generate_content|stream_generate_content -> messages` 可保存到 Anthropic 档案，其他非法方向拒绝 | 已通过 | 2026-06-26 通过 |
| 前端回归 | 账号编辑保存流程 | `pnpm --dir frontend test:account-edit-save-flow` | UI 和保存 payload 与后端协议矩阵一致 | 已通过 | 2026-06-26 通过 |
| 前端回归 | 导入协议说明 | `pnpm --dir frontend test:account-import-protocol` | 内置导入协议说明包含 Gemini -> Messages 规则 | 已通过 | 2026-06-26 通过 |
| Mock 回归 | Gemini native 到 Anthropic JSON | `pnpm --dir backend test:gemini-gateway-mock-ai` | `generateContent` 命中上游 `/v1/messages` 并返回 Gemini JSON | 已通过 | 2026-06-26 通过，覆盖 text、image、tool_use 回包和 usage |
| Mock 回归 | Gemini native 到 Anthropic SSE | `pnpm --dir backend test:gemini-gateway-mock-ai` | `streamGenerateContent` 命中 Anthropic SSE 并返回 Gemini SSE | 已通过 | 2026-06-26 通过，覆盖 text delta、tool_use 和 usage |
| Mock 回归 | 工具和图片 | `pnpm --dir backend test:gemini-gateway-mock-ai` | functionDeclarations / functionCall / functionResponse / 图片输入转换正确 | 已通过 | 2026-06-26 通过，覆盖 inlineData 图片、functionDeclarations、toolConfig、历史 functionCall / functionResponse |
| Mock 回归 | guidance-first | `pnpm --dir backend test:gemini-gateway-mock-ai` | cachedContent / 非图片 inlineData / 不支持工具不上游，返回 Gemini guidance | 已通过 | 2026-06-26 覆盖 responseMimeType / responseSchema 不上游并返回 Gemini guidance |
| 类型检查 | 后端类型检查 | `pnpm --dir backend typecheck` | TypeScript 通过 | 已通过 | 2026-06-26 通过 |
| 类型检查 | 前端类型检查 | `pnpm --dir frontend typecheck` | Vue / TypeScript 通过 | 已通过 | 2026-06-26 通过 |
| 整体回归 | 协议矩阵 mockai | PLAN-0062 既有整体 mock 命令集 | 现有 Chat / Responses / Messages / Gemini 桥接不回归 | 已通过 | 2026-06-26 复跑 Anthropic 原生、OpenAI->Anthropic、Anthropic->Chat 和 Gemini mock，均通过 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-26 | 进行中 | AI | 用户指出当前还缺 Gemini native 到 Anthropic Messages；本计划作为 PLAN-0067 的后续增量，不改写 PLAN-0067 历史范围。 |
| 2026-06-26 | 进行中 | AI | 审计确认当前 Gemini native 只允许映射到 Chat，Anthropic driver 只识别 OpenAI Chat / Responses 到 Messages，缺 Gemini native -> Messages 的 driver 入口和响应还原层。 |
| 2026-06-26 | 已完成 | AI | 已完成文档、前后端模型映射、Anthropic driver、Gemini->Anthropic bridge helper 和 mockai 回归；未使用真实账户。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-26 | Gemini native 到 Messages 必须显式账号模型映射触发 | 防止仅凭模型名或客户端画像误路由，保持混合智能路由可排障 | 没有映射时仍走 Gemini native 原生候选 |
| 2026-06-26 | Gemini native 到 Responses 继续禁止 | Responses 状态、工具、compact 和上下文链与 Gemini native 不同构，不能为了“完全兼容”伪造 | 前端、后端和导入校验继续禁右侧 Responses |
| 2026-06-26 | 不支持能力走 Gemini 形态 guidance-first | 上游不支持不是网关 500；静默丢字段会造成客户端误判 | Bridge 必须先验证不可保真字段，失败不上游 |

## 验收标准

- [x] 用户可在 Anthropic 协议账号模型映射中配置 `generate_content -> messages` 和 `stream_generate_content -> messages`。
- [x] 非法方向 `generate_content|stream_generate_content -> responses` 仍无法通过 UI、保存 API 或导入预览。
- [x] Gemini JSON 请求经映射命中 Anthropic `/v1/messages`，下游收到 Gemini JSON。
- [x] Gemini SSE 请求经映射命中 Anthropic Messages SSE，下游收到 Gemini SSE。
- [x] 工具、图片、usage、finish reason 和 guidance-first 边界有 mockai 覆盖。
- [x] 现有 OpenAI -> Anthropic、Anthropic -> Chat、Gemini -> Chat、Responses -> Chat 回归通过；Chat -> Responses 已由 PLAN-0070 收敛为禁止方向。

## 验证记录

- 2026-06-26 执行 `pnpm --dir backend test:gemini-gateway-mock-ai`，通过；覆盖 Gemini native 直连、Gemini->Chat、Responses->Gemini OpenAI Chat、Gemini GenerateContent / StreamGenerateContent -> Anthropic Messages JSON / SSE、工具、图片、usage、finish reason 和 guidance-first。
- 2026-06-26 执行 `pnpm --dir backend test:account-model-mapping`，通过；覆盖 Gemini native -> Messages 保存、候选窗口、Gemini native -> Responses 拒绝、OpenAI 档案配置 Gemini->Messages 拒绝。
- 2026-06-26 执行 `pnpm --dir frontend test:account-edit-save-flow`、`pnpm --dir frontend test:account-import-protocol` 和 `pnpm --dir frontend typecheck`，通过。
- 2026-06-26 执行 `pnpm --dir backend test:anthropic-gateway-mock-ai`、`test:protocol-boundary-openai-anthropic`、`test:anthropic-openai-chat-bridge-mock`、`test:anthropic-openai-chat-gateway-mock`，通过；`test:protocol-boundary-openai-anthropic` 首次并发执行遇到一次 `ECONNRESET`，单独重跑通过。
- 2026-06-26 执行 `pnpm --dir backend typecheck`，通过。

## 风险与注意事项

- Anthropic Messages 支持 thinking、cache_control、server tools 等能力，但 Gemini native 下游没有等价响应形态；本桥接只承接 Gemini 可表达的生成、工具和图片子集。
- Gemini `safetySettings`、`cachedContent`、非图片二进制、原生搜索 / code execution / live 等不应静默丢弃。
- Anthropic SSE 的 `tool_use` 输入可能分片到达，必须在 `content_block_stop` 后再输出 Gemini `functionCall`。
- 真实账户如果额度不足或供应商中转私有化 Anthropic SSE 格式，只记录真实阻塞，不影响 mockai 验收。

## 完成总结

- 完成时间：2026-06-26。
- 实际完成内容：新增 Gemini native GenerateContent / StreamGenerateContent 到 Anthropic Messages 的显式模型映射桥接；保持下游 Gemini JSON / SSE 形态；非法 Responses 方向继续禁止；不可保真能力按 Gemini guidance-first 返回。
- 主要改动位置：`backend/src/modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.ts`、`backend/src/modules/providers/drivers/anthropic/driver.ts`、`backend/src/modules/gateway/protocols/openai-v1/model-mapping.ts`、`backend/src/storage/account-model-normalization.ts`、`frontend/src/views/accounts/AccountStrategySection.vue`、`frontend/src/views/accounts/accountSavePayload.ts`。
- 验证结果：专项 mock、协议互转相关 mock、前端 / 后端 typecheck 通过；本轮未使用真实账户。
- 后续建议：真实账户额度和模型可用性稳定后，再做 Gemini CLI / SDK 对 Claude / Anthropic Messages 的代表性真实抽样；不要把 Gemini cachedContent、safetySettings、Live、Batch 等非生成类或不可保真能力并入本桥接。
