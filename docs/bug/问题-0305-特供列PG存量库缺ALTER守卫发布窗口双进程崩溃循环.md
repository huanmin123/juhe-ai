# 问题-0305：特供列 PG 存量库缺 ALTER 守卫发布窗口双进程崩溃循环约 11 分钟

- **日期**：2026-10-10 17:44–17:56（发布窗口）
- **严重级**：P1（/v1 与管理面入口全量不可用约 11 分钟，POSTGRES/Redis 无恙，数据零损失）
- **状态**：已修复（生产手工 ALTER 恢复 + 仓库守卫/测试/契约三处入库）

## 现象

2026-10-10 17:4x 发布「AI 账户特供快速恢复通道」（含 6 个未发布提交：特供、dispatch 重构、题库内置题、缓存率动态档位、chat 两笔）时，`deploy.sh all` 第 [5/6] 步 gateway 连续 30 轮（5 分钟）未 healthy 自动中止；`docker compose ps` 实证 gateway 与 jobs 双双 `Restarting (1)` 崩溃循环，/v1 与管理面入口不可用。

启动日志（两进程同根因）：

```text
gateway startup failed: open J3b Business owner connection: Business PostgreSQL schema juhe_business missing column accounts.expedited_recovery_enabled
jobs: verify J1 account-health direct-input contract: 验证 PG direct input 候选查询失败: ERROR: column a.expedited_recovery_enabled does not exist (SQLSTATE 42703)
```

## 根因

设计契约《AI账户特供快速恢复通道设计.md》v3.1/v3.2 §7.1 **错误省略了 `accounts.expedited_recovery_enabled` 的 PG 既有库 ALTER 守卫**：

- PG 侧只给 `system_accounts.expedited_account_limit` 写了 `ADD COLUMN IF NOT EXISTS` 守卫；`accounts` 侧仅有建表 DDL（只救新库）+ 触发器 ROW 投影（只影响变更检测）。
- 契约当时的依据是"同文件先例 `temporary_unavailable_continuous_probe_enabled` 同样没有 accounts ALTER"——**该先例不成立**：probe 列先于存量库存在（09-05/09-27 建库时 DDL 已含它），从不需要 ALTER；特供列是后加列，存量库（生产 103.36.63.105 与开发主库 192.168.1.203）只能靠显式 ALTER 获得。
- 两轮外部审核（含 SQLite 既有库守卫、触发器投影同源等项）均未质疑 PG accounts 侧缺 ALTER，复审盲区与设计者同源。
- ensure-schema 报告 `StatementCount: 705` "成功"加重了误判：触发器函数体为惰性校验，引用缺失列也能创建成功；语句计数不等于"列已存在"。

发布序列本身执行了 10-06 以来的"maintenance 先行"改良（新 maintenance 镜像先建、ensure-schema 先于新二进制 up），但**DDL 集合本身缺语句**，前置失效——与 10-09 BUG-0300（jobs 对预置索引 fail-closed）同族，根因从"序列"深化为"契约集不完整"。

## 恢复（约 11 分钟收敛）

17:55 对生产库手工执行与 DDL 逐字一致的加法 ALTER：

```sql
ALTER TABLE juhe_business.accounts ADD COLUMN IF NOT EXISTS expedited_recovery_enabled
  integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1));
```

restart 策略下 gateway/jobs 下一轮启动通过契约校验自动恢复 healthy（无需手动 up，同 BUG-0300 恢复形态）。17:56 起五容器 healthy、公网 200、verify-release PASS、md5 三点闭环（gateway `f3cf9e3d…` / jobs `30ff3b0b…`）、恢复后 ERROR=0、调度耗尽 24h=0。

## 修复（仓库侧）

1. `pg_schema_business_tables.go`：新增 `account-expedited-recovery-pg-column` 守卫（`ALTER TABLE accounts ADD COLUMN IF NOT EXISTS ...`，注释锚定本档案）。
2. `pg_schema_test.go`：golden 计数 705→706、juhe_business 317→318、ALTER 计数 91→92。
3. `expedited_recovery_schema_test.go`：新增 accounts ALTER 守卫的逐字钉死断言。
4. 设计契约 §7.1 修正（v3.3）+ `docker/single-server/README.md` schema 条目更正。

## 教训（固化）

1. **PG 加列的守卫判据不是"先例有没有 ALTER"，而是"存量库建库时 DDL 是否已含该列"**——凡是后加列，PG 侧必须与 SQLite 侧同等配备 `ADD COLUMN IF NOT EXISTS` 守卫；"照先例省略"必须先验证先例的时序前提。
2. **`ensure-schema` 语句计数成功 ≠ 列已存在**：触发器/函数体惰性校验会掩盖缺失列；发布前用 `\d` 或 information_schema 实证目标列在位，不能只看 StatementCount。
3. 发布序列改良（maintenance 先行）只保证"清单里的语句先执行"；清单本身缺语句时无解——DDL 集完整性审查（新列 → 四处同源：建表 DDL、存量 ALTER、触发器投影、守卫测试）应作为发布门禁检查项。

## 关联

- 同族：BUG-0300（J3a 索引 fail-closed，2026-10-09）、2026-10-04「新二进制带新列、旧 maintenance 缺 DDL」事故。
- 特供功能契约：docs/functions/AI账户特供快速恢复通道设计.md（§7.1 已修正，v3.3）。
