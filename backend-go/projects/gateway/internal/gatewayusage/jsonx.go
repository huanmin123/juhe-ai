package gatewayusage

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// timeRFC3339Millis is the ISO string format Node produces with
// new Date(...).toISOString(): millisecond precision, Z suffix.
const timeRFC3339Millis = "2006-01-02T15:04:05.000Z07:00"

// OrderedObject is an insertion-ordered JSON object. Audit metadata bodies
// and usage snapshots are byte-persisted, so construction order must
// survive serialization exactly like JS object insertion order.
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

// Clone deep-copies nested OrderedObjects so callers cannot mutate
// already-built payloads.
func (o *OrderedObject) Clone() *OrderedObject {
	if o == nil {
		return nil
	}
	cloned := NewOrderedObject()
	for _, key := range o.keys {
		cloned.Set(key, cloneJSONValue(o.values[key]))
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case *OrderedObject:
		return typed.Clone()
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJSONValue(item)
		}
		return cloned
	default:
		return value
	}
}

// AsMap converts to a plain map (log-field adapter; order is not preserved).
func (o *OrderedObject) AsMap() map[string]any {
	out := make(map[string]any, o.Len())
	if o == nil {
		return out
	}
	for _, key := range o.keys {
		out[key] = o.values[key]
	}
	return out
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

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
