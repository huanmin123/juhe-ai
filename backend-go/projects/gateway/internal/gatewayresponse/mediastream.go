// mediastream.go 承载 M1 同步音频在非流式响应管道的转换计划（音频设计
// §5/契约 §5.1；M3 起 minimax t2a_v2 同族接入，契约 §8.2）：转换型
// speech 上游回 JSON（gemini inlineData base64 PCM / minimax data.audio
// hex），网关在完整缓冲后经 gatewaymedia SpeechAdapter 转为二进制音频
// 透传下游（零转码）。openai 族直连账户（openai/gpt/xai/deepseek/glm 等
// 非 gemini 协议且非 minimax provider）的 speech 响应已是音频二进制，
// 维持纯透传，不进本计划。
package gatewayresponse

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// mediaSpeechTransformMaxBytes 是媒体 speech 转换的完整缓冲窗口：base64
// PCM JSON 体积（语音产物按 24kHz/16bit/mono ≈48KB/s PCM、base64 膨胀 4/3
// 估算；minimax hex 膨胀 2 倍，量级相同），远超 1MB 协议检查窗口；超限即
// 502 拒绝（拒绝透传未转换 JSON）。
const mediaSpeechTransformMaxBytes = 32 * 1024 * 1024

// mediaSpeechTransform 是一次请求的转换计划。
type mediaSpeechTransform struct {
	active  bool
	adapter gatewaymedia.SpeechAdapter
	// request 是归一后的 SpeechRequest（响应转换的 content-type 推导与
	// minimax extra_info 计量上游形态判定输入；解析失败保持零值）。
	request gatewaymedia.SpeechRequest
}

// mediaSpeechTransformPlan 判定本响应是否需要媒体 speech 转换：请求路径
// 是 /audio/speech、账户是转换型 speech 上游（gemini 协议，或 M3 起
// provider=minimax 的 openai 协议族账户——t2a_v2 报文由链上 minimax
// speech 分派改写）且上游 2xx。openai 族直连与其它 provider 恒不激活
//（现有行为零改动）。
func mediaSpeechTransformPlan(input HandleUpstreamResponseInput) mediaSpeechTransform {
	plan := mediaSpeechTransform{}
	if input.Req == nil || !input.UpstreamResponse.OK() {
		return plan
	}
	if !gatewaymedia.IsSpeechPath(input.Req.PathAndQuery()) {
		return plan
	}
	if input.Account == nil {
		return plan
	}
	adapterKey := ""
	if strings.ToLower(strings.TrimSpace(input.Account.GetProtocolCode())) == "gemini" {
		adapterKey = "gemini"
	} else if strings.ToLower(strings.TrimSpace(input.Account.GetProviderCode())) == "minimax" {
		adapterKey = "minimax"
	}
	if adapterKey == "" {
		return plan
	}
	adapter := gatewaymedia.SpeechAdapterForProvider(adapterKey)
	if adapter == nil {
		return plan
	}
	plan.active = true
	plan.adapter = adapter
	// 归一 SpeechRequest（转换 content-type 推导与计量上游形态判定输入）；
	// 解析失败保持零值，adapter 按各自缺省语义处理（minimax format 缺省
	// mp3）。body 缺失/不可解析的 speech 请求到不了上游 2xx，此分支仅是
	// 防御性兜底。
	if body := input.Req.ParsedJSONObjectBody(); body != nil {
		if ir, err := gatewaymedia.ParseSpeechRequest(body); err == nil {
			plan.request = ir
		}
	}
	return plan
}
