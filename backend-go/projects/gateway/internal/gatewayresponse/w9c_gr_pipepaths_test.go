package gatewayresponse

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// 管道级缺口：parser skipped / 拦截发生在已写下游之后 / finalize 预提交失败
// 事件写客户端。
// ---------------------------------------------------------------------------

func TestW9CStreamPipeParserSkippedOnNonSSEBody(t *testing.T) {
	recorder := &failureRecorder{}
	// Plain JSON text on a stream pipe: the SSE parser skips the body and the
	// bytes pass through untouched.
	result, err := runPipe(NewSliceUpstreamBody([]byte(`{"ignored":true}`)), nil, StreamPipeOptions{
		ClientRetryEnabled:         true,
		ResponseInspectionPolicies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		ResponseInspectionContext:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result.ResponseBodyText, "ignored") {
		t.Fatalf("skipped body must pass through: %q", result.ResponseBodyText)
	}
	if result.Completed {
		t.Fatalf("non-SSE passthrough cannot validate protocol: %+v", result)
	}
}

func TestW9CStreamPipeInterceptionAfterDownstreamWrite(t *testing.T) {
	recorder := &failureRecorder{}
	// Without RetryBeforeDownstreamWriteUntilOutput the delta chunk is already
	// written downstream when the policy intercepts the error event.
	result, err := runPipe(NewSliceUpstreamBody([]byte(chatDeltaChunk), contextWindowErrorChunk), nil, StreamPipeOptions{
		ClientRetryEnabled:         true,
		DownstreamProtocol:         "chat_completions_sse",
		ResponseInspectionPolicies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		ResponseInspectionContext:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("intercepted stream must not complete: %+v", result)
	}
	if result.DownstreamBytesWritten == 0 {
		t.Fatal("delta chunk must reach downstream before interception")
	}
	if result.ResponseInspection == nil || result.ResponseInspection.PolicyID != "default_openai_context_window_error" {
		t.Fatalf("inspection = %+v", result.ResponseInspection)
	}
}

func TestW9CWritePreCommitStreamFailureToClient(t *testing.T) {
	driver := NewOpenAIResponseDriver()
	newInput := func() (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		input := HandleUpstreamResponseInput{
			Downstream:       StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)},
			UpstreamResponse: &GatewayUpstreamResponse{Status: 200, Header: httpHeaderOf(nil)},
			ClientStrategy: &ClientStrategyView{
				ClientProfile:               "codex",
				DownstreamProtocol:          "responses_sse",
				RetryPreCommitProtocolError: true,
			},
		}
		return input, recorder
	}

	// Full arm: uncommitted body plus the failure event reach the client.
	input, recorder := newInput()
	pipeResult := StreamPipeResult{
		ErrorCode:               "upstream_retryable_error",
		Message:                 "上游失败",
		UncommittedResponseBody: []byte("data: {\"partial\":true}\n\n"),
	}
	written := writePreCommitStreamFailureToClient(&input, pipeResult, driver)
	if written == nil {
		t.Fatal("failure event must be written")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "partial") || !strings.Contains(body, "upstream_retryable_error") {
		t.Fatalf("client body = %q", body)
	}
	if !input.Downstream.Res.HeadersSent() {
		t.Fatal("headers must be sent with the failure event")
	}

	// Semantic-committed results never rewrite the client.
	input2, recorder2 := newInput()
	committed := pipeResult
	committed.SemanticCommitted = true
	if writePreCommitStreamFailureToClient(&input2, committed, driver) != nil {
		t.Fatal("committed results must not write")
	}
	if recorder2.Body.Len() != 0 {
		t.Fatalf("committed client body = %q", recorder2.Body.String())
	}

	// Missing error code short-circuits.
	input3, recorder3 := newInput()
	if writePreCommitStreamFailureToClient(&input3, StreamPipeResult{Message: "m"}, driver) != nil {
		t.Fatal("missing error code must skip the write")
	}
	if recorder3.Body.Len() != 0 {
		t.Fatal("missing error code writes nothing")
	}

	// Already-writable-ended downstream short-circuits.
	input4, recorder4 := newInput()
	input4.Downstream.WritableEndedOverride = func() bool { return true }
	if writePreCommitStreamFailureToClient(&input4, pipeResult, driver) != nil {
		t.Fatal("ended downstream must skip the write")
	}
	if recorder4.Body.Len() != 0 {
		t.Fatal("ended downstream writes nothing")
	}
}
