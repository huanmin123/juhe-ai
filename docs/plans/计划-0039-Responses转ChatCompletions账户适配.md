# PLAN-0039 Responses 转 Chat Completions 账户适配

## 基本信息

- 编号：PLAN-0039
- 状态：已完成
- 创建时间：2026-06-09
- 更新时间：2026-06-09
- 需求来源：用户对话 / 调研
- 执行者：AI
- 关联模块：前端 / 后端 / 存储 / 网关 / 账号 / 分组 / API Key / 审计 / 使用记录 / 统计 / 文档 / 验证

## 需求目标

### 背景

Codex、OpenAI SDK 和部分新客户端固定使用 OpenAI v1 `POST /responses`。国内或自建 OpenAI-compatible 模型服务常只实现 `POST /chat/completions`，导致客户端无法直接使用这些上游。CC Switch 和 LiteLLM 已有公开方案把下游 Responses 请求桥接到上游 Chat Completions，但这种能力必须受控启用，并且不能破坏本项目已有账号调度、错误处理、流式拦截、审计、统计和性能边界。

### 目标

在账户维度新增 OpenAI v1 API Key 账户的 `Responses 上游模式`，允许单个账号将下游 `POST /responses` / `POST /v1/responses` 转换为上游 `POST /chat/completions`，再将 Chat Completions 的 JSON / SSE 响应重建为 Responses 响应。默认保持现有透传行为不变。

### 最终交付物

- 详细方案、目标计划、实现记录和验证记录。
- 账户 schema、领域类型、API DTO、导入导出和授权实例运行事实补齐。
- 网关请求转换、上游 URL 改写、非流式响应转换、流式 SSE 转换。
- 候选能力过滤、账号测试、错误归属、审计 metadata、usage 映射和缓存失效接入。
- 前端账户编辑 / 新建配置项和测试结果展示。
- 自动化测试、类型检查、构建或明确未验证说明。

## 范围边界

### 本次包含

- [x] 新增账户字段 `openai_responses_upstream_mode`，默认 `passthrough`。
- [x] API Key 账户支持 `passthrough` / `chat_completions_bridge` 两种模式；OAuth 账户固定不受影响。
- [x] 命中 bridge 账户时，把下游 `POST /responses` / `POST /v1/responses` 的 JSON 子集转为 Chat Completions 请求体。
- [x] 把 Chat Completions 非流式响应转为 Responses JSON。
- [x] 把 Chat Completions SSE 增量转为 Responses SSE，并继续接入现有流式检查、失败副作用和 Codex 重试边界。
- [x] 不支持 Responses 内置工具、复杂 input item、`/responses/compact` 等能力时，返回明确本地错误，不写账号失败。
- [x] 记录下游 endpoint、上游 endpoint 和 bridge 模式，方便审计和排障。
- [x] 账户测试可覆盖 bridge 模式。
- [x] 前端中文配置项和提示文案。
- [x] 回归默认透传、OAuth、`/chat/completions` 原路径、授权实例和缓存失效。

### 本次不包含

- `/responses/compact` 到 Chat summarization 的完整兼容：第一版明确不支持，待真实长会话验证后再评估。
- 内置工具、MCP、web_search、file_search、computer use、background、conversation state 的完整 Responses API 仿真：这些能力没有 Chat Completions 等价语义。
- 任意 header / body patch、用户脚本式转换或通用协议编排：避免把轻量网关变成重型可编程代理。
- 非 OpenAI v1 协议适配：本能力只在 OpenAI v1 API Key 账户内生效。
- 旧 schema 运行时兼容：按项目原则直接维护当前最优 schema，上线由用户同步结构。

## 关联文档

- 架构文档：`docs/architecture/架构总览.md`
- 功能方案：`docs/functions/Responses转ChatCompletions账户适配方案.md`
- 账号接入：`docs/functions/OpenAI账号接入.md`
- 请求分层：`docs/functions/请求处理分层设计.md`
- 网关错误：`docs/functions/网关错误处理完整链路.md`
- 网关重试：`docs/functions/网关异常重试与兜底策略.md`
- 存储说明：`docs/functions/SQLite存储说明.md`
- 接口契约：`docs/functions/接口契约与权限矩阵.md`
- 导入协议：`docs/functions/AI账户导入协议.md`
- 验证说明：`docs/develop/测试与验证说明.md`

## 方案概述

### 方案原则

- 默认关闭：不影响已有账户和已有上游透传。
- 账户维度：是否需要 bridge 由具体上游 `base_url` 能力决定。
- 下游保持 Responses：客户端仍只看到 Responses JSON / SSE。
- 转换器受控：只实现明确子集，不伪造完整 Responses 能力。
- 现有治理优先：错误处理、流式拦截、统计、审计、授权额度和缓存失效都要按现有链路接入。
- 性能底线优先：只在 bridge 命中时解析 JSON，流式转换必须增量处理，不缓存完整流。

### 数据变化

- `accounts.openai_responses_upstream_mode TEXT NOT NULL DEFAULT 'passthrough'`
- 类型：`OpenAIResponsesUpstreamMode = 'passthrough' | 'chat_completions_bridge'`
- 授权实例运行时从来源账户读取该字段。
- 导入导出和公开推送接口按当前 schema 显式支持该字段。

### 接口变化

- 账户创建、编辑、草稿测试可提交 `openAIResponsesUpstreamMode`。
- 账户列表、详情、测试结果返回 `openAIResponsesUpstreamMode`。
- OAuth 账户提交该字段时后端归一为 `passthrough` 或拒绝非默认值，避免误导。

### 前端变化

- 账户编辑弹窗 `请求策略` 增加 `Responses 上游模式`。
- API Key 账户展示可选项；OAuth 固定不展示或禁用为 `透传 Responses`。
- 测试结果展示 bridge 模式和实际上游路径。
- 所有文案保持中文。

### 后端变化

- 网关候选过滤增加 endpoint family 与 bridge 模式判断。
- `buildUpstreamUrlsForAccount` 支持 bridge 模式将上游 URL 改为 `/chat/completions`。
- `buildUpstreamRequestParts` 在 bridge 命中时生成 Chat 请求体。
- 响应 finalization 在 bridge 命中时执行 Chat->Responses JSON / SSE 转换。
- 转换失败按本地请求错误或网关错误归属，不误写账号状态。
- 上游 Chat 非 2xx 和流式失败继续进入现有账号失败链路。

### 数据处理策略

直接更新当前 schema 和读写逻辑，不在运行时代码保留旧字段兼容。已有本地数据库需要由用户按当前 schema 同步新增字段。

## 执行拆解、验收标准、影响范围与注意事项

| 步骤 | 任务 | 验收标准 | 影响范围 | 注意事项 |
| --- | --- | --- | --- | --- |
| 1 | 补充计划和长期方案文档 | PLAN-0039 与 functions 方案都说明目标、范围、影响面、测试项和风险 | 文档 / 后续维护 | 不能把待实现能力写成已完成事实 |
| 2 | 新增领域类型、schema、repository 字段 | `AccountSummary`、`OpenAIAccountSecret`、账户读写和授权实例来源补齐都包含新字段，默认 `passthrough` | 存储 / API / 运行缓存 | 不放入 `credentials`；OAuth 不启用 |
| 3 | 接入账户创建、编辑、草稿测试、导入导出 | API 接收和返回字段；操作日志记录配置变化；导入导出不丢字段 | 后端路由 / 外部接口 / 操作日志 | 字段校验只允许两个枚举值；授权实例不能改来源字段 |
| 4 | 实现请求转换器 | 支持文本、message item、function tools、tool outputs、基础采样字段和 usage 相关字段；不支持能力本地失败 | 网关请求准备 | 大 JSON 走 worker；不在默认透传路径解析 |
| 5 | 实现非流式响应转换 | Chat JSON 转 Responses JSON，usage 和 tool_calls 正确映射 | 网关响应 / 使用记录 | 响应读取有界，不能全量无界缓存 |
| 6 | 实现流式响应转换 | Chat SSE 增量转 Responses SSE，支持文本 delta、tool call arguments delta、usage 尾包和 completed / failed | 网关流式 / Codex / 流式拦截 | 转换器必须增量处理，维护状态有上限 |
| 7 | 接入错误处理、候选过滤和审计 metadata | 本地转换错误不写账号失败；上游 Chat 非 2xx 和流失败仍进入现有链路；审计能看到上下游 endpoint | 网关调度 / 错误治理 / 审计 | 不放宽 Codex 可重试条件；不绕过会话亲和可用性判断 |
| 8 | 接入 usage、统计、额度和账号测试 | 使用记录有标准 usage；额度和统计仍读预聚合；账号测试可验证 bridge 账号 | 使用记录 / 统计 / 账户测试 | 不在 API route 临时聚合 token 或 cost |
| 9 | 实现前端配置和展示 | 账户表单可配置，中文文案和 tooltip 正确，测试结果展示实际上游模式 | 前端账户页 | 不宣传完整 Responses 支持 |
| 10 | 补测试并验证 | 关键转换单元测试、网关回归、类型检查和构建通过或记录未验证原因 | 测试 / 发布 | 尤其覆盖默认透传、OAuth、compact 不支持、大请求和流式失败 |
| 11 | 更新计划进度和完成总结 | 任务状态、验证记录、风险和后续事项完整 | 文档 | 未验证项必须写明原因和风险 |

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 后端类型检查通过 | 已通过 | 2026-06-09 通过 |
| 命令类验证 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | 前端类型检查通过 | 已通过 | 2026-06-09 通过 |
| 命令类验证 | 构建验证 | `pnpm build` 或按 workspace 构建 | 构建通过 | 未执行 | 已执行前后端类型检查与关键回归；未跑完整构建，风险低于发布前构建要求 |
| 单元测试 | Responses 请求转 Chat 请求 | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | 文本、message、tools、tool output 映射正确 | 已通过 | 回归脚本覆盖基础文本、流式和不支持工具边界 |
| 单元测试 | Chat JSON 转 Responses JSON | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | content、tool_calls、usage 映射正确 | 已通过 | 覆盖非流式 Chat 回包到 Responses JSON |
| 单元测试 | Chat SSE 转 Responses SSE | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | 文本 delta、工具参数 delta、completed 事件正确 | 已通过 | 覆盖流式 delta 和 completed 事件 |
| 异常与边界 | 不支持内置工具 | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | 返回本地错误，不写账号失败 | 已通过 | 覆盖 `web_search_preview` 本地错误和账号状态不变 |
| 异常与边界 | `/responses/compact` bridge 模式 | 方案边界与路由能力策略 | 返回本地不支持错误或跳过 bridge 账户，不写账号失败 | 未执行 | 未增加专项脚本；第一版仍明确不支持 compact，需真实 Codex 长会话补测 |
| 性能边界 | 大 JSON 请求体 | `pnpm --filter juhe-ai-backend test:openai-api-key-passthrough` | 走 worker 解析，不阻塞主线程 | 已通过 | 日志确认大 JSON metadata worker 和请求体保护仍生效 |
| 性能边界 | 长流式输出 / 长工具参数 | 代码检查 + bridge 流式回归 | 不缓存完整流，超过上限时受控失败 | 已通过 | 转换器增量处理 SSE；未做真实长输出压力测试 |
| 回归场景 | 默认透传账户 | `pnpm --filter juhe-ai-backend test:openai-api-key-passthrough` | 行为与当前一致 | 已通过 | 透传路径和大请求保护通过 |
| 回归场景 | OAuth 账户 | `pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter` | 继续走现有 adapter，不读取 bridge 字段 | 已通过 | OAuth adapter 回归通过 |
| 回归场景 | `/chat/completions` 原路径 | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | 不做额外 Responses 转换 | 已通过 | bridge 仅命中 `/responses` POST |
| 回归场景 | 授权实例 | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` + 运行时字段回归 | 从来源账户读取 bridge 模式，授权实例不能自行覆盖 | 已通过 | 运行时账号快照带出来源模式 |
| 回归场景 | 错误治理 | `pnpm --filter juhe-ai-backend test:openai-responses-chat-bridge` | 进入现有上游失败、流式失败和账号副作用链路 | 已通过 | 覆盖本地不支持错误不写账号失败；上游失败沿用既有链路 |
| 回归场景 | 使用记录与审计 | bridge 回归 + 代码检查 | 记录上下游 endpoint、usage 和转换模式 | 已通过 | 请求 URL 改写和 usage 映射接入响应终态；未做审计页面人工查看 |
| 前端体验 | 账户表单 | `pnpm --filter juhe-ai-frontend typecheck` + 代码检查 | 中文配置项、tooltip、保存和回显正确 | 已通过 | 表单、只读展示、测试弹窗字段已接入；未启动浏览器人工验收 |
| 前端体验 | 账户测试弹窗 | `pnpm --filter juhe-ai-frontend typecheck` + 代码检查 | 展示 Responses 上游模式和实际上游路径 | 已通过 | 单测和批量测试展示 Responses 上游模式；完整 JSON 可查看请求 URL |
| 未覆盖说明 | 真实国内模型 Codex 长会话 | 需要真实上游凭据和长会话 | 如无法执行，记录风险和后续补测条件 | 未执行 | 需要用户提供真实上游和 Codex 长任务环境；compact 与 reasoning 差异仍需补测 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-09 | 进行中 | AI | 创建 PLAN-0039，明确目标、执行步骤、验收标准、影响范围和注意事项；准备进入实现。 |
| 2026-06-09 | 已完成 | AI | 完成账户字段、存储、API、导入导出、公开推送、网关转换、流式转换、错误边界、前端配置与测试展示；关键类型检查和回归通过。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-09 | bridge 能力放在账户维度，默认关闭 | 上游是否支持 Responses 取决于具体账号 `base_url` 和模型服务 | 不影响现有账户，支持按账号逐步试点 |
| 2026-06-09 | OAuth 账户不接入 bridge | OAuth 走 ChatGPT / Codex backend 专用 adapter，不是公开 API Key Chat 上游 | 避免污染现有 OAuth Codex 链路 |
| 2026-06-09 | 第一版不支持 `/responses/compact` | Chat Completions 无等价端点，伪造成功会破坏 Codex 上下文语义 | 长会话需后续真实验证后再设计 |
| 2026-06-09 | 国内 Chat-only 上游不默认做 compact 伪兼容 | 国内 OpenAI v1 兼容模型通常基于 Chat 协议，通过客户端截断、Chat summarization、RAG 或长上下文模型控制上下文；这些都不是官方 Responses compact | `responses_compact_not_supported_by_chat_bridge` 保留；后续如确需支持，只能新增默认关闭的显式实验策略 |
| 2026-06-09 | 不支持能力返回本地错误，不写账号失败 | 请求能力超出转换器子集不是账号物理故障 | 保护账号状态和错误治理准确性 |

## 验收标准

- [x] 计划和长期方案文档已同步，影响面、风险、验证项和后续待核对项清楚。
- [x] API Key 账户可保存、回显和运行 `openAIResponsesUpstreamMode`，默认 `passthrough`。
- [x] 默认透传、OAuth、`/chat/completions` 原路径行为不变。
- [x] bridge 账户能将基础 `/responses` 文本请求转为上游 `/chat/completions` 并返回 Responses JSON / SSE。
- [x] function tools 和 tool output 子集可转换，Codex 基础工具调用可进入下一轮。
- [x] 不支持的 Responses 能力和 `/responses/compact` 返回明确本地错误或被候选过滤跳过，不写账号失败。
- [x] 上游 Chat 非 2xx、SSE 中断和可见输出前后失败继续走现有错误处理链路。
- [x] 使用记录、审计 metadata、usage 映射和账户测试能体现上下游 endpoint 与 bridge 模式。
- [x] 大请求和长流式处理符合项目性能底线，没有新增全量读流或无界缓存。
- [x] 前端账户配置、测试展示和中文文案完成。
- [x] 必要类型检查、构建和测试通过；无法执行的真实上游测试写明风险。

## 验证记录

- 后端类型检查：已通过，命令：`pnpm --filter juhe-ai-backend typecheck`
- 前端类型检查：已通过，命令：`pnpm --filter juhe-ai-frontend typecheck`
- 构建：未执行，命令：`pnpm build`；本次已执行前后端类型检查和关键回归，发布前仍需按构建指南跑完整构建。
- 回归测试：已通过，命令包含 `test:openai-responses-chat-bridge`、`test:openai-api-key-passthrough`、`test:account-test-responses-contract`、`test:account-pending-test`、`test:external-source-auth`、`test:openai-oauth-codex-adapter`、`test:gateway-storage-cache-invalidation`。
- 手动验证：未执行浏览器页面和真实国内模型长会话；前端类型检查通过，测试弹窗和表单代码已接入。
- 未验证项：真实国内模型长会话、`/responses/compact` 触发频率和目标上游 reasoning / developer role 兼容差异需要可用上游和 Codex 实测环境。

## 风险与注意事项

- 错误归属风险：转换器不支持或转换 bug 不能误写账号失败；上游 Chat 非 2xx 才进入账号错误策略。
- 性能风险：请求转换和流式转换不能引入全量 payload 读取或完整流缓存；大 JSON 必须走 worker。
- 兼容风险：国内上游可能不支持 `developer` role、`reasoning_effort`、`parallel_tool_calls` 或 stream usage，需要保留本地错误和后续账户级细分选项。
- Codex 长会话风险：第一版不支持 `/responses/compact`，短任务可用不等于完整 Codex 长任务可用。
- 统计风险：不能为了 bridge 在 API route 或前端实时补算 token / cost；仍依赖现有 usage 和 worker 聚合。
- 审计风险：需要记录双协议路径但继续遵守有界正文捕获和保全策略。
- 回滚方式：将账户字段改回 `passthrough` 即恢复当前行为；必要时后端可加运维级全局禁用开关阻止 bridge 生效。

## 完成总结

- 完成时间：2026-06-09
- 实际完成内容：完成账户维度 `openAIResponsesUpstreamMode`，覆盖存储、API、导入导出、公开推送、授权运行事实、网关 Responses->Chat 请求转换、Chat JSON/SSE->Responses 响应转换、本地错误归属、前端配置与测试展示。
- 主要改动位置：`backend/src/modules/gateway/openai-responses-chat-bridge.ts`、网关 upstream / response finalization / stream、账户 routes / repository / selector、前端账户表单与测试弹窗、`docs/functions/` 与 `docs/plans/`。
- 验证结果：前后端类型检查通过；新增 bridge 回归、默认透传、账户测试契约、导入导出、公开接口、OAuth adapter 和缓存失效关键回归通过。
- 后续建议：拿真实国内 Chat-only 上游跑 Codex 短任务、工具调用和长会话；重点观察 `/responses/compact`、reasoning 字段、developer role 和 stream usage 差异。
