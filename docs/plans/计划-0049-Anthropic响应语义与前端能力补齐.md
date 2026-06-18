# PLAN-0049 Anthropic 响应语义与前端能力补齐

## 基本信息

- 编号：PLAN-0049
- 状态：已完成
- 创建时间：2026-06-18
- 需求来源：用户要求在 Anthropic 调研和设计后一次性落地文档、计划、实现和真实 mock 检查
- 执行者：Codex
- 关联模块：后端 / 前端 / 网关 / Anthropic / 响应检查策略 / 模型价格 / 使用记录 / 文档 / 验证

## 需求目标

补齐 Anthropic 官方 API Key 直连在当前 GPT/OpenAI 闭环中缺失的能力：原生 Messages JSON / SSE 响应语义检查、Claude Code 客户端画像边界、Anthropic 模型价格目录、账户前端字段、响应检查策略前端协议选择、导入协议示例和真实 mock 回归。

本需求要求一次性完成，不拆阶段；OAuth / Setup Token / Claude Code token 账号链路继续不做。

## 范围边界

本次做：

- Anthropic Messages JSON / SSE 映射到统一响应语义帧。
- Anthropic 流式检查器接入通用流式管道，识别 `event: error`、`message_stop`、文本 delta 和 usage。
- 非流式 JSON 检查按账号协议选择 OpenAI 或 Anthropic 适配器。
- 响应检查策略支持 `protocolCode = anthropic`，前端可按协议选择供应商。
- Anthropic API Key 账户前端可配置 `Anthropic-Version`、`Anthropic-Beta`，导出和导入协议同步。
- `anthropic-beta` 按客户端 header 和账号配置合并去重，不注入 Claude Code 专属 beta。
- Anthropic 模型价格目录进入 `providerCode = anthropic`，成本估算覆盖 input / output / cache read。
- mock 回归覆盖真实网关路径、Claude Code header 过滤、响应策略换号、JSON error 和 SSE `event:error`。

本次不做：

- Anthropic OAuth、Setup Token、Claude Code token 账号类型。
- OpenAI Chat / Responses 到 Anthropic Messages 的自动互转。
- Bedrock、Vertex、Foundry 等云平台 Claude 账号。
- DeepSeek / GLM / Kimi 等 Anthropic-compatible 供应商落地。
- cache write、1h cache、thinking token 的使用记录 schema 扩展；当前只在文档和价格数据中保留目标口径，不在主统计中臆造字段。

## 关联文档

- [Anthropic 账号接入](../functions/Anthropic账号接入.md)
- [Claude Code 客户端画像兼容设计](../functions/ClaudeCode客户端画像兼容设计.md)
- [响应语义检查管线设计](../functions/响应语义检查管线设计.md)
- [模型价格与用量统计口径](../functions/模型价格与用量统计口径.md)
- [测试与验证说明](../develop/测试与验证说明.md)
- [架构总览](../architecture/架构总览.md)

## 执行拆解

- [x] 梳理 Anthropic 与当前 GPT/OpenAI 能力差距。
- [x] 新增 Anthropic JSON / SSE 响应语义适配器。
- [x] 新增 Anthropic 流式检查器并接入通用流式管道。
- [x] 非流式 JSON 响应检查按账号协议选择适配器。
- [x] 响应检查策略支持 OpenAI / Anthropic 协议维度。
- [x] 前端响应检查策略增加协议选择和同协议供应商过滤。
- [x] 前端 Anthropic API Key 增加版本头、beta 头和非官方 Base URL 提醒。
- [x] 账户凭据导入、导出和编辑回填支持 Anthropic 头字段。
- [x] Anthropic 模型价格目录接入模型目录和成本估算。
- [x] mock 回归覆盖 Anthropic 响应策略换号、JSON error、SSE error 和 beta 合并。
- [x] 更新长期文档、计划索引和验证说明。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果 |
| --- | --- | --- | --- | --- | --- |
| 类型检查 | 后端 TypeScript | `pnpm --filter juhe-ai-backend typecheck` | 无类型错误 | 已通过 | 已通过 |
| 类型检查 | 前端 Vue / TypeScript | `pnpm --filter juhe-ai-frontend typecheck` | 无类型错误 | 已通过 | 已通过 |
| 单元回归 | 响应检查策略 | `pnpm --filter juhe-ai-backend test:response-inspection-policy` | OpenAI 与 Anthropic 语义帧、默认规则和策略匹配通过 | 已通过 | 已通过 |
| 单元回归 | usage / pricing | `pnpm --filter juhe-ai-backend test:usage-pricing` | Anthropic input / output / cache read 成本可估算，OpenAI 回归不退化 | 已通过 | 已通过 |
| 客户端画像 | Claude Code | `pnpm --filter juhe-ai-backend test:claude-code-client-strategy` | 显式 header 命中，普通 Anthropic 和 OpenAI 隔离 | 已通过 | 已通过 |
| 真实 mock | Anthropic 网关链路 | `pnpm --filter juhe-ai-backend test:anthropic-gateway-mock-ai` | 临时 SQLite、mock 上游和本地网关全链路通过 | 已通过 | 已通过 |
| 真实联网 | Anthropic-compatible 上游 E2E | `pnpm --filter juhe-ai-backend test:anthropic-real-gateway-e2e` | `/v1/models`、本地网关选号、Messages JSON / SSE、Count Tokens、工具调用和并发请求按真实模型通过 | 已部分执行 / 上游阻断 | `https://vsllm.com` `/v1/models` 返回 69 个模型；临时账号写入、runtime 选号和本地 `/v1/models` 已通过；`claude-sonnet-4-6` 与 `auto-free` 曾返回真实内容，后续因上游额度不足返回 HTTP 403，完整覆盖被上游账户状态阻断 |
| 可选抓包 | 官方 Claude Code CLI mock 捕获 | `JUHE_RUN_CLAUDE_CODE_CLI_MOCK=1 pnpm --filter juhe-ai-backend test:anthropic-gateway-mock-ai` | CLI 通过本地网关命中 `/v1/messages`，请求头和认证形态可被捕获 | 环境阻断 | 本机可通过 `npx @anthropic-ai/claude-code@latest --version` 输出 2.1.181，但 `--print` 在当前 Windows 非交互环境未发出本地请求并超时；确定性画像回归仍已通过 |

## 进度记录

### 2026-06-18

- 完成 Anthropic 官方 API Key 直连能力对 GPT/OpenAI 当前能力的差距复查。
- 明确 Anthropic 账户类型当前只做 API Key；OAuth / Setup Token / Claude Code token 不进入本目标。
- Claude Code 继续作为下游客户端画像处理，不新增账户类型。
- 补齐 Anthropic 响应语义适配器、流式检查器、响应策略协议维度、前端字段、价格目录和导入协议。
- 将响应检查策略新建默认动作从短期避让账号改为重试但不避让，避免用户刚建规则时默认写入账号运行态。
- 完成真实 mock 回归：Messages JSON / SSE、Count Tokens、本地 Models、Claude Code header 过滤、beta 合并、污染文本换号、JSON error 换号、SSE event:error 换号和 OpenAI 分组隔离。
- 补齐 `message_token_counting` 端点能力枚举，避免 Count Tokens 在账号能力过滤层被遗漏。
- 真实 Anthropic SSE 联调发现 Anthropic `message_stop` 后第三方兼容网关可能保持连接，流式管道已在协议终止事件写出后主动结束下游响应，避免客户端等待上游 EOF。
- 使用用户提供的 Anthropic-compatible 上游完成真实联网尝试：`/v1/models`、临时账号落库、runtime 选号和本地模型目录已通过；真实 Messages 覆盖最终被上游余额不足 / HTTP 403 阻断，不能作为本地网关失败归因。
- 增加官方 Claude Code CLI 可选 mock 捕获脚本；当前本机 CLI 能输出版本但非交互 `--print` 未命中本地网关，记录为环境阻断，不把 CLI OAuth / token 账号链路并入本目标。

## 验收标准

- Anthropic API Key 账户仍只走 `anthropic/v1` 原生 Messages 协议，不复用 OpenAI 字段路径。
- Anthropic JSON / SSE 响应可进入统一响应语义检查，策略按 `protocolCode + providerCode` 隔离。
- 错误类型和 HTTP 状态码只作为语义帧、审计和策略输入，不在协议适配器中直接写账号死亡状态。
- Claude Code 只通过显式本地 header 识别为客户端画像，header 不透传上游。
- Anthropic 模型目录和成本估算不污染 OpenAI 聚合目录。
- 前端账户创建、编辑、导入和响应检查策略入口都有对应 Anthropic 能力。
- 必要类型检查和 mock 回归全部通过。

## 验证记录

已执行并通过：

```powershell
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-frontend typecheck
pnpm --filter juhe-ai-backend test:response-inspection-policy
pnpm --filter juhe-ai-backend test:usage-pricing
pnpm --filter juhe-ai-backend test:claude-code-client-strategy
pnpm --filter juhe-ai-backend test:anthropic-gateway-mock-ai
```

已执行真实 Anthropic-compatible 联网验证：

```powershell
pnpm --filter juhe-ai-backend test:anthropic-real-gateway-e2e
```

验证记录：使用用户提供的 `https://vsllm.com` 与本机临时网关，`/v1/models` 成功返回 69 个模型；临时 Anthropic 账号、分组、本地 API Key、runtime 选号、本地 `/v1/models` 均通过。`claude-sonnet-4-6` 和 `auto-free` 曾返回真实模型内容；后续完整 E2E 在 Messages 请求处收到上游 `HTTP 403` 额度不足错误，真实模型全覆盖被上游账户状态阻断。该错误只作为真实联调诊断和账户错误策略输入，不能在协议适配器里硬编码为账号死亡状态。

已执行官方 Claude Code CLI 捕获尝试：本机 `npx @anthropic-ai/claude-code@latest --version` 可输出 `2.1.181`，但 `--print` 在当前 Windows 非交互环境未向临时本地网关发出请求并超时。默认 mock 回归和 `test:claude-code-client-strategy` 已覆盖本项目可控的 Claude Code 画像、协议隔离和 header 过滤。

## 风险与注意事项

- `cache_creation_input_tokens`、1h cache 和 `thinking_tokens` 需要 usage schema 和统计 worker 扩展后才能进入主统计；当前不在请求链路临时补字段。
- Anthropic 模型价格是静态快照，后续应按官方模型和价格变更更新，不把第三方 Anthropic-compatible 价格套到官方 Anthropic。
- 响应检查策略仍提供短期避让动作供明确配置使用；默认新建动作已改为不避让账号的重试。用户配置避让动作时应把匹配条件写得足够精确。
- 非官方 Anthropic-compatible Base URL 允许用于测试或后续独立供应商接入，但不能宣传为官方 Claude 直连；真实联调出现的余额、额度和模型不可用错误必须按上游账户状态解释，不能倒推为本地协议适配失败。

## 完成总结

本次 Anthropic API Key 直连能力已补齐到 mock 可完整覆盖、真实联网可启动验证的闭环：原生请求、返回语义、策略换号、前端配置、模型价格、Count Tokens、Claude Code 客户端画像和文档计划均已落地。OAuth / Claude Code token 账号链路继续保持不支持，避免在未真实验证前把订阅账号形态并入中转主链路。
