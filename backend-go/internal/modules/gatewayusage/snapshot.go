package gatewayusage

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxSnapshotBytes       = 64 * 1024
	MaxSnapshotStringBytes = 16 * 1024
	MaxSnapshotArrayItems  = 50
	MaxSnapshotObjectKeys  = 80
	MaxSnapshotDepth       = 6
	MaxSnapshotNodes       = 20_000
)

type Snapshot = json.RawMessage

type snapshotSanitizer struct {
	seen      map[visit]struct{}
	truncated bool
	remaining int
	nodes     int
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func SanitizeSnapshot(value any) Snapshot {
	if value == nil {
		return nil
	}
	sanitizer := snapshotSanitizer{
		seen:      make(map[visit]struct{}),
		remaining: MaxSnapshotBytes,
	}
	result := sanitizer.value(reflect.ValueOf(value), 0)
	if object, ok := result.(map[string]any); ok && sanitizer.truncated {
		object["_truncated"] = true
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Snapshot(`{"_truncated":true,"_reason":"json_marshal_failed"}`)
	}
	if len(encoded) > MaxSnapshotBytes {
		fallback, _ := json.Marshal(map[string]any{
			"_truncated":     true,
			"_originalBytes": len(encoded),
		})
		return Snapshot(fallback)
	}
	return Snapshot(encoded)
}

func (s *snapshotSanitizer) value(value reflect.Value, depth int) any {
	if !value.IsValid() {
		return nil
	}
	if s.remaining <= 0 || s.nodes >= MaxSnapshotNodes {
		s.truncated = true
		return "[truncated]"
	}
	s.nodes++
	s.remaining--
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return boundedUTF8String(value.Interface().(time.Time).UTC().Format(time.RFC3339Nano), MaxSnapshotStringBytes, s)
	}
	switch value.Kind() {
	case reflect.String:
		return boundedUTF8String(value.String(), MaxSnapshotStringBytes, s)
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if s.cyclic(value) {
			return "[circular]"
		}
		return s.value(value.Elem(), depth)
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if depth >= MaxSnapshotDepth {
			s.truncated = true
			return "[depth_truncated]"
		}
		if s.cyclic(value) {
			return "[circular]"
		}
		return s.mapValue(value, depth)
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return map[string]any{
				"_buffer":   true,
				"bytes":     value.Len(),
				"truncated": value.Len() > MaxSnapshotStringBytes,
			}
		}
		fallthrough
	case reflect.Array:
		if depth >= MaxSnapshotDepth {
			s.truncated = true
			return "[depth_truncated]"
		}
		if value.Kind() == reflect.Slice && s.cyclic(value) {
			return "[circular]"
		}
		return s.arrayValue(value, depth)
	case reflect.Struct:
		if depth >= MaxSnapshotDepth {
			s.truncated = true
			return "[depth_truncated]"
		}
		return s.structValue(value, depth)
	default:
		return fmt.Sprintf("[unsupported:%s]", value.Kind())
	}
}

func (s *snapshotSanitizer) mapValue(value reflect.Value, depth int) any {
	if value.Len() > MaxSnapshotObjectKeys {
		s.truncated = true
		s.consume(32)
		return map[string]any{"_truncated": true, "_originalKeys": value.Len()}
	}
	keys := make([]string, 0, value.Len())
	values := make(map[string]reflect.Value, value.Len())
	iter := value.MapRange()
	for iter.Next() {
		key := iter.Key()
		var text string
		if key.Kind() == reflect.String {
			text = key.String()
		} else {
			text = fmt.Sprint(key.Interface())
		}
		keys = append(keys, text)
		values[text] = iter.Value()
	}
	sort.Strings(keys)
	output := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		if !s.consume(len(key) + 4) {
			break
		}
		output[key] = s.value(values[key], depth+1)
	}
	if s.truncated {
		output["_truncated"] = true
	}
	return output
}

func (s *snapshotSanitizer) arrayValue(value reflect.Value, depth int) any {
	limit := min(value.Len(), MaxSnapshotArrayItems)
	output := make([]any, 0, limit+1)
	for index := 0; index < limit; index++ {
		if s.remaining <= 0 {
			break
		}
		output = append(output, s.value(value.Index(index), depth+1))
	}
	if value.Len() > len(output) {
		s.truncated = true
		output = append(output, fmt.Sprintf("[%d items truncated]", value.Len()-len(output)))
	}
	return output
}

func (s *snapshotSanitizer) structValue(value reflect.Value, depth int) any {
	typ := value.Type()
	keys := make([]string, 0, value.NumField())
	values := make(map[string]reflect.Value, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		keys = append(keys, name)
		values[name] = value.Field(index)
	}
	sort.Strings(keys)
	if len(keys) > MaxSnapshotObjectKeys {
		keys = keys[:MaxSnapshotObjectKeys]
		s.truncated = true
	}
	output := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		if !s.consume(len(key) + 4) {
			break
		}
		output[key] = s.value(values[key], depth+1)
	}
	if s.truncated {
		output["_truncated"] = true
	}
	return output
}

func (s *snapshotSanitizer) cyclic(value reflect.Value) bool {
	var pointer uintptr
	switch value.Kind() {
	case reflect.Map, reflect.Pointer, reflect.Slice:
		pointer = value.Pointer()
	}
	if pointer == 0 {
		return false
	}
	key := visit{typ: value.Type(), ptr: pointer}
	if _, found := s.seen[key]; found {
		s.truncated = true
		return true
	}
	s.seen[key] = struct{}{}
	return false
}

func boundedUTF8String(value string, maxBytes int, sanitizer *snapshotSanitizer) string {
	maxBytes = min(maxBytes, max(0, sanitizer.remaining))
	if len(value) <= maxBytes {
		sanitizer.consume(len(value))
		return value
	}
	sanitizer.truncated = true
	suffix := fmt.Sprintf("...[truncated %d bytes]", len(value)-maxBytes)
	limit := max(0, maxBytes-len(suffix))
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	result := value[:limit] + suffix
	sanitizer.consume(len(result))
	return result
}

func (s *snapshotSanitizer) consume(bytes int) bool {
	if bytes <= 0 {
		return true
	}
	if bytes > s.remaining {
		s.remaining = 0
		s.truncated = true
		return false
	}
	s.remaining -= bytes
	return true
}
