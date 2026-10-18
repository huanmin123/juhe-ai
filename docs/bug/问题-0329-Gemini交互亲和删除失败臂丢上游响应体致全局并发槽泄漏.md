# BUG-0329：Gemini 交互亲和删除失败臂丢上游响应体致全局并发槽泄漏

- 状态：待修复（2026-10-17 登记于 BUG-0328 终审，尚未实施）
- 级别：P2（触发面窄——Gemini 交互 DELETE + 亲和存储故障窗口才命中；但命中即与 BUG-0328 同型：全局并发槽永久泄漏一个，仅重启恢复）
- 发现：2026-10-17 BUG-0328 终审（`glm_5.3_max` 丢体路径清剿项），主代理已逐行复核成立

## 现象与根因

`cmd/juhe-ai-gateway/chain_v1.go:725-726`：Gemini interactions DELETE 收到上游 2xx 后，响应体处理（`HandleStreamUpstreamResponse` / `HandleNonStreamUpstreamResponse`，唯一会关闭 `responseSnapshot.Body` 的消费方）之前先执行提交前亲和移除 `deleteInteractionAffinityBeforeCommit`（`chain_interaction_affinity.go:67-91`，2026-10-15 `f1991b708d` 引入）。该调用返回错误时 `err = affinityErr`，**两个响应处理器都不会被调用**，控制流直达下方 503 分支（本地渲染，不触碰上游体）；而 `:616` 构造的 `responseSnapshot.Body`（`ReaderUpstreamBody` → 观察器链 → `slotReleasingBody`，持有全局并发槽）全程无任何人 Close，函数内也没有兜底 defer。

泄漏为永久性：`ReaderUpstreamBody` 的 pump goroutine 阻塞在无缓冲 channel 的首个 chunk 发送上（`gatewayresponse/upstream.go:100-106`），永远到不了 EOF；请求结束取消的只是读 ctx，`slotReleasingBody` 没有 ctx-done 兜底、非 EOF 的 Read 错误也不释放（`gatewayupstream/transport.go:719-732`）。

与 BUG-0328 的区别：BUG-0328 是「Close 有人调但链路传不下去」；本条是「错误臂根本没人调 Close」——同缺陷类（全局槽归还依赖响应体终态）的残留面，BUG-0328 的 Close 链修复对本路径无效（没有 Close 可传导）。

## 触发条件

Gemini 账户 + 交互资源 DELETE 请求 + 上游返回 2xx + 亲和存储 `Affinity.Delete` 失败（即 Redis/亲和存储故障窗口，恰为系统性触发点）。

## 修复方向（待实施）

- 在 `handleUpstreamResponse` 内 `:616` 之后加函数级收口，或亲和错误臂显式 `_ = responseSnapshot.Body.Close()` 后再渲染 503（`ReaderUpstreamBody.Close` 幂等，once 守卫）。
- 补回归测试：「亲和删除失败仍归还全局并发槽」（容量 1 governor + 亲和端口注入失败，断言第二次 Acquire 成功）。
- 顺带核对同函数 REMEMBER 臂（`interactionAffinityRememberBeforeCommit`，create 面）——终审核对结论为管道内部消费、不在管道前关体，无同类风险，测试可一并覆盖。

## 验收标准

1. 亲和 `Delete` 失败时客户端仍收到 503 `interaction_affinity_unavailable`（行为不变）。
2. 该路径下全局并发槽归还（回归测试锁定）。
3. 既有 Gemini 交互生命周期测试零回归。
