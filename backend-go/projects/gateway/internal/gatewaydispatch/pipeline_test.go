package gatewaydispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Candidate pipeline tests: dispatch/candidate-filter.ts +
// dispatch/preparation.ts + dispatch/api-key-group-fallback-candidate.ts +
// dispatch/model-filter.ts + dispatch/capacity.ts.

func newPipeline(t *testing.T) (*CandidatePipeline, *Engine, *fakeDriver, *fakeFailureDispatcher) {
	t.Helper()
	engine, driver, dispatcher := newTestEngine(t)
	return NewCandidatePipeline(engine), engine, driver, dispatcher
}

func candidateFilterInput(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) gatewaypreauth.CandidateFilterInput {
	t.Helper()
	return gatewaypreauth.CandidateFilterInput{
		Req:             req,
		AuditCapture:     &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:     testUsageContext(),
		StartedAt:        NowMs(),
		RawCandidates:    accounts,
		SystemAccountID:  "system-1",
		GroupID:          "group-1",
		Endpoint:         "/v1/chat/completions",
		RouteCoordinator: &capturingCoordinator{},
	}
}

// capturingCoordinator records route fallback / failure calls.
type capturingCoordinator struct {
	fallbackReason string
	fallbackCalled bool
	failure        *gatewayrouting.GatewayRouteFinalFailure
	nextFallback   *gatewayrouting.GatewayRouteFallbackDecision
}

func (c *capturingCoordinator) RequestFallback(ctx context.Context, reason string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	c.fallbackCalled = true
	c.fallbackReason = reason
	if c.nextFallback != nil {
		return *c.nextFallback, nil
	}
	return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
}

func (c *capturingCoordinator) CompleteFailure(ctx context.Context, failure gatewayrouting.GatewayRouteFinalFailure) error {
	copied := failure
	c.failure = &copied
	return nil
}

func TestFilterCandidatesAccounts(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	accounts := testAccounts("a-1", "a-2")
	result, err := pipeline.FilterCandidates(context.Background(), candidateFilterInput(t, req, accounts))
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(result.Accounts))
	}
	if result.ModelPriority == nil {
		t.Fatal("model priority missing")
	}
	for _, account := range accounts {
		if rank := result.ModelPriority.RankByAccountID[account.ID]; rank != ModelPriorityRankDirect {
			t.Fatalf("rank(%s) = %d", account.ID, rank)
		}
	}
}

func TestFilterCandidatesNoAccountsTriesFallback(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	coordinator := &capturingCoordinator{
		nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true},
	}
	input := candidateFilterInput(t, req, nil)
	input.RouteCoordinator = coordinator
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "no_candidate_accounts" {
		t.Fatalf("result = %#v", result)
	}
	if !coordinator.fallbackCalled || coordinator.fallbackReason != "no_candidate_accounts" {
		t.Fatalf("coordinator = %#v", coordinator)
	}
}

func TestFilterCandidatesCapabilityMismatchCompletesWith503(t *testing.T) {
	pipeline, _, driver, _ := newPipeline(t)
	driver.mismatchAll = true
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := candidateFilterInput(t, req, testAccounts("a-1"))
	input.RouteCoordinator = coordinator
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil {
		t.Fatal("completeFailure missing")
	}
	if coordinator.failure.StatusCode != 503 || coordinator.failure.ErrorCode != "request_capability_mismatch" {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
	if coordinator.failure.Message != "当前分组无账户支持请求路径或客户端协议" {
		t.Fatalf("message = %q", coordinator.failure.Message)
	}
}

func TestFilterCandidatesUnsupportedModelMessage(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Driver = &fakeDriver{}
	req := newTestRequest(t, `{"model":"no-such-model","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := candidateFilterInput(t, req, testAccounts("a-1"))
	// Give the account a narrow supported-model list so the filter skips it.
	input.RawCandidates = []AccountCandidate{{
		ID: "a-1", Name: "a-1", Type: "api_key",
		SupportedModels: []string{"other-model"},
	}}
	input.RouteCoordinator = coordinator
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil || coordinator.failure.ErrorCode != "unsupported_model" {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
	if !strings.Contains(coordinator.failure.Message, "当前分组无账户支持请求模型：no-such-model") {
		t.Fatalf("message = %q", coordinator.failure.Message)
	}
}

func TestFilterCandidatesMissingModelMessage(t *testing.T) {
	req := newTestRequest(t, `{"stream":true}`)
	result := FilterGatewayAccountsByRequestedModel([]AccountCandidate{{
		ID: "a-1", SupportedModels: []string{"m"},
	}}, "", gatewayrouting.EndpointFamilyChatCompletions)
	if result.Reason != "missing_model" {
		t.Fatalf("reason = %q", result.Reason)
	}
	message := GatewayModelFilterFailureMessage(result)
	if message != "请求缺少 model，当前分组内账户均需要按支持模型匹配，无法调度" {
		t.Fatalf("message = %q", message)
	}
	_ = req
}

func TestFilterCandidatesModelMappingRank(t *testing.T) {
	accounts := []AccountCandidate{
		{
			ID: "mapping-account", SupportedModels: []string{"mapped-model"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "mapped-model",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}},
		},
		{ID: "direct-account", SupportedModels: []string{"gpt-test"}},
	}
	result := FilterGatewayAccountsByRequestedModel(accounts, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.DirectMatchedCount != 1 || result.MappingMatchedCount != 1 {
		t.Fatalf("counts = %d/%d", result.DirectMatchedCount, result.MappingMatchedCount)
	}
	// Direct matches precede mapping matches.
	if result.Accounts[0].ID != "direct-account" {
		t.Fatalf("ordering = %#v", accountIDs(result.Accounts))
	}
	if result.ModelPriority.RankByAccountID["mapping-account"] != ModelPriorityRankMapping {
		t.Fatalf("mapping rank = %d", result.ModelPriority.RankByAccountID["mapping-account"])
	}
}

func TestPrepareDispatchAccountsReady(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	input := gatewaypreauth.DispatchPreparationInput{
		Req:             req,
		AuditCapture:    &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:    testUsageContext(),
		StartedAt:       NowMs(),
		CandidateAccounts: testAccounts("a-1", "a-2"),
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{
			RankByAccountID: map[string]int{"a-1": ModelPriorityRankDirect, "a-2": ModelPriorityRankDirect},
		},
		GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:  "system-1",
		APIKeyID:         "apikey-1",
		GroupID:          "group-1",
		ClientStrategy:   gatewaypreauth.ClientStrategyContext{},
		RequestLane:      "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator: &capturingCoordinator{},
		Signal:           context.Background(),
	}
	concurrency := &fakeConcurrencyStore{}
	engine.Concurrency = concurrency
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s (fallback reason %q)", result.Outcome, result.Reason)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(result.Accounts))
	}
	if concurrency.acquired.Load() != 0 {
		t.Fatal("normal groups must not consume per-account slots during preparation")
	}
}

func TestPrepareDispatchAccountsQuotaDeniedCompletes429(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Quota = &fakeQuota{denied: map[string]struct{}{"a-1": {}}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		CandidateAccounts: testAccounts("a-1"),
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  coordinator,
		Signal:            context.Background(),
	}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil || coordinator.failure.StatusCode != 429 {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
	if coordinator.failure.Message != "额度已用完，请联系管理员提升额度" {
		t.Fatalf("message = %q", coordinator.failure.Message)
	}
}

func TestPrepareDispatchAccountsNoAvailableAccount(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Quota = &fakeQuota{denied: map[string]struct{}{}}
	// All candidates suppressed → completed 503 path via empty accounts.
	engine.Suppression = &fakeSuppression{allSuppressed: false}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		CandidateAccounts: nil,
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  coordinator,
		Signal:            context.Background(),
	}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil || coordinator.failure.Message != "没有可用的上游账户" {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
	if coordinator.failure.ErrorCode != "no_available_upstream_account" {
		t.Fatalf("error code = %q", coordinator.failure.ErrorCode)
	}
}

func TestPrepareDispatchAccountsAllSuppressedFallsBack(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Suppression = &fakeSuppression{allSuppressed: true}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	coordinator := &capturingCoordinator{
		nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true},
	}
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		CandidateAccounts: testAccounts("a-1"),
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  coordinator,
		Signal:            context.Background(),
	}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "local_account_suppressed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolveNextGroupFallbackCandidate(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("b-1"),
		},
	}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			{GroupID: "group-3", Status: "disabled", GroupEnabled: 1},
		},
	}
	if !CanAttemptApiKeyGroupFallback(apiKeyRecord, "group-1", nil) {
		t.Fatal("fallback should be attemptable with a later active binding")
	}
	if !CanAttemptApiKeyGroupFallback(apiKeyRecord, "group-2", nil) {
		t.Fatal("a later binding (even disabled) keeps fallback attemptable, mirroring Node")
	}
	if CanAttemptApiKeyGroupFallback(apiKeyRecord, "group-3", nil) {
		t.Fatal("the last binding must not allow fallback")
	}
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:             req,
		Reason:          "runtime_degraded",
		APIKeyRecord:    apiKeyRecord,
		SystemAccountID: "system-1",
		GroupID:         "group-1",
		RequestLane:     "text",
	})
	if err != nil {
		t.Fatalf("ResolveNextGroupFallbackCandidate: %v", err)
	}
	if !found {
		t.Fatal("expected a fallback candidate from group-2")
	}
	if candidate.GroupID != "group-2" || len(candidate.Accounts) != 1 {
		t.Fatalf("candidate = %#v", candidate)
	}
}

// TestResolveNextGroupFallbackCandidateExcludesExhaustedAccounts pins the F5-1
// wiring: the request-level exhausted set (switchToFallbackGroup's
// excludedAccountIds, routes.ts:625) removes shared accounts from every
// candidate group window (api-key-group-fallback-candidate.ts:79-84), and a
// group whose accounts are all exhausted is skipped instead of entered.
func TestResolveNextGroupFallbackCandidateExcludesExhaustedAccounts(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("a-shared", "b-1"),
			"group-3": testAccounts("a-shared"),
		},
	}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			{GroupID: "group-3", Status: "active", GroupEnabled: 1},
		},
	}
	// Cross-group overlap: a-shared failed in group-1 enters the exhausted
	// set and must not reappear in the group-2 fallback window.
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:                req,
		Reason:             "upstream_accounts_exhausted",
		APIKeyRecord:       apiKeyRecord,
		SystemAccountID:    "system-1",
		GroupID:            "group-1",
		RequestLane:        "text",
		ExcludedAccountIDs: map[string]struct{}{"a-shared": {}},
	})
	if err != nil {
		t.Fatalf("ResolveNextGroupFallbackCandidate: %v", err)
	}
	if !found {
		t.Fatal("expected a fallback candidate from group-2")
	}
	if candidate.GroupID != "group-2" {
		t.Fatalf("candidate group = %s", candidate.GroupID)
	}
	if len(candidate.Accounts) != 1 || candidate.Accounts[0].ID != "b-1" {
		t.Fatalf("exhausted account must be excluded from the candidate window: %#v", candidate.Accounts)
	}
	// A group whose accounts are all exhausted is skipped entirely: the scan
	// continues and reports not-found when no later group has candidates.
	_, found, err = pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:                req,
		Reason:             "upstream_accounts_exhausted",
		APIKeyRecord:       apiKeyRecord,
		SystemAccountID:    "system-1",
		GroupID:            "group-2",
		RequestLane:        "text",
		ExcludedAccountIDs: map[string]struct{}{"a-shared": {}},
	})
	if err != nil {
		t.Fatalf("resolve group-3: %v", err)
	}
	if found {
		t.Fatal("a group with only exhausted accounts must be skipped")
	}
}

func TestResolveNextGroupFallbackCandidateRuntimeDegradedSkipsAllDegraded(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("b-1"),
		},
	}
	// Degradation port reports every candidate degraded → skip the group.
	engine.Degradation = &allDegradedPort{}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		},
	}
	_, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:             newTestRequest(t, `{"model":"gpt-test","stream":true}`),
		Reason:          "runtime_degraded",
		APIKeyRecord:    apiKeyRecord,
		SystemAccountID: "system-1",
		GroupID:         "group-1",
		RequestLane:     "text",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if found {
		t.Fatal("runtime_degraded groups with only degraded accounts must be skipped")
	}
}

func TestCapacityOrderingKeepsStableOrder(t *testing.T) {
	accounts := testAccounts("a-1", "a-2", "a-3")
	ordered, err := OrderGatewayAccountsByLaneCapacityAvailabilityAsync(
		context.Background(), &fakeConcurrencyStore{}, accounts, "text", nil, nil)
	if err != nil {
		t.Fatalf("ordering: %v", err)
	}
	for index, account := range ordered {
		if account.ID != accounts[index].ID {
			t.Fatalf("order changed: %#v", accountIDs(ordered))
		}
	}
	busy, err := AreGatewayAccountsCapacityBusyForLaneAsync(context.Background(), &fakeConcurrencyStore{}, nil, "text", nil)
	if err != nil || busy {
		t.Fatalf("empty account set must not be busy (%v, %v)", busy, err)
	}
}

func TestGatewayAccountConcurrencyIdentityUsesCredentialSource(t *testing.T) {
	source := "source-account"
	accounts := []AccountCandidate{
		{ID: "a", CredentialSourceAccountID: &source},
		{ID: "b", CredentialSourceAccountID: &source},
		{ID: "c"},
	}
	ids := gatewaySessionConcurrencyIDs(accounts)
	if len(ids) != 2 || ids[0] != "source-account" || ids[1] != "c" {
		t.Fatalf("ids = %#v", ids)
	}
}

// fakeCacheFallback serves per-group cached accounts.
type fakeCacheFallback struct {
	accounts map[string][]AccountCandidate
}

func (f *fakeCacheFallback) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error) {
	return f.accounts[groupID], nil
}

func (f *fakeCacheFallback) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	return gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}, true, nil
}

func (f *fakeCacheFallback) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	return nil, nil
}

// allDegradedPort reports bypassedAllDegraded for every candidate set.
type allDegradedPort struct{}

func (f *allDegradedPort) OrderGatewayAccountsByRuntimeDegradation(accounts []AccountCandidate, modelRankByAccountID map[string]int) DegradationOrder {
	return DegradationOrder{Accounts: accounts, BypassedAllDegraded: true, DegradedCount: len(accounts)}
}

func (f *allDegradedPort) OrderWithLaneAsync(ctx context.Context, accounts []AccountCandidate, requestLane string, policy *gatewayruntimecache.GroupSchedulingPolicy, priority *gatewayrouting.GatewayAccountModelPriority) (DegradationOrder, error) {
	return f.OrderGatewayAccountsByRuntimeDegradation(accounts, nil), nil
}

func (f *allDegradedPort) OrderSync(accounts []AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) DegradationOrder {
	return f.OrderGatewayAccountsByRuntimeDegradation(accounts, nil)
}
