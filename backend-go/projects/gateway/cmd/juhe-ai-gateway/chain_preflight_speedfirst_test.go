package main

// Speed-first body admission gate tests (chain_preflight.go, the server.ts
// admitSpeedFirstRequestBody adapter over the G13b gatewayhotquality
// registry) plus the image-permission downgrade warn-log regression.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// test collaborators
// ---------------------------------------------------------------------------

// chainPreauthStubRuntimeCache satisfies gatewaypreauth.RuntimeCacheReader
// without touching a database (the gate only reads the Observability seam).
type chainPreauthStubRuntimeCache struct{}

func (chainPreauthStubRuntimeCache) ReadCachedGatewayRuntimeAsync(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}
func (chainPreauthStubRuntimeCache) ReadCachedGatewaySettingsAsync(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}
func (chainPreauthStubRuntimeCache) ResolveCachedGroupUsageAccessMetadataAsync(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListFreshOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedActiveResponseInspectionPoliciesForAccountsAsync(context.Context, []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedProviderModelCatalogAsync(context.Context, gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	return nil, nil
}

type capturedSpeedFirstStage struct {
	stage   string
	fields  map[string]any
	outcome string
}

type capturedWarnLine struct {
	event   string
	fields  map[string]any
	message string
}

// chainCapturedObservability records the request-stage lines and warn events
// the gate and the image preflight emit.
type chainCapturedObservability struct {
	// mu guards the captured slices: the speed-first admission gate logs from
	// the queued goroutine and the test goroutine concurrently.
	mu     sync.Mutex
	stages []capturedSpeedFirstStage
	warns  []capturedWarnLine
}

func (o *chainCapturedObservability) Logger() gatewaypreauth.Logger { return o }
func (o *chainCapturedObservability) Warn(event string, fields map[string]any, message string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.warns = append(o.warns, capturedWarnLine{event: event, fields: fields, message: message})
}
func (o *chainCapturedObservability) TraceID() string { return "trace_chain_test" }
func (o *chainCapturedObservability) CreateTraceID() string {
	return "trace_chain_test"
}
func (o *chainCapturedObservability) SanitizeURLForLog(value string) string { return value }
func (o *chainCapturedObservability) LogRequestStage(stage string, fields map[string]any, outcome string, _ time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stages = append(o.stages, capturedSpeedFirstStage{stage: stage, fields: fields, outcome: outcome})
}

// snapshotStages copies the captured stages under the lock: assertions may
// run while an admitted request still logs from its queue goroutine.
func (o *chainCapturedObservability) snapshotStages() []capturedSpeedFirstStage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]capturedSpeedFirstStage(nil), o.stages...)
}

func newChainSpeedFirstGateForTest(obs *chainCapturedObservability) *chainSpeedFirstBodyAdmissionGate {
	service, err := gatewaypreauth.New(gatewaypreauth.Service{
		RuntimeCache:  chainPreauthStubRuntimeCache{},
		Observability: obs,
		Clock:         gatewaypreauth.SystemClock{},
	})
	if err != nil {
		panic(err)
	}
	return &chainSpeedFirstBodyAdmissionGate{preauth: service}
}

// speedFirstRuntime builds the resolved runtime snapshot of one speed_first
// key on a high_concurrency group.
func speedFirstRuntime(mode, preference, groupType *string, policy gatewayruntimecache.GroupSchedulingPolicy, accounts int) *gatewayruntimecache.GatewayRuntime {
	groupTypeValue := "high_concurrency"
	if groupType != nil {
		groupTypeValue = *groupType
	}
	modeValue := "normal"
	if mode != nil {
		modeValue = *mode
	}
	preferenceValue := "speed_first"
	if preference != nil {
		preferenceValue = *preference
	}
	var policyPtr *gatewayruntimecache.GroupSchedulingPolicy
	if policy != nil {
		policyPtr = &policy
	}
	accountList := make([]gatewayruntimecache.OpenAIAccountSecret, 0, accounts)
	for index := 0; index < accounts; index++ {
		accountList = append(accountList, gatewayruntimecache.OpenAIAccountSecret{
			ID:               "acc_speed_" + string(rune('a'+index)),
			ConcurrencyLimit: 1,
		})
	}
	return &gatewayruntimecache.GatewayRuntime{
		APIKey: &gatewayruntimecache.GatewayAPIKeyRow{
			ID:                                  "key_speed",
			SystemAccountID:                     "sys_owner",
			RouteStrategyID:                     "rs_speed",
			RouteStrategyMode:                   modeValue,
			SelectedGroupID:                     "group_speed",
			NormalRoutingConfig:                 &gatewayruntimecache.RouteStrategyNormalRoutingConfig{SchedulingPreference: preferenceValue},
			SystemAccountImageGenerationEnabled: 1,
		},
		GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
			GroupType:        &groupTypeValue,
			SchedulingPolicy: policyPtr,
		},
		Accounts: accountList,
	}
}

func speedFirstRequest(t *testing.T, runtime *gatewayruntimecache.GatewayRuntime) (*gatewaypreauth.GatewayRequest, *gatewaypreauth.TrackingWriter, *httptest.ResponseRecorder) {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Runtime = runtime
	recorder := httptest.NewRecorder()
	return req, gatewaypreauth.NewTrackingWriter(recorder), recorder
}

// ---------------------------------------------------------------------------
// gate behaviour
// ---------------------------------------------------------------------------

func TestChainSpeedFirstBodyAdmissionGateSkipsNonApplicable(t *testing.T) {
	personal := "personal"
	dynamicMode := "round_robin"
	costFirst := "cost_first"
	cases := []struct {
		name    string
		runtime *gatewayruntimecache.GatewayRuntime
		lane    gatewayproto.RequestLane
	}{
		{"missing runtime", nil, gatewayproto.LaneText},
		{"dynamic route strategy", speedFirstRuntime(&dynamicMode, nil, nil, nil, 1), gatewayproto.LaneText},
		{"cost_first preference", speedFirstRuntime(nil, &costFirst, nil, nil, 1), gatewayproto.LaneText},
		{"personal group", speedFirstRuntime(nil, nil, &personal, nil, 1), gatewayproto.LaneText},
		{"no accounts", speedFirstRuntime(nil, nil, nil, nil, 0), gatewayproto.LaneText},
		{"image lane", speedFirstRuntime(nil, nil, nil, nil, 1), gatewayproto.LaneImage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
			defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
			obs := &chainCapturedObservability{}
			gate := newChainSpeedFirstGateForTest(obs)
			req, res, recorder := speedFirstRequest(t, testCase.runtime)

			outcome, err := gate.AdmitBody(context.Background(), req, res, testCase.lane)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if outcome.Handled || outcome.Release != nil {
				t.Fatalf("outcome = %+v, want a plain pass-through", outcome)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("pass-through must not write a response, status=%d", recorder.Code)
			}
			found := false
			for _, stage := range obs.snapshotStages() {
				if stage.stage == "body.speed_first_admission" && stage.outcome == "skipped" {
					found = true
					if stage.fields["admissionMode"] != "speed_first_high_concurrency" || stage.fields["applicable"] != false {
						t.Fatalf("skipped fields = %+v", stage.fields)
					}
				}
			}
			if !found {
				t.Fatalf("missing body.speed_first_admission skipped stage: %+v", obs.snapshotStages())
			}
		})
	}
}

func TestChainSpeedFirstBodyAdmissionGateAcquiresAndReleases(t *testing.T) {
	gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	obs := &chainCapturedObservability{}
	gate := newChainSpeedFirstGateForTest(obs)
	req, res, recorder := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, nil, 2))

	outcome, err := gate.AdmitBody(context.Background(), req, res, gatewayproto.LaneText)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if outcome.Handled || outcome.Release == nil {
		t.Fatalf("outcome = %+v, want an admitted lease", outcome)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("admission must not write a response, status=%d", recorder.Code)
	}
	snapshot := gatewayhotquality.SpeedFirstBodyAdmissionSnapshot()
	if len(snapshot) != 1 || snapshot[0].Active != 1 {
		t.Fatalf("snapshot = %+v, want one active lease", snapshot)
	}
	success := false
	for _, stage := range obs.snapshotStages() {
		if stage.outcome == "success" && stage.fields["acquired"] == true && stage.fields["capacity"] == 2 {
			success = true
		}
	}
	if !success {
		t.Fatalf("missing success stage: %+v", obs.snapshotStages())
	}

	// Release is idempotent (Node res finish/close listeners both firing).
	outcome.Release()
	outcome.Release()
	snapshot = gatewayhotquality.SpeedFirstBodyAdmissionSnapshot()
	if len(snapshot) != 0 {
		t.Fatalf("snapshot after release = %+v, want the state cleaned up", snapshot)
	}
}

// speedFirstBusyJSON is the byte-exact 429 contract (Node gatewayErrorPayload
// key order message, type; no code).
const speedFirstBusyJSON = `{"error":{"message":"当前分组繁忙，请稍后重试或增加可用账户。","type":"rate_limit_error"}}`

func chainSpeedFirstPolicy(waitMs int64, queueSize int) gatewayruntimecache.GroupSchedulingPolicy {
	return gatewayruntimecache.GroupSchedulingPolicy{
		"maxQueueWaitMs":      float64(waitMs),
		"maxQueueSize":        float64(queueSize),
		"perApiKeyQueueLimit": float64(queueSize),
	}
}

func TestChainSpeedFirstBodyAdmissionGateRejectsBusyContract(t *testing.T) {
	gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	obs := &chainCapturedObservability{}
	gate := newChainSpeedFirstGateForTest(obs)
	policy := chainSpeedFirstPolicy(500, 1)

	// Request A takes the only slot.
	reqA, resA, _ := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))
	outcomeA, err := gate.AdmitBody(context.Background(), reqA, resA, gatewayproto.LaneText)
	if err != nil || outcomeA.Handled || outcomeA.Release == nil {
		t.Fatalf("admit A: outcome=%+v err=%v", outcomeA, err)
	}
	defer outcomeA.Release()

	// Request B queues behind A (the only queue slot).
	reqB, resB, recorderB := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))
	doneB := make(chan chainSpeedFirstBodyAdmissionOutcome, 1)
	errB := make(chan error, 1)
	go func() {
		outcome, err := gate.AdmitBody(context.Background(), reqB, resB, gatewayproto.LaneText)
		doneB <- outcome
		errB <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range gatewayhotquality.SpeedFirstBodyAdmissionSnapshot() {
			if entry.Queued == 1 {
				goto queued
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("request B never queued: %+v", gatewayhotquality.SpeedFirstBodyAdmissionSnapshot())
queued:

	// Request C finds the queue full and receives the busy 429 immediately.
	reqC, resC, recorderC := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))
	outcomeC, err := gate.AdmitBody(context.Background(), reqC, resC, gatewayproto.LaneText)
	if err != nil {
		t.Fatalf("admit C: %v", err)
	}
	if !outcomeC.Handled || outcomeC.Release != nil {
		t.Fatalf("outcome C = %+v, want a handled rejection", outcomeC)
	}
	if recorderC.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", recorderC.Code)
	}
	if body := recorderC.Body.String(); body != speedFirstBusyJSON {
		t.Fatalf("body=%q want %q", body, speedFirstBusyJSON)
	}
	if contentType := recorderC.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%q", contentType)
	}
	if connection := recorderC.Header().Get("Connection"); connection != "close" {
		t.Fatalf("connection=%q want close", connection)
	}
	expectedFailure := false
	for _, stage := range obs.snapshotStages() {
		if stage.outcome == "expected_failure" && stage.fields["failureReason"] == "speed_first_body_admission_queue_full" {
			expectedFailure = true
		}
	}
	if !expectedFailure {
		t.Fatalf("missing queue_full expected_failure stage: %+v", obs.snapshotStages())
	}

	// Request B times out of the queue with the same 429 contract.
	outcomeB := <-doneB
	if err := <-errB; err != nil {
		t.Fatalf("admit B: %v", err)
	}
	if !outcomeB.Handled || outcomeB.Release != nil {
		t.Fatalf("outcome B = %+v, want a handled timeout", outcomeB)
	}
	if recorderB.Code != http.StatusTooManyRequests {
		t.Fatalf("B status=%d want 429", recorderB.Code)
	}
	if body := recorderB.Body.String(); body != speedFirstBusyJSON {
		t.Fatalf("B body=%q want %q", body, speedFirstBusyJSON)
	}
	bTimedOut := false
	for _, stage := range obs.snapshotStages() {
		if stage.outcome == "expected_failure" && stage.fields["failureReason"] == "speed_first_body_admission_timeout" {
			bTimedOut = true
		}
	}
	if !bTimedOut {
		t.Fatalf("missing timeout expected_failure stage: %+v", obs.snapshotStages())
	}
}

func TestChainSpeedFirstBodyAdmissionGateMalformedPolicyFails(t *testing.T) {
	gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	obs := &chainCapturedObservability{}
	gate := newChainSpeedFirstGateForTest(obs)
	// maxQueueWaitMs out of the 1..3_600_000 bound throws like the Node
	// boundedInteger validator (next(error) -> 500 contract).
	policy := gatewayruntimecache.GroupSchedulingPolicy{"maxQueueWaitMs": float64(0)}
	req, res, recorder := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))

	outcome, err := gate.AdmitBody(context.Background(), req, res, gatewayproto.LaneText)
	if err == nil {
		t.Fatalf("malformed policy must fail, outcome=%+v", outcome)
	}
	if !strings.Contains(err.Error(), "maxQueueWaitMs") {
		t.Fatalf("error=%v, want the bounded validator message", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("gate must not write the response, status=%d", recorder.Code)
	}
}

func TestChainSpeedFirstBodyAdmissionGateRecordsRejection(t *testing.T) {
	gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	obs := &chainCapturedObservability{}
	gate := newChainSpeedFirstGateForTest(obs)
	var rejections []gatewaybody.RejectionInput
	gate.Recorder = recorderFunc(func(_ *http.Request, _ *gatewaybody.Request, input gatewaybody.RejectionInput) {
		rejections = append(rejections, input)
	})
	policy := chainSpeedFirstPolicy(500, 1)

	reqA, resA, _ := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))
	outcomeA, err := gate.AdmitBody(context.Background(), reqA, resA, gatewayproto.LaneText)
	if err != nil || outcomeA.Release == nil {
		t.Fatalf("admit A: outcome=%+v err=%v", outcomeA, err)
	}
	defer outcomeA.Release()

	reqB, resB, recorderB := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, policy, 1))
	reqB.HTTP.Header.Set("Content-Length", "123")
	doneB := make(chan chainSpeedFirstBodyAdmissionOutcome, 1)
	go func() {
		outcome, _ := gate.AdmitBody(context.Background(), reqB, resB, gatewayproto.LaneText)
		doneB <- outcome
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		queued := false
		for _, entry := range gatewayhotquality.SpeedFirstBodyAdmissionSnapshot() {
			if entry.Queued == 1 {
				queued = true
			}
		}
		if queued {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if outcome := <-doneB; !outcome.Handled {
		t.Fatalf("outcome B = %+v", outcome)
	}
	if recorderB.Code != http.StatusTooManyRequests {
		t.Fatalf("B status=%d", recorderB.Code)
	}
	if len(rejections) != 1 {
		t.Fatalf("rejections=%d want 1", len(rejections))
	}
	rejection := rejections[0]
	if rejection.StatusCode != http.StatusTooManyRequests ||
		rejection.Reason != "gateway_body_admission" ||
		rejection.ErrorCode != "speed_first_body_admission_timeout" ||
		rejection.ErrorMessage != "当前分组繁忙，请稍后重试或增加可用账户。" ||
		rejection.RawBodyBytes != 123 {
		t.Fatalf("rejection = %+v", rejection)
	}
	// Node gatewayErrorPayload('当前分组繁忙…', 'rate_limit_error') carries no code.
	encoded, _ := json.Marshal(rejection.ResponsePayload)
	if string(encoded) != `{"error":{"message":"当前分组繁忙，请稍后重试或增加可用账户。","type":"rate_limit_error"}}` {
		t.Fatalf("payload=%s", string(encoded))
	}
}

type recorderFunc func(*http.Request, *gatewaybody.Request, gatewaybody.RejectionInput)

func (fn recorderFunc) RecordGatewayBodyRejection(r *http.Request, req *gatewaybody.Request, input gatewaybody.RejectionInput) {
	fn(r, req, input)
}

// ---------------------------------------------------------------------------
// image-permission downgrade warn log (image-permission-preflight.ts:62-68)
// ---------------------------------------------------------------------------

func TestChainImagePreflightLogsDowngradedWarn(t *testing.T) {
	obs := &chainCapturedObservability{}
	service, err := gatewaypreauth.New(gatewaypreauth.Service{
		RuntimeCache:  chainPreauthStubRuntimeCache{},
		Observability: obs,
		Clock:         gatewaypreauth.SystemClock{},
	})
	if err != nil {
		t.Fatalf("create preauth service: %v", err)
	}
	preflight := &chainImagePreflight{preauth: service}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","tools":[{"type":"image_generation"}]}`))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	rawBody := []byte(`{"model":"gpt-test","tools":[{"type":"image_generation"}]}`)
	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	req.Body = &gatewaybody.Request{
		RawBody:           rawBody,
		Body:              parsed,
		ContentTypeHeader: "application/json",
		State:             &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON, ContentType: "application/json"},
	}
	capture := &chainTestAuditCapture{}
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

	result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
		Req:             req,
		Res:             res,
		AuditCapture:    capture,
		APIKeyRecord:    &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
		RequestLane:     string(gatewayproto.LaneImage),
		SystemAccountID: "sys_owner",
		APIKeyID:        "key_1",
		GroupID:         "group_1",
		Endpoint:        "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Completed {
		t.Fatalf("downgraded request must continue, result=%+v", result)
	}
	if result.RequestLane != string(gatewaypreauth.RequestLaneText) {
		t.Fatalf("requestLane=%q want text", result.RequestLane)
	}
	if len(obs.warns) != 1 {
		t.Fatalf("warns=%d want 1: %+v", len(obs.warns), obs.warns)
	}
	warn := obs.warns[0]
	if warn.event != "gateway_image_generation_tool_downgraded" {
		t.Fatalf("event=%q", warn.event)
	}
	if warn.message != "系统账户未开启图像生成，已移除 Responses auto 图像生成工具并按文本请求继续" {
		t.Fatalf("message=%q", warn.message)
	}
	if warn.fields["removedToolCount"] != 1 || warn.fields["systemAccountId"] != "sys_owner" ||
		warn.fields["apiKeyId"] != "key_1" || warn.fields["groupId"] != "group_1" {
		t.Fatalf("fields=%+v", warn.fields)
	}
}

// chainTestAuditCapture is the minimal gatewaypreauth.AuditCaptureContext.
type chainTestAuditCapture struct {
	metadata []string
}

func (c *chainTestAuditCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (c *chainTestAuditCapture) AddGatewayMetadata(label string, _ map[string]any) {
	c.metadata = append(c.metadata, label)
}
func (c *chainTestAuditCapture) Finalize(gatewaypreauth.AuditFinalizeInput) {}
