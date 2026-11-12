// imagejson.go 承载同步图像原生响应到 OpenAI Images 的转换计划（2026-11-11
// 图像生成统一协议同步批）。gpt/xai/volcengine 的上游响应已是 OpenAI Images
// 兼容形态，保持纯透传；只有 qwen 的 DashScope multimodal-generation 响应需要
// 完整缓冲后转换。响应宽容：转换成功只需要 data[] 有 url；缺 created/model/
// usage 不拦截。
package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// mediaImageTransformMaxBytes 是图像原生响应转换的完整缓冲窗口。qwen 图像
// 响应只含图片 URL（非 base64），4MB 足够覆盖多图/额外元数据；超限即 502，
// 不把未转换的原生 JSON 透传给期待 OpenAI Images 的客户端。
const mediaImageTransformMaxBytes = 4 * 1024 * 1024

type mediaImageTransform struct {
	active  bool
	adapter gatewaymedia.ImageAdapter
	request gatewaymedia.ImageRequest
}

func mediaImageTransformPlan(input HandleUpstreamResponseInput) mediaImageTransform {
	plan := mediaImageTransform{}
	if input.Req == nil || input.Account == nil || input.UpstreamResponse == nil || !input.UpstreamResponse.OK() {
		return plan
	}
	if !gatewaymedia.IsImageGenerationPath(input.Req.PathAndQuery()) {
		return plan
	}
	adapter := gatewaymedia.ImageAdapterForProvider(input.Account.GetProviderCode())
	if adapter == nil || !adapter.RequiresResponseTransform() {
		return plan
	}
	plan.active = true
	plan.adapter = adapter
	plan.request = gatewaymedia.ParseImageRequest(input.Req.ParsedJSONObjectBody(), input.Account.GetProviderCode())
	return plan
}
