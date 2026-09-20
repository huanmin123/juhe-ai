package gatewayusage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type stubModelResolver struct {
	resolution UsageModelResolution
	lastModel  string
	lastFamily string
}

func (s *stubModelResolver) ResolveUsageModel(account UsageModelAccount, requestedModel string, sourceEndpointFamily string) UsageModelResolution {
	s.lastModel = requestedModel
	s.lastFamily = sourceEndpointFamily
	return s.resolution
}

type stubSemantics struct{ semantic string }

func (s *stubSemantics) UsageSemanticForProfile(*ProviderProtocolProfile) string { return s.semantic }

type stubDefaultProvider struct{ code string }

func (s *stubDefaultProvider) DefaultUsageProviderCode() string { return s.code }

type stubPricing struct {
	model          string
	cost           float64
	cacheReadCost  float64
	cacheWriteCost float64
}

func (s *stubPricing) ResolvePricingModel(providerCode string, systemAccountID string, model string) string {
	return s.model
}

func (s *stubPricing) EstimateCost(input PricingCostInput) *float64 {
	value := s.cost
	return &value
}

func (s *stubPricing) EstimateCacheReadCost(input PricingCostInput) *float64 {
	value := s.cacheReadCost
	return &value
}

func (s *stubPricing) EstimateCacheWriteCost(input PricingCostInput) *float64 {
	value := s.cacheWriteCost
	return &value
}

type stubMetrics struct {
	failureClass string
	statusCode   *int
	reason       string
	calls        int
}

func (s *stubMetrics) RecordUpstreamFailure(failureClass string, statusCode *int, reasonClass string) {
	s.calls++
	s.failureClass = failureClass
	s.statusCode = statusCode
	s.reason = reasonClass
}

type stubProtocolErrors struct {
	payload  any
	lastBody string
	called   bool
}

func (s *stubProtocolErrors) ParseProtocolErrorPayload(account UsageModelAccount, bodyText string, headers map[string]any) any {
	s.called = true
	s.lastBody = bodyText
	return s.payload
}

type captureLogger struct {
	warns  []capturedLog
	debugs []capturedLog
}

type capturedLog struct {
	message string
	fields  map[string]any
}

func (l *captureLogger) Debug(msg string, fields map[string]any) {
	l.debugs = append(l.debugs, capturedLog{msg, fields})
}

func (l *captureLogger) Warn(msg string, fields map[string]any) {
	l.warns = append(l.warns, capturedLog{msg, fields})
}

func (l *captureLogger) Error(msg string, fields map[string]any) {}

type testHarness struct {
	service   *Service
	recorder  *MemoryUsageRecorder
	models    *stubModelResolver
	pricing   *stubPricing
	metrics   *stubMetrics
	logger    *captureLogger
	idFactory *countingIDFactory
	protocol  *stubProtocolErrors
	clock     fixedClock
}

func newHarness(config ServiceConfig) *testHarness {
	recorder := NewMemoryUsageRecorder(nil, nil)
	idFactory := &countingIDFactory{}
	recorder.idFactory = idFactory
	harness := &testHarness{
		recorder:  recorder,
		models:    &stubModelResolver{resolution: UsageModelResolution{UpstreamModel: "gpt-x", ModelMappingApplied: true, ModelMappingSource: "account_mapping", UpstreamEndpointFamily: "chat_completions"}},
		pricing:   &stubPricing{model: "gpt-x-catalog", cost: 0.02, cacheReadCost: 0.001, cacheWriteCost: 0.002},
		metrics:   &stubMetrics{},
		logger:    &captureLogger{},
		idFactory: idFactory,
		protocol:  &stubProtocolErrors{payload: protocolErrorPayload("insufficient_quota", "upstream boom")},
		clock:     fixedClock{ms: 1700000000000},
	}
	dispatch := NewFinalizationDispatch(recorder, nil, 8, 2)
	dispatch.clock = harness.clock
	service := NewService(dispatch, config)
	service.WithClock(harness.clock)
	service.WithLogger(harness.logger)
	service.WithModelResolver(harness.models)
	service.WithUsageSemantics(&stubSemantics{semantic: "openai"})
	service.WithDefaultProviderCode(&stubDefaultProvider{code: "gpt"})
	service.WithPricingCatalog(harness.pricing)
	service.WithMetrics(harness.metrics)
	service.WithProtocolErrorParser(harness.protocol)
	harness.service = service
	return harness
}

func protocolErrorPayload(code string, message string) *OrderedObject {
	// parseGatewayProtocolErrorPayload returns the flat protocol error
	// object: {code?, type?, message?, ...}.
	payload := NewOrderedObject()
	payload.Set("code", code)
	payload.Set("message", message)
	return payload
}

func testAccount() UsageModelAccount {
	return UsageModelAccount{
		ID:                        "acc-1",
		Name:                      "账号一",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile-1",
		ProxyURL:                  "http://user:pass@proxy:8080",
		UsageAccess: UsageAccessFields{
			AccountOwnerSystemAccountID:      "sys-owner",
			GroupOwnerSystemAccountID:        "sys-group",
			AccountAccessType:                AccountAccessTypeAccountAuthorized,
			GroupAccessType:                  GroupAccessTypeAuthorized,
			AccountAuthorizationID:           "authz-1",
			AccountAuthorizationSourceType:   AuthorizationSourceTypeTeam,
			AccountAuthorizationSourceTeamID: "team-1",
			GroupAuthorizationID:             "group-authz-1",
			GroupAuthorizationSourceType:     AuthorizationSourceTypeManual,
		},
	}
}

func usageContext() GatewayUsageContext {
	return GatewayUsageContext{
		TraceID:                  "trace-1",
		TrafficSource:            TrafficSourceGateway,
		ClientIP:                 "1.2.3.4",
		SystemAccountID:          "sys-owner",
		APIKeyID:                 "key-1",
		GroupID:                  "group-1",
		Endpoint:                 "POST /v1/chat/completions",
		RequestSnapshot:          BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{Method: "POST", Path: "/v1/chat/completions", OriginalURL: "/v1/chat/completions", TraceID: "trace-1"}),
		RequestedServiceTier:     "flex",
		EffectiveServiceTier:     "flex",
		RequestedReasoningEffort: "high",
		EffectiveReasoningEffort: "high",
	}
}

func TestRecordFailedUpstreamAttempt(t *testing.T) {
	harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
	status := 502
	attempt := usageContext()
	err := harness.service.RecordFailedUpstreamAttempt(context.Background(), attempt, testAccount(), RecordFailedUpstreamAttemptInput{
		Model:                "gpt-requested",
		Stream:               true,
		SourceEndpointFamily: "chat_completions",
		UpstreamURL:          "https://upstream.example.com/v1",
		StartedAtMs:          1700000000000 - 250,
		StatusCode:           &status,
		Headers:              map[string]any{"content-type": "application/json"},
		BodyText:             `{"error":{"message":"upstream boom","code":"insufficient_quota"}}`,
		ErrorMessage:         "",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if harness.metrics.calls != 1 || harness.metrics.failureClass != FailureClassOpaqueUpstreamResponse || harness.metrics.reason != MetricReasonQuota {
		t.Fatalf("metrics = %+v", harness.metrics)
	}
	if !harness.protocol.called || harness.protocol.lastBody != `{"error":{"message":"upstream boom","code":"insufficient_quota"}}` {
		t.Fatalf("protocol parser = %+v", harness.protocol)
	}
	if len(harness.logger.warns) != 1 {
		t.Fatalf("warns = %d", len(harness.logger.warns))
	}
	warn := harness.logger.warns[0]
	if warn.message != "网关上游尝试失败" || warn.fields["event"] != "gateway_upstream_attempt_failed" {
		t.Fatalf("log = %+v", warn)
	}
	if warn.fields["upstreamUrl"] != "https://upstream.example.com/v1" {
		t.Fatalf("upstreamUrl = %v", warn.fields["upstreamUrl"])
	}
	if !harness.service.dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	record, _ := harness.recorder.LastRecord()
	if record.Success {
		t.Fatal("failure record must not succeed")
	}
	if record.ErrorCode != "insufficient_quota" {
		t.Fatalf("errorCode = %q", record.ErrorCode)
	}
	if record.ErrorMessage != "upstream boom" {
		t.Fatalf("errorMessage = %q", record.ErrorMessage)
	}
	if record.FailureAttribution != FailureAttributionAccountUpstream {
		t.Fatalf("attribution = %q", record.FailureAttribution)
	}
	if record.ResponseSnapshot == nil {
		t.Fatal("response snapshot expected")
	}
}

func TestRecordFailedUpstreamAttemptProbeDropsSnapshotsAndDebugs(t *testing.T) {
	harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
	attempt := usageContext()
	attempt.TrafficSource = TrafficSourceAccountHealthCheck
	if err := harness.service.RecordFailedUpstreamAttempt(context.Background(), attempt, testAccount(), RecordFailedUpstreamAttemptInput{
		Model:       "gpt-requested",
		UpstreamURL: "concurrency:limit",
		StartedAtMs: 1700000000000 - 10,
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(harness.logger.debugs) != 1 || len(harness.logger.warns) != 0 {
		t.Fatalf("probe traffic must log debug only: %+v", harness.logger)
	}
	if !harness.service.dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	record, _ := harness.recorder.LastRecord()
	if record.RequestSnapshot != nil || record.ResponseSnapshot != nil {
		t.Fatalf("probe must drop snapshots: %+v", record)
	}
	if record.FailureAttribution != FailureAttributionGatewayCapacity {
		t.Fatalf("attribution = %q", record.FailureAttribution)
	}
	if record.ErrorCode != "" || record.ErrorMessage != "" {
		t.Fatalf("unexpected error fields: %+v", record)
	}
}

func TestRecordGatewayFailure(t *testing.T) {
	harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
	payload := NewOrderedObject()
	errorObject := NewOrderedObject()
	errorObject.Set("message", "策略拒绝")
	errorObject.Set("code", "policy_rejected")
	payload.Set("error", errorObject)
	status := 403
	err := harness.service.RecordGatewayFailure(context.Background(), GatewayFailureUsageContext{
		GatewayUsageContext: GatewayUsageContext{
			TraceID:              "trace-1",
			TrafficSource:        TrafficSourceGateway,
			SystemAccountID:      "sys-owner",
			GroupID:              "group-1",
			Endpoint:             "POST /v1/chat/completions",
			RequestedServiceTier: "flex",
		},
		GroupOwnerSystemAccountID: "sys-group",
		GroupAccessType:           GroupAccessTypeOwner,
	}, RecordGatewayFailureInput{
		Model:           "gpt-requested",
		Stream:          true,
		StatusCode:      status,
		StartedAtMs:     1700000000000 - 30,
		CompletedAtMs:   1700000000000,
		ResponsePayload: payload,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(harness.logger.warns) != 1 || harness.logger.warns[0].message != "网关请求失败" {
		t.Fatalf("warns = %+v", harness.logger.warns)
	}
	if !harness.service.dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	record, _ := harness.recorder.LastRecord()
	if record.ProviderCode != "gpt" {
		t.Fatalf("default provider code = %q", record.ProviderCode)
	}
	if record.ErrorCode != "policy_rejected" || record.ErrorMessage != "策略拒绝" {
		t.Fatalf("error fields = %q/%q", record.ErrorCode, record.ErrorMessage)
	}
	if record.FailureAttribution != FailureAttributionGatewayPolicy {
		t.Fatalf("attribution = %q", record.FailureAttribution)
	}
	if record.GroupID != "group-1" {
		t.Fatalf("group preserved = %q", record.GroupID)
	}
	if record.CreatedAt != "2023-11-14T22:13:20.000Z" {
		t.Fatalf("createdAt = %q", record.CreatedAt)
	}
	if record.UsageSemantic != "openai" {
		t.Fatalf("usageSemantic = %q", record.UsageSemantic)
	}
	responseSnapshot, ok := record.ResponseSnapshot.(*OrderedObject)
	if !ok || responseSnapshot.Get("generatedBy") != GeneratedByGateway || responseSnapshot.Get("errorMessage") != "策略拒绝" {
		t.Fatalf("response snapshot = %+v", record.ResponseSnapshot)
	}
}

func TestRecordGatewayFailureOmitsUnresolvedGroupScope(t *testing.T) {
	harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
	err := harness.service.RecordGatewayFailure(context.Background(), GatewayFailureUsageContext{
		GatewayUsageContext: GatewayUsageContext{
			TraceID:       "trace-1",
			TrafficSource: TrafficSourceGateway,
			GroupID:       "group-1",
		},
	}, RecordGatewayFailureInput{
		Model:       "gpt-requested",
		StatusCode:  429,
		StartedAtMs: 1700000000000 - 5,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	foundOmissionWarning := false
	for _, warn := range harness.logger.warns {
		if warn.message == "网关失败 usage 缺少分组归属快照，已省略分组统计维度" {
			foundOmissionWarning = true
		}
	}
	if !foundOmissionWarning {
		t.Fatalf("expected omission warning, got %+v", harness.logger.warns)
	}
	if !harness.service.dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	record, _ := harness.recorder.LastRecord()
	if record.GroupID != "" || record.GroupOwnerSystemAccountID != "" {
		t.Fatalf("group scope must be omitted: %+v", record)
	}
	if record.AccountID != "" {
		t.Fatalf("no account expected: %+v", record)
	}
}

func TestDispatchOverflowSpoolsInsteadOfBlocking(t *testing.T) {
	// The gated recorder keeps the first task active so queue occupancy is
	// deterministic (Node capacity counts queued entries only).
	gate := &gatedRecorder{entered: make(chan struct{}, 8), release: make(chan struct{})}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:  t.TempDir(),
		InstanceID: "inst-1",
		MaxItems:   100,
		MaxBytes:   1024 * 1024,
		Enabled:    true,
	}, fixedClock{ms: 1700000000000}, nil)
	overflow := &memoryOverflow{spool: spool}
	dispatch := NewFinalizationDispatch(gate, overflow, 1, 1)
	dispatch.OverflowEnabled = true
	if err := dispatch.DispatchUsageRecord(context.Background(), completedRecord("trace-a"), 100); err != nil {
		t.Fatalf("err = %v", err)
	}
	<-gate.entered // task-a is active.
	if err := dispatch.DispatchUsageRecord(context.Background(), completedRecord("trace-b"), 100); err != nil {
		t.Fatalf("err = %v", err)
	}
	// The queue is full (maxItems=1) → trace-c spools without blocking.
	if err := dispatch.DispatchUsageRecord(context.Background(), completedRecord("trace-c"), 100); err != nil {
		t.Fatalf("err = %v", err)
	}
	runtime := dispatch.Runtime()
	if runtime.OverflowSpoolCount != 1 {
		t.Fatalf("overflow count = %d runtime %+v", runtime.OverflowSpoolCount, runtime)
	}
	close(gate.release)
	if !dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	// WaitForIdle only accounts queued/active/waiters; the overflow factory
	// goroutine (queue-tracked only) can still be inside spool.Persist. Wait
	// for the persist counter to settle — observable only after the final
	// rename, which is Persist's last filesystem operation — so returning
	// here can never race t.TempDir's RemoveAll with a straggler writer
	// (full-suite concurrency, -race).
	waitForSpoolSettled(t, spool, 1)
	spooled := overflow.snapshot()
	if len(spooled) != 1 || spooled[0].TraceID != "trace-c" {
		t.Fatalf("spooled = %+v", spooled)
	}
	if !gate.seenTrace("trace-a") || !gate.seenTrace("trace-b") {
		t.Fatalf("queued tasks must still deliver: %v", gate.seenSnapshot())
	}
	if gate.seenTrace("trace-c") {
		t.Fatal("spooled record must not double-deliver")
	}
}

// gatedRecorder is a UsageRecorder port mock with controllable delivery.
type gatedRecorder struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	seen    map[string]bool
}

func (g *gatedRecorder) EnqueueUsageRecord(ctx Ctx, input UsageRecordInput) error {
	g.entered <- struct{}{}
	<-g.release
	g.mu.Lock()
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	g.seen[input.TraceID] = true
	g.mu.Unlock()
	return nil
}

func (g *gatedRecorder) seenTrace(trace string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seen[trace]
}

func (g *gatedRecorder) seenSnapshot() map[string]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]bool, len(g.seen))
	for key, value := range g.seen {
		out[key] = value
	}
	return out
}

func completedRecord(trace string) UsageRecordInput {
	return UsageRecordInput{TraceID: trace, TrafficSource: TrafficSourceGateway, Success: true}
}

func TestRecorderPortFailureDoesNotBlockDispatch(t *testing.T) {
	recorder := NewMemoryUsageRecorder(nil, nil)
	recorder.SetFailures(1)
	dispatch := NewFinalizationDispatch(recorder, nil, 4, 1)
	service := NewService(dispatch, ServiceConfig{})
	service.WithClock(fixedClock{ms: 1700000000000})
	attempt := usageContext()
	failedInput := RecordFailedUpstreamAttemptInput{
		Model:       "gpt-requested",
		UpstreamURL: "https://upstream.example.com/v1",
		StartedAtMs: 1700000000000 - 10,
	}
	if err := service.RecordFailedUpstreamAttempt(context.Background(), attempt, testAccount(), failedInput); err != nil {
		t.Fatalf("dispatch must not surface recorder failures: %v", err)
	}
	if !dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	if len(recorder.Records()) != 0 {
		t.Fatal("failed record must not be retained")
	}
	// Recovery: next record goes through.
	if err := service.RecordFailedUpstreamAttempt(context.Background(), attempt, testAccount(), failedInput); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !dispatch.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	if len(recorder.Records()) != 1 {
		t.Fatalf("records = %d", len(recorder.Records()))
	}
}

func TestFinalizationQueueCapacityWaitAndRuntime(t *testing.T) {
	queue := NewGatewayUsageFinalizationQueue(2, 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := queue.Dispatch(context.Background(), func(Ctx) error {
		started <- struct{}{}
		<-release
		return nil
	}, 100, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	<-started
	// Two more admissions fill the queue (maxItems=2; active tasks do not
	// count against admission, mirroring Node hasCapacity).
	for index := 0; index < 2; index++ {
		if err := queue.Dispatch(context.Background(), func(Ctx) error { return nil }, 100, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	}
	runtime := queue.Runtime()
	if runtime.QueuedCount != 2 || runtime.ActiveCount != 1 {
		t.Fatalf("runtime = %+v", runtime)
	}
	// The next admission exceeds maxItems with no overflow → blocks.
	admitted := make(chan error, 1)
	go func() {
		admitted <- queue.Dispatch(context.Background(), func(Ctx) error { return nil }, 100, nil)
	}()
	select {
	case err := <-admitted:
		t.Fatalf("admission should block, got %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-admitted; err != nil {
		t.Fatalf("admission err = %v", err)
	}
	if !queue.WaitForIdle(2000) {
		t.Fatal("queue must go idle")
	}
	runtime = queue.Runtime()
	if runtime.MaxItems != 2 || runtime.MaxConcurrency != 1 || runtime.DroppedCount != 0 {
		t.Fatalf("runtime = %+v", runtime)
	}
}

func TestFinalizationQueueOversizeTaskRejected(t *testing.T) {
	queue := NewGatewayUsageFinalizationQueue(2, 1)
	err := queue.Dispatch(context.Background(), func(Ctx) error { return nil }, gatewayUsageFinalizationTaskMaxBytes+1, nil)
	if err == nil || err.Error() != "网关使用记录异步收尾任务超过单条容量上限" {
		t.Fatalf("err = %v", err)
	}
}

func TestFinalizationQueueErrorSwallowedAndLogged(t *testing.T) {
	logger := &captureLogger{}
	queue := NewGatewayUsageFinalizationQueue(2, 1).WithLogger(logger)
	if err := queue.Dispatch(context.Background(), func(Ctx) error {
		return errors.New("writer down")
	}, 10, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !queue.WaitForIdle(2000) {
		t.Fatal("not idle")
	}
	if len(logger.warns) == 0 {
		t.Fatal("task failure must be logged, not propagated")
	}
}

type memoryOverflow struct {
	mu      sync.Mutex
	spool   *UsageRecordSpool
	spooled []UsageRecordInput
}

func (m *memoryOverflow) PersistOverflow(ctx Ctx, input UsageRecordInput) error {
	m.mu.Lock()
	m.spooled = append(m.spooled, input)
	m.mu.Unlock()
	return m.spool.Persist(ctx, input)
}

func (m *memoryOverflow) snapshot() []UsageRecordInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageRecordInput, len(m.spooled))
	copy(out, m.spooled)
	return out
}

// TestFinalizationDefaultsMirrorNodeConcurrencyGlobalMax 验证收尾队列默认档位：
// maxItems 2048（内存保护保留）与 maxConcurrency 5000（Node
// failure-finalization.service.ts 的 runtimeConfig.concurrency.globalMax）。
func TestFinalizationDefaultsMirrorNodeConcurrencyGlobalMax(t *testing.T) {
	queue := NewGatewayUsageFinalizationQueue(0, 0)
	runtime := queue.Runtime()
	if runtime.MaxItems != 2048 {
		t.Fatalf("default maxItems: %d", runtime.MaxItems)
	}
	if runtime.MaxConcurrency != 5000 {
		t.Fatalf("default maxConcurrency: %d", runtime.MaxConcurrency)
	}
}
