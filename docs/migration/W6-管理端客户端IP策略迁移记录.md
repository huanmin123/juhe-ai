# W6 管理端客户端 IP 策略迁移记录

> 本文记录客户端 IP 白名单写接口的 Node 当前契约、Go opt-in 实现和删除门禁。当前只迁移 `allowlist` / `unallowlist` 两条写路径；列表、详情、封禁和解封仍由 Node 单 owner 提供。Go 代码、前端请求契约、真实 Go smoke 入口和 integration smoke 入口完成，不表示真实依赖已通过、生产流量已切到 Go，或 Node IP 管理模块可以删除。

## 基本信息

- 接口：
  - `POST /__aisys__/api/ip-stats/{ipHash}/allowlist`
  - `POST /__aisys__/api/ip-stats/{ipHash}/unallowlist`
- 当前 Node owner：`backend/src/modules/ip-stats/ip-stats.routes.ts`、`backend/src/storage/client-ip-policy.repository.ts`
- 目标 Go owner：`backend-go/internal/modules/managementclientippolicies/`、`backend-go/internal/httpapi/management_client_ip_policies.go`、`backend-go/internal/store/postgres/managementclientippolicies.go`
- 当前状态：Go opt-in 已实现，真实依赖待复跑，未生产接管
- 关联计划：`../plans/计划-0081-Node转Go渐进减法迁移.md`

## 权限与传输

- 两条接口只允许 `admin` / `super_admin` 管理 Cookie 调用，不提供个人端路径或 owner query。
- 两条路由随 `JUHE_AI_MANAGEMENT_API_ENABLED` 注册，默认关闭。
- 中间件顺序固定为：system API IP write limiter -> `256 KiB` JSON parser -> 写鉴权与 session touch -> 已认证用户 write limiter -> admin role -> mutation guard -> handler。
- 请求体为 strict object，只允许可选字符串 `reason`。`reason` 使用 ECMAScript trim，最多 500 个 JavaScript UTF-16 code unit；`null`、非字符串和未知字段返回 `400`。
- `ipHash` trim 后必须为 64 位十六进制；无效值返回 `400 { "message": "IP 标识无效" }`。
- 两条成功响应均为 `200`、`Cache-Control: no-store`：
  - allowlist：`{ "data": ClientIpPolicySummary }`
  - unallowlist：`{ "data": { "disabledCount": number } }`
- 当前 Node 对业务、注册表和存储错误统一使用 `400`；Go 本切片保持该契约，不改写成 `404` 或通用 `500`。

## 重复提交保护

mutation fingerprint 固定包含请求中的原始 `ipHash` 和 `reason`，operation key 分别为：

```text
client_ip_stats.allowlist
client_ip_stats.unallowlist
```

进程内 mutation guard 使用 processing 120 秒、success 60 秒、failure 10 秒窗口，重复请求返回 `409`。普通用户在 mutation guard 前被拒绝，不占用 mutation claim。该 guard 不是分布式幂等事实；跨进程正确性由 PostgreSQL 行锁和事务保证。

## 事务与并发

- allowlist 先按 `ip_hash` 锁定 `client_ip_registry` 行；注册表不存在时返回 `IP 不存在`。
- 锁定后在同一事务停用该 IP 的全部 active 策略，再插入永久 active allowlist，保证白名单与封禁互斥。
- unallowlist 同样尝试锁定注册表行，再只停用 active allowlist；注册表或 active allowlist 不存在时 `disabledCount=0` 仍为成功。
- 停用语句为有界单语句更新，不在请求路径先读取全量 policy ID。
- 所有事务只包含锁读和数据库写入；Redis、队列和其他外部调用均在提交后执行，避免延长锁持有时间。
- callback 或 commit 失败时不发布缓存失效，也不写成功操作日志。

## 写后副作用

事务提交后以脱离客户端取消、最长 5 秒的 best-effort context 更新 Node 兼容 shared cache version：

```text
gateway:client-ip-policy-by-ip
```

失效失败记录 `management_client_ip_policy_cache_invalidation_failed` warning，不回滚已提交策略，也不把成功写入误报为失败。

操作日志固定为：

- module：`client_ip_stats`
- action：`allowlist` / `unallowlist`
- operation key：`client_ip_stats.<action>`
- resource type：`client_ip`
- resource ID：完整请求 hash；resource name 为前 12 位
- mode：`admin`
- detail level：`full`
- visibility：`admin_only`
- operation scope：空，不能把系统级策略错误归属到操作者账户
- status code：`200`

`disabledCount=0` 的 unallowlist 仍写成功操作日志。operation log 入队失败沿用共享 best-effort 入口，不覆盖业务响应。

## 前端契约证据

`frontend/src/api/domains/ipStats.ts` 继续使用现有产品 API，并通过集中 helper 对 `ipHash` 执行 `encodeURIComponent`。`frontend/scripts/regression/ip-stats-policy-api-regression.ts` 固定 detail、blacklist、allowlist、unblock、unallowlist 五条路径的方法、编码 URL、query/body 和响应解包；其中 allowlist / unallowlist 同时覆盖空 payload 和 `disabledCount=0`。

独立 `pnpm smoke:plan0081-real-go-client-ip-allowlist` 只能用于外部加锁、专属且可重建的隔离 fixture，禁止指向共享环境或任意生产 IP hash。当前产品契约没有 expected-none / policy-ID CAS 和可查询幂等结果，脚本无法自行证明 hash 没有业务策略，也无法阻止并发 writer；确认值只能绑定目标，不能替代环境隔离。默认 CI / 发布门禁只运行本地 mock regression。共享或生产环境要无人值守执行 mutation smoke，必须先补“创建要求无 active policy、清理只匹配本次 policy ID、超时可查询幂等结果”的服务端条件写语义。

隔离 fixture 模式必须显式提供真实 Go Base URL、短期管理员 Cookie、专属 lowercase 64 位 hash、外部 lease ID 和绑定完整 API Base URL / hash / lease 的确认值；HTTP 只允许 loopback，其他目标必须使用 HTTPS。脚本只尝试一次 allowlist，验证第一次 unallowlist `=1`、第二次 `=0`，并在 finally 使用每次不同的 reason 有界重试，最终取得连续两次 `disabledCount=0`。传输错误、HTTP `408` 和全部 `5xx` 都按提交不确定处理：先守候 130 秒 horizon，再在尝试开始后至少 160 秒的有效 deadline 内重新清理和确认；另提供 cleanup-only 入口，恢复时不得重新执行 allowlist。成功摘要不打印 Cookie、Base URL、hash、policy ID 或响应 body。

本地 mock regression 与前端 typecheck 已通过；当前未提供真实后端参数，因此真实命令未执行。这些证据也不等于浏览器 IP 管理页面已经连接真实 Go 后端完成操作。

## 验证记录

非容器验证命令：

```powershell
Set-Location backend-go
go test ./... -count=1
go test -race ./internal/httpapi ./internal/app ./internal/modules/managementclientippolicies ./internal/modules/gatewaycache -count=1
go vet ./...
go mod tidy -diff
go test -tags=integration ./internal/testkit/integration -run '^$' -count=1

Set-Location ..
pnpm --filter juhe-ai-backend test:client-ip-stats
pnpm --filter juhe-ai-backend test:system-api-rate-limit
pnpm --filter juhe-ai-backend test:gateway-cache-invalidation-index
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-frontend test:ip-stats-policy-api
pnpm test:plan0081-real-go-client-ip-allowlist-smoke
pnpm --filter juhe-ai-frontend typecheck
git diff --check
```

真实依赖目标命令：

```powershell
Set-Location backend-go
$env:JUHE_AI_REQUIRE_INTEGRATION = '1'
try {
  go test -v -tags=integration ./internal/testkit/integration -run '^TestW6ManagementClientIPPolicyPostgresRedisAsynqSmoke$' -count=1
} finally {
  Remove-Item Env:\JUHE_AI_REQUIRE_INTEGRATION -ErrorAction SilentlyContinue
}
```

不设置强制变量时，本机 Docker provider 不可用会令目标用例输出 `SKIP`，只适合本地发现测试入口；CI、发布和删除门禁必须设置 `JUHE_AI_REQUIRE_INTEGRATION=1`，provider 不健康时 `TestMain` 直接失败并返回非零退出码。本机已确认普通模式为 `SKIP`、强制模式退出码为 `1`，两者都不记录为真实 PostgreSQL、Redis、HTTP 或 operation-log ingest 通过。

该用例通过真实 PostgreSQL store、Redis version reader / 两层 limiter、Asynq ingest worker 和 production handlers 组装进程内 router，使用 `pg_stat_activity` 证明第二个 allowlist 事务确实等待同一 registry `FOR UPDATE` 行锁；它是 production-component smoke，不启动 `app.RunServer` listener。完整 server 注入继续由 `internal/app` wiring 测试固定，真实 listener、反向代理和前端页面另列门禁。

代码提交：

| 提交 | 内容 |
| --- | --- |
| `5087c1fef` | store port、PostgreSQL 事务、行锁和 SQL 回归 |
| `916fba0e2` | allowlist / unallowlist 前端 request-capture 基线 |
| `f549cd79e` | `gateway:client-ip-policy-by-ip` shared cache version 失效 |
| `ff296ea3e` | allowlist / unallowlist service、校验、事务和提交后失效 |
| `4b63b5bd4` | 五条前端 IP 路径统一编码及完整 request-capture |
| `731d19eed` | HTTP、鉴权/限流、mutation guard、operation log 和 app wiring |
| `619e27491` | 真实 PostgreSQL / Redis / Asynq production-component smoke、版本消费、行锁竞争和审计归属 |
| `06215947f` | 隔离 fixture 目标绑定、迟到提交防护和 cleanup-only 前端真实 Go smoke |
| `3899ab00e` | 全策略时间固定三位毫秒，以及 blacklist / unblock store、service 和事务基础 |

## 删除门禁

以下条件全部满足前，不得删除 Node 对应写接口：

1. 健康 Docker/testcontainers 或独立环境中真实 PostgreSQL + Redis + Asynq smoke 通过。
2. 真实 PostgreSQL 并发 allowlist 证明同一 IP 最终只有一条 active 策略，且无死锁或长锁等待。
3. Node 与 Go 对 `gateway:client-ip-policy-by-ip` 的 shared version 互操作通过，另一 runtime 的下一次读取能看到策略变化。
4. 前端 IP 管理页面连接真实 Go 后端完成加入白名单、移出白名单、`disabledCount=0` 和错误提示 smoke。
5. 反向代理只把这两条 POST 路径切到 Go 单 owner，并完成回滚演练；未迁移的四条 IP 路径继续由 Node 负责。
6. 生产观测确认错误率、延迟、PostgreSQL 行锁等待、Redis 失效失败和 operation log 入队告警正常。
7. 删除 Node route/store 分支后提供静态搜索和回归证据；共享 repository 仍被 blacklist/unblock 使用时不得整体删除。

## 当前结论

客户端 IP allowlist / unallowlist 已形成 Go store、service、HTTP、router/app、缓存失效、操作日志、前端 request-capture、隔离 fixture 真实 Go smoke 入口和 integration smoke 入口。真实依赖普通模式因 Docker 不可用 `SKIP`，强制模式按预期非零失败；真实 Go smoke 因缺少隔离 fixture 参数未执行。`blacklist` / `unblock` 已补 Go port、PostgreSQL 和 service 基础，并将四类策略时间统一为 Node `.SSSZ`，但 HTTP/router/app 尚未注册，因此这两条产品路由仍属于 Node owner。浏览器页面联调、生产单 owner 切流、回滚和 Node 删除仍未完成；IP 列表与详情也继续由 Node 提供。
