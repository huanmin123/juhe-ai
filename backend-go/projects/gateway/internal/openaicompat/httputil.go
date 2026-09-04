package openaicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// parseJSNumber mirrors JavaScript Number(text) for the string branches of
// queryInteger/queryNumber: ParseFloat covers decimal, exponent and sign
// forms; empty input is NaN; non-finite results count as NaN like
// Number.isFinite consumers expect.
func parseJSNumber(text string) (float64, bool) {
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

// queryStringParam mirrors queryString(req.query.x): only single string
// values count (repeated params become arrays in express and are ignored),
// and blank results are undefined.
func queryStringParam(query url.Values, name string) *string {
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

// queryIntegerParam mirrors queryInteger(req.query.x): string numbers are
// truncated; anything non-finite is undefined.
func queryIntegerParam(query url.Values, name string) *int {
	text := queryStringParam(query, name)
	if text == nil {
		return nil
	}
	return integerFromNumber(*text)
}

func integerFromNumber(text string) *int {
	number, ok := parseJSNumber(text)
	if !ok {
		return nil
	}
	truncated := int(number)
	return &truncated
}

// queryIntegerValue mirrors queryInteger over a decoded JSON value: numbers
// go through String(value) -> Number, strings through queryString.
func queryIntegerValue(value any) *int {
	switch typed := value.(type) {
	case float64:
		return intFromFloat(typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		return integerFromNumber(text)
	default:
		return nil
	}
}

func intFromFloat(number float64) *int {
	// Node truncates any finite float; values beyond the float64-safe integer
	// range never occur through the JSON decode path.
	if number != number || number > 9.007199254740991e15 || number < -9.007199254740991e15 {
		return nil
	}
	truncated := int(number)
	return &truncated
}

// queryNumberValue mirrors queryNumber over a decoded JSON value.
func queryNumberValue(value any) *float64 {
	switch typed := value.(type) {
	case float64:
		return &typed
	case string:
		number, ok := parseJSNumber(typed)
		if !ok {
			return nil
		}
		return &number
	default:
		return nil
	}
}

// stringValue mirrors stringValue: trimmed non-empty strings only.
func stringValue(value any) *string {
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

// objectValue mirrors objectValue: plain JSON objects only.
func objectValue(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return nil
}

// readJSONObjectBody mirrors readJsonObjectBody in vector-stores.routes.ts:
// a 1 MiB reading cap (413 request_body_too_large), an empty body defaulting
// to {}, and distinct errors for invalid JSON vs non-object JSON.
func readJSONObjectBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil {
		return map[string]any{}, nil
	}
	limited := io.LimitReader(r.Body, jsonBodyLimit+1)
	buffer, err := io.ReadAll(limited)
	if err != nil {
		return nil, errUnhandled
	}
	if int64(len(buffer)) > jsonBodyLimit {
		return nil, newRequestError("JSON 请求体过大", 413, "request_too_large", "request_body_too_large")
	}
	text := strings.TrimSpace(string(buffer))
	if text == "" {
		return map[string]any{}, nil
	}
	var parsed any
	if jsonErr := json.Unmarshal([]byte(text), &parsed); jsonErr != nil {
		return nil, badRequest("JSON 请求体无效", "invalid_json_body")
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil, badRequest("JSON 请求体必须是对象", "invalid_json_body")
	}
	return record, nil
}

const jsonBodyLimit = 1024 * 1024
