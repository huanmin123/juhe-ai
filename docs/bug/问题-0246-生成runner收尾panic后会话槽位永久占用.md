# BUG-0246 生成 runner 收尾 panic 后会话槽位永久占用

## 基本信息

- 编号：BUG-0246
- 状态：已修复（2026-09-30 批次）
- 严重程度：P2（低概率触发但无自愈，触发即该会话不可用至进程重启）
- 发现时间：2026-09-30
- 发现方式：全面审查（多子代理复审+主代理核验）
- 模块：后端（gateway / chat）
- 关联计划：无
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：生成 runner 在 orchestrator 之外的收尾代码 panic 时，completion 永不 close、hub 槽位永不释放。
- 期望：任意 panic 路径都保证 completion 关闭并触发槽位释放（onSettled）。
- 实际：panic 被 `safego.Recover` 吞掉（仅 recover+log），`finish` 不再可达。
- 影响范围：(a) stream handler goroutine 永久阻塞泄漏；(b) `GenerationHub.runners[conversationID]` 永久占用，该会话后续发送全部 409 chat_stream_conflict 直到进程重启；(c) SSE 订阅者收不到终态。

## 复现步骤

1. 在 runner 收尾阶段（orchestrator 之外、finish 调用之前）注入 panic；
2. 请求以 panic 日志结束，但客户端流不收终态、completion 不 close；
3. 同一 conversationID 再次发送——409 chat_stream_conflict，且不随时间恢复。

## 环境信息

- 分支 / 版本：master
- 数据状态：不涉及数据损坏（运行态槽位问题，进程重启即清）
- 是否稳定复现：注入 panic 后稳定；自然触发低概率

## 根因分析

- 表象：会话槽位被永久占用。
- 真实根因：`generation_runner.go` 的 `run`（约 :621）只有 `defer safego.Recover(...)`（仅 recover+log），`finish`（close completion + onSettled 释放 hub 槽位）仅在正常控制流三处调用；execute 闭包在 orchestrator 之外的收尾代码 panic 时被 Recover 吞掉，completion 永不 close、槽位永不释放。
- 为什么会发生：finish 的调用约定绑定在正常控制流上，defer 只做了 recover 没做收尾。

## 修复方案

- defer 内 recover 后补 finish（保证 panic 路径也 close completion 并触发槽位释放；注意与正常路径的幂等，避免双触发）。

## 验证结果
- `generation_runner.go`：`run` 的 `defer safego.Recover` 改为 `defer safego.Handle(..., func(recovered any){ r.finish(onSettled) })`（panic 后补 finish）；主体提取 settle/applySuccess/applyFailure，onUnexpectedError 回调改全程无锁执行、持锁段各自 defer Unlock——否则持锁段 panic 展开后 finish→onSettled→hub.rememberTerminalSnapshot 会重入死锁，槽位照样不释放。onSettled 幂等已核实（rememberTerminalSnapshot 先删后写同 key、deleteIfMatches 二次返回 false）。
- 新增 `bug0246_runner_panic_finish_test.go`：execute panic / onUnexpectedError panic → completion close + onSettled 恰一次（含 50ms 宽限防双触发回归）；正常路径恰一次；hub 集成：panic 后 GetRunner 不再 active、同会话第二个 runner Start 成功完成（原为永久 409）。
- `go test ./projects/gateway/internal/chat/ -count=1` 全绿；gofmt/vet 干净。限制：applySuccess/applyFailure 持锁段无外部注入点，其锁平衡由 defer 结构性保证。

## 防回归与观测性备注

- 补"收尾 panic 后槽位仍释放、同会话可再次发送"注入测试。
