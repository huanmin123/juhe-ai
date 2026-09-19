package openaicompatcore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ParseJSNumber mirrors JavaScript Number(text) for the string branches of
// queryInteger/queryNumber: ParseFloat covers decimal, exponent and sign
// forms; empty input is NaN; non-finite results count as NaN like
// Number.isFinite consumers expect.
func ParseJSNumber(text string) (float64, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// QueryStringParam mirrors queryString(req.query.x): only single string
// values count (repeated params become arrays in express and are ignored),
// and blank results are undefined.
func QueryStringParam(query url.Values, name string) *string {
	values, exists := query[name]
	if !exists || len(values) != 1 {
		return nil
	}
	text := strings.TrimSpace(values[0])
	if text == "" {
		return nil
	}
	return &text
}

// QueryIntegerParam mirrors queryInteger(req.query.x): string numbers are
// truncated; anything non-finite is undefined.
func QueryIntegerParam(query url.Values, name string) *int {
	text := QueryStringParam(query, name)
	if text == nil {
		return nil
	}
	return IntegerFromNumber(*text)
}

func IntegerFromNumber(text string) *int {
	number, ok := ParseJSNumber(text)
	if !ok {
		return nil
	}
	truncated := int(number)
	return &truncated
}

// QueryIntegerValue mirrors queryInteger over a decoded JSON value: numbers
// go through String(value) -> Number, strings through queryString.
func QueryIntegerValue(value any) *int {
	switch typed := value.(type) {
	case float64:
		return IntFromFloat(typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		return IntegerFromNumber(text)
	default:
		return nil
	}
}

func IntFromFloat(number float64) *int {
	// Node truncates any finite float; values beyond the float64-safe integer
	// range never occur through the JSON decode path.
	if number != number || number > 9.007199254740991e15 || number < -9.007199254740991e15 {
		return nil
	}
	truncated := int(number)
	return &truncated
}

// QueryNumberValue mirrors queryNumber over a decoded JSON value.
func QueryNumberValue(value any) *float64 {
	switch typed := value.(type) {
	case float64:
		return &typed
	case string:
		number, ok := ParseJSNumber(typed)
		if !ok {
			return nil
		}
		return &number
	default:
		return nil
	}
}

// StringValue mirrors stringValue: trimmed non-empty strings only.
func StringValue(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// ObjectValue mirrors objectValue: plain JSON objects only.
func ObjectValue(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return nil
}

// ReadJSONObjectBody mirrors readJsonObjectBody in vector-stores.routes.ts:
// a 1 MiB reading cap (413 request_body_too_large), an empty body defaulting
// to {}, and distinct errors for invalid JSON vs non-object JSON.
func ReadJSONObjectBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil {
		return map[string]any{}, nil
	}
	limited := io.LimitReader(r.Body, JSONBodyLimit+1)
	buffer, err := io.ReadAll(limited)
	if err != nil {
		return nil, ErrUnhandled
	}
	if int64(len(buffer)) > JSONBodyLimit {
		return nil, NewRequestError("JSON 请求体过大", 413, "request_too_large", "request_body_too_large")
	}
	text := strings.TrimSpace(string(buffer))
	if text == "" {
		return map[string]any{}, nil
	}
	var parsed any
	if jsonErr := json.Unmarshal([]byte(text), &parsed); jsonErr != nil {
		return nil, BadRequest("JSON 请求体无效", "invalid_json_body")
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil, BadRequest("JSON 请求体必须是对象", "invalid_json_body")
	}
	return record, nil
}

const JSONBodyLimit = 1024 * 1024
