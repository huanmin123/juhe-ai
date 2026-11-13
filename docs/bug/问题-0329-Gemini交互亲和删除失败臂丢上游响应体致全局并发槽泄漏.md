# BUG-0329：Gemini 交互亲和删除失败臂丢上游响应体致全局并发槽泄漏

- 状态：已修复（2026-10-17 登记于 BUG-0328 终审；2026-11-13 以当前源码重新取证后修复并验证）
- 级别：P2（触发面窄——Gemini 交互 DELETE + 亲和存储故障窗口才命中；命中即与 BUG-0328 同型：全局并发槽泄漏，仅重启恢复）
- 发现：2026-10-17 BUG-0328 终审（`glm_5.3_max` 丢体路径清剿项），主代理已逐行复核成立

## 现象与根因（2026-11-13 按当前源码复核，行号已自登记时漂移）

`backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_v1.go` 的 `handleUpstreamResponse`：Gemini interactions DELETE 收到上游 2xx 后，`:699` 构造 `responseSnapshot.Body = gatewayresponse.NewReaderUpstreamBody(ctx, upstream.Body)`（`upstream.Body` 是 `slotReleasingBody`，`gatewayupstream/transport.go:449`，持有 `RequestUpstream` 内 `governor.Acquire` 取得的全局并发槽 release 函数）；`:833-839` 先执行提交前亲和移除 `deleteInteractionAffinityBeforeCommit`（`chain_interaction_affinity.go:71`），返回错误时 `err = affinityErr`，两个响应处理器（`HandleStreamUpstreamResponse` / `HandleNonStreamUpstreamResponse`，唯一会消费并关闭 `responseSnapshot.Body` 的管道）都不会被调用；`:868-876` 的 `AffinityUnavailableError` 503 分支只渲染下游错误、不触碰上游体；函数内与 dispatch loop（`chain_v1_loop.go:639-703` 的 `responseRound`，只兜底账户级槽与租约）都没有对上游体的兜底关闭。

泄漏机理（比登记时「永远到不了 EOF」的表述更精确）：全局槽的归还只发生在 `slotReleasingBody` 的 Read 命中 `io.EOF` 或被 Close（`transport.go:719-739`），无 ctx-done 兜底。`ReaderUpstreamBody` 的 pump goroutine 以 32KiB buffer 逐块读底层体并经**无缓冲 channel** 送达消费者（`gatewayresponse/upstream.go:93-123`）——错误臂短路后消费者不存在：

- body 未被一次读尽：pump 的首个 chunk 读出后阻塞在 channel 发送上，不再发起下一次 Read，`slotReleasingBody` 停在非 EOF 状态——全局槽永久滞留 + pump goroutine 永久阻塞（请求结束取消的只是派生 readCtx，解除 pump 阻塞但不释放槽）。
- body 一次 Read 即读尽（如 ≤32KiB 的体，`io.LimitedReader` 在读满 Content-Length 的同一次 Read 附带 `io.EOF`）：EOF 在 `slotReleasingBody.Read` 层面即到达，槽当场释放——只泄漏 pump goroutine，不泄漏槽。

即「槽泄漏」的实际条件是 body 未读尽 + 亲和存储故障窗口；goroutine 泄漏在任意带体 2xx 的亲和失败路径都发生。与 BUG-0328 的区别不变：BUG-0328 是「Close 有人调但链路传不下去」；本条是「错误臂根本没人调 Close」——BUG-0328 的修复（观察器中间层 Close 转发 `gatewayobs/upstreamresponsemodel.go:459`；引擎 `transformUpstreamResponseForAccount` 错误分支兜底关体 `gatewaydispatch/upstreamattempts.go:197-202`）都在引擎 attempt 循环内或响应管道消费途中，成功返回链面之后的错误臂没有任何 Close 可传导，对本路径无效。同函数 REMEMBER 臂（`interactionAffinityRememberBeforeCommit`）经复核为响应管道内回调（`chain_v1.go:750` 注入、管道内部消费），不在管道前短路，无同类风险。

## 触发条件

Gemini 账户 + 交互资源 DELETE 请求 + 上游返回带未读尽体的 2xx + 亲和存储 `Affinity.Delete` 失败（Redis/亲和存储故障窗口）。

## 修复（2026-11-13）

`chain_v1.go` 亲和错误臂在 `err = affinityErr` 之前显式 `responseSnapshot.Body.Close()`：

- `ReaderUpstreamBody.Close` 幂等（`sync.Once`）：取消 readCtx 解除 pump 阻塞（goroutine 回收），并向下关闭 `slotReleasingBody`（其 `releaseOnce` 幂等）——无论 body 是否已读尽，全局并发槽确定性归还。
- 客户端行为不变：503 `interaction_affinity_unavailable` 渲染分支、响应不提交语义均未改动；无开关。
- BUG-0328 的既有修复保持原样（各自覆盖其所有权面），本条只补链面错误臂这一处缺口。

## 回归测试

`cmd/juhe-ai-gateway/chain_interaction_affinity_test.go` 新增 `TestChainInteractionAffinityDeleteFailureReleasesGlobalSlot`（与既有 gemini interaction 亲和链测试同文件）：容量 1 全局 governor（compose 后替换 `chain.engine.Transport.Governor`，引擎每次 attempt 动态读取）、亲和注入「写正常、Delete 恒失败」的 store（先 create 2xx 建立绑定，DELETE 的 preflight Resolve 才能命中路由到上游）、DELETE 上游回 200 **未读尽大体**（128KiB，超过 pump 的 32KiB buffer——小体一次读尽附 EOF 会在 `slotReleasingBody` 层释放槽，用例不决定性）。断言：第一个 DELETE 收到 503 `interaction_affinity_unavailable`（行为不变）；第二个 DELETE 在短超时窗口内穿过 `governor.Acquire` 走完同一链路（槽已归还）。

## 验证证据

- 先红后绿（`JUHE_AI_TEST_EXTERNAL=1 go test ./cmd/juhe-ai-gateway/ -run TestChainInteractionAffinityDeleteFailureReleasesGlobalSlot -count=1`，pgtestkit 隔离子库）：
  - stash 修复后（无 `Close()`）：`--- FAIL ... context deadline exceeded (Client.Timeout exceeded while awaiting headers)`——第二个 DELETE 阻塞在 `Acquire` 直至超时，缺陷复现。
  - 恢复修复后：`--- PASS: TestChainInteractionAffinityDeleteFailureReleasesGlobalSlot (0.93s)`。
- gateway 模块 `go build ./...` / `go vet ./...` 通过。
- `JUHE_AI_TEST_EXTERNAL=1 go test ./cmd/juhe-ai-gateway/ -run 'Gemini|Affinity' -count=1`：ok，35 用例全 PASS（含既有 `TestChainInteractionAffinityLifecycle` / `TestChainInteractionAffinityWriteFailure503` 零回归）。
- `gatewaygemini` / `gatewayresponse` / `gatewaydispatch` 三包 `-count=1` 全绿。

## 验收标准核对

1. 亲和 `Delete` 失败时客户端仍收到 503 `interaction_affinity_unavailable`（行为不变）——回归用例第一断言锁定。
2. 该路径下全局并发槽归还——回归用例第二断言（第二个请求穿透容量 1 governor）锁定，且修复前红。
3. 既有 Gemini 交互生命周期测试零回归——`Gemini|Affinity` 定向全绿。
