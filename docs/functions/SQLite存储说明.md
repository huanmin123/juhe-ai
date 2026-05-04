# SQLite 存储说明

## 为什么用 SQLite

当前项目只给个人使用，不需要复杂部署、水平扩展或极限并发。SQLite 足够稳定，文件备份也简单，更符合轻量项目定位。

## 默认位置

后端默认数据库文件：

```text
backend/data/juhe-ai.sqlite3
```

如需调整位置，编辑项目内本地配置文件 `backend/.env`，不设置系统环境变量：

```dotenv
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
```

相对路径按 `backend/` 目录解析。为了保持可移植部署，推荐使用 `./data/juhe-ai.sqlite3` 这类项目内相对路径；如果确实要把数据放到项目外，也可以填写当前操作系统支持的绝对路径。

迁移到其他电脑或服务器时，保留 `backend/.env` 和 `backend/data/` 即可带走配置与数据。

## 当前实现

- 使用 Node 22 内置 `node:sqlite`
- 启动时自动建表
- 启动时自动写入默认管理员账号、OpenAI 供应商、默认分组、默认全局设置和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据
- 使用记录按每次上游尝试写入；失败记录保存 `request_snapshot_json` / `response_snapshot_json`，用于前端查看请求与返回日志
- AI 账户使用授权和分组使用授权使用独立表记录，不复制账户凭据；授权资源调用时使用记录按实际调用方隔离，同时冗余资源所有者和授权关系用于聚合统计。
- `accounts.account_expires_at` 保存可选的本地套餐/账号购买到期时间；为空表示不过期，到期后账户自动改为停用并退出调度。
- 登录验证码挑战暂不写入 SQLite，使用后端进程内存保存短时一次性验证码；过期和已提交的挑战会被清理。
- 登录失败限频和账号临时锁定暂不写入 SQLite，使用后端进程内存保存短时窗口和锁定状态；后续多实例部署时再迁移到共享存储。

## 统计缓存与监控存储

个人或几个人使用时，统计缓存也优先放在 SQLite 内，不额外引入 Redis、ClickHouse 或 Prometheus。`usage_records` 仍是事实源，但账户列表、分组列表、管理员统计概览和监控图都应读取缓存表。缓存表属于业务数据缓存，必须按 `system_account_id` 隔离；统计概览和主机级系统监控属于管理员视角，默认只给管理员看。

建议新增表：

- `usage_stats_totals`：按 `system_account_id + scope_type + scope_id` 保存累计请求、成功、错误、token、成本、平均响应所需的求和字段。
- `usage_stats_daily`：按 `system_account_id + scope_type + scope_id + stat_date` 保存业务统计，用于今日请求、今日 Token、今日成本和最近天级趋势。
- `usage_stats_hourly`：按 `system_account_id + scope_type + scope_id + stat_hour` 保存业务统计，用于近 24 小时和近 7 天趋势图。
- `usage_model_daily`：按 `system_account_id + stat_date + model` 保存请求数、Token 和成本，用于模型分布。
- `usage_error_daily`：按 `system_account_id + stat_date + error_group + error_code` 保存错误数量，用于错误情况。
- `system_metrics_samples`：按采样时间保存 CPU、内存、RSS、Heap、事件循环延迟、网络入站/出站吞吐、网卡累计收发、数据库文件大小和统计滞后。
- `system_metrics_hourly`：把采样数据按小时聚合为平均值、最大值和最小值；网络吞吐平均值按有效网络速率样本数计算，避免采样端暂不可用时被按 0 稀释。
- `stats_job_state`：记录后台任务的作用域、游标、上次成功时间、上次错误和滞后秒数；业务统计作用域为 `system_account`，主机监控作用域为 `global`。

建议索引：

- `usage_records(system_account_id, created_at, id)`：统计 worker 增量扫描。
- `usage_records(account_owner_system_account_id, account_id, created_at, id)`：账户所有者查看真实账户统一用量。
- `usage_records(account_owner_system_account_id, account_authorization_id, created_at, id)`：授权管理弹窗按授权关系聚合用量。
- `usage_records(group_owner_system_account_id, group_id, created_at, id)`：分组所有者查看真实分组统一用量。
- `usage_records(group_owner_system_account_id, group_authorization_id, created_at, id)`：分组授权管理弹窗按授权关系聚合用量。
- `account_authorizations(owner_system_account_id, account_id, status)`：账户所有者查看授权名单。
- `account_authorizations(grantee_system_account_id, status)`：被授权用户查询可用授权账户。
- `account_authorizations(account_id, grantee_system_account_id, status)`：防止同一账户重复有效授权给同一用户。
- `group_authorizations(owner_system_account_id, group_id, status)`：分组所有者查看授权名单。
- `group_authorizations(grantee_system_account_id, status)`：被授权用户查询可用授权分组。
- `group_authorizations(group_id, grantee_system_account_id, status)`：防止同一分组重复有效授权给同一用户。
- `usage_records(system_account_id, first_token_ms, created_at, id)`、`usage_records(system_account_id, duration_ms, created_at, id)`、`usage_records(system_account_id, cost_usd, created_at, id)`：使用记录页按首 token、总耗时、成本排序时只取有限窗口，避免大数据量下前端全量排序或数据库临时排序。
- `usage_records(first_token_ms, created_at, id)`、`usage_records(duration_ms, created_at, id)`、`usage_records(cost_usd, created_at, id)`：管理员查看全部系统账户时的全局排序索引。
- `usage_stats_totals(system_account_id, scope_type, scope_id)`：列表读取累计值。
- `usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)`：今日和天级趋势读取。
- `usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)`：小时趋势读取。
- `usage_model_daily(system_account_id, stat_date, model)`：模型分布读取。
- `usage_error_daily(system_account_id, stat_date, error_group, error_code)`：错误分布读取。
- `stats_job_state(scope_type, scope_id, job_name)`：后台任务游标读取。

默认任务策略：

- 统计 worker 每 1 分钟按 `system_account_id` 和 `(created_at, id)` 游标增量读取 `usage_records` 并 upsert 到聚合表。
- 授权账户调用需要同时写入调用方统计和真实账户统计：调用方列表、分组、API Key 和日志按 `system_account_id` 聚合；账户所有者的账户总用量按 `account_owner_system_account_id + account_id` 聚合；账户授权弹窗按 `account_owner_system_account_id + account_authorization_id` 聚合。
- 授权分组调用需要同时写入调用方统计和真实分组统计：调用方 API Key 和日志按 `system_account_id` 聚合；分组所有者的分组总用量按 `group_owner_system_account_id + group_id` 聚合；分组授权弹窗按 `group_owner_system_account_id + group_authorization_id` 聚合。
- 管理员全局汇总从各系统账户缓存聚合，不回扫 `usage_records`。
- 系统采样 worker 每 10 到 30 秒写入一次 `system_metrics_samples`，不写用户级业务归属。
- 账户、分组、API Key 等列表接口只读 `usage_stats_totals` / `usage_stats_daily`，不要在列表查询里 `SUM usage_records`。
- 概览图表接口优先读 `usage_stats_hourly`、`usage_model_daily`、`usage_error_daily` 和 `system_metrics_hourly`。

默认保留策略：

- `usage_stats_hourly` 保留 14 天。
- `usage_stats_daily`、`usage_model_daily`、`usage_error_daily` 保留 180 天。
- `usage_stats_totals` 长期保留。
- `system_metrics_samples` 保留 7 到 14 天，`system_metrics_hourly` 可保留 180 天。
- 如果统计缓存损坏，可以按系统账户从 `usage_records` 重新构建缓存，不需要删除原始使用记录。

## AI 账户使用授权存储

账户授权只保存使用权关系，不保存或复制上游凭据。建议新增 `account_authorizations` 表：

- `id`
- `account_id`
- `owner_system_account_id`
- `grantee_system_account_id`
- `scope`：第一阶段固定为 `use`
- `status`：`active`、`revoked`
- `remark`
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `updated_at`

约束：

- `account_id` 必须指向真实存在的 AI 账户。
- `owner_system_account_id` 必须等于该账户的 `accounts.system_account_id`。
- `grantee_system_account_id` 不能等于 `owner_system_account_id`。
- 同一个 `account_id + grantee_system_account_id` 同一时间只能存在一条 `active` 授权；SQLite 可用部分唯一索引或 service 层事务校验实现。
- 收回授权只把 `status` 改为 `revoked` 并写入 `revoked_by` / `revoked_at`，不物理删除，历史统计继续可查。

`usage_records` 需要额外冗余这些字段：

- `account_owner_system_account_id`：命中账户所有者，自有账户时等于 `system_account_id`。
- `account_access_type`：`owner`、`account_authorized`、`group_authorized`。
- `account_authorization_id`：命中账户授权时写入，自有账户或仅通过分组授权访问时为空。

日志隔离和统计口径：

- 使用记录页按 `usage_records.system_account_id` 查询，所以被授权用户只看自己的调用明细。
- 账户所有者不按明细读取被授权用户的 `usage_records`，授权管理弹窗只读取按 `account_authorization_id` 聚合后的请求数、Token、成本、成功失败和最后使用时间。
- 账户真实总用量按 `account_owner_system_account_id + account_id` 聚合，所以自用和被授权用户使用都会消耗同一个账户额度。

## 分组使用授权存储

分组授权只保存分组使用权关系，不保存或复制分组内账户凭据。该能力排在账户使用授权之后实现。建议新增 `group_authorizations` 表：

- `id`
- `group_id`
- `owner_system_account_id`
- `grantee_system_account_id`
- `scope`：第一阶段固定为 `use`
- `status`：`active`、`revoked`
- `remark`
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `updated_at`

约束：

- `group_id` 必须指向真实存在的分组。
- `owner_system_account_id` 必须等于该分组的 `groups.system_account_id`。
- `grantee_system_account_id` 不能等于 `owner_system_account_id`。
- 同一个 `group_id + grantee_system_account_id` 同一时间只能存在一条 `active` 授权；SQLite 可用部分唯一索引或 service 层事务校验实现。
- 收回授权只把 `status` 改为 `revoked` 并写入 `revoked_by` / `revoked_at`，不物理删除，历史统计继续可查。
- 被授权用户绑定该授权分组的 API Key 不必立即删除，但调度时必须校验授权仍有效；授权失效时该 API Key 应返回“分组授权已失效”类错误或提示用户改绑。

`usage_records` 需要额外冗余这些字段：

- `group_owner_system_account_id`：命中分组所有者，自有分组时等于 `system_account_id`。
- `group_access_type`：`owner` 或 `authorized`。
- `group_authorization_id`：命中授权分组时写入，自有分组为空。

日志隔离和统计口径：

- 使用记录页仍按 `usage_records.system_account_id` 查询，所以被授权用户只看自己的调用明细。
- 分组所有者不按明细读取被授权用户的 `usage_records`，分组授权管理弹窗只读取按 `group_authorization_id` 聚合后的请求数、Token、成本、成功失败、最后使用时间和命中账户数。
- 分组真实总用量按 `group_owner_system_account_id + group_id` 聚合，所以自用和被授权用户通过该分组使用都会累计到同一个分组维度。
- 分组授权共享的是动态分组集合，但第一阶段只共享分组所有者自有账户；如果分组里包含别人授权来的账户，调度时必须过滤，不能通过分组授权继续共享给第三方。
- 统计按请求实际命中的 `group_id` 和当时的 `group_authorization_id` 记录，历史不会因后续分组账户变化而重算。

## 账户套餐到期

`accounts.account_expires_at` 是本地账户套餐/购买到期时间，适用于 OpenAI OAuth 和 OpenAI API Key 账户。它和 OAuth 凭据里的 `expires_at` 分离：`credentials.expires_at` 来自 OpenAI token 的 `expires_in`，只表示 access token 过期时间。

- 字段为空表示不设置本地套餐到期。
- 创建或编辑账户时如果填入过去时间，账户立即保存为 `disabled` 且 `schedulable = 0`。
- 列表、调度和相关恢复入口会先处理已过期账户，过期后写入“账户套餐已过期，已自动停用”。
- 网关选号和后台 OAuth 额度快照刷新只处理未到期账户。

## OpenAI OAuth 额度快照刷新

OpenAI OAuth 的 `5h` / `7d` 额度进度是账号运行态快照，不属于本地 token / 成本统计。快照按 `system_account_id + account_id + kind` 存储，列表只读缓存。

刷新策略：

- OAuth 账户创建成功后，后端立即触发一次首次额度快照刷新；刷新失败不回滚账户，只记录失败状态并交给后台重试。
- 真实网关请求返回 Codex rate-limit 响应头时，直接被动更新 `account_usage_snapshots`。
- 账户测试如果拿到相同响应头，也可以作为副作用更新快照，但 UI 不把测试描述成“刷新用量”。
- 后台 `oauth_usage_snapshot_refresh` worker 统一扫描缺失、过期或接近恢复点的 OpenAI OAuth 账户并刷新快照。
- AI 账户管理页更多菜单不提供“刷新用量”按钮，快照缺失时显示“等待后台刷新”。

建议字段：

- `system_account_id`
- `account_id`
- `kind`：第一阶段固定为 `openai_codex`
- `source`：`gateway_response`、`account_test`、`background_probe`
- `snapshot_json`
- `refresh_status`：`fresh`、`pending`、`failed`、`rate_limited`
- `last_attempt_at`
- `last_success_at`
- `next_refresh_after`
- `last_error_message`
- `created_at` / `updated_at`

后台刷新规则：

- 每个系统账户内默认并发为 1，全局并发保持较小，并加随机抖动。
- 只处理 `provider_code = openai`、`type = oauth`、未停用、未到 `account_expires_at` 且仍可调度的账户。
- 快照未过期时不探测；账号处于限流冷却时，`next_refresh_after` 不早于 reset 时间。
- 探测前如果 access token 即将过期，先用 `refresh_token` 自动刷新授权。
- 探测失败保留旧快照，只更新刷新状态、错误摘要和退避后的 `next_refresh_after`。
- 收到 OAuth 429 时，优先用 header 计算 reset 时间，header 不足时再解析响应体 `resets_at` / `resets_in_seconds`，并把账号标记为 `rate_limited` 到该时间。

## 错误兜底策略

账户添加和编辑优先维护账号自己的 `credentials.error_handling_rules`。网关不会把未处理的上游 `4xx/5xx` 原样返回给客户端：如果当前账号的内嵌规则没有命中，当前账号会按 `defaultTemporaryUnschedulableMinutes` 进入临时不可调用，并切换到同分组内下一个可用账号重试；全部账号都不可用时，客户端只会收到“没有可用账号”的网关错误。

## 默认运行策略

全局设置默认写入：

- `appName = 聚合 AI`
- `appIcon = /brand-icon.svg`
- 全局设置只保存系统名称和系统图标路径，只有管理员可修改；登录页标题由系统名称派生，角标、副标题和首页样式按 [产品与品牌边界](../architecture/frontend/产品与品牌边界.md) 固定。

系统设置默认写入：

- `defaultTemporaryUnschedulableMinutes = 5`：未知异常、策略冷却和流熔断共用的临时不可调用时长。
- `temporaryUnschedulableRetryIntervalSeconds = 3`：进入临时不可调用前的默认短暂重试间隔。
- `temporaryUnschedulableRetryAttempts = 3`：进入临时不可调用前的默认短暂重试次数。
- `streamCircuitBreakerEnabled = true`：流熔断默认开启。
- `streamRequestTimeoutSeconds = 180`：流式请求首包前的请求熔断时间，超时后会切换账号重发。
- `streamIdleTimeoutSeconds = 30`、`streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：流式响应异常的轻量阈值。
- `statsAggregationIntervalSeconds = 60`：统计缓存默认增量汇总间隔。
- `systemMetricsSampleIntervalSeconds = 30`：系统监控默认采样间隔。
- `usageStatsHourlyRetentionDays = 14`、`usageStatsDailyRetentionDays = 180`、`systemMetricsRetentionDays = 14`：默认缓存保留时长。
- `oauthUsageSnapshotRefreshIntervalSeconds = 300`：OAuth 额度快照后台扫描间隔。
- `oauthUsageSnapshotTtlSeconds = 900`：OAuth 额度快照默认过期时间。
- `oauthUsageSnapshotRetryBackoffSeconds = 300`：OAuth 额度快照刷新失败后的默认退避。
- `oauthUsageSnapshotPerAccountConcurrency = 1`：每个系统账户内 OAuth 快照刷新默认并发。
- `statsLagWarningSeconds = 300`：统计任务滞后超过该值时在运维概览里提示。

旧库升级时会清理不再展示的 `defaultErrorPolicyId`、`streamFailureAction`、`streamAccountCooldownMinutes`、`overloadCooldownEnabled`、`overloadCooldownMinutes`，并通过一次性迁移把流熔断默认打开。

## 系统账户隔离补充

- `accounts`、`account_authorizations`、`groups`、`group_authorizations`、`group_accounts`、`api_keys`、`error_policies`、`usage_records`、`account_usage_snapshots`、`system_settings` 后续都会按 `system_account_id` 或明确的 owner/grantee 字段隔离。
- `usage_stats_totals`、`usage_stats_daily`、`usage_stats_hourly`、`usage_model_daily`、`usage_error_daily` 也必须按 `system_account_id` 隔离。
- `providers`、`proxy_profiles`、`global_settings`、`system_metrics_samples`、`system_metrics_hourly` 保持全局共享；`providers` 和 `proxy_profiles` 只允许管理员维护，主机级系统监控默认仅管理员可见。
- 管理员可以读取所有系统账户的数据；普通用户只读取自己的系统账户数据，以及其他用户主动授权给自己的 AI 账户和分组使用摘要。

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码

这是单人自用系统，自有账户接口会返回前端需要展示的完整密钥；授权账户和授权分组接口不能返回完整密钥，只能返回列表摘要和必要状态。数据库中仍尽量加密保存。

API Key 明文只在创建时返回一次。




