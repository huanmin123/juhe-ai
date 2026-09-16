package gatewayresponse

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// inspection.go firstPositiveMatch 全匹配维度。
// ---------------------------------------------------------------------------

func TestW9CFirstPositiveMatchAllDimensions(t *testing.T) {
	// Error messages include.
	msgMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorMessagesIncludes: []string{"上下文"}}
	got := firstPositiveMatch(gatewayproto.SemanticFrame{ErrorMessage: "请求上下文长度超限"}, msgMatch)
	if got == nil || got.field != "errorMessageIncludes" {
		t.Fatalf("message match = %+v", got)
	}
	if firstPositiveMatch(gatewayproto.SemanticFrame{ErrorMessage: ""}, msgMatch) != nil {
		t.Fatal("empty message must not match")
	}
	// Finish reasons (falls back to frame status).
	finishMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{FinishReasons: []string{"length"}}
	if got := firstPositiveMatch(gatewayproto.SemanticFrame{FinishReason: "length"}, finishMatch); got == nil {
		t.Fatal("finish reason must match")
	}
	if got := firstPositiveMatch(gatewayproto.SemanticFrame{Status: "length"}, finishMatch); got == nil {
		t.Fatal("status fallback must match")
	}
	if firstPositiveMatch(gatewayproto.SemanticFrame{FinishReason: "stop", Status: "stop"}, finishMatch) != nil {
		t.Fatal("different finish reason must not match")
	}
	// JSON paths.
	pathMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{JSONPathsExists: []string{"error.code"}}
	frame := gatewayproto.SemanticFrame{RawJSON: map[string]any{"error": map[string]any{"code": "x"}}}
	if got := firstPositiveMatch(frame, pathMatch); got == nil || got.field != "jsonPathsExists" {
		t.Fatalf("json path match = %+v", got)
	}
	if firstPositiveMatch(gatewayproto.SemanticFrame{RawJSON: map[string]any{}}, pathMatch) != nil {
		t.Fatal("missing path must not match")
	}
	// Raw text includes.
	rawMatch := gatewayruntimecache.ResponseInspectionPolicyMatch{RawTextIncludes: []string{"needle"}}
	got = firstPositiveMatch(gatewayproto.SemanticFrame{RawText: "haystack needle here"}, rawMatch)
	if got == nil || got.value != "needle" || got.snippet == "" {
		t.Fatalf("raw text match = %+v", got)
	}
	if firstPositiveMatch(gatewayproto.SemanticFrame{RawText: ""}, rawMatch) != nil {
		t.Fatal("empty raw text must not match")
	}
	// Combined dimensions pick the first matching snippet.
	combined := gatewayruntimecache.ResponseInspectionPolicyMatch{
		ErrorCodes: []string{"ctx_exceeded"},
		ErrorTypes: []string{"invalid_request_error"},
	}
	combinedFrame := gatewayproto.SemanticFrame{ErrorCode: "ctx_exceeded", ErrorType: "invalid_request_error"}
	got = firstPositiveMatch(combinedFrame, combined)
	if got == nil || got.field != "errorCodes" {
		t.Fatalf("combined match = %+v", got)
	}
	// Matched entries with empty snippets still return the first entry.
	emptySnippet := gatewayruntimecache.ResponseInspectionPolicyMatch{FinishReasons: []string{"length"}}
	got = firstPositiveMatch(gatewayproto.SemanticFrame{FinishReason: "length", Status: ""}, emptySnippet)
	if got == nil {
		t.Fatal("finish-only match must return")
	}
	// No dimensions configured: nil.
	if firstPositiveMatch(gatewayproto.SemanticFrame{}, gatewayruntimecache.ResponseInspectionPolicyMatch{}) != nil {
		t.Fatal("empty match must yield nil")
	}
}

// ---------------------------------------------------------------------------
// nonstream.go handleNonStreamPipeError + StreamDownstream.InterruptNow。
// ---------------------------------------------------------------------------

func TestW9CHandleNonStreamPipeError(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody(), 200, nil)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	// Downstream-closed arm (client abort signal): usage + audit records are
	// written and the pipe error is rethrown to the caller.
	audit := input.AuditCapture.(*mockAuditCapture)
	closed := make(chan struct{})
	close(closed)
	input.Signal = chanSignal{ch: closed}
	result, err := input.handleNonStreamPipeError(errors.New("read boom"))
	if err == nil {
		t.Fatal("pipe error must propagate")
	}
	if result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	if len(usage.completed) != 1 || usage.completed[0].Success {
		t.Fatalf("downstream-closed usage records = %+v", usage.completed)
	}
	if usage.completed[0].ErrorMessage != DownstreamConnectionClosedMessage {
		t.Fatalf("message = %q", usage.completed[0].ErrorMessage)
	}
	if len(audit.completed) != 1 || audit.completed[0].Success {
		t.Fatalf("audit attempts = %+v", audit.completed)
	}

	// Plain pipe error arm: no records, error rethrown.
	input2, _ := newInputFixture(NewSliceUpstreamBody(), 200, nil)
	input2.Deps = input.Deps
	audit2 := input2.AuditCapture.(*mockAuditCapture)
	result2, err := input2.handleNonStreamPipeError(errors.New("pipe boom"))
	if err == nil || result2.AlreadyFinalized {
		t.Fatalf("plain result = %+v err = %v", result2, err)
	}
	if len(usage.completed) != 1 || len(audit2.completed) != 0 {
		t.Fatalf("plain arm must not record: usage=%d audit=%d", len(usage.completed), len(audit2.completed))
	}
}

func TestW9CStreamDownstreamInterruptAndDestroyed(t *testing.T) {
	recorder := httptest.NewRecorder()
	interrupted := 0
	downstream := StreamDownstream{
		Res:       gatewaypreauth.NewTrackingWriter(recorder),
		Interrupt: func() { interrupted++ },
		Destroyed: func() bool { return true },
	}
	downstream.InterruptNow()
	if interrupted != 1 {
		t.Fatalf("interrupt calls = %d", interrupted)
	}
	if !downstream.DestroyedNow() {
		t.Fatal("destroyed hook must surface")
	}
	// Nil hooks are inert.
	var empty StreamDownstream
	empty.InterruptNow()
	if empty.DestroyedNow() {
		t.Fatal("nil destroyed must be false")
	}
}

// ---------------------------------------------------------------------------
// 管道：BeforeDownstreamCommit 钩子 + 预提交缓冲 flush。
// ---------------------------------------------------------------------------

func TestW9CStreamPipeBeforeDownstreamCommitHook(t *testing.T) {
	recorder := &failureRecorder{}
	commits := []string{}
	hookErrors := 0
	body := NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk))
	result, err := runPipe(body, nil, StreamPipeOptions{
		ClientRetryEnabled: true,
		BeforeDownstreamCommit: func(responseResourceId string) error {
			commits = append(commits, responseResourceId)
			return nil
		},
		BeforeCommittedFailureSignal: func(CommittedStreamFailureSignalContext) error {
			hookErrors++
			return errors.New("signal hook boom")
		},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("stream must complete: %+v", result)
	}
	if len(commits) == 0 {
		t.Fatal("before-downstream-commit hook must run before writes")
	}
	// StreamBeforeDownstreamCommitError wraps the hook failure.
	inner := errors.New("commit refused")
	wrapped := &StreamBeforeDownstreamCommitError{OriginalError: inner}
	if !errorsIs(wrapped.Unwrap(), inner) || wrapped.Error() == "" {
		t.Fatal("before-downstream-commit error surface")
	}
}
