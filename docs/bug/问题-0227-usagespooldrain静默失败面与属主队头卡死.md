# BUG-0227 usagespooldrain 静默失败面与属主队头卡死

## 基本信息

- 编号：BUG-0227
- 状态：已修复待发布
- 严重程度：P1
- 发现时间：2026-09-28
- 发现方式：监控（usage_records gateway 流量断供 + spool 积压）
- 模块：后端（jobs `internal/usagespooldrain`）
- 关联计划：无
- 关联 bug：BUG-0225（触发源：容器 root→app 切换发布产生跨属主 spool 文件）；发布序列修正的部署侧修复由另一任务承担，见"完成总结"。
- 责任人：主代理委派代码侧 subagent

## 问题概述

- 现象：2026-09-28 16:00 生产发布（BUG-0225 容器 root→app(UID1000) 切换）后，`usage_records` 的 gateway 流量自 16:00 断供至 17:35；`usage-record-spool/gateway-chain/` 积压 448 个待消费文件；期间 jobs 日志零错误。
- 期望：属主异常的队头文件不应阻塞 drain 消费后续文件；任何排空失败必须有遥测面。
- 实际：文件名序队头的 2 个 root:root 0600 文件使 app 用户的 `os.ReadFile` 返回 EACCES，DrainOnce 终止本轮；Run 循环对该错误静默退避，形成永久 head-of-line 停摆且零日志。
- 影响范围：生产国内单机（103.36.63.105，Docker go-only）gateway 用量记录入表延迟 95 分钟（16:00-17:35）。数据未丢失（spool at-least-once 保全），止血后 20 秒内 448 个文件全部消费。

## 复现步骤

1. `usage-record-spool/gateway-chain/` 文件名序最前存在 2 个 root:root 0600 属主文件（旧 root 容器停止前最后写入），其后为 app 属主正常文件。
2. jobs drain 以 app(UID1000) 运行 DrainOnce，按文件名序处理到队头时 `os.ReadFile` 返回 EACCES。
3. DrainOnce 直接 `return processed, err` 终止本轮；Run 循环按固定退避进入下一轮，队头不变，循环往复；无任何错误日志，积压持续增长。

## 环境信息

- 分支 / 版本：生产 2026-09-28 16:00 发布（BUG-0225 容器 root→app 切换）
- 数据状态：spool 积压 448 文件（队头 2 个 root:root 0600）；usage_records gateway 流量断供
- 浏览器 / 系统 / Node 版本：不适用（Go jobs 进程，国内单机 Docker go-only 形态）
- 是否稳定复现：是（属主不符文件位于文件名序队头即必现）

## 根因分析

- 表象：gateway 用量断供 + spool 积压 + 零日志。
- 真实根因：三层叠加。
  1. 触发源：BUG-0225 发布把容器用户 root 切换为 app(UID1000)，旧 root 容器停止前最后写入的 2 个 spool 文件为 root:root 0600，恰位于文件名序最前（队头）。
  2. 代码缺陷——三个静默失败面（修复前 `backend-go/projects/jobs/internal/usagespooldrain/drain.go`）：
     - ① Run 循环吞掉 DrainOnce 错误，静默退避不打日志（drain.go:385-394）；
     - ② listSpoolFiles 的 `os.ReadDir` 错误（根目录 drain.go:146-150、实例子目录 drain.go:158-161）上抛后汇入①被吞；
     - ③ DrainOnce 的 ReadFile 非 ErrNotExist 错误直接 `return` 终止本轮（drain.go:210-216），不可读队头永久阻塞后续全部文件。
  3. 水位缺陷：refreshPendingWatermark / oldestSpoolFilePath（drain.go:259-321）队头不可读时保留上次水位，ingestgate 统计游标安全门被永久卡在不可读文件处。
- 为什么会发生：drain 的失败语义只设计了"瞬态入队失败即停重试"（对齐 Node 本地队列语义），没有区分"永久性不可读"错误形态；且全部失败路径最终汇入 Run 的静默退避分支，任何 drain 侧故障都无遥测暴露。

## 修复方案

契约（已裁决，行为目标）：

1. DrainOnce 对 ReadFile 非 ErrNotExist 错误：记 Error 日志（`event=usage_record_spool_file_read_failed`，含 `file`、`error`）后跳过该文件继续本轮后续文件；文件不隔离、不删除，保留待下轮重试（运维修复属主后自然消费，区别于 `.corrupt` 隔离语义）。同文件同错误按 60 秒窗口节流，窗口内不重复记录，避免每秒 × 文件数刷屏。
2. Run 循环对 DrainOnce 返回的非 nil 错误打 Warn 日志（`event=usage_record_spool_drain_round_failed`，含 `error`、`processed`），不再静默退避。
3. 水位前进：队头文件不可读（非 ErrNotExist）时，`OldestPendingCreatedAt` 反映第一个可读待消费文件的 created_at（跳过不可读文件），统计游标安全门不得被永久卡死。目录仅剩不可读文件时保留上次水位（不前进也不清空：可读积压已消费完，水位停留在最后一个可读积压点；不可读文件修复入表时其记录晚于该水位属已知取舍——故障由契约 1 的 Error 日志即时暴露）。
4. 不改变既有消费成功语义：100ms 节拍、500 批、Enqueue 幂等、损坏隔离 `.corrupt`、停机排空、Enqueue/删除失败保留文件终止本轮。

- 修改点：
  - `backend-go/projects/jobs/internal/usagespooldrain/drain.go`：DrainOnce 读取失败跳过语义与节流 Error；Run 失败轮 Warn；`refreshPendingWatermark` 重写为按文件名序探测第一个可读候选（原 `oldestSpoolFilePath` 收敛为 `pendingSpoolCandidates`）；包/函数注释同步。
  - 测试：`w9g_edge_test.go` 的 `TestW9GDrainOnceReadFileError` 由"终止本轮并上抛"锚点改造为新契约锚点；新增 `w0227_unreadable_head_test.go`（不可读队头不阻塞 + 节流 + 恢复消费、水位跳过前进、Run 失败轮 Warn 断言）。
- 行为影响：不可读文件不再阻塞消费链；drain 侧故障可见（新增两类事件）；水位不再被不可读队头永久卡死。
- 发布异常处理：上线无需数据操作。若线上仍有 root 属主残留文件，上线后将显式出现 `usage_record_spool_file_read_failed` 日志（每文件 60s 一条），运维 `chown 1000:1000` 即恢复消费。

## 生产止血记录（2026-09-28，主代理执行）

- 对 2 个 root:root spool 文件执行 `chown 1000:1000`。
- 20 秒内 448 个积压文件全部消费；`usage_record_spool_drain_summary` windowFiles=448 日志实证；usage_records gateway 流量恢复。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元测试 | usagespooldrain 包全量（含新增 BUG-0227 回归） | `cd backend-go && go test ./projects/jobs/internal/usagespooldrain/... -count=1` | 通过 | ok（4.061s，含 TestW0227DrainOnceSkipsUnreadableHeadAndRecovers / TestW0227WatermarkSkipsUnreadableHead / TestW0227RunLogsWarnOnDrainRoundFailure / 改造后的 TestW9GDrainOnceReadFileError 全 PASS） | 已通过 |
| 回归测试 | jobs 组合根 Spool/Drain 相关 | `cd backend-go && go test ./projects/jobs/cmd/juhe-ai-jobs/ -run 'Spool|Drain' -count=1` | 通过 | ok（10.723s） | 已通过 |
| 构建验证 | jobs 模块全量构建 | `cd backend-go && go build ./projects/jobs/...` | 通过 | 无错误输出 | 已通过 |
| 端到端（隔离） | 不可读队头不阻塞消费、水位前进、修复后自然恢复 | 包内 Windows 独占句柄三测试 | 通过 | 全 PASS | 已通过 |
| 生产验证 | 发布后 drain 正常消费、零积压、计费实时 | 观察发布后 jobs 日志与 spool 目录/usage_records | 持续消费、无积压 | **已发布实证（2026-09-28 19:16 jobs 随 gateway+jobs 发布）**：`usage_record_spool_drain_summary windowFiles=2`（发布后 74s 窗口即时消费 2 个交接文件）、spool 目录 0 积压、`usage_records` gateway 流量实时入库（19:17:51 最新） | 通过 |

## 复发记录

- 无。

## 下次遇到

- 先查什么：spool 目录积压 + jobs 日志 `usage_record_spool_*` 事件（修复后 `read_failed` / `round_failed` 直接指认文件与错误原文）。
- 重点看什么：spool 文件属主与权限（root→app 切换窗口、chown）；文件名序队头文件是否被独占或不可读。
- 如何避免误判：不要把"零日志"当作"无故障"；drain 停摆先看水位 `OldestPendingCreatedAt` 是否卡死与文件名序队头状态。

## 完成总结

- 完成时间：2026-09-28
- 结论：代码侧已修复（契约 1-3 落地，契约 4 保持），包内测试与组合根回归全绿；**已发布（2026-09-28 19:16 随 gateway+jobs 全量，生产 drain 恢复实时消费实证见验证记录）**。
- 后续建议：发布序列修正（消除 root→app 过渡窗口产生跨属主文件的根因，如切换用户前先停旧容器写入、发布后检查 spool 属主）由部署侧另一任务承担，本记录仅覆盖代码侧修复。
