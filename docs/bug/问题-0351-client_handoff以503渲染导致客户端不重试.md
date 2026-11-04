# BUG-0351 client_handoff 以 503 渲染导致客户端不重试

## 基本信息

- 编号：BUG-0351
- 状态：已修复待发布（2026-11-04）
- 严重程度：P2
- 发现时间：2026-11-04
- 发现方式：锁死 hold 交接复核时扩展到全部 client_handoff 出口
- 模块：网关（链面交接 / 路由协调）
- 关联计划：无
- 关联 bug：BUG-0350
- 责任人：网关维护者

## 问题概述

- 现象：`client_handoff` 合同（协调预算耗尽、路由动作无可用分组）在 Go 链面统一渲染为 503 `upstream_retryable_error`；交接失效——客户端是否重试取决于版本与策略（实测 Codex 0.152 会重试 503，但不能据此假设所有客户端都重试）。
- 期望：`client_handoff` 必须按「非终态不得返回客户端失败」交还——流式请求先提交 SSE 响应头再断开，非流式请求先提交 200/JSON 头再断开；Hijack 不可用时才回退 503。
- 实际：`sendStreamServerRetryExhaustedResponse` 的所有调用点均写 503；Node 的 committed-disconnect 分支未迁移，Go 注释明确“every Go call site renders the 503”。
- 影响范围：协调预算耗尽的流式请求、`resolveRouteAction` 的 `RouteOutcomeClientHandoff` 请求；锁死 hold 与响应检查否决同属该类（已在 BUG-0350 一并修复）。2026-11-04 用户裁决扩展为全量口径：终态可返回错误，非终态一律不得返回客户端失败（含整请求墙钟预算、`temporarily_blocked`、全部账户临时隔离、compact 摘要预检锁死 hold）。

## 复现步骤

1. 构造一个流式 `/v1/chat/completions` 请求。
2. 让路由协调预算在派发前耗尽（或让 `resolveRouteAction` 返回 `client_handoff`）。
3. 观察客户端收到的响应：503 `upstream_retryable_error`，连接正常结束；客户端不重试。

## 根因分析

- 表象：交接被当成普通 HTTP 错误返回。
- 真实根因：Go 响应面冻结后没有 committed-SSE 通道，`sendStreamServerRetryExhaustedResponse` 把所有 `client_handoff` 都收口成 503；后续虽新增 `newDownstreamInterrupt`（Hijack+Close），但只接在响应层 post-commit 管道，未接到链面 pre-commit 的 `client_handoff` 出口。
- 佐证：`chain_v1_streamretry.go` 注释明确 committed-disconnect 分支不可达；`chain_v1_loop.go` 的协调预算分支和 `resolveRouteAction` 均直接调用 503 渲染。

## 修复方案

- 新增 `renderClientHandoff` 统一交接：流式请求先写 `200 + Content-Type: text/event-stream` 并 flush，再 Hijack+Close；非流式请求先写 `200 + application/json` 并 flush，再 Hijack+Close（不写完整响应，反向代理透传截断正文而不是合成 502）；Hijack 能力缺失才回退 503，能力存在但运行期失败以截断正文收场并记降级。
- 接入协调预算 `WallBudgetKindCoordination`、整请求墙钟 `WallBudgetKindWall`、`resolveRouteAction` 的 `RouteOutcomeClientHandoff`、`finalizeRouteAction` 的 `temporarily_blocked`/非终态失败（503 `upstream_retryable_error` 临时隔离）。
- 锁死 hold、锁死响应检查否决、切组锁死门（`v1FallbackLockHold`）与 speed_first 切号锁死门共用同一传输实现，并统一经 `renderClientHandoffWithMetadata` / `renderClientHandoffFailure` 接入。
- Codex compact 摘要预检的锁死 hold 走 `gatewaypreauth.InterruptClientConnection` 真断开，Hijack 不可用才回退 503。
- 交接断开成功时仍落失败审计与 usage：新增 `gatewaypreauth.GatewayFailureRecorder`，`gatewayresponse.Sink.RecordGatewayFailureWithoutResponse` 只记账不写响应（审计 outcome/phase/code 与同源 503 一致）；零上游 attempt 的 `gateway_failed` 行按既有审计收录规则不落审计主行（只落 usage），wall / 临时隔离出口与改动前 503 一致。
- 审计新增 `gateway_client_handoff_transport`，记录 `retryReason`、`errorCode`、`handoff=stream_committed_disconnect|connection_interrupt|http_503_fallback|stream_headers_committed_hijack_failed|response_headers_committed_hijack_failed`。

## 验证

- `TestRenderClientHandoffStreamingCommitsSSEThenDisconnects`：流式先提交 `text/event-stream` 再 Hijack/Close，不写 503。
- `TestRenderClientHandoffAfterHeartbeatCommitsStreaming`：等待心跳已提交 200/SSE 后仍 Hijack+Close，不退化成正常 EOF。
- `TestRenderClientHandoffRejectsCommittedNonSSEHeaders`：已提交 JSON 响应不得 Hijack，回退 503 契约。
- `TestRenderClientHandoffRecordsCommittedHijackFailure`：SSE 头已提交后 Hijack 运行期失败，审计记为 `stream_headers_committed_hijack_failed` 降级。
- `TestRenderClientHandoffFallsBackTo503WhenHijackUnavailable`：无 Hijack 时回退 503。
- `TestInterruptClientConnectionHijacksAndCloses` / `...WithoutHijackerKeepsHeadersUnsent` / `...HijackErrorKeepsHeadersUnsent`：Hijack 能力缺失或运行期失败都不得污染 `HeadersSent`，503 回退可写。
- `TestFinalizeRouteActionTemporarilyBlockedHandsOff`：route action 的 503 `upstream_retryable_error`（全部账户临时隔离）真断开且不写 503；无 Hijack 回退 503 并保留 `Retry-After` 语义与 `gateway_capacity` 归因。
- `TestSettleResponseStreamServerRetryLockedFallbackHandsOff` / `TestSettleDispatchErrorAttemptLockedFallbackHandsOff` / `TestSettleSpeedFirstCutoverLockedHandsOff`：三处锁死否决（切组与速度切号）均交接断连，不落终态耗尽 503。
- `TestSettleResponseStreamServerRetryLockReadFailureM4Narrowing`：锁读取失败仅在已观察到锁行时 fail-closed。
- `TestSettleSpeedFirstCutoverLockReadFailureFailClosedWhenObserved`：speed_first 结算期读取失败同样按 observation 收窄。
- `TestClientHandoffThroughReverseProxyStaysTruncated`：真实 `httptest` 后端 + `httputil.ReverseProxy` 前置代理验证——非流式交接先提交 200 头再断连时客户端收到 200 + 截断正文（网络级读错误）；未提交响应直接断连会被代理合成为 502。
- dev 服务器 `caddy:2-alpine`（Caddy v2.11.7）真机 + 真实 Codex CLI 0.152.0 链路验证，结果见下节「Caddy 真机验证」。
- `TestApplyChatBridgeCompactPreflightLockHoldInterruptsConnection` / `...FallsBackTo503`：compact 摘要预检锁死 hold 真断开、Hijack 不可用回退 503 且不污染 `HeadersSent`（该文件受 `JUHE_AI_TEST_EXTERNAL` 外部 PG 门控制，默认回归跳过；其核心传输/回退行为由 `internal/gatewaypreauth` 的默认单测覆盖）。
- 既有 `WallBudgetKinds` / `RouteAction` / `TestV1|TestW1` 定向集全绿。
- 命令：`go build ./...`、`go test ./cmd/juhe-ai-gateway -run 'TestV1|TestW1|AccountLock|SpeedFirst|ClientHandoff|WallBudgetKinds|RouteAction' -count=1`、`go test ./internal/gatewaydispatch -count=1`、`go test ./internal/gatewaycodex ./internal/gatewayresponse ./internal/gatewaypreauth -count=1`。

## 错误帧裁决（原 T120，2026-11-04 关闭）

真实 Codex CLI 0.152.0 打到本地探针服务器（`codex exec` + 自定义 `model_provider`，`wire_api = "responses"`，统计同一会话的 HTTP 请求数）：

| 失败形态 | Codex 行为 | 请求数 |
| --- | --- | --- |
| 503 JSON `upstream_retryable_error` + `Retry-After` | `Reconnecting... 1/5..5/5` 后报错 | 30 |
| 200 SSE + `response.failed` 错误帧 | 5 次 reconnect 后 `stream disconnected before completion` | 6 |
| 200 SSE 先提交头再断连 | 5 次 reconnect 后 `Transport error: network error` | 6 |
| 未提交响应直接断连（经反向代理合成 502） | 5 次 reconnect 后 `unexpected status 502` | 30 |

结论：
- Codex 0.152 对 503、`response.failed` 错误帧、断连都会重试；"客户端只在断连时重试"的前提对该版本不成立。因此 pre-commit 终态失败帧（`finalize.go` 的 `writePreCommitStreamFailureToClient`）**保留**：它是协议内终态信号，客户端会按其重试并在预算内收敛。
- `nonstream.go` 的 SSE 保活后非流式响应路径（`finalizeNonStreamResponseAfterSseHeartbeat`）在 2026-10-23 契约收口后已是网络级中断，不写错误帧；T120 原证据把该函数的 message 行误读为错误写出，予以更正。
- `interceptor.go` 的 `interceptorFailureEvent` 无生产调用方（响应检查只产出 verdict，终态帧由 finalize 统一渲染），已删除函数与其单测。
- 该裁决不改变"非终态不得返回客户端失败"的用户规则：上述帧路径只在网关已完成决策空间（无可切换候选/策略终态）后触发。

## Caddy 真机验证（原 T121，2026-11-04 关闭）

dev 服务器 `192.168.1.203`（Docker 29.1.3）启动 `caddy:2-alpine`（Caddy v2.11.7，`--network host`，`reverse_proxy 127.0.0.1:28080`，默认 HTTP/1.1 upstream）打到本机探针：

| 探针形态 | 经 Caddy 后客户端观察 | Caddy 日志 |
| --- | --- | --- |
| 完整 SSE 成功 | 200 + 完整事件流 | 正常 |
| 503 JSON | 503 + 原文透传 | 正常 |
| 200 SSE + `response.failed` | 200 + 完整错误帧 | 正常 |
| SSE 头已提交后断连 | **200 + 截断流**（curl 18） | `aborting with incomplete response` |
| 200/JSON 头已提交后断连 | **200 + 截断正文**（curl 18） | `aborting with incomplete response` |
| 未提交响应直接断连 | **502 由 Caddy 合成** | `statusError 502` |

真实 Codex CLI 0.152.0 经 dev Caddy（`base_url=http://192.168.1.203:28081/v1`）访问「200 头已提交后断连」探针：1 次首发 + 5 次 `Reconnecting`（共 6 次请求），最终 `Transport error: network error`，Caddy 日志持续 `aborting with incomplete response`——客户端按网络错误重试，未收到 Caddy 合成的 502。

结论：流式与非流式交接都必须在断连前提交 200 传输头；HTTP/2 upstream 不支持 Hijack 的限制已在 `docker/single-server/README.md` 固化为「Caddy → gateway upstream 必须保持 HTTP/1.1」。验证后已清理 dev 服务器上的探针、Caddy 容器与临时目录，未改动既有容器。

**Nginx 形态（同环境追加验证，2026-11-04）**：`nginx:alpine`（nginx/1.31.8）`--network host` 反代同一探针，默认 `proxy_http_version 1.0` 与显式 `1.1` 两种配置结果一致——SSE 头或 JSON 200 头提交后断连，客户端均收到 **200 + 截断正文**（curl 18），Nginx 记 `upstream prematurely closed connection while reading upstream`（error 级）；未提交响应断连才返回 **502**（`while reading response header from upstream`）。真实 Codex CLI 经 Nginx：6 次请求（1 首发 + 5 次 `Reconnecting`）、`Transport error: network error`。注意事项已写入部署 README：交接产生的 error 日志属预期，SSE 依赖 `X-Accel-Buffering: no`，多实例 upstream 建议 `proxy_next_upstream off`。

## 剩余边界

- HTTP/2 直连（不经 Caddy）不支持 Hijack，交接退化为 503；生产经 Caddy HTTP/1.1 upstream 为正常路径，部署契约已固化。Caddy 真机链路（v2.11.7）已在 dev 验证，见上节。

## 关联

- [AI账户短窗口热质量与精准切号设计](../functions/AI账户短窗口热质量与精准切号设计.md) §client_handoff
- [AI账户锁死调度机制设计与实现前复审](../functions/AI账户锁死调度机制设计与实现前复审.md) §3.4 / §10.6
- [BUG-0350](问题-0350-锁死窗口内同组候选切换仍会切号.md)
- 部署契约：[docker/single-server/README.md](../../docker/single-server/README.md)「Caddy → gateway upstream 必须保持 HTTP/1.1」
