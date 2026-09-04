// Package gatewayhotquality is the G13b slice of the W4-W5 gateway chain:
// the hot quality (热质量) storage/runtime layer, same-tier exploration
// (同层探索) credit stores and the speed-first (速度优先) body admission and
// cutover reservation helpers.
//
// It mirrors backend/src/modules/gateway/runtime/:
//
//   - hot-quality-store.ts            → store.go (types, constants, model
//     family catalog, scope keys, normalization)
//   - hot-quality-snapshot.ts         → snapshot.go (window snapshots,
//     reliability/confidence math, first-byte EWMA + p95 bucket)
//   - hot-quality-memory-store.ts     → memory_store.go (process-local driver)
//   - hot-quality-redis-store.ts      → redis_store.go (Redis driver; the
//     mutation/read/stats Lua scripts are carried over verbatim so both
//     runtimes speak the same Redis payload)
//   - same-tier-exploration-store.ts  → exploration.go (state invariants,
//     credit/cursor normalization, fencing tombstones)
//   - same-tier-exploration-memory-store.ts → exploration_memory_store.go
//   - same-tier-exploration-redis-store.ts  → exploration_redis_store.go
//   - hot-quality-runtime.service.ts  → runtime.go (driver singleton, account
//     ordering orchestration, route scope/pool keys, model family bucketing)
//   - hot-quality-attempt-lifecycle.ts → attempt_lifecycle.go (attempt +
//     terminal recording with first-byte capture and swallowed errors)
//   - speed-first-body-admission.service.ts → speedfirst_body_admission.go
//   - speed-first-cutover-reservation.service.ts →
//     speedfirst_cutover_reservation.go
//
// Candidate *selection* (tier ordering, cursor fairness decision) is owned by
// internal/gatewayhybrid (G09, hot-quality-candidate-selection.ts); this
// package imports gatewayhybrid for the shared tier key, decision-state and
// credit constants, and converts its full snapshot into the reduced
// gatewayhybrid.HotQualitySnapshot selection view. Body lane detection lives
// in internal/gatewaybody and is imported by callers, not here.
//
// Every store is dual-driver (memory + Redis, Redis keys under the
// juhe-ai:<namespace> convention), every time source is an injected clock
// (Node injects Date.now the same way) and every external capability Node
// reaches through module singletons (routing observability, request logger,
// Redis client, account-concurrency slot acquirer) is a port here and is
// mocked in tests, so status transitions, credit boundaries (1.0 / 0.99 /
// exhausted), snapshot cloning and reservation races replay deterministically.
package gatewayhotquality
