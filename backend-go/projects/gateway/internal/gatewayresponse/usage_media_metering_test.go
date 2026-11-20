package gatewayresponse

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
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

func TestUsageWithRequestMediaMeteringEmptyDataCountsZero(t *testing.T) {
	// 上游成功响应带 data: []：data[] 存在即按 0 张计（source=upstream），
	// 禁止回落到请求 n=4 造成超收（统一计费设计 §7.3）。
	req := newImageMeteringRequest(t, "/v1/images/generations",
		[]byte(`{"size":"1024x1024","n":4}`), "application/json")
	parsed := gatewayopenai.ParseUsageFromJSONBuffer([]byte(`{"created":1,"data":[],"usage":{"output_tokens":0}}`))
	usage := usageWithRequestMediaMetering(req, parsed)
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 0 {
		t.Fatalf("empty data[] must count 0, got %v", usage.OutputImageCount)
	}
	if usage.QuantitySource != "upstream" {
		t.Fatalf("quantitySource = %q, want upstream", usage.QuantitySource)
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

func TestUsageWithRequestMediaMeteringGeminiImageFallback(t *testing.T) {
	// Gemini 原生 generateContent 图像请求：无上游计数时按 candidateCount
	// 兜底并标 request（流式分片不做 part 计数，设计 §6.2/§7.3）。
	req := newImageMeteringRequest(t, "/v1beta/models/gemini-3-pro-image:generateContent",
		[]byte(`{"generationConfig":{"imageConfig":{"aspectRatio":"1:1"},"candidateCount":2}}`), "application/json")
	usage := usageWithRequestMediaMetering(req, gatewayproto.EmptyUsage())
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 2 {
		t.Fatalf("gemini image count = %v, want 2", usage.OutputImageCount)
	}
	if usage.QuantitySource != "request" {
		t.Fatalf("quantitySource = %q, want request", usage.QuantitySource)
	}

	// 流式 generateContent 同样按请求 candidateCount 兜底：分片不按 part
	// 计数，但一次请求必须有一次可计费张数。
	streamReq := newImageMeteringRequest(t, "/v1beta/models/gemini-3-pro-image:streamGenerateContent",
		[]byte(`{"generationConfig":{"responseModalities":["IMAGE"],"candidateCount":2}}`), "application/json")
	streamUsage := usageWithRequestMediaMetering(streamReq, gatewayproto.EmptyUsage())
	if streamUsage.OutputImageCount == nil || *streamUsage.OutputImageCount != 2 {
		t.Fatalf("gemini stream image count = %v, want 2", streamUsage.OutputImageCount)
	}
	if streamUsage.QuantitySource != "request" {
		t.Fatalf("gemini stream quantitySource = %q, want request", streamUsage.QuantitySource)
	}

	// 超出 Gemini 语义上限（1..8）的 candidateCount 视为缺失，回落 1；
	// 不能把异常值当成无上游计数时的兜底张数。
	hugeReq := newImageMeteringRequest(t, "/v1beta/models/gemini-3-pro-image:generateContent",
		[]byte(`{"generationConfig":{"responseModalities":["IMAGE"],"candidateCount":1000000}}`), "application/json")
	hugeUsage := usageWithRequestMediaMetering(hugeReq, gatewayproto.EmptyUsage())
	if hugeUsage.OutputImageCount == nil || *hugeUsage.OutputImageCount != 1 {
		t.Fatalf("oversized candidateCount must fall back to 1, got %v", hugeUsage.OutputImageCount)
	}

	// 上游响应 inlineData 已给出计数时优先，不被请求候选数覆盖。
	upstream := 3
	usage = usageWithRequestMediaMetering(req, gatewayproto.ParsedUsage{OutputImageCount: &upstream})
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 3 {
		t.Fatalf("upstream count overwritten: %v", usage.OutputImageCount)
	}
	if usage.QuantitySource != "upstream" {
		t.Fatalf("quantitySource = %q, want upstream", usage.QuantitySource)
	}

	// 完整非流式响应 candidates 在场但无图片 part：上游明确 0 张，不得
	// 回落请求 candidateCount 造成超收。
	zeroUsage := usageWithRequestMediaMetering(req, gatewayproto.ParsedUsage{ImageCandidatesPresent: true})
	if zeroUsage.OutputImageCount == nil || *zeroUsage.OutputImageCount != 0 {
		t.Fatalf("explicit zero image count not preserved: %v", zeroUsage.OutputImageCount)
	}
	if zeroUsage.QuantitySource != "upstream" {
		t.Fatalf("zero count quantitySource = %q, want upstream", zeroUsage.QuantitySource)
	}

	// 非图像 generateContent（无 imageConfig/IMAGE 模态、模型名不含 image）
	// 不得凭空写入图片计量。
	textReq := newImageMeteringRequest(t, "/v1beta/models/gemini-3-pro:generateContent",
		[]byte(`{"generationConfig":{"candidateCount":2}}`), "application/json")
	textUsage := usageWithRequestMediaMetering(textReq, gatewayproto.EmptyUsage())
	if textUsage.OutputImageCount != nil || textUsage.QuantitySource != "" {
		t.Fatalf("text generateContent must stay untouched: %#v", textUsage)
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
