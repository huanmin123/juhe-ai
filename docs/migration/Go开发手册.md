# Go 开发手册

> 本手册适用于 `gateway`、`jobs`、`maintenance` 及迁移兼容模块。项目边界见 [Go 三项目架构基线](Go三项目架构基线.md)，完整功能接管规则见 [完整功能接管与 Node 归档迁移规则](完整功能接管与Node归档迁移规则.md)。

## 1. 先复用，再新增

1. 编码前先检查已有 Store、配置解析、结构化日志、HTTP client、签名、分页、租约、超时、测试夹具和同类模块；能复用就复用。
2. 标准库足够时优先标准库；需要外部能力时先使用项目已经选定的库，其次选择官方维护或成熟、活跃、许可证可接受的开源库。
3. 只有现有能力和合适库都不能满足明确契约时才自建最小实现。自建必须记录原因、替代方案、边界、测试和后续退出条件。
4. 共享代码只放稳定且无业务 owner 的契约或基础设施。不得为复用把业务 service、跨项目 Store 或全局可变状态塞进 shared，也不得为了隔离复制多套同类实现。

## 2. 三项目边界

- `gateway`：对外 API、管理 API、AI 上游桥接和请求级业务处理。
- `jobs`：定时探活、复制、统计、保留清理和其他可恢复周期任务。
- `maintenance`：schema、历史迁移、回填、重建、诊断和明确的一次性维护。
- 三者独立构建、部署和重启，禁止互相 import。共享只通过 `shared/contracts`、稳定数据契约或外部协议完成。
- F1/F2 当前在 `jobs`，F3/F4 当前在 `gateway`。新定时功能只进入 `jobs`，不能追加到 gateway 或 Node worker。
- 当前过渡期内，Node 仍是对外网关和主业务 owner。`gateway` Go 项目不得因为后台任务方便而承担任务调度；`jobs` 不得因为读取账户方便而调用 Node、调用 Go gateway、import gateway internal 包或复用网关请求生命周期。

## 3. 并发原则

1. 不复制 Node 的事件循环、worker thread、IPC pending、Redis Stream 或小并发队列。独立 I/O、账户、上游、文件和批次默认直接以 goroutine + `context` fan-out。
2. 不设置迁移层全局小并发闸门。并发边界来自 SQLite 单 writer、PostgreSQL/PgBouncer pool、事务/锁超时、HTTP transport、代理、FD、CPU、网络、payload 和上游限流。
3. 实时任务优先：探活、账号检测、独立上游调用和其他对新鲜度敏感的工作优先并发。统计使用游标和窗口并发；历史扫描、回填和冷数据清理可以更慢并给实时任务让位。
4. 并发多不等于无界：每个工作必须有 owner、`context`、deadline、幂等键、结果和指标。禁止无 owner goroutine、无限 backlog、无限重试和静默 fallback。
5. SQLite 的同一物理文件始终单 writer；PostgreSQL 使用连接池、事务、`statement_timeout`、`lock_timeout` 和批量窗口；外部服务使用共享 client、连接预算和取消。

## 4. 后台任务

- 后台任务按完整功能落在 `jobs`，不是按 Node worker 的历史进程模型拆分。
- 每项任务明确优先级、触发周期、候选读取窗口、owner lease、幂等键、失败分类、重试/下一轮恢复和指标。
- 单条业务错误只记录该条失败，不能终止整个 listener 或项目；仅进程退出、lease 丢失或不可恢复基础设施错误触发项目级重启。
- 任务启动后必须可停止。服务退出、超时、依赖取消和租约丢失要沿 `context` 传播；重启后从持久事实、cursor 或幂等键恢复，不能依赖内存队列。
- 调整并发前后以吞吐、P95/P99、错误率、pool/锁等待、外部限流、heap、goroutine、FD 和取消延迟验证，不凭直觉保守串行或盲目放大。

### 4.1 jobs 的数据与上游边界

1. `jobs` 的探活、余额刷新、复制校验、模型检测和其他账户级外部 I/O，直接请求账户配置指向的真实上游；不得经 Node 网关、Go gateway、内部 gateway URL 或 Node IPC 转发。探活是最小诊断协议，不携带终端用户鉴权、路由策略、分组选择、配额、流式响应编排、G2/G3 网关运行态或对外响应语义。
2. PostgreSQL 模式下，任务只能按冻结的稳定数据库契约读取候选和写入其已接管的任务事实；读取/写入均使用项目已有 PostgreSQL adapter、短事务、超时和 owner lease。不得通过调用 gateway 换取业务状态。
3. SQLite 模式下，同一物理业务文件只能有一个 writer。`jobs` 不得直接打开或写入 Node/gateway 的业务 SQLite 文件，也不得要求 Node 作为同步 RPC 依赖。需要账户输入时，由当前业务 owner 单向推送经签名、版本化的诊断快照到 jobs 自己拥有的 Store；需要回传结果时，Node 只读 jobs 的稳定结果协议。若这些协议、密钥与单 writer 证明尚未具备，该任务必须保持 Node owner，不能启用半功能 Go scheduler。
4. 凭据只可通过既有共享秘密管理或可审计的加密引用交给 jobs；不得把明文写入日志、队列、指标、错误、URL 或测试产物。没有可独立解析的凭据引用时，credential-requiring task 不能迁移。
5. 每个直接上游调用必须设置账户/协议级 deadline、取消、代理与 TLS 边界；错误按可重试上游故障、凭据/配置错误、协议 neutral、取消和基础设施错误分类。不能把失败静默伪装为“账号不可用”，也不能调用 gateway 作为 fallback。

## 5. Node 代码只作行为证据，不作 Go 设计模板

1. Node 现有调用链、worker、queue、IPC、repository 和网关路径用于冻结现有用户可见行为、字段、状态机与失败语义；它们不是 Go 必须照搬的架构。发现 Node 实现把一个后台能力反向绑到网关、另一个 worker 或隐式运行态时，先拆出该能力真正需要的稳定输入、直接上游诊断和结果契约。
2. 不能为了短期兼容让 Go `jobs` 调 Node、调 Go `gateway`、复制网关请求生命周期，或新增长期 bridge / fallback。这样会把 owner 环依赖带入 Go，导致任何一个模块都无法独立 handover；例如账号探活必须直连账户真实上游，而不是反向经过网关。
3. 取证发现实现需要超出当前迁移功能的 owner、数据表、凭据访问、公开接口、运行态、共享模块或生产切换时，立即停止该功能的实现和切换。报告已证实事实、最小受影响范围、不能闭环的原因、可选方案、回滚影响和缺少的验收；由用户决定扩大范围、改设计、拆分切片或暂缓。
4. 未取得上述决定前，只可做只读取证、契约草案和不改变 owner 的隔离测试；不得以“先搭接口”“先加 fallback”或“先同步两边”继续写入，避免形成数日后必须整体撤回的半迁移代码。
5. 用户决定后的新范围必须重新冻结目标、非目标、唯一 owner、SQLite/PG 数据边界、依赖方向、Node 清零条件、验证和回滚，再开始实现。决定与停点记录在对应迁移契约或路线图，不能只留在对话中。

## 6. 迁移工作方式

1. 先冻结字段、权限、幂等、状态、失败、保留和读 API 契约。
2. 实现目标 Go 项目的 SQLite/PostgreSQL adapter、真实资源边界和测试。
3. 先在独立开发环境验证，再停止该功能的 Node owner；不做长期双写、Node fallback 或兼容队列。
4. 切换后扫描活跃 Node import、旧 worker、旧 queue、Redis/IPC 和 schema owner，确认归零后归档专属源码。
5. 每个完成单元至少运行紧贴模块测试；影响存储、并发或跨进程边界时扩展到 race、SQLite/PG/Redis smoke 和发布路径验证。
