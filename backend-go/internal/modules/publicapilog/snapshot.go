package publicapilog

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	SnapshotMaxBytes           = 32 * 1024
	SnapshotMaxDepth           = 8
	SnapshotMaxEntries         = 200
	SnapshotStringPreviewBytes = 4096
)

type Snapshot struct {
	Data      map[string]any
	Status    port.PublicAPILogCaptureStatus
	SizeBytes int64
}

type RequestSnapshotInput struct {
	Method             string
	Path               string
	Query              map[string]any
	Body               any
	ContentType        string
	ContentLength      string
	BodySizeBytes      *int64
	QueryString        string
	BodyRejectedReason string
}

type ResponseSnapshotInput struct {
	StatusCode    int
	Body          any
	BodySizeBytes *int64
}

type snapshotBudget struct {
	remainingBytes int
	truncated      bool
}

func BuildRequestSnapshot(input RequestSnapshotInput) Snapshot {
	body := input.Body
	bodyRejected := strings.TrimSpace(input.BodyRejectedReason)
	if bodyRejected != "" {
		body = map[string]any{
			"dropped": true,
			"reason":  bodyRejected,
		}
	}

	data := map[string]any{
		"method": strings.ToUpper(input.Method),
		"path":   input.Path,
		"query":  input.Query,
		"body":   body,
		"headers": map[string]any{
			"contentType":   emptyStringAsNil(input.ContentType),
			"contentLength": emptyStringAsNil(input.ContentLength),
		},
	}

	bodySize := EstimatePayloadSizeBytes(input.Body)
	if input.BodySizeBytes != nil {
		bodySize = max(0, *input.BodySizeBytes)
	}
	querySize := int64(len([]byte(input.QueryString)))
	snapshot := BoundedSnapshot(data, bodySize+querySize)
	if bodyRejected != "" {
		snapshot.Status = port.PublicAPILogCaptureDropped
	}
	return snapshot
}

func BuildResponseSnapshot(input ResponseSnapshotInput) Snapshot {
	bodySize := EstimatePayloadSizeBytes(input.Body)
	if input.BodySizeBytes != nil {
		bodySize = max(0, *input.BodySizeBytes)
	}
	return BoundedSnapshot(map[string]any{
		"statusCode": input.StatusCode,
		"body":       input.Body,
	}, bodySize)
}

func SanitizeQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	for index, part := range parts {
		if part == "" {
			continue
		}
		key, value, hasValue := strings.Cut(part, "=")
		decodedKey := queryUnescapeOrRaw(key)
		if sensitiveSnapshotKey(decodedKey) {
			if hasValue {
				parts[index] = key + "=[redacted]"
			} else {
				parts[index] = "[redacted]"
			}
			continue
		}
		if !hasValue {
			if sensitiveSnapshotString(queryUnescapeOrRaw(key)) {
				parts[index] = "[redacted]"
			}
			continue
		}
		decodedValue := queryUnescapeOrRaw(value)
		if sensitiveSnapshotString(decodedValue) {
			parts[index] = key + "=[redacted]"
		}
	}
	return strings.Join(parts, "&")
}

func BoundedSnapshot(data map[string]any, sizeBytes int64) Snapshot {
	sanitizedSizeBytes := max(0, sizeBytes)
	if isSnapshotEmpty(data) {
		return Snapshot{
			Data:      data,
			Status:    port.PublicAPILogCaptureEmpty,
			SizeBytes: sanitizedSizeBytes,
		}
	}

	budget := &snapshotBudget{
		remainingBytes: SnapshotMaxBytes,
	}
	boundedValue := cloneSnapshotValue(data, budget, 0)
	boundedData, ok := boundedValue.(map[string]any)
	if !ok {
		boundedData = map[string]any{
			"value": boundedValue,
		}
	}

	jsonBytes, err := json.Marshal(boundedData)
	jsonSizeBytes := int64(len(jsonBytes))
	if err == nil && !budget.truncated && jsonSizeBytes <= SnapshotMaxBytes {
		if sanitizedSizeBytes == 0 {
			sanitizedSizeBytes = jsonSizeBytes
		}
		return Snapshot{
			Data:      boundedData,
			Status:    port.PublicAPILogCaptureComplete,
			SizeBytes: sanitizedSizeBytes,
		}
	}

	originalSize := sanitizedSizeBytes
	if originalSize == 0 {
		originalSize = max(jsonSizeBytes, SnapshotMaxBytes+1)
	}
	return Snapshot{
		Data: map[string]any{
			"truncated":             true,
			"originalJsonSizeBytes": originalSize,
			"preview":               sliceUTF8(string(jsonBytes), SnapshotMaxBytes),
		},
		Status:    port.PublicAPILogCaptureTruncated,
		SizeBytes: originalSize,
	}
}

func EstimatePayloadSizeBytes(value any) int64 {
	if value == nil {
		return 0
	}
	switch typed := value.(type) {
	case []byte:
		return int64(len(typed))
	case string:
		return int64(len([]byte(typed)))
	}

	budget := &snapshotBudget{
		remainingBytes: SnapshotMaxBytes + 1,
	}
	bounded := cloneSnapshotValue(value, budget, 0)
	data, err := json.Marshal(bounded)
	if err != nil {
		return 0
	}
	size := int64(len(data))
	if budget.truncated {
		return max(size, SnapshotMaxBytes+1)
	}
	return size
}

func cloneSnapshotValue(value any, budget *snapshotBudget, depth int) any {
	if budget.remainingBytes <= 0 {
		return truncatedSnapshotMarker(budget)
	}
	if value == nil {
		chargeSnapshotBytes(budget, 4)
		return nil
	}

	switch typed := value.(type) {
	case string:
		return cloneSnapshotString(typed, budget)
	case []byte:
		return cloneSnapshotBytes(typed, budget)
	case time.Time:
		return cloneSnapshotString(typed.UTC().Format(time.RFC3339Nano), budget)
	case json.Number:
		chargeSnapshotBytes(budget, len(typed.String()))
		return typed.String()
	case int:
		return cloneSnapshotNumber(typed, budget)
	case int8:
		return cloneSnapshotNumber(typed, budget)
	case int16:
		return cloneSnapshotNumber(typed, budget)
	case int32:
		return cloneSnapshotNumber(typed, budget)
	case int64:
		return cloneSnapshotNumber(typed, budget)
	case uint:
		return cloneSnapshotNumber(typed, budget)
	case uint8:
		return cloneSnapshotNumber(typed, budget)
	case uint16:
		return cloneSnapshotNumber(typed, budget)
	case uint32:
		return cloneSnapshotNumber(typed, budget)
	case uint64:
		return cloneSnapshotNumber(typed, budget)
	case float32:
		return cloneSnapshotNumber(float64(typed), budget)
	case float64:
		return cloneSnapshotNumber(typed, budget)
	case bool:
		if typed {
			chargeSnapshotBytes(budget, 4)
		} else {
			chargeSnapshotBytes(budget, 5)
		}
		return typed
	case map[string]any:
		if depth >= SnapshotMaxDepth {
			return truncatedSnapshotMarker(budget)
		}
		return cloneSnapshotMap(typed, budget, depth)
	case map[string]string:
		if depth >= SnapshotMaxDepth {
			return truncatedSnapshotMarker(budget)
		}
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			converted[key] = value
		}
		return cloneSnapshotMap(converted, budget, depth)
	case []any:
		if depth >= SnapshotMaxDepth {
			return truncatedSnapshotMarker(budget)
		}
		return cloneSnapshotArray(typed, budget, depth)
	case []string:
		if depth >= SnapshotMaxDepth {
			return truncatedSnapshotMarker(budget)
		}
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return cloneSnapshotArray(items, budget, depth)
	default:
		return cloneSnapshotString(fmt.Sprint(value), budget)
	}
}

func cloneSnapshotMap(value map[string]any, budget *snapshotBudget, depth int) map[string]any {
	chargeSnapshotBytes(budget, 2)
	out := make(map[string]any, min(len(value), SnapshotMaxEntries))
	count := 0
	for key, item := range value {
		if count >= SnapshotMaxEntries || budget.remainingBytes <= 0 {
			out["__truncated"] = true
			budget.truncated = true
			break
		}
		count++
		chargeSnapshotBytes(budget, len([]byte(key))+4)
		if sensitiveSnapshotKey(key) && !snapshotKeyMayContainStructuredPublicData(key, item) {
			out[key] = cloneSnapshotString("[redacted]", budget)
			continue
		}
		out[key] = cloneSnapshotValue(item, budget, depth+1)
	}
	return out
}

func cloneSnapshotArray(value []any, budget *snapshotBudget, depth int) []any {
	chargeSnapshotBytes(budget, 2)
	limit := min(len(value), SnapshotMaxEntries)
	out := make([]any, 0, limit+1)
	for index := 0; index < limit; index++ {
		if budget.remainingBytes <= 0 {
			break
		}
		out = append(out, cloneSnapshotValue(value[index], budget, depth+1))
	}
	if len(value) > limit || budget.remainingBytes <= 0 {
		out = append(out, truncatedSnapshotMarker(budget))
	}
	return out
}

func cloneSnapshotNumber(value any, budget *snapshotBudget) any {
	if floatValue, ok := snapshotFloat(value); ok && (math.IsNaN(floatValue) || math.IsInf(floatValue, 0)) {
		return cloneSnapshotString(fmt.Sprint(value), budget)
	}
	text := fmt.Sprint(value)
	chargeSnapshotBytes(budget, len([]byte(text)))
	return value
}

func snapshotFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func cloneSnapshotBytes(value []byte, budget *snapshotBudget) map[string]any {
	previewBytes := min(len(value), SnapshotStringPreviewBytes, max(0, budget.remainingBytes))
	chargeSnapshotBytes(budget, previewBytes+64)
	if len(value) > previewBytes {
		budget.truncated = true
	}
	return map[string]any{
		"type":       "Buffer",
		"byteLength": len(value),
		"preview":    sliceUTF8(string(value[:previewBytes]), previewBytes),
		"truncated":  len(value) > previewBytes,
	}
}

func cloneSnapshotString(value string, budget *snapshotBudget) string {
	if sensitiveSnapshotString(value) {
		return cloneSnapshotString("[redacted]", budget)
	}
	size := len([]byte(value))
	if size <= budget.remainingBytes {
		chargeSnapshotBytes(budget, size)
		return value
	}

	budget.truncated = true
	previewBytes := min(max(0, budget.remainingBytes), SnapshotStringPreviewBytes)
	budget.remainingBytes = 0
	return sliceUTF8(value, previewBytes) + "...[truncated]"
}

func truncatedSnapshotMarker(budget *snapshotBudget) string {
	budget.truncated = true
	return "[truncated]"
}

func chargeSnapshotBytes(budget *snapshotBudget, bytes int) {
	budget.remainingBytes -= max(0, bytes)
	if budget.remainingBytes < 0 {
		budget.truncated = true
		budget.remainingBytes = 0
	}
}

func isSnapshotEmpty(data map[string]any) bool {
	body, hasBody := data["body"]
	query, hasQuery := data["query"]
	if hasBody && body != nil && !isEmptyObject(body) {
		return false
	}
	if hasQuery && !isEmptyObject(query) {
		return false
	}
	return !hasBody || body == nil
}

func isEmptyObject(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return len(typed) == 0
	case map[string]string:
		return len(typed) == 0
	default:
		return false
	}
}

func sliceUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	bytes := []byte(value)
	if len(bytes) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(string(bytes[:maxBytes]), "\uFFFD")
}

func emptyStringAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func queryUnescapeOrRaw(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func sensitiveSnapshotKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "", "_", "", ".", "").Replace(normalized)
	switch normalized {
	case "authorization",
		"cookie",
		"password",
		"passwd",
		"secret",
		"token",
		"apikey",
		"key",
		"hash",
		"keyhash",
		"tokenhash",
		"apikeyhash",
		"proxy",
		"proxyurl",
		"proxyuri",
		"accesstoken",
		"refreshtoken",
		"tokensecretencrypted",
		"keysecretencrypted":
		return true
	default:
		return strings.HasSuffix(normalized, "secret") ||
			strings.HasSuffix(normalized, "token") ||
			strings.HasSuffix(normalized, "apikey") ||
			strings.HasSuffix(normalized, "hash")
	}
}

func snapshotKeyMayContainStructuredPublicData(key string, value any) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "", "_", "", ".", "").Replace(normalized)
	if normalized != "apikey" {
		return false
	}
	_, ok := value.(map[string]any)
	return ok
}

func sensitiveSnapshotString(value string) bool {
	text := strings.TrimSpace(value)
	if len(text) < 12 {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(text, "juis_") {
		return true
	}
	if strings.HasPrefix(text, "sk-") && len(text) >= 32 {
		return true
	}
	if strings.Contains(text, "://") {
		if parsed, err := url.Parse(text); err == nil && parsed.User != nil {
			return true
		}
	}
	return secretHashSnapshotPattern.MatchString(text) || secretLikeSnapshotPattern.MatchString(text)
}

var secretLikeSnapshotPattern = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/=-]{12,}|(?:api[_-]?key|token|secret)=([^&\s]{8,}))`)

var secretHashSnapshotPattern = regexp.MustCompile(`(?i)^(?:sha256:)?[a-f0-9]{64}$`)
