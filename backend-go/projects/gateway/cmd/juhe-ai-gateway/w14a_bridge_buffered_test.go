package main

// w14a（单元层）：2026-11-05 协议面收窄后，跨协议映射只保留 OpenAI v1
// Responses -> Chat Completions（流式）；本文件只保留仍然可达的非桥守卫、
// 同族 mapping 透传与 Code Assist 解包/读错误臂。已退役的 chat↔anthropic/
// gemini 缓冲转换用例删除，不再以测试固化旧行为。

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

// w14aSameFamilyAccount 构造合法同族改名 mapping（chat -> chat）；同族映射
// 不触发 bridge，只验证响应透传与 mapping 解析守卫。
func w14aSameFamilyAccount(sourceModel, upstreamModel string) gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:           "w14a-same-family-acc",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            sourceModel,
			SourceEndpointFamily:   "chat_completions",
			UpstreamModel:          upstreamModel,
			UpstreamEndpointFamily: "chat_completions",
			Enabled:                true,
		}},
	}
}

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

// TestW14aBridgeGuardsAndPassthrough 钉住入口守卫：非 OK 响应、无 mapping、
// 请求无 model、mapping 不命中、同族 mapping 时都保持逐字节透传。
func TestW14aBridgeGuardsAndPassthrough(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	payload := `{"note":"verbatim"}`

	// 非 OK 上游响应原样透传（Node !response.ok 守卫）。
	notOK := gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusBadGateway,
		newBridgeUpstreamResponse("application/json", nil).Header, io.NopCloser(strings.NewReader(payload)))
	notOKInput := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`,
		w14aSameFamilyAccount("client-model", "upstream-model"), notOK)
	got, err := transformer.TransformUpstreamResponseForAccount(notOKInput)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("非 OK 响应必须逐字节透传, got %s", text)
	}

	// 无 mapping（请求缺 model 字段）→ chainBridgeResponseMappingOf 返回 nil。
	noModel := w14aBridgeInput(t, "/v1/chat/completions", `{"messages":[]}`,
		gatewaydispatch.AccountCandidate{ID: "w14a-plain", VendorCode: "gpt", ProtocolCode: "openai"},
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(noModel)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("缺 model 的请求必须透传, got %s", text)
	}

	// model 存在但 mapping 行不命中 → resolver 返回 nil → 透传。
	unmatched := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"other-model","messages":[]}`,
		w14aSameFamilyAccount("client-model", "upstream-model"),
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(unmatched)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("mapping 不命中必须透传, got %s", text)
	}

	// 同族 mapping（chat -> chat）不做桥转换。
	sameFamily := w14aBridgeInput(t, "/v1/chat/completions", `{"model":"client-model","messages":[]}`,
		w14aSameFamilyAccount("client-model", "upstream-model"),
		newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))
	got, err = transformer.TransformUpstreamResponseForAccount(sameFamily)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("同族 mapping 必须透传, got %s", text)
	}
}

// TestW14aRetiredCrossProtocolMappingDoesNotBridge 钉住矩阵外映射的运行时
// 拒绝：messages -> chat_completions 已随 2026-11-05 协议面收窄退役，即使
// 运行时候选里残留该映射行，响应侧也不得执行旧跨协议转换。
func TestW14aRetiredCrossProtocolMappingDoesNotBridge(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	payload := `{"id":"chatcmpl-old","choices":[{"index":0,"message":{"role":"assistant","content":"raw"}}]}`
	account := gatewaydispatch.AccountCandidate{
		ID:         "w14a-retired-mapping",
		VendorCode: "gpt",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   "messages",
			UpstreamModel:          "upstream-model",
			UpstreamEndpointFamily: "chat_completions",
			Enabled:                true,
		}},
	}
	input := w14aBridgeInput(t, "/v1/messages", `{"model":"client-model","max_tokens":16,"messages":[]}`,
		account, newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader(payload))))

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if text := w14aReadAll(t, got.Body); text != payload {
		t.Fatalf("矩阵外 mapping 不得执行旧跨协议 bridge，应逐字节透传, got %s", text)
	}
}

// TestW14aBridgeReadErrorPropagates 覆盖 Code Assist 非流式收集的 ReadAll
// 错误传播（与已退役跨协议缓冲桥无关）。
func TestW14aBridgeReadErrorPropagates(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	readErr := errors.New("w14a read failure")
	input := w14aBridgeInput(t, "/v1beta/models/gem-1:generateContent", `{"contents":[]}`,
		codeAssistAccount(), newBridgeUpstreamResponse("text/event-stream", io.NopCloser(errReader{err: readErr})))
	if _, err := transformer.TransformUpstreamResponseForAccount(input); !errors.Is(err, readErr) {
		t.Fatalf("Code Assist 收集读取错误必须原样传播, got %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read(p []byte) (int, error) { return 0, r.err }
func (r errReader) Close() error               { return nil }

// TestW14aBridgeCodeAssistWithSameFamilyMapping 覆盖带同族 mapping 的 Code
// Assist 账户：解包走 mapping!=nil 的流式判定臂与 JSON Content-Type 收集臂。
func TestW14aBridgeCodeAssistWithSameFamilyMapping(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	sse := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"collected"}]}}]}}` + "\n\n"
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
