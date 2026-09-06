package accounts

import (
	"context"
	"log/slog"
)

// TestDispatchEffects is the narrow cross-package port the manual account
// test diagnostic family needs into the background worker. Node routed task
// dispatch/cancel from the admin API to the background worker over IPC
// (dispatchAccountTestTasks / dispatchAccountTestCancel,
// account-test-task-queue.service.ts + background-ipc.ts); the actual
// diagnostic execution (testOpenAIAccountWithDiagnosticRetries chain) never
// ran in the admin API process. The Go equivalent keeps execution on the jobs
// side (opsjobs.ManualTestQueue + manualtestrepo + accountprobe) and bridges
// this port at the composition root through the jobs internal-api loopback
// endpoint (POST /__aiinternal__/v1/account-test/dispatch, HMAC-signed — see
// jobs/internal/internalapi/dispatch.go, which ports the Node internal-api
// route verbatim).
//
// A nil port keeps the endpoints self-contained for tests: task creation
// succeeds and the dispatch step reports unavailable (503 for POST /{id}/test,
// matching the Node worker-unavailable path); cancel dispatch is a no-op.
// Production assembly MUST wire a real bridge.
type TestDispatchEffects interface {
	// DispatchAccountTestTasks mirrors dispatchAccountTestTasks: enqueue every
	// task id on the worker; false means the worker is unavailable (the route
	// then fails the task with the Node copy and renders 503).
	DispatchAccountTestTasks(ctx context.Context, taskIDs []string) bool
	// DispatchAccountTestCancel mirrors dispatchAccountTestCancel:
	// fire-and-forget cancel signal (session cancel fans one call per task).
	DispatchAccountTestCancel(taskID string)
}

// SetTestDispatchEffects wires the dispatch port (composition-root handover;
// nil keeps the family self-contained).
func (s *Store) SetTestDispatchEffects(effects TestDispatchEffects) {
	s.testEffects = effects
}

// SetTestDispatchEffects is the Deps-level alias so composition roots can set
// the field and let Mount wire it, mirroring the Authorized reader precedent.
func (d *Deps) wireTestEffects() {
	if d.TestDispatch != nil {
		d.Store.SetTestDispatchEffects(d.TestDispatch)
	}
}

func (s *Store) testEffectsOrNil() TestDispatchEffects {
	if s.testEffects == nil {
		slog.Debug("test dispatch effects port not wired; manual test dispatch stays unavailable")
	}
	return s.testEffects
}
