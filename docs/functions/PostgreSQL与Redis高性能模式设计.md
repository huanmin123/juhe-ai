# PostgreSQL 与 Redis 高性能模式设计

> 本文是项目唯一存储形态——PostgreSQL + Redis——的存储、运行态与部署设计契约。2026-10-24/25 存储形态收口（见下）后，SQLite 与内存本地模式已物理退役，gateway / jobs / maintenance 三个 Go 项目只支持 PostgreSQL 事实库 + Redis 缓存 / 运行态这一种装配；现行部署 env 契约以 `docker/single-server/README.md` 与 `docs/deploy/部署指南.md` 为准。
> Node 时代拓扑（DB service、Node worker、Redis Streams 任务队列、PgBouncer、三个物理 Redis 实例、`JUHE_AI_ENV_FILE` 分层）已随迁移退役，不再作为现行叙述。
> 数据库、缓存、运行态和队列的业务语义适配边界见 存储适配接口设计（Node 时代文档，已删除，git 历史可溯）。
> 统计准确性、读写资源隔离、Redis 清理和压测验收的细化规则见 [可靠统计与读写资源隔离设计](可靠统计与读写资源隔离设计.md)。
> 管理后台页面 revision、字段投影、统一确认、IndexedDB 和最近登录用户预热见 页面数据缓存与增量更新设计（已清理，git 历史可溯）。

## 存储形态收口决策记录（2026-10-25）

- **2026-10-24/25 收口决策**：SQLite 本地存储与进程内 memory 驱动模式彻底退役，全仓唯一存储形态为 PostgreSQL + Redis。本决策经用户 2026-10-24 明确裁决，**反转 2026-09-19 的零配置决策**（当时 gateway 二进制空 env 可用 standalone SQLite + memory 驱动零配置启动）；退役理由与执行契约见 [PLAN-20261024T051925945Z：SQLite 与内存模式彻底退役](../plans/PLAN-20261024T051925945Z-SQLite与内存模式彻底退役.md)。
- 退役范围包括：运行时模式开关（`JUHE_AI_RUNTIME_MODE` / `JUHE_AI_DATABASE_DRIVER` / `JUHE_AI_CACHE_DRIVER` / `JUHE_AI_RUNTIME_STATE_DRIVER`）、组合根 sqlite/memory 装配臂、共享层方言双实现、maintenance `--driver sqlite --paths` 形态与 `--apply-j3b-model-check-sqlite` 等一次性命令、sqlite 分片（usage shards / codex-context state shards）与全部 `*_DATABASE_PATH` 路径族 env。上述 env 与旗标代码不再读取，残留值一律无效；不提供任何兼容层、数据迁移或读取转换。
- 原《SQLite 存储说明》已整篇退役删除；其中仍然成立的现行事实（存储域表边界、数据保留与统一清理、审计 / 操作日志存储边界、敏感字段口径、统计缓存离线重建）已迁入本文对应章节。
- 启动必填项（缺失即 fail-fast）：`JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL`。

## 背景

PostgreSQL + Redis 形态最初为高用户量、高请求量部署引入，用于突破单机 SQLite 文件级写锁、进程内 LRU 缓存不可共享、队列写入必须让出文件锁等系统性限制。2026-10-24/25 收口后，该形态成为唯一形态：维护双方言双实现（每次存储迁移双方言双工、组合根双臂、golden 双份、跨进程契约双载体）的成本远超其带来的本地轻量部署收益，项目裁决彻底物理删除本地模式，统一收敛到数据库事务、连接池和缓存 / state 隔离管理的并发模型。

因此当前系统只有一种存储装配：

| 职责 | 承载 |
| --- | --- |
| 全部事实数据（业务、数据集、使用记录、统计、日志索引、运行可恢复数据） | PostgreSQL |
| 跨进程短 TTL 缓存 | Redis cache（可丢弃） |
| 跨进程短 TTL 运行态、硬并发槽、限流计数 | Redis state（`noeviction` 语义，必须有 TTL） |
| J3b 电路运行态 | 独立 Redis 键空间（不得与 state 同键空间） |

## 目标

- PostgreSQL 承接所有事实数据、预聚合统计、日志索引和运行可恢复数据。
- Redis 承接跨进程短 TTL 缓存、调度运行态、限流、验证码 / 登录失败窗口、缓存版本和必要的原子计数。
- 数据访问方只依赖统一的 store / cache / runtime state 接口边界，不在业务代码里散落存储形态判断。
- 不在代码库里做旧结构迁移、双读、双写、自动迁移或旧结构兼容；历史数据处理由上线窗口在代码库外单独完成，应用只面向当前 schema。

## 不做什么

- 不引入 Kafka、RabbitMQ、BullMQ、ClickHouse 或其他外部队列 / OLAP。
- 不实现多节点自动故障转移、跨地域复制或自动用户迁移；多节点路由仍按 [分布式部署与用户分片设计](分布式部署与用户分片设计.md) 单独推进。
- 不把 Redis 作为账务、权限、授权、账号健康、审计或使用记录的持久事实库；跨进程短 TTL 缓存、运行态和原子计数的当前事实源必须是 Redis，不能回退到进程内 memory。持久事实统一落 PostgreSQL，不引入外部队列。
- 不为运行路径保留旧 schema 兼容分支；历史数据处理只允许离线脚本或重建流程。
- 不恢复 SQLite / memory 本地模式，也不为旧 env 值、旧命令行形态、sqlite 数据文件提供任何读取或转换能力。

## 配置模型

- 启动必填（缺失启动即失败，fail-fast 保留）：`JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL`。
- 已退役 env：`JUHE_AI_RUNTIME_MODE`、`JUHE_AI_DATABASE_DRIVER`、`JUHE_AI_CACHE_DRIVER`、`JUHE_AI_RUNTIME_STATE_DRIVER` 与全部 sqlite 路径 / 分片族（`*_DATABASE_PATH`、`JUHE_AI_USAGE_SHARD_ROOT` / `_COUNT`、`JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` / `_COUNT`）及 store 选择族（`JUHE_AI_AUDIT_LOG_STORE` / `JUHE_AI_OPERATION_LOG_STORE` / `JUHE_AI_RUNTIME_LOG_STORE` / `JUHE_AI_J3B_STORE` / `JUHE_AI_ACCOUNT_HEALTH_STORE` / `JUHE_AI_ACCOUNT_BALANCE_STORE`）——代码不再读取，部署面残留值无效，应从 `.env` 删除（清理清单见 `docker/single-server/README.md`）。
- 普通文件目录派生（PG 模式仍消费）：`JUHE_AI_DATA_DIR` 缺省 `./data`（相对进程工作目录），下派生 `usage-record-spool`（gateway↔jobs 用量交接，可用 `JUHE_AI_USAGE_SPOOL_DIRECTORY` 显式覆盖）、`codex-context` segments 根（可用 `JUHE_AI_CODEX_CONTEXT_ROOT` 显式覆盖）、`logs`、`chat-assets`、`audit-blob`。gateway 与 jobs 双进程消费同一目录时必须解析到同一宿主机路径。
- `JUHE_AI_CACHE_DRIVER` 类语义不复存在：Redis cache 只表示可丢弃缓存；需要硬并发槽、限流计数或短 TTL 调度运行态时必须走 Redis state（`JUHE_AI_REDIS_STATE_URL`）。
- `JUHE_AI_REDIS_NAMESPACE` 是 Redis key 的部署隔离前缀，必填非空（现行生产配 `prod`）；压测、回归和多套环境共用 Redis 实例时必须使用不同 namespace。
- 运行态与 J3b 电路运行态必须分离键空间：`JUHE_AI_J3B_CIRCUIT_REDIS_URL` 不得与 `JUHE_AI_REDIS_STATE_URL` 相同（gateway 启动强校验），现行生产以同实例不同 DB 承载。
- Go 不读取 `JUHE_AI_QUEUE_DRIVER` / `JUHE_AI_REDIS_QUEUE_URL`（Node 时代死配置）；Go 三项目没有 Redis 任务队列，无需第三个队列 Redis 实例。
- `JUHE_AI_DB_POOL_MAX`、`JUHE_AI_DB_WRITE_MAX_CONCURRENCY`、`JUHE_AI_POSTGRES_*_TIMEOUT_MS`、`JUHE_AI_REDIS_STREAM_*`、`JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT` 等 Node 时代 env 在 Go 中不存在；现行必需 env 契约以 `docker/single-server/README.md` 的".env 契约"节为准。
- 生产环境不使用 `latest` 镜像；PostgreSQL 和 Redis 镜像必须固定 major / patch 或 digest。
- 审计保全不因部署形态放宽：成功正文默认全量保留（采样率默认 `1`，可调），最近 `1` 小时热保留窗口默认开启；失败、异常、中断和重试后成功链路仍全量进入审计。程序不因容量压力自动降采样，容量和吞吐问题通过 PG 小批次写入、热窗口清理、payload 摘要 / 压缩 / 去重治理；口径详见 [审计日志保全策略设计](审计日志保全策略设计.md)。

## 部署边界

现行唯一生产形态是国内单机 Docker（`docker/single-server/`，go-only）：Caddy 入口 + gateway / jobs 常驻容器 + maintenance 一次性容器 + postgres / redis 两个中间件容器；部署、初始化与 env 契约以 `docker/single-server/README.md` 和 `docs/deploy/部署指南.md` 为准。

| 服务 | 职责 | 说明 |
| --- | --- | --- |
| `postgres` | 主事实库 | `postgres:18-alpine`，单库 `juhe_ai` 多 schema，应用直连、无 PgBouncer；compose 预置 `shared_buffers=2GB`、`effective_cache_size=4GB`。 |
| `redis` | 缓存 + 运行态 + J3b 电路 | 单 `redis:7.4-alpine` 容器，`appendonly yes`、`maxmemory-policy=noeviction`；cache / state / J3b circuit 以 DB 编号分离，state 与 J3b 电路键空间强制分离（见配置模型）。 |
| `gateway` | 管理面、公开面与 `/v1` 网关链 | 进程内装配 F3 审计、F4 usage spool 等 owner 包。 |
| `jobs` | 后台任务族 | jobregistry 注册的统计聚合、保留清理、探针、OAuth 刷新等任务族。 |
| `maintenance` | schema / seed 一次性 CLI | `juhe-ai-maintenance --ensure-schema / --seed`（`--dsn` 单形态）幂等执行。 |
| `caddy` | HTTPS 入口 | ACME 自动续期，反代管理 SPA 与 `/v1`。 |

版本参考：PostgreSQL 18 是当前正式版本线，PostgreSQL 19 仍处 beta 阶段，不作为生产默认；Redis 现行镜像为 `redis:7.4-alpine`，后续升级大版本前需单独确认授权接受度与兼容性，不在本设计里静默切换。

## PostgreSQL 存储形态

全部事实域映射为单库 `juhe_ai` 内的 schema；表名保留业务语义，schema 归属由各进程 store 包固化，不把 schema 名散落进业务逻辑：

| PostgreSQL schema | 数据域 | 主要表 |
| --- | --- | --- |
| `juhe_business` | 业务库 | 系统账户、会话、供应商、协议与协议档案、账号、分组、路由策略、调度偏好、API Key、授权、代理、设置、公告、系统团队、外部来源系统、账户测试任务 / 会话、自定义模型、媒体任务 |
| `juhe_dataset` | 数据集域 | 审计事实（`audit_logs` / `audit_log_attempts` / `audit_payload_refs` / `audit_payload_blobs` / `audit_error_groups`）、操作日志四表、`public_api_logs`、运行日志索引（`runtime_logs` / `runtime_log_file_cursors`，由 jobs 的 F1 索引器唯一写入）、模型检测三表、`api_key_record_cleanup_targets` |
| `juhe_usage` | 使用记录域 | `usage_records`（日分区）与列表索引、账号 / API Key scope catalog |
| `juhe_stats` | 统计结果域 | 用量桶、额度窗口、范围窗口、排行、账号质量、`stats_job_state` 等预聚合结果（逐表键与口径见 [统计指标与分层聚合设计](统计指标与分层聚合设计.md)） |
| `juhe_codex_context` | Responses 桥接状态索引 | Responses session / response / compact 索引 |
| `juhe_chat` | AI 问答正文 | 会话、消息（日分区）、幂等键、用户存储窗口（见文末补充） |
| `juhe_jobs` / `juhe_j3b` | jobs 事实域 / J3b 模型检测 | 余额租约四表等 jobs 事实、`model_check_*` 专库表（需手工预置 schema 后由 maintenance `--apply-*` 建表） |

schema 权威是 maintenance 的 Go DDL（`backend-go/projects/maintenance/internal/schema/pg_schema*.go`），由 `juhe-ai-maintenance --ensure-schema` 幂等执行（Node 时代的 `postgres-schema.ts` / `postgres:init-schema` 已随迁移删除）。生产单库除上表 schema 外的预置序列（手工 `CREATE SCHEMA` 与 `--apply-*` 命令）以 `docker/single-server/README.md` 的拓扑节为准。

表 / 数据库容量观测由 postgres_exporter 直接抓取生产 PostgreSQL 承担（原"按 5 个 schema 采样 relation size 写入 `juhe_stats.database_storage_snapshots` / `juhe_stats.table_storage_snapshots`"的应用内表监控已随 2026-10-14 监控改造删除，两张快照表退役），见 [Prometheus 指标暴露设计](Prometheus指标暴露设计.md)。

### 存储域边界（承自 SQLite 存储说明 2026-10-25 退役）

- **业务库（`juhe_business`）是恢复的硬边界**，保存可恢复核心业务数据：`system_accounts` / `system_sessions`、`global_settings` / `system_settings`、`providers` / `protocols` / `protocol_endpoint_families` / `provider_protocol_profiles` / `provider_protocol_profile_families` / `proxy_profiles`、`accounts` 及其支持模型 / 模型映射 / 标签 / 测试任务 / 测试会话、`custom_provider_models`、`groups` / `group_authorization_settings` / `group_accounts` / `group_account_stats_dirty`、`route_strategies` / `route_strategy_groups`、`scheduling_preferences`、`api_keys`、`system_teams` / `system_team_members`、`resource_authorization_*` 三表、`external_integration_sources` / `external_integration_source_tokens`、`announcements` / `announcement_reads`。供应商 / 协议档案的当前清单与约束以 [供应商与上游协议面重构设计](供应商与上游协议面重构设计.md) 与 [核心功能设计](核心功能设计.md) 为准。
- **数据集域与统计结果域是可丢弃、可重建数据**（含使用记录分区、审计 payload blob、日志索引、统计缓存），只有离线取证时才额外停机备份；默认备份只覆盖业务库与部署配置。
- 运行时代码不做跨 schema 的隐式耦合查询；业务写入归 gateway 唯一 owner，统计结果域写入归 jobs 串行提交，审计 / 操作日志 / 运行日志索引 / 模型检测归各自 owner（见"队列与消费并发"）。
- 使用记录列表由 repository 内部有界读取并稳定合并，不做全量精确 `COUNT(*)`；账户名筛选先在业务库解析账户 ID 集合，再以 `account_id IN (...)` 查明细（授权场景解析范围包含授权实例名称、来源账户名称与分组授权来源分组内账户名称，保证被授权用户按来源账户名可命中且不越权）。

### usage_records 当前形态

`juhe_usage.usage_records` 按 `created_at` 建日分区父表，主键为 `(created_at, id)`。写入前会按记录时间确保对应日分区存在，列表、详情、统计游标和清理都必须带上时间窗口或由 usage id 推导分区窗口，不能对分区父表做无界扫描。

- 分区键：`created_at` 日 range partition；过期在线数据在统计安全游标追平后按分区事务内 `DETACH / DROP`，不保留同库冷归档副本或归档 manifest。
- 热查询索引白名单以用户维度开头：`system_account_id + created_at + id`、`system_account_id + api_key_id + created_at + id`、`system_account_id + account_id + created_at + id`、`system_account_id + trace_id COLLATE "C" + created_at + id`。
- 管理员使用记录页未限定用户时只能读取配置的 `usageStatsTimezone`（显式 IANA）当天、`created_at DESC, id DESC` 的全用户列表，由当天分区和父表复合主键 `(created_at, id)` 承接；账户名称、结果、状态码、IP、分组、模型、trace ID、请求来源、手工日期或非默认排序必须先选择系统账户。其他管理员大表页面仍按各自功能边界先限定用户和日期窗口；独立 `trace_id`、独立 `request_id`、全局 `model/path/status/client_ip` 不能作为大表默认索引。
- PostgreSQL 前缀筛选必须显式使用稳定 collation。`trace_id`、`client_ip`、API Key 名称和 AI 性能账号选项名称等文本前缀查询使用 `COLLATE "C"`、二进制上界和对应 C collation 表达式索引；不能依赖 `prefix + '\uffff'`，也不能假设数据库默认 collation 的排序行为。
- 清理策略：常规过期清理按统计安全游标追平后处理整日分区；已删除账号 / API Key 的关联明细小批次清理必须使用 `(created_at, id)` 命中分区，不能只按裸 `id` 删除。
- 统计游标：`stats.stats_job_state` 继续记录按分区 / 窗口推进的游标，统计写入和游标推进在同一 PostgreSQL 事务提交。

索引白名单与 collation 细节以 `backend-go/projects/maintenance/internal/schema/pg_schema_usage.go` 当前 DDL 为准。

### JSON 与时间

- 需要按字段筛选、局部更新或索引的配置字段使用 `jsonb`；只作整体存取的 JSON 使用 `text`。
- 真正表示 PostgreSQL instant 的查询和持久化列优先使用 `timestamptz`；经明确证明需要版本、CAS 或共享 schema 兼容的列，可以保存 canonical UTC RFC3339 text，但写入必须严格校验并拒绝无 offset。内部传输、比较和持久化继续使用 RFC3339 绝对时间，管理 API 输出 UTC `Z` 或明确数字 offset；Vue / 浏览器按用户本地时区展示。date-only 与业务日历字段另行明确，并使用显式 IANA timezone；不得把这些例外机械扩展为 schema-wide 数据库迁移。
- 金额和成本如果需要精确累加，优先使用 `numeric`；只作为展示缓存且已有浮点口径的字段可以保持 `double precision`，但统计总量字段必须固定类型并写清楚。
- `juhe_business.accounts` 使用 `health_check_model` 保存账户必填检查模型，不保留 `default_test_model` 或 `health_check_enabled`。供应商级默认检查模型表已随《供应商与上游协议面重构设计》§9.11 删除；新建账户检查模型由创建事务按最终支持模型集解析，供应商列表展示值由 `models` 单表按最新发布时间且可文本生成的模型只读派生。
- `juhe_business.provider_model_catalog` 和 `juhe_business.custom_provider_models` 复用 `supported_service_tiers_json`、`supported_reasoning_efforts_json` 和 `default_reasoning_effort`，并各用一个 `service_tier_prices_json` 保存非标准实际档位价格；标准价继续使用现有扁平字段，不新增价格表。支持请求覆盖的供应商账户凭据 JSON 可选保存现有 `service_tier_override`、`reasoning_effort_override`。字段值由所属供应商模型共同能力和目标 driver 校验，`ultra` 与 Responses Multi-agent Beta 不属于这两个账户覆盖字段。

### 约束与并发

- 业务唯一约束继续由数据库兜底，例如账号名称、API Key token hash、分组绑定、授权来源等。
- 管理端幂等仍保留 Redis 防重复提交 + 数据库唯一约束双层保护。
- PostgreSQL 不存在文件级全局写锁，但仍存在行锁、索引页竞争、连接池耗尽、长事务和 autovacuum 压力；不能把所有队列无限并发。
- System API 管理端 DB 在途请求默认保留保护阈值：standalone 默认 `64`，performance 默认 `256`，可按高性能部署压测继续调整。（Node 时代截面，现行以 Go gateway 管理面实现为准。）
- 同一账号等热点资源的高频并发写入仍会形成行锁排队；需要按资源维度串行、合并写入或把短 TTL 运行态拆到 Redis，不能把同一行无限并发写。
- AI 账户批量编辑必须在单个 PostgreSQL 事务内校验全部自有物理账户、字段白名单、同构模型条件和 `expectedVersions`，任一版本冲突或配置非法时整批回滚。事务提交后再失效缓存和投递必要的后台系统检查。

## SQL Dialect 与 Repository

- SQL 方言以 PostgreSQL 为唯一方言；`sqldialect` 保留 `?`→`$n` 占位符重写与 advisorylock（PG-only advisory lock）等 PG 路径基础设施，不存在第二方言实现。
- repository 层按 schema 包（`backend-go/projects/maintenance/internal/schema`）与各进程 store 包组织：gateway / jobs 各自的 `internal/...` store、repo 包持有各自事实域的 SQL。
- 细节以 `backend-go` 各 internal 包为准，本文不再逐路径登记。

## Redis 缓存与运行态

缓存层拆成三类（以各进程 internal 包的 cache / runtime state 组件承载）：

- 进程本地缓存：进程内同步缓存，可存放不可序列化对象，例如 HTTP agent、临时近端只读结果和进程内 memoization；仍允许使用，但不能承载跨进程事实源。需要跨进程一致的缓存必须接入 Redis，进程本地缓存只能作为可丢弃本地 L1 或明确的进程内易失优化。
- 跨进程可丢弃缓存：Redis cache（`JUHE_AI_REDIS_CACHE_URL`）。
- 跨进程短 TTL 运行态：Redis state（`JUHE_AI_REDIS_STATE_URL`）；硬并发槽、限流计数这类不能超卖的状态必须通过该层工具或封装后的专用原子操作访问。分布式锁只保留给明确需要单资源串行化且不可接受重复执行的基础设施保护，例如 OAuth access token 单账号刷新；网关调度、账号运行态探针、上游桶避让、IP 级账号回避、IP 错误熔断和 Codex turn retry 不允许在请求路径使用 Redis 分布式锁。

Redis key 统一命名：

```text
juhe-ai:{namespace}:{driver}:{cache-name}:v{version}:{scope}:{key}
```

规则：

- `{namespace}` 由 `JUHE_AI_REDIS_NAMESPACE` 决定；必填非空，生产建议显式配置并保持稳定（键形状以 Go cache / runtime state 各实现为准）。
- 缓存失效优先递增 domain version，实现常量成本失效，不做全库 `SCAN + DEL`。
- 所有 Redis cache / state key 必须有 TTL；确需长期保留的运行态要先证明不是持久业务事实。
- Redis payload 使用 JSON，并带 `schemaVersion`；结构变化时递增 cache domain version。
- API Key 明文、OAuth token、代理密码、完整请求 / 响应 payload、审计正文和可能造成越权的权限中间结果不得进入通用 Redis cache。
- 调度运行态、并发占用、IP 级错误熔断、登录失败窗口、验证码挑战、会话亲和和 cache invalidation index 进入 Redis state。
- 账户页展示的当前并发是列表加载时的瞬时 in-flight 值，不是累计请求数。管理端和用户侧账户列表必须在列表响应中批量读取当前可见账户在 Redis state 中的并发槽；授权实例必须按来源账号 ID 读取同一个硬并发槽，不能按授权实例 ID 另算一份并发。没有占用或 Redis state 读取失败时当前并发返回 `0`；前端不得为账户当前并发额外请求快照或开启定时轮询。

### Redis 运行态一致性分级

Redis runtime state 按一致性要求分三类处理：

- 硬约束状态：账号并发槽、系统 API 限流、验证码发放限频等不能突破上限的状态，必须使用 Lua / Redis 原生命令 / 封装后的原子操作，失败时按保护性拒绝或快速失败处理。
- 短 TTL 调度状态：上游桶避让、IP 级账号回避、IP 错误熔断、Codex turn retry、AI 账户运行态探针状态。这类状态允许秒级短暂不一致和重复写入，使用 TTL、时间窗口、generation、成功信号清理和后台探针收敛，不在用户请求路径等待分布式锁。
- 可丢弃缓存：列表快照、只读 options、runtime cache version 等可重建数据，使用 `SharedJsonCache` 或本机 L1，失效慢一点只影响短期展示或调度偏好，不能承载账务、授权、使用记录或审计事实。

用户业务请求次数限制是"速度优先的软协调配额"，不归入上面的硬约束状态；现行机制、参数与降级行为以 [用户请求限制设计](用户请求限制设计.md) 为准。

短 TTL 调度状态禁止把"避免丢一个计数"作为加分布式锁的理由。高并发下丢失少量样本的代价低于请求路径等待锁、锁残留、跨节点排队和恢复探针被误限制的代价；状态升级必须由时间窗口、最小观察期、探针结果和成功信号共同决定。

### AI 账户运行态探针

AI 账户运行态探针的运行态必须跨进程可见（权威口径见 [AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)）：

- `failure_observed`、`local_suppressed`、`runtime_degraded`、`precheck_pending` 这类短 TTL 调度态可以存放在 Redis runtime state，但不能作为账务、授权、审计或账号健康持久事实。
- 状态事件只提交 probe intent；探针调度器负责去重、本机预算、jitter、generation 和 due 索引。
- 请求调度链路允许短暂不一致，优先使用 server 进程内短 TTL 近端缓存读取 Redis 探针状态，避免高并发下每次请求按候选账号数量访问 Redis。
- due 索引必须跨节点可见，使用 Redis sorted set 或等价 Redis runtime state 结构保存 `runtimeKey -> dueAt`。任意 server 节点都可以 sweep due 任务；重复执行由 generation 条件写入和条件删除收敛。
- 不使用 Redis 分布式锁、分布式全局预算锁、provider 锁、proxy 锁或 baseUrl 锁限制恢复探针预算；预算只做本机保护和 jitter，避免 Redis 锁残留导致恢复并发被误限制。
- 探针结果回写和探针成功清理前必须校验 generation，避免旧探针覆盖或误删真实成功、手动恢复或后续状态转换产生的新状态。
- Redis 探针状态只保存非敏感运行态元数据和 due 信息，不保存账号凭据、API Key、OAuth token、代理密码、失败请求 payload、失败请求 model 或 endpoint；执行探针前从业务库重载账号凭据。
- 运行态恢复探针和持久健康 / 冷却探针严格使用重载账户的 `health_check_model`；缺失或非法时记录配置异常，不从个人默认、系统默认或支持模型首项兜底。
- 短 TTL 运行态探针的 due sweep 与执行、持久冷却复测由 gateway 运行态协调与 jobs 探针任务族按任务登记分工承接（任务条目以 `backend-go/projects/jobs/internal/jobregistry/registry.go` 为准）。
- `runtime_recovery_probe` 只表示本地 / Redis 运行态恢复探针；持久 `temporary_unavailable / rate_limited` 冷却复测继续使用 `cooldown_retest`。两类探针都不写账号质量分钟样本，不保存完整请求 / 响应正文。
- Redis state 不可用时不能静默退回进程内 memory；应记录基础设施错误并保守跳过探针或快速失败。

## 队列与消费并发

Go 三项目没有 Redis 任务队列（Node 时代的 Redis Streams consumer group 队列表已随迁移退役，见 git 历史）。异步链路按数据域收敛为进程内组件与文件交接：

- usage 链路：gateway 在 `/v1` 链内把用量记录原子写入文件 spool（`JUHE_AI_USAGE_SPOOL_DIRECTORY`，未配置时按 `JUHE_AI_DATA_DIR` 派生 `usage-record-spool`），jobs 的 `usagespooldrain` 扫描 spool 文件、经归一化校验后交接给 `usagewriter` 直接异步写分区（`backend-go/projects/jobs/internal/usagespooldrain/drain.go`）。gateway 与 jobs 两侧 spool 目录必须解析到同一宿主机路径，否则用量记录永不到库且无报错（`docker/single-server/README.md`"运行时数据目录契约"）。
- 审计与操作日志：gateway 进程内 F3 / F4 owner 包直接写入，不经队列。
- 公开接口日志由 gateway 进程内组件写入；运行日志索引由 jobs 的 F1 索引器按文件 cursor 消费。
- 后台任务族由 `jobregistry` 统一注册，lease 围栏、单持有者、固定退避语义见 `backend-go/projects/jobs/internal/jobregistry/registry.go`；低优先级清理不得阻塞关键用量与审计链路。

统计聚合仍按作用域 / 分区并行读取、窗口事务写入控制，避免同一 summary key 并发 upsert 放大冲突；事实明细 append-only，不做 last-write-wins；失败重试必须指数退避并有上限，不能无限占用执行预算。

## Go 三进程分工

- `gateway`（`backend-go/projects/gateway`）：管理面、公开面与 `/v1` 网关链主入口；进程内装配 F3 原始审计、F4 操作日志 owner 包和 usage 文件 spool 写入，owner lease 围栏下的审计 / 操作日志保留清理也在本进程执行。
- `jobs`（`backend-go/projects/jobs`）：后台任务族执行进程，任务条目以 `internal/jobregistry/registry.go` 注册表为准（统计聚合、数据保留清理、探针任务族、OpenAI OAuth token 刷新等），每个任务显式登记 Go 绑定状态、不允许静默跳过。J3a 已由 Go `juhe-ai-jobs` 独占执行、结果投影和 `proxy_profiles` 写回。
- `maintenance`（`backend-go/projects/maintenance`）：schema / seed 一次性 CLI，`--ensure-schema / --seed`（`--dsn` 单形态）幂等执行。

人工账户测试是 gateway 进程内单持有者队列（共享 `backend-go-platform/accounttest`），仍保持独立单账户诊断语义：每次使用独立测试会话，A/B 互不阻塞，不建立用户级全局锁，也不提供多账户批量测试；测试结果只写任务、使用记录和审计，不修改账户配置或调度运行态。

## 事务与一致性

- 单个业务写操作必须在一个 PostgreSQL 事务内完成。
- 跨事实域强一致需求应优先收敛到同一个 PostgreSQL 事务。
- 对 usage 明细、审计、日志、统计缓存这类异步事实链路，仍保持"事实先落库，统计后聚合"的最终一致模型。
- 统计结果和统计游标必须同事务提交。
- Redis cache / Redis state 只承接可重建缓存和短 TTL 运行态，不承载未落库消息（Go 无 Redis 任务队列）。原始审计和操作日志由 gateway 进程内 F3 / F4 owner 包直接写入；usage 明细经文件 spool 由 jobs 的 `usagewriter` 落库；普通运行日志只追加到 JSONL 文件，由 jobs 的 F1 索引器按持久 cursor 批量索引。事实最终以 PostgreSQL 落库为准。

### 使用记录批量落库锁顺序

jobs 的 `usagewriter` 以批量事务把用量记录写入 PostgreSQL。`usage_records` 的唯一键保证重复投递幂等，但不能替代锁顺序：若事务先写 usage 分区的唯一索引、再按输入顺序更新多个 `juhe_business.accounts` 行，两个批次就可能分别持有 usage 索引和账户行锁，形成 `40P01` 循环等待。

PostgreSQL 使用记录批事务必须遵守以下边界：

- 在事务外完成批次归属、价格和账户副作用的汇总，不持有数据库锁。
- 事务开始后，先对所有将写入副作用的未删除账户执行 `ORDER BY id FOR NO KEY UPDATE`，由数据库按唯一稳定顺序获取行锁。
- 账户行锁持有后，才写入 `juhe_usage.usage_records` 及其分区元数据，最后执行 `last_used_at` 和健康成功信号的条件更新。
- 事务失败时整体回滚；spool 文件按 at-least-once 语义保留重试。不得以降低批量规模、关闭幂等写入或删除历史记录规避竞争。

该顺序仅约束使用记录批写事务，不要求把所有账户写入路径串行化；其他账户变更仍须避免在同一事务中先持有其他互斥资源、再反向等待 usage 写入。该锁顺序在 Go `usagewriter` 的 PG 写路径中原样保留（`backend-go/projects/jobs/internal/usagewriter/store.go` 的 `ORDER BY id FOR NO KEY UPDATE` 先锁账户行、再写 usage 分区）。

## PostgreSQL 调优基线

具体数值按目标机器 CPU / 内存 / 磁盘压测后调整；现行生产基线已固化进 `docker/single-server/compose.yml`。默认基线：

- 现行生产为单机容器直连 PostgreSQL（无 PgBouncer）；compose 预置 `shared_buffers=2GB`、`effective_cache_size=4GB`（`docs/deploy/部署指南.md`），新建 / 更新实例不得回退到镜像默认 `shared_buffers=128MB`。
- `shared_buffers` 按机器内存约 25% 估算，`effective_cache_size` 按 50% 到 75% 估算（为优化器估值，不占内存）。
- `work_mem` 保守设置，避免高并发下排序 / hash 聚合放大内存。
- 打开 `pg_stat_statements`，记录慢 SQL、平均耗时、调用次数和 rows。
- usage 热表分区开启 aggressive autovacuum，过期数据优先 drop partition。
- 设置 `log_min_duration_statement`，生产初期建议 500ms 到 1000ms。
- 定期执行 `ANALYZE`，大批导入或离线迁移后必须刷新统计信息。

## Redis 调优基线

- 现行生产为单 `redis:7.4-alpine` 容器，cache / state / J3b circuit 以 DB 编号分离；可淘汰缓存与不可淘汰运行态混用同一实例时必须守住 `noeviction` 边界，运行态与 J3b 电路运行态不得混用同一键空间（`JUHE_AI_J3B_CIRCUIT_REDIS_URL` 强制分离）。
- 可丢弃缓存设置 `maxmemory` 和淘汰策略，命中率低于阈值时优先检查 key 设计，不直接加内存。
- 运行态使用 `noeviction`，所有写入必须带 TTL；写失败时调用方按降级策略处理。
- 硬约束运行态使用 Lua 或 Redis 原生命令收口，不把 `GET -> 本地判断 -> SET` 暴露给并发调用方；短 TTL 调度状态可以直接读写并接受短暂覆盖，但必须有 TTL、成功清理、后台恢复或 generation 收敛机制。
- 监控 `used_memory`、`evicted_keys`、`expired_keys`、`blocked_clients`、`instantaneous_ops_per_sec`、命中率和慢命令。

## 数据保留与统一清理（承自 SQLite 存储说明 2026-10-25 退役）

逐表保留策略：

| 数据 | 保留策略 | 统一定时清理 | 注意事项 |
| --- | --- | --- | --- |
| `runtime_logs` / `runtime_log_file_cursors` | 索引行默认 14 天（`runtimeLogIndexRetentionDays`，1..90），cursor 清理按最近 3 天未更新 | 是，Go F1 indexer 按各自 interval 清理 | 只删除 PG 搜索索引行，不删除后端 `.log` 文件；文件日志按 30 天 / 500 个轮转文件保留 |
| `model_check_runs` / `model_check_items` / `model_check_observations` | 默认 30 天，最多 365 天 | 是，`data-retention-cleanup` | observation 通过 run 外键级联删除，只保留 HMAC 桶、Token 数值和固定 8 维身份特征，不保存题面、回答正文或普通流量正文 |
| 操作日志四表（`operation_logs` / `operation_log_targets` / `operation_log_viewers` / `operation_log_summary_search_terms`） | 默认 365 天，业务设置允许 1..3650 天 | 是，Go F4 owner 按独立 interval 清理 | 只按时间清理，不因资源删除级联删除历史日志；摘要词项随操作日志级联删除；设置不可读或非法时跳过该轮并记录错误 |
| `audit_logs` / `audit_log_attempts` | 成功 payload 热窗口默认 1 小时，成功长期 payload 样本默认 3 天，问题事件默认 7 天 | 是，`audit-hot-retention-cleanup` 每分钟裁剪热窗口；`data-retention-cleanup` 清理长期过期数据 | 完全成功请求先全量热保留；超过热窗口后，未命中 10% 长期采样的记录删除 attempts 和 payload 引用、主记录降为 `metadata_only`；成功三项环境变量全为 0 时不采集成功审计 |
| `audit_payload_refs` / `audit_payload_blobs` | 成功样本正文默认 3 天，问题正文默认 7 天 | 是，`data-retention-cleanup` | 先删除过期引用，再删除无引用 blob 和本地 blob 文件（`JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY`） |
| `audit_error_groups` | 默认 7 天 | 是，`data-retention-cleanup` | 只做展示和排障聚合，不替代事件记录 |
| `juhe_codex_context` 关系索引 + segments 文件 | 固定 7 天未使用即清理 | 是，jobs `data-retention-cleanup` | 过期索引删除与 storage key 入队在同一事务内；只删除没有任何剩余引用的 segment 文件（`JUHE_AI_CODEX_CONTEXT_ROOT`），失败项指数退避，成功或文件已不存在才确认出队 |
| `juhe_usage` 明细与 scope catalog | 默认 7 天在线保留（`usageRecordOnlineRetentionDays`），按统计安全游标追平后整日分区 `DETACH / DROP` | 是，`data-retention-cleanup` | 自动保留任务必须按统计聚合 / 回填游标保护，只删除已确认进入缓存的旧明细 |
| 账号质量分钟桶 / 统计缓存各桶 | 分钟桶默认 48 小时；小时桶默认 60 天；日桶默认 400 天；周桶默认 104 周；月桶默认 24 个月 | 是，`data-retention-cleanup` | 快照刷新水位只读 `MAX(updated_at)`，不以全表 `COUNT(*)` 感知删除 |
| 授权日摘要 / 热范围窗口 | 日摘要跟随日级统计保留；热窗口默认今天 / 昨天 / 最近 7 天 / 最近 31 天 / 当前月，按需窗口按 TTL 清理 | 是，`data-retention-cleanup`；刷新任务会覆盖当前窗口 | 自定义范围先登记请求再后台生成，过期窗口可重建或丢弃 |
| 排行 / 概览 / AI 性能 / 额度窗口缓存 | 默认最近 31 天窗口或最近 30 天刷新结果；排行快照默认 30 天 | 是，`data-retention-cleanup` | API 只直读预聚合结果，不在请求时回扫明细；额度小时窗口只在用量聚合实际推进后刷新，空轮不重建 |
| `account_usage_snapshots` | 默认 30 天未更新即清理 | 是，`data-retention-cleanup` | 正常按账号主键 upsert，清理的是长期未更新的过期快照 |
| `system_sessions` | 到期即清理 | 是，后台数据保留任务 | 查询时也会校验过期时间；`last_seen_at` 只允许按短间隔节流刷新，不应在每个鉴权请求中无条件写入 |

不按保留期物理清理：

- `usage_stats_totals` 长期保留，作为账户、分组、授权和全局总量缓存。
- `stats_job_state` 长期保留，作为统计游标和任务状态；它是自动保留任务和管理员手动清理删除 `usage_records` 的安全边界。
- `group_account_stats` 是当前分组账户状态缓存，由刷新任务按脏分组刷新；业务库 `group_account_stats_dirty` 是刷新队列，不属于历史日志，处理完成即可删除。
- 普通日志文件由文件日志滚动配置清理，不属于 PG 表清理；当前默认保留 30 天，并受最多 500 个轮转文件和单文件大小限制；`grep 模式` 扫描当前保留的 `.log` 文件，但单次文件时间范围最多 7 天。
- 数据集域、使用记录域和统计结果域新增表都应是普通表或普通索引，必须接入统一保留期或明确说明长期保留理由。

统一清理规则：

- 除 F1 / F4 专属事实外，表数据长期保留期统一由 `data-retention-cleanup` 默认每 10 分钟执行；原始审计普通成功请求的 1 小时热窗口后置采样裁剪由 `audit-hot-retention-cleanup` 每分钟分批执行（单轮固定 5 秒预算，预算耗尽后停止本轮留给下一轮）。F1 运行日志和 F4 操作日志分别由对应 Go owner 保留清理，其他清理器不得删除它们的专用事实。
- 清理任务按内部常量小批次删除，固定每类表每轮最多处理 1000 条、最多 20 批，即每类数据每轮最多 2 万行；这些吞吐参数不属于系统设置，不允许用户在线调整。PostgreSQL 按 ctid / 主键窗口分批删除，避免单事务长时间持锁。保留清理按表独立推进，前序表已经清完后，如果后续表失败，下一轮从失败表继续。
- 手动清理 `usage_records` 按批次执行，截止时间不能晚于当前时间 24 小时前，并且必须受统计聚合游标和必要回填游标保护；没有可安全清理记录或统计游标尚未建立时拒绝并说明原因。
- 统计聚合和审计日志落库只负责写入或聚合，不在各自流程里顺手删除历史表数据。

### 统计缓存离线重建（rebuild-usage-stats）

统计缓存损坏或统计口径升级时，使用 `backend-go/projects/maintenance` 的一次性命令 `juhe-ai-maintenance rebuild-usage-stats` 停服离线重建（BUG-0182）：

- 离线门禁：必须显式传 `--confirm-offline` 或设置 `JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1`，否则拒绝执行且零副作用；前置条件是 gateway 与 jobs 已停止。除显式确认外，重建入口还会自动做离线校验：`background_job_leases` 存在未过期租约，或 `background_task_runs` 在活动回看窗口（5 分钟）内有心跳痕迹时拒绝（退出码 4，零副作用）——jobs 是统计结果表唯一在线写入方；停止 jobs 后如被拒绝，等待活动滑出回看窗口（至多约 5 分钟）再重试。
- 存储形态：`--dsn <PostgreSQL URL>` 单形态（事实源 `juhe_usage.usage_records`，重建目标 `juhe_stats`，业务 schema `juhe_business` 只读）。
- 重建语义：单事务清空 statsagg 实际写入的统计结果面（`usage_stats_*` 六桶、`usage_model_*` / `usage_error_*` / `usage_latency_*` 各五桶、`account_quality_minute_stats`、`account_health_hourly`、授权摘要 / 窗口表、`usage_rank_snapshots`、`ai_performance_summary_windows`、`usage_overview_*_windows`、`usage_model_rank_windows` / `usage_error_rank_windows`、`usage_scope_range_windows`、`usage_quota_hourly_windows` 等，执行时逐表打印；清单由防漂移守卫测试对 schema 全量分类断言）并重置 `stats_job_state` 的 `usage_stats_aggregation` 与 `usage_quota_hourly_windows_expiry` 游标行，然后从 `usage_records` 逐批重放聚合（复用 jobs `internal/statsagg` 既有口径，经 `backend-go/projects/jobs/statsrebuild` 受控导出面）；重放排干后运行全部窗口 / 排行刷新阶段，使统计缓存面一次回到与在线收敛一致的状态。业务库与 `usage_records` 源表全程只读。
- 吞吐与续跑：`--batch-size` 默认 2000（钳 1..50000）、`--max-batches` 默认 1000（钳 1..10000）；单轮达到批数上限时退出码 3（未完成），调大上限后再次执行本命令即可完成重建（每轮重新清空并从零重放）。退出码契约：0 完成（含空源）、1 运行失败、2 用法错误（含门禁拒绝）、3 未完成、4 离线校验未通过。
- dirty 队列处置：`usage_overview_dirty_scopes` / `ai_performance_summary_dirty_system_accounts` 的在线消费方是两个窗口刷新 job 的同日增量轮；离线重建在同一执行内已按当前口径全量重建窗口，完成路径末尾清空标记；`usage_quota_hourly_window_dirty_scopes` 保留重放再生的标记（恢复在线后首轮额度窗口刷新消费补建）；`account_quality_dirty_accounts` 保留（gateway 账号质量刷新据此重算分数）。
- 空源语义：`usage_records` 为空或历史记录已丢弃时明确提示"放弃历史统计重建，后续从新请求重新累计"，统计缓存面重置为空并正常完成（退出码 0）。

## 初始化与数据边界

schema 初始化与默认 seed 由 `juhe-ai-maintenance --ensure-schema / --seed`（`--dsn` 单形态）幂等执行；项目只提供当前 PostgreSQL schema 初始化、默认 seed 和当前运行路径回归，不提供旧结构迁移脚本、启动期自动迁移、双读双写或旧 schema 兼容。历史数据是否保留、如何导入、如何对齐旧结构，由上线窗口在代码库外单独处理，并最终落到当前 schema。

初始化后必须执行登录、管理 CRUD、网关请求、usage 写入、统计聚合、缓存失效、Redis 重启降级和备份恢复验证；验证不通过时按当前 schema 和当前代码修复，不在运行路径增加旧结构兼容分支。

## 存储边界与敏感字段口径（承自 SQLite 存储说明 2026-10-25 退役）

审计与操作日志存储边界：

- 原始审计由 Go F3 在同一请求事务中按策略计算正文保留、压缩、去重和错误聚合，写入 `juhe_dataset` 的 `audit_logs`、`audit_log_attempts`、`audit_payload_refs`、`audit_payload_blobs` 和 `audit_error_groups`；网关请求链路不同步写审计表，SSE 等流式响应按终态一次签名提交。大 payload blob 由 `JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY` 指向的专用目录保存，PG 仅保存 blob 元数据和引用；audit payload 不参与统计 worker，不作为用量事实源。
- 成功样本 body 不超过 `512KB` 时保存完整正文，超过后保存 `summary_only` 摘要；问题链路 body 不超过 `2MB` 时保存完整正文，超过后保存原始 hash、大小、content type、头尾 `256KB` 和文本预览。`headers_sha256` / `body_sha256` 均针对压缩前的原始字节计算；相同 `sha256 + raw_size_bytes + content_type` 的 blob 只存一份，多条事件通过 refs 引用；未完整保留正文时 `capture_status` 必须如实标记。单个请求活跃捕获上限 `64MB`。
- 操作日志由 gateway 一次签名提交给 Go F4；F4 是 `operation_logs`、`operation_log_targets`、`operation_log_viewers` 和 `operation_log_summary_search_terms` 的唯一 writer / reader / retention owner。只记录成功提交的状态变更，查询请求不写操作日志；提交或单条落库失败只影响追溯数据，不反向回滚已提交业务变更。字段表与脱敏 allowlist 口径见 [操作日志设计](操作日志设计.md)，审计字段表见 [原始审计日志设计](原始审计日志设计.md)。
- 运行日志以完整 JSON Lines 写入角色日志文件并滚动清理；最近 3 天的索引查询只使用 `juhe_dataset.runtime_logs`，F1 索引器经 `runtime_log_file_cursors` 记录文件读取游标增量追新；keyword 只对 `message` 列做普通模糊匹配，未显式传时间范围时默认加最近 6 小时窗口，完整日志正文搜索交给 `grep 模式`。运行日志不使用内存或 Redis 逐行队列，不允许按级别采样、丢弃完整行或截断 `raw_json`。

敏感字段口径（必须加密存储）：

- OpenAI OAuth token、OpenAI / Anthropic / 各厂商上游 API Key、代理密码。
- 本地网关 API Key 经 `key_secret_encrypted` 密文保存，`key_hash` 用于网关校验，`key_prefix` / `key_suffix` 用于摘要展示；完整明文只在创建成功时返回或经单条完整密钥读取接口按资源权限返回，列表和更新响应只返回安全标识。授权账户和授权分组接口不能返回完整密钥。缺少密文或密文不可解的数据不进入运行时，应停机离线修复。
- 操作日志非敏感字段、metadata 或 summary 中由调用方错误传入的敏感值：精确命中认证容器 allowlist（`credentials` / `credential` / `token` / `key` / `secret` / `password` 及 API Key、access / refresh / id / identity token、client secret、session token、proxy password 的 camelCase / snake_case 精确字段名）时只持久化状态摘要，不扫描普通文本或字段子串。
- 外部来源系统 token 在创建响应中返回完整明文，也可按管理员权限经单条复制接口读取；`external_integration_source_tokens` 保存带用途前缀的 SHA-256 摘要 `token_hash`、完整 token 密文 `token_secret_encrypted`、展示用 `token_prefix` / `token_suffix`。默认种子写入固定来源 `extsrc_builtin_test` 与固定 token `exttok_builtin_test`（内置测试 Token，限频 `60s/10次`，只返回公开接口 mock 数据）。普通日志、运行日志、错误响应、操作记录和 demo 成功响应都不能输出真实明文 token 或 token hash。
- API Key 额度配置（`quota_limits_json`）与时间计划（`availability_schedule_json`）不属于敏感凭据，明文 JSON 保存。完整凭据展示与请求快照规则见 [安全与日志策略](安全与日志策略.md)。

## 验证要求

存储形态相关改动至少覆盖以下验证（以 Go 三项目构建与相关包 `go test` 为载体；存储测试统一经 pgtestkit 在真实 PG 上执行，属授权外部链路——唯一授权开关 `JUHE_AI_TEST_EXTERNAL=1`，未授权时真实 PG 臂一律 SKIP、SKIP 不作验证通过证据，授权后行为不变；原「PG 不可达显式失败而非 skip」口径已随 2026-10-27 测试链路治理废止，见[后端测试分层规则](../develop/后端测试分层规则.md) §3.6）：

| 类型 | 验证项 | 预期 |
| --- | --- | --- |
| 构建 | Go 三项目构建 | `gateway` / `jobs` / `maintenance` 可编译，相关 internal 包测试通过 |
| 配置 | 缺少 PostgreSQL / Redis 连接串 | 启动快速失败，错误可读 |
| Redis cache / state | TTL、失效、序列化、domain clear | 硬计数原子化不突破限制，请求路径不等待 Redis 分布式锁 |
| usage | 高并发写入和统计聚合 | 明细无丢失，统计游标与结果同事务推进 |
| 网关 | API Key 校验、调度、使用记录、审计 | 主链路成功，缓存失效后能读到新事实 |
| 运维 | PostgreSQL / Redis 重启 | 可读错误、重连、短 TTL 状态丢失可恢复 |
| 部署 | 单机 Docker | compose 启动、健康检查、备份、恢复和日志路径明确 |

## 风险

- PostgreSQL 解决了单文件写锁瓶颈，但不解决所有并发问题；热点行、唯一索引冲突、长事务和连接池耗尽仍会拖慢系统。
- Redis 可淘汰缓存与运行态如果混用同一键空间和 LRU 淘汰，可能导致限流、并发占用或调度屏蔽被意外淘汰；现行单容器以 DB 编号与键空间强制分离（J3b circuit 不得与 state 同键空间）守住该边界。
- 大并发写入如果不经连接池与批量事务背压约束，可能把数据库连接耗尽。
- 现行镜像为 `redis:7.4-alpine`；后续升级 Redis 大版本前需单独确认授权接受度与兼容性，不得在本设计里静默切换。

## AI 问答聊天存储补充

AI 问答高增长正文使用同一 PostgreSQL 集群内的独立 `juhe_chat` schema，不写入 `juhe_business`：

- `chat_conversations`、`chat_message_idempotency`、`chat_user_storage_windows` 为普通表。
- `chat_messages` 按 UTC `created_at` 建每日 range partition，并提前创建当天和下一天分区。
- 保留窗口默认 `3` 天、可配置（`JUHE_AI_CHAT_RETENTION_DAYS`，`1..365`；gateway 读取与 jobs 清理双进程必须同值）；完整过期日分区直接 drop，最老部分重叠分区按 `(expires_at, id)` 游标小批删除。
- 每用户当前保留窗口内正文默认最多 `2 GiB`，门禁只读取 `chat_user_storage_windows` 日桶，不允许 API 请求实时聚合消息明细（口径见 [AI 问答设计](AI问答设计.md)）。
- Redis 只保存可丢失的取消信号、短期 SSE 协调和运行门禁，不保存聊天正文、会话或模型上下文事实。
- 普通发布只执行 `juhe_chat` schema-only 同步，不重建 `juhe_business`、`juhe_dataset`、`juhe_usage`、`juhe_stats`、Redis 或其他非本次变更的数据。
