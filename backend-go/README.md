# Go Backend

> 2026-07-18 产品撤销：登录会话管理不是当前或后续预留能力，相关灰度路由、配置、service/store/query/test、文档指引和前端入口已按 `PLAN-0150` 完整删除。登录、当前令牌退出、改密和改密后的其他令牌安全撤销继续保留。

> 当前 W0 Go 工程与 PG/Redis/Asynq 常规基线已落地，W1a 公开设置读接口 Go 实现中，W1b `/__aipublic__` 已补公开维护接口基础设施和四类资源纵切面；W2 已补管理端辅助路径的代理列表 / options、系统账户列表读 / options、授权候选 options、供应商列表 / options / 模型 catalog / 默认检查模型偏好、策略路由 options、分组 options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口、外部来源列表 / 详情 / scope / 接入文档目录 / Token secret 和 operation log 保留清理 worker；W3 已补验证码、登录、当前用户、当前用户资料、改密、登出、系统账户创建、完整 mixed PATCH 和供应商自定义模型 CRUD；W4 已补系统团队读 / 创建 / 更新 / 成员维护，授权列表 / 详情 / 创建 / 更新 / 有效期更新 / 归还 / 回收、批量到期扫描 worker、授权用量 range window 刷新 worker、网关配额快照构建 / 可选 Redis runtime state 发布 worker，以及授权团队 / 用户用量 overview / 授权用量明细 Go opt-in 灰度能力；W5 已补代理管理创建 / 更新 / 删除 / 手动检测、管理端全局品牌设置 GET + PATCH、系统运行设置 GET + PATCH，以及分组 create/list/detail/update/delete Go opt-in 灰度能力；W6 已补管理侧 / 个人侧统计 `usage-window`、使用记录列表 / 详情、运行日志列表 / 详情 / facets / runtime、公开接口日志列表 / 详情和审计日志轻量列表只读契约、system API 两层 read / write 限流对齐、客户端 IP 统计列表和四条策略写接口。所有生产挂载仍默认关闭，未正式生产接管任何现有 Node 业务接口。
> 本轮另补 `juhe-ai-worker operation-log-retention-cleanup` 的 Go opt-in worker；该 worker 只覆盖 PostgreSQL `operation_logs` 保留期清理，不覆盖完整数据保留任务、公开接口日志 / 运行日志 / 模型检测清理或生产 supervisor 接管。

本目录是 `juhe-ai` 后端从 Node.js 迁移到 Go 的新后端工程。迁移规则见 `../docs/migration/README.md`。

## 当前范围

- Redis 角色校验按规范化 URL 的 `host:port` 文本判重并拒绝 `localhost` / `::1`，不解析 DNS；部署预检仍必须确认 cache/state/queue 映射到不同 Redis 进程。

- Go module、命令入口、配置读取、结构化日志、HTTP 路由和健康检查。
- PostgreSQL health、baseline migration、sqlc catalog 查询、事务封装和 store adapter 基线。
- Redis cache / state client 基线：namespace key、TTL set、pipeline、`IncrWithTTL` 原子计数、`GETDEL` 一次性消费、fixed-window 原子限流、W1b penalty-window 原子限流和 cache / state / queue Redis 物理端点去重、loopback 别名拒绝校验。
- Asynq queue 基线：Redis URL 解析、`rediss` TLS、显式 Redis timeout、client ping、enqueue、inspector 和 pending smoke。
- 诊断端点基线：`/__aisys__/health`、`/__aisys__/api/health` 和受 `JUHE_AI_METRICS_ENABLED` 控制的 `/__aisys__/metrics`；pprof 只允许通过 `JUHE_AI_PPROF_ENABLED` 在受控 / loopback 场景启用。后续系统监控指标按 `../docs/migration/Go迁移指标与观测规划.md` 落地，不能沿用 Node event-loop / DB service / SQLite 文件指标。
- W1a 公开设置读接口：`GET /__aisys__/api/settings/public`，读取 `juhe_business.global_settings`，返回 `{ data: { appName, appIcon } }`，并按 `system_settings` 读取限流配置，按 `JUHE_AI_TRUST_PROXY` 识别客户端 IP，生产 server 路径使用 Redis state 原子 minute / burst IP read rate limit。
- W1b 外部维护公开接口基础设施：`internal/modules/publicapi` 固定 16 个 `/__aipublic__` method/path/scope、旧公开路径不进入 catalog、内置测试 token 常量；`internal/modules/publicapi/auth` 固定 Bearer 解析、token hash、source/token 状态与过期、scope 交集、auth error 和 `last_used_at` touch 判断；`internal/modules/publicapi/ratelimit` 映射 source/token 维度 penalty-window；`internal/modules/publicapilog` 提供公开接口日志 request / response 快照，query/body/response 在 32KB 单侧预算、深度 8、每对象 / 数组 200 条目和单字符串 4096 字节预览内保留原值，不做递归脱敏；请求 headers 只捕获 `contentType` / `contentLength`，不捕获 `Authorization`、Cookie 或作为 Bearer 凭据传入的来源 token；继续保留 dropped / truncated / empty / complete、499 和错误摘要构造。`internal/jobs/publicapilog` 提供 Asynq payload/enqueue/handler；`internal/jobs/worker` 和 `internal/app/ingest_worker.go` 提供 `juhe-ai-worker ingest` 日志消费 runtime；`internal/httpapi/public_api_shell.go` 提供 W1b HTTP shell / capture 契约测试组合，覆盖 499 客户端提前断开和底层 `ResponseWriter` 可选接口透传；`internal/config/config.go`、`internal/app/server.go` 和 `internal/httpapi/router.go` 提供 `JUHE_AI_PUBLIC_API_ENABLED=false` 默认关闭的生产 router opt-in guard；公开 API 启用时 fail-fast 要求 Redis cache/state/queue，public API Key create/update/delete 会按 Node 顺序发布 validation/runtime/quota 失效；`cmd/juhe-ai-maintenance w1b-public-api-smoke` 提供本地 httptest 灰度 smoke，验证默认 guard、显式 opt-in mount、真实 PostgreSQL / Redis cache / Redis state / Redis queue、临时测试 token 和 worker 写入 public API log；`internal/store/port/publicapi.go` 固定不泄露 pgx/sqlc/Redis 类型的 auth/log store port；`internal/store/postgres/publicapi.go` 提供 PostgreSQL auth/log adapter。
- W1b public group / public route strategy / public API Key / public account 四条资源纵切面：已提供 handler、service、store port、PostgreSQL sqlc query、integration smoke 和 shell E2E 代码；public API Key 采用 hash-only 存储，完整 key 只在新增业务响应返回一次，同时会按原值进入有界响应快照；public account 上游凭据使用 `JUHE_AI_SECRET` 派生的 AES-GCM 加密，业务响应不回显 `apiKey` / `baseUrl` / `credentials` / `healthCheckModel`，但进入请求日志捕获范围的值按原值保存。日志快照语义与业务响应白名单互不替代。`account/add` 的 `supportedModels` 使用 presence 三态：省略时继承 `providers.default_supported_models_json`，显式非空数组使用调用方值，显式 `[]` 或最终默认值为空时返回 `账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型`；重复名称冲突继续优先。账户内部 `health_check_model` 为必填字段，创建时按“个人默认 > 管理员系统默认 > 协议档案默认”解析，并要求属于最终 `supportedModels`；`account/update` 只有在标准化后的模型集合实际变化时校验目录并重写绑定，但任何更新都拒绝留下不属于最终模型集合的检查模型，不再静默清空。公开请求不能提交该内部字段，公开响应也不暴露。详见 `../docs/bug/问题-0048-Go健康检查模型契约漂移.md`。
- W2 管理端辅助接口当前已迁移路径：已覆盖 `proxies` 分页列表读、`proxies/options` 轻量下拉、`system-accounts` 列表读、`system-accounts/options` 轻量下拉、authorization grantee accounts / teams / groups、providers 列表 / options / models / default-health-check-model、route-strategies options、groups options / account-options、accounts options、accounts tags 只读 / 未绑定删除 / 独立 PATCH、operation-logs / my-operation-logs 列表与详情读接口、external-integration-sources 读写与内置 Token reset，以及 `juhe-ai-worker operation-log-retention-cleanup` 操作日志保留清理 worker。所有管理 HTTP 路径都复用管理端 session 鉴权和 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭的 router opt-in guard；worker 只允许显式命令灰度验证，不代表生产 supervisor 接管。系统账户创建 / 完整更新已在 W3 Go opt-in 覆盖但未生产接管；代理手动检测已在 W5 Go opt-in 覆盖但未生产接管；授权来源 / grant / 授权写接口、主账户写入 `tags`、OpenAI OAuth / 导入标签写路径、完整账号 summary 响应和其他写接口 operation log 仍未迁移。详细边界见 `../docs/migration/W2-管理端只读辅助接口迁移记录.md`。
- W2 外部来源切片：列表、详情、`scopes`、`api-docs`、Token secret、来源 `POST/PATCH/DELETE`、Token `POST/PATCH` 和 `POST /__aisys__/api/external-integration-sources/built-in-test-token/reset` 已进入 Go opt-in。详情继续返回安全 `tokens[]`，不含 `primaryToken`、hash、ciphertext 或完整 secret；Token secret 使用 source + token 联合密文点查和 Node 兼容 AES-256-GCM 解密。reset 固定 source -> token 行锁、48 字符一次性明文、hash/密文/preview 轮换、恢复 active 和清空 revoked，保留名称、scope、到期与最近使用字段，并写只含 preview 的 operation log。详情 `POST/PUT`、Token 集合 `GET` 与 Token `PUT/DELETE` 等未定义方法继续显式 `404`。所有路径仍由 `JUHE_AI_MANAGEMENT_API_ENABLED` opt-in，生产单 owner 保持 Node；reset listener smoke 强制 loopback 和两阶段 confirmation，真实 PG/Redis/Asynq 与 listener 证据不等于生产切流或 Node 删除。
- W2 外部来源 Token secret 由 `04c08744d`（service/store）、`ea3069060`（HTTP/router/integration）和 `5459ae57d`（frontend smoke）分块落地。targeted tests / race / vet / sqlc、`go test -p=1 ./...`、`go vet ./...`、`go mod tidy -diff`、integration compile，以及前端 regression / typecheck / build 已通过。该通用 secret 切片当时的精确 PostgreSQL 测试因本机 Docker 不可用而 `SKIP`，通用管理 smoke 的可选 secret reveal 尚未在真实 URL / Cookie 上执行；后续内置 reset 专用 listener 已实际覆盖固定内置 Token 的 secret 回读，但不等同于任意来源 Token secret、生产切流或 Node 删除。
- W5 代理管理写接口切片：`POST /__aisys__/api/proxies`、`PATCH /__aisys__/api/proxies/{id}`、`DELETE /__aisys__/api/proxies/{id}` 和 `POST /__aisys__/api/proxies/{id}/test` 已进入 Go opt-in。创建 / 更新使用 Node 兼容 AES-256-GCM `v1` 密文保存代理密码，密码不进入响应或操作日志明文；连接参数变化会重置最近检测缓存；删除只读取 4 条绑定账户窗口并在占用时返回 `409`；手动检测复刻 Node `ProxyTestReport`、诊断并发闸门、25 秒总 deadline、15 秒单次探测、512KB 响应体上限、出口信息保留语义和 `proxies.test` 操作日志；写入后发布 gateway runtime cache 失效。管理 API 启用时现在必须配置不少于 32 字符的稳定 `JUHE_AI_SECRET`。该切片不包含前端真实 Go 后端 smoke、生产切流、自动代理检测 worker 或 Node `/proxies` 删除。
- W5 管理端全局品牌设置读写切片：`GET /__aisys__/api/settings/global` 与 `PATCH /__aisys__/api/settings/global` 已进入 Go opt-in，只返回精确 `{ data: { appName, appIcon } }`。两条路径都要求 `admin` / `super_admin`；GET 使用只读 session 和 read limiter，PATCH 使用写鉴权 touch、write limiter、strict partial update 和 PostgreSQL 事务，提交后 best-effort 写 `settings:global` shared cache version 与 `settings.update_global` operation log。已迁移管理写 handler 统一通过共享 operation log 入口执行设置驱动的 changes 清洗和敏感值归一化。默认 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 时不注册。该切片不代表生产切流或 Node settings 删除，详细边界见 `../docs/migration/W5-管理端全局品牌设置读取记录.md`。
- W5 管理端系统运行设置读写切片：`GET /__aisys__/api/settings` 与 `PATCH /__aisys__/api/settings` 已进入 Go opt-in。当前固定 53 key；GPT Priority / Flex 只使用模型目录精确档位价格，不再提供通用倍率设置。PATCH body 上限为 `256 KiB`，超限返回 `413`，JSON parser 位于 system API IP limiter 后且 auth / touch / authenticated user limiter 前。GET 使用 read auth 不 touch，PATCH 使用 write auth touch。PostgreSQL GET 固定白名单有界读取，PATCH 在事务内按 `ORDER BY key ASC FOR UPDATE` 锁定完整 snapshot 并按稳定 key 更新；migration `000024_w5_system_settings.sql` 初始化 53 项设置并按 `JUHE_AI_USAGE_STATS_TIMEZONE`、PostgreSQL `TimeZone`、`UTC` 顺序 seed 时区，`000043_w5_remove_gpt_service_tier_multipliers.sql` 删除历史倍率设置行。Node / Go 共存期要求统计时区与 Node `Intl` 部署时区一致，在线 PATCH 禁止修改统计时区。提交后 best-effort 执行 `settings:system` shared cache version、reason=`settings_updated` 的 gateway runtime 失效和 `settings.update` / `action=update_settings` operation log。真实依赖 smoke 与 loopback listener 写 smoke 已有记录，但仍不代表生产接管，详细边界见 `../docs/migration/W5-管理端系统运行设置迁移记录.md`。
- W5 管理端分组 CRUD 切片：管理端与个人端的 `POST /groups`、`GET /groups`、`GET /groups/{id}`、`PATCH /groups/{id}`、`DELETE /groups/{id}` 及对应 `my-groups` 路径已进入 Go opt-in。创建固定 `personal` / `high_concurrency` 契约，列表固定 progressive pagination 与 owner / authorized union，详情固定 owner 账户 ID / Redis 实时并发和 authorized 隐藏边界，更新固定 owner / authorized 可写字段与路由策略保护，删除固定 owner-only、默认分组保护、级联和统计脏标记。完整记录见 `../docs/migration/W5-管理端分组创建迁移记录.md`、`../docs/migration/W5-管理端分组列表迁移记录.md`、`../docs/migration/W5-管理端分组详情迁移记录.md`、`../docs/migration/W5-管理端分组更新迁移记录.md` 和 `../docs/migration/W5-管理端分组删除迁移记录.md`。
- W6 统计窗口只读切片：`GET /__aisys__/api/stats/usage-window` 和 `GET /__aisys__/api/my-stats/usage-window` 已进入 Go opt-in。两条路径只读取 `usageStatsTimezone` 系统设置，按配置时区返回当天向前包含当天的固定 31 天窗口；管理侧要求管理员，个人侧允许任意已认证账户，均使用只读鉴权且不 touch session。时区解析兼容 Node 大小写不敏感的 IANA 名称和必要历史别名，显式拒绝 Go-only `Local` / `Factory`，并缓存已验证配置 60 秒；设置缺失、空白或非法时区返回通用 500，不回退 UTC；请求路径不读取使用记录或统计明细，不执行实时聚合。该切片不包含其他 W6 记录 / 日志 / 统计接口、真实 Go 后端前端 smoke、生产切流或 Node 同名路由删除，详细边界见 `../docs/migration/W6-记录与统计读接口迁移记录.md`。
- W6 公开接口日志只读切片：`GET /__aisys__/api/public-api-logs` 和 `GET /__aisys__/api/public-api-logs/{id}` 已进入 Go opt-in。两条路径 admin-only、只读 session 不 touch、`no-store`；列表保持 Node 筛选和 progressive pagination，只选择摘要列，不读取 payload；详情按 ID 单行读取两个有界 JSON，不存在返回 404。Node PostgreSQL 旧表使用 integer/text，fresh Goose 使用 boolean/bigint/timestamptz，生产切流前必须离线同步并确定单一写入 owner，运行时不做兼容。本批未新增索引，只在 integration 中为已有 created/source 常用索引设置 EXPLAIN 门禁，trace/clientIp 无 EXPLAIN 证据。详细边界见 `../docs/migration/W6-记录与统计读接口迁移记录.md`。
- W6 system API 限流对齐：`/__aisys__/api/*` 的 IP limiter 已扩展为 read / write 两类 60 秒 minute + 10 秒 burst fixed-window，并在已注册管理业务路由鉴权后增加系统账户 read / write 60 秒 fixed-window。六项默认设置依次为 `600/120/180/40/300/120`，设置缓存 60 秒；IP 层位于鉴权前且仅跳过 health，captcha、login 和 `settings/public` 均纳入 IP 层；用户层位于已注册业务路由鉴权后，`/auth/*` 与 `settings/public` 跳过。Redis / 进程内实现、client IP allowlist 两层 bypass、30 秒有界缓存 / shared version 失效、`429 Retry-After` 和中文文案已覆盖，生产 server 同时注入两层 Redis limiter。该范围不是 100% Node 等价：Node 已认证 404 / 错误 method 会经过用户 limiter，而 Go 仍只在已注册业务路由挂载；详细边界见 `../docs/migration/W6-System-API限流对齐记录.md`。
- W6 客户端 IP 统计列表切片：`GET /__aisys__/api/ip-stats` 已进入 Go opt-in，使用只读管理鉴权和 read limiter，只读取 Node 生产 writer / worker 写入的 `client_ip_registry`、`client_ip_usage_range_windows`、两张 range dirty 表、`stats_job_state` 和当前策略结果。migration `000040_w6_management_client_ip_stats_list.sql` 只补列表 range output、两张 dirty 表及查询索引，不迁移 `client_ip_stats_daily`、账号维度 daily / range window、详情 reader 或任何统计 writer。默认及显式 `requestCount desc` 固定使用 `ORDER BY request_count DESC, ip_hash ASC`，其他排序继续使用静态 `CASE` 查询。Go 全量测试 / vet、目标 race、integration 编译 / vet / race 编译和前端 request-capture / mock management smoke / typecheck 已通过；真实 PostgreSQL / Redis、EXPLAIN 断言和真实 Go URL / Cookie listener smoke 因本机 Docker provider 不可用或未提供真实目标而尚未执行。
- W6 客户端 IP 策略写切片：`POST /__aisys__/api/ip-stats/{ipHash}/allowlist`、`unallowlist`、`blacklist`、`unblock` 已进入 Go opt-in。四条路径固定 admin-only、`256 KiB` strict JSON、写 session touch、两层 write limiter、进程内 mutation guard、PostgreSQL registry 行锁和策略互斥、Node 兼容 `gateway:client-ip-policy-by-ip` shared version 与 `client_ip_stats` operation log；blacklist 支持永久、分钟或固定 24 小时天数，四类策略时间统一为 UTC 三位毫秒。production-component smoke 代码覆盖真实 PostgreSQL / Redis / Asynq 组合，但当前机器无 Docker，仅确认普通模式 `SKIP` 和强制模式失败，不计真实依赖通过。详情、Node 统计 writer / worker、真实 listener、前端真实 Go 后端 smoke、生产切流和 Node 删除仍未完成，详细边界见 `../docs/migration/W6-管理端客户端IP策略迁移记录.md`。
- W2 供应商默认检查模型契约与偏好写入：`GET /providers` 和 `GET /providers/options` 顶层 `defaultHealthCheckModel` 按“个人默认 > 管理员系统默认 > 协议档案默认”返回当前作用域有效值，`systemDefaultHealthCheckModel` 只返回管理员显式系统默认，协议档案 `protocolProfiles[].defaultHealthCheckModel` 保持原始事实；`PUT /__aisys__/api/providers/{code}/default-health-check-model` 复用 provider model catalog 可见性校验，只允许设置 active 文本生成模型。普通用户写 `provider_default_health_check_models` 当前账户作用域，管理员写 `provider_system_default_health_check_models` 全局作用域，不能把个人模型设为系统默认。请求体使用 strict JSON 并拒绝未知字段与尾随 JSON。这些接口仍受 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭门禁约束，详见 `../docs/bug/问题-0048-Go健康检查模型契约漂移.md`。
- W3 自定义供应商模型 CRUD 补充写接口：`POST /__aisys__/api/providers/{code}/models`、`PATCH /__aisys__/api/providers/{code}/models/{id}` 和 `DELETE /__aisys__/api/providers/{code}/models/{id}` 已进入 Go opt-in，覆盖个人 / 管理员目标用户 / 全局模型权限、价格校验、int4 上界、错误优先级、同来源供应商绑定删除保护、默认检查模型偏好清理和 gateway runtime cache 失效；该切片不代表供应商定义写接口、供应商协议档案写接口、前端真实 Go 后端 smoke、生产切流或 Node 删除。
- W2 / W4 资源归还入口权限标记：`accounts/options`、`my-accounts/options`、`groups/options`、`my-groups/options`、`groups/account-options` 和 `my-groups/account-options` 会按授权 runtime 是否存在 active manual source 设置 `permissions.canReturnAuthorization`；owner 资源和纯团队来源授权仍为 false。该字段只用于 Go opt-in 下的资源页归还按钮可见性，不代表完整账户 / 分组列表、详情或写接口已经迁移。
- W3 验证码、登录、当前用户读、资料更新、改密、当前令牌登出和系统账户写接口切片：`GET /__aisys__/api/auth/captcha` 使用 Redis state 保存 5 分钟 challenge，按客户端 IP 60 次 / 分钟 fixed-window 限流，返回 `{ captchaId, image, expiresAt }` PNG data URL，并提供 Go 内部 `VerifyChallenge` 的 Redis `GETDEL` 一次性消费能力；该 GET 路径同时进入 system API IP read limiter，只有 health 跳过 system API IP limiter。`POST /__aisys__/api/auth/login` 已复用验证码一次性消费、Redis 登录失败 IP / 用户名锁定、active 系统账户密码校验、PG session token hash 写入、`last_login_at` / 初始 `last_seen_at` 和 `juhe_ai_session` Cookie 签发。`GET /__aisys__/api/auth/me` 和 `POST /__aisys__/api/auth/logout` 不 touch session；`PATCH /__aisys__/api/auth/me` 和 `POST /__aisys__/api/auth/change-password` 按 Node 写模式 touch 当前 session。`/auth/*` 全部跳过 authenticated user limiter，但仍按 method 进入 IP read / write limiter。`POST /__aisys__/api/system-accounts` 已补系统账户创建、默认分组 / 路由 / API Key fanout 和操作日志脱敏；`PATCH /__aisys__/api/system-accounts/{id}` 已补 full mixed partial update，支持资料、密码、角色、状态、初始改密标记和图像权限，禁止停用 / 降级最后一个启用 `super_admin`，提交密码或禁用状态会撤销目标全部 session，状态或图像权限真实变化会清理 gateway runtime cache / API Key validation cache。本切片不代表完整 Cookie 安全部署、前端真实 Go 后端 smoke、生产单 owner 切流或 Node `/auth` / `/system-accounts` 删除已完成，详细边界见 `../docs/migration/W3-登录与系统账户迁移记录.md`。
- W4 团队与统一授权切片：`GET /__aisys__/api/system-teams`、`GET /__aisys__/api/system-teams/{id}`、`GET /__aisys__/api/my-teams`、`GET /__aisys__/api/my-teams/{id}`、`POST /__aisys__/api/system-teams`、`PATCH /__aisys__/api/system-teams/{id}`、`POST /__aisys__/api/system-teams/{id}/members`、`DELETE /__aisys__/api/system-teams/{id}/members/{memberId}`、`GET /__aisys__/api/authorizations`、`GET /__aisys__/api/authorizations/{id}`、`GET /__aisys__/api/authorizations/{id}/usage`、`GET /__aisys__/api/my-authorizations`、`GET /__aisys__/api/my-authorizations/{id}`、`GET /__aisys__/api/my-authorizations/{id}/usage`、`GET /__aisys__/api/authorizations/usage/team-details`、`GET /__aisys__/api/my-authorizations/usage/team-details`、`GET /__aisys__/api/authorizations/usage/user-details`、`GET /__aisys__/api/my-authorizations/usage/user-details`、`POST /__aisys__/api/authorizations`、`POST /__aisys__/api/my-authorizations`、`PATCH /__aisys__/api/authorizations/{id}`、`PATCH /__aisys__/api/my-authorizations/{id}`、`PATCH /__aisys__/api/authorizations/{id}/expire`、`PATCH /__aisys__/api/my-authorizations/{id}/expire`、`DELETE /__aisys__/api/authorizations/{id}/return`、`DELETE /__aisys__/api/my-authorizations/{id}/return`、`POST /__aisys__/api/accounts/{id}/return-authorization`、`POST /__aisys__/api/my-accounts/{id}/return-authorization`、`POST /__aisys__/api/groups/{id}/return-authorization`、`POST /__aisys__/api/my-groups/{id}/return-authorization`、`DELETE /__aisys__/api/authorizations/{id}` 和 `DELETE /__aisys__/api/my-authorizations/{id}` 已进入 Go opt-in；授权列表以 `resource_authorization_grants` 为分页主表并返回轻量 DTO / `sourceSummary`，不触发到期扫描；授权详情按 grant ID 只读当前关系、limits、source 明细和基础 usage 空对象；授权 team/user usage overview 和授权用量明细只读 `juhe_stats.authorization_*_usage_range_windows` 预聚合窗口，不扫描明细，不实时汇总；授权创建 / 普通更新 / 有效期更新 / grant ID 归还 / 账号分组资源页归还 / 回收写 grant、source、runtime authorization、stats dirty 和授权缓存失效；`juhe-ai-worker authorization-expiry-sweep` 已按 grant 到期索引 fixed window + `FOR UPDATE SKIP LOCKED` 标记 `authorization_expired` 并刷新 runtime；`juhe-ai-worker authorization-usage-range-windows-refresh` 已按 `usageStatsTimezone` 刷新授权 hot range window，只从授权日汇总表写入 range window，不读取 `usage_records`；`juhe-ai-worker gateway-quota-snapshot-build` 已按 Node 当前快照 scope 构建 API Key / 授权成本快照，只读取统计预聚合表和额度小时窗口，不扫描明细，并可通过 `--publish-runtime-state` 写入 Node 兼容 Redis runtime state 供 Node gateway Redis 模式消费。该切片不覆盖批量到期扫描真实部署 / supervisor 接管、授权用量窗口真实 PG smoke / 生产部署接管、网关配额快照真实 PG/Redis 生产部署 smoke、浏览器连接真实 Go 后端的前端团队 / 统一授权页 smoke、生产切流或 Node `/system-teams` / `/authorizations` 删除，详细边界见 `../docs/migration/W4-团队与统一授权迁移记录.md`。
- 不接管任何现有 Node 业务接口，不删除 Node 旧实现。

W1a / W1b / W2 / W3 / W4 / W5 / W6 当前已迁移路径仍是 Go 实现中，不是生产接管状态；W1b 生产 router 默认不注册 `/__aipublic__`，只有显式设置 `JUHE_AI_PUBLIC_API_ENABLED=true` 才会挂载，且这只表示可灰度验证。W2 到 W6 的已迁移管理路径默认不注册，只有显式设置 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 才会挂载。这些开关都只表示可灰度验证，不代表反向代理切流、生产流量或 Node 删除已完成。真实 Docker/testcontainers shell E2E、真实 `w1a-public-settings-smoke`、真实 PG/Redis/Asynq integration、前端连接真实 Go 后端 smoke、反向代理切流和 Node 删除证据还未完成。

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
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6380/1'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_TRUST_PROXY = 'false'
$env:JUHE_AI_PUBLIC_API_ENABLED = 'false'
$env:JUHE_AI_MANAGEMENT_API_ENABLED = 'false'
$env:JUHE_AI_AUTH_CAPTCHA_DISABLED = 'false'
$env:JUHE_AI_USAGE_STATS_TIMEZONE = 'Asia/Shanghai'
goose -dir db/migrations postgres $env:JUHE_AI_POSTGRES_URL up
go run ./cmd/juhe-ai server
```

开发、测试自动化或生产临时排障可把 `JUHE_AI_AUTH_CAPTCHA_DISABLED` 设为 `true`。Node 与 Go 会统一让 `GET /auth/captcha` 返回 `required=false`，登录只省略验证码校验，账号密码、登录限频、正式 session Cookie 和权限边界保持不变；服务启动时会输出 `auth_captcha_disabled` warn，排障结束后必须恢复 `false` 并重启。

`JUHE_AI_USAGE_STATS_TIMEZONE` 应替换为实际部署使用的 IANA 时区。migration `000024` 初始化 53 项设置并优先使用该值 seed 时区；未设置时回退 PostgreSQL `TimeZone`，再回退 `UTC`。migration `000043` 删除历史 `gptPriorityPriceMultiplier` 与 `gptFlexPriceMultiplier` 设置行；回滚仅按旧默认值补种且使用 `ON CONFLICT DO NOTHING`。Node / Go 共存期必须让时区 seed 与 Node `Intl` 实际部署时区一致；PostgreSQL 在线禁止通过 settings PATCH 修改统计时区。

灰度验证 W1b `/__aipublic__` Go 挂载时，必须额外配置独立 Redis cache、Redis queue 和稳定密钥，并先启动 ingest worker：

```powershell
$env:JUHE_AI_PUBLIC_API_ENABLED = 'true'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6381/2'
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

`JUHE_AI_MANAGEMENT_API_ENABLED=true` 注册当前已迁移的 W2 管理端辅助路径、W3 auth / system account / 自定义供应商模型 CRUD、W4 团队 / 授权切片、W5 代理管理 create/update/delete/test、W5 管理端 `GET/PATCH /settings/global` 与 `GET/PATCH /settings`、W5 分组 create/list/detail/update/delete 双作用域路由，以及 W6 管理侧 / 个人侧统计 usage-window、运行日志列表 / 详情、公开接口日志列表 / 详情、客户端 IP 统计列表与四条策略写路由。当前可灰度验证的范围包括验证码发放、登录小闭环、当前用户读取、当前用户显示名更新、当前用户改密、当前会话登出、代理列表 / options / 创建 / 更新 / 删除 / 手动检测、全局品牌设置读写、系统运行设置读写、系统账户列表 / 创建 / 更新 / options、授权候选 options、供应商列表 / options / 模型 catalog / 默认检查模型偏好 / 自定义模型 CRUD、策略路由 options、分组 create/list/detail/update/delete / options、账户 options、账户标签只读 / 删除 / PATCH、operation log 管理 / 个人读接口、外部来源列表 / 详情 / scopes / api-docs / Token secret、系统团队读写、团队成员维护、授权读写切片、统计日期窗口、运行日志 / 公开接口日志只读、客户端 IP 统计列表和封禁 / 解封 / 白名单写入。管理 API 启用时必须配置 Redis state、Redis queue 和不少于 32 字符的稳定 `JUHE_AI_SECRET`；生产 server 会对 system API 同时注入鉴权前 IP read / write Redis limiter 和已注册业务路由鉴权后的系统账户 read / write Redis limiter。IP 层仅跳过 health，用户层跳过 `/auth/*` 与 `settings/public`；client IP allowlist 可在不绕过鉴权的前提下绕过两层 limiter，并通过 shared version 立即失效。`GET /settings/global` 和 `GET /settings` 都属于普通管理 read route，使用 read auth 且不 touch。`PATCH /settings/global` 和 `PATCH /settings` 使用 write auth touch；两条 settings PATCH 的 `256 KiB` JSON parser 位于 IP limiter 后且 auth / touch / user limiter 前。当前 limiter 仍不覆盖 Node 全局 `requireAuth` 后 authenticated 404 / 错误 method 的用户限流语义，因此不能视为完整 Node 等价或删除证据。前端真实 Go 后端 smoke、供应商定义写接口、供应商协议档案写接口、各 worker 生产接管、W6 IP 详情和其他记录 / 日志 / 统计接口、客户端 IP 统计生产 writer / worker、公开接口日志离线结构同步与单写 owner、主账户写入 `tags`、OAuth / 导入标签写路径、生产单 owner 切流和 Node 对应路径删除仍未完成。

其中 W5 分组挂载范围已经包含 create/list/detail/update/delete 双作用域路由，W6 只读范围还包含公开接口日志列表 / 详情和 `GET /__aisys__/api/ip-stats`。真实管理 smoke 根命令为 `pnpm smoke:plan0081-real-go-management`，本地 mock regression 为 `pnpm test:plan0081-real-go-management-smoke`。默认 smoke 会读取外部来源列表 / 可选非内置来源详情、公开接口日志第一页、分组、供应商 / 模型 options 和客户端 IP 统计列表，但不会读取任何 Token secret。只有同时提供 `JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_ID` 与 `JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_TOKEN_ID` 才会额外验证 secret；只提供其中一个会在发送 HTTP 前失败。source / token 路径段都使用 encoded segment，成功摘要只输出 `externalIntegrationSourceTokenSecretChecked` 布尔值，明文 Token 和 raw Axios secret 错误不会写入控制台。当前本机未执行显式真实 Go URL / Cookie listener smoke，也未据此验证真实 PostgreSQL / Redis / session；生产切流和 Node 删除仍未完成。完整命令和其他安全边界见 `../docs/develop/测试与验证说明.md`。

启动 W1b public API log ingest worker 需要 PostgreSQL 和 Redis queue：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6381/2'
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
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6380/1'
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

执行数据库 migration 前先检查目录文件名、版本唯一性和过程块的 Goose 语句边界；该命令只流式读取本地文件系统，不连接 PostgreSQL 或 Redis：

```powershell
go run ./cmd/juhe-ai-maintenance migration-catalog-preflight --dir db/migrations
```

在把 `stats-overview`、`system-metrics` 或 `table-monitor` 的 Go 只读路由加入灰度 owner 前，还必须对目标 PostgreSQL 执行 Node stats writer 共存契约检查：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
go run ./cmd/juhe-ai-maintenance stats-schema-contract-preflight
```

该命令只读 `information_schema.columns`，不会建表、补列或启动 writer。成功输出固定声明 `writerOwner=node`，只证明当前 `juhe_stats` 关系和列足够支撑三组 Go reader；它不证明 Node 聚合新鲜、Go stats worker 已接管或可以删除 Node。

真实 PG/Redis/Asynq smoke：

```powershell
$env:JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable'
$env:JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6380/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6381/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
go run ./cmd/juhe-ai-maintenance w0-smoke
```

未配置上述 URL 时，`w0-smoke` 会直接失败，避免把依赖 `skipped` 误判成真实 smoke 通过。cache / state / queue 必须分别指向三个不同 Redis 进程的 `host:port`；不同 DB、namespace、密码或淘汰策略不能替代物理进程隔离，`localhost` / `::1` 也不能作为 Redis URL host。当前本机没有 Docker 时，testcontainers 容器测试会明确跳过。

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
$env:JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6380/1'
$env:JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6381/2'
$env:JUHE_AI_REDIS_NAMESPACE = 'juhe-ai'
$env:JUHE_AI_SECRET = 'replace-with-at-least-32-random-characters'
go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke
```

运行该命令前需要另一个进程已启动 `go run ./cmd/juhe-ai-worker ingest`，并连接同一 PostgreSQL 与 Redis queue。命令会临时创建内置测试 source/token fixture，通过本地 `httptest` 请求 `GET /__aipublic__/group/list`，等待 worker 把 public API log 写入 PostgreSQL，结束后清理临时 token。输出中的 `takeoverEvidence` 固定为 `false`，`productionTakeoverNotEvaluated` 固定为 `true`；通过不代表 Go server 已监听生产端口，也不代表 `/__aipublic__` 已切流或 Node 可以删除。

该 maintenance smoke 不调用 `account/add`，不能替代 BUG-0035 的公开账户默认模型定向测试、PostgreSQL smoke 或 shell E2E。
