package gatewayopenai

import (
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

var geminiImageModelPattern = regexp.MustCompile(`(?:^|-)gemini(?:[^/]*-)?image(?:-|$)`)

// ResolveRequestLane mirrors resolveOpenAIGatewayRequestLane. The body may
// be nil; bodyModel carries the request model hint when the caller already
// parsed the JSON body.
func ResolveRequestLane(pathWithQuery string, body any, bodyModel string) gatewayproto.RequestLane {
	path, _ := SplitPathAndQuery(pathWithQuery)
	path = strings.ToLower(path)
	if isImageEndpointOrModelPath(path, bodyModel) {
		return gatewayproto.LaneImage
	}
	if requestBodyHasImageGenerationHint(body) || requestBodyRequestsImageOutput(body) {
		return gatewayproto.LaneImage
	}
	return gatewayproto.LaneText
}

func isImageEndpointOrModelPath(path, model string) bool {
	if path == "/images" || strings.HasPrefix(path, "/images/") ||
		path == "/v1/images" || strings.HasPrefix(path, "/v1/images/") {
		return true
	}
	return IsImageGenerationModel(model)
}

// IsImageGenerationModel mirrors isOpenAIGatewayImageGenerationModel.
func IsImageGenerationModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "gpt-image") ||
		strings.HasPrefix(normalized, "dall-e") ||
		strings.HasPrefix(normalized, "imagen-") ||
		strings.HasPrefix(normalized, "nano-banana") ||
		geminiImageModelPattern.MatchString(normalized)
}

// requestBodyHasImageGenerationHint mirrors requestBodyHasImageGenerationHint
// (request/image-generation-tools.ts).
func requestBodyHasImageGenerationHint(body any) bool {
	inspection := inspectImageGenerationTools(body)
	return inspection.imageToolCount > 0 || inspection.forcedImageGeneration
}

type imageGenerationToolInspection struct {
	imageToolCount        int
	nonImageToolCount     int
	forcedImageGeneration bool
}

func inspectImageGenerationTools(value any) imageGenerationToolInspection {
	result := imageGenerationToolInspection{}
	body, ok := value.(map[string]any)
	if !ok {
		return result
	}
	if text, _ := body["type"].(string); text == "image_generation" {
		result.forcedImageGeneration = true
	}
	collectToolDefinitionCounts(body["tools"], &result, 0)
	collectToolDefinitionCounts(toolChoiceTools(body["tool_choice"]), &result, 0)
	if toolChoiceForcesImageGeneration(body["tool_choice"]) {
		result.forcedImageGeneration = true
	}
	if choice, _ := body["tool_choice"].(string); choice == "required" &&
		result.imageToolCount > 0 && result.nonImageToolCount == 0 {
		result.forcedImageGeneration = true
	}
	return result
}

func collectToolDefinitionCounts(value any, result *imageGenerationToolInspection, depth int) {
	if depth > 4 || value == nil {
		return
	}
	switch typed := value.(type) {
	case string:
		if typed == "image_generation" {
			result.imageToolCount++
		} else if strings.TrimSpace(typed) != "" {
			result.nonImageToolCount++
		}
	case []any:
		for _, item := range typed {
			collectToolDefinitionCounts(item, result, depth+1)
		}
	case map[string]any:
		typeValue, _ := typed["type"].(string)
		if typeValue == "image_generation" {
			result.imageToolCount++
			return
		}
		if strings.TrimSpace(typeValue) != "" {
			result.nonImageToolCount++
		}
	}
}

func toolChoiceForcesImageGeneration(value any) bool {
	if text, ok := value.(string); ok {
		return text == "image_generation"
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	typeValue, _ := object["type"].(string)
	return typeValue == "image_generation"
}

func toolChoiceTools(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object["tools"]
}

// requestBodyRequestsImageOutput mirrors requestBodyRequestsImageOutput:
// generationConfig responseModalities containing "image" or an image/* mime.
func requestBodyRequestsImageOutput(body any) bool {
	root, ok := body.(map[string]any)
	if !ok {
		return false
	}
	generationConfig, ok := objectValue(root["generationConfig"])
	if !ok {
		generationConfig, ok = objectValue(root["generation_config"])
	}
	if !ok {
		return false
	}
	modalities, _ := generationConfig["responseModalities"].([]any)
	if modalities == nil {
		modalities, _ = generationConfig["response_modalities"].([]any)
	}
	for _, entry := range modalities {
		if text, ok := entry.(string); ok && strings.ToLower(strings.TrimSpace(text)) == "image" {
			return true
		}
	}
	mime, _ := generationConfig["responseMimeType"].(string)
	if mime == "" {
		mime, _ = generationConfig["response_mime_type"].(string)
	}
	return strings.HasPrefix(strings.TrimSpace(mime), "image/")
}

// objectValue mirrors the Node objectValue helper for map results.
func objectValue(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}
