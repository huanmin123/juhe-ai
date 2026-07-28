# AI 账户多模型能力健康与精确隔离设计

> 状态：目标设计，尚未实现。
>
> 本文只解决一个问题：同一 AI 账户配置多个模型时，一个模型失败不能拖死其他仍可用模型，同时失败模型也不能继续被无限调度。
>
> 自动闭环范围是当前允许后台执行独立最小探针的文本生成 Route。纯图片生成受现有“后台不得自动付费生图”规则限制，边界见第 3、6、9 节。

## 1. 当前代码事实

以下结论来自当前实现，不是目标能力：

1. 网关请求失败通过 `request-failure-health-check.ts` 只发送 `accountId + request_failure`，没有携带实际失败模型、endpoint、映射路径或所用 Key。
2. `account-health-check.service.ts` 对 `request_failure` 使用 5 分钟账户级内存 Map 去重。它只能约束当前 worker 进程，worker 重启或多实例部署后不构成唯一锁。
3. 请求失败触发的健康检查仍使用账户保存的 `healthCheckModel + healthCheckEndpointMode`，不一定是本次失败的模型和请求形态。
4. `request_failure` 检查的失败阈值固定为 1；独立检查未成功后，当前实现可以把整个账户写成 `temporary_unavailable`。
5. 当前 account circuit 已有 `CLOSED / SUSPECT / OPEN / HALF_OPEN / RECOVERING`，并支持 `account / key / protocol_model` 三种作用域；其中 `protocol_model` 主要处理 transport 完整性，作用域还不包含精确 endpoint mode、模型映射路径和所选凭据。
6. 当前 `protocol_model` 子电路可以按多个子作用域失败升级为父账户电路，因此仍可能让一个多模型账户整体不可调度。
7. 当前 AI 健康监控页面展示的是 `healthCheckModel` 单哨兵的小时结果和账户持久状态，不具备模型级当前状态或模型级历史。

因此，当前系统确实存在两个相反问题：

- 模型 B 已经持续失败，但账户仍可能显示可调度并继续命中 B；
- 请求失败触发账户级检查后，又可能因为哨兵失败把仍可使用模型 A 的整个账户隔离。

## 2. 设计结论

采用“账户硬状态 + 模型能力状态 + transport 电路”三层结构：

- `accounts.status` 继续管理人工停用、待激活、整号异常、整号限流、整号临时不可用和质量隔离。
- 新增精确的模型能力作用域，管理某个实际模型调用组合是否可执行。确认后的成功 / 失败事实绑定真实凭据来源账户；授权实例只叠加自己的状态、授权、分组、额度和未确认失败的本地避让，不重复探测同一套上游资源。
- 现有 transport 电路继续只判断连接、超时、读取中断和响应 framing，不判断模型业务能力。

模型能力层不得写入或清理 `accounts.status`。模型 B 失败时只阻断 B 的精确调用组合；只要模型 A 仍有可执行 Key，A 就继续调度。

自动探针不维护 HTTP 状态码、错误码或错误正文分类表。只判断：

- 探针是否真正发到上游；
- 是否得到协议校验通过的成功结果；
- 没成功时，是上游能力不可用，还是探针任务自身未完成。

完整的 `401 / 403 / 429 / 500 / 502 / 503`、错误 JSON、HTML 或供应商自定义错误，对模型能力探针统一是“本次能力验证未成功”；不得据此推断密钥失效、欠费、封号或供应商故障。具体错误仍只用于诊断和审计展示。

## 3. 不在本设计范围内

- 不改变部署、备份、灾备或发布协议。
- 不重写 Node 转 Go 的整体迁移方案。
- 不把 AI 健康监控改造成逐请求统计系统。
- 不按页面打开行为扫描所有模型。
- 不让人工测试修改生产健康状态。
- 不为未知供应商维护错误码白名单或黑名单。
- 不把 tools、图片输入、structured output、提示词内容等任意请求特征扩展为健康状态维度；本设计验证的是模型在某种上游请求形态下的最小可执行能力。
- 不用 `/v1/models` 的目录可见性冒充纯图片模型的真实生成能力，也不在没有单独成本授权时新增后台付费生图。

## 4. 精确能力作用域

### 4.1 Route 作用域

`CapabilityRouteKey` 表示不含具体凭据的模型调用路径：

```text
credentialSourceAccountId
clientModel
clientEndpointFamily
finalUpstreamModel
upstreamEndpointMode
```

字段含义：

- `credentialSourceAccountId`：真实保存 Base URL、代理、模型和凭据的来源账户。自有账户取自身 ID；授权实例取来源账户 ID。同一来源的健康事实只维护一份。
- `clientModel + clientEndpointFamily`：调用方请求命中的模型和入口形态，用于区分直接模型与模型映射来源。
- `finalUpstreamModel + upstreamEndpointMode`：模型映射和协议桥接完成后真正发给上游的模型和请求形态，例如 `responses_sse`、`chat_completions_json`、`messages_sse`。

客户端与最终上游两组字段已经能区分直连、映射和协议 bridge，不再增加重复的路径类型字段。这组字段足以回答“模型 B 的哪条实际调用路径不可用”。不得继续把 prompt、tool、图片附件、响应格式或其他请求字段加入 key，否则状态数量会随业务参数组合失控。

### 4.2 Attempt 作用域

`CapabilityAttemptKey = CapabilityRouteKey + credentialScopeKey`。

`credentialScopeKey` 使用现有凭据槽位的稳定非敏感标识或内部指纹，不保存 API Key 或 OAuth token。API Key-1 的失败不能阻断同账户的 API Key-2。

### 4.3 revision

每个作用域携带 `definitionRevision`。以下变化必须只为受影响的 Route / Attempt 生成新 revision，使旧探针结果失效：

- Base URL、代理、协议档案或 endpoint mode 变化；
- 支持模型、模型映射或桥接配置变化；
- API Key 被替换、删除或重新加入；
- 来源账户发生会改变真实上游执行配方的其他变化。

授权关系、授权实例本地状态、分组绑定、优先级、额度和无关展示字段不改变来源账户能力定义，不能让所有模型状态回落 unknown。

## 5. 状态与调度语义

复用现有 circuit phase，不再创建第二套平行状态机：

| 内部 phase | 页面状态 | 调度行为 |
| --- | --- | --- |
| `CLOSED` 且当前 definition revision 有成功事实 | 可用 | 正常参与调度 |
| `CLOSED` 且当前 revision 没有成功事实 | 未确认 | 允许尝试，但不能显示绿色健康 |
| `SUSPECT` | 确认中 | 共享层只表示探针待确认；触发失败的运行实例对该 Attempt 做本地避让，其他授权实例在独立探针确认前不继承负面阻断 |
| `OPEN` | 暂时不可用 | 只排除该 Attempt |
| `HALF_OPEN` | 半开验证 | 只允许持有探针租约的后台验证 |
| `RECOVERING` | 恢复中 | 普通请求仍排除，按现有恢复阈值继续验证 |

现有 circuit 的退避数组、恢复成功次数和 lease/fencing 机制继续复用；本设计不另造一套时间参数。唯一新增的固定产品规则是：同一物理凭据来源账户，自动物理探针每 5 分钟最多启动一次。

## 6. 请求失败如何触发探针

### 6.1 可以提名探针的失败

只有同时满足以下条件的最终 Attempt 才能提名：

1. 来自普通用户 gateway traffic，而不是健康检查、人工测试、恢复探针或模型检测；
2. 已经选择了明确的凭据来源账户、Route 和凭据；
3. 确实尝试向真实上游派发；
4. 最终没有形成协议校验通过的成功结果；
5. 同一请求内，相同 Attempt 后续没有成功；
6. definition revision 在提交时仍有效。

本地鉴权拒绝、参数校验失败、没有选到账户、用户在派发前取消或后台任务自身异常都不能提名模型能力失败。

只有已经定义无用户数据、可独立执行的最小探针配方的 Route 才能提名。当前纯图片生成没有获准的后台付费探针，因此普通生图失败只保留请求诊断和 transport 结果，不进入本设计的自动 `SUSPECT -> OPEN` 闭环；不得改用 `/v1/models` 结果代替。

同一个网关请求无论经历多少账户、Key 或后备分组重试，最多接纳一个新的共享验证意图。选择请求结束前最后一个真实上游失败、且没有被同 Attempt 后续成功抵消的候选；其他 Attempt 仍保留诊断事实，但不能由一个请求扇出多份探针。

### 6.2 请求路径只做两件事

请求失败本身只允许：

1. 在当前自有账户或授权实例的易失运行态中避让该精确 Attempt，健康候选和后备分组优先；
2. 把来源账户的精确 Attempt 置为或保持 `SUSPECT`，请求后台建立共享验证意图。

共享 `SUSPECT` 本身不阻断其他授权实例。请求失败不得直接写 `OPEN`、`temporary_unavailable` 或 `error`，也不得根据响应状态码选择账户级动作。

### 6.3 同一请求中的成功优先

如果 Key-1 失败后 Key-2 在相同 Route 成功：

- Key-1 的失败候选仍可验证 Key-1；
- Route 仍然可调度，因为 Key-2 可用；
- 不得建立账户级失败；
- 较早、较低 revision 的失败结果不能覆盖较新的成功事实。

普通请求的最终成功也是正向事实：它可以把当前 revision 下从未观察的 Attempt 标为可用，或取消同 generation 的共享 `SUSPECT` 和所有匹配的实例本地避让。只在首次成功、revision 变化或 phase 变化时异步持久化，不为每个成功请求写健康记录。

## 7. 探针执行与结果

### 7.1 探针必须验证实际失败能力

后台探针使用失败 Attempt 保存的非敏感配方重新解析当前来源账户配置：

- 相同最终上游模型；
- 相同客户端模型/入口和最终模型/endpoint；
- 相同凭据槽位；
- 当前仍匹配的 definition revision。

不得退回账户的 `healthCheckModel` 代替失败模型。凭据或配置已变化时，本轮结果记为 stale，不写健康状态。

### 7.2 结果只有四类

| 结果 | 含义 | 状态动作 |
| --- | --- | --- |
| `complete_success` | 真实上游执行成功且协议校验通过 | 关闭 SUSPECT，或推进 HALF_OPEN / RECOVERING |
| `capability_unavailable` | 已真实执行，但没有得到协议成功 | SUSPECT 确认为 OPEN；恢复期则回到 OPEN |
| `probe_task_failure` | 未形成可信上游结论，例如本地任务异常、取消或执行环境故障 | 不改变能力 phase，按基础设施重试 |
| `stale` | 来源配置、凭据或 revision 已变化 | 丢弃旧结果，由新 revision 按需重新观察 |

transport 同时独立记录自己的结果：完整 HTTP/SSE framing 对 transport 是中性或成功，对 capability 仍可以是 `capability_unavailable`。两者不能共用结果解释器。

`probe_task_failure` 或 `stale` 不能让 `SUSPECT` 和实例本地避让无限保留。现有确认租约 / 有界等待结束仍没有可信结果时，清除本次本地避让并把共享能力回到 `CLOSED + unknown`；只能等待后续真实失败重新提名，不能写 `OPEN`。

### 7.3 恢复

- `OPEN` 到期后由后台取得唯一 half-open lease；普通业务请求不能抢占该 lease。
- 成功后按现有 circuit 的 `RECOVERING` 成功阈值继续验证；达到阈值才回到 `CLOSED`。
- 任一次可信失败回到 `OPEN` 并继续现有有界退避。
- 业务请求在 OPEN 期间偶然成功可以记录正向事实并提前安排恢复验证，但不直接跳过恢复阈值。

## 8. 单飞与防探针风暴

必须同时满足两层约束：

### 8.1 精确作用域单飞

同一个 `CapabilityAttemptKey + definitionRevision` 同时只能有一个活动验证意图。1000 个并发失败只能合并到同一 generation，不能累计 1000 次失败后扩大处罚。

### 8.2 来源账户 5 分钟门禁

按 `credentialSourceAccountId` 建立持久门禁。实现只需要一个带过期时间的原子租约和下一次允许时间，不需要新的部署协议：

```text
nextAutomaticProbeAt
leaseOwner
leaseUntil
leaseScopeKey
leaseGeneration
```

自动探针启动时必须原子完成：

1. 确认当前没有 running probe；
2. 确认数据库时间已经到达 `nextAutomaticProbeAt`；
3. 取得带过期时间的租约并把下一次允许时间推进 5 分钟；
4. 绑定本次 Attempt 和 generation，迟到结果只能提交到相同 generation 和 definition revision。

该门禁必须位于权威业务存储，不能继续只用进程内 Map。worker 重启、多个 gateway 或多个授权实例都共享同一物理来源账户门禁。

不同模型的逻辑验证意图可以同时等待，但物理调用按最早到期时间选择。一次只启动一个；其余保留各自的 `SUSPECT / retryAt` 状态，由 worker 到期再选，不新增一套无限队列或忙轮询。

人工测试和现有账户操作都不能绕过这条自动探针门禁，也不能写模型能力状态。本设计不新增“强制生产探针”入口。

### 8.3 已知取舍

来源账户 5 分钟门禁会限制所有模型：如果 A 刚完成探针，B 随后首次失败，B 最长可能等待 5 分钟才得到独立确认。等待期间，触发失败的运行实例本地避让 B，其他可用模型、Key、账户和后备分组继续工作；其他授权实例不因未确认失败被同步阻断。

这是“防止单账户探针风暴”与“更快确认不同模型”之间的明确取舍。本设计选择遵守单来源账户 5 分钟规则，不允许实现按模型偷偷绕过。若产品不能接受最长 5 分钟的模型确认等待，应在实施前把规则改成“同来源同时一个探针、每个 Attempt 各自 5 分钟冷却”；两种规则不能混用。

## 9. 周期健康检查与激活检查

账户保存的 `healthCheckModel + healthCheckEndpointMode` 继续承担“激活锚点”和单哨兵作用，不升级成全模型扫描：

- `pending_test` 只要求用户选定的激活锚点成功，以证明这套来源账户至少有一条基础调用路径可用。锚点失败时账户继续待检查；系统不自动遍历所有支持模型，用户可以调整锚点后重试。
- active 账户每小时哨兵仍写现有 v1 健康历史，但页面必须明确它只代表锚点。
- 只有真实凭据来源账户拥有激活锚点和周期哨兵任务；授权实例复用来源的脱敏能力结果，不为同一套上游资源重复创建每小时任务。
- 生成类哨兵结果可以更新它对应的精确 Route/Attempt；不得据此把其他模型标为可用或不可用，也不得再把已激活的多模型账户整体写成 `temporary_unavailable`。
- 现有纯图片模型自动检查使用 `/v1/models` 目录存在性，它只能更新“目录可见”诊断，不能冒充真实生图能力成功。真实图片 Route 只有实际生成请求或同形态独立探针才能确认。

因此，“AI 健康监控每小时一次”和“请求失败触发验证”不是重复探测：前者是固定哨兵计划，后者是按真实失败能力建立的按需验证；两者都必须经过同一物理账户 5 分钟门禁和 running 单飞，若配方相同则复用同一次执行结果。

## 10. Route 与账户聚合

### 10.1 Route 聚合

同一 Route 有多个 Key 时：

- 任一 Attempt 可调度，Route 就可调度；
- 部分 Key OPEN、部分 Key 可用，Route 显示“部分凭据不可用”；
- 对当前运行实例，本地避让或共享 `OPEN / HALF_OPEN / RECOVERING` 的 Attempt 不参与普通派发；所有 Attempt 都被过滤时，Route 才不可调度；
- 没有当前凭据时显示“无可用凭据”，不能显示能力健康。

### 10.2 账户摘要

账户列表同时返回两类事实：

- `status`：现有账户持久状态；
- `capabilitySummary`：只汇总来源账户的模型能力事实；
- `effectiveSchedulableRouteCount`：再叠加当前自有账户或授权实例的状态、授权、额度、分组、Key、transport 和本地避让后，当前实例真正可以派发的 Route 数量。

`capabilitySummary` 使用以下状态：

| 状态 | 条件 |
| --- | --- |
| `unknown` | 至少一个 Route 在当前 revision 没有确认结果，且不存在确认中或不可用 Route |
| `available` | 全部 Route 在当前 revision 都有至少一个成功 Attempt，且没有局部异常 |
| `partially_unavailable` | 至少一个 Route 可调度，同时存在 `SUSPECT / OPEN / HALF_OPEN / RECOVERING` 的 Route 或 Attempt |
| `verifying` | 当前没有可调度 Route，但至少一个 Route 仍在 SUSPECT / HALF_OPEN / RECOVERING |
| `unavailable` | 当前配置 Route 全部已确认不可调度 |
| `no_routable_capability` | 没有有效 Route 或支持模型配置为空 |

页面只有在账户硬状态允许且 `effectiveSchedulableRouteCount > 0` 时才显示“可调度”。`status=active` 不能单独推出“可调度”。

实际请求仍按请求模型过滤精确 Route：账户可能整体“部分可用”，但对请求模型 B 是不可调度，对模型 A 是可调度。

同一来源账户的授权实例共享已经确认的模型能力事实和探针门禁，但不会共享授权实例自己的 `accounts.status`、调度开关、冷却、授权有效性、额度、分组绑定或未确认失败的本地避让。来源模型 B 被探针确认 `OPEN` 后所有实例共同过滤；恢复后，各实例只在自身其他门禁也允许时恢复调度。

## 11. 存储边界

复用现有 account circuit 控制面，最小扩展如下：

1. `AccountCircuitScope` 增加 `model_capability`，包含第 4 节的来源账户 Route / Attempt 身份。
2. durable circuit incident 保存 phase、generation、definition revision、retryAt、恢复计数、lease 和最后可信结果。
3. 为每个凭据来源账户保存第 8.2 节 5 分钟门禁。
4. 探针配方只保存客户端模型与入口、最终模型与 endpoint、凭据槽位标识和 revision；不保存 token、API Key、请求正文或上游正文。
5. 状态按需稀疏创建：只为首次成功、失败提名、探针或 phase 变化涉及的 Attempt 保存记录，不预生成“全部模型 × 全部 endpoint × 全部 Key”的笛卡尔积。配置中存在但没有记录的 Route 直接聚合为 unknown。
6. 未确认失败的实例本地避让复用现有易失运行态，不写来源账户持久状态；权威 generation、门禁和确认后的探针结果必须能够在 worker 重启后恢复，Redis / memory 只能作为热路径投影。

具体实现沿用届时项目已经启用的权威业务存储和 worker，不在本文另写数据库或部署拓扑方案。

## 12. 调度改动

调度顺序固定为：

1. 应用现有账户硬状态、授权、到期、分组和时间计划门禁；
2. 完成模型映射和协议桥接，得到 `CapabilityRouteKey`；
3. 枚举当前可用凭据，为每个凭据生成 `CapabilityAttemptKey`；
4. 分别求交 Key 运行态、transport 电路和 model capability phase；
5. 在剩余 Attempt 中按现有 Key 池和账户调度策略选择；
6. 全部被过滤时进入现有后备分组或有界等待，不回退使用已 OPEN 的模型能力。

必须删除两条旧行为：

- 普通请求失败不再调用只携带 accountId 的 `request_failure -> account healthCheckModel` 路径；
- `protocol_model` 子电路不再按子作用域数量升级父账户电路。

transport 的真正账户全局故障如果以后需要，必须由独立账户级探针和明确证据建立，不能从多个模型失败投票推断。

## 13. API 与前端

### 13.1 账户列表

账户列表只增加用于正确展示的最小摘要：

```text
capabilitySummary.status
capabilitySummary.routeCount
capabilitySummary.availableRouteCount
capabilitySummary.lastObservedAt
effectiveSchedulableRouteCount
```

前端保留原账户状态标签，并增加独立的“模型能力”标签。不得把两者合成一个字段，也不得由前端自行重新计算状态。

### 13.2 能力详情

AI 健康监控按需加载 Route 详情，返回：

```text
clientModel
clientEndpointFamily
finalUpstreamModel
upstreamEndpointMode
routeStatus
effectiveSchedulable
automaticIsolationSupported
credentialSummary（仅所有者/管理员可见计数，不返回指纹）
lastProbeOutcome
lastObservedAt
nextProbeAt
```

详情只需要展示每个 Route 下“可用 / 确认中 / 不可用”的凭据数量，不提供凭据级展开。授权使用方不能看到凭据数量、指纹或错误原文，只看到 Route 级结果。

### 13.3 AI 健康监控

现有每小时哨兵历史保持原样，明确标注为“激活锚点 / 账户哨兵”。页面在账户卡片增加当前 `capabilitySummary` 和模型能力详情即可。

本设计不新增逐模型长期统计、复杂快照引用、跨 revision 历史分页或独立统计数据库。确有历史审计需求时，优先展示 circuit transition 的有界事件列表，不把目标设计扩张成新的统计平台。

## 14. 启用与兼容边界

该能力不能以两个会同时生效的半成品路径上线。启用时必须同时满足：

1. `model_capability` 状态、来源账户 5 分钟门禁、精确失败提名、调度过滤和前端摘要已经闭环；旧能力初始为 unknown，不从 v1 哨兵历史倒推。
2. `request_failure -> accountId -> healthCheckModel -> 整号 temporary_unavailable` 旧路径已经关闭，不能与精确能力路径并行写状态。
3. 周期哨兵和激活检查按第 9 节执行；人工测试仍保持零生产副作用。
4. 现有 `accounts.status=temporary_unavailable` 不迁移成某个模型失败，因为历史数据无法证明真实模型、endpoint 和 Key，继续由现有账户恢复入口处理。
5. 现有 `protocol_model` incident 继续按 transport 语义自然恢复或过期，不转换成 model capability；子 scope 自动升级父账户的旧逻辑必须关闭。

本功能不要求新的部署、双写或迁移平台。若任一闭环组件未完成，则整个能力保持关闭，而不是让新旧两套健康写入同时运行。

## 15. 可观测性

只要求能够回答四个排障问题：

- 哪个来源账户、Route 和 generation 因什么请求事实进入确认；
- 探针是被去重、被 5 分钟门禁延后、实际执行，还是任务自身失败；
- 哪个可信结果引起了 phase 变化；
- 调度为什么过滤某个 Attempt，同时为什么仍选择了同账户的其他 Route 或 Key。

指标保留探针提名 / 去重 / 执行 / 结果数量、等待时间、各 phase 数量和“部分可用 / 全部不可用”账户数量即可，不新增逐请求统计平台。

日志不得包含密钥、token、用户 prompt、附件或上游响应正文。

## 16. 验收矩阵

以下测试全部通过才算功能完成：

1. 同账户 A 成功、B 失败：B 被阻断，A 持续可调度，`accounts.status` 不变。
2. 同 Route 的 Key-1 失败、Key-2 成功：Route 仍可调度，只隔离 Key-1 Attempt。
3. 1000 个并发相同失败：只形成一个活动验证意图。
4. 单个请求重试多个账户 / Key 后失败：最多接纳一个新的共享验证意图，不按 attempt 数量扇出探针。
5. 同一来源账户多个模型同时失败：任意滚动 5 分钟只启动一个自动物理探针，其余状态保留并按到期时间后续验证。
6. worker 重启和双实例竞争：5 分钟门禁与 running=1 仍成立。
7. 请求失败的探针使用实际失败的客户端模型/入口、最终模型/endpoint 和凭据，不使用 `healthCheckModel` 替代。
8. `401 / 403 / 429 / 500 / 502 / 503` 和错误正文不触发不同状态分支，统一按探针是否成功处理。
9. 探针任务取消、本地异常和配置变化不把能力写 OPEN；确认租约结束后也不会无限保留本地避让。
10. 较旧失败结果晚于新成功返回时，旧结果因 generation / revision fence 失效。
11. `OPEN -> HALF_OPEN -> RECOVERING -> CLOSED` 和恢复期再次失败均能按现有 circuit 阈值触发。
12. 周期哨兵与请求触发验证配方相同时复用执行，不产生双探针；配方不同时仍受账户 5 分钟门禁。
13. 人工测试和现有账户操作不改变模型能力或 transport 状态，也不能绕过自动探针门禁。
14. 账户 status active、全部 Route blocked 时，列表不再显示绿色“可调度”。
15. 部分 Route blocked 时，列表显示“部分能力不可用”，请求模型 A/B 的候选过滤结果不同。
16. AI 健康监控继续正确显示 v1 哨兵历史，并能查看当前模型能力摘要；不会把一次哨兵成功显示成全部模型健康。
17. 旧 request_failure 账户级派发和 protocol_model 父账户升级在新路径启用后均有测试证明已关闭。
18. 同一来源账户的多个授权实例复用确认后的模型能力和 5 分钟门禁；某实例的请求失败只让该实例本地避让，探针确认 `OPEN` 后才阻断全部实例，同时各实例继续保留独立的账户状态、授权、额度和分组门禁。
19. 激活锚点失败时账户继续待检查；已激活多模型账户的锚点失败只影响对应 Route，不阻断其他已确认可用 Route。
20. 纯图片 Route 明确显示不支持自动精确隔离；目录探针成功不能把它标成真实生成可用，普通生图失败也不能在没有付费探针授权时写 `OPEN`。

## 17. 实现影响面

若审核后决定实施，实际改动只应落在以下现有边界，不新建平行平台：

- 网关最终派发描述和请求失败收集；
- account circuit 控制面及其权威存储；
- 后台账户探针 worker；
- 候选账户 / Key 过滤；
- 账户摘要 API、AI 健康监控页面；
- 对应并发、恢复、授权实例和前端状态回归。

## 18. 是否值得实施

本文证明的是“如果要解决该问题，边界应如何设计”，不证明该能力在当前生产中的收益一定高于成本。决定写代码前，应只看现有使用记录和审计中的三个事实：

1. 是否反复出现同一凭据来源账户中，模型 B 在一个时间窗口持续失败而模型 A 同期成功；
2. B 的失败是否确实导致整号状态变化，或导致后续请求继续命中 B；
3. 这种事件的账户数、请求数和持续时间，是否足以承担新增稀疏状态、调度过滤、持久单飞和前端展示的维护成本。

如果账户实际基本只承载一个模型，或故障几乎总是整号共同失败，就不建议实现本能力；继续完善账户级探针更简单。如果上述局部故障已经稳定发生，本设计才有明确价值。纯图片自动隔离是否值得承担真实探针成本，仍需单独决定，不能混在文本模型实现中默认开启。

## 19. 完成定义

只有同时满足以下条件才算完成：

- B 的失败不会再让仍可用的 A 停止调度；
- B 被确认不可用后不会继续接收普通请求；
- 请求失败不会再通过固定哨兵模型把整个账户写成临时不可用；
- 单来源账户 5 分钟物理探针门禁在重启、多进程和多个授权实例下仍成立；
- 账户列表、能力详情和 AI 健康监控对当前可调度性给出一致结果；
- 文档明确标识当前事实与目标设计，没有把未实现能力写成已上线。
