# AI 账户主探测与 Key-模型短屏蔽修复设计

> 状态：最终实现契约 v1.2，代码尚未按本契约落地。
>
> 本文是本轮生产调度问题的唯一实现依据，并覆盖 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 在“非主模型短屏蔽”部分的早期目标方案。它是对现有账户健康探测、API Key 故障隔离和网关调度的局部修复，不采用重做整套多模型账户健康聚合的方案。实现、测试、运行态验证和发布门禁完成前，不能把本文当作已上线能力。文中“固定”参数不得被实现者替换为环境默认值、随机值或未记录的配置；需要调整必须先修改本文并重新走评审。

## 1. 决策摘要

账户自动健康状态继续由账户配置的主探测对象唯一决定：

```text
healthCheckModel + healthCheckEndpointMode
```

主探测对象由现有 J1 账户健康任务负责。它可以按现有健康契约更新 `accounts.status`、健康失败计数、账户冷却和账户恢复事实。非主模型的真实请求失败不能直接修改 `accounts.status`。

新增修复只增加一个窄的运行时保险丝：

```text
对非主模型建立 credentialSource + key + model + endpoint/mapping 的短暂屏蔽
```

该屏蔽只影响对应 API Key 和模型路由；其他 Key、其他模型、并发硬上限、配额、会话亲和、授权和蓝绿槽位继续走原有调度。为切断“同一 Key 在同一时间被 10 个请求一起打死”的失败风暴，新增的是精确 Key-模型的上游前置准入闸门，不是账户级并发上限，也不是把整个账户串行化。

恢复由独立 Go model-recovery 任务执行。它可以复用现有探测执行器的 HTTP、代理、TLS、凭据解密、超时、租约和结果完整性能力，但必须使用独立任务入口、状态作用域、Key 游标和恢复计数。恢复成功只能解除同一个 Key 的同一个模型路由屏蔽，不能清理账户状态，也不能清理其他 Key 或其他模型的屏蔽。

Node 必须做最小接入，因为当前网关掌握实际选中的 Key、客户端模型、最终映射和请求形态。Node 只负责生成精确失败事实、写入/投递短屏蔽和候选过滤；Go 负责定时恢复探测，不接管 Node 网关普通派发。

## 2. 当前事实与问题边界

### 2.1 账户健康

- `accounts.health_check_model` 和 `accounts.health_check_endpoint_mode` 是账户固定检查配置。
- J1 严格读取该配置，不因为最近哪个模型成功就改写检查模型。
- J1 多 API Key 探测可以轮换 Key，并记录成功的 winner Key；任意符合 J1 规则的 Key 成功，仍按现有账户健康语义决定账户结果。
- `accounts.status` 继续承载 `active`、`pending_test`、`temporary_unavailable`、`rate_limited`、`error` 和 `disabled` 等账户级状态。

权威背景见 [账号健康检测设计](账号健康检测设计.md)、[账户内 API Key 故障隔离设计](账户内APIKey故障隔离设计.md) 和 [J1 账号健康探活完整迁移契约](../migration/J1-账号健康探活完整迁移契约.md)。

### 2.2 调度

网关已有账户硬门禁、API Key 轮换、同账户有限重试、`protocol_model` 电路、恢复探针、会话亲和、代理避让、并发和配额排序。本修复不能替换候选选择器或重排整个调度链。新增逻辑分成两个相邻但不同的动作：在“请求模型、协议、映射和实际 Key 已确定、但尚未发送上游”时做精确 Key-模型准入；通过后仍由原有账户并发槽和后续派发链负责。若当前实现已经先取得账户并发槽，准入返回 `blocked/busy` 必须立即释放该槽，不能拿账户槽等待一个已经被短屏蔽或前置预算占满的 Key。

### 2.3 需要解决的场景

```text
账户配置 A、B、C，主探测模型 A
Key-1 的 B 失败
Key-2 的 B 可用
Key-3 的 C 可用
```

按 `account + model` 屏蔽会误阻断 Key-2 的 B；按账户状态处理会把 B 的问题扩大到 A、C；A 的一次探测成功又可能清除不相关的 B/C 失败。屏蔽必须下沉到实际物理 Key 和完整模型路由。

### 2.4 并发失败风暴

只在请求结束后写 `OPEN` 仍然不够：如果 10 个请求同时通过旧候选筛选并已经发送到同一 Key，第一条失败到达时，其余请求可能已经不可撤回。上游已发送的请求不能靠事后状态判断取消，也不能把 10 条已发生的失败伪装成一次成功。因此必须把保护边界前移到“发送上游之前”，并为每个精确 `CapabilityKey` 设置有限的**未提交请求预算**：同一 Key-模型最多允许 2 个尚未达到协议 `precommit` 的业务请求同时在途；其余请求立即改选其他 Key/账户，只有所有候选都被该闸门占用时，才在现有请求重试预算内等待闸门事件后重新枚举一次。

该预算只约束同一 `CapabilityKey` 的上游前置阶段，不改变账户已有 `concurrencyLimit`、线程池、配额或其他模型的吞吐。健康流式请求在首个有效协议帧达到 `precommit` 后释放闸门许可，长流仍由原有账户并发槽管理；非流式请求在完整成功或失败时释放。这样 10 并发遇到坏 Key 时，最多 2 个请求会实际进入该 Key 的前置失败窗口，其余请求不会继续把同一个坏 Key 打穿。

## 3. 不变边界

本轮不做以下事情：

1. 不新增替代 `accounts.status` 的账户健康状态机。
2. 不扫描全部配置模型，不把未使用模型推断为可用或不可用。
3. 不让非主模型失败写入 `accounts.status`、账户健康失败计数、账户冷却或主探测时间。
4. 不按 HTTP 状态码、错误码、错误类型、错误文案或正文选择屏蔽分支。
5. 不绕过 API Key 状态、授权、分组、到期、配额、并发、代理、会话亲和或蓝绿 owner fence。
6. 不把人工测试结果当作自动恢复事实。
7. 不把 `/v1/models` 目录存在性当成文本生成或图片生成成功。
8. Images 不默认进入定时恢复探测；只有显式 `allowModelRecoveryProbe=true` 的图片路由才允许恢复探测，未显式开启时只接受真实业务成功事实。
9. 不让新 Key-模型屏蔽参与 `protocol_model` 到父账户的升级投票。

## 4. 精确作用域

### 4.1 主探测对象

主探测不是单独的模型名，而是：

```text
MainProbe = healthCheckModel + healthCheckEndpointMode + 当前有效映射
```

只有客户端模型、入口族、最终上游模型和请求形态全部匹配时，才属于主探测路由。主模型使用其他入口、映射或协议形态时，按非主路由处理。

### 4.2 物理凭据来源

- owner 账户使用自身账户作为 `credentialSourceAccountId`；
- 授权实例使用来源账户作为 `credentialSourceAccountId`；
- 授权实例自己的分组、额度、调度开关、到期、冷却和本地避让仍独立执行。

来源账户和授权实例若最终使用相同 `credentialSourceAccountId + keyFingerprint + 模型/入口/映射 + dispatchRevision`，就是同一个物理 `CapabilityKey`，共享 foreground permit 和已确认的 `OPEN`，避免不同实例同时打穿同一上游连接。授权实例自己的分组、额度、状态、到期、代理或映射不同会先经过独立硬门禁；任一字段使最终 CapabilityKey 不同，就不能共享屏蔽。只有已经真实到达上游的 `upstream_not_complete` 才能跨实例写共享 `OPEN`，本地校验、候选为空、`busy`、取消或 `unknown` 不得广播。

### 4.3 Key-模型路由键

```text
CapabilityKey =
  credentialSourceAccountId
  + keyFingerprint
  + clientModel
  + clientEndpointFamily
  + finalUpstreamModel
  + upstreamEndpointMode
  + dispatchRevision
```

`keyFingerprint` 只使用现有脱敏指纹。不得放入 prompt、工具参数、采样参数、用户正文、token 或明文 Key。映射目标或入口不同，必须形成不同键；凭据、Base URL、协议、模型映射、Key 池或授权变化必须推进 `dispatchRevision`。

### 4.4 运行态 phase

| phase | 含义 | 普通派发 | 恢复任务 |
| --- | --- | --- | --- |
| `CLOSED` | 没有运行时屏蔽 | 允许 | 无 |
| `OPEN` | 短暂屏蔽 | 过滤该 Key-模型 | 等待 `retryAt` |
| `HALF_OPEN` | 正在恢复验证 | 仍过滤 | 允许一个 probe lease |
| `RECOVERING` | 已有成功但未稳定 | 仍过滤 | 继续按阈值验证 |
| `STALE` | revision 已变化的结果标记，不是持久 phase | 旧状态失效 | 丢弃旧结果 |

`key_model` 复用现有 circuit 的 phase、lease、generation 和 CAS 机制，但只持久化 `CLOSED`、`OPEN`、`HALF_OPEN`、`RECOVERING`；`SUSPECT` 不得写入新的 `key_model` scope。`STALE` 只表示被 revision/generation fence 拒绝的结果，不能写入 phase。不得建立第三套恢复状态机。`OPEN` 只是 Key-模型运行态，不是 `accounts.status=temporary_unavailable`。非主模型的首次真实失败直接进入 `OPEN`，不经过账户级 `SUSPECT` 或父级升级。

`key_model` 实际禁止使用 `SUSPECT`：如果底层共享 store 读到 `SUSPECT`，Node 按过滤处理，model-recovery 先将它按原 generation 收敛为 `OPEN` 或 `CLOSED`，不能把它作为新的账户确认阶段。

### 4.5 规范化与时间基准

- `clientModel` 和 `finalUpstreamModel` 保留供应商原始大小写，只去除首尾空白；不得自行转小写或别名化。
- `clientEndpointFamily`、`upstreamEndpointMode` 使用固定枚举原文；未知枚举拒绝建立 state。
- `dispatchRevision` 使用当前路由配置的单调 `int64`；配置、凭据、Base URL、协议 profile、模型映射、Key 集合或授权来源任一变化都必须递增。
- `capabilityHash` 是 canonical JSON（字段按字典序、UTF-8、无空白）经 SHA-256 得到的 64 位小写十六进制值。Redis state key、幂等键和指标内部关联只使用该 hash，不使用明文凭据。
- `retryAt`、lease 和过期判断统一使用 Redis `TIME` 返回的服务器时间；Node 与 Go 不使用各自本机墙钟作最终裁决。

### 4.6 首版覆盖范围

最终方案覆盖已有文本协议：OpenAI Chat、OpenAI Responses、Anthropic Messages、Gemini GenerateContent、Gemini Interactions，以及已经冻结模型映射的 Hybrid。每个入口按完整 `CapabilityKey` 隔离，恢复请求复用同一协议 profile 和 endpoint mode。Images 允许进入本契约，但自动恢复探测默认关闭；只有账户路由明确携带 `allowModelRecoveryProbe=true` 时才允许创建共享 `key_model OPEN` 并执行现有 `images_json` 最小图片探测。未开启时，图片 `upstream_not_complete` 只能创建当前实例既有的本地 30 秒避让，不创建共享 state；真实成功只清除该本地避让。该图片例外不属于本表的共享 Key-model 首次 OPEN 参数。未知协议、未冻结映射、无法构造最小探测请求的路由不建立共享 state，保持现有调度。

### 4.7 最终参数总表

以下参数是首版生产固定值，单位均为毫秒或数量；实现不得从账户配置、错误内容、HTTP 状态或随机数覆盖。参数变更必须先修改本文、更新验收基线，再发布新版本。

| 参数 | 固定值 | 作用 |
| --- | --- | --- |
| Key-model runtime guard | performance/dev Redis 模式直接启用；standalone 无 Redis 时不启用 | 不再提供运行时 kill switch；Redis-backed admission、短屏蔽和候选过滤随 performance/dev profile 生效 |
| 首次 `OPEN` | `5 秒` | 非主 Key-model 第一次真实失败后的最短屏蔽窗口 |
| 退避序列 | `5 秒 -> 15 秒 -> 1 分钟 -> 5 分钟` | 恢复失败后的下一次探测时间，第四次以后保持 5 分钟封顶 |
| `recoverySuccessThreshold` | `3` 次连续成功 | 防止一次偶然成功立即放行坏 Key-model |
| `probeTimeout` | `30 秒` | 单次最小恢复探测的完整墙钟上限；与 J1 的账户探测超时独立 |
| `probeLease` | `45 秒` | 探测 owner 租约；必须覆盖 30 秒探测并留出写回时间 |
| `leaseRenewInterval` | `10 秒` | 探测期间续租频率；续租失败立即停止请求 |
| `unknownRetry` | `10 秒` | `unknown` 或失租后的重试延迟；不增加退避级别 |
| `recoveringProbeInterval` | `10 秒` | 每次恢复成功后下一次连续验证的间隔 |
| `recoverySuccessMaxGap` | `2 分钟` | 两次实际成功证据的最大间隔；排队时不清零，下一次成功若超出则以该次成功重新计为 `1` |
| `recoveryContinuationStartSLO` | 到期后 `45 秒` 内开始 | `RECOVERING` continuation 的调度时限；超时告警但排队本身不修改成功计数 |
| `recoveryContinuationGlobalReserve` | `8`（包含在全局 32 内） | 为 `RECOVERING` continuation 保留的全局槽；无 continuation 时可借给普通 OPEN probe |
| `recoveryContinuationSourceReserve` | `1`（包含在同来源 2 内） | 为同来源 continuation 保留一个槽；无 continuation 时可借用 |
| `scanInterval` | `1 秒` | Go recovery 扫描到期 state 的周期；使首次 5 秒 OPEN 的正常发现延迟不超过约 1 秒 |
| `batchLimit` | `128` | 每轮最多取得的到期 Key-model 数，超出留到下一轮 |
| `globalProbeConcurrency` | `32` | 所有 model-recovery worker 的共享并发上限 |
| `sourceProbeConcurrency` | `2` | 同一 `credentialSourceAccountId` 的并发探测上限 |
| `perCapabilityLease` | `1` | 同一 CapabilityKey 同时最多一个 half-open probe |
| `maxResponseBytes` | `256 KiB` | 恢复探测响应读取上限；超限视为 `upstream_not_complete` |
| `stateCapacity` | `50,000` | `key_model` 活动态容量；只清理 CLOSED，不删除活动 OPEN/HALF_OPEN/RECOVERING |
| `closedRetention` | `5 分钟` | CLOSED 记录保留时间，用于幂等重放和前端最近状态读取 |
| `stateReadRetry` | `50 ms` 后重试 1 次 | Node 读取运行态的唯一重试；仍失败按不可选处理 |
| `requestIntentLimit` | `8` 个 CapabilityKey/请求 | 防止一次多账户重试扇出无限失败意图，超出只记录诊断 |
| `j1ConfirmationDebounce` | `2 分钟/来源账户/revision` | 非主模型失败触发 J1 主探测确认的限频；不创建第二个 J1 owner |
| `foregroundInFlightLimit` | `2` | 同一精确 CapabilityKey 尚未达到协议 `precommit` 的业务请求上限；不是账户并发上限 |
| `foregroundQueueWait` | `最多 1,200 ms`，且不得超过当前 request 的剩余 wall budget | 当前候选均被前置闸门占用时，只等待一次状态变化并重新枚举；不建立无界队列，也不读取账户并发重试环境变量 |
| `foregroundRedisOperationTimeout` | `100 ms/次`，使用同一 attemptId 在 `50 ms` 后最多重试 1 次 | 准入、失败封口和释放的热路径 Redis 上限；最坏等待不超过 `250 ms`，仍失败则精确 fail-closed 并记录原始错误；生产启用前必须验证 Redis p99 小于该上限 |
| `foregroundPrecommitLease` | `90 秒` | 前置 permit 的可续租故障自愈租约；正常请求在首帧/终态提前释放，进程崩溃后最多遗留 90 秒 |
| `foregroundLeaseRenewInterval` | `30 秒` | 尚未 `precommit` 时续租；续租失败立即取消该 upstream attempt 并按第 5 节结算，不能继续持有失去的 permit |
| `mainProbeFenceLease` | `J1 confirmation lease`（当前 `90 秒`） | **同一失败 MainProbe CapabilityKey** 等待 J1 结论期间的临时同波次准入栅栏；J1 在其他 Key 上成功不能清理它；不创建 `key_model` phase |
| `timeSource` | Redis `TIME` | Node、Go、lease 和 `retryAt` 统一使用共享服务端时间 |

J1 自身的 probe timeout、owner lease、现有账户健康阈值和现有 Key cursor 保持不变；本表中的 30 秒/45 秒 recovery 参数只属于 `model_recovery_probe`，不能反写或覆盖 J1 配置。所有时间均为墙钟时间，连续成功计数还必须满足同一 `generation`、同一 `dispatchRevision`。排队期间不根据墙钟清零；只有下一次真实成功完成后才比较成功间隔，超过 2 分钟时当前成功作为新序列的第 1 次。

参数选值依据固定如下：首次 OPEN 为 5 秒，配套 1 秒扫描后正常情况下约 5–6 秒进入恢复竞争；5 秒/15 秒/1 分钟/5 分钟让短抖动快速恢复，同时把永久故障探测封顶为每 5 分钟一次。最小恢复探测使用 30 秒 timeout、45 秒 lease 和 10 秒续租，不复制 J1 的账户探测参数。连续 3 次成功仍用于排除偶然成功，成功间隔改为 10 秒；`RECOVERING` 通过全局 8 个、同来源 1 个可借用保留槽和 45 秒启动 SLO 防止被大量新 OPEN 饿死。排队不是能力失败，不在等待过程中清零；只有新的成功证据与上次成功相隔超过 2 分钟时，才用当前成功重启计数。前置未提交预算固定为 2，保证同一时间最多两个请求进入同一 Key-模型的不可撤回窗口；前置等待硬封顶 1,200 ms 并受请求剩余墙钟预算限制。热路径 Redis 单次操作固定 100 ms、只允许一次幂等重试；foreground permit 使用 90 秒可续租租约、30 秒续租，避免长 TTL 在进程崩溃后卡住健康 Key。全局 32、同来源 2 是恢复专用并发上限；128 批量限制和 50,000 state 容量防止故障风暴产生无界任务。上述数值是 v1.2 固定契约，不能通过账户设置或隐藏环境变量覆盖。

### 4.8 参数关系与容量前提

参数必须作为一组实现，不能只改一个常量：

1. `scanInterval=1 秒` 不大于首次 OPEN 的五分之一，因此 5 秒到期后的正常扫描误差约为 0 至 1 秒；如果实际扫描延迟 p99 超过 1 秒，5 秒首轮目标不成立，必须先修复调度而不是缩短 OPEN。
2. `probeLease=45 秒` 必须始终大于 `probeTimeout=30 秒`，并留下 15 秒用于取消、结果校验和 CAS 写回；`leaseRenewInterval=10 秒` 小于 lease 的三分之一，单次续租抖动不会立即造成双 owner。
3. `recoverySuccessMaxGap=2 分钟` 覆盖 `recoveringProbeInterval 10 秒 + continuation 启动目标 45 秒 + probe timeout 30 秒 + 35 秒写回/调度余量`。只要 continuation 启动 SLO 成立，即使第二次探测跑满 30 秒也不会因调度等待清掉第一次成功。若实际成功间隔仍超过 2 分钟，说明探测或队列已经明显失去实时性；此时以当前成功重新计为 1 比使用陈旧成功直接放行更安全。
4. 以探测均跑满 30 秒估算，8 个全局 continuation 保留槽最多完成约 16 次 continuation/分钟；同来源 1 个保留槽最多完成约 2 次/分钟。每个 Key 从第一次成功到关闭还需要 2 次 continuation，所以最坏耗时下约支持全局 8 个 Key/分钟、单来源 1 个 Key/分钟完成稳定恢复。超过该到期速率时 45 秒是 SLO 而非数学保证，必须通过队列告警扩容或降低 OPEN 产生速率，不能放宽状态语义掩盖积压。
5. 5 分钟退避封顶意味着单个持续故障 CapabilityKey 稳态最多主动探测 12 次/小时；全局 32、同来源 2 和单 CapabilityKey lease 1 仍是最终保护，避免大量坏 Key 同时向同一上游施压。
6. `probeTimeout=30 秒` 只适用于最小恢复探测。UAT 必须证明健康文本探测 p99 不超过 20 秒且超时率低于 5%；达不到时不得启用该协议 lane。Images 只有能用 URL 等小响应完成协议验证且响应不超过 256 KiB 时才允许自动探测，可能返回大体积 base64 的路由继续保持 `allowModelRecoveryProbe=false`。
7. `foregroundPrecommitLease=90 秒` 不是业务请求超时；尚未 `precommit` 的健康 attempt 每 30 秒续租，正常首帧/终态即时释放，只有进程崩溃或续租失败才依赖 lease 回收。因此它可以短于现有请求 wall timeout，但续租失败必须取消 attempt，不能让已失去 permit 的请求继续发送。

以上容量是安全边界，不是吞吐承诺。上线后必须同时观察 `recovery_due_age`、`recovery_continuation_start_delay`、保留槽占用率、probe timeout 和 source 队列长度；任一 continuation 启动延迟连续 5 分钟超过 45 秒，禁止扩大灰度。

## 5. 不判断状态码和错误类型

### 5.1 统一观察

在新的 `key_model` 路径中，Node 失败记录和 Go model-recovery 只消费三类事实；J1 账户健康仍按既有 J1 outcome 契约运行，不受本节枚举替换：

```text
complete_success
upstream_not_complete
unknown
```

- `complete_success`：真实到达上游并完成当前协议要求的成功终态。
- `upstream_not_complete`：真实到达上游但没有完整成功；无论状态码、错误码、正文或文案是什么，均不再细分。
- `unknown`：没有可信上游结论，例如本地校验失败、尚未发出就取消、排队取消、进程关闭、任务异常或配置已经过期。

只有 `upstream_not_complete` 可以产生共享 Key-模型屏蔽；Images 未开启 `allowModelRecoveryProbe` 时按本节例外只产生本地避让。状态码、错误码、错误正文和 trace 仅用于日志、使用记录和人工诊断，不得进入任何新路径的业务分支。

### 5.2 请求失败动作

真实业务请求使用某个 `CapabilityKey` 但未完整成功时：

1. 本次请求立即排除该 Key-模型；
2. 如果不是 `MainProbe`，按单飞规则提名一个共享短屏蔽意图；
3. 当前请求仍可按现有策略尝试其他 Key、其他账户或后备分组；
4. 后续 Key 成功时请求可以成功，但不能立即清除失败 Key 的屏蔽；
5. `unknown` 不产生共享 Key-模型屏蔽。

同一 gateway 请求对同一 CapabilityKey 最多提交一次失败意图；单个请求最多提交 8 个不同 CapabilityKey 的失败意图，超过部分只保留诊断，不创建新 state。失败写入遇到 store 错误时，本次立即排除该 Key，不能假装已经建立共享 `OPEN`；后续请求按 state 读取结果处理。

如果多个账户都失败，也不能按账户数量或错误内容投票扩大屏蔽范围；每个实际发生的 CapabilityKey 单独处理。

## 6. 主探测与非主模型严格分流

### 6.1 主探测 A

```text
请求 A 且完整命中 MainProbe
  -> 只申请 foreground permit 吸收同波次并发，不写 Key-模型短屏蔽
  -> permit busy 时释放账户槽并按现有 Key 轮换/候选继续
  -> 首个真实失败建立 mainProbeFence，继续/复用 J1 主探测确认
  -> 按 J1 request_failure 规则投递主探测确认
  -> 账户状态仍只由 J1 决定
```

J1 用 A 探测账户。J1 成功或失败可以按既有契约更新账户状态，但不得清理 B/C 或其他 Key 的运行时屏蔽。

### 6.2 非主 B/C

请求 B、C，或者请求 A 但使用不同入口/映射时：真实失败创建对应 Key-模型屏蔽，并按来源账户和 `dispatchRevision` 每 2 分钟最多投递一次 J1 主探测确认；失败请求本身不写 `accounts.status`，由 Go model-recovery 按原路由恢复，账户是否异常仍只由 J1 的 A 探测结果决定。J1 已在运行时只加入原任务，不创建第二个 J1 owner。

### 6.3 禁止交叉写入

- A 成功不能关闭 B 的 `OPEN`；
- B 失败不能把账户写成 `temporary_unavailable`；
- C 恢复不能清理 A 的健康失败计数；
- 非主恢复不能修改 `healthCheckModel`；
- J1 cursor 与 model-recovery cursor 不得共用；
- 模型恢复证据不能累计到父账户 circuit。

## 7. 多 API Key 规则

多个 Key 可以对应不同模型能力，不能假设完全相同：

| Key | A | B | C |
| --- | --- | --- | --- |
| Key-1 | 可用 | `OPEN` | 未观测 |
| Key-2 | 可用 | 可用 | `OPEN` |
| Key-3 | `OPEN` | 无路由 | 可用 |

请求 B 允许 Key-2，不允许 Key-1；请求 C 允许 Key-3，不允许 Key-2。Key-1 的 B 失败不影响 Key-1 的 A/C，也不影响 Key-2 的 B。

当前请求继续复用现有 Key 选择、排除集合、安全尝试上限和同账户轮换。后续 Key 成功只证明后续 Key 的本次成功，不能恢复此前失败 Key。现有“同账户 Key 轮换成功后确认失败 Key”逻辑可以继续更新 Key 运行态，但不得把 Key-模型屏蔽升级为账户级屏蔽。

J1 主探测继续按账户级规则遍历允许探测的 Key：任一符合 J1 规则的 Key 成功，账户可以保持/恢复 `active`；所有可探测 Key 都没有完整成功，才按现有 J1 阈值和 CAS 更新账户状态。J1 不创建非主模型恢复记录，也不清理 B/C。

## 8. 屏蔽与恢复生命周期

### 8.1 创建与单飞

Node 提交的屏蔽意图必须带：

```text
CapabilityKey + requestId + attemptId + dispatchRevision + observedAt + sourceFence
```

同一 `CapabilityKey + dispatchRevision` 同时只能有一个活动 generation。首次 `upstream_not_complete` 立即创建 `OPEN` generation；同一 generation 后续失败只更新最近观察和退避，不新建 generation。1000 个相同失败只能合并一个恢复意图，不能扇出 1000 个探测。`requestId + attemptId` 是幂等键，重复提交只能返回原 mutation 结果。

### 8.2 退避

首版参数固定如下，不允许按账户、模型或错误内容动态改变：

```text
第 1 次 OPEN：5 秒
第 2 次恢复失败：15 秒
第 3 次恢复失败：1 分钟
第 4 次及以后：5 分钟（封顶）
```

`backoffAttempt` 从 1 开始，失败后按上表递增，最大保持 4；首次真实失败创建 `OPEN` 时使用 5 秒，后续**真实恢复探测**的 `upstream_not_complete` 按当前级别重新计算 `retryAt`，不重置退避。并发晚到的业务失败只追加观察和幂等回执，不得重新计算 `retryAt` 或提升级别。无随机 jitter，避免同一 Key 的恢复时间不可预测；不同 Key 由独立 lease 和批次分散执行。这是防止坏路由反复占用上游的临时保护，不是账户长期处罚。`dispatchRevision` 变化时旧屏蔽立即视为 `STALE`，新 revision 从 `CLOSED` 开始。

### 8.3 Go model-recovery

1. `retryAt` 到期后取得同一 CapabilityKey 的唯一 `half_open` lease；未取得 lease 的 worker 不发上游请求。处于 `RECOVERING` 且已到期的 continuation 优先于普通 `OPEN` probe；调度器不得让普通 probe 持续占满其保留容量。
2. 使用同一个 Key、客户端模型、入口、映射和 endpoint 发起最小探测；探测完整墙钟上限为 30 秒，租约为 45 秒，10 秒续租一次，租约必须覆盖探测和写回。
3. `complete_success` 只在 CAS 成功写回时更新 `lastRecoverySuccessAt=now`。若没有上一次成功时间，或 `now-lastRecoverySuccessAt <= recoverySuccessMaxGap`，则将 `recoverySuccessCount` 加 1；若实际成功间隔超过 2 分钟，则当前成功作为新序列第 1 次。排队、等待 lease、扫描延迟和 worker 未轮到都不清零计数，也不改写 `lastRecoverySuccessAt`。
4. `HALF_OPEN` 首次成功进入 `RECOVERING`；`RECOVERING` 在计数未达到 3 时按 `recoveringProbeInterval=10 秒` 安排下一次 continuation，达到 3 才回到 `CLOSED`。计数为 0 的 `CLOSED` 才表示可正常派发。
5. `upstream_not_complete` 将计数清零，回到 `OPEN` 并按固定退避延长 `retryAt`；这表示真实探测失败，不等同于排队超时。
6. `unknown` 或取消不改变能力结论；仍持有有效 lease 的 owner 释放 lease 后按探测前稳定态返回：原计数为 0 时回到 `OPEN`，原计数大于 0 时回到 `RECOVERING`，两者均设置 `retryAt=now+10 秒` 且不增加 `backoffAttempt`、不修改成功计数或成功时间。失租和 CAS 冲突的 worker 没有写回权，只丢弃本地结果，由当前 owner 的权威 state 决定后续；配置变化的结果标记 `STALE`，旧 generation/revision 不得写回。
7. 成功只清理同一个 Key-模型，不清理同账户其他 Key、其他模型或账户状态。

`recoveryContinuationGlobalReserve=8` 和 `recoveryContinuationSourceReserve=1` 是从全局 32、来源 2 中划出的可借用保留槽。没有到期 continuation 时，普通 `OPEN` probe 可以借用；一旦 continuation 到期，新的普通 probe 不得再占用该槽，已运行的普通 probe 不强杀，空闲后优先服务最老的 continuation。`recoveryContinuationStartSLO=45 秒` 是到期后的启动目标，不是把排队视为失败的计时器；若到期 continuation 因容量持续超过该目标，只产生 `recovery_continuation_slo_breach` 告警并按最老项优先，不能清零成功计数。该保留吞吐不能覆盖无限恢复积压，生产启用前必须用实际探测耗时验证来源级和全局级队列不会持续超标。

`lastRecoverySuccessAt` 只属于当前 `generation + dispatchRevision` 的成功序列：首次真实失败从 `CLOSED` 创建新 generation 时必须清空；达到 3 次成功转为 `CLOSED` 后也不把旧成功时间带入下一次 generation。它不能复用 `lastObservedAt`，也不能在扫描、排队、失租或状态读取时刷新。

恢复不能用 `healthCheckModel` 代替实际失败模型，也不能用 Key-2 的成功恢复 Key-1。

### 8.3.1 状态转换表

| 当前 phase | 事件 | 下一 phase | `backoffAttempt` | `recoverySuccessCount` |
| --- | --- | --- | --- | --- |
| `CLOSED` | 非主 `upstream_not_complete` | `OPEN` | `1` | `0` |
| `OPEN` | 未到 `retryAt` 的业务请求 | `OPEN` | 不变 | `0` |
| `OPEN` | 到期且取得 lease | `HALF_OPEN` | 不变 | 保留，首次为 `0` |
| `RECOVERING` | 到期且取得 lease | `HALF_OPEN` | 不变 | 保留 `1` 或 `2` |
| `HALF_OPEN` | `complete_success`，原计数 `0` | `RECOVERING` | 不变 | `1` |
| `HALF_OPEN` | `complete_success`，实际成功间隔 `<= 2 分钟`、原计数 `1` | `RECOVERING` | 不变 | `2` |
| `HALF_OPEN` | `complete_success`，实际成功间隔 `<= 2 分钟`、原计数 `2` | `CLOSED` | `0` | `0` |
| `HALF_OPEN` | `complete_success`，实际成功间隔 `> 2 分钟`（当前成功序列重启） | `RECOVERING` | 不变 | `1` |
| `HALF_OPEN` | `upstream_not_complete` | `OPEN` | 加 1，最大 `4` | `0`，清空成功时间 |
| `HALF_OPEN` | `unknown` 或取消，原计数 `0` | `OPEN` | 不变 | `0` |
| `HALF_OPEN` | `unknown` 或取消，原计数 `> 0` | `RECOVERING` | 不变 | 保留 |
| `HALF_OPEN` | 失租、CAS 冲突 | 权威 state 不变，本地结果 `STALE` | 不变 | 不变 |
| `OPEN/RECOVERING` | 尚未取得 lease 就取消 | 当前 phase 不变 | 不变 | 不变 |
| 任意活动 phase | revision 变化 | 旧结果 `STALE`，新 revision `CLOSED` | `0` | `0` |

只有 `CLOSED` 允许普通派发；`HALF_OPEN` 和 `RECOVERING` 即使探测成功一次仍继续过滤。`HALF_OPEN` 是持租执行态，必须保留进入它之前的 `recoverySuccessCount`，不能用 phase 切换隐式清零。任何状态转换必须同时校验 `capabilityHash + dispatchRevision + generation + leaseId`，缺一项即拒绝。

### 8.4 同一 Key 的并发失败风暴防护

#### 8.4.1 保护对象和锁顺序

并发保护对象是精确 `CapabilityKey` 的**未提交上游请求**，不是账户，不是 `credentialSourceAccountId`，也不是整个模型。Node 必须按下面的顺序执行，不能把账户并发槽拿来充当前置闸门：

```text
账户硬门禁/本地屏蔽/代理可用性
  -> 账户并发槽（沿用现有实现；仅在进入上游前短暂持有）
  -> prepareUpstreamAccount，确定最终 Key、模型、入口、映射和 endpoint
  -> foreground admission（同一 CapabilityKey 最多 2 个未提交请求）
  -> 上游发送
```

如果 `foreground admission` 返回 `busy`，Node 必须立即释放刚拿到的账户并发槽，不得在闸门等待期间占着账户槽；随后把当前 Key 加入本请求的临时排除集，按现有 Key 游标尝试同账户其他 Key，再按原有账户/分组候选顺序继续。只有所有候选都因闸门 `busy` 或既有并发容量暂不可用时，才复用 `foregroundQueueWait` 等待一次共享 Redis admission 事件并重新枚举；等待前和唤醒后都必须重读 state，不能依赖通知不丢失；不创建独立的无界请求队列。

这条顺序同时解决两个竞态：被 `OPEN` 的 Key 不会先消耗账户槽再被丢弃；同一账户的其他模型和其他 Key 仍可使用自己的账户并发预算。foreground 闸门与已有账户硬并发是两层约束，实际发送必须同时持有两者。

#### 8.4.2 原子准入和失败封口

`admitForeground(capabilityHash, dispatchRevision, attemptId)` 必须由 Redis Lua 在一次原子操作中完成：

1. 先校验 revision；`OPEN/HALF_OPEN/RECOVERING` 直接返回 `blocked`，不增加在途计数。
2. 清理已过 `foregroundPrecommitLease` 的遗留 permit；计数小于 2 才创建带 `attemptId` 的 permit 并返回 `admitted`，否则返回 `busy`。
3. 同一个 `attemptId` 重复调用返回原结果，不能重复占用 permit。

业务请求在真实上游结果为 `upstream_not_complete` 时，必须在同一个 Redis 原子写路径里完成“当前 generation 的 `OPEN`（非 MainProbe）+ 按第 8.2 节计算 `retryAt`（首次为 `now+5s`）+ 新请求封口 + 唤醒等待者”。第一条失败赢得 generation；并发晚到的失败只追加观察/幂等回执，不得新建 generation、重置退避或重新放大计数。Lua 同时递增该 `CapabilityKey` 的 admission wake sequence，并向命名空间事件通道发布 `capabilityHash`；跨 Gateway 副本的等待者先重读 state，再按事件唤醒，丢通知时不得把请求永久挂起。permit 的释放仍须在 `finally` 做带 owner token 的幂等删除，并递增同一 wake sequence；失败写和 permit 释放的先后不允许让新的请求绕过 `OPEN`。Redis 写失败时只能释放本地 permit 并把当前 Key 排除，必须保留原始写错误，不能声称共享 `OPEN` 已建立。

已经发送上游的其他请求无法被事后取消；它们的响应仍按各自的真实结果结算。`OPEN` 只阻止新的准入，不伪造已发请求的结果，也不把晚到结果写入新的 generation。

#### 8.4.3 permit 释放和流式边界

- 非流式请求：在 `complete_success` 或 `upstream_not_complete/unknown` 终态释放 permit。
- 流式请求：在收到首个满足当前协议的有效帧并向客户端提交 `precommit` 后释放 permit；此后长流继续持有原有账户并发槽，但不再占用“失败风暴前置预算”。
- 首帧前超时、连接失败、响应无法形成有效协议帧均视为 `upstream_not_complete`（已真实到达上游）或 `unknown`（没有可信上游结论），按第 5 节结算；不能因为 permit 已释放而吞掉后续真实失败。
- permit 释放必须放在 `finally`，并带 owner token；重复释放是幂等操作。进程崩溃由 `foregroundPrecommitLease` 自然回收，不能依赖进程内 Map。
- 同一业务请求的 Key 轮换、兼容性重试或备用 upstream URL 每产生一次新的真实上游 attempt，都必须先结算并释放上一个 attempt 的 permit，再为新的完整 `CapabilityKey` 重新申请；未获准入的 attempt 不得写入 request attempt tracker 的“已发送”集合。

#### 8.4.4 10 并发的确定性行为

同一 `CapabilityKey` 收到 10 个同时请求时，最多 2 个请求能在同一时刻进入未提交上游窗口。其余请求不向这个 Key 发送请求：

```text
前 2 个：admitted -> 上游
其余 8 个：busy -> 释放账户槽 -> 改选其他 Key/账户
首个真实失败：原子 OPEN -> 唤醒等待者 -> 该 Key 后续全部 blocked
```

如果前 2 个请求在健康情况下很快提交首帧/完成，permit 会即时释放，等待者重新枚举后继续使用；因此不会把健康 Key 永久变成单线程。若只有这一个 Key 且它确实不可用，等待预算耗尽后返回现有的可重试容量/候选耗尽结果，不能再把 8 个请求送到已证实失败的上游。这个结果可能是“没有可用候选”，但不会制造 10 个同一上游失败，也不会把账户误标为异常。

#### 8.4.5 MainProbe 的特殊规则

完整命中 `MainProbe` 的请求也可以使用同一前置 permit 来吸收同波次并发，但**绝不创建 `key_model` phase**。首个主模型失败只为失败的完整 `CapabilityKey` 建立短暂的 `mainProbeFence`，并按现有 J1 confirmation lease 触发/复用一个 J1 主探测；J1 探测 owner 绕过该 fence。J1 返回后：

- J1 `complete_success` 且 winner Key 与 fence 的 `keyFingerprint` 相同：只清理该精确 `mainProbeFence`；winner 是其他 Key 时，其他 Key 的 fence 不得被清理，按 fence 剩余 TTL 保持临时避让；
- J1 失败：由现有 J1 账户状态和冷却契约决定，前置 fence 不越权写账户；
- J1 `unknown`、失租或 CAS 冲突：fence 延后 `unknownRetry` 后重新观察，不写 `accounts.status`。

该 fence 只是同一失败 Key 并发波次的入口协调，不是第二套账户健康状态机；账户状态仍只由 J1 决定，非主模型的 `key_model` 恢复也不能清理它。J1 的 winner Key 必须作为现有结果事实保留并参与 fence CAS，不能只返回账户成功而丢掉物理 Key 身份。

### 8.5 重启与多实例

phase、generation、`retryAt`、`lastRecoverySuccessAt` 和 probe lease 必须在跨进程共享运行时存储中。model-recovery worker 重启后，未过期 `OPEN` 仍过滤，过期的 45 秒 probe lease 可被新 worker 接管；model-recovery lease 每 10 秒续租一次，续租失败立即停止探测并将结果记为 `unknown`。J1 自身仍沿用现有 lease/续租参数；foreground permit 使用 90 秒租约并每 30 秒续租。旧结果因 generation/revision 不匹配失效。不能用进程内 Map 代替权威事实。

## 9. Job 与代码所有权

两个任务可以运行在同一个 Go `jobs` 进程，但必须分离目标、写权限、状态和游标：

| 项目 | J1 主探测 | model-recovery |
| --- | --- | --- |
| 目标 | `MainProbe` | 非主 `CapabilityKey` |
| 可写状态 | 账户健康、账户冷却 | Key-模型 phase |
| 可写 `accounts.status` | 可以 | 禁止 |
| Key cursor | `health_check` | `model_recovery:<capabilityHash>` |
| 任务来源 | `account_health_check` | `model_recovery_probe` |

model-recovery 首版固定运行参数：扫描周期 1 秒、每次最多取 128 个到期 state、全局最多 32 个并发探测、同一 `credentialSourceAccountId` 最多 2 个并发探测、单个 CapabilityKey 只能有 1 个 lease、单次探测超时 30 秒、lease 45 秒、10 秒续租、`RECOVERING` 全局保留 8 个可借用槽、同来源保留 1 个可借用槽、continuation 到期后 45 秒内启动为目标、单次响应上限 256 KiB。批次超限的 state 保留原 `retryAt`，下一轮继续处理；到期 `RECOVERING` continuation 优先于普通 OPEN probe，超过启动目标只告警不清成功计数；禁止无界创建 goroutine、队列或探测请求。J1 的现有调度参数不复制到 model-recovery，也不因本修复调整。

共享底层能力可以包括配置读取、凭据解封、Key 选择、映射解析、代理、TLS、HTTP/SSE、超时、取消、lease、CAS 和脱敏日志；model-recovery probe 不取得 foreground permit，避免恢复任务与业务请求互相挤占；不能共享会互相覆盖的 outcome、cursor、恢复计数或状态投影。

Node 网关负责捕获最终 Key/模型/入口、记录统一观察、提交 typed intent 和过滤候选。Go jobs 负责恢复 lease、精确探测、phase/generation/retryAt 和模型运行态投影。J1 继续由现有 Go owner 负责账户状态。`model_recovery_probe` 不得写 `accounts.status`、J1 cursor、J1 failure count 或账户 cooldown。

### 9.1 权威写入矩阵

| 对象 | Node gateway | Go J1 | Go model-recovery | 业务 DB projector |
| --- | --- | --- | --- | --- |
| `accounts.status`、账户 cooldown、J1 failure count | 只读 | 唯一写入者 | 禁止 | 只投影 |
| `key_model` `OPEN` intent | 唯一提交者 | 禁止 | 禁止直接凭空创建 | 只投影 |
| `key_model` `phase/retryAt/generation` | 只读/过滤 | 禁止 | 唯一写入者 | 只投影 |
| foreground permit / `mainProbeFence` | 唯一申请、释放和观察者 | J1 只可绕过 MainProbe fence | 不占用业务 permit；不得修改 | 不投影为账户状态 |
| J1 cursor `health_check` | 禁止 | 唯一写入者 | 禁止 | 禁止 |
| recovery cursor `model_recovery:<capabilityHash>` | 禁止 | 禁止 | 唯一写入者 | 禁止 |
| 管理 API `runtimeSuppression` | 只读查询 | 禁止 | 禁止 | 由共享 state 查询生成 |

“唯一写入者”是业务规则，不是进程数量限制；多副本通过共享 lease、owner token、generation 和 CAS 保证。同一个副本不得以另一角色兼任写入路径。

## 10. Node 调度接入点

顺序必须是：

1. API Key、路由策略、分组、授权和账户硬门禁；
2. 执行现有本地 suppression、transport circuit、代理/IP 避让；
3. 解析客户端模型、入口族、最终映射和上游 endpoint，并枚举账户 API Key；
4. 过滤对应 CapabilityKey 的 `OPEN/HALF_OPEN/RECOVERING`；
5. 执行现有配额、并发、会话亲和、质量和优先级排序；
6. 对最终选定的 CapabilityKey 执行 `admitForeground`；`blocked` 立即换候选，`busy` 释放本次已取得的账户并发槽后换候选；
7. 取得 foreground permit 后才派发上游；响应首个有效协议帧/终态或失败时幂等释放 permit。

Key-模型的 phase 过滤必须在选择最终 Key 前完成，但 foreground permit 必须在最终 Key 已确定后申请；不能提前把整个账户从候选窗口删除。某模型所有 Key 被过滤，或所有未过滤 Key 都返回 `busy` 时，只表示该账户对本次模型暂时没有可派发候选；继续走其他账户、后备分组或一次有界等待，不写账户 `temporary_unavailable`。`busy` 不算业务失败、不能写 `key_model OPEN`，也不能触发 J1。

运行时状态读取失败不能静默当作 `CLOSED`，也不能借机修改账户健康状态。Node 对同一 state store 只做 1 次 50 毫秒后重试；仍不可读时，对受影响的精确 Key-模型本次按不可选处理并尝试其他已通过硬门禁的 Key/账户，其他 Key、其他模型和账户状态不受影响。必须记录不可读事件并保留原始错误；不能把读取异常伪装成模型健康结论。performance/dev profile 的 Redis state 不可用时按 fail-closed 处理，standalone profile 不进入 Redis-backed guard。

## 11. 授权、映射和协议

- 来源账户已确认的 Key-模型 `OPEN` 可以保护同来源授权实例；授权实例自己的未确认失败只能本地避让。
- 源模型相同但映射目标不同，必须使用不同 CapabilityKey；映射变化通过 revision 使旧屏蔽失效。
- Chat、Responses、Messages、GenerateContent、Interactions、Images 按入口族和最终 endpoint 分开。
- 图片请求属于独立 lane。只有 `allowModelRecoveryProbe=true` 的图片路由允许自动恢复探测；未开启时只保留诊断，不伪造恢复成功。

## 12. 现有 `protocol_model` 电路

现有 Node `protocol_model` 电路有向父账户累计多 scope 证据的逻辑，不能直接承担新的 Key-模型短屏蔽。启用新路径前必须保证：

1. 新 `key_model` scope 不参与父账户升级；
2. 新 scope 的 `OPEN` 只被 Key-模型候选过滤消费；
3. 账户状态只由 J1 和明确账户级管理动作写入；
4. 旧 transport circuit 对连接、读取和 framing 仍按原语义工作；
5. 同一次非主失败不能同时触发旧父级升级和新 Key-模型处罚。

旧 incident 若缺少真实 Key、最终映射或 endpoint，不得猜测转换成新状态；按旧路径恢复/过期，新 revision 从空白或 `CLOSED` 开始。

## 13. 前端与监控

为了不新增复杂账户聚合状态，账户列表继续展示 J1 账户状态，但增加轻量运行时提示：

```text
账户状态：正常
运行时屏蔽：2 个 Key-模型路由
主探测：A
```

规则：

- `accounts.status` 仍表示主探测和账户硬状态；
- 非主屏蔽显示为“运行时屏蔽”，不显示成账户 `temporary_unavailable`；
- 计数只统计当前 revision 的活动 phase；
- 详情按需展示脱敏 Key 标识、模型、入口、最终上游模型、到期时间和最近探测结果；
- 不展示明文 Key、token、完整错误正文或用户 prompt；
- 主探测成功不能显示“所有模型均正常”；
- `foregroundBusy`、等待数和 permit 数是瞬时调度指标，不是账户异常；前端列表不得把一次 `busy` 显示为“账户故障”，详情页只在排障模式展示当前 admitted 数和最近等待耗时；
- 管理 API 必须按本节固定结构返回；在 API 发布前，结构化日志和指标必须先可用，但前端不得自行从错误记录推断状态。

健康监控并列显示“账户健康（J1 主探测）”和“运行时屏蔽（Key-模型）”，两者不合并。

并发风暴保护还必须提供以下结构化观测，不用于业务分支：`foreground_admit_total`、`foreground_busy_total`、`foreground_blocked_total`、`foreground_wait_ms`、`foreground_release_total`、`foreground_precommit_total`、`foreground_open_fence_total`、`foreground_permit_expired_total`。Prometheus 聚合指标只使用低基数标签 `lane`、`providerCode`、`outcome` 和 `reason`；`capabilityHash`、`dispatchRevision`、脱敏 `keyFingerprint` 只进入结构化日志、审计和按 hash 查询的详情接口，禁止作为常驻指标标签。告警至少包括：同一 hash 的 `busy` 持续 5 分钟且没有 `precommit`、permit 过期率超过 1%、以及 `foreground_busy` 导致的候选耗尽率较启用前基线上升超过 10%。日志必须能关联 `traceId + attemptId + capabilityHash`，但不得记录 Key 明文或用户内容。

管理 API 的固定返回结构为：

```json
{
  "status": "active",
  "mainProbe": { "model": "A", "endpointMode": "chat_json" },
  "runtimeSuppression": {
    "activeCount": 2,
    "phaseCounts": { "OPEN": 2, "HALF_OPEN": 0, "RECOVERING": 0 },
    "routes": []
  }
}
```

`routes` 只在详情接口返回，列表接口只返回 `activeCount` 和 `phaseCounts`；Key 只返回脱敏指纹，`lastOutcome` 只返回三态值，`retryAt` 返回 ISO-8601 时间。前端不能根据 `lastOutcome`、HTTP 状态或错误文本再次计算账户状态。

详情 `routes[]` 固定字段为 `capabilityHash`、`keyFingerprint`、`clientModel`、`clientEndpointFamily`、`finalUpstreamModel`、`upstreamEndpointMode`、`phase`、`retryAt`、`recoverySuccessCount`、`lastRecoverySuccessAt`、`lastOutcome`、`lastObservedAt`；不得返回 token、Base URL、完整 Key、请求正文或上游响应。

## 14. 存储与接口契约

最终实现复用现有 account circuit 的控制面接口、lease、generation、CAS 和 outbox 机制，但为 `key_model` 使用独立的 Redis 逻辑命名空间 `gateway-account-circuit-key-model` 和独立 50,000 条容量计数；它与现有 `gateway-account-circuit` 的账户/Key/`protocol_model` 容量互不争抢。foreground admission permit 和 `mainProbeFence` 只存短 TTL 的 Redis 计数/租约，不计入 50,000 条 `key_model` 状态容量，不写业务数据库；它们只允许在已持有现有账户并发槽的真实候选上创建，活跃数量因此受既有全局/账户并发上限约束，不得按任意请求参数无界建 Key。SQLite/PostgreSQL 业务库只接收现有 control-plane outbox 的脱敏投影，不作为 Node/Go 派发热路径的锁。新增明确的 `key_model` scope；旧 `protocol_model` 行不迁移、不复用。

逻辑 Redis 键固定为 `state:<capabilityHash>`、`due`、`lease:<capabilityHash>`、`receipt:<intentId>`、`admission:<capabilityHash>`、`admissionLease:<capabilityHash>:<attemptId>`、`admissionWake:<capabilityHash>`、`mainProbeFence:<capabilityHash>`、`admission-events` 和 `capacity`，统一经过现有 namespace 包装。`admission` 是短期前置 permit 计数，`admissionWake` 是单调 wake sequence，`admission-events` 是跨副本通知通道；通知可以丢，state 和 sequence 才是权威事实。`mainProbeFence` 使用独立 owner 标识，不写入 `key_model.phase`。这些对象都不是第三套健康状态机。Node 和 Go 必须访问同一组键；禁止再建进程内 Map、第二个 Redis 前缀或独立数据库。

`key_model` 的持久化主键为 `scopeKind=key_model + capabilityHash`，最多保留 50,000 个活动 scope；容量达到上限时，最旧的 `CLOSED` 记录先清理，仍不足则拒绝创建新屏蔽并将本次 Key 按不可选处理，不能删除 `OPEN/HALF_OPEN/RECOVERING`。关闭记录保留 5 分钟供幂等重放，之后由有界清理任务删除。

每条 `key_model` 记录固定包含：

```text
scopeKind, credentialSourceAccountId, keyFingerprint,
clientModel, clientEndpointFamily, finalUpstreamModel,
upstreamEndpointMode, capabilityHash, dispatchRevision, generation, phase,
backoffAttempt, retryAt, recoverySuccessCount, lastRecoverySuccessAt,
probeLease, lastObservedAt,
lastOutcome, createdAt, updatedAt, retainedUntil
```

Node 只提交 typed intent，不提交 SQL 或任意 patch。Go 只提交带 CapabilityKey、generation、revision、lease 和观察时间的恢复结果。所有写入必须做 capability 完整性校验、revision fence、单飞、幂等和 CAS；凭据明文、正文、token 和完整上游响应禁止落库。数据库迁移必须把 `account_circuit_incidents.scope_kind`、`failure_scope` 的允许值加入 `key_model`，并增加 `capability_hash`、`credential_source_account_id`、`client_endpoint_family`、`final_upstream_model`、`upstream_endpoint_mode` 字段及对应唯一约束；旧 `protocol_model` 约束和父级关系不改变。

Node 失败意图固定字段为 `intentId`、`requestId`、`attemptId`、`capabilityHash`、`dispatchRevision`、`observedAt`、`outcome=upstream_not_complete`、`sourceFence`；Go 结果固定字段为 `runId`、`capabilityHash`、`generation`、`dispatchRevision`、`leaseId`、`observedAt`、`outcome`、`recoverySuccessCount`、`lastRecoverySuccessAt`。任何缺字段、hash 不匹配、revision 过期、lease 不属于当前 owner 的写入均返回 `stale`，不做隐式补全。

## 15. 取消、配置变化和回滚

- 尚未发出上游请求就取消：`unknown`，不改 phase；
- 已发出但未完整成功：`upstream_not_complete`，按恢复规则处理；
- worker 失租、revision 变化或 CAS 冲突：旧结果 `stale`，不能覆盖新状态；
- `disabled` Key 不得由恢复任务自动复活；
- 回滚只关闭新 intent、过滤和 model-recovery owner，不改写 `accounts.status`，不与旧路径双写；
- 新运行态等待 TTL 或 revision 自然失效；清理任务每 5 分钟运行一次，每轮最多清理 1,000 条 CLOSED 记录。

### 15.1 统一失败返回与恢复开关

| 情况 | 本次请求 | 持久化动作 | 对账户状态 |
| --- | --- | --- | --- |
| 精确 state 为 `OPEN/HALF_OPEN/RECOVERING` | 跳过该 Key，尝试其他候选 | 不写 | 不影响 |
| foreground permit 为 `busy` | 释放账户槽，尝试同账户其他 Key 或其他候选；仅在全候选 busy 时有界等待 | 不写 | 不影响 |
| `mainProbeFence` 存在 | 跳过同波次 MainProbe 业务请求；J1 owner 可绕过并取得主探测结论 | 不写 `key_model` | 仍只由 J1 决定 |
| state store 读取失败 | 跳过该 Key，50 ms 后只重试一次 | 写入 `runtime_state_unavailable` 指标 | 不影响 |
| state store 写入失败 | 当前 Key 本地排除，继续其他候选 | 保留原始错误，不假装 `OPEN` | 不影响 |
| model-recovery 容量不足 | 不启动该 probe，保留 `retryAt` | 写 `recovery_capacity_exhausted` | 不影响 |
| Go owner/lease 丢失 | 终止当前 probe | 结果为 `unknown/stale` | 不影响 |
| standalone 或 Redis state 不可用 | 不进入 Redis-backed admission；保留原有本地调度并记录配置/运行时错误 | 不创建共享 `key_model` state | 不影响 |

本版本移除 `JUHE_AI_GATEWAY_KEY_MODEL_RUNTIME_GUARD_ENABLED` kill switch。performance/dev profile 必须配置 Redis state 并直接启用 admission、短屏蔽和精确恢复；standalone profile 没有 Redis state，保持原有无共享 state 的本地调度行为。启用前仍必须完成 shadow、UAT、单生产分组验收和 Node/Go/Redis 互操作 smoke，不得以配置开关替代验收。

## 16. 启用与灰度

启用前必须证明：共享 `key_model` 状态、admission permit、lease、generation、Node 捕获/过滤、Go 精确恢复和主/非主写权限已经闭环；普通请求失败可以按限频规则触发 J1 A 探测，但不能直接执行 `request_failure -> 整号 temporary_unavailable` 写入；`protocol_model` 父级升级已隔离；多 Key、授权、映射、多协议、取消、重启和蓝绿测试通过。所有承接流量的 Gateway replica 必须先升级到同一版本并指向同一 Redis namespace，确认没有旧副本绕过 `admitForeground` 后才能承接流量；灰度期间旧/新版本不能混接同一生产流量池。

上线门槛固定为以下全部满足：shadow 阶段 1 小时内 Node 计算的过滤候选与离线回放一致率 100%；UAT 连续 2 小时无主探测误屏蔽、无跨 Key 误屏蔽；单生产分组连续 24 小时 `runtime_state_unavailable` 为 0、`stale` 写入率低于 0.1%、恢复探测超时率低于 5%、候选耗尽率不高于启用前基线的 1.1 倍；Redis state、lease、outbox、Node 和 Go 互操作 smoke 全部通过。任一门槛失败，停止扩大流量并回滚版本，不以配置开关代替故障处置。

灰度顺序：

1. shadow：只记录“如果过滤会过滤谁”；
2. UAT：只启用非主文本模型，并执行 10 并发同一 CapabilityKey 的坏上游回放；
3. 单个生产分组：限制账户范围和恢复并发；
4. 观察完整屏蔽/恢复窗口；
5. 文本协议连续观察 24 小时且错误率、恢复延迟和候选耗尽均未恶化后，才允许显式开启图片探测；未知协议永远不进入本版自动恢复。

## 17. 验收矩阵

### 核心与多 Key

1. A/B/C 中主探测 A；B 在 Key-1 失败，Key-2 的 B 仍可调度。
2. Key-1 的 B `OPEN` 不影响 Key-1 的 A/C。
3. Key-2 的 C `OPEN` 不影响 Key-2 的 A/B。
4. B 的所有可用 Key 被屏蔽时，只过滤账户对 B 的候选，不改账户状态。
5. 完整命中 MainProbe 的 A 失败不创建 Key-模型屏蔽，只走 J1/现有账户健康确认。
6. A 使用其他入口或映射时，按非主路由处理。
7. A 成功不清 B/C；B/C 恢复不改账户健康。
8. 三个 Key 的模型集合不同，调度结果按矩阵正确过滤。
9. Key-1 的 B 失败、Key-2 的 B 成功时，Key-1 的 B 不立即恢复。
10. 恢复必须使用原失败 Key、模型、入口、映射和 endpoint。

### 结果、并发与一致性

11. 状态码、错误码、错误类型和正文不产生不同的屏蔽业务分支。
12. 本地校验失败、候选为空、并发拒绝、客户端取消和任务异常不产生共享屏蔽。
13. 1000 个相同失败只形成一个活动 generation/恢复意图。
14. 同一 Key-模型只允许一个 half-open lease。
15. 同一 CapabilityKey 10 并发时最多 2 个未提交请求进入上游；其余请求在发送前改选，不产生失败记录。
16. `busy` 请求释放已取得的账户并发槽，不在闸门等待期间占槽，不触发 J1 或 `key_model OPEN`。
17. 首个真实失败原子封口并唤醒等待者；后续等待请求不再发送到该 Key。
18. 流式首个有效帧 `precommit` 后释放 foreground permit，长流仍受原账户并发槽管理。
19. 跨 Gateway 副本丢失 admission 通知时，等待者能通过 state/sequence 重读退出或重新枚举，不永久挂起。
20. 连续成功阈值未达到时仍保持屏蔽；再次失败按退避延长。
21. 旧结果晚于新 revision 返回时被拒绝。
22. Node/Go 重启、双实例竞争和旧 lease 接管保持单 owner。
23. `disabled` Key 不被自动复活。

### 既有链路、展示和发布

24. 授权、来源状态、分组、额度、实例到期和账户硬状态仍是硬门禁。
25. Key 轮换、线程池、账户硬并发、配额、会话亲和、代理和质量排序不改变；foreground permit 仅是额外的精确前置约束。
26. `temporary_unavailable` 账户仍按现有账户门禁停止所有模型；模型 `OPEN` 不能反向恢复账户。
27. MainProbe 的同波次 fence 不写 `key_model`，J1 owner 可绕过 fence；J1 在其他 Key 上成功不能清理失败 Key 的 fence，账户状态仍只由 J1 决定。
28. 蓝绿 standby 不运行第二个 model-recovery owner。
29. Chat、Responses、Messages、GenerateContent、Images 和映射使用独立精确键。
30. 前端同时显示 J1 账户状态和 Key-模型运行时屏蔽，不把后者伪装成账户状态。
31. 未使用模型不会自动显示为不可用，日志不泄露凭据和用户内容。
32. 第一次恢复成功后，即使 continuation 在队列中等待 90 秒，成功计数和 `lastRecoverySuccessAt` 仍保持不变；下一次真实成功在 2 分钟内到达时递增，超过 2 分钟时以当前成功重新计为 1；大量 OPEN probe 持续到来时，RECOVERING continuation 按保留槽和最老优先，不得永久饥饿。

## 18. 自审结论

### 18.1 方案成立

- 保留现有账户状态含义，主探测仍是账户异常唯一来源。
- 非主失败在请求层快速隔离，Key 维度保留不同凭据能力不同的事实。
- 主探测与模型恢复共用底层探测能力但分离写权限，成功/失败不能互相清状态。
- 不依赖脆弱的状态码或错误码表。
- 过滤位于 Key 选择前的精确层，不替换并发、配额、会话、代理和蓝绿逻辑。
- 并发失败风暴在上游发送前被限制为每个精确 Key-模型最多 2 个未提交请求；`busy` 请求改选或有界等待，不占住账户槽。

### 18.2 残余风险

1. 单次真实失败可能造成最多 5 秒的首轮误屏蔽；固定的 5 秒首退避、1 秒扫描、单飞和同 Key 连续恢复将其限制在可接受范围内。恢复队列若持续超过保留吞吐，会拉长实际恢复时间，但不会被错误记为能力失败。
2. 主模型不同入口若只比较模型名会误分流，必须比较完整 MainProbe。
3. 现有 `protocol_model` 父级升级若未隔离，局部故障仍会穿透到账户级。
4. Go 与 Node 若不共享同一权威状态，会出现一边恢复、一边仍屏蔽；共享存储、generation、lease 和 revision 是硬门禁。
5. 前端账户主状态可能仍为正常，这是保留语义；必须用单独运行时提示表达局部屏蔽。
6. 每个 Key-模型固定 2 个未提交 permit 会限制“只有一个可用 Key 且响应很慢”的峰值并发；这是阻止 10 条不可撤回失败的必要取舍，必须用多 Key/多账户候选和首帧后释放来保持整体吞吐。

### 18.3 失败模式预案

| 失败模式 | 保护动作 | 恢复条件 |
| --- | --- | --- |
| Redis 不可读 | 精确 Key 本次 fail-closed，50 ms 后重试 1 次，不修改账户 | Redis 恢复且读取成功 |
| Redis 不可写 | 当前 Key 本地最长 5 秒短暂避让，保留写错误，不假装共享 `OPEN` | 下一次请求成功写入 intent |
| Go recovery 全部停止 | 现有 OPEN 继续过滤，不扩大新 OPEN；告警 | Go owner 重新取得任务 lease |
| Node 新过滤逻辑异常 | 停止承接新流量并回滚版本 | 修复并重新通过 shadow/UAT |
| 状态容量达到 50,000 | 只清理 CLOSED，活动 state 不删除 | 清理出容量后继续 |
| 路由 revision 变化 | 所有旧 state 标记 stale，新 revision 从 CLOSED 开始 | 新请求重新观察 |
| 单个来源账户探测过载 | 同来源并发硬上限 2，其他到期项顺延 | 当前探测释放 lease |

告警固定为：`runtime_state_unavailable > 0` 立即告警；`OPEN` 活动数连续 10 分钟增长且恢复成功率低于 50% 告警；任一 `RECOVERING` continuation 启动延迟连续 5 分钟超过 45 秒时触发 `recovery_continuation_slo_breach`；最老 due age 超过 2 分钟或任一来源队列持续占满保留槽时触发恢复容量告警；候选耗尽率超过启用前基线 1.1 倍告警；任意账户状态被 `model_recovery_probe` 写入立即告警并阻断发布。

### 18.4 实施顺序

实现顺序固定为：

```text
非主文本模型 + API Key 账户 + Chat/Responses
+ Node 失败记录和过滤 + Go jobs 恢复
+ 不修改前端账户硬状态
```

先用 shadow 和 UAT 证明文本 API Key 路径不影响主探测、其他模型和既有调度；随后按同一契约接入 OAuth、授权实例和已列明协议。Images 只有在显式开启 `allowModelRecoveryProbe=true` 后才接入自动恢复；未知协议永久不进入自动恢复。实现过程不得改变本契约中的状态、参数或写权限。

## 19. 完成定义

- 账户状态严格由主探测配置的 J1 结果决定；
- 非主失败只建立对应 Key、模型、入口和映射的短暂屏蔽；
- 一个 Key 的模型屏蔽不影响同账户其他 Key 或其他模型；
- 主探测成功不清非主屏蔽，非主恢复不改账户状态；
- 不存在按状态码、错误码或错误类型分支的屏蔽规则；
- Go 使用原失败 Key 和原模型路由，连续成功后才恢复；
- 单飞、lease、generation、revision 和重启接管在多实例下有效；
- 同一 Key-模型的 10 并发不会在首个失败已可见后继续全部打向该 Key，前置 permit、失败封口和 bounded wait 的竞态回归通过；
- 账户、Key、授权、并发、线程池、会话、图片、多协议和蓝绿回归通过；
- 前端和监控明确区分账户健康与 Key-模型运行时屏蔽；
- 任一闭环未完成时，新路径保持关闭，不与旧路径双写。
