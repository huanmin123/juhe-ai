package gatewaypreauth

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Test doubles for every port: deterministic, replayable and shared by the
// table-driven tests of this package.

type fakeClock struct {
	mu    sync.Mutex
	nowMs int64
}

func newFakeClock(nowMs int64) *fakeClock { return &fakeClock{nowMs: nowMs} }

func (c *fakeClock) Now() time.Time { return time.UnixMilli(c.nowMsValue()) }
func (c *fakeClock) nowMsValue() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nowMs
}
func (c *fakeClock) Advance(ms int64) {
	c.mu.Lock()
	c.nowMs += ms
	c.mu.Unlock()
}

// fakeObservability records log events and stage logs.
type fakeObservability struct {
	mu         sync.Mutex
	events     []loggedEvent
	stages     []loggedStage
	sanitized  []string
	traceIDSeq int
}

type loggedEvent struct {
	event   string
	fields  map[string]any
	message string
}

type loggedStage struct {
	stage   string
	fields  map[string]any
	outcome string
}

func (o *fakeObservability) Logger() Logger  { return &fakeLogger{obs: o} }
func (o *fakeObservability) TraceID() string { return "trace_fixed" }
func (o *fakeObservability) CreateTraceID() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.traceIDSeq++
	return "trace_" + itoaTest(o.traceIDSeq)
}
func (o *fakeObservability) SanitizeURLForLog(value string) string {
	o.mu.Lock()
	o.sanitized = append(o.sanitized, value)
	o.mu.Unlock()
	return value
}
func (o *fakeObservability) LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time) {
	o.mu.Lock()
	o.stages = append(o.stages, loggedStage{stage: stage, fields: fields, outcome: outcome})
	o.mu.Unlock()
}
func (o *fakeObservability) eventsByName(name string) []loggedEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []loggedEvent{}
	for _, event := range o.events {
		if event.event == name {
			out = append(out, event)
		}
	}
	return out
}

type fakeLogger struct{ obs *fakeObservability }

func (l *fakeLogger) Warn(event string, fields map[string]any, message string) {
	l.obs.mu.Lock()
	l.obs.events = append(l.obs.events, loggedEvent{event: event, fields: fields, message: message})
	l.obs.mu.Unlock()
}

// fakeCircuits records circuit interactions.
type fakeCircuits struct {
	mu               sync.Mutex
	inspectDecision  CircuitDecision
	recordDecision   CircuitDecision
	clientIPDecision CircuitDecision
	sampleDecision   CircuitDecision
	recordErr        error
	preAuthFailures  []PreAuthFailureInput
	samples          []ClientIPErrorCircuitSampleInput
	successRecords   []ClientIPErrorCircuitInput
}

func (c *fakeCircuits) InspectPreAuthCircuit(_ context.Context, input PreAuthCircuitInput) (CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inspectDecision, nil
}
func (c *fakeCircuits) RecordPreAuthFailure(_ context.Context, input PreAuthFailureInput) (CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preAuthFailures = append(c.preAuthFailures, input)
	if c.recordErr != nil {
		return CircuitDecision{}, c.recordErr
	}
	return c.recordDecision, nil
}
func (c *fakeCircuits) InspectClientIPErrorCircuit(_ context.Context, input ClientIPErrorCircuitInput) (CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clientIPDecision, nil
}
func (c *fakeCircuits) RecordClientIPErrorCircuitSuccess(_ context.Context, input ClientIPErrorCircuitInput) error {
	c.mu.Lock()
	c.successRecords = append(c.successRecords, input)
	c.mu.Unlock()
	return nil
}
func (c *fakeCircuits) RecordClientIPErrorCircuitSample(_ context.Context, input ClientIPErrorCircuitSampleInput) (CircuitDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, input)
	return c.sampleDecision, nil
}

// fakeIPPolicy mirrors inspectClientIpPolicy.
type fakeIPPolicy struct {
	decision ClientIPPolicyDecision
	hits     []BlacklistPolicy
}

func (p *fakeIPPolicy) InspectClientIPPolicy(_ context.Context, _ string, _ bool) (ClientIPPolicyDecision, error) {
	return p.decision, nil
}
func (p *fakeIPPolicy) RecordClientIPPolicyHit(policy BlacklistPolicy) {
	p.hits = append(p.hits, policy)
}

// fakeUserLimits mirrors the user request limit counter.
type fakeUserLimits struct {
	mu       sync.Mutex
	decision UserRequestLimitDecision
	inputs   []UserRequestLimitConsumeInput
	started  int
}

func (u *fakeUserLimits) Consume(input UserRequestLimitConsumeInput) UserRequestLimitDecision {
	u.mu.Lock()
	u.inputs = append(u.inputs, input)
	u.mu.Unlock()
	return u.decision
}
func (u *fakeUserLimits) StartCoordinator() { u.started++ }

// fakeModelsRateLimit mirrors the authenticated models limiter.
type fakeModelsRateLimit struct {
	decision AuthenticatedModelsRateLimitDecision
	inputs   []AuthenticatedModelsRateLimitInput
}

func (m *fakeModelsRateLimit) Consume(_ context.Context, input AuthenticatedModelsRateLimitInput) (AuthenticatedModelsRateLimitDecision, error) {
	m.inputs = append(m.inputs, input)
	return m.decision, nil
}

// fakeRuntimeCache mirrors the runtime cache reader.
type fakeRuntimeCache struct {
	runtimeByKey map[string]gatewayruntimecache.GatewayRuntime
	readErr      error
	settings     gatewayruntimecache.GatewaySettings
	groupAccess  *gatewayruntimecache.GroupUsageAccessMetadata
	accounts     []gatewayruntimecache.OpenAIAccountSecret
	catalog      []gatewayruntimecache.ProviderModelCatalogItem
	policies     []gatewayruntimecache.ResponseInspectionPolicySummary
}

func (c *fakeRuntimeCache) ReadCachedGatewayRuntimeAsync(_ context.Context, apiKey string) (gatewayruntimecache.GatewayRuntime, error) {
	if c.readErr != nil {
		return gatewayruntimecache.GatewayRuntime{}, c.readErr
	}
	if runtime, ok := c.runtimeByKey[apiKey]; ok {
		return runtime, nil
	}
	return gatewayruntimecache.GatewayRuntime{}, nil
}
func (c *fakeRuntimeCache) ReadCachedGatewaySettingsAsync(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return c.settings, nil
}
func (c *fakeRuntimeCache) ResolveCachedGroupUsageAccessMetadataAsync(_ context.Context, _, _ string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return c.groupAccess, nil
}
func (c *fakeRuntimeCache) ListCachedOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return c.accounts, nil
}
func (c *fakeRuntimeCache) ListFreshOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return c.accounts, nil
}
func (c *fakeRuntimeCache) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (c *fakeRuntimeCache) ListCachedActiveResponseInspectionPoliciesForAccountsAsync(context.Context, []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return c.policies, nil
}
func (c *fakeRuntimeCache) ListCachedProviderModelCatalogAsync(context.Context, gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	return c.catalog, nil
}

// fakeQuotaCheckers mirror the quota ports.
type fakeAPIKeyQuota struct {
	decision gatewayquota.Decision
	err      error
	rows     []gatewayquota.APIKeyRow
}

func (q *fakeAPIKeyQuota) CheckAPIKeyQuotaAsync(_ context.Context, apiKey gatewayquota.APIKeyRow) (gatewayquota.Decision, error) {
	q.rows = append(q.rows, apiKey)
	if q.err != nil {
		return gatewayquota.Decision{}, q.err
	}
	return q.decision, nil
}

type fakeAuthorizationQuota struct {
	decision gatewayquota.Decision
	err      error
	calls    []gatewayquota.GroupAccessMetadata
}

func (q *fakeAuthorizationQuota) CheckAuthorizationQuotaAsync(_ context.Context, groupAccess gatewayquota.GroupAccessMetadata, _ *gatewayquota.AccountAuthorizationSummary) (gatewayquota.Decision, error) {
	q.calls = append(q.calls, groupAccess)
	if q.err != nil {
		return gatewayquota.Decision{}, q.err
	}
	return q.decision, nil
}

type fakeInflight struct {
	decision gatewayquota.InflightDecision
	err      error
	inputs   []gatewayquota.GatewayReserveInput
}

func (q *fakeInflight) ReserveGatewayCost(_ context.Context, input gatewayquota.GatewayReserveInput) (gatewayquota.InflightDecision, error) {
	q.inputs = append(q.inputs, input)
	if q.err != nil {
		return gatewayquota.InflightDecision{}, q.err
	}
	return q.decision, nil
}

// fakeResponseSink records failure responses and model sends.
type fakeResponseSink struct {
	mu            sync.Mutex
	failureInputs []FailureResponseInput
	modelSends    []ModelsResponseInput
	authFailures  int
}

func (r *fakeResponseSink) SendGatewayFailureResponse(input FailureResponseInput) {
	r.mu.Lock()
	r.failureInputs = append(r.failureInputs, input)
	r.mu.Unlock()
}
func (r *fakeResponseSink) FinalizeGatewayAuthFailureAudit(*GatewayRequest, GatewayResponseWriter, AuditCaptureContext) {
	r.mu.Lock()
	r.authFailures++
	r.mu.Unlock()
}
func (r *fakeResponseSink) SendAuthenticatedModelsGatewayResponse(input ModelsResponseInput) {
	r.recordModelSend(input)
}
func (r *fakeResponseSink) SendOpenAIModelsGatewayResponse(input ModelsResponseInput) {
	r.recordModelSend(input)
}
func (r *fakeResponseSink) SendAnthropicModelsGatewayResponse(input ModelsResponseInput) {
	r.recordModelSend(input)
}
func (r *fakeResponseSink) SendGeminiModelsGatewayResponse(input ModelsResponseInput) {
	r.recordModelSend(input)
}
func (r *fakeResponseSink) recordModelSend(input ModelsResponseInput) {
	r.mu.Lock()
	r.modelSends = append(r.modelSends, input)
	r.mu.Unlock()
}
func (r *fakeResponseSink) lastFailure() (FailureResponseInput, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failureInputs) == 0 {
		return FailureResponseInput{}, false
	}
	return r.failureInputs[len(r.failureInputs)-1], true
}

// fakeAuditCapture records the audit capture calls.
type fakeAuditCapture struct {
	contexts []AuditGatewayContext
	metadata []auditMetadataCall
	finals   []AuditFinalizeInput
}

type auditMetadataCall struct {
	label    string
	metadata map[string]any
}

func (a *fakeAuditCapture) BindContext(context AuditGatewayContext) {
	a.contexts = append(a.contexts, context)
}
func (a *fakeAuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	a.metadata = append(a.metadata, auditMetadataCall{label: label, metadata: metadata})
}
func (a *fakeAuditCapture) Finalize(input AuditFinalizeInput) { a.finals = append(a.finals, input) }

// remaining fakes: strategy / session / affinity / codex / candidates /
// images / recoverable / audit settings / dispatcher / validator.

type fakeClientStrategy struct {
	strategy ClientStrategyContext
	resolves int
}

func (f *fakeClientStrategy) Resolve(*GatewayRequest, ClientStrategyInput) ClientStrategyContext {
	f.resolves++
	return f.strategy
}
func (f *fakeClientStrategy) AuditMetadata(ClientStrategyContext) map[string]any {
	return map[string]any{"profile": f.strategy.ClientProfile}
}

type fakeSessionIdentity struct {
	identity SessionIdentity
}

func (f *fakeSessionIdentity) ResolveGatewaySessionIdentity(*GatewayRequest, SessionIdentityInput) SessionIdentity {
	return f.identity
}

type fakeSessionAffinity struct {
	fromClientSource string
	fromIdentity     string
}

func (f *fakeSessionAffinity) ResolveKeyFromClientSource(*ClientSource, SessionAffinityScope) (string, bool) {
	if f.fromClientSource == "" {
		return "", false
	}
	return f.fromClientSource, true
}
func (f *fakeSessionAffinity) ResolveKey(SessionIdentity, SessionAffinityScope) (string, bool) {
	if f.fromIdentity == "" {
		return "", false
	}
	return f.fromIdentity, true
}

type fakeCodex struct {
	compactionExpected bool
	stateCompleted     bool
	compactResult      CodexCompactPreflightResult
	compactErr         error
}

func (f *fakeCodex) CompactionExpectedForRequest(*GatewayRequest) bool { return f.compactionExpected }
func (f *fakeCodex) ApplyContextStatePreflight(context.Context, CodexContextStateInput) (bool, error) {
	return f.stateCompleted, nil
}
func (f *fakeCodex) ApplyChatBridgeCompactPreflight(context.Context, CodexCompactPreflightInput) (CodexCompactPreflightResult, error) {
	if f.compactErr != nil {
		return CodexCompactPreflightResult{}, f.compactErr
	}
	return f.compactResult, nil
}

type fakeCandidates struct {
	filterResult         CandidateFilterResult
	filterErr            error
	preparation          DispatchPreparationResult
	prepareErr           error
	fallbackCandidate    *GroupFallbackCandidate
	fallbackCandidateErr error
	fallbackFound        bool
}

func (f *fakeCandidates) FilterCandidates(context.Context, CandidateFilterInput) (CandidateFilterResult, error) {
	if f.filterErr != nil {
		return CandidateFilterResult{}, f.filterErr
	}
	return f.filterResult, nil
}
func (f *fakeCandidates) PrepareDispatchAccounts(context.Context, DispatchPreparationInput) (DispatchPreparationResult, error) {
	if f.prepareErr != nil {
		return DispatchPreparationResult{}, f.prepareErr
	}
	return f.preparation, nil
}
func (f *fakeCandidates) ResolveNextGroupFallbackCandidate(context.Context, GroupFallbackCandidateInput) (GroupFallbackCandidate, bool, error) {
	if f.fallbackCandidateErr != nil {
		return GroupFallbackCandidate{}, false, f.fallbackCandidateErr
	}
	if f.fallbackCandidate == nil {
		return GroupFallbackCandidate{}, false, nil
	}
	return *f.fallbackCandidate, f.fallbackFound, nil
}

type fakeImages struct {
	result ImagePermissionPreflightResult
}

func (f *fakeImages) Apply(context.Context, ImagePermissionPreflightInput) (ImagePermissionPreflightResult, error) {
	return f.result, nil
}

type fakeRecoverable struct{ waited []string }

func (f *fakeRecoverable) WaitForRecoverableUnavailableState(_ context.Context, input RecoverableWaitInput) error {
	f.waited = append(f.waited, input.ScopeKey)
	return nil
}

type fakeAuditSettings struct{ enabled bool }

func (f *fakeAuditSettings) AuditLogEnabled() bool { return f.enabled }

type fakeAuditDispatcher struct{ dispatched []DispatchedAuditLogInput }

func (f *fakeAuditDispatcher) Dispatch(input DispatchedAuditLogInput) {
	f.dispatched = append(f.dispatched, input)
}

type fakeAPIKeyValidator struct {
	row *gatewayruntimecache.GatewayAPIKeyRow
}

func (f *fakeAPIKeyValidator) Validate(context.Context, string) (*gatewayruntimecache.GatewayAPIKeyRow, error) {
	return f.row, nil
}

type fakeAccountAvoidance struct{ created int }

func (f *fakeAccountAvoidance) CreateTracker(ClientIPAccountAvoidanceInput) ClientIPAccountAvoidanceTracker {
	f.created++
	return &struct{}{}
}

type fakeRouteResolver struct {
	normal NormalRouteResult
	hybrid HybridRouteResult
}

func (f *fakeRouteResolver) ResolveNormalGatewayModelRoute(context.Context, NormalRouteInput) (NormalRouteResult, error) {
	return f.normal, nil
}
func (f *fakeRouteResolver) ResolveHybridGatewayRoute(context.Context, HybridRouteInput) (HybridRouteResult, error) {
	return f.hybrid, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestService(t *testing.T, mutate func(*Service)) (*Service, *fakeObservability, *fakeResponseSink) {
	t.Helper()
	obs := &fakeObservability{}
	sink := &fakeResponseSink{}
	service := &Service{
		RuntimeCache:       &fakeRuntimeCache{},
		APIKeyValidator:    &fakeAPIKeyValidator{row: validRuntimeRow()},
		APIKeyQuota:        &fakeAPIKeyQuota{decision: gatewayquota.AllowedDecision()},
		AuthorizationQuota: &fakeAuthorizationQuota{decision: gatewayquota.AllowedDecision()},
		InflightQuota:      &fakeInflight{decision: gatewayquota.InflightDecision{Allowed: true}},
		Circuits:           &fakeCircuits{},
		IPPolicy:           &fakeIPPolicy{},
		UserLimits:         &fakeUserLimits{decision: UserRequestLimitDecision{Allowed: true}},
		ModelsRateLimit:    &fakeModelsRateLimit{decision: AuthenticatedModelsRateLimitDecision{Allowed: true}},
		ClientStrategy:     &fakeClientStrategy{strategy: ClientStrategyContext{ClientProfile: "generic"}},
		SessionIdentity:    &fakeSessionIdentity{identity: SessionIdentity{SessionID: "session_1"}},
		SessionAffinity:    &fakeSessionAffinity{},
		Codex:              &fakeCodex{},
		Candidates:         &fakeCandidates{},
		Images:             &fakeImages{result: ImagePermissionPreflightResult{RequestLane: "text"}},
		Responses:          sink,
		Recoverable:        &fakeRecoverable{},
		AuditSettings:      &fakeAuditSettings{enabled: true},
		AuditDispatch:      &fakeAuditDispatcher{},
		Observability:      obs,
		RouteResolver:      &fakeRouteResolver{},
		AccountAvoidance:   &fakeAccountAvoidance{},
		Clock:              newFakeClock(1_700_000_000_000),
	}
	if mutate != nil {
		mutate(service)
	}
	if _, err := New(*service); err != nil {
		t.Fatalf("New() 失败: %v", err)
	}
	return service, obs, sink
}

func newTestRequest(method, target string) (*GatewayRequest, *httptest.ResponseRecorder, *TrackingWriter) {
	httpReq := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	return &GatewayRequest{HTTP: httpReq, RemoteAddr: "203.0.113.9:44556", ClientIP: "203.0.113.9"}, recorder, writer
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是 JSON: %v (%q)", err, recorder.Body.String())
	}
	return body
}

func errorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := decodeBody(t, recorder)
	errObject, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 error 对象: %v", body)
	}
	return errObject
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func validRuntimeRow() *gatewayruntimecache.GatewayAPIKeyRow {
	return &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                                  "key_1",
		SystemAccountID:                     "sys_1",
		RouteStrategyMode:                   gatewayruntimecache.RouteStrategyModeNormal,
		SelectedGroupID:                     "group_1",
		Status:                              "active",
		SystemAccountImageGenerationEnabled: 1,
		SystemAccountRequestLimits: &gatewayruntimecache.UserRequestLimits{
			PerMinute: int64Ptr(50),
		},
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{
			ID: "binding_1", APIKeyID: "key_1", GroupID: "group_1",
			Status: "active", GroupEnabled: 1, ProviderCode: "openai",
		}},
	}
}

func validRuntime() gatewayruntimecache.GatewayRuntime {
	return gatewayruntimecache.GatewayRuntime{
		APIKey: validRuntimeRow(),
		Settings: gatewayruntimecache.GatewaySettings{
			NoAvailableAccountWaitTimeoutSeconds: 30,
			ImageRequestWallTimeoutSeconds:       300,
		},
		GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
			ProviderCode: "openai", GroupAccessType: "owner",
		},
	}
}

func plainPreflightOptions() *PreflightOptions {
	return &PreflightOptions{
		Identity: &OpenAIGatewayRequestIdentity{
			SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1",
		},
		APIKeyRecord: validRuntimeRow(),
	}
}

// bodyRequestForState builds a body request carrying only the state.
func bodyRequestForState(state *gatewaybody.BodyState) gatewaybody.Request {
	return gatewaybody.Request{State: state}
}

// bodyRequestForBody builds a body request carrying a parsed JSON object.
func bodyRequestForBody(body map[string]any) gatewaybody.Request {
	return gatewaybody.Request{
		Body: body,
		State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
			ParsedBody:      body,
		}),
	}
}

// gatewaybodyInvalidJSON mirrors a captured invalid_json state.
func gatewaybodyInvalidJSON() *gatewaybody.BodyState {
	return gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{
		JSONParseStatus: gatewaybody.JSONParseStatusInvalidJSON,
	})
}

// openAIRequest mirrors a /v1 gateway request.
func openAIRequest() *GatewayRequest {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return &GatewayRequest{HTTP: req, RemoteAddr: "203.0.113.9:44556", ClientIP: "203.0.113.9"}
}

// anthropicNativeRequest mirrors a native anthropic messages request.
func anthropicNativeRequest() *GatewayRequest {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	return &GatewayRequest{HTTP: req, RemoteAddr: "203.0.113.9:44556", ClientIP: "203.0.113.9"}
}

func assertContains(t *testing.T, body string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			t.Fatalf("响应体缺少 %q: %s", needle, body)
		}
	}
}

var _ = gatewayproto.LaneText
var _ = gatewayrouting.RouteStrategyModeNormal
