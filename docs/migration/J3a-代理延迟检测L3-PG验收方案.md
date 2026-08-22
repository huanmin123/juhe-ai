# J3a 代理延迟检测 L3 PostgreSQL/PgBouncer 验收方案

> 状态：既有 dev 隔离 scratch 已经通过 PgBouncer `6432` 的 required smoke 并完成清理；本轮深度对照未重跑外部 smoke。该历史证据只覆盖 jobs Store、reader、lease、input/claim/outcome、权限与事务，不包含 Node→Go→Node projector/readback、生产/L4、Docker/Redis 或 owner handoff。该方案不翻转 Go owner、不停止 Node owner，也不替代后续真实依赖、投影和部署门禁。

## 1. 目的与硬边界

本方案只验证 J3a jobs Store 与 PostgreSQL 只读业务输入在隔离开发子库中的连接、权限、事务和 fence 语义。测试文件为 `backend-go/projects/jobs/internal/proxylatency/postgres_integration_test.go`，默认不连接任何数据库；只有显式设置两个 URL 时才进入 smoke。

必须同时满足：

- 数据库名称以 `juhe_ai_sub2api_dev_j3a_` 开头；禁止使用长期开发主库、生产库或共享 `shared.env`。
- jobs 与 business reader URL 均通过 PgBouncer `6432`；管理员只允许用直连 `5432` 做一次性建库、建角色、schema owner、GRANT 和销毁。
- jobs role 只拥有外部预建的 `juhe_jobs` schema 及其中 J3a 表；不得由 Go 创建业务 schema，不得访问或写入 `juhe_business`。
- business reader role 只读 `juhe_business.proxy_profiles`、`providers`、`provider_protocol_profiles`，不能写业务表、不能访问 `juhe_jobs`。
- fixture、连接串和密码只存在于本机临时环境；不得写入仓库、日志、审计载荷或本文档。

## 2. 环境变量与 SKIP 语义

| 环境变量 | 作用 |
| --- | --- |
| `J3A_PG_SMOKE_JOBS_URL` | jobs 应用角色经 PgBouncer `6432` 的 PostgreSQL URL |
| `J3A_PG_SMOKE_INPUT_URL` | business reader 角色经 PgBouncer `6432` 的 PostgreSQL URL |
| `J3A_PG_SMOKE_REQUIRED` | `1/true/yes/on/required` 时强制运行；缺 URL 或失败必须非零退出 |
| `J3A_PG_SMOKE_DB_PREFIX` | 可选 scratch 数据库名前缀，但必须以 `juhe_ai_sub2api_dev_j3a_` 开头；默认 `juhe_ai_sub2api_dev_j3a_` |

未设置两个 URL 且未设置 `J3A_PG_SMOKE_REQUIRED` 时，测试必须输出明确 `SKIP`；SKIP 不是 PostgreSQL 通过证据。设置任一 URL、或把 `REQUIRED` 设为真但缺 URL，均按配置错误失败；`REQUIRED` 的未知非空值也必须失败。测试会在连接前拒绝非 `postgres/postgresql` scheme、非 `6432` 端口、无显式角色、非固定 scratch database 或 jobs/input 不同 database 的 URL，并在连接后再次检查 `current_database()` 的 scratch 前缀。

PowerShell 示例（值只存在于当前进程，不要写入文件）：

```powershell
$env:J3A_PG_SMOKE_JOBS_URL = 'postgres://<jobs-role>:<secret>@<host>:6432/juhe_ai_sub2api_dev_j3a_<name>?sslmode=require'
$env:J3A_PG_SMOKE_INPUT_URL = 'postgres://<reader-role>:<secret>@<host>:6432/juhe_ai_sub2api_dev_j3a_<name>?sslmode=require'
$env:J3A_PG_SMOKE_REQUIRED = '1'
go test -count=1 ./internal/proxylatency -run '^TestPostgresSmoke$'
Remove-Item Env:J3A_PG_SMOKE_JOBS_URL, Env:J3A_PG_SMOKE_INPUT_URL, Env:J3A_PG_SMOKE_REQUIRED -ErrorAction SilentlyContinue
```

## 3. 管理员一次性 scratch 准备

管理员在直连 `5432` 上创建一次性数据库 `juhe_ai_sub2api_dev_j3a_<name>`、jobs role、business reader role 和外部 `juhe_jobs` schema。`juhe_jobs` 的 owner 必须是 jobs role；业务 schema/表由现行 Node 业务 schema 维护流程提供，不能用 Go jobs schema 初始化掩盖缺失。

准备最小权限：

1. jobs role：`USAGE`/`CREATE` 仅限 `juhe_jobs`，可在其中读写 J3a 表；拒绝 `juhe_business` 业务表访问。
2. business reader role：`USAGE` 加三张业务表 `SELECT`；显式拒绝 `UPDATE/INSERT/DELETE`，拒绝 `juhe_jobs` schema。
3. fixture：至少包含一个启用的 proxy profile、一个启用 provider、一个启用 protocol profile，且字段满足 Node schema 与 J3a reader 的 username-only/password envelope 规则。
4. 连接：为两个角色分别生成仅经 PgBouncer `6432` 的 URL；不要把管理员直连 URL 传给测试。

管理员应在测试前记录数据库名、角色名、schema owner、GRANT 摘要和 PgBouncer pool mode；记录中不得包含密码。主库、生产库和共享 `shared.env` 不属于本方案目标。

## 4. Smoke 覆盖与顺序

`TestPostgresSmoke` 的固定顺序是：

1. jobs/input 两个 URL 语法、端口、数据库前缀、`current_user` 预检与 `Ping`。
2. jobs schema owner 检查、`Store.EnsureSchema`；该调用只能在外部已有 `juhe_jobs` 时成功。
3. jobs role 业务写权限拒绝、reader role jobs 访问和业务写权限拒绝。
4. reader `CheckContract` 与 `LoadDue`，其事务必须使用 `REPEATABLE READ`、`READ ONLY`、`statement_timeout=5s`、`lock_timeout=1s`。
5. Store owner lease → issued input → proxy lease → `AdmitExecution` claim → `AppendOutcome` → committed replay；验证 request identity、payload digest 和 successor-safe replay 语义。
6. 使用唯一 scratch proxy/request 记录，释放 lease 后删除测试 rows；schema 本身由管理员按 scratch 生命周期销毁。

测试不会发起真实代理上游请求，不会调用 Node、Redis、projector、scheduler 或 owner handoff。真实数据库结果必须保留原始命令退出码、数据库/角色摘要（脱敏）、PgBouncer pool mode、fixture revision、清理结果和失败原因，不能只保留“通过”文字。

## 5. 清理与失败处理

测试结束时先关闭应用连接、删除本次唯一 proxy/request rows、释放 lease，再由管理员关闭会话并删除 scratch 数据库、角色、临时 fixture 和本机环境变量。任何清理失败都要记录为失败，不得把残留 scratch 数据库描述为已清理。

出现 URL 非 scratch、端口不是 `6432`、角色权限过大、schema owner 错误、读事务/锁超时不符或 reader 能写入业务表时，立即停止，不切换 owner、不重试主库。错误报告必须脱敏连接串、密码、业务 payload 和代理凭据。

## 6. 通过条件与后续门禁

本方案通过只表示隔离 PG/PgBouncer smoke 通过；仍必须分别完成：

- Node → Go → Node outcome/projector/readback 闭环；
- 真实 owner lease handoff、scheduler、health/readiness、取消/重启恢复；
- Docker/Redis、Jenkins/GitOps、发布候选、回滚与 active-path-zero；
- 明确的 Go owner 切换、Node 旧路径 drain/归档和回滚点。

在上述证据完成前，`JUHE_AI_PROXY_LATENCY_ENABLED` 与 `JUHE_AI_PROXY_LATENCY_JOBS_OWNER=go` 仍保持关闭，不能把本地 SQLite、默认 SKIP 或静态 SQL 检查当作生产接管证据。
