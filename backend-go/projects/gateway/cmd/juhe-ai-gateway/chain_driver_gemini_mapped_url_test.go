package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// mappedURLTestRequest 构造带 JSON body 捕获态的 GatewayRequest；多个 driver
// 测试文件共用（保留在 driver 测试文件而非具体 case 文件）。
func mappedURLTestRequest(t *testing.T, method, target, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

// TestChainDriverGeminiNativePathUnmappedKeepsRouteHelper pins the unmapped
// regression: without a mapping the native gemini path keeps the route-helper
// URL construction.
func TestChainDriverGeminiNativePathUnmappedKeepsRouteHelper(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		// 动机（v2 直拼契约）：base_url 即版本根（自带 /v1beta）。
		ID: "acc_gem_native", VendorCode: "google", ProtocolCode: "gemini",
		Type: "api_key", BaseURL: "https://gem.example/v1beta", APIKey: "gk-1",
	}
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gem-1:generateContent",
		strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) == 0 || !strings.Contains(urls[0], "gem.example") || !strings.Contains(urls[0], "gem-1:generateContent") {
		t.Fatalf("native unmapped url = %#v", urls)
	}
}
