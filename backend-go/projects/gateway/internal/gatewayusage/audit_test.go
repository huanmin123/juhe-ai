package gatewayusage

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// recordingAuditDispatcher is the in-memory AuditDispatcher mock.
type recordingAuditDispatcher struct {
	mu        sync.Mutex
	inputs    []AuditLogInput
	onDispatch func(AuditLogInput)
}

func (r *recordingAuditDispatcher) DispatchAuditLog(ctx Ctx, input AuditLogInput) {
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	hook := r.onDispatch
	r.mu.Unlock()
	if hook != nil {
		hook(input)
	}
}

func (r *recordingAuditDispatcher) all() []AuditLogInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AuditLogInput, len(r.inputs))
	copy(out, r.inputs)
	return out
}

func auditSettings(enabled bool, sampleRate float64) AuditLogSettings {
	return AuditLogSettings{
		Enabled:                   enabled,
		FullBodyCaptureEnabled:    true,
		SuccessSampleRate:         sampleRate,
		ActiveCaptureMaxBytes:     DefaultAuditCaptureHardLimitBytes,
		SuccessHotRetentionHours:  0,
		SuccessRetentionDays:      30,
		ProblemRetentionDays:      90,
		SuccessFullBodyLimitBytes: 1024,
		ProblemFullBodyLimitBytes: 2048,
	}
}

func captureInput(dispatcher *recordingAuditDispatcher, mutate func(*AuditCaptureInput)) *AuditCaptureContext {
	input := AuditCaptureInput{
		TraceID:       "trace-audit-1",
		StartedAtMs:   1700000000000,
		TrafficSource: TrafficSourceGateway,
		Method:        "POST",
		Path:          "/v1/chat/completions",
		OriginalURL:   "/v1/chat/completions?api_key=secret",
		UserAgent:     "test-agent",
		Model:         "gpt-requested",
		Stream:        false,
		Settings:      FixedAuditLogSettingsSource{Settings: auditSettings(true, 0)},
	}
	if dispatcher != nil {
		input.Dispatcher = dispatcher
	}
	if mutate != nil {
		mutate(&input)
	}
	return NewAuditCaptureContext(input)
}

func TestResolveAuditFinalization(t *testing.T) {
	root := &FailedAuditAttemptRoot{ErrorPhase: "upstream_request", ErrorCode: "connect_timeout", ErrorMessage: "连接超时"}
	tests := []struct {
		name        string
		input       ResolveAuditFinalizationInput
		closed      bool
		hadFailed   bool
		root        *FailedAuditAttemptRoot
		outcome     AuditOutcome
		success     bool
		errorPhase  string
		errorCode   string
		errorMessage string
	}{
		{
			"plain success",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeSuccess, Success: true},
			false, false, nil,
			AuditOutcomeSuccess, true, "", "", "",
		},
		{
			"downstream closed input",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeDownstreamClosed, Success: false},
			true, false, nil,
			AuditOutcomeDownstreamClosed, false, "downstream", "downstream_connection_closed", "下游连接关闭",
		},
		{
			"success after retry",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeSuccess, Success: true},
			false, true, nil,
			AuditOutcomeSuccessAfterRetry, true, "", "", "",
		},
		{
			"input root failure",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, ErrorPhase: "upstream_response", ErrorCode: "boom"},
			false, false, nil,
			AuditOutcomeUpstreamFailed, false, "upstream_response", "boom", "",
		},
		{
			"generic retry facade keeps attempt root",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, ErrorCode: "upstream_retryable_error"},
			true, false, root,
			AuditOutcomeUpstreamFailed, false, "upstream_request", "connect_timeout", "连接超时",
		},
		{
			"attempt root failure on downstream close",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeSuccess, Success: false, ErrorPhase: "downstream"},
			true, false, root,
			AuditOutcomeUpstreamFailed, false, "upstream_request", "connect_timeout", "连接超时",
		},
		{
			"stream attempt root",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeSuccess, Success: false, ErrorPhase: "downstream"},
			true, false, &FailedAuditAttemptRoot{ErrorPhase: "stream", ErrorCode: "stream_reset"},
			AuditOutcomeStreamFailed, false, "stream", "stream_reset", "",
		},
		{
			"downstream close without attempt root",
			ResolveAuditFinalizationInput{Outcome: AuditOutcomeSuccess, Success: false, ErrorPhase: "downstream"},
			true, false, nil,
			AuditOutcomeDownstreamClosed, false, "downstream", "downstream_connection_closed", "下游连接关闭",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAuditFinalization(tt.input, tt.closed, tt.hadFailed, tt.root)
			if got.Outcome != tt.outcome || got.Success != tt.success {
				t.Fatalf("outcome/success = %q/%v want %q/%v (%+v)", got.Outcome, got.Success, tt.outcome, tt.success, got)
			}
			if got.ErrorPhase != tt.errorPhase || got.ErrorCode != tt.errorCode || got.ErrorMessage != tt.errorMessage {
				t.Fatalf("error fields = %+v", got)
			}
		})
	}
}

func TestAuditCaptureDisabled(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(false, 0)}
	})
	capture.AddGatewayMetadata("should_be_skipped", nil)
	tempID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST",
	})
	if tempID != "" {
		t.Fatalf("disabled capture must return empty temp id, got %q", tempID)
	}
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)})
	if len(dispatcher.all()) != 0 {
		t.Fatalf("disabled audit must not dispatch: %d", len(dispatcher.all()))
	}
	if GetActiveAuditCaptureCount() != 0 {
		t.Fatalf("active count = %d", GetActiveAuditCaptureCount())
	}
}

func TestAuditCaptureMetadataOnlyMode(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.CaptureMode = CaptureModeMetadataOnly
	})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
	dispatched := dispatcher.all()
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %d", len(dispatched))
	}
	auditLog := dispatched[0]
	if auditLog.CaptureStatus != "metadata_only" {
		t.Fatalf("captureStatus = %q", auditLog.CaptureStatus)
	}
	if auditLog.SampleReason != "gateway_metadata_only" {
		t.Fatalf("sampleReason = %q", auditLog.SampleReason)
	}
	// An unsampled successful envelope retains no payloads (Node
	// unsampledSuccessEnvelope empties attempts and payloads).
	if len(auditLog.Payloads) != 0 {
		t.Fatalf("payloads = %+v", auditLog.Payloads)
	}

	// A failed metadata-only capture keeps the traffic_source metadata body.
	ResetActiveAuditCaptureCountForTest()
	failureDispatcher := &recordingAuditDispatcher{}
	failureCapture := captureInput(failureDispatcher, func(input *AuditCaptureInput) {
		input.CaptureMode = CaptureModeMetadataOnly
	})
	failureCapture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false}
	})
	failureLog := failureDispatcher.all()[0]
	if len(failureLog.Payloads) != 1 || failureLog.Payloads[0].PartType != AuditPartGatewayMetadata {
		t.Fatalf("failure payloads = %+v", failureLog.Payloads)
	}
	body := string(failureLog.Payloads[0].Body)
	want := `{"type":"gateway_metadata","label":"traffic_source","metadata":{"trafficSource":"gateway","captureMode":"metadata_only"}}`
	if body != want {
		t.Fatalf("metadata body = %q", body)
	}
	if !strings.Contains(auditLog.Path, "/v1/chat/completions") {
		t.Fatalf("path = %q", auditLog.Path)
	}
	if auditLog.QueryString != "api_key=secret" {
		t.Fatalf("queryString = %q", auditLog.QueryString)
	}
}

func TestAuditCaptureSamplingGate(t *testing.T) {
	tests := []struct {
		name              string
		sampleRate        float64
		hotRetention      bool
		failure           bool
		wantReason        string
		wantCaptureStatus string
		wantPayloads      int
	}{
		{"sampled success", 1, false, false, "success_sample_1", "complete", 2},
		{"unsampled success", 0, false, false, "success_metadata_only", "metadata_only", 0},
		{"hot retention success", 0, true, false, "success_hot_full_retention", "complete", 2},
		{"failure always full", 0, false, true, "full_capture", "complete", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetActiveAuditCaptureCountForTest()
			dispatcher := &recordingAuditDispatcher{}
			capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
								input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
					Enabled: true, FullBodyCaptureEnabled: true,
					SuccessSampleRate: tt.sampleRate, ActiveCaptureMaxBytes: DefaultAuditCaptureHardLimitBytes,
					SuccessHotRetentionHours: boolHours(tt.hotRetention),
					SuccessFullBodyLimitBytes: 1024, ProblemFullBodyLimitBytes: 2048,
				}}
			})
			finalizeInput := FinalizeAuditInput{
				StatusCode: intPointer(200),
				ResponseBody: []byte(`{"ok":true}`), HasResponseBody: true,
				ResponseHeaders: map[string]any{"content-type": "application/json"},
			}
			if tt.failure {
				finalizeInput.Outcome = AuditOutcomeUpstreamFailed
			} else {
				finalizeInput.Outcome = AuditOutcomeSuccess
				finalizeInput.Success = true
			}
			capture.FinalizeLazy(func() FinalizeAuditInput { return finalizeInput })
			dispatched := dispatcher.all()
			if len(dispatched) != 1 {
				t.Fatalf("dispatched = %d", len(dispatched))
			}
			auditLog := dispatched[0]
			if auditLog.SampleReason != tt.wantReason {
				t.Fatalf("sampleReason = %q want %q", auditLog.SampleReason, tt.wantReason)
			}
			if auditLog.CaptureStatus != tt.wantCaptureStatus {
				t.Fatalf("captureStatus = %q want %q", auditLog.CaptureStatus, tt.wantCaptureStatus)
			}
			if len(auditLog.Payloads) != tt.wantPayloads {
				t.Fatalf("payloads = %d want %d", len(auditLog.Payloads), tt.wantPayloads)
			}
			if auditLog.SampleBucket < 0 || auditLog.SampleBucket >= 10000 {
				t.Fatalf("sampleBucket = %d", auditLog.SampleBucket)
			}
			wantOutcome := AuditOutcomeSuccess
			if tt.failure {
				wantOutcome = AuditOutcomeUpstreamFailed
			}
			if auditLog.AuditOutcome != wantOutcome || auditLog.Success != !tt.failure {
				t.Fatalf("outcome/success = %q/%v", auditLog.AuditOutcome, auditLog.Success)
			}
			if auditLog.HTTPDurationMs != nil {
				t.Fatal("http duration must stay undefined without a completion observer")
			}
			if auditLog.DurationMs == nil {
				t.Fatal("duration expected")
			}
		})
	}
	ResetActiveAuditCaptureCountForTest()
}

func boolHours(enabled bool) int {
	if enabled {
		return 24
	}
	return 0
}

func TestAuditCaptureAttemptsAndFailures(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.Stream = true
		input.Models = &stubModelResolver{resolution: UsageModelResolution{
			UpstreamModel: "gpt-x", ModelMappingApplied: true, UpstreamEndpointFamily: "chat_completions",
		}}
	})
	tempID := capture.StartAttempt(StartAttemptInput{
		Account:      testAccount(),
		AttemptIndex: 1,
		UpstreamURL:  "https://upstream.example.com/v1/chat?token=abc",
		Method:       "POST",
		Headers:      map[string]any{"content-type": "application/json", "authorization": "Bearer abc"},
		Body:         []byte(`{"model":"gpt"}`),
		HasBody:      true,
	})
	if tempID == "" {
		t.Fatal("temp id expected")
	}
	// The in-progress envelope must be enqueued for stream requests.
	dispatched := dispatcher.all()
	if len(dispatched) != 1 || dispatched[0].LifecycleStatus != AuditLifecycleInProgress {
		t.Fatalf("in-progress envelope = %+v", dispatched)
	}
	envelope := dispatched[0]
	if envelope.AuditOutcome != AuditOutcomeGatewaySucceeded || !envelope.Success || envelope.SampleReason != "in_progress" {
		t.Fatalf("envelope fields = %+v", envelope)
	}
	capture.CompleteAttempt(tempID, CompleteAttemptInput{
		StatusCode: intPointer(500), Success: false,
		ErrorPhase: "upstream_response", ErrorCode: "upstream_500", ErrorMessage: "上游错误",
		ResponseBody: []byte("server error"), HasResponseBody: true,
	})
	capture.MarkDownstreamClosed()
	capture.MarkDownstreamClosed() // idempotent
	capture.MarkServerDiagnosticTimeout()
	capture.MarkServerDiagnosticCancellation()
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{
			Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(500),
			ErrorPhase: "upstream_response", ErrorCode: "upstream_500", ErrorMessage: "上游错误",
			ResponseBody: []byte(`{"error":"final"}`), HasResponseBody: true,
			ResponsePartType: AuditPartGatewayError,
		}
	})
	final := dispatcher.all()
	var auditLog AuditLogInput
	inProgressCount := 0
	for _, entry := range final {
		if entry.LifecycleStatus == AuditLifecycleInProgress {
			inProgressCount++
			continue
		}
		auditLog = entry
	}
	if inProgressCount != 1 {
		t.Fatalf("in-progress count = %d", inProgressCount)
	}
	if auditLog.AuditOutcome != AuditOutcomeUpstreamFailed || auditLog.Success {
		t.Fatalf("outcome = %q/%v", auditLog.AuditOutcome, auditLog.Success)
	}
	if auditLog.CaptureStatus != "complete" || auditLog.SampleReason != "full_capture" {
		t.Fatalf("capture = %q/%q", auditLog.CaptureStatus, auditLog.SampleReason)
	}
	if len(auditLog.Attempts) != 1 {
		t.Fatalf("attempts = %d", len(auditLog.Attempts))
	}
	attempt := auditLog.Attempts[0]
	if attempt.UpstreamURL != "https://upstream.example.com/v1/chat?token=abc" {
		t.Fatalf("upstream url = %q", attempt.UpstreamURL)
	}
	if attempt.Success == nil || *attempt.Success || *attempt.UpstreamStatusCode != 500 {
		t.Fatalf("attempt status = %+v", attempt)
	}
	if attempt.AccountID != "acc-1" || attempt.ProviderCode != "gpt" || attempt.UpstreamModel != "gpt-x" {
		t.Fatalf("attempt accounting = %+v", attempt)
	}
	if attempt.ModelMappingApplied == nil || !*attempt.ModelMappingApplied {
		t.Fatalf("model mapping = %+v", attempt)
	}
	metadataPayloads := 0
	for _, payload := range auditLog.Payloads {
		switch payload.PartType {
		case AuditPartUpstreamRequest:
			if payload.CaptureStatus != "" {
				t.Fatalf("small retained request payload must keep undefined status: %+v", payload)
			}
			if payload.BodySha256 == "" || *payload.RawBodySizeBytes != len(`{"model":"gpt"}`) {
				t.Fatalf("request payload hash/size = %q/%v", payload.BodySha256, payload.RawBodySizeBytes)
			}
		case AuditPartUpstreamResponse:
			if string(payload.Body) != "server error" {
				t.Fatalf("response payload = %q", payload.Body)
			}
		case AuditPartGatewayMetadata:
			metadataPayloads++
			body := string(payload.Body)
			if !strings.Contains(body, `"label":"downstream_connection_closed"`) &&
				!strings.Contains(body, `"label":"server_diagnostic_timeout"`) &&
				!strings.Contains(body, `"label":"server_diagnostic_cancelled"`) {
				t.Fatalf("unexpected metadata = %q", body)
			}
		}
	}
	if metadataPayloads != 3 {
		t.Fatalf("metadata payloads = %d", metadataPayloads)
	}
	if auditLog.ErrorPhase != "upstream_response" || auditLog.ErrorCode != "upstream_500" {
		t.Fatalf("finalization fields = %+v", auditLog)
	}
}

func TestAuditCaptureRecordFailedDispatchAttemptAndRetrySuccess(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {})
	capture.RecordFailedDispatchAttempt(RecordFailedDispatchAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "account:preparation", Method: "POST",
		StartedAtMs: 1700000000000 - 100,
		ErrorCode:   "account_preparation_failed", ErrorMessage: "账号准备失败",
	})
	successTemp := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 1,
		UpstreamURL: "https://upstream", Method: "POST",
	})
	capture.CompleteAttempt(successTemp, CompleteAttemptInput{StatusCode: intPointer(200), Success: true})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
	dispatched := dispatcher.all()
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %d", len(dispatched))
	}
	auditLog := dispatched[0]
	if auditLog.AuditOutcome != AuditOutcomeSuccessAfterRetry {
		t.Fatalf("outcome = %q", auditLog.AuditOutcome)
	}
	if len(auditLog.Attempts) != 2 {
		t.Fatalf("attempts = %d", len(auditLog.Attempts))
	}
	failed := auditLog.Attempts[0]
	if *failed.Success || failed.UpstreamURL != "account:preparation" || failed.DurationMs == nil {
		t.Fatalf("failed dispatch attempt = %+v", failed)
	}
	// Unsampled success with a failed attempt must retain full payloads.
	if auditLog.CaptureStatus != "complete" {
		t.Fatalf("capture status = %q", auditLog.CaptureStatus)
	}
}

func TestAuditCaptureCancelAndFinalizeOnce(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, nil)
	capture.Cancel()
	capture.Cancel()
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true})
	if len(dispatcher.all()) != 0 {
		t.Fatal("cancelled capture must not dispatch")
	}
	if GetActiveAuditCaptureCount() != 0 {
		t.Fatalf("active count after cancel = %d", GetActiveAuditCaptureCount())
	}
}

func TestAuditCapturePayloadOverflow(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true,
			SuccessSampleRate: 1, ActiveCaptureMaxBytes: 1024,
			SuccessFullBodyLimitBytes: 1024, ProblemFullBodyLimitBytes: 1024,
		}}
	})
	capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
		Body: make([]byte, 4096), HasBody: true,
	})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(500)}
	})
	dispatched := dispatcher.all()
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %d", len(dispatched))
	}
	auditLog := dispatched[0]
	if auditLog.CaptureStatus != "overflow" {
		t.Fatalf("captureStatus = %q", auditLog.CaptureStatus)
	}
	if len(auditLog.Payloads) != 1 || auditLog.Payloads[0].CaptureStatus != AuditCaptureOverflow {
		t.Fatalf("overflow payloads = %+v", auditLog.Payloads)
	}
	body := string(auditLog.Payloads[0].Body)
	if !strings.Contains(body, "active_capture_overflow") || !strings.Contains(body, "\"residentPayloadBytes\":") {
		t.Fatalf("overflow body = %q", body)
	}
}

func TestAuditCaptureOmitPayloadBodies(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, nil)
	tempID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
		Body: []byte(`{"model":"gpt"}`), HasBody: true,
	})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{
		Success: false, StatusCode: intPointer(500),
		ErrorPhase: "upstream_response", ErrorCode: "boom",
		ResponseBody: []byte("response body"), HasResponseBody: true,
	})
	capture.OmitPayloadBodies(OmitPayloadBodiesInput{
		Label: "payload_bodies_omitted",
		PartTypes: []AuditPayloadPartType{AuditPartUpstreamRequest, AuditPartUpstreamResponse, AuditPartClientRequest},
	})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(500)}
	})
	dispatched := dispatcher.all()
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %d", len(dispatched))
	}
	auditLog := dispatched[0]
	omitted := 0
	for _, payload := range auditLog.Payloads {
		if payload.PartType == AuditPartGatewayMetadata {
			continue
		}
		if payload.CaptureStatus == AuditCaptureHashOnly {
			omitted++
			if payload.HasBody || payload.ContentEncoding != "" {
				t.Fatalf("omitted payload must drop body and encoding: %+v", payload)
			}
			if payload.BodySha256 == "" || payload.RawBodySizeBytes == nil {
				t.Fatalf("hash/size missing: %+v", payload)
			}
			continue
		}
		if !payload.HasBody {
			// The bodyless client_request tombstone added at finalize has
			// nothing to omit (Node skips body-undefined payloads).
			continue
		}
		t.Fatalf("payload must have been omitted: %+v", payload)
	}
	if omitted != 2 {
		t.Fatalf("omitted payloads = %d", omitted)
	}
	metadataFound := false
	for _, payload := range auditLog.Payloads {
		if payload.PartType != AuditPartGatewayMetadata {
			continue
		}
		body := string(payload.Body)
		if strings.Contains(body, "payload_bodies_omitted") {
			metadataFound = true
			if !strings.Contains(body, `"auditBodyPayloadsOmitted":true`) ||
				!strings.Contains(body, `"omittedPayloadCount":2`) {
				t.Fatalf("omission metadata = %q", body)
			}
		}
	}
	if !metadataFound {
		t.Fatal("omission metadata entry missing")
	}
}

func TestAuditCaptureDispatchFailureIsBestEffort(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	// A nil dispatcher mirrors the Node unconfigured-loopback contract:
	// finalize completes and never errors even when delivery is impossible.
	capture := captureInput(nil, nil)
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false}
	})
}

func TestAuditTrafficSourceGate(t *testing.T) {
	tests := []struct {
		source  string
		persist bool
	}{
		{TrafficSourceGateway, true},
		{TrafficSourceManualAccountTest, true},
		{TrafficSourceHybridScoring, true},
		{TrafficSourceHybridQualityScoring, true},
		{TrafficSourceAccountHealthCheck, false},
		{TrafficSourceRuntimeRecoveryProbe, false},
		{TrafficSourceCooldownRetest, false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := ShouldPersistAuditTrafficSource(tt.source); got != tt.persist {
				t.Fatalf("persist = %v want %v", got, tt.persist)
			}
		})
	}
	t.Run("capture skips non-persisted sources at dispatch", func(t *testing.T) {
		ResetActiveAuditCaptureCountForTest()
		dispatcher := &recordingAuditDispatcher{}
		capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
			input.TrafficSource = TrafficSourceCooldownRetest
		})
		capture.FinalizeLazy(func() FinalizeAuditInput {
			return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false}
		})
		if len(dispatcher.all()) != 0 {
			t.Fatal("probe traffic must not reach the dispatcher")
		}
	})
}

func TestSummarizeAuditPayloadForLimit(t *testing.T) {
	t.Run("gateway metadata skipped", func(t *testing.T) {
		payload := AuditLogPayloadInput{PartType: AuditPartGatewayMetadata, Body: []byte("{}"), HasBody: true}
		if SummarizeAuditPayloadForLimit(&payload, 0, SummarizeAuditPayloadOptions{}) {
			t.Fatal("gateway metadata must be skipped without include flag")
		}
		if !SummarizeAuditPayloadForLimit(&payload, 0, SummarizeAuditPayloadOptions{IncludeGatewayMetadata: true}) {
			t.Fatal("with include flag it must summarize")
		}
	})
	t.Run("zero limit hashes body", func(t *testing.T) {
		body := []byte("hello audit")
		payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: body, HasBody: true}
		if !SummarizeAuditPayloadForLimit(&payload, 0, SummarizeAuditPayloadOptions{}) {
			t.Fatal("expected summarization")
		}
		if payload.CaptureStatus != AuditCaptureHashOnly || payload.HasBody {
			t.Fatalf("payload = %+v", payload)
		}
		if payload.BodySha256 != sha256Hex(body) {
			t.Fatalf("sha = %q", payload.BodySha256)
		}
		if *payload.RawBodySizeBytes != len(body) {
			t.Fatalf("size = %d", *payload.RawBodySizeBytes)
		}
	})
	t.Run("under limit untouched", func(t *testing.T) {
		payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: []byte("small"), HasBody: true}
		if SummarizeAuditPayloadForLimit(&payload, 1024, SummarizeAuditPayloadOptions{}) {
			t.Fatal("under-limit body must stay untouched")
		}
	})
	t.Run("over limit becomes summary", func(t *testing.T) {
		body := []byte(strings.Repeat("a", 4096))
		payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: body, HasBody: true, ContentType: "application/json"}
		if !SummarizeAuditPayloadForLimit(&payload, 1024, SummarizeAuditPayloadOptions{}) {
			t.Fatal("expected summary")
		}
		if payload.CaptureStatus != AuditCaptureSummaryOnly || payload.ContentType != AuditPayloadSummaryContentType {
			t.Fatalf("payload = %+v", payload)
		}
		if payload.BodySha256 != sha256Hex(body) {
			t.Fatal("original sha must be preserved")
		}
		var summary map[string]any
		if err := unmarshalJSON(payload.Body, &summary); err != nil {
			t.Fatalf("summary json = %v", err)
		}
		if summary["type"] != "audit_payload_summary" || summary["originalSizeBytes"] != float64(4096) {
			t.Fatalf("summary = %v", summary)
		}
		head := mustDecodeBase64(summary["headBase64"].(string))
		tail := mustDecodeBase64(summary["tailBase64"].(string))
		if len(head)+len(tail) != 1024 {
			t.Fatalf("retained = %d", len(head)+len(tail))
		}
		preview, ok := summary["textPreview"].(map[string]any)
		if !ok || len(preview["head"].(string)) == 0 {
			t.Fatalf("textPreview = %v", summary["textPreview"])
		}
	})
	t.Run("binary content skips preview", func(t *testing.T) {
		body := []byte(strings.Repeat("\x00", 4096))
		payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: body, HasBody: true, ContentType: "application/octet-stream", ContentEncoding: "gzip"}
		if !SummarizeAuditPayloadForLimit(&payload, 512, SummarizeAuditPayloadOptions{}) {
			t.Fatal("expected summary")
		}
		if strings.Contains(string(payload.Body), "textPreview") {
			t.Fatal("non-text payloads must not carry a text preview")
		}
	})
	t.Run("existing summary shrinks to zero limit", func(t *testing.T) {
		body := []byte(strings.Repeat("b", 4096))
		payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: body, HasBody: true, ContentType: "text/plain"}
		SummarizeAuditPayloadForLimit(&payload, 1024, SummarizeAuditPayloadOptions{})
		if SummarizeAuditPayloadForLimit(&payload, 0, SummarizeAuditPayloadOptions{}) {
			t.Fatal("second call with limit 0 must report false")
		}
		if payload.CaptureStatus != AuditCaptureHashOnly || payload.HasBody {
			t.Fatalf("payload = %+v", payload)
		}
	})
}

func TestResponseInspectionDecisionAuditMetadata(t *testing.T) {
	metadata := ResponseInspectionDecisionAuditMetadata(ResponseInspectionDecisionAuditMetadataInput{
		Action: "rewrite", Reason: "matched_policy", Transport: "stream",
		DownstreamWritten: true, RetryEnabled: false, CodexCompactionExpected: true,
		PolicyName: "策略",
	})
	encoded, err := metadata.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal = %v", err)
	}
	text := string(encoded)
	if !strings.HasPrefix(text, `{"responsePolicyMatched":true,"responseInspectionIntercepted":true,"fallbackReason":"matched_policy","inspectionAction":"rewrite"`) {
		t.Fatalf("key order/content mismatch: %q", text)
	}
	if !strings.Contains(text, `"downstreamWritten":true`) || !strings.Contains(text, `"retryEnabled":false`) {
		t.Fatalf("boolean fields missing: %q", text)
	}
	if !strings.Contains(text, `"codexCompactionExpected":true`) {
		t.Fatalf("codex flag missing: %q", text)
	}
}

func TestSampleBucketForTraceID(t *testing.T) {
	bucket := sampleBucketForTraceID("trace-audit-1")
	if bucket < 0 || bucket >= 10000 {
		t.Fatalf("bucket = %d", bucket)
	}
	if bucket != sampleBucketForTraceID("trace-audit-1") {
		t.Fatal("same trace must map to the same bucket")
	}
}

func mustDecodeBase64(text string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		panic(err)
	}
	return decoded
}

func unmarshalJSON(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
