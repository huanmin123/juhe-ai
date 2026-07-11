# AI 账户运行态探针恢复设计

## 目标

本文固定 AI 账户在真实网关失败、高并发失败风暴、调度降级、临时不可用和恢复探测之间的状态机。核心目标是：

- 用户请求只负责发现失败和写入失败样本，不负责恢复账号。
- 账号恢复、调度降级确认和持久状态确认统一由状态事件触发探针和后台兜底探针完成。
- 高并发或多 IP 同一时间打出的失败不能把账号快速打死。
- 每个运行态和持久态都必须有自动恢复出口，避免长期挂死。
- 高性能模式可能存在多个 Web 节点，跨节点运行态、generation 和 due 索引必须进入 Redis runtime state，不能回退到进程内 memory，也不使用 Redis 分布式锁。

## 总原则

真实请求链路和后台探针链路必须分工：

- 真实请求失败：记录失败样本、短暂避让当前账号、切换后续账号救当前请求。
- 状态事件触发探针：账号进入 `local_suppressed`、`latency_degraded`、`runtime_degraded`、`precheck_pending` 或持久冷却到期时，由状态机调度通用探针。触发源是状态转换，不是“用户请求到来”。
- 后台兜底探针：扫描 due 的探针任务，补偿漏调度、进程重启和无新请求场景。本地运行态由 Web / Redis runtime state 探针恢复，已落库冷却态由 ops-worker 冷却复测恢复。
- 真实请求成功：可以作为强恢复信号，清理运行态观察和调度降级，但不能恢复已经持久化的限流或临时不可用；持久态恢复仍由对应后台复测或显式人工恢复完成。
- 请求数量不能直接驱动状态升级。状态升级必须同时满足最小观察时间、失败窗口、无成功证据和后台探针结果。
- 运行态恢复探针只允许使用系统账户检查探针，也就是共享最小请求执行器；模型固定为账户保存的 `healthCheckModel`。禁止从历史测试、个人默认、管理员默认、协议档案默认、支持模型首项或失败请求回退模型，也禁止复用失败请求的 endpoint、stream 或用户 payload。
- 人工测试是独立诊断流量，成功或失败都不清理、确认、升级或恢复本设计中的任何运行态和持久态。

## 状态分层

| 状态 | 存储位置 | 调度影响 | 触发入口 | 自动恢复 |
| --- | --- | --- | --- | --- |
| `normal` | 无运行态 / `accounts.status = active` | 正常调度 | 后台激活检查成功、探针恢复、运行态清理 | 无需恢复 |
| `failure_observed` | Web 进程运行态样本 | 不影响排序 | 真实请求命中上游后失败 | 成功信号清理；观察窗口过期清理；后台探针成功清理 |
| `latency_degraded` | Web / Redis 短 TTL 运行态 | 速度优先普通路由下未降级硬可承接候选优先，首字慢账号兜底；有效期内可临时覆盖账户偏好 | 普通路由速度优先下首字慢样本达到阈值 | 后台探针连续达标清理；真实请求首字连续达标清理；TTL 过期清理 |
| `local_suppressed` | Web 进程短 TTL 运行态 | 暂不选中该账号 | 真实请求失败后立即止血 | TTL 到期由后台探针验证；探针成功清理；窗口过期且无新失败清理 |
| `runtime_degraded` | Web 进程运行态快照 | 普通候选优先，降级账号兜底 | 后台探针确认近期不稳 | 后台探针连续成功清理；真实完整成功可清理；观察窗口无新失败清理；手动恢复清理 |
| `precheck_pending` | Web 进程运行态快照 / 后台探针队列 | 暂不作为普通候选 | 达到确认条件后由后台探针接管 | 探针成功清理；探针失败且并发归零后写持久态；探针异常按退避重试 |
| `temporary_unavailable` | `accounts.status` / `cooldown_until` | 不参与调度 | 后台探针确认不可用 | 后台冷却复测成功恢复；手动恢复清理 |
| `rate_limited` | `accounts.status` / `cooldown_until` | 不参与调度 | 账户错误策略或探针确认限流 | 后台慢速复测成功恢复；手动恢复清理 |
| `error` | `accounts.status = error` | 不参与调度 | 明确硬异常，例如凭据无效、OAuth 刷新连续失败 | 可自动恢复的异常由对应后台任务恢复；不可自动恢复的异常需要用户修配置或手动恢复 |

`failure_observed` 是内部观察态，不作为前端状态标签展示；`latency_degraded` 只表示当前普通路由速度优先偏好下近期首字慢，不表示账号不可用，也不能升级为持久账号状态。它是路由策略目标对账户偏好的短 TTL 覆盖层，不改写超级优先、账号优先级、备用层、会话亲和或质量排序；恢复清理后，后续请求必须重新回到账户配置排序。账户页只展示会影响调度或需要排障的 `latency_degraded`、`runtime_degraded`、`local_suppressed`、`precheck_pending`、`precheck_failed` 和持久状态。

## 失败采样规则

失败样本按运行态键聚合：

```text
自有账户：accountId
授权实例：accountId + 使用方系统账户 + 本地分组 + 授权 ID
```

采样只接收已经真实进入上游账号调用链路的失败：

- 上游请求异常、连接失败、超时、EOF。
- 上游 HTTP 非 `2xx`。
- 非流式 `2xx` 响应体中断。
- `200 + SSE` 失败事件、缺少终止事件、流中断或空闲超时。
- 代理 profile 准备失败这类账号依赖失败。

不进入账号失败采样：

- 本地 JSON 非法、模型过滤失败、额度不足、授权不可用。
- 分组队列满、账号并发满、单 IP 并发满。
- 客户端主动断开、慢客户端背压。
- IP 级错误熔断和 IP 级账号回避。
- 普通路由速度优先的首字慢样本。它进入 `latency_degraded` 采样窗口，不进入账号失败采样，也不能直接写 `temporary_unavailable`。

高并发去重规则：

- 同一运行态键在很短时间内的大量失败先合并为同一观察窗口内的失败样本，不按请求数快速升级状态。
- 多 IP 只作为可信度维度，不作为立即升级条件。
- 状态升级必须满足最小观察时长；例如刚开始 60 秒内只允许短暂避让和采样，不允许直接写 `runtime_degraded` 或 `temporary_unavailable`。

## 首字慢采样规则

`latency_degraded` 只服务普通路由速度优先，和账号失败状态机分开：

- 采样来源是已经真实进入上游账号调用链路的首字等待。流式请求按首个可见语义输出计算，SSE comment、空心跳和未完成语义事件不算达标；非流式请求按上游 `2xx` 后首个 body 字节计算。
- 采样键必须带当前调用方和路由上下文，建议为 `systemAccountId + routeStrategyId + groupId + accountRuntimeKey`。不同路由策略的速度偏好不能互相污染。
- 窗口内连续慢样本达到策略配置后进入 `latency_degraded`。快样本只清理首字慢连续计数，不清理普通失败样本。
- `latency_degraded` 有效期内，速度优先可以把未降级且硬可承接的同分组账号排到前面，即使被后置账号拥有超级优先、更高账号优先级、主池身份或会话亲和。
- `latency_degraded` 只覆盖账户偏好，不覆盖硬约束；候选仍必须满足账户状态、授权、时间计划、模型能力、协议能力、额度、账号硬并发、分组队列和本地不可调度过滤。
- `latency_degraded` 账号仍可兜底调度；所有硬可承接候选都处于 `latency_degraded` 时应旁路该排序，保留原账户顺序，避免保护机制筛空号池。
- 后台探针或真实请求连续首字达标清理 `latency_degraded` 后，后续选号立即回到账户配置排序，避免恢复账号饿死或替补账号被长期耗尽额度。
- 首字慢不是账号故障。只有真实上游错误、响应中断、探针确认失败或账户错误处理策略命中，才进入 `failure_observed`、`local_suppressed`、`runtime_degraded` 或持久冷却链路。

## 后台探针分层

探针按状态存储位置分两层，避免为了恢复 Web 进程内易失状态而引入额外分布式依赖：

- 运行态恢复探针：处理 `failure_observed`、`latency_degraded`、`local_suppressed`、`runtime_degraded` 和 `precheck_pending` 这类短 TTL 运行态。standalone 模式可以保存在 Web 进程内；performance 模式必须保存在 Redis runtime state，但不使用 Redis 分布式锁。
- ops-worker 冷却复测：处理 `accounts.status = temporary_unavailable / rate_limited` 这类持久状态，按冷却时间和退避策略持续复测，直到恢复、进入长期低频复测或用户手动处理。
- 所有后台系统探针统一采用二元结果：只有完整探测成功才算成功；超时、网络错误、非成功响应、协议错误和业务错误都算本次失败。失败原因只用于日志、状态展示和排障，不能让某类失败跳过失败次数累计或下一次复测时间推进。
- ops-worker 冷却复测必须按 `(cooldown_until, priority, created_at, id)` 复合游标公平扫描，并在扫描到末尾后回绕。已在队列或执行中的账户不能永久占用固定查询窗口，避免前排账户让后续到期账户长期得不到复测。
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
- 相同或更早 `dueAt` 的新意图合并到同一个任务，失败计数和来源维度取最大值或并集。
- 所有探针任务带 `generation`。探针开始时记录 generation，结束回写前必须确认 generation 未变化；如果真实成功、手动恢复或新状态转换已经推进 generation，旧探针结果直接丢弃，旧 generation 也不能覆盖或删除新 generation 状态。
- 调度时间必须带 jitter，避免大批账号在同一秒同时探测。
- 有本机全局并发保护、单账号最小间隔，以及 provider / proxy / baseUrl 维度的本机最小间隔。预算不足时只推迟 due 时间，不把账号升级为更重状态；该预算不进入 Redis 分布式锁。
- 后台 sweep 周期性读取 due 索引补偿漏调度；performance 模式的 due 索引必须在 Redis 中，不能只存在于某个 Web 进程的 timer。
- 恢复探针任务本身不携带失败请求的 model、endpoint、stream、payload 或失败形态摘要；失败现场只作为状态判断、日志和后续人工排障信息。

建议探针分级：

| 级别 | 状态 | 探针策略 |
| --- | --- | --- |
| L1 soft recovery | `local_suppressed` 到期 | 单次账号健康探针，短超时，成功即清 |
| L2 latency verify | `latency_degraded` | 使用账号健康探针校验首字是否回到策略阈值内；连续达标后清理 |
| L2 stability verify | `runtime_degraded` | 低频账号健康探针；必要时连续成功再清 |
| L3 precheck confirm | `precheck_pending` | 完整诊断，多次失败且并发归零后才落库 |
| L4 cooldown retest | `temporary_unavailable / rate_limited` | ops-worker 持久冷却复测，退避更保守 |
| L5 hard error | `error` | 默认不高频自动探，只对可恢复错误触发 |

## Redis / 多实例边界

performance 模式默认可能有多个 server 节点，同一账号的运行态必须满足跨节点一致性：

- 状态转换、probe intent、probe generation 和 due index 进入 Redis runtime state；失败窗口和 success observation 可以保留进程内短窗口，用时间窗口、后台探针和真实成功信号兜底。
- 请求调度链路允许短暂不一致，优先性能。读取 Redis 探针状态时使用 server 进程内短 TTL 近端缓存；缓存过期前可能继续避让或短暂误选，但不能写坏持久状态。
- Redis key 必须有 TTL，不能成为持久事实；Redis 丢失时最多回到保守调度，不能丢账务、授权、审计或使用记录事实。
- due index 用 Redis sorted set 或等价 runtime state 索引保存 `runtimeKey -> dueAt`，各 Web 节点都可以 sweep；重复执行由 generation 条件写入和条件删除收敛。
- 不使用 Redis 分布式锁、分布式全局预算锁、provider 锁、proxy 锁或 baseUrl 锁限制运行态恢复探针；预算只作为本机保护和 jitter，避免 Redis 锁残留或抖动把恢复并发压低。
- Redis 不可用时，高性能模式不能静默退回进程内 memory 作为跨节点事实源；应记录基础设施错误并走保守调度或 fail-fast。
- 状态快照展示可以允许短暂延迟，但状态转换、探针结果回写和探针成功清理必须通过 generation 防止旧结果覆盖或误删新状态。

当前落地边界：

- `RuntimeProbeStateStore` 是运行态探针专用存储。standalone 使用 memory；performance 使用 Redis state URL。
- Redis 存储拆为 JSON 状态、due sorted set 和 generation key。写状态时同时写 due 索引；删除状态时同时清理 due。
- generation 使用 Redis `INCR + PEXPIRE` 原子递增；探针执行和事前确认每次回写前都读取当前 generation，旧结果直接丢弃。
- server 进程启动后会启动 Redis due sweep，周期读取 due 索引并执行到期探针；worker 和 DB service 不执行运行态恢复探针。
- 调度过滤在 Redis runtime state 下读取共享探针状态，但使用 `1s` 正向缓存和 `500ms` 负向缓存降低热路径 Redis 读放大；真实请求成功、手动恢复或持久状态写回会清理共享探针状态和当前进程缓存。
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

失败现场只保留为状态判断和排障信息：

- 连接失败、代理不可用、认证失败、上游 5xx、上游超时等可以进入账号状态机。
- `model_not_found`、`unsupported_endpoint`、模型映射错误、某 endpoint 不支持 stream 等不能直接打死账号。
- `invalid_request`、`context_length_exceeded`、用户 payload 非法、策略拒绝等不影响账号状态。

## 后台探针职责

后台主动探针负责所有恢复和重状态确认：

1. 扫描 due 的失败样本、运行态降级、短暂避让、探针确认态和持久冷却态。
2. 多个 Web 节点可以短暂重复执行同一个运行态键的探针；探针结果回写必须校验 generation，旧探针结果直接丢弃，不能靠 Redis 分布式锁保证唯一执行。
3. 使用系统账户检查探针发起最小请求；模型固定为账户 `healthCheckModel`，不从历史测试、目录默认或失败请求提取 model / endpoint。
4. 运行态恢复探针必须标记 `traffic_source = runtime_recovery_probe`；持久冷却复测继续使用 `traffic_source = cooldown_retest`，两者都不能伪装成真实用户网关流量。
5. 探针使用记录和审计只保留诊断摘要，不参与业务统计或账号质量统计。
6. Web 本地探针成功清理运行态；ops-worker 冷却复测成功时恢复持久状态为 `active`。
7. 探针失败只推进后台状态机，不把失败结果回灌成用户请求失败，也不进入账号质量统计。

## 状态转换条件

### 从 `failure_observed` 到 `local_suppressed`

真实请求失败后可以立即短暂避让，目的是挡住同一波并发风暴：

- 首次失败进入短 TTL 避让。
- TTL 到期后由后台探针检查，不把用户请求作为半开恢复主路径。
- 如果后台探针成功，清理观察和短暂避让。
- 如果后台探针失败，继续短 TTL 或进入更高观察态，但仍需满足最小观察时间。

### 从 `local_suppressed` 到 `runtime_degraded`

只能由后台探针或后台状态评估触发，不能由用户请求次数直接触发。

推荐条件：

- 观察窗口至少持续 `60s`。
- 窗口内至少有多个时间点的失败样本，不能全部来自同一瞬间并发。
- 没有近期真实成功或探针成功。
- 后台探针至少失败 1 次，证明问题不是单次请求抖动。

进入 `runtime_degraded` 后，有普通候选时排后；普通候选不足时仍可兜底尝试，避免账号池被保护机制筛空。

### 从 `runtime_degraded` 到 `precheck_pending`

只能由后台探针持续失败触发。

推荐条件：

- `runtime_degraded` 持续超过最小确认窗口。
- 后台探针连续失败达到确认阈值。
- 当前账号仍没有真实成功信号。

进入 `precheck_pending` 后不再由用户请求恢复；后台探针继续执行确认。

### 从 `precheck_pending` 到持久状态

后台探针连续失败后，如果当前账号仍有在途并发，只保持 `precheck_pending`，等待并发归零。

并发归零后按待确认目标写入：

- 默认写 `temporary_unavailable`。
- 命中账户错误处理策略时，可以写 `rate_limited` 或 `error`。
- 写入原因使用后台探针真实上游 `HTTP/code/message` 摘要，不使用网关最终 `service_unavailable` 兜底文案。

### 持久状态恢复

`temporary_unavailable` 和 `rate_limited` 不允许长期挂死：

- 后台复测固定启用。
- 起始短退避，例如 3 秒。
- 连续失败后指数退避，进入慢速恢复后不超过系统配置的最大暂停时间。
- 超过长期观察阈值后降低频率继续探测，不停止。
- 探针成功恢复 `active`，清理冷却、错误摘要和运行态。

`error` 只用于硬异常：

- OAuth 刷新失败这类可自动恢复异常，由对应后台任务成功后恢复。
- 凭据无效、账号被封禁等不可自动修复异常，需要用户修配置或手动恢复。
- 错误态必须有明确错误码和说明，不能只写模糊“上游失败”。

## 用户请求链路边界

用户请求失败时允许做：

- 记录失败使用记录和审计。
- 写失败样本。
- 短 TTL 避让当前账号。
- 忘记当前会话亲和。
- 切换后续账号或后续分组救当前请求。

用户请求失败时不允许做：

- 直接写 `temporary_unavailable`。
- 直接把账号标为 `runtime_degraded`。
- 因多个 IP 同时失败而绕过最小观察时间。
- 用“用户请求到来”作为探针触发条件。
- 把请求数量当成状态转换依据。

## 恢复出口矩阵

| 状态 | 自动恢复出口 | 人工恢复出口 |
| --- | --- | --- |
| `failure_observed` | 窗口过期；后台探针成功；真实成功 | 手动恢复正常 |
| `latency_degraded` | 后台探针连续首字达标；真实请求连续首字达标；TTL 过期 | 手动恢复正常；关闭对应普通路由速度优先 |
| `local_suppressed` | TTL 到期后后台探针成功；窗口过期 | 手动恢复正常 |
| `runtime_degraded` | 后台探针成功；真实完整成功；观察窗口过期 | 手动恢复正常 |
| `precheck_pending` | 后台探针成功 | 手动恢复正常 |
| `temporary_unavailable` | 后台冷却复测成功 | 手动恢复正常 |
| `rate_limited` | 后台慢速复测成功 | 手动恢复正常 |
| `error` | 对应后台任务成功，例如 OAuth 刷新恢复 | 修复配置后由后台复检；恢复异常 |

## 日志和排障

建议保留这些事件语义：

- `gateway_account_failure_observed`：真实请求失败已记录为样本。
- `gateway_account_latency_degraded`：普通路由速度优先确认账号近期首字慢，账号进入速度降级。
- `gateway_account_latency_probe_success`：首字恢复探针达标，速度降级恢复计数推进或已清理。
- `gateway_account_latency_probe_failed`：首字恢复探针未达标，继续速度降级并等待下一轮。
- `gateway_account_local_suppressed`：账号进入短暂避让。
- `gateway_account_recovery_probe_scheduled`：后台探针已调度。
- `gateway_account_recovery_probe_budget_delayed`：探针因本机并发保护、单账号最小间隔或本机 provider / proxy / Base URL 最小间隔被推迟。
- `gateway_account_recovery_probe_stale_result_ignored`：探针结束时 generation 已变化，旧结果已丢弃。
- `gateway_account_recovery_probe_success`：后台探针成功，运行态已恢复。
- `gateway_account_recovery_probe_failed`：后台探针失败，等待下一轮。
- `gateway_account_runtime_degraded`：后台确认近期不稳，账号进入调度降级。
- `gateway_account_precheck_scheduled`：后台进入持久状态确认。
- `gateway_account_precheck_failed_marked`：后台确认失败并写入持久状态。

日志只记录账号 ID、运行态键、状态、窗口、失败计数、首字耗时、探针结果和短错误摘要，不写完整请求 / 响应 payload。

## 验证要求

改动该状态机时至少验证：

- 单账号高并发多 IP 同一波失败只进入短暂避让，不进入 `runtime_degraded` 或持久状态。
- 普通路由速度优先下，首字慢只进入 `latency_degraded`，不写账号 `temporary_unavailable`、`rate_limited`、`error` 或健康检测失败次数。
- `latency_degraded` 账号在同普通路由分组内后置，未降级硬可承接账号可临时越过账户超级优先、账号优先级、备用层和会话亲和；全部候选都速度降级时旁路排序并保留原候选顺序。
- `latency_degraded` 恢复清理后，后续请求重新按账户配置排序，已恢复的主账号不会因为之前切到替补账号而长期饿死。
- 首字超阈值且下游尚未写出可见内容时，可按策略限制切换同分组后续账号；下游已写出可见内容后不得透明切号。
- 关闭速度优先或修改路由策略后，对应 `latency_degraded` 运行态必须清理。
- 后台探针成功能清理 `failure_observed`、`latency_degraded`、`local_suppressed`、`runtime_degraded` 和 `precheck_pending`。
- 后台探针连续失败且并发归零后才写 `temporary_unavailable`。
- `temporary_unavailable` 后台复测成功能自动恢复 `active`。
- 所有用户请求失败路径仍能切号救回当前请求。
- 旧 generation 探针结果不能覆盖真实成功、手动恢复或更新后的状态。
- performance / Redis runtime state 下，多节点同时调度同一运行态允许短暂重复执行；旧 generation 结果不能覆盖或误删新状态，due sweep 能补偿进程重启后的任务。
- 模型 / endpoint 明确不支持时不进入账号级状态升级；恢复探针不能复用失败请求形态，也不能因此把账号打成不可用。
- Mock AI 覆盖失败后恢复、失败后持续不可用、高并发失败风暴和授权实例隔离。
