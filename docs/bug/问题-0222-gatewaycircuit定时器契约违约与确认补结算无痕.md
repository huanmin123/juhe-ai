# BUG-0222 gatewaycircuit Bridge 默认 NewTimer 违反 stop 契约与确认补结算失败无痕

## 基本信息

- 编号：BUG-0222
- 状态：已修复（已发布 2026-09-28 16:00 全量，验证通过）
- 严重程度：P2
- 发现时间：2026-09-28
- 发现方式：自查（迁移后整体深度复审，goroutine 泄漏指纹扫描）
- 模块：后端 / 网关 / 熔断 / gatewaycircuit / 资源生命周期
- 关联计划：无
- 关联 bug：无
- 责任人：主代理（复审交付）

## 问题概述

- 现象：两层缺陷——①熔断 Bridge 的默认 NewTimer 实现在 rebuild 抢跑场景下泄漏 goroutine 到进程停机；②确认槽位的二次补结算失败完全无日志留痕。
- 期望：①stop 调用后 done 通道必须可读（wait.go:82-87 契约原文逐字预言了该违约后果："stop 只调 timer.Stop 而不关 done 会让被抢跑结算废弃的 goroutine 永久泄漏"）；②补结算失败应 Warn 留痕。
- 实际：①默认 `newTimer` 的 stop 仅 `timer.Stop()` 不关 done，`retryPendingImmediately` 抢跑清理后 1396 行的 select goroutine 卡到 `Bridge.Close`；②`releaseAcquiredConfirmation` 第二次 `CompleteConfirmation` 错误 `_, _ =` 丢弃。
- 影响范围：①生产装配（chain_circuit_controlplane 的 `NewBridge` 未注入 NewTimer）使用违约默认值，每次 rebuild 对每个带 pending 退避 timer 的 scope 泄漏 1 个 goroutine（低频但真实，运行期不可回收）；②确认槽位悬挂时无线索可查。

## 复现步骤

1. 构造 Bridge 持久化失败触发 retry 退避布防，随后 rebuild 走 `retryPendingImmediately` 抢跑 stop。
2. 观察 goroutine 数不回落（停机前不释放）。

## 环境信息

- 分支 / 版本：master（2026-09-28 工作区）
- 是否稳定复现：是（代码路径确定性）

## 根因分析

- 表象：goroutine 缓慢增长、确认悬挂无日志。
- 真实根因：①同包两处实现不同步——`wait.go:97-115` WaitCoordinator 修复过同款契约（R3 修复，sync.Once + Stop + closeDone），Bridge 的默认值未同步对齐；②service.go 无 logger 注入面，补结算路径沿用 `_, _ =` 丢弃。
- 为什么会发生：NewTimer 契约只写在 wait.go 注释里，无编译期/测试强制；BridgeOptions.NewTimer 文档未声明契约。

## 修复方案

- 修改点：
  1. `bridge.go` 默认 newTimer 对齐 wait.go 范式（sync.Once 包 closeDone，stop 无条件 Stop 后幂等 close）；`BridgeOptions.NewTimer` 文档补注入契约。
  2. `service.go` ServiceOptions 新增可选 `Logger`（nil 默认 NopLogger，沿用包内 suppression.go 注入模式）；`releaseAcquiredConfirmation` 二次补结算失败 Warn 结构化留痕（event/accountRuntimeKey/scopeKey/generation/dispatchRevision/leaseID/error），best-effort 语义不变。
  3. 生产装配接线：`chain_wiring_w2c.go` 的 `newChainAccountCircuitService` 加 logger 参数，`chain_runtime.go` 调用点传 `chainCircuitWaitLogger{inner: slog.Default()}`（复用既有 slog 适配器）；测试调用点传 nil。
- 行为影响：retry 调度语义零变化（延迟计算、backoff 存储、rebuild 行为不动）；仅 stop 契约收口与日志补齐。
- 发布异常处理：无需。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元回归 | 新增 3 测试（stop 后 done 可读 / fire 后 stop 幂等 / 抢跑 goroutine 从 done 醒来） | `go test -count=1 -run TestBug0222 ./projects/gateway/internal/gatewaycircuit/ -v` | 3/3 PASS | 3/3 PASS | 通过 |
| 包回归 | gatewaycircuit 全量 | `go test -count=1 ./projects/gateway/internal/gatewaycircuit/` | 全绿 | ok 13.383s | 通过 |
| 装配回归 | 组合根相关测试族 | `go test -count=1 -run 'TestChainCircuit|TestW17e|TestComposeSystemAPIMounts|TestW1X' ./projects/gateway/cmd/juhe-ai-gateway/` | 全绿 | ok 18.003s | 通过 |
| 静态检查 | `go vet` 两包 | 通过 | 通过 | 通过 | 通过 |

## 复发记录

- 无。

## 下次遇到

- 先查什么：任何自定义 newTimer/timer 工厂注入点的 stop 语义是否满足"stop 后 done 必可读"。
- 重点看什么：包内契约注释（wait.go）与各默认实现是否同步；同一契约多处实现必须共享测试。
- 如何避免误判：泄漏仅在 rebuild 抢跑窗口触发，常规测试看不到；需构造 pending 退避 + 抢跑路径。

## 完成总结

- 完成时间：2026-09-28
- 结论：stop 契约对齐 + 补结算日志 + 生产装配注入，全部验证通过。
- 后续建议：可选——将来把 NewTimer 契约提升为接口文档注释统一引用。
