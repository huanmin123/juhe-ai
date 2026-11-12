package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 2026-11-11 图像生成统一协议同步批：driver 级请求归一与 URL 分支。
func TestChainProviderDriverImageRequestParts(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("gpt 忽略 stream/partial_images 返回 JSON", func(t *testing.T) {
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
		var body map[string]any
		if err := json.Unmarshal(parts.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		for _, key := range []string{"stream", "partial_images"} {
			if _, ok := body[key]; ok {
				t.Fatalf("%s must be ignored: %s", key, parts.Body)
			}
		}
		if body["model"] != "gpt-image-2" || body["prompt"] != "a cat" {
			t.Fatalf("base params changed: %s", parts.Body)
		}
		if parts.Headers.Get("Authorization") != "Bearer sk-gpt" {
			t.Fatalf("authorization = %q", parts.Headers.Get("Authorization"))
		}
	})

	t.Run("xai 合并 metadata 并忽略未文档化参数", func(t *testing.T) {
		raw := `{"model":"grok-imagine-image-2.0","prompt":"a cat","stream":true,"quality":"high","metadata":{"response_format":"url"}}`
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
			t.Fatalf("metadata not merged: %s", parts.Body)
		}
		for _, key := range []string{"metadata", "provider_options"} {
			if _, ok := body[key]; ok {
				t.Fatalf("%s must not leak: %s", key, parts.Body)
			}
		}
	})

	t.Run("公开 provider_options 返回 400 invalid_image_request", func(t *testing.T) {
		raw := `{"model":"grok-imagine-image-2.0","prompt":"a cat","provider_options":{"xai":{"response_format":"url"}}}`
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/generations", raw, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_img_xai_legacy", ProtocolCode: "openai", VendorCode: "xai",
			Type: "api_key", APIKey: "sk-xai", BaseURL: "https://api.x.ai/v1",
		}
		_, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		var validation *gatewaypreauth.GatewayRequestValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("err = %v, want GatewayRequestValidationError", err)
		}
		if validation.StatusCode != http.StatusBadRequest || validation.Code != "invalid_image_request" {
			t.Fatalf("validation = %#v, want 400 invalid_image_request", validation)
		}
		if !strings.Contains(validation.Message, "metadata") {
			t.Fatalf("message = %q, want metadata hint", validation.Message)
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

// 2026-11-12 T216：编辑面在 driver 层的请求归一与出站路径。
func chainImageEditMultipart(t *testing.T, fields, fileField, filename string) (string, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, field := range strings.Split(fields, "&") {
		name, value, _ := strings.Cut(field, "=")
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+fileField+`"; filename="`+filename+`"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\nmock-png-body")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buffer.String(), writer.FormDataContentType()
}

func TestChainProviderDriverImageEditRequestParts(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("gpt multipart 字节级透传", func(t *testing.T) {
		raw, contentType := chainImageEditMultipart(t, "model=gpt-image-2&prompt=edit", "image", "cat.png")
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/edits", raw,
			map[string]string{"Content-Type": contentType})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_edit_gpt", ProtocolCode: "openai", VendorCode: "gpt",
			Type: "api_key", APIKey: "sk-gpt", BaseURL: "https://api.openai.com/v1",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if string(parts.Body) != raw {
			t.Fatalf("gpt multipart edit must stay byte passthrough: got %s", parts.Body)
		}
		if got := parts.Headers.Get("Content-Type"); got != contentType {
			t.Fatalf("content-type = %q, want %q", got, contentType)
		}
		if parts.Headers.Get("Authorization") != "Bearer sk-gpt" {
			t.Fatalf("authorization = %q", parts.Headers.Get("Authorization"))
		}
	})

	t.Run("gpt multipart 带 stream 时重建并删除字段", func(t *testing.T) {
		raw, contentType := chainImageEditMultipart(t, "model=gpt-image-2&prompt=edit&stream=true&partial_images=2", "image", "cat.png")
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/edits", raw,
			map[string]string{"Content-Type": contentType})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_edit_gpt2", ProtocolCode: "openai", VendorCode: "gpt",
			Type: "api_key", APIKey: "sk-gpt", BaseURL: "https://api.openai.com/v1",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if string(parts.Body) == raw {
			t.Fatal("multipart with stream must be rebuilt")
		}
		rebuiltType := parts.Headers.Get("Content-Type")
		if !strings.HasPrefix(rebuiltType, "multipart/form-data; boundary=") {
			t.Fatalf("rebuilt content-type = %q", rebuiltType)
		}
		_, params, err := mime.ParseMediaType(rebuiltType)
		if err != nil {
			t.Fatalf("parse rebuilt content-type: %v", err)
		}
		reader := multipart.NewReader(bytes.NewReader(parts.Body), params["boundary"])
		names := map[string]bool{}
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatalf("read rebuilt part: %v", nextErr)
			}
			names[part.FormName()] = true
			_ = part.Close()
		}
		if names["stream"] || names["partial_images"] {
			t.Fatalf("rebuilt multipart still carries ignored fields: %#v", names)
		}
		if !names["model"] || !names["prompt"] || !names["image"] {
			t.Fatalf("rebuilt multipart lost fields: %#v", names)
		}
	})

	t.Run("qwen 编辑改写为原生 multimodal-generation 报文", func(t *testing.T) {
		raw, contentType := chainImageEditMultipart(t, "model=qwen-image-3.0&prompt=edit&response_format=url", "image[]", "cat.png")
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/edits", raw,
			map[string]string{"Content-Type": contentType})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_edit_qwen", ProtocolCode: "openai", VendorCode: "qwen",
			Type: "api_key", APIKey: "sk-qwen", BaseURL: "https://dashscope.aliyuncs.com",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		var body map[string]any
		if err := json.Unmarshal(parts.Body, &body); err != nil {
			t.Fatalf("decode body: %v; body=%s", err, parts.Body)
		}
		input, _ := body["input"].(map[string]any)
		messages, _ := input["messages"].([]any)
		content, _ := messages[0].(map[string]any)["content"].([]any)
		if len(content) != 2 || content[0].(map[string]any)["image"] == nil || content[1].(map[string]any)["text"] != "edit" {
			t.Fatalf("content = %#v", content)
		}
	})
}

func TestChainProviderDriverImageEditUpstreamURLs(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	req := newAuthMatrixRequest(t, http.MethodPost, "/v1/images/edits", `{"model":"x","prompt":"p"}`, nil)
	cases := []struct {
		name    string
		account gatewaydispatch.AccountCandidate
		want    string
	}{
		{
			name: "gpt 原生 edits",
			account: gatewaydispatch.AccountCandidate{
				ID: "e1", ProtocolCode: "openai", VendorCode: "gpt", Type: "api_key",
				BaseURL: "https://api.openai.com/v1",
			},
			want: "https://api.openai.com/v1/images/edits",
		},
		{
			name: "xai 原生 edits",
			account: gatewaydispatch.AccountCandidate{
				ID: "e2", ProtocolCode: "openai", VendorCode: "xai", Type: "api_key",
				BaseURL: "https://api.x.ai/v1",
			},
			want: "https://api.x.ai/v1/images/edits",
		},
		{
			name: "volcengine 编辑改写为 generations",
			account: gatewaydispatch.AccountCandidate{
				ID: "e3", ProtocolCode: "openai", VendorCode: "volcengine", Type: "api_key",
				BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
			},
			want: "https://ark.cn-beijing.volces.com/api/v3/images/generations",
		},
		{
			name: "qwen 编辑走原生 multimodal-generation",
			account: gatewaydispatch.AccountCandidate{
				ID: "e4", ProtocolCode: "openai", VendorCode: "qwen", Type: "api_key",
				BaseURL: "https://dashscope.aliyuncs.com",
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
