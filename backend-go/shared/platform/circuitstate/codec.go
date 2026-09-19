package circuitstate

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// decodeStrict parses a Lua cjson response. Lua encodes an empty array as
// `{}`, so relatedStates is decoded leniently via stringList-style tolerance;
// UseNumber keeps integer precision.
func DecodeStrict(encoded string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func RedisStringResult(raw any) (string, bool) {
	switch typed := raw.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	}
	return "", false
}

func NumericRedisResult(raw any) (int64, error) {
	switch typed := raw.(type) {
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case string:
		value, ok := ParseSafeInteger(typed)
		if !ok {
			return 0, errors.New("Redis 账户电路数值返回无效")
		}
		return int64(value), nil
	}
	return 0, errors.New("Redis 账户电路数值返回无效")
}

// ParseSafeInteger mirrors Number(value) + Number.isSafeInteger checks for
// dispatch revision strings. Revision values are decimal numbers ("3") or
// opaque digests ("v1:<sha256>"); anything Number() would reject stays false.
func ParseSafeInteger(value string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number != math.Trunc(number) || math.Abs(number) > 9007199254740991 {
		return 0, false
	}
	return number, true
}

// PointerNowMs extracts the nowMs value from a payload map that may carry it
// as int64 or *int64.
func PointerNowMs(payload map[string]any) *int64 {
	switch value := payload["nowMs"].(type) {
	case int64:
		return &value
	case *int64:
		return value
	}
	return nil
}

// NormalizedNowValue mirrors normalizedNow: clamps negatives to zero (the
// finite-number check from Node is unrepresentable for int64 inputs).
func NormalizedNowValue(nowMs *int64, fallback func() int64) int64 {
	value := int64(0)
	if nowMs != nil {
		value = *nowMs
	} else if fallback != nil {
		value = fallback()
	}
	if value < 0 {
		return 0
	}
	return value
}

func CursorString(value any, fallback string) string {
	switch typed := value.(type) {
	case string:
		if typed != "" {
			return typed
		}
	case float64:
		return fmt.Sprintf("%d", int64(typed))
	case json.Number:
		return typed.String()
	}
	return fallback
}

func PayloadInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	}
	return 0, false
}

func PositiveInteger(value int64, name string) (int64, error) {
	if value < 1 {
		return 0, fmt.Errorf("账户电路 %s 必须是正整数", name)
	}
	return value, nil
}

func RequiredValue(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路操作缺少 %s", name)
	}
	return normalized, nil
}

func RequiredScopePart(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路作用域缺少 %s", name)
	}
	return normalized, nil
}

func RequiredPayloadString(input map[string]any, key string) (string, error) {
	value, _ := input[key].(string)
	normalized, err := RequiredValue(value, key)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func RequiredEvidenceKeyPayload(input map[string]any, key string) error {
	value, _ := input[key].(string)
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return fmt.Errorf("账户电路操作缺少 %s", key)
	}
	if !IsSHA256Hex(normalized) {
		return errors.New("账户电路 failureEvidenceKey 必须是 SHA256")
	}
	return nil
}

func EncodedScopeKey(parts ...string) string {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = fmt.Sprintf("%d:%s", len(part), part)
	}
	return strings.Join(encoded, "|")
}

func IsSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// SHA1Hex returns the lowercase hex SHA-1 digest (Node createHash('sha1')).
func SHA1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Int64Min(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func StrPtr(value string) *string { return &value }

func BoolPtr(value bool) *bool { return &value }

func DerefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
