package accounts

import (
	"context"
	"log/slog"
)

// TestDispatchEffects is the narrow dispatch port the manual account test
// diagnostic family needs into the background executor. Node routed task
// dispatch/cancel from the admin API to the background worker over IPC
// (dispatchAccountTestTasks / dispatchAccountTestCancel,
// account-test-task-queue.service.ts + background-ipc.ts). The Go equivalent
// (去跨进程战役) wires this port at the composition root to the in-process
// shared execution chain (backend-go-platform/accounttest ManualTestQueue +
// manualtestrepo + manualtest + accountprobe, see
// compose_account_test_local.go): the queue runs in this process as the
// single sweep owner, no cross-process HTTP hop remains.
//
// A nil port keeps the endpoints self-contained for tests: task creation
// succeeds and the dispatch step reports unavailable (503 for POST /{id}/test,
// matching the Node worker-unavailable path); cancel dispatch is a no-op.
// Production assembly MUST wire the in-process adapter.
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

// TestDispatchEffects exposes the wired port for composition-root
// effectiveness assertions (装配断线零容忍：组合根测试用它断言端口已接线).
func (s *Store) TestDispatchEffects() TestDispatchEffects {
	if s == nil {
		return nil
	}
	return s.testEffects
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
