package gatewayresponse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewaydownstream"
	"juhe-ai/backend-go/internal/modules/gatewayretry"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	"juhe-ai/backend-go/internal/store/port"
)

func TestHandleJSONSafeRepairWritesGuardedBodyAndHandoff(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"object":"response","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}],"usage":{"input_tokens":7,"output_tokens":3}}`)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{sink}, StartedAt: testNow.Add(-time.Second),
		Codex: &CodexGuard{Mode: codexresponses.ModeSafeRepair, Checkpoint: RawUpstreamCheckpoint(),
			CreateItemID: func(prefix, _ string, _, _ int) string { return prefix + "_guarded" }},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !body.closed || result.State != StateSucceeded || !result.TransportCommitted || !result.SemanticCommitted || result.RetryAllowed {
		t.Fatalf("result/body = %#v/%v", result, body.closed)
	}
	if !strings.Contains(sink.String(), `"id":"ctc_guarded"`) || strings.Contains(sink.String(), `"id":"fc_wrong"`) {
		t.Fatalf("sink = %s", sink.String())
	}
	if result.Guard == nil || result.Guard.Outcome != codexresponses.OutcomeRepairedSafe || len(result.Guard.RepairRuleIDs) != 1 {
		t.Fatalf("guard = %#v", result.Guard)
	}
	if result.Handoff.Usage.Outcome != gatewayusage.OutcomeSucceeded || !result.Handoff.Audit.Success {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
	if _, ok := result.Handoff.Usage.ResponseSnapshot.(*GuardSummary); !ok {
		t.Fatalf("response snapshot = %#v", result.Handoff.Usage.ResponseSnapshot)
	}
	if result.Handoff.Usage.Usage.InputTokens == nil || *result.Handoff.Usage.Usage.InputTokens != 7 || result.Handoff.Usage.Usage.OutputTokens == nil || *result.Handoff.Usage.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %#v", result.Handoff.Usage.Usage)
	}
}

func TestHandleJSONStrictBlocksBeforeDownstreamCommit(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"object":"response","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}],"usage":{"input_tokens":11,"output_tokens":5}}`)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{sink}, Codex: &CodexGuard{Mode: codexresponses.ModeStrictIntercept, Checkpoint: RawUpstreamCheckpoint()},
	})
	if !errors.Is(err, ErrCodexProtocolIntercepted) || !body.closed || sink.Len() != 0 {
		t.Fatalf("error/body/sink = %v/%v/%q", err, body.closed, sink.String())
	}
	if result.State != StateFailedBeforeCommit || !result.RetryAllowed || result.TransportCommitted || result.Guard == nil || result.Guard.Outcome != codexresponses.OutcomeRepairable {
		t.Fatalf("result = %#v", result)
	}
	if result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionAccountUpstream || result.Handoff.Audit.RequestedOutcome != gatewayaudit.OutcomeUpstreamFailed {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
	if result.Handoff.Usage.Usage.InputTokens == nil || *result.Handoff.Usage.Usage.InputTokens != 11 || result.Handoff.Usage.Usage.OutputTokens == nil || *result.Handoff.Usage.Usage.OutputTokens != 5 {
		t.Fatalf("blocked usage = %#v", result.Handoff.Usage.Usage)
	}
}

func TestProtocolRetryHandoffDrivesPlannerToNextCandidate(t *testing.T) {
	planner, err := gatewayretry.NewPlanner(gatewayretry.PlanInput{
		Route:       gatewayrouting.OrderResult{Bindings: []gatewayrouting.Binding{{ID: "binding", GroupID: "group"}}},
		Candidates:  []port.GatewayAccountCandidate{{AccountID: "a", GroupID: "group"}, {AccountID: "b", GroupID: "group"}},
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := planner.Start(context.Background())
	body := &trackingBody{Reader: strings.NewReader(`{"object":"response","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`)}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{&bytes.Buffer{}}, Codex: &CodexGuard{Mode: codexresponses.ModeStrictIntercept, Checkpoint: RawUpstreamCheckpoint()},
	})
	if !errors.Is(err, ErrCodexProtocolIntercepted) || !result.Handoff.Retry.Allowed {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	second := planner.Fail(context.Background(), *first.Attempt, result.Handoff.Retry.Failure)
	if second.Action != gatewayretry.ActionAttempt || second.Attempt == nil || second.Attempt.Account.AccountID != "b" {
		t.Fatalf("second decision = %#v", second)
	}
}

func TestHandleJSONMalformedSafeBlocksWhileShadowForwardsOpaqueBytes(t *testing.T) {
	for _, test := range []struct {
		mode      codexresponses.Mode
		wantError error
		wantWrite bool
	}{
		{mode: codexresponses.ModeSafeRepair, wantError: ErrCodexProtocolBlocked},
		{mode: codexresponses.ModeShadow, wantWrite: true},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			body := &trackingBody{Reader: strings.NewReader(`{"object":"response"`)}
			sink := &bytes.Buffer{}
			result, err := testHandler().Handle(Input{
				Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
				Sink: sinkAdapter{sink}, Codex: &CodexGuard{Mode: test.mode, Checkpoint: RawUpstreamCheckpoint()},
			})
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) || sink.Len() != 0 || !result.RetryAllowed {
					t.Fatalf("result/error/sink = %#v/%v/%q", result, err, sink.String())
				}
				return
			}
			if err != nil || !test.wantWrite || sink.String() != `{"object":"response"` || result.Guard == nil || result.Guard.Outcome != codexresponses.OutcomeBlocked {
				t.Fatalf("result/error/sink = %#v/%v/%q", result, err, sink.String())
			}
		})
	}
}

func TestHandleNonSuccessStatusForwardsCompleteResponseWithoutPolicy(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"error":"busy"}`)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusServiceUnavailable, body), Transport: TransportJSON, Sink: sinkAdapter{sink},
	})
	if err != nil || !body.closed || sink.String() != `{"error":"busy"}` || result.State != StateUpstreamFailureForwarded || result.RetryAllowed || len(result.BufferedBody) != 0 {
		t.Fatalf("result/error/body/sink = %#v/%v/%v/%q", result, err, body.closed, sink.String())
	}
	if result.Handoff.Usage.StatusCode == nil || *result.Handoff.Usage.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
}

func TestHandleTransparentFailureCommitsStatusAndSafeHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	sink, _ := gatewaydownstream.NewHTTPWriterSink(recorder)
	body := &trackingBody{Reader: strings.NewReader(`{"error":"busy"}`)}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportJSON, Sink: sink,
		Dispatch: gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {"9"}, "Set-Cookie": {"secret=1"}}, Body: body}},
	})
	if err != nil || recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "9" || recorder.Header().Get("Set-Cookie") != "" || recorder.Body.String() != `{"error":"busy"}` || !result.TransportCommitted || !result.SemanticCommitted {
		t.Fatalf("result/error/recorder = %+v/%v/%#v", result, err, recorder)
	}
}

func TestHandleExplicitPolicyDoesNotCommitStagedResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	sink, _ := gatewaydownstream.NewHTTPWriterSink(recorder)
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportJSON, Sink: sink,
		Dispatch:            gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {"9"}}, Body: &trackingBody{Reader: strings.NewReader(`{"error":"busy"}`)}}},
		ResponseDisposition: gatewayretry.ResponseDispositionExplicitPolicy,
	})
	if !errors.Is(err, ErrUpstreamStatus) || !result.RetryAllowed || sink.Snapshot().TransportCommitted || recorder.Body.Len() != 0 {
		t.Fatalf("result/error/state = %+v/%v/%+v", result, err, sink.Snapshot())
	}
}

func TestHandleJSONHeaderOnlySuccessCommitsWithoutBodyBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	sink, _ := gatewaydownstream.NewHTTPWriterSink(recorder)
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportJSON, Sink: sink,
		Dispatch: gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{"Content-Length": {"99"}}, Body: &trackingBody{Reader: strings.NewReader("")}}},
	})
	if err != nil || result.State != StateSucceeded || recorder.Code != http.StatusNoContent || recorder.Header().Get("Content-Length") != "" || !result.TransportCommitted || !result.SemanticCommitted || result.BytesWritten != 0 || !sink.Snapshot().TransportCommitted {
		t.Fatalf("result/error/recorder/state = %+v/%v/%#v/%+v", result, err, recorder, sink.Snapshot())
	}
}

func TestHandleJSONHeaderCommitFailureBeforeWriteRemainsUncommitted(t *testing.T) {
	commitErr := errors.New("commit failed")
	sink := &failingCommitSink{err: commitErr}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportJSON, Sink: sink,
		Dispatch: gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusOK, Body: &trackingBody{Reader: strings.NewReader(`{"ok":true}`)}}},
	})
	if !errors.Is(err, ErrDestinationWrite) || !errors.Is(err, commitErr) || result.State != StateFailedBeforeCommit || result.TransportCommitted || sink.Snapshot().TransportCommitted {
		t.Fatalf("result/error/state = %+v/%v/%+v", result, err, sink.Snapshot())
	}
}

func TestHandleNonSuccessStatusUsesExplicitClassificationHandoff(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"error":"busy"}`)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusServiceUnavailable, body), Transport: TransportJSON, Sink: sinkAdapter{sink},
		ResponseDisposition: gatewayretry.ResponseDispositionExplicitPolicy,
	})
	if !errors.Is(err, ErrUpstreamStatus) || !body.closed || sink.Len() != 0 || !result.RetryAllowed || string(result.BufferedBody) != `{"error":"busy"}` {
		t.Fatalf("result/error/body/sink = %#v/%v/%v/%q", result, err, body.closed, sink.String())
	}
	if result.Handoff.Usage.ErrorCode != "upstream_http_status" || result.Handoff.Usage.ErrorMessage != "busy" {
		t.Fatalf("policy facts = %#v", result.Handoff.Usage)
	}
}

func TestHandleExplicitPolicyUsesStructuredErrorFacts(t *testing.T) {
	t.Run("semantic error does not retry server status", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader(`{"error":{"code":"model_not_found","type":"invalid_request_error","message":"missing"}}`)}
		result, err := testHandler().Handle(Input{
			Context: context.Background(), Dispatch: dispatchResult(http.StatusServiceUnavailable, body), Transport: TransportJSON,
			Sink: sinkAdapter{&bytes.Buffer{}}, ResponseDisposition: gatewayretry.ResponseDispositionExplicitPolicy,
		})
		if !errors.Is(err, ErrUpstreamStatus) || result.RetryAllowed || result.Handoff.Retry.Classification.Class != gatewayretry.FailureClassRequestSemantic {
			t.Fatalf("result/error = %#v/%v", result, err)
		}
		if result.Handoff.Retry.Failure.ErrorCode != "model_not_found" || result.Handoff.Retry.Failure.ErrorType != "invalid_request_error" || result.Handoff.Usage.ErrorMessage != "missing" {
			t.Fatalf("facts = %#v/%#v", result.Handoff.Retry.Failure, result.Handoff.Usage)
		}
	})
	t.Run("nested wrapper facts win and numeric code is retained", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader(`{"type":"error","code":500,"error":{"code":123,"type":"invalid_request_error","message":"bad request"}}`)}
		result, err := testHandler().Handle(Input{
			Context: context.Background(), Dispatch: dispatchResult(http.StatusServiceUnavailable, body), Transport: TransportJSON,
			Sink: sinkAdapter{&bytes.Buffer{}}, ResponseDisposition: gatewayretry.ResponseDispositionExplicitPolicy,
		})
		if !errors.Is(err, ErrUpstreamStatus) || result.RetryAllowed || result.Handoff.Retry.Failure.ErrorCode != "123" || result.Handoff.Retry.Failure.ErrorType != "invalid_request_error" {
			t.Fatalf("result/error = %#v/%v", result, err)
		}
	})
	t.Run("alternative key avoids key not account", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader(`{"error":{"code":"invalid_api_key","type":"authentication_error","message":"bad key"}}`)}
		result, err := testHandler().Handle(Input{
			Context: context.Background(), Dispatch: dispatchResult(http.StatusUnauthorized, body), Transport: TransportJSON,
			Sink: sinkAdapter{&bytes.Buffer{}}, ResponseDisposition: gatewayretry.ResponseDispositionExplicitPolicy,
			ResponsePolicy: ResponsePolicyFacts{HasAlternativeAPIKeys: true},
		})
		if !errors.Is(err, ErrUpstreamStatus) || !result.RetryAllowed || !result.Handoff.Retry.Classification.WouldAvoidAPIKey || result.Handoff.Retry.Classification.WouldAvoidAccount {
			t.Fatalf("result/error = %#v/%v", result, err)
		}
	})
}

func TestHandleJSONPartialWriteDisablesRetry(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"object":"response","output":[]}`)}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink:  &partialSink{limit: 3, err: errors.New("client gone")},
		Codex: &CodexGuard{Mode: codexresponses.ModeShadow, Checkpoint: RawUpstreamCheckpoint()},
	})
	if !errors.Is(err, ErrDestinationWrite) || result.State != StateFailedAfterCommit || result.BytesWritten != 3 || result.RetryAllowed || !result.TransportCommitted {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionDownstreamClosed || result.Handoff.Usage.ErrorMessage != "下游连接关闭" {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
	if result.Handoff.Audit.ErrorPhase != "downstream" {
		t.Fatalf("audit = %#v", result.Handoff.Audit)
	}
	if _, ok := result.Handoff.Usage.ResponseSnapshot.(*GuardSummary); !ok {
		t.Fatalf("response snapshot = %#v", result.Handoff.Usage.ResponseSnapshot)
	}
}

func TestHandleJSONPassesDispatcherBodyLimitIntoGuard(t *testing.T) {
	raw := `{"object":"response","output":[],"padding":"` + strings.Repeat("x", 16<<20) + `"}`
	body := &trackingBody{Reader: strings.NewReader(raw)}
	sink := &countingSink{}
	handler := Handler{Dispatcher: gatewaydispatch.Dispatcher{MaxResponseBodyBytes: 20 << 20}, Now: func() time.Time { return testNow }}
	result, err := handler.Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON, Sink: sink,
		Codex: &CodexGuard{Mode: codexresponses.ModeStrictIntercept, Checkpoint: RawUpstreamCheckpoint()},
	})
	if err != nil || result.State != StateSucceeded || sink.written != int64(len(raw)) {
		t.Fatalf("result/error/written = %#v/%v/%d", result, err, sink.written)
	}
}

func TestHandleJSONRejectsSemanticInitialCommitBeforeReading(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"object":"response","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`)}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{&bytes.Buffer{}}, InitialCommit: codexresponses.CommitState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 12},
		Codex: &CodexGuard{Mode: codexresponses.ModeSafeRepair, Checkpoint: RawUpstreamCheckpoint()},
	})
	if !errors.Is(err, ErrJSONAlreadyCommitted) || result.State != StateFailedAfterCommit || result.RetryAllowed || !result.TransportCommitted || !result.SemanticCommitted || result.Guard != nil {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestHandleJSONAllowsHeaderOnlyInitialCommit(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"ok":true}`)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{sink}, InitialCommit: codexresponses.CommitState{TransportCommitted: true},
	})
	if err != nil || result.State != StateSucceeded || !result.TransportCommitted || !result.SemanticCommitted || result.Handoff.Commit.DownstreamBytes != int64(sink.Len()) || sink.String() != `{"ok":true}` {
		t.Fatalf("result/error/sink = %#v/%v/%q", result, err, sink.String())
	}
}

func TestHandleBufferedReadFailureAllowsPrecommitRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		transport Transport
		status    int
	}{
		{name: "json", transport: TransportJSON, status: http.StatusOK},
		{name: "stream error response", transport: TransportStream, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &failingBody{err: errors.New("connection reset")}
			result, err := testHandler().Handle(Input{
				Context: context.Background(), Dispatch: dispatchResult(test.status, body), Transport: test.transport,
				Sink: sinkAdapter{&bytes.Buffer{}},
			})
			if !errors.Is(err, gatewaydispatch.ErrResponseBodyRead) || !body.closed || !result.RetryAllowed || result.Handoff.Retry.Failure.ResponseSignal != gatewayretry.ResponseSignalStreamInterrupted || result.Handoff.Audit.ErrorPhase != "upstream_response" {
				t.Fatalf("result/error/body = %#v/%v/%v", result, err, body.closed)
			}
		})
	}
}

func TestHandleJSONCloseOnlyFailureIsGatewayCleanup(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"ok":true}`), closeErr: errors.New("close failed")}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{&bytes.Buffer{}},
	})
	if !errors.Is(err, gatewaydispatch.ErrResponseBodyClose) || result.RetryAllowed || result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionGatewayPolicy || result.Handoff.Audit.ErrorPhase != "gateway" {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestHandleStreamSafeRepairUsesDispatcherRelay(t *testing.T) {
	stream := ": keepalive\n\n" + `data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}}` + "\n\n"
	body := &trackingBody{Reader: strings.NewReader(stream)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportStream,
		Sink: sinkAdapter{sink}, StartedAt: testNow.Add(-time.Second),
		Codex: &CodexGuard{Mode: codexresponses.ModeSafeRepair, Checkpoint: GatewayBridgeCheckpoint(),
			CreateItemID: func(prefix, _ string, _, _ int) string { return prefix + "_stream_guarded" }},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !body.closed || result.Stream == nil || result.Stream.State != gatewaystreamrelay.StateCompleted || result.Guard == nil || result.Guard.Outcome != codexresponses.OutcomeRepairedBridge {
		t.Fatalf("result/body = %#v/%v", result, body.closed)
	}
	if !strings.Contains(sink.String(), `"id":"ctc_stream_guarded"`) || result.Handoff.Usage.Outcome != gatewayusage.OutcomeSucceeded {
		t.Fatalf("sink/handoff = %s/%#v", sink.String(), result.Handoff)
	}
	if !strings.HasPrefix(sink.String(), ": keepalive\n\n") {
		t.Fatalf("keepalive was not preserved: %q", sink.String())
	}
	if _, ok := result.Handoff.Usage.ResponseSnapshot.(*GuardSummary); !ok {
		t.Fatalf("stream response snapshot = %#v", result.Handoff.Usage.ResponseSnapshot)
	}
}

func TestHandleStreamStrictBlocksAndInvalidConfigClosesBody(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}}` + "\n\n"
	body := &trackingBody{Reader: strings.NewReader(stream)}
	sink := &bytes.Buffer{}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportStream,
		Sink: sinkAdapter{sink}, Codex: &CodexGuard{Mode: codexresponses.ModeStrictIntercept, Checkpoint: RawUpstreamCheckpoint()},
	})
	if err == nil || !body.closed || sink.Len() != 0 || result.Stream == nil || !result.RetryAllowed || result.Guard == nil {
		t.Fatalf("result/error/body/sink = %#v/%v/%v/%q", result, err, body.closed, sink.String())
	}

	invalidBody := &trackingBody{Reader: strings.NewReader(stream)}
	_, err = testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, invalidBody), Transport: TransportStream,
		Sink: sinkAdapter{&bytes.Buffer{}}, Codex: &CodexGuard{Mode: codexresponses.ModeSafeRepair, Checkpoint: Checkpoint{}},
	})
	if !errors.Is(err, ErrInvalidCodexCheckpoint) || !invalidBody.closed {
		t.Fatalf("invalid config error/body = %v/%v", err, invalidBody.closed)
	}
}

func TestHandleStreamInvalidRelayLimitsProducesFailureHandoffAndClosesBody(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("opaque")}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportStream,
		Sink: sinkAdapter{&bytes.Buffer{}}, RelayOptions: gatewaystreamrelay.Options{Limits: gatewaystreamrelay.Limits{MaxBytes: -1}},
	})
	if !errors.Is(err, gatewaystreamrelay.ErrInvalidLimits) || !body.closed || result.State != StateFailedBeforeCommit || result.Handoff.Usage.Outcome != gatewayusage.OutcomeFailed || result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionGatewayPolicy || result.Handoff.Audit.ErrorPhase != "gateway" {
		t.Fatalf("result/error/body = %#v/%v/%v", result, err, body.closed)
	}
}

func TestHandleStreamCloseErrorCannotRemainSuccessful(t *testing.T) {
	closeErr := errors.New("close failed")
	stream := `data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}` + "\n\n"
	body := &trackingBody{Reader: strings.NewReader(stream), closeErr: closeErr}
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportStream,
		Sink: sinkAdapter{&bytes.Buffer{}}, Codex: &CodexGuard{Mode: codexresponses.ModeShadow, Checkpoint: RawUpstreamCheckpoint()},
	})
	if !errors.Is(err, gatewaydispatch.ErrResponseBodyClose) || !errors.Is(err, closeErr) || result.State != StateFailedAfterCommit || result.RetryAllowed || result.Handoff.Usage.Outcome != gatewayusage.OutcomeFailed || result.Handoff.Commit.DownstreamBytes != result.BytesWritten || result.Handoff.Commit.DownstreamBytes == 0 || result.Handoff.Audit.ErrorPhase != "gateway" || !result.Handoff.Audit.TerminalRequired || !result.Handoff.Audit.TerminalReceived {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestHandleStreamHeaderCommitFencesZeroByteWrite(t *testing.T) {
	writer := &zeroWriteResponseWriter{header: make(http.Header)}
	sink, _ := gatewaydownstream.NewHTTPWriterSink(writer)
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportStream, Sink: sink,
		Dispatch: gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &trackingBody{Reader: strings.NewReader("data: visible\n\n")}}},
	})
	if err == nil || result.State != StateFailedAfterCommit || result.RetryAllowed || !result.TransportCommitted || result.BytesWritten != 0 || writer.status != http.StatusOK || !sink.Snapshot().TransportCommitted {
		t.Fatalf("result/error/writer/state = %+v/%v/%+v/%+v", result, err, writer, sink.Snapshot())
	}
}

func TestHandleEmptyStreamRemainsPreCommit(t *testing.T) {
	flushErr := errors.New("flush failed")
	writer := &flushErrorResponseWriter{header: make(http.Header), err: flushErr}
	sink, _ := gatewaydownstream.NewHTTPWriterSink(writer)
	result, err := testHandler().Handle(Input{
		Context: context.Background(), Transport: TransportStream, Sink: sink,
		Dispatch: gatewaydispatch.Result{Response: &http.Response{StatusCode: http.StatusOK, Body: &trackingBody{Reader: strings.NewReader("")}}},
	})
	if !errors.Is(err, gatewaystreamrelay.ErrPreCommitEvidenceMissing) || errors.Is(err, flushErr) || result.State != StateFailedBeforeCommit || result.TransportCommitted || !result.RetryAllowed || writer.status != 0 {
		t.Fatalf("result/error/writer = %+v/%v/%+v", result, err, writer)
	}
}

func TestFinishStreamFailureExposesTypedCommittedDisposition(t *testing.T) {
	t.Parallel()
	handler := testHandler()
	committed := Result{
		State: StateFailedAfterCommit, TransportCommitted: true, SemanticCommitted: true,
		Handoff: Handoff{Commit: codexresponses.CommitState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 12}},
		Stream:  &gatewaystreamrelay.Result{},
	}
	precise, _ := handler.finishStreamFailure(Input{CommittedFailureSignal: gatewaystreamrelay.CommittedFailureSignalProtocolEvent}, committed, gatewaystreamrelay.ErrSourceRead)
	if precise.TerminalDisposition == nil || !precise.TerminalDisposition.EmitControlledEvent || precise.TerminalDisposition.Disconnect || precise.TerminalDisposition.RetryUpstream {
		t.Fatalf("precise disposition=%#v", precise.TerminalDisposition)
	}
	generic, _ := handler.finishStreamFailure(Input{}, committed, gatewaystreamrelay.ErrSourceRead)
	if generic.TerminalDisposition == nil || generic.TerminalDisposition.EmitControlledEvent || !generic.TerminalDisposition.Disconnect || generic.TerminalDisposition.RetryUpstream {
		t.Fatalf("generic disposition=%#v", generic.TerminalDisposition)
	}
	completedTerminal := committed
	completedTerminal.Stream = &gatewaystreamrelay.Result{Inspection: gatewaystreamrelay.Inspection{TerminalReceived: true}}
	completed, _ := handler.finishStreamFailure(Input{CommittedFailureSignal: gatewaystreamrelay.CommittedFailureSignalProtocolEvent}, completedTerminal, gatewaystreamrelay.ErrSourceRead)
	if completed.TerminalDisposition == nil || completed.TerminalDisposition.EmitControlledEvent || !completed.TerminalDisposition.Disconnect {
		t.Fatalf("completed terminal disposition=%#v", completed.TerminalDisposition)
	}
	precommit := Result{State: StateFailedBeforeCommit, RetryAllowed: true, Stream: &gatewaystreamrelay.Result{}}
	retried, _ := handler.finishStreamFailure(Input{CommittedFailureSignal: gatewaystreamrelay.CommittedFailureSignalProtocolEvent}, precommit, gatewaystreamrelay.ErrMissingTerminal)
	if retried.TerminalDisposition == nil || !retried.TerminalDisposition.RetryUpstream || retried.TerminalDisposition.EmitControlledEvent || retried.TerminalDisposition.Disconnect {
		t.Fatalf("precommit disposition=%#v", retried.TerminalDisposition)
	}
}

func TestResponsePolicyFactsUseValidBoundedUTF8(t *testing.T) {
	message := strings.Repeat("界", 100) + "🙂"
	facts := parseResponsePolicyFacts([]byte(`{"error":{"message":"` + message + `"}}`))
	if len(facts.ErrorMessage) > 256 || !utf8.ValidString(facts.ErrorMessage) {
		t.Fatalf("message bytes/valid = %d/%v", len(facts.ErrorMessage), utf8.ValidString(facts.ErrorMessage))
	}
}

func TestHandleRejectsInvalidInitialCommitAndClosesBody(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"ok":true}`)}
	_, err := testHandler().Handle(Input{
		Context: context.Background(), Dispatch: dispatchResult(http.StatusOK, body), Transport: TransportJSON,
		Sink: sinkAdapter{&bytes.Buffer{}}, InitialCommit: codexresponses.CommitState{DownstreamBytes: 1},
	})
	if !errors.Is(err, ErrInvalidInitialCommit) || !body.closed {
		t.Fatalf("error/body = %v/%v", err, body.closed)
	}
}

func testHandler() Handler {
	return Handler{Dispatcher: gatewaydispatch.Dispatcher{MaxResponseBodyBytes: 1024 * 1024}, Now: func() time.Time { return testNow }}
}

func dispatchResult(status int, body io.ReadCloser) gatewaydispatch.Result {
	return gatewaydispatch.Result{Response: &http.Response{StatusCode: status, Body: body}}
}

var testNow = time.Date(2026, 7, 23, 6, 0, 0, 0, time.UTC)

type trackingBody struct {
	*strings.Reader
	closed   bool
	closeErr error
}

type zeroWriteResponseWriter struct {
	header http.Header
	status int
}

func (w *zeroWriteResponseWriter) Header() http.Header       { return w.header }
func (w *zeroWriteResponseWriter) WriteHeader(status int)    { w.status = status }
func (w *zeroWriteResponseWriter) Write([]byte) (int, error) { return 0, nil }

type flushErrorResponseWriter struct {
	header http.Header
	status int
	err    error
}

func (w *flushErrorResponseWriter) Header() http.Header            { return w.header }
func (w *flushErrorResponseWriter) WriteHeader(status int)         { w.status = status }
func (w *flushErrorResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *flushErrorResponseWriter) FlushError() error              { return w.err }

type failingCommitSink struct{ err error }

func (s *failingCommitSink) Stage(gatewaydownstream.Plan) error { return nil }
func (s *failingCommitSink) Commit(context.Context) error       { return s.err }
func (s *failingCommitSink) Write(context.Context, []byte) (int, error) {
	return 0, errors.New("write should not be called")
}
func (s *failingCommitSink) MarkSemantic() {}
func (s *failingCommitSink) Snapshot() gatewaystreamrelay.SinkState {
	return gatewaystreamrelay.SinkState{}
}

func (b *trackingBody) Close() error { b.closed = true; return b.closeErr }

type failingBody struct {
	err    error
	closed bool
}

func (b *failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b *failingBody) Close() error             { b.closed = true; return nil }

type sinkAdapter struct{ *bytes.Buffer }

func (s sinkAdapter) Write(_ context.Context, p []byte) (int, error) { return s.Buffer.Write(p) }

type partialSink struct {
	limit int
	err   error
}

type countingSink struct{ written int64 }

func (s *countingSink) Write(_ context.Context, p []byte) (int, error) {
	s.written += int64(len(p))
	return len(p), nil
}

func (s *partialSink) Write(_ context.Context, p []byte) (int, error) {
	return min(s.limit, len(p)), s.err
}
