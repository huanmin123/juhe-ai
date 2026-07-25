# PLAN-20260623T020406000Z DeepSeek 与 GLM Claude Code 兼容接入

## 基本信息

- 计划 ID：PLAN-20260623T020406000Z
- 状态：进行中
- 创建时间：2026-06-23
- 更新时间：2026-07-14
- 需求来源：用户对话
- 执行者：AI / 维护者
- 关联模块：前端 / 后端 / 存储 / 网关 / 供应商驱动 / 导入导出 / 使用记录 / 文档 / 验证

## 需求目标

- 将 `deepseek` 和 `glm` 的 Claude Code / Anthropic Messages 兼容能力落成独立协议档案，不混入 OpenAI Chat 档案或官方 Anthropic 账号池。
- 让 DeepSeek 通过 `profile_deepseek_anthropic_v1`，GLM 通过 `profile_glm_coding_anthropic_v1`，分别支持账户创建、导入导出、供应商默认分组内的精确候选过滤、网关调度、模型测试和统计记录；分组本身不绑定协议档案。
- 保持当前最优方案优先，不为旧账户结构、旧协议档案或旧客户端兼容分支保留运行时回退。

## 范围边界

### 本次包含

- [x] 新增 DeepSeek Anthropic v1 协议档案、账户类型和 frontend fallback；复用单一 DeepSeek 供应商默认分组。
- [x] 新增 GLM Coding Anthropic v1 协议档案、账户类型和 frontend fallback；分组继续只表达 GLM 供应商账号池。
- [-] 按 profile 创建或绑定 DeepSeek / GLM Claude Code 独立默认分组的旧任务已撤销，由现有 `providerCode + model`、账户 profile 和 endpoint mode 候选过滤取代。
- [x] 统一 Anthropic 协议 driver、credentials driver 和 endpoint modes 默认值。
- [x] 统一按 profile 记 usage semantic，避免同一 providerCode 下 OpenAI / Anthropic 混淆。
- [x] 补齐导入导出协议、存储默认 seed 和中文表单文案。
- [ ] 补齐 mock / 真实网关回归，覆盖 `/v1/messages`、`/v1/models`、headers、base URL 拼接和 count_tokens 默认禁用。
- [ ] 补齐 Claude Code / Anthropic 客户端 smoke，至少覆盖 DeepSeek 和 GLM 各一条。

### 本次不包含

- DeepSeek / GLM 原生 Responses 兼容。
- FIM、Prefix beta、MCP、Files、Batches、code execution 等未验证能力。
- 任何旧结构兼容分支、双写分支或启动迁移逻辑。

## 关联文档

- DeepSeek 账号接入：`docs/functions/DeepSeek账号接入.md`
- 智谱 GLM 账号接入：`docs/functions/智谱GLM账号接入.md`
- AI 账户导入协议：`docs/functions/AI账户导入协议.md`
- SQLite 存储说明：`docs/functions/SQLite存储说明.md`
- 架构总览：`docs/architecture/架构总览.md`
- 统一模型能力与计费抽象：`docs/functions/统一模型能力与计费抽象设计.md`

## 执行拆解

- [x] 补后端协议常量、profile seed 和 driver 支持；default group 沿用供应商单分组，不按 profile 新建。
- [x] 补前端 fallback provider、账户类型、导入协议和表单文案。
- [x] 补存储 seed 和导入协议文档。
- [ ] 补 mock/回归脚本，覆盖 DeepSeek / GLM 的 Anthropic Messages 请求与默认 endpoint modes。
- [ ] 跑后端 typecheck、前端 typecheck 和目标回归脚本。
- [ ] 根据验证结果补充文档中的实测记录和风险边界。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 |
| --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 未执行 |
| 命令类验证 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | 通过 | 未执行 |
| 回归 | DeepSeek Anthropic mock | `pnpm --filter juhe-ai-backend test:deepseek-gateway-mock-ai` | `/v1/messages` 可调度，`message_token_counting` 默认不开放 | 未执行 |
| 回归 | GLM Anthropic mock | `pnpm --filter juhe-ai-backend test:glm-gateway-mock-ai` | `/v1/messages` 可调度，`message_token_counting` 默认不开放 | 未执行 |
| 前端 | 导入协议 | `pnpm --filter juhe-ai-frontend test:account-import-protocol` | DeepSeek / GLM Claude Code 示例可 round-trip | 未执行 |

## 验收标准

- DeepSeek Claude Code 和 GLM Claude Code 都有独立协议档案和账户创建入口；各供应商使用自己的单一默认分组，分组不绑定 profile。
- 默认 endpoint modes 仅包含 Messages JSON / SSE，不默认启用 Count Tokens。
- 使用记录、统计和导入导出都能按 profile 维度区分 OpenAI / Anthropic 档案。
- 文档、前端和后端对外口径一致。

## 验证记录

- 2026-07-14：方案边界修订，撤销按 profile 创建默认分组；DeepSeek / GLM 的 Anthropic-compatible 账户继续使用各自供应商默认分组，候选由现有模型目录、账户 profile 和 endpoint mode 精确过滤。该记录只修订目标契约，既有 mock / 真实网关验证仍待执行。
