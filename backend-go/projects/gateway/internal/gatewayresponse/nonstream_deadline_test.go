package gatewayresponse

// R5：非流式管线首字截止竞速（速度优先切号核心机制）。覆盖 timer 胜出
// abort/continue、决策期间读 settle 的 supersede 双管线语义、deadline 前
// 读胜出与 HandleNonStreamUpstreamResponse 的字段透传。

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// deadlinePipeInput 在基础管道入参上配置竞速三字段，返回 superseded 观测指针。
func deadlinePipeInput(
	t *testing.T,
	body UpstreamBody,
	deadlineMs int64,
	handler FirstByteDeadlineHandler,
) (NonStreamPipeInput, *httptest.ResponseRecorder, *bool) {
	t.Helper()
	recorder := httptest.NewRecorder()
	superseded := false
	input := NonStreamPipeInput{
		Body:                          body,
		Downstream:                    StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)},
		StartedAtMs:                   1000,
		NowMs:                         func() int64 { return 1000 },
		FirstByteDeadlineMs:           &deadlineMs,
		OnFirstByteDeadline:           handler,
		OnFirstByteDeadlineSuperseded: func() { superseded = true },
	}
	return input, recorder, &superseded
}

func TestNonStreamFirstByteDeadlineTimerWinAborts(t *testing.T) {
	body := newChanBody()
	handlerCalled := false
	var observedInput FirstByteDeadlineInput
	input, _, superseded := deadlinePipeInput(t, body, 60, func(in FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		handlerCalled = true
		observedInput = in
		return FirstByteDeadlineAbort, nil
	})
	_, err := PipeNonStreamUpstreamResponse(input)
	var pipeErr *NonStreamBodyPipeError
	if !errors.As(err, &pipeErr) {
		t.Fatalf("err = %v", err)
	}
	var timeoutErr *gatewaydispatch.GatewayFirstByteTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("unwrap err = %v", err)
	}
	if timeoutErr.Source != gatewaydispatch.FirstByteTimeoutSourceConfiguredDeadline {
		t.Fatalf("source = %q", timeoutErr.Source)
	}
	if timeoutErr.TimeoutMs != 60 {
		t.Fatalf("timeoutMs = %d", timeoutErr.TimeoutMs)
	}
	if !strings.HasSuffix(timeoutErr.Message, "后仍未返回首个字节") {
		t.Fatalf("message = %q", timeoutErr.Message)
	}
	if !handlerCalled {
		t.Fatal("timer 胜出应调用路由决策回调")
	}
	if observedInput.Transport != "non_stream" {
		t.Fatalf("decisionInput = %+v", observedInput)
	}
	if *superseded {
		t.Fatal("timer 胜出 abort 不应通知 superseded")
	}
}

func TestNonStreamFirstByteDeadlineContinueKeepsReading(t *testing.T) {
	body := newChanBody()
	input, recorder, superseded := deadlinePipeInput(t, body, 60, func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, nil
	})
	go func() {
		time.Sleep(30 * time.Millisecond)
		body.push([]byte(`{"late":`))
		body.push([]byte(`true}`))
		body.end()
	}()
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(recorder.Body.String(), `"late"`) {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if result.TransferredBytes == 0 {
		t.Fatalf("result = %+v", result)
	}
	if *superseded {
		t.Fatal("continue 决策后的正常读不应通知 superseded")
	}
}

// deadlineBody 是 push/end 可能来自不同 goroutine 的线程安全 chanBody 变体：
// 并发 send 与 close 是 Go channel 的数据竞争，必须由调用方串行化。
type deadlineBody struct {
	mu     sync.Mutex
	ch     chan ChunkResult
	closed bool
}

func newMutexBody() *deadlineBody {
	return &deadlineBody{ch: make(chan ChunkResult)}
}

func (b *deadlineBody) push(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.ch <- ChunkResult{Data: data}
}

func (b *deadlineBody) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.ch)
	}
}

func (b *deadlineBody) Next() <-chan ChunkResult { return b.ch }

func (b *deadlineBody) Close() { b.end() }

func TestNonStreamFirstByteDeadlineReadSupersedesOnPlainPipe(t *testing.T) {
	body := newMutexBody()
	input, recorder, superseded := deadlinePipeInput(t, body, 80, func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		// 决策执行期间首块 settle：纯透传管线的原始字节推翻截止决策。
		body.push([]byte(`{"a":1}`))
		time.Sleep(20 * time.Millisecond)
		return FirstByteDeadlineContinue, nil
	})
	go func() {
		time.Sleep(300 * time.Millisecond)
		body.end()
	}()
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Body.String() != `{"a":1}` {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if result.TransferredBytes != int64(len(`{"a":1}`)) {
		t.Fatalf("result = %+v", result)
	}
	if !*superseded {
		t.Fatal("透传管线读胜出应通知 superseded")
	}
}

func TestNonStreamFirstByteDeadlineInspectionPipeRequiresSemanticRead(t *testing.T) {
	body := newChanBody()
	input, _, superseded := deadlinePipeInput(t, body, 80, func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		// 决策执行期间原始字节 settle，但检查管线只有语义决策可推翻：
		// handler 返回 abort → 按「完整语义响应」configured 超时。
		body.push([]byte(`{"a":1}`))
		time.Sleep(20 * time.Millisecond)
		return FirstByteDeadlineAbort, nil
	})
	input.InspectBytes = 1024
	_, err := PipeNonStreamUpstreamResponse(input)
	var pipeErr *NonStreamBodyPipeError
	if !errors.As(err, &pipeErr) {
		t.Fatalf("err = %v", err)
	}
	var timeoutErr *gatewaydispatch.GatewayFirstByteTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("unwrap err = %v", err)
	}
	if timeoutErr.Source != gatewaydispatch.FirstByteTimeoutSourceConfiguredDeadline {
		t.Fatalf("source = %q", timeoutErr.Source)
	}
	if !strings.HasSuffix(timeoutErr.Message, "后仍未返回完整语义响应") {
		t.Fatalf("message = %q", timeoutErr.Message)
	}
	if *superseded {
		t.Fatal("检查管线原始字节不应推翻截止决策")
	}
}

func TestNonStreamFirstByteDeadlineReadBeatsDeadline(t *testing.T) {
	body := newChanBody()
	handlerCalled := false
	input, recorder, superseded := deadlinePipeInput(t, body, 5_000, func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		handlerCalled = true
		return FirstByteDeadlineAbort, nil
	})
	go func() {
		body.push([]byte("fast"))
		body.end()
	}()
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Body.String() != "fast" || result.TransferredBytes != 4 {
		t.Fatalf("result = %+v body = %q", result, recorder.Body.String())
	}
	if handlerCalled || *superseded {
		t.Fatal("deadline 前读胜出不应触发决策回调与 superseded")
	}
}

func TestNonStreamHandleUpstreamResponseForwardsFirstByteDeadline(t *testing.T) {
	// R5 形状全链：2xx JSON + 慢 body，HandleUpstreamResponseInput 的截止
	// 字段经 pipeSpec 进入管道（此前被丢弃），timer 胜 abort 冒泡
	// configured_deadline 首字超时。
	body := newChanBody()
	input, _ := newInputFixture(body, 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	deadlineMs := int64(50)
	input.FirstByteDeadlineMs = &deadlineMs
	input.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineAbort, nil
	}
	supersededCalled := false
	input.OnFirstByteDeadlineSuperseded = func() { supersededCalled = true }
	_, err := HandleNonStreamUpstreamResponse(input)
	var timeoutErr *gatewaydispatch.GatewayFirstByteTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v", err)
	}
	if timeoutErr.Source != gatewaydispatch.FirstByteTimeoutSourceConfiguredDeadline || timeoutErr.TimeoutMs != 50 {
		t.Fatalf("timeoutErr = %+v", timeoutErr)
	}
	if supersededCalled {
		t.Fatal("timer 胜出 abort 不应通知 superseded")
	}
}
