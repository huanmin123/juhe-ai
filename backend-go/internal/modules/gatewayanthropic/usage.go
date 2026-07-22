// Package gatewayanthropic extracts bounded Anthropic response facts for the
// provider-independent gateway usage owner. It performs no I/O or persistence.
package gatewayanthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"regexp"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

const (
	DefaultJSONMaxBytes = 16 << 20
	MaxJSONBytes        = 64 << 20
)

var (
	ErrInvalidJSON     = errors.New("anthropic: invalid JSON response")
	ErrPayloadTooLarge = errors.New("anthropic: response payload too large")
	ErrInvalidLimit    = errors.New("anthropic: invalid byte limit")

	serviceTierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type JSONOptions struct {
	MaxBytes int
}

// Result contains protocol observations only. Terminal is set only by a
// complete JSON response or an explicit SSE terminal event.
type Result struct {
	Usage           gatewayusage.UsageFacts
	Terminal        bool
	Failed          bool
	Status          string
	ErrorCode       string
	ErrorMessage    string
	Events          int64
	MalformedEvents int64
	Pending         bool
}

func ParseJSON(body []byte, options JSONOptions) (Result, error) {
	limit, err := normalizedLimit(options.MaxBytes, DefaultJSONMaxBytes, MaxJSONBytes)
	if err != nil {
		return Result{}, err
	}
	if len(body) > limit {
		return Result{}, ErrPayloadTooLarge
	}
	value, err := decodeJSONObject(body)
	if err != nil {
		return Result{}, err
	}
	result := resultFromObject(value)
	result.Terminal = true
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
	result := Result{Usage: usageFromEnvelope(value)}
	result.Status = stringValue(value["stop_reason"])
	errorObject := objectValue(value["error"])
	if errorObject != nil || equalFold(stringValue(value["type"]), "error") {
		result.Failed = true
		result.Status = "failed"
		result.ErrorCode = firstString(errorObject, "type", "code")
		result.ErrorMessage = stringValue(field(errorObject, "message"))
		if result.ErrorCode == "" {
			result.ErrorCode = firstString(value, "error_code", "code")
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = stringValue(value["message"])
		}
	}
	return result
}

func usageFromEnvelope(value map[string]any) gatewayusage.UsageFacts {
	if usage := objectValue(value["usage"]); usage != nil {
		return extractUsage(usage)
	}
	if message := objectValue(value["message"]); message != nil {
		if usage := objectValue(message["usage"]); usage != nil {
			return extractUsage(usage)
		}
	}
	return gatewayusage.UsageFacts{}
}

func extractUsage(usage map[string]any) gatewayusage.UsageFacts {
	cacheCreation := objectValue(usage["cache_creation"])
	cacheWrite5m := tokenValue(field(cacheCreation, "ephemeral_5m_input_tokens"))
	cacheWrite1h := tokenValue(field(cacheCreation, "ephemeral_1h_input_tokens"))
	cacheWrite := tokenValue(usage["cache_creation_input_tokens"])
	if cacheWrite == nil {
		cacheWrite = sumTokens(cacheWrite5m, cacheWrite1h)
	}
	outputDetails := objectValue(usage["output_tokens_details"])
	return gatewayusage.UsageFacts{
		ReportedServiceTier: normalizedServiceTier(usage["speed"]),
		InputTokens:         tokenValue(usage["input_tokens"]),
		OutputTokens:        tokenValue(usage["output_tokens"]),
		CacheReadTokens:     tokenValue(usage["cache_read_input_tokens"]),
		CacheWriteTokens:    cacheWrite,
		CacheWrite1hTokens:  cacheWrite1h,
		ThinkingTokens:      tokenValue(field(outputDetails, "thinking_tokens")),
	}
}

func tokenValue(value any) *int64 {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
	default:
		return nil
	}
	if text == "" {
		return nil
	}
	parsed, _, err := big.ParseFloat(text, 10, 256, big.ToZero)
	if err != nil || parsed.Sign() < 0 || parsed.IsInf() {
		return nil
	}
	integer, _ := parsed.Int(nil)
	if !integer.IsInt64() {
		return nil
	}
	result := integer.Int64()
	return &result
}

func sumTokens(values ...*int64) *int64 {
	var total int64
	found := false
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value > math.MaxInt64-total {
			return nil
		}
		total += *value
		found = true
	}
	if !found {
		return nil
	}
	return &total
}

func normalizedServiceTier(value any) string {
	text, ok := value.(string)
	if !ok || !serviceTierPattern.MatchString(text) {
		return ""
	}
	return text
}

func normalizedLimit(value, defaultValue, maximum int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 1 || value > maximum {
		return 0, ErrInvalidLimit
	}
	return value, nil
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func field(value map[string]any, key string) any {
	if value == nil {
		return nil
	}
	return value[key]
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringValue(field(value, key)); text != "" {
			return text
		}
	}
	return ""
}

func equalFold(left, right string) bool {
	return strings.EqualFold(left, right)
}
