# AI 账户健康状态收敛设计

## 1. 目标

本文固定 AI 账户后台检查失败后的复检、终止和人工操作语义，避免账户长期停留在没有明确终点的 `pending_test` 或长期不可用状态。

本设计只调整账户持久状态机。人工“测试”默认仍是无状态诊断；唯一例外是已保存自有账户以保存的检查模型和精确请求形态完成列表测试、协议成功且当前持久不可调度时，可在任务终态和配置版本 CAS 同时匹配后恢复持久调度资格。该例外不适用于草稿、授权实例、停用或质量隔离账户，也不替代后台激活检查。

## 2. 自动探针结果契约

后台激活、周期健康、请求失败确认、质量失败确认、运行态恢复和冷却复测统一消费四类结果，但按任务职责区分账户可用性与 transport 状态：

- `complete_success`：协议成功且 framing 完整；按当前任务职责激活、记录正向健康或恢复匹配来源的自动状态。
- `framing_complete_neutral`：framing 完整但协议 / 业务未成功；对 transport 电路和冷却恢复保持中性，但对固定模型、固定协议的后台激活、周期健康、请求失败确认和质量失败确认属于一次账户可用性失败，可累计对应健康阈值。
- `upstream_failure`：仅限连接失败、硬超时、读取中断或 framing 未完成的 `transport_incomplete`；既是 transport 状态机负向证据，也是账户可用性失败。
- `probe_task_failure`、`stale` 或其他 `unknown`：任务、本地配置、解密、版本过期或无法归因；不计数、不改变状态。

不得从上游 HTTP status/body 推断凭据失效、授权失败、限流、封禁或服务故障。本地可验证的配置、解密和 OAuth token 生命周期问题走独立路径。除精确匹配保存检查配置的列表测试恢复例外外，人工测试与模型检测始终无状态。

## 3. 待检查 transport 失败

未显式指定状态的新建账户、所有导入账户和需要重新确认连接配置的账户进入 `pending_test`，在后台检查通过前保持 `schedulable = false`。普通创建接口显式提交 `status = active` 是创建时一次性的人工风险确认：直接保存为 `active + schedulable` 并跳过首次后台检查；`status = disabled` 仍保持停用且不投递检查。导入载荷中的 `active` 仍先规范化为 `pending_test`。该例外不改变默认创建、导入、配置变更重新检查或异常恢复进入 `pending_test` 的状态机。管理页面对已存在待检查账户仍提供“恢复可调度”显式确认；该确认仍受账户时间计划和套餐过期状态约束。

- 第一次独立账户可用性失败写入 `health_check_failure_started_at`；只有后续独立、同配置 revision 且通过 CAS 的可用性失败才累计并保留最早起点。
- `last_health_check_*` 保存最近一次诊断；`transport_incomplete` 与受控探针的 `framing_complete_neutral` 都增加健康失败计数，任务故障和 stale/unknown 只更新有界调度。
- 从首次账户可用性失败起满 24 小时后的下一次独立失败，repository 才可原子写入 `status = error`、`schedulable = false`、`last_error_code = account_activation_check_timeout` 和明确的 `last_error_message`，同时停止继续安排 `pending_test` 复检。
- `complete_success` 清空自动 transport 失败计数和首次失败时间，将持久 `schedulable` 开关恢复为允许调度，并按账户时间计划把当前 `status` 激活或停用；时间计划不得反向关闭持久调度开关。
- `probe_task_failure` / `stale` / `unknown` 不修改计数或状态，只做有界重排。

页面只对同时满足 `status = pending_test` 和存在 `last_health_check_at + last_health_check_error_*` 的自有账户显示“重新检查”。该操作原子重置为新账户式待检查状态，清空健康检查、冷却和运行失败标记，并立即投递后台激活检查。

自有待检查账户还可以在操作菜单中执行“恢复可调度”。该人工恢复要求操作者确认账户当前可用，原子恢复账户的持久调度资格并清理运行态可用性；当前时间计划不生效时账户仍保持停用。此操作不适用于授权账户；健康检查失败时保留“重新检查”入口，供不跳过检查的恢复路径使用。

## 4. 长期不可用

`temporary_unavailable` 和 `rate_limited` 继续使用 `cooldown_retest_observation_started_at` 作为自动恢复观察起点。

- `temporaryUnavailableContinuousProbeEnabled = true` 时保持现有长期恢复与 7 天终态。
- 该字段为 `false` 且状态为 `temporary_unavailable` 时，观察窗口固定为 10 分钟，下一次退避不得越过截止点；截止后必须执行一次真实最终探针，`complete_success` 可按来源恢复，只有独立 `transport_incomplete` 才写入 `cooldown_retest_limited_probe_timeout` 并转为 `error`。完整 framing 中性结果、worker 停止或任务未形成真实上游尝试时不能只按墙钟判异常。
- 已处于 `temporary_unavailable` 时真实 `true -> false` 保存会从保存时间重开 10 分钟窗口并清理本轮复测计数；重复保存 `false` 不续期。`rate_limited` 始终沿用原恢复规则。

- 只有独立 `transport_incomplete`（自动探针归类为 `upstream_failure`）才推进冷却恢复失败计数：第 1 至 5 次严格按 `3`、`6`、`12`、`24`、`48` 秒进入快速通道；第 6 次起进入慢速通道。慢速每轮以账户 ID、冷却 generation 和失败次数的确定性散列映射到闭区间 `1` 至 `5` 分钟，账户之间错峰，进程重启后不漂移，也不受 `maxPauseMinutes` 截断。
- `framing_complete_neutral`、`probe_task_failure` 和其他已执行但无法归因的 `unknown` 都不增加持久失败计数、不推进长期或终态；它们同样以账户 ID、generation 和当前 `cooldown_until` 在 `1` 至 `5` 分钟内错峰顺延。顺延后的截止时间会参与下一轮散列，避免同一批账户反复同时探测。`stale` 队列项由投递和写回围栏直接丢弃，不安排新的复测。
- 默认从 `cooldown_retest_observation_started_at` 连续累计真实 `upstream_failure` 满 `12` 小时后的下一次真实失败进入长期不可用阶段；长期阶段固定每 `1` 小时复检一次，不使用更长的配置间隔。
- 只有独立 `transport_incomplete` 才启动并推进观察窗口；从观察起点满 7 天后的下一次独立 `transport_incomplete`，repository 原子写入 `status = error`、`schedulable = false`、清空 `cooldown_until`，并写入 `last_error_code = cooldown_retest_observation_timeout`。`framing_complete_neutral` 只顺延，`probe_task_failure` / `stale` / `unknown` 不计数。
- 终态错误信息必须包含观察起点、持续 7 天和最后一次本地 transport 错误，便于运维定位；不得用上游业务 status/body 充当终态依据。
- 转为 `error` 后不再进入冷却复检候选。

用户显式 `temporary_unavailable` / `rate_limited` 不进入无来源自动清理：只允许 TTL、匹配来源恢复动作或人工恢复；普通/后台无 provenance 成功不得清理。

## 5. 异常恢复和停用

- 页面和提示统一使用“异常恢复”。
- 自有 `error` 账户执行异常恢复时只能进入 `pending_test`，不能直接进入 `active`。
- 异常恢复会清空健康检查、冷却复测和运行失败标记，关闭调度并立即投递后台激活检查；只有后台检查成功才能恢复正常。
- 所有非 `disabled` 的自有账户都允许执行“停用账户”，包括 `pending_test`、`error`、`temporary_unavailable` 和 `rate_limited`。
- 手动启用仍只针对真正的 `disabled` 账户；待检查、异常和冷却状态不能借“启用账户”直接变为正常。待检查账户仅可通过上述确认风险的“恢复可调度”人工操作例外直接恢复。
- 正常自有账户提供“人工隔离”操作。该名称只表示人工触发动作，不新增账户状态；操作与“超级优先”一致，点击后不做二次确认，立即把账户写为 `temporary_unavailable`，由现有路由过滤、网关缓存失效、账号级冷却复测和成功恢复链路接管。授权账户实例不提供该操作，避免使用方改变来源账户的持久运行态。
- 授权实例沿用自身的本地调度状态规则，本次“所有状态可停用”只扩展自有账户。

## 6. 存储契约

SQLite 当前 schema 和 PostgreSQL migration 均包含：

```text
accounts.health_check_failure_started_at
```

字段为空表示当前没有连续的待检查账户可用性失败窗口；是否累计由该任务的探针结果契约决定。生产 PostgreSQL 上线前必须执行 `000042_w1b_account_health_failure_window.sql`；运行时代码不提供旧结构兼容或自动补列。

状态终止错误码：

| 场景 | 错误码 |
| --- | --- |
| 待检查从首次独立 `transport_incomplete` 起满 24 小时 | `account_activation_check_timeout` |
| 冷却恢复从独立 `transport_incomplete` 观察起点满 7 天 | `cooldown_retest_observation_timeout` |

## 7. 并发和原子性

- SQLite 写入在业务事务中读取并更新账户行。
- PostgreSQL 使用 `SELECT ... FOR UPDATE` 锁定账户行，并在同一事务内计算失败窗口和写入终态。
- 健康检查写回继续使用 `config_revision` 和真实成功时间守卫；配置已变化或检查期间出现更新成功信号时，旧失败不得覆盖新状态。
- 终态转换 SQL 同时校验当前状态，已被人工停用或其他任务改变的账户不得被迟到结果改写。

## 8. 验证

- `account-pending-test-regression`：完整 HTTP / 协议失败中性、独立 transport 失败的 1 小时复检与 24 小时终态、重新检查和异常恢复。
- `account-health-check-regression`：SQLite 字段、PostgreSQL 行锁和双存储写入契约。
- `cooldown-retest-recovery-regression`：长期阶段每小时复检、仅 transport 证据推进 7 天终态、显式来源恢复边界及终态恢复。
- `disabled-account-state-guard-regression`：待检查和异常自有账户均可停用，异常恢复不直接激活。
- `account-status-formatters-regression`：菜单名称、重新检查资格、异常/待检查停用入口和提示文案。
- `account-test-local-restore-regression`：保存账户的模型和精确请求形态匹配时仅恢复自有不可调度账户；任一选择不匹配时保持原状态。
- Go migration 契约测试：PostgreSQL 新字段和非破坏性 Down 约束。
