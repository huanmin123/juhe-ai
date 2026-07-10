# W6 System API 限流对齐记录

## 当前范围

本轮把 Go 现有 system API IP read limiter 扩展为两层限流：

- 鉴权前的客户端 IP read / write limiter。
- 已认证管理业务路由鉴权后的系统账户 read / write limiter。

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

## 验证

```powershell
Set-Location backend-go
. .\scripts\use-go-env.ps1
go test ./internal/store/postgres -run TestW1PublicSettingsMigrationSeedsAllSystemAPIRateLimits -count=1
go test ./internal/httpapi -run TestSystemAPIRateLimitSettingsCacheRefreshesAfterTTL -count=1
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
- `429`、`Retry-After` 和中文文案。
- 生产 server 同时注入两层 Redis limiter，缺失已认证 limiter 时已注册管理业务路由 fail-fast。

## 剩余差异与删除门禁

当前实现不能标记为完整 Node 等价，至少还存在以下差异：

- Node 的 IP limiter 和 authenticated user limiter 都会检查 client IP allowlist；Go 当前尚未迁移该 allowlist bypass。白名单 IP 在 Go 中仍会消耗两层 limiter bucket。
- Node 在全局 `requireAuth` 后挂载 authenticated user limiter，因此已认证但未命中业务路由的 404 也会经过用户 limiter；Go 当前只在已注册管理业务路由上挂载用户 limiter，未知路由不会进入该层。
- 本轮只对齐已迁移、已注册的管理业务路由；未迁移或未注册的 system API 不属于本轮用户 limiter 覆盖证据。

满足以下条件前，不得删除 Node system API limiter：

- Go 补齐两层 client IP allowlist bypass，并有 allowlist 命中 / 未命中回归。
- 明确并对齐 authenticated 404 的用户 limiter 归属，或先更新正式契约和测试预期。
- 所有计划接管的 system API 已迁移、已注册并通过 read / write 分类与鉴权顺序测试。
- 真实 PostgreSQL + Redis、可信反向代理客户端 IP、生产配置缓存、两层 Redis limiter 和 `429 Retry-After` smoke 通过。
- 反向代理完成单 owner 切流，并取得 Node limiter 入口删除和静态搜索证据。
