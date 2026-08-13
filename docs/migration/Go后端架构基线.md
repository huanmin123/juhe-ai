# Go 后端架构基线

> **现行双模式基线（2026-08-12）。** 当前已有受版本控制的 `backend-go/go.mod` 及 F1/F2/F3 实现。Go 要同时支持 SQLite 与 PostgreSQL/Redis，并按 [完整功能接管与 Node 归档迁移规则](完整功能接管与Node归档迁移规则.md) 的 L1-L4 生命周期一次接管一个完整功能：接管后对应 Node 文件退出活跃路径并归档，不保留 fallback。功能批次编号 F1-F6 以 [当前状态页](迁移状态与后续批次-20260812.md) 为准。

## 1. 技术基线

- 具体依赖选择以 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md) 为准；本文只记录架构边界和运行约束。
- Go 版本：以正式落代码时 `go.dev/dl/` 的最新稳定 Go 版本为准；当前文档参考 `go1.26.x`，落代码时再固定 `go.mod` 主次版本。
- HTTP 基础：优先使用标准库 `net/http`，路由层使用轻量 `go-chi/chi/v5`。不引入 Gin、Fiber 这类更重的框架，避免把问题转移到框架约定。
- JSON：默认使用标准库 `encoding/json`。只有压测证明 JSON 编解码成为瓶颈时，才评估替代库，并先写报告。
- 日志：使用标准库 `log/slog`，统一结构化字段、trace ID、请求 ID、模块名和错误摘要。
- 配置：使用 `internal/config` 封装 env 解析，不引入 Viper；配置库只在 config 层出现。
- PostgreSQL：适配器使用连接池、事务函数和上下文超时收口；具体库选择在 B0 后固定。
- Redis：仅 PostgreSQL/Redis 模式的 cache、state 可使用 Redis。当前 Node Redis Streams 是 Node 历史实现，新 Go 完整功能默认直接异步执行，不能混称或假定已接线。
- SQLite：必须提供 SQLite Store adapter；具体 driver、连接方式和 Node owner bridge 在 B0 定案。SQLite 模式不要求 Redis，并继续遵守每个文件单 writer。
- 观测：Prometheus `/__aisys__/metrics`、受控 pprof、结构化 `slog` 和内部系统监控 API 分层维护；Go runtime 指标以 `runtime/metrics`、Prometheus Go collector、PG/Redis adapter 和直接异步执行状态为基础，具体口径见 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)。
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
      accounthealthcheckdispatch/
      postgres/
      redis/
    store/
      port/
      postgres/
    jobs/
      async/
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
- `internal/store/port/` 定义业务语义接口；SQLite / PostgreSQL 方言、schema 与事务差异只在 store adapter 内出现，Redis cache / state 只通过项目封装访问。
- `internal/jobs/` 仅放完整功能专属的定时触发与维护入口，禁止 HTTP route 直接启动失去生命周期管理的长期工作。
- `internal/async/` 定义直接异步执行、`context` 取消、结果汇总和按资源维度的并发控制；不引入通用队列、常驻 worker pool 或第三方任务类型。
- `internal/jobs/<domain>/` 放完整功能的触发、输入和直接异步入口；goroutine 内直接调用 Store Port 并返回提交结果，不通过内存队列伪装持久化。
- `internal/jobs/worker/` 仅在完整功能确有独立进程生命周期时放命令装配和取消；SQLite 使用文件 owner bridge / 直接等待，PostgreSQL/Redis 直接经连接池执行。业务模块、store port 和 HTTP route 不得依赖历史队列类型。
- `internal/protocols/` 放协议适配和桥接，不把 OpenAI / Anthropic / Gemini 字段路径写散到 gateway service。
- `internal/runtime/` 放短 TTL 运行态、并发占用和缓存版本，所有 map 必须有锁或使用并发安全结构。

## 3. 进程模型

Go 目标不是复制当前 Node 进程树。

- 主 server：承载系统 API、公开 API、静态资源、网关入口、健康检查和必要 supervisor。
- worker：保留 `ingest`、`stats`、`ops` 三类角色的业务边界，但不再因为 Node 事件循环阻塞而拆出额外 DB service。
- maintenance：生产维护脚本以独立命令运行，必须明确 dry run、影响范围和失败行为。
- DB service：当前 SQLite 模式保留。Go 与 Node 共存期间，Go 只能经 typed command / owner bridge 写入 Node 正在拥有的业务 SQLite；完成冻结、drain 与 handoff 后才可独占目标文件。
- W1b 到 W7 的既有 bridge 叙述是历史实现记录；当前 Node Web 与 `ops-worker` 仍需运行，直到对应完整功能完成 L1-L4 接管；不得由局部 Go 实现推导 Go-only、网关、账户管理或主要 HTTP 接口接管。

历史 PG/Redis 进程与命令记录如下，仅用于保留原方案的接口和验证线索；当前不证明这些 Go 命令、worker runtime 或依赖已存在。B0 必须先分别完成 SQLite owner bridge 与 PostgreSQL/Redis Store、直接异步执行的 PoC：

| 进程 / 命令 | 职责 | 健康检查 | 退出与重启 |
| --- | --- | --- | --- |
| `juhe-ai server` | HTTP API、静态资源、公开接口、网关入口、轻量 supervisor 和 readiness；W1b `/__aipublic__` 仅在 `JUHE_AI_PUBLIC_API_ENABLED=true` 时 opt-in 挂载，W2 管理端辅助、标签和 operation log 读路径以及 W3 `auth/captcha`、`auth/login`、`auth/me`、`auth/change-password`、`auth/logout`、`POST system-accounts`、`PATCH system-accounts/{id}` 完整 mixed PATCH 仅在 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 时 opt-in 挂载；登录会话列表和按 ID 撤销不注册；系统账户列表 / options 保持非敏感字段白名单，完整 PATCH 已覆盖 `super_admin` 权限、Node 兼容密码 hash、session 撤销、最后一个启用 `super_admin` 保护、gateway runtime cache / API Key validation cache 清理和密码日志脱敏；相关开关默认关闭，不代表生产接管 | HTTP health、PG/Redis 基础连通、网关运行态只读检查；W1b 开启时检查 Redis cache/state/queue 和 `JUHE_AI_SECRET`；W2/W3 全量管理开关开启时检查管理端会话 store、session schema、Redis cache/state/queue 和稳定 `JUHE_AI_SECRET`，任一缺失或不可用均在监听前 fail-fast；已删除登录会话专用窄开关及其专用依赖分支 | 优雅关闭 HTTP、取消请求 context，不吞 worker 失败；W1b / W2 / W3 回滚优先关闭开关并恢复路径 owner |
| `juhe-ai-worker ingest` | 已完整接管的记录型功能的独立命令 | 直接异步运行数、写入延迟、游标推进、失败与重启恢复 | 取消正在运行的 context；未完成工作从功能自身持久事实重新发现 |
| `juhe-ai-worker stats` | 统计窗口、额度窗口、TopN、趋势、系统监控和表监控 | job state、统计滞后、PG 写入延迟 | 持有租约或单 owner，退出时释放租约 |
| `juhe-ai-worker ops` | 账号测试、健康检测、代理检测、OAuth token 保活、授权到期扫描 | 任务队列、租约、外部 I/O 并发和错误率 | 取消外部请求，记录未完成任务，等待下轮重试 |
| `juhe-ai-maintenance` | 离线导出、导入、重建、清理和诊断 | 一次性命令退出码和报告文件 | 必须支持 dry run 或明确影响范围 |

如果后续为了轻量部署把 worker 由 server 看护启动，也只能作为进程生命周期管理方式，不能改变完整功能边界、SQLite 单 writer、连接池预算、取消和重启恢复语义。

## 4. 并发与线程安全

Go 解决的是 Node 单事件循环问题。迁移默认使用 Go 原生 goroutine 直接异步模型，不保留仅为 Node event loop、worker thread、`p-limit`、IPC pending 或通用队列妥协而存在的保守串行实现和低并发常量；但 goroutine 是低成本 M:N 调度，不是无限成本的虚拟线程，外部依赖、文件描述符、CPU、网络和任务 payload 仍不能无界增长。

### 4.1 默认高并发执行模型

- 独立 I/O 和后台分析默认以 `errgroup.WithContext` 或等价结构直接 fan-out goroutine，不因 Node 历史实现保留逐条串行、常驻自制线程池、通用队列或任意低并发常量。
- 每个 goroutine 直接调用 Store Port 并收集提交结果。必须跨重启恢复的工作从该功能的持久事实重新发现；不使用内存队列、Asynq 或 Redis Streams 作为新 Go 功能的默认前提。
- 数据库候选读取仍按 cursor / `LIMIT` / claim window 有界获取，再对当前窗口 fan-out；禁止先全表加载再启动海量 goroutine。统计任务继续读取预聚合输入或按游标增量处理，不借高并发回到请求链路实时扫描明细。
- 并发按 SQLite 文件、PostgreSQL pool、上游 / 代理、CPU、网络和单任务 payload 等真实维度隔离；不设置迁移层人为全局低并发或业务限速。是否增加并发由吞吐、P95/P99、错误率、依赖饱和度、goroutine/heap/FD 共同决定。

### 4.2 真实容量边界

- 并发只受真实资源约束：SQLite 文件单 writer、PostgreSQL / PgBouncer pool、共享 `http.Transport` 的连接预算、代理容量、文件描述符、CPU、网络、内存中的单任务 payload，以及第三方明确返回的 `429/503` / timeout。
- 默认按依赖维度隔离，不设置一个迁移层全局 goroutine 总闸门，也不添加业务限速 / 限频。一个慢依赖不能把无关任务串行化。
- SQLite 写入直接等待唯一 file owner；PostgreSQL 由 pool 等待和事务超时收口；外部 I/O 由 context 取消与连接预算收口。错误、拒绝和超时必须可观测并原样进入功能结果，不能用无限重试、内存积压或静默降级掩盖。
- 旧 Node 并发字段迁移到对应 Go owner 时逐项审计：只为 event loop 稳定存在的字段直接删除；承载真实资源容量的字段改名为真实资源预算，并补压测证据。
- 禁止以“goroutine 几乎不占内存”为理由启动无 owner、无取消、无超时、无幂等或无观测的工作；这不是业务限速，而是避免资源耗尽和数据错误的正确性边界。

### 4.3 生命周期与共享资源

- 所有请求入口必须创建或继承 `context.Context`，客户端断开、超时、上游取消和服务关闭时必须向下传递取消信号。
- 外部请求使用共享 `http.Client` 和自定义 `Transport`，设置连接池、空闲连接、TLS、代理和超时；禁止每次请求新建无界 client。
- 数据库访问必须受连接池、事务作用域、上下文超时和热点 key 顺序约束。
- PostgreSQL 通过 PgBouncer / pool budget 隔离 server、gateway hot path、management API、ingest、stats 和 ops；慢管理查询、后台批量写和网关热路径不能共用一个无差别池。
- PostgreSQL 写入必须受事务范围、`statement_timeout`、`lock_timeout`、`idle_in_transaction_session_timeout`、批量窗口、分区查询窗口、稳定排序和热点 key 顺序约束；连接必须带 `application_name` 便于定位来源。
- 仅 PostgreSQL/Redis adapter 使用 Redis cache、state；SQLite adapter 默认不依赖 Redis。新 Go 功能不把 Redis / Asynq / Node Redis Streams 当作直接异步执行的默认前提；来源系统已有跨实例运行态契约时，按该功能的完整契约保留。
- W1b-W3 的 Redis queue、Node health bridge、`JUHE_AI_*_API_ENABLED` opt-in、会话鉴权和管理接口切片均只是历史 PG/Redis 灰度记录；它们不构成 B0/L1-L4 的实现前提。现行完整功能不得依赖 Node bridge、历史 queue 或局部 HTTP 切片。
- B0 为每个 profile 定义 Store、直接异步执行和 owner 的启动 fail-fast 条件。
- Node Redis Streams 是当前 Node adapter；它不进入新 Go 功能的默认执行模型。
- 内存 map、LRU、账号并发快照、IP 运行态、会话亲和和短 TTL 状态必须使用 mutex、RWMutex、atomic 或专用并发结构。
- channel 仅可用于功能内部同步，必须有容量和关闭语义；不得作为无限积压的迁移队列。
- 启动的 goroutine 必须属于 server lifecycle、worker lifecycle 或明确任务 context；禁止无 owner 的后台 goroutine。
- 使用 `go test -race` 作为迁移期默认验证项之一。

### 4.4 Go 开发决策与后台并发准则

以下规则是后续 Go 模块、sidecar 和后台任务的固定开发准则，不需要为每个功能重复决策。

1. **先复用项目内能力。** 新实现先检查现有 Store adapter、HTTP client、配置解析、结构化日志、鉴权、分页、超时、owner lease、测试工具和 F1/F2/F3 的已验证模式。相同的领域不允许各自实现另一套签名、重试、数据库连接、并发控制或 schema 初始化；抽出共享能力时只放在明确的基础设施边界，不能造一个模糊的万能 helper。
2. **标准库优先，成熟库次之。** 标准库能够清晰、安全解决的问题直接使用标准库；需要路由、PostgreSQL/SQLite driver、连接池、SQL 生成、指标、测试容器、输入校验或协议实现时，优先采用官网维护或社区成熟、维护活跃、许可证可接受且有清晰 Go 边界的库。先验证项目是否已经选定同类依赖，再评估官方库；只有不存在合适库、库不能满足明确契约或引入成本有证据地超过自建成本时，才允许自建最小实现，并记录原因、替代方案、测试和退出边界。
3. **不为避免 goroutine 而串行化。** Go 的 goroutine 适合大量独立 I/O、独立文件、独立账户、独立上游和独立批次的 fan-out。不得沿用仅为 Node 单事件循环、worker thread、`p-limit` 或 IPC 队列而存在的低并发常量；后台任务应先识别可独立的工作单元，再按资源维度并发执行与汇总。
4. **并发由重要性和真实资源决定。** 实时性强、可独立且依赖尚有容量的任务应优先并发：例如多账户健康检查、多个上游请求、独立日志文件和有界批次。低优先级的历史扫描、离线重建、冷数据清理可保持较小窗口或让位执行。SQLite 同一物理文件始终只有一个 writer；PostgreSQL 受 pool、事务、锁和 statement timeout 约束；外部服务受连接池、代理、文件描述符、payload、`429/503` 和 deadline 约束。它们是正确性和容量边界，不是复刻 Node 的全局低并发闸门。
5. **后台任务必须可停止、可观察、可恢复。** 每个 fan-out 都继承 `context`、deadline 和服务生命周期；任务结果按成功、可重试失败、不可重试失败和取消分别记录。跨重启工作从该功能的持久事实、cursor 或幂等键恢复，不能依赖内存 backlog。重要任务必须记录延迟、失败、积压来源、依赖饱和和实际并发；单条业务失败不得拖垮 listener 或 sidecar，只有 lease 丢失、进程退出或不可恢复基础设施错误才进入组件重启边界。
6. **用证据调整并发。** 不预设小并发，也不把“百万 goroutine 很轻”解读为允许无限提交。扩张或收缩任务窗口时，依据吞吐、P95/P99、错误率、PG pool 等待、锁等待、外部限流、heap、goroutine、FD 和取消延迟作出判断；涉及共享 Store 或外部资源时补压测、race 测试或真实 smoke 证据。

执行顺序固定为：复核现有实现与依赖 -> 选择标准库或已选成熟库 -> 定义任务资源边界与取消语义 -> 实现有界读取后的并发 fan-out -> 运行单元、race、真实依赖和容量验证。禁止因为“Go 很轻量”跳过幂等、事务、锁、超时、观测或测试。

## 5. 内存与大数据边界

- 请求体、响应体、OAuth token 响应、审计 payload、日志文件、导入导出文件继续使用大小上限、stream、cursor、offset 或窗口读取。
- SSE 解析必须增量处理，不为了常规判断拼接完整流。
- 管理列表、日志、审计、使用记录和统计页面不得把全量数据读入内存分页。
- 统计、额度、趋势、TopN 和摘要继续读取 worker 生成的窗口表、summary 表或缓存，不在 API 请求里实时扫描明细。
- pprof 和运行时指标作为 Go 后端标配入口，但公网部署必须有访问控制。`/__aisys__/metrics` 面向外部采集，pprof 面向受控诊断，内部系统监控页面读取 PostgreSQL 窗口表；三者不能互相替代。
- Go 系统监控契约必须使用 goroutine、scheduler latency、GC pause、heap、RSS、PG pool、Redis、直接异步运行数、写入延迟和 stats freshness 等字段；不得把这些指标命名为 Node `eventLoopLagMs`，也不得继续把 `db-service` 作为 Go 长期角色。

Go 运行边界矩阵：

| 入口 | 读取方式 | 边界要求 |
| --- | --- | --- |
| 网关 raw body | 有上限读取，必要时分 lane 识别 | 继承当前 `64mb` 入口硬上限和文本 lane `16mb` 业务上限；认证前可拒绝的请求不得先读大 body |
| 大 JSON 请求 | 有界扫描或流式解析 | 不在请求路径构造无界对象；需要改写时才进入完整解析 |
| SSE | 增量解析和 flush | 不拼接完整流；处理慢客户端 backpressure、半帧事件、客户端断开和上游 cancel |
| OAuth token 响应 | 固定字节上限 | 超限主动中断，错误摘要不得包含敏感 token |
| 审计 payload / 日志文件 | offset / cursor / stream / window | 不完整读入内存分页；只在完整行或完整窗口后推进游标 |
| 导入导出 / 离线迁移 | 离线批处理 | 明确 dry run、批量窗口、失败续跑和报告位置 |

## 6. 未来可评估的 Node 专用复杂度（非 B0 / L1-L4 整体删除清单）

只有未来另立退出决策且不破坏双模式契约时，才可评估下列实现收敛：

- `node:sqlite` 能力预检和 Node 版本分支。
- SQLite standalone、SQLite 多库拆分、usage shard 文件写入、SQLite read worker、SQLite writer owner 和相关测试矩阵：**B0 / L1-L4 不整体删除**，它们是 SQLite profile 的现行边界；只有已接管完整功能的专属 Node 文件可在 L4 归档。
- DB service HTTP/IPC 代理层：**B0 / L1-L4 不整体删除**，SQLite profile 继续通过它保持单 writer / typed command 正确性；除非其已成为已接管完整功能的唯一专属实现。
- Node 专属系统指标：`eventLoopLagMs`、`process_event_loop_*`、V8 `processHeap*` / `external` / `arrayBuffers`、DB service 运行态、SQLite 文件体积、usage shard 文件路径和 IPC pending 队列指标。
- worker thread 大 JSON 解析边界，改为 Go 请求 goroutine + 有界解析策略。
- `p-limit` 等为 Node 并发协调补出来的通用胶水，不作为 Go 的通用队列或业务限流复刻；Go 以 context、连接池、SQLite owner 和直接 goroutine 生命周期保证正确性。
- `tsx` 开发运行链路和后端 TypeScript 编译链路。
- Node worker role 中只为事件循环隔离存在的 IPC pending 防护。
- 历史 W1b 临时 Go dispatch adapter、Node internal route 和两个 `JUHE_AI_NODE_INTERNAL_*` 配置；不得把它们作为完整功能接管后的长期 fallback。

不能删除的真实业务约束：

- PostgreSQL 连接池、事务隔离、锁等待、索引和批量写窗口要求。
- Redis cache / state 的 TTL、容量和降级要求；历史 Node 队列仍按其原有契约维护，不能被新 Go 功能静默接管或替换。
- 上游账号并发、代理质量、账号冷却、短 TTL 屏蔽和来源保护。
- 使用记录、审计、操作日志、运行日志和统计聚合的异步写入边界。
- 请求体大小、SSE backpressure、客户端断开和上游取消处理。
- 敏感字段加密、脱敏和权限控制。
- 系统可观测性边界：health 只判断当前依赖可用性，Prometheus 负责实时采集，pprof 负责诊断，系统监控 API 负责管理页面窗口趋势；所有指标 label 必须低基数且不含敏感信息。
