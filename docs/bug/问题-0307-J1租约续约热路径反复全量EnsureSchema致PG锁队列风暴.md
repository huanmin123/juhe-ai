# 问题-0307：J1 租约续约热路径反复全量 EnsureSchema 致 PG 锁队列风暴与 /v1 分钟级阻塞

- **日期**：2026-10-11 日巡检发现（10-10 整夜已在发生）；修复同日上线（提交 `67dc778d3`）
- **严重级**：P1（间歇性：/v1 出现过 366 秒阻塞后 503；J1/J2/F1/F3/F4 同窗连环 55P03；管理面健康探针间歇 503）
- **状态**：已修复（jobs 单项目； EnsureSchema 成功后按实例记忆化）

## 现象（2026-10-11 日巡检取证链）

1. **整夜间歇慢语句波**：PG 日志 >500ms 语句 20 时起逐小时累计 200-1359 条（00 时峰值 1359），持续到发布前；`commit`、`pg_advisory_xact_lock`、租约 fence_token SELECT 均出现 10s 级等待——典型"长事务持锁 + 锁队列排队"形态。
2. **55P03 lock timeout 连环**：gateway F3/F4 owner 租约丢失+重试（04 时 43 条、10:53 连续失败 7-10 次）、jobs F1 运行日志索引器 50+ 次、J2 余额刷新轮次 deadline 超时 14 次（时间分布与 BUG-0300 事故的整夜周期波同族，但 J3a 修复索引已在位——另有根因）。
3. **用户可见影响**：09:06 一条 `/v1/responses` 阻塞 **366 秒**后 503；10:53 `/__aisys__/health` 503。
4. 当时瞬时采样**零锁等待**——风暴为间歇波，事后 pg 锁现场不可得，靠 pg_stat_statements 累计证据定案。

## 根因（pg_stat_statements 实证）

```text
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS next_due_at TIMESTAMPTZ
calls=400  max=143s  （3 天累计；脚本中同族 ALTER 共 7 条）
```

`accounthealth.Store.AcquireOwnerLease`（store.go）**每次 J1 owner 租约获取/续约都全量执行 `EnsureSchema`**，其中 postgresSchema 含 7 条 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`。**PG 的 `ADD COLUMN IF NOT EXISTS` 即使列已存在（无操作）也要先取表级 ACCESS EXCLUSIVE 锁**（先锁表后判断列），于是：

```
J1 续约（周期性）→ ALTER 排队等长读事务（最长 143s）
  → account_health_current_state 全部读写被锁队列冻结（新查询排在等待中的 EXCLUSIVE 之后）
  → J1 结算事务卡在队列里、但已持有 juhe_business.accounts 行锁（accounts FOR UPDATE max 142s）
  → /v1 候选选择 FOR UPDATE 跟着阻塞 → 分钟级 503
```

同窗次生受害者（F1 索引器 INSERT、F3/F4 租约 UPDATE、J2 轮次）共享夜间 IO 高峰（备份窗口 04:10 起 + 表监控概览长查询，见观察项）被放大成整夜波。

400 次/3 天 ≈ J1 租约续约节奏，与调度周期吻合。

## 修复

`Store.EnsureSchema` 成功后按实例以 `atomic.Bool`（schemaReady）记忆化：**成功一次后热路径（AcquireOwnerLease）不再触碰 DDL**；失败不记忆、下次自动重试（保留幂等重试语义）；PG/SQLite 双分支成功提交后置位。启动装配处既有的显式 EnsureSchema 调用（worker_assembly 等）不变。

测试：`w15b_schema_ensure_memo_test.go` 双用例钉死"成功记忆化（含租约功能路径）"与"失败不记忆"；`w13g5/w13g8` 白盒臂测试按新契约修正（验证故障臂前显式复位记忆位，故障臂覆盖不减）。全包失败集合与干净树基线完全一致（10 个既有 dev-PG 环境失败，零新增回归）。

## 同窗观察项（另行跟进，非本缺陷）

1. **表存储监控概览查询**（`WITH latest_snapshots ... table_storage_snapshots`）：mean 373s / max 577s / 9 次调用——表 30 万行 **2.2GB**（行均 ~7KB）。SELECT 不阻塞 DML，但长事务持快照拖累 vacuum 与 IO，是夜间波放大器。方向：快照表保留期/预聚合/索引化 DISTINCT ON 列。
2. **`UPDATE usage_stats_minute SET success_cost_usd...`**：max 234s / 5 次——24 万行全扫 UPDATE（谓词无索引）。方向：谓词索引或改批量分段。
3. 运维手册《监控与告警运维.md》5.11 已有 Redis 运行态一致性巡检；建议增加"PG ALTER 热路径"巡检口径：`pg_stat_statements` 中 DDL 语句 calls 应在进程生命周期内只增一轮，持续增长即热路径 DDL 回归。

## 教训（固化）

1. **`ADD COLUMN IF NOT EXISTS` 不是免费幂等**：PG 侧它是 DDL，无操作也取 ACCESS EXCLUSIVE 并进入锁队列（队列会阻塞后续一切该表请求）。任何"每次操作前 ensure"的写法在 PG 都等于周期性自造锁风暴；schema 保证必须收敛到启动/部署期一次性执行。
2. 日巡检新增固定项：`pg_stat_statements` Top by max_exec_time + `docker logs` 的 55P03 计数——本次即由该路径发现，且瞬时 pg_locks 采样对间歇风暴无效，累计证据（pg_stat_statements + 慢日志按小时分布）才是定案工具。
