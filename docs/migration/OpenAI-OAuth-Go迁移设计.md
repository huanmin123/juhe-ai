# OpenAI OAuth Go 迁移设计

> 状态：设计已批准，待实施。本文固定 OpenAI OAuth 管理接口、临时授权会话、凭据存储、Access Token 保活、运行时 owner、切流和回滚边界。Node 当前事实由 `f4066292d` 的 `testdata/openai-oauth-contract/v1/node-authority.json` 冻结；实现时必须先把该 golden 合入目标分支。

## 1. 目标与范围

本次迁移搬运的是 OAuth 业务原理和用户可见能力，不复制 Node 为事件循环、DB service IPC 或进程内队列形成的实现形态。目标是：

- 用 Go 原生 `net/http`、`crypto/rand`、`context`、显式 store port 和 PostgreSQL / Redis 完成 OAuth 管理能力。
- 保持现有管理端与个人端入口、账户作用域、PKCE、账户创建 / 重新授权语义和凭据密文格式可互读。
- 修复已证实的 Node 缺陷；Go 不为错误行为保留兼容分支。
- 把管理 HTTP、Access Token 保活 worker、网关消费分成三个 owner，分别切换，不以一个切片完成推导整个 OAuth 已接管。
- 第一轮先完成必要定向测试和契约验证；真实上游、并发、跨运行时和完整回归在统一验收轮集中执行。

本设计不包含：

- OAuth 网关 Codex adapter、Responses 协议转换或用量快照的重新设计。
- 恢复已删除的 OAuth 主动额度刷新或账号质量主动探测。
- 新增浏览器 callback listener、poll 或 cancel 接口。当前继续使用“用户提交完整 callback URL”的交互。
- 删除 Node 实现、生产切流或不可逆数据迁移。
- 把 OpenAI OAuth 抽象成尚未有真实需求的通用多供应商 OAuth 框架。

## 2. 当前 Node 权威事实

### 2.1 路由与权限

同一组相对路由挂载两次：

| 前缀 | 权限 | 作用域 |
| --- | --- | --- |
| `/__aisys__/api/my-openai-oauth` | 已登录用户 | 强制当前 `systemAccountId`，忽略调用方伪造的其他 owner |
| `/__aisys__/api/openai-oauth` | 管理员 | 可按现有管理查询规则指定 `systemAccountId` |

| 方法与相对路径 | 成功状态 | 主要结果 |
| --- | --- | --- |
| `POST /auth-url` | `200` | `authUrl`、`sessionId` |
| `POST /create-from-code` | `201` | 脱敏账户摘要 |
| `POST /create-from-refresh-token` | `201` | 脱敏账户摘要 |
| `POST /accounts/{id}/refresh-token` | `200` | 脱敏凭据载体账户摘要 |
| `POST /accounts/{id}/reauthorize-from-code` | `200` | 脱敏账户摘要 |
| `POST /accounts/{id}/reauthorize-from-refresh-token` | `200` | 脱敏账户摘要 |

创建接口必须继续复用账户创建的供应商协议档案、分组、模型、健康检查、代理、错误策略、响应检查策略、标签、可用时段和重复名称校验。OAuth 层只强制：

- `providerCode=gpt`
- `type=oauth`
- `status=pending_test`
- `schedulable=false`
- `providerProtocolProfileId` 必须为已启用且支持 OAuth 的 OpenAI 协议档案

创建成功后写 operation log，投递 pending health check，返回脱敏账户；不主动请求模型额度。

### 2.2 OpenAI 授权参数

- authorize URL：`https://auth.openai.com/oauth/authorize`
- token URL：`https://auth.openai.com/oauth/token`
- client ID：`app_EMoamEEZ73f0CkXaXp7hrann`
- redirect URI：`http://localhost:1455/auth/callback`
- scope：`openid profile email offline_access api.connectors.read api.connectors.invoke`
- PKCE：`S256`
- 额外参数：`id_token_add_organizations=true`、`codex_cli_simplified_flow=true`、`originator=codex_cli_rs`

授权码交换使用 `application/x-www-form-urlencoded`；Refresh Token 交换使用 JSON，并发送当前 Codex `originator` 和 `user-agent`。请求超时 `25s`，响应上限 `256KiB`，`expires_in` 必须是至少 1 秒的有限正整数。

### 2.3 账户凭据

账户仍写入现有 `accounts.credentials_encrypted`，明文对象允许以下 OAuth 服务端字段：

```text
access_token, refresh_token, id_token, expires_at, client_id,
email, account_id, chatgpt_user_id, plan_type, base_url
```

`supported_endpoint_modes`、`service_tier_override`、`reasoning_effort_override`、`error_handling_rules` 和 `response_inspection_rules` 是用户配置，不属于 token 响应。重新授权必须保留这些非 token 字段，只覆盖服务端 token / identity 字段。列表、详情、operation log、错误、trace 和指标不得出现 access token、refresh token、id token、authorization code、PKCE verifier、完整 callback URL 或密文。

## 3. 目标分层

```text
httpapi/management_openai_oauth.go
  -> modules/managementopenaioauth/service.go
      -> openaioauth/session.Store        (Redis / bounded memory test fallback)
      -> openaioauth/token.Client         (OpenAI token endpoint)
      -> store/port OAuthAccountStore     (PostgreSQL + config revision CAS)
      -> secretcrypto.JSONCodec           (Node-compatible credentials cipher)
      -> operationlog enqueue
      -> account health dispatch

jobs/openaioauthrefresh
  -> same token.Client + OAuthAccountStore
  -> Redis per-account lease
  -> Asynq/ops worker runtime
```

HTTP handler 只负责鉴权结果、strict JSON、body 上限、query/path 解析、错误到 HTTP 的稳定映射。service 负责流程和状态机；token client 不访问数据库；store 不发外部 HTTP；worker 不复用 HTTP handler。

不把 Node 的 DB service IPC、进程内 Promise 队列、`setInterval` 或同步 / db-service 双实现迁入 Go。

### 3.1 Go port 契约

接口按职责拆分，具体类型名可随现有 package 命名微调，但不得合并成一个同时操作 HTTP、Redis 和 PostgreSQL 的大 service：

```go
type SessionStore interface {
    Create(ctx context.Context, input CreateSessionInput) (SessionCreated, error)
    Acquire(ctx context.Context, input AcquireSessionInput) (SessionLease, error)
    ReleaseRetryable(ctx context.Context, lease SessionLease) error
    SaveExchanged(ctx context.Context, lease SessionLease, token TokenInfo) error
    Complete(ctx context.Context, lease SessionLease, accountID string) error
}

type TokenClient interface {
    ExchangeCode(ctx context.Context, input CodeExchangeInput) (TokenInfo, error)
    Refresh(ctx context.Context, input RefreshInput) (TokenInfo, error)
}

type AccountStore interface {
    ResolveCreateDependencies(ctx context.Context, input CreateDependenciesInput) (CreateDependencies, error)
    CreateOAuthAccount(ctx context.Context, input CreateOAuthAccountInput) (AccountSummary, error)
    LoadOAuthAccount(ctx context.Context, input LoadOAuthAccountInput) (OAuthAccountSnapshot, error)
    CompareAndSwapCredentials(ctx context.Context, input CompareAndSwapCredentialsInput) (AccountSummary, bool, error)
    MarkOAuthRefreshFailure(ctx context.Context, input MarkOAuthRefreshFailureInput) (bool, error)
    ClearOAuthRefreshFailure(ctx context.Context, input ClearOAuthRefreshFailureInput) (bool, error)
}
```

`OAuthAccountSnapshot` 至少包含 account / owner / provider profile / account type、`config_revision`、`credentials_encrypted`、proxy profile、status 和 last OAuth refresh error。port 传密文或明确的领域 token 类型，不传 `map[string]any` 到 HTTP 层。

## 4. HTTP 契约

### 4.1 输入与响应

- 全部接口复用 System API `256KiB` body 上限、`no-store`、session auth、管理 / 个人读写限流和 strict JSON；拒绝未知字段及尾随 JSON。
- `auth-url` 只接受空对象。
- callback 提交只解析 URL 的 `code`、`state` 和 OAuth 标准错误参数；禁止在日志中保留原 URL。
- 创建请求的账户配置字段复用 Go 账户创建 normalization，不复制另一套默认值。
- 账户更新类接口必须验证目标为可编辑、可查看凭据的 OpenAI OAuth 自有账户；授权实例不得更新来源凭据。
- 所有成功响应继续使用当前 `{ "data": ... }` envelope。任何响应不得返回 token、PKCE verifier、密文或 credential fingerprint。

Go 错误响应沿用项目现有顶层 `message`，增量加入稳定 `code`：

```json
{
  "code": "oauth_session_expired",
  "message": "OAuth 会话不存在或已过期"
}
```

`message` 可为中文，前端分支只能依赖顶层 `code`。首批稳定错误码：

| HTTP | `code` | 语义 |
| --- | --- | --- |
| `400` | `oauth_request_invalid` | strict JSON、callback URL、provider/profile/group/policy 或本地参数无效 |
| `400` | `oauth_state_invalid` | state 不匹配；不透露实际 owner/state |
| `400` | `oauth_account_state_invalid` | 当前账户状态不允许 OAuth 刷新 / 重新授权 |
| `404` | `oauth_account_not_found` | 账户不存在或无权操作，避免资源枚举 |
| `409` | `oauth_session_expired` | session 不存在或 TTL 已过；客户端需要重新生成授权链接 |
| `409` | `oauth_session_processing` | 同一 session 正由有效租约处理 |
| `409` | `oauth_session_consumed` | session 已终态消费 |
| `409` | `oauth_grant_invalid` | 上游明确返回已使用、过期或撤销的 code / refresh token |
| `409` | `oauth_account_conflict` | 账户唯一约束或配置 revision 冲突且重试后仍失败 |
| `502` | `oauth_upstream_unavailable` | OpenAI 非终态 5xx、连接、代理、超时或响应格式错误 |
| `503` | `oauth_session_store_unavailable` | Redis/session 原子状态无法保证 |

中间件产生的 `401/403/413/429` 继续使用 System API 通用错误。上游响应正文只能经过有界读取和现有诊断脱敏后进入内部日志摘要，不能原样返回。

### 4.2 幂等与取消

- `create-from-code` 与 `create-from-refresh-token` 复用 Go mutation guard，处理租约 `180s`，指纹用 owner 和敏感字段 HMAC，不存原文。
- 刷新、重新授权不依赖 HTTP 重放幂等，而由 per-account lease + `config_revision` CAS 防止旧 token 覆盖新 token。
- 客户端在 token 请求开始前断开时可取消；一旦上游可能轮换 refresh token，服务端必须完成 token 落库或安全恢复，不能因客户端断开留下旧 refresh token。响应写入仍尊重 request context。

## 5. OAuth session 与 PKCE

### 5.1 生成

使用 `crypto/rand`：

- `state` 至少 256 bit；只向客户端返回原值，store 保存 SHA-256 并用 `subtle.ConstantTimeCompare` 比较。
- `code_verifier` 使用 RFC 7636 允许的 base64url 无填充字符串，熵至少 256 bit。
- `sessionId` 至少 128 bit，不含 owner、时间或可预测前缀。
- session 总 TTL `30m`、全局最多 `1024`、每 owner 最多 `8`；超限淘汰该 owner 最旧的 pending session，不抢占 processing / exchanged session。

生产和多实例模式使用 Redis。session payload 包含 PKCE verifier，因此使用现有 `JUHE_AI_SECRET` 派生的 AEAD 加密后再写 Redis；生产配置禁止回落到开发默认 secret。内存 store 只用于测试或明确单进程开发，必须保持相同状态机和容量语义。

### 5.2 原子状态机

```text
pending
  -> processing(leaseToken, leaseUntil)
      -> pending              网络失败、超时、OpenAI 5xx，可重试
      -> exchanged(ciphertext) token 成功，等待数据库提交，可恢复续跑
      -> consumed             invalid_grant 等已明确消耗授权材料的终态失败
exchanged
  -> consumed(accountId)      账户提交成功
```

Redis Lua 或等价单次原子命令必须同时完成 owner、state hash、TTL、状态和租约校验：

1. owner 不匹配或 state 不匹配时不改变 session。
2. `pending` 可领取；租约未过期的 `processing` 返回 `oauth_session_processing`。
3. 租约过期的 `processing` 可被新 lease token 接管。
4. 只有持有当前 lease token 的调用能 release、写 `exchanged` 或 consume。
5. token 交换成功后把有界 token result 加密保存为 `exchanged`，使数据库瞬时失败后的同一幂等请求可以续跑，不重复使用 authorization code。
6. 成功账户 ID 或 consumed tombstone 保留到原 session TTL，重复请求返回稳定结果或 `oauth_session_consumed`，不退化成模糊 404。

这修复 Node 在发出 token HTTP 请求前 compare-delete session 的问题，同时保持 state 一次性、owner 绑定和授权码不可重放。

### 5.3 Redis key 与数据边界

key 必须经过项目 Redis namespace helper，逻辑名固定如下，便于 owner 审计和减法删除：

| 逻辑 key | 值 | TTL / 作用 |
| --- | --- | --- |
| `openai-oauth:sessions:v2:{sessionId}` | AEAD 密文 session state | 总 TTL `30m`，原子状态机事实 |
| `openai-oauth:sessions:v2:owner:{ownerId}` | session 创建时间有序索引 | 与成员 session 同步清理，执行 owner 上限 |
| `openai-oauth:sessions:v2:all` | 全局创建时间有序索引 | 执行全局上限，清理过期成员 |
| `openai-oauth:refresh-locks:v2:{accountId}` | 随机 lease token | TTL 覆盖单次 token 请求和 CAS，不作业务事实 |
| `openai-oauth:refresh-failures:v2:{accountId}` | count / backoffUntil | 最长 `7d`，成功清理 |

session 明文结构只允许 `version`、owner ID、state hash、PKCE verifier、redirect URI、client ID、created / expires 时间、状态、lease 元数据、加密 token result、预分配 account ID 和成功 account ID。不得存 callback URL、用户提交的 code、账户表单或 operation log。owner 索引只能保存不可逆 owner hash，避免 Redis key 直接泄露系统账户 ID。

## 6. Token client 与 secrets

- 使用专用 `http.Client` / `Transport`，总请求 deadline `25s`；显式限制连接、TLS、响应 header 和响应 body，不使用 `http.DefaultClient`。
- 禁止自动跟随 token endpoint redirect。
- 代理只来自账户 proxy profile 或 `JUHE_AI_OAUTH_PROXY_URL`，协议复用现有 HTTP(S) / SOCKS allowlist；不得把请求参数当代理 URL。
- 响应使用 `io.LimitReader(max+1)`，超过 `256KiB` 立即返回 `oauth_upstream_unavailable`。
- token body 使用结构化 encoder；日志不得记录 request body、Authorization header 或响应原文。
- `expires_in` 严格解析为有限正整数，并检查加法溢出；`expires_at` 统一 UTC。
- JWT 只做有界 payload 解码以提取展示 metadata，不验证签名，因此 `email/account_id/plan_type` 不能用于授权、owner、去重或权限判断；超大、非法或非对象 payload 直接忽略 metadata。
- 凭据使用 `secretcrypto.NewJSONCodec` 的 Node-compatible AES-GCM `v1` 格式。加密失败不得写空串；解密失败对外只返回通用错误。
- credential fingerprint 只能由规范化的稳定身份字段生成，不包含 access token；如果无法证明现有账户唯一约束需要它，沿用账户创建 service 当前规则，不在 OAuth 层自行发明。

## 7. 账户存储与并发更新

### 7.1 创建

顺序固定为：

1. 完成本地权限、profile、group、模型和策略校验。
2. 领取 OAuth session 或校验 Refresh Token 请求。
3. 完成 token exchange。
4. 在 session `exchanged` 状态或 mutation claim 中保存预分配 account ID。
5. 加密凭据，在 PostgreSQL 事务内使用该 ID 创建账户、绑定分组 / 标签及写入必要关系。同 ID 已存在时只允许在 owner、provider、type 和本次幂等来源一致时回读，不得把任意冲突账户当作成功。
6. 提交后先完成 session / mutation success 标记，再投递 operation log 和 pending health check；旁路失败不回滚账户，但必须记录有界失败事件。
7. 返回脱敏账户。

数据库瞬时失败时，授权码路径依靠 `exchanged` session 恢复；账户事务已提交但 session complete 失败时，同一预分配 account ID 使重试只回读原账户。Refresh Token 创建依靠 mutation guard 保存预分配 ID 和成功结果，同一幂等指纹不得创建两个账户。

### 7.2 刷新与重新授权

- 外部 HTTP 期间不持有 PostgreSQL transaction / row lock。
- 读取账户时取得 `config_revision` 和密文，解密后请求上游。
- 写入使用现有 `ExpectedConfigRevision` CAS；成功时原子递增 revision。
- CAS 失败后只重读一次：若数据库已经有更新且未过期 token，则使用新值；若 refresh token 已轮换，则用最新 token 最多再交换一次；否则返回 `oauth_account_conflict`。
- 合并凭据时保留用户配置字段，覆盖服务端 token / identity 字段；上游未返回新 refresh token 时保留原 token。
- 重新授权只清理 `oauth_token_refresh_failed` 造成的失败状态；其他业务异常不被 OAuth 操作静默清除。`pending_test` 和 `disabled` 不自动变为 active。
- 成功写入后发布账户 / gateway runtime 精确失效；失效失败不得回滚已持久化的新 refresh token。

## 8. Access Token 保活 worker

Access Token 保活属于 `worker` owner，不随管理路由自动切换：

- 继续使用 `oauthAccessTokenRefreshIntervalSeconds`、`LeadSeconds`、`BatchSize` 和 `RetryBackoffSeconds` 当前设置范围与默认值。
- 候选只包含自有、未停止、OpenAI OAuth、存在 refresh token 且 token 缺失 / 无效 / 进入 lead window 的账户。
- PostgreSQL 只做有界候选读取；每账户通过 Redis lease 单飞，外部 HTTP 不占数据库连接。
- 一批候选用有界 goroutine fan-out，实际并发由独立 semaphore、HTTP Transport、Redis 和上游预算共同限制，不复制 Node 串行 `for`，也不允许无界 goroutine。
- 刷新结果通过 `config_revision` CAS 写入。失败计数和 backoff 使用有 TTL 的 Redis 状态；连续 3 次失败且非 `pending_test` 时写 `oauth_token_refresh_failed`，成功后只恢复该错误。
- job 必须输出 scanned / due / refreshed / failed / exceptioned / skipped-backoff、duration 和 queue lag；标签不含账户 ID、token 或用户邮箱。
- Asynq periodic task 只有 Go worker owner 时注册。Node scheduler 与 Go scheduler 不得同时运行同一刷新任务。

OAuth 额度快照仍只由真实网关请求或账户测试响应头被动更新；worker 不发模型请求拿额度。

## 9. Owner、切流与回滚

当前 `deploy/owner-manifest.json` 只有 `management`、`public`、`gateway`、`worker` 四个粗粒度 owner，不能把单个 OAuth 切片完成误写成整个 management 或 worker 已接管。

### 9.1 共存期

- `management=node`：生产 OAuth 六条路径仍只到 Node。Go 路由仅在 opt-in / 测试 listener 注册。
- `worker=node`：Node 继续单独执行 Access Token 保活。Go worker 可以运行纯测试 job，但不得注册生产 periodic task。
- `gateway=node`：Node 继续在真实请求前读取 / 刷新 OAuth token、执行 Codex adapter 和写被动额度快照。
- Node / Go 通过同一个稳定 `JUHE_AI_SECRET` 读写 `credentials_encrypted`；禁止双写同一账户 refresh token。

### 9.2 切流顺序

1. 合入 Node golden 和 Go contract tests，完成新 schema / Redis key 的向前兼容验证。
2. 只把两组 OAuth 路径精确路由到 Go，确认代理配置不会同时落到 Node；粗粒度 `management` owner 保持 Node，直到该域整体迁完。
3. 观察创建、重新授权、token refresh、operation log、健康任务和错误码；在途 Node session 不迁移，切流窗口前停止新增授权或接受用户重新生成链接。
4. Go ops worker ready 后先停 Node OAuth refresh scheduler，确认无运行中 lease，再启动 Go periodic task；任何时刻只允许一个 scheduler owner。
5. Go gateway 完成 OAuth 消费、Codex adapter、refresh-on-request 和被动快照后，才能切换 gateway owner。
6. 只有对应粗粒度域全部满足门禁时才更新 owner manifest；OAuth 单项完成只记录到迁移清单。

### 9.3 回滚

- 管理 HTTP 回滚：精确路由切回 Node。Go v2 在途 session 不向 Node 转换，统一失效并提示重新授权；已创建账户和密文可由 Node 直接读取。
- worker 回滚：先停止 Go periodic task并等待 job drain / lease 到期，再启动 Node scheduler；不得靠双跑提升可用性。
- gateway 回滚：切回 Node 前确认 Go 已提交的 refresh token 已持久化并完成缓存失效；Node 从 PostgreSQL 重读。
- 不回滚 token 到旧值，不执行双写补偿，不删除 Go 创建的有效账户。若新 schema 只含可空 / 独立运行态字段，Node 忽略；破坏性 schema 变化不得进入本迁移。
- Node OAuth 代码保留到用户明确启动减法阶段且回滚观察窗结束。

## 10. 已确认 Node 缺陷与 Go 处置

| 缺陷 | Node 当前行为 | Go 处置 | 证据 |
| --- | --- | --- | --- |
| session 在 token 成功前被消费 | compare-delete 后才发 token 请求；网络失败或上游 5xx 后同一 callback 无法重试 | processing lease + retryable release + encrypted exchanged state；终态才 consume | golden `session-consumed-before-token-success` 与 Node service 调用顺序 |
| 无稳定机器错误码 | 路由主要返回中文 message，冲突还依赖 `message.includes('已存在')` | 领域 typed error + 稳定顶层 `code`；中文仅用于展示 | golden `no-stable-machine-error-code` 与 Node route error mapper |

JWT claim 未验签只允许作为非权威展示 metadata；如果后续代码把这些 claim 用于权限、owner、唯一约束或调度，则是新缺陷，必须阻断实现并单独记录。

## 11. 验收门禁

第一轮必要门禁：

- Node migration golden 通过，且 Go contract test 覆盖六路由、PKCE、session 状态机、token request、凭据白名单、error code 和脱敏。
- Go 定向单元 / HTTP / store 测试、`go test` 目标包、目标 `go vet`、`git diff --check` 通过。
- 不需要真实 OpenAI 凭据即可完成假 token server、Redis 原子状态和 PostgreSQL CAS 验证。

统一验收轮门禁：

- 真实 PostgreSQL / Redis / Asynq、Node-created account -> Go refresh、Go-created account -> Node read 跨运行时通过。
- 真实 listener + 前端管理 / 个人路径通过，响应与日志没有 secrets。
- 并发 session 领取、租约过期、token rotation、CAS 竞争、worker 双实例和 shutdown drain 通过。
- 经批准的真实 OpenAI 小流量 create / reauthorize / refresh / gateway smoke 通过；未获凭据时明确保持未验证。
- 精确路由、scheduler 和 gateway owner 各自只有一个；回滚演练通过。
- 进入 Node 删除前，六条 route、session store、token client、refresh service、worker registry、前端调用、配置和测试的 Node 引用均有删除清单。
