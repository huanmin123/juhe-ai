# 冷却复测 PostgreSQL 写回修复设计

## 背景

生产 release `20260718-runtime-governance-baf5cae4b` 上线后，冷却复测仍能扫描候选并真实请求上游，但 PostgreSQL 模式无法持久化复测失败，也无法在复测成功后恢复账户。目标账户多次真实收到 `HTTP 403 insufficient_user_quota`，worker 随后只记录 `background_cooldown_account_retest_retry_exhausted`；账户表仍停留在上线前的失败次数和最后复测时间。

生产只读 `EXPLAIN UPDATE` 已复现 PostgreSQL `42P18`：`(? IS NULL OR column = ?)` 转换为 `$n` 参数后，只参与 `IS NULL` 的参数没有类型上下文，数据库在执行前拒绝语句。成功恢复与失败累加使用同一写法，因此两个出口同时失效。

## 目标

- 修复 SQLite 与 PostgreSQL 冷却复测成功、失败写回的代次条件构造。
- 保留 `configRevision` 与 `cooldownRetestObservationStartedAt` 的乐观并发保护，迟到结果不能覆盖新配置或新观察代次。
- 在真实 PostgreSQL 上验证失败累加、成功恢复和迟到结果拒绝。
- 复测队列执行异常时记录脱敏后的原始错误，避免只留下“重试耗尽”。
- 不改变复测批次、并发、复合游标、公平调度、退避算法或账户状态语义。

## 非目标

- 不调整 `cooldownAccountRetestBatchSize`、每分钟扫描周期或共享三路完整诊断门禁。
- 不修改账户错误策略、运行态软阻断、受控半开或网关普通请求行为。
- 不变更数据库 schema、Redis namespace、API 或前端字段。
- 不为历史 release 或旧数据库结构增加兼容分支。

## 方案比较

### 方案一：为 nullable 参数显式写 PostgreSQL cast

将条件改为 `CAST(? AS integer) IS NULL` 和 `CAST(? AS text) IS NULL`。改动最小，但 SQL 继续携带重复参数，并让 SQLite/PostgreSQL 路径保留容易再次误用的 nullable guard 模式。

### 方案二：按实际 expectation 动态构造条件，推荐

根据 `expectedConfigRevision` 与 `expectedObservationStartedAt` 是否存在，生成零到两个 `AND column = ?` 条件，同时按相同顺序追加参数。每个 placeholder 都直接与有类型的列比较，不存在无类型 `IS NULL` 参数；SQLite 与 PostgreSQL 使用同一构造结果，代次语义不会漂移。

### 方案三：扩展 DatabaseClient typed-parameter API

为所有 PostgreSQL 查询引入显式参数类型描述。这会扩大公共存储抽象和调用面，不符合本次生产热修的最小范围。

本次采用方案二。

## 实现设计

### expectation 条件

在 `backend/src/storage/account-cooldown-retest.repository.ts` 内增加文件私有构造器，输入只包含两个已存在的 expectation 字段，输出 SQL 片段和参数数组：

```ts
interface CooldownRetestExpectationInput {
  expectedConfigRevision?: number
  expectedObservationStartedAt?: string
}

interface CooldownRetestExpectationClause {
  sql: string
  params: Array<string | number>
}
```

构造规则：

- `expectedConfigRevision !== undefined` 时追加 `AND config_revision = ?` 和整数参数。
- `expectedObservationStartedAt !== undefined` 时追加 `AND cooldown_retest_observation_started_at = ?` 和文本参数。
- 未提供某项时不生成对应条件，也不传递占位参数。
- 成功恢复输入中的 `expectedConfigRevision` 仍是必填，因此成功 SQL 始终校验配置版本。

四个写回入口复用同一构造器：SQLite 成功、PostgreSQL 成功、SQLite 失败、PostgreSQL 失败。现有状态、删除标记和事务边界保持不变。

### 写回语义

失败写回仍在 PostgreSQL 最小事务中完成：锁定账户当前冷却状态、计算失败次数和退避、更新账户、标记分组账户统计 dirty，随后提交。expectation 不匹配时 `changed=false`，不累计失败，不改变冷却时间。

成功写回仍原子清理持久冷却状态、错误摘要、复测计数和观察起点，并恢复 `active + schedulable=1`。expectation 不匹配时保持原状态。

### 错误日志

`cooldown-account-retest.service.ts` 的 `onExhausted` 使用现有 `errorLogFields()` 合并队列异常字段，继续记录账户 ID、名称和尝试次数。日志不得写凭据、完整请求、响应正文或敏感连接串。数据库错误应至少保留安全的错误类型、code 和 message，使 `42P18`、超时或事务错误可直接定位。

## 测试设计

### RED

扩展 `account-probe-postgres-smoke.ts` 的完整账号探针覆盖，并新增窄范围 `cooldown-retest-postgres-smoke.ts` 在一次性隔离数据库中直接验证 repository 写回：

1. 当前代次失败能写回并把失败次数加一。
2. 当前代次成功能恢复 `active` 并清理冷却字段。
3. 错误配置版本或旧观察起点的成功、失败结果均 `changed=false`。

在旧实现上，真实 PostgreSQL 必须以 `42P18 could not determine data type of parameter` 失败，证明回归命中生产根因。

同时扩展本地回归，固定 expectation SQL 不再包含 `? IS NULL`，并固定耗尽日志携带 `errorLogFields(event.error, ...)`。

### GREEN

实现动态条件后复跑相同 PostgreSQL smoke，预期成功、失败和迟到结果断言全部通过；再运行 SQLite 冷却恢复回归，确认两种 driver 的代次语义一致。

### 集成验证

- `pnpm --filter juhe-ai-backend test:cooldown-retest-recovery`
- `pnpm --filter juhe-ai-backend test:cooldown-retest-postgres-smoke`
- `pnpm --filter juhe-ai-backend test:account-probe-postgres-smoke`（完整测试库 fixture 可用时）
- `pnpm --filter juhe-ai-backend test:database-client`
- `pnpm --filter juhe-ai-backend typecheck`
- `pnpm --filter juhe-ai-backend build`
- `git diff --check`

真实 PostgreSQL smoke 只使用测试环境连接串和任务专用随机数据库；脚本按随机账号 ID 清理，外层无论成功失败都强制删除整库。Redis 只使用测试 DB/namespace，不清理生产 namespace。

## 集成与发布

- 从最新 `master` 的独立 worktree 开发并提交。
- 开发期间在自然检查点 fetch 并合并最新 `origin/master`，每次合并后重跑最贴近回归。
- 验证和独立复核通过后合并到 `master` 并推送。
- 再将最新 `master` 合入本地 `feature/20260706-go`；不得覆盖该分支已有提交或未提交内容。若当前主工作区未提交内容阻止安全合并，使用新的 feature 集成 worktree 完成分支合并，再让原工作区仅刷新分支引用。
- 生产按第一档发布：无 schema、数据迁移或 Redis 清理。正式写生产前领取部署计划锁，使用 macOS temporary 服务接管，生成配对项目/业务备份，候选和主服务均验证 `1 server + 1 db-service + 3 worker`。
- 切回 main 后执行不少于 60 秒稳定观察，必须验证冷却复测 PostgreSQL 成功/失败写回 smoke 或等价结构级检查；任何 `42P18`、worker/supervisor/DB service fatal 都阻断上线并自动回退 temporary。

## 回滚

本次无 schema 或数据迁移。代码回滚切回上一 release 即可，业务备份仅作为标准发布恢复点。回滚后旧版仍存在 PostgreSQL 冷却复测写回缺陷，因此回滚只用于新 release 出现更高优先级故障，不能视为该问题的修复。
