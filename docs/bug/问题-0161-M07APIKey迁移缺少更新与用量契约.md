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

### 11. SQLite 清理目标写入了错误的数据库

- Node 证据：`backend/src/storage/api-key-record-cleanup.ts` 的 SQLite 登记函数使用 `getDatasetDatabase()`；业务库与 dataset 库是独立 SQLite 文件，后台清理从 dataset 库读取 `api_key_record_cleanup_targets`。
- Go 证据：`backend-go/projects/gateway/internal/apikeys/store.go` 的 `Store` 只持有一个 `*sql.DB`（用于 `api_keys` 等业务表），`datasetTable` 在 `pg == false` 时仅返回无前缀表名，`Delete` 因而把清理目标写入业务库连接。M07 测试在同一内存库创建该表，未覆盖生产的双 SQLite 文件边界。
- 影响：SQLite 生产环境即使删除事务成功，目标也不会出现在 Node/维护侧实际读取的 dataset 数据库；关联 usage/日志/分片记录无法被清理，或因业务库没有该表而直接回滚删除并返回 500。
- 结论：这是存储边界的确定性偏离；Go 需要为 dataset 目标注入独立数据库句柄（或等价的受控写入端口），不能用业务库的同名表替代。

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

### 12. `32bb54673` 的用量补充仍是可选接线，且错误被过度降级

- Go 证据：`32bb54673` 新增 `backend-go/projects/gateway/internal/apikeys/usage.go`，通过 `Store.SetUsageSource` 注入 stats reader；但在该提交的 gateway 代码中没有任何生产调用 `SetUsageSource` 或 `NewStatsUsageSource`，默认 `Store.usage` 仍为 `nil`，列表继续返回全零 usage。只有 `usage_test.go` 手动 setter 才能看到真实值。
- Node 证据：`backend/src/storage/api-key-list-mappers.ts` 总是调用 `loadApiKeyListUsageSummariesForScopesAsync`；其 SQLite 路径仅对明确的缺失 stats DB/表降级为空，PostgreSQL 路径及其他查询错误直接抛出并让列表失败。
- Go 证据：`hydrateListUsage` 对 `UsageSource` 的所有错误统一 `return`，把连接失败、权限错误、SQL 错误等都转成零值成功响应；`StatsUsageSource` 也以宽泛字符串匹配吞掉多类“does not exist”错误。
- 影响：在 32bb 之后，真实生产列表仍可能显示零用量；stats 服务异常时 Go 返回 200/零值，而 Node 会在非缺失错误上返回失败，既掩盖故障也改变管理端观察结果。
- 结论：该补充提交没有闭合 BUG-0161 的 usage 契约；需要完成生产接线，并仅保留 Node 明确允许的缺失资源降级。

### 13. API Key 排程状态切换后缺少网关缓存失效

- Node 证据：`backend/src/storage/api-key-schedule-status-sync.repository.ts` 在同步提交后调用 `invalidateChangedApiKeyCaches`；SQLite 路径逐个清理 lookup cache，PostgreSQL 路径还通过 validation invalidation 通知网关。排程切换后的数据库状态与运行时缓存因此保持一致。
- Go 证据：`32bb54673` 的 `backend-go/projects/jobs/internal/oauthrefresh/availability.go` 中 `SyncApiKeyScheduleStatuses` 仅执行 `applyScheduleUpdates` 并返回 `ChangedIDs`；`oauthrefresh.Store` 没有 cache invalidator 依赖，文件内也没有任何失效调用。
- 影响：排程边界到达时 Go 数据库会从 active/disabled 切换，但网关 validation/lookup cache 仍可能保留旧结果，直到其他刷新或缓存过期才改变实际放行行为；Node 与 Go 的最终可用性结果不一致。
- 结论：这是排程运行态副作用的遗漏；需要为 Go worker 注入等价的 lookup/validation 失效端口，并在提交成功后只对真正改变的 API Key 执行失效。

### 14. 查看完整密钥的操作日志丢失脱敏标识

- Node 证据：历史 `GET /:id/secret` 在返回密钥前构造 `safeChange('key', '密钥标识', undefined, \`${apiKey.keyPrefix}...${apiKey.keySuffix}\`)`；日志的 `before` 不写入，`after` 保留可核对的前后缀而不包含完整密钥（`backend/src/modules/api-keys/api-keys.routes.ts`）。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/routes.go` 的 `reveal` 记录 `{Field: "key", Label: "密钥标识", Before: "未设置", After: "已变更", Sensitive: true}`，没有写入 `keyPrefix...keySuffix`，并额外产生了 Node 不存在的 `before` 值。
- 结果偏差：同一成功查看操作在 operation log 中无法像 Node 一样通过前后缀确认实际密钥版本，审计变更字段也从“无 before + 脱敏标识”变成“未设置→已变更”；依赖日志追溯密钥轮换/泄露调查时，Go 结果信息不足且 DTO 不一致。该差异不影响明文密钥 HTTP 响应，但属于已迁移管理副作用的功能遗漏。
- 结论：这是 M07 `reveal_secret` 操作日志投影的确定性偏差，当前仅记录，未修改 Go 代码。

### 15. 列表/详情对关联策略路由 `mode` 的默认值与脏值语义不一致

- Node 证据：历史 `api-key-list-mappers.ts` 与 `api-key-mappers.ts` 在读取 `route_strategy_mode` 时调用 `normalizeRouteStrategyMode`；该函数对 `null`/空字符串补默认 `normal`，对 `normal`、`hybrid_smart`、`weighted`、`failover`、`round_robin` 以外的值直接抛出“路由策略模式无效”。路由策略表的 `mode` 仅有 `NOT NULL DEFAULT`，没有枚举 `CHECK`，因此空字符串或未知脏值仍可能被读到。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/store.go` 的 `normalizedRouteStrategyMode` 对 `NULL`/空字符串返回 `nil`，对未知值也静默返回 `nil`；`newListItem` 同时用于 `GET /api-keys` 列表和 `GET /api-keys/:id` 详情。
- 结果偏差：关联策略 `mode=''` 时 Node 返回 `routeStrategyMode: "normal"`，Go 省略字段；关联策略 `mode='bogus'` 时 Node 让列表/详情进入错误处理，Go 却以 200 成功并省略该字段。这样 API Key 的路由能力展示与损坏数据可见性均不一致，前端可能把未知策略误当成无模式而继续编辑或放行。
- 结论：这是 M07 列表/详情读模型的确定性状态规范化缺口，当前仅记录，未修改 Go 代码。

### 16. API Key 详情遗漏 Node 的真实 usage 汇总

- Node 证据：历史 `findApiKeySummaryAsync` 读取后调用 `apiKeySummariesFromRowsAsync`，默认 `includeUsage !== false`，会按 `{systemAccountId, scopeId: apiKey.id}` 加载并返回 `usage`（`requestCount`、`totalTokens`、`totalCost`）；同一 mapper 也用于详情 DTO。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/store.go` 的 `FindDetail` 复用 `newListItem`，该函数把 `Usage` 固定填为 `emptyListUsageSummary()`；历史 M07 包没有详情 usage 查询或注入路径。
- 结果偏差：同一 `GET /api-keys/:id`（以及 self 详情）在 Node 返回该 API Key 的真实累计用量，Go 始终返回零值。即使列表 usage 后续完成接线，详情仍会显示错误统计，前端详情页和审计使用方无法得到 Node 的结果。
- 结论：这是 M07 详情读模型的确定性字段遗漏，当前仅记录，未修改 Go 代码。

### 17. 创建时 `expiresAt` 纯空白字符串被错误当成清除值

- Node 证据：历史 `apiKeyMutationSchema` 允许 `expiresAt` 为字符串后，`normalizeOptionalApiKeyExpiresAt` 只把 `undefined`、`null` 和精确空字符串 `''` 当成未设置；非空但 `trim()` 后为空的值（例如 `'   '`）会抛出“API Key 过期时间必须是有效时间字符串”，创建返回 400。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/routes.go` 的 `createBody` 接受任意字符串 `expiresAt`，`backend-go/projects/gateway/internal/apikeys/store.go` 的 `normalizeOptionalExpiresAt` 以 `strings.TrimSpace(*value) == ""` 直接返回 NULL，因此纯空白输入会成功创建且不设置过期时间。
- 结果偏差：相同 `POST /api-keys` 请求 `{"name":"...","expiresAt":"   "}` 在 Node 被拒绝，Go 返回 201 并持久化无过期时间；客户端无法依赖 Node 的非法时间输入保护，且错误请求会产生真实密钥和审计记录。
- 结论：这是 M07 创建时间字段的确定性输入契约偏差，当前仅记录，未修改 Go 代码。

### 18. 刷新密钥操作日志丢失旧/新脱敏前后缀

- Node 证据：历史 `POST /:id/refresh-key` 的 `safeChange('key', '密钥标识', \`${outcome.previousKeyPrefix}...${outcome.previousKeySuffix}\`, \`${outcome.result.keyPrefix}...${outcome.result.keySuffix}\`)` 同时记录旧密钥和新密钥的脱敏标识，并保留 validation-cache 失败时的 500 `statusCode`。
- Go 证据：历史 `backend-go/projects/gateway/internal/apikeys/routes.go` 的 `refresh_key` 日志固定写 `{Before: "已设置", After: "已变更", Sensitive: true}`，没有使用 `PreviousKeyPrefix/PreviousKeySuffix`，也没有在当前日志结构中携带状态码（状态码遗漏另有已登记子项）。
- 结果偏差：刷新成功或失效失败时，Node 审计可以核对密钥轮换前后的掩码，Go 只能看到泛化状态，无法确认轮换对象；日志字段内容和追溯能力均不一致。完整密钥不会因此泄露，但审计副作用仍不等价。
- 结论：这是 M07 `refresh_key` 日志投影相对 reveal 之外的独立确定性偏差，当前仅记录，未修改 Go 代码。

### 19. API Key 排程状态同步只有库代码，未接入 Go 生产调度入口

- Node 证据：历史 `32bb54673` 的 `backend/src/modules/background/background-jobs.ts` 注册 `api-key-availability-schedule-status-sync`，每 10 秒执行一次（初始延迟 1 秒，并通过 PostgreSQL scheduled lease 防重入）；任务调用 `runApiKeyAvailabilityScheduleStatusSync`，再请求 DB service 的 `sync_api_key_availability_schedule_statuses`。提交后对 `changedIds` 清理网关运行态缓存，并对无效排程记录告警。
- Go 证据：同一历史提交新增 `backend-go/projects/jobs/internal/oauthrefresh/availability.go`，其中已有 `Store.SyncApiKeyScheduleStatuses`，并配套 `schedule_test.go`/`availability_test.go`；但 `backend-go/projects/jobs/cmd/juhe-ai-jobs/main.go` 没有导入 `backend-go-jobs/internal/oauthrefresh`，`components` 只注册 F1、F2、J1、模型恢复、J2、J3，启动日志也没有 J4 或 API Key 排程任务。以 `git grep` 检查该提交的 Go 生产代码，除 `oauthrefresh` 包自身及其测试外没有 `oauthrefresh.NewRunner`、`SyncApiKeyScheduleStatuses` 或等价调度调用。网关侧 `backend-go/projects/gateway/internal/business/account_runtime/operations.go` 还把 `sync_api_key_availability_schedule_statuses` 列在 `OutstandingManifestOperations`，注明仍需 `ScheduleEvaluator`，说明该能力没有完成 owner 接线。
- 结果偏差：Go 包内单测通过只证明可调用的库逻辑存在，并不代表生产进程会运行它。若按该历史迁移结果由 Go 接管，API Key 到达排程边界时不会有 10 秒扫描、事件去重写回、`next_check_at` 推进、无效排程禁用或 changed ID 的网关缓存失效；数据库状态和网关实际放行结果会长期停留在旧值，而 Node 会在下一轮调度完成切换。
- 结论：这是运行时接线缺失造成的完整功能未迁移，不是 Node/Go 排程算法的语义小差异。当前仅登记，未修改 Go 代码；修复时必须补齐唯一的 Go jobs owner、租约/周期/失败退避配置以及提交后的缓存失效端口，并用可观测的启动注册和边界回放证明任务实际运行。

## 修复与验证

- 修改点：补齐 PATCH 及严格校验/乐观并发，接入真实 usage 投影和必需 invalidator；补 gateway main、Redis/SQLite 双模式、前端端点和写后缓存回归。
- 当前验证：Go 包内测试通过不代表 PATCH/usage/失效契约；未执行真实 gateway listener 验证。
- 新增未修复子项：`reveal_secret` 日志应与 Node 一样保留 `keyPrefix...keySuffix` 脱敏 after、不要伪造 `before: "未设置"`，并补充 operation-log JSON 回放，确认完整密钥不会进入日志。
- 新增未修复子项：API Key 列表/详情读取关联策略时应复用 Node 的 `mode` 规范化：空值补 `normal`，未知值显式失败；补充空字符串、未知 mode 和正常五种 mode 的 Node/Go DTO 与错误回放。
- 新增未修复子项：API Key 详情必须像 Node `findApiKeySummaryAsync` 一样加载并返回该 key 的真实 usage 汇总，补充非零 usage 与空 usage 的 Node/Go DTO 回放；不能只修列表。
- 新增未修复子项：创建 `expiresAt` 必须区分精确空字符串与纯空白字符串：仅前者按未设置处理，后者应保持 Node 的 400；补充空字符串、空白、合法 offset 和非法时间的回放。
- 新增未修复子项：`refresh_key` 日志应保留 Node 的旧/新 `keyPrefix...keySuffix` 脱敏值，并与 statusCode 一起回放；确认日志不写入完整密钥且能区分轮换前后。
- 结论：M07 不能视为完整 API Key 功能迁移或 Node 可归档。
