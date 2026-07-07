# Go 后端架构基线

## 1. 技术基线

- 具体依赖选择以 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md) 为准；本文只记录架构边界和运行约束。
- Go 版本：以正式落代码时 `go.dev/dl/` 的最新稳定 Go 版本为准；当前文档参考 `go1.26.x`，落代码时再固定 `go.mod` 主次版本。
- HTTP 基础：优先使用标准库 `net/http`，路由层使用轻量 `go-chi/chi/v5`。不引入 Gin、Fiber 这类更重的框架，避免把问题转移到框架约定。
- JSON：默认使用标准库 `encoding/json`。只有压测证明 JSON 编解码成为瓶颈时，才评估替代库，并先写报告。
- 日志：使用标准库 `log/slog`，统一结构化字段、trace ID、请求 ID、模块名和错误摘要。
- 配置：使用 `internal/config` 封装 env 解析，不引入 Viper；配置库只在 config 层出现。
- PostgreSQL：使用 `pgx/v5`、`pgxpool` 和 `sqlc` 生成查询代码，通过连接池、事务函数和上下文超时收口。
- Redis：使用 `redis/go-redis/v9` 承接 cache、state、counter、fixed-window 和 penalty-window 运行态；可靠任务队列默认使用 Asynq，不手写通用 Redis Streams 队列。
- SQLite：Go 后端不引入 SQLite driver，不提供 standalone 模式，不维护 SQLite schema、adapter 或测试矩阵。
- 观测：Prometheus `/__aisys__/metrics`、受控 pprof、结构化 `slog` 和内部系统监控 API 分层维护；Go runtime 指标以 `runtime/metrics`、Prometheus Go collector、PG/Redis/Asynq adapter 和 worker 采样为基础，具体口径见 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)。
- 校验：使用 DTO 校验库处理字段形状和范围，跨字段业务规则仍写 service 校验，并转换为项目中文错误结构。
- 测试：使用 Go 标准 `testing`、`httptest`、`go test ./... -race`、基准测试、testcontainers、必要的 mock upstream 和 goroutine 泄漏检查；跨服务依赖测试必须显式触发。

## 2. 目标目录

首个 Go 后端目录建议独立于当前 `backend/`，例如：

```text
backend-go/
  go.mod
  go.sum
  cmd/
    juhe-ai/
      main.go
    juhe-ai-worker/
      main.go
    juhe-ai-maintenance/
      main.go
  internal/
    app/
    config/
    httpapi/
    middleware/
    modules/
      auth/
      publicapi/
      systemaccounts/
      accounts/
      groups/
      apikeys/
      routestrategies/
      providers/
      proxies/
      usagerecords/
      auditlogs/
      stats/
      settings/
      gateway/
    platform/
      postgres/
      redis/
    store/
      port/
      postgres/
    jobs/
      queue/
      publicapilog/
      worker/
      ingest/
      stats/
      ops/
    protocols/
      openai/
      anthropic/
      gemini/
      bridge/
    runtime/
    security/
    shared/
    testkit/
```

目录规则：

- `cmd/` 只做命令装配和启动参数解析。
- `internal/httpapi/` 放统一响应、错误结构、分页、路由注册和 API 契约工具。
- `internal/modules/<module>/` 放模块 route、service、DTO 和模块私有测试。
- `internal/platform/` 放 PG / Redis / 外部基础设施 health、client 和低层 adapter；第三方库类型不能穿透到业务模块。
- `internal/store/port/` 定义业务语义接口；PostgreSQL 查询实现只在 store adapter 内出现，Redis cache / state 只通过项目封装访问。
- `internal/jobs/` 放后台任务角色，禁止 HTTP route 直接启动长期任务。
- `internal/jobs/queue/` 封装 Asynq，业务模块只依赖项目 Queue Port，不能 import Asynq 类型。
- `internal/jobs/<domain>/` 放任务 payload、enqueue 封装和 handler，handler 依赖项目 store port；Asynq server/mux 装配只允许放在 worker runtime adapter。
- `internal/jobs/worker/` 放 Asynq worker runtime adapter、任务注册和不可重试错误映射；业务模块、store port 和 HTTP route 不能 import Asynq。
- `internal/protocols/` 放协议适配和桥接，不把 OpenAI / Anthropic / Gemini 字段路径写散到 gateway service。
- `internal/runtime/` 放短 TTL 运行态、并发占用和缓存版本，所有 map 必须有锁或使用并发安全结构。

## 3. 进程模型

Go 目标不是复制当前 Node 进程树。

- 主 server：承载系统 API、公开 API、静态资源、网关入口、健康检查和必要 supervisor。
- worker：保留 `ingest`、`stats`、`ops` 三类角色的业务边界，但不再因为 Node 事件循环阻塞而拆出额外 DB service。
- maintenance：生产维护脚本以独立命令运行，必须明确 dry run、影响范围和失败行为。
- DB service：迁移完成后删除。Go 后端直接通过 PostgreSQL 连接池、事务、Redis state/cache/queue 和有界后台队列表达存储边界，不再为 SQLite 单写者保留独立进程。

目标进程拓扑先按独立角色设计，不把所有 worker 长期塞进主 server goroutine：

| 进程 / 命令 | 职责 | 健康检查 | 退出与重启 |
| --- | --- | --- | --- |
| `juhe-ai server` | HTTP API、静态资源、公开接口、网关入口、轻量 supervisor 和 readiness；W1b `/__aipublic__` 仅在 `JUHE_AI_PUBLIC_API_ENABLED=true` 时 opt-in 挂载，W2 已迁移的 `proxies/options`、`providers/options`、`providers/models/options`、`providers/{code}/models`、`route-strategies/options`、`my-route-strategies/options`、`groups/options`、`my-groups/options`、`groups/account-options`、`my-groups/account-options`、`accounts/options`、`my-accounts/options`、`accounts/tags` 和 `my-accounts/tags` owner-only 等管理端只读路由仅在 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 时 opt-in 挂载，二者默认关闭 | HTTP health、PG/Redis 基础连通、网关运行态只读检查；W1b 开启时检查 Redis state、Redis queue 和 `JUHE_AI_SECRET`；W2 开启时检查管理端会话 store 和 session schema | 优雅关闭 HTTP、取消请求 context，不吞 worker 失败；W1b / W2 回滚优先关闭开关并恢复路径 owner |
| `juhe-ai-worker ingest` | 使用记录、审计、操作日志、公开接口日志、运行日志索引和维护清理消费 | Asynq queue depth、retry / dead 数量、PG 写入延迟、游标推进 | shutdown drain，未完成任务由队列重试或进入 dead / archived 状态 |
| `juhe-ai-worker stats` | 统计窗口、额度窗口、TopN、趋势、系统监控和表监控 | job state、统计滞后、PG 写入延迟 | 持有租约或单 owner，退出时释放租约 |
| `juhe-ai-worker ops` | 账号测试、健康检测、代理检测、OAuth token 保活、授权到期扫描 | 任务队列、租约、外部 I/O 并发和错误率 | 取消外部请求，记录未完成任务，等待下轮重试 |
| `juhe-ai-maintenance` | 离线导出、导入、重建、清理和诊断 | 一次性命令退出码和报告文件 | 必须支持 dry run 或明确影响范围 |

如果后续为了轻量部署把 worker 由 server 看护启动，也只能作为进程生命周期管理方式，不能改变角色边界、租约、连接池预算和队列 ACK 语义。

## 4. 并发与线程安全

Go 解决的是 Node 单事件循环问题，不代表可以无界并发。

- 所有请求入口必须创建或继承 `context.Context`，客户端断开、超时、上游取消和服务关闭时必须向下传递取消信号。
- 外部请求使用共享 `http.Client` 和自定义 `Transport`，设置连接池、空闲连接、TLS、代理和超时；禁止每次请求新建无界 client。
- 数据库访问必须受连接池、事务作用域、上下文超时和热点 key 顺序约束。
- PostgreSQL 通过 PgBouncer / pool budget 隔离 server、gateway hot path、management API、ingest、stats 和 ops；慢管理查询、后台批量写和网关热路径不能共用一个无差别池。
- PostgreSQL 写入必须受事务范围、`statement_timeout`、`lock_timeout`、`idle_in_transaction_session_timeout`、批量窗口、分区查询窗口、稳定排序和热点 key 顺序约束；连接必须带 `application_name` 便于定位来源。
- Redis cache、state、queue 必须使用独立 namespace / DB / 实例或明确隔离配置；Go 配置层要拒绝 cache / state / queue 指向同一个 Redis DB，queue 不和 cache/state 共用淘汰策略。来源系统限频这类跨实例运行态必须落在 Redis state，不允许生产路径退回进程内 map。
- W1b public API 生产挂载必须由 `JUHE_AI_PUBLIC_API_ENABLED` 显式控制，默认不注册 `/__aipublic__`；开启时必须显式配置 Redis state、Redis queue 和稳定 `JUHE_AI_SECRET`，不能使用开发默认密钥或本地队列兜底。
- W2 管理端只读接口已经具备 `requireAuth` 级 Go 会话鉴权基线，并已覆盖 `proxies/options`、`providers/options`、`providers/models/options`、`providers/{code}/models`、`route-strategies/options`、`my-route-strategies/options`、`groups/options`、`my-groups/options`、`groups/account-options`、`my-groups/account-options`、`accounts/options`、`my-accounts/options`、`accounts/tags` 和 `my-accounts/tags` owner-only 只读路径：读取 `juhe_ai_session`、按 SHA-256 `token_hash` 查询 `system_sessions`、校验 `system_accounts.status` 和 `expires_at`，并按 Node 语义拦截普通用户初始密码修改；其中管理侧策略路由、分组下拉、分组账号下拉、账户下拉和账号标签下拉必须 admin-only，用户侧 `my-*` 路径必须强制当前登录用户作用域且不返回管理侧 owner 字段，模型目录必须保留 OpenAI-compatible / hybrid 聚合和 `hybrid` 自身排除规则。分组下拉 / 分组账号下拉当前尚未覆盖 Node 的授权组 union，账户下拉尚未覆盖授权账户 union 和名称包含搜索，账号标签只读接口不覆盖标签写接口，不得作为完整生产接管证据。W3 登录 / session 创建、`requireAdmin` 和 `my-*` 作用域尚未整体迁移前，生产 server 必须默认关闭 W2 opt-in。任何 `__aisys__/api` 后台路由都不能为了联调绕过会话鉴权、初始密码修改拦截、admin / 普通用户边界、`my-*` 作用域或模型目录可见性边界。
- Asynq 可靠任务队列必须配置任务超时、重试、dead / archived 处理、Redis dial/read/write timeout、队列深度和最老任务年龄监控；任务 handler 必须幂等。
- 原始 Redis Streams 只允许作为专项 adapter 例外使用，不能绕过 Asynq 再手写一套通用 queue 框架。
- 内存 map、LRU、账号并发快照、IP 运行态、会话亲和和短 TTL 状态必须使用 mutex、RWMutex、atomic 或专用并发结构。
- channel 必须有容量和关闭语义；后台队列必须定义满载时拒绝、合并、丢弃或降级策略。
- 启动的 goroutine 必须属于 server lifecycle、worker lifecycle 或明确任务 context；禁止无 owner 的后台 goroutine。
- 使用 `go test -race` 作为迁移期默认验证项之一。

## 5. 内存与大数据边界

- 请求体、响应体、OAuth token 响应、审计 payload、日志文件、导入导出文件继续使用大小上限、stream、cursor、offset 或窗口读取。
- SSE 解析必须增量处理，不为了常规判断拼接完整流。
- 管理列表、日志、审计、使用记录和统计页面不得把全量数据读入内存分页。
- 统计、额度、趋势、TopN 和摘要继续读取 worker 生成的窗口表、summary 表或缓存，不在 API 请求里实时扫描明细。
- pprof 和运行时指标作为 Go 后端标配入口，但公网部署必须有访问控制。`/__aisys__/metrics` 面向外部采集，pprof 面向受控诊断，内部系统监控页面读取 PostgreSQL 窗口表；三者不能互相替代。
- Go 系统监控契约必须使用 goroutine、scheduler latency、GC pause、heap、RSS、PG pool、Redis、Asynq queue、worker lag 和 stats freshness 等字段；不得把这些指标命名为 Node `eventLoopLagMs`，也不得继续把 `db-service` 作为 Go 长期角色。

Go 运行边界矩阵：

| 入口 | 读取方式 | 边界要求 |
| --- | --- | --- |
| 网关 raw body | 有上限读取，必要时分 lane 识别 | 继承当前 `64mb` 入口硬上限和文本 lane `16mb` 业务上限；认证前可拒绝的请求不得先读大 body |
| 大 JSON 请求 | 有界扫描或流式解析 | 不在请求路径构造无界对象；需要改写时才进入完整解析 |
| SSE | 增量解析和 flush | 不拼接完整流；处理慢客户端 backpressure、半帧事件、客户端断开和上游 cancel |
| OAuth token 响应 | 固定字节上限 | 超限主动中断，错误摘要不得包含敏感 token |
| 审计 payload / 日志文件 | offset / cursor / stream / window | 不完整读入内存分页；只在完整行或完整窗口后推进游标 |
| 导入导出 / 离线迁移 | 离线批处理 | 明确 dry run、批量窗口、失败续跑和报告位置 |

## 6. 可删除的 Node 专用复杂度

迁移完成后应删除或收敛：

- `node:sqlite` 能力预检和 Node 版本分支。
- SQLite standalone、SQLite 多库拆分、usage shard 文件写入、SQLite read worker、SQLite writer owner 和相关测试矩阵。
- 因事件循环阻塞而存在的 DB service HTTP/IPC 代理层。
- Node 专属系统指标：`eventLoopLagMs`、`process_event_loop_*`、V8 `processHeap*` / `external` / `arrayBuffers`、DB service 运行态、SQLite 文件体积、usage shard 文件路径和 IPC pending 队列指标。
- worker thread 大 JSON 解析边界，改为 Go 请求 goroutine + 有界解析策略。
- `p-limit` 等为 Node 并发协调补出来的通用胶水，改为 context、semaphore、channel 或连接池。
- `tsx` 开发运行链路和后端 TypeScript 编译链路。
- Node worker role 中只为事件循环隔离存在的 IPC pending 防护。

不能删除的真实业务约束：

- PostgreSQL 连接池、事务隔离、锁等待、索引和批量写窗口要求。
- Redis cache / state / queue 的 TTL、容量、任务重试、死信和降级要求。
- 上游账号并发、代理质量、账号冷却、短 TTL 屏蔽和来源保护。
- 使用记录、审计、操作日志、运行日志和统计聚合的异步写入边界。
- 请求体大小、SSE backpressure、客户端断开和上游取消处理。
- 敏感字段加密、脱敏和权限控制。
- 系统可观测性边界：health 只判断当前依赖可用性，Prometheus 负责实时采集，pprof 负责诊断，系统监控 API 负责管理页面窗口趋势；所有指标 label 必须低基数且不含敏感信息。
