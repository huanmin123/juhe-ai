# BUG-0142 Codex 压缩失败终态误判缺少完成事件

## 基本信息

- 编号：BUG-0142
- 状态：已关闭（2026-10-03，Node 时代遗留状态收口）
- 严重程度：P1
- 发现时间：2026-07-29
- 发现方式：生产审计定位，回归固化
- 模块：后端 / 网关 / Codex Responses / SSE
- 关联计划：PLAN-20260729T130300133Z
- 关联 bug：BUG-0060
- 责任人：待定

## 问题概述

- 现象：Codex Remote Compaction V2 收到精确 `response.failed` 后，暂存状态仍在 EOF 生成本地 compact 契约 mismatch。
- 期望：精确失败终态清空暂存并进入通用结构失败路径。
- 实际：失败事件未被 compact 状态机作为终态识别。
- 影响范围：Codex 兼容账户的 compact 预提交 SSE 失败收口。

## 复现步骤

1. 发送被识别为 Codex compact 期望的 Responses 流。
2. 在 `response.completed` 前发送一个暂存的 compact output 事件。
3. 发送精确 `response.failed` 并结束流。

## 根因分析

- 表象：EOF 本地 mismatch 覆盖了已到达的失败终态。
- 真实根因：compact 缓冲状态机只把 `response.completed` 视为终态。
- 为什么会发生：本地契约 EOF 收尾没有先区分精确失败事件身份。

## 修复方案

- 修改点：仅按 `eventType` 或 `eventName` 的精确 `response.failed` 判断失败终态；清空暂存，放行当前事件给既有通用结构失败管线。
- 行为影响：预提交失败统一得到脱敏 `upstream_protocol_failure`；成功 compact 校验与缺少两种终态的 EOF mismatch 保持不变。
- 发布异常处理：仅回滚本地状态机改动；不涉及数据迁移或生产操作。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | compact 精确失败终态与 EOF mismatch | `pnpm --filter juhe-ai-backend test:response-inspection-policy` | 结构失败与既有契约回归通过 | 通过 | 通过 |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 通过 | 通过 |
| 类型检查 | 工作区类型检查 | `pnpm typecheck` | 通过 | backend 与 frontend 通过 | 通过 |
| 差异检查 | 补丁空白检查 | `git diff --check` | 通过 | 通过 | 通过 |

## 下次遇到

- 先查什么：Codex compact 缓冲中的精确终态判定和通用 SSE 结构失败管线。
- 重点看什么：不得让本地 EOF 契约覆盖已到达的精确失败终态。
- 如何避免误判：失败终态只按协议事件身份判断，payload 错误字段不是终态依据。

## 完成总结

- 完成时间：2026-07-29
- 结论：本地修复完成，待统一上线/生产验证。
- 后续建议：统一发布前复核真实环境的 Codex compact 失败链路。

## 关闭记录（2026-10-03）

本档按 Node 后端时代（pnpm/juhe-ai-backend）口径收口，不作为 Go 后端的待办：

- 修复载体已消失：Node 后端已于 2026-09-05 完成全量 Go 迁移并清零，本档 2026-07-29 的 Node 侧补丁不存在「待上线」对象。
- Go 实现复核无此病灶（2026-10-03，代码级核查）：Go 的压缩契约检查为逐事件即时判定——`gatewayresponse/interceptor.go` 只对 `response.output_item.done` 事件调用 `CountCodexCompactionOutputItemsFromStreamEvent` 计数并判定 mismatch（`gatewayresponse/codexcontract.go`）；`response.failed` 事件不进入 compact 计数，即时经 `ExtractSseSemanticFrames` 走通用结构失败管线；EOF 收尾（`FlushPendingOnEOF`）只冲残留缓冲字节，无本地 compact mismatch 补判步骤。本档病灶依赖的「EOF 本地补判且不区分失败终态」形态在 Go 架构中不存在，精确失败终态天然进入通用失败路径（与 Node 补丁的目标行为一致）。
- 处置：状态关闭，档案保留（BUG-0060/0142 的经验教训仍有效：失败终态只按协议事件身份判断，不得让本地契约收尾覆盖已到达的精确失败终态）。
