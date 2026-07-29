# BUG-0141 使用记录 PostgreSQL 批量落库死锁

## 基本信息

- 编号：BUG-0141
- 状态：待发布验证
- 严重程度：P1
- 发现时间：2026-07-29
- 发现方式：生产日志 / PostgreSQL 运行统计 / 代码审计
- 模块：后端 / Usage worker / Redis Stream / PostgreSQL / accounts
- 关联计划：PLAN-20260729T094521796Z
- 关联 bug：BUG-0117
- 责任人：AI

## 问题概述

- 现象：高性能模式的 usage worker 间歇记录 PostgreSQL `40P01 deadlock detected`，失败消息由 Redis Stream 保持 pending 并重试。
- 期望：并行 usage worker 能安全批量持久化，重复投递通过唯一 ID 幂等处理，不出现结构性死锁或记录丢失。
- 实际：生产 PostgreSQL 累计 deadlock 计数达到 411，且本次修复尚未发布时仍新增 1 次；日志的等待位置交替出现在 `juhe_business.accounts` 行更新和 usage 日分区唯一索引插入。
- 影响范围：失败批次会延迟确认和重试，短时间放大日志与队列处理成本；已确认没有使用记录丢失和持续积压证据。

## 复现步骤

1. 在高性能模式启动至少两个 usage worker，使它们从同一 Redis Stream consumer group 并行消费。
2. 构造两个包含相同账户、或包含相同 usage ID 重投递的批次。
3. 让其中一个事务先写 usage 唯一索引，另一个事务先进入账户副作用更新，或让多账户 Map 以相反顺序更新。
4. 原实现可能形成 accounts 行锁与 usage 唯一索引的循环等待，PostgreSQL 终止其中一条事务并返回 `40P01`。

## 环境信息

- 分支 / 版本：生产高性能 PostgreSQL + Redis Stream 部署
- 数据状态：运行中的使用记录与账户数据；未修改、删除或修正历史数据
- 浏览器 / 系统 / Node 版本：macOS 生产节点 / Node LTS / PostgreSQL
- 是否稳定复现：生产高并发窗口间歇出现；代码锁图可稳定推导

## 根因分析

- 表象：相同错误有时报告更新 accounts 行等待，有时报告写 usage 日分区索引等待。
- 真实根因：`createUsageRecordsBatchPostgres()` 在事务内先插入 usage，再从按输入顺序建立的 Map 迭代更新 accounts。并行 worker 的输入顺序和重复投递不固定，导致同类事务获得 accounts 与 usage 唯一索引锁的顺序不同。
- 为什么会发生：Redis Stream consumer group 设计允许多个 worker 并行持久化，且消息必须在提交后确认；唯一键保证幂等但会参与等待。usage 表、日分区和 accounts 没有外键或用户触发器，排除了隐藏锁链。

## 修复方案

- 修改点：事务外汇总账户副作用；事务开始后按数据库 `ORDER BY id` 对涉及账户执行 `FOR NO KEY UPDATE`，再写 usage、最后执行既有条件更新。
- 行为影响：同一账户的并行 usage 批在短事务内有序等待；不同账户仍可并发。唯一 ID 幂等、消息确认时机和回滚语义不变。
- 发布异常处理：失败时回退到同 schema 的上一已验证 release；不清理 Redis pending、不修改历史使用记录或账户数据。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 生产只读 | 运行前证据 | PostgreSQL、Redis、worker health 只读查询 | worker 健康且无持续积压，定位锁链 | 已完成 | 通过 |
| 专项回归 | 事务锁顺序 | usage PostgreSQL 锁顺序专项 | accounts 预锁在 usage 写前 | 通过 | 通过 |
| 数据库边界 | PostgreSQL client / transaction | 现有数据库边界专项 | 事务及 SQL 绑定保持正确 | 通过 | 通过 |
| 相邻回归 | usage 快照和请求时计价 | 现有专项 | 相邻写入边界保持正确 | 通过 | 通过 |
| 类型检查 | 后端 TypeScript | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 无关错误阻塞 | 未涉及本次文件 |
| 构建配置检查 | 后端生产 tsconfig | `tsc -p tsconfig.build.json --noEmit` | 通过 | 无关错误阻塞 | 未涉及本次文件 |
| 既有 SQLite 回归 | 批写、写入池、分片路由 | 现有专项 | 进入 usage 断言 | 账户模型校验提前阻塞 | 未涉及本次文件 |
| 发布后观察 | 死锁增量与队列 | 压力窗口观察日志、deadlock 计数和 Redis pending | 不再出现此锁图 | 待填 | 未执行 |

## 复发记录

- 无。

## 下次遇到

- 先读取 PostgreSQL deadlock detail，分别确认等待的 relation、事务调用链和每个事务已持有的资源。
- 重点比较同一业务事务的多行更新顺序，以及幂等唯一索引写入与业务副作用写入的相对顺序。
- 不要以减少 worker、清理 pending 或无限重试代替锁顺序修复；先固定资源获取顺序，再评估是否需要有界退避。

## 完成总结

- 完成时间：待发布验证
- 结论：结构性锁顺序修复和针对性回归已完成；完整门禁受工作区无关错误阻塞，尚未发布。
- 后续建议：修复现有统计 / system-team 类型错误及旧 SQLite 夹具后，完成生产构建、受控发布，并观察真实并发窗口内的 deadlock 累计计数。
