package gatewaydispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func gatewayroutingNewWallBudget(acceptedAt int64) (*gatewayrouting.GatewayRequestWallBudget, error) {
	budgetMs := int64(60_000)
	return gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: acceptedAt,
		BudgetMs:            &budgetMs,
	}, nil)
}

func gatewayDispatchIdentityForTest() gatewayrouting.GatewayDispatchAttemptIdentity {
	return gatewayrouting.GatewayDispatchAttemptIdentity{
		AccountRuntimeKey:     "system-1::a-1",
		PhysicalCredentialKey: "a-1",
		ProtocolModelKey:      `["system-1::a-1","openai","v1","gpt-test"]`,
	}
}

// Dispatch engine tests: fetchFirstAvailableUpstream candidate loop,
// attempt lifecycle, retry classification, budget and cancellation.

// sequentialServer returns a server that fails the first N requests with the
// given status, then succeeds with a chat completion body.
func sequentialServer(t *testing.T, failFirst int, status int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call <= failFirst {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
}

func dispatchArgs(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	return FetchFirstAvailableUpstreamArgs{
		Req:                         req,
		Accounts:                    accounts,
		Settings:                    gatewaySettingsForTest(),
		UsageContext:                testUsageContext(),
		AuditCapture:                AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}},
		Signal:                      context.Background(),
		RequestLane:                 "text",
		AccountStateMutationEnabled: true,
		RequestCoordination:         newTestCoordination(t),
		WaitForRecoverableFailures:  true,
	}
}

func gatewaySettingsForTest() gatewaySettingsType {
	return gatewaySettingsType{
		TemporaryUnschedulableRetryIntervalSeconds: 1,
		TemporaryUnschedulableRetryAttempts:        2,
		AccountCircuitConfirmationFailuresRequired: 2,
		TextFirstResponseTimeoutSeconds:            30,
		TextStreamIdleTimeoutSeconds:               300,
		TextUncommittedAttemptMaxLifetimeSeconds:   60,
		ImageFirstResponseTimeoutSeconds:           60,
		ImageStreamIdleTimeoutSeconds:              600,
		ImageUncommittedAttemptMaxLifetimeSeconds:  120,
		NoAvailableAccountWaitTimeoutSeconds:       10,
	}
}

// gatewaySettingsType avoids importing gatewayruntimecache in the test body.
type gatewaySettingsType = gatewayruntimecache.GatewaySettings

func TestFetchFirstAvailableUpstreamFirstAccountSuccess(t *testing.T) {
	server := sequentialServer(t, 0, 500)
	defer server.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := testAccounts("a-1")
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if !result.Response.OK() {
		t.Fatalf("status = %d", result.Response.Status())
	}
	if result.UpstreamURL != server.URL+"/v1/chat/completions" {
		t.Fatalf("upstream url = %q", result.UpstreamURL)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if result.EffectiveServiceTier != "default" {
		t.Fatalf("service tier = %q", result.EffectiveServiceTier)
	}
	affinity := engine.Affinity.(*fakeAffinity)
	if len(affinity.remembered) != 1 || affinity.remembered[0] != "a-1" {
		t.Fatalf("affinity remembered = %#v", affinity.remembered)
	}
	if engine.Concurrency.(*fakeConcurrencyStore).released.Load() != 0 {
		t.Fatal("the selected attempt keeps its concurrency slot")
	}
	if result.ReleaseConcurrency == nil {
		t.Fatal("releaseConcurrency missing")
	}
	result.ReleaseConcurrency()
	result.ReleaseConcurrency() // idempotent
	if got := engine.Concurrency.(*fakeConcurrencyStore).released.Load(); got != 1 {
		t.Fatalf("release calls = %d", got)
	}
}

func TestFetchFirstAvailableUpstreamFailoverToSecondAccount(t *testing.T) {
	// a-1 gets a 500 (dispatcher answers skip_account), a-2 succeeds.
	engine, driver, dispatcher := newTestEngine(t)
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer failServer.Close()
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()

	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, testAccounts("a-1", "a-2")))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("expected failover to a-2, got %s", result.Account.ID)
	}
	// Node retries a transient 500 on the same account up to
	// maxSameAccountRetries = min(2, temporaryUnschedulableRetryAttempts)
	// before moving on: 1 initial + 2 same-account retries on a-1.
	if dispatcher.handleFailedCalls.Load() != 3 {
		t.Fatalf("failed-response handler calls = %d", dispatcher.handleFailedCalls.Load())
	}
}

func TestFetchFirstAvailableUpstreamAllFailedMessage(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, testAccounts("a-1", "a-2")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if !strings.HasPrefix(attemptErr.Message, "所有上游账户均失败") {
		t.Fatalf("message = %q", attemptErr.Message)
	}
	if !strings.Contains(attemptErr.Message, "返回") {
		t.Fatalf("message lacks last attempt tail: %q", attemptErr.Message)
	}
	if len(attemptErr.FailedAccountIDs) != 2 {
		t.Fatalf("failed accounts = %#v", attemptErr.FailedAccountIDs)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.Status != http.StatusBadGateway {
		t.Fatalf("last attempt = %#v", attemptErr.LastAttempt)
	}

	// Single-account prefix.
	_, err = engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, testAccounts("a-1")))
	if !strings.HasPrefix(err.Error(), "上游账户请求失败") {
		t.Fatalf("single-account message = %q", err.Error())
	}
}

func TestFetchFirstAvailableUpstreamCancelPropagation(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {"https://127.0.0.1:9/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := dispatchArgs(t, req, testAccounts("a-1"))
	args.Signal = ctx
	_, err := engine.FetchFirstAvailableUpstream(ctx, args)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) || aborted.Message != "请求已取消" {
		t.Fatalf("expected cancel abort, got %v", err)
	}
}

func TestFetchFirstAvailableUpstreamRequiresCoordination(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	req := newTestRequest(t, `{"model":"gpt-test"}`)
	args := dispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination = nil
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "requires shared request coordination context") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchFirstAvailableUpstreamWallBudgetAssertion(t *testing.T) {
	server := sequentialServer(t, 0, 500)
	defer server.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, testAccounts("a-1"))
	// Exhaust the wall budget so the pre-attempt assertion fires.
	past := gatewayupstream.NowMs() - 60_000
	wallBudget, err := gatewayroutingNewWallBudget(past)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	args.RequestCoordination.GatewayRequestWallBudget = wallBudget
	_, err = engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) {
		t.Fatalf("expected wall budget error, got %v", err)
	}
	if budgetErr.Error() != "网关请求墙钟预算已进入最终响应预留区" {
		t.Fatalf("message = %q", budgetErr.Error())
	}
}

func TestFetchFirstAvailableUpstreamSuppressionSkipsAccount(t *testing.T) {
	server := sequentialServer(t, 0, 500)
	defer server.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	engine.Suppression = &fakeSuppression{allSuppressed: true}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = false
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:locally_suppressed" {
		t.Fatalf("last attempt = %#v", attemptErr.LastAttempt)
	}
	if !strings.Contains(attemptErr.LastAttempt.Message, "账号处于本地短期屏蔽") {
		t.Fatalf("message = %q", attemptErr.LastAttempt.Message)
	}
}

func TestSameAccountRetryReservationFlow(t *testing.T) {
	// reserveSameAccountRetry rejects an unregistered identity (Node
	// sameAccountRetryNotApplicable) so no retry id is produced.
	coordination := newTestCoordination(t)
	identity := gatewayDispatchIdentityForTest()
	reservation, err := coordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !reservation.Allowed {
		t.Fatalf("first registration must pass: %+v", reservation)
	}
	second, err := coordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if second.Allowed || second.Reason == "" {
		t.Fatalf("duplicate registration must be rejected: %+v", second)
	}
}

func TestBuildDiagnosticUpstreamErrorMessages(t *testing.T) {
	t.Run("timeout classification", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:            "a-1",
			UpstreamURL:          "https://upstream.example/v1",
			Message:              "网关传输失败",
			TransportFailureKind: TransportFailureKindTimeout,
		}, "fallback", nil)
		if diagnostic.StatusCode != 504 {
			t.Fatalf("status = %d", diagnostic.StatusCode)
		}
		if diagnostic.ErrorMessage != "网关传输失败" {
			t.Fatalf("message = %q", diagnostic.ErrorMessage)
		}
		if diagnostic.Payload.Error.Type != "upstream_timeout_error" || diagnostic.Payload.Error.Code != "upstream_timeout" {
			t.Fatalf("payload = %#v", diagnostic.Payload.Error)
		}
	})
	t.Run("upstream error object preserved", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:        "a-1",
			UpstreamURL:      "https://upstream.example/v1",
			Status:           429,
			HasStatus:        true,
			ResponseBodyText: `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
		}, "fallback", nil)
		if diagnostic.StatusCode != 429 {
			t.Fatalf("status = %d", diagnostic.StatusCode)
		}
		if diagnostic.Payload.Error.Message != "Rate limit reached" || diagnostic.Payload.Error.Code != "rate_limit_exceeded" {
			t.Fatalf("payload = %#v", diagnostic.Payload.Error)
		}
		if !diagnostic.PreserveUpstreamMessage {
			t.Fatal("upstream message must be preserved")
		}
	})
	t.Run("no attempt", func(t *testing.T) {
		if BuildDiagnosticUpstreamError(nil, "fallback", nil) != nil {
			t.Fatal("nil attempt yields nil diagnostic")
		}
	})
}

func TestCircuitTransportFailureClassification(t *testing.T) {
	timeout := circuitTransportFailure(&UpstreamRequestTimeoutError{Message: "ETIMEDOUT 上游请求超时"}, "")
	if timeout.kind != TransportFailureKindTimeout {
		t.Fatalf("kind = %q", timeout.kind)
	}
	connection := circuitTransportFailure(context.DeadlineExceeded, "连接被重置")
	if connection.kind != "transport" || connection.reason != "连接被重置" {
		t.Fatalf("failure = %#v", connection)
	}
	fallback := circuitTransportFailure(nil, "")
	if fallback.reason != "上游传输失败" {
		t.Fatalf("fallback reason = %q", fallback.reason)
	}
}

func TestCircuitEvidenceKeyPrefersSessionThenIP(t *testing.T) {
	// With a session identity the session branch wins even when an IP exists.
	sessionEngine, _, _ := newTestEngine(t)
	sessionEngine.SessionIdentity = func(req *gatewaypreauth.GatewayRequest) SessionIdentityView {
		return SessionIdentityView{SessionID: "session-1"}
	}
	usage := testUsageContext()
	usageWithIP := usage
	usageWithIP.ClientIP = "203.0.113.5"
	withSession := sessionEngine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), usageWithIP)

	// Without a session identity the client IP isolates the evidence.
	ipEngine, _, _ := newTestEngine(t)
	withIP := ipEngine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), usageWithIP)

	// Without any client address callers aggregate across keys.
	unknown := ipEngine.gatewayForegroundAccountCircuitFailureEvidenceKey(newTestRequest(t, `{}`), testUsageContext())

	if withSession == withIP || withIP == unknown || withSession == unknown {
		t.Fatalf("evidence keys must differ: %s %s %s", withSession, withIP, unknown)
	}
	if len(withSession) != 64 {
		t.Fatalf("digest length = %d", len(withSession))
	}
}

func TestIsTransientSameAccountHttpStatus(t *testing.T) {
	cases := map[int]bool{
		408: true, 425: true, 429: true, 500: true, 599: true,
		400: false, 401: false, 404: false, 200: false, 600: false,
	}
	for status, want := range cases {
		if got := isTransientSameAccountHttpStatus(status); got != want {
			t.Fatalf("status %d: got %v want %v", status, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 通用调度排除集 + 分派段状态机（调度内核通用化设计 5.1，批次 1）：原 Codex
// turn 避让整体过滤 + last-resort 翻回两段画像分支的改写用例。段内让位、段
// 耗尽后翻回与逐段推进语义的矩阵回归见 dispatchsegments_test.go。
// ---------------------------------------------------------------------------

func schedulingDispatchArgs(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, audit *frozenAudit, excluded []string, segments []DispatchSegment) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	args := dispatchArgs(t, req, accounts)
	args.AuditCapture = AuditCapture{Context: audit, Sink: audit.sink}
	if excluded != nil {
		args.SchedulingExclusions = &SchedulingExclusions{ExcludedAccountIDs: excluded}
	}
	args.DispatchSegments = segments
	return args
}

// TestFetchFirstAvailableUpstreamExclusionYieldsInSegment: 排除集只做段内让位
// ——同段普通候选可用时被排除账号不派发（原 CodexTurnAvoidanceFilter 用例的
// 通用化改写，行为等价）。
func TestFetchFirstAvailableUpstreamExclusionYieldsInSegment(t *testing.T) {
	excludedHits := 0
	var mu sync.Mutex
	excludedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		excludedHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-excluded"}`))
	}))
	defer excludedUpstream.Close()
	freshUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fresh"}`))
	}))
	defer freshUpstream.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {excludedUpstream.URL + "/v1/chat/completions"},
		"a-2": {freshUpstream.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	accounts := testAccounts("a-1", "a-2")
	segments := []DispatchSegment{{
		OpaqueSegmentID: "prep-seg-1",
		Tier:            dispatchPriorityTierOf(accounts[0], nil),
		Accounts:        accounts,
	}}
	args := schedulingDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("fresh account must be dispatched, got %s", result.Account.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if excludedHits != 0 {
		t.Fatalf("被排除账号在普通候选成功时必须零尝试，hits = %d", excludedHits)
	}
	for _, label := range audit.metadata {
		if label == "gateway_dispatch_exclusion_release" {
			t.Fatalf("普通候选成功时不得触发释放: %v", audit.metadata)
		}
	}
}

// TestFetchFirstAvailableUpstreamExclusionReleaseAfterExhaustion: 段内普通候选
// 真实耗尽后释放被排除账号并按原顺序尝试（原 CodexTurnLastResortReversal 用例
// 的通用化改写），审计为 gateway_dispatch_exclusion_release。
func TestFetchFirstAvailableUpstreamExclusionReleaseAfterExhaustion(t *testing.T) {
	var mu sync.Mutex
	freshHits, excludedHits := 0, 0
	freshUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		freshHits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer freshUpstream.Close()
	excludedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		excludedHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-excluded-ok"}`))
	}))
	defer excludedUpstream.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {freshUpstream.URL + "/v1/chat/completions"},
		"a-2": {excludedUpstream.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	accounts := testAccounts("a-1", "a-2")
	segments := []DispatchSegment{{
		OpaqueSegmentID: "prep-seg-1",
		Tier:            dispatchPriorityTierOf(accounts[0], nil),
		Accounts:        accounts,
	}}
	args := schedulingDispatchArgs(t, req, accounts, audit, []string{"a-2"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("被排除账号必须在普通候选耗尽后释放并服务, got %s", result.Account.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	// 尝试顺序：普通候选先打并在失败后耗尽，被排除账号在其后恰好尝试一次。
	if excludedHits != 1 || freshHits < 1 {
		t.Fatalf("hits: fresh=%d excluded=%d（被排除账号不得先于普通候选尝试）", freshHits, excludedHits)
	}
	releaseAudited := false
	for _, label := range audit.metadata {
		if label == "gateway_dispatch_exclusion_release" {
			releaseAudited = true
		}
	}
	if !releaseAudited {
		t.Fatalf("释放必须审计为 gateway_dispatch_exclusion_release: %v", audit.metadata)
	}
}

// TestFetchFirstAvailableUpstreamExclusionEntryRelease: 段入口即无普通候选时
// 立即释放（原 CodexTurnEntryReversal 用例的通用化改写），审计为
// gateway_dispatch_exclusion_release。
func TestFetchFirstAvailableUpstreamExclusionEntryRelease(t *testing.T) {
	excludedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-excluded-entry"}`))
	}))
	defer excludedUpstream.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {excludedUpstream.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	accounts := testAccounts("a-1")
	segments := []DispatchSegment{{
		OpaqueSegmentID: "prep-seg-1",
		Tier:            dispatchPriorityTierOf(accounts[0], nil),
		Accounts:        accounts,
	}}
	args := schedulingDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("段入口无普通候选必须立即释放并服务, got %s", result.Account.ID)
	}
	releaseAudited := false
	for _, label := range audit.metadata {
		if label == "gateway_dispatch_exclusion_release" {
			releaseAudited = true
		}
	}
	if !releaseAudited {
		t.Fatalf("入口释放必须审计: %v", audit.metadata)
	}
}
