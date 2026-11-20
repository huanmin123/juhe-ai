package gatewayresponse

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// usageWithRequestMediaMetering 为同步图片请求补齐统一媒体模板所需的
// 计量事实（非对话媒体统一计费设计 §7.3）：
//
//   - 张数：上游 usage/data[] 计数优先并标 QuantitySource=upstream；
//     无上游计数时用请求 n（缺省 1）并标 request。
//   - 档位：请求生效 size 按长边归入 1k/2k/4k；size 为 auto/缺失/无法
//     解析时留空，由引擎回落到模型 baseTier（设计 §4.2）。
//
// 只处理图片端点；token 等既有 usage 事实不变。失败响应不调用本函数，
// 避免把请求参数当作成功计量。
//
// Gemini 原生 generateContent 图像面（媒体设计 §6.2）：响应完整 JSON 的
// inlineData 图片张数由 gatewaygemini 解析为 upstream 计数；流式分片不做
// part 计数（避免同一张图的分片重复计费），无上游计数时按请求
// generationConfig.candidateCount（缺省 1）兜底并标 request。
func usageWithRequestMediaMetering(req *gatewaypreauth.GatewayRequest, usage gatewayproto.ParsedUsage) gatewayproto.ParsedUsage {
	if req == nil {
		return usage
	}
	path := req.PathAndQuery()
	if isSyncImageGenerationPath(path) {
		requestSize, requestCount := imageRequestMetering(req, path)
		if usage.OutputImageCount == nil {
			count := requestCount
			if count <= 0 {
				count = 1
			}
			usage.OutputImageCount = &count
			usage.QuantitySource = "request"
		} else if usage.QuantitySource == "" {
			usage.QuantitySource = "upstream"
		}
		if usage.ResolutionTier == "" {
			if width, height, ok := mediapricing.ParseSize(requestSize); ok {
				if tier, ok := mediapricing.ImageTierForSize(width, height); ok {
					usage.ResolutionTier = tier
				}
			}
		}
		return usage
	}
	if count, ok := geminiRequestImageCount(req, path); ok {
		if usage.OutputImageCount == nil {
			if usage.ImageCandidatesPresent {
				// 完整非流式响应有 candidates 但无图片 part：上游明确 0 张，
				// 不得回落请求 candidateCount 造成超收（流式片段不置该标志，
				// 仍按请求候选数兜底）。
				zero := 0
				usage.OutputImageCount = &zero
				usage.QuantitySource = "upstream"
			} else {
				usage.OutputImageCount = &count
				usage.QuantitySource = "request"
			}
		} else if usage.QuantitySource == "" {
			usage.QuantitySource = "upstream"
		}
	}
	return usage
}

// geminiRequestImageCount 识别 Gemini 原生 generateContent/streamGenerateContent
// 图像请求并返回请求张数（candidateCount，缺省 1）。识别信号按可靠性排序：
// imageConfig、responseModalities 含 IMAGE、模型名含 image；三者都无则不是
// 图像请求。流式分片不计 inlineData part，仍按请求候选数兜底（一次请求
// 只计一次，避免同一张图的分片重复计费）。
func geminiRequestImageCount(req *gatewaypreauth.GatewayRequest, path string) (int, bool) {
	endpoint := strings.ToLower(gatewaybody.EndpointPathOf(path))
	// 流式与非流式共用同一请求形态与 candidateCount 语义；只在响应解析层
	// 区分是否按 inlineData part 计数。
	if !strings.Contains(endpoint, ":generatecontent") &&
		!strings.Contains(endpoint, ":streamgeneratecontent") {
		return 0, false
	}
	object := imageRequestJSONObject(req)
	imageRequest := false
	count := 1
	if object != nil {
		if config, ok := object["generationConfig"].(map[string]any); ok {
			if _, hasImageConfig := config["imageConfig"]; hasImageConfig {
				imageRequest = true
			}
			if modalities, ok := config["responseModalities"].([]any); ok {
				for _, raw := range modalities {
					if text, ok := raw.(string); ok && strings.EqualFold(strings.TrimSpace(text), "IMAGE") {
						imageRequest = true
					}
				}
			}
			if parsed, valid := imageRequestCountWithin(config["candidateCount"], maxGeminiImageCandidateCount); valid {
				count = parsed
			}
		}
		if model, ok := object["model"].(string); ok && strings.Contains(strings.ToLower(model), "image") {
			imageRequest = true
		}
	}
	if strings.Contains(strings.ToLower(geminiModelFromRequestPath(endpoint)), "image") {
		imageRequest = true
	}
	if !imageRequest {
		return 0, false
	}
	return count, true
}

// geminiModelFromRequestPath 取 /models/{model}:generateContent 中的模型名。
func geminiModelFromRequestPath(endpoint string) string {
	index := strings.Index(endpoint, "/models/")
	if index < 0 {
		return ""
	}
	model := endpoint[index+len("/models/"):]
	if colon := strings.Index(model, ":"); colon >= 0 {
		model = model[:colon]
	}
	return model
}

// isSyncImageGenerationPath 匹配 OpenAI images 生成/编辑/变体端点族，兼容
// 带供应商前缀的路径（如 /api/v3/images/generations）。
func isSyncImageGenerationPath(path string) bool {
	path = strings.ToLower(gatewaybody.EndpointPathOf(path))
	index := strings.LastIndex(path, "/images/")
	if index < 0 {
		return false
	}
	switch path[index+len("/images/"):] {
	case "generations", "edits", "variations":
		return true
	default:
		return false
	}
}

// imageRequestMetering 读取请求生效 size 与 n：capture 阶段缓存优先
// （JSON 与 multipart 共用）；缓存缺失时 JSON 体再解析一次，multipart
// （images/edits、images/variations）走共享有界扫描。缺失/非法返回零值。
func imageRequestMetering(req *gatewaypreauth.GatewayRequest, path string) (string, int) {
	if state := req.BodyState(); state != nil {
		if state.ImageRequestSize != nil || state.ImageRequestCount != nil {
			size, count := "", 0
			if state.ImageRequestSize != nil {
				size = strings.TrimSpace(*state.ImageRequestSize)
			}
			if state.ImageRequestCount != nil {
				count = *state.ImageRequestCount
			}
			return size, count
		}
		if !state.IsJSON {
			// capture 阶段已对非 JSON 图片体做过一次有界扫描；无字段即
			// 确实缺失，不再对最大 64MB 的上传体做二次遍历。
			return "", 0
		}
	}
	if object := imageRequestJSONObject(req); object != nil {
		size, _ := object["size"].(string)
		size = strings.TrimSpace(size)
		count := 0
		if raw, ok := object["n"]; ok {
			if parsed, valid := imageRequestCount(raw); valid {
				count = parsed
			}
		}
		return size, count
	}
	if req.Body == nil || len(req.Body.RawBody) == 0 {
		return "", 0
	}
	contentType := req.Header("Content-Type")
	if req.Body.State != nil && req.Body.State.ContentType != "" {
		contentType = req.Body.State.ContentType
	}
	return gatewaybody.ExtractMultipartImageMetering(req.Body.RawBody, contentType, path)
}

// imageRequestJSONObject 复用请求已解析对象；未解析时只对原始 JSON 体做
// 一次小对象反序列化（图片生成体不含二进制上传）。
func imageRequestJSONObject(req *gatewaypreauth.GatewayRequest) map[string]any {
	if object := req.ParsedJSONObjectBody(); object != nil {
		return object
	}
	if req.Body == nil || len(req.Body.RawBody) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(req.Body.RawBody, &object); err != nil {
		return nil
	}
	return object
}

// imageRequestCount 解析请求 n（JSON number/字符串或 multipart 字符串）。
// 非正整数与异常超大值视为缺失，避免无依据的数量进入计费。
func imageRequestCount(value any) (int, bool) {
	return imageRequestCountWithin(value, maxImageRequestCount)
}

// imageRequestCountWithin 同 imageRequestCount，但按供应商语义收窄上限
// （Gemini candidateCount 远小于 OpenAI images n 的防御上限）。
func imageRequestCountWithin(value any, limit int) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 || typed != math.Trunc(typed) || typed > float64(limit) {
			return 0, false
		}
		return int(typed), true
	case int:
		if typed <= 0 || typed > limit {
			return 0, false
		}
		return typed, true
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		if err != nil || parsed <= 0 || parsed > limit {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil || parsed <= 0 || parsed > limit {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// maxImageRequestCount 是请求 n 的计量上限：只用于拒绝异常值，不改变
// 上游实际返回 data[] 的计数口径。
const maxImageRequestCount = 1000000

// maxGeminiImageCandidateCount 是 Gemini 图像 candidateCount 的语义上限
// （官方 1..8）；无上游计数时超限按缺失回落 1，避免异常值被当成兜底张数。
const maxGeminiImageCandidateCount = 8
