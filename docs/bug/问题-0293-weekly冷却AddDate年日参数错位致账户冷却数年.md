# BUG-0293 weekly 冷却 AddDate 年/日参数错位致账户冷却数年

## 基本信息

- 编号：BUG-0293
- 状态：已修复（待发布验证）
- 严重程度：P1
- 发现时间：2026-10-07（线索自 Node→Go 迁移终局报告「疑似生产问题」，以 `w1_policy_test.go` / `w1_error_policy_arms_test.go` 注释按现状固化近一月无人跟进）
- 发现方式：BUG-0291 同类缺陷全库审计中核实为真缺陷
- 模块：后端 / 网关（账户错误处理策略 weekly 冷却计算）
- 关联计划：无
- 关联 bug：BUG-0291（同类审计引入：三态/参数方向写反家族）；编号备注：本缺陷修复代码曾被并发提交 `53d685183` 以「BUG-0292」注释带入，而 0292 编号实属模型检测作答流截断，本档案为 weekly 缺陷的正确编号，测试注释已回正为 0293。

## 问题概述

- 现象：配置了 `reset_strategy=weekly` 重置策略的账户错误处理规则命中后，账户冷却时间落在**数年后**（daysAhead=3 时实测约 3 年），近乎永久下线；weekly 目标时刻已过的顺延臂固定 +7 年。
- 期望：weekly 重置按 Node 语义对齐到下一周 `weekly_reset_day` 的 `weekly_reset_hour`（0..6 天后）；目标时刻已过则顺延 7 天。
- 实际：Go `AddDate(years, months, days)` 的天数被误传进年参数——`target.AddDate(daysAhead, 0, 0)`（+N 年）与顺延 `target.AddDate(7, 0, 0)`（+7 年）。
- 影响范围：`chain_error_policy.go` `accountErrorRuleCooldownUntil` 的 weekly 分支两处；唯一消费方是账户级错误处理规则的冷却决策（同文件 `:268`，`decisionActionCooldown` → `cooldown_until`）。duration/daily 分支与配额恢复（`quotaRecoveryCooldownUntil`）不受影响。

## 复现步骤

1. 为任一账户配置错误处理规则：`reset_strategy=weekly`、`weekly_reset_day=3`（周三）、`weekly_reset_hour=8`。
2. 周日 10:00 触发规则命中的错误（daysAhead=(3-0+7)%7=3）。
3. 观察冷却时间：落在 2029 年（+3 年），而非 2026-09-16 08:00（+3 天）。

## 环境信息

- 分支 / 版本：Node→Go 迁移后全部已发布版本；修复随 `53d685183` 入库（2026-10-07）。
- 数据状态：生产中已被 weekly 规则打入年跨度冷却的账户行（`cooldown_until` 为未来数年）不会因发布自动解除，见「存量处置」。
- 是否稳定复现：是（单元级确定性复现）。

## 根因分析

- 表象：weekly 冷却"特别长"，账户命中后长期不再参与调度。
- 真实根因：`AddDate` 参数位错用（同 BUG-0291 的"方向/位置写反"家族）：主对齐臂 `AddDate(daysAhead, 0, 0)` 把 0..6 天当年；顺延臂 `AddDate(7, 0, 0)` 把 7 天当年。
- 为什么会发生：Node `Date` 无 AddDate，迁移手写换算时参数位错位；迁移期测试已发现（`w1_error_policy_arms_test.go` 记「疑似生产 bug，待用户裁决，不在测试中固化」、`w1_policy_test.go` 记「行为存疑」按现状断言），但两条线索都未建问题档案，滞留近一月。

## 修复方案

- 修改点：
  - `chain_error_policy.go`：`AddDate(0, 0, daysAhead)` 与 `AddDate(0, 0, 7)`（已随 `53d685183` 入库）。
  - `w1_policy_test.go`：「行为存疑」年跨度断言反转为正确契约断言（[70h±1h] 与顺延 [166h±1h]，抖动窗口按 `passiveScheduleJitterWindowMs` 的 [24h,7d) → ±1h 推导）。
  - `w1_error_policy_arms_test.go`：两段 weekly「诚实性质」断言按其自述回正为 `w1eAssertWithin` 中心断言（2026-09-04T08:00Z / 2026-09-08T08:00Z ±1h）。
- 行为影响：weekly 冷却从年跨度回正为按周对齐（最长约 7 天 + 1h 抖动）；无 env/schema 变化。
- 存量处置：发布后对仍处异常冷却的账户在页面执行一次「恢复正常」即可解除（同 BUG-0288 处置先例）；如需先摸底范围，可查业务库 `accounts.cooldown_until` 晚于 now+30 天的行（先备份再操作，属生产数据修复，另行授权）。无需 schema 变更。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 红测试 | 修复前新断言（仅改测试） | `go test ./cmd/juhe-ai-gateway/ -run TestW1AccountErrorRuleCooldownUntil` | [70h-1h,70h+1h] 断言红 | 红（实测跨度 26308h ≈ 3 年） | 通过 |
| 绿测试 | 修复后 | 同上 | 两用例（对齐 + 顺延）绿 | PASS | 通过 |
| 回归 | 冷却/抖动族 | `-run 'TestW1ERuleCooldownUntilArms|TestW1AccountErrorRuleCooldownUntil|TestW1ErrorPolicyCooldownAndJitter'` 等 6 用例 | 全绿 | 6/6 绿 | 通过 |
| 全库扫描 | `AddDate(` 首参同型错位 | rg 全 backend-go 76 处 | 无第二处 | 无（years 参数均为常量 0） | 通过 |

全包 `go test ./cmd/juhe-ai-gateway/` 存在 5 个与本缺陷无关的既有环境依赖失败（端口绑定/外部库/种子计数），失败文件与冷却代码零耦合（rg 核实），未验证为绿的原因是本机无外部 PG/Redis。

## 复发记录

- 无（首次登记）。

## 下次遇到

- 先查什么：Go 时间运算函数的参数位（`AddDate(years, months, days)`），变量传参一律核对位置。
- 重点看什么：「时间跨度异常大/异常小」的冷却与重置计算；测试注释里的「疑似生产 bug/行为存疑」是否已建档案。
- 如何避免误判：时间对齐类计算先写中心时刻推导注释再写断言，红绿验证必须先红。

## 完成总结

- 完成时间：2026-10-07
- 结论：两处参数错位修复，weekly 冷却回正为按周对齐；红绿闭环 + 全库 AddDate 扫描无同型错位。
- 后续建议：发布后摸底存量年跨度冷却行并引导用户手动恢复；「行为存疑」线索档案欠账见 BUG-0294 清点档案。
