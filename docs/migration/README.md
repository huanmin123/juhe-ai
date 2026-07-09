# Go 渐进减法迁移目录

> 面向 AI、维护者和后续迁移执行者。
> 本目录集中维护 `juhe-ai` 从 Node.js + TypeScript 后端迁移到 Go 后端的长期规则、顺序、验收和部署调整。

## 1. 目录目标

- 把迁移目标、迁移顺序、删除规则、Go 技术基线和验证要求固定下来，避免后续只靠对话记忆推进。
- 支持“渐进式 + 减法迁移”：每迁移一个模块，就让该模块只有一个运行时 owner，并删除对应 Node 旧实现。
- 先迁移公开接口和后台管理接口，最后迁移真实中转协议网关。
- 把 Go 能天然简化的 Node 事件循环、阻塞规避、worker thread、SQLite 单写者治理和 IPC 复杂度提前列为删除对象。
- 明确 Go 不是无界并发：PostgreSQL 连接池、Redis 队列、上游账号并发、队列容量、请求体大小和 goroutine 生命周期仍必须有边界。

## 2. 首次阅读顺序

1. [迁移规划总览](迁移规划总览.md)：迁移原则、阶段、减法规则和整体边界。
2. [Go 后端架构基线](Go后端架构基线.md)：目标目录、进程模型、并发模型和线程安全规则。
3. [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)：Go 框架、日志、配置、DB、SQL、job、测试、观测和安全扫描的默认依赖。
4. [存储目标与 SQLite 移除](存储目标与SQLite移除.md)：PostgreSQL + Redis 单模式目标、SQLite 删除范围和数据边界。
5. [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)：系统指标从 Node 事件循环口径切换到 Go runtime、PG/Redis/Asynq 和网关观测口径的规划。
6. [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)：W6 / W7 / W8 执行系统监控迁移时逐项删除 Node 字段、替换 Go 字段和验证前端契约的清单。
7. [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md)：模块迁移波次、删除条件和验收门禁。
8. [W1b 外部维护公开接口迁移记录](W1b-外部维护公开接口迁移记录.md)：`/__aipublic__` 外部维护接口的当前契约、Node 对照命令、Go 目标边界和删除门禁。
9. [W2 管理端只读辅助接口迁移记录](W2-管理端只读辅助接口迁移记录.md)：后台 options / catalog 接口和账号标签切片的当前契约、已迁移路径、系统账户轻量下拉、authorization grantee accounts / grantee teams / grantee groups、分组授权组只读 union、账户授权账户只读 union、账号标签 owner-only 只读 / 未绑定删除 / 独立 PATCH opt-in、主账户标签写路径和 operation log 缺口、接管门禁。
10. [W3 登录与系统账户迁移记录](W3-登录与系统账户迁移记录.md)：登录、当前用户、会话、登出、改密、验证码和系统账户写接口的分块迁移记录；当前覆盖 `GET /auth/captcha` 验证码发放 / 校验基础、`POST /auth/login` 登录 / session 创建小切片、`GET /auth/me` 读切片、`PATCH /auth/me` 当前用户资料更新切片、`POST /auth/change-password` 当前用户改密切片、`POST /auth/logout` 当前会话撤销切片、`GET /auth/sessions` 当前用户会话列表、`DELETE /auth/sessions/{id}` 当前用户单条会话撤销、`POST /system-accounts` 创建切片，以及 `PATCH /system-accounts/{id}` 完整 mixed partial update。全部仍为 Go opt-in 灰度路径，不代表 W3、Node `/auth` 或 Node `/system-accounts` 已接管。
11. [W4 团队与统一授权迁移记录](W4-团队与统一授权迁移记录.md)：系统团队、成员、授权 grant、授权来源展开和最终用户授权的分块迁移记录；当前覆盖 `GET /system-teams` / `GET /my-teams` 团队列表 / 详情读接口、`POST /system-teams` 团队创建、`PATCH /system-teams/{id}` 团队更新 Go opt-in 灰度能力、授权来源 / grant / 额度窗口 / 统计脏标记 PostgreSQL schema 基线和授权缓存失效，不代表 W4、Node `/system-teams` 或 Node `/authorizations` 已接管。
12. [测试与验收策略](测试与验收策略.md)：契约测试、回归矩阵、性能验证和网关专项验收。
13. [开发构建部署调整](开发构建部署调整.md)：本地开发、构建、发布包、Docker 和常驻运行的迁移安排。
14. [迁移文档示例](迁移文档示例.md)：后续单模块迁移记录的写法示例。

## 3. 目录职责

| 文档 | 职责 |
| --- | --- |
| `README.md` | 迁移目录入口、阅读顺序和维护规则 |
| `迁移规划总览.md` | 长期迁移策略、阶段、边界和不做事项 |
| `Go后端架构基线.md` | Go 目标架构、依赖选择、并发、线程安全和内存治理 |
| `Go技术选型与依赖基线.md` | Go 框架、日志、配置、DB、SQL、job、测试、观测和安全扫描的默认依赖与禁用依赖 |
| `存储目标与SQLite移除.md` | PostgreSQL + Redis 单模式、SQLite 删除清单、离线数据处理和验证要求 |
| `Go迁移指标与观测规划.md` | Go 目标系统指标、Prometheus、pprof、PG/Redis/Asynq、worker 和网关观测口径 |
| `Go系统指标字段迁移清单.md` | Node 系统指标字段删除、Go 字段替换、前端页面迁移和 W6 / W7 / W8 验收清单 |
| `模块迁移顺序与减法清单.md` | 模块优先级、迁移状态、Node 删除证据和测试门禁 |
| `W1b-外部维护公开接口迁移记录.md` | `/__aipublic__` 外部维护接口契约、Go 迁移范围、Node 对照命令和删除门禁 |
| `W2-管理端只读辅助接口迁移记录.md` | 管理端只读辅助接口与账号标签只读 / 未绑定删除 / 独立 PATCH 契约、Go 当前实现范围、权限边界、系统账户轻量下拉、authorization grantee accounts / grantee teams / grantee groups、分组授权组只读 union、账户授权账户只读 union、主账户标签写路径 / 完整 summary / operation log 缺口和删除门禁 |
| `W3-登录与系统账户迁移记录.md` | 登录、当前用户、会话、登出、改密、验证码和系统账户写接口迁移记录；当前固定 `GET /auth/captcha` 验证码发放 / 校验基础、`POST /auth/login` 登录 / session 创建小切片、`GET /auth/me` 只读切片、`PATCH /auth/me` 当前用户资料更新切片、`POST /auth/change-password` 当前用户改密切片、`POST /auth/logout` 当前会话撤销切片、`GET /auth/sessions` 当前用户会话列表、`DELETE /auth/sessions/{id}` 当前用户单条会话撤销、`POST /system-accounts` 创建切片、`PATCH /system-accounts/{id}` 完整 mixed partial update 和后续拆分门禁 |
| `W4-团队与统一授权迁移记录.md` | 团队与统一授权迁移记录；当前固定团队列表 / 详情读接口、`POST /system-teams` 团队创建、`PATCH /system-teams/{id}` 团队更新 Go opt-in 灰度能力、授权来源 / grant / 额度窗口 / 统计脏标记 PostgreSQL schema 基线和授权缓存失效，后续继续拆成员增删、授权 grant 写接口、授权来源展开 / 归还 / 回收 / 到期和删除门禁 |
| `测试与验收策略.md` | 单模块、系统、网关、性能、安全和发布验收 |
| `开发构建部署调整.md` | 开发环境、命令、包结构、部署脚本和平台差异 |
| `迁移文档示例.md` | 后续新增单模块迁移记录时的参考格式 |

## 4. 维护规则

- 任何 Go 迁移任务开始前，先确认本目录和 `docs/plans/计划-0081-Node转Go渐进减法迁移.md`。
- 每迁移一个模块，必须更新 [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 的状态、Node 删除证据和测试结果。
- 影响 Go 目录结构、并发模型、进程模型或存储 owner 时，更新 [Go 后端架构基线](Go后端架构基线.md)。
- 影响 Go 框架、日志、配置、SQL、job、测试、观测、安全扫描或外部依赖时，先更新 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)，再改代码。
- 影响系统指标、Prometheus、pprof、worker lag、队列状态、PG/Redis/Asynq 观测或前端系统监控契约时，先更新 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md) 和 [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)，不能把 Node `eventLoopLagMs` / `db-service` 字段模拟成 Go 指标。
- 影响 PostgreSQL、Redis、SQLite 移除、数据导入导出或存储部署时，更新 [存储目标与 SQLite 移除](存储目标与SQLite移除.md)。
- 影响本地启动、安装、构建、发布包、Docker、服务化或回滚方式时，更新 [开发构建部署调整](开发构建部署调整.md)，并同步 `docs/develop/` 或 `docs/deploy/` 对应当前手册。
- 影响公开 API、管理 API、权限、安全、统计、存储或网关语义时，同步更新 `docs/functions/` 下对应功能文档。
- 影响当前真实架构事实时，同步更新 `docs/architecture/架构总览.md` 和 `docs/architecture/backend/README.md`。

## 5. 边界说明

- 迁移期间，前端仍按 Vue 3 + TypeScript + Ant Design Vue 维护。
- Go 迁移优先覆盖后端运行时；前端 API 调用契约要通过测试证明未缺失，但不因迁移重做前端信息架构。
- “不向下兼容”指不为旧 Node 内部结构、旧 schema、旧 repository 或旧 IPC 保留运行时兼容分支；对当前产品公开契约和用户可见行为，迁移必须做到等价或在文档中明确记录新契约。
- 迁移目标不再保留 SQLite standalone / PostgreSQL performance 两套模式。Go 后端目标只有 PostgreSQL + Redis；SQLite 只作为当前 Node 旧实现和离线导出来源存在。
- 迁移中允许测试环境短期存在 Node 与 Go 两个服务按路径分流，但同一接口、同一模块或同一后台任务不能长期双 owner。
