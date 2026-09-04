// Package gatewayclientip owns the G13a "client-ip runtime family": the
// Client-IP policy cache + hit buffer, the per client-IP concurrency slots,
// the client-IP error circuit (including the pre-auth circuit), the
// client-IP account avoidance memory and the high-concurrency group queue.
//
// Node → Go mapping (backend/src/modules/gateway/runtime/):
//
//	client-ip-policy-cache.service.ts        → policy.go + policycache.go
//	client-ip-concurrency.service.ts         → concurrency.go
//	client-ip-error-circuit.service.ts       → circuit.go
//	client-ip-account-avoidance.service.ts   → avoidance.go
//	high-concurrency-queue.service.ts        → queue.go
//	runtime/client-ip hit-buffer regression  → policycache_test.go (行为对齐
//	    backend/src/scripts/regression/client-ip-policy-hit-buffer-regression.ts)
//
// Support files port the consumed dependencies:
//
//	storage/client-ip-normalization.ts       → normalize.go
//	domain/group-scheduling.ts (consumed
//	    subset: policy resolution defaults)  → scheduling.go
//	shared/runtime-state-store.ts            → statestore.go
//	runtime/account-dispatch-priority-order
//	    .ts (preserveGatewayAccountDispatchPriorityTiers) → priorityorder.go
//	storage/client-ip-policy.repository.ts
//	    (list / find-by-hash / record-hits)  → policysql.go (dual mode
//	    SQLite path pool or PostgreSQL pool through database/sql)
//
// G05 ports (internal/gatewaypreauth, read-only) this package satisfies:
//
//	gatewaypreauth.ClientIPPolicy            → *PolicyCache
//	gatewaypreauth.PreAuthCircuits           → *ErrorCircuit
//	gatewaypreauth.ClientIPAccountAvoidanceFactory → *Avoidance
//
// G10 seam: the high-concurrency group queue consumes account live
// concurrency through AccountConcurrencySource, which embeds the
// gatewayruntimecache.ConcurrencySource interface.
//
// Behavior contract: every threshold, TTL, queue bound and Chinese log /
// error message mirrors the Node implementation byte for byte. All time
// reads flow through an injected gatewayruntimecache.Clock and the hit
// buffer flush runs through an injectable scheduler so tests stay
// deterministic under -race.
package gatewayclientip
