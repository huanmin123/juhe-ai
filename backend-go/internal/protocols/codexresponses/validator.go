package codexresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

var ErrInvalidJSON = errors.New("Codex Responses JSON 无效")

type Issue struct {
	Code        string
	Message     string
	Path        []any
	Provenance  Provenance
	ItemType    string
	OutputIndex int
	RepairLevel RepairLevel
}

type ValidationResult struct {
	Revision          string
	Provenance        Provenance
	Outcome           Outcome
	Issues            []Issue
	OmittedIssueCount int
}

func ValidateJSON(raw []byte, provenance Provenance) (ValidationResult, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ValidationResult{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ValidationResult{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidJSON)
		}
		return ValidationResult{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	response, ok := value.(map[string]any)
	if !ok {
		return ValidationResult{}, ErrInvalidJSON
	}
	return Validate(response, provenance), nil
}

func Validate(response map[string]any, provenance Provenance) ValidationResult {
	issues := make([]Issue, 0)
	collection, invalid := responseItems(response)
	if invalid != nil {
		issues = append(issues, newIssue(provenance, invalid.code, invalid.message, invalid.path, -1, "", RepairR2))
		return finish(provenance, issues)
	}
	if collection == nil {
		return finish(provenance, nil)
	}

	registry := NewRegistry()
	seenIDs := make(map[string]int)
	calls := make(map[string]string)
	toolOutputs := make([]toolOutput, 0)
	for index, rawItem := range collection.items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			issues = append(issues, newIssue(provenance, "item_not_object", "Responses item 必须是对象", []any{collection.field, index}, index, "", RepairR2))
			continue
		}
		itemType, ok := nonEmptyString(item["type"])
		if !ok {
			issues = append(issues, newIssue(provenance, "item_type_missing", "Responses item 缺少非空 type", []any{collection.field, index, "type"}, index, "", RepairR2))
			continue
		}
		contract, known := registry.Item(itemType)
		if !known {
			issues = append(issues, newIssue(provenance, "unknown_item_type", "未知 Codex Responses item type: "+itemType, []any{collection.field, index, "type"}, index, itemType, RepairNone))
			continue
		}
		hasRepairableIDIssue := false
		if rawID, present := item["id"]; present {
			if contract.Prefix == "" {
				issues = append(issues, newIssue(provenance, "item_id_forbidden", itemType+" 不允许携带 item ID", []any{collection.field, index, "id"}, index, itemType, RepairR2))
				continue
			}
			if rawID == nil && provenance != ProvenanceRequestHistory {
				// A null response id is equivalent to a non-persisted response item.
			} else {
				id, valid := nonEmptyString(rawID)
				if !valid {
					issues = append(issues, newIssue(provenance, "item_id_invalid", itemType+" 的 item ID 必须是非空字符串", []any{collection.field, index, "id"}, index, itemType, RepairR0))
					hasRepairableIDIssue = true
				} else {
					if !isExpectedID(id, contract.Prefix) {
						issues = append(issues, newIssue(provenance, "item_id_prefix_mismatch", itemType+" 的 item ID 前缀与 contract 不一致", []any{collection.field, index, "id"}, index, itemType, RepairR0))
						hasRepairableIDIssue = true
					}
					if previous, duplicate := seenIDs[id]; duplicate {
						_ = previous
						issues = append(issues, newIssue(provenance, "duplicate_item_identity", "同一 Responses 文档中出现重复 item ID", []any{collection.field, index, "id"}, index, itemType, RepairR2))
					} else {
						seenIDs[id] = index
					}
				}
			}
		}
		if !(provenance == ProvenanceRequestHistory && hasRepairableIDIssue) {
			for _, fieldIssue := range validateFields(item, contract) {
				issues = append(issues, newIssue(provenance, fieldIssue.code, itemType+"."+fieldIssue.field+" 不满足 Codex Responses contract", []any{collection.field, index, fieldIssue.field}, index, itemType, RepairR2))
			}
		}
		callID, hasCallID := stringValue(item["call_id"])
		if hasCallID {
			if _, isCall := toolCallOutputType[itemType]; isCall {
				if previousType, exists := calls[callID]; exists {
					code := "tool_call_type_mismatch"
					message := fmt.Sprintf("%s 同时被声明为 %s 和 %s", callID, previousType, itemType)
					if previousType == itemType {
						code = "duplicate_tool_call_identity"
						message = fmt.Sprintf("%s 在同一 Responses 文档中出现重复工具调用", callID)
					}
					issues = append(issues, newIssue(provenance, code, message, []any{collection.field, index, "call_id"}, index, itemType, RepairR2))
				} else {
					calls[callID] = itemType
				}
			}
			if _, isOutput := toolOutputTypes[itemType]; isOutput && !(itemType == "tool_search_output" && stringValueEquals(item["execution"], "server")) {
				toolOutputs = append(toolOutputs, toolOutput{index: index, itemType: itemType, callID: callID})
			}
		}
	}
	externalHistory := nonEmptyStringValue(response["previous_response_id"])
	completedOutputs := make(map[string]struct{})
	for _, output := range toolOutputs {
		if _, duplicate := completedOutputs[output.callID]; duplicate {
			issues = append(issues, newIssue(provenance, "duplicate_tool_output", output.callID+" 在同一 Responses 文档中出现重复工具输出", []any{collection.field, output.index, "call_id"}, output.index, output.itemType, RepairR2))
			continue
		}
		completedOutputs[output.callID] = struct{}{}
		callType, exists := calls[output.callID]
		if exists {
			if toolCallOutputType[callType] != output.itemType {
				issues = append(issues, newIssue(provenance, "tool_call_type_mismatch", callType+" 的 call_id 不能由 "+output.itemType+" 完成", []any{collection.field, output.index, "call_id"}, output.index, output.itemType, RepairR2))
			}
		} else if externalHistory == "" {
			issues = append(issues, newIssue(provenance, "orphan_tool_output", output.itemType+" 引用了当前完整历史中不存在的 call_id", []any{collection.field, output.index, "call_id"}, output.index, output.itemType, RepairR2))
		}
	}
	return finish(provenance, issues)
}

type responseCollection struct {
	field string
	items []any
}
type invalidCollection struct {
	code, message string
	path          []any
}
type toolOutput struct {
	index            int
	itemType, callID string
}

func responseItems(response map[string]any) (*responseCollection, *invalidCollection) {
	input, hasInput := response["input"]
	output, hasOutput := response["output"]
	if hasInput && hasOutput {
		return nil, &invalidCollection{"response_item_collections_ambiguous", "同一 Codex Responses 文档不能同时包含 input 与 output item 集合", nil}
	}
	if !hasInput && !hasOutput {
		return nil, nil
	}
	field, value := "input", input
	if hasOutput {
		field, value = "output", output
	}
	items, ok := value.([]any)
	if !ok {
		return nil, &invalidCollection{"response_item_collection_invalid", field + " 必须是 Responses item 数组", []any{field}}
	}
	return &responseCollection{field: field, items: items}, nil
}

func finish(provenance Provenance, issues []Issue) ValidationResult {
	outcome := OutcomeClean
	for _, value := range issues {
		if value.RepairLevel == RepairR2 {
			outcome = OutcomeBlocked
			break
		}
		if value.RepairLevel == RepairR0 {
			outcome = OutcomeRepairable
		}
		if value.RepairLevel == RepairNone && outcome == OutcomeClean {
			outcome = OutcomeObservedUnknown
		}
	}
	result := ValidationResult{Revision: Revision, Provenance: provenance, Outcome: outcome}
	if len(issues) > DiagnosticLimit {
		result.OmittedIssueCount = len(issues) - DiagnosticLimit
		issues = issues[:DiagnosticLimit]
	}
	result.Issues = append([]Issue(nil), issues...)
	return result
}

func newIssue(provenance Provenance, code, message string, path []any, index int, itemType string, level RepairLevel) Issue {
	return Issue{Code: code, Message: message, Path: append([]any(nil), path...), Provenance: provenance, OutputIndex: index, ItemType: itemType, RepairLevel: level}
}

func validateFields(item map[string]any, contract ItemContract) []struct{ code, field string } {
	issues := make([]struct{ code, field string }, 0)
	for _, field := range contract.RequiredFields {
		if !validField(item, field, true) {
			issues = append(issues, struct{ code, field string }{"item_required_field_invalid", field.Name})
		}
	}
	for _, field := range contract.OptionalFields {
		if !validField(item, field, false) {
			issues = append(issues, struct{ code, field string }{"item_optional_field_invalid", field.Name})
		}
	}
	return issues
}

func validField(item map[string]any, field Field, required bool) bool {
	value, present := item[field.Name]
	if !present {
		return !required
	}
	if value == nil {
		return field.Nullable
	}
	switch field.Kind {
	case FieldPresent:
		return true
	case FieldString:
		_, ok := value.(string)
		return ok
	case FieldArray:
		_, ok := value.([]any)
		return ok
	case FieldObject:
		_, ok := value.(map[string]any)
		return ok
	case FieldEnum:
		candidate, ok := value.(string)
		if !ok {
			return false
		}
		for _, allowed := range field.Values {
			if candidate == allowed {
				return true
			}
		}
		return false
	case FieldFunctionOutput:
		return validFunctionOutput(value)
	case FieldLocalShellAction:
		return validLocalShellAction(value)
	default:
		return false
	}
}

func validFunctionOutput(value any) bool {
	if _, ok := value.(string); ok {
		return true
	}
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		switch item["type"] {
		case "input_text":
			if _, ok := item["text"].(string); !ok {
				return false
			}
		case "encrypted_content":
			if _, ok := item["encrypted_content"].(string); !ok {
				return false
			}
		case "input_image":
			if _, ok := item["image_url"].(string); !ok {
				return false
			}
			if detail, exists := item["detail"]; exists && detail != nil {
				candidate, ok := detail.(string)
				if !ok || !contains([]string{"auto", "low", "high", "original"}, candidate) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func validLocalShellAction(value any) bool {
	action, ok := value.(map[string]any)
	if !ok || action["type"] != "exec" {
		return false
	}
	command, ok := action["command"].([]any)
	if !ok {
		return false
	}
	for _, part := range command {
		if _, ok := part.(string); !ok {
			return false
		}
	}
	for _, name := range []string{"working_directory", "user"} {
		if value, exists := action[name]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if value, exists := action["timeout_ms"]; exists && value != nil {
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseInt(string(number), 10, 64)
		if err != nil || parsed < 0 || parsed > maxSafeJSONInteger {
			return false
		}
	}
	if value, exists := action["env"]; exists && value != nil {
		env, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, entry := range env {
			if _, ok := entry.(string); !ok {
				return false
			}
		}
	}
	return true
}

const maxSafeJSONInteger int64 = 9007199254740991

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func nonEmptyString(value any) (string, bool) {
	candidate, ok := value.(string)
	return candidate, ok && candidate != ""
}
func nonEmptyStringValue(value any) string { candidate, _ := nonEmptyString(value); return candidate }
func stringValue(value any) (string, bool) { candidate, ok := value.(string); return candidate, ok }
func stringValueEquals(value any, target string) bool {
	candidate, ok := value.(string)
	return ok && candidate == target
}

var toolCallOutputType = map[string]string{"function_call": "function_call_output", "local_shell_call": "function_call_output", "custom_tool_call": "custom_tool_call_output", "tool_search_call": "tool_search_output"}
var toolOutputTypes = map[string]struct{}{"function_call_output": {}, "custom_tool_call_output": {}, "tool_search_output": {}}
