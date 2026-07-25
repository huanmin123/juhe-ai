# AI 账户错误语义与状态变更边界

> 本文是 AI 账户切换、熔断、恢复和错误副作用的强约束文档。任何修改网关账户错误处理、API Key 轮换、账户运行态、后台探针或恢复状态机的代码，必须先阅读本文。

## 1. 不可信上游原则

上游供应商不是可信的协议语义来源。它可以用任意 HTTP 状态码、错误码、错误类型、错误文案或流式事件表达任意故障；网关不能假设 `401` 一定是凭据错误、`429` 一定是限流、`5xx` 一定是供应商故障，也不能假设响应正文符合供应商标准格式。

HTTP 状态码、错误码、错误类型和正文可以作为脱敏审计事实保存，但在没有用户显式策略或独立探针证据时，不得把它们解释成账户、Key、代理、模型或供应商的健康事实。

## 2. 唯一的状态变更授权来源

账户、Key、代理、模型和供应商的**业务语义状态**只能由用户显式配置的账户错误策略授权变更。账户 `credentials.error_handling_rules` 命中后，可以按用户配置执行切号、限流、临时不可调用或异常动作；这是执行用户意图，不是系统猜测上游语义。

传输电路是独立的、非语义状态机，只能记录连接失败、响应头未到达、读取中断、超时或完整 framing 等可观察传输事实。业务请求的首个传输失败只能形成待确认事实；账户级升级必须使用隔离的独立证据、租约和 CAS / generation，恢复必须由独立后台传输探针确认。传输电路不得维护 HTTP 状态码、错误码、错误类型或正文关键词白名单。

默认电路确认阈值是“首次传输失败 + 2 次独立 confirmation 失败”。同一会话、同一来源或同一 evidence 的并发重放不能自证账户死亡；`SUSPECT` 也不能依赖新的客户端流量才能继续确认，必须进入有界的后台 due 队列，由 single-flight 传输探针提供独立证据。完整 framing、task failure 和 unknown 分别只负责清除传输怀疑、保持中性或延后，不得伪造负向确认。unknown 必须保留当前 `SUSPECT` generation 和确认计数，并使用当前电路退避序列与确定性 jitter 渐进延后；不得把 `retryAt` 重置为当前时间形成忙循环，也不得借 unknown 增加失败次数。

用户配置的路由首字截止和传输 hard timeout 是两类事实。`normalRoutingConfig.firstByteDeadlineMs`、速度优先 cutover、墙钟 handoff 等配置截止只回答“当前请求是否继续等待/换候选”，到期结果对账户电路、Key 运行态、共享质量和恢复副作用保持中性。只有建连失败、真实读取中断，以及 `textFirstResponseTimeoutSeconds` / lane hard lifetime 等传输层 hard timeout 才能作为 transport evidence；即使请求层根据配置截止主动取消了旧 attempt，也不得把该取消反写成 transport failure。

从子 `protocol_model` 电路升级为父 `account` 电路时，只按当前仍为 `OPEN` 的独立 scope 计数，不累计同一 scope 的重复失败或 confirmation 次数。独立 scope 阈值由 `JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_DISTINCT_SCOPE_THRESHOLD` 配置，默认 `3` 且硬下限为 `3`；滚动证据窗口由 `JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_WINDOW_MS` 配置，默认 `600000ms`。scope 必须同时包含 `accountRuntimeKey + protocolProfile + requestLane + modelBucket`，单模型、单 lane 或同坏会话风暴不得扩大为父账户死亡。父 `account` incident 与每个子 `protocol_model` incident 都必须按 `incidentId + generation + dispatchRevision` 持久化，并在冷启动时按父子关系重建；父 incident 的 `requiredRecoveryScopeKeys / childIncidentIds` 最多保留 64 个去重子 scope，超出部分仍可保留独立子 incident，但不得形成无界父 payload 或无界单次恢复 fan-out。

父 `account` scope 的关闭使用可配置的通用协议成功证据阈值，当前默认 `3`；不再要求 `requiredRecoveryScopeKeys` 中每个肇事子 scope 都再次获得流量或逐一成功。父级关闭只解除父 scope 的 shadow/阻断并清理本代父级升级账本，不能关闭、删除或重置仍处于 `OPEN / RECOVERING` 的子 incident；对应模型/lane 后续仍由各自子状态决定是否可派发。这样既避免无流量子模型让父账户永久卡死，也避免父级恢复掩盖局部持续故障。

电路 `dispatch_revision` 只属于上游传输身份：凭据、连接地址、代理绑定、协议和客户端兼容能力变化可以推进 revision。优先级、并发数、调度状态、时间表、模型目录和用户错误策略不得借配置更新清除活动电路；旧 numeric revision、同 revision 重放和迟到探针均必须被 CAS fencing。

网关普通业务请求的未知上游错误不能直接写 `accounts.status`、Key 持久状态、共享避让、代理健康、模型能力或供应商故障状态。请求侧电路记录“传输是否完成”不等于识别上游业务语义。

自动健康、质量、冷却和 Key 复测必须使用离散结果：

1. `complete_success`：请求协议校验成功且 framing 完整，才是业务可用的正向证据；
2. `framing_complete_neutral`：完整 HTTP / SSE 已结束，但协议或业务未成功，只记录诊断并有界顺延，最多关闭同来源 transport 怀疑，不得累计失败、启动 24 小时或 7 天窗口，也不得激活、恢复、冷却、禁用或异常化账户/Key；
3. `upstream_failure`：只表示连接失败、传输 hard timeout、读取中断或 framing 未完成，是自动探针唯一的负向状态证据；
4. `probe_task_failure`、`stale` 或其他 `unknown`：没有形成可归因的当前代上游 transport 事实，不计数、不改变账户/Key 状态，并按有界退避延后。

任意完整 HTTP 响应可以恢复“传输是否可完成”的电路，但不能据此清除用户显式策略产生的业务状态。业务状态的恢复来源必须和其创建来源匹配，不能用另一个无 provenance 的后台任务覆盖。

OAuth Access Token 刷新属于凭据生命周期，不是上游账户健康分类器。Token 端点返回任意非 `2xx`、错误正文、畸形 JSON、缺失字段，或发生网络、代理、timeout 等异常时，只能记录脱敏诊断并有界退避；不得按次数把账户写成 `error` 或永久退出刷新候选。本地可独立验证的凭据缺失、解密失败或配置无法装配可以进入各自明确的配置异常路径，但不能借远端返回内容推断账户死亡。刷新成功只能更新当前代 token，并按匹配 provenance 清理该刷新路径自己创建的状态，不能清理用户显式策略状态。

用户显式策略创建的 `temporary_unavailable`、`rate_limited` 或 `error` 必须持久保存创建来源、代次和观察边界。普通协议成功、后台健康成功、旧在途慢成功或没有匹配 provenance 的刷新成功都不得提前清除；只有 TTL 到期、同来源明确恢复动作、用户人工恢复，或该状态机文档明确授权的匹配恢复证据可以改变它。请求开始时间和完成时间都不能替代来源匹配，迟到结果必须受 generation / revision / observed-at fencing。

代理连通性检测同样只观察 transport。任意状态码的完整 HTTP framing 都证明本次代理链路可达，状态码只作诊断；只有连接、DNS、TLS、代理隧道、绝对 hard timeout、响应中断或 EOF 前 framing 未完成才能写代理检测 `failed`。没有可测试供应商、worker / 配置异常或预算到期前未形成真实请求时写 `unknown`，不得伪装成代理故障。代理检测结果是诊断元数据，不得因单次状态更新刷新无关的全局选路配置；迟到检测写回必须受代理配置 revision / generation 约束。

## 3. 允许的请求侧动作

未知上游响应只允许在安全重放边界内做以下动作：

- 记录 attempt、审计、耗时和脱敏的原始状态摘要；
- 在当前请求内排除已经失败的凭据/账户候选，并按既定预算尝试下一个 Key、账户或分组；
- 对确定不可安全重放的请求保留最后一次失败并返回稳定的网关可重试错误；
- 清除当前会话对失败账户的亲和绑定，避免同一坏会话持续命中同一候选。

当前请求的 Key 排除必须使用请求内集合或等价的 request generation。未知 HTTP 响应不得写入跨请求共享的 `temporary_unavailable`、`rate_limited`、`error` 或其他带语义的 Key 避让状态。

未知 HTTP 响应也不得创建跨请求的客户端 IP × API Key × 账户避让，或进入共享 reliability、质量分数和候选排名。同一 NAT 下的其他会话不能被一个坏会话连带降级。实现可以保留独立的诊断计数，但该计数必须对选路和状态流转保持中性。

完整 HTTP 非 `2xx` 不能被伪装成 `completed_response` 成功，也不能被伪装成“显式策略失败”。无论网关在安全边界内接管重试还是把响应透传给客户端，都必须以中性的诊断终态结束本次质量 attempt。完整响应表示传输 framing 已完成，不应据此确认传输电路失败。

“下游尚未提交”只证明可以保持客户端响应完整，不能证明上游没有执行或计费。图片生成、图片编辑以及被识别为 image lane 的 Responses 请求一旦开始上游传输，默认采用 at-most-once：不应用文本首字切换，不因未知 HTTP、结果未知的 transport failure 或响应体中断跨 Key/账户重放。音频生成、文件/资源创建、后台任务、带 hosted tool 或其他可产生外部副作用的 POST 也遵守同一“发送后禁止隐式重放”边界，不能只因为它不是 image lane 就套用文本推理重试。无法安全重放时返回网关自有的稳定中性错误，不把供应商原始错误当作客户端重试指令。

图片 lane 只有在上游已经派发后的 **in-flight attempt** 使用独立长时限；派发前的候选扫描、并发排队和零可派发等待仍受普通请求协调预算约束。当前默认图片首响应上限为 600 秒、流式 idle 上限为 120 秒、未提交 attempt 上限为 3600 秒；配置可以按实际生成服务调整到 10–15 分钟或更长。在途图片请求允许跨过文本 270 秒墙钟继续等待，但仍只能执行一次；只有图片专用时限到期才中止该 attempt，不得切 Key、账户或后备分组。下游尚未提交时返回网关稳定的 `503/upstream_outcome_unknown`；下游已经写出状态、响应头或正文时只能结束或断开当前响应并在内部记录结果未知，不能改写成第二个错误响应。

文本推理的透明切号属于明确的有界 at-least-once 可用性模式，可能产生重复 token 或重复计费；它必须受请求级候选去重、总墙钟和重试预算约束。用户显式策略只有精确命中并明确选择 `retry_next` 时才表达本次重放意图；`temp_unschedulable`、`rate_limited`、`error_disabled` 或仅命中其他副作用动作都不等价于授权重放。`retry_next` 也不能越过请求语义门禁：图片、音频、文件、资源、后台任务或 hosted tool 等副作用请求一旦派发，必须保持 at-most-once，不能只按 URL 或“下游未提交”判断安全。

普通同账户安全原地重试只允许用于网关前台、可安全重放的文本请求，并且只消费“主上游请求已经开始、响应头尚未到达时发生的 transport failure”。完整 HTTP 响应、响应正文中断、用户配置的首字切换截止、未真正开始的本地异常，以及图片、音频、文件、资源、后台任务或 hosted tool 请求都不得消费该预算。同账户兄弟 Key 先按请求内池顺序唯一尝试且不消费该 token；只有兄弟 Key 已耗尽时，才允许 token 重试当前凭据。次数和间隔由整次请求共享，不得按账户、Key、分组、流式重新调度或 compact 预处理重新发放，并且每次再次派发前都要重新检查墙钟和最终响应保留时间。中间失败只形成审计/使用记录事实，不写电路 confirmation、共享质量、Key/账户状态、客户端 IP 避让、代理或上游桶；预算不足或剩余墙钟不足时直接进入后续候选或客户端 handoff，不得再发送一次上游请求。

同一账户的多个当前可调度 Key 应在本请求内按池顺序唯一穷尽，不能用固定的 2-Key 截断制造假性全池死亡。跨账户重复的同一物理凭据在一个请求内只允许命中一次；整个请求最多发出 64 个唯一多-Key attempt，并在每次真实上游发送前检查共享总墙钟和最终响应保留时间。安全预算截断与真实 Key 池穷尽必须使用不同审计结果，均不得写成 Key 或账户死亡。

客户端重试是新的网关请求，不继承上次请求的失败游标。新请求必须基于当时的优先级、质量、速度、可用状态、全局轮换游标和恢复结果重新选择；因此高优先级账户已经恢复时可以重新选择高优先级账户，而不是机械延续到上次失败账户的下一个号码。

流式失败只允许在精确协议适配器中按协议声明的事件结构识别，例如 SSE 事件名 `event: error`、Responses 的 `response.failed` 或图片协议的 `image_generation.failed`。普通 SSE 事件的 `data` JSON 仅包含 `error`、`data.error`、`metadata.error`、`code` 或 `message` 字段时只是普通 payload，不能被猜测为失败终态；事件携带的 code、type、message 和 HTTP 状态同样不可信。结构失败只证明本次 attempt 失败，不授权共享账户状态。只有允许自动重放的安全文本可以在输出前丢弃未提交事件并切号；图片及其他副作用请求即使尚未输出也只能结束唯一 attempt，由独立后台 due 探针提供后续传输证据。输出后必须保留已提交内容、丢弃原始失败事件并稳定结束，不能拼接网关错误或透明重放；`generic_*` 客户端仍保持事件透明。

余额查询和人工诊断同样不得解释上游状态码。完整非 `2xx` 可以继续尝试其他余额 adapter，但不能清配置、修改首选 adapter 或改变账户调度状态；人工诊断按既定 10/20/30 秒档位记录真实结果，不产生网关共享状态副作用。

Key 冷却探针的 success、transport failure 和 neutral defer 都必须携带候选读取时的 status、next probe、updated-at、账户 config revision、fingerprint 和 secret 代次。SQLite / PostgreSQL 只能用 update-only CAS 回写；任何人工编辑、显式策略、凭据轮换或较新探针先完成后，迟到结果不得重新插入或覆盖状态。

多实例 Key 冷却探针必须先通过数据库原子 claim 领取带 token 的限时租约，success、transport failure 和 neutral defer 回写时同时校验 claim token 与完整代次。候选扫描应先有界过滤已失效 fingerprint，再领取当前 Key；旧 fingerprint 不能占满固定窗口并使当前 Key 永久饿死。租约到期可以由新 worker 接管，但旧 worker 的迟到结果必须被 CAS 拒绝。

电路运行态容量耗尽不能伪装成 `CLOSED`。Memory / Redis 必须返回共享的 capacity sentinel 并保守阻塞对应派发，容量释放后自动恢复；重建必须有单页、总时限、最大页数和 cursor 前进约束，并允许已完成权威查询的账户渐进服务。恢复扫描必须使用有界批次、有限并发、长期退避和确定性 jitter，不能对大账户池形成同步探针风暴。

## 4. 明确禁止的实现

下列实现一律视为越界，除非它们位于用户显式策略的匹配结果分支中：

- `statusCode === 401/403` 就标记凭据或账户失效；
- `statusCode === 429` 就标记限流或进入账户冷却；
- `statusCode >= 500` 就标记供应商、代理或账户不可用；
- 根据 `error.code`、`error.type`、正文关键词或流式事件名称推断 Key/账户死亡；
- generic 请求收到一个业务错误后，把当前 Key 的失败写入跨请求共享运行态；
- generic 请求收到一个业务错误后，写入跨请求客户端 IP 避让或降低共享账户质量；
- 图片或其他资源创建请求已发送后，仅因下游尚未提交就自动切 Key/账户重放；
- 音频、文件、后台任务、hosted tool 或其他副作用 POST 已发送后，按普通文本推理规则隐式重放；
- 把 framing 完整但协议校验失败的自动探针结果写成账户/Key 不可用；
- 把上游返回的任意错误类别传给客户端，要求客户端承担内部账户状态决策。
- OAuth token 刷新连续收到远端异常后，把账户升级为永久 `error` 或停止所有自动恢复；
- 用普通协议成功或旧在途成功，清除用户显式策略创建且 provenance 不匹配的冷却、限流或硬错误；
- 把某个账户经同一代理发生的 request-local transport failure 写成代理级排除，连带跳过同代理的其他账户；

代码中的状态码只能用于协议边界、HTTP 响应转发、审计和显式规则匹配；出现 `401/429/5xx` 字面量并不自动违规，但必须能证明它不参与内部账户状态推断。

## 5. 电路容量、重建与恢复边界

- account circuit 的 memory / Redis 容量必须有硬上限并可配置；容量耗尽时不得把未记录的故障伪装成 `CLOSED`。运行态用共享容量哨兵把未知 scope 标为受控阻塞，只有活动 incident 关闭或容量提高后才自动解除。
- 冷启动全量重建必须同时受单页时限、总时限、最大页数和严格递增复合 cursor 约束。页失败、数据库挂起或容量不足都必须有界返回，并释放 `rebuilding`，允许下一轮重试。
- 全量重建未完成时，请求只能通过按账户权威查询渐进恢复。该查询必须一次返回并投影当前账户的父 incident、最多 64 个活动/恢复必需子 incident，以及当前 dispatch revision 下仍保留的 `CLOSED` ledger；不能按每条子 scope 再做无界查询。这样既避免先加载的旧 `OPEN` 卡住账户，也避免只重建父级而把子级误当 `CLOSED`。无法确认的账户继续阻塞，但不得连带阻塞已经确认完成的其他账户。
- 长期 OPEN 的退避上限为 15 分钟基线，并从第 5 档开始按 scope 做确定性 `±20%` jitter；恢复 worker 使用可配置的有界 batch 和并发。Redis due 修复采用多次小 Lua 分页，单次 Lua 不得扫描整个容量。
- Redis 的账户 revision 清理必须使用有界 `HSCAN` 分页，不能在账户或 scope 数量增长后退化为 `HGETALL` 全量读取。全量重建暂未完成时，恢复 sweep 和待投影 control-plane 事件仍需继续处理已经加载的 scope，不能互相等待形成全局自锁。
- 低容量组的 FIFO 排队必须同时受组队列时限、服务器重试预算和请求墙钟约束。只有后续分组存在真实可承接的模型、额度和并发候选时才允许立即 fallback；没有可承接候选时保留当前组有界等待。图片请求在派发前仍受这些协调预算约束；一旦上游派发成功，in-flight attempt 改由图片 600/3600 秒专用时限约束，不得被文本 270 秒墙钟提前取消或切号重放。
- recoverable waiter 的 `global` 容量是单个 Node 进程内的共享上限；多进程部署的聚合容量会按进程数放大。文档、指标和压测不得把它误写成跨进程全局配额，除非以后迁移到共享协调存储。

## 6. 代码审查与测试门槛

涉及本边界的变更至少要覆盖：

- 同一坏会话并发请求不会把多个正常账户或 Key 写成死亡；
- 任意状态码互换（例如把 `400`、`401`、`429`、`500`、`503` 互换）不会改变 generic 请求的内部状态动作；
- 用户显式策略命中时，才会产生配置指定的持久状态；
- 普通协议成功、旧在途成功和 OAuth 刷新成功不能清理 provenance 不匹配的用户显式冷却；匹配来源恢复、TTL 和人工恢复分别有 SQLite / PostgreSQL 并发 fencing 测试；
- OAuth token 端点的 `400/401/403/429/500/503`、错误正文、坏 JSON、缺字段、断连和 timeout 都只形成诊断与有界退避，不把账户写成永久 `error`；
- 代理检测对任意完整 HTTP 状态都只记录 transport reachable，连接/TLS/绝对 timeout/读取未完成才失败，无请求事实时为 `unknown`；
- 同一代理下账户 A 的 request-local transport failure 不得让账户 B 在本请求内被代理级排除；
- 请求内 Key 排除在新请求开始时清空，不跨请求污染；
- 同一客户端 IP 下的另一个会话不继承未知 HTTP 响应形成的账户避让；
- transport / timeout 与完整 HTTP 非 2xx 的电路、质量和恢复结果互不混淆；
- 透传和接管的完整 HTTP 非 `2xx` 都结束质量 attempt，但不增加成功率，也不降低共享质量；
- 自动探针的 `2xx-invalid-body`、任意 `4xx/5xx` 完整响应保持业务状态中性，真实 transport failure 才推进传输失败；
- 图片 lane 的首字截止、未知 HTTP、请求体已发送后的断连和 `2xx` 正文中断都只产生一次上游执行；
- 音频、文件、资源创建、后台任务和 hosted tool 等非图片副作用 POST 已发送后同样不发生隐式重放；
- 异步首字/读取切换决策返回前 attempt 已完成时，迟到决策不得再取消或重放；
- Redis 和 memory 的并发写入、迟到成功/失败、恢复租约和 generation 结果一致。
- 首次失败及两个独立 confirmation 才能 `OPEN`；同一坏会话高并发始终不能自证死亡，低流量 `SUSPECT` 可由后台探针最终恢复或打开。
- `SUSPECT` 探针 unknown 保持 generation/计数不变并渐进退避；大量 unknown 不得在 due 队列形成零间隔热循环，也不得推进 `OPEN`。
- 3 个、6 个和超大 Key 池按顺序唯一尝试；真实池穷尽、64-attempt 安全截断和总墙钟截断可区分且都不污染共享状态。
- 客户端下一次请求根据当时的质量、速度、优先级和恢复状态重新选号，不延续上次请求的候选游标。
- 普通事件中的 `error/code/message` 字段保持中性；只有协议声明的失败事件结构触发输出前切号或输出后稳定结束。
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
