# PostgreSQL 与 Redis 高性能模式设计

> 本文定义当前 Node 过渡阶段从默认 SQLite + 内存缓存扩展到 PostgreSQL + Redis 高性能模式的边界。执行计划见 [PLAN-20260626T000630000Z PostgreSQL 与 Redis 高性能模式](../plans/计划-20260626T000630000Z-PostgreSQL与Redis高性能模式.md)。
> 数据库、缓存、运行态和队列的业务语义适配边界见 [存储适配接口设计](存储适配接口设计.md)。
> 统计准确性、读写资源隔离、Redis 清理和压测验收的细化规则见 [可靠统计与读写资源隔离设计](可靠统计与读写资源隔离设计.md)。
> 管理后台页面 revision、字段投影、统一确认、IndexedDB 和最近登录用户预热见 [页面数据缓存与增量更新设计](页面数据缓存与增量更新设计.md)。

> 迁移方向更新（2026-08-08）：本文仍描述当前 Node 的 `standalone` / `performance` 实现；Go 的正式目标同时包括 SQLite 和 PostgreSQL/Redis，模式只选择 adapter。PostgreSQL/Redis 下继续使用连接池、事务、lease 与幂等；未迁 Node 功能仍可使用 Redis Streams，但新 Go 完整功能直接异步执行，不引入任务 Queue Port 或通用队列。详见 [完整功能接管与 Node 归档迁移规则](../migration/完整功能接管与Node归档迁移规则.md)。

## 背景

当前默认单机模式已经通过 DB service、SQLite 多库拆分、usage shard、单写者 writer queue 和预聚合统计缓解了大部分轻量部署瓶颈。但随着用户量和网关请求量上升，瓶颈已经从“单个 SQL 慢”变成了几类系统性限制：

- SQLite 文件级写锁要求同一数据库文件只能有一个运行时 writer，高频写入只能通过串行 owner / shard 缓解。
- 进程内 LRU 缓存无法跨 server、DB service 和 worker 共享，进程重启后短 TTL 运行态全部丢失。
- usage、审计、运行日志索引、统计聚合等队列在 SQLite 模式下必须尽量让出写锁，不能按数据库实际并发能力扩展。
- 后续如果继续扩大用户数，需要事实存储、运行态缓存、连接池、队列背压和部署监控一起升级，而不是只替换 SQL 语法。

因此新增两套明确运行模式：

| 模式 | 数据库 | 缓存 / 运行态 | 默认用途 |
| --- | --- | --- | --- |
| `standalone` | SQLite 多库 + usage shard | 进程内 LRU / Map | 默认单机、轻量部署、本地开发 |
| `performance` | PostgreSQL | Redis | 高用户量、高请求量、Docker / 生产部署 |

默认仍是 `standalone`，只有显式配置 `performance` 才启用 PostgreSQL 和 Redis。

## 目标

- 保持默认单机模式行为不变，现有用户不配置 PostgreSQL / Redis 也能继续运行。
- 高性能模式使用 PostgreSQL 承接所有事实数据、预聚合统计、日志索引和运行可恢复数据。
- 高性能模式使用 Redis 承接跨进程短 TTL 缓存、调度运行态、限流、验证码 / 登录失败窗口、缓存版本和必要的原子计数。
- 数据访问方只依赖统一 Store Port / cache / runtime state / queue 接口，不在业务代码里散落 `if sqlite / if postgres / if redis`。
- 复用现有 typed command 和队列语义，但 PostgreSQL 模式下消费端可以并发执行，默认最大消费并发为 `100`，并受连接池、队列容量和同键顺序约束保护。
- 不在代码库里做 SQLite 与 PostgreSQL 数据迁移、旧 PostgreSQL 结构迁移、双读、双写、自动迁移或旧结构兼容；历史数据处理由上线窗口在代码库外单独完成，应用只面向当前 schema。

## 不做什么

- 不把 PostgreSQL 高性能模式做成默认依赖。
- 不在首期引入 Kafka、RabbitMQ、BullMQ、ClickHouse 或其他外部队列 / OLAP。
- 不在首期实现多节点自动故障转移、跨地域复制或自动用户迁移；多节点路由仍按 [分布式部署与用户分片设计](分布式部署与用户分片设计.md) 单独推进。
- 不把 Redis 作为账务、权限、授权、账号健康、审计或使用记录的持久事实库；但在高性能模式下，跨进程短 TTL 缓存、运行态、原子计数和记录型队列的当前事实源必须是 Redis / Redis Streams，不能回退到进程内 memory 或本地 IPC 后继续声明高性能模式可用。Redis Streams 消息成功写入事实库后才 ack。
- 不为了迁移保留运行时旧 schema 兼容分支；历史数据处理只允许离线脚本或重建流程。

## 配置模型

配置文件按职责分层：

- `backend/.env` 只保留通用和 standalone 默认配置，例如监听端口、密钥、Cookie、日志、SQLite 路径和 smoke 参数。
- 非 Docker 高性能节点复制 `backend/.env.performance.example` 为 `backend/.env.performance`，再通过 `JUHE_AI_ENV_FILE=./.env.performance` 从主配置加载覆盖项。
- Docker 高性能部署只使用 `docker/.env.performance` 和 `docker/compose.performance.yml` 管理中间件、容器内连接串和高并发参数。
- 加载优先级为进程环境变量 > `JUHE_AI_ENV_FILE` 指向的覆盖文件 > `backend/.env`。

新增运行模式配置：

```dotenv
JUHE_AI_RUNTIME_MODE=standalone
JUHE_AI_DATABASE_DRIVER=sqlite
JUHE_AI_CACHE_DRIVER=memory
JUHE_AI_RUNTIME_STATE_DRIVER=memory
JUHE_AI_QUEUE_DRIVER=memory
```

高性能模式：

```dotenv
JUHE_AI_RUNTIME_MODE=performance
JUHE_AI_DATABASE_DRIVER=postgres
JUHE_AI_CACHE_DRIVER=redis
JUHE_AI_RUNTIME_STATE_DRIVER=redis
JUHE_AI_QUEUE_DRIVER=redis_stream
JUHE_AI_POSTGRES_URL=postgres://juhe_ai:<密码URL编码>@pgbouncer:5432/juhe_ai
JUHE_AI_REDIS_CACHE_URL=redis://:<缓存密码URL编码>@redis-cache:6379/0
JUHE_AI_REDIS_STATE_URL=redis://:<运行态密码URL编码>@redis-state:6379/0
JUHE_AI_REDIS_QUEUE_URL=redis://:<队列密码URL编码>@redis-queue:6379/0
JUHE_AI_REDIS_NAMESPACE=prod
JUHE_AI_REDIS_STREAM_READ_COUNT=1000
JUHE_AI_REDIS_STREAM_BLOCK_MS=1000
JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS=60000
JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT=256
JUHE_AI_DB_POOL_MAX=150
JUHE_AI_DB_WRITE_MAX_CONCURRENCY=100
JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS=50000
JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS=30000
JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS=2000
JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS=30000
```

规则：

- `JUHE_AI_RUNTIME_MODE=performance` 时必须同时配置 PostgreSQL、Redis cache、Redis state 和 Redis queue；缺失时快速失败。
- `JUHE_AI_DATABASE_DRIVER=postgres` 时不读取 `JUHE_AI_DATABASE_PATH`、`JUHE_AI_DATASET_DATABASE_PATH`、`JUHE_AI_USAGE_CATALOG_DATABASE_PATH`、`JUHE_AI_STATS_DATABASE_PATH` 和 `JUHE_AI_USAGE_SHARD_ROOT`。
- `JUHE_AI_CACHE_DRIVER=redis` 只表示可丢弃缓存；需要硬并发槽、限流计数或短 TTL 调度运行态时必须走 `JUHE_AI_RUNTIME_STATE_DRIVER`。
- `JUHE_AI_QUEUE_DRIVER=redis_stream` 使用 Redis Streams 承接高性能模式队列缓冲；`JUHE_AI_REDIS_QUEUE_URL` 默认应指向独立 `redis-queue`，不再复用 `redis-state`。Redis Stream 入队失败不回退 IPC 或本地内存队列，避免掩盖队列基础设施故障；可靠队列入队不使用 `MAXLEN ~` 自动裁剪，消息成功 `XACK` 后同步 `XDEL` 删除已落库条目。
- `JUHE_AI_REDIS_NAMESPACE` 是 Redis key 的部署隔离前缀，生产建议显式配置并保持稳定；压测、回归和多套环境共用 Redis 实例时必须使用不同 namespace。
- 高性能模式强制 cache/state/queue 指向三个不同的物理 Redis `host:port`；同一进程的不同 DB 也不算隔离，且不存在共享开关。生产本机 loopback 必须写成 `127.0.0.1`，Docker service name 或明确的远端内网 host 仍可使用，但三个物理 endpoint 必须不同。
- 普通单条 SQL 使用数据库或应用 role 默认的 `statement_timeout`、`lock_timeout` 和 `idle_in_transaction_session_timeout`；显式多语句事务再按 `JUHE_AI_POSTGRES_*_TIMEOUT_MS` 执行 `SET LOCAL`。上线必须通过 PgBouncer 新连接 `SHOW` 三项默认值，不能把 timeout 追加到 URL startup parameter。
- 生产环境不使用 `latest` 镜像；PostgreSQL 和 Redis 镜像必须固定 major / patch 或 digest。
- 高性能模式不能为了吞吐自行降低原始审计保留语义，必须和 standalone 使用相同的显式部署配置。默认完全成功请求先进入最近 `1` 小时热保留窗口，超过热窗口后只保留 `10%` 稳定采样；运维可按统一环境变量契约显式调整或关闭成功审计，失败、异常、中断和重试后成功链路仍全量进入审计。程序不因容量压力自动降采样，容量和吞吐问题通过 Redis Streams 背压、PG 小批次写入、热窗口清理、payload 摘要 / 压缩 / 去重治理。

## 部署边界

测试环境使用本地 Linux Docker 主机 `<测试主机IP>` 搭建；凭据只保存在部署目录和服务器安全配置中，不写入仓库文档。后续生产环境复用同一套 compose / env / backup / monitoring 结构。

首期推荐 Docker 服务：

| 服务 | 职责 | 说明 |
| --- | --- | --- |
| `postgres` | 主事实库 | 使用 PostgreSQL 18 当前稳定版本线；PostgreSQL 19 仍处 beta 阶段，不作为生产默认。 |
| `pgbouncer` | 连接池网关 | 应用进程和 worker 连接 PgBouncer，避免多进程各自连接池把 PostgreSQL 连接打爆。 |
| `redis-cache` | 可丢弃缓存 | `appendonly no`、`save ""`、`maxmemory-policy=allkeys-lru`，只放可重建缓存。 |
| `redis-state` | 运行态和原子计数 | `appendonly no`、`save ""`、`maxmemory-policy=noeviction`；重启后从 PostgreSQL 和请求流量重建。 |
| `redis-queue` | Redis Streams 可靠队列 | `appendonly yes`、`appendfsync everysec`、`save ""`、`maxmemory-policy=noeviction`；未 ACK 消息不可按缓存丢弃。 |
| `juhe-ai` | 后端服务 | 按现有 server / DB service / worker 拓扑启动，连接 PostgreSQL 和 Redis。 |

版本参考：

- PostgreSQL 18 是当前正式版本线，参考 [PostgreSQL 18 Released](https://www.postgresql.org/about/news/postgresql-18-released-3142/) 和 [PostgreSQL 18 release notes](https://www.postgresql.org/docs/release/18.0/)；PostgreSQL 19 Beta 1 只用于测试预览，参考 [PostgreSQL 19 Beta 1 Released](https://www.postgresql.org/about/news/postgresql-19-beta-1-released-3313/)，不进入生产默认。
- Redis 8 已 GA，参考 [Redis 8 GA](https://redis.io/blog/redis-8-ga/)；当前 Redis Open Source 版本线包含 AGPLv3 / RSALv2 / SSPLv1 三许可，参考 [Redis licenses](https://redis.io/legal/licenses/) 和 [Redis official Docker image license](https://hub.docker.com/_/redis)。生产前必须确认授权接受度。如果后续不接受 Redis 8 授权，需要单独评估 Valkey 替代方案，不能在本设计里静默切换。

## PostgreSQL 存储形态

PostgreSQL 模式不再模拟多个 SQLite 文件，而是把当前事实域映射为 schema：

| PostgreSQL schema | 当前 SQLite 域 | 主要表 |
| --- | --- | --- |
| `juhe_business` | 业务库 | 系统账户、会话、供应商、账号、分组、API Key、授权、代理、设置、公告 |
| `juhe_dataset` | 数据集目录库 | 审计元数据、操作日志、公开接口日志、模型检测、记录清理目标；运行日志索引表由 Go F1 indexer 唯一写入，Node 仅只读 |
| `juhe_usage` | 使用记录目录库 + usage shard | `usage_records`、使用记录列表索引、账号 / API Key scope catalog |
| `juhe_stats` | 统计结果库 | 用量桶、额度窗口、范围窗口、排行、账号质量、系统监控、表监控、`stats_job_state` |
| `juhe_codex_context` | Responses 桥接状态索引 | Responses session / response / compact 索引 |

表名可以保留当前语义，代码通过 repository / dialect 选择 schema，不把 schema 名写进业务服务层。
表监控在 PostgreSQL 模式下按这 5 个 schema 采样 relation size、估算行数和 1 小时 / 24 小时增长，结果统一写入 `juhe_stats.database_storage_snapshots` 与 `juhe_stats.table_storage_snapshots`，不回读 SQLite 文件路径。

当前已新增 `backend/src/storage/postgres-schema.ts`，从现有 SQLite schema DDL 收集建表 / 建索引语句并映射为 PostgreSQL SQL：移除 `PRAGMA`，把 `COLLATE NOCASE` 映射为 `lower(...)` 表达式索引，把 SQLite JSON object check 映射为 `jsonb_typeof(...::jsonb)`，并按外键依赖重新排序 `CREATE TABLE`，避免 PostgreSQL 的前向外键引用失败。`postgres:init-schema` 默认执行 schema 初始化并写入默认种子数据，`postgres:init-schema-only` 只执行 DDL。当前版本完整初始化应以 `test:postgres-schema-sql` / `test:postgres-seed-defaults` 的实际输出为准；最近本地校验口径为 5 个 schema、594 条 schema 语句。

### usage_records 当前形态

高性能模式不继续复制 SQLite usage catalog 明细模型，`juhe_usage.usage_records` 当前按 `created_at` 建日分区父表，主键为 `(created_at, id)`。写入前会按记录时间确保对应日分区存在，列表、详情、统计游标和清理都必须带上时间窗口或由 usage id 推导分区窗口，不能对分区父表做无界扫描。

- 分区键：`created_at` 日 range partition；过期在线数据在统计安全游标追平后按分区事务内 `DETACH / DROP`，不保留同库冷归档副本或归档 manifest。
- 热查询索引白名单以用户维度开头：`system_account_id + created_at + id`、`system_account_id + api_key_id + created_at + id`、`system_account_id + account_id + created_at + id`、`system_account_id + trace_id COLLATE "C" + created_at + id`。
- 管理员使用记录页未限定用户时只能读取部署时区当天、`created_at DESC, id DESC` 的全用户列表，由当天分区和父表复合主键 `(created_at, id)` 承接；账户名称、结果、状态码、IP、分组、模型、trace ID、请求来源、手工日期或非默认排序必须先选择系统账户。其他管理员大表页面仍按各自功能边界先限定用户和日期窗口；独立 `trace_id`、独立 `request_id`、全局 `model/path/status/client_ip` 不能作为大表默认索引。
- PostgreSQL 前缀筛选必须显式使用稳定 collation。`trace_id`、`client_ip`、API Key 名称和 AI 性能账号选项名称等文本前缀查询使用 `COLLATE "C"`、二进制上界和对应 C collation 表达式索引；不能依赖 `prefix + '\uffff'`，也不能假设数据库默认 collation 的排序行为和 SQLite 一致。
- 清理策略：常规过期清理按统计安全游标追平后处理整日分区；已删除账号 / API Key 的关联明细小批次清理必须使用 `(created_at, id)` 命中分区，不能只按裸 `id` 删除。
- 统计游标：`stats.stats_job_state` 继续记录按分区 / shard 窗口推进的游标，统计写入和游标推进在同一 PostgreSQL 事务提交。

### JSON 与时间

- SQLite JSON 字符串字段在 PostgreSQL 中按用途选择 `jsonb` 或 `text`。需要按字段筛选、局部更新或索引的配置字段使用 `jsonb`。
- 所有时间统一保存为 `timestamptz`，接口返回继续使用 ISO 字符串。
- 金额和成本如果需要精确累加，优先使用 `numeric`；只作为展示缓存且已有浮点口径的字段可以保持 `double precision`，但统计总量字段必须固定类型并写清楚。
- `juhe_business.accounts` 使用 `health_check_model` 保存账户必填检查模型，不保留 `default_test_model` 或 `health_check_enabled`；`provider_default_health_check_models` 保存个人供应商默认，`provider_system_default_health_check_models` 保存管理员系统默认，协议档案保留内置默认。三层默认只初始化新账户。
- `juhe_business.provider_model_catalog` 和 `juhe_business.custom_provider_models` 复用 `supported_service_tiers_json`、`supported_reasoning_efforts_json` 和 `default_reasoning_effort`，并各用一个 `service_tier_prices_json` 保存非标准实际档位价格；标准价继续使用现有扁平字段，不新增价格表。支持请求覆盖的供应商账户凭据 JSON 可选保存现有 `service_tier_override`、`reasoning_effort_override`。字段值由所属供应商模型共同能力和目标 driver 校验，`ultra` 与 Responses Multi-agent Beta 不属于这两个账户覆盖字段。

### 约束与并发

- 业务唯一约束继续由数据库兜底，例如账号名称、API Key token hash、分组绑定、授权来源等。
- 管理端幂等仍保留 Redis / 内存防重复提交 + 数据库唯一约束双层保护。
- PostgreSQL 不存在 SQLite 文件级全局写锁，但仍存在行锁、索引页竞争、连接池耗尽、长事务和 autovacuum 压力；高性能模式不能把所有队列无限并发。
- System API 管理端 DB 在途请求默认保留保护阈值：standalone 默认 `64`，performance 默认 `256`，可按高性能部署压测继续调整。
- 同一账号等热点资源的高频并发写入仍会形成行锁排队；需要按资源维度串行、合并写入或把短 TTL 运行态拆到 Redis，不能因为切换 PostgreSQL 就把同一行无限并发写。
- AI 账户批量编辑必须在单个 PostgreSQL 事务内校验全部自有物理账户、字段白名单、同构模型条件和 `expectedVersions`，任一版本冲突或配置非法时整批回滚。事务提交后再失效缓存和投递必要的后台系统检查。

## SQL Dialect 与 Repository

新增统一数据库接口：

```ts
interface DatabaseClient {
  query<T>(sql: SqlStatement, params?: unknown[]): Promise<T[]>
  one<T>(sql: SqlStatement, params?: unknown[]): Promise<T | undefined>
  execute(sql: SqlStatement, params?: unknown[]): Promise<ExecuteResult>
  transaction<T>(fn: (tx: DatabaseTransaction) => Promise<T>): Promise<T>
}
```

新增 `SqlDialect` 收口差异：

| 差异 | SQLite | PostgreSQL |
| --- | --- | --- |
| 占位符 | `?` | `$1` / `$2` |
| upsert | `INSERT ... ON CONFLICT ... DO UPDATE` | 同语义，但 `excluded`、条件更新和 `RETURNING` 单独生成 |
| 自增 / ID | 当前字符串 ID 为主 | 继续字符串 ID；不要依赖 serial 作为业务 ID |
| 时间函数 | `datetime(...)` / 应用层 ISO | `now()` / `timestamptz` |
| JSON | text + 应用解析 / SQLite JSON 函数 | `jsonb` + `->` / `->>` / GIN 索引 |
| 返回插入结果 | `last_insert_rowid` / changes | `RETURNING` |
| PRAGMA | 需要 | 不存在 |

repository 迁移原则：

- 业务 service 不感知数据库方言。
- 不新增任意 SQL 执行器；DB service 和 worker 仍只暴露 typed operation。
- 大查询、列表、统计和维护清理继续使用稳定排序、窗口分页和游标。
- SQLite 模式仍走同步 `node:sqlite` 包装，但通过同一 repository 接口暴露。

当前已新增 `backend/src/storage/database-client.ts`，提供 `DatabaseClient`、SQLite driver、PostgreSQL driver 和 `SqlDialect` 基础实现。已覆盖 `?` 到 `$1` / `$2` 占位符转换、调用层动态 `IN (...)` 占位符、标识符引用、SQLite 真库 CRUD / upsert / transaction、PostgreSQL query / execute / transaction 和多语句 DDL result 归一化；`postgres:init-schema` 已通过该基础层执行真实远端 PostgreSQL schema / seed 初始化。

第一条业务 repository 读路径已落地到 `backend/src/storage/provider.repository.ts`：

- 新增 `listProvidersAsync()`、协议供应商列表、协议档案查找、默认检查模型和启用协议档案校验的 async 双 driver 版本。
- PostgreSQL 查询通过 `juhe_business` schema 读取 `providers`、`provider_protocol_profiles`、`provider_protocol_profile_families` 和 `protocol_endpoint_families`。
- `backend/src/storage/repositories.ts` 已导出 async provider API；`/__aisys__/api/providers` 和 `/__aisys__/api/providers/options` 已切到 async repository。
- `pnpm --filter juhe-ai-backend test:provider-repository-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的供应商读取一致性。

登录 / 会话和系统账户管理关键路径已落地到 `backend/src/storage/system-accounts.repository.ts`：

- 新增默认管理员凭据校验、按 ID / 用户名读取系统账户、创建 / 查询 / touch / 撤销 session、更新最近登录时间的 async 双 driver 版本。
- `backend/src/modules/auth/auth.routes.ts` 的登录、登出和 `/auth/me` 会话读取，以及 `backend/src/modules/auth/auth.middleware.ts` 的 `requireAuth` 已切到 async session repository。
- 新增系统账户分页列表、选项列表、创建、更新、预哈希创建 / 更新的 async 双 driver 版本；创建系统账户时会在同一 transaction 内写入默认内置分组。
- `backend/src/modules/system-accounts/system-accounts.routes.ts` 的列表、选项、创建和更新已切到 async repository，并通过 `runLoggedOperationAsync()` 保持操作日志入队语义。
- `pnpm --filter juhe-ai-backend test:system-account-session-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的登录 / 会话仓储一致性。
- `pnpm --filter juhe-ai-backend test:system-account-management-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的系统账户管理读写一致性。

登录前公共设置、系统 API 限流设置读取和系统设置管理读写已落地到 `backend/src/storage/settings.repository.ts`：

- 新增公共全局设置列表、全局设置列表和按 key 批量读取系统设置的 async 双 driver 版本。
- 新增全局设置和系统设置局部更新的 async 双 driver 版本，写入通过 `DatabaseClient.transaction()` 执行，并在写入后清理 settings cache。
- `backend/src/modules/system-api/system-api-app.ts` 的 `/__aisys__/api/settings/public`、`backend/src/modules/system-api/system-api-rate-limit.middleware.ts` 的限流配置读取，以及 `backend/src/modules/settings/settings.routes.ts` 的 `/settings/global`、`/settings` GET / PATCH 已切到 async settings repository。
- `backend/src/modules/operation-logs/operation-log.service.ts` 新增 `runLoggedOperationAsync()`，用于 settings async 路由在记录操作日志前通过 `getSettingsAsync()` 读取 operation log 配置。
- `backend/src/shared/gateway-cache-invalidation.ts` 在 PostgreSQL driver 下不再触碰 SQLite after-commit 队列，已迁移的 PG 写路径在事务提交后直接执行当前进程 invalidator；performance 模式下会把网关运行态、授权额度和 API Key 额度缓存失效版本写入 Redis runtime state，其他 server 进程在网关 / 配额读取入口按 1 秒节流同步版本并触发本地 handler。
- `pnpm --filter juhe-ai-backend test:settings-public-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的设置读取一致性。
- `pnpm --filter juhe-ai-backend test:settings-management-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的设置管理读写一致性。
- `pnpm --filter juhe-ai-backend test:performance-system-api-smoke` 已覆盖 public settings、captcha、login、me、settings GET / PATCH、system-accounts GET / POST / PATCH、providers、provider options 和 logout 的最小 HTTP 链路。
- PostgreSQL 模式暂不支持在线修改 `usageStatsTimezone`；统计分区和重建流程完成前，统计时区调整必须走停机离线迁移 / 重建。

分组管理关键路径已落地到 `backend/src/storage/group-read.repository.ts`、`backend/src/storage/group-summary.repository.ts` 和 `backend/src/storage/group-write.repository.ts`：

- 新增分组列表、分页、选项、账户组选项、摘要读取、创建、更新和删除的 async 双 driver 版本。
- PostgreSQL 查询通过 `juhe_business` schema 读取 `groups`、`group_accounts`、`accounts`、`resource_authorizations`、`group_authorization_settings`、`api_keys`、`route_strategies` 和 `route_strategy_groups`。
- 创建和更新保留供应商校验、同供应商分组名称唯一性、默认分组只读、高并发分组调度策略、授权分组本地设置和网关缓存失效语义。
- 删除保留 API Key 唯一启用号池保护；PG 模式下删除后暂不写 SQLite stats dirty 标记，等待统计 repository PG 适配后改写 `juhe_stats`。
- PG 模式下分组列表和账户组选项会读取真实分组与绑定账户 ID；`accountStats` 的用量、状态聚合和授权来源详情仍等待账号、授权、usage 和 stats repository 迁移，当前不在请求链路临时扫描明细表。
- `backend/src/modules/groups/groups.routes.ts` 的列表、选项、账户组选项、创建、更新和删除已切到 async repository；`return-authorization` 仍依赖 resource authorization 写仓库，待授权 repository 迁移后再切换。
- `pnpm --filter juhe-ai-backend test:group-management-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的分组管理读写一致性。
- `pnpm --filter juhe-ai-backend test:performance-system-api-smoke` 已扩展覆盖 groups GET / POST / PATCH / DELETE 的最小 HTTP 链路。

API Key 管理关键路径已落地到 `backend/src/storage/api-key.repository.ts`、`backend/src/storage/api-key-mappers.ts` 和 `backend/src/storage/route-strategy.repository.ts`：

- 新增 API Key 列表、分页、摘要、secret 查询、创建、更新、刷新密钥和删除的 async 双 driver 版本。
- PostgreSQL 查询通过 `juhe_business` schema 读取 `api_keys`、`route_strategies`、`route_strategy_groups`、`groups`、`system_accounts`、`resource_authorizations`、`group_authorization_settings` 和 `request_quota_hourly_window_configs`。
- 创建和更新保留路由策略绑定边界、同账户名称唯一性、启用策略保护、策略分组优先级唯一性、额度限制、时间计划、密钥加密和网关缓存 / 额度缓存失效语义。
- 网关 API Key 校验入口已新增 async 双 driver 版本；PG 模式下 `read_gateway_runtime` 能读取有效 API Key、绑定路由策略、网关设置、分组访问元数据、响应检查策略和最小可调度账号候选。候选账号读取已通过 `juhe_business` / `juhe_stats` schema 覆盖策略分组绑定、账号状态、授权实例、支持模型、模型映射、API Key 运行态、代理和质量分窗口；完整 `/v1` PG-ready 仍需继续迁移账号管理写路径、usage / stats、授权额度和审计记录链路。
- `account_supported_models` 和 `account_model_mappings` 已新增 async replace helper；PG 模式下通过事务先删后写，供账号创建 / 更新迁移复用。`test:api-key-management-driver` 已覆盖创建账号时直接写入最小候选账号模型配置，并从 `read_gateway_runtime` 断言读回。
- `createAccountAsync` 的 PG 创建事务统一写入 `accounts.health_check_model`、支持模型和模型映射；新建以及导入请求中的 `active` 都先保存为 `pending_test`，提交后投递后台激活检查，成功后才进入 `active`。人工草稿测试任务 ID 不进入创建事务，也不能作为激活凭证。
- 自定义模型管理的 PG 路径必须读写 GPT 精确服务等级和思考级别能力字段；`/providers/models/options` 同步返回能力数组，账户保存按最终上游模型能力校验请求覆盖。Codex `/models` 使用独立 Codex 能力事实。
- `listAccountsPageAsync()` 的 PG 账号列表分页只返回展示、筛选、权限和乐观版本所需摘要，不为测试或编辑提前装配完整支持模型、检查模型、endpoint modes 或凭据；不做精确 `COUNT(*)`，沿用 `pageSize + 1` 上界 total 语义。
- `listAccountOptionsAsync()` 已新增 PG 普通 owner 账号 options 轻量读取：支持 ID、账号名称精确 / 前缀 / 词项候选包含搜索、供应商、协议档案、分组、标签、类型、状态和调度状态过滤；只返回下拉所需轻量字段，不读取统计库质量分和 usage 聚合。
- `findAccountSummaryAsync()` / 账户详情路径从 `accounts`、支持模型、模型映射、标签和授权来源装配完整配置；列表人工测试另用 `GET .../:id/test-options` 按需读取 `healthCheckModel`、可用请求形态和账户所有者可见的启用文本目录模型，不受 `supportedModels` 限制，也不返回凭据。
- `updateAccountAsync()` 和 `setAccountGroupAsync()` 的 PG 更新路径不把普通编辑作为状态入口。凭据、Base URL、协议档案或关键代理变化后进入 `pending_test` 并投递后台激活复检；检查模型变化立即投递后台健康检查；支持模型变化必须保证检查模型仍有效；非连接配置不改状态。
- `batchUpdateAccountsAsync()` 通过独立管理侧 / 用户侧接口处理至少 2 个账户，显式白名单覆盖、拒绝授权实例和身份 / 连接私密 / 状态运行态字段；模型区只在全部目标具有相同 `providerCode + providerProtocolProfileId + type` 时开放，并使用乐观锁全成全败。
- `clearAccountFailureStateAsync()` 已新增 PG 普通 owner 账号异常状态恢复路径，`updateAuthorizedAccountBindingDispatchAsync()` 已新增 PG 授权实例调度恢复路径：支持将临时不可用、限流中和错误状态恢复为 active，并清理冷却、失败、stream failure 和重测状态字段；套餐过期 owner 账号仍保持 disabled 并写入 `account_expired`。授权实例恢复会校验当前被授权账号、目标分组和运行态授权绑定，避免跨用户恢复。
- `deleteAccountWithRelatedCleanupAsync()` 已新增 PG 普通 owner 账号逻辑删除路径：事务内撤销该账号资源的授权 grant / runtime authorization / source，逻辑删除源账号和对应授权实例，清理标签绑定与名称搜索索引，提交后失效账号、分组、网关和授权运行态缓存。该路径暂不执行过期已删除账号的物理清理，也不登记 dataset / stats 历史数据清理目标。
- `returnAccountAuthorizationInstanceForGranteeAsync()` 已新增 PG 授权账户实例归还路径：通过事务把个人直授权 grant 标记为 `returned`，撤销 manual source，并按 active team / paused team / active manual / 无有效来源顺序刷新 runtime authorization；`POST /__aisys__/api/accounts/:id/return-authorization` 已切到 async repository。
- `returnResourceAuthorizationForGranteeAsync()` 和 `returnGroupAuthorizationForGranteeAsync()` 已新增 PG 授权列表个人归还 / 分组个人归还路径；`POST /__aisys__/api/authorizations/:id/return` 和 `POST /__aisys__/api/groups/:id/return-authorization` 已切到 async repository。当前覆盖个人归还，团队归还仍按后续完整授权场景继续补回归。
- `listResourceAuthorizationsPageAsync()` 和授权候选项 options 已新增 PG 读取路径：`/__aisys__/api/authorizations`、`/__aisys__/api/my-authorizations`、`/__aisys__/api/authorization-options/grantee-accounts`、`grantee-teams`、`grantee-groups` 会读取 `juhe_business` 授权 / 系统账号 / 团队 / 分组表，并通过 `juhe_stats` 预聚合表装配授权列表 usage 摘要。路由进入列表前的统计时区读取已新增 `usageStatsTimezoneAsync()`，PG 模式下不再回退打开 SQLite 设置库。
- `getResourceAuthorizationUsageAsync()` 已新增 PG 授权 usage 详情读取路径：`GET /__aisys__/api/authorizations/:id/usage` 会通过 `juhe_business.resource_authorizations` 定位运行态授权，并从 `juhe_stats.usage_scope_range_windows` / `authorization_team_usage_range_windows` 读取个人或团队授权范围窗口；路由使用异步统计时区读取，不再在 PG 模式下打开 SQLite 设置库。
- `createResourceAuthorizationAsync()` 已新增 PG 授权创建路径：`POST /__aisys__/api/authorizations` 会在同一事务内写入 grant、runtime authorization、source、额度窗口配置；授权 AI 账号给个人时会创建或恢复授权实例账号并绑定目标分组。当前 HTTP smoke 覆盖个人账号授权创建 / 回收，团队授权 fanout 逻辑已按同步路径迁移但仍建议补独立团队场景回归。
- `updateResourceAuthorizationAsync()` 和 `revokeResourceAuthorizationAsync()` 已新增 PG 现有授权管理写路径：`PATCH /__aisys__/api/authorizations/:id` 支持暂停 / 恢复和额度更新，`PATCH /__aisys__/api/authorizations/:id/expire` 支持有效期与额度更新，`DELETE /__aisys__/api/authorizations/:id` 支持回收；PG 事务内会更新 grant、runtime authorization、source、额度窗口配置，并在提交后失效网关运行态、授权额度、API Key 校验和授权读取缓存。
- 完整账号管理端还需授权实例列表视图、过期物理清理、统计聚合和使用记录读写继续迁移；当前 `authorizations` 路由本身没有独立 `GET /:id` 详情入口。
- PG 模式下 API Key 摘要会读取真实绑定路由策略及其策略分组；列表按当前页 Key 批量读取 `juhe_stats` 预聚合 `usage`，不再通过前端独立用量请求补发，也不扫描使用记录明细。
- PG 模式下删除 API Key 会删除业务库中的 key 和绑定，并投递关联记录清理目标；record-maintenance 通过 PostgreSQL usage / dataset / stats 清理实现推进，不再因 PostgreSQL driver 跳过历史数据清理。
- PG 模式下 API Key 列表 keyword 搜索使用 `matched_api_key_ids` materialized CTE 先按 `lower(name) COLLATE "C"` 前缀范围命中名称索引，再按原列表排序输出；如果直接在主查询中叠加 keyword 过滤，PostgreSQL 可能优先选择列表排序索引后过滤名称，导致大表搜索退化。
- PG 模式下 API Key 创建 / 更新混合路由配置会通过 async provider 与模型目录读取校验评分模型、质量评分模型和等级目标模型；`test:performance-system-api-smoke` 已覆盖 SQLite 与远端 PostgreSQL / Redis 下混合路由 API Key 的 HTTP 创建、更新和删除。
- `backend/src/modules/api-keys/api-keys.routes.ts` 的列表、secret、创建、更新、刷新密钥和删除已切到 async repository。
- `pnpm --filter juhe-ai-backend test:api-key-management-driver` 已覆盖本地 SQLite 和远端 PostgreSQL / Redis URL 下的 API Key 管理读写、网关 Key 校验和 `read_gateway_runtime` Key 读取入口一致性。
- `pnpm --filter juhe-ai-backend test:performance-system-api-smoke` 已扩展覆盖 accounts POST / GET 列表 / 详情 / options / PATCH / clearFailureState / DELETE、authorizations `POST` / `GET /:id/usage` / `PATCH /:id` / `PATCH /:id/expire` / `DELETE /:id` 以及 api-keys GET / POST / PATCH / secret / refresh-key / DELETE 的最小 HTTP 链路。

其他业务 repository 仍待逐个迁移到该接口，尤其是完整授权实例列表视图、统计和使用记录读写路径；不能据此认为完整管理端已 PG-ready。

## Redis 缓存与运行态

缓存层拆成三类：

```ts
interface AppCache<K, V> {
  get(key: K): V | undefined
  set(key: K, value: V, options?: { ttlMs?: number }): void
  delete(key: K): void
  clear(): void
}

interface SharedJsonCache<V> {
  get(key: string): Promise<V | undefined>
  set(key: string, value: V, options?: { ttlMs?: number }): Promise<void>
  delete(key: string): Promise<void>
  clear(): Promise<void>
}

interface RuntimeStateStore {
  getJson<T>(key: RuntimeKey): Promise<T | undefined>
  getDeleteJson<T>(key: RuntimeKey): Promise<T | undefined>
  setJson<T>(key: RuntimeKey, value: T, ttlMs: number): Promise<void>
  delete(key: RuntimeKey): Promise<void>
  incr(key: RuntimeKey, options: { ttlMs: number; max?: number }): Promise<number>
  acquireLock(key: RuntimeKey, options: { ttlMs: number; token: string }): Promise<boolean>
  releaseLock(key: RuntimeKey, token: string): Promise<void>
}
```

边界：

- `AppCache` 是进程本地同步缓存，可存放不可序列化对象，例如 HTTP agent、临时近端只读结果和进程内 memoization；高性能模式下仍允许使用，但不能承载跨进程事实源。需要跨进程一致的缓存必须接入 `SharedJsonCache` / Redis，`AppCache` 只能作为可丢弃本地 L1 或明确的进程内易失优化。
- `SharedJsonCache` 是跨进程可丢弃 JSON 缓存，`standalone` 使用 memory driver，`performance` 使用 Redis cache driver。
- `RuntimeStateStore` 是跨进程短 TTL 运行态，`standalone` 使用 memory driver，`performance` 使用 Redis state driver；硬并发槽、限流计数这类不能超卖的状态必须通过该工具或封装后的专用原子操作访问。`acquireLock / releaseLock` 只保留给明确需要单资源串行化且不可接受重复执行的基础设施保护，例如 OAuth access token 单账号刷新；网关调度、账号运行态探针、上游桶避让、IP 级账号回避、IP 错误熔断和 Codex turn retry 不允许在请求路径使用 Redis 分布式锁。

Redis key 统一命名：

```text
juhe-ai:{namespace}:{driver}:{cache-name}:v{version}:{scope}:{key}
```

规则：

- `{namespace}` 由 `JUHE_AI_REDIS_NAMESPACE` 决定；没有显式配置时运行配置会基于 `JUHE_AI_SECRET` 派生稳定默认值，但生产仍建议写明。
- `clear()` 不做全库 `SCAN + DEL`，优先递增 domain version，实现常量成本失效。
- 所有 Redis cache / state key 必须有 TTL；确需长期保留的运行态要先证明不是持久业务事实。Redis Streams 使用长度、pending 和 consumer 监控控制容量，不用 TTL 表达消息生命周期。
- Redis payload 使用 JSON，并带 `schemaVersion`；结构变化时递增 cache domain version。
- API Key 明文、OAuth token、代理密码、完整请求 / 响应 payload、审计正文和可能造成越权的权限中间结果不得进入通用 Redis cache。
- 调度运行态、并发占用、IP 级错误熔断、登录失败窗口、验证码挑战、会话亲和和 cache invalidation index 在 performance 模式下进入 Redis state。
- 当前已落地 `RuntimeStateStore` Redis driver、`RuntimeProbeStateStore` Redis driver、`SharedJsonCache` Redis driver、登录失败窗口 Redis state、验证码 challenge / 发放限频 Redis state、账号并发槽 Redis 原子获取 / 释放、账号并发列表展示 Redis 批量读取、网关缓存失效 runtime state 版本广播、AI 账户运行态探针 due / generation、上游桶避让、IP 级账号回避、IP 错误熔断、Codex turn retry 和记录型 Redis Streams 队列；不能在 performance 模式下把跨进程事实绑定到进程内 memory。
- 账户页展示的当前并发是列表加载时的瞬时 in-flight 值，不是累计请求数。performance 模式下管理端和用户侧账户列表必须在列表响应中批量读取当前可见账户在 Redis state 中的并发槽；授权实例必须按来源账号 ID 读取同一个硬并发槽，不能按授权实例 ID 另算一份并发。没有占用或 Redis state 读取失败时当前并发返回 `0`；前端不得为账户当前并发额外请求快照或开启定时轮询。

### 页面数据 revision 与投影缓存

以下是 `PLAN-0115` 待实现目标，不表示当前 performance 模式已经提供统一页面 confirm：

- `redis-state` 保存稳定有限的数据域 revision、epoch、运行态字段投影和变更日志水位；`redis-cache` 保存静态实体、规范化查询结果、默认页面和统计响应投影；`redis-queue` 承接持久变更 outbox 发布任务。三类 Redis 不得混用职责。
- 统一页面变更确认接口只读 Redis state，业务数据库查询次数必须为 0。Redis state 不可用时返回轻量 503 和 `Retry-After`，禁止回源 PostgreSQL 承接全部页面轮询。
- PostgreSQL 持久变更在原业务事务内追加有界 outbox，提交后至少一次发布到 Redis；运行态值和对应 revision 使用同一 state Redis Lua 原子更新。不同 Redis 实例之间不假设原子事务。
- Redis 健康但具体投影 key miss 时，只允许对当前实体 ID、当前页或已有预聚合窗口执行有界回填，并使用 singleflight、短租约分布式锁、并发上限和 TTL 抖动防止击穿。
- 最近 7 天登录用户的首页默认窗口和“我的 AI 账户”默认第一页由 worker 分批预热；不预热任意筛选、任意日志 grep 或全量历史明细。
- Node / Go performance 共存期必须共享 revision token、事件 schema、field mask 和测试夹具；Go 不实现 standalone / memory adapter。

### Redis 运行态一致性分级

Redis runtime state 按一致性要求分三类处理：

- 硬约束状态：账号并发槽、系统 API 限流、验证码发放限频等不能突破上限的状态，必须使用 Lua / Redis 原生命令 / 封装后的原子操作，失败时按保护性拒绝或快速失败处理。
- 短 TTL 调度状态：上游桶避让、IP 级账号回避、IP 错误熔断、Codex turn retry、AI 账户运行态探针状态。这类状态允许秒级短暂不一致和重复写入，使用 TTL、时间窗口、generation、成功信号清理和后台探针收敛，不在用户请求路径等待分布式锁。
- 可丢弃缓存：列表快照、只读 options、runtime cache version 等可重建数据，使用 `SharedJsonCache` 或本机 L1，失效慢一点只影响短期展示或调度偏好，不能承载账务、授权、使用记录或审计事实。

用户业务请求次数限制是“速度优先的软协调配额”，不归入上面的硬约束状态：

- 每个 Node server 在请求路径同步更新本机有界计数，不读取或等待 Redis；Redis 每秒后台批量协调，允许约 1～2 秒跨节点短暂超额。
- Redis 哈希按实例保存累计值并维护 `__total`，Lua 只累加当前实例差值，不扫描 `HVALS`；单批最多 1,024 个脏桶，失败使用 3 秒超时和最长 30 秒指数退避。
- Redis 故障时继续本机限制，不把基础设施故障放大为全部网关请求失败；server 退出前最多 3 秒尝试排空脏计数。
- PostgreSQL `system_settings` 若尚未补齐四个新全局设置行，Node reader 临时按 `0`（无限）兼容并在后续 PATCH 时用现有 upsert 持久化。本次不修改 Go catalog 或 Go 运行时契约。

短 TTL 调度状态禁止把“避免丢一个计数”作为加分布式锁的理由。高并发下丢失少量样本的代价低于请求路径等待锁、锁残留、跨节点排队和恢复探针被误限制的代价；状态升级必须由时间窗口、最小观察期、探针结果和成功信号共同决定。

### AI 账户运行态探针

AI 账户运行态探针在 performance 模式下必须按多节点设计运行：

- `failure_observed`、`local_suppressed`、`runtime_degraded`、`precheck_pending` 这类短 TTL 调度态可以存放在 Redis runtime state，但不能作为账务、授权、审计或账号健康持久事实。
- 状态事件只提交 probe intent；探针调度器负责去重、本机预算、jitter、generation 和 due 索引。
- 请求调度链路允许短暂不一致，优先使用 server 进程内短 TTL 近端缓存读取 Redis 探针状态，避免高并发下每次请求按候选账号数量访问 Redis。
- due 索引必须跨节点可见，使用 Redis sorted set 或等价 Redis runtime state 结构保存 `runtimeKey -> dueAt`。任意 server 节点都可以 sweep due 任务；重复执行由 generation 条件写入和条件删除收敛。
- 不使用 Redis 分布式锁、分布式全局预算锁、provider 锁、proxy 锁或 baseUrl 锁限制恢复探针预算；预算只做本机保护和 jitter，避免 Redis 锁残留导致恢复并发被误限制。
- 探针结果回写和探针成功清理前必须校验 generation，避免旧探针覆盖或误删真实成功、手动恢复或后续状态转换产生的新状态。
- Redis 探针状态只保存非敏感运行态元数据和 due 信息，不保存账号凭据、API Key、OAuth token、代理密码、失败请求 payload、失败请求 model 或 endpoint；执行探针前通过 DB service 重载账号凭据。
- 运行态恢复探针和持久健康 / 冷却探针严格使用重载账户的 `health_check_model`；缺失或非法时记录配置异常，不从个人默认、系统默认或支持模型首项兜底。
- server 进程负责 sweep Redis due 索引并执行运行态恢复探针；worker 和 DB service 不执行该类短 TTL 运行态探针。持久冷却复测仍由 ops-worker 负责。
- `runtime_recovery_probe` 只表示本地 / Redis 运行态恢复探针；持久 `temporary_unavailable / rate_limited` 冷却复测继续使用 `cooldown_retest`。两类探针都不写账号质量分钟样本，不保存完整请求 / 响应正文。
- Redis state 不可用时，高性能模式不能静默退回进程内 memory；应记录基础设施错误并保守跳过探针或快速失败。

## 队列与消费并发

现有队列机制不推翻，只调整 PostgreSQL 模式下的 drain 策略。

| 队列 / command | SQLite standalone | PostgreSQL performance |
| --- | --- | --- |
| DB service 业务写 | 业务库单 owner 串行 | 保持 typed operation，可并发执行，按事务和同 key 顺序约束 |
| usage records | ingest 内按 shard / 批次串行 | `redis_stream` 模式先写入 Redis Stream `juhe-ai:queue:usage-records`，多个 usage worker 通过同一 consumer group 消费，落库成功后 ack；Redis 入队失败时写入 release 外的本机 durable spool，保留稳定 ID 等待 primary Usage worker 重放，禁止回退 IPC / 内存队列 |
| audit / operation / public logs | ingest owner 串行 | performance 模式先写入对应 Redis Stream，再由多个 log worker 在同一 consumer group 内分摊，落库成功后 ack；旁路异常不能回滚已经成功的客户端或管理业务结果 |
| stats aggregation | stats writer 串行短事务 | 可按作用域 / 分区并行读取，写入仍按窗口事务控制，避免同一 summary key 并发 upsert 放大冲突 |
| record maintenance | 低优先级串行小批 | 低优先级并发小批，受全局并发和连接池限制 |

消费规则：

- `JUHE_AI_DB_WRITE_MAX_CONCURRENCY=100` 是后台写入队列总并发上限，不等于 PostgreSQL 连接池必须开到 100。
- 实际执行受 `JUHE_AI_DB_POOL_MAX`、PgBouncer 池大小、命令优先级、同资源互斥和队列容量共同限制。
- Redis Streams 只提供 at-least-once 投递，不提供 exactly-once；消费端写库必须保持幂等，失败消息留在 pending，超过 `JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS` 后由消费者重新 claim。
- 可靠队列入队固定不发送 `MAXLEN`，避免消费滞后时 Redis 近似裁剪未落库消息；消费成功后必须 `XACK` 并 `XDEL` 已确认条目，stream length 只反映未清理或未确认 backlog。容量风险通过 stream length、pending 数量、consumer idle、落库失败次数、积压告警和人工扩容 / 清理处理，不能通过静默裁剪处理。压测报告中判断本轮是否制造积压时必须使用当前测试窗口的 positive pending / backlog delta，历史遗留 pending / lag 只能作为单独清理项记录，不能直接判定当前压测失败。
- 入队后立即调度 drain，不再等待固定 SQLite flush 周期；但允许 0 到 10ms 的微批窗口合并当前事件循环内已经排队的同类写入。
- 同一个业务资源的状态覆盖类 command 仍可合并；事实明细 append-only 不做 last-write-wins。
- 失败重试必须指数退避并有最大重试 / dead-letter 指标，不能无限占用并发槽。
- 当队列积压超过阈值时，网关只允许继续投递关键使用记录和审计终态；低优先级清理、监控采样和表监控可以跳过本轮。

## DB service 与 worker 角色

PostgreSQL 模式下保留 DB service，理由不是规避 SQLite 同步阻塞，而是继续保持系统管理 API、网关热路径、repository 和数据库连接池的隔离：

- server 进程仍不直接导入管理路由和 repository。
- DB service 承接系统管理 API、登录态校验、网关关键读写和业务 typed operation。
- standalone 常驻后台进程保持 ingest-worker、stats-worker、ops-worker 三类；performance 拆为可配置的 usage-worker、log-worker，以及单例 stats-worker、ops-worker，写入 PostgreSQL 时按 typed operation、consumer group 和连接池背压并发消费。
- 高性能模式下 ops-worker 承接 API Key / 账户时间计划同步、资源授权过期扫描、过期逻辑删除账户清理、账户激活检查、始终存在的周期健康检测、账号冷却复测、账户内 API Key 检查 / 冷却复测、代理延迟刷新和 OpenAI OAuth access token 自动刷新；不存在 `health_check_enabled` 候选条件。这些任务的候选读取和状态写回必须走 PG async repository / DB service 分支。
- 高性能模式下 primary usage-worker 注册 `data-retention-cleanup` 并处理 record-maintenance，primary log-worker 只注册审计、操作和公开接口日志保留。PG 分支按系统设置投递 `usage_records_cleanup`，并按原始审计固定保全策略投递 `audit_retained_data_cleanup`；其他保留入口继续按各自 retention 清理。底层单机数据保留清理服务在 PG 下保持 fail-fast，避免回落 SQLite 清理链路。
- 运行日志索引、cursor、facet 与索引保留由唯一 `juhe-ai-go-sidecar` 内的 F1 完整负责，不再由任何 Node worker 注册或维护。
- ops-worker 的账号健康检测和冷却复测执行队列仍是本地短窗口 retry queue，只保存 accountId 等小对象；候选、取消、状态和结果事实以 PostgreSQL 为准。没有真实积压、重启恢复延迟或多 worker 抢占证据前，不把该执行缓冲强行迁入 Redis Streams。
- 人工账户测试仍是独立单账户诊断任务：每次使用独立 `testSessionId`，A/B 会话互不阻塞，不建立用户级全局锁，也不提供多账户批量测试。测试结果只写任务、使用记录和审计，不修改账户、授权实例、Key、额度快照、健康或 Redis 调度运行态。
- OpenAI OAuth access token 自动刷新已恢复 PG 调度；OAuth token、refresh token 和代理 URL 不进入 Redis shared cache。远端 smoke 使用测试替身 token endpoint 验证 PG 候选、写回、任意远端失败只诊断 / 退避、来源匹配恢复和错误脱敏，真实上游 refresh token 仍按真实账号和生产网络单独验证。代理延迟刷新已恢复 PG 调度，但代理 URL 只在探测进程内即时使用，不作为共享缓存内容。
- 已退役的 `metrics-worker`、`snapshot-worker`、`probe-worker` 和 `maintenance-worker` 不再作为独立 worker role 出现在调度分支中。

## 事务与一致性

- 单个业务写操作必须在一个 PostgreSQL 事务内完成。
- 跨事实域强一致需求应优先收敛到同一个 PostgreSQL 事务；不再按 SQLite 跨库短事务拆解。
- 对 usage 明细、审计、日志、统计缓存这类异步事实链路，仍保持“事实先落库，统计后聚合”的最终一致模型。
- 统计结果和统计游标必须同事务提交。
- Redis cache / Redis state 只承接可重建缓存和短 TTL 运行态；Redis queue 承接 usage、audit、operation log、public API log 和 record maintenance 等未落库消息，未 ACK 前不能当作可丢缓存处理。`redis-queue` 必须使用 `noeviction`、持久化和 pending / lag 监控；事实最终以 PostgreSQL 落库为准。普通运行日志只追加到角色 JSONL 文件，由唯一 Go sidecar 内 F1 按持久 cursor 批量索引，不进入 Redis queue。

### 使用记录批量落库锁顺序

高性能模式下，多个 usage worker 会并行消费同一个 Redis Stream consumer group，并且只有 PostgreSQL 事务提交后才会确认消息。`usage_records` 的唯一键保证重复投递幂等，但不能替代锁顺序：若事务先写 usage 分区的唯一索引、再按输入顺序更新多个 `juhe_business.accounts` 行，两个批次就可能分别持有 usage 索引和账户行锁，形成 `40P01` 循环等待。

PostgreSQL 使用记录批事务必须遵守以下边界：

- 在事务外完成批次归属、价格和账户副作用的汇总，不持有数据库锁。
- 事务开始后，先对所有将写入副作用的未删除账户执行 `ORDER BY id FOR NO KEY UPDATE`，由数据库按唯一稳定顺序获取行锁。
- 账户行锁持有后，才写入 `juhe_usage.usage_records` 及其分区元数据，最后执行 `last_used_at` 和健康成功信号的条件更新。
- 事务失败时整体回滚；Redis Stream 消息不确认，保持 pending 以便按既有至少一次语义重试。不得以降低 usage worker 数量、关闭幂等写入或删除历史记录规避竞争。

该顺序仅约束使用记录批写事务，不要求把所有账户写入路径串行化；其他账户变更仍须避免在同一事务中先持有其他互斥资源、再反向等待 usage 写入。

## PostgreSQL 调优基线

具体数值按 `<测试主机IP>` 和生产机器 CPU / 内存 / 磁盘测试后写入部署文档。默认基线：

- 应用连接 PgBouncer，不直接把每个 Node 进程的连接池打到 PostgreSQL。
- 应用通过 `storage/postgres-client.ts` 创建连接，`application_name` 必须区分 `server`、`db-service`、`ingest-worker`、`stats-worker` 和 `ops-worker`，便于生产从 `pg_stat_activity` 定位慢源。
- 默认启用 `JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS`、`JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS` 和 `JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS`，避免后台重统计或维护 SQL 无限占用连接；超时失败由 worker 记录并等待下一轮重试。
- PostgreSQL `max_connections` 按 PgBouncer 后端池设置，不为每个 worker 并发开同等连接。
- `shared_buffers` 初始按机器内存 25% 估算，`effective_cache_size` 按 50% 到 75% 估算。
- `work_mem` 保守设置，避免并发 100 下排序 / hash 聚合放大内存。
- 打开 `pg_stat_statements`，记录慢 SQL、平均耗时、调用次数和 rows。
- usage 热表分区开启 aggressive autovacuum，过期数据优先 drop partition。
- 设置 `log_min_duration_statement`，生产初期建议 500ms 到 1000ms。
- 定期执行 `ANALYZE`，大批导入或离线迁移后必须刷新统计信息。

## Redis 调优基线

- `redis-cache`、`redis-state` 和 `redis-queue` 生产建议拆成三个实例或三个容器，因为 cache 可以淘汰，runtime state 与可靠队列都不能被 LRU 随机淘汰，队列还需要单独按 backlog 扩容。
- `redis-cache` 设置 `maxmemory` 和淘汰策略，命中率低于阈值时优先检查 key 设计，不直接加内存。
- `redis-state` 使用 `noeviction`，所有写入必须带 TTL；写失败时调用方按降级策略处理。
- `redis-queue` 使用 `noeviction + AOF everysec`，Redis Streams 不配置自动裁剪；队列内存和磁盘水位异常时应扩容或排查消费者，而不是丢弃未消费消息。
- 硬约束运行态使用 Lua 或 Redis 原生命令收口，不把 `GET -> 本地判断 -> SET` 暴露给并发调用方；短 TTL 调度状态可以直接读写并接受短暂覆盖，但必须有 TTL、成功清理、后台恢复或 generation 收敛机制。
- 监控 `used_memory`、`evicted_keys`、`expired_keys`、`blocked_clients`、`instantaneous_ops_per_sec`、命中率和慢命令。

## 数据切换边界

从 standalone 切到 performance 时，项目只提供当前 PostgreSQL schema 初始化、默认 seed 和当前运行路径回归；不提供 SQLite -> PostgreSQL 数据迁移脚本、旧 PostgreSQL 结构迁移脚本、启动期自动迁移、双读双写或旧 schema 兼容。历史数据是否保留、如何导入、如何对齐旧结构，由上线窗口在代码库外单独处理，并最终落到当前 schema。

切换后必须执行登录、管理 CRUD、网关请求、usage 写入、统计聚合、缓存失效、Redis 重启降级和备份恢复验证；验证不通过时按当前 schema 和当前代码修复，不在运行路径增加旧结构兼容分支。

## 验证要求

高性能模式落地必须新增这些验证：

| 类型 | 验证项 | 预期 |
| --- | --- | --- |
| 配置 | standalone 默认启动 | 不要求 PostgreSQL / Redis，行为与当前一致 |
| 配置 | performance 缺少 PostgreSQL / Redis | 启动快速失败，错误可读 |
| SQL dialect | SQLite / PostgreSQL 同一 repository 语义 | CRUD、分页、唯一约束、upsert、事务回滚一致；provider 只读 repository、登录 / 会话最小 repository、系统账户管理读写、分组管理读写、API Key 管理读写与网关 Key 校验入口、授权列表 / options / usage 详情读取、公共设置读取、系统 API 限流设置读取和系统设置管理读写已通过双 driver 回归 |
| Redis cache | memory / redis driver 一致 | TTL、失效、序列化、domain clear 行为一致 |
| Redis state | 硬计数原子化、短 TTL 调度状态无锁 | 账号并发槽和限流不突破限制；网关调度、探针、上游桶、IP 回避 / 熔断和 Codex turn retry 不在请求路径等待 Redis 分布式锁 |
| 队列 | DB 写并发上限 100，Redis Streams 使用记录缓冲 | 入队即触发消费，连接池不爆，pending 可重投，低优先级不饿死高优先级 |
| usage | 高并发写入和统计聚合 | 明细无丢失，统计游标与结果同事务推进 |
| 网关 | API Key 校验、调度、使用记录、审计 | 主链路成功，缓存失效后能读到新事实 |
| 运维 | PostgreSQL / Redis 重启 | 可读错误、重连、短 TTL 状态丢失可恢复 |
| 部署 | Docker host `<测试主机IP>` | compose 启动、健康检查、备份、恢复和日志路径明确 |

## 落地顺序

1. 新增配置模型、运行模式校验和文档。已完成基础配置、`.env` 示例和 fail-fast 校验。
2. 抽象 `DatabaseClient` / `SqlDialect`，保持 SQLite 默认路径不变，并接入 PostgreSQL 初始化脚本、schema 映射、默认种子和主要 repository async adapter。
3. 完成核心管理链路和网关链路的 PostgreSQL adapter：登录 / 会话、系统账户、系统团队、分组、API Key、授权、AI 账户、代理、设置、模型目录、网关 Key 校验、候选账号读取、使用记录、审计 / 操作 / 公开接口 / 运行日志和主要统计读写已纳入 smoke 或 readiness。
4. 完成 Redis cache / runtime state / Redis Streams 基础 driver 与关键调用点：共享缓存、硬约束运行态计数、短 TTL 调度状态、验证码状态、网关缓存失效版本、使用记录、审计日志、操作日志、公开接口日志和维护队列均有回归覆盖；普通运行日志由 JSONL 文件 spool / Go F1 cursor 回归覆盖，不属于 Redis Streams。
5. 完成高性能模式测试栈、schema 初始化、远端 PG/Redis smoke、readiness、压测、故障演练和备份恢复验证；生产上线前仍必须在目标机器执行本机安装、配置、数据导出 / 导入演练和维护窗口回归。
6. 明确数据切换边界：代码库不提供 SQLite -> PostgreSQL 自动迁移、旧 schema 兼容、双读双写或启动期自动迁移；历史数据保留和导入只在上线窗口按当前 schema 离线处理。
7. 保留 fail-fast 边界：低频管理入口、复杂筛选、长周期运维任务或未验证路径如果尚未覆盖 PostgreSQL，必须显式失败并补专项验证，不能在 performance 模式回退 SQLite。

## 风险

- PostgreSQL 解决 SQLite 文件级写锁，但不解决所有并发问题；热点行、唯一索引冲突、长事务和连接池耗尽仍会拖慢系统。
- Redis cache 与 Redis runtime state 如果混用同一实例和 LRU 淘汰，可能导致限流、并发占用或调度屏蔽被意外淘汰。生产建议拆成两个实例。
- 并发 100 如果不经过 PgBouncer 和队列背压，可能把数据库连接耗尽，反而比 SQLite 单写者更不稳定。
- Redis 8 授权需要生产前确认；如果业务分发方式不接受当前 Redis 授权，必须单独决策替代实现。
- SQLite 与 PostgreSQL 的 SQL 差异会触及大量 repository；必须先建 dialect 测试矩阵，不能直接批量替换 SQL 字符串。
# AI 问答聊天存储补充

AI 问答高增长正文使用同一 PostgreSQL 集群内的独立 `juhe_chat` schema，不写入 `juhe_business`：

- `chat_conversations`、`chat_message_idempotency`、`chat_user_storage_windows` 为普通表。
- `chat_messages` 按 UTC `created_at` 建每日 range partition，并提前创建当天和下一天分区。
- 保留窗口为精确滚动 `7 × 24` 小时：完整过期日分区直接 drop，最老部分重叠分区按 `(expires_at, id)` 游标小批删除。
- 每用户最近 7 天默认 2 GiB 门禁只读取 `chat_user_storage_windows` 日桶，不允许 API 请求实时聚合消息明细。
- Redis 只保存可丢失的取消信号、短期 SSE 协调和运行门禁，不保存聊天正文、会话或模型上下文事实。
- 普通发布只执行 `juhe_chat` schema-only 同步，不重建 `juhe_business`、`juhe_dataset`、`juhe_usage`、`juhe_stats`、Redis 或其他非本次变更的数据。
