package main

// w14a（单元层）：chain_bridge_response.go 缓冲（非流式）臂、passthrough 守卫、
// mapping 解析助手与 Code Assist 非流式分支的覆盖收敛。流式增量管道见
// w14a_bridge_pumps_test.go；既有流式语义钉子见 chain_bridge_response_stream_test.go。
//
// 本文件登记的进程内不可达语句（chain_bridge_response.go）：
//   - 111-113（transformed == nil 回落透传）：可达该 switch 的
//     (sourceFamily, upstreamFamily) 组合都由 IsCrossProtocolBridgeRequired
//     白名单约束，且对应 transformTo*Client 非流式分支对每个白名单组合都有
//     非 nil 返回（含错误体），transformed 恒非 nil。
//   - 114-116 与 transformToChatClient/transformToAnthropicClient/
//     transformToGeminiNativeClient 内各 `if stream` 缓冲回退臂（284-298、
//     307-318、340-341、360-371、398-409、433-444、455-456）：stream=true 时
//     bridgeStreamPump 对同一批白名单组合恒返回非 nil pump 并提前 return
//     （89-93），三个缓冲 helper 只在 stream=false 时被调用。

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w14aBridgeMappedAccount 构造带单条跨协议 mapping 的 hybrid 账户。mapping
// 行使用存储词汇（chat_completions/responses/messages/generate_content/
// stream_generate_content）；RuntimeSource/RuntimeRouteRuleID 保持 nil 以
// 覆盖 chainBridgeResponseMappingOf 的 nil 指针回落臂。
func w14aBridgeMappedAccount(sourceModel, sourceFamily, upstreamModel, upstreamFamily string) gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:           "w14a-bridge-acc",
		ProviderCode: "hybrid",
		ProtocolCode: "gemini",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            sourceModel,
			SourceEndpointFamily:   sourceFamily,
			UpstreamModel:          upstreamModel,
			UpstreamEndpointFamily: upstreamFamily,
			Enabled:                true,
		}},
	}
}

// w14aBridgeInput 构造挂载了 JSON body 捕获态的桥转换输入（ParsedJSONObjectBody
// 依赖该挂载，对齐 chain_bridge_response_stream_test 的构造方式）。
func w14aBridgeInput(t *testing.T, target, body string, account gatewaydispatch.AccountCandidate, response *gatewaydispatch.GatewayUpstreamResponse) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	}
	return gatewaydispatch.UpstreamResponseTransformInput{Req: req, Account: account, Response: response}
}

func w14aReadAll(t *testing.T, body io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	return string(data)
}

const w14aChatUpstreamJSON = `{"id":"chatcmpl-w14a","model":"upstream-chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"bridge text"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

const w14aAnthropicUpstreamJSON = `{"id":"msg_w14a","type":"message","role":"assistant","model":"upstream-claude-model","content":[{"type":"text","text":"anthropic text"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":3}}`

const w14aGeminiUpstreamJSON = `{"candidates":[{"content":{"parts":[{"text":"gemini text"}]},"finishReason":"STOP"}],"modelVersion":"upstream-gemini-model"}`

// --- 缓冲臂：chat 上游 -> 各客户端协议 ---

func TestW14aBridgeChatUpstreamToAnthropicClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aChatUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"stop_reason"`) || !strings.Contains(text, "bridge text") {
		t.Fatalf("chat 上游缓冲臂必须产出 anthropic messages JSON（缺陷修复钉子）, got %s", text)
	}
	if strings.Contains(text, `"choices"`) {
		t.Fatalf("anthropic 客户端不得收到 chat choices 结构, got %s", text)
	}
}

func TestW14aBridgeChatUpstreamToAnthropicClientInvalidJSON(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(`<not-json>`)))
	input := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","max_tokens":16,"messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, "upstream_chat_completions_invalid_json") {
		t.Fatalf("不可解析 chat 上游必须回落 anthropic 错误体, got %s", text)
	}
}

func TestW14aBridgeChatUpstreamToGeminiClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "generate_content", "upstream-chat-model", "chat_completions")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aChatUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1beta/models/client-model:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"candidates"`) || !strings.Contains(text, "bridge text") {
		t.Fatalf("chat 上游必须转换为 Gemini GenerateContent JSON, got %s", text)
	}
}

func TestW14aBridgeChatUpstreamToGeminiClientInvalidJSON(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "generate_content", "upstream-chat-model", "chat_completions")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(`!!!`)))
	input := w14aBridgeInput(t, "/v1beta/models/client-model:generateContent", `{"contents":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, "无法解析为 Gemini GenerateContent") || !strings.Contains(text, `"finishReason":"STOP"`) {
		t.Fatalf("不可解析 chat 上游必须回落 Gemini 文本提示体, got %s", text)
	}
}

// 注：transformToChatClient 的 FamilyResponses 臂（chat 上游 -> responses
// 客户端，含 bridgeStreamPump 的同名流式臂）在本组合根不可达：
// IsCrossProtocolBridgeRequired 白名单不含 responses→chat_completions
// （gatewayopenai 把该方向按 OpenAI 协议原生映射处理，不走跨协议桥），带
// mapping 的响应永远到不了这两个臂，见文件头不可达清单。

// --- 缓冲臂：anthropic 上游 -> 各客户端协议 ---

func TestW14aBridgeAnthropicUpstreamToGeminiClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "generate_content", "upstream-claude-model", "messages")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aAnthropicUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1beta/models/client-model:generateContent", `{"contents":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"candidates"`) || !strings.Contains(text, "anthropic text") {
		t.Fatalf("anthropic 上游必须转换为 Gemini GenerateContent JSON, got %s", text)
	}
}

func TestW14aBridgeAnthropicUpstreamToGeminiClientInvalidJSON(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "generate_content", "upstream-claude-model", "messages")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(`[[[`)))
	input := w14aBridgeInput(t, "/v1beta/models/client-model:generateContent", `{"contents":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, "upstream_anthropic_messages_invalid_json") {
		t.Fatalf("不可解析 anthropic 上游必须回落 gemini 错误体, got %s", text)
	}
}

func TestW14aBridgeAnthropicUpstreamToChatClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-claude-model", "messages")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aAnthropicUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"choices"`) || !strings.Contains(text, "anthropic text") {
		t.Fatalf("anthropic 上游必须转换为 chat completions JSON, got %s", text)
	}
}

func TestW14aBridgeAnthropicUpstreamToChatClientInvalidJSON(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-claude-model", "messages")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(`{broken`)))
	input := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"type":"error"`) {
		t.Fatalf("不可解析 anthropic 上游必须回落 anthropic 形错误体, got %s", text)
	}
}

func TestW14aBridgeAnthropicUpstreamToResponsesClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "responses", "upstream-claude-model", "messages")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aAnthropicUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/responses", `{"model":"client-model","stream":false,"input":"hi"}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"type":"message"`) || !strings.Contains(text, "anthropic text") {
		t.Fatalf("anthropic 上游必须渲染为 Responses 输出项 JSON, got %s", text)
	}
}

// --- 缓冲臂：gemini 原生上游 -> 各客户端协议 ---

func TestW14aBridgeGeminiNativeUpstreamToChatClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-gemini-model", "generate_content")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aGeminiUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"choices"`) || !strings.Contains(text, "gemini text") {
		t.Fatalf("gemini 原生上游必须转换为 chat completions JSON, got %s", text)
	}
}

func TestW14aBridgeGeminiNativeUpstreamToChatClientInvalidJSON(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-gemini-model", "generate_content")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(`nope{`)))
	input := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"object":"chat.completion"`) {
		t.Fatalf("不可解析 gemini 上游按空载荷回落 chat JSON（非错误体）, got %s", text)
	}
}

func TestW14aBridgeGeminiNativeUpstreamToResponsesClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "responses", "upstream-gemini-model", "generate_content")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aGeminiUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/responses", `{"model":"client-model","stream":false,"input":"hi"}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"object":"response"`) || !strings.Contains(text, "output_text") {
		t.Fatalf("gemini 原生上游必须渲染为 Responses JSON, got %s", text)
	}
}

func TestW14aBridgeGeminiNativeUpstreamToMessagesClientBuffered(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-gemini-model", "generate_content")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(w14aGeminiUpstreamJSON)))
	input := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","max_tokens":16,"messages":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if !strings.Contains(text, `"content"`) || !strings.Contains(text, "gemini text") {
		t.Fatalf("gemini 原生上游必须渲染为 anthropic messages JSON, got %s", text)
	}
}

// --- 守卫与错误臂 ---

// TestW14aBridgeBufferedReadError 覆盖缓冲路径的 body 读取错误传播
// （TransformUpstreamResponseForAccount 与 Code Assist 收集两处 ReadAll）。
func TestW14aBridgeBufferedReadError(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	readErr := errors.New("w14a read failure")
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions")
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(errReader{err: readErr}))
	input := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","max_tokens":16,"messages":[]}`, account, response)

	if _, err := transformer.TransformUpstreamResponseForAccount(input); !errors.Is(err, readErr) {
		t.Fatalf("缓冲读取错误必须原样传播, got %v", err)
	}

	// Code Assist 非流式收集的 ReadAll 错误臂。
	codeAssistInput := w14aBridgeInput(t, "/v1beta/models/gem-1:generateContent", `{"contents":[]}`,
		codeAssistAccount(), newBridgeUpstreamResponse("text/event-stream", io.NopCloser(errReader{err: readErr})))
	if _, err := transformer.TransformUpstreamResponseForAccount(codeAssistInput); !errors.Is(err, readErr) {
		t.Fatalf("Code Assist 收集读取错误必须原样传播, got %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read(p []byte) (int, error) { return 0, r.err }
func (r errReader) Close() error               { return nil }

// TestW14aBridgeGuardsAndPassthrough 钉住入口守卫：非 OK 响应、无 mapping、
// 请求无 model、mapping 不命中时的透传语义。
func TestW14aBridgeGuardsAndPassthrough(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	payload := `{"note":"verbatim"}`

	// 非 OK 上游响应原样透传（Node !response.ok 守卫）。
	notOK := gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusBadGateway,
		newBridgeUpstreamResponse("application/json", nil).Header, io.NopCloser(strings.NewReader(payload)))
	notOKInput := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","messages":[]}`,
		w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions"), notOK)
	got, err := transformer.TransformUpstreamResponseForAccount(notOKInput)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("非 OK 响应必须逐字节透传, got %s", text)
	}

	// 无 mapping（请求缺 model 字段）→ chainBridgeResponseMappingOf 返回 nil，
	// 非 Code Assist 账户透传。
	noModel := w14aBridgeInput(t, "/v1/messages", `{"max_tokens":16,"messages":[]}`,
		gatewaydispatch.AccountCandidate{ID: "w14a-plain", ProviderCode: "openai", ProtocolCode: "openai"},
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(noModel)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("缺 model 的请求必须透传, got %s", text)
	}

	// model 存在但 mapping 行不命中 → resolver 返回 nil → 透传。
	unmatched := w14aBridgeInput(t, "/v1/messages", `{"model":"other-model","messages":[]}`,
		w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions"),
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(unmatched)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("mapping 不命中必须透传, got %s", text)
	}

	// 源族 == 上游族（非跨协议）→ 不做桥转换，仅保留 Code Assist 解包检查。
	sameFamily := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","messages":[]}`,
		w14aBridgeMappedAccount("client-model", "messages", "upstream-claude-model", "messages"),
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(sameFamily)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("同族 mapping 必须透传, got %s", text)
	}
}

// TestW14aBridgeCodeAssistWithSameFamilyMapping 覆盖带同族 mapping 的 Code
// Assist 账户：解包走 mapping!=nil 的流式判定臂与 JSON Content-Type 收集臂。
func TestW14aBridgeCodeAssistWithSameFamilyMapping(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	sse := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"collected"}]}}]}}` + "\n\n"
	// Content-Type 带 json：收集结果按原响应头返回（499 直返臂）。
	response := newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(sse)))
	account := codeAssistAccount()
	account.ModelMappings = []gatewayruntimecache.AccountModelMapping{{
		SourceModel:            "gem-1",
		SourceEndpointFamily:   "generate_content",
		UpstreamModel:          "gem-1",
		UpstreamEndpointFamily: "generate_content",
		Enabled:                true,
	}}
	input := w14aBridgeInput(t, "/v1beta/models/gem-1:generateContent", `{"contents":[]}`, account, response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	text := w14aReadAll(t, got.Body)
	if strings.Contains(text, `"response":`) || !strings.Contains(text, "collected") {
		t.Fatalf("Code Assist 解包必须去 {response:...} 包装, got %s", text)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "json") {
		t.Fatalf("json Content-Type 分支必须保留原头, got %q", ct)
	}
}
