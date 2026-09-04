package usagewriter

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// JSON helpers mirroring gateway/internal/gatewayusage/jsonx.go (duplicated
// because the gateway and jobs Go modules cannot import each other).

// OrderedObject is an insertion-ordered JSON object. Usage snapshots are
// byte-persisted, so construction order must survive serialization exactly
// like JS object insertion order.
type OrderedObject struct {
	keys   []string
	values map[string]any
}

// NewOrderedObject builds an empty ordered object.
func NewOrderedObject() *OrderedObject {
	return &OrderedObject{values: map[string]any{}}
}

// Set inserts or replaces a key, preserving first-insertion position for
// replacements (JS semantics).
func (o *OrderedObject) Set(key string, value any) *OrderedObject {
	if o.values == nil {
		o.values = map[string]any{}
	}
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
	return o
}

// Get returns the value behind key.
func (o *OrderedObject) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

// Has reports whether key exists.
func (o *OrderedObject) Has(key string) bool {
	if o == nil {
		return false
	}
	_, exists := o.values[key]
	return exists
}

// Len returns the key count.
func (o *OrderedObject) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys returns the ordered key list.
func (o *OrderedObject) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// MarshalJSON emits keys in insertion order.
func (o *OrderedObject) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("null"), nil
	}
	var builder strings.Builder
	builder.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		builder.Write(keyJSON)
		builder.WriteByte(':')
		valueJSON, err := json.Marshal(o.values[key])
		if err != nil {
			return nil, err
		}
		builder.Write(valueJSON)
	}
	builder.WriteByte('}')
	return []byte(builder.String()), nil
}

// UnmarshalJSON decodes an object preserving the encoded key order.
func (o *OrderedObject) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return strconv.ErrSyntax
	}
	o.values = map[string]any{}
	o.keys = nil
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, _ := keyToken.(string)
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		o.Set(key, normalizeDecodedJSONValue(value))
	}
	_, err = decoder.Token()
	return err
}

func normalizeDecodedJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		ordered := NewOrderedObject()
		// json.Decode into map loses order; sort for determinism.
		for _, key := range sortedMapKeys(typed) {
			ordered.Set(key, normalizeDecodedJSONValue(typed[key]))
		}
		return ordered
	case []any:
		for index, item := range typed {
			typed[index] = normalizeDecodedJSONValue(item)
		}
		return typed
	default:
		return value
	}
}

func sortedMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// formatFloat mirrors the JS Number string form closely enough for byte
// estimation (shortest round-trip form, no exponent for typical magnitudes).
func formatFloat(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
