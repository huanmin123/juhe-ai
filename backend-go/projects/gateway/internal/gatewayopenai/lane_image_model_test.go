package gatewayopenai

import "testing"

// TestIsImageGenerationModelGrokImagineVideoExclusion 钉值 grok-imagine 前缀
// 的图像判定边界（契约 §6.1：grok-imagine-video-* 是 xAI 官方视频模型）：
// 图像前缀命中后必须排除 grok-imagine-video 段，否则视频创建请求会被
// preflight 的 accountModelsTargetImage 强改 LaneImage 并被图像门禁 403。
func TestIsImageGenerationModelGrokImagineVideoExclusion(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"grok-imagine", true},
		{"grok-imagine-image", true},
		{"grok-imagine-image-2.0", true},
		{"grok-imagine-video-1.5", false},
		{"grok-imagine-video", false},
		{"Grok-Imagine-Video-1.5", false},
		{"gpt-image-1", true},
		{"dall-e-3", true},
		{"grok-4.7", false},
	}
	for _, tc := range cases {
		if got := IsImageGenerationModel(tc.model); got != tc.want {
			t.Errorf("IsImageGenerationModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
