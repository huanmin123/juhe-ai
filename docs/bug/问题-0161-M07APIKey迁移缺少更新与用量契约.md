# BUG-0161 M07 API Key 迁移缺少更新与用量契约

## 基本信息

- 编号：BUG-0161
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 网关 / 前端 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：BUG-0152
- 责任人：待定

## 问题概述

- 现象：M07 Go API Key 包新增列表、详情、secret、创建、刷新和删除，但未覆盖 Node/前端已有的 PATCH 更新接口。
- 期望：Go 接管后，`PATCH /api-keys/:id` 和 `PATCH /my-api-keys/:id`、列表真实 usage、写后 validation cache 失效均保持 Node 行为。
- 实际：Go `routes.go`/`store.go` 没有 PATCH；列表 usage 固定为零值；`NewStore` 允许 `inval == nil`，refresh/delete 在未注入 invalidator 时不会执行 Node 要求的 validation cache 失效。owner manifest 仍将 api-keys 标为 `missing`，gateway main 也未挂载该包。
- 影响范围：管理端无法编辑 API Key；列表用量展示错误；网关可能继续使用旧 validation/可用性缓存。

## 根因与证据

- Node/前端端点：`backend/src/modules/api-keys/api-keys.routes.ts`、`frontend/src/api/domains/apiKeys.ts`。
- Go 路由：`backend-go/projects/gateway/internal/apikeys/routes.go` 只有 list/detail/secret/create/refresh/delete。
- usage：`backend-go/projects/gateway/internal/apikeys/store.go` 返回零值；Node `api-key-list-mappers.ts` 会加载真实 usage summaries。
- invalidation：Go `NewStore` 接受 nil invalidator；Node repository 在 refresh/delete 后始终执行 validation cache invalidation。

## 已确认的功能偏差（本轮审计）

### 1. 未指定排程时区时使用了错误的时区来源

- Node 证据：`backend/src/storage/api-key-availability-schedule.ts` 的 `defaultScheduleTimezone()` 调用 `usageStatsTimezone()`；该函数从 `system_settings` 中 `system_account_id = 'sys_admin'`、`key = 'usageStatsTimezone'` 的 JSON 设置读取业务时区，缺失或无效时抛错。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/schedule.go` 的 `defaultScheduleTimezone()` 直接返回 `time.Local`，只有本地时区名不可解析时才回退 `UTC`，没有读取业务设置。
- 影响：请求未传 `availabilitySchedule.timezone` 时，Node 与 Go 可能按不同本地日期/星期/分钟计算排程。这样会改变创建或更新时的 API Key `status`（`active`/`disabled`）以及 `availability_schedule_next_check_at`，并进一步影响网关是否认为该 Key 当前可用。只要部署主机时区与 `usageStatsTimezone` 不一致即可复现。
- 结论：这是可观察的功能结果偏离，不是实现风格差异；需要让 Go 使用与 Node 相同的业务时区来源，并在设置缺失/无效时保持同等失败语义。

### 2. 排程子字段显式 `null` 被错误当成缺失

- Node 证据：`backend/src/modules/api-keys/api-key-availability-schedule.schema.ts` 将 `dateRange`、`exceptions`、例外项 `windows` 定义为 `.optional()`，没有 `.nullable()`；`apiKeyMutationSchema` 只对整个 `availabilitySchedule` 做 `.nullable()`。因此例如 `{"dateRange":null}`、`{"exceptions":null}`，以及拒绝例外 `{"action":"deny","windows":null}` 都应在路由层返回 400。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/schedule.go` 的 `normalizeScheduleDateRange`、`normalizeScheduleExceptions` 以及拒绝例外分支以 `input == nil` / `object["windows"] != nil` 判断，无法区分 JSON 缺失与显式 `null`；这些输入会被接受并归一化为无日期范围、无例外或无 `windows` 的合法排程。
- 影响：客户端传入的非法 JSON 不再得到 Node 的 400，而会创建/更新 API Key 并改变排程存储结果。该偏差不依赖数据库脏数据，直接通过管理 API 可复现。
- 结论：这是输入校验与最终写入结果的功能偏离，需在 Go 解析层保留字段 presence，并对非 nullable 子字段拒绝显式 `null`。

### 3. 额度子项显式 `null` 被错误接受

- Node 证据：`backend/src/modules/request-quota-limit.schema.ts` 中 `hourly`、`daily`、`weekly`、`monthly`、`total` 均为 `.optional()` 的对象 schema，未声明 `.nullable()`；只有 API Key mutation 的顶层 `quotaLimits` 使用 `.nullable()`。因此 `quotaLimits: {"daily":null}` 等请求应返回 400，`quotaLimits:null` 才表示清空全部额度。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/store.go` 的 `normalizeQuotaLimit` / `normalizeHourlyQuotaLimit` 以 `value == nil` 直接返回 nil，把 JSON 显式 `null` 与字段缺失合并；`normalizeQuotaLimits` 随后将其当作空配置写入。
- 影响：非法额度请求在 Go 中可能成功创建或更新，并把原有额度静默清除（尤其是 PATCH 传入 `{quotaLimits:{"daily":null}}` 时），而 Node 会拒绝请求且保留原值。
- 结论：这是可观察的输入校验和数据结果偏离；Go 需要区分字段缺失与显式 `null`，仅对顶层 `quotaLimits:null` 执行清空语义。

### 4. 列表分页数字查询的合法输入集合变窄

- Node 证据：`backend/src/shared/query-values.ts` 的 `integerQueryValue` 对字符串执行 `Number(trimmed)`，只要结果是整数就返回；因此 `page=1e2`、`pageSize=1.0` 等十进制/科学计数法字符串会参与后续 1–窗口范围归一化。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/routes.go` 的 `queryHasInteger`、`queryInteger` 使用 `strconv.Atoi`，仅接受十进制整数文本；同样的 `1e2`/`1.0` 被当成“未提供”，`ListPage` 回落到默认页或默认 `pageSize=50`。
- 影响：相同 HTTP 查询在两套实现中返回不同的 `page`、`pageSize`、`items` 和 `hasMore`。例如 `pageSize=1e2` 时 Node 采用 100（再按上限裁剪），Go 采用默认 50，管理端分页位置和列表内容直接偏离。
- 结论：这是可观察的列表结果差异；Go 应复用 Node 的数字强制转换语义，而不是仅放宽或收紧任意格式。

### 5. PostgreSQL 关键字筛选缺少 C 排序与精确前缀守卫

- Node 证据：`backend/src/storage/api-key.repository.ts` 的 PostgreSQL `buildApiKeyFiltersForClient` 对名称使用 `COLLATE "C" >= ? AND COLLATE "C" < ?`，并额外要求 `starts_with(api_keys.name, ?)`；SQLite 才使用不带该守卫的范围查询。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/store.go` 的 `ListPage` 在 SQLite、PostgreSQL 两种模式都只拼接 `(api_keys.name >= ? AND api_keys.name < ?)`，没有 `COLLATE "C"` 与 `starts_with`。
- 影响：在 PostgreSQL 数据库默认排序规则不是 C，或存在边界字符/大小写排序差异时，Go 的范围条件可能包含并非以关键字开头的名称；Node 会通过 C 排序和 `starts_with` 排除这些行。管理端同一 `keyword` 的 `items`、`total`、`hasMore` 因此可能不一致。
- 结论：这是 PostgreSQL 列表过滤条件的功能偏离，不是 SQL 写法替换；Go 需要保留 Node 的精确前缀语义并按数据库方言构造查询。

### 6. 存储排程的空白脏值被静默当成无排程

- Node 证据：`backend/src/storage/api-key-availability-schedule.ts` 的 `parseApiKeyAvailabilityScheduleJson` 仅对 falsy 值（`null`、`undefined`、空字符串）返回 `undefined`；字符串包含空格时仍执行 `JSON.parse(value)`，因此 `"   "` 等损坏存储会抛错，列表/详情路由进入 500 错误路径。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/schedule.go` 的 `ParseScheduleJSON` 先 `strings.TrimSpace(raw)`，Trim 后为空就直接返回 `nil`，同样的数据库值会被当作没有排程并正常返回 200。
- 影响：数据库排程字段出现仅空白字符时，Go 隐藏了数据损坏并继续提供可用 DTO；Node 会显式失败。排查、告警和上层可用性结果因此偏离，且错误数据可能长期留存。
- 结论：这是读路径错误处理的可观察差异；Go 应区分 NULL/空字符串与非空白但无 JSON 内容的脏值，保持 Node 的失败语义。

### 7. API Key 说明长度将 UTF-16 单元错误地按 Unicode code point 计算

- Node 证据：`backend/src/modules/api-keys/api-keys.routes.ts` 的 mutation schema 使用 `z.string().trim().max(200)`；JavaScript 字符串 `.length` 按 UTF-16 code units 计数。101 个 emoji（每个占两个 code units）已经超过 200，应返回 400。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/routes.go` 的 `createBody` 和 `backend-go/projects/gateway/internal/apikeys/store.go` 的 `normalizeOptionalDescription` 都用 `len([]rune(...)) > 200`，按 code point 计数；同样 101–200 个 emoji 会被接受并写入。
- 影响：相同说明文本在 Node 与 Go 的校验结果不同，Go 可存入 Node 会拒绝的超长值，管理端创建接口的成功/失败及后续 DTO 内容发生偏离。
- 结论：这是输入契约的确定性偏差；Go 需要按 Node 的 UTF-16 code-unit 长度检查，而不是按 rune 数量检查。

### 8. 删除后的关联清理没有投递 worker，且 SQLite 目标登记失败会回滚删除

- Node 证据：`backend/src/modules/api-keys/api-key-cleanup.service.ts` 的 `submitApiKeyRelatedCleanupAsync` 在事务提交后构造 `api_key_related_cleanup` maintenance job；SQLite 先尝试登记目标但捕获登记异常，随后仍继续投递 worker。`api-key.repository.ts` 的 PostgreSQL 删除事务只登记 dataset cleanup target，路由的 `afterCommit` 再负责投递。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/store.go` 的 `Delete` 在删除 API Key 的同一事务内无条件 `INSERT ... api_key_record_cleanup_targets`，提交后只返回 `CleanupTargetAPIKeyID/OwnerID`；`backend-go/projects/gateway/internal/apikeys/routes.go` 的 `remove` 没有调用任何 cleanup queue/worker。历史 M07 Go 包内也没有 `api_key_related_cleanup` 或 maintenance enqueue 实现。
- 影响：Go 删除成功后不会触发 Node 所要求的关联记录清理，相关 usage/日志/分片数据可能长期残留。另一方面，在 SQLite 目标表缺失或写入失败时，Go 会回滚已经执行的 API Key 删除并返回 500；Node 会保持删除成功（204）并将清理任务留给 worker 重试，删除结果相反。
- 结论：这是删除副作用与失败语义的确定性偏离；Go 需要接入同等的 after-commit 清理投递，并按 SQLite/PostgreSQL 保持目标登记的事务边界。

### 9. PostgreSQL API Key 写操作缺少 Node 的行锁串行化

- Node 证据：`backend/src/storage/api-key.repository.ts` 的异步 refresh/delete 查询在 PostgreSQL 下追加 `FOR UPDATE`；异步 create 通过 `findPreferredDefaultRouteStrategyReferenceAsync(..., true)` 与 `assertRouteStrategySelectableForApiKeyAsync(..., true)` 锁定策略、绑定和分组相关行。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/store.go` 的 `RefreshSecret`、`Delete`、`findPreferredDefaultRouteStrategy`、`routeStrategyReference` 均为普通 `SELECT ... LIMIT 1`，没有 PostgreSQL 行锁。
- 影响：并发 refresh 可能让两个请求同时读取同一旧 `updated_at`，Go 其中一个更新成功、另一个返回“已被其他操作修改”；Node 会在行锁后串行读取最新版本并完成两次刷新。创建同时停用/删除默认组或策略路由时，Go 也可能基于过期可用性通过校验并写入 API Key，而 Node 会在锁保护下按串行顺序判定。
- 结论：这是 PostgreSQL 并发下的成功/冲突及最终绑定结果偏离；Go 需要在同等事务边界补齐方言条件的 `FOR UPDATE` 锁，而不是仅依赖更新行数兜底。

### 10. 创建/刷新/删除操作日志丢失 Node 的 HTTP `statusCode`

- Node 证据：`backend/src/modules/api-keys/api-keys.routes.ts` 通过 `runLoggedOperationAsync` 写入日志：create 固定 `statusCode: 201`；refresh 为 validation cache 失败时 500、否则 200；delete 为失效失败时 500、否则 204。上述状态码也会写入 operation log 记录。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/routes.go` 构造的 `authsys.OperationLogEntry` 未携带状态码；`backend-go/projects/gateway/internal/authsys/app.go` 的 `OperationLogEntry` 结构本身没有 `StatusCode` 字段，`OperationLogProducerSink` 因而只能落空值。
- 影响：HTTP 响应状态虽然由 Go 路由返回，但审计查询无法区分创建成功、刷新/删除后的 validation cache 失效失败等结果；同一操作在 Node 与 Go 的日志 DTO 不一致，影响审计、告警和后续重放分析。
- 结论：这是操作日志副作用的功能遗漏；Go 需要把最终 HTTP 状态（尤其是失效失败分支）传入统一日志生产者，而不是仅记录动作和资源字段。

## 修复与验证

- 修改点：补齐 PATCH 及严格校验/乐观并发，接入真实 usage 投影和必需 invalidator；补 gateway main、Redis/SQLite 双模式、前端端点和写后缓存回归。
- 当前验证：Go 包内测试通过不代表 PATCH/usage/失效契约；未执行真实 gateway listener 验证。
- 结论：M07 不能视为完整 API Key 功能迁移或 Node 可归档。
