package codexresponses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

const StreamDiagnosticLimit = DiagnosticLimit

type StreamInput struct {
	ResponseResourceID string
	Event              map[string]any
	Comment            bool
}

type StreamStage string

const (
	StreamStageAdded StreamStage = "added"
	StreamStageDelta StreamStage = "delta"
	StreamStageDone  StreamStage = "done"
)

type StreamIdentity struct {
	ItemID         string
	UpstreamItemID string
	ClientItemID   string
	ItemType       string
	CallID         string
	OutputIndex    int
	Stage          StreamStage
}

type StreamRepair struct {
	OutputIndex  int
	ItemType     string
	Field        string
	ClientItemID string
}

type StreamEventResult struct {
	Revision      string
	Outcome       Outcome
	Issues        []Issue
	Repairs       []StreamRepair
	EventCategory string
}

type StreamSnapshot struct {
	Revision          string
	IdentityCount     int
	ItemIDOwnerCount  int
	Diagnostics       []Issue
	OmittedIssueCount int
}

type StreamState struct {
	provenance      Provenance
	registry        Registry
	repairItemIDs   bool
	createItemID    func(prefix, itemType string, sequence, outputIndex int) string
	identities      map[string]StreamIdentity
	itemIDOwners    map[string]string
	observedItemIDs map[string]struct{}
	clientItemIDs   map[string]struct{}
	diagnostics     []Issue
	omitted         int
	completed       bool
	standaloneSeq   int
	repairSeq       int
}

func NewStreamState(provenance Provenance, repairItemIDs bool, createItemID func(prefix, itemType string, sequence, outputIndex int) string) *StreamState {
	return &StreamState{
		provenance:      provenance,
		registry:        NewRegistry(),
		repairItemIDs:   repairItemIDs,
		createItemID:    createItemID,
		identities:      make(map[string]StreamIdentity),
		itemIDOwners:    make(map[string]string),
		observedItemIDs: make(map[string]struct{}),
		clientItemIDs:   make(map[string]struct{}),
		repairSeq:       1,
	}
}

func (s *StreamState) Consume(input StreamInput, allowNewRepair bool) StreamEventResult {
	if input.Comment {
		return StreamEventResult{Revision: Revision, Outcome: OutcomeClean, EventCategory: "sse_comment"}
	}
	if input.Event == nil {
		return s.result([]Issue{s.issue("stream_event_not_object", "Responses SSE event 必须是对象", []any{"event"}, -1, "", RepairR2)})
	}
	if s.completed {
		itemType, _ := stringValue(input.Event["type"])
		return s.result([]Issue{s.issue("event_after_response_completed", "response.completed 后不得继续发送协议事件", []any{"event", "type"}, -1, itemType, RepairR2)})
	}
	eventType, ok := nonEmptyString(input.Event["type"])
	if !ok {
		return s.result([]Issue{s.issue("stream_event_type_missing", "Responses SSE event 缺少非空 type", []any{"event", "type"}, -1, "", RepairR2)})
	}
	if eventType == "response.completed" {
		s.completed = true
		return s.consumeCompleted(input.ResponseResourceID, input.Event, allowNewRepair)
	}
	stage, knownStage := streamEventStage(eventType)
	if !knownStage {
		return s.result(nil)
	}
	return s.consumeItem(input.ResponseResourceID, eventType, input.Event, stage, allowNewRepair)
}

func (s *StreamState) CanTransparentRetry(semanticCommitted bool) bool { return !semanticCommitted }

func (s *StreamState) IdentityFor(responseResourceID string, outputIndex int) (StreamIdentity, bool) {
	identity, ok := s.identities[streamItemKey(responseResourceID, outputIndex)]
	return identity, ok
}

func (s *StreamState) Snapshot() StreamSnapshot {
	return StreamSnapshot{Revision: Revision, IdentityCount: len(s.identities), ItemIDOwnerCount: len(s.itemIDOwners), Diagnostics: cloneIssues(s.diagnostics), OmittedIssueCount: s.omitted}
}

func (s *StreamState) Dispose() {
	s.identities = make(map[string]StreamIdentity)
	s.itemIDOwners = make(map[string]string)
	s.observedItemIDs = make(map[string]struct{})
	s.clientItemIDs = make(map[string]struct{})
	s.diagnostics = nil
	s.omitted = 0
	s.completed = false
	s.standaloneSeq = 0
	s.repairSeq = 1
	s.registry = NewRegistry()
}

func (s *StreamState) consumeItem(responseResourceID, eventType string, event map[string]any, stage StreamStage, allowNewRepair bool) StreamEventResult {
	outputIndex, hasOutputIndex := nonNegativeInteger(event["output_index"])
	if _, present := event["output_index"]; present && !hasOutputIndex {
		return s.result([]Issue{s.issue("event_output_index_invalid", "Responses item event 的 output_index 必须是非负整数", []any{"event", "output_index"}, -1, "", RepairR2)})
	}
	if !hasOutputIndex {
		outputIndex = -1
	}
	item, hasItem := objectValue(event["item"])
	if (stage == StreamStageAdded || stage == StreamStageDone) && !hasItem {
		return s.result([]Issue{s.issue("event_item_missing", "output_item added/done 必须包含 item 对象", []any{"event", "item"}, outputIndex, "", RepairR2)})
	}
	expectedType := deltaItemType(eventType)
	itemType := firstNonEmptyString(item["type"], event["item_type"], expectedType)
	itemID, itemIDPresent := firstStringPresent(item["id"], event["item_id"])
	callID, callIDPresent := firstStringPresent(item["call_id"], event["call_id"])
	scope := streamScope(responseResourceID)
	ownerKey := ""
	if itemID != "" {
		ownerKey = s.itemIDOwners[streamIDKey(scope, itemID)]
		if ownerKey == "" && responseResourceID != "" {
			ownerKey = s.itemIDOwners[streamIDKey(streamScope(""), itemID)]
		}
	}
	internalKey := ""
	if hasOutputIndex {
		internalKey = streamItemKey(responseResourceID, outputIndex)
	} else if stage == StreamStageDelta && ownerKey != "" {
		internalKey = ownerKey
	} else {
		s.standaloneSeq++
		internalKey = fmt.Sprintf("%d:%s:standalone:%d", len(scope), scope, s.standaloneSeq)
	}
	previousKey := internalKey
	previous, hasPrevious := s.identities[previousKey]
	if !hasPrevious && responseResourceID != "" && hasOutputIndex {
		fallbackKey := streamItemKey("", outputIndex)
		if fallback, ok := s.identities[fallbackKey]; ok {
			previous, hasPrevious, previousKey = fallback, true, fallbackKey
		}
	}
	targetKey := internalKey
	if hasPrevious && responseResourceID != "" && previous.OutputIndex >= 0 {
		targetKey = streamItemKey(responseResourceID, previous.OutputIndex)
	}
	issues := make([]Issue, 0)
	if hasPrevious {
		if stage == StreamStageDelta && expectedType != "" && previous.ItemType != "" && previous.ItemType != identityToken(expectedType) {
			issues = append(issues, s.issue("event_delta_type_mismatch", eventType+" 不能用于 "+previous.ItemType, []any{"event", "type"}, outputIndex, previous.ItemType, RepairR2))
		}
		if previous.Stage == StreamStageDone || (stage == StreamStageAdded && previous.Stage == StreamStageAdded) {
			issues = append(issues, s.issue("event_stage_inconsistent", "同一 output identity 的事件阶段不一致", []any{"event", "type"}, outputIndex, itemType, RepairR2))
		}
		if previous.ItemID != "" && itemID != "" && previous.ItemID != identityToken(itemID) {
			issues = append(issues, s.issue("event_item_id_inconsistent", "同一 Responses output identity 的 item ID 在事件间发生变化", streamItemPath(stage, "id"), outputIndex, itemType, RepairR2))
		}
		if previous.ItemType != "" && itemType != "" && previous.ItemType != identityToken(itemType) {
			issues = append(issues, s.issue("event_item_type_inconsistent", "同一 Responses output identity 的 item type 在事件间发生变化", streamItemPath(stage, "type"), outputIndex, itemType, RepairR2))
		}
		if previous.CallID != "" && callID != "" && previous.CallID != identityToken(callID) {
			issues = append(issues, s.issue("event_call_id_inconsistent", "同一 Responses output identity 的 call_id 在事件间发生变化", streamItemPath(stage, "call_id"), outputIndex, itemType, RepairR2))
		}
	}
	if stage == StreamStageDelta && expectedType == "" {
		issues = append(issues, s.issue("unknown_delta_event_type", "Responses stream 出现 registry 未识别的 delta event", []any{"event", "type"}, outputIndex, itemType, RepairNone))
	}
	effectiveType := itemType
	if effectiveType == "" && hasPrevious {
		effectiveType = previous.ItemType
	}
	contract, known := s.registry.Item(effectiveType)
	if effectiveType != "" && !known {
		issues = append(issues, s.issue("unknown_item_type", "Responses stream 出现 registry 未识别的 item type", streamItemPath(stage, "type"), outputIndex, effectiveType, RepairNone))
	} else if known {
		if !contains(contract.EventStages, string(stage)) {
			issues = append(issues, s.issue("event_stage_invalid", effectiveType+" 不允许 "+string(stage)+" 阶段", []any{"event", "type"}, outputIndex, effectiveType, RepairR2))
		}
		if hasItem && stage != StreamStageDelta {
			for _, fieldIssue := range validateFields(item, contract) {
				issues = append(issues, s.issue(fieldIssue.code, effectiveType+"."+fieldIssue.field+" 不满足 Codex Responses contract", append([]any{"event", "item"}, fieldIssue.field), outputIndex, effectiveType, RepairR2))
			}
		}
		rawID, present := item["id"]
		if stage == StreamStageDelta {
			rawID, present = event["item_id"]
		}
		if present {
			if contract.Prefix == "" {
				issues = append(issues, s.issue("item_id_forbidden", effectiveType+" 不允许携带 item ID", streamItemPath(stage, "id"), outputIndex, effectiveType, RepairR2))
			} else if rawID != nil {
				if candidate, valid := nonEmptyString(rawID); !valid {
					issues = append(issues, s.issue("item_id_invalid", effectiveType+" 的 item ID 必须是字符串或 null", streamItemPath(stage, "id"), outputIndex, effectiveType, RepairR0))
				} else if !isExpectedID(candidate, contract.Prefix) {
					issues = append(issues, s.issue("item_id_prefix_mismatch", effectiveType+" 的 item ID 前缀与 contract 不一致", streamItemPath(stage, "id"), outputIndex, effectiveType, RepairR0))
				}
			}
		}
	}
	if itemID != "" {
		itemToken := identityToken(itemID)
		s.observedItemIDs[itemToken] = struct{}{}
		if _, reserved := s.clientItemIDs[itemToken]; reserved {
			issues = append(issues, s.issue("generated_item_identity_collision", "上游 item ID 与已生成的 client item ID 冲突", streamItemPath(stage, "id"), outputIndex, effectiveType, RepairR2))
		}
		previousOwner := s.itemIDOwners[streamIDKey(scope, itemID)]
		if previousOwner == "" && responseResourceID != "" {
			previousOwner = s.itemIDOwners[streamIDKey(streamScope(""), itemID)]
		}
		if previousOwner != "" && previousOwner != internalKey && previousOwner != previousKey {
			issues = append(issues, s.issue("duplicate_item_identity", "同一 Responses 流的多个 output index 使用了重复 item ID", streamItemPath(stage, "id"), outputIndex, effectiveType, RepairR2))
		}
	}
	clientItemID := ""
	if hasPrevious {
		clientItemID = previous.ClientItemID
	}
	if known && !hasRepairLevel(issues, RepairR2) && hasRepairLevel(issues, RepairR0) && s.repairItemIDs && clientItemID == "" && allowNewRepair {
		clientItemID = s.newClientItemID(contract, effectiveType, outputIndex)
	}
	if !hasRepairLevel(issues, RepairR2) {
		identityItemID := identityTokenOrEmpty(itemID)
		upstreamItemID := identityItemID
		if !itemIDPresent && hasPrevious {
			identityItemID = previous.ItemID
			upstreamItemID = previous.UpstreamItemID
		}
		identityCallID := identityTokenOrEmpty(callID)
		if !callIDPresent && hasPrevious {
			identityCallID = previous.CallID
		}
		identity := StreamIdentity{ItemID: identityItemID, UpstreamItemID: upstreamItemID, ClientItemID: clientItemID, ItemType: identityTokenOrEmpty(effectiveType), CallID: identityCallID, OutputIndex: outputIndex, Stage: stage}
		s.identities[targetKey] = identity
		if previousKey != targetKey {
			delete(s.identities, previousKey)
		}
		if identity.ItemID != "" {
			ownerItemID := itemID
			if ownerItemID == "" {
				ownerItemID = identityItemID
			}
			if previousKey != targetKey {
				delete(s.itemIDOwners, streamIDKey(streamScope(""), ownerItemID))
			}
			key := streamIDKey(scope, ownerItemID)
			if s.itemIDOwners[key] == "" {
				s.itemIDOwners[key] = targetKey
			}
		}
	}
	result := s.result(issues)
	if clientItemID != "" && known && hasRepairLevel(issues, RepairR0) && !hasRepairLevel(issues, RepairR2) {
		result.Repairs = []StreamRepair{{OutputIndex: outputIndex, ItemType: effectiveType, Field: streamRepairField(stage), ClientItemID: clientItemID}}
	}
	return result
}

func (s *StreamState) consumeCompleted(responseResourceID string, event map[string]any, allowNewRepair bool) StreamEventResult {
	response, ok := objectValue(event["response"])
	issues := make([]Issue, 0)
	repairs := make([]StreamRepair, 0)
	if !ok {
		issues = append(issues, s.issue("response_resource_id_missing", "response.completed 缺少 response 对象", []any{"event", "response"}, -1, "", RepairR2))
		return s.result(issues)
	}
	completedID, hasID := nonEmptyString(response["id"])
	if !hasID {
		issues = append(issues, s.issue("response_resource_id_missing", "response.completed 缺少 response.id", []any{"event", "response", "id"}, -1, "", RepairR2))
	} else if responseResourceID != "" && completedID != responseResourceID {
		issues = append(issues, s.issue("response_resource_id_inconsistent", "response.completed 的 response.id 与当前流资源不一致", []any{"event", "response", "id"}, -1, "", RepairR2))
	}
	output, present := response["output"]
	if !present {
		return s.result(issues)
	}
	items, ok := output.([]any)
	if !ok {
		issues = append(issues, s.issue("response_item_collection_invalid", "response.completed.response.output 必须是数组", []any{"event", "response", "output"}, -1, "", RepairR2))
		return s.result(issues)
	}
	rawSeen := make(map[string]int)
	visibleSeen := make(map[string]int)
	for index, rawItem := range items {
		issueStart := len(issues)
		item, ok := objectValue(rawItem)
		if !ok {
			issues = append(issues, s.issue("item_not_object", "completed output item 必须是对象", []any{"event", "response", "output", index}, index, "", RepairR2))
			continue
		}
		itemType, ok := nonEmptyString(item["type"])
		if !ok {
			issues = append(issues, s.issue("item_type_missing", "completed output item 缺少 type", []any{"event", "response", "output", index, "type"}, index, "", RepairR2))
			continue
		}
		previous, hasPrevious := s.identities[streamItemKey(responseResourceID, index)]
		if !hasPrevious && responseResourceID != "" {
			// Some providers omit response.created. Preserve an unscoped stream
			// identity when response.completed finally supplies the resource ID.
			previous, hasPrevious = s.identities[streamItemKey("", index)]
		}
		itemID, _ := stringValue(item["id"])
		callID, _ := stringValue(item["call_id"])
		if hasPrevious && itemID != "" && previous.ItemID != identityToken(itemID) {
			issues = append(issues, s.issue("event_item_id_inconsistent", "completed output 的 item ID 与流式 identity 不一致", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR2))
		}
		if hasPrevious && previous.ItemType != "" && previous.ItemType != identityToken(itemType) {
			issues = append(issues, s.issue("event_item_type_inconsistent", "completed output 的 item type 与流式 identity 不一致", []any{"event", "response", "output", index, "type"}, index, itemType, RepairR2))
		}
		contract, known := s.registry.Item(itemType)
		if !known {
			issues = append(issues, s.issue("unknown_item_type", "completed output 出现 registry 未知 item type", []any{"event", "response", "output", index, "type"}, index, itemType, RepairNone))
			continue
		}
		if !contains(contract.EventStages, "done") {
			issues = append(issues, s.issue("event_stage_invalid", itemType+" 不允许 done 阶段", []any{"event", "response", "output", index, "type"}, index, itemType, RepairR2))
		}
		for _, fieldIssue := range validateFields(item, contract) {
			issues = append(issues, s.issue(fieldIssue.code, itemType+"."+fieldIssue.field+" 不满足 Codex Responses contract", []any{"event", "response", "output", index, fieldIssue.field}, index, itemType, RepairR2))
		}
		rawID, presentID := item["id"]
		if presentID {
			if contract.Prefix == "" {
				issues = append(issues, s.issue("item_id_forbidden", itemType+" 不允许携带 item ID", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR2))
			} else if rawID != nil {
				candidate, valid := nonEmptyString(rawID)
				if !valid {
					issues = append(issues, s.issue("item_id_invalid", itemType+" 的 item ID 必须是字符串或 null", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR0))
				} else if !isExpectedID(candidate, contract.Prefix) {
					issues = append(issues, s.issue("item_id_prefix_mismatch", itemType+" 的 item ID 前缀与 contract 不一致", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR0))
				}
			}
		}
		if itemID != "" {
			token := identityToken(itemID)
			if _, reserved := s.clientItemIDs[token]; reserved && (!hasPrevious || previous.ClientItemID != itemID) {
				issues = append(issues, s.issue("generated_item_identity_collision", "completed output 的上游 ID 与已生成 client ID 冲突", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR2))
			}
			if prior, exists := rawSeen[token]; exists && prior != index {
				issues = append(issues, s.issue("duplicate_item_identity", "completed output 的多个 item 使用了重复 ID", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR2))
			} else {
				rawSeen[token] = index
			}
		}
		if hasPrevious && previous.CallID != "" && callID != "" && previous.CallID != identityToken(callID) {
			issues = append(issues, s.issue("event_call_id_inconsistent", "completed output 的 call_id 与流式 identity 不一致", []any{"event", "response", "output", index, "call_id"}, index, itemType, RepairR2))
		}
		itemIssues := issues[issueStart:]
		clientID := ""
		if s.repairItemIDs && !hasRepairLevel(itemIssues, RepairR2) && hasRepairLevel(itemIssues, RepairR0) {
			if hasPrevious {
				clientID = previous.ClientItemID
			}
			if clientID == "" && allowNewRepair {
				clientID = s.newClientItemID(contract, itemType, index)
			}
		}
		visibleID := itemID
		if clientID != "" {
			visibleID = clientID
		}
		if visibleID != "" {
			token := identityToken(visibleID)
			if prior, exists := visibleSeen[token]; exists && prior != index {
				issues = append(issues, s.issue("client_visible_item_identity_collision", "completed output 重写后会产生重复 client item ID", []any{"event", "response", "output", index, "id"}, index, itemType, RepairR2))
			} else {
				visibleSeen[token] = index
			}
		}
		if clientID != "" && !hasRepairLevel(issues[issueStart:], RepairR2) {
			repairs = append(repairs, StreamRepair{OutputIndex: index, ItemType: itemType, Field: "response.output.id", ClientItemID: clientID})
		}
	}
	result := s.result(issues)
	result.Repairs = repairs
	return result
}

func (s *StreamState) newClientItemID(contract ItemContract, itemType string, outputIndex int) string {
	for attempt := 0; attempt < 100; attempt++ {
		sequence := s.repairSeq
		s.repairSeq++
		candidate := ""
		if s.createItemID != nil {
			candidate = s.createItemID(contract.Prefix, itemType, sequence, outputIndex)
		} else {
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				continue
			}
			candidate = fmt.Sprintf("%s_%x", contract.Prefix, token[:])
		}
		if len(candidate) <= 256 && isExpectedID(candidate, contract.Prefix) {
			token := identityToken(candidate)
			if _, exists := s.clientItemIDs[token]; !exists {
				if _, observed := s.observedItemIDs[token]; !observed {
					s.clientItemIDs[token] = struct{}{}
					return candidate
				}
			}
		}
	}
	return ""
}

func (s *StreamState) result(issues []Issue) StreamEventResult {
	for _, issue := range issues {
		if len(s.diagnostics) < StreamDiagnosticLimit {
			s.diagnostics = append(s.diagnostics, cloneStreamIssue(issue))
		} else {
			s.omitted++
		}
	}
	bounded := issues
	if len(bounded) > StreamDiagnosticLimit {
		bounded = bounded[:StreamDiagnosticLimit]
	}
	return StreamEventResult{Revision: Revision, Outcome: streamOutcome(issues), Issues: cloneIssues(bounded), EventCategory: "protocol_event"}
}

func (s *StreamState) issue(code, message string, path []any, outputIndex int, itemType string, level RepairLevel) Issue {
	return newIssue(s.provenance, boundedText(code, 96), boundedText(message, 256), boundedPath(path), outputIndex, boundedText(itemType, 128), level)
}

func streamOutcome(issues []Issue) Outcome {
	if len(issues) == 0 {
		return OutcomeClean
	}
	for _, issue := range issues {
		if issue.RepairLevel == RepairR2 {
			return OutcomeBlocked
		}
	}
	for _, issue := range issues {
		if issue.RepairLevel == RepairR0 {
			return OutcomeRepairable
		}
	}
	return OutcomeObservedUnknown
}

func streamEventStage(eventType string) (StreamStage, bool) {
	switch {
	case eventType == "response.output_item.added":
		return StreamStageAdded, true
	case eventType == "response.output_item.done":
		return StreamStageDone, true
	case len(eventType) > len("response.") && eventType[:len("response.")] == "response." && len(eventType) > len(".delta") && eventType[len(eventType)-len(".delta"):] == ".delta":
		return StreamStageDelta, true
	default:
		return "", false
	}
}

func deltaItemType(eventType string) string {
	switch eventType {
	case "response.output_text.delta":
		return "message"
	case "response.function_call_arguments.delta":
		return "function_call"
	case "response.custom_tool_call_input.delta":
		return "custom_tool_call"
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return "reasoning"
	default:
		return ""
	}
}

func streamItemKey(responseResourceID string, outputIndex int) string {
	scope := streamScope(responseResourceID)
	return fmt.Sprintf("%d:%s:%d", len(scope), scope, outputIndex)
}

func streamScope(value string) string {
	if value == "" {
		return "<unscoped>"
	}
	return identityToken(value)
}

func streamIDKey(scope, itemID string) string {
	return fmt.Sprintf("%d:%s:%s", len(scope), scope, identityToken(itemID))
}

func identityToken(value string) string {
	if len(value) <= 256 {
		return value
	}
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func identityTokenOrEmpty(value string) string {
	if value == "" {
		return ""
	}
	return identityToken(value)
}

func streamItemPath(stage StreamStage, field string) []any {
	if stage == StreamStageDelta && field == "id" {
		return []any{"event", "item_id"}
	}
	if stage == StreamStageDelta && field == "type" {
		return []any{"event", "item_type"}
	}
	return []any{"event", "item", field}
}

func streamRepairField(stage StreamStage) string {
	if stage == StreamStageDelta {
		return "item_id"
	}
	return "item.id"
}

func objectValue(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok && result != nil
}

func firstStringPresent(values ...any) (string, bool) {
	for _, value := range values {
		if result, ok := stringValue(value); ok {
			return result, true
		}
	}
	return "", false
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		if result, ok := nonEmptyString(value); ok {
			return result
		}
	}
	return ""
}

func nonNegativeInteger(value any) (int, bool) {
	switch candidate := value.(type) {
	case int:
		return candidate, candidate >= 0 && int64(candidate) <= maxSafeJSONInteger
	case int64:
		return int(candidate), candidate >= 0 && candidate <= maxSafeJSONInteger && int64(int(candidate)) == candidate
	case json.Number:
		parsed, err := strconv.ParseInt(string(candidate), 10, 64)
		return int(parsed), err == nil && parsed >= 0 && parsed <= maxSafeJSONInteger && int64(int(parsed)) == parsed
	case float64:
		return int(candidate), candidate >= 0 && candidate <= float64(maxSafeJSONInteger) && candidate == float64(int(candidate))
	default:
		return 0, false
	}
}

func hasRepairLevel(issues []Issue, level RepairLevel) bool {
	for _, issue := range issues {
		if issue.RepairLevel == level {
			return true
		}
	}
	return false
}

func boundedPath(path []any) []any {
	if len(path) > 12 {
		path = path[:12]
	}
	result := make([]any, len(path))
	copy(result, path)
	return result
}

func boundedText(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func cloneIssues(issues []Issue) []Issue {
	result := make([]Issue, len(issues))
	for index, issue := range issues {
		result[index] = issue
		result[index].Path = append([]any(nil), issue.Path...)
	}
	return result
}

func cloneStreamIssue(issue Issue) Issue {
	issue.Path = append([]any(nil), issue.Path...)
	return issue
}
