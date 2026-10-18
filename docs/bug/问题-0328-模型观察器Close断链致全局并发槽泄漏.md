# BUG-0328：模型观察器 Close 断链致全局并发槽永久泄漏（低流量数日后网关全量 503）

- 状态：已修复（2026-10-17，待发布）
- 级别：P1（资源静默累积至进程内全部上游派发不可用；进程侧零可观测信号，只有重启能恢复）
- 发现：2026-10-17 外部同源部署事故分析回灌 + 本地代码逐行审计定性（缺陷自 2026-09-10 观察器接线起即在）

## 现象

外部同源部署（sub2api 系旧版本实例，低流量 1–3 req/min、goroutine 恒定 60–80）运行约 4 天后，08:15:02 起 4 分钟内 159 次上游获取全部失败，错误统一为「全局并发槽获取已取消」（`gatewayupstream.UpstreamRequestAbortedError`，`transport_urlpolicy.go` `BoundedConcurrencyGovernor.Acquire` 的 `ctx.Done` 分支），形态为「排队到客户端超时/放弃」（CCH 侧观测 90–100 秒 524 首字节超时），重启进程后立即恢复。全程 goroutine 数、CPU、内存、连接数均无异常——泄漏对象只有一个不带任何运行时痕迹的信号量槽位。

本仓库生产单机（103.36.63.105）未复现同型事故记录（`docs/`、`.local/project-resources/prod/issues/` 均无「全局并发槽」命中），但代码缺陷同在，触发条件（流式客户端中途断开）属常态流量。

## 根因

全局并发槽在 `client.Do` 成功后的唯一持有者是响应体 `slotReleasingBody`（`gatewayupstream/transport.go:449`），归还途径只有两条：读到 EOF（:729）或 `Close()`（:737）；容量默认 5000（`JUHE_AI_CONCURRENCY_GLOBAL_MAX`，`chain_compose.go:100`）。

2026-09-10（`363f649e8`）起每个上游响应体在派发引擎内被模型观察器包装（`chain_compose.go:575` 无条件接线，`chain_compose.go:853` 替换 `response.Body`），包装链为：

```
消费方 Close
→ ReaderUpstreamBody.Close()             （gatewayresponse/upstream.go:132，对 io.Closer 断言成立，透传 ✓）
→ publishingObservedBodyReader.Close()   （gatewayobs/upstreamresponsemodel.go:420，只发布观察值）
→ reader.inner.(io.Closer) 断言          ← inner 动态类型 *observingUpstreamBodyReader
    该类型只有 Read 没有 Close（body 字段还是 io.Reader），断言恒失败 → return nil
→ slotReleasingBody.Close() 永远不会被调到
```

**对网关流量，Close 链在观察器层断流，EOF 成为槽位唯一归还途径。**所有未读尽即终结的响应各永久泄漏一个槽：

- 流式客户端中途断开（`PipeUpstreamStream` 的 `defer Close` 存在但被吞）——高频常态泄漏源；
- 上游 body 中途断连、空闲看门狗到期取消（下一次 Read 报错非 EOF；修复前看门狗只 `cancel()` 不归还、Close 链断裂也救不回来，修复后管道 defer Close 在 Read 出错后正常归还槽位）；
- 失败换号路径错误体超过 256KB 捕获上限后提前停读（`chain_ports.go:391` 的显式 Close 被吞）；
- 跨协议桥非流式缓冲转换 `io.ReadAll` 出错丢弃响应体（`chain_bridge_response.go:105-108`、Gemini Code Assist `:497-500`）——此路径连包装层 Close 都没调，且 `upstreamattempts.go` 转换错误分支原样丢弃 response，attempt 循环错误分支拿到的 response 为 nil，无人能关；
- 媒体 job 对象超捕获上限（理论面，job 对象小，实际罕见）。

正常读完的响应（流式正常结束、非流式完整下载、≤256KB 错误体）不泄漏——靠 EOF 归还，这也是缺陷长期未暴露的原因：每层单独看都"有 Close"，只有跨层串起来才断；`gatewayupstream` 包此前没有槽位释放测试，观察器测试也没覆盖"包装链提前 Close 是否透传"。

外部事故量化自洽：无需「58% 路径泄漏」之类的倒推假设，泄漏只发生在异常终结请求上，低流量叠加一定比例的客户端断开/超时切割即可在数日内耗尽 5000 槽。

## 修复方案

1. **根因修复（`gatewayobs/upstreamresponsemodel.go`）**：给 `observingUpstreamBodyReader` 实现 `Close() error`——对 `reader.body` 断言 `io.Closer` 并转发，非 Closer 保持 no-op。包装链 Close 恢复贯通，既有的全部 Close 纪律（流式 defer、非流式 defer、失败路径显式 Close、桥接泵 defer+panic 兜底）立即恢复生效；`publishingObservedBodyReader.Close` 的既有断言无需改动。
2. **所有权契约兜底（`gatewaydispatch/upstreamattempts.go`）**：`transformUpstreamResponseForAccount` 中转换器返回错误时由引擎 `_ = response.Body.Close()`——契约明确为「转换器报错 = 响应体所有权归还引擎」，覆盖桥接缓冲两处 `ReadAll` 出错路径及未来新增转换分支的同类遗漏（Close 幂等：`slotReleasingBody` released CAS + 解码 reader 重复 Close 安全）。
3. 全仓 `.(io.Closer)` 断言点同型排查（4 处）：仅观察器链一处为"断言落在无 Close 中间层"的真缺陷；`gatewayresponse/upstream.go:132`、`gatewayupstream/body.go:258`（CloseReader 及其调用方）、`closeReader`（无生产调用方）均为对具体值断言的 maybe-closer 惯用法，健全。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 决定性回归 | 观察器包装下提前 Close 归还全局槽 | `go test ./internal/gatewaydispatch/gatewayupstream/ -run TestSlotReleasingBody` | 第二次 Acquire 立即成功（修复前阻塞至超时） | PASS | 通过 |
| 基线回归 | EOF 归还路径不变 | 同上（TestSlotReleasingBodyEOFReleasesGlobalSlot） | PASS | PASS | 通过 |
| 包装层单测 | Close 透传内层 + 非 Closer no-op + 提前 Close 仍发布 | `go test ./internal/gatewayobs/ -run 'TestPublishingObservedBodyClose\|TestObservingUpstreamBodyClose'` | PASS | PASS | 通过 |
| 引擎兜底 | 转换器报错关闭响应体 | `go test ./internal/gatewaydispatch/ -run TestTransformUpstreamResponseForAccountErrorClosesBody` | PASS | PASS | 通过 |
| 全包回归 | 三个受影响包全量 | `go test ./internal/gatewayobs/ ./internal/gatewaydispatch/ ./internal/gatewaydispatch/gatewayupstream/ -count=1` | ok | ok（10.5s / 52.7s / 0.9s） | 通过 |
| 编译与 vet | 全模块编译 + 三包 vet | `go build ./...` + `go vet` | 通过 | 通过 | 通过 |

新增测试文件：`gatewayobs/upstreamresponsemodel_close_test.go`、`gatewaydispatch/gatewayupstream/transport_slot_release_test.go`、`gatewaydispatch/upstreamattempts_transform_close_test.go`。

## 下次遇到

- 先查什么：低流量数日后突发的「全局并发槽获取已取消」全量失败 + goroutine/CPU/内存平坦 = 槽位泄漏指纹；先数 `BoundedConcurrencyGovernor` 槽位的归还路径（EOF / Close）是否被中间包装层截断。
- 重点看什么：任何 `io.Reader` 中间包装层是否实现 Close 并转发；`.(io.Closer)` 断言的动态类型链。
- 如何避免误判：单层单元测试（"这层有 Close"）证明不了跨层贯通；包装 `io.ReadCloser` 的中间层必须以 Closer 契约测试锁定透传。

## 完成总结

- 完成时间：2026-10-17
- 结论：Close 链贯通 + 引擎兜底 + 三层回归测试锁定；外部同源实例需升级到含本修复的版本方可根治。
- 独立终审：`glm_5.3_max` 全项复核（含新行为面、丢体路径清剿、测试必红推导、文档行号逐一对账）判定 SHIP；终审在覆盖范围外发现同型残留丢体点一处（Gemini 交互亲和删除失败臂，`chain_v1.go:725`），已另立 [BUG-0329](问题-0329-Gemini交互亲和删除失败臂丢上游响应体致全局并发槽泄漏.md)。
- 后续建议：生产单机发布后可观察 `JUHE_AI_CONCURRENCY_GLOBAL_MAX` 相关全量失败是否绝迹；外部实例（sub2api 系）升级事项由使用者自行安排。
