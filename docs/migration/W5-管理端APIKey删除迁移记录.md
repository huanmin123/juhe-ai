# W5 管理端 API Key 删除迁移记录

> 本文记录管理端和个人端 API Key 删除的 Go opt-in 实现、HTTP 契约、事务与写后副作用边界。C1、C2a 和 C2b 代码已完成，但真实依赖 smoke 因 Docker provider 不可用而跳过；本文不代表生产接管、持久化失效重试问题已解决或 Node 路由可以删除。

## 基本信息

- 模块：管理端 / 个人端 API Key 删除
- 状态：Go opt-in 代码已完成，待真实依赖、前端联调、生产单 owner 切流与回滚证据
- 迁移波次：W5
- 当前 Node owner：`backend/src/modules/api-keys/api-keys.routes.ts`、`backend/src/storage/api-key.repository.ts`
- 目标 Go owner：`backend-go/internal/modules/managementapikeys/`、`backend-go/internal/httpapi/management_api_key_delete.go`、`backend-go/internal/store/postgres/managementapikeydelete.go`、`backend-go/internal/store/postgres/queries/w5_management_api_key_delete.sql`、`backend-go/internal/store/postgres/queries/w1b_public_api_keys.sql`、`backend-go/db/migrations/000037_w5_api_key_record_cleanup_targets.sql`、`backend-go/internal/httpapi/router.go`、`backend-go/internal/app/server.go`
- 关联 integration：`backend-go/internal/testkit/integration/w5_management_api_key_delete_smoke_test.go`、`backend-go/internal/testkit/integration/w5_management_api_key_list_smoke_test.go`
- 关联计划：`../plans/计划-0081-Node转Go渐进减法迁移.md`
- 相邻记录：[W5 管理端 API Key 密钥生命周期迁移记录](W5-管理端APIKey密钥生命周期迁移记录.md)

## 当前切片与提交

当前切片只包含：

- `DELETE /__aisys__/api/api-keys/{id}`
- `DELETE /__aisys__/api/my-api-keys/{id}`

两条路径只在 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 的完整管理 API opt-in 下注册；默认生产配置不接管。实现提交固定为：

| 阶段 | 提交 | 内容 |
| --- | --- | --- |
| 前置 | `adbf148ca` | 新增 API Key 关联记录 cleanup-target schema |
| C1 | `7bdbc08c7` | 新增管理端 API Key 原子删除 service / store |
| C1 | `c51c08e7a` | 管理端与公开 API Key 删除复用 cleanup-target upsert |
| C2a | `010f8f104` | 接入管理 / 个人 DELETE HTTP、router、app、失效与操作日志 |
| C2b | `caf9edbe2` | 补管理端 API Key 删除 PostgreSQL / Redis / Asynq smoke 覆盖 |

## 权限、middleware 与 HTTP 契约

- 管理路由使用管理 API 写链路：先进入鉴权前 IP write limiter，再执行写鉴权并 touch session、已认证用户 write limiter，随后校验 `admin` / `super_admin`；普通用户返回 `403 { "message": "需要管理员权限" }`。限流阈值按当前系统设置读取，fresh schema 默认值为 IP write `180/minute`、`40/10 seconds` burst 和已认证用户 write `120/minute`。
- 个人路由使用同一 IP write limiter、写鉴权、session touch 和已认证用户 write limiter，但不叠加管理员角色校验。
- DELETE 路由不注册 JSON body parser，也不注册 mutation guard；请求体即使 malformed 也不会参与业务解析或改变删除语义。
- 管理路由省略 `systemAccountId` 或传 `systemAccountId=all` 时按全局精确 ID 查找；传一个非空 `systemAccountId` 时收窄到该 owner。空值返回 `400 { "message": "系统账号 ID 不能为空" }`，重复参数返回 `400 { "message": "Expected string, received array" }`。
- 个人路由始终强制当前 session 系统账户；伪造或重复 `systemAccountId` 均被忽略，不能扩大作用域。
- 目标不存在或 owner 不匹配返回 `404 { "message": "API Key 不存在" }`；默认 API Key 返回 `409 { "message": "默认 API Key 不允许删除" }`。
- 成功返回 `204 No Content`，响应 body 必须为空，并设置 `Cache-Control: no-store` 与 `Pragma: no-cache`。
- 未知内部错误返回通用 `500 { "message": "服务器内部错误" }`，不暴露 PostgreSQL、Redis、队列或内部错误细节。

## PostgreSQL 事务与 cleanup target

删除在一个 PostgreSQL 事务内完成：

1. 按 API Key ID 和可选 owner 执行 `SELECT ... FOR UPDATE OF api_keys`。
2. 在持锁状态检查 `is_default`；默认 Key 在任何删除前返回冲突。
3. 按实际 owner 硬删除 `juhe_business.api_keys`，并以 `RETURNING id` 确认目标被删除。
4. 在同一事务调用与公开 API Key 删除共用的 `UpsertAPIKeyRecordCleanupTarget`。upsert 失败时整个事务回滚，API Key 保留。

cleanup target 使用 `ON CONFLICT (api_key_id) DO UPDATE`，冲突时只刷新 `system_account_id` 和 `updated_at`；`created_at` 以及实际重试字段 `attempt_count`、`last_attempt_at`、`last_blocked_reason`、`last_error_message` 必须保留。当前表没有 `retry_after` 列。

HTTP 请求路径不扫描、聚合或同步清理 usage、audit、runtime log、operation log 或其他记录明细，也不对这些表执行 `SUM`、`GROUP BY` 或内存 `reduce`。事务只登记 cleanup target，后续由 cleanup worker 按目标继续处理关联记录。

## 提交后失效与残余风险

- 数据库提交后使用脱离请求取消、最长 5 秒的 context 执行失效。
- validation cache 失效是必需步骤；失败时停止后续失效并返回通用 `500`，但数据库删除已经提交。
- lookup、gateway runtime 和 quota 失效只在 validation 成功后继续执行，均为 best-effort；失败不覆盖已经成功的 `204`。
- 失效 reason 固定为 `api_key_deleted`。

当前实现没有为 validation cache 失效提供持久化重试。删除已提交但 validation 失效失败时，旧 validation 结果可能在其他失效、缓存到期或人工恢复前继续被接受，这是明确的安全与恢复风险。cleanup target 只服务关联记录清理，不是 validation 失效重试队列。生产接管前必须提供真实环境证据、告警、排障与恢复路径；不得把该风险写成已经解决。

## 操作日志契约

- 只有数据库事务已经提交时才登记删除操作日志；未找到、owner 不匹配、默认 Key 冲突或事务回滚不写日志。
- 操作日志固定为 module=`api_keys`、action=`delete`、resource type=`api_key`，记录目标 owner viewer 和 `deleted: false -> true`，不得包含完整 Key、hash、ciphertext 或其他敏感值。
- 正常成功记录 `status_code=204`。
- validation cache 必需失效失败时，HTTP 返回通用 `500`，但仍登记已经提交的 `api_keys.delete`，并记录最终 `status_code=500`。
- operation log enqueue 是 best-effort；队列失败不把已完成的 `204` 改成失败。关联记录 cleanup 由 cleanup worker 后续处理，请求路径不扫描 operation log 或 audit 明细。

## 验证记录

| 验证 | 结果 |
| --- | --- |
| `go test ./internal/modules/managementapikeys ./internal/httpapi ./internal/store/postgres ./internal/app -count=1` | 通过 |
| `go test ./... -count=1` | 通过 |
| `go test -tags=integration ./internal/testkit/integration -run '^$' -count=1` | 通过；只证明 integration 包可编译 |
| `go test -v -tags=integration ./internal/testkit/integration -run '^TestW5ManagementAPIKeyUpdatePostgresRedisSmoke$' -count=1` | 已实际发起，但 Docker provider 不可用，按 testcontainers 健康门禁 `SKIP` |

deterministic unit / full Go validation 已通过，integration package compile 已通过。C2b smoke 代码覆盖管理 / 个人 DELETE、作用域、默认 Key 保护、硬删除、fresh / conflict cleanup target、upsert 回滚、提交后 validation 失败、Redis 失效与 operation log 断言，但目标用例本次没有实际进入真实依赖断言。

因此不得描述真实 PostgreSQL、Redis、Asynq、HTTP 或 operation-log ingest 已通过。

## Node 语义漂移审计

审计基线覆盖到 `origin/master` 的 `0e6ecaa69`。`861c5751e` 引入的账户余额自动识别和 GPT catalog 更新、`ce3477135` 引入的余额扫描上游 agent 收口、`d51b3c3ea` 引入的 Redis client 收口均未改变已迁移 API Key 删除的 HTTP、事务、失效或审计语义；相关 cleanup-target 行为已经对齐。合并远程新增的 provider catalog migration 后，cleanup-target migration 从冲突的 `000036` 顺延为 `000037`。该结论不改变 Node 当前 owner，也不是 Node 删除证据。

## 剩余门禁

满足以下条件前不得声明 API Key 删除生产接管、W5 完成或删除 Node 路由：

- 在 Docker/testcontainers 健康环境真实运行目标 integration，取得 PostgreSQL、Redis、Asynq、HTTP 和 operation-log ingest 非 `SKIP` 证据。
- 前端 API Key 页面连接真实 Go 后端完成管理 / 个人删除 smoke，并覆盖 204 空 body、默认 Key 冲突、owner 隔离和列表刷新。
- 对 validation cache 必需失效失败补生产告警、残留接受窗口评估和可执行恢复证据；如需持久化重试，应作为后续独立实现和验收项。
- 完成生产单 owner cutover、停止 Go 后回退 Node 的 rollback 演练与证据。
- 删除 Node API Key DELETE route/service/store/test，并提供静态扫描和运行证据。

## 当前结论

管理端和个人端 API Key 删除代码已经完成 opt-in 接入，C1/C2a/C2b 契约与非容器验证已固定。

真实依赖执行、前端真实 Go smoke、生产单 owner 接管、回滚证据、持久化 validation 失效重试和 Node 路由删除均未完成；PLAN-0081 与 W5 必须继续保持进行中。
