// Package gatewayobs 是 G19「观测 + 诊断」切片：Node
// backend/src/modules/gateway/observability/ 与 backend/src/modules/gateway/diagnostics/
// 的 Go 移植。
//
// 文件映射（Node -> Go）：
//
//	observability/routing-observability-store.ts          -> observation.go
//	observability/routing-observability-memory-store.ts   -> memory_store.go
//	observability/routing-observability-redis-store.ts    -> redis_store.go（Lua 原样携带）
//	observability/routing-observability.service.ts        -> service.go + dispatchsummary.go
//	observability/upstream-response-model.ts              -> upstreamresponsemodel.go
//	diagnostics/diagnostic-sanitizer.ts                   -> diagnosticsanitizer.go
//	diagnostics/diagnostic-response-context.ts            -> diagnosticresponsecontext.go
//	shared/redis-namespace.ts（import 链）                 -> redisnamespace.go
//	shared/redis-client.ts（import 链）                    -> redis_store.go（GetRedisClient）
//
// Node 模块级单例（observeGatewayRouting 等全局函数）在 Go 中成为显式
// Observer 类型；既有各包的 observer port 形状对照：
//
//	gatewayrouting.RoutingObserver   ObserveRouting(kind, outcome string, nowMs int64)
//	  -> *Observer.ObserveRouting 同签名，可直接满足该接口。
//	gatewayhotquality.RoutingObserver ObserveGatewayRouting(observation RoutingObservation{Kind,Outcome,Operation,Status})
//	  -> *Observer.ObserveGatewayRouting 使用本包同形 RoutingObservation
//	  （Go 接口按名义类型匹配）；接线处用 gatewayhotquality.RoutingObserverFunc
//	  一行适配。
//	gatewaycircuit.SetObservabilitySink(func(RoutingObservabilityEvent))
//	  -> 接线处把事件字段装入本包 Observation 后调用 Observer.Observe。
//
// 与 Node 的已确认架构差异（可观察行为不变）：
//   - Node async-local request context -> Go context.Context 携带
//     DispatchSummaryHolder；无 ctx 的 port 入口通过
//     ObserverOptions.ContextSource 注入（nil 时视作无请求上下文）。
//   - Node queueMicrotask 批量 flush -> Go goroutine flush；测试可注入
//     同步调度或直接调用 FlushPending。
//   - Node 单线程可变模块状态 -> Go sync.Mutex 保护；内存/Redis 计数
//     饱和上限与 JS Number.MAX_SAFE_INTEGER 对齐。
//   - 观测快照 counters 在 Redis 路径按 Go map 序列化（键序排序）；Node
//     Redis 路径保留 HGETALL 返回序。键集合与数值一致，JSON 对象键序
//     非语义。
package gatewayobs
