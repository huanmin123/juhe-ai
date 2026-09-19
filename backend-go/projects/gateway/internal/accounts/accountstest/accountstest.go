// Package accountstest is the manual account-test session subdomain of the
// accounts slice (REFACTOR-0005 阶段 A): the AccountTestSession/AccountTestTask
// persistence port of storage/account-test-tasks.repository.ts, the manual
// test options surface (account-manual-test-context.repository.ts +
// account-test-endpoint-modes.ts + account-test-options.service.ts + the
// catalog read subset) and the TestDispatchEffects worker port. The HTTP
// surface (account-test-dispatch.routes.ts) stays in the accounts facade,
// which consumes this package through the forwarded Store methods.
//
// 依赖方向约束：accountstest 只 import accountscore（中立类型层），不 import
// accounts 门面；Store 能力经 accountscore.StoreBase 窄端口由门面装配注入
// （门面每次调用以活字段构造 Service，兼容测试克隆 Store 后替换字段的语义）。
package accountstest

import (
	"context"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
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

// Service carries the manual-test session/options slice state: the injected
// Store capability port plus the dispatch effects seam (构造函数接管注入口，
// 门面保留 SetTestDispatchEffects 同名转发).
type Service struct {
	store accountscore.StoreBase
	// testEffects is the manual-test dispatch port. Nil until wired through
	// New / SetTestDispatchEffects; a nil port keeps the diagnostic family
	// self-contained (tests) and renders dispatch unavailable.
	testEffects TestDispatchEffects
}

// New builds the subdomain service over the injected Store capability port.
func New(store accountscore.StoreBase, effects TestDispatchEffects) *Service {
	return &Service{store: store, testEffects: effects}
}

// SetTestDispatchEffects wires the dispatch port (composition-root handover;
// nil keeps the family self-contained).
func (s *Service) SetTestDispatchEffects(effects TestDispatchEffects) {
	s.testEffects = effects
}

// testEffectsOrNil resolves the wired port for the dispatch paths (装配断线
// 时保持家族自含：返回 nil 让路由走 worker 不可用分支).
func (s *Service) testEffectsOrNil() TestDispatchEffects {
	if s.testEffects == nil {
		slog.Debug("test dispatch effects port not wired; manual test dispatch stays unavailable")
	}
	return s.testEffects
}
