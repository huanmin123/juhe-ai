package gatewaydispatch

// T91-6 / B2-M5 钉住测试：coordination.SameAccountRetry 钉住单账户的安全重试
// 臂也必须经过电路准入。首次尝试后账户进入 OPEN/SUSPECT 时，重试按既有被拦
// 出口（lastAttempt=account:circuit_blocked + 候选耗尽失败）阻断，不触达上游、
// 不写电路状态（准入只读：不重复计数、不重复写 confirmation）；健康（CLOSED）
// 账户的同账户重试仍照常执行。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

// t91CircuitFixture 构造与引擎同源的电路 store/service 及账户 scope/revision。
func t91CircuitFixture(t *testing.T, account AccountCandidate) (*gatewaycircuit.MemoryStore, *gatewaycircuit.CircuitService, gatewaycircuit.Scope, string) {
	t.Helper()
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 64})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	circuits, err := gatewaycircuit.NewCircuitService(store, gatewaycircuit.ServiceOptions{})
	if err != nil {
		t.Fatalf("circuit service: %v", err)
	}
	model := "gpt-test"
	scope, err := gatewaycircuit.GatewayAccountProtocolModelScope(account, "text", &model)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	revision, err := gatewaycircuit.AccountCircuitDispatchRevision(account)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	return store, circuits, scope, revision
}

// t91PinnedRetryArgs 构造同账户安全重试的派发参数（候选窗塌缩到 pinned）。
func t91PinnedRetryArgs(t *testing.T, accounts []AccountCandidate, pinned AccountCandidate) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	coordination := newTestCoordination(t)
	coordination.RequestBodyOverride = &RequestBodyOverride{AccountID: pinned.ID, Body: []byte(`{"model":"gpt-test","input":"retry"}`)}
	coordination.SameAccountRetry = &SameAccountRetry{Account: pinned}
	args := dispatchArgs(t, req, accounts)
	args.RequestCoordination = coordination
	return args
}

// t91UpstreamServer 返回 2xx JSON 的上游替身，hits 记录真实触达次数。
func t91UpstreamServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-ok"}`))
	}))
	t.Cleanup(server.Close)
	return server, hits
}

// TestT91SameAccountRetryBlockedByCircuit OPEN/SUSPECT 账户的同账户重试必须被
// 电路准入阻断，且准入不写任何电路状态。
func TestT91SameAccountRetryBlockedByCircuit(t *testing.T) {
	for _, phase := range []string{gatewaycircuit.PhaseOpen, gatewaycircuit.PhaseSuspect} {
		t.Run(phase, func(t *testing.T) {
			server, hits := t91UpstreamServer(t)
			accounts := testAccounts("a-1")
			pinned := accounts[0]
			store, circuits, scope, revision := t91CircuitFixture(t, pinned)
			accountScope := gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey}
			ctx := context.Background()
			if phase == gatewaycircuit.PhaseSuspect {
				if _, err := store.Suspect(ctx, gatewaycircuit.SuspectInput{
					Scope:            accountScope,
					DispatchRevision: revision,
					TransitionID:     "t91-suspect",
					Reason:           "t91",
				}); err != nil {
					t.Fatalf("plant suspect: %v", err)
				}
			} else {
				state := gatewaycircuit.ClosedState(accountScope, revision, 1, "t91-open", 0)
				state.Phase = gatewaycircuit.PhaseOpen
				if _, err := store.Restore(ctx, state, nil); err != nil {
					t.Fatalf("plant open: %v", err)
				}
			}
			before, err := store.Get(ctx, accountScope, nil)
			if err != nil {
				t.Fatalf("state before: %v", err)
			}
			if before.Phase != phase {
				t.Fatalf("planted phase = %s, want %s", before.Phase, phase)
			}

			engine, driver, _ := newTestEngine(t)
			engine.Circuits = circuits
			driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}

			_, err = engine.FetchFirstAvailableUpstream(ctx, t91PinnedRetryArgs(t, accounts, pinned))
			var attemptErr *UpstreamAttemptError
			if !errorsAs(err, &attemptErr) {
				t.Fatalf("expected UpstreamAttemptError, got %v", err)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("OPEN/SUSPECT 账户的同账户重试不得触达上游, hits = %d", got)
			}
			if attemptErr.LastAttempt == nil {
				t.Fatal("被拦重试必须由 lastAttempt 承载")
			}
			if attemptErr.LastAttempt.UpstreamURL != "account:circuit_blocked" {
				t.Fatalf("lastAttempt code = %q", attemptErr.LastAttempt.UpstreamURL)
			}
			if !strings.Contains(attemptErr.LastAttempt.Message, phase) {
				t.Fatalf("lastAttempt message 必须带 phase %s: %q", phase, attemptErr.LastAttempt.Message)
			}
			after, err := store.Get(ctx, accountScope, nil)
			if err != nil {
				t.Fatalf("state after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("准入必须只读（不得重复计数/重复写 confirmation）:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

// TestT91SameAccountRetryHealthyAccountStillDispatches CLOSED 账户的同账户重试
// 仍照常派发到钉住账户。
func TestT91SameAccountRetryHealthyAccountStillDispatches(t *testing.T) {
	server, hits := t91UpstreamServer(t)
	accounts := testAccounts("a-1")
	pinned := accounts[0]
	store, circuits, scope, _ := t91CircuitFixture(t, pinned)

	engine, driver, _ := newTestEngine(t)
	engine.Circuits = circuits
	driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), t91PinnedRetryArgs(t, accounts, pinned))
	if err != nil {
		t.Fatalf("健康账户的同账户重试必须放行: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("健康账户重试必须真实触达上游一次, hits = %d", got)
	}
	// 准入只读：CLOSED 账户不产生电路状态写。
	state, err := store.Get(context.Background(), gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey}, nil)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Phase != gatewaycircuit.PhaseClosed {
		t.Fatalf("健康账户不得被准入写为非 CLOSED: %s", state.Phase)
	}
}
