// Byte estimation ported from shared/queue-size.ts estimateJsonLikeBytes: the
// bounded-capacity accounting the enqueue path uses to decide queue overflow.
// Values are estimated, not serialized: strings count their UTF-8 length
// (long strings approximate at 4 bytes per character), objects/arrays count
// structural bytes, and the walk stops at the node ceiling.
package publicapilogs

import (
	"encoding/json"
	"strconv"
)

const (
	estimateExactStringByteLengthMaxChars = 16 * 1024
)

type jsonLikeByteEstimateContext struct {
	seen      map[*snapshotObject]bool
	total     int
	nodeCount int
	maxBytes  int
	maxNodes  int
}

// estimateInputBytes mirrors estimatePublicApiLogBytes: the walk visits the
// Input fields in the Node object-literal order (id last, exactly like
// ensurePublicApiLogQueueId's spread), so queue accounting behaves like the
// Node local queue at the same limits. Map-shaped payloads sort their keys for
// determinism; Node iterates them in insertion order, which only shifts
// borderline drop decisions by a few bytes.
func estimateInputBytes(input Input, maxBytes, maxNodes int) int {
	context := &jsonLikeByteEstimateContext{
		seen:     map[*snapshotObject]bool{},
		maxBytes: maxBytes,
		maxNodes: maxNodes,
	}
	if maxBytes <= 0 {
		context.maxBytes = int(^uint(0) >> 1)
	}
	if maxNodes <= 0 {
		context.maxNodes = int(^uint(0) >> 1)
	}
	// Node key order of the queued input object (id appended last).
	visitJSONLikeValue(input.TraceID, context)
	visitJSONLikeValue(input.SourceRefID, context)
	visitJSONLikeValue(input.SourceName, context)
	visitJSONLikeValue(input.TokenID, context)
	visitJSONLikeValue(input.TokenName, context)
	visitJSONLikeValue(input.TokenPrefix, context)
	visitJSONLikeValue(input.IsTestToken, context)
	visitJSONLikeValue(input.Method, context)
	visitJSONLikeValue(input.Path, context)
	visitJSONLikeValue(input.QueryString, context)
	visitJSONLikeValue(input.ClientIP, context)
	visitJSONLikeValue(input.UserAgent, context)
	visitJSONLikeValue(input.StatusCode, context)
	visitJSONLikeValue(input.Success, context)
	visitJSONLikeValue(input.DurationMS, context)
	visitJSONLikeValue(input.RequestSizeBytes, context)
	visitJSONLikeValue(input.ResponseSizeBytes, context)
	visitJSONLikeValue(string(input.RequestCaptureStatus), context)
	visitJSONLikeValue(string(input.ResponseCaptureStatus), context)
	visitJSONLikeValue(input.RequestData, context)
	visitJSONLikeValue(input.ResponseData, context)
	visitJSONLikeValue(input.ErrorCode, context)
	visitJSONLikeValue(input.ErrorMessage, context)
	visitJSONLikeValue(input.StartedAt, context)
	visitJSONLikeValue(input.EndedAt, context)
	visitJSONLikeValue(input.CreatedAt, context)
	visitJSONLikeValue(input.ID, context)
	return context.total
}

func estimateLimitReached(context *jsonLikeByteEstimateContext) bool {
	return context.total >= context.maxBytes || context.nodeCount >= context.maxNodes
}

func visitJSONLikeValue(value any, context *jsonLikeByteEstimateContext) {
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
	case undefinedValue:
		addEstimatedBytes(context, 4)
	case bool:
		addEstimatedBytes(context, boolTextLen(typed))
	case string:
		addEstimatedBytes(context, estimateStringBytes(typed, context)+2)
	case int:
		addEstimatedBytes(context, len(strconv.Itoa(typed)))
	case int64:
		addEstimatedBytes(context, len(strconv.FormatInt(typed, 10)))
	case float64:
		addEstimatedBytes(context, len(strconv.FormatFloat(typed, 'g', -1, 64)))
	case json.Number:
		addEstimatedBytes(context, len(typed.String()))
	case []byte:
		addEstimatedBytes(context, len(typed))
	case *snapshotObject:
		if typed == nil {
			addEstimatedBytes(context, 4)
			return
		}
		if context.seen[typed] {
			addEstimatedBytes(context, 16)
			return
		}
		context.seen[typed] = true
		addEstimatedBytes(context, 2)
		for _, key := range typed.keys {
			addEstimatedBytes(context, estimateStringBytes(key, context)+3)
			visitJSONLikeValue(typed.vals[key], context)
			addEstimatedBytes(context, 1)
			if estimateLimitReached(context) {
				return
			}
		}
	case map[string]any:
		if typed == nil {
			addEstimatedBytes(context, 4)
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
	case []any:
		if typed == nil {
			addEstimatedBytes(context, 4)
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
	default:
		addEstimatedBytes(context, 16)
	}
}

func addEstimatedBytes(context *jsonLikeByteEstimateContext, bytes int) {
	if bytes < 0 {
		bytes = 0
	}
	if context.total >= context.maxBytes {
		return
	}
	context.total += bytes
	if context.total > context.maxBytes {
		context.total = context.maxBytes
	}
}

func estimateStringBytes(value string, context *jsonLikeByteEstimateContext) int {
	runes := utf16Length(value)
	if runes <= estimateExactStringByteLengthMaxChars {
		return len(value)
	}
	return runes * 4
}

// utf16Length approximates JS string .length (UTF-16 code units).
func utf16Length(value string) int {
	count := 0
	for _, runeValue := range value {
		count++
		if runeValue > 0xFFFF {
			count++
		}
	}
	return count
}

func boolTextLen(value bool) int {
	if value {
		return 4
	}
	return 5
}
