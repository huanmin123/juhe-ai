package gatewayhybrid

// Hybrid quality repair instruction, mirroring
// backend/src/modules/gateway/hybrid/quality-repair.service.ts.

// hybridQualityRepairInstructionMaxChars mirrors the Node constant.
const hybridQualityRepairInstructionMaxChars = 2000

// MutableGatewayJSONBody mirrors mutableGatewayJsonBody: request.body →
// gatewayParsedJsonBody → raw-body parse; a cloned JSON object or nil.
func MutableGatewayJSONBody(view *GatewayRequestView) *OrderedJSON {
	if body := view.bodyObject(); body != nil {
		return body.Clone()
	}
	if !view.hasRawBody() {
		return nil
	}
	parsed, err := ParseJSONOrdered(view.RawBody)
	if err != nil {
		return nil
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		return nil
	}
	return object.Clone()
}

// AppendHybridQualityRepairInstruction mirrors appendHybridQualityRepairInstruction:
// append the repair instruction as a chat `messages` user entry, a Responses
// `input` string suffix or a Responses `input` message entry. Returns the
// replacement body and whether the body changed.
func AppendHybridQualityRepairInstruction(view *GatewayRequestView, quality *HybridQualityInspectionOutcome) (*OrderedJSON, bool) {
	if quality == nil || quality.Result == nil {
		return nil, false
	}
	body := MutableGatewayJSONBody(view)
	if body == nil {
		return nil, false
	}
	instruction := BuildHybridQualityRepairInstruction(quality)
	if messages := OrderedChildArray(body, "messages"); messages != nil {
		entry := NewOrderedJSON()
		entry.Set("role", "user")
		entry.Set("content", instruction)
		updated := append(append([]any{}, messages...), entry)
		body.Set("messages", updated)
		return body, true
	}
	if input, ok := body.Get("input"); ok {
		if text, isString := input.(string); isString {
			body.Set("input", text+"\n\n"+instruction)
			return body, true
		}
		if entries, isArray := input.([]any); isArray {
			content := []any{inputTextInstruction(instruction)}
			updated := append(append([]any{}, entries...), inputMessageEntry(content))
			body.Set("input", updated)
			return body, true
		}
	}
	return nil, false
}

func inputTextInstruction(text string) *OrderedJSON {
	entry := NewOrderedJSON()
	entry.Set("type", "input_text")
	entry.Set("text", text)
	return entry
}

func inputMessageEntry(content []any) *OrderedJSON {
	entry := NewOrderedJSON()
	entry.Set("type", "message")
	entry.Set("role", "user")
	entry.Set("content", content)
	return entry
}

// BuildHybridQualityRepairInstruction mirrors buildHybridQualityRepairInstruction:
// the feedback lines join with newline and the text caps at 2000 UTF-16
// code units with a "..." suffix.
func BuildHybridQualityRepairInstruction(quality *HybridQualityInspectionOutcome) string {
	result := quality.Result
	lines := []string{
		"上一次回答没有通过混合路由质量评分。请基于原始用户需求重新给出最终答案，不要解释评分过程。",
		"质量评分反馈：",
	}
	if result != nil && result.HasFailureType {
		lines = append(lines, "- 问题类型："+result.FailureType)
	}
	if result != nil {
		// typeof result?.score === 'number' — score is always materialized.
		lines = append(lines, "- 质量分："+jsNumberText(result.Score))
	}
	if result != nil && result.Reason != nil && *result.Reason != "" {
		lines = append(lines, "- 失败原因："+*result.Reason)
	}
	if result != nil && result.RetryRecommendation != "" {
		lines = append(lines, "- 评分建议："+result.RetryRecommendation)
	}
	lines = append(lines, "修复要求：补齐遗漏内容，修正不符合要求的格式、字段、工具参数或文件内容；如果原请求要求严格 JSON、代码、补丁或结构化输出，本次只输出符合要求的最终结果。")
	text := joinLines(lines)
	if utf16Length(text) <= hybridQualityRepairInstructionMaxChars {
		return text
	}
	return truncateUTF16(text, hybridQualityRepairInstructionMaxChars) + "..."
}

func joinLines(lines []string) string {
	out := ""
	for index, line := range lines {
		if index > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}

// jsNumberText mirrors String(number) for the quality score rendering.
func jsNumberText(value float64) string {
	return nodeNumberText(value)
}
