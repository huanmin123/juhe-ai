package gatewayresponse

// w11b 波次：pipefinal.go 终态族（passthrough 终态、parser skipped、
// incomplete/failed 收尾、协议失败、EOF 段拦截与 terminal 收尾）与
// nonstreaminspection.go 副作用函数的覆盖补齐。

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---- 构造工具 ----

// w11bFakeInspector 固定返回快照，绕开真实 SSE 解析以精准驱动 finalize 分支。
type w11bFakeInspector struct {
	snapshot gatewayproto.StreamInspection
	finish   gatewayproto.StreamInspection
	pushed   [][]byte
}

func (f *w11bFakeInspector) PushChunk(chunk []byte) { f.pushed = append(f.pushed, chunk) }
func (f *w11bFakeInspector) PushText(text string)   { f.pushed = append(f.pushed, []byte(text)) }
func (f *w11bFakeInspector) Finish() gatewayproto.StreamInspection {
	if f.finish.EventCount != 0 || f.finish.TerminalReceived || f.finish.FailedReceived || f.finish.Skipped {
		return f.finish
	}
	return f.snapshot
}
func (f *w11bFakeInspector) Snapshot() gatewayproto.StreamInspection { return f.snapshot }
func (f *w11bFakeInspector) DrainEventSummariesCanEndStream() bool   { return false }

// w11bPassInterceptor 透传 chunks，可注入 passthrough/intercepted/pending。
type w11bPassInterceptor struct {
	onPush    func(chunk []byte) StreamInterceptorSseResult
	onFlush   StreamInterceptorSseResult
	marked    int
	pushCalls int
}

func (i *w11bPassInterceptor) PushChunk(chunk []byte) StreamInterceptorSseResult {
	i.pushCalls++
	if i.onPush != nil {
		return i.onPush(chunk)
	}
	return StreamInterceptorSseResult{Chunks: [][]byte{chunk}}
}

func (i *w11bPassInterceptor) FlushPendingOnEOF() StreamInterceptorSseResult { return i.onFlush }
func (i *w11bPassInterceptor) MarkDownstreamWrite()                          { i.marked++ }

func w11bNewFinalPipe(t *testing.T, mutate func(*StreamPipeOptions)) (*streamPipe, *failureRecorder) {
	t.Helper()
	options := StreamPipeOptions{NowMs: func() int64 { return 1000 }}
	if mutate != nil {
		mutate(&options)
	}
	recorder := &failureRecorder{}
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody(),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options:             options,
	})
	return pipe, recorder
}

// ---- stream.go：nopStreamLogger 三面 ----

func TestW11BNopStreamLoggerFaces(t *testing.T) {
	var logger StreamLogger = nopStreamLogger{}
	logger.Debug("d", map[string]any{"k": 1}, "m")
	logger.Info("i", nil, "m")
	logger.Warn("w", nil, "m")
}

// ---- handlePassthroughTerminal：主循环与 EOF 段 ----

func TestW11BPassthroughUpstreamFailureTerminalFromLoop(t *testing.T) {
	interceptor := &w11bPassInterceptor{onPush: func(chunk []byte) StreamInterceptorSseResult {
		return StreamInterceptorSseResult{Chunks: [][]byte{chunk}, PassthroughUpstreamFailure: true}
	}}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk)),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor: interceptor,
			NowMs:       func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.PassthroughUpstreamFailure || !result.Completed {
		t.Fatalf("result = %+v", result)
	}
	if result.Message != "已原样转发上游失败终态" {
		t.Fatalf("message = %q", result.Message)
	}
	if recorder.count() != 0 {
		t.Fatalf("passthrough 不应记录失败，records = %+v", recorder.values)
	}
}

func TestW11BPassthroughUpstreamFailureTerminalAtEOF(t *testing.T) {
	// 主循环透传普通 chunk，EOF 段 FlushPendingOnEOF 才标记 passthrough。
	interceptor := &w11bPassInterceptor{
		onFlush: StreamInterceptorSseResult{
			Chunks:                     [][]byte{[]byte(chatDoneChunk)},
			PassthroughUpstreamFailure: true,
		},
	}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk)),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor: interceptor,
			NowMs:       func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.PassthroughUpstreamFailure || !result.Completed {
		t.Fatalf("result = %+v", result)
	}
}

// ---- finalizeParserSkipped ----

func TestW11BFinalizeParserSkippedSuccess(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		Skipped: true, SkipReason: "stream_inspection_max_bytes",
		EventCount: 4, LastEventType: "message",
		OutputReceived: true, TerminalReceived: true,
	}}
	pipe.completed = true
	pipe.preCommitSseEvidence.DataEventObserved = true
	result, err := pipe.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BFinalizeParserSkippedFailureBranches(t *testing.T) {
	// interpret 开启 + inspection.ErrorCode 优先。
	pipe, recorder := w11bNewFinalPipe(t, nil)
	pipe.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		Skipped: true, FailedReceived: true, ErrorMessage: "bad", ErrorCode: "orig_code",
	}}
	pipe.completed = true
	pipe.preCommitSseEvidence.DataEventObserved = true
	result, err := pipe.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.ErrorCode != "orig_code" || result.Message != "bad" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}

	// inspection.ErrorCode 优先（interpret 开启时）。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		Skipped: true, FailedReceived: true, ErrorCode: "custom_code", ErrorMessage: "m2",
	}}
	pipe2.completed = true
	pipe2.preCommitSseEvidence.DataEventObserved = true
	result, err = pipe2.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode != "custom_code" {
		t.Fatalf("errorCode = %q", result.ErrorCode)
	}

	// 无 errorCode → StreamClientFailureCode。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	pipe3.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		Skipped: true, FailedReceived: true, ErrorMessage: "m3",
	}}
	pipe3.completed = true
	pipe3.preCommitSseEvidence.DataEventObserved = true
	result, err = pipe3.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode == "" {
		t.Fatal("缺省 errorCode 应由 StreamClientFailureCode 推导")
	}
}

// ---- finalizeIncompleteOrFailed ----

func TestW11BFinalizeIncompleteOrFailedBeforeCommit(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.preCommitBuffer.Buffering = true
	// 已观察数据事件的流不再按"纯非语义帧"丢弃 pre-commit 缓冲。
	pipe.preCommitSseEvidence.DataEventObserved = true
	pipe.preCommitSseEvidence.OnlyNonSemanticFramingObserved = false
	AppendStreamPreCommitChunk(pipe.preCommitBuffer, []byte("held"))
	pipe.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		TerminalReceived: true, FailedReceived: true, ErrorMessage: "upstream boom", ErrorCode: "e1",
	}}
	pipe.completed = true
	result, err := pipe.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.Message != "upstream boom" || result.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("result = %+v", result)
	}
	if result.UncommittedResponseBody == nil || string(result.UncommittedResponseBody) != "held" {
		t.Fatal("pre-commit buffer 应保留为未提交正文")
	}
}

func TestW11BFinalizeIncompleteOrFailedAfterCommitSignaledAndInterrupted(t *testing.T) {
	// 无 buffering → 提交后；无 clientRetry → interrupted。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		TerminalReceived: true, FailedReceived: true, ErrorCode: "e2",
	}}
	pipe.completed = true
	result, err := pipe.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.ErrorCode != "upstream_protocol_failure" || result.Message != "上游流式响应返回失败终态" {
		t.Fatalf("result = %+v", result)
	}
	if pipe.lastCommittedDisposition != "interrupted" {
		t.Fatalf("disposition = %q", pipe.lastCommittedDisposition)
	}

	// clientRetry + responses_sse → 补发脱敏失败事件。
	pipe2, recorder := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.ClientRetryEnabled = true
		o.DownstreamProtocol = "responses_sse"
	})
	pipe2.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		TerminalReceived: true, FailedReceived: true, OutputReceived: true, ErrorCode: "e3",
	}}
	pipe2.completed = true
	result, err = pipe2.finalizeAfterLoop()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pipe2.lastCommittedDisposition != "signaled" {
		t.Fatalf("disposition = %q", pipe2.lastCommittedDisposition)
	}
	if recorder.count() != 1 || recorder.last().message != "上游流式响应返回失败终态" {
		t.Fatalf("records = %+v", recorder.values)
	}
	// 补发事件进入响应捕获并结束下游。
	tracking := pipe2.downstream.Res.(*gatewaypreauth.TrackingWriter)
	if !tracking.WritableEnded() {
		t.Fatal("signaled 后下游应结束")
	}
}

func TestW11BFinalizeIncompleteOrFailedHandleStreamFailureError(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	boom := errors.New("callback failed")
	pipe.input.HandleStreamFailure = func(string, string, StreamFailureContext) error { return boom }
	pipe.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{
		TerminalReceived: true, FailedReceived: true,
	}}
	pipe.completed = true
	_, err := pipe.finalizeAfterLoop()
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// ---- handleProtocolFailure 各分支 ----

func TestW11BHandleProtocolFailureBranches(t *testing.T) {
	ins := gatewayproto.StreamInspection{
		FailedReceived: true, ErrorMessage: "供应商失败", ErrorCode: "provider_error",
		EventCount: 3, RecentEventTypes: []string{"response.failed"},
	}
	// 提交前：取走 pre-commit buffer。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.preCommitBuffer.Buffering = true
	// 已观察数据事件的流不再按"纯非语义帧"丢弃 pre-commit 缓冲。
	pipe.preCommitSseEvidence.DataEventObserved = true
	pipe.preCommitSseEvidence.OnlyNonSemanticFramingObserved = false
	AppendStreamPreCommitChunk(pipe.preCommitBuffer, []byte("held"))
	result, err := pipe.handleProtocolFailure(ins, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.ErrorCode != "provider_error" {
		t.Fatalf("result = %+v", result)
	}
	if pipe.preCommitBuffer.Chunks != nil {
		t.Fatal("提交前失败应取走 pre-commit chunks")
	}

	// 提交后（非 eof）+ 前次 disposition=signaled 日志分支。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.lastCommittedDisposition = "signaled"
	if _, err := pipe2.handleProtocolFailure(ins, false); err != nil {
		t.Fatalf("err = %v", err)
	}

	// EOF pending 变体：提交前与提交后各走一遍日志分支。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	pipe3.preCommitBuffer.Buffering = true
	if _, err := pipe3.handleProtocolFailure(ins, true); err != nil {
		t.Fatalf("err = %v", err)
	}
	pipe4, _ := w11bNewFinalPipe(t, nil)
	if _, err := pipe4.handleProtocolFailure(ins, true); err != nil {
		t.Fatalf("err = %v", err)
	}

	// interpret 关闭时 errorCode 固定 upstream_protocol_failure。
	pipe5, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.InterpretProtocolFailuresSet = true
		o.InterpretProtocolFailures = false
	})
	result, err = pipe5.handleProtocolFailure(gatewayproto.StreamInspection{FailedReceived: true, ErrorCode: "should_be_overridden"}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("errorCode = %q", result.ErrorCode)
	}

	// HandleStreamFailure 回调错误透传。
	pipe6, _ := w11bNewFinalPipe(t, nil)
	boom := errors.New("hsf failed")
	pipe6.input.HandleStreamFailure = func(string, string, StreamFailureContext) error { return boom }
	if _, err := pipe6.handleProtocolFailure(ins, false); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// ---- handleInterceptedBeforeWrite ----

func TestW11BHandleInterceptedBeforeWriteBranches(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	ins := gatewayproto.StreamInspection{OutputReceived: true}

	// nil decision 与 DownstreamWritten=true：不接管。
	if _, _, handled := pipe.handleInterceptedBeforeWrite(nil, ins, false); handled {
		t.Fatal("nil decision 不应接管")
	}
	written := &ResponseInspectionDecision{Reason: "configured_response_policy", DownstreamWritten: true}
	if _, _, handled := pipe.handleInterceptedBeforeWrite(written, ins, false); handled {
		t.Fatal("已写下游的决策不应在此接管")
	}

	// 可服务端重试（ShouldReturn...=true）：交由上层换号。
	retryable := &ResponseInspectionDecision{
		Reason: "configured_response_policy", PolicySource: "user_policy",
		PolicyID: "p1", PolicyName: "策略一", AccountSwitch: "request_next_account", RetryEnabled: true,
	}
	result, err, handled := pipe.handleInterceptedBeforeWrite(retryable, ins, true)
	if !handled || err != nil || result == nil {
		t.Fatalf("handled=%v err=%v result=%v", handled, err, result)
	}
	if result.ResponseInspection.PolicyID != "p1" {
		t.Fatalf("inspection = %+v", result.ResponseInspection)
	}

	// 不可重试 + 提交前：按失败交由上层。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.preCommitBuffer.Buffering = true
	plain := &ResponseInspectionDecision{Reason: "before_downstream_write_response_failure", Action: "client_retry"}
	result, err, handled = pipe2.handleInterceptedBeforeWrite(plain, ins, false)
	if !handled || err != nil || result == nil {
		t.Fatalf("handled=%v err=%v result=%v", handled, err, result)
	}

	// 不可重试 + 已提交（non-buffering）：不接管，走 after chunks。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	if _, _, handled := pipe3.handleInterceptedBeforeWrite(plain, ins, false); handled {
		t.Fatal("不可重试且已过提交点时不应提前接管")
	}
}

// ---- handleInterceptedAfterChunks ----

func TestW11BHandleInterceptedAfterChunksBranches(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	if _, _, handled := pipe.handleInterceptedAfterChunks(nil, gatewayproto.StreamInspection{}); handled {
		t.Fatal("nil decision 不应接管")
	}
	decision := &ResponseInspectionDecision{
		Reason: "configured_response_policy", UpstreamErrorMessage: "命中策略", UpstreamErrorCode: "pol_code",
	}
	result, err, handled := pipe.handleInterceptedAfterChunks(decision, gatewayproto.StreamInspection{})
	if !handled || err != nil || result == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}

	// 提交前分支。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.preCommitBuffer.Buffering = true
	result, _, handled = pipe2.handleInterceptedAfterChunks(decision, gatewayproto.StreamInspection{})
	if !handled || result == nil {
		t.Fatalf("handled=%v", handled)
	}

	// 回调错误透传。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	boom := errors.New("hsf after chunks")
	pipe3.input.HandleStreamFailure = func(string, string, StreamFailureContext) error { return boom }
	_, err, handled = pipe3.handleInterceptedAfterChunks(decision, gatewayproto.StreamInspection{})
	if !handled || !errors.Is(err, boom) {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

// ---- eofFlushPhase 拦截与终态分支 ----

func TestW11BEofFlushInterceptedAfterWrite(t *testing.T) {
	// EOF pending 决策已写下游：走 handleInterceptedAfterChunks 的提交后路径。
	interceptor := &w11bPassInterceptor{
		onFlush: StreamInterceptorSseResult{Intercepted: &ResponseInspectionDecision{
			Reason: "configured_response_policy", RewriteMessage: "EOF 命中", DownstreamWritten: true,
		}},
	}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody(),
		Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor: interceptor,
			NowMs:       func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ResponseInspection == nil || result.Message != "EOF 命中" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("records = %d", recorder.count())
	}
}

func TestW11BEofFlushInterceptedBeforeCommit(t *testing.T) {
	interceptor := &w11bPassInterceptor{
		onFlush: StreamInterceptorSseResult{Intercepted: &ResponseInspectionDecision{
			Reason: "configured_response_policy", UpstreamErrorMessage: "EOF 提交前命中",
		}},
	}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody([]byte(": ping\n\n")),
		Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor:                           interceptor,
			RetryBeforeDownstreamWriteUntilOutput: true,
			NowMs:                                 func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ResponseInspection == nil || result.DownstreamBytesWritten != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BEofFlushTerminalSuccess(t *testing.T) {
	// terminal 经 EOF transformed chunks 到达：eof 终态成功分支。
	interceptor := &w11bPassInterceptor{}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk)),
		Downstream:   StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor: interceptor,
			FlushTransformedUpstreamChunks: func() [][]byte {
				return [][]byte{[]byte(chatDoneChunk)}
			},
			NowMs: func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BEofFlushEmptyReturnsNil(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{}}
	result, err := pipe.eofFlushPhase()
	if err != nil || result != nil {
		t.Fatalf("result = %v err = %v", result, err)
	}
}

// ---- finishTerminalSuccess ----

func TestW11BFinishTerminalSuccessDrainAndFailure(t *testing.T) {
	// drainForKeepAlive=true：排空迭代器后成功收尾。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.body = NewSliceUpstreamBody([]byte(chatDoneChunk))
	result, err := pipe.finishTerminalSuccess(gatewayproto.StreamInspection{
		TerminalReceived: true, OutputReceived: true, EventCount: 2,
	}, true, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}

	// drain 后矛盾失败终态（drainForKeepAlive=false + FailedReceived）。
	pipe2, recorder := w11bNewFinalPipe(t, nil)
	pipe2.body = NewSliceUpstreamBody()
	result, err = pipe2.finishTerminalSuccess(gatewayproto.StreamInspection{
		TerminalReceived: true, FailedReceived: true, ErrorMessage: "终止后失败",
	}, false, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.Message != "终止后失败" || result.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("records = %d", recorder.count())
	}

	// HandleStreamFailure 回调错误透传。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	pipe3.body = NewSliceUpstreamBody()
	boom := errors.New("fts failed")
	pipe3.input.HandleStreamFailure = func(string, string, StreamFailureContext) error { return boom }
	if _, err := pipe3.finishTerminalSuccess(gatewayproto.StreamInspection{TerminalReceived: true, FailedReceived: true}, false, false); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	// BeforeDownstreamCommit 回调错误包装。
	pipe4, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.BeforeDownstreamCommit = func(string) error { return errors.New("commit blocked") }
	})
	pipe4.body = NewSliceUpstreamBody()
	_, err = pipe4.finishTerminalSuccess(gatewayproto.StreamInspection{TerminalReceived: true, OutputReceived: true}, false, false)
	var beforeCommitErr *StreamBeforeDownstreamCommitError
	if !errors.As(err, &beforeCommitErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestW11BDrainIteratorAfterTerminalErrorAndTimeout(t *testing.T) {
	// 读错误立即结束排空。
	inspector := &w11bFakeInspector{}
	body := NewSliceUpstreamBody()
	body.Close()
	ins := drainIteratorAfterTerminalForInspection(body, inspector)
	_ = ins
	// 持续供数的 body 在 keep-alive 窗口内排空（chan body EOF）。
	chanStyle := newChanBody()
	chanStyle.end()
	ins2 := drainIteratorAfterTerminalForInspection(chanStyle, inspector)
	if ins2.EventCount != 0 {
		t.Fatalf("inspection = %+v", ins2)
	}
	// timeAfter 下限保护。
	if <-timeAfter(0) == (time.Time{}) {
		t.Fatal("timeAfter(0) 必须立即到期")
	}
}

// ---- omitBodyCaptureIfImageStream / bodyOmissionFor ----

func TestW11BOmitBodyCaptureIfImageStreamAndSummary(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.upstreamCapture.Push([]byte("up"))
	pipe.responseCapture.Push([]byte("resp"))
	pipe.omitBodyCaptureIfImageStream(gatewayproto.StreamInspection{ImageOutputReceived: true}, true)
	if !pipe.bodyCaptureOmitted {
		t.Fatal("图像流应省略正文捕获")
	}
	if len(pipe.upstreamCapture.Buffer()) != 0 || len(pipe.responseCapture.Buffer()) != 0 {
		t.Fatal("捕获缓冲应被清空")
	}
	// 重复调用应幂等。
	pipe.omitBodyCaptureIfImageStream(gatewayproto.StreamInspection{ImageOutputReceived: true}, false)
	summary := pipe.bodyOmissionFor(gatewayproto.StreamInspection{EventCount: 5, LastEventType: "image"})
	if summary == nil || summary.Reason != "image_stream_payload" {
		t.Fatalf("summary = %+v", summary)
	}
	// 未省略时 summary 为 nil。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	if pipe2.bodyOmissionFor(gatewayproto.StreamInspection{EventCount: 1}) != nil {
		t.Fatal("未省略捕获时 summary 应为 nil")
	}
	// 空 inspection 回退 lastInspection。
	pipe.lastInspection = gatewayproto.StreamInspection{EventCount: 9, ImageOutputReceived: true}
	if summary := pipe.bodyOmissionFor(gatewayproto.StreamInspection{}); summary.SseEventCount != 9 {
		t.Fatalf("summary = %+v", summary)
	}
}

// ---- flushPreCommitChunks / writeGatewayStreamFailureEvent 写失败 ----

type w11bFailingWriter struct {
	inner *gatewaypreauth.TrackingWriter
}

func (w *w11bFailingWriter) Header() http.Header       { return w.inner.Header() }
func (w *w11bFailingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (w *w11bFailingWriter) WriteHeader(int)           {}
func (w *w11bFailingWriter) HeadersSent() bool         { return false }
func (w *w11bFailingWriter) StatusCode() int           { return 0 }

func TestW11BFlushPreCommitChunksWriteError(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	AppendStreamPreCommitChunk(pipe.preCommitBuffer, []byte("chunk"))
	err := pipe.flushPreCommitChunks()
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("err = %v", err)
	}
	// 空 buffer no-op。
	pipe.preCommitBuffer.Chunks = nil
	if err := pipe.flushPreCommitChunks(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestW11BWriteGatewayStreamFailureEventFailingWriter(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.DownstreamProtocol = "responses_sse"
	})
	pipe.downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	if event := pipe.writeGatewayStreamFailureEventWithBackpressure("m", "c"); event != nil {
		t.Fatalf("写失败应返回 nil，got %q", event)
	}
	// 非 responses_sse 的 OpenAI 协议无补发事件。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	if event := pipe2.writeGatewayStreamFailureEventWithBackpressure("m", "c"); event != nil {
		t.Fatalf("无协议事件应返回 nil，got %q", event)
	}
}

// ---- nonstreaminspection.go 副作用函数 ----

func TestW11BApplyInspectionObservations(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{AccountEffects: effects}
	audit := input.AuditCapture.(*mockAuditCapture)
	input.applyInspectionObservations(nil) // 空观察直接返回
	input.applyInspectionObservations([]ResponseInspectionDecision{
		{PolicyID: "ob-1"}, {PolicyID: "ob-2"},
	})
	if len(effects.sideEffects) != 2 {
		t.Fatalf("sideEffects = %v", effects.sideEffects)
	}
	if len(audit.metadata) != 1 {
		t.Fatalf("metadata = %+v", audit.metadata)
	}
	if audit.metadata[0]["__label"] != "response_inspection_observations" {
		t.Fatalf("label = %v", audit.metadata[0]["__label"])
	}
}

type w11bFailingAccountEffects struct {
	mockAccountEffects
}

func (m *w11bFailingAccountEffects) ApplyInspectionPolicySideEffects(*ResponseInspectionDecision, AccountView, bool) error {
	return errors.New("side effect failed")
}

func TestW11BApplyInspectionDecisionSideEffectsError(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	input.Deps = &FinalizationDeps{AccountEffects: &w11bFailingAccountEffects{}}
	// 错误仅记录 warn，不透传。
	input.applyInspectionDecisionSideEffects(&ResponseInspectionDecision{PolicyID: "p"})

	input2, _ := newInputFixture(nil, 200, nil)
	input2.Deps = nil // Deps 缺失直接返回。
	input2.applyInspectionDecisionSideEffects(&ResponseInspectionDecision{})
}

func TestW11BFinalizeAuditWithExtrasFallback(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	audit := input.AuditCapture.(*mockAuditCapture)
	input.finalizeAuditWithExtras(gatewaypreauth.AuditFinalizeInput{Outcome: "upstream_failed"}, AuditFinalizeExtras{AccountID: "acc-1"})
	if len(audit.finalized) != 1 {
		t.Fatalf("finalized = %+v", audit.finalized)
	}
	// 扩展面：extenderAuditCapture（w4a 定义）走 FinalizeExtended。
	input2, _ := newInputFixture(nil, 200, nil)
	extender := &extenderAuditCapture{mockAuditCapture: newMockAuditCapture()}
	input2.AuditCapture = extender
	input2.finalizeAuditWithExtras(gatewaypreauth.AuditFinalizeInput{}, AuditFinalizeExtras{AccountID: "acc-2"})
	if len(extender.extended) != 1 || extender.extended[0].AccountID != "acc-2" {
		t.Fatalf("extended = %+v", extender.extended)
	}
}

func TestW11BForgetSessionAffinityForFailure(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{AccountEffects: effects}
	input.SessionAffinityKey = "sess-key"
	input.forgetSessionAffinityForFailure()
	if len(effects.affinity) != 1 || effects.affinity[0] != "sess-key:acc-1" {
		t.Fatalf("affinity = %v", effects.affinity)
	}
	input.Deps = nil
	input.forgetSessionAffinityForFailure() // nil Deps 安全
}

// ---- auditUpstreamBodyForResult 分支 ----

func TestW11BAuditUpstreamBodyForResultBranches(t *testing.T) {
	capture := NewLimitedCapture(1024)
	capture.Push([]byte("body"))
	omission := &StreamBodyOmissionSummary{Reason: "image_stream_payload"}
	if got := auditUpstreamBodyForResult(capture, true, true, omission); got != nil {
		t.Fatalf("omission 应返回 nil，got %q", got)
	}
	if got := auditUpstreamBodyForResult(capture, true, true, nil); string(got) != "body" {
		t.Fatalf("completed+capture = %q", got)
	}
	if got := auditUpstreamBodyForResult(capture, false, true, nil); string(got) != "body" {
		t.Fatalf("incomplete+capture = %q", got)
	}
	// 未完成 + 不捕获成功载荷 → 诊断捕获。
	diagnostic := NewLimitedCapture(1024)
	diagnostic.Push([]byte("diag"))
	if got := auditUpstreamBodyForResult(diagnostic, false, false, nil); string(got) != "diag" {
		t.Fatalf("diagnostic = %q", got)
	}
	if got := auditUpstreamBodyForResult(NewLimitedCapture(-1), true, false, nil); got != nil {
		t.Fatalf("completed+no capture = %q", got)
	}
	_ = io.EOF
}
