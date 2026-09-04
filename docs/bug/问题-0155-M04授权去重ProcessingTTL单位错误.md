# BUG-0155 M04 授权去重 ProcessingTTL 单位错误

## 基本信息

- 编号：BUG-0155
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 网关 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：BUG-0154
- 责任人：待定

## 问题概述

- 现象：M04 `authorizations` 路由在 `backend-go/projects/gateway/internal/authz/routes.go:81-87` 配置 `ProcessingTTL: 120_000_000`。
- 期望：同一授权 mutation 在处理期间保持 Node 的 processing 去重窗口，避免并发或重试重复写入。
- 实际：Go 的 `time.Duration` 单位为纳秒，`120_000_000` 仅为 120ms；Node `deduplication.service.ts` 的默认 processing TTL 为 `120_000ms`（120 秒），且 M04 Node 路由未覆盖该默认值。请求在 120ms 后即可重新 claim，远早于 Node。
- 影响范围：授权创建等写操作遇到慢数据库、网络重试或客户端超时后，可能在前一请求仍处理时被第二次执行，产生重复授权或并发状态竞争。当前 M04 Go 路由尚未接入生产 gateway，影响会在切换后出现。

## 复现步骤

1. 查看 `HEAD:backend-go/projects/gateway/internal/authz/routes.go` 的 `ProcessingTTL` 数值及 `time.Duration` 类型。
2. 查看 `HEAD:backend/src/modules/deduplication/deduplication.service.ts` 的 `defaultProcessingTtlMs = 120_000`，以及 Node authorizations mutation guard 未传入覆盖值。
3. 让第一次授权请求保持 processing 超过 120ms，再以相同去重键重试；Go 允许再次 claim，Node 应仍返回 processing 冲突。

## 环境信息

- 分支 / 版本：审计范围 `af841ce7a094bc66fbbb0c3817ea1fc0797245f1..fbad7b341b4fc5a7ae7457668b867f6b7091213d`（仅已提交对象）
- 数据状态：无需业务数据；TTL 单位由源码确定
- 浏览器 / 系统 / Node 版本：未执行浏览器验证
- 是否稳定复现：是（单位换算确定）

## 根因分析

- 表象：Go 路由显式填写了一个看似与 Node 数值相近的 `120_000_000`。
- 真实根因：把 Node 的毫秒配置直接复制到 Go `time.Duration`，没有乘 `time.Millisecond`；测试只验证单请求生命周期，没有等待 processing 窗口后重试。
- 为什么会发生：跨语言时间单位未在迁移契约中固化，去重测试缺少慢处理和 TTL 边界场景。

## 修复方案

- 修改点：将 Go processing TTL 与 Node 默认 120 秒建立显式常量和单位转换，补充 120ms、120s 边界及并发重试回归；确认迁移后所有授权 mutation 使用同一配置。
- 行为影响：恢复 Node 的 processing 防重复窗口，不增加用户可见 API 字段。
- 发布异常处理：修复和回归通过前不得切换 M04 owner；不通过跨进程调用 Node 维持去重状态。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 静态单位检查 | Go/Node TTL 对照 | 查看上述两个提交对象 | 数值和单位等价 | 120ms vs 120s | 未通过 |
| 定向测试 | processing 窗口内重复 claim | 授权请求延迟后重试 | 120s 内保持 processing | 现有测试未覆盖 | 未执行 |
| 并发回归 | 慢事务 + 客户端重试 | mock 慢存储并发请求 | 只产生一次授权写入 | 未执行 | 未执行 |

## 复发记录

- 时间：无
- 环境：无
- 现象：无
- 关联处理：无

## 下次遇到

- 先查什么：先确认语言的时间类型单位，再与 Node 的毫秒常量逐项换算。
- 重点看什么：processing/succeeded/failed 三种 TTL 以及慢请求、重试、并发写入。
- 如何避免误判：不能只比较数字字面量；必须比较实际时间长度和重复 claim 行为。

## 完成总结

- 完成时间：待修复
- 结论：M04 Go 授权去重 processing 窗口比 Node 短 1000 倍，存在重复写入风险。
- 后续建议：修正单位并补慢处理回归后，再进行 M04 完整 owner 和归档审计。
