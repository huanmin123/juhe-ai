// Package gatewayproxyhealth ports the Node gateway runtime slice G13c:
//
//	backend/src/modules/gateway/runtime/proxy-health.service.ts
//	→ proxyhealth.go（上游桶运行态健康：排序避让 / 失败记录 / 半开探测 / 成功清理）
//	backend/src/modules/gateway/runtime/normal-route-latency-degradation.service.ts
//	→ latencydegradation.go（普通路由速度优先延迟降级：慢采样 / 降级 / 恢复探针 / 代际清理）
//	backend/src/modules/gateway/runtime/user-request-limit-counter.ts
//	→ userrequestlimit.go（用户请求限数：本地内存桶 + 周时分月语义）
//	backend/src/modules/gateway/runtime/user-request-limit-coordinator.ts
//	→ userrequestlimitcoordinator.go（后台 Redis Lua 同步，单飞 + 退避）
//	backend/src/modules/rate-limit/penalty-window-rate-limit.go（被两个模型列表限流消费）
//	→ penaltywindow.go（penalty window 固定/指数窗口限流：内存 + Redis Lua 双驱）
//	backend/src/modules/gateway/runtime/authenticated-models-rate-limit.service.ts
//	→ modelsratelimit.go（认证模型列表限流，实现 gatewaypreauth.AuthenticatedModelsRateLimit）
//	backend/src/modules/gateway/runtime/public-models-rate-limit.service.ts
//	→ modelsratelimit.go（公开模型列表限流）
//	backend/src/modules/gateway/runtime/account-dispatch-priority-order.ts
//	→ dispatchpriority.go（调度优先级层保序）
//	backend/src/modules/gateway/runtime/account-runtime-keys.ts（延迟降级消费的键派生）
//	→ accountruntimekeys.go
//
// server-retry-budget.ts has already been ported by G05 in
// internal/gatewaypreauth/retrybudget.go and is reused from there; this
// package does not duplicate it.
//
// Driver model mirrors the Node runtime: every service takes a Clock and a
// RuntimeStateStore / Redis client seam. A nil store selects the in-memory
// driver (Node runtimeConfig.runtimeStateDriver !== 'redis'); the Redis
// drivers reuse the exact Node Lua scripts and key layouts so both stacks can
// interoperate on the same state during the migration window. Compare-set
// operations pass the raw JSON bytes read from Redis as the expected value so
// CAS stays byte-identical regardless of which stack wrote the entry.
//
// None of the migrated services read the business database, so the
// sqlitepath/sqlpool dual-mode rule has no surface here (verified against the
// Node imports: no db/repository is reachable from these files).
package gatewayproxyhealth
