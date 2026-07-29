# BUG-0142 Codex 压缩失败终态误判缺少完成事件

## 基本信息

- 编号：BUG-0142
- 状态：本地修复完成，待统一上线/生产验证
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
