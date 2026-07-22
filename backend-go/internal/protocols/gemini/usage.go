package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
)

const (
	DefaultJSONMaxBytes = 16 << 20
	MaxJSONBytes        = 64 << 20
)

var (
	ErrInvalidJSON     = errors.New("gemini: invalid JSON response")
	ErrPayloadTooLarge = errors.New("gemini: response payload too large")
	ErrInvalidMaxBytes = errors.New("gemini: invalid byte limit")

	serviceTierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Usage contains only provider-reported observations. Request/effective/billed
// policy and costs are resolved by the gateway usage owner, not this parser.
type Usage struct {
	ReportedServiceTier string
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	ThinkingTokens      *int64
}

type Result struct {
	Usage           Usage
	Terminal        bool
	Failed          bool
	Status          string
	ErrorCode       string
	ErrorMessage    string
	InteractionID   string
	Events          int
	MalformedEvents int
	Pending         bool
}

type JSONOptions struct {
	MaxBytes int
}

func ParseJSON(body []byte, options JSONOptions) (Result, error) {
	maxBytes, err := normalizedLimit(options.MaxBytes, DefaultJSONMaxBytes, MaxJSONBytes)
	if err != nil {
		return Result{}, err
	}
	if len(body) > maxBytes {
		return Result{}, ErrPayloadTooLarge
	}
	value, err := decodeJSONObject(body)
	if err != nil {
		return Result{}, err
	}
	result := resultFromObject(value)
	result.Terminal = true
	if result.Status == "" && result.Failed {
		result.Status = "failed"
	}
	return result, nil
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidJSON
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidJSON
	}
	return object, nil
}

func resultFromObject(value map[string]any) Result {
	result := Result{Usage: extractUsage(value)}
	interaction := objectValue(value["interaction"])
	result.InteractionID = validIdentifier(stringValue(interaction["id"]), false)
	if result.InteractionID == "" {
		result.InteractionID = validIdentifier(stringValue(value["interaction_id"]), false)
	}
	if result.InteractionID == "" {
		result.InteractionID = validIdentifier(stringValue(value["id"]), false)
	}
	result.Status = stringValue(interaction["status"])
	if result.Status == "" {
		result.Status = stringValue(value["status"])
	}
	errorObject := objectValue(value["error"])
	if errorObject == nil {
		errorObject = objectValue(interaction["error"])
	}
	if errorObject != nil {
		result.Failed = true
		result.ErrorCode = firstString(errorObject, "status", "code")
		result.ErrorMessage = stringValue(errorObject["message"])
	}
	if equalFold(result.Status, "failed") {
		result.Failed = true
	}
	return result
}

func extractUsage(value map[string]any) Usage {
	interaction := objectValue(value["interaction"])
	serviceTier := normalizedServiceTier(value["service_tier"])
	if serviceTier == "" {
		serviceTier = normalizedServiceTier(interaction["service_tier"])
	}

	var nested map[string]any
	if metadata := objectValue(value["metadata"]); metadata != nil {
		nested = objectValue(metadata["total_usage"])
	}
	for _, candidate := range []any{value["total_usage"], value["usageMetadata"], value["usage"], interaction["usage"]} {
		if nested == nil {
			nested = objectValue(candidate)
		}
	}
	if nested != nil {
		usage := extractUsage(nested)
		if serviceTier != "" {
			usage.ReportedServiceTier = serviceTier
		}
		return usage
	}

	candidateTokens := firstToken(value, "candidatesTokenCount", "totalOutputTokens", "total_output_tokens", "outputTokens", "output_tokens")
	thinkingTokens := firstToken(value, "thoughtsTokenCount", "totalThoughtTokens", "total_thought_tokens", "thoughtTokens", "thought_tokens")
	return Usage{
		ReportedServiceTier: serviceTier,
		InputTokens:         firstToken(value, "promptTokenCount", "totalInputTokens", "total_input_tokens", "inputTokens", "input_tokens"),
		OutputTokens:        sumTokens(candidateTokens, thinkingTokens),
		CacheReadTokens:     firstToken(value, "cachedContentTokenCount", "totalCachedTokens", "total_cached_tokens", "cachedTokens", "cached_tokens"),
		ThinkingTokens:      thinkingTokens,
	}
}

func mergeUsage(current, next Usage) Usage {
	if next.ReportedServiceTier != "" {
		current.ReportedServiceTier = next.ReportedServiceTier
	}
	if next.InputTokens != nil {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens != nil {
		current.OutputTokens = next.OutputTokens
	}
	if next.CacheReadTokens != nil {
		current.CacheReadTokens = next.CacheReadTokens
	}
	if next.ThinkingTokens != nil {
		current.ThinkingTokens = next.ThinkingTokens
	}
	return current
}

func firstToken(value map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		if token := tokenValue(value[key]); token != nil {
			return token
		}
	}
	return nil
}

func tokenValue(value any) *int64 {
	var raw string
	switch typed := value.(type) {
	case json.Number:
		raw = typed.String()
	case string:
		raw = typed
	default:
		return nil
	}
	if raw == "" {
		return nil
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return nil
		}
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func sumTokens(values ...*int64) *int64 {
	var total int64
	defined := false
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value > 0 && total > int64(^uint64(0)>>1)-*value {
			return nil
		}
		total += *value
		defined = true
	}
	if !defined {
		return nil
	}
	return &total
}

func normalizedServiceTier(value any) string {
	tier, ok := value.(string)
	if !ok || !serviceTierPattern.MatchString(tier) {
		return ""
	}
	return tier
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if output := stringValue(value[key]); output != "" {
			return output
		}
	}
	return ""
}

func validIdentifier(value string, model bool) string {
	if !identifierAllowed(value, model) {
		return ""
	}
	return value
}

func equalFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range len(left) {
		leftByte, rightByte := left[index], right[index]
		if leftByte >= 'A' && leftByte <= 'Z' {
			leftByte += 'a' - 'A'
		}
		if rightByte >= 'A' && rightByte <= 'Z' {
			rightByte += 'a' - 'A'
		}
		if leftByte != rightByte {
			return false
		}
	}
	return true
}

func normalizedLimit(value, defaultValue, maximum int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 1 || value > maximum {
		return 0, ErrInvalidMaxBytes
	}
	return value, nil
}
