// mediastream.go 承载 M1 同步音频在非流式响应管道的转换计划（音频设计
// §5/契约 §5.1）：gemini 账户的 speech 请求上游回 JSON（inlineData base64
// PCM），网关在完整缓冲后经 gatewaymedia SpeechAdapter 转为二进制音频透传
// 下游（零转码：pcm 裸字节 + audio/L16;rate=24000）。openai 族账户的 speech
// 响应已是音频二进制，维持纯透传，不进本计划。
package gatewayresponse

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// mediaSpeechTransformMaxBytes 是媒体 speech 转换的完整缓冲窗口：base64
// PCM JSON 体积（语音产物按 24kHz/16bit/mono ≈48KB/s PCM、base64 膨胀 4/3
// 估算），远超 1MB 协议检查窗口；超限即 502 拒绝（拒绝透传未转换 JSON）。
const mediaSpeechTransformMaxBytes = 32 * 1024 * 1024

// mediaSpeechTransform 是一次请求的转换计划。
type mediaSpeechTransform struct {
	active  bool
	adapter gatewaymedia.SpeechAdapter
}

// mediaSpeechTransformPlan 判定本响应是否需要媒体 speech 转换：请求路径
// 是 /audio/speech、账户协议是 gemini（audio 转换型上游）且上游 2xx。非
// gemini 协议或非 speech 路径恒不激活（现有行为零改动）。
func mediaSpeechTransformPlan(input HandleUpstreamResponseInput) mediaSpeechTransform {
	plan := mediaSpeechTransform{}
	if input.Req == nil || !input.UpstreamResponse.OK() {
		return plan
	}
	if !gatewaymedia.IsSpeechPath(input.Req.PathAndQuery()) {
		return plan
	}
	if input.Account == nil || strings.ToLower(strings.TrimSpace(input.Account.GetProtocolCode())) != "gemini" {
		return plan
	}
	adapter := gatewaymedia.SpeechAdapterForProvider("gemini")
	if adapter == nil {
		return plan
	}
	plan.active = true
	plan.adapter = adapter
	return plan
}
