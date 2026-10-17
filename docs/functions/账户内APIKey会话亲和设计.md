# 账户内 API Key 会话亲和设计

> 设计状态：**已实施（2026-10-17，实现与设计契约一致，独立复审无 blocker）**。本文是行为契约的权威来源；当前行为与本文不一致时，按本文回正代码。

## 1. 背景与问题

`api_key` 类型 AI 账户支持配置多个上游 API Key，按 `credentials.api_key_strategy`（`failover` 主备 / `round_robin` 轮询 / `weighted_round_robin` 权重轮询）选择。账户维度的会话亲和已存在：会话黏住 AI 账户（`internal/gatewaysession`，Redis 绑定记录 + 进程内回退）。

但账户内 Key 选择目前每个请求独立按策略执行（`backend-go/projects/gateway/internal/gatewaydispatch/apikeyrotation.go` `selectAccountRuntimeApiKeyEntry`）：轮询与权重走**全账户共享的全局游标**（Redis 键 `juhe-ai:route-state:account-api-key:<strategy>:<base64url(accountId)>`，INCR + 30 天 TTL，`cmd/juhe-ai-gateway/chain_apikey_rotation_redis.go`）。同一会话的后续请求会随全局游标漂到不同 Key——会话只在账户维度黏住，不在 Key 维度黏住。

现行契约（[账户内 API Key 故障隔离设计](账户内APIKey故障隔离设计.md) §会话亲和）明确「会话亲和仍绑定到账户，不绑定到具体 Key……后续同会话的新请求按当时轮询/权重、共享 Key 状态和全局游标重新选择」。本设计将该契约变更为：**会话黏住账户内一把 Key；轮询/权重/主备策略只在黏不住（故障或不可用）时决策。**

当初不绑 Key 的理由是「避免同一个账户内 Key 状态变化导致会话亲和碎片化」。本设计用「指纹标识 + 优雅回退」消除该顾虑：Key 被删除/替换后指纹失配，视同无黏性按策略重选并重绑，不对任何一把 Key 的存在性做硬依赖。

## 2. 目标与非目标

### 目标

1. 在已有账户亲和绑定且账户 Key 池隔离生效时，同一会话优先复用同一把上游 Key；候选须未禁用、未处于冷却、未被本请求排除。
2. 黏住 Key 失配时按现行策略重新选择；仅最终响应通过 `ProtocolValidatedSuccess=true` 后，才把该 Key 指纹绑定到账户记录。
3. 复用既有账户维度会话亲和的存储、TTL、隔离维度与 Redis/进程内双路径：绑定记录追加字段，不新增第二份绑定记录，不新增配置项或开关。

### 非目标

1. 不改变账户维度会话亲和与调度语义（排序提升、claim、流量迁移、高并发车道、失败 Forget）。
2. 不改变 Key 状态机（禁用/冷却/探测恢复）、请求内救回链路（`ContinueAfterFingerprint`）与 Key 级失败记账。
3. 不做 Key 级独立授权、独立分组、独立并发池（继承现行不做范围）。
4. 不改变前端表单与 `api_key_strategy` 配置面：策略配置项保留，语义从「每次请求的选择规则」变为「黏不住时的选择规则」。
5. 不为不支持 Key 池隔离的账户扩展 Key 级运行态（见 §5 生效边界）。
6. 不新增 Key 维度的迁移索引；流量迁移保持账户粒度。

## 3. 可见行为

前置：会话亲和键（`sessionAffinityKey`）已由 preauth 按既有链路解析；无受支持会话 Header 时 `sessionAffinityKey` 为空，本设计完全不启用，行为与现状一致（统一会话身份设计中的明确降级）。

| 场景 | 行为 |
| --- | --- |
| 会话首个请求命中账户 A，策略选中 Key #2 | 现有账户级 claim 可先记录账户 A；仅当最终响应满足 `ProtocolValidatedSuccess=true` 后，绑定记录才写入 Key #2 指纹（见 §6） |
| 同会话后续请求仍命中 A，Key #2 可用 | 直接使用 Key #2，不执行策略，不消费轮转计数器 |
| Key #2 本次尝试失败 | 本次 `dispatchSingleAccount` 后续轮次不重读亲和；按既有排除集、`ContinueAfterFingerprint` 和策略救回。最终响应通过协议成功判定后，才把最终 Key 指纹写回 |
| Key #2 被禁用，或进入冷却且仍有健康候选 | 黏性失配，按现行候选过滤及策略选择；最终协议成功后重绑 |
| 所有未排除 Key 都处于冷却 recovery 集 | 忽略黏性，完整沿用现行 recovery 规则（按池顺序返回首个 recovery 候选）；不以策略游标重排 recovery 集 |
| Key #2 被删除或替换（指纹不在当前 Key 池） | 黏性失配，等同无黏性按策略选择，成功后重绑 |
| 现有流程调用 `ForgetAsync` | 整条绑定（含指纹）删除；不新增 Forget 条件。取消、首字截止、未完成流/响应检查失败等已有清理路径照旧；不代表任意 4xx/5xx 或账户失败 |
| 无会话键 / 黏性读取失败 / 无有效指纹 | 无 Key 黏性，进入现行选择管线；读取失败按现有亲和读取规则告警并降级，不影响请求 |
| JSON 语法损坏或缺少有效 `accountId` | 沿用现有行为删除整条损坏绑定；仅新增指纹字段类型或格式无效时丢弃该字段、保留其余绑定 |
| 上游返回 2xx，但最终协议校验失败、正文不完整或请求被取消 | 不写入新的 Key 指纹；原账户级 `RememberAsync`/`ForgetAsync` 语义仍按现状运行 |
| 最终 Key 写回线性化时绑定已过期、仍缺失或属于其他账户 | 跳过 Key 写回，不创建绑定、不改变 owner；下一次请求按现行账户/Key 策略处理 |
| `ForgetAsync` 后、在途协议成功回调之前，同账户已重新建立绑定 | 回调可更新线性化时仍存在的同账户绑定；`ForgetAsync` 不作为在途成功回调的代次栅栏。若 `ForgetAsync` 在线性化顺序上晚于该回调，则删除整条绑定 |
| 单 Key 账户 | 行为不变（唯一可用 Key 恒被选中，黏性无额外效果） |
| 不支持 Key 池隔离的多 Key 账户（§5） | 行为不变（纯策略选择） |
| `failover` 策略 | 黏住主 Key；主 Key 失败按既有主备顺序接管并黏住接管 Key。主 Key 恢复后**已切换会话不回切**，直至接管 Key 也失效（黏性优先于回切） |
| `weighted_round_robin` / `round_robin` | 同会话流量不再按权重/轮转摊开；权重与轮转只在首选和故障重选时生效。这是本设计的预期行为变化 |

## 4. 绑定记录扩展

`internal/gatewaysession` 的 `SessionBinding` 与 Redis JSON 编码 `sessionBindingJSON` 追加可选字段：

```text
upstreamApiKeyFingerprint  // string，可省略
```

- 取值为 `HMAC-SHA256(key=JUHE_AI_SECRET, message=上游 Key 明文)` 的 64 位小写十六进制结果，与 `apiKeyFingerprint`、`account_api_key_runtime_states` 同源同值；不存明文。授权实例的指纹由来源账户真实凭据计算，但绑定仍按被派发的实例 `AccountID` 隔离。
- marshal 仅在非空时写入；parse 逐字段容错，只接受恰好 64 个 `[0-9a-f]` 字符，其他类型或格式丢弃该字段，保留合法的账户和 scope。整条 JSON 语法损坏或 `accountId` 非字符串仍沿用现有删除行为。
- 存量 JSON 缺少该可选字段时按无 Key 黏性处理。旧版本能解析未知 JSON 字段不代表其会执行 Key 黏性；混跑期间行为不保证一致，稳定语义以所有 Gateway 实例完成升级为前提。
- 所有共享 Redis 绑定的 Gateway 实例必须使用相同 `JUHE_AI_SECRET`；不同 secret 实例混跑不是受支持的稳定形态，发布编排必须先完成统一切换再恢复流量。协调轮换后，旧指纹自然失配；会话在下一次最终协议成功时写入新指纹。
- Redis Lua 脚本（redisdriver.go）以整个 rawValue 作为 CAS 期望值、对新字段透明；实施时核验脚本不解析 value 内部结构。
- 进程内路径同步携带：`gatewaysession` 服务的 `SessionBinding` TTLCache 条目天然携带；当前 `gatewaydispatch` 的 memory 组合使用 cmd 侧 `localSessionAffinity`（24h TTL map），该 map 条目也扩展指纹字段。
- 命名刻意区别于绑定 scope 里既有的 `scope.apiKeyId`——那是下游入口 Gateway API Key 的 ID（preflight.go:227-232，来自 `GatewayAPIKeyRow`），与上游 Key 无关。

生命周期跟随绑定记录本体，不独立设 TTL：Redis dispatch 绑定为 1 小时（`sessionAffinityTtlMs`）；当前 cmd memory dispatch map 为 24 小时。`AffinityService` 自身的 memory cache TTL 也是 1 小时，但当前 memory 配置下 `gatewaydispatch.SessionAffinityPort` 接入的是 cmd map，不是该 cache。Key 黏性读不刷新 TTL（Redis 读使用 `refreshTtl=false`，本地 map 读不改 deadline）。账户级已有操作保留各自现状：Redis 排序读与同 owner claim 会刷新 TTL；本地 map 的排序读与同 owner claim 不刷新 TTL，账户级 `RememberAsync` 会刷新 TTL。Key 指纹成功写入在两个驱动中都刷新绑定 TTL。

## 5. Key 选择管线插入点与生效边界

### 插入点

`Engine.SelectAccountApiKeyForDispatch` 的**非 fixed 分支**内。会话偏好必须在状态过滤后匹配，不能绕过账户级 fixed 选择、禁用状态或请求内排除：

```text
账户级 fixedFingerprint 分支（现状优先级及语义不变）
        -> 加载状态 -> 剔除 disabled -> 请求内排除 -> 分出健康候选与 cooldown recovery 候选
        -> 仅首轮普通选择：会话指纹命中健康候选 -> 直接选择，不走策略、不消费计数器
        -> 未命中：单候选直选 -> ContinueAfterFingerprint -> 策略（failover/weighted/rr）
        -> 健康候选为空时按现行 recovery 顺序返回首个候选，不应用黏性或轮转策略
```

- `SelectApiKeyOptions` 增加 `PreferredApiKeyFingerprint string`（空串 = 无黏性）。每次 `dispatchSingleAccount` 调用最多读取一次，只传给该调用的首轮普通 Key 选择；同一调用后续轮换不重读、不重复应用。
- 黏性只在健康候选中匹配：禁用、请求内已排除的 Key 不可命中；有健康候选时 cooldown Key 不可命中。健康候选为空时沿用当前 recovery 集按池顺序返回首项，黏性与策略均不改 recovery 次序。
- 既有 `fixedFingerprint` 分支（包括生效中的 `SameAccountRetry` 固定 Key）优先于会话黏性且语义不变；非生效 retry 路径按现有代码剥离 fixed 指纹后，首轮选择仍可命中会话黏性。后续轮次由排除集和 `ContinueAfterFingerprint` 处理。
- 黏住不调用 `APIKeyRotationCounter`。黏性失配后仍按现有“单候选 / continuation / 策略”路径；只有实际进入轮转策略分支才消费计数器。

### 调用点接线

- **黏性读**：每次 `dispatchSingleAccount` 调用最多读取一次，且仅在会话键非空、账户满足池隔离边界时读取。后续 Key 轮换不重读；新的 retry dispatch 按其既有调用边界重新评估。
- **成功写回**：保留 `attemptoutcomes.go` 的账户级 `RememberAsync` 时点和语义，不扩展为 Key 写回。`UpstreamDispatchResult` 携带延后 Key 写回回调；链面仅在 `ProtocolValidatedSuccess=true` 且不重试上游的最终成功分支调用。2xx 响应头、协议校验失败、正文不完整、取消及重试中的响应均不提交 Key 指纹。

### 端口扩展

`gatewaydispatch.SessionAffinityPort`（ports.go:588）：

```go
// 新增：会话黏住的上游 Key 指纹。绑定存在、AccountID 匹配且字段格式有效时返回；
// 无绑定、账户不匹配或读取失败均返回空串，读取失败须按既有读取失败路径告警。
StickyApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey, accountID string) string

// 新增：仅最终协议成功后写入/更新 Key 指纹；不得改变已有 accountID。
RememberApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey, accountID, upstreamApiKeyFingerprint string)
```

- `chainSessionAffinityDispatchPort` 接 `AffinityService` 新方法；Redis 读失败告警并返回空串，与既有亲和读取失败降级一致。
- `localSessionAffinity` 同步实现读写；读不刷新 24h deadline，Key 写入在互斥锁内执行。
- Sticky 只在 `binding.AccountID == accountID` 时返回指纹。Key 写入只在当前绑定账户相同的情况下改字段；不同时不抢占账户绑定。
- Redis Key 写入以最新 raw JSON 做全值 CAS。每轮先 GET；记录缺失或 `AccountID` 不匹配时立即跳过，不执行 CAS。记录存在且 owner 匹配时执行一次 set Lua，最多 2 轮；首轮 CAS 冲突后重读并重新校验 owner，owner 已变化或记录已删除则放弃；第二轮仍冲突则告警并跳过。CAS helper 必须接收该轮 GET 得到的 raw expected，不得在 helper 内隐式再 GET。读取/写入错误不改写已完成响应。set Lua 同时维护既有索引与 TTL；refresh Lua 只刷新 TTL，不能用于字段更新。
- 同账户更新只替换 `upstreamApiKeyFingerprint`，保留该轮 GET 到的最新记录的 `Scope` 和 `TrafficMigrationPreferred`；Key 写入从不创建绑定。账户级 `RememberAsync` 已在 2xx 响应头阶段建立或续期账户绑定。若 Key 写入线性化时记录缺失/过期或 owner 已改变，回调跳过。`ForgetAsync` 与成功回调按 Redis CAS 或本地互斥锁的原子顺序裁决：Forget 后仍缺失时不重建；若同账户 Claim 已先重建，成功回调可更新该现存绑定；Forget 若后提交则删除整条绑定。本设计不增加绑定代次字段，也不保证 Forget 能栅栏已在途的协议成功回调。

### 生效边界

会话黏性与现行 Key 池隔离同边界：`isAccountApiKeyPoolIsolationEnabled`（api_key 类型 + 受支持 provider/protocol + 去重后 Key 数 > 1，accountpreparation.go:368-389）。理由：Key 级运行态（禁用/冷却）只为池隔离账户维护，黏住一把无法感知状态的 Key 会绕过故障隔离。单 Key 账户与不受支持的多 Key 账户维持纯策略行为。OAuth 账户（非 `api_key` 类型）无 Key 池，天然不受影响。

## 6. Key 指纹写回契约

Key 指纹写回与账户级 `RememberAsync` 分离。账户级记忆仍在 `response.OK()` 时按现有语义运行；Key 指纹只在响应最终满足 `ProtocolValidatedSuccess=true` 且不再重试上游时提交。该判据包含 2xx、协议校验通过和完整响应终态；2xx 响应头本身不构成 Key 写回成功。失败请求不写入新指纹，但既有账户级 claim、remember、forget 或迁移仍可能改变/删除账户绑定。Key 写回只更新线性化时仍存在且 owner 匹配的绑定；记录缺失、过期或 owner 不匹配时跳过，绝不创建绑定。

`RememberApiKeyFingerprintAsync` 的规则：

1. `sessionAffinityKey` 或指纹为空时不写入；指纹格式必须为 64 位小写十六进制。
2. 当前记录不存在时不写入、不创建绑定；成功请求已有的账户级 `RememberAsync` 应负责账户绑定的创建或续期。
3. 当前记录的 `AccountID` 相同时，只更新指纹并保留最新记录的 `Scope` 与 `TrafficMigrationPreferred`；相同指纹仍刷新 TTL。
4. 当前记录属于其他账户时不覆盖，也不迁移账户所有权。
5. Redis 每次写入最多执行 2 轮 `GET + CAS EVAL`；每轮 GET 后若记录缺失或 owner 不匹配即停止。首次 CAS 冲突后重读并重新校验账户；第二次仍冲突则告警并跳过。Redis 错误也告警并跳过，不改写已完成的响应。
6. memory Key 写入在同一把互斥锁内校验当前未过期记录和账户，再更新指纹；记录缺失或 owner 不匹配时跳过。本地账户级 `ClaimAsync` 对已有 owner 返回原记录时必须保留 Key 指纹；因记录缺失/过期创建新 owner 时没有旧指纹可带入。`RememberAsync` 若更新同一 owner，必须保留已有 Key 指纹并更新 scope/TTL；若覆盖为不同 owner，必须清除旧 owner 的 Key 指纹。Redis 同 owner claim/remember 续期不得清除新增字段，owner 更换时不得携带旧指纹。

并发胜者定义为**最后成功提交的原子更新**：Redis 是最后成功提交 CAS 的指纹，memory 是最后在互斥锁内完成更新的指纹；都不是按协议响应完成时间排序。Redis CAS 冲突最多重试一次，仍冲突时当前写入放弃。账户 owner 变化与 Key 指纹更新竞争时，重读/加锁校验发现 owner 不匹配即放弃，旧账户请求不得覆盖新 owner。`ForgetAsync` 不栅栏在途成功回调；若 Forget 后同账户已重新 Claim，晚到的协议成功回调可更新该新绑定，最终仍按原子提交顺序裁决。

Redis `AffinityService` 的账户 claim 对已有绑定为 first-binder-wins；cmd `localSessionAffinity.RememberAsync` 的现有账户级写入则可覆盖 owner。两者差异属于现行账户亲和行为，本设计不统一它们。新增 Key 写入在两个驱动中都不主动更改 owner，并只为写入瞬间匹配的 `accountID` 更新指纹；memory 账户级写入需在同 owner 时保留指纹、owner 替换时清除旧指纹。因此不同驱动的账户 owner 结果仍由原流程决定，但都不得把旧 owner 的 Key 指纹带到新 owner。

## 7. 边界情况

| 情况 | 处理 |
| --- | --- |
| 黏住 Key 进入冷却 | 有健康候选时黏性失配；所有未排除 Key 都在 recovery 集时沿用池顺序首项，不强制策略轮转 |
| 绑定 JSON 损坏/解析失败 | JSON 语法错误或账户字段无效时沿用现有行为删除记录；新增指纹字段无效时只丢弃该字段 |
| memory 驱动（进程内回退） | `localSessionAffinity` 的 Key 黏性 TTL 为 24h，读不刷新 TTL；不同进程不共享。Redis 与 memory 的 Key 查找/同账户更新规则一致；Key 写回只更新存在且 owner 匹配的记录，不创建记录。账户 owner 规则和 TTL 仍按既有驱动行为 |
| 授权实例账户 | 指纹基于来源账户 Key 明文计算，黏性绑定仍挂在被派发的实例账户维度；两个授权实例可各自黏不同 Key，不冲突 |
| high_concurrency 分组 | 黏性发生在账户内 Key 选择，与分组排序车道无关，行为一致 |
| 账户切换到 B | 读取时必须校验当前绑定 `AccountID`；A 的指纹不能给 B 使用。Key 写入不改变 owner；owner 变更、Forget 和迁移仍由账户亲和原流程处理 |
| 流量迁移（`MigrateOpenAIAccountSessionAffinity`） | 两条迁移写回路径均按字段重建，指纹不随迁移保留；目标账户按其策略选择，并在协议成功后重新绑定。不得将迁移改为结构体整拷贝 |
| 请求内同账户换 Key 重试 | 同次 `dispatchSingleAccount` 只在首轮读一次 sticky；后续轮由排除集与 continuation 处理。生效中的 `SameAccountRetry` fixed 指纹优先；未生效 retry 按现有规则剥离 fixed 指纹后仍可命中 sticky |
| `JUHE_AI_SECRET` 协调轮换 | 所有共享 Redis 的 Gateway 先统一切换到同一新 secret；旧指纹与新计算结果不匹配，等同无 sticky，下一次协议成功后更新为新指纹。新旧 secret 实例并存不是受支持的稳定形态 |
| 旧版 Gateway 混跑 | 旧版不执行 Key 黏性；可选 JSON 字段保证新版本能读存量记录，不构成混合版本的行为兼容承诺 |

## 8. 验收标准

1. 建立 3 Key、`round_robin` 账户和一个受支持会话键：首个协议成功请求选择并绑定一把 Key；在该请求完成后采集轮转计数器基线，随后连续 20 个协议成功请求都命中同一 Key，计数器相对该基线增量必须为 0。以 mock 上游记录的 fixture Key ID 为 oracle，不依赖未确认的审计字段。
2. 同一账户使用两个不同 session key，各自完成 1 次首个协议成功请求和 20 次后续成功请求；分别断言每个键固定复用自己的指纹，删除/重绑 A 不改变 B。另用无会话键执行 6 个 `round_robin` 请求：3 把健康 Key 各命中 2 次，计数器增量恰为 6，亲和读写次数均为 0。
3. 对仍在当前 Key 池中的黏住 Key，分别测试 disabled、cooldown（且保留至少 1 个健康 Key）、本请求排除三种状态；另单测从池删除/替换。各场景都不能命中旧指纹；最终协议成功后才绑定新 Key。删除/替换是池成员变化，不与 disabled/cooldown 状态叠加计数。
4. 将所有未排除 Key 置于 cooldown recovery 集：严格选择现行池顺序首项；sticky 不覆盖 recovery 规则，策略计数器不因 recovery 分支递增。
5. 以同一已绑定账户做 2xx 响应头后正文中断、协议校验失败和 `ProtocolValidatedSuccess=false` 三例：Key 指纹均不得新增或替换。正文中断/取消，以及非流式协议校验失败等既有响应失败路径，允许既有 `ForgetAsync` 删除整条绑定（Key 指纹随绑定同删，不得脱离绑定单独留存或替换）；此后下一请求按现行策略重新选择并在协议成功后写入新指纹。最终 `ProtocolValidatedSuccess=true` 才写回最终 Key。
6. disabled/冷却候选、单 Key 账户、不支持池隔离的多 Key 账户和 OAuth 账户覆盖边界；生效中的 `SameAccountRetry` fixed 指纹仍优先于 sticky。
7. 显式调用既有 `ForgetAsync` 后断言账户和 Key 指纹一并消失；流量迁移后目标记录不含来源 Key 指纹，目标账户首次成功后才写入新指纹。
8. 同一账户两个并发协议成功写入不同 Key：Redis 用 barrier 强制一次 CAS 冲突，断言至多 2 轮 CAS，最后成功提交 CAS 的指纹留存；第二轮仍冲突时保留当前记录并告警。memory 断言互斥区内最后完成的更新留存。另测 owner 在重试间切换时旧账户写入放弃；记录在 Key 写入线性化时被 `ForgetAsync` 删除或 TTL 过期后不得新建；若同账户已在回调前重新 Claim，则允许该回调更新新绑定；若 Forget 在线性化顺序上最后执行则断言绑定及指纹均删除。
9. Redis driver 与 memory driver 分别验证 Key 读/同账户写语义；Redis TTL 为 1h、本地 dispatch map TTL 为 24h，sticky read 不延长 TTL。账户 owner 的既有驱动差异不纳入一致性断言。
10. 旧 JSON 无新增字段、合法字段、非字符串字段、错误长度、非小写 hex、未知字段分别覆盖；只有新增字段无效时保留账户绑定。
11. `JUHE_AI_SECRET` 协调轮换后旧指纹失配；首次协议成功请求写入新指纹。多 Gateway 实例测试使用相同 secret；部署检查拒绝不同 secret 共享同一 Redis namespace，不测试不同 secret 混跑的一致性。

## 9. 验证方式

- **单元测试（Mock 优先）**：
  - `apikeyrotation_test.go` 覆盖 sticky 命中/失配（disabled、cooldown、请求排除、指纹不存在）、fixed 优先、recovery 全冷却规则和计数器调用次数。
  - `gatewaysession` 覆盖 JSON 字段格式/存量兼容、读账户校验、Redis GET 失败降级、TTL 不刷新、同账户 CAS 更新、异账户不覆盖、scope/迁移标记保留、2 次 CAS 上限和竞争失败；记录在写回线性化点缺失/过期时不创建，Forget 与回调竞态按 CAS 原子顺序断言。
  - `localSessionAffinity` 覆盖同 owner `ClaimAsync` 保留指纹、同 owner `RememberAsync` 保留指纹、owner 替换清除指纹、缺失记录写回跳过，以及并发更新串行化。
  - `gatewaydispatch` 覆盖 2xx 头之后不写 Key、final protocol success 才触发回调、失败/取消/重试不触发、最终选中 Key 写回。
  - `chain` 覆盖 `ProtocolValidatedSuccess` 的实际消费点，以及 `chainSessionAffinityDispatchPort` / `localSessionAffinity` 两种 driver 的适配。
- **回归**：账户亲和排序/claim/forget、Key 轮转、请求内救回、`SameAccountRetry`、流响应最终判定、失败派发清理、授权实例 Key 运行态归属等相关测试通过。
- **集成**：当前全链路 mock 验收执行 §8 的 1、2、3（401 与冷却两变体）和 5；其中冷却变体也验证验收 6 的 cooldown 边界。测试为隔离后端 + mock 多 Key 上游，只记录 fixture Key ID，不记录真实凭据；真实供应商联调不是必要条件。验收 1/2 的集成层计数器证据是间接的：每个会话执行 20 次黏性请求，再以 6 次无会话请求验证轮询从预期 Key 开始；直接断言黏性分支不调用计数器由 `TestSelectAccountAPIKeyStickyHitBypassesStrategy` 覆盖。本次未在集成层重跑完整 1-11 矩阵，其他验收按本节列出的单测、回归或部署检查验证。验收 4（全部未排除 Key 冷却）在新鲜隔离 fixture、仅由网关串行请求驱动且未通过管理面或 jobs 探针预置 Key 运行态时不可构造：同账户 Key 失败待确认项仅在协议成功时冲刷，且同一次结算先确认失败 Key、再记录成功 Key；全冷却 recovery 选择规则由 `TestSelectAccountAPIKeyStickyIgnoredWhenOnlyRecovery` 直接构造状态覆盖。

## 10. 实施时必须同步修订的文档

按「文档与代码同一次交付内同步」执行；以下为已核实的现行表述位置：

| 文档 | 位置与修订内容 |
| --- | --- |
| `docs/functions/账户内APIKey故障隔离设计.md` | 用 Key 级 sticky 契约替换 §会话亲和的账户级重选描述；补充策略只在 sticky 失配后执行；清理所有声称同会话每次按全局轮转游标重选的冲突表述。使用记录字段只作为排障信息，不作为本功能的必要实现或验收条件 |
| `frontend/src/views/accounts/AccountApiKeySection.vue` | 将轮询、权重帮助改为“无可复用会话 Key 时/会话 sticky 失配后按策略选择”；主备帮助明确切到备用 Key 后在 sticky 失效前不自动回切。只改帮助文案，不改表单字段；最终文案按本设计 §10 下方建议句执行 |
| `docs/develop/测试与验证说明.md` | 将“同 NAT 的健康会话仍按当前轮询/权重命中健康 Key”拆分为相同 `sessionAffinityKey` 固定已绑定 Key、不同会话键彼此隔离且各自按首选/失配规则运行；不再把“同 NAT”当作同一会话的充分条件 |
| `docs/functions/OpenAI账号接入.md` | 更新 Key 选择语义和会话亲和绑定粒度；一并修正“绑定只保存在进程内存中”的过时表述，写明 Redis 1h 与 memory dispatch map 24h 的实际边界 |
| `docs/functions/统一会话身份与审计串联设计.md` | 更新绑定粒度、隔离维度和验收，并明确 Key 字段只在 `ProtocolValidatedSuccess=true` 后写入；Key 指纹不是 session identity 的组成部分 |
| `docs/functions/README.md` | 本条目已在账户内 API Key 专题下索引；审核通过后状态更新为“设计已审核、待实施”，第 188 行的 Key 池专题归属规则加入本设计 |
| `docs/plans/` | 实施前按 plans/README 规范建立实施计划文档 |

前端帮助的建议口径：`轮询：首次选择或会话 Key 黏性失配后，按 Key 顺序轮流选择；命中会话黏性时复用上次协议成功的 Key。` `权重：首次选择或会话 Key 黏性失配后，按配置权重选择；命中会话黏性时复用上次协议成功的 Key。` `主备：首次按顺序选择；会话切换到备用 Key 后，在绑定失效前不自动回切主 Key。`

## 11. 影响面与风险

- **代码**：`internal/gatewaysession`（affinity.go / affinityredis.go / redisdriver.go）、`internal/gatewaydispatch`（ports.go / accountpreparation.go / apikeyrotation.go / attemptoutcomes.go / dispatchsingle.go / upstreamdispatch.go）、`cmd/juhe-ai-gateway`（chain_affinity_dispatch.go / chain_ports.go / chain_v1_loop.go）及对应测试。
- **行为风险**：weighted/rr 账户的同会话流量分布从摊开变为集中（本设计预期）；黏住可能让单会话流量集中于一把 Key，Key 级限流冷却由既有状态机承接（冷却即失配重选）。
- **存储**：每条紧凑 JSON 绑定新增字段的原始值增量为 **95 字节**：字段名 25 字节、字段名引号与冒号 3 字节、64 位十六进制值及两侧引号 66 字节，再加 JSON 成员分隔逗号 1 字节。该值不代表 Redis allocator/对象内存增量。仅对实际进入且满足池隔离的 `dispatchSingleAccount` 调用新增最多 1 次 sticky GET；同一请求若有 N 次这类调用，sticky 读数上限为 N。当前没有为此设计新增固定账户尝试数上限，N 定义为该请求中实际进入该边界的 `dispatchSingleAccount` 调用数。最终协议成功的 Key 写入若绑定仍存在且 owner 匹配，最多另加 2 次 GET + 2 次 CAS EVAL；因此每请求新逻辑上限为 `GET <= N + 2, EVAL <= 2`（不含原有账户排序、claim、remember 等 Redis 操作）。
- **兼容**：可选字段兼容缺少该字段的存量 JSON；不能据此承诺旧版 Gateway 执行新行为或混合版本稳定黏性。Node 归档实现不再运行，无跨实现兼容负担。

## 12. 已裁决的取舍

1. **最终协议成功后写 Key**：保留账户级 `RememberAsync` 的现有时点；Key 指纹只在 `ProtocolValidatedSuccess=true` 且不重试上游时提交。
2. **原子提交顺序胜出**：Redis 最多两轮 CAS，memory 单锁更新；最后成功提交的指纹留存，不承诺最后完成协议响应的请求胜出。`ForgetAsync` 不栅栏已在途成功回调；两者按原子提交顺序决定最终记录。
3. **不转移账户 owner**：Key 写入仅更新 owner 匹配的记录；账户绑定 owner 语义仍依 Redis/memory 的现有实现。
4. **指纹而非索引/明文**：Key 重排不影响指纹命中；Key 替换或 secret 协调轮换会失配并在下一次协议成功后重绑。
5. **不建 Key 级迁移索引**：流量迁移保持账户粒度，迁移字段重建时丢弃 Key 指纹。
6. **黏住不消费轮转计数器**；只在健康候选集合内匹配，所有 Key cooldown 时遵循 recovery 规则。
7. **生效边界跟随池隔离支持面**，不因黏性扩展 Key 级运行态的供应商范围；不依赖审计 metadata 才能验证。
8. **无开关**：实现即默认生效；黏性失配的回退路径即既有策略管线，无需功能开关。
