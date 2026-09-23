# BUG-0183 mockdata 占位行毒丸卡死 J1 调度器（outbox claim 无逐行隔离）

## 基本信息

- 编号：BUG-0183
- 状态：已修复（代码修复 + 测试 + 本地数据恢复完成）
- 严重程度：P2（本地 dev 触发；但 claim 无逐行隔离是设计缺口，任何一行写侧回归脏行即可让 J1 整体停摆至保留期收敛）
- 发现时间：2026-09-23
- 发现方式：本地日志排查（jobs 进程每 4~9 秒重复 WARN）
- 模块：后台任务（J1 账户健康 outbox drain）、maintenance mockdata autofill
- 关联 bug：无
- 责任人：胡安民

## 问题概述

- 现象：本地 dev jobs 进程每个扫描周期打出一条 `WARN account-health owner lease released error="invalid character 'm' looking for beginning of value"`（约每 4~9 秒一条，无限循环），J1 账户健康调度完全停摆。
- 期望：outbox 中单行数据损坏只影响该行（记 warn、按已处理收敛），其余行正常消费，调度器持续存活。
- 实际：一行 `source_fence` 非 JSON 的 pending 行让整个 claim 中止，首轮 `runCycle` 失败 → owner lease 被释放 → 下个周期重新拿租约 → 再次失败，形成确定性死循环；期间 outbox 正常行也无法消费。
- 影响范围：J1 全部探活调度与 outbox 消费。本地 dev（SQLite 零配置）实测触发；生产 PG 的 `juhe_business.account_health_probe_request_outbox` 经查为 0 行，未受影响。

## 根因（两层缺陷叠加）

1. **写侧（触发源）**：`maintenance/internal/mockdata/autofill.go` 的自动补全向 `account_health_probe_request_outbox` 插入占位行，`source_fence` 列被填成纯文本 `mockdata_autofill_..._source_fence_N`（首字符 `m` 即报错来源）。该列的写侧契约是 JSON 对象（gateway 写侧 `chain_request_failure_health.go` 用 `json.Marshal`）或空串。autofill 跳过清单 `autofillSkipExactTables` 登记了 `account_health_jobs_input_outbox`、`account_health_outcomes` 等 J1 owner 表，**唯独漏了 `account_health_probe_request_outbox`**（gateway 写、jobs 消费的交接 outbox）。
2. **消费侧（放大器）**：`jobs/cmd/juhe-ai-jobs/worker_health_probe_outbox.go` 的 `ClaimPendingProbeRequests` 在 claim 逐行扫描时调用 `ParseProbeOutboxSourceFence`（`internal/accounthealth/outbox_drain.go:334`，全调用链唯一裸返回 `*json.SyntaxError` 的点），一行解析失败即 `return nil, err` 中止整个 claim——与 `outbox_drain.go` 头注释"逐行失败不阻塞其余行 / 确定性失败按已处理收敛"的既有契约矛盾。行保持 pending，重试永不成功，形成毒丸；若无人工干预，要等 7 天 outbox 保留期 prune 才收敛。

## 证据与时间线

- 本地 dev 实际运行形态：SQLite 零配置模式（`JUHE_AI_DATA_DIR` 派生，业务库 `.local/dev/data/business.sqlite3`），非 `.local/project-resources/dev/env/shared.env` 的 PG profile。
- 2026-09-23 12:23~12:25（本地）：mockdata 运行，插入 3 行 pending 占位行（`created_at=2026-09-23T04:23~04:25Z`），与 `.local/dev/data/mockdata-summary.json`（12:25）吻合。
- 14:38：dev gateway/jobs 重启（旧代码），14:53 起用户日志出现 WARN 死循环。
- 15:20 只读复查：`account_health_probe_request_outbox` 共 3 行、全部 pending、`source_fence` 均为非 JSON 的 `mockdata_autofill_*` 文本。
- 15:41 执行清理程序：表已为 0 行（期间 15:20~15:41 被外部途径清除，最可能是用户按诊断报告自行执行了 DELETE，现有证据无法完全归因，登记存疑）；清理程序备份 0 行，确认此后无新增 mockdata 行。

## 修复内容（2026-09-23）

1. **根因修复** `maintenance/internal/mockdata/autofill.go`：`autofillSkipExactTables` 登记 `account_health_probe_request_outbox`（理由：gateway 写入、jobs J1 drain 消费的运行时 outbox，占位 source_fence 非 JSON 会毒丸卡死 J1）；同步修正该块"以下四张"的过期计数注释。`mockdata_autofill_test.go` 跳过断言表新增该表。
2. **消费加固** `jobs/cmd/juhe-ai-jobs/worker_health_probe_outbox.go`：`ClaimPendingProbeRequests` 对确定性损坏行（`source_fence` 非 JSON、`deadline_at` 非 RFC3339 文本）记结构化 warn（`event=account_health_probe_outbox_row_corrupt`）后幂等出队，不阻塞其余行；SQL/扫描级错误仍整体上抛（下一周期重试）。store 增加 nil 安全的 logger；`internal/accounthealth/outbox_drain.go` 接口注释同步声明"实现可收敛确定性损坏行"。
3. **测试**：`w12c_cmd_units_test.go` 原"非法 fence 整体报错"断言改为隔离语义（损坏行被出队、合法行正常返回），新增 `TestW12CHealthProbeOutboxClaimIsolatesCorruptDeadline`；`w16d_cmd_sqlite_arms_test.go` 损坏臂重写为隔离语义（并修正旧注释：整数经 SQLite TEXT affinity 实存为文本，旧行为实际由文本解析报错驱动）。

## 验证记录

- `go test ./projects/jobs/cmd/juhe-ai-jobs/ -run '…Outbox…'`（7 个靶向测试）全部 PASS；`go test ./projects/maintenance/internal/mockdata/` 全量 PASS（171s）；jobs / maintenance 两模块 `go build ./...` 通过；改动文件 gofmt / go vet 干净（`w14j_cmd_pgseed_test.go` 为既有未格式化文件，不属本次改动）。
- 运行实例恢复证据（毒丸行消失后，旧二进制即可恢复正常）：`account-health.sqlite3` 的 `account_health_owner_leases` 显示租约被同一 owner 持续持有并续约（`updated_at` 15:45:01 → 15:46:01，`fence_token` 恒 1，无释放/重建），15:30 以来写入 5 条 `account_health_outcomes`——WARN 死循环消失。

## 遗留与注意

- dev 常驻进程（14:38 启动）仍是旧二进制：**重启 dev 后 claim 逐行隔离才生效**；在此之前若再跑 mockdata，autofill 修复同样未生效（旧二进制不包含两处代码修复），会重新插入毒丸行并复现死循环。
- `deadline_at` 的 SQL/类型级损坏仍会使 claim 整体报错（有意保留：类型级异常保持响亮失败；SQLite TEXT affinity 下实际难以构造该形态）。
- 一次性只读诊断与清理程序保留在 `.local/project-resources/dev/env/scratch/outboxcheck-20260923/`（`**/.local/` 已 gitignore，不入库）。

## 完成总结

- 完成时间：2026-09-23
- 结论：autofill 漏登记 + claim 无逐行隔离两层缺陷均已修复并有测试锁定；本地 dev 数据已恢复，J1 调度已验证恢复续约与探针写入。
- 后续建议：重启本地 dev 使新代码生效后，可再跑一次 mockdata 做真实验证（新代码应只输出一条 `account_health_probe_outbox_row_corrupt` warn 且 J1 不再中断，autofill 修复生效后则根本不会插入该表）。
