package gateway

import (
	"regexp"
	"strings"
)

var geminiImageGenerationModelPattern = regexp.MustCompile(`(^|-)gemini([^/]*-)?image(-|$)`)

func ResolveRequestLane(request RequestShape) RequestLane {
	if openAIEndpointFamily(request.Path) == EndpointImages || isImageGenerationModel(request.Model) || request.ImageGenerationHint {
		return RequestLaneImage
	}
	return RequestLaneText
}

func isImageGenerationModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return normalized != "" && (strings.HasPrefix(normalized, "gpt-image") ||
		strings.HasPrefix(normalized, "dall-e") ||
		strings.HasPrefix(normalized, "imagen-") ||
		strings.HasPrefix(normalized, "nano-banana-") ||
		geminiImageGenerationModelPattern.MatchString(normalized))
}
