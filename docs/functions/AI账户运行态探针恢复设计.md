# AI 账户运行态探针恢复设计

> 本文负责 transport / recovery 运行态。多模型完整目标由 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 约束：普通请求派生的恢复全部使用最终 Attempt 精确作用域，不复用账户哨兵；当前没有自动 account-global owner。本文出现的 `recovery_wait / precheck_pending` 默认均指精确 scope，只有明确标注 account-global 且具有专属 owner 时才表示整号。

## 目标

本文固定 AI 账户在真实网关失败、高并发失败风暴、调度降级、临时不可用和恢复探测之间的状态机。核心目标是：

- 普通用户请求只负责救当前请求、记录诊断事实并向 request collector 提交最终 Attempt 候选；本地可验证 transport failure 还可建立有界 IP / 传输局部回避。durable admission 以 scopeId 跨实例单飞创建精确 `recovery_wait / intent`，不能按 accountId 投递固定哨兵。opaque HTTP、协议失败和坏会话不能建立、续期、升级或清理账户级运行态。
- 账号运行态建立、恢复、调度降级确认和持久状态确认统一由独立后台探针或明确人工操作完成；账户所有者显式配置的账户错误策略和响应拦截策略属于主动管理意图，命中后仍可按配置直接执行。
- 高并发或多 IP 同一时间打出的失败不能把账号快速打死。
- 每个运行态和持久态都必须有自动恢复出口，避免长期挂死。
- 高性能模式可能存在多个 Web 节点，跨节点 incident、intent、generation、due、预算和物理账户 5 分钟启动门禁以 PostgreSQL ledger 为权威；Redis 只保存可重建的调度投影，不能回退到进程内 memory，也不使用 Redis 分布式锁充当持久事实。

## 总原则

真实请求链路和后台探针链路必须分工：

- 真实请求结果：安全文本请求可在语义提交前排除本请求已失败的 Key/账号并切换后续候选；所有可精确归因的终态失败可进入 capability collector，只有建连失败、lane hard timeout、真实读取中断或未完成 framing 等本地 transport failure 还能建立来源局部回避。未命中显式策略时，普通请求不得改变任何共享状态。
- 后台探针调度：精确 `recovery_wait / intent` 是不作为账户状态展示的内部任务。独立后台探针形成 `transport_failed` 后，只允许 CAS 推进同 Attempt scope 的 `precheck_pending / circuit`；后续探针由状态机和 due sweep 调度，不依赖新的用户请求到来。
- 后台兜底探针：扫描 due 的探针任务，补偿漏调度、进程重启和无新请求场景。本地运行态由 Web / Redis runtime state 探针恢复，已落库冷却态由 ops-worker 冷却复测恢复。
- 真实请求形成完整 framing：只记录本次业务事实，并可清理本请求或当前来源范围内的 transport 局部回避；不能直接清理、降级或恢复共享 phase。精确 scope 的 phase 只由匹配 `scopeId + generation` 的 intent owner 推进；legacy、用户显式策略或未来专属 account-global owner 的账户状态只由各自匹配 provenance 的后台任务或明确人工动作恢复。
- 请求数量不能直接驱动状态升级。状态升级必须同时满足最小观察时间、失败窗口、独立后台探针结果及正向 observation fence；较旧负向结果不得覆盖 claim 后出现的同 scope 成功。
- 共享最小请求执行器承载三类互斥配方：`pending_test` 激活使用 ready Catalog 的持久 activation selection，`healthCheckModel` 只作为候选优先级；周期哨兵和真正账户全局状态恢复固定使用账户保存的 `healthCheckModel`；`protocol_model / model_capability` 子 scope 恢复必须使用失败 attempt 已持久化的最终上游模型、endpoint、lane、转换路径和凭据作用域。三类探针都禁止复用用户 payload，也禁止在执行时从历史测试或目录默认猜测配方。
- 人工测试是独立诊断流量，成功或失败都不清理、确认、升级或恢复本设计中的任何运行态和持久态。

## 状态分层

| 状态 | 存储位置 | 调度影响 | 触发入口 | 自动恢复 |
| --- | --- | --- | --- | --- |
| `normal` | 无运行态 / `accounts.status = active` | 正常调度 | 运行态 transport 怀疑被清理；持久账户业务状态仅由匹配来源的 `complete_success` 或明确恢复动作激活 | 无需恢复 |
| `recovery_wait` | Redis + durable intent | 只在 soft avoid 窗口内避让该 Attempt，不作为账户状态展示 | 普通请求终态候选经 scopeId admission accepted | 后台任务按精确 generation 删除、退避或推进；普通请求不得续期或改写 |
| `failure_observed` | PostgreSQL 观察 / Redis 投影 | 不影响其他 Attempt | 后台核实任务开始处理精确 transport failure | 后台探针 `framing_complete_neutral` 只清理匹配 scope 的 transport 怀疑，或观察过期清理 |
| `latency_degraded` | PostgreSQL owner 事实 / Redis 短 TTL 投影 | 速度优先普通路由下未降级硬可承接候选优先，首字慢账号兜底；有效期内可临时覆盖账户偏好 | 后台探针或后台状态评估确认持续首字慢 | 后台探针连续达标、TTL 到期或手动恢复清理 |
| `local_suppressed` | 用户策略 owner 事实 / Redis TTL 投影 | 暂不选中该账号 | 用户显式响应拦截 `avoid_account_ttl` 等主动策略 | 配置 TTL 到期或人工恢复清理；后台自动成功不能提前解除显式策略 |
| `runtime_degraded` | PostgreSQL 精确事实 / Redis 投影 | 仅后置匹配 Attempt，其他模型 / endpoint / Key 不受影响 | 后台 transport 探针确认该 scope 近期不稳 | 只有匹配 `scopeId + generation + provenance` 的独立结果、观察窗口过期或能力级人工重检推进；不得使用账户哨兵或其他 scope 成功清理 |
| `precheck_pending` | Redis + circuit ledger | 只软阻断匹配 Attempt；其他模型 / endpoint / Key 继续调度 | 首次有效独立后台探针形成 `transport_failed` | 只有匹配 scope / generation 的独立成功或恢复流程推进；不能借账户哨兵或其他 Key 恢复 |
| `temporary_unavailable` | `accounts.status` / `cooldown_until` | 不参与调度 | 用户显式策略，或真正账户全局事实 owner 的独立确认；模型子 scope 不得写入 | 系统自动账户全局态只由匹配来源的 `complete_success` 冷却复测恢复；显式策略按自身 TTL/匹配恢复/人工动作清理 |
| `rate_limited` | `accounts.status` / `cooldown_until` | 不参与调度 | 用户显式账户错误策略或明确后台任务 | 配置 TTL、匹配来源后台任务或人工恢复；无关 transport 成功不得清理 |
| `error` | `accounts.status = error` | 不参与调度 | 本地可验证硬异常或用户显式策略/人工操作 | 可自动恢复的同来源本地异常由对应任务恢复；远端 OAuth token endpoint 失败只诊断和退避，不写账户 `error`；显式硬异常需要用户修配置或手动恢复 |

`failure_observed` 是内部观察态，不作为前端状态标签展示；`latency_degraded` 只表示当前普通路由速度优先偏好下近期首字慢，不表示账号不可用，也不能升级为持久账号状态。它是路由策略目标对账户偏好的短 TTL 覆盖层，不改写超级优先、账号优先级、备用层、会话亲和或质量排序；恢复清理后，后续请求必须重新回到账户配置排序。精确 `runtime_degraded / precheck_pending / precheck_failed` 在账户页只显示聚合摘要，具体阻断对象进入能力详情展示；不得把其中任一子 scope 渲染成整号异常。

## 失败采样规则

请求派生失败只消费最终派发规划器生成的精确身份，本文不得用字符串拼接重新计算 scope：

```text
scopeId = SHA-256(capability-attempt-scope/v1 编码后的 ResolvedGatewayAttemptCapability)
ResolvedGatewayAttemptCapability = accountRuntimeKey + runtimeAccountId + credentialSourceAccountId
  + providerProtocolProfileId + adapterRouteKey + upstreamEndpointMode + requestLane
  + normalizedUpstreamModel + credentialScopeKey
effectiveDispatchRevision + capabilityUniverseRevision 只标识账户目录 publication，不进入 scope hash
routeDefinitionRevision + attemptDefinitionRevision 作为未变 scope 的状态 fencing
授权实例另携 binding system account + local group + authorization ID 做权限 fencing
```

legacy / 未来专属 account-global owner 使用独立、版本化且不可由 request collector 构造的身份：

```text
scopeKind = account_global
scopeId = SHA-256(account-global-scope/v1 编码后的 accountRuntimeKey + ownerKind + provenanceNamespace)
```

该 `scopeId` 只供已有 legacy 恢复或注册后的专属 owner 使用，不能由 Attempt、child incident 数量、多个 IP 或候选耗尽推导。Attempt 与 account-global 编码使用不同 namespace，所有 owner 仍统一以 `scopeId + generation` 单飞和 fenced 写回，不得退回 `accountId / runtimeKey` 作为任务唯一身份。

普通请求可以记录已经真实进入上游账号调用链路的诊断样本；所有可归因终态失败由 Collector 决定能力候选，只有以下本地 transport 事实还能服务来源级回避和精确 transport intent，且仍不能直接建立共享状态：

- 上游请求异常、连接失败、超时、EOF。
- 上游 transport / lane hard timeout / read error / 未完成 framing；精确客户端画像确认的协议失败只影响当前请求，不进入账号运行态探针输入。
- 非流式 `2xx` 响应体中断。
- `200 + SSE` 真实读取中断或 lane stream idle hard timeout；上游失败事件、正常 EOF 但缺少终止事件等协议结构失败不进入。
- 已由本地独立检查确认的代理 profile / 凭据装配依赖失败；普通上游状态或正文不能反推这类本地失败。

不进入账号失败采样：

- 本地 JSON 非法、模型过滤失败、额度不足、授权不可用。
- 分组队列满、账号并发满、单 IP 并发满。
- 客户端主动断开、慢客户端背压。
- 完整非 `2xx`、`2xx + error`、任意状态码/错误正文和完整但协议未成功的响应。
- 普通路由配置首字截止主动取消；它是请求级中性调度事实，不是 lane hard timeout。
- 精确客户端协议结构/语义失败只证明当前 attempt 未交付结果；所有请求在下游尚未语义提交且对应 lane 预算允许时，均按统一候选规则救当前请求，但不得据此推进共享账户状态。
- IP 级错误熔断和 IP 级账号回避。
- 普通路由速度优先的首字慢样本。它进入 `latency_degraded` 采样窗口，不进入账号失败采样，也不能直接写 `temporary_unavailable`。

高并发去重规则：

- 同一 `scopeId` 在很短时间内的大量失败先合并为同一观察窗口内的失败样本，不按请求数快速升级状态。
- 多 IP 只作为可信度维度，不作为立即升级条件。
- 状态升级必须由独立后台探针结果驱动并满足最小观察时长；普通请求在任何时间都不能直接写 `runtime_degraded`、`precheck_pending` 或 `temporary_unavailable`。

## 首字慢采样规则

`latency_degraded` 只服务普通路由速度优先，和账号失败状态机分开：

- 采样来源是已经真实进入上游账号调用链路的首字等待。流式请求按首个可见语义输出计算，SSE comment、空心跳和未完成语义事件不算达标；非流式请求按上游 `2xx` 后首个 body 字节计算。
- 采样键必须带当前调用方和路由上下文，建议为 `systemAccountId + routeStrategyId + groupId + accountRuntimeKey`。不同路由策略的速度偏好不能互相污染。
- 窗口内慢样本只投递后台核实；后台探针或后台状态评估确认持续超阈值后才进入 `latency_degraded`。普通快样本只作为诊断事实，不能清理无关精确 scope 或任何账户级状态。
- `latency_degraded` 有效期内，速度优先可以把未降级且硬可承接的同分组账号排到前面，即使被后置账号拥有超级优先、更高账号优先级、主池身份或会话亲和。
- `latency_degraded` 只覆盖账户偏好，不覆盖硬约束；候选仍必须满足账户状态、授权、时间计划、模型能力、协议能力、额度、账号硬并发、分组队列和本地不可调度过滤。
- `latency_degraded` 账号仍可兜底调度；所有硬可承接候选都处于 `latency_degraded` 时应旁路该排序，保留原账户顺序，避免保护机制筛空号池。
- 后台探针连续首字达标清理 `latency_degraded` 后，后续选号立即回到账户配置排序，避免恢复账号饿死或替补账号被长期耗尽额度。
- 首字慢不是账号故障。本地 transport failure 可 nomination 精确 transport intent；opaque HTTP/协议失败只可 nomination 精确 execution capability intent。后台探针只推进匹配 scope，不能进入整号持久冷却；`local_suppressed` 只由用户显式策略建立。

## 后台探针分层

探针按状态存储位置分两层，避免为了恢复 Web 进程内易失状态而引入额外分布式依赖：

- 运行态恢复探针：按 `scopeId` 处理系统自动的 `failure_observed`、`runtime_degraded` 和 `precheck_pending`；`latency_degraded` 继续使用自己的路由上下文 owner。用户显式策略建立的 `local_suppressed` 只按配置 TTL 到期或人工恢复清理。capability v2 在 standalone / performance 都写 PostgreSQL durable ledger；standalone 可以把 Redis 投影与 worker role 合并进同一进程，但不得用 memory 代替事实或绕过 admission。
- ops-worker 冷却复测：只处理 provenance 明确且已由当前 epoch compatibility owner 接管的 legacy，或未来专属 account-global owner 的 `temporary_unavailable`；请求派生子 scope 和 `legacy_account_health_holds(decision=unclassified_hold)` 永不进入该链。用户显式 `temporary_unavailable / rate_limited` 只由匹配来源任务、配置 TTL 或人工恢复处理，不被通用 transport 复测越权清理。
- transport owner 采用 `framing_complete_neutral / transport_failed / unknown`，execution capability owner 采用 `complete_success / capability_unavailable / probe_task_failure / stale`；同一次执行分别持 lease 并提交。完整 HTTP framing 对 transport 是 neutral，无论状态码或正文；对 execution capability，协议未成功可归并为当前 scope 的 unavailable，但不解释具体语义。客户端中止、执行器内部错误、配置异常、未实际派发或无法归因不改变 phase。禁止用状态码、错误码或文案派生具体原因。
- ops-worker 冷却复测必须按 `(cooldown_until, priority, created_at, id)` 复合游标公平扫描，并在扫描到末尾后回绕。已在队列或执行中的账户不能永久占用固定查询窗口，避免前排账户让后续到期账户长期得不到复测。
- 每个进程内的自动完整诊断共享最多 3 路门禁，不能随批量设置或同进程多个队列叠加放大；server 运行态恢复探针、恢复探针升级后的 precheck 和 Redis 运行态探针也必须共享同一 3 路门禁。不同 worker 不引入 Redis 全局诊断锁，依靠任务归属、启动错峰和 DB service 优先级隔离。冷却复测、Key 级冷却复测和速度优先恢复探针在 ops-worker 启动后分别延迟 60、65、75 秒执行首轮，避免与 stats-worker 启动期窗口刷新同时争用 DB service。探针 DB service 请求窗口为 30 秒，超时日志必须携带具体 operation 类型。
- `error` 只在明确硬异常时写入；可自动恢复的后台任务成功后可以清理，不能把普通上游抖动写成长期硬错误。

## 状态事件触发探针调度器

运行态探针由统一调度器负责，不允许各状态分支直接散落 `setTimeout` 或各自实现 Redis key。

状态转换只提交 `ProbeIntent`：

```text
intentId
ownerKind
scopeKind
provenance
runtimeKey
accountId
scopeId
routeScopeId
effectiveDispatchRevision
capabilityUniverseRevision
routeDefinitionRevision
attemptDefinitionRevision
sourceState
targetState
reason
dueAt
priority
attempt
generation
claimedPositiveObservationVersion
```

上表是跨 owner 的逻辑信封，不要求把 transport、model capability、legacy 和用户策略塞进一张通用 intent 表。`ownerKind` 必须由 owner 专表 / adapter 固定，或作为带 CHECK 的不可变字段持久化；`scopeKind` 至少区分 `attempt` 与 `account_global`。例如 `account_capability_probe_intents` 的表归属已隐含 `model_capability`，不必为此复制 owner 字段；transport 和 legacy 继续使用各自权威存储，但都必须提供等价的 scope、generation、claim、revision 和 provenance fencing。当前没有注册 `dedicated_account_global` 自动 owner，普通 request collector 只能 nomination 前两类 attempt owner；用户显式策略不进入通用自动探针 admission。`claimedPositiveObservationVersion` 只属于 capability intent，在 worker 成功 claim 时从权威 incident 原子固化，不由 gateway 预填。

调度器职责：

- standalone 和 performance 都通过同一 `scopeId` admission / durable intent 保证跨实例单飞；generation 只由 admission winner 原子分配，唯一身份和结果 fencing 固定为 `scopeId + generation`。performance 不允许多个 server 对同 generation 短暂重复执行。物理相同的授权实例任务另由 PhysicalProbeExecutionKey 合并，但逻辑 intent 仍分别写回自己的 scope。
- 后台状态机内部生成的相同或更早 `dueAt` 意图按 scopeId + generation 合并；普通请求只能 nomination，只有 durable accepted 才创建 `recovery_wait`，不能合并失败计数、改写 generation、刷新 TTL 或推迟已有任务。
- 每个**逻辑 intent** 都持久化 `scopeId + generation`；共享物理调用的 Asynq 信封只携带 `physicalExecutionId`，worker 必须从 PostgreSQL 读取 claim 前冻结的 member 集合，不能从任务参数重建逻辑 scope。结果 fanout 前逐 member 校验 scope、generation、route / attempt definition revisions、owner provenance、logical fencing 和 delivery lease；账户双 revision 变化但 scope definition 未变时允许 carry-forward，旧 definition 结果只能 stale。transport owner 的独立探针形成 `framing_complete_neutral` 时只能推进匹配 transport scope；它不能恢复 execution capability、legacy / 专属 account-global、用户显式账户状态或 Key 状态。
- 同 `scopeId` 的 finalizer 形成 `protocolValidatedSuccess` 时，每次先进入 Redis scope `CapabilityPositiveObservationGate`，完整协议以主设计第 9.1 节为准：admission 与 physical claim 都先持久化 gate reservation、再发布 marker、最后提交 ledger；claim 必须推进 claimGateEpoch，保证 claim 前 S1 和 claim 后 S2 使用不同 positiveFenceKey。Redis winner 只有经过 `reserved -> durable_spooled` 才能让后续 success coalesced；reserve 后崩溃可 lease takeover，Redis miss / 对账未知走 durable fallback。gateway 在释放 request tracker 前 fsync release 外 spool；control owner 在 PostgreSQL scope 事务以 positiveFenceKey 条件唯一收敛 fallback，只有一个 committed observation 递增 `positiveObservationVersion`。`capability_unavailable` fanout 还要求当前 version 等于 claim 冻结值；claim 后 committed success 使 intent `superseded_by_newer_success` 并写 durable verification due。quarantine / hold 激活会推进 positive gate resolution epoch，下一次真实成功不能被旧 winner 吞掉。任何未终态 handoff 令精确 evidence unconfirmed，不能只记日志、等待下一次请求。
- half_open 首次成功、recovering 中间成功及 task failure 耗尽都必须在 incident / intent 事务内同时更新 recoverySuccessCount、nextVerificationAt、durable due 和 control outbox；首次成功至少 30 秒后安排第二次验证，第二次成功才 available。task failure 不增加能力退避，但必须按 infrastructure retry backoff 继续 due，不能让 recovering 永久悬挂或依赖新流量。
- 调度时间必须带 jitter，避免大批账号在同一秒同时探测。
- 有 PostgreSQL durable global / per-account reservation、每 credentialSourceAccountId 自动物理探针 5 分钟启动门禁，以及 provider / proxy / Base URL 维度的有界容量。预算不足时原子推进 durable due，不把账号升级为更重状态；Redis 只加速读取，进程重启或多节点不能绕过门禁。所有者显式生产重检可绕过自动 5 分钟间隔，但仍受 running=1、命令限频、成本和 global budget。
- control scheduler 周期性读取 PostgreSQL durable due ledger 补偿漏调度；performance 模式可用 Redis sorted set 加速候选定位，但 Redis 只是由 outbox 重建的索引，miss 时必须回到数据库 sweep，不能只存在于 Redis 或某个 Web 进程的 timer。
- legacy / 专属 account-global 恢复任务不携带失败请求的 model、endpoint、stream、payload 或失败形态摘要，并必须保存不可变 owner provenance；请求派生 transport / model_capability 任务必须携带非敏感的精确 Attempt 描述和 ProbeRecipe，但同样不得携带用户 payload、正文或具体错误语义。

建议探针分级：

| 级别 | 状态 | 探针策略 |
| --- | --- | --- |
| L1 recovery intake | 精确 `recovery_wait` | 使用失败 Attempt 的 ProbeRecipe；未形成有效上游尝试时丢弃结论，其他能力保持普通可调度 |
| L2 latency verify | `latency_degraded` | 使用账号健康探针校验首字是否回到策略阈值内；连续达标后清理 |
| L2 stability verify | 精确 `runtime_degraded` | 使用当前 scope 的 ProbeRecipe 低频核实；不得改用账户健康哨兵，必要时连续成功再清 |
| L3 precheck confirm | 精确 `precheck_pending` | 独立 transport / capability 探针分别持 lease 并推进同 scope；`framing_complete_neutral` 对 transport 中性、对 execution 可以 unavailable，`unknown` 只退避 |
| L4 cooldown retest | provenance 明确的 legacy / 专属 account-global owner `temporary_unavailable` | ops-worker 按匹配 provenance 复测；请求派生子 scope、来源不明 legacy 和用户显式 `rate_limited/error` 均不进入 |
| L5 hard error | `error` | 默认不高频自动探，只对可恢复错误触发 |

## 父子作用域、恢复与冷重建

传输电路的 `protocol_model` 子 scope、模型能力 `model_capability` 子 scope 与真正账户全局的 account scope 是独立 incident：

- 任意数量的模型子 scope 都不得通过投票升级为父 account。别名、endpoint、lane 和转换路径会放大 scope 数量，数量不能证明整号死亡。
- 只有当前 CapabilityScopeCatalog 中**全部非零 Route 都已被 confirmed blocked** 时，才派生 `all_configured_capabilities_blocked` 门禁；零 Route 使用 `no_routable_capability`，blocked + unknown / SUSPECT 使用部分或待确认结论。Key 集合只参与最终 effective 交集，不得把 Key 冷却改写成 capability blocked。上述派生均不创建父 incident，也不写 `accounts.status`。
- 父 account 只允许由真正与模型无关、具有专属独立证明的账户全局事实 owner 建立；当前请求派生的子 scope 没有该证明。
- 既有父级关闭只解除父层，不能删除、关闭或重置任何 `OPEN / RECOVERING` 子 scope。子 scope 恢复也不能越权清理父 incident。
- 所有 incident 的 scope、generation、definition revisions、账户 publication revisions、recovery count / due 和 CLOSED ledger 必须进入持久控制面并支持冷重建；Redis miss 在按账户权威加载前不能解释为 CLOSED。配置 publication 变化时，未变 scope 由 definition revision 原子 carry-forward，不能重置成 unknown。
- `SUSPECT` 探针得到 `unknown` 时保持状态、generation 和确认计数不变，下一次 due 使用当前递进退避级别与 jitter 后移；连续 unknown 不得零间隔自旋、推进 `OPEN` 或伪造成功恢复。

## Redis / 多实例边界

performance 模式默认可能有多个 server 节点，同一精确 scope 的运行态必须满足跨节点一致性：

- 状态转换、probe intent、probe generation、due index 和用于负向 fencing 的 `positiveObservationVersion` 必须进入跨节点权威控制面；只有不参与 CAS 的诊断窗口可以保留进程内。任何 owner 只能推进匹配 `scopeId + generation + provenance` 的状态。
- 请求调度链路允许短暂不一致，优先性能。读取 Redis 探针状态时使用 server 进程内短 TTL 近端缓存；缓存过期前可能继续避让或短暂误选，但不能写坏持久状态。
- Redis key 必须有 TTL，不能成为持久事实；父子 incident 的持久控制面和冷重建账本才是跨重启权威来源。Redis 丢失时最多回到保守调度并触发重建，不能把活动 incident 视为 `CLOSED`，也不能丢账务、授权、审计或使用记录事实。
- PostgreSQL due 表保存 `scopeId + definition revisions + incidentStateVersion + recoveryStep -> dueAt` 权威身份；Redis sorted set 只是可丢失索引。control due owner 按租约 sweep 并回到 PostgreSQL admission，不能由任意 Web 节点直接执行；不得以 accountRuntimeKey 作为 due member 合并多个模型。
- 不使用 Redis 分布式锁承担单飞或预算。logical intent 条件唯一、physical execution claim、global / account reservation 和 5 分钟启动门禁都由 PostgreSQL 行锁 / 条件唯一 / fencing CAS 实现；Redis 锁残留或抖动不能改变正确性。
- Redis 不可用时，高性能模式不能静默退回进程内 memory 作为跨节点事实源；应记录基础设施错误并走保守调度或 fail-fast。
- 状态快照展示可以允许短暂延迟，但状态转换、探针结果回写和 transport 清理必须通过 `scopeId + generation + leaseId/fencingToken` 防止旧结果覆盖或误删新状态。

完整目标落地边界：

- PostgreSQL account circuit ledger、logical intent、physical execution / member、due、budget、account physical gate 和统一 outbox 是唯一持久控制面；`RuntimeProbeStateStore` 只可作为迁移前 legacy 名称或 Redis projection adapter，不能继续拥有状态转换。
- Redis 按 `scopeId` 保存 phase / evidence / lease / due 的可重建投影与近端索引；写 PostgreSQL 事务先追加 outbox，projector 再更新 Redis。删除投影不删除 durable due / generation，Redis flush 后必须由 ledger + outbox rebuild。
- generation 按 `scopeId + definition revisions + membership incarnations` 在 PostgreSQL admission 原子递增；探针执行和写回校验当前 definition / binding / generation / fencing。accountRuntimeKey、物理账户、分组和全局身份只用于容量与归属，不能替代 incident 唯一身份。
- control scheduler 读取 PostgreSQL due、完成 admission 并以 physicalExecutionId 投递 Asynq；Go Asynq worker 只执行已 claim 的不可变 physical execution，结果先持久化再 fanout。Web / gateway、DB service 和 Redis due sweep 都不得直接调用上游。standalone 仅允许这些 role 同进程部署，不改变 owner 边界。
- 迁移前 Node memory / Redis due sweep 只能作为旧行为证据；capability v2 active epoch 前必须由 manifest 证明旧 producer / consumer 已停，不能与本节目标 owner 混跑。
- 调度过滤在 Redis runtime state 下读取精确探针状态，但使用 `1s` 正向缓存和 `500ms` 负向缓存降低热路径 Redis 读放大；后台独立 transport 探针形成匹配来源的 `framing_complete_neutral` 时，只按 `scopeId + generation` 推进轻量 transport incident 并更新诊断。普通请求的协议成功只通过 committed observation + version + outbox 的 PostgreSQL 原子事务更新正向事实；它不直接清理 OPEN，也不能清理 legacy / 专属 account-global、用户显式策略或 Key 状态。
- Redis 探针状态只保存非敏感元数据：runtime identity、scopeId、账户 publication revisions、route / attempt definition revisions、状态、evidence status、观察时间、due、原因、generation 和 gate epoch；精确 ProbeRecipe 与授权绑定上下文保存在 PostgreSQL durable intent。
- Redis / intent 不保存账号凭据、API Key、OAuth token、代理密码、完整请求 / 响应正文或用户 payload。执行探针前必须用 runtime account ID + binding system account + bound group + authorization ID 走权威 repository 重载当前凭据和权限。

## 系统账户检查探针

运行态恢复和事前确认共享底层最小执行器，但任务 owner 和配方严格分离。

系统账户检查探针、模型子 scope 探针与人工测试复用最小请求构造和网关链路，但不复用人工测试会话或状态策略：

- 底层入口使用纯结果执行器 `executeAccountProbe()`。
- `pending_test` 激活 owner 从当前 ready Catalog / credential baseline 建立持久 activation selection，按稳定 Route / credential 顺序轮转免费 execution Attempt，`healthCheckModel` 对应 Route 只优先；任一当前 definition / binding 成功可完成账户准入，单项失败只更新精确能力。active 周期哨兵读取 `healthCheckModel`，但只拥有解析后的哨兵精确 Route / Attempt 能力。未来真正 account-global owner 必须先独立注册专属 provenance，当前默认不存在。所有请求派生 transport / model_capability scope 都读取持久 ProbeRecipe，并用当前 route planner 重新解析、核对最终 Attempt 描述。
- 探针请求使用协议档案、endpoint modes 和模型协议能力解析出的最小 payload 与 endpoint；模型子 scope 禁止回退到账户哨兵。
- 探针链路仍走账号自己的供应商、协议档案、Base URL、代理和凭据。
- 探针使用 `traffic_source = runtime_recovery_probe`；执行器不自行修改状态，由 intent owner 根据 `purpose + scopeId + generation + owner provenance` 决定是否推进匹配 scope。
- active 哨兵或真正 account-global 任务的检查模型缺失、不可见、不属于支持模型或请求形态不匹配时，记录“检查模型配置异常”并停止本轮，不猜测其他模型，也不把该配置错误升级为账户不可用。activation item 自身不合法时按 unknown / stale 安全终结并继续 selection；只有本地可证明且覆盖全部 Route / credential 的共用配置错误，才交给专属 account-global configuration owner。

明确禁止：

- 账户级探针从失败请求猜 model、endpoint 或 stream。
- 模型子 scope 探针直接复用用户 payload，或绕过映射 / bridge 只探 normalized model。
- 任一子 scope 探针失败后写账号 `temporary_unavailable`。
- 将目录可见性当作 execution 成功或失败。

精确模型探针复用的是由最终派发规划器生成的非敏感描述和最小配方，不是用户失败正文。它的失败最多阻断对应模型、endpoint、转换路径和凭据作用域。

失败现场只保留为本地事实分类和排障信息：

- 建连失败、lane hard timeout、真实读取中断、未完成 framing，以及本地独立确认的代理/凭据装配失败可以进入对应 transport 或本地依赖状态机。
- 完整 HTTP `4xx/5xx`、所谓认证/限流/余额/封禁文案、上游错误码、精确客户端协议失败和 `2xx-invalid-body` 对 transport 保持中性，不能推断具体业务语义；独立 execution capability 探针可把完整失败归并为当前精确 scope 的通用 unavailable。
- `model_not_found`、`unsupported_endpoint`、模型映射错误、某 endpoint 不支持 stream、`invalid_request`、`context_length_exceeded`、用户 payload 非法和策略拒绝只影响当前请求，不直接打死账号。

## 后台探针职责

后台主动探针负责所有恢复和重状态确认：

1. 扫描 due 的精确 `recovery_wait`、系统自动失败观察、运行态降级、精确 `precheck_pending` 和 legacy / 专属 owner 持久冷却态；不接管用户显式策略 TTL。
2. 多个 Web 节点必须通过 durable logical intent、physical execution / member、claim 和 PhysicalProbeExecutionKey 单飞；逻辑唯一身份固定为 `scopeId + generation`，物理唯一身份固定为 `physicalExecutionId`。真实结果先持久化为不可变 result，再由可重领且不重发上游的 fanout delivery lease 逐 member 回写；每次回写校验 route / attempt definition revisions、owner provenance、logical fencing 和正向 observation fence，账户 publication revision 仅作来源解释，不能用允许短暂重复执行替代一致性。
3. pending_test 激活使用持久 Catalog activation selection，`healthCheckModel` 只影响候选优先级；周期哨兵和真正 account-global 任务使用 `healthCheckModel`；请求派生子 scope 使用持久 ProbeRecipe 和当前 route planner 解析出的精确 model / endpoint。三类任务都不从历史测试或目录默认猜测。
4. 运行态恢复探针必须标记 `traffic_source = runtime_recovery_probe`；持久冷却复测继续使用 `traffic_source = cooldown_retest`，两者都不能伪装成真实用户网关流量。
5. 探针使用记录和审计只保留诊断摘要，不参与业务统计或账号质量统计。
6. `framing_complete_neutral` 对 transport 只形成 framing 完整观察，对精确 execution capability 可以形成通用 unavailable；两者必须分别持 matching scope lease 并 fenced 提交。精确 `runtime_degraded` 只由自己的 ProbeRecipe owner 推进；ops-worker 对 legacy / 专属 account-global 冷却态和父 account 恢复只有形成匹配 provenance 的 `complete_success` 才能改变其 owner 状态。用户显式策略状态不被无关探针成功清理。
7. 只有 `transport_failed` 推进后台传输状态机；`unknown` 保持状态和计数并递进退避，完整 HTTP/协议结果不回灌成用户请求失败，也不进入账号质量失败统计。

## 状态转换条件

### 普通请求到精确 `recovery_wait`

未命中用户显式策略的普通请求形成可精确归因终态失败时，只允许执行以下候选动作：

- 当前请求排除已失败 Attempt 并继续换号；只有本地 transport failure 可按 IP / transport scope 建立有界局部回避。普通请求结果不得写共享 Key 或账户状态。
- Collector flush 先把跨账户有序候选集写入 gateway durable capability handoff，再由 scopeId admission 和持久 receipt 原子创建 durable `recovery_wait / intent`；RPC 失败由同 handoffId 重放，不依赖下一次请求。每请求最多 accepted 一个，5 分钟冷却从 accepted 开始。
- 已有事件时不得累计请求次数、合并来源、替换 generation、刷新 TTL 或提前 / 推迟 due 时间。
- 普通成功和失败都不能建立、清理或改写账户级运行态。

opaque HTTP 和精确客户端协议失败可以成为 execution capability 候选，但不能进入 transport 电路；路由配置截止保持中性。精确 `recovery_wait` 不展示账户异常，30 秒内只 soft avoid 同 Attempt；后台执行器未形成可归因结果时结论为 `unknown`，保持稳定 phase 并按递进退避和 jitter 后移 due。

### 有效后台失败到 `precheck_pending`

独立后台探针形成 `transport_failed` 后，才能按当前 scope + generation 原子进入精确 `precheck_pending`：

- standalone / performance 都使用 PostgreSQL `scope + definition revisions + generation + leaseId / fencingToken` 条件事务；Redis Lua 只更新同一 outbox 事实的投影，不能单独推进 phase。
- `precheck_pending` 只从普通候选中阻断匹配 Attempt，后台探针和受控半开仍可访问；其他模型 / endpoint / adapter / Key 不受影响。
- 后台探针任务取消、执行器内部错误、检查模型配置异常、未实际派发或无法归因都属于 `unknown`，不得建立软阻断；完整 HTTP framing 为 transport 恢复证据，不得按状态码/正文建立软阻断。
- 后台探针后续结果按最小观察时间、独立轮次和 generation 推进；普通请求数量不能参与状态转换。同 scope 新业务成功只有完成 committed observation + version + outbox 的 PostgreSQL 原子事务后才递增 `positiveObservationVersion`，不能直接恢复 OPEN；`capability_unavailable` 提交发现当前 version 不等于 physical claim 的冻结值时，以 `superseded_by_newer_success` 终结当前 intent、释放预算并写 durable due，不写新负向 phase。原 phase 已 OPEN 时保持 OPEN；所有后续恢复验证使用新 generation，物理任务使用新的 physicalExecutionId。

### 全池软阻断与受控半开

当前分组普通候选全部软阻断时，先尝试健康候选和路由策略允许的后备分组；仍无可用候选时：

- 恢复调度器按 `scopeId + generation` 获取精确半开租约和 `leaseId/fencingToken`，并同时受 accountRuntimeKey、物理账户和全局探针预算限制；这些预算不能改变租约身份。
- 只有持有租约并按不可变 ProbeRecipe 执行的独立探针可以推进半开；普通用户请求不能借半开直接改 phase，用户显式策略建立的 TTL 避让也不参与系统自动半开。
- 其他请求按分组、模型和候选 generation 进入有上限的 FIFO 等待；协调器使用单 timer、支持 AbortSignal 并在结束时清理等待者。
- 半开探针禁用同账户 / 同 Key 原地重试。transport owner 的独立探针形成完整 framing 时可以按 `scopeId + generation + leaseId` 推进匹配 transport incident；execution owner 必须形成 `complete_success` 才能推进能力恢复。两者不能互相清理；headers、首字节或部分 token 都不算完成。
- 半开 `transport_failed`、`unknown` 或客户端中断都只释放匹配租约，不能累计后台探针轮次；旧 generation 或旧 leaseId 不能清理新状态。
- 后备、排队和重新选号共享 `ServerRetryBudget`；默认 270 秒只累计零可派发账号、受控半开和 FIFO / 并发槽等待，fetch、正常上游 attempt 与活跃响应读取期间暂停。请求级候选切换还受 lane-aware 墙钟和 attempt 上限约束；图片使用 600/120/3600 单次时限和默认 3600 秒整请求墙钟，失败且语义未提交时仍继续后备候选。

### 精确 scope 与账户持久状态边界

请求派生的 `protocol_model / model_capability` 子 scope 永远停留在各自 circuit / 能力层，不能进入账户级 `precheck_pending`，也不能写传输来源的 `temporary_unavailable`。真正账户全局的 precheck 只接受专属 account-scope owner 的独立证据；当前没有从模型子 incident 自动生成该证据的路径。任何账户全局原因仍只允许保存本地有界 failure class、作用域、时间和 trace，不得保存或派生上游 `HTTP/code/message`、正文摘要或网关最终 `service_unavailable` 文案。

账户所有者显式配置的账户错误策略和响应拦截策略不走上述自动确认路径：规则实际命中后按用户配置直接执行 `retry_next`、TTL 运行态避让、`temporary_unavailable`、`rate_limited` 或 `error` 等动作；显式 TTL 只在到期或人工恢复时清理，后台自动成功不能提前解除。

### 持久状态恢复

以下恢复仅适用于迁移前已有且 provenance 明确并被 compatibility owner 接管，或未来专属 account-global owner 合法建立的 transport `temporary_unavailable`；当前请求派生路径和来源不明 legacy 不得进入。用户显式策略写入的 `temporary_unavailable / rate_limited / error` 按策略自己的 TTL、匹配恢复动作或人工恢复处理，不能被不相关 transport 探针越权清理：

- 每个可自动复测的账户级状态必须持久化不可变 `ownerKind + scopeKind + scopeId + provenance + sourceGeneration + configRevision`。迁移前只有来源明确的旧 transport 状态可标为 `legacy_account_global`，并由当前 epoch 的兼容 owner 接管；旧 producer / consumer 必须关闭。来源不明的 legacy 状态固定写入 `legacy_account_health_holds(decision=unclassified_hold)`，不进入通用探针；已有明确 `cooldown_until` 只能按原字段 TTL 自然到期，否则只允许逐账户人工 manifest + CAS 裁决，不能恢复旧 consumer。
- 未来 `dedicated_account_global` owner 未注册时，账户级 admission 固定拒绝；不得从 child incident 数量、多个模型失败、多个 IP 或当前请求候选耗尽推导该 owner。
- 用户显式策略状态保持自己的 owner 和 TTL / 匹配恢复动作，不进入通用 transport 冷却复测或系统自动半开。

- 后台复测固定启用。
- 起始短退避，例如 3 秒。
- 连续失败后指数退避，进入慢速恢复后不超过系统配置的最大暂停时间。
- 超过长期观察阈值后降低频率继续探测，不停止。
- transport 复测只有形成匹配来源的 `complete_success` 后才恢复对应系统自动状态为 `active`；`framing_complete_neutral` 最多清理同来源 transport 怀疑并有界顺延，完整业务错误响应不能恢复持久业务状态。

冷却复测结果写回必须同时满足以下存储契约：

- 成功恢复和失败退避都要校验任务入队时的 `configRevision` 与观察起点；旧配置或旧观察代次的迟到结果只能返回未变更，不能覆盖当前状态。
- 可选 expectation 只能在值存在时生成 `AND column = ?` 条件。禁止使用 `(? IS NULL OR column = ?)` 传递无类型 nullable 参数；该写法在 PostgreSQL 扩展查询协议下会触发 `42P18`，同时阻断成功恢复和失败次数写回。
- SQLite 与 PostgreSQL 必须复用相同的 expectation 条件构造规则，并分别执行真实 driver 回归，不能用 SQLite 通过替代 PostgreSQL 参数绑定验证。
- 写回或后续副作用抛错时，本轮账户状态保持原有事务语义；队列耗尽日志必须保留经统一脱敏后的原始错误 code/message，不能只记录“重试耗尽”。

`error` 只用于有明确本地来源或用户明确授权的硬异常：

- OAuth token endpoint 的非 `2xx`、畸形响应、超时、代理或读取异常只记录诊断并退避；只有独立可验证的本地凭据缺失、解密或装配异常才可进入带 provenance 的本地配置异常路径，并由对应维护动作恢复。
- 本地凭据无法解密、配置结构损坏等可验证异常需要用户修配置；“账号被封禁”“凭据无效”等上游文案只有命中用户显式策略时才可写对应状态。
- 错误态必须保留动作来源和本地错误码/说明，不能只写模糊“上游失败”，也不能把上游状态码或正文伪装成本地硬证据。

## 用户请求链路边界

用户请求得到失败结果时允许做：

- 记录失败使用记录和审计。
- 写本请求诊断样本，并在终态由 Collector 提交精确能力候选；只有本地 transport failure 还能进入同 Attempt 的 transport confirmation。
- 忘记当前会话亲和。
- 按本请求排除集合切换后续 Key/账号/分组救安全文本请求；只有 transport failure 可建立 IP / transport 跨请求局部回避，普通请求结果不写共享 Key 失败。
- 命中账户所有者显式错误策略或响应拦截策略时，按配置执行明确动作。

用户请求失败时不允许做：

- 直接写 `temporary_unavailable`。
- 直接推进任意精确 `runtime_degraded` phase。
- 直接建立 `precheck_pending` 等系统自动共享 phase；`local_suppressed` 只能由已命中的用户显式策略 owner 建立。
- 把普通成功作为直接清理任何共享 phase 的依据；它只能更新同 scope 的正向 observation fence，并在已 OPEN 时提前恢复 due。
- 因多个 IP 同时失败而绕过最小观察时间。
- 让用户请求直接执行探针或推进共享 phase；请求只能 nomination，后续由 durable intent 独立推进。
- 把请求数量当成状态转换依据。
- 不得根据完整 HTTP 状态、错误码、错误正文或精确客户端协议字段建立任何系统自动共享状态。
- 图片、音频、文件、资源创建、hosted tool 和其他请求在未交付结果且语义未提交时，必须与普通文本一样继续后备候选；不得按请求类型建立例外。

## 恢复出口矩阵

| 状态 | 自动恢复出口 | 人工恢复出口 |
| --- | --- | --- |
| 精确 `recovery_wait` | 后台任务完成、丢弃未知结论或推进同 scope 运行态 | 无需作为账户状态展示；能力详情可“重新检查此能力” |
| `failure_observed` | 窗口过期；后台探针 `framing_complete_neutral` 清理匹配 transport 怀疑 | 手动恢复正常 |
| `latency_degraded` | 后台探针连续首字达标；TTL 过期 | 手动恢复正常；关闭对应普通路由速度优先 |
| `local_suppressed` | 显式策略 TTL 到期 | 手动恢复正常 |
| 精确 `runtime_degraded` | 匹配 scope 的独立 ProbeRecipe 结果；观察窗口过期 | 能力详情“重新检查此能力” |
| 精确 `precheck_pending` | 匹配 scope / generation 的独立 transport 或 capability 恢复流程 | 能力详情“重新检查此能力”；不得恢复整号 |
| legacy / 专属 account-global `temporary_unavailable` | 后台冷却复测形成匹配来源的 `complete_success` | 手动恢复正常 |
| 用户显式 `temporary_unavailable / rate_limited` | 配置 TTL、匹配的策略恢复动作 | 手动恢复正常 |
| `error` | 同来源本地配置维护成功或匹配来源的 `complete_success`；远端 OAuth 失败不进入此状态 | 人工“异常恢复”只进入 `pending_test` 并立即投递后台复检 |

## 日志和排障

## 用户状态摘要契约

运行态内部字段与用户提示卡片严格分离。对外状态只通过 `AccountRuntimeAvailability.probePresentation` 提供：

- `lastObservation`：最近一次真正形成可归因结果的探针时间、`framing_complete / transport_failed / unknown`、诊断用 HTTP 状态、错误码、本地 failure class、原因和 `traceId`；HTTP 状态/错误码不得决定三态，未形成结果的执行器异常不伪造业务失败 observation。
- `schedule`：只表示真实探针任务的 `scheduled`、`due_waiting`、`running` 或 `none`。内存态以实际 timer 存在为准，Redis 以 due ZSET membership 为准；任务丢失时必须返回 `none`。
- `recoveryAt`：仅用于用户显式策略 TTL，`recoveryAtKind` 固定为 `policy_ttl_expiry`，不能作为探针下次时间。

`until`、失败次数、来源 IP、API Key 数量、探针轮次、半开租约截止和 Redis 内部 `nextProbeAtMs` 都是调度实现字段，不得直接渲染到账户状态卡片。Redis 多节点探针通过 `scopeId + generation + runId/leaseId` 原子取得、提交和清理，负向提交还要校验 `claimedPositiveObservationVersion`；旧执行结果不能覆盖新的 observation 或半开租约。

建议保留这些事件语义：

- `gateway_account_failure_observed`：历史事件名；真实请求的本地 transport failure 已按 scopeId 记录并提交精确 nomination。事件必须含 scopeId，不能被消费为 account-global 事实。
- `gateway_account_latency_degraded`：普通路由速度优先确认账号近期首字慢，账号进入速度降级。
- `gateway_account_latency_probe_success`：首字恢复探针达标，速度降级恢复计数推进或已清理。
- `gateway_account_latency_probe_failed`：首字恢复探针未达标，继续速度降级并等待下一轮。
- `gateway_account_local_suppressed`：用户显式策略命中，账号进入配置 TTL 避让；普通失败不触发。
- `gateway_account_recovery_probe_scheduled`：后台探针已调度。
- `gateway_account_recovery_probe_budget_delayed`：探针因 durable global reservation、物理账户 5 分钟启动门禁或 provider / proxy / Base URL 容量被推迟；指标携带受限 reason，不暴露账户或凭据标识。
- `gateway_account_recovery_probe_stale_result_ignored`：探针结束时 generation 已变化，旧结果已丢弃。
- `gateway_account_recovery_probe_success`：后台探针形成 `framing_complete_neutral`，匹配来源的轻量 transport 怀疑已清理；事件名不表达协议业务成功，也不恢复 `runtime_degraded`、`precheck_pending`、父 account、持久账户或 Key 业务状态。形成匹配 `complete_success` 时另记录对应的业务恢复结果。
- `gateway_account_recovery_probe_failed`：后台探针形成 `transport_failed`，等待下一轮；`unknown` 使用独立结果并递进退避，不得冒充失败。
- `gateway_account_runtime_degraded`：历史事件名；后台确认精确 scope 近期不稳，仅匹配 Attempt 进入调度降级，事件必须携带 scopeId。
- `gateway_account_precheck_scheduled`：历史事件名；后台进入精确 scope 确认。
- `gateway_account_precheck_failed_marked`：历史事件名；后台确认失败并写入精确 circuit ledger，不写 accounts.status。
- `background_cooldown_account_retest_retry_exhausted`：冷却复测队列项执行异常且无可用重试；必须附带脱敏后的原始错误，区分 PostgreSQL 参数、事务、DB service 超时和探针执行错误。

日志只记录账号 ID、运行态键、状态、窗口、失败计数、首字耗时、探针结果和短错误摘要，不写完整请求 / 响应 payload。

## 验证要求

改动该状态机时至少验证：

- 单账号高并发多 IP 同一波 transport failure 只影响当前请求和来源 transport 局部回避，并按 scopeId 单飞创建一个 durable intent，不建立账户级运行态或持久状态；opaque HTTP/协议错误可成为精确 execution 候选，但不得形成来源跨请求回避。
- 普通路由速度优先下，首字慢只记录样本并投递后台核实；只有后台确认后才进入 `latency_degraded`，且不写账号 `temporary_unavailable`、`rate_limited`、`error` 或健康检测失败次数。
- `latency_degraded` 账号在同普通路由分组内后置，未降级硬可承接账号可临时越过账户超级优先、账号优先级、备用层和会话亲和；全部候选都速度降级时旁路排序并保留原候选顺序。
- 后台探针形成匹配来源的 `complete_success` 并清理 `latency_degraded` 后，后续请求重新按账户配置排序，已恢复的主账号不会因为之前切到替补账号而长期饿死。
- 仅可重放文本在首字超阈值且下游尚未写出可见内容时，可按策略限制切换同分组后续账号；图像和其他副作用请求永久退出首字慢样本与速度切号，下游已写出可见内容后也不得透明切号。
- 关闭速度优先或修改路由策略后，对应 `latency_degraded` 运行态必须清理。
- 独立 transport 探针的 `framing_complete_neutral` 只能按匹配 `scopeId + generation + leaseId` 推进 transport incident；execution capability 必须形成 `complete_success` 才能恢复。任何结果都不能提前清理用户显式策略建立的 TTL 避让。
- 任意数量的模型子 scope 连续独立 `transport_failed` 都只推进精确子 incident，不写系统自动 transport 来源的 `temporary_unavailable`；任意完整 `4xx/5xx`、`2xx-invalid-body` 或协议失败也不推进 transport。
- 真正 account-scope owner 已建立的系统自动 transport `temporary_unavailable` 才能由匹配来源的后台复测 `complete_success` 恢复 `active`；用户显式状态只走匹配恢复出口。
- SQLite 与真实 PostgreSQL 都要覆盖冷却复测当前代次成功恢复、当前代次失败累加、错误配置版本拒绝和旧观察起点拒绝；PostgreSQL 回归必须真实执行参数绑定，防止无类型 nullable 参数重新进入写回 SQL。
- 各类请求的 opaque HTTP 与 transport 失败在语义提交前都能按唯一 Key/账户候选救回当前请求，整个请求最多 64 次真实 attempt。
- 旧 generation 探针结果不能覆盖后台 / 主动健康 / 匹配租约半开成功、手动恢复或更新后的状态。
- performance / Redis runtime state 下，多节点同 scope 必须由 durable intent / claim 单飞，不能接受短暂重复执行；旧 generation 结果不能覆盖或误删新状态，due sweep 能补偿进程重启后的任务。
- 同 scope 成功发生在 claim 后、负向结果迟到时必须得到 `superseded_by_newer_success` 且不打开新负向 phase；成功发生在 claim 前时后续独立失败可以生效；OPEN 后业务成功只提前恢复 due，仍由独立探针恢复。
- 精确 `precheck_pending` 在 Redis 下只阻断匹配 Attempt；Route 全部 Key 或账户全部 Route 都阻断时才派生无可调度能力门禁。健康 / 后备优先、scope 单飞半开和有界等待均生效；等待预算与请求墙钟不能混为一谈。
- 模型 / endpoint 明确不支持时不进入账号级状态升级；账户级恢复不能猜测失败请求形态，模型子 scope 只能复用最终派发描述和最小 ProbeRecipe，不能复用用户 payload，也不能因此把整号打成不可用。
- Mock AI 覆盖失败后恢复、失败后持续不可用、高并发失败风暴和授权实例隔离。
- Mock AI 覆盖 `SUSPECT` 连续 unknown：状态/generation/确认计数不变，due 随递进退避和 jitter 后移，不出现零间隔自旋。
- 3 个、64 个或更多模型 child scope 都不能投票生成父 account；已有独立父 incident 的关闭不抹掉仍 `OPEN/RECOVERING` 的 child，父子 incident、generation、revision、due 和 CLOSED ledger 跨重启保持各自一致。
