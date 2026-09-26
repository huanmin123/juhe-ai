# 问题-0192：可用性维护事务与逐 claim 事务跨表死锁（40P01 持续）

- 发现：2026-09-26 18:10 左右（数据层回切云机后的体检中）。与 0184 不是同一处：0184 修的是 ApplyClaims 与网关脏标记的成环；本问题是 **jobs 进程内部两个并发任务**互锁。
- 现象：jobs 日志持续 `J3a proxy execution failed ... 写入 J3a Go proxy state 失败: deadlock detected (SQLSTATE 40P01)`（J3a 只是被殃及的受害者，真实冲突方见下）；约 20 条/10 分钟，回切云机（17:36）后仍持续。

## 死锁配对（PG 日志实锤，2026-09-26 19:46）

- T1（维护任务，`listavailability.go` 同一函数内两条语句一个事务，约 380-434 行）：
  1. `INSERT ... ON CONFLICT(account_id) DO UPDATE SET claim_token=NULL...`（批量重置 dirty 行 → **先持有整批 dirty 行锁直到提交**）
  2. `UPDATE viewer_health SET is_current=0 WHERE viewer_system_account_id IN (SELECT DISTINCT accounts.system_account_id FROM accounts LEFT JOIN resource_authorizations ...)`（**无 ORDER BY**，按计划流式任意顺序锁 viewer 行）
- T2（ApplyClaims 逐 claim 短事务，0184 修复形态）：claim dirty 行 → upsert viewer_health（is_current=1）→ `DELETE dirty WHERE account_id=$1 AND generation=$2 AND claim_token=$3`。
- 成环：T1 持 dirty 行 Di 未提交 → T2 的 DELETE Di 等待；T2 持 viewer 行 Vi → T1 的 viewer 扫描等 Vi。双方 hold-and-wait，与语句内部顺序无关——**T1 跨表长期持锁是根因**。

## 修复设计（待实施）

最小充分方案：把 T1 拆成两个短事务，消除跨表 hold-and-wait：
1. tx-A：批量重置 dirty（提交）；
2. tx-B：viewer_health 批量置 stale（内部 SELECT 加 `ORDER BY accounts.system_account_id` 保证确定性顺序，提交）。
- 原子性取舍：两段之间崩溃 → dirty 已重置但 viewer 未置 stale。判定可接受的前提：viewer staleness 有 EnsureViewerHealth/周期补偿兜底（需在实施时核对 439 行 EnsureViewerHealth 及维护循环的补偿语义）；若核对不通过，改为方案 B：tx-A 保持，tx-B 前先短暂 sleep/分批（每批 ≤N 行提交），仍保证不跨表持锁。
- 同步整改：`ORDER BY` 一并加到 T1 语句 2（即使拆事务也保留，保证确定性行锁顺序）。
- 验证：jobs 包测试（w13g5 系列）+ 部署后观察 1 小时 40P01=0（补上 0184 遗留的复核欠账），`J3a proxy execution failed` 归零。

## 关联

- 0184：`ApplyClaims` 排序+逐 claim 短事务（15:45 上线，本问题在它的隔壁）。
- 运维侧记录：`.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md` 2026-09-26 回切记录。

## 实施记录（2026-09-26 晚）

- `EnqueueAllForRuntimeRecovery` 已拆为两个短事务（dirty 重置先行提交；viewer_health 置 stale 独立事务并加 `ORDER BY accounts.system_account_id`），`go test ./internal/circuitstore/` 通过。
- 部署：交叉编译 → 服务器 build --no-cache → force-recreate；已验证运行中二进制（`/usr/local/bin/juhe-ai-go-project`，入口统一改名——注意以后验二进制要查这个路径）含修复标记（grep=1）。
- 待办：观察 1 小时 40P01 是否归零。重启后首 40 秒仍有 3 条 40P01——需确认是否为旧连接收尾残留或其他 tx 对（同文件还有 EnsureViewerHealth/due-transitions 等跨表事务），若持续则按 PG 死锁日志继续定位下一对。

## 复检与第三轮修复（2026-09-26 21:48–21:55）

复检结论（21:48 取证）：

- 21:18（jobs 重启）后死锁断崖下降：此前每分钟 2–12 条，21:18–21:29 共 5 条，21:29 起归零——前两轮修复（advisory 锁串行化全部应用侧 dirty 写事务 + EnqueueAllForRuntimeRecovery 拆分）对应用侧路径生效。
- 重启后首 40 秒的 3 条与后续 5 条同源，不是残留：PG `DETAIL` 实锤第三个环——一方 `UPDATE juhe_business.proxy_profiles SET test_status=…`（账户测试写回），另一方批量 `INSERT INTO account_list_availability_dirty`。成环机制：proxy_profiles 上的触发器 `account_list_availability_proxies` → `account_list_availability_proxy_dirty_trigger()` → `mark_dirty_proxy` → `mark_dirty_accounts` 隐式写 dirty，该路径在数据库端，应用侧 advisory 锁覆盖不到；批量事务持 dirty 行 A 等 B，proxy 事务（触发器已写 B）等 A。
- 该汇聚函数同时是其余全部隐式写 dirty 触发器（circuit/authorization/group/tag/search/runtime/quota/accounts statement 级）的公共路径。

第三轮修复（最小充分，一处闭环）：

- 在 `juhe_business.account_list_availability_mark_dirty_accounts` 函数体内（空数组早退后）加 `PERFORM pg_advisory_xact_lock(7001001)`，与 Go 侧 `advisorylock.AccountListDirty` 同键。事务级 advisory 锁可重入，已持锁应用事务经触发器再取无阻塞；SQLite 方言无这套触发器（全库写锁天然串行），不涉及。
- 落点两处：schema 源码 `backend-go/projects/maintenance/internal/schema/pg_schema_business_tables.go`（持久化，`go build ./...` 与 maintenance schema 测试、circuitstore 测试通过）；线上热修 `CREATE OR REPLACE FUNCTION`（2026-09-26 21:50 执行，`grep=1` 验证生效，应用无需重启）。热修 SQL 与改前函数定义备份在 `.local/project-resources/prod/database/hotfix-mark-dirty-accounts-advisory-lock-20260926.sql` / `backup-mark-dirty-accounts-函数定义-20260926-2148.sql`。
- 观察基线：21:55:00 `pg_stat_database.deadlocks` = 1281（PG 实例 17:32 启动以来累计）。验证口径：该计数不再增长且 PG 日志无新 `deadlock detected`，需覆盖晚间高峰后确认。

## 第四轮补齐：应用侧 advisory 锁上线（2026-09-26 22:12）

- 热修后 21:57 仍出现 2 条同环死锁，取证定性：一方（proxy 触发器路径）已通过热修取锁，另一方（应用侧批量 `INSERT dirty`）**未取锁**。交叉编译验证实锤：21:18 部署的 jobs 二进制不含 `lockDirtyWritesInTx` 符号（grep=0，含锁新构建 grep=2）——上次会话已把 advisory 锁写入本地代码但**未部署**（服务器 `/opt/juhe-ai/build/bin` 留有 21:47/22:04/22:06 三份 gateway 备份，属中断的部署尝试）。
- 补齐动作：本地重建 gateway/jobs/maintenance 三件套（均含 `pg_advisory_xact_lock` 代码）→ 上传（md5 校验：gateway `7bb267b5…`、jobs `3875cc6b…`、maintenance `315b7f6e…`）→ `docker compose build gateway jobs maintenance` → `up -d gateway jobs` → 容器内 md5 与本地一致、健康检查 `ready:true` → `maintenance --ensure-schema` 幂等重放（632 条语句，schema 源与库内函数对齐）。
- 至此 dirty 全部写入方互斥闭环：jobs 应用事务、gateway 脏标记事务（同一锁键）、库端触发器汇聚函数（热修）。预期 40P01 归零；验证观察继续到晚间高峰后确认。

## 终验（2026-09-26 22:45）

- `pg_stat_database.deadlocks` 自 22:12 部署基线 1283 起 **30+ 分钟零新增**；PG 日志同窗口零 `deadlock detected`；jobs 日志零 `40P01`、零 `jobsched_run_failed`。对比修复前：高峰期每分钟 2–12 条（17:32–21:18 累计 1281 条）。
- 结论：三个环（ApplyClaims 大事务环、EnqueueAllForRuntimeRecovery 跨表环、proxy 触发器/无锁应用写入环）全部消除。18:xx 后低峰归零不代表生效的教训已吸取——本次验证窗口虽未覆盖深夜高峰，但死锁全部三对对立面已被逐一取证封堵（每一对的两侧都持同一把锁），结构性归零；仍建议次日白天/晚间各复查一次计数不增长。
- 运维注意（新发现，非本缺陷）：force-recreate jobs 会使 F3 audit owner 租约短暂易主，gateway 按 fail-closed 设计退出重启（RestartCount 数次、分钟级中断后自愈，22:34 实测）。**重启/部署 jobs 的窗口要预期 gateway 短暂重启**；`compose up -d` 同时重启两者时 gateway 会等租约 TTL（22:10 部署实测约 9 分钟）。

## 遗留

- `J3a proxy execution failed ... 写入 J3a Go proxy state 失败` 同源（proxy_profiles 触发器路径），本轮已随之封堵，随终验一并归零。
- 3 条 `generation N 无法确认删除` 继续观察是否复现。
