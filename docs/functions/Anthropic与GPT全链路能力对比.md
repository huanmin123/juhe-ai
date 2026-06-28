# Anthropic 与 GPT 全链路能力对比

## 结论

Anthropic API Key 当前已经具备可用的原生中转闭环：账户创建、分组绑定、本地 API Key 认证、`/v1/messages` JSON / SSE、`/v1/messages/count_tokens`、本地 `/v1/models`、Claude Code 客户端画像识别、响应语义检查、账户测试、模型目录、使用记录明细和成本估算都已接入。

和 GPT 全链路相比，Anthropic 当前剩余缺口不在“能不能发请求”，而在 GPT 长期沉淀的增强层：

- OpenAI-compatible 客户端到 Anthropic Messages 的协议互转已进入 PLAN-0058，目标覆盖 Chat JSON、Chat SSE、Responses JSON、Responses SSE 四类入口，设计见 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md)。
- Anthropic usage 明细已进入 input / output / cache read / cache write / 1h cache / thinking token；统计预聚合表、授权消耗和大盘卡片仍只按现有通用维度展示。
- Claude Code 画像已识别，但没有 GPT Codex 那套 turn 级失败避让、专用可重试 SSE 兜底和订阅额度快照。

本轮已补齐 Anthropic native 的协议感知本地错误渲染、顶层 `model` 账户映射，以及多 API Key 的 Key 级故障隔离；这些能力需要继续保留对应回归测试。

这些缺口需要分层处理，不能把 GPT 的 Codex / OAuth 机制直接套到 Anthropic API Key。

## 对比范围

本对比以当前 GPT 闭环为基准，覆盖：

- 供应商和协议档案
- 账户创建、编辑、导入、导出
- 账户测试和后台复测
- 分组、授权、API Key 路由
- 网关认证、候选筛选、调度、切号
- 上游请求准备、模型映射、客户端画像
- 返回侧 JSON / SSE、错误兜底、响应检查
- usage、成本、统计、审计
- 前端表单、测试弹窗、模型和策略入口

## 能力矩阵

| 链路 | GPT / OpenAI 当前能力 | Anthropic 当前能力 | 差距判断 |
| --- | --- | --- | --- |
| 供应商协议档案 | `openai/v1`、`gpt/openai_v1`，区分通用 OpenAI-compatible 与 GPT 专属能力 | `anthropic/v1` 独立档案，当前官方 Anthropic API Key | 已对齐 |
| 账户类型 | GPT API Key、GPT OAuth；通用 OpenAI 只 API Key | Anthropic 只 API Key | OAuth / Setup Token / Claude Code token 属于明确不做，不是当前缺口 |
| Base URL | API Key 可填 OpenAI-compatible 根地址 | API Key 可填 Anthropic-compatible 根地址 | 已对齐 |
| 多 API Key | 单账户多 key、轮询 / 加权、Key 级故障隔离 | 多 key 可保存并参与轮询 / 加权，已启用 Key 级运行态隔离 | 已对齐 |
| 客户端兼容能力 | GPT API Key 同时具备 OpenAI 标准 / Codex Responses，OAuth 固定 Codex；请求侧兼容能力决定是否改写 | Anthropic API Key 具备 Anthropic 原生 / Claude Code 请求能力，不暴露 OpenAI 账号兼容切换 | 正确，不应照搬 |
| OpenAI-compatible 客户端 | 支持 `/v1/chat/completions`、`/v1/responses` 等 OpenAI v1 入口 | PLAN-0058 目标支持 OpenAI Chat / Responses 到 Anthropic Messages 的显式桥接，覆盖 JSON / SSE 四类入口 | 进行中；不能隐藏在 Anthropic raw passthrough 中 |
| Anthropic native 客户端 | GPT 不适用 | 支持 `Authorization` 和 `x-api-key` 作为本地认证，支持 `/v1/messages` / `/v1/models` | 已落地 |
| Claude Code 画像 | Codex 有 turn metadata、Responses SSE、专用失败兜底和 turn 级避让 | Claude Code 通过显式 header 或官方 CLI 多信号识别，只影响画像与审计 | 可选增强，不应复制 Codex 语义 |
| 账户测试 | 按账户能力选择 Chat / Responses / Codex 请求，复用真实网关链路 | 用 `/v1/messages` 最小请求，支持 Anthropic 模型目录 | 主链路已对齐 |
| 后台冷却复测 | 复用账户测试链路，恢复临时不可用 / 限流账号 | 复用同一测试服务，Anthropic 协议已进入测试服务 | 基础对齐 |
| 接口能力限制 | `chat_json/chat_sse/responses_json/responses_sse` | `messages_json/messages_sse/message_token_counting` | 已对齐 |
| 分组与授权 | 分组按供应商隔离，授权实例独立运行态和用量归属 | 同样按供应商进入分组，账户协议档案保留真实上游能力 | 已对齐 |
| API Key 多分组 fallback | 同档案号池可 fallback，跨供应商依赖模型路由计划 | Anthropic 同档案可复用现有 fallback；跨供应商同样依赖模型路由计划 | 和 GPT 同级，跨供应商不是 Anthropic 单点缺口 |
| 模型限制 | 候选账号按请求 `model` 过滤 | 已复用候选过滤 | 已对齐 |
| 模型映射 | OpenAI adapter 会改写请求体 `model` | Anthropic native 请求会按账户模型映射改写顶层 `model`，保留其他原生字段 | 已对齐 |
| 上游请求头 | 替换上游 `Authorization`，过滤本地敏感头；OpenAI Beta 只透传客户端值 | 替换上游 `x-api-key`，默认补 `anthropic-version`，只透传客户端 `anthropic-beta` | 已对齐 |
| 路径归一 | OpenAI 根路径和 `/v1` 归一到上游 `/v1/*` | Anthropic 根路径和 `/v1` 归一到 `/v1/messages` 等 | 已对齐 |
| 非流式响应语义 | OpenAI Chat / Responses JSON 提取输出、usage、错误 | Anthropic JSON 提取 text、thinking、tool_use 摘要、usage、error object | 主链路已对齐 |
| 流式响应语义 | OpenAI / Codex SSE 解析、首 token、usage、错误事件 | Anthropic SSE 解析 `message_*`、`content_block_*`、`event:error`，`message_stop` 后主动结束 | 主链路已对齐 |
| 本地错误响应 | OpenAI-compatible JSON，Codex 流式可写 `response.failed` | Anthropic native 本地错误按 `{type:"error", error:{...}}` 渲染，流式已提交后的本地错误使用 Anthropic `event: error` | 已补齐 |
| 流式失败兜底 | Codex Responses SSE 有专用 `upstream_retryable_error` 兜底 | Anthropic 不伪造 Codex 事件；未提交前可服务端切号，已输出后不拼接 | 当前正确；如需 Anthropic 客户端可读失败事件需单独设计 |
| 账户错误处理策略 | 状态码 / 错误码 / 文案只作为策略输入，确认后改状态 | Anthropic 错误类型也进入语义帧和策略输入，不直接判死 | 已对齐 |
| 会话亲和 | 支持 header/body 会话字段、迁移流量、Codex turn 级特殊避让 | 通用会话字段可复用；Claude Code 没有 turn 级避让 | 基础可用，Claude Code 专项可选 |
| OAuth 额度快照 | GPT OAuth 记录 Codex 5h / 7d 被动快照 | Anthropic API Key 无等价字段；OAuth 不做 | 不应补到 API Key |
| usage 解析 | input / output / cache read / 图片 / 音频等通用字段 | input / output / cache read / cache write / 1h cache / thinking token 已进入使用记录明细 | 明细已补齐；统计聚合扩维另立范围 |
| 成本估算 | OpenAI/GPT 多模型价格、缓存读、图片音频等 | Anthropic input/output/cache read/cache write/1h cache 已进入成本估算；thinking token 作为 output 分解字段展示，不单独重复计费 | 主成本已对齐 |
| 原始审计 | 请求 / 响应 / header / body 截断、响应检查元数据 | Anthropic 复用审计，记录画像、上游响应和语义检查 | 基础对齐 |
| 前端账户表单 | API Key / OAuth 分支、客户端兼容能力只读展示、接口能力、模型限制、模型映射、代理、时间计划、策略 | API Key 分支、Anthropic 原生 / Claude Code 能力展示、接口能力、模型限制、模型映射入口可用 | 已对齐 |
| 前端测试弹窗 | 展示测试请求形态、模型、后台任务、完整 JSON | Anthropic 展示“测试请求形态 / 实际请求形态：Anthropic 原生请求” | 已修正 |

## 建议优先级

### 已补齐：稳定供应商主链路

- 协议感知本地错误渲染：Anthropic native 请求在本地认证、调度失败、端点不支持、无可用账号、请求体非法等本地错误场景返回 Anthropic error shape，不影响 OpenAI-compatible 客户端错误结构。
- Anthropic 模型映射：native `model` 字段按账户映射改写，其他 raw body 字段保持原样，并沿用大 body JSON 解析边界。
- Anthropic API Key 池故障隔离：`isAccountApiKeyPoolIsolationEnabled` 已扩展到 Anthropic API Key，Key fingerprint、冷却、复测和账号可用性判断按单 Key 运行态处理。

### P1：影响 Anthropic 作为稳定供应商的能力

1. Anthropic 统计聚合扩维
   - 使用记录明细已保存 `usage_semantic`、cache write、1h cache 和 thinking token，成本拆分和前端使用记录页已可展示。
   - 仍未把这些新维度扩展到 stats staged / window / summary 表、授权消耗报表和统计大盘卡片。
   - 后续扩维必须继续由 worker 增量聚合，不能在 API 路由临时扫描明细或把 cache write 并入普通 input tokens。

### P2：看产品方向再决定

1. Claude Code 专项稳定性增强
   - 可参考 Codex turn 级避让，但不能直接复用 Codex `response.failed` 事件。
   - 如果真实 Claude Code 客户端需要稳定续会，可基于 `x-claude-code-session-id`、`metadata.user_id`、`container`、`context_management` 等明确字段建立会话亲和和失败避让。

2. Anthropic-compatible 第三方供应商档案
   - DeepSeek、GLM、Kimi 等应各自建供应商 / 协议档案、模型目录和价格目录。
   - 不能并入官方 `anthropic` 账号池。

3. Anthropic 上游 Models 同步
   - 当前 `/v1/models` 返回本地目录。
   - 如需同步官方或兼容网关模型目录，必须做大小限制、分页 / 流式读取和管理员触发，不在普通请求链路调用上游。

### 进行中：OpenAI -> Anthropic 显式桥接 adapter

- 目标：让非 Claude / 非 Anthropic SDK 的 OpenAI-compatible 客户端用 `/v1/chat/completions` 或 `/v1/responses` 调 Claude / Anthropic Messages 模型。
- 范围：Chat JSON、Chat SSE、Responses JSON、Responses SSE 四类入口；system/developer、function tools、tool_choice、tool_result、image、thinking、cache、stream、usage、错误渲染都必须单独测试。
- 边界：不隐藏在官方 Anthropic API Key raw passthrough 中，不把 Anthropic 账号真实 endpoint modes 伪装成 OpenAI 能力，不支持字段必须受控拒绝或明确降级记录。
- 跟踪：PLAN-0058，设计见 [OpenAI 到 Anthropic Messages 协议桥接设计](OpenAI到Anthropic协议桥接设计.md)。

### 不建议补到 Anthropic API Key 路径

- Anthropic OAuth、Setup Token、Claude Code token 账号。
- Claude Code OAuth cloaking、TLS 指纹、CCH signing。
- API Key 路径默认注入 Claude Code 专属 `anthropic-beta` 或 `User-Agent`。
- 按 `authentication_error`、`rate_limit_error`、HTTP 403 / 429 / 5xx 直接写死账号状态。
- 把国产 Anthropic-compatible Base URL 标记成官方 Claude 直连。

## 当前小修记录

- 账户测试弹窗已经统一为“测试请求形态 / 实际请求形态”。
- 账户策略区域已经将“客户端兼容”改为账号可承接能力展示，Anthropic 显示 Anthropic 原生和 Claude Code。
- Anthropic native 本地错误响应已经按 Anthropic error shape 渲染。
- Anthropic native 模型映射已经支持只改写顶层 `model`。
- Anthropic 多 API Key 已启用 Key 级故障隔离，单个 Key 失败不会直接冷却整个账户。
- Anthropic 使用记录明细已经保存 cache write、1h cache、thinking tokens 和 `usage_semantic`，成本估算已按 Anthropic 分列 usage 口径计算，`count_tokens` 404/405 能力缺失不会污染普通 Messages 账号健康。

## 后续验证建议

建议至少保留这些回归：

- Anthropic 本地认证失败、分组无账号、端点能力不匹配、模型不匹配时返回 Anthropic error shape。
- Anthropic 非流式 / 流式 usage 覆盖 cache creation、cache read、thinking tokens。
- Anthropic 多 API Key 中单 key 失败后只摘除当前 key，不直接冷却整个账户；全部 key 不可用时才跳过账户。
- Anthropic 模型映射只改写 `model`，不破坏 `messages`、`system`、`tools`、`thinking`、`stream`、`metadata` 等原生字段。
- OpenAI/GPT 回归确认错误结构、模型映射、Codex 兜底和 OAuth 额度快照不受 Anthropic 扩展影响。
