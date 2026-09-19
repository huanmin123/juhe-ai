# /v1 链性能基线采集（2026-09-18，R5 波次产物）

> 机器产物目录：本目录存放 pprof 画像与基准说明；结论性报告在 `docs/reports/`（待各波次收敛后成文）。
> 采集环境：Windows / i9-13900K / go1.26；**并行负载下采集**（另有 9 个重构 agent 共享 CPU），绝对值仅供热点排序参考，正式基线需空载复测。

## 产物清单

| 文件 | 内容 |
| --- | --- |
| `response_sse_cpu.prof` | `PipeUpstreamStream`（OpenAI Chat SSE 泵）CPU 画像，10s benchtime，420k 次迭代 |
| `response_sse_mem.prof` | 同泵 alloc_space 画像 |
| 符号化二进制 | `gatewayresponse_bench.test` 在 `backend-go/projects/gateway/`（`gatewaydispatch_bench.test` 同） |

复现命令（空载时）：

```bash
cd backend-go/projects/gateway
go test -o gatewayresponse_bench.test -c ./internal/gatewayresponse/
./gatewayresponse_bench.test -test.bench=BenchmarkResponsePipeUpstreamStreamOpenAIChatSSE \
  -test.benchtime=10s -test.run=^$ \
  -test.cpuprofile=<...>/cpu.prof -test.memprofile=<...>/mem.prof
go tool pprof -top gatewayresponse_bench.test cpu.prof
```

## 基准基线（1s benchtime，H5 harness，`*_bench_test.go` 可重放）

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| DispatchFetchFirstAvailableUpstreamHappyPath | 339,570 | 51,133 | 348 |
| DispatchFilterOpenAIGatewayRequestCandidateAccounts | 5,419 | 24,944 | 9 |
| DispatchPrepareDispatchAccounts | 16,309 | 56,125 | 78 |
| DispatchNormalizeOpenAIReasoningFieldsChat | 2,533 | 2,176 | 50 |
| DispatchApplyGptAccountRequestOverridesBody | 1,682 | 1,560 | 29 |
| ResponsePipeUpstreamStreamOpenAIChatSSE | 28,241 | 19,035 | 219 |
| …TimeoutsDisabled（对照） | 25,715 | 17,199 | 190 |
| ResponseHandleStreamUpstreamResponseOpenAIChatSSE | 33,171 | 21,192 | 228 |
| ResponseHandleNonStreamUpstreamResponseChatJSON | 15,850 | 11,179 | 143 |
| ResponseFinalizeHandledUpstreamResponseSuccess | 977 | 864 | 3 |

## 初步热点排序（供 R5 结论，非优化项）

1. **dispatch 全链 340µs 中 loopback HTTP 传输占绝对主导**：进程内 filter（5.4µs）+ prepare（16.3µs）合计 <7%。
2. **SSE 泵 CPU 被 scheduler 原语主导**：semawakeup+semasleep+lock2 ≈ 26% 采样——每分片 `time.After` + select 竞速的 goroutine 停靠成本；TimeoutsDisabled 对照每次泵省 ~2.9µs / 1.8KB / 29 allocs。
3. **SSE 泵分配热点（alloc_space 占比）**：`gatewayopenai.copyCounts` 10.2%、`encoding/json` objectInterface 8.0%、`newStreamPipe`+`newPendingRead`+`LimitedCapture.Buffer`（每分片管道结构）合计 ~18.5%、`bytes.growSlice` 5.6%、`regexp.bitState.reset` 4.5%。
4. prepare（16.3µs/56KB/78 allocs）比 filter（5.4µs/25KB/9 allocs）重 3 倍，56KB/op 待 pprof 定位。

## 已知缺口

- `dispatch_fetch_cpu/mem.prof` 未采到：全链基准依赖真实 loopback socket，并行负载下系统 socket 缓冲耗尽（bind 失败），需空载复测补采。
- JSON 编解码占比的链路级量化（A6 风险项）需 dispatch 全链画像到位后合并结论。
