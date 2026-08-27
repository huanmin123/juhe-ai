# AI 账户主探测与 Key-模型短屏蔽修复设计

> 状态：最终实现契约 v1.0，代码尚未按本契约落地。
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

该屏蔽只影响对应 API Key 和模型路由；其他 Key、其他模型、主探测模型、并发、配额、会话亲和、授权和蓝绿槽位继续走原有调度。

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

网关已有账户硬门禁、API Key 轮换、同账户有限重试、`protocol_model` 电路、恢复探针、会话亲和、代理避让、并发和配额排序。本修复不能替换候选选择器或重排整个调度链，只能在“账户已通过硬门禁、请求模型和协议已确定、实际 Key 已确定”之后增加精确过滤层。

### 2.3 需要解决的场景

```text
账户配置 A、B、C，主探测模型 A
Key-1 的 B 失败
Key-2 的 B 可用
Key-3 的 C 可用
```

按 `account + model` 屏蔽会误阻断 Key-2 的 B；按账户状态处理会把 B 的问题扩大到 A、C；A 的一次探测成功又可能清除不相关的 B/C 失败。屏蔽必须下沉到实际物理 Key 和完整模型路由。

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

来源账户的已确认 Key-模型故障可以保护同来源授权实例不重复调用；未确认的单请求失败只能形成当前实例本地避让，不能跨实例扩大。跨实例共享前必须先由同一 CapabilityKey 的共享确认/Go 恢复探测写入权威 `OPEN`，不能把某个授权实例的单次失败直接广播给其他实例。

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

最终方案覆盖已有文本协议：OpenAI Chat、OpenAI Responses、Anthropic Messages、Gemini GenerateContent、Gemini Interactions，以及已经冻结模型映射的 Hybrid。每个入口按完整 `CapabilityKey` 隔离，恢复请求复用同一协议 profile 和 endpoint mode。Images 允许进入本契约，但自动恢复探测默认关闭；只有账户路由明确携带 `allowModelRecoveryProbe=true` 时才允许创建共享 `key_model OPEN` 并执行现有 `images_json` 最小图片探测。未开启时，图片 `upstream_not_complete` 只能创建当前实例的本地 30 秒避让，不创建共享 state；真实成功只清除该本地避让。未知协议、未冻结映射、无法构造最小探测请求的路由不建立共享 state，保持现有调度。

### 4.7 最终参数总表

以下参数是首版生产固定值，单位均为毫秒或数量；实现不得从账户配置、错误内容、HTTP 状态或随机数覆盖。参数变更必须先修改本文、更新验收基线，再发布新版本。

| 参数 | 固定值 | 作用 |
| --- | --- | --- |
| `guardEnabled` | `false`（发布验收后才改为 `true`） | 总开关；关闭时停止新屏蔽和候选过滤，但不删除已有 state、不改变账户状态 |
| 首次 `OPEN` | `30 秒` | 非主 Key-model 第一次真实失败后的最短屏蔽窗口 |
| 退避序列 | `30 秒 -> 2 分钟 -> 10 分钟 -> 30 分钟` | 恢复失败后的下一次探测时间，第四次以后保持 30 分钟 |
| `recoverySuccessThreshold` | `3` 次连续成功 | 防止一次偶然成功立即放行坏 Key-model |
| `probeTimeout` | `65 秒` | 单次恢复探测的完整墙钟上限 |
| `probeLease` | `90 秒` | 探测 owner 租约；必须覆盖 65 秒探测并留出写回时间 |
| `leaseRenewInterval` | `15 秒` | 探测期间续租频率；续租失败立即停止请求 |
| `unknownRetry` | `30 秒` | `unknown` 或失租后的重试延迟；不增加退避级别 |
| `recoveringProbeInterval` | `30 秒` | 每次恢复成功后下一次连续验证的间隔 |
| `recoverySuccessMaxGap` | `5 分钟` | 两次成功之间超过该间隔，连续成功计数归零 |
| `scanInterval` | `5 秒` | Go recovery 扫描到期 state 的周期 |
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
| `timeSource` | Redis `TIME` | Node、Go、lease 和 `retryAt` 统一使用共享服务端时间 |

J1 自身的 65 秒 probe timeout、90 秒 owner lease、现有账户健康阈值和现有 Key cursor 保持不变；本表中的 recovery 参数只属于 `model_recovery_probe`，不能反写或覆盖 J1 配置。所有时间均为墙钟时间，连续成功计数还必须满足同一 `generation`、同一 `dispatchRevision` 和 `successAt` 间隔不超过 5 分钟。

参数选值依据固定如下：30 秒首屏蔽足以切断当前坏 Key 的连续打击，但不会让临时网络抖动长时间消失；30 秒/2 分钟/10 分钟/30 分钟把探测压力从秒级拉开并设置明确上限；连续 3 次成功用于排除一次偶然成功；65 秒和 90 秒直接沿用现有 J1 探测与 owner lease，避免两套超时语义；5 秒扫描只决定发现到期 state 的延迟，不会缩短 30 秒屏蔽；全局 32、同来源 2 是恢复专用并发上限，确保恢复任务不会挤占网关业务连接和单一凭据来源；128 批量限制和 50,000 state 容量防止故障风暴产生无界任务。上述数值是 v1 固定契约，不能通过账户设置或隐藏环境变量覆盖。

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
  -> 不写 Key-模型短屏蔽
  -> 继续现有 Key 轮换和请求级处理
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
第 1 次 OPEN：30 秒
第 2 次恢复失败：2 分钟
第 3 次恢复失败：10 分钟
第 4 次及以后：30 分钟（封顶）
```

`backoffAttempt` 从 1 开始，失败后按上表递增，最大保持 4；每次 `upstream_not_complete` 都重新计算 `retryAt`，不重置退避。无随机 jitter，避免同一 Key 的恢复时间不可预测；不同 Key 由独立 lease 和批次分散执行。这是防止坏路由反复占用上游的临时保护，不是账户长期处罚。`dispatchRevision` 变化时旧屏蔽立即视为 `STALE`，新 revision 从 `CLOSED` 开始。

### 8.3 Go model-recovery

1. `retryAt` 到期后取得同一 CapabilityKey 的唯一 `half_open` lease；未取得 lease 的 worker 不发上游请求。
2. 使用同一个 Key、客户端模型、入口、映射和 endpoint 发起最小探测；探测超时固定为 65 秒，租约固定为 90 秒，租约必须覆盖探测超时。
3. `complete_success` 将 `recoverySuccessCount` 加 1 并进入 `RECOVERING`；连续 3 次成功才回到 `CLOSED`，计数为 0 的 `CLOSED` 才表示可正常派发。
4. `upstream_not_complete` 将计数清零，回到 `OPEN` 并按固定退避延长 `retryAt`。
5. `unknown`、取消、失租和 CAS 冲突不改变能力结论；`HALF_OPEN/RECOVERING` 收到这些结果时释放 lease 并回到 `OPEN`，`retryAt=now+30 秒`，不增加 `backoffAttempt`；配置变化的结果直接标记 `STALE` 并丢弃，旧 generation/revision 不得写回。
6. 成功只清理同一个 Key-模型，不清理同账户其他 Key、其他模型或账户状态。

恢复不能用 `healthCheckModel` 代替实际失败模型，也不能用 Key-2 的成功恢复 Key-1。

### 8.3.1 状态转换表

| 当前 phase | 事件 | 下一 phase | `backoffAttempt` | `recoverySuccessCount` |
| --- | --- | --- | --- | --- |
| `CLOSED` | 非主 `upstream_not_complete` | `OPEN` | `1` | `0` |
| `OPEN` | 未到 `retryAt` 的业务请求 | `OPEN` | 不变 | `0` |
| `OPEN` | 到期且取得 lease | `HALF_OPEN` | 不变 | `0` |
| `HALF_OPEN` | `complete_success` | `RECOVERING` | 不变 | `1` |
| `RECOVERING` | `complete_success` 且计数 `< 3` | `RECOVERING` | 不变 | 加 1 |
| `RECOVERING` | `complete_success` 且计数 `= 3` | `CLOSED` | `0` | `0` |
| `HALF_OPEN/RECOVERING` | `upstream_not_complete` | `OPEN` | 加 1，最大 `4` | `0` |
| `HALF_OPEN/RECOVERING` | `unknown`、取消、失租、CAS 冲突 | `OPEN` | 不变 | 不变 |
| `OPEN` | `unknown`、取消、失租、CAS 冲突 | `OPEN` | 不变 | `0` |
| 任意活动 phase | revision 变化 | 旧结果 `STALE`，新 revision `CLOSED` | `0` | `0` |

只有 `CLOSED` 允许普通派发；`HALF_OPEN` 和 `RECOVERING` 即使探测成功一次仍继续过滤。任何状态转换必须同时校验 `capabilityHash + dispatchRevision + generation + leaseId`，缺一项即拒绝。

### 8.4 重启与多实例

phase、generation、`retryAt` 和 probe lease 必须在跨进程共享运行时存储中。worker 重启后，未过期 `OPEN` 仍过滤，过期 lease 可被新 worker 接管；lease 续租每 15 秒一次，续租失败立即停止探测并将结果记为 `unknown`。旧结果因 generation/revision 不匹配失效。不能用进程内 Map 代替权威事实。

## 9. Job 与代码所有权

两个任务可以运行在同一个 Go `jobs` 进程，但必须分离目标、写权限、状态和游标：

| 项目 | J1 主探测 | model-recovery |
| --- | --- | --- |
| 目标 | `MainProbe` | 非主 `CapabilityKey` |
| 可写状态 | 账户健康、账户冷却 | Key-模型 phase |
| 可写 `accounts.status` | 可以 | 禁止 |
| Key cursor | `health_check` | `model_recovery:<capabilityHash>` |
| 任务来源 | `account_health_check` | `model_recovery_probe` |

model-recovery 首版固定运行参数：扫描周期 5 秒、每次最多取 128 个到期 state、全局最多 32 个并发探测、同一 `credentialSourceAccountId` 最多 2 个并发探测、单个 CapabilityKey 只能有 1 个 lease、单次探测超时 65 秒、lease 90 秒、单次响应上限 256 KiB。批次超限的 state 保留原 `retryAt`，下一轮继续处理；禁止无界创建 goroutine、队列或探测请求。J1 的现有调度参数不复制到 model-recovery，也不因本修复调整。

共享底层能力可以包括配置读取、凭据解封、Key 选择、映射解析、代理、TLS、HTTP/SSE、超时、取消、lease、CAS 和脱敏日志；不能共享会互相覆盖的 outcome、cursor、恢复计数或状态投影。

Node 网关负责捕获最终 Key/模型/入口、记录统一观察、提交 typed intent 和过滤候选。Go jobs 负责恢复 lease、精确探测、phase/generation/retryAt 和模型运行态投影。J1 继续由现有 Go owner 负责账户状态。`model_recovery_probe` 不得写 `accounts.status`、J1 cursor、J1 failure count 或账户 cooldown。

### 9.1 权威写入矩阵

| 对象 | Node gateway | Go J1 | Go model-recovery | 业务 DB projector |
| --- | --- | --- | --- | --- |
| `accounts.status`、账户 cooldown、J1 failure count | 只读 | 唯一写入者 | 禁止 | 只投影 |
| `key_model` `OPEN` intent | 唯一提交者 | 禁止 | 禁止直接凭空创建 | 只投影 |
| `key_model` `phase/retryAt/generation` | 只读/过滤 | 禁止 | 唯一写入者 | 只投影 |
| J1 cursor `health_check` | 禁止 | 唯一写入者 | 禁止 | 禁止 |
| recovery cursor `model_recovery:<capabilityHash>` | 禁止 | 禁止 | 唯一写入者 | 禁止 |
| 管理 API `runtimeSuppression` | 只读查询 | 禁止 | 禁止 | 由共享 state 查询生成 |

“唯一写入者”是业务规则，不是进程数量限制；多副本通过共享 lease、owner token、generation 和 CAS 保证。同一个副本不得以另一角色兼任写入路径。

## 10. Node 调度接入点

顺序必须是：

1. API Key、路由策略、分组、授权和账户硬门禁；
2. 解析客户端模型、入口族、最终映射和上游 endpoint；
3. 枚举账户 API Key；
4. 过滤对应 CapabilityKey 的 `OPEN/HALF_OPEN/RECOVERING`；
5. 执行现有本地 suppression、transport circuit、代理/IP 避让；
6. 执行现有配额、并发、会话亲和、质量和优先级排序；
7. 派发上游。

Key-模型过滤必须在选择 Key 前完成，但不能提前把整个账户从候选窗口删除。某模型所有 Key 被过滤时，只表示该账户对本次模型没有候选；继续走其他账户、后备分组或有界等待，不写账户 `temporary_unavailable`。

运行时状态读取失败不能静默当作 `CLOSED`，也不能借机修改账户健康状态。Node 对同一 state store 只做 1 次 50 毫秒后重试；仍不可读时，对受影响的精确 Key-模型本次按不可选处理并尝试其他已通过硬门禁的 Key/账户，其他 Key、其他模型和账户状态不受影响。必须记录不可读事件，并提供关闭新过滤入口的 kill switch；kill switch 只停止新屏蔽写入和候选过滤，不删除已有 state，也不停止 Go 恢复，恢复 owner 仍可自然清理。不能把读取异常伪装成模型健康结论。

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
- 管理 API 必须按本节固定结构返回；在 API 发布前，结构化日志和指标必须先可用，但前端不得自行从错误记录推断状态。

健康监控并列显示“账户健康（J1 主探测）”和“运行时屏蔽（Key-模型）”，两者不合并。

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

详情 `routes[]` 固定字段为 `capabilityHash`、`keyFingerprint`、`clientModel`、`clientEndpointFamily`、`finalUpstreamModel`、`upstreamEndpointMode`、`phase`、`retryAt`、`recoverySuccessCount`、`lastOutcome`、`lastObservedAt`；不得返回 token、Base URL、完整 Key、请求正文或上游响应。

## 14. 存储与接口契约

最终实现复用现有 account circuit 的控制面接口、lease、generation、CAS 和 outbox 机制，但为 `key_model` 使用独立的 Redis 逻辑命名空间 `gateway-account-circuit-key-model` 和独立 50,000 条容量计数；它与现有 `gateway-account-circuit` 的账户/Key/`protocol_model` 容量互不争抢。SQLite/PostgreSQL 业务库只接收现有 control-plane outbox 的脱敏投影，不作为 Node/Go 派发热路径的锁。新增明确的 `key_model` scope；旧 `protocol_model` 行不迁移、不复用。

逻辑 Redis 键固定为 `state:<capabilityHash>`、`due`、`lease:<capabilityHash>`、`receipt:<intentId>` 和 `capacity`，统一经过现有 namespace 包装。Node 和 Go 必须访问同一组键；禁止再建进程内 Map、第二个 Redis 前缀或独立数据库。

`key_model` 的持久化主键为 `scopeKind=key_model + capabilityHash`，最多保留 50,000 个活动 scope；容量达到上限时，最旧的 `CLOSED` 记录先清理，仍不足则拒绝创建新屏蔽并将本次 Key 按不可选处理，不能删除 `OPEN/HALF_OPEN/RECOVERING`。关闭记录保留 5 分钟供幂等重放，之后由有界清理任务删除。

每条 `key_model` 记录固定包含：

```text
scopeKind, credentialSourceAccountId, keyFingerprint,
clientModel, clientEndpointFamily, finalUpstreamModel,
upstreamEndpointMode, capabilityHash, dispatchRevision, generation, phase,
backoffAttempt, retryAt, recoverySuccessCount, probeLease, lastObservedAt,
lastOutcome, createdAt, updatedAt, retainedUntil
```

Node 只提交 typed intent，不提交 SQL 或任意 patch。Go 只提交带 CapabilityKey、generation、revision、lease 和观察时间的恢复结果。所有写入必须做 capability 完整性校验、revision fence、单飞、幂等和 CAS；凭据明文、正文、token 和完整上游响应禁止落库。数据库迁移必须把 `account_circuit_incidents.scope_kind`、`failure_scope` 的允许值加入 `key_model`，并增加 `capability_hash`、`credential_source_account_id`、`client_endpoint_family`、`final_upstream_model`、`upstream_endpoint_mode` 字段及对应唯一约束；旧 `protocol_model` 约束和父级关系不改变。

Node 失败意图固定字段为 `intentId`、`requestId`、`attemptId`、`capabilityHash`、`dispatchRevision`、`observedAt`、`outcome=upstream_not_complete`、`sourceFence`；Go 结果固定字段为 `runId`、`capabilityHash`、`generation`、`dispatchRevision`、`leaseId`、`observedAt`、`outcome`、`recoverySuccessCount`。任何缺字段、hash 不匹配、revision 过期、lease 不属于当前 owner 的写入均返回 `stale`，不做隐式补全。

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
| state store 读取失败 | 跳过该 Key，50 ms 后只重试一次 | 写入 `runtime_state_unavailable` 指标 | 不影响 |
| state store 写入失败 | 当前 Key 本地排除，继续其他候选 | 保留原始错误，不假装 `OPEN` | 不影响 |
| model-recovery 容量不足 | 不启动该 probe，保留 `retryAt` | 写 `recovery_capacity_exhausted` | 不影响 |
| Go owner/lease 丢失 | 终止当前 probe | 结果为 `unknown/stale` | 不影响 |
| kill switch 开启 | 不新建屏蔽、不消费新过滤 | 已有 state 只由 Go 恢复 | 不影响 |

kill switch 名称固定为 `JUHE_AI_GATEWAY_KEY_MODEL_RUNTIME_GUARD_ENABLED`，默认 `false`，只有完成 shadow、UAT 和单生产分组验收后改为 `true`。关闭时不删除数据、不清理账户状态、不启动备用 Node writer；恢复到旧调度只需将其改回 `false`。

## 16. 启用与灰度

启用前必须证明：共享 `key_model` 状态、lease、generation、Node 捕获/过滤、Go 精确恢复和主/非主写权限已经闭环；普通请求失败可以按限频规则触发 J1 A 探测，但不能直接执行 `request_failure -> 整号 temporary_unavailable` 写入；`protocol_model` 父级升级已隔离；多 Key、授权、映射、多协议、取消、重启和蓝绿测试通过。

上线门槛固定为以下全部满足：shadow 阶段 1 小时内 Node 计算的过滤候选与离线回放一致率 100%；UAT 连续 2 小时无主探测误屏蔽、无跨 Key 误屏蔽；单生产分组连续 24 小时 `runtime_state_unavailable` 为 0、`stale` 写入率低于 0.1%、恢复探测超时率低于 5%、候选耗尽率不高于启用前基线的 1.1 倍；Redis state、lease、outbox、Node 和 Go 互操作 smoke 全部通过。任一门槛失败，保持 kill switch `false`，不得扩大灰度。

灰度顺序：

1. shadow：只记录“如果过滤会过滤谁”；
2. UAT：只启用非主文本模型；
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
15. 连续成功阈值未达到时仍保持屏蔽；再次失败按退避延长。
16. 旧结果晚于新 revision 返回时被拒绝。
17. Node/Go 重启、双实例竞争和旧 lease 接管保持单 owner。
18. `disabled` Key 不被自动复活。

### 既有链路、展示和发布

19. 授权、来源状态、分组、额度、实例到期和账户硬状态仍是硬门禁。
20. Key 轮换、线程池、并发、配额、会话亲和、代理和质量排序不改变。
21. `temporary_unavailable` 账户仍按现有账户门禁停止所有模型；模型 `OPEN` 不能反向恢复账户。
22. 蓝绿 standby 不运行第二个 model-recovery owner。
23. Chat、Responses、Messages、GenerateContent、Images 和映射使用独立精确键。
24. 前端同时显示 J1 账户状态和 Key-模型运行时屏蔽，不把后者伪装成账户状态。
25. 未使用模型不会自动显示为不可用，日志不泄露凭据和用户内容。

## 18. 自审结论

### 18.1 方案成立

- 保留现有账户状态含义，主探测仍是账户异常唯一来源。
- 非主失败在请求层快速隔离，Key 维度保留不同凭据能力不同的事实。
- 主探测与模型恢复共用底层探测能力但分离写权限，成功/失败不能互相清状态。
- 不依赖脆弱的状态码或错误码表。
- 过滤位于 Key 选择前的精确层，不替换并发、配额、会话、代理和蓝绿逻辑。

### 18.2 残余风险

1. 单次真实失败可能造成最多 30 秒的误屏蔽；固定的 30 秒首退避、单飞和同 Key 连续恢复将其限制在可接受范围内。
2. 主模型不同入口若只比较模型名会误分流，必须比较完整 MainProbe。
3. 现有 `protocol_model` 父级升级若未隔离，局部故障仍会穿透到账户级。
4. Go 与 Node 若不共享同一权威状态，会出现一边恢复、一边仍屏蔽；共享存储、generation、lease 和 revision 是硬门禁。
5. 前端账户主状态可能仍为正常，这是保留语义；必须用单独运行时提示表达局部屏蔽。

### 18.3 失败模式预案

| 失败模式 | 保护动作 | 恢复条件 |
| --- | --- | --- |
| Redis 不可读 | 精确 Key 本次 fail-closed，50 ms 后重试 1 次，不修改账户 | Redis 恢复且读取成功 |
| Redis 不可写 | 当前 Key 本地 30 秒避让，保留写错误 | 下一次请求成功写入 intent |
| Go recovery 全部停止 | 现有 OPEN 继续过滤，不扩大新 OPEN；告警 | Go owner 重新取得任务 lease |
| Node 新过滤逻辑异常 | kill switch 关闭新写入和过滤 | 修复并重新通过 shadow/UAT |
| 状态容量达到 50,000 | 只清理 CLOSED，活动 state 不删除 | 清理出容量后继续 |
| 路由 revision 变化 | 所有旧 state 标记 stale，新 revision 从 CLOSED 开始 | 新请求重新观察 |
| 单个来源账户探测过载 | 同来源并发硬上限 2，其他到期项顺延 | 当前探测释放 lease |

告警固定为：`runtime_state_unavailable > 0` 立即告警；`OPEN` 活动数连续 10 分钟增长且恢复成功率低于 50% 告警；候选耗尽率超过启用前基线 1.1 倍告警；任意账户状态被 `model_recovery_probe` 写入立即告警并阻断发布。

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
- 账户、Key、授权、并发、线程池、会话、图片、多协议和蓝绿回归通过；
- 前端和监控明确区分账户健康与 Key-模型运行时屏蔽；
- 任一闭环未完成时，新路径保持关闭，不与旧路径双写。
