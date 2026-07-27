# AI 账户运行态探针恢复设计

> 本文负责账户级 transport 运行态。多模型完整目标另由 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 约束：模型能力确认与恢复使用精确失败作用域，不复用账户哨兵；完整失败对 transport 电路保持中性，但独立模型能力探针可以形成不带具体错误语义的局部阻断。目标实现必须与本文账户级状态机并存，不能互相替代。

## 目标

本文固定 AI 账户在真实网关失败、高并发失败风暴、调度降级、临时不可用和恢复探测之间的状态机。核心目标是：

- 普通用户请求只负责救当前请求、记录诊断事实；只有本地可验证 transport failure 才能建立有界 IP / 传输局部回避，并在不存在同账户后台事件时原子首次投递 `recovery_wait`。opaque HTTP、协议失败和坏会话只扩大本请求排除集合，不能建立、续期、升级或清理账户级运行态。
- 账号运行态建立、恢复、调度降级确认和持久状态确认统一由独立后台探针或明确人工操作完成；账户所有者显式配置的账户错误策略和响应拦截策略属于主动管理意图，命中后仍可按配置直接执行。
- 高并发或多 IP 同一时间打出的失败不能把账号快速打死。
- 每个运行态和持久态都必须有自动恢复出口，避免长期挂死。
- 高性能模式可能存在多个 Web 节点，跨节点运行态、generation 和 due 索引必须进入 Redis runtime state，不能回退到进程内 memory，也不使用 Redis 分布式锁。

## 总原则

真实请求链路和后台探针链路必须分工：

- 真实请求结果：安全文本请求可在语义提交前排除本请求已失败的 Key/账号并切换后续候选；只有建连失败、lane hard timeout、真实读取中断或未完成 framing 等本地 transport failure 才能建立传输局部回避并原子首次投递 `recovery_wait`。未命中显式策略时，完整 HTTP/协议结果不得改变任何跨请求调度资格。
- 后台探针调度：`recovery_wait` 是不展示、保持普通可调度的内部后台任务。独立后台探针形成 `transport_failed` 后，才允许 CAS 建立 `precheck_pending` 等账户级传输运行态；后续探针由状态机和 due sweep 调度，不依赖新的用户请求到来。
- 后台兜底探针：扫描 due 的探针任务，补偿漏调度、进程重启和无新请求场景。本地运行态由 Web / Redis runtime state 探针恢复，已落库冷却态由 ops-worker 冷却复测恢复。
- 真实请求形成完整 framing：只记录本次业务事实，并可清理本请求或当前来源范围内的 transport 局部回避；不能清理、降级或恢复账户级运行态和持久状态。账户级恢复只由匹配 generation 的后台探针、主动健康检查、显式人工恢复或对应后台任务完成。
- 请求数量不能直接驱动状态升级。状态升级必须同时满足最小观察时间、失败窗口、无后台成功证据和独立后台探针结果。
- 运行态恢复探针只允许使用系统账户检查探针，也就是共享最小请求执行器；模型固定为账户保存的 `healthCheckModel`。禁止从历史测试、个人默认、管理员默认、协议档案默认、支持模型首项或失败请求回退模型，也禁止复用失败请求的 endpoint、stream 或用户 payload。
- 人工测试是独立诊断流量，成功或失败都不清理、确认、升级或恢复本设计中的任何运行态和持久态。

## 状态分层

| 状态 | 存储位置 | 调度影响 | 触发入口 | 自动恢复 |
| --- | --- | --- | --- | --- |
| `normal` | 无运行态 / `accounts.status = active` | 正常调度 | 运行态 transport 怀疑被清理；持久账户业务状态仅由匹配来源的 `complete_success` 或明确恢复动作激活 | 无需恢复 |
| `recovery_wait` | memory / Redis 后台任务 | 不影响普通调度，不作为账户状态展示 | 普通请求的本地 transport failure 原子首次投递后台核实 | 后台任务接管后按探针三态删除、退避或推进；普通请求不得续期或改写 |
| `failure_observed` | memory / Redis 后台观察 | 不影响排序 | 后台核实任务开始处理 transport failure | 后台探针 `framing_complete_neutral` 只清理匹配 transport 怀疑，或观察过期清理 |
| `latency_degraded` | memory / Redis 短 TTL 运行态 | 速度优先普通路由下未降级硬可承接候选优先，首字慢账号兜底；有效期内可临时覆盖账户偏好 | 后台探针或后台状态评估确认持续首字慢 | 后台探针连续达标、TTL 到期或手动恢复清理 |
| `local_suppressed` | memory / Redis TTL 运行态 | 暂不选中该账号 | 用户显式响应拦截 `avoid_account_ttl` 等主动策略 | 配置 TTL 到期或人工恢复清理；后台自动成功不能提前解除显式策略 |
| `runtime_degraded` | memory / Redis 运行态快照 | 普通候选优先，降级账号兜底 | 后台 transport 探针确认近期不稳 | 只有匹配 generation / provenance 的 `complete_success`、观察窗口过期或手动恢复清理；`framing_complete_neutral` 只保留诊断 / 顺延，不恢复该运行态 |
| `precheck_pending` | memory / Redis 运行态快照 | 软阻断普通候选；后台探针、主动健康检查和 generation 租约半开可访问 | 首次有效独立后台探针形成 `transport_failed` | 只有匹配 generation / provenance 的 `complete_success` 或专属人工出口恢复；`framing_complete_neutral` 只保留 transport 怀疑并顺延，transport failure 释放租约并继续后台确认，unknown 只退避 |
| `temporary_unavailable` | `accounts.status` / `cooldown_until` | 不参与调度 | 后台多轮独立 transport failure 确认不可用，或用户显式策略 | 系统自动传输态只由匹配来源的 `complete_success` 冷却复测恢复；显式策略按自身 TTL/匹配恢复/人工动作清理 |
| `rate_limited` | `accounts.status` / `cooldown_until` | 不参与调度 | 用户显式账户错误策略或明确后台任务 | 配置 TTL、匹配来源后台任务或人工恢复；无关 transport 成功不得清理 |
| `error` | `accounts.status = error` | 不参与调度 | 本地可验证硬异常或用户显式策略/人工操作 | 可自动恢复的同来源本地异常由对应任务恢复；远端 OAuth token endpoint 失败只诊断和退避，不写账户 `error`；显式硬异常需要用户修配置或手动恢复 |

`failure_observed` 是内部观察态，不作为前端状态标签展示；`latency_degraded` 只表示当前普通路由速度优先偏好下近期首字慢，不表示账号不可用，也不能升级为持久账号状态。它是路由策略目标对账户偏好的短 TTL 覆盖层，不改写超级优先、账号优先级、备用层、会话亲和或质量排序；恢复清理后，后续请求必须重新回到账户配置排序。账户页只展示会影响调度或需要排障的 `latency_degraded`、`runtime_degraded`、`local_suppressed`、`precheck_pending`、`precheck_failed` 和持久状态。

## 失败采样规则

失败样本按运行态键聚合：

```text
自有账户：accountId
授权实例：accountId + 使用方系统账户 + 本地分组 + 授权 ID
```

普通请求可以记录已经真实进入上游账号调用链路的诊断样本，但只有以下本地 transport 事实能服务来源级回避和首次 `recovery_wait` 投递，且仍不能直接建立账户级运行态：

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

- 同一运行态键在很短时间内的大量失败先合并为同一观察窗口内的失败样本，不按请求数快速升级状态。
- 多 IP 只作为可信度维度，不作为立即升级条件。
- 状态升级必须由独立后台探针结果驱动并满足最小观察时长；普通请求在任何时间都不能直接写 `runtime_degraded`、`precheck_pending` 或 `temporary_unavailable`。

## 首字慢采样规则

`latency_degraded` 只服务普通路由速度优先，和账号失败状态机分开：

- 采样来源是已经真实进入上游账号调用链路的首字等待。流式请求按首个可见语义输出计算，SSE comment、空心跳和未完成语义事件不算达标；非流式请求按上游 `2xx` 后首个 body 字节计算。
- 采样键必须带当前调用方和路由上下文，建议为 `systemAccountId + routeStrategyId + groupId + accountRuntimeKey`。不同路由策略的速度偏好不能互相污染。
- 窗口内慢样本只投递后台核实；后台探针或后台状态评估确认持续超阈值后才进入 `latency_degraded`。普通快样本只作为诊断事实，不能清理账户级运行态。
- `latency_degraded` 有效期内，速度优先可以把未降级且硬可承接的同分组账号排到前面，即使被后置账号拥有超级优先、更高账号优先级、主池身份或会话亲和。
- `latency_degraded` 只覆盖账户偏好，不覆盖硬约束；候选仍必须满足账户状态、授权、时间计划、模型能力、协议能力、额度、账号硬并发、分组队列和本地不可调度过滤。
- `latency_degraded` 账号仍可兜底调度；所有硬可承接候选都处于 `latency_degraded` 时应旁路该排序，保留原账户顺序，避免保护机制筛空号池。
- 后台探针连续首字达标清理 `latency_degraded` 后，后续选号立即回到账户配置排序，避免恢复账号饿死或替补账号被长期耗尽额度。
- 首字慢不是账号故障。只有普通请求的本地 transport failure 首次投递 `recovery_wait`；opaque HTTP/协议失败不投递。后台探针形成 transport failure 后才进入 `failure_observed`、`runtime_degraded` 或持久冷却链路。`local_suppressed` 只由用户显式策略建立。

## 后台探针分层

探针按状态存储位置分两层，避免为了恢复 Web 进程内易失状态而引入额外分布式依赖：

- 运行态恢复探针：处理系统自动的 `failure_observed`、`latency_degraded`、`runtime_degraded` 和 `precheck_pending`。用户显式策略建立的 `local_suppressed` 只按配置 TTL 到期或人工恢复清理。standalone 模式可以保存在 Web 进程内；performance 模式必须保存在 Redis runtime state，但不使用通用 Redis 分布式锁。
- ops-worker 冷却复测：自动处理系统 transport 来源的 `temporary_unavailable`，按冷却时间和退避策略持续复测，直到恢复、进入长期低频复测或用户手动处理。用户显式 `temporary_unavailable / rate_limited` 只由匹配来源任务、配置 TTL 或人工恢复处理，不被通用 transport 复测越权清理。
- 后台系统探针统一采用三类本地事实：`framing_complete_neutral`、`transport_failed`、`unknown`。完整 HTTP framing 或 SSE 在无读取中断时正常结束均为 `framing_complete_neutral`，无论状态码、正文、错误对象或协议完成字段如何；它最多关闭匹配来源的轻量 transport 怀疑，并不是 Key / 账户业务成功证据，不能恢复 `runtime_degraded`、`precheck_pending`、持久冷却或父 account。建连失败、lane hard timeout、真实读取中断和未完成 framing 为 `transport_failed`，可推进传输状态机。只有协议校验成功且 framing 完整才形成 `complete_success`。客户端中止、执行器内部错误、检查模型配置异常、未实际派发或无法归因结果为 `unknown`，不改变状态和确认计数，只按现有退避与 jitter 推迟 due。禁止用是否收到响应头、状态码范围、错误码或错误文案把完整响应分类成失败。
- ops-worker 冷却复测必须按 `(cooldown_until, priority, created_at, id)` 复合游标公平扫描，并在扫描到末尾后回绕。已在队列或执行中的账户不能永久占用固定查询窗口，避免前排账户让后续到期账户长期得不到复测。
- 每个进程内的自动完整诊断共享最多 3 路门禁，不能随批量设置或同进程多个队列叠加放大；server 运行态恢复探针、恢复探针升级后的 precheck 和 Redis 运行态探针也必须共享同一 3 路门禁。不同 worker 不引入 Redis 全局诊断锁，依靠任务归属、启动错峰和 DB service 优先级隔离。冷却复测、Key 级冷却复测和速度优先恢复探针在 ops-worker 启动后分别延迟 60、65、75 秒执行首轮，避免与 stats-worker 启动期窗口刷新同时争用 DB service。探针 DB service 请求窗口为 30 秒，超时日志必须携带具体 operation 类型。
- `error` 只在明确硬异常时写入；可自动恢复的后台任务成功后可以清理，不能把普通上游抖动写成长期硬错误。

## 状态事件触发探针调度器

运行态探针由统一调度器负责，不允许各状态分支直接散落 `setTimeout` 或各自实现 Redis key。

状态转换只提交 `ProbeIntent`：

```text
runtimeKey
accountId
sourceState
targetState
reason
dueAt
priority
attempt
generation
```

调度器职责：

- standalone 模式同一个 `runtimeKey` 尽量只保留一个本地探针；performance 模式允许多个 server 节点短暂重复执行同一个 `runtimeKey` 的探针。
- 后台状态机内部生成的相同或更早 `dueAt` 意图可以按 generation 合并；普通请求只能首次创建 `recovery_wait`，不能合并失败计数、改写 generation、刷新 TTL 或推迟已有任务。
- 所有探针任务带 `generation`。探针开始时记录 generation，结束回写前必须确认 generation 未变化；如果后台探针 / 主动健康检查 / 匹配租约半开形成 `framing_complete_neutral` 并只清理同代轻量 transport 怀疑、手动恢复或新状态转换已经推进 generation，旧探针结果直接丢弃，旧 generation 也不能覆盖或删除新 generation 状态。该 framing 结果不能恢复 `runtime_degraded`、`precheck_pending`、持久账户或 Key 业务状态。
- 调度时间必须带 jitter，避免大批账号在同一秒同时探测。
- 有本机全局并发保护、单账号最小间隔，以及 provider / proxy / baseUrl 维度的本机最小间隔。预算不足时只推迟 due 时间，不把账号升级为更重状态；该预算不进入 Redis 分布式锁。
- 后台 sweep 周期性读取 due 索引补偿漏调度；performance 模式的 due 索引必须在 Redis 中，不能只存在于某个 Web 进程的 timer。
- 恢复探针任务本身不携带失败请求的 model、endpoint、stream、payload 或失败形态摘要；失败现场只作为状态判断、日志和后续人工排障信息。

建议探针分级：

| 级别 | 状态 | 探针策略 |
| --- | --- | --- |
| L1 recovery intake | `recovery_wait` | 单次账号健康探针；未形成有效上游尝试时丢弃结论，账户保持普通可调度 |
| L2 latency verify | `latency_degraded` | 使用账号健康探针校验首字是否回到策略阈值内；连续达标后清理 |
| L2 stability verify | `runtime_degraded` | 低频账号健康探针；必要时连续成功再清 |
| L3 precheck confirm | `precheck_pending` | 独立 transport 探针；多轮 `transport_failed` 且并发归零后才落库，匹配 generation 的 `framing_complete_neutral` 只诊断 / 顺延，`unknown` 只退避；运行态和持久业务状态要求匹配 provenance 的 `complete_success` |
| L4 cooldown retest | 系统自动 transport 来源的 `temporary_unavailable` | ops-worker 持久冷却复测，退避更保守；用户显式 `rate_limited/error` 不被无关 transport 成功清理 |
| L5 hard error | `error` | 默认不高频自动探，只对可恢复错误触发 |

## 父子作用域升级、恢复与冷重建

传输电路的 `protocol_model` 子 scope 与 account 父 scope 是两个独立 incident，不能用父级状态覆盖子级明细：

- 子 scope 升级到父 account scope 的门槛必须可配置；当前默认要求至少 `3` 个独立子 scope 的本地 transport evidence 达标，不能退回“2 个模型 + 累计 3 次”硬编码。父 incident 最多携带 `64` 个 child ID；第 65 个及后续 child 仍保留独立 incident，但不得继续扩张父 payload。
- 父 account scope 在达到可配置的通用协议成功证据阈值后可以关闭，当前默认阈值为 `3`。父级恢复证据比单纯 framing 完整更强，但不依赖状态码/错误文案；这些证据不要求逐一命中所有肇事子 scope，也不要求没有新流量的子模型重新获得流量，避免父账户永久卡死。
- 父级关闭只解除父 account scope 的阻断，不能删除、关闭或重置仍处于 `OPEN / RECOVERING` 的子 scope；后续请求仍必须遵守各子 scope 自己的 generation、due 和状态。子 scope 恢复也不能越权清理父 incident。
- 父子 incident 的 scope、generation、dispatch revision、due、父子关联和 CLOSED ledger 必须进入持久控制面并支持启动冷重建。memory / Redis 只是运行时投影；Redis 缓存丢失、容量淘汰或进程重启不得把仍活动的父/子 incident 默认成 `CLOSED`，也不得因父 payload 达到 64 而漏掉独立子 incident。
- `SUSPECT` 探针得到 `unknown` 时保持状态、generation 和确认计数不变，下一次 due 使用当前递进退避级别与 jitter 后移；连续 unknown 继续渐进退避，不得写 `retryAt = now`、零间隔自旋、推进 `OPEN` 或伪造成功恢复。

## Redis / 多实例边界

performance 模式默认可能有多个 server 节点，同一账号的运行态必须满足跨节点一致性：

- 状态转换、probe intent、probe generation 和 due index 进入 Redis runtime state；失败窗口和 success observation 可以保留进程内短窗口，但账户级结论只能由后台探针、主动健康检查或显式人工操作推进。
- 请求调度链路允许短暂不一致，优先性能。读取 Redis 探针状态时使用 server 进程内短 TTL 近端缓存；缓存过期前可能继续避让或短暂误选，但不能写坏持久状态。
- Redis key 必须有 TTL，不能成为持久事实；父子 incident 的持久控制面和冷重建账本才是跨重启权威来源。Redis 丢失时最多回到保守调度并触发重建，不能把活动 incident 视为 `CLOSED`，也不能丢账务、授权、审计或使用记录事实。
- due index 用 Redis sorted set 或等价 runtime state 索引保存 `runtimeKey -> dueAt`，各 Web 节点都可以 sweep；重复执行由 generation 条件写入和条件删除收敛。
- 不使用 Redis 分布式锁、分布式全局预算锁、provider 锁、proxy 锁或 baseUrl 锁限制运行态恢复探针；预算只作为本机保护和 jitter，避免 Redis 锁残留或抖动把恢复并发压低。
- Redis 不可用时，高性能模式不能静默退回进程内 memory 作为跨节点事实源；应记录基础设施错误并走保守调度或 fail-fast。
- 状态快照展示可以允许短暂延迟，但状态转换、探针结果回写和 `framing_complete` 清理必须通过 generation 防止旧结果覆盖或误删新状态。

当前落地边界：

- `RuntimeProbeStateStore` 是运行态探针专用存储。standalone 使用 memory；performance 使用 Redis state URL。
- Redis 存储拆为 JSON 状态、due sorted set 和 generation key。写状态时同时写 due 索引；删除状态时同时清理 due。
- generation 使用 Redis `INCR + PEXPIRE` 原子递增；探针执行和事前确认每次回写前都读取当前 generation，旧结果直接丢弃。
- server 进程启动后会启动 Redis due sweep，周期读取 due 索引并执行到期探针；worker 和 DB service 不执行运行态恢复探针。
- 调度过滤在 Redis runtime state 下读取共享探针状态，但使用 `1s` 正向缓存和 `500ms` 负向缓存降低热路径 Redis 读放大；后台探针或主动健康检查形成匹配来源的 `framing_complete_neutral` 时，只按 generation 清理轻量 transport 怀疑并更新诊断，普通请求完整响应不能清理账户级运行态；`complete_success`、专属 TTL 或人工恢复才可清理对应运行态或持久业务状态。
- Redis 探针状态只保存非敏感元数据：`runtimeKey`、`accountId`、账号展示名、供应商、系统账户 / 分组 ID、设置快照、失败计数、观察时间、下一次探针时间、原因和 generation。
- Redis 探针状态不保存账号凭据、API Key、OAuth token、代理密码、完整请求 / 响应正文、失败请求 model、endpoint、stream 或用户 payload。执行探针前必须通过 DB service 按 `accountId + groupId + systemAccountId` 重载账号凭据，并允许读取当前不可用账号用于恢复验证。

## 系统账户检查探针

运行态恢复和事前确认只使用一类底层执行器：系统账户检查探针。

系统账户检查探针与人工测试复用最小请求构造和网关链路，但不复用人工测试会话或状态策略：

- 底层入口使用纯结果执行器 `executeAccountProbe()`。
- 探针模型严格读取账户 `healthCheckModel`，必须属于账户 `supportedModels` 并能按协议档案发起最小文本请求。
- 探针请求使用协议档案、endpoint modes 和模型协议能力解析出的最小 payload 与 endpoint。
- 探针链路仍走账号自己的供应商、协议档案、Base URL、代理和凭据。
- 探针使用 `traffic_source = runtime_recovery_probe`；执行器不自行修改状态，由运行态恢复策略根据 `purpose` 和 generation 决定是否清理或升级。
- 检查模型缺失、不可见、不属于支持模型或请求形态不匹配时，记录“检查模型配置异常”并停止本轮，不猜测其他模型，也不把该配置错误升级为账户不可用。

明确禁止：

- 禁止复用失败请求的 model。
- 禁止复用失败请求的 endpoint。
- 禁止复用失败请求的 stream 形态。
- 禁止复用失败请求的用户 payload。
- 禁止因为某个失败请求形态再次失败就把账号写成 `temporary_unavailable`。

原因是失败现场可能只是模型不支持、endpoint 不匹配、payload 非法、上下文过长或局部能力问题；用它做恢复探针会把可用账号误判为不可用。

失败现场只保留为本地事实分类和排障信息：

- 建连失败、lane hard timeout、真实读取中断、未完成 framing，以及本地独立确认的代理/凭据装配失败可以进入对应 transport 或本地依赖状态机。
- 完整 HTTP `4xx/5xx`、所谓认证/限流/余额/封禁文案、上游错误码、精确客户端协议失败和 `2xx-invalid-body` 都保持系统自动业务状态中性；只能作为诊断或用户显式策略输入。
- `model_not_found`、`unsupported_endpoint`、模型映射错误、某 endpoint 不支持 stream、`invalid_request`、`context_length_exceeded`、用户 payload 非法和策略拒绝只影响当前请求，不直接打死账号。

## 后台探针职责

后台主动探针负责所有恢复和重状态确认：

1. 扫描 due 的 `recovery_wait`、系统自动失败观察、运行态降级、`precheck_pending` 和持久冷却态；不接管用户显式策略 TTL。
2. 多个 Web 节点可以短暂重复执行同一个运行态键的探针；探针结果回写必须校验 generation，旧探针结果直接丢弃，不能靠 Redis 分布式锁保证唯一执行。
3. 使用系统账户检查探针发起最小请求；模型固定为账户 `healthCheckModel`，不从历史测试、目录默认或失败请求提取 model / endpoint。
4. 运行态恢复探针必须标记 `traffic_source = runtime_recovery_probe`；持久冷却复测继续使用 `traffic_source = cooldown_retest`，两者都不能伪装成真实用户网关流量。
5. 探针使用记录和审计只保留诊断摘要，不参与业务统计或账号质量统计。
6. 运行态探针或主动健康检查形成 `framing_complete_neutral` 时，只能按 generation 清理同来源轻量 transport 怀疑；ops-worker 对系统自动 transport 冷却态复测、`runtime_degraded`、`precheck_pending` 和父 account 恢复都只有形成匹配来源的 `complete_success` 才能改变业务状态。用户显式策略状态不被无关探针成功清理。
7. 只有 `transport_failed` 推进后台传输状态机；`unknown` 保持状态和计数并递进退避，完整 HTTP/协议结果不回灌成用户请求失败，也不进入账号质量失败统计。

## 状态转换条件

### 普通请求到 `recovery_wait`

未命中用户显式策略的普通请求只有形成本地可验证 transport failure 时，才允许执行以下账户核实副作用：

- 当前请求排除已失败账号并继续换号；按 IP / transport scope 建立有界局部回避。普通请求 transport 结果不得归咎并写共享 Key 状态。
- 仅在该运行态键不存在后台事件时，原子首次创建 `recovery_wait`。
- 已有事件时不得累计请求次数、合并来源、替换 generation、刷新 TTL 或提前 / 推迟 due 时间。
- 普通成功和失败都不能建立、清理或改写账户级运行态。

opaque HTTP、精确客户端协议失败和路由配置截止只允许按端点语义处理当前请求，不能执行上述副作用。`recovery_wait` 不展示账户异常、不影响普通调度；后台执行器未形成可归因结果时结论为 `unknown`，保持状态与计数并按递进退避和 jitter 后移 due。

### 有效后台失败到 `precheck_pending`

独立后台探针形成 `transport_failed` 后，才能按当前 generation 原子进入 `precheck_pending`：

- standalone 使用 memory 条件更新；performance 使用 Redis `phase + generation + leaseId` Lua CAS。
- `precheck_pending` 立即从普通候选中软阻断，但后台探针、主动健康检查和受控半开仍可访问。
- 后台探针任务取消、执行器内部错误、检查模型配置异常、未实际派发或无法归因都属于 `unknown`，不得建立软阻断；完整 HTTP framing 为 transport 恢复证据，不得按状态码/正文建立软阻断。
- 后台探针后续结果按最小观察时间、独立轮次和 generation 推进；普通请求数量和普通成功信号不能参与账户级状态转换。

### 全池软阻断与受控半开

当前分组普通候选全部软阻断时，先尝试健康候选和路由策略允许的后备分组；仍无可用候选时：

- 按 `runtimeKey + generation` 获取账户半开租约，并按 `systemAccountId + groupId` 限制同分组同时最多一个半开。
- 只有租约持有者可以发起真实半开请求；显式用户策略建立的 TTL 避让不参与系统自动半开。
- 其他请求按分组、模型和候选 generation 进入有上限的 FIFO 等待；协调器使用单 timer、支持 AbortSignal 并在结束时清理等待者。
- 半开请求禁用同账户 / 同 Key 原地重试。完整 HTTP framing 或 SSE 在没有读取中断时正常结束形成 `framing_complete`，按 generation CAS 清理同来源 transport 软阻断；headers、首字节或部分 token 不算 framing 完成，状态码和业务正文不参与。
- 半开 `transport_failed`、`unknown` 或客户端中断都只释放匹配租约，不能累计后台探针轮次；旧 generation 或旧 leaseId 不能清理新状态。
- 后备、排队和重新选号共享 `ServerRetryBudget`；默认 270 秒只累计零可派发账号、受控半开和 FIFO / 并发槽等待，fetch、正常上游 attempt 与活跃响应读取期间暂停。请求级候选切换还受 lane-aware 墙钟和 attempt 上限约束；图片使用 600/120/3600 单次时限和默认 3600 秒整请求墙钟，失败且语义未提交时仍继续后备候选。

### 从 `precheck_pending` 到持久状态

后台探针完成至少 5 分钟观察、3 个独立 `transport_failed` 轮次且轮次至少间隔 2 分钟后，如果当前账号仍有在途并发，只保持 `precheck_pending` 并等待并发归零。并发归零后，系统自动路径默认写传输来源的 `temporary_unavailable`；原因只允许使用本地有界 failure class、作用域、时间和 trace，不得保存或派生上游 `HTTP/code/message`、正文摘要或网关最终 `service_unavailable` 文案。

账户所有者显式配置的账户错误策略和响应拦截策略不走上述自动确认路径：规则实际命中后按用户配置直接执行 `retry_next`、TTL 运行态避让、`temporary_unavailable`、`rate_limited` 或 `error` 等动作；显式 TTL 只在到期或人工恢复时清理，后台自动成功不能提前解除。

### 持久状态恢复

系统自动 transport 来源的 `temporary_unavailable` 不允许长期挂死；用户显式策略写入的 `temporary_unavailable / rate_limited / error` 按策略自己的 TTL、匹配恢复动作或人工恢复处理，不能被不相关 transport 探针越权清理：

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
- 写本请求诊断样本；只有本地 transport failure 原子首次投递 `recovery_wait`。
- 忘记当前会话亲和。
- 按本请求排除集合切换后续 Key/账号/分组救安全文本请求；只有 transport failure 可建立 IP / transport 跨请求局部回避，普通请求结果不写共享 Key 失败。
- 命中账户所有者显式错误策略或响应拦截策略时，按配置执行明确动作。

用户请求失败时不允许做：

- 直接写 `temporary_unavailable`。
- 直接把账号标为 `runtime_degraded`。
- 直接建立 `local_suppressed` 或 `precheck_pending` 等系统自动账户级运行态。
- 把普通成功作为清理任何账户级运行态的依据。
- 因多个 IP 同时失败而绕过最小观察时间。
- 用“用户请求到来”作为探针触发条件。
- 把请求数量当成状态转换依据。
- 不得根据完整 HTTP 状态、错误码、错误正文或精确客户端协议字段建立任何系统自动共享状态。
- 图片、音频、文件、资源创建、hosted tool 和其他请求在未交付结果且语义未提交时，必须与普通文本一样继续后备候选；不得按请求类型建立例外。

## 恢复出口矩阵

| 状态 | 自动恢复出口 | 人工恢复出口 |
| --- | --- | --- |
| `recovery_wait` | 后台任务完成、丢弃未知结论或推进为探针运行态 | 无需展示；人工恢复可精确清理后台事件 |
| `failure_observed` | 窗口过期；后台探针 `framing_complete_neutral` 清理匹配 transport 怀疑 | 手动恢复正常 |
| `latency_degraded` | 后台探针连续首字达标；TTL 过期 | 手动恢复正常；关闭对应普通路由速度优先 |
| `local_suppressed` | 显式策略 TTL 到期 | 手动恢复正常 |
| `runtime_degraded` | 匹配来源的 `complete_success`；观察窗口过期 | 手动恢复正常 |
| `precheck_pending` | 后台探针或匹配 generation 的半开形成 `complete_success`；中性 framing 只清理 transport 怀疑 | 手动恢复正常 |
| 系统自动 transport 来源的 `temporary_unavailable` | 后台冷却复测形成匹配来源的 `complete_success` | 手动恢复正常 |
| 用户显式 `temporary_unavailable / rate_limited` | 配置 TTL、匹配的策略恢复动作 | 手动恢复正常 |
| `error` | 同来源本地配置维护成功或匹配来源的 `complete_success`；远端 OAuth 失败不进入此状态 | 人工“异常恢复”只进入 `pending_test` 并立即投递后台复检 |

## 日志和排障

## 用户状态摘要契约

运行态内部字段与用户提示卡片严格分离。对外状态只通过 `AccountRuntimeAvailability.probePresentation` 提供：

- `lastObservation`：最近一次真正形成可归因结果的探针时间、`framing_complete / transport_failed / unknown`、诊断用 HTTP 状态、错误码、本地 failure class、原因和 `traceId`；HTTP 状态/错误码不得决定三态，未形成结果的执行器异常不伪造业务失败 observation。
- `schedule`：只表示真实探针任务的 `scheduled`、`due_waiting`、`running` 或 `none`。内存态以实际 timer 存在为准，Redis 以 due ZSET membership 为准；任务丢失时必须返回 `none`。
- `recoveryAt`：仅用于用户显式策略 TTL，`recoveryAtKind` 固定为 `policy_ttl_expiry`，不能作为探针下次时间。

`until`、失败次数、来源 IP、API Key 数量、探针轮次、半开租约截止和 Redis 内部 `nextProbeAtMs` 都是调度实现字段，不得直接渲染到账户状态卡片。Redis 多节点探针通过 generation + runId 原子取得、提交和清理，旧执行结果不能覆盖新的 observation 或半开租约。

建议保留这些事件语义：

- `gateway_account_failure_observed`：真实请求的本地 transport failure 已记录为诊断样本并首次投递核实；opaque HTTP/协议失败不触发。
- `gateway_account_latency_degraded`：普通路由速度优先确认账号近期首字慢，账号进入速度降级。
- `gateway_account_latency_probe_success`：首字恢复探针达标，速度降级恢复计数推进或已清理。
- `gateway_account_latency_probe_failed`：首字恢复探针未达标，继续速度降级并等待下一轮。
- `gateway_account_local_suppressed`：用户显式策略命中，账号进入配置 TTL 避让；普通失败不触发。
- `gateway_account_recovery_probe_scheduled`：后台探针已调度。
- `gateway_account_recovery_probe_budget_delayed`：探针因本机并发保护、单账号最小间隔或本机 provider / proxy / Base URL 最小间隔被推迟。
- `gateway_account_recovery_probe_stale_result_ignored`：探针结束时 generation 已变化，旧结果已丢弃。
- `gateway_account_recovery_probe_success`：后台探针形成 `framing_complete_neutral`，匹配来源的轻量 transport 怀疑已清理；事件名不表达协议业务成功，也不恢复 `runtime_degraded`、`precheck_pending`、父 account、持久账户或 Key 业务状态。形成匹配 `complete_success` 时另记录对应的业务恢复结果。
- `gateway_account_recovery_probe_failed`：后台探针形成 `transport_failed`，等待下一轮；`unknown` 使用独立结果并递进退避，不得冒充失败。
- `gateway_account_runtime_degraded`：后台确认近期不稳，账号进入调度降级。
- `gateway_account_precheck_scheduled`：后台进入持久状态确认。
- `gateway_account_precheck_failed_marked`：后台确认失败并写入持久状态。
- `background_cooldown_account_retest_retry_exhausted`：冷却复测队列项执行异常且无可用重试；必须附带脱敏后的原始错误，区分 PostgreSQL 参数、事务、DB service 超时和探针执行错误。

日志只记录账号 ID、运行态键、状态、窗口、失败计数、首字耗时、探针结果和短错误摘要，不写完整请求 / 响应 payload。

## 验证要求

改动该状态机时至少验证：

- 单账号高并发多 IP 同一波 transport failure 只影响当前请求和来源 transport 局部回避，并原子首次投递 `recovery_wait`，不建立账户级运行态或持久状态；同一坏会话的 opaque HTTP/协议错误甚至不得投递恢复或形成跨请求回避。
- 普通路由速度优先下，首字慢只记录样本并投递后台核实；只有后台确认后才进入 `latency_degraded`，且不写账号 `temporary_unavailable`、`rate_limited`、`error` 或健康检测失败次数。
- `latency_degraded` 账号在同普通路由分组内后置，未降级硬可承接账号可临时越过账户超级优先、账号优先级、备用层和会话亲和；全部候选都速度降级时旁路排序并保留原候选顺序。
- 后台探针形成匹配来源的 `complete_success` 并清理 `latency_degraded` 后，后续请求重新按账户配置排序，已恢复的主账号不会因为之前切到替补账号而长期饿死。
- 仅可重放文本在首字超阈值且下游尚未写出可见内容时，可按策略限制切换同分组后续账号；图像和其他副作用请求永久退出首字慢样本与速度切号，下游已写出可见内容后也不得透明切号。
- 关闭速度优先或修改路由策略后，对应 `latency_degraded` 运行态必须清理。
- 后台探针 `framing_complete_neutral` 只能按匹配 generation 清理 transport 怀疑；只有匹配来源的 `complete_success` 才能清理系统自动建立的业务运行态 `runtime_degraded` 和 `precheck_pending`，不能提前清理用户显式策略建立的 TTL 避让。
- 后台探针连续独立 `transport_failed` 且并发归零后才写系统自动 transport 来源的 `temporary_unavailable`；任意完整 `4xx/5xx`、`2xx-invalid-body` 或协议失败均不推进。
- 系统自动 transport 来源的 `temporary_unavailable` 在后台复测 `complete_success` 后自动恢复 `active`；用户显式状态只走匹配恢复出口。
- SQLite 与真实 PostgreSQL 都要覆盖冷却复测当前代次成功恢复、当前代次失败累加、错误配置版本拒绝和旧观察起点拒绝；PostgreSQL 回归必须真实执行参数绑定，防止无类型 nullable 参数重新进入写回 SQL。
- 各类请求的 opaque HTTP 与 transport 失败在语义提交前都能按唯一 Key/账户候选救回当前请求，整个请求最多 64 次真实 attempt。
- 旧 generation 探针结果不能覆盖后台 / 主动健康 / 匹配租约半开成功、手动恢复或更新后的状态。
- performance / Redis runtime state 下，多节点同时调度同一运行态允许短暂重复执行；旧 generation 结果不能覆盖或误删新状态，due sweep 能补偿进程重启后的任务。
- `precheck_pending` 在 memory / Redis 下都软阻断普通调度；全池软阻断时健康 / 后备优先、同账户与同分组单飞半开、FIFO 等待均生效。`noAvailableAccountWaitTimeoutSeconds` 控制的 `ServerRetryBudget` 默认 270 秒，只累计零可派发、半开和 FIFO / 并发槽等待；正常上游 attempt 不消耗该累计等待预算。可重放文本的 `GatewayRequestWallBudget` 是另一套固定默认 270 秒绝对墙钟，不能混为一谈。
- 模型 / endpoint 明确不支持时不进入账号级状态升级；恢复探针不能复用失败请求形态，也不能因此把账号打成不可用。
- Mock AI 覆盖失败后恢复、失败后持续不可用、高并发失败风暴和授权实例隔离。
- Mock AI 覆盖 `SUSPECT` 连续 unknown：状态/generation/确认计数不变，due 随递进退避和 jitter 后移，不出现零间隔自旋。
- 父 account scope 达到可配置的通用协议成功证据阈值（当前默认 3）后关闭，不等待所有 child scope 再获流量；仍 `OPEN/RECOVERING` 的 child 不被父关闭抹掉。父子 incident、generation/revision/due/CLOSED ledger 跨重启一致，父 payload 最多 64 个 child ID，第 65 个 child 仍保留独立 incident。
