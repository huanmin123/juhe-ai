package accountprobe

// BUG-0287 回归：本级预算在响应体中途到点属于探针侧超时，必须保留超时证据
// 让 [10s,20s,30s] 分级阶梯逐级晋级，终审按 server_diagnostic_timeout 口径；
// 上游真实读取中断（非超时错误）与截断前已给出明确上游错误语义的截断保持
// 既有 read_incomplete 终审、不晋级。全部用例经注入传输层确定性驱动，
// 不依赖真实计时竞态。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// w287BudgetTransport 按请求序次返回预置响应体工厂；attempt 记录真实尝试次数。
type w287BudgetTransport struct {
	mu      sync.Mutex
	attempt int
	bodies  []func(requestCtx context.Context) io.ReadCloser
}

func (t *w287BudgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	index := t.attempt
	if index >= len(t.bodies) {
		index = len(t.bodies) - 1
	}
	t.attempt++
	t.mu.Unlock()
	return &http.Response{
		StatusCode:    http.StatusOK,
		Body:          t.bodies[index](request.Context()),
		Header:        http.Header{},
		ContentLength: -1,
		Request:       request,
	}, nil
}

func (t *w287BudgetTransport) attempts() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempt
}

// w287DeadlineBody 先吐出 head，随后阻塞到请求 ctx 到点并返回 ctx 错误——
// 等价真实传输层在探针本级预算到期时中断响应体读取。
type w287DeadlineBody struct {
	ctx     context.Context
	head    string
	emitted bool
}

func (b *w287DeadlineBody) Read(p []byte) (int, error) {
	if !b.emitted {
		b.emitted = true
		return copy(p, b.head), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func w287Service(t *testing.T, transport http.RoundTripper, timeouts []time.Duration) (*Service, *View) {
	t.Helper()
	view := probeView("https://bug287-upstream.invalid")
	service, err := NewService(Options{
		Source:        &fakeSource{view: view},
		Secret:        "bug287-secret",
		Client:        &http.Client{Transport: transport},
		Now:           testNowNow,
		RetryTimeouts: timeouts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, view
}

func w287DeadlineBodyFactory(head string) func(context.Context) io.ReadCloser {
	return func(ctx context.Context) io.ReadCloser {
		return io.NopCloser(&w287DeadlineBody{ctx: ctx, head: head})
	}
}

// 分级阶梯耗尽：每一级都因流中途截断晋级，终审按探针超时口径并保留
// 部分响应作为诊断（HTTP 200 + 已读正文）。
func TestBug287BudgetDeadlineMidBodyEscalatesToTimeout(t *testing.T) {
	head := `{"choices":[{"delta":{"content":"juhe"}}]}`
	transport := &w287BudgetTransport{bodies: []func(context.Context) io.ReadCloser{
		w287DeadlineBodyFactory(head),
	}}
	service, view := w287Service(t, transport, []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 80 * time.Millisecond})

	observation, err := service.probeFixedKey(context.Background(), view, &view.APIKeyEntries[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.attempts(); got != 3 {
		t.Fatalf("流中途截断必须逐级晋级到阶梯耗尽, attempts=%d", got)
	}
	if observation.Result.Success ||
		observation.Result.ErrorCode != "server_diagnostic_timeout" ||
		observation.Result.Message != "账户测试超时" {
		t.Fatalf("终审必须按探针超时口径: %+v", observation.Result)
	}
	if !observation.Evidence.TimedOut ||
		observation.Evidence.TransportFailureKind != "timeout" ||
		observation.Evidence.UpstreamCompleted {
		t.Fatalf("超时证据不符: %+v", observation.Evidence)
	}
	if observation.Result.StatusCode == nil || *observation.Result.StatusCode != http.StatusOK ||
		!strings.Contains(observation.Result.ResponseBodyText, "juhe") {
		t.Fatalf("已收到的部分响应必须保留为诊断: %+v", observation.Result)
	}
}

// 慢上游在第二轮预算内完成：阶梯必须治愈用户场景（流开始但 10s 内读不完），
// 不再 10s 一票终审。
func TestBug287EscalationRecoversSlowUpstream(t *testing.T) {
	complete := `{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`
	transport := &w287BudgetTransport{bodies: []func(context.Context) io.ReadCloser{
		w287DeadlineBodyFactory(`{"choices":[{"delta":{"content":"ju"}`),
		func(context.Context) io.ReadCloser {
			return io.NopCloser(strings.NewReader(complete))
		},
	}}
	service, view := w287Service(t, transport, []time.Duration{40 * time.Millisecond, 5 * time.Second})

	observation, err := service.probeFixedKey(context.Background(), view, &view.APIKeyEntries[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.attempts(); got != 2 {
		t.Fatalf("首轮截断后必须晋级重测, attempts=%d", got)
	}
	if !observation.Result.Success {
		t.Fatalf("第二轮完整响应必须成功: %+v", observation.Result)
	}
	if observation.Evidence.TransportFailureKind != "" || observation.Evidence.TimedOut {
		t.Fatalf("成功观测不得携带传输失败证据: %+v", observation.Evidence)
	}
}

// 上游真实读取中断（非超时错误）保持 read_incomplete 终审，不晋级。
func TestBug287RealReadInterruptionStaysReadIncomplete(t *testing.T) {
	head := `{"choices":[{"delta":{"content":"juhe"}}]}`
	transport := &w287BudgetTransport{bodies: []func(context.Context) io.ReadCloser{
		func(context.Context) io.ReadCloser {
			return io.NopCloser(io.MultiReader(strings.NewReader(head), &w12hFailReader{}))
		},
	}}
	service, view := w287Service(t, transport, []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 80 * time.Millisecond})

	observation, err := service.probeFixedKey(context.Background(), view, &view.APIKeyEntries[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.attempts(); got != 1 {
		t.Fatalf("真实读取中断不得晋级, attempts=%d", got)
	}
	if observation.Result.Success || observation.Result.ErrorCode != errorCodeInvalidProtocolSuccessResponse {
		t.Fatalf("读取中断保持缺证据终审: %+v", observation.Result)
	}
	if observation.Evidence.TimedOut || observation.Evidence.TransportFailureKind != "read_incomplete" {
		t.Fatalf("读取中断证据不符: %+v", observation.Evidence)
	}
}

// 截断前上游已给出明确错误语义：不得改判探针超时、不晋级，保持
// read_incomplete 终审（对外错误码沿用 classifyResponse 既有口径）。
func TestBug287UpstreamErrorSemanticSurvivesDeadlineCut(t *testing.T) {
	head := `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`
	transport := &w287BudgetTransport{bodies: []func(context.Context) io.ReadCloser{
		w287DeadlineBodyFactory(head),
	}}
	service, view := w287Service(t, transport, []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 80 * time.Millisecond})

	observation, err := service.probeFixedKey(context.Background(), view, &view.APIKeyEntries[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.attempts(); got != 1 {
		t.Fatalf("上游错误语义明确时不得晋级, attempts=%d", got)
	}
	if observation.Result.ErrorCode == "server_diagnostic_timeout" || observation.Result.Message == "账户测试超时" {
		t.Fatalf("上游错误截断不得改判探针超时: %+v", observation.Result)
	}
	if observation.Evidence.TimedOut || observation.Evidence.TransportFailureKind != "read_incomplete" {
		t.Fatalf("上游错误截断证据不符: %+v", observation.Evidence)
	}
}

// limited（冷却复测）口径：终审错误码与晋级证据保留，文案统一脱敏。
func TestBug287LimitedMaskingKeepsTimeoutEvidence(t *testing.T) {
	head := `{"choices":[{"delta":{"content":"juhe"}}]}`
	transport := &w287BudgetTransport{bodies: []func(context.Context) io.ReadCloser{
		w287DeadlineBodyFactory(head),
	}}
	service, view := w287Service(t, transport, []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 80 * time.Millisecond})

	observation, err := service.probeFixedKey(context.Background(), view, &view.APIKeyEntries[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.attempts(); got != 3 {
		t.Fatalf("limited 口径同样必须晋级, attempts=%d", got)
	}
	if observation.Result.ErrorCode != "server_diagnostic_timeout" || observation.Result.Message != "上游请求失败" {
		t.Fatalf("limited 终审文案不符: %+v", observation.Result)
	}
	if !observation.Evidence.TimedOut || observation.Evidence.TransportFailureKind != "timeout" {
		t.Fatalf("limited 超时证据不符: %+v", observation.Evidence)
	}
}
