# W5 管理端 API Key 密钥生命周期迁移记录

> 本文记录管理端和个人端 API Key 创建、完整密钥查看、密钥刷新的 Go opt-in 迁移实现与验收边界。当前 store、service、HTTP、router、app 接线和真实依赖 smoke 代码已完成；本机 Docker 不可用，因此 PostgreSQL / Redis / Asynq smoke 尚未真实执行通过。

## 基本信息

- 模块：管理端 / 个人端 API Key 创建与密钥生命周期
- 状态：Go opt-in 代码已完成，待真实依赖、前端联调与生产切流
- 迁移波次：W5
- 当前 Node owner：`backend/src/modules/api-keys/api-keys.routes.ts`、`backend/src/storage/api-key.repository.ts`
- 目标 Go owner：`backend-go/internal/modules/managementapikeys/`、`backend-go/internal/httpapi/management_api_key_create.go`、`backend-go/internal/httpapi/management_api_key_secret.go`、`backend-go/internal/store/postgres/managementapikeycreate.go`、`backend-go/internal/store/postgres/managementapikeysecret.go`、`backend-go/internal/store/postgres/queries/w5_management_api_key_create.sql`、`backend-go/internal/store/postgres/queries/w5_management_api_key_secret.sql`、`backend-go/internal/httpapi/router.go`、`backend-go/internal/app/server.go`
- 关联 integration：`backend-go/internal/testkit/integration/w5_management_api_key_list_smoke_test.go`、`backend-go/internal/testkit/integration/w5_management_api_key_create_smoke_test.go`、`backend-go/internal/testkit/integration/w5_management_api_key_secret_smoke_test.go`
- 关联计划：`../plans/计划-0081-Node转Go渐进减法迁移.md`
- 相邻记录：[W5 管理端 API Key 列表迁移状态](模块迁移顺序与减法清单.md)

## 当前切片

本切片只包含：

- `POST /__aisys__/api/api-keys`
- `POST /__aisys__/api/my-api-keys`
- `GET /__aisys__/api/api-keys/{id}/secret`
- `GET /__aisys__/api/my-api-keys/{id}/secret`
- `POST /__aisys__/api/api-keys/{id}/refresh-key`
- `POST /__aisys__/api/my-api-keys/{id}/refresh-key`

六条路径只在 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 且完整注入管理鉴权、PostgreSQL store、稳定 `JUHE_AI_SECRET`、Redis cache/state invalidator 和 operation log queue 时注册。

本切片不包含 API Key 普通更新、删除、路由策略编辑、前端生产入口切换、生产单 owner 切流或 Node 路由删除。API Key 列表属于相邻只读切片，W5 API Key CRUD 整体仍未接管。

## 权限与 HTTP 边界

- `/api-keys/{id}/*` 只允许 `admin` / `super_admin`；可省略 `systemAccountId` 做全局精确 ID 查询，也可传一个非空账户 ID 收窄 owner。重复参数或空值返回 `400`。
- `/my-api-keys/{id}/*` 强制当前 session 账户作用域，query 中伪造 `systemAccountId` 不扩大权限。
- 创建管理路由缺省或 `systemAccountId=all` 时以 actor 为 owner，明确单一非空 ID 时以该 ID 为 owner；个人创建路由始终以 actor 为 owner并隐藏 owner 字段。
- 创建 body 是 strict object，只允许 `name`、`description`、`routeStrategyId`、`status`、`expiresAt`、`quotaLimits` 和 `availabilitySchedule`；quota 使用 `json.Decoder.UseNumber` 保留精度。
- 创建 parser 固定 `256 KiB`，位于 system API IP limiter 后、写鉴权 / session touch 和 authenticated user write limiter 前；malformed/scalar、空 body、非 JSON body、array、unsupported charset 和超限语义与分组创建 transport 对齐。
- 创建 admin middleware 顺序固定为 body parser、写鉴权与限流、admin role、`api_keys.create` mutation guard；guard 指纹只使用 effective owner 与 trim 后名称。
- 不存在、owner 不匹配或普通用户跨账户访问统一返回 `404`，避免泄露资源存在性。
- secret GET 使用只读鉴权，不 touch session；refresh POST 使用写鉴权 touch。
- refresh JSON parser 位于 system API IP limiter 后、鉴权 / touch / authenticated user limiter 前，固定 `256 KiB` 上限。
- refresh 对空 body、JSON object 和 JSON array 兼容 Node / Express 当前 transport；JSON primitive 或 malformed JSON 返回 `400`；非 JSON Content-Type 不解析业务 body。
- secret 和 refresh 成功响应均设置 `Cache-Control: no-store` 与 `Pragma: no-cache`。

## 密钥存储与返回

- create 在 PostgreSQL 单条 CTE 中锁定 active route strategy，写 API Key 与可选小时额度窗口；不要求路由当前已有分组或账户，也不拒绝 disabled target system account。
- create 成功返回 `201`，消息为 `API Key 已创建，请立即复制完整密钥`；完整 `key` 只在本次响应出现，管理员 DTO 带 owner，个人 DTO 隐藏 owner。
- 完整 API Key 只保存在 `key_secret_encrypted` 的 AES-GCM JSON 密文中，运行时使用稳定 `JUHE_AI_SECRET` 解密。
- secret GET 成功响应只返回 `{ data: { key } }`；操作日志和普通 DTO 不保存或回显完整密钥。
- 密文缺失、为空、无法解密或 payload 缺少字符串 `key` 时返回通用 `500`，不回退 hash、prefix 或 suffix 拼接。
- refresh 在 PostgreSQL 单事务中锁定目标行，生成新密钥，同时更新 `key_hash`、`key_prefix`、`key_suffix`、`key_secret_encrypted` 和 `updated_at`。
- refresh 成功响应返回更新后的 API Key DTO 和一次性完整 `key`；管理员响应包含 owner 字段，个人响应隐藏 owner 字段。
- operation log 的 `changes` 只记录 `prefix...suffix` marker，禁止写入完整旧密钥或新密钥。

## 写后副作用

- create 提交后 best-effort 发布 gateway runtime 与目标 API Key quota 失效，reason=`api_key_created`；不刷新 validation cache。
- create 成功写 `api_keys.create` operation log；changes 只含 name/status/routeStrategyId/availabilitySchedule 和 `prefix...suffix` marker，不含完整 key、hash、ciphertext、description、expiresAt 或 quotaLimits。
- 数据库提交后必须在独立的 5 秒有界 context 中刷新 `gateway:api-key-validation` shared cache version；客户端取消不能跳过该必需失效。
- validation cache 失效失败时请求返回通用 `500`，但数据库密钥已经提交；运维必须按“写入已成功、失效失败”处理，不能重复使用旧密钥。
- gateway runtime reason=`api_key_secret_refreshed` 和指定 API Key quota topic 失效为 best-effort，不覆盖业务成功。
- secret 查看成功写 `api_keys.reveal_secret` operation log；refresh 成功写 `api_keys.refresh_key` operation log。
- owner 不匹配、密文不可用和 refresh 失败不产生日志。
- operation log、测试失败输出和前端 smoke 摘要都不得打印完整 API Key 或环境资源标识。

## 实现提交

| 提交 | 内容 |
| --- | --- |
| `c56a9ad4c` | 抽取 API Key 与通用 JSON secret 加解密基础 |
| `18a3301c9` | 禁止零值 codec 使用全零密钥，并补 malformed ciphertext 门禁 |
| `0e610c9ca` | 新增 secret / refresh service、store、HTTP、router、app 和基础 integration |
| `362a0d704` | 对齐 refresh body、middleware、scope 参数和取消后的必需失效契约 |
| `e02f6747f` | 使用真实 Redis / Asynq operation log smoke，固定 session、缓存失效和密钥日志安全 |
| `a8ba6d018` | 接入管理 / 个人 API Key 创建 HTTP、strict body、typed error、router/app 与 operation log |
| `af8c9a974` | 扩展 W5 PostgreSQL / Redis / Asynq smoke，覆盖 API Key 创建真实依赖链路 |
| `c3498cf0d` | 在 mutation claim 前完成创建 scope / payload 校验，避免无效请求进入失败去重窗口 |
| `759f9e3d9` | 收口创建 smoke 与 unit test 失败诊断，禁止打印完整 key、hash、ciphertext 或解密 payload |

## 验证记录

| 验证 | 结果 |
| --- | --- |
| `go test ./internal/modules/managementapikeys ./internal/httpapi ./internal/store/postgres ./internal/app -count=1` | 通过 |
| `go test -race ./internal/modules/managementapikeys ./internal/httpapi ./internal/store/postgres ./internal/app -count=1` | 通过 |
| `go test ./... -count=1`、`go vet ./...`、`go mod tidy -diff` | 通过；tidy 无 diff |
| `go test -tags=integration ./internal/testkit/integration -run '^$' -count=1` | 通过 |
| `TestW5ManagementAPIKeyListPostgresSmoke` | 本机 Docker 不可用，按 testcontainers 健康门禁 `SKIP`，不计真实依赖通过 |
| Node `test:api-key-management-driver`、`test:api-key-route-validation`、`test:api-key-availability-schedule`、`test:api-key-single-read`、`test:scope`、`test:system-api-rate-limit` | 通过 |
| `pnpm --filter juhe-ai-frontend typecheck` | 通过；不代表真实 Go 后端联调 |

integration smoke 已编码覆盖真实 migrations、PostgreSQL、Redis cache/state、Asynq ingest worker、管理 session、admin / self create/reveal、管理员 refresh、disabled target、owner 隔离、active/disabled/missing/wrong-owner route、duplicate name/hash 约束、密文解密与缺失失败、小时窗口原子写入、schedule next-check、DB hash/prefix/suffix/ciphertext 更新、create runtime/quota 与 refresh validation/runtime/quota 失效、session touch、成功 operation log、失败 trace 零日志，以及所有测试失败诊断和持久化日志不泄露完整密钥。

## 最近 Node 变更对齐

`362a0d704..99584cbad` 的 Node 变更已审计：

- 健康检查首次激活优先队列属于 W7 ops worker，当前 Go W5 API Key 密钥切片不消费该队列。
- GPT 请求覆盖按最终模型能力解析属于 W10 网关和尚未整体迁移的账户配置，不改变当前 Go API Key secret / refresh 契约。
- 因此本轮不需要为这些 Node 提交修改已迁移 Go 代码；W7 和 W10 接管前必须重新纳入对应语义。

补充审计基线：`ddbb3d3d..759f9e3d9`，当前 Go 实现基线为 `759f9e3d9`（2026-07-11）。该范围内 API Key create 核心生产契约无净漂移：

- `acc61a4e1` 仅澄清测试断言：summary / update 响应不得包含完整 key；create 成功响应仍是完整 key 唯一一次可见的管理生命周期入口，不改变本切片一次性返回契约。
- `df1dc86ec` 的 gateway quota runtime state 发布、`8bb00ea0a` 的 authorization quota 失效时间语义，以及 `2ec3d776d` 的账户健康检查 / 人工诊断链路，分别属于尚未迁移的网关额度、授权额度和账户健康范围；本切片继续 defer，不提前复制到 API Key create HTTP / service。
- 因此本次只修正 create 预校验去重边界和测试诊断脱敏，不扩展 gateway quota、authorization quota、account health、OAuth、usage/audit 或 worker 迁移范围。
- 本机 `TestW5ManagementAPIKeyListPostgresSmoke` 仍因 Docker provider 不可用输出 `SKIP`；该结果只表示真实依赖测试未执行，不是 PostgreSQL / Redis / Asynq / HTTP 通过证据。

## 剩余门禁

满足以下条件前不得声明生产接管或删除 Node API Key 密钥接口：

- 在 Docker/testcontainers 健康环境真实通过 `TestW5ManagementAPIKeyListPostgresSmoke`，确认 PostgreSQL、Redis、Asynq 和 operation log 均实际执行而非 `SKIP`。
- 前端 API Key 页面连接真实 Go HTTP 服务完成列表、复制完整密钥、管理员 / 个人刷新、刷新后旧密钥失效、新密钥调用成功和页面不持久化完整密钥 smoke。
- 对 validation cache 失效失败后的“数据库已提交”场景补生产告警、排障和人工恢复演练。
- 完成 API Key update/delete 等剩余 W5 CRUD，并统一评估单 owner 切流。
- 生产反向代理按路径形成单 owner，并完成停止 Go 后回退 Node 的演练。
- 对应 Node route/service/store/test 已删除并提供 `rg` 静态证据。

## 当前结论

Go 已具备 W5 API Key 创建、完整密钥查看和刷新的一组独立 opt-in 实现，并完成非容器回归、integration 编译和真实依赖测试代码。真实 PostgreSQL / Redis / Asynq 执行、前端真实后端联调、API Key update/delete、生产切流和 Node 删除均未完成，本切片不能作为生产接管证据。
