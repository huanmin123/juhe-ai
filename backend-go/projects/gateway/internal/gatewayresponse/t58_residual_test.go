package gatewayresponse

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// T58 三项残留的收口用例（docs/待办与阻塞事项.md T58 残留①②③）：
// ①EOF 段隔离器尾字节写出次序；②raceWindowDeadline pending 已 settle 子路径、
// 隔离器 256KB maxHold 超限整段放行、静默后干净 EOF 终局判定；③失败臂结算
// 日志的 parseErrorEventCount / invalidProtocolEventCount 观测字段。

// ---- 残留①：EOF 残片经隔离器既放行完整事件又持有新尾部时，tail 必须晚于
// 其前的完整事件到达客户端（终审 N4 修正后的真实触发形态；EOF 残片经拦截器
// FlushPendingOnEOF 释放，不使用生产不可达的「转换器 EOF 冲刷多帧」形态）----

const (
	t58SecondDeltaChunk = "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"窗内二\"}}]}\n\n"
	t58EOFDelta3Event   = "data: {\"id\":\"c3\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"EOF帧\"}}]}\n\n"
	t58UnterminatedTail = "data: {\"choices\":[{\"delta\":{\"content\":\"partial-tail\"}}]}"
)

func TestT58EOFTailIsWrittenAfterReleasedCompleteEvents(t *testing.T) {
	recorder := &failureRecorder{}
	// EOF 残片：一个完整合法事件 + 一个未完结新尾部（单 chunk 内「既放行完整
	// 事件又持有新尾部」的隔离器输入形态）。
	interceptor := &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{
		Chunks: [][]byte{[]byte(t58EOFDelta3Event + t58UnterminatedTail)},
	}}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.Interceptor = interceptor
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),      // 首个语义事件：入窗
		[]byte(t58SecondDeltaChunk), // 窗内续帧
		// 干净 EOF：EOF 残片经 FlushPendingOnEOF 进入隔离器
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// 字节序断言（整流精确比对）：EOF 残片放行的完整事件必须先于其后的新尾部
	// 写出；尾字节随既有 generic 收敛 flush 最后到达。
	expected := []byte(chatDeltaChunk + t58SecondDeltaChunk + t58EOFDelta3Event + t58UnterminatedTail)
	if !bytes.Equal(written, expected) {
		t.Fatalf("EOF 段尾字节次序错误：\n got = %q\nwant = %q", string(written), string(expected))
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("尾字节随既有收敛透传，流应正常完成: %+v", result)
	}
	if result.ParseErrorEventCount != 0 {
		t.Fatalf("合法 JSON 未完结尾字节不得计解析失败: %d", result.ParseErrorEventCount)
	}
	if recorder.count() != 0 {
		t.Fatalf("失败记录数 = %d", recorder.count())
	}
}

// ---- 残留②-b：隔离器 256KB maxHold 越限整段放行（interceptor.go push 超限臂）----

func TestT58IsolatorMaxHoldOverflowReleasesHeldSegmentWhole(t *testing.T) {
	iso := &parseErrorEventIsolator{}
	// 未完结前缀（无事件边界）：隔离器持有。
	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}")
	if out := iso.push(first); len(out) != 0 {
		t.Fatalf("未越限前缀应被持有: %q", string(out))
	}
	// 追加至持有量恰等于上限：放行判定为严格大于，不触发。
	filler := bytes.Repeat([]byte("a"), parseErrorEventIsolatorMaxHoldBytes-len(first))
	if out := iso.push(filler); len(out) != 0 {
		t.Fatalf("持有量等于上限不得放行: %d bytes", len(out))
	}
	// 再追加 1 字节越过上限：持有字节整段原样放行（超大帧保护，预提交缓冲
	// 语义照常生效），持有状态清空。
	third := []byte("b")
	held := iso.push(third)
	want := append(append(append([]byte{}, first...), filler...), third...)
	if !bytes.Equal(held, want) {
		t.Fatalf("越限应整段原样放行: got %d bytes, want %d bytes", len(held), len(want))
	}
	// 放行后隔离器状态复位：后续完整坏事件照常隔离（隔离语义不因超限失效）。
	if out := iso.push([]byte(w22StickyBadPayload)); out != nil && strings.Contains(string(out), "上级") {
		t.Fatalf("超限放行后坏事件应照常隔离: %q", string(out))
	}
	if iso.dropped != 1 {
		t.Fatalf("dropped = %d, want 1", iso.dropped)
	}
}

// ---- 残留②-a：raceWindowDeadline 的 pending 已 settle 子路径
// （streamread.go：窗口截止到点 flush 后，已 settle 的 pending 随本结果带回）----

// t58SwapBody 允许逐尝试替换上游 channel 的 UpstreamBody 测试实现。
type t58SwapBody struct {
	mu sync.Mutex
	ch chan ChunkResult
}

func (b *t58SwapBody) setNext(ch chan ChunkResult) {
	b.mu.Lock()
	b.ch = ch
	b.mu.Unlock()
}

func (b *t58SwapBody) Next() <-chan ChunkResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ch
}

func (b *t58SwapBody) Close() {}

func TestT58WindowDeadlineReturnsSettledPendingChunk(t *testing.T) {
	_, downstream := newDownstream()
	body := &t58SwapBody{ch: make(chan ChunkResult)}
	// 每尝试的观测状态：settle chunk 在截止 flush 的写回调内投喂（无缓冲 send
	// 完成即桥接 goroutine 已接收——pending 先 settle、后随窗口截止分支返回）。
	state := struct {
		mu      sync.Mutex
		ch      chan ChunkResult
		settle  []byte
		pushed  bool
		written []byte
	}{}
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream: StreamDownstream{Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
			state.mu.Lock()
			state.written = append(state.written, data...)
			ch, settle, pushed := state.ch, state.settle, state.pushed
			state.mu.Unlock()
			if pushed || ch == nil {
				return
			}
			state.mu.Lock()
			state.pushed = true
			state.mu.Unlock()
			ch <- ChunkResult{Data: settle}
			runtime.Gosched()
			runtime.Gosched()
		}}},
		TimeoutProfile: TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:    time.Now().UnixMilli(),
		Options:        StreamPipeOptions{NowMs: func() int64 { return time.Now().UnixMilli() }},
	})
	pipe.preCommitSseEvidence.DataEventObserved = true

	const markerChunk = "data: {\"marker\":\"deadline-flush\"}\n\n"
	const settleChunk = "data: {\"settled\":\"pending-chunk\"}\n\n"
	type readOutcome struct {
		result streamChunkReadResult
		err    error
	}
	settled := false
	for attempt := 0; attempt < 40 && !settled; attempt++ {
		// 每次尝试独立上游 channel + 完整重开的窗口状态（截止 timer channel 由
		// 测试直接持有并预填充，select 首轮必然裁到窗口截止臂）。
		next := make(chan ChunkResult)
		body.setNext(next)
		state.mu.Lock()
		state.ch = next
		state.settle = []byte(settleChunk)
		state.pushed = false
		state.written = nil
		state.mu.Unlock()
		timerCh := make(chan time.Time, 1)
		timerCh <- time.Now()
		pipe.preCommitBuffer.Buffering = true
		pipe.preCommitBuffer.BufferedBytes = len(markerChunk)
		pipe.preCommitBuffer.Chunks = [][]byte{[]byte(markerChunk)}
		pipe.preCommitBuffer.Windowing = true
		pipe.preCommitBuffer.WindowStartMs = pipe.nowMs()
		pipe.preCommitBuffer.WindowBytes = len(markerChunk)
		pipe.replayWindowOpen = true
		pipe.replayWindowTimer = nil
		pipe.replayWindowTimerCh = timerCh
		pipe.replayWindowDurationMs = nil

		outcomeCh := make(chan readOutcome, 1)
		go func() {
			result, err := pipe.readNextStreamChunk()
			outcomeCh <- readOutcome{result: result, err: err}
		}()
		var outcome readOutcome
		select {
		case outcome = <-outcomeCh:
		case <-time.After(2 * time.Second):
			// 未 settle 子路径（flush 后 continue，下一轮无定时器可竞）会阻塞：
			// 关闭 channel 让桥接中继 ErrUnexpectedEOF 使调用返回，回收后重试。
			close(next)
			outcome = <-outcomeCh
		}
		if outcome.err != nil || outcome.result.chunk.Err != nil {
			continue
		}
		state.mu.Lock()
		flushedMarker := bytes.Contains(state.written, []byte(markerChunk))
		state.mu.Unlock()
		// 返回 chunk 恰为 flush 期间投喂的 settle chunk ⇒ 只能经
		// 「pending.isSettled → receive → 随本结果带回」子路径产生。
		if bytes.Equal(outcome.result.chunk.Data, []byte(settleChunk)) && flushedMarker && !pipe.replayWindowOpen {
			settled = true
			if outcome.result.firstByteDeadlineObserved {
				t.Fatalf("未配置首字 deadline 时不得置位 observed: %+v", outcome.result)
			}
		}
	}
	if !settled {
		t.Fatal("窗口截止分支未在重试预算内观察到 pending 已 settle 子路径")
	}
}

// ---- 残留②-c：静默（T 到点强制 flush）后干净 EOF → 终局判定独立用例 ----

func TestT58SilenceThenCleanEOFGenericConvergence(t *testing.T) {
	options := w22WindowOptions(func() int64 { return time.Now().UnixMilli() })
	body, written, _, outcomeCh := w22LivePipe(t, 60, options)
	body.push([]byte(chatDeltaChunk))
	// 静默超 T：真实截止 timer 强制关窗 flush（提交）。
	w22WaitFor(t, 2*time.Second, func() bool {
		select {
		case outcome := <-outcomeCh:
			t.Fatalf("管道提前结束: %+v %v", outcome.result, outcome.err)
		default:
		}
		return strings.Contains(written(), "你好")
	})
	// 静默后干净 EOF（无终止事件、无读错误）：终局判定按 §3.2.1 判定 4 收敛
	// success（区别于静默后补 [DONE] 的主循环终态臂）。
	body.fail(io.EOF)
	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("err = %v", outcome.err)
	}
	if !outcome.result.Completed || outcome.result.Message != "已完成" {
		t.Fatalf("静默 flush 后干净 EOF 应按 generic 收敛 success: %+v", outcome.result)
	}
	if !outcome.result.SemanticCommitted {
		t.Fatalf("T 到点 flush 应已语义提交: %+v", outcome.result)
	}
	if outcome.result.WindowDurationMs == nil || *outcome.result.WindowDurationMs <= 0 {
		t.Fatalf("静默关窗应记录窗口时长: %+v", outcome.result.WindowDurationMs)
	}
}

func TestT58SilenceThenCleanEOFWithParseErrorSettlesAbnormalEOF(t *testing.T) {
	options := w22WindowOptions(func() int64 { return time.Now().UnixMilli() })
	body, written, interrupts, outcomeCh := w22LivePipe(t, 60, options)
	body.push([]byte(chatDeltaChunk))      // 语义事件：入窗
	body.push([]byte(w22StickyBadPayload)) // 解析失败事件：隔离 + 计数
	// 静默超 T：真实截止 timer 强制关窗 flush（提交）。
	w22WaitFor(t, 2*time.Second, func() bool {
		select {
		case outcome := <-outcomeCh:
			t.Fatalf("管道提前结束: %+v %v", outcome.result, outcome.err)
		default:
		}
		return strings.Contains(written(), "你好")
	})
	// 静默后干净 EOF：EOF 前存在解析失败事件 → §3.2.1 判定 2+3 失败结算；
	// 已提交 → 提交后分支真·网络级断开。
	body.fail(io.EOF)
	outcome := <-outcomeCh
	if outcome.result.Completed {
		t.Fatalf("异常终局不得收敛 success: %+v", outcome.result)
	}
	if outcome.result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("errorCode = %q, want %q", outcome.result.ErrorCode, UpstreamStreamAbnormalEOFCode)
	}
	if outcome.result.ParseErrorEventCount != 1 {
		t.Fatalf("ParseErrorEventCount = %d, want 1", outcome.result.ParseErrorEventCount)
	}
	if interrupts() != 1 {
		t.Fatalf("提交后异常终局应真·网络级断开一次，interrupts = %d", interrupts())
	}
	if !strings.Contains(written(), "你好") || strings.Contains(written(), "上级") {
		t.Fatalf("已提交内容保留、坏字节不透传: %q", written())
	}
}

// ---- 残留③：失败臂结算日志携带 parseErrorEventCount / invalidProtocolEventCount
// （与 success 两臂、abnormal 臂、empty-completion 臂同一结算链）----

func TestT58FailureArmsCarryParseErrorSettlementFields(t *testing.T) {
	// missing-terminal 臂（精确画像默认解释协议、提交前）：解析失败事件计数 1。
	{
		logger := &w22CaptureLogger{}
		recorder := &failureRecorder{}
		options := StreamPipeOptions{
			RetryBeforeDownstreamWriteUntilOutput: true,
			NowMs:                                 func() int64 { return 1000 },
			Logger:                                logger,
		}
		result, _, _, err := w22RunPipeWithCapture(t, [][]byte{
			[]byte(chatDeltaChunk),
			[]byte(w22StickyBadPayload), // 解析失败：隔离 + 计数
			// 干净 EOF 无终止：精确画像 missing-terminal
		}, options, recorder)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.Completed || recorder.count() != 1 {
			t.Fatalf("missing-terminal 应按失败结算: %+v", result)
		}
		warn := logger.warnOf("gateway_stream_missing_terminal")
		if warn == nil {
			t.Fatalf("缺少 gateway_stream_missing_terminal 结算日志: %+v", logger.events())
		}
		if warn.fields["parseErrorEventCount"] != 1 {
			t.Fatalf("missing_terminal parseErrorEventCount = %v, want 1", warn.fields["parseErrorEventCount"])
		}
		if warn.fields["invalidProtocolEventCount"] == nil {
			t.Fatalf("missing_terminal 缺 invalidProtocolEventCount 字段: %+v", warn.fields)
		}
	}
	// incompleteOrFailed 臂（提交后 interrupted）：协议形状异常计数 1。
	{
		logger := &w22CaptureLogger{}
		recorder := &failureRecorder{}
		options := StreamPipeOptions{
			RetryBeforeDownstreamWriteUntilOutput: true,
			NowMs:                                 func() int64 { return 1000 },
			Logger:                                logger,
		}
		bigDelta := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + strings.Repeat("输", 3000) + "\"}}]}\n\n"
		result, _, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
			[]byte(bigDelta), // 超 B：不入缓冲、随 flush 写出（提交）
			// L2 协议形状异常帧（choices 非数组）：隔离 + 计数
			[]byte("data: {\"id\":\"\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"\",\"choices\":null,\"usage\":null}\n\n"),
			// 失败帧缺终止空行即 EOF：Finish 冲出 FailedReceived → 不完整/失败臂
			[]byte("data: {\"error\":{\"code\":\"e\",\"type\":\"upstream_error\"}}"),
		}, options, recorder)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.Completed || interrupts != 1 {
			t.Fatalf("提交后失败流应中断一次: %+v interrupts=%d", result, interrupts)
		}
		warn := logger.warnOf("gateway_stream_finished_failed_interrupted")
		if warn == nil {
			t.Fatalf("缺少 gateway_stream_finished_failed_interrupted 结算日志: %+v", logger.events())
		}
		if warn.fields["invalidProtocolEventCount"] != 1 {
			t.Fatalf("finished_failed_interrupted invalidProtocolEventCount = %v, want 1", warn.fields["invalidProtocolEventCount"])
		}
		if warn.fields["parseErrorEventCount"] == nil {
			t.Fatalf("finished_failed_interrupted 缺 parseErrorEventCount 字段: %+v", warn.fields)
		}
	}
	// pipe_error 臂（提交后读错误）：结算日志携带两计数。
	{
		logger := &w22CaptureLogger{}
		options := w22WindowOptions(func() int64 { return time.Now().UnixMilli() })
		options.Logger = logger
		body, written, _, outcomeCh := w22LivePipe(t, 60, options)
		body.push([]byte(chatDeltaChunk))
		w22WaitFor(t, 2*time.Second, func() bool {
			select {
			case outcome := <-outcomeCh:
				t.Fatalf("管道提前结束: %+v %v", outcome.result, outcome.err)
			default:
			}
			return strings.Contains(written(), "你好")
		})
		body.fail(errors.New("上游连接被重置"))
		outcome := <-outcomeCh
		if outcome.result.Completed {
			t.Fatalf("读错误应按失败收尾: %+v", outcome.result)
		}
		warn := logger.warnOf("gateway_stream_pipe_error")
		if warn == nil {
			t.Fatalf("缺少 gateway_stream_pipe_error 结算日志: %+v", logger.events())
		}
		if warn.fields["parseErrorEventCount"] == nil || warn.fields["invalidProtocolEventCount"] == nil {
			t.Fatalf("pipe_error 缺计数结算字段: %+v", warn.fields)
		}
	}
}
