package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// body.go 非流式正文读取 / 截止竞速 / 捕获缓冲的单元测试。
// 确定性策略：注入 NowMs 控制过期分支；阻塞 reader 由测试显式释放；
// 竞速定时器统一用 1ms 级（maxInt64(1, remaining)）可控窗口。

// blockingReader 阻塞直到 release 被调用（用于竞速超时分支）。
type blockingReader struct {
	release chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{release: make(chan struct{})}
}

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

func (b *blockingReader) close() { close(b.release) }

// chunkReader 按 buffer 尺寸切片回放数据，最后返回 io.EOF。
type chunkReader struct {
	chunks [][]byte
}

func (c *chunkReader) Read(buffer []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	next := c.chunks[0]
	if len(next) > len(buffer) {
		copy(buffer, next[:len(buffer)])
		c.chunks[0] = next[len(buffer):]
		return len(buffer), nil
	}
	c.chunks = c.chunks[1:]
	copy(buffer, next)
	return len(next), nil
}

func TestReadFirstNonStreamChunkNoDeadlines(t *testing.T) {
	reader := &chunkReader{chunks: [][]byte{[]byte("hello")}}
	buffer := make([]byte, 32)
	read, observed, err := readFirstNonStreamChunkWithDeadlines(reader, buffer, NowMs(), firstByteDeadlineReadInput{
		signal: context.Background(),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if observed {
		t.Fatal("无截止时不观测首字")
	}
	if read.n != 5 {
		t.Fatalf("n = %d", read.n)
	}
	// 读完回 EOF → done。
	read, _, err = readFirstNonStreamChunkWithDeadlines(reader, buffer, NowMs(), firstByteDeadlineReadInput{
		signal: context.Background(),
	})
	if err != nil || !read.done {
		t.Fatalf("EOF read: done=%v err=%v", read.done, err)
	}
}

func TestReadFirstNonStreamChunkPrecommitAlreadyElapsed(t *testing.T) {
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	read, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-100, firstByteDeadlineReadInput{
		startedAt:                     base - 100,
		signal:                        context.Background(),
		responsePrecommitDeadlineAtMs: ptrInt64(base - 50),
		pendingReadSupersedesDeadline: true,
	})
	if read.n != 0 || read.done {
		t.Fatalf("read = %#v", read)
	}
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(err, &deadlineErr) || deadlineErr.DeadlineAtMs != base-50 {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFirstNonStreamChunkMaxLifetimeAlreadyElapsed(t *testing.T) {
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-100, firstByteDeadlineReadInput{
		startedAt:             base - 100,
		signal:                context.Background(),
		maxLifetimeDeadlineAt: ptrInt64(base - 50),
		maxLifetimeMs:         ptrInt64(5_000),
	})
	var lifetimeErr *UpstreamBodyReadMaxLifetimeError
	if !errorsAs(err, &lifetimeErr) || lifetimeErr.TimeoutMs != 5_000 {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFirstNonStreamChunkSoftDeadlineConfiguredAbort(t *testing.T) {
	// 软截止已过 + handler abort → configured_deadline 首字超时。
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-5_000, firstByteDeadlineReadInput{
		startedAt:           base - 5_000,
		signal:              context.Background(),
		firstByteDeadlineMs: ptrInt64(1_000),
		onFirstByteDeadline: func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionAbort
		},
	})
	var timeoutErr *GatewayFirstByteTimeoutError
	if !errorsAs(err, &timeoutErr) {
		t.Fatalf("err = %v", err)
	}
	if timeoutErr.Source != FirstByteTimeoutSourceConfiguredDeadline {
		t.Fatalf("source = %q", timeoutErr.Source)
	}
	if timeoutErr.Message != "上游非流式响应 1s 后仍未返回首个字节" {
		t.Fatalf("message = %q", timeoutErr.Message)
	}
}

func TestReadFirstNonStreamChunkSoftDeadlineHandlerContinueLoops(t *testing.T) {
	// handler continue：循环重进，随后 precommit 已过期 → 墙钟错误。
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-5_000, firstByteDeadlineReadInput{
		startedAt:                     base - 5_000,
		signal:                        context.Background(),
		firstByteDeadlineMs:           ptrInt64(1_000),
		responsePrecommitDeadlineAtMs: ptrInt64(base - 1),
		onFirstByteDeadline: func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionContinue
		},
	})
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(err, &deadlineErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFirstNonStreamChunkHardTimeout(t *testing.T) {
	// 硬超时（firstByteTimeoutMs）已过：1ms 竞速窗口后 hard_timeout。
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	startedAt := base - 5_000
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), startedAt, firstByteDeadlineReadInput{
		startedAt:          startedAt,
		signal:             context.Background(),
		firstByteTimeoutMs: ptrInt64(2_000), // 硬截止 = startedAt+2000，早已过期 → 剩余为负 → 直接判超时
	})
	var timeoutErr *GatewayFirstByteTimeoutError
	if !errorsAs(err, &timeoutErr) {
		t.Fatalf("err = %v", err)
	}
	if timeoutErr.Source != "" {
		t.Fatalf("硬超时无 configured source, got %q", timeoutErr.Source)
	}
}

func TestReadFirstNonStreamChunkSoftDeadlineContinueThenRead(t *testing.T) {
	// 软截止先于读完成：handler continue 后循环重进，race 读完成胜出。
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	reader := &delayedReader{delay: 2 * time.Millisecond}
	read, observed, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 32), base-5_000, firstByteDeadlineReadInput{
		startedAt:                     base - 5_000,
		signal:                        context.Background(),
		firstByteDeadlineMs:           ptrInt64(1_000),
		pendingReadSupersedesDeadline: true,
		onFirstByteDeadline: func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionContinue
		},
		onFirstByteDeadlineSuperseded: func() {},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.n != 3 || !observed {
		t.Fatalf("read = %#v observed=%v", read, observed)
	}
}

func TestReadFirstNonStreamChunkSoftDeadlineContinueThenEOF(t *testing.T) {
	// 软截止后循环重进，race 读到 EOF：done 形态返回。
	base := int64(10_000)
	injectNowMs(t, func() int64 { return base })
	read, _, err := readFirstNonStreamChunkWithDeadlines(&chunkReader{chunks: nil}, make([]byte, 32), base-5_000, firstByteDeadlineReadInput{
		startedAt:                     base - 5_000,
		signal:                        context.Background(),
		firstByteDeadlineMs:           ptrInt64(1_000),
		pendingReadSupersedesDeadline: true,
		onFirstByteDeadline: func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionContinue
		},
	})
	if err != nil || !read.done {
		t.Fatalf("read = %#v err = %v", read, err)
	}
}

func TestFirstNonStreamReadAfterDeadlineDecisionDecisionErr(t *testing.T) {
	decision := deadlineDecision{hasRead: true, decisionErr: errors.New("决策失败")}
	read, _, err := firstNonStreamReadAfterDeadlineDecision(decision, true, firstByteDeadlineReadInput{})
	if read.n != 0 || err == nil {
		t.Fatalf("read = %#v err = %v", read, err)
	}
	// pendingReadSupersedesDeadline=false + abort 决策 → configured 超时。
	decision = deadlineDecision{action: FirstByteDeadlineActionAbort}
	_, _, err = firstNonStreamReadAfterDeadlineDecision(decision, true, firstByteDeadlineReadInput{
		firstByteDeadlineMs: ptrInt64(2_000),
	})
	var timeoutErr *GatewayFirstByteTimeoutError
	if !errorsAs(err, &timeoutErr) || timeoutErr.Message != "上游非流式响应 2s 后仍未返回完整语义响应" {
		t.Fatalf("err = %v", err)
	}
	// pendingReadSupersedesDeadline=true 时 superseded 回调先执行。
	superseded := false
	decision = deadlineDecision{hasRead: true, read: chunkResult{n: 3}}
	read, _, err = firstNonStreamReadAfterDeadlineDecision(decision, true, firstByteDeadlineReadInput{
		pendingReadSupersedesDeadline: true,
		onFirstByteDeadlineSuperseded: func() { superseded = true },
	})
	if !superseded || read.n != 3 || err != nil {
		t.Fatalf("read = %#v superseded=%v err=%v", read, superseded, err)
	}
	// supersede + EOF → done。
	decision = deadlineDecision{hasRead: true, read: chunkResult{err: io.EOF}}
	read, _, err = firstNonStreamReadAfterDeadlineDecision(decision, true, firstByteDeadlineReadInput{
		pendingReadSupersedesDeadline: true,
	})
	if err != nil || !read.done {
		t.Fatalf("read = %#v err = %v", read, err)
	}
}

func TestRaceReadWithDeadlinesReadWins(t *testing.T) {
	reader := &chunkReader{chunks: [][]byte{[]byte("ok")}}
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	raceType, result, err := raceReadWithDeadlines(pendingRead, context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if raceType != raceReadDone || result.n != 2 {
		t.Fatalf("raceType=%v result=%#v", raceType, result)
	}
}

func TestRaceReadWithDeadlinesHardTimeoutWins(t *testing.T) {
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	// 负剩余 → 1ms 定时（maxInt64 下限），读阻塞 → hard timeout 胜出。
	raceType, _, _ := raceReadWithDeadlines(pendingRead, context.Background(), nil, ptrInt64(-1), nil, nil)
	if raceType != raceHardTimeout {
		t.Fatalf("raceType = %v", raceType)
	}
}

func TestRaceReadWithDeadlinesMaxLifetimeWins(t *testing.T) {
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	raceType, _, _ := raceReadWithDeadlines(pendingRead, context.Background(), nil, nil, ptrInt64(-1), nil)
	if raceType != raceMaxLifetimeTimeout {
		t.Fatalf("raceType = %v", raceType)
	}
}

func TestRaceReadWithDeadlinesAbortWins(t *testing.T) {
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	raceType, _, _ := raceReadWithDeadlines(pendingRead, canceled, nil, nil, nil, nil)
	if raceType != raceAbort {
		t.Fatalf("raceType = %v", raceType)
	}
}

func TestRaceReadWithDeadlinesSoftVsPrecommitAttribution(t *testing.T) {
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	// precommit 截止不晚于 soft → 墙钟归因胜出。
	raceType, _, _ := raceReadWithDeadlines(pendingRead, context.Background(), ptrInt64(-1), nil, nil, ptrInt64(-1))
	if raceType != raceResponsePrecommitTimeout {
		t.Fatalf("raceType = %v", raceType)
	}
	// precommit 晚于 soft → soft 归因。
	other := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := (&blockingReader2{}).Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	raceType, _, _ = raceReadWithDeadlines(other, context.Background(), ptrInt64(-1), nil, nil, ptrInt64(60_000))
	if raceType != raceSoftTimeout {
		t.Fatalf("raceType = %v", raceType)
	}
}

// blockingReader2 独立阻塞 reader（避免与上一个用例共享释放）。
type blockingReader2 struct {
	release chan struct{}
}

func (b *blockingReader2) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

func TestReadNonStreamChunkWithAbsoluteDeadlinePaths(t *testing.T) {
	// 无截止 → 直读。
	reader := &chunkReader{chunks: [][]byte{[]byte("chunk")}}
	n, err, _ := readNonStreamChunkWithAbsoluteDeadline(reader, make([]byte, 16), context.Background(), nil, nil, nil)
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	base := int64(20_000)
	injectNowMs(t, func() int64 { return base })

	// precommit 已过期。
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(newBlockingReader(), make([]byte, 8), context.Background(), nil, nil, ptrInt64(base-10))
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(err, &deadlineErr) {
		t.Fatalf("err = %v", err)
	}

	// maxLifetime 已过期。
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(newBlockingReader(), make([]byte, 8), context.Background(), ptrInt64(base-10), ptrInt64(5_000), nil)
	var lifetimeErr *UpstreamBodyReadMaxLifetimeError
	if !errorsAs(err, &lifetimeErr) {
		t.Fatalf("err = %v", err)
	}

	// maxLifetime 竞速超时。
	blocked := newBlockingReader()
	t.Cleanup(blocked.close)
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(blocked, make([]byte, 8), context.Background(), ptrInt64(base-1), ptrInt64(5_000), nil)
	if !errorsAs(err, &lifetimeErr) {
		t.Fatalf("race lifetime err = %v", err)
	}

	// precommit 竞速超时。
	blocked2 := newBlockingReader()
	t.Cleanup(blocked2.close)
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(blocked2, make([]byte, 8), context.Background(), nil, nil, ptrInt64(base-1))
	if !errorsAs(err, &deadlineErr) {
		t.Fatalf("race precommit err = %v", err)
	}

	// 读完成后结算晚于 precommit → 墙钟归因。
	slowSettle := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		time.Sleep(2 * time.Millisecond)
		return chunkResult{n: 3}, nil
	})
	_ = slowSettle
	reader3 := &delayedReader{delay: 3 * time.Millisecond}
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(reader3, make([]byte, 8), context.Background(), ptrInt64(base+60_000), ptrInt64(120_000), ptrInt64(base+1))
	if err == nil {
		// 读取在 3ms 内完成但结算晚于 precommit(base+1)：墙钟归因。
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("settled-after-deadline err = %v", err)
		}
	}

	// 取消 → aborted。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	blocked3 := newBlockingReader()
	t.Cleanup(blocked3.close)
	_, err, _ = readNonStreamChunkWithAbsoluteDeadline(blocked3, make([]byte, 8), canceled, ptrInt64(base+60_000), ptrInt64(120_000), nil)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) || !aborted.UpstreamRequestStarted {
		t.Fatalf("err = %v", err)
	}
}

// delayedReader 延迟后返回一小块数据再 EOF。
type delayedReader struct {
	delay time.Duration
	done  bool
}

func (d *delayedReader) Read(buffer []byte) (int, error) {
	if d.done {
		return 0, io.EOF
	}
	d.done = true
	time.Sleep(d.delay)
	copy(buffer, "abc")
	return 3, nil
}

func TestNonStreamBodyMaxLifetimeDeadlineAt(t *testing.T) {
	if nonStreamBodyMaxLifetimeDeadlineAt(1_000, nil) != nil {
		t.Fatal("无上限返回 nil")
	}
	zero := int64(0)
	if nonStreamBodyMaxLifetimeDeadlineAt(1_000, &zero) != nil {
		t.Fatal("非正上限返回 nil")
	}
	value := int64(5_000)
	deadline := nonStreamBodyMaxLifetimeDeadlineAt(1_000, &value)
	if deadline == nil || *deadline != 6_000 {
		t.Fatalf("deadline = %#v", deadline)
	}
	negative := int64(-1)
	if nonStreamBodyMaxLifetimeDeadlineAt(1_000, &negative) != nil {
		t.Fatal("负上限返回 nil")
	}
}

func TestLimitedAndRollingBufferCaptures(t *testing.T) {
	capture := newLimitedBufferCapture(8)
	capture.push([]byte("12345678"))
	capture.push([]byte("extra")) // 截断
	if !capture.truncated {
		t.Fatal("超出限制必须标记截断")
	}
	if string(capture.buffer()) != "12345678" {
		t.Fatalf("buffer = %q", capture.buffer())
	}
	if capture.completeBuffer() != nil {
		t.Fatal("截断后 completeBuffer 为 nil")
	}
	if capture.toText() == nil || *capture.toText() != "12345678" {
		t.Fatalf("toText = %#v", capture.toText())
	}
	empty := newLimitedBufferCapture(8)
	if empty.completeBuffer() != nil || empty.toText() != nil {
		t.Fatal("空捕获的文本面为 nil")
	}
	// 负 limit 不收集。
	disabled := newLimitedBufferCapture(-1)
	disabled.push([]byte("x"))
	if len(disabled.buffer()) != 0 {
		t.Fatal("负 limit 不收集")
	}

	rolling := newRollingBufferCapture(8)
	rolling.push([]byte("12345678"))
	rolling.push([]byte("90")) // 触发 trimOverflow 头部消费
	text := rolling.toText()
	if text == nil || *text != "34567890" {
		t.Fatalf("rolling text = %#v", text)
	}
	// 超大 chunk 直接替换窗口。
	rolling.push([]byte("abcdefgh"))
	rolling.push([]byte("IJKLMNOPQ"))
	if got := *rolling.toText(); got != "JKLMNOPQ" {
		t.Fatalf("replace window = %q", got)
	}
	// 逐块消费后 compact（headIndex>64 分支用小步推进验证不到，走基本路径）。
	small := newRollingBufferCapture(4)
	small.push([]byte("ab"))
	small.push([]byte("cd"))
	small.push([]byte("ef"))
	if got := *small.toText(); got != "cdef" {
		t.Fatalf("small rolling = %q", got)
	}
	zeroLimit := newRollingBufferCapture(0)
	zeroLimit.push([]byte("x"))
	if zeroLimit.toText() != nil {
		t.Fatal("零 limit 不收集")
	}
}

func TestBuildNonStreamPipeResultTexts(t *testing.T) {
	capture := newLimitedBufferCapture(4)
	capture.push([]byte("abcdef"))
	tail := newRollingBufferCapture(3)
	tail.push([]byte("abcdef"))
	result := buildNonStreamPipeResult(capture, tail, true, 42, 6)
	if result.CapturedBodyText == nil || *result.CapturedBodyText != "abcd" {
		t.Fatalf("captured = %#v", result.CapturedBodyText)
	}
	if result.CaptureTruncated != true {
		t.Fatal("截断标记应保留")
	}
	if result.DiagnosticBodyText == nil || !strings.HasSuffix(*result.DiagnosticBodyText, "\n[truncated]") {
		t.Fatalf("diagnostic = %#v", result.DiagnosticBodyText)
	}
	if result.UsageTailText == nil || *result.UsageTailText != "def" {
		t.Fatalf("usage tail = %#v", result.UsageTailText)
	}
	if result.FirstByteMs == nil || *result.FirstByteMs != 42 {
		t.Fatalf("firstByte = %#v", result.FirstByteMs)
	}
	// 未见到首字节时无 FirstByteMs。
	noFirstByte := buildNonStreamPipeResult(newLimitedBufferCapture(4), newRollingBufferCapture(3), false, 0, 0)
	if noFirstByte.FirstByteMs != nil {
		t.Fatal("未见首字节不应有 FirstByteMs")
	}
}

func TestBodySmallHelpers(t *testing.T) {
	if errorMessageOf(nil, "fallback") != "fallback" {
		t.Fatal("nil 错误回退 fallback")
	}
	if errorMessageOf(errors.New("boom"), "fallback") != "boom" {
		t.Fatal("错误消息优先")
	}
	if errorMessageOf(errors.New(""), "fallback") != "fallback" {
		t.Fatal("空错误消息回退 fallback")
	}
	if *bytesTextPtr([]byte("x")) != "x" {
		t.Fatal("bytesTextPtr 语义不符")
	}
	if derefInt64(nil) != 0 || derefInt64(ptrInt64(7)) != 7 {
		t.Fatal("derefInt64 语义不符")
	}
	if derefOrMax(nil) != int64(1)<<62 || derefOrMax(ptrInt64(3)) != 3 {
		t.Fatal("derefOrMax 语义不符")
	}
	if !derefMin(nil, nil) || !derefMin(ptrInt64(1), ptrInt64(2)) || derefMin(ptrInt64(2), ptrInt64(1)) {
		t.Fatal("derefMin 语义不符")
	}
	if formatSecondsText(1_500) != "2s" || formatSecondsText(2_000) != "2s" {
		t.Fatal("formatSecondsText 语义不符")
	}
}

func TestReadUpstreamBodyLimitedFirstByteAndAbort(t *testing.T) {
	base := int64(1_000)
	injectNowMs(t, func() int64 { return base + 25 })
	firstByteCalled := false
	result, err := ReadUpstreamBodyLimited(context.Background(), strings.NewReader("hello"), LimitedBodyReadInput{
		StartedAt:   ptrInt64(base),
		OnFirstByte: func() { firstByteCalled = true },
		Signal:      context.Background(),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !firstByteCalled {
		t.Fatal("首字节回调未触发")
	}
	if result.BodyText != "hello" || result.ReadBytes != 5 {
		t.Fatalf("result = %#v", result)
	}
	if result.FirstByteMs == nil || *result.FirstByteMs != 25 {
		t.Fatalf("firstByteMs = %#v", result.FirstByteMs)
	}
	// nil body 透传空结果。
	if result, err := ReadUpstreamBodyLimited(context.Background(), nil, LimitedBodyReadInput{}); err != nil || result.ReadBytes != 0 {
		t.Fatalf("nil body: %#v %v", result, err)
	}
	// 中途取消 → aborted。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ReadUpstreamBodyLimited(context.Background(), &stuckReader{release: make(chan struct{})}, LimitedBodyReadInput{
		Signal: canceled,
	})
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
}

// stuckReader 阻塞到 release。
type stuckReader struct {
	release chan struct{}
}

func (s *stuckReader) Read([]byte) (int, error) {
	<-s.release
	return 0, io.EOF
}

func TestReadUpstreamBodyLimitedIncompleteRead(t *testing.T) {
	_, err := ReadUpstreamBodyLimited(context.Background(), &failingReader{}, LimitedBodyReadInput{
		Signal: context.Background(),
	})
	var incomplete *UpstreamBodyReadIncompleteError
	if !errorsAs(err, &incomplete) {
		t.Fatalf("err = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("连接重置") }
