// Capture ported from public-api-log-capture.middleware.ts: builds the exact
// PublicApiLogInput the Node middleware assembles on request finish / client
// close, including the 32 KiB bounded request/response snapshots, the OAuth
// URL redaction, the 499 client-closed mapping and the body-rejection
// (parse-failed / too-large) markers. The HTTP lifecycle wiring (which event
// records the log) belongs to the gateway pipeline; this package exposes the
// exactly-once Capture recorder so double signalling stays impossible.
package publicapilogs

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Snapshot budget mirrors the publicApiSnapshot* constants.
const (
	publicAPISnapshotMaxBytes           = 32 * 1024
	publicAPISnapshotMaxDepth           = 8
	publicAPISnapshotMaxEntries         = 200
	publicAPISnapshotStringPreviewBytes = 4096
	publicAPIErrorInfoMaxRunes          = 1000
	publicAPIErrorServerInternalMessage = "服务器内部错误"
)

// BodyRejection mirrors res.locals.publicApiRequestBodyRejected.
type BodyRejection struct {
	StatusCode int
	ErrorType  string
}

// CaptureSpec carries everything the Node middleware reads from
// req/res/res.locals at record time. Nil/Undefined semantics follow Node:
// Query nil means req.query was absent, Body nil means req.body was
// undefined (the body parser failed or never ran).
type CaptureSpec struct {
	Method        string
	BaseURL       string // express req.baseUrl
	Path          string // express req.path
	OriginalURL   string // express req.originalUrl
	Query         any
	Body          any
	ContentType   string
	ContentLength string
	UserAgent     string
	StatusCode    int
	Closed        bool
	// ResponsePayload is the first res.json / res.send payload.
	ResponsePayload any
	StartedAt       time.Time
	EndedAt         time.Time
	DurationMS      int64
	TraceID         string
	ClientIP        string
	Source          *SourceContext
	BodyRejected    *BodyRejection
}

// capturedSnapshot mirrors CapturedSnapshot.
type capturedSnapshot struct {
	data      any
	status    CaptureStatus
	sizeBytes int64
}

// BuildInput mirrors buildPublicApiLogInput.
func BuildInput(spec CaptureSpec) Input {
	sanitizedURL := sanitizeURLForLog(spec.OriginalURL)
	path, queryString := splitPathAndQuery(sanitizedURL)
	statusCode := spec.StatusCode
	if spec.Closed {
		statusCode = 499
	}
	requestSnapshot := buildRequestSnapshot(&spec, statusCode)
	responseSnapshot := buildResponseSnapshot(spec.ResponsePayload, statusCode)

	var errorCode, errorMessage string
	if spec.Closed {
		errorCode = "public_api_client_closed"
		errorMessage = "客户端连接提前关闭"
	} else {
		errorCode, errorMessage = extractPublicAPIErrorInfo(spec.ResponsePayload, statusCode)
	}

	return Input{
		TraceID:               spec.TraceID,
		SourceRefID:           sourceField(spec.Source, func(s *SourceContext) string { return s.SourceRefID }),
		SourceName:            sourceField(spec.Source, func(s *SourceContext) string { return s.SourceName }),
		TokenID:               sourceField(spec.Source, func(s *SourceContext) string { return s.TokenID }),
		TokenName:             sourceField(spec.Source, func(s *SourceContext) string { return s.TokenName }),
		TokenPrefix:           sourceField(spec.Source, func(s *SourceContext) string { return s.TokenPrefix }),
		IsTestToken:           spec.Source != nil && spec.Source.IsTestToken,
		Method:                strings.ToUpper(spec.Method),
		Path:                  path,
		QueryString:           queryString,
		ClientIP:              spec.ClientIP,
		UserAgent:             spec.UserAgent,
		StatusCode:            statusCode,
		Success:               !spec.Closed && statusCode >= 200 && statusCode < 400,
		DurationMS:            spec.DurationMS,
		RequestSizeBytes:      requestSnapshot.sizeBytes,
		ResponseSizeBytes:     responseSnapshot.sizeBytes,
		RequestCaptureStatus:  requestSnapshot.status,
		ResponseCaptureStatus: responseSnapshot.status,
		RequestData:           requestSnapshot.data,
		ResponseData:          responseSnapshot.data,
		ErrorCode:             errorCode,
		ErrorMessage:          errorMessage,
		StartedAt:             isoMillis(spec.StartedAt),
		EndedAt:               isoMillis(spec.EndedAt),
		CreatedAt:             isoMillis(spec.EndedAt),
	}
}

// Capture is the per-request exactly-once recorder: it mirrors the Node
// middleware's `recorded` flag so finish + close + abort can never enqueue the
// same request twice.
type Capture struct {
	spec     CaptureSpec
	sink     func(Input) bool
	recorded bool
	// Now overrides the ended-at clock; nil falls back to time.Now.
	Now func() time.Time
}

// NewCapture starts a capture for one request; sink receives the built input
// exactly once (normally Pipeline.Enqueue).
func NewCapture(spec CaptureSpec, sink func(Input) bool) *Capture {
	return &Capture{spec: spec, sink: sink}
}

// RecordFinish mirrors the res 'finish' path.
func (c *Capture) RecordFinish(statusCode int, responsePayload any) bool {
	return c.record(statusCode, responsePayload, false)
}

// RecordClosed mirrors the res close / req aborted / socket close path: the
// client went away before completion.
func (c *Capture) RecordClosed(statusCode int, responsePayload any) bool {
	return c.record(statusCode, responsePayload, true)
}

func (c *Capture) record(statusCode int, responsePayload any, closed bool) bool {
	if c == nil || c.recorded {
		return false
	}
	c.recorded = true
	c.spec.StatusCode = statusCode
	c.spec.ResponsePayload = responsePayload
	c.spec.Closed = closed
	c.spec.EndedAt = c.now()
	c.spec.DurationMS = c.spec.EndedAt.Sub(c.spec.StartedAt).Milliseconds()
	if c.sink == nil {
		return false
	}
	return c.sink(BuildInput(c.spec))
}

// Recorded reports whether the log was already emitted.
func (c *Capture) Recorded() bool { return c.recorded }

// now returns the injected clock or real time.
func (c *Capture) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func sourceField(source *SourceContext, get func(*SourceContext) string) string {
	if source == nil {
		return ""
	}
	return get(source)
}

// splitPathAndQuery mirrors `sanitizedUrl.split('?')` + join('?'): the path is
// everything before the first '?', the query keeps any further '?' characters.
func splitPathAndQuery(sanitizedURL string) (path string, queryString string) {
	index := strings.Index(sanitizedURL, "?")
	if index < 0 {
		return sanitizedURL, ""
	}
	return sanitizedURL[:index], sanitizedURL[index+1:]
}

func buildRequestSnapshot(spec *CaptureSpec, statusCode int) capturedSnapshot {
	rejectedReason := requestBodyRejectedReason(spec, statusCode)
	var body any = Undefined
	if rejectedReason != "" {
		body = newSnapshotObject().set("dropped", true).set("reason", rejectedReason)
	} else {
		body = spec.Body
	}
	// express req.query is always an object ({} without a query string).
	query := spec.Query
	if query == nil {
		query = newSnapshotObject()
	}
	headers := newSnapshotObject().
		set("contentType", textOrUndefined(spec.ContentType)).
		set("contentLength", textOrUndefined(spec.ContentLength))
	data := newSnapshotObject().
		set("method", strings.ToUpper(spec.Method)).
		set("path", spec.BaseURL+spec.Path).
		set("query", query).
		set("body", body).
		set("headers", headers)
	sizeBytes := contentLengthBytes(spec.ContentLength)
	if strings.Contains(spec.OriginalURL, "?") {
		sizeBytes += int64(utf8ByteLen(strings.SplitN(spec.OriginalURL, "?", 2)[1]))
	}
	snapshot := boundedSnapshot(data, sizeBytes)
	if rejectedReason != "" {
		snapshot.status = CaptureStatusDropped
	}
	return snapshot
}

func buildResponseSnapshot(payload any, statusCode int) capturedSnapshot {
	data := newSnapshotObject().
		set("statusCode", statusCode).
		set("body", payload)
	return boundedSnapshot(data, 0)
}

// boundedSnapshot mirrors boundedSnapshot.
func boundedSnapshot(data *snapshotObject, sizeBytes int64) capturedSnapshot {
	sanitizedSizeBytes := sizeBytes
	if sanitizedSizeBytes < 0 {
		sanitizedSizeBytes = 0
	}
	if isSnapshotEmpty(data) {
		return capturedSnapshot{data: data, status: CaptureStatusEmpty, sizeBytes: sanitizedSizeBytes}
	}
	bounded := boundedSnapshotValue(data, publicAPISnapshotMaxBytes)
	jsonText, jsonErr := marshalCompact(bounded.value)
	if jsonErr != nil {
		jsonText = ""
	}
	jsonSizeBytes := int64(len(jsonText))
	if !bounded.truncated && jsonSizeBytes <= publicAPISnapshotMaxBytes {
		return capturedSnapshot{
			data:      bounded.value,
			status:    CaptureStatusComplete,
			sizeBytes: firstNonZero(sanitizedSizeBytes, jsonSizeBytes),
		}
	}
	return capturedSnapshot{
		data: newSnapshotObject().
			set("truncated", true).
			set("originalJsonSizeBytes", firstNonZero(sanitizedSizeBytes, maxInt64(jsonSizeBytes, publicAPISnapshotMaxBytes+1))).
			set("preview", sliceUTF8(jsonText, publicAPISnapshotMaxBytes)),
		status:    CaptureStatusTruncated,
		sizeBytes: firstNonZero(sanitizedSizeBytes, maxInt64(jsonSizeBytes, publicAPISnapshotMaxBytes+1)),
	}
}

// isSnapshotEmpty mirrors isSnapshotEmpty.
func isSnapshotEmpty(data *snapshotObject) bool {
	body := data.get("body")
	query := data.get("query")
	if !isAbsent(body) && !isPlainEmptyObject(body) {
		return false
	}
	if !isAbsent(query) && isPlainObject(query) && hasOwnEnumerableKey(query) {
		return false
	}
	return isAbsent(body)
}

func isAbsent(value any) bool {
	return value == nil || isUndefined(value)
}

func isPlainObject(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return typed != nil
	case *snapshotObject:
		return typed != nil
	}
	return false
}

func isPlainEmptyObject(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return len(typed) == 0
	case *snapshotObject:
		return typed != nil && len(typed.keys) == 0
	}
	return false
}

func hasOwnEnumerableKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return len(typed) > 0
	case *snapshotObject:
		return typed != nil && len(typed.keys) > 0
	}
	return false
}

// extractPublicAPIErrorInfo mirrors extractPublicApiErrorInfo.
func extractPublicAPIErrorInfo(payload any, statusCode int) (errorCode string, errorMessage string) {
	if statusCode < 400 {
		return "", ""
	}
	if isPlainObject(payload) {
		nestedError := objectGet(payload, "error")
		var nested any
		if isPlainObject(nestedError) {
			nested = nestedError
		}
		errorCode = firstString(
			objectGet(payload, "code"),
			objectGet(payload, "type"),
			objectGet(nested, "code"),
			objectGet(nested, "type"),
		)
		errorMessage = firstString(
			objectGet(payload, "message"),
			objectGet(nested, "message"),
			objectGet(payload, "error"),
		)
		return errorCode, errorMessage
	}
	if text, ok := payload.(string); ok {
		runes := []rune(text)
		if len(runes) > publicAPIErrorInfoMaxRunes {
			runes = runes[:publicAPIErrorInfoMaxRunes]
		}
		return "", string(runes)
	}
	if statusCode >= 500 {
		return "", publicAPIErrorServerInternalMessage
	}
	return "", "请求失败：HTTP " + itoa(statusCode)
}

// requestBodyRejectedReason mirrors requestBodyRejectedReason.
func requestBodyRejectedReason(spec *CaptureSpec, statusCode int) string {
	if spec.BodyRejected != nil {
		if statusCode == 413 || spec.BodyRejected.ErrorType == "entity.too.large" {
			return "request_body_too_large"
		}
		return "request_body_parse_failed"
	}
	if spec.Body != nil || statusCode < 400 {
		return ""
	}
	method := strings.ToUpper(spec.Method)
	if method != "POST" && method != "PUT" && method != "PATCH" {
		return ""
	}
	if contentLengthBytes(spec.ContentLength) > 0 {
		if statusCode == 413 {
			return "request_body_too_large"
		}
		return "request_body_parse_failed"
	}
	return ""
}

// contentLengthBytes mirrors contentLengthBytes: Number() semantics with a
// non-negative finite guard.
func contentLengthBytes(value string) int64 {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0
	}
	return int64(parsed)
}

func textOrUndefined(value string) any {
	if value == "" {
		return Undefined
	}
	return value
}

func objectGet(payload any, key string) any {
	switch typed := payload.(type) {
	case map[string]any:
		if typed == nil {
			return nil
		}
		return typed[key]
	case *snapshotObject:
		return typed.get(key)
	}
	return nil
}

// firstString mirrors firstString: the first string with a non-blank value,
// trimmed and capped at 1000 characters.
func firstString(values ...any) string {
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		runes := []rune(trimmed)
		if len(runes) > publicAPIErrorInfoMaxRunes {
			return string(runes[:publicAPIErrorInfoMaxRunes])
		}
		return trimmed
	}
	return ""
}

// sliceUTF8 mirrors sliceUtf8: hard byte cut; an invalid trailing sequence
// becomes U+FFFD when JSON-encoded (matching Buffer.toString('utf8')).
func sliceUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 0 {
		return ""
	}
	return value[:maxBytes]
}

func utf8ByteLen(value string) int { return len(value) }

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// snapshotBudget mirrors SnapshotBudgetState.
type snapshotBudget struct {
	remainingBytes int
	truncated      bool
	seen           map[*snapshotObject]bool
}

type boundedResult struct {
	value     any
	truncated bool
}

// boundedSnapshotValue mirrors boundedSnapshotValue.
func boundedSnapshotValue(value any, maxBytes int) boundedResult {
	state := &snapshotBudget{
		remainingBytes: maxBytes,
		seen:           map[*snapshotObject]bool{},
	}
	if state.remainingBytes < 1 {
		state.remainingBytes = 1
	}
	return boundedResult{value: cloneSnapshotValue(value, state, 0), truncated: state.truncated}
}

// cloneSnapshotValue mirrors cloneSnapshotValue: deep budgeted copy. Node uses
// a WeakSet for cycles; JSON-decoded payloads are acyclic and Go maps cannot
// serve as identity keys, so cycle protection covers *snapshotObject only.
func cloneSnapshotValue(value any, state *snapshotBudget, depth int) any {
	if state.remainingBytes <= 0 {
		return truncatedSnapshotMarker(state)
	}
	switch typed := value.(type) {
	case nil, undefinedValue:
		chargeSnapshotBytes(state, 4)
		return value
	case string:
		return cloneSnapshotString(typed, state)
	case bool:
		return cloneSnapshotScalar(typed, state)
	case int:
		return cloneSnapshotScalar(typed, state)
	case int64:
		return cloneSnapshotScalar(typed, state)
	case float64:
		return cloneSnapshotScalar(typed, state)
	case json.Number:
		return cloneSnapshotScalar(typed, state)
	case []byte:
		return cloneSnapshotBuffer(typed, state)
	case time.Time:
		return cloneSnapshotString(isoMillis(typed), state)
	}
	if depth >= publicAPISnapshotMaxDepth {
		return truncatedSnapshotMarker(state)
	}
	switch typed := value.(type) {
	case []any:
		return cloneSnapshotArray(typed, state, depth)
	case *snapshotObject:
		if state.seen[typed] {
			return cloneSnapshotString("[Circular]", state)
		}
		state.seen[typed] = true
		out := cloneSnapshotOrderedObject(typed, state, depth)
		delete(state.seen, typed)
		return out
	case map[string]any:
		return cloneSnapshotMap(typed, state, depth)
	case snapshotObject:
		return cloneSnapshotValue(&typed, state, depth)
	}
	// Unknown composite values fall back to their JSON text, mirroring the
	// Node String(value) branch for exotic primitives.
	if text, err := marshalCompact(value); err == nil {
		return cloneSnapshotString(text, state)
	}
	return cloneSnapshotString("[unavailable]", state)
}

func cloneSnapshotScalar(value any, state *snapshotBudget) any {
	text, err := marshalCompact(value)
	if err != nil {
		text = "null"
	}
	chargeSnapshotBytes(state, len(text))
	return value
}

func cloneSnapshotArray(value []any, state *snapshotBudget, depth int) any {
	chargeSnapshotBytes(state, 2)
	output := make([]any, 0, len(value))
	length := len(value)
	if length > publicAPISnapshotMaxEntries {
		length = publicAPISnapshotMaxEntries
	}
	for index := 0; index < length; index++ {
		if state.remainingBytes <= 0 {
			break
		}
		output = append(output, cloneSnapshotValue(value[index], state, depth+1))
	}
	if len(value) > length || state.remainingBytes <= 0 {
		output = append(output, truncatedSnapshotMarker(state))
	}
	return output
}

func cloneSnapshotOrderedObject(value *snapshotObject, state *snapshotBudget, depth int) *snapshotObject {
	chargeSnapshotBytes(state, 2)
	output := newSnapshotObject()
	count := 0
	for _, key := range value.keys {
		if count >= publicAPISnapshotMaxEntries || state.remainingBytes <= 0 {
			output.set("__truncated", true)
			state.truncated = true
			break
		}
		count++
		chargeSnapshotBytes(state, len(key)+4)
		output.set(key, cloneSnapshotValue(value.vals[key], state, depth+1))
	}
	return output
}

func cloneSnapshotMap(value map[string]any, state *snapshotBudget, depth int) *snapshotObject {
	chargeSnapshotBytes(state, 2)
	output := newSnapshotObject()
	count := 0
	for _, key := range sortedMapKeys(value) {
		if count >= publicAPISnapshotMaxEntries || state.remainingBytes <= 0 {
			output.set("__truncated", true)
			state.truncated = true
			break
		}
		count++
		chargeSnapshotBytes(state, len(key)+4)
		output.set(key, cloneSnapshotValue(value[key], state, depth+1))
	}
	return output
}

func cloneSnapshotBuffer(value []byte, state *snapshotBudget) any {
	previewBytes := len(value)
	if previewBytes > publicAPISnapshotStringPreviewBytes {
		previewBytes = publicAPISnapshotStringPreviewBytes
	}
	if previewBytes > state.remainingBytes {
		previewBytes = state.remainingBytes
	}
	if previewBytes < 0 {
		previewBytes = 0
	}
	chargeSnapshotBytes(state, previewBytes+64)
	if len(value) > previewBytes {
		state.truncated = true
	}
	return newSnapshotObject().
		set("type", "Buffer").
		set("byteLength", len(value)).
		set("preview", sliceUTF8(string(value[:previewBytes]), previewBytes)).
		set("truncated", len(value) > previewBytes)
}

func cloneSnapshotString(value string, state *snapshotBudget) string {
	size := len(value)
	if size <= state.remainingBytes {
		chargeSnapshotBytes(state, size)
		return value
	}
	state.truncated = true
	previewBytes := state.remainingBytes
	if previewBytes > publicAPISnapshotStringPreviewBytes {
		previewBytes = publicAPISnapshotStringPreviewBytes
	}
	if previewBytes < 0 {
		previewBytes = 0
	}
	state.remainingBytes = 0
	return sliceUTF8(value, previewBytes) + "...[truncated]"
}

func truncatedSnapshotMarker(state *snapshotBudget) string {
	state.truncated = true
	return "[truncated]"
}

func chargeSnapshotBytes(state *snapshotBudget, bytes int) {
	if bytes < 0 {
		bytes = 0
	}
	state.remainingBytes -= bytes
	if state.remainingBytes < 0 {
		state.truncated = true
		state.remainingBytes = 0
	}
}

func sortedMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
