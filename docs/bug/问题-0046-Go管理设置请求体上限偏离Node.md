# BUG-0046 Go 管理设置请求体上限偏离 Node

## 基本信息

- 编号：BUG-0046
- 状态：已修复
- 严重程度：P2
- 发现时间：2026-07-10
- 发现方式：Node 转 Go 已迁移切片横向审计
- 模块：后端 / Go / 管理接口 / 请求体校验
- 关联计划：[PLAN-20260706T071505000Z Node 转 Go 渐进减法迁移](../plans/计划-20260706T071505000Z-Node转Go渐进减法迁移.md)
- 关联迁移记录：[W5 管理端全局品牌设置读写记录](../migration/W5-管理端全局品牌设置读取记录.md)、[W5 管理端系统运行设置迁移记录](../migration/W5-管理端系统运行设置迁移记录.md)
- 关联 bug：无
- 责任人：主 agent / 维护者

## 问题概述

- 现象：已迁移的 Go 全局品牌设置 PATCH 接口接受最大 `1 MiB` 请求体，超限时返回 `400`。
- 期望：与 Node system API 的全局 JSON parser 保持一致，最大请求体为 `256 KiB`，超限返回 `413 { "message": "请求体过大" }`。
- 实际：`256 KiB` 到 `1 MiB` 之间的请求只会在 Go 路径被接受；超过 `1 MiB` 时 Go 也使用了错误的状态码。
- 影响范围：W5 `PATCH /__aisys__/api/settings/global`，以及引用同一边界设计的后续系统设置迁移。

## 复现步骤

1. 构造一个略大于 `256 KiB`、小于 `1 MiB` 的全局设置 JSON 请求体。
2. 分别请求 Node 与 Go 的 `PATCH /__aisys__/api/settings/global`。
3. Node 返回 `413`，旧 Go 实现继续解析或按其他校验返回 `400`，两条灰度路径行为不一致。

## 环境信息

- 分支 / 版本：`feature/20260706-go`，W5 Go opt-in 阶段。
- 数据状态：与数据库内容无关，请求在 HTTP body 边界被处理。
- 浏览器 / 系统 / Node 版本：Windows / PowerShell 7；问题位于 Go HTTP handler。
- 是否稳定复现：是。

## 根因分析

- 表象：全局设置 handler 的超限状态码与 Node 不同。
- 真实根因：Go 独立定义了 `managementGlobalSettingsMaxBodyBytes = 1 << 20`，并将 `http.MaxBytesError` 归类为普通 `400`，没有复用 Node system API 的 `256kb` 全局 JSON 限制语义。
- 为什么会发生：迁移时只对齐了字段级严格 JSON 校验，没有把 system API parser 的容量上限和 Express 超限状态码纳入接口契约。

## 修复方案

- 将 Go 管理设置请求体上限调整为 `256 KiB`。
- 将读取首个 JSON 值和检测尾随 JSON 时出现的 `http.MaxBytesError` 统一映射为 `413`。
- 保持语法错误、类型错误、未知字段、空对象、顶层非对象和尾随 JSON 为 `400`。
- 同步修正 W5 全局品牌设置与系统运行设置迁移记录。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| HTTP 定向测试 | 无效 body 与超限 body 状态码 | `go test ./internal/httpapi -run 'TestManagementGlobalSettingsUpdateHandlerStrictBodyValidation' -count=1` | 超限返回 413，其余非法 body 返回 400 | 通过 | 已通过 |
| 静态验证 | 修改文件空白与补丁格式 | `git diff --check -- <本次文件>` | 无错误 | 通过 | 已通过 |

## 复发记录

- 2026-07-22：公告 Go create 路由的 mutation guard 曾沿用默认 `1 MiB` JSON 读取，超过 Node system API `256 KiB` 的请求可能进入公告 handler；独立 handler 又会把超限误报为 `400`。修复为共享 `announcementJSONMaxBodyBytes = 256 << 10`，guard 和 handler 均将 `http.MaxBytesError` 映射为 `413 { "message": "请求体过大" }`，并固定超限时不调用公告 create service。该修复不改变 Node writer / 发布 owner，只补 Go opt-in 契约。

## 下次遇到

- 先查什么：Node system API 的全局 parser 配置与错误中间件，而不只看具体路由 schema。
- 重点看什么：请求体字节上限、超限状态码、尾随 JSON 和字段级错误是否分别对齐。
- 如何避免误判：容量限制属于接口契约；即使正常表单永远达不到上限，也必须保证 Node / Go 灰度路径一致。

## 完成总结

- 完成时间：2026-07-10。
- 结论：Go 已迁移管理设置接口恢复为 Node 的 `256 KiB/413` 请求体边界，后续 W5 系统运行设置沿用同一契约。
- 后续建议：新增管理 JSON 写接口时复用统一 body decoder 或明确引用同一常量和错误映射；已接入 mutation guard 的写路由还必须验证 guard 与实际 handler 都不会接受超过 `256 KiB` 的请求。
