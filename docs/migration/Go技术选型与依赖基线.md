# Go 技术选型与依赖基线

> 面向 Go 后端迁移执行者。
> 本文固定 Go 迁移期默认依赖、禁用依赖、评估门禁和封装规则。后续新增 Go 模块不得临时自选框架或把第三方库类型暴露到业务层。

## 1. 选型原则

- 长期维护优先：选择社区成熟、接口稳定、文档清晰、许可证可接受、和 Go 标准库配合好的库。
- Go 原生优先：能用标准库稳定解决的问题，不为“方便”引入重型框架。
- 不手写通用基础设施：路由、配置解析、SQL 类型生成、数据库迁移、Redis 客户端、异步任务、测试容器、指标和安全扫描使用成熟工具。
- 不复制 Node 复杂度：不把事件循环规避、SQLite 单写者、IPC pending、本地队列胶水迁到 Go。
- 依赖不穿透业务：第三方库只允许出现在 `internal/platform/`、`internal/store/`、`internal/jobs/`、`internal/httpapi/`、`internal/testkit/` 等基础设施层；模块 service 和业务 port 使用项目自定义类型。
- 每个依赖必须有替换边界：外部库升级、替换或下线时，只改 adapter / platform 层，不改业务模块。

## 2. 默认依赖清单

| 领域 | 默认选择 | 用途 | 边界 |
| --- | --- | --- | --- |
| Go 版本 | 正式落代码时 `go.dev/dl/` 最新稳定版；当前参考 `go1.26.x` | 编译、测试和发布基线 | `go.mod` 固定主次版本；补丁版本按安全和 CI 结果升级 |
| HTTP server | 标准库 `net/http` | HTTP server、client、SSE、反向代理基础能力 | 网关流式、取消、backpressure 和 header 过滤直接基于标准库 |
| Router | `github.com/go-chi/chi/v5` | 管理 API、公开 API、健康检查、路由分组和中间件 | 只在 `internal/httpapi` 注册路由；业务模块不直接依赖 chi context |
| ResponseWriter 包装 | `github.com/felixge/httpsnoop` | HTTP response capture、保留 `Unwrap` / `Flush` / `Hijack` / `ReadFrom` / `Push` 等可选接口组合 | 只允许在 `internal/httpapi` 等 HTTP 基础设施层使用；不得让业务 handler 依赖其类型 |
| CLI | `github.com/spf13/cobra` | `juhe-ai server`、`juhe-ai-worker`、`juhe-ai-maintenance`、离线维护命令 | 不引入 Viper；配置仍由 env 结构解析 |
| 配置 | `github.com/caarlos0/env/v11` + `github.com/joho/godotenv` | 环境变量解析、默认值、必填校验、duration / URL 类型；本地 / 测试 `.env` 加载 | 统一封装在 `internal/config`；生产只读环境变量，godotenv 只允许在开发 / 测试入口加载 |
| 日志 | 标准库 `log/slog` | JSON 结构化日志、trace ID、request ID、模块名、错误摘要 | 不首批引入 zap / zerolog；性能不足必须先有报告 |
| Request ID | `github.com/google/uuid` | 没有上游 `X-Request-Id` 时生成请求 ID | 只在 HTTP 中间件使用；业务模块不直接依赖 |
| 校验 | `github.com/go-playground/validator/v10` + 手写业务校验 | DTO 形状校验、字段格式、范围检查 | 错误转换为项目中文错误结构；跨字段业务规则仍写 service 校验 |
| PostgreSQL driver | `github.com/jackc/pgx/v5` + `pgxpool` | PostgreSQL 连接池、事务、批量写、COPY、LISTEN / NOTIFY 预留 | 不用 `database/sql` 作为主边界；连接池按 server / gateway / worker 隔离 |
| SQL 生成 | `sqlc` | 从 SQL 生成类型安全 Go 查询代码 | 不引入 GORM / ent；复杂查询保留显式 SQL 和 review 能力 |
| Schema 迁移 | `github.com/pressly/goose/v3` CLI | PostgreSQL DDL、seed、离线迁移执行 | 不在 Go 启动路径自动迁移；生产迁移由维护命令或发布流程显式执行 |
| Redis client | `github.com/redis/go-redis/v9` | cache、state、counter、fixed-window / penalty-window 限频、Redis 连接与 pipeline | queue 通过 Asynq；业务代码不直接拼 Redis key |
| Job / queue | `github.com/hibiken/asynq` | 异步任务、重试、延迟任务、worker crash 恢复、队列指标 | 只通过 `internal/jobs/queue` Port 使用；不用项目自研 Redis Streams 通用队列 |
| 指标 | `github.com/prometheus/client_golang` | HTTP、PG、Redis、worker、队列、网关指标 | `/metrics` 必须受部署边界保护；不首批引入 Prometheus API client |
| pprof | 标准库 `net/http/pprof` | CPU、heap、goroutine、block profile | 只能挂在受保护的 debug/admin 入口 |
| OAuth | `golang.org/x/oauth2` | OpenAI OAuth token 交换 / 刷新辅助 | HTTP client、代理、超时和响应上限仍由项目封装控制 |
| 测试断言 | `github.com/stretchr/testify/require` | 测试断言和失败中止 | 不默认使用 testify mock；优先手写 fake / testkit |
| 集成测试容器 | `github.com/testcontainers/testcontainers-go` | PostgreSQL、Redis、mock dependency 的集成测试环境 | 只在 `-tags=integration` 或专项 smoke 中使用 |
| Goroutine 泄漏检查 | `go.uber.org/goleak` | worker、网关、SSE 和取消测试的泄漏检查 | 只在适合的包测试中启用，必须排除标准库已知后台 goroutine |
| Lint | `golangci-lint` | go vet、staticcheck、errcheck、bodyclose、copylocks 等统一检查 | 配置从小集合开始，避免一开始引入大量风格噪音 |
| 安全扫描 | `govulncheck` | Go 依赖和调用链漏洞扫描 | 发布前和依赖升级时执行 |

## 3. Job 与队列决策

Go 目标不再手写通用 Redis Streams 队列。默认采用 Asynq 作为 Redis-backed task queue：

- 适合当前项目的使用记录、审计、操作日志、公开接口日志、运行日志索引、账号探测、OAuth 保活、统计刷新和维护清理。
- 由 Asynq 承接任务入队、worker 并发、失败重试、延迟任务、周期任务、worker crash 恢复、暂停队列、队列指标和管理检查。
- Redis queue 仍必须独立于 cache / state，生产建议 `noeviction + AOF`，不能和可淘汰缓存共用淘汰策略。
- 任务 payload 必须有版本号、幂等 key、trace ID、创建时间、来源模块和最小必要字段；不得把完整敏感 payload 放进任务体。
- 任务 handler 成功写入 PostgreSQL 或完成业务副作用后才返回成功；失败由 Asynq 重试或进入死信 / archived 状态。
- 网关已经可返回响应时，副作用入队失败必须记录结构化错误和指标；是否中断请求由该副作用的重要性在功能文档中固定。

Asynq 当前仍是 `v0.x` 公共 API，存在破坏性变更风险。因此必须做到：

- Asynq 类型不得穿透到业务 service、store port 或模块 DTO；生产代码只允许 `internal/jobs/queue` 和 `internal/jobs/worker` import Asynq。
- `internal/jobs/queue` 封装 enqueue、task type、retry、deadline、unique key、explicit Redis timeout、metrics、inspector 和 smoke。
- W0 阶段必须做最小 PoC：入队、消费、重试、dead / archived、worker crash 恢复、优雅关闭、指标和 Redis 持久化边界。当前已落地入队、消费、inspector、pending smoke、retry / archive / retry exhaustion integration、W1b public API log handler 和真实 worker runtime smoke；periodic、crash recover、长任务 drain、队列指标和 Redis 持久化演练仍不能视为完成。
- 如果 PoC 证明 Asynq 无法满足稳定性或 Redis 部署边界，才能在 W0 决策中改为 `riverqueue/river` 或直接 `go-redis` Streams adapter；不能在业务迁移中途混用两套通用队列。

## 4. 不默认引入的依赖

| 依赖 / 类型 | 默认结论 | 原因 |
| --- | --- | --- |
| Gin / Echo / Fiber | 不引入 | 项目需要精确控制 `net/http`、SSE、反压、取消和 header；chi 更轻且与标准库兼容 |
| GORM / ent | 不引入 | 网关、统计和权限查询需要显式 SQL、索引和窗口控制；sqlc 更利于审查和性能治理 |
| Viper | 不引入 | 当前配置以 env 为主；Viper 会扩大配置来源和优先级复杂度 |
| zap / zerolog | 暂不引入 | `slog` 已满足结构化日志；性能不足先用压测证明 |
| Temporal / NATS / Kafka / RabbitMQ | 暂不引入 | 当前目标是单机到小规模部署，PG + Redis 已是正式依赖；不扩大部署面 |
| River | 首批不引入 | River 更适合 PostgreSQL transactional jobs；当前队列优先降低网关热路径对 PG 的写入耦合。未来需要 DB 事务内 job 时再评估 |
| robfig/cron | 首批不引入 | 周期任务先使用 Asynq periodic task；Asynq 不满足时再单独评估 |
| generic retry HTTP client | 不引入 | 上游模型请求、SSE 和账号调度重试必须按业务语义控制，不能交给通用 retry 包 |
| generic SSE library | 不引入 | OpenAI / Responses / Anthropic / Gemini 事件语义差异大，SSE 解析需要协议专项测试 |
| OpenTelemetry 全套 collector | 暂不引入 | 首批用 slog + Prometheus + pprof；多服务链路追踪需求明确后再评估 |

## 5. 依赖使用规则

- 新增依赖前必须更新本文，说明用途、替代方案、许可证、封装层、验证命令和退出条件。
- 所有依赖必须进入 `go.mod`，执行 `go mod tidy` 后提交；不允许把临时工具库留在运行依赖里。
- 运行依赖和开发工具依赖分开管理；CLI 工具可通过 `tools.go` 或文档化安装命令固定版本。
- 业务模块禁止 import `pgx`、`redis`、`asynq`、`chi`、`validator`、`cobra`、`prometheus` 等第三方库。
- 第三方库错误必须转换为项目错误类型和中文摘要；日志中记录库名、操作、trace ID 和安全摘要，不泄露凭据。
- 依赖升级必须至少执行 `go test ./...`、`go test ./... -race`、`go vet ./...`、`golangci-lint run` 和 `govulncheck ./...`；涉及 PG/Redis/queue 的升级还要跑 integration。

## 6. W0 依赖 PoC 门禁

W0 不能只创建空 Go 工程，必须证明这些依赖能在本项目边界下工作：

| 项 | 必须证明 |
| --- | --- |
| HTTP / chi | `server` 可注册 `__aisys__`、`__aipublic__`、health 和未来 gateway catch-all，路径不互抢 |
| config / cobra | Windows PowerShell、Linux 和 macOS 下命令、env、默认值、必填错误一致 |
| slog | JSON 日志包含 trace ID、request ID、模块、错误摘要；敏感字段不会落日志 |
| pgx / sqlc | 新库初始化、事务、唯一约束、分页稳定排序、批量写和超时可测 |
| goose | DDL / seed 能离线执行；启动路径不会自动迁移 |
| go-redis | cache / state / counter namespace 隔离，TTL、pipeline、连接超时可测 |
| Asynq | enqueue、consume、retry、dead / archived、periodic task、worker crash recover、shutdown drain 和指标可测 |
| Prometheus / pprof | 指标和 profile 入口可访问且能被部署边界保护 |
| testcontainers | `-tags=integration` 能启动 PostgreSQL / Redis 并清理资源 |
| lint / vuln | lint、govulncheck、race 的命令能在 CI 或本地稳定执行 |

## 7. 当前 W0 落地版本

W0 当前只表示工程和基础设施 PoC 的版本基线；后续升级依赖必须更新本表并重跑验证矩阵。

| 类型 | 当前版本 / 模块 | 验证状态 |
| --- | --- | --- |
| Go | `go1.26.4 windows/amd64` | `go test ./...`、`go test -race ./...`、`go vet ./...` 已通过 |
| Windows C 工具链 | w64devkit `2.8.0`，GCC `16.1.0` | 用于 Windows race，已替换旧 MinGW race 失败路径 |
| Router | `github.com/go-chi/chi/v5 v5.3.0` | W0 health / metrics 路由已通过 smoke |
| ResponseWriter 包装 | `github.com/felixge/httpsnoop v1.0.4` | W1b HTTP shell / capture 用于保留底层 writer 可选接口组合，已覆盖 499、`Unwrap` / `ResponseController.Flush`、`Flush` 和 `ReadFrom` 契约测试 |
| CLI | `github.com/spf13/cobra v1.10.2` | server / worker / maintenance `version` 命令已通过 |
| 配置 | `github.com/caarlos0/env/v11 v11.4.1` + `github.com/joho/godotenv v1.5.1` | 配置单测已通过 |
| Request ID | `github.com/google/uuid v1.6.0` | HTTP request ID 中间件已落地 |
| PostgreSQL | `github.com/jackc/pgx/v5 v5.10.0` | health adapter、goose baseline / W1a / W1b migration、sqlc catalog / W1b auth-log 查询已落地；integration 用例已补，当前主线程 Docker 不可用时 `SKIP`，需 Docker 环境复跑 |
| Redis | `github.com/redis/go-redis/v9 v9.21.0` | cache / state health adapter、namespace key 封装、TTL set、pipeline、`IncrWithTTL` 原子计数、fixed-window 和 W1b penalty-window Lua 限频、Redis DB 去重校验已落地；W1b penalty-window integration 用例已补，需 Docker 环境复跑 |
| Job / queue | `github.com/hibiken/asynq v0.26.0` | Asynq client ping health、`rediss` TLS、显式 Redis timeout、enqueue 封装、inspector、pending smoke、retry / archive / retry exhaustion integration 代码已落地；W1b `public-api-log:write` payload/enqueue/handler、`juhe-ai-worker ingest` runtime、invalid payload `SkipRetry` 映射已补；真实 Redis/Asynq/PG smoke 当前主线程 Docker 不可用时 `SKIP`，需 Docker 环境复跑；periodic / crash recover / 长任务 drain / 队列指标待补 |
| 指标 | `github.com/prometheus/client_golang v1.23.2` | `/__aisys__/metrics` loopback smoke 已通过 |
| SQL CLI | `sqlc v1.31.1` | CLI 已安装，已生成 `internal/store/postgres/postgresqueries` |
| Migration CLI / lib | `github.com/pressly/goose/v3 v3.27.2` | CLI 已安装；integration 测试通过 goose 执行 baseline DDL |
| Testcontainers | `github.com/testcontainers/testcontainers-go v0.43.0` | `go test -tags=integration ./internal/testkit/integration -count=1` 当前主线程因 Docker 不可用输出 `SKIP`；Docker/testcontainers 健康环境必须复跑，覆盖 PostgreSQL / Redis / Asynq / W1a / W1b foundation / W1b public API log queue / public group / public route strategy / public API Key / public account smoke 与 shell E2E |
| Lint | `golangci-lint 2.12.2` | `0 issues` |
| 安全扫描 | `govulncheck v1.5.0` | 无已调用漏洞 |

当前目标依赖中，`validator`、`oauth2` 和 `goleak` 仍未进入实际代码路径；等对应 DTO 校验、OAuth 或 goroutine 泄漏测试落地时再加入 `go.mod` 并更新本表。

## 8. 参考源

- Go 下载页：<https://go.dev/dl/>
- w64devkit：<https://github.com/skeeto/w64devkit>
- chi：<https://github.com/go-chi/chi>
- httpsnoop：<https://github.com/felixge/httpsnoop>
- pgx：<https://github.com/jackc/pgx>
- sqlc：<https://docs.sqlc.dev/>
- goose：<https://github.com/pressly/goose>
- go-redis：<https://github.com/redis/go-redis>
- Asynq：<https://github.com/hibiken/asynq>
- caarlos0/env：<https://github.com/caarlos0/env>
- godotenv：<https://github.com/joho/godotenv>
- Cobra：<https://github.com/spf13/cobra>
- Prometheus Go client：<https://github.com/prometheus/client_golang>
- Testcontainers for Go：<https://golang.testcontainers.org/>
- govulncheck：<https://go.dev/doc/tutorial/govulncheck>
- golangci-lint：<https://github.com/golangci/golangci-lint>
