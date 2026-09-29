# BUG-0236 jobsched 退避基数位移溢出致 backoff 被跳过

## 基本信息

- 编号：BUG-0236
- 状态：待验证（代码已修复，待生产部署验证）
- 严重程度：P3（行为影响轻微：本案例任务 interval 与 BackoffMax 同为 10 分钟，节奏无可见变化；日志字段异常）
- 发现时间：2026-09-29
- 发现方式：自查（生产巡检日志字段 `backoffMs` 出现约 -24.8 年等巨大负值）
- 模块：后端（jobs / jobsched）
- 关联计划：无
- 关联 bug：BUG-0235（同一失败日志中暴露）
- 责任人：待定

## 问题概述

- 现象：`jobsched_run_failed` 日志的 `backoffMs` 字段为巨大负数（如 -781766978154、-3355047297637）。
- 期望：`backoffMs` 为正的下次重试退避毫秒数，封顶 `Backoff.Max`。
- 实际：连续失败次数（consecFail）较大时退避目标时间被算到过去，`shouldBackoff` 判定失效，任务退避被跳过（按普通 interval 节奏继续重试）。
- 影响范围：jobsched 全部配置了 Backoff 的周期任务在连续失败 20+ 次后的退避行为；不影响成功任务与网关。

## 根因分析

- 真实根因：`scheduler.go` `backoffTargetLocked` 用 `Base << uint(exponent)` 计算退避上限，exponent 钳到 30；当 Base=30s（chat-retention-cleanup 配置）时 `30e9 ns << 30 ≈ 3.2e19` 溢出 int64 为负，负值又绕过其后 `ceiling > max` 的封顶钳制，`now.Add(负ceiling)` 使 backoffUntil 落到过去。
- 表现链：backoffUntil 在过去 → `backoffMs = backoffUntil - finishedAt` 为巨大负数 → `now.Before(backoffUntil)` 恒 false → 退避跳过。

## 修复方案

- 位移计算改为翻倍循环：`ceiling` 每轮左移一位，达到 `max` 或临近溢出（> `1<<62`）即截止；移除 exponent=30 钳制（循环天然受 exponent 与溢出双上限约束）。
- 修复位置：`jobsched/scheduler.go` `backoffTargetLocked`。

## 验证结果

- 新增回归测试 `TestW12CBackoffTargetNoOverflow`（生产参数 Base=30s/Max=10min，consecFail=1/5/31/38/40/100，断言退避目标落在 `(now, now+Max]`）——通过。
- `jobsched` 包全量测试通过。

## 防回归备注

- 溢出用例并入 `w12c_jobsched_arms_test.go`；若后续调大 Backoff.Base 或 Max，该测试参数需同步覆盖。
