package gatewaydispatch

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestSessionAffinitySameLayer(t *testing.T) {
	owner := AccountCandidate{ID: "owner", Priority: 5}
	same := AccountCandidate{ID: "same", Priority: 5}
	different := AccountCandidate{ID: "different", Priority: 6}
	accounts := []AccountCandidate{owner, same, different}

	if !sessionAffinitySameLayer(accounts, "owner", same, nil) {
		t.Fatal("same four-tuple must be same layer")
	}
	if sessionAffinitySameLayer(accounts, "owner", different, nil) {
		t.Fatal("different priority must be a different layer")
	}
	if sessionAffinitySameLayer(accounts, "missing", same, nil) {
		t.Fatal("owner outside the candidate window must not latch")
	}
	if !sessionAffinitySameLayer(accounts, "same", same, nil) {
		t.Fatal("same owner must compare as same layer")
	}
}

func TestSessionAffinityOwnerWasCapacityRejected(t *testing.T) {
	failures := []AccountCapacityLimitFailure{{account: AccountCandidate{ID: "owner"}}}
	if !sessionAffinityOwnerWasCapacityRejected(failures, "owner") {
		t.Fatal("owner capacity rejection not detected")
	}
	if sessionAffinityOwnerWasCapacityRejected(failures, "other") {
		t.Fatal("other account must not match")
	}
	if sessionAffinityOwnerWasCapacityRejected(failures, "") {
		t.Fatal("empty owner must not match")
	}
}

func TestRemainingAfterAffinityOwnerWait(t *testing.T) {
	if got := remainingAfterAffinityOwnerWait(1200, 300, nil); got != 900 {
		t.Fatalf("remaining = %d, want 900", got)
	}
	if got := remainingAfterAffinityOwnerWait(300, 300, nil); got != 0 {
		t.Fatalf("exhausted remaining = %d, want 0", got)
	}
	if got := remainingAfterAffinityOwnerWait(1200, 0, nil); got != 1200 {
		t.Fatalf("unspent remaining = %d, want 1200", got)
	}
}

func TestConfirmSessionAffinityRebindFunc(t *testing.T) {
	scope := AffinityScope{SystemAccountID: "sys", APIKeyID: "key", GroupID: "grp"}
	ctx := context.Background()

	// Same owner: refresh lease.
	port := &fakeAffinity{}
	fn := newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "a", false, false, false, true, scope, AuditCapture{})
	if fn == nil {
		t.Fatal("same-owner confirm must not be nil")
	}
	fn()
	if len(port.rebinds) != 1 || port.rebinds[0].expectedOwner != "a" || port.rebinds[0].newOwner != "a" {
		t.Fatalf("same-owner rebind = %+v", port.rebinds)
	}

	// Owner change without capacity rejection / soft busy: metric-only closure
	// (cross-account switch counted), no rebind.
	fn = newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "b", false, false, false, true, scope, AuditCapture{})
	if fn == nil {
		t.Fatal("cross-account switch must still carry a settlement closure")
	}
	fn()
	if len(port.rebinds) != 1 {
		t.Fatalf("non-eligible owner change must not rebind: %+v", port.rebinds)
	}
	// Owner change with capacity rejection but cross-layer: metric-only closure,
	// no rebind.
	fn = newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "b", true, false, false, false, scope, AuditCapture{})
	if fn == nil {
		t.Fatal("cross-layer switch must still carry a settlement closure")
	}
	fn()
	if len(port.rebinds) != 1 {
		t.Fatalf("cross-layer owner change must not rebind: %+v", port.rebinds)
	}
	// Owner change with capacity rejection + same layer: rebind (reason capacity).
	fn = newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "b", true, false, false, true, scope, AuditCapture{})
	if fn == nil {
		t.Fatal("same-layer capacity failover must produce a rebind closure")
	}
	fn()
	last := port.rebinds[len(port.rebinds)-1]
	if last.sessionAffinityKey != "aff" || last.expectedOwner != "a" || last.newOwner != "b" || last.scope != scope {
		t.Fatalf("rebind call = %+v", last)
	}
	// Owner soft-busy（普通分组软阈值让位）+ same layer: rebind，reason
	// = soft_threshold。
	capture := &rebindMetadataAudit{}
	fn = newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "c", false, true, false, true, scope, AuditCapture{Context: capture})
	if fn == nil {
		t.Fatal("same-layer soft-busy failover must produce a rebind closure")
	}
	fn()
	last = port.rebinds[len(port.rebinds)-1]
	if last.expectedOwner != "a" || last.newOwner != "c" {
		t.Fatalf("soft-busy rebind call = %+v", last)
	}
	if capture.reason != "soft_threshold" || !capture.applied || !capture.sameLayer {
		t.Fatalf("soft-busy rebind metadata = %+v", capture.metadata)
	}
	// Slot fallback（准备期名额回退到其他层账户）：允许跨层重绑，reason =
	// slot_fallback。
	capture = &rebindMetadataAudit{}
	fn = newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "a", "d", false, false, true, false, scope, AuditCapture{Context: capture})
	if fn == nil {
		t.Fatal("slot fallback must produce a rebind closure")
	}
	fn()
	last = port.rebinds[len(port.rebinds)-1]
	if last.expectedOwner != "a" || last.newOwner != "d" {
		t.Fatalf("slot-fallback rebind call = %+v", last)
	}
	if capture.reason != "slot_fallback" || !capture.applied || capture.sameLayer {
		t.Fatalf("slot-fallback rebind metadata = %+v", capture.metadata)
	}
	// No owner: nil.
	if fn := newConfirmSessionAffinityRebindFunc(port, ctx, "aff", "", "b", true, false, false, true, scope, AuditCapture{}); fn != nil {
		t.Fatal("empty owner must be nil")
	}
}

// rebindMetadataAudit 捕获 session_affinity_rebind 元数据（reason 断言载体）。
type rebindMetadataAudit struct {
	metadata  map[string]any
	reason    string
	applied   bool
	sameLayer bool
}

func (a *rebindMetadataAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (a *rebindMetadataAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	if label != "session_affinity_rebind" {
		return
	}
	a.metadata = metadata
	if reason, ok := metadata["reason"].(string); ok {
		a.reason = reason
	}
	if applied, ok := metadata["applied"].(bool); ok {
		a.applied = applied
	}
	if sameLayer, ok := metadata["sameLayer"].(bool); ok {
		a.sameLayer = sameLayer
	}
}

func (a *rebindMetadataAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func TestDispatchAffinityOwnerPreparedBusyRebinds(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	// Preparation moved the hard-busy owner behind a free same-layer peer; the
	// request succeeds on the peer without ever attempting the owner. The
	// preparation busy flag must still drive the final rebind.
	args := fastDispatchArgs(t, req, testAccounts("a-2", "a-1"))
	args.SessionAffinityKey = "aff-prep-busy"
	args.SessionAffinityOwnerAccountID = "a-1"
	args.SessionAffinityOwnerBusy = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s, want a-2", result.Account.ID)
	}
	if result.SessionAffinityOwnerAccountID != "a-1" || !result.SessionAffinityOwnerCapacityRejected || !result.SessionAffinitySameLayer {
		t.Fatalf("owner settlement = %q rejected=%v sameLayer=%v", result.SessionAffinityOwnerAccountID, result.SessionAffinityOwnerCapacityRejected, result.SessionAffinitySameLayer)
	}
	if result.ConfirmSessionAffinityRebind == nil {
		t.Fatal("prepared-busy owner failover must carry a rebind closure")
	}
	result.ConfirmSessionAffinityRebind()
	affinity := engine.Affinity.(*fakeAffinity)
	if len(affinity.rebinds) != 1 {
		t.Fatalf("rebinds = %+v", affinity.rebinds)
	}
	call := affinity.rebinds[0]
	if call.sessionAffinityKey != "aff-prep-busy" || call.expectedOwner != "a-1" || call.newOwner != "a-2" {
		t.Fatalf("rebind call = %+v", call)
	}
}

func TestDispatchAffinityOwnerCapacityRejectedRebinds(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Concurrency = &rpmFullStore{rpmFullAccountID: "a-1", rpmResetAtMs: gatewayupstream.NowMs() + 60_000}
	engine.Config.AccountConcurrencyRetryBudgetMs = 0
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.SessionAffinityKey = "aff-latch-engine"
	args.SessionAffinityOwnerAccountID = "a-1"
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s, want a-2", result.Account.ID)
	}
	if result.SessionAffinityOwnerAccountID != "a-1" || !result.SessionAffinityOwnerCapacityRejected || !result.SessionAffinitySameLayer {
		t.Fatalf("owner settlement = %q rejected=%v sameLayer=%v", result.SessionAffinityOwnerAccountID, result.SessionAffinityOwnerCapacityRejected, result.SessionAffinitySameLayer)
	}
	if result.ConfirmSessionAffinityRebind == nil {
		t.Fatal("same-layer capacity failover must carry a rebind closure")
	}
	result.ConfirmSessionAffinityRebind()
	affinity := engine.Affinity.(*fakeAffinity)
	if len(affinity.rebinds) != 1 {
		t.Fatalf("rebinds = %+v", affinity.rebinds)
	}
	call := affinity.rebinds[0]
	if call.sessionAffinityKey != "aff-latch-engine" || call.expectedOwner != "a-1" || call.newOwner != "a-2" {
		t.Fatalf("rebind call = %+v", call)
	}
}
