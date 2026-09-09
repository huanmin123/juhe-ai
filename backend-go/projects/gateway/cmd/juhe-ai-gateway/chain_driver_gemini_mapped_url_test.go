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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestChainDriverGeminiModelMappedUpstreamURL pins the B-4 model-mapped URL
// rewrite (Node gemini/driver.ts:126-128 buildUpstreamUrls +
// geminiGenerateContentModelMappedUpstreamPathAndQuery): a cross-protocol
// request whose mapping targets gemini must not enter the native-route
// helper (which rejects non-native client paths); the upstream URL becomes
// /v1beta/models/<upstreamModel>:<action>[?alt=sse] on the account base.

func mappedURLTestRequest(t *testing.T, method, target, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

func TestChainDriverGeminiModelMappedUpstreamURL(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_gem_map",
		ProviderCode: "hybrid",
		ProtocolCode: "gemini",
		Type:         "api_key",
		BaseURL:      "https://gem.example",
		APIKey:       "gk-1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-x",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "gem-2",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyGenerateContent,
			Enabled:                true,
		}},
	}

	// 非流式：客户端 responses 路径 + responses→gemini mapping。
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-x","input":"hi"}`)
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://gem.example/v1beta/models/gem-2:generateContent" {
		t.Fatalf("non-stream mapped url = %#v", urls)
	}

	// 流式。
	req = mappedURLTestRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-x","input":"hi","stream":true}`)
	urls, err = driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build stream urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://gem.example/v1beta/models/gem-2:streamGenerateContent?alt=sse" {
		t.Fatalf("stream mapped url = %#v", urls)
	}

	// models/ 前缀剥除（Node geminiModelPathSegment）。
	account.ModelMappings[0].UpstreamModel = "models/gem-3"
	req = mappedURLTestRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-x","input":"hi"}`)
	urls, err = driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build prefixed urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://gem.example/v1beta/models/gem-3:generateContent" {
		t.Fatalf("prefixed mapped url = %#v", urls)
	}
}

// TestChainDriverGeminiNativePathUnmappedKeepsRouteHelper pins the unmapped
// regression: without a mapping the native gemini path keeps the route-helper
// URL construction.
func TestChainDriverGeminiNativePathUnmappedKeepsRouteHelper(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_gem_native", ProviderCode: "google", ProtocolCode: "gemini",
		Type: "api_key", BaseURL: "https://gem.example", APIKey: "gk-1",
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
