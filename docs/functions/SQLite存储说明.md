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
- 原始审计日志使用独立表保存完整链路原文；网关请求链路只能终态入队，后台批量写库，不能同步写审计表
- 普通运行日志仍以 JSON Lines 写入日志文件并滚动清理；搜索能力使用 SQLite 索引表 `runtime_logs` 和 FTS5 表 `runtime_log_search`，不在查询时扫描日志文件。
- 系统团队、团队成员和统一资源授权使用独立表记录；授权不复制账户凭据，授权资源调用时使用记录按实际调用方隔离，同时冗余资源所有者、授权关系和授权对象用于聚合统计。
- `accounts.account_expires_at` 保存可选的本地套餐/账号购买到期时间；为空表示不过期，到期后账户自动改为停用并退出调度。
- 登录验证码挑战暂不写入 SQLite，使用后端进程内存保存短时一次性验证码；过期和已提交的挑战会被清理。
- 登录失败限频和账号临时锁定暂不写入 SQLite，使用后端进程内存保存短时窗口和锁定状态；后续多实例部署时再迁移到共享存储。

## 统计缓存与监控存储

个人或几个人使用时，统计缓存也优先放在 SQLite 内，不额外引入 Redis、ClickHouse 或 Prometheus。`usage_records` 仍是事实源，但账户列表、分组列表、管理员统计概览和监控图都应读取缓存表。缓存表属于业务数据缓存，必须按 `system_account_id` 隔离；统计概览和主机级系统监控属于管理员视角，默认只给管理员看。

建议新增表：

- `usage_stats_totals`：按 `system_account_id + scope_type + scope_id` 保存累计请求、成功、错误、token、成本、平均响应所需的求和字段。
- `usage_stats_daily`：按 `system_account_id + scope_type + scope_id + stat_date` 保存业务统计，用于账户、分组、API Key 和授权用量的自然日口径。
- `usage_stats_hourly`：按 `system_account_id + scope_type + scope_id + stat_hour` 保存业务统计，用于统计概览近一天、近三天、近一周和近一月监控窗口趋势。
- `usage_model_daily`：按 `system_account_id + stat_date + model` 保存请求数、Token 和成本，用于自然日模型分布。
- `usage_model_hourly`：按 `system_account_id + stat_hour + model` 保存小时级模型分布，用于统计概览监控窗口。
- `usage_error_daily`：按 `system_account_id + stat_date + error_group + error_code` 保存错误数量，用于自然日错误情况。
- `usage_error_hourly`：按 `system_account_id + stat_hour + error_group + error_code` 保存小时级错误数量，用于统计概览监控窗口。
- `group_account_stats`：按 `system_account_id + group_id` 保存分组绑定账户数量、可用数、状态数量和并发上限，供分组列表直接读取。
- `system_metrics_samples`：按采样时间保存 CPU、内存、RSS、Heap、事件循环延迟、网络入站/出站吞吐、网卡累计收发、数据库文件大小和统计滞后。
- `system_metrics_hourly`：把采样数据按小时聚合为平均值、最大值和最小值；网络吞吐平均值按有效网络速率样本数计算，避免采样端暂不可用时被按 0 稀释。
- `stats_job_state`：记录后台任务的作用域、游标、上次成功时间、上次错误和滞后秒数；业务统计作用域为 `system_account`，主机监控作用域为 `global`。
- `audit_logs`：保存每次被采样或非成功客户端请求的审计元数据，用于后台页面检索。
- `audit_log_attempts`：保存审计请求下每次上游尝试、命中账号、代理、状态码和错误摘要。
- `audit_log_payloads`：保存客户端请求、上游请求、上游响应和最终网关响应的完整原文，建议加密存储。
- `runtime_logs`：保存最近 3 天普通运行日志的可检索字段和原始 JSON 行，用于管理员“日志搜索”页面。
- `runtime_log_search`：FTS5 `trigram` 虚表，保存普通运行日志关键字段和原始 JSON，用于关键字检索。

建议索引：

- `system_accounts(lower(username))`：保证用户账户大小写不敏感唯一，用户账户创建后不允许修改。
- `system_accounts(lower(display_name))`：保证用户名称大小写不敏感唯一。
- `usage_records(system_account_id, created_at, id)`：统计 worker 增量扫描。
- `usage_records(account_owner_system_account_id, account_id, created_at, id)`：账户所有者查看真实账户统一用量。
- `usage_records(group_owner_system_account_id, group_id, created_at, id)`：分组所有者查看真实分组统一用量。
- `usage_records(account_owner_system_account_id, account_authorization_id, created_at, id)`：授权管理按账户授权关系聚合用量。
- `usage_records(group_owner_system_account_id, group_authorization_id, created_at, id)`：授权管理按分组授权关系聚合用量。
- `system_teams(name)`：保证团队名称唯一。
- `system_teams(status, name)`：团队列表和授权对象选择器读取。
- `system_team_members(team_id, system_account_id, status)`：校验团队成员有效性。
- `system_team_members(system_account_id, status)`：查询当前系统账户所属团队。
- `resource_authorizations(resource_owner_system_account_id, resource_type, resource_id, status)`：资源所有者查看授权名单。
- `resource_authorizations(grantee_system_account_id, status)`：查询系统账户可用授权。
- `resource_authorizations(resource_type, resource_id, grantee_system_account_id, status)`：防止同一资源重复有效授权给同一用户。
- 有效资源查询需要在 service 层按 `resource_type + resource_id + caller_system_account_id` 去重；团队来源只能合并到用户授权来源摘要，不能展开成多条 AI 账户或分组。
- `usage_records(system_account_id, first_token_ms, created_at, id)`、`usage_records(system_account_id, duration_ms, created_at, id)`、`usage_records(system_account_id, cost_usd, created_at, id)`：使用记录页按首 token、总耗时、成本排序时只取有限窗口，避免大数据量下前端全量排序或数据库临时排序。
- `usage_records(first_token_ms, created_at, id)`、`usage_records(duration_ms, created_at, id)`、`usage_records(cost_usd, created_at, id)`：管理员查看全部系统账户时的全局排序索引。
- `usage_stats_totals(system_account_id, scope_type, scope_id)`：列表读取累计值。
- `usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)`：今日和天级趋势读取。
- `usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)`：小时趋势读取。
- `usage_model_daily(system_account_id, stat_date, model)`：模型分布读取。
- `usage_error_daily(system_account_id, stat_date, error_group, error_code)`：错误分布读取。
- `usage_model_hourly(system_account_id, stat_hour, model)`：监控窗口模型分布读取。
- `usage_error_hourly(system_account_id, stat_hour, error_group, error_code)`：监控窗口错误分布读取。
- `group_account_stats(system_account_id, group_id)`：分组列表读取账户数量与状态统计。
- `stats_job_state(scope_type, scope_id, job_name)`：后台任务游标读取。
- `audit_logs(created_at, id)`：审计日志默认分页。
- `audit_logs(system_account_id, created_at, id)`：管理员按调用方筛选审计日志。
- `audit_logs(audit_outcome, created_at, id)`、`audit_logs(final_status_code, created_at, id)`：按结果和状态码筛选。
- `audit_logs(path, created_at, id)`、`audit_logs(api_key_id, created_at, id)`、`audit_logs(group_id, created_at, id)`、`audit_logs(account_id, created_at, id)`：按接口、API Key、分组和账号定位问题。
- `audit_logs(trace_id)`：按链路 ID 追踪完整链路。
- `audit_log_attempts(audit_log_id, attempt_index)`：审计详情按尝试顺序读取。
- `audit_log_payloads(audit_log_id, part_type, sequence_index)`：审计详情按阶段和顺序读取原文。
- `audit_log_payloads(audit_log_id, sequence_index)`：审计详情按真实捕获顺序展示完整链路片段。
- `runtime_logs(trace_id, time, id)`：按链路 ID 快速抓取同一次请求相关运行日志。
- `runtime_logs(time, id)`：默认读取最近日志和按时间范围检索。
- `runtime_logs(level, time, id)`、`runtime_logs(event, time, id)`：按通用日志级别和事件定位问题；接口、状态码和客户端 IP 等请求维度只放在审计日志中查询。

默认任务策略：

- 所有定时和批处理任务都必须在独立 background worker 进程内调度和执行，不能在 Web/API 主进程里用 `setInterval`、cron 或调度框架直接执行任务函数。
- 调度框架只负责 worker 进程内的注册、不可重入、错误隔离和触发时机；worker 不能通过 IPC 回到主进程执行统计、清理、刷新或批量落库。
- 主进程可以把请求链路产生的待处理数据投递给 worker，但 IPC 或等价通道必须有上限，满载时按任务安全等级丢弃或降级，不能阻塞正常请求。
- 统计 worker 每 1 分钟按 `system_account_id` 和 `(created_at, id)` 游标增量读取 `usage_records` 并 upsert 到聚合表。
- 用量统计菜单的多日窗口只读取 `usage_stats_daily` 的日累计行并按窗口相加：近 1 天、近 3 天、近一周、近半月和近一月分别对应最近 1 / 3 / 7 / 15 / 30 个自然日；总用量读取 `usage_stats_totals`。前端不能把 `n` 天作为查询条件去实时回扫 `usage_records`。
- 统计概览属于监控窗口，不使用 0 点重置的今日口径；默认展示近一天，并支持近一天、近三天、近一周和近一月筛选。概览摘要、趋势、模型分布和错误 Top 10 均从小时级缓存表按窗口相加，不读取 `usage_stats_totals`，也不临时回扫 `usage_records`。
- 分组账户统计 worker 定时重建 `group_account_stats`，分组列表不得在查询时临时 `COUNT/SUM group_accounts + accounts`。
- 授权账户调用需要同时写入调用方统计、真实账户统计和授权消耗统计：调用方列表、分组、API Key 和日志按 `system_account_id` 聚合；账户所有者的账户总用量按 `account_owner_system_account_id + account_id` 聚合；授权管理按 `account_owner_system_account_id + account_authorization_id` 聚合，并过滤资源归属人自用消耗。
- 授权分组调用需要同时写入调用方统计、真实分组统计和授权消耗统计：调用方 API Key 和日志按 `system_account_id` 聚合；分组所有者的分组总用量按 `group_owner_system_account_id + group_id` 聚合；授权管理按 `group_owner_system_account_id + group_authorization_id` 聚合，并过滤资源归属人自用消耗。
- 授权管理列表和用量明细只读取 `usage_stats_daily` / `usage_stats_totals` 缓存；前端查询和详情接口不能临时 `SUM usage_records`，否则会把高频统计压力转移到页面请求。
- 管理员全局汇总读取后台写入的 `system_account = global` 缓存行，不在概览接口里临时汇总多个系统账户缓存行，更不能回扫 `usage_records`。
- 系统采样 worker 每 10 到 30 秒写入一次 `system_metrics_samples`，不写用户级业务归属。
- 审计日志 worker 每隔短时间或达到批量阈值后，从 worker 队列取终态审计记录并用单事务批量写入 `audit_logs`、`audit_log_attempts` 和 `audit_log_payloads`。
- 网关请求处理中不能同步写 `audit_logs`；SSE 和其他流式响应必须等自然结束、失败、超时或客户端断开后，才按终态记录入队。
- 运行日志索引 worker 从 Pino JSONL 输出流旁路接收日志行，按 worker 队列批量写入 `runtime_logs` 和 `runtime_log_search`；索引失败只能写 `stderr`，不能再通过普通 logger 递归打日志。
- “日志搜索”索引查询必须先使用 `traceId`、级别、事件和时间范围等通用索引条件缩小结果，关键字走 FTS5 `trigram` 虚表；列表默认展示最近 100 条并通过后端分页继续翻页。
- “日志搜索”的 `grep 模式` 直接扫描后端日志目录中当前保留的全部 `.log` 文件，不受索引表 3 天保留期限制，不设置查询超时，最多展示 100 行；全平台统一要求安装 `ripgrep`（`rg`），后端按文件更新时间从近到远、按文件末尾到开头的顺序返回最新匹配。
- 审计队列是 best-effort 队列，系统重启、进程崩溃或队列溢出导致审计记录丢失可以接受；队列丢弃计数应进入运维监控。
- 账户、分组、API Key 等列表接口只读 `usage_stats_totals` / `usage_stats_daily`，不要在列表查询里 `SUM usage_records`。
- 概览图表接口优先读 `usage_stats_hourly`、`usage_model_hourly`、`usage_error_hourly` 和 `system_metrics_hourly`。
- 全局规则：除独立 background worker、离线清洗脚本、使用记录分页明细外，任何前端列表、概览、详情和下拉元数据接口都不能在请求时做统计聚合。

默认保留策略：

- `usage_stats_hourly`、`usage_model_hourly`、`usage_error_hourly` 至少保留 30 天。
- `usage_stats_daily`、`usage_model_daily`、`usage_error_daily` 保留 180 天。
- `usage_stats_totals` 长期保留。
- `system_metrics_samples` 保留 7 到 14 天，`system_metrics_hourly` 可保留 180 天。
- `audit_logs`、`audit_log_attempts`、`audit_log_payloads` 默认保留 7 天，后续可按系统设置调整；清理审计日志不影响使用记录、统计缓存和 OAuth 额度快照。
- `runtime_logs` 和 `runtime_log_search` 固定只保留最近 3 天；清理索引不删除日志文件，只影响“日志搜索”页面可检索范围。
- 审计日志和运行日志索引的保留期清理由后台任务每天运行一次；页面不提供手动清理入口。
- 如果统计缓存损坏，可以运行 `pnpm --filter juhe-ai-backend stats:rebuild` 从 `usage_records` 重新构建缓存，不需要删除原始使用记录。该命令会清空并重建当前 `.env` 指向数据库里的统计缓存，执行前应确认数据库路径并先备份。

## 原始审计日志存储

原始审计日志是独立于使用记录的高权限排障数据，具体行为见 [原始审计日志设计](原始审计日志设计.md)。

源码边界：

- `schema.ts` 中只保留当前 `audit_logs`、`audit_log_attempts`、`audit_log_payloads` 表结构和索引，不保留旧审计结构兼容分支。
- 网关模块只创建请求内捕获上下文、追加原始片段和终态投递，不直接调用 repository 同步写审计表。
- 独立 background worker 进程里的审计批量落库服务负责从 worker 队列取终态记录，并在一个事务里写入主表、尝试表和 payload 表。
- 审计 payload 不参与统计 worker，不作为用量事实源。

建议新增 `audit_logs` 表：

- `id`
- `trace_id`
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
- `capture_status`：`complete`、`dropped`、`overflow`
- `started_at`
- `ended_at`
- `duration_ms`
- `first_token_ms`
- `created_at`

建议新增 `audit_log_attempts` 表：

- `id`
- `audit_log_id`
- `attempt_index`
- `account_id`
- `account_owner_system_account_id`
- `group_id`
- `proxy_id`
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

建议新增 `audit_log_payloads` 表：

- `id`
- `audit_log_id`
- `attempt_id`
- `part_type`：`client_request`、`upstream_request`、`upstream_response`、`gateway_response`、`gateway_error`
- `sequence_index`
- `content_type`
- `content_encoding`
- `headers_encrypted`
- `body_encrypted`
- `body_sha256`
- `size_bytes`
- `created_at`

保存规则：

- `headers_encrypted` 和 `body_encrypted` 保存完整原文，不脱敏、不截断、不改写。
- 加密存储优先复用 `JUHE_AI_SECRET` 派生的应用层加密能力；复用旧数据库时必须保持该密钥稳定，否则原始审计 payload 无法解密。
- 单条请求超过活跃捕获上限或队列上限时，不能写入伪完整记录；允许丢弃整条审计记录并增加队列丢弃计数。
- 完全成功请求默认只保存 10%；非成功、客户端中断、流式中断和重试后成功请求全量保存。

## 系统团队与统一授权存储

当前项目未正式上线，本地 SQLite 可以备份后直接重建或清洗，因此新版授权统一使用 `resource_authorizations`，不保留旧 `account_authorizations` / `group_authorizations` 分支。

源码边界：

- `backend/src/storage/schema.ts` 只保留当前完整表结构、索引、默认约束和外键。
- 后端启动路径、repository、routes、前端页面都不能长期保留一次性迁移、旧数据兼容、临时同步修复、临时表改名或迁移标记代码。
- 本地库异常或结构变化时，先备份数据库，再用直接 SQL、临时离线脚本或重建库处理；处理脚本不得挂入运行时代码。
- 正式上线后如需支持外部用户升级，再另行设计版本化 schema 演进，不和当前预上线规则混用。

建议新增 `system_teams` 表：

- `id`
- `name`
- `description`
- `status`：`active`、`disabled`
- `created_by`
- `created_at`
- `updated_at`

建议新增 `system_team_members` 表：

- `id`
- `team_id`
- `system_account_id`
- `member_role`：第一阶段固定 `member`
- `status`：`active`、`removed`
- `joined_at`
- `removed_at`
- `created_by`
- `created_at`
- `updated_at`

建议新增 `resource_authorizations` 表：

- `id`
- `resource_type`：`account`、`group`
- `resource_id`
- `resource_owner_system_account_id`
- `grantee_system_account_id`：最终被授权系统账户
- `source_type`：`manual`、`team`
- `source_team_id`：团队来源 ID，手动个人授权为空
- `scope`：第一阶段固定为 `use`
- `expires_at`：可选自动回收时间，到期后状态变为 `expired`，记录保留。
- `limits_json`：预留字段，第一阶段不启用。
- `model_policy_json`：预留字段，第一阶段不启用。
- `status`：`active`、`paused`、`expired`、`revoked`
- `remark`
- `created_by`
- `created_at`
- `revoked_by`
- `revoked_at`
- `updated_at`

约束：

- `resource_type = account` 时，`resource_id` 必须指向真实存在的 AI 账户，且 `resource_owner_system_account_id` 必须等于 `accounts.system_account_id`。
- `resource_type = group` 时，`resource_id` 必须指向真实存在的分组，且 `resource_owner_system_account_id` 必须等于 `groups.system_account_id`。
- `grantee_system_account_id` 必须指向存在的系统账户，且不能等于 `resource_owner_system_account_id`。
- 团队授权由 service 层展开为成员用户级授权；资源所有者如果也是团队成员，其自用调用仍按自用处理，不计入授权消耗。
- 同一个 `resource_type + resource_id + grantee_system_account_id` 同一时间只能存在一条 `active` 授权；SQLite 可用部分唯一索引或 service 层事务校验实现。
- 收回授权只把 `status` 改为 `revoked` 并写入 `revoked_by` / `revoked_at`，不物理删除，历史统计继续可查。
- 到期授权只把 `status` 改为 `expired`，不物理删除，独立 background worker 进程负责自动处理。
- 团队停用、成员移除或系统账户停用后，网关实时校验应立即阻断团队授权使用权。
- 授权资源不能继续被被授权人二次授权给第三方。

`group_accounts` 和 `api_keys` 的授权路径字段：

- `group_accounts.account_id` 仍指向唯一物理 AI 账户；同一 `system_account_id + group_id + account_id` 只能有一条有效分组成员关系，不能因多个授权来源重复加入同一账户。
- `group_accounts.account_authorization_id`：当被授权用户把授权 AI 账户加入自己分组时记录首选统一授权 ID；为空表示自有账户。
- `api_keys.group_id` 仍指向唯一分组；同一个授权分组不能因为个人来源和团队来源重复出现在 API Key 绑定选项。
- `api_keys.group_authorization_id`：当 API Key 绑定授权分组时记录首选统一授权 ID；为空表示绑定自有分组。
- 这两个字段只记录当时选择的授权路径，调度时必须重新校验授权、团队、成员、资源状态，不能只信任历史绑定。
- 当同一用户对同一账户或分组同时拥有个人来源和团队来源时，只允许存在一条用户级授权，并在资源行返回全部来源供排查。
- 调度只校验用户级授权是否仍有效；来源变化不应导致资源重复或历史绑定断裂。

`usage_records` 需要额外冗余这些字段：

- `account_owner_system_account_id`：命中账户所有者，自有账户时等于 `system_account_id`。
- `group_owner_system_account_id`：命中分组所有者，自有分组时等于 `system_account_id`。
- `account_access_type`：`owner`、`authorized`。
- `group_access_type`：`owner`、`authorized`。
- `account_authorization_id`：命中授权 AI 账户时写入统一授权 ID，自有账户或仅通过授权分组访问时为空。
- `group_authorization_id`：命中授权分组时写入统一授权 ID，自有分组为空。
- `authorization_source_summary`：可选快照，记录当时用户授权来自个人、团队或两者合并。

日志隔离和统计口径：

- 使用记录页按 `usage_records.system_account_id` 查询，所以被授权用户只看自己的调用明细。
- 资源所有者不按明细读取被授权用户的 `usage_records`，授权管理只读取后台 worker 按统一授权 ID 写入的统计缓存。
- 授权消耗统计必须过滤自用记录：账户授权按 `usage_records.system_account_id != account_owner_system_account_id`，分组授权按 `usage_records.system_account_id != group_owner_system_account_id`。
- 账户真实总用量按 `account_owner_system_account_id + account_id` 聚合，所以自用和所有用户授权都会累计到同一个真实账户维度。
- 分组真实总用量按 `group_owner_system_account_id + group_id` 聚合，所以自用和所有用户授权都会累计到同一个真实分组维度。
- 授权统计主口径是“资源 × 用户”：按用户级统一授权 ID 聚合，不再把个人来源和团队来源拆成两份。
- 个人授权详情需要按实际调用方 `system_account_id` 聚合，所以 A 授权给 B 时，A 能看到 B 对该资源的消耗。
- 团队授权详情从用户级授权集合聚合：团队总量是成员用户消耗去重汇总，成员明细按 `system_account_id` 展示每个用户在该资源上的消耗。
- 团队来源变更只影响用户级授权来源摘要；成员移除后，如果该用户没有其他来源则授权失效，但历史用户消耗仍按该用户保留。
- 分组授权共享的是动态分组集合，但只共享分组所有者自有账户；如果分组里包含别人授权来的账户，调度时必须过滤，不能通过分组授权继续共享给第三方。
- `usage_records` 按每次实际上游请求 / 上游尝试写入；同一个客户端请求如果发生重试或切号，可以产生多条记录。`usage_records` 是排障和重建统计的事实源，不等同于业务消耗统计。
- `request_count` 统计的是有效上游消耗次数：后台统计 worker 只聚合成功记录、已经拿到流式有效输出（首 token）的记录，或已经产生 token / cost 的记录。授权不可用、暂停、过期、无可用上游账号、网络连接失败、无 token / cost 的上游 HTTP 错误、首包前超时 / 中断和只收到流式错误事件等排障记录可以留在 `usage_records`，但不进入 `usage_stats_*`，也不计入授权消耗。
- 同一次上游尝试不能因为调用方同时拥有个人来源和团队来源而重复入库；资源真实总量按 `usage_records.id` 去重。
- 统计按请求实际命中的 `group_id`、`account_id` 和当时的统一授权 ID 记录，历史不会因后续团队成员或分组账户变化而重算。
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
- 独立 background worker 进程中的 `oauth_usage_snapshot_refresh` worker 统一扫描缺失、过期或接近恢复点的 OpenAI OAuth 账户并刷新快照。
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
- 刷新调度和上游探测只允许在独立 background worker 进程内执行。
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
- `streamIdleTimeoutSeconds = 60`、`streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：流式响应异常的轻量阈值。
- `statsAggregationIntervalSeconds = 60`：统计缓存默认增量汇总间隔。
- `systemMetricsSampleIntervalSeconds = 30`：系统监控默认采样间隔。
- `usageStatsHourlyRetentionDays = 30`、`usageStatsDailyRetentionDays = 180`、`systemMetricsRetentionDays = 14`：默认缓存保留时长。
- `oauthUsageSnapshotRefreshIntervalSeconds = 300`：OAuth 额度快照后台扫描间隔。
- `oauthUsageSnapshotTtlSeconds = 900`：OAuth 额度快照默认过期时间。
- `oauthUsageSnapshotRetryBackoffSeconds = 300`：OAuth 额度快照刷新失败后的默认退避。
- `oauthUsageSnapshotPerAccountConcurrency = 1`：每个系统账户内 OAuth 快照刷新默认并发。
- `statsLagWarningSeconds = 300`：统计任务滞后超过该值时在运维概览里提示。
- `auditLogEnabled = true`：默认启用原始审计日志。
- `auditLogSuccessSampleRate = 0.1`：完全成功请求默认保存 10%。
- `auditLogFlushIntervalSeconds = 5`：审计队列默认每 5 秒批量落库一次。
- `auditLogBatchSize = 50`：审计队列单批最多写入 50 条客户端请求。
- `auditLogQueueMaxItems = 1000`：待写队列最多保留 1000 条终态审计请求。
- `auditLogQueueMaxBytesMb = 256`：待写队列近似最大原文体积，单位 MB。
- `auditLogActiveCaptureMaxBytesMb = 64`：单个请求活跃捕获上限，单位 MB，超过后丢弃整条审计记录。
- `auditLogRetentionDays = 7`：原始审计日志默认保留 7 天。

预上线阶段如本地库仍存在不再展示的 `defaultErrorPolicyId`、`streamFailureAction`、`streamAccountCooldownMinutes`、`overloadCooldownEnabled`、`overloadCooldownMinutes` 等旧设置，直接通过备份后清洗或重建库处理；源码不保留启动清理分支。

## 系统账户隔离补充

- `accounts`、`system_teams`、`system_team_members`、`resource_authorizations`、`groups`、`group_accounts`、`api_keys`、`error_policies`、`usage_records`、`audit_logs`、`account_usage_snapshots`、`system_settings` 后续都会按 `system_account_id` 或明确的 owner/grantee 字段隔离。
- `usage_stats_totals`、`usage_stats_daily`、`usage_stats_hourly`、`usage_model_daily`、`usage_model_hourly`、`usage_error_daily`、`usage_error_hourly` 也必须按 `system_account_id` 隔离。
- `providers`、`proxy_profiles`、`global_settings`、`system_metrics_samples`、`system_metrics_hourly` 保持全局共享；`providers` 和 `proxy_profiles` 只允许管理员维护，主机级系统监控默认仅管理员可见。
- 管理员可以读取所有系统账户的数据；普通用户只读取自己的系统账户数据，以及其他用户主动授权给自己的 AI 账户和分组使用摘要。
- 原始审计日志虽然带有 `system_account_id`，第一阶段仍仅管理员可读取；普通用户不能通过审计日志接口查看自己的完整原文请求。

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码
- 原始审计日志 payload 中的完整 headers 和 body

这是单人自用系统，自有账户接口会返回前端需要展示的完整密钥；授权账户和授权分组接口不能返回完整密钥，只能返回列表摘要和必要状态。数据库中仍尽量加密保存。

API Key 明文只在创建时返回一次。

更完整的凭据展示、请求快照、原始审计日志、日志脱敏、数据保留和备份迁移规则见 [安全与日志策略](安全与日志策略.md) 与 [原始审计日志设计](原始审计日志设计.md)。




