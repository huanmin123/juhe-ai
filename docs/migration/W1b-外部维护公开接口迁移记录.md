# W1b `/__aipublic__` 外部维护公开接口迁移记录

## 基本信息

- 模块：`/__aipublic__` 外部维护公开接口
- 状态：Go 实现中（基础设施已补；public group、public route strategy、public API Key 与 public account 四类资源 16 个 CRUD 的 Go 纵切面代码已补；生产 router 支持 `JUHE_AI_PUBLIC_API_ENABLED` opt-in 挂载 guard，默认关闭；已新增 `w1b-public-api-smoke` 独立灰度验证入口；未正式接管）
- 迁移波次：W1b
- 当前 Node owner：`backend/src/modules/external-integrations/`、`backend/src/modules/public-api-logs/`、`backend/src/storage/external-integration-source*.ts`、`backend/src/storage/public-api-logs.repository.ts`；迁移期健康任务 bridge 还依赖 `backend/src/modules/internal-api/account-health-check-dispatch.routes.ts`、`backend/src/modules/accounts/account-health-check-dispatch.service.ts` 和 Node `ops-worker` 健康检查实现
- 目标 Go owner：`backend-go/internal/modules/publicapi`、`backend-go/internal/modules/publicapi/auth`、`backend-go/internal/modules/publicapi/ratelimit`、`backend-go/internal/modules/publicapilog`、`backend-go/internal/modules/publicgroups`、`backend-go/internal/modules/publicroutestrategies`、`backend-go/internal/modules/publicapikeys`、`backend-go/internal/modules/publicaccounts`、`backend-go/internal/jobs/publicapilog`、`backend-go/internal/jobs/worker`、`backend-go/internal/app/server.go`、`backend-go/internal/app/ingest_worker.go`、`backend-go/internal/config/config.go`、`backend-go/internal/httpapi/router.go`、`backend-go/internal/httpapi/public_api_shell.go`、`backend-go/internal/httpapi/public_groups.go`、`backend-go/internal/httpapi/public_route_strategies.go`、`backend-go/internal/httpapi/public_api_keys.go`、`backend-go/internal/httpapi/public_accounts.go`、`backend-go/internal/store/port/publicapi.go`、`backend-go/internal/store/postgres/publicapi.go`、`backend-go/internal/store/postgres/publicgroups.go`、`backend-go/internal/store/postgres/publicroutestrategies.go`、`backend-go/internal/store/postgres/publicapikeys.go`、`backend-go/internal/store/postgres/publicaccounts.go`、`backend-go/internal/store/postgres/queries/w1b_public_groups.sql`、`backend-go/internal/store/postgres/queries/w1b_public_route_strategies.sql`、`backend-go/internal/store/postgres/queries/w1b_public_api_keys.sql`、`backend-go/internal/store/postgres/queries/w1b_public_accounts.sql`、`backend-go/db/migrations/000004_w1b_public_groups.sql`、`backend-go/db/migrations/000005_w1b_public_accounts.sql`、`backend-go/internal/platform/accounthealthcheckdispatch`、`backend-go/internal/platform/redis`、`backend-go/internal/maintenance/w1bpublicapismoke.go` 和 `backend-go/cmd/juhe-ai-maintenance` 已作为 W1b 基础设施、四类公开资源纵切面、临时健康任务 adapter、opt-in 生产挂载 guard 和独立灰度 smoke 入口落地；account shell E2E 测试代码已补，Docker/testcontainers 环境真实 integration 与 shell E2E 复跑、反向代理切流和 Node 删除仍待完成
- 关联计划：[PLAN-0081 Node 转 Go 渐进减法迁移](../plans/计划-0081-Node转Go渐进减法迁移.md)
- 关联 bug：[BUG-0035 Go 公开账户默认模型语义漂移](../bug/问题-0035-Go公开账户默认模型语义漂移.md)
- 关联 bug：[BUG-0043 Go 公开账户支持模型更新未清理默认测试模型](../bug/问题-0043-Go公开账户支持模型更新未清理默认测试模型.md)
- 关联功能文档：[公开资源维护接口设计](../functions/公开资源维护接口设计.md)、[外部来源系统鉴权设计](../functions/外部来源系统鉴权设计.md)、[公开接口日志设计](../functions/公开接口日志设计.md)、[接口契约与权限矩阵](../functions/接口契约与权限矩阵.md)、[安全与日志策略](../functions/安全与日志策略.md)

## 当前契约

W1b 只包含 `/__aipublic__` 下 16 个外部维护 CRUD，不包含 W1a `GET /__aisys__/api/settings/public`，也不包含管理端 `GET /__aisys__/api/external-integration-sources/api-docs` 或来源 / token 管理接口。

所有公开接口都要求：

```http
Authorization: Bearer <source_token>
```

| 资源 | Method | Path | Scope | 当前 Node handler |
| --- | --- | --- | --- | --- |
| API Key | GET | `/__aipublic__/api-key/list` | `juhe_ai_public:api_key_list:read` | `listPublicApiKeysAsync` |
| API Key | POST | `/__aipublic__/api-key/add` | `juhe_ai_public:api_key_add:write` | `addPublicApiKeyAsync` |
| API Key | POST | `/__aipublic__/api-key/update` | `juhe_ai_public:api_key_update:write` | `updatePublicApiKeyAsync` |
| API Key | POST | `/__aipublic__/api-key/del` | `juhe_ai_public:api_key_delete:write` | `deletePublicApiKeyAsync` |
| 路由策略 | GET | `/__aipublic__/route-strategy/list` | `juhe_ai_public:route_strategy_list:read` | `listPublicRouteStrategiesAsync` |
| 路由策略 | POST | `/__aipublic__/route-strategy/add` | `juhe_ai_public:route_strategy_add:write` | `addPublicRouteStrategyAsync` |
| 路由策略 | POST | `/__aipublic__/route-strategy/update` | `juhe_ai_public:route_strategy_update:write` | `updatePublicRouteStrategyAsync` |
| 路由策略 | POST | `/__aipublic__/route-strategy/del` | `juhe_ai_public:route_strategy_delete:write` | `deletePublicRouteStrategyAsync` |
| 分组 | GET | `/__aipublic__/group/list` | `juhe_ai_public:group_list:read` | `listPublicGroupsAsync` |
| 分组 | POST | `/__aipublic__/group/add` | `juhe_ai_public:group_add:write` | `addPublicGroupAsync` |
| 分组 | POST | `/__aipublic__/group/update` | `juhe_ai_public:group_update:write` | `updatePublicGroupAsync` |
| 分组 | POST | `/__aipublic__/group/del` | `juhe_ai_public:group_delete:write` | `deletePublicGroupAsync` |
| AI 账户 | GET | `/__aipublic__/account/list` | `juhe_ai_public:account_list:read` | `listPublicWelfareAccountsAsync` |
| AI 账户 | POST | `/__aipublic__/account/add` | `juhe_ai_public:account_add:write` | `addPublicWelfareAccountAsync` |
| AI 账户 | POST | `/__aipublic__/account/update` | `juhe_ai_public:account_update:write` | `updatePublicWelfareAccountAsync` |
| AI 账户 | POST | `/__aipublic__/account/del` | `juhe_ai_public:account_delete:write` | `deletePublicWelfareAccountAsync` |

旧公开路径已经移除，W1b Go 接管后仍必须继续返回公开前缀 404，不能恢复为新契约：

```text
GET /__aipublic__/demo/source-auth
GET /__aipublic__/ip/usage
GET /__aipublic__/account/usage
GET /__aipublic__/consumption/ranking
GET /__aipublic__/access/info
```

## 横切契约

| 项 | 当前契约 |
| --- | --- |
| 路由归属 | `__aipublic__` 不能落入 `/v1/*` 网关 catch-all，也不能落入前端静态兜底 |
| JSON body | POST JSON body 上限 `256kb`；无效 JSON 返回 `400 { message: "请求体无效" }`，过大返回 `413 { message: "请求体过大" }` |
| 成功响应 | 统一 `{ data: ... }` |
| 普通业务错误 | 通常为 `{ message: "..." }` |
| Bearer 解析 | 只读 `Authorization` header；`Bearer` 大小写不敏感；不支持 query/body token |
| token 摘要 | 对明文 token 计算 `sha256(hex)`，用途前缀为 `external-integration-source-token:` |
| source/token 校验 | source 和 token 都必须 active、未过期；scope 使用 source scopes 与 token scopes 的交集 |
| 鉴权错误 | 缺 token `401 external_source_token_missing`；无效 token `401 external_source_unauthorized`；source disabled/expired 为 `403`；token unavailable 为 `401`；scope 不足为 `403 external_source_scope_forbidden` |
| last_used | 鉴权成功后 touch source/token 的 `last_used_at` 和 `updated_at`；60 秒节流；限频发生在鉴权成功之后，因此 429 请求也可能 touch |
| 限频 | source 级规则，同一 `sourceRefId:tokenId:tokenPrefix` 共用额度；无规则不限频；超限 `429`、`Retry-After`、`external_source_rate_limited` 和 `details.windowSeconds/maxRequests/retryAfterSeconds` |
| 内置测试 token | source `extsrc_builtin_test`、token `exttok_builtin_test`、默认 `60s/10` 限频、全量当前公开 scope、只返回 mock |
| 公开接口日志 | `/__aipublic__` 前缀下成功、失败、鉴权失败、限频、公开前缀 404、JSON 解析失败、body 过大、客户端提前断开都写日志；客户端提前断开按 `499`、`public_api_client_closed` 和响应快照 `statusCode=499` 记录 |
| 日志快照 | 请求 headers 只捕获 `contentType` / `contentLength`，`Authorization`、Cookie 和作为 Bearer 凭据传入的来源 token 不进入快照；query/body/response body 在 32KB 单侧预算、深度 8、每对象 / 数组 200 条目和单字符串 4096 字节预览内保留原值，不递归脱敏、不写 `[redacted]`，状态可为 `complete/truncated/empty/dropped` |

2026-07-14 Go 漂移修复已删除 `SanitizeQueryString`、敏感字段名匹配和 secret-like 字符串替换，`query_string` 改为保存 `r.URL.RawQuery`，request / response snapshot 与 Node 当前 `test:public-api-logs` 原值契约对齐。该变化只调整日志快照，不改变公开 API 业务响应白名单：API Key 完整 key 仍只在新增响应返回一次，账户响应仍不返回 `apiKey`、`baseUrl` 或 `credentials`。

未决兼容任务（不得计为完整 parity）：Go `requestData.query` 仍由 `r.URL.Query()` 经 `publicAPIQueryMap` 生成扁平 map，Node Express 4.22.1 则使用 qs@6.14.2 extended parser；因此 bracket query 与畸形转义输入的解析后 query 形态 / 错误处理尚未完全等价。`queryString` 已精确保留 `RawQuery`，原始请求证据不会因此丢失，但解析后 query 的兼容决策与回归用例必须作为独立任务完成，完成前不能宣告 W1b query 完整 parity。

## 生产挂载 guard

Go 主 server 已补 W1b opt-in guard：`JUHE_AI_PUBLIC_API_ENABLED=false` 时，生产 router 不注册 `/__aipublic__`，请求仍返回根路由通用 `404 { error: "接口不存在" }`；`JUHE_AI_PUBLIC_API_ENABLED=true` 时，Go 主 server 才把 `/__aipublic__` 和 `/__aipublic__/*` 交给 W1b public shell。

开启该 guard 的前置条件：

- `JUHE_AI_POSTGRES_URL` 指向已执行当前 Go migration 的 PostgreSQL。
- `JUHE_AI_REDIS_STATE_URL` 用于公开来源 token 限频和系统 API IP 读限流。
- `JUHE_AI_REDIS_QUEUE_URL` 用于 Asynq `public-api-logs` 日志队列。
- `JUHE_AI_SECRET` 必须显式配置且不少于 32 个字符，用于 AI 账户上游凭据 AES-GCM 加密；公开接口开启时不允许落回开发默认密钥。
- `juhe-ai-worker ingest` 已启动并能消费 `public-api-log:write`。

该 guard 只是灰度验证入口，不等于 W1b 正式接管，不等于可以删除 Node。正式切流前仍必须完成 Docker/testcontainers 真实 PG/Redis/Asynq integration、account shell E2E 真实通过、公开日志副作用检查、反向代理单 owner 切换和 Node 删除证据。严禁让 Node 和 Go 同时作为同一公网 `/__aipublic__/*` 路径的正式 owner；回滚优先关闭 `JUHE_AI_PUBLIC_API_ENABLED` 并恢复反向代理 owner。

### 生产公开账户健康任务 bridge

Go public account 只在新增 `pending_test` 账户提交成功后以 `activation` 调用 Node；调用脱离客户端请求取消并异步执行，受 `JUHE_AI_NODE_INTERNAL_REQUEST_TIMEOUT` 限制，不阻塞新增响应。公开账户更新不再即时发送 `configuration`，只在 PostgreSQL 中写入 `next_health_check_at` 等后台检查调度状态，由现有 worker 消费。配置为无默认且 public API 开启时必填的 `JUHE_AI_NODE_INTERNAL_BASE_URL`，只允许 `http` loopback IP literal + 显式端口；timeout 默认 `2s`、范围 `100ms..10s`；双方使用 trim 后一致的 `JUHE_AI_SECRET`。

内部协议固定为 `POST /__aiinternal__/v1/account-health-check/dispatch`，原始 JSON 最大 `1024 bytes`，字段仅 `accountId/reason`，签名为 HMAC-SHA256（域 `juhe-ai:account-health-check-dispatch:v1\n` + 原始 body），仅 HTTP `202` 成功。Node router 校验 loopback、identity encoding、签名和 strict payload 后调用现有 dispatch。Go 新增提交后的异步 activation 是 best-effort，失败记录告警但不回滚写入、不重试、不构成可靠交付；更新路径不调用该 bridge。

过渡期 Node Web 与 `ops-worker` 仍需运行，Node 先就绪但 Go 启动不做存活探测。当前仅支持同主机、同网络命名空间，internal path 不得反代公网；分容器无法直接使用 loopback，当前不支持。W7 接管健康任务时删除临时 Go adapter、Node internal route 和两个 `JUHE_AI_NODE_INTERNAL_*` 环境变量。

## 独立 maintenance smoke

W1b 已新增独立真实依赖 smoke：

```powershell
Set-Location backend-go
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke
```

前置条件：

- PostgreSQL 已执行当前 Go migration，至少包含 `juhe_business.external_integration_sources`、`juhe_business.external_integration_source_tokens` 和 `juhe_dataset.public_api_logs`。
- Redis cache、Redis state 与 Redis queue 可连接，且不能配置为同一个 Redis DB。
- `JUHE_AI_SECRET` 不少于 32 个字符。
- `go run ./cmd/juhe-ai-worker ingest` 已在另一个进程启动，并连接同一个 PostgreSQL 与 Redis queue。

命令验证范围：

- `JUHE_AI_PUBLIC_API_ENABLED=false` 默认 router guard 仍返回根路由 `404 { error: "接口不存在" }`。
- 本命令内部把 Go config copy 临时设为 `PublicAPIEnabled=true`，用生产 public API handler 组装路径和本地 `httptest` 发起 `GET /__aipublic__/group/list`，不启动真实监听端口。
- 使用临时内置测试 source/token fixture 触发 `IsTestToken` mock 分支，避免写业务分组、账号、API Key 或路由策略资源。
- 使用真实 PostgreSQL auth store、Redis cache invalidator 装配、Redis state 限频、Redis queue 和 Asynq public API log 入队路径。
- 通过固定 public log ID 轮询 `juhe_dataset.public_api_logs.id`，确认 external `juhe-ai-worker ingest` 已把本次 public API log 写入 PostgreSQL。
- 结束时按 sentinel 清理临时 smoke token；如果命令临时创建了内置测试 source，也只在没有其他 token 时删除该 sentinel source。

命令输出边界：

- `success=true` 只代表 `local_httptest_public_api_smoke` 通过。
- `takeoverEvidence` 固定为 `false`。
- `takeoverAssessment.productionTakeoverNotEvaluated` 固定为 `true`。
- smoke 注入 no-op 健康检查 dispatcher，不连接 Node bridge。
- 通过不证明 Go server 正在真实端口监听，不证明反向代理已切流，不证明生产流量由 Go 处理，不证明 Node 入口可以删除。
- 如果 `publicAPILogIngest` 失败，优先检查 `juhe-ai-worker ingest` 是否启动、是否连接同一个 Redis queue / PostgreSQL，以及 public API log worker 是否有 retry / archived 任务。

## 资源字段边界

### API Key

- `list`：`targetUsername` 必填；可选 `routeStrategyId`、`keyword`、`status=active|disabled|all`、`page`、`pageSize`。
- `add`：`targetUsername`、`name`、`routeStrategyId` 必填；可选 `description|null`、`status`、`expiresAt`、`quotaLimits|null`、`availabilitySchedule|null`。
- `update`：`apiKeyId` 必填；`targetUsername` 可选归属校验；至少提交一个变更字段。
- `del`：`apiKeyId` 必填；`targetUsername` 可选归属校验。
- 响应只在 `add` 本次返回完整 `key`；`list/update/del` 只返回 `keyPrefix/keySuffix` 等摘要。Go 侧同时回显已规范化的 `quotaLimits` / `availabilitySchedule` 便于审计，但不回显完整 secret。API Key 只绑定 `routeStrategyId`，不接收旧式分组绑定字段。

### 路由策略

- `list`：`targetUsername` 必填；可选 `keyword`、`mode=normal|hybrid_smart|weighted|failover|round_robin|all`、`status=active|disabled|all`、`page`、`pageSize`。
- `add`：`targetUsername`、`name`、`groupBindings[]` 必填；`groupBindings[]` 中 `groupId` 必填，`priority` 为正整数，`weight=1..100`，`status=active|disabled`。
- `update`：`routeStrategyId` 必填；`targetUsername` 可选归属校验；`groupBindings` 提供时整体覆盖。
- `del`：`routeStrategyId` 必填；`targetUsername` 可选归属校验。
- 响应返回路由配置、分组绑定摘要和 `apiKeyCount`，不返回账号凭据或 API Key 明文。

### 分组

- `list`：`targetUsername` 必填；可选 `providerCode`、`keyword`、`page`、`pageSize`。
- `add`：`targetUsername`、`name`、`providerCode` 必填；可选 `targetDisplayName`、`description`、`enabled`、`groupType=personal|high_concurrency`。
- `update`：`groupId` 必填；`targetUsername` 可选归属校验；至少提交一个变更字段。
- `del`：`groupId` 必填；`targetUsername` 可选归属校验。
- 响应不返回组内账号、账号凭据、用量、并发快照或账号 ID 列表。

### AI 账户

- `list`：`targetUsername` 必填；可选 `targetGroupName`、`providerCode`、`providerProtocolProfileId`、`groupId`、`keyword`、`type`、`status`、`schedulable=all|enabled|disabled|cooling`、`page`、`pageSize`。按 `targetGroupName` 或 `providerProtocolProfileId` 筛选时必须同时带 `providerCode`。
- `add`：`targetUsername`、`targetGroupName`、`providerCode`、`providerProtocolProfileId`、`name`、`type=api_key`、`baseUrl`、`apiKey` 必填；可选 `targetDisplayName`、`supportedModels`、`status`、`concurrencyLimit=1..100000`、`priority=0..100000`、`availabilitySchedule|null`、`notes`。除 `availabilitySchedule` 外，可选字段显式 `null` 均返回 `400`；`notes` 允许 trim 后空字符串。`supportedModels` 省略时继承 `providers.default_supported_models_json`；显式非空数组按 trim、去空、去重后的结果写入；显式 `[]` 或 provider 默认值最终为空时返回 HTTP `400 { message: "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型" }`。
- `update`：`accountId` 必填；`targetUsername`、`targetGroupName`、`providerCode`、`providerProtocolProfileId`、`type` 只作为防误改校验，不属于可变字段，只有 `type` 的请求仍因没有真实变更字段返回 `400`；除 `availabilitySchedule` 外，可选字段显式 `null` 均返回 `400`，`notes` 允许 trim 后空字符串。`supportedModels` 省略或标准化后与当前集合无序等价时不校验模型目录、不重写模型绑定；每次账户更新都按最终模型集合自愈 `default_test_model`，不再包含现有默认测试模型时同事务清为 `NULL`，仍包含时保留。
- `del`：`accountId` 必填；可选归属、防误删校验字段。
- 响应不返回 `credentials`、上游 `apiKey`、`baseUrl`、OAuth token、代理密码、加密字段、凭据指纹或账户 `default_test_model`。`clientCompatibility` 是只读派生摘要，不作为入参。

## 关键状态语义

- 列表响应包含 `source/generatedAt/target/page/pageSize/pageUpperBound/hasMore/items`；`pageUpperBound` 是翻页上界，不是精确总数。
- 请求 schema 是 strict，多余字段应返回 `400`；GET 的 `page/pageSize` 可 coerce，POST 数字和布尔不 coerce。
- `group/add` 同用户、同供应商、同名分组已存在时幂等成功，HTTP `201`，`action=existing`。
- `account/add` 同目标分组、供应商、协议档案、账号名已存在时返回 `409`。
- `account/add` 的重复名称检查保持 Node 当前顺序，优先于支持模型非空校验；重复名称并同时提交 `supportedModels: []` 时仍先返回名称冲突。
- API Key、路由策略、分组名称唯一冲突返回 `409`。
- `group/update|del`、`api-key/update|del`、`route-strategy/update|del` 找不到时返回 `404 { message: "...不存在" }`。
- `account/update` 找不到或归属校验不匹配时返回 `404`；`account/del` 常规找不到时返回 `200 { data: { action: "not_found", account: null } }`。
- 删除默认分组、默认 API Key、默认路由策略，或删除仍被 API Key 使用的路由策略，应被拒绝。
- `group/add` 和 `account/add` 会自动创建目标用户；`account/add` 也会自动创建目标分组；`route-strategy/add` 与 `api-key/add` 要求目标用户已存在。
- `account/add status=active` 当前实际落为 `pending_test` 且 `schedulable=false`；`status=disabled` 仍为 disabled。
- `account/add` 按供应商凭据 driver 默认值写入 `supported_endpoint_modes`；`account/update` 解密实际凭据并校验或补齐该字段，健康检查 endpoint family 只依据实际凭据能力选择，不以 profile capability 代替。hybrid 账户继续保留跨协议 endpoint mode。
- 现有非空 `api_keys` 池是运行时权威来源；公开接口提交单个 `apiKey` 不破坏已有池，规范化后的 `api_key`、credential fingerprint 和 mask 始终指向池首项。只有显式提交 `priority` 才更新 `group_accounts.local_priority`，改名、备注、并发等更新不触碰分组绑定优先级。
- 当前只有账号公开 add/update/delete 写 operation log；分组、路由策略和 API Key 公开写入不在该位置写 operation log。

## 当前存储事实

当前 Node 实现仍有 SQLite / PostgreSQL 双路径；W1b Go 目标必须只走 PostgreSQL + Redis，不新增 SQLite driver、SQLite 路径配置或 Node DB service/IPC 分支。

| 数据 | 当前 Node 表 / 队列 | Go 目标 |
| --- | --- | --- |
| 来源授权 | `external_integration_sources` | PostgreSQL 当前 schema，Go store port 隔离 SQL 类型 |
| 来源 token | `external_integration_source_tokens`，`token_hash` 唯一 | PostgreSQL 点查，不扫描 token 表 |
| 公开接口日志 | `public_api_logs` | fresh Goose `juhe_dataset.public_api_logs`；生产切流前需从 Node PostgreSQL 旧结构离线同步，不能直接共用旧表 |
| 公开分组目标用户 | `system_accounts` | PostgreSQL `juhe_business.system_accounts`，`group/add` 可自动创建目标用户，运行路径不引入 SQLite |
| 公开分组供应商 | `providers` | PostgreSQL `juhe_business.providers`，只接受已存在且启用的 provider |
| 公开分组 | `groups`、`group_accounts`、`route_strategies`、`route_strategy_groups` | PostgreSQL `juhe_business.groups` 及关联表，覆盖同用户同供应商同名唯一、分页、默认分组保护、账号绑定保护和活跃策略路由保护 |
| 公开路由策略 | `route_strategies`、`route_strategy_groups`、`api_keys` | PostgreSQL `juhe_business.route_strategies`、`route_strategy_groups` 和最小 `api_keys` 删除保护表，覆盖同用户同名唯一、mode/config/status 约束、绑定优先级 / 权重约束、活跃绑定 priority 唯一、默认策略保护和 API Key 使用保护 |
| 公开 API Key | `api_keys`、`route_strategies`、`system_accounts` | PostgreSQL `juhe_business.api_keys`、`route_strategies` 和 `system_accounts`，覆盖目标用户必须已存在且 active、`routeStrategyId` 同 owner 且 active、同用户同名唯一、默认 API Key 删除保护、secret hash / prefix / suffix 存储、`add` 仅本次返回完整 `key`、`list/update/del` 只返回摘要；Go 目标采用 hash-only，不保存可逆 `key_secret_encrypted` |
| 公开 AI 账户 | `accounts`、`account_supported_models`、`group_accounts`、`provider_protocol_profiles`、`providers`、`groups`、`system_accounts` | PostgreSQL `juhe_business.accounts`、`account_supported_models`、`group_accounts` 和供应商协议档案表，覆盖目标用户 / 目标分组自动创建、provider/profile 启用校验、同目标分组同供应商同协议档案同名唯一、上游凭据 AES-GCM 可逆加密、凭据指纹 hash、凭据摘要、软删除、绑定清理、列表分页和响应白名单；AI 账户凭据未来网关要使用，不能 hash-only |

说明：当前 hash-only 边界特指业务 `api_keys`。W1b 来源系统 token 仍沿用 `external_integration_source_tokens.token_hash` + `token_secret_encrypted` 的现有鉴权存储契约，后续如果要把来源 token 也改为不可逆存储，需要单独立迁移记录和删除验证。
| 来源限频 | 当前 Node 可用本地 store / Redis 等价逻辑 | Redis state 原子实现，生产缺 Redis 必须 fail-fast |
| 公开日志队列 | Node 本地队列 / Redis Stream / DB service IPC | Go 侧应使用 Asynq 或明确的可靠队列 adapter，不保留 Node IPC |

Node PostgreSQL 旧结构由 SQLite DDL 转换生成，`is_test_token` / `success` 为 `integer`、`duration_ms` 为 `integer`、`started_at` / `ended_at` / `created_at` 为 `text`；Go fresh Goose `000003` 使用 `boolean`、`bigint` 和 `timestamptz`。两者物理类型不兼容。生产切流前必须在受控停写窗口离线完成结构 / 数据同步，并确定单一写入 owner；不允许 Node / Go 同时写同一张表，也不在 runtime 增加双读、类型转换或旧结构兼容分支。

公开接口日志保留期由 `publicApiLogRetentionDays` 控制，默认 30 天，合法范围 `1..365`。Go 接管公开日志后，清理任务必须按批次删除，不能在请求路径同步清理。

## Go 实现范围

当前已补 Go 基础设施：

- `backend-go/internal/modules/publicapi`：固定 W1b `Prefix`、`Bearer` auth type、`256KB` JSON body limit、16 个 method/path/scope、内置测试 token ID 和旧公开路径不进入 catalog 的测试。
- `backend-go/internal/modules/publicapi/auth`：固定 Bearer 解析、token hash namespace、source/token 状态和过期、scope 交集、AuthError code/message/status、`last_used_at` 60 秒 touch 判断和 source/token rate-limit key。
- `backend-go/internal/modules/publicapi/ratelimit`：新增 W1b source/token 维度 penalty-window limiter，映射 `sourceRefId:tokenId:tokenPrefix`、命中规则和 `Retry-After`。
- `backend-go/internal/modules/publicapilog`：公开接口日志 request / response 快照构造已对齐 Node 当前原值语义：query/body/response 在 32KB 单侧预算、深度 / 条目 / 字符串预算内保留原值，不做字段名或字符串递归脱敏；请求 headers 只捕获 `contentType` / `contentLength`，不捕获 `Authorization`、Cookie 或作为 Bearer 凭据传入的来源 token；继续保留 `complete/truncated/empty/dropped`、body rejected、499 和业务错误摘要。
- `backend-go/internal/jobs/publicapilog`：新增 `public-api-log:write` Asynq task payload、`public-api-logs` queue 入队封装、30 秒 timeout、24 小时 retention、10 次 retry 和 handler；handler 只通过 `PublicAPILogStore` 幂等写入 PostgreSQL。
- `backend-go/internal/jobs/worker`、`backend-go/internal/app/ingest_worker.go` 和 `backend-go/cmd/juhe-ai-worker`：新增 W1b public API log 的 `juhe-ai-worker ingest` 装配，监听 `public-api-logs` 队列，注册 `public-api-log:write`，启动前检查 PostgreSQL 和 Redis queue，shutdown 由项目 context 触发；坏 payload 映射为 Asynq `SkipRetry`，store 写入错误仍交给 Asynq retry。
- `backend-go/internal/config/config.go`、`backend-go/internal/app/server.go` 和 `backend-go/internal/httpapi/router.go`：新增 `JUHE_AI_PUBLIC_API_ENABLED` 默认关闭的生产 router opt-in guard；开启时 fail-fast 要求 Redis cache、Redis state、Redis queue 和不少于 32 字符的 `JUHE_AI_SECRET`，server 装配 Bearer auth、Redis penalty-window limiter、网关共享 cache/state invalidator、Asynq log queue、四类资源 handler，并验证 16 个 catalog endpoint 都有 handler。
- `backend-go/internal/app/server.go` 同时暴露可注入 public API log ID 的 handler 构造入口，供 maintenance smoke 复用生产 public API handler 组装路径，同时避免按未建索引的 `trace_id` 扫描 public API log。
- `backend-go/internal/maintenance/w1bpublicapismoke.go` 和 `backend-go/cmd/juhe-ai-maintenance`：新增 `w1b-public-api-smoke`，覆盖默认 router guard、显式 opt-in mount、真实 PostgreSQL / Redis cache / Redis state / Redis queue、共享 invalidator 装配、临时内置测试 source/token、public API log 入队和 external `juhe-ai-worker ingest` 写 PG；当前请求仍只覆盖 group list，不把 API Key 缓存失效记为真实端到端通过；输出固定不提供生产接管证据。
- `backend-go/internal/httpapi/public_api_shell.go`：新增 W1b HTTP shell / capture 契约组合，覆盖 16 个 catalog endpoint 进入 auth / limiter / injected handler、旧公开路径 404、JSON body `400`、body 超限 `413`、`401/403/429`、response writer capture、499 客户端提前断开、`httpsnoop` 保留底层 `ResponseWriter` 可选接口组合、source context 写日志和 public API log 异步入队；生产 router 挂载时复用外层 request id，避免重复生成 trace。
- `backend-go/internal/httpapi/public_groups.go`：新增 public group list/add/update/delete handler，接入 HTTP shell 的 injected handler map；覆盖 strict body、GET pagination coerce、POST 数字 / 布尔不 coerce、内置测试 token mock、中文错误和统一 `{ data: ... }` 响应。
- `backend-go/internal/httpapi/public_route_strategies.go`：新增 public route strategy list/add/update/delete handler，接入 HTTP shell 的 injected handler map；覆盖 strict query/body、GET 分页 coerce、POST 数字不 coerce、`groupBindings` 嵌套字段白名单、内置测试 token mock、中文错误和统一 `{ data: ... }` 响应。
- `backend-go/internal/httpapi/public_api_keys.go` 和 `backend-go/internal/modules/publicapikeys`：新增 public API Key list/add/update/delete handler 与 service，接入 HTTP shell 的 injected handler map；覆盖 strict query/body、GET 分页 coerce、POST 数字 / 布尔不 coerce、`quotaLimits` / `availabilitySchedule` 对象白名单、内置测试 token mock、中文错误、统一 `{ data: ... }` 响应，以及 `add` 返回完整 `key`、其他响应只返回摘要；create 提交后 best-effort 发布 runtime/quota 失效，update/delete 提交后先刷新 validation shared cache version，再 best-effort 发布 runtime/quota 失效。
- `backend-go/internal/httpapi/public_accounts.go`：新增 public account list/add/update/delete handler，接入 HTTP shell 的 injected handler map；覆盖 strict query/body、GET 分页 coerce、POST 数字不 coerce、`type=api_key` 白名单、可选字段 `null` 边界、内置测试 token mock、中文错误、统一 `{ data: ... }` 响应，以及业务响应不返回 `apiKey` / `baseUrl` / `credentials`。`account/add` 使用 `StringListValue` 保留 `supportedModels` 的字段省略、显式空数组和显式非空数组三态，不能在 HTTP 边界提前折叠。
- `backend-go/internal/modules/publicgroups`：新增分组公开 CRUD service，覆盖目标用户读取 / `group/add` 自动创建、目标用户 active 校验、provider 存在且启用校验、同用户同供应商同名 `existing` 幂等、并发 target / group 唯一冲突后的事务级重试、默认分组修改 / 删除保护、修改 provider 前账号绑定保护、停用 / 删除前活跃策略路由唯一可用分组保护和响应白名单。
- `backend-go/internal/modules/publicroutestrategies`：新增路由策略公开 CRUD service，覆盖目标用户必须已存在且 active、同名冲突 `409`、`update/delete` 先按 `routeStrategyId` 定位 owner 后做可选 `targetUsername` 归属校验、`groupBindings` 整体覆盖、默认策略删除保护、API Key 使用保护、普通 / 故障回退模式规则、绑定重复 / active priority 冲突 / 停用分组 active 绑定拒绝、响应白名单和 `apiKeyCount` 摘要。
- `backend-go/internal/modules/publicapikeys`：新增 API Key 公开 CRUD service，覆盖目标用户必须已存在且 active、路由策略同 owner 且 active 校验、同名冲突 `409`、`update/delete` 可选 `targetUsername` 归属校验、至少一个变更字段、默认 API Key 删除保护、默认 API Key 禁止更换策略路由、`quotaLimits` / `availabilitySchedule` 规范化、schedule 覆盖 status、hash-only secret 存储和响应白名单。
- `backend-go/internal/modules/publicaccounts`：新增 AI 账户公开 CRUD service，覆盖目标用户 / 目标分组自动创建、目标 active 校验、provider/profile 存在且启用、公开接口只允许 `type=api_key`、上游 `baseUrl` 禁止 userinfo / localhost / 内网 / 保留 IP、上游凭据和 endpoint mode 规范化、多 API Key 池权威顺序、凭据指纹 hash、同目标分组同供应商同协议档案同名冲突 `409`、`pending_test -> active` 禁止公开接口直启、`update` 不移动 owner / group / provider / profile、显式 priority presence、`delete` 软删除并清理绑定、`not_found` 幂等响应和响应白名单。新增时先保持重复名称冲突优先，再按字段 presence 选择调用方模型或 provider 默认模型，并对最终归一化结果执行非空校验；新增提交后异步 activation，更新只持久化后台检查调度。
- `backend-go/internal/platform/redis`：新增 Redis Lua penalty-window helper，保持阻断期间惩罚延长语义；现有 fixed-window 继续只服务 W1a 等固定窗口场景。
- `backend-go/db/migrations/000003_w1b_public_api_foundation.sql`：新增 fresh Goose `juhe_business.external_integration_sources`、`juhe_business.external_integration_source_tokens` 和 `juhe_dataset.public_api_logs`，直接使用 PostgreSQL 原生 `timestamptz`、`boolean`、`bigint`；该结构不兼容 Node PostgreSQL 旧表，切流依赖离线同步而非运行时兼容。
- `backend-go/db/migrations/000004_w1b_public_groups.sql`：新增 W1b public group、public route strategy 与 public API Key 纵切面需要的 `system_accounts`、`providers`、`groups`、`group_accounts`、`route_strategies`、`route_strategy_groups` 和 `api_keys` PostgreSQL schema / 索引 / provider seed；`group_accounts`、`route_strategy_groups`、`api_keys` 使用 owner 复合 FK 约束，避免跨系统账户错误绑定；`api_keys` 固定 hash / prefix / suffix 非空、hash 唯一、同 owner name 唯一、quota / schedule JSON object 约束和 schedule next-check 索引；down 为 no-op，按业务数据处理。
- `backend-go/db/migrations/000005_w1b_public_accounts.sql`：新增 `protocols`、`provider_protocol_profiles`、`accounts`、`account_supported_models` 和 `group_accounts -> accounts` owner 复合 FK；seed 当前公开账号所需供应商协议档案；`accounts` 保存可逆加密凭据、凭据指纹、调度字段、冷却 / 错误摘要、`default_test_model`、软删除字段和列表索引。W1b 公开账户默认模型使用当前 provider seed 事实，`000008_w2_management_provider_options.sql` 中 `gpt` / `openai` 的 GPT-5.6 默认模型由静态 guard 固定。
- `backend-go/internal/store/port/publicapi.go`：固定 `PublicAPIAuthStore`、`PublicAPILogStore`、`PublicGroupStore` 和 `PublicGroupTransactor` port，业务层只接触 token hash、auth record、last_used touch、公开日志和分组业务 DTO，不暴露 pgx/sqlc/Redis 类型。
- `backend-go/internal/store/postgres/publicapi.go`：新增 W1b auth token 点查、`last_used_at` touch 和公开接口日志 27 列幂等写入 adapter。
- `backend-go/internal/store/postgres/publicgroups.go` 和 `backend-go/internal/store/postgres/queries/w1b_public_groups.sql`：新增 PostgreSQL public group store / sqlc query，覆盖事务、目标用户、provider、列表分页、同名唯一冲突、更新、删除、账号绑定计数和活跃策略路由唯一可用分组计数；停用 / 删除保护会在同一事务内按 route strategy ID 顺序锁定受影响的 active route strategy 行，再重新计算可用分组，避免同一策略下多个分组并发停用 / 删除后变成 0 个可用分组。
- `backend-go/internal/store/postgres/publicroutestrategies.go` 和 `backend-go/internal/store/postgres/queries/w1b_public_route_strategies.sql`：新增 PostgreSQL public route strategy store / sqlc query，覆盖事务、目标用户、列表分页、路由策略行锁、分组绑定读取和整体替换、同名唯一冲突、默认策略删除保护的前置查询、API Key 使用计数，以及 owner-only 可绑定分组查询。
- `backend-go/internal/store/postgres/publicapikeys.go` 和 `backend-go/internal/store/postgres/queries/w1b_public_api_keys.sql`：新增 PostgreSQL public API Key store / sqlc query，覆盖事务、目标用户、列表分页、API Key 行锁、路由策略行锁 / owner 校验、同名唯一冲突、hash 唯一冲突、create / update / delete 和 summary join。
- `backend-go/internal/store/postgres/publicaccounts.go` 和 `backend-go/internal/store/postgres/queries/w1b_public_accounts.sql`：新增 PostgreSQL public account store / sqlc query，覆盖事务、目标用户 / 分组 / provider profile、列表分页、账号行锁、同名唯一冲突、账号创建、分组绑定创建、模型替换、仅在显式 priority 输入时更新分组绑定调度、软删除和 summary join；provider profile 查询联表 `providers` 返回并解码 `default_supported_models_json`，供 service 处理字段省略时的默认继承。
- `backend-go/internal/httpapi/public_api_takeover_guard_test.go`：固定生产 router 当前没有注册 `/__aipublic__`，避免骨架期误接管。

当前未补：生产路由挂载、Docker/testcontainers 环境真实 integration 与四类资源完整 shell E2E 复跑、反向代理切流和 Node 删除。HTTP shell / capture / public group handler / public route strategy handler / public API Key handler / public account handler 当前只在显式构造和测试中使用，不代表 `/__aipublic__` 生产接管。

public group、public route strategy、public API Key 和 public account 是 W1b 当前已落地的四条真实资源纵切面，已经把 HTTP shell、handler、service、store port、PostgreSQL query 和 schema 串起来；它们只证明“资源可以按 Go + PostgreSQL + Redis 单模式落地”，不改变 `/__aipublic__` 整体 owner。生产路由仍由 Node 承担，Go 侧不代理 Node、不双写、不引入 SQLite / DB service / IPC 分支。

W1b 不应一次性把 16 个 CRUD 揉成一个巨大 service。建议按纵切和资源分阶段推进：

1. `publicapi` catalog、scope 常量、统一响应和错误结构。
2. `publicapi/auth`：Bearer 解析、token hash 点查、source/token 状态和过期、scope 交集、last_used touch。
3. Redis state penalty-window 限频：保持 `Retry-After`、details 字段和阻断期间惩罚延长语义。
4. `publicapilog`：已补 32KB 快照、499、错误摘要、任务 payload、异步入队、handler、W1b public API log worker runtime，以及不挂生产 router 的 HTTP shell / response capture / 公开前缀 404 / 400 / 413 场景契约测试。请求路径只入队，不直接写 PG。
5. 资源顺序仍为分组、路由策略、API Key、AI 账户。四类资源均已推进到 handler/service/store/query/schema，四类 shell E2E 测试代码已补；后续重点是 Docker/testcontainers 环境真实 integration 与 shell E2E 复跑、评估生产 router 挂载与反向代理切流，不提前删除 Node。

Go runtime 不调用 Node，不代理 Node，不移植 `databaseDriver` / SQLite / DB service / IPC / 本地队列分支。Node 只作为迁移前契约对照。

## Node 删除范围

只有 W1b 全部通过 Go 验收并完成路径切流后，才能删除：

- `/__aipublic__` Node proxy / route owner。
- `backend/src/modules/external-integrations/` 中仅服务公开 16 个 CRUD 的 route/service/payload/sanitize/mock/catalog 入口。
- `backend/src/modules/public-api-logs/` 的 Node capture、queue 和 IPC 写入入口。
- `backend/src/storage/external-integration-source-auth.repository.ts` 中公开接口 runtime 鉴权路径。
- `backend/src/storage/public-api-logs.repository.ts` 中 Node 公开接口日志写入路径。
- package scripts 中 W1b 专用 Node 回归命令，或替换为 Go 对应命令。

public group CRUD、public route strategy CRUD、public API Key CRUD 与 public account CRUD 只是 W1b 内部四条纵切面，不能把状态推进到 `待删除 Node`，也不能单独删除 Node 某个公开资源 handler；Node 侧公开鉴权、catalog、日志和 16 个 CRUD 入口当前仍是一个共享运行 owner。减法删除条件必须同时满足：Go 生产 router 正式接管 `/__aipublic__/*`、4 类资源 16 个 CRUD 全部通过契约测试、PG+Redis 单模式 smoke 通过、反向代理切流有回滚记录、`rg` 删除验证只剩历史文档或未迁移管理接口引用。

管理端 `/__aisys__/api/external-integration-sources/*` 来源授权管理接口不是 W1b 公开前缀接管范围；如果后续单独迁移，应进入后台管理接口波次。

## Node 对照命令

这些命令只作为 Node 契约冻结证据，不能作为 Go 已接管证据。2026-07-07 已执行并通过：

```powershell
Set-Location F:\sub2api-lite

pnpm test:external-source-auth
pnpm test:public-api-logs
pnpm --filter juhe-ai-backend test:external-integration-source-expires-at
pnpm --filter juhe-ai-backend test:external-integration-source-async-boundary
pnpm --filter juhe-ai-backend test:external-public-account-push-async-boundary
pnpm --filter juhe-ai-backend test:public-api-log-db-service-ipc
pnpm test:sqlite-query-regression
```

本轮已通过的实际结果：

- `pnpm test:external-source-auth`：通过，确认公开前缀、Bearer token、测试 token mock、权限校验、停用来源、限频、后台登录边界、旧公开统计路径 404，以及 API Key / 路由策略 / 分组 / 账号增删改查符合当前契约。
- `pnpm test:public-api-logs`：通过，确认公开请求记录、管理员查询、原文日志和 30 天保留清理符合预期。
- `pnpm --filter juhe-ai-backend test:external-integration-source-expires-at`：通过，确认 source/token `expiresAt` 不再接受宽松日期或空字符串清空。
- `pnpm --filter juhe-ai-backend test:external-integration-source-async-boundary`：通过，确认管理 CRUD、token 操作、鉴权和操作日志固定 async 路径。
- `pnpm --filter juhe-ai-backend test:external-public-account-push-async-boundary`：通过，确认公开账号、分组、路由策略、API Key 与操作日志固定 async/PG 路径。
- `pnpm --filter juhe-ai-backend test:public-api-log-db-service-ipc`：通过，确认 DB service 可把公开接口日志转发给 server，再进入 ingest-worker 队列。
- `pnpm test:sqlite-query-regression`：通过，确认 SQLite read worker、System/Public API DB access、system API 读突发、DB service 读写调度模拟、外部来源鉴权和公开账号 async 边界组合仍符合当前 Node 契约。

PostgreSQL / Redis smoke 必须只在测试库执行，不能指向生产库：

```powershell
$env:JUHE_AI_RUNTIME_MODE = 'performance'
$env:JUHE_AI_DATABASE_DRIVER = 'postgres'
$env:JUHE_AI_CACHE_DRIVER = 'redis'
$env:JUHE_AI_RUNTIME_STATE_DRIVER = 'redis'
$env:JUHE_AI_QUEUE_DRIVER = 'redis_stream'
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'

pnpm --filter juhe-ai-backend test:external-integration-source-postgres-smoke
pnpm --filter juhe-ai-backend test:external-public-account-push-postgres-smoke
```

`sqlite-query-regression`、`public-api-log-db-service-ipc` 和 DB service 相关命令验证的是待删除的 Node SQLite/read-worker/IPC 边界，不代表 Go 目标架构。

## Go 验收项

| 测试类型 | 验证方式 | 预期 |
| --- | --- | --- |
| 路由 catalog | Go 单元 / API 契约测试 | 16 个 method/path/scope 完整，旧 5 个路径 404，管理端 `api-docs` 不误归入公开接口 |
| 鉴权 | Go API 契约测试 | Bearer-only、token hash、source/token 状态与过期、scope 交集、错误 code/message/status 一致 |
| 限频 | Redis state 集成测试 | 多实例安全，429、`Retry-After`、details、惩罚窗口和内置测试 token `60s/10` 一致 |
| last_used | Store 测试 | 鉴权成功 touch source/token，60 秒节流；429 请求仍可能 touch |
| 公开日志 | API + worker/queue 测试 | 成功、失败、400、401、403、404、413、429、499、500 都记录；499 时顶层状态和 response snapshot 都是 `499`，快照和字段一致 |
| 分组纵切面 | Handler + service + PG store + shell API 契约测试 | `group/list|add|update|del` 严格字段、GET coerce、POST 不 coerce、目标用户自动创建、provider 校验、同名幂等、并发幂等重试、默认分组保护、账号绑定保护、活跃策略路由锁和并发删除保护、响应白名单和日志副作用一致 |
| 路由策略纵切面 | Handler + service + PG store + shell API 契约测试 | `route-strategy/list|add|update|del` 严格字段、GET coerce、POST 不 coerce、目标用户必须已存在、同名冲突、绑定整体替换、普通 / 故障回退模式规则、默认策略和 API Key 使用保护、绑定约束、响应白名单和日志副作用一致 |
| API Key 纵切面 | Handler + service + PG store + shell API 契约测试 | `api-key/list|add|update|del` 严格字段、GET coerce、POST 不 coerce、目标用户必须已存在、路由策略 owner / active 校验、同名冲突、默认 Key 删除 / 换路由保护、secret 只在新增业务响应返回、完整 key 按原值进入有界响应快照、列表 / 更新 / 删除只返回摘要、响应白名单和日志副作用一致 |
| AI 账户纵切面 | Handler + service + PG store + shell API 契约测试 | `account/list|add|update|del` 严格字段、GET coerce、POST 数字不 coerce、目标用户 / 分组自动创建、provider/profile 校验、同名冲突、凭据加密保存、响应凭据隐藏、`pending_test -> active` 拒绝、软删除和 404 / `not_found` 语义一致；`account/add` 必须区分 `supportedModels` 省略 / 显式空 / 非空，省略继承 provider 默认模型，最终为空返回固定 `400`，重复名称仍优先，fresh seed 的 GPT-5.6 默认模型有静态门禁 |
| 健康任务 bridge | Node/Go 专项 + 跨运行时 integration | loopback、`1024 bytes`、HMAC-SHA256、仅新增 `activation`、仅 `202` 成功和提交后脱离请求的异步 best-effort；公开账户 update 只写后台检查调度，不经过 bridge；`TestAccountHealthCheckDispatchNodeE2E` 仅验证 Go client -> Node 生产 router |
| 捕获与响应边界 | API 契约测试 | 业务响应不回显账号凭据，API Key 完整 secret 只在新增返回；公开日志 query/body/response 在预算内保留原值，`Authorization` / Cookie / 来源 token 不进入 headers 快照 |
| 存储 | PG/Redis 集成测试 | PostgreSQL schema / 索引 / 事务正确，Redis state 和 queue 隔离，不引入 SQLite |
| 路径归属 | API 契约测试 | `__aisys__`、`__aipublic__`、`/v1` 和前端兜底不互抢 |
| 删除验证 | `rg "__aipublic__|external-source|public-api-logs" backend/src backend/package.json package.json` | 已接管路径的 Node runtime 入口和专用脚本删除或替换，只剩未迁移管理接口或历史文档 |

当前 Go W1b 非容器基础验证已通过：

- `sqlc generate`
- `go mod tidy -diff`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `golangci-lint run`
- `govulncheck ./...`

`go test -tags=integration ./internal/testkit/integration -count=1` 是 W1b 必跑项，但当前主线程复核时 Docker / testcontainers 不可用，容器子测试输出 `SKIP`，不计为真实 PG/Redis 通过。后续必须在 Docker 健康环境复跑，并确认不是 skip 后，才能把 W1b PG migration smoke、真实 Redis/Asynq/PG 公开日志队列 smoke 和四条 shell E2E 测试记为通过。

这些非容器命令证明 Go W1b catalog/auth/store port、公开接口日志快照构造、Asynq payload/enqueue/handler、HTTP shell / capture 契约、499 客户端提前断开捕获、ResponseWriter 可选接口透传和默认关闭 / 显式开启的 router guard 可用；不证明 `/__aipublic__` 已正式切流、真实 PG/Redis 集成、完整资源级 API 契约测试、account shell E2E 已在真实 Docker/testcontainers 环境通过或 Node 删除。

当前已新增 `go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke`，并已通过缺配置 fail-fast、默认 guard 和输出 takeover 边界的单元测试。该命令需要真实 PostgreSQL、Redis cache、Redis state、Redis queue 和已启动的 `juhe-ai-worker ingest`；主线程尚未具备真实 PG/Redis/worker 环境，因此还没有把该命令记为真实依赖通过。

public group、public route strategy、public API Key 与 public account 作为当前四条真实资源纵切面，已补 handler、service、PostgreSQL store/query 和 integration/shell 测试代码；当前可确认 handler/service/HTTP 非容器测试和 integration 包编译通过。真实 PostgreSQL / Redis / Asynq integration 和 shell E2E 需要 Docker/testcontainers 健康环境复跑，不能把 `SKIP` 当作通过：

BUG-0035 的公开账户默认模型同步修复已通过 HTTP 三态、service 默认继承 / 最终非空、provider 默认 JSON 联表、GPT-5.6 seed 静态 guard、全量 Go、目标包 race、vet、tidy diff 和 integration 包编译。真实 PostgreSQL 写入和 Docker shell E2E 因本机 Docker 未运行输出 `SKIP`，仍待健康环境复跑。

BUG-0043 的账户默认测试模型一致性修复已同步 Node 提交 `4f32fe11e`：每次账户更新都按最终模型集合自愈无效 `default_test_model`，`SupportedModelsChanged` 继续只控制目录校验和模型绑定重写。已通过 `sqlc generate`、store / public account 目标测试、目标 race、全量 Go、vet/tidy 和 integration 编译。真实 `TestW1bPublicAccountsPostgresSmoke` 已覆盖备注更新自愈、无序等价自愈、包含和排除默认模型，但本机 Docker 未运行输出 `SKIP`，仍待健康环境复跑。

| 层级 | 建议命令 / 验证方式 | 必须覆盖 |
| --- | --- | --- |
| Handler | `go test ./internal/httpapi -run TestPublicGroupHandlers` | shell 注入 handler 后的 `group/list|add|update|del`、strict query/body、GET 分页 coerce、POST 数字 / 布尔不 coerce、中文错误、测试 token mock 和统一响应；当前已通过 |
| Service | `go test ./internal/modules/publicgroups -run Test` | 目标用户读取和 `group/add` 自动创建、目标用户停用、provider 缺失 / 停用、同用户同供应商同名 `existing`、并发 target / group 唯一冲突后重试、重复名 `409`、默认分组修改 / 删除保护、修改 provider 前账号绑定保护、停用 / 删除前活跃策略路由唯一可用分组保护；当前已通过 |
| PostgreSQL store / integration | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicGroupsPostgresSmoke -count=1` | `000004_w1b_public_groups.sql`、sqlc query、事务、唯一索引、owner 复合 FK、账号绑定计数、活跃策略路由锁、同一策略下两个分组并发删除只能成功一个；需在 Docker/testcontainers 健康环境真实通过，且确认无 SQLite driver、SQLite env、DB service 或 IPC 引用 |
| Shell E2E | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicGroupsShellE2E -count=1` | 需在 Docker/testcontainers 健康环境真实通过；覆盖目标为 Bearer/auth/scope、Redis limiter、public API log 入队 / worker 写 PG、原值快照边界和业务响应白名单一起穿过 HTTP shell；仍不代表生产 router 已挂载 |
| 路由归属 | `go test ./internal/httpapi -run TestPublicAPITakeoverGuard` | 生产 router 仍不注册 `/__aipublic__`；纵切面只在显式构造的 shell 中可用 |
| 删除验证 | `rg "__aipublic__|external-source|public-api-logs" backend/src backend/package.json package.json` | W1b 未整体切流前只记录待删范围，不删除 Node；整体接管后旧 Node runtime 入口和专用脚本必须清除 |

public route strategy 纵切面专项矩阵：

| 层级 | 建议命令 / 验证方式 | 必须覆盖 |
| --- | --- | --- |
| Handler | `go test ./internal/httpapi -run TestPublicRouteStrategyHandlers` | shell 注入 handler 后的 `route-strategy/list|add|update|del`、strict query/body、GET 分页 coerce、POST 数字不 coerce、`groupBindings` 嵌套字段白名单、中文错误、测试 token mock 和统一响应；当前已通过 |
| Service | `go test ./internal/modules/publicroutestrategies -run Test` | 目标用户必须已存在且 active、同名冲突 `409`、`update/delete` 归属校验、绑定整体覆盖、普通 / 故障回退模式规则、重复分组、active priority 冲突、停用分组 active 绑定拒绝、默认策略删除保护和 API Key 使用保护；当前已通过 |
| PostgreSQL store / integration | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicRouteStrategiesPostgresSmoke -count=1` | `000004_w1b_public_groups.sql` 中 route strategy / binding / api key 约束、`w1b_public_route_strategies.sql`、事务、同名唯一索引、绑定整体替换、active priority 唯一、默认策略和 API Key 使用保护；需在 Docker/testcontainers 健康环境真实通过，且确认无 SQLite driver、SQLite env、DB service 或 IPC 引用 |
| Shell E2E | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicRouteStrategiesShellE2E -count=1` | 需在 Docker/testcontainers 健康环境真实通过；覆盖目标为 Bearer/auth/scope、Redis limiter、public API log 入队 / worker 写 PG、add/list/update/delete/limited、原值快照边界和业务响应白名单一起穿过 HTTP shell；仍不代表生产 router 已挂载 |

public API Key 纵切面专项矩阵：

| 层级 | 建议命令 / 验证方式 | 必须覆盖 |
| --- | --- | --- |
| Handler | `go test ./internal/httpapi -run TestPublicAPIKeyHandlers` | shell 注入 handler 后的 `api-key/list|add|update|del`、strict query/body、GET 分页 coerce、POST 数字 / 布尔不 coerce、中文错误、测试 token mock、`add` 返回完整 `key` 且响应快照在预算内保留该原值 |
| Service | `go test ./internal/modules/publicapikeys -run Test` | 目标用户必须已存在且 active、路由策略同 owner 且 active、同名冲突 `409`、`update/delete` 归属校验、至少一个变更字段、默认 API Key 删除 / 换路由保护、quota / schedule / expiresAt 校验、hash / prefix / suffix 边界和响应白名单；当前已通过 |
| PostgreSQL store / integration | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicAPIKeysPostgresSmoke -count=1` | `000004_w1b_public_groups.sql` 中 API Key 字段 / 索引 / JSON 约束、`w1b_public_api_keys.sql`、事务、唯一索引、route strategy owner 校验、默认 Key 删除保护、secret 不明文落库且只保存 hash / prefix / suffix、分页和摘要字段；需在 Docker/testcontainers 健康环境真实通过，且确认无 SQLite driver、SQLite env、DB service 或 IPC 引用 |
| Shell E2E | `go test -tags=integration ./internal/testkit/integration -run TestW1bPublicAPIKeysShellE2E -count=1` | 需在 Docker/testcontainers 健康环境真实通过；覆盖目标为 Bearer/auth/scope、Redis limiter、public API log 入队 / worker 写 PG、add/list/update/delete/limited、完整 key 在新增业务响应与有界日志响应快照中保留原值；`Authorization` / Cookie / 来源 token 不进入 headers 快照，list/update/delete 业务响应仍只返回摘要；仍不代表生产 router 已挂载 |

public account 纵切面专项矩阵：

| 层级 | 建议命令 / 验证方式 | 必须覆盖 |
| --- | --- | --- |
| Handler | `go test ./internal/httpapi -run TestPublicAccount -count=1` | shell 注入 handler 后的 `account/list|add|update|del`、strict query/body、GET 分页 coerce、POST 数字不 coerce、可选字段 `null` / 空 notes / nullable schedule、中文错误、测试 token mock、业务响应不返回 `apiKey` / `baseUrl` / `credentials`，请求日志在预算内保留上游 `apiKey` 和带 userinfo URL 原值；`supportedModels` 省略、显式空数组和非空数组的 presence 必须保持到 service |
| Service | `go test ./internal/modules/publicaccounts -run TestService -count=1` | 目标用户 / 目标分组自动创建、provider/profile 启用校验、`type=api_key` 校验但不可变、上游 base URL 安全、endpoint mode、凭据可逆加密、多 Key 池与 fingerprint/mask 一致、同名冲突、`pending_test -> active` 拒绝、局部凭据更新、priority presence、add 异步 activation / update 仅调度、软删除和 `not_found` 幂等；省略模型继承 provider 默认值，显式空数组和空默认值报固定错误，重复名称优先 |
| PostgreSQL store / seed guard | `go test ./internal/store/postgres -run TestPublicAccount -count=1` | provider profile SQL 联表读取 `providers.default_supported_models_json`，默认模型 JSON 正确解码，非法 JSON fail-fast，`gpt` / `openai` fresh seed 保留 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` |
| PostgreSQL integration | `go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsPostgresSmoke -count=1` | `000005_w1b_public_accounts.sql`、`w1b_public_accounts.sql`、事务、provider profile seed、owner 复合 FK、默认模型继承、显式空数组拒绝、模型替换、凭据不明文落库、软删除和绑定清理；必须在 Docker/testcontainers 健康环境真实通过，且确认不引入 SQLite / DB service / IPC |
| Shell E2E | `go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsShellE2E -count=1` | 测试代码已补；需在 Docker/testcontainers 健康环境真实通过；目标为 Bearer/auth/scope、Redis limiter、public API log 入队 / worker 写 PG、add/list/update/delete/limited、默认模型继承、显式空数组 `400`、重复名称优先、source/token last_used、凭据请求日志原值、headers 排除 `Authorization` / Cookie / 来源 token 和账户业务响应白名单一起穿过 HTTP shell；仍不代表生产 router 已挂载 |

## 灰度与回滚

- 灰度方式：测试环境按 `/__aipublic__/*` 前缀切到 Go；后台管理 API、网关和前端静态路径不随 W1b 切流。
- 失败阈值：鉴权错误结构、公开日志写入、public group 幂等 / 默认分组保护 / 路由策略保护、public route strategy 绑定规则 / 默认策略保护 / API Key 使用保护、API Key secret 返回边界、账号凭据隐藏、`account/del not_found`、body parser 状态码或路径归属任一不一致，都不能进入生产接管。
- 回滚方式：恢复上一版反向代理 / 发布包的 `/__aipublic__` owner；不做 Node/Go 双写，不把临时兼容分支写入 runtime。

## 文档同步

- 变更 16 个公开接口、字段、错误、scope 或日志快照边界时，先更新 [公开资源维护接口设计](../functions/公开资源维护接口设计.md)、[外部来源系统鉴权设计](../functions/外部来源系统鉴权设计.md)、[公开接口日志设计](../functions/公开接口日志设计.md) 和 [接口契约与权限矩阵](../functions/接口契约与权限矩阵.md)。
- 变更存储、限频或队列时，同步 [Go 后端架构基线](Go后端架构基线.md)、[Go 技术选型与依赖基线](Go技术选型与依赖基线.md) 和 [存储目标与 SQLite 移除](存储目标与SQLite移除.md)。
- W1b 进入 Go 实现、待删除 Node 或已接管时，同步 [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 和 PLAN-0081。

## 验收结论

- 代码已实现：部分，当前已补 Go publicapi 契约骨架、PostgreSQL auth/log adapter、Redis penalty-window helper、公开接口日志快照 / 日志 DTO 构造、Asynq payload/enqueue/handler、W1b public API log worker runtime、HTTP shell / capture 契约、499 客户端提前断开捕获、ResponseWriter 可选接口透传、W1b PG / Asynq smoke 用例、public group CRUD、public route strategy CRUD、public API Key CRUD 与 public account CRUD 四条真实资源纵切面、`JUHE_AI_PUBLIC_API_ENABLED` 默认关闭的生产 router opt-in guard，以及 `w1b-public-api-smoke` 独立 maintenance smoke。
- 验证已通过：Node 非 PG 对照命令已通过；本轮 Go `sqlc generate`、`go mod tidy -diff`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/...`、`go test -tags=integration ./internal/testkit/integration -run '^$'`、public account handler / service / public API log 非容器专项，以及 `go test ./internal/maintenance ./internal/app` 已通过。此前 public group / public route strategy / public API Key 的 handler / service 非容器专项已通过。`TestW1bPublicAccountsShellE2E` 测试代码已补，当前 `go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsShellE2E -count=1` 因 Docker 不可用输出 `SKIP`，不计为真实通过；`w1b-public-api-smoke` 真实依赖执行、真实 PostgreSQL integration 专项和完整 Redis limiter + public API log worker shell E2E 需 Docker 或真实 PG/Redis/worker 环境复跑；PG/Redis Node 对照 smoke、生产路由切流和 Node 删除待本阶段后续执行。
- 2026-07-14 原值漂移修复回归：`pnpm test:public-api-logs`、Go public log targeted、`go test ./... -count=1`、public log targeted `-race` 和 integration package compile 均通过；快照 map key 已排序，保证 200 条目 / 32KB 预算下的选择稳定，JSON number 也保持数值类型、不再字符串化。该验证不覆盖 Express qs extended parser 与 Go `r.URL.Query()` 的解析后 query 等价性。`JUHE_AI_REQUIRE_INTEGRATION=1 go test -v -tags=integration ./internal/testkit/integration -run '^TestW6ManagementPublicAPILogsGoWriterReaderPostgresSmoke$' -count=1` 因 Windows 无可用 Docker provider 退出 `1`，只证明 strict gate 可阻止 `SKIP` 误过，不记录为真实 PostgreSQL、schema 类型或 EXPLAIN 通过。
- BUG-0035 验证状态：默认模型三态、默认继承、最终非空、重复名称优先、provider 默认 JSON 联表和 GPT-5.6 seed guard 目标测试已通过；真实 PostgreSQL / Docker/testcontainers 目标测试因 Docker 未运行输出 `SKIP`，不计真实通过。
- 生产已接管：否。
- Node 已删除：否。
- 删除证据：待填写。
- bridge 验证：`TestAccountHealthCheckDispatchNodeE2E` 已在 Windows 本机以强制依赖模式执行，普通目标测试和 `-race` 均通过；该结果只证明真实 Go client -> Node 生产 router，不证明 public HTTP 写入、worker 入队、最终健康状态或可靠交付。
- 剩余风险：四类资源纵切面已覆盖真实资源写入、服务层边界、PG 事务 / 索引、owner 复合 FK、并发幂等、路由策略并发删除保护、绑定整体替换、API Key hash-only secret、AI 账户可逆加密凭据、软删除和响应白名单；但生产 router 只是 opt-in guard，默认关闭，`w1b-public-api-smoke` 也只是本地 httptest + 真实依赖 smoke，不验证真实监听端口或反向代理切流。account shell E2E 测试代码已补但真实 Docker/testcontainers 环境仍需复跑，默认模型继承和显式空数组拒绝也必须在真实 PostgreSQL / shell E2E 中确认，Docker/testcontainers 环境真实 PG/Redis/Asynq 仍需复跑，反向代理切流未执行。公开日志有意在捕获预算内保留业务 secret，因此管理员权限、保留期、容量预算和导出前人工移除是硬边界；Node PostgreSQL 旧结构与 fresh Goose 不兼容，离线同步和单写 owner 未完成前不能切流；Go 扁平 query map 与 Node qs extended parser 的 bracket / 畸形转义解析边界仍是独立未决兼容任务，完成前不能宣告完整 parity。Node 当前 SQLite/DB service/IPC 仍只是对照事实，不属于 Go 目标架构。
