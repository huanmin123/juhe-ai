# Go Backend

> 当前 W0 Go 工程与 PG/Redis/Asynq 常规基线已落地，W1a 公开设置读接口 Go 实现中，W1b `/__aipublic__` 已补公开维护接口基础设施和四类资源纵切面；W2 已补管理端辅助路径的系统账户列表读 / options、授权候选 options、供应商 options / 模型 catalog / 默认测试模型偏好、策略路由 options、分组 options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口；W3 已补验证码、登录、当前用户、当前用户资料、改密、登出、会话列表 / 撤销、系统账户创建和完整 mixed PATCH；W4 已补系统团队读 / 创建 / 更新 / 成员维护，以及授权列表 / 详情 / 创建 / 归还 Go opt-in 灰度能力。所有生产挂载仍默认关闭，未正式生产接管任何现有 Node 业务接口。
> 本轮另补 `PUT /__aisys__/api/providers/{code}/default-test-model` 的 Go opt-in 实现，只覆盖当前系统账户默认测试模型偏好写入，不覆盖自定义模型 CRUD 或供应商管理写接口。

本目录是 `juhe-ai` 后端从 Node.js 迁移到 Go 的新后端工程。迁移规则见 `../docs/migration/README.md`。

## 当前范围

- Go module、命令入口、配置读取、结构化日志、HTTP 路由和健康检查。
- PostgreSQL health、baseline migration、sqlc catalog 查询、事务封装和 store adapter 基线。
- Redis cache / state client 基线：namespace key、TTL set、pipeline、`IncrWithTTL` 原子计数、`GETDEL` 一次性消费、fixed-window 原子限流、W1b penalty-window 原子限流和 cache / state / queue Redis DB 去重校验。
- Asynq queue 基线：Redis URL 解析、`rediss` TLS、显式 Redis timeout、client ping、enqueue、inspector 和 pending smoke。
- 诊断端点基线：`/__aisys__/health`、`/__aisys__/api/health` 和受 `JUHE_AI_METRICS_ENABLED` 控制的 `/__aisys__/metrics`；pprof 只允许通过 `JUHE_AI_PPROF_ENABLED` 在受控 / loopback 场景启用。后续系统监控指标按 `../docs/migration/Go迁移指标与观测规划.md` 落地，不能沿用 Node event-loop / DB service / SQLite 文件指标。
- W1a 公开设置读接口：`GET /__aisys__/api/settings/public`，读取 `juhe_business.global_settings`，返回 `{ data: { appName, appIcon } }`，并按 `system_settings` 读取限流配置，按 `JUHE_AI_TRUST_PROXY` 识别客户端 IP，生产 server 路径使用 Redis state 原子 minute / burst IP read rate limit。
- W1b 外部维护公开接口基础设施：`internal/modules/publicapi` 固定 16 个 `/__aipublic__` method/path/scope、旧公开路径不进入 catalog、内置测试 token 常量；`internal/modules/publicapi/auth` 固定 Bearer 解析、token hash、source/token 状态与过期、scope 交集、auth error 和 `last_used_at` touch 判断；`internal/modules/publicapi/ratelimit` 映射 source/token 维度 penalty-window；`internal/modules/publicapilog` 提供公开接口日志 request / response snapshot、query string 脱敏、32KB 单侧预算、dropped / truncated / empty / complete 状态、499 和错误摘要构造；`internal/jobs/publicapilog` 提供 Asynq payload/enqueue/handler；`internal/jobs/worker` 和 `internal/app/ingest_worker.go` 提供 `juhe-ai-worker ingest` 日志消费 runtime；`internal/httpapi/public_api_shell.go` 提供 W1b HTTP shell / capture 契约测试组合，覆盖 499 客户端提前断开和底层 `ResponseWriter` 可选接口透传；`internal/config/config.go`、`internal/app/server.go` 和 `internal/httpapi/router.go` 提供 `JUHE_AI_PUBLIC_API_ENABLED=false` 默认关闭的生产 router opt-in guard；`cmd/juhe-ai-maintenance w1b-public-api-smoke` 提供本地 httptest 灰度 smoke，验证默认 guard、显式 opt-in mount、真实 PostgreSQL / Redis state / Redis queue、临时测试 token 和 worker 写入 public API log；`internal/store/port/publicapi.go` 固定不泄露 pgx/sqlc/Redis 类型的 auth/log store port；`internal/store/postgres/publicapi.go` 提供 PostgreSQL auth/log adapter。
- W1b public group / public route strategy / public API Key / public account 四条资源纵切面：已提供 handler、service、store port、PostgreSQL sqlc query、integration smoke 和 shell E2E 代码；public API Key 采用 hash-only 存储，完整 key 只在新增响应返回一次，日志快照和 `query_string` 均脱敏；public account 上游凭据使用 `JUHE_AI_SECRET` 派生的 AES-GCM 加密，响应不回显 `apiKey` / `baseUrl` / `credentials`。
- W2 管理端辅助接口当前已迁移路径：已覆盖 `system-accounts` 列表读、`system-accounts/options` 轻量下拉、authorization grantee accounts / teams / groups、providers options / models / default-test-model、route-strategies options、groups options / account-options、accounts options、accounts tags 只读 / 未绑定删除 / 独立 PATCH、operation-logs / my-operation-logs 列表与详情读接口。所有路径都复用管理端 session 鉴权和 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭的 router opt-in guard；只允许灰度验证，不代表生产接管。系统账户创建 / 完整更新已在 W3 Go opt-in 覆盖但未生产接管；完整会话管理生产接管、授权来源 / grant / 授权写接口、主账户写入 `tags`、OpenAI OAuth / 导入标签写路径、完整账号 summary 响应、operation log 保留清理和其他写接口 operation log 仍未迁移。详细边界见 `../docs/migration/W2-管理端只读辅助接口迁移记录.md`。
- W2 默认测试模型偏好写入：`PUT /__aisys__/api/providers/{code}/default-test-model` 已复用 provider model catalog 可见性校验和 `provider_default_test_models` upsert，只允许设置 active 文本生成模型，普通用户强制当前账户作用域，管理员可指定 `systemAccountId`；该接口仍受 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭门禁约束。
- W3 验证码、登录、当前用户读、资料更新、改密、当前会话登出、会话列表 / 撤销和系统账户写接口切片：`GET /__aisys__/api/auth/captcha` 使用 Redis state 保存 5 分钟 challenge，按客户端 IP 60 次 / 分钟 fixed-window 限流，返回 `{ captchaId, image, expiresAt }` PNG data URL，并提供 Go 内部 `VerifyChallenge` 的 Redis `GETDEL` 一次性消费能力；该 GET 路径会跳过 system API IP 读限流，避免和验证码自身限流叠加。`POST /__aisys__/api/auth/login` 已复用验证码一次性消费、Redis 登录失败 IP / 用户名锁定、active 系统账户密码校验、PG session token hash 写入、`last_login_at` / 初始 `last_seen_at` 和 `juhe_ai_session` Cookie 签发。`GET /__aisys__/api/auth/me`、`GET /__aisys__/api/auth/sessions` 和 `POST /__aisys__/api/auth/logout` 不 touch session；`DELETE /__aisys__/api/auth/sessions/{id}` 只撤销当前系统账户下指定 session，撤销当前 session 时清 Cookie。`PATCH /__aisys__/api/auth/me` 和 `POST /__aisys__/api/auth/change-password` 按 Node 写模式 touch 当前 session。`POST /__aisys__/api/system-accounts` 已补系统账户创建、默认分组 / 路由 / API Key fanout 和操作日志脱敏；`PATCH /__aisys__/api/system-accounts/{id}` 已补 full mixed partial update，支持资料、密码、角色、状态、初始改密标记和图像权限，禁止停用 / 降级最后一个启用 `super_admin`，提交密码或禁用状态会撤销目标全部 session，状态或图像权限真实变化会清理 gateway runtime cache / API Key validation cache。本切片不代表完整 Cookie 安全部署、完整会话管理生产接管、前端真实 Go 后端 smoke、生产单 owner 切流或 Node `/auth` / `/system-accounts` 删除已完成，详细边界见 `../docs/migration/W3-登录与系统账户迁移记录.md`。
- W4 团队与统一授权切片：`GET /__aisys__/api/system-teams`、`GET /__aisys__/api/system-teams/{id}`、`GET /__aisys__/api/my-teams`、`GET /__aisys__/api/my-teams/{id}`、`POST /__aisys__/api/system-teams`、`PATCH /__aisys__/api/system-teams/{id}`、`POST /__aisys__/api/system-teams/{id}/members`、`DELETE /__aisys__/api/system-teams/{id}/members/{memberId}`、`GET /__aisys__/api/authorizations`、`GET /__aisys__/api/authorizations/{id}`、`GET /__aisys__/api/my-authorizations`、`GET /__aisys__/api/my-authorizations/{id}`、`POST /__aisys__/api/authorizations`、`POST /__aisys__/api/my-authorizations`、`DELETE /__aisys__/api/authorizations/{id}/return` 和 `DELETE /__aisys__/api/my-authorizations/{id}/return` 已进入 Go opt-in；授权列表以 `resource_authorization_grants` 为分页主表并返回轻量 DTO / `sourceSummary`，不触发到期扫描；授权详情按 grant ID 只读当前关系、limits、source 明细和基础 usage 空对象；授权创建 / 归还写 grant、source、runtime authorization、stats dirty 和授权缓存失效。该切片不覆盖授权 usage、更新、回收、到期 worker、quota snapshot worker、前端统一授权页 smoke、生产切流或 Node `/system-teams` / `/authorizations` 删除，详细边界见 `../docs/migration/W4-团队与统一授权迁移记录.md`。
- 不接管任何现有 Node 业务接口，不删除 Node 旧实现。

W1a / W1b / W2 / W3 / W4 当前已迁移路径仍是 Go 实现中，不是生产接管状态；W1b 生产 router 默认不注册 `/__aipublic__`，只有显式设置 `JUHE_AI_PUBLIC_API_ENABLED=true` 才会挂载，且这只表示可灰度验证。W2 / W3 / W4 生产 router 默认不注册 `/__aisys__/api/*` 已迁移后台路径，只有显式设置 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 才会挂载；该开关也只表示可灰度验证，不代表反向代理切流、生产流量或 Node 删除已完成。真实 Docker/testcontainers shell E2E、真实 `w1a-public-settings-smoke`、真实 PG/Redis/Asynq integration、W2/W3/W4 前端 smoke、反向代理切流和 Node 删除证据还未完成。

## 当前 Windows 工具链

当前本机已配置：

- Go：`E:\gosdk\go1.26.5`
- Windows race C 工具链：`E:\tools\w64devkit-2.8.0\w64devkit\bin`
- Go CLI tools：`C:\Users\Administrator\go\bin`

当前 Codex 线程执行 Go 命令前建议显式设置：

```powershell
. .\scripts\use-go-env.ps1
```

该脚本会 fail-fast 校验 `go version` 必须是 `go1.26.5`，`gcc --version` 必须来自 w64devkit `16.1.0`。不要用默认 PATH 下的旧 Go / MinGW 结果记录 W0 / W1 验证。

## 本地验证

从仓库根目录进入 Go 工程：

```powershell
Set-Location backend-go
. .\scripts\use-go-env.ps1
go test ./...
go test ./... -race
go test -tags=integration ./...
go vet ./...
go build ./...
golangci-lint run
govulncheck ./...
```

启动 Go server 前必须先配置 PostgreSQL 和 Redis state，并显式执行 migration；Go 启动路径不会自动迁移 schema：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_TRUST_PROXY = 'false'
$env:JUHE_AI_PUBLIC_API_ENABLED = 'false'
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'false'
goose -dir db/migrations postgres $env:JUHE_AI_POSTGRES_URL up
go run ./cmd/juhe-ai server
```

灰度验证 W1b `/__aipublic__` Go 挂载时，必须额外配置 Redis queue 和稳定密钥，并先启动 ingest worker：

```powershell
$env:JUHE_AI_PUBLIC_API_ENABLED = 'true'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai server
```

`JUHE_AI_PUBLIC_API_ENABLED=true` 不是正式接管证据；没有 account shell E2E、真实 PG/Redis/Asynq integration、公开日志副作用检查、反向代理单 owner 切流和 Node 删除证据前，不允许把 Node `/__aipublic__` 入口删除。

启用 W2 管理端辅助接口灰度挂载：

```powershell
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'true'
go run ./cmd/juhe-ai server
```

`JUHE_AI_MANAGEMENT_API_ENABLED=true` 注册当前已迁移的 W2 管理端辅助路径、W3 auth / system account 切片和 W4 团队 / 授权列表 / 授权详情 / 授权创建 / 授权归还切片。当前可灰度验证的范围包括验证码发放、登录小闭环、当前用户读取、当前用户显示名更新、当前用户改密、当前会话登出、系统账户列表 / 创建 / 更新 / options、授权候选 options、供应商 options / 模型 catalog / 默认测试模型偏好、策略路由 options、分组 options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口、系统团队读写、团队成员维护、授权列表 / 详情 / 创建 / 归还；已迁移管理写接口会按 Node 写模式执行 `last_seen_at` 60 秒节流 touch，读接口和 logout 不 touch。`POST /auth/login` 依赖 PostgreSQL + Redis state，不允许回退 SQLite、进程内验证码或进程内失败锁定；`JUHE_AI_ENV=production` 且未显式配置 `JUHE_AI_COOKIE_SECURE` 时 Go 默认签发 Secure Cookie。完整会话管理生产接管、安全日志、前端真实 Go 后端 smoke、授权 usage、授权更新 / 回收 / 到期、quota snapshot worker、主账户写入 `tags`、OAuth / 导入标签写路径、operation log 保留清理、生产单 owner 切流和 Node `/auth` / `/system-accounts` / `/system-teams` / `/authorizations` 删除仍未完成。

启动 W1b public API log ingest worker 需要 PostgreSQL 和 Redis queue：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-worker ingest
```

该 worker 目前只消费 `public-api-logs` 队列中的 `public-api-log:write` 任务，并不代表 `/__aipublic__` HTTP 链路已经挂载或切流。

`go test -tags=integration ./...` 需要 Docker / testcontainers 可用；当前本机没有 Docker 时会明确跳过容器子测试。
记录验证结论时必须区分 `SKIP` 和真实 `PASS`：只有 Docker / testcontainers 健康并实际启动 PostgreSQL / Redis 容器后，才能把 integration 记为真实 PG/Redis 通过。

`go.sum` 中的 `modernc.org/sqlite` 来自 `github.com/pressly/goose/v3` 测试依赖，不属于当前 Go 业务 runtime；业务代码仍以 PostgreSQL + Redis 为唯一目标。

健康检查：

```powershell
Invoke-RestMethod http://127.0.0.1:3000/__aisys__/health
Invoke-RestMethod http://127.0.0.1:3000/__aisys__/api/health
```

真实 PG/Redis/Asynq smoke：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-maintenance w0-smoke
```

未配置上述 URL 时，`w0-smoke` 会直接失败，避免把依赖 `skipped` 误判成真实 smoke 通过。cache / state / queue 必须指向不同 Redis DB 或不同实例；当前本机没有 Docker 时，testcontainers 容器测试会明确跳过。

真实 W1a public settings smoke：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_TRUST_PROXY = 'false'
go run ./cmd/juhe-ai-maintenance w1a-public-settings-smoke
```

`w1a-public-settings-smoke` 验证 W1a 公开设置读取、当前 migration、`{ data: { appName, appIcon } }` 精确响应、`no-store`、公开字段边界和 Redis state 原子 IP 读限流。该 smoke 通过不代表生产接管；反向代理切流、前端 smoke 和 Node 删除证据仍需单独完成。

真实 W1b public API opt-in smoke：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke
```

运行该命令前需要另一个进程已启动 `go run ./cmd/juhe-ai-worker ingest`，并连接同一 PostgreSQL 与 Redis queue。命令会临时创建内置测试 source/token fixture，通过本地 `httptest` 请求 `GET /__aipublic__/group/list`，等待 worker 把 public API log 写入 PostgreSQL，结束后清理临时 token。输出中的 `takeoverEvidence` 固定为 `false`，`productionTakeoverNotEvaluated` 固定为 `true`；通过不代表 Go server 已监听生产端口，也不代表 `/__aipublic__` 已切流或 Node 可以删除。
