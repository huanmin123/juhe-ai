package gatewayresponse

import (
	"strings"
	"sync"
	"testing"
)

// 上游流式失败帧拦截设计 §7 第 4 条：顶层 error 对象形态（无 event 名、无
// type 字段，DeepSeek 生产事故帧）在管道级的提交前/提交后处置锚定。失败判定
// 改动在 gatewayopenai.ClassifyStreamEvent，经 DefaultOpenAIStreamDriver 的
// 真实 inspector 生效；本文件沿用 runPipe / NewSliceUpstreamBody 基建喂真实
// SSE 驱动管道（默认 driver 即真实 gatewayopenai inspector，无策略、无
// ClientRetryEnabled 时不装配拦截器，路径为纯 interpretedProtocolFailure）。

const w30TopLevelErrorFrame = "data: {\"error\":{\"code\":\"upstream_error\",\"message\":\"upstream sent an error event\"}}\n\n"

// 提交前：预提交缓冲仍持有内容时收到顶层 error 对象帧 →
// before_downstream_commit 路径：缓冲被丢弃、零下游写入、结果交回上层。
func TestW30TopLevelErrorFrameBeforeDownstreamCommit(t *testing.T) {
	recorder := &failureRecorder{}
	heldChunk := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{}}]}\n\n"
	result, err := runPipe(NewSliceUpstreamBody([]byte(heldChunk), []byte(w30TopLevelErrorFrame)), nil, StreamPipeOptions{
		RetryBeforeDownstreamWriteUntilOutput: true,
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("失败流不得按完成收尾: %+v", result)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("提交前失败不得写下游, bytes = %d", result.DownstreamBytesWritten)
	}
	if len(result.UncommittedResponseBody) != 0 {
		t.Fatalf("预提交缓冲应被丢弃, body = %q", result.UncommittedResponseBody)
	}
	if result.TransportCommitted || result.SemanticCommitted {
		t.Fatalf("提交前失败不得置提交位: %+v", result)
	}
	if strings.Contains(result.ResponseBodyText, "upstream sent an error event") {
		t.Fatalf("下游响应正文不得含上游错误原文帧: %q", result.ResponseBodyText)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	last := recorder.last()
	if last.message != "upstream sent an error event" || last.errorCode != "upstream_error" {
		t.Fatalf("failure = %q/%q", last.message, last.errorCode)
	}
	if !last.context.ProtocolFailureEventReceived {
		t.Fatalf("context = %+v", last.context)
	}
}

// 提交后：delta chunk 已写下游提交后收到顶层 error 对象帧 → interrupted 处置：
// 下游无错误载荷，连接干净中断。
func TestW30TopLevelErrorFrameAfterDownstreamCommitInterrupted(t *testing.T) {
	recorder := &failureRecorder{}
	_, downstream := newDownstream()
	var mu sync.Mutex
	var written []byte
	input := PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatDeltaChunk), []byte(w30TopLevelErrorFrame)),
		Downstream: StreamDownstream{
			Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
				mu.Lock()
				written = append(written, data...)
				mu.Unlock()
			}},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
	}
	result, err := PipeUpstreamStream(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("失败流不得按完成收尾: %+v", result)
	}
	if !result.TransportCommitted || !result.SemanticCommitted {
		t.Fatalf("delta chunk 应已提交下游: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(written), "你好") {
		t.Fatalf("delta 应已到达下游: %q", written)
	}
	if strings.Contains(string(written), "upstream sent an error event") {
		t.Fatalf("interrupted 处置不得补发上游错误载荷: %q", written)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	last := recorder.last()
	if last.message != "upstream sent an error event" || last.errorCode != "upstream_error" {
		t.Fatalf("failure = %q/%q", last.message, last.errorCode)
	}
}

// error 帧为流中唯一内容（零正常 chunk 前置）：走 before_downstream_commit
// 路径——零下游写入、预提交缓冲丢弃、结果交回上层（与「错误帧前已有缓冲」
// 的既有用例互补，锚定首个事件即失败的边界）。
func TestW30TopLevelErrorFrameOnlyContentBeforeCommit(t *testing.T) {
	recorder := &failureRecorder{}
	result, err := runPipe(NewSliceUpstreamBody([]byte(w30TopLevelErrorFrame)), nil, StreamPipeOptions{
		RetryBeforeDownstreamWriteUntilOutput: true,
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("失败流不得按完成收尾: %+v", result)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("零前置输出时失败不得写下游, bytes = %d", result.DownstreamBytesWritten)
	}
	if len(result.UncommittedResponseBody) != 0 {
		t.Fatalf("预提交缓冲应被丢弃, body = %q", result.UncommittedResponseBody)
	}
	if result.TransportCommitted || result.SemanticCommitted {
		t.Fatalf("提交前失败不得置提交位: %+v", result)
	}
	if result.OutputReceived {
		t.Fatalf("零前置输出不应有输出: %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	last := recorder.last()
	if last.message != "upstream sent an error event" || last.errorCode != "upstream_error" {
		t.Fatalf("failure = %q/%q", last.message, last.errorCode)
	}
	if !last.context.ProtocolFailureEventReceived {
		t.Fatalf("context = %+v", last.context)
	}
}

// 成功终态后的矛盾 error 帧（设计 §5.5 行为变化点）：[DONE] 已成功提交后、
// EOF 前再收到顶层 error 对象帧 → 按 gateway_stream_failed_after_terminal
// 语义失败收尾：结果 failed，下游字节保留完整成功流但不含 error 载荷。
func TestW30TopLevelErrorFrameAfterTerminalFailedFinish(t *testing.T) {
	recorder := &failureRecorder{}
	_, downstream := newDownstream()
	var mu sync.Mutex
	var written []byte
	input := PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk), []byte(w30TopLevelErrorFrame)),
		Downstream: StreamDownstream{
			Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
				mu.Lock()
				written = append(written, data...)
				mu.Unlock()
			}},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
	}
	result, err := PipeUpstreamStream(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("终态后矛盾失败应按失败收尾: %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	last := recorder.last()
	if last.message != "upstream sent an error event" {
		t.Fatalf("failure message = %q", last.message)
	}
	if last.errorCode != "upstream_protocol_failure" {
		t.Fatalf("failed-after-terminal 固定错误码 = %q", last.errorCode)
	}
	mu.Lock()
	defer mu.Unlock()
	text := string(written)
	if !strings.Contains(text, "你好") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("成功终态前的正常流应已提交下游: %q", text)
	}
	if strings.Contains(text, "upstream sent an error event") || strings.Contains(text, `{"error"`) {
		t.Fatalf("下游不得收到终态后的 error 载荷帧: %q", text)
	}
}

// error 帧后跟随正常 chunk：管道在 error 帧处收尾（提交后 interrupted），
// 后续帧不再出现在下游。
func TestW30TopLevelErrorFrameStopsForwarding(t *testing.T) {
	afterChunk := "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"AFTER-ERROR\"}}]}\n\n"
	recorder := &failureRecorder{}
	_, downstream := newDownstream()
	var mu sync.Mutex
	var written []byte
	input := PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(w30TopLevelErrorFrame), []byte(afterChunk)),
		Downstream: StreamDownstream{
			Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
				mu.Lock()
				written = append(written, data...)
				mu.Unlock()
			}},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
	}
	result, err := PipeUpstreamStream(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("失败流不得按完成收尾: %+v", result)
	}
	if !result.TransportCommitted || !result.SemanticCommitted {
		t.Fatalf("error 帧前的 delta 应已提交下游: %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	if last := recorder.last(); last.message != "upstream sent an error event" || last.errorCode != "upstream_error" {
		t.Fatalf("failure = %q/%q", last.message, last.errorCode)
	}
	mu.Lock()
	defer mu.Unlock()
	text := string(written)
	if !strings.Contains(text, "你好") {
		t.Fatalf("error 帧前的 delta 应已到达下游: %q", text)
	}
	if strings.Contains(text, "AFTER-ERROR") {
		t.Fatalf("error 帧收尾后不得继续转发后续帧: %q", text)
	}
	if strings.Contains(text, "upstream sent an error event") {
		t.Fatalf("下游不得收到错误载荷: %q", text)
	}
}
