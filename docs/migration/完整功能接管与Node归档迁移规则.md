# 完整功能接管与 Node 归档迁移规则

> 决策日期：2026-08-08。
> 本文取代此前按 route、job、writer 或仅影子链路拆分接管的现行优先级。SQLite 与 PostgreSQL/Redis 双模式要求继续有效；它们不改变本规则的最小迁移单元。

## 1. 最小迁移单元

一个**完整功能**是唯一可接管单元，不按一个 handler、一个 job、一个 DTO、一个小区域或一个文件中的若干方法接管。完整功能至少包含：

- 它自身的全部入口与错误契约：HTTP 功能包含路由和鉴权；纯后台被动功能包含 CLI / worker 生命周期、scheduler、停机与重启恢复；
- 全部 service、store、配置、启动装配、定时 / 异步触发、数据读写和运行态副作用；
- 该功能专属的前端调用、脚本、测试、监控与部署入口；纯后台功能的已有只读浏览页面若只消费其已持久化产物，可作为独立的只读消费者功能保留，但不得保留 Node writer、retention 或 owner bridge；
- 两种存储模式下的实现与验证：SQLite 的单 writer 与 owner 边界，PostgreSQL/Redis 的事务和连接池边界。仅尚未迁移的 Node 功能可以继续使用其既有 owner bridge；L3 后被接管功能的 SQLite owner 必须是 Go，不能继续依赖 Node bridge。

若 Node 文件同时承载多个功能，不能只搬走其中一段。先把 Node 侧整理为不改变行为的独立完整功能文件，完成 Node 回归后，再把整个文件随该功能一次性归档；仍被其他 Node 功能引用的共享基础设施必须保留为独立 Node 依赖，不能把共享文件的一半搬入归档。

## 2. 接管与 Node 下线

每个功能按以下生命周期推进；`L1-L4` 只表示单功能内部步骤，`F1-F6` 保留给功能批次，二者不得混用：

1. **L1 冻结**：冻结 Node 的完整功能边界与外部契约，列出所有入口、文件、调用方、数据写入、定时触发和副作用。
2. **L2 实现**：在 Go 中完成整个功能及其双模式实现；不注册部分 Go route/job 作为长期并行 owner。
3. **L3 验收与切换**：在隔离环境完成契约、两种存储模式、并发、失败、重启和调用方验证；随后切换为 Go 的唯一正式 owner，停止 Node 的整个功能入口、scheduler、consumer 和写入。
4. **L4 归档**：将 Node 完整功能文件移动到 `migration-backup/node/<feature-id>/`，移除活跃 Node import、路由、启动、部署和测试入口。归档代码不得被运行时、构建、打包或测试自动加载。

完成 L4 才算“已接管”。不保留 Node fallback、bridge、双写、双 consumer 或仅用于灰度的长期开关。若 Go 功能失败，回滚通过恢复归档文件对应的明确提交和完整 Node 功能 owner 完成，不得混跑两个 owner。

## 3. Node 归档区

仓库根目录的 `migration-backup/` 是受版本控制、但不参与任何应用构建的 Node 源码归档区。每个功能创建：

```text
migration-backup/
  node/
    <feature-id>/
      manifest.json
      README.md
      backend/src/...             # 按原相对路径保存的完整 Node 文件
      frontend/src/...            # 仅当该功能专属前端代码一并退出
      scripts/...                 # 仅当该功能专属脚本一并退出
```

`manifest.json` 至少记录：功能 ID、归档时间、原始路径、归档路径、原始文件 SHA-256、接管提交、Go 实现路径、停用的 Node 入口、双模式验证、回滚提交和维护者。归档禁止包含 `.env`、密钥、真实数据库、日志、`node_modules`、构建产物或用户数据。

归档根目录已提供 [`migration-backup/node/manifest.template.json`](../../migration-backup/node/manifest.template.json)。每次 L4 必须先复制模板为该功能目录的 `manifest.json`，再填入实际文件与验证证据；模板本身不是任何功能已归档的证据。

归档只用于代码对照、遗漏排查和可审计回滚，不是兼容层。新功能不得 import、复制粘贴后继续修改或从归档目录运行代码；需要恢复时必须恢复整个功能文件集和 owner，而不是从归档挑选一段代码回填。

## 4. Go 直接异步与并发

Go 使用 goroutine（M:N 调度），不是无限成本的虚拟线程。新接管功能默认**直接异步执行**：不为迁移新增通用队列、常驻 worker pool、业务限速或人为低并发；独立工作按功能维度直接 fan-out，并通过 `context` 取消和结果汇总收口。

这不授权无界 goroutine：无界 fan-out 会耗尽内存、文件描述符、连接池并破坏 SQLite 写入正确性。并发只按真实资源维度隔离，而不是设置一个任意全局限速：

- SQLite：同一文件仍只有一个 writer，调用直接等待该文件 owner 完成，不经内存队列绕过单 writer。
- PostgreSQL：直接异步访问由连接池和事务超时约束；每个依赖维度按其 pool / 锁 / payload 容量运行。
- 上游、代理和网络：不新增迁移层业务限频，但保留已有产品契约与第三方明确限制；连接取消、超时和错误必须原样可见，不能无限重试或静默降级。

“直接异步”不等于丢失关键事实。必须持久的写入应在 goroutine 内直接提交到 Store，并等待该提交结果；进程退出前未完成的工作必须由功能自身可重放的事实来源重新发现。若一个功能无法说明重启后的恢复语义，它不能以裸 goroutine 接管。

### 4.1 Go 迁移并发减法决策（2026-08-21）

后续 Node -> Go 迁移不得把 Node 运行时为了事件循环、worker thread、`p-limit`、IPC pending 或历史队列而设置的“低并发常量、业务限速、固定 worker 数、通用内存队列”直接带入 Go。Go 功能默认按独立 I/O 工作单元直接 fan-out；不得仅为了“看起来稳妥”新增一道人为全局并发上限，也不得新增只用于限制业务吞吐的环境变量。

本条的“无业务限速”不等于删除正确性和物理资源边界。以下边界是迁移契约的一部分，不得为了追求“无限”而删除、绕过或静默降级：

- `context` 取消、请求 / 事务 deadline、owner lease、fence、CAS、幂等键和周期防重入；
- SQLite 文件单 writer、PostgreSQL / PgBouncer 连接池、事务 / 锁等待、statement timeout 和稳定分页窗口；
- 上游 / 代理连接、文件描述符、内存、payload、HTTP transport、`429/503` 和客户端断开语义；
- cursor、`LIMIT`、claim window、scan cap 等用于公平读取、恢复和背压的窗口；它们不是产品限流，必须证明窗口耗尽后可重放、可观测且不会永久饿死后续合法工作。

因此，迁移评审必须把“人为业务限速”与“真实资源及正确性边界”分开记录：前者默认删除，后者只在实际运行确实出现问题时处理。默认先直接并发；没有实际故障依据，不得凭假设新增限制、降低并发或把容量写死。后续若出现明确故障，再针对根因调整实现、保留回滚方式并重新验证。任何新增有限值都必须说明它保护的真实资源、取消 / 恢复行为和回滚方式；没有实际故障依据不得把有限值写进 Go 业务层，也不得把“goroutine 成本低”作为无限连接、无限扫描或无限提交的依据。本决策只约束迁移设计，不授权生产发布或改变当前 owner。

### 4.2 Go 运行模式、并发与 PostgreSQL pool 统一规则（2026-08-22）

本条是后续 Node -> Go 迁移的固定默认，不得把 Node 的低并发、队列或 worker 常量移植进 Go：

- SQLite 默认 worker 并发为 `4`；需要高性能模式时由外部配置提升到 `64`，仍保留 SQLite 文件单 writer、事务和 owner/fence 正确性边界。
- PostgreSQL 性能模式的 J1 账号探活、J2 余额刷新以及同类 Go I/O worker 使用 `4..256` 的外部配置范围，默认 `256`；不得在业务代码中另加低并发、固定批次等待或通用内存队列。J3/F2 等按自身 I/O 工作单元使用同一并发策略，不能因历史 Node 实现引入 `p-limit` 式人为限速。
- PostgreSQL `database/sql` 连接池默认 `max open = 1000`、`max idle = 1000`，所有相关参数必须通过环境变量或启动配置注入，不得写死在业务调用点。一个 Go 进程内，完全相同的 PostgreSQL URL 与权限/逻辑 role 只能复用同一个 registry pool；不同 URL、不同权限 role 或不同进程必须保持隔离，不能伪装成同一连接池。
- 连接池配置键按功能前缀保持可发现：J1 使用 `JUHE_AI_ACCOUNT_HEALTH_*_POSTGRES_MAX_{OPEN,IDLE}_CONNS`，J2 使用 `JUHE_AI_ACCOUNT_BALANCE_*`，F2 使用 `JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_{OPEN,IDLE}_CONNS`，F3/F4 使用各自 `JUHE_AI_AUDIT_LOG_*` / `JUHE_AI_OPERATION_LOG_*`；J1/J2/F2/F3/F4 的 worker/source 并发分别由对应 `*_MAX_CONCURRENCY` 或 `*_MAX_CONCURRENT_SOURCES` 注入。未声明的参数不得在代码中另设隐藏常量。
- jobs 与 gateway 是不同 Go 进程，不能共享进程内 `*sql.DB`；每个进程分别按上述 registry 复用。使用 `pgxpool` 的模块（例如运行日志）必须显式记录其 driver/pool 边界；在没有完成同 driver 迁移前，不得声称它与 `database/sql` pool 共享同一连接池。
- 探活和代理 transport 不得再设置固定的 `MaxConnsPerHost` 串行上限；当前 Go 代码使用 `0` 表示不由迁移层追加 per-host 活跃连接限制，真实上游、代理、FD、超时、取消和 PostgreSQL pool 仍按实际故障处理。
- “1000”是默认 pool 配置，不是已经完成的生产容量证明；迁移验收仍需记录真实 PostgreSQL/PgBouncer、长连接、取消、连接池等待和吞吐验证结果。没有该证据时只能报告“代码和配置已实现，容量待验证”，不得直接上线或宣称可承载任意并发。

后续迁移如需新增任何并发、pool、timeout、batch、scan 或 transport 参数，必须同时写明：外部配置键、默认值、作用的真实资源、取消/恢复语义、回滚方式及验证命令。没有实际故障证据，不得为了“看起来稳妥”再加限制；发现故障后只修复被证实的根因，不将监控指标本身变成业务限流。

## 5. 每功能验收

接管记录必须证明：完整文件清单已经归档、Node 活跃路径为零、Go 是唯一 owner、两种存储模式均通过、直接异步并发不丢事实、取消和重启可恢复、调用方和部署均不再引用 Node 功能。只完成 Go 的某个小文件、读路径、影子路径或一次 smoke 均不构成接管。

## 6. 首个功能批次 F1：运行日志索引与保留

建议先冻结并接管“**运行日志索引与保留**”这个完整被动功能。它以进程已落盘的运行日志文件为输入，职责包括：扫描与解析、cursor 持久化、运行日志索引 / facets、索引维护、过期清理、启动装配、周期触发、失败与重启恢复，以及该功能专属的脚本、测试和监控。Node 的通用日志输出仍是其他 Node 功能的共享基础设施，不属于此功能的归档范围。

当前共享启动文件（例如 worker 角色装配）不能直接抽走几行。F1 在实施时必须先完成 L1，把运行日志索引与保留的启动注册、服务、测试和配置引用整理为专属文件；随后依次完成 L2 实现、L3 唯一 owner 验收和 L4 Node 整体归档。若发现 cursor、索引表或 SQLite 文件仍与未迁功能不可隔离，必须扩展至完整共享 owner 或延后该候选，不能保留 Node bridge 作为迁移后依赖。

相关入口：[双模式存储与被动任务优先迁移方案](双模式存储与被动任务优先迁移方案.md)、[迁移规划总览](迁移规划总览.md)、[测试与验收策略](测试与验收策略.md)。
