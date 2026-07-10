# Go Backend

> 当前 W0 Go 工程与 PG/Redis/Asynq 常规基线已落地，W1a 公开设置读接口 Go 实现中，W1b `/__aipublic__` 已补公开维护接口基础设施和四类资源纵切面；W2 已补管理端辅助路径的代理列表 / options、系统账户列表读 / options、授权候选 options、供应商列表 / options / 模型 catalog / 默认检查模型偏好、策略路由 options、分组 options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口和 operation log 保留清理 worker；W3 已补验证码、登录、当前用户、当前用户资料、改密、登出、会话列表 / 撤销、系统账户创建、完整 mixed PATCH 和供应商自定义模型 CRUD；W4 已补系统团队读 / 创建 / 更新 / 成员维护，授权列表 / 详情 / 创建 / 更新 / 有效期更新 / 归还 / 回收、批量到期扫描 worker、授权用量 range window 刷新 worker、网关配额快照构建 / 可选 Redis runtime state 发布 worker，以及授权团队 / 用户用量 overview / 授权用量明细 Go opt-in 灰度能力；W5 已补代理管理创建 / 更新 / 删除 / 手动检测、管理端全局品牌设置 GET + PATCH 和系统运行设置 GET + PATCH Go opt-in 灰度能力；W6 已补管理侧 / 个人侧统计 `usage-window` 只读和 system API 两层 read / write 限流对齐。所有生产挂载仍默认关闭，未正式生产接管任何现有 Node 业务接口。
> 本轮另补 `juhe-ai-worker operation-log-retention-cleanup` 的 Go opt-in worker；该 worker 只覆盖 PostgreSQL `operation_logs` 保留期清理，不覆盖完整数据保留任务、公开接口日志 / 运行日志 / 模型检测清理或生产 supervisor 接管。

本目录是 `juhe-ai` 后端从 Node.js 迁移到 Go 的新后端工程。迁移规则见 `../docs/migration/README.md`。

## 当前范围

- Go module、命令入口、配置读取、结构化日志、HTTP 路由和健康检查。
- PostgreSQL health、baseline migration、sqlc catalog 查询、事务封装和 store adapter 基线。
- Redis cache / state client 基线：namespace key、TTL set、pipeline、`IncrWithTTL` 原子计数、`GETDEL` 一次性消费、fixed-window 原子限流、W1b penalty-window 原子限流和 cache / state / queue Redis DB 去重校验。
- Asynq queue 基线：Redis URL 解析、`rediss` TLS、显式 Redis timeout、client ping、enqueue、inspector 和 pending smoke。
- 诊断端点基线：`/__aisys__/health`、`/__aisys__/api/health` 和受 `JUHE_AI_METRICS_ENABLED` 控制的 `/__aisys__/metrics`；pprof 只允许通过 `JUHE_AI_PPROF_ENABLED` 在受控 / loopback 场景启用。后续系统监控指标按 `../docs/migration/Go迁移指标与观测规划.md` 落地，不能沿用 Node event-loop / DB service / SQLite 文件指标。
- W1a 公开设置读接口：`GET /__aisys__/api/settings/public`，读取 `juhe_business.global_settings`，返回 `{ data: { appName, appIcon } }`，并按 `system_settings` 读取限流配置，按 `JUHE_AI_TRUST_PROXY` 识别客户端 IP，生产 server 路径使用 Redis state 原子 minute / burst IP read rate limit。
- W1b 外部维护公开接口基础设施：`internal/modules/publicapi` 固定 16 个 `/__aipublic__` method/path/scope、旧公开路径不进入 catalog、内置测试 token 常量；`internal/modules/publicapi/auth` 固定 Bearer 解析、token hash、source/token 状态与过期、scope 交集、auth error 和 `last_used_at` touch 判断；`internal/modules/publicapi/ratelimit` 映射 source/token 维度 penalty-window；`internal/modules/publicapilog` 提供公开接口日志 request / response 快照、query string 脱敏、32KB 单侧预算、dropped / truncated / empty / complete 状态、499 和错误摘要构造；`internal/jobs/publicapilog` 提供 Asynq payload/enqueue/handler；`internal/jobs/worker` 和 `internal/app/ingest_worker.go` 提供 `juhe-ai-worker ingest` 日志消费 runtime；`internal/httpapi/public_api_shell.go` 提供 W1b HTTP shell / capture 契约测试组合，覆盖 499 客户端提前断开和底层 `ResponseWriter` 可选接口透传；`internal/config/config.go`、`internal/app/server.go` 和 `internal/httpapi/router.go` 提供 `JUHE_AI_PUBLIC_API_ENABLED=false` 默认关闭的生产 router opt-in guard；公开 API 启用时 fail-fast 要求 Redis cache/state/queue，public API Key create/update/delete 会按 Node 顺序发布 validation/runtime/quota 失效；`cmd/juhe-ai-maintenance w1b-public-api-smoke` 提供本地 httptest 灰度 smoke，验证默认 guard、显式 opt-in mount、真实 PostgreSQL / Redis cache / Redis state / Redis queue、临时测试 token 和 worker 写入 public API log；`internal/store/port/publicapi.go` 固定不泄露 pgx/sqlc/Redis 类型的 auth/log store port；`internal/store/postgres/publicapi.go` 提供 PostgreSQL auth/log adapter。
- W1b public group / public route strategy / public API Key / public account 四条资源纵切面：已提供 handler、service、store port、PostgreSQL sqlc query、integration smoke 和 shell E2E 代码；public API Key 采用 hash-only 存储，完整 key 只在新增响应返回一次，日志快照和 `query_string` 均脱敏；public account 上游凭据使用 `JUHE_AI_SECRET` 派生的 AES-GCM 加密，响应不回显 `apiKey` / `baseUrl` / `credentials` / `healthCheckModel`。`account/add` 的 `supportedModels` 使用 presence 三态：省略时继承 `providers.default_supported_models_json`，显式非空数组使用调用方值，显式 `[]` 或最终默认值为空时返回 `账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型`；重复名称冲突继续优先。账户内部 `health_check_model` 为必填字段，创建时按目标系统账户的供应商偏好优先、协议档案 `default_health_check_model` 兜底，并要求属于最终 `supportedModels`；`account/update` 只有在标准化后的模型集合实际变化时校验目录并重写绑定，但任何更新都拒绝留下不属于最终模型集合的检查模型，不再静默清空。公开请求不能提交该内部字段，公开响应也不暴露。详见 `../docs/bug/问题-0048-Go健康检查模型契约漂移.md`。
- W2 管理端辅助接口当前已迁移路径：已覆盖 `proxies` 分页列表读、`proxies/options` 轻量下拉、`system-accounts` 列表读、`system-accounts/options` 轻量下拉、authorization grantee accounts / teams / groups、providers 列表 / options / models / default-health-check-model、route-strategies options、groups options / account-options、accounts options、accounts tags 只读 / 未绑定删除 / 独立 PATCH、operation-logs / my-operation-logs 列表与详情读接口，以及 `juhe-ai-worker operation-log-retention-cleanup` 操作日志保留清理 worker。所有管理 HTTP 路径都复用管理端 session 鉴权和 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭的 router opt-in guard；worker 只允许显式命令灰度验证，不代表生产 supervisor 接管。系统账户创建 / 完整更新已在 W3 Go opt-in 覆盖但未生产接管；代理手动检测已在 W5 Go opt-in 覆盖但未生产接管；完整会话管理生产接管、授权来源 / grant / 授权写接口、主账户写入 `tags`、OpenAI OAuth / 导入标签写路径、完整账号 summary 响应和其他写接口 operation log 仍未迁移。详细边界见 `../docs/migration/W2-管理端只读辅助接口迁移记录.md`。
- W5 代理管理写接口切片：`POST /__aisys__/api/proxies`、`PATCH /__aisys__/api/proxies/{id}`、`DELETE /__aisys__/api/proxies/{id}` 和 `POST /__aisys__/api/proxies/{id}/test` 已进入 Go opt-in。创建 / 更新使用 Node 兼容 AES-256-GCM `v1` 密文保存代理密码，密码不进入响应或操作日志明文；连接参数变化会重置最近检测缓存；删除只读取 4 条绑定账户窗口并在占用时返回 `409`；手动检测复刻 Node `ProxyTestReport`、诊断并发闸门、25 秒总 deadline、15 秒单次探测、512KB 响应体上限、出口信息保留语义和 `proxies.test` 操作日志；写入后发布 gateway runtime cache 失效。管理 API 启用时现在必须配置不少于 32 字符的稳定 `JUHE_AI_SECRET`。该切片不包含前端真实 Go 后端 smoke、生产切流、自动代理检测 worker 或 Node `/proxies` 删除。
- W5 管理端全局品牌设置读写切片：`GET /__aisys__/api/settings/global` 与 `PATCH /__aisys__/api/settings/global` 已进入 Go opt-in，只返回精确 `{ data: { appName, appIcon } }`。两条路径都要求 `admin` / `super_admin`；GET 使用只读 session 和 read limiter，PATCH 使用写鉴权 touch、write limiter、strict partial update 和 PostgreSQL 事务，提交后 best-effort 写 `settings:global` shared cache version 与 `settings.update_global` operation log。已迁移管理写 handler 统一通过共享 operation log 入口执行设置驱动的 changes 清洗和敏感值归一化。默认 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 时不注册。该切片不代表生产切流或 Node settings 删除，详细边界见 `../docs/migration/W5-管理端全局品牌设置读取记录.md`。
- W5 管理端系统运行设置读写切片：`GET /__aisys__/api/settings` 与 `PATCH /__aisys__/api/settings` 已进入 Go opt-in。当前固定 53 key，前端 `SystemSettings` 已补 `usageHotWindowRefreshIntervalSeconds`；PATCH body 上限为 `256 KiB`，超限返回 `413`，JSON parser 位于 system API IP limiter 后且 auth / touch / authenticated user limiter 前。GET 使用 read auth 不 touch，PATCH 使用 write auth touch。PostgreSQL GET 固定白名单有界读取，PATCH 在事务内按 `ORDER BY key ASC FOR UPDATE` 锁定完整 snapshot 并按稳定 key 更新；migration `000024_w5_system_settings.sql` 按 `JUHE_AI_USAGE_STATS_TIMEZONE`、PostgreSQL `TimeZone`、`UTC` 顺序 seed，Node / Go 共存期要求与 Node `Intl` 部署时区一致，在线 PATCH 禁止修改统计时区。提交后 best-effort 执行 `settings:system` shared cache version、reason=`settings_updated` 的 gateway runtime 失效和 `settings.update` / `action=update_settings` operation log。真实依赖 smoke 仍受本机无 Docker 阻塞，不代表生产接管，详细边界见 `../docs/migration/W5-管理端系统运行设置迁移记录.md`。
- W6 统计窗口只读切片：`GET /__aisys__/api/stats/usage-window` 和 `GET /__aisys__/api/my-stats/usage-window` 已进入 Go opt-in。两条路径只读取 `usageStatsTimezone` 系统设置，按配置时区返回当天向前包含当天的固定 31 天窗口；管理侧要求管理员，个人侧允许任意已认证账户，均使用只读鉴权且不 touch session。时区解析兼容 Node 大小写不敏感的 IANA 名称和必要历史别名，显式拒绝 Go-only `Local` / `Factory`，并缓存已验证配置 60 秒；设置缺失、空白或非法时区返回通用 500，不回退 UTC；请求路径不读取使用记录或统计明细，不执行实时聚合。该切片不包含其他 W6 记录 / 日志 / 统计接口、真实 Go 后端前端 smoke、生产切流或 Node 同名路由删除，详细边界见 `../docs/migration/W6-记录与统计读接口迁移记录.md`。
- W6 system API 限流对齐：`/__aisys__/api/*` 的 IP limiter 已扩展为 read / write 两类 60 秒 minute + 10 秒 burst fixed-window，并在已注册管理业务路由鉴权后增加系统账户 read / write 60 秒 fixed-window。六项默认设置依次为 `600/120/180/40/300/120`，设置缓存 60 秒；IP 层位于鉴权前且仅跳过 health，captcha、login 和 `settings/public` 均纳入 IP 层；用户层位于已注册业务路由鉴权后，`/auth/*` 与 `settings/public` 跳过。Redis / 进程内实现、`429 Retry-After` 和中文文案已覆盖，生产 server 同时注入两层 Redis limiter。该范围不是 100% Node 等价：Go 尚未迁移两层 client IP allowlist bypass，且 Node 已认证 404 会经过用户 limiter，而 Go 仍只在已注册业务路由挂载；详细边界见 `../docs/migration/W6-System-API限流对齐记录.md`。
- W2 供应商默认检查模型契约与偏好写入：`GET /providers` 和 `GET /providers/options` 顶层 `defaultHealthCheckModel` 使用用户偏好并在缺失时回退 `systemDefaultHealthCheckModel`，协议档案 `protocolProfiles[].defaultHealthCheckModel` 保持原始事实；`PUT /__aisys__/api/providers/{code}/default-health-check-model` 复用 provider model catalog 可见性校验和 `provider_default_health_check_models` upsert，只允许设置 active 文本生成模型，普通用户强制当前账户作用域，管理员可指定 `systemAccountId`。请求体使用 strict JSON 并拒绝未知字段与尾随 JSON。这些接口仍受 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭门禁约束，详见 `../docs/bug/问题-0048-Go健康检查模型契约漂移.md`。
- W3 自定义供应商模型 CRUD 补充写接口：`POST /__aisys__/api/providers/{code}/models`、`PATCH /__aisys__/api/providers/{code}/models/{id}` 和 `DELETE /__aisys__/api/providers/{code}/models/{id}` 已进入 Go opt-in，覆盖个人 / 管理员目标用户 / 全局模型权限、价格校验、int4 上界、错误优先级、同来源供应商绑定删除保护、默认检查模型偏好清理和 gateway runtime cache 失效；该切片不代表供应商定义写接口、供应商协议档案写接口、前端真实 Go 后端 smoke、生产切流或 Node 删除。
- W2 / W4 资源归还入口权限标记：`accounts/options`、`my-accounts/options`、`groups/options`、`my-groups/options`、`groups/account-options` 和 `my-groups/account-options` 会按授权 runtime 是否存在 active manual source 设置 `permissions.canReturnAuthorization`；owner 资源和纯团队来源授权仍为 false。该字段只用于 Go opt-in 下的资源页归还按钮可见性，不代表完整账户 / 分组列表、详情或写接口已经迁移。
- W3 验证码、登录、当前用户读、资料更新、改密、当前会话登出、会话列表 / 撤销和系统账户写接口切片：`GET /__aisys__/api/auth/captcha` 使用 Redis state 保存 5 分钟 challenge，按客户端 IP 60 次 / 分钟 fixed-window 限流，返回 `{ captchaId, image, expiresAt }` PNG data URL，并提供 Go 内部 `VerifyChallenge` 的 Redis `GETDEL` 一次性消费能力；该 GET 路径同时进入 system API IP read limiter，只有 health 跳过 system API IP limiter。`POST /__aisys__/api/auth/login` 已复用验证码一次性消费、Redis 登录失败 IP / 用户名锁定、active 系统账户密码校验、PG session token hash 写入、`last_login_at` / 初始 `last_seen_at` 和 `juhe_ai_session` Cookie 签发。`GET /__aisys__/api/auth/me`、`GET /__aisys__/api/auth/sessions` 和 `POST /__aisys__/api/auth/logout` 不 touch session；`DELETE /__aisys__/api/auth/sessions/{id}` 只撤销当前系统账户下指定 session，撤销当前 session 时清 Cookie。`PATCH /__aisys__/api/auth/me` 和 `POST /__aisys__/api/auth/change-password` 按 Node 写模式 touch 当前 session。`/auth/*` 全部跳过 authenticated user limiter，但仍按 method 进入 IP read / write limiter。`POST /__aisys__/api/system-accounts` 已补系统账户创建、默认分组 / 路由 / API Key fanout 和操作日志脱敏；`PATCH /__aisys__/api/system-accounts/{id}` 已补 full mixed partial update，支持资料、密码、角色、状态、初始改密标记和图像权限，禁止停用 / 降级最后一个启用 `super_admin`，提交密码或禁用状态会撤销目标全部 session，状态或图像权限真实变化会清理 gateway runtime cache / API Key validation cache。本切片不代表完整 Cookie 安全部署、完整会话管理生产接管、前端真实 Go 后端 smoke、生产单 owner 切流或 Node `/auth` / `/system-accounts` 删除已完成，详细边界见 `../docs/migration/W3-登录与系统账户迁移记录.md`。
- W4 团队与统一授权切片：`GET /__aisys__/api/system-teams`、`GET /__aisys__/api/system-teams/{id}`、`GET /__aisys__/api/my-teams`、`GET /__aisys__/api/my-teams/{id}`、`POST /__aisys__/api/system-teams`、`PATCH /__aisys__/api/system-teams/{id}`、`POST /__aisys__/api/system-teams/{id}/members`、`DELETE /__aisys__/api/system-teams/{id}/members/{memberId}`、`GET /__aisys__/api/authorizations`、`GET /__aisys__/api/authorizations/{id}`、`GET /__aisys__/api/authorizations/{id}/usage`、`GET /__aisys__/api/my-authorizations`、`GET /__aisys__/api/my-authorizations/{id}`、`GET /__aisys__/api/my-authorizations/{id}/usage`、`GET /__aisys__/api/authorizations/usage/team-details`、`GET /__aisys__/api/my-authorizations/usage/team-details`、`GET /__aisys__/api/authorizations/usage/user-details`、`GET /__aisys__/api/my-authorizations/usage/user-details`、`POST /__aisys__/api/authorizations`、`POST /__aisys__/api/my-authorizations`、`PATCH /__aisys__/api/authorizations/{id}`、`PATCH /__aisys__/api/my-authorizations/{id}`、`PATCH /__aisys__/api/authorizations/{id}/expire`、`PATCH /__aisys__/api/my-authorizations/{id}/expire`、`DELETE /__aisys__/api/authorizations/{id}/return`、`DELETE /__aisys__/api/my-authorizations/{id}/return`、`POST /__aisys__/api/accounts/{id}/return-authorization`、`POST /__aisys__/api/my-accounts/{id}/return-authorization`、`POST /__aisys__/api/groups/{id}/return-authorization`、`POST /__aisys__/api/my-groups/{id}/return-authorization`、`DELETE /__aisys__/api/authorizations/{id}` 和 `DELETE /__aisys__/api/my-authorizations/{id}` 已进入 Go opt-in；授权列表以 `resource_authorization_grants` 为分页主表并返回轻量 DTO / `sourceSummary`，不触发到期扫描；授权详情按 grant ID 只读当前关系、limits、source 明细和基础 usage 空对象；授权 team/user usage overview 和授权用量明细只读 `juhe_stats.authorization_*_usage_range_windows` 预聚合窗口，不扫描明细，不实时汇总；授权创建 / 普通更新 / 有效期更新 / grant ID 归还 / 账号分组资源页归还 / 回收写 grant、source、runtime authorization、stats dirty 和授权缓存失效；`juhe-ai-worker authorization-expiry-sweep` 已按 grant 到期索引 fixed window + `FOR UPDATE SKIP LOCKED` 标记 `authorization_expired` 并刷新 runtime；`juhe-ai-worker authorization-usage-range-windows-refresh` 已按 `usageStatsTimezone` 刷新授权 hot range window，只从授权日汇总表写入 range window，不读取 `usage_records`；`juhe-ai-worker gateway-quota-snapshot-build` 已按 Node 当前快照 scope 构建 API Key / 授权成本快照，只读取统计预聚合表和额度小时窗口，不扫描明细，并可通过 `--publish-runtime-state` 写入 Node 兼容 Redis runtime state 供 Node gateway Redis 模式消费。该切片不覆盖批量到期扫描真实部署 / supervisor 接管、授权用量窗口真实 PG smoke / 生产部署接管、网关配额快照真实 PG/Redis 生产部署 smoke、浏览器连接真实 Go 后端的前端团队 / 统一授权页 smoke、生产切流或 Node `/system-teams` / `/authorizations` 删除，详细边界见 `../docs/migration/W4-团队与统一授权迁移记录.md`。
- 不接管任何现有 Node 业务接口，不删除 Node 旧实现。

W1a / W1b / W2 / W3 / W4 / W5 / W6 当前已迁移路径仍是 Go 实现中，不是生产接管状态；W1b 生产 router 默认不注册 `/__aipublic__`，只有显式设置 `JUHE_AI_PUBLIC_API_ENABLED=true` 才会挂载，且这只表示可灰度验证。W2 到 W6 的已迁移管理路径默认不注册，只有显式设置 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 才会挂载；如仅灰度验证 W3 当前用户会话列表 / 撤销，可单独设置 `JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED=true` 只注册 `GET /auth/sessions` 和 `DELETE /auth/sessions/{id}`，不会注册 W5 `GET/PATCH /settings/global` 或 `GET/PATCH /settings`。这些开关都只表示可灰度验证，不代表反向代理切流、生产流量或 Node 删除已完成。真实 Docker/testcontainers shell E2E、真实 `w1a-public-settings-smoke`、真实 PG/Redis/Asynq integration、前端连接真实 Go 后端 smoke、反向代理切流和 Node 删除证据还未完成。

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

W1b 公开账户默认模型语义目标测试：

```powershell
go test ./internal/httpapi -run TestPublicAccount -count=1
go test ./internal/modules/publicaccounts -run TestServiceAdd -count=1
go test ./internal/store/postgres -run TestPublicAccount -count=1
go test ./internal/httpapi ./internal/modules/publicaccounts ./internal/store/postgres -count=1
```

该矩阵固定 HTTP `StringListValue` 的省略 / 显式空 / 非空三态、service 默认继承和最终非空、重复名称优先、provider 默认 JSON 联表 / 解码，以及 `gpt` / `openai` 的 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` seed guard；目标包、全量 Go、目标包 race、vet、tidy diff 和 integration 包编译已通过。

真实 PostgreSQL / Docker 验证：

```powershell
go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsPostgresSmoke -count=1
go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsShellE2E -count=1
```

只有容器真实启动且测试不是 `SKIP`，才能记录默认模型继承、显式空数组 `400`、重复名称优先、真实模型写入、公开日志和响应白名单通过。本机 Docker 未运行，上述两个目标测试均按现有门禁输出 `SKIP`，不计真实 PostgreSQL / shell E2E 通过。

启动 Go server 前必须先配置 PostgreSQL 和 Redis state，并显式执行 migration；Go 启动路径不会自动迁移 schema：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_TRUST_PROXY = 'false'
$env:JUHE_AI_PUBLIC_API_ENABLED = 'false'
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'false'
$env:JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED = 'false'
$env:JUHE_AI_USAGE_STATS_TIMEZONE = 'Asia/Shanghai'
goose -dir db/migrations postgres $env:JUHE_AI_POSTGRES_URL up
go run ./cmd/juhe-ai server
```

`JUHE_AI_USAGE_STATS_TIMEZONE` 应替换为实际部署使用的 IANA 时区。migration `000024` 优先使用该值；未设置时回退 PostgreSQL `TimeZone`，再回退 `UTC`。Node / Go 共存期必须让该 seed 与 Node `Intl` 实际部署时区一致；PostgreSQL 在线禁止通过 settings PATCH 修改统计时区。

灰度验证 W1b `/__aipublic__` Go 挂载时，必须额外配置独立 Redis cache、Redis queue 和稳定密钥，并先启动 ingest worker：

```powershell
$env:JUHE_AI_PUBLIC_API_ENABLED = 'true'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai server
```

`JUHE_AI_PUBLIC_API_ENABLED=true` 不是正式接管证据；没有 account shell E2E、真实 PG/Redis/Asynq integration、公开日志副作用检查、反向代理单 owner 切流和 Node 删除证据前，不允许把 Node `/__aipublic__` 入口删除。

启用 W2 管理端辅助接口灰度挂载：

```powershell
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'true'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai server
```

`JUHE_AI_MANAGEMENT_API_ENABLED=true` 注册当前已迁移的 W2 管理端辅助路径、W3 auth / system account / 自定义供应商模型 CRUD、W4 团队 / 授权切片、W5 代理管理 create/update/delete/test、W5 管理端 `GET/PATCH /settings/global` 与 `GET/PATCH /settings`，以及 W6 管理侧 / 个人侧统计 usage-window。当前可灰度验证的范围包括验证码发放、登录小闭环、当前用户读取、当前用户显示名更新、当前用户改密、当前会话登出、代理列表 / options / 创建 / 更新 / 删除 / 手动检测、全局品牌设置读写、系统运行设置读写、系统账户列表 / 创建 / 更新 / options、授权候选 options、供应商列表 / options / 模型 catalog / 默认检查模型偏好 / 自定义模型 CRUD、策略路由 options、分组 options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口、系统团队读写、团队成员维护、授权读写切片和统计日期窗口。管理 API 启用时必须配置 Redis state、Redis queue 和不少于 32 字符的稳定 `JUHE_AI_SECRET`；生产 server 会对 system API 同时注入鉴权前 IP read / write Redis limiter 和已注册业务路由鉴权后的系统账户 read / write Redis limiter。IP 层仅跳过 health，用户层跳过 `/auth/*` 与 `settings/public`；`GET /settings/global` 和 `GET /settings` 都属于普通管理 read route，使用 read auth 且不 touch。`PATCH /settings/global` 和 `PATCH /settings` 使用 write auth touch；两条 settings PATCH 的 `256 KiB` JSON parser 位于 IP limiter 后且 auth / touch / user limiter 前。当前两层 limiter 尚未迁移 Node client IP allowlist bypass，且不覆盖 Node 全局 `requireAuth` 后 authenticated 404 的用户限流语义，因此不能视为完整 Node 等价或删除证据。完整会话管理生产接管、安全日志、前端真实 Go 后端 smoke、供应商定义写接口、供应商协议档案写接口、各 worker 生产接管、W6 其他记录 / 日志 / 统计接口、主账户写入 `tags`、OAuth / 导入标签写路径、生产单 owner 切流和 Node 对应路径删除仍未完成。

只灰度挂载 W3 当前用户会话列表 / 撤销时，可以保持总管理开关关闭并启用窄开关：

```powershell
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'false'
$env:JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED = 'true'
go run ./cmd/juhe-ai server
```

`JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED=true` 只注册 `GET /__aisys__/api/auth/sessions` 和 `DELETE /__aisys__/api/auth/sessions/{id}`，仍要求 PostgreSQL 和 Redis state；它不会注册 captcha、login、auth/me、logout、系统账户、W2 辅助接口或 W4 团队 / 授权接口。两条 `/auth/*` 路径仍进入 system API IP read / write limiter，但跳过 authenticated user limiter；撤销会话走写鉴权 touch middleware，列表不 touch session。

启动 W1b public API log ingest worker 需要 PostgreSQL 和 Redis queue：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-worker ingest
```

该 worker 目前消费 `public-api-logs` 队列中的 `public-api-log:write` 任务和 `operation-logs` 队列中的 `operation-log:write` 任务，并不代表 `/__aipublic__` 或管理端 HTTP 链路已经挂载或切流。

启动 W2 操作日志保留清理 worker 只需要 PostgreSQL：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
go run ./cmd/juhe-ai-worker operation-log-retention-cleanup
```

该 worker 默认 13 分钟后首次运行，此后每 10 分钟执行一次；每批最多删除 1000 条过期 `operation_logs`，每轮最多 20 批，满批之间暂停 25ms。保留天数优先读取 `system_settings.operationLogRetentionDays`，缺失时使用 Node 当前默认值 365 天；可用 `--run-once` 做一次性 smoke，用 `--retention-days`、`--batch-size`、`--max-batches`、`--interval` 和 `--initial-delay` 调整本地验证参数。它只覆盖操作日志主表保留清理并依赖 FK cascade 清理 targets / viewers / summary search terms；不覆盖 public API log、runtime log、model check、usage record 等完整 data-retention 生产接管。

启动 W4 授权到期扫描 worker 需要 PostgreSQL、Redis state 和 Redis cache：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-worker authorization-expiry-sweep
```

该 worker 默认每 1 分钟扫描一次，初始延迟 54 秒，单批默认沿用 service 的 20 条窗口；可用 `--run-once` 做一次性 smoke，用 `--limit`、`--interval` 和 `--initial-delay` 调整本地验证参数。它会发布授权运行态和授权额度缓存失效，但仍不代表 W4 已完成生产切流或 Node 删除。

启动 W4 授权用量 range window 刷新 worker 只需要 PostgreSQL：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
go run ./cmd/juhe-ai-worker authorization-usage-range-windows-refresh
```

该 worker 默认每 6 小时刷新一次，初始延迟 43 分钟；可用 `--run-once` 做一次性 smoke，用 `--interval`、`--initial-delay` 和 `--timezone` 调整本地验证参数。它按 `usageStatsTimezone` 计算当天、昨日、近 7 天、近 31 天和当月 hot ranges，只从 `juhe_stats.authorization_*_usage_summary_daily` 汇总写入 `authorization_*_usage_range_windows`，不读取 `juhe_usage.usage_records`。真实 PG smoke、生产部署 / supervisor 接管和 Node stats-worker 删除仍未完成。

启动 W4 网关配额快照构建 worker 默认只需要 PostgreSQL：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
go run ./cmd/juhe-ai-worker gateway-quota-snapshot-build --run-once
```

如需把构建结果发布给 Node gateway Redis 模式消费，额外配置 Redis state 并显式开启发布：

```powershell
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-worker gateway-quota-snapshot-build --run-once --publish-runtime-state
```

该 worker 默认每 1 分钟构建一次，初始延迟 37 秒；可用 `--run-once` 做一次性 smoke，用 `--interval`、`--initial-delay`、`--timezone`、`--publish-runtime-state` 和 `--snapshot-ttl` 调整本地验证参数。它只读取 `api_keys`、`resource_authorizations`、`resource_authorization_grants`、`accounts`、系统设置、`usage_stats_*` 预聚合表和 `usage_quota_hourly_windows`，按 Node 当前 API Key / direct authorization / team authorization scope 构建快照并记录数量与完整性；不读取 `juhe_usage.usage_records`，发布时写入 `gateway_quota_snapshot/current` Redis runtime state，不写 Redis shared cache 索引。真实 PG/Redis smoke、生产部署 / supervisor 接管和 Node gateway quota snapshot 删除仍未完成。

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
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke
```

运行该命令前需要另一个进程已启动 `go run ./cmd/juhe-ai-worker ingest`，并连接同一 PostgreSQL 与 Redis queue。命令会临时创建内置测试 source/token fixture，通过本地 `httptest` 请求 `GET /__aipublic__/group/list`，等待 worker 把 public API log 写入 PostgreSQL，结束后清理临时 token。输出中的 `takeoverEvidence` 固定为 `false`，`productionTakeoverNotEvaluated` 固定为 `true`；通过不代表 Go server 已监听生产端口，也不代表 `/__aipublic__` 已切流或 Node 可以删除。

该 maintenance smoke 不调用 `account/add`，不能替代 BUG-0035 的公开账户默认模型定向测试、PostgreSQL smoke 或 shell E2E。
