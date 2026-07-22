package httpapi

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	publicAPIQueryBracketPattern        = regexp.MustCompile(`\[[^\[\]]*\]`)
	publicAPIQueryEncodedBracketPattern = regexp.MustCompile(`(?i)%5[bd]`)
)

const (
	publicAPIQueryParameterLimit = 1000
	publicAPIQueryDepthLimit     = 5
	publicAPIQueryArrayLimit     = 20
)

type publicAPIQuerySegment struct {
	key      string
	index    int
	array    bool
	append   bool
	overflow bool
}

type publicAPIQueryOverflowValues map[string]any

func parsePublicAPIQuery(rawQuery string) map[string]any {
	result := any(map[string]any{})
	if rawQuery == "" {
		return result.(map[string]any)
	}
	query := publicAPIQueryNormalizeBrackets(rawQuery)
	valuesByKey := make(map[string]any)
	orderedKeys := make([]string, 0)

	for start, count := 0, 0; start <= len(query) && count < publicAPIQueryParameterLimit; count++ {
		end := strings.IndexByte(query[start:], '&')
		if end < 0 {
			end = len(query)
		} else {
			end += start
		}
		part := query[start:end]
		rawKey, rawValue := publicAPIQuerySplitPart(part)
		key := publicAPIQueryUnescapeKey(rawKey)
		if key != "" {
			value := publicAPIQueryUnescape(rawValue)
			if existing, exists := valuesByKey[key]; exists {
				valuesByKey[key] = publicAPIQueryCombineValues(existing, value)
			} else {
				orderedKeys = append(orderedKeys, key)
				valuesByKey[key] = value
			}
		}
		if end == len(query) {
			break
		}
		start = end + 1
	}
	for _, key := range publicAPIQueryObjectKeys(orderedKeys) {
		segments := publicAPIQuerySegments(key)
		if len(segments) > 0 {
			result = publicAPIQueryMerge(result, publicAPIQueryBuildValue(segments, valuesByKey[key]))
		}
	}
	compacted := publicAPIQueryCompact(result)
	switch typed := compacted.(type) {
	case map[string]any:
		return typed
	case publicAPIQueryOverflowValues:
		return map[string]any(typed)
	default:
		return map[string]any{}
	}
}

func publicAPIQuerySplitPart(part string) (string, string) {
	separator := strings.Index(part, "]=")
	if separator >= 0 {
		separator++
	} else {
		separator = strings.IndexByte(part, '=')
	}
	if separator < 0 {
		return part, ""
	}
	return part[:separator], part[separator+1:]
}

func publicAPIQueryObjectKeys(insertionOrder []string) []string {
	type integerKey struct {
		key   string
		value uint64
	}
	integers := make([]integerKey, 0)
	stringsInOrder := make([]string, 0, len(insertionOrder))
	for _, key := range insertionOrder {
		value, err := strconv.ParseUint(key, 10, 32)
		if err == nil && value < uint64(^uint32(0)) && strconv.FormatUint(value, 10) == key {
			integers = append(integers, integerKey{key: key, value: value})
			continue
		}
		stringsInOrder = append(stringsInOrder, key)
	}
	sort.Slice(integers, func(left int, right int) bool {
		return integers[left].value < integers[right].value
	})
	ordered := make([]string, 0, len(insertionOrder))
	for _, item := range integers {
		ordered = append(ordered, item.key)
	}
	return append(ordered, stringsInOrder...)
}

func publicAPIQueryCombineValues(existing any, value string) any {
	switch typed := existing.(type) {
	case []any:
		if len(typed) >= publicAPIQueryArrayLimit {
			overflow := publicAPIQueryOverflowValues(publicAPIQueryArrayToObject(typed))
			overflow[strconv.Itoa(len(typed))] = value
			return overflow
		}
		return append(typed, value)
	case publicAPIQueryOverflowValues:
		typed[strconv.Itoa(publicAPIQueryNextObjectIndex(map[string]any(typed)))] = value
		return typed
	default:
		return []any{existing, value}
	}
}

// publicAPIQueryBuildValue mirrors qs's bracket tree construction while bounding
// numeric array indexes before any allocation can occur.
func publicAPIQueryBuildValue(segments []publicAPIQuerySegment, value any) any {
	current := any(value)
	for index := len(segments) - 1; index >= 0; index-- {
		segment := segments[index]
		if segment.key == "__proto__" {
			current = map[string]any{}
			continue
		}
		if segment.overflow {
			current = publicAPIQueryOverflowValues{segment.key: current}
			continue
		}
		if !segment.array {
			current = map[string]any{segment.key: current}
			continue
		}
		if segment.append {
			switch typed := current.(type) {
			case []any:
				current = typed
			case publicAPIQueryOverflowValues:
				current = typed
			default:
				current = []any{current}
			}
			continue
		}
		values := make([]any, segment.index+1)
		values[segment.index] = current
		current = values
	}
	return current
}

func publicAPIQueryMerge(target any, source any) any {
	if source == nil {
		return target
	}
	if target == nil {
		return source
	}
	if typedSource, ok := source.(publicAPIQueryOverflowValues); ok {
		return publicAPIQueryMergeOverflowSource(target, typedSource)
	}
	if typedTarget, ok := target.(publicAPIQueryOverflowValues); ok {
		return publicAPIQueryMergeIntoOverflow(typedTarget, source)
	}
	if !publicAPIQueryIsContainer(source) {
		switch typed := target.(type) {
		case []any:
			return publicAPIQueryAppend(typed, source)
		case map[string]any:
			typed[publicAPIQueryScalarKey(source)] = true
			return typed
		default:
			return publicAPIQueryAppend([]any{target}, source)
		}
	}
	if !publicAPIQueryIsContainer(target) {
		switch typed := source.(type) {
		case []any:
			return publicAPIQueryAppendAll([]any{target}, typed)
		default:
			return []any{target, source}
		}
	}

	switch typedTarget := target.(type) {
	case []any:
		switch typedSource := source.(type) {
		case []any:
			return publicAPIQueryMergeArrays(typedTarget, typedSource)
		case map[string]any:
			return publicAPIQueryMergeObject(publicAPIQueryArrayToObject(typedTarget), typedSource)
		}
	case map[string]any:
		return publicAPIQueryMergeObject(typedTarget, source)
	}

	return source
}

func publicAPIQueryIsContainer(value any) bool {
	switch value.(type) {
	case []any, map[string]any, publicAPIQueryOverflowValues:
		return true
	default:
		return false
	}
}

func publicAPIQueryScalarKey(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func publicAPIQueryAppend(values []any, value any) any {
	if len(values) >= publicAPIQueryArrayLimit {
		object := publicAPIQueryOverflowValues(publicAPIQueryArrayToObject(values))
		object[strconv.Itoa(len(values))] = value
		return object
	}
	return append(values, value)
}

func publicAPIQueryAppendAll(values []any, more []any) any {
	current := any(values)
	for _, value := range more {
		if value == nil {
			continue
		}
		switch typed := current.(type) {
		case []any:
			current = publicAPIQueryAppend(typed, value)
		case map[string]any:
			typed[strconv.Itoa(publicAPIQueryNextObjectIndex(typed))] = value
		case publicAPIQueryOverflowValues:
			typed[strconv.Itoa(publicAPIQueryNextObjectIndex(map[string]any(typed)))] = value
		}
	}
	return current
}

func publicAPIQueryMergeArrays(target []any, source []any) any {
	current := any(target)
	for index, sourceValue := range source {
		if sourceValue == nil {
			continue
		}
		switch typed := current.(type) {
		case []any:
			if index >= len(typed) || typed[index] == nil {
				if index >= publicAPIQueryArrayLimit {
					current = publicAPIQueryMergeObject(publicAPIQueryArrayToObject(typed), map[string]any{strconv.Itoa(index): sourceValue})
					continue
				}
				if index >= len(typed) {
					typed = append(typed, make([]any, index-len(typed)+1)...)
				}
				typed[index] = sourceValue
				current = typed
				continue
			}
			if publicAPIQueryIsContainer(typed[index]) && publicAPIQueryIsContainer(sourceValue) {
				typed[index] = publicAPIQueryMerge(typed[index], sourceValue)
				continue
			}
			current = publicAPIQueryAppend(typed, sourceValue)
		case map[string]any:
			key := strconv.Itoa(index)
			if existing, exists := typed[key]; exists {
				typed[key] = publicAPIQueryMerge(existing, sourceValue)
			} else {
				typed[key] = sourceValue
			}
		case publicAPIQueryOverflowValues:
			current = publicAPIQueryMergeIntoOverflow(typed, map[string]any{strconv.Itoa(index): sourceValue})
		}
	}
	return current
}

func publicAPIQueryMergeOverflowSource(target any, source publicAPIQueryOverflowValues) any {
	switch typed := target.(type) {
	case publicAPIQueryOverflowValues:
		return publicAPIQueryMergeIntoOverflow(typed, source)
	case map[string]any:
		return publicAPIQueryMergeIntoOverflow(publicAPIQueryOverflowValues(typed), source)
	case []any:
		return publicAPIQueryMergeIntoOverflow(publicAPIQueryOverflowValues(publicAPIQueryArrayToObject(typed)), source)
	default:
		shifted := publicAPIQueryOverflowValues{"0": target}
		for _, key := range publicAPIQueryMapKeys(map[string]any(source)) {
			value := source[key]
			if index, ok := publicAPIQueryIntegerKey(key); ok {
				shifted[strconv.FormatUint(index+1, 10)] = value
			} else {
				shifted[key] = value
			}
		}
		return shifted
	}
}

func publicAPIQueryMergeIntoOverflow(target publicAPIQueryOverflowValues, source any) publicAPIQueryOverflowValues {
	if !publicAPIQueryIsContainer(source) {
		target[strconv.Itoa(publicAPIQueryNextObjectIndex(map[string]any(target)))] = source
		return target
	}
	mergeKey := func(key string, value any) {
		if existing, exists := target[key]; exists {
			target[key] = publicAPIQueryMerge(existing, value)
		} else {
			target[key] = value
		}
	}
	switch typed := source.(type) {
	case map[string]any:
		for _, key := range publicAPIQueryMapKeys(typed) {
			mergeKey(key, typed[key])
		}
	case publicAPIQueryOverflowValues:
		values := map[string]any(typed)
		for _, key := range publicAPIQueryMapKeys(values) {
			mergeKey(key, values[key])
		}
	case []any:
		for index, value := range typed {
			if value != nil {
				mergeKey(strconv.Itoa(index), value)
			}
		}
	}
	return target
}

func publicAPIQueryMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return publicAPIQueryObjectKeys(keys)
}

func publicAPIQueryIntegerKey(key string) (uint64, bool) {
	value, err := strconv.ParseUint(key, 10, 32)
	return value, err == nil && value < uint64(^uint32(0)) && strconv.FormatUint(value, 10) == key
}

func publicAPIQueryMergeObject(target map[string]any, source any) map[string]any {
	switch typed := source.(type) {
	case map[string]any:
		for key, value := range typed {
			if existing, exists := target[key]; exists {
				target[key] = publicAPIQueryMerge(existing, value)
			} else {
				target[key] = value
			}
		}
	case []any:
		for index, value := range typed {
			if value == nil {
				continue
			}
			key := strconv.Itoa(index)
			if existing, exists := target[key]; exists {
				target[key] = publicAPIQueryMerge(existing, value)
			} else {
				target[key] = value
			}
		}
	}
	return target
}

func publicAPIQueryUnescape(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return strings.ReplaceAll(value, "+", " ")
}

func publicAPIQueryUnescapeKey(value string) string {
	return publicAPIQueryUnescape(publicAPIQueryNormalizeBrackets(value))
}

func publicAPIQueryNormalizeBrackets(value string) string {
	return publicAPIQueryEncodedBracketPattern.ReplaceAllStringFunc(value, func(encoded string) string {
		if encoded[len(encoded)-1] == 'B' || encoded[len(encoded)-1] == 'b' {
			return "["
		}
		return "]"
	})
}

func publicAPIQuerySegments(key string) []publicAPIQuerySegment {
	firstBracket := publicAPIQueryBracketPattern.FindStringIndex(key)
	if firstBracket == nil {
		return []publicAPIQuerySegment{{key: key}}
	}

	segments := make([]publicAPIQuerySegment, 0, publicAPIQueryDepthLimit+1)
	if parent := key[:firstBracket[0]]; parent != "" {
		segments = append(segments, publicAPIQuerySegment{key: parent})
	}

	nextStart := firstBracket[0]
	for depth := 0; depth < publicAPIQueryDepthLimit; depth++ {
		bracket := publicAPIQueryBracketPattern.FindStringIndex(key[nextStart:])
		if bracket == nil {
			return segments
		}
		bracket[0] += nextStart
		bracket[1] += nextStart
		segments = append(segments, publicAPIQueryBracketSegment(key[bracket[0]+1:bracket[1]-1]))
		nextStart = bracket[1]
	}
	if overflow := publicAPIQueryBracketPattern.FindStringIndex(key[nextStart:]); overflow != nil {
		segments = append(segments, publicAPIQuerySegment{key: key[nextStart+overflow[0]:]})
	}
	return segments
}

func publicAPIQueryBracketSegment(value string) publicAPIQuerySegment {
	if value == "" {
		return publicAPIQuerySegment{array: true, append: true}
	}
	index, err := strconv.Atoi(value)
	if err == nil && index >= 0 && strconv.Itoa(index) == value {
		if index < publicAPIQueryArrayLimit {
			return publicAPIQuerySegment{array: true, index: index}
		}
		return publicAPIQuerySegment{key: value, index: index, overflow: true}
	}
	return publicAPIQuerySegment{key: value}
}

func publicAPIQueryArrayToObject(values []any) map[string]any {
	out := make(map[string]any, len(values))
	for index, value := range values {
		if value != nil {
			out[strconv.Itoa(index)] = value
		}
	}
	return out
}

func publicAPIQueryNextObjectIndex(values map[string]any) int {
	next := 0
	for key := range values {
		index, err := strconv.Atoi(key)
		if err == nil && index >= 0 && strconv.Itoa(index) == key && index >= next {
			next = index + 1
		}
	}
	return next
}

func publicAPIQueryCompact(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				out = append(out, publicAPIQueryCompact(item))
			}
		}
		return out
	case map[string]any:
		for key, item := range typed {
			typed[key] = publicAPIQueryCompact(item)
		}
		return typed
	case publicAPIQueryOverflowValues:
		out := map[string]any(typed)
		for key, item := range out {
			out[key] = publicAPIQueryCompact(item)
		}
		return out
	default:
		return value
	}
}
