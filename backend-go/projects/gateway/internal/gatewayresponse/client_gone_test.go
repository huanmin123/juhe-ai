package gatewayresponse

// 2026-11-12 计费漏记修复的回归面：客户端断开后停止写下游、继续读上游到
// 终态/预算，并用实际/估算 usage 记 downstream_closed 失败行。

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestClientGoneStreamDrainsAndRecordsActualUsage(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody(
		[]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk),
	), 200, nil)
	input.Downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized {
		t.Fatalf("result = %+v, want AlreadyFinalized", result)
	}
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	audit := input.AuditCapture.(*mockAuditCapture)
	if len(audit.finalized) != 1 {
		t.Fatalf("audit finalized = %d, want 1", len(audit.finalized))
	}
	if audit.finalized[0].Outcome != "downstream_closed" || audit.finalized[0].ErrorPhase != "downstream" {
		t.Fatalf("audit final = %+v, want downstream_closed/downstream", audit.finalized[0])
	}
	row := usage.completed[0]
	if row.Success {
		t.Fatal("client gone row must be failure")
	}
	if row.ErrorCode != ClientCanceledErrorCode {
		t.Fatalf("errorCode = %q", row.ErrorCode)
	}
	if row.FailureAttribution != FailureAttributionDownstreamClosed {
		t.Fatalf("attribution = %q", row.FailureAttribution)
	}
	if !row.Stream {
		t.Fatal("stream row must carry Stream=true")
	}
	if row.Usage.OutputTokens == nil || *row.Usage.OutputTokens != 7 {
		t.Fatalf("output tokens = %v, want 7 (drained usage)", row.Usage.OutputTokens)
	}
	if row.Usage.InputTokens == nil || *row.Usage.InputTokens != 5 {
		t.Fatalf("input tokens = %v, want 5 (drained usage)", row.Usage.InputTokens)
	}
}

func TestClientGoneStreamDrainBudgetExceeded(t *testing.T) {
	pipe := w13cNewPipe(t, nil, []byte(chatDeltaChunk))
	pipe.nowMs = func() int64 { return 2000 }
	pipe.clientGone = true
	pipe.clientGoneAtMs = 1000
	pipe.clientGoneDrainBudget = 100
	result, err := pipe.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.ClientGone {
		t.Fatal("result must carry ClientGone")
	}
	if result.Completed {
		t.Fatal("budget exhausted must settle as failure")
	}
	if result.ErrorCode != ClientCanceledErrorCode {
		t.Fatalf("errorCode = %q", result.ErrorCode)
	}
}

func TestClientGoneNonStreamDrainsAndRecordsUsage(t *testing.T) {
	body := `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	handling, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !handling.DownstreamGone {
		t.Fatalf("handling = %+v, want DownstreamGone", handling)
	}
	if !handling.GatewayLocalFailure || handling.ErrorCode != ClientCanceledErrorCode {
		t.Fatalf("handling neutral markers = %+v", handling)
	}
	FinalizeHandledUpstreamResponse(input, handling)
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	audit := input.AuditCapture.(*mockAuditCapture)
	if len(audit.finalized) != 1 {
		t.Fatalf("audit finalized = %d, want 1", len(audit.finalized))
	}
	if audit.finalized[0].Outcome != "downstream_closed" || audit.finalized[0].ErrorPhase != "downstream" {
		t.Fatalf("audit final = %+v, want downstream_closed/downstream", audit.finalized[0])
	}
	row := usage.completed[0]
	if row.Success {
		t.Fatal("client gone row must be failure")
	}
	if row.ErrorCode != ClientCanceledErrorCode {
		t.Fatalf("errorCode = %q", row.ErrorCode)
	}
	if row.FailureAttribution != FailureAttributionDownstreamClosed {
		t.Fatalf("attribution = %q", row.FailureAttribution)
	}
	if row.Stream {
		t.Fatal("non-stream row must carry Stream=false")
	}
	if row.Usage.OutputTokens == nil || *row.Usage.OutputTokens != 7 {
		t.Fatalf("output tokens = %v, want 7", row.Usage.OutputTokens)
	}
}

func TestNonStreamBodyInterruptionRecordsFailureRow(t *testing.T) {
	body := newChanBody()
	go func() {
		body.push([]byte(`{"partial":`))
		body.fail(io.ErrUnexpectedEOF)
	}()
	input, _ := newInputFixture(body, 200, nil)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	if _, err := HandleNonStreamUpstreamResponse(input); err == nil {
		t.Fatal("body interruption must surface error")
	}
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	row := usage.completed[0]
	if row.Success {
		t.Fatal("body interruption row must be failure")
	}
	if row.ErrorCode != UpstreamBodyInterruptedErrorCode {
		t.Fatalf("errorCode = %q", row.ErrorCode)
	}
	if row.FailureAttribution != "account_upstream" {
		t.Fatalf("attribution = %q", row.FailureAttribution)
	}
	if row.Usage.InputTokens != nil || row.Usage.OutputTokens != nil {
		t.Fatalf("partial body must not invent usage: %+v", row.Usage)
	}
}

func TestNonStream4xxDoesNotParseErrorBodyUsage(t *testing.T) {
	body := `{"error":{"code":"rate_limit_exceeded","message":"slow down"},"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 429, map[string]string{"Content-Type": "application/json"})
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	handling, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	FinalizeHandledUpstreamResponse(input, handling)
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	row := usage.completed[0]
	if row.Success {
		t.Fatal("429 row must be failure")
	}
	if row.Usage.InputTokens != nil || row.Usage.OutputTokens != nil {
		t.Fatalf("4xx error body usage must be ignored: %+v", row.Usage)
	}
}

func TestNonStreamConfiguredDeadlineDoesNotRecordUsageRow(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte("x")), 200, nil)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	deadlineErr := &NonStreamBodyPipeError{
		OriginalError: &gatewaydispatch.GatewayFirstByteTimeoutError{Message: "配置首字截止", TimeoutMs: 1},
	}
	if _, err := input.handleNonStreamPipeError(deadlineErr); err == nil {
		t.Fatal("deadline error must surface")
	}
	if len(usage.completed) != 0 {
		t.Fatalf("configured deadline must not record usage row, got %d", len(usage.completed))
	}
	if len(input.AuditCapture.(*mockAuditCapture).completed) != 0 {
		t.Fatal("configured deadline must not complete audit attempt")
	}
}

func TestNonStreamWriteThroughClientGoneContinuesReading(t *testing.T) {
	body := NewSliceUpstreamBody(
		[]byte(`{"a":1}`),
		[]byte(`{"usage":{"prompt_tokens":5,"completion_tokens":7}}`),
	)
	input := NonStreamPipeInput{
		Body:         body,
		Downstream:   StreamDownstream{Res: &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}},
		StartedAtMs:  1000,
		CaptureBody:  true,
		CaptureBytes: 1024,
		Signal:       staticSignal(),
		NowMs:        func() int64 { return 1000 },
	}
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.DownstreamGone {
		t.Fatal("write-through failure must mark DownstreamGone")
	}
	if !strings.Contains(result.UsageTailText, "completion_tokens") {
		t.Fatalf("usage tail = %q, want drained usage bytes", result.UsageTailText)
	}
}

func TestClientGoneStreamDrainBudgetInterruptsBlockedRead(t *testing.T) {
	body := newChanBody()
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream:   StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          3_600_000,
			IdleTimeoutMs:                   3_600_000,
			UncommittedAttemptMaxLifetimeMs: 3_600_000,
		},
		StartedAtMs:         time.Now().UnixMilli(),
		HandleStreamFailure: (&failureRecorder{}).handle,
		Signal:              staticSignal(),
		Options:             StreamPipeOptions{NowMs: func() int64 { return time.Now().UnixMilli() }},
	})
	pipe.clientGoneDrainBudget = 80
	pipe.markClientGone(nil)

	done := make(chan struct{})
	var result StreamPipeResult
	var err error
	go func() {
		result, err = pipe.run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain budget did not interrupt blocked upstream read")
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.ClientGone || result.Completed || result.ErrorCode != ClientCanceledErrorCode {
		t.Fatalf("result = %+v, want clientGone budget failure", result)
	}
}

func TestClientSignalDetectedWithoutWriteFailure(t *testing.T) {
	body := newChanBody()
	input, recorder := newInputFixture(body, 200, nil)
	clientCtx, cancel := context.WithCancel(context.Background())
	cancel()
	input.ClientCtx = clientCtx
	input.Signal = context.WithoutCancel(clientCtx)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	go func() {
		time.Sleep(20 * time.Millisecond)
		body.push([]byte(chatDeltaChunk))
		body.push([]byte(chatFinishChunk))
		body.push([]byte(chatDoneChunk))
		body.end()
	}()

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized {
		t.Fatalf("result = %+v, want AlreadyFinalized", result)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("client gone must not write downstream, got %q", recorder.Body.String())
	}
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	row := usage.completed[0]
	if row.Success || row.FailureAttribution != FailureAttributionDownstreamClosed {
		t.Fatalf("row = %+v, want downstream_closed failure", row)
	}
	if row.Usage.OutputTokens == nil || *row.Usage.OutputTokens != 7 {
		t.Fatalf("output tokens = %v, want 7", row.Usage.OutputTokens)
	}
}

func TestClientSignalNonStreamDetectedWithoutWriteFailure(t *testing.T) {
	body := newChanBody()
	input, _ := newInputFixture(body, 200, map[string]string{"Content-Type": "application/json"})
	clientCtx, cancel := context.WithCancel(context.Background())
	cancel()
	input.ClientCtx = clientCtx
	input.Signal = context.WithoutCancel(clientCtx)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	go func() {
		time.Sleep(20 * time.Millisecond)
		body.push([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`))
		body.end()
	}()

	handling, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !handling.DownstreamGone {
		t.Fatalf("handling = %+v, want DownstreamGone", handling)
	}
	FinalizeHandledUpstreamResponse(input, handling)
	if len(usage.completed) != 1 {
		t.Fatalf("completed rows = %d, want 1", len(usage.completed))
	}
	row := usage.completed[0]
	if row.Success || row.FailureAttribution != FailureAttributionDownstreamClosed {
		t.Fatalf("row = %+v, want downstream_closed failure", row)
	}
	if row.Usage.OutputTokens == nil || *row.Usage.OutputTokens != 7 {
		t.Fatalf("output tokens = %v, want 7", row.Usage.OutputTokens)
	}
}
