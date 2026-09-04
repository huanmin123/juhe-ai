package gatewayusage

import (
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// JSON-like byte estimation mirroring backend/src/shared/queue-size.ts and
// the snapshot bounding helpers of record-queue.service.ts.

const exactStringByteLengthMaxChars = 16 * 1024

// EstimateJSONLikeBytesOptions mirrors JsonLikeByteEstimateOptions; zero
// limits mean unbounded (Node Number.POSITIVE_INFINITY).
type EstimateJSONLikeBytesOptions struct {
	MaxBytes int
	MaxNodes int
}

type jsonLikeEstimateContext struct {
	seen      *identitySet
	total     int
	nodeCount int
	maxBytes  int
	maxNodes  int
}

// EstimateJSONLikeBytes mirrors estimateJsonLikeBytes over JSON-like Go
// values (nil, bool, string, numeric, []any, map[string]any, OrderedObject,
// []byte buffers, time.Time dates).
func EstimateJSONLikeBytes(value any, options EstimateJSONLikeBytesOptions) int {
	context := &jsonLikeEstimateContext{
		seen:     newIdentitySet(),
		maxBytes: normalizeEstimateLimit(options.MaxBytes),
		maxNodes: normalizeEstimateLimit(options.MaxNodes),
	}
	visitJSONLikeValue(value, context)
	return context.total
}

func visitJSONLikeValue(value any, context *jsonLikeEstimateContext) {
	if estimateLimitReached(context) {
		return
	}
	context.nodeCount++
	if estimateLimitReached(context) {
		return
	}
	switch typed := value.(type) {
	case nil:
		addEstimatedBytes(context, 4)
	case string:
		addEstimatedBytes(context, estimateStringBytes(typed, context)+2)
	case bool:
		addEstimatedBytes(context, len(boolText(typed)))
	case int:
		addEstimatedBytes(context, len(itoa(typed)))
	case int64:
		addEstimatedBytes(context, len(itoa64(typed)))
	case uint64:
		addEstimatedBytes(context, len(itoa64(int64(typed))))
	case float64:
		addEstimatedBytes(context, len(formatFloat(typed)))
	case []byte:
		addEstimatedBytes(context, len(typed))
	case time.Time:
		addEstimatedBytes(context, len(typed.UTC().Format(timeRFC3339Millis))+2)
	case []any:
		if circularRef(context, value) {
			addEstimatedBytes(context, 16)
			return
		}
		addEstimatedBytes(context, 2)
		for _, item := range typed {
			visitJSONLikeValue(item, context)
			addEstimatedBytes(context, 1)
			if estimateLimitReached(context) {
				return
			}
		}
	case *OrderedObject:
		if circularRef(context, value) {
			addEstimatedBytes(context, 16)
			return
		}
		addEstimatedBytes(context, 2)
		for _, key := range typed.Keys() {
			addEstimatedBytes(context, estimateStringBytes(key, context)+3)
			visitJSONLikeValue(typed.Get(key), context)
			addEstimatedBytes(context, 1)
			if estimateLimitReached(context) {
				return
			}
		}
	case map[string]any:
		if circularRef(context, value) {
			addEstimatedBytes(context, 16)
			return
		}
		addEstimatedBytes(context, 2)
		for _, key := range sortedMapKeys(typed) {
			addEstimatedBytes(context, estimateStringBytes(key, context)+3)
			visitJSONLikeValue(typed[key], context)
			addEstimatedBytes(context, 1)
			if estimateLimitReached(context) {
				return
			}
		}
	default:
		if handled := visitStructLikeValue(value, context); handled {
			return
		}
		addEstimatedBytes(context, 16)
	}
}

// visitStructLikeValue walks exported struct fields with their JSON names so
// typed records (UsageRecordInput) estimate exactly like the Node plain
// objects they mirror. Returns false for non-struct values.
func visitStructLikeValue(value any, context *jsonLikeEstimateContext) bool {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return false
	}
	if circularRef(context, value) {
		addEstimatedBytes(context, 16)
		return true
	}
	addEstimatedBytes(context, 2)
	rt := rv.Type()
	for index := 0; index < rt.NumField(); index++ {
		field := rt.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name, keep := jsonFieldName(field)
		if !keep {
			continue
		}
		addEstimatedBytes(context, estimateStringBytes(name, context)+3)
		visitJSONLikeValue(exportFieldValue(rv.Field(index)), context)
		addEstimatedBytes(context, 1)
		if estimateLimitReached(context) {
			return true
		}
	}
	return true
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name := tag
	if comma := strings.Index(tag, ","); comma >= 0 {
		name = tag[:comma]
	}
	if name == "" {
		name = field.Name
	}
	return name, true
}

func exportFieldValue(field reflect.Value) any {
	if !field.IsValid() {
		return nil
	}
	if field.Kind() == reflect.Ptr {
		if field.IsNil() {
			return nil
		}
		return field.Elem().Interface()
	}
	if field.Kind() == reflect.Interface {
		if field.IsNil() {
			return nil
		}
		return field.Interface()
	}
	return field.Interface()
}

func circularRef(context *jsonLikeEstimateContext, value any) bool {
	return context.seen.add(value)
}

// identitySet tracks container identity by data pointer (the WeakSet
// adaptation; interface keys would panic on slice/map values).
type identitySet struct {
	items []identityItem
}

type identityItem struct {
	kind  reflect.Kind
	pointer uintptr
}

func newIdentitySet() *identitySet { return &identitySet{} }

func (s *identitySet) add(value any) bool {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map:
		pointer := rv.Pointer()
		for _, item := range s.items {
			if item.kind == rv.Kind() && item.pointer == pointer {
				return true
			}
		}
		s.items = append(s.items, identityItem{kind: rv.Kind(), pointer: pointer})
	}
	return false
}

func addEstimatedBytes(context *jsonLikeEstimateContext, bytes int) {
	if context.total >= context.maxBytes {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	context.total += bytes
	if context.total > context.maxBytes {
		context.total = context.maxBytes
	}
}

func estimateLimitReached(context *jsonLikeEstimateContext) bool {
	return context.total >= context.maxBytes || context.nodeCount >= context.maxNodes
}

func estimateStringBytes(value string, context *jsonLikeEstimateContext) int {
	if context.maxBytes <= 0 || utf8.RuneCountInString(value) <= exactStringByteLengthMaxChars {
		return len(value)
	}
	return len(value) * 4
}

func normalizeEstimateLimit(value int) int {
	if value > 0 {
		return value
	}
	return int(^uint(0) >> 1) // max int, mirroring Number.POSITIVE_INFINITY
}

// sliceStringByUTF8Bytes mirrors sliceStringByUtf8Bytes: cut at rune
// granularity so no character exceeds maxBytes.
func sliceStringByUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	bytes := 0
	index := 0
	for index < len(value) {
		charBytes := runeUTF8ByteLength(value[index])
		if charBytes > len(value)-index {
			charBytes = len(value) - index
		}
		if bytes+charBytes > maxBytes {
			break
		}
		bytes += charBytes
		index += charBytes
	}
	return value[:index]
}

// runeUTF8ByteLength mirrors codePointUtf8ByteLength from the leading byte.
func runeUTF8ByteLength(leading byte) int {
	switch {
	case leading < 0x80:
		return 1
	case leading < 0xE0:
		return 2
	case leading < 0xF0:
		return 3
	default:
		return 4
	}
}

// boundedStringByteLength mirrors boundedStringByteLength: exact UTF-8
// length for reasonably sized strings, len*4 upper bound beyond that.
func boundedStringByteLength(value string, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	var length int
	if utf8.RuneCountInString(value) <= exactSnapshotStringByteLengthMaxChars {
		length = len(value)
	} else {
		length = len(value) * 4
	}
	if length > maxBytes {
		return maxBytes
	}
	return length
}

const exactSnapshotStringByteLengthMaxChars = 16 * 1024

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
