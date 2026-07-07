# PLAN-0081 Node 转 Go 渐进减法迁移

## 基本信息

- 编号：PLAN-0081
- 状态：进行中
- 创建时间：2026-07-06
- 更新时间：2026-07-08
- 需求来源：用户对话
- 执行者：AI / 维护者
- 关联模块：后端 / 存储 / 网关 / 后台 worker / 公开接口 / 管理接口 / 部署 / 文档 / 验证

## 需求目标

- 背景：当前 Node 后端为了处理并发、阻塞、事件循环、SQLite 写锁、IPC 和后台任务隔离，已经积累了大量复杂机制。用户已明确决定彻底抛弃 Node 后端，转向 Go，并彻底去掉 SQLite，不再维护 standalone / performance 两套模式。
- 目标：通过渐进式、减法式迁移，把后端逐步迁移到 Go + PostgreSQL + Redis；迁移一个模块就删除一个 Node 旧模块和对应 SQLite / 双模式旧路径，最终只保留前端构建所需的 Node 工具链。当前已完成 M0 文档基线；W0 Go 工程与 PG/Redis/Asynq 常规基线已落地并通过本地 Go 非容器验证矩阵；W1a `GET /__aisys__/api/settings/public` 已进入 Go 实现中，未生产接管；W1b `/__aipublic__` 已补 Go catalog/auth、PostgreSQL auth/log adapter、Redis penalty-window、公开接口日志 snapshot/log builder、Asynq payload/enqueue/handler、`juhe-ai-worker ingest` 日志消费 runtime、HTTP shell / capture 契约、499 客户端提前断开捕获、ResponseWriter 透传、public group CRUD、public route strategy CRUD、public API Key CRUD、public account CRUD 四类资源 16 个 CRUD 纵切面、对应 integration / shell 测试代码、`JUHE_AI_PUBLIC_API_ENABLED=false` 默认关闭的生产 router opt-in guard，以及 `w1b-public-api-smoke` 独立灰度验证入口；account shell E2E 测试代码已补，当前主线程 Docker 不可用，真实 PG/Redis/Asynq integration、`w1b-public-api-smoke` 真实依赖执行与四类 shell E2E 待复跑，反向代理切流和 Node 删除仍未完成，未正式生产接管；W2 已补 `GET /__aisys__/api/proxies/options`、`GET /__aisys__/api/providers/options`、`GET /__aisys__/api/providers/models/options`、`GET /__aisys__/api/providers/{code}/models`、`GET /__aisys__/api/route-strategies/options`、`GET /__aisys__/api/my-route-strategies/options`、`GET /__aisys__/api/groups/options`、`GET /__aisys__/api/my-groups/options`、`GET /__aisys__/api/groups/account-options`、`GET /__aisys__/api/my-groups/account-options`、`GET /__aisys__/api/accounts/options`、`GET /__aisys__/api/my-accounts/options`、`GET /__aisys__/api/accounts/tags` 和 `GET /__aisys__/api/my-accounts/tags` 的 Go PG schema / 既有表复用、sqlc/store、service、handler、`system_sessions` 会话鉴权基线、admin / self 作用域和 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭的生产 router opt-in guard；其中分组 options / account-options 已覆盖 owner + 授权组只读 union，账户 options 已覆盖 owner + 授权账户只读 union、owner `tagIds` 过滤和名称包含搜索，账户 tags 仍是 owner-only 只读；授权来源 / grant / 授权写接口、标签写接口、真实 Docker/testcontainers integration、前端 smoke、生产切流和 Node 删除仍未完成，未正式生产接管。系统指标统计已纳入 Go 迁移规划，后续必须从 Node event-loop / DB service / SQLite 文件指标改为 Go runtime、PG、Redis、Asynq、worker lag、stats freshness 和网关 SLI，并作为 W6 / W7 独立迁移分块推进。
- 交付物：Go 迁移目录、迁移总计划、Go 架构基线、模块顺序、测试验收策略、开发构建部署调整，以及后续代码迁移计划入口。

## 范围边界

本计划是 Node 后端迁移到 Go + PostgreSQL + Redis 的长期总计划；当前已完成 M0 文档与规则基线，W0 Go 工程与 PG/Redis/Asynq 常规工程基线已落地，W1a 公开设置读接口、W1b 外部维护公开接口和 W2 管理端只读辅助接口当前已迁移路径都处于 Go 实现中。下面“当前阶段不包含”约束当前未接管范围，不表示长期计划不追踪后续模块迁移。

### 本次包含

- [x] 新增 `docs/migration/` 迁移目录。
- [x] 规划渐进减法迁移原则和阶段。
- [x] 规划 Go 后端目标架构、技术依赖、并发和线程安全边界。
- [x] 规划 Go 系统指标、Prometheus / pprof、内部系统监控契约、worker lag、PG / Redis / Asynq 和网关 SLI 替换口径。
- [x] 规划 PostgreSQL + Redis 单模式存储目标和 SQLite 移除范围。
- [x] 规划模块迁移顺序，明确公开接口、后台接口优先，真实网关最后。
- [x] 规划完整测试与验收策略。
- [x] 规划开发、安装、构建、部署、Docker、服务化和回滚调整。
- [x] 更新文档入口和计划索引。
- [x] 新增 `backend-go/` 最小 Go 工程、命令入口、配置、日志、健康检查、PG store、Redis namespace/cache/state、Asynq inspector/queue 基础封装、baseline migration、sqlc PoC、integration 测试骨架和 W0 live smoke 命令。
- [x] 配置本机 Go 1.26.4、w64devkit、sqlc、goose、golangci-lint 和 govulncheck 工具链。

### 当前阶段不包含

- 不生产接管现有业务接口：当前 W1a 只实现 `GET /__aisys__/api/settings/public` 的 Go 路由、store、migration、Redis state 原子限流封装、`JUHE_AI_TRUST_PROXY` 客户端 IP 识别、契约测试和 `w1a-public-settings-smoke` 验证入口；W1b 已补 catalog/auth/store/log/queue/worker-runtime 基础设施、HTTP shell / capture / 499 / ResponseWriter 透传契约，并补 public group CRUD、public route strategy CRUD、public API Key CRUD 与 public account CRUD 四条真实资源纵切面、integration / shell 测试代码、默认关闭的 opt-in 生产 router guard 和 `w1b-public-api-smoke` 灰度验证入口；W2 已补 `GET /__aisys__/api/proxies/options`、`GET /__aisys__/api/providers/options`、`GET /__aisys__/api/providers/models/options`、`GET /__aisys__/api/providers/{code}/models`、`GET /__aisys__/api/route-strategies/options`、`GET /__aisys__/api/my-route-strategies/options`、`GET /__aisys__/api/groups/options`、`GET /__aisys__/api/my-groups/options`、`GET /__aisys__/api/groups/account-options`、`GET /__aisys__/api/my-groups/account-options`、`GET /__aisys__/api/accounts/options`、`GET /__aisys__/api/my-accounts/options`、`GET /__aisys__/api/accounts/tags` 和 `GET /__aisys__/api/my-accounts/tags` 的 Go schema / 既有表复用、store / service / handler、管理端 `requireAuth` 会话鉴权基线、admin / self 作用域、模型目录聚合规则、owner / authorized permissions、owner `accountIds`、授权组 `accountIds=[]`、owner `tagIds` 过滤、名称包含搜索、账户授权实例只读 union、账号标签只读、`system_sessions` migration / query 和 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭的 opt-in 生产 router guard；授权来源 / grant / 授权写接口和标签写接口仍未迁移。当前主线程 Docker 不可用，真实 PG/Redis/Asynq、`w1b-public-api-smoke` 真实依赖执行与四类 shell E2E 通过待复跑。`JUHE_AI_PUBLIC_API_ENABLED=true` 和 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 只表示可灰度挂载验证；`w1b-public-api-smoke` 通过也只证明本地 httptest shell、真实依赖和 worker ingest 链路，不代表真实端口监听、反向代理切流或 Node 删除。未完成整体切流前，不接管生产路径。
- 不删除 Node 后端代码：只有某个模块由 Go 接管并通过验收后，才按减法规则删除。
- 不立即修改数据库 schema 或生成离线数据导入脚本：Go 迁移期间如需要 schema 调整或旧 SQLite 数据导入 PostgreSQL，另建具体计划。
- 不迁移真实网关：网关必须在公开接口、后台接口、存储和 worker 迁移成熟后执行。

## 关联文档

- 迁移目录：`docs/migration/README.md`
- 迁移总览：`docs/migration/迁移规划总览.md`
- Go 架构基线：`docs/migration/Go后端架构基线.md`
- Go 技术选型：`docs/migration/Go技术选型与依赖基线.md`
- Go 指标观测：`docs/migration/Go迁移指标与观测规划.md`
- Go 指标字段清单：`docs/migration/Go系统指标字段迁移清单.md`
- 存储目标：`docs/migration/存储目标与SQLite移除.md`
- 模块清单：`docs/migration/模块迁移顺序与减法清单.md`
- W1b 迁移记录：`docs/migration/W1b-外部维护公开接口迁移记录.md`
- W2 迁移记录：`docs/migration/W2-管理端只读辅助接口迁移记录.md`
- 测试策略：`docs/migration/测试与验收策略.md`
- 开发构建部署：`docs/migration/开发构建部署调整.md`
- 公开资源维护接口：`docs/functions/公开资源维护接口设计.md`
- 外部来源系统鉴权：`docs/functions/外部来源系统鉴权设计.md`
- 公开接口日志：`docs/functions/公开接口日志设计.md`
- 架构总览：`docs/architecture/架构总览.md`
- 后端架构入口：`docs/architecture/backend/README.md`
- 开发入口：`docs/develop/README.md`
- 部署入口：`docs/deploy/README.md`

## 方案概述

- 方案原则：渐进式、减法迁移、单 owner、测试先行、Go 原生优先、网关最后。
- 数据变化：本次不改 schema；迁移目标为 PostgreSQL + Redis 单模式，后续 schema 只保留当前最优结构，不在运行路径保留旧 SQLite 结构兼容、双写或双读。
- 接口变化：本次不改接口；后续迁移需保证当前公开契约和管理契约等价，或先更新功能文档和测试预期。
- 前端变化：本次不改前端；后续前端继续以 Vue 3 + TypeScript + Ant Design Vue 维护。
- 后端变化：目标后端从 Node.js + TypeScript 迁移到 Go，逐步删除 Node 后端模块、DB service、SQLite adapter、SQLite 配置和双模式分支。
- 指标变化：系统指标统计必须随 Go 接管同步重建，Node `eventLoopLagMs`、`process_event_loop_*`、`processEventLoop*`、`db-service`、SQLite 文件体积和 usage shard 文件路径不能作为 Go 长期契约。
- 数据处理策略：既有 SQLite 数据如需保留，只通过一次性离线导出、清洗、导入 PostgreSQL 处理，不写入 Go 或 Node 运行路径。

## 执行拆解

### M0 文档基线

- [x] 更新相关长期文档入口。
- [x] 新增迁移目录主文档和示例文档。
- [x] 新增迁移总览、Go 架构基线、模块顺序、测试策略和部署调整。
- [x] 新增 Go 技术选型与依赖基线，固定框架、日志、配置、DB、SQL、job、测试、观测和安全扫描默认依赖。
- [x] 新增 Go 迁移指标与观测规划和 Go 系统指标字段迁移清单，固定 Go runtime、PG、Redis、Asynq、worker lag、stats freshness 和网关 SLI 指标边界，并给出 Node `eventLoopLagMs` / `processEventLoop*` / `db-service` / SQLite 文件指标的字段级删除门禁。
- [x] 明确 PostgreSQL + Redis 单模式和 SQLite 移除边界。

### 后续迁移阶段

- [~] 建立 Go 后端最小工程和 PostgreSQL / Redis / Asynq 基线。
- [~] 固定公开接口契约并迁移公开接口：W1a 公开设置读接口已进入 Go 实现中；W1b `/__aipublic__` 外部维护接口已补 Go catalog/auth、PostgreSQL auth/log adapter、Redis penalty-window helper、公开接口日志 snapshot/log builder、Asynq payload/enqueue/handler、`juhe-ai-worker ingest` 日志消费 runtime、HTTP shell / capture / 499 / ResponseWriter 透传契约、PG foundation smoke 用例、public API log Asynq smoke 用例、public group CRUD、public route strategy CRUD、public API Key CRUD、public account CRUD 四条真实资源纵切面、默认关闭的 opt-in 生产 router guard，以及 `w1b-public-api-smoke` 独立 maintenance smoke；public group / route strategy / API Key / account 四类完整 Redis limiter + public API log worker shell E2E 测试代码已补；未正式生产接管；当前主线程 Docker 不可用，真实 PG/Redis/Asynq integration、`w1b-public-api-smoke` 真实依赖执行与四类 shell E2E 待复跑。
- [~] 迁移后台管理只读接口：`GET /__aisys__/api/proxies/options`、`GET /__aisys__/api/providers/options`、`GET /__aisys__/api/providers/models/options`、`GET /__aisys__/api/providers/{code}/models`、`GET /__aisys__/api/route-strategies/options`、`GET /__aisys__/api/my-route-strategies/options`、`GET /__aisys__/api/groups/options`、`GET /__aisys__/api/my-groups/options`、`GET /__aisys__/api/groups/account-options`、`GET /__aisys__/api/my-groups/account-options`、`GET /__aisys__/api/accounts/options`、`GET /__aisys__/api/my-accounts/options`、`GET /__aisys__/api/accounts/tags` 和 `GET /__aisys__/api/my-accounts/tags` 已补 Go PG schema / 既有表复用、store、service、handler、`requireAuth` 会话鉴权基线、admin / self 作用域和默认不接管的 `JUHE_AI_MANAGEMENT_API_ENABLED` router opt-in guard；分组 options / account-options 已覆盖 owner + 授权组只读 union，账户 options 已覆盖 owner + 授权账户只读 union、owner `tagIds` 过滤和名称包含搜索，授权来源 / grant / 授权写接口、标签写接口、前端 smoke、生产切流和 Node 删除仍未完成。
- [ ] 迁移后台管理写接口和权限链路。
- [ ] 迁移 worker、Node storage 删除收尾和维护脚本。
- [ ] 迁移真实中转网关。
- [ ] 删除 Node 后端发布链路并更新最终部署文档。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档结构 | 迁移目录入口 | 检查 `docs/migration/README.md` 和示例文档 | 新目录有 README 和示例文档 | 已通过 | 本次新增 |
| 文档链接 | 文档入口引用 | `rg "migration|迁移" docs` + 相对链接检查 | 入口文档能找到迁移目录和 PLAN-0081，新增链接指向真实文件 | 已通过 | 已执行 |
| 指标规划 | Go 系统指标替换口径 | 检查 `docs/migration/Go迁移指标与观测规划.md`、`docs/migration/Go系统指标字段迁移清单.md`、功能契约和架构入口 | Node event-loop / DB service / SQLite 文件指标被标记为过渡事实，Go 指标口径有明确替代、字段级删除清单和前端契约门禁 | 已通过 | 本次补强 |
| 代码验证 | 当前代码类型检查 | `pnpm typecheck` | 当前前后端类型检查通过 | 不适用 | 本次只改文档 |
| Go 骨架 | Go 单元测试 | `go test ./...` | Go 代码测试通过 | 已通过 | `backend-go` 已通过 |
| Go 骨架 | Race 检测 | `go test -race ./...` | Windows 本机 race 稳定通过 | 已通过 | 已使用 w64devkit 2.8.0 / GCC 16.1.0 修复旧 MinGW 导致的 race 失败 |
| Go 质量 | vet / lint / vuln | `go vet ./...`、`golangci-lint run`、`govulncheck ./...` | vet 无错误、lint 0 issues、无漏洞 | 已通过 | `0 issues`，`No vulnerabilities found` |
| W0 smoke | 命令入口和健康接口 | `go run ./cmd/juhe-ai version`、`go run ./cmd/juhe-ai-worker version`、`go run ./cmd/juhe-ai-maintenance version`、本地临时端口 health / metrics smoke | 命令可运行，health / API health / metrics 正常 | 已通过 | 三个命令均输出 `0.1.0-w0`；health smoke 返回 `ok` |
| W0 integration | testcontainers / goose / sqlc / Redis / Asynq | `go test -tags=integration ./internal/testkit/integration -count=1` | 启动 PG/Redis 并跑 migration、sqlc 查询、Redis TTL/pipeline/counter/fixed-window/penalty-window、Asynq enqueue / consume / retry / archive | 待 Docker 复跑 | 当前主线程 Docker / testcontainers 不可用，容器子测试输出 `SKIP`，不计为真实 PG/Redis 通过 |
| W0 live smoke | 真实 PG/Redis/Asynq URL | `go run ./cmd/juhe-ai-maintenance w0-smoke` | 缺配置 fail-fast；配置真实 URL 时 PG/Redis 均为 `ok`，Asynq 至少完成 enqueue + inspector pending smoke | 部分通过 | 已验证缺少真实 URL 时 fail-fast；本机未配置真实 PG/Redis/Asynq URL |
| W1a 公开设置 | `GET /__aisys__/api/settings/public` | Go API 契约测试 + store 测试 + Redis limiter 测试 + 客户端 IP / trust proxy 测试 + integration seed 测试 + `go run ./cmd/juhe-ai-maintenance w1a-public-settings-smoke` | 匿名 200、`{ data: { appName, appIcon } }`、no-store、只暴露公开字段、Redis state 原子 IP read rate limit、`JUHE_AI_TRUST_PROXY` 默认不信任公网请求头、错误不泄露内部依赖 | Go 实现中 | 已补 Go route/service/store/migration/sqlc/test、Redis 固定窗口原子限流代码、trust proxy 客户端 IP 识别和 W1a 独立 smoke 命令；testcontainers integration 当前主线程因 Docker 不可用输出 `SKIP`，独立真实外部 URL smoke 仍待部署环境执行 |
| W1b `/__aipublic__` | 外部维护公开接口契约 | Node 对照脚本 + Go 契约 / store / Redis / integration 测试 + public group / public route strategy / public API Key / public account 专项矩阵 + router guard 测试 + `w1b-public-api-smoke` + 后续 Go API 契约测试 | Bearer 鉴权、scope、Redis penalty-window 限频、公开接口日志、旧公开路径 404、public group CRUD、public route strategy CRUD、public API Key CRUD、public account CRUD、默认关闭 / 显式开启 router guard、`w1b-public-api-smoke`、API Key secret 只在新增响应返回且日志脱敏、AI 账户凭据加密落库且不回显、分页、业务响应敏感字段不回显、公开日志快照边界和错误语义不缺失；smoke 输出不能提供生产接管证据 | Go 实现中 | 已新增 W1b 迁移记录并补 Go catalog/auth/store port、PostgreSQL auth/log adapter、Redis penalty-window helper、公开接口日志 snapshot/log builder、Asynq payload/enqueue/handler、`juhe-ai-worker ingest` 日志消费 runtime、HTTP shell / capture / 499 / ResponseWriter 透传契约、W1b PG foundation smoke 用例、public API log Asynq smoke 用例、public group CRUD、public route strategy CRUD、public API Key CRUD、public account CRUD 四条真实资源纵切面、`JUHE_AI_PUBLIC_API_ENABLED=false` 默认关闭的 opt-in 生产 router guard 和 `w1b-public-api-smoke`；Node 非 PG 对照命令已通过，Go 常规验证矩阵、四类资源 handler/service 专项、router guard 快速测试、maintenance smoke 缺配置 / 输出边界测试和 account shell E2E 编译已通过；account shell E2E 测试代码已补但当前主线程因 Docker 不可用输出 `SKIP`，真实 PG integration、真实 `w1b-public-api-smoke` 与完整 Redis limiter + public API log worker shell E2E 需 Docker/testcontainers 或真实 PG/Redis/worker 环境复跑；生产路由切流和 Node 删除待执行；暂不正式接管 |
| W2 管理只读 | `GET /__aisys__/api/proxies/options`、`GET /__aisys__/api/providers/options`、`GET /__aisys__/api/providers/models/options`、`GET /__aisys__/api/providers/{code}/models`、`GET /__aisys__/api/route-strategies/options`、`GET /__aisys__/api/my-route-strategies/options`、`GET /__aisys__/api/groups/options`、`GET /__aisys__/api/my-groups/options`、`GET /__aisys__/api/groups/account-options`、`GET /__aisys__/api/my-groups/account-options`、`GET /__aisys__/api/accounts/options`、`GET /__aisys__/api/my-accounts/options`、`GET /__aisys__/api/accounts/tags` 和 `GET /__aisys__/api/my-accounts/tags` | `go test ./internal/modules/managementauth ./internal/modules/managementproxies ./internal/modules/managementproviders ./internal/modules/managementprovidermodels ./internal/modules/managementroutestrategies ./internal/modules/managementgroups ./internal/modules/managementaccounts ./internal/httpapi ./internal/store/postgres ./internal/config ./internal/app`、`go test -tags=integration ./internal/testkit/integration -run TestW2Management -count=1` | options / catalog 字段、错误、权限、owner/self 作用域、敏感字段白名单和 router opt-in guard 必须通过；分组 options / account-options 覆盖 owner + 授权组只读 union、`manageableOnly=true` 排除授权组、authorized permissions 和授权组 `accountIds=[]`；`accounts/options` 覆盖 owner + 授权账户只读 union、`accessType=authorized`、授权字段、authorized permissions、owner `tagIds` 命中任一标签过滤和名称包含搜索，`accounts/tags` / `my-accounts/tags` 覆盖 owner 标签字典和未删除账号绑定计数；账号标签只读不覆盖标签写接口，授权来源 / grant / 授权写接口仍未迁移；`juhe_ai_session` 会话 cookie、SHA-256 `token_hash`、`system_sessions` join `system_accounts`、过期 / 停用拒绝和普通用户初始密码拦截符合 Node 当前语义；默认生产 router 不注册，显式 `JUHE_AI_MANAGEMENT_API_ENABLED=true` 才允许灰度挂载 | Go 实现中 | 单元 / handler / store / auth / config / app 快速测试已通过；integration smoke 代码已补，当前 Docker/testcontainers 真实执行输出 `SKIP`，待健康环境复跑 |
| Worker | 后台任务 | Go worker 测试 + 游标验证 | 队列、游标、重试和保留期正确 | 未执行 | 这里指 W7 全量 ingest / stats / ops worker 迁移；W1b public API log 单任务消费 runtime 已在 W1b 行记录，不代表后台 worker 已生产接管 |
| 存储 | PostgreSQL + Redis 单模式 | Go store 测试 + PG/Redis/Asynq smoke + SQLite 引用清理检查 | 新库初始化、Redis 队列和运行态可用，Go 运行路径无 SQLite | 部分通过 | W0 已补 baseline migration、sqlc catalog 查询 PoC、testcontainers integration 和 `w0-smoke`；真实容器 / 真实 URL smoke 因本机环境缺失未执行 |
| 网关 | 真实中转网关 | mock AI / real e2e / 性能压测 | SSE、调度、审计、usage 和失败恢复正确 | 未执行 | 最后执行 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-07-06 | 进行中 | AI | 创建 Go 渐进减法迁移规划，先落文档基线，不改业务代码。 |
| 2026-07-06 | 进行中 | AI | 根据最新决策补充 PostgreSQL + Redis 单模式目标，新增 SQLite 移除范围和旧数据离线处理边界。 |
| 2026-07-06 | 进行中 | AI | 按 10 个 agent 审阅结果补齐 PG/Redis 基线前置、网关 W9/W10 门禁、测试命令矩阵、部署收尾、旧 SQLite 文档过渡说明和历史计划方向收口。 |
| 2026-07-06 | 进行中 | AI | 根据长期维护要求补充 Go 技术选型与依赖基线，默认采用 `net/http` + chi、slog、caarlos0/env、godotenv、pgx、sqlc、goose、go-redis、Asynq、Prometheus、testcontainers、golangci-lint 和 govulncheck，并要求 W0 完成依赖 PoC。 |
| 2026-07-07 | 进行中 | AI | 按系统指标统计 Go 化要求补充 `docs/migration/Go系统指标字段迁移清单.md`，把 Node `eventLoopLagMs`、`processEventLoop*`、`db-service`、V8 heap、SQLite 文件体积和 usage shard 文件指标列为 Go 接管时必须删除或替换的字段，并补充 Go runtime、PG、Redis、Asynq、worker lag、stats freshness 和前端页面迁移门禁。 |
| 2026-07-06 | 进行中 | AI | 创建 `backend-go/` W0 工程骨架，落地 Go 1.26.4 本机工具链、w64devkit 2.8.0 C 工具链、sqlc / goose / golangci-lint / govulncheck CLI，并通过单测、race、vet、build、lint、漏洞扫描、版本命令和 health smoke。 |
| 2026-07-06 | 进行中 | AI | 根据 4 个 gpt-5.5 xhigh 子 agent 审阅结果补齐 W0 PoC：Asynq health 改为真实 client ping，`rediss` 传 TLS，diagnostics 限制 loopback，health 错误脱敏，新增 sqlc 生成查询、testcontainers integration 和 `juhe-ai-maintenance w0-smoke`。 |
| 2026-07-06 | 进行中 | AI | 继续按子 agent 审阅结论收紧 W0 基线：PG store 隐藏 pgx/sqlc 类型、Redis 增加 namespace / TTL / pipeline / `IncrWithTTL` 和 DB 去重、Asynq 增加显式 Redis timeout、inspector 与 pending smoke；重新通过常规 Go 验证矩阵和三平台构建。 |
| 2026-07-06 | 进行中 | AI | 启动 W1a 公开设置读接口迁移：用 4 个 gpt-5.5 xhigh 子 agent 梳理 Node 路由、前端契约、存储 schema 和验证门禁；确认 `/__aipublic__` 应拆为 W1b，不与公开设置读接口混同。Go 侧新增 `juhe_business.global_settings` migration / seed、sqlc 查询、PG store 方法、`publicsettings` service 和 `GET /__aisys__/api/settings/public` route。 |
| 2026-07-06 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1a 限流、Go Redis 封装、文档口径和依赖边界；Go 侧新增 Redis Lua 原子 fixed-window limiter、HTTP 可插拔限流器、真实 server Redis state 注入、跨 client / 并发 integration 断言和 router 缺 limiter fail-fast。 |
| 2026-07-06 | 进行中 | AI | 继续用 3 个 gpt-5.5 xhigh 子 agent 复核 Node trust proxy、Go 当前 IP 识别和文档门禁；Go 侧新增 `JUHE_AI_TRUST_PROXY` 解析、客户端 IP resolver、W1a 限流接入和 diagnostics loopback 接入。 |
| 2026-07-06 | 进行中 | AI | 继续用 3 个 gpt-5.5 xhigh 子 agent 复核 W1a smoke 范围、maintenance 命令结构和文档落点；Go 侧新增 `w1a-public-settings-smoke`，用于真实 PG public settings / system settings 读取、HTTP route 精确响应、Redis state 原子限流和跨 client 共享状态 smoke。 |
| 2026-07-07 | 进行中 | AI | 继续用多个 gpt-5.5 xhigh 子 agent 复核 W1b `/__aipublic__` Node 路由、鉴权、scope、限频、公开日志、存储、资源字段和回归脚本；新增 W1b 单模块迁移记录，当前仍保持契约固定、暂不接管。 |
| 2026-07-07 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1b Go 骨架落点、auth/scope 常量、store port 和文档措辞；Go 侧新增 `internal/modules/publicapi` catalog、`internal/modules/publicapi/auth` Bearer/token hash/source-token scope 骨架、`internal/store/port/publicapi.go` auth store port 和 router 未接管 guard；仍未挂载 `/__aipublic__` 生产路由。 |
| 2026-07-07 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1b Node 数据模型、16 个 CRUD 行为、Go PG/sqlc 边界和 Redis/队列日志边界；Go 侧新增 W1b PG migration、sqlc query、PostgreSQL auth/log adapter、Redis penalty-window Lua helper、publicapi rate limiter 和 W1b PG foundation integration smoke；仍未挂载 `/__aipublic__` 生产路由，未实现公开日志 capture/队列和 16 个 CRUD。 |
| 2026-07-07 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1b 公开接口日志快照语义、Asynq 任务边界、handler 落点和文档状态；Go 侧新增 `internal/modules/publicapilog` snapshot/log builder、`internal/jobs/publicapilog` payload/enqueue/handler 和 W1b public API log Asynq integration smoke；仍未挂载 `/__aipublic__` 生产路由，未接入 HTTP capture middleware、worker runtime 或 16 个 CRUD。 |
| 2026-07-07 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1b public API log worker runtime、Asynq 边界、Node 队列语义取舍和文档口径；Go 侧新增 `internal/jobs/worker` ingest runtime、`internal/app/ingest_worker.go`、`juhe-ai-worker ingest`、invalid payload `SkipRetry` 映射和 Asynq import guard；W1b public API log integration 改为启动真实 worker runtime。仍未挂载 `/__aipublic__` 生产路由，未接入 HTTP capture middleware 或 16 个 CRUD。 |
| 2026-07-07 | 进行中 | AI | 继续用 3 个 gpt-5.5 xhigh 子 agent 复核 W1b HTTP shell 落点、Node body/auth/capture 顺序和 Go 单测方案；Go 侧新增不挂生产 router 的 `internal/httpapi/public_api_shell.go`，覆盖 16 个 catalog endpoint、旧公开路径 404、JSON body 400、body 413、401/403 auth、429 rate limit、response capture 和 public API log 入队。仍未挂载 `/__aipublic__` 生产路由，未实现真实资源 handler 或 16 个 CRUD。 |
| 2026-07-07 | 进行中 | AI | 继续用 4 个 gpt-5.5 xhigh 子 agent 复核 W1b Node 499 契约、Go `ResponseWriter` 包装、499 单测方案和分组公开接口下一步契约；Go 侧 HTTP shell 补齐客户端提前断开 499 日志、response snapshot `statusCode=499`、写入断连错误识别、`httpsnoop` 保留 `Unwrap` / `Flush` / `ReadFrom` 等底层 writer 能力，并记录分组 CRUD 仍缺业务 schema/query/service/handler。仍未挂载 `/__aipublic__` 生产路由，未实现真实资源 handler 或 16 个 CRUD。 |
| 2026-07-07 | 进行中 | AI | W1b public group CRUD 作为真实资源纵切面之一进入记录范围：Go 侧已补 `publicgroups` service、`public_groups` handler、PublicGroup store port、PostgreSQL `publicgroups` adapter、`w1b_public_groups.sql` query 和 `000004_w1b_public_groups.sql` schema / seed，覆盖目标用户自动创建、provider 启用校验、同名幂等、并发幂等重试、默认分组保护、账号绑定保护、owner 复合 FK 和活跃策略路由行锁保护。handler / service 非容器专项已通过；真实 PG integration 待 Docker 环境复跑。后续记录已补齐完整 Redis limiter + public log shell E2E 用例、public route strategy CRUD、public API Key CRUD、public account CRUD 和 account shell E2E 测试代码，当前剩余生产路由挂载、真实 Docker/testcontainers 复跑、生产切流和 Node 删除证据。 |
| 2026-07-07 | 进行中 | AI | W1b public group 完整 Redis limiter + public API log worker shell E2E 用例已补，覆盖目标为真实 Bearer/auth/scope、Redis penalty-window、public API log 入队 / worker 写 PG、响应脱敏和 HTTP shell 贯穿路径；当前主线程 Docker 不可用，真实 E2E 待复跑；仍未挂生产 router。后续记录已补齐 public route strategy、public API Key、public account 纵切面和 account shell E2E 测试代码，当前剩余生产路由挂载、真实 Docker/testcontainers 复跑、生产切流和 Node 删除证据。 |
| 2026-07-07 | 进行中 | AI | W1b public route strategy CRUD 作为第二条真实资源纵切面已补：Go 侧新增 `publicroutestrategies` service、`public_route_strategies` handler、PublicRouteStrategy store port、PostgreSQL `publicroutestrategies` adapter、`w1b_public_route_strategies.sql` query，并在 `000004_w1b_public_groups.sql` 补 route strategy / binding / api key 约束。handler / service 非容器专项已通过；PostgreSQL store / shell E2E 待 Docker 环境复跑。仍未挂生产 router。后续记录已补齐 public API Key、public account 纵切面和 account shell E2E 测试代码，当前剩余生产路由挂载、真实 Docker/testcontainers 复跑、生产切流和 Node 删除证据。 |
| 2026-07-07 | 进行中 | AI | W1b public API Key CRUD 作为第三条真实资源纵切面已补：Go 侧新增 `publicapikeys` service、`public_api_keys` handler、PublicAPIKey store port、PostgreSQL `publicapikeys` adapter、`w1b_public_api_keys.sql` query，并扩展 `000004_w1b_public_groups.sql` 的 `api_keys` 字段 / 索引 / JSON 约束。handler / service 非容器专项已通过；PostgreSQL store / shell E2E 待 Docker 环境复跑。Go 目标采用 hash-only secret 存储，完整 key 只在新增响应返回且 public API log / query string 脱敏。仍未挂生产 router，后续已补齐 public account 纵切面和 account shell E2E 测试代码，当前剩余生产路由挂载、真实 Docker/testcontainers 复跑、生产切流和 Node 删除证据。 |
| 2026-07-07 | 进行中 | AI | W1b public account CRUD 作为第四条真实资源纵切面已补：Go 侧新增 `publicaccounts` service、`public_accounts` handler、PublicAccount store port、PostgreSQL `publicaccounts` adapter、`w1b_public_accounts.sql` query 和 `000005_w1b_public_accounts.sql` schema / provider profile seed。覆盖目标用户 / 目标分组自动创建、provider/profile 校验、上游凭据 AES-GCM 可逆加密、凭据指纹 hash、响应白名单、pending_test 直启拦截、软删除和绑定清理。已通过 `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/...`、`go mod tidy -diff`、`sqlc generate` 和 integration 包编译；真实 Docker/testcontainers integration、account shell E2E 真实复跑、生产路由挂载和 Node 删除仍待完成。已提交 `2c2efe4ec feat(go): add backend migration baseline and W1 public APIs`。 |
| 2026-07-07 | 进行中 | AI | W1b 生产 router opt-in guard 已补：`JUHE_AI_PUBLIC_API_ENABLED=false` 默认不注册 `/__aipublic__`，显式 `true` 时 Go 主 server 装配 Bearer auth、Redis penalty-window、Asynq public API log queue 和四类资源 handler；开启时要求 Redis state、Redis queue 和不少于 32 字符的 `JUHE_AI_SECRET`，AI 账户凭据使用该密钥加密。已通过 `go test ./internal/config ./internal/httpapi ./internal/app`。该 guard 只用于灰度验证，不代表 W1b 正式接管；account shell E2E 测试代码已补，真实 PG/Redis/Asynq integration、account shell E2E 真实复跑、反向代理切流和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W1b public account shell E2E 测试代码已补：新增 `TestW1bPublicAccountsShellE2E`，覆盖 Bearer/auth/scope、Redis limiter、public API log 入队 / worker 写 PG、account add/list/update/delete/limited、source/token last_used、账号凭据加密落库、软删除、请求日志 `apiKey/baseUrl/notes/query` 脱敏和响应白名单。已通过 integration 包编译；当前 `go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsShellE2E -count=1` 因 Docker 不可用输出 `SKIP`，不计为真实通过。 |
| 2026-07-07 | 进行中 | AI | W1b 新增 `go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke` 独立灰度验证入口：复用生产 public API handler 装配，内部用本地 `httptest` 验证默认 router guard、显式 opt-in mount、临时内置测试 source/token、真实 PostgreSQL / Redis state / Redis queue、public API log 入队和 external `juhe-ai-worker ingest` 写 PG；输出固定 `takeoverEvidence=false`、`productionTakeoverNotEvaluated=true`。已通过 `go test ./internal/maintenance ./internal/app`；真实依赖执行仍待具备 PostgreSQL / Redis / ingest worker 环境后复跑，不构成生产切流或 Node 删除证据。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第一块进入 Go 实现中：用 4 个 gpt-5.5 xhigh 子 agent 分别梳理 Node 路由 / 权限、前端依赖、Go schema 和文档门禁；确认先迁 `GET /__aisys__/api/proxies/options`，且不能在 Go 管理端会话鉴权未迁移前裸挂生产路由。Go 侧新增 `proxy_profiles` migration、sqlc 查询、Management store port、PostgreSQL adapter、`managementproxies` service、HTTP handler、router 注入 guard 和 W2 integration smoke 代码；已通过 `sqlc generate` 和 `go test ./internal/modules/managementproxies ./internal/httpapi ./internal/store/postgres`，真实 Docker/testcontainers integration、生产切流和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端会话鉴权基线已补：用 3 个 gpt-5.5 xhigh 子 agent 梳理 Node `requireAuth`、Go router 装配和 `system_sessions` schema；Go 侧新增 `000007_w2_management_session_auth.sql`、`w2_management_auth.sql`、`managementauth` module、`NewManagementAPIAuthMiddleware`、PostgreSQL session adapter、`JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭配置和 app 装配。覆盖 `juhe_ai_session` cookie、SHA-256 token hash、过期 / 停用会话 401、普通用户初始密码 403、admin 初始密码有效值归零和 W2 opt-in guard；已通过 `sqlc generate`、`go test ./internal/modules/managementauth ./internal/modules/managementproxies ./internal/httpapi ./internal/store/postgres ./internal/config ./internal/app`、`go test ./...` 和 integration 包编译。`TestW2ManagementAuthPostgresSmoke` 当前因 Docker/testcontainers 不可用输出 `SKIP`，生产切流、前端 smoke 和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第二块 `GET /__aisys__/api/providers/options` 已补：用 4 个 gpt-5.5 xhigh 子 agent 梳理 ProviderDefinition 契约、前端字段依赖、Go 分层落点和后续 options 顺序；确认本块只迁供应商定义 / 协议画像 / endpoint families / 系统账户默认测试模型偏好，不混入 provider model catalog。Go 侧新增 `000008_w2_management_provider_options.sql`、`w2_management_provider_options.sql`、`managementproviders` module、PostgreSQL provider options adapter、HTTP handler、router/app 注入和 integration smoke 代码。已通过 `sqlc generate`、`go test ./internal/modules/managementproviders ./internal/store/postgres ./internal/httpapi ./internal/app`、`go test ./...` 和 integration 包编译；`TestW2ManagementProviderOptionsPostgresSmoke` 当前因 Docker/testcontainers 不可用输出 `SKIP`，生产切流、前端 smoke、W2 全范围和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第三块 API Key 表单依赖的策略路由 options 已补：用 4 个 gpt-5.5 xhigh 子 agent 梳理 Node `route-strategies/options` 和 `my-route-strategies/options` 契约、前端 selected strategy 回填依赖、Go schema/owner 和文档门禁；确认管理侧必须 admin-only 且可全局 / 指定用户查看，用户侧必须强制 self scope 并忽略 query `systemAccountId`。Go 侧新增 `managementroutestrategies` module、`w2_management_route_strategy_options.sql`、PostgreSQL route strategy options adapter、HTTP handler、router/app 注入和 integration smoke 代码，复用既有 `route_strategies` 表不新增 migration。已通过 `sqlc generate`、`go test ./internal/modules/managementauth ./internal/modules/managementproxies ./internal/modules/managementproviders ./internal/modules/managementroutestrategies ./internal/httpapi ./internal/store/postgres ./internal/config ./internal/app`、`go test ./...` 和 integration 包编译；`TestW2ManagementRouteStrategiesPostgresSmoke` 当前因 Docker/testcontainers 不可用输出 `SKIP`，生产切流、前端 smoke、W2 全范围和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第四块分组 options owner-only 阶段已补：用 4 个 gpt-5.5 xhigh 子 agent 分别核对 Node `groups/options` / `my-groups/options` 契约、Go schema 能力、现有管理接口模式和文档落点；确认当前 Go schema 尚无 `resource_authorizations` / `group_authorization_settings`，因此本块只迁 owner 自有分组选项，不声明等价 Node 授权组 union。Go 侧新增 `managementgroups` module、`w2_management_group_options.sql`、PostgreSQL group options adapter、HTTP handler、router/app 注入和 integration smoke 代码，复用既有 `groups` / `system_accounts` 表不新增 migration。已通过 `sqlc generate` 和 `go test ./internal/modules/managementgroups ./internal/httpapi ./internal/store/postgres ./internal/app`；真实 Docker/testcontainers integration、授权组 union、group account-options、账户 options owner-only 后续记录、生产切流、前端 smoke、W2 全范围和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第五块账户 options owner-only 阶段已补：用 4 个 gpt-5.5 xhigh 子 agent 分别核对 Node `accounts/options` / `my-accounts/options` 契约、Go schema 能力、前端调用面和现有 W2 实现模式；确认当前 Go schema 尚无 `resource_authorizations`、账号标签表和 `account_name_search_*` 名称检索表，因此本块只迁 owner 自有账户下拉，不声明等价 Node 授权账户 union。Go 侧新增 `managementaccounts` module、`w2_management_account_options.sql`、PostgreSQL account options adapter、HTTP handler、router/app 注入和 integration smoke 代码，复用既有 `accounts` / `group_accounts` / `system_accounts` 表不新增 migration。已通过 `sqlc generate` 和 `go test ./internal/modules/managementaccounts ./internal/httpapi ./internal/store/postgres ./internal/app`；真实 Docker/testcontainers integration、授权账户 union、账号标签、group account-options、生产切流、前端 smoke、W2 全范围和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第六块 group account-options owner-only 阶段已补：用 4 个 gpt-5.5 xhigh 子 agent 分别核对 Node `groups/account-options` / `my-groups/account-options` 契约、Go schema 能力、前端调用面和现有 W2 实现模式；确认该接口不是 `accounts/options?groupId` 别名，而是“分组选项 + accountIds”，当前 Go schema 足以实现 owner 自有分组和启用 owner 绑定账号 ID，不声明等价 Node 授权组 union。Go 侧新增 `w2_management_group_account_options.sql`、PostgreSQL group account options adapter、`managementgroups.Service.AccountOptions`、HTTP handler、router/app 注入和 integration smoke 代码，复用既有 `groups` / `group_accounts` / `accounts` / `system_accounts` 表不新增 migration。已通过 `sqlc generate`、`go test ./internal/modules/managementgroups ./internal/httpapi ./internal/store/postgres ./internal/app`、`go test ./...` 和 integration 包编译；`TestW2ManagementGroupAccountOptionsPostgresSmoke` 因 Docker/testcontainers 不可用输出 `SKIP`，真实 Docker/testcontainers integration、授权组 union、授权账户 union、账号标签、生产切流、前端 smoke、W2 全范围和 Node 删除仍待完成。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第七块账号标签 owner-only 阶段已补：用 3 个 gpt-5.5 xhigh 子 agent 对比账号标签、分组授权 union 和账号授权 union 的范围与风险，确认先迁低风险只读标签字典和 `accounts/options?tagIds` 过滤。Go 侧新增 `000010_w2_management_account_tags.sql`、`w2_management_account_tags.sql`、`ListManagementAccountTags`、`account_tag_bindings` 过滤、`managementaccounts.Service.Tags`、`accounts/tags` / `my-accounts/tags` handler、router/app 注入和 W2 integration smoke 扩展；当时仍保留授权账户 union、标签写接口和名称包含搜索缺口，其中 owner-only 名称包含搜索后续已在第九块补齐。另用子 agent 巡检系统指标统计，补强 Go 指标规划里的 W2 operation 名称和 W6/W7 独立迁移门禁。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第八块授权组只读 union 已补：用子 agent 和本地检索复核 Node `groups/options` / `groups/account-options` 的 owner + authorized union 契约，确认范围只覆盖分组下拉只读可见性，不迁授权写接口、授权来源 / grant、授权账户 union、网关调度或统计写链路。Go 侧新增 `000011_w2_management_group_authorizations.sql`、扩展 `w2_management_group_options.sql` / `w2_management_group_account_options.sql` 为 owner + authorized CTE union，补 `ManageableOnly` 下传、authorized DTO 字段、authorized permissions、授权组 `accountIds=[]` 和 W2 integration smoke 断言；当时仍保留授权账户 union、标签写接口和名称包含搜索缺口，其中 owner-only 名称包含搜索后续已在第九块补齐。 |
| 2026-07-07 | 进行中 | AI | W2 管理端只读辅助接口第九块账户 options owner-only 名称包含搜索已补：用子 agent 和本地检索复核 Node `account_name_search_terms` / `account_name_search_documents` 契约，确认范围只覆盖 `accounts/options` / `my-accounts/options` 的 owner-only 远程搜索，不迁账户主列表、授权账户 union 或账户写接口索引维护。Go 侧新增 `000012_w2_management_account_name_search.sql`、扩展 `w2_management_account_options.sql` 为前缀窗口 + terms/documents contains 候选收敛，补 NFKC/trim/1-3 gram helper、SQL source guard、integration smoke search terms fixture 和中间片段 / 非连续误命中 / owner scope 断言；文档同步保留授权账户 union、授权写接口、标签写接口、生产切流和 Node 删除门禁。 |
| 2026-07-08 | 进行中 | AI | W2 管理端只读辅助接口第十块账户 options 授权账户只读 union 已补：用 4 个 gpt-5.5 xhigh 子 agent 分别核对 Node 契约、Go schema、测试缺口和文档门禁；确认范围只覆盖 `accounts/options` / `my-accounts/options` 的 owner + 授权实例下拉，不迁授权来源 / grant / 授权写接口、标签写接口、前端切流或网关调度。Go 侧新增 `000013_w2_management_account_authorization_instances.sql`，扩展 `w2_management_account_options.sql` 为 owner + authorized CTE union，补 `accessType=authorized`、授权字段、authorized permissions、本地授权实例 / 来源账户 / 同一授权 ID 启用分组绑定的 `status` 与 `schedulable` 判定，以及 W2 integration smoke 断言；生产 router 仍默认关闭，真实 Docker/testcontainers、前端 smoke、生产单 owner 切流和 Node 删除仍待完成。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-07-06 | 后端目标运行时转 Go | 当前 Node 后端围绕事件循环和阻塞规避积累了过多复杂度 | 后续后端模块按 Go 架构迁移，Node 后端逐步删除 |
| 2026-07-06 | 采用渐进减法迁移 | 避免一次性重写风险，同时避免迁移后旧实现残留 | 每个模块 Go 接管后必须删除 Node 旧实现 |
| 2026-07-06 | 公开接口和后台接口优先，真实网关最后 | 公开/后台接口风险相对低，网关是最核心链路 | 网关迁移必须等待测试、存储和 worker 基线成熟 |
| 2026-07-06 | Go 架构优先使用标准库和轻量依赖 | 避免从 Node 复杂度迁移到框架复杂度 | 默认 `net/http`、`chi`、`slog`、`pgx`、`go-redis`；不引入 SQLite driver |
| 2026-07-06 | Go 后端只保留 PostgreSQL + Redis 单模式 | 继续保留 SQLite 会让 schema、repository、测试和部署长期双倍维护 | 不再引入 Go SQLite driver；删除 standalone / performance 模式分支；旧 SQLite 数据只走离线导入 PostgreSQL |
| 2026-07-06 | Go 通用能力优先采用成熟开源库并封装在基础设施层 | 不能为了方便手写长期维护成本高的框架、队列、SQL 映射、迁移和观测能力，也不能让第三方库类型污染业务层 | 新增依赖必须先更新 `Go技术选型与依赖基线.md`；业务模块不得直接 import chi、pgx、redis、asynq、validator、prometheus 等基础设施库 |

## 本阶段验收标准

- [x] `docs/migration/` 有 README 和示例文档。
- [x] 迁移原则、阶段、减法规则和网关最后策略已经写入文档。
- [x] Go 技术基线、目录结构、并发、线程安全、内存和大数据边界已经写入文档。
- [x] Go 框架、日志、配置、DB、SQL、job、测试、观测和安全扫描依赖基线已经写入文档。
- [x] PostgreSQL + Redis 单模式和 SQLite 移除范围已经写入文档。
- [x] 测试与验收策略覆盖公开接口、后台接口、worker、存储、网关、性能和删除验证。
- [x] 开发、构建、部署、Docker、服务化、配置和回滚调整已有规划。

## 后续迁移门禁

- [ ] 每个代码迁移模块都有独立验证记录和删除证据。
- [ ] Go 工程和 PostgreSQL / Redis 基线完成后，再允许模块进入 `已接管`。
- [ ] W0 必须完成依赖 PoC，包括 chi、config、slog、pgx、sqlc、goose、go-redis、Asynq、Prometheus、testcontainers、lint 和 govulncheck。
- [ ] W9 不删除 Node 网关 preflight；W10 整条网关灰度接管后统一删除 Node gateway。
- [ ] W11 发布收尾完成后，开发、部署、Docker、watchdog 和 env 文档不再把 SQLite / standalone 当正式路径。

## 当前 W0 验收状态

- [x] 本机 Go 版本固定到 `go1.26.4 windows/amd64`，`GOROOT=E:\gosdk\go1.26.4`。
- [x] Windows race 所需 C 工具链固定到 `E:\tools\w64devkit-2.8.0\w64devkit\bin`，并在系统 PATH 中排在旧 MinGW 前面。
- [x] `backend-go/` 已包含 Go module、server / worker / maintenance 命令入口、配置解析、slog 日志、chi 路由、健康检查、Prometheus metrics、PG/Redis/Asynq health adapter、goose baseline migration、sqlc 查询和生成包。
- [x] W0 基础设施封装已补：PG store 事务和 catalog 查询不向业务暴露 pgx/sqlc 类型；Redis client 支持 namespace key、TTL set、pipeline、`IncrWithTTL` 原子计数并校验 cache/state/queue 不指向同一 Redis DB；Asynq queue 支持 Redis URL / `rediss` TLS / 显式 timeout / enqueue / inspector / pending smoke。
- [x] W0 基础验证已通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、`golangci-lint run`、`govulncheck ./...`、命令入口和 health smoke。
- [x] `testcontainers` integration 测试代码已补：PostgreSQL + goose + sqlc baseline schema 查询、W1a public settings smoke、Redis health、fixed-window、penalty-window、Asynq enqueue / consume / retry / SkipRetry archive / retry exhaustion archive、W1b public API foundation smoke 和 W1b public API log Asynq smoke 均已有用例。
- [x] `juhe-ai-maintenance w0-smoke` 已补，缺少真实 PG/Redis/Asynq URL 时会 fail-fast，避免把依赖 `skipped` 当作 smoke 成功；配置真实 queue URL 时会执行 Asynq enqueue + inspector pending smoke。
- [ ] 基于 testcontainers 的 PostgreSQL / Redis / Asynq smoke 当前主线程因 Docker 不可用输出 `SKIP`，需在 Docker 健康环境复跑后再标记真实通过；独立真实外部 URL 的 `w0-smoke` 仍可作为部署环境验证命令保留。
- [ ] Asynq periodic task、worker crash recover、长任务 shutdown drain、队列 metrics 和 Redis 持久化边界仍待后续 W0/W7 专项补齐。

## 当前 W1a 验收状态

- [x] Node / 前端契约已梳理：`GET /__aisys__/api/settings/public` 匿名可读，前端期待 `{ data: { appName, appIcon } }`，失败时登录页回退默认品牌。
- [x] Go schema 已补 `juhe_business.global_settings` 和默认 seed：`appName = 聚合 AI`、`appIcon = /__aisys__/brand-icon.svg`。
- [x] Go route / service / store 已补：`backend-go/internal/modules/publicsettings`、`backend-go/internal/httpapi/public_settings.go`、`backend-go/internal/store/postgres`。
- [x] Go 单元测试已补：公开设置 HTTP envelope / no-store / 错误脱敏，公开设置 JSON 字符串解析。
- [x] Go W1a 已补系统 API IP 读限流基本语义：读取 `system_settings` 的读限流字段，按 IP 做 minute / burst 固定窗口，超限返回 `429`、`Retry-After` 和 `请求过于频繁，请稍后重试`。
- [x] Go W1a 已补 Redis state 原子限流代码：`go-redis` Lua 一次检查 minute / burst 两个 bucket，两个 bucket 都通过才提交，超限不递增；真实 server 启动路径必须配置 `JUHE_AI_REDIS_STATE_URL`，不会静默退回进程内限流。
- [x] Go W1a 已补客户端 IP 识别：`JUHE_AI_TRUST_PROXY=false` 时使用 socket `RemoteAddr`，启用后按可信反代链路解析 `X-Forwarded-For`；Redis state IP read rate limit 和 diagnostics loopback 判断使用解析后的客户端 IP。
- [x] Go W1a 已补独立 smoke 命令：`go run ./cmd/juhe-ai-maintenance w1a-public-settings-smoke`，用于真实 PostgreSQL public settings / rate limit settings、HTTP route 精确响应和 Redis state 跨 client 限流验证；缺少必要 URL 时 fail-fast。
- [ ] `TestW1aPublicSettingsSmoke` 当前主线程因 Docker 不可用输出 `SKIP`，需在 Docker 健康环境复跑后再标记真实通过；独立真实外部 URL 的 `w1a-public-settings-smoke` 仍待部署环境执行。
- [ ] 生产接管、Node 删除证据、反向代理路径切换和前端 smoke 均未执行。

## 验证记录

- 类型检查：不适用，W0 Go 工程使用 Go 验证矩阵替代 Node 类型检查。
- 构建：已执行 `go build ./...`，通过。
- 文档检查：已执行迁移入口检索、相对链接检查和 Go 目标存储方向检查；新增文档命名、入口索引和相对链接检查通过。
- 链接验证：已执行覆盖本次新增和同步修改文档的 Markdown 相对链接检查，结果为 `All checked markdown links resolve.`。
- 方向验证：已检查 `docs/migration/`、PLAN-0081、后端架构入口、存储功能文档和历史计划；旧 SQLite / 双模式内容均已标记为当前 Node 过渡事实或历史记录，Go 目标统一为 PostgreSQL + Redis 单模式。
- 依赖基线验证：已补充 Go 默认依赖、禁用依赖和 W0 PoC 门禁；迁移文档中 Redis Streams 只保留为“不要手写通用队列 / 专项 adapter 例外”的说明，Go 默认队列改为 Asynq。
- 多 agent 审阅：已按 10 个 agent 的审阅意见补齐关键风险，包括 PG/Redis 基线必须先于模块迁移、W9 不能提前删除 Node 网关准备层、可靠队列重试 / 死信 / 幂等边界、PgBouncer / pool / timeout 边界、W0-W11 验收矩阵和部署发布收尾清单。
- Go W0/W1a 验证：已执行 `sqlc generate`、`go mod tidy -diff`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`golangci-lint run`、`govulncheck ./...`、`go build ./...`；均通过，`golangci-lint` 为 `0 issues`，`govulncheck` 无已调用漏洞，`go mod tidy -diff` 无输出。W1a 覆盖公开设置 HTTP envelope / no-store / 错误脱敏、store JSON / 整数设置解析、IP read rate limit `429` / `Retry-After`、Redis limiter 注入、Redis fixed-window 参数校验、`JUHE_AI_TRUST_PROXY` 配置解析、客户端 IP 规范化、限流使用解析后 IP、diagnostics loopback 判断和 `w1a-public-settings-smoke` 缺配置 fail-fast。
- Go W0 smoke：已执行三个命令入口 `version`，均输出 `0.1.0-w0`；本地临时端口已验证 `/__aisys__/health`、`/__aisys__/api/health` 和 `/__aisys__/metrics`。
- Go W0/W1a/W1b integration：已执行 `go test -tags=integration ./internal/testkit/integration -count=1`，当前主线程输出 `SKIP`，原因是 Docker / testcontainers 不可用；不计为真实 PG/Redis 通过。本轮已执行 `go test -tags=integration ./internal/testkit/integration -run '^$'` 证明 integration 包可编译；已执行 `go test -v -tags=integration ./internal/testkit/integration -run TestW1bPublicAccountsShellE2E -count=1`，因 Docker 不可用输出 `SKIP`，不计为真实通过。待 Docker 健康环境复跑时，必须覆盖 `TestW0PostgresMigrationSmoke`、`TestW0RedisAndAsynqSmoke`、`TestW1aPublicSettingsSmoke`、`TestW1bPublicAPIFoundationPostgresSmoke`、`TestW1bPublicAPILogAsynqSmoke`、public group / route strategy / API Key / account PostgreSQL smoke、public group / route strategy / API Key / account shell E2E 和 `TestImagesArePinnedForW0`。
- Go W0 live smoke：已执行 `go run ./cmd/juhe-ai-maintenance w0-smoke` 的缺配置路径，确认缺少 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL`、`JUHE_AI_REDIS_QUEUE_URL` 时 fail-fast。
- Go W1a smoke：已执行 `go run ./cmd/juhe-ai-maintenance w1a-public-settings-smoke` 的缺配置路径断言，确认缺少 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_STATE_URL` 时 fail-fast 且不输出成功 JSON；真实 PG/Redis 执行未完成。
- Go W1b smoke：已新增 `go run ./cmd/juhe-ai-maintenance w1b-public-api-smoke`，并通过单元测试确认缺少 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_STATE_URL`、`JUHE_AI_REDIS_QUEUE_URL` 或少于 32 字符的 `JUHE_AI_SECRET` 时 fail-fast 且不输出成功 JSON；默认 router guard 保持关闭；输出固定 `takeoverEvidence=false` 和 `productionTakeoverNotEvaluated=true`。真实 PostgreSQL / Redis / `juhe-ai-worker ingest` 执行未完成。
- 跨平台构建：已执行 `GOOS/GOARCH=windows/amd64`、`linux/amd64`、`darwin/amd64` + `CGO_ENABLED=0` 的 `go build ./...`，均通过。
- Node W1a 对照：已执行 `pnpm --filter juhe-ai-backend test:settings-public-driver`，通过，确认公开设置只暴露 `appName/appIcon`；已执行 `pnpm --filter juhe-ai-backend test:request-context-client-ip`，通过，确认 Node client IP 由 Express trust proxy 后的 `req.ip` 决定，不直接信任转发头；已执行 `pnpm --filter juhe-ai-backend test:system-api-rate-limit`，通过，确认 `/settings/public` 读限流、`429`、`Retry-After`、health bypass 和 IP 白名单语义。`pnpm --filter juhe-ai-backend test:db-service-system-api-http` 当前未通过，复现为 `settings/public` 返回 500；调试日志显示 system API 限流读取系统设置时 SQLite read worker 30s 超时，属于当前 Node 对照链路未稳定通过，不是 Go W1a 代码失败。
- W1b 契约固定：已用多个 gpt-5.5 xhigh 子 agent 做只读交叉梳理，新增 `docs/migration/W1b-外部维护公开接口迁移记录.md`；固定当前 16 个 `/__aipublic__` 公开 CRUD、旧 5 个公开路径继续 404、Bearer-only 鉴权、source/token scope 交集、source 级限频、60 秒 `last_used_at` 节流、公开接口日志 32KB 快照边界、资源字段和 Node 对照命令。已执行并通过 `pnpm test:external-source-auth`、`pnpm test:public-api-logs`、`pnpm --filter juhe-ai-backend test:external-integration-source-expires-at`、`pnpm --filter juhe-ai-backend test:external-integration-source-async-boundary`、`pnpm --filter juhe-ai-backend test:external-public-account-push-async-boundary`、`pnpm --filter juhe-ai-backend test:public-api-log-db-service-ipc` 和 `pnpm test:sqlite-query-regression`；W1b PG/Redis Node 对照 smoke、生产路由切流和 Node 删除仍待执行。
- Go W1b 基础设施与四类公开资源纵切面：已新增 `backend-go/internal/modules/publicapi`、`backend-go/internal/modules/publicapi/auth`、`backend-go/internal/modules/publicapi/ratelimit`、`backend-go/internal/modules/publicapilog`、`backend-go/internal/modules/publicgroups`、`backend-go/internal/modules/publicroutestrategies`、`backend-go/internal/modules/publicapikeys`、`backend-go/internal/modules/publicaccounts`、`backend-go/internal/jobs/publicapilog`、`backend-go/internal/jobs/worker`、`backend-go/internal/app/server.go`、`backend-go/internal/app/ingest_worker.go`、`backend-go/internal/config/config.go`、`backend-go/internal/httpapi/router.go`、`backend-go/internal/httpapi/public_api_shell.go`、`backend-go/internal/httpapi/public_groups.go`、`backend-go/internal/httpapi/public_route_strategies.go`、`backend-go/internal/httpapi/public_api_keys.go`、`backend-go/internal/httpapi/public_accounts.go`、`backend-go/internal/store/port/publicapi.go`、`backend-go/internal/store/postgres/publicapi.go`、`backend-go/internal/store/postgres/publicgroups.go`、`backend-go/internal/store/postgres/publicroutestrategies.go`、`backend-go/internal/store/postgres/publicapikeys.go`、`backend-go/internal/store/postgres/publicaccounts.go`、`backend-go/internal/store/postgres/queries/w1b_public_groups.sql`、`backend-go/internal/store/postgres/queries/w1b_public_route_strategies.sql`、`backend-go/internal/store/postgres/queries/w1b_public_api_keys.sql`、`backend-go/internal/store/postgres/queries/w1b_public_accounts.sql`、`backend-go/internal/platform/redis` penalty-window helper、`backend-go/internal/maintenance/w1bpublicapismoke.go`、`backend-go/db/migrations/000003_w1b_public_api_foundation.sql`、`backend-go/db/migrations/000004_w1b_public_groups.sql`、`backend-go/db/migrations/000005_w1b_public_accounts.sql` 和 W1b integration smoke；覆盖 16 个 `/__aipublic__` method/path/scope、旧 5 个公开路径不进入 catalog、`Bearer` 解析、token hash namespace、source/token 状态与过期、scope 交集、auth error code/message/status、`last_used_at` 60 秒 touch 判断、source/token rate-limit key、公开接口日志 32KB snapshot / dropped / 499 / error info、Asynq payload/enqueue/handler、`juhe-ai-worker ingest` runtime、invalid payload `SkipRetry` 映射、Asynq import guard、PostgreSQL auth/log adapter、Redis penalty-window 限频、HTTP shell 中 16 个 catalog endpoint 进入 auth / limiter / injected handler、默认关闭 / 显式开启 router guard、`w1b-public-api-smoke` 本地 httptest + 真实依赖验证入口、旧公开路径 404 不鉴权但写日志、JSON body 400、body 413、429 `Retry-After`、response capture、客户端提前断开 499、ResponseWriter 可选接口透传、PublicGroup / PublicRouteStrategy / PublicAPIKey / PublicAccount store port 不暴露 pgx/sqlc/Redis 类型、四类资源 handler/service/store/query/schema 纵切面、AI 账户 provider profile、凭据 AES-GCM 加密、凭据指纹 hash、模型替换、pending_test 直启拦截、软删除和绑定清理，以及完整 key / query string / 来源 token / 上游 apiKey / URL userinfo 日志脱敏。account shell E2E 测试代码已补，覆盖 add/list/update/delete/limited、source/token last_used、账号凭据加密落库、软删除和 public API log 落库脱敏。已执行并通过 `sqlc generate`、`go mod tidy -diff`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/...`、`go test -tags=integration ./internal/testkit/integration -run '^$'`、`go test ./internal/modules/publicaccounts ./internal/httpapi ./internal/store/postgres ./internal/modules/publicapilog`、`go test ./internal/config ./internal/httpapi ./internal/app` 和本轮 `go test ./internal/maintenance ./internal/app`；真实 Docker/testcontainers integration、`w1b-public-api-smoke` 真实依赖执行、account shell E2E 真实复跑、生产路由切流和 Node 删除仍待完成。
- Go W2 管理只读当前已迁移路径：已新增 `backend-go/db/migrations/000006_w2_management_proxy_options.sql`、`000007_w2_management_session_auth.sql`、`000008_w2_management_provider_options.sql`、`000009_w2_management_provider_model_catalog.sql`、`000010_w2_management_account_tags.sql`、`000011_w2_management_group_authorizations.sql`、`000012_w2_management_account_name_search.sql`、`000013_w2_management_account_authorization_instances.sql`，以及 management auth/proxy/provider/model/route strategy/group/account 的 store port、PostgreSQL adapter、sqlc query、service、handler、router/app 注入和 integration smoke。当前覆盖 `proxies/options`、`providers/options`、`providers/models/options`、`providers/{code}/models`、`route-strategies/options`、`my-route-strategies/options`、`groups/options`、`my-groups/options`、`groups/account-options`、`my-groups/account-options`、`accounts/options`、`my-accounts/options`、`accounts/tags` 和 `my-accounts/tags`；其中分组 options / account-options 已覆盖 owner + 授权组只读 union、`manageableOnly=true` 排除授权组、authorized permissions、授权组 `accountIds=[]` 和管理员全局不重复授权行，`accounts/options` 已覆盖 owner + 授权账户只读 union、`accessType=authorized`、授权字段、authorized permissions、owner `tagIds` 命中任一标签过滤和名称包含搜索，`accounts/tags` / `my-accounts/tags` 已覆盖 owner 标签字典、未删除账号绑定计数、admin / self 作用域和 router opt-in guard。已通过 `sqlc generate`、`go mod tidy -diff`、管理端相关 Go 快速测试、`go test ./...` 和 integration 包编译；真实 Docker/testcontainers integration、授权来源 / grant / 授权写接口、标签写接口、生产路由切流、前端 smoke 和 Node 删除仍待完成。
- W2 Node 对照：已通过 `pnpm --filter juhe-ai-backend test:route-strategy-list-query-guard`、`pnpm --filter juhe-ai-backend test:api-key-route-validation`、`pnpm --filter juhe-ai-backend test:system-api-read-burst` 和 `pnpm --filter juhe-ai-backend test:group-options-lightweight`；`pnpm --filter juhe-ai-backend test:scope` 当前失败，失败点为“用户 A 应能打开自有账户详情并读取非敏感 Base URL”，属于 Node 旧链路账户详情断言，未作为本轮 Go W2 group account-options 的通过证据。
- Go 系统指标规划：已新增 `docs/migration/Go迁移指标与观测规划.md`，并同步迁移 README、迁移总览、模块减法清单、Go 架构基线、技术选型、测试验收、开发部署、接口契约、统计分层设计和后端架构入口；本轮补充 `docs/migration/Go系统指标字段迁移清单.md`，把 Node `eventLoopLagMs`、`process_event_loop_*`、`processEventLoop*`、`db-service`、V8 heap、SQLite 文件体积和 usage shard 文件路径指标列为 Go 接管时的字段级删除门禁，并明确 Go runtime、PG、Redis、Asynq、worker lag、stats freshness、网关 SLI 和前端系统监控页面替换要求。
- 文档验证：已执行覆盖本次新增 W1b / W2 文档、迁移入口、迁移清单、测试策略和 PLAN-0081 的相对链接检查，结果为 `All checked markdown links resolve.`；已执行 `git diff --check`，仅有既有 LF/CRLF 提示，无 whitespace error。本轮 W2 账户 options 授权账户只读 union 文档同步后已再次执行本次改动 Markdown 相对链接检查，结果为 `All checked markdown links resolve.`。
- 未验证项：独立真实外部 URL 的 `w0-smoke` / `w1a-public-settings-smoke` / `w1b-public-api-smoke` 部署环境执行、W2 `TestW2ManagementProxyOptionsPostgresSmoke` / `TestW2ManagementAuthPostgresSmoke` / `TestW2ManagementProviderOptionsPostgresSmoke` / `TestW2ManagementProviderModelsPostgresSmoke` / `TestW2ManagementRouteStrategiesPostgresSmoke` / `TestW2ManagementGroupOptionsPostgresSmoke` / `TestW2ManagementGroupAccountOptionsPostgresSmoke` / `TestW2ManagementAccountOptionsPostgresSmoke` 真实 Docker/testcontainers 执行、反向代理切流 smoke、前端品牌加载 smoke、Node public settings 删除证据、W1b Node PG/Redis 对照 smoke、Go W1b / W2 生产路由挂载、W2 授权来源 / grant / 授权写接口、标签写接口、前端 W2 options / tags smoke、account shell E2E 真实 Docker/testcontainers 复跑、Docker/testcontainers 真实 integration、Node 删除证据和网关测试均待后续代码迁移阶段执行。

## 风险与注意事项

- 风险 1：Go 迁移不能误以为“goroutine 等于无限并发”，PostgreSQL 连接池、Redis 队列、上游账号并发和队列容量仍需硬边界。
- 风险 2：双运行时过渡容易残留旧实现，必须坚持单 owner 和删除证据。
- 风险 3：SQLite 数据若需要保留，必须单独离线导入 PostgreSQL，不能把双读双写或启动迁移写进运行路径。
- 风险 4：真实网关迁移风险最高，必须在存储、worker、协议测试、SSE 和性能验证成熟后执行。
- 风险 5：public group、public route strategy、public API Key、public account 四条资源纵切面、`JUHE_AI_PUBLIC_API_ENABLED` opt-in guard 和 `w1b-public-api-smoke` 只是 W1b 灰度入口，容易被误判为 `/__aipublic__` 已接管；必须保持默认关闭，直到 16 个 CRUD、account shell E2E 在真实 Docker/testcontainers 环境通过、PG+Redis smoke、公开日志副作用、反向代理单 owner 切流和 Node 删除证据整体完成。
- 风险 6：W2 管理只读接口如果绕过 Node 当前 `requireAuth` / `requireAdmin` / `my-*` 作用域，会把后台数据裸露出来；因此当前 `proxies/options`、`providers/options`、`route-strategies/options`、`my-route-strategies/options`、`groups/options`、`my-groups/options`、`groups/account-options`、`my-groups/account-options`、`accounts/options`、`my-accounts/options`、`accounts/tags` 和 `my-accounts/tags` 虽已补 Go `requireAuth` 会话鉴权基线、admin-only 管理侧下拉、self-only 用户侧下拉、分组授权组只读 union 和账户授权账户只读 union，也仍必须由 `JUHE_AI_MANAGEMENT_API_ENABLED=false` 默认关闭。显式开启只允许灰度验证已迁移路径，不能替代授权来源 / grant / 授权写接口、标签写接口、前端 smoke、单 owner 切流和 Node 删除证据。
- 风险 7：系统指标如果只是把 Node 事件循环字段改名为 Go 字段，会造成错误观测和误告警；Go 接管时必须把系统指标统计作为 W6 / W7 独立分块，先落地 Go 原生采样、低基数 Prometheus 标签、PG 窗口表、内部系统监控 DTO、前端契约和旧字段删除证据。
- 发布异常处理：每个模块生产切换前保留上一版发布包、配置和数据备份；schema 变化不依赖运行时自动回滚。

## 完成总结

- 完成时间：待后续迁移全部完成后填写。
- 实际完成内容：当前完成 M0 文档规划，W0 Go 常规工程基线已落地，W1a 公开设置读接口已进入 Go 实现中，W1b 已补 catalog/auth、PG auth/log adapter、Redis penalty-window、公开接口日志 snapshot/log builder、Asynq payload/enqueue/handler、public API log 单任务 worker runtime、HTTP shell / capture 契约、499 客户端提前断开捕获、ResponseWriter 可选接口透传、public group CRUD、public route strategy CRUD、public API Key CRUD、public account CRUD 四条真实资源纵切面、默认关闭的生产 router opt-in guard，以及 `w1b-public-api-smoke` 独立 maintenance smoke；W2 当前 `proxies/options`、`providers/options`、`providers/models/options`、`providers/{code}/models`、`route-strategies/options`、`my-route-strategies/options`、`groups/options`、`my-groups/options`、`groups/account-options`、`my-groups/account-options`、`accounts/options`、`my-accounts/options`、`accounts/tags` 和 `my-accounts/tags` 已补 Go schema / 既有表复用 / store / service / handler / `requireAuth` 会话鉴权 / admin / self 作用域 / `JUHE_AI_MANAGEMENT_API_ENABLED` router opt-in guard；其中分组选项已覆盖 owner + 授权组只读 union，账户 options 已覆盖 owner + 授权账户只读 union、owner `tagIds` 过滤和名称包含搜索，账户 tags 仍是 owner-only 只读；Go 系统指标与观测迁移规划已补齐并明确 W6 / W7 独立分块门禁；业务接口尚未正式生产接管。
- 主要改动位置：`docs/migration/`、`docs/plans/计划-0081-Node转Go渐进减法迁移.md`、`backend-go/`。
- 验证结果：W0 常规 Go 验证矩阵、race、lint、漏洞扫描、命令入口、health smoke、public group / public route strategy / public API Key / public account handler / service 非容器专项、W2 `managementauth` / `managementproxies` / `managementproviders` / `managementprovidermodels` / `managementroutestrategies` / `managementgroups` / `managementaccounts` / `httpapi` / `store/postgres` / `config` / `app` 快速测试和跨平台构建已记录；本轮 `sqlc generate`、`go mod tidy -diff`、`gofmt`、`go test ./internal/modules/managementaccounts ./internal/store/postgres ./internal/httpapi ./internal/app`、`go test ./...`、`go test -tags=integration ./internal/testkit/integration -run '^$'`、`pnpm --filter juhe-ai-backend test:account-options-lightweight`、`pnpm --filter juhe-ai-backend test:account-list-query-guard` 和 Markdown 相对链接检查已通过；`go test -v -tags=integration ./internal/testkit/integration -run TestW2Management -count=1` 因 Docker/testcontainers 不可用输出 `SKIP`，覆盖 W2 proxy options、provider options、provider model catalog、route strategy options、group options、group account options、account options / tags 和 management auth smoke 入口；真实 PG/Redis/Asynq integration 仍需 Docker 健康环境复跑。Node 对照中 route strategy query guard、API Key route validation、system API read burst、group options lightweight、account options lightweight 和 account list query guard 已通过；`test:scope` 仍失败在 Node 账户详情 Base URL 断言，需后续单独处理。
- 后续建议：下一步在具备 Docker 或真实 PG/Redis 连接串的环境执行 W0/W1a/W1b integration、W0 live smoke、`w1a-public-settings-smoke`、`w1b-public-api-smoke` 和 W1b account shell E2E 真实复跑；随后评估生产 router 挂载、反向代理切流和 Node 删除证据。
