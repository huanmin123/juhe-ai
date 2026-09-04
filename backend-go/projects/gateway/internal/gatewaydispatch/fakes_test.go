package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Shared fakes for the dispatch engine tests: each port mirrors the Node
// consumed surface with deterministic behavior and observation counters.

type fakeDriver struct {
	prepareCount   atomic.Int64
	supportsAll    bool
	mismatchAll    bool
	urls           []string
	urlByAccount   map[string][]string
	partsHeaders   map[string]string
	partsBody      string
	mismatchReason string
}

func (f *fakeDriver) PrepareGatewayUpstreamAccount(ctx context.Context, account AccountCandidate) (AccountCandidate, error) {
	f.prepareCount.Add(1)
	return account, nil
}

func (f *fakeDriver) BuildGatewayUpstreamURLsForAccount(ctx context.Context, account AccountCandidate, req *gatewaypreauth.GatewayRequest) ([]string, error) {
	if f.urlByAccount != nil {
		if urls, ok := f.urlByAccount[account.ID]; ok {
			return urls, nil
		}
	}
	if f.urls != nil {
		return f.urls, nil
	}
	return []string{"https://upstream.example/v1/chat/completions"}, nil
}

func (f *fakeDriver) BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error) {
	header := http.Header{}
	apiKey := "key-" + account.ID
	header.Set("Authorization", "Bearer "+apiKey)
	for name, value := range f.partsHeaders {
		header.Set(name, value)
	}
	body := f.partsBody
	if body == "" {
		body = `{"model":"gpt-test","input":"hi"}`
	}
	return PreparedRequestParts{
		Headers:              header,
		Body:                 []byte(body),
		EffectiveServiceTier: "default",
	}, nil
}

func (f *fakeDriver) AccountSupportsGatewayRequest(req *gatewaypreauth.GatewayRequest, account AccountCandidate, requestClientCompatibility string) bool {
	if f.mismatchAll {
		return false
	}
	return true
}

func (f *fakeDriver) GatewayRequestCapabilityMismatchReason(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) string {
	if f.mismatchReason != "" {
		return f.mismatchReason
	}
	return "request_capability_mismatch"
}

type fakeFailureDispatcher struct {
	handleFailedCalls atomic.Int64
	// failedResult is returned by HandleFailedUpstreamResponse.
	failedResult FailedUpstreamResponseResult
	// skipAccount controls HandleUpstreamRequestError's action.
	keyScopedFailure atomic.Bool
	handleErrorCalls atomic.Int64
	opaqueFailover   bool
}

func (f *fakeFailureDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	f.handleFailedCalls.Add(1)
	// Drain the body so the transport slot releases like the Node iterator
	// consumption does.
	if input.Response != nil && input.Response.Body != nil {
		_, _ = io.Copy(io.Discard, input.Response.Body)
		_ = input.Response.Body.Close()
	}
	result := f.failedResult
	if result.LastAttempt == nil {
		result.LastAttempt = &UpstreamAttempt{
			AccountID:   input.Account.ID,
			AccountName: input.Account.Name,
			UpstreamURL: input.UpstreamURL,
			Status:      input.Response.Status(),
			HasStatus:   true,
			Message:     "上游响应失败",
		}
	}
	return result, nil
}

func (f *fakeFailureDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	f.handleErrorCalls.Add(1)
	return UpstreamRequestErrorResult{
		Action:           FailedResponseActionSkipAccount,
		LastAttempt:      &UpstreamAttempt{AccountID: input.Account.ID, UpstreamURL: input.UpstreamURL, Message: input.Error.Error()},
		KeyScopedFailure: f.keyScopedFailure.Load(),
	}, nil
}

func (f *fakeFailureDispatcher) IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool {
	return f.opaqueFailover
}

type fakeSuppression struct {
	filterCalls atomic.Int64
	allSuppressed bool
}

func (f *fakeSuppression) FilterAsync(ctx context.Context, accounts []AccountCandidate, options SuppressionFilterOptions) (SuppressionFilterResult, error) {
	f.filterCalls.Add(1)
	if f.allSuppressed {
		return SuppressionFilterResult{
			Accounts:             nil,
			SuppressedCount:      len(accounts),
			AllSuppressed:        true,
			SuppressedAccountIDs: accountIDs(accounts),
			AcquiredHalfOpenLeases: []HalfOpenLease{},
		}, nil
	}
	return SuppressionFilterResult{Accounts: accounts, AcquiredHalfOpenLeases: []HalfOpenLease{}}, nil
}

func (f *fakeSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

func accountIDs(accounts []AccountCandidate) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

type fakeDegradation struct{}

func (f *fakeDegradation) OrderGatewayAccountsByRuntimeDegradation(accounts []AccountCandidate, modelRankByAccountID map[string]int) DegradationOrder {
	return DegradationOrder{Accounts: accounts}
}

func (f *fakeDegradation) OrderWithLaneAsync(ctx context.Context, accounts []AccountCandidate, requestLane string, policy *gatewayruntimecache.GroupSchedulingPolicy, priority *gatewayrouting.GatewayAccountModelPriority) (DegradationOrder, error) {
	return DegradationOrder{Accounts: accounts}, nil
}

func (f *fakeDegradation) OrderSync(accounts []AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) DegradationOrder {
	return DegradationOrder{Accounts: accounts}
}

type fakeIdentityOrder[T any] struct{}

func (f *fakeIdentityOrder[T]) OrderAsync(ctx context.Context, accounts []AccountCandidate, key string, options AffinityOrderingOptions) ([]AccountCandidate, error) {
	return accounts, nil
}

type fakeLatency struct{}

func (f *fakeLatency) OrderAsync(ctx context.Context, accounts []AccountCandidate, scope *LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, modelPriority *gatewayrouting.GatewayAccountModelPriority) (LatencyDegradationOrder, error) {
	return LatencyDegradationOrder{Accounts: accounts}, nil
}

type fakeProxyHealth struct {
	recordedFailures []string
}

func (f *fakeProxyHealth) OrderAsync(ctx context.Context, accounts []AccountCandidate, modelPriority *gatewayrouting.GatewayAccountModelPriority) (ProxyHealthOrder, error) {
	return ProxyHealthOrder{Accounts: accounts}, nil
}

func (f *fakeProxyHealth) RecordFailureAsync(ctx context.Context, account AccountCandidate, message string) error {
	f.recordedFailures = append(f.recordedFailures, account.ID+":"+message)
	return nil
}

type fakeAvoidance struct{}

func (f *fakeAvoidance) OrderAsync(ctx context.Context, accounts []AccountCandidate, scope ClientIPAvoidanceScope, modelPriority *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{Accounts: accounts}, nil
}

type fakeClientSourceAvoidance struct{}

func (f *fakeClientSourceAvoidance) OrderAsync(ctx context.Context, accounts []AccountCandidate, clientStrategy gatewaypreauth.ClientStrategyContext, modelPriority *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{Accounts: accounts}, nil
}

type fakeHotQuality struct{}

func (f *fakeHotQuality) OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	return HotQualityOrder{Accounts: input.Accounts, DispatchIntent: "primary_service"}, nil
}

type fakeAffinity struct {
	remembered []string
	forgotten  []string
	busy       bool
}

func (f *fakeAffinity) OrderAsync(ctx context.Context, accounts []AccountCandidate, sessionAffinityKey string, options AffinityOrderingOptions) ([]AccountCandidate, error) {
	return accounts, nil
}

func (f *fakeAffinity) ClaimAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope AffinityScope) (string, bool) {
	return "", false
}

func (f *fakeAffinity) RememberAsync(ctx context.Context, sessionAffinityKey, accountID string, scope AffinityScope) {
	f.remembered = append(f.remembered, accountID)
}

func (f *fakeAffinity) ForgetAsync(ctx context.Context, sessionAffinityKey, accountID string) error {
	f.forgotten = append(f.forgotten, accountID)
	return nil
}

func (f *fakeAffinity) AreHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []AccountCandidate, options HighConcurrencyBusyOptions) (bool, error) {
	return f.busy, nil
}

type fakeQueue struct{}

func (f *fakeQueue) WaitForCapacity(ctx context.Context, input HighConcurrencyWaitInput) (QueueWaitResult, error) {
	return QueueWaitResult{Ready: true}, nil
}

type fakeClientIPConcurrency struct{}

func (f *fakeClientIPConcurrency) Acquire(ctx context.Context, input ClientIPConcurrencyInput) (ClientIPConcurrencyDecision, error) {
	return ClientIPConcurrencyDecision{Enabled: false, Acquired: true, Release: func() {}}, nil
}

type fakeQuota struct {
	denied map[string]struct{}
}

func (f *fakeQuota) CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []AccountCandidate) (map[string]QuotaDecision, error) {
	decisions := make(map[string]QuotaDecision, len(accounts))
	for _, account := range accounts {
		_, denied := f.denied[account.ID]
		decisions[account.ID] = QuotaDecision{Allowed: !denied}
	}
	return decisions, nil
}

type fakeConcurrencyStore struct {
	acquired atomic.Int64
	limit    int
	// exhausted makes every acquire fail.
	exhausted bool
	released  atomic.Int64
}

func (f *fakeConcurrencyStore) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *fakeConcurrencyStore) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *fakeConcurrencyStore) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	if f.exhausted {
		return ConcurrencySlot{Acquired: false, Current: f.limit, Limit: f.limit, Lane: options.Lane}, nil
	}
	f.acquired.Add(1)
	return ConcurrencySlot{
		Acquired: true,
		Current:  1,
		Limit:    concurrencyLimit,
		Lane:     options.Lane,
		Release: func() {
			f.released.Add(1)
		},
		MarkFirstOutput: func() {},
	}, nil
}

type fakeCache struct{}

func (f *fakeCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error) {
	return nil, nil
}

func (f *fakeCache) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	return gatewayruntimecache.GroupUsageAccessMetadata{}, false, nil
}

func (f *fakeCache) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	return nil, nil
}

type fakeLocks struct{}

func (f *fakeLocks) FindStateAsync(ctx context.Context, accountID string) (*AccountLockStateView, error) {
	return nil, nil
}

func (f *fakeLocks) AcquireRetryLeaseAsync(ctx context.Context, accountID string, configuredDelayMs int64) (LockLeaseAcquire, error) {
	return LockLeaseAcquire{Allowed: true}, nil
}

func (f *fakeLocks) ConsumeRetryLeaseAsync(ctx context.Context, accountID, leaseID string) (bool, error) {
	return true, nil
}

func (f *fakeLocks) ReleaseRetryLeaseAsync(ctx context.Context, input ReleaseRetryLeaseInput) (bool, error) {
	return true, nil
}

func (f *fakeLocks) AbandonRetryReservationAsync(ctx context.Context, lease AccountLockRetryLease) error {
	return nil
}

func (f *fakeLocks) RecordFailureAsync(ctx context.Context, accountID, reason string, observation *AccountLockObservation) error {
	return nil
}

func (f *fakeLocks) SettleDeadlineAsync(ctx context.Context, accountID string, nowMs int64, observation *AccountLockObservation) error {
	return nil
}

func (f *fakeLocks) ListStatesAsync(ctx context.Context, accountIDs []string) (map[string]AccountLockStateView, error) {
	return map[string]AccountLockStateView{}, nil
}

type fakeUsage struct {
	records []FailedAttemptRecord
}

func (f *fakeUsage) RecordFailedUpstreamAttempt(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account AccountCandidate, record FailedAttemptRecord) error {
	f.records = append(f.records, record)
	return nil
}

type fakeAuditSink struct {
	started     int
	completed   int
	failed      int
	metadata    []string
}

func (f *fakeAuditSink) StartAttempt(input StartAttemptInput) string {
	f.started++
	return "attempt-" + intToStringTest(f.started)
}

func (f *fakeAuditSink) CompleteAttempt(attemptID string, input CompleteAttemptInput) {
	f.completed++
}

func (f *fakeAuditSink) RecordFailedDispatchAttempt(input FailedDispatchAttemptInput) {
	f.failed++
}

func intToStringTest(value int) string {
	digits := ""
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// frozenAudit implements the gatewaypreauth.AuditCaptureContext subset.
type frozenAudit struct {
	metadata []string
	sink     *fakeAuditSink
}

func (f *frozenAudit) BindContext(context gatewaypreauth.AuditGatewayContext) {}

func (f *frozenAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	f.metadata = append(f.metadata, label)
}

func (f *frozenAudit) Finalize(input gatewaypreauth.AuditFinalizeInput) {}

// newTestEngine assembles an Engine with fakes and optional overrides.
func newTestEngine(t *testing.T) (*Engine, *fakeDriver, *fakeFailureDispatcher) {
	t.Helper()
	driver := &fakeDriver{}
	dispatcher := &fakeFailureDispatcher{
		failedResult: FailedUpstreamResponseResult{Action: FailedResponseActionSkipAccount, FailureKind: ""},
	}
	engine := NewEngine(driver, dispatcher)
	engine.Suppression = &fakeSuppression{}
	engine.Degradation = &fakeDegradation{}
	engine.Latency = &fakeLatency{}
	engine.ProxyHealth = &fakeProxyHealth{}
	engine.ClientIPAvoidance = &fakeAvoidance{}
	engine.ClientSourceAvoidance = &fakeClientSourceAvoidance{}
	engine.HotQuality = &fakeHotQuality{}
	engine.Affinity = &fakeAffinity{}
	engine.HighConcurrencyQueue = &fakeQueue{}
	engine.ClientIPConcurrency = &fakeClientIPConcurrency{}
	engine.Quota = &fakeQuota{}
	engine.Concurrency = &fakeConcurrencyStore{}
	engine.Cache = &fakeCache{}
	engine.Locks = &fakeLocks{}
	engine.Usage = &fakeUsage{}
	return engine, driver, dispatcher
}

// newTestRequest builds a gateway request for POST /v1/chat/completions.
func newTestRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	raw.Header.Set("Content-Type", "application/json")
	request := gatewaypreauth.NewGatewayRequest(raw)
	if body == "" {
		body = `{"model":"gpt-test","stream":true}`
	}
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    mustJSONObject(t, body),
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
		},
	}
	return request
}

func mustJSONObject(t *testing.T, body string) map[string]any {
	t.Helper()
	object, ok := decodeJSONObject([]byte(body))
	if !ok {
		return map[string]any{}
	}
	return object
}

// testUsageContext is the shared usage context of the dispatch tests.
func testUsageContext() gatewaypreauth.GatewayFailureUsageContext {
	return gatewaypreauth.GatewayFailureUsageContext{
		TraceID:         "trace-test",
		TrafficSource:   "gateway",
		SystemAccountID: "system-1",
		APIKeyID:        "apikey-1",
		GroupID:         "group-1",
		Endpoint:        "/v1/chat/completions",
	}
}

// testAccounts builds n candidate accounts.
func testAccounts(ids ...string) []AccountCandidate {
	accounts := make([]AccountCandidate, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, AccountCandidate{
			ID:               id,
			Name:             "账号 " + id,
			Type:             "api_key",
			Status:           "active",
			ConcurrencyLimit: 4,
			Priority:         1,
			APIKey:           "key-" + id,
			SupportedModels:  []string{"gpt-test"},
		})
	}
	return accounts
}

// newTestCoordination builds the per-request coordination context.
func newTestCoordination(t *testing.T) *RequestCoordinationContext {
	t.Helper()
	now := NowMs()
	wallBudget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: now,
		BudgetMs:            ptrInt64(60_000),
	}, nil)
	if err != nil {
		t.Fatalf("NewGatewayRequestWallBudget: %v", err)
	}
	coordinationBudget, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: "trace-test",
	})
	if err != nil {
		t.Fatalf("NewRouteCoordinationBudget: %v", err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatalf("NewGatewayRequestAttemptTracker: %v", err)
	}
	return &RequestCoordinationContext{
		Scope:                    CoordinationScopeGatewayRequest,
		ServerRetryBudget:        gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		GatewayRequestWallBudget: wallBudget,
		RouteCoordinationBudget:  coordinationBudget,
		RequestAttemptTracker:    tracker,
	}
}
