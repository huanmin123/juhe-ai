# 整体架构设计

## 产品目标

`juhe-ai` 要做成一个轻量、易扩展的中转管理系统。整体模块先设计完整，但每期只落必要能力，避免一开始就变成重型系统。

## 技术架构

- 前端：Vue 3 + TypeScript + Ant Design Vue
- 后端：Node.js + TypeScript
- API 风格：REST 优先，后续需要实时状态时再补 WebSocket 或 SSE
- 对外中转协议：统一兼容 OpenAI `/v1` 协议；客户端 Base URL 使用本服务的 `/v1` 地址，API Key 使用本地网关密钥
- 存储：SQLite，适合单人自用、低复杂度、低运维成本的场景
- 敏感字段：OAuth token、上游 API Key、本地网关 API Key 加密存储；账户列表不展示上游凭据，编辑弹窗可查看和修改完整凭据；API Key 管理页仍完整展示本地网关 Key 方便自用复制

## 模块总览

```mermaid
flowchart LR
  APIKey["API Key"] --> Group["分组"]
  Account["账户"] --> Group
  Account --> Provider["供应商"]
  Account --> Proxy["代理"]
  Account --> ErrorPolicy["错误策略"]
  Gateway["OpenAI 兼容中转网关"] --> APIKey
  Gateway --> Usage["使用记录"]
  Settings["系统设置"] --> Gateway
  Settings --> Account
```

## 核心关系

- 供应商定义账户创建方式，例如 OpenAI 支持 OAuth 和 API Key；对外请求协议仍统一收敛为 OpenAI 兼容协议。
- 账户属于某个供应商，并选择一种账户类型。
- 分组归属于一个供应商；账户主动选择归属分组，分组汇总同供应商账户并代表一组可调度资源。
- API Key 绑定分组，请求进入后只能使用该分组内的账户。
- 代理独立管理，账户按需绑定代理。
- 使用记录记录 API Key、分组、账户、接口、供应商、模型、状态、IP、首 token、总耗时和错误；上游请求每一次尝试都会记录，包含重试过程中的失败。
- 系统设置保存全局默认值，不直接替代账号级配置。
- 流熔断只作用于流式响应异常：首包前请求超时、超过空闲超时没有新数据或流被异常中断时，按账号累计失败并临时不可调用。

## SQLite 存储策略

这个项目当前只给个人使用，不需要追求极限并发和分布式能力，所以 SQLite 是默认存储方案。

设计原则：

- 数据库文件默认放在 `backend/data/juhe-ai.sqlite3`
- 运行配置读取项目内 `backend/.env`；数据库文件可通过 `JUHE_AI_DATABASE_PATH` 指定，不要求系统环境变量
- 使用 WAL 模式提升本地读写体验
- 不引入复杂 ORM，先用简单 repository 封装 SQL
- 敏感字段加密后存储；自用后台接口按页面需要返回完整密钥
- 后续如果真要迁移 PostgreSQL，只替换 repository 层

当前表：

- `providers`
- `accounts`
- `groups`
- `group_accounts`
- `api_keys`
- `proxy_profiles`
- `error_policies`
- `usage_records`
- `system_settings`

## 供应商管理

供应商不是简单字符串，而是能力定义。不同供应商可以有不同的账户创建方式、字段 schema 和测试逻辑。

建议字段：

- `id`：供应商 ID
- `code`：稳定编码，例如 `openai`
- `name`：展示名称
- `enabled`：是否启用
- `base_url`：供应商默认上游地址，例如 OpenAI 固定为 `https://api.openai.com/v1`，不放入系统设置
- `account_types`：支持的账户类型定义
- `capabilities`：支持的能力，例如 responses、chat、models、stream
- `created_at` / `updated_at`

第一期内置：

- `openai`：支持 `oauth` 和 `api_key`

供应商模型目录：

- 模型价格应归属于供应商，而不是散落在网关代码里。
- 第一阶段内置 OpenAI 模型价格快照，字段采用 LiteLLM / `model-price-repo` 的语义，token 单价以 USD/token 存储，接口展示时换算为 USD/1M tokens。
- 供应商页提供“查看模型”入口，展示模型版本、输入/输出/缓存读写/图片价格、上下文窗口和能力标签。
- 网关成本估算复用供应商模型目录；未命中模型时只记录 token，不猜测成本。

## 账户管理

账户是上游凭据和调度配置的承载对象。

建议字段：

- `id`
- `provider_code`：供应商稳定编码，例如 `openai`
- `name`
- `type`：`oauth` 或 `api_key`
- `status`：`active`、`disabled`、`error`、`rate_limited`、`temporary_unavailable`
- `credentials`：加密后的凭据内容
- `credential_mask`：兼容保留字段；列表不展示凭据，编辑弹窗可展示完整凭据
- `proxy_profile_id`
- `concurrency_limit`
- `current_concurrency`：运行态字段，不建议直接持久化为主状态
- `passthrough_enabled`
- `error_policy_id`
- `priority`
- `schedulable`
- `notes`
- `last_used_at`
- `cooldown_until`：账号临时冷却截止时间；未来时间内不参与调度
- `last_error_message`：最近一次熔断、临时不可调用或失败原因摘要
- `stream_failure_count`：流失败窗口内累计次数
- `stream_failure_window_started_at`：流失败统计窗口起始时间
- `created_at` / `updated_at`

账户列表第一期展示：账户名称、账户类型、供应商、并发数、状态、用量情况、优先级、最近使用时间、操作。账号优先级按数字从小到大生效，`0` 优先于 `10`；同分组调度时先尝试优先级更高的账号，当前账号失败或进入冷却后再切换到下一个可用账号。`Access/API Key` 与 `Refresh Token` 不在列表展示，只在编辑弹窗里查看和修改；`Refresh Token` 只对 OAuth 账户有意义。状态语义统一为：正常、停用、错误、限流中、临时不可调用。

账户创建入口保持统一：页面只提供“添加账户”，弹窗内先选择供应商，再按供应商能力展示账户类型，最后渐进展开具体配置。第一期 OpenAI 支持 `oauth` 与 `api_key` 两种类型；后续 Claude Code、Gemini 等供应商只扩展供应商定义和对应创建表单，不再新增外部入口按钮。

账号级错误处理通过 `credentials.error_handling_rules` 保存为账户内嵌规则；新建和编辑账户都在弹窗内维护规则列表。网关不会把未命中的上游错误透传给客户端：未命中账号规则时，当前账号会临时不可调用并切换到同分组内下一个可用账号。列表不额外展示规则明细，避免挤占用户关心的状态、并发、用量和最近使用时间。

OpenAI OAuth 建议凭据：

- `access_token`
- `refresh_token`
- `expires_at`
- `account_id`
- `organization_id`

OpenAI API Key 建议凭据：

- `api_key`
- `base_url`
- `organization_id`

## 分组

分组是调度和授权边界。账户主动选择归属分组，API Key 绑定分组。

建议字段：

- `id`
- `name`
- `provider_code`：分组所属供应商，默认 `openai`；只允许同一供应商的账户归入该分组
- `description`
- `enabled`
- `account_count`
- `created_at` / `updated_at`

关系表：

- `group_account.group_id`
- `group_account.account_id`
- `group_account.weight`
- `group_account.enabled`

调度顺序以账号 `priority` 升序为主；`group_account.weight` 只作为同优先级下的辅助排序字段。

第一阶段建议：

- 一个账户主动归属到零个或一个分组；调整入口在账户创建 / 编辑表单中
- 一个分组可以汇总多个同供应商账户
- 一个分组只汇总同一 `provider_code` 下的账户，列表只展示数量与聚合状态，不展开账户名称
- 一个 API Key 先绑定一个分组

## API Key 管理

API Key 是对外访问入口，不直接绑定账户，而是绑定分组。

建议字段：

- `id`
- `name`
- `key_hash`
- `key_prefix`
- `status`
- `group_id`
- `expires_at`
- `rate_limit`
- `quota_limit`
- `scopes`
- `last_used_at`
- `created_at` / `updated_at`

注意：

- 明文 API Key 只在创建时返回一次。
- 列表和详情直接展示完整密钥，便于单人自用场景复制和排查。

## 代理管理

代理独立于账户，账户只引用代理配置。

建议字段：

- `id`
- `name`
- `type`：`http`、`https`、`socks5`
- `host`
- `port`
- `username`
- `password_encrypted`
- `enabled`
- `test_status`
- `last_tested_at`
- `created_at` / `updated_at`

## 使用记录

使用记录用于排查、统计和后续计费。网关每次实际发起上游请求都写入一条记录，重试失败、切换账号失败和最终成功都会分别保留。

建议字段：

- `id`
- `request_id`
- `api_key_id`
- `group_id`
- `account_id`
- `endpoint`
- `provider_id`
- `model`
- `stream`
- `status_code`
- `success`
- `client_ip`
- `first_token_ms`
- `duration_ms`
- `input_tokens`
- `output_tokens`
- `cache_read_tokens`
- `cost_usd`
- `error_code`
- `error_message`
- `request_snapshot_json`：失败请求的客户端请求快照
- `response_snapshot_json`：失败请求的网关返回与最后一次上游响应快照
- `created_at`

第一阶段网关已写入请求记录，并从 OpenAI 响应里的 `usage.input_tokens`、`usage.output_tokens`、`usage.input_tokens_details.cached_tokens` 统计 token。使用记录会保存 `METHOD /v1/path` 形式的接口，便于区分 `/v1/models` 这类成功但没有模型和 token 用量的请求。成本统计复用供应商模型目录里的 OpenAI 价格快照做轻量 1:1 估算；未覆盖的模型先只记录 token，不强行猜价格。使用记录接口会派生输入成本、输出成本、输入单价、输出单价、缓存读取成本、账户计费和固定 1x 倍率，用于前端悬浮明细展示，不额外写入数据库。失败记录额外保存请求/响应快照，便于在使用记录页查看当时的请求体、响应体和最后一次上游错误。

## 错误处理策略

错误处理策略以账号为主，参考 `sub2api` 的账号内嵌规则：账户创建和编辑时把规则列表保存到 `credentials.error_handling_rules`。系统不提供默认错误处理策略；账号规则未命中的上游错误统一触发当前账号临时不可调用并切换账号重试。

账号规则字段：

- 匹配条件：`status_codes`、`error_codes`、`error_types`、`keywords`，配置多个字段时必须同时命中。
- 排序与开关：`enabled`、`priority`、`name`、`description`。
- 处理动作只保留三种：`rate_limited`（限流）、`temp_unschedulable`（临时不可调用）、`error_disabled`（错误）。
- 限流恢复：`reset_strategy` 支持 `duration`、`daily`、`weekly`，并保留 `duration_hours`、`daily_reset_hour`、`weekly_reset_day`、`weekly_reset_hour`、`reset_timezone`。

动作语义：`rate_limited` 会把账号置为限流中并按恢复策略自动恢复；`temp_unschedulable` 会写入临时不可调用；只有显式配置 `error_disabled` 才会把账号置为错误。

## 系统设置

系统设置只放全局调度默认值，不要覆盖供应商定义或账号级明确配置。OpenAI 默认 Base URL 属于供应商定义，第一阶段固定为官方地址，不在系统设置中暴露。

建议配置：

- 默认上游请求超时
- 默认账号并发上限
- 默认临时不可调用时长
- 默认代理策略
- API Key 自动生成规则（内部固定，不在系统设置暴露）
- 自用后台完整密钥展示规则
- 日志保留天数

### 流熔断与账号处理

轻量版参考 `sub2api` 的流超时处理语义，但不引入复杂运行时调度器。当前实现完全基于 SQLite 字段和请求时判断：

- `defaultTemporaryUnschedulableMinutes`：默认 `5` 分钟。未知异常、策略冷却和流熔断都使用这个全局时长。
- `temporaryUnschedulableRetryIntervalSeconds`：默认 `3` 秒。进入临时不可调用前会按这个间隔短暂重试。
- `temporaryUnschedulableRetryAttempts`：默认 `3` 次。超过次数后才标记为临时不可调用。
- `streamCircuitBreakerEnabled`：默认 `true`。启用后累计流式空闲/中断失败。
- `streamRequestTimeoutSeconds`：默认 `180` 秒。流式请求在首个数据块到来前超过该时间，强制切换账号重发。
- `streamIdleTimeoutSeconds`：默认 `30` 秒。流式响应在首个数据块后超过该时间没有新数据，记录一次失败。
- `streamFailureThresholdCount`：默认 `3` 次。
- `streamFailureThresholdWindowMinutes`：默认 `10` 分钟。

处理方式：

- 流熔断达到阈值后一律写入 `accounts.status = 'temporary_unavailable'` 并设置冷却截止时间；首包前请求熔断会先切换账号重发，冷却结束后会自动恢复为正常。
- 未被账户错误处理策略截获的上游非成功响应，会立即把当前账号写入 `defaultTemporaryUnschedulableMinutes` 的临时不可调用状态，并切换到下一个可用账号重试，不把上游错误返回给客户端。
- 未知请求异常会先按 `temporaryUnschedulableRetryIntervalSeconds` 与 `temporaryUnschedulableRetryAttempts` 做短暂重试，仍失败后才进入 `defaultTemporaryUnschedulableMinutes` 的临时不可调用状态并切换账号。
- 命中错误策略里的 `rate_limited` 会标记为限流中；命中 `error_disabled` 才会显式标记为错误。
- 网关请求成功后会清理该账号的失败计数、冷却时间和最近错误，并恢复为正常。
- 账户列表展示五态状态、冷却截止时间和错误摘要；完整 token 仍只在编辑弹窗中查看。

## 中转协议与流程

对外只暴露 OpenAI 兼容的 `/v1/*` 网关入口。开发环境默认 Base URL 是 `http://127.0.0.1:3000/v1`，常用路径包括 `/v1/models`、`/v1/responses` 和 `/v1/chat/completions`。后续新增提供方时，也优先把上游差异适配到 OpenAI 兼容格式，不在客户端侧暴露各厂商私有协议。

当前轻量中转请求链路是：

1. 校验 API Key
2. 找到 API Key 绑定的分组
3. 获取分组内可调度账户
4. 按状态、并发、代理、错误冷却等规则选账号
5. 按供应商和账户类型构造上游请求
6. 根据账号错误策略处理上游响应；未命中则临时不可调用当前账号并切换账号
7. 写入使用记录



## OpenAI OAuth 授权设计

- OAuth 手动授权：生成 PKCE 授权链接，用户复制 localhost 回调 URL，后端校验 `state` 后换取 token。
- Refresh Token 授权：用户粘贴 `refresh_token`，后端刷新后创建 OAuth 账户。
- OAuth 凭据字段：`access_token`、`refresh_token`、`expires_at`、`client_id`、`email`、`organization_id`、`base_url`。
- 网关调度：同一分组内可混合 API Key 账户和 OAuth 账户，OAuth 账户使用 `access_token` 透传到上游。
- 自动刷新：OAuth `expires_at` 接近过期时，用 `refresh_token` 刷新并写回 SQLite。
