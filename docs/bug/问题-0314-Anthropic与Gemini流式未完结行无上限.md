# BUG-0314 Anthropic/Gemini 流式未完结行无上限

## 基本信息

- 编号：BUG-0314
- 状态：已修复（2026-10-14，待发布）
- 严重程度：P1（安全 / 资源边界）
- 发现时间：2026-10-14
- 发现方式：流式管道复审（BUG-0313 Tier 1 落地后的保形复核）
- 模块：后端 / 网关 / 流式转发 / 协议 inspector
- 关联计划：无
- 关联 bug：BUG-0014（OpenAI 侧同类行上限的完整修复）、BUG-0313（inspector 内存有界性核查）
- 责任人：AI

## 问题概述

- 现象：Anthropic / Gemini 协议 `StreamInspector` 对持续无换行的 SSE 数据把分段持续 `append` 到未完结行缓冲（`pendingLine`），没有任何长度上限，内存随输入无界增长。
- 期望：未完结行缓冲必须有与 OpenAI 侧一致的 256KiB 行级上限；超限按协议既有"事件超限"语义整流跳过并报告原因。
- 实际：256KiB 限制只在完整 `data:` 行进入 `processLine` 后生效（`dataBytes > streamInspectorMaxEventBytes` 检查）；行未完结期间无任何检查，异常上游可让单个活跃流的 inspector 缓冲无上界增长，唯一自然止境是管道层读取超时。
- 影响范围：Anthropic `/v1/messages` 与 Gemini `/v1beta` 流式响应的旁路解析（检查状态、usage 提取、终止事件识别）；主转发路径按原始字节透传，不受 inspector 跳过影响。

## 复现步骤

1. 构造 `gatewayanthropic.StreamInspector`（或 `gatewaygemini` 同名 inspector）。
2. 持续 `PushChunk` 不含 `\n` 的数据（如先推 `data: ` 再循环推 4KiB 填充）。
3. 修复前：`pendingLine` 随输入线性增长（实测推至 262150 字节仍无跳过）；修复后：缓冲不超过 256KiB 行上限，超限立即 `Skipped` 并报告 `anthropic_stream_event_too_large` / `gemini_stream_event_too_large`。

## 根因分析

- 表象：无换行输入下 inspector 内存增长无上限；`gateway_stream_inspector_skipped` 只在行完结后才可能出现。
- 真实根因：BUG-0313 Tier 1 把 pending 行改为 `[]byte` 缓冲（消除每 chunk string 拷贝）时逐分支保形，只保留了既有语义，未同步补 OpenAI 侧已存在的 `streamInspectorMaxLineBytes` 行级上限。
- 为什么会发生：OpenAI 侧行上限来自 BUG-0014 的完整修复（行 256KiB 前缀截断 + 超大事件分类）；anthropic/gemini 的超限语义当时只绑定"完整事件"（256KiB 事件上限），行级边界在协议迁移与消拷贝两轮中均未覆盖。
- 触发链路：上游响应 chunk → `pushBytes` 逐行扫描 → 无换行分段 `append` 到 `pendingLine` → 修复前无任何上限检查。

## 修复方案

- 修改点：
  - `backend-go/projects/gateway/internal/gatewayanthropic/streaminspection.go`：新增 `streamInspectorMaxLineBytes = 256 * 1024`；`pushBytes` 在 append 前检查 `len(pendingLine)+len(segment) > 上限`，超限即 `Skipped = true` + 既有 `anthropic_stream_event_too_large` 原因 + 释放缓冲 + `resetEvent`，与事件超限同语义。
  - `backend-go/projects/gateway/internal/gatewaygemini/streaminspection.go`：同构修复（`gemini_stream_event_too_large`）。
- 语义说明：单行超限与事件超限同语义（整流跳过并报告既有原因）——与两包既有"超大事件跳过契约"一致；跳过后 `PushText` / `PushChunk` / `PushParsedEvent` / `Finish` 全部短路，缓冲随跳过释放。
- 行为影响：单行超过 256KiB 的输入（无论是否换行）从"仅完整 `data:` 行触发事件超限跳过、非 data 行被忽略"统一为"立即整流跳过"；256KiB 内行为不变，OpenAI 侧行为不变。
- 回归测试：`TestWAStreamInspectorNoNewlineLineCap`（持续无换行推入 2×上限：缓冲始终 ≤ 上限且最终跳过）、`TestWAStreamInspectorLineCapBoundary`（恰为上限不跳过、超 1 字节跳过），两包各一对；修复前必红（`pendingLine` 越界累积 262150 / 超限行未跳过），修复后绿。复审补充边界用例：`TestWAStreamInspectorLineCapCrossChunk`（跨 chunk 累计恰达上限不跳过、再 +1 字节跳过且缓冲释放）、`TestWAStreamInspectorOversizedLineWithNewline`（同 chunk 超限内容带换行完结：预追加检查先于行处理触发，整流跳过且不分类事件）、`TestWAStreamInspectorOversizedEventPrefixLine`（`event:` 前缀行同判、跳过后继续推入短路），两包各一对；两包全量绿。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | 行上限用例红→绿（含复审补充边界） | `go test ./internal/gatewayanthropic/ ./internal/gatewaygemini/ -run 'LineCap|Oversized' -count=1`（Git Bash 下 `\|` 为字面量，应写 `-run 'LineCap|Oversized'`） | 修复前红、修复后绿 | 符合 | 通过 |
| 包测试 | 五包全量 | `go test ./internal/gatewayproto/ ./internal/gatewayopenai/ ./internal/gatewayanthropic/ ./internal/gatewaygemini/ ./internal/gatewayresponse/ -count=1` | 全绿 | 全绿 | 通过 |
| 静态检查 | go vet + gofmt | `go vet ./internal/gatewayanthropic/ ./internal/gatewaygemini/`；`gofmt -l` | 干净 | 干净 | 通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：`gateway_stream_inspector_skipped` 的 `skipReason` 与上游是否出现超大单行（大 base64 图像、大 JSON 事件）。
- 重点看什么：三个协议 inspector 的行 / 事件上限语义是否仍一致（openai 行 256KiB + 事件 512KiB 分类处理；anthropic/gemini 行 256KiB + 事件 256KiB，超限整流跳过）。
- 如何避免误判：主转发路径不受 inspector 跳过影响；skip 只影响旁路检查状态、usage 与终止识别。

## 完成总结

- 完成时间：2026-10-14
- 结论：Anthropic/Gemini inspector 未完结行缓冲补齐 256KiB 行级上限，超限整流跳过并报告既有原因；两包新增红绿回归用例，五包全量测试与 `go vet`/gofmt 全绿。
- 后续建议：若后续需要 Anthropic/Gemini 图像流大事件继续可解析（对齐 BUG-0014 的 oversized 事件语义），应另立设计任务评估 oversized 事件分类与 usage 尾窗提取。
