# BUG-0237 账户可用性 dirty 触发器锁序死锁（J3a×J1 三方环 + 发布期 DDL 并发）

## 基本信息

- 编号：BUG-0237
- 状态：已修复（待发布）
- 严重程度：P2（低频：稳态 24h 级 1 次 + 发布窗口偶发；单事务回滚，轮询任务下轮自然重试）
- 发现时间：2026-09-30
- 发现方式：监控（PostgresDeadlocks 告警触发生产排查）
- 模块：后端 jobs（J3a proxylatency / J1 accounthealth）+ maintenance（ensure-schema）
- 关联 bug：BUG-0184、BUG-0192（本问题证伪 0192 的"结构性归零"结论）
- 责任人：已修复

## 问题概述

- 现象：生产 PG（juhe_ai 库）`deadlock detected` 告警，2026-09-29 23:15 与 09-30 00:23 各一次事件（24h 内共 2 次）。
- 期望：账户可用性 dirty 写入域（问题-0184/0192 引入 advisory 7001001 串行化）不再出现 40P01。
- 实际：两类新死锁形态。
- 影响范围：死锁牺牲方单事务回滚。00:23 型双方均为 jobs 每秒轮询任务（J3a 代理探测回写、J1 账户健康投影），下轮自动重试，无业务可见中断；23:15 型牺牲方为发布期 ensure-schema 的 DDL 语句，此前靠人工重跑发布步骤。

## 两类死锁的现场与根因

### 形态一（00:23）：J3a × J1 × 合规方三方环

PG 死锁日志（`DETAIL` 还原）：

- 事务 B（J3a，`proxylatency/result_projector.go` 的 `UPDATE proxy_profiles SET test_status=...`）：先持 proxy_profiles/receipts 行锁 → AFTER UPDATE ROW 触发器 `account_list_availability_proxies` → `mark_dirty_proxy` → `mark_dirty_accounts` 函数体内 `PERFORM pg_advisory_xact_lock(7001001)`（schema `pg_schema_business_tables.go:1526`）→ **持行锁等 advisory**。
- 事务 C（J1，`accounthealth/projection.go` 的 cooldown 回写 `UPDATE accounts ... EXISTS (account_health_jobs_input_versions ...)`）：首语句取的是**另一把键**（`hashtextextended('juhe-ai:account-health-projection:v1',0)`）→ 持 accounts/input_versions 行锁 → 语句级触发器 → 同一汇聚点等 7001001 → **持行锁等 advisory**。
- 事务 A（合规方，gateway `chain_dispatch.go:915` 或 jobs `circuitstore/listavailability.go:272`，首语句取 7001001）：**持 advisory 等行锁**。
- 环：B 持 advisory 队首等 C 的行锁，C 等 advisory（队列在 A 后），A 等 advisory（队列在 B 后）——advisory 锁的队列公平性把"两方反序"放大成三方环。

根因：0192 的串行化设计前提是"所有 dirty 写方的**第一把锁**就是 7001001"；但触发器路径物理上只能在事务中途（函数体内）取锁，"先锁后行"约定对它不可满足。B/C 两个每秒轮询任务的应用侧事务没有先取 7001001，恰好与合规方构成"持行锁等 advisory × 持 advisory 等行锁"的反序对。**0192 的"结构性归零"结论被本事件证伪**（其互斥闭环只覆盖了 circuitstore、gateway marker 与触发器三方，B/C 从未纳入）。

### 形态二（23:15）：发布期 ensure-schema DDL × projection 写入

- DDL 方：一次性容器 `juhe-ai-maintenance-run-0ac92855ae84`（`docker compose run --rm maintenance --ensure-schema`，623 条幂等 DDL，含 `CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_accounts`）。journald dockerd 事件时间窗 23:15:47.4–23:15:50.8 精确包住死锁时刻 23:15:48；`--rm` 执行完即删容器是此前"找不到执行者"的原因（取证方法见监控手册死锁排查节）。
- 另一方：23:15:06 刚重建的 jobs 的每秒 projection 写入（`INSERT account_list_availability_projection_tags` 等）。
- 根因：发布序列的 ensure-schema 与新启动 jobs 的任务并发，DDL 锁（pg_proc/关联对象）与业务写入偶发交错成环。属发布流程固有并发，非外部操作。

## 修复

按"修已证死锁组合 + 发布序列加固"，不重构全局锁体系（裁决依据见下"非目标"）：

1. **J3a 锁序**（`backend-go/projects/jobs/internal/proxylatency/result_projector.go`）：新增 `lockAccountListDirtyInTx`（PG-only，SQLite no-op，对齐 `circuitstore.lockDirtyWritesInTx` 模式），4 个事务路径（drainProjectRow / projectStored / ProjectManualNoTargets / ProjectManualOutbound 含 CAS 快路径）在 BeginTx 后首语句取 7001001，触发器内取锁变为可重入无阻塞。
2. **J1 双键顺序**（`backend-go/projects/jobs/internal/accounthealth/projection.go` 的 `projectOutcome`）：首语句改为先取 7001001、再取 health-projection 键——双键顺序契约固定为 `7001001 → health:v1 → 行锁`，与全部合规方一致。
3. **maintenance ensure-schema 语句级重试**（`backend-go/projects/maintenance/internal/schema/pg_lock_retry.go` 新增）：执行器对 SQLSTATE `40P01`/`55P03`/`57014` 类瞬时锁冲突最多重试 3 次（退避 1s/2s/4s，ctx 取消返回原始错误），非锁冲突不重试、耗尽后原样上抛；DDL 文本不变。
4. **advisorylock.go 注释修正**：说明"首语句"约定的适用边界（能控制事务结构的应用侧路径）与触发器路径的通病，指向本记录。

## 验证

- **真实 PG 复现测试**（`jobs/internal/proxylatency/w184_dirty_lock_pg_test.go`，w1cover 覆盖库）：阶段一按修复前形态（行锁→触发器）与合规方并发，**第 1 轮即捕获 SQLSTATE 40P01**（死锁可复现）；阶段二修复后形态（双方先取 7001001）并发全部提交成功、零 40P01、触发器正常留 dirty 行。
- 契约测试：`w184_dirty_lock_pg_test.go` 的录制测试断言 J3a 三个入口 PG 事务首语句为 7001001 且先于行锁/FOR UPDATE/CAS；`accounthealth/w184_projection_lockorder_test.go` 断言双键顺序与非 PG 方言零 advisory。
- maintenance：`internal/schema/pg_lock_retry_test.go` 10 个 Mock 测试（分类/退避/耗尽/全流程中段重试/错误链保留）；maintenance 全量 `go test ./...` 绿。
- jobs 全量 26 包 `go test ./...` 绿。

## 非目标与已知残留风险（明确不做，避免过度修复）

- **不**给全部 ~20 组"行锁先于 advisory"的既有写事务补锁（盘点清单见排查会话）：其中网关请求路径多为 autocommit 单语句（错误策略 markCooldown、键状态机等），补锁需收敛为显式事务并会把请求串行化到一把全局锁，吞吐代价不可接受。
- **不**移除 `account_list_availability_dirty` 对 accounts 的 FK、不重构触发器取锁位置/方式（try-lock 会破坏 dirty 语义，回退到 0184 形态）。
- 残留风险：上述未补锁路径理论上仍可与合规方成环（同构于本次 B×C，频率低、单事务回滚自愈）；其中请求路径 autocommit × applyOneClaim（dirty FK 对 accounts 行 KEY SHARE）为最高频组合，列为观察项——若生产再出现死锁，按监控手册排查节取证后按同模式补点修复。
- 键序约束：任何未来新增的"取 7001001 + 另一把 advisory"事务必须遵守 7001001 最先的顺序（本记录双键契约）。

## 后续

- 生产发布：jobs + maintenance 二进制随下次发布窗口上线（发布后观察 `pg_stat_database_deadlocks` 增量与 PG 日志零 40P01，沿用 0192 验收口径，需覆盖高峰窗口——0184 教训"低峰归零不构成生效证明"）。
- 0192 文档已追加勘误注记（见该文档末尾）。
