# BUG-0009 proberepo 读取脏时间戳触发 panic 崩溃循环风险

## 基本信息

- 编号：BUG-0009
- 状态：已确认，待修复
- 发现时间：2026-09-20（w16k 覆盖率补齐任务的 profile 分析中发现）
- 关联模块：后端 / shared/platform / 探针 worker

## 问题

`shared/platform/accounttest/proberepo/reader.go` 的可用性推导路径对数据库中的脏时间戳数据使用 `panic(err)` 而非错误返回：

- `reader.go:354`：`account_expires_at` 无法解析时 `panic(err)`（`isPastInstant` 失败）；
- `reader.go:377`：`cooldown_until` 无法解析时 `panic(err)`（`isFutureInstant` 失败）。

**影响**：`juhe_business` 账户行中任一条 `account_expires_at`/`cooldown_until` 存在非法值（手工修数、历史脏数据、导入误差），probe worker 读取该账户时会 panic → supervisor 重启 → 重启后同一条脏行仍在 → **panic 崩溃循环**，拖垮整个探针 worker（全部账户的探针停摆），并以每次重启一轮的频率刷错误日志。

## 复现路径

1. 在 `juhe_business.accounts`（或 probe 读取的同源行）将某账户 `account_expires_at` 置为非法字符串；
2. 触发 probe worker 读取该账户（到期候选扫描）；
3. 观察 worker panic 与重启循环。

## 建议修复

1. `deriveEffectiveAvailability` 路径将 `panic(err)` 改为返回错误（函数签名增加 error 或跳过该账户并记 warn 日志 + 降级为不可调度），语义对齐 Node 版的容错行为（待对照 `migration-backup` 确认 Node 是否同样 panic——如 Node 也 panic 则属迁移保真，修复需产品决策）；
2. 修复补回归：库内脏时间戳行 → worker 不崩溃、该账户降级/跳过、其余账户正常。

## 备注

- 发现路径：w16k 覆盖率补齐任务的 profile 分析（w16j/w16k 之前该路径无测试覆盖——按"无法测试的都是设计问题"标准，此即未测代码隐藏问题的又一实证）。
- `panic` 疑似迁移期对"该路径输入恒合法"的假设；但数据库字段是外部输入，假设不成立。
- jobs cmd 的 `worker_probe_jobs.go:288-290` 存在依赖该错误传导的不可达臂（w16k 登记），修复后该臂自然可达。
