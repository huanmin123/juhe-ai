# AI 账户错误语义与状态变更边界

> 本文是 AI 账户切换、熔断、恢复和错误副作用的强约束文档。任何修改网关账户错误处理、API Key 轮换、账户运行态、后台探针或恢复状态机的代码，必须先阅读本文。

## 1. 不可信上游原则

上游供应商不是可信的协议语义来源。它可以用任意 HTTP 状态码、错误码、错误类型、错误文案或流式事件表达任意故障；网关不能假设 `401` 一定是凭据错误、`429` 一定是限流、`5xx` 一定是供应商故障，也不能假设响应正文符合供应商标准格式。

网关监控也必须保持同一边界：终态上游失败与网关自身失败使用有界 `failure_scope`（`upstream` / `gateway` / `none`）区分；不得以账户、模型、请求、错误正文或上游地址作为指标标签。系统可用性告警只统计 `gateway`，上游失败保留在审计和低基数观测中，不作为系统故障告警。

HTTP 状态码、错误码、错误类型和正文可以作为脱敏审计事实保存，但在没有用户显式策略、受控系统继承策略或独立探针证据时，不得把它们解释成账户、Key、代理、模型或供应商的健康事实。

## 2. 状态变更授权来源

账户、Key、代理、模型和供应商的**具体业务语义状态**只能由用户显式配置的账户错误策略，或本文件明确的受控系统继承策略授权变更。账户 `credentials.error_handling_rules` 命中后，可以按用户配置执行切号、限流、临时不可调用或异常动作；这是执行用户意图，不是系统猜测上游语义。当前唯一系统继承例外是 `system.upstream_insufficient_quota`：仅 HTTP `403` 且命中稳定额度错误码，或结构化错误消息命中高置信余额/额度文本时，才进入额度恢复状态。对支持该语义的 API Key，优先遵守已校验的供应商恢复时间；没有可靠时间时使用账户 `quota_recovery_policy` 的 duration 策略（默认 60 分钟并按全局被动策略每轮重新偏移）。OAuth / Google OAuth 不消费 API Key reset 字段，默认使用 UTC 每日策略，也可在账户策略中选择受限的 duration、daily 或 weekly 模式和 IANA 时区。系统规则仍优先于账户自定义规则。多 Key 只写失败 fingerprint，通用模式连续确认 30 天后该 Key 进入人工恢复的 `error`。裸 `403`、泛化 `quota`、权限或内容策略语义不得命中。该规则不写入 `credentials.error_handling_rules`、`system_settings` 或静态 system registry；`quota_recovery_policy` 仅保存账户可配置的恢复参数，授权实例只读继承有效投影。系统还可以依据受控的独立可用性探针写入通用 `temporary_unavailable`，但不得把探针返回的状态码、错误码或正文解释成限流、凭据失效、永久异常等具体原因。

传输电路是独立的、非语义状态机，只能记录连接失败、响应头未到达、读取中断、超时或完整 framing 等可观察传输事实。业务请求的首个传输失败只能形成待确认事实；账户级升级必须使用隔离的独立证据、租约和 CAS / generation，恢复必须由独立后台传输探针确认。传输电路不得维护 HTTP 状态码、错误码、错误类型或正文关键词白名单。

默认电路确认阈值是“首次传输失败 + 2 次独立 confirmation 失败”。同一会话、同一来源或同一 evidence 的并发重放不能自证账户死亡；`SUSPECT` 也不能依赖新的客户端流量才能继续确认，必须进入有界的后台 due 队列，由 single-flight 传输探针提供独立证据。完整 framing、task failure 和 unknown 分别只负责清除传输怀疑、保持中性或延后，不得伪造负向确认。unknown 必须保留当前 `SUSPECT` generation 和确认计数，并使用当前电路退避序列与全局被动偏移渐进延后；不得把 `retryAt` 重置为当前时间形成忙循环，也不得借 unknown 增加失败次数。

用户配置的路由首字截止和传输 hard timeout 是两类事实。`normalRoutingConfig.firstByteDeadlineMs`、速度优先 cutover、墙钟 handoff 等配置截止只回答“当前请求是否继续等待/换候选”，到期结果对账户电路、Key 运行态、共享质量和恢复副作用保持中性。只有建连失败、真实读取中断，以及 `textFirstResponseTimeoutSeconds` / lane hard lifetime 等传输层 hard timeout 才能作为 transport evidence；即使请求层根据配置截止主动取消了旧 attempt，也不得把该取消反写成 transport failure。

从子 `protocol_model` 电路升级为父 `account` 电路时，只按当前仍为 `OPEN` 的独立 scope 计数，不累计同一 scope 的重复失败或 confirmation 次数。独立 scope 阈值由 `JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_DISTINCT_SCOPE_THRESHOLD` 配置，默认 `3` 且硬下限为 `3`；滚动证据窗口由 `JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_WINDOW_MS` 配置，默认 `600000ms`。scope 必须同时包含 `accountRuntimeKey + protocolProfile + requestLane + modelBucket`，单模型、单 lane 或同坏会话风暴不得扩大为父账户死亡。父 `account` incident 与每个子 `protocol_model` incident 都必须按 `incidentId + generation + dispatchRevision` 持久化，并在冷启动时按父子关系重建；父 incident 的 `requiredRecoveryScopeKeys / childIncidentIds` 最多保留 64 个去重子 scope，超出部分仍可保留独立子 incident，但不得形成无界父 payload 或无界单次恢复 fan-out。

父 `account` scope 的关闭使用可配置的通用协议成功证据阈值，当前默认 `3`；不再要求 `requiredRecoveryScopeKeys` 中每个肇事子 scope 都再次获得流量或逐一成功。父级关闭只解除父 scope 的 shadow/阻断并清理本代父级升级账本，不能关闭、删除或重置仍处于 `OPEN / RECOVERING` 的子 incident；对应模型/lane 后续仍由各自子状态决定是否可派发。这样既避免无流量子模型让父账户永久卡死，也避免父级恢复掩盖局部持续故障。

电路 `dispatch_revision` 只属于上游传输身份：凭据、连接地址、代理绑定、协议和客户端兼容能力变化可以推进 revision。优先级、并发数、调度状态、时间表、模型目录和用户错误策略不得借配置更新清除活动电路；旧 numeric revision、同 revision 重放和迟到探针均必须被 CAS fencing。

网关普通业务请求的未知上游错误不能直接写 `accounts.status`、Key 持久状态、共享避让、代理健康、模型能力或供应商故障状态。请求侧电路记录“传输是否完成”不等于识别上游业务语义。

自动健康、质量、冷却和 Key 复测必须使用离散结果，并区分“传输电路”与“账户可用性确认”两个消费上下文：

1. `complete_success`：请求协议校验成功且 framing 完整，才是业务可用的正向证据；
2. `framing_complete_neutral`：完整 HTTP / SSE 已结束，但协议或业务未成功；对传输电路仍是中性并可关闭同来源 transport 怀疑，对使用固定健康模型和固定协议的独立账户可用性探针则是一次失败确认，可累计健康阈值或完成质量失败复核；
3. `upstream_failure`：只表示连接失败、传输 hard timeout、读取中断或 framing 未完成；它既是传输电路负向证据，也是独立账户可用性探针的失败结果；
4. `probe_task_failure`、`stale` 或其他 `unknown`：没有形成可归因的当前代上游 transport 事实，不计数、不改变账户/Key 状态，并按有界退避延后。

任意完整 HTTP 响应可以恢复“传输是否可完成”的电路，但不能据此清除用户显式策略产生的业务状态。业务状态的恢复来源必须和其创建来源匹配，不能用另一个无 provenance 的后台任务覆盖。

OAuth Access Token 刷新属于凭据生命周期，不是上游账户健康分类器。Token 端点返回任意非 `2xx`、错误正文、畸形 JSON、缺失字段，或发生网络、代理、timeout 等异常时，只能记录脱敏诊断并有界退避；不得按次数把账户写成 `error` 或永久退出刷新候选。本地可独立验证的凭据缺失、解密失败或配置无法装配可以进入各自明确的配置异常路径，但不能借远端返回内容推断账户死亡。刷新成功只能更新当前代 token，并按匹配 provenance 清理该刷新路径自己创建的状态，不能清理用户显式策略状态。

用户显式策略创建的 `temporary_unavailable`、`rate_limited` 或 `error` 必须持久保存创建来源、代次和观察边界。普通协议成功、后台健康成功、旧在途慢成功或没有匹配 provenance 的刷新成功都不得提前清除；只有 TTL 到期、同来源明确恢复动作、用户人工恢复，或该状态机文档明确授权的匹配恢复证据可以改变它。请求开始时间和完成时间都不能替代来源匹配，迟到结果必须受 generation / revision / observed-at fencing。

代理连通性检测同样只观察 transport。任意状态码的完整 HTTP framing 都证明本次代理链路可达，状态码只作诊断；只有连接、DNS、TLS、代理隧道、绝对 hard timeout、响应中断或 EOF 前 framing 未完成才能写代理检测 `failed`。没有可测试供应商、worker / 配置异常或预算到期前未形成真实请求时写 `unknown`，不得伪装成代理故障。代理检测结果是诊断元数据，不得因单次状态更新刷新无关的全局选路配置；迟到检测写回必须受代理配置 revision / generation 约束。

## 3. 允许的请求侧动作

未知上游响应必须先终止当前候选，允许的动作只有：

- 记录 attempt、审计、耗时和有界的原始状态、错误码与错误摘要；
- 完整非 `2xx` 在下游尚未提交时排除当前候选并继续账户/分组调度；只有候选耗尽后才返回网关统一错误；
- 没有完整 HTTP 响应的连接、读取中断和 timeout 返回可诊断的协议错误（连接/读取 `502`，timeout `504`）；
- 流已提交后按下游协议发送一个携带真实 `code/message` 的终止失败事件，协议无法表示时关闭连接。

已知 JSON 协议的非流式 `2xx` 必须在下游提交前完成完整的 JSON 与协议结构验证。上游伪造 `content-type`、返回畸形 JSON、结构缺字段，或正文超过固定验证窗口时，网关必须终止为 `502` 和 `upstream_protocol_error`；不得先透传未验证正文，再把该 attempt 记为成功。该限制不适用于本来就是二进制下载的未知协议路径。

账户 `credentials.error_handling_rules` 与受控系统继承策略的动作只决定当前账户状态副作用，不决定当前请求是否切号：`retry_next` 只切号，`rate_limited`、`temp_unschedulable`、`error_disabled` 在写入对应账户状态后同样排除当前账户并继续切号；未匹配任一策略的完整非 `2xx` 也按统一 `response.ok=false` 规则切号。`system.upstream_insufficient_quota` 命中后不投递低上下文 `request_failure` 探测，避免其与明确的 `rate_limited` 状态竞争。只有同账户兄弟 Key 轮换仍需要 `retry_next` 明确授权。

当前请求的 Key 排除必须使用请求内集合或等价的 request generation。未知 HTTP 响应不得写入跨请求共享的 `temporary_unavailable`、`rate_limited`、`error` 或其他带语义的 Key 避让状态。

未知 HTTP 响应或明确协议失败不得直接创建跨请求的客户端 IP × API Key × 账户健康状态，也不得直接降低共享账户质量。若请求已经解析出合法的 `clientSourceKey`，网关可以记录该来源的短 TTL 候选避让，并异步投递共享的独立账户健康探活；这不是账户健康状态或 transport circuit。来源键缺失、invalid 或 conflict 时不写来源避让。真实 gateway 请求每个请求最多选择一个账户投递独立健康检查；业务请求本身不能写账户状态。该二次确认成功时只清理匹配来源，确认 health failure 时才按专用阈值 1 写通用 `temporary_unavailable`；正常周期健康对完整真实诊断未通过的结果统一立即写入 `temporary_unavailable`，未形成可靠结论的任务失败、取消和 unknown 不改账户状态。

来源级短窗口候选避让统一以不可逆 `clientSourceKey` 为根键，而不是每个供应商各自拼接 Header：可信客户端 adapter 只提交自己的稳定证据，公共层再按系统账户、可信 `API Key ID`、来源类型与 semantic namespace HMAC。官方 Codex / Claude Code 会话是 `official_session`，Gemini 已存在 interaction resource 是 `protocol_resource`，完全没有细粒度证据且已知规范化 IP 时才可用 `clientIp + API Key ID` 的 `ip_api_key_fallback` 短 TTL 软桶。官方会话为 `invalid` 或 `conflict` 时必须停止，绝不可降级到 IP/API Key 桶；没有 IP 也不创建跨请求避让。来源状态还必须按客户端画像、endpoint 与下游协议隔离，持久 retry/CAS 分片再加入账户身份；会话亲和才按路由策略、分组和供应商协议档案隔离，因此来源避让会保留同一 API Key 在路由切换时的短期失败经验。原始 Header、interaction ID、请求 body/hash、User-Agent、显式 `x-juhe-client-profile`、客户端自报 `user` 和 request/trace ID 都不能进入持久键。

当前来源级避让由公共网关失败路径统一消费：Codex Responses SSE 或 `/responses/compact` 在公共来源作用域下再加入精确 `turnId` 子键，使同一 turn 下多个账户互不覆盖；Claude Code、Gemini 和通用协议请求直接使用公共来源作用域。后续供应商必须接入公共来源层，不能重新拼接外部字段或把协议资源冒充会话。每次 activation 只异步投递同一账户 runtime/config generation 的共享健康检查；control 拒绝、网络失败或 worker 入队失败按 fence 结算 `unknown`，保留短避让且不写账户或 circuit 状态。

该避让只重排同一 dispatch priority tier 内的候选，不能越过健康的高优先级或 fallback 层，也不能泄漏到其他来源、API Key、endpoint 或协议。它不改变账户、Key、账户 circuit 或共享账户可用性；同一 API Key 来源发生路由切换时允许连续。Codex `committed_retry_signal`、其他客户端专属 retryable 语义和普通 availability probe 都不是 transport 证据；账户级 `temporary_unavailable` 仍只能由用户显式策略或独立探活按既有阈值、配置版本和观察栅栏写入；系统额度规则对多 Key API Key 只写当前 Key 的 `rate_limited`，通用模式 30 天后才允许该 Key 进入 `error`，明确 reset 信号不得进入该终局。

可用性探活按 `account runtime scope + probe kind + config revision` 共享单飞。Redis runtime state 为多实例权威，memory 仅单机进程内回退；同一 generation 只有 owner 可实际执行，source-only joined 触发不得追加真实上游 probe 或 retry-queue follow-up。若同一 worker 执行中已存在普通 `request_failure`，它保留一个延迟尾随任务，待 foreign owner 租约结束后重新获取 generation；这是账户级确认，不能被 source-only joined 语义吞掉。Codex 来源 activation 把 HMAC `stateKey`、账户、`sourceGeneration`、runtime/config、`probeGeneration` 组成有界 source fence（单 generation 至多 64 条），随健康检查 dispatch/IPC 交给真实 worker；worker 完成后回传同一 fence，由持有 gateway 状态的进程只执行 `stateKey + accountId + sourceGeneration` 的精确清理。worker 不得依赖另一个进程的 memory Map，也不得按账户清 Codex 状态。

来源 owner 在健康任务明确入队后释放带截止时间的 handoff lease；未回传时 lease 到期可由新 source owner 接管，避免无限 joined waiter。owner token、lease 过期接管、completion/fence 原子边界和 generation fencing 保护结算：已 settled 的旧 generation 必须原子替换，新 activation 只能加入新 generation，不能消费旧 success；替换操作必须原子返回旧 fence 快照，旧 worker 与新 owner 任一方都可按旧 outcome 精确结算，不能让 fence 因替换丢失。投递拒绝、在途合并或释放栅栏失败均结算为 `probe_task_failure` / `unknown`，不会把尚未执行的任务假称成功。

source-only owner 的 `complete_success` 只清该 generation 中完全匹配的来源避让，绝不写账户健康、账户运行态或 circuit；`unknown`、任务失败、取消、stale 和 `framing_complete_neutral` 保留短避让且不写账户不可调度。确认的 health failure 才按既有自动探活分类、阈值和观察栅栏处理。若原本的 scheduled/quality/request-failure 健康任务正在执行，同一 generation 新附着 source fence 不会把该普通任务降格：普通账户健康副作用仍完整结算，同时 source fence 只得到自己的窄清理/保留结果。`temporary_unavailable` 仍只能通过既有 cooldown retest 恢复；系统额度 `rate_limited` 只有在 `cooldown_until` 到期后才进入实际复测。

完整 HTTP 非 `2xx` 不能被伪装成 `completed_response` 成功，也不能被改写为 `upstream_retryable_error` 或“未识别失败”。使用记录中的 `opaque_upstream` 只表达“没有匹配账户策略”，列表必须同时显示已知的 HTTP 状态、错误码和有界错误摘要；完整原始正文留在审计详情。完整响应表示传输 framing 已完成，不应据此确认传输电路失败。

“下游尚未提交”说明网关可以安全终止当前候选并尝试后备账户，但不授权同账户 Key 原地重放。图片、音频、文件/资源创建、后台任务、hosted tool 和普通文本都遵循同一规则：完整非 `2xx` 在语义提交前均排除当前候选并继续统一调度；正文中断、timeout 和已提交流仍按各自传输/语义边界处理。

图片 lane 继续使用独立长时限：默认首响应上限 600 秒、流式 idle 上限 120 秒、单次未提交 attempt 上限 3600 秒。图片时限到期只终止当前请求并返回 timeout，不再触发候选切换；已写出真实协议事件、正文或可见输出时只能结束或断开当前响应，不能拼接第二个候选的结果。

账户级候选切换是所有完整上游非 `2xx` 的统一请求级行为，仍必须受请求级去重、总墙钟和 attempt 预算约束；不得因失败产生重复 token、重复生成、重复外部副作用或重复计费。用户显式策略与 `system.upstream_insufficient_quota` 的 `cooldown`、`rate_limited`、`error_disabled` 等动作先写账户状态，再沿同一候选切换路径继续请求。

未知 transport failure 不再使用同一凭据原地重试 token。中间失败形成审计/使用记录事实，并可投递限频的独立健康检查，但不写电路 confirmation、共享质量、Key/账户状态、客户端 IP 避让、代理或上游桶；共享状态必须等待独立探针确认。

同一账户的多个 Key 只有在实际命中 `retry_next` 后才能在本请求内继续按池顺序选择；未知失败不得轮换兄弟 Key。跨账户重复的同一物理凭据在一个请求内只允许命中一次；显式重试仍受总墙钟和 attempt 预算约束。安全预算截断与真实 Key 池穷尽必须使用不同审计结果，均不得写成 Key 或账户死亡。

客户端重试是新的网关请求，不继承上次请求的失败游标。新请求必须基于当时的优先级、质量、速度、可用状态、全局轮换游标和恢复结果重新选择；因此高优先级账户已经恢复时可以重新选择高优先级账户，而不是机械延续到上次失败账户的下一个号码。

流式失败只允许在精确协议适配器中按协议声明的事件结构识别，例如 SSE 事件名 `event: error`、Responses 的 `response.failed` 或图片协议的 `image_generation.failed`。普通 SSE 事件的 `data` JSON 仅包含 `error`、`data.error`、`metadata.error`、`code` 或 `message` 字段时只是普通 payload，不能被猜测为失败终态；事件携带的 code、type、message 和 HTTP 状态同样不可信。结构失败只证明本次 attempt 失败，不授权共享账户状态或候选切换。下游尚未语义提交时返回当前失败；输出后必须保留已提交内容，并按协议补发包含错误摘要的终态或关闭连接，不能拼接第二个候选的结果。`generic_*` 客户端仍保持事件透明。

余额查询和人工诊断同样不得解释上游状态码。完整非 `2xx` 可以继续尝试其他余额 adapter，但不能清配置、修改首选 adapter 或改变账户调度状态；人工诊断按既定 10/20/30 秒档位记录真实结果，不产生网关共享状态副作用。

Key 冷却探针的 success、transport failure 和 neutral defer 都必须携带候选读取时的 status、next probe、updated-at、账户 config revision、fingerprint 和 secret 代次。SQLite / PostgreSQL 只能用 update-only CAS 回写；任何人工编辑、显式策略、凭据轮换或较新探针先完成后，迟到结果不得重新插入或覆盖状态。

多实例 Key 冷却探针必须先通过数据库原子 claim 领取带 token 的限时租约，success、transport failure 和 neutral defer 回写时同时校验 claim token 与完整代次。候选扫描应先有界过滤已失效 fingerprint，再领取当前 Key；旧 fingerprint 不能占满固定窗口并使当前 Key 永久饿死。租约到期可以由新 worker 接管，但旧 worker 的迟到结果必须被 CAS 拒绝。

电路运行态容量耗尽不能伪装成 `CLOSED`。Memory / Redis 必须返回共享的 capacity sentinel 并保守阻塞对应派发，容量释放后自动恢复；重建必须有单页、总时限、最大页数和 cursor 前进约束，并允许已完成权威查询的账户渐进服务。恢复扫描必须使用有界批次、有限并发、长期退避和确定性 jitter，不能对大账户池形成同步探针风暴。

## 4. 明确禁止的实现

下列实现一律视为越界，除非它们位于用户显式策略，或本文件定义的 `system.upstream_insufficient_quota` 严格匹配结果分支中：

- `statusCode === 401/403` 就标记凭据或账户失效；
- `statusCode === 429` 就标记限流或进入账户冷却；
- `statusCode >= 500` 就标记供应商、代理或账户不可用；
- 根据 `error.code`、`error.type`、正文关键词或流式事件名称推断 Key/账户死亡；
- generic 请求收到一个业务错误后，把当前 Key 的失败写入跨请求共享运行态；
- generic 请求收到一个业务错误后，直接写入跨请求客户端 IP 避让或账户持久状态；
- 根据请求类型、模型名称、HTTP 状态码、错误码或错误正文决定是否允许自动切号，形成多套准入规则；
- 已经向客户端交付真实协议事件、正文或可见输出后，再切换候选并拼接第二份结果；
- 把业务请求自身的 framing 完整失败直接写成账户/Key 不可用，或把独立探针的失败状态码解释成具体错误语义；
- 把上游返回的任意错误类别传给客户端，要求客户端承担内部账户状态决策。
- OAuth token 刷新连续收到远端异常后，把账户升级为永久 `error` 或停止所有自动恢复；
- 用普通协议成功、低上下文探测或旧在途成功，清除用户显式或系统继承策略创建且 provenance 不匹配的冷却、限流或硬错误；
- 把某个账户经同一代理发生的 request-local transport failure 写成代理级排除，连带跳过同代理的其他账户；

代码中的状态码只能用于协议边界、HTTP 响应转发、审计和显式规则匹配；出现 `401/429/5xx` 字面量并不自动违规，但必须能证明它不参与内部账户状态推断。

## 5. 电路容量、重建与恢复边界

- account circuit 的 memory / Redis 容量必须有硬上限并可配置；容量耗尽时不得把未记录的故障伪装成 `CLOSED`。运行态用共享容量哨兵把未知 scope 标为受控阻塞，只有活动 incident 关闭或容量提高后才自动解除。
- 冷启动全量重建必须同时受单页时限、总时限、最大页数和严格递增复合 cursor 约束。页失败、数据库挂起或容量不足都必须有界返回，并释放 `rebuilding`，允许下一轮重试。
- 全量重建未完成时，请求只能通过按账户权威查询渐进恢复。该查询必须一次返回并投影当前账户的父 incident、最多 64 个活动/恢复必需子 incident，以及当前 dispatch revision 下仍保留的 `CLOSED` ledger；不能按每条子 scope 再做无界查询。这样既避免先加载的旧 `OPEN` 卡住账户，也避免只重建父级而把子级误当 `CLOSED`。无法确认的账户继续阻塞，但不得连带阻塞已经确认完成的其他账户。
- 长期 OPEN 的退避上限为 15 分钟基线，并从第 5 档开始按全局被动策略每轮重新偏移；恢复 worker 使用可配置的有界 batch 和并发。Redis due 修复采用多次小 Lua 分页，单次 Lua 不得扫描整个容量。
- Redis 的账户 revision 清理必须使用有界 `HSCAN` 分页，不能在账户或 scope 数量增长后退化为 `HGETALL` 全量读取。全量重建暂未完成时，恢复 sweep 和待投影 control-plane 事件仍需继续处理已经加载的 scope，不能互相等待形成全局自锁。
- 低容量组的 FIFO 排队必须同时受组队列时限、服务器重试预算和请求墙钟约束。只有派发前发现当前分组没有可承接模型、额度和并发候选时，才允许按路由策略选择后续分组；已经发出上游请求后的失败不触发该 fallback。图片默认使用 600 秒首响应、120 秒 idle、3600 秒单次未提交 attempt 和 3600 秒整请求墙钟；单次时限到期即返回当前 timeout。
- recoverable waiter 的 `global` 容量是单个 Node 进程内的共享上限；多进程部署的聚合容量会按进程数放大。文档、指标和压测不得把它误写成跨进程全局配额，除非以后迁移到共享协调存储。

## 6. 代码审查与测试门槛

涉及本边界的变更至少要覆盖：

- 同一坏会话并发请求不会把多个正常账户或 Key 写成死亡；
- 任意状态码互换（例如把 `400`、`401`、`429`、`500`、`503` 互换）不会改变 generic 请求的内部状态动作；
- 用户显式策略或严格系统额度策略命中时，才会产生配置指定的持久状态；
- 普通协议成功、低上下文探测、旧在途成功和 OAuth 刷新成功不能清理 provenance 不匹配的用户显式或系统额度硬错误；匹配来源恢复、TTL 和人工恢复分别有 SQLite / PostgreSQL 并发 fencing 测试；
- OAuth token 端点的 `400/401/403/429/500/503`、错误正文、坏 JSON、缺字段、断连和 timeout 都只形成诊断与有界退避，不把账户写成永久 `error`；
- 代理检测对任意完整 HTTP 状态都只记录 transport reachable，连接/TLS/绝对 timeout/读取未完成才失败，无请求事实时为 `unknown`；
- 同一代理下账户 A 的 request-local transport failure 不得让账户 B 在本请求内被代理级排除；
- 请求内 Key 排除在新请求开始时清空，不跨请求污染；
- 同一客户端 IP 下的另一个会话不继承未知 HTTP 响应形成的账户避让；
- transport / timeout 与完整 HTTP 非 2xx 的电路事实互不混淆，完整 framing 不得推进传输电路失败；
- 透传和接管的完整 HTTP 非 `2xx` 都以 `opaque_upstream` 结束诊断 attempt，不降低共享质量；gateway 请求只允许投递去重限频的独立健康检查，不解释具体错误语义，也不直接写账户状态；
- 独立账户可用性探针的 `2xx-invalid-body`、任意 `4xx/5xx` 完整失败可以累计健康阈值或确认质量失败；传输电路仍只由真实 transport failure 推进；
- 图片 lane 的未知 HTTP、请求体已发送后的断连、timeout 和 `2xx` 正文中断都会在当前候选终止，并保留可诊断的失败事实；
- 图片在 `speed_first` 策略下不得创建文本首 token timer、慢样本或速度切号；直接图片和账户模型映射升级后的图片跨过文本 270 秒后失败时，仍在图片整请求墙钟内返回该次失败，不尝试健康后备候选；
- 音频、文件、资源创建、后台任务和 hosted tool 等请求与普通文本一样，未知失败不自动切换候选；
- 异步首字/读取切换决策返回前 attempt 已完成时，迟到决策不得再取消或重放；
- Redis 和 memory 的并发写入、迟到成功/失败、恢复租约和 generation 结果一致。
- 首次失败及两个独立 confirmation 才能 `OPEN`；同一坏会话高并发始终不能自证死亡，低流量 `SUSPECT` 可由后台探针最终恢复或打开。
- `SUSPECT` 探针 unknown 保持 generation/计数不变并渐进退避；大量 unknown 不得在 due 队列形成零间隔热循环，也不得推进 `OPEN`。
- 3 个、6 个和超大 Key 池按顺序唯一尝试；真实池穷尽、64-attempt 安全截断和总墙钟截断可区分且都不污染共享状态。
- 客户端下一次请求根据当时的质量、速度、优先级和恢复状态重新选号，不延续上次请求的候选游标。
- 普通事件中的 `error/code/message` 字段保持中性；只有协议声明的失败事件结构触发当前请求失败收尾，输出后稳定结束且不切号。
- 父账户升级阈值不能配置到 3 以下；两个 OPEN scope、同 scope 重复失败和伪造较大 failure count 均不得升级，第三个当前 OPEN 独立 scope 才能在配置窗口内升级。
- 电路满容量、数据库挂起、分页失败、重复 cursor 和 10k due scope 均有界，不能静默 fail-open 或永久全局 fail-closed。
- 容量耗尽后当前请求和下一请求都被识别为 `runtime_state_capacity_exhausted`，释放 CLOSED 后自动恢复；control-plane restore 不能越过容量上限；
- 重建的 DB hang、页失败、重复 cursor、持续递增 cursor、超大分页和局部账户渐进恢复均有 fake clock / Mock 覆盖；
- 冷重建同时覆盖父 incident、子 incident、`CLOSED` ledger、最多 64 个恢复子 scope、超限截断、旧 generation/revision 丢弃和父子恢复顺序；
- 10k 同时到期的长期 OPEN 不形成整分钟同步探针波次，且 recovery 并发不超过配置上限。
- 低容量真实 HTTP 风暴至少覆盖同一坏会话 64 路并发、多个 transport 故障账户、健康账户并发上限为 1、队列取消、槽位释放和恢复后重新选择高优先级账户；中间错误不得泄漏给客户端。
- 多实例 Key probe 至少覆盖 64 个旧 fingerprint 堵塞窗口、claim 互斥、租约过期接管、旧失败迟到和新成功恢复。

测试应断言状态转移和副作用，而不是只断言最终客户端 HTTP 状态。任何新增的错误分类器都必须证明它只用于诊断，或删除具体供应商语义分类。

相关实现：

- `backend/src/modules/gateway/response/failure-dispatch.ts`
- `backend/src/modules/gateway/response/upstream-failure-classifier.ts`
- `backend/src/modules/gateway/runtime/account-circuit.service.ts`
- `backend/src/modules/gateway/runtime/account-side-effects.service.ts`
- `backend/src/modules/gateway/runtime/account-api-key-failure-guard.service.ts`
- `backend/src/modules/background/account-circuit-recovery.service.ts`
