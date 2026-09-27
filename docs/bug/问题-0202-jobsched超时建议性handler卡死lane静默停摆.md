# BUG-0202：jobsched 超时是建议性的，handler 卡死永久占死 lane 且停摆不可见

## 现象

2026-09-27 生产（容器 08:40 启动）：

- `external-account-maintenance` lane 自 08:45:42 起静默停摆 9 小时+：`account-balance-auto-detect-recovery` 完成 6 轮后再无任何日志、`xai-grok-usage-refresh` 仅启动轮执行一次、`openai-oauth-access-token-refresh` / `oauth-keepalive-token-refresh` 从未有 INFO——全部零 WARN/ERROR；
- 同进程其他 lane（F2 table-monitor、record maintenance）与 J2 自有循环全程正常。

## 根因

`backend-go/projects/jobs/internal/jobsched/scheduler.go` 的 `runOnce` 同步调用 `spec.Task`：Timeout 仅构造 `context.WithTimeout` 取消 ctx；`timedOut` 判定、`jobsched_run_timeout` 记账与日志、`job.running` 复位全部位于 handler **返回之后**；`laneState.runningJob` 仅在 `fire` 收尾 `releaseLane` 清除。handler 忽略 ctx 卡死（无超时的外部调用等）即：

- 该任务 jobLoop 永久停在 `runOnce`，lane 永久占用；
- 同 lane 其余任务每轮 fire 命中 `jobsched_lane_busy`——**Debug 级**，生产 INFO 级完全不可见；
- 无任何超时/失败/panic 日志，停摆不可归因。

## 修复

`scheduler.go`：

- `runOnce` 将 handler 放入独立 goroutine（safego 兜底），结果经 buffered channel 恰好投递一次；主路径 `select` 双路：
  - handler 先返回 → 原 finishRun 记账（成功/失败/partial/panic/stopped 语义不变）；
  - ctx `DeadlineExceeded` 先到 → 立即按 timeout 记账 + `jobsched_run_timeout` Warn + 复位 running + 释放 lane，下一轮可正常调度；泄漏 handler 迟到结果静默丢弃（一次性 Info `jobsched_run_leaked_finished`，不二次记账/释放）；
  - 停机（Canceled）保持既有 `jobsched_run_stopped` 语义，不强记 timeout。
- stuck 可见性：`stuckWatchLoop` 对泄漏 run 在 `Timeout + 5min` 宽限后打 Error `jobsched_run_stuck`（含 runningSince/超时时长），重复间隔 5min 起指数封顶 20min；四个节奏常量为包级 var，测试通过 fakeClock 直接推进时间断言（`-race` 下 happens-before 无法可靠注入宽限变量，未走注入路径）。

## 验证

- 新增测试：超时后 lane 释放且下一轮可执行、timeout Warn 恰好一条、迟到结果丢弃不二次记账、停机不误报、stuck 指数间隔与恢复；`go test ./internal/jobsched/...` 全绿。
- 生产行为变化：任务卡死不再拖死整个 lane，且停摆以 Error 级可见；发布重启本身会清除当前卡死状态。

## 状态

已修复，2026-09-27 23:19 发布；发布重启清除历史卡死，补偿任务恢复持续调度（0206 观察窗口内连续运行）。

## 遗留（复审 Minor，可观测性退化，非阻塞）

同一 job 在一次退避窗口内发生第二次超时泄漏时，第二次 `trackLeakedHandler` 会覆写 `leakStartedAt`，首个泄漏 handler 迟到返回会提前清掉登记、stuck 日志提前停止；仅在 `Timeout + 退避` 内二次超时才可达，后续按需加 per-job 泄漏计数保护。
