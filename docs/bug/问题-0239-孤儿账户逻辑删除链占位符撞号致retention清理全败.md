# BUG-0239 孤儿账户逻辑删除链占位符撞号致 retention 清理全败

## 基本信息

- 编号：BUG-0239
- 状态：已修复（2026-09-30 批次）
- 严重程度：P1（orphan 清理与已删除账户物理清理链在 PG 生产整体失效）
- 发现时间：2026-09-30
- 发现方式：全面审查（多子代理复审+主代理核验）
- 模块：后端（jobs / cleanuprepo）
- 关联计划：无
- 关联 bug：BUG-0235（同款 BindIn+Bind 双层包裹撞号；0235 修复未做全库同模式排查，本条为残留）
- 责任人：待定

## 问题概述

- 现象：PG 生产形态下 retention 任务的孤儿账户逻辑删除链失败于 `mismatched param and argument count`，事务回滚，CleanupExpired 整体中止。
- 期望：orphan 扫描命中的账户正常逻辑删除并进入后续清理，CleanupExpired 各路径（含已删除账户物理清理）按周期推进。
- 实际：orphan 扫描命中至少一行即失败，事务回滚，CleanupExpired 整体中止；orphan 行确定性存在时每轮复现并进入退避循环。
- 影响范围：仅 jobs 进程 retention 任务族（orphan 扫描与已删除账户清理）；网关链路与其他任务族不受影响。

## 复现步骤

1. 业务库存在满足 orphan 扫描条件的账户行；
2. 等待 retention 任务周期触发（worker_retention.go:134 → CleanupExpired）；
3. orphanSweepPostgres（PG 生产恒启用）进入 logicallyDeleteAccountsTx，pgx 报 `mismatched param and argument count`，事务回滚，CleanupExpired 整体中止（含已删除账户物理清理）。

## 环境信息

- 分支 / 版本：master
- 数据状态：生产当前是否存在 orphan 行未验证（运维事实）；PG 生产形态 orphanSweepPostgres 恒启用
- 是否稳定复现：是（orphan 命中即必现；SQLite 模式不复现）

## 根因分析

- 表象：orphan 清理失败于占位符计数错误。
- 真实根因：`deleteaccount.go` `logicallyDeleteAccountsTx`（约 :748 起）三条语句（UPDATE accounts 置 deleted、SELECT deleted ids、tombstone SELECT）均为 `s.Business.Bind(fmt.Sprintf(..., s.Business.BindIn(n)))` 双层包裹，且 SQL 体内含额外 `?`（UPDATE 三个、两条 SELECT 各一个）。PG 下 `BindIn` 先把 IN 列表的 `?` 改写为 `$1..$n`，外层 `Bind` 把其余 `?` 从 `$1` 重新编号，`$1` 双重引用、传参数（3+n 或 1+n）与最大引用号不符，pgx 报 `mismatched param and argument count`。
- 为什么会发生：与 BUG-0235 同款代码生成模式；0235 修复仅处理 chat.go 资产认领一处，未做全库同模式排查，本条为残留。SQLite 模式下 Bind 为 no-op 天然正确，dev 隔离测试测不出。

## 修复方案

- 三处 IN 列表改用 `placeholderList(n)`（dataretention.go:991 既有），由外层 Bind 统一编号，对齐 BUG-0235 修复在 chat.go:702 确立的惯例。
- 补参数计数断言测试（BUG-0235 已证明录制驱动测试对此类缺陷失明）。

## 验证结果

- 修复落点：`deleteaccount.go` 三处 IN 列表改 `placeholderList(n)`（含 BUG-0239 注释）；第四处 DELETE（:805-813）经核对 SQL 无额外 `?`，不满足撞号条件，保持原样。
- 新增回归测试 `deleteaccount_placeholder_collision_test.go` 三测试（UPDATE/SELECT/tombstone 各一，n∈{1,2,3,7}）：
  - 修复版渲染后最大 `$n` 序号 == 参数数且无残留 `?`——通过；
  - 旧写法（外层 Bind 包 BindIn）双包断言下暴露撞号——通过（对照复现）。
- `go test ./projects/jobs/internal/cleanuprepo/ -run 'Placeholder|DeleteAccount'` PASS；cleanuprepo 全包测试 138.45s ok、0 失败；gofmt/vet 干净。
- 全库复核：jobs 内其余 BindIn 调用点（deleteaccount 其余 8 处、usageshards 3 处、dataretention 1 处、chat.go 2 处）逐处核对均不满足撞号条件（SQL 无额外 `?`）——本缺陷为该模式最后三处残留。

## 防回归与观测性备注

- 占位符计数断言随修复固化；"外层 Bind + BindIn"模式经 BUG-0235 + 本条完成全库排查闭环。
