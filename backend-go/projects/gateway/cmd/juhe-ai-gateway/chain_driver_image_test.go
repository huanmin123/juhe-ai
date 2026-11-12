package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// 2026-11-11 图像生成统一协议同步批：driver 级请求归一与 URL 分支。
func TestChainProviderDriverImageRequestParts(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("gpt 无 provider_options 保持字节透传", func(t *testing.T) {
		raw := `{"model":"gpt-image-2","prompt":"a cat","stream":true,"partial_images":1}`
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", raw, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_img_gpt", ProtocolCode: "openai", VendorCode: "gpt",
			Type: "api_key", APIKey: "sk-gpt", BaseURL: "https://api.openai.com/v1",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if string(parts.Body) != raw {
			t.Fatalf("gpt image body changed: %s", parts.Body)
		}
		if parts.Headers.Get("Authorization") != "Bearer sk-gpt" {
			t.Fatalf("authorization = %q", parts.Headers.Get("Authorization"))
		}
	})

	t.Run("xai 合并 provider_options 并忽略未文档化参数", func(t *testing.T) {
		raw := `{"model":"grok-imagine-image-2.0","prompt":"a cat","stream":true,"quality":"high","provider_options":{"xai":{"response_format":"url"}}}`
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", raw, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_img_xai", ProtocolCode: "openai", VendorCode: "xai",
			Type: "api_key", APIKey: "sk-xai", BaseURL: "https://api.x.ai/v1",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(parts.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, ok := body["stream"]; ok {
			t.Fatalf("stream must be ignored: %s", parts.Body)
		}
		if _, ok := body["quality"]; ok {
			t.Fatalf("quality must be ignored: %s", parts.Body)
		}
		if body["response_format"] != "url" {
			t.Fatalf("provider option not merged: %s", parts.Body)
		}
		if _, ok := body["provider_options"]; ok {
			t.Fatalf("provider_options must not leak: %s", parts.Body)
		}
	})

	t.Run("volcengine 默认 watermark=false", func(t *testing.T) {
		raw := `{"model":"doubao-seedream-5-0-flash-260915","prompt":"a cat"}`
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", raw, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_img_volc", ProtocolCode: "openai", VendorCode: "volcengine",
			Type: "api_key", APIKey: "ark-key", BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(parts.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["watermark"] != false {
			t.Fatalf("watermark = %v, want false: %s", body["watermark"], parts.Body)
		}
	})

	t.Run("qwen 改写为原生 multimodal-generation 报文", func(t *testing.T) {
		raw := `{"model":"qwen-image-3.0","prompt":"a cat","n":2,"size":"1024x1024","response_format":"url"}`
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", raw, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_img_qwen", ProtocolCode: "openai", VendorCode: "qwen",
			Type: "api_key", APIKey: "sk-qwen", BaseURL: "https://dashscope.aliyuncs.com",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(parts.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "qwen-image-3.0" {
			t.Fatalf("model = %v", body["model"])
		}
		input, _ := body["input"].(map[string]any)
		if input == nil {
			t.Fatalf("input missing: %s", parts.Body)
		}
		parameters, _ := body["parameters"].(map[string]any)
		if parameters["size"] != "1024*1024" || parameters["n"] != float64(2) || parameters["watermark"] != false {
			t.Fatalf("parameters = %#v", parameters)
		}
	})
}

func TestChainProviderDriverImageUpstreamURLs(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", `{"model":"x","prompt":"p"}`, nil)
	cases := []struct {
		name    string
		account gatewaydispatch.AccountCandidate
		want    string
	}{
		{
			name: "gpt 原生",
			account: gatewaydispatch.AccountCandidate{
				ID: "u1", ProtocolCode: "openai", VendorCode: "gpt", Type: "api_key",
				BaseURL: "https://api.openai.com/v1",
			},
			want: "https://api.openai.com/v1/images/generations",
		},
		{
			name: "xai 原生",
			account: gatewaydispatch.AccountCandidate{
				ID: "u2", ProtocolCode: "openai", VendorCode: "xai", Type: "api_key",
				BaseURL: "https://api.x.ai/v1",
			},
			want: "https://api.x.ai/v1/images/generations",
		},
		{
			name: "volcengine 版本根",
			account: gatewaydispatch.AccountCandidate{
				ID: "u3", ProtocolCode: "openai", VendorCode: "volcengine", Type: "api_key",
				BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
			},
			want: "https://ark.cn-beijing.volces.com/api/v3/images/generations",
		},
		{
			name: "qwen origin",
			account: gatewaydispatch.AccountCandidate{
				ID: "u4", ProtocolCode: "openai", VendorCode: "qwen", Type: "api_key",
				BaseURL: "https://dashscope.aliyuncs.com",
			},
			want: "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation",
		},
		{
			name: "qwen 误配 compatible-mode 版本根仍归一到 origin",
			account: gatewaydispatch.AccountCandidate{
				ID: "u5", ProtocolCode: "openai", VendorCode: "qwen", Type: "api_key",
				BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			},
			want: "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, tc.account, req)
			if err != nil {
				t.Fatalf("build urls: %v", err)
			}
			if len(urls) != 1 || urls[0] != tc.want {
				t.Fatalf("urls = %#v, want %q", urls, tc.want)
			}
		})
	}
}
