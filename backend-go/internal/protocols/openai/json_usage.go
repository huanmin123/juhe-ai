package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	ErrJSONUsageInvalid  = errors.New("openai JSON usage response 无效")
	ErrJSONUsageTooLarge = errors.New("openai JSON usage response 超过限制")
)

const (
	DefaultJSONUsageMaxBytes int64 = 16 << 20
	MaxJSONUsageBytes        int64 = 64 << 20
)

// ParseJSONUsage extracts provider-independent usage from one bounded
// OpenAI-compatible JSON response. It deliberately does not validate endpoint
// semantics; protocol guards and the response owner retain that responsibility.
func ParseJSONUsage(raw []byte, maxBytes int64) (SSEUsage, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultJSONUsageMaxBytes
	}
	if maxBytes > MaxJSONUsageBytes {
		maxBytes = MaxJSONUsageBytes
	}
	if int64(len(raw)) > maxBytes {
		return SSEUsage{}, fmt.Errorf("%w: limit=%d", ErrJSONUsageTooLarge, maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return SSEUsage{}, fmt.Errorf("%w: %v", ErrJSONUsageInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return SSEUsage{}, fmt.Errorf("%w: trailing JSON value", ErrJSONUsageInvalid)
		}
		return SSEUsage{}, fmt.Errorf("%w: %v", ErrJSONUsageInvalid, err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return SSEUsage{}, fmt.Errorf("%w: response must be an object", ErrJSONUsageInvalid)
	}
	return extractSSEUsage(root), nil
}
