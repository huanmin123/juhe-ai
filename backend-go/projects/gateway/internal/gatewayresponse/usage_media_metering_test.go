package gatewayresponse

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 同步图片计量契约（非对话媒体统一计费设计 §7.3/AC-006）：
// data[] 张数优先，否则请求 n（缺省 1）；请求 size 按长边归档，auto/缺失
// 留给引擎回落 baseTier。

func newImageMeteringRequest(t *testing.T, path string, rawBody []byte, contentType string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(rawBody))
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Body = &gatewaybody.Request{
		RawBody:           rawBody,
		ContentTypeHeader: contentType,
	}
	return req
}

func TestUsageWithRequestMediaMeteringJSONImage(t *testing.T) {
	req := newImageMeteringRequest(t, "/v1/images/generations",
		[]byte(`{"model":"gpt-image-2","size":"1536x1024","n":2}`), "application/json")
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 2 {
		t.Fatalf("outputImageCount = %v, want 2", usage.OutputImageCount)
	}
	if usage.QuantitySource != "request" {
		t.Fatalf("quantitySource = %q, want request", usage.QuantitySource)
	}
	if usage.ResolutionTier != "2k" {
		t.Fatalf("resolutionTier = %q, want 2k", usage.ResolutionTier)
	}
}

func TestUsageWithRequestMediaMeteringUpstreamCountWins(t *testing.T) {
	req := newImageMeteringRequest(t, "/v1/images/generations",
		[]byte(`{"size":"4096x4096","n":4}`), "application/json")
	upstreamCount := 2
	usage := usageWithRequestMediaMetering(req, gatewayproto.ParsedUsage{OutputImageCount: &upstreamCount})
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 2 {
		t.Fatalf("upstream count overwritten: %v", usage.OutputImageCount)
	}
	if usage.QuantitySource != "upstream" {
		t.Fatalf("quantitySource = %q, want upstream", usage.QuantitySource)
	}
	if usage.ResolutionTier != "4k" {
		t.Fatalf("resolutionTier = %q, want 4k", usage.ResolutionTier)
	}
}

func TestUsageWithRequestMediaMeteringDefaultsAndAutoSize(t *testing.T) {
	req := newImageMeteringRequest(t, "/v1/images/generations",
		[]byte(`{"size":"auto"}`), "application/json")
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 1 {
		t.Fatalf("outputImageCount = %v, want default 1", usage.OutputImageCount)
	}
	if usage.QuantitySource != "request" {
		t.Fatalf("quantitySource = %q, want request", usage.QuantitySource)
	}
	if usage.ResolutionTier != "" {
		t.Fatalf("auto size must leave tier empty, got %q", usage.ResolutionTier)
	}
}

func TestUsageWithRequestMediaMeteringInvalidN(t *testing.T) {
	req := newImageMeteringRequest(t, "/v1/images/generations",
		[]byte(`{"size":"1024x1024","n":0}`), "application/json")
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 1 {
		t.Fatalf("invalid n must fall back to 1, got %v", usage.OutputImageCount)
	}
	if usage.ResolutionTier != "1k" {
		t.Fatalf("resolutionTier = %q, want 1k", usage.ResolutionTier)
	}
}

func TestUsageWithRequestMediaMeteringMultipartEdit(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("size", "2048X2048")
	_ = writer.WriteField("n", "3")
	part, err := writer.CreateFormFile("image", "a.png")
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte("PNGDATA")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := newImageMeteringRequest(t, "/v1/images/edits", buf.Bytes(), writer.FormDataContentType())
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 3 {
		t.Fatalf("outputImageCount = %v, want 3", usage.OutputImageCount)
	}
	if usage.QuantitySource != "request" {
		t.Fatalf("quantitySource = %q, want request", usage.QuantitySource)
	}
	if usage.ResolutionTier != "2k" {
		t.Fatalf("resolutionTier = %q, want 2k", usage.ResolutionTier)
	}
}

func TestUsageWithRequestMediaMeteringSkipsNonImagePath(t *testing.T) {
	req := newImageMeteringRequest(t, "/v1/chat/completions",
		[]byte(`{"size":"1536x1024","n":2}`), "application/json")
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount != nil || usage.QuantitySource != "" || usage.ResolutionTier != "" {
		t.Fatalf("non-image path must stay untouched: %#v", usage)
	}
}
