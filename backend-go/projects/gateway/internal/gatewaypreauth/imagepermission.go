package gatewaypreauth

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Image generation permission gate, mirroring request/image-permission.ts
// plus the request-lane resolution the pre-auth path needs.

// ImageGenerationDisabledCode mirrors imageGenerationDisabledCode.
const ImageGenerationDisabledCode = "image_generation_disabled"

// ImageGenerationDisabledMessage mirrors imageGenerationDisabledMessage
// (byte-identical client copy).
const ImageGenerationDisabledMessage = "当前用户图像生成被禁用了，请联系管理员开启"

// IsImageGenerationDisabledForAPIKey mirrors isImageGenerationDisabledForApiKey:
// the image lane is rejected unless the owning system account explicitly
// enabled image generation (system_account_image_generation_enabled === 1).
func IsImageGenerationDisabledForAPIKey(apiKey *gatewayruntimecache.GatewayAPIKeyRow, requestLane gatewayproto.RequestLane) bool {
	return requestLane == gatewayproto.LaneImage &&
		apiKey != nil &&
		apiKey.SystemAccountImageGenerationEnabled != 1
}

// ResolveOpenAIGatewayRequestLane mirrors resolveOpenAIGatewayRequestLane:
// the image endpoint/model short circuit plus the captured body image hints.
// The pure classification delegates to the gatewayopenai lane port; the
// captured bodyState.imageGeneration flag is checked here like the Node
// source.
func ResolveOpenAIGatewayRequestLane(req *GatewayRequest) gatewayproto.RequestLane {
	lane := gatewayopenai.ResolveRequestLane(req.PathAndQuery(), laneInspectionBody(req), requestModelHint(req))
	if lane == gatewayproto.LaneImage {
		return lane
	}
	if state := req.BodyState(); state != nil && state.ImageGeneration {
		return gatewayproto.LaneImage
	}
	return lane
}

// laneInspectionBody mirrors requestInspectionBody: req.body when it is a
// record, else the parsed JSON body.
func laneInspectionBody(req *GatewayRequest) map[string]any {
	return req.ParsedJSONObjectBody()
}

// requestModelHint mirrors requestModelHint: the raw parsed body model
// string wins over the captured body state model.
func requestModelHint(req *GatewayRequest) string {
	if object := req.ParsedJSONObjectBody(); object != nil {
		if model, ok := object["model"].(string); ok {
			return model
		}
	}
	if state := req.BodyState(); state != nil && state.Model != nil {
		return *state.Model
	}
	return ""
}

// IsOpenAIGatewayImageEndpointOrModelRequest mirrors
// isOpenAIGatewayImageEndpointOrModelRequest: the /images endpoint prefix or
// an image generation model hint; body tool hints do not count.
func IsOpenAIGatewayImageEndpointOrModelRequest(req *GatewayRequest) bool {
	path := strings.ToLower(strings.SplitN(req.Path(), "?", 2)[0])
	if path == "" {
		path = strings.ToLower(strings.SplitN(req.PathAndQuery(), "?", 2)[0])
	}
	if path == "/images" || strings.HasPrefix(path, "/images/") ||
		path == "/v1/images" || strings.HasPrefix(path, "/v1/images/") {
		return true
	}
	return IsOpenAIGatewayImageGenerationModel(requestModelHint(req))
}

// IsOpenAIGatewayImageGenerationModel mirrors isOpenAIGatewayImageGenerationModel.
func IsOpenAIGatewayImageGenerationModel(model string) bool {
	return gatewayopenai.IsImageGenerationModel(model)
}
