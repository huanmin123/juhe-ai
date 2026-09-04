package gatewaydispatch

import "encoding/json"

// Codex builtin tool normalization, migrated from
// adapters/gpt-codex/builtin-tools.ts.

// NormalizeOpenAICodexBuiltinTools mirrors normalizeOpenAICodexBuiltinTools:
// web_search_preview* tool types collapse to web_search.
func NormalizeOpenAICodexBuiltinTools(body map[string]any) {
	if tools, ok := body["tools"].([]any); ok {
		normalizedTools := make([]any, len(tools))
		changed := false
		for index, tool := range tools {
			normalized := normalizeOpenAICodexBuiltinTool(tool)
			normalizedTools[index] = normalized
			if !jsonValueEqual(normalized, tool) {
				changed = true
			}
		}
		if changed {
			body["tools"] = normalizedTools
		}
	}

	if toolChoice, ok := body["tool_choice"].(map[string]any); ok {
		normalizedToolChoice := normalizeOpenAICodexBuiltinTool(toolChoice)
		toolChoiceTools, hasTools := toolChoice["tools"].([]any)
		if !hasTools {
			if !jsonValueEqual(normalizedToolChoice, toolChoice) {
				body["tool_choice"] = normalizedToolChoice
			}
			return
		}
		normalizedTools := make([]any, len(toolChoiceTools))
		toolsChanged := false
		for index, tool := range toolChoiceTools {
			normalized := normalizeOpenAICodexBuiltinTool(tool)
			normalizedTools[index] = normalized
			if !jsonValueEqual(normalized, tool) {
				toolsChanged = true
			}
		}
		if !jsonValueEqual(normalizedToolChoice, toolChoice) || toolsChanged {
			choiceObject, _ := normalizedToolChoice.(map[string]any)
			merged := make(map[string]any, len(choiceObject)+1)
			for key, value := range choiceObject {
				merged[key] = value
			}
			merged["tools"] = normalizedTools
			body["tool_choice"] = merged
		}
	}
}

func normalizeOpenAICodexBuiltinTool(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	current, _ := object["type"].(string)
	if current == "web_search_preview" || current == "web_search_preview_2025_03_11" {
		replacement := make(map[string]any, len(object))
		for key, item := range object {
			replacement[key] = item
		}
		replacement["type"] = "web_search"
		return replacement
	}
	return value
}

// jsonValueEqual compares two decoded JSON values structurally.
func jsonValueEqual(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftRaw) == string(rightRaw)
}
