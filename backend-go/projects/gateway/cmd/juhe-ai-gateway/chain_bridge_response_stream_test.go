package main

// 2026-11-05 协议面收窄后，流式跨协议映射只保留 OpenAI v1 Responses ->
// Chat Completions（chat-only 账户承接 /v1/responses）。本文件覆盖该保留
// bridge 的增量管道语义（首事件先于上游 EOF、断开传播、取消关闭上游体），
// 以及 Code Assist 同族解包的增量语义。已退役 chat↔anthropic/gemini 方向
// 的测试删除，不再以测试固化旧行为。

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
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

// bridgeStreamMappingAccount 构造保留的 responses -> chat_completions bridge
// 账户（chat-only 上游），用于增量管道语义测试。
func bridgeStreamMappingAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:           "acc_bridge_stream",
		VendorCode:   "glm",
		ProtocolCode: "openai",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   "responses",
			UpstreamModel:          "mapped-upstream-model",
			UpstreamEndpointFamily: "chat_completions",
			Enabled:                true,
		}},
	}
}

func bridgeStreamTransformInput(t *testing.T, stream bool, response *gatewaydispatch.GatewayUpstreamResponse) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	streamText := "false"
	if stream {
		streamText = "true"
	}
	body := `{"model":"client-model","stream":` + streamText + `,"input":"hi"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return gatewaydispatch.UpstreamResponseTransformInput{
		Req:      req,
		Account:  bridgeStreamMappingAccount(),
		Response: response,
	}
}

func chatBridgeUpstreamEvent() string {
	return "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"real-upstream-model-v1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"
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

// TestChainBridgeResponseStreamFirstEventReadableBeforeUpstreamEOF 钉住保留
// bridge 的增量语义：上游写出一个事件并阻塞（未 EOF）时，下游必须已能读到
// 首个转换后的 Responses SSE 事件。
func TestChainBridgeResponseStreamFirstEventReadableBeforeUpstreamEOF(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &gatedUpstreamBody{
		chunk:   chatBridgeUpstreamEvent(),
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
	input := bridgeStreamTransformInput(t, true, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "event-stream") {
		t.Fatalf("streaming bridge must answer SSE, got %q", ct)
	}

	firstRead := make(chan streamReadResult, 1)
	go readStreamOnce(got.Body, firstRead)
	var first streamReadResult
	select {
	case first = <-firstRead:
	case <-time.After(2 * time.Second):
		t.Fatalf("上游未 EOF 时下游 2s 内未读到首个转换事件（转换仍是等全量 buffer）")
	}
	if first.err != nil {
		t.Fatalf("首个转换事件读取失败: %v", first.err)
	}
	if !strings.Contains(first.text, "event: response.") {
		t.Fatalf("首个转换事件必须是 Responses SSE 帧, got %q", first.text)
	}
	select {
	case <-emitted:
	default:
		t.Fatalf("下游读到首事件时上游 chunk 尚未被消费（管道时序异常）")
	}

	// 放行上游 EOF；排空下游即可，尾部完整性由 glm_responses_chat_bridge_
	// adjudication_test 的完整转换用例覆盖。
	close(release)
	_, _ = io.ReadAll(got.Body)
}

// TestChainBridgeResponseStreamPropagatesUpstreamDisconnect 钉住错误传播：
// 上游中途断开时，下游读取必须以同一错误结束，而不是静默当作正常 EOF。
func TestChainBridgeResponseStreamPropagatesUpstreamDisconnect(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	disconnect := errors.New("upstream disconnected mid-stream")
	upstreamBody := &gatedUpstreamBody{
		chunk:   chatBridgeUpstreamEvent(),
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
	input := bridgeStreamTransformInput(t, true, response)

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

// TestChainBridgeCodeAssistStreamFirstEventReadableBeforeUpstreamEOF 钉住
// Code Assist unwrap 路径的增量语义：{response:...} 包装在事件边界到达时
// 即时解开，下游在上游 EOF 前即可读到首个解包事件。
func TestChainBridgeCodeAssistStreamFirstEventReadableBeforeUpstreamEOF(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &gatedUpstreamBody{
		chunk:   "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}]}}\n\n",
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
	close(release)
	if _, err := io.ReadAll(got.Body); err != nil {
		t.Fatalf("排空下游失败: %v", err)
	}
}

// TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose 钉住
// 取消语义：下游提前关闭管道读端时，pump goroutine 必须退出并关闭上游体。
func TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &closingTrackingBody{gatedUpstreamBody: gatedUpstreamBody{
		chunk:   chatBridgeUpstreamEvent(),
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
	input := bridgeStreamTransformInput(t, true, response)

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
