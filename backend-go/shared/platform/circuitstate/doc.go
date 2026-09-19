// Package circuitstate holds the authoritative Lua transition scripts and the
// shared data vocabulary for the account-circuit runtime state that the
// gateway and jobs processes both read and write in the same Redis keys.
//
// The Lua scripts are the Node account-circuit scripts verbatim
// (backend/src/modules/gateway/runtime/account-circuit-redis-store.ts at git
// HEAD). They are the authoritative transition semantics for the shared
// runtime state; the Go stores on both sides only build the same JSON
// payloads and parse the same cjson responses. Keeping a single copy removes
// the risk of the two formerly duplicated scripts drifting apart and forking
// the shared state semantics.
//
// This package intentionally contains data shapes, scripts and pure helpers
// only. Store behaviour (Redis/memory stores, coordinators, control plane)
// stays in the owning modules; it must not import business packages.
package circuitstate
