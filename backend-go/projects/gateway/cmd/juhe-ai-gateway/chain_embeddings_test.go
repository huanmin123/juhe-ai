package main

// 向量协议补齐批（《对外协议面对齐 new-api 设计》§4.5）：链级路径判定、
// qwen 兼容模式 URL 与请求报文透传的钉值测试。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func newEmbeddingsTestRequest(t *testing.T, path, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	}
	return req
}

func TestChainIsEmbeddingsRequestPath(t *testing.T) {
	cases := []struct {
		pathAndQuery string
		want         bool
	}{
		{"/v1/embeddings", true},
		{"/embeddings", true},
		{"/v1/embeddings?foo=bar", true},
		{"/V1/Embeddings", true},
		{"/v1/embeddings/multimodal", false},
		{"/v1/rerank", false},
		{"/v1/chat/completions", false},
	}
	for _, tc := range cases {
		if got := chainIsEmbeddingsRequestPath(tc.pathAndQuery); got != tc.want {
			t.Fatalf("embeddings path %q = %v, want %v", tc.pathAndQuery, got, tc.want)
		}
	}
}

func TestChainQwenEmbeddingsUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, want string
	}{
		{
			name: "origin base",
			base: "https://dashscope.aliyuncs.com",
			want: "https://dashscope.aliyuncs.com" + qwenEmbeddingsUpstreamPath,
		},
		{
			name: "compatible-mode base stripped",
			base: "https://dashscope.aliyuncs.com/compatible-mode/v1/",
			want: "https://dashscope.aliyuncs.com" + qwenEmbeddingsUpstreamPath,
		},
		{
			name: "api v1 base stripped",
			base: "https://dashscope.aliyuncs.com/api/v1",
			want: "https://dashscope.aliyuncs.com" + qwenEmbeddingsUpstreamPath,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainQwenEmbeddingsUpstreamURL(tc.base)
			if err != nil {
				t.Fatalf("chainQwenEmbeddingsUpstreamURL(%q): %v", tc.base, err)
			}
			if got != tc.want {
				t.Fatalf("url = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := chainQwenEmbeddingsUpstreamURL("not-a-url"); err == nil {
		t.Fatal("invalid base url must fail")
	}
}

func TestChainEmbeddingsUpstreamURLGeneric(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{VendorCode: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3"}
	req := newEmbeddingsTestRequest(t, "/v1/embeddings", `{"model":"doubao-embedding","input":"hi"}`)
	got, err := chainEmbeddingsUpstreamURL(account, req)
	if err != nil {
		t.Fatalf("chainEmbeddingsUpstreamURL: %v", err)
	}
	if got != "https://ark.cn-beijing.volces.com/api/v3/embeddings" {
		t.Fatalf("url = %q", got)
	}
}

func TestChainEmbeddingsUpstreamURLCanonicalizesPathCase(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{VendorCode: "openai", BaseURL: "https://api.openai.com/v1"}
	req := newEmbeddingsTestRequest(t, "/V1/Embeddings?trace=1", `{"model":"text-embedding-3-small","input":"hi"}`)
	got, err := chainEmbeddingsUpstreamURL(account, req)
	if err != nil {
		t.Fatalf("chainEmbeddingsUpstreamURL: %v", err)
	}
	if got != "https://api.openai.com/v1/embeddings?trace=1" {
		t.Fatalf("url = %q", got)
	}
}

func TestChainEmbeddingsRequestPartsPassThrough(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		VendorCode:   "openai",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.openai.com/v1",
		APIKey:       "sk-test",
	}
	body := `{"model":"text-embedding-3-small","input":["a","b"],"encoding_format":"float"}`
	req := newEmbeddingsTestRequest(t, "/v1/embeddings", body)
	parts, ok, err := driver.chainEmbeddingsRequestParts(req, account)
	if err != nil || !ok {
		t.Fatalf("request parts ok=%v err=%v", ok, err)
	}
	if string(parts.Body) != body {
		t.Fatalf("embeddings body must pass through unchanged: %s", string(parts.Body))
	}
	if got := parts.Headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestChainEmbeddingsRequestPartsEmptyBody(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{VendorCode: "openai", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"}
	req := newEmbeddingsTestRequest(t, "/v1/embeddings", "")
	_, ok, err := driver.chainEmbeddingsRequestParts(req, account)
	if !ok || err == nil {
		t.Fatalf("empty embeddings body must fail: ok=%v err=%v", ok, err)
	}
}

func TestChainEmbeddingsMultimodalNotIntercepted(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{VendorCode: "volcengine", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", APIKey: "vk"}
	req := newEmbeddingsTestRequest(t, "/v1/embeddings/multimodal", `{"model":"doubao-embedding-vision-251215","input":[]}`)
	_, ok, err := driver.chainEmbeddingsRequestParts(req, account)
	if err != nil || ok {
		t.Fatalf("multimodal subpath must stay on passthrough: ok=%v err=%v", ok, err)
	}
}

func TestChainProtocolRequestMethodPathAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/embeddings", true},
		{http.MethodPost, "/embeddings", true},
		{http.MethodPost, "/v1/embeddings/multimodal", true},
		{http.MethodPost, "/v1/rerank", true},
		{http.MethodPost, "/rerank", true},
		{http.MethodGet, "/v1/embeddings", false},
		{http.MethodGet, "/v1/rerank", false},
		{http.MethodPost, "/v1/embeddings/other", false},
		{http.MethodPost, "/v1/rerank/other", true}, // 非协议门路径，交给链内 404/兼容面
		{http.MethodPost, "/v1/chat/completions", true},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		req := gatewaypreauth.NewGatewayRequest(request)
		if got := chainProtocolRequestMethodPathAllowed(req); got != tc.want {
			t.Fatalf("%s %s allowed = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
