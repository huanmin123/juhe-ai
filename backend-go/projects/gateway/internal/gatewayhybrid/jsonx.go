package gatewayhybrid

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// This file ports the JSON primitives the Node hybrid modules rely on:
// JSON.stringify key order (insertion order), undefined-vs-null semantics,
// Number() coercion, UTF-16 length/substring semantics and JSON.parse
// preserving key order.

// Undefined mirrors JavaScript `undefined`: JSON.stringify drops object keys
// carrying it. Distinguish from nil, which renders as JSON null.
type undefinedType struct{}

// Undefined mirrors JS undefined.
var Undefined = undefinedType{}

// IsUndefined reports whether the value is the Undefined sentinel.
func IsUndefined(value any) bool {
	_, ok := value.(undefinedType)
	return ok
}

// OrderedJSON preserves JavaScript object key insertion order so that
// JSON rendering is byte-identical to Node JSON.stringify for the payloads
// hybrid routing produces (scoring/quality request bodies, contexts,
// diagnostics).
type OrderedJSON struct {
	keys   []string
	values map[string]any
}

func NewOrderedJSON() *OrderedJSON {
	return &OrderedJSON{values: map[string]any{}}
}

// Set appends the key or updates an existing key in place (JS object
// semantics: re-assigning an existing key keeps its original position).
func (o *OrderedJSON) Set(key string, value any) {
	if o.values == nil {
		o.values = map[string]any{}
	}
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *OrderedJSON) Get(key string) (any, bool) {
	value, ok := o.values[key]
	return value, ok
}

// GetString returns the string value for key.
func (o *OrderedJSON) GetString(key string) (string, bool) {
	value, ok := o.values[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func (o *OrderedJSON) Delete(key string) {
	if _, exists := o.values[key]; !exists {
		return
	}
	delete(o.values, key)
	for index, candidate := range o.keys {
		if candidate == key {
			o.keys = append(o.keys[:index], o.keys[index+1:]...)
			break
		}
	}
}

func (o *OrderedJSON) Len() int {
	return len(o.keys)
}

func (o *OrderedJSON) Keys() []string {
	return append([]string(nil), o.keys...)
}

func (o *OrderedJSON) Clone() *OrderedJSON {
	cloned := NewOrderedJSON()
	for _, key := range o.keys {
		cloned.Set(key, cloneJSONValue(o.values[key]))
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case *OrderedJSON:
		return typed.Clone()
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJSONValue(item)
		}
		return cloned
	default:
		return typed
	}
}

// IsJSONObject reports whether the value behaves like a non-array JS object.
func IsJSONObject(value any) bool {
	_, ok := value.(*OrderedJSON)
	return ok
}

// IsArray reports whether the value behaves like a JS array.
func IsArray(value any) bool {
	_, ok := value.([]any)
	return ok
}

// ParseJSONOrdered decodes JSON preserving object key insertion order
// (JSON.parse in V8 keeps insertion order for string keys). Numbers decode
// as float64 exactly like JSON.parse.
func ParseJSONOrdered(data []byte) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("trailing content after JSON value")
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedFromToken(decoder, token)
}

func decodeOrderedFromToken(decoder *json.Decoder, token json.Token) (any, error) {
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := NewOrderedJSON()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Set(key, value)
			}
			if _, err := decoder.Token(); err != nil { // consume '}'
				return nil, err
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if _, err := decoder.Token(); err != nil { // consume ']'
				return nil, err
			}
			return array, nil
		}
		return nil, fmt.Errorf("unexpected delimiter")
	case json.Number:
		return typed.Float64()
	default:
		return typed, nil
	}
}

// NodeJSONStringify renders value exactly like Node JSON.stringify for the
// JSON value domain: OrderedJSON objects (insertion order, Undefined keys
// dropped), []any arrays, string, float64, bool, nil (JSON null). Go maps
// fall back to sorted keys (deterministic; hybrid payloads always use
// OrderedJSON).
func NodeJSONStringify(value any) string {
	var builder strings.Builder
	writeNodeJSON(&builder, value)
	return builder.String()
}

func writeNodeJSON(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case undefinedType:
		builder.WriteString("null")
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writeNodeJSONString(builder, typed)
	case float64:
		builder.WriteString(nodeNumberText(typed))
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case *OrderedJSON:
		builder.WriteByte('{')
		first := true
		for _, key := range typed.keys {
			item := typed.values[key]
			if IsUndefined(item) {
				continue
			}
			if !first {
				builder.WriteByte(',')
			}
			first = false
			writeNodeJSONString(builder, key)
			builder.WriteByte(':')
			writeNodeJSON(builder, item)
		}
		builder.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		first := true
		for _, key := range keys {
			item := typed[key]
			if IsUndefined(item) {
				continue
			}
			if !first {
				builder.WriteByte(',')
			}
			first = false
			writeNodeJSONString(builder, key)
			builder.WriteByte(':')
			writeNodeJSON(builder, item)
		}
		builder.WriteByte('}')
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteByte(',')
			}
			writeNodeJSON(builder, item)
		}
		builder.WriteByte(']')
	default:
		builder.WriteString("null")
	}
}

// nodeNumberText mirrors JSON.stringify number output: integral values print
// without a decimal part.
func nodeNumberText(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "null"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// writeNodeJSONString mirrors JSON.stringify string escaping: minimal JSON
// escapes, HTML characters untouched.
func writeNodeJSONString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, symbol := range text {
		switch symbol {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if symbol < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, symbol))
			} else {
				builder.WriteRune(symbol)
			}
		}
	}
	builder.WriteByte('"')
}

// NodeNumber mirrors the global Number() coercion for JSON values: numbers
// pass through, booleans map to 1/0, null maps to 0, strings parse after
// trimming (empty string is 0), objects/arrays are NaN. ok=false means NaN.
func NodeNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case nil:
		return 0, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, true
		}
		switch trimmed {
		case "Infinity", "+Infinity":
			return math.Inf(1), true
		case "-Infinity":
			return math.Inf(-1), true
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// ---- OrderedJSON navigation helpers shared by scoring/quality parsing ----

// OrderedChild returns the object value at key.
func OrderedChild(object *OrderedJSON, key string) *OrderedJSON {
	if object == nil {
		return nil
	}
	value, ok := object.Get(key)
	if !ok {
		return nil
	}
	child, _ := value.(*OrderedJSON)
	return child
}

// OrderedChildArray returns the array value at key.
func OrderedChildArray(object *OrderedJSON, key string) []any {
	if object == nil {
		return nil
	}
	value, ok := object.Get(key)
	if !ok {
		return nil
	}
	array, _ := value.([]any)
	return array
}

// OrderedChildObjectAtIndex mirrors `object[key][index]` for object entries.
func OrderedChildObjectAtIndex(object *OrderedJSON, key string, index int) *OrderedJSON {
	array := OrderedChildArray(object, key)
	if index < 0 || index >= len(array) {
		return nil
	}
	entry, _ := array[index].(*OrderedJSON)
	return entry
}

// OrderedValue returns the raw value at key (nil when absent).
func OrderedValue(object *OrderedJSON, key string) any {
	if object == nil {
		return nil
	}
	value, ok := object.Get(key)
	if !ok {
		return nil
	}
	return value
}

// OrderedValueOrUndefined returns the value at key, or the Undefined
// sentinel when absent (JS `object.key` on a missing key).
func OrderedValueOrUndefined(object *OrderedJSON, key string) any {
	if object == nil {
		return Undefined
	}
	if value, ok := object.Get(key); ok {
		return value
	}
	return Undefined
}

// OrderedString returns the string value at key ("" when absent/not string).
func OrderedString(object *OrderedJSON, key string) string {
	if object == nil {
		return ""
	}
	value, ok := object.Get(key)
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// utf16Length mirrors JavaScript string.length (UTF-16 code units).
func utf16Length(text string) int {
	units := 0
	for _, symbol := range text {
		if symbol > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// truncateUTF16 mirrors `text.slice(0, maxChars)` with UTF-16 semantics.
func truncateUTF16(text string, maxChars int) string {
	if utf16Length(text) <= maxChars {
		return text
	}
	units := 0
	for index, symbol := range text {
		width := 1
		if symbol > 0xFFFF {
			width = 2
		}
		if units+width > maxChars {
			return text[:index]
		}
		units += width
	}
	return text
}

// optionalStringPointer mirrors `typeof value === 'string' ? value : undefined`.
func optionalStringPointer(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}
