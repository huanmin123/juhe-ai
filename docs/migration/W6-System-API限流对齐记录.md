# W6 System API 限流对齐记录

## 当前范围

本轮把 Go 现有 system API IP read limiter 扩展为两层限流：

- 鉴权前的客户端 IP read / write limiter。
- 已认证管理业务路由鉴权后的系统账户 read / write limiter。
- 两层 limiter 共用客户端 IP allowlist 判定；命中 active、未过期的 allowlist policy 时不消耗任一 bucket。

该能力随当前 Go 管理 API opt-in 路由使用，生产 server 同时注入两层 Redis limiter。它只对齐已经迁移并在 Go router 中注册的管理业务路由，不代表 system API 已达到 100% Node 等价，也不代表生产接管或 Node 限流代码可以删除。

## 设置与窗口

两层 limiter 都从 `sys_admin` 系统设置读取当前配置，Go 侧缓存设置 60 秒。默认值如下：

| 设置 | 默认值 | 窗口 |
| --- | ---: | --- |
| `systemApiRateLimitIpReadPerMinute` | 600 | 60 秒 fixed-window |
| `systemApiRateLimitIpReadBurstPer10Seconds` | 120 | 10 秒 fixed-window |
| `systemApiRateLimitIpWritePerMinute` | 180 | 60 秒 fixed-window |
| `systemApiRateLimitIpWriteBurstPer10Seconds` | 40 | 10 秒 fixed-window |
| `systemApiRateLimitUserReadPerMinute` | 300 | 60 秒 fixed-window |
| `systemApiRateLimitUserWritePerMinute` | 120 | 60 秒 fixed-window |

`GET`、`HEAD`、`OPTIONS` 归类为 read，其他 HTTP method 归类为 write。IP limiter 同时检查 60 秒 minute bucket 和 10 秒 burst bucket；用户 limiter 检查 60 秒 minute bucket。

## 中间件顺序与跳过范围

Go 当前 system API 链路按以下顺序执行：

1. `/__aisys__/api/*` 先进入 IP limiter。
2. IP limiter 仅跳过 `/__aisys__/api/health`。
3. `/auth/*` 和 `/settings/public` 不进入用户 limiter，但仍进入 IP limiter。
4. 已注册管理业务路由先完成只读鉴权或写鉴权 touch，再按 read / write 进入用户 limiter。
5. 通过两层 limiter 后才进入具体业务 handler。

因此：

- `GET /__aisys__/api/auth/captcha` 已纳入 system API IP read limiter，同时保留验证码自身的生成限流，不再跳过 system API IP limiter。
- `POST /__aisys__/api/auth/login` 进入 IP write limiter，不进入用户 limiter。
- `GET /__aisys__/api/settings/public` 进入 IP read limiter，不进入用户 limiter。
- `/auth/*` 的 current-user、资料、改密、登出和会话路径不进入用户 limiter。
- W2 到 W6 当前已注册的管理 / 个人业务路由在鉴权成功后进入对应用户 read / write limiter。

用户 limiter 位于鉴权后，因此未认证请求先返回鉴权错误，不消耗用户 bucket；写路由仍先执行现有 session touch 语义，再进入用户 write limiter。

## 实现与错误语义

- IP limiter 和用户 limiter 都提供 Redis 与进程内实现；进程内实现用于单元测试和显式注入场景。
- 生产 `server` 路径使用 Redis state，同时注入 IP limiter 和 authenticated user limiter，不静默回退进程内 limiter。
- Redis key 按 IP / 系统账户、read / write 和时间窗口隔离。
- 超限统一返回 `429`，设置 `Retry-After`，响应文案为 `请求过于频繁，请稍后重试`。
- 系统设置读取或 limiter 执行失败按内部错误处理，不把依赖错误详情暴露给客户端。

## 客户端 IP 白名单

- 只对规范化 IPv4 生成 `SHA-256("client-ip:" + ip)` 十六进制 hash；未知地址和 IPv6 不命中 allowlist。
- PostgreSQL 查询联结 `juhe_stats.client_ip_registry` 与 `juhe_stats.client_ip_policies`，只接受 `policy_type=allowlist`、`status=active` 且未过期的最新 policy。
- IP limiter 和 authenticated user limiter 共用同一个 allowlist inspector。命中后继续执行鉴权和业务 handler，但跳过两层 limiter bucket。
- positive / negative 判定都在进程内缓存 30 秒，最多 5000 项，超限收敛至 4500 项；positive cache 不会超过 policy 自身到期时间。
- 配置 `JUHE_AI_REDIS_CACHE_URL` 时，Go 读取 `gateway:client-ip-policy-by-ip` shared cache version；版本变化立即清空本地判定缓存。未配置 cache Redis 时保留 30 秒本地缓存上界。
- policy 查询或 shared cache version 读取失败时记录 warning，不绕过限流，继续执行正常 bucket 判断。

## 验证

```powershell
Set-Location backend-go
. .\scripts\use-go-env.ps1
go test ./internal/store/postgres -run TestW1PublicSettingsMigrationSeedsAllSystemAPIRateLimits -count=1
go test ./internal/httpapi -run TestSystemAPIRateLimitSettingsCacheRefreshesAfterTTL -count=1
go test ./internal/httpapi -run 'SystemAPI.*Allowlist|ClientIP.*Allowlist' -count=1
go test ./internal/httpapi ./internal/store/postgres ./internal/app -count=1
go test -race ./internal/httpapi ./internal/store/postgres ./internal/app -count=1
go test -v -tags=integration ./internal/testkit/integration -run TestW0PostgresMigrationSmoke -count=1
```

前两条是非 Docker 默认门禁：

- `TestW1PublicSettingsMigrationSeedsAllSystemAPIRateLimits` 直接检查 fresh DB migration 定义，确认六个 system API 限流默认键和值完整为 `600/120/180/40/300/120`。
- `TestSystemAPIRateLimitSettingsCacheRefreshesAfterTTL` 使用可控时间验证 60 秒 TTL 到期前复用缓存、到期时重新读取设置。

`TestW0PostgresMigrationSmoke` 用于在 Docker / testcontainers 可用时补充验证 migration 真实 apply 后的数据库值，不是六项默认值的唯一验证入口。

目标测试至少覆盖：

- IP read / write 的 minute + burst 窗口、用户 read / write 的 minute 窗口。
- health 唯一跳过、captcha 纳入、`/auth/*` 与 `settings/public` 跳过用户 limiter。
- 用户 limiter 位于业务鉴权后，管理业务读 / 写鉴权路由分类正确。
- Redis / 进程内实现、设置 60 秒缓存及到期刷新、fresh DB migration 六项默认设置值的非 Docker schema guard。
- allowlist 命中 / 未命中、两层 bucket bypass、鉴权不绕过、IPv4 hash、30 秒缓存、policy expiry、shared cache version 失效和读取失败继续限流。
- `429`、`Retry-After` 和中文文案。
- 生产 server 同时注入两层 Redis limiter，缺失已认证 limiter 时已注册管理业务路由 fail-fast。

## 剩余差异与删除门禁

当前实现不能标记为完整 Node 等价，至少还存在以下差异：

- Node 在全局 `requireAuth` 后、业务 router 前挂载 authenticated user limiter，因此已认证未知路径和错误 method 都会按实际 method 消耗用户 bucket；Go 当前按精确路由和 method 挂载，未知路径和 method mismatch 不进入用户层。
- Go 只迁移了 system API limiter 所需的 allowlist 消费能力；`/ip-stats` 列表、详情以及 `blacklist`、`allowlist`、`unblock`、`unallowlist` 管理路由尚未迁移。
- 本轮只对齐已迁移、已注册的管理业务路由；未迁移或未注册的 system API 不属于本轮用户 limiter 覆盖证据。

满足以下条件前，不得删除 Node system API limiter：

- 保持 allowlist 命中 / 未命中、两层 bypass、鉴权不绕过、读取失败继续限流、policy expiry 和 shared cache version 失效回归通过。
- 明确并对齐已认证未知路径和错误 method 的用户 limiter 归属，或先更新正式契约和测试预期。
- 所有计划接管的 system API 已迁移、已注册并通过 read / write 分类与鉴权顺序测试。
- IP policy 管理 API 完成迁移，或明确生产共存期由 Node 单 owner 写 policy、Go 只消费。
- 真实 PostgreSQL policy 查询、Redis limiter bucket、Redis cache version、Node 写 policy 后 Go 缓存失效、policy expiry、可信反向代理客户端 IP 和 `429 Retry-After` smoke 通过。
- 反向代理完成单 owner 切流，并取得 Node limiter 入口删除和静态搜索证据。
