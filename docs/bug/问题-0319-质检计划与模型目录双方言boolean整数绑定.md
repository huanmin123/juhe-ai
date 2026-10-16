# BUG-0319 质检计划与模型目录双方言 boolean 整数绑定

## 基本信息

- 编号：BUG-0319
- 状态：已修复（2026-10-15，待发布）
- 严重程度：L1（模型检测主动治理域的管理面写路径在 PostgreSQL 全部失败；provider_model_catalog 目录替换写路径同因失败；`/v1` 转发主链不受影响）
- 发现时间：2026-10-15
- 发现方式：生产管理界面操作报错（`failed to encode args[6]: unable to encode 1 into binary format for bool (OID 16): cannot find encode plan`）
- 模块：后端 / 网关 / modelcheckowner 质量治理、business settings 模型目录
- 关联计划：无
- 关联 bug：无
- 责任人：AI

## 问题概述

- 现象：生产发布后在管理界面保存质检计划（定时检查配置）报 pgx 编码错误 `unable to encode 1 into binary format for bool (OID 16)`，语句未到达数据库。
- 期望：`timeout_penalty_enabled` 列在 PostgreSQL 按 boolean 写入成功。
- 实际：写入绑定值是 Go `int`（1/0），pgx 无法把整数编码进 boolean 列，INSERT/UPDATE 直接在客户端失败。
- 影响范围：`model_quality_policies` 与 `model_quality_schedules` 的全部写路径（创建/编辑质检计划、保存质量策略中 timeout_penalty_enabled 绑定点），以及 `provider_model_catalog` 的目录整体替换（`ReplaceProviderCatalog`）。读路径、调度执行路径（只更新 lease/last_run/next_run_at，不绑 boolean 参数）不受影响。

## 根因分析

`timeout_penalty_enabled` 列在双方言 schema 中 SQLite 为 `INTEGER 0/1`（CHECK 约束）、PostgreSQL 为 `boolean NOT NULL DEFAULT false`（2026-10-13 提交 59dfd527a 以建表 DDL 内联 + 幂等 ALTER 两种方式引入，2026-10-15 随发布首次上生产）。写路径 helper `boolIntQuality` 恒返回 `int`，只对 SQLite 成立。

读路径 `scanQualitySchedule` 已有方言注释并正确扫描（`bool` 目标双方言驱动都可取值），写路径漏掉了同样的方言分支——读写不对称缺陷。

「为什么上线后才出现」：功能（列 + 写路径）10-13 提交、10-15 发布首次到达生产；发布前生产行由旧代码创建，scheduler 的续租/完成更新不绑 boolean 参数，所以从未触发。modelcheckowner 常规测试全部以 SQLite 模式运行（全绿），PG 路径只有 opt-in 冒烟测试（`J3B_GATEWAY_POSTGRES_SMOKE=1`，默认跳过）且未覆盖质检计划写路径——缺陷因此漏网。

## 修复方案

- `modelcheckowner/business_quality_management.go`：新增 `(*BusinessQualityManager).boolBind`（PG → Go `bool`，SQLite → `boolIntQuality`），替换四处 `timeout_penalty_enabled` 绑定：policies INSERT/UPDATE（原 args[3]/args[2]）、schedules INSERT/UPDATE（原 args[9]/args[6]）。`manual_enforcement_enabled` 与 `enabled` 两列双方言均为 `integer`，绑定保持 `boolIntQuality` 不变。
- `business/settings/settings.go`：`ReplaceProviderCatalog` 的 `catalog_visible` 绑定同因（同文件 SELECT 谓词早已按方言切换、INSERT 漏了），新增 `(*Store).boolBind` 修复。
- 全仓 16 个 PG boolean 列写入审计：其余写入点均已正确方言处理——`proxyprofiles` `enabledDBValue`（返回 any）、`auditlog` `boolValue`（返回 Go bool）、`accountstest` `BoolTrue/FalseLiteral` 字面量、`accountstransfer` 直接绑 bool、jobs `proxylatency` 双方言字面量（PG `TRUE` / SQLite `1`）、`account_balance_outcomes.committed` 不绑定靠列默认值。缺陷恰好两处，均已修复。
- 新增方言契约单测 `TestBusinessQualityManagerBoolBindDialect`、`TestStoreBoolBindDialect` 锁定绑定类型。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 根因复现 | int 绑 PG boolean 列报错 | pgx 直连 dev PG `UPDATE ... SET cancel_requested=$1`（值 1） | 逐字复现用户报错 | 逐字复现 | 通过 |
| 反向验证 | Go bool 绑 PG boolean 列成功 | 同上改绑 `true` | 写入成功 | 写入成功 | 通过 |
| 契约单测 | 两处 boolBind 方言返回类型 | `go test -run 'BoolBindDialect' -count=1 -v`（两包） | PASS | PASS | 通过 |
| 包回归 | modelcheckowner 全包 | `go test ./internal/modelcheckowner -count=1` | 通过 | 通过（73.7s） | 通过 |
| 包回归 | settings 全包 + gateway 构建 | `go test ./internal/business/settings -count=1`；`go build ./...` | 通过 | 通过 | 通过 |

## 复发记录

- 2026-10-15 生产窗口：质检计划保存报错（用户操作触发）。修复尚未发布前，生产创建/编辑质检计划与目录替换会持续失败。

## 下次遇到

- 先查什么：报错 `args[N]` 的 N 对应语句中第 N+1 个占位参数，直接定位绑定值的 Go 类型与目标列的双方言类型。
- 重点看什么：双方言新增 boolean 列时，写路径是否有方言分支（`int` 绑定只对 SQLite 成立）；读写两侧要同查（本次读侧正确、写侧遗漏）。
- 如何避免误判：常规 `go test` 全绿只证明 SQLite 路径；modelcheckowner 的 PG 路径是 opt-in 冒烟（`J3B_GATEWAY_POSTGRES_SMOKE=1` + `JUHE_AI_J3B_POSTGRES_SMOKE_URL`），涉及新列写入时应补跑或补 PG 用例。发布前审计清单应包含「新增列的双方言写入绑定核对」。

## 完成总结

- 完成时间：2026-10-15
- 结论：两处双方言 boolean 整数绑定修复，方言契约以单测锁定；全仓 boolean 列写入审计确认无第三处同因缺陷。
- 后续建议：发布 gateway 后在生产界面实际保存一次质检计划（含开启超时处罚开关）验证闭环。
