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

## 5. 迁移工作方式

1. 先冻结字段、权限、幂等、状态、失败、保留和读 API 契约。
2. 实现目标 Go 项目的 SQLite/PostgreSQL adapter、真实资源边界和测试。
3. 先在独立开发环境验证，再停止该功能的 Node owner；不做长期双写、Node fallback 或兼容队列。
4. 切换后扫描活跃 Node import、旧 worker、旧 queue、Redis/IPC 和 schema owner，确认归零后归档专属源码。
5. 每个完成单元至少运行紧贴模块测试；影响存储、并发或跨进程边界时扩展到 race、SQLite/PG/Redis smoke 和发布路径验证。
