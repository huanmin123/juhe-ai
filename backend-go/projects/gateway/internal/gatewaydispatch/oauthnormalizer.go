package gatewaydispatch

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
)

// Codex body normalization, migrated from adapters/gpt-codex/oauth-normalizer.ts.

// OpenAIOAuthCodexAccount mirrors OpenAIOAuthCodexAccount.
type OpenAIOAuthCodexAccount struct {
	ID          string
	APIKey      string
	Credentials map[string]any
}

// OpenAIOAuthCodexIdentity mirrors OpenAIOAuthCodexIdentity.
type OpenAIOAuthCodexIdentity struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
}

// OpenAIOAuthCodexNormalizeInput mirrors OpenAIOAuthCodexNormalizeInput.
type OpenAIOAuthCodexNormalizeInput struct {
	InputHeaders                      http.Header
	Account                           OpenAIOAuthCodexAccount
	Identity                          OpenAIOAuthCodexIdentity
	Compact                           bool
	SanitizeCodexHistory              bool
	ModelOverride                     string
	RequestOverrideModelCapabilities  *GptRequestOverrideModelCapabilities
	// ApplyGptAccountRequestOverrides mirrors the injected
	// providers/drivers/gpt/request-overrides dependency; nil keeps the body
	// unchanged. Errors of type *GptAccountRequestOverrideError become
	// account-scoped OpenAIOAuthCodexAdapterError.
	ApplyGptAccountRequestOverrides   func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error)
}

// GptRequestOverrideModelCapabilities mirrors the capability subset.
type GptRequestOverrideModelCapabilities struct {
	SupportedServiceTiers    []string
	SupportedReasoningEfforts []string
}

// GptAccountOverrideInput mirrors applyGptAccountRequestOverrides' input.
type GptAccountOverrideInput struct {
	Credentials     map[string]any
	EndpointFamily  string
	Compact         bool
	ModelCapabilities *GptRequestOverrideModelCapabilities
}

// GptAccountRequestOverrideError mirrors the override error marker.
type GptAccountRequestOverrideError struct{ Message string }

func (e *GptAccountRequestOverrideError) Error() string { return e.Message }

// NormalizedCodexBody mirrors NormalizedCodexBody.
type NormalizedCodexBody struct {
	// Body is the serialized JSON when SanitizeCodexHistory is off.
	Body                  string
	// BodyBytes is the serialized JSON when the history was sanitized (the
	// sanitizer marks the buffer for the dispatch reuse check).
	BodyBytes             []byte
	CodexHistorySanitized bool
	Stream                bool
	Session               OpenAIOAuthCodexSessionResolution
	Model                 string
}

// OpenAIOAuthCodexSessionResolution mirrors the session resolution.
type OpenAIOAuthCodexSessionResolution struct {
	SessionID       string
	ConversationID  string
	PromptCacheKey  string
}

// openAIOAuthCodexDroppedFields mirrors openAIOAuthCodexDroppedFields.
var openAIOAuthCodexDroppedFields = []string{
	"background",
	"conversation",
	"context_management",
	"frequency_penalty",
	"max_completion_tokens",
	"max_output_tokens",
	"metadata",
	"presence_penalty",
	"prompt_cache_retention",
	"safety_identifier",
	"stream_options",
	"temperature",
	"top_p",
	"truncation",
	"user",
}

// openAIOAuthCodexCompactDroppedFields mirrors the compact superset.
var openAIOAuthCodexCompactDroppedFields = append(append([]string{}, openAIOAuthCodexDroppedFields...),
	"include",
	"parallel_tool_calls",
	"prompt_cache_key",
	"store",
	"stream",
	"text",
	"tool_choice",
	"tools",
	"top_logprobs",
)

// NormalizeOpenAIOAuthCodexParsedBody mirrors normalizeOpenAIOAuthCodexParsedBody.
func NormalizeOpenAIOAuthCodexParsedBody(parsedBody any, input OpenAIOAuthCodexNormalizeInput) (NormalizedCodexBody, error) {
	body, ok := parsedBody.(map[string]any)
	if !ok {
		return NormalizedCodexBody{}, NewOpenAIOAuthCodexAdapterError("请求体必须是 JSON 对象")
	}
	cloned := cloneJSONObject(body)
	if input.ModelOverride != "" {
		cloned["model"] = input.ModelOverride
	}
	if err := validateOpenAIOAuthCodexBody(cloned, input.Compact); err != nil {
		return NormalizedCodexBody{}, err
	}
	session := resolveOpenAIOAuthCodexSession(input.InputHeaders, cloned, input.Account, input.Identity)
	applyOpenAIOAuthCodexSessionToBody(cloned, session, input.Compact)
	if err := normalizeOpenAIOAuthCodexInstructions(cloned); err != nil {
		return NormalizedCodexBody{}, err
	}
	normalizeOpenAIOAuthCodexInput(cloned)
	normalizeOpenAIOAuthCodexLegacyFunctions(cloned)
	normalizeOpenAIOAuthCodexTools(cloned)
	if err := applyOpenAIOAuthCodexAccountRequestOverrides(cloned, input); err != nil {
		return NormalizedCodexBody{}, err
	}

	stream := true
	if input.Compact {
		deleteFields(cloned, openAIOAuthCodexCompactDroppedFields)
		model, _ := cloned["model"].(string)
		NormalizeOpenAICodexResponsesLiteBody(cloned, model, nil)
		if input.SanitizeCodexHistory {
			sanitizeOpenAIOAuthCodexHistory(cloned, input.Account.ID)
		}
		serialized, err := json.Marshal(cloned)
		if err != nil {
			return NormalizedCodexBody{}, err
		}
		result := NormalizedCodexBody{
			CodexHistorySanitized: input.SanitizeCodexHistory,
			Stream:                false,
			Session:               session,
			Model:                 strings.TrimSpace(model),
		}
		if input.SanitizeCodexHistory {
			result.BodyBytes = serialized
		} else {
			result.Body = string(serialized)
		}
		return result, nil
	}

	deleteFields(cloned, openAIOAuthCodexDroppedFields)
	ensureOpenAIOAuthCodexReasoningInclude(cloned)
	cloned["store"] = false
	cloned["stream"] = true
	model, _ := cloned["model"].(string)
	NormalizeOpenAICodexResponsesLiteBody(cloned, model, nil)
	if input.SanitizeCodexHistory {
		sanitizeOpenAIOAuthCodexHistory(cloned, input.Account.ID)
	}
	serialized, err := json.Marshal(cloned)
	if err != nil {
		return NormalizedCodexBody{}, err
	}
	_ = stream
	result := NormalizedCodexBody{
		CodexHistorySanitized: input.SanitizeCodexHistory,
		Stream:                true,
		Session:               session,
		Model:                 strings.TrimSpace(model),
	}
	if input.SanitizeCodexHistory {
		result.BodyBytes = serialized
	} else {
		result.Body = string(serialized)
	}
	return result, nil
}

// SanitizeCodexHistoryItems is the sanitizer hook (Node
// codex-responses/request-history-sanitizer.ts, G18). It receives the input
// array and returns the (possibly rewritten) items.
type SanitizeCodexHistoryItems func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult

// SanitizeCodexHistoryOptions mirrors the sanitizer options.
type SanitizeCodexHistoryOptions struct {
	Store                  bool
	TargetScopeKey         string
	TargetPersistenceScope string
}

// CodexHistorySanitizeResult mirrors the sanitizer result.
type CodexHistorySanitizeResult struct {
	Items   []any
	Changed bool
}

// SanitizeCodexHistory is the injectable sanitizer; nil keeps items intact.
var SanitizeCodexHistory SanitizeCodexHistoryItems

func sanitizeOpenAIOAuthCodexHistory(body map[string]any, accountID string) {
	items, ok := body["input"].([]any)
	if !ok {
		return
	}
	if SanitizeCodexHistory == nil {
		return
	}
	targetScopeKey := ""
	if accountID != "" {
		targetScopeKey = "account:" + accountID
	}
	result := SanitizeCodexHistory(items, SanitizeCodexHistoryOptions{
		Store:                  false,
		TargetScopeKey:         targetScopeKey,
		TargetPersistenceScope: "none",
	})
	if result.Changed {
		body["input"] = result.Items
	}
}

func applyOpenAIOAuthCodexAccountRequestOverrides(body map[string]any, input OpenAIOAuthCodexNormalizeInput) error {
	if input.ApplyGptAccountRequestOverrides == nil {
		return nil
	}
	overridden, err := input.ApplyGptAccountRequestOverrides(body, GptAccountOverrideInput{
		Credentials:     input.Account.Credentials,
		EndpointFamily:  "responses",
		Compact:         input.Compact,
		ModelCapabilities: input.RequestOverrideModelCapabilities,
	})
	if err != nil {
		var overrideErr *GptAccountRequestOverrideError
		if asOverrideError(err, &overrideErr) {
			return NewOpenAIOAuthCodexAdapterError(overrideErr.Message, WithCodexAdapterAccountScoped())
		}
		return err
	}
	if jsonValueEqual(overridden, body) {
		return nil
	}
	for key := range body {
		delete(body, key)
	}
	for key, value := range overridden {
		body[key] = value
	}
	return nil
}

func asOverrideError(err error, target **GptAccountRequestOverrideError) bool {
	for err != nil {
		if overrideErr, ok := err.(*GptAccountRequestOverrideError); ok {
			*target = overrideErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// NormalizeOpenAIOAuthCodexRawBody mirrors normalizeOpenAIOAuthCodexRawBody.
func NormalizeOpenAIOAuthCodexRawBody(rawBody []byte, input OpenAIOAuthCodexNormalizeInput) (NormalizedCodexBody, error) {
	var parsed any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return NormalizedCodexBody{}, NewOpenAIOAuthCodexAdapterError("请求体必须是有效的 JSON 对象")
	}
	return NormalizeOpenAIOAuthCodexParsedBody(parsed, input)
}

// EnsureOpenAIOAuthCodexPlainJsonObject mirrors
// ensureOpenAIOAuthCodexPlainJsonObject.
func EnsureOpenAIOAuthCodexPlainJsonObject(value any) (map[string]any, error) {
	if object, ok := value.(map[string]any); ok {
		return cloneJSONObject(object), nil
	}
	return nil, NewOpenAIOAuthCodexAdapterError("请求体必须是 JSON 对象")
}

func cloneJSONObject(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = jsonCloneValue(value)
	}
	return cloned
}

func validateOpenAIOAuthCodexBody(body map[string]any, compact bool) error {
	model, _ := body["model"].(string)
	if strings.TrimSpace(model) == "" {
		return NewOpenAIOAuthCodexAdapterError("请求体中的 model 必须是非空字符串")
	}

	if compact {
		return nil
	}

	if _, ok := body["input"]; !ok {
		return NewOpenAIOAuthCodexAdapterError("请求体必须包含 input 字段")
	}
	switch body["input"].(type) {
	case string, []any:
		return nil
	default:
		return NewOpenAIOAuthCodexAdapterError("请求体中的 input 必须是字符串或数组")
	}
}

func normalizeOpenAIOAuthCodexInstructions(body map[string]any) error {
	if _, ok := body["instructions"]; !ok {
		body["instructions"] = ""
		return nil
	}
	if _, ok := body["instructions"].(string); !ok {
		return NewOpenAIOAuthCodexAdapterError("请求体中的 instructions 必须是字符串")
	}
	return nil
}

func normalizeOpenAIOAuthCodexInput(body map[string]any) {
	if text, ok := body["input"].(string); ok {
		body["input"] = []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": text,
					},
				},
			},
		}
		return
	}

	items, ok := body["input"].([]any)
	if !ok {
		return
	}

	var systemInstructions []string
	next := make([]any, len(items))
	for index, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			next[index] = item
			continue
		}
		if role, _ := record["role"].(string); role != "system" {
			next[index] = item
			continue
		}
		instruction := openAIOAuthCodexContentText(record["content"])
		if instruction != "" {
			systemInstructions = append(systemInstructions, instruction)
		}
		converted := cloneJSONObject(record)
		converted["role"] = "developer"
		next[index] = converted
	}
	body["input"] = next
	if len(systemInstructions) > 0 {
		existing, _ := body["instructions"].(string)
		existing = strings.TrimSpace(existing)
		parts := append([]string{}, systemInstructions...)
		if existing != "" {
			parts = append(parts, existing)
		}
		body["instructions"] = strings.Join(parts, "\n\n")
	}
}

func normalizeOpenAIOAuthCodexLegacyFunctions(body map[string]any) {
	if _, ok := body["functions"]; ok {
		if definitions, ok := body["functions"].([]any); ok {
			tools := make([]any, 0, len(definitions))
			for _, definition := range definitions {
				tools = append(tools, map[string]any{"type": "function", "function": definition})
			}
			body["tools"] = tools
		}
		delete(body, "functions")
	}
	if _, ok := body["function_call"]; !ok {
		return
	}
	switch value := body["function_call"].(type) {
	case string:
		body["tool_choice"] = value
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			body["tool_choice"] = map[string]any{"type": "function", "name": strings.TrimSpace(name)}
		}
	}
	delete(body, "function_call")
}

func ensureOpenAIOAuthCodexReasoningInclude(body map[string]any) {
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || len(reasoning) == 0 {
		return
	}
	encryptedContent := "reasoning.encrypted_content"
	switch existing := body["include"].(type) {
	case nil:
		body["include"] = []any{encryptedContent}
	case []any:
		found := false
		for _, item := range existing {
			if text, ok := item.(string); ok && text == encryptedContent {
				found = true
				break
			}
		}
		if !found {
			body["include"] = append(append([]any{}, existing...), encryptedContent)
		}
	}
}

func openAIOAuthCodexContentText(content any) string {
	if text, ok := content.(string); ok {
		return strings.TrimSpace(text)
	}
	items, ok := content.([]any)
	if !ok {
		return ""
	}
	var builder strings.Builder
	for _, part := range items {
		if record, ok := part.(map[string]any); ok {
			if text, ok := record["text"].(string); ok {
				builder.WriteString(text)
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func normalizeOpenAIOAuthCodexTools(body map[string]any) {
	NormalizeOpenAICodexBuiltinTools(body)
}

func resolveOpenAIOAuthCodexSession(
	inputHeaders http.Header,
	body map[string]any,
	account OpenAIOAuthCodexAccount,
	identity OpenAIOAuthCodexIdentity,
) OpenAIOAuthCodexSessionResolution {
	rawPromptCacheKey := firstNonEmptyString(
		stringValueOf(body["prompt_cache_key"]),
		headerValueOf(inputHeaders, "prompt_cache_key"),
		headerValueOf(inputHeaders, "x-prompt-cache-key"),
	)
	rawSessionID := firstNonEmptyString(headerValueOf(inputHeaders, "session-id"))
	rawConversationID := firstNonEmptyString(headerValueOf(inputHeaders, "thread-id"))
	rawPrimary := firstNonEmptyString(rawSessionID, rawConversationID, rawPromptCacheKey)

	session := OpenAIOAuthCodexSessionResolution{}
	if rawSessionID != "" {
		session.SessionID = IsolateOpenAIOAuthCodexSessionID(rawSessionID, account, identity)
	}
	if rawConversationID != "" {
		session.ConversationID = IsolateOpenAIOAuthCodexSessionID(rawConversationID, account, identity)
	}
	if rawPromptCacheKey != "" {
		session.PromptCacheKey = IsolateOpenAIOAuthCodexSessionID(rawPromptCacheKey, account, identity)
	} else if rawPrimary != "" {
		session.PromptCacheKey = IsolateOpenAIOAuthCodexSessionID(rawPrimary, account, identity)
	}
	return session
}

func applyOpenAIOAuthCodexSessionToBody(body map[string]any, session OpenAIOAuthCodexSessionResolution, compact bool) {
	delete(body, "session_id")
	delete(body, "conversation_id")
	if compact {
		delete(body, "prompt_cache_key")
		return
	}
	if session.PromptCacheKey != "" {
		body["prompt_cache_key"] = session.PromptCacheKey
	}
}

// IsolateOpenAIOAuthCodexSessionId mirrors isolateOpenAIOAuthCodexSessionId:
// keep the local cache/session namespace stable when dispatch switches
// upstream accounts.
func IsolateOpenAIOAuthCodexSessionID(raw string, _ OpenAIOAuthCodexAccount, identity OpenAIOAuthCodexIdentity) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return ""
	}
	apiKeyID := identity.APIKeyID
	if apiKeyID == "" {
		apiKeyID = "internal"
	}
	payload, err := json.Marshal(map[string]string{
		"systemAccountId": identity.SystemAccountID,
		"apiKeyId":        apiKeyID,
		"raw":             normalized,
	})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hexEncode(digest[:])[:32]
}

func hexEncode(data []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(data)*2)
	for _, b := range data {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

func headerValueOf(inputHeaders http.Header, name string) string {
	if inputHeaders == nil {
		return ""
	}
	values := inputHeaders.Values(name)
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringValueOf(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func deleteFields(body map[string]any, fields []string) {
	for _, field := range fields {
		delete(body, field)
	}
}
