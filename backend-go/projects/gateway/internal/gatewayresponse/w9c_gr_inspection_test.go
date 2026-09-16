package gatewayresponse

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// nonstream.go / inspection.go 纯函数缺口。
// ---------------------------------------------------------------------------

func TestW9CNonStreamSmallHelpers(t *testing.T) {
	// jsonBytesOf.
	if got := jsonBytesOf(map[string]any{"a": 1.0}); string(got) != `{"a":1}` {
		t.Fatalf("jsonBytesOf = %s", got)
	}
	if jsonBytesOf(make(chan int)) != nil {
		t.Fatal("unmarshalable values yield nil")
	}

	// markFirstOutputOnce.
	marked := false
	calls := 0
	markFirstOutputOnce(&marked, func() { calls++ })
	markFirstOutputOnce(&marked, func() { calls++ })
	if !marked || calls != 1 {
		t.Fatalf("marked=%v calls=%d", marked, calls)
	}
	remaining := false
	markFirstOutputOnce(&remaining, nil)
	if remaining {
		t.Fatal("nil callback must not mark")
	}

	// Image payload omission.
	imageBody := `{"data":[{"type":"image_generation_call","result":"aGVsbG8="}]}`
	parsed := GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: map[string]any{
		"data": []any{map[string]any{"type": "image_generation_call", "result": "aGVsbG8="}},
	}}
	omission := nonStreamImageResponseBodyOmission(imageBody, []byte(imageBody), parsed)
	if omission == nil || omission.Reason != "image_json_payload" || !omission.ImageOutputReceived {
		t.Fatalf("omission = %+v", omission)
	}
	if nonStreamImageResponseBodyOmission("", nil, parsed) != nil {
		t.Fatal("empty body yields no omission")
	}
	nonImage := GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: map[string]any{"data": []any{}}}
	if nonStreamImageResponseBodyOmission(`{"data":[]}`, nil, nonImage) != nil {
		t.Fatal("non-image body yields no omission")
	}
	metadata := bodyOmissionMetadata(omission)
	if metadata["reason"] != "image_json_payload" || metadata["omitted"] != true {
		t.Fatalf("metadata = %v", metadata)
	}
	rich := &StreamBodyOmissionSummary{Reason: "r", Message: "m", SseEventCount: 3, LastSseEventType: "t", RecentSseEventTypes: []string{"a", "b"}}
	richMetadata := bodyOmissionMetadata(rich)
	if richMetadata["sseEventCount"] != 3 || richMetadata["lastSseEventType"] != "t" {
		t.Fatalf("rich metadata = %v", richMetadata)
	}

	// Known non-stream JSON paths.
	if !isKnownNonStreamJSONRequestPath("/v1/models") || !isKnownNonStreamJSONRequestPath("/v1/embeddings") ||
		!isKnownNonStreamJSONRequestPath("/v1/moderations") || !isKnownNonStreamJSONRequestPath("/v1/images/generations") ||
		!isKnownNonStreamJSONRequestPath("/v1/audio/transcriptions") {
		t.Fatal("known JSON paths must be accepted")
	}
	if isKnownNonStreamJSONRequestPath("/v1/chat/completions") {
		t.Fatal("chat path is not a known JSON-only path")
	}
}

func TestW9CFirstPositiveMatchArms(t *testing.T) {
	// outputTextIncludes requires visible text.
	match := gatewayruntimecache.ResponseInspectionPolicyMatch{OutputTextIncludes: []string{"hello"}}
	if firstPositiveMatch(gatewayproto.SemanticFrame{}, match) != nil {
		t.Fatal("empty text must not match outputTextIncludes")
	}
	hideText := gatewayproto.SemanticFrame{Text: "hello world", VisibleOutput: false}
	if firstPositiveMatch(hideText, match) != nil {
		t.Fatal("invisible output must not match")
	}
	visible := gatewayproto.SemanticFrame{Text: "say hello world", VisibleOutput: true}
	got := firstPositiveMatch(visible, match)
	if got == nil || got.value != "hello" {
		t.Fatalf("visible match = %+v", got)
	}
	// Excluded text marks the frame via outputTextExcluded.
	exclude := gatewayruntimecache.ResponseInspectionPolicyMatch{
		OutputTextIncludes: []string{"hello"}, OutputTextExcludes: []string{"world"},
	}
	if !outputTextExcluded(visible, exclude) {
		t.Fatal("excluded substring must be detected")
	}
	cleanText := gatewayproto.SemanticFrame{Text: "say hello", VisibleOutput: true}
	if outputTextExcluded(cleanText, exclude) {
		t.Fatal("non-excluded text must pass")
	}
	// Error codes and types.
	codeMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"ctx_exceeded"}}
	got = firstPositiveMatch(gatewayproto.SemanticFrame{ErrorCode: "ctx_exceeded"}, codeMatch)
	if got == nil || got.field != "errorCodes" {
		t.Fatalf("code match = %+v", got)
	}
	if firstPositiveMatch(gatewayproto.SemanticFrame{ErrorCode: "other"}, codeMatch) != nil {
		t.Fatal("different code must not match")
	}
	typeMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorTypes: []string{"invalid_request_error"}}
	if got := firstPositiveMatch(gatewayproto.SemanticFrame{ErrorType: "invalid_request_error"}, typeMatch); got == nil {
		t.Fatal("error type must match")
	}
	// policyMatchesRuntimeContext arms.
	profilePolicy := RuntimeResponseInspectionPolicy{Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ClientProfiles: []string{"codex"}}}
	if !policyMatchesRuntimeContext(profilePolicy, &ResponseInspectionRuntimeContext{ClientProfile: "codex"}) {
		t.Fatal("matching client profile must pass")
	}
	if policyMatchesRuntimeContext(profilePolicy, &ResponseInspectionRuntimeContext{ClientProfile: "generic"}) {
		t.Fatal("different client profile must fail")
	}
	if policyMatchesRuntimeContext(profilePolicy, nil) {
		t.Fatal("nil context must fail a profile-scoped policy")
	}
	errorPolicy := RuntimeResponseInspectionPolicy{
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"x"}},
	}
	if policyMatchesRuntimeContext(errorPolicy, nil) {
		t.Fatal("error-code policy without provider scope must fail")
	}
	providerScoped := RuntimeResponseInspectionPolicy{
		ScopeType: "provider", ProviderCode: "openai",
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"x"}},
	}
	if !policyMatchesRuntimeContext(providerScoped, nil) {
		t.Fatal("provider-scoped error policy must pass")
	}
}

func TestW9COpenAIInterceptorFlushPendingOnEOF(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{ClientRetryEnabled: true})
	// A partial event without the trailing blank line stays pending; EOF
	// flushes it downstream.
	result := interceptor.PushChunk([]byte("data: {\"id\":\"partial\""))
	if len(result.Chunks) != 0 {
		t.Fatalf("incomplete event must stay pending: %+v", result)
	}
	flushed := interceptor.FlushPendingOnEOF()
	joined := ""
	for _, chunk := range nonEmptyChunks(flushed.Chunks) {
		joined += string(chunk)
	}
	if !strings.Contains(joined, "partial") {
		t.Fatalf("flush must release the pending event: %q (%+v)", joined, flushed)
	}
	// A second flush is empty.
	if chunks := interceptor.FlushPendingOnEOF(); len(nonEmptyChunks(chunks.Chunks)) != 0 {
		t.Fatalf("second flush must be empty: %+v", chunks)
	}
}

func nonEmptyChunks(chunks [][]byte) [][]byte {
	out := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk) > 0 {
			out = append(out, chunk)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 流管道：下游提交后的协议失败 → 补发失败事件。
// ---------------------------------------------------------------------------

func TestW9CStreamPipeCommittedFailureSignalsProtocolEvent(t *testing.T) {
	recorder := &failureRecorder{}
	abortHooks := 0
	body := NewSliceUpstreamBody([]byte(chatDeltaChunk), contextWindowErrorChunk)
	result, err := runPipe(body, nil, StreamPipeOptions{
		ClientRetryEnabled: true,
		DownstreamProtocol: "chat_completions_sse",
		BeforeCommittedFailureSignal: func(CommittedStreamFailureSignalContext) error {
			abortHooks++
			return nil
		},
		OnIncompleteClientAbort: func(IncompleteClientAbortContext) error {
			abortHooks += 10
			return nil
		},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("protocol failure must not complete: %+v", result)
	}
	if recorder.count() == 0 {
		t.Fatal("failure must be recorded")
	}
	last := recorder.last()
	if last.errorCode == "" {
		t.Fatalf("failure record = %+v", last)
	}
	// The committed-failure signal context ran (not the incomplete-abort one).
	if abortHooks == 0 || abortHooks >= 10 {
		t.Fatalf("hook calls = %d", abortHooks)
	}
	// Output was received before the failure, so the failure context marks it.
	if !last.context.OutputReceived {
		t.Fatalf("context = %+v", last.context)
	}
	_ = gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
}
