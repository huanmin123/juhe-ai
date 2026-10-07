# PostgreSQL 与 Redis 高性能模式设计

> 本文是 PostgreSQL/Redis（performance）模式的存储、运行态与部署设计契约；standalone SQLite 与 performance PostgreSQL+Redis 双模式均由 Go 三项目原生支持（`backend-go/projects/gateway/cmd/juhe-ai-gateway/runtime.go` 的模式推断与装配）。Node 时代拓扑（DB service、Node worker、Redis Streams 任务队列、PgBouncer、三个物理 Redis 实例、`JUHE_AI_ENV_FILE` 分层）已随迁移退役，不再作为现行叙述；现行部署 env 契约以 `docker/single-server/README.md` 与 `docs/deploy/部署指南.md` 为准。
> 数据库、缓存、运行态和队列的业务语义适配边界见 存储适配接口设计（Node 时代文档，已删除，git 历史可溯）。
> 统计准确性、读写资源隔离、Redis 清理和压测验收的细化规则见 [可靠统计与读写资源隔离设计](可靠统计与读写资源隔离设计.md)。
> 管理后台页面 revision、字段投影、统一确认、IndexedDB 和最近登录用户预热见 [页面数据缓存与增量更新设计](页面数据缓存与增量更新设计.md)。

## 背景

默认 standalone 模式通过 SQLite 多库拆分、usage shard、单写者 writer 和预聚合统计缓解了大部分轻量部署瓶颈。但随着用户量和网关请求量上升，瓶颈已经从“单个 SQL 慢”变成了几类系统性限制：

- SQLite 文件级写锁要求同一数据库文件只能有一个运行时 writer，高频写入只能通过串行 owner / shard 缓解。
- 进程内 LRU 缓存无法跨进程共享，进程重启后短 TTL 运行态全部丢失。
- usage、审计、运行日志索引、统计聚合等队列在 SQLite 模式下必须尽量让出写锁，不能按数据库实际并发能力扩展。
- 后续如果继续扩大用户数，需要事实存储、运行态缓存、连接池、队列背压和部署监控一起升级，而不是只替换 SQL 语法。

因此新增两套明确运行模式：

| 模式 | 数据库 | 缓存 / 运行态 | 默认用途 |
| --- | --- | --- | --- |
| `standalone` | SQLite 多库 + usage shard | 进程内 LRU / Map | 默认单机、轻量部署、本地开发 |
| `performance` | PostgreSQL | Redis | 高用户量、高请求量、Docker / 生产部署 |

默认仍是 `standalone`；显式配置 `performance`，或存在 PostgreSQL / Redis URL 时自动启用 PostgreSQL 和 Redis。

## 目标

- 保持默认单机模式行为不变，现有用户不配置 PostgreSQL / Redis 也能继续运行。
- 高性能模式使用 PostgreSQL 承接所有事实数据、预聚合统计、日志索引和运行可恢复数据。
- 高性能模式使用 Redis 承接跨进程短 TTL 缓存、调度运行态、限流、验证码 / 登录失败窗口、缓存版本和必要的原子计数。
- 数据访问方只依赖统一的 store / cache / runtime state 接口边界，不在业务代码里散落 `if sqlite / if postgres / if redis`。
- 不在代码库里做 SQLite 与 PostgreSQL 数据迁移、旧 PostgreSQL 结构迁移、双读、双写、自动迁移或旧结构兼容；历史数据处理由上线窗口在代码库外单独完成，应用只面向当前 schema。

## 不做什么

- 不把 PostgreSQL 高性能模式做成默认依赖。
- 不在首期引入 Kafka、RabbitMQ、BullMQ、ClickHouse 或其他外部队列 / OLAP。
- 不在首期实现多节点自动故障转移、跨地域复制或自动用户迁移；多节点路由仍按 [分布式部署与用户分片设计](分布式部署与用户分片设计.md) 单独推进。
- 不把 Redis 作为账务、权限、授权、账号健康、审计或使用记录的持久事实库；但在高性能模式下，跨进程短 TTL 缓存、运行态和原子计数的当前事实源必须是 Redis，不能回退到进程内 memory 后继续声明高性能模式可用。持久事实统一落 PostgreSQL，不引入外部队列。
- 不为了迁移保留运行时旧 schema 兼容分支；历史数据处理只允许离线脚本或重建流程。

## 配置模型

模式由 `JUHE_AI_RUNTIME_MODE` 显式选择；未显式配置时按 `JUHE_AI_POSTGRES_URL` / `JUHE_AI_REDIS_CACHE_URL` / `JUHE_AI_REDIS_STATE_URL` 任一存在自动推断为 `performance`，否则为 `standalone`（`backend-go/projects/gateway/cmd/juhe-ai-gateway/runtime.go` 的 `loadRuntimeConfig`）。三个 driver（`JUHE_AI_DATABASE_DRIVER`、`JUHE_AI_CACHE_DRIVER`、`JUHE_AI_RUNTIME_STATE_DRIVER`）未配置时按模式取默认（performance 为 postgres / redis / redis，standalone 为 sqlite / memory / memory），显式配置优先，非法值快速失败。

standalone 默认：

```dotenv
JUHE_AI_RUNTIME_MODE=standalone
JUHE_AI_DATABASE_DRIVER=sqlite
JUHE_AI_CACHE_DRIVER=memory
JUHE_AI_RUNTIME_STATE_DRIVER=memory
```

performance 高性能模式：

```dotenv
JUHE_AI_RUNTIME_MODE=performance
JUHE_AI_DATABASE_DRIVER=postgres
JUHE_AI_CACHE_DRIVER=redis
JUHE_AI_RUNTIME_STATE_DRIVER=redis
JUHE_AI_POSTGRES_URL=postgres://juhe_ai:<密码URL编码>@<host>:5432/juhe_ai
JUHE_AI_REDIS_CACHE_URL=redis://:<密码URL编码>@<host>:6379/0
JUHE_AI_REDIS_STATE_URL=redis://:<密码URL编码>@<host>:6379/1
JUHE_AI_REDIS_NAMESPACE=prod
```

规则：

- Go 不读取 `JUHE_AI_QUEUE_DRIVER` / `JUHE_AI_REDIS_QUEUE_URL`（Node 时代死配置，`runtime.go` 中有显式注释；`docker/single-server/README.md`），Go 三项目没有 Redis 任务队列，无需第三个队列 Redis 实例。
- `JUHE_AI_DB_POOL_MAX`、`JUHE_AI_DB_WRITE_MAX_CONCURRENCY`、`JUHE_AI_POSTGRES_*_TIMEOUT_MS`、`JUHE_AI_REDIS_STREAM_*`、`JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT` 等 Node 时代 env 在 Go 中不存在；现行必需 env 契约以 `docker/single-server/README.md` 的".env 契约"节为准。
- 不再使用 `backend/.env.performance` + `JUHE_AI_ENV_FILE` 覆盖加载；模式未显式配置时按上述 URL 存在性自动推断。
- `JUHE_AI_DATABASE_DRIVER=postgres` 时 SQLite 路径类 env 不再表达存储语义，仅按 datadir 约定作为 spool 等本地文件目录的派生根。
- `JUHE_AI_CACHE_DRIVER=redis` 只表示可丢弃缓存；需要硬并发槽、限流计数或短 TTL 调度运行态时必须走 `JUHE_AI_RUNTIME_STATE_DRIVER`。
- `JUHE_AI_REDIS_NAMESPACE` 是 Redis key 的部署隔离前缀，redis 驱动下必填非空（现行生产配 `prod`）；压测、回归和多套环境共用 Redis 实例时必须使用不同 namespace。
- 运行态与 J3b 电路运行态必须分离键空间：`JUHE_AI_J3B_CIRCUIT_REDIS_URL` 不得与 `JUHE_AI_REDIS_STATE_URL` 相同（gateway 启动强校验），现行生产以同实例不同 DB 承载。
- 生产环境不使用 `latest` 镜像；PostgreSQL 和 Redis 镜像必须固定 major / patch 或 digest。
- 高性能模式不能为了吞吐自行降低原始审计保留语义，必须和 standalone 使用相同的显式部署配置。成功正文默认全量保留（采样率默认 `1`，可调），最近 `1` 小时热保留窗口默认开启；失败、异常、中断和重试后成功链路仍全量进入审计。程序不因容量压力自动降采样，容量和吞吐问题通过 PG 小批次写入、热窗口清理、payload 摘要 / 压缩 / 去重治理；口径详见 [审计日志保全策略设计](审计日志保全策略设计.md)。

## 部署边界

现行唯一生产形态是国内单机 Docker（`docker/single-server/`，go-only）：Caddy 入口 + gateway / jobs 常驻容器 + maintenance 一次性容器 + postgres / redis 两个中间件容器；部署、初始化与 env 契约以 `docker/single-server/README.md` 和 `docs/deploy/部署指南.md` 为准。

| 服务 | 职责 | 说明 |
| --- | --- | --- |
| `postgres` | 主事实库 | `postgres:18-alpine`，单库 `juhe_ai` 多 schema，应用直连、无 PgBouncer；compose 预置 `shared_buffers=2GB`、`effective_cache_size=4GB`。 |
| `redis` | 缓存 + 运行态 + J3b 电路 | 单 `redis:7.4-alpine` 容器，`appendonly yes`、`maxmemory-policy=noeviction`；cache / state / J3b circuit 以 DB 编号分离，state 与 J3b 电路键空间强制分离（见配置模型）。 |
| `gateway` | 管理面、公开面与 `/v1` 网关链 | 进程内装配 F3 审计、F4 usage spool 等 owner 包。 |
| `jobs` | 后台任务族 | jobregistry 注册的统计聚合、保留清理、探针、OAuth 刷新等任务族。 |
| `maintenance` | schema / seed 一次性 CLI | `juhe-ai-maintenance --ensure-schema / --seed` 幂等执行。 |
| `caddy` | HTTPS 入口 | ACME 自动续期，反代管理 SPA 与 `/v1`。 |

版本参考：PostgreSQL 18 是当前正式版本线，PostgreSQL 19 仍处 beta 阶段，不作为生产默认；Redis 现行镜像为 `redis:7.4-alpine`，后续升级大版本前需单独确认授权接受度与兼容性，不在本设计里静默切换。

## PostgreSQL 存储形态

PostgreSQL 模式不再模拟多个 SQLite 文件，而是把当前事实域映射为 schema：

| PostgreSQL schema | 当前 SQLite 域 | 主要表 |
| --- | --- | --- |
| `juhe_business` | 业务库 | 系统账户、会话、供应商、账号、分组、API Key、授权、代理、设置、公告 |
| `juhe_dataset` | 数据集目录库 | 审计元数据、操作日志、公开接口日志、模型检测、记录清理目标；运行日志索引表由 jobs 的 F1 索引器唯一写入 |
| `juhe_usage` | 使用记录目录库 + usage shard | `usage_records`、使用记录列表索引、账号 / API Key scope catalog |
| `juhe_stats` | 统计结果库 | 用量桶、额度窗口、范围窗口、排行、账号质量、系统监控、表监控、`stats_job_state` |
| `juhe_codex_context` | Responses 桥接状态索引 | Responses session / response / compact 索引 |

表名保留当前语义，schema 归属由各进程 store 包固化，不把 schema 名散落进业务逻辑。
表监控在 PostgreSQL 模式下按这 5 个 schema 采样 relation size、估算行数和 1 小时 / 24 小时增长，结果统一写入 `juhe_stats.database_storage_snapshots` 与 `juhe_stats.table_storage_snapshots`，不回读 SQLite 文件路径。

schema 权威是 maintenance 的 Go DDL（`backend-go/projects/maintenance/internal/schema/pg_schema*.go`），由 `juhe-ai-maintenance --ensure-schema` 幂等执行（Node 时代的 `postgres-schema.ts` / `postgres:init-schema` 已随迁移删除）。生产单库除上表 5 个 schema 外，另有 `juhe_chat`（同由 `--ensure-schema` 预置）与 `juhe_jobs`、`juhe_j3b`（需手工预置 schema 后由 maintenance `--apply-*` 建表），以 `docker/single-server/README.md` 的拓扑节为准。

### usage_records 当前形态

高性能模式不继续复制 SQLite usage catalog 明细模型，`juhe_usage.usage_records` 当前按 `created_at` 建日分区父表，主键为 `(created_at, id)`。写入前会按记录时间确保对应日分区存在，列表、详情、统计游标和清理都必须带上时间窗口或由 usage id 推导分区窗口，不能对分区父表做无界扫描。

- 分区键：`created_at` 日 range partition；过期在线数据在统计安全游标追平后按分区事务内 `DETACH / DROP`，不保留同库冷归档副本或归档 manifest。
- 热查询索引白名单以用户维度开头：`system_account_id + created_at + id`、`system_account_id + api_key_id + created_at + id`、`system_account_id + account_id + created_at + id`、`system_account_id + trace_id COLLATE "C" + created_at + id`。
- 管理员使用记录页未限定用户时只能读取配置的 `usageStatsTimezone`（显式 IANA）当天、`created_at DESC, id DESC` 的全用户列表，由当天分区和父表复合主键 `(created_at, id)` 承接；账户名称、结果、状态码、IP、分组、模型、trace ID、请求来源、手工日期或非默认排序必须先选择系统账户。其他管理员大表页面仍按各自功能边界先限定用户和日期窗口；独立 `trace_id`、独立 `request_id`、全局 `model/path/status/client_ip` 不能作为大表默认索引。
- PostgreSQL 前缀筛选必须显式使用稳定 collation。`trace_id`、`client_ip`、API Key 名称和 AI 性能账号选项名称等文本前缀查询使用 `COLLATE "C"`、二进制上界和对应 C collation 表达式索引；不能依赖 `prefix + '\uffff'`，也不能假设数据库默认 collation 的排序行为和 SQLite 一致。
- 清理策略：常规过期清理按统计安全游标追平后处理整日分区；已删除账号 / API Key 的关联明细小批次清理必须使用 `(created_at, id)` 命中分区，不能只按裸 `id` 删除。
- 统计游标：`stats.stats_job_state` 继续记录按分区 / shard 窗口推进的游标，统计写入和游标推进在同一 PostgreSQL 事务提交。

索引白名单与 collation 细节以 `backend-go/projects/maintenance/internal/schema/pg_schema_usage.go` 当前 DDL 为准。

### JSON 与时间

- SQLite JSON 字符串字段在 PostgreSQL 中按用途选择 `jsonb` 或 `text`。需要按字段筛选、局部更新或索引的配置字段使用 `jsonb`。
- 真正表示 PostgreSQL instant 的查询和持久化列优先使用 `timestamptz`；SQLite 以及经明确证明需要版本、CAS 或共享 schema 兼容的列，可以保存 canonical UTC RFC3339 text，但写入必须严格校验并拒绝无 offset。内部传输、比较和持久化继续使用 RFC3339 绝对时间，管理 API 输出 UTC `Z` 或明确数字 offset；Vue / 浏览器按用户本地时区展示。date-only 与业务日历字段另行明确，并使用显式 IANA timezone；不得把这些例外机械扩展为 schema-wide 数据库迁移。
- 金额和成本如果需要精确累加，优先使用 `numeric`；只作为展示缓存且已有浮点口径的字段可以保持 `double precision`，但统计总量字段必须固定类型并写清楚。
- `juhe_business.accounts` 使用 `health_check_model` 保存账户必填检查模型，不保留 `default_test_model` 或 `health_check_enabled`；`provider_default_health_check_models` 保存个人供应商默认，`provider_system_default_health_check_models` 保存管理员系统默认，协议档案保留内置默认。三层默认只初始化新账户。
- `juhe_business.provider_model_catalog` 和 `juhe_business.custom_provider_models` 复用 `supported_service_tiers_json`、`supported_reasoning_efforts_json` 和 `default_reasoning_effort`，并各用一个 `service_tier_prices_json` 保存非标准实际档位价格；标准价继续使用现有扁平字段，不新增价格表。支持请求覆盖的供应商账户凭据 JSON 可选保存现有 `service_tier_override`、`reasoning_effort_override`。字段值由所属供应商模型共同能力和目标 driver 校验，`ultra` 与 Responses Multi-agent Beta 不属于这两个账户覆盖字段。

### 约束与并发

- 业务唯一约束继续由数据库兜底，例如账号名称、API Key token hash、分组绑定、授权来源等。
- 管理端幂等仍保留 Redis / 内存防重复提交 + 数据库唯一约束双层保护。
- PostgreSQL 不存在 SQLite 文件级全局写锁，但仍存在行锁、索引页竞争、连接池耗尽、长事务和 autovacuum 压力；高性能模式不能把所有队列无限并发。
- System API 管理端 DB 在途请求默认保留保护阈值：standalone 默认 `64`，performance 默认 `256`，可按高性能部署压测继续调整。（Node 时代截面，现行以 Go gateway 管理面实现为准。）
- 同一账号等热点资源的高频并发写入仍会形成行锁排队；需要按资源维度串行、合并写入或把短 TTL 运行态拆到 Redis，不能因为切换 PostgreSQL 就把同一行无限并发写。
- AI 账户批量编辑必须在单个 PostgreSQL 事务内校验全部自有物理账户、字段白名单、同构模型条件和 `expectedVersions`，任一版本冲突或配置非法时整批回滚。事务提交后再失效缓存和投递必要的后台系统检查。

## SQL Dialect 与 Repository

Go 实现不再维护 Node 时代的 `DatabaseClient` / `SqlDialect` 双方言抽象、TS repository 迁移进度记录与 `test:*-driver` 回归矩阵（历史截面见 git 历史）。现行口径：

- SQL 方言以 PostgreSQL 为主，SQLite standalone 路径按同一业务契约双模实现。
- repository 层按 schema 包（`backend-go/projects/maintenance/internal/schema`）与各进程 store 包组织：gateway / jobs 各自的 `internal/...` store、repo 包持有各自事实域的 SQL。
- 细节以 `backend-go` 各 internal 包为准，本文不再逐路径登记迁移状态。

## Redis 缓存与运行态

缓存层拆成三类（Go 以各进程 internal 包的 cache / runtime state 组件承载，不再是 Node 的 TS 接口签名）：

- 进程本地缓存：进程内同步缓存，可存放不可序列化对象，例如 HTTP agent、临时近端只读结果和进程内 memoization；高性能模式下仍允许使用，但不能承载跨进程事实源。需要跨进程一致的缓存必须接入跨进程缓存 / Redis，进程本地缓存只能作为可丢弃本地 L1 或明确的进程内易失优化。
- 跨进程可丢弃缓存：`standalone` 使用 memory driver，`performance` 使用 Redis cache driver。
- 跨进程短 TTL 运行态：`standalone` 使用 memory driver，`performance` 使用 Redis state driver；硬并发槽、限流计数这类不能超卖的状态必须通过该层工具或封装后的专用原子操作访问。分布式锁只保留给明确需要单资源串行化且不可接受重复执行的基础设施保护，例如 OAuth access token 单账号刷新；网关调度、账号运行态探针、上游桶避让、IP 级账号回避、IP 错误熔断和 Codex turn retry 不允许在请求路径使用 Redis 分布式锁。

Redis key 统一命名：

```text
juhe-ai:{namespace}:{driver}:{cache-name}:v{version}:{scope}:{key}
```

规则：

- `{namespace}` 由 `JUHE_AI_REDIS_NAMESPACE` 决定；redis 驱动下必填非空，生产建议显式配置并保持稳定（键形状以 Go cache / runtime state 各实现为准）。
- 缓存失效优先递增 domain version，实现常量成本失效，不做全库 `SCAN + DEL`。
- 所有 Redis cache / state key 必须有 TTL；确需长期保留的运行态要先证明不是持久业务事实。
- Redis payload 使用 JSON，并带 `schemaVersion`；结构变化时递增 cache domain version。
- API Key 明文、OAuth token、代理密码、完整请求 / 响应 payload、审计正文和可能造成越权的权限中间结果不得进入通用 Redis cache。
- 调度运行态、并发占用、IP 级错误熔断、登录失败窗口、验证码挑战、会话亲和和 cache invalidation index 在 performance 模式下进入 Redis state。
- 账户页展示的当前并发是列表加载时的瞬时 in-flight 值，不是累计请求数。performance 模式下管理端和用户侧账户列表必须在列表响应中批量读取当前可见账户在 Redis state 中的并发槽；授权实例必须按来源账号 ID 读取同一个硬并发槽，不能按授权实例 ID 另算一份并发。没有占用或 Redis state 读取失败时当前并发返回 `0`；前端不得为账户当前并发额外请求快照或开启定时轮询。

### Redis 运行态一致性分级

Redis runtime state 按一致性要求分三类处理：

- 硬约束状态：账号并发槽、系统 API 限流、验证码发放限频等不能突破上限的状态，必须使用 Lua / Redis 原生命令 / 封装后的原子操作，失败时按保护性拒绝或快速失败处理。
- 短 TTL 调度状态：上游桶避让、IP 级账号回避、IP 错误熔断、Codex turn retry、AI 账户运行态探针状态。这类状态允许秒级短暂不一致和重复写入，使用 TTL、时间窗口、generation、成功信号清理和后台探针收敛，不在用户请求路径等待分布式锁。
- 可丢弃缓存：列表快照、只读 options、runtime cache version 等可重建数据，使用 `SharedJsonCache` 或本机 L1，失效慢一点只影响短期展示或调度偏好，不能承载账务、授权、使用记录或审计事实。

用户业务请求次数限制是“速度优先的软协调配额”，不归入上面的硬约束状态；现行机制、参数与降级行为以 [用户请求限制设计](用户请求限制设计.md) 为准。（原文中本机计数 + Redis 后台协调的 Node server 分工细节与“system_settings 未补齐时 Node reader 按 0 兼容”过渡注记已随迁移删除。）

短 TTL 调度状态禁止把“避免丢一个计数”作为加分布式锁的理由。高并发下丢失少量样本的代价低于请求路径等待锁、锁残留、跨节点排队和恢复探针被误限制的代价；状态升级必须由时间窗口、最小观察期、探针结果和成功信号共同决定。

### AI 账户运行态探针

AI 账户运行态探针在 performance 模式下的运行态必须跨进程可见（权威口径见 [AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)）：

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
- Redis state 不可用时，高性能模式不能静默退回进程内 memory；应记录基础设施错误并保守跳过探针或快速失败。

## 队列与消费并发

Go 三项目没有 Redis 任务队列（Node 时代的 Redis Streams consumer group 队列表已随迁移退役，见 git 历史）。异步链路按数据域收敛为进程内组件与文件交接：

- usage 链路：gateway 在 `/v1` 链内把用量记录原子写入文件 spool（`JUHE_AI_USAGE_SPOOL_DIRECTORY`，未配置时按 datadir 约定派生），jobs 的 `usagespooldrain` 扫描 spool 文件、经归一化校验后交接给 `usagewriter` 直接异步写分片 / 分区（`backend-go/projects/jobs/internal/usagespooldrain/drain.go`）。gateway 与 jobs 两侧 spool 目录必须解析到同一宿主机路径，否则用量记录永不到库且无报错（`docker/single-server/README.md`"运行时数据目录契约"）。
- 审计与操作日志：gateway 进程内 F3 / F4 owner 包直接写入，不经队列。
- 公开接口日志由 gateway 进程内组件写入；运行日志索引由 jobs 的 F1 索引器按文件 cursor 消费。
- 后台任务族由 `jobregistry` 统一注册，lease 围栏、单持有者、固定退避语义见 `backend-go/projects/jobs/internal/jobregistry/registry.go`；低优先级清理不得阻塞关键用量与审计链路。

统计聚合仍按作用域 / 分区并行读取、窗口事务写入控制，避免同一 summary key 并发 upsert 放大冲突；事实明细 append-only，不做 last-write-wins；失败重试必须指数退避并有上限，不能无限占用执行预算。

## Go 三进程分工

Node 时代的 DB service 与 server / worker 进程族已随迁移退役。现行分工为 Go 三进程：

- `gateway`（`backend-go/projects/gateway`）：管理面、公开面与 `/v1` 网关链主入口；进程内装配 F3 原始审计、F4 操作日志 owner 包和 usage 文件 spool 写入，owner lease 围栏下的审计 / 操作日志保留清理也在本进程执行。
- `jobs`（`backend-go/projects/jobs`）：后台任务族执行进程，任务条目以 `internal/jobregistry/registry.go` 注册表为准（统计聚合、数据保留清理、探针任务族、OpenAI OAuth token 刷新等），每个任务显式登记 Go 绑定状态、不允许静默跳过。J3a 已由 Go `juhe-ai-jobs` 独占执行、结果投影和 `proxy_profiles` 写回。
- `maintenance`（`backend-go/projects/maintenance`）：schema / seed 一次性 CLI，`--ensure-schema / --seed` 幂等执行。

人工账户测试是 gateway 进程内单持有者队列（共享 `backend-go-platform/accounttest`），仍保持独立单账户诊断语义：每次使用独立测试会话，A/B 互不阻塞，不建立用户级全局锁，也不提供多账户批量测试；测试结果只写任务、使用记录和审计，不修改账户配置或调度运行态。

## 事务与一致性

- 单个业务写操作必须在一个 PostgreSQL 事务内完成。
- 跨事实域强一致需求应优先收敛到同一个 PostgreSQL 事务；不再按 SQLite 跨库短事务拆解。
- 对 usage 明细、审计、日志、统计缓存这类异步事实链路，仍保持“事实先落库，统计后聚合”的最终一致模型。
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

## 数据切换边界

从 standalone 切到 performance 时，schema 初始化与默认 seed 由 `juhe-ai-maintenance --ensure-schema / --seed` 幂等执行；项目只提供当前 PostgreSQL schema 初始化、默认 seed 和当前运行路径回归，不提供 SQLite -> PostgreSQL 数据迁移脚本、旧 PostgreSQL 结构迁移脚本、启动期自动迁移、双读双写或旧 schema 兼容。历史数据是否保留、如何导入、如何对齐旧结构，由上线窗口在代码库外单独处理，并最终落到当前 schema。

切换后必须执行登录、管理 CRUD、网关请求、usage 写入、统计聚合、缓存失效、Redis 重启降级和备份恢复验证；验证不通过时按当前 schema 和当前代码修复，不在运行路径增加旧结构兼容分支。

## 验证要求

高性能模式相关改动至少覆盖以下验证（以 Go 三项目构建与相关包 `go test` 为载体）：

| 类型 | 验证项 | 预期 |
| --- | --- | --- |
| 构建 | Go 三项目构建 | `gateway` / `jobs` / `maintenance` 可编译，相关 internal 包测试通过 |
| 配置 | standalone 默认启动 | 不要求 PostgreSQL / Redis，行为与当前一致 |
| 配置 | performance 缺少 PostgreSQL / Redis | 启动快速失败，错误可读 |
| Redis cache / state | memory / redis 行为一致 | TTL、失效、序列化、domain clear 一致；硬计数原子化不突破限制，请求路径不等待 Redis 分布式锁 |
| usage | 高并发写入和统计聚合 | 明细无丢失，统计游标与结果同事务推进 |
| 网关 | API Key 校验、调度、使用记录、审计 | 主链路成功，缓存失效后能读到新事实 |
| 运维 | PostgreSQL / Redis 重启 | 可读错误、重连、短 TTL 状态丢失可恢复 |
| 部署 | 单机 Docker | compose 启动、健康检查、备份、恢复和日志路径明确 |

## 风险

- PostgreSQL 解决 SQLite 文件级写锁，但不解决所有并发问题；热点行、唯一索引冲突、长事务和连接池耗尽仍会拖慢系统。
- Redis 可淘汰缓存与运行态如果混用同一键空间和 LRU 淘汰，可能导致限流、并发占用或调度屏蔽被意外淘汰；现行单容器以 DB 编号与键空间强制分离（J3b circuit 不得与 state 同键空间）守住该边界。
- 大并发写入如果不经连接池与批量事务背压约束，可能把数据库连接耗尽，反而比 SQLite 单写者更不稳定。
- 现行镜像为 `redis:7.4-alpine`；后续升级 Redis 大版本前需单独确认授权接受度与兼容性，不得在本设计里静默切换。
# AI 问答聊天存储补充

AI 问答高增长正文使用同一 PostgreSQL 集群内的独立 `juhe_chat` schema，不写入 `juhe_business`：

- `chat_conversations`、`chat_message_idempotency`、`chat_user_storage_windows` 为普通表。
- `chat_messages` 按 UTC `created_at` 建每日 range partition，并提前创建当天和下一天分区。
- 保留窗口默认 `3` 天、可配置（`JUHE_AI_CHAT_RETENTION_DAYS`，`1..365`；gateway 读取与 jobs 清理双进程必须同值）；完整过期日分区直接 drop，最老部分重叠分区按 `(expires_at, id)` 游标小批删除。
- 每用户当前保留窗口内正文默认最多 `2 GiB`，门禁只读取 `chat_user_storage_windows` 日桶，不允许 API 请求实时聚合消息明细（口径见 [AI 问答设计](AI问答设计.md)）。
- Redis 只保存可丢失的取消信号、短期 SSE 协调和运行门禁，不保存聊天正文、会话或模型上下文事实。
- 普通发布只执行 `juhe_chat` schema-only 同步，不重建 `juhe_business`、`juhe_dataset`、`juhe_usage`、`juhe_stats`、Redis 或其他非本次变更的数据。
