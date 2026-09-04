// JSON serialization helpers mirroring Node JSON.stringify behavior for the
// capture snapshots: compact output, HTML characters unescaped, object keys in
// insertion order for the deterministic snapshot shapes, undefined values
// omitted from objects (null kept).
package publicapilogs

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
)

// undefinedValue mirrors JS undefined inside capture payloads: omitted from
// objects (JSON.stringify drops the key), null inside arrays.
type undefinedValue struct{}

// Undefined is the sentinel the capture builders use where Node would leave a
// field undefined.
var Undefined = undefinedValue{}

func isUndefined(value any) bool {
	_, ok := value.(undefinedValue)
	return ok
}

// snapshotObject is an insertion-ordered JSON object. Capture builders use it
// so stored request/response snapshots keep Node's key order (and therefore
// the same truncation previews and JSON byte sizes).
type snapshotObject struct {
	keys []string
	vals map[string]any
}

func newSnapshotObject() *snapshotObject {
	return &snapshotObject{vals: map[string]any{}}
}

func (o *snapshotObject) set(key string, value any) *snapshotObject {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
	return o
}

func (o *snapshotObject) get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// MarshalJSON renders `{"k":v,...}` in insertion order, skipping undefined
// values exactly like JSON.stringify.
func (o *snapshotObject) MarshalJSON() ([]byte, error) {
	buf := []byte{'{'}
	for index, key := range o.keys {
		value := o.vals[key]
		if isUndefined(value) {
			continue
		}
		if index > 0 && len(buf) > 1 {
			buf = append(buf, ',')
		}
		buf = appendJSONString(buf, key)
		buf = append(buf, ':')
		buf = appendJSONValue(buf, value)
	}
	return append(buf, '}'), nil
}

// marshalCompact serializes any capture value the way JSON.stringify would:
// compact, no HTML escaping, plain map keys sorted (Go maps have no insertion
// order), undefined null-ed outside objects.
func marshalCompact(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	buf := appendJSONValue([]byte(nil), value)
	if buf == nil {
		return "", errJSONUnsupported
	}
	return string(buf), nil
}

var errJSONUnsupported = jsonUnsupportedError{}

type jsonUnsupportedError struct{}

func (jsonUnsupportedError) Error() string { return "publicapilogs: value is not JSON serializable" }

func appendJSONValue(buf []byte, value any) []byte {
	switch typed := value.(type) {
	case nil:
		return append(buf, "null"...)
	case undefinedValue:
		return append(buf, "null"...)
	case string:
		return appendJSONString(buf, typed)
	case bool:
		if typed {
			return append(buf, "true"...)
		}
		return append(buf, "false"...)
	case float64:
		return appendJSONNumber(buf, typed)
	case float32:
		return appendJSONNumber(buf, float64(typed))
	case int:
		return strconv.AppendInt(buf, int64(typed), 10)
	case int8:
		return strconv.AppendInt(buf, int64(typed), 10)
	case int16:
		return strconv.AppendInt(buf, int64(typed), 10)
	case int32:
		return strconv.AppendInt(buf, int64(typed), 10)
	case int64:
		return strconv.AppendInt(buf, typed, 10)
	case uint:
		return strconv.AppendUint(buf, uint64(typed), 10)
	case uint32:
		return strconv.AppendUint(buf, uint64(typed), 10)
	case uint64:
		return strconv.AppendUint(buf, typed, 10)
	case json.Number:
		return append(buf, typed.String()...)
	case []byte:
		// Raw byte payloads mirror the Node Buffer snapshot shape.
		return appendBufferSnapshot(buf, typed)
	case []any:
		buf = append(buf, '[')
		for index, item := range typed {
			if index > 0 {
				buf = append(buf, ',')
			}
			buf = appendJSONValue(buf, item)
		}
		return append(buf, ']')
	case *snapshotObject:
		if typed == nil {
			return append(buf, "null"...)
		}
		text, err := typed.MarshalJSON()
		if err != nil {
			return buf
		}
		return append(buf, text...)
	case map[string]any:
		if typed == nil {
			return append(buf, "null"...)
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf = append(buf, '{')
		first := true
		for _, key := range keys {
			value := typed[key]
			if isUndefined(value) {
				continue
			}
			if !first {
				buf = append(buf, ',')
			}
			first = false
			buf = appendJSONString(buf, key)
			buf = append(buf, ':')
			buf = appendJSONValue(buf, value)
		}
		return append(buf, '}')
	}
	return appendJSONFallback(buf, value)
}

func appendJSONFallback(buf []byte, value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		// json.Marshal fails on channels/functions/cycles; snapshot builders
		// never pass those. Be defensive like safeJsonStringify ('').
		return buf
	}
	return append(buf, encoded...)
}

func appendJSONNumber(buf []byte, value float64) []byte {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return append(buf, "null"...)
	}
	if value == float64(int64(value)) && value < 1e15 && value > -1e15 {
		return strconv.AppendInt(buf, int64(value), 10)
	}
	return strconv.AppendFloat(buf, value, 'g', -1, 64)
}

// appendJSONString quotes like JSON.stringify: encoding/json already replaces
// invalid UTF-8 with U+FFFD and escapes controls; undo its extra HTML escapes.
func appendJSONString(buf []byte, value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return append(buf, '"', '"')
	}
	return append(buf, htmlUnescape(encoded)...)
}

// htmlUnescape reverses the <,>,& escaping encoding/json applies, which
// JSON.stringify never does.
func htmlUnescape(encoded []byte) []byte {
	if !bytes.Contains(encoded, []byte(`\u00`)) {
		return encoded
	}
	out := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if index+6 <= len(encoded) && encoded[index] == '\\' && encoded[index+1] == 'u' {
			switch string(encoded[index+2 : index+6]) {
			case "003c":
				out = append(out, '<')
				index += 6
				continue
			case "003e":
				out = append(out, '>')
				index += 6
				continue
			case "0026":
				out = append(out, '&')
				index += 6
				continue
			}
		}
		out = append(out, encoded[index])
		index++
	}
	return out
}

// appendBufferSnapshot mirrors cloneSnapshotBuffer's storage shape
// ({type:'Buffer', byteLength, preview, truncated}); the budget-aware walker
// in capture.go handles the remaining-bytes case, this fallback caps the
// preview at the fixed string budget.
func appendBufferSnapshot(buf []byte, value []byte) []byte {
	previewBytes := len(value)
	if previewBytes > publicAPISnapshotStringPreviewBytes {
		previewBytes = publicAPISnapshotStringPreviewBytes
	}
	buf = append(buf, `{"type":"Buffer","byteLength":`...)
	buf = strconv.AppendInt(buf, int64(len(value)), 10)
	buf = append(buf, `,"preview":`...)
	buf = appendJSONString(buf, sliceUTF8(string(value[:previewBytes]), previewBytes))
	buf = append(buf, `,"truncated":`...)
	if len(value) > previewBytes {
		buf = append(buf, "true"...)
	} else {
		buf = append(buf, "false"...)
	}
	return append(buf, '}')
}
