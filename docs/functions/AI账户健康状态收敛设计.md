# AI 账户健康状态收敛设计

## 1. 目标

本文固定 AI 账户后台检查失败后的复检、终止和人工操作语义，避免账户长期停留在没有明确终点的 `pending_test` 或长期不可用状态。

本设计只调整账户持久状态机。人工“测试”仍是无状态诊断，不得激活或恢复账户。

## 2. 待检查失败

新建账户和需要重新确认连接配置的账户进入 `pending_test`，在后台检查通过前保持 `schedulable = false`。

- 第一次后台检查失败时写入 `health_check_failure_started_at`。
- `last_health_check_*` 保存最近一次失败事实，`next_health_check_at` 固定为本次失败后的 1 小时。
- 后续失败保留最早的 `health_check_failure_started_at`，不能用最近失败时间重置 24 小时窗口。
- 从首次失败起满 24 小时的下一次失败，repository 原子写入 `status = error`、`schedulable = false`、`last_error_code = account_activation_check_timeout` 和明确的 `last_error_message`，同时停止继续安排 `pending_test` 复检。
- 检查成功时清空失败计数和首次失败时间，将持久 `schedulable` 开关恢复为允许调度，并按账户时间计划把当前 `status` 激活或停用；时间计划不得反向关闭持久调度开关。

页面只对同时满足 `status = pending_test` 和存在 `last_health_check_at + last_health_check_error_*` 的自有账户显示“重新检查”。该操作原子重置为新账户式待检查状态，清空健康检查、冷却和运行失败标记，并立即投递后台激活检查。

## 3. 长期不可用

`temporary_unavailable` 和 `rate_limited` 继续使用 `cooldown_retest_observation_started_at` 作为自动恢复观察起点。

- `temporaryUnavailableContinuousProbeEnabled = true` 时保持现有长期恢复与 7 天终态。
- 该字段为 `false` 且状态为 `temporary_unavailable` 时，观察窗口固定为 10 分钟，下一次退避不得越过截止点；截止后必须执行一次真实最终探针，成功恢复，真实失败才写入 `cooldown_retest_limited_probe_timeout` 并转为 `error`。worker 停止或任务未形成真实上游尝试时不能只按墙钟判异常。
- 已处于 `temporary_unavailable` 时真实 `true -> false` 保存会从保存时间重开 10 分钟窗口并清理本轮复测计数；重复保存 `false` 不续期。`rate_limited` 始终沿用原恢复规则。

- 快速和慢速阶段沿用现有受控退避。
- 进入长期不可用阶段后固定每 1 小时复检一次，不使用更长的配置间隔。
- 从观察起点满 7 天的下一次失败，repository 原子写入 `status = error`、`schedulable = false`、清空 `cooldown_until`，并写入 `last_error_code = cooldown_retest_observation_timeout`。
- 终态错误信息必须包含观察起点、持续 7 天和最后一次上游错误，便于运维定位。
- 转为 `error` 后不再进入冷却复检候选。

## 4. 异常恢复和停用

- 页面和提示统一使用“异常恢复”。
- 自有 `error` 账户执行异常恢复时只能进入 `pending_test`，不能直接进入 `active`。
- 异常恢复会清空健康检查、冷却复测和运行失败标记，关闭调度并立即投递后台激活检查；只有后台检查成功才能恢复正常。
- 所有非 `disabled` 的自有账户都允许执行“停用账户”，包括 `pending_test`、`error`、`temporary_unavailable` 和 `rate_limited`。
- 手动启用仍只针对真正的 `disabled` 账户；待检查、异常和冷却状态不能借“启用账户”直接变为正常。
- 授权实例沿用自身的本地调度状态规则，本次“所有状态可停用”只扩展自有账户。

## 5. 存储契约

SQLite 当前 schema 和 PostgreSQL migration 均包含：

```text
accounts.health_check_failure_started_at
```

字段为空表示当前没有连续的待检查失败窗口。生产 PostgreSQL 上线前必须执行 `000042_w1b_account_health_failure_window.sql`；运行时代码不提供旧结构兼容或自动补列。

状态终止错误码：

| 场景 | 错误码 |
| --- | --- |
| 待检查从首次失败起满 24 小时 | `account_activation_check_timeout` |
| 冷却恢复从观察起点满 7 天 | `cooldown_retest_observation_timeout` |

## 6. 并发和原子性

- SQLite 写入在业务事务中读取并更新账户行。
- PostgreSQL 使用 `SELECT ... FOR UPDATE` 锁定账户行，并在同一事务内计算失败窗口和写入终态。
- 健康检查写回继续使用 `config_revision` 和真实成功时间守卫；配置已变化或检查期间出现更新成功信号时，旧失败不得覆盖新状态。
- 终态转换 SQL 同时校验当前状态，已被人工停用或其他任务改变的账户不得被迟到结果改写。

## 7. 验证

- `account-pending-test-regression`：1 小时复检、24 小时终态、重新检查和异常恢复。
- `account-health-check-regression`：SQLite 字段、PostgreSQL 行锁和双存储写入契约。
- `cooldown-retest-recovery-regression`：长期阶段每小时复检、7 天终态及终态恢复。
- `disabled-account-state-guard-regression`：待检查和异常自有账户均可停用，异常恢复不直接激活。
- `account-status-formatters-regression`：菜单名称、重新检查资格、异常/待检查停用入口和提示文案。
- Go migration 契约测试：PostgreSQL 新字段和非破坏性 Down 约束。
