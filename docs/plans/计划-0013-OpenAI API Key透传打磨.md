# PLAN-0013 OpenAI API Key 透传打磨

## 基本信息

- 编号：PLAN-0013
- 状态：已完成
- 创建时间：2026-05-08
- 更新时间：2026-05-08
- 需求来源：用户继续要求重点复查 API Key 和透传，比较 `F:\temp-project\中转` 下主流中转项目，并把需要做的写到文档后开始落地。
- 执行者：Codex
- 关联模块：前端 / 后端 / 网关 / OpenAI API Key / 文档 / 验证

## 需求目标

- 背景：API Key 账号当前 body 透传较好，但 Header 仍偏黑名单复制，容易把客户端组织 / 项目头、SDK 内部头、tracing 头、部署平台头和本地代理链路噪声带到上游。
- 目标：保持 API Key raw body 真透传优势，同时把 Header 透传收敛到账号池适合的边界：过滤危险头和噪声头，不把 OpenAI 组织 / 项目 / Beta 这类用户难以判断的字段暴露到普通 API Key 账户表单。
- 交付物：细节统计比较文档、计划文档、后端 Header 策略、API Key 表单纠偏、回归脚本和验证记录。

## 范围边界

### 本次包含

- [x] 对比本项目与 `F:\temp-project\中转` 下 `one-api`、`new-api`、Portkey、LiteLLM、`sub2api_source` 等主流中转项目的 API Key Header / body 处理。
- [x] 新增 API Key 透传细节统计与比较文档，明确“他们哪里好、我们哪里好、最终怎么取舍”。
- [x] 保留 API Key 透传开启时的 raw body 原样转发。
- [x] 后端过滤本地认证、代理链路、hop-by-hop、cookie、压缩、SDK / tracing / 部署平台噪声和 OAuth / Codex 专用头。
- [x] 后端不再默认相信客户端 `OpenAI-Organization` / `OpenAI-Project`，也不从账号凭据生成这两个头。
- [x] 保留客户端 `OpenAI-Beta` 功能语义，但不允许账号凭据覆盖。
- [x] 前端 API Key 账户表单撤回 `OpenAI 组织`、`OpenAI 项目`、`OpenAI Beta`，避免要求用户猜测不可生产的字段。
- [x] 补充 API Key passthrough 回归脚本。

### 本次不包含

- 不做绕过平台限制、规避风控或伪装真实身份的策略。
- 不改变 OAuth / Codex adapter 已有边界；OAuth 账号继续走 `openai_oauth_codex`。
- 不新增数据库 schema 字段；不新增 OpenAI 组织、项目和 Beta 的账号凭据配置。
- 不实现跨账号自动重试下的 `Idempotency-Key` 重写或命名空间化；当前仅保留公开 API 幂等语义。
- 不实现完整 multipart / 文件上传专项测试；如后续重点使用上传类接口，再单独补 raw body + boundary 验证。

## 关联文档

- 架构文档：`docs/architecture/架构总览.md`
- 功能设计：`docs/functions/核心功能设计.md`
- OpenAI 账号接入：`docs/functions/OpenAI账号接入.md`
- 透传定位：`docs/functions/中转透传机制调研与定位修正.md`
- API Key 细节调研：`docs/functions/OpenAI API Key透传细节统计与比较.md`
- OAuth 细节调研：`docs/functions/OpenAI OAuth透传细节统计与比较.md`

## 方案概述

- 方案原则：API Key 账号走公开 OpenAI API 语义；请求体尽量原样，Header 透传必须尊重账号池、多租户和协议安全边界。
- 数据变化：不改 schema；API Key 账号凭据只保存 `api_key` 和 `base_url` 等当前必要字段，不新增 `openai_organization`、`openai_project`、`openai_beta`。
- 接口变化：管理 / 用户账户创建和编辑接口仍使用原 `credentials` 字段；不新增 API。
- 前端变化：OpenAI API Key 配置区只保留 API Key 和 Base URL，撤回组织、项目和 Beta 输入项。
- 后端变化：`buildUpstreamHeaders()` 增加 Header 过滤函数；过滤客户端组织 / 项目头，不读取账号凭据里的同类残留键。
- 验证变化：新增 `test:openai-api-key-passthrough` 脚本，覆盖 raw body、Header 过滤和残留账号凭据不生效。

## 执行拆解

- [x] 复核本项目 API Key raw body 与 Header 构造。
- [x] 复核参考项目的 Header 策略和 OpenAI provider options。
- [x] 新增 API Key 透传统计比较文档并更新功能文档索引。
- [x] 更新架构总览、核心功能设计和 OpenAI 账号接入文档。
- [x] 实现后端 Header 过滤，并撤回账号级 OpenAI Header 覆盖。
- [x] 撤回前端 API Key 账户可选 OpenAI Header 配置。
- [x] 新增 API Key passthrough 回归脚本和 package script。
- [x] 执行回归脚本、OAuth 回归和类型检查。
- [x] 更新验证记录与完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | API Key passthrough 回归 | `pnpm --filter juhe-ai-backend test:openai-api-key-passthrough` | raw body、Header 过滤、残留账号凭据不生效均通过断言 | 已通过 | 2026-05-08 通过 |
| 命令类验证 | OAuth Codex adapter 回归 | `pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter` | OAuth adapter 行为不被 API Key 调整影响 | 已通过 | 2026-05-08 通过 |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 后端 TypeScript 无错误 | 已通过 | 2026-05-08 通过 |
| 命令类验证 | 全项目类型检查 | `pnpm typecheck` | 前后端 TypeScript 无错误 | 已通过 | 2026-05-08 通过 |
| 功能主流程 | API Key raw body | 构造 raw JSON 请求 | 上游 body 与 `rawBody` 字节一致 | 已通过 | 回归脚本覆盖 |
| 功能主流程 | Header 过滤 | 构造包含本地认证、代理链路、tracing、SDK 噪声的请求 | 上游 Header 不包含这些字段 | 已通过 | 回归脚本覆盖 |
| 功能主流程 | OpenAI 账号上下文头边界 | 客户端传组织 / 项目，历史账号凭据残留组织、项目、Beta | 组织 / 项目不上游；Beta 保留客户端显式值，不被账号凭据覆盖 | 已通过 | 回归脚本覆盖 |
| 回归场景 | `OpenAI-Beta` 客户端语义 | 客户端传 `OpenAI-Beta`，账号未配置 | 上游保留客户端值 | 已通过 | 回归脚本覆盖 |
| 回归场景 | OAuth 账号 | OAuth 请求仍走专用 adapter | Header allowlist、body normalize、session isolation 保持 | 已通过 | OAuth 回归覆盖 |
| 前端体验 | API Key 账户表单 | 打开创建 / 编辑 API Key 账户弹窗 | 只展示 API Key 和 Base URL，不展示组织、项目和 Beta | 已通过 | `pnpm typecheck` 覆盖字段移除和类型；未做浏览器截图 |
| 未覆盖说明 | 真实上游 OpenAI 调用 | 无真实凭据或不主动消耗账号 | 不做真实请求；通过构造测试验证协议策略 | 不适用 | 后续可在安全测试窗口补测 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-05-08 | 进行中 | Codex | 已复核本项目与 `F:\temp-project\中转` 参考实现，确认 API Key body 继续 raw passthrough，Header 策略吸收成熟中转的安全边界。 |
| 2026-05-08 | 进行中 | Codex | 已新增文档、后端 Header 过滤、前端 API Key 可选 OpenAI Header 字段和回归脚本，等待执行验证。 |
| 2026-05-08 | 进行中 | Codex | 复查后确认组织 / 项目不能由系统生产，Beta 也不应要求用户猜测，已撤回前端字段和账号凭据覆盖逻辑。 |
| 2026-05-08 | 已完成 | Codex | API Key 回归、OAuth 回归、后端类型检查和全项目类型检查均已通过，计划收口。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-05-08 | API Key body 保持 raw passthrough | 这是当前实现比许多传统 relay 更接近公开 API 真透传的优势 | 不破坏文件、图片、大上下文和客户端原始 JSON 字节 |
| 2026-05-08 | 默认不透传客户端 `OpenAI-Organization` / `OpenAI-Project`，也不从账号凭据生成 | 这两个头属于上游账号归属上下文，不能由系统生产，普通用户也通常不知道该填什么 | API Key 表单保持轻量，历史残留凭据不影响上游请求 |
| 2026-05-08 | `OpenAI-Beta` 客户端可透传但账号不可覆盖 | Beta 头常是公开 API 功能语义，调用方知道自己使用什么能力时才应传入；账号侧不猜测 | 保留客户端语义，避免错误固定 Beta |
| 2026-05-08 | 暂保留 `Idempotency-Key` | 它是公开 API 幂等语义 | 后续跨账号自动重试前必须再次评估 |

## 验收标准

- [x] API Key 透传细节统计与比较文档已新增并进入功能文档索引。
- [x] API Key raw body 透传逻辑保留。
- [x] API Key Header 过滤覆盖本地认证、代理链路、协议危险头和常见 SDK / tracing / 部署平台噪声。
- [x] `OpenAI-Organization` / `OpenAI-Project` 不从客户端透传，也不从账号配置写入。
- [x] API Key 账户表单不暴露 OpenAI 组织、项目和 Beta。
- [x] API Key passthrough 回归通过。
- [x] OAuth adapter 回归通过。
- [x] TypeScript 类型检查通过。

## 验证记录

- API Key passthrough 回归：已执行通过，命令：`pnpm --filter juhe-ai-backend test:openai-api-key-passthrough`
- OAuth Codex adapter 回归：已执行通过，命令：`pnpm --filter juhe-ai-backend test:openai-oauth-codex-adapter`
- 后端类型检查：已执行通过，命令：`pnpm --filter juhe-ai-backend typecheck`
- 全项目类型检查：已执行通过，命令：`pnpm typecheck`
- 真实上游请求：不适用，本计划不主动消耗真实 OpenAI 账号额度。

## 风险与注意事项

- 客户端如果依赖直接传 `OpenAI-Organization` / `OpenAI-Project` 临时切换组织或项目，将不再生效；第一阶段不支持在账号池中转里切换上游组织 / 项目上下文。
- `x-openai-*` 默认过滤可能影响少数未文档化客户端扩展头；如真实公开 API 功能需要，再按明确字段纳入 allowlist。
- `Idempotency-Key` 当前保留；跨账号自动重试或错误切换前必须重新设计，避免同一幂等键在不同上游账号间造成语义混乱。
- Header 过滤只解决协议噪声，不解释既有账号封禁原因；上游账号状态仍可能受出口 IP、请求频率、请求内容、后台探测和账号自身状态影响。

## 完成总结

- 完成时间：2026-05-08
- 实际完成内容：完成 API Key 透传细节比较文档；保留 raw body 透传；收敛 Header 过滤策略；撤回账号级 OpenAI 组织、项目和 Beta 配置；补充前端表单纠偏和回归脚本。
- 主要改动位置：`backend/src/modules/gateway/upstream/request.ts`、`backend/src/scripts/regression/openai-api-key-passthrough-regression.ts`、`frontend/src/views/accounts/AccountApiKeySection.vue`、`docs/functions/OpenAI API Key透传细节统计与比较.md`。
- 验证结果：API Key 回归、OAuth 回归、后端类型检查和全项目类型检查均通过。
- 后续建议：如后续实现跨账号自动重试，先单独设计 `Idempotency-Key` 策略；如重点支持文件上传或图片编辑，再补 multipart raw body 边界测试。
