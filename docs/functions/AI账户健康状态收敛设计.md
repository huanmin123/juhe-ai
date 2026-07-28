# AI 账户健康状态收敛设计

## 1. 目标

本文固定 AI 账户后台检查失败后的复检、提示和人工操作语义。多模型账户的 pending 激活与 active 精确能力状态都以 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 为准：pending_test 使用 durable Catalog activation selection，单模型失败不再拥有整号终止权；模型能力层不得写 `accounts.status`。

本设计只调整账户持久状态机。人工“测试”仍是无状态诊断，不得激活或恢复账户。

## 2. 自动探针结果契约

后台激活、周期哨兵、精确能力探针、质量确认、运行态恢复和冷却复测统一消费四类结果，但按任务 owner 区分账户连接、模型 execution 与 transport 状态：

- `complete_success`：协议成功且 framing 完整；按当前任务职责激活、记录正向健康或恢复匹配来源的自动状态。
- `framing_complete_neutral`：framing 完整但协议 / 业务未成功；对 transport 电路和冷却恢复保持中性。pending_test / active 都只把它作为当前 activation / sentinel Route / Attempt 的 execution 失败。
- `upstream_failure`：仅限连接失败、硬超时、读取中断或 framing 未完成的 `transport_incomplete`；它是 transport 状态机负向证据，也是 pending / active 当前精确 Attempt 的 execution 失败，不能从子 scope 升级整号。
- `probe_task_failure`、`stale` 或其他 `unknown`：任务、本地配置、解密、版本过期或无法归因；不计数、不改变状态。

不得从上游 HTTP status/body 推断凭据失效、授权失败、限流、封禁或服务故障。本地可验证的配置、解密和 OAuth token 生命周期问题走独立路径。人工测试与模型检测始终无状态。

## 3. 待检查激活失败

新建账户、导入请求中的 active 账户和需要重新确认连接配置的账户都进入 `pending_test`，在后台检查通过前保持 `schedulable = false`。管理页面、导入和内部创建均不得提供“确认风险后跳过激活检查”的旁路；人工测试也不能作为激活凭证。

- 配置 owner 先生成 ready Catalog / credential baseline 和 durable activation selection；只纳入免费 execution Attempt，优先 healthCheckModel 后按 Route / credential 稳定轮转。第一次精确失败写入 selection 诊断起点；只有同 definition / membership / binding 且通过 CAS 的独立失败才累计该 item，失败事实进入精确 capability ledger。
- `last_health_check_*` 保存最近一次诊断；pending_test 的 `transport_incomplete` 与 `framing_complete_neutral` 只增加当前 selection 诊断计数，任务故障和 stale/unknown 只更新有界调度。active 的相同结果只更新精确 Route / Attempt 能力和哨兵观察，不增加账户失败计数，也不授权账户状态转换。
- 首次失败满 24 小时仍没有任一 current Attempt complete_success 时，只写 `activation_unconfirmed / owner_action_required` 提示与运维告警，保持 pending_test / schedulable=false 并按小时重建 selection；不得写 account_activation_check_timeout 或整号 error。只有本地可证明覆盖全部 Route / credential 的共用配置错误，才由 account-global configuration owner 独立写 error。
- 任一当前 selection Attempt 的 `complete_success` 清空激活失败诊断，将持久 `schedulable` 开关恢复为允许调度，并按账户时间计划把当前 `status` 激活或停用；其他失败 Attempt 保留精确 blocked，时间计划不得反向关闭持久调度开关。
- `probe_task_failure` / `stale` / `unknown` 不修改计数或状态，只做有界重排。

页面对 pending_test 显示 selection 进度、精确失败摘要与“重新检查”。该操作原子重建 Catalog / activation selection 和账户哨兵计划，不清理已确认的精确 capability blocked、Key / 冷却状态或直接写 active。

自有待检查账户只提供“重新检查”和“停用账户”，不提供直接“恢复可调度”。需要恢复时必须重新投递后台激活检查并由 `complete_success` 退出 `pending_test`。

active 周期哨兵不使用账户级失败阈值：`complete_success` 只更新匹配 Route / Attempt 的正向事实；`framing_complete_neutral` 与 `transport_incomplete` 只向匹配的 execution capability 提交通用负向结果；`probe_task_failure / stale / unknown` 中性。无论连续多少次，周期哨兵都不得把整号写为 `temporary_unavailable`。

## 4. 长期不可用

`temporary_unavailable` 和 `rate_limited` 继续使用 `cooldown_retest_observation_started_at` 作为自动恢复观察起点。

- `temporaryUnavailableContinuousProbeEnabled = true` 时保持现有长期恢复与 7 天终态。
- 该字段为 `false` 且状态为 `temporary_unavailable` 时，观察窗口固定为 10 分钟，下一次退避不得越过截止点；截止后必须执行一次真实最终探针，`complete_success` 可按来源恢复，只有独立 `transport_incomplete` 才写入 `cooldown_retest_limited_probe_timeout` 并转为 `error`。完整 framing 中性结果、worker 停止或任务未形成真实上游尝试时不能只按墙钟判异常。
- 已处于 `temporary_unavailable` 时真实 `true -> false` 保存会从保存时间重开 10 分钟窗口并清理本轮复测计数；重复保存 `false` 不续期。`rate_limited` 始终沿用原恢复规则。

- 快速和慢速阶段沿用现有受控退避。
- 进入长期不可用阶段后固定每 1 小时复检一次，不使用更长的配置间隔。
- 只有独立 `transport_incomplete` 才启动并推进观察窗口；从观察起点满 7 天后的下一次独立 `transport_incomplete`，repository 原子写入 `status = error`、`schedulable = false`、清空 `cooldown_until`，并写入 `last_error_code = cooldown_retest_observation_timeout`。`framing_complete_neutral` 只顺延，`probe_task_failure` / `stale` / `unknown` 不计数。
- 终态错误信息必须包含观察起点、持续 7 天和最后一次本地 transport 错误，便于运维定位；不得用上游业务 status/body 充当终态依据。
- 转为 `error` 后不再进入冷却复检候选。

用户显式 `temporary_unavailable` / `rate_limited` 不进入无来源自动清理：只允许 TTL、匹配来源恢复动作或人工恢复；普通/后台无 provenance 成功不得清理。

## 5. 异常恢复和停用

- 页面和提示统一使用“异常恢复”。
- 自有 `error` 账户执行异常恢复时只能进入 `pending_test`，不能直接进入 `active`。
- 异常恢复会清空健康检查、冷却复测和运行失败标记，关闭调度并立即投递后台激活检查；只有后台检查成功才能恢复正常。
- 所有非 `disabled` 的自有账户都允许执行“停用账户”，包括 `pending_test`、`error`、`temporary_unavailable`、`rate_limited` 和 `quality_isolated`。
- 手动启用仍只针对真正的 `disabled` 账户；待检查、异常、质量隔离和冷却状态不能借“启用账户”直接变为正常。
- 正常自有账户提供“人工隔离”操作。该名称只表示人工触发动作，不新增账户状态；操作与“超级优先”一致，点击后不做二次确认，立即把账户写为 `temporary_unavailable`，由现有路由过滤、网关缓存失效、账号级冷却复测和成功恢复链路接管。授权账户实例不提供该操作，避免使用方改变来源账户的持久运行态。
- 授权实例沿用自身的本地调度状态规则，本次“所有状态可停用”只扩展自有账户。

## 6. 存储契约

迁移前当前 Node schema 和 PostgreSQL migration 均包含：

```text
accounts.health_check_failure_started_at
```

字段为空表示当前 activation selection 尚无失败诊断；该窗口同时接收 `framing_complete_neutral` 与 `transport_incomplete`，不是 transport 专用窗口，也不是账户终态计时器。生产 PostgreSQL 上线前必须执行 `000042_w1b_account_health_failure_window.sql`；运行时代码不提供旧结构兼容或自动补列。完整多模型目标不新增 SQLite 长期 adapter，最终由 Go + PostgreSQL owner 维护。

状态终止错误码：

| 场景 | 错误码 |
| --- | --- |
| 待检查满 24 小时仍无任一合法 activation success | 非终态提示 `activation_unconfirmed / owner_action_required`；不写 accounts.error |
| 冷却恢复从独立 `transport_incomplete` 观察起点满 7 天 | `cooldown_retest_observation_timeout` |

## 7. 并发和原子性

- SQLite 写入在业务事务中读取并更新账户行。
- PostgreSQL 使用 `SELECT ... FOR UPDATE` 锁定账户行，并在同一事务内计算失败窗口和写入终态。
- pending_test 写回使用 activation selectionId / cursor、route / attempt definition revisions、membership incarnations、binding 权限和真实成功时间守卫；配置已变化时旧结果不得覆盖新状态，多 scheduler / 重启只能推进同一 item 一次。active 精确能力结果使用相同 definition identity、generation 和 ledger CAS。每个精确 Attempt 保存单调递增的 `positiveObservationVersion`，physical claim 保存 `claimedPositiveObservationVersion`；业务成功只有在 PostgreSQL scope 串行化事务内原子提交 committed observation、version 增量和连续 outbox 后才算正向 fence，不能直接终结 active intent。若成功发生在 claim 之后，迟到负向 outcome 必须标记为 `superseded_by_newer_success`、不得阻断；当前 intent 终态释放预算并写 durable due，后续独立验证使用新 generation / physicalExecutionId。
- 终态转换 SQL 同时校验当前状态，已被人工停用或其他任务改变的账户不得被迟到结果改写。

## 8. 验证

- `account-pending-test-regression`：durable activation selection 中 B 失败 / A 成功可激活、完整 HTTP / transport 失败只隔离精确 Attempt、1 小时重建、24 小时仅 owner_action_required、task unknown 中性、重启 / 多 scheduler cursor 单调、重新检查和异常恢复；不存在人工跳过激活旁路。
- `account-capability-positive-observation-regression`：业务成功不直接终结 active intent；claim 后新成功使迟到负向 outcome superseded，且只写一次 durable due，由新 generation / TaskID 执行独立验证。
- `account-health-check-regression`：SQLite 字段、PostgreSQL 行锁和双存储写入契约。
- `cooldown-retest-recovery-regression`：长期阶段每小时复检、仅 transport 证据推进 7 天终态、显式来源恢复边界及终态恢复。
- `disabled-account-state-guard-regression`：待检查和异常自有账户均可停用，异常恢复不直接激活。
- `account-status-formatters-regression`：菜单名称、重新检查资格、异常/待检查停用入口、无“恢复可调度”旁路和提示文案。
- Go migration 契约测试：PostgreSQL 新字段和非破坏性 Down 约束。
