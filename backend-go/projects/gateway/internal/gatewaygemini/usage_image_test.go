package gatewaygemini

import "testing"

// Gemini 原生图像响应（非流式）的 inlineData 张数进入统一媒体 image 计量：
// 只计 image/* part，音频/文本 part 不计；来源标 upstream（设计 §6.2/§7.3）。
func TestParseUsageFromJSONBufferCountsInlineImages(t *testing.T) {
	body := []byte(`{
		"candidates": [{
			"content": {"parts": [
				{"inlineData": {"mimeType": "image/png", "data": "aW1nMQ=="}},
				{"inlineData": {"mimeType": "image/jpeg", "data": "aW1nMg=="}},
				{"inlineData": {"mimeType": "audio/L16;rate=24000", "data": "YXVkaW8="}},
				{"text": "done"}
			]}
		}],
		"usageMetadata": {"promptTokenCount": 3, "candidatesTokenCount": 5}
	}`)
	usage := ParseUsageFromJSONBuffer(body)
	if usage.OutputImageCount == nil || *usage.OutputImageCount != 2 {
		t.Fatalf("outputImageCount = %v, want 2", usage.OutputImageCount)
	}
	if usage.QuantitySource != "upstream" {
		t.Fatalf("quantitySource = %q, want upstream", usage.QuantitySource)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 3 {
		t.Fatalf("token usage must stay unchanged: %#v", usage)
	}
}

func TestParseUsageTextResponseHasNoImageCount(t *testing.T) {
	usage := ParseUsageFromJSONBuffer([]byte(`{
		"candidates": [{"content": {"parts": [{"text": "hello"}]}}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 2}
	}`))
	if usage.OutputImageCount != nil {
		t.Fatalf("text response must not infer image count: %v", *usage.OutputImageCount)
	}
	if usage.QuantitySource != "" {
		t.Fatalf("quantitySource = %q, want empty", usage.QuantitySource)
	}
	// 完整响应 candidates 在场但无图片 part：标记「上游明确 0 张」事实，
	// 由 gatewayresponse 只对图片请求写 0；文本请求不进入图片计量。
	if !usage.ImageCandidatesPresent {
		t.Fatal("完整响应 candidates 在场应标记 ImageCandidatesPresent")
	}
}
