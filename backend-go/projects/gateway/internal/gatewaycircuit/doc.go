// Package gatewaycircuit is the Go migration of the Node gateway account
// circuit work package (G11): the protocol/model -> account circuit state
// machine (account-circuit-store.ts + memory/redis stores), the foreground
// confirmation service (account-circuit.service.ts), the control-plane bridge
// (account-circuit-control-plane-bridge.ts), the process-local suppression /
// degradation store (account-local-suppression-store.ts), the recoverable
// unavailable wait coordinator (recoverable-unavailable-wait.ts), the
// availability probe coordinator (availability-probe-coordinator.ts), the
// precheck probe policy (account-probe-confirmation-policy.ts), the precheck
// summary mapper (account-precheck-summary.mapper.ts) and the dispatch
// priority tier preservation (account-dispatch-priority-order.ts).
//
// Behavior, phase names, status strings, Chinese error/log copy and time
// arithmetic mirror the Node baseline byte for byte. All time comes from an
// injected clock (Now func() int64 milliseconds). The Redis store embeds the
// Node Lua scripts verbatim so the transition semantics cannot drift.
package gatewaycircuit
