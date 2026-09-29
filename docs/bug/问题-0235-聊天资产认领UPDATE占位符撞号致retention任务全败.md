# BUG-0235 聊天资产认领 UPDATE 占位符撞号致 chat-retention-cleanup 全败

## 基本信息

- 编号：BUG-0235
- 状态：已修复（2026-09-29 23:15 发布，生产实证通过）
- 严重程度：P1（聊天保留清理任务自上线从未成功，资产清理链失效）
- 发现时间：2026-09-29
- 发现方式：自查（生产整体巡检）
- 模块：后端（jobs / cleanuprepo）
- 关联计划：无
- 关联 bug：BUG-0232（同为 PG 方言占位符类缺陷）
- 责任人：待定

## 问题概述

- 现象：生产 `chat-retention-cleanup` 任务每轮失败，错误 `mismatched param and argument count`，`consecFail` 持续递增（连续失败 40+），任务自上线（迁移提交 20e033ec4）以来从未成功。
- 期望：任务按 10 分钟周期正常清理过期聊天数据与聊天资产。
- 实际：每轮事务回滚，聊天资产清理与保留清理完全失效；过期资产永不清理形成死循环触发。
- 影响范围：仅 jobs 进程 chat-retention-cleanup 任务；`juhe_chat` 数据只进不出（当前生产数据量小，未造成实际存储压力）；不影响其他任务族与网关链路。

## 复现步骤

1. 生产库存在过期 `juhe_chat.chat_assets` 行（当前 3 行全部过期）；
2. 等待 chat-retention-cleanup 周期触发；
3. jobs 日志出现 `jobsched_run_failed`，error=`mismatched param and argument count`，`consecFail` 每轮 +1。

## 环境信息

- 分支 / 版本：master（含 2026-09-29 18:56 构建的 jobs 镜像）
- 数据状态：`juhe_chat.chat_assets` 3 行过期；checkpoints/compacting/stale 轮次均为 0（唯一有数据的清理路径即资产认领）
- 是否稳定复现：是（每 10 分钟一次）

## 根因分析

- 表象：任务每轮失败于同一错误。
- 真实根因：`cleanuprepo/chat.go` `cleanupExpiredAssets` 的资产认领 UPDATE 使用 `s.DB.Bind(fmt.Sprintf(..., s.DB.BindIn(n)))` 双层包裹：`BindIn` 内部已把 `?` 改写为 `$1..$n`，外层 `Bind` 将 SET 子句剩余的 `?` **从 $1 重新编号**，渲染产物形如 `SET ... = $1,$2,$3 WHERE id IN ($1,$2,$3)`。服务端 Parse 数出 3 个参数占位符，客户端传 3+n 个，pgx v5 在 bind 阶段报 `mismatched param and argument count`。
- 触发链：过期资产存在 → 每轮进入认领分支 → 认领 UPDATE 撞号失败 → 事务回滚 → 资产保持过期状态 → 下一轮再次触发（死循环）。
- 排除项：`pinScheduledLease`（装配处未注入 Lease，nil 跳过）；`completeAssetDeletion` 的 advisory lock（位于认领之后，执行不到）；`pgpool.rewriteDriver` 全局改写层行为正确（撞号正是全局/局部改写层对"已有 $n + 剩余 ?"混合 SQL 的固有编号规则）。

## 修复方案

- `cleanupExpiredAssets` 认领 UPDATE 的 IN 列表改用 `placeholderList(n)`（未编号 `?` 序列），由外层 `Bind` 统一编号，与同文件 checkpoints DELETE、statssubtract 的既有惯例一致。
- 修复提交：`cleanuprepo/chat.go`（`placeholderList(len(assetIDs))` 替换 `s.DB.BindIn(len(assetIDs))`）。

## 验证结果

- 新增回归测试 `chat_asset_claim_placeholders_test.go`：
  - 纯文本计数断言（IN(1/2/3/7) 渲染后最大 `$n` 序号 == 3+n 且无残留 `?`；旧写法对照必须暴露撞号）——通过；
  - 真实 PG 端到端（dev 库，生产同构 pgpool rewriteDriver 路径，目标 ID 不存在 + 事务回滚零副作用）：修复版 UPDATE 通过参数绑定，旧写法复现 `mismatched param and argument count`——通过。
- `cleanuprepo`/`retention`/`jobregistry` 包全量测试通过（169s 全绿）。
- 生产验证（2026-09-29 23:15 发布 jobs `f2fcdb5fa1467ab0306c5b4f92b464b0`，md5 三点闭环 + healthy + 公网 200 + ensure-schema 幂等 623 语句与上批一致）：**23:19:53 首轮即成功**——`chat_retention_cleanup_completed`，`claimedAssets:3, deletedAssets:3, failedAssets:0`，存量 3 行过期资产全部认领清理（`chat_assets` 归零，含物理文件删除链）；新进程 `jobsched_run_failed` 计数 0（修复前每 10 分钟一次）。

## 防回归与观测性备注

- 占位符计数断言已固化；同款"外层 Bind + BindIn"模式全仓扫描仅此一处（`placeholderList` + 外层 Bind 为正确写法）。
- 定位过程中暴露的观测缺口（同批处置，2026-09-29 23:5x gateway 发布）：**gateway `gatewaycircuit.Bridge.persistWithRetry` 已透出最后底层错误**——耗尽路径原先返回固定文案"账户 circuit control-plane 持久化重试耗尽"，生产 6442 条失败（仅 2 个账户、两波积压回放：acc_0877c91c6f1a16ee 4515 条 01-03 点、acc_1788710035361_583e5e47 1929 条 12-13 点）无法定性最后一级原因；现错误携带"最后错误/最后 CAS 状态"，下次风暴可直接定性。
- 仍待改进（另行任务）：`jobsched_run_failed` 无阶段/SQL 上下文；jobs 进程无文件日志 sink（stdout 仅约 4 小时窗口，历史不可考；gateway 已有 `processlog.FileSink` 共享实现，jobs 移植涉及文件名/清理/索引器兼容契约，按文档先行原则立项）。
- 巡检同窗另两支独立现象（未定性，观察项）：PG lock timeout 逐日上升（9-26:1→9-29:195，集中凌晨 00-09 点，受害语句集中在 `juhe_dataset.runtime_log_index_owner_leases` 行锁，消费方为 jobs runtimelog 索引器，量小且自愈）；F4 operation log retention lock timeout 21 次（同域锁等待）。
