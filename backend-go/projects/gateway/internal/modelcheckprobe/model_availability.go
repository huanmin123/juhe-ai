package modelcheckprobe

import "strings"

// IsModelUnavailable recognizes provider responses that specifically reject
// the requested model. Upstreams are inconsistent: some return HTTP 200 with
// an OpenAI error envelope, while others use 400/404. Generic transport or
// endpoint failures are intentionally not classified as model-scoped.
func IsModelUnavailable(result Result, expectedModel string) bool {
	if result.Success {
		return false
	}
	var values []string
	collectAvailabilityValues(result.JSON, &values)
	values = append(values, result.ErrorMessage)
	joined := strings.ToLower(strings.Join(values, " "))
	for _, code := range []string{"model_not_found", "model_not_supported", "unsupported_model", "invalid_model", "unknown_model", "model_unavailable"} {
		if strings.Contains(joined, code) {
			return true
		}
	}
	model := strings.ToLower(strings.TrimSpace(expectedModel))
	modelMentioned := model != "" && strings.Contains(joined, model)
	if modelMentioned && (strings.Contains(joined, "not supported") || strings.Contains(joined, "unsupported") || strings.Contains(joined, "not available") || strings.Contains(joined, "does not exist") || strings.Contains(joined, "not found") || strings.Contains(joined, "unknown model")) {
		return true
	}
	phrases := []string{
		"model not found", "model does not exist", "unknown model", "unsupported model",
		"model is not supported", "model isn't supported", "model unavailable",
		"no available channel", "no available model", "model.*not supported",
		"模型不存在", "模型未找到", "模型不支持", "模型不可用", "可用渠道不存在",
	}
	for _, phrase := range phrases {
		if strings.Contains(joined, phrase) && (modelMentioned || strings.Contains(phrase, "model") || strings.Contains(phrase, "模型")) {
			return true
		}
	}
	return false
}

func collectAvailabilityValues(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "code" || key == "type" || key == "message" || key == "param" || key == "status" {
				if text, ok := child.(string); ok {
					*out = append(*out, text)
				}
			}
			collectAvailabilityValues(child, out)
		}
	case []any:
		for _, child := range typed {
			collectAvailabilityValues(child, out)
		}
	}
}
