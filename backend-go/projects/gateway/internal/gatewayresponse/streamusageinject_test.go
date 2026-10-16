package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 流式零计费修复的响应侧测试：网关注入 include_usage 的判定（客户端显式要求
// 与网关注入的区分）、usage-only 帧剥除的纯函数语义、流管道剥除后计量不变、
// 以及 HandleStreamUpstreamResponse 装配面（RequestBody + Req 推导）接线。

// chatUsageOnlyChunk 是 usage-only chunk：choices 空数组 + usage 非空对象
// （上游响应 stream_options.include_usage 的末尾计量帧形态）。
const chatUsageOnlyChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n"

// usageInjectTestRequest 构造带解析 body 的客户端请求视图（映射网关 ingress
// 扫描后的形态；clientBody 传空串表示 Body 未解析/缺席）。
func usageInjectTestRequest(t *testing.T, clientBody string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if clientBody == "" {
		return req
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(clientBody), &parsed); err != nil {
		t.Fatalf("client body parse: %v", err)
	}
	req.Body = &gatewaybody.Request{RawBody: []byte(clientBody), Body: parsed}
	return req
}

func openaiInjectAccount() AccountView {
	return OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
		ID:           "acc_usage_inject",
		ProviderCode: "openai",
		ProtocolCode: "openai",
	}}
}

// injectedUpstreamBody 是网关注入后的出站 body 形态（chain_driver splice 产物
// 的语义等价形态）。
const injectedUpstreamBody = `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":true},"messages":[]}`

func TestGatewayInjectedChatStreamUsageDerivation(t *testing.T) {
	cases := []struct {
		name        string
		clientBody  string // 空 = 客户端 body 未解析
		requestBody string
		protocol    string
		want        bool
	}{
		{"网关注入形态", "", injectedUpstreamBody, "openai", true},
		{"客户端未解析且注入", "", `{"model":"gpt-x","stream":true,"messages":[],"stream_options":{"include_usage":true}}`, "openai", true},
		{"客户端显式要求不剥除", `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":true}}`, injectedUpstreamBody, "openai", false},
		{"出站未带 stream_options", "", `{"model":"gpt-x","stream":true,"messages":[]}`, "openai", false},
		{"出站 stream_options 未开 usage", "", `{"model":"gpt-x","stream":true,"stream_options":{}}`, "openai", false},
		{"出站非流式", "", `{"model":"gpt-x","stream":false,"stream_options":{"include_usage":true}}`, "openai", false},
		{"非 openai 协议", "", injectedUpstreamBody, "anthropic", false},
		{"客户端显式 false 由网关改写", `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":false}}`, injectedUpstreamBody, "openai", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := usageInjectTestRequest(t, tc.clientBody)
			account := openaiInjectAccount()
			if tc.protocol != "openai" {
				account = OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
					ID: "acc_other", ProtocolCode: tc.protocol,
				}}
			}
			if got := gatewayInjectedChatStreamUsage(req, []byte(tc.requestBody), account); got != tc.want {
				t.Fatalf("gatewayInjectedChatStreamUsage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGatewayInjectedChatStreamUsageGuards(t *testing.T) {
	// nil 请求 / 空 body / nil 账户恒 false。
	if gatewayInjectedChatStreamUsage(nil, []byte(injectedUpstreamBody), openaiInjectAccount()) {
		t.Fatal("nil req must not suppress")
	}
	req := usageInjectTestRequest(t, "")
	if gatewayInjectedChatStreamUsage(req, nil, openaiInjectAccount()) {
		t.Fatal("empty request body must not suppress")
	}
	if gatewayInjectedChatStreamUsage(req, []byte(injectedUpstreamBody), nil) {
		t.Fatal("nil account must not suppress")
	}
	// body 含 stream_options 片段但整体非 JSON（防御）：不剥除。
	if gatewayInjectedChatStreamUsage(req, []byte(`not-json "stream_options"`), openaiInjectAccount()) {
		t.Fatal("invalid json body must not suppress")
	}
}

func TestFilterGatewayInjectedUsageOnlySseFrames(t *testing.T) {
	delta := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	cases := []struct {
		name    string
		chunk   string
		want    string
		dropped bool
	}{
		{"整片 usage-only 帧", chatUsageOnlyChunk, "", true},
		{"普通 delta 帧快路径", delta, "", false},
		{"delta 与 usage-only 混合分片", delta + chatUsageOnlyChunk, delta, true},
		{"usage 附着于 choices 非空帧保留", chatFinishChunk, "", false},
		{"无 choices 键的 usage 帧保留", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3}}}\n\n", "", false},
		{"空 usage 对象保留", "data: {\"choices\":[],\"usage\":{}}\n\n", "", false},
		{"DONE 帧保留", chatDoneChunk, "", false},
		{"尾部未完帧保留", `data: {"choices":[],"usage":{"prompt_to`, "", false},
		{"内容字符串含 usage 字样保留", "data: {\"choices\":[{\"delta\":{\"content\":\"say \\\"usage\\\" here\"}}]}\n\n", "", false},
		{"CRLF 边界 usage-only 帧剥除", strings.ReplaceAll(chatUsageOnlyChunk, "\n\n", "\r\n\r\n"), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filtered, dropped := filterGatewayInjectedUsageOnlySseFrames([]byte(tc.chunk))
			if dropped != tc.dropped {
				t.Fatalf("dropped = %v, want %v", dropped, tc.dropped)
			}
			if tc.dropped && string(filtered) != tc.want {
				t.Fatalf("filtered = %q, want %q", filtered, tc.want)
			}
			if !tc.dropped && filtered != nil {
				t.Fatalf("未剥除时 filtered 必须为 nil（零拷贝快路径），got %q", filtered)
			}
		})
	}
}

// capturingWriterRef 收集写下游字节（stream_test.go capturingWriter 的互斥
// 复用版本，避免与本文件外测试共享状态）。
type capturingWriterRef struct {
	inner   gatewaypreauth.GatewayResponseWriter
	mu      sync.Mutex
	written []byte
}

func (w *capturingWriterRef) Header() http.Header    { return w.inner.Header() }
func (w *capturingWriterRef) WriteHeader(status int) { w.inner.WriteHeader(status) }
func (w *capturingWriterRef) HeadersSent() bool      { return w.inner.HeadersSent() }
func (w *capturingWriterRef) StatusCode() int        { return w.inner.StatusCode() }
func (w *capturingWriterRef) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.written = append(w.written, data...)
	w.mu.Unlock()
	return w.inner.Write(data)
}

func (w *capturingWriterRef) bytesWritten() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.written...)
}

func runUsageInjectPipe(t *testing.T, chunks []string, suppress bool) (StreamPipeResult, []byte) {
	t.Helper()
	recorder := &failureRecorder{}
	downstream := &capturingWriterRef{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	options := StreamPipeOptions{
		SuppressGatewayInjectedUsageChunk: suppress,
		NowMs:                             func() int64 { return 1000 },
	}
	body := NewSliceUpstreamBody(makeChunkBytes(chunks)...)
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream:   StreamDownstream{Res: downstream},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             options,
	})
	if err != nil {
		t.Fatalf("pipe err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("unexpected failure records = %+v", recorder.values)
	}
	return result, downstream.bytesWritten()
}

func makeChunkBytes(chunks []string) [][]byte {
	out := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, []byte(chunk))
	}
	return out
}

func usageValues(result StreamPipeResult) (int, int) {
	input, output := -1, -1
	if result.Usage.InputTokens != nil {
		input = *result.Usage.InputTokens
	}
	if result.Usage.OutputTokens != nil {
		output = *result.Usage.OutputTokens
	}
	return input, output
}

// TestPipeUpstreamStreamInjectedUsageChunkConsumption 钉住修复 A 响应侧主契约：
// 网关注入场景（suppress=true）客户端收不到 usage-only chunk 但其余 chunk 与
// [DONE] 完整，且 usage 计量提取照常；客户端显式要求场景（suppress=false）
// chunk 照旧透传；两场景计量提取结果一致（记账回归）。
func TestPipeUpstreamStreamInjectedUsageChunkConsumption(t *testing.T) {
	streamChunks := []string{chatDeltaChunk, chatUsageOnlyChunk, chatDoneChunk}

	suppressedResult, suppressedWritten := runUsageInjectPipe(t, streamChunks, true)
	wantSuppressed := chatDeltaChunk + chatDoneChunk
	if string(suppressedWritten) != wantSuppressed {
		t.Fatalf("注入场景下游字节 = %q, want %q（usage-only chunk 不下发、[DONE] 完整）", suppressedWritten, wantSuppressed)
	}
	suppressedInput, suppressedOutput := usageValues(suppressedResult)
	if suppressedInput != 5 || suppressedOutput != 7 {
		t.Fatalf("注入场景 usage = (%d, %d), want (5, 7)（剥除不得影响计量提取）", suppressedInput, suppressedOutput)
	}

	explicitResult, explicitWritten := runUsageInjectPipe(t, streamChunks, false)
	wantExplicit := chatDeltaChunk + chatUsageOnlyChunk + chatDoneChunk
	if string(explicitWritten) != wantExplicit {
		t.Fatalf("显式场景下游字节 = %q, want %q（chunk 照旧透传）", explicitWritten, wantExplicit)
	}
	explicitInput, explicitOutput := usageValues(explicitResult)
	if explicitInput != suppressedInput || explicitOutput != suppressedOutput {
		t.Fatalf("显式场景 usage = (%d, %d)，与注入场景 (%d, %d) 不一致（记账回归）",
			explicitInput, explicitOutput, suppressedInput, suppressedOutput)
	}
	if !explicitResult.ProtocolValidated || !suppressedResult.ProtocolValidated {
		t.Fatalf("两场景都应协议校验通过：explicit=%v suppressed=%v",
			explicitResult.ProtocolValidated, suppressedResult.ProtocolValidated)
	}
}

// TestPipeUpstreamStreamMixedUsageOnlyChunkWithDone 钉住混合分片语义：
// usage-only 帧与 [DONE] 同片到达时只剥 usage 帧，[DONE] 照常下发。
func TestPipeUpstreamStreamMixedUsageOnlyChunkWithDone(t *testing.T) {
	result, written := runUsageInjectPipe(t, []string{chatDeltaChunk, chatUsageOnlyChunk + chatDoneChunk}, true)
	if string(written) != chatDeltaChunk+chatDoneChunk {
		t.Fatalf("下游字节 = %q, want %q", written, chatDeltaChunk+chatDoneChunk)
	}
	input, output := usageValues(result)
	if input != 5 || output != 7 {
		t.Fatalf("usage = (%d, %d), want (5, 7)", input, output)
	}
}

// TestHandleStreamUpstreamResponseInjectedUsageWiring 钉住装配面推导：
// HandleUpstreamResponseInput.RequestBody（本次 attempt 实际发送体）带注入而
// 客户端 body 未要求 → usage chunk 不下发；客户端 body 显式要求 → 透传。
func TestHandleStreamUpstreamResponseInjectedUsageWiring(t *testing.T) {
	upstreamChunks := [][]byte{[]byte(chatDeltaChunk), []byte(chatUsageOnlyChunk), []byte(chatDoneChunk)}

	// 网关注入场景：客户端 body 未解析（ingress scanned 形态），发送体带注入。
	input, recorder := newInputFixture(NewSliceUpstreamBody(upstreamChunks...), 200, nil)
	input.RequestBody = []byte(injectedUpstreamBody)
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"choices":[]`) || !strings.Contains(body, `[DONE]`) {
		t.Fatalf("注入场景下游响应 = %q（usage-only 不得下发，[DONE] 完整）", body)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 5 {
		t.Fatalf("usage = %+v（计量照常）", result.Usage)
	}

	// 客户端显式要求场景：chunk 照旧透传。
	explicitInput, explicitRecorder := newInputFixture(NewSliceUpstreamBody(upstreamChunks...), 200, nil)
	explicitInput.RequestBody = []byte(injectedUpstreamBody)
	explicitReq := usageInjectTestRequest(t, `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":true}}`)
	explicitInput.Req = explicitReq
	explicitInput.Deps = input.Deps
	explicitResult, err := HandleStreamUpstreamResponse(explicitInput)
	if err != nil {
		t.Fatalf("explicit err = %v", err)
	}
	if !strings.Contains(explicitRecorder.Body.String(), `"choices":[]`) {
		t.Fatalf("显式场景下游响应 = %q（usage chunk 应透传）", explicitRecorder.Body.String())
	}
	if explicitResult.Usage.InputTokens == nil || *explicitResult.Usage.InputTokens != 5 {
		t.Fatalf("explicit usage = %+v", explicitResult.Usage)
	}

	// 发送体未注入（客户端零 stream_options 直通流）：零剥除。
	passthroughInput, passthroughRecorder := newInputFixture(NewSliceUpstreamBody(upstreamChunks...), 200, nil)
	passthroughInput.RequestBody = []byte(`{"model":"gpt-x","stream":true,"messages":[]}`)
	passthroughInput.Deps = input.Deps
	if _, err := HandleStreamUpstreamResponse(passthroughInput); err != nil {
		t.Fatalf("passthrough err = %v", err)
	}
	if !strings.Contains(passthroughRecorder.Body.String(), `"choices":[]`) {
		t.Fatalf("未注入流下游响应 = %q（usage chunk 应照旧透传）", passthroughRecorder.Body.String())
	}
}
