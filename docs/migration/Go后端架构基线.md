# Go 后端架构基线

## 1. 技术基线

- Go 版本：以正式落代码时 `go.dev/dl/` 的最新稳定 Go 版本为准；文档阶段不固定补丁号。
- HTTP 基础：优先使用标准库 `net/http`，路由层使用轻量 `go-chi/chi/v5`。不引入 Gin、Fiber 这类更重的框架，避免把问题转移到框架约定。
- JSON：默认使用标准库 `encoding/json`。只有压测证明 JSON 编解码成为瓶颈时，才评估替代库，并先写报告。
- 日志：使用标准库 `log/slog`，统一结构化字段、trace ID、请求 ID、模块名和错误摘要。
- 配置：使用项目内 `internal/config` 读取环境变量和 `.env`，不默认引入 Viper 这类重型配置框架。
- PostgreSQL：使用 `pgx/v5`，通过连接池、事务函数和上下文超时收口。
- Redis：使用 `redis/go-redis/v9`，区分 cache、state 和 queue 连接配置。
- SQLite：Go 后端不引入 SQLite driver，不提供 standalone 模式，不维护 SQLite schema、adapter 或测试矩阵。
- 校验：优先在 DTO 层手写小型校验和中文错误，不默认引入大型 validator。重复校验稳定后再抽通用 helper。
- 测试：使用 Go 标准 `testing`、`httptest`、`go test ./... -race`、基准测试和必要的 mock upstream；跨服务依赖测试通过 Docker / 本地服务环境明确触发。

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
    store/
      port/
      postgres/
      redis/
    jobs/
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
- `internal/store/port/` 定义业务语义接口；PostgreSQL 和 Redis 只在 adapter / 基础设施层出现。
- `internal/jobs/` 放后台任务角色，禁止 HTTP route 直接启动长期任务。
- `internal/protocols/` 放协议适配和桥接，不把 OpenAI / Anthropic / Gemini 字段路径写散到 gateway service。
- `internal/runtime/` 放短 TTL 运行态、并发占用和缓存版本，所有 map 必须有锁或使用并发安全结构。

## 3. 进程模型

Go 目标不是复制当前 Node 进程树。

- 主 server：承载系统 API、公开 API、静态资源、网关入口、健康检查和必要 supervisor。
- worker：保留 `ingest`、`stats`、`ops` 三类角色的业务边界，但不再因为 Node 事件循环阻塞而拆出额外 DB service。
- maintenance：生产维护脚本以独立命令运行，必须明确 dry run、影响范围和失败行为。
- DB service：迁移完成后删除。Go 后端直接通过 PostgreSQL 连接池、事务、Redis state/cache/queue 和有界后台队列表达存储边界，不再为 SQLite 单写者保留独立进程。

## 4. 并发与线程安全

Go 解决的是 Node 单事件循环问题，不代表可以无界并发。

- 所有请求入口必须创建或继承 `context.Context`，客户端断开、超时、上游取消和服务关闭时必须向下传递取消信号。
- 外部请求使用共享 `http.Client` 和自定义 `Transport`，设置连接池、空闲连接、TLS、代理和超时；禁止每次请求新建无界 client。
- 数据库访问必须受连接池、事务作用域、上下文超时和热点 key 顺序约束。
- PostgreSQL 写入必须受连接池、事务范围、锁等待、批量窗口和热点 key 顺序约束；Redis 队列必须受 stream 长度、consumer group、重试和死信策略约束。
- 内存 map、LRU、账号并发快照、IP 运行态、会话亲和和短 TTL 状态必须使用 mutex、RWMutex、atomic 或专用并发结构。
- channel 必须有容量和关闭语义；后台队列必须定义满载时拒绝、合并、丢弃或降级策略。
- 启动的 goroutine 必须属于 server lifecycle、worker lifecycle 或明确任务 context；禁止无 owner 的后台 goroutine。
- 使用 `go test -race` 作为迁移期默认验证项之一。

## 5. 内存与大数据边界

- 请求体、响应体、OAuth token 响应、审计 payload、日志文件、导入导出文件继续使用大小上限、stream、cursor、offset 或窗口读取。
- SSE 解析必须增量处理，不为了常规判断拼接完整流。
- 管理列表、日志、审计、使用记录和统计页面不得把全量数据读入内存分页。
- 统计、额度、趋势、TopN 和摘要继续读取 worker 生成的窗口表、summary 表或缓存，不在 API 请求里实时扫描明细。
- pprof 和运行时指标作为 Go 后端标配入口，但公网部署必须有访问控制。

## 6. 可删除的 Node 专用复杂度

迁移完成后应删除或收敛：

- `node:sqlite` 能力预检和 Node 版本分支。
- SQLite standalone、SQLite 多库拆分、usage shard 文件写入、SQLite read worker、SQLite writer owner 和相关测试矩阵。
- 因事件循环阻塞而存在的 DB service HTTP/IPC 代理层。
- worker thread 大 JSON 解析边界，改为 Go 请求 goroutine + 有界解析策略。
- `p-limit` 等为 Node 并发协调补出来的通用胶水，改为 context、semaphore、channel 或连接池。
- `tsx` 开发运行链路和后端 TypeScript 编译链路。
- Node worker role 中只为事件循环隔离存在的 IPC pending 防护。

不能删除的真实业务约束：

- PostgreSQL 连接池、事务隔离、锁等待、索引和批量写窗口要求。
- Redis cache / state / queue 的 TTL、容量、consumer group、重试和降级要求。
- 上游账号并发、代理质量、账号冷却、短 TTL 屏蔽和来源保护。
- 使用记录、审计、操作日志、运行日志和统计聚合的异步写入边界。
- 请求体大小、SSE backpressure、客户端断开和上游取消处理。
- 敏感字段加密、脱敏和权限控制。
