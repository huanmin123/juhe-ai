# BUG-0353 Responses 超大终止事件复用上一条事件类型致流误判失败

## 基本信息

- 编号：BUG-0353
- 状态：已修复待发布（2026-11-04）
- 严重程度：P1
- 发现时间：2026-11-04
- 发现方式：用户反馈 + 生产审计/日志取证 + 本机原文回放
- 模块：后端 / 网关 / 流式转发 / OpenAI Responses 流检查器
- 关联计划：无
- 关联 bug：BUG-0014（同源「普通事件结束后未清理 oversized 临时状态」扩展根因，Go 迁移遗漏）
- 责任人：网关维护者

## 问题概述

- 现象：生产 trace `123a9c32-7fba-4d39-ae3b-b38ad71f189f`（2026-11-04 15:25:42 → 15:26:28，`POST /responses`，`gpt-6.1-sol`，Codex Desktop）审计为 `stream_failed`、`success=false`、`final_status_code=200`、`error_phase=stream`、`error_code=upstream_retryable_error`、`error_message=上游流在协议终止事件前结束`；但抓取的响应原文 454,263 字节末尾包含完整的 `response.completed`（最后一条 `data:` 行 399,567 字节，`status=completed`，usage 完整），上游实际成功结束。
- 期望：上游返回完整协议终止事件的 Responses 流按成功收尾，审计与用量记录 `success=true`。
- 实际：流检查器把超大 `response.completed` 事件误归类为上一条普通事件的类型（本 trace 为 `response.created`），`TerminalReceived` 保持 false；EOF 时进入 `finalizeMissingTerminal`，记为 `upstream_retryable_error`，并在下游已提交 200 后执行 Hijack+Close 断链。
- 影响范围：所有 OpenAI Responses 流中单条 SSE `data:` 行超过 `streamInspectorMaxLineBytes`（256KB）的请求；生产 2026-11-04 当天同型错误 138 条（2026-11-01 另有 1 条），全部为 `gpt-6.1-sol`，涉及 6 个账户（近 7 天 7 个）；另抽两条同型响应，最终 `response.completed` 行分别为 390,153 / 389,137 字节。其他厂商当天无同型错误（deepseek 6106 条、gpt 5138 条、glm 357 条、openai 11 条），原因是 chat/Anthropic/Gemini 的终止帧很小，不触发 oversized 分支，并非不存在解析器缺陷。

## 复现步骤

1. 用 `gatewayopenai.NewStreamInspector()` 先喂入一条普通事件（如 `event: response.created` + 小 data + 空行）。
2. 再喂入一条 `data:` 行超过 256KB 的 `event: response.completed`（真实 trace 中为 399,567 字节）。
3. `Finish()` 后断言：修复前 `TerminalReceived=false`、`EventTypeCounts["response.created"]==2` 且无 `response.completed`；修复后 `TerminalReceived=true`、`response.completed==1`。
4. 生产等价路径：按 trace 查 `audit_logs` 与 `gateway_stream_missing_terminal` 日志，再读取 `audit_payload_refs.part_type='upstream_response'` 对应 blob 原文回放。

## 环境信息

- 分支 / 版本：当前工作区（Go gateway，未发布）
- 数据状态：生产 `juhe_dataset.audit_logs` / `audit_log_attempts` / `audit_payload_refs` 与审计 blob 原文
- 浏览器 / 系统 / Node 版本：与后端缺陷无关（客户端 Codex Desktop 0.166.0-alpha.6）
- 是否稳定复现：是（同一份 454,263 字节原文按任意 chunk 大小回放均稳定复现）

## 根因分析

- 表象：`gateway_stream_missing_terminal` 日志显示 `sseEventCount=161`、`sseEventTypeCounts` 中 `response.created=2`、无 `response.completed`，`recentSseEventTypes` 最后一项是 `response.created`。
- 真实根因：`backend-go/projects/gateway/internal/gatewayopenai/inspector.go` 的 oversized 临时状态跨事件泄漏：
  - `processLine` 对每条 `data:` 行调用 `rememberOversizedEventType(dataLine)`，为「当前事件后续可能超过 512KB 事件上限」预先记住类型；
  - `flushEvent` 正常收尾普通事件时只清理 `eventName`、`dataLines`、`dataBytes`，没有清理 `oversizedEventType` / `oversizedEventImage` / `oversizedEventUsage`；
  - `rememberOversizedEventType` 只在字段为空时赋值，`flushOversizedEvent` 直接使用该字段；因此后续超大事件沿用整条流第一条普通事件的类型。
- 为什么会发生：`response.completed` 是 Responses 协议中唯一携带完整 response 对象的终止事件，长输出时单行远超 256KB；Go 侧单行上限触发 oversized 轻量分类，而状态生命周期没有按事件边界闭合。BUG-0014 的「扩展根因」已记录该风险（普通事件结束后没有清理 oversized 临时状态），但 2026-09-04 Go 迁移重建时未落实清理。
- 为什么其他厂商不触发：OpenAI Chat Completions 终止帧为 `data: [DONE]`，Anthropic 为 `message_stop`，Gemini 为小型 finish 事件；正文都拆成小 delta，终止帧本身不会超过 256KB。OpenAI chat 与 Responses 共用同一 inspector，但 chat 的终止判定不依赖大事件，因此不表现为同型失败；Anthropic/Gemini 使用各自 inspector。该缺陷仍是 OpenAI inspector 的通用状态生命周期问题，只要其他路径出现超大单行事件同样可能命中。

## 修复方案

- 修改点：
  - `backend-go/projects/gateway/internal/gatewayopenai/inspector.go`：`flushEvent` 的普通事件出口（含无 data 的空事件出口）在事件结束时调用 `resetOversizedEventState()`，把 `oversizedEventType` / `oversizedEventImage` / `oversizedEventUsage` 收敛为下一事件初始态；oversized 出口继续由 `flushOversizedEvent` 自身 reset。
  - 新增 `backend-go/projects/gateway/internal/gatewayopenai/inspector_oversized_state_test.go`：覆盖「普通事件 + 超大 `response.completed`」与「无 `event:` 名 + 超大 `response.completed`」两条路径，断言终止识别与事件类型计数。
  - 新增 `backend-go/projects/gateway/internal/gatewayresponse/bug0353_oversized_terminal_test.go`：按真实生产形态从 `PipeUpstreamStream` 入口回归，断言普通 `response.created` + 超大 `response.completed` 按成功收尾且不产生失败记录。
- 行为影响：仅修正流检查器的旁路状态分类；主链路仍按原始字节透传，SSE 行/事件上限、usage 尾窗提取、oversized 分类集合与下游渲染均不变。无 schema、接口或配置变更，回退 gateway 镜像即恢复旧行为。
- 发布异常处理：纯 gateway 代码修复；随下次 gateway 发布生效，无需数据修复。若发布后仍出现同型审计，先核对 `sseEventTypeCounts` 是否仍有 `response.created` 重复计数，再按本档复现步骤重新取证。

## 同源场景审计

- `gatewayanthropic` / `gatewaygemini`：BUG-0354 之前两个 inspector 没有 `oversizedEventType/Image/Usage` 这一组旁路状态，`resetEvent` 覆盖当时的全部逐事件字段；BUG-0354 同批新增了 per-event `oversizedEventType/Failed/Output/Prefix` 状态，并统一在各自 `resetEvent` 清理，不存在本 bug 的「上一条事件类型泄漏」。两者原对单行/单事件超过 256KB 按 BUG-0314 语义整流跳过（整条流 `Skipped`），已由 [BUG-0354](问题-0354-跨协议超大SSE事件整流跳过致终止失败丢失.md) 改为 per-event oversized 续解析。
- OpenAI Chat Completions：与 Responses 共用 `gatewayopenai.StreamInspector`，因此本 bug 的 stale type 修复同时覆盖 chat 路径；chat 的终止帧 `[DONE]` 很小，通常不触发 oversized。超大未知类型 `data:` 行的整条流 `skipParsing` 同属 BUG-0354 的整流失明根因，已在该档改为只丢弃当前事件、后续继续解析。
- 桥接与拦截层（`openaicompatbridge`、`gatewayresponse.OpenAIStreamInterceptor`、`parseErrorEventIsolator`）：状态按流实例持有，不存在按事件缓存的 type/image/usage 临时字段；不参与本 bug 的终止分类。
- 结论：本 bug 所属的「逐事件 oversized 临时状态跨事件泄漏」在 `gatewayopenai` 内已按事件边界根治；Anthropic/Gemini 与 OpenAI chat 的超大事件整流失明属于 BUG-0354，已在同批按 per-event oversized 续解析处理。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 定向回归 | 普通事件 + 超大 `response.completed` | `go test -count=1 ./internal/gatewayopenai` | `TerminalReceived=true`、`response.completed=1`、`response.created=1` | 通过（`ok` 0.540s；修复前该用例实测 `TerminalReceived=false`、`response.created=2`） | 通过 |
| 相邻回归 | 流管道既有流式收尾与新增端到端用例 | `go test -count=1 ./internal/gatewayresponse` | 全绿 | 通过（`ok` 6.507s） | 通过 |
| 静态检查 | 改动包 vet | `go vet ./internal/gatewayopenai ./internal/gatewayresponse` | 无输出、退出 0 | 通过（exit 0） | 通过 |
| 构建验证 | gateway 全量构建 | `go build ./...`（`backend-go/projects/gateway`） | 通过 | 通过（exit 0） | 通过 |
| 失败语义保持 | 真缺失终止事件仍按失败收尾 | `go test -run TestPipeUpstreamStreamMissingTerminal ./internal/gatewayresponse` | `Completed=false`、`upstream_retryable_error` | 通过（清理仅作用于已识别终止事件的误分类，不改变真缺失终端的失败判定） | 通过 |
| 生产回放 | 454,263 字节原文回放 | 本机 inspector 回放（临时测试，跑完删除） | `terminal=true`、`events=161`、`last=response.completed` | 通过（修复前 `terminal=false`、`response.created:2`；修复后 `terminal=true`、`response.completed:1`） | 通过 |

## 复发记录

- 时间：2026-11-04
- 环境：生产 103.36.63.105，gateway 当前版本；`gpt-6.1-sol` / Codex Desktop
- 现象：`response.completed` 单行 399,567 字节被归为 `response.created`，审计 `stream_failed`
- 关联处理：本档修复；BUG-0014 的 Go 清账结论需按本档回正（其「扩展根因」未在 Go 实现中闭环）

## 下次遇到

- 先查什么：按 trace 查 `audit_logs.error_message` 与 `gateway_stream_missing_terminal` 日志，重点看 `sseEventTypeCounts` 是否出现不可能的重复类型（如 `response.created=2`）且缺 `response.completed`。
- 重点看什么：读取 `upstream_response` blob 的最后一条 `data:` 行长度，确认是否超过 256KB；再用同一 inspector 回放原文，对比 `TerminalReceived`、`LastEventType` 与 `EventTypeCounts`。
- 如何避免误判：这是网关旁路解析误判，不是上游截断；上游终止事件完整时不得按 `upstream_retryable_error` 记失败，也不得据此重启上游或更换厂商。

## 完成总结

- 完成时间：2026-11-04（已修复待发布）
- 结论：修复 oversized 临时状态跨事件泄漏，恢复超大 `response.completed` 的终止识别；定向回归、流管道端到端回归、vet 与 build 均通过。
- 后续建议：随下次 gateway 发布上线后，抽样审计中 `raw_payload_bytes` 较大的 Responses 成功请求，确认 `sseEventTypeCounts` 含 `response.completed` 且 `stream_failed` 同型错误归零。
