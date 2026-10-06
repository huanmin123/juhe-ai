# BUG-0234：日志搜索 grep 时间范围被最早文件 mtime 钳成零宽

## 基本信息

- 编号：BUG-0234
- 状态：已上线（随 2026-10-06 前后批次发布；2026-10-06 状态同步）
- 严重程度：P2
- 发现时间：2026-09-29
- 发现方式：用户反馈
- 模块：网关 / 运行日志读取面（backend-go gateway internal/logreads）+ 前端日志搜索页
- 关联计划：无
- 关联 bug：无
- 责任人：主代理

## 问题概述

- 现象：用户报障「日志搜索时间范围调整不了、开始时间被强制对齐结束时间、默认应可筛今天」——grep 模式默认开始时间等于结束时间，用户手动修改时间范围不生效。
- 期望：默认窗口为最近 3 天（可覆盖今天），用户选择的时间范围不被后端改写。
- 实际：`/grep-options` 的 `defaultStartAt` / `defaultEndAt` 与 `/grep` 的窗口归一化被保留文件 mtime 强制改写，单活跃文件场景默认窗口退化为零宽；用户选 `endAt` 早于活跃文件 mtime 时该文件被排除，历史窗口搜不到。
- 影响范围：生产单机 go-only 形态的运行日志 grep 模式（文件日志 `juhe-ai.log` 超 100MB 才轮转、活跃文件 mtime≈now，恰好落入退化场景）；索引查询模式与其它读面不受影响。

## 复现步骤

1. 生产网关运行中（单一活跃 `juhe-ai.log`，mtime≈now），管理员打开日志搜索页切换 grep 模式。
2. `/grep-options` 返回的 `defaultStartAt` 被钳到 `earliestFileTime`（≈now），`defaultStartAt`≈`defaultEndAt`，前端默认零宽窗口、开始时间跟随结束时间。
3. 用户把 `endAt` 改到早于活跃文件 mtime（如今天 12:00、文件 22:17 仍在写），`/grep` 的文件筛选按 `mtime <= endMs` 上界排除该文件，返回「当前文件时间范围内没有可搜索的日志文件」。

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（gateway 单进程文件日志，`shared/platform/processlog/filesink.go` 单一 `juhe-ai.log`、超 100MB 轮转）。
- 是否稳定复现：是（单活跃文件未轮转期间必现）。

## 根因分析

- 表象：前端时间范围控件不可调、默认窗口零宽；后端历史窗口搜索无结果。
- 真实根因：`internal/logreads/runtime_grep.go` 的 `Options()`（/grep-options）与 `normalizeGrepTimeRange` 把保留文件 mtime 最小值 `earliestFileMs` 当成时间范围硬边界：
  1. `normalizeGrepTimeRange` 把 endMs 抬到 latestFileMs/earliestFileMs、把 startMs 钳到 earliestFileMs。单活跃文件时 earliest≈now → 默认 `DefaultStartAt`==`DefaultEndAt`≈now（零宽），前端开始==结束且改不动。
  2. `filterLogFilesByTimeRange` 的 `mtimeMs <= endMs` 上界把 mtime 晚于用户所选 end 的文件排除，历史窗口无文件可扫。
- 为什么会发生：该逻辑把「文件 mtime」误用作「文件内日志行的时间范围」边界；未轮转的活跃文件 mtime 持续前移，与文件内历史行时间脱节。

## 修复（契约先行：`docs/functions/安全与日志策略.md` grep 模式契约段同步修订；前后端同批交付，本文件记录后端部分）

- 后端 `runtime_grep.go`：
  - `normalizeGrepTimeRange` 签名简化为 `(startAt, endAt string)`，删除全部 earliestFileMs/latestFileMs 改写分支；归一化仅保留：endAt 缺省=now、endAt>now 截到 now、startAt 缺省=end-3 天、start>end 时 start=end-3 天（倒置修正）、窗口>7 天时 start=end-7 天。默认窗口由墙钟计算。
  - `filterLogFilesByTimeRange` 改相交下界语义：`file.mtimeMs >= startMs` 即参与扫描（mtime 早于窗口起点的文件不可能含窗口内行，仍排除；删除 mtime<=end 上界），保留 size>0 排除。
  - `Options()` 行为不变地返回 `earliestFileTime`（信息性字段：当前保留文件最小 mtime，前端不再消费），`defaultStartAt` / `defaultEndAt` 自然变为 [now-3d, now]。
- 前端（并行任务同批交付）：不再消费 `earliestFileTime` 钳制可选范围，日期选择下界改为文件保留期。
- 测试：`runtime_grep_test.go` 补 `defaultStartAt` / `defaultEndAt` 墙钟断言（fixture 文件 mtime=pinnedNow-30min 复现单活跃文件场景，默认窗口必须仍为 3 天宽）与「endAt 早于 fixture 文件 mtime 仍扫描全部文件」场景；`w11f_helpers_test.go` 中直接断言旧 mtime 钳制行为的用例按新契约修正（签名连带适配）。

## 验证记录

- `cd backend-go && go test ./projects/gateway/internal/logreads/...` 通过（原始输出见交付说明）。
- `cd backend-go && go build ./...` 通过。
- 前端回归脚本由并行任务执行（前后端同批交付）。

## 遗留

- 无 schema/env/compose 变更；发布后管理页刷新即生效。

2026-10-06 状态同步：滚动全量发布默认规则（修复已入库，随 2026-10-06 前后批次上线）；HEAD 复核「runtime_grep.go（logreads）」命中。
