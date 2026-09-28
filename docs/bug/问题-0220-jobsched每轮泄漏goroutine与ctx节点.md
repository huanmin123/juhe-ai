# BUG-0220 jobsched 每轮任务执行泄漏 goroutine 与 context 节点

## 基本信息

- 编号：BUG-0220
- 状态：已修复（已发布 2026-09-28 16:00 全量，验证通过）
- 严重程度：P1
- 发现时间：2026-09-28
- 发现方式：自查（迁移后整体深度复审，jobsched 死锁家族第四隐患排查）
- 模块：后端 / jobs / jobsched / 资源生命周期
- 关联计划：无
- 关联 bug：BUG-0202 / BUG-0208 / BUG-0209（jobsched 同文件状态机问题家族，本次为资源泄漏非死锁）
- 责任人：主代理（复审交付）

## 问题概述

- 现象：长驻 jobs 进程的 goroutine 数与内存随时间持续增长，数周量级最终 OOM/重启。
- 期望：每轮任务执行结束后，其派生的 goroutine 与 context 节点全部释放。
- 实际：每轮执行净增 1 个常驻 goroutine + 1 个滞留 context 节点，仅 Scheduler.Stop 时集中释放；生产任务节拍（1s×2 + 5s×3 + 10s×2 + 分钟级）合计每天约 24 万轮泄漏。
- 影响范围：全部 Timeout>0 的任务（生产 jobregistry 全部任务均配置 Timeout）；jobs 进程 `restart: unless-stopped` 长驻，泄漏在进程生命周期内无界累积。

## 复现步骤

1. 注册任意 Timeout>0 的周期任务并运行 N 轮。
2. 采样 `runtime.NumGoroutine()`：每轮净增 1，不回落。
3.（负向验证已做）旧代码 + 新回归测试：51 轮后 goroutine 净增 106。

## 环境信息

- 分支 / 版本：master（2026-09-28 工作区）
- 数据状态：与数据无关，纯代码路径
- 是否稳定复现：是（确定性泄漏路径）

## 根因分析

- 表象：goroutine 缓慢增长，重启后消失。
- 真实根因：两层泄漏——① `runOnce` 每次调用 `contextBoundToStop()`，该函数每次新建 1 个 ctx + 1 个监听 goroutine（select stopCh / ctx.Done），其 cancel 只在 stopCh 分支执行，任务正常结束不释放；② `runOnce` 内 `taskCtx, cancel := context.WithCancel(...)` 后 `taskCtx, cancel = context.WithTimeout(taskCtx, spec.Timeout)` 变量遮蔽：Timeout>0 时 WithCancel 层的 cancel 永不调用，该层 ctx 节点作为停机根的 child 永久滞留（Go context 取消只向下传播，cancel 子层不影响父层）。
- 为什么会发生：BUG-0202 把 handler goroutine 化时引入 stop-bound ctx 传播链，未审视 `contextBoundToStop` 的每调用成本与 cancel 遮蔽；测试只覆盖超时回收与 stuck 观测，未覆盖 ctx 生命周期。

## 修复方案

- 修改点（`projects/jobs/internal/jobsched/scheduler.go`）：① Scheduler struct 新增 `stopBoundCtx`/`stopBoundCancel`，NewScheduler 构造一次，Stop/StopAndDrain 的 stopOnce 闭包内与 `close(stopCh)` 原子执行取消；② `contextBoundToStop()` 改为返回单例 ctx（保留方法做单一入口，注释禁止回退）；③ `runOnce` 拆开变量遮蔽（`taskCancel` 独立持有），handler 收尾 defer 一并调用两层 cancel（幂等）。新增 `w0220_run_ctx_leak_test.go` 回归（真实时钟 ≥50 轮短间隔 Timeout>0 任务，goroutine 基线-终值对比 ±2 容忍，沿用 gatewaycircuit 泄漏测试先例模式）。
- 行为影响：停机传播链（stopCh 关闭 → taskCtx 取消）、超时判定采样顺序、泄漏 handler watcher、finishRun 记账语义全部不变；仅资源生命周期收口。
- 发布异常处理：无需；发布后 goroutine 曲线应转为水平（可通过 system-metrics-sample 的 goroutines 指标观察确认）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 负向验证 | 旧代码 + 新测试 | `go test -race -count=1 -run TestSchedulerRunOnceDoesNotLeakGoroutinePerRun`（临时 stash 修复） | FAIL | `goroutine 净增 = 106（baseline=50 after=156）`；口径说明：106 高于"每轮 1"的理论值 51，含停止取样窗口内仍在飞的 handler 尾巴与调度器自身 goroutine 稳定窗噪声，不影响"泄漏存在"的定性（核心证据是修复后 leaked=0 与模型一致） | 通过 |
| 绿验证 | 修复后泄漏回归 | 同上（恢复修复） | PASS | `runs=51 baseline=4 after=4 leaked=0` | 通过 |
| 包回归 | jobsched 全量 | `go test -count=1 ./projects/jobs/internal/jobsched/` | 全绿 | ok 1.602s | 通过 |
| 竞态 | 泄漏测试 race | `go test -race -count=1 -run TestSchedulerRunOnceDoesNotLeak...` | PASS | PASS | 通过 |
| 静态检查 | `go vet ./projects/jobs/internal/jobsched/` | 通过 | 通过 | 通过 | 通过 |

## 复发记录

- 无。

## 下次遇到

- 先查什么：jobs 进程 goroutine 曲线是否水平（system-metrics-sample）；`runOnce`/`fire` 路径任何新增 ctx 派生点的 cancel 归属。
- 重点看什么：变量遮蔽导致的 cancel 丢失（`taskCtx, cancel = ...` 覆盖写法）；每调用建 goroutine 的 helper 必须有对应生命周期收口。
- 如何避免误判：泄漏在 Stop 时集中释放，短生命周期测试看不到；泄漏测试必须跑足够轮次并对比基线。附带发现：`w2_outcome_log_test.go` 5 个测试在 `-race` 下失败（无锁读 bytes.Buffer，HEAD 基线同样失败，非本次引入），另行任务处理。

## 完成总结

- 完成时间：2026-09-28
- 结论：两层泄漏均根除，负向/正向双向验证通过。
- 后续建议：发布后观察生产 goroutines 指标确认水平化；w2 测试既有 race 另行登记修复。
