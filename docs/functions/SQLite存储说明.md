# SQLite 存储说明

## 为什么用 SQLite

当前项目只给个人使用，不需要复杂部署、水平扩展或极限并发。SQLite 足够稳定，文件备份也简单，更符合轻量项目定位。

## 默认位置

后端运行时按业务库、统计数据集域和统计结果库三个职责组织 SQLite，不再保留旧记录库运行时兼容入口。统计数据集域由数据集目录库和 usage shard 文件组成；未配置统计数据集目录库、统计结果库或 usage shard 路径时，会使用各自默认位置，不会回退到旧记录库：

```text
业务库：backend/data/juhe-ai.sqlite3
统计数据集目录库：backend/data/juhe-ai-dataset.sqlite3
统计结果库：backend/data/juhe-ai-stats.sqlite3
使用记录分片：backend/data/usage-shards/
```

如需调整位置，编辑项目内本地配置文件 `backend/.env`，不设置系统环境变量：

```dotenv
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_DATASET_DATABASE_PATH=./data/juhe-ai-dataset.sqlite3
JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3
JUHE_AI_USAGE_SHARD_ROOT=./data/dataset/usage
JUHE_AI_USAGE_SHARD_COUNT=16
```

相对路径按 `backend/` 目录解析。为了保持可移植部署，推荐使用 `./data/juhe-ai.sqlite3`、`./data/juhe-ai-dataset.sqlite3`、`./data/juhe-ai-stats.sqlite3` 和项目内 usage shard 目录这类项目内相对路径。三个 SQLite 文件路径必须互不相同，usage shard 根目录也必须与这些文件路径区分；如果确实要把数据放到项目外，也可以填写当前操作系统支持的绝对路径。

迁移到其他电脑或服务器时，保留 `backend/.env` 和 `backend/data/` 即可带走配置与数据。

## 业务库、统计数据集域与统计结果库边界

业务库只保存可恢复的核心业务数据：

- `system_accounts`、`system_sessions`
- `global_settings`、`system_settings`
- `providers`、`proxy_profiles`、`error_policies`
- `accounts`、`account_supported_models`、`groups`、`group_accounts`、`api_keys`
- `system_teams`、`system_team_members`
- `resource_authorization_grants`、`resource_authorizations`、`resource_authorization_sources`
- `announcements`、`announcement_reads`

统计数据集域保存高增长、可丢失、可过期或排障类事实数据；其中数据集目录库保存未分片明细和元数据，新写入的 `usage_records` 保存到 usage shard 文件：

- `usage_records`（usage shard 文件）
- `audit_logs`、`audit_log_attempts`、`audit_payload_blobs`、`audit_payload_refs`、`audit_error_groups`
- `operation_logs`、`operation_log_targets`、`operation_log_viewers`
- `runtime_logs`、`runtime_log_file_cursors`
- `model_check_runs`、`model_check_items`
- `api_key_record_cleanup_targets`

统计结果库保存可重建、紧凑且面向查询的结果数据：

- `usage_stats_*`、`usage_model_*`、`usage_error_*`、`usage_latency_*`、`usage_rank_snapshots`、`stats_job_state`、`usage_record_cleanup_deductions`
- `authorization_*_usage_*`、`usage_quota_hourly_windows`、`usage_scope_range_windows`
- `group_account_stats`、`group_account_stats_dirty`、`account_quality_scores`、`account_quality_minute_stats`
- `account_usage_snapshots`
- `system_metrics_samples`、`system_metrics_hourly`、`system_metrics_trend_windows`
- `process_event_loop_samples`、`process_event_loop_hourly`、`process_event_loop_trend_windows`
- `database_storage_snapshots`、`table_storage_snapshots`

运行时代码不通过 `ATTACH` 跨库查询，也不在业务库里兼容读取旧记录表。业务库是迁移和恢复的硬边界，必须保留；统计数据集目录库、usage shard 和统计结果库都可以丢弃、清空或重建。旧整库需要拆分或清理时，只能使用停机后的显式脚本；不关心历史统计和排障明细时，可以直接新建空数据集目录库、usage shard 目录和统计结果库。

## 统计数据集与结果库拆分方案

当前实现已经落地统计数据集域和统计结果库入口。为减少高频数据集写入对统计结果查询的影响，旧记录库职责已经拆为：

- 统计数据集目录库：保存原始审计、操作日志、运行日志索引、模型检测明细、记录清理目标和 usage shard catalog 等高增长事实元数据；新写入的 `usage_records` 已拆到 usage shard 文件。
- 统计结果库：保存 `usage_stats_*`、`usage_model_*`、`usage_error_*`、`usage_latency_*`、额度窗口、范围窗口、排行快照、授权消耗窗口、账号质量结果、分组统计、系统监控采样、监控趋势、`stats_job_state` 和使用记录清理扣减账本等紧凑结果。

页面统计、列表用量和网关额度只读统计结果库；明细排障通过 repository 读取 usage shard 和数据集目录库中的其他明细；后台 worker 从 usage shard 按分片游标读取事实，并在统计结果库事务中写入结果和推进水位。旧 `juhe-ai-records.sqlite3` 不再作为运行时回退目标；如需保留旧历史明细，只能停服务后用一次性迁移脚本处理。

## 数据集库分片写入设计

三库拆分解决了业务库、数据集目录库和统计结果库之间互相拖慢的问题，但不能消除单个统计数据集库 SQLite 文件内部的单 writer 上限。当前高频写入优化以 [数据集库分片写入设计](数据集库分片写入设计.md) 为准，先只拆最热的 `usage_records`：

- `JUHE_AI_DATASET_DATABASE_PATH` 继续作为数据集目录库，保存审计、操作日志、运行日志索引、模型检测和 shard catalog。
- 新增 `JUHE_AI_USAGE_SHARD_ROOT`，默认跟随数据集库目录生成 `usage-shards`；生产也可以显式配置为 `./data/dataset/usage`。
- 新增 `JUHE_AI_USAGE_SHARD_COUNT`，默认 `16`。
- 新写入的 `usage_records` 按 `bucket_date + stable_hash(id) % shardCount` 路由到多个 SQLite shard 文件。
- 当前实现复用 background worker usage 队列，在 worker 内按 shard 分组并对每个 shard 执行短事务；独立 child writer pool 作为后续性能增强。
- 统计结果库仍不分片；统计 worker 改为按 shard 独立游标读取 usage，再在统计结果库同事务写结果和推进水位。
- 使用记录详情优先通过 usage id 中的日期和 shard 信息直接定位；显式旧格式 id 只做受控 shard 扫描，不再回写旧单表。
- 使用记录列表由 repository 内部跨 shard 有界读取并稳定合并，不做全 shard 精确 `COUNT(*)`。
- 数据保留优先按 shard 文件删除或归档旧数据，避免大表批量 `DELETE` 和 freelist 膨胀。

分片设计不改变外部 API 响应、统计结果口径、API Key 额度读取口径或统一授权额度口径。业务主库必须保护；统计数据集目录库、usage shard 和统计结果库可以按需要删除重建。

## 当前实现

- 使用 Node 内置 `node:sqlite`，要求官方 Node.js LTS；当前支持 22.x LTS（>=22.13.0）或 24.x LTS（>=24.11.0），且内置 `node:sqlite` 必须可用。
- 启动时自动建表
- 启动时自动写入默认管理员账号、OpenAI 供应商、默认分组、默认全局设置和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 每个 SQLite 连接必须设置短暂写锁等待时间，避免 DB service、background worker 和管理面低频写操作短事务重叠时立即返回 `database is locked`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据
- 系统管理 API、登录态校验、管理面 CRUD、客户请求链路中的高频 SQLite 读写、公开设置读取、运行日志索引查询、账号错误状态副作用、OAuth Access Token 刷新持久化和 OAuth Codex 额度快照写入，都通过独立本地 DB service 进程完成；主 Web 进程只代理 `/__aisys__/api/*`，不解析管理 API JSON body，不直接导入管理路由或 repository。DB service 不改变 SQLite 单写者模型，DB service 不可用时请求返回可读错误，不能回退到主 Web 进程本地同步执行。
- 业务库通过 `JUHE_AI_DATABASE_PATH` 打开；统计数据集库通过 `JUHE_AI_DATASET_DATABASE_PATH` 打开；统计结果库通过 `JUHE_AI_STATS_DATABASE_PATH` 打开。三类入口都使用 WAL，并且三个路径必须互不相同。
- 使用记录按每次上游尝试写入 usage shard；server 角色只把使用记录投递给 background worker IPC 队列，不在 worker 未就绪时回落到主进程本地队列或同步写库。失败记录保存 `request_snapshot_json` / `response_snapshot_json`，用于前端查看请求与返回日志
- 操作日志使用独立表保存已成功提交的业务状态变更，用于追溯系统账户对资源的增删改、启停、绑定、授权和配置变更；查询请求不写操作日志。
- 管理端写操作需要按 [幂等与唯一约束设计](幂等与唯一约束设计.md) 接入防重复提交和业务唯一约束：前端重复点击或网络重试不应创建多条业务数据，重复提交拦截不写第二条操作日志。
- 原始审计日志使用独立表保存完全成功请求的 10% 稳定样本，以及失败、异常、客户端中断、流式中断和重试后成功链路；请求 / 响应正文按 [审计日志保全策略设计](审计日志保全策略设计.md) 压缩、去重并通过 payload 引用保存，server 角色只能终态投递 background worker IPC 队列，后台批量写库，不能同步写审计表，也不能在 worker 未就绪时本地落库。
- 普通运行日志仍以 JSON Lines 写入日志文件并滚动清理；最近 3 天的索引查询只使用数据集库表 `runtime_logs`，后台 worker 通过 `runtime_log_file_cursors` 记录当前日志文件读取游标，只追新增内容，不在启动时全量扫描当前日志文件；管理后台索引查询和 facets 读取经 DB service 完成，不在主进程同步读取 SQLite 索引。运行日志不再维护额外搜索影子表，关键字只在 `runtime_logs.message` 列做普通模糊匹配，完整日志正文搜索交给 `grep 模式`。
- 系统团队、团队成员和统一资源授权使用独立表记录；授权不复制账户凭据，授权资源调用时使用记录按实际调用方隔离，同时冗余资源所有者、授权关系和授权对象用于聚合统计。
- `accounts.system_account_id` 和 `groups.system_account_id` 表示资源归属人；`group_accounts.system_account_id` 表示本地分组绑定所属的使用方系统账户。授权账户绑定到被授权用户分组时写入同一个物理 `account_id` 和稳定的 `account_authorization_id`，只改变该使用方本地绑定与本地调度状态，不改变账户资源归属或账户所有者的分组绑定。
- `accounts.account_expires_at` 保存可选的本地套餐/账号购买到期时间；为空表示不过期，到期后账户自动改为停用并退出调度。
- `accounts.last_error_code` 保存账户异常子类型；顶层状态仍统一使用 `status = error` 表示“异常”，可读细节继续放在 `accounts.last_error_message`。
- `accounts.cooldown_retest_failure_count`、`cooldown_retest_observation_started_at`、`cooldown_retest_last_at` 和 `cooldown_retest_last_status_code` 保存 `temporary_unavailable` 后台复测的连续失败次数、本轮自动恢复观察起点、最近复测时间和最近 HTTP 状态；复测成功、手动恢复、停用或到期时清空。进入慢速恢复后，如果复测失败发生在最长自动恢复观察之后，账户转为 `status = error` 并保留最后错误摘要。
- `account_supported_models` 保存账号显式支持的模型列表；账号没有任何模型行表示不限制。网关账号池缓存 miss 时按账号 ID 批量读取这些行，并把结果放入运行时账号快照，正常请求只做内存过滤，不逐次查询该表。
- 高并发分组已使用 `groups.group_type` 和 `groups.scheduling_policy_json`：前者保存分组调度类型，默认 `personal`；后者接收最大单账户排队阈值和分组最大等待时间，快速优先、慢请求分流、亲和打破、备用启用和分组级短等待容量都使用代码内置默认开启策略。默认分组和历史分组保持个人分组语义。
- 高并发分组的单账户排队阈值只来自 `groups.scheduling_policy_json` 里的分组目标配置；账户绑定关系不再保存绑定级权重或绑定级单账户排队阈值。实际调度阈值仍不能突破账号 `concurrency_limit` 硬上限。
- 高并发分组需要的近期质量、超时率、首 token 和总耗时趋势应由 background worker 或请求终态事件增量写入紧凑缓存，网关热路径只读取 DB service 已批量带出的运行时快照，不实时回扫 `usage_records` 或 `account_quality_minute_stats`。跨进程共享指标必须落到 SQLite 缓存表或明确 IPC，不能只放在 worker 进程内存。
- 登录验证码挑战暂不写入 SQLite，使用后端进程内存保存短时一次性验证码；过期和已提交的挑战会被清理。
- 登录失败限频和账号临时锁定暂不写入 SQLite，使用后端进程内存保存短时窗口和锁定状态；后续多实例部署时再迁移到共享存储。
- 会话亲和、当前并发占用、短 TTL 网关校验缓存、验证码挑战、登录失败窗口和 IPC 待投递队列都属于单节点易失运行态；SQLite 只承载长期事实、业务状态、数据集明细和预聚合缓存。服务或子进程重启后这些易失状态允许丢失，并从 SQLite 事实和预聚合结果重新恢复保守判断。

## 列表补数与缓存策略

后端列表接口不能因为 DTO 需要展示补充字段就拖慢首屏。凡是主列表当前页一次查询拿不齐、需要按 ID 反查其他业务表补充的数据，都必须先判断数据变化频率，再选择缓存策略。

低频业务数据走正向按需缓存：

- 适用对象：系统账户、AI 账户、分组、API Key、系统团队、展示名称、资源归属、低频绑定关系、授权来源摘要等不频繁变化的数据。
- 读取方式：repository / DAO lookup 层接收当前页 ID，先查内存 LRU 缓存；缓存 miss 时按 ID 分批查询 SQLite，再写入缓存。
- 写入方式：创建、更新、删除、绑定、授权、团队成员变化等写路径必须主动失效对应 ID 或对应缓存域，不能只等 TTL。
- 禁止模式：列表 hydration 阶段逐行查库、拉全量名称表、拉全量关系表、在前端再次请求一组名称或关系来补页面。

高频业务数据走反向主动缓存：

- 适用对象：当前并发、账号质量分、运行时可用性、额度快照、网关校验运行态、频繁变化的授权可用性、近期统计窗口等。
- 写入方式：事实变化发生时由网关副作用、写接口或后台 worker 主动把最新结果写入内存缓存或预聚合表；列表读取时先读缓存或窗口表。
- 刷新节奏：高频缓存有效期必须跟对应后台定时统计或刷新间隔一致，不能单独拍一个 TTL。用量统计默认跟随 `statsAggregationIntervalSeconds = 60`，分组账户状态默认跟随 `groupAccountStatsRefreshIntervalSeconds = 60`，账号质量默认跟随 `accountQualityRefreshIntervalSeconds = 600`；后台任务刷新统计时，应同时补齐 API 列表需要的缓存行、窗口行或轻量快照。
- 进程边界：worker 定时刷新如果只写 worker 自己的内存缓存，API server 和 DB service 不会自动拿到。需要给前端列表读取的高频缓存，优先写入紧凑 SQLite 缓存 / 预聚合 / 窗口表；确需内存预热时必须明确由 API 进程预热或通过 IPC 同步，不能假设跨进程内存共享。
- 兜底方式：缓存 miss 时可以按主键读 SQLite 或读取最近预聚合行，并在读取后回填缓存；不能在请求路径扫描明细、现场 `SUM/GROUP BY` 或逐条补齐。
- 用户告知：如果某类高频数据无法可靠主动缓存，必须在需求或实现说明中告知用户存在实时查询成本或统计滞后，并优先改为后台预聚合、窗口表或事件驱动缓存。

敏感和大体积数据不得进入通用 lookup 缓存：API Key 明文、OAuth token、代理密码、完整请求 / 响应 payload、审计正文、日志大字段和可能造成越权的权限判定中间结果，必须走专门的受控读取、脱敏和分块策略。

当前轻量缓存优先使用 `backend/src/shared/cache.ts` 的进程内 LRU 封装；多实例或跨进程一致性需求出现前，不引入 Redis 等分布式依赖。缓存只用于降低请求链路反查成本，事实源仍是 SQLite 表、统计结果库预聚合表或网关运行态事实。

## 大文件与频繁读取底线

统计数据集库会持有日志索引、审计 payload、使用记录和原始监控采样等持续增长数据。任何运行路径只要涉及大文件或高频文件读取，都必须按 offset / cursor / stream / 分块窗口推进，不能先全量读入内存再 `split`、过滤、排序或分页。

- 运行日志索引通过 `runtime_log_file_cursors` 保存文件 offset、行号和文件标识；worker 重启后从游标继续，首次遇到已有当前日志文件时默认从文件末尾开始，避免导入历史大文件。
- 按行读取只在完整换行结束后推进 offset；末尾半行保留到下一轮，轮转、截断或文件标识变化时重置游标。
- 审计 payload blob 详情接口只能按 offset / limit 返回有限窗口；未压缩 blob 使用文件 offset 读取，gzip blob 通过解压流跳过到逻辑 offset 后只收集当前窗口，接口返回 `bodyOffset`、`bodyLimit`、`bodyTotalBytes`、`bodyNextOffset` 和 `bodyTruncated`。超过单次最大读取窗口的 payload 默认保持未压缩，避免读取后半段时反复从头解压 gzip。
- 使用记录、操作日志、原始审计日志、审计错误组和运行日志索引这类高增长列表，默认只读取当前页 `pageSize + 1` 条来判断 `hasMore`，不在请求路径执行全量 `COUNT(*)`；返回的 `total` 只是前端分页器上界值，不代表精确全表总数。
- 小 `.env` 配置、极小系统状态文件、测试 / 回归脚本、明确有大小上限的网关 raw body 或诊断响应捕获可以作为例外；`/v1` raw body 可接收 `64mb`，JSON 请求体继续正常解析以保留 `model`、`stream`、会话粘滞、统计和审计字段，`2mb` 只是大 JSON 预警阈值，不作为拒绝阈值，超过后保持客户端连接并进入 worker thread 异步解析，解析完成再继续调度和转发；使用记录请求快照只保存体积摘要，不能为了快照把完整大请求体再写入明细表，完整原始内容由原始审计按策略捕获；例外不得用于运行日志、审计 payload、使用记录导出或统计明细。

## 统计缓存与监控存储

个人或几个人使用时，统计缓存优先放在统计结果库内，不额外引入 Redis、ClickHouse 或 Prometheus。`usage_records` 仍是统计数据集域事实源，当前新记录保存到 usage shard 文件；账户列表、分组列表、用户统计概览、管理员统计概览和监控图都应读取统计结果库缓存表。缓存表必须按 `system_account_id` 隔离；用户统计概览只读取当前用户缓存，管理员统计概览默认读取全局缓存并支持筛选指定系统账户，主机级系统监控只给管理员看。

统计底线：所有业务汇总都只能由后台 worker 或离线重建脚本按游标增量计算；管理 API、前端页面、详情接口和下拉接口只能直读已经预聚合好的 staged / window / summary 行。请求路径不得为了展示临时 `SUM/GROUP BY`，也不得把明细行或缓存桶再相加成页面汇总。统计缓存是可丢弃数据，表结构不匹配时优先重建和改造，不能牺牲请求性能兼容旧口径。

当前统计相关表：

- `usage_stats_totals`：按 `system_account_id + scope_type + scope_id` 保存累计请求、成功、错误、token、缓存成本、总成本、平均总耗时所需的求和字段。
- `usage_stats_daily`：按 `system_account_id + scope_type + scope_id + stat_date` 保存业务统计，用于个人账户按日消耗趋势、账户 / 分组 / API Key 今日口径和授权额度自然日口径；管理侧授权团队 / 用户明细不读此表做报表汇总。
- `usage_stats_hourly`：按 `system_account_id + scope_type + scope_id + stat_hour` 保存业务统计，用于 worker 刷新统计概览、排行、额度和 AI 性能摘要窗口，也用于 `AI性能监控` 的账户级首 token 和总耗时小时趋势；页面摘要和额度不能在请求时聚合小时桶。
- `authorization_team_usage_summary_daily`：按 `system_account_id + stat_date + team_filter_id + resource_filter_type + resource_filter_id` 保存授权团队日摘要行，由 worker 随使用记录游标增量写入。
- `authorization_team_usage_range_windows`：按 `system_account_id + start_date + end_date + team_filter_id + resource_filter_type + resource_filter_id` 保存团队消耗页最近 31 天内任意日范围窗口，接口按日期范围直读。
- `authorization_user_usage_summary_daily`：按 `system_account_id + stat_date + team_filter_id + grantee_filter_system_account_id + resource_filter_type + resource_filter_id` 保存授权用户日摘要行，由 worker 随使用记录游标增量写入。
- `authorization_user_usage_range_windows`：按 `system_account_id + start_date + end_date + team_filter_id + grantee_filter_system_account_id + resource_filter_type + resource_filter_id` 保存用户消耗页最近 31 天内任意日范围窗口，接口按日期范围直读。
- `usage_overview_summary_windows`：按 `system_account_id + window_key + start_date + end_date` 保存统计概览范围摘要，接口不再按小时桶临时汇总。
- `usage_overview_trend_windows`：按 `system_account_id + window_key + start_date + end_date + bucket_key` 保存统计概览趋势点，范围分桶在 worker 内完成。
- `usage_model_rank_windows`：按 `system_account_id + window_key + start_date + end_date + rank` 保存统计概览模型 TopN，接口只按范围窗口读取排名行。
- `usage_error_rank_windows`：按 `system_account_id + window_key + start_date + end_date + rank` 保存统计概览错误 TopN，接口只按范围窗口读取排名行。
- `ai_performance_summary_windows`：按 `system_account_id + window_key + start_date + end_date` 保存 AI 性能监控摘要，前端账户筛选只影响图表显隐，不重新计算摘要。
- `usage_quota_hourly_windows`：按 `system_account_id + scope_type + scope_id + window_hours` 保存 n 小时额度成本，随 `statsAggregationIntervalSeconds` 刷新，网关额度判断不再 `SUM usage_stats_hourly`。
- `usage_scope_range_windows`：按 `system_account_id + scope_type + scope_id + start_date + end_date` 保存最近 31 天范围内的范围总量，用量统计和授权详情只按范围 key 直读；账号用量页关键词先在业务库解析为账号 ID，再用 `scope_id` 命中窗口表，不能在统计结果窗口查询中拼业务字段多列 `LIKE`。
- `usage_model_daily`：按 `system_account_id + stat_date + model` 保存请求数、Token 和成本，用于自然日模型分布。
- `usage_model_hourly`：按 `system_account_id + stat_hour + model` 保存小时级模型分布，用于统计概览监控窗口。
- `usage_error_daily`：按 `system_account_id + stat_date + error_group + error_code` 保存错误数量，用于自然日错误情况。
- `usage_error_hourly`：按 `system_account_id + stat_hour + error_group + error_code` 保存小时级错误数量，用于统计概览监控窗口。
- `group_account_stats`：按 `system_account_id + group_id` 保存分组绑定账户数量、可用数、状态数量和并发上限，供分组列表直接读取；写路径只标记脏分组，worker 随 `groupAccountStatsRefreshIntervalSeconds` 刷新脏分组，必要时才全量重建。
- `group_account_stats_dirty`：按 `group_id` 记录待刷新的分组统计缓存，来源包括账户状态 / 调度属性变化、分组绑定变化、团队和授权关系变化；请求链路不得同步重建 `group_account_stats`。影响面无法精确收敛时只能写入 `__all__` 全量哨兵，由 worker 消费后全量刷新，禁止在写请求中先 `SELECT id FROM groups` 再逐分组展开。
- `system_metrics_samples`：按采样时间保存 CPU、内存、RSS、Heap、网络入站/出站吞吐、网卡累计收发、数据库文件大小和统计滞后；`event_loop_lag_ms` 继续保存后台 worker 采样值，作为旧接口和主机采样兼容字段，不再作为多进程事件循环趋势的唯一来源。
- `system_metrics_hourly`：把采样数据按小时聚合为平均值、最大值和最小值；网络吞吐平均值按有效网络速率样本数计算，避免采样端暂不可用时被按 0 稀释。
- `system_metrics_trend_windows`：按统计概览日期范围预生成系统性能 / 网络吞吐趋势，接口只按范围窗口直读。
- `process_event_loop_samples`：按采样时间和进程角色保存事件循环额外延迟，当前角色为 `server`、`worker`、`db-service`，用于区分主 Web 进程、后台 worker 和本地 DB service 哪个进程卡顿。
- `process_event_loop_hourly`：按 `stat_hour + process_role` 汇总事件循环延迟样本数、平均值和最大值，保留为长期粗粒度排障和兼容缓存。
- `process_event_loop_trend_windows`：保留旧统计概览范围窗口缓存；管理侧事件循环趋势展示不再按日期范围读这个窗口，改为固定读取最近 24 小时 `process_event_loop_samples` 并按分钟聚合，避免长范围窗口膨胀，也避免把卡顿尖峰压成天级趋势。
- `database_storage_snapshots`：按采样时间保存业务库、统计数据集库和统计结果库文件大小、WAL / SHM、页大小、总页数、空闲页和表数量；这是 10 分钟常规采样的主指标。
- `table_storage_snapshots`：按采样时间保存表级可选行数、表大小、索引大小、总大小和 1 小时 / 24 小时增长；表级数据按游标轮转分批刷新，不要求所有表在同一采样时间都有新快照。后台常规采样通过 `dbstat` 叶子页 cell 数滚动写入可推导的行数，不提供精确 `COUNT(*)` 采样分支；SQLite `dbstat` 不可用或表类型不适合推导时，表大小、索引大小、总大小、页数和行数保持为空，不写入伪造的 0。
- 表监控文件级采样由后台 worker 每 10 分钟执行一次；表级采样默认每轮每个库最多刷新 4 张表，历史默认保留最近一月。
- `stats_job_state`：记录后台任务的作用域、游标、上次成功时间、上次错误和滞后秒数；业务统计作用域为 `system_account`，主机监控作用域为 `global`。任务尚未写入状态或滞后无法判断时，`lag_seconds` 保持为空，不按 0 处理。
- `usage_record_cleanup_deductions`：API Key 历史记录物理清理的统计扣减账本，按 `usage_id + source_shard_key` 记录已扣减但可能尚未完成 shard 删除的使用记录；统计扣减和账本标记在同一个统计结果库事务内提交，用于跨 SQLite 文件清理失败后的幂等续跑。
- `operation_logs`：保存业务操作主事件，包括操作人、业务作用域、模块、动作、主资源、安全差异和 trace ID；`keyword` 直接对摘要、资源名、资源 ID、操作人、操作键、模块和动作等主表字段做普通 SQL 模糊匹配，不维护额外搜索影子表。
- `operation_log_targets`：保存一次操作涉及或影响的资源，支持按资源反查历史操作。
- `operation_log_viewers`：保存普通用户可见性和可见原因，资源删除或授权撤销后仍按当时关系追溯。
- `audit_logs`：保存进入原始审计的客户端请求事件元数据，用于后台页面检索；完全成功请求默认只采样 10%。
- `audit_log_attempts`：保存审计请求下每次上游尝试、命中账号、代理、状态码和错误摘要。
- `audit_payload_refs`：保存审计事件到 headers/body blob 的引用、part 类型、顺序、hash、大小和保留状态。
- `audit_payload_blobs`：保存客户端请求、上游请求、上游响应和最终网关响应 blob 元数据；可压缩且不超过单次读取窗口的正文会 gzip，超大正文保持 plain 以支持直接 offset 读取；blob 文件落在本地数据目录。
- `audit_error_groups`：保存短时间窗口内重复错误的聚合信息，列表默认可按错误组查看。
- `runtime_logs`：保存最近 3 天普通运行日志的结构化字段和原始 JSON 行，用于管理员“日志搜索”页面。索引查询中的 `keyword` 只对 `message` 列做普通 SQL 模糊匹配，不检索 `raw_json`，也不维护额外搜索影子表。
- `runtime_log_file_cursors`：保存当前普通运行日志文件的读取 offset、行号、文件标识和最近读取状态，worker 重启后从游标继续追增量日志，文件轮转或截断时重置对应文件游标。
- `account_quality_scores`：按 `account_id` 保存账号质量缓存，包括质量分、质量状态、近 10 分钟真实网关首 token 聚合、成功率和最近真实样本时间。该表只作为调度辅助缓存，事实源仍由 `usage_records` 通过统计 worker 增量进入 `account_quality_minute_stats`；账号质量主动探测能力已删除，不保留重新开启的旧设置。超过 24 小时没有真实样本的质量分不参与调度。
- `account_quality_minute_stats`：按 `account_id + stat_minute` 保存真实网关请求的分钟级质量聚合，由主用量统计 worker 随 `(created_at, id)` 游标递增写入；服务重启后保留，重建时跟随主用量统计游标重新生成。
- `announcements`：保存平台公告，用户侧只读取最近 30 条已发布公告，管理员侧维护草稿、已发布和已下线公告。
- `announcement_reads`：按 `announcement_id + system_account_id` 保存用户已读公告记录，支撑铃铛未读提醒跨刷新、跨浏览器保持一致。

当前索引重点：

- `system_accounts(lower(username))`：保证用户账户大小写不敏感唯一，用户账户创建后不允许修改。
- `system_accounts(lower(display_name))`：保证用户名称大小写不敏感唯一。
- `system_accounts(display_name, id)`：系统账户管理列表只按用户名称精确 / 前缀匹配。
- `system_accounts(username, id)`：保留给系统账户选项、授权候选用户 options 和 AI 性能账号选项解析 owner ID；系统账户管理列表不按用户名或 ID 搜索。
- `accounts(system_account_id, provider_code, lower(name))`：保证同一用户同一供应商下 AI 账户名称唯一，凭据唯一仍由 `credential_fingerprint` 兜底。
- `accounts(name, id)`、`accounts(system_account_id, name, id)`：AI 账户列表、账户选项和 AI 性能账号选项按账号名称精确 / 前缀定位。
- `accounts(provider_code, id)`、`accounts(system_account_id, provider_code, id)`、`accounts(type, id)`、`accounts(system_account_id, type, id)`：保留给供应商、账户类型筛选或排序使用；AI 账户通用搜索不再匹配这些字段。
- `groups(system_account_id, provider_code, lower(name))`：保证同一用户同一供应商下分组名称唯一。
- `groups(name, id)`、`groups(system_account_id, name, id)`：账户绑定分组和分组选项按分组名前缀定位。
- 当前未为 `group_type` 单独增加索引；分组列表和网关候选快照仍复用所有者、供应商和名称相关索引读取，只有出现明确查询瓶颈时再补 `groups(system_account_id, provider_code, group_type, id)` 这类定向索引。
- `groups(system_account_id, provider_code) WHERE is_default = 1`：保证同一用户同一供应商只有一个默认分组。
- `system_teams(name, id)`：授权团队列表只按团队名称精确 / 前缀匹配，不搜索团队 ID 或说明。
- `api_keys(system_account_id, lower(name))`：保证同一用户下 API Key 名称唯一，密钥本身仍由 `key_hash` 兜底。
- `api_keys(name, id)`、`api_keys(system_account_id, name, id)`：API Key 列表关键词只按名称精确 / 前缀匹配，不搜索 Key 前缀或说明，也不做无边界包含匹配。
- API Key 列表不维护 Key 前缀、说明或多字段组合搜索索引；`key_prefix` 只作为摘要展示字段保留，不参与列表关键词查询。
- `proxy_profiles(lower(name))`：保证代理配置名称全局唯一。
- `proxy_profiles(name, id)`：代理管理列表和代理选项只按代理名称精确 / 前缀匹配；不维护代理地址、类型、用户名或说明的组合搜索索引。
- 业务唯一索引直接落到当前 schema；若本地旧库已有重复记录，应先离线清理旧数据或重建库，不在启动路径保留跳过索引的兼容逻辑。
- `usage_records(system_account_id, created_at, id)`：统计 worker 增量扫描。
- `usage_records(traffic_source, created_at, id)`：按真实网关请求、手动账号测试和恢复探活区分明细；后台复测只用 `traffic_source = gateway` 的记录学习真实请求形态。
- `usage_records(account_owner_system_account_id, account_id, created_at, id)`：账户所有者查看真实账户统一用量。
- `usage_records(group_owner_system_account_id, group_id, created_at, id)`：分组所有者查看真实分组统一用量。
- `usage_records(group_id, created_at, api_key_id)`：账号质量 worker 判断分组近 24 小时是否有真实网关请求，账号测试和后台探测不计入客户活跃。
- `usage_records(group_id, created_at, id)`、`usage_records(system_account_id, group_id, created_at, id)`：使用记录页按分组筛选并按时间分页。
- `usage_records(account_owner_system_account_id, account_authorization_id, created_at, id)`：统计 worker 增量写账户授权缓存、授权日摘要和范围窗口表。
- `usage_records(group_owner_system_account_id, group_authorization_id, created_at, id)`：统计 worker 增量写分组授权缓存、授权日摘要和范围窗口表。
- `system_teams(name)`：保证团队名称唯一。
- `system_teams(name, id)`：授权候选团队 options 按团队 ID 和名称做精确 / 前缀匹配。
- `system_teams(status, name)`：团队列表和授权对象选择器读取。
- `system_team_members(team_id, system_account_id, status)`：校验团队成员有效性。
- `system_team_members(system_account_id, status)`：查询当前系统账户所属团队。
- `resource_authorizations(resource_owner_system_account_id, resource_type, resource_id, status)`：资源所有者查看授权名单。
- `resource_authorizations(grantee_system_account_id, status)`：查询系统账户可用授权。
- `resource_authorizations(resource_type, resource_id, grantee_system_account_id)`：保证同一资源同一最终用户全生命周期只有一条运行时授权记录。
- `resource_authorization_grants(resource_owner_system_account_id, status)`：资源归属人查看授权操作。
- `resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = active AND grantee_type = system_account`：防止同一资源重复授权给同一用户。
- `resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = active AND grantee_type = team`：防止同一资源重复授权给同一团队。
- `resource_authorization_sources(authorization_id, source_type) WHERE status = active AND source_type = manual`、`resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = active AND source_type = team`：防止同一最终授权存在重复有效来源。
- 有效资源查询需要在 service 层按 `resource_type + resource_id + caller_system_account_id` 去重；团队来源只能合并到用户授权来源摘要，不能展开成多条 AI 账户或分组。
- `usage_records(system_account_id, first_token_ms, created_at, id)`、`usage_records(system_account_id, duration_ms, created_at, id)`、`usage_records(system_account_id, cost_usd, created_at, id)`：使用记录页按首 token、总耗时、成本排序时只取有限窗口，避免大数据量下前端全量排序或数据库临时排序。
- `usage_records(first_token_ms, created_at, id)`、`usage_records(duration_ms, created_at, id)`、`usage_records(cost_usd, created_at, id)`：管理员查看全部系统账户时的全局排序索引。
- `accounts(fallback_enabled, super_priority_enabled, status, priority)`：账号调度和列表排序读取降级备用、超级优先与优先级。
- `account_quality_scores(provider_code, quality_score, quality_state)`：账号质量缓存排序和后台挑选候选。
- `account_quality_minute_stats(stat_minute, account_id)`：账号质量刷新读取近窗口分钟桶，不实时回扫 `usage_records`。
- `usage_stats_totals(system_account_id, scope_type, scope_id)`：列表读取累计值。
- `usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)`：今日和天级趋势读取。
- `usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)`：小时趋势读取。
- `usage_overview_summary_windows(system_account_id, window_key, start_date, end_date)`：统计概览摘要读取。
- `usage_overview_trend_windows(system_account_id, window_key, start_date, end_date, bucket_key)`：统计概览趋势读取。
- `usage_model_rank_windows(system_account_id, window_key, start_date, end_date, rank)`：模型 TopN 读取。
- `usage_error_rank_windows(system_account_id, window_key, start_date, end_date, rank)`：错误 TopN 读取。
- `ai_performance_summary_windows(system_account_id, window_key, start_date, end_date)`：AI 性能监控摘要读取。
- `usage_quota_hourly_windows(system_account_id, scope_type, scope_id, window_hours)`：n 小时额度读取。
- `usage_scope_range_windows(system_account_id, scope_type, scope_id, start_date, end_date)`：最近 31 天范围总量按具体 scope 读取。
- `usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, scope_id)`：账号用量列表按系统账户、作用域类型和日期范围分页读取。
- `system_metrics_trend_windows(window_key, start_date, end_date, bucket_key)`：系统性能 / 网络吞吐趋势读取。
- `process_event_loop_trend_windows(window_key, start_date, end_date, bucket_key, process_role)`：主进程、后台 worker 和 DB service 事件循环趋势读取。
- `usage_model_daily(system_account_id, stat_date, model)`：模型分布读取。
- `usage_error_daily(system_account_id, stat_date, error_group, error_code)`：错误分布读取。
- `usage_model_hourly(system_account_id, stat_hour, model)`：监控窗口模型分布读取。
- `usage_error_hourly(system_account_id, stat_hour, error_group, error_code)`：监控窗口错误分布读取。
- `group_account_stats(system_account_id, group_id)`：分组列表读取账户数量与状态统计。
- 系统账户选项接口只读业务库里的 `id`、`username`、`display_name`、`status`，不得使用 `SELECT *`，也不得读取 `password_hash`、角色、改密状态、最后登录、创建 / 更新时间等系统账户管理字段。
- 账户 / 分组选项接口只读业务库里的资源基础字段、有效授权关系和必要的 `group_accounts` 账号 ID 映射；普通下拉不得读取 `group_account_stats`、用量缓存、账户质量记录或并发快照，账户页专用分组 `account-options` 也只允许额外返回 `accountIds`；分组选项关键词只支持分组 ID、名称和供应商的精确 / 前缀匹配，不做无边界包含匹配。
- `stats_job_state(scope_type, scope_id, job_name)`：后台任务游标读取。
- `operation_logs(created_at, id)`：操作日志默认分页。
- `operation_logs(actor_system_account_id, created_at, id)`：按操作人筛选。
- `operation_logs(operation_scope_system_account_id, created_at, id)`：按业务作用域筛选。
- `operation_logs(module, action, created_at, id)`：按模块和动作筛选。
- `operation_logs(resource_type, resource_id, created_at, id)`：按主资源追溯。
- `operation_logs(resource_id, created_at, id)`：按主资源 ID 直接定位。
- `operation_logs(visibility_scope, created_at, id)`：用户侧合并全员摘要日志。
- `operation_logs(trace_id)`：按链路 ID 关联普通运行日志。
- `operation_logs(trace_id, created_at, id)`：按 trace ID 前缀定位时保持稳定分页。
- `operation_log_targets(target_type, target_id, created_at)`：按任意受影响资源反查。
- `operation_log_viewers(system_account_id, created_at, operation_log_id)`：用户侧读取可见操作日志。
- `operation_log_viewers(system_account_id, operation_log_id)`：用户侧当前页日志详情级别裁剪。
- `operation_log_viewers(operation_log_id, system_account_id)`：详情权限校验。
- `audit_logs(created_at, id)`：审计日志默认分页。
- `audit_logs(system_account_id, created_at, id)`：管理员按调用方筛选审计日志。
- `audit_logs(traffic_source, created_at, id)`：区分真实网关请求、手动账号测试和恢复探活审计，便于排障时过滤探活噪声。
- `audit_logs(audit_outcome, created_at, id)`、`audit_logs(final_status_code, created_at, id)`：按结果和状态码筛选。
- `audit_logs(path, created_at, id)`、`audit_logs(api_key_id, created_at, id)`、`audit_logs(group_id, created_at, id)`、`audit_logs(account_id, created_at, id)`：按接口、API Key、分组和账号定位问题。
- `audit_logs(model, created_at, id)`、`audit_logs(client_ip, created_at, id)`：按模型精确筛选、按客户端 IP 前缀定位。
- `audit_logs(trace_id)`：按链路 ID 追踪完整链路。
- `audit_log_attempts(audit_log_id, attempt_index)`：审计详情按尝试顺序读取。
- `audit_logs(error_group_id, created_at, id)`：按重复错误组反查每次 occurrence。
- `audit_payload_refs(audit_log_id, part_type, sequence_index)`：审计详情按阶段和顺序读取 payload 引用。
- `audit_payload_refs(audit_log_id, sequence_index)`：审计详情按真实捕获顺序展示完整链路片段。
- `audit_payload_refs(headers_blob_id)`、`audit_payload_refs(body_blob_id)`：清理任务判断 blob 引用。
- `audit_payload_blobs(sha256, raw_size_bytes, content_type)`：payload 去重和无引用 blob 清理。
- `audit_error_groups(window_started_at, id)`、`audit_error_groups(fingerprint, window_started_at)`：错误组列表和 upsert 定位。
- `runtime_logs(trace_id, time, id)`：按链路 ID 快速抓取同一次请求相关运行日志。
- `runtime_logs(time, id)`：默认读取最近日志。
- `runtime_logs(level, time, id)`、`runtime_logs(event, time, id)`：按通用日志级别和事件定位问题；接口、状态码和客户端 IP 等请求维度只放在审计日志中查询。
- `runtime_logs(created_at, id)`：保留运行日志写入顺序定位能力。
- `announcements(status, published_at, created_at)`：用户侧读取最近已发布公告。
- `announcements(updated_at, created_at)`：管理员公告管理列表按最近更新排序。
- `announcement_reads(system_account_id, read_at)`：按用户读取或排查公告已读状态。

默认任务策略：

- 所有定时和批处理任务都必须在独立 background worker 进程内调度和执行，不能在 Web/API 主进程里用 `setInterval`、cron 或调度框架直接执行任务函数。
- 调度框架只负责 worker 进程内的注册、不可重入、错误隔离和触发时机；worker 不能通过 IPC 回到主进程执行统计、清理、刷新或批量落库。
- 主进程和 DB service 可以把请求链路产生的待处理记录投递给 worker，但 IPC 或等价通道必须有上限，满载时按任务安全等级丢弃或降级，不能阻塞正常请求；server / DB service 角色下使用记录、操作日志、审计记录和运行日志索引都只能进入 worker IPC 队列，不能因为 worker 未就绪而回退到本地 SQLite 队列。
- DB service 负责系统管理 API 与数据库请求隔离，不负责后台定时调度；后台 worker 仍负责统计、操作日志落库、审计、运行日志索引、数据保留、代理检测和 OAuth 后台刷新。server 角色下 DB service 未就绪、队列满、IPC 超时或内部系统 API 不可用时，请求链路返回可读错误并等待 supervisor 重启，不能在主进程同步执行 DB service 操作，也不能恢复主进程管理 CRUD。业务库写入必须短事务同步提交；数据集库记录型写入优先投递 worker 队列，由 worker 作为消费端使用短事务和 `busy_timeout` 控制 SQLite 写锁等待。
- 统计 worker 每 1 分钟按 `system_account_id` 和 `(created_at, id)` 游标增量读取 `usage_records` 并 upsert 到聚合表。usage 分片落地后，统计输入侧改为每个 shard 独立维护 `(created_at, id)` 游标，统计结果库表和查询口径不变。
- 用量统计菜单只读取统计缓存，且口径是当前调用方自己的账户消耗：用户侧 `我的用量` 和管理侧 `用量统计管理` 页面日期范围都默认最近 31 天，最大最近 31 天；筛选区下方趋势账户列表在普通用户和管理员指定用户时，默认从 `usage_rank_snapshots` 读取 `caller_account + last7d + request_count` 的最近 7 天活跃前 10。趋势点读取 `usage_stats_daily` 的日行，范围累计读取 `usage_scope_range_windows` 的范围行；管理员全部用户视图的顶部摘要读取 `system_account = global` 的范围行。接口不能把每日行再相加生成范围汇总，前端也不能把行汇总成摘要。
- 统计概览属于监控窗口；页面日期范围默认今天，最大最近 31 天。概览摘要、请求 / 失败 / Token / 平均总耗时趋势、模型分布和错误 Top 10 均读取 worker 写入的 `usage_overview_*_windows`、`usage_model_rank_windows` 和 `usage_error_rank_windows`，不在接口中按小时缓存临时相加；这些窗口快照由 worker 按功能表分阶段短事务刷新，阶段之间让出事件循环；用户侧展示自己的错误 Top 10，系统性能 / 网络吞吐趋势和进程事件循环趋势只在管理侧展示。
- `AI性能监控` 的默认账户池只读取 `usage_rank_snapshots` 中 `account + last7d + request_count` 的最近 7 天活跃前 10，快照缺失时默认列表为空，不能在接口请求时临时聚合降级；图表序列只读取 `usage_stats_hourly` 的 `scope_type = account` 数据。账号选项关键词只支持账号 ID 精确 / 前缀、账号名前缀、供应商前缀和系统账号名前缀；系统账号名先解析为 owner ID，再查询账号，不能在账号选项查询中使用多列前导通配符扫描。用户侧只接受当前登录用户自有 AI 账户，别人授权给当前用户使用的账户不能作为默认账户、搜索结果或临时追加账户返回；管理侧支持按系统账户筛选，未筛选时读取 `system_account_id = global` 的账户统计缓存和排行快照。拥有者看到的是账户真实总量，自用和被授权人调用都会进入同一账户曲线。页面日期范围默认最近 3 天，最大最近 31 天，按小时返回首 token 和总耗时的平均值 / 最大值；页面顶部摘要由后端返回，前端账户筛选只影响图表显隐，不重新计算业务摘要。接口不得实时 `GROUP BY usage_records`。
- 分组账户统计 worker 定时刷新 `group_account_stats_dirty` 标记的脏分组，分组列表不得在查询时临时 `COUNT/SUM group_accounts + accounts`；授权或团队变化影响面无法精确收敛时，可以写入 `__all__` 哨兵触发全量刷新，但写请求本身不能展开所有分组，仍由 worker 异步刷新。
- 高并发分组调度使用的当前并发、分组排队、单账户排队阈值占用、慢请求计数和最近分配时间属于网关主进程内易失运行态；SQLite 只保存分组类型、策略配置、账号硬配置和可重建质量缓存。服务重启后高并发调度允许回到保守默认，不能把这些易失指标当作授权、额度、账务或审计事实。
- 高并发分组的后台反向缓存只保存可重建的短窗口质量摘要，不能保存敏感 payload、完整错误响应或会话内容。缓存缺失、过期或 worker 滞后时，网关必须按保守默认选号，不能在请求链路触发同步重建。
- 账号质量刷新 worker 默认每 10 分钟执行一次：先 flush 使用记录队列，再从 `account_quality_minute_stats` 汇总近 10 分钟真实网关请求刷新 `account_quality_scores`。分钟桶由用量统计 worker 随主游标增量写入；刷新 worker 不回扫 `usage_records`，旧质量行一次性加载为 Map，避免按账号逐条查询。主动探测能力已删除，worker 只处理真实请求样本，源码和预上线本地库都不再保留 `last_probe_at`。超过 24 小时未更新的质量分不参与网关调度，活跃账号短窗口无新样本时会标记为 `stale`。
- OpenAI OAuth Access Token 保活 worker 默认每 1 分钟扫描仍存在、未删除、有 `refresh_token` 且即将过期的 OAuth 账户，扫描不受账户状态和调度标记影响；成功时只更新 `accounts.credentials_encrypted` 中的 token 凭据，不恢复调度状态、不清理冷却和最近错误；连续 3 次失败会把账户写为 `status = error`、`last_error_code = oauth_token_refresh_failed`。
- 网关请求中触发的 OpenAI OAuth Access Token 即时刷新，在 server 角色下必须通过 DB service 查最新账户、解析代理和持久化新凭据，不能直接读取或更新 SQLite。
- 冷却账号恢复性复测只处理冷却到期的 `temporary_unavailable`、仍可调度、已绑定分组且未过期的账号；`rate_limited` 等其他状态不进入后台复测队列，不做自愈恢复。复测前优先从最近真实 `usage_records` 学习 `endpoint/model/stream` 元信息，最多按最近 7 天 date shard 倒序查找，命中后立即停止，endpoint 只按规范化后的 OpenAI 路径精确 / 子路径前缀识别，不使用前导通配符扫描；该流程只读取 `traffic_source = gateway` 的真实请求，不读取 `request_snapshot_json`，也不重放用户 prompt、工具参数或文件内容。探活输入使用后端最小 Responses 默认输入。后台复测固定启用，复用真实网关链路但关闭单次探活内部同账号短重试；账号先按 3 秒进入快速恢复通道，失败后翻倍更新 `cooldown_until`，超过快速阈值后进入慢速恢复，单次等待不超过 `defaultTemporaryUnschedulableMinutes`，进入慢速恢复后如果复测失败发生在 `cooldownAccountRetestMaxBackoffHours` 表达的最长观察之后则转为异常。恢复探活使用记录与审计均标记 `traffic_source = cooldown_retest`，不参与业务统计、账户质量统计和真实请求形态学习，Codex 额度快照也保留 `cooldown_retest` 来源而不伪装成真实网关流量。
- 代理延迟刷新 worker 固定每 1 分钟检测最多 20 个启用代理，测试目标来自已启用供应商的默认地址，并把最近状态、延迟和检测时间写回 `proxy_profiles`；出口 IP / 地区只由手动测试刷新，不提供系统设置项调整。
- 授权账户调用需要同时写入调用方统计、调用方命中账户统计、真实账户统计、授权额度统计和授权报表：调用方列表、分组、API Key 和日志按 `system_account_id` 聚合；`我的用量` 按 `system_account_id + scope_type = caller_account + account_id` 读取本人对该账户的消耗；账户所有者的账户总用量按 `account_owner_system_account_id + scope_type = account + account_id` 聚合；授权额度按 `account_owner_system_account_id + account_authorization_id` 聚合；管理侧团队 / 用户消耗按授权范围窗口表直读，并过滤资源归属人自用消耗。
- 历史授权分组调用记录需要保留调用方统计、真实分组统计、授权额度统计和授权报表口径：调用方 API Key 和日志按 `system_account_id` 聚合；分组所有者的分组总用量按 `group_owner_system_account_id + group_id` 聚合；授权额度按 `group_owner_system_account_id + group_authorization_id` 聚合；管理侧团队 / 用户消耗按授权范围窗口表直读，并过滤资源归属人自用消耗。新建 API Key 不再绑定授权分组，授权账户应先进入调用方自有分组后再产生调用。
- `统一授权` / `授权操作` 关系列表不展示额度和用量统计；团队 / 用户授权消耗明细只读取 `authorization_team_usage_range_windows` 和 `authorization_user_usage_range_windows`，其数据由 `authorization_team_usage_summary_daily` 和 `authorization_user_usage_summary_daily` 刷新而来。统计 worker 随 `usage_records` 游标增量写入日摘要，并预先写好 `all`、资源类型汇总和指定资源三种资源筛选行；接口只能按最近 31 天内日期范围、`page/pageSize` 和筛选条件直读一组窗口行，不能临时 `SUM usage_records`，也不能把 `usage_stats_daily/weekly/monthly` 或报表缓存再二次聚合。
- 统一授权可选美元成本额度保存在业务主表 `resource_authorization_grants.limits_json`，并同步到最终用户授权 `resource_authorizations.limits_json`；JSON 内 `limit` 表示美元金额。网关按 `usage_stats_totals`、`usage_stats_daily`、`usage_stats_weekly`、`usage_stats_monthly` 和 `usage_quota_hourly_windows` 的 `account_authorization` / `group_authorization` 维度直读成本，判断 n 小时、日、周、月和总额度，不在请求内扫描 `usage_records`，也不在请求内按小时桶求和。
- 团队授权美元额度读取 `account_authorization_team` / `group_authorization_team` 作用域缓存，`scope_id = resource_id + ':' + team_id`，由统计 worker 随使用记录增量写入；网关不再枚举成员授权并求和。统计 worker 默认每 1 分钟增量推进并刷新额度小时窗口，网关额度判断带短 TTL 内存缓存，因此允许轻微超额，统计追平后下一次请求会返回 429 和“额度已用完，请联系管理员提升额度”。
- API Key 列表展示累计用量，读取 `usage_stats_totals` 中 `scope_type = api_key` 的缓存，不使用今日 `usage_stats_daily` 口径；API Key 可选美元成本额度保存在 `api_keys.quota_limits_json`，JSON 内 `limit` 表示美元金额。网关按 `usage_stats_totals`、`usage_stats_daily`、`usage_stats_weekly`、`usage_stats_monthly` 和 `usage_quota_hourly_windows` 的 API Key 维度直读成本，判断 n 小时、日、周、月和总额度，不在请求内扫描 `usage_records`，也不在请求内按小时桶求和。
- API Key 额度是轻量异步统计口径：统计 worker 默认每 1 分钟增量推进，网关额度判断带短 TTL 内存缓存，因此允许轻微超额；统计追平后下一次请求会返回 429 和“额度已用完，请联系管理员提升额度”。
- API Key 生命周期分为停用、业务删除和记录物理清理：停用只改状态并立即拒绝后续调用；业务删除同步移除业务库记录并让后续请求立即无法再使用该 Key；该 Key 关联的历史使用记录、原始审计日志、审计尝试、payload 引用和审计错误组由数据集库维护队列分批清理，API Key 维度统计缓存、额度窗口、排行 / 范围窗口以及调用方、账户、分组、授权、模型和错误等相关聚合缓存由统计结果库反向扣减。usage shard 与统计结果库不能共享强事务，因此清理任务先在统计结果库用 `usage_record_cleanup_deductions` 保证每条 usage 只扣减一次，再删除对应 shard 行；如果 shard 删除或后续步骤失败，后台重试只补删或继续收尾，不重复扣减统计。未被任何审计引用继续使用的 payload blob 由后续无引用 blob 清理任务删除。
- 管理员全局汇总读取后台写入的 `system_account = global` 缓存行，不在概览接口里临时汇总多个系统账户缓存行，更不能回扫 `usage_records`。
- 系统采样 worker 每 10 到 30 秒写入一次 `system_metrics_samples`，不写用户级业务归属；`event_loop_lag_ms` 来自 worker 内独立事件循环直方图，记录上个采样窗口内去除采样分辨率基线后的额外最大延迟，保留为主机采样兼容字段。多进程事件循环延迟单独写入 `process_event_loop_samples`：worker 本地采样自身，server 通过 IPC 返回主进程样本，并通过专用诊断 IPC 读取 DB service 样本；诊断 IPC 不进入 DB service 业务请求计数，也不触发不可用熔断。采样频率跟随系统监控间隔，只有少量 IPC 和一次直方图读取，不扫描业务数据。概览接口的最新进程采样状态和最近 24 小时峰值状态都固定返回 `server`、`worker`、`db-service` 三个角色；缺少样本时用 `sampleAvailable = false` 和 `null` 字段表达未知，不用缺项、0 或默认时间伪装正常。事件循环趋势固定展示最近 24 小时分钟峰值桶，不跟随概览日期范围，单次读取被原始采样 7 天保留期和 24 小时窗口硬限制住。
- 后台 worker 是一个固定常驻子进程，不是每个后台任务临时创建一个进程；内部各定时任务由同一个调度器注册、不可重入执行并记录运行快照。管理侧系统监控可查看每个后台任务的最近耗时、最长耗时、运行中状态、成功次数、失败次数、跳过次数和最近错误，用于判断具体是哪一个任务拖慢 worker；worker snapshot 不可用时接口必须返回显式可用性标记和 `null`，不能把未知状态压成空任务数组。
- 系统采样的 `memory_used_percent` 表示主机实际内存压力口径，不是所有平台都等同于 `totalmem - freemem`。macOS 会把可回收文件缓存、inactive 和 speculative 页面排除在已用内存外，按 `vm_stat` 的 `Anonymous pages + Pages wired down + Pages occupied by compressor` 计算，避免把系统缓存误报为应用内存占用；读取失败时才回退到 Node 默认口径。
- 审计日志 worker 每隔短时间或达到批量阈值后，从 worker 队列取终态审计记录，按策略计算正文保留、压缩、去重和错误聚合，并用短事务批量写入 `audit_logs`、`audit_log_attempts`、`audit_payload_refs`、`audit_payload_blobs` 和 `audit_error_groups` 元数据；超过单次读取窗口的大 blob 保持 plain，文件写入本地数据目录。
- 网关请求处理中不能同步写 `audit_logs`；SSE 和其他流式响应必须等自然结束、失败、超时或客户端断开后，才按终态记录入队。
- 操作日志在业务库写操作提交成功后入队，worker 从操作日志队列批量写入 `operation_logs`、`operation_log_targets` 和 `operation_log_viewers`；操作日志入队或落库失败只影响追溯数据，不反向回滚已提交业务变更。
- 数据集库和统计结果库维护类动作不在管理接口或 DB service 内直接执行；API Key 删除后的关联记录清理、表监控手动清理使用记录、OpenAI Codex 用量快照写入等都投递 `recordMaintenanceQueue`，由 worker 分批执行。
- 运行日志索引 worker 从 Pino JSONL 输出流旁路接收日志行，按 worker 队列批量写入 `runtime_logs`，并随新增日志增量维护级别 / 事件 facets；常规维护只在 facets 缺失时重建，数据保留清理按已删除索引行扣减 facets，不能每轮或每次清理后对 `runtime_logs` 全量 `COUNT/GROUP BY`。
- 日志搜索不再有额外搜索影子表回填任务。
- “日志搜索”索引查询读取当前 SQLite `runtime_logs` 表，使用 `traceId`、级别、事件和日志时间等通用索引条件缩小结果，关键字只对 `message` 列做普通模糊匹配；列表默认展示最近 100 条并通过后端分页继续翻页。索引表保留周期由后台清理任务控制。
- “日志搜索”的 `grep 模式` 通过后端 `rg` 直接扫描日志目录中当前保留的 `.log` 文件，不受索引表 3 天保留期限制；文件日志默认保留 30 天，并受最多 500 个轮转文件和单文件大小限制。该模式默认按文件时间搜索最近 3 天，单次文件时间范围最多 7 天，时间范围只用于筛选参与扫描的文件，不读取文件内容判断行时间；不设置查询超时，最多展示 100 行；多关键字必须在同一行同时命中，后端按日志时间或文件时间返回最新匹配。运行环境缺少 `rg` 时直接返回错误提示，不回退到慢速文件扫描。
- 使用记录、操作日志、数据集库维护、审计和运行日志索引队列都是 best-effort 队列，系统重启、进程崩溃或队列溢出导致数据集库事实丢失或维护延迟可以接受；队列丢弃或投递失败计数应进入运维监控。
- 账户、分组、API Key 等列表接口只读 `usage_stats_totals` / `usage_stats_daily`，不要在列表查询里 `SUM usage_records`。
- 概览图表接口读取 `usage_overview_*_windows`、`usage_model_rank_windows`、`usage_error_rank_windows` 和 `system_metrics_trend_windows`；进程事件循环趋势是短期运维排障图，固定读取最近 24 小时低频采样并按分钟聚合，不跟随业务日期范围，页面上方摘要取同一 24 小时窗口内各进程最大延迟；后台任务运行状态来自 worker 运行态快照，不落表；运行态快照缺失时通过可用性字段表达不可观测，不用 `0`、`false`、`[]` 或默认天数伪装正常；`AI性能监控` 图表只读 `usage_stats_hourly` 和账户元数据，摘要只读 `ai_performance_summary_windows`。
- 全局规则：除独立 background worker、离线清洗脚本、使用记录分页明细外，任何前端列表、概览、详情和下拉元数据接口都不能在请求时做统计聚合。

表数据保留与统一清理：

| 表 | 数据类型 | 保留策略 | 是否已有统一定时清理 | 注意事项 |
| --- | --- | --- | --- | --- |
| `runtime_logs` | 普通运行日志搜索索引 | 固定最近 3 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只删除 SQLite 搜索索引，不删除后端 `.log` 文件 |
| `runtime_log_file_cursors` | 当前日志文件增量读取游标 | 固定最近 3 天未更新游标 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 当前存在的日志文件会被追尾任务持续刷新；缺失或长期未更新的旧文件游标会自动删除 |
| `model_check_runs`、`model_check_items` | 模型检测历史和诊断明细 | 默认 30 天，最多 365 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只保留有界脱敏摘要，过期检测运行和检测项一起删除 |
| `operation_logs`、`operation_log_targets`、`operation_log_viewers` | 业务操作追溯日志 | 默认 365 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只按时间清理，不因资源删除级联删除历史日志 |
| `audit_logs`、`audit_log_attempts` | 原始审计事件和上游尝试 | 成功样本默认 7 天，失败 / 异常事件默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 完全成功请求未命中 10% 采样时不写入原始审计 |
| `audit_payload_refs`、`audit_payload_blobs` | 原始审计 payload 引用和压缩 blob 元数据 | 成功样本正文默认 7 天，失败 / 异常正文默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 先删除过期引用，再删除无引用 blob 和本地 blob 文件 |
| `audit_error_groups` | 重复错误聚合 | 默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只做展示和排障聚合，不替代事件记录 |
| `usage_records` | 网关请求事实明细 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理；管理员也可在表监控按截止时间手动清理 | 自动保留任务和手动清理都必须按统计聚合 / 回填游标保护，只删除已确认进入缓存的旧明细；手动清理额外强制保留最近 1 天 |
| `account_quality_minute_stats` | 账号质量分钟桶 | 默认 24 小时 | 是，`data-retention-cleanup` 每天在 worker 内清理；账号质量刷新任务也会兜底清理 | 只保存真实网关请求短窗口质量样本，不回扫 `usage_records` |
| `usage_stats_minute`、`usage_model_minute`、`usage_error_minute`、`usage_latency_minute` | 分钟级统计缓存 | 默认 48 小时 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 供短窗口、账号质量和后续精细统计使用，不作为页面大范围查询事实源 |
| `usage_stats_hourly`、`usage_model_hourly`、`usage_error_hourly`、`usage_latency_hourly` | 小时级统计缓存 | 默认 60 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 覆盖 AI 性能最近 31 天小时趋势，并供 worker 刷新概览、排行、额度和 AI 性能窗口快照；API 摘要不在请求时聚合这些小时桶 |
| `usage_stats_daily`、`usage_model_daily`、`usage_error_daily`、`usage_latency_daily` | 日级统计缓存 | 默认 400 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 覆盖近一年自然日统计、范围窗口刷新和日 / 周 / 月重建 |
| `usage_stats_weekly`、`usage_model_weekly`、`usage_error_weekly`、`usage_latency_weekly` | 周级统计缓存 | 默认 104 周 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 自然周额度、周报和长期趋势基础 |
| `usage_stats_monthly`、`usage_model_monthly`、`usage_error_monthly`、`usage_latency_monthly` | 月级统计缓存 | 默认 24 个月 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 自然月额度、月度账单和年度追溯基础 |
| `authorization_team_usage_summary_daily`、`authorization_user_usage_summary_daily` | 授权日报表缓存 | 默认跟随日级统计保留 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 由统计 worker 增量写入，供授权范围窗口刷新 |
| `authorization_team_usage_range_windows`、`authorization_user_usage_range_windows`、`usage_scope_range_windows` | 最近 31 天范围窗口 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 只保存可直读范围缓存，旧窗口可重建或丢弃 |
| `usage_rank_snapshots` | 常用 TopN 快照 | 默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | AI 性能监控、我的用量和排障排行读取最新快照；快照缺失时页面默认池为空 |
| `usage_overview_summary_windows`、`usage_overview_trend_windows`、`usage_model_rank_windows`、`usage_error_rank_windows`、`ai_performance_summary_windows`、`usage_quota_hourly_windows` | 统计概览 / AI 性能 / 额度窗口缓存 | 默认最近 31 天窗口或最近 30 天刷新结果 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | API 只直读这些预聚合结果，不在请求时回扫明细 |
| `account_usage_snapshots` | 账号额度最新快照 | 默认 30 天未更新即清理 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 正常按账号主键 upsert，清理的是长期未更新的旧快照 |
| `system_metrics_samples` | 主机监控原始采样 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只用于最新状态和短期排障 |
| `system_metrics_hourly` | 主机监控小时汇总 | 默认 30 天，最多 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 供 worker 刷新 `system_metrics_trend_windows`；API 不在请求时聚合这些小时桶 |
| `system_metrics_trend_windows` | 系统监控窗口趋势缓存 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 供概览接口直读 |
| `process_event_loop_samples` | 进程事件循环原始采样 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 按 `server`、`worker`、`db-service` 分进程角色保存，用于短期定位哪个进程卡顿 |
| `process_event_loop_hourly` | 进程事件循环小时汇总 | 默认 30 天，最多 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 保留长期粗粒度排障和兼容缓存；管理页趋势不再用小时桶 |
| `process_event_loop_trend_windows` | 进程事件循环旧窗口趋势缓存 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 保留兼容；管理页趋势固定用最近 24 小时分钟桶 |
| `database_storage_snapshots`、`table_storage_snapshots` | 表监控采样历史 | 默认最近一月，最多最近一月 | 是，`data-retention-cleanup` 每天在 worker 内清理，采样写入时也会轻量兜底清理 | 用于管理员表监控页面容量趋势，不纳入默认业务备份 |
| `system_sessions` | 后台登录会话 | 到期即清理 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 查询时也会校验过期时间，定时清理用于回收表数据；`last_seen_at` 只允许按短间隔节流刷新，不应在每个鉴权请求中无条件写入 |

不按保留期物理清理：

- `usage_stats_totals` 长期保留，作为账户、分组、授权和全局总量缓存。
- `stats_job_state` 长期保留，作为统计游标和任务状态；它是自动保留任务和管理员手动清理删除 `usage_records` 的安全边界。
- `group_account_stats` 是当前分组账户状态缓存，由刷新任务按脏分组刷新；`group_account_stats_dirty` 是刷新队列，不属于历史日志，处理完成即可删除。
- 数据集库不维护日志搜索影子表；数据集库和统计结果库新增表都应是普通表或普通索引，必须接入统一保留期或明确说明长期保留理由。
- 普通日志文件由文件日志滚动配置清理，不属于 SQLite 表清理；当前默认保留 30 天，并受最多 500 个轮转文件和单文件大小限制；`grep 模式` 扫描当前保留的 `.log` 文件，但单次文件时间范围最多 7 天。

统一清理规则：

- 表数据保留期统一由 `data-retention-cleanup` 每天在独立 background worker 进程内执行；表监控页面额外提供 `usage_records` 按截止时间手动清理入口，用于容量异常时提前释放可复用页。
- 清理任务按批次删除，默认每类表每轮最多处理 `dataRetentionCleanupBatchSize = 10000` 条、最多 `dataRetentionCleanupMaxBatchesPerRun = 10` 批，避免长时间占用 SQLite 写锁。
- 手动清理 `usage_records` 同样按批次执行，截止时间不能晚于当前时间 24 小时前，并且必须受统计聚合游标和必要回填游标保护。提交前先按 `created_at < cutoffAt` 与统计安全游标交集做有限预检查；没有可安全清理记录、统计游标尚未建立或 worker 投递不可用时返回 `queued = false` 和原因；预检查通过后才返回 `queued = true` 并交给 worker 分批清理。
- 统计聚合、系统指标采样、审计日志落库和运行日志索引队列只负责写入或聚合，不再在各自流程里顺手删除历史表数据。
- 如果统计缓存损坏或统计口径升级，可以停服务后在发布包根目录运行 `node backend/dist/scripts/maintenance/rebuild-usage-stats.js`，从 usage shard 文件中尚未清理的 `usage_records` 重新构建缓存。该命令会清空并重建当前 `backend/.env` 指向统计结果库里的统计缓存；如果 usage shard 为空或历史 `usage_records` 已丢弃，历史统计明确放弃，后续从新请求重新累计。执行前必须确认业务库路径无误，避免误操作业务数据。

## 操作日志存储

操作日志是独立于使用记录和原始审计日志的业务变更追溯数据，具体行为见 [操作日志设计](操作日志设计.md)。

源码边界：

- schema 入口及 `storage/schema/` 拆分文件中只保留当前 `operation_logs`、`operation_log_targets`、`operation_log_viewers` 表结构和索引。
- 操作日志只记录成功提交的状态变更；`GET`、列表、详情、筛选、分页和日志查看不写操作日志。
- 操作日志不保存完整请求体、完整响应体、完整 headers、凭据、token、代理密码、验证码、登录密码或原始审计 payload。
- 删除业务资源时不删除历史操作日志；历史日志保留当时的资源 ID、资源名称、安全摘要和影响用户。
- 普通用户可见性优先由 `operation_log_viewers` 预计算，全员安全摘要由 `operation_logs.visibility_scope = 'all_users'` 承载，不为全员摘要展开 viewer 行。
- 用户侧列表由 `operation_log_viewers.system_account_id` 命中的可见集合与 `visibility_scope = 'all_users'` 的全员摘要集合合并，列表不解析字段差异 JSON，详情按权限再读取明细。

当前 `operation_logs` 表：

- `id`
- `trace_id`
- `actor_system_account_id`
- `actor_username`
- `actor_display_name`
- `actor_role`：`admin`、`user`
- `operation_scope_system_account_id`
- `mode`：`self`、`admin`
- `module`
- `action`
- `operation_key`
- `resource_type`
- `resource_id`
- `resource_name`
- `summary`
- `detail_level`：`full`、`summary`
- `visibility_scope`：`targeted`、`all_users`、`admin_only`
- `changes_json`
- `metadata_json`
- `method`
- `path`
- `status_code`
- `client_ip`
- `user_agent`
- `created_at`

当前 `operation_log_targets` 表：

- `id`
- `operation_log_id`
- `target_type`
- `target_id`
- `target_name`
- `target_owner_system_account_id`
- `relation`：`primary`、`affected`、`created`、`deleted`、`owner`、`grantee`、`team_member`、`bound_resource`
- `created_at`

当前 `operation_log_viewers` 表：

- `operation_log_id`
- `system_account_id`
- `visibility_reason`：`actor_self`、`resource_owner`、`admin_managed_my_resource`、`authorization_owner`、`authorization_grantee`、`team_member`、`team_authorization`、`global_affected`、`bound_resource_affected`
- `detail_level`：`full`、`summary`
- `created_at`

保存规则：

- `changes_json` 只保存安全字段差异；敏感字段只保存“已变更”“已清空”“已设置”等摘要。
- `metadata_json` 只保存业务排查所需的安全上下文，例如授权来源摘要、设置分组、资源归属快照等。
- 管理员操作某个用户资源时，`actor_system_account_id` 保存真实管理员，`operation_scope_system_account_id` 保存被管理用户或资源 owner。
- 全员可见的设置或公告变化可不展开 `operation_log_viewers`，由 `visibility_scope = 'all_users'` 支撑用户侧查询。

## 原始审计日志存储

原始审计日志是独立于使用记录的高权限排障数据，捕获行为见 [原始审计日志设计](原始审计日志设计.md)，容量治理见 [审计日志保全策略设计](审计日志保全策略设计.md)。

源码边界：

- schema 入口及 `storage/schema/` 拆分文件中只保留当前审计事件、尝试、payload 引用、blob 元数据和错误聚合表结构，不保留旧审计结构兼容分支。
- 网关模块只创建请求内捕获上下文、追加原始片段和终态投递，不直接调用 repository 同步写审计表。
- 独立 background worker 进程里的审计批量落库服务负责从 worker 队列取终态记录，计算正文保全策略、压缩、去重和错误聚合，并用短事务写入元数据。
- 大 payload blob 第一版存放在 `backend/data/audit/blobs/` 下的本地文件中，SQLite 只保存 blob 元数据和引用。
- 审计 payload 不参与统计 worker，不作为用量事实源。

当前 `audit_logs` 表：

- `id`
- `trace_id`
- `traffic_source`：`gateway`、`manual_account_test`、`cooldown_retest`
- `system_account_id`
- `api_key_id`
- `group_id`
- `account_id`
- `provider_code`
- `method`
- `path`
- `query_string`
- `model`
- `stream`
- `client_ip`
- `user_agent`
- `audit_outcome`：`success`、`success_after_retry`、`gateway_failed`、`upstream_failed`、`stream_failed`、`client_aborted`
- `success`
- `final_status_code`
- `error_phase`
- `error_code`
- `error_message`
- `sample_bucket`
- `sample_reason`
- `attempt_count`
- `payload_count`
- `payload_bytes`
- `raw_payload_bytes`
- `compressed_payload_bytes`
- `compression_saved_bytes`
- `error_group_id`
- `capture_status`：`complete`、`dropped`、`overflow`
- `started_at`
- `ended_at`
- `duration_ms`
- `first_token_ms`
- `created_at`

当前 `audit_log_attempts` 表：

- `id`
- `audit_log_id`
- `attempt_index`
- `account_id`
- `account_owner_system_account_id`
- `group_id`
- `proxy_url`
- `provider_code`
- `upstream_method`
- `upstream_url`
- `upstream_status_code`
- `success`
- `error_phase`
- `error_code`
- `error_message`
- `started_at`
- `ended_at`
- `duration_ms`

当前 `audit_payload_refs` 表：

- `id`
- `audit_log_id`
- `attempt_id`
- `part_type`：`client_request`、`upstream_request`、`upstream_response`、`gateway_response`、`gateway_error`、`gateway_metadata`
- `sequence_index`
- `content_type`
- `content_encoding`
- `headers_blob_id`
- `body_blob_id`
- `headers_sha256`
- `body_sha256`
- `raw_size_bytes`
- `compressed_size_bytes`
- `capture_status`：`complete`、`summary_only`、`hash_only`、`expired`、`dropped`
- `created_at`

当前 `audit_payload_blobs` 表：

- `id`
- `sha256`
- `raw_size_bytes`
- `compressed_size_bytes`
- `content_type`
- `content_encoding`
- `compression`：当前固定支持 `none`、`gzip`；超过单次最大读取窗口的正文保持 `none`，便于后续按 offset 读取窗口。
- `storage_key`
- `ref_count`
- `first_seen_at`
- `last_seen_at`
- `created_at`

当前 `audit_error_groups` 表：

- `id`
- `fingerprint`
- `window_started_at`
- `window_ended_at`
- `system_account_id`
- `api_key_id`
- `group_id`
- `account_id`
- `provider_code`
- `path`
- `model`
- `status_code`
- `error_phase`
- `error_code`
- `error_type`
- `normalized_request_fingerprint`
- `normalized_error_fingerprint`
- `count`
- `first_event_id`
- `last_event_id`
- `sample_event_id`
- `last_message`
- `created_at`
- `updated_at`

保存规则：

- `audit_logs` 只写入命中 10% 稳定采样的完全成功请求，以及所有失败、异常、客户端中断、流式中断和重试后成功链路；每次请求事实仍由 `usage_records` 保底。
- `headers_sha256` 和 `body_sha256` 均针对压缩前的原始字节计算。
- payload blob 可以压缩存储，压缩算法、原始大小和压缩后大小必须记录。
- 相同 `sha256 + raw_size_bytes + content_type` 的 blob 只存一份，多条事件通过 `audit_payload_refs` 引用。
- 未完整保留正文时，`capture_status` 必须明确标记为 `summary_only`、`hash_only`、`expired`、`overflow` 或 `dropped`，不能伪装成完整原文。
- 单条请求超过活跃捕获上限或队列上限时，允许只保存轻量事件和降级原因；不能写入伪完整 payload。
- 错误聚合只影响展示和排障汇总，不删除 `audit_logs` 事件。

## 系统团队与统一授权存储

当前项目未正式上线，本地 SQLite 可以备份后直接重建或清洗，因此新版授权统一使用 `resource_authorization_grants`、`resource_authorizations` 和 `resource_authorization_sources`，不保留旧 `account_authorizations` / `group_authorizations` 分支。

源码边界：

- `backend/src/storage/schema.ts` 作为 schema 入口，`backend/src/storage/schema/` 拆分文件只保留当前完整表结构、索引、默认约束和外键。
- 后端启动路径、repository、routes、前端页面都不能长期保留一次性迁移、旧数据兼容、临时同步修复、临时表改名或迁移标记代码。
- 本地库异常或结构变化时，先备份数据库，再用直接 SQL、临时离线脚本或重建库处理；处理脚本不得挂入运行时代码。
- 正式上线后如需支持外部用户升级，再另行设计版本化 schema 演进，不和当前预上线规则混用。

当前 `system_teams` 表：

- `id`
- `name`
- `description`
- `status`：`active`、`disabled`
- `created_by`
- `created_at`
- `updated_at`

当前 `system_team_members` 表：

- `id`
- `team_id`
- `system_account_id`
- `member_role`：当前固定 `member`
- `status`：`active`、`removed`
- `joined_at`
- `removed_at`
- `created_by`
- `created_at`
- `updated_at`

当前 `resource_authorization_grants` 表：

- `id`
- `resource_type`：`account`、`group`
- `resource_id`
- `resource_owner_system_account_id`
- `grantee_type`：`system_account`、`team`
- `grantee_system_account_id`：`grantee_type = system_account` 时必填
- `grantee_team_id`：`grantee_type = team` 时必填
- `scope`：当前固定为 `use`
- `status`：`active`、`paused`、`expired`、`revoked`
- `remark`
- `expires_at`
- `limits_json`
- `model_policy_json`
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `updated_at`

当前 `resource_authorizations` 表：

- `id`
- `resource_type`：`account`、`group`
- `resource_id`
- `resource_owner_system_account_id`
- `grantee_system_account_id`：最终被授权系统账户
- `scope`：当前固定为 `use`
- `status`：`active`、`paused`、`expired`、`revoked`
- `effective_source_type`：当前生效来源，`manual`、`team` 或空
- `effective_source_team_id`：当前生效来源是团队时记录团队 ID，否则为空
- `activated_at`
- `last_source_changed_at`
- `remark`
- `expires_at`：可选自动回收时间，到期后状态变为 `expired`，记录保留。
- `limits_json`：美元成本额度限制 JSON，内部 `limit` 表示美元金额，支持 n 小时、日、周、月和累计总额度；为空表示不限制。
- `model_policy_json`：预留字段，当前不启用。
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `revoked_reason`
- `updated_at`

当前 `resource_authorization_sources` 表：

- `id`
- `authorization_id`
- `source_type`：`manual`、`team`
- `source_team_id`：`source_type = team` 时记录团队 ID
- `status`：`active`、`superseded`、`revoked`
- `activated_at`
- `ended_at`
- `ended_reason`
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `updated_at`

约束：

- `resource_type = account` 时，`resource_id` 必须指向真实存在的 AI 账户，且 `resource_owner_system_account_id` 必须等于 `accounts.system_account_id`。
- `resource_type = group` 时，`resource_id` 必须指向真实存在的分组，且 `resource_owner_system_account_id` 必须等于 `groups.system_account_id`。
- `grantee_system_account_id` 必须指向存在的系统账户，且不能等于 `resource_owner_system_account_id`。
- 授权操作以 `resource_authorization_grants` 为业务主表；最终用户可调用关系以 `resource_authorizations` 为运行时主表；来源解释和优先级切换以 `resource_authorization_sources` 追踪。
- 团队授权由 service 层展开为成员用户级授权；资源所有者如果也是团队成员，其自用调用仍按自用处理，不计入授权消耗。
- 同一个 `resource_type + resource_id + grantee_system_account_id` 在 `resource_authorizations` 中只维护一条最终用户授权；同一资源同一业务目标的有效人员 / 团队授权由 `resource_authorization_grants` 的部分唯一索引兜底。
- 收回授权只把 `status` 改为 `revoked` 并写入 `revoked_by` / `revoked_at`，不物理删除，历史统计继续可查。
- 到期授权只把 `status` 改为 `expired`，不物理删除，独立 background worker 进程负责自动处理。
- 团队停用、成员移除或系统账户停用后，授权来源和最终用户授权状态必须同步更新；网关调度按最终用户授权、系统账户、资源和绑定状态阻断调用，不再额外把团队作为运行时主体遍历判断。
- 授权资源不能继续被被授权人二次授权给第三方。

`group_accounts` 和 `api_keys` 的授权路径字段：

- `group_accounts.account_id` 仍指向唯一物理 AI 账户；同一 `system_account_id + group_id + account_id` 只能有一条有效分组成员关系，不能因多个授权来源重复加入同一账户。`system_account_id` 表示这条分组绑定属于哪个使用方，本地分组绑定不改变账户所有者的资源归属。
- `group_accounts.account_authorization_id`：当被授权用户把授权 AI 账户加入自己分组时记录首选统一授权 ID；为空表示自有账户。
- `api_keys.group_id` 仍指向唯一分组；新建和编辑时只能绑定 API Key 所属系统账户自己的分组。
- `api_keys.group_authorization_id` 是历史兼容字段；新建和编辑 API Key 不再写入，网关运行时要求该字段为空，并要求 `api_keys.group_id` 的分组归属人与 `api_keys.system_account_id` 一致。
- 授权账户通过 `group_accounts.account_authorization_id` 进入被授权用户自己的分组后参与调度；调度时仍必须重新校验最终用户授权、调用方系统账户、资源和绑定状态，不能只信任历史绑定。
- 当同一用户对同一账户或分组同时拥有个人来源和团队来源时，只允许存在一条用户级授权，并在资源行返回全部来源供排查。
- 调度只校验用户级授权是否仍有效；来源变化不应导致资源重复或历史绑定断裂。

`usage_records` 需要额外冗余这些字段：

- `traffic_source`：`gateway` 表示真实客户端网关请求，`manual_account_test` 表示手动账号测试，`cooldown_retest` 表示后台恢复探活。
- `account_owner_system_account_id`：命中账户所有者，自有账户时等于 `system_account_id`。
- `group_owner_system_account_id`：命中分组所有者，自有分组时等于 `system_account_id`。
- `account_access_type`：`owner`、`authorized`。
- `group_access_type`：`owner`、`authorized`；`authorized` 仅用于历史授权分组调用记录或兼容读路径，新建 API Key 不再产生授权分组调度。
- `account_authorization_id`：命中授权 AI 账户时写入统一授权 ID，自有账户或历史授权分组访问时为空。
- `account_authorization_source_type`：命中授权 AI 账户时的生效来源快照，取 `manual` 或 `team`。
- `account_authorization_source_team_id`：命中授权 AI 账户且来源为团队时的团队 ID 快照。
- `group_authorization_id`：历史授权分组调用记录的统一授权 ID，自有分组为空。
- `group_authorization_source_type`：命中授权分组时的生效来源快照，取 `manual` 或 `team`。
- `group_authorization_source_team_id`：命中授权分组且来源为团队时的团队 ID 快照。

日志隔离和统计口径：

- 使用记录页按 `usage_records.system_account_id` 查询，所以被授权用户只看自己的调用明细。
- 资源所有者不按明细读取被授权用户的 `usage_records`，授权消耗明细只读取后台 worker 按统一授权 ID、团队快照、用户和资源筛选写入的日摘要与范围窗口表。
- 授权消耗统计必须过滤自用记录：账户授权按 `usage_records.system_account_id != account_owner_system_account_id`，分组授权按 `usage_records.system_account_id != group_owner_system_account_id`。
- 账户真实总用量按 `account_owner_system_account_id + account_id` 聚合，所以自用和所有用户授权都会累计到同一个真实账户维度。
- 分组真实总用量按 `group_owner_system_account_id + group_id` 聚合，所以自用和所有用户授权都会累计到同一个真实分组维度。
- 授权统计主口径是“资源 × 用户”：按用户级统一授权 ID 进入统计，worker 同步写入团队报表行和用户报表行，不再把个人来源和团队来源拆成两份。
- 个人授权详情需要按实际调用方 `system_account_id` 写入用户报表，所以 A 授权给 B 时，A 能看到 B 对该资源的消耗。
- 团队授权详情基于使用记录里的团队来源快照写入团队报表；团队总量和用户筛选结果在 worker 中预先生成，页面请求不能现场按成员或授权关系聚合。
- 团队来源变更只影响用户级授权来源摘要；成员移除后，如果该用户没有其他来源则授权失效，但历史用户消耗仍按该用户保留。
- 分组授权共享的是动态分组集合，但只共享分组所有者自有账户；如果分组里包含别人授权来的账户，调度时必须过滤，不能通过分组授权继续共享给第三方。
- `usage_records` 按每次实际上游请求 / 上游尝试写入；同一个客户端请求如果发生重试或切号，可以产生多条记录。`traffic_source` 用于区分真实网关请求、手动账号测试和恢复探活。`usage_records` 是排障和重建统计的事实源，不等同于业务消耗统计。
- `request_count` 统计的是进入业务统计口径的请求次数：后台统计 worker 聚合真实网关请求和手动账号测试，恢复探活 `traffic_source = cooldown_retest` 只保留明细和审计，不进入业务用量统计、账户质量统计或真实请求形态学习；统计安全游标可以确认处理恢复探活明细，避免阻塞后续 `usage_records` 清理。没有 token / cost 的失败记录按 0 token、0 cost 计入请求和错误次数，便于统一追踪；成本、缓存成本与 token 仍以实际解析到的上游用量和模型价格快照为准。
- 同一次上游尝试不能因为调用方同时拥有个人来源和团队来源而重复入库；资源真实总量按 `usage_records.id` 去重。
- 统计按请求实际命中的 `group_id`、`account_id` 和当时的统一授权 ID 记录，历史不会因后续团队成员或分组账户变化而重算。
## 账户套餐到期

`accounts.account_expires_at` 是本地账户套餐/购买到期时间，适用于 OpenAI OAuth 和 OpenAI API Key 账户。它和 OAuth 凭据里的 `expires_at` 分离：`credentials.expires_at` 来自 OpenAI token 的 `expires_in`，只表示 access token 过期时间。

- 字段为空表示不设置本地套餐到期。
- 创建或编辑账户时如果填入过去时间，账户立即保存为 `disabled` 且 `schedulable = 0`。
- 列表、调度和相关恢复入口会先处理已过期账户，过期后写入“账户套餐已过期，已自动停用”。
- 网关选号只处理未到期账户；OAuth 额度快照由真实网关请求被动更新，已到期账户不会进入真实调度。

## OpenAI OAuth 额度快照

OpenAI OAuth 的 `5h` / `7d` 额度进度是账号运行态快照，不属于本地 token / 成本统计。快照按 `system_account_id + account_id + kind` 存储，列表只读缓存。

更新策略：

- 真实网关请求返回 Codex rate-limit 响应头时，直接被动更新 `account_usage_snapshots`。
- 账户测试如果拿到相同响应头，也可以作为副作用更新快照，但 UI 不把测试描述成“刷新用量”。
- OAuth 账户创建成功后不主动发模型请求获取额度；首次真实请求或账户测试返回相关响应头后才出现快照。
- 独立 background worker 不再注册 OAuth 额度快照主动刷新任务。
- AI 账户管理页更多菜单不提供“刷新用量”按钮，快照缺失时显示“暂无快照”或“等待真实请求更新”。

当前字段：

- `system_account_id`
- `account_id`
- `kind`：当前固定为 `openai_codex`
- `source`：`gateway`、`gateway_error`、`account_test`
- `snapshot_json`
- `refresh_status`：`fresh`、`pending`、`failed`、`rate_limited`
- `last_attempt_at`
- `last_success_at`
- `next_refresh_after`
- `last_error_message`
- `created_at` / `updated_at`

后台规则：

- 后台 worker 只保留 OAuth Access Token 预刷新，不再为了额度快照发起模型请求。
- 快照状态中的 `last_attempt_at`、`next_refresh_after` 和 `last_error_message` 是当前排障字段；额度快照链路只做被动更新，不再维护后台主动刷新退避。
- 收到 OAuth Codex 429 时，网关仍可从真实响应 header 或响应体里解析 reset 时间，并由账号错误处理链路写入限流或临时不可调用状态。

## 错误兜底策略

账户添加和编辑优先维护账号自己的 `credentials.error_handling_rules`。命中内嵌账号错误规则时，网关按规则写入限流、临时不可调用或异常状态；异常状态统一落到 `status = error`，异常类型写入 `last_error_code`。停用和异常都是不可调度硬状态，网关异步成功/失败回写、流熔断、冷却写入和 OAuth 刷新成功都不能自动恢复或降级覆盖这两个状态；异常只能通过显式“恢复异常”清理。非 2xx 上游错误响应不再经过代码内置特征规则提前短路；未命中账号规则的上游非成功响应先按系统短重试配置同账号重试，重试仍失败后作为待确认失败，不立即写入账号状态。后续账号请求成功时，前序待确认失败仅记录并进入来源级短期回避，不写入全局临时不可调用；只有两个候选账号时，两个不同账号同签名即可确认请求级失败；有第三个可用候选账号时，两个账号同签名继续探测第三个，第三个也同签名才原样透传上游错误给客户端。已确认的同源同请求短时间重复出现时，可命中进程内请求级错误签名缓存并原样返回最近确认过的上游错误；该缓存不写入 SQLite，不写账号状态，不进入 IP 级错误熔断。完整流程见 [网关异常重试与兜底策略](网关异常重试与兜底策略.md)。凡是决定放行给客户端的上游响应，都必须透传上游状态码、可透传响应头和原始响应体，不改写、不包装为网关自有错误格式。

## 默认运行策略

全局设置默认写入：

- `appName = 聚合 AI`
- `appIcon = /__aisys__/brand-icon.svg`
- 全局设置只保存系统名称和系统图标路径，只有管理员可修改；登录页标题由系统名称派生，角标、副标题和首页样式按 [产品与品牌边界](../architecture/frontend/产品与品牌边界.md) 固定。

系统设置默认写入：

- `defaultTemporaryUnschedulableMinutes = 5`：临时不可调用恢复流程中的最大单次暂停时间；账号进入 `temporary_unavailable` 后先 3 秒快速恢复，连续失败后翻倍，慢速恢复单次等待不超过该上限。
- `temporaryUnschedulableRetryIntervalSeconds = 3`：进入临时不可调用前的默认短暂重试间隔。
- `temporaryUnschedulableRetryAttempts = 3`：未知失败进入切换账号前的默认短暂重试次数。
- `streamCircuitBreakerEnabled = true`：流熔断默认开启。
- `streamRequestTimeoutSeconds = 180`：流式请求首段上游内容前的总熔断时间；超过该时间没有收到任何上游 chunk，网关补发 `response.failed` 并结束本次 SSE。
- `streamIdleTimeoutSeconds = 60`：流式响应首段内容后，没有任何上游 chunk 的输出停顿上限；只把 raw chunk 完全停顿作为硬超时，持续有 raw chunk 但暂未形成完整 SSE 事件时只记录诊断并继续转发。
- `streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：流式响应异常的轻量阈值。
- `statsAggregationIntervalSeconds = 60`：统计缓存默认增量汇总间隔。
- `statsAggregationBatchSize = 2000`、`statsAggregationMaxBatchesPerRun = 5`：统计缓存每轮聚合批量上限；连续批次之间让出事件循环，额度小时窗口随本任务刷新，排行和概览窗口快照由独立 worker 任务刷新。
- `groupAccountStatsRefreshIntervalSeconds = 60`：分组账户统计缓存默认刷新间隔。
- `systemMetricsSampleIntervalSeconds = 30`：系统监控默认采样间隔。
- `tableMonitorMaxTablesPerRun = 4`：表监控每轮每个库最多刷新多少张表级快照；设置为 `0` 时只采样文件级指标。后台表级采样只读取本轮表和索引大小，并通过 `dbstat` 叶子页 cell 数滚动写入可推导的行数，不执行精确 `COUNT(*)`。
- `modelCheckRetentionDays = 30`：模型检测历史和诊断明细默认保留 30 天。
- `usageRecordRetentionDays = 7`：自动保留任务默认保留使用记录 7 天，并等待统计游标已处理；表监控手动清理只强制保留最近 1 天。
- `usageStatsMinuteRetentionHours = 48`：分钟级统计缓存默认保留 48 小时。
- `usageStatsHourlyRetentionDays = 60`：小时级统计缓存默认保留 60 天，覆盖 AI 性能最近 31 天小时趋势和边界回查。
- `usageStatsDailyRetentionDays = 400`：日级统计缓存默认保留 400 天，覆盖一年内自然日查询和月表重建。
- `usageStatsWeeklyRetentionWeeks = 104`：周级统计缓存默认保留 104 周。
- `usageStatsMonthlyRetentionMonths = 24`：月级统计缓存默认保留 24 个月。
- `usageRankSnapshotRetentionDays = 30`：常用 TopN 排行快照默认保留 30 天。
- `systemMetricsRetentionDays = 7`、`systemMetricsHourlyRetentionDays = 30`：系统监控原始采样默认保留 7 天，小时汇总默认保留 30 天。
- `dataRetentionCleanupBatchSize = 10000`、`dataRetentionCleanupMaxBatchesPerRun = 10`：统一表数据清理任务的批量上限。
- OAuth 额度快照不再有后台主动刷新默认项；快照只由真实网关响应头和账户测试副作用被动更新。
- `operationLogEnabled = true`：默认启用操作日志。
- `operationLogRetentionDays = 365`：操作日志默认保留 365 天。
- `operationLogMaxChangesPerRecord = 100`：单条操作日志最多保存 100 个字段差异，超过后折叠摘要。
- `accountQualityRefreshIntervalSeconds = 600`：账号质量缓存默认每 10 分钟刷新一次。
- `accountQualityWindowMinutes = 10`：真实网关请求首 token 统计窗口默认 10 分钟。
- 账号质量主动探测相关默认设置已删除，不允许通过系统配置恢复。
- `cooldownAccountRetestMaxBackoffHours = 24`：冷却账号后台复测最长自动恢复观察，默认 24 小时；从账号进入 `temporary_unavailable` 开始计时，进入慢速恢复后若复测失败发生在该观察窗口之后则转异常并停止自动复测。后台冷却复测固定启用，只用于冷却到期后的恢复性测试；请求形态只学习真实流量的低风险元信息，不复用用户请求内容。
- `cooldownAccountRetestIntervalSeconds = 3`：冷却账号后台复测默认扫描间隔，用于承接 3 秒起步的快速恢复通道。
- `cooldownAccountRetestBatchSize = 10`：默认每轮最多恢复性复测 10 个冷却到期账号。

原始审计日志与保全策略：

- 固定启用原始审计日志，不提供系统设置开关。
- 完全成功请求默认只按稳定桶采样 `10%` 进入原始审计；未命中采样时不写 `audit_logs`。
- 每次请求事实由 `usage_records` 保底，原始审计不替代使用记录。
- 失败、异常、重试后成功、客户端中断和流式中断默认进入原始审计，并按策略保全可捕获正文。
- 正文 blob 压缩后按原始 hash 精确去重，并通过 payload 引用关联到事件。
- 重复错误按短时间窗口聚合展示，但每次 occurrence 仍由 `audit_logs` 事件追溯。
- 审计队列固定每 `5` 秒批量落库一次，单批最多 `50` 条客户端请求。
- 待写队列最多保留 `1000` 条终态审计请求，近似最大原文体积为 `256MB`。
- 单个请求活跃捕获上限为 `64MB`，超过后丢弃整条审计记录。
- 默认固定保留：成功样本 `7` 天，失败 / 异常事件 `30` 天，失败 / 异常正文随事件引用分层清理，错误聚合组 `30` 天；具体以 [审计日志保全策略设计](审计日志保全策略设计.md) 为准。

系统设置接口只返回并写入当前白名单内的键；本地库如果残留不在白名单内的旧键，直接通过备份后清洗或重建库处理。源码不保留启动清理分支，运行时也不会读取这些旧键。

## 系统账户隔离补充

- `accounts`、`system_teams`、`system_team_members`、`resource_authorization_grants`、`resource_authorizations`、`resource_authorization_sources`、`groups`、`group_accounts`、`api_keys`、`error_policies`、`usage_records`、`operation_logs`、`operation_log_targets`、`operation_log_viewers`、`audit_logs`、`account_usage_snapshots` 都按 `system_account_id` 或明确的 owner/grantee/viewer 字段隔离；`system_settings` 当前按默认管理员作用域保存系统级运行策略。
- `usage_stats_*`、`usage_model_*`、`usage_error_*`、`usage_latency_*` 和各类窗口 / 排行快照也必须按 `system_account_id` 或全局虚拟账户明确隔离。
- `providers`、`proxy_profiles`、`global_settings`、`system_metrics_samples`、`system_metrics_hourly`、`process_event_loop_samples`、`process_event_loop_hourly`、`process_event_loop_trend_windows` 保持全局共享；`providers` 和 `proxy_profiles` 只允许管理员维护，主机级系统监控默认仅管理员可见。进程事件循环趋势固定按最近 24 小时分钟峰值桶展示，不按系统账户隔离。`proxy_profiles.latency_ms`、`outbound_ip`、`outbound_region`、`test_status`、`last_tested_at` 和 `last_test_message` 是代理最近检测缓存，不参与账号调度事实判断。
- 管理员可以读取所有系统账户的数据；普通用户只读取自己的系统账户数据，以及其他用户主动授权给自己的 AI 账户和分组使用摘要。
- 原始审计日志虽然带有 `system_account_id`，当前仍仅管理员可读取；普通用户不能通过审计日志接口查看自己的完整原文请求。
- 操作日志按 `operation_log_viewers.system_account_id` 和 `visibility_scope = 'all_users'` 控制普通用户可见范围；管理员可读取全部操作日志。

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码
- 操作日志中涉及敏感字段的变更详情

这是单人自用系统，自有 API Key 列表接口会按权限返回前端需要展示和复制的完整本地密钥；授权账户和授权分组接口不能返回完整密钥，只能返回列表摘要和必要状态。数据库中仍通过 `key_secret_encrypted` 密文保存本地 API Key，`key_hash` 用于网关校验，`key_prefix` 用于摘要展示；API Key 列表通用搜索只按名称匹配。

历史缺少 `key_secret_encrypted` 的旧 API Key 行无法反解完整密钥，只能展示前缀并提示重新创建。

API Key 额度配置不属于敏感凭据，保存在 `api_keys.quota_limits_json`：空值表示不限制，JSON 内 `limit` 表示美元金额；日额度按服务端本地自然日 0 点重置，周额度按周一 0 点重置，月额度按每月 1 号 0 点重置，总额度读取累计 `total_cost_usd` 缓存。网关只读取 API Key 维度统计缓存判断美元成本额度，不回扫明细表，也不做实时扣减。

更完整的凭据展示、请求快照、操作日志、原始审计日志、日志脱敏、数据保留和备份迁移规则见 [安全与日志策略](安全与日志策略.md)、[操作日志设计](操作日志设计.md) 与 [原始审计日志设计](原始审计日志设计.md)。
