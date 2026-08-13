# Go 技术选型与依赖基线

> 面向 Go 后端迁移执行者。
> 本文固定 Go 迁移期默认依赖、禁用依赖、评估门禁和封装规则。后续新增 Go 模块不得临时自选框架或把第三方库类型暴露到业务层。

> **2026-08-14 现行边界。** `backend-go/go.work` 管理独立的 `gateway`、`jobs`、`maintenance` 模块，F1/F2 在 `jobs`，F3/F4 在 `gateway`；本文件中的早期依赖版本和“已落地”段落若与当前代码冲突，只作历史记录。Go 功能默认直接异步执行，不以 Redis、Asynq 或通用 Queue Port 为前置；每个功能仍须分别验证 SQLite 与 PostgreSQL/Redis Store adapter、goroutine 生命周期和资源维度并发。详见 [完整功能接管与 Node 归档迁移规则](完整功能接管与Node归档迁移规则.md)。

## 1. 选型原则

- 长期维护优先：选择社区成熟、接口稳定、文档清晰、许可证可接受、和 Go 标准库配合好的库。
- Go 原生优先：能用标准库稳定解决的问题，不为“方便”引入重型框架。
- 不手写通用基础设施：路由、配置解析、SQL 类型生成、数据库迁移、Redis 客户端、测试容器、指标和安全扫描使用成熟工具；直接异步执行使用 Go 标准并发原语，不引入任务队列。
- 不复制 Node 业务无关的事件循环规避；但 SQLite 单写者与 owner bridge 是 SQLite 模式的正确性边界，Go 必须适配而非绕过。
- 不复制 Node 补偿代码：旧数据、旧字段、旧状态或历史部署不符合当前契约时，Go 运行路径默认 fail-fast / 明确报错；不新增长期 fallback、自动补偿、静默修复或双结构兼容。确需修复历史数据时只记录离线 SQL、离线脚本或重建步骤。
- 善用 Go 语言能力：泛型、接口组合、反射、代码生成和模板化测试可以用于收敛重复 DTO 映射、字段校验、响应 envelope、option 解析和 store adapter 样板；使用前必须保证可读、可测、错误显式，且第三方库 / 反射细节不穿透业务 service。
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
| PostgreSQL driver | B0 后固定（候选 `github.com/jackc/pgx/v5` + `pgxpool`） | PostgreSQL 连接池、事务、批量写、COPY、LISTEN / NOTIFY 预留 | 仅 PostgreSQL/Redis 模式使用；连接池按 server / gateway / worker 隔离 |
| SQLite driver | B0 后固定 | SQLite Store adapter 与受控 owner bridge / handoff | 不能让 Go 与 Node 并行写同一文件；SQLite 模式不依赖 Redis |
| SQL 生成 | `sqlc` | 从 SQL 生成类型安全 Go 查询代码 | 不引入 GORM / ent；复杂查询保留显式 SQL 和 review 能力 |
| Schema 迁移 | `github.com/pressly/goose/v3` CLI | PostgreSQL DDL、seed、离线迁移执行 | 不在 Go 启动路径自动迁移；生产迁移由维护命令或发布流程显式执行 |
| Redis client | PostgreSQL/Redis adapter 的可选依赖 `github.com/redis/go-redis/v9` | cache、state、counter 和既有跨实例运行态 | SQLite profile 不依赖它；新 Go 功能默认不使用 Redis 队列 |
| 直接异步执行 | Go 标准库 goroutine、`context`、`errgroup` 等价实现 | 功能内 fan-out、取消、结果汇总和按资源维度并发 | 不引入通用队列或业务限速；SQLite 单 writer、PG pool 和外部 I/O 预算仍必须保留 |
| Job / queue | 本轮不引入 | 直接异步工作必须直接提交 Store，并从持久事实重新发现未完成工作 | Asynq / Redis Streams 仅是 Node 历史实现或历史规划，不能作为新 Go 功能依赖 |
| 指标 | 标准库 `runtime/metrics` + `github.com/prometheus/client_golang` | Go runtime、HTTP、PG、Redis、直接异步任务、网关指标 | `/metrics` 必须受部署边界保护；内部系统监控读取 PG 窗口表；不首批引入 Prometheus API client |
| pprof | 标准库 `net/http/pprof` | CPU、heap、goroutine、block profile | 只能挂在受保护的 debug/admin 入口 |
| OAuth | `golang.org/x/oauth2` | OpenAI OAuth token 交换 / 刷新辅助 | HTTP client、代理、超时和响应上限仍由项目封装控制 |
| 测试断言 | `github.com/stretchr/testify/require` | 测试断言和失败中止 | 不默认使用 testify mock；优先手写 fake / testkit |
| 集成测试容器 | `github.com/testcontainers/testcontainers-go` | PostgreSQL、Redis、mock dependency 的集成测试环境 | 只在 `-tags=integration` 或专项 smoke 中使用 |
| Goroutine 泄漏检查 | `go.uber.org/goleak` | worker、网关、SSE 和取消测试的泄漏检查 | 只在适合的包测试中启用，必须排除标准库已知后台 goroutine |
| Lint | `golangci-lint` | go vet、staticcheck、errcheck、bodyclose、copylocks 等统一检查 | 配置从小集合开始，避免一开始引入大量风格噪音 |
| 安全扫描 | `govulncheck` | Go 依赖和调用链漏洞扫描 | 发布前和依赖升级时执行 |

## 3. 直接异步与完整功能恢复边界

当前没有 Go Queue 实现，本轮也不引入持久任务 adapter。B0 先固定直接异步执行契约：输入版本、幂等键、context 取消、错误归因、结果汇总、资源维度并发和重启后的重新发现。任何完整功能若不能从自身持久事实直接恢复，就不能以裸 goroutine 接管，必须延后，而不是新增队列：

- **SQLite adapter**：不要求 Redis；直接调用唯一 file owner。需要跨进程恢复时由完整功能的持久事实重新发现，不能用本地内存队列替代。
- **PostgreSQL/Redis adapter**：直接访问 PostgreSQL pool；Redis 只在该完整功能已有跨实例运行态契约时启用，不承担新 Go 任务队列。
- 直接异步 handler 必须保留最小输入、幂等 key、trace ID、创建时间和来源模块；Store 提交失败必须返回原始错误，不能静默成功。
- 不得以未来可能增加 Asynq 为理由提前引入第三方队列类型、Redis task key 或 queue 配置；本轮的重启恢复必须可由 Store 状态、cursor 或幂等键审计。

## 4. 不默认引入的依赖

| 依赖 / 类型 | 默认结论 | 原因 |
| --- | --- | --- |
| Gin / Echo / Fiber | 不引入 | 项目需要精确控制 `net/http`、SSE、反压、取消和 header；chi 更轻且与标准库兼容 |
| GORM / ent | 不引入 | 网关、统计和权限查询需要显式 SQL、索引和窗口控制；sqlc 更利于审查和性能治理 |
| Viper | 不引入 | 当前配置以 env 为主；Viper 会扩大配置来源和优先级复杂度 |
| zap / zerolog | 暂不引入 | `slog` 已满足结构化日志；性能不足先用压测证明 |
| Temporal / NATS / Kafka / RabbitMQ | 暂不引入 | 当前目标是单机到小规模部署，PG + Redis 已是正式依赖；不扩大部署面 |
| River | 不引入 | River 会引入 PostgreSQL transactional job 队列，与本轮直接异步、不新增任务队列的决定冲突 |
| robfig/cron | 不引入 | 完整功能的周期触发由生命周期内直接调用 / scheduler 表达；确有 durable schedule 时另立契约，不以 cron 或 Asynq 作为默认 |
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

## 6. B0 双模式依赖基线门禁

B0 不能只创建空 Go 工程，必须证明两个 profile 的 adapter 能在本项目边界下工作：

| 项 | 必须证明 |
| --- | --- |
| HTTP / chi | `server` 可注册 `__aisys__`、`__aipublic__`、health 和未来 gateway catch-all，路径不互抢 |
| config / cobra | Windows PowerShell、Linux 和 macOS 下命令、env、默认值、必填错误一致 |
| slog | JSON 日志包含 trace ID、request ID、模块、错误摘要；敏感字段不会落日志 |
| pgx / sqlc | 新库初始化、事务、唯一约束、分页稳定排序、批量写和超时可测 |
| goose | DDL / seed 能离线执行；启动路径不会自动迁移 |
| SQLite owner bridge | 本地唯一 writer / owner bridge 的直接提交、取消、失败可见、单 writer 与恢复重新发现语义可测 |
| 直接异步执行 | 两个 profile：goroutine 生命周期、context 取消、资源维度并发、SQLite 单 writer、PG 事务与恢复重新发现可测 |
| Go 任务队列 | 本轮不评估、不引入；任何未来变更都需要用户重新决策 | B0 只验证 Store、直接异步执行和持久事实恢复，不得以 durable queue 取代这些验证 |
| Prometheus / pprof | 指标和 profile 入口可访问且能被部署边界保护 |
| testcontainers | `-tags=integration` 能启动 PostgreSQL / Redis 并清理资源 |
| lint / vuln | lint、govulncheck、race 的命令能在 CI 或本地稳定执行 |

## 7. 历史 W0 依赖记录（不表示当前已落地）

> 下表保留 2026-08-08 前 PG/Redis-only 方案的依赖与验证记录。该历史快照当时尚无受版本控制的 Go 源码或 `go.mod`；当前事实以本文顶部 2026-08-12 现行边界为准，不能将下表视为当前依赖或双模式验收结果。

| 类型 | 当前版本 / 模块 | 验证状态 |
| --- | --- | --- |
| Go | `go1.26.5 windows/amd64` | `go test ./...`、`go test -race ./...`、`go vet ./...` 已通过 |
| Windows C 工具链 | w64devkit `2.8.0`，GCC `16.1.0` | 用于 Windows race，已替换旧 MinGW race 失败路径 |
| Router | `github.com/go-chi/chi/v5 v5.3.0` | W0 health / metrics 路由已通过 smoke |
| ResponseWriter 包装 | `github.com/felixge/httpsnoop v1.0.4` | W1b HTTP shell / capture 用于保留底层 writer 可选接口组合，已覆盖 499、`Unwrap` / `ResponseController.Flush`、`Flush` 和 `ReadFrom` 契约测试 |
| CLI | `github.com/spf13/cobra v1.10.2` | server / worker / maintenance `version` 命令已通过 |
| 配置 | `github.com/caarlos0/env/v11 v11.4.1` + `github.com/joho/godotenv v1.5.1` | 配置单测已通过 |
| Request ID | `github.com/google/uuid v1.6.0` | HTTP request ID 中间件已落地 |
| PostgreSQL | `github.com/jackc/pgx/v5 v5.10.0` | health adapter、goose baseline / W1a / W1b migration、sqlc catalog / W1b auth-log 查询已落地；integration 用例已补，当前主线程 Docker 不可用时 `SKIP`，需 Docker 环境复跑 |
| Redis | `github.com/redis/go-redis/v9 v9.21.0` | 历史 PostgreSQL/Redis adapter 候选记录；不适用于 SQLite profile，P0 需重新验证 |
| Job / queue | `github.com/hibiken/asynq v0.26.0` | 历史 PostgreSQL/Redis adapter 候选记录；不表示当前直接异步架构会引入该依赖，B0 仅验证 SQLite / PostgreSQL adapter 与 goroutine 生命周期 |
| 指标 | 标准库 `runtime/metrics` + `github.com/prometheus/client_golang v1.23.2` | `/__aisys__/metrics` loopback smoke 已通过；Go runtime / PG / Redis / Asynq / worker lag 的完整采样口径见 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)，后续 W6/W7 落地 |
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
