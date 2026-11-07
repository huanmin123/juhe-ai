package main

// 2026-11-07 跨协议 Responses→Chat 桥退役：原保留 bridge 的增量管道语义用例
// 改钉 Code Assist 解包路径——bridgeStreamPipedResponse 的 io.Pipe 增量机制
// 只剩 Gemini Code Assist unwrap（transformGeminiCodeAssistIfApplicable 流式
// 分支）这一个生产者。已退役 responses→chat / chat↔anthropic / gemini 方向
// 的测试删除，不再以测试固化旧行为。

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// gatedUpstreamBody 模拟“写出一个事件后阻塞、从不主动 EOF”的上游 SSE 流：
// 首次 Read 交付 chunk 并关闭 emitted，后续 Read 阻塞在 release 上；release
// 关闭后按 readErr（nil = io.EOF）收尾。
type gatedUpstreamBody struct {
	chunk     string
	chunkRead bool
	emitted   chan struct{}
	release   chan struct{}
	readErr   error
}

func (b *gatedUpstreamBody) Read(p []byte) (int, error) {
	if !b.chunkRead {
		b.chunkRead = true
		n := copy(p, b.chunk)
		close(b.emitted)
		return n, nil
	}
	<-b.release
	if b.readErr != nil {
		return 0, b.readErr
	}
	return 0, io.EOF
}

func (b *gatedUpstreamBody) Close() error { return nil }

func codeAssistUpstreamEvent() string {
	return "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}]}}\n\n"
}

func newBridgeUpstreamResponse(contentType string, body io.ReadCloser) *gatewaydispatch.GatewayUpstreamResponse {
	header := http.Header{}
	header.Set("Content-Type", contentType)
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK, header, body)
}

type streamReadResult struct {
	n    int
	text string
	err  error
}

func readStreamOnce(body io.Reader, result chan<- streamReadResult) {
	buffer := make([]byte, 8192)
	n, err := body.Read(buffer)
	result <- streamReadResult{n: n, text: string(buffer[:n]), err: err}
}

// TestChainBridgeCodeAssistStreamFirstEventReadableBeforeUpstreamEOF 钉住
// Code Assist unwrap 路径的增量语义：{response:...} 包装在事件边界到达时
// 即时解开，下游在上游 EOF 前即可读到首个解包事件。
func TestChainBridgeCodeAssistStreamFirstEventReadableBeforeUpstreamEOF(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &gatedUpstreamBody{
		chunk:   codeAssistUpstreamEvent(),
		emitted: emitted,
		release: release,
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	response := newBridgeUpstreamResponse("text/event-stream", upstreamBody)
	input := nativeCodeAssistTransformerTestInput(t, http.MethodPost,
		"/v1beta/models/gem-1:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"ping"}]}]}`,
		codeAssistAccount(), response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	firstRead := make(chan streamReadResult, 1)
	go readStreamOnce(got.Body, firstRead)
	var first streamReadResult
	select {
	case first = <-firstRead:
	case <-time.After(2 * time.Second):
		t.Fatalf("Code Assist 上游未 EOF 时下游 2s 内未读到首个解包事件")
	}
	if first.err != nil {
		t.Fatalf("首个解包事件读取失败: %v", first.err)
	}
	if !strings.Contains(first.text, "\"candidates\"") || strings.Contains(first.text, "\"response\":") {
		t.Fatalf("首个解包事件必须是去包装后的 gemini 载荷, got %q", first.text)
	}
	select {
	case <-emitted:
	default:
		t.Fatalf("下游读到首事件时上游 chunk 尚未被消费（管道时序异常）")
	}
	close(release)
	if _, err := io.ReadAll(got.Body); err != nil {
		t.Fatalf("排空下游失败: %v", err)
	}
}

// TestChainBridgeCodeAssistStreamPropagatesUpstreamDisconnect 钉住错误传播：
// 上游中途断开时，下游读取必须以同一错误结束，而不是静默当作正常 EOF。
func TestChainBridgeCodeAssistStreamPropagatesUpstreamDisconnect(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	disconnect := errors.New("upstream disconnected mid-stream")
	upstreamBody := &gatedUpstreamBody{
		chunk:   codeAssistUpstreamEvent(),
		emitted: emitted,
		release: release,
		readErr: disconnect,
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	response := newBridgeUpstreamResponse("text/event-stream", upstreamBody)
	input := nativeCodeAssistTransformerTestInput(t, http.MethodPost,
		"/v1beta/models/gem-1:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"ping"}]}]}`,
		codeAssistAccount(), response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	readAllDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(got.Body)
		readAllDone <- err
	}()
	close(release)
	select {
	case err := <-readAllDone:
		if !errors.Is(err, disconnect) {
			t.Fatalf("上游断开必须传播为下游读取错误, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("上游断开未在 2s 内传播为下游读取错误")
	}
}

// TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose 钉住
// 取消语义：下游提前关闭管道读端时，pump goroutine 必须退出并关闭上游体。
func TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &closingTrackingBody{gatedUpstreamBody: gatedUpstreamBody{
		chunk:   codeAssistUpstreamEvent(),
		emitted: emitted,
		release: release,
	}}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	response := newBridgeUpstreamResponse("text/event-stream", upstreamBody)
	input := nativeCodeAssistTransformerTestInput(t, http.MethodPost,
		"/v1beta/models/gem-1:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"ping"}]}]}`,
		codeAssistAccount(), response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if err := got.Body.Close(); err != nil {
		t.Fatalf("close downstream: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if upstreamBody.upstreamClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("下游关闭后 pump goroutine 未在 2s 内关闭上游体")
}

// closingTrackingBody 在 gatedUpstreamBody 之上记录 Close 是否被调用。
type closingTrackingBody struct {
	gatedUpstreamBody
	mu          sync.Mutex
	closeCalled bool
}

func (b *closingTrackingBody) Close() error {
	b.mu.Lock()
	b.closeCalled = true
	b.mu.Unlock()
	return b.gatedUpstreamBody.Close()
}

func (b *closingTrackingBody) upstreamClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalled
}
