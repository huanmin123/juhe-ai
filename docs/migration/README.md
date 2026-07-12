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
11. [W4 团队与统一授权迁移记录](W4-团队与统一授权迁移记录.md)：系统团队、成员、授权 grant、授权来源展开和最终用户授权的分块迁移记录；当前覆盖 `GET /system-teams` / `GET /my-teams` 团队列表 / 详情读接口、`POST /system-teams` 团队创建、`PATCH /system-teams/{id}` 团队更新、`POST /system-teams/{id}/members` 成员新增、`DELETE /system-teams/{id}/members/{memberId}` 成员移除、`GET /authorizations` 和 `GET /my-authorizations` 授权列表、`GET /authorizations/{id}` 和 `GET /my-authorizations/{id}` 授权详情、`POST /authorizations` 和 `POST /my-authorizations` 授权创建、`PATCH /authorizations/{id}` 和 `PATCH /my-authorizations/{id}` 授权更新、`PATCH /authorizations/{id}/expire` 和 `PATCH /my-authorizations/{id}/expire` 授权有效期更新、`DELETE /authorizations/{id}/return` 和 `DELETE /my-authorizations/{id}/return` 授权归还、`DELETE /authorizations/{id}` 和 `DELETE /my-authorizations/{id}` 授权回收 Go opt-in 灰度能力、授权来源 / grant / 额度窗口 / 统计脏标记 PostgreSQL schema 基线和授权缓存失效，不代表 W4、Node `/system-teams` 或 Node `/authorizations` 已接管。
12. [W5 管理端全局品牌设置读取记录](W5-管理端全局品牌设置读取记录.md)：`GET/PATCH /__aisys__/api/settings/global` 的 Go opt-in 契约、`publicsettings` / store 复用、管理员权限、读写 session、两层限流、精确品牌 DTO、验证记录和删除门禁。
13. [W5 管理端系统运行设置迁移记录](W5-管理端系统运行设置迁移记录.md)：已进入 Go opt-in 的 `GET/PATCH /__aisys__/api/settings`，固定 55 key，新增 `gptPriorityPriceMultiplier=2` 与 `gptFlexPriceMultiplier=0.5`（范围均为 `0.01..100`），并覆盖 `256 KiB` / `413`、parser 与鉴权 / 限流顺序、PostgreSQL 有界事务、`000024` 初始设置 seed、`000034` 增量 GPT 倍率 seed、双缓存失效、操作日志和删除门禁；真实依赖因 Docker 不可用输出 `SKIP` 时不计通过。
14. [W5 管理端分组创建迁移记录](W5-管理端分组创建迁移记录.md)：已进入 Go opt-in 的 `POST /groups` 与 `POST /my-groups` 创建契约、作用域、完整高并发策略、唯一约束、写后副作用、验证记录和删除门禁。
15. [W5 管理端分组列表迁移记录](W5-管理端分组列表迁移记录.md)：`GET /groups` 与 `GET /my-groups` 的权限、分页、progressive DTO、预聚合读取、共存期 Node 单 writer 和最终 Go stats worker 门禁。
16. [W5 管理端分组详情迁移记录](W5-管理端分组详情迁移记录.md)：`GET /groups/{id}` 与 `GET /my-groups/{id}` 的 owner / authorized 详情 DTO、实时账户并发、授权来源、权限和真实依赖门禁。
17. [W5 管理端分组更新迁移记录](W5-管理端分组更新迁移记录.md)：`PATCH /groups/{id}` 与 `PATCH /my-groups/{id}` 的 owner / authorized 字段边界、事务保护、路由绑定保护、缓存与运行态失效、操作日志和真实依赖门禁。
18. [W5 管理端分组删除迁移记录](W5-管理端分组删除迁移记录.md)：`DELETE /groups/{id}` 与 `DELETE /my-groups/{id}` 的 owner-only 权限、默认分组和路由策略保护、硬删除级联、统计脏标记、缓存与运行态失效、操作日志和真实依赖门禁。
19. [W5 管理端策略路由列表与详情迁移记录](W5-管理端策略路由列表与详情迁移记录.md)：管理 / 个人四条 GET 的 admin global / owner narrowing、self 强制本人、渐进分页、大小写敏感名称前缀、轻量列表、完整详情和真实依赖门禁；当前仅为 Go opt-in 迁移中，不代表生产接管。
20. [W5 管理端 API Key 密钥生命周期迁移记录](W5-管理端APIKey密钥生命周期迁移记录.md)：管理端 / 个人端 API Key 创建、完整密钥查看与刷新、加密存储、权限、缓存失效、操作日志和真实依赖门禁。
21. [W5 管理端 API Key 删除迁移记录](W5-管理端APIKey删除迁移记录.md)：`DELETE /api-keys/{id}` 与 `DELETE /my-api-keys/{id}` 的作用域、204 空响应、原子硬删除、cleanup target、提交后失效、操作日志、残余安全风险和真实依赖门禁。
22. [W6 记录与统计读接口迁移记录](W6-记录与统计读接口迁移记录.md)：记录、日志和统计只读接口迁移记录；当前仅覆盖管理侧 / 个人侧 `usage-window` 固定 31 天窗口 Go opt-in。
23. [W6 System API 限流对齐记录](W6-System-API限流对齐记录.md)：system API IP read / write、已认证用户 read / write、client IP allowlist bypass、缓存失效、验证和剩余 Node 差异。
24. [测试与验收策略](测试与验收策略.md)：契约测试、回归矩阵、性能验证和网关专项验收。
25. [开发构建部署调整](开发构建部署调整.md)：本地开发、构建、发布包、Docker 和常驻运行的迁移安排。
26. [迁移文档示例](迁移文档示例.md)：后续单模块迁移记录的写法示例。

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
| `W4-团队与统一授权迁移记录.md` | 团队与统一授权迁移记录；当前固定团队列表 / 详情读接口、`POST /system-teams` 团队创建、`PATCH /system-teams/{id}` 团队更新、`POST /system-teams/{id}/members` 成员新增、`DELETE /system-teams/{id}/members/{memberId}` 成员移除、`GET /authorizations` 和 `GET /my-authorizations` 授权列表、`GET /authorizations/{id}` 和 `GET /my-authorizations/{id}` 授权详情、`GET /authorizations/usage/team-details` / `user-details` 与 `my-authorizations` 对应 usage overview、`POST /authorizations` 和 `POST /my-authorizations` 授权创建、`PATCH /authorizations/{id}` 和 `PATCH /my-authorizations/{id}` 授权更新、`PATCH /authorizations/{id}/expire` 和 `PATCH /my-authorizations/{id}/expire` 授权有效期更新、`DELETE /authorizations/{id}/return` 和 `DELETE /my-authorizations/{id}/return` 授权归还、`DELETE /authorizations/{id}` 和 `DELETE /my-authorizations/{id}` 授权回收 Go opt-in 灰度能力、授权来源 / grant / 额度窗口 / 统计脏标记 / usage window PostgreSQL schema 基线和授权缓存失效，后续继续拆 `:id/usage` 明细、usage window 刷新 worker、批量到期扫描 worker 和删除门禁 |
| `W5-管理端全局品牌设置读取记录.md` | W5 `GET/PATCH /settings/global` 管理端品牌设置读写切片；固定管理 API 默认关闭、管理员权限、读写 session、两层 read/write 限流、精确品牌 DTO、PostgreSQL 事务、`settings:global` shared cache version 和 `settings.update_global` operation log；与系统运行设置纵切面分开登记，明确排除生产切流和 Node 删除 |
| `W5-管理端系统运行设置迁移记录.md` | W5 `GET/PATCH /settings` 已进入 Go opt-in 的系统运行设置纵切面；固定 55 key，新增 `gptPriorityPriceMultiplier=2` 与 `gptFlexPriceMultiplier=0.5`（范围均为 `0.01..100`），并覆盖 `256 KiB` / `413`、IP limiter 后且 auth / touch / user limiter 前的 PATCH parser、GET read auth 不 touch、PATCH touch、PostgreSQL 固定有界读取 / `FOR UPDATE` / 稳定 key / 完整 snapshot、migration `000024` 的初始设置与统计时区 seed、migration `000034` 的增量 GPT 倍率 seed、PostgreSQL 在线禁改时区、`settings:system` / `settings_updated` 双失效和 `settings.update` operation log；真实依赖 smoke 因本机无 Docker 输出 `SKIP` 时不计通过，不代表生产接管 |
| `W5-管理端分组创建迁移记录.md` | W5 `POST /groups` 与 `POST /my-groups` 已进入 Go opt-in；固定 admin / self owner 作用域、strict body、`256 KiB`、个人 / 高并发默认值、完整 16 字段策略 JSON、数据库唯一约束、`201` 基础摘要、gateway runtime 失效与 `groups.create` operation log best-effort；integration 代码已补，真实依赖仍待健康 Docker 环境复跑；明确排除列表、详情、更新、删除和生产接管 |
| `W5-管理端分组列表迁移记录.md` | W5 `GET /groups` 与 `GET /my-groups` 设计；固定 admin / self 作用域、1000 行 progressive pagination、owner / authorized union、稳定排序、轻量 DTO、预聚合 stats / usage 批量读取、共存期 Node 单 writer 和最终 Go stats worker 删除门禁 |
| `W5-管理端分组详情迁移记录.md` | W5 `GET /groups/{id}` 与 `GET /my-groups/{id}` 已进入 Go opt-in；固定 admin / self 作用域、owner / authorized 可见性、owner `accountIds` 与 Redis v2 实时并发、authorized 账户 ID 隐藏与预聚合统计、完整授权来源、两层 read limiter、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端分组更新迁移记录.md` | W5 `PATCH /groups/{id}` 与 `PATCH /my-groups/{id}` 已进入 Go opt-in；固定 owner / authorized 字段边界、strict partial JSON、PostgreSQL 事务与路由绑定保护、授权本地设置、完整详情回读、shared cache / runtime 失效、`groups.update` operation log、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端分组删除迁移记录.md` | W5 `DELETE /groups/{id}` 与 `DELETE /my-groups/{id}` 已进入 Go opt-in；固定 owner-only、authorized 404、默认分组和全作用域路由策略保护、硬删除级联、事务内统计脏标记、双 shared cache / runtime 失效、`groups.delete` operation log、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端策略路由列表与详情迁移记录.md` | W5 策略路由管理 / 个人四条 GET 迁移记录；固定 admin global / owner narrowing、self 强制本人、`pageSize=50` 默认与 `1..200`、无最大页、大小写敏感名称字面前缀、非法 mode/status 忽略、`pageSize+1` progressive total、列表最多 3 条绑定预览与计数、详情完整 bindings/config，以及真实 PostgreSQL / HTTP / 前端 / 切流 / 回滚 / Node 删除门禁；当前仅为 Go opt-in 迁移中 |
| `W5-管理端APIKey密钥生命周期迁移记录.md` | W5 `POST /api-keys` / `POST /my-api-keys`、`GET .../secret` 与 `POST .../refresh-key` 管理 / 个人双路由已进入 Go opt-in；固定 owner scope、strict create body、AES-GCM 密文、一次性明文返回、create runtime/quota 与 refresh validation/runtime/quota 失效、session touch、operation log marker 和真实 PostgreSQL / Redis / Asynq 门禁；删除由独立迁移记录维护，不代表 API Key 生产接管 |
| `W5-管理端APIKey删除迁移记录.md` | W5 `DELETE /api-keys/{id}` 与 `DELETE /my-api-keys/{id}` 已进入 Go opt-in；固定 admin global / owner narrowing、self actor scope、写鉴权与限流、204 空 body / no-store、默认 Key 保护、PostgreSQL 原子硬删除与 cleanup-target upsert、validation 必需失效、`api_keys.delete` 操作日志、残余失效重试风险和真实依赖门禁 |
| `W6-记录与统计读接口迁移记录.md` | W6 记录、日志和统计只读接口迁移记录；当前固定管理侧 / 个人侧 `usage-window` 权限、时区、31 天窗口、无明细扫描和删除门禁 |
| `W6-System-API限流对齐记录.md` | system API 两层 read / write 限流记录；固定六项设置默认值、鉴权前 IP 层、鉴权后已注册业务路由用户层、Redis / 内存实现、client IP allowlist 两层 bypass、30 秒缓存 / shared version 失效、429 语义，以及已认证未知路径 / 错误 method 尚未对齐的删除门禁 |
| `测试与验收策略.md` | 单模块、系统、网关、性能、安全和发布验收 |
| `开发构建部署调整.md` | 开发环境、命令、包结构、部署脚本和平台差异 |
| `迁移文档示例.md` | 后续新增单模块迁移记录时的参考格式 |

## 4. 维护规则

- 任何 Go 迁移任务开始前，先确认本目录和 `../plans/计划-0081-Node转Go渐进减法迁移.md`。
- 每迁移一个模块，必须更新 [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 的状态、Node 删除证据和测试结果。
- 影响 Go 目录结构、并发模型、进程模型或存储 owner 时，更新 [Go 后端架构基线](Go后端架构基线.md)。
- 影响 Go 框架、日志、配置、SQL、job、测试、观测、安全扫描或外部依赖时，先更新 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)，再改代码。
- 影响系统指标、Prometheus、pprof、worker lag、队列状态、PG/Redis/Asynq 观测或前端系统监控契约时，先更新 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md) 和 [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)，不能把 Node `eventLoopLagMs` / `db-service` 字段模拟成 Go 指标。
- 影响 PostgreSQL、Redis、SQLite 移除、数据导入导出或存储部署时，更新 [存储目标与 SQLite 移除](存储目标与SQLite移除.md)。
- 影响本地启动、安装、构建、发布包、Docker、服务化或回滚方式时，更新 [开发构建部署调整](开发构建部署调整.md)，并同步 `../develop/` 或 `../deploy/` 对应当前手册。
- 影响公开 API、管理 API、权限、安全、统计、存储或网关语义时，同步更新 `../functions/` 下对应功能文档。
- 影响当前真实架构事实时，同步更新 `../architecture/架构总览.md` 和 `../architecture/backend/README.md`。

## 5. 边界说明

- 迁移期间，前端仍按 Vue 3 + TypeScript + Ant Design Vue 维护。
- Go 迁移优先覆盖后端运行时；前端 API 调用契约要通过测试证明未缺失，但不因迁移重做前端信息架构。
- “不向下兼容”指不为旧 Node 内部结构、旧 schema、旧 repository 或旧 IPC 保留运行时兼容分支；对当前产品公开契约和用户可见行为，迁移必须做到等价或在文档中明确记录新契约。
- 迁移目标不再保留 SQLite standalone / PostgreSQL performance 两套模式。Go 后端目标只有 PostgreSQL + Redis；SQLite 只作为当前 Node 旧实现和离线导出来源存在。
- 迁移中允许测试环境短期存在 Node 与 Go 两个服务按路径分流，但同一接口、同一模块或同一后台任务不能长期双 owner。
