# PLAN-0064 Anthropic Messages 转 Chat 协议桥接

## 基本信息

- 编号：PLAN-0064
- 状态：已完成
- 创建时间：2026-06-25
- 更新时间：2026-06-25
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 前端 / 网关 / 供应商驱动 / 模型映射 / 协议桥接 / Mock 回归 / 文档 / 验证

## 需求目标

- 背景：当前系统已有 OpenAI Chat / Responses 到 Anthropic Messages、Responses 到 Chat、Chat 到 Responses 的桥接，但缺少 Anthropic Messages 下游请求到 OpenAI Chat-only 上游的显式桥接。用户要求一次性完整落地，不做“首版”留尾巴，并按上游真实能力适配，不支持的能力返回可被客户端 agent 消费的引导。
- 目标：完整支持 `messages -> chat_completions`，覆盖模型映射、候选账号筛选、请求转换、JSON / SSE 响应还原、工具调用、图片输入、受控 guidance、前端配置、导入协议和回归测试。
- 交付物：设计文档、协议矩阵更新、前后端实现、mock 回归和验证记录。

## 范围边界

### 本次包含

- [x] 新增设计文档并纳入功能文档索引。
- [x] 更新协议互转矩阵和模型映射长期规则。
- [x] 扩展 `sourceEndpointFamily` 支持 `messages`，补齐后端 domain / schema / runtime 类型、前端 contract / form type 和模型池入口。
- [x] 模型映射保存 / 导入校验支持 `messages -> chat_completions`，并拒绝 `messages -> responses` / `messages -> messages`。
- [x] 前端模型映射 UI 允许选择 Messages 作为下游协议，并根据协议矩阵强制右侧 Chat Completions。
- [x] 新增共享 bridge：Anthropic Messages 请求转 Chat Completions 请求，Chat JSON / SSE 响应还原为 Anthropic Messages JSON / SSE。
- [x] OpenAI-compatible、GPT、DeepSeek、GLM Chat 上游驱动通过显式映射承接 Anthropic `/v1/messages`。
- [x] 增加 Anthropic guidance 渲染，不支持能力返回 200 message / SSE，而不是 500。
- [x] 补 mock 回归覆盖文本、工具、图片、SSE、错误和 guidance。
- [x] 跑专项回归、供应商回归、类型检查和空白检查。

### 本次不包含

- 不做 `messages -> responses`。
- 不把 server-side web search、MCP、computer use、code execution、image generation 等运行时能力靠字段转换伪造。
- 不把 Anthropic native 上游能力伪装成 OpenAI Responses。
- 不做旧非法映射运行时兼容迁移；如历史数据存在非法映射，由用户离线清理。

## 关联文档

- 设计文档：`docs/functions/AnthropicMessages转Chat协议转换设计.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- 协议矩阵计划：`docs/plans/计划-0062-协议互转矩阵与模型映射约束.md`
- 导入协议：`docs/functions/AI账户导入协议.md`
- 架构总览：`docs/architecture/架构总览.md`

## 执行拆解

- [x] 文档和矩阵先行。
- [x] 类型与 schema：后端 domain、zod、前端 contract、form type 全部支持 `sourceEndpointFamily=messages`。
- [x] 保存校验：source / upstream 协议组合按白名单校验，source model 池按协议族区分。
- [x] 路由识别：新增通用请求端点族解析，Anthropic `/v1/messages` 能参与账号模型映射命中。
- [x] Provider driver：OpenAI-compatible / GPT / DeepSeek / GLM 允许显式 `messages -> chat_completions` 映射生成 `/chat/completions` 上游 URL。
- [x] Bridge 请求侧：system、messages、content blocks、tools、tool_choice、采样和 metadata 映射完整。
- [x] Bridge 响应侧：Chat JSON / SSE 还原 Anthropic JSON / SSE，包含文本、工具、usage、finish reason 和 SSE error。
- [x] Guidance：扩展 Anthropic message / stream guidance 渲染。
- [x] 回归脚本：新增 `anthropic-openai-chat-bridge-mock-regression.ts` 和 `anthropic-openai-chat-gateway-mock-regression.ts`。
- [x] 验证并记录结果。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| Mock 回归 | Anthropic Messages -> Chat bridge helper | `pnpm --dir backend test:anthropic-openai-chat-bridge-mock` | JSON / SSE / tool / image / guidance 全通过 | 已通过 | 2026-06-25 通过；复查后补充覆盖 cache_control guidance、非流式 refusal、非法 Chat JSON error、SSE 尾部 usage |
| Mock 回归 | Anthropic Messages -> Chat 网关 E2E | `pnpm --dir backend test:anthropic-openai-chat-gateway-mock` | OpenAI-compatible / GPT / DeepSeek / GLM 通过显式映射承接 `/v1/messages`，输出 Anthropic JSON / SSE，unsupported 能力返回 guidance | 已通过 | 2026-06-25 通过；覆盖请求体改写、上游路径、headers、usage / audit 映射 |
| 真实联调 | Anthropic Messages -> OpenAI-compatible Chat | `pnpm --dir backend test:anthropic-openai-chat-real` | 真实上游下 Messages JSON / SSE、强制 function tool、unsupported guidance、usage / audit 通过 | 已通过 | 2026-06-25 使用 `https://vsllm.com`、source `claude-sonnet-4-6`、upstream `gpt-5.4-mini` 通过；可选图片用例在 `gpt-5.4-mini` 120 秒超时，记录为上游模型视觉路径待复测 |
| Mock 回归 | 模型映射保存校验 | `pnpm --dir backend test:account-model-mapping` | `messages -> chat_completions` 可保存；`messages -> responses` 被拒绝 | 已通过 | 2026-06-25 通过；覆盖保存、关系表、候选窗口、导入预览和 Messages source 模型池 |
| Mock 回归 | 草稿测试快照协议映射 | `pnpm --dir backend test:account-api-key-draft-activation` | 草稿测试任务记录读回后保留 `messages -> chat_completions` 映射 | 已通过 | 2026-06-25 通过 |
| 前端回归 | 账户保存 / 草稿测试 payload | `pnpm --dir frontend test:account-edit-save-flow` | 保存 payload 与草稿测试 payload 保留 `messages -> chat_completions` 协议维度 | 已通过 | 2026-06-25 通过 |
| 回归 | OpenAI -> Anthropic bridge | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 既有桥接不回归 | 已通过 | 2026-06-25 通过 |
| 回归 | 候选账号模型映射窗口 | `pnpm --dir backend test:gateway-dispatch-candidate-window` | `requestedEndpointFamily=messages` 不破坏候选扫描窗口和映射过滤 | 已通过 | 2026-06-25 通过 |
| 回归 | Responses -> Chat bridge | `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai` | GLM / DeepSeek 现有 Codex Responses bridge 不回归 | 已通过 | 2026-06-25 通过 |
| 回归 | 模型价格目录 | `pnpm --dir backend test:usage-pricing` | GPT / GLM / DeepSeek / OpenAI-compatible 价格和发布日期目录不回归 | 已通过 | 2026-06-25 通过 |
| 类型检查 | 后端类型检查 | `pnpm --dir backend typecheck` | 通过 | 已通过 | 2026-06-25 通过 |
| 类型检查 | 前端类型检查 | `pnpm --dir frontend typecheck` | 通过或仅剩既有无关问题 | 已通过 | 2026-06-25 通过 |
| 空白检查 | Git whitespace | `git diff --check` | 无新增空白错误 | 已通过 | 2026-06-25 通过；仅有 LF/CRLF 提示 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-25 | 进行中 | AI | 已创建计划；明确 `messages -> chat_completions` 只通过显式账号模型映射进入，`messages -> responses` 不做，unsupported native 能力走 guidance-first。 |
| 2026-06-25 | 进行中 | AI | 复查发现后端 `account-model-mappings` 已支持 Messages 来源协议白名单，但前端模型映射仍屏蔽 Messages，且模型选项 API 只支持 OpenAI 协议池。本轮先补 UI / 模型池 / 保存校验，确保显式 `messages -> chat_completions` 可配置且非法 `messages -> responses` 不可保存。 |
| 2026-06-25 | 进行中 | AI | 配置层已补齐：前端允许 OpenAI 协议账号选择左侧 Messages 并强制右侧 Chat；Anthropic 协议账号只允许右侧 Messages；模型选项 API 支持 Anthropic 协议模型池。响应侧 `messages -> chat_completions` bridge 仍需后续专项实现。 |
| 2026-06-25 | 已完成 | AI | 完成共享 bridge、四个 OpenAI 协议供应商驱动接入、Anthropic guidance 渲染、网关级 mock E2E、模型映射回归和前后端类型检查。 |
| 2026-06-25 | 已完成 | AI | 复查修复协议边界：`cache_control` 不再静默丢弃，非流式 Chat `refusal` 保真，非法 Chat JSON 转稳定 Anthropic error，SSE 等待尾部 usage；补齐草稿测试快照、custom model 协议白名单、前端保存 payload 和根脚本入口回归。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-25 | 只支持 `messages -> chat_completions`，禁止 `messages -> responses` | Chat Completions 和 Responses 的状态、工具、上下文恢复语义不同；反向合成 Responses 会扩大不确定性 | 模型映射白名单、UI、保存校验和驱动路由都必须按该方向限制 |
| 2026-06-25 | 不支持能力返回 guidance，不伪造执行器 | web/MCP/computer/code/image 等能力需要真实运行时，字段转换无法执行 | bridge 只处理函数工具和可等价 content；其他能力交给客户端 agent 修复 |

## 验收标准

- [x] 用户可在 OpenAI 协议账号模型映射中配置 `messages -> chat_completions`。
- [x] 非法方向 `messages -> responses` 无法通过 UI、保存 API 或导入预览。
- [x] OpenAI-compatible、GPT、DeepSeek、GLM Chat 上游都能在显式映射下承接 Anthropic `/v1/messages`。
- [x] JSON / SSE 响应形态符合 Anthropic Messages 客户端预期。
- [x] 工具调用、图片输入和 usage 映射有 mock 覆盖。
- [x] 不支持能力不会导致 500 或会话直接中断；会返回有目的的 agent guidance。
- [x] 相关专项回归和类型检查通过。

## 验证记录

- 2026-06-25 执行 `pnpm --dir backend test:account-model-mapping` 和根目录 `pnpm test:account-model-mapping`，通过；覆盖 `messages -> chat_completions` 保存、运行时映射命中、`messages -> responses` 和 `messages -> messages` 拒绝、Messages source 必须来自 Anthropic 协议模型池、custom model `messages/message_token_counting` 协议保真、导入预览非法方向拒绝。
- 2026-06-25 执行 `pnpm --dir backend test:anthropic-openai-chat-bridge-mock` 和根目录 `pnpm test:anthropic-openai-chat-bridge-mock`，通过；覆盖 helper 层请求转换、JSON / SSE 响应转换、工具、图片、SSE error、unsupported guidance、`cache_control` guidance、非流式 refusal、非法 Chat JSON error 和 SSE 尾部 usage。
- 2026-06-25 执行 `pnpm --dir backend test:anthropic-openai-chat-gateway-mock`，通过；覆盖 OpenAI-compatible / GPT / DeepSeek / GLM 显式 `messages -> chat_completions` 映射下的真实网关调度、上游 `/chat/completions` 路由、headers 清理、Anthropic JSON / SSE 输出、usage / audit 映射和 unsupported guidance。
- 2026-06-25 执行 `pnpm --dir backend test:account-api-key-draft-activation`，通过；覆盖草稿测试任务快照读回后保留 `messages -> chat_completions` 映射。
- 2026-06-25 执行 `pnpm --dir frontend test:account-edit-save-flow`，通过；覆盖账户保存 payload 与草稿测试 payload 保留 `messages -> chat_completions` 协议维度。
- 2026-06-25 执行 `pnpm --dir backend test:anthropic-openai-chat-real`，通过；真实上游为 `https://vsllm.com` 的 `gpt-5.4-mini`，覆盖 Messages JSON、Messages SSE、强制 function tool、unsupported `thinking` guidance、usage 和 audit。额外开启 `JUHE_REAL_ANTHROPIC_OPENAI_CHAT_RUN_IMAGE=1` 时，`gpt-5.4-mini` 图片路径 120 秒超时；`gemini-3.5-flash` 文本探针可用但本脚本基础 JSON 返回空文本，暂不作为图片真实验收模型。
- 2026-06-25 执行 `pnpm --dir frontend test:account-import-protocol`，通过；导入弹窗内置 Markdown 和正式协议文档均说明 `messages -> chat_completions` 与 `messages -> responses` 禁止方向。
- 2026-06-25 执行 `pnpm --dir backend test:gateway-dispatch-candidate-window`，通过；确认候选窗口和模型映射过滤不回归。
- 2026-06-25 执行 `pnpm --dir backend test:deepseek-gateway-mock-ai`、`pnpm --dir backend test:glm-gateway-mock-ai`，均通过；确认现有 Responses -> Chat bridge 和供应商能力边界不回归。
- 2026-06-25 执行 `pnpm --dir backend test:usage-pricing`，通过；确认模型价格 / 发布日期目录不回归。
- 2026-06-25 执行 `pnpm --dir backend typecheck`、`pnpm --dir frontend typecheck`、`pnpm --dir backend test:openai-anthropic-bridge-mock`，均通过。
- 2026-06-25 执行 `git diff --check`，通过；仅有 LF/CRLF 提示，无新增空白错误。

## 风险与注意事项

- Anthropic content block 类型扩展较快，新增 block 不能默认透传到 Chat；必须先明确等价语义。
- Chat 上游可能不支持图片、工具或某些采样字段；字段桥接成功不代表模型真实具备能力。供应商 / 模型不支持时按上游错误和客户端 guidance 处理，不在网关伪造能力。
- 如果账号 `supported_endpoint_modes` 没有 `chat_json` / `chat_sse`，即使命中映射也不能路由。

## 完成总结

- 完成时间：2026-06-25
- 实际完成内容：落地 `messages -> chat_completions` 显式协议桥接；补齐模型映射、候选账号筛选、OpenAI-compatible / GPT / DeepSeek / GLM 驱动、Anthropic guidance、前端配置入口、导入协议和专项回归。
- 主要改动位置：`backend/src/modules/providers/drivers/_shared/anthropic-openai-chat-bridge.ts`、`backend/src/modules/providers/drivers/{openai-compatible,gpt,deepseek,glm}/driver.ts`、`backend/src/modules/gateway/protocols/openai-v1/model-mapping.ts`、`backend/src/modules/gateway/request/error-response.ts`、`backend/src/storage/account-model-normalization.ts`、`backend/src/scripts/regression/anthropic-openai-chat-real-e2e.ts`、`frontend/src/views/accounts/AccountStrategySection.vue`、`docs/functions/AnthropicMessages转Chat协议转换设计.md`、`docs/develop/测试与验证说明.md`。
- 验证结果：专项 helper、网关 mock E2E、真实 `Messages -> Chat` 默认 E2E、模型映射、候选窗口、DeepSeek / GLM 供应商 mock、usage-pricing、前后端 typecheck、既有 OpenAI -> Anthropic bridge 和空白检查已通过；真实图片路径仍需可用视觉 Chat 模型复测。
