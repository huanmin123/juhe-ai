// mediatranscription.go 承载同步语音转写在非流式响应管道的转换计划。
// 火山 openspeech 极速版返回头驱动状态 + {audio_info.duration, result.text}
// 的厂商 JSON；对外必须保持 OpenAI /v1/audio/transcriptions 的
// json/verbose_json/text/srt/vtt 形态。响应完整缓冲后经 TranscriptionAdapter
// 转换再下发，未转换的上游 JSON 不透传给客户端。
package gatewayresponse

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// mediaTranscriptionTransform 是一次同步转写请求的响应转换计划。
type mediaTranscriptionTransform struct {
	active  bool
	adapter gatewaymedia.TranscriptionAdapter
	request gatewaymedia.TranscriptionRequest
}

// mediaTranscriptionTransformPlan 判定本响应是否需要同步转写转换：请求路径
// 是 /audio/transcriptions（含 translations 形态由 chain 侧先行拒绝）、账户
// provider=volcengine、上游 2xx。其它 provider（gpt/glm 等原生 OpenAI 形态）
// 维持纯透传，不进本计划。
func mediaTranscriptionTransformPlan(input HandleUpstreamResponseInput) mediaTranscriptionTransform {
	plan := mediaTranscriptionTransform{}
	if input.Req == nil || !input.UpstreamResponse.OK() {
		return plan
	}
	if !gatewaymedia.IsTranscriptionPath(input.Req.PathAndQuery()) {
		return plan
	}
	if input.Account == nil {
		return plan
	}
	if strings.ToLower(strings.TrimSpace(input.Account.GetProviderCode())) != "volcengine" {
		return plan
	}
	adapter := gatewaymedia.TranscriptionAdapterForProvider("volcengine")
	if adapter == nil {
		return plan
	}
	plan.active = true
	plan.adapter = adapter
	// 响应转换只需要 response_format/language；音频内容与 model 已在请求侧
	// 消费（此处再次读取表单字段仅为纯函数投影，不复制上传文件）。
	if input.Req.Body != nil && len(input.Req.Body.RawBody) > 0 {
		contentType := input.Req.Header("Content-Type")
		if input.Req.Body.State != nil && input.Req.Body.State.ContentType != "" {
			contentType = input.Req.Body.State.ContentType
		}
		endpoint, _ := gatewaymedia.AudioEndpointPath(input.Req.PathAndQuery())
		plan.request.ResponseFormat, _ = gatewaybody.ExtractMultipartAudioResponseFormat(input.Req.Body.RawBody, contentType, endpoint)
		plan.request.Language, _ = gatewaybody.ExtractMultipartAudioLanguage(input.Req.Body.RawBody, contentType, endpoint)
	}
	return plan
}
