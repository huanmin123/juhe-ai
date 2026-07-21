# AI 账户短窗口热质量与精准切号设计

> 当前状态：目标设计，尚未落地实现。
>
> 本文定义普通路由账户调度、短窗口热质量、故障单飞、受控半开、请求内精准切号和探活恢复的统一目标合同。现行实现仍以 [策略路由设计](策略路由设计.md)、[普通路由速度优先延迟切换设计](普通路由速度优先延迟切换设计.md)、[AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)、[网关错误处理完整链路](网关错误处理完整链路.md) 和 [网关异常重试与兜底策略](网关异常重试与兜底策略.md) 为准；实施本文时必须按第 14 节迁移顺序收敛冲突，不能把本文当作现行代码已经满足的事实。

## 1. 决策摘要

本设计先固定以下不可变合同：

1. 路由策略是最高业务调度层。路由先决定模式、分组、分组顺序、目标和路由覆盖；账户层不能重新选组、重抽权重、重建轮询环或自行 fallback。
2. 账户层只在路由允许的目标范围内回答可执行性，并在最终有效候选带内按账户配置层排序。路由目标可以覆盖账户偏好，但账户电路不能被路由偏好绕过。
3. 同一账户配置层使用“所有唯一候选最多一轮”，不再使用固定的“同层失败 2 个账号”预算。5 个 P1 账号中，前 4 个在请求救援准入时间内结束时，游标必须继续到第 5 个；第 5 个仍失败且仍满足时间准入时才允许进入下一层，期限先到则明确返回 deadline_exhausted。
4. 首字统一的是事实和仲裁器，不是一个适用于所有 lane、模式和请求的数值。软观察、lane hard timeout、attempt lifetime、idle timeout 和请求级救援期限必须保持不同原因码。
5. 自动电路只消费网关本地可验证的 transport 事实。完整 HTTP/SSE framing 在未命中用户显式高级规则时透明转发；上游状态码、响应头、错误码和正文不能被内部自动机制解释为切号或账号故障。
6. 运行态分为配置意图、热运行态和持久异常三层。首次 transport 失败先阻断对应 protocol-model 作用域并发起唯一 confirmation；只有独立作用域证据满足条件，才升级到账号级电路。
7. 热质量保存在 Redis 或进程内存，按最近 5、10、30 分钟计算。质量只能在完全相同的有效账户配置层内排序，不能跨优先级、备用或分组。
8. 同层探索允许存在，但只能是低频、真实业务流量、同一有效层内的首选替换；不增加请求、不访问 P2/备用、不绕过会话亲和、电路、容量和路由覆盖。
9. route coordinator 独占请求级游标、尝试集合、等待预算和跨组推进。routes、preparation、candidate-filter、dispatch 不得各自创建 fallback、重试预算或新的尝试轮次。
10. scoped 电路负责秒级止损，持久 `temporary_unavailable` 只在至少 5 分钟、3 个独立后台轮次和整账号能力覆盖后建立。开启持续探活时长期低频恢复，不因纯 transport 波动持续 7 天就自动判为 `error`；恢复必须经过串行 canary 和渐进放量。

## 2. 背景与目标

上游账号的可用性可能在秒级变化：前 10 分钟正常、随后 2 小时超时，或者前一分钟成功、后一分钟持续失败。日级历史和长期质量无法代表当前请求；偶尔成功两次也不能证明账号已经恢复。

当前生产问题集中在四个方面：

- 同一个物理账号被多个会话、多个 IP 或多个授权实例同时打入，首次失败后仍继续接受新请求。
- 同账号重试、recoverable 重放、容量等待和分组 fallback 分散在多层，导致同一个 P1 被重复扫描，客户端长时间卡住。
- 运行态中 recovery_wait、precheck_pending、半开和 UI 展示的语义不一致，账号实际不可承接但仍显示为可调度。
- 质量统计是历史或后台聚合，不足以指导最近几分钟的选择；后排账号没有样本时又可能永久饥饿。

本文目标是以最小额外请求代价完成以下闭环：

```text
路由策略确定范围
  -> 冻结请求级路由计划
  -> 账户候选过滤与同层游标
  -> 一次真实 attempt
  -> 终态幂等记录
  -> 热质量 / 电路 / 用量投影
  -> 必要时唯一 confirmation 或探活 lease
  -> RECOVERING 三次 canary
  -> CLOSED 或再次 OPEN
```

## 3. 适用范围与非目标

### 3.1 适用范围

- 普通路由 normal 下的 cost_first 和 speed_first。
- 已经由策略路由选定的分组、主备顺序、RR、weighted、hybrid 目标中的账户调度。
- OpenAI 兼容网关的 JSON、SSE、Responses 和协议适配后的真实上游 attempt。
- standalone 的进程内热状态和 performance 的 Redis 共享热状态。

### 3.2 非目标

- 不改变 API Key、路由策略、分组绑定、权重、轮询、主备和 hybrid 的产品语义。
- 不以日级、周级或完整历史质量直接参与实时选号。
- 不为获得质量样本向较低优先级、备用层或其他分组主动发业务流量。
- 不做多个上游同时抢跑后取最快结果。
- 不用热质量直接修改 accounts.status、schedulable、cooldown_until 或用户配置的优先级。
- 不使用上游状态码、响应头、错误码、正文或供应商文案建立系统自动故障分类。

## 4. 路由层与账户层边界

### 4.1 固定层级

```text
API Key
  -> 路由策略
    -> 路由模式、分组顺序、目标和覆盖
      -> 路由选定范围内的账户可执行性
        -> 真实上游 attempt
```

路由策略高于账户策略，但“高于”不代表路由可以强制调用已经不可执行的账号。账户层只提供硬可执行性事实；它不能借此扩大或重写路由范围。

### 4.2 请求级路由计划

请求开始时由 route coordinator 创建不可变 routePlanSnapshot：

```text
routePlanId
routeStrategyId
mode
orderedAllowedTargets
groupCursor
candidateCursor
pendingBlockedTargets
weightedDecisionToken
roundRobinDecisionToken
hybridDecision
hybridActionPlan {
  actionSequence
  allowedTargets
  policyRevision
  replayRequirement
}
requestReplayPolicy {
  replayDisposition
  idempotencyScope
  adapterRevision
}
routeObjectivePolicy
groupDispatchPolicyRevision
dispatchRevision
```

快照冻结本次请求的组顺序、权重决定、轮询起点、hybrid action plan、请求重放判定、路由目标策略和允许范围。routeOverrideBand 由 coordinator 汇总冻结 route reducer 或当前选中组的 `groupDispatchReducer` 结果，并结合当前动态运行态求值；它可以让候选暂时不可执行，但不能重新抽样、重建环、重算权重或扩大目标范围。

### 4.3 routeOverrideBand 与 baseTierKey

账户候选先经过 route coordinator 汇总出的最终有效覆盖带，再在覆盖带内排序：

```text
routePlanSnapshot
  -> hard eligibility
  -> route / group reducer
  -> routeOverrideBand
  -> baseTierKey
  -> mode-specific affinity / hot quality order
  -> same-tier exploration
```

routeOverrideBand 是 coordinator 汇总对应 route reducer 或选中组 `groupDispatchReducer` 后得到的候选子集和原因，至少记录：

```text
routeObjective
manualMigrationTarget
latencyDegradedTarget
overrideReason
groupDispatchPolicyRevision
```

`routePlanSnapshot` 只冻结允许范围、策略 revision 和路由决定，不冻结瞬时队列、并发或容量快照。账户层不能把手动迁移、快速模式慢目标、高并发 fallback 和账户比较器重新合并成第二个 comparator。不同路由模式已有的相对顺序由对应 route reducer 或选中分组的 `groupDispatchReducer` 产出；动态 reducer 每次只返回当前有效覆盖带和原因，最终推进仍由 coordinator 完成。

baseTierKey 固定为：

```text
modelMatchRank
+ fallbackEnabled
+ superPriorityEnabled
+ priority
```

热质量、会话亲和和同层探索都不能跨越 baseTierKey；快速模式和高并发等既有覆盖若需要跨越，必须由对应 reducer 在 routeOverrideBand 中明确表达，而不是由通用账户层偷偷改变优先级。

同层内部比较顺序按模式固定：

- `cost_first`：有效 session/cache affinity -> 热质量 -> 稳定顺序；soft checkpoint 只能记录样本，不能触发慢切号。
- `speed_first`：先应用路由层 `latency_degraded` 覆盖，再在同一覆盖带和 base tier 内使用热质量、有效 affinity 与稳定顺序；affinity 不能拉回已降级账号。
- 高并发分组：由 `groupDispatchReducer` 保留既有 migration、fallback-on-queue、soft-busy、超级优先、priority 和容量比较；热质量只是其同档输入，coordinator 不复制该比较器。

### 4.4 模式约束

| 模式 | 路由层负责 | 账户层负责 |
| --- | --- | --- |
| cost_first | 保持组、账户偏好和亲和，只在 transport 失败或显式策略后切号 | 过滤电路、授权、能力、容量和已尝试身份；同层一轮后报告结果 |
| speed_first | 速度目标、latency_degraded、安全窗口和可接受的跨层切号 | 过滤不可执行账号；不得把已确认慢账号复活到覆盖带 |
| failover | 主用、备用和后续分组顺序 | 只报告当前分组结果，不自行进入备用组 |
| round_robin | 本轮环、起点和回访顺序 | 推进当前分组候选游标，不重建环 |
| weighted | 本次权重 token 和允许目标 | 不重抽权重、不把质量写成持久重分配 |
| hybrid_smart | 评分、模型档位、repair/upgrade/return action | 在目标档位内过滤账户；质量 action 必须由 coordinator 执行 |

本文不新增 hybrid action。已有 hybrid 路由在请求开始时把允许的评分、repair、upgrade、return 动作及目标预编为不可变 `hybridActionPlan`；这些是用户显式选择 hybrid 策略后产生的路由动作，不是账户电路动作。动作只能在语义尚未提交、请求可安全重放且响应检查使用有界缓冲时执行，并继续使用同一个请求 hard deadline、route plan 和 attempted 集合。否则返回当前完整响应或唯一失败，不得在响应层临时扩大目标或重算路由。

`replaySafe` 默认拒绝，只有协议适配器明确证明以下条件全部满足才允许 repair/upgrade：

- 请求属于已声明的纯模型推理 endpoint，不是 Files、Uploads、Batches、Fine-tuning、资源创建/删除或未知 endpoint；
- 没有已经执行或可能在上游执行的 hosted tool、外部 action、后台任务、持久 store 写入或其他不可幂等副作用；
- 请求级 `committedAttemptId == null`，服务端尚未对外提交工具调用或可见输出；
- 请求体仍在有界可重放预算内，且 provider adapter 明确返回 `replaySafeBeforeCommit=true`。

未知协议、未知 endpoint、`background/store` 类持久化请求或无法证明副作用边界时，一律不做 hybrid repair/upgrade。用户选择 hybrid 路由不等于自动放宽 replay 安全门槛。

## 5. 候选快照与身份

### 5.1 候选快照

候选快照保留绑定实例，不在 SQL 层提前合并授权实例。每个候选至少包含：

```text
dispatchBindingId
accountId
credentialSourceAccountId
physicalResourceKey
accountRuntimeKey
providerProtocolProfile
requestLane
modelFamily
baseTierKey
routeOverrideBand
stableOrderKey
uniqueCandidateId
candidateSetRevision
dispatchRevision
```

快照冻结候选允许范围、层级和稳定顺序，不冻结动态容量和电路结果；每次真正占用并发槽或发出请求前，必须重新核对持久硬资格、运行态 generation、容量和 dispatchRevision。

### 5.2 三类身份

```text
physicalResourceKey
  = credentialSourceAccountId + provider + upstreamProfile

dispatchBindingKey
  = accountId + systemAccountId + groupId + authorizationId

protocolModelKey
  = physicalResourceKey + protocolProfile + endpoint + requestLane + modelFamily
```

- physicalResourceKey 共享真实凭据、代理、并发和上游资源事实。
- dispatchBindingKey 保留授权实例的本地权限、额度、分组绑定、亲和和策略状态。
- protocolModelKey 用于本请求的真实 transport 去重和作用域电路。

本地权限、配额或绑定失败只写 binding 作用域；真实 transport 故障先写 protocolModelKey 作用域。只有账号级聚合条件满足，或用户显式规则明确指定账号级动作，才升级到物理账号级电路。

这是对现行授权实例运行态隔离的有意迁移，边界固定如下：

| 事实 | 是否跨授权实例共享 | 展示与恢复 |
| --- | --- | --- |
| binding 权限、额度、优先级、备用、亲和、持久状态 | 否 | 只显示在当前授权实例，由当前实例规则恢复 |
| 物理凭据、代理、并发、上游 profile | 是 | 所有引用同一来源资源的实例读取同一物理事实 |
| protocol-model transport 电路 | 仅相同物理资源、协议、endpoint、lane、模型族共享 | 授权实例显示 `source_runtime_blocked` 及来源 scope；共享 canary 恢复 |
| account 聚合电路 | 是，但只能由独立 protocol-model 证据或显式账号动作建立 | 共享 generation 和恢复 lease，不改写各实例持久状态 |
| 5/10/30 分钟热质量 | 仅相同 `physicalResourceKey + protocolProfile + requestLane + mappedModelFamily` 共享 | 只作为当前覆盖带和 base tier 内的短窗口排序输入，不回写 binding 持久状态 |
| `latency_degraded` | 否；固定在 `routeStrategyId + groupId + dispatchBindingKey` 作用域 | 只影响启用该快速目标的策略范围，不传播为物理账号故障 |
| 用户显式策略 block | 按策略配置的 binding/account scope | 自动 canary 不能清理 |

### 5.3 请求级 attempted 集合

同一请求维护以下集合，跨组、fallback、hybrid 和上下文切换都不能清空：

```text
attemptedProtocolModelKeys
reservedProtocolModelKeys
attemptedBindingKeys
attemptedAccountRuntimeKeys
attemptedKeyFingerprints
attemptedPurposes
```

coordinator 在进入异步 dispatch 前先把 identity 原子加入 `reservedProtocolModelKeys`；随后在真正网络派发线性点取得 `dispatchAdmissionToken`，一次性核对 cluster runtime readiness、`runtimeRecoveryEpoch`、当前节点 membership lease、circuit state/generation、dispatchRevision 和容量 permit。token 必须携带取得时的 recovery epoch、nodeLeaseId 和 admissionId；任一 revision 不匹配都在发网前拒绝并要求刷新运行态，不能降级为旧快照派发。

取得 token 即定义为“已经在途”，并在同一个原子操作中从 reserved 转入 attempted，同时写入按 attemptId 索引的 admission registry：

```text
attemptId
protocolModelKey
nodeId
nodeLeaseId
runtimeRecoveryEpoch
requestHardDeadlineAt
admittedAtMs
```

终态 CAS 必须关闭对应 registry entry。确认没有发网时才允许释放 reserved；已经取得 token 的请求即使随后失去 Redis，也只能按 unknown/outbox 合同收尾，不能假装没有 admission。SUSPECT 转换只能阻止线性点之后的新 admission，不能撤销之前已经取得 token 的首轮在途请求。

普通业务取得 admission 后加入 attemptedProtocolModelKeys。本地 Key 准备失败或用户明确允许 Key 动作时，只加入 attemptedKeyFingerprints。confirmation、half-open、hybrid repair 是带 attemptPurpose 和 lease/generation 的显式例外，不能被普通 attempt 再次占用。

同一物理协议模型通过不同授权实例或后续分组出现时，本请求仍沿 route plan 访问并独立校验每个 binding 的授权和额度，但不得重复发出同一普通业务 attempt；重复物理目标记录 `duplicate_physical_target` 后推进原路由游标。这是有意的跨组行为迁移，不代表跳过后续不同物理目标。

### 5.4 候选窗口与完整性

当前数据库扫描窗口和 hydrate 窗口不是全量证明。设计要求：

- keyset 必须使用一致候选 revision 和唯一总序，例如 `candidateSetRevision + stableOrderKey + uniqueCandidateId`，而不是 offset 重新排序。
- 页间 revision 变化、无法维持 MVCC snapshot 或唯一总序时立即返回 window_incomplete，不能在新旧候选集合之间继续证明完整耗尽。
- 页面或窗口被截断、hydrate 丢失或 scanLimitReached 时返回 window_incomplete。
- window_incomplete 不能伪装成 hard_exhausted，也不能让请求重新扫描已经 attempted 的身份。
- 只有完整证明当前路由范围没有硬可执行候选时，才允许 hard_exhausted。

## 6. Route coordinator 合同

### 6.1 唯一 owner

route coordinator 是以下状态的唯一 owner：

- route plan、分组和候选游标；
- 请求级 attempted 集合；
- 普通 attempt、confirmation、hybrid repair 的用途预算；
- routeCoordinationBudget 和请求救援墙钟；
- temporarily_blocked 的等待、回访和后续路由推进；
- 语义提交前的切号决定。

账户层、准备层和 dispatch 层不得直接调用分组 fallback，不得创建第二套预算，不得把 recoverable 账号重新塞回同一普通扫描轮次。

### 6.2 账户可用性结果

账户层返回：

```text
dispatchable {
  candidates
  routeOverrideBand
  runtimeRecoveryEpoch
  circuitGeneration
  dispatchRevision
}

temporarily_blocked {
  earliestRetryAtMs
  confirmationInFlight
  blockedTargets
  queueBlocked
  queueWaitCostMs
  reason
}

request_exhausted {
  reason
  attemptedProtocolModelKeys
  attemptedBindingKeys
  attemptedKeyFingerprints
  completeness
}

hard_exhausted {
  reason
  completeness
}
```

completeness 至少区分 complete、window_incomplete 和 deadline_exhausted。后两者不能被记录为长期账号耗尽。

账户层不得构造最终客户端错误。路由协调器根据当前模式决定继续当前分组、推进既有备用/环/权重/评分目标、在统一预算内等待，或结束请求。

### 6.3 同层唯一候选一轮

同一 baseTierKey 的普通业务游标单调前进：

1. CLOSED 且硬资格通过的候选按稳定顺序进入尝试。
2. 已经 SUSPECT / OPEN / OPEN_UNKNOWN / HALF_OPEN / RECOVERING / QUARANTINED 的普通候选直接跳过，不占用普通 attempt。
3. 容量忙、队列等待或暂时阻断的候选进入 pendingBlockedTargets，不算已尝试；回访时仍只能消费该候选一次。
4. 真实 transport attempt 发出后，候选永远不能回到本请求的普通游标。
5. 当前层所有唯一候选完成一轮后，且仍有请求救援时间，才进入下一层。
6. 请求救援墙钟先耗尽时，返回 deadline_exhausted，不能声称当前层已经完整耗尽。

因此，5 个 P1 的严格合同是“不允许被固定失败次数提前截断”：只要前 4 个产生终态且仍允许启动救援 attempt，P1 #5 必须取得一次 admission 机会。这里的“到达第 5 个”指真实派发，不承诺第 5 个在任意合法慢时延内完成。前 4 个各自占满 lane hard 或请求 hard deadline 已到期时，系统必须按期限结束并记录 `deadline_exhausted`，不能谎称完整扫描，也不能为了无条件到达第 5 个而缩短已启动 attempt 的 lane hard。

### 6.4 路由回访

pendingBlockedTargets 只能是当前 route plan 已允许的目标子集。回访前必须重新检查：

```text
RequestRescueLedger.committedAttemptId == null
serverRescueDeadlineAt 未到期
circuit generation / lease 有效
dispatchRevision 未变化
capacity 仍可预占
```

回访不能重抽 RR/weighted、扩大 hybrid 目标或重新加入已经 attempted 的候选。

## 7. 首字事件与请求预算

### 7.1 统一事实，不统一所有数值

每个真实 attempt 只创建一个 AttemptArbiter，但保留不同约束：

```text
AttemptArbiter {
  attemptId
  attemptPurpose
  lane
  softCheckpointAt
  laneHardAt
  attemptLifetimeAt
  idleDeadlineAt
  networkWriteState
  requestAbortSignal
  terminalCas
}
```

首字事实定义为：

- 非流式：首个可见 body 字节；
- 流式：首个可见语义 chunk；
- 响应头、SSE heartbeat、空事件、内部缓冲不算首字；
- 每个 attempt 只产生一次 observed / deadline_reached / cancelled / unknown。

统一事件不代表把快速模式软观察、普通 lane hard timeout 和 attempt lifetime 压成同一个数字。任何账户层、响应层或快速模式都不得再次创建首字 timer。

语义提交使用请求级 winner CAS，而不是每个 attempt 各自决定。AttemptArbiter 不保存独立 `semanticCommitted`；所有 guard 只读取 RequestRescueLedger 的 `committedAttemptId` 和 `semanticCommitVersion`。唯一 downstream writer 在首个可见语义事件入队下游响应流之前执行 `trySemanticCommit(attemptId, expectedVersion)`；首次成功原子写入 winner，其他 attempt 永久失去语义写权限。

CAS 失败的 loser 必须立即触发自己的 AbortController、释放容量 permit / reservation / lease，并以 `superseded_before_commit` 提交唯一终态。该终态只进入请求审计和 admission registry 清理，不进入热质量、电路失败、RECOVERING 计数或客户端 usage；不得继续占用并发直到请求 hard deadline。语义 write、soft/hard timer、abort 和切号都必须经过请求级 writer/ledger 互斥；winner 提交后只允许继续该响应、输出协议错误或断流，绝不允许其他 attempt 换号或写入。

### 7.2 四层时间边界

| 边界 | 作用 | 是否写自动账户电路 |
| --- | --- | --- |
| softCheckpoint | 路由观察、速度样本或请求内 handoff | 否 |
| laneHardAt | 首响应/读取未完成的真实 transport 失败 | 是，写对应作用域 |
| attemptLifetimeAt / idleDeadlineAt | 首字后的资源和流式活跃生命周期 | 只有真实读取中断/EOF 才写对应作用域 |
| requestHardDeadlineAt | 请求级最终墙钟，防止无限挂起 | 不把正常慢请求伪装成账号故障 |

另有 serverRescueDeadlineAt，它只决定还能不能启动新的隐藏救援 attempt；已经启动的 attempt 继续遵守自己的 lane hard/lifetime，除非请求 hard deadline 或客户端取消。任何候选均分值只能用于 `speed_first` 在存在替代候选时的 soft handoff 决策，不能改写 laneHardAt。

### 7.3 普通模式与快速模式

- cost_first：soft checkpoint 只记录样本，不触发 handoff；只有真实 transport 失败或用户显式策略动作才切号，继续保持现有成本优先语义。
- speed_first：同一个 soft event 先幂等记录慢样本，再由 slowTriggerCount、latency_degraded、安全写出窗口和模式切号上限决定是否切号。慢样本不能直接打开账户电路。
- 真正的 lane hard timeout、读取中断、EOF 或连接失败才进入 transport 电路和 confirmation。
- 图像、长思考、embedding 等 lane 保留各自 timeout profile；不能因为速度模式的软阈值中断合法长耗时请求。
- 任何模式准备在已有 attempt 之后启动新 attempt 时，都必须通过 7.6 的统一重放授权；速度优先、故障切号和 hybrid 不得各自维护一套更宽松的规则。

### 7.4 请求救援账本

每个请求只有一个 RequestRescueLedger：

```text
RequestRescueLedger {
  requestStartAt
  routePlanCreatedAt
  requestHardDeadlineAt
  serverRescueDeadlineAt
  coordinationRemainingMs
  activeGroupQueueDeadlineAt
  ordinaryAttemptCount
  confirmationRemaining
  hybridRepairRemaining
  reservedDispatchMs
  committedAttemptId
  semanticCommitVersion
  version
}
```

规则：

- 不设一个会截断 RR/weighted 全环或 hybrid 合法 repair 的固定全局 N=6。
- 普通业务的唯一候选上限由 route plan 和同层一轮决定；confirmation、hybrid repair、recovery canary 使用独立目的预算，但共享请求救援墙钟。
- serverRescueDeadlineAt 只限制启动下一 attempt。新 ordinary、confirmation、hybrid auxiliary/repair 或跨组派发都必须满足 `now + reservedDispatchMs < serverRescueDeadlineAt`；首期 `reservedDispatchMs` 固定 5 秒。不得用该保留值硬中断已经取得 admission 的 attempt。
- routeCoordinationBudget 是普通模式和快速模式共同的累计控制面等待预算，初始建议 3 秒，覆盖故障后切号等待、重选号、层级切换和未进入 `activeGroupQueue` 的零可派发协调等待。
- 高并发分组的 admission FIFO 不是上述控制面等待，继续使用分组 `maxQueueWaitMs`；实际队列截止为 `min(group.maxQueueWaitMs, requestHardRemaining)`。队列响应客户端取消，出队后必须重验 circuit generation、dispatchRevision 和容量，不能叠加第二个 no-available 等待。
- 现有 ServerRetryBudget 只能作为该唯一账本的兼容实现；不能再与旧 270 秒 no-available 预算叠加。每请求只创建一次，不能跨组重置。
- 活跃 fetch、首字观察和响应读取不扣协调等待；等待前必须扣除协调成本并保留最小派发时间。
- 任何预算耗尽都只能由 coordinator 产生一次终态，不能继续进入低层等待或重复派发。

requestHardDeadlineAt 必须在请求开始时由单调时钟冻结为有限值，并满足 `serverRescueDeadlineAt <= requestHardDeadlineAt`。它统一控制活动 fetch、group queue、控制面等待和 lease 的 AbortSignal：

- 语义尚未提交时，到期原子取消所有活动分支、释放 reservation/permit/lease，并返回一次网关本地、协议兼容的 timeout 错误；该错误不依赖上游状态码，也不写账号失败。
- 语义已经提交时，到期只终止当前上游和下游响应，不再切号。
- 所有清理必须在有界 cleanup grace 内完成；重复 timer、取消和队列唤醒只能命中同一个 terminal CAS。

为使该合同可配置、可测试，新增请求级和 lane 级系统设置：

| 设置 | 默认值 | 允许范围 | 计算 |
| --- | --- | --- | --- |
| `textRequestMaxLifetimeSeconds` | 1800 | 60-86400 | `requestStart + value` |
| `imageRequestMaxLifetimeSeconds` | 3600 | 60-86400 | `requestStart + value` |
| `serverRescueWindowSeconds` | 60 | 10-120 | `routePlanCreatedAt + value`，普通/快速和所有 lane 共用 |

如果客户端提供了网关认可且更短的绝对 deadline，则取两者最小值。默认值与现有对应 lane 的 uncommitted attempt lifetime 对齐，但新设置是整个请求的最终 wall-clock 上限，不能被每次换账号、分组或 hybrid action 重置。

`serverRescueDeadlineAt` 在最终 request lane 和不可变 route plan 形成后、首次排队或派发前一次性计算：

```text
serverRescueDeadlineAt = min(
  requestHardDeadlineAt - cleanupGraceMs,
  routePlanCreatedAt + serverRescueWindowSeconds * 1000
)
```

首期 `cleanupGraceMs` 固定 1 秒，只用于 terminal CAS 和资源释放，不作为新的等待预算；`activeGroupQueueDeadlineAt` 不存在时在最小值计算中按正无穷处理。

普通模式、快速模式、fallback、RR/weighted、hybrid 和 confirmation 都不得重建或延长该截止。它不按剩余候选平均切片，也不设置固定账号尝试数；同层所有唯一候选只要能在时间准入内依次快速形成可重放终态就继续推进。截止到达时，已有 attempt 继续遵守自己的 lane hard/lifetime；它结束后不再切号。没有活动 attempt 且尚未提交语义时，coordinator 返回一次 `deadline_exhausted` 或更具体的 `indeterminate_upstream_outcome`。

协调等待的有效时长固定取以下剩余值的最小值，等待结束后必须重新走 admission CAS：

```text
min(
  coordinationRemainingMs,
  activeGroupQueueDeadlineAt - now,
  serverRescueDeadlineAt - now - reservedDispatchMs,
  requestHardDeadlineAt - now - cleanupGraceMs
)
```

### 7.5 Confirmation

confirmation 是一次真实上游 attempt，不是第二套重试循环：

- 首次作用域 transport 失败时，CAS 设置 SUSPECT 并只发放一个 matching-generation lease。
- 当前业务请求只有通过 7.6 重放授权时才可消费该 lease 执行一次 confirmation；其他请求立即换赛道。请求不可重放时不发送 confirmation，原子释放 lease、保持 SUSPECT，并登记 nextLeaseAt/due，由后续安全业务请求或后台有界 probe 重新竞争。
- confirmation 使用同一个首字事实和当前 lane 规则，不另设隐含 5 秒 timer。
- confirmation 在语义提交前失败或超时，作用域进入 OPEN；租约未知、客户端取消、进程退出或 lease 超时不计失败，但必须原子清空旧 lease、保留 SUSPECT 并写入 `nextLeaseAtMs` 和 due index，到期后恰好允许一个新 confirmation lease。
- confirmation 成功只能进入 RECOVERING，不能一次成功直接 CLOSED。

### 7.6 发网后的统一重放授权

请求准备阶段由协议适配器产生不可变 `requestReplayPolicy`，默认 `replay_forbidden`。每个 attempt 的本地网络边界只记录以下事实，不读取上游状态码或正文：

```text
networkWriteState = not_started | confirmed_not_sent | possibly_sent
replayDisposition = replay_forbidden | replay_safe_before_commit | idempotency_protected
```

在已经存在普通或特殊 attempt 后，coordinator 只有满足以下任一条件才能启动下一 attempt：

1. 前一个 attempt 被本地 transport 证明为 `confirmed_not_sent`；
2. 请求级 `committedAttemptId == null`，并且冻结的 replayDisposition 为 `replay_safe_before_commit`；
3. 适配器证明上游幂等键已透传且覆盖当前 endpoint、请求体和副作用范围，replayDisposition 为 `idempotency_protected`。

`possibly_sent + replay_forbidden` 固定禁止普通切号、speed-first handoff、confirmation、hybrid repair/upgrade 和跨组 failover。若原 attempt 仍活跃则继续等待它自己的 lane/lifetime；若 transport 已结束且结果未知，则只返回一次本地 `indeterminate_upstream_outcome`，不尝试用第二个账号掩盖，也不把完整上游响应解释为失败。

纯模型推理 endpoint 也不是天然可重放。只有适配器同时证明不存在 hosted tool、外部 action、background/store、资源写入或其他不可幂等副作用，且请求体处于有界重放预算内，才能返回 `replay_safe_before_commit`。所有未知协议、未知 endpoint 和无法证明的工具能力默认拒绝。

## 8. 运行态电路与完整生命周期

### 8.1 作用域

| 作用域 | 触发 | 影响 |
| --- | --- | --- |
| binding | 本地权限、额度、绑定配置失败 | 当前授权实例/分组 |
| key | 本地 Key 装配失败或用户显式 Key 规则 | 当前 Key 指纹 |
| protocol_model | transport、lane hard、读取中断、EOF | 物理资源的协议/通道/模型族 |
| account | 多个独立 protocol_model 作用域聚合，或用户显式账号规则 | 物理账号跨请求共享 |
| upstream_bucket | 多账号共享代理/Base URL 的 transport 故障 | 现有上游桶作用域 |
| latency_degraded | 快速路由慢样本状态机 | 当前路由策略范围，不是账号不可用 |

单个模型、单条协议或单个 Key 的故障不能自动污染其他健康能力。建议账号级升级至少要求同一物理资源在短窗口内有两个不同 protocol_model 作用域分别 confirmation 失败，并且满足最小独立失败次数；具体阈值通过回归和生产窗口校准，不得按并发数直接升级。

### 8.2 状态机

```text
CLOSED
  | 本地 transport failure / explicit policy
  v
SUSPECT -- confirmation success --> RECOVERING
   |                              |
   +-- confirmation fail --------> OPEN
   +-- unknown/cancel -----------> SUSPECT + nextLeaseAt

OPEN -- backoff due + lease --> HALF_OPEN
  |                               |
  +-- canary success ------------> RECOVERING

OPEN_UNKNOWN -- orphan horizon + lease --> HALF_OPEN
QUARANTINED -- quarantine due + audit lease --> HALF_OPEN

RECOVERING -- 3 independent canaries --> CLOSED
RECOVERING -- transport failure -------> OPEN
RECOVERING -- unknown/cancel ----------> RECOVERING + nextLeaseAt
```

| 状态 | 普通业务 | 特殊 lease | 说明 |
| --- | --- | --- | --- |
| CLOSED | 可派发 | 无 | 允许正常候选排序 |
| SUSPECT | 排除 | 一个 confirmation | 首次失败后的即时止损 |
| OPEN | 排除 | 到期后一个 half-open | 退避中 |
| OPEN_UNKNOWN | 排除 | 孤儿期限后一个 half-open | admission/终态无法确权，禁止按旧 CLOSED 放流 |
| HALF_OPEN | 排除 | 一个 recovery canary | 多节点单飞 |
| RECOVERING | 排除 | 一个 matching-generation canary | 连续三次稳定恢复 |
| QUARANTINED | 排除 | 到期后一个 audit canary | scope 级补偿/DLQ 失败，不影响其他 scope |

收到首字、heartbeat 或一次 framing 不完整的成功都不能关闭电路。只有完整 HTTP framing，或 SSE 在没有读取中断的情况下正常结束，才计入 canary 成功；判断不得读取状态码和业务正文。

### 8.3 Redis CAS 与租约

电路状态至少包含：

```text
state
failureScope
runtimeRecoveryEpoch
generation
leaseId
leaseOwnerNodeId
leaseOwnerBootId
leaseUntilMs
attemptHardDeadlineMs
openUntilMs
quarantineUntilMs
backoffLevel
consecutiveFailures
recoveringSuccesses
recoveryEpoch
expectedCanaryOrdinal
lastFailureClass
transitionId
nextLeaseAtMs
dispatchRevision
updatedAtMs
```

Redis Lua 或等价原子 Store 必须同时校验和更新：

```text
runtime phase/readiness + runtimeRecoveryEpoch
+ nodeId + bootId + node readiness/heartbeat
+ state + generation + leaseId + transitionId
+ expectedCanaryOrdinal + dispatchRevision
```

同一 generation 只能有一个 confirmation/half-open/recovery lease。leaseId 必须全局唯一且结果只能消费一次；每次重新进入 OPEN 或 RECOVERING 都递增 transitionId/recoveryEpoch，串行 canary 还必须匹配 expectedCanaryOrdinal，旧回调不能污染新恢复轮次。全局 runtimeRecoveryEpoch、节点 bootId 或节点 membership lease 不匹配时，旧 admission、旧 lease 和旧终态只能返回 STALE，不能推进状态。

发放任何 confirmation/half-open/recovery lease 时，必须在同一个 Lua/CAS 中把 `scopeKey + runtimeRecoveryEpoch + generation + transitionId + leaseId + leaseUntilMs` 写入 due ZSET。所有存活节点都可运行共享 lease reaper：用 Redis TIME 领取短期 reaper lease，重新校验完整 fencing 后清理自然过期或持有进程崩溃留下的 lease，并写入下一次可竞争时间。reaper 自身崩溃后由其他节点在其 lease 到期后接管；候选 admission 路径也必须对已过期 lease 做同一套机会式修复。

SUSPECT、HALF_OPEN、RECOVERING、OPEN_UNKNOWN 和 QUARANTINED 都禁止出现“没有有效 lease、没有 nextLeaseAtMs、due index 也没有成员”的永久阻断态。取消、未知、进程崩溃和 lease 过期必须通过相同 CAS 清理旧 lease、写下次可竞争时间并维护 due index；到期时仍只有一个节点能取得新 lease。scope 状态 TTL 必须晚于 `max(nextLeaseAtMs, leaseUntilMs, openUntilMs, quarantineUntilMs) + safetyMargin`；状态丢失但 due/tombstone 仍存在时重建为 `OPEN_UNKNOWN`，不能把 Redis key 自然消失解释为 CLOSED。配置、凭据、代理、协议档案或模型映射变化要递增 dispatchRevision 和 generation，旧结果只能释放资源，不能提交新状态。

### 8.4 探活与恢复

- 后台探活使用有界、成本可控的真实协议请求，只判断 framing/传输是否完成，不读取上游业务语义。
- runtime_probe 和 recovery_canary 不计入业务热质量，也不推进普通探索令牌。
- RECOVERING 的三个 canary 必须是不同 lease、跨时间串行完成，不能在一个请求里连发三次。
- scoped hot recovery 与 persistent Verifying 命中同一 physical scope、healthCheckModel、endpoint mode 和 configRevision 时，共用同一个真实 canary；一次终态可分别按各自 generation 幂等投影，不能为两个状态机重复发网。persistent Verifying 的更强间隔和稳定窗口同时满足 scoped recovery，不再追加第二组三次 canary。
- 任一 canary transport 失败立即回到 OPEN 并增加退避。
- 客户端取消、没有真实 attempt、旧 generation 或未知结果不计成功或失败；当前 generation 的未知结果必须按 8.3 的规则写 nextLeaseAtMs/due index，旧 generation 只能幂等释放自己的资源。

### 8.5 三平面可用性

```text
configuredIntent
  ∩ bindingAndAuthorization
  ∩ sourceResourceAvailability
  ∩ hotCircuitAvailability
  ∩ capacity
  = effectiveAvailability
```

- 配置意图来自业务库，包含启用、优先级、备用、授权和用户策略。
- 热运行态来自 Redis/进程内存，包含电路、租约、质量和探索令牌。
- 持久异常由人工、后台健康检查或冷却复测推进。

自动 CLOSED 不能覆盖用户显式 TTL 或持久禁用。performance 的账户列表必须批量叠加 Redis runtime overlay，展示状态、原因、generation 和新鲜度；不能只显示持久 schedulable。

### 8.6 持久账户状态与热电路分层

切号、电路和账户持久状态不是同一层。目标模型固定为四个彼此独立的事实面：

| 事实面 | 事实源 | 职责 | 禁止事项 |
| --- | --- | --- | --- |
| 配置意图 | 业务库 `schedulable`、时间计划、授权、用户策略 | 表示用户是否希望账号在健康时参与调度 | 自动 transport 失败不得改写用户意图 |
| 持久健康状态 | 业务库 `active / pending_test / temporary_unavailable / rate_limited / error / disabled` | 跨进程重启表达整账号的长期资格 | 单个请求、单个模型或单个并发窗口不得直接升级 |
| 热运行态 | Redis / standalone memory 电路、lease、quality、warmup | 秒级止损、分钟级恢复和受控放量 | 不替代路由策略，不回写优先级或备用配置 |
| 有效可用性 | 前三层与容量的实时交集 | 网关过滤和页面展示的唯一读模型 | 不把 `accounts.status = active` 直接等同于可派发 |

现有 `accounts.schedulable` 在目标合同中只表达配置意图。自动进入 `temporary_unavailable` 时保留该意图，不把它永久改为 false；恢复时只有原意图仍开启、授权/时间计划仍有效，才允许重新参与调度。`active + schedulable=false` 表示用户主动暂停，`temporary_unavailable + schedulable=true` 表示用户仍希望使用但系统正在恢复。实现迁移若暂时无法直接复用该字段，必须新增等价的持久意图字段，不能靠错误状态前的内存值猜测。

持久状态边界固定如下：

| 持久状态 | 普通派发 | 进入条件 | 自动出口 |
| --- | --- | --- | --- |
| `active` | 受热电路和容量过滤后可派发 | 激活/恢复验证完成，配置意图允许 | scoped transport 故障先进入热电路，不直接改持久态 |
| `pending_test` | 禁止 | 新账户、关键连接配置变化、error 人工恢复 | 有界后台激活验证成功后 active；本地确定性配置错误可进入 error |
| `temporary_unavailable` | 禁止，只允许恢复 lease | 整个物理账号在跨时间、跨能力聚合后被证明无可派发路径 | 三次恢复验证和最小稳定窗口通过后 active；失败按 fast/slow/long_term 退避 |
| `rate_limited` | 禁止，只允许策略允许的复测 | 仅用户显式高级规则或人工动作 | 按显式 TTL/恢复规则；系统不得从上游 429 自动建立 |
| `error` | 禁止 | 本地确定性配置损坏、用户显式规则，或用户关闭持续探活后有界最终探针失败 | 修复配置/人工异常恢复后只进入 pending_test，不直接 active |
| `disabled` | 禁止，默认不自动探活 | 用户停用、时间计划或本地到期事实 | 仅用户/时间计划重新启用；任何自动成功不得覆盖 |

上游完整 HTTP 响应无论 2xx、401、429 还是 5xx，在没有用户显式规则时都只是 transport completed：不能自动进入 `rate_limited`、`temporary_unavailable` 或 `error`。后台健康检查、激活检查、冷却复测和 canary 同样遵守这一边界；只有本地可验证 transport/framing 事实参与自动状态机。

现有 `last_health_check_status_code`、`last_error_code` 等字段如因兼容性继续保存，只能作为原始诊断展示和审计，不得进入候选过滤、promotion、退避、恢复或质量比较；实施时要补反向回归，确保旧 `http_*` / `statusCode` 分支不再偷偷改变账号生命周期。

### 8.7 从 scoped 故障到整账号临时不可用

首次 transport failure 只打开对应 `protocolModelKey` 的 SUSPECT/OPEN，不直接判死物理账号。创建整账号观察 epoch 时至少保存：

```text
physicalResourceKey
accountHealthGeneration
configRevision
observationStartedAtMs
lastCompletedTransportAtMs
independentFailedScopeKeys
independentFailureRounds
independentSuccessRounds
lastFailureRoundAtMs
sourceBucketGeneration
nextEvaluationAtMs
```

从 active 自动升级为持久 `temporary_unavailable` 必须同时满足：

1. 观察持续至少 5 分钟，且存在至少 3 个独立后台失败轮次，轮次间隔至少 2 分钟；同一并发风暴无论有 2 个还是 200 个请求都只折叠为一个观察轮次。
2. 每轮都形成了真实 upstream transport attempt，并以连接失败、lane hard、读取中断、EOF 或 framing 不完整结束；任务取消、探针执行器失败、Redis 失联和未知结果不计失败。
3. 以当前 configRevision、模型映射和 Key 池重新计算后，所有当前可配置派发路径都被 scoped circuit 阻断；单模型、单协议或单 Key 故障只保持局部 OPEN。
4. 账户只有一个可派发能力时，必须由标准账户检查 probe 和该唯一 scope 的独立 confirmation 共同证明；有多个能力时，至少两个不同 protocol-model scope 提供独立证据，且标准检查 probe 同样失败。
5. 当前观察代次尚未完成 matching-generation 的三次独立成功验证。普通业务或旧 in-flight 的单次完整 success 只记录为弱证据，并把下一次 promotion evaluation 至多推迟一个最小轮次间隔；它不能清理 scoped circuit、重置失败轮次或取消观察 epoch。只有三次串行成功 canary 和稳定窗口才能证明恢复，避免“100 次失败、偶尔成功 2 次”的账号长期卡在可调度状态。
6. 当前物理账号没有未关闭普通 admission；最终写库使用 accountHealthGeneration + configRevision + observationStartedAt 的 CAS，迟到结果不得覆盖配置修改、人工停用或新观察代次。
7. 失败未被更高层 upstream bucket 相关性吸收。相同 proxy/baseUrl/provider bucket 在短窗口出现多账号 transport 故障时，先打开 bucket circuit 并暂停账号持久升级，不能把一次供应商网络波动批量写成几十个死号。

任一条件不满足时只保留 scoped 热电路和下一次 due，不写持久状态。scoped circuit 在 5 分钟内完成三次恢复 canary 时取消整账号观察 epoch；因此短暂 10 秒、1 分钟或 2 分钟波动会被快速切号隔离，但不会制造大量 `temporary_unavailable`，而零星成功也不会把故障账号错误复活。

授权实例不各自重复升级物理账号：transport 观察和持久健康归属 `physicalResourceKey` 对应的来源账号，所有授权实例只投影 `source_runtime_blocked` 或来源持久状态。binding 权限、额度、显式策略和本地状态仍独立。多 Key 账号只要还有一个 Key 对当前能力硬可用，就不能因其他 Key 故障升级整账号。

### 8.8 临时、长期不可用与恢复闭环

持久 `temporary_unavailable` 不是死亡终态，而是带恢复代次的自动恢复状态。推荐持久保存 `recoveryGeneration + recoveryStage + observationStartedAt + nextProbeAt + failureCount`，并使用同一 physical scope 的唯一 recovery lease：

```mermaid
stateDiagram-v2
    [*] --> Active
    Active --> ScopedOpen: "transport failure"
    ScopedOpen --> Active: "scoped canaries recovered"
    ScopedOpen --> TemporaryUnavailable: "5m + 3 independent rounds + full-account coverage"
    TemporaryUnavailable --> Verifying: "one complete recovery probe"
    Verifying --> Active: "3 independent canaries + stability window"
    Verifying --> TemporaryUnavailable: "transport failure / unknown rescheduled"
    TemporaryUnavailable --> LongTermRecovery: "continuous probe enabled and long threshold reached"
    LongTermRecovery --> Verifying: "complete recovery probe"
    LongTermRecovery --> LongTermRecovery: "bounded low-frequency failure"
    TemporaryUnavailable --> Error: "explicit bounded-recovery opt-out final failure"
    Error --> PendingTest: "configuration repair / manual recovery"
    PendingTest --> Active: "activation verification"
    Active --> Disabled: "manual / schedule / local expiry"
    Disabled --> PendingTest: "manual or schedule re-enable"
```

恢复节奏建议：

- fast：3s -> 5s -> 10s -> 30s -> 60s；
- slow：之后按有界指数退避，最大间隔首期建议 5 分钟，足以覆盖“坏 2 小时后突然恢复”的上游；
- long_term：连续不可用 12 小时后降为每 1 小时一次；开启持续恢复探活时无限期保留低频恢复，不再仅因持续 7 天自动转 error；
- bounded：用户显式关闭 `temporaryUnavailableContinuousProbeEnabled` 时，保留现有 10 分钟有界窗口和最后一次真实 probe；最终失败进入 error 是用户明确选择停止长期探活的结果，不是内部解析上游状态码所得结论。

`long_term` 是 `temporary_unavailable` 的持久恢复阶段，不新增一个会扩散到所有 SQL、API 和前端枚举的“长期不可调度”主状态；页面通过 `recoveryStage=long_term` 展示“长期恢复中”，网关仍按同一持久阻断和 recovery lease 处理。这样既能表达长期不可用，又不会把一个可能在两小时后恢复的上游账号写成不可逆死号。

第一次完整恢复 probe 不能直接把账号恢复为 active。它只进入 Verifying/RECOVERING，并要求 3 个不同 lease 的完整 transport canary、至少间隔 10 秒、总稳定观察不少于 30 秒。任一失败回到 temporary_unavailable 并增加退避；未知/取消只重排 nextProbeAt，不增加失败。

恢复 CAS 成功后才把持久状态改为 active，并建立 `warmupStage`：前 30 秒最多 1 个 ordinary admission，随后 60 秒最多使用账号配置并发的 25%，之后恢复全量。warmup 中发生 transport failure 立即回到 scoped OPEN 并重启当前 accountHealthGeneration；不得因为账号刚恢复就让 20 个会话同时灌入。

持久恢复成功同时递增 `qualityEpoch`。旧 epoch 的 5/10/30 分钟失败桶继续保留诊断，但不再参与新 epoch 排序；新 epoch 从无样本中性状态开始，不赠送成功分。只有 warmup 中的真实业务 attempt 建立新质量，scoped 短故障恢复则不重置 qualityEpoch，避免波动账号靠频繁开关电路洗掉最近失败。

人工“恢复正常”也不能直接打开普通流量，只能提升恢复任务优先级或把 error 送入 pending_test。关键连接配置、凭据、代理、协议档案或模型映射变化必须递增 configRevision/generation，取消旧 probe/lease；旧结果只能清理自己的资源。

### 8.9 恢复容量、全池保护与公平性

业务容量耗尽不能阻断恢复。系统保留独立但有界的 probe admission lane：

- 同一 physicalResourceKey 同时最多 1 个 confirmation/recovery/health probe；同一 protocol-model generation 最多 1 个 lease。
- 同一分组默认最多 1 个恢复 probe；同一进程/worker 共享现有全局完整诊断上限，首期最多 3 路。
- provider/proxy/baseUrl bucket 维护独立有界并发和 jitter；bucket OPEN 时先运行 bucket canary，禁止每个账号各发一轮探针造成恢复风暴。
- due 队列使用 `(nextProbeAt, recoveryStage, lastSuccessfulBusinessAt, stableAccountId)` 的 keyset 公平扫描，并为长期恢复保留轮转份额；高优先账号可较早恢复，但不能让后排账号永久拿不到 probe。
- 探针不占普通 route cursor、不进入业务质量、不生成客户端 usage；但必须占真实物理并发，避免恢复流量绕过账号容量。

所有自动探针入口必须先写统一 `ProbeIntent`，再由一个 coalescer 决定真实派发：

```text
physicalResourceKey
protocolProfile / endpointMode / healthCheckModel
configRevision
purposeSet = runtime_canary | persistent_retest | scheduled_health | activation
requiredGenerationByPurpose
earliestDueAt / latestStartAt
```

只有检查配置和副作用边界完全相同的 intent 才能合并；派发前为每个 purpose 预留 matching generation，终态逐个 CAS 投影。不能合并时按 activation > recovery canary > persistent retest > scheduled health 的优先级串行执行，低优先 intent 重新入 due 且保留原始等待年龄。授权实例、周期健康检查、cooldown worker 和 server due sweep 都不得绕过 coalescer 直接执行同一 physical probe。

当某分组所有账号都处于 scoped OPEN 或 persistent unavailable 时，路由 coordinator 先按冻结路由计划进入后备/下一层；仍无候选时只允许 matching-generation 的一个 half-open/recovery lease，其他请求在统一预算内等待或返回本地无可用错误。绝不为了“保可用”把全部坏账号 fail-open，也不因为当前业务请求失败而把所有账号批量升级。

### 8.10 与现有状态流转的兼容性裁决

现行机制具备可复用的 DB 状态、cooldown retest、generation guard、due 扫描和页面投影，但不能原样叠加。迁移裁决如下：

| 现行机制 | 裁决 | 目标关系 |
| --- | --- | --- |
| 普通请求只投递 `recovery_wait`，失败期间继续可调度 | 替换调度语义 | 请求可原子建立 scoped SUSPECT 即时止损，但不能直接改持久账号状态 |
| `precheck_pending / precheck_failed` | 合并 | 迁移到 SUSPECT/OPEN/HALF_OPEN/RECOVERING；不得与新电路双重阻断、双重 lease |
| 5 分钟、3 个独立轮次、2 分钟间隔 | 保留并上移 | 只作为整账号持久 temporary_unavailable 的升级门槛，不承担秒级止损 |
| 后台成功直接清理运行态 | 收紧 | 必须匹配 epoch/generation/lease，并走三 canary；普通/周期成功不能越权清理 |
| `temporary_unavailable` 冷却复测 | 复用并增强 | 接入 physical singleflight、fast/slow/long_term、Verifying 和 warmup |
| 持续 7 天 transport 失败自动 `error` | 删除默认路径 | 持续探活开启时长期低频恢复；只有本地确定性错误或显式用户策略进入 error |
| 完整非成功 HTTP 作为后台失败 | 删除 | 完整 framing 是 transport success；状态码/正文只由用户显式高级规则解释 |
| 多 Web 节点可重复运行同一 probe | 替换 | Redis/memory Store 使用 generation lease 和 due reaper，物理 scope 单飞 |
| `schedulable` 随自动健康状态反复改写 | 分离 | 固定为用户配置意图；effectiveAvailability 承担实际派发判断 |

迁移期间每种状态转换只能有一个 owner。先以 shadow event 对比新旧结果，再启用 scoped circuit admission；启用后必须同时关闭旧 precheck 调度 gate 和旧 probe 写入。随后切换整账号 promotion owner，最后切换 cooldown recovery owner。禁止长期双写后“谁更严格听谁”，否则同一账号会被两套 generation、两套探活和两套恢复互相卡死。

## 9. 热质量与受控同层探索

### 9.1 热数据存储

- performance：Redis 是跨节点热状态事实源。
- standalone：进程内存保存等价状态，固定最大条目数和 TTL 清理。
- Redis state 不可用时，当前节点的 performance `nodeRuntimeReadiness` 固定 fail-closed：禁止所有新 ordinary dispatch、confirmation、half-open、recovery canary 和探索，不得回退本机内存；客户端收到一次明确的网关运行态基础设施错误。仍连通 Redis 的其他节点不能只依赖本机缓存放流，必须继续执行本节的 membership 和 admission registry 校验。
- Redis 维护全局 `runtimeRecoveryEpoch + phase(BOOTSTRAPPING|READY) + revision`。只有 Redis 运行态数据丢失、schema 不兼容或管理员显式全量重建时，才能 CAS 提升 epoch 并进入 BOOTSTRAPPING；普通节点重连、进程重启或 membership 过期不得提升全局 epoch。每次 admission/lease 都实时校验全局 READY 和 epoch，不能使用启动时无限期缓存。
- 每个节点注册 `nodeId + bootId + runtimeRecoveryEpoch + readiness + admissionEnabled + heartbeatUntilRedisMs`。节点启动或重连必须生成新 bootId、清空旧本地运行态并重新注册；只有当前 bootId 为 READY、admissionEnabled=true 且 heartbeat 未过期时，Redis 才签发 token/lease。历史或已过期节点不计入恢复确认，也不能永久阻塞健康节点。
- Redis 在 admission 后、终态 CAS 前失联时，已经在途的响应可以按下游边界收尾，但运行态结论记为 unknown；以同 attemptId 写入不含响应正文和凭据的本地耐久 terminal outbox，重投仍受 terminal dedup 约束，不能伪造 CLOSED/OPEN。该节点立即停止新 admission，旧 bootId 的迟到 outcome 只能按 fencing 返回 STALE。
- admission registry 的存活时间必须覆盖 request hard deadline 和 cleanup grace。其他节点准备向同一 protocol-model scope 派发时，若发现未关闭 registry 的 owner bootId 已过 heartbeat 租约，必须先原子把该 scope 转为 `OPEN_UNKNOWN`/隔离态并递增 generation，再决定是否发放受控 canary；不能继续按旧 CLOSED 放流。心跳 TTL 形成一个明确、有界的故障感知窗口，首期建议 3 秒续约、10 秒过期。
- outbox 写入失败、达到容量上限或检测到上次进程非正常退出时，只允许该节点或可识别的受影响 scope 保持 fail-closed。重连后节点用新 bootId 重放 outbox、修复 admission registry/circuit/due 索引并等待旧 permit 的最大有效期；无法证明终态的 scope 进入 `QUARANTINED`/受控审计 canary。
- 连续重投失败的可识别 scope 进入带 generation、退避、`quarantineUntil` 和 `nextAuditAt` 的 scope 级 DLQ；DLQ 不关闭全局 READY，也不阻塞其他 scope。到期后只允许一个审计 canary；管理员手动重试或清理也必须创建新 generation，不能删除状态造成 fail-open。只有无法识别任何受影响 scope 的损坏记录才保持全局 BOOTSTRAPPING，并要求显式人工重新确权。
- 全局重建完成时，只等待当前 live-membership 集合确认当前 epoch，并确认未确权 scope 已被隔离；不等待历史下线节点，也不要求 scope DLQ 清空。随后按 `runtimeRecoveryEpoch + revision` CAS 打开 READY。任何 admission、lease 或 terminal outcome 都必须再次校验该 epoch/readiness，因此旧节点和旧 CLOSED 快照无法越过恢复栅栏。
- 业务数据库质量表继续用于展示和历史分析，不作为实时调度依赖。

### 9.2 质量维度与窗口

热质量 key：

```text
physicalResourceKey
+ protocolProfile
+ requestLane
+ mappedModelFamily
+ qualityEpoch
```

保存最近 30 个一分钟桶，派生最近 5、10、30 分钟窗口。桶只保存有界计数和首字分布：

```text
businessAttempts
completedTransports
localTransportFailures
timeouts
readInterruptions
incompleteResponses
firstByteHistogram
unknownOutcomes
clientCancellations
lastCompletedAtMs
```

每个 attemptId 先以 gateway-attempt-terminal:v1:{attemptId} 原子落唯一终态，再由质量、电路和 usage 投影幂等更新。不得用 GET -> 修改 -> SET 计数。质量不保存上游状态码、正文、供应商文案或无限模型字符串。

质量排序建议使用“可靠性置信度优先、最近窗口速度其次、样本新鲜度再次”的稳定比较器：

- unknown 不获得负分，也不因为没有样本永久排到末尾。
- 已知 transport 不稳定的账号由电路过滤，不依赖质量分慢慢淘汰。
- 探针、canary、容量拒绝和客户端取消不进入业务质量成功/失败样本。
- business_primary 与 business_explore 都进入业务热质量，但必须分别计数以便审计。

### 9.3 同层探索

同层探索是“同一有效层内替换首选”，不是新增请求：

1. 候选必须位于同一 routeOverrideBand 和同一 baseTierKey。
2. 候选必须通过 binding、能力、容量、电路、Key 和有效 affinity 校验。
3. 已知 OPEN / SUSPECT / HALF_OPEN / RECOVERING 或明确不健康账号不得被探索。
4. 有效 session affinity 默认关闭探索；若将来允许突破 affinity，必须增加显式上限和过期规则。
5. 使用两级热 token：跨请求共享桶 key 为 `systemAccountId + routeStrategyId + groupId + lane + modelFamily + baseTierKey + policyRevision`，请求内幂等 key 才使用 routePlanId；每个共享桶最多一个在途探索，每请求最多一次。
6. 首轮低频标准同时满足“最近 100 个 eligible 请求最多 1 次”和“最近 60 秒最多 1 次”。共享状态保存 eligibleCount、windowStartedAtMs、nextEligibleAtMs、policyRevision 和 `inFlightLease { leaseId, holderAttemptId, runtimeRecoveryEpoch, leaseUntilMs }`。
7. 探索 reservation 必须以唯一 leaseId 原子取得，并写入 due index。取得 dispatchAdmissionToken 后，CAS 把 reservation 转为占用到 nextEligibleAtMs 的 committed cooldown 并清空 inFlightLease；真实 attempt 快速完成也不能提前清除 cooldown。若节点在发网前崩溃，reaper 只按 matching leaseId + epoch + policyRevision 回收；旧节点迟到释放不能清掉新 lease。只有取消、资格失效或确认未发网时才允许 matching CAS 回滚本次 reservation。
8. 探索沿用正常业务 attempt，不发送额外探针、不改变账户优先级、不写持久状态、不跨 P2/备用。
9. 首期不新增用户配置项，默认只在完全相同层内低频启用；后续如需关闭，配置属于路由/分组策略，不属于账号。

探索成功计入业务热质量，但不能推进 RECOVERING；探索失败按真实 transport 作用域处理，不能因“探索”扩大电路范围。

## 10. 响应语义、流式与客户端边界

### 10.1 自动机制

未命中用户显式高级规则时：

- 完整 HTTP 响应无论状态码、响应头和正文是什么，都视为 transport completed 并透明转发。
- 完整响应不能自动重试、切号、轮换 Key、写账号电路或写上游桶失败。
- 连接失败、lane hard timeout、读取中断、EOF 或未形成完整 framing，才进入对应 transport 作用域。
- generic_* 客户端不能被服务端自动解释响应语义；协议渲染和客户端画像不能成为自动切号授权。

用户显式选择的 hybrid 路由可以按其冻结的 `hybridActionPlan` 执行质量评分、repair 或 upgrade，这是路由策略动作，不是系统自动账号故障判断。它只能在请求级 `committedAttemptId == null`、通过 7.6 的统一重放授权且有界检查完成时执行；完整响应无论评分结果如何都不能写 transport 电路、Key 故障或账号状态。

### 10.2 用户显式规则

只有账户所有者或具备该账户/授权实例策略管理权限的管理员，在前端显式配置的错误策略或响应检查策略，才能读取状态码、响应头、正文、JSON 路径、SSE 事件或完成状态。规则动作标记为 source=explicit_policy，并独立计入策略电路/持久 TTL；自动 transport 成功不能提前清理用户 TTL。

### 10.3 语义提交

- 只有请求级 `RequestRescueLedger.committedAttemptId == null`、server rescue admission 仍开放且通过 7.6 重放授权时，才可在冻结路由计划范围内切号；不存在 attempt-local 的第二份 semanticCommitted 状态。
- SSE 已写出可见语义内容后，禁止透明拼接第二账号、重复工具调用或重放请求。
- SSE heartbeat、响应头、空事件和内部缓冲不算语义提交。
- 非流式实现可在边界安全的前提下缓冲有限首段，但不能要求无界完整 body 缓冲来换号。
- 客户端取消只释放并发和 lease，不把账号标记为失败；confirmation/half-open 取消结论为未知，并按第 8.3 节写入下一次 lease 的 due index。

## 11. 与现有机制的关系

| 现有机制 | 目标关系 |
| --- | --- |
| 显式账户错误策略 | 保留，优先级高于自动热质量；可执行 retry_next / cooldown / disable |
| 账户内多 Key | 本地 Key 准备失败或显式 Key 动作才可换 Key；完整响应不能自动轮 Key |
| IP 级账号回避 | 保留为窄作用域辅助，不承担共享账号故障保护 |
| 上游桶健康 | 继续识别代理/Base URL/provider 公共故障，不替代单账号电路 |
| recovery_wait / precheck_pending | 迁移为后台持久确认层；新短电路先承担秒级止损，recovery_wait 不能继续允许无限新请求 |
| 持久冷却复测 | 复用现有队列、退避和 CAS 基础；按 8.8-8.10 收敛为物理账号单飞、三 canary、渐进放量和持续低频恢复，不进入业务热质量 |
| 历史质量统计 | 保留展示和分析，不参与实时候选读取 |
| latency_degraded | 保持路由层状态，只影响启用该快速目标的范围 |
| system_default 响应规则 | 保留协议渲染能力，但不得产生未授权的账户调度副作用 |
| 高并发分组 FIFO | 保留分组 `maxQueueWaitMs` 和 group reducer；只受请求 hard deadline 限制，不并入 3 秒控制面等待 |
| 授权实例运行态 | binding/额度/持久状态继续隔离；相同物理 protocol-model 的 transport 电路按来源资源共享，并投影 `source_runtime_blocked` |

### 11.1 必须迁移的现行冲突

当前 failure-dispatch、upstream-dispatch、流式结束重试决策和默认响应检查仍可能依据完整非 2xx、错误类型或正文自动重试、切号、轮 Key、写上游桶或写运行态。实施时必须：

- 停用未获用户显式授权的响应语义副作用；
- 收敛 routes、preparation、candidate-filter、dispatch 的直接 fallback；
- 用 route-coordinator 统一 attempted、预算、游标和跨组推进；
- 在 route plan 冻结协议适配器的 requestReplayPolicy，让 ordinary failover、speed handoff、confirmation 和 hybrid 共用发网后重放门禁；
- 将旧 same-account retry budget 改为 confirmation lease 一次例外；
- 更新“recovery_wait 仍可调度”的旧测试契约，使首次作用域 transport 故障可以即时阻断新流量；
- 补齐 Redis runtime overlay，避免网关与管理页面对可调度状态产生分歧。
- 为跨授权物理 transport 去重补迁移开关、`duplicate_physical_target` 审计和 binding 独立授权回归；不把来源电路回写为授权实例持久状态。
- 将高并发 group reducer 的动态队列/容量判断接入 coordinator，但保留 `maxQueueWaitMs` 作为组级队列上限；3 秒只控制重选号、层切换和未进入 `activeGroupQueue` 的零可派发协调等待。
- 为 performance 增加 runtime epoch/readiness、节点 membership、admission registry、lease/due reaper、terminal outbox 和 scope 级 DLQ；所有 admission/lease CAS 都必须带 fencing。
- 在 settings repository、schema defaults、管理 API 和验证脚本中落地 request hard deadline 与 60 秒 server rescue window，不能只在运行时代码写隐藏常量。
- 将 `precheck_pending / precheck_failed / recovery_wait` 的调度 owner 原子迁移到 scoped circuit；新 gate 开启时同步关闭旧 gate、旧 lease 和旧 probe 写入，禁止双写。
- 将 5 分钟、3 个独立失败轮次保留为整物理账号 promotion 门槛，并增加全能力覆盖、标准检查 probe、成功证据、上游桶相关性、在途归零和 accountHealthGeneration CAS。
- 将所有自动 `mark_*DisabledByFailure` / `mark_*TemporaryUnavailable` 调用收敛到 scoped circuit 与 promotion owner；只有本地确定性错误或用户显式策略可以调用持久 error/disable 写路径。
- 复用 cooldown retest 队列但删除持续探活开启时“纯 transport 失败满 7 天自动 error”；增加 long_term 低频恢复、Verifying 三 canary 和 30/90 秒渐进放量。
- 保持 `schedulable` 为配置意图；页面和网关统一读取持久状态 + runtime overlay + warmup + capacity 的 effectiveAvailability。

## 12. 日志、指标与可观测性

建议事件：

```text
gateway_route_plan_created
gateway_attempt_started
gateway_attempt_terminal
gateway_account_circuit_suspected
gateway_account_confirmation_lease_acquired
gateway_account_confirmation_result
gateway_account_circuit_opened
gateway_account_half_open_lease_acquired
gateway_account_circuit_recovering
gateway_account_circuit_closed
gateway_account_circuit_dispatch_skipped
gateway_priority_tier_escape
gateway_same_tier_exploration
gateway_window_incomplete
```

关键指标：

```text
account_circuit_open_total
account_confirmation_inflight
account_half_open_inflight
account_circuit_cas_conflict_total
account_circuit_lease_contention_total
account_circuit_stale_result_total
hot_quality_terminal_dedup_total
gateway_accounts_attempted_per_request
gateway_precommit_switch_duration_ms
gateway_request_rescued_by_account_switch_total
gateway_same_tier_exploration_total
gateway_window_incomplete_total
gateway_coordination_budget_exhausted_total
gateway_semantic_commit_conflict_total
gateway_dispatch_admission_rejected_total
gateway_runtime_terminal_outbox_pending
gateway_runtime_recovery_epoch_total
gateway_runtime_orphan_admission_total
gateway_runtime_scope_quarantined_total
gateway_replay_forbidden_total
gateway_attempt_superseded_total
gateway_exploration_lease_reaped_total
account_health_promotion_suppressed_total
account_temporary_unavailable_total
account_recovery_stage_transition_total
account_recovery_probe_lease_conflict_total
account_recovery_warmup_rejected_total
upstream_bucket_correlated_failure_total
```

日志只在状态转换和请求终态输出结构化摘要。被电路排除的请求不伪造上游失败 usage，也不重复打印同一上游错误正文；摘要只记录候选身份、作用域、原因码、generation、游标和预算，不记录上游响应正文。

## 13. 验收矩阵

实施必须覆盖以下场景，普通模式和快速模式都要分别验证：

1. 20 个同 IP、多会话同时命中同一坏账号：已在途请求允许结束；首次状态转换后只允许一个 confirmation，后续新请求全部换赛道。
2. 不同 IP、不同 API Key 命中同一物理资源：按物理作用域共享电路，不依赖 IP 回避阈值。
3. 5 个 P1 中前 4 个在 `serverRescueDeadlineAt - reservedDispatchMs` 前快速失败、第 5 个健康：必须给第 5 个一次 admission，不得因固定失败预算提前进入 P2；时间准入不再满足时只能返回 deadline_exhausted。
4. 5 个 P1 全部快速失败、P2 健康且仍满足时间准入：P1 只扫描一轮，随后按路由计划进入 P2，不重复扫描 P1。
5. P1 长期健康：P2 和备用不产生探索流量。
6. 同层后排账号无样本：只在同层 token 允许时被真实业务探索，不跨优先级、不消耗备用资源。
7. 一个账号 10 分钟后持续 transport timeout：第一次作用域失败立即 SUSPECT，新请求不再灌入，不等待质量窗口。
8. 偶尔成功两次后再次失败：保持 RECOVERING 语义，三次独立 canary 前不能关闭。
9. 两个 Redis server 同时抢 confirmation/half-open：同一 generation 只有一个 lease 成功。
10. 单 Key 本地装配失败且另一个 Key 可用：只切 Key，不打开账号电路；完整响应不能自动轮 Key。
11. 通用客户端收到完整 401/429/5xx：透明转发，不自动切号、不写自动电路。
12. cost-first 合法慢请求：soft checkpoint 只记录样本，不切号、不误写账号故障；只有 transport 失败才遵守 lane hard 后切换。
13. speed-first 已确认慢的超级优先账号：未降级候选可按路由覆盖越过；热质量和 affinity 不得拉回慢账号。
14. speed-first 单次慢但未达到阈值：只记慢样本，不强制切号。
15. SSE 首字前 transport 失败只有在 `confirmed_not_sent` 或 requestReplayPolicy 允许时可以切号；首字后读取中断绝不拼接第二账号。
16. 客户端取消 confirmation/half-open：只释放 lease，不累计失败；同一 CAS 写入 nextLeaseAtMs/due index，后续恰好允许一个新 lease。
17. 同一物理账号通过主用和备用授权实例出现：同请求不重复发普通 protocol-model attempt。
18. 256 窗口或扫描窗口截断：返回 window_incomplete，不能 hard_exhausted。
19. hybrid repair/upgrade：使用独立 purpose budget，但不重建 route plan、不清空 attempted、不绕过语义提交或统一重放边界。
20. 高并发队列等待：使用 `min(group.maxQueueWaitMs, requestHardRemaining)`，不扣 3 秒控制面账本；出队后重新校验 revision/circuit/capacity。
21. Redis 不可用：失联节点禁止新 dispatch、confirmation、half-open、canary 和探索，不回退本机内存；仍连通节点按 membership/admission registry 把失联节点的未决 scope 转为 OPEN_UNKNOWN，不按旧 CLOSED 放流。
22. 同一 attempt 终态重复提交：质量桶和电路只投影一次，CAS 冲突可观测。
23. 配置 revision 在 confirmation 期间变化：旧结果不能污染新 generation。
24. performance 账户列表：显示 Redis runtime overlay 的状态、原因和新鲜度，与网关候选一致。
25. 四个 P1 悬挂时：救援期限只禁止启动新 attempt 或触发有替代的 speed handoff，不缩短已启动 lane hard；若仍有期限，P1 #5 获得 admission。
26. 所有候选悬挂或阻断：requestHardDeadline 到期在有界 cleanup grace 内 abort queue/fetch/lease，并只返回一次网关本地 timeout。
27. SSE 语义事件入队与切号同时发生：`trySemanticCommit` 与 write/abort 互斥，提交后绝不换号。
28. 旧 canary 回调在新 RECOVERING epoch 到达：transitionId、recoveryEpoch、leaseId 和 expectedCanaryOrdinal CAS 拒绝旧结果。
29. 两个救援 attempt 同时产生首个语义事件：请求级 committedAttemptId 只能绑定一个 winner，另一个永远不能写下游。
30. Redis 在 admission 后失联并发生进程重启：新 bootId、耐久 outbox、membership/admission registry 和 runtimeRecoveryEpoch fencing 阻止旧 CLOSED 快照放行；可识别 scope 完成重放/孤儿修复/受控探活前保持隔离。
31. hybrid 请求包含 hosted tool、background/store 或未知 endpoint：replaySafe 默认拒绝，不执行 repair/upgrade。
32. 同层候选快速返回：探索仍受“100 个 eligible 请求且 60 秒”双门槛约束，不能连续消耗后排账号。
33. 普通或 speed-first 请求已可能发网且包含 hosted tool/store/未知副作用：禁止换号和 confirmation，返回 indeterminate_upstream_outcome；不能在两个上游重复执行。
34. 两个 attempt 同时争夺 SSE winner：loser CAS 失败后立即 abort、释放容量并提交 superseded_before_commit，不进入质量或电路。
35. confirmation/half-open 持有节点进程崩溃：due reaper 或 admission 机会式修复在 lease 到期后只放出一个新 lease，状态不能永久卡住。
36. 单节点 Redis 非对称断连：其 membership 过期后，其他节点在新 admission 前先隔离未关闭 registry 对应 scope；健康无关 scope 继续服务。
37. 某个 scope 的 outbox 永久重投失败：仅该 scope 进入 QUARANTINED/DLQ 并按退避审计，live membership 过期节点和该 DLQ 都不能永久关闭全局 READY。
38. 探索 lease 持有节点在发网前崩溃或旧节点迟到释放：matching epoch/policyRevision/leaseId 才能回收，新 lease 不被旧回调删除；已发网 cooldown 不因快速完成提前释放。
39. server rescue window 默认 60 秒：普通/快速、fallback、RR/weighted 和 hybrid 均不重置；60 秒后已启动 image/text attempt 继续按 lane hard 运行，但结束后不再启动新 attempt。
40. 20 个并发在 10 秒内同时 transport 失败：只建立一个 scoped failure round 和一个 confirmation lease，不直接写 temporary_unavailable；5 分钟内恢复后取消账号 promotion epoch。
41. 单模型、单协议或单 Key 持续失败但同物理账号另一路径健康：只保持 scoped OPEN，整账号仍可通过健康路径调度，不得进入 temporary_unavailable。
42. 单能力账号连续波动：只有标准检查 probe、独立 confirmation、5 分钟和 3 个间隔轮次全部满足，且期间无完整成功，才能进入 temporary_unavailable。
43. 同一 proxy/baseUrl/provider 下多个账号同时失败：先建立 upstream bucket circuit，暂停逐账号持久 promotion；bucket 恢复后账号不被批量留在死亡状态。
44. temporary_unavailable 账号坏 2 小时后恢复：slow 阶段仍公平获得单飞 probe，首次成功进入 Verifying，三次独立 canary 和稳定窗口后才 active。
45. 开启持续恢复探活的账号 transport 失败持续 7 天：进入每小时 long_term 恢复，不自动转 error；其他健康账号和 due 队列不被该账号阻塞。
46. 用户显式关闭持续恢复探活：10 分钟内按现有有界退避复测，只有到期后的真实最终 probe 失败才进入 error；worker 停机不能仅凭墙钟伪造 error。
47. 恢复探针成功后 20 个业务会话同时到达：warmup 前 30 秒只允许 1 个 ordinary admission，随后 60 秒最多 25% 配置并发，不能瞬间灌满。
48. warmup 中再次 transport 失败：立即回到当前 scoped OPEN 并创建新 generation，旧 canary/业务终态不能把账号重新 active。
49. 人工恢复、配置修改、停用与迟到 probe 竞争：error 只进入 pending_test，configRevision/accountHealthGeneration CAS 拒绝旧结果，disabled 和用户 schedulable 意图不被覆盖。
50. 授权实例共享同一来源物理账号：只运行一套 physical promotion/recovery；各 binding 的权限、额度和显式策略保持隔离，不产生 N 倍探活。
51. due 队列同时包含大量 fast 和 long_term 账号：keyset 轮转为 long_term 保留份额，后排账号最终获得 probe，且每物理账号、分组、provider 的并发上限均生效。
52. 新 scoped circuit migration gate 开启：旧 recovery_wait/precheck gate 和旧探针写入同时关闭；shadow 阶段只比较不影响派发，任何阶段不存在双 lease 或双重阻断。
53. 一个账号在 100 次 transport failure 中偶尔完成 2 次旧 in-flight success：不能清理 scoped OPEN、不能取消 promotion epoch；只有 matching-generation 三 canary 才能进入恢复验证。

## 14. 落地顺序

### 第零阶段：合同与反向回归

- 建立 route-coordinator result contract、route plan snapshot 和请求级 ledger 类型。
- 先补完整响应透明、状态码不参与自动机制、同层五账号一轮和客户端语义提交的反向回归。
- 明确旧测试中与新核心原则冲突的断言和迁移开关。

### 第一阶段：协调器收敛

- 收敛 routes、preparation、candidate-filter、dispatch 的直接 fallback。
- 让 route coordinator 成为唯一的游标、attempted、等待和跨组推进 owner。
- 保留现有 route mode 的分组、权重、轮询、hybrid 和快速目标语义。

### 第二阶段：短电路与单飞

- 建立 memory/Redis 电路 Store Port、runtime epoch/readiness、node membership、generation、lease、CAS、admission registry 和 due index/reaper。
- 首次作用域 transport 失败立即 SUSPECT，确认 lease 只有一个持有者。
- 完成 OPEN / HALF_OPEN / RECOVERING 和三次 canary，迁移旧 recovery_wait 的 admission 语义；未知/取消/崩溃必须维护 nextLeaseAtMs 和 due index。
- 让 dispatch admission 在网络派发前成为唯一线性点，绑定 runtimeRecoveryEpoch、node bootId、circuit generation、dispatchRevision、容量 permit 和 registry entry。

### 第三阶段：持久状态 promotion 与恢复生命周期

- 建立 physical accountHealthGeneration 和 promotion observation Store；同一并发风暴折叠为单轮，标准 probe、跨能力覆盖、最近成功和 upstream bucket 相关性共同守门。
- 迁移旧 recovery_wait/precheck owner：先 shadow 对比，随后同一开关原子启用 scoped circuit gate并关闭旧 gate、旧 lease 与旧 probe 写入。
- 复用 cooldown retest 的持久队列、keyset 扫描和 configRevision CAS，收敛为每物理账号单飞的 fast/slow/long_term/bounded 状态机。
- 删除持续探活开启时纯 transport 7 天自动 error；保留用户显式 bounded 终态和本地确定性 error。
- 将一次恢复成功改为 Verifying 三 canary、稳定窗口和 30/90 秒 warmup；页面与网关共同读取 effectiveAvailability。

### 第四阶段：首字与请求账本

- 建立统一 AttemptArbiter，保留 lane、soft、lifetime 和 request hard 分层。
- 删除普通扫描中的多轮同账号 retry，只保留 confirmation 一次例外。
- 统一 3 秒控制面协调等待账本，移除与旧 270 秒预算的叠加关系；高并发 FIFO 仍用 `min(maxQueueWaitMs, requestHardRemaining)`。
- 冻结有限 requestHardDeadline 和默认 60 秒 server rescue admission deadline，统一 AbortSignal、terminal CAS、请求级 SSE semantic commit、loser cleanup 和 deadline cleanup。
- 冻结 requestReplayPolicy，把普通切号、速度 handoff、confirmation 和 hybrid 的发网后重放授权收敛到一个 guard。

### 第五阶段：热质量与同层探索

- 建立一分钟桶、5/10/30 分钟派生、terminal dedup 和 Redis 原子更新。
- 在 routeOverrideBand + baseTierKey 内接入质量排序。
- 接入带 epoch/policyRevision/leaseId/due 回收的低频同层探索 token；探针、canary 与业务探索样本分流。

### 第六阶段：响应语义和管理闭环

- 停用默认响应规则的服务端调度副作用，仅保留用户显式规则动作。
- 补齐 performance runtime overlay、状态原因、新鲜度、node membership、scope quarantine/DLQ 和孤儿修复。
- 完成双节点 Redis fail-closed/非对称断连、SSE semantic commit/loser abort、冻结 hybridActionPlan/replay policy、高并发和长 lane 组合回归。

每个阶段都必须同时验证 cost_first 和 speed_first，不能先实现通用热质量再让快速模式被动适配。

## 15. 待校准参数

以下是首轮建议，不得在没有 Mock AI、短窗口生产观测和双节点回归前固化为用户配置：

| 参数 | 首轮建议 |
| --- | --- |
| 控制面协调等待总预算 | 3 秒，普通/快速统一；不包含高并发 admission FIFO |
| 高并发队列 | `min(group.maxQueueWaitMs, requestHardRemaining)` |
| confirmation 次数 | 每个作用域、每请求最多 1 次 |
| scoped hot RECOVERING canary | 串行 3 次，间隔建议 3 秒；如同时承担 persistent Verifying，使用更强的 10 秒/30 秒合同 |
| 自动退避 | 3s -> 5s -> 10s -> 30s -> 60s，上限建议 5 分钟 |
| 整账号持久升级 | 至少 5 分钟、3 个独立后台失败轮次，轮次至少间隔 2 分钟；另需全能力覆盖和标准 probe |
| 恢复验证 | 3 个独立 canary，至少间隔 10 秒，总稳定窗口至少 30 秒 |
| 恢复 warmup | 前 30 秒最多 1 个普通 admission；随后 60 秒最多 25% 配置并发；之后全量 |
| 长期恢复 | 连续不可用 12 小时后每 1 小时一次；持续探活开启时不设纯 transport 7 天死亡终点 |
| 全局完整诊断 | 复用现有有界上限，首期每进程/worker 最多 3 路；每物理账号和分组最多 1 路 |
| 同层探索 | 每请求最多 1 次；共享桶最近 100 eligible 请求且 60 秒最多 1 次 |
| 文本请求 hard deadline | 默认 1800 秒，范围 60-86400 秒 |
| 图像请求 hard deadline | 默认 3600 秒，范围 60-86400 秒 |
| 请求救援准入墙钟 | 默认 60 秒，范围 10-120 秒；route plan 形成时冻结，所有模式/lane 共用，不重置 |
| 新派发预留 | 固定 5 秒；`now + 5s < serverRescueDeadlineAt` 才能启动下一 attempt |

调参不能改变以下不变量：路由优先、同层普通候选最多一轮、语义提交后不拼接、状态必须 CAS、完整响应不自动解释、热数据不落业务库。

## 16. 评审结论

本文替代旧版以下结论：固定同层失败数 2、单一公共硬首字 10 秒、协调预算与 270 秒预算叠加、完全禁止同层探索、首次失败直接账号级封锁和固定全局 attempt cap。

本文保留：路由策略最高级、普通/快速模式职责分离、透明响应边界、短窗口热质量、账户配置层优先级、受控半开、三次恢复 canary、持久状态与热运行态分离。

在代码实现、回归通过和双节点验证完成前，本文只作为实施合同，不能作为“生产已经具备该行为”的说明。
