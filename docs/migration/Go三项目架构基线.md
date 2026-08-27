# Go 三项目架构基线

> 状态：架构落地第一阶段（项目边界与工程骨架），不代表三个项目已经完成业务接管或生产切流。
>
> 本文是 Go 后续迁移的边界契约。F1/F2 已物理迁入 `jobs`，F3/F4 已物理迁入 `gateway`；`backend-go` 根目录只保留工作区定义。一般新迁移的定时功能进入 `jobs`；J3b 是经方案 A 明确批准的例外：其管理入口、调度和投影与 Business SQLite owner 同进程落在 `gateway`。

## 1. 最终项目划分

| 项目 | 运行形态 | 负责 | 不负责 |
| --- | --- | --- | --- |
| `gateway` | 长驻 HTTP 服务 | 管理 API、公开 API、AI 接口桥接上游、请求鉴权、请求级业务处理；方案 A 下承载 J3b 完整 runtime/scheduler/projector | 一般定时扫描、统计批处理、离线迁移和历史回填 |
| `jobs` | 可独立部署的长驻定时服务 | 账号复制/探活、除 J3b 外的模型质量任务（明确不含 J3b）、OAuth 保活、统计聚合、定时保留清理及其他周期任务 | 对外业务 HTTP、AI 请求代理、一次性人工维护命令；J3b runtime、scheduler、listener、store 和 projector |
| `maintenance` | 独立执行的一次性命令 | schema 校验/迁移、历史数据回填、重建索引、诊断、人工触发的清理和导出 | 常驻调度、对外 HTTP、请求链路业务逻辑 |

项目3按当前 Go 基线定义为 `maintenance` 控制面。它是一次性运维与数据工具，不是第三个常驻 worker；如果后续确认项目3需要承载其他长期职责，必须先修改本基线和模块边界，再迁移业务。

三项目必须支持独立构建、独立配置、独立发布、独立重启和独立健康检查。一个项目退出、升级或依赖故障，不得通过进程内 import 或进程看护让另外两个项目被动退出。

当前 Node 网关与主业务尚未迁移时，Go `gateway` 不是 `jobs` 的服务依赖；后台任务不得调用 Node 或 Go gateway。J3b 不属于该通用后台例外：其代码必须在 gateway 进程内完成，jobs 不得调用 gateway。其他任务只能使用本基线规定的稳定数据契约，或由当前业务 owner 单向推送的签名、版本化 jobs 输入协议。

## 2. 源码与依赖方向

```text
gateway       jobs       maintenance
   |            |             |
   +------------+-------------+
                |
          shared/contracts
```

- 三个项目各自拥有独立 `go.mod`、`cmd`、`internal`、测试和发布产物。
- 项目之间禁止直接 import：`gateway` 不得依赖 `jobs` 或 `maintenance`；`jobs` 不得依赖 `gateway`；`maintenance` 不得依赖前两个项目。
- 共享代码只能进入明确的、无业务编排的库，例如 DTO、枚举、错误码、协议版本和稳定 ID 规则。共享库不得反向 import 任一项目，也不得放入“万能 service”“跨项目 repository”或隐含 owner 的全局状态。
- 数据库驱动、HTTP client、日志、配置解析、租约和并发工具优先复用已有经过验证的 Go 基础设施；抽取共享实现前先检查现有 `backend-go/projects/*/internal`、`shared/platform` 能力和已选官方/成熟开源库，不能因项目拆分各写一套。
- 业务表 schema、Store 和 owner 仍按完整功能归属项目；共享库不拥有业务数据。跨项目只通过稳定的数据库契约、外部协议或一次性维护输入交互，不通过 Go 包调用对方 service。

工程骨架位于：

```text
backend-go/
  go.work                        # 本地同时开发全部模块，不作为发布依赖
  shared/contracts/              # 非部署库，仅放跨项目稳定契约
  projects/gateway/              # 独立 Go 项目
  projects/jobs/                 # 独立 Go 项目
  projects/maintenance/          # 独立 Go 项目
```

## 3. 运行和数据边界

### `gateway`

- 只处理请求生命周期内的工作：鉴权、业务事务、上游协议适配、响应和必要的热状态。
- 一般不启动业务定时器；方案 A 的 J3b scheduler 是显式注册、可取消、可观测的唯一例外，且不得导入 `jobs` 的内部实现。不在请求结束后偷偷启动无 owner 的 goroutine。
- 业务成功后产生的事实必须按对应功能契约持久化；需要异步处理的功能使用持久事实或明确的外部协议发现工作，不把进程内 channel 当作可靠队列。

### `jobs`

- 只注册周期任务和可恢复的后台执行。任务必须有稳定 ID、owner lease/单 owner 规则、取消上下文、超时、下一轮重试和可观察结果。
- 复制、探活、统计、窗口聚合和保留清理由这里逐项迁移。迁移一个完整功能后，Node 原 scheduler/worker 和旧 Go owner 必须退出活跃路径并归档，不做长期双写。
- 可实时的探活、账号检测和独立外部 I/O 按候选窗口直接 fan-out goroutine；统计按游标/批次并发；低优先级历史扫描和冷数据清理使用较小窗口，让位于实时任务。
- `jobs` 不提供通用业务 API。对已由 `jobs` 完整拥有的后台功能，允许提供受认证、精确路由的管理命令入口（例如 J3a）；J3b 不在此列，其入口和 runtime 由 gateway 在同一进程内提供。任何功能的入口都不得转发给 Node 或另一个 Go 项目，也不得演化为通用业务代理。
- `jobs` 直接向账户配置的上游执行轻量诊断，不经 Node 或 Go gateway。J3b 由 gateway 进程内直接向上游执行，其诊断入口不经过 jobs。两者都不实现未获授权的用户请求路由、配额或跨项目写回。
- PostgreSQL 使用稳定数据库契约；SQLite 使用 jobs 自己拥有的 Store 和单向输入/只读结果协议。`jobs` 绝不成为 Node SQLite 文件的第二 writer，也不以 Node/gateway RPC 为 fallback。

### `maintenance`

- 命令启动后完成一次明确工作并退出，必须支持 dry-run（若命令会写数据）或明确列出影响范围、失败位置和恢复方式。
- 只能调用共享契约和自己的 maintenance adapter；不能复用 `gateway`/`jobs` 的内部 service。
- 历史迁移、schema 校验、索引重建和审计/操作日志一致性修复都应有独立命令和报告，不放进常驻定时循环。

## 4. 并发和任务优先级

Go 不复刻 Node 的事件循环、worker thread 或低并发队列。goroutine 是默认执行单元，但“并发多”不等于“无边界”：边界来自真实资源和正确性。

1. 先按独立 I/O、独立账户、独立上游、独立文件或独立批次识别 fan-out 单元，再直接使用 `context` + goroutine（必要时使用已选 `errgroup`）并汇总结果。
2. 不设置迁移层全局小并发常量。按 PostgreSQL pool/PgBouncer、SQLite 文件单 writer、HTTP transport、代理、FD、CPU、payload、上游 `429/503` 和任务 deadline 设置实际边界。
3. 实时探活和高价值可恢复任务优先获得并发和新鲜度；统计批处理受游标/窗口控制；低价值历史重建和冷数据清理可以慢，但不能阻塞实时任务。
4. 单条业务失败必须记录并返回该条失败；不能杀死整个 jobs listener。租约丢失、进程退出和不可恢复基础设施错误才进入项目重启边界。
5. 每个 goroutine 都必须属于项目生命周期或具体任务 context，具备取消、超时、幂等键和结果观测。禁止无 owner goroutine、无限内存 backlog、无限重试和静默 fallback。
6. 并发调优用吞吐、P95/P99、pool/锁等待、错误率、heap、goroutine、FD 和取消延迟证据决定；不能仅凭“goroutine 占用内存很小”跳过资源和依赖约束。

## 5. 定时迁移顺序

1. 先完成 `jobs` 项目骨架、任务 registry、生命周期、lease、指标和 SQLite/PostgreSQL Store 接入边界。
2. 按完整功能迁移 Node 定时域：先复制/探活等 jobs 域实时外部 I/O；J3b 按方案 A 在 gateway 内完成；再迁移统计窗口和聚合，最后低优先级维护/保留清理。
3. 每一项迁移先冻结字段、状态、幂等、租约、失败和恢复契约，再实现 Go job；Node 只保留生产者/读适配所需的最小边界。
4. 在 `jobs` 独立实例通过 SQLite 和 PostgreSQL/Redis 开发验证后，停止对应 Node scheduler/worker，验证无双 owner，再归档 Node 专属文件。
5. `gateway` 的业务 API、AI 桥接及方案 A 的 J3b runtime 按各自契约迁移；不得为了让 jobs 复用代码而把 gateway 业务 service 放进 shared。
6. `maintenance` 命令与常驻 jobs 分开验收；历史 blob、schema 和数据一致性修复不作为定时任务 fallback。

### 5.1 首批定时域优先级

| 优先级 | 当前 Node 角色/任务域 | 目标项目 | 迁移要求 |
| --- | --- | --- | --- |
| P0 实时 | `ops-worker` 账号复制、账号健康探活、冷却复测 | `jobs` | 先冻结状态机、lease、取消和上游超时；按账户/上游直接 fan-out，单账户失败隔离。模型检测按 J3b 方案 A 由 gateway 单进程 owner 承担 |
| P0 实时 | OAuth token 保活、授权到期扫描 | `jobs` | token/授权事实必须幂等；外部 I/O 与数据库写入分 lane，不能阻塞探活 |
| P1 批处理 | `stats-worker` 用量聚合、IP/分组统计、趋势和窗口刷新 | `jobs` | 以 cursor/window 增量读取，统计写入独立 owner；按窗口并发，不回请求路径实时汇总 |
| P1 批处理 | 系统指标和表监控采样 | `jobs` | F2 已物理归入 jobs；后续按完整功能继续收敛 Node owner，禁止双写 |
| P2 冷任务 | retention、历史扫描、索引/派生数据重建 | `jobs` 或 `maintenance` | 周期清理进入 jobs；一次性重建/回填进入 maintenance；大批次可慢但必须可恢复、可报告 |

下列内容暂不随本次项目骨架迁移：F3/F4 已上线或切换前实现的主链路、网关请求处理、管理 API、AI 上游桥接和历史 Node 业务 worker。它们必须分别按照完整功能契约和 L1-L4 门禁迁移，不能把“已经有 jobs 模块”当作接管证据。

## 6. 发布和配置契约

- 配置按项目命名空间隔离：`JUHE_AI_GATEWAY_*`、`JUHE_AI_JOBS_*`、`JUHE_AI_MAINTENANCE_*`。共享数据库连接只在明确 owner 的项目中配置，不能通过“默认共享全部 env”隐藏依赖。
- 每个项目有独立 release、日志目录、健康/就绪端点、进程身份和重启策略。部署编排可以放在同一机器，但不能以同一进程或同一 supervisor 作为运行前提。
- `gateway` 的健康检查覆盖 HTTP/API 和上游桥接依赖；`jobs` 的健康检查覆盖 scheduler、lease、任务滞后和关键 Store；`maintenance` 以退出码和报告文件验收，不伪造长驻 health。
- 当前源码已将 F1/F2 放入 `jobs`、F3/F4 放入 `gateway`；J3b 的目标 owner 由方案 A 改为 gateway。每个环境的实际常驻 owner 仍须由该环境的部署、健康、读回和回滚证据确认；除 J3b 外不把新定时功能放入 gateway。

## 7. 验收门

- 三个模块可分别 `go test ./...`、构建和查看版本；模块间无直接 import。
- 新 jobs 任务有单元测试、取消/超时测试、单条失败隔离测试、SQLite 单 writer 测试和 PostgreSQL/Redis 真实 smoke（环境可用时）。
- 迁移完成前必须保留旧 owner 的回切证据；迁移完成后扫描 Node 活跃 import、旧 queue/worker、双写和 fallback 引用均为零。
- 文档、启动器、Docker、systemd/launchd/PowerShell 和回归脚本必须以三项目边界为准；只更新代码不算架构完成。
