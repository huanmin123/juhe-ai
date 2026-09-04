package gatewaybody

import "strings"

// Image generation tool inspection, mirroring the subset of
// request/image-generation-tools.ts consumed by body.ts: inspection, hint /
// forced predicates and the auto image-generation tool downgrade.

// ImageGenerationToolInspection mirrors ImageGenerationToolInspection.
type ImageGenerationToolInspection struct {
	ImageToolCount        int
	NonImageToolCount     int
	ForcedImageGeneration bool
}

// ImageGenerationToolDowngradeReason mirrors the downgrade reason union.
type ImageGenerationToolDowngradeReason string

const (
	DowngradeReasonAutoImageGenerationToolRemoved ImageGenerationToolDowngradeReason = "auto_image_generation_tool_removed"
	DowngradeReasonNotJSONObject                  ImageGenerationToolDowngradeReason = "not_json_object"
	DowngradeReasonNoAutoImageGenerationTool      ImageGenerationToolDowngradeReason = "no_auto_image_generation_tool"
	DowngradeReasonForcedImageGenerationTool      ImageGenerationToolDowngradeReason = "forced_image_generation_tool"
	DowngradeReasonInvalidJSON                    ImageGenerationToolDowngradeReason = "invalid_json"
	DowngradeReasonImageEndpointOrModel           ImageGenerationToolDowngradeReason = "image_endpoint_or_model"
	DowngradeReasonJSONWorkerOverloaded           ImageGenerationToolDowngradeReason = "json_worker_overloaded"
)

// ImageGenerationToolDowngradeResult mirrors GatewayImageGenerationToolDowngradeResult.
type ImageGenerationToolDowngradeResult struct {
	Downgraded       bool
	RemovedToolCount int
	Reason           ImageGenerationToolDowngradeReason
}

// RequestBodyHasImageGenerationHint mirrors requestBodyHasImageGenerationHint.
func RequestBodyHasImageGenerationHint(body any) bool {
	inspection := InspectImageGenerationTools(body)
	return inspection.ImageToolCount > 0 || inspection.ForcedImageGeneration
}

// RequestBodyForcesImageGeneration mirrors requestBodyForcesImageGeneration.
func RequestBodyForcesImageGeneration(body any) bool {
	return InspectImageGenerationTools(body).ForcedImageGeneration
}

// InspectImageGenerationTools mirrors inspectImageGenerationTools. Parsed
// bodies arrive as map[string]any from encoding/json, so the Node object
// checks map to map assertions (arrays were never image-tool carriers).
func InspectImageGenerationTools(value any) ImageGenerationToolInspection {
	result := ImageGenerationToolInspection{}
	body, ok := value.(map[string]any)
	if !ok {
		return result
	}
	if text, _ := body["type"].(string); text == "image_generation" {
		result.ForcedImageGeneration = true
	}
	collectToolDefinitionCounts(body["tools"], &result, 0)
	collectToolDefinitionCounts(toolChoiceTools(body["tool_choice"]), &result, 0)
	if toolChoiceForcesImageGeneration(body["tool_choice"]) {
		result.ForcedImageGeneration = true
	}
	if choice, _ := body["tool_choice"].(string); choice == "required" &&
		result.ImageToolCount > 0 && result.NonImageToolCount == 0 {
		result.ForcedImageGeneration = true
	}
	return result
}

// DowngradeAutoImageGenerationToolsInBody mirrors downgradeAutoImageGenerationToolsInBody.
func DowngradeAutoImageGenerationToolsInBody(body map[string]any) (ImageGenerationToolDowngradeResult, map[string]any) {
	if body == nil {
		return ImageGenerationToolDowngradeResult{Reason: DowngradeReasonNotJSONObject}, nil
	}
	inspection := InspectImageGenerationTools(body)
	if inspection.ForcedImageGeneration {
		return ImageGenerationToolDowngradeResult{Reason: DowngradeReasonForcedImageGenerationTool}, nil
	}
	if inspection.ImageToolCount <= 0 {
		return ImageGenerationToolDowngradeResult{Reason: DowngradeReasonNoAutoImageGenerationTool}, nil
	}
	nextBody := copyJSONObject(body)
	removedToolCount := removeImageGenerationToolDefinitions(nextBody)
	if removedToolCount <= 0 {
		return ImageGenerationToolDowngradeResult{Reason: DowngradeReasonNoAutoImageGenerationTool}, nil
	}
	return ImageGenerationToolDowngradeResult{
		Downgraded:       true,
		RemovedToolCount: removedToolCount,
		Reason:           DowngradeReasonAutoImageGenerationToolRemoved,
	}, nextBody
}

func collectToolDefinitionCounts(value any, result *ImageGenerationToolInspection, depth int) {
	if depth > 4 || value == nil {
		return
	}
	switch typed := value.(type) {
	case string:
		if typed == "image_generation" {
			result.ImageToolCount++
		} else if strings.TrimSpace(typed) != "" {
			result.NonImageToolCount++
		}
	case []any:
		for _, item := range typed {
			collectToolDefinitionCounts(item, result, depth+1)
		}
	case map[string]any:
		typeValue, _ := typed["type"].(string)
		if typeValue == "image_generation" {
			result.ImageToolCount++
			return
		}
		if strings.TrimSpace(typeValue) != "" {
			result.NonImageToolCount++
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

func removeImageGenerationToolDefinitions(body map[string]any) int {
	removedToolCount := removeImageGenerationToolArray(body, "tools")
	if choice, ok := parsedObjectValue(body, "tool_choice"); ok {
		toolChoice := copyJSONObject(choice)
		removedChoiceTools := removeImageGenerationToolArray(toolChoice, "tools")
		if removedChoiceTools > 0 {
			body["tool_choice"] = toolChoice
			removedToolCount += removedChoiceTools
		}
	}
	return removedToolCount
}

func removeImageGenerationToolArray(owner map[string]any, key string) int {
	tools, ok := owner[key].([]any)
	if !ok {
		return 0
	}
	nextTools := make([]any, 0, len(tools))
	for _, tool := range tools {
		if !isImageGenerationToolDefinition(tool) {
			nextTools = append(nextTools, tool)
		}
	}
	removedToolCount := len(tools) - len(nextTools)
	if removedToolCount <= 0 {
		return 0
	}
	if len(nextTools) > 0 {
		owner[key] = nextTools
	} else {
		delete(owner, key)
	}
	return removedToolCount
}

func isImageGenerationToolDefinition(value any) bool {
	if text, ok := value.(string); ok {
		return text == "image_generation"
	}
	object, ok := value.(map[string]any)
	return ok && func() bool {
		typeValue, _ := object["type"].(string)
		return typeValue == "image_generation"
	}()
}

// copyJSONObject performs the shallow { ...body } spread semantics.
func copyJSONObject(body map[string]any) map[string]any {
	next := make(map[string]any, len(body))
	for key, value := range body {
		next[key] = value
	}
	return next
}
