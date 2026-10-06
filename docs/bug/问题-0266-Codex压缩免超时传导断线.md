# BUG-0266：Codex 压缩请求免超时传导断线（响应层重建 profile 掐断压缩流）

- 状态：已上线（随 10-03 00:4x 发布）
- 定性：Go 实现与《Responses上下文压缩落地方案.md》契约偏差——Node 迁移遗漏「禁超时」判定的两处传导接线；preflight 识别正确、engine 兜底生效，但响应层/coordination 断线导致压缩流仍被 120s 首响应预算掐断
- 发现方式：生产用户报障「客户端压缩卡死，使用日志全是 上游流式请求 120s 内未返回首段数据」（2026-10-02，系统账户 huanmin，Codex Desktop 0.159.2）+ 审计/网关日志/Node 权威源码三方对照取证

## 1. 现象

用户 Codex Desktop 客户端执行上下文压缩（Remote Compaction V2：`POST /v1/responses`，input 数组末尾 `{"type":"compaction_trigger"}`，body 0.5–7.9MB）全部失败。实证 trace（`f29bc6f2`，body 552KB）：

- 18:09:17 attempt1 拿到上游 200（fetch 25.4s），流上 120s 仅 24 字节、0 个 SSE 事件 → 18:11:17（= fetch 完成 + 120.0s）被掐「上游流式响应 120s 内未返回有效输出、失败或终止事件」；
- attempt2–9：每个 2–16s 拿到 200 后**同一毫秒**报「上游流式请求 120s 内未返回首段数据」（pipe 日志 elapsedMs 从请求起点算 147974ms > 120000ms 预算）；
- 9 账户耗尽 → 503，客户端压缩永久卡死。

同日另有 2 条成功压缩（`173cd86a`/`8db7a4aa`，125.7s/130.4s 首输出，SSE 含 `response.compaction.compacting` 事件流）——上游提前发 compacting/keepalive 事件时，事件不断刷新空闲计时，流能撑到输出；上游静默思考（不发事件）时必被 120s 掐断。成败分界是上游是否发心跳，与账户好坏无关（同账户三种结局）。

## 2. 根因（对照 Node 权威链路，F:/juhe-node-temp 迁移备份源码）

Node 契约链：`preflight.ts:397` 判定 `compactionTimeoutsDisabled` → `:405/:410-412` wall budget `unbounded=true`（`withoutLimit()`）随返回值带回 → 主循环 `routes.ts:1261-1263` 从 `unbounded` 重导出 `timeoutPolicy: 'codex_compaction_unbounded'` 进 dispatch coordination → `upstream-dispatch.ts:367-371`（coordination 字段 OR 兜底重查）生成 `timeoutsDisabled: true` 的 profile → 结果字段 `:194` 外传 → `routes.ts:1572` **原样直传**响应层 → `stream-read-plan.ts:20-22` `timeoutsDisabled → return undefined`（读超时全灭）；transport 层 `request.ts:285-287` 的 120s socket watch 同被 `disableTimeouts` 闸住。

Go 侧 preflight 识别、engine 判定、transport 闸门均正确（生产审计 `codex_compaction_timeouts_disabled` 元数据在案、attempt1 非 transport 错误文案佐证 transport 已禁超时），断线在三处：

1. **断点 A1（budgets 未回收）**：`chain_v1.go:286` `newRequestBudgets` 自建 budgets 直接装入 loop（:316），未从 `preflight.DispatchContext.GatewayRequestWallBudget`（`preflight.go:300-301` 对 compaction 调 `WithoutLimit()` 得到的 Unbounded 实例，:309 已写回）回收——同函数 :296-298 对 `serverRetryBudget` 已有回收模式，wall/coordination/tracker 漏了。
2. **断点 A2（TimeoutPolicy 未重导出）**：`chain_v1_loop.go:127-133` 构造 `RequestCoordinationContext` 不设 `TimeoutPolicy`（恒空串），Node `routes.ts:1261` 的重导出步骤缺失。A1+A2 使 dispatch coordination 判定只剩 `upstreamdispatch.go:459` 的兜底重查兜住（本次生产未爆雷，但三重冗余只剩一重）。
3. **断点 B（主因，响应层重建 profile）**：`chain_v1.go:494` 响应层用 `timeoutProfileOf(settings, lane)` 重建 profile，`:729` 硬编码 `disableTimeouts=false`；`UpstreamDispatchResult.TimeoutProfile`（`upstreamdispatch.go:37`，engine 已正确生成 `TimeoutsDisabled=true`）被忽略。Node 响应层从不重建。结果：压缩流管道的 readplan 以 120s 首响应预算（请求级锚点 `pipe.startedAt`）照常运转——attempt1 的 semantic_result 计时器 120s 掐断 + attempt2-9 的 first_chunk 预算已尽同毫秒秒败，全部由此产生。

## 3. 修复（三处接线，对齐 Node；`cmd/juhe-ai-gateway` 范围）

1. `chain_v1.go` 预算回收统一收口到 `v1DispatchLoop.adoptDispatchContextBudgets`（见第 5 节加固后的最终形态；首轮修复为编排入口从初始 `preflight.DispatchContext` 回收，加固时扩展到全部 `loop.current` 换代点），compaction 的 Unbounded wall budget 从此进入主循环。
2. `chain_v1_loop.go` coordination 构造提取为 `newRequestCoordination()`：`l.budgets.wall.Unbounded` 时设 `TimeoutPolicy = TimeoutPolicyCodexCompactionUnbounded`（对齐 Node routes.ts:1261-1263）。
3. `chain_v1.go:494` 改用 `responseTimeoutProfileOf(dispatched, settings, lane)`：优先投影 `dispatched.TimeoutProfile`（映射 `gatewayresponse.TimeoutProfile` 消费字段子集），零值（engine 未带出）回退 settings 派生——非压缩请求行为不变（dispatch 层对普通请求本就按同 settings/lane/false 生成）。
4. **响应层超时锚点改为 attempt 级**（同日追加，attempt2-9 秒败的直接根因）：`chain_v1.go` handleUpstreamResponse 的 `StartedAtMs` 基准从请求级 `startedAt` 改为 `dispatched.AttemptStartedAt`（`dispatchsingle.go` 每 attempt 赋值；零值回退请求级）——对齐 Node routes.ts:1574/:1604 从 upstreamResult 解构 `attemptStartedAt` 传响应层的契约。修复前：首字（first_chunk）/语义结果（semantic_result）/attempt 生命周期（stream_lifetime）/非流式首响应的预算与 `usage_records.first_token_ms` 统计全部从**请求进入时刻**起算，第一个账户耗掉的时间直接从后续账户预算里扣除，换号重试的流一建立即预算耗尽秒败（生产 attempt2-9 同毫秒「120s 内未返回首段数据」形态）。修复后每 attempt 独立计量；请求级总墙钟（`GatewayRequestWallBudget` 默认 270s，Node 同构）仍封顶多账户轮换总时长，`ServerRetryBudget`/`RouteCoordinationBudget` 等待类总预算维持请求级语义不变。**例外**：Prometheus 首字直方图（`firstOutputMetricMarkOf`）保持请求级锚点——Node routes.ts:1507-1508 `recordGatewayFirstOutputMetric(Date.now() - requestContext.startedAt, ...)` 以请求入口时刻为锚，attempt 级只用于 :1509 的 markFirstByte（hot-quality），锚点复审发现的分叉契约按 Node 恢复。

不改：`gatewaydispatch`/`gatewaypreauth`/`gatewayresponse`/`gatewayrouting` 生产代码（判定与结构本就正确）；`chatbridgestate.go` 的 `ReplaceGatewayJSONBody` 丢 `CodexCompactionTrigger` 标记问题为跨协议桥场景加固项，另行登记，不在本档范围（已登记为 BUG-0280，2026-10-03 修复收口）。

## 4. 验证

- 新增 `chain_v1_compaction_timeout_test.go`：
  - `TestV1LoopCoordinationTimeoutPolicyMirrorsWallBudget`：Unbounded → `codex_compaction_unbounded`；bounded/nil → 空串；
  - `TestV1HandleUpstreamResponseHonorsDispatchedTimeoutsDisabled`：dispatched 带 `TimeoutsDisabled=true` 的慢流（首输出 >120s）完整存活；零值 profile 回退 settings 预算；
  - `TestV1ResponseTimeoutProfileOf`：disabled profile → `BuildGatewayStreamReadPlan` 返回 nil（读超时全灭）；字段映射与零值回退；
  - `TestV1LoopAdoptsDispatchContextBudgets`（加固补测）：换代点回收——fallback DispatchContext 的 Unbounded wall budget/重试预算逐指针替换 loop 实例且 adopt 后 coordination 立即重导出 `codex_compaction_unbounded`；nil 字段保持原值、nil context 无操作；
  - `TestV1HandleUpstreamResponseAttemptAnchoredBudget`（锚点修复补测）：请求已进行 10s 后换号的 attempt（`AttemptStartedAt = 请求起点+10s`）在 5s 首响应预算下拥有完整预算、1.2s 慢首段正常送达（修复前该形态预算 5000−10000 ≤ 0 立即秒败）；零值 `AttemptStartedAt` 回退请求级锚点、预算耗尽时照常掐断（兜底行为不变）。
- 独立复审（2026-10-02）：可交付、无阻断项；确认非 compaction 请求数值恒等（同 settings/lane/false 生成路径）、image lane 预算替换为 Node 对齐行为、第四处断线核查无遗漏（finalize.go:287-289 的 timeoutsWithDisabled / dispatchsingle.go:557 / attemptoutcomes.go:53 源头门均已就位）。
- 回归：`cmd/juhe-ai-gateway` 主链路与回退路径子集（TestV1/ChainDispatch/ChainFailureDispatch/Recoverable/RouteAction/Fallback/FallbackGroup/SpeedFirst，全过）+ 包全量（唯一失败 `TestListenLoopbackPrivateBindRequiresOptIn` 为本机 13306 端口被既有进程占用的预存环境问题，与改动无关）、`gatewaydispatch`/`gatewayresponse`/`gatewayrouting` 包全过。
- 生产实证（修复前取证即修复后判据）：压缩请求应不再出现「120s 内未返回首段数据/有效输出」类失败；上游静默期（无心跳）可撑到真实输出或 EOF。发布后以 huanmin 的 Codex Desktop 压缩复测为准。

## 5. 边界加固（复审观察的跟进修复，2026-10-02 同日完成）

复审发现的换代点回收缺口已修复：初始 preflight 返回 `RouteAction` 再经 fallback preflight 成功、以及 dispatch 期间 `switchToFallbackGroup` 切组两个换代点，原实现只回收初始 `DispatchContext` 的预算（`serverRetryBudget` 既有回收同型缺口），`l.budgets` 保持自建有界实例，压缩请求在该路径下 `coordination.TimeoutPolicy` 仍为空（仅靠 engine 兜底重查 + 响应层直传 profile 兜底，残留暴露为 engine 侧 `assertGatewayRequestWallBudgetAvailableForAttempt` 对 >270s 多尝试压缩请求的有界断言）。

加固为**单一收口**：`chain_v1.go` 编排入口的初始回收删除（serverRetryBudget/budgets 先自建装配），新增 `v1DispatchLoop.adoptDispatchContextBudgets(context)`（serverRetryBudget + wall/coordination/tracker 四实例非 nil 才覆盖），在两个 `loop.current` 换代点统一调用——`chain_v1.go` 的初始 `resolveRouteAction` 产物（覆盖初始成功与 RouteAction→fallback 两条路径）与 `chain_v1_loop.go` 的 `switchToFallbackGroup` 切组产物。fallback preflight 的预算经 options 从 action/current 同源携带、compaction 分支重新判定并 `WithoutLimit`，换代点收口后 Unbounded 实例必然进入 `l.budgets` 并被 `newRequestCoordination` 重导出。

加固复审（2026-10-02，独立复审角色）发现并已修复一处在制品回归：首轮加固把初始回收删除后，D-120（BUG-0175）SSE 等待心跳的 `SetWaitObserver` 仍挂在编排入口的自建 `ServerRetryBudget` 局部实例上，而初始 preflight 恒构造新实例（编排入口 options 不带预算）、adopt 必然替换，导致 observer 落在被丢弃的实例上——所有 SSE dispatch 等待（零可派发/并发排队/恢复等待）的保活心跳失效，长等待下游空闲断连。修复：心跳装配块整体后移到 adopt 之后，observer 挂 `loop.serverRetryBudget`（最终实例；切组换代经 options 同源复用实例，observer 随之保持有效），并补回归断言 `TestV1LoopAdoptsDispatchContextBudgets/wait_observer_edges_fire_on_adopted_retry_budget`（adopt 后实例的 Begin/Pause 边沿必须驱动心跳起停）。该回归由复审代码级核查发现（现有测试盲区，全绿状态下存在），修复后心跳/等待/主链路/回退子集回归全过。

## 6. 已知偏差（终审登记，非阻断）

**usage 成功终态记录的 started_at 语义漂移**：`FinalizeHandledUpstreamResponse` → `RecordCompletedUpstreamAttempt` 传 `StartedAtMs: input.StartedAtMs`（`nonstream.go` 审计完成路径），锚点修复（第 3 节第 4 项）后随 input 变为 attempt 级；Node 对应路径（finalization 的 `recordCompletedUpstreamAttempt`）的 startedAt 来自 finalization 入参的**请求级** startedAt（routes.ts:257）。根因是 Go 单一 `HandleUpstreamResponseInput` 无法区分 Node 的双 startedAt（响应管道 attempt 级 / 审计记录请求级）。影响限于多 attempt 请求的 usage 记录时间轴（单 attempt 仅差 preflight 时长，方向更精确），`first_token_ms` 不受影响（响应处理内 attempt 级计算，与 Node dist 口径一致）。留档声明该偏差，后续如需严格对齐可与双字段拆分任务绑定处理。

## 7. 后续边界变更（2026-10-03 设计登记）

《普通路由速度优先延迟切换设计》第 6 节"总时间兜底截止"（待实施）将给压缩请求增加**调度层总时长软观察**（`speedFirstConfig.compactionTotalTimeDeadlineSeconds`，默认 300 秒）：到期未完成记总时间慢样本、确认慢后经 `latency_degraded` 降级兜底。该机制不属于本文修复或豁免的 lane 硬超时范围——本文语义全部保持：首字截止、首响应 / 语义结果 / attempt 生命周期 / 非流式首响应等 lane 超时豁免、transport watch 禁用、`Unbounded` wall budget 与读超时 plan 为 nil 均不变。总时间软截止对已写出内容的压缩流不做任何中断（照常读完）；仅当压缩流尚未写出任何可见内容、账户已被确认总时间慢且满足速度优先安全切号链（未写出、有候选、未超每请求换号上限、无坑不跳）时，允许与其他可重放文本一致的隐藏切号（中止当前上游并在新账户重放 compact 契约检查），不产生 lane 硬超时类失败语义。实施时的接线变化：preflight 对压缩请求的首字运行态配置（`NormalRouteFirstByteConfig`）维持 nil、速度优先配置整体照常携带，仅观测层按维度分流（见该设计 6.8）；`codex_compaction_timeouts_disabled` 审计含义不变。

2026-10-06 状态同步：随 10-03 00:4x 发布（发布记录显式点名）；HEAD 复核「compactWaitHeartbeat（chain_v1_loop.go）」命中。
