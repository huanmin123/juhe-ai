# PLAN-0066 PostgreSQL 与 Redis 高性能模式

## 基本信息

- 编号：PLAN-0066
- 状态：进行中
- 创建时间：2026-06-25
- 需求来源：用户反馈用户量增长后 SQLite + 内存缓存已无法承载，要求新增默认单机模式和 PostgreSQL + Redis 高性能模式，并在 `192.168.1.203` Linux Docker 主机上搭建测试环境，后续生产复用同一套部署。
- 执行者：AI / 维护者
- 关联模块：后端 / 存储 / DB service / 后台 worker / 网关 / 缓存 / Redis / PostgreSQL / Docker 部署 / 迁移 / 文档 / 验证

## 需求目标

- 新增双运行模式：默认 `standalone` 使用 SQLite + 内存缓存，高性能 `performance` 使用 PostgreSQL + Redis。
- 保持调用方不关心底层缓存实现，统一通过缓存工具和运行态工具访问 memory / Redis。
- PostgreSQL 模式下取消 SQLite 文件级单写者限制，保留 typed command / 队列语义，但写入消费可以并发，最大消费并发为 `100`；使用记录队列先用 Redis Streams 承接高性能模式可恢复缓冲。
- 在 `192.168.1.203` Linux Docker 主机搭建 PostgreSQL、PgBouncer、Redis cache、Redis state 测试栈，生产环境复用该拓扑和调优基线。
- 提供停机离线 SQLite -> PostgreSQL 迁移和统计重建边界，不在运行时代码保留双读双写或旧 schema 兼容。

## 范围边界

### 本次包含

- [x] 新增 PostgreSQL 与 Redis 高性能模式长期设计文档。
- [x] 同步架构总览和 functions / plans 索引。
- [x] 新增运行模式配置与启动校验。
- [ ] 新增 `DatabaseClient`、`SqlDialect` 和 SQLite adapter，保证现有 SQLite 回归先通过。基础层已完成并接入 PostgreSQL 初始化路径；provider 只读 repository、登录 / 会话最小 repository、系统账户管理读写、分组管理读写、API Key 管理读写与网关 Key 校验入口、网关运行态最小候选账号读取、AI 账户最小创建与分组绑定 repository、AI 账户普通 owner 列表 / 详情 / options 读取、常规更新、异常状态恢复、逻辑删除、账户授权实例归还、授权列表个人归还、分组个人归还、账号支持模型 / 模型映射附属表 replace 写入、公共设置读取、系统 API 限流设置读取和系统设置管理读写已完成双 driver 适配，其他 repository 待逐个迁移。
- [ ] 新增 PostgreSQL schema、连接池、PgBouncer 配置和 repository adapter。已完成 PostgreSQL pool 骨架、schema 初始化脚本、默认种子写入、provider 只读 repository adapter、登录 / 会话最小 repository adapter、系统账户管理读写 adapter、分组管理读写 adapter、API Key 管理读写与网关 Key 校验 adapter、网关运行态最小候选账号读取 adapter、AI 账户最小创建与分组绑定 repository、AI 账户普通 owner 列表 / 详情 / options 读取、常规更新、异常状态恢复、逻辑删除、账户授权实例归还、授权列表个人归还、分组个人归还 adapter、账号支持模型 / 模型映射附属表 replace 写入 helper、公共设置读取 adapter、系统 API 限流设置读取 adapter、系统设置管理读写 adapter 和远端 PG 验证；其他 repository adapter 待完成。
- [ ] 新增 `AppCache` / `RuntimeStateStore` 抽象，完成 memory driver 和 Redis driver。已完成 `SharedJsonCache` 与 `RuntimeStateStore` 基础 driver，调用点仍需继续迁移。
- [ ] 调整 DB service / ingest / stats 写队列，在 PostgreSQL 模式下支持最大消费并发 `100`。已完成使用记录 Redis Streams 首批队列与 PG 并发 drain 骨架，其他队列待迁移。
- [x] 编写 Docker Compose 测试栈和服务器部署说明。
- [ ] 编写 SQLite -> PostgreSQL 停机迁移脚本与统计重建说明。
- [ ] 补充自动化回归、压测、Redis / PostgreSQL 故障演练和备份恢复验证。

### 本次不包含

- 不把 PostgreSQL / Redis 变成默认依赖。
- 不在常规运行路径做 SQLite 与 PostgreSQL 双读、双写或自动迁移。
- 不引入 Kafka、RabbitMQ、BullMQ、ClickHouse 或其他外部队列 / OLAP。
- 不实现多节点自动故障转移、跨地域复制或自动用户迁移。
- 不把 Redis 作为账务、权限、授权、使用记录或审计事实源；Redis Streams 只作为队列缓冲，数据库落库成功后才 ack。

## 关联文档

- 架构文档：[整体架构设计](../architecture/架构总览.md)
- 功能设计：[PostgreSQL 与 Redis 高性能模式设计](../functions/PostgreSQL与Redis高性能模式设计.md)
- 现有存储说明：[SQLite 存储说明](../functions/SQLite存储说明.md)
- 写队列治理：[SQLite 单写者写队列治理设计](../functions/SQLite单写者写队列治理设计.md)
- 数据集分片：[数据集库分片写入设计](../functions/数据集库分片写入设计.md)
- 统计拆分：[统计数据集与结果库拆分设计](../functions/统计数据集与结果库拆分设计.md)
- 后台任务：[后台任务使用说明](../architecture/backend/后台任务使用说明.md)
- 部署文档：[部署指南](../deploy/部署指南.md)
- 验证文档：[测试与验证说明](../develop/测试与验证说明.md)

## 方案概述

- 模式：`standalone = SQLite + memory`，`performance = PostgreSQL + Redis`，默认仍为 `standalone`。
- 数据库：通过 `DatabaseClient` 和 `SqlDialect` 抽象 SQLite / PostgreSQL 差异；PostgreSQL 按 `juhe_business`、`juhe_dataset`、`juhe_usage`、`juhe_stats`、`juhe_codex_context` schema 承接当前多库职责。
- 缓存：通过 `AppCache` 承接可丢弃缓存，通过 `RuntimeStateStore` 承接原子计数、锁、并发占用、限流和短 TTL 调度态。
- 队列：typed command 不推翻；SQLite 模式继续按 owner 串行，PostgreSQL 模式入队即触发 drain，默认最大消费并发 `100`，并受连接池和同 key 顺序限制；`redis_stream` 先覆盖使用记录队列，其他记录类队列按同一边界后续迁移。
- 部署：测试主机 `192.168.1.203` 使用 Docker Compose；生产同构，至少包含 PostgreSQL、PgBouncer、Redis cache、Redis state 和应用服务。

## 执行拆解

- [x] 阶段 0：设计文档落地，明确双模式、PG schema 映射、Redis 抽象、队列并发和部署边界。
- [x] 阶段 1：配置和启动校验，新增 runtime mode、database driver、cache driver、runtime state driver。
- [ ] 阶段 2：数据库抽象，SQLite 先跑在统一 `DatabaseClient` / `SqlDialect` 下，现有测试不变。基础层和回归已完成，provider 只读 repository、登录 / 会话最小 repository、系统账户管理读写、分组管理读写、API Key 管理读写与网关 Key 校验入口、网关运行态最小候选账号读取、AI 账户最小创建与分组绑定 repository、AI 账户普通 owner 列表 / 详情 / options 读取、常规更新、异常状态恢复、逻辑删除、账户授权实例归还、授权列表个人归还、分组个人归还、账号支持模型 / 模型映射附属表 replace 写入、公共设置读取、系统 API 限流设置读取和系统设置管理读写已接入，现有同步 repository 仍待逐个迁移。
- [ ] 阶段 3：PostgreSQL schema 和 repository adapter，完成业务库、dataset、usage、stats、codex context 的表结构映射。schema 初始化、默认种子、provider 只读 repository adapter、登录 / 会话最小 repository adapter、系统账户管理读写 adapter、分组管理读写 adapter、API Key 管理读写与网关 Key 校验 adapter、网关运行态最小候选账号读取 adapter、AI 账户最小创建与分组绑定 repository、AI 账户普通 owner 列表 / 详情 / options 读取、常规更新、异常状态恢复、逻辑删除、账户授权实例归还、授权列表个人归还、分组个人归还 adapter、账号支持模型 / 模型映射附属表 replace 写入 helper、公共设置读取 adapter、系统 API 限流设置读取 adapter 和系统设置管理读写 adapter 已完成，其他 repository adapter 待完成。
- [ ] 阶段 4：Redis cache / runtime state driver，迁移网关运行态、缓存失效、限流和会话亲和。基础 driver、登录失败窗口和账号并发槽已完成。
- [ ] 阶段 5：队列并发改造，PostgreSQL 模式下写队列入队即消费，最大并发 `100`，增加背压指标。使用记录 Redis Streams 首批队列和 PG 并发 drain 骨架已完成，其他队列待迁移。
- [x] 阶段 6：Docker 测试栈，在 `192.168.1.203` 搭建 PostgreSQL、PgBouncer、Redis cache、Redis state。中间件容器已 healthy，PG schema 与默认种子已在远端库验证。
- [ ] 阶段 7：离线迁移和统计重建，编写 SQLite -> PostgreSQL 脚本和导入后验证流程。
- [ ] 阶段 8：压测与故障演练，覆盖高并发写入、连接池耗尽、Redis 重启、PostgreSQL 重启、备份恢复。
- [ ] 阶段 9：同步部署和验证文档，形成生产可复用配置、备份、恢复和监控说明。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档验证 | 设计文档和索引 | 人工检查 docs 链接 | 新设计、计划、架构入口和索引互相链接正确 | 已通过 | 当前只落地文档 |
| 配置回归 | standalone 默认启动 | `pnpm --filter juhe-ai-backend test:runtime-config-env-override` | 行为与当前 SQLite + memory 一致 | 已通过 | 已覆盖默认 standalone 和 performance 配置读取 |
| 配置边界 | performance 缺少依赖 | runtime config regression / 启动校验 | 快速失败，错误可读 | 已通过 | 配置层已校验 PG / Redis URL；PG repository 仍 fail-fast |
| SQL 抽象 | SQLite / PostgreSQL DatabaseClient 基础回归 | `pnpm --filter juhe-ai-backend test:database-client` | SQLite 真库 CRUD / upsert / transaction 正常；PostgreSQL 占位符、execute、transaction、多语句 DDL result 归一化正常 | 已通过 | 基础层已落地；`bindPlaceholders()` 固定调用层继续写 `?`，PG driver 统一转 `$1...` |
| PG 主流程 | PostgreSQL schema 初始化 | `pnpm --filter juhe-ai-backend postgres:init-schema-only`、`postgres:init-schema` | 当前 schema 创建成功，默认数据写入成功 | 已通过 | `192.168.1.203` 验证 5 个 schema、570 条 schema 语句、138 条默认种子语句；抽样表数量和默认数据计数正常 |
| PG repository | provider 只读 repository 双 driver | `pnpm --filter juhe-ai-backend test:provider-repository-driver` | SQLite 和 PostgreSQL 供应商、协议档案、endpoint family 读取语义一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/providers` 和 `/__aisys__/api/providers/options` 已切到 async repository |
| PG repository | 登录 / 会话最小 repository 双 driver | `pnpm --filter juhe-ai-backend test:system-account-session-driver` | 默认管理员凭据校验、session 创建 / 查询 / touch / revoke、最近登录时间更新一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；登录、登出、`requireAuth` 和 `/auth/me` 会话读取已切到 async repository |
| PG repository | 系统账户管理读写双 driver | `pnpm --filter juhe-ai-backend test:system-account-management-driver` | 系统账户分页 / 选项 / 创建 / 默认分组 / 更新 / 禁用 / 唯一性校验一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/system-accounts` 列表、选项、创建和更新已切到 async repository |
| PG repository | 分组管理读写双 driver | `pnpm --filter juhe-ai-backend test:group-management-driver` | 分组分页 / 选项 / 账户组选项 / 创建 / 高并发调度策略 / 更新 / 删除 / 唯一性校验一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/groups` 列表、选项、账户组选项、创建、更新和删除已切到 async repository；PG 模式下分组列表已读取真实分组和绑定账户 ID，统计聚合仍待账号 / 统计 repository 迁移 |
| PG repository | API Key 管理读写与网关 Key 校验双 driver | `pnpm --filter juhe-ai-backend test:api-key-management-driver` | API Key 分页 / secret / 创建 / 分组绑定 / 更新 / 刷新密钥 / 删除 / 唯一性校验 / 网关 Key 校验 / `read_gateway_runtime` Key、分组和最小账号候选读取入口一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/api-keys` 列表、secret、创建、更新、刷新密钥和删除已切到 async repository；回归通过 `createAccountAsync` 创建带支持模型和模型映射的最小候选账号并绑定分组；PG 模式下 `read_gateway_runtime` 已能读取有效 Key、绑定、分组访问元数据、默认响应检查策略、最小可调度账号候选、账号支持模型和模型映射；混合路由模型目录校验和删除后的 dataset/stats 清理目标登记仍待模型目录与清理仓储迁移 |
| PG repository | 公共设置和系统 API 限流设置双 driver | `pnpm --filter juhe-ai-backend test:settings-public-driver` | 公共全局设置、全局设置列表和系统 API 限流设置读取一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/settings/public` 和系统 API 限流中间件已切到 async repository |
| PG repository | 系统设置管理读写双 driver | `pnpm --filter juhe-ai-backend test:settings-management-driver` | 全局设置和系统设置的局部更新、恢复、缓存失效、非法字段拒绝一致 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；`/__aisys__/api/settings/global` 和 `/__aisys__/api/settings` 已切到 async GET / PATCH；PG 模式暂不支持在线修改统计时区 |
| System API | performance 最小 HTTP 烟测 | `pnpm --filter juhe-ai-backend test:performance-system-api-smoke` | public settings、captcha、login、me、settings GET/PATCH、system-accounts GET/POST/PATCH、providers、provider options、groups GET/POST/PATCH/DELETE、accounts POST/GET 列表/详情/options/PATCH/clearFailureState/DELETE、api-keys GET/POST/PATCH/secret/refresh-key/DELETE、logout 链路在 SQLite 和 PG/Redis 下可用 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；只覆盖登录前后、设置管理、系统账户管理、分组管理、AI 账户创建 / 普通 owner 列表 / 详情 / options / 常规更新 / 异常状态恢复 / 删除和 API Key 管理最小链路，不代表完整管理端 PG-ready |
| PG repository | 授权个人归还双 driver | `pnpm --filter juhe-ai-backend test:authorization-return-driver` | 账户授权实例归还、授权列表个人归还和分组个人归还后，grant 为 `returned`，manual source 撤销，runtime authorization 为 `returned`，重复归还返回空结果 | 已通过 | 本地 SQLite 通过；`192.168.1.203` 远端 PostgreSQL / Redis URL 下通过；只覆盖个人归还仓储，不代表授权实例列表 / 详情 / options、授权实例异常恢复或完整授权管理已 PG-ready |
| PG repository | 其他 CRUD / upsert / 分页 / 事务 | 双 driver 回归脚本 | SQLite 和 PostgreSQL 语义一致 | 未执行 | 授权实例列表 / 详情 / options、授权实例异常状态恢复、授权创建 / 更新 / 回收 / 列表、模型目录、授权、统计等 repository 待迁移 |
| Redis cache | memory / Redis 缓存一致 | `pnpm --filter juhe-ai-backend test:runtime-state-cache-driver` | TTL、delete、clear、版本失效一致 | 部分通过 | memory driver 已通过；Redis 连通待 Docker 栈验证 |
| Redis state | 原子计数 / 锁 / 并发占用 | `pnpm --filter juhe-ai-backend test:runtime-state-cache-driver`、`test:account-concurrency-lane-index`、`test:gateway-concurrency-preparation` | 不突破上限，不误释放锁 | 部分通过 | memory / 默认链路通过；Redis Lua 路径待连通压测 |
| 队列并发 | 最大消费并发 100 | `pnpm --filter juhe-ai-backend test:usage-record-writer-pool`、`test:usage-record-byte-batch` | 入队即消费，连接池不爆，队列指标可观测 | 部分通过 | 使用记录队列 PG 并发 drain 骨架已落地；PG 写入压测待 adapter |
| Redis Streams 队列 | 使用记录高性能队列缓冲 | `pnpm --filter juhe-ai-backend test:redis-stream-queue` | performance 默认 `redis_stream`，非 ingest 写入 Stream，consumer 成功落库后 ack，失败保持 pending | 已通过 | 本地回归覆盖 Stream 解析、配置、源码边界；`192.168.1.203` redis-state 已通过临时 Stream 的 XADD / XREADGROUP / XACK 烟测；真实积压恢复和端到端压测待执行 |
| 网关回归 | API Key 校验与调度 | mock gateway e2e | 缓存 miss / hit / 失效均正确 | 未执行 | 待实现 |
| 使用记录 | 高并发 usage 写入 | 性能脚本 | 明细无丢失，统计游标与结果一致 | 未执行 | 待实现 |
| 部署验证 | Docker 测试栈 | `192.168.1.203` compose 启动 / config 校验 | PG / PgBouncer / Redis / 应用健康，当前仓库 compose 可解析 | 部分通过 | PostgreSQL / PgBouncer / Redis cache / Redis state 已在 `/home/huanmin/juhe-ai-performance` 启动并 healthy；PG schema 和默认种子已初始化；当前仓库 standalone / performance compose 已在远端 `/tmp` 临时目录通过 `docker compose config --quiet`，临时目录已删除；本机连接远端验证需使用宿主机发布端口：PgBouncer `6432`、redis-cache `6379`、redis-state `6380`；应用容器完整 PG 管理端链路待 adapter |
| 故障演练 | Redis / PG 重启 | 手动重启容器 | 可读错误、自动重连、短 TTL 状态可恢复 | 未执行 | 待实现 |
| 备份恢复 | PostgreSQL 备份恢复 | 备份后恢复到新库 | 业务事实可恢复，统计可重建 | 未执行 | 待实现 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-25 | 默认保留 SQLite + memory，显式开启 PG + Redis | 保持轻量部署和已有用户使用习惯 | 新配置必须不破坏默认启动 |
| 2026-06-25 | PostgreSQL 模式仍保留 typed command / 队列语义 | 队列不仅解决 SQLite 写锁，也承接背压、重试、优先级和可观测性 | 只调整消费并发，不把所有调用改成直接写库 |
| 2026-06-25 | PostgreSQL 写队列最大消费并发设为 100 | 用户明确要求高性能模式下入队即可异步消费并发处理 | 必须配套连接池、PgBouncer 和背压指标 |
| 2026-06-25 | Redis cache 与 Redis runtime state 拆分 | cache 可以淘汰，调度运行态和原子计数不能被随机淘汰 | 生产推荐两个 Redis 实例或容器 |
| 2026-06-26 | 暂不引入 RabbitMQ，先用 Redis Streams 承接高性能队列缓冲 | Redis 已是高性能模式必选依赖，先减少新中间件数量；Redis Streams 可以满足使用记录首批队列的持久化缓冲、consumer group、pending 重投和容量控制 | 首批只覆盖使用记录队列；审计、操作日志、公开接口日志、运行日志索引和数据维护仍按后续迁移计划推进 |
| 2026-06-25 | SQLite 到 PostgreSQL 只支持停机离线迁移 | 项目默认不做运行时旧结构兼容、双读双写或自动迁移 | 迁移脚本必须显式确认离线执行 |
| 2026-06-25 | PostgreSQL schema 使用 `juhe_*` 独立 schema 并从 SQLite DDL 生成 | 保留当前多库职责边界，同时避免业务代码直接感知 PostgreSQL schema 名 | 初始化脚本负责 SQL 方言转换、外键依赖排序和默认种子写入 |
| 2026-06-25 | 先落地 `DatabaseClient` / `SqlDialect` 基础层，再逐个迁移 repository | 当前同步 repository 数量大，直接批量切 PG 风险高 | `postgres:init-schema` 已接入基础层，provider 只读 repository 已作为第一条 PG 读路径验证，登录 / 会话最小 repository 已作为第一条 PG 认证路径验证，公共设置和系统 API 限流设置已作为登录前读链路验证，系统设置管理读写、系统账户管理读写、分组管理读写、API Key 管理读写与网关 Key 校验入口已作为管理端和网关认证入口验证，后续按模块迁移和验证 |
| 2026-06-26 | 网关运行态接入 PG 最小候选账号读取 | 已补齐账号候选读取依赖的分组访问元数据、候选窗口、账号授权、支持模型、模型映射、代理、API Key 运行态、质量分和响应检查策略 PG 读取，并用本地 SQLite 与远端 PostgreSQL / Redis 回归验证 | `read_gateway_runtime` 在 PG 模式下可读取有效 API Key、绑定分组、网关设置、分组访问元数据和最小可调度账号候选；完整 `/v1` PG-ready 仍需账号管理写路径、模型目录、usage / stats、授权额度和审计链路继续迁移 |
| 2026-06-26 | 账号模型配置附属表先补 async replace 写入 helper | 账号创建 / 更新完整迁移前，需要先解除 `account_supported_models` 和 `account_model_mappings` 的 SQLite 写入依赖 | PG 模式下可通过事务写入候选账号支持模型和模型映射；已纳入 `test:api-key-management-driver` 的本地 SQLite 与远端 PostgreSQL / Redis 验证 |
| 2026-06-26 | 先落地 AI 账户最小创建 repository，不直接切账号管理路由 | 账号 HTTP 路由仍依赖同步 provider / group / list / detail / operation log 周边路径，直接切路由会牵出更大迁移面 | `createAccountAsync` 已支持 PG 事务创建账号、绑定分组、写名称搜索词、标签、支持模型和模型映射；模型目录读取已补 async PG 路径，当前回归创建时直接携带模型配置 |
| 2026-06-26 | AI 账户详情先支持普通 owner 回看，不一次性迁移列表和授权实例详情 | 管理端创建后的回看是当前 System API smoke 缺口；完整列表和授权视图依赖统计、授权、质量和运行态多张表，迁移面更大 | `findAccountSummaryAsync` / `findAccountForTestAsync` 已支持 PG 读取普通 owner 账号详情、分组绑定、标签、支持模型和模型映射；usage、账号质量、API Key 运行态明细暂不在详情请求里实时聚合 |
| 2026-06-26 | AI 账户列表先支持普通 owner 分页，不做授权实例和精确计数 | 管理端自有账号列表是创建后继续管理的基本链路；授权实例和统计质量维度依赖更多 repository，不能在列表请求里临时扫明细 | `listAccountsPageAsync` 已支持 PG 普通 owner 列表、名称词项搜索、分组 / 标签 / 状态 / 调度状态过滤和基础排序；`test:performance-system-api-smoke` 已覆盖按 keyword + groupId 查回新建账号 |
| 2026-06-26 | AI 账户 options 先支持普通 owner 轻量下拉 | 账号选择器只需要轻量字段，适合先解除管理端常用筛选入口对 SQLite 同步仓储的依赖；授权实例 options 仍涉及授权视图边界 | `listAccountOptionsAsync` 已支持 PG 普通 owner ID、名称、供应商、协议档案、分组、标签、类型、状态和调度状态过滤；`test:account-options-lightweight` 与 `test:performance-system-api-smoke` 已覆盖 |
| 2026-06-26 | AI 账户更新先支持普通 owner 常规字段和分组重绑 | 更新路径需要复用创建时的模型、标签、代理、时间计划和名称搜索校验；授权实例运行态 mutation 单独迁移更稳 | `updateAccountAsync` / `setAccountGroupAsync` 已支持 PG 普通 owner 常规 PATCH 和分组绑定更新；`test:performance-system-api-smoke` 已覆盖 |
| 2026-06-26 | AI 账户异常恢复先支持普通 owner | 普通 owner 恢复只需要更新账号失败、冷却和 stream failure 字段；授权实例恢复还要校验授权绑定上下文 | `clearAccountFailureStateAsync` 已支持 PG 普通 owner `clearFailureState`，授权实例异常恢复仍待迁移；`test:performance-system-api-smoke` 已覆盖 |
| 2026-06-26 | AI 账户删除先支持普通 owner 逻辑删除 | 删除会影响源账号、授权实例、授权状态、名称搜索和缓存失效，适合先迁移业务库内一致性；dataset / stats 历史清理目标后续随清理仓储迁移 | `deleteAccountWithRelatedCleanupAsync` 已支持 PG 撤销账号资源授权、逻辑删除源账号和授权实例、清理标签 / 搜索索引并失效网关与授权运行态缓存；`test:performance-system-api-smoke` 已覆盖 DELETE 后详情 404 |
| 2026-06-26 | 授权个人归还先补仓储级 PG 事务路径 | HTTP 完整授权管理仍依赖大量同步授权仓储；个人归还自身可以先按最小事务闭环解除 SQLite 写入依赖 | `returnAccountAuthorizationInstanceForGranteeAsync`、`returnResourceAuthorizationForGranteeAsync` 和 `returnGroupAuthorizationForGranteeAsync` 已支持 PG 更新 direct grant、manual source 和 runtime authorization；账户 / 授权列表 / 分组归还路由已切 async；`test:authorization-return-driver` 已覆盖本地 SQLite 和远端 PG |
| 2026-06-25 | 宿主机验证不能直接复用容器内 DNS 连接串 | `pgbouncer`、`redis-cache`、`redis-state` 只在 Compose 网络内可解析，宿主机应走发布端口 | 文档和验证脚本必须区分容器内 URL 与宿主机 URL；redis-state 容器内端口是 `6379`，宿主机默认发布为 `6380` |
| 2026-06-25 | PostgreSQL 模式下网关运行态缓存失效不走 SQLite after-commit 队列 | 已迁移 PG 写路径提交后再通知当前进程 invalidator，继续调用 SQLite after-commit 会被 fail-fast 拦截 | `runGatewayCacheInvalidatorsAfterCommit()` 在 PG driver 下直接执行 effect；跨进程缓存失效广播仍待后续 runtime state / Redis 化 |

## 验收标准

- [ ] 默认 standalone 不配置 PostgreSQL / Redis 也能启动并通过现有回归。
- [ ] performance 模式缺少 PostgreSQL / Redis 配置时快速失败。
- [x] PostgreSQL schema 能初始化空库，并写入默认数据。
- [ ] 主要 repository 在 SQLite 和 PostgreSQL 下语义一致。
- [ ] Redis cache / runtime state driver 支持 TTL、版本失效、原子计数和锁。
- [ ] PostgreSQL 模式写队列入队即触发消费，最大并发 `100`，且连接池和队列背压可观测；使用记录 Redis Streams 首批链路已完成，其他队列待迁移。
- [ ] `192.168.1.203` Docker 测试栈可以启动、健康检查、备份和恢复。
- [ ] 网关主链路、使用记录、审计、统计聚合、授权额度和缓存失效在 performance 模式下通过验证。
- [ ] 部署、迁移、备份恢复和故障演练文档同步完成。

## 验证记录

- 类型检查：已通过，命令 `pnpm --filter juhe-ai-backend typecheck`。
- 构建：未执行，命令待实现后执行 `pnpm build`。
- 单项验证：已通过 `test:runtime-config-env-override`、`test:runtime-state-cache-driver`、`test:database-client`、`test:provider-repository-driver`、`test:system-account-session-driver`、`test:system-account-management-driver`、`test:group-management-driver`、`test:api-key-management-driver`、`test:authorization-return-driver`、`test:settings-public-driver`、`test:settings-management-driver`、`test:redis-stream-queue`、`test:performance-system-api-smoke`、`test:authorization-return`、`test:gateway-runtime-cache`、`test:postgres-schema-sql`、`test:auth-memory-maintenance-boundary`、`test:auth-must-change-password-boundary`、`test:system-api-rate-limit`、`test:system-account-list-query-guard`、`test:system-account-whitespace-boundary`、`test:system-account-options-lightweight`、`test:account-options-lightweight`、`test:deleted-account-related-cleanup`、`test:group-options-lightweight`、`test:group-scheduling-policy`、`test:default-group-current-contract`、`test:api-key-route-validation`、`test:api-key-availability-schedule`、`test:api-key-multi-group-bindings`、`test:account-concurrency-lane-index`、`test:gateway-concurrency-preparation`、`test:usage-record-writer-pool`、`test:usage-record-byte-batch`。
- 手动验证：`192.168.1.203` 已完成中间件实启，PostgreSQL / PgBouncer / Redis cache / Redis state 均 healthy，`pg_stat_statements` 已创建。当前仓库 standalone / performance compose 已在远端 `/tmp` 临时目录通过 `docker compose config --quiet`，远端 Docker Compose 版本 `2.40.3+ds1-0ubuntu1~22.04.1`，临时目录已删除且未启动新容器。远端 PostgreSQL 已通过 `DatabaseClient` 路径执行 `postgres:init-schema-only` 和 `postgres:init-schema`，结果为 5 个 schema、570 条 schema 语句、138 条默认种子语句；抽样计数：业务表 47、数据集表 19、使用表 5、统计表 59、Codex context 表 3、默认系统账号 1、供应商 6、协议档案 10、默认分组 10、系统设置 55。`test:provider-repository-driver`、`test:system-account-session-driver`、`test:system-account-management-driver`、`test:group-management-driver`、`test:api-key-management-driver`、`test:authorization-return-driver`、`test:settings-public-driver`、`test:settings-management-driver` 和 `test:performance-system-api-smoke` 已使用远端 PostgreSQL / Redis URL 验证 provider 只读 repository、登录 / 会话最小 repository、系统账户管理读写、分组管理读写、API Key 管理读写与网关 Key 校验入口、AI 账户最小创建与分组绑定 repository、AI 账户创建和普通 owner 列表 / 详情 / options / 常规更新 / 异常状态恢复 / 删除 HTTP 链路、账户授权实例归还、授权列表个人归还、分组个人归还、网关运行态最小账号候选读取、账号支持模型 / 模型映射附属表 replace 写入、公共设置读取、系统 API 限流设置读取、系统设置管理读写和最小 HTTP 链路。redis-state 已通过临时 Stream 的 XADD / XREADGROUP / XACK 烟测并删除临时 key。宿主机连接远端 Docker 栈时已确认必须使用发布端口：PgBouncer `6432`、redis-cache `6379`、redis-state `6380`，不能把 redis-state 的容器内 `6379` 误当宿主机端口。
- 未验证项：其他 PostgreSQL repository adapter、应用容器 performance 模式、Redis Streams 远端端到端积压恢复、迁移脚本、PG 压测、故障演练和备份恢复。

## 风险与注意事项

- PostgreSQL 没有 SQLite 文件级全局写锁，但仍有行锁、唯一索引冲突、长事务、连接池耗尽和 autovacuum 压力。
- 最大并发 `100` 必须配套 PgBouncer、连接池上限和队列背压；不能让每个 Node 进程都创建 100 个数据库连接。
- Redis cache 与 runtime state 如果共用 LRU 实例，可能导致限流、并发占用或短 TTL 屏蔽被意外淘汰。
- Redis 8 授权需要生产前确认；如果授权不可接受，要另起决策评估替代方案。
- 迁移会触及大量 repository 和 SQL 差异，必须先做 SQLite adapter 回归，再接 PostgreSQL。
- 服务器凭据不能写入仓库文档、日志或计划，只能保存在部署环境安全位置。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
