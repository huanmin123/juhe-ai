# BUG-0209：jobsched lane 交接自死锁——被唤醒任务重排队，lane 永久 busy

- 编号：BUG-0209
- 状态：已修复（待发布）
- 发现：2026-09-28 发布后验证 BUG-0206 收口时，recovery 首轮后不再触发；/health 任务快照显示 `external-account-maintenance` 与 `stats-online` 两个 lane 全体成员 `QueuedForLane: true`、`LastSkipReason: resource_lane_busy`、无人 Running

## 根因

`releaseLane` 的交接契约：释放时把 `lane.runningJob` 置为队首任务名并唤醒它（保持 FIFO）。但被唤醒任务的 `fire → acquireLane` 只认空 lane——看到 `runningJob` 非空（恰恰是自己的名字）即判 lane 忙：在 `OverlapCoalesceOne` 下把自己**重新入队**并返回失败。结果：

- 被交接任务永不执行、永不释放；`lane.runningJob` 永久指向它；
- 同 lane 后续全部任务永久 `resource_lane_busy`（跳过仅 Debug 级，无告警）；
- `TestReleaseLaneHandsOffQueuedJob` 只测了 release 侧标记、未组合「唤醒后 fire」路径，`TestW12CFireOnLaneWake` 又只在空 lane 上验证——组合缺口使缺陷存活。

生产影响（回溯）：jobs 每次重启后，lane 上第一次交接即冻结。本轮 08:30 发布后 `external-account-maintenance`（余额探测补偿/ OAuth 刷新/xai 用量）与 `stats-online`（用量统计聚合/分组账户统计/账户质量）双双只跑了一轮即停摆；BUG-0206 的剩余 7 个候选因此无人推进。昨晚 23:24 后 recovery 轮次消失也是同一机制。

## 修复

`acquireLane` 增加交接再入认领：`lane.runningJob == job.spec.Name` 时视作交接所有权直接获取（唯一可达路径就是 releaseLane 交接唤醒；同任务不可能在持有时并发二次 fire——jobLoop 单线程且 overlap 检查在前）。

## 验收

- `TestSchedulerLaneHandoffRunsQueuedJob`（新增组合回归）：holder 占 lane 阻塞 → follower 排队 → holder 释放交接 → follower 必须真正执行一轮（缺陷形态 SuccessCount 恒 0）且执行后 lane 干净。
- jobsched 全包测试绿（含 0202 超时回收、lane busy Debug、交接标记既有契约）。
- 发布后生产：/health 快照中两个 lane 的成员恢复轮转（RunCount 推进、无持续 lane_busy）；余额探测补偿轮次恢复，候选集清零。

## 状态

已修复，随 BUG-0206/0208 同批发布验证。
