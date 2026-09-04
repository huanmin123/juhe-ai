package gatewaycodex

// Port of codex-responses/request-history-types.ts +
// request-history-sanitizer.ts.

// ResponsesPersistenceScope mirrors ResponsesPersistenceScope.
type ResponsesPersistenceScope = string

// Responses persistence scopes.
const (
	PersistenceScopeNone                = "none"
	PersistenceScopeAccount             = "account"
	PersistenceScopeUpstreamBucket      = "upstream_bucket"
	PersistenceScopeProviderGlobal      = "provider_global"
	PersistenceScopeWebsocketConnection = "websocket_connection"
)

// CodexHistorySanitizerContext mirrors CodexHistorySanitizerContext.
type CodexHistorySanitizerContext struct {
	Store                  bool
	SourceScopeKey         string
	TargetScopeKey         string
	TargetPersistenceScope ResponsesPersistenceScope
}

// CodexHistorySanitizerResult mirrors CodexHistorySanitizerResult.
type CodexHistorySanitizerResult struct {
	Items            []any
	Changed          bool
	RemovedIDCount   int
	DroppedItemCount int
	IssueCodes       []string
}

// SanitizeCodexResponseHistoryItems mirrors sanitizeCodexResponseHistoryItems.
func SanitizeCodexResponseHistoryItems(items []any, context CodexHistorySanitizerContext) CodexHistorySanitizerResult {
	var output []any
	sanitized := false
	removedIDCount := 0
	droppedItemCount := 0
	issueCodes := []string{}

	for index := 0; index < len(items); index++ {
		item := items[index]
		decision := itemIDRemovalDecision(item, context)
		if decision == nil {
			continue
		}
		outputIndex := index - droppedItemCount
		if output == nil {
			output = append([]any(nil), items...)
			sanitized = true
		}
		if !IsReplayableCodexHistoryItem(item) {
			output = append(output[:outputIndex], output[outputIndex+1:]...)
			droppedItemCount++
			issueCodes = pushIssueCode(issueCodes, decision.issueCode)
			issueCodes = pushIssueCode(issueCodes, "unrecoverable_item_dropped")
			continue
		}
		copy := cloneJSONMap(decision.item)
		delete(copy, "id")
		output[outputIndex] = copy
		removedIDCount++
		issueCodes = pushIssueCode(issueCodes, decision.issueCode)
	}

	if output == nil {
		output = items
	}
	return CodexHistorySanitizerResult{
		Items: output,
		// Node: changed = output !== undefined — output is allocated exactly
		// when a decision fired for at least one item.
		Changed:          sanitized,
		RemovedIDCount:   removedIDCount,
		DroppedItemCount: droppedItemCount,
		IssueCodes:       issueCodes,
	}
}

func pushIssueCode(issueCodes []string, issueCode string) []string {
	for _, existing := range issueCodes {
		if existing == issueCode {
			return issueCodes
		}
	}
	return append(issueCodes, issueCode)
}

type itemIDRemoval struct {
	item      map[string]any
	itemType  string
	issueCode string
}

func itemIDRemovalDecision(value any, context CodexHistorySanitizerContext) *itemIDRemoval {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	itemType := stringValue(record["type"])
	expectedPrefix := ""
	if itemType != "" {
		if item, found := defaultCodexResponsesContractRegistry.Item(itemType); found {
			expectedPrefix = item.Prefix
		}
	}
	if itemType == "" || expectedPrefix == "" {
		return nil
	}
	rawID, hasID := record["id"]
	if !hasID {
		return nil
	}

	id := stringValue(rawID)
	if id == "" {
		return &itemIDRemoval{item: record, itemType: itemType, issueCode: "invalid_item_id"}
	}

	if !hasNonEmptyPrefixAndSuffix(id) {
		return &itemIDRemoval{item: record, itemType: itemType, issueCode: "legacy_item_id"}
	}
	if !startsWith(id, expectedPrefix+"_") {
		return &itemIDRemoval{item: record, itemType: itemType, issueCode: "item_id_prefix_mismatch"}
	}
	if context.TargetPersistenceScope == PersistenceScopeNone && !context.Store {
		return &itemIDRemoval{item: record, itemType: itemType, issueCode: "unpersisted_item_reference"}
	}
	if context.SourceScopeKey != "" && context.TargetScopeKey != "" && context.SourceScopeKey != context.TargetScopeKey {
		return &itemIDRemoval{item: record, itemType: itemType, issueCode: "cross_scope_item_reference"}
	}
	return nil
}

// IsReplayableCodexHistoryItem mirrors isReplayableCodexHistoryItem.
func IsReplayableCodexHistoryItem(value any) bool {
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch stringValue(record["type"]) {
	case "additional_tools":
		_, roleIsString := record["role"].(string)
		_, toolsIsArray := record["tools"].([]any)
		return roleIsString && toolsIsArray
	case "message":
		_, roleIsString := record["role"].(string)
		_, contentIsArray := record["content"].([]any)
		return roleIsString && contentIsArray
	case "agent_message":
		_, authorIsString := record["author"].(string)
		_, recipientIsString := record["recipient"].(string)
		_, contentIsArray := record["content"].([]any)
		return authorIsString && recipientIsString && contentIsArray
	case "reasoning":
		return hasArrayContent(record["summary"]) ||
			hasArrayContent(record["content"]) ||
			func() bool {
				encrypted, isString := record["encrypted_content"].(string)
				return isString && len(encrypted) > 0
			}()
	case "local_shell_call":
		_, actionIsObject := record["action"].(map[string]any)
		return actionIsObject
	case "function_call":
		_, nameIsString := record["name"].(string)
		_, argumentsIsString := record["arguments"].(string)
		_, callIDIsString := record["call_id"].(string)
		return nameIsString && argumentsIsString && callIDIsString
	case "tool_search_call":
		_, hasArguments := record["arguments"]
		_, executionIsString := record["execution"].(string)
		return hasArguments && executionIsString
	case "function_call_output", "custom_tool_call_output":
		_, callIDIsString := record["call_id"].(string)
		_, hasOutput := record["output"]
		return callIDIsString && hasOutput
	case "custom_tool_call":
		_, nameIsString := record["name"].(string)
		_, inputIsString := record["input"].(string)
		_, callIDIsString := record["call_id"].(string)
		return nameIsString && inputIsString && callIDIsString
	case "tool_search_output":
		_, executionIsString := record["execution"].(string)
		_, toolsIsArray := record["tools"].([]any)
		return executionIsString && toolsIsArray
	case "web_search_call":
		_, hasAction := record["action"]
		_, statusIsString := record["status"].(string)
		return hasAction || statusIsString
	case "image_generation_call":
		_, statusIsString := record["status"].(string)
		_, resultIsString := record["result"].(string)
		return statusIsString && resultIsString
	case "compaction", "compaction_summary":
		_, encryptedIsString := record["encrypted_content"].(string)
		return encryptedIsString
	case "context_compaction":
		_, encryptedIsString := record["encrypted_content"].(string)
		return encryptedIsString
	default:
		return false
	}
}

func hasNonEmptyPrefixAndSuffix(value string) bool {
	separator := indexOfByte(value, '_')
	return separator > 0 && separator < len(value)-1
}

func indexOfByte(value string, needle byte) int {
	for i := 0; i < len(value); i++ {
		if value[i] == needle {
			return i
		}
	}
	return -1
}

func startsWith(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

func hasArrayContent(value any) bool {
	array, ok := value.([]any)
	return ok && len(array) > 0
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok || len(text) == 0 {
		return ""
	}
	return text
}

func cloneJSONMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneJSONValue(value)
	}
	return output
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		output := make([]any, len(typed))
		for index, item := range typed {
			output[index] = cloneJSONValue(item)
		}
		return output
	default:
		return value
	}
}
