# OpenAI 账号接入

## 范围

当前版本启用 OpenAI 供应商，账户类型支持：

- OpenAI OAuth
- OpenAI API Key

其他供应商保留架构扩展位，暂不开放页面和接口。

对外中转入口统一使用 OpenAI 兼容协议：客户端 Base URL 可填服务根地址或 `/v1`，例如开发环境 `http://127.0.0.1:3000` 或 `http://127.0.0.1:3000/v1`；API Key 填 API 密钥页生成的本地网关密钥。后续即使增加其他主流厂商，也先适配为 OpenAI 兼容请求格式。

“OpenAI 兼容”在这里指本地客户端入口、认证方式和返回错误结构尽量兼容 OpenAI 协议；不同账户类型的上游能力不完全相同，具体以能力矩阵为准。

| 账户类型 | 上游链路 | 当前路径策略 | 主要限制 |
| --- | --- | --- | --- |
| OpenAI API Key | 公开 OpenAI-compatible API，默认上游归一到 `/v1/*` | 网关不维护逐路径白名单；客户端访问根路径或 `/v1/*` 时，都会按账户 `base_url` 归一到上游 `/v1` 后透传，例如 `/responses`、`/v1/responses`、`/chat/completions`、`/v1/chat/completions`。`GET /models` / `/v1/models` 由本地模型目录直接返回。 | 请求体优先 raw passthrough；本地过滤危险 header 和本地认证 header；不自动生成 OpenAI 组织、项目或 Beta 配置。某条路径最终能否成功取决于该 API Key 上游 `base_url` 本身是否支持。 |
| OpenAI OAuth | ChatGPT / Codex backend 的 `openai_oauth_codex` adapter | 只为 `POST /responses`、`POST /v1/responses`、`POST /responses/compact` 和 `POST /v1/responses/compact` 构造 Codex 上游请求；`GET /models` / `/v1/models` 由本地模型目录返回。 | 不等价于公开 OpenAI API Key；不承诺 `/chat/completions` 到 Responses 的重型协议翻译，也不承诺公开 Responses API 全字段原样进入 Codex backend。OAuth-only 分组请求不支持的路径时不会落到公开 OpenAI API。 |

混合分组中如果同时存在 API Key 账号和 OAuth 账号，某条路径是否可用取决于调度命中的账号类型：OAuth 账号不支持的路径会跳过该账号，仍可由同分组内可用的 API Key 账号承接；如果当前 API Key 还绑定了后续号池，且当前分组没有任何账号类型能承接该路径，网关应继续尝试下一号池；如果所有号池都没有可用账号，则返回“没有可用的上游账户”一类网关错误，而不是自动协议翻译。多号池混合规则见 [API Key 多分组路由设计](APIKey多分组路由设计.md)。

单次流式响应收到首段上游内容后，如果本次响应超过输出停顿上限仍没有任何上游新数据，或连接读取异常中断，服务端不换账号也不重新请求上游续写；持续有 raw chunk 但暂未形成完整 SSE 事件时只记录诊断并继续转发。当前运行时按客户端策略处理可见输出前失败：只有命中 Codex profile、Responses SSE 和可解析的 `x-codex-turn-metadata.turn_id` 时，才写出 Codex 可重试的 `response.failed/upstream_retryable_error`，并在同一 turn 第 4 次失败链路上避让已失败账号；未命中 Codex profile 的 OpenAI-compatible 请求不伪造 Codex 可重试码。调研结论见 [流式中断与客户端重试调研](流式中断与客户端重试调研.md)。

## OpenAI 供应商定义

```ts
type ProviderCode = 'openai'

type OpenAIAccountType = 'oauth' | 'api_key'
```

默认能力：

- 模型列表
- Responses API 预留
- 流式响应预留
- 供应商级默认透传

## OpenAI OAuth 创建方式

当前支持手动授权链接和直接粘贴 Refresh Token 两种创建方式。

表单字段：

- 账户名称
- `access_token`
- `refresh_token`
- 支持模型（可选，不选择表示不限制）
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 错误策略
- 备注

`expires_at`、`account_id` / `chatgpt_account_id` 属于 OpenAI OAuth token 响应或 token 解析出的系统元数据，不作为用户表单输入项。`account_expires_at` 是本系统的账户套餐到期时间，可选填写，和 OAuth token 的 `expires_at` 不是同一个字段。

保存要求：

- token 加密存储
- `refresh_token` 按凭据指纹做数据库全局唯一约束，不能被其他系统账户重复添加；无 `refresh_token` 时兜底约束 `access_token`
- 列表不展示 Access Token 与 Refresh Token，编辑弹窗可查看和修改
- `expires_at` 由后端根据 OpenAI 返回的 `expires_in` 自动计算和刷新
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 可手动启用 / 停用
- `refresh_token` 只对 OAuth 账户需要，账户列表不展示

## OpenAI API Key 创建方式

表单字段：

- 账户名称
- `api_key`
- `base_url`
- 支持模型（可选，不选择表示不限制）
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 错误策略
- 备注

保存要求：

- API Key 加密存储
- API Key 按凭据指纹做数据库全局唯一约束，不能被其他系统账户重复添加
- 列表不展示 API Key，编辑弹窗可查看和修改
- `base_url` 默认使用 OpenAI 官方地址
- 不提供 `OpenAI-Organization`、`OpenAI-Project` 和 `OpenAI-Beta` 的账号表单配置；组织 / 项目属于 OpenAI 账号上下文，服务端不凭空生成，Beta 由客户端按公开 API 需求显式传入
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 可手动启用 / 停用

透传策略：OpenAI 账户默认按供应商网关策略透传，用户侧不提供开关；服务端只保留本地鉴权、账号调度、上游认证替换、安全头剔除、流式转发和错误兜底等必要中转职责。API Key 账号请求体优先原样使用客户端 `rawBody`；Header 会过滤本地认证、代理链路、SDK / tracing 噪声和客户端传入的 `OpenAI-Organization` / `OpenAI-Project`，不从账号凭据生成这些上游账号上下文头。`OpenAI-Beta` 保留客户端显式传入值，服务端不做账号级覆盖。

### 账户支持模型限制

账户可以配置支持模型列表，选项来自所属供应商模型目录；不选择任何模型表示不限制。网关调度时在账号池缓存快照内按请求 `model` 做内存过滤，只有不限制账号或精确支持该模型的账号才进入上游尝试。因模型不匹配被跳过的账号不算失败，不触发账号错误策略、本地屏蔽或临时不可调用。

如果分组内所有可用账号都不支持当前请求模型，网关直接返回本地错误，不继续逐个请求上游。`GET /models` / `GET /v1/models` 仍由本地供应商模型目录返回，不参与账号模型限制过滤。完整设计见 [账户模型限制设计](账户模型限制设计.md)。

## 账户分组绑定

当前关系规则：

- `accounts.system_account_id` 表示物理上游账户的资源归属人；账户所有者可以把账户授权给其他系统账户使用，但授权不改变账户所有者，也不复制凭据。
- `group_accounts` 表示某个使用方的本地分组绑定；同一个物理账户可以被账户所有者绑定到自己的分组，也可以在授权有效时被被授权用户绑定到自己的同供应商分组。
- 一个账户在同一个使用方作用域内同一时间只保留一个有效分组绑定；自有账户在所有者作用域内创建 / 编辑时选择分组，被授权账户在被授权用户作用域内通过“绑定分组 / 调整分组”选择自己的本地分组。
- 被授权用户把授权账户加入自己的同供应商分组后，自己的 API Key 才能通过该分组调度该授权账户；这只是本地调度绑定，不改变账户所有者的分组绑定。
- 被授权用户不能编辑、删除、查看敏感凭据、修改代理/并发/错误策略/状态/调度配置，也不能继续转授权
- 统一授权管理支持分组授权；分组所有者可以把整个分组授权给系统账户或系统团队使用，授权共享该分组内当前全部可共享账户，但授权方分组不再作为被授权方新建 API Key 的直接调度池
- 被授权用户不能把自己的 API Key 直接绑定到授权方分组；授权账户会进入被授权用户自己的同供应商本地分组，再由该用户自己的 API Key 通过该本地分组调度
- 分组可以汇总多个 OpenAI 账户，但只能汇总同一供应商下的账户
- API Key 只绑定调用方自己的一个或多个本地分组；统一授权只提供账户或分组的使用权，不能绕过本地分组边界
- 请求进入后先按 API Key 号池优先级选择一个可承接分组，再只能使用该分组内的账户

## 当前页面入口

1. 供应商页：展示 OpenAI、支持的账户类型和模型价格目录。
2. 账户页：OpenAI OAuth / API Key 创建、编辑、状态切换、测试、授权来源和 OAuth 额度进度展示。
3. 分组页：维护分组基础信息、查看账户数量与聚合状态、绑定自有或授权账户。
4. 统一授权页：统一维护账户 / 分组授权、系统账户 / 团队授权对象、授权到期和授权额度。
5. 团队消耗明细 / 用户消耗明细：按日期范围查看授权团队和最终用户消耗。
6. API Key 页：创建本地网关密钥并绑定当前系统账户自己的一个或多个本地分组号池；授权账户需要先加入该用户自己的分组后再参与调度。
7. 代理页：维护代理并给账户选择，支持手动代理检测和出口信息缓存。
8. 使用记录页、用量统计页、AI 性能监控页、日志和审计页面：查看 OpenAI 网关请求事实、统计缓存、性能趋势和排障链路。
9. 系统设置页：维护当前白名单内的网关、后台任务、操作日志和数据保留策略。

## 当前接口入口

- 系统后台 API 统一在 `/__aisys__/api/*` 下，用户侧接口使用 `/__aisys__/api/my-*`，管理侧接口使用 `/__aisys__/api/*` 并按需要求管理员权限。
- OpenAI 账号接入相关接口包括 `providers`、`accounts` / `my-accounts`、`openai-oauth` / `my-openai-oauth`、`groups` / `my-groups`、`api-keys` / `my-api-keys`、`authorizations` / `my-authorizations`、`system-teams` / `my-teams`、`proxies` 和 `stats` / `my-stats`。
- 授权消耗明细接口固定使用 `startDate=YYYY-MM-DD`、`endDate=YYYY-MM-DD`、`page` 和 `pageSize` 参数读取窗口分页，管理侧为 `/__aisys__/api/authorizations/usage/team-details`、`/__aisys__/api/authorizations/usage/user-details`，用户侧为 `/__aisys__/api/my-authorizations/usage/team-details`、`/__aisys__/api/my-authorizations/usage/user-details`。
- 客户端网关入口是根路径和 `/v1/*`，不使用后台登录态，只使用本地 API Key。

## 当前不包含

- 其他供应商的页面创建和网关适配。
- 非 OpenAI 兼容协议的专用客户端网关。
- 重型计费结算系统。
- 多实例 / 多租户商业化体系。
- 账号质量主动探测；账号质量只来自真实请求统计和冷却到期后的恢复性复测。



## OpenAI OAuth 授权方式

`juhe-ai` 为 OpenAI OAuth 账户实现两种轻量授权方式：

### 手动授权

1. 后端生成 `state`、`code_verifier`、`code_challenge` 和授权链接。
2. 前端打开 `https://auth.openai.com/oauth/authorize`。
3. 用户登录 OpenAI 后浏览器会跳转到 `http://localhost:1455/auth/callback`。
4. 如果本机没有监听该端口，浏览器显示连接失败也没关系，复制地址栏完整 URL。
5. 前端把回调 URL 提交给后端，后端校验 `state` 并用 PKCE `code_verifier` 换取 token；Client ID 与 Redirect URI 使用后端内置默认值，不暴露给用户填写。
6. 创建 OpenAI OAuth 账户，保存 `access_token`、`refresh_token`、`expires_at`、`client_id`、邮箱和可选的 `account_expires_at`。
7. 账户落库后不主动请求模型接口获取额度；额度快照等待第一次真实网关请求或账户测试返回 Codex rate-limit 响应头后被动更新。
8. 创建接口默认不携带账号级错误处理规则；只有用户显式添加专属规则时，客户端才通过 `credentialsPatch.error_handling_rules` 携带。`access_token`、`refresh_token`、`expires_at`、`client_id` 和 `base_url` 必须以 OpenAI token endpoint 返回或服务端 fallback 为准，不能被请求体覆盖。

### Refresh Token 授权

1. 用户直接粘贴已有 `refresh_token`。
2. 后端使用内置默认 Client ID 和 `grant_type=refresh_token` 向 OpenAI token endpoint 刷新。
3. 刷新成功后创建 OpenAI OAuth 账户。
4. 账户落库后不主动请求模型接口获取额度；额度快照等待第一次真实网关请求或账户测试返回 Codex rate-limit 响应头后被动更新。
5. 如果 OpenAI 没返回新的 `refresh_token`，继续保留用户输入的原始 `refresh_token`。

### 调度与授权刷新

- API Key 账户使用 `credentials.api_key` 作为上游 Bearer token。
- OAuth 账户使用 `credentials.access_token` 作为上游 Bearer token。
- API Key 账户继续按账户 `base_url` 转发，默认指向 `https://api.openai.com/v1`，承接客户端网关 `/*` 和 `/v1/*` 兼容请求；OpenAI API Key 上游仍归一到 `/v1/*`。
- OAuth 账户不把 `access_token` 当作官方 OpenAI API Key 打到 `api.openai.com/v1`；真实转发走 ChatGPT / Codex backend 专用链路 `https://chatgpt.com/backend-api/codex`。
- OAuth Codex 网关当前支持 Codex 原生 `POST /responses` 和 `POST /responses/compact`，暂不做 `/chat/completions` 到 Responses 的重型协议翻译。
- `GET /v1/models` 由本地 OpenAI 模型价格目录返回，不依赖某个上游账号是否可调度，避免 OAuth-only 分组在客户端初始化阶段失败。
- OAuth Codex 转发会补齐必要 Codex CLI 协议头，并在账号凭据包含 `chatgpt_account_id` / `account_id` 时写入 `chatgpt-account-id`；客户端传入的同名头不会透传，避免跨账号伪造。
- 网关发现 OAuth token 即将过期时，会优先用 `refresh_token` 自动刷新并写回账户，作为请求前懒刷新兜底。
- OAuth Access Token 刷新按账户串行执行；刷新前会在锁内重读账户，避免使用缓存里的旧 `refresh_token`。刷新成功后 server 进程会短 TTL 记住最近新凭据，同一波临期请求复用该结果，不再逐个重复读写 DB service。如果 OpenAI 返回 `refresh_token_reused` / `invalid_grant` 且重读后发现账户凭据已经被其他请求或后台任务更新，会采用最新凭据恢复，不把竞争误判为账户失效。
- 后台 worker 另有 `openai-oauth-access-token-refresh` 专职任务，默认每 60 秒扫描所有仍存在、未删除、有 `refresh_token` 且 Access Token 距离过期小于 5 分钟的 OpenAI OAuth 账户，提前刷新并写回凭据；扫描不受 `active`、`disabled`、`error`、`rate_limited`、`temporary_unavailable` 或 `schedulable` 状态影响。后台预刷新只做 token 保活，成功时不恢复普通冷却状态、不清理无关错误；失败时按退避等待并累计连续失败次数，连续 3 次失败后把非停用账户写入 `status = error`，`last_error_code = oauth_token_refresh_failed`，`last_error_message` 记录最近失败摘要，后续后台刷新成功会自动恢复该异常。手动停用账户不会被后台刷新失败覆盖成异常。
- 后台不再为了 OAuth 额度快照发起模型请求；额度快照只从真实网关请求或账户测试返回的 Codex rate-limit 响应头被动更新。access token 即将过期时，真实网关请求仍会按正常规则做请求前懒刷新。
- OAuth token 响应里的 `expires_in` 只用于计算 `credentials.expires_at`，表示 access token 过期时间；账户购买/套餐到期时间使用单独的 `account_expires_at`。
- 账户 `account_expires_at` 到期后直接停用、关闭调度，不再参与网关选号；OAuth 额度快照只会在真实请求命中该账号时被动更新。
- 账户页不提供常驻“刷新授权”或“刷新用量”按钮；授权续期由请求前懒刷新和后台 Access Token 预刷新维护，额度快照由真实请求响应头被动维护。
- OAuth token 刷新和账户测试会优先使用账户绑定的代理；没有绑定代理时默认直连。账户测试必须复用本地 OpenAI 网关模型请求链路并写入使用记录，不能在测试服务里单独直连上游；API Key 账户测试会优先复用最近真实请求形态，模型按显式传入、最近真实请求、账户支持模型、默认模型顺序选择，避免固定测试模型和真实可用模型不一致。API Key 账户的 Responses 测试不发送 `max_output_tokens`，兼容不支持该参数的 OpenAI 兼容上游；管理后台测试弹窗只选择模型，测试输入由后端使用默认探活输入生成。后台账号质量主动探测能力已删除；后台冷却复测固定启用，复用同一网关模型请求链路去恢复冷却到期的 `temporary_unavailable` 和 `rate_limited` 账号。账号进入冷却态后先按 3 秒进入快速恢复通道，复测失败后按 `3s -> 6s -> 12s -> 24s -> ...` 翻倍；超过快速阈值后退化为慢速恢复通道，单次等待不超过 `defaultTemporaryUnschedulableMinutes` 表达的最大暂停时间。后台复测成功恢复正常，失败只继续退避复测，不把系统自动冷却升级为永久异常。恢复探活使用 `traffic_source = cooldown_retest` 写入使用记录和审计，避免污染业务统计、账户质量和真实请求形态学习；写入账号 `last_error_code/last_error_message` 时使用本次上游真实错误摘要，避免把网关最终兜底 503 覆盖成账号原因；如果响应里带有 Codex 额度头，额度快照来源也记录为 `cooldown_retest`，不伪装成真实网关流量。迁移旧账户时不再自动创建或绑定本机固定端口代理，避免换电脑或服务器部署后误连本机端口。

## 会话亲和调度

OpenAI 网关使用短期内存会话亲和，只影响账号排序，不绕过本地 API Key、分组授权、账号状态、冷却、到期时间、并发、错误策略和上游可用性判断。

- 会话标识来源包括请求头或请求体里的 `previous_response_id`、`session_id`、`conversation_id`、`prompt_cache_key`，以及 `metadata.session_id`、`metadata.conversation_id`、`metadata.user_id`。
- 亲和键按 `system_account_id + api_key_id + session` 隔离，避免不同本地 API Key 或系统账户共享同一个上游会话绑定；`group_id` 不参与亲和键。
- OAuth Codex adapter 写入上游的 `session_id`、`conversation_id` 和 `prompt_cache_key` 也按同一层本地边界隔离，不把上游账号 ID、账号类型或分组 ID 写进隔离 key；同一个本地 API Key 路由下因失败、冷却或并发切换上游账号时，尽量保留客户端会话和 prompt cache 连续性。
- 首次成功命中账号后写入短期绑定；同一会话后续请求在同一调度层级内优先尝试同一账号，降低 Codex / Responses 多轮会话被调度到不同 OAuth 账号的概率。
- 客户可用性优先于粘性：会话亲和不会跨过超级优先、账号优先级和更优质量候选。绑定账号并发满时会先在本请求内做很短的同账号等待和重查，尽量复用上游会话 / 缓存；短等后仍满、账号不可用或请求失败时才让后续候选继续尝试。
- 绑定只保存在进程内存中，服务重启、缓存淘汰、账号失败、流式首包失败、流式中断、冷却、停用或到期都会自然失效或被清理。
- 会话亲和不是客户端身份认证，也不是 Codex 重试计数依据。Codex 第 4 次切号这类 turn 级策略只能使用可解析的 `x-codex-turn-metadata.turn_id` 加本地 API Key / endpoint / 请求体哈希边界；识别不到时不使用 `session_id` 或 `x-client-request-id` 回退。

### OpenAI OAuth 额度进度

OpenAI OAuth 账户受上游 Codex/ChatGPT 使用窗口限制，常见窗口包括约 `5h` 窗口和 `7d` 窗口；这类额度不是 API Key 的 token / 成本用量，必须单独展示和处理。

- 数据来源使用真实网关请求或账号测试返回的 Codex rate-limit 响应头：`x-codex-primary-used-percent`、`x-codex-primary-reset-after-seconds`、`x-codex-primary-window-minutes`、`x-codex-secondary-used-percent`、`x-codex-secondary-reset-after-seconds`、`x-codex-secondary-window-minutes`。后台不再为了额度快照主动探测。
- 归一化规则：只按 `window_minutes` 判断窗口，`<= 360` 分钟归为 `5h`，更长归为 `7d`；没有窗口长度的 primary / secondary 原始 header 只保存原始字段，不生成 `codex_5h_*` 或 `codex_7d_*` 归一化字段。
- 存储字段保存为账号运行态快照，并按 `system_account_id + account_id + kind` 隔离：`codex_5h_used_percent`、`codex_5h_reset_after_seconds`、`codex_5h_reset_at`、`codex_5h_window_minutes`、`codex_7d_used_percent`、`codex_7d_reset_after_seconds`、`codex_7d_reset_at`、`codex_7d_window_minutes`、`codex_usage_updated_at`、`last_attempt_at`、`last_success_at`、`next_refresh_after`、`refresh_status`、`last_error_message`。
- 获取策略：列表只读已缓存快照，不因展示批量探测；新建 OAuth 账户不触发首次快照刷新，缺失或过期时等待真实请求或账户测试的响应头更新。
- 后台策略：不再注册 OAuth 额度快照主动探测任务；后台只保留 Access Token 预刷新。
- 官方限额处理：收到 OpenAI OAuth / Codex 官方限额响应时，先解析 header 里已耗尽窗口的 reset 时间；如果 header 不足，再解析响应体 `error.resets_at` 或 `error.resets_in_seconds`；计算出的时间写入账号 `rate_limited` 冷却截止时间，后台下次刷新不早于 reset 时间。该逻辑属于官方账号语义，不要求账号内置默认错误规则。
- UI 展示：OAuth 行在“用量情况”里显示本地请求/token/成本摘要，同时额外显示 `5h`、`7d` 两条进度条、百分比、倒计时/恢复时间、快照更新时间和快照来源；API Key 行不显示这两条 OAuth 额度进度。
- 授权展示：OAuth 额度快照是账号非敏感运行态，被授权用户获得该 OAuth 账户使用权后，也能在自己的账户列表看到同一账号的 `5h` / `7d` 额度进度，但仍不能查看 Access Token、Refresh Token 或完整请求内容。
- UI 限制：更多菜单不提供“刷新用量”按钮；快照缺失或过期时显示“等待真实请求更新”或“暂无快照”，不触发前端即时探测。

## 账户列表字段

账户列表只展示运维判断需要的信息：

- 账户名称
- 来源：自有账户 / 授权账户
- 所有者摘要：仅授权账户展示
- 账户类型
- 供应商
- 并发数
- 状态（正常、停用、异常、限流中、临时不可调用；授权账户额外按有效性展示授权到期、授权已失效、授权额度已用完，账户套餐过期展示账户到期）
- 用量情况
- 优先级
- 最近使用时间
- 到期时间（授权账户优先展示授权到期时间；没有授权到期时间时展示账户到期时间）
- 操作

操作区提供编辑、删除和“更多”菜单；更多菜单包含测试、迁移流量、停用/启用账户、恢复正常和切换客户端，不再提供分散的授权入口，授权关系统一到管理侧 `统一授权管理 / 统一授权` 或用户侧 `我的授权 / 授权操作` 维护。编辑弹窗只维护名称、凭据、分组、并发、优先级、代理、过期时间、备注和错误策略等配置，不提供状态修改；保存编辑时也不得提交 `status`，避免覆盖测试、后台自动恢复、错误处理或网关冷却刚写入的状态。迁移流量用于人工处理上游返回状态码正常但内容异常、自动错误策略未识别的情况；弹窗展示当前账户、同分组可用目标账户和迁移后原账户状态，默认把原账户改为临时不可调用，也可指定为停用账户。该动作只影响后续请求，不主动打断当前正在输出的流式连接。手动启用只针对真正已停用的账户；停用态是人工硬边界，不能被账户测试、后台冷却复测、OAuth 刷新成功或网关异步错误处理自动恢复，也不能被这些后台路径改为临时不可调用。`限流中` 和 `临时不可调用` 可通过更多菜单的“恢复正常”手动清理冷却与最近错误并恢复调度，也可由手动测试成功恢复；后台冷却复测固定启用，会在冷却时间到期后复测 `temporary_unavailable` 和 `rate_limited` 账号。复测失败会先短重试确认，再按指数退避延长下一次复测时间，不升级为永久异常。`异常` 使用 `status = error` 作为统一硬状态，页面状态标签显示“异常”，tooltip 展示 `last_error_code` 对应的异常类型和 `last_error_message` 详情；`oauth_token_refresh_failed` 这类后台刷新异常会在后台刷新成功后自动恢复，其它显式硬异常仍保留编辑（状态锁定为异常）、删除、测试和“恢复异常”入口。授权额度耗尽是授权关系的展示层状态，只显示“授权额度已用完”并由网关按授权额度返回 429，不改变物理 AI 账户状态；只有上游账号本身触发错误策略 `rate_limited` 时才显示“限流中”。账户套餐到期显示“账户到期”，授权到期或绑定的稳定授权 ID 失效显示“授权到期 / 授权已失效”。

自有账户测试入口不因停用状态隐藏；停用账户仍可手动测试凭据、代理和上游链路，但测试结果只作为诊断，不会恢复或改写停用状态。在非停用状态下，自有账户测试不受 `schedulable` 标记或冷却时间限制，只要账户仍绑定分组且凭据可读取，就固定测试当前账号。授权账户测试还必须满足授权可用、额度未耗尽、已绑定当前用户分组、账户未到期，停用的授权账户同样可以保留测试入口用于诊断；授权用户测试时只返回脱敏后的状态码、耗时、成败和简短错误，完整上游响应头、响应体、上游地址、代理和请求体诊断只对所有者或管理员开放。授权账户只保留使用相关操作，隐藏编辑、删除和所有配置修改入口。测试会打开结果弹窗，可选择模型；弹窗终端区域展示测试过程、成败、模型返回内容，并在结束行内显示总耗时；所有者或管理员可通过完整 JSON 查看状态码、请求 URL、代理、原始响应正文等排查字段，不再额外展示测试结果表格。测试必须复用正常客户请求的网关调度、代理、OAuth 刷新、错误策略、用量解析和成本统计链路，并固定只测试当前账号；测试接口会等待网关账号副作用写入完成后再回读状态。若人工测试确认被测账号不可用，且失败不是分组绑定、凭据读取、取消等配置或操作问题，后端只会把当前仍为正常状态且可调度的自有账号标记为 `temporary_unavailable` 并写入冷却时间；授权账号只处理当前用户分组绑定里的正常可调度本地状态，不改所有者物理账号。其他状态不会被失败测试覆盖；如果测试响应里带有 Codex rate-limit header，可作为副作用更新 OAuth 额度快照。

## 统一授权管理

OpenAI 账户不再分别设计账户授权弹窗和分组授权弹窗，授权关系统一进入管理侧 `统一授权管理 / 统一授权` 或用户侧 `我的授权 / 授权操作`。完整规则见 [系统团队与统一授权设计](系统团队与统一授权设计.md)。

- 授权资源支持 AI 账户和分组。
- 授权对象支持系统账户和系统团队。
- 新增授权时选择资源类型、自有资源、授权对象类型、系统账户或团队和备注。
- 回收授权只把授权状态改成 `revoked`，归还授权只把授权状态改成 `returned`，都不删除历史行；被授权用户已绑定的授权账户分组关系会保留但运行时不可用，重新授权同一用户后可按同一稳定授权 ID 恢复使用。API Key 入口只绑定调用方自己的一个或多个本地分组，不保留授权分组绑定关系。
- 使用统计按统一授权 ID 聚合展示请求次数、成功次数、错误次数、输入 Token、输出 Token、缓存读取 Token、总 Token、成本、最后使用时间和最近模型；成功、失败、账户测试和后台检测都按同一套网关使用记录聚合，未产生 token / cost 的失败记录按 0 token、0 cost 计入请求和错误次数。
- 团队授权展开为成员用户授权；统计仍按“资源 × 用户”展示，团队视图只是成员用户用量的筛选汇总。
- 授权消耗统计不包含资源归属人自己的自用消耗；资源归属人的账户 / 分组用量情况仍展示全部总消耗。
- 分组授权是动态使用权，分组所有者后续新增、移除或停用可共享账户，会直接影响被授权用户通过该分组可调度的账户集合。
- 授权分组只共享分组所有者自有账户；如果分组里包含别人授权来的账户，不能通过分组授权继续共享给第三方。
- 资源所有者只能看授权聚合用量，不能看被授权用户的请求快照、响应快照、客户端 IP、API Key 明文或业务请求内容。
## 统计口径

- 网关会记录调用方系统账户、账户所有者、分组所有者、统一授权 ID、授权对象类型、命中账户、API Key、分组、模型、状态、IP、首 token、总耗时、错误和 token。
- 授权账户调用时，请求日志归实际调用方；账户真实用量按同一个 `account_id` 统一累计；授权消耗明细按 `account_authorization_id` 或被授权用户聚合。
- 授权分组调用时，请求日志归实际调用方；分组真实用量按同一个 `group_id` 统一累计；授权消耗明细按 `group_authorization_id` 或被授权用户聚合。
- OpenAI JSON 响应读取 `usage.input_tokens`、`usage.output_tokens` 和 `usage.input_tokens_details.cached_tokens`。
- OpenAI SSE 响应读取 `response.completed` / `response.done` / `response.failed` 事件里的 `response.usage`。
- 成本按 OpenAI 官方 API 价格表做轻量估算；没有覆盖的模型先只记 token。
- OpenAI OAuth `5h` / `7d` 额度进度来自上游 Codex 限制快照，只用于判断账号剩余额度和恢复时间，不计入 `usage_records.cost_usd`，也不替代本地 token 用量统计。

