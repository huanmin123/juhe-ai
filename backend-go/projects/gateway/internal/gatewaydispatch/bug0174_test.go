package gatewaydispatch

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// BUG-0174 M-2/M-3/M-9 regression tests. The behavior baseline is the Node
// archive upstream-dispatch.ts (migration-backup/node/final-archive).

// multiKeyTestAccount builds a pool-isolation account (two distinct keys, a
// supported provider) so its selected key fingerprint is non-nil and the
// key-model busy/blocked exclusion logic applies like it does in Node.
func multiKeyTestAccount(id string, keys ...string) AccountCandidate {
	account := testAccounts(id)[0]
	account.ProviderCode = "openai"
	account.ProtocolCode = "openai"
	account.ProtocolVersion = "v1"
	account.APIKeys = keys
	return account
}

// ---------------------------------------------------------------------------
// M-2: a single-account terminal skip must not abandon the remaining
// candidates (Node for-of at upstream-dispatch.ts:619 continues with the next
// account; there is no skip-rest-of-cycle).
// ---------------------------------------------------------------------------

// TestFetchFirstAvailableUpstreamContinueAfterAccountLevelSkip: the first
// account has no upstream URL (single-account skip terminal, Node :894) and
// the second account must still serve the request. Before the fix the whole
// group was abandoned (skipRestOfCycle) and the dispatch failed.
func TestFetchFirstAvailableUpstreamContinueAfterAccountLevelSkip(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {}, // no upstream URL -> single-account skip
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, testAccounts("a-1", "a-2")))
	if err != nil {
		t.Fatalf("the good account must still serve after the bad account skipped: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("expected the good account to serve, got %s", result.Account.ID)
	}
}

// TestFetchFirstAvailableUpstreamContinueAfterKeyPoolSkip: the first account's
// key pool is unavailable (a blocked key-model admission excludes its key,
// Node :967/:1180-1181) and the cycle must continue with the second account.
func TestFetchFirstAvailableUpstreamContinueAfterKeyPoolSkip(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	engine, driver, _ := newTestEngine(t)
	keyModel := &fakeKeyModelAdmission{statuses: map[string][]gatewayaccounteffects.AttemptPreparationStatus{
		"a-1": {gatewayaccounteffects.AttemptPreparationBlocked},
	}}
	engine.KeyModel = keyModel
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{
		multiKeyTestAccount("a-1", "key-a", "key-b"),
		testAccounts("a-2")[0],
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("the cycle must continue after the key-pool skip: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("expected a-2 to serve, got %s", result.Account.ID)
	}
	// Both keys of the pool get one blocked admission each (blocked excludes
	// the current key and the rotation advances to the sibling, M-3 cursor),
	// then the pool is unavailable and the cycle moves on.
	if got := keyModel.calls("a-1"); got != 2 {
		t.Fatalf("blocked must rotate through the pool before the skip, calls = %d", got)
	}
}

// ---------------------------------------------------------------------------
// M-3: key-model busy/blocked rotation alignment (upstream-dispatch.ts:856-875
// and 1183-1242).
// ---------------------------------------------------------------------------

// fakeKeyModelAdmission answers per-account status sequences: the n-th call
// returns statuses[n-1], the last one repeats.
type fakeKeyModelAdmission struct {
	mu       sync.Mutex
	statuses map[string][]gatewayaccounteffects.AttemptPreparationStatus
	callsBy  map[string]int
}

func (f *fakeKeyModelAdmission) calls(accountID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callsBy[accountID]
}

func (f *fakeKeyModelAdmission) Prepare(_ context.Context, _ gatewayaccounteffects.KeyModelRuntimeStore, input gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput) (gatewayaccounteffects.GatewayKeyModelAttemptPreparation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callsBy == nil {
		f.callsBy = map[string]int{}
	}
	accountID := input.Route.AccountID
	f.callsBy[accountID]++
	seq := f.statuses[accountID]
	index := f.callsBy[accountID] - 1
	if index >= len(seq) {
		index = len(seq) - 1
	}
	status := gatewayaccounteffects.AttemptPreparationAdmitted
	if len(seq) > 0 {
		status = seq[index]
	}
	return gatewayaccounteffects.GatewayKeyModelAttemptPreparation{Status: status}, nil
}

// TestKeyModelBusyKeepsKeyAndReacquiresConcurrencySlot: inside the foreground
// wait window a busy admission keeps the key eligible (upstream-dispatch.ts:
// 1183-1191), rotates through the do/while and re-acquires the released
// concurrency slot for the next key attempt (:856-875, :1198-1199).
func TestKeyModelBusyKeepsKeyAndReacquiresConcurrencySlot(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	engine, driver, _ := newTestEngine(t)
	engine.Config.KeyModelForegroundQueuePollMs = 5
	engine.KeyModel = &fakeKeyModelAdmission{statuses: map[string][]gatewayaccounteffects.AttemptPreparationStatus{
		"a-1": {gatewayaccounteffects.AttemptPreparationBusy, gatewayaccounteffects.AttemptPreparationAdmitted},
	}}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, []AccountCandidate{
		multiKeyTestAccount("a-1", "key-a", "key-b"),
	}))
	if err != nil {
		t.Fatalf("busy within the wait window must keep the pool and succeed: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if got := engine.KeyModel.(*fakeKeyModelAdmission).calls("a-1"); got != 2 {
		t.Fatalf("the pool must be retried after busy, prepare calls = %d", got)
	}
	concurrency := engine.Concurrency.(*fakeConcurrencyStore)
	if concurrency.acquired.Load() != 2 {
		t.Fatalf("the slot must be re-acquired after the busy release, acquires = %d", concurrency.acquired.Load())
	}
	if concurrency.released.Load() != 1 {
		t.Fatalf("exactly one busy release expected, releases = %d", concurrency.released.Load())
	}
}

// TestKeyModelBusyWindowExpiryDropsKey: once the foreground window is
// exhausted the busy key is dropped from the candidates (upstream-dispatch.ts:
// 1192-1193), the account ends as key-pool-unavailable and the cycle continues
// with the next account.
func TestKeyModelBusyWindowExpiryDropsKey(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	engine, driver, _ := newTestEngine(t)
	engine.Config.KeyModelForegroundQueueWaitMs = 30
	engine.Config.KeyModelForegroundQueuePollMs = 10
	keyModel := &fakeKeyModelAdmission{statuses: map[string][]gatewayaccounteffects.AttemptPreparationStatus{
		"a-1": {gatewayaccounteffects.AttemptPreparationBusy},
	}}
	engine.KeyModel = keyModel
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{
		multiKeyTestAccount("a-1", "key-a", "key-b"),
		testAccounts("a-2")[0],
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("the next account must serve after the busy window expired: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("expected a-2 to serve, got %s", result.Account.ID)
	}
	// The window kept the pool eligible for several busy rounds before the
	// expiry dropped it; the old always-delete behavior would have called
	// prepare exactly once.
	if got := keyModel.calls("a-1"); got < 2 {
		t.Fatalf("busy must retry the pool within the window before expiry, calls = %d", got)
	}
}

// ---------------------------------------------------------------------------
// M-9: runtime keys follow the Node contract (runtime/account-runtime-keys.ts
// via gatewaycircuit): bare accounts key as id, authorized bindings key as
// id:authorized:system:group:authz; different bindings of the same account
// are distinct attempt identities.
// ---------------------------------------------------------------------------

func authorizedTestAccount(id, systemID, groupID, authorizationID string) AccountCandidate {
	account := testAccounts(id)[0]
	account.AccountAccessType = "account_authorized"
	account.BindingSystemAccountID = &systemID
	account.BoundGroupID = &groupID
	account.AccountAuthorizationID = &authorizationID
	return account
}

func TestGatewayAccountRuntimeKeyNodeContract(t *testing.T) {
	plain := testAccounts("a-1")[0]
	plainKey, err := gatewayAccountRuntimeKey(plain)
	if err != nil {
		t.Fatalf("plain account key: %v", err)
	}
	if plainKey != "a-1" {
		t.Fatalf("plain key = %q, want a-1", plainKey)
	}

	bound := authorizedTestAccount("a-1", "sys-1", "grp-1", "authz-1")
	boundKey, err := gatewayAccountRuntimeKey(bound)
	if err != nil {
		t.Fatalf("authorized account key: %v", err)
	}
	if boundKey != "a-1:authorized:sys-1:grp-1:authz-1" {
		t.Fatalf("authorized key = %q", boundKey)
	}

	// The dispatch key must equal the gatewaycircuit confirmation contract so
	// AccountCircuitConfirmation.AccountRuntimeKey comparisons match.
	circuitKey, err := gatewaycircuit.GatewayAccountRuntimeKey(gatewaycircuit.SuppressibleGatewayAccount{
		ID:                     "a-1",
		AccountAccessType:      "account_authorized",
		BindingSystemAccountID: "sys-1",
		BoundGroupID:           "grp-1",
		AccountAuthorizationID: "authz-1",
	})
	if err != nil {
		t.Fatalf("circuit key: %v", err)
	}
	if boundKey != circuitKey {
		t.Fatalf("dispatch key %q must equal the circuit contract key %q", boundKey, circuitKey)
	}

	// Missing binding context surfaces the Node error.
	empty := ""
	broken := authorizedTestAccount("a-1", "sys-1", "grp-1", "authz-1")
	broken.AccountAuthorizationID = &empty
	if _, keyErr := gatewayAccountRuntimeKey(broken); keyErr == nil {
		t.Fatal("authorized account without authorization id must fail")
	} else if !strings.Contains(keyErr.Error(), "授权账户运行态键缺少绑定上下文") {
		t.Fatalf("error = %v", keyErr)
	}
}

func TestAuthorizedBindingAttemptIdentityDistinctPerBinding(t *testing.T) {
	// M-9 identity semantics: two authorized bindings of the same account are
	// distinct runtime keys. Each binding gets its own attempt on a fresh
	// request (distinct trackers mirror distinct requests); inside one request
	// the physical-credential guard still forbids a second runtime identity of
	// the same physical credential (Node route-coordination.ts:446-488, kept
	// as-is by this fix).
	physicalCredentialKey := "a-1"
	protocolModelKey := "protocol-model"
	recordInput := func(runtimeKey string) gatewayrouting.GatewayDispatchAttemptRecordInput {
		return gatewayrouting.GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: gatewayrouting.GatewayDispatchAttemptIdentity{
				AccountRuntimeKey:     runtimeKey,
				PhysicalCredentialKey: physicalCredentialKey,
				ProtocolModelKey:      protocolModelKey,
			},
		}
	}
	binding1 := "a-1:authorized:sys-1:grp-1:authz-1"
	binding2 := "a-1:authorized:sys-1:grp-1:authz-2"
	if binding1 == binding2 {
		t.Fatal("distinct authorization ids must produce distinct runtime keys")
	}

	// A fresh request per binding: each binding is admitted once.
	for _, binding := range []string{binding1, binding2} {
		tracker := newTestCoordination(t).RequestAttemptTracker
		first, err := tracker.TryRecordDispatchAttempt(recordInput(binding))
		if err != nil || !first.Allowed {
			t.Fatalf("binding %s must be admitted on its own request: %+v %v", binding, first, err)
		}
	}

	// Same request: the physical credential guard rejects the second binding,
	// identical to Node.
	tracker := newTestCoordination(t).RequestAttemptTracker
	first, err := tracker.TryRecordDispatchAttempt(recordInput(binding1))
	if err != nil || !first.Allowed {
		t.Fatalf("first binding must be allowed: %+v %v", first, err)
	}
	second, err := tracker.TryRecordDispatchAttempt(recordInput(binding2))
	if err != nil || second.Allowed || second.Reason != "physical_credential_already_attempted" {
		t.Fatalf("the physical-credential guard must keep Node semantics: %+v %v", second, err)
	}
}

// The confirmation filter uses the Node-contract key end to end.
func TestAccountCircuitConfirmationFilterUsesNodeContractKey(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	boundKey, err := gatewayAccountRuntimeKey(authorizedTestAccount("a-2", "sys-2", "grp-2", "authz-2"))
	if err != nil {
		t.Fatalf("runtime key: %v", err)
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, []AccountCandidate{
		testAccounts("a-1")[0],
		authorizedTestAccount("a-2", "sys-2", "grp-2", "authz-2"),
	})
	args.AccountCircuitConfirmation = &gatewaycircuit.Confirmation{AccountRuntimeKey: boundKey}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("the confirmation must retain exactly the bound account, got %s", result.Account.ID)
	}
}
