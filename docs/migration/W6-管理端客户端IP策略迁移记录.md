# W6 管理端客户端 IP 统计与策略迁移记录

> 本文记录客户端 IP 统计列表、账号详情与策略写接口的 Node 当前契约、Go opt-in 实现和删除门禁。`GET /__aisys__/api/ip-stats`、`GET /__aisys__/api/ip-stats/{ipHash}/detail` 与 `allowlist`、`unallowlist`、`blacklist`、`unblock` 四条 POST 已进入 Go opt-in；统计生产 writer / worker 和窗口刷新仍由 Node 负责。Go 代码和测试入口完成不表示真实 PostgreSQL / Redis 已执行通过、生产流量已切到 Go，或 Node IP 管理模块可以删除。

## 基本信息

- 接口：
  - `GET /__aisys__/api/ip-stats`
  - `GET /__aisys__/api/ip-stats/{ipHash}/detail`
  - `POST /__aisys__/api/ip-stats/{ipHash}/allowlist`
  - `POST /__aisys__/api/ip-stats/{ipHash}/unallowlist`
  - `POST /__aisys__/api/ip-stats/{ipHash}/blacklist`
  - `POST /__aisys__/api/ip-stats/{ipHash}/unblock`
- 当前 Node 对照实现：`backend/src/modules/ip-stats/ip-stats.routes.ts`、`backend/src/storage/client-ip-stats-list.repository.ts`、`backend/src/storage/client-ip-stats-detail.repository.ts`
- 当前 Node 统计 writer / refresh owner：`backend/src/storage/client-ip-stats-writer.ts`、`backend/src/storage/client-ip-stats-aggregation.repository.ts`、`backend/src/storage/client-ip-usage-range-windows.repository.ts`
- 目标 Go 列表 / 详情 owner：`backend-go/internal/modules/managementclientipstats/`、`backend-go/internal/httpapi/management_client_ip_stats.go`、`backend-go/internal/httpapi/management_client_ip_stats_detail.go`、`backend-go/internal/store/postgres/managementclientipstats.go`、`backend-go/internal/store/postgres/managementclientipstatsdetail.go`
- 目标 Go 策略 owner：`backend-go/internal/modules/managementclientippolicies/`、`backend-go/internal/httpapi/management_client_ip_policies.go`、`backend-go/internal/store/postgres/managementclientippolicies.go`
- 当前状态：Go opt-in 已实现，真实依赖待复跑，未生产接管
- 关联计划：`../plans/计划-0081-Node转Go渐进减法迁移.md`

## 只读接口与存储 owner

- Go 列表与详情只读取 Node 已生成的 PostgreSQL 预聚合结果，不写 `client_ip_registry`、daily、range window、dirty marker 或 `stats_job_state`，也不触发窗口刷新。
- Node 仍是 `client_ip_registry`、`client_ip_stats_daily`、`client_ip_account_stats_daily`、`client_ip_usage_range_windows`、`client_ip_account_usage_range_windows`、`client_ip_range_window_dirty_ips`、`client_ip_account_range_window_dirty_ips` 和 `stats_job_state(scope_type='client_ip_range_window')` 的生产写入 / 刷新方。
- Go migration `backend-go/db/migrations/000040_w6_management_client_ip_stats_list.sql` 补列表需要的 `client_ip_usage_range_windows` range output、两张 dirty 表及列表 / policy / keyword 查询索引；`000041_w6_management_client_ip_stats_detail.sql` 补详情 reader 需要的 `client_ip_account_usage_range_windows` 和默认 `request_count DESC` 索引。两者都不创建或迁移 `client_ip_stats_daily`、`client_ip_account_stats_daily`，也不代表 Go 已有 client IP stats writer / refresh worker。
- Go 请求 SQL 只读两张 range window、`client_ip_registry`、`client_ip_policies`、两张 dirty 表、`stats_job_state` 以及详情当前页所需的 `accounts/system_accounts` 元数据；源码门禁禁止读取 `usage_records`、daily 表或在请求路径执行 `SUM`、`COUNT`、`GROUP BY`。

## 列表查询与分页契约

- `GET /__aisys__/api/ip-stats` 只允许 `admin` / `super_admin` 管理 Cookie 调用，使用只读 session 鉴权、不 touch session，并进入 system API IP read limiter 和已认证用户 read limiter。
- 查询参数固定为 `page`、`pageSize`、`keyword`、`status`、`startDate`、`endDate`、`lastUsedStartDate`、`lastUsedEndDate`、`sortField`、`sortOrder`。已识别参数重复出现、分页值非法、非法 `status/sortField/sortOrder` 返回 `400 { "message": "IP 统计参数无效" }`；枚举按原值精确匹配、不做 trim，未知参数继续忽略。
- `page` 默认 `1`，`pageSize` 默认 `20` 且范围为 `1..100`。两者保持 Node `Number` 兼容的有限整数语义，可接受等价的十六进制 / 八进制 / 二进制 / 指数写法，拒绝空值、非整数、非有限数和非正数。服务端把页码限制在最多 1000 行的窗口内，即最大页为 `max(1, floor(1000 / pageSize))`，底层固定读取 `pageSize + 1` 行判断 `hasMore`，不执行精确总数查询。
- `startDate/endDate` 按 `usageStatsTimezone` 的自然日解释。缺失或非法值回退当前日，范围夹在最近 31 个自然日内，未来日期夹到当前日，`startDate > endDate` 时收敛为同一天；响应 `range` 返回规范化后的 `startDate/endDate/days/maxDays=31`。
- `lastUsedStartDate/lastUsedEndDate` 任一存在时启用最近使用筛选，并按同一日期归一化规则转换为配置时区的 `[start day, end day + 1)` 边界，筛选 `client_ip_registry.last_seen_at`；两者都缺失时不应用该筛选。
- `keyword` 使用 ECMAScript whitespace trim，只对 `aggregate_ip_key` 和明文 `client_ip` 做大小写敏感的右侧前缀匹配，不搜索 `ip_hash`，不允许前导通配符。
- `status` 默认 `all`，可选 `all | normal | blacklisted | allowlisted`；状态只认 active 且未过期 policy，异常同时存在两类策略时 blacklist 优先显示。
- `sortField` 可选 `requestCount | successCount | errorCount | errorRate | totalTokens | totalCost | activeDays | lastUsedAt`。未传 `sortField` 时固定 `requestCount desc`，即使只传 `sortOrder=asc` 也不改变默认；显式字段未传 `sortOrder` 时默认 `desc`。
- 默认和显式 `sortField=requestCount&sortOrder=desc` 都走独立静态 SQL `ORDER BY request_count DESC, ip_hash ASC`，避免 pgx `cache_statement` 下 PostgreSQL generic plan 无法稳定利用参数化 `CASE` 排序索引。其他排序继续走现有静态字段白名单 `CASE` 查询，并追加稳定 `ip_hash` 兜底。
- `pageUpperBound` 是当前页 offset 加已返回行数，并在 `hasMore=true` 时再加 1 的渐进分页上界，不是精确总数。`rangeReady=false` 时固定返回空 `items`、`pageUpperBound=0`、`hasMore=false`，保留规范化 `page/pageSize/range`；请求不会同步重建窗口。

## 详情查询与分页契约

- `GET /__aisys__/api/ip-stats/{ipHash}/detail` 与列表共用管理员只读鉴权和两层 read limiter。`ipHash` 使用 ECMAScript whitespace trim 后必须为 64 位十六进制，查询前转小写；非法值返回 `400 { "message": "IP 标识无效" }`，registry 不存在优先返回 `404 { "message": "IP 不存在" }`，不会先因统计时区异常误报 `500`。
- 查询参数固定为 `page`、`pageSize`、`startDate`、`endDate`、`sortField`、`sortOrder`，日期、页大小、1000 行窗口、`pageSize + 1` 探测和 `pageUpperBound` 语义与列表一致。详情与列表的默认排序边界不同：详情只传 `sortOrder=asc` 时按 `requestCount ASC`，列表仍忽略孤立的 `sortOrder` 并使用 `requestCount DESC`。
- 详情支持与列表相同的 8 个排序字段；主字段升序时并列 `account_id DESC`，主字段降序时并列 `account_id ASC`。默认和显式 `requestCount DESC` 使用独立静态 SQL 及 `idx_client_ip_account_range_requests`，其他排序使用字段白名单查询；`errorRate` 排序与 Node 一致使用 PostgreSQL `REAL` 精度，`lastUsedAt` 沿用 PostgreSQL 默认 NULL 排序。
- registry 查找先于时区读取；确认 IP 存在后才读取 `usageStatsTimezone`、归一化日期并检查 range readiness。`rangeReady=false` 返回 registry 元数据与空列表，不读取账号窗口。
- ready 路径只读取 `client_ip_account_usage_range_windows` 当前窗口和当前页账号，再通过有界 `LEFT JOIN` 取得账号名与所属系统账户名；不存在的账号元数据和 `averageDurationMs`、`averageFirstTokenMs`、`maxDurationMs`、`lastUsedAt`、`lastErrorAt` 等可选字段通过 `omitempty` 省略，不返回 `null`。

## 策略写接口权限与传输

- 四条接口只允许 `admin` / `super_admin` 管理 Cookie 调用，不提供个人端路径或 owner query。
- 四条路由随 `JUHE_AI_MANAGEMENT_API_ENABLED` 注册，默认关闭。
- 中间件顺序固定为：system API IP write limiter -> `256 KiB` JSON parser -> 写鉴权与 session touch -> 已认证用户 write limiter -> admin role -> mutation guard -> handler。
- 请求体为 strict object，四条路径都只允许可选字符串 `reason`；blacklist 额外允许可选 `durationMinutes` 或 `durationDays`。`reason` 使用 ECMAScript trim，最多 500 个 JavaScript UTF-16 code unit；`null`、非字符串和未知字段返回 `400`。blacklist 时长按 Node JSON number 语义接受 `1`、`1.0`、`1e0` 等等价整数，分钟范围 `1..525600`、天范围 `1..3650`，二者最多传一个；天固定按 24 小时计算。
- `ipHash` trim 后必须为 64 位十六进制；无效值返回 `400 { "message": "IP 标识无效" }`。
- 四条成功响应均为 `200`、`Cache-Control: no-store`：
  - allowlist：`{ "data": ClientIpPolicySummary }`
  - unallowlist：`{ "data": { "disabledCount": number } }`
  - blacklist：`{ "data": ClientIpPolicySummary }`
  - unblock：`{ "data": { "disabledCount": number } }`
- 当前 Node 对业务、注册表和存储错误统一使用 `400`；Go 本切片保持该契约，不改写成 `404` 或通用 `500`。

## 重复提交保护

operation key 分别为：

```text
client_ip_stats.allowlist
client_ip_stats.unallowlist
client_ip_stats.blacklist
client_ip_stats.unblock
```

allowlist、unallowlist 和 unblock 的 fingerprint 固定包含请求中的原始 `ipHash` 和 `reason`；blacklist 还包含按 Node 双精度 JSON number 语义归一化的 `durationMinutes` / `durationDays`。`1`、`1.0`、`1e0` 和 Node 舍入后相同的数值命中同一 fingerprint；指数溢出按 Node `JSON.stringify` 的非有限数行为归一为 `null`。进程内 mutation guard 使用 processing 120 秒、success 60 秒、failure 10 秒窗口，重复请求返回 `409`。普通用户在 mutation guard 前被拒绝，不占用 mutation claim。该 guard 不是分布式幂等事实；跨进程正确性由 PostgreSQL 行锁和事务保证。

## 事务与并发

- allowlist 先按 `ip_hash` 锁定 `client_ip_registry` 行；注册表不存在时返回 `IP 不存在`。
- 锁定后在同一事务停用该 IP 的全部 active 策略，再插入永久 active allowlist，保证白名单与封禁互斥。
- unallowlist 同样尝试锁定注册表行，再只停用 active allowlist；注册表或 active allowlist 不存在时 `disabledCount=0` 仍为成功。
- blacklist 锁定注册表行，停用该 IP 的全部 active 策略，再插入永久或定时 active blacklist；定时策略的创建、更新和过期时间统一为 UTC 三位毫秒文本。
- unblock 尝试锁定注册表行，再只停用 active blacklist；注册表或 active blacklist 不存在时 `disabledCount=0` 仍为成功。
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
- action：`allowlist` / `unallowlist` / `blacklist` / `unblock`
- operation key：`client_ip_stats.<action>`
- resource type：`client_ip`
- resource ID：完整请求 hash；resource name 为前 12 位
- mode：`admin`
- detail level：`full`
- visibility：`admin_only`
- operation scope：空，不能把系统级策略错误归属到操作者账户
- status code：`200`

`disabledCount=0` 的 unallowlist / unblock 仍写成功操作日志；blacklist 日志额外记录 duration label、过期时间和实际时长字段。operation log 入队失败沿用共享 best-effort 入口，不覆盖业务响应。

## 前端契约证据

`frontend/src/api/domains/ipStats.ts` 继续使用现有产品 API，并通过集中 helper 对 `ipHash` 执行 `encodeURIComponent`。`frontend/scripts/regression/ip-stats-policy-api-regression.ts` 固定 list、detail、blacklist、allowlist、unblock、unallowlist 六条产品路径的方法、完整列表 query、编码 URL、body 和响应解包；其中 allowlist / unallowlist 同时覆盖空 payload 和 `disabledCount=0`。

`frontend/scripts/smoke/plan0081-real-go-management-smoke.ts` 的默认只读流程已加入列表和详情读取，校验 ready / not-ready DTO、`pageUpperBound`、`hasMore`、日期范围、账号元数据和各用量字段。普通模式在 range 未就绪或列表为空时允许跳过详情；发布门禁设置 `JUHE_REAL_GO_MANAGEMENT_REQUIRE_CLIENT_IP_DETAIL=1` 后，无可验证目标、详情 `rangeReady=false` 或账号明细为空都失败。`JUHE_REAL_GO_MANAGEMENT_CLIENT_IP_HASH` 可指定已知 64 位十六进制 hash 并强制实际请求详情；单独设置 hash 是允许 not-ready / 空明细的端点探测，需与 strict 标记组合才构成发布门禁。`frontend/scripts/regression/plan0081-real-go-management-smoke-regression.ts` 固定普通跳过、严格无目标 / not-ready / 空明细失败、显式 hash 探测、非法配置、HTTP 错误、超时和敏感值不进入错误文本。真实 smoke 仍要求显式 Go Base URL 与管理员 Cookie，本机未执行真实 listener。

独立 `pnpm smoke:plan0081-real-go-client-ip-allowlist` 只能用于外部加锁、专属且可重建的隔离 fixture，禁止指向共享环境或任意生产 IP hash。当前产品契约没有 expected-none / policy-ID CAS 和可查询幂等结果，脚本无法自行证明 hash 没有业务策略，也无法阻止并发 writer；确认值只能绑定目标，不能替代环境隔离。默认 CI / 发布门禁只运行本地 mock regression。共享或生产环境要无人值守执行 mutation smoke，必须先补“创建要求无 active policy、清理只匹配本次 policy ID、超时可查询幂等结果”的服务端条件写语义。

隔离 fixture 模式必须显式提供真实 Go Base URL、短期管理员 Cookie、专属 lowercase 64 位 hash、外部 lease ID 和绑定完整 API Base URL / hash / lease 的确认值；HTTP 只允许 loopback，其他目标必须使用 HTTPS。脚本只尝试一次 allowlist，验证第一次 unallowlist `=1`、第二次 `=0`，并在 finally 使用每次不同的 reason 有界重试，最终取得连续两次 `disabledCount=0`。传输错误、HTTP `408` 和全部 `5xx` 都按提交不确定处理：先守候 130 秒 horizon，再在尝试开始后至少 160 秒的有效 deadline 内重新清理和确认；另提供 cleanup-only 入口，恢复时不得重新执行 allowlist。成功摘要不打印 Cookie、Base URL、hash、policy ID 或响应 body。

列表 / 详情 / 策略 request-capture、mock real-Go-management smoke、allowlist 安全 smoke regression 与前端 typecheck 已通过。当前未提供真实后端参数，因此真实 management URL / Cookie listener smoke、真实 allowlist 和 cleanup-only 命令均未执行。blacklist / unblock 目前只有 request-capture 和 component integration 入口，还没有具备迟到提交双向清理能力的安全真实前端 smoke。这些证据也不等于浏览器 IP 管理页面已经连接真实 Go 后端完成操作。

## 验证记录

非容器验证命令：

```powershell
Set-Location backend-go
go test ./... -count=1
go test -race ./internal/httpapi ./internal/app ./internal/store/postgres ./internal/modules/managementclientipstats ./internal/modules/managementclientippolicies ./internal/modules/gatewaycache -count=1
go vet ./...
go mod tidy -diff
go test -tags=integration ./internal/testkit/integration -run '^$' -count=1
go vet -tags=integration ./internal/testkit/integration
go test -race -tags=integration ./internal/testkit/integration -run '^$' -count=1

Set-Location ..
pnpm --filter juhe-ai-backend test:client-ip-stats
pnpm --filter juhe-ai-backend test:system-api-rate-limit
pnpm --filter juhe-ai-backend test:gateway-cache-invalidation-index
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-frontend test:ip-stats-policy-api
pnpm --filter juhe-ai-frontend test:plan0081-real-go-management-smoke
pnpm test:plan0081-real-go-client-ip-allowlist-smoke
pnpm --filter juhe-ai-frontend typecheck
git diff --check
```

真实依赖目标命令：

```powershell
Set-Location backend-go
$env:JUHE_AI_REQUIRE_INTEGRATION = '1'
try {
  go test -v -tags=integration ./internal/testkit/integration -run '^(TestW6ManagementClientIPPolicyPostgresRedisAsynqSmoke|TestW6ManagementClientIPStatsListPostgresRedisSmoke|TestW6ManagementClientIPStatsNodeWriterGoReaderSmoke|TestW6ManagementClientIPStatsDetailMigrationNodeWriterGoReaderSmoke)$' -count=1
} finally {
  Remove-Item Env:\JUHE_AI_REQUIRE_INTEGRATION -ErrorAction SilentlyContinue
}
```

不设置强制变量时，本机 Docker provider 不可用会令目标用例输出 `SKIP`，只适合本地发现测试入口；CI、发布和删除门禁必须设置 `JUHE_AI_REQUIRE_INTEGRATION=1`，provider 不健康时 `TestMain` 直接失败并返回非零退出码。本机已确认普通模式为 `SKIP`、强制模式退出码为 `1`，两者都不记录为真实 PostgreSQL、Redis、HTTP、EXPLAIN 或 operation-log ingest 通过。

`TestW6ManagementClientIPPolicyPostgresRedisAsynqSmoke` 通过真实 PostgreSQL store、Redis version reader / 两层 limiter、Asynq ingest worker 和四条 production handler 组装进程内 router，覆盖策略事务、版本消费、限流 bypass 和 operation log。`TestW6ManagementClientIPStatsListPostgresRedisSmoke` 已加入真实 Goose migration、PostgreSQL + Redis 管理鉴权 / 限流、列表筛选 / 排序 / readiness 和 production SQL `EXPLAIN` 断言，并检查默认与显式 `requestCount desc` 在 custom / generic plan 下使用同一静态 SQL。`TestW6ManagementClientIPStatsNodeWriterGoReaderSmoke` 保留 000040 列表迁移证据；`TestW6ManagementClientIPStatsDetailMigrationNodeWriterGoReaderSmoke` 独立执行 Goose `40 -> 41`，先确认 000040 后详情表不存在，再确认 000041 创建详情窗口表和索引，然后让 Node production writer / refresh 写入并由 Go production HTTP reader 读取。Node 子进程只接收显式环境白名单；详情专用模式不预装完整 Node schema，只从 Node production schema 选择 writer 必需的 daily 表和索引，避免掩盖 000041。四者都使用进程内 `httptest` router，不启动 `app.RunServer` listener；本机 Docker provider 不可用，普通模式只完成 skip / 编译门禁，强制模式按预期非零失败，真实 listener、反向代理和前端页面另列门禁。

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
| `f2b018887` | blacklist / unblock 前端 payload 与 URL request-capture |
| `02316ef64` | blacklist / unblock HTTP、鉴权/限流、Node 数值 fingerprint、operation log、router/app wiring |
| `779955d2d` | 四写路径 PostgreSQL / Redis / Asynq production-component smoke 扩展 |
| `b21034cbf`、`8fd78cecd`、`3c59b4375` | 客户端 IP 列表 service、只读 PostgreSQL store、migration `000040` 和 Node 边界对齐 |
| `975600500`、`5d19ddd03` | 客户端 IP 列表 HTTP、router 与 app Go opt-in 挂载 |
| `ee86241bd` | 前端列表 request-capture 与 real-Go-management mock / 真实 smoke 入口 |
| `362f9d8c3`、`98739ffff` | PostgreSQL + Redis component smoke、静态默认排序和 custom / generic plan EXPLAIN 门禁代码 |
| `e5e238cf6` | Node production writer / refresh 到 Go 列表 reader 的跨运行时 smoke 初版 |
| `93485c01e`、`30e5043af`、`423d01033`、`3af58771b` | 详情 range schema、PostgreSQL reader、service、HTTP/router/app Go opt-in 纵切面 |
| `27b14745a` | registry 不存在优先于统计时区错误的详情错误顺序 |
| `818534af8`、`fa362eb85` | Node production writer 到 Go 详情 reader，以及 Goose `40 -> 41` 不被 Node 完整 schema 掩盖的独立证据 |
| `a1ec1a864`、`0414e4b94`、`8d2d9a12c` | 前端详情 real-Go-management smoke，以及要求 ready 且非空账号明细的严格门禁 |
| `41d10a964` | `errorRate` 排序使用与 Node 一致的 PostgreSQL `REAL` 精度 |

## 删除门禁

以下条件全部满足前，不得删除 Node 对应列表、详情或写接口：

1. 健康 Docker/testcontainers 或独立环境中真实 PostgreSQL + Redis + Asynq smoke 通过；客户端 IP 列表和详情 production SQL 的真实 `EXPLAIN` 断言必须实际执行，不接受编译通过替代。
2. 真实 PostgreSQL 并发 allowlist 和跨类型顺序替换证明同一 IP 最终只有一条 active 策略，且无死锁或长锁等待。
3. Node 与 Go 对 `gateway:client-ip-policy-by-ip` 的 shared version 互操作通过，另一 runtime 的下一次读取能看到策略变化。
4. 000040 列表路径和独立 Goose `40 -> 41` 详情路径都在健康真实依赖环境通过 Node production writer / range refresh -> Go reader，覆盖非 UTC 午夜边界、非对称统计和 ready / stale，且 Node 子进程只接收显式环境白名单；Go 不成为 daily、range、dirty 或 stats state writer。
5. 前端 IP 管理页面连接真实 Go 后端完成列表和详情 ready / not-ready、筛选 / 排序 / 分页，以及封禁、解封、加入白名单、移出白名单、两类 `disabledCount=0` 和错误提示 smoke；详情发布验证必须启用严格门禁，可用显式 hash 提供稳定目标，但不能以非严格 hash 探测替代；mutation 脚本必须具备超时迟到提交后的安全清理。
6. 反向代理只把 `GET /ip-stats`、`GET /ip-stats/{ipHash}/detail` 与四条 POST 路径切到 Go 单 owner，并完成回滚演练；Node writer / worker 继续负责统计生产与窗口刷新。
7. 生产观测确认列表 / 详情错误率与延迟、PostgreSQL 查询计划 / 行锁等待、Redis 失效失败和 operation log 入队告警正常。
8. 删除 Node 列表、详情 route 和四条写 route 分支后提供静态搜索和回归证据；共享 repository、daily / range writer、dirty / stats state 仍被 Node worker 使用时不得整体删除。

## 当前结论

客户端 IP 列表、详情与四条写路径已形成 Go store、service、HTTP、router/app、前端 request-capture 和对应 integration smoke 代码；两个读接口只读 Node production writer / worker 生成的预聚合结果，allowlist / unallowlist 另有隔离 fixture 安全真实 Go smoke 入口。Go 全量测试 / vet、目标 race、integration 编译 / vet / race 编译、前端 request-capture、含详情严格门禁的 mock real-Go-management smoke 和 typecheck 已通过。真实依赖普通模式因 Docker 不可用 `SKIP`，强制模式按预期非零失败，因此真实 PostgreSQL / Redis、EXPLAIN、Node writer -> Go reader 和真实 listener 均未执行通过；真实 allowlist smoke 也因缺少隔离 fixture 参数未执行。生产单 owner 切流、回滚和 Node route 删除仍未完成；统计生产 writer / worker 继续由 Node 提供。
