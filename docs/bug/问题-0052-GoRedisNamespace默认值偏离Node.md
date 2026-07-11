# BUG-0052 Go Redis namespace 默认值偏离 Node

## 基本信息

- 编号：BUG-0052
- 状态：已修复（待真实环境验证）
- 严重程度：P1
- 发现时间：2026-07-11
- 发现方式：Node 到 Go 迁移代码审查
- 模块：后端 / Go / Redis / 配置 / 管理接口 / 运行态
- 关联计划：`PLAN-0081`
- 关联 bug：[BUG-0029 高性能账号并发读数长期为零](问题-0029-高性能账号并发读数长期为零.md)
- 修复提交：`72381a33c`

## 问题概述

- 现象：未显式配置 `JUHE_AI_REDIS_NAMESPACE` 时，Go 管理端读取不到 Node 写入的账户并发和其他共享 Redis 运行态，调用成功但值可能长期为零或缓存失效不生效。
- 触发条件：Node 与 Go 共用 Redis，双方使用同一 `JUHE_AI_SECRET`，但部署未显式设置 `JUHE_AI_REDIS_NAMESPACE`。
- 期望：Node 与 Go 从相同 secret 派生同一稳定 namespace。
- 实际：Node 使用 `env-<sha256(secret) 前 12 位>`；Go 固定使用 `juhe-ai`，随后各 Redis adapter 再拼接自己的 key 前缀。

## 根因分析

Go 配置结构把 `RedisNamespace` 默认写死为 `juhe-ai`，没有移植 Node `runtime.ts` 的 secret 派生和 namespace 清洗规则。Redis 查询不存在的 key 通常返回空集合或零值，不一定返回错误，因此问题会表现为“依赖正常但实时数据为零”，而不是显式启动失败。

该问题与 BUG-0029 的现象相似，但根因不同：BUG-0029 是授权实例与来源账号的并发事实 ID 不一致；本问题是 Node 与 Go 的部署 namespace 不一致，因此新建独立编号。

## 修复方案

- 移除 Go `RedisNamespace` 的固定 `juhe-ai` env 默认值。
- `config.Load` 在未显式配置时按当前 `JUHE_AI_SECRET` 派生 `env-<sha256 前 12 位>`。
- secret 未配置时使用与 Node 相同的开发默认 secret。
- 显式 namespace 使用与 Node 相同的 trim、非法字符替换、首尾下划线清理和 64 字符上限。
- 不增加双读、旧 key fallback 或运行时兼容分支；部署只保留一个当前 namespace。

## 验证记录

| 验证 | 结果 |
| --- | --- |
| `go test ./internal/config` | 通过 |
| `go test ./internal/platform/redis` | 通过 |
| `go test ./internal/app` | 通过 |
| `go test ./...` | 通过 |
| `pnpm --filter juhe-ai-backend test:runtime-config-env-override` | 通过，Node 默认派生基线仍成立 |
| Node / Go 共用真实 Redis 且不显式配置 namespace | 待可用真实环境验证 |

## 防复发要求

- 新增跨运行时 Redis key 时，必须同时固定 root namespace、业务 key、清洗规则和默认派生测试。
- Redis 返回零值不能直接证明 key 读取正确；真实 smoke 至少写入一个非零值并由另一运行时读回。
- 生产环境仍建议显式设置稳定的 `JUHE_AI_REDIS_NAMESPACE`，但显式配置不能掩盖默认契约不一致。

## 完成总结

Go 与 Node 的 Redis namespace 默认派生和清洗规则已统一。非容器测试已通过；真实 Node writer 到 Go reader 的共享 Redis 验证仍属于生产切流门禁。
