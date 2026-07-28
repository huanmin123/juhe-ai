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
- 默认禁止后台付费生图、音视频等高成本 E2E。没有受控真实执行配方时，只能保持 unknown，并可建立不进入健康 phase 的短时 `cost_verification_notice`；不能承诺自动硬隔离。
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

模型能力成功不得恢复或覆盖上述任何既有硬状态；但 pending_test 激活按第 16 节改为 Catalog 多 Route selection，单模型 opaque 失败不再拥有“24 小时后转整号 error”的权限。用户显式策略 TTL、quality_isolated 和有完整 provenance 的既有硬状态仍由各自 owner 管理。

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
10. 任意结果写回同时校验 routeDefinitionRevision、attemptDefinitionRevision、当前 binding 权限、generation、ledgerRevision、claimToken、fencingToken 和有效租约；effectiveDispatchRevision、capabilityUniverseRevision 只作为 publication provenance，不能把无关配置变化当成 scope 失效条件。
11. Redis miss 在账户 runtime projection 未 ready 时不得解释为 unknown。
12. 人工测试零生产副作用；生产“重新检查”是另一个有权限、审计和预算的命令。
13. Collector 只接收普通用户 gateway traffic；激活、周期哨兵、capability probe、transport recovery、冷却复测、人工测试和生产重检都直接提交各自 outcome，绝不再次进入 Collector。
14. Collector 的 control RPC 失败或超时不得覆盖或改变原用户响应；positive gate 判定 durable-required 的成功与全部失败候选在释放 request tracker 前必须先进入 gateway 本地 durable handoff spool，允许为一次有上限的本地 durable append 增加极短收尾尾延迟，不能依赖下一次请求补触发。只有 durable ledger 已提交才能返回 admission accepted。
15. 健康状态机只承认已经在 PostgreSQL scope 串行化事务中提交的正向 observation；Redis provisional gate、进程内回调或尚未确认提交的 usage / audit handoff 都不能单独构成“较新成功”。
16. 每个会影响重建、预算、任务恢复或结果写回的持久 mutation 都必须在同一事务追加连续 control outbox；不能依靠发布前再扫一次表猜测快照之后发生过什么。

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
  Finalizer --> Handoff["Gateway durable capability handoff"]
  Collector --> Handoff
  Handoff --> Admission["能力观察 / 意图原子 admission"]
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
  routeMembershipIncarnation
  routeDefinitionRevision

ResolvedGatewayAttemptCapability extends ResolvedGatewayRouteCapability
  credentialScopeKey
  credentialMembershipIncarnation
  attemptDefinitionRevision
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

账户级版本字段与两个 definition revision 都不进入 scope hash；前者用于冻结配置 publication 和 API 快照，后者作为规划器附加的运行态 fencing 元数据：

- effectiveDispatchRevision：凭据、Base URL、代理、协议档案、转换目标等真实派发身份。
- capabilityUniverseRevision：支持模型、endpoint modes、映射可达性、probe strategy、Key 集合和授权实例能力目录。

它们不能作为所有能力事实的粗粒度失效开关。每个 Catalog Route 另保存不可复用的 `routeMembershipIncarnation`，每个当前凭据成员保存不可复用的 `credentialMembershipIncarnation`；只要成员在连续 ready publication 中保持 unchanged 就沿用，removed 后即使原样 re-add 也必须从持久 sequence 分配新 incarnation。`routeDefinitionRevision = hash(Route 描述 + probe recipe contract + routeMembershipIncarnation)`；`attemptDefinitionRevision = hash(routeDefinitionRevision + credentialScopeKey + Base URL / proxy / egress / protocol execution identity + credentialMembershipIncarnation)`。intent、claim、positive observation、outcome、handoff hold 和 due 以 `scopeId + routeDefinitionRevision + attemptDefinitionRevision` 做状态 fencing；账户级双 revision 只证明它们来自哪一个完整目录 publication。Key-1 替换、增加模型 C 或删除无关 Route 时，未变化的 B + Key-2 定义 revision 保持不变，其 OPEN / RECOVERING / positive version 必须原子 carry-forward 到新 publication，不能回落 unknown 或重新放行。定义 digest 变化、scope 被删除或 credential lineage 改变时才使该 scope 的旧代 stale；删除后原样重建也因 incarnation 改变而拒绝删除前的迟到 handoff / outcome，防止 ABA。

配置事务先在不可见 staging 生成新 Catalog / credential baseline，并按 scopeId + definition revision 与上一 ready publication 做确定性 diff：unchanged 沿用 membership incarnation 并复用同一 ledger current pointer；added 从持久 sequence 分配新 incarnation 后初始化 unknown；changed 使用新 definition revision 并保留旧事实作历史；removed 写携带旧 incarnation 的 revision tombstone。全部 carry-forward、目录 pointer、账户级双 revision 和 outbox 在同一事务发布；无法证明 diff 完整时新 publication 不 ready。名称、备注、优先级等无关编辑不得推进这些 revision。映射目标变化同时推进账户双 revision 及受影响 scope definition revision；只改变可选支持目录时只推进 capabilityUniverseRevision 和真实新增 / 删除 Route，不能让未变 scope 失效。

## 8. CapabilityScopeCatalog

### 8.1 配置侧目录

unknown 不能依赖事实表空行猜测。配置 owner 必须用与网关相同的规划器生成 CapabilityScopeCatalog：

- 每行是一个不含 credentialScopeKey 的可路由 Route 描述。
- 只枚举模型目录、账户 supportedModels、endpoint modes 和映射共同允许的真实路由，不做盲目模型乘 endpoint 全笛卡尔积。
- 记录 probeStrategy、最小探针配方版本、routeMembershipIncarnation、routeDefinitionRevision、来源 capabilityUniverseRevision 和 publication identity；credential baseline 同步记录 credentialMembershipIncarnation / attemptDefinitionRevision。
- Key 集合保持在现有 Key 目录；查询时与稀疏能力 incident 合并，不物化全部 Route × Key 行。

账户列表摘要、能力明细、all configured capabilities blocked 派生结论都必须以同一 revision 的 Catalog 为全集。任一 unknown、stale 或尚未加载的 Key 都不能被算成“已确认不可用”。

### 8.2 容量

- 合法配置默认最多 8192 个 Route 描述，部署下限不得低于现有 500 模型乘全部适用 endpoint mode 的产品上限。
- 每个物理账户最多 50 个 credential slot，与现有账户凭据保存上限一致；授权实例不能复制槽位绕过该上限。配置事务在生成 Catalog / revision 前拒绝超限。
- 每个运行账户在统计时区的同一个 `statHour` 最多提交 128 次会生成非零 capability publication 段的配置事务；第 129 次在写配置前返回 `429 capability_revision_rate_limited`，Retry-After 指向下一 `statHour` 边界。该 admission 与 v2 统计的 128 段上限使用同一时区与半开小时身份。这里的 `capabilityUniverseRevision` / publication revision 是能力目录发布版本，不是管理接口的 `configRevision`；名称、备注等编辑仍推进管理 `configRevision`，但不生成能力 publication 段。
- 超出限制在账户保存时拒绝并返回明确配置错误，不能运行时静默截断。
- capability incident 持久层不设 512 行硬上限；活动负向状态不得为腾容量被删除。
- 512 只可作为单进程热缓存默认容量。CLOSED / unknown 可淘汰；负向 miss 必须按账户有界 lazy-load，容量耗尽返回现有 capacity sentinel。
- 单个 stats publication build 默认最多写 100000 个 versioned delta 行。估算超限时不得发布半成品或丢事件；该账户保持上一 publication 并标记 historyStale / rebuilding，使用同一 buildId 分块写不可见 staging，全部完成后一次 CAS 发布。Route、slot 或 revision 输入已经超过前述硬边界时必须在源配置事务拒绝，不能把无界工作推给 stats owner。

## 9. 请求证据与失败收集

### 9.1 普通业务成功

只有 finalizer 形成 protocolValidatedSuccess 才是正向能力观察：

- 没有 active intent 且当前没有确认负向状态时，可以建立或刷新 available。
- 热门 scope 的正向持久写按 scope 节流，默认最多 15 分钟一次；每请求成功不得同步热写 PostgreSQL。
- active intent 存在时，普通成功只原子递增该 scope 的 positiveObservationVersion 并写有界 observation，不由业务路径直接终结 generation。
- OPEN / HALF_OPEN / RECOVERING 只能由匹配 generation 的独立探针推进恢复。
- OPEN / HALF_OPEN / RECOVERING 期间出现同 scope 业务成功时不直接恢复。免费 execution 可把恢复 due 提前到 `max(database_now, firstRecoverySuccessAt + 30s)`；manual_costed_execution 只记录“出现新鲜业务成功”，不得创建、提前或消费付费 due，仍需所有者新的成本授权命令确认。

15 分钟节流只适用于没有 active intent / provisional admission 且没有确认负向 phase 的普通正向刷新。每次 protocolValidatedSuccess 都调用 Redis Lua `CapabilityPositiveObservationGate(scopeId, routeDefinitionRevision, attemptDefinitionRevision, trace / attempt identity)`，但不是每次都写 spool / PostgreSQL：

1. admission 先在 PostgreSQL scope 串行化事务创建唯一 `gate_reservation(kind=admission, generation, gateEpoch, token, expected definition revisions)` 并写 outbox，但尚不创建 intent / SUSPECT；projector 随后在同一 Redis scope 域发布 provisional marker，最后第二个 PostgreSQL 事务校验 marker ACK 后才原子创建 intent、预算、SUSPECT / soft avoid、handoff accepted 和 outbox。reservation 与 marker / ledger 未对齐期间该精确 scope evidence unconfirmed；reconciler 只能按 token 完成或补偿，不能靠 TTL 猜删。
2. physical claim 是**多 scope 原子组协议**，不能把合并执行误写成单 scope CAS。claim owner 先锁 physical execution 行并按 scopeId 稳定排序确定全部 candidate member，为每个 scope 持久创建同一 `claimGroupId` 下的新 `claimGateEpoch` reservation；再逐 scope 发布 Redis claim marker。只有全部 marker ACK 后，单个 PostgreSQL 事务才按同一稳定顺序锁定 execution、全部 scope / intent / observation，重新校验 definition revisions 与 binding 权限，冻结各自**最新** positiveObservationVersion / gate epoch，并把准确全集置为 frozen / dispatchable。任一 marker 部分失败、scope 变 stale 或事务结果未知都不得发送上游；reconciler 只能按 claimGroupId 补齐全部 marker 后重试同一 ledger，或整体补偿并为已发布 marker 写终态。claim marker 发布后完成的 success 使用对应 scope 的新 gate epoch；marker / group ledger 未对齐时必须 durable fallback，不能 coalesced。这样每个 scope claim 前的 S1 都进入自己的冻结值，claim 后的 S2 一定获得新的 fence key。
3. gate key 固定为 `positiveFenceKey = hash(scopeId, routeDefinitionRevision, attemptDefinitionRevision, gateEpoch, winnerKind)`。active / provisional generation、claim、确认负向 ledger 和 hold resolution 各自推进 gateEpoch；普通稳定 scope 只在 `normal_refresh_due_at` 到期时推进 refresh epoch。winner 生成事件唯一 observationId，同时携带 positiveFenceKey、generation / ledger revision 和 gate epoch。
4. Redis gate 自身使用 `open -> reserved -> durable_spooled -> terminal`。reserved 保存 lease / token / observationId，但只有 gateway 确认 spool record 已被 durable watermark 覆盖后才能 CAS 为 durable_spooled；只有 durable_spooled 或 PostgreSQL terminal 的同 fence success 才可返回 `coalesced`。进程在 reserve 后、spool durable 前崩溃时，后续 success 必须 lease-takeover 同一 positiveFenceKey 或走 durable fallback，不能被孤立 reserved winner 吞掉。旧 record 若随后出现，PostgreSQL 的 positiveFenceKey 条件唯一约束只允许一个 committed winner，其余以 duplicate_coalesced 终结且不递增 version。
5. Redis miss、epoch / definition revision 不匹配、active marker 对账未知、reserved lease 未确认或 gate 调用失败不能返回 coalesced，必须降级为 `durable_required_unconfirmed` 并写第 9.2 节 handoff。Redis 故障时可能有多个 fallback envelope，但 PostgreSQL 先按 positiveFenceKey 收敛，只有唯一 winner 写 observation / version；这避免按请求递增，同时保留 post-claim 正确性。

active / provisional intent、claim、hold resolution 或确认负向 phase 上的 winner 属于正确性 fence，必须提交 `CapabilityPositiveObservationEnvelope`。对健康状态机而言，只有 control owner 在 PostgreSQL 中取得该 scope 的串行化锁、先锁定 positiveFenceKey，并在同一事务插入 observation、递增 `positiveObservationVersion`、追加连续 control outbox / handoff receipt 后，才算正向 observation 已接纳；同 positiveFenceKey 的其他 observationId 返回 duplicate_coalesced，不递增 version。事务返回未知时按 observationId / positiveFenceKey 重试到确定结果，不能先在内存里宣称成功。该低频正确性写不受 15 分钟节流。

Redis positive gate 是决定“本次成功是否必须 durable handoff”的线性化运行态门禁，同时仍不是健康事实源，也不能替代 durable observation。winner gate 携带 observationId、scope、definition revisions、winner kind、generation / ledger revision 和 resolution epoch；事务确定 committed / stale 后由 outbox / reconciler 终结。gateway 必须先把 winner 或 unconfirmed fallback 的同一 observationId 非敏感 envelope 写入第 9.2 节 durable handoff spool，再尝试短时 control RPC；RPC 超时、进程崩溃或 Redis flush 只延迟接纳，重放 worker 仍会以同一 ID 重试到 `committed / stale` 的持久终态。只有 PostgreSQL 已提交的 observation 才参与正向 fence，spool 行和 gate 都不得提前把 scope 标为 available；因此负向 outcome 先提交时，迟到成功只提前新的恢复验证，不按客户端完成墙钟倒置状态。

quarantine / health hold 不能被旧 winner 永久封死。创建或重新激活任一精确 hold 的 PostgreSQL 事务必须在同一 scope 串行化域单调递增 `positive_gate_resolution_epoch`、保留旧 winner tombstone 并写 control outbox；Redis projector 应用新 epoch 后，下一次匹配 definition revision 的 protocolValidatedSuccess 必须成为新的 durable-required winner。若 projector 未追平或 Redis 丢失，gateway 走 unconfirmed fallback 而不是 coalesced。新 observation committed 后不会恢复 OPEN，但必须在同一 scope 串行化事务中释放**所有**满足“同一 scope、同一 definition revisions、hold 激活 control sequence / resolution epoch 不晚于本 observation”的 active hold；更晚创建、不同 definition 或账户 / 全局 hold 保留。逐 hold 写 release proof 与 outbox，不能只解除碰巧关联到本次 handoff 的一行。这样首个 success handoff 自身被 quarantine / 过期时，后续真实成功仍可形成新鲜证据，也不会留下永远无法清除的旧 hold。

positive observation 事务、physical claim 和负向 outcome reservation 使用同一个 scope 串行化域。claim 必须在事务中确认没有未裁决 observation，并固化当前 version；负向 outcome 还要求 version 未变化。负向事务先提交时，较晚 durable 成功只提前恢复 due；正向事务先提交时，负向结果必须 `superseded_by_newer_success`。所有顺序以 PostgreSQL 串行化与 version 为准，不比较跨机器墙钟。provisional gate 的提交结果若长期未知，reconciler 重放确定性 observationId；在冻结 gateway ingress、drain finalizer、等待数据库最大事务时限并确认 observation 行不存在后，才可写审计 tombstone 并清 gate，不能靠普通 TTL 猜测。

available 正向事实默认 24 小时后只在展示层回落 unknown；调度上两者都允许。

### 9.2 Gateway durable capability handoff

普通用户响应不能被远端健康控制面拖慢，但控制面短时不可用也不能把 positive gate winner、unconfirmed success fallback 或已识别失败永久丢掉。每个 gateway 实例使用 release 外共享数据目录中的独立 append-only segment 与 ACK cursor，形成 `capability-handoff-spool`。它只保存规范化 Route / Attempt 身份、definition / publication revisions、recipe ID / digest、trace / attempt ordinal、producer instance epoch / sequence、replay deadline 和确定性 handoff ID；禁止保存凭据、用户 payload、上游正文或完整错误响应。gateway 只生产 segment；每个 gateway host 的独立 `capability-handoff-replay` 服务同时持本机 OS 文件锁和 PostgreSQL 单调 fencing lease 后才可重放、quarantine 或推进 ACK，ingress 进程退出后仍存活到 drain 完成。旧 lease owner 的 token 不能更新 delivery、watermark、quarantine 或本地 ACK cursor。

- positive gate 返回 winner / durable-required fallback 或 Collector 形成失败候选时，finalizer / Collector 才在释放 request tracker 与 audit capture 前同步完成一次有界本地 append；普通 `coalesced` success 不写 spool。记录使用定长 header + length + checksum + sequence，只有 durable watermark 经 `fdatasync` / 等价 OS 原语覆盖该 sequence 才算本机 append 完成；当 manifest 声明 `replicationMode=sync_rpo0` 时，还必须取得覆盖同一 sequence 和 content digest 的第二故障域 replica receipt，`replicaAckThrough >= sequence` 后才算 durable append 成功。允许专用单 writer 做默认不超过 10ms 的 group commit，但不得只把数据留在语言 runtime / OS page cache 后宣称 durable。该等待只增加收尾尾延迟，不改变已经形成的 HTTP 状态、body / SSE 事件或 audit 终态。正常路径在 append 成功后发送短时 control RPC；本机 sync 或 RPO=0 replica ACK 失败时仍执行一次有界 emergency control RPC 尝试取得 PostgreSQL durable commit，但无论是否成功都立刻把 gateway 摘流，因为声明的持久依赖已经失效。
- `positive_observation` 使用 observationId 作为业务幂等身份；`failure_nomination` 使用 `hash(traceId, orderedCandidateDigest, terminalId)`，并保存同一请求跨账户 / 后备分组候选的固定顺序。每个 candidate 自带自己的 runtime / binding 身份与双 revision，不能把父 handoff 误归为单一账户。相同 handoff ID 的 payload digest 不一致时 control owner fail-closed 报警。
- gateway 可以在 append 后立即用短时 RPC 发送；无论即时发送还是后台重放，只有 control owner 已持久写入回执并返回确定结果，producer 才推进 ACK cursor。崩溃发生在 control commit 与本地 ACK 之间时只会重放同一 ID。
- handoff replication policy 是 active epoch manifest 的不可变部分，至少包含 `replicationMode=single_host|async_declared_rpo|sync_rpo0`、`replicaSetDigest`、源 / 副本 failure-domain identity、可空 `maxDeclaredRpoSeconds` 和 receipt contract version。sync receipt 以 deployment epoch + producer epoch + sequence 唯一并保存 content digest、replica set digest、源 / 副本 failure-domain identity、单调 replica fencing token、`replicaAckThrough` 和 ack 时间；同 sequence 异 digest 是必须隔离的冲突，不能靠把 digest 放进唯一键创建第二条“合法”receipt。副本 ACK 未覆盖本次 sequence、digest 不同、两个 identity 属于同一故障域或 token stale 都不能满足 RPO=0。异步复制只能按实际确认水位暴露 RPO，不得把“已发送”当“已确认”。
- failure nomination 是一个不可变有序候选集。首次扫描按 ordinal 处理：`already_running / cooling_down / unsupported / stale` 写永久 disposition 后推进 scan cursor；容量饱和、租约竞争和可恢复基础设施错误把该 ordinal 写入 durable retry set，并可继续检查后续候选；首个 `accepted` 才以 linked intent 终结整组。扫描到末尾仍有 retryable ordinal 时父 receipt 保持 pending，scheduler 按 `(retry_after, ordinal)` 重新领取，而不是从末尾 cursor 永久跳过；每轮重新执行 revision / active intent / cooling / capacity admission，直到 accepted 或转成永久 disposition。
- 全部候选永久裁决且没有 accepted 时，父 receipt 按证据终结：存在 already_running 为 `covered_by_existing_intent`，否则存在 cooling 为 `cooling_suppressed`，其余为 `no_eligible_candidate`。这些终态都表示当前已有保护或没有合法探测对象，可让 producer ACK；不能留下单候选 already-running / cooling 的永久 pending。网络 / Redis / PostgreSQL 错误和提交结果未知不推进候选状态，只更新 retry_after。重放仍受同 scope 5 分钟 admission、active-intent 条件唯一约束、账户 / 全局预算和 receipt 上“同一 request handoff 最多 accepted 一个新 intent”的事务约束，不能形成探针风暴。
- positive observation 只有 PostgreSQL observation 行确定为 `committed / stale` 才是持久终态。`committed` 由状态机使用；`stale` 只作为不可重放的审计终态，二者都可让 producer ACK。
- spool 按字节、高水位年龄和未 ACK 条数设置硬边界。到达预警线即令该 gateway `readyz=false` 并从代理摘流，在积压恢复前不接新请求；append 不可写、sync 超时、校验损坏或达到硬上限时同样立即摘流并产生 critical 告警，禁止丢弃最旧行后继续宣称 ready。当前已在途响应仍按原协议结束。
- 启动 preflight 必须验证目录在 release 外、权限正确、原子 append / fsync / rename 可用且剩余空间满足保留预算；每实例只写自己的 segment。gateway ready 前要把新的 durable producer epoch 注册到 active deployment 的 expected producer inventory，并确认同 host replay service 已持可续租的数据库 fencing lease；replay 同时取得 OS 文件锁后才能接管遗留 segment。清理必须以连续 ACK cursor、匹配 replay fencing token 和不可变 terminal proof 为依据，不能按文件年龄猜删。producer 只有 sealed、全部 delivery resolved、ACK 连续到 final sequence 且 registry 进入 retired 后才能从 expected inventory 删除。
- control plane 分别保存 producer epoch + sequence 的连续 delivered / ACK 水位；ACK 只可越过 `processed_terminal` 或已有 immutable quarantine proof 的 `quarantined_unconfirmed`。gap、digest 冲突、超过 replay deadline 仍未终态或 spool backlog 超标时，监控显示 `observation_handoff_unconfirmed`；能够从规范化 envelope / candidate rows 确定 runtime + scope 的项写持久 health hold，只让匹配 Attempt 的 `evidenceDataStatus=unconfirmed` 并精确过滤，不能把模型 B 的 hold 提升成整个账户 dataStatus。只能确定账户而不能确定 scope 时才写 account-wide hold。若连账户 affected set 都无法安全确定，producer gateway 立即不 ready，deployment coordinator 同时激活全局 capability handoff barrier，使所有 gateway 的 capability admission / scheduling fail-closed，直到 tail 取证或有审计的全局 adjudication；仅摘除原 producer 不足以保护其他 gateway，不能把这个缺口解释成 unknown / healthy。
- digest 冲突、合法边界内的 checksum 损坏和超过 replay deadline 的 pending receipt 不得永久卡住连续 ACK，也不得被当作正常处理。replay owner 先把原始非敏感 bytes / 两个 digest、producer epoch / sequence、可安全解析的 affected set 和原因写入 release 外只追加 quarantine artifact，再由 control plane 写 `account_capability_handoff_quarantines`、逐 scope hold 和连续 outbox；只有 artifact digest、行 count / 引用和 source sequence 全部校验后，才把该 sequence 标为 `quarantined_unconfirmed` 并推进 delivery ACK。health hold 仍保持，直到同 scope 新鲜 committed success、新 generation 独立探针、revision 失效或有审计的人工 adjudication 解除。
- segment 损坏后只有 header / length / checksum 能证明下一个 record 边界时才允许隔离单条并继续；无法证明边界必须隔离整个 tail、摘流该 producer 并等待取证，禁止扫描任意字节猜下一个 sequence。超过 replay deadline 的 failure candidate 不再用旧业务失败直接创建 intent；若 revision / recipe 仍有效，hold owner 创建新的免费 verification due，manual-costed / unsupported 则等待 owner 操作。这样可以释放 spool，但不会把陈旧或冲突数据静默解释为健康。

生产部署至少预留 24 小时峰值 handoff 容量；replay deadline 不能短于最大允许控制面故障窗口。备份或迁移不得恢复已超过 deadline 的旧 segment 后重新创建 intent；过期项只有在 control terminal receipt / tombstone 或上述 immutable quarantine + health hold 证明原结论时才 ACK，不能靠墙钟丢弃。

可靠性边界必须如实暴露：本机进程 / release 崩溃时，只要 host-local durable append 或 control PostgreSQL 事务任一成功，同一事件可恢复；host / 磁盘永久丢失不在单副本 spool 承诺内，producer 必须转 lost 并维持 deployment-wide barrier。要求 RPO=0 的部署必须按上一条 receipt 契约在 append 被视为 durable 前同步复制到第二故障域；异步副本要在 manifest 声明最大 RPO 并持续暴露实际 `replicaAckThrough`。若某个在途请求恰逢本地 spool / replica ACK 不可用且 control RPC 也未 durable commit，系统无法凭空保证该事件不丢。此时不得记录 `handoff accepted`，gateway 必须立即不 ready、保留 `capability_handoff_unrecoverable` 进程告警并停止承接新流量；能够识别的受影响账户进入 unconfirmed。部署预检、磁盘预留和摘流阈值的目的，是把双故障窗口限制在已经在途的请求，而不是用“零延迟”承诺掩盖它。

### 9.3 请求级 CapabilityFailureCollector

Collector 挂到跨账户重试和后备分组共享的 request attempt tracker：

1. 只有 `trafficSource = user_gateway` 的普通用户流量进入 Collector。激活、周期哨兵、capability / transport probe、冷却复测、人工测试和生产重检不得递归产生新 intent。
2. 每个已经真实派发、终态未形成 protocolValidatedSuccess、失败不是客户端取消或本地前置原因且可以精确归因的 attempt，加入完整 Attempt 描述和非敏感 probe recipe；这包括下游提交后的协议失败、失败终态事件、畸形完成和读取中断。
3. 客户端取消、本地鉴权 / 配额失败、非法请求体、未派发、主动配置截止取消等不加入。retryEligible 与 capabilityProbeEligible 分别计算。
4. 同一请求后续在完全相同 Attempt 描述上成功时，删除较早失败候选。不同 Key 不属于同一描述，不互相取消。
5. 所有路由、流式 finalizer 和异常清理结束后，在最外层 finally、释放 request tracker 与 audit capture 之前只 flush 一次；B 失败后 A 成功救回也必须 flush B。flush 先 append 第 9.2 节 spool，再尝试即时 control RPC。
6. 正常路径的 control RPC 是非抛出、有严格短时限的旁路操作，不进入连接收尾等待。超时、IPC、Redis 或 PostgreSQL 错误不能覆盖原响应或改变 audit 终态，也不得留下无界 detached Promise；对应 handoff 保留未 ACK 并由专用 replay worker 重试。只有本地 durable append 失败时才执行第 9.2 节有界 emergency RPC，并同时摘流。控制面只有在 PostgreSQL ledger 与 handoff receipt 同事务提交后才返回 accepted，超时 / 未知不得记 accepted，也不得丢弃 spool 项等待下一次真实失败。
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
| catalog_only | 只检查目录可见性 | 只展示 visibility；任务以 catalog outcome 中性终结并恢复此前 execution phase，不建立 execution SUSPECT / soft avoid，但 durable accepted 后仍开始 5 分钟 visibility admission 冷却 |
| manual_costed_execution | 每次都需要物理账户所有者显式允许成本 | 普通业务失败只建立精确 scope 的 5 分钟 `cost_verification_notice` 软避让，不创建共享 intent / SUSPECT 或付费调用；带本次成本授权的生产重检可形成硬结论 |
| unsupported | 没有安全配方 | admission 直接返回 unsupported，保持 unknown，不创建 intent、soft avoid 或冷却 |

目录中存在模型不能恢复 execution scope；目录缺失也不能证明执行失败。catalog_only 与 execution 共用 scope admission / active intent 单飞；目录任务无论可见、缺失或未知都不能改变 execution phase，但 accepted 后保留至 `acceptedAt + 5m` 的 visibility cooldown，不能因中性快速终结被下一批请求立即重复触发。图片、音视频等默认 catalog_only 或 manual_costed_execution。后者的业务失败通过 Redis 原子写精确 scope `cost_verification_notice_until = now + 5m`，相同 TTL 内不刷新；它只在有替代 Attempt 时软避让，所有候选都只剩 notice 时仍走现有有界等待 / 兜底，不是 health phase、active intent 或 confirmed unavailable，Redis 丢失也不需要从 PostgreSQL 伪造恢复。API 可由有界诊断事件显示“待成本授权验证”，但账户 aggregate 仍按 unknown / confirming 而非 blocked。

manual_costed_execution 的一次性授权只来自生产重检命令 `{ authorizeCostedExecution: true, maxEstimatedCostUsd }`，后端必须校验物理所有者权限、估算上限、部署级日预算和 Idempotency-Key，并写操作日志；它不变成账户永久开关。真实付费派发前，worker 必须在**同一个 PostgreSQL 事务**锁定 command item 与 physical execution，核对授权、request digest 和成本 token，并同时写入 `execution_started_at`、`cost_execution_started_at`、dispatch idempotency key 和 replay deadline；事务提交后才允许网络发送。任一单边 started 标记、事务提交未知、标记后的超时或 worker 崩溃都收敛为 `cost_execution_indeterminate` 中性终结，禁止自动再次调用。再次执行必须由所有者提交新的成本授权命令。

manual-costed 的恢复同样需要两次独立成功，但**每次物理调用都需要一条新的成本授权命令**。确认 OPEN 的失败事务写 `requires_cost_authorization=true`、`nextProbeAt=null` 和按 5/10/20/30/60 分钟策略计算的 `nextCostEligibleAt`，不写自动 paid due；第一次授权恢复成功写 `recovering + successCount=1 + nextProbeAt=null + nextCostEligibleAt>=database_now+30s`，终结本次 intent / command item并释放预算；达到该时间后第二条新授权命令成功才写 available。任一授权失败仍写 blocked、推进 nextCostEligibleAt、终结本次 intent，不能自动排队再次扣费。业务成功、普通失败、task failure 和进程恢复均不得生成付费 due。definition revision 真正变化才使旧状态 stale。只有未来另行设计有预算 owner 的自动付费 policy，才可承诺自动硬隔离与恢复。

### 10.3 稳定结果

| outcome | 定义 | 能力状态 |
| --- | --- | --- |
| complete_success | 最小请求通过协议校验且 framing 完整 | 正向 |
| capability_unavailable | 独立执行形成任意完整失败，或真实 transport 不完整 | 通用负向 |
| probe_task_failure | 队列、进程、DB、临时配置读取、执行器、取消等本地任务失败 | 中性；仅能证明未派发时才重试同一物理执行 |
| stale | revision、generation、租约、权限上下文或描述已变化 | 丢弃 |
| catalog_visible / catalog_unknown | 目录可见性观察 | 不改变 execution 状态 |

结果中不保存或解释上游正文。HTTP 状态和错误详情仍可留在现有受权限保护的 usage / audit 诊断记录，但不得进入能力决策或 failure class。物理执行在写入 `execution_started_at` 前失败可以安全重试；写入后结果未知时，只有 provider driver 明确声明上游幂等键覆盖本次 endpoint、请求 digest 完全一致、当前时间未超过 `upstream_idempotency_expires_at`，且本地 replay deadline 更早时，才允许复用同一 dispatch key 重试。缺少任一证明或超过上游去重窗口都固定为 `physical_execution_indeterminate` 中性终结，不能按本地 7 天任务保留期推断上游仍会去重。manual_costed_execution 无论上游是否支持幂等都不自动重放。

### 10.4 本地账户级事实

本地可确定配置错误也必须按最小作用域归属，以下分类互斥：

- 单个 credential slot 缺失 / 解密失败、单 Key 装配失败：由 credential owner 标记该 slot 不可执行，只过滤对应 Attempt；不得写整账户状态或阻断其他 Key / 模型。
- 单 Route 的 endpoint、adapter、probe recipe 或映射无法构造：由 Route / capability Catalog owner 标记该 Route 配置无效；不触发探针，也不得扩大到其他 Route。
- 只有 Base URL、代理、协议档案等配置被结构证明为该运行账户**全部 Route 与全部 credential slot 强制共用**，并且没有任何可独立执行的旁路时，现有 account-global configuration owner 才能形成整账户配置门禁。该门禁必须带不可变 provenance 和受影响全集证明，不能由某次请求或单 Key 异常推导。
- 队列不可用、数据库暂时失败、worker 崩溃、临时配置读取失败：probe_task_failure。
- 某个模型没有合法探针模板：unsupported / unknown。

不得把同一异常同时记为 task failure、slot / Route 配置错误和整号故障。账户存在至少一个可构造 Route + credential 时，局部配置错误只能通过 effectiveAvailability / no-effective-route 派生展示，不能把仍可用模型 A 的账户写死。

### 10.5 Key owner 门禁

- worker 执行前重新读取目标凭据槽位和 Key owner 状态；能力重检永远不能旁路、清理或恢复 Key 状态。
- 目标 Key 已删除、目标凭据 lineage / attemptDefinitionRevision 已变化或授权绑定失效：intent 以 stale fenced 终结，撤销当前 generation 的 provisional soft avoid，不产生 capability outcome。仅增加 / 删除其他 Key 导致账户 key-set publication revision 变化时，当前 scope definition 未变则 carry-forward，不能误杀本 intent 或清除其 OPEN。
- Key 当前处于 temporary_unavailable、rate_limited、error 或 disabled：初始 SUSPECT intent 中性终结并恢复此前稳定 phase；已经 OPEN / HALF_OPEN / RECOVERING 的 scope 保留原 phase。两类都必须在同一事务终结 member / 当前 intent、释放 logical / physical reservation、写 `parked_by_key_owner` due 与 outbox，不能只取消 claim 后留下 active-intent 单飞。Key owner 恢复事件重新激活 parked due；manual-costed 只恢复“可授权”状态，不自动创建付费调用。
- Key owner 恢复事件只重新安排原 capability scope 的独立探针，不能直接把能力改 available。

## 11. 状态机

### 11.1 触发

- probeStrategy=execution 的业务失败 admission：CLOSED / 无行 -> SUSPECT，并建立默认 30 秒 softAvoidUntil。catalog_only 保持 execution phase，仅建立 visibility intent / cooldown；manual_costed_execution 只写 5 分钟 Redis cost-verification notice，unsupported 的普通业务失败不创建共享 intent 或跨请求软状态。
- 周期哨兵或人工生产重检：统一经过 durable admission、generation、claim、预算和 fencing，但在 unknown / available 上只附加 verification intent，保持稳定 phase 且不建立 soft avoid；独立结果成功保持 available，失败才进入 OPEN。
- 存在 active intent 时不创建新 generation。SUSPECT 只由当前 intent 终结；OPEN / HALF_OPEN / RECOVERING 没有 active intent 时，只有符合状态时钟的自动 due 或有权限生产重检可以通过同一个 admission 原子分配新 generation，普通业务失败不能推进。
- execution 的 OPEN 到期由恢复调度器通过原子 admission 获取 lease、创建新 generation 并进入 HALF_OPEN；有权限生产重检可走同一入口提前请求验证，但不能绕过 active intent、最小成功间隔或 lease。manual_costed_execution 的 OPEN 没有自动 due，只有新的物理所有者成本授权命令才能通过同一原子入口执行 OPEN -> HALF_OPEN；命令本身不得直接写 phase。

30 秒只是共享软避让窗口，不是探针 hard deadline。窗口过期后 SUSPECT 仍保留 active intent 和单飞，但普通调度可以重新使用该 scope，避免合法长探针造成长时间全池停摆。

### 11.2 完整转移矩阵

| 当前状态 | complete_success | capability_unavailable | probe_task_failure / lease 过期 | stale |
| --- | --- | --- | --- | --- |
| unknown / available + verification intent | available | temporarily_blocked | 回到此前稳定展示；不增加失败 | 不变 |
| suspect | available | temporarily_blocked | 同 generation 有界重试；耗尽后回到此前稳定展示 | 不变 |
| temporarily_blocked | 由自动 due 或有权限重检先原子进入 half_open 后执行 | 保持 blocked | 保持 blocked，按任务退避 | 不变 |
| half_open | recovering，成功数 1，并安排第二次恢复验证 | blocked 并推进能力退避 | 回 blocked，不增加能力失败次数，并安排基础设施重试 due | 不变 |
| recovering | 达到 2 次则 available，否则保持 recovering 并安排下一次恢复验证 | blocked 并推进能力退避 | 保持 recovering，不增加能力失败次数，并安排基础设施重试 due | 不变 |

初始能力退避为 5、10、20、30、60 分钟，之后保持 60 分钟并加入确定性正负 20% jitter。第一次恢复成功后至少间隔 30 秒再执行第二次。能力策略参数由 scope policy 提供，不修改 transport scope 的既有参数；manual-costed 只把同一策略写成 `nextCostEligibleAt`，不生成自动 due。

恢复推进不能依赖 worker 内存或下一次用户请求。**每个**探针 outcome 都必须在同一 PostgreSQL 事务锁定 incident、logical intent 和 member，终结本 intent / member、释放预算、更新 phase / backoff / recovery count，并写下一代 due 与统一 control outbox；不存在“状态已改但旧 intent 仍 active”或“消费 due 后没有下一步”的中间态。首次 `capability_unavailable` 从 verification / SUSPECT 进入 blocked 时也必须在该事务推进 capability backoff，并为免费 execution 写唯一恢复 due。half_open success 对免费 execution 原子写 `recovering + successCount=1 + nextVerificationAt>=database_now+30s`；recovering success 未达阈值时递增 count 并写下一次 due，达到 2 时写 available 并清 due。任何 unavailable 终结当前 intent、回 / 保持 blocked、推进能力退避并写下一代 due；available 重置 backoff 并清 due。manual-costed 按上一节改写为 `nextProbeAt=null + nextCostEligibleAt`，绝不写 paid due。

同 intent 的 pre-dispatch task retry identity 固定为 `physicalExecutionId + attemptCount`，只覆盖可证明未调用上游或仍在 provider 幂等窗口内的重放；跨 generation 的恢复 due identity 固定为 `scopeId + routeDefinitionRevision + attemptDefinitionRevision + incidentStateVersion + recoveryStep`，不得把旧 intent 的 task retry 冒充下一次独立恢复。task retry 耗尽时 half_open 回 blocked、recovering 保持 recovering，并按 30 秒、1 分钟、2 分钟、5 分钟的独立 infrastructure backoff 写下一代 durable due；它不增加能力失败次数或 capability backoff。due claim 只有 admission accepted 的事务才能消费；saturated / 基础设施失败原子改写 bounded capacity retry，already-running 关联 existing intent，cooling 推进到状态时钟，stale / deleted 才可终结删除，claim 崩溃按 lease 恢复，禁止到期行热循环或永久丢失。

task retry 使用 30 秒、1 分钟、2 分钟、5 分钟退避，但只适用于物理派发前可证明未请求上游的失败，或仍处于 provider driver 声明的幂等覆盖窗口且 request digest / dispatch key 完全相同的重放。写入 `execution_started_at` 后结果不确定且缺少上述证明时，physical execution 以 indeterminate 终结，member 按 task failure 回退，不对同 generation 再建物理调用。manual_costed_execution 的两个 started 标记原子提交后，任何不确定结果都禁止重放。初始确认任务达到配置的最大任务重试后终结 intent，释放 SUSPECT 为此前稳定展示；已确认 OPEN / RECOVERING 不因基础设施失败被恢复为健康。

### 11.3 租约与时钟

- 文本 execution 初始 hard deadline 为 75 秒，覆盖现有最大 60 秒执行策略和安全余量。
- 图片付费 execution 初始 hard deadline 为 135 秒，覆盖现有 120 秒执行策略和安全余量。
- catalog probe 初始 hard deadline 为 30 秒。
- 以后新增 lane 必须先定义执行器 hard timeout，再令 lease 覆盖 hard timeout + 安全余量。

真实上游结果写入 physical execution 时必须满足 physical claimUntil 大于数据库当前时间；尚未形成持久结果时失去 physical lease 必须立即取消，迟到结果只能记 stale。结果一旦以 `result_ready / indeterminate` 持久化，后续逐 member 写回改用可重新领取的 **fanout delivery lease**，不再要求原短期 execution / logical worker lease 仍有效。PostgreSQL 使用 CURRENT_TIMESTAMP，Redis Lua 使用 Redis TIME；客户端 Date.now 只用于展示。重新领取 fanout lease 只能改变 delivery token / owner / claimUntil，不得改变冻结时保存的 generation、logical fencing、claimedPositiveObservationVersion、claimStartedAt、执行结果或 observedAt，也绝不能重发上游。

### 11.4 业务成功竞态

业务请求不直接终结 active intent，但不能让较旧的负向探针覆盖较新的同 scope 成功：

1. physical claim 冻结 member 时，把每个 scope 当前的 `positiveObservationVersion` 固化为该 member 的 `claimedPositiveObservationVersion`。
2. 同 scope 的 protocolValidatedSuccess 生成确定性 observationId；只有 control owner 的 PostgreSQL scope 串行化事务原子提交 committed observation、递增一次 version 和写连续 outbox 后才算正向 fence。不同 Key 或不同 Attempt 不影响它，未提交诊断也不递增。
3. `capability_unavailable` outcome 的 CAS 除全部 fencing 外，还要求当前 version 等于 claim 值。若已变化，控制面把当前 intent 终结为 `superseded_by_newer_success` 并释放预算，不提交负向 phase。初始确认恢复到 admission 前的 unknown / available，并为免费 execution 写 durable verification due；调度器用新 generation 建 verification intent。原本已 OPEN 的免费 scope 保持 OPEN，并把 due 提前到 `max(database_now, firstRecoverySuccessAt+30s)`。旧 generation 已终态后绝不复用；manual_costed_execution 只更新可授权提示与 `nextCostEligibleAt`，不自动生成新的付费验证。
4. complete_success 不使用上述负向 version equality gate，可以按匹配 generation / logical fencing 正常提交；业务成功发生在 claim 之前时已包含在冻结值内，后续独立失败仍可生效。

因此终结动作仍由 intent owner 完成，普通业务成功不能直接恢复 OPEN；但“较早探针失败、较晚业务成功、失败结果再迟到”不会误开电路。

配置变更只在当前 scope 的 route / attempt definition revision、membership incarnation 或 binding 权限变化时使该 intent stale；账户 publication 双 revision 变化而 scope 定义未变时 carry-forward。清理不得删除 generation floor 后允许旧结果盲 upsert。

## 12. 单飞、5 分钟冷却与预算

### 12.1 原子 admission

单飞身份固定为精确 Attempt `scopeId`，不包含由调用方预生成的 generation。admission 对外仍是一个幂等状态机，但为了和成功 gate 对齐，内部使用第 9.1 节 reservation -> Redis marker -> ledger 三段协议：

1. 校验 runtime projection revision、当前 phase、active intent 和业务触发冷却。
2. 校验 per-runtime logical、physical account pending / running 与 global 容量；物理 running 槽被占用只让新 intent 排队，不能把不同 scope 误判 saturated。
3. 只有 winner 在 PostgreSQL gate reservation 中分配下一 generation、gateEpoch、reservation token 和候选 softAvoidUntil；此时不消耗逻辑预算、不写 SUSPECT，也不返回 accepted。
4. Redis provisional marker ACK 后，最终 ledger 事务重新校验 scope definition revisions、active intent 和容量，以 scopeId + generation 唯一约束写 durable intent；pending winner 与 SUSPECT / softAvoid、预算 reservation、handoff candidate accepted / parent terminal、delivery resolution 和 outbox 同事务提交，此事务才是 accepted 线性化点。容量在两段之间变化时 reservation 以 retryable / compensated 终结，不留下 phantom generation。

两个 gateway 不得各自生成 generation 后用不同 Redis key 加锁。

### 12.2 业务触发冷却

- 同一完整 Attempt scope 在一个 durable intent 被接受后，5 分钟内不再接受新的业务失败触发；catalog_only 的中性终结也保留该 visibility admission 冷却，但不建立 execution phase 或 soft avoid。
- active intent 优先于冷却；未终结前永不创建下一 generation。
- OPEN / HALF_OPEN / RECOVERING 只由自动 due 或有权限生产重检经同一 admission、时钟、generation 和 lease 入口推进；HTTP 命令、业务成功和 worker 均不得旁路直接改 phase。
- task failure 重试同一 intent，不依赖新用户请求，也不重新开始 5 分钟窗口。
- unsupported / revision_stale 不创建 SUSPECT，也不开始冷却。只有 logical pending / global hard budget 真正无槽才返回 queue_saturated；它不伪造 SUSPECT，但业务 handoff candidate 保持 retryable，并从 durable receipt 派生精确 scope 的 `verification_backpressure_notice`。notice 只在有替代 Attempt 时软避让、Route 展示 confirming，不能称为 blocked；容量释放后同一 handoff 自动重试 admission，不等待新请求。
- manual_costed_execution 的 cost-verification notice 使用独立 5 分钟 Redis TTL 和同 scope 原子首次写，不占 intent / pending / running 预算，不因重复失败续期；它只能软避让并提示 owner 授权，不能被统计或前端称为 blocked。

人工生产重检使用独立的 30 秒用户 / 账户命令限频，可以绕过业务触发冷却，但不能绕过 active intent、物理预算、hard deadline、cost policy 或 fencing。

### 12.3 逻辑与物理预算

- 每个 accountRuntimeKey 同时最多 8 个**不同 scope** 的 active logical intent；同 scope 仍由条件唯一约束只允许一个。该额度使模型 A 的任务 / 基础设施重试不能阻塞模型 B 建立自己的 SUSPECT 和 durable pending intent。
- 每个 credentialSourceAccountId 同时最多一个 running physical execution，跨其全部运行实例等待中的 logical intent / physical execution 合计最多 16；多个 joined member 只占一个 physical running 槽，但各自仍占 logical pending 配额。物理串行负责限制真实上游探针风暴，logical pending 只保存精确诊断责任和公平排队。
- 每个 credentialSourceAccountId 的**自动** physical probe 另有跨节点 5 分钟启动门禁。PostgreSQL 原子 CAS `next_automatic_physical_probe_at` 是持久事实，Redis 只做可重建快路径；execution、catalog、周期哨兵和恢复 due 只有在真实 physical start 事务成功时才推进该时间，join 到同一次 physical execution 不重复推进。其他 scope 保留 logical due 并按账户内公平队列等待，不能在几十秒内串行打完 8 个模型。所有者显式生产重检可绕过这条自动间隔，但仍受 running=1、30 秒命令限频、成本授权、global budget 与 physical key 单飞；人工诊断测试仍零生产副作用，不能借此推进 capability。
- 全部署 pending 默认上限 4096，按逻辑 intent 计数以防授权实例 fan-out 绕过容量；running 的物理执行继续复用共享账户诊断 limiter，具体 lane 并发由现有探针限制统一配置。
- 调度采用 due physical accounts 集合加每账户队列轮转，不用一个 scope 时间 ZSET 冒充公平队列。
- 物理执行单飞使用独立 `PhysicalProbeExecutionKey`，明确排除 accountRuntimeKey、runtimeAccountId、系统账户和授权实例身份。它包含 credentialSourceAccountId、稳定 credentialScopeKey、协议档案、Base URL / proxy / egress 不可逆指纹、adapterRouteKey、adapterImplementationRevision、最终模型、endpoint、lane、recipeVersion、plannerContextKey、sourceRequestShapeKey 和 probeStrategy。`manual_costed_execution` 额外纳入 commandItemId / 物理执行幂等 token，不与免费任务或另一份成本授权合并；每次真实付费调用都能唯一追溯到所有者授权。只有该 key 完全相同的逻辑 intent 才合并一次执行；结果仍按各自 runtime binding、definition revisions、generation 和 lease 分别 fenced 写回，账户 publication revision 单独用于来源解释。
- logical / global 队列满时 admission 返回 saturated 并在同一 handoff receipt 上保存 retry_after；不建立健康 phase，但 projector 原子创建 / 续期 `verification_backpressure_notice_until = min(next_retry_at + safety, now + 5m)`，重复用户失败本身不续期。容量 owner 按 due 公平重试，accepted / covered-by-existing / cooling / permanent skip 后清 notice。替换 pending 项必须在同一原子操作中终结旧 intent、解除旧 soft block 和释放预算，不能先腾计数再丢任务。

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
- route_membership_incarnation
- credential_membership_incarnation
- route_definition_revision
- attempt_definition_revision
- capability_universe_revision
- soft_avoid_until_ms
- probe_strategy
- last_probe_outcome
- last_probe_at_ms
- last_business_success_at_ms
- positive_observation_version
- positive_gate_epoch
- positive_gate_resolution_epoch
- normal_refresh_due_at_ms
- incident_state_version
- recovery_success_count
- capability_backoff_step
- scope_policy_revision
- next_cost_eligible_at_ms
- requires_cost_authorization
- positive_evidence_expires_at_ms

新增 account_capability_probe_intents，只保存任务生命周期，不复制健康 phase：

- intent_id、scope_id、generation
- runtime_account_id、account_runtime_key、credential_source_account_id
- binding_system_account_id、bound_group_id、account_authorization_id（自有账户可为空）
- effective_dispatch_revision、capability_universe_revision、route_definition_revision、attempt_definition_revision
- recipe_version、probe_recipe_json
- claimed_positive_observation_version、claim_started_at、physical_execution_id
- purpose、status、available_at
- claim_token、claim_owner、claim_until、fencing_token
- hard_deadline、attempt_count、terminal_outcome
- trace_id、created_at、updated_at

唯一约束为 `scope_id + route_definition_revision + attempt_definition_revision + generation`；active intent 条件唯一也覆盖同一 definition identity。generation 在同一 definition identity 内单调递增，removed -> re-added 因 membership incarnation 进入新 definition，不与保留期内旧 intent 冲突；所有 member、due、command 和 tombstone 外键都携带同一复合身份，禁止只按 scopeId + generation 关联。既有 `account_circuit_incidents.scope_key` 对 model_capability 保存同一个 `scopeId`，但 current ledger identity 还必须包含 definition revisions；migration 必须同步扩展 `scope_kind` CHECK 与唯一键。worker 只用 runtime_account_id + 三个绑定上下文字段向权威 repository 加载当前权限，不解析 opaque runtime key。payload 不包含 API Key、OAuth token、用户 prompt、附件或上游正文。gate epoch / resolution epoch / normal refresh due 只能在同一 incident definition 行锁域单调 CAS，reservation、hold 与正向 observation 都不能自行猜下一个 epoch。

新增 `account_capability_probe_dues` 作为跨 generation 的唯一恢复 / infrastructure / parked 时钟，不能把 OPEN 后 due 隐藏在已终结 intent 或 Redis ZSET：

- 条件唯一身份为 `scope_id + route_definition_revision + attempt_definition_revision + incident_state_version + recovery_step`，并保存 membership incarnations、due_at、reason=`capability_backoff|infrastructure_retry|capacity_retry|positive_observation_refresh|parked_by_key_owner`、`pending | claimed | terminal`、claim owner / until / fencing token、attempt_count、retry_at、prior_intent_id、accepted_intent_id、policy revision 和 outbox sequence。
- outcome 事务推进 incidentStateVersion、终结当前 intent / member、释放预算并插入下一代 due；available / stale / deleted 在同事务终结不再适用的 due。只有新 admission accepted 并关联新 intent 的事务才消费 due；capacity / infrastructure retry 只改写 claim 与 retryAt，不能提前分配 generation。
- due owner 重启按 pending / 过期 claim 恢复，Redis ZSET 从本表重建。相同 stateVersion / recoveryStep 重放返回原行，不创建第二时钟；更旧 stateVersion 永远不能唤醒当前 scope。

新增 `account_capability_positive_observations` 作为正向 fence 的 durable reservation：

- `observation_id` 幂等唯一，`positive_fence_key` 条件唯一，并保存 scope_id、runtime / binding 身份、route / attempt definition revisions、账户 publication revisions、gate epoch / winner kind、trace_id、attempt_ordinal、terminal_id、producer_handoff_id、producer_replay_deadline、producer_terminal_proof_id、`committed | projected | stale | duplicate_coalesced`、committed_positive_observation_version、created_at / committed_at / projected_at，以及可审计 resolution_reason。
- active intent / claim success 不受普通 15 分钟正向节流；同 observationId 重放只能返回原行，同 positiveFenceKey 的并发 fallback 只有一个 committed winner。control owner 在同一 scope 串行化事务内校验 definition revisions，插入 committed 行、递增一次 version 并写连续 control outbox；不匹配则插入 stale tombstone，重复 fence 插入 duplicate_coalesced，二者都不递增。不存在“durable pending 但尚未参与 fence”的窗口。
- committed / projected 行是 claim / outcome fencing 的事实源。Redis provisional gate 只减少事务在途期间的多余 claim，丢失后不得削弱 PostgreSQL CAS；孤立 gate 只有在重放 observationId 得到 committed / stale，或冻结 ingress 并证明原事务不存在后才能清理。

新增 `account_capability_positive_gate_reservations` 保存 admission / claim 的三段协议与 hold / refresh epoch：以 `scope_id + route_definition_revision + attempt_definition_revision + gate_epoch` 唯一，包含 kind、claim_group_id、generation、definition revisions / membership incarnations、expected positive version、reservation token、`reserved | marker_applied | ledger_committed | compensated | terminal`、marker ACK、lease / owner、linked intent / physical execution / observation 和 outbox 序号。每个 incident definition 的 `positive_gate_epoch` 单调不回退；removed -> re-added 使用新 definition identity，不与保留期内旧 reservation 冲突。reserved / marker_applied 不能被普通 success 当成可 coalesce 终态。physical execution 另保存不可变 `claim_group_member_digest / claim_group_status`，只有成员全集 reservation 与 marker ACK 都齐备，才能在一次多行锁事务提交 ledger。reconciler 只能按 token / claimGroupId 完成整组 ledger、整体补偿或在冻结 ingress 后写 tombstone。

新增 `account_capability_physical_probe_account_gates`，以 credential_source_account_id 唯一保存 `next_automatic_physical_probe_at`、last physical execution、policy revision、fencing token 和 outbox sequence。自动 physical start 必须在锁定该行并校验 database_now 到期的同一事务推进下一次时间、占用 running 槽和 claim execution；任务失败若可证明尚未发送上游，可按同 physical execution 的 task retry 处理，但不得再消费一份 5 分钟令牌。Redis 丢失后从该表重建，禁止退回进程内计时器。

新增 `account_capability_handoff_receipts` 与 `account_capability_handoff_candidate_receipts` 作为 gateway spool 与控制面的持久交接边界：

- 父表以 `handoff_id` 唯一，保存 kind、业务 payload digest、`pending | terminal | quarantined_unconfirmed`、disposition、failure nomination 的 scan_cursor / retry_cycle / candidate_resolution_digest、linked observation / intent / command ID、next_retry_at、producer_replay_deadline、producer_terminal_proof_id 和时间字段；同 ID 异 business digest 是不可自动修复的 contract violation。业务 receipt 不拥有 producer ACK identity，同一 handoff 可由多个 delivery 引用。
- 子表以 `handoff_id + candidate_ordinal` 唯一，保存每个候选自己的 scope、runtime / binding 身份、definition revisions、candidate digest、`pending | permanently_skipped | retryable | accepted | quarantined_unconfirmed`、disposition、retry_after、attempt_count 和 linked intent；另有 `UNIQUE(handoff_id) WHERE status='accepted'`，数据库级保证整组最多一个 accepted。retryable 行有 due 索引，scan cursor 到末尾后仍会按 due 回访。候选顺序或身份不一致固定为 digest 冲突。
- 首次扫描和 due 回访都先 `SELECT ... FOR UPDATE` 锁同一父 receipt，再按 ordinal 锁 candidate；accepted intent、candidate accepted、父 `terminal(disposition=accepted)`、关联 delivery 的 `processed_terminal` 和 outbox 在同一 admission 事务提交，并立即允许连续 ACK，不等待探针执行结束。该事务还必须把所有非 accepted sibling 原子写成 `permanently_skipped(disposition=covered_by_group_accept)`，删除其 retry due，并清理各自 verification_backpressure_notice；否则前面 saturated 的候选会在父终态后继续重试。already-running / cooling / unsupported / stale 的永久结论也在父锁内更新；容量饱和和基础设施失败在尚无 accepted 时不写伪 terminal，只更新 pending 的 retry_after。父锁、条件唯一约束和事务检查共同保证并发 scheduler 不会让两个 ordinal 各建 intent。
- receipt 至少保留 31 天且不得短于 producer 最大 replay / 备份恢复窗口。完整行到期后只有 producer terminal proof、连续 producer ACK 水位、关联 observation / intent / outbox 终态都齐备时才可压缩为 ID + digest + disposition + epoch + deadline 的 tombstone；同 handoffId 重放永远返回原结论，不能创建第二 intent 或第二次 observation version 增量。

新增 handoff quarantine 与精确 hold：

- `account_capability_handoff_quarantines` 以 deployment epoch + producer epoch + sequence 唯一，保存可空 handoffId、kind、first / conflicting digest、artifact digest / immutable location、reason=`digest_conflict|checksum_corrupt|replay_deadline_exceeded`、affected-set digest、replay fencing token、`open | adjudicated | superseded_by_fresh_evidence`、operation log / adjudicator 和时间字段。业务库只保存非敏感索引与 artifact digest，原始 quarantine artifact 位于 release 外受限目录并纳入备份 / 校验。
- 通用 `account_capability_evidence_holds` 以 `source_kind + source_id + scope_id + route_definition_revision + attempt_definition_revision` 唯一，source_kind 至少覆盖 `handoff_quarantine | fanout_delivery_unconfirmed | projection_gap`，并保存可空 quarantine_id、runtime / binding 身份、membership incarnations、activation control sequence、positive_gate_resolution_epoch、fresh verification due、`active | released`、release proof 和 outbox sequence。handoff、fanout 与 projector 不能各造一张不互通的 hold 表。active hold 只使精确 Attempt `evidenceDataStatus=unconfirmed` 并从调度过滤；Route / 账户按第 17 节局部聚合，不写 OPEN、accounts.status 或其他模型。definition revision 改变、新鲜 committed observation / probe 或人工 manifest 只能按 activation sequence / epoch CAS 逐项释放。
- producer sequence receipt 另有 `processed_terminal | quarantined_unconfirmed` delivery resolution。两者都可推进连续 ACK；后者永远不能推进健康 resolved watermark，只靠对应 hold 控制局部 readiness。affected set 不可证明时不创建伪 scope hold，producer gateway 保持 not-ready，直到 tail 取证完成。

新增显式 `account_capability_handoff_deliveries`：主键为 `(deployment_epoch, producer_epoch, sequence)`，允许多个 delivery 引用同一 handoff_id；每行保存 business digest、record / checksum version、`received | processed_terminal | quarantined_unconfirmed`、linked receipt / quarantine、replay fencing token、artifact digest、delivered_at / resolved_at 和 producer terminal proof。同一 handoff_id 的 delivery 必须校验 business digest 一致，但不能用 handoff_id 唯一约束阻止跨 epoch / sequence 重投。连续 delivered / ACK / health-resolved watermark 只能从该表和不可变 tombstone 推导，不能只存在内存或指标中。producer epoch 在 gateway ready 前写入 release 外原子 metadata 并 fsync；每 epoch 只有一个 segment writer，sequence 单调且永不复用。append / sync 结果未知时立即封存该 epoch、摘流并停止写入；append 失败但 emergency RPC committed 时仍用已分配的 epoch + sequence 写 delivery terminal，进程不得继续该 epoch。若磁盘故障无法写封存标记，重启也必须创建全新持久 producer epoch，旧 epoch 只由 control terminal proof / 取证处理。

producer inventory、watermark 和 replay lease 是显式控制事实：

- `account_capability_handoff_producers` 以 deployment epoch + host identity + producer epoch 唯一，保存 inventory certificate revision / digest、metadata digest、record contract、first / final sequence、`registered | active_target_pending | active | draining | sealed | drained | retired | lost`、连续 delivered / ack / health-resolved watermark、unacked count / bytes / oldest age、unknown-tail flag 和 last heartbeat。prepared target 的 metadata / initial sequence 必须先落在 release 外并 fsync，注册后只能处于 `active_target_pending`，数据库拒绝它产生 delivery、业务 mutation 或承接流量；只有 activation 或运行期 inventory pointer CAS 同时匹配 certificate 与 producer set digest 后才可转 active。active epoch coordinator 通过签名 certificate 链保存 expected host / gateway inventory；缺失心跳或主机丢失只能转 lost + barrier，不能从检查集合消失后变成 ready。
- `account_capability_handoff_replica_receipts` 以 deployment epoch + producer epoch + sequence 唯一，保存 content digest、replication mode、replica set digest、源 / 副本 failure-domain identity、replica fencing token、replica ack through、immutable object location / digest 和 ack 时间；相同主键只能按相同 content digest 幂等返回，异 digest 触发 quarantine / barrier。RPO=0 producer 的本地 durable watermark 不得越过对应 replica ack through；receipt gap、同故障域伪副本、digest 冲突或 stale token 都使 producer / gateway not-ready。异步模式同样保存实际 ACK，但只用于计算声明 RPO，不参与虚假的同步成功。
- `account_capability_handoff_replay_leases` 以 producer epoch 唯一，保存 owner、lease_until 和单调 fencing token。delivery / quarantine / watermark / ACK cursor 的每次 CAS 都要求当前 token；本地 ACK cursor 同时写 token 与 terminal proof digest，旧 owner 即使仍持文件描述符也不能推进。
- `account_capability_handoff_tail_quarantines` 独立保存 host、producer epoch、segment identity、last_good_sequence / offset、tail_start_offset、tail digest、artifact digest / location、可空 expected sequence 和 replay fencing token；它不伪造 handoffId 或可信 sequence。active tail quarantine 同事务创建 deployment-wide barrier。
- `account_capability_handoff_account_holds` 以 quarantine + accountRuntimeKey 唯一，保存账户 publication revisions、affected-set reason、resolution state / proof 和 outbox；只能定位账户不能定位 scope 时使用。active 时该账户 dataStatus unconfirmed；解析出精确 scope 后在同一事务替换为 scope holds 并释放 account hold。
- `account_capability_handoff_global_barriers` 以 deployment epoch + barrier id 唯一，保存 tail quarantine、冻结的 producer inventory certificate revision / digest、`active | resolved | adjudicated`、before-image / 双人 adjudication 和 outbox。active 时所有 gateway 的 capability scheduling / admission fail-closed。只有尾部被证明无 durable record、恢复为逐 delivery / scope / account hold，或有审计的 deployment-wide adjudication 时才能 CAS 解除。

跨授权实例物理单飞使用两张持久表，不能只靠进程内 promise 或把逻辑 intentId 当物理任务：

- `account_capability_physical_probe_executions`：保存 physical_execution_id、physical_probe_key_digest、probe_strategy、recipe / request digest、`pending | claimed | result_ready | indeterminate | terminal`、execution claim / fencing / lease、execution_started_at、dispatch idempotency key、provider idempotency contract / expiry、本地 replay deadline、cost execution token、不可变通用结果、observed_at 和时间字段；physical_probe_key_digest 的 active 条件唯一只覆盖 `pending | claimed`。写入 result_ready / indeterminate 的事务必须释放该 key 与 running 槽，使后续新 intent 可创建新的 physical execution，而不能消费早于自身 admission 的旧结果。manual_costed 的 key 已包含 command item token。
- `account_capability_physical_probe_members`：按 physical_execution_id + intent_id 唯一关联逻辑 intent，并保存 `joined | frozen | fanout_pending | terminal`、terminal_reason、冻结的 logical fencing / generation / definition revisions / membership incarnations / claimedPositiveObservationVersion / claimStartedAt / resultObservedAt，以及独立 fanout_claim_token / owner / until / fencing、fanout_attempt_count、next_delivery_at、delivery_deadline 和 result_digest。另以条件唯一约束保证一个 intent 同时只关联一个 `joined | frozen | fanout_pending` member。数据库约束 / trigger 拒绝向非 pending execution 插入 joined member；claim 后到达的相同 key intent 只能另建物理执行，不能消费早于自身 admission 的结果。
- join 与 claim 必须先 `SELECT ... FOR UPDATE` 锁同一 physical execution 行。join 在锁内重新校验 `status=pending AND execution_started_at IS NULL` 后才插 member；claim 在锁内原子切为 claimed、封闭 join 窗口并冻结当时全集。两者不能只靠应用层先读状态。claim 事务把不适合本次执行的 member 直接转成 `terminal(detached)`，清空 intent.physical_execution_id、释放本次物理关联、保留逻辑预算、写 durable due 与 outbox，随后可加入新执行；不得留下非终态 deferred 行。其余 member 走 `joined -> frozen`，同时把 logical intent 标为由该 physical execution 持有，并固化上述不可变 fence。没有可执行 member 时在同一事务直接 terminal 并释放 active key / running 槽，不进入 claimed。
- 物理结果事务一次写入 `result_ready / indeterminate`，把所有 frozen member 与对应 logical intent 原子转成 `fanout_pending`，释放 physical key / running 槽并写连续 outbox。之后 fanout reconciler 可逐 member 领取、续租或重新领取 delivery lease；崩溃只重放写回，不改变不可变执行证据或重新调用上游。每个 member 的 outcome 应用以 physical_execution_id + intent_id 条件唯一，锁 member / intent 后在同一事务更新 incident、终结 member / intent、释放逻辑预算并写 outbox。可恢复投影失败按 nextDeliveryAt 有界退避；超过 attempt 上限或 deliveryDeadline 时原子写 `terminal(fanout_delivery_unconfirmed)`、建立同 definition 的精确 evidence hold、释放逻辑预算并保留不可变 result 供裁决，不能让一个 poison member 永久占槽或伪装成功。单个 member stale / superseded / unconfirmed 不影响其他 member；全部已冻结 member terminal 后 physical execution 才 terminal。claim 事务发现零 frozen member 时必须在同一事务把 execution 直接 terminal、释放 active key / running 槽并写 outbox，不能留下 poison key。

生产重检另建持久命令资源，不能把 HTTP 的批量任务和 10 分钟幂等语义临时塞进逐 scope intent：

- `account_capability_recheck_commands`：保存外部 `task_id / command_id`、actor 类型与 ID、runtime / system account 身份、request digest、两个 expected revision、命令状态、item / accepted / terminal 计数、创建 / 完成时间和终态资源过期时间。活动命令不得按墙钟清理；只有进入 terminal 后才设置 `resource_expires_at = completed_at + 24h`。
- `account_capability_recheck_command_items`：按 command_id + ordinal / scope_id 唯一，拆分 `lifecycle_status = pending_admission | active | terminal` 与 `disposition`。pending 初始 disposition 为 `pending`；admission 后可为 `accepted / already_running` 并保持 active；`unsupported / cost_authorization_required / saturated / stale / forbidden / no_longer_eligible / cost_authorization_expired / rollback_cancelled` 都是 terminal disposition；linked intent 结束后再写 `complete_success / capability_unavailable / probe_task_failure / superseded_by_newer_success / indeterminate` 等 terminal disposition。每个 item 只从 pending -> active -> terminal 或 pending -> terminal 单调推进，terminal 不可改写。成本授权项还保存本次授权上限、授权过期时间与物理执行幂等 token，不保存凭据或用户 payload。
- `account_capability_recheck_idempotency`：按 actor + runtime account + Idempotency-Key 唯一保存当前 command_id、request digest 和 `idempotency_expires_at`。事务内锁定该行；10 分钟内同 digest 返回原 command，不同 digest 返回 `409 idempotency_conflict`；过期后以 CAS 替换指针，旧 command 在自身 24 小时保留期内仍可查询。
- `account_capability_recheck_selections`：保存 selection_id、actor / runtime identity、来源 capability view publication / version、双 revision、候选策略版本、过滤 digest、冻结评价时间、candidate_count、next_sort_tuple、cursor_version、claimed_count、`active | exhausted | expired`、idle_expires_at、absolute_expires_at 和 source manifest digest。候选集合由 immutable source publication + 固定 predicate + Route / Attempt 唯一排序逻辑冻结，不复制最多 8192 x 50 个全部 scope。source publication 必须预聚合固定 automatic eligibility count，selection 用它初始化 candidate_count；remainingCount 只按 candidate_count - claimed_count 计算，不临时 COUNT mutable current。
- 每个命令在一个事务锁定 selection，验证 selectionRef 中 expected cursor_version，从保留的 source view as-of 版本按 next_sort_tuple keyset 读取 `LIMIT 101`，只把最多 100 条物化为 command items，并插入 `account_capability_recheck_selection_page_claims(selection_id, cursor_version, command_id, from_tuple, to_tuple, member_count, member_digest)`；随后 CAS 推进 tuple / claimed_count / cursor_version，并在同一事务提交 command、items、operation log 和 idempotency pointer。相同 Idempotency-Key 先重放原 command，不再次领取；不同 key 使用已消费的旧 selectionRef 返回 `409 selection_cursor_conflict`，不能重复领取同页。
- selectionRef 的 idle TTL 为 10 分钟，每次成功领取一页可续 10 分钟但不得超过创建后 24 小时 absolute expiry；领取事务同时把 source publication、Attempt view、Catalog / credential baseline 的 retain_until 单调延长到新 idle expiry + safety。当前 pointer 或命令自身 outbox 推进不改变 source 集合。候选当前已恢复、被占用或失去资格仍消费其冻结位置并写 terminal(no_longer_eligible / stale / forbidden)，不能用后排项回填导致集合漂移。
- 条件唯一约束限制同 actor + runtime + 双 revision + filter digest 同时只有一个 active selection；每 runtime 最多 4 个、全局最多 1000 个 active selection，达到容量返回 `429 selection_capacity_saturated`。同 actor 重复创建时返回 `409 selection_in_progress`，原 command task 可重新取得最新 nextSelectionRef。idle / absolute 到期原子转 expired 并释放容量；已领取 command / item 仍按自身 24 小时契约终结和查询。

HTTP 在产生任何探针副作用前先以单个数据库事务校验当前权限、解析 scopeRef / selectionRef、核对 revision，并把已解析的 scope 身份、授权依据版本和 selection member 持久化到 command / items，同时写操作日志及幂等指针。随后按 item 执行原有 durable admission；dispatcher / reconciler 重新校验当前权限、runtime binding、资源存在性和双 revision，但不再验外部 ref TTL、原 capability snapshot 是否仍是 current，也不因本 command 自己推进 outbox 把 item 判 stale。进程在中途退出时只恢复 lifecycle_status=pending_admission 的 item，不重新创建 command、selection 或重复成本授权。linked intent 终结时在同一事务链把 active item 转 terminal 并推进 command 计数；所有 item 的 lifecycle_status 都是 terminal 后 command 才 terminal。这样 `Location`、批量部分接纳、already-running 关联和重试查询都有唯一事实源。

新增 account_capability_scope_catalog，保存当前 revision 的 Route 描述和 probe strategy；由配置事务 / outbox 唯一 owner 重建。

pending_test 激活另建 durable lazy selection，不能只把“下一个模型”放在 worker 内存：`account_activation_probe_selections` 保存 selection_id、runtime / publication identity、Catalog / credential baseline digest、definition / membership set digest、稳定排序版本、cursor、candidate / attempted / terminal count、`active | succeeded | exhausted | stale`、created / updated；`account_activation_probe_selection_items` 只按页物化最多 8 个当前免费 execution Attempt，保存 ordinal、scope / definition / binding、linked intent / physical execution 和终态。selection / page claim / schedule 更新同事务，重启后从 cursor 继续；同一 current publication 只有一个 active selection。任一 item complete_success 原子把 selection succeeded 并按计划激活账户，其他未执行 item 终结为 covered；definition / binding 变化使旧 selection stale 后由新 publication 重建，不能重复探同一旧失败项或回退单一 healthCheckModel。

管理读取另建短期版本化 capability view，不把 API 翻页绑在 mutable current 上：

- `account_capability_view_publications` 按 accountRuntimeKey 保存单调 `view_version`、publication_id、双 revision、control applied outbox seq、Key owner snapshot revision、credential membership baseline、账户本地 `dataStatus / dataStatusReason`、固定 automatic recheck predicate / sort version 与 eligible candidate count、published_at、retain_until、manifest digest 和 ready 状态；current pointer 只指向最新 ready publication。deployment-wide barrier 不通过逐账户重写 publication 生效，而由 current deployment barrier revision 在 gateway / API 最外层覆盖，避免账户数越多安全门禁越晚生效。
- `account_capability_route_view_versions` 与 `account_capability_attempt_view_versions` 保存 Route / Attempt API 所需非敏感投影及 `valid_from_view_version / superseded_at_view_version`。projector 只为发生变化的行追加版本，不在每次 observation 时复制整个 8192 Route × 50 slot 目录；Route 聚合与 Attempt 行在同一 publication 事务中原子可见。
- 旧 publication、versioned 行、Catalog / credential baseline 至少保留到最后签发 `capabilitySnapshotRef` 的 10 分钟 TTL + safety。current pointer 推进不使旧引用失效；只有引用到期、保留版本已合法清理或权限不可见时才返回 stale / 404。授权 DTO 仍在读取时按三套字段白名单序列化，publication 不复制敏感凭据。

### 13.3 提交顺序

沿用现有 runtime + durable control-plane saga，但明确责任：

- Redis 是 ready 状态下的调度运行态权威。
- PostgreSQL ledger 是重启、审计、API 和重建的持久权威。
- Redis reservation 只是 provisional；只有 ledger 事务提交 incident、intent 和 outbox 后 admission 才返回 accepted。
- 提交后用 reservation token 标记 Redis committed；若该步失败，outbox projector 必须补齐。affected scope 可确定时只建立该 scope 的 projection hold，使对应 Attempt evidence unconfirmed；只有无法细分但可确定账户的 projection gap 才返回 account runtime_state_unconfirmed，不能把模型 B 的投影失败扩大到模型 A。
- ledger 事务失败时用 token 补偿 provisional reservation；无法确认补偿时使用同样的 scope / account affected-set 规则。无法确定账户 affected set 的缺口激活 deployment-wide barrier，而不是按 unknown 放行。
- incident、logical intent、positive observation、handoff receipt、physical execution / member、result_ready / indeterminate、fanout、due、command linkage 和预算 reservation / release 的每次 mutation，都必须在**同一 PostgreSQL 事务**追加统一连续 control outbox。没有 outbox 的旁表更新属于 contract violation，readyz 必须失败；shadow rebuild 只以该连续序列建立快照后 barrier。
- `PhysicalCapabilityProbeTaskEnvelope` 与 ProbeRecipe 分离，只包含 taskId=physicalExecutionId、physicalExecutionId 和最小 owner 定位信息；Asynq 使用 physicalExecutionId 作为幂等 TaskID。outbox 只有在 enqueue 成功或已存在同 TaskID 后才 ACK。一个逻辑 intent 不能自行再入一份物理任务。
- worker 权威加载 physical execution 与 frozen member 集合并取得 physical claim。真实发送前在一个事务 CAS execution、request digest、dispatch key、started 标记和 replay deadline；manual-costed 同时 CAS command item 及成本 started 标记。通用结果与 frozen -> fanout_pending 转移事务性落库后才 ACK。enqueue 成功但 outbox ACK 失败或结果提交后 Asynq ACK 失败都只能重放同 physicalExecutionId 的状态机；`execution_started_at` 已存在而没有 durable 结果时，只能在 provider idempotency contract 与 replay deadline 仍有效时重试，否则写 indeterminate，绝不盲目再次调用。
- fanout 的所有 outcome 都必须匹配 intentId、scopeId、generation、冻结的 logical fencing、routeDefinitionRevision、attemptDefinitionRevision、membership incarnations、当前 binding 权限、ledgerRevision、不可变 execution result / digest 和当前 fanout delivery lease；账户 publication 双 revision 仅作 provenance。outcome 以 physicalExecutionId + intentId 条件唯一，并在同一事务更新 incident、终结 intent / member、释放逻辑预算、写下一代 due 与 outbox。只有 `capability_unavailable` 额外要求当前 positiveObservationVersion 等于冻结值，且不存在未裁决或 post-claim committed observation；遇到正向 fence 时终结为 `superseded_by_newer_success` 并按免费 / manual-costed 规则产生下一步。`complete_success` 不受 version equality / post-claim positive gate 拒绝，仍可按匹配 generation 推进恢复；task failure / indeterminate 只按中性矩阵终结。禁止因 incident 行不存在 insert / upsert。fanout 崩溃由 result_ready 行和可重领 delivery lease 恢复，不重复物理调用；达到 fanout delivery 上限按精确 hold 收敛，不能永久占用 logical budget。

这不是两个事实源：Redis-only provisional 状态只能保守阻断，不能进入 API / 历史；PostgreSQL-only 未投影状态会触发 readiness gate，不能被当成健康。

### 13.4 冷启动、Redis 丢失和对账

1. 每次全局或账户 lazy rebuild 分配 `rebuildEpoch`，在 PostgreSQL 一致快照中捕获 `snapshotOutboxSeq`，按复合 cursor 扫描所有 SUSPECT / OPEN / HALF_OPEN / RECOVERING、active intent、committed positive observation、未终态 handoff receipt 及紧凑 handoff / observation tombstone、record / tail quarantine、scope / account hold、deployment-wide barrier、expected producer inventory / registry、delivery resolution、delivered / ACK / health-resolved watermark、replay fencing token、非终态 physical execution / member、result_ready fanout、当前 definition revision CLOSED ledger、due、command linkage 和预算 reservation。
2. 扫描结果写入隔离的 shadow epoch；同一 projection owner 在重建期间把 snapshot 之后的 outbox 按连续序号重放到 shadow，遇到 gap 立即保持 not-ready，不得跳号。
3. 在同一 projection lease 下取得数据库写 barrier 并读取当前 stream head。只有 shadow 的 appliedOutboxSeq 连续追平该 barrier、Catalog carry-forward / definition revision 未变、active intent / physical member / fanout lease / due / pending / running counter 已对账、expired execution / delivery claim 已回收、provisional gate 已与 committed observation 对齐、expected producer inventory 中每个 producer 都存在且 delivered / ACK 连续水位与 delivery / quarantine resolution 一致、全部 active scope / account hold 已投影、无 active global barrier，并以 physicalExecutionId 幂等补投所有仍处于合法 replay deadline 的任务、恢复全部 result_ready fanout 后，才能 CAS 发布 `(accountRuntimeKey, revisions, rebuildEpoch, appliedOutboxSeq)` readiness。quarantined delivery 可以推进 ACK 但不能推进 health-resolved watermark；unknown tail、lost expected producer 或无法定位 affected set 时 deployment-wide capability readiness 保持 false。barrier 之后的 mutation 必须继续从统一 outbox 串行应用；任何旁表 mutation 缺 outbox 都使 rebuild 失败。execution_started_at 后结果未知且超出 provider 幂等窗口的执行必须收敛为 indeterminate，不能在重建时重发。
4. 发布后新 outbox 仍由同一串行 owner 应用；全局重建完成前，仅允许已发布账户服务，其他账户返回 runtime_state_rebuilding 并尝试后备候选。
5. 页数、单页时限、总时限、容量和 cursor 前进都有硬限制；失败必须丢弃 shadow epoch、释放 rebuilding lease 并允许下一轮重试。
6. 运行期 reconciler 按 ledgerRevision 和连续 outbox 水位修复 incident、due、intent、预算和 revision tombstone。
7. 可信缓存负向状态继续阻断；没有权威快照时遵循现有基础设施门禁，禁止在候选循环逐账户回源 PostgreSQL。

### 13.5 保留与清理

- 活动负向 incident、active intent、committed 但未投影的 positive observation、非终态 physical execution / member 和未完成 fanout 不按容量 TTL 删除。
- capability namespace 的 Asynq retry / dead task 硬重放 TTL 固定为 7 天；超过 TTL 只能导出诊断元数据并不可逆取消，不能人工重新投递旧 envelope。
- CLOSED tombstone / generation floor 至少保留 31 天，且删除前必须证明相关 intent、outbox、retry/dead task 全部终态或已被不可逆 fenced cancel。无法证明时保留紧凑 generation floor，不能仅按墙钟删除。
- 正向 evidence 24 小时过期后展示为 unknown；行可在 7 天无活动且无 active intent 后用 stateVersion 条件删除。
- positive observation 完整行至少保留 31 天；到期后也只有在该 observation 的 finalizer / handoff 已持久终态、producer replay deadline 已过、关联 retry / dead / in-flight 与审计 handoff 均有不可逆 ACK / cancel 证明、control outbox 已连续投影，且没有 active intent / fanout 引用时，才可删除或压缩。任何证明缺失时必须保留只含 observationId、scope / revision、committed version、producer proof / replay deadline、epoch 和 digest 的紧凑幂等 tombstone；该 tombstone 仍参与唯一冲突检查，重放同 ID 只能返回原 resolution，绝不能再次递增 version。tombstone 的最终删除还要满足同一组证明和最大重放窗口，不能只按 31 天墙钟。
- quarantine 行、artifact 和 active health hold 不按普通 spool TTL 删除。artifact 至少保留 31 天且不短于审计 / 备份恢复窗口；只有全部 hold 具备 fresh-evidence / revision / adjudication release proof、producer ACK 已越过、operation log 已落库且 artifact 校验可重现时才能压缩。unknown tail 未完成取证前禁止删除或恢复 producer ready。
- terminal physical execution / member 至少保留 7 天，并不得早于关联 recheck command、outbox、retry / dead task 和 generation floor 的证明窗口。开放 fanout 或 result_ready 永不按墙钟删除。
- 配置删除先推进 revision，再异步清理旧 Catalog / incident。迟到 outcome 不得因行不存在而盲 upsert。

## 14. 调度与多 Key 全链路

对请求模型 M：

1. 为候选账户解析当前 Route 描述。
2. 批量读取当前 Catalog、credential 目录、Key owner 运行态和所有已观察 model_capability incident；Key 运行态与 capability phase 是两条独立轴。
3. 对每个账户，只有至少一个 Attempt 同时通过 Key owner 与精确 capability 门禁时才保留账户。
4. SUSPECT 在 softAvoidUntil 内有健康候选时避让；全池只剩 soft avoid 时进入现有有界等待，等待边界仍是进程内而非伪称跨实例全局 FIFO。
5. 账户准备阶段从仍可执行的 Key 中选择一个，并形成不可变 Attempt 描述。
6. 真实失败在当前请求内按原规则切 Key / 账户 / 分组；生产探针旁路独立收集。
7. Key-1 + M 被阻断后，后续可选 Key-2 + M；同一账户其他模型按各自 scope 判断。

账户聚合必须区分三种结论：

- `capabilitySchedulable`：至少一个当前非删除 credential slot 对该 Route 同时满足 `evidenceDataStatus=ready`，且 capability 为 available / unknown，或 SUSPECT soft avoid 已过期；scope hold、fanout delivery unconfirmed 等任何 evidence unconfirmed Attempt 一律不计入。该值不消费 Key owner 状态。
- `effectiveSchedulable`：至少一个同一 Attempt 同时满足 evidence ready、Key owner 可执行和 capabilitySchedulable；真实 gateway Attempt 门禁与 Route 聚合都必须使用该交集，不能只在 API 摘要中隐藏 unconfirmed。
- `instance_all_capabilities_unavailable`：当前同一 universe revision 的全部 Route 都是 `routeAggregate=blocked`，即全部 Attempt 已确认阻断；它只表示能力层。全部 Route 暂时 capabilitySchedulable=false 但仍含 confirming / recovering / unknown 时，使用对应聚合与有界等待，不得冒充全部能力已确认不可用。
- `all_keys_unavailable`：由现有 Key owner 在所有 credential slot 不可执行时派生，不得伪装成模型能力 blocked。
- `instance_no_effective_route`：仍有 Key 可执行、也仍有某些 capability 可调度，但每个 Route 的 Key × capability 交集都为空。例如 Key-1 可执行但 B 被能力阻断，Key-2 的 B 未阻断但 Key-2 正在冷却。页面显示“凭据与模型能力组合当前无可调度路径”。
- `instance_no_routable_capability`：当前 ready Catalog 的 Route 数为 0，表示没有配置出可路由模型能力；它不是 unknown，也不能显示“可调度”。

上述 instance 结论都是 effectiveAvailability 的派生 blocker，不写 accounts.status，不启动全模型扫描，不交给旧哨兵冷却恢复器。零 Route 先于能力颜色处理；Key blocker 优先于纯 capability blocker，混合 blocker 只在前两者均不成立且交集为空时使用。

## 15. transport 父级规则修正

现有“10 分钟内三个不同 protocol_model scope 自动打开父 account circuit”必须删除。不同客户端别名、endpoint、lane 或同一个坏模型的多个子 scope 都不能证明整号 transport 死亡。

目标规则：

- protocol_model / model_capability 子 incident 永不以投票方式写父 account OPEN。
- all configured capabilities blocked 由 Catalog + 当前子状态派生门禁，不需要父 incident。
- 父 account transport circuit 只允许由真正账户全局、与模型无关且有专属独立证据的 owner 建立；目前没有满足该证明的自动子级升级路径，因此默认关闭。
- 父 account 恢复不能清理任何子模型、Key 或用户策略状态。
- transport scope 也必须使用 Attempt 描述中的真实上游模型和 endpoint，而不是客户端 requestModel 和固定哨兵 endpoint。

## 16. healthCheckModel、激活与周期检查

healthCheckModel + healthCheckEndpointMode 只表示 active 账户的周期哨兵 Route，不能再作为多模型账户激活的单一生死判据：

- pending_test 先生成 ready Catalog / credential baseline，再建立确定性、有界的 activation candidate queue。只纳入无需额外成本授权且 probeStrategy=execution 的当前 Attempt，优先配置的 healthCheckModel，随后按 Route / credential 稳定顺序轮转；单轮最多物化 8 项，但真实发送仍经过统一 durable admission、每物理账户 running=1 和自动 physical start 5 分钟门禁。失败只写该精确 capability unavailable 并让下一合法候选在后续物理窗口继续，不能把模型 B 的 opaque 失败记成整号激活失败。
- 任一当前 definition / binding 下的 activation Attempt 取得 complete_success，即可按时间计划把账户从 pending_test 进入 active 或 disabled；已失败的其他 Attempt 保留精确 blocked，之后各自恢复。Catalog 无 Route、只有 catalog_only / manual-costed / unsupported、或全部免费 execution 尚未成功时保持 pending_test，并显示 `activation_unconfirmed / owner_action_required`，不得因为 24 小时完整 HTTP 失败自动写整号 error。只有本地可证明且覆盖全部 Route / credential 的共用配置错误，才允许 account-global configuration owner 写 error；人工重新检查会重建 activation selection，不直接改状态。
- active 周期检查约每小时为哨兵精确 Route 产生一个 admission trigger。trigger 本身不直接调用上游；若 scope 已有 intent、处于 OPEN / HALF_OPEN / RECOVERING 且非当前 due、自动物理 5 分钟门禁未到或预算饱和，就以 covered / cooling / retry schedule 终结本轮哨兵计划，不产生第二次物理调用。真实成功 / 失败只更新该能力和哨兵监控，不再累计账户级失败后写 temporary_unavailable。
- 每个 `accountRuntimeKey / runtimeAccountId` 在自身运行账户行拥有独立哨兵成功水位，原子保存 `last_health_success_at + last_health_success_route_scope_id + last_health_success_dispatch_revision + last_health_success_universe_revision`；授权实例不得覆盖来源账户或其他实例的槽位。只有四项与当前解析出的哨兵 Route 和 revision 全部匹配，才允许跳过本轮主动检查；旧数据缺任一身份字段时按无有效水位处理。
- 只有同 normalized model + endpoint mode + lane + adapter route 的真实业务成功才能刷新本轮哨兵成功水位；Route 水位不绑定具体 Key，同 Route 的任一当前可执行 Key 成功均可刷新，但 Key 级 capability incident 仍按 Attempt scope 隔离。其他 Route 成功不能顺延哨兵。
- active capability intent 存在时，业务成功只写正向 observation 和哨兵水位，不由业务路径终结 generation；若成功发生在 worker claim 之后，之后到达的匹配负向 outcome 必须按 positive observation fence 终结为 superseded，不能覆盖较新成功，后续验证使用新 generation。成功发生在 claim 之前时已进入 claim fence，后续独立负向仍可正常提交。
- request_failure 不再调用哨兵探针。
- 纯图片哨兵的 catalog probe 只证明目录可见性；除非账户所有者为当次生产重检显式授权付费 E2E，否则不能声称图片 execution 可用或不可用，也不能单独完成账户激活。

切换必须受 `sentinel_watermark_contract_version` 门禁：账户候选查询、真实成功副作用、worker、列表 API 和统计 reader 同批改为完整四元组；仍只读取 `last_health_success_at` 的旧 reader 不得与新 writer 混跑。

## 17. 派生摘要与 effectiveAvailability

### 17.1 摘要字段

~~~text
CapabilityHealthSummary
  semanticsVersion = 2
  capabilityUniverseRevision
  dataStatus = ready | unconfirmed | rebuilding
  dataStatusReason = null | deployment_capability_barrier | observation_handoff_unconfirmed | projection_gap | capacity_exceeded | catalog_unconfirmed | credential_baseline_unconfirmed | rebuild_in_progress
  deploymentCapabilityBarrierRevision
  scopedUnconfirmedAttemptCount
  scopedUnconfirmedRouteCount
  hasScopedUnconfirmed
  aggregate
  routableRouteCount
  capabilitySchedulableRouteCount
  effectiveSchedulableRouteCount
  healthyRouteCount
  unknownRouteCount
  confirmingRouteCount
  partiallyUnavailableRouteCount
  blockedRouteCount
  recoveringRouteCount
  lastObservedAt
  generatedAt
~~~

每个 Route 只按当前 credential 目录和 Attempt capability phase 聚合唯一 `routeAggregate`；Key owner 状态不进入能力健康颜色：

1. active deployment-wide global barrier 存在时，gateway 与 API 必须按 current `deploymentCapabilityBarrierRevision` 在账户 publication 之外做 O(1) 最外层覆盖：每个账户摘要都强制 `dataStatus=unconfirmed`、`dataStatusReason=deployment_capability_barrier`，即使该账户本地 publication 原本 ready 也不得显示或参与调度。除此以外，只有 account-wide projection gap、Catalog / credential baseline 不完整、账户级 affected set 无法再细分或 rebuild 未完成时，账户摘要 dataStatus 才为 unconfirmed / rebuilding，并返回上述单一主 `dataStatusReason`；多个原因并存时优先级固定为 deployment_capability_barrier、observation_handoff_unconfirmed、projection_gap、capacity_exceeded、catalog_unconfirmed、credential_baseline_unconfirmed、rebuild_in_progress，详细并集只进管理员诊断指标。已知 scope 的 handoff health hold 不得提升为账户级 dataStatus。
2. 每个 Attempt 另有 `evidenceDataStatus=ready | unconfirmed` 与 `evidenceDataStatusReason`。已知 affected set 的 handoff hold 只把匹配 `scopeId + definition revisions` 标为 unconfirmed，并从精确调度候选排除；同 Route / 账户其他 ready Attempt 继续按自身 phase 调度。Route 只有 unconfirmed、没有 confirmed blocked 时聚合为 confirming；同时有 confirmed blocked 与 unconfirmed 时为 partially_unavailable。账户级 `scopedUnconfirmedAttemptCount / scopedUnconfirmedRouteCount` 只作局部提示，顶层 dataStatus 仍为 ready。
3. 当前 credential slot 数为 0：unknown；最终不可调度原因交给 Key owner，不得显示 capability healthy / blocked。
4. capabilitySchedulable=true，同时另有 OPEN / HALF_OPEN / RECOVERING：partially_unavailable。
5. capabilitySchedulable=false 且存在 HALF_OPEN / RECOVERING：recovering。
6. capabilitySchedulable=false 且全部 ready Attempt 都是 confirmed blocked、并且不存在 unconfirmed Attempt：blocked。
7. 同时存在 confirmed blocked 与 SUSPECT / unconfirmed、但此前未命中：partially_unavailable；这表示状态混合，不承诺 Route 当前可调度。
8. 存在 SUSPECT / unconfirmed、但没有 confirmed blocked / recovery：confirming。
9. 此前未命中但至少一个 Attempt 有未到期 `verification_backpressure_notice`：confirming；该 Attempt 的 capability phase 不变，notice 只影响软避让和展示。
10. 至少一个 ready Attempt 为 unknown、且此前未命中：unknown。
11. 当前 Attempt 均为 ready 且有新鲜正向事实：healthy。

上述 Route 计数互斥，所有计数之和等于 routableRouteCount；不能把一个 Route 同时算入 healthy 和 blocked。账户 aggregate 再按优先级固定：

1. account-wide dataStatus 不是 ready：不返回健康结论，由 runtime_state_unconfirmed / rebuilding 门禁展示；scope-local evidence unconfirmed 不命中此项。
2. dataStatus=ready 且 routeCount 为 0：no_routable_capability；Catalog 尚未 ready 已在前一项返回，不得伪装成零 Route。
3. capabilitySchedulableRouteCount 为 0，且存在 recovering：recovering。
4. blockedRouteCount 等于 routableRouteCount 且大于 0：all_configured_capabilities_blocked。
5. partiallyUnavailable / blocked / recovering 大于 0：partially_unavailable。
6. confirming 大于 0：confirming。
7. unknown 大于 0：no_confirmed_unavailability。
8. 全部 Route 都有新鲜正向事实：healthy。

unknown 允许调度，但绝不显示成 healthy；`evidenceDataStatus=unconfirmed` 则不允许该 Attempt 调度。all_configured_capabilities_blocked 只有在当前 Catalog 和 credential membership baseline 已完成权威加载、所有 Attempt evidence ready 且 confirmed blocked 后才能返回；Key owner 的冷却 / 禁用运行态不参与该能力结论。

`effectiveSchedulableRouteCount` 是管理列表 / 能力详情按当前 Key owner 状态实时求交的派生值，不写 v2 能力小时表。能力 aggregate 只描述 capability；账户卡片再按硬状态、instance_no_routable_capability、all_keys_unavailable、instance_all_capabilities_unavailable、instance_no_effective_route 的优先级形成最终 presentation，不能让 capability healthy 覆盖 Key 或混合阻断。纯 SUSPECT、零凭据 unknown、blocked + unknown 都不满足“全部 Route confirmed blocked”，因此不能落入 all_configured_capabilities_blocked。`partially_unavailable` 只描述状态混合，不保证存在可调度 Route；展示必须另看 effectiveSchedulableRouteCount。

### 17.2 页面统一状态

后端现有 effectiveAvailability / availabilityPresentation 增加 capability blocker。账户列表、能力详情和健康监控必须消费同一个纯映射 `CapabilityHealthPresentation`，不得各自硬编码颜色或文案：

| 条件 | 页面文案 | 语义色 | 调度含义 |
| --- | --- | --- | --- |
| `dataStatus=unconfirmed + dataStatusReason=deployment_capability_barrier` | 能力调度已全局暂停 | danger | deployment-wide 安全门禁；所有账户能力 Attempt fail-closed，等待取证或有审计裁决 |
| `dataStatus=unconfirmed` | 能力数据待确认 | neutral | 保守门禁，不按 unknown 放行 |
| `dataStatus=rebuilding` | 能力数据重建中 | info | 保守门禁，等待投影 ready |
| `dataStatus=ready + hasScopedUnconfirmed + effectiveSchedulableRouteCount > 0` | 可调度 · 部分能力证据待确认 | warning | 只过滤被 hold 的精确 Attempt，其他 Route / Key 继续调度 |
| `dataStatus=ready + hasScopedUnconfirmed + effectiveSchedulableRouteCount = 0` | 当前无可调度路径 · 能力证据待确认 | warning | 不扩大为整账户健康故障，但当前请求走后备 / 有界等待 |
| `active + no_routable_capability` | 未配置可路由模型能力 | danger | 当前无可执行 Route，不按 unknown 放行 |
| `active + healthy` | 可调度 · 能力可用 | success | 仍需通过 Key 与其他硬门禁 |
| `active + unknown / no_confirmed_unavailability` | 可调度 · 能力未确认 | neutral | unknown 可尝试，绝不显示绿色 |
| `active + confirming` | 能力确认中 | warning | soft avoid 内优先避让；没有替代时按有界等待规则处理 |
| `active + partially_unavailable + effectiveSchedulableRouteCount > 0` | 可调度 · 部分能力不可用 | warning | 仅保留通过精确门禁的 Attempt |
| `active + partially_unavailable + effectiveSchedulableRouteCount = 0` | 当前无可调度路径 · 部分能力待确认 | warning | 不冒充全部 confirmed blocked，按中间态有界等待 / 后备 |
| `active + recovering` | 能力恢复验证中 | info | 只由匹配恢复流程推进 |
| `active + all_keys_unavailable` | 全部 Key 不可用 | danger | 沿用 Key owner 恢复入口 |
| `active + all_configured_capabilities_blocked` | 当前无可调度模型能力 | danger | 不改 `accounts.status` |
| `active + instance_no_effective_route` | 凭据与模型能力组合当前无可调度路径 | danger | Key 与 capability 各自未全坏，但交集为空 |

`quality_isolated`、disabled、error、用户显式 temporary_unavailable 等硬状态优先，能力摘要只作补充。Attempt 的 `evidenceDataStatus=unconfirmed` 优先显示“能力证据待确认”并禁止调度；evidence ready 时再按 available=“能力可用”、unknown=“能力未确认”、suspect=“能力确认中”、temporarily_blocked=“能力已阻断”、half_open=“半开验证”、recovering=“能力恢复中”显示。unknown 使用 neutral，不能回落到 success。

## 18. 管理 API

### 18.1 账户列表

普通账户管理列表单次完整响应对当前页批量水合最新 capabilityHealthSummary，不重新引入 status-snapshot 或逐账户详情请求。AI 健康监控的 10 分钟排序 / 历史快照不能冻结该 current；它使用第 19 节 capabilityCurrentRef 批量刷新，并只在 invalidation 通道失效时做页面级 5 秒兜底，不是逐账户轮询。

### 18.2 能力明细

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health
GET /__aisys__/api/my-accounts/:accountId/capability-health
~~~

参数：

- limit 默认 50，范围 1 到 100。
- 首次响应返回短期签名 `capabilitySnapshotRef`。它绑定 accountRuntimeKey、capability view publication_id / view_version、effectiveDispatchRevision、capabilityUniverseRevision、control projection appliedOutboxSeq、Key owner snapshot revision、credential membership baseline、账户本地 dataStatus / dataStatusReason、权限视图和 10 分钟过期时间。第一页只选择 current ready publication，summary 与 items 读取同一 immutable as-of 版本，不在 GET 中临时复制全目录；从未有 ready publication 时返回 `503 projection_unavailable`。current `deploymentCapabilityBarrierRevision` 不冻结进该历史引用，每一页与每次 current hydration 都必须重新读取并覆盖 summary / presentation，使全局安全门禁不等待 10 分钟 snapshot 到期。
- Route cursor purpose 固定为 `capability_route`，绑定 capabilitySnapshotRef、规范化 state / model 过滤、limit 和最后排序键。后续页先验证当前权限，再按引用读取仍在保留期内的旧 publication；control / Key / membership / 账户本地 dataStatus 产生新 current publication 不影响旧引用翻页，但 active deployment barrier 始终以 current overlay 覆盖。引用 TTL 到期或保留版本已合法清理返回 `409 stale_view`，格式、签名、purpose、filter 或 limit 不匹配返回 400，跨账户 / 运行实例或权限已失效返回 404。
- 可选 state、model 过滤。
- 稳定排序：normalizedUpstreamModel、upstreamEndpointMode、adapterRouteKey、routeScopeId。

响应包含 generatedAt、两个 revision、nextCursor、summary 和 Route 粒度 items。Route item 至少包含：

- routeScopeId；只有物理账户所有者 / 管理员才返回签名并绑定 capabilitySnapshotRef 的 routeScopeRef
- upstreamModel、endpointMode、requestLane、adapterRouteKey
- probeStrategy、routeAggregate、capabilitySchedulable、effectiveSchedulable、effectiveBlocker
- 物理所有者 / 管理员可见 capability 状态的 credentialTotal / available / unknown / suspect / blocked / halfOpen / recovering 计数，另返回 `evidenceUnconfirmedCount / hasScopedUnconfirmed` 和正交的 keyEligible / keyBlocked 计数；phase 计数内部守恒，evidence 是正交轴，不能相加。授权实例只返回 credentialTopologyHidden=true、Route 能力聚合、hasScopedUnconfirmed、effectiveSchedulable 和通用 `credential / capability / evidence / mixed` blocker，不泄漏 Key 池规模或内部 hold 原因
- canView、canViewCredentials、canRecheck、canViewTrace

Route DTO 使用三套显式白名单：管理员可见上述全部字段以及 lastOutcome、lastObservedAt、positiveEvidenceExpiresAt、nextProbeAt / nextCostEligibleAt、requiresCostAuthorization；物理所有者可见相同运行诊断，但不能得到内部 audit ID、来源授权实例或原始 fingerprint；授权实例只返回自身可见 Route 描述、probeStrategy、routeAggregate、capabilitySchedulable、effectiveSchedulable、hasScopedUnconfirmed、通用 blocker、credentialTopologyHidden 和权限布尔值，不返回凭据计数、精确 evidence 原因、lastOutcome、精确活动时间、evidence expiry、nextProbeAt、routeScopeRef、scopeRef 或 traceRef。序列化器不得先构造完整 DTO 再靠前端隐藏。

Route item 对物理所有者 / 管理员另返回 `verificationPendingCapacityCount`；授权实例只得到通用 confirming / blocker，不得到容量或凭据拓扑。Route item 不返回任意挑选的单个 scopeId。展开某个 Route 时调用：

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health/:routeScopeRef/credentials
GET /__aisys__/api/my-accounts/:accountId/capability-health/:routeScopeRef/credentials
~~~

该接口只允许物理账户所有者或管理员调用；授权实例使用者统一返回 404。Attempt cursor purpose 固定为 `capability_attempt`，并绑定 routeScopeRef、capabilitySnapshotRef、limit、当前权限视图和最后 scopeId；它与 Route 页读取同一保留的 view publication，current pointer 后续变化不使 cursor 失效。它按 scopeId 稳定排序，返回每个 Attempt 的 scopeId、scopeRef、脱敏 credential label、state、`evidenceDataStatus / evidenceReason`、`verificationPendingCapacity / retryAfter`、schedule、nextCostEligibleAt、requiresCostAuthorization、canRecheck、canViewTrace，以及有权限时的 lastTraceRef。notice 显示“等待检查资源”或“待成本授权验证”，不改写 state 为 blocked；unknown Route 从该 publication 绑定的 Catalog 与 credential baseline 合成，不要求已有 incident。原始 fingerprint、来源账户名和其他授权实例信息不返回。

非法 cursor / limit / scopeRef 返回 400；同一可见资源 revision 已变化返回 409 stale_view；跨账户、跨 Route、跨运行实例或资源不可见统一返回 404。

### 18.3 生产重新检查

~~~text
POST /__aisys__/api/accounts/:accountId/capability-health/:scopeRef/recheck
POST /__aisys__/api/my-accounts/:accountId/capability-health/:scopeRef/recheck
POST /__aisys__/api/accounts/:accountId/capability-health/recheck
POST /__aisys__/api/my-accounts/:accountId/capability-health/recheck
~~~

- scopeRef 必须由当前凭据明细返回，可以对应 Catalog 中尚无 incident 的合法精确凭据 scope；后端验签并核对 runtime identity、capabilitySnapshotRef、effectiveDispatchRevision 和 capabilityUniverseRevision，不匹配返回 409 stale_view。接口不接受任意 model / endpoint 文本。
- 单 scope pending_admission / accepted 返回 202、taskId 和 Location；pending_admission 的 generation 固定为 null，accepted 返回当前 generation。
- already_running item 关联既有 intent，但 HTTP 始终返回本次新建或由 Idempotency-Key 重放的 durable command taskId 和 202；不得把另一 actor / command 的 taskId 泄漏给调用方。
- 预算饱和返回 429 与 Retry-After。
- unsupported 或需要成本授权返回 409。
- 单 scope 与批量命令都要求 `Idempotency-Key`。规范化 request digest 覆盖方法、路径、去重后按 scopeId 稳定排序的 scopeRefs、selectionRef、两个 expected revision 和成本授权字段。处理顺序固定为：先认证当前 session 并确认 actor 对该 runtime account **当前仍有 canRecheck 权限**，失败统一 404；再按 actor + runtime account + key 查 10 分钟幂等指针。同 digest 即使当前 revision 已变化也可返回原命令资源和原状态，不同 digest 返回 `409 idempotency_conflict`；只有没有指针时才校验 scopeRef / selectionRef、snapshot 和当前 revision 并创建命令。幂等只跳过重复 admission，绝不跳过当前鉴权或泄漏旧 Location / task body。
- manual_costed_execution 只允许物理账户所有者发起单 scope 命令，并要求 body `{ authorizeCostedExecution: true, maxEstimatedCostUsd }`；管理员不是物理 owner 时提交成本授权返回 `403 cost_authorization_forbidden`。缺失授权、估算超限或日预算不足返回 409 / 429，均不创建 intent。批量命令不得隐式替用户授权成本。授权在 `cost_execution_started_at` 写入前到期时 item 终结为 `cost_authorization_expired`，不得执行或自动续期；再次执行必须由所有者用新命令授权。
- 批量命令 body 固定为 `{ expectedEffectiveDispatchRevision, expectedCapabilityUniverseRevision, scopeRefs?: string[], selectionRef?: string }`，scopeRefs 与 selectionRef 互斥。省略二者时，只从 source view publication 中 `automatic_recheck_eligible=true`、probeStrategy 为 execution / catalog_only 且当前 suspect / blocked / halfOpen / recovering 的 scope 建立 lazy selection header；manual_costed_execution、unsupported、无当前重检权限和成本授权必需项全部排除，不能因 `nextProbeAt=null` 占据队首。候选按 `nextProbeAt NULLS FIRST、phase priority、Route 稳定排序、scopeId` 逻辑冻结，phase priority 固定为 `halfOpen=0、blocked=1、recovering=2、suspect=3`，不得依赖数据库枚举字面排序。每个命令只从 immutable source as-of view 事务领取并物化最多 100 个 item，响应返回 selectedCount、精确 remainingCount 和 10 分钟 nextSelectionRef；nextSelectionRef 绑定 selectionId + expected cursorVersion，成功领取可续 idle TTL、绝对上限 24 小时。新变为 due 的 scope 不插入旧 selection，命令自身 outbox 也不使 continuation 失效；不同 key 并发消费同一旧 ref 只有一个 CAS winner，另一个返回 409。双 revision 变化返回 409；冻结候选在执行前已恢复、被占用或失去资格时仍消费该位置并逐 item 安全终结，不用后排项回填。显式 scopeRefs 最多 100 个，验签后按 scopeId 去重并稳定排序；重复项只处理一次，仍不得批量隐式授权付费。
- 命令及全部 item 先持久化，再由有界同步 dispatcher 和后台 reconciler 幂等处理 lifecycle_status=pending_admission。dispatcher / reconciler 在每次 admission 前必须重新校验当前 actor 权限、账户归属、已解析 scope 身份、双 revision 和成本授权时效；不重新验外部 ref TTL 或 source view 是否仍 current。权限撤销或资源变化使 item 以 terminal(forbidden / stale) 终结。worker 在真实派发前再校验当前 runtime binding 与凭据权限。逐 scope 返回 lifecycleStatus 与 disposition；至少一个 pending / active item 时返回 202，全部 terminal(saturated) 返回 429，全部 terminal(unsupported / cost_authorization_required) 返回 409，混合终态返回命令资源中的逐项结果。所有有效命令响应都返回同一 taskId；202 额外返回 Location。command 已持久化后即使全部 saturated 也消费本次幂等键，同 key 在当前鉴权通过后重放原 429；调用方只能在 Retry-After 后使用新 key 再次 admission。部分接纳不得回滚已 accepted intent，崩溃恢复也不得重复成本授权。
- 命令只投递受控探针，不直接改成 available。

生产重检会影响调度，必须写操作日志；能力详情中的命令固定命名“重新检查此能力”。纯人工“测试”和 pending_test 的账户“重新检查”继续使用各自既有文案与接口，三者不可混用。

命令状态和 trace 使用独立资源：

~~~text
GET /__aisys__/api/accounts/:accountId/capability-health/recheck-tasks/:taskId
GET /__aisys__/api/my-accounts/:accountId/capability-health/recheck-tasks/:taskId
GET /__aisys__/api/accounts/:accountId/capability-health/traces/:traceRef
GET /__aisys__/api/my-accounts/:accountId/capability-health/traces/:traceRef
~~~

- task 资源直读 durable recheck command，返回 pending_admission / running / terminal、单 scope 或逐 scope 结果、acceptedCount、terminalCount、创建 / 完成时间和有界错误码；终态保留 24 小时，过期后 404。Location 必须指向该资源，前端按 Retry-After 轮询并在 terminal 停止。
- traceRef 是绑定 runtime identity、scopeId 和权限的短期签名引用；仅 canViewTrace=true 时返回。trace 接口对管理员返回脱敏阶段、outcome、时间、revision 和关联审计 ID；物理所有者不返回内部审计 ID；授权实例没有 traceRef。所有视图都不返回 Key、token、prompt、附件或上游正文。

前端交互固定为：Route 行只负责展开，只有 `canViewCredentials=true` 才显示展开入口；Attempt 行才显示“重新检查此能力”和 trace。点击重检时生成一次 Idempotency-Key，并在同一用户动作及网络重试中复用，终态或用户明确重新发起后才生成新 key。202 后禁用重复提交并按 Retry-After 轮询；批量任务逐项显示 pending / accepted / already running / rejected / terminal。409 stale_view 关闭旧抽屉并刷新，idempotency_conflict 要求重新发起，cost_authorization_required 只向物理 owner 展示一次性成本确认，429 保留当前结果并倒计时，terminal 资源过期 404 后停止轮询并提示刷新状态。授权实例从 UI 层隐藏展开、重检和 trace，而不是依赖点击后的 404。

### 18.4 权限

- 管理路径仅 super_admin / admin。
- my-* 强制使用当前 session，不接受前端 systemAccountId。
- 物理账户所有者可查看、重检并查看自己的 trace。
- 授权实例使用者只能查看自身运行实例的 Route 聚合，不能访问 credential endpoint、scopeRef、凭据计数或 trace，也不能消耗来源账户凭据发起生产重检。
- 不可见资源返回 404；权限摘要固定为 canView / canViewCredentials / canRecheck / canViewTrace。

## 19. AI 健康监控

旧 account_health_hourly 继续表示哨兵账户观察，语义版本为 1。新能力监控使用版本 2，不能把旧小时记录倒推成模型能力事实。

v2 schema、原子发布、权限和分页以 [AI 健康监控设计](AI健康监控设计.md) 为唯一明细契约，主结构固定为：

- `account_capability_health_events`：Attempt 有界事件，按同一运行账户固定分区的 `stats_partition_id + partition_seq` 连续消费，默认保留 7 天。
- `account_capability_route_state_segments`：Route 聚合的稀疏状态有效区间，不物化 Route × 744 小时全量快照。
- `account_capability_health_hourly`：只有 observation / transition / baseline 变化时才写的 Route 稀疏小时事实。
- `account_capability_health_hourly_summary`：按账户、统计小时和双 revision 稠密保存六类互斥 Route 计数、capabilitySchedulableRouteCount、dataStatus 和 aggregate。
- `account_capability_health_publications`：staging delta 的事务发布清单和短期不可变读取快照；业务行保留 snapshot-validity 区间，使繁忙当前小时的 hourRef / cursor 在 TTL 内稳定翻页。

ingest owner 写事件，stats owner 按固定 account partition 连续增量聚合；管理 API 只读已发布投影，不扫描 usage、完整 ledger 或运行日志临时 GROUP BY。每小时列表最多 744 个槽，同小时多 revision 通过签名 revisionsRef 和独立 revisionCursor 访问；Route / event 明细必须携带 hourRef，并分别使用独立 cursor。v2 历史只描述 capability，不把 Key owner 冷却写进能力颜色。

`accountListSnapshotRef` 只冻结账户排序、v1 哨兵版本和 v2 小时历史，不冻结 `capability.current`。每一页按 runtime identity 批量水合最新 control view；可见行再通过最多 50 个 `capabilityCurrentRef` 的批量端点消费 control invalidation，失去 invalidation 通道时最多每 5 秒轮询一次。最新 current 未 ready 返回 unconfirmed，不允许旧快照继续显示十分钟“可调度”。`listStale` 只提示排序 / 历史 publication 延迟，不能覆盖实时 current。

小时条同时展示：

- 原哨兵 success / failure / unknown。
- v2 能力摘要；部分阻断显示“部分能力不可用”，未知显示灰色。

展开按模型、endpoint 和转换路径分页读取 v2 明细。能力切换日前只显示 v1；切换日之后若 v2 聚合滞后，显示“能力数据待聚合”，不得回退伪造全绿。publication 更新不能立即使仍在 TTL 内的 hourRef 失效；快照到期、revision 不再保留或权限变化时才按契约返回 stale / 404。

## 20. 可观测性

日志事件至少包含：

- capability_candidate_collected
- capability_candidate_canceled_by_same_scope_success
- capability_handoff_spooled / committed / replayed / terminal / digest_conflict
- capability_handoff_retry_scheduled / quarantined / hold_released / unknown_tail
- capability_handoff_backlog_unconfirmed / gateway_drained
- capability_probe_admission_accepted / running / cooling / saturated / unsupported
- capability_probe_started / completed / task_failed / stale
- capability_state_transition
- capability_projection_gap / rebuilt / reconciled
- capability_scope_filtered

指标至少包含：

- admission accepted、deduped、cooling、saturated
- capability handoff append latency / failure、unacked count / bytes / oldest age、replay throughput / retry / deadline exceeded、producer sequence gap、receipt pending / terminal、quarantine / active hold / unknown tail 和 gateway readyz drain
- 每 scope 5 分钟重复抑制数
- 每逻辑实例、物理账户和全局 pending / running
- probe queue wait、执行时长和四类稳定 outcome
- phase 数量、OPEN 年龄、恢复成功 / 失败
- outbox lag、Asynq retry / dead、projection gap、rebuild / lazy-load
- 全部能力派生阻断账户数

模型 ID 和 endpoint 可以记录；凭据只记录不可逆内部 scopeId。禁止记录密钥、token、用户 prompt、附件和上游正文。

## 21. 配置与生命周期

- 本节的 capability / dispatch revision 与管理 PATCH 的 `configRevision` 分开：任何真实管理业务变化都推进 `configRevision`；名称、备注、仅展示优先级变化不推进能力 revision，但不能因此被理解为不推进配置版本。
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

### 22.1 前置门禁

本方案不能插入当前仅有 Go seam 的生产形态。启用前必须同时完成：

- W7 全量 Go worker 接管：ingest / stats / ops、账户健康、冷却、Key、测试和模型检测都已有真实 PostgreSQL / Redis / Asynq owner，`routeOwners.worker=go`，Node worker 与 IPC 已 drain；不能按单个任务伪造 worker 分拆 owner。
- W8 PostgreSQL / Redis / Asynq 存储收尾：生产不再依赖 SQLite、DB service 或仅进程内 retry queue 作为可恢复事实。
- W10 Go gateway production listener：完整候选循环、协议 finalizer、usage / audit durable handoff、circuit side-effect、真实 upstream、drain、单 owner canary 和 rollback 全部有证据。前置条件只要求 Node gateway writer 已可禁用并排空，不要求删除源码；Node 主链路与冻结的 rollback artifact 保留到 W11。
- capability 管理 API 的精确 route owner、Go control / Asynq / stats 进程和 macOS / 高性能 supervisor 配置已完成。当前仅有 listener seam、部分 worker CLI 或未接真实 Probe / outcome adapter 时一律禁止切换。

### 22.2 Contract epoch 与 owner 锁

`deploy/owner-manifest` 必须先升级为 schemaVersion 3，再增加不可变 `contractVersions`。schemaVersion 2 validator 当前拒绝未知字段，因此切换顺序固定为：先在 version 1 active epoch 发布可同时严格读取 v2 / v3、但只写 v2 的 dual-read validator；全部旧 validator 进程 drain 后，再提交 v3 manifest。禁止直接把 `contractVersions` 字段交给旧二进制，也禁止同时接受 `contracts.*` 别名造成签名内容与运行语义分叉。

~~~text
schemaVersion = 3
deploymentEpoch
contractVersions.capability = { version: 2, minReader: 2, minWriter: 2 }
contractVersions.sentinelWatermark = { version: 2, minReader: 2, minWriter: 2 }
contractVersions.stats = { version: 2, minReader: 2, minWriter: 2 }
contractVersions.capabilityHandoff = {
  version: 2,
  minReader: 2,
  minWriter: 2,
  spoolRecordVersion: 1,
  checksumVersion: 1,
  receiptSchemaVersion: 1,
  quarantineSchemaVersion: 1,
  healthHoldSchemaVersion: 1,
  deliveryResolutionVersion: 1,
  ackCursorVersion: 1,
  producerRegistrySchemaVersion: 1,
  producerWatermarkSchemaVersion: 1,
  producerInventoryCertificateSchemaVersion: 1,
  replayLeaseFencingVersion: 1,
  accountPhysicalProbeGateSchemaVersion: 1,
  probeDueSchemaVersion: 1,
  activationSelectionSchemaVersion: 1,
  accountHoldSchemaVersion: 1,
  globalBarrierSchemaVersion: 1,
  tailQuarantineFormatVersion: 1,
  quarantineArtifactFormatVersion: 1,
  evidenceArtifactManifestVersion: 1,
  backupBarrierSchemaVersion: 1,
  backupEvidenceVersion: 1,
  producerEpochPolicyVersion: 1,
  replicationReceiptSchemaVersion: 1,
  artifactArchiveManifestVersion: 1,
  canonicalManifestEnvelopeVersion: 1,
  maxReplayRecordVersion: 1
}
targetDeploymentEpoch + targetEpochMode + release + routeOwners + rollbackRouteOwners
roleTopology + sourceProducerInventoryCertificateDigest + targetProducerInventoryCertificateDigest
sourceProducerSetDigest + targetProducerSetDigest + gooseCatalogDigest + capabilityCohort
manifestDigest（保存在签名 envelope / 数据库 epoch 行，不作为被摘要 JSON 的自引用字段）
signatureEnvelope（位于被摘要 manifest 外，包含 purpose、canonicalization=RFC8785、digestAlgorithm=SHA-256、signatureAlgorithm=Ed25519、manifestDigest、keyId、trustRootId、environmentId、deploymentEpoch、barrierId、releaseDigest、issuedAt、expiresAt、sequence、signature）
~~~

PostgreSQL 新增 append-only `deployment_contract_epochs` 和单行 active pointer，并增加 `deployment_activation_barriers` / role registrations。epoch 保存 manifest digest、四个 contract version / reader / writer 下限、handoff 子契约版本、owner、source / target producer inventory certificate digest、source / target producer set digest、`prepared | active | retired`、previous epoch 和时间；它不保存会在普通备份、扩缩容或 producer 重启时变化的 current inventory revision。activation barrier 单调经过 `ingress_frozen -> mutation_fenced -> source_drained -> epoch_active -> roles_ready -> proxy_switched | rollback_required`。legacy accountId dispatcher / 单字段水位固定为 version 1，本文精确能力 / 四元水位 / stats / capability handoff 固定为 version 2。每个二进制声明支持范围以及显式 `targetDeploymentEpoch` / `targetEpochMode=active|prepared`：对外 listener 和任何生产 writer 只能绑定 active；prepared 进程只能使用隔离 namespace 做只读 smoke、shadow rebuild 和无副作用校验，数据库拒绝其业务 mutation。gateway、每 host 独立 capability-handoff-replay / quarantine owner、control、worker、stats、API reader 在监听 / 消费前同时校验本地 manifest、目标 PostgreSQL epoch、Goose schema、当前 producer inventory certificate 和 release，任一不符即 startup / readyz fail-closed。Redis namespace、Asynq task envelope、outbox、lease、spool record 和 receipt 都携带 epoch，旧 epoch writer 的每次 mutation 都被数据库 CAS 拒绝。

运行期 producer 集合使用独立 append-only `deployment_producer_inventory_certificates` 和每 epoch 单行 active pointer。certificate 固定 `deploymentEpoch / inventoryRevision / previousCertificateDigest / ownerManifestDigest / producerSetDigest / producerEntriesDigest / expectedHostSetDigest / replicationPolicyDigest / reason / issuedAt`，以 RFC 8785 + SHA-256 + Ed25519 envelope 签名；revision 必须连续递增，previous digest 必须指向同 epoch 前一张证书，第一张必须等于 manifest 的 `targetProducerInventoryCertificateDigest`。producer 先以 `active_target_pending` 注册并 fsync metadata / initial sequence，coordinator 在 deployment lock 下同时校验证书链、registry set digest、role proof 和 replication policy，再 CAS active certificate pointer 与对应 producer 状态；旧证书仍可审计但不能授权 append。备份恢复、扩缩容、producer replacement / restart 都生成新 certificate，不修改 owner manifest 或 epoch 行，也不允许 reopen sealed producer epoch。barrier 固定的是 `inventoryRevision + certificateDigest`；冻结后任何集合增删或 pointer 变化都使 certificate 失败，不能只比较可重复的整数 revision。

`sourceProducerSetDigest` / `targetProducerSetDigest` 覆盖所有能够 append handoff record、发布 failure mutation source 或向旧健康队列 enqueue 的 producer identity、host / instance、contract、epoch 和 expected membership；v2 handoff producer 还必须引用 `account_capability_handoff_producers` 的精确 registry row / metadata digest。source digest 因而包含 legacy failure writer / queue producer 的 seal 身份，target digest 包含新 gateway spool producer 的 prepared 身份；对应 source / target inventory certificate digest 把集合绑定到不可变 manifest 与连续证书链。replay、control / scheduler / worker 等 consumer / owner 不伪装成可 seal producer，而由 `roleTopology`、role registration 与 drain / ready proof 单独约束；roleTopology、两个 set digest、两个 certificate digest 或 barrier 冻结的 certificate pointer 任一不一致都使 activation barrier 不可签发。

manifest、producer inventory certificate 与 backup / rollback evidence 的签名输入统一为 RFC 8785 canonical JSON bytes：被摘要 payload 明确排除自身 digest 和整个 `signatureEnvelope`，先以 SHA-256 得到内容 digest；再对 envelope 中除 `signature` 之外的全部字段做 RFC 8785 canonicalization，并由 Ed25519 签名。`purpose` 固定为 `owner_activation | producer_inventory | runtime_authorization | backup_evidence | rollback_evidence | restore_authorization` 之一，validator 必须校验 purpose、环境、deployment epoch、barrier、release、单调 sequence、有效期、当前或轮换重叠期 trust root，并拒绝已撤销 key、旧 sequence 重放、跨 purpose / 环境 / barrier 复用和 envelope 内外字段不一致。activation 使用短期 expiresAt；其过期后 manifest 内容仍可审计，但任何进程启动、重启或重新 ready 都必须取得新鲜 `runtime_authorization`，该 envelope 绑定 manifestDigest、当前 active producer inventory certificate digest、releaseDigest、role identity / shard、environment 和 deployment epoch，不能重用过期 activation envelope。长期 backup / rollback evidence 到期后仍可作为不可变内容证明，但任何 restore / rollback 执行都必须取得引用其 manifestDigest 的新鲜 `restore_authorization` envelope，不能绕过当前信任根与撤销策略。key rotation / revocation 是 deployment control 数据，私钥不得进入 manifest、证书、备份 artifact 或运行日志；无法取得可信验证链或新鲜执行授权时 startup、activation、backup publish、restore 和 rollback 一律 fail-closed。

epoch 行创建后不可修改。部署协调器持有 PostgreSQL 全局 advisory lock，只能把完整 prepared epoch CAS 为 active；owner manifest 文件、反向代理配置和 supervisor 使用预先校验的同一 digest。prepared 阶段必须冻结旧 active epoch 当前 certificate，令其 producer 集合等于 `sourceProducerSetDigest` / `sourceProducerInventoryCertificateDigest`；同时让目标 gateway host 创建并 fsync 全新的 producer metadata / initial sequence、注册为 `active_target_pending`，取得 target replay lease 与 role proof，并签发与 manifest 完全匹配的首张 `targetProducerInventoryCertificateDigest` / `targetProducerSetDigest`。target 此时必须是零生产流量、零 delivery、零 capability side-effect，prepared ready 不能代替 active-mode ready。最终激活在短暂流量冻结窗口内执行：原子 fence 新 ingress、command、due claim、admission 和 probe scheduling，允许已接受请求的 finalizer、outbox / projector、fanout 与 source replay 收尾；source set 全部 sealed / drained 且水位闭合后记录 source_drained，再 CAS 激活数据库 epoch及 target inventory pointer，使旧 writer 被 fence，并把证书中完全匹配的 target set 从 `active_target_pending` 转 active。目标进程逐角色注册 manifest digest、active epoch、rebuild / outbox barrier、target inventory certificate / replay lease、新鲜 runtime authorization 和 active-mode ready proof；只有 gateway、replay、control、Asynq、stats、API 所有期望角色达到 roles_ready，才原子替换代理配置并记录 proxy_switched。角色未齐、source / target set 或 certificate digest、frozen pointer、证书链或 role authorization 任一漂移时继续保持公网流量冻结并创建新 rollback epoch，禁止把代理切给半启动 owner，也禁止重新激活旧 epoch。

运行期锁域固定为：gateway 可以多实例但只接受 active epoch；每个 gateway host 另运行独立 `capability-handoff-replay` 服务，按 producer instance epoch / segment lease 单 owner 重放并负责本机 quarantine artifact，不接受业务流量，gateway ingress 停止后仍存活到 delivered / ACK drain 完成；control projector / reconciler 和 due scheduler 按 epoch + shard 持租约；Asynq consumer 可多实例但必须依赖 physicalExecutionId TaskID、physical claim 和逐 member logical fencing，不另设伪全局锁；stats owner 按 event partition / cursor 持租约；部署 / schema 迁移只使用全局 deployment advisory lock。单机可复用同一 Go binary 的不同 role，但禁止把 replay 做成随 gateway 连接 drain 一起退出的 goroutine。Docker、Linux systemd、Windows service、macOS launchd 和高性能 supervisor 都必须声明 gateway、每 host replay / quarantine、control、Asynq、stats 的进程数、volume / lease 域、健康检查、停止顺序、目标 epoch 和这些锁域；未提供对应 runbook 的平台不得启用 capability v2。

### 22.3 完整切换顺序

1. 冻结可回滚的 Node / Go 二进制、manifest before-image、代理配置和 schema catalog；备份 PostgreSQL，并导出现有 circuit / health、所有 retry / dead / in-flight 任务、预算与 owner 清单。回滚 Node artifact 必须已经支持 epoch / contract fail-closed 门禁，不能拿无法识别新门禁的旧任意二进制冒险回切。
2. 在入口仍开放时只执行演练过的 **expand-only online migration**：新增表、索引、可空列和兼容默认值，并先部署 dual-read / dual-write 或 backfill owner；不得在这一阶段执行 Goose Down、删除 / 重命名列、收紧约束、切换唯一 owner contract，或采用会超过签名 lock-time budget 的 DDL。`juhe_business` 创建 epoch、ledger / durable due / logical intent / positive observation、handoff parent / candidate / delivery receipt、producer registry / watermark / replay lease、record / tail quarantine、scope / account hold / global barrier、physical execution / member / account physical gate、Catalog、activation / recheck selection、command、control / stats outbox 和 legacy hold；`juhe_stats` 创建 event、time due、hour-close cursor、Route event projection、segment、账户 publication / member / pointer、versioned list row / list publication 和 hourly schema。Node / Go 均对目标 `public.goose_db_version` 与 capabilityHandoff 全部子契约完成 preflight；需要 destructive / tightening / contract switch 的 migration 必须延迟到第 8 步同时持有 `ingress_frozen + mutation_fence`、旧 owner / finalizer 已 drain 且 rollback compat 证明齐备后执行。schema 不兼容时不得继续。

   capability v2 的上线 / 灾备备份必须由 coordinator 建立可恢复 barrier：同一事务写 `ingress_frozen + mutation_fence`，冻结当前 producer inventory certificate revision / digest，并 fence **新的** ingress、recheck command、due claim、admission 和 probe scheduling；已经被 barrier 前接受的请求只能完成 finalizer / durable handoff，已经 claim 的任务只能收敛到 terminal / indeterminate，outbox / projector、fanout 和 replay 必须继续到连续水位闭合。barrier 期间新 failure nomination 允许持久终结为 `barrier_deferred`，但不得创建 logical / physical execution；恢复后只能基于当时 current revision 生成新鲜 verification due，不能重放旧业务失败。active request / finalizer / claimed execution / fanout 均归零后，才把该 certificate 的**当前 active producer set** 转 draining -> sealed / drained，要求 `control_resolved_through = producer_ack_through = finalSequence`、unacked=0、无 unknown tail / lost producer / global barrier，再创建数据库内 immutable backup barrier 行并捕获 snapshot LSN。此时没有只存在本地 spool 的 pending record，故常规备份不接受“归档一部分未 ACK segment”作为替代。

   单个逻辑 snapshot 覆盖 `public.goose_db_version + juhe_business + juhe_stats`，compat 存在时同 snapshot 覆盖 `juhe_rollback_compat`；同时保存 owner manifest / deployment epoch before-image、任务 / 预算清单、Asynq retry/dead/in-flight、sealed active producer set / watermarks、replica receipts、quarantine / active-hold artifact。coordinator 必须先把每个 artifact 的**实际 bytes**复制到备份故障域内的内容寻址 immutable archive，再逐对象读回校验 media type、schema version、byte length、SHA-256 和引用闭合；仅保存本机 path、artifact 列表或 digest 不能算备份。根 manifest 固定 `activeProducerSetDigest`、`producerInventoryCertificateRevision`、`producerInventoryCertificateDigest`、`replicaSetDigest`、`policyDigest`、`barrierOutboxSeq`、`snapshotLsn`、`physicalBackupProofDigest`、object digests 和签名 envelope；location remap 也必须是同一签名证据的一部分。外部对象、逻辑 dump 和数据库 barrier 对账后才发布备份；Redis 仍是可重建派生数据，不作为唯一备份。

   所有 active capability v2 生产部署都必须另有 PostgreSQL physical base backup / managed snapshot + 连续 WAL archive，不能把它降级为 compat 存在时才启用，也不能靠一次 `pg_dump` 承诺灾备 RPO。physical proof 至少固定 `systemIdentifier`、`timeline`、`baseBackupId`、`baseBackupStartLsn`、`baseBackupStopLsn`、`restorePointLsn`、`walStartLsn`、`walEndLsn`、`walObjectDigests` 与保留证明，并由隔离恢复验证可 PITR 到不早于 `snapshotLsn` / backup barrier LSN。host-local capability spool 只承诺进程 / release 崩溃恢复：主机或磁盘永久丢失时若没有第二故障域副本，coordinator 必须把 producer 标为 lost 并维持 global barrier，不能宣称事件可恢复。要求 RPO=0 的部署必须验证 `replicaAckThrough=finalSequence`、replica set / failure-domain / fencing token 与内容 digest；异步镜像必须声明最大 RPO，不能伪装成零丢失。

   备份发布后不能重新打开已 sealed 的 producer epoch。coordinator 让每个 gateway 创建并 fsync 新 producer epoch、以 `active_target_pending` 注册并取得 replay lease / role-ready proof，随后签发 previous digest 指向已冻结证书、revision 连续递增的新 producer inventory certificate；只有 registry set / replication policy / certificate signature 全部匹配并原子 CAS active pointer 后，才把新 set 转 active 并解除 mutation fence 与 ingress fence。任一新 epoch、证书链、replica policy、role 或签名验证失败都保持流量冻结并进入有审计的恢复 / rollback，禁止复用旧 finalSequence 继续 append。
3. 建立 version 1 的 pre-cutover epoch，先部署带门禁的 legacy-compatible owner；确认行为不变后，再把 version 2 Go gateway / control / Asynq / stats 作为 prepared epoch 暗启，只运行只读 smoke、隔离 namespace 验证和 shadow rebuild，不接生产 side-effect。
4. 在入口仍开放时只准备 Node request_failure / 子 scope 父级升级 writer 的 seal 清单、reason-aware export / cancel 和共享健康队列重建证明，不得先停止 producer。当前 `createRetryQueue` 不能按 reason 可靠选择性删除：只有先实现 reason-aware durable export / cancel，才允许在后续 ingress-frozen barrier 内单独取消 request_failure；否则必须在同一 barrier 等待全部 in-flight 结束、停止整个队列，证明 activation / configuration / scheduled 等合法任务均能从 PostgreSQL 权威 due 状态重建，再丢弃进程队列并由 Go owner 重建。禁止在流量仍可产生新失败时提前形成 owner 零写窗口。
5. 在入口仍开放时只验证 legacy producer / consumer / queue 的**完整 inventory**、source set digest、seal / drain 命令演练结果、reason-aware export / rebuild golden evidence 和迟到 mutation fencing fixture；此时不得要求在途请求、producer、consumer 或队列计数为零，也不得真正停写制造 owner gap。为所有账户计算 capabilityUniverseRevision / Catalog，执行 Node / Go scope codec golden vectors。version 1 mutation 的真实归零与迟到写 CAS 拒绝只能在第 8 步 barrier 内取证。
6. 迁移旧 circuit 与账户级运行态。证据优先级固定为：带 actor / policy revision 的用户显式策略 -> 注册的 dedicated account-global owner provenance -> request_failure task / childIncidentIds / escalation transition 的完整链路 -> 语义明确的绝对 cooldown -> unknown；字段互相冲突、链路缺项或只剩 last_error_code 时一律按 unknown。只有完整链路证明由子 scope 投票或 request_failure 生成，才允许保留可证明的精确子 incident并以 generation / ledgerRevision CAS 关闭父层、写 outbox tombstone。旧账户级 `recovery_wait / runtime_degraded / precheck_pending / precheck_failed` 若能证明由请求派生则关闭或不重建；旧数据无法还原真实模型、endpoint 和 Key 时不得伪造新的精确子 scope。

   unknown 项写入 `legacy_account_health_holds(deployment_epoch, account_id, source_kind, source_record_id, evidence_kind, evidence_digest, before_status, before_config_revision, before_ledger_revision, decision, state, approved_by, approved_at, released_by, released_at, operation_log_id)`，以 source identity 条件唯一。deployment coordinator 是唯一 writer，调度读取 active hold 作为保守硬门禁，通用探针不认领。释放只允许语义明确的绝对 cooldown 由 compatibility owner 到期，或双人审批的逐 ID manifest + before-image CAS；冲突保持 hold。回滚按保存 revision 逐 ID 恢复，不能整库覆盖。
7. 从 PostgreSQL 按 rebuildEpoch + snapshotOutboxSeq 重建 prepared epoch 的 redis-state，连续追平 outbox、对账 intent / due / budget / claim、补投任务并逐账户 ready。未 ready 账户不得开放。
8. 短暂冻结新 gateway 流量，取得 deployment lock，并在同一数据库 barrier 写 `ingress_frozen + mutation_fence`：立即 fence 新连接、新 recheck command、due claim、admission 和 probe scheduling，同时冻结 source producer inventory certificate revision / digest；只允许 barrier 前已接受请求的 listener / finalizer、已 claim execution 的确定收尾、outbox / projector、fanout 与 source replay 推进到持久终态。此后才 seal legacy failure producer、停止旧 consumer / queue admission、完成第 4 步 export / rebuild 证明，并验证 active request / finalizer / legacy producer / consumer / in-flight queue 为零、certificate 对应的 `sourceProducerSetDigest` 全部 sealed / drained、连续水位闭合且 active certificate pointer 未变化。若确有 destructive / tightening migration，只有此时并且 rollback compat / before-image / signed restore proof 已齐备才可执行；正常 expand-contract 路径应把删除动作延迟到 W11。

   target gateway host 必须已按 manifest 创建并 fsync 新 producer epoch，`targetProducerSetDigest` 全部 registered 且处于 `active_target_pending`，首张 target producer inventory certificate、target replay lease / shadow state / outbox / role proof 齐备，同时确认 target producer delivery=0、业务流量=0、capability side-effect=0。随后才在同一事务 CAS 激活 version 2 epoch 与 target certificate pointer，把匹配 certificate / set digest 的 producer 原子转 active，使 version 1 迟到 mutation 被 epoch fence 拒绝；Go gateway / 每 host replay / control / Asynq / stats / API 再凭新鲜 runtime authorization 提交 active-mode role-ready barrier。全部角色 ready 后才原子切换反向代理并写 proxy_switched；source / target set 或 certificate digest、certificate pointer、证书链或任一角色失败都保持流量冻结并生成新的 rollback epoch。Node 进程可以保留作 rollback artifact，但 writer / readyz 必须为 disabled，不构成第二 owner。
9. 激活 v2 epoch 后所有流量都由 Go gateway 承接。manifest 把 capability 分成两个独立开关：`mutationCohort` 只控制哪些 accountRuntimeKey 产生新的 Collector / admission / probe / recovery mutation；`denySafetyProjection=true` 从首次激活起对**所有**请求持续应用 active epoch 已有的 blocked / recovering / evidence hold / account / global barrier。账户退出 cohort 或 cohort CAS 到 0 时只能停止新 mutation，绝不能忽略既有 deny 事实重新放行坏模型；这些事实冻结到 mutation 恢复、人工有审计裁决或完整新 epoch rollback。非 canary 流量仍由同一 Go owner 服务，不能回流 Node。

   mutation cohort 固定按 0% -> 1% -> 5% -> 25% -> 100% 扩大，每档至少 30 分钟，晋级只允许相邻 CAS。1% / 5% / 25% 使用 `controlMode=live_same_window`：candidate 是本档 `mutationCohortEnabled=true` 的独立 gateway Attempt，control 是同一 measurement window、同 release / route policy 下未启用 mutation 的独立 Attempt；两者 denominator 任一为 0 都不得晋级。每档除至少 10000 次 candidate Attempt 外，还必须达到 manifest policy 固定的最少 distinct accountRuntimeKey、Route、protocol profile、producer host，并让 positive_observation 与 failure_nomination 两类 handoff 非零覆盖；天然无失败流量时只能在隔离 namespace 使用确定性 failure envelope smoke，不能拿 0 事件当通过。

   25% -> 100% 前必须把最近一个成功且完整的 25% window 发布为签名不可变 baseline，固定 `controlMode=frozen_previous_cohort_baseline`、manifest / release / policy / schema digest、measurement 起止、candidate / live-control denominator、coverage、全部 typed measurement 和 hard-invariant 结果。100% 阶段没有 live control 是预期事实，不要求制造伪 control 或因 live-control denominator=0 失败；它要求 100% candidate denominator / coverage 非零、所有绝对 SLO 与 hard invariant 持续通过，并只对语义和单位仍一致的相对指标与冻结 25% baseline 比较。baseline 过期、digest / measurement schema 不匹配或签名不可验证时不得继续 100%，只能相邻降到 25% 重建 baseline。

   默认硬门禁为：重复 physical execution、跨 scope 误伤 / false allow、handoff append failure / unrecoverable、producer sequence gap、digest conflict、checksum corrupt、unknown tail、lost producer、新增 unresolved quarantine、5 分钟 admission / account physical gate 违规和未解释 dead task 均为 0；运行期 active producer 必须满足 expected=registered=watermarked=active，unacked oldest age / bytes、active hold count / ratio / max age 与 allowed-reason count 低于签名 policy 阈值；`sealed / drained` 只用于 activation / backup / rollback / stop barrier，不能作为在线 cohort 的假 ready 条件。projection p99 lag <= 5 秒且 max <= 30 秒；1% / 5% / 25% 的 capability 额外 5xx 相对 live control 增量 <= 0.5 个百分点，100% 同时满足签名 absolute 5xx SLO 和与 frozen 25% baseline 的可比增量门禁。所有 measurement 使用类型化字段、明确单位、cohort / control mode 和 denominator。

   transition matrix 固定为：晋级只能 `0 -> 1 -> 5 -> 25 -> 100`；普通可用性阈值退化一次只降一档 `100 -> 25 -> 5 -> 1 -> 0` 并重置观察窗口，后续独立窗口仍失败才可继续降；数据完整性、安全门禁、producer inventory / watermark / replica barrier 破坏可从任意档直接 CAS 到 0 并冻结 admission，`denySafetyProjection` 始终保持。projection lag 连续 5 分钟超 30 秒、已解释 infrastructure dead task 超过 10 个或仅导致保守多过滤的误过滤率超过 0.1% 属于普通逐档降级；跨 scope 放行、错误解除 deny、未解释 dead task 或 producer barrier 缺口属于直接归零。只有 listener / 数据契约本身故障才进入完整新 epoch 回滚。生产重检继续关闭，100% 稳定至少 60 分钟后单独开放。
10. W11 之前不删除 Node gateway / worker 源码与 rollback artifact。W11 只有在 Go gateway、worker、AI Chat 和回滚演练全部通过后才执行最终减法。

历史 `temporary_unavailable / runtime_degraded / recovery_wait / precheck_pending` 可能没有可靠 provenance。`last_error_code` 为空或来源不明的记录一律写入 `legacy_account_health_holds`，并标记 `decision=unclassified_hold`，保留切换前的保守调度效果，但不进入通用探针、父级投票或新 capability 状态机。只有已经持久化且语义明确的绝对 `cooldown_until` 可由 active epoch compatibility owner 到期；其余只接受人工确认的账户 ID allowlist、before-image 和逐 ID CAS 裁决。旧 producer / consumer 不得为“自然恢复”重新启动，也禁止声称可按旧来源码精确筛选。

### 22.4 回滚契约

- 开始切换后禁止只回滚二进制；旧 dispatcher 会重新产生整号探针。
- 回滚顺序固定为：先在同一数据库事务写 epoch `ingress_frozen + mutation_fence`，停止新连接、新 recheck command、due claim、admission、生产重检与 probe scheduling，并冻结当前 producer inventory certificate revision / digest；这些入口不能等 drain certificate 后才关闭，否则 drain 期间仍会生成新工作。barrier 前已接受的普通请求 / 长流只允许完成 finalizer 与 durable handoff，已 claim execution 只允许收敛到 terminal / indeterminate，outbox / projector、fanout 和 handoff replay 继续运行；barrier 后到达的 failure nomination 只持久终结为 `rollback_cancelled`，不得创建新 intent / execution / due。默认 drain 上限 5 分钟；到期仍存活的连接由 gateway 记录 `rollback_drain_aborted` 后主动终止，并等待其 finalizer / handoff 到达可审计终态。随后把该 certificate 的**当前 active producer set**逐个转 `draining -> sealed`，fsync 不可变 finalSequence（零流量为 0），并由 control 记录 seal proof。只有 active-request / finalizer / claimed execution / fanout 为零、active certificate pointer 未变化、每个 producer `control_resolved_through = producer_ack_through = finalSequence`、RPO=0 时另有 `replicaAckThrough=finalSequence`、未 ACK 数量与字节数为零、无 unknown tail / lost producer / global barrier，或每个已知残留 sequence 均有已校验 immutable quarantine + 精确 active hold 证明且仍达到 sealed finalSequence，数据库 barrier 才能签发 drain certificate；随后停止 gateway side-effect owner、handoff replay owner 和 Asynq consumer。quarantined sequence 只满足 delivery drain，不得冒充 health resolved，sealed producer epoch 永不重新打开。
- 随后以 fencing token 终结或停放 PostgreSQL 全部非终态 recheck command / item、logical intent、physical execution / member、result_ready fanout 和未投影 positive observation；pending_admission item 固定终结为 `rollback_cancelled` 并推进 command 计数，幂等指针继续指向该终态资源直到原 TTL。execution_started_at 后结果未知的物理执行只能收敛为 indeterminate，不能为完成回滚重发上游。在同一事务链终结 intent / member、写 outbox 并补偿 reservation / pending / running 计数；等待 projector 连续追平并验证 active command/item、active intent、非终态 physical execution、未完成 fanout、未投影 observation、孤立 provisional gate 与所有预算占用均为零。孤立 gate 只有在 ingress 已冻结、finalizer 已 drain、超过数据库最大事务时限且按 observationId 确认无 durable 行后才写审计 tombstone 并清理。导出 retry / dead task 清单后，才允许清理 capability Asynq namespace、Redis epoch 和运行态 tombstone。
- 破坏性 Goose Down 前，deployment coordinator 必须把尚未超过各自 `retain_until` 的 command / item、24 小时 task 资源、idempotency pointer、positive observation / 紧凑 tombstone、handoff terminal receipt / tombstone、quarantine / health-hold 索引、当前 blocked / recovering incident、terminal physical execution / member / result、generation floor 和必要 operation-log 引用复制到不参与业务 Goose 版本判断的 `juhe_rollback_compat` schema。同时生成 deny-only `capability_gate_snapshot`，按当前 Route / Attempt scope、definition revisions 和 runtime binding 保存 `blocked | recovering | evidence_unconfirmed | account_unconfirmed`，以及 source control outbox barrier / retainUntil；coordinator 还必须把 release 外 quarantine artifact 的实际 bytes 复制到 rollback 故障域的内容寻址 immutable archive，逐对象读回校验 length / digest / schema，并把签名 location remap 纳入 immutable `rollback_compat_manifest(sourceEpoch, barrierOutboxSeq, tableCounts, tableDigests, artifactDigests, producerWatermarks, capabilityGateDigest, maxRetainUntil, createdAt)`。复制必须在冻结 barrier 的一致快照上完成；manifest 按第 22.2 节 canonical envelope 签名，逐表 count / digest、artifact bytes、producer 连续 / replica 水位、引用完整性、source epoch 和签名校验全部通过前禁止 Down。存在 unknown tail / deployment-wide handoff barrier 时不得 Down 或切入 rollback gateway。
- 冻结的 rollback artifact 必须预先实现该 compat schema 的只读 adapter：每次候选 Attempt 在派发前应用 `capability_gate_snapshot` 的 deny-only 精确门禁，scope blocked / recovering / evidence_unconfirmed 时过滤，account_unconfirmed 时整运行账户保守门禁；它还要让 task GET 在原 24 小时窗口返回冻结终态，相同 Idempotency-Key + digest 在原 10 分钟窗口重放原 command 响应、异 digest 返回 conflict，并按 observationId / physicalExecutionId tombstone 拒绝 retired epoch 的迟到 task / handoff。adapter 不能创建新 v2 intent、推进 phase、自动恢复 hold 或成为第二 writer。compat gate 非空期间禁止 Base URL、凭据 lineage、supportedModels、映射、endpoint、probe strategy 等 capability-affecting 配置写入，只允许名称 / 备注等无关编辑，避免旧 gateway 无法解释新 scope。恢复 v2 或有审计的离线 adjudication 后才解除。compat cleaner 只按每行 retain_until 与上一节 producer terminal proof 删除；最长窗口结束并且 manifest / gate 全部清空后才删除 schema。若 rollback artifact 不支持精确 gate adapter，只能延迟破坏性 Down 并保持流量冻结，不能用重新放行坏 scope 来完成回滚。
- 完成 compat 导出后，才用 Goose 把业务 schema 恢复到冻结 rollback artifact 声明的精确版本；根据 before-image 生成全新的 rollback deploymentEpoch / manifest digest，写 prepared 行并完成旧 owner startup preflight，再在流量冻结窗口激活新 rollback epoch 并切换代理。不得原样恢复旧 manifest 或复用旧 Redis / Asynq epoch。额外取证副本可以另存 archive schema，但不能替代上述强制 compat manifest 与 adapter。
- 本方案不修改 accounts.status，因此不需要整库回档，也不会覆盖切换后其他业务写入。
- 若执行过迁移自动关闭或人工 legacy allowlist 清理，必须用保存的 before-image 和 configRevision / ledgerRevision 逐 ID CAS 恢复；切换后发生冲突的记录保持 fail-closed 并转人工，不能整库还原或覆盖新写入。上线备份只用于灾难恢复和逐表取证，不授权把 `juhe_business` / `juhe_stats` 整库恢复到旧时间点。
- 现有只会切换反向代理的 macOS `temporary-cutover`、普通上一发布包恢复和任何自动纯代理反切都不得用于 capability v2；各平台脚本必须调用 deployment coordinator 完成 ingress fence、drain、new rollback epoch 和 schema preflight，否则 fail-closed 退出。
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
8. 未允许付费探针的图片失败只形成不进入 health phase 的短时 cost-verification notice，能力保持 unknown；允许后真实 E2E 才可阻断和恢复。

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
23. 普通成功不直接恢复 OPEN；physical claim 后出现更新 positiveObservationVersion 时，迟到负向 outcome 以 superseded 终结并写 durable due，调度器必须使用新 generation 和新的 physicalExecutionId 验证，不能覆盖较新成功或复用已终态物理任务。
24. 两次恢复成功、间隔和 5/10/20/30/60 退避准确。
25. 配置、Key 集合、映射和授权在探针途中变化时，definition revision 真正变化或 scope 被删除的结果 fenced；无关配置变化的未变 scope 继续按 carry-forward 后的当前 definition 接纳，不能一律清空。

### 23.5 并发、存储与灾难恢复

26. 1000 个同 scope 并发失败在 5 分钟内只有一个 durable intent 和一个真实 probe。
27. 两个 gateway 不能各分配一个 generation。
28. 同 runtime 8 个不同 scope 都能建立 durable logical intent，模型 A 的 task retry 不阻塞模型 B；物理上游仍同时只调用 1 次。第 9 个 runtime scope、第 17 个 credential-source pending、全局第 4097 个 pending 得到可观察且可自动重试的 verification backpressure notice，队列替换无悬挂 SUSPECT / notice。
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
39. pending_test 用 durable activation selection 轮转免费 execution Attempt，任一成功即可激活；连续 24 小时无成功只产生 owner_action_required 告警，不因单模型或完整失败响应写整号 error。active 哨兵失败只影响精确能力。
40. 其他 Route、旧 dispatch revision、旧 universe revision 或缺少身份字段的成功不能顺延哨兵下一次检查；同 Route 的另一当前 Key 成功可以刷新 Route 水位，但不能清理原 Key 的 capability incident。

### 23.7 API、前端和监控

41. 列表单请求批量返回摘要，无 N+1、无 status-snapshot。
42. Catalog 合成 unknown，摘要不会把 A 成功、B/C 未观察显示为 healthy。
43. 六种 scope 状态、全部 aggregate 和硬状态优先级均有前端真实数据回归。
44. 管理 / my-* 的 400 / 403 / 404、授权实例脱敏、canViewCredentials、canRecheck 和 canViewTrace 正确。
45. recheck 的 202 / 409 / 429、Location、Retry-After、幂等 taskId 和最多 100 批量上限正确。
46. v1 哨兵小时记录和 v2 能力小时记录不互相改写；切换日前后、聚合滞后、同小时多 revision、短期快照分页和明细过期显示正确。

### 23.8 部署与回滚

47. capability_contract_version 能阻止新旧 dispatcher 混跑。
48. 来源不明 legacy temporary_unavailable / runtime_degraded / recovery_wait / precheck_pending 进入 hold，不被自动探针认领或清理。
49. Goose Up / Down、Catalog 重建、owner manifest 和“先归零 intent / 预算再清 Redis / Asynq namespace”的顺序可重复执行。
50. 完整回滚演练不丢失切换后的其他业务写入。

### 23.9 集成与所有权补充

51. capability probe、周期哨兵、激活、冷却、人工测试和生产重检失败都不会再次进入 Collector 或递归创建 intent。
52. Key-1 到 Key-2 轮换为两个不可变 Attempt 描述，各自重新门禁和取 lease；Key-1 的 scope / lease 不被复用。
53. 目标 Key 删除 / lineage 或 attemptDefinitionRevision 变化时 intent stale；其他 Key 集合变化不使未变 scope stale。Key owner 阻断时 capability probe 不旁路，恢复事件只唤醒独立验证。
54. Collector flush 的超时、IPC、Redis 和 PostgreSQL 异常均不改变 HTTP / SSE 结果、audit 终态或连接收尾；未 durable commit 不返回 accepted。
55. plannerContextKey、sourceRequestShapeKey 和 adapterImplementationRevision 变化会使旧 recipe fenced，且 payload 不含用户输入。
56. 两个授权实例使用不同 runtime identity、在 physical claim 前以相同 PhysicalProbeExecutionKey 加入同一 durable execution，只执行一次并分别 fenced fanout；claim 后新 intent 不消费旧结果，任一物理字段不同也不合并。
57. worker 只用显式 binding system account / group / authorization 上下文鉴权，opaque runtime key 无法被解析成权限。
58. 普通 OAuth token 刷新不改变 scope / revision；API Key 替换和授权 lineage 变化必然失效旧 intent。
59. 同一次执行没有 transport lease 时只写 transport observation；同时持有两种 lease 时，完整失败对 capability 为 unavailable、对 transport 为 framing_complete_neutral。
60. 周期哨兵 / 人工生产重检在稳定 phase 建 verification intent，不进入 SUSPECT 或 soft avoid；结果仍经过 durable admission、claim 和 fencing。
61. 重建扫描期间并发创建 OPEN / intent 时，shadow epoch 必须连续追平 snapshot 后 outbox 才 ready；gap、counter 不平或任务漏投均保持阻断。
62. enqueue 成功但 outbox ACK 失败、physical result_ready 后 Asynq ACK 失败都只按 physicalExecutionId 重放状态机；fanout 中途崩溃从 durable member 续跑，不重复上游调用，错误 token / revision / ledgerRevision 无法 upsert。
63. 7 天后的 dead task 不能人工复活；tombstone 删除前证明 intent / outbox / task 全终态，否则 generation floor 保留。
64. Route 列表与 Attempt 凭据分页粒度不混合，所有 cursor / scopeRef 绑定两个 revision，stale 页面返回 409。
65. 单 scope / 批量 recheck 的幂等、部分接纳、task Location / terminal / 24 小时过期及 trace 权限完整回归。
66. 授权实例与来源账户的哨兵四元组互不覆盖，旧单字段 reader 被 contract version 拒绝 ready。
67. legacy child-escalation 父 incident 被 CAS 关闭且子 incident 保留；显式 / 真全局父状态保留，来源不明账户进入 manifest 门禁。
68. 回滚必须先 fenced 终结 intent 并把预算归零，再清任务 / Redis 和 Goose Down；旧二进制只在精确兼容 schema 上启动。
69. catalog_only 中性终结不改 execution phase，但 accepted 后 5 分钟内不会被并发业务失败重复触发。
70. 同一 Idempotency-Key 的相同 request digest 返回原 task，不同 digest 返回 409；批量 admission 中途崩溃后只恢复 pending item，不重复 intent 或成本授权。
71. 纯 SUSPECT Route 聚合为 confirming，零凭据 Route 聚合为 unknown；只有全部 Route 都是 confirmed blocked 才得到 all_configured_capabilities_blocked，blocked + unknown 得到 partially_unavailable。
72. 当前小时持续写入时，旧 hourRef / revisionsRef 在签名 TTL 内仍能稳定翻完 Route、revision 和 event 页；快照到期后才返回 stale_view，不能因每次 publication 推进而永久 409。
73. stats 分区内存在 gap 时该账户 publication 不推进；不同分区并行消费不共享伪全局序号，重分区只能随 contract epoch 和全量 rebuild 完成。
74. proven request-derived 的旧 account-level runtime_degraded / recovery_wait / precheck_pending 不被重建，来源不明项进入 `legacy_account_health_holds(decision=unclassified_hold)`；A 正常、B 精确 runtime_degraded 时 A 仍保持原排序和调度。
75. version 2 切换验证 W7 / W8 / W10、legacy producer 全停、混合队列 drain / DB 重建、prepared epoch、短暂流量冻结、active CAS、代理切换和新 rollback epoch；HTTP 200 不能单独作为 ready 证据。
76. active intent 的成功 observation 在 provisional gate、PostgreSQL scope 串行化事务和 outcome reservation 前后逐点注入崩溃；只有 durable transaction committed 才被健康状态机承认为成功，已接纳成功必然递增 version 并 supersede 迟到负向，未接纳诊断不得冒充 fence。
77. physical execution 在 claim 前允许同 key member join；execution_started_at 前崩溃可重试，之后结果未知且 endpoint 未声明幂等、digest 不同或已超过 provider retention / 本地 replay deadline 时必须 indeterminate，manual-costed 和普通探针都不得盲重发。
78. manual-costed 业务失败只形成 5 分钟精确 cost-verification notice，重复失败不续期、不占预算、不写 blocked；A 和其他模型继续调度，owner 授权重检才可形成硬结论。
79. result_ready 后立即令原 logical / physical lease 过期并逐点崩溃；fanout 只能重领 delivery lease，冻结的 version / claimStartedAt / resultObservedAt 不变，且上游调用次数仍为 1。
80. manual-costed 在 command item、execution_started_at、cost_execution_started_at 和 dispatch token 的每个写点注入崩溃，数据库只能看到全有或全无；发现历史单边标记固定 indeterminate，不能重复计费。
81. provider 幂等窗口内相同 digest / key 可按契约重放，窗口到期、digest 变化或 provider 未声明支持时即使 Asynq task 尚在 7 天保留期也必须 indeterminate。
82. 零 Route 统一显示 no_routable_capability；blocked + 未到期 SUSPECT 得到 partially_unavailable 但 effectiveSchedulableRouteCount=0 时不得显示“可调度”，也不得冒充 all blocked。
83. Route / Attempt 翻页期间 control、Key、membership 或 dataStatus 产生新 current publication 时，旧 capabilitySnapshotRef 仍能在 10 分钟 TTL 内无重复 / 漏项翻完；只有引用到期、保留版本清理或权限撤销才 stale / 404，filter、limit、purpose 或权限视图变化不能复用 cursor。
84. 超过 100 个、直至 8192 x 50 边界的可重检 scope 不做全量 member INSERT；lazy selection 按 immutable source 固定顺序，用 nextSelectionRef 逐页无重复、无遗漏地领取。相同幂等键重放不推进 cursor，不同 key 并发消费同一 cursor 只有一个 winner；冻结项恢复后仍消费位置，重复 scopeRefs 去重，selection idle / absolute expiry、source pin 和容量门禁都返回确定错误。
85. 回滚在长流 finalizer、positive observation handoff、pending_admission command 和 result_ready fanout 各阶段注入；ingress / producer barrier 未归零时不得清 namespace 或切代理。
86. 对每一种 incident / intent / observation / physical-member / fanout / due / command / budget mutation 注入“表事务提交后 projector 崩溃”，统一 outbox barrier 必须补齐，任何缺 outbox mutation 都阻止 ready。
87. legacy provenance 冲突和缺字段进入持久 hold；只有完整证据链可自动 CAS 关闭，审批释放 / rollback 都按 before-image 逐 ID 执行。
88. active v2 epoch 的 1% / 5% / 25% / 100% cohort 只由 Go gateway 承接；阈值触发先自动降 cohort，纯代理反切脚本必须被拒绝。
89. positive observation 满 31 天但 producer handoff 未终态、replay deadline 未过或 retry/dead 证明缺失时只能压缩为紧凑 tombstone；同 observationId 重放不递增 version。全部证明齐备后清理也不会遗漏 active intent / fanout 引用。
90. 回滚在 command 24 小时、idempotency 10 分钟、physical 7 天和 observation 31 天窗口内执行时，先生成并校验 rollback compat manifest；Goose Down 后原 task 查询 / 幂等重放仍按冻结结果工作，迟到 epoch 写被拒绝，compat adapter 永不创建新 v2 状态。
91. finalizer append capability handoff 后在 control commit 前、commit 后 ACK 前和 ACK cursor 推进前逐点崩溃；重启 / lease 接管最终得到一个 receipt，同 observation version 只递增一次、同 request 最多 accepted 一个新 intent。
92. control RPC、Redis 或 PostgreSQL 中断期间持续产生大量同 scope 失败；spool 不丢行，恢复后仍触发探针，同时 5 分钟 admission 和 active intent 唯一约束保证物理探针次数不形成风暴。
93. spool 达到 age / bytes 预警线、硬上限、磁盘只读、fdatasync 超时和 segment 校验损坏时，gateway 按契约摘流或 fail readyz；append 失败但 emergency control commit 成功时事件仍只落一次，二者都失败时不得出现 accepted 且受影响 projection 为 unconfirmed。禁止 drop-oldest 后继续显示 healthy / 可调度。
94. 同 handoffId 异 digest、producer sequence gap、超过 replay deadline 的恢复 segment 和 receipt 已压缩 tombstone 分别 fail-closed；旧备份重放不能创建第二 intent 或重复正向 version。
95. `accountListSnapshotRef` 保持同一排序与小时历史时，当前页某 Route 从 schedulable 变为 blocked / unconfirmed；批量 current 水合立即返回新 `currentViewVersion`，`listStale` 只描述列表历史延迟且不能掩盖当前阻断。
96. retryable failure candidate 在首次扫描后由 due scheduler 回访；前排 candidate 一直 already-running、cooling 或 capacity saturated 时，父 receipt 最终分别闭合为 covered-by-existing、cooling-suppressed、no-eligible / pending-capacity，不会永久 pending 或重复探测。
97. 可证明单条 checksum 损坏和 replay deadline 超期先生成 immutable quarantine artifact，再推进 delivery ACK；对应 health hold 未被 fresh evidence / revision / adjudication 释放前始终 unconfirmed。无法确定 record boundary 的 unknown tail 不能推进 ACK，也不能恢复 producer ready。
98. success gate 在 admission reservation / marker / ledger、physical claim reservation / marker / ledger、Redis winner reserve / spool fsync / durable_spooled 和 PG commit 的每个边界逐点崩溃；S1 claim 前 committed 后，S2 claim 后必须使用新 claimGateEpoch 形成 fence。reserved 但未 spooled 的 winner 可被 lease takeover，Redis flush 下多个 fallback 由 positiveFenceKey 只递增一次 version，任何未确认 gate 都令精确 evidence unconfirmed。
99. Key-1 替换、增加模型 C、删除无关 Route 和修改 B 以外映射时，配置 staging diff 只让真实 changed / removed scope stale；未变 B + Key-2 的 OPEN / RECOVERING / hold / positive version 通过相同 definition revision 原子 carry-forward，不回落 unknown 或重新放行。
100. 模型 B 的可定位 handoff hold 只令 B 对应 Attempt evidence unconfirmed；模型 A 和 B 的其他 ready Key 继续调度。只能定位账户时才 account dataStatus unconfirmed；无法定位账户的 tail 激活 deployment-wide barrier，其他 gateway 也不得继续放行。
101. half_open 首次成功事务同时写 recovering、successCount=1、至少 30 秒后的 durable due 和 outbox；recovering task failure 耗尽仍写 infrastructure due，第二次成功原子 available 并清 due。worker / scheduler 在每个提交点崩溃都不会永久挂 recovering 或重复 capability backoff。
102. 同一 handoff 通过两个 delivery epoch 重投时只产生一个 business resolution、两个独立 delivery terminal；并发 candidate scheduler 在父 receipt 行锁与 accepted 条件唯一约束下最多创建一个 intent。replay owner lease 切换后旧 fencing token 无法推进 ACK；expected producer 丢失、unknown tail 或水位 gap 均保持 not-ready。
103. result_ready / indeterminate 原子释放 physical active key，旧 fanout 卡住时新 generation 可建立新 execution且不消费旧结果；join / claim 行锁竞争没有 claim 后加入，零 frozen member 不留下 poison key。
104. rollback compat gate 在 Goose Down 后仍精确过滤 blocked / recovering / scope-unconfirmed，并对 account/global barrier fail-closed；compat gate 活跃时 capability-affecting 配置写被拒绝，旧 rollback gateway 不能重新放行坏 scope。
105. Attempt phase=available / unknown 但存在 handoff、fanout 或 projection evidence hold 时，gateway 最终 Attempt gate、Route capabilitySchedulable 和 effectiveSchedulable 都为 false；同 Route 其他 evidence-ready Key 与模型 A 继续调度。
106. manual-costed OPEN 的第一次 owner 授权成功只写 recovering + successCount=1 + nextCostEligibleAt，终结 intent 且 nextProbeAt=null；没有第二条新授权命令时永不自动扣费。第二条在至少 30 秒后成功才 available，失败推进 nextCostEligibleAt，业务成功 / due sweep 均不创建付费调用。
107. active 周期哨兵完整失败同时把账户哨兵计划推进 1 小时 + jitter，并给精确 capability 写独立 5 分钟 recovery due；task failure / unknown 按 5/15/30/60 分钟基础设施计划，stale 由新 revision 接管。OPEN / HALF_OPEN / RECOVERING 或 existing intent 期间的每小时 tick 不增加上游调用，所有 outcome 后无 overdue 热循环。
108. 一个 physical execution 合并多个 scope 时，claim group 的全部 reservation / marker ACK 与一次多 scope ledger 事务不可分割；任一 scope stale、marker 部分失败或进程崩溃均不发送上游，补偿后每个 scope 的 success fence 仍按自己的 gate epoch 工作。
109. 同一 credentialSourceAccountId 的 8 个不同模型可同时拥有 logical intent，但任意滚动 5 分钟只启动一个自动 physical probe；其他 due 公平排队且不丢失。owner 显式重检可旁路自动间隔，但 running physical 始终最多 1，成本 / global budget 不可旁路。
110. pending_test activation selection 中 B 失败、A 成功会激活账户并保留 B 精确 blocked；满 24 小时无 success 只显示 owner_action_required。两个 scheduler、重启、page claim 丢响应和配置变化时 cursor / item 单调，不能重复第一候选或把旧 success 写入新 definition。
111. 首次 OPEN、half_open / recovering 的每种 outcome、task retry 耗尽、capacity saturated、Key owner park / wake 都在同一事务终结旧 intent / member、释放预算并创建或终结唯一 `account_capability_probe_dues`；accepted 才消费 due，重启和 lease 过期无丢 due / 双 generation / tight loop。
112. Route / credential 删除后在旧 handoff 在途期间原样重加，新的 membership incarnation / definition identity 使旧 observation、outcome、hold、due、intent 和 gate reservation 全部 stale；旧保留行与新 generation 不发生唯一键冲突。
113. fanout member 超过 delivery deadline 时转 `fanout_delivery_unconfirmed`、写通用精确 evidence hold并释放 logical budget；fresh evidence 只释放同 definition 且激活 sequence 不晚于它的全部旧 hold，更晚 hold 保留。
114. W8 backup 对**当前 active producer set**先 fence 新 ingress / command / due / admission / scheduling，允许 barrier 前 finalizer / outbox / fanout / replay drain，再令 set sealed；`controlResolved=producerAck=finalSequence`、RPO=0 时 `replicaAck=finalSequence`、unacked=0、无 lost / unknown tail / global barrier后才创建 snapshot。备份实际读取 public Goose ledger、business / stats / compat、内容寻址 artifact bytes 和 physical base backup / timeline / WAL；隔离恢复 fixture PITR 到 barrier LSN，并验证 pending / duplicate / quarantine + hold / tombstone、签名 location remap 与引用闭合。
115. mutation cohort 降档后，已存在 blocked / recovering / scope-account-global hold 仍由 denySafetyProjection 对所有请求生效；只有新 mutation 停止，坏模型不能因降档重新放行。1% / 5% / 25% 的 typed candidate / live control 任一 denominator 为 0、coverage 不足或新增 unresolved quarantine 时不得晋级；100% 使用签名 frozen successful 25% baseline，不要求伪造 live control，但 candidate / absolute SLO / hard invariant 任一不足即失败。
116. activation 冻结 source producer inventory certificate revision / digest，证明其 `sourceProducerSetDigest` 全部 sealed / drained，同时证明 manifest 的首张 target certificate 与 `targetProducerSetDigest` 全部 registered / fsynced / `active_target_pending` 且 delivery、生产流量、side-effect 都为 0；activation CAS 必须同时切 active epoch 与 target certificate pointer，只有匹配 target set 可转 active。backup、rollback 和普通停机只要求各自当前 certificate 的**active set** sealed / drained，不得把 activation target 预先 seal；检查水位后迟到 append 被 fencing 拒绝，replay owner 在 ingress 停止后继续到 drain certificate。
117. expand-only migration 在入口开放、Node / Go 并行 reader 下重复执行不破坏兼容性；destructive / tightening / owner contract switch 在缺少 `ingress_frozen + mutation_fence`、source drain 或 rollback proof 任一条件时被 coordinator / 数据库拒绝。
118. cohort transition validator 拒绝跨档晋级和普通退化直降多档；普通退化仅走相邻档，数据完整性、安全或 producer / replica barrier 从任意档直接归零。25% baseline digest、schema、release、policy、有效期任一不匹配时 100% 自动回 25%，不能继续使用旧 baseline。
119. RPO=0 在第二故障域 ACK 前注入 gateway crash、replica timeout、同故障域伪副本、digest 冲突和 stale fencing token；本地 append 均不得返回 durable，gateway 摘流且不会出现 handoff accepted。合法 receipt 覆盖 sequence 后重放仍只产生一个业务终态。
120. owner / activation / backup / rollback manifest 对 canonicalization、字段顺序、manifestDigest / signature 自引用排除、purpose、Ed25519 签名、key rotation / revocation、environment / epoch / barrier / release binding、expiresAt 和 sequence 重放做正反 fixture；任一篡改都 fail-closed。过期 archive evidence 只有取得引用原 digest 的新鲜 restore_authorization 才能执行恢复，且不得改写原 manifest。
121. capability v2 的 legacy backup、缺 physical base backup / 连续 WAL、只保存 artifact 路径 / digest 而未复制 bytes、raw compose down 和 sealed epoch reopen 都必须 fail-closed；恢复验证实际对象 byte length / digest / schema、PITR barrier LSN、producer / replica watermarks 与签名 location remap。
122. active deployment-wide global barrier 注入后，所有账户 API / 列表 / 能力详情 / AI 健康监控都返回 `dataStatus=unconfirmed + dataStatusReason=deployment_capability_barrier` 并显示“能力调度已全局暂停”；该原因压过 observation handoff / projection / rebuild，全部 Attempt fail-closed。barrier 经合法 CAS 解除后，各账户只恢复自己的原 dataStatus / scope hold，不得批量写 healthy。
123. producer inventory certificate 对首张 anchor、连续 revision、previous digest、manifest / epoch / environment binding、registry set digest、active pointer CAS 和签名做正反 fixture。普通备份后新 producer 只能由新 certificate 激活，sealed epoch 与旧 certificate 都不能再次授权 append；过期 activation envelope 不能用于进程重启，只有绑定同一 manifest、当前 certificate、release 和 role 的新鲜 runtime authorization 才可 ready。

## 24. 实现落点

- Go gateway：最终 route / attempt descriptor、批量门禁、Key 过滤、request tracker collector 和 release 外 capability handoff spool producer；gateway 不拥有 replay 生命周期。
- Go capability-handoff-replay：每个 gateway host 一个独立受监管服务，按 producer epoch / segment lease 重放、推进 ACK、生成 quarantine artifact 并上报 watermarks；ingress 停止后继续运行到 drain 证明完成。
- Go control worker：model_capability scope policy、durable 正向观察接纳、admission、ledger、logical intent、统一 control outbox、reconcile 和 rebuild barrier。
- Go Asynq worker：按 physicalExecutionId 执行精确 ProbeRecipe，维护 execution lease、不可变 result 与独立 fanout delivery lease；恢复 due 不复用已终态 execution。
- PostgreSQL `juhe_business`：扩展 circuit ledger、positive observation、handoff receipt、logical intent、physical execution / member、Catalog、command、budget、due 和统一 control outbox。
- PostgreSQL deployment control：append-only contract epoch、producer inventory certificate / active pointer、activation / backup / rollback barrier、role registration 和 trust / revocation 状态；owner manifest 不承载运行期可变 inventory revision。
- PostgreSQL `juhe_stats`：保存 stats contract epoch、连续 event outbox / ingest、deadline / hour-close cursor、Route segment / event projection、账户级 publication / member / pointer 和 hourly summary。
- gateway release 外共享目录：保存按实例分段的 capability handoff spool、ACK cursor 与接管 lease；它是控制面短时故障期间的必达交接事实，不能放在会随发布删除的 release 目录。
- redis-state：缓存原子 phase / lease / generation / budget / due / readiness 和 provisional gate；不保存长期唯一事实，flush 后必须可由 PostgreSQL 重建。
- frontend：账户列表统一 presentation、能力明细、生产重检、带短期快照与独立游标的 v2 健康监控。

当前 Node 实现可作为行为证据和迁移参照，但完整完成定义不新增 Node + SQLite 双套长期 adapter。切换后必须删除或禁用 Node 的 accountId request_failure 健康派发和 protocol_model 子投票父级升级。

## 25. 完成定义

以下条件全部满足才算完成：

- A/B、多 Key、映射、endpoint、流式提交后失败均能精确归因和隔离。
- 模型能力层没有任何 accounts.status 写入口。
- transport 子 incident 不再通过计数打开父账户。
- 5 分钟冷却、跨实例单飞、物理预算、gateway durable handoff、可靠队列、fencing、重建和回滚都有自动化证据。
- unknown、部分阻断、全部阻断和恢复中在列表、详情与健康监控一致展示。
- capability、Attempt、健康账户、小时 revision、Route 与事件分页都使用权限绑定的短期快照；当前投影变化不会造成重复 / 漏页或永久 409。
- stats epoch、连续 outbox、deadline、hour-close、账户级 publication 和保留期证明均通过故障注入与容量门禁。
- 旧 request_failure 哨兵路径已互斥下线。
- W7 / W8 / W10 证据门禁、canonical manifest v3 bootstrap、链式签名 producer inventory certificate / runtime authorization、source / target producer activation、Go-only cohort、全平台 runbook 和新 epoch 回滚演练均已完成。
- 所有 active capability v2 环境都有可 PITR 到 barrier LSN 的 physical backup + 连续 WAL、实际 artifact bytes 归档、可验证签名 envelope；声明 RPO=0 的环境另有跨故障域逐 sequence replica receipt 证据。
- cohort promotion / degradation transition matrix、1% / 5% / 25% live control 和 100% frozen successful 25% baseline 均由 schema validator 与故障注入证明，不依赖人工解释零分母。
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
