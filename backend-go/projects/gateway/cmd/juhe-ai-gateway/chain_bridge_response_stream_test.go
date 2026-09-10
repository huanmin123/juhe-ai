package main

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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat"
)

// gatedUpstreamBody 模拟"写出一个事件后阻塞、从不主动 EOF"的上游 SSE 流：
// 首次 Read 交付 chunk 并关闭 emitted，后续 Read 阻塞在 release 上；release
// 关闭后按 readErr（nil = io.EOF）收尾。测试用它证明下游读到首个转换事件时
// 上游尚未 EOF。
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

// bridgeStreamMappingAccount 构造 hybrid 账户上 responses -> gemini 的跨协议
// mapping（gatewayopenai 支持面），上游模型名与上游真实 modelVersion 刻意
// 不同，用于区分"映射名"与"上游原生归因"。
func bridgeStreamMappingAccount() gatewaydispatch.AccountCandidate {
	enabled := true
	return gatewaydispatch.AccountCandidate{
		ID:           "acc_bridge_stream",
		ProviderCode: "hybrid",
		ProtocolCode: "gemini",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   "responses",
			UpstreamModel:          "mapped-upstream-model",
			// 映射行存储的是 gatewayopenai 词汇（generate_content），桥侧经
			// NormalizeEndpointFamily 归一为 gemini_generate_content。
			UpstreamEndpointFamily: "generate_content",
			Enabled:                enabled,
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
	// ParsedJSONObjectBody 依赖 body 管道捕获态，测试里手工挂载（对齐
	// chain_driver_gemini_mapped_url_test 的构造方式）。
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return gatewaydispatch.UpstreamResponseTransformInput{
		Req:      req,
		Account:  bridgeStreamMappingAccount(),
		Response: response,
	}
}

func geminiBridgeUpstreamEvent() string {
	return "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}],\"modelVersion\":\"real-upstream-model-v1\"}\n\n"
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

// TestChainBridgeResponseStreamFirstEventReadableBeforeUpstreamEOF 钉住 P1
// 核心语义：跨协议桥的流式转换是增量管道——上游写出一个事件并阻塞（未
// EOF）时，下游必须已能读到首个转换事件；同时对照 buffer 版本断言完整输出
// 逐字节一致（增量不改变转换结果）。
func TestChainBridgeResponseStreamFirstEventReadableBeforeUpstreamEOF(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &gatedUpstreamBody{
		chunk:   geminiBridgeUpstreamEvent(),
		emitted: emitted,
		release: release,
	}
	t.Cleanup(func() {
		// 失败路径也要让 pump goroutine 退出并释放管道。
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

	// 首个转换事件必须在上游 EOF 之前可读（2s 超时判定）。
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
	if !strings.Contains(first.text, "data:") {
		t.Fatalf("首个转换事件必须是 Responses SSE 帧, got %q", first.text)
	}
	select {
	case <-emitted:
	default:
		t.Fatalf("下游读到首事件时上游 chunk 尚未被消费（管道时序异常）")
	}

	// 放行上游 EOF，排空下游，对照 buffer 转换函数逐字节一致。
	close(release)
	rest, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("排空下游失败: %v", err)
	}
	full := first.text + string(rest)
	buffered := openaicompat.TransformGeminiSseBufferToDownstreamSse(
		[]byte(geminiBridgeUpstreamEvent()), openaicompat.GeminiNativeProtocolResponses, "mapped-upstream-model")
	if full != string(buffered) {
		t.Fatalf("增量管道输出必须与 buffer 转换逐字节一致:\n增量=%q\nbuffer=%q", full, string(buffered))
	}
}

// TestChainBridgeResponseStreamPropagatesUpstreamDisconnect 钉住错误传播：
// 上游中途断开时，下游读取必须以同一错误结束（Node abort 分支的截断语义），
// 而不是静默当作正常 EOF。
func TestChainBridgeResponseStreamPropagatesUpstreamDisconnect(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	disconnect := errors.New("upstream disconnected mid-stream")
	upstreamBody := &gatedUpstreamBody{
		chunk:   geminiBridgeUpstreamEvent(),
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
	// ReadAll 在 goroutine 中执行：它会先读到首个事件，然后阻塞在未放行的
	// 上游读上；close(release) 触发上游断开，错误必须经管道传播回来。
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

// TestChainBridgeResponseNonStreamMappingKeepsBufferSemantics 钉住非流式
// 回归：非流式桥转换保持 buffer 语义（JSON 收集），Content-Type 为 JSON。
func TestChainBridgeResponseNonStreamMappingKeepsBufferSemantics(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	payload := "{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"full\"}]}}],\"modelVersion\":\"real-upstream-model-v1\"}"
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload)))
	input := bridgeStreamTransformInput(t, false, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "full") {
		t.Fatalf("非流式桥转换必须产出完整 JSON, got %q", text)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "json") {
		t.Fatalf("非流式桥转换必须回答 JSON, got %q", ct)
	}
}

// TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose 钉住
// 取消语义：下游提前关闭管道读端时，pump goroutine 必须退出并关闭上游体
// （并发槽释放/请求取消挂在 Body.Close 上）。
func TestChainBridgeResponseStreamPipeClosesUpstreamBodyOnDownstreamClose(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	release := make(chan struct{})
	emitted := make(chan struct{})
	upstreamBody := &closingTrackingBody{gatedUpstreamBody: gatedUpstreamBody{
		chunk:   geminiBridgeUpstreamEvent(),
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
