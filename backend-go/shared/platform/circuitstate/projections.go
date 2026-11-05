package circuitstate

// Account-circuit outbox projection keys. The outbox is a fan-out log: every
// event is written once per projection key and each consumer claims only its
// own key. The gateway main circuit runtime and the J3b circuit runtime use
// different Redis namespaces, so they must never compete for the same row
// (DR-33 / BUG-0295).
const (
	// ProjectionKeyRuntime is the main gateway/jobs account-circuit runtime.
	ProjectionKeyRuntime = "account_circuit_runtime_v1"
	// ProjectionKeyJ3B is the J3b model-check circuit runtime.
	ProjectionKeyJ3B = "account_circuit_runtime_j3b_v1"
)
