# PLAN-0012 OpenAI OAuth 透传适配器打磨

## 基本信息

- 编号：PLAN-0012
- 状态：已完成
- 创建时间：2026-05-08
- 更新时间：2026-05-08
- 需求来源：用户要求复查主流中转与 `F:\temp-project` 下实现差异后，先写清楚本项目需要做什么，再开始细化落地。
- 执行者：Codex
- 关联模块：后端 / 网关 / OpenAI OAuth / 使用记录 / 原始审计日志 / 文档 / 验证

## 需求目标

- 背景：当前 OpenAI OAuth 账户虽然转向 `chatgpt.com/backend-api/codex`，但请求体和 Header 仍较多复用普通 OpenAI API Key 透传逻辑，容易形成“公开 Responses API 字段 + Codex backend 头”的混合形态。
- 目标：把 OpenAI OAuth 账号从普通 OpenAI API Key 透传中拆成内部 `openai_oauth_codex` 适配语义，收敛请求体、Header、会话标识和错误处理，降低异常请求噪声。
- 交付物：计划文档、架构边界说明、网关适配器代码、回归脚本、验证记录。

## 范围边界

### 本次包含

- [x] 新增 OpenAI OAuth / Codex 内部适配器边界，不改变客户端 `/v1` 入口和前端配置项。
- [x] OAuth / Codex 请求体归一化：JSON 对象校验、`instructions` 校验与兜底、`store=false`、非 compact `stream=true`、compact 删除 `store/stream`、移除 Codex backend 不支持或高风险字段。
- [x] OAuth / Codex Header 收敛：从黑名单复制改为白名单 + 默认值，强制精确 `content-type: application/json`，按 endpoint 设置 `accept`。
- [x] 上游会话标识隔离：对 `session_id`、`conversation_id` 和 `prompt_cache_key` 混入本地系统账户、API Key、分组与账号维度后重写。
- [x] 对非 JSON 或 `instructions` 类型异常的 OAuth 请求本地返回 400，不再把异常 body 送到 Codex backend。
- [x] 补充面向适配器的回归脚本，覆盖请求体、Header、会话隔离和 compact 行为。

### 本次不包含

- 不做绕过平台限制、伪装身份或规避风控策略；本计划只处理协议正确性、多租户隔离、兼容性和异常流量收敛。
- 不引入前端复杂开关，例如 `codex_cli_only` 或高级 Header 透传配置；第一阶段保持轻量。
- 不实现 WebSocket Codex 续链缓存，也不改写 `previous_response_id`；该字段需要独立设计上下文缓存后再处理。
- 不大改后台主动探测和 OAuth 用量快照调度；本次仅在文档风险中记录，后续可单独计划收敛主动请求预算。
- 不保证能解释既有账号封禁原因；没有上游侧审计数据时只能降低当前链路中的异常形态。

## 关联文档

- 架构文档：`docs/architecture/架构总览.md`
- 阶段计划：`docs/plans/第一阶段计划.md`
- 调研文档：`docs/functions/OpenAI OAuth透传细节统计与比较.md`
- 既有透传定位：`docs/functions/中转透传机制调研与定位修正.md`
- 验证说明：`docs/develop/运行与验证说明.md`

## 方案概述

- 方案原则：OAuth 账号不是“API Key 的另一种凭据”，而是内部 Codex backend 适配器；公开 OpenAI Responses API 的合法字段，不等于 Codex backend 都应原样接受。
- 数据变化：不新增数据库字段，不改变账号、分组、API Key 或使用记录 schema。
- 接口变化：客户端仍访问 `/v1/responses` 和 `/v1/responses/compact`；OAuth 账号仍只允许这两个 endpoint。
- 后端变化：网关构造上游请求时按账号类型分支；API Key 账号保留原有透传策略，OAuth 账号走专用 body/header/session normalizer。
- 错误变化：OAuth 账号遇到非 JSON body、非对象 body、`instructions` 非字符串时，网关返回 OpenAI 兼容 400 错误。
- 观测变化：审计日志记录的是归一化后的上游请求，有助于排查实际送往 Codex backend 的协议形态。

## 执行拆解

- [x] 复核现有网关入口、上游请求构造、OAuth Codex URL 限制和会话亲和实现。
- [x] 新建本计划并更新计划索引。
- [x] 更新架构边界说明，明确 OAuth / Codex 是内部 adapter。
- [x] 新增 OAuth / Codex adapter：请求体归一化、Header allowlist、会话隔离。
- [x] 将网关上游请求构造切换到 adapter，并补本地 400 错误处理。
- [x] 新增回归脚本并运行验证。
- [x] 更新计划验证记录和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | OAuth Codex adapter 回归 | `pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter` | 请求体、Header、会话隔离、compact 行为均通过断言 | 已通过 | 2026-05-08 通过 |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | TypeScript 无错误 | 已通过 | 2026-05-08 通过 |
| 功能主流程 | OAuth `/v1/responses` 请求体归一化 | 构造含 `metadata`、`temperature`、`store=true`、`stream=false` 的请求 | 上游 body 删除不支持字段，`store=false`、`stream=true`，`instructions` 为字符串 | 已通过 | 回归脚本覆盖 |
| 功能主流程 | OAuth `/v1/responses/compact` 请求体归一化 | 构造 compact 请求 | 上游 body 删除 `store`、`stream` 和不支持字段 | 已通过 | 回归脚本覆盖 |
| 功能主流程 | Header 收敛 | 构造包含 cookie、x-forwarded、OpenAI SDK UA 的请求头 | OAuth 上游只保留白名单，强制 `content-type: application/json`，默认 Codex 头齐全 | 已通过 | 回归脚本覆盖 |
| 异常与边界 | 非 JSON body | OAuth 账号处理无效 JSON | 本地 400，不请求上游 | 已通过 | 回归脚本覆盖构造层 |
| 异常与边界 | `instructions` 类型错误 | `instructions` 为对象或数组 | 本地 400，不请求上游 | 已通过 | 回归脚本覆盖 |
| 回归场景 | API Key 账号透传 | 使用 API Key 账号构造相同请求 | 保持原有 raw body / header 策略，不走 OAuth Codex adapter | 已通过 | 回归脚本覆盖 |
| 未覆盖说明 | 真实 OpenAI OAuth 上游请求 | 需要真实可用 OAuth 账户与明确测试窗口 | 不在无凭据环境强行请求上游 | 不适用 | 本地只做协议构造验证 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-05-08 | 进行中 | Codex | 已完成对 `sub2api_source`、`new-api`、`CLIProxyAPI` 与本项目现状的差异复核，确认先落 P0/P1：adapter、body normalize、header allowlist、session isolation、本地 400。 |
| 2026-05-08 | 已完成 | Codex | 已落地 `openai-oauth-codex-adapter`、接入网关上游请求构造、补充回归脚本，并通过 adapter 回归与后端类型检查。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-05-08 | OAuth 账号按 `openai_oauth_codex` 内部适配器处理 | OAuth 上游是 ChatGPT / Codex backend，不应复用公开 API Key 全字段透传策略 | API Key 账号行为保持，OAuth 账号请求形态更接近 Codex 客户端 |
| 2026-05-08 | Header 使用 allowlist + 默认值 | 黑名单复制容易把 SDK、代理、浏览器或部署环境噪声带给上游 | 减少异常 header，强制精确 `content-type` |
| 2026-05-08 | 会话标识混入本地调用方边界后重写 | 多用户使用相同 `session_id` / `prompt_cache_key` 时，上游可能看到跨用户碰撞 | 牺牲跨 API Key 共享缓存，换取多租户隔离 |
| 2026-05-08 | 不在本次改写 `previous_response_id` | 该字段语义依赖上游响应对象或连接态缓存，盲目 hash 会破坏续链 | 先保留原语义，后续单独设计 |

## 验收标准

- [x] OAuth 账号上游请求构造不再使用普通透传黑名单复制策略。
- [x] OAuth `/responses` 上游 body 固定 `store=false`、`stream=true`，删除已确认不适合 Codex backend 的字段。
- [x] OAuth `/responses/compact` 上游 body 删除 `store/stream` 和不支持字段。
- [x] OAuth 上游 Header 强制精确 `content-type: application/json`，默认 Codex 关键头齐全，危险头不透传。
- [x] `session_id`、`conversation_id`、`prompt_cache_key` 被本地隔离，两个 API Key 使用相同原始 session 时不会得到相同上游标识。
- [x] 非 JSON body 和 `instructions` 类型错误本地返回 400。
- [x] 回归脚本和后端类型检查完成，未验证项有明确说明。

## 验证记录

- OAuth Codex adapter 回归：已执行通过，命令：`pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter`
- 后端类型检查：已执行通过，命令：`pnpm --filter juhe-ai-backend typecheck`
- 真实上游请求：不适用，当前计划不主动消耗真实 OAuth 账号额度。

## 风险与注意事项

- OAuth 非 compact 请求强制 `stream=true` 后，非流式客户端可能收到 SSE；这与 Codex backend 兼容优先的策略一致，但后续如要完整兼容非流式客户端，需要增加 SSE 到 JSON 的本地聚合转换。
- `previous_response_id` 仍按客户端原值保留；如果真实问题集中在续链语义，需要新增 WebSocket / HTTP 上下文缓存设计。
- Header 默认值不是“伪装策略”，只是缺省兼容值；真实 Codex 客户端的 `originator`、`version`、`user-agent` 允许在白名单内保留。
- 后台 OAuth 用量快照、账号质量探测和冷却复测仍会产生真实上游请求；如封禁风险继续，需要单独计划收敛主动请求预算和频率。

## 完成总结

- 完成时间：2026-05-08
- 实际完成内容：新增 OAuth Codex 专用 adapter，OAuth 账号上游请求现在会做 body normalize、Header allowlist、上游 session/cache 标识隔离和本地 400 校验；API Key 账号保留原有透传行为。
- 主要改动位置：`backend/src/modules/gateway/openai-oauth-codex-adapter.ts`、`backend/src/modules/gateway/openai-gateway-upstream.ts`、`backend/src/modules/gateway/openai-gateway.routes.ts`、`backend/src/scripts/openai-oauth-codex-adapter-regression.ts`、`docs/architecture/架构总览.md`。
- 验证结果：`pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter` 通过；`pnpm --filter juhe-ai-backend typecheck` 通过。
- 后续建议：单独建立计划收敛 OAuth 用量快照、账号质量探测和冷却复测的主动请求预算；如需支持非流式 OAuth 客户端，再补 SSE 聚合到 JSON 的转换层；如需稳定续链，再设计 `previous_response_id` 上下文缓存。
