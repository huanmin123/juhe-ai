# BUG-0047 Go 管理设置 JSON 解析顺序偏离 Node

## 基本信息

- 编号：BUG-0047
- 状态：已修复
- 严重程度：P2
- 发现时间：2026-07-10
- 发现方式：W5 系统运行设置迁移代码复核
- 模块：后端 / Go / System API / 鉴权 / 限流 / 请求体解析
- 关联计划：[PLAN-0081 Node 转 Go 渐进减法迁移](../plans/计划-0081-Node转Go渐进减法迁移.md)
- 关联迁移记录：[W5 管理端全局品牌设置读写记录](../migration/W5-管理端全局品牌设置读取记录.md)、[W5 管理端系统运行设置迁移记录](../migration/W5-管理端系统运行设置迁移记录.md)
- 关联 bug：[BUG-0046 Go 管理设置请求体上限偏离 Node](问题-0046-Go管理设置请求体上限偏离Node.md)
- 责任人：主 agent / 维护者

## 问题概述

- 现象：Go 管理设置 PATCH 路由在 session 鉴权、touch 和登录用户限流之后才解析 JSON 请求体。
- 期望：与 Node system API 一致，执行顺序为 IP 限流、`256 KiB` JSON 解析、session 鉴权 / touch、登录用户限流、业务校验。
- 实际：未登录的非法或超限 JSON 先返回 `401`；普通用户可能先返回 `403`；已登录请求会在 body 被拒绝前 touch session 并消耗用户写限流额度。
- 影响范围：
  - `PATCH /__aisys__/api/settings/global`。
  - 新迁移的 `PATCH /__aisys__/api/settings`。

## 复现步骤

1. 不携带有效 session，向任一管理设置 PATCH 路由发送语法错误或超过 `256 KiB` 的 JSON。
2. 观察旧 Go 路径先进入写鉴权，返回 `401`。
3. 对比 Node 路径，其全局 JSON parser 位于路由鉴权之前，分别返回 `400` 或 `413`。

## 环境信息

- 分支 / 版本：`feature/20260706-go`，W5 Go opt-in 阶段。
- 数据状态：与数据库内容无关。
- 浏览器 / 系统 / Node 版本：Windows / PowerShell 7；问题位于 Go router middleware 顺序。
- 是否稳定复现：是。

## 根因分析

- 表象：相同非法请求在 Node / Go 灰度路径返回不同状态码。
- 真实根因：Go 将请求体解析放在具体 handler 内，而管理 session 和 authenticated user limiter 由 router middleware 先执行。
- 为什么会发生：迁移时只复用了 handler 级严格 decoder，没有把 Node system API 的全局中间件顺序纳入路由契约。

## 修复方案

- 新增管理设置 JSON 前置中间件：
  - 位于 system API IP limiter 之后。
  - 在管理 session 鉴权、touch 和 authenticated user limiter 之前执行。
  - 只负责 `256 KiB` 容量和单个合法 JSON 值的语法校验。
  - 读取后重建 body，保留 handler 的对象类型、字段、权限和业务校验。
- 同时接入系统运行设置和全局品牌设置 PATCH 路由。
- 新增路由回归，确认 malformed / oversized body 不调用鉴权 touch 和用户限流。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| HTTP 全包 | 两条设置 PATCH 的 parser、鉴权、限流和 handler 回归 | `go test ./internal/httpapi ./internal/app -count=1` | 全部通过 | 通过 | 已通过 |
| Race | HTTP、设置服务和 PostgreSQL 存储并发检查 | `go test -race ./internal/httpapi ./internal/modules/managementsettings ./internal/store/postgres -count=1` | 无 race | 通过 | 已通过 |
| Node 对照 | system API 中间件和设置接口回归 | `pnpm --filter juhe-ai-backend test:system-api-rate-limit`、`test:performance-system-api-smoke` | Node 契约通过 | 通过 | 已通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：Node system API app 级 middleware 顺序，而不只看具体 route handler。
- 重点看什么：IP limiter、body parser、session 鉴权 / touch、authenticated limiter 和业务 handler 的先后关系。
- 如何避免误判：相同状态码不足以证明对齐，还要断言被拒绝请求是否产生 session touch 或消耗用户限流额度。

## 完成总结

- 完成时间：2026-07-10。
- 结论：两条 Go 管理设置 PATCH 路由已恢复 Node 的 parser 与鉴权 / 限流顺序。
- 后续建议：迁移其他 JSON 写路由时明确记录 Node app 级 parser 契约，并为中间件顺序补路由测试。
