package main

// 向量/重排协议补齐批（《对外协议面对齐 new-api 设计》§4.4/§4.5）：
// 链级 URL 构造、请求报文转换与响应归一的钉值测试。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrerank"
)

func newRerankTestRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	return newRerankTestRequestWithPath(t, "/v1/rerank", body)
}

func newRerankTestRequestWithPath(t *testing.T, path, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

func TestChainRerankPathDetection(t *testing.T) {
	cases := []struct {
		pathAndQuery string
		want         bool
	}{
		{"/v1/rerank", true},
		{"/rerank", true},
		{"/v1/rerank?foo=bar", true},
		{"/v1/rerank/", false},
		{"/v1/embeddings", false},
		{"/v1/chat/completions", false},
	}
	for _, tc := range cases {
		if got := gatewayrerank.IsRequestPath(tc.pathAndQuery); got != tc.want {
			t.Fatalf("rerank path %q = %v, want %v", tc.pathAndQuery, got, tc.want)
		}
	}
}

func TestChainQwenRerankUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, want string
	}{
		{
			name: "origin base",
			base: "https://dashscope.aliyuncs.com",
			want: "https://dashscope.aliyuncs.com" + qwenRerankUpstreamPath,
		},
		{
			name: "compatible-mode base stripped",
			base: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			want: "https://dashscope.aliyuncs.com" + qwenRerankUpstreamPath,
		},
		{
			name: "api v1 base stripped",
			base: "https://dashscope.aliyuncs.com/api/v1/",
			want: "https://dashscope.aliyuncs.com" + qwenRerankUpstreamPath,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainQwenRerankUpstreamURL(tc.base)
			if err != nil {
				t.Fatalf("chainQwenRerankUpstreamURL(%q): %v", tc.base, err)
			}
			if got != tc.want {
				t.Fatalf("url = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := chainQwenRerankUpstreamURL("not-a-url"); err == nil {
		t.Fatal("invalid base url must fail")
	}
}

func TestChainRerankUpstreamURLGeneric(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{VendorCode: "cohere", BaseURL: "https://api.cohere.com/v1"}
	req := newRerankTestRequest(t, `{"model":"rerank-v3","query":"q","documents":["a","b"]}`)
	got, err := chainRerankUpstreamURL(account, req)
	if err != nil {
		t.Fatalf("chainRerankUpstreamURL: %v", err)
	}
	if got != "https://api.cohere.com/v1/rerank" {
		t.Fatalf("url = %q", got)
	}
}

func TestChainRerankUpstreamURLCanonicalizesPathCase(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{VendorCode: "cohere", BaseURL: "https://api.cohere.com/v1"}
	req := newRerankTestRequestWithPath(t, "/V1/RERANK?trace=1", `{"model":"rerank-v3","query":"q","documents":["a"]}`)
	got, err := chainRerankUpstreamURL(account, req)
	if err != nil {
		t.Fatalf("chainRerankUpstreamURL: %v", err)
	}
	if got != "https://api.cohere.com/v1/rerank?trace=1" {
		t.Fatalf("url = %q", got)
	}
}

func TestChainRerankRequestPartsQwen(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		VendorCode:   "qwen",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://dashscope.aliyuncs.com",
		APIKey:       "sk-test",
	}
	body := `{"model":"gte-rerank-v2","query":"查询","documents":[{"text":"文档一"},{"text":"文档二"}],"top_n":1,"return_documents":true}`
	req := newRerankTestRequest(t, body)
	parts, ok, err := driver.chainRerankRequestParts(req, account)
	if err != nil || !ok {
		t.Fatalf("request parts ok=%v err=%v", ok, err)
	}
	if got := parts.Headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("authorization = %q", got)
	}
	if got := parts.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	var payload struct {
		Model string `json:"model"`
		Input struct {
			Query     string `json:"query"`
			Documents []any  `json:"documents"`
		} `json:"input"`
		Parameters struct {
			TopN            *int  `json:"top_n"`
			ReturnDocuments *bool `json:"return_documents"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(parts.Body, &payload); err != nil {
		t.Fatalf("decode dashscope body: %v (%s)", err, string(parts.Body))
	}
	if payload.Model != "gte-rerank-v2" || payload.Input.Query != "查询" || len(payload.Input.Documents) != 2 {
		t.Fatalf("dashscope payload = %+v", payload)
	}
	if payload.Parameters.TopN == nil || *payload.Parameters.TopN != 1 {
		t.Fatalf("top_n = %v", payload.Parameters.TopN)
	}
	if payload.Parameters.ReturnDocuments == nil || !*payload.Parameters.ReturnDocuments {
		t.Fatalf("return_documents = %v", payload.Parameters.ReturnDocuments)
	}
	var rawPayload map[string]any
	if err := json.Unmarshal(parts.Body, &rawPayload); err != nil {
		t.Fatalf("decode raw dashscope payload: %v", err)
	}
	rawDocuments, ok := rawPayload["input"].(map[string]any)["documents"].([]any)
	if !ok || len(rawDocuments) != 2 || rawDocuments[0] != "文档一" || rawDocuments[1] != "文档二" {
		t.Fatalf("dashscope documents must be text array: %v", rawPayload["input"])
	}
}

func TestChainRerankRequestPartsGenericPassThrough(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		VendorCode:   "jina",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.jina.ai/v1",
		APIKey:       "jina-key",
	}
	body := `{"model":"jina-reranker-v2","query":"q","documents":["a","b"],"top_n":2}`
	req := newRerankTestRequest(t, body)
	parts, ok, err := driver.chainRerankRequestParts(req, account)
	if err != nil || !ok {
		t.Fatalf("request parts ok=%v err=%v", ok, err)
	}
	var gotBody map[string]any
	if err := json.Unmarshal(parts.Body, &gotBody); err != nil {
		t.Fatalf("decode generic body: %v", err)
	}
	if gotBody["model"] != "jina-reranker-v2" || gotBody["query"] != "q" || gotBody["top_n"] != float64(2) {
		t.Fatalf("generic body semantics changed: %s", string(parts.Body))
	}
	if docs, ok := gotBody["documents"].([]any); !ok || len(docs) != 2 {
		t.Fatalf("generic documents = %v", gotBody["documents"])
	}
}

func TestChainRerankRequestPartsValidation(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{VendorCode: "qwen", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://dashscope.aliyuncs.com", APIKey: "sk-test"}
	req := newRerankTestRequest(t, `{"model":"gte-rerank-v2","documents":["a"]}`)
	_, ok, err := driver.chainRerankRequestParts(req, account)
	if !ok || err == nil {
		t.Fatalf("missing query must fail: ok=%v err=%v", ok, err)
	}
}

func TestChainRerankNormalizeResponse(t *testing.T) {
	req := newRerankTestRequest(t, `{"model":"gte-rerank-v2","query":"q","documents":["a","b"],"top_n":1,"return_documents":false}`)
	upstreamBody := `{"output":{"results":[{"index":0,"relevance_score":0.9,"document":{"text":"a"}},{"index":1,"relevance_score":0.2,"document":{"text":"b"}}]},"usage":{"total_tokens":9}}`
	response := gatewaydispatch.NewGatewayUpstreamResponseForTransform(
		http.StatusOK,
		http.Header{"Content-Type": []string{"application/json"}},
		io.NopCloser(strings.NewReader(upstreamBody)),
	)
	got, err := chainRerankNormalizeResponse(req, response)
	if err != nil {
		t.Fatalf("normalize response: %v", err)
	}
	raw, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read normalized body: %v", err)
	}
	var payload struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Document       any     `json:"document"`
		} `json:"results"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode normalized body: %v (%s)", err, string(raw))
	}
	if len(payload.Results) != 1 || payload.Results[0].Index != 0 {
		t.Fatalf("top_n must trim results: %+v", payload.Results)
	}
	if payload.Results[0].Document != nil {
		t.Fatalf("return_documents=false must drop document: %+v", payload.Results[0].Document)
	}
	if payload.Usage["prompt_tokens"] != 9 || payload.Usage["total_tokens"] != 9 {
		t.Fatalf("usage = %v", payload.Usage)
	}
	if got.Status() != http.StatusOK {
		t.Fatalf("status = %d", got.Status())
	}
}

func TestChainRerankNormalizeResponseOverProtocolWindow(t *testing.T) {
	req := newRerankTestRequest(t, `{"model":"gte-rerank-v2","query":"q","documents":["a"],"return_documents":true}`)
	// 构造超过 1MiB 的合法响应：归一化阶段必须有界读取并显式拒绝，不能先把
	// 完整响应无界读入内存再交给后续协议校验。
	document := strings.Repeat("a", maxRerankResponseBytes)
	upstreamBody := `{"results":[{"index":0,"relevance_score":0.9,"document":{"text":"` + document + `"}}],"usage":{"total_tokens":9}}`
	response := gatewaydispatch.NewGatewayUpstreamResponseForTransform(
		http.StatusOK,
		http.Header{"Content-Type": []string{"application/json"}},
		io.NopCloser(strings.NewReader(upstreamBody)),
	)
	if _, err := chainRerankNormalizeResponse(req, response); err == nil {
		t.Fatal("超过协议窗口的重排响应必须返回错误")
	}
}

func TestChainRerankNormalizeResponseNilHeader(t *testing.T) {
	req := newRerankTestRequest(t, `{"model":"gte-rerank-v2","query":"q","documents":["a"]}`)
	response := gatewaydispatch.NewGatewayUpstreamResponseForTransform(
		http.StatusOK,
		nil,
		io.NopCloser(strings.NewReader(`{"results":[{"index":0,"relevance_score":0.9}],"usage":{"total_tokens":9}}`)),
	)
	got, err := chainRerankNormalizeResponse(req, response)
	if err != nil {
		t.Fatalf("normalize response with nil header: %v", err)
	}
	if got.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", got.Header.Get("Content-Type"))
	}
}
