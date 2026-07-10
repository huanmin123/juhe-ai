# BUG-0044 Go 管理操作日志未统一执行 Node 清洗规则

## 基本信息

- 编号：BUG-0044
- 状态：已修复（待真实环境验证）
- 严重程度：P1
- 发现时间：2026-07-10
- 发现方式：Node 转 Go 已迁移切片横向审计
- 模块：后端 / Go / 管理接口 / 操作日志 / Asynq
- 关联计划：[PLAN-0081 Node 转 Go 渐进减法迁移](../plans/计划-0081-Node转Go渐进减法迁移.md)
- 关联迁移记录：[W3 登录与系统账户迁移记录](../migration/W3-登录与系统账户迁移记录.md)、[W4 团队与统一授权迁移记录](../migration/W4-团队与统一授权迁移记录.md)、[W5 管理端全局品牌设置读写记录](../migration/W5-管理端全局品牌设置读取记录.md)
- 关联 bug：无
- 责任人：主 agent / 维护者

## 问题概述

- 现象：W5 全局品牌设置已经按 Node 规则清洗 operation log changes，但其他已迁移管理写 handler 仍直接调用 Asynq enqueue，绕过了同一清洗规则。
- 期望：所有已迁移管理写接口在入队前统一读取 `operationLogMaxChangesPerRecord`，清洗敏感值、截断字符串和序列化值，并限制单条日志展开的变更数。
- 实际：系统账户、资料、团队、授权、代理和账户标签等路径可能写入未归一化 changes；其中密码等敏感字段使用了各 handler 自定义的“已设置”或“已重置”文案，而不是 Node 最终的 `before="未设置"`、`after="已变更"`。
- 影响范围：
  - W2 账户标签独立 PATCH。
  - W3 当前用户资料更新、系统账户创建和系统账户更新。
  - W4 团队创建 / 更新 / 成员维护和授权创建 / 更新 / 有效期更新 / 归还 / 回收。
  - W5 代理管理和全局品牌设置。

## 复现步骤

1. 调用任一已迁移但直接使用 `operationlogjob.EnqueueWrite` 的管理写接口。
2. 提交敏感 change、超长字符串、对象 / 数组值，或超过 `operationLogMaxChangesPerRecord` 的 changes。
3. 检查 Asynq payload 或最终 `juhe_dataset.operation_logs.changes_json`，可见不同 handler 的清洗行为不一致。

## 环境信息

- 分支 / 版本：`feature/20260706-go`，W2-W5 Go opt-in 阶段。
- 数据状态：PostgreSQL 业务写入成功，operation log 通过 Asynq `operation-log:write` 入队。
- 浏览器 / 系统 / Node 版本：Windows / PowerShell 7；问题位于 Go HTTP handler 公共副作用边界。
- 是否稳定复现：是。

## 根因分析

- 表象：只有 W5 settings handler 能通过日志清洗单测，其他 handler 各自构造并直接入队。
- 真实根因：Go 在分块迁移管理写接口时复制了 operation log enqueue 代码，没有先建立与 Node `recordOperationLog*` 等价的共享入口。
- 为什么会发生：W5 后补了设置驱动的清洗逻辑，但实现最初局限在 settings 文件，没有横向替换已经迁移的管理写 handler，也缺少禁止直接 enqueue 的源代码 guard。

## 修复方案

- 新增共享 `enqueueManagementOperationLog`：
  - 使用脱离请求取消的 5 秒超时上下文完成 best-effort 入队。
  - 生产装配通过共享 `SettingsReader` 读取 `operationLogMaxChangesPerRecord`；未注入 reader 的局部测试使用默认值 100。
  - 设置读取、配置校验或入队失败只记录 warning，不覆盖已成功的业务响应。
- 统一 changes 清洗：
  - 敏感值固定归一化为 `before="未设置"` / `after="已变更"`，不保留 handler 自定义提示或真实敏感内容。
  - 普通字符串最多保留 200 个字符，超出追加 `...`。
  - 对象 / 数组等非基础类型先 JSON 序列化，最多保留 500 个字符。
  - 超过配置条数时保留前 N 项，并追加 `__truncated__` 汇总项。
- 替换所有已迁移管理写 handler 的直接 enqueue，并新增源代码 guard，防止后续再次绕过共享入口。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| HTTP 定向测试 | 共享清洗、敏感值、截断、设置读取失败和写接口接入 guard | `go test ./internal/httpapi -count=1` | 所有已迁移管理写 handler 使用共享入口 | 通过 | 已通过 |
| Go 全量测试 | 相邻模块与应用装配回归 | `go test ./... -count=1` | 全部通过 | 通过 | 已通过 |
| Race | 管理 HTTP handler 并发回归 | `go test -race ./internal/httpapi -count=1` | 无 race | 通过 | 已通过 |
| 静态验证 | vet、依赖和 integration 包编译 | `go vet ./...`、`go mod tidy -diff`、`go test -tags=integration ./internal/testkit/integration -run '^$' -count=1` | 全部通过 | 通过 | 已通过 |
| W4 真实链路代码覆盖 | PG + Redis + Asynq + HTTP + ingest worker 团队写 smoke | `go test -v -tags=integration ./internal/testkit/integration -run TestW4ManagementSystemTeamsPostgresRedisAsynqSmoke -count=1` | 四条团队写日志经共享清洗并写入 PostgreSQL | 本机 Docker provider 不可用，测试输出 `SKIP` | 待真实环境验证 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：管理写 handler 是否调用 `enqueueManagementOperationLog`，生产装配是否注入共享 `SettingsReader`。
- 重点看什么：Asynq enqueue 前的 payload，而不是只看业务 handler 构造的原始 changes。
- 如何避免误判：敏感字段的 handler 原始占位文案不是最终存储契约，必须以统一清洗后的 payload 和 ingest 后 PostgreSQL 结果为准。

## 完成总结

- 完成时间：2026-07-10。
- 结论：已迁移的管理写接口统一使用 Node 等价的 operation log 清洗和 best-effort 入队边界，并由源代码 guard 防止直接 enqueue 回归。
- 后续建议：在 Docker/testcontainers 健康环境复跑 W3/W4 operation log integration，并在生产接管前为设置读取失败和队列失败补统一指标。
