# SQLite 存储说明

## 为什么用 SQLite

当前项目只给个人使用，不需要复杂部署、水平扩展或极限并发。SQLite 足够稳定，文件备份也简单，更符合轻量项目定位。

## 默认位置

后端运行时按业务库、统计数据集域和统计结果库三个职责组织 SQLite。统计数据集域由数据集目录库和 usage shard 文件组成；未配置统计数据集目录库、统计结果库或 usage shard 路径时，直接使用各自默认位置：

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
JUHE_AI_USAGE_SHARD_ROOT=./data/usage-shards
JUHE_AI_USAGE_SHARD_COUNT=16
```

相对路径按 `backend/` 目录解析。为了保持可移植部署，推荐使用 `./data/juhe-ai.sqlite3`、`./data/juhe-ai-dataset.sqlite3`、`./data/juhe-ai-stats.sqlite3` 和 `./data/usage-shards` 这类项目内相对路径。三个 SQLite 文件路径必须互不相同，usage shard 根目录也必须与这些文件路径区分；如果确实要把数据放到项目外，也可以填写当前操作系统支持的绝对路径。

搬到其他电脑或服务器时，保留 `backend/.env` 和当前数据目录即可带走配置与数据；如果本地库结构和当前 schema 不一致，按当前 schema 离线修复或重建，不在运行时代码里放结构适配分支。

## 业务库、统计数据集域与统计结果库边界

业务库只保存可恢复的核心业务数据：

- `system_accounts`、`system_sessions`
- `global_settings`、`system_settings`
- `providers`、`protocols`、`protocol_endpoint_families`、`provider_protocol_profiles`、`provider_protocol_profile_families`、`proxy_profiles`
- `accounts`、`account_supported_models`、`account_test_tasks`、`custom_provider_models`、`groups`、`group_authorization_settings`、`group_accounts`、`group_account_stats_dirty`、`api_keys`、`api_key_group_bindings`
- `system_teams`、`system_team_members`
- `resource_authorization_grants`、`resource_authorizations`、`resource_authorization_sources`
- `external_integration_sources`、`external_integration_source_tokens`
- `announcements`、`announcement_reads`

`system_accounts.image_generation_enabled` 保存系统账户是否允许发起图像生成，默认 `0`。该字段不是敏感正文，也不进入系统账户 options 轻量查询；网关运行态通过 API Key 校验行携带这个布尔值，禁用图像生成时不额外反查系统账户表。

统计数据集域保存高增长、可丢失、可过期或排障类事实数据；其中数据集目录库保存未分片明细和元数据，新写入的 `usage_records` 保存到 usage shard 文件：

- `usage_records`（usage shard 文件）
- `audit_logs`、`audit_log_attempts`、`audit_payload_blobs`、`audit_payload_refs`、`audit_error_groups`
- `public_api_logs`
- `operation_logs`、`operation_log_targets`、`operation_log_viewers`
- `runtime_logs`、`runtime_log_file_cursors`
- `model_check_runs`、`model_check_items`
- `api_key_record_cleanup_targets`

统计结果库保存可重建、紧凑且面向查询的结果数据：

- `usage_stats_*`、`usage_model_*`、`usage_error_*`、`usage_latency_*`、`usage_rank_snapshots`、`stats_job_state`、`usage_record_cleanup_deductions`
- `authorization_*_usage_*`、`usage_quota_hourly_windows`、`usage_scope_range_windows`
- `client_ip_registry`、`client_ip_stats_daily`、`client_ip_usage_range_windows`、`client_ip_policies`、`client_ip_policy_hits`
- `group_account_stats`、`account_quality_scores`、`account_quality_minute_stats`
- `account_usage_snapshots`
- `system_metrics_samples`、`system_metrics_hourly`、`system_metrics_trend_windows`
- `process_event_loop_samples`、`process_event_loop_hourly`、`process_event_loop_trend_windows`
- `database_storage_snapshots`、`table_storage_snapshots`

运行时代码不通过 `ATTACH` 跨库查询，也不读取当前 schema 之外的表结构。业务库是恢复的硬边界，必须保留；统计数据集目录库、usage shard 和统计结果库都可以丢弃、清空或重建。需要拆分、清理或取证本地保留数据时，只能使用停机后的显式离线脚本；不关心既有统计和排障明细时，可以直接新建空数据集目录库、usage shard 目录和统计结果库。

## 统计数据集与结果库拆分方案

当前实现已经落地统计数据集域和统计结果库入口。为减少高频数据集写入对统计结果查询的影响，数据集与统计职责按当前模型拆为：

- 统计数据集目录库：保存原始审计、操作日志、运行日志索引、模型检测明细、记录清理目标和 usage shard catalog 等高增长事实元数据；新写入的 `usage_records` 已拆到 usage shard 文件。
- 统计结果库：保存 `usage_stats_*`、`usage_model_*`、`usage_error_*`、`usage_latency_*`、额度窗口、范围窗口、排行快照、授权消耗窗口、账号质量结果、分组统计、系统监控采样、监控趋势、`stats_job_state` 和使用记录清理扣减账本等紧凑结果。

页面统计、列表用量和网关额度只读统计结果库；明细排障通过 repository 读取 usage shard 和数据集目录库中的其他明细；后台 worker 从 usage shard 按分片游标读取事实，并在统计结果库事务中写入结果和推进水位。既有明细如果需要取证，只能停服务后用一次性离线脚本处理，处理逻辑不得进入源码主路径。

## 数据集库分片写入设计

三库拆分解决了业务库、数据集目录库和统计结果库之间互相拖慢的问题，但不能消除单个数据集目录库 SQLite 文件内部的单 writer 上限。当前高频写入优化以 [数据集库分片写入设计](数据集库分片写入设计.md) 为准，先只拆最热的 `usage_records`：

- `JUHE_AI_DATASET_DATABASE_PATH` 继续作为数据集目录库，保存审计、操作日志、运行日志索引、模型检测和 shard catalog。
- 新增 `JUHE_AI_USAGE_SHARD_ROOT`，未配置或留空时默认跟随数据集目录库所在目录生成 `usage-shards`；生产也可以显式配置为 `./data/usage-shards` 或其他独立目录。
- 新增 `JUHE_AI_USAGE_SHARD_COUNT`，默认 `16`。
- 新写入的 `usage_records` 按 `bucket_date + stable_hash(id) % shardCount` 路由到多个 SQLite shard 文件。
- 当前实现复用 background worker usage 队列，在 worker 内按 shard 分组并对每个 shard 执行短事务；独立 child writer pool 作为后续性能增强。
- 统计结果库仍不分片；统计 worker 改为按 shard 独立游标读取 usage，再在统计结果库同事务写结果和推进水位。
- 使用记录详情通过 usage id 中的日期和 shard 信息直接定位；usage id 必须携带可定位 shard 的日期信息。
- 使用记录列表由 repository 内部跨 shard 有界读取并稳定合并，不做全 shard 精确 `COUNT(*)`。
- 使用记录账户名筛选不在 usage shard 上做 `LIKE`。后端先在业务库按账户名称精确 / 前缀匹配解析最多 200 个当前作用域可见的实际 `account_id`，再对 usage shard 使用 `account_id IN (...)`。对授权场景，解析必须覆盖被授权人的授权实例名称、授权实例来源账户名称，以及分组授权来源分组内账户名称；这样被授权用户用来源账户名查询时能命中自己的使用记录，同时不会越过 `usage_records.system_account_id` 看到授权方或其他调用方明细。
- 数据保留优先按 shard 文件删除或归档旧数据，避免大表批量 `DELETE` 和 freelist 膨胀。

分片设计不改变外部 API 响应、统计结果口径、API Key 额度读取口径或统一授权额度口径。业务主库必须保护；统计数据集目录库、usage shard 和统计结果库可以按需要删除重建。

## 当前实现

- 使用 Node 内置 `node:sqlite`，要求官方 Node.js LTS；当前支持 22.x LTS（>=22.13.0）或 24.x LTS（>=24.11.0），且内置 `node:sqlite` 必须可用。
- 启动时自动建表
- 启动时自动写入默认超级管理员账号、OpenAI v1 协议、`openai` 通用供应商、`gpt` 子供应商、`profile_openai_openai_v1` 与 `profile_gpt_openai_v1` 供应商协议档案、默认 OpenAI 兼容分组、默认 GPT 分组、默认全局设置和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 每个 SQLite 连接必须设置短暂写锁等待时间，避免 DB service、background worker 和管理面低频写操作短事务重叠时立即返回 `database is locked`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据
- 系统管理 API、登录态校验、管理面 CRUD、客户请求链路中的高频 SQLite 读写、公开设置读取、运行日志索引查询、账号错误状态副作用、OAuth Access Token 刷新持久化和 OAuth Codex 额度快照写入，都通过独立本地 DB service 进程完成；主 Web 进程只代理 `/__aisys__/api/*`，不解析管理 API JSON body，不直接导入管理路由或 repository。DB service 不改变 SQLite 单写者模型，DB service 不可用时请求返回可读错误，不能回退到主 Web 进程本地同步执行。
- IP 封禁命中计数只在 server 进程内做短暂有界聚合后投递 DB service：待写 distinct `ip_hash + policy_id` 最多 `5000` 个，单次 flush 最多 `1000` 条，满载时丢弃新的 distinct 命中并计数，不能让恶意多来源封禁流量形成无界 Map 或一次大 IPC。
- 业务库通过 `JUHE_AI_DATABASE_PATH` 打开；数据集目录库通过 `JUHE_AI_DATASET_DATABASE_PATH` 打开；统计结果库通过 `JUHE_AI_STATS_DATABASE_PATH` 打开。三类入口都使用 WAL，并且三个路径必须互不相同。
- 使用记录按每次上游尝试写入 usage shard；`usage_records.client_ip` 只保存规范化 IPv4，非 IPv4 来源写空。server 角色只把使用记录投递给 background worker IPC 队列，不在 worker 未就绪时回落到主进程本地队列或同步写库。失败记录保存 `request_snapshot_json` / `response_snapshot_json`，用于前端查看请求与返回日志
- 操作日志使用独立表保存已成功提交的业务状态变更，用于追溯系统账户对资源的增删改、启停、绑定、授权和配置变更；查询请求不写操作日志。
- 公开接口日志使用 `public_api_logs` 保存 `/__aipublic__` 外部来源系统调用元数据、状态码、耗时、客户端 IP、trace ID、有限请求 / 响应快照和错误摘要；请求 / 响应快照按原文保存并在超限时截断，最大保留 7 天，由后台数据保留任务分批清理。
- 管理端写操作需要按 [幂等与唯一约束设计](幂等与唯一约束设计.md) 接入防重复提交和业务唯一约束：前端重复点击或网络重试不应创建多条业务数据，重复提交拦截不写第二条操作日志；防重复提交缓存属于进程内易失状态，过期维护固定小批量轮转，容量淘汰不全量展开排序。
- 原始审计日志使用独立表保存完全成功请求的 10% 稳定样本，以及失败、异常、客户端中断、流式中断和重试后成功链路；请求 / 响应正文按 [审计日志保全策略设计](审计日志保全策略设计.md) 压缩、去重并通过 payload 引用保存，server 角色只能终态投递 background worker IPC 队列，后台批量写库，不能同步写审计表，也不能在 worker 未就绪时本地落库。
- 普通运行日志仍以 JSON Lines 写入日志文件并滚动清理；最近 3 天的索引查询只使用数据集目录库表 `runtime_logs`，后台 worker 通过 `runtime_log_file_cursors` 记录当前日志文件读取游标，只追新增内容，不在启动时全量扫描当前日志文件；管理后台索引查询和 facets 读取经 DB service 完成，不在主进程同步读取 SQLite 索引。运行日志不再维护额外搜索影子表，关键字只在 `runtime_logs.message` 列做普通模糊匹配，完整日志正文搜索交给 `grep 模式`。
- 系统团队、团队成员和统一资源授权使用独立表记录；账户授权会为被授权用户创建独立授权实例账户，授权资源调用时使用记录按实际调用方隔离，同时冗余资源所有者、授权关系和授权对象用于聚合统计。
- `protocols` 保存协议族和版本，当前为 `openai/v1`；`protocol_endpoint_families` 保存协议下的端点族，当前包含 `chat_completions` 和 `responses`；`providers` 保存供应商身份和父子关系，当前为通用 `openai` 供应商和 `gpt.parent_code = openai` 子供应商；`provider_protocol_profiles` 把供应商绑定到协议版本并保存默认 `base_url`、默认测试模型、账户类型和能力，当前默认档案为 `profile_openai_openai_v1` 和 `profile_gpt_openai_v1`；`provider_protocol_profile_families` 保存档案启用的端点族能力。
- `accounts.provider_protocol_profile_id`、`groups.provider_protocol_profile_id` 和 `account_test_tasks.provider_protocol_profile_id` 保存供应商协议档案；`protocol_code` / `protocol_version` 从档案冗余写入，用于运行时策略、审计和排障。账户只能加入同 `provider_protocol_profile_id` 的分组；分组名称唯一、默认分组唯一和 API Key 号池绑定兼容性都按协议档案维度判断，不能只按 `provider_code` 判断。
- 账户级错误处理策略保存在 `accounts.credentials.error_handling_rules`，用于描述该账户上游非 `2xx` 错误命中后的账号副作用。规则保存启用状态、名称、优先级、状态码 / 错误码 / 错误类型 / 关键字匹配条件、动作和限流恢复策略。请求头、请求体和上下文改写不进入该字段，仍由网关 adapter 内部处理。
- `stream_intercept_policies` 保存管理端流式拦截策略。`scope_type = protocol` 时 `provider_code` 为空，表示当前 `protocol_code` 下的协议层规则；`scope_type = provider` 时必须写入同一协议下已启用的 `provider_code`，表示 GPT、DeepSeek 或其他 OpenAI v1 兼容供应商的局部规则。运行时只在 API Key 校验和分组选定后按当前 `protocol_code + provider_code` 读取候选策略；账户追加规则仍写在账户凭据 `stream_intercept_rules`，不额外保存协议或供应商字段。
- `accounts.system_account_id` 和 `groups.system_account_id` 表示当前资源行所属系统账户；授权实例账户的 `accounts.system_account_id` 是被授权用户，`authorization_instance_source_account_id` 记录来源账户，`authorization_instance_authorization_id` 指向用户级授权，`authorization_instance_owner_system_account_id` 记录原资源归属人。`group_accounts.system_account_id` 表示本地分组绑定所属的使用方系统账户；授权账户绑定到被授权用户分组时写入授权实例账户 ID 和稳定的 `account_authorization_id`。授权实例自己的 `accounts.status / schedulable / cooldown_until / last_error_* / stream_failure_*` 是被授权侧本地运行态；当前分组内排序、超级优先和降级备用以 `group_accounts.local_priority / local_super_priority_enabled / local_fallback_enabled` 为准；真实上游资源事实从来源账户补齐，包括凭据、`base_url`、账号类型、支持模型、代理、并发、可用时段和账户追加流式规则。归属人原账户停用、异常、限流 / 临时不可调用、冷却、关闭调度、套餐到期或测试失败不会覆盖或回写授权实例本地运行态，但会参与授权实例 `effectiveAvailability` 实际可用性计算并阻断调度、账户测试和迁移目标选择；来源账户资源配置变化会同步影响授权实例运行时。来源账户被逻辑删除时，来源账户及其授权实例都会立即隐藏且不可调度，对应授权关系转为已回收；授权实例账户不能通过账户删除接口删除，被授权人不想继续使用个人直授权时通过授权归还入口把个人授权标记为 `returned` 并隐藏该实例。
- `accounts.account_expires_at` 保存可选的本地套餐/账号购买到期时间；为空表示不过期，到期后账户自动改为停用并退出调度。
- `accounts.availability_schedule_json` 保存账户可用时段计划；为空表示不限制时段。该字段只作为网关账号候选过滤和列表展示事实，不驱动 `accounts.status` 自动改写。时段外账户不进入网关候选，系统时区由后端统一默认值决定，前端不暴露用户时区配置。
- `accounts.last_error_code` 保存账户异常子类型；顶层状态仍统一使用 `status = error` 表示“异常”，可读细节继续放在 `accounts.last_error_message`。
- `accounts.last_successful_test_model` 保存该账户最近一次手动账户测试通过时使用的模型；后台系统复测优先使用该模型，没有手动成功记录时使用供应商协议档案 `provider_protocol_profiles.default_test_model`。
- `account_test_tasks` 保存手动 AI 账户测试任务的轻量状态，任务由管理 API 创建并投递给 background worker 执行；表内只保留任务发起人、管理筛选作用域、账户摘要、状态、取消标记和最终脱敏结果，创建 / 编辑弹窗发起的未保存账户测试会额外保存加密草稿快照 `draft_account_encrypted`，默认只保留已完成任务 24 小时。前端手动测试和批量测试只能查询该表的任务状态，不在管理 API 请求链路等待上游测试完成。
- `accounts.cooldown_retest_failure_count`、`cooldown_retest_observation_started_at`、`cooldown_retest_last_at` 和 `cooldown_retest_last_status_code` 保存 `temporary_unavailable` / `rate_limited` 后台复测的连续失败次数、本轮自动恢复观察起点、最近复测时间和最近 HTTP 状态；复测失败时 `last_error_code/last_error_message` 记录本次上游真实错误摘要，复测成功、手动恢复、停用或到期时清空。进入慢速恢复后仍继续自动退避复测；超过 `cooldownAccountRetestMaxBackoffHours` 表达的最长自动恢复观察窗口后写入 `status = error` 和 `last_error_code = cooldown_retest_max_recovery_exceeded`，停止自动复测并等待人工“恢复异常”。
- `account_supported_models` 保存账号显式支持的模型列表；账号没有任何模型行表示不限制。网关账号池缓存 miss 时按账号 ID 批量读取这些行，并把结果放入运行时账号快照，正常请求只做内存过滤，不逐次查询该表。授权实例调度和列表补数只读取来源账户的模型列表，实例行不保存模型快照。
- `custom_provider_models` 保存管理员全局自定义模型和用户个人自定义模型。`scope = global` 的模型不绑定 `system_account_id`，所有系统账户可见；`scope = personal` 的模型只对对应 `system_account_id` 可见。`visibility = mapping_target_only` 只供账号模型映射选择，不进入 `/v1/models`；`/v1/models` 对外只输出 OpenAI 标准字段，不暴露价格、scope、visibility、`pricingModel` 或备注。
- 高并发分组已使用 `groups.group_type` 和 `groups.scheduling_policy_json`：前者保存分组调度类型，默认 `personal`；后者接收最大单账户排队阈值和分组最大等待时间，快速优先、慢请求分流、亲和打破、备用启用和分组级短等待容量都使用代码内置默认开启策略。默认分组使用个人分组语义。
- 高并发分组的单账户排队阈值只来自 `groups.scheduling_policy_json` 里的分组目标配置；账户绑定关系不再保存绑定级权重或绑定级单账户排队阈值。实际调度阈值仍不能突破账号 `concurrency_limit` 硬上限。
- 高并发分组需要的近期质量、超时率、首 token 和总耗时趋势应由 background worker 或请求终态事件增量写入紧凑缓存，网关热路径只读取 DB service 已批量带出的运行时快照，不实时回扫 `usage_records` 或 `account_quality_minute_stats`。跨进程共享指标必须落到 SQLite 缓存表或明确 IPC，不能只放在 worker 进程内存。
- 登录验证码挑战暂不写入 SQLite，使用后端进程内存保存短时一次性验证码；过期和已提交的挑战会被清理，过期维护只做节流后的固定小批量扫描，容量淘汰不展开全部 challenge 排序。
- 登录失败限频和账号临时锁定暂不写入 SQLite，使用后端进程内存保存短时窗口和锁定状态；单个 IP / 用户名只保留阈值内最近失败时间戳，过期维护固定小批量轮转，容量淘汰不全量展开排序；后续多实例部署时再迁移到共享存储。
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

敏感和大体积数据不得进入通用 lookup 缓存：API Key 明文、OAuth token、代理密码、完整请求 / 响应 payload、审计正文、日志大字段和可能造成越权的权限判定中间结果，必须走专门的受控读取、权限裁剪和分块策略。

当前轻量缓存优先使用 `backend/src/shared/cache.ts` 的进程内 LRU 封装；多实例或跨进程一致性需求出现前，不引入 Redis 等分布式依赖。缓存只用于降低请求链路反查成本，事实源仍是 SQLite 表、统计结果库预聚合表或网关运行态事实。

## 大文件与频繁读取底线

数据集目录库会持有日志索引、审计 payload 和原始监控采样等持续增长数据，usage shard 会持有使用记录。任何运行路径只要涉及大文件或高频文件读取，都必须按 offset / cursor / stream / 分块窗口推进，不能先全量读入内存再 `split`、过滤、排序或分页。

- 运行日志索引通过 `runtime_log_file_cursors` 保存文件 offset、行号和文件标识；worker 重启后从游标继续，首次遇到已有当前日志文件时默认从文件末尾开始，避免导入历史大文件。
- 按行读取只在完整换行结束后推进 offset；末尾半行保留到下一轮，轮转、截断或文件标识变化时重置游标。
- 审计 payload blob 详情接口只能按 offset / limit 返回有限窗口；未压缩 blob 使用文件 offset 读取，gzip blob 通过解压流跳过到逻辑 offset 后只收集当前窗口，接口返回 `bodyOffset`、`bodyLimit`、`bodyTotalBytes`、`bodyNextOffset` 和 `bodyTruncated`。超过单次最大读取窗口的 payload 默认保持未压缩，避免读取后半段时反复从头解压 gzip。
- 使用记录、操作日志、原始审计日志、审计错误组和运行日志索引这类高增长列表，默认只读取当前页 `pageSize + 1` 条来判断 `hasMore`，不在请求路径执行全量 `COUNT(*)`；返回的 `total` 只是前端分页器上界值，不代表精确全表总数。
- 小 `.env` 配置、极小系统状态文件、测试 / 回归脚本、明确有大小上限的网关 raw body 或诊断响应捕获可以作为例外；系统管理 API 和公开系统 API 由 DB service 承载，JSON 请求体硬上限为 `256KB`，超过上限直接返回 413，不能把大体积管理 payload 交给 DB service 事件循环同步解析。`/v1` raw body 当前入口硬上限为 `64mb`，认证预检和可提前识别的图像权限拒绝通过后才读取 body；文本 lane 业务上限为 `8mb`，图像生成 lane 保留 `64mb` 上限。网关 JSON body 小于等于 `256KB` 时可主线程内联解析；超过 `256KB` 且不超过入口硬上限时进入 worker thread 做顶层元数据扫描，其中超过 `2MB` 会按大 JSON 请求记录预警；只有 OAuth Codex 归一化、禁用图像权限下移除 optional `image_generation` 工具等必须改写请求体的路径才完整解析；使用记录请求快照只保存体积摘要，不能为了快照把完整大请求体再写入明细表，完整原始内容由原始审计按策略捕获；例外不得用于运行日志、审计 payload、使用记录导出或统计明细。

## 统计缓存与监控存储

个人或几个人使用时，统计缓存优先放在统计结果库内，不额外引入 Redis、ClickHouse 或 Prometheus。`usage_records` 仍是统计数据集域事实源，当前新记录保存到 usage shard 文件；账户列表、分组列表、用户统计概览、管理员统计概览和监控图都应读取统计结果库缓存表。缓存表必须按 `system_account_id` 隔离；用户统计概览只读取当前用户缓存，管理员统计概览默认读取全局缓存并支持筛选指定系统账户，主机级系统监控只给管理员看。

统计底线：所有业务汇总都只能由后台 worker 或离线重建脚本按游标增量计算；管理 API、前端页面、详情接口和下拉接口只能直读已经预聚合好的 staged / window / summary 行。请求路径不得为了展示临时 `SUM/GROUP BY`，也不得把明细行或缓存桶再相加成页面汇总。统计缓存是可丢弃数据，表结构不匹配时按当前口径重建和改造。

当前统计相关表：

- `usage_stats_totals`：按 `system_account_id + scope_type + scope_id` 保存累计请求、成功、错误、token、缓存成本、总成本、平均总耗时所需的求和字段。
- `request_quota_hourly_window_configs`：业务库小表，按 `window_hours` 保存已启用过的滚动小时额度窗口；默认写入 `1/3/6/12/24/72/168/720`，API Key / 统一授权额度写路径登记自定义窗口，统计 worker 刷新 `usage_quota_hourly_windows` 时只读该表，不扫描全部额度 JSON。
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
- `usage_scope_range_windows`：按 `system_account_id + scope_type + scope_id + start_date + end_date` 保存最近 31 天范围内的范围总量，并包含成功数、错误数、耗时、首 token、活跃天数和最后错误时间等账号维度范围指标。用量统计、授权详情和公开账号用量接口只按范围 key 直读；账号用量页关键词先在业务库用账号 ID、名称、供应商、类型的索引范围查询解析为账号 ID，再用 `scope_id` 命中窗口表，不能在统计结果窗口查询中拼业务字段多列 `LIKE`，也不能在请求链路临时 `GROUP BY usage_stats_daily`。
- `client_ip_registry`：按 `ip_hash` 保存来源 IPv4 注册事实，包括 `aggregate_ip_key`、最近样本 IP、IP 版本、首次出现、最近出现和分桶号；当前 IP 管理只写入 IPv4，`aggregate_ip_key` 与规范化 IPv4 一致。该表只由后台 IP 统计 job 注册和更新，页面不从 `usage_records` 扫描 IP；管理页最后使用日期筛选和“最后使用”列以 `last_seen_at` 为准。
- `client_ip_stats_daily`：按 `ip_hash + stat_date` 保存 IP 自然日请求数、成功数、失败数、Token、缓存成本、总成本、首 token / 总耗时样本和、总耗时最大值和最近使用 / 最近错误时间。
- `client_ip_usage_range_windows`：按 `ip_hash + start_date + end_date` 保存最近 31 天内 IP 范围窗口，系统运维 / IP管理 列表和 `/__aipublic__/ip/usage` 外部来源接口只读该窗口，不在请求路径聚合明细、重建窗口或计算范围总统计。IP 速度展示只读取窗口内已落表的平均首 token、平均总耗时和最大总耗时字段，不新增实时明细扫描。
- `client_ip_policies`：保存管理员显式创建的 IP 封禁策略，解封或替换策略只把旧策略改为 `disabled`，不删除历史。
- `client_ip_policy_hits`：按 `ip_hash + stat_date + policy_id` 保存网关封禁命中次数和最近命中时间；网关先写进程内缓冲，再异步批量写库。
- `usage_model_daily`：按 `system_account_id + stat_date + model` 保存请求数、Token 和成本，用于自然日模型分布。
- `usage_model_hourly`：按 `system_account_id + stat_hour + model` 保存小时级模型分布，用于统计概览监控窗口。
- `usage_error_daily`：按 `system_account_id + stat_date + error_group + error_code` 保存错误数量，用于自然日错误情况。
- `usage_error_hourly`：按 `system_account_id + stat_hour + error_group + error_code` 保存小时级错误数量，用于统计概览监控窗口。
- `group_account_stats`：按 `system_account_id + group_id` 保存分组绑定账户数量、可用数、状态数量和并发上限，供分组列表直接读取；写路径只标记脏分组，worker 随 `groupAccountStatsRefreshIntervalSeconds` 刷新脏分组，必要时才全量重建。
- `group_account_stats_dirty`：业务库中的分组统计刷新队列，按 `group_id` 记录待刷新的分组统计缓存，来源包括账户状态 / 调度属性变化、分组绑定变化、团队和授权关系变化；请求链路不得同步重建 `group_account_stats`，也不得为了标记脏缓存写统计结果库。影响面无法精确收敛时只能写入 `__all__` 全量哨兵，worker 消费哨兵时按 `groups.id` 游标固定批次刷新并把游标写回 `reason = all_cursor:<group_id>`，禁止在写请求或单轮 worker 中先 `SELECT id FROM groups` 再逐分组展开。
- `system_metrics_samples`：按采样时间保存 CPU、内存、RSS、Heap、网络入站/出站吞吐、网卡累计收发、数据库文件大小和统计滞后；`event_loop_lag_ms` 保存后台 worker 采样值，用于主机级概览。多进程事件循环趋势以 `process_event_loop_samples` 为准。
- `system_metrics_hourly`：把采样数据按小时聚合为平均值、最大值和最小值；网络吞吐平均值按有效网络速率样本数计算，避免采样端暂不可用时被按 0 稀释。
- `system_metrics_trend_windows`：按统计概览日期范围预生成系统性能 / 网络吞吐趋势，接口只按范围窗口直读。
- `process_event_loop_samples`：按采样时间和进程角色保存事件循环额外延迟，当前角色为 `server`、`worker`、`db-service`，用于区分主 Web 进程、后台 worker 和本地 DB service 哪个进程卡顿。
- `process_event_loop_hourly`：按 `stat_hour + process_role` 汇总事件循环延迟样本数、平均值和最大值，作为长期粗粒度排障缓存。
- `process_event_loop_trend_windows`：统计概览范围窗口缓存；管理侧事件循环趋势展示固定读取最近 24 小时 `process_event_loop_samples` 并按分钟聚合，避免长范围窗口膨胀，也避免把卡顿尖峰压成天级趋势。
- `database_storage_snapshots`：按采样时间保存业务库、数据集目录库和统计结果库文件大小、WAL / SHM、页大小、总页数、空闲页和表数量；这是 10 分钟常规采样的主指标。usage shard 文件集合观测仍是表监控后续增强项。
- `table_storage_snapshots`：按采样时间保存表级可选行数、表大小、索引大小、总大小和 1 小时 / 24 小时增长；表级数据按游标轮转分批刷新，不要求所有表在同一采样时间都有新快照。后台常规采样通过 `dbstat` 叶子页 cell 数滚动写入可推导的行数，不提供精确 `COUNT(*)` 采样分支；SQLite `dbstat` 不可用或表类型不适合推导时，表大小、索引大小、总大小、页数和行数保持为空，不写入伪造的 0。
- 表监控文件级采样由后台 worker 每 10 分钟执行一次；表级采样默认每轮每个库最多刷新 4 张表，历史默认保留最近一月。
- `stats_job_state`：记录后台任务的作用域、游标、上次成功时间、上次错误和滞后秒数；业务统计作用域为 `system_account`，主机监控作用域为 `global`。IP 范围窗口额外使用 `scope_type = client_ip_range_window` 记录窗口刷新 ready/stale 标记，只表达窗口是否完成刷新，不保存数量或范围总量。任务尚未写入状态或滞后无法判断时，`lag_seconds` 保持为空，不按 0 处理。
- `usage_record_cleanup_deductions`：API Key 关联记录物理清理的统计扣减账本，按 `usage_id + source_shard_key` 记录已扣减但可能尚未完成 shard 删除的使用记录；统计扣减和账本标记在同一个统计结果库事务内提交，用于跨 SQLite 文件清理失败后的幂等续跑。
- 已删除 AI 账户和 API Key 的关联记录清理只由后台 record maintenance worker 执行；用户删除请求只提交业务库删除事实，并登记 dataset 清理目标。清理过程按库边界拆成短事务：usage shard 删除、stats 扣减 / 维度清理 / 窗口刷新、dataset 审计和模型检测删除互不包在同一个长事务里。遇到 SQLite `database is locked` 时，清理目标写入 `last_blocked_reason` 并等待后续重试，不能把可重试锁竞争抛成用户删除失败。
- `operation_logs`：保存业务操作主事件，包括操作人、业务作用域、模块、动作、主资源、安全差异和 trace ID。
- `operation_log_targets`：保存一次操作涉及或影响的资源，支持按资源反查历史操作。
- `operation_log_viewers`：保存普通用户可见性和可见原因，资源删除、授权回收或授权归还后仍按当时关系追溯。
- `operation_log_summary_search_terms`：保存操作日志中文摘要生成的规范化倒排词项，`summaryKeyword` 查询通过 `term + created_at` 索引定位，避免在请求路径扫描 `operation_logs` 主表；资源、操作人、模块、动作和 trace ID 使用独立结构化筛选。
- `public_api_logs`：保存公开接口调用排障记录；列表按 `created_at + id` 固定窗口读取，按 `trace_id`、`source_ref_id`、`path`、`status_code`、`success`、`client_ip` 和时间范围筛选，详情读取按原文和容量上限保存的 `request_data_json` 和 `response_data_json`。
- `audit_logs`：保存进入原始审计的客户端请求事件元数据，用于后台页面检索；完全成功请求默认只采样 10%。
- `audit_log_attempts`：保存审计请求下每次上游尝试、命中账号、代理、状态码和错误摘要。
- `audit_payload_refs`：保存审计事件到 headers/body blob 的引用、part 类型、顺序、hash、大小和保留状态。
- `audit_payload_blobs`：保存客户端请求、上游请求、上游响应和最终网关响应 blob 元数据；可压缩且不超过单次读取窗口的正文会 gzip，超大正文保持 plain 以支持直接 offset 读取；blob 文件落在本地数据目录。
- `audit_error_groups`：保存短时间窗口内重复错误的聚合信息，列表默认可按错误组查看。
- `runtime_logs`：保存最近 3 天普通运行日志的结构化字段和原始 JSON 行，用于管理员“日志搜索”页面。索引查询中的 `keyword` 只对 `message` 列做普通 SQL 模糊匹配，不检索 `raw_json`，也不维护额外搜索影子表。
- `runtime_log_file_cursors`：保存当前普通运行日志文件的读取 offset、行号、文件标识和最近读取状态，worker 重启后从游标继续追增量日志，文件轮转或截断时重置对应文件游标。
- `account_quality_scores`：按 `account_id` 保存账号质量缓存，包括质量分、质量状态、近 10 分钟真实网关首 token 聚合、成功率和最近真实样本时间。该表只作为调度辅助缓存，事实源仍由 `usage_records` 通过统计 worker 增量进入 `account_quality_minute_stats`；账号质量主动探测能力已删除，不保留重新开启的旧设置。刷新任务只消费 `account_quality_dirty_accounts` 中固定数量的脏账号，再按这些账号汇总近窗口分钟桶，stale 推进、失效质量行和分钟桶清理由固定批次滚动完成，不一次性加载全部账号、全部近期样本账号或全部质量缓存。超过 24 小时没有真实样本的质量分不参与调度。
- `account_quality_minute_stats`：按 `account_id + stat_minute` 保存真实网关请求的分钟级质量聚合，由主用量统计 worker 随 `(created_at, id)` 游标递增写入；服务重启后保留，重建时跟随主用量统计游标重新生成。
- `account_quality_dirty_accounts`：账号质量刷新 dirty 账号目录，由主用量统计 worker 在写入或扣减账号质量分钟桶时同步 upsert；刷新成功后删除本轮已消费账号，避免刷新任务从 `account_quality_minute_stats` 临时 `GROUP BY` 全部近期账号。
- `announcements`：保存平台公告，用户侧只读取最近 30 条已发布公告，管理员侧维护草稿、已发布和已下线公告。
- `announcement_reads`：按 `announcement_id + system_account_id` 保存用户已读公告记录，支撑铃铛未读提醒跨刷新、跨浏览器保持一致。

当前索引重点：

- `system_accounts(lower(username))`：保证用户账户大小写不敏感唯一，用户账户创建后不允许修改。
- `system_accounts(lower(display_name))`：保证用户名称大小写不敏感唯一。
- `system_accounts(display_name, id)`：系统账户管理列表按用户名称精确 / 前缀匹配。
- `system_accounts(username, id)`：系统账户管理列表、系统账户选项、授权候选用户 options 和 AI 性能账号选项解析 owner ID 按用户名精确 / 前缀定位；系统账户管理列表不按 ID 搜索。
- `accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL`：普通索引，用于排查相同凭据，不承担唯一约束。
- `accounts(system_account_id, lower(name))`：保证同一用户下 AI 账户名称唯一。
- `accounts(name, id)`、`accounts(system_account_id, name, id)`：AI 账户列表、账户选项和 AI 性能账号选项按账号名称精确 / 前缀定位。
- `accounts(provider_code, id)`、`accounts(system_account_id, provider_code, id)`、`accounts(type, id)`、`accounts(system_account_id, type, id)`：保留给供应商、账户类型筛选或排序使用；AI 账户通用搜索不再匹配这些字段。
- `accounts(account_expires_at, updated_at, id) WHERE account_expires_at IS NOT NULL`、`accounts(system_account_id, account_expires_at, updated_at, id) WHERE account_expires_at IS NOT NULL`：账户套餐到期清理只按到期时间读取固定批量 ID，再按主键更新，避免账户列表 / options / 详情请求触发全表扫描、临时排序或无界批量写。
- `account_test_tasks(request_system_account_id, updated_at, id)`：账号测试任务按发起人和更新时间查询；读取时还要校验同一管理筛选作用域，避免跨用户作用域读取任务状态。
- `account_test_tasks(status, queued_at, id)`：background worker 按排队时间读取可执行测试任务。
- `account_test_tasks(finished_at, id) WHERE finished_at IS NOT NULL`：清理已完成的短期测试任务记录。
- `groups(system_account_id, provider_protocol_profile_id, lower(name))`：保证同一用户同一供应商协议档案下分组名称唯一。
- `groups(name, id)`、`groups(system_account_id, name, id)`：账户绑定分组和分组选项按分组名前缀定位。
- 当前未为 `group_type` 单独增加索引；分组列表和网关候选快照仍复用所有者、供应商协议档案和名称相关索引读取，只有出现明确查询瓶颈时再补 `groups(system_account_id, provider_protocol_profile_id, group_type, id)` 这类定向索引。
- `groups(system_account_id, provider_protocol_profile_id) WHERE is_default = 1`：保证同一用户同一供应商协议档案只有一个默认分组；默认分组读取只认 `is_default = 1`，不按固定名称或最新分组兜底。
- `system_teams(name, id)`：授权团队列表只按团队名称精确 / 前缀匹配，不搜索团队 ID 或说明。
- `api_keys(system_account_id, lower(name))`：保证同一用户下 API Key 名称唯一，密钥本身仍由 `key_hash` 兜底。
- `api_keys(name, id)`、`api_keys(system_account_id, name, id)`：API Key 列表关键词只按名称精确 / 前缀匹配，不搜索 Key 前缀或说明，也不做无边界包含匹配。
- API Key 列表不维护 Key 前缀、后缀、说明或多字段组合搜索索引；`key_prefix` 和 `key_suffix` 只作为摘要展示字段保留，不参与列表关键词查询。
- `external_integration_sources(lower(name))`：保证外部来源系统名称唯一。
- `external_integration_sources(updated_at, id)`、`external_integration_sources(status, updated_at, id)`：后台来源系统列表按来源表自身分页排序；token 数和 token 摘要只在当前页来源 ID 窗口内补读，不能在分页前 join / group 全量 token 表。
- `external_integration_sources(name, id)`：后台列表名称搜索只按名称精确 / 前缀匹配。
- `external_integration_source_tokens(token_hash)`：保证来源 token 摘要唯一；来源系统鉴权按 token hash 点查，不扫描表；完整 token 只保存在 `token_secret_encrypted` 密文中，列表只展示前缀和后缀。
- `external_integration_source_tokens(source_ref_id, status, expires_at)`：管理页面按来源系统读取 token 状态和过期边界。
- `api_key_group_bindings(api_key_id, group_id)`：保证同一个 API Key 不重复绑定同一分组号池。
- `api_key_group_bindings(api_key_id, priority) WHERE status = 'active'`：保证同一个 API Key 的 active 号池优先级唯一。
- `api_key_group_bindings(api_key_id, system_account_id, priority, created_at, id) WHERE status = active`：网关按 Key 读取 active 号池路由绑定时固定最多取 20 条窗口，并按路由优先级稳定排序，不能扫描全部绑定或临时排序。
- `api_key_group_bindings(group_id)`、`api_key_group_bindings(system_account_id, api_key_id)`：分组删除保护、列表筛选和管理作用域校验使用。
- `api_key_group_bindings(system_account_id, group_id, api_key_id)`：删除分组前按系统账户和分组读取最多 101 条受影响 API Key 固定窗口，超过 100 个受影响 Key 时拒绝一次性删除，避免主请求全量枚举绑定或开启大事务。
- `proxy_profiles(lower(name))`：保证代理配置名称全局唯一。
- `proxy_profiles(updated_at, id)`、`proxy_profiles(enabled, name, updated_at, id)`：代理管理列表默认排序、启用代理选项和按名称前缀筛选都必须走固定窗口索引，不能为了列表或选项全量扫描代理表。
- `proxy_profiles(name, id)`：代理管理列表和代理选项只按代理名称精确 / 前缀匹配；不维护代理地址、类型、用户名或说明的组合搜索索引。
- `accounts(proxy_profile_id, id)`：代理删除前的账号占用检查按代理 ID 读取最多 4 个账户固定窗口，禁止扫描全部账号或做精确 `COUNT(*)`。
- 业务唯一索引直接落到当前 schema；若本地数据已有重复记录，应先离线清理或重建库，不在启动路径保留跳过索引的兼容逻辑。
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
- `resource_authorization_grants(resource_owner_system_account_id / resource_type + resource_id / grantee_system_account_id / grantee_team_id, status, created_at, id)`：授权列表按归属人、资源、被授权人或团队筛选时直接按创建时间分页，避免请求链路临时排序大量授权操作。
- `resource_authorization_grants(expires_at, updated_at, id) WHERE status IN (active, paused) AND expires_at IS NOT NULL`：统一授权过期扫描只按到期时间读取固定批量，避免 DB service 请求路径或 worker 扫描全表并为排序创建临时 B-tree。
- `resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = active AND grantee_type = system_account`：防止同一资源重复创建有效人员授权；已回收、已归还或已过期的再次授权复用原记录恢复。
- `resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = active AND grantee_type = team`：防止同一资源重复创建有效团队授权；已回收或已过期的再次授权复用原记录恢复。
- `resource_authorization_sources(authorization_id, source_type) WHERE status = active AND source_type = manual`、`resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = active AND source_type = team`：防止同一最终授权存在重复有效来源。
- `group_authorization_settings(system_account_id, group_id)`：按被授权人和分组读取授权分组本地使用配置；主键仍是 `authorization_id`，保证同一用户级分组授权只有一份本地覆盖。
- 有效资源查询需要在 service 层按 `resource_type + resource_id + caller_system_account_id` 去重；团队来源只能合并到用户授权来源摘要，不能展开成多条 AI 账户或分组。
- `usage_records(system_account_id, first_token_ms, created_at, id)`、`usage_records(system_account_id, duration_ms, created_at, id)`、`usage_records(system_account_id, cost_usd, created_at, id)`：使用记录页按首 token、总耗时、成本排序时只取有限窗口，避免大数据量下前端全量排序或数据库临时排序。
- `usage_records(first_token_ms, created_at, id)`、`usage_records(duration_ms, created_at, id)`、`usage_records(cost_usd, created_at, id)`：管理员查看全部系统账户时的全局排序索引。
- `usage_record_shard_entries` 冗余使用记录列表常用筛选和排序字段，列表请求先从目录库读取固定 1001 行以内的候选窗口，再按当前页 ID 回 usage shard 补取详情；禁止在列表请求中逐 shard 拉取大窗口后用 JS 全局排序。
- `usage_record_account_shards(account_id, first_created_at, shard_key)`、`usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key)`：已删除 AI 账户 / API Key 关联记录清理先读取去重 shard 目录，按固定 shard 窗口回 usage shard 清理；不能从请求或 worker 维护路径枚举全部 shard，也不能从 `usage_record_shard_entries` 明细表临时 `GROUP BY shard_key` 聚合定位。
- 管理端和排障端分页列表统一使用固定 1001 行候选窗口，服务端会把过大的 `page` 收敛到当前 `pageSize` 下 `OFFSET <= 1000` 的范围内；API Key、AI 账户、分组、系统账户、代理、公告、外部来源系统、系统团队、统一授权、使用记录、审计日志、操作日志、运行日志、IP 统计、模型检测、账户用量和授权用量都不能因为深翻页产生线性增长的 SQLite offset 扫描。分页响应里的 `total` 或 `pageUpperBound` 只表示当前窗口上界，是否还有下一页以 `hasMore` 为准。
- `accounts(fallback_enabled, super_priority_enabled, status, priority)`：自有账号调度和列表排序读取降级备用、超级优先与优先级。
- `group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id)`：授权实例在当前分组内的备用、超级优先和排序读取绑定级调度事实。
- `account_quality_scores(provider_code, quality_score, quality_state)`：账号质量缓存排序和后台挑选候选。
- `account_quality_minute_stats(stat_minute, account_id)`：账号质量刷新读取近窗口分钟桶，不实时回扫 `usage_records`。
- `account_quality_dirty_accounts(updated_at, account_id)`：账号质量刷新按固定 dirty 账号窗口推进，不能从分钟桶全量聚合近期全部账号。
- `usage_stats_totals(system_account_id, scope_type, scope_id)`：列表读取累计值。
- `usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)`：今日和天级趋势读取。
- `usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)`：小时趋势读取。
- `client_ip_registry(bucket_no, ip_hash)`：后台 worker IP 分桶 Set 懒加载和注册兜底读取；启动时不全量预热。
- `client_ip_registry(last_seen_at DESC, ip_hash)`：IP 运维列表按最近出现排序。
- `client_ip_registry(aggregate_ip_key)`：IP 运维关键词按聚合 IP key 定位。
- `client_ip_registry(client_ip)`：IP 运维关键词按最近样本明文 IP 定位。
- `client_ip_stats_daily(stat_date, ip_hash)`：IP 范围窗口按日期读取 daily 桶。
- `client_ip_usage_range_windows(start_date, end_date, total_cost_usd/request_count/active_days/last_used_at, ip_hash)`：IP 运维列表按范围窗口成本、请求数和活跃天数排序，公开 IP 用量接口按范围窗口最近使用排序；管理页全局最近使用排序走 `client_ip_registry(last_seen_at DESC, ip_hash)`。总 Token 和失败率排序使用表达式索引。平均首 token、平均总耗时和最大总耗时只展示不排序，避免新增高基数排序压力。
- `client_ip_usage_range_windows(end_date)`：数据保留任务按范围窗口结束日期分批清理过期 IP 窗口，避免保留清理退回全表排序。
- `client_ip_range_window_dirty_ips(updated_at, ip_hash)`：持久记录等待刷新范围窗口的 dirty IP；刷新成功后删除，避免 worker 重启丢失内存 dirty Set 后只能靠全量重建恢复。
- `stats_job_state(scope_type, scope_id, job_name)`：同时用于 IP 范围窗口 ready/stale 标记；新 IP daily 写入会把当前固定窗口标记为 stale，后台刷新完全部 dirty IP 后再标记 ready，列表不靠窗口表行数判断可读。
- `client_ip_policies(status, ip_hash, expires_at)`：网关封禁缓存和管理页状态读取 active 封禁策略。
- `client_ip_policy_hits(stat_date DESC, ip_hash)`：后台记录封禁命中趋势，当前 IP 管理页面不展示该明细。
- `usage_overview_summary_windows(system_account_id, window_key, start_date, end_date)`：统计概览摘要读取。
- `usage_overview_trend_windows(system_account_id, window_key, start_date, end_date, bucket_key)`：统计概览趋势读取。
- `usage_model_rank_windows(system_account_id, window_key, start_date, end_date, rank)`：模型 TopN 读取。
- `usage_error_rank_windows(system_account_id, window_key, start_date, end_date, rank)`：错误 TopN 读取。
- `ai_performance_summary_windows(system_account_id, window_key, start_date, end_date)`：AI 性能监控摘要读取。
- `usage_quota_hourly_windows(system_account_id, scope_type, scope_id, window_hours)`：n 小时额度读取。
- `usage_scope_range_windows(system_account_id, scope_type, scope_id, start_date, end_date)`：最近 31 天范围总量按具体 scope 读取。
- `usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, scope_id)`：账号用量列表按系统账户、作用域类型和日期范围分页读取。
- `usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, request_count, total_cost_usd, input_tokens + output_tokens, last_used_at, scope_id)`：管理侧账号用量默认排序读取。
- `usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, request_count / success_count / error_count / error_rate / total_tokens / total_cost_usd / active_days / last_used_at, scope_id)`：公开账号用量按指标排行读取，接口只能使用窗口表索引分页，不能回扫日表聚合后排序。
- `system_metrics_trend_windows(window_key, start_date, end_date, bucket_key)`：系统性能 / 网络吞吐趋势读取。
- `process_event_loop_trend_windows(window_key, start_date, end_date, bucket_key, process_role)`：主进程、后台 worker 和 DB service 事件循环趋势读取。
- `usage_model_daily(system_account_id, stat_date, model)`：模型分布读取。
- `usage_error_daily(system_account_id, stat_date, error_group, error_code)`：错误分布读取。
- `usage_model_hourly(system_account_id, stat_hour, model)`：监控窗口模型分布读取。
- `usage_error_hourly(system_account_id, stat_hour, error_group, error_code)`：监控窗口错误分布读取。
- `group_account_stats(system_account_id, group_id)`：分组列表读取账户数量与状态统计。
- 系统账户选项接口只读业务库里的 `id`、`username`、`display_name`、`status`，不得使用 `SELECT *`，也不得读取 `password_hash`、角色、改密状态、最后登录、创建 / 更新时间等系统账户管理字段。
- 账户 / 分组选项接口只读业务库里的资源基础字段、有效授权关系和必要的 `group_accounts` 账号 ID 映射；普通下拉不得读取 `group_account_stats`、用量缓存、账户质量记录或并发快照，账户页专用分组 `account-options` 也只允许额外返回 `accountIds`；分组选项关键词只支持分组 ID、名称和供应商的精确 / 前缀匹配，不做无边界包含匹配。
- `stats_job_state(scope_type, scope_id, job_name)`：后台任务游标读取；使用记录保留清理的安全 floor cursor 还依赖 `scope_type, cursor_created_at, cursor_id, job_name` 部分索引，同时比较 `usage_stats_aggregation` 与 `client_ip_stats_aggregation` 两类 usage shard 消费者游标。
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
- `announcements(updated_at, created_at, id)`：管理员公告管理列表按最近更新分页排序。
- `announcement_reads(system_account_id, read_at)`：按用户读取或排查公告已读状态。

默认任务策略：

- 所有定时和批处理任务都必须在独立 background worker 进程内调度和执行，不能在 Web/API 主进程里用 `setInterval`、cron 或调度框架直接执行任务函数。
- 调度框架只负责 worker 进程内的注册、不可重入、错误隔离和触发时机；worker 不能通过 IPC 回到主进程执行统计、清理、刷新或批量落库。
- 主进程和 DB service 可以把请求链路产生的待处理记录投递给 worker，但 IPC 或等价通道必须有上限，满载时按任务安全等级丢弃或降级，不能阻塞正常请求；server / DB service 角色下使用记录、操作日志、审计记录和运行日志索引都只能进入 worker IPC 队列，不能因为 worker 未就绪而回退到本地 SQLite 队列。
- worker 本地队列 flush 失败时必须保持原队列等待重试，不能用 `pending = [...batch, ...pending]` 或全量 reduce 字节数的方式把失败 batch 拼回队头；成功写入后再从队头移除已提交 batch，避免数据库异常期间按积压量复制数组阻塞 worker 事件循环。
- DB service 负责系统管理 API 与数据库请求隔离，不负责后台定时调度；后台 worker 仍负责统计、操作日志落库、审计、运行日志索引、数据保留、代理检测和 OAuth 后台刷新。server 角色下 DB service 未就绪、队列满、IPC 超时或内部系统 API 不可用时，请求链路返回可读错误并等待 supervisor 重启，不能在主进程同步执行 DB service 操作，也不能恢复主进程管理 CRUD。业务库写入必须短事务同步提交；统计数据集域记录型写入优先投递 worker 队列，由 worker 作为消费端使用短事务和 `busy_timeout` 控制 SQLite 写锁等待。
- 统计 worker 每 1 分钟按 `system_account_id` 和 `(created_at, id)` 游标增量读取 `usage_records` 并 upsert 到聚合表。usage 分片落地后，统计输入侧改为每个 shard 独立维护 `(created_at, id)` 游标，统计结果库表和查询口径不变。
- 用量统计菜单只读取统计缓存，且口径是当前调用方自己的账户消耗：用户侧 `我的用量` 和管理侧 `用量统计管理` 页面日期范围都默认最近 31 天，最大最近 31 天；筛选区下方趋势账户列表在普通用户和管理员指定用户时，默认从 `usage_rank_snapshots` 读取 `caller_account + last7d + request_count` 的最近 7 天活跃前 10。趋势点读取 `usage_stats_daily` 的日行，范围累计读取 `usage_scope_range_windows` 的范围行；账户关键词必须先在业务库解析为当前调用方 `caller_account` 范围窗口中的实际账户 ID，解析范围包含自有账户名、授权实例名、授权实例来源账户当前名，以及分组授权来源账户名，再用 `scope_id IN (...)` 读取窗口。点选账户时页面只在当前已返回的账户范围窗口行内切换卡片和明细展示，不回扫明细表。管理员全部用户视图的顶部摘要读取 `system_account = global` 的范围行。接口不能把每日行再相加生成范围汇总，前端也不能把日行汇总成摘要。
- 统计概览属于监控窗口；页面日期范围默认今天，最大最近 31 天。概览摘要、请求 / 失败 / Token / 平均总耗时趋势、模型分布和错误 Top 10 均读取 worker 写入的 `usage_overview_*_windows`、`usage_model_rank_windows` 和 `usage_error_rank_windows`，不在接口中按小时缓存临时相加；这些窗口快照由 worker 按功能表分阶段短事务刷新，阶段之间让出事件循环。概览 scope 发现按 `usage_stats_totals.updated_at` 读取固定上限窗口，避免异常系统账号数量让单轮 worker 装载全部 scope；用户侧展示自己的错误 Top 10，系统性能 / 网络吞吐趋势和进程事件循环趋势只在管理侧展示。
- `AI性能监控` 的默认账户池在用户侧读取 `usage_rank_snapshots` 中当前调用方 `caller_account + last7d + request_count` 的最近 7 天活跃前 10，管理侧未筛选时读取 `system_account_id = global` 的 `account` 统计缓存和排行快照；快照缺失时默认列表为空，不能在接口请求时临时聚合降级。图表序列在用户侧读取 `usage_stats_hourly` 的 `scope_type = caller_account` 数据，在管理全局视图读取 `scope_type = account` 数据。账号选项关键词只支持账号名精确 / 前缀和授权实例来源账户当前名精确 / 前缀；显式追加只能通过 `accountIds` 回填，关键词不按账号 ID、供应商编码或系统账号名搜索，不能在账号选项查询中使用多列前导通配符扫描。用户侧可见范围包含自有账户、授权实例账户和授权分组内来源账户；授权分组来源账户只读取当前调用方自己的 `caller_account` 数据，不能把资源归属人的自用数据带入被授权人页面。授权实例账户和归属人原账户分别统计，互不混入。页面日期范围默认最近 3 天，最大最近 31 天，按小时返回首 token 和总耗时的平均值 / 最大值；页面顶部摘要由后端返回，前端账户筛选只影响图表显隐，不重新计算业务摘要。接口不得实时 `GROUP BY usage_records`。
- 分组账户统计 worker 定时读取业务库 `group_account_stats_dirty` 标记的脏分组，并写入统计结果库 `group_account_stats`；分组列表不得在查询时临时 `COUNT/SUM group_accounts + accounts`。授权或团队变化影响面无法精确收敛时，可以写入 `__all__` 哨兵触发全量刷新，但写请求本身不能展开所有分组，worker 也必须按游标固定批次推进，不能在单轮任务里一次性刷新全部分组。
- 高并发分组调度使用的当前并发、账号通道并发计数、分组排队、单账户排队阈值占用、慢请求计数和最近分配时间属于网关主进程内易失运行态；SQLite 只保存分组类型、策略配置、账号硬配置和可重建质量缓存。服务重启后高并发调度允许回到保守默认，不能把这些易失指标当作授权、额度、账务或审计事实。
- 授权分组的高并发配置优先读取 `group_authorization_settings.group_type / scheduling_policy_json`，没有本地覆盖时才读取来源 `groups` 配置；被授权人的本地配置只影响该调用方的授权分组调度，不改写来源分组。
- 高并发分组的后台反向缓存只保存可重建的短窗口质量摘要，不能保存敏感 payload、完整错误响应或会话内容。缓存缺失、过期或 worker 滞后时，网关必须按保守默认选号，不能在请求链路触发同步重建。
- 账号质量刷新 worker 默认每 10 分钟执行一次：先 flush 使用记录队列，再消费 `account_quality_dirty_accounts` 中固定数量账号，从 `account_quality_minute_stats` 汇总这些账号近 10 分钟真实网关请求并刷新 `account_quality_scores`。分钟桶和 dirty 目录由用量统计 worker 随主游标增量写入；刷新 worker 不回扫 `usage_records`，也不一次性加载全部账号、全部近期样本账号或全部质量缓存，只按 dirty 账号窗口批量补业务元数据和旧质量行。无新样本账号的 `stale` 标记、已删除账号质量行和失效分钟桶按固定批次滚动推进，避免账号总量决定单轮 worker 阻塞时间。主动探测能力已删除，worker 只处理真实请求样本。超过 24 小时未更新的质量分不参与网关调度。
- OpenAI OAuth Access Token 保活 worker 默认每 1 分钟扫描仍存在、未删除、有 `refresh_token` 且即将过期的真实 OAuth 来源账户，扫描不受账户状态和调度标记影响；授权实例不作为后台预刷新对象，因为实例不持有真实 token。成功时更新来源账户 `accounts.credentials_encrypted` 中的 token 凭据，不恢复普通冷却状态；连续 3 次失败会把非停用来源账户写为 `status = error`、`last_error_code = oauth_token_refresh_failed`，后续后台刷新成功会自动恢复该异常。手动停用账户不会被后台刷新失败覆盖成异常。
- 网关请求中触发的 OpenAI OAuth Access Token 即时刷新，在 server 角色下必须通过 DB service 查最新账户、解析代理和持久化新凭据，不能直接读取或更新 SQLite；如果命中的是授权实例，刷新结果必须写回 `credentialSourceAccountId` 指向的来源账户，而不是写入授权实例。该路径只作为后台预刷新未覆盖时的正确性兜底，同账号并发刷新必须由进程内串行锁和最近刷新缓存收敛，避免一波临期请求重复打 DB service。
- 冷却账号恢复性复测处理冷却到期的 `temporary_unavailable` / `rate_limited`、仍可调度、已绑定分组且未过期的账号；`error`、`disabled` 等硬状态不进入后台复测队列。复测前优先从最近真实 `usage_records` 学习 `endpoint/model/stream` 元信息，最多按最近 7 天 date shard 倒序查找，命中后立即停止，endpoint 只按规范化后的 OpenAI 路径精确 / 子路径前缀识别，不使用前导通配符扫描；该流程只读取 `traffic_source = gateway` 的真实请求，不读取 `request_snapshot_json`，也不重放用户 prompt、工具参数或文件内容。恢复探活的模型由 `accounts.last_successful_test_model` 显式指定；没有手动成功记录时使用供应商协议档案 `provider_protocol_profiles.default_test_model`，不会被最近真实请求模型覆盖。探活输入使用后端最小 Responses 默认输入。后台复测固定启用，复用真实网关链路但候选只包含当前复测账号，失败后由复测任务自身按 3 秒快速恢复通道和指数退避更新 `cooldown_until`，同时用本次上游真实 `status/code/message` 更新账号错误摘要；超过快速阈值后进入慢速恢复，单次等待不超过 `defaultTemporaryUnschedulableMinutes`，超过 `cooldownAccountRetestMaxBackoffHours` 表达的最长自动恢复观察窗口后转为 `cooldown_retest_max_recovery_exceeded` 异常并停止自动复测。恢复探活使用记录与审计均标记 `traffic_source = cooldown_retest`，不参与业务统计、账户质量统计和真实请求形态学习，Codex 额度快照也保留 `cooldown_retest` 来源而不伪装成真实网关流量。
- 代理延迟刷新 worker 固定每 1 分钟检测最多 20 个启用代理，测试目标来自已启用供应商的默认地址，并把最近状态、延迟和检测时间写回 `proxy_profiles`；出口 IP / 地区只由手动测试刷新，不提供系统设置项调整。
- 授权账户调用需要同时写入调用方统计、调用方命中账户统计、授权实例账户统计、授权额度统计和授权报表：调用方列表、分组、API Key 和日志按 `system_account_id` 聚合；`我的用量` 按 `system_account_id + scope_type = caller_account + account_id` 读取本人对该授权实例的消耗；授权实例账户总量按被授权实例所属 `system_account_id + scope_type = account + account_id` 聚合；账号授权额度按被授权实例所属 `system_account_id + account_authorization_id` 聚合；管理侧团队 / 用户消耗按授权范围窗口表直读，并过滤授权方自用消耗。使用记录写入只拿到授权实例 `account_id` 时，存储层必须自动补齐 `account_owner_system_account_id`、`account_access_type = account_authorized` 和 `account_authorization_id`，避免旁路记录被误记成自有账户。
- 授权分组调用需要写入调用方自己的 `system_account`、`caller_account`、API Key、模型和端点统计，并写入资源归属人侧的 `group_authorization` / 授权报表聚合；它不能写入分组所有者普通 `group` 统计，也不能把命中的来源账户写入资源归属人的普通 `account` 统计。当前调用方 API Key 和日志按 `system_account_id` 聚合；授权额度按当前有效授权 ID 聚合；管理侧团队 / 用户消耗按授权范围窗口表直读，并过滤资源归属人自用消耗。新建和编辑 API Key 可以绑定有效授权分组，网关按调用方读取 `group_authorization_settings` 的本地启用、分组类型和调度策略覆盖；本地停用时该号池保留但不可调度。
- `统一授权` / `授权操作` 关系列表不展示额度和用量统计；团队 / 用户授权消耗明细只读取 `authorization_team_usage_range_windows` 和 `authorization_user_usage_range_windows`，其数据由 `authorization_team_usage_summary_daily` 和 `authorization_user_usage_summary_daily` 刷新而来。统计 worker 随 `usage_records` 游标增量写入日摘要，并预先写好 `all`、资源类型汇总和指定资源三种资源筛选行；接口只能按最近 31 天内日期范围、`page/pageSize` 和筛选条件直读一组窗口行，不能临时 `SUM usage_records`，也不能把 `usage_stats_daily/weekly/monthly` 或报表缓存再二次聚合。
- 统一授权可选美元成本额度保存在业务主表 `resource_authorization_grants.limits_json`，并同步到最终用户授权 `resource_authorizations.limits_json`；JSON 内 `limit` 表示美元金额。启用 hourly 额度时写路径同步登记 `request_quota_hourly_window_configs.window_hours`。网关按 `usage_stats_totals`、`usage_stats_daily`、`usage_stats_weekly`、`usage_stats_monthly` 和 `usage_quota_hourly_windows` 的 `account_authorization` / `group_authorization` 维度直读成本，判断 n 小时、日、周、月和总额度，不在请求内扫描 `usage_records`，也不在请求内按小时桶求和。
- 团队授权美元额度读取 `account_authorization_team` / `group_authorization_team` 作用域缓存；账号授权团队 scope 使用 `authorization_instance_account_id + ':' + team_id`，分组授权团队 scope 使用 `group_id + ':' + team_id`，由统计 worker 随使用记录增量写入；网关额度快照和实时检查 JOIN 授权实例时必须同时限定授权 ID、被授权人和来源账户，不允许在授权实例缺失或脏实例存在时回退到来源账户 ID 作为团队 scope。网关不再枚举成员授权并求和。统计 worker 默认每 1 分钟增量推进并刷新额度小时窗口，网关额度判断带短 TTL 内存缓存，因此允许轻微超额，统计追平后下一次请求会返回 429 和“额度已用完，请联系管理员提升额度”。
- API Key 列表展示累计用量，读取 `usage_stats_totals` 中 `scope_type = api_key` 的缓存，不使用今日 `usage_stats_daily` 口径；API Key 可选美元成本额度保存在 `api_keys.quota_limits_json`，JSON 内 `limit` 表示美元金额。启用 hourly 额度时写路径同步登记 `request_quota_hourly_window_configs.window_hours`。网关按 `usage_stats_totals`、`usage_stats_daily`、`usage_stats_weekly`、`usage_stats_monthly` 和 `usage_quota_hourly_windows` 的 API Key 维度直读成本，判断 n 小时、日、周、月和总额度，不在请求内扫描 `usage_records`，也不在请求内按小时桶求和。
- API Key 额度是轻量异步统计口径：统计 worker 默认每 1 分钟增量推进，网关额度判断带短 TTL 内存缓存，因此允许轻微超额；统计追平后下一次请求会返回 429 和“额度已用完，请联系管理员提升额度”。
- API Key 生命周期分为停用、业务删除和记录物理清理：停用只改状态并立即拒绝后续调用；业务删除同步移除业务库记录并让后续请求立即无法再使用该 Key；该 Key 关联的历史使用记录由 usage shard 清理，原始审计日志、审计尝试、payload 引用和审计错误组由数据集目录库清理，API Key 维度统计缓存、额度窗口、排行 / 范围窗口以及调用方、账户、分组、授权、模型和错误等相关聚合缓存由统计结果库反向扣减。usage shard 与统计结果库不能共享强事务，因此清理任务先通过去重 shard 目录锁定当前 Key 仍有记录的有限 shard 窗口，再在统计结果库用 `usage_record_cleanup_deductions` 保证每条 usage 只扣减一次，最后删除对应 shard 行；如果 shard 删除或后续步骤失败，后台重试只补删或继续收尾，不重复扣减统计。未被任何审计引用继续使用的 payload blob 由后续无引用 blob 清理任务删除。
- AI 账户生命周期分为逻辑删除和过期物理清理：删除操作只面向真实来源账户，只写 `accounts.deleted_at / deleted_by`，并把账户置为停用、不可调度、清空冷却；列表、options、授权使用、网关调度、账户测试、后台 OAuth 刷新和冷却复测都必须排除 `deleted_at IS NOT NULL` 的账户。删除来源账户时，同步逻辑删除该来源下所有授权实例，授权操作记录和运行时授权改为 `revoked`；授权实例账户不接受删除，被授权人归还个人直授权时个人授权改为 `returned`。逻辑删除阶段不删除 `usage_records`、审计、模型检测、质量分、额度快照和统计缓存。独立 background worker 每天扫描 `deleted_at` 已超过 1 个月的账户，先按统计安全游标分批清理 usage shard、数据集目录库和统计结果库，再物理删除账户、分组绑定、支持模型、授权来源、授权主记录、授权 grant 和所有相关统计窗口；如果游标未追平或 SQLite 写锁冲突，保留清理目标等待下次重试。
- 管理员全局汇总读取后台写入的 `system_account = global` 缓存行，不在概览接口里临时汇总多个系统账户缓存行，更不能回扫 `usage_records`。
- 系统采样 worker 每 10 到 30 秒写入一次 `system_metrics_samples`，不写用户级业务归属；`event_loop_lag_ms` 来自 worker 内独立事件循环直方图，记录上个采样窗口内去除采样分辨率基线后的额外最大延迟。多进程事件循环延迟单独写入 `process_event_loop_samples`：worker 本地采样自身，server 通过 IPC 返回主进程样本，并通过专用诊断 IPC 读取 DB service 样本；诊断 IPC 不进入 DB service 业务请求计数，也不触发不可用熔断。采样频率跟随系统监控间隔，只有少量 IPC 和一次直方图读取，不扫描业务数据。概览接口的最新进程采样状态和最近 24 小时峰值状态都固定返回 `server`、`worker`、`db-service` 三个角色；缺少样本时用 `sampleAvailable = false` 和 `null` 字段表达未知，不用缺项、0 或默认时间伪装正常。事件循环趋势固定展示最近 24 小时分钟峰值桶，不跟随概览日期范围，单次读取被原始采样 7 天保留期和 24 小时窗口硬限制住。
- 后台 worker 是一个固定常驻子进程，不是每个后台任务临时创建一个进程；内部各定时任务由同一个调度器注册、不可重入执行并记录运行快照。管理侧系统监控可查看每个后台任务的最近耗时、最长耗时、运行中状态、成功次数、失败次数、跳过次数和最近错误，用于判断具体是哪一个任务拖慢 worker；worker snapshot 不可用时接口必须返回显式可用性标记和 `null`，不能把未知状态压成空任务数组。
- 系统采样的 `memory_used_percent` 表示主机实际内存压力口径，不是所有平台都等同于 `totalmem - freemem`。macOS 会把可回收文件缓存、inactive 和 speculative 页面排除在已用内存外，按 `vm_stat` 的 `Anonymous pages + Pages wired down + Pages occupied by compressor` 计算，避免把系统缓存误报为应用内存占用；读取失败时才回退到 Node 默认口径。
- 审计日志 worker 每隔短时间或达到批量阈值后，从 worker 队列取终态审计记录，按策略计算正文保留、压缩、去重和错误聚合，并用短事务批量写入 `audit_logs`、`audit_log_attempts`、`audit_payload_refs`、`audit_payload_blobs` 和 `audit_error_groups` 元数据；超过单次读取窗口的大 blob 保持 plain，文件写入本地数据目录。
- 网关请求处理中不能同步写 `audit_logs`；SSE 和其他流式响应必须等自然结束、失败、超时或客户端断开后，才按终态记录入队。
- 操作日志在业务库写操作提交成功后入队，worker 从操作日志队列批量写入 `operation_logs`、`operation_log_targets`、`operation_log_viewers` 和 `operation_log_summary_search_terms`；操作日志入队或落库失败只影响追溯数据，不反向回滚已提交业务变更。
- 数据集目录库、usage shard 和统计结果库维护类动作不在管理接口或 DB service 内直接执行；API Key 删除后的关联记录清理、表监控手动非业务数据硬清理、OpenAI Codex 用量快照写入等都投递 `recordMaintenanceQueue`，由 worker 分批执行。表监控硬清理只保留业务库，按截止时间清理数据集目录库、统计结果库、usage shard 和审计 payload 外部文件；普通表按 schema 动态枚举可识别时间列，usage shard 和审计 payload 文件走专门物理删除流程；不等待统计安全游标，也不做关联扣减。
- 运行日志索引 worker 从 Pino JSONL 输出流旁路接收日志行，按 worker 队列批量写入 `runtime_logs`，并随新增日志增量维护级别 / 事件 facets；常规维护只在 facets 缺失时重建，数据保留清理按已删除索引行扣减 facets，不能每轮或每次清理后对 `runtime_logs` 全量 `COUNT/GROUP BY`。
- 日志搜索不再有额外回填任务；运行日志 keyword 只查 `runtime_logs.message`，操作日志 `summaryKeyword` 读取随操作日志写入同步生成的 `operation_log_summary_search_terms` 摘要倒排词项。
- “日志搜索”索引查询读取当前 SQLite `runtime_logs` 表，使用 `traceId`、级别、事件和日志时间等通用索引条件缩小结果，关键字只对 `message` 列做普通模糊匹配；列表默认展示最近 100 条并通过后端分页继续翻页。索引表保留周期由后台清理任务控制。
- “日志搜索”的 `grep 模式` 通过后端 `rg` 直接扫描日志目录中当前保留的 `.log` 文件，不受索引表 3 天保留期限制；文件日志默认保留 30 天，并受最多 500 个轮转文件和单文件大小限制。该模式默认按文件时间搜索最近 3 天，单次文件时间范围最多 7 天，时间范围只用于筛选参与扫描的文件，不读取文件内容判断行时间；同一后端进程一次只允许 1 个 grep 搜索，单次 `rg` 搜索 15 秒超时，最多展示 100 行；多关键字必须在同一行同时命中，后端按日志时间或文件时间返回最新匹配。运行环境缺少 `rg` 时直接返回错误提示，不回退到慢速文件扫描。
- 使用记录、操作日志、统计数据集域维护、审计和运行日志索引队列都是 best-effort 队列，系统重启、进程崩溃或队列溢出导致统计数据集域事实丢失或维护延迟可以接受；队列丢弃或投递失败计数应进入运维监控。
- 账户、分组、API Key 等列表接口只读 `usage_stats_totals` / `usage_stats_daily`，不要在列表查询里 `SUM usage_records`。
- 概览图表接口读取 `usage_overview_*_windows`、`usage_model_rank_windows`、`usage_error_rank_windows` 和 `system_metrics_trend_windows`；进程事件循环趋势是短期运维排障图，固定读取最近 24 小时低频采样并按分钟聚合，不跟随业务日期范围，页面上方摘要取同一 24 小时窗口内各进程最大延迟；后台任务运行状态来自 worker 运行态快照，不落表；运行态快照缺失时通过可用性字段表达不可观测，不用 `0`、`false`、`[]` 或默认天数伪装正常；`AI性能监控` 图表只读 `usage_stats_hourly` 和账户元数据，摘要只读 `ai_performance_summary_windows`。
- 全局规则：除独立 background worker、离线清洗脚本、使用记录分页明细外，任何前端列表、概览、详情和下拉元数据接口都不能在请求时做统计聚合。

表数据保留与统一清理：

| 表 | 数据类型 | 保留策略 | 是否已有统一定时清理 | 注意事项 |
| --- | --- | --- | --- | --- |
| `runtime_logs` | 普通运行日志搜索索引 | 固定最近 3 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只删除 SQLite 搜索索引，不删除后端 `.log` 文件 |
| `runtime_log_file_cursors` | 当前日志文件增量读取游标 | 固定最近 3 天未更新游标 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 当前存在的日志文件会被追尾任务持续刷新；缺失或长期未更新的过期文件游标会自动删除 |
| `model_check_runs`、`model_check_items` | 模型检测历史和诊断明细 | 默认 30 天，最多 365 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只保留有界脱敏摘要，过期检测运行和检测项一起删除 |
| `operation_logs`、`operation_log_targets`、`operation_log_viewers`、`operation_log_summary_search_terms` | 业务操作追溯日志 | 默认 365 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只按时间清理，不因资源删除级联删除历史日志；摘要词项随操作日志级联删除 |
| `audit_logs`、`audit_log_attempts` | 原始审计事件和上游尝试 | 成功样本默认 7 天，失败 / 异常事件默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 完全成功请求未命中 10% 采样时不写入原始审计 |
| `audit_payload_refs`、`audit_payload_blobs` | 原始审计 payload 引用和压缩 blob 元数据 | 成功样本正文默认 7 天，失败 / 异常正文默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 先删除过期引用，再删除无引用 blob 和本地 blob 文件 |
| `audit_error_groups` | 重复错误聚合 | 默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只做展示和排障聚合，不替代事件记录 |
| `usage_records` | 网关请求事实明细 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 自动保留任务必须按统计聚合 / 回填游标保护，只删除已确认进入缓存的旧明细；表监控的非业务数据硬清理是管理员显式操作，不走这个安全保留口径 |
| `account_quality_minute_stats` | 账号质量分钟桶 | 默认 24 小时 | 是，`data-retention-cleanup` 每天在 worker 内清理；账号质量刷新任务也会兜底清理 | 只保存真实网关请求短窗口质量样本，不回扫 `usage_records` |
| `usage_stats_minute`、`usage_model_minute`、`usage_error_minute`、`usage_latency_minute` | 分钟级统计缓存 | 默认 48 小时 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 供短窗口、账号质量和后续精细统计使用，不作为页面大范围查询事实源 |
| `usage_stats_hourly`、`usage_model_hourly`、`usage_error_hourly`、`usage_latency_hourly` | 小时级统计缓存 | 默认 60 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 覆盖 AI 性能最近 31 天小时趋势，并供 worker 刷新概览、排行、额度和 AI 性能窗口快照；API 摘要不在请求时聚合这些小时桶 |
| `usage_stats_daily`、`usage_model_daily`、`usage_error_daily`、`usage_latency_daily` | 日级统计缓存 | 默认 400 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 覆盖近一年自然日统计、范围窗口刷新和日 / 周 / 月重建 |
| `usage_stats_weekly`、`usage_model_weekly`、`usage_error_weekly`、`usage_latency_weekly` | 周级统计缓存 | 默认 104 周 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 自然周额度、周报和长期趋势基础 |
| `usage_stats_monthly`、`usage_model_monthly`、`usage_error_monthly`、`usage_latency_monthly` | 月级统计缓存 | 默认 24 个月 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 自然月额度、月度账单和年度追溯基础 |
| `authorization_team_usage_summary_daily`、`authorization_user_usage_summary_daily` | 授权日报表缓存 | 默认跟随日级统计保留 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 由统计 worker 增量写入，供授权范围窗口刷新 |
| `authorization_team_usage_range_windows`、`authorization_user_usage_range_windows`、`usage_scope_range_windows`、`client_ip_usage_range_windows` | 最近 31 天范围窗口 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 只保存可直读范围缓存，过期窗口可重建或丢弃；IP 窗口只保留 IP 行维度，不保存范围总聚合 |
| `usage_rank_snapshots` | 常用 TopN 快照 | 默认 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | AI 性能监控、我的用量和排障排行读取最新快照；快照缺失时页面默认池为空 |
| `usage_overview_summary_windows`、`usage_overview_trend_windows`、`usage_model_rank_windows`、`usage_error_rank_windows`、`ai_performance_summary_windows`、`usage_quota_hourly_windows` | 统计概览 / AI 性能 / 额度窗口缓存 | 默认最近 31 天窗口或最近 30 天刷新结果 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | API 只直读这些预聚合结果，不在请求时回扫明细 |
| `account_usage_snapshots` | 账号额度最新快照 | 默认 30 天未更新即清理 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 正常按账号主键 upsert，清理的是长期未更新的过期快照 |
| `system_metrics_samples` | 主机监控原始采样 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 只用于最新状态和短期排障 |
| `system_metrics_hourly` | 主机监控小时汇总 | 默认 30 天，最多 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 供 worker 刷新 `system_metrics_trend_windows`；API 不在请求时聚合这些小时桶 |
| `system_metrics_trend_windows` | 系统监控窗口趋势缓存 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 供概览接口直读 |
| `process_event_loop_samples` | 进程事件循环原始采样 | 默认 7 天，最多 7 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 按 `server`、`worker`、`db-service` 分进程角色保存，用于短期定位哪个进程卡顿 |
| `process_event_loop_hourly` | 进程事件循环小时汇总 | 默认 30 天，最多 30 天 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 长期粗粒度排障缓存；管理页趋势不再用小时桶 |
| `process_event_loop_trend_windows` | 进程事件循环窗口趋势缓存 | 默认最近 31 天窗口 | 是，`data-retention-cleanup` 每天在 worker 内清理；刷新任务会覆盖当前窗口 | 供进程事件循环趋势接口直读；管理页固定展示最近 24 小时分钟桶 |
| `database_storage_snapshots`、`table_storage_snapshots` | 表监控采样历史 | 默认最近一月，最多最近一月 | 是，`data-retention-cleanup` 每天在 worker 内清理，采样写入时也会轻量兜底清理 | 用于管理员表监控页面展示业务库、数据集目录库和统计结果库容量趋势，不纳入默认业务备份 |
| `system_sessions` | 后台登录会话 | 到期即清理 | 是，`data-retention-cleanup` 每天在 worker 内清理 | 查询时也会校验过期时间，定时清理用于回收表数据；`last_seen_at` 只允许按短间隔节流刷新，不应在每个鉴权请求中无条件写入 |

不按保留期物理清理：

- `usage_stats_totals` 长期保留，作为账户、分组、授权和全局总量缓存。
- `stats_job_state` 长期保留，作为统计游标和任务状态；它是自动保留任务和管理员手动清理删除 `usage_records` 的安全边界。
- `group_account_stats` 是当前分组账户状态缓存，由刷新任务按脏分组刷新；业务库 `group_account_stats_dirty` 是刷新队列，不属于历史日志，处理完成即可删除。
- 数据集目录库不维护需要回填的日志搜索影子表；操作日志摘要词项只随新操作日志同步写入并随保留期级联清理。数据集目录库和统计结果库新增表都应是普通表或普通索引，必须接入统一保留期或明确说明长期保留理由。
- 普通日志文件由文件日志滚动配置清理，不属于 SQLite 表清理；当前默认保留 30 天，并受最多 500 个轮转文件和单文件大小限制；`grep 模式` 扫描当前保留的 `.log` 文件，但单次文件时间范围最多 7 天。

统一清理规则：

- 表数据保留期统一由 `data-retention-cleanup` 每天在独立 background worker 进程内执行；表监控页面额外提供 `usage_records` 按截止时间手动清理入口，用于容量异常时提前释放可复用页。
- 清理任务按小批次删除，默认每类表每轮最多处理 `dataRetentionCleanupBatchSize = 1000` 条、最多 `dataRetentionCleanupMaxBatchesPerRun = 2` 批；运行时会把异常偏大的配置钳制到这个上限，避免长时间占用 SQLite 写锁或拉高 worker 事件循环延迟。保留清理按表独立推进，前序表已经清完后，如果后续表失败，下一轮从失败表继续。
- 手动清理 `usage_records` 同样按批次执行，截止时间不能晚于当前时间 24 小时前，并且必须受统计聚合游标和必要回填游标保护。提交前先按 `created_at < cutoffAt` 与统计安全游标交集做有限预检查；没有可安全清理记录、统计游标尚未建立或 worker 投递不可用时返回 `queued = false` 和原因；预检查通过后才返回 `queued = true` 并交给 worker 分批清理。
- 统计聚合、系统指标采样、审计日志落库和运行日志索引队列只负责写入或聚合，不再在各自流程里顺手删除历史表数据。
- 如果统计缓存损坏或统计口径升级，可以停服务后在发布包根目录运行 `node backend/dist/scripts/maintenance/rebuild-usage-stats.js`，从 usage shard 文件中尚未清理的 `usage_records` 重新构建缓存。该命令会清空并重建当前 `backend/.env` 指向统计结果库里的统计缓存；如果 usage shard 为空或历史 `usage_records` 已丢弃，历史统计明确放弃，后续从新请求重新累计。执行前必须确认业务库路径无误，避免误操作业务数据。

## 操作日志存储

操作日志是独立于使用记录和原始审计日志的业务变更追溯数据，具体行为见 [操作日志设计](操作日志设计.md)。

源码边界：

- schema 入口及 `storage/schema/` 拆分文件中只保留当前 `operation_logs`、`operation_log_targets`、`operation_log_viewers`、`operation_log_summary_search_terms` 表结构和索引。
- 操作日志只记录成功提交的状态变更；`GET`、列表、详情、筛选、分页和日志查看不写操作日志。
- 操作日志不主动采集完整请求体、完整响应体、完整 headers 或原始审计 payload；如果调用方把凭据、token、代理密码、验证码、登录密码等字段作为变更项、摘要或元数据传入，操作日志服务不再按字段名脱敏，只保留条数、单值长度和可见性裁剪边界。
- 删除业务资源时不删除历史操作日志；历史日志保留当时的资源 ID、资源名称、安全摘要和影响用户。
- 普通用户可见性优先由 `operation_log_viewers` 预计算，全员安全摘要由 `operation_logs.visibility_scope = 'all_users'` 承载，不为全员摘要展开 viewer 行。
- 用户侧列表由 `operation_log_viewers.system_account_id` 命中的可见集合与 `visibility_scope = 'all_users'` 的全员摘要集合按固定窗口分别读取后合并，列表不解析字段差异 JSON，详情按权限再读取明细。

当前 `operation_logs` 表：

- `id`
- `trace_id`
- `actor_system_account_id`
- `actor_username`
- `actor_display_name`
- `actor_role`：`super_admin`、`admin`、`user`
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

- `changes_json` 保存调用方传入的字段差异；不再因为字段名命中敏感词自动改写为“已变更”“已清空”“已设置”等摘要，但仍受变更条数和单值长度上限控制。
- `metadata_json` 只保存业务排查所需的安全上下文，例如授权来源摘要、设置分组、资源归属快照等。
- 管理员操作某个用户资源时，`actor_system_account_id` 保存真实管理员，`operation_scope_system_account_id` 保存被管理用户或资源 owner。
- 全员可见的设置或公告变化可不展开 `operation_log_viewers`，由 `visibility_scope = 'all_users'` 支撑用户侧查询。

## 原始审计日志存储

原始审计日志是独立于使用记录的高权限排障数据，捕获行为见 [原始审计日志设计](原始审计日志设计.md)，容量治理见 [审计日志保全策略设计](审计日志保全策略设计.md)。

源码边界：

- schema 入口及 `storage/schema/` 拆分文件中只维护当前审计事件、尝试、payload 引用、blob 元数据和错误聚合表结构。
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
- `raw_payload_bytes`：原始逻辑字节，summary-only / hash-only 时仍按原始 body 大小计入
- `compressed_payload_bytes`：实际落盘 blob 字节，summary-only 时只计算摘要 blob 的落盘大小
- `compression_saved_bytes`：原始逻辑字节减实际落盘字节，用于容量报表
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
- 成功样本 body 不超过 `512KB` 时保存完整正文，超过后保存 `summary_only` 摘要；问题链路 body 不超过 `2MB` 时保存完整正文，超过后保存原始 hash、大小、头尾 `256KB` 和 JSON 结构摘要。
- 临时全量捕获可在审计日志页面通过弹窗运行期配置；范围可选全局或指定 AI 账户，并可选择是否让命中范围内的普通 `200 success` 跳过成功采样进入原始审计。`JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED=1` 只作为服务启动后的全局 body 捕获默认值，不改变普通成功请求采样率。命中配置后不做 body 摘要化或流式 body 省略，headers、queryString 和 body 均按原文保存，但仍受 `64MB` 活跃捕获硬上限、blob 压缩去重和窗口读取约束。
- `headers_sha256` 和 `body_sha256` 均针对压缩前的原始字节计算。
- payload blob 可以压缩存储，压缩算法、原始大小和压缩后大小必须记录。
- 相同 `sha256 + raw_size_bytes + content_type` 的 blob 只存一份，多条事件通过 `audit_payload_refs` 引用。
- 未完整保留正文时，`capture_status` 必须明确标记为 `summary_only`、`hash_only`、`expired`、`overflow` 或 `dropped`，不能伪装成完整原文。
- 单条请求超过活跃捕获上限或队列上限时，允许只保存轻量事件和降级原因；不能写入伪完整 payload。
- 错误聚合只影响展示和排障汇总，不删除 `audit_logs` 事件。

## 系统团队与统一授权存储

当前项目未正式上线，本地 SQLite 可以备份后直接重建或清洗，因此授权统一使用 `resource_authorization_grants`、`resource_authorizations` 和 `resource_authorization_sources`。

源码边界：

- `backend/src/storage/schema.ts` 作为 schema 入口，`backend/src/storage/schema/` 拆分文件只保留当前完整表结构、索引、默认约束和外键。
- 后端启动路径、repository、routes、前端页面都只维护当前数据契约，不挂载一次性数据处理、临时同步修复、临时表改名或处理标记代码。
- 本地库异常或结构变化时，先备份数据库，再按当前 schema 用直接 SQL、临时离线脚本或重建库处理；处理脚本不得挂入运行时代码。
- 本地库异常或结构变化需要处理既有数据时，单独生成一次性离线修复方案，不写进主代码。

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
- `status`：`active`、`paused`、`expired`、`revoked`、`returned`
- `remark`
- `expires_at`
- `limits_json`
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
- `status`：`active`、`paused`、`expired`、`revoked`、`returned`
- `effective_source_type`：当前生效来源，`manual`、`team` 或空
- `effective_source_team_id`：当前生效来源是团队时记录团队 ID，否则为空
- `activated_at`
- `last_source_changed_at`
- `remark`
- `expires_at`：可选自动回收时间，到期后状态变为 `expired`，记录保留。
- `limits_json`：美元成本额度限制 JSON，内部 `limit` 表示美元金额，支持 n 小时、日、周、月和累计总额度；为空表示不限制。
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

当前 `group_authorization_settings` 表：

- `authorization_id`：用户级分组授权 ID，主键，指向 `resource_authorizations.id`。
- `system_account_id`：被授权系统账户 ID。
- `group_id`：授权方原分组 ID。
- `enabled`：被授权人本地是否启用该授权分组，默认 `1`；来源分组停用时仍以来源停用为实际阻断。
- `group_type`：被授权人本地分组类型，当前为 `personal` 或 `high_concurrency`。
- `scheduling_policy_json`：被授权人本地高并发调度策略 JSON；`group_type` 不是 `high_concurrency` 时为空。
- `created_at`
- `updated_at`

约束：

- `resource_type = account` 时，`resource_id` 必须指向真实存在的 AI 账户，且 `resource_owner_system_account_id` 必须等于 `accounts.system_account_id`。
- `resource_type = group` 时，`resource_id` 必须指向真实存在的分组，且 `resource_owner_system_account_id` 必须等于 `groups.system_account_id`。
- `grantee_system_account_id` 必须指向存在的系统账户，且不能等于 `resource_owner_system_account_id`。
- 授权操作以 `resource_authorization_grants` 为业务主表；最终用户可调用关系以 `resource_authorizations` 为运行时主表；来源解释和优先级切换以 `resource_authorization_sources` 追踪。
- AI 账户授权实例必须在授权创建、团队成员加入或授权重新激活这类写路径中物化；账户列表、账户 options、账户详情和网关调度选号只能读取已物化的实例行，不得在请求读取路径按被授权人全量扫描 `resource_authorizations` 后补建实例。
- 团队授权由 service 层展开为成员用户级授权；资源所有者如果也是团队成员，其自用调用仍按自用处理，不计入授权消耗。
- 为避免团队越大导致管理写请求、授权创建、团队停用或成员变化在 DB service 中同步展开过久，系统团队采用固定展开边界：单个团队最多 20 个有效成员，单次最多添加 20 个成员，单个团队最多 20 条有效团队授权；团队列表页也固定最多返回 20 个团队。超过该边界应拆分团队或回收旧授权，不能在请求路径一次性展开更多成员或授权。
- 同一个 `resource_type + resource_id + grantee_system_account_id` 在 `resource_authorizations` 中只维护一条最终用户授权；同一资源同一业务目标的有效人员 / 团队授权由 `resource_authorization_grants` 的部分唯一索引兜底。
- 授权记录不提供普通物理删除动作；AI 账户授权实例创建后与归属人的原账户运行态解耦。来源账户被删除时只先逻辑删除来源账户和所有授权实例，授权操作记录与运行时授权标记为 `revoked`，一个月后由过期物理清理任务删除账户、授权、绑定、历史记录和统计缓存。被授权人归还个人直授权时，个人授权标记为 `returned`，来源账户不受影响；授权实例账户不能通过账户删除接口删除。
- 暂停授权把 `status` 改为 `paused`，被授权人仍可见但不可用；到期授权把 `status` 改为 `expired`，被授权人仍可见但不可用，独立 background worker 进程负责自动处理。
- 授权到期扫描按固定批量处理，单次最多推进 20 条到期授权；请求路径只做同样的短批次兜底，不能因为积压大量到期授权而一次性同步更新全部运行态。
- 回收授权把 `status` 改为 `revoked` 并写入 `revoked_by` / `revoked_at`，被授权人不可见也不可用；归还授权把 `status` 改为 `returned`，被授权人不可见也不可用。历史统计继续按原授权 ID 可查。
- 团队停用、成员移除或系统账户停用后，授权来源和最终用户授权状态必须同步更新；网关调度按最终用户授权、系统账户、资源和绑定状态阻断调用，不再额外把团队作为运行时主体遍历判断。
- 授权资源不能继续被被授权人二次授权给第三方。
- 被授权人编辑授权分组时只写 `group_authorization_settings`，不得更新来源 `groups` 行；列表、API Key 可绑定分组、API Key 运行态和网关调度都按当前调用方读取本地覆盖后的有效启用状态、分组类型和调度策略。

`group_accounts`、`api_keys` 和 `api_key_group_bindings` 的授权路径字段：

- `group_accounts.account_id` 指向当前使用方自己的可调度账户行；自有账户指向自有 `accounts` 行，授权账户指向被授权用户名下的授权实例账户行。`system_account_id` 表示这条分组绑定属于哪个使用方，本地分组绑定不改变原资源归属。
- `group_accounts.account_authorization_id`：当被授权用户把授权实例账户加入自己分组时记录对应用户级统一授权 ID；为空表示自有账户。
- `group_accounts.local_priority / local_super_priority_enabled / local_fallback_enabled`：当前使用方当前分组绑定的调度事实。被授权人调整排序、超级优先或降级备用时只更新这条绑定，不修改授权实例全局账号字段、来源账户或其他被授权人的绑定。
- 授权实例运行态以授权实例 `accounts` 行为准；冷却、最近错误和流式失败窗口不落在 `group_accounts` 本地绑定字段上。
- 来源 AI 账户删除时，来源账户和对应授权实例账户都只做逻辑删除并立即隐藏；对应 `resource_authorization_grants` 和 `resource_authorizations` 必须标记为 `revoked`，历史统计和用量在逻辑删除阶段继续保留原授权 ID；默认授权列表不再把它作为生效授权展示。超过 1 个月后，物理清理任务再删除授权实例、授权记录、绑定关系、历史记录和统计窗口。
- `api_keys` 不保存主号池字段；API Key 的分组路由事实只来自 `api_key_group_bindings`。
- `api_keys.availability_schedule_json` 保存 API Key 可用时段计划；为空表示不限制时段。该字段只作为网关门禁和列表展示事实，不驱动 `api_keys.status` 自动改写，也不写入统计缓存。
- `accounts.availability_schedule_json` 与 API Key 计划使用同一结构；账户计划作用在具体账户行上，授权实例账户按自己的实例行计划参与调度，来源账户计划作为来源账户可用性的一部分参与授权实例实际可用性判断。
- `api_key_group_bindings.api_key_id / group_id / priority / status` 保存 API Key 到多个可用分组号池的路由绑定；新建和编辑时可以绑定 API Key 所属系统账户自己的分组，也可以绑定有效授权给该系统账户的分组。至少保留一个 `active` 绑定，同一个 Key 下 active 绑定优先级唯一，同一个 Key 下所有绑定分组必须属于同一供应商协议档案。
- 授权实例账户通过 `group_accounts.account_authorization_id` 进入被授权用户自己的分组后参与调度；调度时必须重新校验最终用户授权、调用方系统账户、实例账户状态、实例绑定状态、额度缓存和来源账户可用性。上游凭据、`base_url`、模型、代理、并发、可用时段和账户追加流式规则从来源账户补齐，并随来源账户资源配置更新同步进入列表、账户测试和网关运行缓存；列表读取来源账户状态、调度开关、到期和错误摘要作为 `effectiveAvailability` 的来源侧阻断依据，但这些来源状态字段不能覆盖或回写授权实例自己的本地运行态。
- 当同一用户对同一账户或分组同时拥有个人来源和团队来源时，只允许存在一条用户级授权，并在资源行返回全部来源供排查。
- 调度只校验用户级授权是否仍有效；来源变化不应导致资源重复或历史绑定断裂。

`usage_records` 需要额外冗余这些字段：

- `traffic_source`：`gateway` 表示真实客户端网关请求，`manual_account_test` 表示手动账号测试，`cooldown_retest` 表示后台恢复探活。
- `account_owner_system_account_id`：命中账户所有者，自有账户时等于 `system_account_id`。
- `group_owner_system_account_id`：命中分组所有者，自有分组时等于 `system_account_id`。
- `account_access_type`：`owner`、`authorized`。
- `group_access_type`：`owner`、`authorized`；API Key 网关可绑定调用方自有分组或有效授权分组，授权分组访问必须走当前显式授权校验路径。
- `account_authorization_id`：命中授权 AI 账户时写入统一授权 ID，自有账户为空。
- `account_authorization_source_type`：命中授权 AI 账户时的生效来源快照，取 `manual` 或 `team`。
- `account_authorization_source_team_id`：命中授权 AI 账户且来源为团队时的团队 ID 快照。
- `group_authorization_id`：当前有效分组授权调用记录的统一授权 ID，自有分组为空。
- `group_authorization_source_type`：命中授权分组时的生效来源快照，取 `manual` 或 `team`。
- `group_authorization_source_team_id`：命中授权分组且来源为团队时的团队 ID 快照。

日志隔离和统计口径：

- 使用记录页按 `usage_records.system_account_id` 查询，所以被授权用户只看自己的调用明细。
- 资源所有者不按明细读取被授权用户的 `usage_records`，授权消耗明细只读取后台 worker 按统一授权 ID、团队快照、用户和资源筛选写入的日摘要与范围窗口表。
- 授权消耗统计必须过滤自用记录：账户授权按 `usage_records.system_account_id != account_owner_system_account_id`，分组授权按 `usage_records.system_account_id != group_owner_system_account_id`。
- 账户行总用量按账户行所属系统账户和 `account_id` 聚合；授权实例账户与归属人原账户分别统计，互不混入。分组授权命中来源账户时只进入调用方 `caller_account` 和授权报表，不进入资源归属人的普通账户 / 分组曲线。授权方查看被授权消耗时读取授权报表窗口，不把被授权实例或授权分组用量并入来源账户曲线。
- 分组真实总用量按 `group_owner_system_account_id + group_id` 聚合，所以自用和所有用户授权都会累计到同一个真实分组维度。
- API Key 绑定多个号池时，API Key 维度统计仍按同一个 `api_key_id` 聚合；分组维度统计只按实际命中的 `group_id` 聚合。
- 授权统计主口径是“资源 × 用户”：按用户级统一授权 ID 进入统计，worker 同步写入团队报表行和用户报表行，不再把个人来源和团队来源拆成两份。
- 个人账号授权的运行时用量按实际调用方 `system_account_id` 写入 `account_authorization` 统计；个人授权详情和授权给我的列表读取被授权人侧缓存，所以 A 授权给 B 时，B 能在自己的授权列表看到自己产生的用量，A 也只能通过授权报表看到 B 对该资源的消耗。
- 团队授权详情基于使用记录里的团队来源快照写入团队报表；团队总量和用户筛选结果在 worker 中预先生成，页面请求不能现场按成员或授权关系聚合。
- 团队来源变更只影响用户级授权来源摘要；成员移除后，如果该用户没有其他来源则授权失效，但历史用户消耗仍按该用户保留。
- 分组授权共享的是动态分组集合，但只共享分组所有者自有账户；如果分组里包含别人授权来的账户，调度时必须过滤，不能通过分组授权继续共享给第三方。
- `usage_records` 按每次实际上游请求 / 上游尝试写入；同一个客户端请求如果发生重试或切号，可以产生多条记录。`traffic_source` 用于区分真实网关请求、手动账号测试和恢复探活。`usage_records` 是排障和重建统计的事实源，不等同于业务消耗统计。
- `request_count` 统计的是进入业务统计口径的请求次数：后台统计 worker 聚合真实网关请求和手动账号测试，恢复探活 `traffic_source = cooldown_retest` 只保留明细和审计，不进入业务用量统计、账户质量统计或真实请求形态学习；统计安全游标可以确认处理恢复探活明细，避免阻塞后续 `usage_records` 清理。没有 token / cost 的失败记录按 0 token、0 cost 计入请求和错误次数，便于统一追踪；成本、缓存成本与 token 仍以实际解析到的上游用量和模型价格快照为准。
- 同一次上游尝试不能因为调用方同时拥有个人来源和团队来源而重复入库；授权统计事实按 `usage_records.id` 去重。
- 统计按请求实际命中的 `group_id`、`account_id` 和当时的统一授权 ID 记录，历史不会因后续 API Key 号池优先级、团队成员或分组账户变化而重算。
## 账户套餐到期

`accounts.account_expires_at` 是本地账户套餐/购买到期时间，适用于 OpenAI OAuth 和 GPT API Key 账户。它和 OAuth 凭据里的 `expires_at` 分离：`credentials.expires_at` 来自 OpenAI token 的 `expires_in`，只表示 access token 过期时间。

- 字段为空表示不设置本地套餐到期。
- 创建或编辑账户时如果填入过去时间，账户立即保存为 `disabled` 且 `schedulable = 0`。
- 列表、options、详情和相关恢复入口只允许按到期时间索引读取固定批量 ID，再按主键小批量写入“账户套餐已过期，已自动停用”；列表读取本身会按 `account_expires_at` 计算有效停用状态，因此不能为了展示一致性在请求链路无界更新全部过期账户。
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
- 收到 OpenAI OAuth / Codex 额度响应时，网关只被动更新额度快照；账号状态写入仍走账户错误处理策略或通用上游失败链路，不再内置 OAuth reset、状态码、错误码或文案判断。

## 错误兜底策略

账户添加和编辑可以维护账号自己的错误处理规则；账户凭据中的 `error_handling_rules` 缺省为空，未命中规则时走通用失败处理。规则只决定运行态确认失败后的目标状态是只切号、限流、临时不可调用还是异常；未命中策略或命中 `retry_next` 时，待确认目标为通用 `temporary_unavailable`。状态码、错误码和错误文案只作为诊断摘要、审计字段和账户错误处理策略输入，代码不再内置 OAuth reset、余额不足、限流或固定状态码判断。自有账户、分组授权账户和账户授权实例都只在确认失败后写各自 `accounts.status / cooldown_until / last_error_code / last_error_message`，不会写归属人原账户，也不会写 `group_accounts.local_*` 作为运行态。异常状态统一落到对应账户行的 `status = error`，异常类型或说明写入同一账户行的错误字段。停用和异常都是不可调度硬状态，网关异步成功/失败回写、冷却写入和 OAuth 刷新成功都不能自动恢复或降级覆盖这些硬状态；异常只能通过显式恢复清理。普通非 `2xx` 上游错误响应、上游请求异常、非流式正文中断和流式失败都会先把当前账号加入进程内短暂避让屏障，并继续切换后续账号；后续账号请求成功时，本次请求救回，前序失败账号保留运行态屏障并可进入来源级短期回避，不立即写 SQLite。全部候选账号失败时返回网关统一 `service_unavailable`，不把最后一个上游错误体返回给客户端。后续请求进入候选排序时先过滤本地屏蔽账号；若当前分组所有候选都被屏蔽，则先按 API Key 分组绑定顺序尝试后续可承接分组，没有后续分组时立即返回网关统一 `503` 并写入 `Retry-After`。本地屏蔽状态不写 SQLite，短暂避让按 `3s -> 5s -> 10s` 阶梯半开探测；服务重启或进程重启后运行态丢失并恢复普通调度。持久账号状态由账号事前确认、账户错误处理策略确认失败、后台复测、手动账号测试或人工操作写入当前命中的账户行，并且事前确认连续失败后仍要等当前账号并发归零才能落库。完整流程见 [网关异常重试与兜底策略](网关异常重试与兜底策略.md)。凡是决定放行给客户端的上游成功响应，都必须透传上游状态码、可透传响应头和原始响应体，不改写。

## 默认运行策略

全局设置默认写入：

- `appName = 聚合 AI`
- `appIcon = /__aisys__/brand-icon.svg`
- 全局设置只保存系统名称和系统图标路径，只有管理员可修改；登录页标题由系统名称派生，角标、副标题和首页样式按 [产品与品牌边界](../architecture/frontend/产品与品牌边界.md) 固定。

系统设置默认写入：

- `defaultTemporaryUnschedulableMinutes = 5`：临时不可调用恢复流程中的最大单次暂停时间；账号进入 `temporary_unavailable` 后先 3 秒快速恢复，连续失败后翻倍，慢速恢复单次等待不超过该上限。
- `temporaryUnschedulableRetryIntervalSeconds = 3`：普通上游请求异常或非 `2xx` 响应切号前，同账号原地确认重试之间的等待间隔；冷却恢复复测会显式覆盖为不做同账号重试。
- `temporaryUnschedulableRetryAttempts = 3`：普通上游请求异常或非 `2xx` 响应切号前，同账号原地确认重试次数；冷却恢复复测会显式覆盖为不做同账号重试。
- 本地短暂避让不落库、不使用 `defaultTemporaryUnschedulableMinutes`，固定按 `3s -> 5s -> 10s` 进程内阶梯执行；每阶到期后只允许一个真实请求半开探测，半开租约跟随请求并发生命周期释放，固定租约时间只用于无在途并发时回收孤儿租约；三阶半开仍失败后进入事前确认。真实网关流量中的代理 profile 已知不可用也只推进这套运行态阶梯，确认失败且账号并发归零前不写持久临时不可调用。
- `streamCircuitBreakerEnabled = true`：流式超时检测默认开启；真实网关流式失败先进入运行态屏障，确认失败且当前账号并发归零后才写持久账号状态。
- `streamRequestTimeoutSeconds = 180`：上游首包等待上限；非流式和流式在响应头前、非流式在 `2xx` 响应首字节前超过该时间时按上游请求异常切换后续账号；流式拿到 `2xx + SSE` 后，超过该时间没有收到任何上游 chunk 时补发失败事件并结束本次 SSE。
- `streamIdleTimeoutSeconds = 60`：流式响应首段内容后，没有任何上游 chunk 的输出停顿上限；只把 raw chunk 完全停顿作为硬超时，持续有 raw chunk 但暂未形成完整 SSE 事件时只记录诊断并继续转发。
- `streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：历史流失败计数参数；真实网关流式失败已改为单次失败即写账号状态。
- `statsAggregationIntervalSeconds = 60`：统计缓存默认增量汇总间隔。
- `statsAggregationBatchSize = 2000`、`statsAggregationMaxBatchesPerRun = 5`：统计缓存每轮聚合批量上限；连续批次之间让出事件循环，额度小时窗口随本任务刷新，排行和概览窗口快照由独立 worker 任务刷新。
- `groupAccountStatsRefreshIntervalSeconds = 60`：分组账户统计缓存默认刷新间隔。
- `systemMetricsSampleIntervalSeconds = 30`：系统监控默认采样间隔。
- `tableMonitorMaxTablesPerRun = 4`：表监控每轮每个库最多刷新多少张表级快照；设置为 `0` 时只采样文件级指标。后台表级采样只读取本轮表和索引大小，并通过 `dbstat` 叶子页 cell 数滚动写入可推导的行数，不执行精确 `COUNT(*)`。
- `modelCheckRetentionDays = 30`：模型检测历史和诊断明细默认保留 30 天。
- `usageRecordRetentionDays = 7`：自动保留任务默认保留使用记录 7 天，并等待统计游标已处理；表监控非业务数据硬清理不使用这个保留期设置。
- `usageStatsMinuteRetentionHours = 48`：分钟级统计缓存默认保留 48 小时。
- `usageStatsHourlyRetentionDays = 60`：小时级统计缓存默认保留 60 天，覆盖 AI 性能最近 31 天小时趋势和边界回查。
- `usageStatsDailyRetentionDays = 400`：日级统计缓存默认保留 400 天，覆盖一年内自然日查询和月表重建。
- `usageStatsWeeklyRetentionWeeks = 104`：周级统计缓存默认保留 104 周。
- `usageStatsMonthlyRetentionMonths = 24`：月级统计缓存默认保留 24 个月。
- `usageRankSnapshotRetentionDays = 30`：常用 TopN 排行快照默认保留 30 天。
- `systemMetricsRetentionDays = 7`、`systemMetricsHourlyRetentionDays = 30`：系统监控原始采样默认保留 7 天，小时汇总默认保留 30 天。
- `dataRetentionCleanupBatchSize = 1000`、`dataRetentionCleanupMaxBatchesPerRun = 2`：统一表数据清理任务的批量上限；运行时同样钳制到小批范围，异常偏大的配置不会让清理任务一次删除过多记录。
- OAuth 额度快照不再有后台主动刷新默认项；快照只由真实网关响应头和账户测试副作用被动更新。
- `operationLogEnabled = true`：默认启用操作日志。
- `operationLogRetentionDays = 365`：操作日志默认保留 365 天。
- `operationLogMaxChangesPerRecord = 100`：单条操作日志最多保存 100 个字段差异，超过后折叠摘要。
- `accountQualityRefreshIntervalSeconds = 600`：账号质量缓存默认每 10 分钟刷新一次。
- `accountQualityWindowMinutes = 10`：真实网关请求首 token 统计窗口默认 10 分钟。
- 账号质量主动探测相关默认设置已删除，不允许通过系统配置恢复。
- `cooldownAccountRetestMaxBackoffHours = 24`：冷却账号后台复测最长自动恢复观察窗口，默认 24 小时；进入慢速恢复后仍继续自动退避复测，超过该窗口后转为 `cooldown_retest_max_recovery_exceeded` 异常并停止自动复测。后台冷却复测固定启用，只用于冷却到期后的恢复性测试；请求形态只学习真实流量的低风险元信息，不复用用户请求内容。
- `cooldownAccountRetestIntervalSeconds = 3`：冷却账号后台复测默认扫描间隔，用于承接 3 秒起步的快速恢复通道。
- `cooldownAccountRetestBatchSize = 10`：默认每轮最多恢复性复测 10 个冷却到期账号。

原始审计日志与保全策略：

- 固定启用原始审计日志，不提供系统设置开关。
- 完全成功请求默认只按稳定桶采样 `10%` 进入原始审计；未命中采样时不写 `audit_logs`。
- 每次请求事实由 `usage_records` 保底，原始审计不替代使用记录。
- 失败、异常、重试后成功、客户端中断和流式中断默认进入原始审计，并按策略保全可捕获正文。
- 默认正文保全按成功样本 `512KB`、问题链路 `2MB` 分档；超限后写 `summary_only` 摘要，不把摘要伪装成完整原文。
- 临时全量捕获可通过审计日志页面弹窗短时运行期配置；`JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED=true` / `1` 只决定启动默认的全局 body 捕获状态。页面配置可定向指定 AI 账户，并可让命中范围内的普通成功请求进入原始审计；命中后不做 body 摘要化或流式正文省略，凭据类 headers 和 queryString 同样按原文保留。
- 正文 blob 压缩后按原始 hash 精确去重，并通过 payload 引用关联到事件。
- 重复错误按短时间窗口聚合展示，但每次 occurrence 仍由 `audit_logs` 事件追溯。
- 问题列表 / 审计事件列表不新增 payload 字节列；`raw_payload_bytes` 和 `compressed_payload_bytes` 只用于后端报表、容量分析和内部接口字段。
- 审计队列固定每 `5` 秒批量落库一次，单批最多 `200` 条客户端请求。
- 待写队列不设固定请求数或字节数上限，由机器资源决定承载能力；队列异常不能阻塞网关请求。
- 单个请求活跃捕获上限为 `64MB`，超过后丢弃整条审计记录。
- 默认固定保留：成功样本 `7` 天，失败 / 异常事件 `30` 天，失败 / 异常正文随事件引用分层清理，错误聚合组 `30` 天；具体以 [审计日志保全策略设计](审计日志保全策略设计.md) 为准。

系统设置接口只返回并写入当前白名单内的键；读取时在 SQL 层按固定 `systemSettingKeys` 白名单查询，不先读出同一系统账户下的全部设置再由 JS 过滤。本地库如果存在不在白名单内的键，直接通过备份后清洗或重建库处理。源码不保留启动清理分支，运行时也不会读取这些键。

## 系统账户隔离补充

- `accounts`、`system_teams`、`system_team_members`、`resource_authorization_grants`、`resource_authorizations`、`resource_authorization_sources`、`groups`、`group_authorization_settings`、`group_accounts`、`api_keys`、`api_key_group_bindings`、`usage_records`、`operation_logs`、`operation_log_targets`、`operation_log_viewers`、`audit_logs`、`account_usage_snapshots` 都按 `system_account_id` 或明确的 owner/grantee/viewer 字段隔离；`system_settings` 当前按默认超级管理员作用域保存系统级运行策略；账户错误处理策略跟随 `accounts.credentials`，按账户所属作用域隔离。
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
- 操作日志中明确传入的敏感字段变更详情

本地网关 API Key 的完整明文在创建成功时返回，也可通过单条完整密钥读取接口按资源权限返回；列表和更新响应只返回空 `key` 以及 `key_prefix` / `key_suffix` 组成的安全标识，不能批量暴露完整密钥。授权账户和授权分组接口不能返回完整密钥，只能返回列表摘要和必要状态。数据库中必须通过 `key_secret_encrypted` 密文保存本地 API Key，`key_hash` 用于网关校验，`key_prefix` 和 `key_suffix` 用于摘要展示；API Key 列表通用搜索只按名称匹配。缺少 `key_secret_encrypted` 或密文不可解的数据不进入运行时，应停机离线修复或重建 API Key。

API Key 额度配置不属于敏感凭据，保存在 `api_keys.quota_limits_json`：空值表示不限制，JSON 内 `limit` 表示美元金额；日额度按服务端本地自然日 0 点重置，周额度按周一 0 点重置，月额度按每月 1 号 0 点重置，总额度读取累计 `total_cost_usd` 缓存。网关只读取 API Key 维度统计缓存判断美元成本额度，不回扫明细表，也不做实时扣减。API Key 可用时段计划也不属于敏感凭据，保存在 `api_keys.availability_schedule_json`；计划以分钟为粒度判断，网关运行态缓存命中计划边界时最多保留到下一分钟，避免时段切换长时间滞后。

外部来源授权 token 在创建响应中返回完整明文，也可通过单条 token 复制接口按管理员权限读取；业务库 `external_integration_source_tokens` 保存带用途前缀的 SHA-256 摘要 `token_hash`、完整 token 密文 `token_secret_encrypted`、安全展示用 `token_prefix` / `token_suffix`、状态、scope 和过期时间。每一个公开接口都是独立资源 scope，来源授权和 token 都必须包含目标接口 scope，才允许调用。默认种子会写入固定来源 `extsrc_builtin_test` 和固定 token `exttok_builtin_test` 作为内置测试 Token，完整明文只加密保存在业务库，可在公开接口授权列表按管理员权限复制；接入文档和 curl 示例只展示 `<source_token>` 占位符，不返回内置测试 Token 明文。内置测试来源固定授权当前所有公开接口 scope，固定限频 `60s/10次`，只返回公开接口 mock 数据；允许停用和重置，不允许编辑名称、scope、限频、到期时间、备注、新增 token 或删除。普通日志、运行日志、错误响应、操作记录和 demo 成功响应都不能输出真实明文 token 或 token hash。

更完整的凭据展示、请求快照、操作日志、原始审计日志、日志原文保留、数据保留和备份迁移规则见 [安全与日志策略](安全与日志策略.md)、[操作日志设计](操作日志设计.md) 与 [原始审计日志设计](原始审计日志设计.md)。
