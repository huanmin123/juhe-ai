# AI 账户多模型能力健康与精确隔离设计

## 1. 文档定位

本文定义一个 AI 账户同时承载多个模型、多个 endpoint mode、多个协议转换路径和多个上游凭据时，生产能力健康、故障隔离、调度过滤、主动探针、半开恢复、存储、接口、前端、监控、迁移和回滚的完整目标契约。

本文一次性描述最终交付，不拆成可独立上线的半成品。只有网关、控制面、worker、PostgreSQL、Redis、Asynq、管理 API、前端、监控、迁移和回归一起闭环后，功能才允许启用。

与本文冲突时：

- 模型执行能力作用域、请求失败触发和能力恢复以本文为准。
- transport 生命周期事实仍以 [AI 账户错误语义与状态变更边界](AI账户错误语义与状态变更边界.md) 为准。
- 账户激活、用户显式策略和硬状态恢复仍以各自专题为准。
- 人工测试保持纯诊断，不因本文变成生产健康写入口。

## 2. 问题与最终结论

账户支持模型 A、B 时，B 可能因为模型权限、endpoint 能力、协议转换或特定凭据而不可用，A 仍然正常。账户级 active / temporary_unavailable 无法表达这种局部事实。继续使用单一 healthCheckModel 复测，也无法证明失败请求的真实能力是否恢复。

最终结论固定为：

1. 在账户硬状态和 transport 电路之间增加稀疏的模型执行能力层。
2. 负向状态绑定到最终真实派发描述，不绑定客户端模型别名。
3. 多 API Key 账户还必须绑定到本次选中的非敏感凭据作用域，不能用一个 Key 的结论覆盖全部 Key。
4. 普通请求失败只产生候选证据；只有受控独立执行探针可以确认能力不可用或恢复已确认阻断。
5. 不按 HTTP 状态码、错误码、错误类型或正文推断凭据失效、限流、封禁、模型下线等具体语义。完整失败只形成 capability_unavailable 通用结论。
6. 模型能力层永不写入或清理 accounts.status。即使全部配置能力都已阻断，也只形成派生的 instance_all_capabilities_unavailable 调度门禁。
7. 现有 protocol_model transport 子电路不得再通过“任意多个子 scope”自动打开父账户电路；否则单个坏模型仍能绕过本文隔离并拖死整号。
8. healthCheckModel 保留为激活和周期哨兵，不代表全部模型。

## 3. 目标与明确限制

### 3.1 目标

- B 被确认不可用后，只过滤 B 对应的真实能力；A 和 B 的其他可用路径继续调度。
- 同一波并发失败只产生一个同作用域探针意图，5 分钟内不重复放大。
- 阻断能力可以在无新用户流量时进入 half-open、recovering 并自动恢复。
- 多节点、迟到结果、配置变化、授权实例和 Redis 重建均有 fencing 与保守恢复边界。
- 页面不能在全部能力已阻断时继续只显示绿色“可调度”。
- 未观察能力始终显示 unknown，不能被一个成功模型冒充为“全部可用”。

### 3.2 明确限制

- 本文不根据上游错误内容自动建立 rate_limited、error、disabled 等业务语义。
- 本文不把模型目录可见性等同于真实执行能力。
- 默认禁止后台付费生图、音视频等高成本 E2E。没有受控真实执行配方时，只能保持 unknown 或短时 suspect，不能承诺自动硬隔离。
- 本文不周期扫描账户全部模型。能力事实由真实业务成功、失败触发的独立探针、哨兵探针和人工生产重检稀疏形成。
- 本文不新增 SQLite 长期实现。完整目标 owner 为 Go gateway/control worker + PostgreSQL + redis-state + Asynq。

## 4. 三层状态与优先级

### 4.1 账户持久状态

现有枚举保持：

- active
- pending_test
- disabled
- error
- rate_limited
- temporary_unavailable
- quality_isolated

模型能力成功不得恢复或覆盖上述任何硬状态。pending_test 的激活、24 小时失败转 error、用户显式策略 TTL 和 quality_isolated 均继续由原 owner 管理。

### 4.2 模型执行能力状态

对外状态映射到现有 circuit phase：

| 对外状态 | circuit phase / 事实 | 调度语义 |
| --- | --- | --- |
| unknown | 没有新鲜正向事实，或 CLOSED 正向事实已过期 | 允许调度 |
| available | CLOSED 且正向事实仍新鲜 | 允许调度 |
| suspect | SUSPECT | softAvoidUntil 内优先避让，过期后可调度但不重复建探针 |
| temporarily_blocked | OPEN | 普通调度排除 |
| half_open | HALF_OPEN | 只允许持租约的独立探针 |
| recovering | RECOVERING | 普通调度排除，等待连续成功 |

能力状态不是新的 accounts.status 枚举。

### 4.3 transport 电路

transport 电路只解释连接、硬超时、读取中断和 framing 是否完整。能力探针的同一次真实执行可以同时产生两套正交结果：

- 对 transport：完整 HTTP / SSE framing 为中性或成功，真实不完整传输为负向。
- 对 capability：协议完整成功为正向；完整失败响应和真实不完整传输都为通用 capability_unavailable。

两套状态共享最终派发描述，但不得共享 outcome 解释器或租约。能力 worker 始终持有 capability intent claim；只有 transport owner 同时判定该精确 transport scope 到期并另行授予 matching generation / lease 时，才允许同一次执行改变 transport phase。没有 transport lease 时只记录 transport observation，不能顺手确认、打开或关闭 transport 电路。完整 HTTP 对 transport 是 framing complete，对 capability 可以是 unavailable；两份 outcome 必须分别 fenced 提交。

### 4.4 最终门禁顺序

最终可执行性依次取交集：

1. 分组边界、账户硬状态、到期、可用时段、额度和真正账户全局的父 transport 电路。
2. 为 Route 枚举当前 revision 下仍存在的凭据槽位；Route 级摘要只能证明“可能至少有一个 Key”，不能代替精确门禁。
3. 对每个 Key 生成新的不可变 Attempt 描述，同时求交该 Key 的显式策略 / 运行态、精确 transport 电路和精确 model_capability 状态。
4. 在仍可执行的 Attempt 中按 Key 池策略选择一个；需要半开或 canary 时先取得该 Attempt scope 的匹配租约。
5. 最后校验并发、会话、请求墙钟和 attempt 预算后派发。

Key-1 失败后轮换 Key-2 时，必须从同一个 Route 描述派生新的 Attempt 描述，重新执行第 3 到 5 步并取得新的 circuit attempt / lease；禁止修改或复用 Key-1 的描述、门禁结果或租约。

任何下层成功都不能清除上层阻断。

## 5. 核心不变量

1. 单模型、单 endpoint、单转换路径或单 Key 的失败不得写整号 temporary_unavailable。
2. 模型能力层永不修改 accounts.status。
3. 普通业务失败和完整失败响应不得直接写 OPEN。
4. 只有真正派发到上游且可精确归因的 attempt 才能成为能力探针候选。
5. retryEligible 与 capabilityProbeEligible 是两个独立判断；下游已经提交只禁止切号，不自动吞掉旁路探针。
6. 业务成功只能使用 finalizer 的 protocolValidatedSuccess，不能用 upstreamResponse.ok、收到响应头或 dispatch 返回代替。
7. 同一请求内，相同完整派发描述后续取得 protocolValidatedSuccess 时，必须取消该描述较早的失败候选。
8. 独立探针完整失败不区分 401、403、429、500、502、503、错误 JSON、HTML 或供应商文案。
9. task failure、取消、过期、无合法模板、目录缺失和 stale 不得伪造 capability_unavailable。
10. 任意结果写回同时校验 effectiveDispatchRevision、capabilityUniverseRevision、generation、ledgerRevision、claimToken、fencingToken 和有效租约。
11. Redis miss 在账户 runtime projection 未 ready 时不得解释为 unknown。
12. 人工测试零生产副作用；生产“重新检查”是另一个有权限、审计和预算的命令。
13. Collector 只接收普通用户 gateway traffic；激活、周期哨兵、capability probe、transport recovery、冷却复测、人工测试和生产重检都直接提交各自 outcome，绝不再次进入 Collector。
14. Collector flush 失败或超时不得覆盖、延迟或改变原用户响应；只有 durable ledger 已提交才能返回 admission accepted。

## 6. 完整架构

~~~mermaid
flowchart LR
  Request["用户请求"] --> Plan["最终路由与派发规划"]
  Plan --> RouteDescriptor["ResolvedGatewayRouteCapability"]
  RouteDescriptor --> BatchGate["批量能力门禁"]
  BatchGate --> KeySelect["过滤并选择 Key"]
  KeySelect --> AttemptDescriptor["ResolvedGatewayAttemptCapability"]
  AttemptDescriptor --> Upstream["真实上游 attempt"]
  Upstream --> Finalizer["协议 finalizer"]
  Finalizer --> Collector["请求级 CapabilityFailureCollector"]
  Collector --> Admission["能力意图原子 admission"]
  Admission --> Runtime["现有 circuit runtime store"]
  Admission --> Ledger["PostgreSQL circuit ledger + intent + outbox"]
  Ledger --> Queue["Asynq 能力探针"]
  Queue --> Probe["独立最小执行探针"]
  Probe --> Outcome["CapabilityProbeOutcome"]
  Outcome --> Ledger
  Ledger --> Projector["outbox projector / reconciler"]
  Projector --> Runtime
  Runtime --> BatchGate
  Ledger --> API["管理 API / 小时聚合"]
~~~

唯一状态机是现有 account circuit 领域引擎扩展出的 model_capability scope policy。不得再创建一套 AccountCapabilityHealthStore，在 PostgreSQL、Redis、gateway 和 worker 各复制转换条件。

## 7. 最终派发描述与能力作用域

### 7.1 两级不可变描述

最终路由规划器一次生成、沿 attempt 全链路传递：

~~~text
ResolvedGatewayRouteCapability
  accountRuntimeKey
  runtimeAccountId
  credentialSourceAccountId
  providerProtocolProfileId
  adapterRouteKey
  upstreamEndpointMode
  requestLane
  normalizedUpstreamModel
  effectiveDispatchRevision
  capabilityUniverseRevision

ResolvedGatewayAttemptCapability extends ResolvedGatewayRouteCapability
  credentialScopeKey
~~~

Route 描述用于账户级批量预过滤；Attempt 描述在选中具体 Key 后形成，用于真实派发、usage、transport circuit、失败收集、探针意图和结果写回。

adapterRouteKey 区分 direct chat、responses-to-chat、messages-to-chat、Gemini bridge 等不同请求转换管线。两个别名只有在最终上游模型、endpoint mode、lane、转换管线和凭据作用域全部相同时才共享能力状态。

### 7.2 credentialScopeKey

- 多 API Key 账户使用现有 HMAC Key 指纹，不保存明文。
- 单 Key 和 OAuth 使用持久的凭据槽位身份。普通 OAuth access token 刷新以及同一授权 lineage 内的 refresh token 轮换不改变 credentialScopeKey，也不推进能力 revision；只有 API Key 替换、明确重新授权到不同主体 / 权限集合、凭据槽位重建或授权 lineage 变化才失效旧 scope。
- 授权实例的逻辑状态仍按 accountRuntimeKey 隔离；物理探针预算按 credentialSourceAccountId 合并。
- Key-1 的 B 失败只阻断 Key-1 + B。只要 Key-2 + B 仍可执行，账户对 B 仍可调度。
- 能力成功不能恢复 Key 的 temporary_unavailable / rate_limited / error；Key 专题仍是唯一 owner。

### 7.3 生成顺序

生成顺序固定为：

1. 静态账户资格和可见模型过滤。
2. 对每个账户解析模型映射、协议桥接、最终 lane、上游 endpoint mode 和 adapterRouteKey。
3. 形成 Route 描述并批量加载当前 Key 集合与能力门禁。
4. 执行既有排序并选择账户。
5. 枚举 Key；每个 Key 都先形成自己的 Attempt 描述，再对 Key 状态、精确 transport 和精确 capability 求交，随后选择一个可执行 Attempt。
6. 取得该 Attempt 所需的 circuit lease 并派发；Key 轮换必须回到第 5 步生成新描述，禁止原地替换 credentialScopeKey。
7. 将本次选中的同一个 Attempt 描述传到 fetch、finalizer、usage、circuit 和 failure collector；禁止在返回阶段从 req / account 再推导一次。

### 7.4 模型规范化与编码

normalizedUpstreamModel 来自 provider driver 对最终上游模型 ID 的显式 canonicalizer：

- 默认只去除输入校验已禁止的外围空白，保留大小写和 Unicode 字节语义。
- 只有 provider 明确声明大小写不敏感时才允许折叠大小写。
- 不得继续沿用全局 lowercase modelBucket。

Route 和 Attempt 分别使用版本化二进制编码 `capability-route-scope/v1` 与 `capability-attempt-scope/v1`。编码器按固定字段顺序写入字段编号、四字节大端长度和 UTF-8 字节，分别得到内部 `routeScopeKey` / `attemptScopeKey`，再计算 SHA-256 得到外部 `routeScopeId` / `scopeId`。`routeScopeId` 不含 `credentialScopeKey`，用于批量门禁、哨兵水位和管理聚合；`scopeId` 在同一 Route 字段后追加 `credentialScopeKey`，用于精确 incident、探针、Key 级调度和 admission 唯一身份。文档和新代码禁止再使用含义不明的 `scopeHash`；既有数据库列名 `scope_key` 在 model_capability 行中固定保存 `scopeId`，不是原始编码字节。Node 与 Go 必须共享两组 golden vectors；接口只暴露不可逆 ID。

版本字段不进入 scope hash，而作为 fencing：

- effectiveDispatchRevision：凭据、Base URL、代理、协议档案、转换目标等真实派发身份。
- capabilityUniverseRevision：支持模型、endpoint modes、映射可达性、probe strategy、Key 集合和授权实例能力目录。

名称、备注、优先级等无关编辑不得推进这两个 revision。映射目标变化同时推进两者；只改变可选支持目录时只推进 capabilityUniverseRevision。

## 8. CapabilityScopeCatalog

### 8.1 配置侧目录

unknown 不能依赖事实表空行猜测。配置 owner 必须用与网关相同的规划器生成 CapabilityScopeCatalog：

- 每行是一个不含 credentialScopeKey 的可路由 Route 描述。
- 只枚举模型目录、账户 supportedModels、endpoint modes 和映射共同允许的真实路由，不做盲目模型乘 endpoint 全笛卡尔积。
- 记录 probeStrategy、最小探针配方版本和 capabilityUniverseRevision。
- Key 集合保持在现有 Key 目录；查询时与稀疏能力 incident 合并，不物化全部 Route × Key 行。

账户列表摘要、能力明细、all configured capabilities blocked 派生结论都必须以同一 revision 的 Catalog 为全集。任一 unknown、stale 或尚未加载的 Key 都不能被算成“已确认不可用”。

### 8.2 容量

- 合法配置默认最多 8192 个 Route 描述，部署下限不得低于现有 500 模型乘全部适用 endpoint mode 的产品上限。
- 超出限制在账户保存时拒绝并返回明确配置错误，不能运行时静默截断。
- capability incident 持久层不设 512 行硬上限；活动负向状态不得为腾容量被删除。
- 512 只可作为单进程热缓存默认容量。CLOSED / unknown 可淘汰；负向 miss 必须按账户有界 lazy-load，容量耗尽返回现有 capacity sentinel。

## 9. 请求证据与失败收集

### 9.1 普通业务成功

只有 finalizer 形成 protocolValidatedSuccess 才是正向能力观察：

- 没有 active intent 且当前没有确认负向状态时，可以建立或刷新 available。
- 热门 scope 的正向持久写按 scope 节流，默认最多 15 分钟一次；每请求成功不得同步热写 PostgreSQL。
- active intent 存在时，普通成功只原子递增该 scope 的 positiveObservationVersion 并写有界 observation，不由业务路径直接终结 generation。
- OPEN / HALF_OPEN / RECOVERING 只能由匹配 generation 的独立探针推进恢复。
- OPEN / HALF_OPEN / RECOVERING 期间出现同 scope 业务成功时不直接恢复，但把恢复 due 提前到当前时间；仍需独立探针确认。

available 正向事实默认 24 小时后只在展示层回落 unknown；调度上两者都允许。

### 9.2 请求级 CapabilityFailureCollector

Collector 挂到跨账户重试和后备分组共享的 request attempt tracker：

1. 只有 `trafficSource = user_gateway` 的普通用户流量进入 Collector。激活、周期哨兵、capability / transport probe、冷却复测、人工测试和生产重检不得递归产生新 intent。
2. 每个已经真实派发、终态未形成 protocolValidatedSuccess、失败不是客户端取消或本地前置原因且可以精确归因的 attempt，加入完整 Attempt 描述和非敏感 probe recipe；这包括下游提交后的协议失败、失败终态事件、畸形完成和读取中断。
3. 客户端取消、本地鉴权 / 配额失败、非法请求体、未派发、主动配置截止取消等不加入。retryEligible 与 capabilityProbeEligible 分别计算。
4. 同一请求后续在完全相同 Attempt 描述上成功时，删除较早失败候选。不同 Key 不属于同一描述，不互相取消。
5. 所有路由、流式 finalizer 和异常清理结束后，在最外层 finally、释放 request tracker 与 audit capture 之前只 flush 一次；B 失败后 A 成功救回也必须 flush B。
6. flush 是非抛出、有严格短时限的旁路操作。超时、IPC、Redis 或 PostgreSQL 错误只记指标和有界日志，不能覆盖原响应、延迟连接收尾或改变 audit 终态；不得留下无界 detached Promise。控制面只有在 PostgreSQL ledger 事务已提交后才返回 accepted，超时 / 未知按 non-accepted 处理，由后续真实失败重新触发。
7. 候选去重后按 hash(traceId, scopeId) 形成确定性轮转顺序。逐个调用 admission；already_running、cooling_down、unsupported 和 saturated 继续尝试下一个，首个 accepted 后停止。
8. 每个用户请求最多接受一个新探针意图。Collector 本身有界，不超过统一真实 attempt 上限。

旧 request_failure -> accountId -> healthCheckModel 派发路径必须与新 Collector 互斥关闭，不能同时触发两次探针。

## 10. 独立探针与结果契约

### 10.1 ProbeRecipe

ProbeRecipe 是每个 generation 的非敏感不可变执行配方：

~~~text
recipeVersion
sourceModel
sourceEndpointFamily
streamShape
adapterRouteKey
adapterImplementationRevision
plannerContextKey
sourceRequestShapeKey
minimalPayloadKind
probeStrategy
expectedRouteDescriptor
expectedCredentialScopeKey
effectiveDispatchRevision
capabilityUniverseRevision
~~~

`plannerContextKey` 和 `sourceRequestShapeKey` 由 provider driver 生成，是版本化、非敏感、可确定重建最小请求的枚举 / 摘要；不得保存客户端 prompt、附件或原始 payload。worker 重新读取当前凭据，使用同一规划器和 adapter implementation revision 重新解析配方，并逐字段核对 expected Attempt 描述。任何不一致返回 stale，不得回退到账户哨兵模型，也不得直接拿 normalizedUpstreamModel 绕过映射链路。

### 10.2 ProbeStrategy

| 策略 | 证据能力 | 调度副作用 |
| --- | --- | --- |
| execution | 真实最小协议执行 | 可确认 unavailable、available 和恢复 |
| catalog_only | 只检查目录可见性 | 只展示 visibility；任务以 catalog outcome 中性终结并恢复此前 execution phase，不建立 5 分钟业务冷却 |
| manual_costed_execution | 每次都需要物理账户所有者显式允许成本 | 普通业务失败只留诊断和请求内避让，不创建共享 intent / SUSPECT；带本次成本授权的生产重检可形成硬结论 |
| unsupported | 没有安全配方 | admission 直接返回 unsupported，保持 unknown，不创建 intent、soft avoid 或冷却 |

目录中存在模型不能恢复 execution scope；目录缺失也不能证明执行失败。图片、音视频等默认 catalog_only 或 manual_costed_execution。manual_costed_execution 的一次性授权只来自生产重检命令 `{ authorizeCostedExecution: true, maxEstimatedCostUsd }`，后端必须校验物理所有者权限、估算上限、部署级日预算和 Idempotency-Key，并写操作日志；它不变成账户永久开关。该探针若确认 OPEN，自动恢复不得再次消费成本，状态显示 `requires_cost_authorization`、`nextProbeAt = null`，直到新的有权限成本授权重检成功或配置 revision 使旧状态 stale。只有未来另行设计有预算 owner 的自动付费 policy，才可承诺自动硬隔离与恢复。

### 10.3 稳定结果

| outcome | 定义 | 能力状态 |
| --- | --- | --- |
| complete_success | 最小请求通过协议校验且 framing 完整 | 正向 |
| capability_unavailable | 独立执行形成任意完整失败，或真实 transport 不完整 | 通用负向 |
| probe_task_failure | 队列、进程、DB、临时配置读取、执行器、取消等本地任务失败 | 中性，任务重试 |
| stale | revision、generation、租约、权限上下文或描述已变化 | 丢弃 |
| catalog_visible / catalog_unknown | 目录可见性观察 | 不改变 execution 状态 |

结果中不保存或解释上游正文。HTTP 状态和错误详情仍可留在现有受权限保护的 usage / audit 诊断记录，但不得进入能力决策或 failure class。

### 10.4 本地账户级事实

以下分类互斥：

- 已确定的凭据缺失 / 解密失败、无效 Base URL、不可构造代理或协议配置：由现有账户配置 owner 处理账户级状态。
- 队列不可用、数据库暂时失败、worker 崩溃、临时配置读取失败：probe_task_failure。
- 某个模型没有合法探针模板：unsupported / unknown。

不得把同一异常同时记为 task failure 和整号故障。

### 10.5 Key owner 门禁

- worker 执行前重新读取目标凭据槽位和 Key owner 状态；能力重检永远不能旁路、清理或恢复 Key 状态。
- Key 已删除、凭据槽位 / key-set revision 已变化或授权绑定失效：intent 以 stale fenced 终结，撤销当前 generation 的 provisional soft avoid，不产生 capability outcome。
- Key 当前处于 temporary_unavailable、rate_limited、error 或 disabled：初始 SUSPECT intent 中性终结并恢复此前稳定 phase；已经 OPEN / HALF_OPEN / RECOVERING 的 scope 保留原 phase，取消本次 claim，并由 Key owner 的恢复事件重新唤醒 capability due，不做忙循环。
- Key owner 恢复事件只重新安排原 capability scope 的独立探针，不能直接把能力改 available。

## 11. 状态机

### 11.1 触发

- 业务失败 admission：CLOSED / 无行 -> SUSPECT，并建立默认 30 秒 softAvoidUntil。
- 周期哨兵或人工生产重检：统一经过 durable admission、generation、claim、预算和 fencing，但在 unknown / available 上只附加 verification intent，保持稳定 phase 且不建立 soft avoid；独立结果成功保持 available，失败才进入 OPEN。
- 已处于 SUSPECT、OPEN、HALF_OPEN、RECOVERING 或存在 active intent 时不创建新 generation。
- OPEN 到期由恢复调度器获取 lease 后进入 HALF_OPEN。

30 秒只是共享软避让窗口，不是探针 hard deadline。窗口过期后 SUSPECT 仍保留 active intent 和单飞，但普通调度可以重新使用该 scope，避免合法长探针造成长时间全池停摆。

### 11.2 完整转移矩阵

| 当前状态 | complete_success | capability_unavailable | probe_task_failure / lease 过期 | stale |
| --- | --- | --- | --- | --- |
| unknown / available + verification intent | available | temporarily_blocked | 回到此前稳定展示；不增加失败 | 不变 |
| suspect | available | temporarily_blocked | 同 generation 有界重试；耗尽后回到此前稳定展示 | 不变 |
| temporarily_blocked | 不直接执行；到期先 half_open | 保持 blocked | 保持 blocked，按任务退避 | 不变 |
| half_open | recovering，成功数 1 | blocked 并推进能力退避 | 回 blocked，不增加能力失败次数 | 不变 |
| recovering | 达到 2 次则 available，否则保持 recovering | blocked 并推进能力退避 | 保持 recovering，不增加能力失败次数 | 不变 |

初始能力退避为 5、10、20、30、60 分钟，之后保持 60 分钟并加入确定性正负 20% jitter。第一次恢复成功后至少间隔 30 秒再执行第二次。能力策略参数由 scope policy 提供，不修改 transport scope 的既有参数。

task retry 使用 30 秒、1 分钟、2 分钟、5 分钟退避。初始确认任务达到配置的最大任务重试后终结 intent，释放 SUSPECT 为此前稳定展示；已确认 OPEN / RECOVERING 不因基础设施失败被恢复为健康。

### 11.3 租约与时钟

- 文本 execution 初始 hard deadline 为 75 秒，覆盖现有最大 60 秒执行策略和安全余量。
- 图片付费 execution 初始 hard deadline 为 135 秒，覆盖现有 120 秒执行策略和安全余量。
- catalog probe 初始 hard deadline 为 30 秒。
- 以后新增 lane 必须先定义执行器 hard timeout，再令 lease 覆盖 hard timeout + 安全余量。

结果提交必须满足 claimUntil 大于数据库当前时间。PostgreSQL 使用 CURRENT_TIMESTAMP，Redis Lua 使用 Redis TIME；客户端 Date.now 只用于展示。长任务续租必须携带 fencing token，失去租约立即取消，迟到结果只能记 stale。

### 11.4 业务成功竞态

业务请求不直接终结 active intent，但不能让较旧的负向探针覆盖较新的同 scope 成功：

1. worker claim 时把当前 `positiveObservationVersion` 固化为 `claimedPositiveObservationVersion`。
2. 同 scope 的 protocolValidatedSuccess 原子递增该 version；不同 Key 或不同 Attempt 不影响它。
3. `capability_unavailable` outcome 的 CAS 除全部 fencing 外，还要求当前 version 等于 claim 值。若已变化，控制面把 intent 终结为 `superseded_by_newer_success`，不提交负向 phase；unknown / available 保持稳定，已 OPEN 的 scope 保持 OPEN 并立即安排新的恢复验证。
4. complete_success 可以正常提交；业务成功发生在 claim 之前时已包含在 fence 内，后续独立失败仍可生效。

因此终结动作仍由 intent owner 完成，普通业务成功不能直接恢复 OPEN；但“较早探针失败、较晚业务成功、失败结果再迟到”不会误开电路。

配置变更通过 revision 使整代 intent stale；清理不得删除 generation floor 后允许旧结果盲 upsert。

## 12. 单飞、5 分钟冷却与预算

### 12.1 原子 admission

单飞身份固定为精确 Attempt `scopeId`，不包含由调用方预生成的 generation。admission 原子操作内部完成：

1. 校验 runtime projection revision、当前 phase、active intent 和业务触发冷却。
2. 校验 global / physical account pending 与 running 容量。
3. 只有 winner 分配下一 generation、reservation token 和 softAvoidUntil。
4. 以 scopeId + generation 唯一约束写 durable intent。

两个 gateway 不得各自生成 generation 后用不同 Redis key 加锁。

### 12.2 业务触发冷却

- 同一完整 Attempt scope 在一个 durable intent 被接受后，5 分钟内不再接受新的业务失败触发。
- active intent 优先于冷却；未终结前永不创建下一 generation。
- OPEN / HALF_OPEN / RECOVERING 只由恢复调度器推进。
- task failure 重试同一 intent，不依赖新用户请求，也不重新开始 5 分钟窗口。
- queue_saturated / unsupported / revision_stale 不创建 SUSPECT，也不开始冷却。

人工生产重检使用独立的 30 秒用户 / 账户命令限频，可以绕过业务触发冷却，但不能绕过 active intent、物理预算、hard deadline、cost policy 或 fencing。

### 12.3 逻辑与物理预算

- 每个 accountRuntimeKey 同时最多一个能力探针。
- 每个 credentialSourceAccountId 同时最多一个能力探针，等待最多 16 个。
- 全部署 pending 默认上限 4096，running 继续复用共享账户诊断 limiter；具体 lane 并发由现有探针限制统一配置。
- 调度采用 due physical accounts 集合加每账户队列轮转，不用一个 scope 时间 ZSET 冒充公平队列。
- 物理执行单飞使用独立 `PhysicalProbeExecutionKey`，明确排除 accountRuntimeKey、runtimeAccountId、系统账户和授权实例身份。它包含 credentialSourceAccountId、稳定 credentialScopeKey、协议档案、Base URL / proxy / egress 不可逆指纹、adapterRouteKey、adapterImplementationRevision、最终模型、endpoint、lane、recipeVersion、plannerContextKey 和 sourceRequestShapeKey。只有该 key 完全相同的逻辑 intent 才合并一次执行；结果仍按各自 runtime binding、revision、generation 和 lease 分别 fenced 写回。
- 队列满时 admission 直接返回 saturated，不建立 soft block。替换 pending 项必须在同一原子操作中终结旧 intent 并解除旧 soft block。

## 13. 存储、控制面与崩溃一致性

### 13.1 唯一状态引擎

扩展现有 account circuit 基础设施：

- AccountCircuitScope 增加 model_capability。
- AccountCircuitStore 继续负责 phase、lease、generation、due、capacity sentinel 和原子状态转换。
- account_circuit_incidents 继续作为可重建 durable ledger。
- account_circuit_outbox 继续负责 PostgreSQL 到 redis-state 的投影与 revision tombstone。
- control-plane bridge / reconciler 继续负责重建、按账户 lazy-load 和 projection gap 修复。

不得新增第二套 account_capability_health 当前状态表。

### 13.2 Schema 扩展

account_circuit_incidents 为 model_capability 增加：

- credential_scope_key
- provider_protocol_profile_id
- adapter_route_key
- upstream_endpoint_mode
- normalized_upstream_model
- capability_universe_revision
- soft_avoid_until_ms
- probe_strategy
- last_probe_outcome
- last_probe_at_ms
- last_business_success_at_ms
- positive_observation_version
- positive_evidence_expires_at_ms

新增 account_capability_probe_intents，只保存任务生命周期，不复制健康 phase：

- intent_id、scope_id、generation
- runtime_account_id、account_runtime_key、credential_source_account_id
- binding_system_account_id、bound_group_id、account_authorization_id（自有账户可为空）
- effective_dispatch_revision、capability_universe_revision
- recipe_version、probe_recipe_json
- claimed_positive_observation_version
- purpose、status、available_at
- claim_token、claim_owner、claim_until、fencing_token
- hard_deadline、attempt_count、terminal_outcome
- trace_id、created_at、updated_at

唯一约束为 scope_id + generation；active intent 另有条件唯一约束。既有 `account_circuit_incidents.scope_key` 对 model_capability 保存同一个 `scopeId`；migration 必须同步扩展 `scope_kind` CHECK 约束。worker 只用 runtime_account_id + 三个绑定上下文字段向权威 repository 加载当前权限，不解析 opaque runtime key。payload 不包含 API Key、OAuth token、用户 prompt、附件或上游正文。

新增 account_capability_scope_catalog，保存当前 revision 的 Route 描述和 probe strategy；由配置事务 / outbox 唯一 owner 重建。

### 13.3 提交顺序

沿用现有 runtime + durable control-plane saga，但明确责任：

- Redis 是 ready 状态下的调度运行态权威。
- PostgreSQL ledger 是重启、审计、API 和重建的持久权威。
- Redis reservation 只是 provisional；只有 ledger 事务提交 incident、intent 和 outbox 后 admission 才返回 accepted。
- 提交后用 reservation token 标记 Redis committed；若该步失败，outbox projector 必须补齐，账户在补齐前返回 runtime_state_unconfirmed，不能按 unknown 放行。
- ledger 事务失败时用 token 补偿 provisional reservation；无法确认补偿时同样进入账户级 projection gate。
- `CapabilityProbeTaskEnvelope` 与 ProbeRecipe 分离，只包含 taskId=intentId、intentId、scopeId、generation、两个 revision 和最小 owner 定位信息；Asynq 使用 intentId 作为幂等 TaskID。outbox 只有在 enqueue 成功或已存在同 TaskID 后才 ACK。
- worker 先权威加载 intent 并取得 claim；outcome 事务必须同时匹配 intentId、scopeId、generation、claimToken、fencingToken、effectiveDispatchRevision、capabilityUniverseRevision、ledgerRevision、claimedPositiveObservationVersion 和 active/running 状态，在同一事务更新 incident、终结 intent、释放预算并写 outbox。负向 outcome 遇到更新的 positiveObservationVersion 按 superseded 终结。禁止因行不存在 insert / upsert incident。handler 只有在该事务提交后才 ACK；enqueue 成功但 outbox ACK 失败只能产生同 TaskID 的幂等重放。

这不是两个事实源：Redis-only provisional 状态只能保守阻断，不能进入 API / 历史；PostgreSQL-only 未投影状态会触发 readiness gate，不能被当成健康。

### 13.4 冷启动、Redis 丢失和对账

1. 每次全局或账户 lazy rebuild 分配 `rebuildEpoch`，在 PostgreSQL 一致快照中捕获 `snapshotOutboxSeq`，按复合 cursor 扫描所有 SUSPECT / OPEN / HALF_OPEN / RECOVERING、active intent、当前 revision CLOSED ledger、due 和预算 reservation。
2. 扫描结果写入隔离的 shadow epoch；同一 projection owner 在重建期间把 snapshot 之后的 outbox 按连续序号重放到 shadow，遇到 gap 立即保持 not-ready，不得跳号。
3. 在同一 projection lease 下读取当前 stream head，只有 shadow 的 appliedOutboxSeq 连续追平 head、revision 未变、active intent / due / pending / running counter 已对账、expired claim 已回收，并以 intentId 幂等补投所有未终结任务后，才能 CAS 发布 `(accountRuntimeKey, revisions, rebuildEpoch, appliedOutboxSeq)` readiness。
4. 发布后新 outbox 仍由同一串行 owner 应用；全局重建完成前，仅允许已发布账户服务，其他账户返回 runtime_state_rebuilding 并尝试后备候选。
5. 页数、单页时限、总时限、容量和 cursor 前进都有硬限制；失败必须丢弃 shadow epoch、释放 rebuilding lease 并允许下一轮重试。
6. 运行期 reconciler 按 ledgerRevision 和连续 outbox 水位修复 incident、due、intent、预算和 revision tombstone。
7. 可信缓存负向状态继续阻断；没有权威快照时遵循现有基础设施门禁，禁止在候选循环逐账户回源 PostgreSQL。

### 13.5 保留与清理

- 活动负向 incident 和 active intent 不按容量 TTL 删除。
- capability namespace 的 Asynq retry / dead task 硬重放 TTL 固定为 7 天；超过 TTL 只能导出诊断元数据并不可逆取消，不能人工重新投递旧 envelope。
- CLOSED tombstone / generation floor 至少保留 31 天，且删除前必须证明相关 intent、outbox、retry/dead task 全部终态或已被不可逆 fenced cancel。无法证明时保留紧凑 generation floor，不能仅按墙钟删除。
- 正向 evidence 24 小时过期后展示为 unknown；行可在 7 天无活动且无 active intent 后用 stateVersion 条件删除。
- 配置删除先推进 revision，再异步清理旧 Catalog / incident。迟到 outcome 不得因行不存在而盲 upsert。

## 14. 调度与多 Key 全链路

对请求模型 M：

1. 为候选账户解析当前 Route 描述。
2. 批量读取当前 Catalog、Key 运行态和所有已观察 model_capability incident。
3. 对每个账户，只要存在至少一个可执行 Key 且其精确能力不是 OPEN / HALF_OPEN / RECOVERING，即保留账户。
4. SUSPECT 在 softAvoidUntil 内有健康候选时避让；全池只剩 soft avoid 时进入现有有界等待，等待边界仍是进程内而非伪称跨实例全局 FIFO。
5. 账户准备阶段从仍可执行的 Key 中选择一个，并形成不可变 Attempt 描述。
6. 真实失败在当前请求内按原规则切 Key / 账户 / 分组；生产探针旁路独立收集。
7. Key-1 + M 被阻断后，后续可选 Key-2 + M；同一账户其他模型按各自 scope 判断。

账户聚合：

- Route 可调度：至少一个当前可执行 Key 对该 Route 为 available / unknown，或 SUSPECT soft avoid 已过期。
- Route 不可调度：全部当前可执行 Key 都处于 OPEN / HALF_OPEN / RECOVERING。
- instance_all_capabilities_unavailable：当前同一 universe revision 的全部 Route 都不可调度。

最后一项只是 effectiveAvailability 的派生 blocker，不写 accounts.status，不启动全模型扫描，不交给旧哨兵冷却恢复器。

## 15. transport 父级规则修正

现有“10 分钟内三个不同 protocol_model scope 自动打开父 account circuit”必须删除。不同客户端别名、endpoint、lane 或同一个坏模型的多个子 scope 都不能证明整号 transport 死亡。

目标规则：

- protocol_model / model_capability 子 incident 永不以投票方式写父 account OPEN。
- all configured capabilities blocked 由 Catalog + 当前子状态派生门禁，不需要父 incident。
- 父 account transport circuit 只允许由真正账户全局、与模型无关且有专属独立证据的 owner 建立；目前没有满足该证明的自动子级升级路径，因此默认关闭。
- 父 account 恢复不能清理任何子模型、Key 或用户策略状态。
- transport scope 也必须使用 Attempt 描述中的真实上游模型和 endpoint，而不是客户端 requestModel 和固定哨兵 endpoint。

## 16. healthCheckModel、激活与周期检查

healthCheckModel + healthCheckEndpointMode 只表示哨兵 Route：

- pending_test：继续执行账户激活检查。complete_success 按现有规则进入 active；失败保持 pending_test 每小时复检，并保留从首次失败起 24 小时转 account_activation_check_timeout 的规则。
- active 周期检查：每小时观察哨兵精确 Route。成功 / 失败只更新该能力和哨兵监控，不再累计账户级失败后写 temporary_unavailable。
- 每个 `accountRuntimeKey / runtimeAccountId` 在自身运行账户行拥有独立哨兵成功水位，原子保存 `last_health_success_at + last_health_success_route_scope_id + last_health_success_dispatch_revision + last_health_success_universe_revision`；授权实例不得覆盖来源账户或其他实例的槽位。只有四项与当前解析出的哨兵 Route 和 revision 全部匹配，才允许跳过本轮主动检查；旧数据缺任一身份字段时按无有效水位处理。
- 只有同 normalized model + endpoint mode + lane + adapter route 的真实业务成功才能刷新本轮哨兵成功水位；Route 水位不绑定具体 Key，同 Route 的任一当前可执行 Key 成功均可刷新，但 Key 级 capability incident 仍按 Attempt scope 隔离。其他 Route 成功不能顺延哨兵。
- active capability intent 存在时，业务成功只写正向 observation 和哨兵水位，不终结 generation，不使之后到达的匹配独立探针 outcome stale。
- request_failure 不再调用哨兵探针。
- 纯图片哨兵的 catalog probe 只证明目录可见性；除非账户显式允许付费 E2E，否则不能声称图片 execution 可用或不可用。

切换必须受 `sentinel_watermark_contract_version` 门禁：账户候选查询、真实成功副作用、worker、列表 API 和统计 reader 同批改为完整四元组；仍只读取 `last_health_success_at` 的旧 reader 不得与新 writer 混跑。

## 17. 派生摘要与 effectiveAvailability

### 17.1 摘要字段

~~~text
CapabilityHealthSummary
  semanticsVersion = 2
  capabilityUniverseRevision
  aggregate
  routableRouteCount
  schedulableRouteCount
  availableRouteCount
  unknownRouteCount
  suspectRouteCount
  softBlockedRouteCount
  blockedRouteCount
  halfOpenRouteCount
  recoveringRouteCount
  lastObservedAt
  generatedAt
~~~

aggregate 规则按优先级固定：

1. 没有 Catalog 或 routeCount 为 0：unknown。
2. schedulableRouteCount 为 0，且存在 halfOpen / recovering：recovering。
3. schedulableRouteCount 为 0：all_configured_capabilities_blocked。
4. blocked / halfOpen / recovering 大于 0：partially_unavailable。
5. suspect 大于 0：confirming。
6. unknown 大于 0：no_confirmed_unavailability。
7. 全部 Route 都有新鲜正向事实：healthy。

unknown 允许调度，但绝不显示成 healthy。all_configured_capabilities_blocked 只有在当前 Catalog 和 Key 集合已完成权威加载后才能返回。

### 17.2 页面统一状态

后端现有 effectiveAvailability / availabilityPresentation 增加 capability blocker：

- 硬状态优先。
- active + partially_unavailable：可调度 · 部分能力不可用。
- active + confirming：能力确认中。
- active + recovering：能力恢复验证中。
- active + all_configured_capabilities_blocked：当前无可调度模型能力。
- quality_isolated 等硬状态保持原文案，能力摘要只作补充。

## 18. 管理 API

### 18.1 账户列表

账户列表单次完整响应对当前页批量水合 capabilityHealthSummary。不重新引入 status-snapshot、轮询或逐账户详情请求。

### 18.2 能力明细

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health
GET /__aisys__/api/my-accounts/:accountId/capability-health
~~~

参数：

- limit 默认 50，范围 1 到 100。
- cursor 为后端签名游标，绑定 accountRuntimeKey、effectiveDispatchRevision、capabilityUniverseRevision 和最后排序键；任一 revision 变化返回 409 stale_view，前端必须重新加载，不能跨 Catalog 代继续翻页。
- 可选 state、model 过滤。
- 稳定排序：normalizedUpstreamModel、upstreamEndpointMode、adapterRouteKey、routeScopeId。

响应包含 generatedAt、两个 revision、nextCursor、summary 和 Route 粒度 items。Route item 至少包含：

- routeScopeId、routeScopeRef（签名并绑定两个 revision）
- upstreamModel、endpointMode、requestLane、adapterRouteKey
- probeStrategy、state、schedulable
- 物理所有者 / 管理员可见 credentialTotal / available / unknown / blocked 计数；授权实例只返回 credentialTopologyHidden=true 和 Route 聚合状态，不泄漏 Key 池规模
- lastOutcome、lastObservedAt、positiveEvidenceExpiresAt、nextProbeAt
- canRecheck、canViewTrace

Route item 不返回任意挑选的单个 scopeId。展开某个 Route 时调用：

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health/:routeScopeRef/credentials
GET /__aisys__/api/my-accounts/:accountId/capability-health/:routeScopeRef/credentials
~~~

该接口只允许物理账户所有者或管理员调用；授权实例使用者统一返回 404。它按 scopeId 稳定排序并使用同样绑定 revision 的签名分页，返回每个 Attempt 的 scopeId、scopeRef、脱敏 credential label、state、schedule、canRecheck、canViewTrace，以及有权限时的 lastTraceRef。unknown Route 从 Catalog 与当前 Key 集合合成，不要求已有 incident。原始 fingerprint、来源账户名和其他授权实例信息不返回。

非法 cursor / limit 返回 400；资源不可见统一返回 404。

### 18.3 生产重新检查

~~~text
POST /__aisys__/api/accounts/:accountId/capability-health/:scopeRef/recheck
POST /__aisys__/api/my-accounts/:accountId/capability-health/:scopeRef/recheck
POST /__aisys__/api/accounts/:accountId/capability-health/recheck
POST /__aisys__/api/my-accounts/:accountId/capability-health/recheck
~~~

- scopeRef 必须由当前凭据明细返回，可以对应 Catalog 中尚无 incident 的合法精确凭据 scope；后端验签并核对 runtime identity、effectiveDispatchRevision 和 capabilityUniverseRevision，不匹配返回 409 stale_view。接口不接受任意 model / endpoint 文本。
- 单 scope accepted 返回 202、taskId、Location 和当前 generation。
- already_running 返回同一 taskId 和 202。
- 预算饱和返回 429 与 Retry-After。
- unsupported 或需要成本授权返回 409。
- 单 scope 与批量命令都要求 `Idempotency-Key`，同一 actor + account + key 在 10 分钟内返回同一命令资源。
- manual_costed_execution 只允许单 scope 命令，并要求 body `{ authorizeCostedExecution: true, maxEstimatedCostUsd }`；缺失授权、估算超限或日预算不足返回 409 / 429，均不创建 intent。批量命令不得隐式替用户授权成本。
- 批量命令 body 固定为 `{ expectedEffectiveDispatchRevision, expectedCapabilityUniverseRevision, scopeRefs?: string[] }`。省略 scopeRefs 时只选择当前 suspect / blocked / halfOpen / recovering；显式列表也最多 100 个。revision 不匹配整体返回 409；绝不把“全部重检”实现成扫描全部 unknown 模型。
- 批量 admission 逐 scope 返回 accepted、already_running、unsupported、cost_authorization_required、saturated 或 stale。至少一个 accepted / already_running 时返回 202；全部 saturated 返回 429；全部 unsupported / cost denied 返回 409。部分接纳不得回滚已 accepted intent。
- 命令只投递受控探针，不直接改成 available。

生产重检会影响调度，必须写操作日志；能力详情中的命令固定命名“重新检查此能力”。纯人工“测试”和 pending_test 的账户“重新检查”继续使用各自既有文案与接口，三者不可混用。

命令状态和 trace 使用独立资源：

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health/recheck-tasks/:taskId
GET /__aisys__/api/my-accounts/:accountId/capability-health/recheck-tasks/:taskId
GET /__aisys__/api/accounts/:accountId/capability-health/traces/:traceRef
GET /__aisys__/api/my-accounts/:accountId/capability-health/traces/:traceRef
~~~

- task 资源返回 pending / running / terminal、单 scope 或逐 scope 结果、acceptedCount、terminalCount、创建 / 完成时间和有界错误码；终态保留 24 小时，过期后 404。Location 必须指向该资源，前端按 Retry-After 轮询并在 terminal 停止。
- traceRef 是绑定 runtime identity、scopeId 和权限的短期签名引用；仅 canViewTrace=true 时返回。trace 接口只返回脱敏阶段、outcome、时间、revision 和关联审计 ID，不返回 Key、token、prompt、附件或上游正文。

### 18.4 权限

- 管理路径仅 super_admin / admin。
- my-* 强制使用当前 session，不接受前端 systemAccountId。
- 物理账户所有者可查看、重检并查看自己的 trace。
- 授权实例使用者只能查看自身运行实例的 Route 聚合，不能访问 credential endpoint、scopeRef、凭据计数或 trace，也不能消耗来源账户凭据发起生产重检。
- 不可见资源返回 404；权限摘要固定为 canView / canRecheck / canViewTrace。

## 19. AI 健康监控

旧 account_health_hourly 继续表示哨兵账户观察，语义版本为 1。新能力监控使用版本 2，不能把旧小时记录倒推成模型能力事实。

新增：

- account_capability_health_events：有界事件，保存通用 outcome、scopeId、phase before / after、trace、时间和 revision，不保存正文，默认保留 7 天。
- account_capability_health_hourly：按 systemAccountId + accountRuntimeKey + routeScopeId + effectiveDispatchRevision + capabilityUniverseRevision + statHour 聚合最后状态、各 outcome 次数和最后观察，默认保留 31 天；revision 变化立即封闭旧桶，禁止同小时混代。
- account_capability_health_hourly_summary：按账户 / 小时保存 Catalog 总数、available / unknown / suspect / blocked / recovering 计数、aggregate 和 semanticsVersion。

ingest owner 写事件，stats owner 增量聚合；管理 API 不扫描 usage 明细或运行日志临时 GROUP BY。

小时条同时展示：

- 原哨兵 success / failure / unknown。
- v2 能力摘要；部分阻断显示“部分能力不可用”，未知显示灰色。

展开按模型、endpoint 和转换路径分页读取 v2 明细。能力切换日前只显示 v1；切换日之后若 v2 聚合滞后，显示“能力数据待聚合”，不得回退伪造全绿。

## 20. 可观测性

日志事件至少包含：

- capability_candidate_collected
- capability_candidate_canceled_by_same_scope_success
- capability_probe_admission_accepted / running / cooling / saturated / unsupported
- capability_probe_started / completed / task_failed / stale
- capability_state_transition
- capability_projection_gap / rebuilt / reconciled
- capability_scope_filtered

指标至少包含：

- admission accepted、deduped、cooling、saturated
- 每 scope 5 分钟重复抑制数
- 每逻辑实例、物理账户和全局 pending / running
- probe queue wait、执行时长和四类稳定 outcome
- phase 数量、OPEN 年龄、恢复成功 / 失败
- outbox lag、Asynq retry / dead、projection gap、rebuild / lazy-load
- 全部能力派生阻断账户数

模型 ID 和 endpoint 可以记录；凭据只记录不可逆内部 scopeId。禁止记录密钥、token、用户 prompt、附件和上游正文。

## 21. 配置与生命周期

- 名称、备注、优先级变化：不推进能力 revision。
- supportedModels、endpoint modes、probe policy、Catalog 可达性变化：推进 capabilityUniverseRevision。
- API Key 替换、OAuth 授权主体 / 权限集合 / lineage 变化、Base URL、代理、协议档案、映射目标或转换实现变化：推进 effectiveDispatchRevision 和 capabilityUniverseRevision。普通 OAuth access token 刷新及同 lineage 的 token 轮换不推进。
- adapterImplementationRevision 是部署清单的一部分；转换代码语义变化必须推进对应 Route 的 effectiveDispatchRevision，不能复用旧 recipe / incident。
- 来源账户 revision 通过现有 family outbox 传播到所有授权实例；worker 保存 runtime account ID、binding system account、bound group、authorization ID 和来源 revision，不解析 opaque runtime key 猜权限。
- 人工停用：保留 ledger，但不执行恢复探针。
- 删除账户 / 归还授权：推进 revision、取消 intent、投影 tombstone，再异步清理。
- Key 删除：对应 credentialScopeKey facts 因 key-set revision 失效。
- 配置清理只处理旧 revision；不得批量把剩余能力改 available。

## 22. 部署、切换与回滚

### 22.1 完整切换

1. 备份 PostgreSQL，并导出 owner manifest、schema version、现有 circuit / health 设置和待处理任务清单。
2. 把 W10 Go gateway 的 production listener、完整候选循环、协议 finalizer、usage / audit handoff、circuit side-effect、drain / canary / rollback 和 Node owner 删除证据作为前置门禁；当前仅有 seam、未接管生产 listener 的状态不能实施本方案。macOS 与高性能部署手册必须在同批次给出 Go gateway / control / Asynq / stats supervisor 配置。
3. 用 Goose 一次性创建 / 扩展 ledger、intent、Catalog、event 和 hourly schema；运行 schema contract preflight。
4. 为所有账户计算 capabilityUniverseRevision 和 Catalog，执行 Node / Go 两套 scope codec golden vectors。
5. 部署唯一 Go owner：gateway 生成描述和收集候选，control worker 管 admission / ledger / outbox，Asynq worker 执行探针，stats owner 聚合监控。
6. capability_contract_version 与 sentinel_watermark_contract_version 门禁确认所有 gateway / worker / reader 已禁用旧 accountId request-failure dispatcher 和单字段哨兵水位；发现新旧 owner 混跑时全部拒绝 ready。
7. 迁移旧 circuit：按 provenance / childIncidentIds / legacy escalation metadata 找出由子 scope 投票生成的父 account incident，保留子 incident 并用 generation / ledgerRevision CAS 关闭父层、写 outbox tombstone。用户显式策略或有专属全局 owner 的父状态必须保留；来源不明的父 incident 导出逐账户人工 manifest，在裁决前对应账户 fail-closed，不得静默继承为新全局事实。
8. 从 PostgreSQL 按 rebuildEpoch + snapshotOutboxSeq 重建 redis-state，完成连续 outbox 追平、按账户 readiness、任务补投和 capacity 校验后再开放流量。
9. 清空仅属于旧 request_failure 探针的待处理队列；不得清空其他账户测试、冷却、Key 或 transport 任务。
10. 验证 5 分钟单飞、投影 lag、probe amplification、A/B 隔离、多 Key 和 UI 状态后开放生产重检。

历史 temporary_unavailable 没有可靠 provenance。last_error_code 为空或来源不明的行一律不自动清理，继续由原恢复器自然处理。确需清理时，只接受人工确认的账户 ID allowlist 和逐 ID CAS manifest；禁止宣称可按旧来源码精确筛选。

### 22.2 回滚契约

- 开始切换后禁止只回滚二进制；旧 dispatcher 会重新产生整号探针。
- 回滚顺序固定为：冻结 admission / 生产重检和新任务投递；停止新 gateway side-effect owner 与 Asynq consumer；以 fencing token drain 或 cancel PostgreSQL 全部非终态 intent；在同一事务链终结 intent、写 outbox 并补偿 reservation / pending / running 计数；等待 projector 连续追平并验证 active intent 与所有预算占用为零；导出 retry / dead task 清单；此后才允许清理 capability Asynq namespace、Redis epoch 和 tombstone。
- 清理运行态后，用 Goose 把 schema 恢复到旧二进制声明的精确版本，再恢复旧 owner manifest、二进制和 contract version。若需保留新表取证，必须复制到不参与 Goose 版本判断的独立 archive schema；只有旧二进制 catalog 本身已包含该 migration 时才能原地保留，不能把“Goose Down 或保留只读表”作为无条件二选一。
- 本方案不修改 accounts.status，因此不需要整库回档，也不会覆盖切换后其他业务写入。
- 若执行过人工 legacy allowlist 清理，必须用保存的 before-image 和 configRevision 逐 ID CAS 恢复，不能整库还原。
- 回滚触发阈值至少覆盖 projection lag 超标、重复探针率、状态不一致、调度错误率和 dead task 激增。
- 上线前必须演练 reservation-before-ledger、ledger-before-project、claim 后 worker 崩溃、outcome 后 ACK 失败、Redis flush 和完整回滚。

## 23. 测试矩阵

### 23.1 核心隔离

1. A 成功、B 失败；B 独立探针失败后只过滤 B，A 继续调度。
2. B 的两个客户端别名映射到同一上游模型和转换路径，只形成一个 scope。
3. 相同上游模型的 direct chat 与 responses-to-chat 使用不同 adapterRouteKey，互不误伤。
4. chat_json 失败、chat_sse 成功时只阻断精确 endpoint。
5. 模型大小写由 provider canonicalizer 决定，不被全局 lowercase 合并。
6. 401、403、429、500、502、503、HTML 和畸形 JSON 的独立探针全部形成同一通用 unavailable。
7. catalog visible / missing 都不改变 execution 状态。
8. 未允许付费探针的图片失败只能短时 suspect / unknown；允许后真实 E2E 才可阻断和恢复。

### 23.2 请求生命周期

9. B 失败后 A 成功救回，最外层 finally 仍 flush B。
10. 同一 Key + 完整描述先失败后成功，失败候选被取消。
11. Key-1 失败、Key-2 成功不取消 Key-1 的精确候选。
12. 下游提交后的读取中断、协议失败终态、畸形完成和失败事件不能切号，但仍可收集精确能力候选。
13. 客户端取消、本地配额失败、非法请求和未派发不产生候选。
14. 一个请求跨 64 个 attempt 最多 accepted 一个新 intent。

### 23.3 多 Key 与授权

15. Key-1 无 B 能力、Key-2 可用：只阻断 Key-1 + B，账户 B 仍可调度。
16. 所有 Key 的 B 都阻断时只派生 B Route 不可调度，A 不受影响。
17. 多个授权实例共享物理凭据的 100 路失败，物理同时只执行一个探针。
18. 授权实例 revision 变化后旧结果 stale，且不泄漏来源 Key / trace。

### 23.4 状态机与竞态

19. 对每个状态覆盖 complete_success、unavailable、task_failure、stale 和 lease expiry。
20. 初始 probe task 重试耗尽回到此前稳定展示，不伪造失败。
21. half_open task failure 回 OPEN 且不增加能力失败次数。
22. recovering task failure 保持 recovering。
23. 普通成功不直接恢复 OPEN；claim 后出现更新 positiveObservationVersion 时，迟到负向 outcome 以 superseded 终结并触发新验证，不能覆盖较新成功。
24. 两次恢复成功、间隔和 5/10/20/30/60 退避准确。
25. 配置、Key 集合、映射和授权在探针途中变化，旧结果全部 fenced。

### 23.5 并发、存储与灾难恢复

26. 1000 个同 scope 并发失败在 5 分钟内只有一个 durable intent 和一个真实 probe。
27. 两个 gateway 不能各分配一个 generation。
28. 第 17 个物理账户 pending、全局 4097 个 pending 和队列替换均无悬挂 SUSPECT。
29. reservation 后进程崩溃、ledger 提交后 projector 崩溃、claim 后 worker 崩溃、结果提交后 ACK 失败均可恢复。
30. Redis flush / 重启时 OPEN 不被 miss 当 unknown；未 ready 账户局部阻断，已加载账户继续服务。
31. capacity sentinel、分页重建、cursor 不前进、总时限和 reconcile gap 有真实回归。
32. CLOSED 清理与迟到 outcome 并发时，旧结果不能盲 upsert。
33. Node / Go canonical scope golden vectors 完全一致。

### 23.6 transport 与账户边界

34. 同一 B 的三个 transport 子 scope 不再打开父账户电路。
35. 多个不同模型 transport 子 incident 也不通过投票写父 account OPEN。
36. all configured capabilities blocked 只形成派生门禁，不改变 accounts.status。
37. 单模型账户能力阻断后页面显示“当前无可调度模型能力”，恢复后自动解除。
38. quality_isolated、显式 temporary_unavailable 和 Key 状态不会被能力成功清理。
39. pending_test 保留每小时复检和 24 小时 activation timeout；active 哨兵失败只影响精确能力。
40. 其他 Route、旧 dispatch revision、旧 universe revision 或缺少身份字段的成功不能顺延哨兵下一次检查；同 Route 的另一当前 Key 成功可以刷新 Route 水位，但不能清理原 Key 的 capability incident。

### 23.7 API、前端和监控

41. 列表单请求批量返回摘要，无 N+1、无 status-snapshot。
42. Catalog 合成 unknown，摘要不会把 A 成功、B/C 未观察显示为 healthy。
43. 六种 scope 状态、全部 aggregate 和硬状态优先级均有前端真实数据回归。
44. 管理 / my-* 的 403 / 404、授权实例脱敏、canRecheck 和 canViewTrace 正确。
45. recheck 的 202 / 409 / 429、Location、Retry-After、幂等 taskId 和最多 100 批量上限正确。
46. v1 哨兵小时记录和 v2 能力小时记录不互相改写；切换日前后、聚合滞后和明细过期显示正确。

### 23.8 部署与回滚

47. capability_contract_version 能阻止新旧 dispatcher 混跑。
48. 来源不明 legacy temporary_unavailable 不被自动清理。
49. Goose Up / Down、Catalog 重建、Redis namespace 清理和 owner manifest 可重复执行。
50. 完整回滚演练不丢失切换后的其他业务写入。

### 23.9 集成与所有权补充

51. capability probe、周期哨兵、激活、冷却、人工测试和生产重检失败都不会再次进入 Collector 或递归创建 intent。
52. Key-1 到 Key-2 轮换为两个不可变 Attempt 描述，各自重新门禁和取 lease；Key-1 的 scope / lease 不被复用。
53. 目标 Key 删除 / revision 变化时 intent stale；Key owner 阻断时 capability probe 不旁路，恢复事件只唤醒独立验证。
54. Collector flush 的超时、IPC、Redis 和 PostgreSQL 异常均不改变 HTTP / SSE 结果、audit 终态或连接收尾；未 durable commit 不返回 accepted。
55. plannerContextKey、sourceRequestShapeKey 和 adapterImplementationRevision 变化会使旧 recipe fenced，且 payload 不含用户输入。
56. 两个授权实例使用不同 runtime identity 但 PhysicalProbeExecutionKey 完全相同时只执行一次；任一物理字段不同都不合并。
57. worker 只用显式 binding system account / group / authorization 上下文鉴权，opaque runtime key 无法被解析成权限。
58. 普通 OAuth token 刷新不改变 scope / revision；API Key 替换和授权 lineage 变化必然失效旧 intent。
59. 同一次执行没有 transport lease 时只写 transport observation；同时持有两种 lease 时，完整失败对 capability 为 unavailable、对 transport 为 framing complete。
60. 周期哨兵 / 人工生产重检在稳定 phase 建 verification intent，不进入 SUSPECT 或 soft avoid；结果仍经过 durable admission、claim 和 fencing。
61. 重建扫描期间并发创建 OPEN / intent 时，shadow epoch 必须连续追平 snapshot 后 outbox 才 ready；gap、counter 不平或任务漏投均保持阻断。
62. enqueue 成功但 outbox ACK 失败、outcome commit 后 Asynq ACK 失败都只按 intentId 幂等重放；错误 token / revision / ledgerRevision 无法 upsert。
63. 7 天后的 dead task 不能人工复活；tombstone 删除前证明 intent / outbox / task 全终态，否则 generation floor 保留。
64. Route 列表与 Attempt 凭据分页粒度不混合，所有 cursor / scopeRef 绑定两个 revision，stale 页面返回 409。
65. 单 scope / 批量 recheck 的幂等、部分接纳、task Location / terminal / 24 小时过期及 trace 权限完整回归。
66. 授权实例与来源账户的哨兵四元组互不覆盖，旧单字段 reader 被 contract version 拒绝 ready。
67. legacy child-escalation 父 incident 被 CAS 关闭且子 incident 保留；显式 / 真全局父状态保留，来源不明账户进入 manifest 门禁。
68. 回滚必须先 fenced 终结 intent 并把预算归零，再清任务 / Redis 和 Goose Down；旧二进制只在精确兼容 schema 上启动。

## 24. 实现落点

- Go gateway：最终 route / attempt descriptor、批量门禁、Key 过滤、request tracker collector。
- Go control worker：model_capability scope policy、admission、ledger、intent、outbox、reconcile。
- Go Asynq worker：精确 ProbeRecipe 执行、租约续期、恢复 due。
- PostgreSQL：扩展 circuit ledger、intent、Catalog、event 和 hourly projection。
- redis-state：原子 phase / lease / generation / budget / due / readiness；不保存长期唯一事实。
- frontend：账户列表统一 presentation、能力明细、生产重检、v2 健康监控。

当前 Node 实现可作为行为证据和迁移参照，但完整完成定义不新增 Node + SQLite 双套长期 adapter。切换后必须删除或禁用 Node 的 accountId request_failure 健康派发和 protocol_model 子投票父级升级。

## 25. 完成定义

以下条件全部满足才算完成：

- A/B、多 Key、映射、endpoint、流式提交后失败均能精确归因和隔离。
- 模型能力层没有任何 accounts.status 写入口。
- transport 子 incident 不再通过计数打开父账户。
- 5 分钟冷却、跨实例单飞、物理预算、可靠队列、fencing、重建和回滚都有自动化证据。
- unknown、部分阻断、全部阻断和恢复中在列表、详情与健康监控一致展示。
- 旧 request_failure 哨兵路径已互斥下线。
- 权威文档、接口、权限矩阵、迁移和运维手册已同步。

在这些条件完成前，不能只上线探针、只上线状态表或只上线前端标签。

## 26. 相关文档

- [架构总览](../architecture/架构总览.md)
- [AI 账户错误语义与状态变更边界](AI账户错误语义与状态变更边界.md)
- [AI 账户短窗口热质量与精准切号设计](AI账户短窗口热质量与精准切号设计.md)
- [AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)
- [网关失败归因与自动探针结果设计](网关失败归因与自动探针结果设计.md)
- [账户内 API Key 故障隔离设计](账户内APIKey故障隔离设计.md)
- [AI 健康监控设计](AI健康监控设计.md)
- [接口契约与权限矩阵](接口契约与权限矩阵.md)
- [存储目标与 SQLite 移除](../migration/存储目标与SQLite移除.md)
