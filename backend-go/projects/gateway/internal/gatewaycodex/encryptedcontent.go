package gatewaycodex

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of request/codex-encrypted-content-recovery.ts.
//
// Retry one pre-commit OpenAI Responses attempt with opaque encrypted state
// removed only after the upstream explicitly rejects that state. The caller
// owns the one-attempt budget; this helper never mutates the client request.

// CodexEncryptedContentRecoverySignal mirrors CodexEncryptedContentRecoverySignal.
type CodexEncryptedContentRecoverySignal = string

// Encrypted content recovery signals.
const (
	SignalThinkingSignatureInvalid         = "thinking_signature_invalid"
	SignalInvalidEncryptedContent          = "invalid_encrypted_content"
	SignalEncryptedContentDecryptionFailed = "encrypted_content_decryption_failed"
)

// CodexEncryptedContentRecoveryExhaustedMessage mirrors
// codexEncryptedContentRecoveryExhaustedMessage.
const CodexEncryptedContentRecoveryExhaustedMessage = "上游拒绝了加密上下文，网关已尝试一次兼容性清理但仍然失败。请新建会话，或不要携带上一会话的加密 reasoning、工具输出或 compaction 后重新发送请求。"

// CodexEncryptedContentRecoveryMetadata mirrors
// CodexEncryptedContentRecoveryMetadata.
type CodexEncryptedContentRecoveryMetadata struct {
	Strategy                                   string
	Signal                                     CodexEncryptedContentRecoverySignal
	RemovedReasoningEncryptedContentCount      int
	RemovedFunctionOutputEncryptedContentCount int
	RemovedAgentMessageEncryptedContentCount   int
	RemovedCompactionEncryptedContentCount     int
	RemovedReasoningItemCount                  int
	RemovedAgentMessageItemCount               int
	RemovedCompactionItemCount                 int
	PreservedPreviousResponseID                bool
	BodyBytesBefore                            int
	BodyBytesAfter                             int
}

// CodexEncryptedContentRecoveryResult mirrors
// CodexEncryptedContentRecoveryResult. Action is one of
// 'retry_with_body_variant' | 'not_applicable' | 'not_recoverable'.
type CodexEncryptedContentRecoveryResult struct {
	Action          string
	Body            []byte
	SemanticRetryID string
	Metadata        *CodexEncryptedContentRecoveryMetadata
	// Signal / Reason carry the not_recoverable diagnostics.
	Signal string
	Reason string
}

// Encrypted content recovery actions / reasons.
const (
	RecoveryActionRetryWithBodyVariant = "retry_with_body_variant"
	RecoveryActionNotApplicable        = "not_applicable"
	RecoveryActionNotRecoverable       = "not_recoverable"

	RecoveryReasonRequestBodyParseFailed      = "request_body_parse_failed"
	RecoveryReasonNoRemovableEncryptedContent = "no_removable_encrypted_content"
)

// EncryptedContentRecoveryInput mirrors recoverCodexEncryptedContentRequest's
// input bag. EndpointFamily optionally carries the
// gatewayModelMappingSourceEndpointFamilyOverride of the Node request object
// (synthetic chat requests); an empty value derives the family from Req.
type EncryptedContentRecoveryInput struct {
	Req               *gatewaypreauth.GatewayRequest
	Account           gatewayruntimecache.OpenAIAccountSecret
	Body              []byte
	UpstreamErrorText string
	EndpointFamily    string
}

// RecoverCodexEncryptedContent mirrors recoverCodexEncryptedContentRequest.
func RecoverCodexEncryptedContent(_ context.Context, input EncryptedContentRecoveryInput) CodexEncryptedContentRecoveryResult {
	if !isOpenAIProtocolProfile(input.Account) || gatewayRequestEndpointFamily(input.Req, input.EndpointFamily) != gatewayopenai.FamilyResponses {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotApplicable}
	}

	signal := ClassifyCodexEncryptedContentRecoverySignal(input.UpstreamErrorText)
	if signal == "" || input.Body == nil {
		if signal != "" {
			return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotRecoverable, Signal: signal}
		}
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotApplicable}
	}

	parsed, ok := parseJSONObjectBody(input.Body)
	if !ok {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotRecoverable, Signal: signal, Reason: RecoveryReasonRequestBodyParseFailed}
	}

	sanitized := removeRejectedCodexEncryptedContent(parsed)
	if !sanitized.changed {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotRecoverable, Signal: signal, Reason: RecoveryReasonNoRemovableEncryptedContent}
	}

	serialized := gatewaybody.SerializeGatewayJSONObject(sanitized.body)
	preserved := false
	if previous, isString := sanitized.body["previous_response_id"].(string); isString && strings.TrimSpace(previous) != "" {
		preserved = true
	}
	return CodexEncryptedContentRecoveryResult{
		Action:          RecoveryActionRetryWithBodyVariant,
		Body:            serialized.Raw,
		SemanticRetryID: "codex_encrypted_content_cleanup:" + signal,
		Metadata: &CodexEncryptedContentRecoveryMetadata{
			Strategy:                              "codex_encrypted_content_cleanup",
			Signal:                                signal,
			RemovedReasoningEncryptedContentCount: sanitized.removedReasoningEncryptedContentCount,
			RemovedFunctionOutputEncryptedContentCount: sanitized.removedFunctionOutputEncryptedContentCount,
			RemovedAgentMessageEncryptedContentCount:   sanitized.removedAgentMessageEncryptedContentCount,
			RemovedCompactionEncryptedContentCount:     sanitized.removedCompactionEncryptedContentCount,
			RemovedReasoningItemCount:                  sanitized.removedReasoningItemCount,
			RemovedAgentMessageItemCount:               sanitized.removedAgentMessageItemCount,
			RemovedCompactionItemCount:                 sanitized.removedCompactionItemCount,
			PreservedPreviousResponseID:                preserved,
			BodyBytesBefore:                            len(input.Body),
			BodyBytesAfter:                             len(serialized.Raw),
		},
	}
}

// ClassifyCodexEncryptedContentRecoverySignal mirrors
// classifyCodexEncryptedContentRecoverySignal.
func ClassifyCodexEncryptedContentRecoverySignal(upstreamErrorText string) CodexEncryptedContentRecoverySignal {
	if exactSignal := signalForExactErrorCode(upstreamErrorText); exactSignal != "" {
		return exactSignal
	}
	for _, payload := range structuredErrorPayloads(upstreamErrorText) {
		if signal := signalForStructuredErrorPayload(payload); signal != "" {
			return signal
		}
	}
	return ""
}

func signalForExactErrorCode(value string) CodexEncryptedContentRecoverySignal {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "thinking_signature_invalid":
		return SignalThinkingSignatureInvalid
	case "invalid_encrypted_content":
		return SignalInvalidEncryptedContent
	case "encrypted_content_decryption_failed":
		return SignalEncryptedContentDecryptionFailed
	default:
		return ""
	}
}

func structuredErrorPayloads(value string) []map[string]any {
	var payloads []map[string]any
	if direct := parseJSONRecord(value); direct != nil {
		payloads = append(payloads, direct)
	}

	var eventDataLines []string
	appendEventPayload := func() {
		if len(eventDataLines) == 0 {
			return
		}
		if payload := parseJSONRecord(strings.Join(eventDataLines, "\n")); payload != nil {
			payloads = append(payloads, payload)
		}
		eventDataLines = nil
	}
	for _, line := range splitLines(value) {
		if len(line) == 0 {
			appendEventPayload()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			eventDataLines = append(eventDataLines, jsTrimStart(line[len("data:"):]))
		}
	}
	appendEventPayload()
	return payloads
}

// splitLines mirrors value.split(/\r?\n/).
func splitLines(value string) []string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// jsTrimStart mirrors JS String.prototype.trimStart.
func jsTrimStart(value string) string {
	return strings.TrimLeftFunc(value, unicode.IsSpace)
}

func signalForStructuredErrorPayload(payload map[string]any) CodexEncryptedContentRecoverySignal {
	nestedError, hasNestedError := payload["error"].(map[string]any)
	candidates := []map[string]any{payload}
	if hasNestedError {
		candidates = []map[string]any{payload, nestedError}
	}
	for candidateIndex, candidate := range candidates {
		if code, isString := candidate["code"].(string); isString {
			if signal := signalForExactErrorCode(code); signal != "" {
				return signal
			}
		}

		// Node: candidate === nestedError || payload.type === 'error' ||
		// nestedError !== undefined — the second candidate is the nested
		// error object.
		errorPayload := candidateIndex == 1 ||
			func() bool {
				typeField, isString := payload["type"].(string)
				return isString && typeField == "error"
			}() ||
			hasNestedError
		if errorPayload {
			if message, isString := candidate["message"].(string); isString && looksLikeEncryptedContentDecryptionFailure(message) {
				return SignalEncryptedContentDecryptionFailed
			}
		}
	}
	return ""
}

func looksLikeEncryptedContentDecryptionFailure(value string) bool {
	normalized := strings.ToLower(value)
	contains := func(needle string) bool { return strings.Contains(normalized, needle) }
	return contains("encrypted") &&
		(contains("could not be decrypted") ||
			contains("could not be decoded") ||
			contains("could not be verified") ||
			contains("could not be parsed"))
}

func parseJSONRecord(value string) map[string]any {
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(value))
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return record
}

func parseJSONObjectBody(body []byte) (map[string]any, bool) {
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&parsed); err != nil {
		return nil, false
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil, false
	}
	return record, true
}

type sanitizedEncryptedContent struct {
	body                                       map[string]any
	changed                                    bool
	removedReasoningEncryptedContentCount      int
	removedFunctionOutputEncryptedContentCount int
	removedAgentMessageEncryptedContentCount   int
	removedCompactionEncryptedContentCount     int
	removedReasoningItemCount                  int
	removedAgentMessageItemCount               int
	removedCompactionItemCount                 int
}

func removeRejectedCodexEncryptedContent(body map[string]any) sanitizedEncryptedContent {
	unchanged := sanitizedEncryptedContent{body: body}
	var inputItems []any
	switch typed := body["input"].(type) {
	case []any:
		inputItems = typed
	case map[string]any:
		inputItems = []any{typed}
	default:
		return unchanged
	}

	input := make([]any, 0, len(inputItems))
	for _, item := range inputItems {
		record, isObject := item.(map[string]any)
		if !isObject {
			input = append(input, item)
			continue
		}

		itemType, _ := record["type"].(string)
		if itemType == "reasoning" {
			if _, isString := record["encrypted_content"].(string); isString {
				copy := cloneJSONMap(record)
				delete(copy, "encrypted_content")
				unchanged.changed = true
				unchanged.removedReasoningEncryptedContentCount++
				if isEmptyReasoningItem(copy) {
					unchanged.removedReasoningItemCount++
					continue
				}
				input = append(input, copy)
				continue
			}
		}

		if isCodexCompactionItemWithEncryptedContent(record) {
			unchanged.changed = true
			unchanged.removedCompactionEncryptedContentCount++
			unchanged.removedCompactionItemCount++
			continue
		}

		if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
			output := stripEncryptedContentItems(record["output"])
			if output.changed {
				replacement := cloneJSONMap(record)
				replacement["output"] = output.output
				input = append(input, replacement)
				unchanged.changed = true
				unchanged.removedFunctionOutputEncryptedContentCount += output.removedCount
				continue
			}
		}

		if itemType == "agent_message" {
			content := stripEncryptedContentItems(record["content"])
			if content.changed {
				unchanged.changed = true
				unchanged.removedAgentMessageEncryptedContentCount += content.removedCount
				contentArray, isArray := content.output.([]any)
				if isArray && len(contentArray) == 0 {
					unchanged.removedAgentMessageItemCount++
					continue
				}
				replacement := cloneJSONMap(record)
				replacement["content"] = content.output
				input = append(input, replacement)
				continue
			}
		}
		input = append(input, item)
	}

	if !unchanged.changed {
		return sanitizedEncryptedContent{body: body}
	}
	body["input"] = normalizeSanitizedInput(input, body["input"])
	return sanitizedEncryptedContent{
		body:                                  body,
		changed:                               true,
		removedReasoningEncryptedContentCount: unchanged.removedReasoningEncryptedContentCount,
		removedFunctionOutputEncryptedContentCount: unchanged.removedFunctionOutputEncryptedContentCount,
		removedAgentMessageEncryptedContentCount:   unchanged.removedAgentMessageEncryptedContentCount,
		removedCompactionEncryptedContentCount:     unchanged.removedCompactionEncryptedContentCount,
		removedReasoningItemCount:                  unchanged.removedReasoningItemCount,
		removedAgentMessageItemCount:               unchanged.removedAgentMessageItemCount,
		removedCompactionItemCount:                 unchanged.removedCompactionItemCount,
	}
}

// normalizeSanitizedInput mirrors `Array.isArray(body.input) ? input :
// input[0] ?? []`: a single-object input collapses back to the object.
func normalizeSanitizedInput(input []any, originalInput any) any {
	if _, isArray := originalInput.([]any); isArray {
		return input
	}
	if len(input) > 0 {
		return input[0]
	}
	return []any{}
}

func isCodexCompactionItemWithEncryptedContent(item map[string]any) bool {
	itemType, _ := item["type"].(string)
	if itemType != "compaction" && itemType != "compaction_summary" && itemType != "context_compaction" {
		return false
	}
	_, encryptedIsString := item["encrypted_content"].(string)
	return encryptedIsString
}

type strippedEncryptedContent struct {
	output       any
	changed      bool
	removedCount int
}

func stripEncryptedContentItems(value any) strippedEncryptedContent {
	switch typed := value.(type) {
	case []any:
		removedCount := 0
		output := make([]any, 0, len(typed))
		for _, item := range typed {
			if isEncryptedContentItem(item) {
				removedCount++
				continue
			}
			output = append(output, item)
		}
		return strippedEncryptedContent{output: output, changed: removedCount > 0, removedCount: removedCount}
	case map[string]any:
		if isEncryptedContentItem(typed) {
			return strippedEncryptedContent{output: []any{}, changed: true, removedCount: 1}
		}
	}
	return strippedEncryptedContent{output: value, changed: false, removedCount: 0}
}

// isEncryptedContentItem mirrors the inline predicate: an object with
// type 'encrypted_content' and a string encrypted_content value.
func isEncryptedContentItem(value any) bool {
	record, isObject := value.(map[string]any)
	if !isObject {
		return false
	}
	itemType, _ := record["type"].(string)
	_, encryptedIsString := record["encrypted_content"].(string)
	return itemType == "encrypted_content" && encryptedIsString
}

func isEmptyReasoningItem(item map[string]any) bool {
	for key, value := range item {
		if key == "type" || key == "id" || key == "status" {
			continue
		}
		switch typed := value.(type) {
		case nil:
			continue
		case []any:
			if len(typed) == 0 {
				continue
			}
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
		}
		return false
	}
	return true
}

// isOpenAIProtocolProfile mirrors isOpenAIProtocolProfile(account) on the
// runtime-cache secret (gatewayopenai keeps the same rule on its own
// projection; the two-line predicate is mirrored here because the mapping
// core type is not shared).
func isOpenAIProtocolProfile(account gatewayruntimecache.OpenAIAccountSecret) bool {
	return gatewayopenai.ProtocolCode == normalizeProtocolToken(account.ProtocolCode) &&
		gatewayopenai.ProtocolVersion == normalizeProtocolToken(account.ProtocolVersion)
}

func normalizeProtocolToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// GatewayRequestEndpointFamily mirrors gatewayRequestEndpointFamily(req):
// the optional override wins, then the openai / anthropic / gemini path
// families.
func gatewayRequestEndpointFamily(req *gatewaypreauth.GatewayRequest, override string) string {
	if override != "" {
		return override
	}
	if req == nil {
		return ""
	}
	return openAIRequestEndpointFamily(req.PathAndQuery())
}

func openAIRequestEndpointFamily(pathAndQuery string) string {
	endpoint := pathAndQuery
	if index := strings.IndexByte(endpoint, '?'); index >= 0 {
		endpoint = endpoint[:index]
	}
	return openAIEndpointFamilyFromPath(endpoint)
}

// openAIEndpointFamilyFromPath mirrors openAIEndpointFamilyFromPath
// (domain/openai-endpoint-modes.ts): lowercase containment match, chat
// completions wins over responses.
func openAIEndpointFamilyFromPath(endpoint string) string {
	path := strings.ToLower(strings.TrimSpace(endpoint))
	if path == "" {
		return ""
	}
	if strings.Contains(path, "/chat/completions") {
		return gatewayopenai.FamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return gatewayopenai.FamilyResponses
	}
	return ""
}
