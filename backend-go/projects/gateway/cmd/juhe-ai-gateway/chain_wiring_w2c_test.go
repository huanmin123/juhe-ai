package main

// W2-C composition-root wiring assertions (BUG-0175): each port repaired in
// this slice gets a wiring-level test — the injected chain mounts the real
// collaborator, the uninjected chain keeps the explicit degraded/disabled
// fallback, and the library-side gaps closed alongside the wiring hold their
// archive semantics.

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// readSource loads a composition-root source file for the text-level wiring
// assertions (gofmt-stable needles only).
func readSource(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(source)
}

func strPtrOf(value string) *string { return &value }
func int64PtrOf(value int64) *int64 { return &value }
func intPtrOf(value int) *int       { return &value }

// ---------------------------------------------------------------------------
// D-110: dynamic route group-binding orderer
// ---------------------------------------------------------------------------

func TestChainGroupBindingOrdererRotatesRoundRobinBindings(t *testing.T) {
	orderer := newChainGroupBindingOrderer(gatewayrouting.NewAPIKeyGroupRouteSelector("", nil, ""))
	apiKey := gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key",
		RouteStrategyID:   "strategy",
		RouteStrategyMode: gatewayruntimecache.RouteStrategyModeRoundRobin,
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "b1", GroupID: "g1", Status: "active", Weight: 1, GroupEnabled: 1},
			{ID: "b2", GroupID: "g2", Status: "active", Weight: 1, GroupEnabled: 1},
			{ID: "b3", GroupID: "g3", Status: "active", Weight: 1, GroupEnabled: 1},
		},
	}
	wantFirst := []string{"g1", "g2", "g3", "g1"}
	for _, want := range wantFirst {
		ordered, err := orderer.OrderAPIKeyGroupBindings(context.Background(), apiKey)
		if err != nil {
			t.Fatalf("OrderAPIKeyGroupBindings: %v", err)
		}
		if len(ordered) != 3 {
			t.Fatalf("ordered = %d bindings, want 3", len(ordered))
		}
		if ordered[0].GroupID != want {
			t.Fatalf("rotation head = %s, want %s", ordered[0].GroupID, want)
		}
		// The projected rows return as the stored rows (priority/weight kept).
		if ordered[0].ID == "" || ordered[0].Weight != 1 {
			t.Fatalf("ordered row lost stored fields: %+v", ordered[0])
		}
	}
}

// The composition root must mount the orderer onto the runtime cache and the
// selector must share the runtime-state driver axis (source-level assertion
// over compose.go / chain_runtime.go, gofmt-stable needles).
func TestComposeWiresDynamicRouteOrderer(t *testing.T) {
	runtime := readSource(t, "chain_runtime.go")
	for _, needle := range []string{
		"gatewayrouting.NewAPIKeyGroupRouteSelector(cfg.RuntimeStateDriver",
		"Orderer: newChainGroupBindingOrderer(routeSelector)",
	} {
		if !strings.Contains(runtime, needle) {
			t.Fatalf("chain_runtime.go must wire the dynamic route orderer: %s", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// D-109: client-IP concurrency slots + handler-exit release list
// ---------------------------------------------------------------------------

func TestChainClientIPConcurrencySlotLifecycle(t *testing.T) {
	slots, err := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{})
	if err != nil {
		t.Fatalf("create slots: %v", err)
	}
	defer slots.Close()
	port := newChainClientIPConcurrency(slots)
	policy := gatewayruntimecache.GroupSchedulingPolicy{"clientIpConcurrencyLimit": 1, "clientIpConcurrencyOverflowMode": "reject"}
	input := gatewaydispatch.ClientIPConcurrencyInput{
		SystemAccountID: "sys",
		GroupID:         "grp",
		APIKeyID:        "key",
		ClientIP:        "10.0.0.8",
		Policy:          &policy,
	}
	first, err := port.Acquire(context.Background(), input)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !first.Acquired || first.Release == nil || first.Limit != 1 {
		t.Fatalf("first acquire = %+v, want an acquired slot with release", first)
	}
	second, err := port.Acquire(context.Background(), input)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second.Acquired {
		t.Fatal("second acquire must be rejected at limit 1")
	}
	first.Release()
	third, err := port.Acquire(context.Background(), input)
	if err != nil {
		t.Fatalf("post-release acquire: %v", err)
	}
	if !third.Acquired {
		t.Fatal("post-release acquire must succeed (slot released, no TTL leak)")
	}
	third.Release()
}

func TestClientIPSlotReleaseListReleasesEveryContextOnce(t *testing.T) {
	var calls []string
	list := &clientIPSlotReleaseList{}
	list.Add(nil) // nil releases are skipped
	list.Add(func() { calls = append(calls, "ctx1") })
	list.Add(func() { calls = append(calls, "ctx2") })
	list.ReleaseAll()
	list.ReleaseAll() // idempotent at the list level; each release ran once
	if strings.Join(calls, ",") != "ctx1,ctx2" {
		t.Fatalf("calls = %v, want each context release once in order", calls)
	}
}

func TestComposeWiresClientIPConcurrency(t *testing.T) {
	deps := chainSmokeDeps(t, newChainFixture(t), gatewaypreauth.SystemClock{}, "")
	slots, err := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{})
	if err != nil {
		t.Fatalf("create slots: %v", err)
	}
	t.Cleanup(slots.Close)
	deps.ClientIPSlots = newChainClientIPConcurrency(slots)
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	if chain.engine.ClientIPConcurrency == nil {
		t.Fatal("engine.ClientIPConcurrency must be wired for high_concurrency groups")
	}
}

// ---------------------------------------------------------------------------
// D-131: account circuits (scope isolation + composition)
// ---------------------------------------------------------------------------

func TestGatewayAccountProtocolModelScopeIsolatesAuthorizedBindings(t *testing.T) {
	owner := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc",
		ProviderProtocolProfileID: "profile",
	}
	ownerScope, err := gatewaycircuit.GatewayAccountProtocolModelScope(owner, "text", strPtrOf("gpt-4o"))
	if err != nil {
		t.Fatalf("owner scope: %v", err)
	}
	if ownerScope.AccountRuntimeKey != "acc" {
		t.Fatalf("owner runtime key = %q, want the bare id", ownerScope.AccountRuntimeKey)
	}

	authorized := owner
	authorized.AccountAccessType = gatewayruntimecache.AccountAccessTypeAccountAuthorized
	authorized.BindingSystemAccountID = strPtrOf("sys")
	authorized.BoundGroupID = strPtrOf("grp")
	authorized.AccountAuthorizationID = strPtrOf("authz-1")
	first, err := gatewaycircuit.GatewayAccountProtocolModelScope(authorized, "text", nil)
	if err != nil {
		t.Fatalf("authorized scope: %v", err)
	}
	if first.AccountRuntimeKey != "acc:authorized:sys:grp:authz-1" {
		t.Fatalf("authorized runtime key = %q, want the binding-scoped key", first.AccountRuntimeKey)
	}
	authorized.AccountAuthorizationID = strPtrOf("authz-2")
	second, err := gatewaycircuit.GatewayAccountProtocolModelScope(authorized, "text", nil)
	if err != nil {
		t.Fatalf("second binding scope: %v", err)
	}
	if second.AccountRuntimeKey == first.AccountRuntimeKey {
		t.Fatal("different authorized bindings must not share one circuit scope")
	}

	// An authorized account without its binding context is an error (the
	// Node missing-context contract), never a silent bare-id scope.
	broken := owner
	broken.AccountAccessType = gatewayruntimecache.AccountAccessTypeAccountAuthorized
	if _, err := gatewaycircuit.GatewayAccountProtocolModelScope(broken, "text", nil); err == nil {
		t.Fatal("authorized account without binding context must error")
	}
}

func TestComposeWiresAccountCircuits(t *testing.T) {
	fixture := newChainFixture(t)
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	circuits, closeCircuits, err := newChainAccountCircuitService("memory", "", "")
	if err != nil {
		t.Fatalf("create circuit service: %v", err)
	}
	t.Cleanup(closeCircuits)
	deps.AccountCircuits = circuits
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	if chain.engine.Circuits != circuits {
		t.Fatalf("engine.Circuits = %T, want the injected circuit service", chain.engine.Circuits)
	}
	// SUSPECT 链路可触发：通过注入的服务走一次 PrepareAttempt（closed 状态
	// 放行），证明组合根的服务在引擎调用面上真实可用。
	result, err := circuits.PrepareAttempt(context.Background(), gatewaycircuit.PrepareAttemptInput{
		Account:                     gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProviderProtocolProfileID: "profile"},
		RequestLane:                 gatewaycircuit.LaneText,
		Model:                       strPtrOf("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != gatewaycircuit.PrepareDispatchable {
		t.Fatalf("PrepareAttempt outcome = %v, want dispatchable", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// D-133: key-model capability resolution + permit-lost consumption
// ---------------------------------------------------------------------------

func TestResolveGatewayKeyModelAttemptCapabilityAndMainProbe(t *testing.T) {
	request := newW2CTestRequest(t, `{"model":"gpt-4o","stream":true}`)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc",
		DispatchRevision:          int64PtrOf(1),
		SelectedAPIKeyFingerprint: strPtrOf("fp-1"),
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
	}
	route := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account)
	if route == nil {
		t.Fatal("capability must resolve for a complete route")
	}
	if route.AccountID != "acc" || route.IsMainProbe {
		t.Fatalf("route = %+v, want the account id and non-probe default", route)
	}
	capability := route.Capability
	if capability.KeyFingerprint != "fp-1" ||
		capability.ClientModel != "gpt-4o" ||
		capability.ClientEndpointFamily != "chat_completions" ||
		capability.UpstreamEndpointMode != "chat_sse" ||
		capability.CredentialSourceAccountID != "acc" ||
		capability.DispatchRevision != 1 {
		t.Fatalf("capability = %+v, want the archive projection", capability)
	}

	// Main probe triple alignment: model + endpoint mode + resolved capability.
	account.HealthCheckModel = "gpt-4o"
	account.HealthCheckEndpointMode = "chat_sse"
	probe := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account)
	if probe == nil || !probe.IsMainProbe {
		t.Fatalf("probe route = %+v, want main-probe alignment", probe)
	}
	account.HealthCheckEndpointMode = "chat_json"
	nonProbe := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account)
	if nonProbe == nil || nonProbe.IsMainProbe {
		t.Fatalf("mismatched probe mode = %+v, want non-probe", nonProbe)
	}

	// A missing key fingerprint stays disabled (Node undefined → disabled).
	account.SelectedAPIKeyFingerprint = nil
	if route := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account); route != nil {
		t.Fatalf("capability without fingerprint = %+v, want nil", route)
	}
}

func TestMergePermitLostSignalCancelsOnLostPermit(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	lost := make(chan struct{})
	merged := gatewaydispatch.MergePermitLostSignal(parent, lost)
	select {
	case <-merged.Done():
		t.Fatal("merged signal must stay live before the permit is lost")
	default:
	}
	close(lost)
	select {
	case <-merged.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("losing the foreground permit must cancel the transport signal")
	}
}

// ---------------------------------------------------------------------------
// D-134: suppression filter + recoverable wait
// ---------------------------------------------------------------------------

func TestChainSuppressionPortFiltersAndResolves(t *testing.T) {
	store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})
	port := chainSuppressionPort{
		store:  store,
		waiter: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil),
	}
	accounts := []gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}}

	result, err := port.FilterAsync(context.Background(), accounts, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		t.Fatalf("FilterAsync: %v", err)
	}
	if len(result.Accounts) != 2 || result.AllSuppressed {
		t.Fatalf("clean filter = %+v, want both accounts dispatchable", result)
	}

	// Suppress account a (runtime key = bare id for the owner account).
	store.Suppress("a", 60_000, "test failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	result, err = port.FilterAsync(context.Background(), accounts, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		t.Fatalf("filtered FilterAsync: %v", err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "b" {
		t.Fatalf("filtered accounts = %+v, want only b", result.Accounts)
	}
	if result.SuppressedCount != 1 || len(result.SuppressedAccountIDs) != 1 || result.SuppressedAccountIDs[0] != "a" {
		t.Fatalf("suppression metadata = %+v, want account a suppressed", result)
	}

	// Partial suppression resolves without completing the request.
	resolved, completed, err := port.ResolveLocalSuppressionFilter(context.Background(), gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts,
		GroupID:  "grp",
	})
	if err != nil || completed || resolved == nil {
		t.Fatalf("partial resolve = (%+v, %v, %v), want a filter without completion", resolved, completed, err)
	}

	// An aborted signal with every account suppressed completes silently
	// (the recoverable wait window exits aborted, then the resolver settles
	// the request instead of rendering the 503 contract).
	store.Suppress("b", 60_000, "test failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	abortedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	resolved, completed, err = port.ResolveLocalSuppressionFilter(abortedCtx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts:          accounts,
		GroupID:           "grp",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
		Signal:            abortedCtx,
	})
	if err != nil {
		t.Fatalf("aborted resolve error: %v", err)
	}
	if !completed || resolved != nil {
		t.Fatalf("aborted resolve = (%+v, %v), want completed with no result", resolved, completed)
	}
}

func TestChainSuppressionLeaseCompletesAndReleases(t *testing.T) {
	store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})
	account := gatewaycircuit.SuppressibleAccount{SuppressibleGatewayAccount: gatewaycircuit.SuppressibleGatewayAccount{ID: "acc"}}
	store.Suppress("acc", 60_000, "failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	// Age the suppression into its half-open window by suppressing a lease
	// through the store's own acquire path: filter with lease acquisition.
	result := store.FilterSuppressions([]gatewaycircuit.SuppressibleAccount{account}, nil, gatewaycircuit.SuppressionFilterOptions{AcquireHalfOpenLease: true})
	_ = result
	// Direct lease shape check via the adapter: a store lease bridges onto
	// the dispatch interface and releases through the store.
	lease := &chainSuppressionLease{lease: gatewaycircuit.HalfOpenLease{
		RuntimeKey: "acc",
		AccountID:  "acc",
		LeaseID:    "lease-1",
		Release:    func() bool { return store.ReleaseHalfOpenLease("acc", "acc", "lease-1") },
	}}
	if lease.RuntimeKey() != "acc" {
		t.Fatalf("lease runtime key = %q", lease.RuntimeKey())
	}
	if released, err := lease.Release(); err != nil || released {
		t.Fatalf("unknown lease release = (%v, %v), want a lost lease", released, err)
	}
}

func TestChainDispatchSuppressionWaiterReturnsRefreshedState(t *testing.T) {
	waiter := chainDispatchSuppressionWaiter{wait: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil)}
	state, err := waiter.WaitForState(context.Background(), gatewaydispatch.SuppressionWaitInput{
		ScopeKey:  "sys::grp",
		Reason:    gatewaycircuit.LocalAccountSuppressionWaitReason,
		MaxWaitMs: 50,
		Refresh: func(ctx context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
			return gatewaydispatch.SuppressionFilterResult{Accounts: []gatewaydispatch.AccountCandidate{{ID: "a"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("WaitForState: %v", err)
	}
	if len(state.Accounts) != 1 || state.Accounts[0].ID != "a" {
		t.Fatalf("wait state = %+v, want the refreshed sample", state)
	}
}

// ---------------------------------------------------------------------------
// D-136: proxy health ordering + failure records
// ---------------------------------------------------------------------------

func TestChainProxyHealthPortRecordsFailureAndAvoidsBucket(t *testing.T) {
	service := gatewayproxyhealth.NewProxyHealthService(nil, gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), gatewayproxyhealth.ProxyHealthOptions{}, nil)
	port := chainProxyHealthPort{service: service}
	// The port records proxy-scope failures (RecordGatewayProxyFailureAsync),
	// so the bucket keys off the shared proxy URL; the avoid applies once two
	// distinct accounts failed.
	shared := "https://proxy.internal:8080"
	a1 := gatewaydispatch.AccountCandidate{ID: "a1", SystemAccountID: "sys", ProviderCode: "openai", BaseURL: "https://api.example.com", ProxyURL: strPtrOf(shared)}
	a2 := gatewaydispatch.AccountCandidate{ID: "a2", SystemAccountID: "sys", ProviderCode: "openai", BaseURL: "https://api.example.com", ProxyURL: strPtrOf(shared)}
	if err := port.RecordFailureAsync(context.Background(), a1, "connect failed"); err != nil {
		t.Fatalf("RecordFailureAsync: %v", err)
	}
	if err := port.RecordFailureAsync(context.Background(), a2, "connect failed"); err != nil {
		t.Fatalf("RecordFailureAsync: %v", err)
	}
	order, err := port.OrderAsync(context.Background(), []gatewaydispatch.AccountCandidate{a1, a2}, nil)
	if err != nil {
		t.Fatalf("OrderAsync: %v", err)
	}
	// Two accounts in one suspected proxy bucket land in AvoidedAccountIDs
	// (with only avoided candidates the merge keeps the order and reports
	// the bypass instead of a forced reorder).
	if len(order.AvoidedAccountIDs) != 2 {
		t.Fatalf("applied=%v avoided=%v, want the shared bucket avoided", order.Applied, order.AvoidedAccountIDs)
	}
}

// ---------------------------------------------------------------------------
// D-137: hot quality ordering + attempt lifecycle factory
// ---------------------------------------------------------------------------

func TestChainHotQualityPortOrdersAndDrivesLifecycle(t *testing.T) {
	gatewayhotquality.ResetGatewayHotQualityRuntimeForTest()
	t.Cleanup(gatewayhotquality.ResetGatewayHotQualityRuntimeForTest)
	runtime, err := gatewayhotquality.GetGatewayHotQualityRuntime(context.Background(), gatewayhotquality.RuntimeDriverConfig{
		RuntimeMode:        "standalone",
		RuntimeStateDriver: "memory",
	})
	if err != nil {
		t.Fatalf("create hot quality runtime: %v", err)
	}
	port := chainHotQualityPort{runtime: runtime}
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "a1", ProviderProtocolProfileID: "profile", Priority: 1},
		{ID: "a2", ProviderProtocolProfileID: "profile", Priority: 2},
	}
	order, err := port.OrderAsync(context.Background(), gatewaydispatch.HotQualityOrderInput{
		Accounts:        accounts,
		Mode:            gatewaydispatch.HotQualityModeCostFirst,
		SystemAccountID: "sys",
		GroupID:         "grp",
		RequestLane:     "text",
		Model:           "gpt-4o",
		RequestID:       "req-1",
	})
	if err != nil {
		t.Fatalf("OrderAsync: %v", err)
	}
	if len(order.Accounts) != 2 {
		t.Fatalf("ordered accounts = %d, want both candidates", len(order.Accounts))
	}
	if order.DispatchIntent == "" {
		t.Fatal("ordered intent must be reported")
	}

	factory := newChainHotQualityLifecycleFactory(runtime)
	lifecycle := factory(gatewaydispatch.HotQualityLifecycleInput{
		AttemptID:       "att-1",
		AccountID:       "a1",
		RequestLane:     "text",
		Model:           "gpt-4o",
		ProtocolProfile: "profile",
	})
	if lifecycle == nil {
		t.Fatal("the lifecycle factory must mount the G12 lifecycle for a complete input")
	}
	lifecycle.RecordTerminal(context.Background(), gatewaydispatch.HotQualityTerminal{OutcomeClass: gatewaydispatch.HotQualityOutcomeUnknown})

	// An incomplete input keeps the engine's neutral no-op (nil product).
	if factory(gatewaydispatch.HotQualityLifecycleInput{AccountID: "a1"}) != nil {
		t.Fatal("an input without attempt id must fall back to the no-op lifecycle")
	}
}

// ---------------------------------------------------------------------------
// D-129: composition-root DB service fallback (process-local)
// ---------------------------------------------------------------------------

func TestChainQuotaDBServiceBridgesInProcessReads(t *testing.T) {
	runtime := readSource(t, "chain_runtime.go")
	needle := "DBService: newChainQuotaDBService(apiKeyQuota, authzQuota)"
	if !strings.Contains(runtime, needle) {
		t.Fatalf("chain_runtime.go must inject the process-local quota db-service fallback: %s", needle)
	}
	// The compile-time port satisfaction rides chain_wiring_w2c.go.
	wiring := readSource(t, "chain_wiring_w2c.go")
	if !strings.Contains(wiring, "_ gatewayquota.DBServiceClient") {
		t.Fatal("chainQuotaDBService must assert the gatewayquota.DBServiceClient port")
	}
}

// ---------------------------------------------------------------------------
// compose root text: the W2-C ports ride the production assembly
// ---------------------------------------------------------------------------

func TestComposeWiresW2CChainPorts(t *testing.T) {
	compose := readSource(t, "compose.go")
	for _, needle := range []string{
		"newChainClientIPConcurrency(chainServices.ClientIPSlots)",
		"chainServices.AccountCircuits",
		"chainServices.KeyModelStore",
		"chainProxyHealthPort{service: chainServices.ProxyHealth}",
		"chainHotQualityPort{runtime: chainServices.HotQuality}",
		"newChainHotQualityLifecycleFactory(chainServices.HotQuality)",
		"store:  chainServices.SuppressionStore",
	} {
		if !strings.Contains(compose, needle) {
			t.Fatalf("compose.go must wire the W2-C chain port: %s", needle)
		}
	}
}

// 追加接线（W2-B → W2-C compose 移交）：M11 实例归还路由的终态授权归还写
// 必须接到 authz Store（生产可达 + 自动继承写后失效扇出），且注入发生在
// account store 构造之后。
func TestComposeWiresAuthorizationGrantReturner(t *testing.T) {
	compose := readSource(t, "compose.go")
	needle := "accountStore.SetAuthorizationGrantReturner(authzGrantReturner{store: authzStore})"
	if !strings.Contains(compose, needle) {
		t.Fatal("compose.go must wire the M11 return-authorization port onto the authz store")
	}
	storePos := strings.Index(compose, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(compose, needle)
	if storePos < 0 || wirePos < storePos {
		t.Fatal("the grant returner must be wired after the account store construction")
	}
	// The authz committed-write invalidator hunk (W2-B) stays intact.
	if !strings.Contains(compose, "authzStore.AttachWriteInvalidator(groupStatsDirtyMarker, bus)") {
		t.Fatal("the W2-B AttachWriteInvalidator hunk must stay wired")
	}
}

// newW2CTestRequest builds a gateway request with a parsed JSON body for the
// capability resolution tests (the gatewaydispatch fakes helper is
// package-internal there).
func newW2CTestRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	raw.Header.Set("Content-Type", "application/json")
	request := gatewaypreauth.NewGatewayRequest(raw)
	request.Body = &gatewaybody.Request{
		RawBody:           []byte(body),
		ContentTypeHeader: "application/json",
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
			Model:           strPtrOf("gpt-4o"),
			Stream:          strBoolPtr(true),
		},
	}
	return request
}

func strBoolPtr(value bool) *bool { return &value }
