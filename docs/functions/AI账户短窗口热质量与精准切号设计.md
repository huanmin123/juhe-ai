# AI 账户短窗口热质量与精准切号设计

> 当前状态：待评审设计，尚未落地实现。
>
> 本文定义普通路由下的账户短窗口热质量、故障单飞、受控半开、请求内精准切号和客户端止损目标。现行实现事实仍以 [策略路由设计](策略路由设计.md)、[普通路由速度优先延迟切换设计](普通路由速度优先延迟切换设计.md)、[AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md) 和 [网关异常重试与兜底策略](网关异常重试与兜底策略.md) 为准；本文评审通过并实施时，必须同步收敛其中与本设计冲突的普通失败、优先级边界和半开语义。

## 1. 背景

上游账号的可用性变化以分钟甚至秒为单位：账号可能连续 10 分钟正常，随后 2 小时不可用；也可能前一分钟正常、后一分钟持续超时；偶尔成功一两次不能证明账号已经稳定恢复。日级历史质量无法指导当前请求，甚至会把已经失效的旧成功放大为错误偏好。

现行链路还存在以下组合风险：

- 单个请求先在同一账号原地确认多次，其他并发请求也能同时命中该账号，形成失败风暴。
- 普通失败建立共享软阻断较晚，后台确认生效前，多 IP、多会话仍可重复访问同一异常账号。
- 普通运行态降级和 IP 回避保留账户优先级层，异常高优先级账号可能持续挡住低优先级健康账号。
- 现有质量统计以后台聚合和持久结果为主，不适合作为秒级调度事实；短窗口传输完成率也不能只作为报表字段而不进入可靠性判断。
- 后排账号通常是用户主动保留的兜底资源。为获取样本而主动探索会消耗用户不希望使用的额度，破坏优先级和备用语义。

因此目标不是增加一个持久化“质量分”，而是建立一套短窗口、易失、原子共享的调度运行态。

## 2. 目标与非目标

### 2.1 目标

- 使用最近 5、10、30 分钟的真实网关流量判断账号当前可靠性和速度，旧历史快速失效。
- performance 模式使用 Redis、standalone 模式使用进程内存保存热状态，不把热质量写入业务库或统计结果库。
- 一个账号出现网关本地可验证的 transport、timeout、读取中断或未完成响应后，只允许一个请求持有确认租约继续验证；其他请求立即排除该账号并重新选号。
- 高优先级账号异常时允许当前请求有界逃逸到下一优先级层，避免客户端被异常主账号长期卡住。
- 保持用户配置的超级优先、账号优先级、备用和分组边界；不主动探索或消耗后排兜底账号。
- 明确路由策略是最高业务调度层；账户电路、热质量和切号只能在路由选定的分组与候选范围内工作，保证速度优先继续作为路由层目标覆盖账户偏好。
- 保持多 API Key 隔离、显式账户错误策略、响应检查、客户端画像和流式写出安全边界。

### 2.2 非目标

- 不使用日级、周级或完整历史质量直接参与实时选号。
- 不为了学习质量向后排、低优先级或备用账号主动发送业务探索流量。
- 不做同时请求多个上游后取最快结果的并发抢跑。
- 不用热质量直接修改 `accounts.status`、`schedulable`、`cooldown_until` 或用户配置的优先级。
- 不把请求语义错误、客户端取消或模型能力不匹配升级为账号故障。
- 不在本设计中改变策略路由的分组绑定、权重、轮询、故障回退或混合智能语义。

## 3. 术语与分层

### 3.1 最高层级是路由策略

固定业务层级为：

```text
API Key
-> 路由策略
-> 路由选定的模式、分组顺序与路由目标
-> 选定分组内的 AI 账户调度
-> 真实上游 attempt
```

本文只设计“选定分组内的 AI 账户调度”，不得反向影响路由层：

- 不重新选择或混排路由策略绑定的分组；
- 不改变普通路由单分组边界；
- 不改变权重、轮询、故障回退、混合智能或其他策略路由的分组选择结果；
- 当前分组账户耗尽时，只向路由协调器报告当前分组不可承接；是否进入后续分组由路由策略决定；
- 路由目标与 AI 账户偏好冲突时，路由目标优先。快速模式确认慢后切号可以覆盖账户超级优先、账号优先级、备用、会话亲和和热质量。

账户状态、授权、模型能力、协议能力、额度、并发硬上限和账户电路只回答“这个账户当前能不能执行路由结果”，属于可执行性条件，不是与路由策略竞争的更高业务策略。路由策略不能强制一个实际上不可执行的账号发起请求，账户层也不能借可执行性判断改变路由策略。

### 3.2 普通路由的两种调度偏好

普通路由模式仍为 `normal`，只能绑定一个分组。`normalRoutingConfig.schedulingPreference` 区分：

- `cost_first`：本文简称“普通模式”。优先尊重账户配置、会话亲和和稳定顺序，仅在账号已不可调度或当前请求证明确实失败时切换。
- `speed_first`：本文简称“快速模式”。在硬约束和账户可用性通过后，路由层首字速度目标高于超级优先、账号优先级、备用、会话亲和与热质量；确认进入 `latency_degraded` 的账号可以被同分组未降级硬可承接账号越过。

两者共用账户故障电路、短窗口可靠性、首字定义、计时起点和 `firstByteDeadlineMs`。普通模式把截止时仍无首字解释为本地存活失败；只有快速模式继续启用慢样本累计、`latency_degraded`、确认慢后切号和速度恢复探针。

### 3.3 路由结果内的三类事实

| 类型 | 示例 | 作用域 | 能否跨账户优先级 |
| --- | --- | --- | --- |
| 路由策略目标 | 快速模式的 `latency_degraded` 与安全切号 | 路由策略 + 分组 + 账号 | 可以，路由目标高于账户偏好 |
| 账户可执行性 | 状态、授权、模型能力、并发硬上限、`OPEN`、无租约 `HALF_OPEN` | 路由选定分组内的账号 / Key / 授权实例 | 不是策略排序；不可执行账号不进入可用候选 |
| 账户配置偏好 | 非备用、超级优先、`priority`、会话亲和、热质量 | 分组绑定与请求上下文 | 仅在同等可调度范围内排序 |

路由选定模式与分组后的账户选择流程为：

```text
路由策略目标
-> 账户可执行性过滤
-> 路由目标在可执行候选上的覆盖排序
-> 账户配置层级
-> 同层热质量
-> 会话亲和与稳定顺序
```

这里的账户可执行性过滤不是高于路由策略，而是执行路由结果的必要条件。热质量不能让不可调度账号复活，不能选择路由未选中的分组，也不能把快速模式已确认慢的账号重新排到未降级账号前面。

### 3.4 冲突裁决规则

任何账户调度能力都必须按以下规则处理冲突：

1. 路由策略决定调用范围、分组顺序、路由模式和路由目标；账户层不得反向修改这些结果。
2. 账户层只能在路由结果允许的范围内过滤不可执行账号、执行账户电路和排列可执行候选。
3. 快速模式路由目标与账户优先级、会话亲和或热质量冲突时，快速模式目标胜出。
4. 快速模式选中的账号如果被账户电路判定为不可执行，账户层只能在同一已选路由范围内提供下一个可执行账号；不能强行调用该账号，也不能自行跳到未选分组。
5. 账户层没有可执行候选时，只返回“当前路由范围不可承接”的结构化结果，由路由层决定后续分组、策略 fallback 或最终错误。
6. 显式账户错误策略属于账户所有者的账户侧规则，不能改变路由范围；它只决定当前账号如何处理、是否切下一个账号或建立账户侧持久状态。

账户层返回结果固定区分：

```text
dispatchable {
  accounts
}

temporarily_blocked {
  earliestRetryAtMs
  confirmationInFlight
  blockedAccountIds
  reason
}

hard_exhausted {
  reason
}

request_exhausted {
  attemptedProtocolModelKeys
  attemptedAccountRuntimeKeys
  attemptedKeyFingerprints
  reason
}
```

- `temporarily_blocked` 表示当前路由范围内仍存在本请求可以合法取得唯一 confirmation / half-open lease 的即将到期 `OPEN / HALF_OPEN`，或当前请求自身持有的在途 confirmation；如果阻断账号已经被本请求尝试过且没有合法 lease 例外，就不能为了等待它重复派发，必须返回 `request_exhausted`。
- `hard_exhausted` 只表示当前路由范围内已经没有满足能力、授权、状态或其他硬资格的账号，或已经进入确定不可恢复的持久终态；瞬态 attempt 失败、短电路退避和 confirmation 在途不得构成 `hard_exhausted`。
- `request_exhausted` 表示当前请求已尝试完当前路由范围内所有本来可执行的协议作用域账号，且没有可合法取得 lease 的临时阻断账号；这些账号对其他新请求仍可能是 `CLOSED`，因此该结果不能写共享账号状态。账户层必须把 `attemptedProtocolModelKeys`、`attemptedAccountRuntimeKeys` 与 `attemptedKeyFingerprints` 作为候选过滤输入，返回该结果后不得为同一请求重新派发已尝试的协议作用域、物理运行态账号或 Key。
- 所有路由模式都由路由协调器独占“等待、使用后备分组、执行策略 fallback 或返回最终错误”的决定；账户层不得直接调用分组 fallback。

### 3.5 各路由模式如何消费账户层结果

账户层只报告当前路由已选分组的状态。路由协调器按自身模式消费结果：

| 路由模式 | `dispatchable` | `temporarily_blocked` | `request_exhausted` | `hard_exhausted` |
| --- | --- | --- | --- | --- |
| `normal` | 在唯一分组内派发 | 唯一分组没有立即可派发账号；在共享预算内最多等待 `earliestRetryAtMs`，否则交还客户端 | 本请求的唯一分组终止，交还客户端 | 唯一分组终止，交还客户端 |
| `failover` | 在当前主用或备用分组内派发 | 当前分组此刻不可承接，由路由协调器按既有主用、备用顺序继续；所有允许分组都临时阻断时才考虑有界等待 | 本请求停止扫描当前分组，按既有备用顺序继续 | 当前分组硬耗尽，立即按既有备用顺序继续 |
| `round_robin` | 在本轮路由选中的分组内派发 | 继续本轮稳定环中的后续允许分组；全环临时阻断时才考虑最早到期时间 | 本请求停止扫描当前分组，继续稳定环中的后续允许分组 | 继续本轮稳定环中的后续允许分组 |
| `weighted` | 在本次权重选择的分组内派发 | 由权重路由协调器在本请求剩余允许分组中继续，不能由账户层重写权重或形成持久重分配 | 本请求停止扫描当前分组，由权重路由协调器在剩余允许分组中继续 | 由权重路由协调器在剩余允许分组中继续 |
| `hybrid_smart` | 在评分结果允许的目标模型和分组内派发 | 仅继续评分等级规则已允许的后续目标；账户层不能修改评分、模型档位或扩大分组范围 | 本请求停止扫描当前目标，仅继续评分等级规则已允许的后续目标 | 仅继续评分等级规则已允许的后续目标 |

`temporarily_blocked` 不是让低层强制等待的指令。存在当前路由模式允许的后续分组时，路由协调器优先推进其自身策略；只有阻断账号对本请求仍有合法 lease 机会、所有允许路径都暂时阻断且共享 `ServerRetryBudget` 仍有余额时，才等待最近的 `earliestRetryAtMs`。否则直接返回 `request_exhausted`。该结果只终止本请求的当前分组扫描，不能影响下一请求的候选。任何分组切换都不创建新预算，也不清空本请求已经尝试的协议作用域、运行态账号与 Key 集合；同一物理 `accountRuntimeKey` 即使通过不同授权实例或分组绑定出现，也不能在同一请求中重复派发同一协议作用域。

路由协调器在请求开始时创建不可变 `routePlanSnapshot`，至少包含 `routePlanId / mode / firstByteDeadlineMs / orderedAllowedTargets / cursor / weightedDecisionToken / hybridScoreDecision`。账户结果只能推进 cursor：weighted 不能重新抽样或改变本次权重决策，round-robin 不能重建环，failover 不能跳过既有主备顺序，hybrid 不能重新评分或扩大等级目标。该快照与四类账户结果一起构成 route-coordinator 的实现契约。

## 4. 不探索原则

后排账号无样本是用户优先级生效的正常结果，不视为需要修复的饥饿。

假设分组内配置：

```text
P1: 账号 1、2、3、4
P2: 账号 5、6、7
备用: 账号 8、9
```

正常行为：

- P1 存在健康、可承接账号时，不向 P2 或备用账号发送探索流量。
- P1 内部只有账号配置层级完全相同的候选，才允许按热质量调整相对顺序。
- P1 全部被硬过滤、电路阻断、并发占满，或当前请求耗尽 P1 失败预算后，才进入 P2。
- 非备用账号不能承接后，才进入备用账号。
- 后排账号首次真正被切到时，热质量状态为 `unknown`，按账户配置和稳定顺序选择；真实请求自然产生样本后，才参与同层热质量排序。

禁止增加 `explorationShare`、探索令牌桶、定时业务探索或无样本账号轮流接单。后台健康检查和恢复探针只验证基础可用性，不属于业务质量探索，也不进入业务热质量统计。

## 5. 热质量模型

本文“热质量”默认只表示网关可自动验证的传输完成可靠性与首字速度，不等于上游业务可用性。未配置高级规则时，一个持续快速返回完整 HTTP `401 / 429 / 5xx` 或任意错误正文的账号仍会被视为“传输完整且首字快”；这是“不依赖不可控上游响应语义”的刻意边界，不是遗漏。账户所有者若希望这些响应影响切号、Key 或账号状态，必须在前端高级设置中显式配置规则。

### 5.1 存储边界

- performance：Redis runtime state 是跨节点事实源。
- standalone：单 server 进程内存保存等价状态。
- Redis 不可用时，performance 模式不能静默退回本机内存伪装成共享状态；按运行态基础设施故障处理。
- 热质量桶全部带 TTL，可以丢失并从后续真实流量重新建立；活动电路状态也带 TTL，但不得靠自然过期直接恢复为 `CLOSED`，必须满足第 7 节的 TTL 与 due 索引不变量。
- 现有数据库 `account_quality_scores` 可以继续用于页面、报表和历史排障，但不得成为本设计的实时调度读取依赖。
- standalone 内存实现必须有固定最大条目数和到期清理；建议初始上限 10000 个热质量 key，达到上限时拒绝创建新的细分 key 并退化到已有协议级桶，不能无界增长。
- performance Redis 必须记录 key 创建拒绝、高基数退化、内存压力和写入失败指标；TTL 不是缺少基数保护的替代品。

### 5.2 作用域

自动短电路只消费网关本地可验证的 transport 事实；用户在前端高级设置里显式配置的账户错误策略或响应检查策略可以额外产生用户授权的电路动作。两类输入都先确定作用域，再选择电路 key：

| 作用域 | 典型故障 | 电路范围 |
| --- | --- | --- |
| `key` | 本地 Key 解密 / 装配失败，或用户显式高级策略指定 Key 动作 | 当前 `keyFingerprint` |
| `protocol_model` | 当前请求形态发生 transport、timeout、读取中断或未完成响应 | `accountRuntimeKey + protocolProfile + requestLane + modelFamily` |
| `account` | 多个独立 `protocol_model` 作用域均发生本地可验证失败，或用户显式高级策略指定账号动作 | `accountRuntimeKey`，跨请求、IP、会话和路由实例共享 |
| `upstream_bucket` | 多个不同账号在同一代理 / Base URL / provider 出现 transport、timeout 或读取中断 | 复用现有上游桶健康作用域 |

只有多个独立本地 transport 事实或用户显式高级策略明确指定账号动作时，才写全局 `accountRuntimeKey` 电路。单模型、单 endpoint 或单 Key 故障不能因为共享物理账号而阻断其他模型或另一条快速路由中的健康能力。任何上游状态码、响应头、错误码或正文都不能作为系统自动扩大作用域的证据。

自动从 `protocol_model` 扩大到 `account` 的首轮固定条件为：同一 `accountRuntimeKey` 在 60 秒内至少有 2 个不同 `protocol_model` 作用域分别完成 confirmation 并进入 `OPEN`，合计至少 3 次本地可验证失败，且从最早一条升级证据之后没有该账号的 transport 完整响应。升级证据使用有界 scope set、事件 ID 去重和 account generation 的原子 CAS；任一新的完整响应清除尚未提交的聚合升级证据，但不提前关闭已经建立的 account 电路。实现不得自行把单作用域的并发失败扩大成账号级故障。

热质量按以下维度隔离：

```text
accountRuntimeKey
+ protocolProfile
+ requestLane
+ modelFamily
```

- `requestLane` 至少区分 `text` 与 `image`，避免合法长耗时图像请求污染文本速度。
- `modelFamily` 只能使用模型目录或供应商驱动提供的有界规范化集合；无法稳定归类时统一退化为协议级 `unknown` 桶，不保存原始任意模型字符串形成无界 key。
- 账户优先级、超级优先和备用来自当前分组绑定，不进入质量 key。
- 快速模式 `latency_degraded` 继续使用 `systemAccountId + routeStrategyId + groupId + accountRuntimeKey`，不与通用热质量状态合并。

### 5.3 一分钟环形桶

每个热质量 key 保存最近 30 个一分钟桶，整体 TTL 建议 40 分钟。每个桶只记录有界字段：

```text
attempts
completedResponses
localTransportFailures
timeouts
readInterruptions
incompleteResponses
explicitPolicyFailures
unknownOutcomes
clientCancellations
firstByteSampleCount
firstByteSumMs
firstByteHistogram
lastCompletedAtMs
lastFailureAtMs
```

`firstByteHistogram` 使用固定桶而不是保存原始样本，例如：

```text
<=1s, <=2s, <=5s, <=10s, <=20s, <=30s, <=60s, >60s
```

Redis 更新必须使用原子脚本或等价 Store 操作完成“校验 attemptId 终态幂等键、选择当前分钟桶、递增计数、刷新 TTL、清理过期桶”，禁止每次 attempt 使用 `GET -> 修改 -> SET` 丢失并发增量。每个 `attemptId` 只能提交一次终态；短 TTL 去重键或等价 CAS 必须覆盖 finalizer 重入、队列重放和双 server 重复提交。

逻辑终态键为 `gateway-attempt-terminal:v1:{attemptId}`，保存有界 `terminalOutcomeId / outcomeClass / failureScope / source / createdAtMs`，TTL 至少 1 小时；不得保存上游状态码、响应头或正文。后续质量、电路和 usage 投影都以 `terminalOutcomeId` 去重。

`attempts` 在真实上游派发成功后递增，用于审计实际负载。同一个 attempt 在可靠性计数中只能有一个终态：命中用户显式高级规则的失败动作优先计入 `explicitPolicyFailures`，不再同时计入 `completedResponses`；未命中用户高级规则的完整响应计入 `completedResponses`；形成完整响应前的本地失败计入对应本地失败字段；客户端取消、租约过期但结论未知或进程终止等情况计入 `unknownOutcomes / clientCancellations`。未知和客户端取消不进入完成率分母。这项互斥只服务于用户授权后的质量语义，不允许系统自行解析完整响应。

`localTransportFailures` 是本地失败终态总数，`timeouts / readInterruptions / incompleteResponses` 只是它的互斥诊断子类，计算 `qualityAttempts` 时不得重复相加。

### 5.4 5/10/30 分钟计算

最近 5、10、30 分钟分别计算带先验传输完成率：

```text
qualityAttempts = completedResponses + localTransportFailures + explicitPolicyFailures
adjustedCompletionRate = (completedResponses + 2) / (qualityAttempts + 4)
```

综合可靠性建议：

```text
reliability = completionRate5m * 0.60 + completionRate10m * 0.30 + completionRate30m * 0.10
confidence = min(1, qualityAttempts10m / 10)
effectiveReliability = 0.5 + (reliability - 0.5) * confidence
```

先验和置信度用于防止一个新账号凭一次快速完成直接登顶，也避免无样本账号被默认判死。推荐初始分级：

| 条件 | 可靠性等级 |
| --- | --- |
| 最近 10 分钟有效样本少于 3 | `unknown` |
| 最近 5 分钟样本至少 3，且 `effectiveReliability >= 0.85` | `healthy` |
| 最近 5 分钟样本至少 3，且 `effectiveReliability < 0.60` | `unhealthy` |
| 其他 | `uncertain` |

可靠性分级只影响同账户配置层内排序；本地可验证的 transport 即时失败是否短暂阻断当前作用域由账户电路决定，不等待统计阈值。这里的“完成率”只表示网关完成了传输，不表示系统认可上游业务内容。

### 5.5 速度信号

热速度只使用真实网关请求的首字样本，输出：

- 5 分钟首字 EWMA；
- 10 分钟固定直方图近似 P95；
- `fast / normal / slow / unknown` 速度等级。

速度信号不能覆盖可靠性、电路和快速模式的 `latency_degraded`。普通模式仅在同一账户配置层、相同可靠性等级内用速度减少不必要等待；快速模式仍由路由自己的阈值、慢样本窗口和恢复状态决定是否跨层覆盖。

## 6. 自动输入与用户高级策略

### 6.1 系统自动机制只认本地可验证事实

系统自动热质量、短电路和切号只能消费以下事实：

- 已经选中账号并准备发起真实 HTTP(S) 请求，但连接、代理建立或请求发送在形成完整上游响应前失败；
- 上游在形成完整响应前达到当前 lane 的 timeout；
- 非客户端取消导致的读取中断、EOF、连接重置或未完成响应；
- 精确流式链路在下游语义尚未提交时发生本地可验证的传输不完整；
- 前序账号发生上述本地失败，后续账号对同一请求形成完整响应，证明本次请求可以通过切号救回；
- 后台探针执行器只返回“HTTP framing 或流式传输完整 / 传输未完成 / 结论未知”的有界结果，热电路不读取其上游状态码或错误正文。

自动机制不维护任何上游状态码、错误码、错误类型、响应头、正文、文案、完成状态或供应商特例白名单。自动退避固定使用本文的本地退避序列，不读取上游 `Retry-After` 或类似提示。

### 6.2 任何完整上游响应默认不进入自动处理

只要通用客户端收到完整上游 HTTP 响应，且没有命中用户显式高级规则，无论状态码、响应头和正文是什么，系统都视为“传输已完成”：

- 原样转发状态、允许的响应头和正文；
- 不自动重试或切号；
- 不轮换账户内 Key；
- 不写 Key、`protocol_model` 或账户短电路；
- 不累计热质量失败，只累计 `completedResponses` 和实际首字时间；
- 不据此写持久账户状态或后台失败结论。

精确客户端画像可以继续完成协议兼容和向客户端渲染协议事件，但不能依据完整响应语义自动重试、切号、轮换 Key、写电路或写账户状态。现有任何 `system_default` 响应检查、默认错误类型表或内置客户端重试规则都不构成用户授权；实施本文时必须停用其账户调度副作用，或改造成由账户所有者在前端显式启用的模板。

### 6.3 用户前端高级设置是唯一响应语义入口

只有账户所有者在前端高级设置中显式配置账户错误处理策略或响应检查策略时，状态码、响应头、错误码、正文、JSON 路径、SSE 事件或完成状态才可以作为该规则的输入。

- 系统只执行规则匹配后用户明确选择的 `retry_next`、Key 避让、账号 TTL 避让、`temporary_unavailable`、`rate_limited`、`error` 或其他已声明动作。
- 规则必须明确适用的客户端画像、协议、账号 / Key 作用域和动作；未配置的响应保持完全不透明。
- 用户规则命中产生的短电路事件标记 `source = explicit_policy`，与系统自动 transport 事件分开统计。
- 用户显式 TTL 和持久状态只能按配置到期、后台复测或人工操作恢复；自动 transport 成功不能提前解除。

统一终态裁决顺序为：客户端取消或结论未知；用户显式高级规则动作；未命中规则的 transport 完整；形成完整响应前的本地失败。系统先以 `attemptId` 原子创建唯一 `attempt-terminal` 记录，选定其中一个分支；热质量、电路和使用记录再以同一 `terminalOutcomeId` 幂等投影，不要求跨多个 Redis key 做脆弱的伪事务。显式失败动作优先于“传输完整”，因此不会先进入 `RECOVERING` 再被同一结果重新打开；未配置规则时则绝不能跳过“完整响应透明”边界。

### 6.4 多 API Key

- 系统自动机制不能因为任意完整上游响应轮换或摘除当前 Key。
- selected Key 在本地解密、装配或读取运行态时失败，可以建立 Key 级本地电路并尝试账户内下一个可执行 Key。
- transport、timeout 或读取中断默认进入当前 `protocol_model` 或 `upstream_bucket` 作用域，不因使用了某个 Key 就自动归咎该 Key。
- 只有用户显式高级策略指定 Key 动作时，才允许根据上游响应内容建立 Key 电路。
- 所有 Key 都因本地可验证原因不可执行，或用户显式策略指定账号级动作时，才推进账户级电路。
- Key、`protocol_model` 和账户电路都使用 generation 与租约，旧结果不能覆盖新配置或新凭据状态。

## 7. 账户电路状态机

```mermaid
stateDiagram-v2
  [*] --> CLOSED
  CLOSED --> SUSPECT: 本地可验证 transport 失败或显式策略动作
  SUSPECT --> RECOVERING: 确认租约完整成功
  SUSPECT --> OPEN: 确认 attempt 失败或超时
  OPEN --> HALF_OPEN: openUntil 到期
  HALF_OPEN --> RECOVERING: 半开租约完整成功
  HALF_OPEN --> OPEN: 半开失败/超时
  RECOVERING --> CLOSED: 连续完整成功达到阈值
  RECOVERING --> OPEN: 本地可验证 transport 失败或显式策略动作
```

### 7.1 状态语义

| 状态 | 普通请求 | 租约请求 | 说明 |
| --- | --- | --- | --- |
| `CLOSED` | 正常候选 | 不需要 | 没有共享故障阻断 |
| `SUSPECT` | 排除并换号 | 仅一个 confirmation lease | 首次本地可验证失败后的即时止损 |
| `OPEN` | 排除并换号 | 不允许 | 等待短退避到期 |
| `HALF_OPEN` | 排除并换号 | 仅一个 probe lease | 到期后的单飞验证 |
| `RECOVERING` | 普通请求排除并换号 | 仅一个 matching-generation recovery canary lease | 防止偶尔成功一次立即恢复全部流量 |

只有未命中用户显式失败动作，并且 transport 层形成完整 HTTP 响应，或 SSE 在未发生读取中断的情况下正常结束，才算账户恢复成功；判定过程不得检查 HTTP 状态码、响应头或业务正文。仅收到响应头、首字节、部分 token、心跳，或请求被客户端取消，都不能关闭电路。

`RECOVERING` 不允许普通请求无租约进入。每次只能有一个匹配当前 generation 的 recovery canary lease；canary 完整成功后原子递增 `recoveringSuccesses`，等待建议 3 秒恢复间隔后再允许下一个独立 lease。连续 3 个不同 lease 完整成功才进入 `CLOSED`。任意本地可验证 transport 失败或用户显式策略失败动作立即进入 `OPEN` 并增加退避；取消、没有形成真实 attempt 或旧 generation 结果只释放租约，不计成功或失败。后台恢复探针和受控真实请求都必须先取得该 lease，且恢复探针不进入业务热质量统计。

### 7.2 Redis 状态

建议逻辑 key：

```text
gateway-circuit:v1:account:{accountRuntimeKey}
gateway-circuit:v1:key:{accountRuntimeKey}:{keyFingerprint}
gateway-circuit:v1:protocol-model:{accountRuntimeKey}:{protocolProfile}:{requestLane}:{modelFamily}
gateway-policy-block:v1:{policyScope}
gateway-hot-quality:v1:{qualityScope}
```

电路值至少包含：

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

状态获取、租约获取、结果提交和 generation 清理必须使用原子 CAS。一次脚本或等价事务同时核对 `state + generation + leaseId + dispatchRevision`，更新 `openUntilMs / leaseUntilMs / attemptHardDeadlineMs / transitionId`、刷新 TTL，并维护 due 索引；不得分步留下 state 与索引不一致。多 server 同时看到到期状态时只能有一个请求从 `OPEN` 取得 `HALF_OPEN` 租约。CAS 冲突、租约争抢、旧结果丢弃和幂等重复提交都必须有独立指标和双节点回归。

confirmation、half-open 和 recovery canary 的初始租约必须一次覆盖该 attempt 的 hard lifetime 加安全余量，`attemptHardDeadlineMs` 之前不得因心跳抖动重新发放同 generation 租约；心跳只能延长租约，不能缩短排他窗口。这样即使事件循环暂停或 Redis 短时不可用，也不会产生第二个昂贵上游验证请求。

活动状态不能依赖 Redis 自然过期来恢复：电路 key 的 TTL 必须至少覆盖 `max(openUntilMs, leaseUntilMs, RECOVERING 最迟可完成时间) + 10 分钟`，并在每次原子转换时刷新。缺失 key 只有在 due 索引也没有成员、没有用户策略阻断且没有在途 fencing 记录时才能按 `CLOSED` 处理。后台任务必须惰性清理“索引有成员但状态缺失”和“状态存在但索引缺失”的孤儿，并记录修复指标。

用户显式 TTL / 持久阻断与自动 transport 电路使用独立状态：`gateway-policy-block` 只由用户规则动作、规则到期、后台复测或人工操作修改；自动电路的 canary 成功只能关闭自动层。最终可执行性取“用户策略阻断层允许”与“自动电路层允许”的交集，任何自动 `CLOSED` 都不能覆盖用户 TTL。

显式策略阻断值至少包含 `blocked / policyId / policyGeneration / expiresAtMs / dispatchRevision / updatedAtMs / source=explicit_policy`。策略服务是唯一 owner；设置、续期、到期清理和人工解除都用 `policyGeneration + dispatchRevision` CAS，并维护独立 TTL / due 索引。自动电路脚本只能读取该层，不能修改、清除或把其缺失解释为用户规则已解除。

账号凭据、代理、协议档案、模型映射或影响真实派发的配置变化必须原子递增 `dispatchRevision`、递增 generation 并清理旧 due 索引。旧 revision 的在途结果只能释放本地资源，不能提交 `OPEN / RECOVERING / CLOSED`。

`lastFailureClass` 只允许保存有界的本地类别，例如 `connect_failed / timeout_before_complete / read_interrupted / incomplete_response / explicit_policy`；不得保存或派生上游状态码、错误码、正文摘要或供应商文案。

### 7.3 推荐初始参数

| 参数 | 建议值 |
| --- | --- |
| confirmation 首字 deadline | 使用本次请求统一的 `firstByteDeadlineMs` |
| 同账号 confirmation 重试 | 最多 1 次 |
| 半开同时请求 | 1 |
| 短退避序列 | `3s -> 5s -> 10s -> 30s -> 60s` |
| 自动运行态最大退避 | 5 分钟 |
| `RECOVERING` 并发 | 1 |
| recovery canary 间隔 | 3 秒 |
| `RECOVERING -> CLOSED` | 连续 3 次完整成功 |
| `RECOVERING` 失败 | 立即重新 `OPEN` 并增加退避 |

用户显式策略建立的 TTL、持久 `temporary_unavailable / rate_limited / error` 和后台持久冷却复测继续使用现有语义；短账户电路是请求热路径保护，不替代账户所有者的显式策略。

## 8. 单飞确认与并发换赛道

一个 `CLOSED` 账号首次出现本地可验证 transport 失败，或命中用户显式高级策略的失败动作时，失败请求原子尝试：

1. 把电路从 `CLOSED` 转为 `SUSPECT`，递增 generation。
2. 获取该 generation 的 confirmation lease。
3. 租约持有者最多再确认一次同账号；确认 attempt 使用更短的首字 deadline。初始租约一次覆盖真实 attempt 的 hard lifetime 与安全余量；心跳只允许延长。账号仍有匹配 generation 且未超过 `attemptHardDeadlineMs` 的在途 attempt 时，禁止其他请求重新取得确认租约。
4. 其他请求读取到 `SUSPECT` 后，不等待租约结果，立即排除该账号并在当前分组重新选号。

故障响应返回前已经发往该账号的在途请求不能撤回；本设计保证失败被观察后的新请求不再继续灌入。

confirmation 重试只允许发生在下游语义尚未提交时。confirmation 与普通 attempt 共用本次请求的 `firstByteDeadlineMs`，不再设置 5 秒 confirmation 专属 deadline，也不被协调等待预算改写。confirmation 是真实上游 attempt，其 fetch 与首字观察期间 `routeCoordinationBudget` 暂停；如果当前请求已经没有首字前 attempt 预算，租约持有者直接换号，不强行确认。首字截止只限制首字前等待：一旦收到可见首字，后续响应读取继续使用当前 lane timeout 和流式 idle timeout；只有 HTTP framing 完整，或 SSE 在没有读取中断的情况下正常结束，才提交恢复结果，且不得检查业务正文。

confirmation 租约到期但没有形成真实上游 attempt、或租约请求被客户端取消时，结论为未知：只释放当前租约并允许下一次受控确认，不能把“没有得到结论”伪造成账号失败并推进退避。

半开租约和 confirmation 租约都禁止同账号原地多轮重试。租约失败只提交一次状态转换和一次结构化日志，其他因电路被排除的请求不重复写上游失败日志。

## 9. 候选分层与请求预算

### 9.1 账户配置层

基础层级继续使用：

```text
modelMatchRank
-> fallbackEnabled
-> superPriorityEnabled
-> priority
```

热质量只允许重排完整层级相同的账号。`unknown` 账号不获得负分，按绑定创建顺序和账号 ID 保持稳定顺序。

### 9.2 有界优先级逃逸

普通失败不能让客户端依次等待当前层所有异常账号。每个请求维护：

```text
attemptedProtocolModelKeys
attemptedAccountRuntimeKeys
attemptedKeyFingerprints
sameAccountConfirmationBudget
priorityTierFailureCount
routeCoordinationBudget
```

推荐初始值：

| 预算 | 统一标准 |
| --- | --- |
| 普通同账号原地重试 | 0；仅允许一个 matching-generation confirmation lease |
| confirmation lease 持有者 | 1 |
| 同优先级层本地可验证失败数 | 2 |
| 首字观测 | `normalRoutingConfig.firstByteDeadlineMs`，普通模式和快速模式完全共用；默认 10 秒，范围 10–60 秒 |
| 路由协调等待 | 一个 `routeCoordinationBudget`，初始最多 3 秒 |

同层失败数达到预算后，当前请求允许进入下一账户配置层。该行为只影响当前请求，不重写账户优先级。已经 `SUSPECT / OPEN / HALF_OPEN` 的账号不计为本请求真实失败，也不消耗上游 attempt；它们直接从普通候选中移除。

如果当前层所有账号已经处于 `SUSPECT / OPEN / HALF_OPEN`，候选扫描直接进入下一账户配置层，不等待、不重复扫描，也不人为补足 `priorityTierFailureCount`。同一个 `accountRuntimeKey + protocolProfile + requestLane + modelFamily` 在 `attemptedProtocolModelKeys` 中只允许出现一次；只有合法 confirmation / half-open lease 可以消费一次例外。只有 account 电路已经建立，或用户显式 account 级动作命中时，才把 `accountRuntimeKey` 加入全局 `attemptedAccountRuntimeKeys`。

普通路由在请求开始时只解析一次 `normalRoutingConfig.firstByteDeadlineMs`，普通模式、快速模式、confirmation 和同请求后续账号 attempt 都复用这个值。建议默认 10 秒、允许 10–60 秒，且始终不超过当前 lane first-response timeout。现有 `speedFirstConfig.firstByteThresholdMs` 作为迁移兼容别名读取；目标结构写入公共 `normalRoutingConfig.firstByteDeadlineMs`，迁移完成后删除速度模式专属字段，禁止两个字段同时生效。image 等合法长耗时 lane 继续按 lane 规则决定是否启用首字观测，但一旦启用，也只能产生同一种首字事件。

统一首字协议固定为：真实上游派发时启动一次计时；非流式以首个 body 字节、流式以首个可见语义 chunk 为首字；响应头、SSE heartbeat、空事件和内部缓冲不算；每个 attempt 只产生一次 `observed / deadline_reached / cancelled / unknown` 结果。任何账户层、响应处理层或快速模式都不能再创建第二个首字 timer。

两种模式只在统一结果后的路由动作上不同：`cost_first` 在截止前严格保持超级优先、优先级和会话亲和；截止时仍无首字则由路由层形成一次 `timeout_before_complete`，有替代候选时切号，没有替代候选时结束当前 attempt，不再继续等另一套 120 秒首字 timeout。`speed_first` 在同一截止事件上先记录慢样本，是否切号仍由 `slowTriggerCount`、`latency_degraded`、下游提交状态和请求内切号上限裁决。快速模式不能改变首字 deadline，只能决定事件后的动作。

尝试身份按失败作用域更新：本地 Key 准备失败或用户显式 Key 级动作只加入 `attemptedKeyFingerprints`，允许同一账号尝试另一个未尝试 Key；真实 transport 失败首先加入当前 `attemptedProtocolModelKey`，保持 model / lane / protocol 隔离；只有 account 电路升级或用户显式 account 级动作才加入 `attemptedAccountRuntimeKeys`，从而阻止该物理账号通过另一授权实例或分组绑定再次进入同一请求。完整响应若没有用户显式动作则直接结束请求，不产生新的尝试身份。

`routeCoordinationBudget` 是普通模式和快速模式共享的单一账户协调预算，统一累计：故障 attempt 结束后的租约等待、confirmation 结果后的重新选号、优先级层切换和零可派发等待。活跃上游 fetch、统一首字观察和响应读取期间暂停；不再存在“故障后切号等待”和“零可派发等待”两套预算。普通模式和快速模式都消费同一个首字观测事件；快速模式只额外使用 `slowTriggerCount`、`maxFirstByteRetriesPerRequest` 和未提交安全窗口决定动作。账户层或快速模式都不能创建第二个首字或协调等待计时器。

3 秒统一协调预算不替代、不缩短现有跨后备、半开、重新选号共享的 `ServerRetryBudget`。协调预算到期时，如果存在可恢复账号，账户层返回 `temporarily_blocked` 及 `earliestRetryAtMs / confirmationInFlight`；如果只是本请求已经没有未尝试候选，则返回 `request_exhausted`；两者都不能伪装成 `hard_exhausted`。路由协调器再依据当前路由模式、后续分组和共享 `ServerRetryBudget` 决定等待、fallback 或最终交还客户端。

`ServerRetryBudget` 每个网关请求只创建一次，并且只有路由协调器能够启动、暂停和消费它。派发真实 attempt 前暂停；`routeCoordinationBudget` 运行时同步受它约束；跨账号、账户配置层、分组、主备和轮询环均不得重置。单次等待上限固定为 `min(routeCoordinationBudgetRemaining, ServerRetryBudgetRemaining, max(0, earliestRetryAtMs - now))`。预算耗尽后的最终行为仍由当前路由模式决定，账户层不得自行构造最终错误。

预算状态至少包含 `requestId / budgetId / version / remainingMs / activeSinceMs / lastWaitToken`。同一请求的异步分支只能由 route coordinator 使用 `version` CAS 启停；每次等待携带幂等 `waitToken`，重复唤醒、取消回调或 fallback 回调不能重复扣减。账户层只能读取剩余值，不能创建、续期或消费预算。

预算值是首轮建议，实施前需要通过 Mock AI 的 5 个高优先级异常账号、5 个低优先级健康账号场景验证，并根据真实首字分布调整。故障已经形成后，协调等待不得重新放大到客户端自身重试之前无法完成切号的分钟级等待。

## 10. 普通模式行为

普通模式即普通路由 `cost_first`：

1. 接收路由策略已经选定的普通路由唯一分组，不参与分组选择。
2. 在该分组内执行账户可执行性，以及与当前请求匹配的 Key、`protocol_model`、account 和 `upstream_bucket` 电路过滤。
3. 按非备用、超级优先、`priority` 建立账户配置层。
4. 在当前最高可用层内按 `healthy -> uncertain -> unknown -> unhealthy` 排序。
5. 相同可靠性等级内使用热速度、会话亲和和稳定顺序。
6. 当前账号发生本地可验证 transport 失败或命中用户显式策略动作后触发单飞确认；其他请求立即选择同层后续账号。
7. 当前请求同层失败预算耗尽后进入下一账户层；所有账户层不可承接时向路由层报告当前分组耗尽，不自行选择其他分组。
8. 前层账号保持健康时，不访问后层账号，也不为获取质量样本主动探索。

质量差本身不允许一个 `CLOSED` 高优先级账号被低优先级账号长期越过；只有账户电路、硬不可用、容量不可承接或当前请求失败预算能够触发跨层逃逸。

## 11. 快速模式行为

快速模式即普通路由 `speed_first`。路由策略是最高业务调度层，快速模式的速度目标高于任何 AI 账户偏好；账户可执行性只负责排除无法执行该路由结果的账号。本设计不能削弱现有快速模式。

固定流程：

1. 接收路由策略已经选定的普通路由唯一分组和 `speed_first` 目标，不参与分组或模式选择。
2. 在该分组内通过账户可执行性，以及与当前请求匹配的 Key、`protocol_model`、account 和 `upstream_bucket` 电路移除实际上不能执行的账号。
3. 对剩余可执行候选应用路由层 `latency_degraded`：未降级账号整体优先于已确认慢账号。
4. 未降级候选之间、降级候选之间再按账户配置层和同层热质量排序。
5. 当前 attempt 首字超过软阈值时，仍按快速模式配置累计慢样本；未达到 `slowTriggerCount` 只观察，不因热质量直接中断。
6. 已确认慢、下游未提交、存在未降级硬可承接候选且未超过 `maxFirstByteRetriesPerRequest` 时，快速模式可以跨超级优先、账号优先级和备用层切到未降级账号。
7. 快速模式切号目标不能被热质量重新限制在原账户优先级层，也不能被会话亲和拉回 `latency_degraded` 账号。
8. `latency_degraded` 清理后，后续请求立即回到账户配置和热质量排序；替补账号不能形成长期更强亲和。

账户故障与速度慢必须保持独立：

- `SUSPECT / OPEN / HALF_OPEN` 表示账号当前不能接受普通业务流量，普通模式和快速模式都必须避让。
- `latency_degraded` 表示账号可用但违反当前快速路由的速度目标，只影响启用该快速模式的路由范围。
- 热质量中的速度等级只作为同层细粒度信号，不能代替快速模式的慢样本状态机。
- 快速模式确认慢后的安全切号不消耗账户 confirmation 重试预算；本地可验证 transport 失败或用户显式策略动作也不需要先等待慢样本阈值。

## 12. 流式与客户端边界

- 下游语义提交前可以排除失败账号并重新选号。
- SSE 已写出可见语义内容后，不允许切号拼接另一个账号输出，也不允许重新发送工具调用。
- SSE 心跳、仅响应头或内部缓冲未提交不算完整恢复成功，是否允许切号继续沿用 `downstreamCommitState`。
- 非流式响应只有在完整 body 读取结束后才提交下游；读取中断前不把部分 body 当成成功。
- confirmation / half-open 的流式首个可见语义 chunk 按正常链路立即提交并转发，不为等待完整恢复结果缓存整条流；提交后若读取中断，当前客户端只能结束断流，同时为后续请求重新打开对应电路，不能再切号拼接。
- 精确 Codex、Claude Code、Gemini CLI 客户端可以保持协议事件渲染和客户端侧重试提示，但服务端不能依据完整响应语义自动重试、切号、轮 Key 或写账户状态。
- `generic_*` 的任何完整上游响应继续透明转发，不能建立自动账户电路；只有形成完整响应前的本地 transport 失败，或用户显式高级策略动作，才能建立对应作用域的电路。
- 客户端取消只释放租约和并发，不把账号标为失败；confirmation / half-open 请求被客户端取消时结论为未知，释放当前租约并等待下一次受控验证。

## 13. 与现有机制的关系

| 现有机制 | 保留方式 |
| --- | --- |
| 显式账户错误策略 | 用户配置优先；可直接 `retry_next / cooldown / disable`，不被自动热质量覆盖 |
| 响应检查策略 | 只有用户在前端显式配置时才能读取响应语义；其明确 runtime avoidance 高于普通候选排序 |
| 账户内多 Key 隔离 | 仅本地 Key 准备失败或用户显式 Key 动作建立 Key 电路；所有 Key 不可执行才推进账户层 |
| IP 级账号回避 | 继续作为来源局部辅助，不承担全局异常账号保护 |
| 上游桶健康 | 继续识别代理 / Base URL / provider 公共故障，不替代单账号电路 |
| `recovery_wait / precheck_pending` | 继续负责后台确认和持久状态升级；短账户电路先处理秒级止损 |
| 持久冷却复测 | 继续恢复 `temporary_unavailable / rate_limited`，不进入业务热质量 |
| 历史账户质量统计 | 继续用于展示和分析，不进入实时热路径 |
| 快速模式 `latency_degraded` | 保持路由层最高偏好覆盖，热质量不得改变其跨账户偏好的语义 |

### 13.1 已知现行冲突与迁移要求

本文是目标设计，不代表现行网关已经满足边界。当前 `failure-dispatch`、`upstream-dispatch`、流式结束重试决策和系统默认响应检查仍存在依据完整非 2xx 响应、错误类型或正文自动同号重试、切账号、轮换 Key、写上游桶或写运行态的路径；这些路径与本文及系统核心原则冲突。

实施时必须以行为验收而不是保留旧分支为准：

- 未配置前端高级规则的完整响应只透明转发，现有非 2xx 自动同号重试、自动 `tryNextApiKey`、自动账号回避和上游桶失败写入全部移除。
- `system_default` Codex / Gemini CLI 等响应语义规则不得继续产生服务端账户调度副作用；可以保留协议渲染，或作为用户显式启用的模板。
- 旧的 `same-account retry budget` 不再参与普通候选扫描，只保留 matching-generation confirmation lease 一次验证。
- 现有 preparation / dispatch / routes 多层直接调用 fallback 的路径先收敛为统一 route-coordinator result contract，再接入四类账户结果；否则不能保证跨组预算和尝试集合不被重置。
- 迁移必须增加“完整任意状态响应不自动切号”和“仅用户显式规则允许响应语义动作”的反向回归，防止旧默认规则复活。

短账户电路成功恢复不能提前清除用户显式 TTL，也不能直接把持久异常账号改为 `active`。持久状态恢复仍由后台健康检查、冷却复测或人工操作负责。

显式策略与短电路的精确关系：

- `retry_next` 只是不写持久账户状态；规则命中后可以按用户显式选择建立易失短电路，避免其他请求继续灌入。未配置规则的完整响应不能触发该行为。
- `cooldown / disable` 由显式策略推进持久状态，账户短电路只承担状态写回完成前的即时止损，不能覆盖或撤销持久动作。
- 显式策略 TTL 未到期时，短电路 canary 或普通成功不能提前解除该 TTL。
- 所有路由模式都可以消费 Key / protocol-model / account 电路给出的账户可执行性，但只能在路由已选分组内过滤候选。weighted、round-robin、failover、hybrid 等路由仍由各自协调器解释 `dispatchable / temporarily_blocked / request_exhausted / hard_exhausted`，账户层不改变分组顺序和策略算法。

## 14. 日志与指标

建议新增或统一以下事件：

- `gateway_account_circuit_suspected`
- `gateway_account_confirmation_lease_acquired`
- `gateway_account_confirmation_result`
- `gateway_account_circuit_opened`
- `gateway_account_half_open_lease_acquired`
- `gateway_account_circuit_recovering`
- `gateway_account_circuit_closed`
- `gateway_account_circuit_dispatch_skipped`
- `gateway_priority_tier_escape`
- `gateway_hot_quality_order_applied`

日志去噪原则：

- 真实上游 attempt 继续保留使用记录和审计事实。
- 电路打开后被本地排除的请求不伪造失败使用记录，也不重复输出上游失败 `warn`。
- 相同账号、generation 和失败摘要只在状态转换时输出结构化告警；普通 skip 进入计数指标或 `debug`。
- 首次状态转换前已经并发在途的多个失败 attempt 仍分别保留 usage / audit，但运行日志按 `accountRuntimeKey + generation + failureClass` 聚合，只在首次转换输出一条 `warn`，后续增加聚合计数。现有 response failure 与 usage failure 的双 `warn` 必须收敛为一个状态转换告警，另一个降为结构化审计或 `debug`。
- 每个网关请求结束时记录一次有界调度摘要，包括尝试账号、跳过原因、层级逃逸、最终账号和总首字前等待；不复制完整错误正文。

核心指标：

```text
account_circuit_open_total
account_confirmation_inflight
account_half_open_inflight
account_circuit_skip_total
account_circuit_cas_conflict_total
account_circuit_lease_contention_total
account_circuit_stale_result_total
account_circuit_idempotent_replay_total
account_circuit_revision_mismatch_total
account_circuit_orphan_repair_total
hot_quality_terminal_dedup_total
priority_tier_escape_total
gateway_accounts_attempted_per_request
gateway_precommit_switch_duration_ms
gateway_request_rescued_by_account_switch_total
gateway_hot_quality_state_count
```

## 15. 验证矩阵

实施时至少覆盖：

1. 同一坏账号被 20 个不同会话、同 IP 同时命中：首次状态转换前已经派发的在途 attempt 允许各自结束；转换完成后只能新增一个 confirmation，其余新请求全部换号，不能把“首轮在途数”误验收为零。
2. 不同 IP、不同 API Key 同时命中同一物理账号：账户电路按正确资源键共享，不依赖 IP 回避。
3. 5 个 P1 全失败、5 个 P2 健康：每个请求在 P1 有界失败后进入 P2，不重复多轮扫描 P1。
4. P1 长期健康：P2 和备用账号不产生业务探索流量。
5. 账号最近 100 次只有 2 次形成完整响应但首字很快：完成可靠性先于速度，不能成为同层首选。
6. 无样本后排账号首次因前层失败被启用：按稳定配置顺序选择，不能因 `unknown` 被判不可用。
7. 账号好 10 分钟后突然持续超时：首次响应完成前的本地 timeout 立即建立 `SUSPECT`，不等待 5 分钟质量窗口。
8. 半开偶尔成功一次后再次失败：先进入 `RECOVERING`，失败立即重开，不能一次成功清空历史。
9. Redis 双 server 同时抢 confirmation / half-open：同一 generation 只有一个租约成功。
10. 单 Key 本地解密或装配失败但账户内还有可执行 Key：切 Key，不打开账户电路；任意完整 HTTP 响应都不能触发自动 Key 轮换。
11. 通用客户端收到任意完整 HTTP 响应：不论状态码和正文均透明转发，不自动切号或写电路；形成完整响应前的连接失败、timeout 或读取中断才触发对应作用域的保护。
12. 普通模式：健康高优先级账号继续优先，质量只重排同层账号。
13. 快速模式：已确认慢的超级优先账号被未降级低优先级或备用账号越过；热质量和亲和不能拉回慢账号。
14. 快速模式首次单次慢但未确认：只记录慢样本，不因本设计新增的热速度信号强制切号。
15. 快速模式账号在完整响应前发生本地 transport 失败：立即走账户电路和切号，不等待 `slowTriggerCount`；完整 HTTP 响应仍透明转发。
16. SSE 首字前失败可以切号；首字后失败绝不透明重放。
17. 客户端取消 confirmation / half-open：释放租约，不累计账号失败结论。
18. `RECOVERING` 连续三个不同 canary lease 完整成功后才关闭；普通请求不能抢占 canary，取消不计结果。
19. cost-first 的 model A 在完整响应前发生 transport 失败时只打开 `protocol_model` 电路，speed-first 的 model B 仍可使用同账号；多个独立模型作用域均发生本地 transport 失败，或用户显式指定账号级动作时，才允许两个路由都过滤该账号。
20. 普通模式和快速模式均使用同一个 3 秒 `routeCoordinationBudget`：存在可恢复账号时返回 `temporarily_blocked`，仅本请求无未尝试候选时返回 `request_exhausted`；不覆盖共享 `ServerRetryBudget`，后续分组仍由路由协调器决定。
21. 10 个 P1 同时 `OPEN` 时不重复扫描 P1；到期后同 generation 只允许一个半开租约。
22. 大量未知模型名统一进入有界 `unknown` 桶；内存 / Redis 高基数保护和退化指标生效。
23. Redis 不可用：performance 模式记录基础设施故障并保守处理，不回退本机共享假象。
24. 同一个 confirmation / half-open 结果被重复提交：CAS 只生效一次，due 索引与状态一致，并增加幂等重放指标。
25. `normal / failover / round_robin / weighted / hybrid_smart` 分别收到四类账户结果：分组推进、等待和最终错误均由各自路由协调器决定，账户层不能跨分组或重置 `ServerRetryBudget`。
26. 用户显式 `retry_next` 依次尝试完多个仍为 `CLOSED` 的账号：返回 `request_exhausted`，不重复扫描已尝试账号，也不把这些账号写成共享硬耗尽。
27. 旧 generation、错误 leaseId 或重复 transitionId 在新状态提交结果：全部被原子拒绝，不能改写 state、退避、恢复计数或 due 索引。
28. `OPEN` 到期后两个 server 同时调度 `HALF_OPEN / RECOVERING`：只有一个 matching-generation lease 生效，状态值与 due 索引始终原子一致。
29. 普通模式和快速模式针对同一请求解析出相同 `firstByteDeadlineMs`，每个 attempt 只有一个首字 timer；普通模式在截止时形成存活失败并切号，快速模式在同一事件上按慢样本状态机裁决，confirmation 也不能另建 5 秒 timer。
30. 同一物理账号以不同授权实例出现在主用和备用分组：transport 失败后 `attemptedAccountRuntimeKeys` 阻止本请求在后备组重复打入；显式 Key 级动作仍允许同账号换未尝试 Key。
31. 用户显式失败规则命中一个 framing 完整响应：同一 attempt 只提交 `explicitPolicyFailures` 和对应策略动作，不计 `completedResponses`，不先恢复再重开。
32. circuit key、due 索引或 policy block 发生孤儿：后台惰性修复保持保守状态；活动 `RECOVERING` 不能因 TTL 到期直接变成 `CLOSED`。
33. 凭据、代理或协议配置在 confirmation 在途时更新：旧 `dispatchRevision` 结果被拒绝，不能污染新配置状态。
34. 热质量 finalizer 与队列重放重复提交同一 `attemptId`：一分钟桶只增加一次；取消和未知不进入 `qualityAttempts`。
35. 流式 confirmation 首个语义 chunk 提交后发生读取中断：不缓存整流、不拼接第二账号，当前客户端断流且后续请求重新避让该账号。

## 16. 落地顺序

### 第零阶段：统一协调边界并移除旧语义副作用

- 建立 route-coordinator result contract，由路由协调器统一消费 `dispatchable / temporarily_blocked / request_exhausted / hard_exhausted`。
- 收敛 preparation、dispatch、routes 中分散的直接 fallback、尝试集合和重试预算，确保每请求只有一个 `ServerRetryBudget`。
- 移除未获用户显式授权的非 2xx / 错误正文自动重试、切号、轮 Key、运行态和上游桶写入。
- 停用 `system_default` 响应规则的服务端账户调度副作用，并建立透明响应反向回归。

### 第一阶段：止损

- 建立账户 / Key 短电路 Store Port 和 memory / Redis adapter。
- 把本地 transport 结果分类和用户显式策略动作接入 `SUSPECT / OPEN`，不建立上游响应语义 classifier。
- 实现 confirmation 单飞和其他请求立即换号。
- 把普通同账号默认原地重试从候选扫描前移除，只保留租约持有者一次确认。
- 覆盖通用完整 HTTP 响应透明转发，以及响应完成前 transport 失败的共享保护。

### 第二阶段：精准切号

- 引入请求内 `attemptedAccountRuntimeKeys / attemptedKeyFingerprints`、同层失败预算和公共 `normalRoutingConfig.firstByteDeadlineMs`。
- 明确普通模式与快速模式两套候选裁决顺序。
- 更新当前禁止异常账号跨优先级的冲突测试，使其只保护健康账户的配置顺序。
- 保留快速模式确认慢后跨账户偏好的既有行为。

### 第三阶段：热质量

- 建立一分钟环形桶和 5/10/30 分钟计算。
- 在完整账户配置层相同的候选内应用可靠性与速度排序。
- 接入 Redis 原子更新、attemptId 终态幂等、内存容量上限、TTL 和运行指标。
- 明确历史质量只展示、不参与热路径。

### 第四阶段：恢复与观测

- 完成 `HALF_OPEN / RECOVERING`、后台探针、generation / dispatchRevision fencing、TTL 不变量和孤儿索引修复。
- 补齐账户页运行态摘要，但不把内部失败数、租约和来源 IP 暴露成用户配置。
- 增加调度摘要、日志节流、指标和真实双节点 Redis 回归。

每个阶段都必须同时验证普通模式和快速模式；不能先实现通用热质量排序，再让快速模式被动适配。

## 17. 评审重点

本文采用以下推荐默认值，评审时重点确认：

- 同层本地可验证失败预算为 2 个账号；
- 普通模式和快速模式共用 `normalRoutingConfig.firstByteDeadlineMs`，默认 10 秒、范围 10–60 秒，只在请求开始时解析一次；两种模式只差首字结果后的动作；
- confirmation 最多一次，使用同一个首字 deadline，不另设 5 秒 timer；租约本身覆盖 attempt hard lifetime；
- 故障切号、重新选号和零可派发等待共用一个 3 秒 `routeCoordinationBudget`；
- 短退避为 `3s -> 5s -> 10s -> 30s -> 60s`；
- `RECOVERING` 连续 3 次完整成功才关闭电路；
- 热质量只重排完整账户配置层相同的账号；
- 不存在任何主动业务探索流量；
- 快速模式 `latency_degraded` 和确认慢后切号继续优先于账户偏好与热质量。

这些值不新增首轮用户配置项。先通过 Mock AI、高并发和真实短窗口观测验证固定默认，再判断哪些参数确有用户配置价值。
