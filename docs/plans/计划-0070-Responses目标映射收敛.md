# PLAN-0070 Responses 目标映射收敛

## 基本信息

- 编号：PLAN-0070
- 状态：已完成
- 创建时间：2026-06-26
- 更新时间：2026-06-26
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 前端 / 网关 / 模型映射 / 协议桥接 / Mock AI / 文档 / 验证

## 需求目标

- 背景：前一轮协议矩阵把 `chat_completions -> responses` 作为“真实 Responses 上游例外”放开，并实现了有限桥接。但 OpenAI Responses 包含服务端状态、`previous_response_id`、compact、hosted tools 等 Chat Completions 无法完整表达的语义，该方向不能作为长期无感兼容能力。
- 目标：收敛 `responses` 作为右侧上游协议的规则：任何非 Responses 来源都不能桥接到 Responses；只有 `responses -> responses` 原生直连允许配置，且账号必须真实具备 Responses endpoint mode。
- 交付物：长期文档修订、前后端协议矩阵收紧、driver 过度桥接移除、旧映射运行时不命中、mockai / 矩阵回归验证。

## 范围边界

### 本次包含

- [x] 更新协议桥接框架、模型映射设计、导入协议、架构总览和 PLAN-0062 的当前口径。
- [x] 后端协议矩阵删除 `chat_completions -> responses`。
- [x] 前端协议矩阵删除 `chat_completions -> responses`，保存前直接拒绝。
- [x] GPT / OpenAI-compatible driver 移除 Chat -> Responses bridge 分支。
- [x] 运行时模型映射解析不再返回 `chat_completions -> responses`，避免旧配置继续生效。
- [x] 回归脚本改为验证 Chat / Gemini / Anthropic 到 Responses 均被拒绝。
- [x] 运行 mockai 和类型检查。

### 本次不包含

- 不做 Chat Completions 到 Responses 的“降级兼容”或隐藏转换。
- 不做 Gemini / Anthropic 到 Responses 的合成。
- 不使用真实账户替代 mockai；真实账户只在 mock 全通过后按需抽样。
- 不把历史旧映射自动迁移或运行时兼容；如已有旧数据，按当前 schema 离线清理。

## 关联文档

- 协议桥接框架：`docs/functions/协议桥接框架设计.md`
- 模型映射设计：`docs/functions/自定义模型与模型映射设计.md`
- 导入协议：`docs/functions/AI账户导入协议.md`
- 长期矩阵计划：`docs/plans/计划-0062-协议互转矩阵与模型映射约束.md`
- 测试说明：`docs/develop/测试与验证说明.md`

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-26 | 禁止 `chat_completions -> responses` | Chat 请求无法完整表达 Responses 服务端状态、compact、hosted tools 和 previous response 语义；有限桥接会让用户误认为完全兼容 | 仅保留 `responses -> responses` 原生直连，其他来源不能选择右侧 Responses |
| 2026-06-26 | 删除已实现的 Chat -> Responses bridge | 该实现只是有限字段转换，不符合“客户端无感”的长期目标 | driver 分支、矩阵和测试统一收敛到拒绝 |

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| Mock 回归 | 模型映射保存校验 | `pnpm --dir backend test:account-model-mapping` | `chat_completions -> responses`、`messages -> responses`、`generate_content -> responses` 均拒绝；`responses -> responses` 原生直连允许 | 已通过 | 通过；覆盖旧 Chat -> Responses 映射运行时不命中 |
| 前端回归 | 账号保存前协议矩阵 | `pnpm --dir frontend test:account-edit-save-flow` | 前端 helper 和保存校验拒绝 Chat -> Responses | 已通过 | 通过 |
| 导入协议回归 | 导入协议文档与 Markdown | `pnpm --dir frontend test:account-import-protocol` | 文档不再宣称 Chat -> Responses 可配置 | 已通过 | 通过 |
| 网关回归 | OpenAI-compatible E2E | `pnpm --dir backend test:openai-compatible-gateway-e2e` | Chat 直连仍成功，Chat -> Responses 映射创建被拒绝 | 已通过 | 通过 |
| 整体协议 mock | 已有协议桥接主路径 | `pnpm --dir backend test:gemini-gateway-mock-ai`、`pnpm --dir backend test:openai-anthropic-bridge-mock`、`pnpm --dir backend test:anthropic-openai-chat-bridge-mock` | Gemini / Anthropic / Responses->Chat 等允许方向不回归 | 已通过 | 三条命令均通过，并补跑 DeepSeek / GLM / Anthropic gateway mock |
| 复杂交叉 mock | OpenAI / Anthropic / Gemini 协议互转矩阵 | `pnpm --dir backend test:protocol-cross-matrix-mock-ai` | 允许方向可运行，禁止 Responses 目标方向受控拒绝，不可保真组合 guidance-first 且不上游 | 已通过 | 通过；同时固定 `Responses -> Chat` 非流式下游返回 Responses JSON |
| 类型检查 | 后端 / 前端 | `pnpm --dir backend typecheck`、`pnpm --dir frontend typecheck` | 删除 bridge 后无类型错误 | 已通过 | 通过 |

## 验收标准

- [x] 后端保存、导入、草稿测试不能保存 `chat_completions -> responses`。
- [x] 前端模型映射 UI / 保存校验不能选择或保存 `chat_completions -> responses`。
- [x] driver 不再包含 Chat -> Responses bridge 分支。
- [x] 运行时旧映射不会命中 Responses 上游。
- [x] `responses -> responses` 原生直连仍可配置。
- [x] `responses -> chat_completions`、OpenAI / Anthropic / Gemini 已允许方向不回归。

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-26 | 进行中 | AI | 用户确认 Chat Completions 无法完整转换为 Responses，要求创建目标并整体检查。 |
| 2026-06-26 | 已完成 | AI | 已完成矩阵、driver、运行时 resolver、文档和 mockai 回归收敛；未使用真实账户。 |
| 2026-06-26 | 已完成 | AI | 补充复杂交叉 mockai 回归，覆盖 OpenAI / Anthropic / Gemini 多协议成功路径、Responses 目标禁止方向和不可保真 guidance；修正 `Responses -> Chat` 非流式下游不能原样暴露 Chat SSE。 |

## 验证记录

- `pnpm --dir backend test:account-model-mapping`：通过。
- `pnpm --dir frontend test:account-edit-save-flow`：通过。
- `pnpm --dir frontend test:account-import-protocol`：通过。
- `pnpm --dir backend test:openai-compatible-gateway-e2e`：通过。
- `pnpm --dir backend test:gemini-gateway-mock-ai`：通过。
- `pnpm --dir backend test:openai-anthropic-bridge-mock`：通过。
- `pnpm --dir backend test:anthropic-openai-chat-bridge-mock`：通过。
- `pnpm --dir backend test:deepseek-gateway-mock-ai`：通过。
- `pnpm --dir backend test:glm-gateway-mock-ai`：通过。
- `pnpm --dir backend test:anthropic-gateway-mock-ai`：通过。
- `pnpm --dir backend test:protocol-cross-matrix-mock-ai`：通过。
- `pnpm --dir backend test:account-model-filter`：通过。
- `pnpm --dir backend typecheck`：通过。
- `pnpm --dir frontend typecheck`：通过。

## 完成总结

- 已把 `chat_completions -> responses` 从前后端协议矩阵、GPT / OpenAI-compatible driver 和运行时模型映射解析中移除。
- 已删除 Chat -> Responses bridge helper，避免后续误用有限转换当成完整兼容。
- 已同步长期文档、导入协议、计划文档和测试手册，右侧 `responses` 现在只允许 `responses -> responses` 原生直连。
- mockai 和类型检查已验证现有 Responses -> Chat、OpenAI -> Anthropic、Anthropic -> Chat、Gemini 正反向允许路径不回归。
- 已增加复杂交叉 mockai 入口，后续调整协议矩阵或共享桥接层时必须把 `test:protocol-cross-matrix-mock-ai` 纳入回归。
