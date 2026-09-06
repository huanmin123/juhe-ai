package gatewayresponse

import (
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 上游 body 所有权回归测试：传输层 body 所有权已下放到响应层
// （slotReleasingBody 在 EOF/Close 时 cancel+release），因此响应层管道必须在
// 所有失败终态（客户端断开 abort、response precommit deadline、一般 pipe 错误、
// HandleStreamFailure 回调错误早退、非流式 signal 中断/读错误）与 panic 面上
// 关闭上游 body；正常完成路径语义保持不变。
//
// countingBody 模拟传输层所有权语义：Close → cancel + release，各自经
// sync.Once 保证恰好一次；closeCalls 记录接口层 Close 调用次数（允许管道内
// 显式 Close 与入口 defer Close 重复调用）。

type countingBody struct {
	inner       UpstreamBody
	closeCalls  atomic.Int64
	cancelOnce  sync.Once
	releaseOnce sync.Once
	cancels     atomic.Int64
	releases    atomic.Int64
}

func newCountingBody(inner UpstreamBody) *countingBody {
	return &countingBody{inner: inner}
}

func (b *countingBody) Next() <-chan ChunkResult { return b.inner.Next() }

func (b *countingBody) Close() {
	b.closeCalls.Add(1)
	b.cancelOnce.Do(func() { b.cancels.Add(1) })
	b.releaseOnce.Do(func() { b.releases.Add(1) })
}

func (b *countingBody) assertReleasedExactlyOnce(t *testing.T, scenario string) {
	t.Helper()
	if b.releases.Load() != 1 || b.cancels.Load() != 1 {
		t.Fatalf("%s: release = %d, cancel = %d, want release 与 cancel 恰好各一次", scenario, b.releases.Load(), b.cancels.Load())
	}
	if b.closeCalls.Load() == 0 {
		t.Fatalf("%s: Close 未被调用", scenario)
	}
}

// failingFailureRecorder 让 HandleStreamFailure 回调返回错误，覆盖回调错误早退路径。
type failingFailureRecorder struct {
	failureRecorder
	err error
}

func (r *failingFailureRecorder) handle(message string, errorCode string, context StreamFailureContext) error {
	return r.err
}

type pipeOutcome struct {
	result StreamPipeResult
	err    error
}

func runStreamPipeAsync(input PipeUpstreamStreamInput) <-chan pipeOutcome {
	outcome := make(chan pipeOutcome, 1)
	go func() {
		result, err := PipeUpstreamStream(input)
		outcome <- pipeOutcome{result, err}
	}()
	return outcome
}

func streamPipeInputForCloseTest(body UpstreamBody, signal <-chan struct{}, options StreamPipeOptions, handleStreamFailure func(string, string, StreamFailureContext) error) PipeUpstreamStreamInput {
	return PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream:   StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs:         1000,
		HandleStreamFailure: handleStreamFailure,
		Signal:              chanSignal{ch: signal},
		Options:             options,
	}
}

// ---- 流式：失败终态必须收口 ----

func TestStreamPipeReleasesBodyOnClientAbort(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	recorder := &failureRecorder{}
	signal := make(chan struct{})
	outcome := runStreamPipeAsync(streamPipeInputForCloseTest(body, signal, StreamPipeOptions{NowMs: func() int64 { return 1000 }}, recorder.handle))
	inner.push([]byte(chatDeltaChunk))
	time.Sleep(50 * time.Millisecond)
	close(signal) // 客户端断开
	inner.end()
	select {
	case done := <-outcome:
		if !IsUpstreamRequestAbortedError(done.err) {
			t.Fatalf("err = %v, want abort", done.err)
		}
		body.assertReleasedExactlyOnce(t, "客户端断开 abort")
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestStreamPipeReleasesBodyOnClientAbortAfterTerminalSuccess(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	recorder := &failureRecorder{}
	signal := make(chan struct{})
	outcome := runStreamPipeAsync(streamPipeInputForCloseTest(body, signal, StreamPipeOptions{NowMs: func() int64 { return 1000 }}, recorder.handle))
	inner.push([]byte(chatFinishChunk))
	inner.push([]byte(chatDoneChunk))
	time.Sleep(50 * time.Millisecond)
	close(signal) // 终态写出后客户端断开：按成功流式响应收尾
	select {
	case done := <-outcome:
		if done.err != nil {
			t.Fatalf("err = %v", done.err)
		}
		if !done.result.Completed || done.result.Message != "已完成" {
			t.Fatalf("result = %+v", done.result)
		}
		body.assertReleasedExactlyOnce(t, "终态后客户端断开成功收尾")
		inner.end()
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestStreamPipeReleasesBodyOnResponsePrecommitDeadline(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	recorder := &failureRecorder{}
	now := int64(1000)
	result, err := PipeUpstreamStream(streamPipeInputForCloseTest(body, nil, StreamPipeOptions{
		ResponsePrecommitDeadlineAtMs: int64PtrOf(1050),
		NowMs:                         func() int64 { now += 60; return now },
	}, recorder.handle))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Message != "网关请求墙钟已到，响应尚未产生可提交的语义结果" {
		t.Fatalf("message = %q", result.Message)
	}
	body.assertReleasedExactlyOnce(t, "response precommit deadline")
	inner.end() // 释放 pendingRead 桥接 goroutine
}

func TestStreamPipeReleasesBodyOnUpstreamReadError(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	recorder := &failureRecorder{}
	outcome := runStreamPipeAsync(streamPipeInputForCloseTest(body, nil, StreamPipeOptions{NowMs: func() int64 { return 1000 }}, recorder.handle))
	inner.push([]byte(chatDeltaChunk))
	time.Sleep(20 * time.Millisecond)
	inner.fail(&StartedBodyTransportError{Err: io.ErrUnexpectedEOF, Name: "ReadError", Code: "ECONNRESET"})
	select {
	case done := <-outcome:
		if done.err != nil {
			t.Fatalf("err = %v", done.err)
		}
		if done.result.Completed {
			t.Fatalf("result = %+v", done.result)
		}
		if done.result.TransportFailure == nil || done.result.TransportFailure.Kind != "read_incomplete" {
			t.Fatalf("transport failure = %+v", done.result.TransportFailure)
		}
		body.assertReleasedExactlyOnce(t, "上游读错误（一般 pipe 错误分支）")
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestStreamPipeReleasesBodyOnFailureCallbackError(t *testing.T) {
	failureChunk := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"provider_error\",\"message\":\"供应商失败\"}}}\n\n"
	inner := NewSliceUpstreamBody([]byte(failureChunk))
	body := newCountingBody(inner)
	recorder := &failingFailureRecorder{err: errors.New("失败回调处理失败")}
	_, err := PipeUpstreamStream(streamPipeInputForCloseTest(body, nil, StreamPipeOptions{NowMs: func() int64 { return 1000 }}, recorder.handle))
	if err == nil {
		t.Fatal("want HandleStreamFailure 回调错误早退路径返回错误")
	}
	// HandleStreamFailure 回调错误经 handleProtocolFailure / handlePipeError 早退，
	// body 仍必须在入口 defer 收口。
	body.assertReleasedExactlyOnce(t, "HandleStreamFailure 回调错误早退（协议失败分支）")
}

func TestStreamPipeReleasesBodyOnMissingTerminalCallbackError(t *testing.T) {
	inner := NewSliceUpstreamBody([]byte(chatDeltaChunk)) // EOF 前无协议终止事件
	body := newCountingBody(inner)
	recorder := &failingFailureRecorder{err: errors.New("失败回调处理失败")}
	_, err := PipeUpstreamStream(streamPipeInputForCloseTest(body, nil, StreamPipeOptions{NowMs: func() int64 { return 1000 }}, recorder.handle))
	if err == nil {
		t.Fatal("want HandleStreamFailure 回调错误传播")
	}
	body.assertReleasedExactlyOnce(t, "HandleStreamFailure 回调错误早退（missing terminal）")
}

// ---- 流式：正常完成路径语义不变 ----

func TestStreamPipeReleasesBodyOnSuccess(t *testing.T) {
	inner := NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk))
	body := newCountingBody(inner)
	recorder := &failureRecorder{}
	result, err := runPipe(body, nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("failure records = %+v", recorder.values)
	}
	body.assertReleasedExactlyOnce(t, "流式正常完成")
}

// ---- 非流式：失败终态必须收口 ----

func TestNonStreamPipeReleasesBodyOnAbort(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	signal := make(chan struct{})
	close(signal) // signal 已中断
	_, downstream := newDownstream()
	_, err := PipeNonStreamUpstreamResponse(NonStreamPipeInput{
		Body:        body,
		Downstream:  downstream,
		StartedAtMs: 1000,
		Signal:      chanSignal{ch: signal},
	})
	if !IsUpstreamRequestAbortedError(err) {
		t.Fatalf("err = %v, want abort", err)
	}
	body.assertReleasedExactlyOnce(t, "非流式 signal 中断")
}

func TestNonStreamPipeReleasesBodyOnReadError(t *testing.T) {
	inner := newChanBody()
	body := newCountingBody(inner)
	_, downstream := newDownstream()
	type nonStreamOutcome struct {
		result NonStreamPipeResult
		err    error
	}
	outcome := make(chan nonStreamOutcome, 1)
	go func() {
		result, err := PipeNonStreamUpstreamResponse(NonStreamPipeInput{
			Body:        body,
			Downstream:  downstream,
			StartedAtMs: 1000,
		})
		outcome <- nonStreamOutcome{result, err}
	}()
	inner.fail(errors.New("boom"))
	select {
	case done := <-outcome:
		var pipeErr *NonStreamBodyPipeError
		if !errors.As(done.err, &pipeErr) {
			t.Fatalf("err = %v, want NonStreamBodyPipeError", done.err)
		}
		body.assertReleasedExactlyOnce(t, "非流式读错误")
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestHandleNonStreamUpstreamResponseReleasesBodyWhenSignalPreAborted(t *testing.T) {
	inner := NewSliceUpstreamBody([]byte(`{"choices":[]}`))
	body := newCountingBody(inner)
	signal := make(chan struct{})
	close(signal)
	input, _ := newInputFixture(body, 200, nil)
	input.Signal = chanSignal{ch: signal}
	_, err := HandleNonStreamUpstreamResponse(input)
	if !IsUpstreamRequestAbortedError(err) {
		t.Fatalf("err = %v, want abort", err)
	}
	body.assertReleasedExactlyOnce(t, "非流式 handler 顶部 abort 早退")
}

// ---- 非流式：正常完成路径语义不变 ----

func TestNonStreamPipeReleasesBodyOnSuccess(t *testing.T) {
	inner := NewSliceUpstreamBody([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	body := newCountingBody(inner)
	_, downstream := newDownstream()
	result, err := PipeNonStreamUpstreamResponse(NonStreamPipeInput{
		Body:        body,
		Downstream:  downstream,
		StartedAtMs: 1000,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.TransferredBytes == 0 {
		t.Fatalf("result = %+v", result)
	}
	body.assertReleasedExactlyOnce(t, "非流式正常完成")
}
