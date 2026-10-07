package gatewaycodex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	// SignalEncryptedContextInvalid 是 BUG-0289 补齐的生产信号：Codex 会话携带
	// 上一轮 encrypted_content，被路由到非生成上游时返回
	// code=encrypted_context_invalid（200 流内 response.failed / 非 2xx 失败面
	// 皆可出现），语义与既有三信号同为"加密上下文被上游拒绝，清理后同账户重放"。
	SignalEncryptedContextInvalid = "encrypted_context_invalid"
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
// 协议门控 + 全文 Classify 之后，核心 parse→清理→序列化→组装逻辑统一走
// BuildCodexEncryptedContentRecoveryRetry（与 200 流内失败面共享，BUG-0289）。
func RecoverCodexEncryptedContent(_ context.Context, input EncryptedContentRecoveryInput) CodexEncryptedContentRecoveryResult {
	if !isOpenAIProtocolProfile(input.Account) || gatewayRequestEndpointFamily(input.Req, input.EndpointFamily) != gatewayopenai.FamilyResponses {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotApplicable}
	}

	signal := ClassifyCodexEncryptedContentRecoverySignal(input.UpstreamErrorText)
	if signal == "" {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotApplicable}
	}
	return BuildCodexEncryptedContentRecoveryRetry(input.Body, signal)
}

// BuildCodexEncryptedContentRecoveryRetry 承载加密上下文清理重放的核心构造：
// parse → removeRejectedCodexEncryptedContent → 序列化 → 组装
// Metadata/SemanticRetryID。signal 由调用方分类（非 2xx 失败面用全文
// Classify，200 流内失败面用 ClassifyCodexEncryptedContentFailureParts 的
// 结构化决策字段），body 必须是本次 attempt 实际发送的请求体（已过模型
// 映射）。行为与既有 Recover 分支逐字段一致：
//   - body 为 nil → not_recoverable（仅携带 signal）；
//   - 解析失败 → not_recoverable + request_body_parse_failed；
//   - 无可清理内容 → not_recoverable + no_removable_encrypted_content。
func BuildCodexEncryptedContentRecoveryRetry(body []byte, signal CodexEncryptedContentRecoverySignal) CodexEncryptedContentRecoveryResult {
	if body == nil {
		return CodexEncryptedContentRecoveryResult{Action: RecoveryActionNotRecoverable, Signal: signal}
	}

	parsed, ok := parseJSONObjectBody(body)
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
			BodyBytesBefore:                            len(body),
			BodyBytesAfter:                             len(serialized.Raw),
		},
	}
}

// IsOpenAIResponsesRequest 判断请求是否属于 OpenAI /v1/responses 族（复用
// 既有 endpoint family 判定通道）。200 流内失败面的恢复臂以它做请求族门控；
// EndpointFamily override 场景（合成请求）走 gatewayRequestEndpointFamily 的
// 同一约定——本包装只读请求路径，不携带 override。
func IsOpenAIResponsesRequest(req *gatewaypreauth.GatewayRequest) bool {
	return gatewayRequestEndpointFamily(req, "") == gatewayopenai.FamilyResponses
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
	case "encrypted_context_invalid":
		return SignalEncryptedContextInvalid
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
	// BUG-0289：Responses 失败形态把 error 挂在 response.error 下（200 流内
	// response.failed 事件的 data 行即此形状）。作为追加候选加入——只扩展
	// 信号载体的搜索面，信号判定本身（精确码 / 消息启发）不变。
	responsesError, hasResponsesError := func() (map[string]any, bool) {
		response, ok := payload["response"].(map[string]any)
		if !ok {
			return nil, false
		}
		errorObject, ok := response["error"].(map[string]any)
		return errorObject, ok
	}()
	candidates := []map[string]any{payload}
	if hasNestedError {
		candidates = append(candidates, nestedError)
	}
	if hasResponsesError {
		candidates = append(candidates, responsesError)
	}
	for candidateIndex, candidate := range candidates {
		if code, isString := candidate["code"].(string); isString {
			if signal := signalForExactErrorCode(code); signal != "" {
				return signal
			}
		}

		// Node: candidate === nestedError || payload.type === 'error' ||
		// nestedError !== undefined — the second candidate is the nested
		// error object; the third is the Responses response.error 对象
		// （BUG-0289），与嵌套 error 同等对待。
		errorPayload := candidateIndex >= 1 ||
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
			contains("could not be parsed") ||
			// BUG-0289 生产文案："The upstream could not validate encrypted
			// continuation context..."——主动语态的校验失败变体，与被动语态
			// "could not be decrypted" 同义；仍以 "encrypted" 出现为前提。
			contains("could not validate"))
}

// ClassifyCodexEncryptedContentFailureParts 对齐响应层（BUG-0289）从结构化
// 决策字段分类的入口：先走精确错误码，再走消息启发。非 2xx 失败面继续用
// ClassifyCodexEncryptedContentRecoverySignal(全文)；200 流内失败面在
// finalize 侧已拆出 code / message 字段，不再拼回原始文本。
func ClassifyCodexEncryptedContentFailureParts(errorCode, errorMessage string) CodexEncryptedContentRecoverySignal {
	if signal := signalForExactErrorCode(errorCode); signal != "" {
		return signal
	}
	if looksLikeEncryptedContentDecryptionFailure(errorMessage) {
		return SignalEncryptedContentDecryptionFailed
	}
	return ""
}

func parseJSONRecord(value string) map[string]any {
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
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
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
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
