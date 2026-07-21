# AI 账户短窗口热质量与精准切号设计

> 当前状态：目标设计，尚未落地实现。
>
> 本文定义普通路由账户调度、短窗口热质量、故障单飞、受控半开、请求内精准切号和探活恢复的统一目标合同。现行实现仍以 [策略路由设计](策略路由设计.md)、[普通路由速度优先延迟切换设计](普通路由速度优先延迟切换设计.md)、[AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)、[网关错误处理完整链路](网关错误处理完整链路.md) 和 [网关异常重试与兜底策略](网关异常重试与兜底策略.md) 为准；实施本文时必须按第 16 节迁移顺序收敛冲突，不能把本文当作现行代码已经满足的事实。

## 1. 决策摘要

本设计先固定以下不可变合同：

1. 路由策略是最高业务调度层。路由先决定模式、分组、分组顺序、目标和路由覆盖；账户层不能重新选组、重抽权重、重建轮询环或自行 fallback。
2. 账户层只在路由允许的目标范围内回答可执行性，并在最终有效候选带内按账户配置层排序。路由目标可以覆盖账户偏好，但账户电路不能被路由偏好绕过。
3. 同一账户配置层使用“所有唯一候选最多一轮”，不再使用固定的“同层失败 2 个账号”预算。5 个 P1 账号中，前 4 个在请求救援时间内结束时，游标必须继续到第 5 个；第 5 个仍失败才允许进入下一层。
4. 首字统一的是事实和仲裁器，不是一个适用于所有 lane、模式和请求的数值。软观察、lane hard timeout、attempt lifetime、idle timeout 和请求级救援期限必须保持不同原因码。
5. 自动电路只消费网关本地可验证的 transport 事实。完整 HTTP/SSE framing 在未命中用户显式高级规则时透明转发；上游状态码、响应头、错误码和正文不能被内部自动机制解释为切号或账号故障。
6. 运行态分为配置意图、热运行态和持久异常三层。首次 transport 失败先阻断对应 protocol-model 作用域并发起唯一 confirmation；只有独立作用域证据满足条件，才升级到账号级电路。
7. 热质量保存在 Redis 或进程内存，按最近 5、10、30 分钟计算。质量只能在完全相同的有效账户配置层内排序，不能跨优先级、备用或分组。
8. 同层探索允许存在，但只能是低频、真实业务流量、同一有效层内的首选替换；不增加请求、不访问 P2/备用、不绕过会话亲和、电路、容量和路由覆盖。
9. route coordinator 独占请求级游标、尝试集合、等待预算和跨组推进。routes、preparation、candidate-filter、dispatch 不得各自创建 fallback、重试预算或新的尝试轮次。

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
routeOverrideBand
dispatchRevision
```

快照冻结本次请求的组顺序、权重决定、轮询起点、hybrid 目标档位和路由覆盖带。动态运行态可以让候选暂时不可执行，但不能重新抽样、重建环、重算权重或扩大目标范围。

### 4.3 routeOverrideBand 与 baseTierKey

账户候选先经过路由层产生的最终有效覆盖带，再在覆盖带内排序：

```text
routePlanSnapshot
  -> hard eligibility
  -> routeOverrideBand
  -> baseTierKey
  -> hot quality
  -> affinity / stable order
  -> same-tier exploration
```

routeOverrideBand 是路由层已经裁决好的候选子集和原因，至少记录：

```text
routeObjective
manualMigrationTarget
latencyDegradedTarget
highConcurrencyQueueState
hardCapacityState
overrideReason
```

账户层不能把手动迁移、快速模式慢目标、高并发 fallback 和账户比较器重新合并成第二个 comparator。不同路由模式已有的相对顺序由对应路由 reducer 产出，账户层只消费最终 routeOverrideBand。

baseTierKey 固定为：

```text
modelMatchRank
+ fallbackEnabled
+ superPriorityEnabled
+ priority
```

热质量、会话亲和和同层探索都不能跨越 baseTierKey；快速模式和高并发等路由覆盖若需要跨越，必须在 routeOverrideBand 中明确表达，而不是由账户层偷偷改变优先级。

### 4.4 模式约束

| 模式 | 路由层负责 | 账户层负责 |
| --- | --- | --- |
| cost_first | 保持组和账户偏好，必要时请求内故障切号 | 过滤电路、授权、能力、容量和已尝试身份；同层一轮后报告结果 |
| speed_first | 速度目标、latency_degraded、安全窗口和可接受的跨层切号 | 过滤不可执行账号；不得把已确认慢账号复活到覆盖带 |
| failover | 主用、备用和后续分组顺序 | 只报告当前分组结果，不自行进入备用组 |
| round_robin | 本轮环、起点和回访顺序 | 推进当前分组候选游标，不重建环 |
| weighted | 本次权重 token 和允许目标 | 不重抽权重、不把质量写成持久重分配 |
| hybrid_smart | 评分、模型档位、repair/upgrade/return action | 在目标档位内过滤账户；质量 action 必须由 coordinator 执行 |

hybrid 的质量动作是路由动作，不是账户电路动作。repair_then_upgrade 可以在语义尚未提交时使用独立的 hybrid repair 预算，但必须继续使用同一个请求救援墙钟和 route plan，不能重置 attempted 集合。

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
dispatchRevision
```

快照冻结候选成员、层级和稳定顺序；每次真正占用并发槽或发出请求前，必须重新核对持久硬资格、运行态 generation、容量和 dispatchRevision。

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

### 5.3 请求级 attempted 集合

同一请求维护以下集合，跨组、fallback、hybrid 和上下文切换都不能清空：

```text
attemptedProtocolModelKeys
attemptedBindingKeys
attemptedAccountRuntimeKeys
attemptedKeyFingerprints
attemptedPurposes
```

普通业务真实发出 attempt 后加入 attemptedProtocolModelKeys。本地 Key 准备失败或用户明确允许 Key 动作时，只加入 attemptedKeyFingerprints。confirmation、half-open、hybrid repair 是带 attemptPurpose 和 lease/generation 的显式例外，不能被普通 attempt 再次占用。

同一物理协议模型通过不同授权实例或后续分组出现时，本请求不得重复发出同一普通业务 attempt；本地授权决策仍必须在每个 binding 上独立校验。

### 5.4 候选窗口与完整性

当前数据库扫描窗口和 hydrate 窗口不是全量证明。设计要求：

- 先按稳定排序字段使用 keyset pagination，而不是 offset 重新排序。
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
2. 已经 SUSPECT / OPEN / HALF_OPEN / RECOVERING 的普通候选直接跳过，不占用普通 attempt。
3. 容量忙、队列等待或暂时阻断的候选进入 pendingBlockedTargets，不算已尝试；回访时仍只能消费该候选一次。
4. 真实 transport attempt 发出后，候选永远不能回到本请求的普通游标。
5. 当前层所有唯一候选完成一轮后，且仍有请求救援时间，才进入下一层。
6. 请求救援墙钟先耗尽时，返回 deadline_exhausted，不能声称当前层已经完整耗尽。

因此，5 个 P1 的合同是：前 4 个快速失败时必须尝试 P1 #5；如果前 4 个各自占满合法 lane hard timeout，任何设计都不能同时保证到达第 5 个和保留全部慢请求，系统应按请求救援期限结束并明确记录原因，而不是无限等待。

### 6.4 路由回访

pendingBlockedTargets 只能是当前 route plan 已允许的目标子集。回访前必须重新检查：

```text
semanticCommitted == false
requestRescueDeadline 未到期
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
  semanticCommitted
  terminalCas
}
```

首字事实定义为：

- 非流式：首个可见 body 字节；
- 流式：首个可见语义 chunk；
- 响应头、SSE heartbeat、空事件、内部缓冲不算首字；
- 每个 attempt 只产生一次 observed / deadline_reached / cancelled / unknown。

统一事件不代表把快速模式软观察、普通 lane hard timeout 和 attempt lifetime 压成同一个数字。任何账户层、响应层或快速模式都不得再次创建首字 timer。

### 7.2 四层时间边界

| 边界 | 作用 | 是否写自动账户电路 |
| --- | --- | --- |
| softCheckpoint | 路由观察、速度样本或请求内 handoff | 否 |
| laneHardAt | 首响应/读取未完成的真实 transport 失败 | 是，写对应作用域 |
| attemptLifetimeAt / idleDeadlineAt | 首字后的资源和流式活跃生命周期 | 只有真实读取中断/EOF 才写对应作用域 |
| requestHardDeadlineAt | 请求级最终墙钟，防止无限挂起 | 不把正常慢请求伪装成账号故障 |

另有 serverRescueDeadlineAt，它只决定还能不能启动新的隐藏救援 attempt；已经启动的 attempt 继续遵守自己的 lane hard/lifetime，除非请求 hard deadline 或客户端取消。

### 7.3 普通模式与快速模式

- cost_first：soft checkpoint 只产生请求级观察。存在可执行替代候选且语义尚未提交时，可以做 request_scoped_slow_handoff；该动作不写 latency_degraded、不写账户电路、不计 transport failure。没有替代候选时继续当前 lane 规则。
- speed_first：同一个 soft event 先幂等记录慢样本，再由 slowTriggerCount、latency_degraded、安全写出窗口和模式切号上限决定是否切号。慢样本不能直接打开账户电路。
- 真正的 lane hard timeout、读取中断、EOF 或连接失败才进入 transport 电路和 confirmation。
- 图像、长思考、embedding 等 lane 保留各自 timeout profile；不能因为速度模式的软阈值中断合法长耗时请求。

### 7.4 请求救援账本

每个请求只有一个 RequestRescueLedger：

```text
RequestRescueLedger {
  requestStartAt
  requestHardDeadlineAt
  serverRescueDeadlineAt
  coordinationRemainingMs
  ordinaryAttemptCount
  confirmationRemaining
  hybridRepairRemaining
  reservedDispatchMs
  version
}
```

规则：

- 不设一个会截断 RR/weighted 全环或 hybrid 合法 repair 的固定全局 N=6。
- 普通业务的唯一候选上限由 route plan 和同层一轮决定；confirmation、hybrid repair、recovery canary 使用独立目的预算，但共享请求救援墙钟。
- 每次新 attempt 的首字前时间片取 min(laneHardRemaining, serverRescueRemaining / remainingMandatoryCandidates)，并为后续合法目标保留 reservedDispatchMs。
- routeCoordinationBudget 是普通模式和快速模式共同的累计协调等待预算，初始建议 3 秒，覆盖切号等待、重选号、层级切换和零可派发等待。
- 现有 ServerRetryBudget 只能作为该唯一账本的兼容实现；不能再与旧 270 秒 no-available 预算叠加。每请求只创建一次，不能跨组重置。
- 活跃 fetch、首字观察和响应读取不扣协调等待；等待前必须扣除协调成本并保留最小派发时间。
- 任何预算耗尽都只能由 coordinator 产生一次终态，不能继续进入低层等待或重复派发。

### 7.5 Confirmation

confirmation 是一次真实上游 attempt，不是第二套重试循环：

- 首次作用域 transport 失败时，CAS 设置 SUSPECT 并只发放一个 matching-generation lease。
- lease 持有者最多执行一次 confirmation；其他请求立即换赛道。
- confirmation 使用同一个首字事实和当前 lane 规则，不另设隐含 5 秒 timer。
- confirmation 在语义提交前失败或超时，作用域进入 OPEN；租约未知或客户端取消只释放 lease。
- confirmation 成功只能进入 RECOVERING，不能一次成功直接 CLOSED。

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

OPEN -- backoff due + lease --> HALF_OPEN
  |                               |
  +-- canary success ------------> RECOVERING

RECOVERING -- 3 independent canaries --> CLOSED
RECOVERING -- transport failure -------> OPEN
```

| 状态 | 普通业务 | 特殊 lease | 说明 |
| --- | --- | --- | --- |
| CLOSED | 可派发 | 无 | 允许正常候选排序 |
| SUSPECT | 排除 | 一个 confirmation | 首次失败后的即时止损 |
| OPEN | 排除 | 到期后一个 half-open | 退避中 |
| HALF_OPEN | 排除 | 一个 recovery canary | 多节点单飞 |
| RECOVERING | 排除 | 一个 matching-generation canary | 连续三次稳定恢复 |

收到首字、heartbeat 或一次 framing 不完整的成功都不能关闭电路。只有完整 HTTP framing，或 SSE 在没有读取中断的情况下正常结束，才计入 canary 成功；判断不得读取状态码和业务正文。

### 8.3 Redis CAS 与租约

电路状态至少包含：

```text
state
failureScope
generation
leaseId
leaseUntilMs
attemptHardDeadlineMs
openUntilMs
backoffLevel
consecutiveFailures
recoveringSuccesses
lastFailureClass
transitionId
dispatchRevision
updatedAtMs
```

Redis Lua 或等价原子 Store 必须同时校验和更新：

```text
state + generation + leaseId + dispatchRevision
```

同一 generation 只能有一个 confirmation/half-open/recovery lease。过期状态不能因为 Redis key 自然消失就直接变成 CLOSED；必须同时检查 due index、policy block 和 fencing 记录。配置、凭据、代理、协议档案或模型映射变化要递增 dispatchRevision 和 generation，旧结果只能释放资源，不能提交新状态。

### 8.4 探活与恢复

- 后台探活使用有界、成本可控的真实协议请求，只判断 framing/传输是否完成，不读取上游业务语义。
- runtime_probe 和 recovery_canary 不计入业务热质量，也不推进普通探索令牌。
- RECOVERING 的三个 canary 必须是不同 lease、跨时间串行完成，不能在一个请求里连发三次。
- 任一 canary transport 失败立即回到 OPEN 并增加退避。
- 客户端取消、没有真实 attempt、旧 generation 或未知结果只释放 lease，不计成功或失败。

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

## 9. 热质量与受控同层探索

### 9.1 热数据存储

- performance：Redis 是跨节点热状态事实源。
- standalone：进程内存保存等价状态，固定最大条目数和 TTL 清理。
- Redis 不可用时不能静默退回本机内存伪装成共享事实；应保守拒绝共享运行态决策或显式报告基础设施故障。
- 业务数据库质量表继续用于展示和历史分析，不作为实时调度依赖。

### 9.2 质量维度与窗口

热质量 key：

```text
physicalResourceKey
+ protocolProfile
+ requestLane
+ mappedModelFamily
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
5. 使用热 token，按 routePlanId + groupId + lane + modelFamily + baseTierKey 限制频率；每请求最多一次、每个桶最多一个在途探索，取消或未发出时释放 token。
6. 探索沿用正常业务 attempt，不发送额外探针、不改变账户优先级、不写持久状态、不跨 P2/备用。
7. 首期不新增用户配置项，默认只在完全相同层内低频启用；后续如需关闭，配置属于路由/分组策略，不属于账号。

探索成功计入业务热质量，但不能推进 RECOVERING；探索失败按真实 transport 作用域处理，不能因“探索”扩大电路范围。

## 10. 响应语义、流式与客户端边界

### 10.1 自动机制

未命中用户显式高级规则时：

- 完整 HTTP 响应无论状态码、响应头和正文是什么，都视为 transport completed 并透明转发。
- 完整响应不能自动重试、切号、轮换 Key、写账号电路或写上游桶失败。
- 连接失败、lane hard timeout、读取中断、EOF 或未形成完整 framing，才进入对应 transport 作用域。
- generic_* 客户端不能被服务端自动解释响应语义；协议渲染和客户端画像不能成为自动切号授权。

### 10.2 用户显式规则

只有账户所有者在前端显式配置的错误策略或响应检查策略，才能读取状态码、响应头、正文、JSON 路径、SSE 事件或完成状态。规则动作标记为 source=explicit_policy，并独立计入策略电路/持久 TTL；自动 transport 成功不能提前清理用户 TTL。

### 10.3 语义提交

- semanticCommitted=false 时，仍可在救援账本和路由计划允许的范围内切号。
- SSE 已写出可见语义内容后，禁止透明拼接第二账号、重复工具调用或重放请求。
- SSE heartbeat、响应头、空事件和内部缓冲不算语义提交。
- 非流式实现可在边界安全的前提下缓冲有限首段，但不能要求无界完整 body 缓冲来换号。
- 客户端取消只释放并发和 lease，不把账号标记为失败；confirmation/half-open 取消结论为未知。

## 11. 与现有机制的关系

| 现有机制 | 目标关系 |
| --- | --- |
| 显式账户错误策略 | 保留，优先级高于自动热质量；可执行 retry_next / cooldown / disable |
| 账户内多 Key | 本地 Key 准备失败或显式 Key 动作才可换 Key；完整响应不能自动轮 Key |
| IP 级账号回避 | 保留为窄作用域辅助，不承担共享账号故障保护 |
| 上游桶健康 | 继续识别代理/Base URL/provider 公共故障，不替代单账号电路 |
| recovery_wait / precheck_pending | 迁移为后台持久确认层；新短电路先承担秒级止损，recovery_wait 不能继续允许无限新请求 |
| 持久冷却复测 | 保留现有持久状态恢复语义，不进入业务热质量 |
| 历史质量统计 | 保留展示和分析，不参与实时候选读取 |
| latency_degraded | 保持路由层状态，只影响启用该快速目标的范围 |
| system_default 响应规则 | 保留协议渲染能力，但不得产生未授权的账户调度副作用 |

### 11.1 必须迁移的现行冲突

当前 failure-dispatch、upstream-dispatch、流式结束重试决策和默认响应检查仍可能依据完整非 2xx、错误类型或正文自动重试、切号、轮 Key、写上游桶或写运行态。实施时必须：

- 停用未获用户显式授权的响应语义副作用；
- 收敛 routes、preparation、candidate-filter、dispatch 的直接 fallback；
- 用 route-coordinator 统一 attempted、预算、游标和跨组推进；
- 将旧 same-account retry budget 改为 confirmation lease 一次例外；
- 更新“recovery_wait 仍可调度”的旧测试契约，使首次作用域 transport 故障可以即时阻断新流量；
- 补齐 Redis runtime overlay，避免网关与管理页面对可调度状态产生分歧。

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
```

日志只在状态转换和请求终态输出结构化摘要。被电路排除的请求不伪造上游失败 usage，也不重复打印同一上游错误正文；摘要只记录候选身份、作用域、原因码、generation、游标和预算，不记录上游响应正文。

## 13. 验收矩阵

实施必须覆盖以下场景，普通模式和快速模式都要分别验证：

1. 20 个同 IP、多会话同时命中同一坏账号：已在途请求允许结束；首次状态转换后只允许一个 confirmation，后续新请求全部换赛道。
2. 不同 IP、不同 API Key 命中同一物理资源：按物理作用域共享电路，不依赖 IP 回避阈值。
3. 5 个 P1 中前 4 个快速失败、第 5 个健康：必须到达第 5 个，不得因固定失败预算提前进入 P2。
4. 5 个 P1 全部失败、P2 健康：P1 只扫描一轮，随后按路由计划进入 P2，不重复扫描 P1。
5. P1 长期健康：P2 和备用不产生探索流量。
6. 同层后排账号无样本：只在同层 token 允许时被真实业务探索，不跨优先级、不消耗备用资源。
7. 一个账号 10 分钟后持续 transport timeout：第一次作用域失败立即 SUSPECT，新请求不再灌入，不等待质量窗口。
8. 偶尔成功两次后再次失败：保持 RECOVERING 语义，三次独立 canary 前不能关闭。
9. 两个 Redis server 同时抢 confirmation/half-open：同一 generation 只有一个 lease 成功。
10. 单 Key 本地装配失败且另一个 Key 可用：只切 Key，不打开账号电路；完整响应不能自动轮 Key。
11. 通用客户端收到完整 401/429/5xx：透明转发，不自动切号、不写自动电路。
12. cost-first 合法慢请求：soft checkpoint 不误写账号故障；有替代时可以请求内 handoff，无替代时遵守 lane hard。
13. speed-first 已确认慢的超级优先账号：未降级候选可按路由覆盖越过；热质量和 affinity 不得拉回慢账号。
14. speed-first 单次慢但未达到阈值：只记慢样本，不强制切号。
15. SSE 首字前 transport 失败可以切号；首字后读取中断绝不拼接第二账号。
16. 客户端取消 confirmation/half-open：只释放 lease，不累计失败。
17. 同一物理账号通过主用和备用授权实例出现：同请求不重复发普通 protocol-model attempt。
18. 256 窗口或扫描窗口截断：返回 window_incomplete，不能 hard_exhausted。
19. hybrid repair/upgrade：使用独立 purpose budget，但不重建 route plan、不清空 attempted、不绕过语义提交边界。
20. 高并发队列等待：等待先扣统一协调预算并保留后续 dispatch 时间，不能独占整个救援窗口。
21. Redis 不可用：performance 不伪装成本机共享状态，按保守不可用处理并记录指标。
22. 同一 attempt 终态重复提交：质量桶和电路只投影一次，CAS 冲突可观测。
23. 配置 revision 在 confirmation 期间变化：旧结果不能污染新 generation。
24. performance 账户列表：显示 Redis runtime overlay 的状态、原因和新鲜度，与网关候选一致。

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

- 建立 memory/Redis 电路 Store Port、generation、lease、CAS 和 due index。
- 首次作用域 transport 失败立即 SUSPECT，确认 lease 只有一个持有者。
- 完成 OPEN / HALF_OPEN / RECOVERING 和三次 canary，迁移旧 recovery_wait 的 admission 语义。

### 第三阶段：首字与请求账本

- 建立统一 AttemptArbiter，保留 lane、soft、lifetime 和 request hard 分层。
- 删除普通扫描中的多轮同账号 retry，只保留 confirmation 一次例外。
- 统一 3 秒协调等待账本，移除与旧 270 秒预算的叠加关系。

### 第四阶段：热质量与同层探索

- 建立一分钟桶、5/10/30 分钟派生、terminal dedup 和 Redis 原子更新。
- 在 routeOverrideBand + baseTierKey 内接入质量排序。
- 接入低频同层探索 token；探针、canary 与业务探索样本分流。

### 第五阶段：响应语义和管理闭环

- 停用默认响应规则的服务端调度副作用，仅保留用户显式规则动作。
- 补齐 performance runtime overlay、状态原因、新鲜度和孤儿修复。
- 完成双节点 Redis、SSE、hybrid、高并发和长 lane 组合回归。

每个阶段都必须同时验证 cost_first 和 speed_first，不能先实现通用热质量再让快速模式被动适配。

## 15. 待校准参数

以下是首轮建议，不得在没有 Mock AI、短窗口生产观测和双节点回归前固化为用户配置：

| 参数 | 首轮建议 |
| --- | --- |
| 协调等待总预算 | 3 秒，普通/快速统一 |
| confirmation 次数 | 每个作用域、每请求最多 1 次 |
| RECOVERING canary | 串行 3 次，间隔建议 3 秒 |
| 自动退避 | 3s -> 5s -> 10s -> 30s -> 60s，上限建议 5 分钟 |
| 同层探索 | 同层 token 低频、每请求最多 1 次、每桶最多 1 个在途 |
| 请求救援墙钟 | 由 lane/request profile 和候选数量共同计算，不固定为 10 秒或 60 秒 |

调参不能改变以下不变量：路由优先、同层普通候选最多一轮、语义提交后不拼接、状态必须 CAS、完整响应不自动解释、热数据不落业务库。

## 16. 评审结论

本文替代旧版以下结论：固定同层失败数 2、单一公共硬首字 10 秒、协调预算与 270 秒预算叠加、完全禁止同层探索、首次失败直接账号级封锁和固定全局 attempt cap。

本文保留：路由策略最高级、普通/快速模式职责分离、透明响应边界、短窗口热质量、账户配置层优先级、受控半开、三次恢复 canary、持久状态与热运行态分离。

在代码实现、回归通过和双节点验证完成前，本文只作为实施合同，不能作为“生产已经具备该行为”的说明。
