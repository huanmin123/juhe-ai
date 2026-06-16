# BUG-0024 API Key 时间计划错过边界未停用

## 基本信息

- 编号：BUG-0024
- 状态：已修复
- 严重程度：P1
- 发现时间：2026-06-16
- 发现方式：用户反馈
- 模块：后端 / 网关 / worker / API Key
- 关联计划：无
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：API Key 配置了允许窗口，例如每天 `22:00-23:55`，到停止时间后列表仍显示运行状态为“启用”，网关继续接受该 Key 并访问绑定分组。
- 期望：到计划时段外后 API Key 不应继续进入任何分组路由；后台状态同步即使错过边界分钟，也应在下一轮补偿停用。
- 实际：后台任务只在开始 / 结束的精确分钟生成状态事件；如果 maintenance-worker 在该分钟未执行成功，后续扫描不会补偿。网关校验只看人工状态和过期时间，没有独立的计划派生运行态，因此旧状态会继续放行。
- 影响范围：所有配置 API Key 时间计划的入口 Key；同类风险也覆盖账户时间计划派生候选状态。

## 复现步骤

1. 创建一个 API Key，配置每天 `22:00-23:55` 允许窗口，并绑定可用分组。
2. 让后台同步任务错过 `23:55` 这一分钟，或在 `23:56` 后才恢复运行。
3. 使用该 API Key 调用网关。
4. 旧行为下 `api_keys.status` 仍为 `active`，网关继续放行。

## 环境信息

- 分支 / 版本：`F:\sub2api-lite` 当前工作区，生产 release 来自该仓库打包。
- 数据状态：API Key 已配置 `availability_schedule_json`。
- 浏览器 / 系统 / Node 版本：后端 Node 22 系列。
- 是否稳定复现：在同步任务错过边界分钟时稳定复现。

## 根因分析

- 表象：页面显示“时间计划等待窗口开启”，但运行状态仍是“启用”，并且绑定分组仍有调用日志。
- 真实根因：`syncApiKeyAvailabilityScheduleStatuses()` 依赖 `dueApiKeyAvailabilityScheduleEvent()`，只在 `current.minuteOfDay === start/end` 时更新状态；错过边界分钟后不会按当前应有状态补偿。请求热链路只应读取已派生的运行状态，不能每次解析计划 JSON，因此后台状态同步必须自己兜住边界补偿。
- 为什么会发生：已有回归只验证了“刚好在边界分钟执行会改状态”，没有覆盖“边界后恢复执行也要补偿”和“状态同步后必须清理网关运行缓存”。

## 修复方案

- 修改点：
  - `backend/src/storage/api-key-schedule-status-sync.repository.ts`：同步任务每轮按当前时间计算计划是否允许，只写 `api_keys.availability_schedule_active`，不覆盖人工启停 `status`；时段外补偿派生停用，时段内恢复派生可用，保留窗口中间人工停用语义。
  - `backend/src/storage/account-availability-schedule-status-sync.repository.ts`：新增账户时间计划派生状态同步，把 `accounts.availability_schedule_active` 作为网关候选 SQL 的轻量过滤字段。
  - `backend/src/storage/gateway-api-key.repository.ts`、`backend/src/storage/openai-account-selector.repository.ts`、`backend/src/modules/gateway/runtime/runtime-cache.service.ts`：请求热链路不解析时间计划 JSON，不再用计划分钟边界缩短 runtime cache；只读取 API Key / 账户的人工状态、派生计划布尔字段和到期时间。已加载运行态采用软过期缓存，软过期请求继续使用内存快照并后台刷新，返回前按当前时间过滤已过期 API Key、授权和账号。
  - `backend/src/modules/background/background-jobs.ts`：maintenance-worker 定时同步 API Key 和账户计划派生状态，变更后清理网关运行缓存。
  - `backend/src/scripts/regression/api-key-availability-schedule-regression.ts`：补充错过停止 / 开启边界后的补偿同步，以及状态尚未同步时允许一个同步周期延迟。
- 行为影响：带时间计划的 API Key 和账户不再在每次网关请求内解析计划；派生状态切换由后台同步任务维护，允许最多一个 worker 同步周期的延迟。窗口中间人工停用不会被下一轮同步立即打开。
- 发布异常处理：若线上已经出现未停用 Key，部署后下一轮 maintenance-worker 会补偿状态；部署前可先手动停用止血。

## 横向排查

- AI 账户时间计划：由 `account-availability-schedule-status-sync` 写入 `accounts.availability_schedule_active`；网关候选 SQL 只读派生布尔字段，不在请求链路解析时间计划。状态变化后由后台任务清理 runtime cache，允许一个同步周期延迟。
- 账户到期时间：网关候选 SQL 对账户自身和授权来源账户都带 `account_expires_at > now` 硬过滤；已加载账号快照软过期后仍会在返回前按 `accountExpiresAt` / token `expiresAt` 内存过滤，避免到期后被缓存续命。
- 资源授权到期：分组授权、账户授权和批量授权读取均带 `expires_at > now` 条件；已加载分组访问元数据和账号候选软过期后仍会在返回前按授权到期时间内存过滤。
- IP 封禁策略到期：网关请求路径只读 server 内存 active policy 快照；命中来源级缓存或快照后仍会按 `expiresAt` 本地判断，TTL 截到策略过期点，stats-worker 周期推送快照兜底清出过期策略。
- 手动账户测试、模型检测和冷却复测会显式传 `ignoreAvailability: true`，属于诊断 / 恢复路径，不是普通 API Key 网关请求入口。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | API Key 时间计划边界、补偿和热链路不解析计划 | `pnpm --filter juhe-ai-backend test:api-key-availability-schedule` | 通过 | 通过 | 已通过 |
| 回归验证 | 账户时间计划保存、展示和网关候选过滤 | `pnpm --filter juhe-ai-backend test:account-availability-schedule` | 通过 | 通过 | 已通过 |
| 回归验证 | 网关运行态缓存与 API Key 计划、账户计划、API Key 到期、账户到期和授权到期边界 | `pnpm --filter juhe-ai-backend test:gateway-runtime-cache` | 通过 | 通过 | 已通过 |
| 回归验证 | IP 封禁策略过期后精确查询、列表和来源级缓存不继续命中 | `pnpm --filter juhe-ai-backend test:client-ip-stats` | 通过 | 通过 | 已通过 |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 通过 | 已通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查 API Key 列表的 `status` 与 `availabilityScheduleActive` 是否不一致。
- 重点看 `api-key-availability-schedule-status-sync`、`account-availability-schedule-status-sync` 的 `lastSuccessAt`，以及同步后是否清理了网关 runtime cache。
- 避免误判为前端显示问题：列表计划标签是按计划派生展示，运行状态 / 账户候选派生字段是网关事实；长时间不一致说明后台同步或缓存失效需要检查。

## 完成总结

- 完成时间：2026-06-16
- 结论：根因在后台边界事件同步缺少补偿，已改为停用补偿、开启事件补偿，并把 API Key / 账户时间计划统一收敛为后台派生运行状态，网关热链路不解析计划。
- 后续建议：上线后观察 maintenance-worker 快照中的 `api-key-availability-schedule-status-sync`、`account-availability-schedule-status-sync` 成功时间，以及时段外 API Key 或账户是否仍有网关成功记录。
