# BUG-0025 API Key 时间计划提前启用无效

## 基本信息

- 编号：BUG-0025
- 状态：已修复
- 严重程度：P1
- 发现时间：2026-06-17
- 发现方式：用户反馈
- 模块：前端 / 后端 / 存储 / 网关 / API Key
- 关联计划：无
- 关联 bug：BUG-0024
- 责任人：待定

## 问题概述

- 现象：API Key 配置时间计划后，当前不在允许时段内；用户人工提前启用，但网关请求仍然无效。
- 期望：人工启用可以立即提前放行当前 API Key，人工关闭可以在计划内提前拒绝当前 API Key，后续仍由下一次计划开始 / 结束边界继续接管计划状态。
- 实际：人工启用只改写 `api_keys.status`，`api_keys.availability_schedule_active` 仍为 `0`；网关认证同时要求 `status = active` 和派生计划状态为可用，因此继续拒绝。
- 影响范围：启用了 API Key 时间计划，且需要计划外临时提前启用的本地网关 API Key。

## 复现步骤

1. 创建一个 API Key，配置每天 `22:00-23:55` 允许窗口，并绑定可用分组。
2. 在允许窗口外进入 `API Key 管理`，执行启用或提前启用。
3. 使用该 API Key 调用网关。
4. 旧行为下列表仍显示计划外停用，网关继续拒绝该 Key。

## 环境信息

- 分支 / 版本：`F:\sub2api-lite` 当前工作区。
- 数据状态：API Key 已配置 `availability_schedule_json`，且 `availability_schedule_active = 0`。
- 浏览器 / 系统 / Node 版本：Windows / Node 22 系列。
- 是否稳定复现：是。

## 根因分析

- 表象：页面允许人工启用，但启用后请求仍被时间计划拦截。
- 真实根因：BUG-0024 的连续补偿修复把时间计划变成了“每轮按当前时间硬覆盖派生状态”，而人工启用只写 `status`，没有同步恢复 `availability_schedule_active`。
- 为什么会发生：设计文档、前端文案和后端实现存在分歧；前端表达的是“开始 / 结束边界切换，边界后手动干预不被持续覆盖”，后端实际实现成了持续按当前时段补偿。

## 修复方案

- 修改点：
  - `backend/src/storage/api-key.repository.ts`：创建或编辑时间计划时按当前时间初始化 `availability_schedule_active`；提交 `availabilityScheduleActive: true/false` 时立即改写派生计划状态，用于计划外提前启用或计划内提前关闭；提交 `status: active` 时仍同步置为派生可用。
  - `backend/src/storage/api-key-schedule-status-sync.repository.ts`：同步任务改为只处理开始 / 结束边界事件，并写入 `api_key_schedule_status_events` 去重，不再在非边界时间按当前时段持续覆盖。
  - `frontend/src/views/api-keys/useApiKeyRowActions.ts`：计划外但人工状态仍为启用时，更多菜单展示“提前启用”。
  - `frontend/src/views/api-keys/apiKeyFormatters.ts`：计划外提示改为可提前启用。
  - `docs/functions/`：同步 API Key 时间计划的当前语义。
- 行为影响：时间计划保存时仍会立即按当前时间初始化可用性；之后只有计划边界或人工干预会改变派生状态。人工提前启用后，下一次计划结束边界会再次关闭；人工提前关闭后，下一次计划开始边界会再次打开。
- 发布异常处理：如果已有 Key 被计划派生状态挡住，可以在页面执行“提前启用”或提交 `availabilityScheduleActive: true`；如果需要计划内提前关闭，可以执行“提前关闭”或提交 `availabilityScheduleActive: false`。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | API Key 时间计划边界、人工提前启用 / 提前关闭和网关校验 | `pnpm --filter juhe-ai-backend test:api-key-availability-schedule` | 通过 | 通过 | 已通过 |
| 回归验证 | 前端状态 formatter 提示 | `pnpm --filter juhe-ai-frontend test:account-status-formatters` | 通过 | 通过 | 已通过 |
| 类型检查 | 代码类型检查 | `pnpm typecheck` | 通过 | 通过 | 已通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查 API Key 行的 `status` 与 `availabilityScheduleActive` 是否不一致。
- 重点看 `updateApiKey()` 是否收到 `availabilityScheduleActive`，以及保存后 `availability_schedule_active` 是否按目标布尔值变化。
- 避免误判为纯前端问题：网关认证真正读取的是人工 `status`、派生计划状态、过期时间和系统账户状态。

## 完成总结

- 完成时间：2026-06-17
- 结论：根因是时间计划同步从边界事件变成持续补偿后覆盖了人工干预；已改回边界事件语义，并让人工启用 / 关闭都通过派生计划状态立即生效。
- 后续建议：后续调整时间计划时必须同时验证“计划边界自动切换”和“边界后人工启用 / 关闭”两个方向。
