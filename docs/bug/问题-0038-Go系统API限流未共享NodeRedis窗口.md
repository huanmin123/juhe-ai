# BUG-0038 Go 系统 API 限流未共享 Node Redis 窗口

## 基本信息

- 编号：BUG-0038
- 状态：已修复（待真实环境验证）
- 严重程度：P1
- 发现时间：2026-07-10
- 发现方式：Node 转 Go 契约对照 / 自查
- 模块：后端 / Go / System API / Redis / 限流 / 灰度切流
- 关联计划：[PLAN-20260706T071505000Z Node 转 Go 渐进减法迁移](../plans/计划-20260706T071505000Z-Node转Go渐进减法迁移.md)
- 关联 bug：无
- 责任人：主 agent / 维护者

## 问题概述

- 现象：Node 与 Go System API 同时灰度时，即使连接同一 Redis state 和使用同一 namespace，也分别维护不同的限流 key 与 value 格式。
- 期望：两种运行时对同一 IP / 用户、同一 read / write 类型共享 minute / burst bucket，任一运行时消费的请求都计入同一全局窗口。
- 实际：
  - Node key 为 `juhe-ai:{namespace}:rate-limit:fixed:{base64url(storeHash)}:{base64url(identityHash)}`，value 为 `count:resetAtMs`。
  - 旧 Go key 位于自身 state client namespace 下，使用不同的路径、hex 身份 hash 和整数 counter value。
- 影响范围：Go `SystemAPIIPRateLimiter`、`SystemAPIAuthenticatedRateLimiter`，以及 Node / Go 并行灰度时的后台接口全局限流准确性。

## 复现步骤

1. Node 和 Go 配置相同 `JUHE_AI_REDIS_STATE_URL` 与 `JUHE_AI_REDIS_NAMESPACE`。
2. 从 Node 消费某个 IP read bucket 的额度。
3. 再向 Go 发送同身份请求。
4. 旧 Go 读取不到 Node counter，仍按独立窗口放行。

## 环境信息

- 分支 / 版本：`feature/20260706-go`，System API Go opt-in 灰度阶段。
- 数据状态：Redis state 可用，Node / Go 共用部署 namespace。
- 浏览器 / 系统 / Node 版本：不适用；跨运行时 Redis 协议问题。
- 是否稳定复现：是。

## 根因分析

- 表象：单独运行 Node 或 Go 时限流都能工作，但并行灰度总额度被放大。
- 真实根因：Go 只迁移了限流阈值和 fixed-window 行为，没有迁移 Node Redis key、base64url SHA-256、`count:resetAtMs` value 和多 bucket 原子 Lua 协议。
- 为什么会发生：原 Go 复用了通用整数 counter `AllowFixedWindow`，而 Node System API 使用的是另一套带绝对 reset 时间的专用协议。

## 修复方案

- 修改点：
  - Redis client 新增独立 `AllowNamedFixedWindowRaw`，保留现有 `AllowFixedWindow` 不变。
  - 专用 Lua 精确使用 Node 的 `now_ms`、bucket name、window、limit、`count:resetAtMs` 和先检查后统一提交语义。
  - raw key 绕过 Go state client 自身 namespace，按 Node `juhe-ai:{namespace}:rate-limit:fixed:*` 构造。
  - IP / 用户身份 key 使用 Node 同款 UTF-8 SHA-256 + base64url，传入 limiter 时不暴露原始 IP。
  - minute / burst / user store name 固定为 `system_api_ip_minute`、`system_api_ip_burst`、`system_api_user_minute`。
  - server 和 W1a maintenance smoke 显式传入部署 Redis namespace。
- 行为影响：相同 Redis state 与 namespace 下，Node 和 Go 会消费同一 System API 全局限流窗口。
- 发布异常处理：上线前应清理或等待旧 Go 专用限流 key 自然过期；新代码不会读取旧整数 counter，也不会修改其他模块使用的通用 fixed-window key。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| Go 定向测试 | Redis 参数、HTTP limiter、app 和 maintenance | `go test ./internal/platform/redis ./internal/httpapi ./internal/app ./internal/maintenance -count=1` | key、hash、store、Lua 参数与装配通过 | 通过 | 已通过 |
| 目标 race | Redis client 与 HTTP limiter 并发回归 | `go test -race ./internal/platform/redis ./internal/httpapi -count=1` | 无 race | 通过 | 已通过 |
| Go 全量测试 | 相邻模块回归 | `go test ./... -count=1` | 全部通过 | 通过 | 已通过 |
| 静态验证 | vet 与依赖整洁 | `go vet ./...`、`go mod tidy -diff` | 无输出 | 通过 | 已通过 |
| Node 契约 | System API 限流与 Redis 边界 | `pnpm --filter juhe-ai-backend test:system-api-rate-limit`、`pnpm --filter juhe-ai-backend test:performance-redis-boundary` | 当前 Node 契约通过 | 通过 | 已通过 |
| Integration 编译 | Go integration 包 | `go test -tags=integration ./internal/testkit/integration -run '^$' -count=1` | 编译通过 | 通过 | 已通过 |
| 真实 Redis 跨运行时 | Node 先消费、Go 再读取同 bucket | 使用同一 Redis state / namespace 运行 Node 与 Go | 第二运行时能观察并拒绝超限请求 | 未执行 | 待真实环境验证 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：两个运行时的最终 Redis key、value 格式和 Lua 参数是否逐项一致。
- 重点看什么：namespace 是否重复或缺失，hash 编码是否为 base64url，是否把整数 counter 与 `count:resetAtMs` 混用。
- 如何避免误判：分别通过各自单元测试不代表共享状态；必须验证跨运行时先后消费同一个 bucket。

## 完成总结

- 完成时间：2026-07-10。
- 结论：Go System API Redis limiter 已使用 Node 兼容 key、value 和原子 Lua 协议，同时保留原始身份隐私门禁。
- 后续建议：在可用 Redis 环境补 Node / Go 双进程交叉消费 smoke，并把该 smoke 纳入正式切流门禁。
