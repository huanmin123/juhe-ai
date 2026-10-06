# BUG-0285 usage-hot-window-refresh 任务超时，usage_scope_range_windows 冻结三天

> 本记录为 2026-10-06 发布后生产巡检发现。与当日发布（`c3f598249`，授权功能批次）无关——冻结起点早于发布两天半，且有表级数据实证；按证据先行登记，根因待排查。

## 基本信息

- 编号：BUG-0285
- 状态：已修复（随 2026-10-06 18:0x jobs 单进程发布上线，生产实证）
- 严重程度：P2（当前用户可见影响≈无，但注册表声明该任务 `BlocksUserVisibleFreshness: true`，统计页新鲜度口径受影响；且 jobsched 每 30~65s 空转重试浪费 stats-heavy 车道）
- 发现时间：2026-10-06 17:50
- 发现方式：发布后生产巡检（jobs WARN `jobsched_run_timeout` 定性）
- 模块：后端 / jobs statsagg 热窗口刷新
- 关联 bug：无
- 责任人：本会话代理

## 问题概述

- 现象：`usage-hot-window-refresh`（registry 写面声明 `stats:usage_overview_summary_windows / usage_overview_trend_windows / usage_scope_range_windows`，调度 `Timeout: 35s`，`Lane: stats-heavy`）每次运行在 35s 被杀，`consecFail` 持续累计（17:42 观测 16，jobs 容器 17:20 重建后计数从零起——即新进程内从未成功）。
- 数据实证：`juhe_stats.usage_scope_range_windows.max(updated_at) = 2026-10-03T14:18:16Z`（北京 10-03 22:18）后冻结三天；同任务声明的 overview 写面有独立任务 `usage-overview-windows-refresh` 维持新鲜（`usage_overview_summary_windows / usage_overview_trend_windows` max(updated_at) 实时），掩盖了本任务失败。
- 排除项：
  - 非本次发布引入——冻结起点（10-03 22:18）早于 10-06 发布约两天半；
  - 非 10-03/10-04 代码变更引入——`git log` 该窗口内 statsagg/jobregistry 零提交（唯一 `29dede24e` 视频媒体批次提交于 10-04 10:59，晚于冻结起点）；
  - 非表体量问题——`usage_scope_range_windows` 仅 6230 行 / 15MB。
- 影响范围：
  - `usage_scope_range_windows` 的消费面 = gateway authz `usage_detail`（`{id}/usage` per-scope 摘要与成员 fallback 链），该端点当前无 UI 消费方；团队/用户消耗明细页已改直读日摘要表不受影响；
  - `BlocksUserVisibleFreshness: true` 的失败对统计页新鲜度提示口径的影响待核（是否已在前端呈现"统计延迟"横幅三天）。

## 复查步骤

1. 捕获超时运行的实际在库语句：jobsched 退避至 5min 上限后，在 5 分钟窗口内对 `pg_stat_activity` 轮询抓拍 jobs 会话（`state <> 'idle' AND query ILIKE '%scope_range%' OR '%overview%'`），确认卡在 DELETE 还是 INSERT...SELECT 及其执行计划。
2. 核对 watermark 跳过分支：`windows.go` 的 source watermark + refreshDate 双判定是否可能既不跳过（判定为"有变化"）又写不动（如行锁/索引膨胀）。
3. 检查 10-03 22:18 前后 PG 侧事件（pgbouncer/PG 日志、锁等待、autovacuum）定位突变量；对照 `usage_stats_hourly` 行数增长曲线验证"35s 阈值被数据增长突破"假设。
4. 评估 35s Timeout 是否本身过紧（同族 `authorization-usage-range-windows-refresh` 历史配 10min timeout、`usage-overview-windows-refresh` 节拍更慢），以及 scope 阶段是否应拆出热车道或降频。

## 环境信息

- 生产：国内单机 Docker 103.36.63.105，jobs `edcf07dd`（2026-10-06 17:20 发布，含本次重启清零计数）；PG 17（compose，shared_buffers=2GB）。
- 稳定复现：是（每次调度运行超时）。

## 根因分析

- 表象：热窗口任务 35s 超时循环。
- 真实根因（已定案，2026-10-06）：任务阶段构成为 `[overview, scope]`，而 overview 阶段在当前生产数据量下实测每轮耗时 ~52-55s（`usage-overview-windows-refresh` 的 background_task_runs duration_ms 实证：55117/53906/54217/54046ms），35s 超时永远死在第一个阶段，scope 阶段从未执行。overview 阶段成本随 `usage_stats_hourly`（现 23 万行）持续增长，于 10-03 22:18 越过 35s 阈值；overview 写面被独立任务 usage-overview-windows-refresh（Timeout 10min）掩护，failure 被掩盖三天。排除项：源表 watermark 全表拉取（hourly 23 万行实测 179ms）、表锁泄漏（无 >2s 龄锁）、表体量（scope 表 15MB）、lane 排队（QueuedForLane=false）、代码变更（冻结窗口内 statsagg/jobregistry 零提交）均非根因。


## 修复方案

- 已实施（2026-10-06）：装配层把热任务阶段缩为 scope 单阶段——`worker_assembly.go` hotUsageWindowStages() 只返回 StageUsageScopeRangeWindows；`jobregistry/registry.go` 该任务 Writes 缩为 stats:usage_scope_range_windows 一项、GoBinding 同步。理由：overview 阶段产出已被 usage-overview-windows-refresh（Interval 5min / Timeout 10min）每 5 分钟完整覆盖，热任务里的 overview 是纯重复计算；scope 聚合 SQL 实测 15ms，35s 超时绰绰有余。测试：`wg_assembly_unit_test.go` 断言精确锁定单阶段；statsagg/jobregistry/cmd 三包 -count=1 绿。
- 遗留观察项（已闭环，2026-10-06 18:4x）：overview 阶段 55s 成本 pathology 已随第二次 jobs 补丁发布修复——根因为语句数爆炸（每 scope 实际迭代 496 个区间 × 单行 INSERT，48 scope ≈ 数千语句/轮，含 trend 桶与排名各最多 4960 单行），非单查询慢。修复：summary/trend/model rank/error rank 四类 INSERT 合并为多行 VALUES（200 行分片，行集/行值/行序逐字节不变），生产实证 duration_ms 51301/51979/50848 → **16390ms（3.1 倍）**，窗口新鲜度不变；发布窗口伴随两条瞬态告警（F1 索引器 55P03 锁超时、一个同步任务一次超时）均自动重试自愈。
- 回滚预案：无需——修复为减法（去掉重复阶段），回滚即恢复旧装配（build/bin/*.bak-* 可用）。


## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 数据实证 | scope 窗口冻结 | `SELECT max(updated_at) FROM juhe_stats.usage_scope_range_windows` | 持续前进 | 2026-10-03T14:18:16Z 冻结 | 已证实 |
| 排除 | 本批发布引入 | 冻结起点 vs 发布时间 + statsagg 提交清单 | 发布前已冻结 | 是 | 已证实 |
| 根因 | 在库语句抓拍 + 同阶段任务耗时对照 | pg_stat_activity 6s 轮询 + background_task_runs duration_ms | 定位卡点 | overview 阶段 ~55s > 35s 超时，run 死在第一阶段 | 已证实 |
| 修复验证 | 生产实证 | deploy.sh jobs 后 health 快照 + 表新鲜度 | 任务 success、scope 表前进 | LastOutcome=success / 3178ms / SuccessCount=1 / scope max(updated_at)=10-06 18:07 | 已证实 |

## 复发记录

- 时间：2026-10-06 17:20 起观测；实际起点 2026-10-03 22:18 后。
- 环境：生产单机。
- 现象：每次调度 35s 超时（`durationMs:35001`），overview 写面由独立任务掩护。

## 下次遇到

- 先查什么：`max(updated_at)` 冻结的写面表 vs registry `Writes` 声明、`docker logs juhe-ai-go-jobs | grep jobsched_run_timeout`。
- 重点看什么：Timeout 配置与实际运行时长、watermark 跳过分支、源表增长。
- 如何避免误判：容器重建会清零 `consecFail`——连败计数只能证明"本进程内未成功"，跨发布归因必须查表级数据新鲜度。

## 完成总结

- 完成时间：2026-10-06 18:0x（jobs 单进程发布，公网健康 200、verify-release PASS）
- 结论：装配层移除热任务中的重复 overview 阶段，scope 写面恢复实时刷新；生产实证任务 3.2s 成功、scope 窗口解冻。遗留观察项：overview 阶段 55s 成本优化（独立任务承载，暂有富余）。
