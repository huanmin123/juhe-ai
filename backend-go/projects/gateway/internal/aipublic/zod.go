// zod v3 message mirrors and the request schema parsers for the
// /__aipublic__ family (external-integrations.routes.ts schemas). Every
// parser returns the first zod issue message verbatim so 400 bodies match
// Node byte-for-byte.
package aipublic

import (
	"fmt"
	"net/url"
	"strings"
)

const zodRequired = "Required"

func zodReceived(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func zodInvalidType(expected string, value any) string {
	return "Expected " + expected + ", received " + zodReceived(value)
}

func zodStringMin(n int) string {
	return fmt.Sprintf("String must contain at least %d character(s)", n)
}

func zodStringMax(n int) string {
	return fmt.Sprintf("String must contain at most %d character(s)", n)
}

func zodNumberMin(n int) string {
	return fmt.Sprintf("Number must be greater than or equal to %d", n)
}

func zodNumberMax(n int) string {
	return fmt.Sprintf("Number must be less than or equal to %d", n)
}

func zodEnumMessage(options []string, received string) string {
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = "'" + option + "'"
	}
	return "Invalid enum value. Expected " + strings.Join(quoted, " | ") + ", received '" + received + "'"
}

func zodUnrecognizedKeys(keys ...string) string {
	sorted := append([]string{}, keys...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return "Unrecognized key(s) in object: " + strings.Join(sorted, ", ")
}

// ---------------------------------------------------------------------------
// Query parsing (req.query values; express collapses repeated params to the
// first value for plain zod record parsing, so only url.Values.Get order
// matters — the first value wins like zod's object parse over express 5).
// ---------------------------------------------------------------------------

// parseQueryString mirrors z.string().trim().min(min).max(max) over one query
// value; missing -> ("" , required issue) when required, else ("" , nil).
func parseQueryString(values url.Values, key string, required bool, min, max int) (string, string) {
	raw := values[key]
	if len(raw) == 0 {
		if required {
			return "", zodRequired
		}
		return "", ""
	}
	text := raw[0]
	trimmed := strings.TrimSpace(text)
	if runeLen(trimmed) < min {
		return "", zodStringMin(min)
	}
	if max > 0 && runeLen(trimmed) > max {
		return "", zodStringMax(max)
	}
	return trimmed, ""
}

func parseOptionalQueryString(values url.Values, key string, min, max int) (string, bool, string) {
	raw, exists := values[key]
	if !exists || len(raw) == 0 {
		return "", false, ""
	}
	text := raw[0]
	trimmed := strings.TrimSpace(text)
	if trimmed == "" && min > 0 {
		return "", false, zodStringMin(min)
	}
	if runeLen(trimmed) < min {
		return "", false, zodStringMin(min)
	}
	if max > 0 && runeLen(trimmed) > max {
		return "", false, zodStringMax(max)
	}
	return trimmed, true, ""
}

// parseOptionalQueryEnum mirrors z.enum([...]).optional().
func parseOptionalQueryEnum(values url.Values, key string, options []string) (string, bool, string) {
	raw, exists := values[key]
	if !exists || len(raw) == 0 {
		return "", false, ""
	}
	text := raw[0]
	if !containsString(options, text) {
		return "", false, zodEnumMessage(options, text)
	}
	return text, true, ""
}

// parseOptionalQueryInt mirrors z.coerce.number().int().min(1)[.max(n)].optional().
func parseOptionalQueryInt(values url.Values, key string, min, max int) (int, bool, string) {
	raw, exists := values[key]
	if !exists || len(raw) == 0 {
		return 0, false, ""
	}
	text := raw[0]
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		// Number('') === 0 in the coerce path.
		if min > 0 {
			return 0, false, zodNumberMin(min)
		}
		return 0, true, ""
	}
	value, ok := coerceNumber(trimmed)
	if !ok {
		return 0, false, "Expected number, received nan"
	}
	intValue, isInt := value.(int)
	if !isInt {
		return 0, false, "Expected integer, received float"
	}
	if intValue < min {
		return 0, false, zodNumberMin(min)
	}
	if max > 0 && intValue > max {
		return 0, false, zodNumberMax(max)
	}
	return intValue, true, ""
}

// coerceNumber mirrors Number(text) plus the integer check.
func coerceNumber(text string) (any, bool) {
	value, err := strconvParseFloat(text)
	if err != nil {
		return nil, false
	}
	if value != float64(int64(value)) {
		return value, true // float: the caller renders the integer issue
	}
	return int(value), true
}

// ---------------------------------------------------------------------------
// Body field helpers (strict objects are checked by the caller against the
// known key set before these run).
// ---------------------------------------------------------------------------

func bodyHas(body map[string]any, key string) bool {
	_, exists := body[key]
	return exists
}

func bodyString(value any) (string, bool) {
	text, isString := value.(string)
	if !isString {
		return "", false
	}
	return text, true
}

// trimmedBodyString mirrors z.string().trim().min(min).max(max): absent ->
// ok(nil); null -> invalid_type issue (unless nullable).
func trimmedBodyString(value any, present bool, min, max int) (*string, string) {
	if !present {
		return nil, ""
	}
	text, isString := value.(string)
	if !isString {
		return nil, zodInvalidType("string", value)
	}
	trimmed := strings.TrimSpace(text)
	if runeLen(trimmed) < min {
		return nil, zodStringMin(min)
	}
	if max > 0 && runeLen(trimmed) > max {
		return nil, zodStringMax(max)
	}
	return &trimmed, ""
}

// nullableTrimmedBodyString mirrors z.string().trim().max(n).nullable().optional().
func nullableTrimmedBodyString(value any, present bool, max int) (*string, string) {
	if !present {
		return nil, ""
	}
	if value == nil {
		return nil, ""
	}
	text, isString := value.(string)
	if !isString {
		return nil, zodInvalidType("string", value)
	}
	trimmed := strings.TrimSpace(text)
	if max > 0 && runeLen(trimmed) > max {
		return nil, zodStringMax(max)
	}
	return &trimmed, ""
}

func bodyOptionalString(value any, present bool) (string, bool, string) {
	if !present {
		return "", false, ""
	}
	text, isString := value.(string)
	if !isString {
		return "", false, zodInvalidType("string", value)
	}
	return text, true, ""
}

func bodyOptionalBool(value any, present bool) (bool, bool, string) {
	if !present {
		return false, false, ""
	}
	flag, isBool := value.(bool)
	if !isBool {
		return false, false, zodInvalidType("boolean", value)
	}
	return flag, true, ""
}

func bodyOptionalInt(value any, present bool, min, max int) (int, bool, string) {
	if !present {
		return 0, false, ""
	}
	number, isNumber := value.(float64)
	if !isNumber {
		return 0, false, zodInvalidType("number", value)
	}
	if number != float64(int64(number)) {
		return 0, false, "Expected integer, received float"
	}
	intValue := int(number)
	if intValue < min {
		return 0, false, zodNumberMin(min)
	}
	if max > 0 && intValue > max {
		return 0, false, zodNumberMax(max)
	}
	return intValue, true, ""
}

func bodyOptionalEnum(value any, present bool, options []string) (string, bool, string) {
	if !present {
		return "", false, ""
	}
	text, isString := value.(string)
	if !isString {
		return "", false, zodInvalidType("string", value)
	}
	if !containsString(options, text) {
		return "", false, zodEnumMessage(options, text)
	}
	return text, true, ""
}

// strictObjectKeys reports unknown keys (zod .strict()).
func strictObjectKeys(body map[string]any, allowed ...string) []string {
	var unknown []string
	for key := range body {
		if !containsString(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sortStrings(unknown)
	return unknown
}

// ---------------------------------------------------------------------------
// Required/optional body string chains (z.string().trim().min(a).max(b)).
// ---------------------------------------------------------------------------

// requiredTrimmedBody mirrors a required z.string().trim().min(min).max(max)
// field: absent -> Required, non-string -> invalid_type, then trim/length.
func requiredTrimmedBody(body map[string]any, key string, min, max int) (string, string) {
	value, present := body[key]
	if !present {
		return "", zodRequired
	}
	trimmed, issue := trimmedBodyString(value, true, min, max)
	if issue != "" {
		return "", issue
	}
	if trimmed == nil {
		return "", zodRequired
	}
	return *trimmed, ""
}

// optionalTrimmedBody mirrors z.string().trim().min(min).max(max).optional().
func optionalTrimmedBody(body map[string]any, key string, min, max int) (*string, string) {
	value, present := body[key]
	if !present || value == nil {
		// zod treats an explicit null on a non-nullable string as invalid_type.
		if present && value == nil {
			return nil, zodInvalidType("string", value)
		}
		return nil, ""
	}
	return trimmedBodyString(value, true, min, max)
}

// nullableTrimmedBodyField mirrors z.string().trim().max(n).nullable().optional().
func nullableTrimmedBodyField(body map[string]any, key string, max int) (*string, string) {
	value, present := body[key]
	if !present {
		return nil, ""
	}
	return nullableTrimmedBodyString(value, true, max)
}

func bodyOptionalBoolField(body map[string]any, key string) (bool, bool, string) {
	value, present := body[key]
	if !present {
		return false, false, ""
	}
	return bodyOptionalBool(value, true)
}

func bodyOptionalEnumField(body map[string]any, key string, options []string) (string, bool, string) {
	value, present := body[key]
	if !present {
		return "", false, ""
	}
	return bodyOptionalEnum(value, true, options)
}

// hasAnyField mirrors the zod refine hasOwnProperty checks.
func hasAnyField(body map[string]any, keys []string) bool {
	for _, key := range keys {
		if _, exists := body[key]; exists {
			return true
		}
	}
	return false
}
