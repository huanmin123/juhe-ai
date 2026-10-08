# BUG-0299 J2 余额刷新账户租约 Held 形态 nil 解引用 panic

## 基本信息

- 编号：BUG-0299
- 状态：已修复（2026-10-09 入库，待下次发布）
- 严重程度：P2（ioWorker 被 safego 兜杀进程不死，余额刷新延迟一轮；极端多 Held 形态下并发容量损失可放大为轮次超时）
- 发现时间：2026-10-09 01:26（生产发布后巡检捕获 jobs panic 日志）
- 发现方式：生产实证（发布重启窗口 jobs `accountbalance.runner.ioWorker` panic，栈 `runner.go:293`）+ 本地调试复现
- 模块：后端 / shared / accountbalance / J2 余额刷新
- 关联计划：无（PLAN-20261008T113056000Z 发布巡检发现，缺陷与该计划无因果——该计划未改 accountbalance）
- 关联 bug：无

## 问题概述

- 现象：生产发布重启窗口，jobs 日志出现 `goroutine panic recovered; process keeps running`（component `accountbalance.runner.ioWorker`，`runtime error: invalid memory address or nil pointer dereference`，栈 `(*Runner).runInputs.func4` → `runner.go:293`），伴随两条 `J2 余额刷新轮次失败: context deadline exceeded`。
- 期望：`prepareInput` 返回 skipped 形态时调用方跳过该输入，不计错误、不触碰 query。
- 实际：`prepareInput` 在 `AcquireAccountLease` 返回 `ErrAccountLeaseHeld`（或接口契约上 `!acquired`）时返回 `(runStateSkipped, AccountLease{}, nil query, nil error)`；调用方 `runInputs` 的 skipped 分支只做计数不短路，`itemErr == nil` 不走错误 continue，直落 `dbTask{..., query: *query}` 对 nil 解引用 panic。
- 触发形态：发布重启窗口上一进程的账户租约 TTL 未过期，新进程首轮 J2 对同账户 `AcquireAccountLease` 必然 Held——每次非优雅重启后首轮都可能触发；同 owner 并发重复输入（手动刷新与周期重叠）同理。
- 影响范围：每个 Held 形态杀死一个 ioWorker（`safego.Recover` 兜住进程不死，但该 worker 不再消费 ioJobs）；多 Held 并发时 worker 耗尽 → 剩余输入无人消费 → 发送循环阻塞到轮次 ctx 超时（即伴随的两条轮次失败日志）。余额刷新延迟到下一轮（分钟级），无数据损坏。F2 表监控 `owner_lease.go` 同款恢复语义在 BUG-0299 修复批次中一并根治（见 PLAN-20261008T113056000Z 终审查漏修复项 6）。

## 复现步骤

1. SQLite store 以 owner A 获取 owner lease 并 `AcquireAccountLease(acct, TTL)` 预置账户租约。
2. 同 owner 对 acct 再次 `AcquireAccountLease` → `ErrAccountLeaseHeld`。
3. `prepareInput` 返回 `(runStateSkipped, AccountLease{}, nil, nil)`；修复前调用方对 nil `*QueryResult` 解引用 → panic（本地调试复现栈与生产栈一致，`runner.go:293`）。

## 环境信息

- 分支 / 版本：生产 `54c756c19`（2026-10-09 01:2x 发布）首发观测；缺陷代码自 BUG-0286 批次（2026-09-28 前后）即在，非新引入。
- 是否稳定复现：是（预置租约形态确定性复现；生产触发依赖重启窗口/并发重复输入的时序）。

## 根因分析

- 真实根因：`runInputs` 的 ioWorker 对 `prepareInput` 的 skipped 形态处理不完整——skipped 分支只计数不 `continue`，而三个 skipped 返回点中两个（`ErrAccountLeaseHeld`、`!acquired`）伴随 `itemErr == nil`，既有的 `itemErr != nil` continue 拦不住；调用方注释"query 为 nil 必然已走错误分支（w12h 授权删除）"的假设与 `prepareInput` 实际返回形状不符。
- 为什么会发生：BUG-0286 批次引入多形态 skipped 返回时未同步调用方短路；单机单实例下账户租约 Held 罕见（只在重启窗口/并发重复输入出现），测试全绿掩盖。

## 修复方案

- `runner.go` runInputs：skipped 分支计数后补 `continue`（三形态全部短路），注释修正为按 prepareInput 实际返回形状陈述；executed 臂的 `query == nil` 计数（queryErr 形态）保持不变。
- 回归测试：`TestPrepareInputSkipsHeldLeaseWithoutError`（契约测试：同 owner 预置账户租约后 `prepareInput` 必须返回 `(skipped, nil query, nil error)`，确定性无并发）与 `TestRunInputsAllHeldLeasesDrainWithoutWorkerLoss`（owner lease 不可得时 runInputs 全量 Skipped 优雅返回、不挂起）。
- 已知限制：调用方 `continue` 缺失的直接红绿受限于 `Store` 为具体类型不可注入 mock——契约测试锁形状，panic 本身由生产栈与本地调试复现（栈帧一致）定案，见测试注释中的限制说明。
- 行为影响：skipped 形态不再 panic、ioWorker 存活、轮次正常收口；正常/executed/error 路径零变化。无 env/schema 变化。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元 | Held 形态契约 | `go test ./accountbalance/ -run 'TestPrepareInputSkipsHeldLeaseWithoutError' -count=1`（shared/platform） | (skipped, nil query, nil error) | PASS | 通过 |
| 单元 | owner lease 不可得优雅返回 | `go test ./accountbalance/ -run 'TestRunInputsAllHeldLeasesDrainWithoutWorkerLoss' -count=1` | 全量 Skipped、nil error、不挂起 | PASS | 通过 |
| 回归 | accountbalance 全包 | `go test ./accountbalance/ -count=1` | 全绿 | ok 17.7s | 通过 |
| 生产实证 | panic 栈一致性 | 生产日志栈 `runner.go:293` vs 本地复现栈 | 同帧同错 | 一致 | 通过 |

## 复发记录

- 无（首次登记）。

## 下次遇到

- 先查什么：jobs panic 日志的 component 与栈帧；`accountbalance.runner.ioWorker` 的 nil 解引用优先核对 `prepareInput` 返回形状与调用方分支完备性。
- 如何避免误判：safego 兜底后 report 计数可能仍"正常"（skipped 在 panic 前已累加），不能以 report 完整性排除 worker 死亡；以 panic 日志为准。

## 完成总结

- 完成时间：2026-10-09
- 结论：skipped 分支短路修复 + 两条回归测试入库（待下次发布）；生产当前版本受影响面为余额刷新延迟一轮，无需紧急再发布。
