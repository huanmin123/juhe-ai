package oauthrefresh

// Bounded failure context logging, ported from the Node module
// shared/logging/log-failure-context.ts (ExpectedFailureContext,
// UnexpectedFailureContext, captureExpectedFailureContext,
// captureUnexpectedFailureContext).
//
// Node semantics preserved:
//   - expected failures carry a stable reasonCode + sanitized decisionInputs;
//   - unexpected failures carry a bounded error capture with a cause chain of
//     depth 4, optional snapshots and a 64 KiB UTF-8 byte budget;
//   - every over-limit value is cut to a bounded UTF-8 prefix with
//     fieldSizes (original length in UTF-16 code units), fieldHashes
//     (sha256 over the first 8 KiB of UTF-8 content) and a single
//     truncationReason = "field_or_event_limit" marker.
//
// Go-specific adaptations (documented):
//   - Go errors carry no creation-site stack, so CapturedError.Stack stays
//     empty unless the error exposes a StackTrace() string method.
//   - JS property descriptors (unreadable accessors) do not exist; the
//     "[unreadable: accessor]" fallback has no Go counterpart.
//   - Map keys are sorted so budget allocation and hashes are deterministic
//     (Node iterates insertion order).
//   - errors.Join groups have no Node analog; the first joined error continues
//     the cause chain.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	failureMaxStringLength      = 8 * 1024
	failureMaxCauseDepth        = 4
	failureMaxEventBytes        = 64 * 1024
	failureMaxObjectDepth       = 8
	failureMaxCollectionEntries = 100
	failureMaxHashInputLength   = 8 * 1024
	// failureTruncationReason mirrors the single truncation marker Node sets
	// whenever a string, depth, collection, cycle or the event byte budget
	// forced a cut.
	failureTruncationReason = "field_or_event_limit"
)

// CapturedError mirrors the Node CapturedError shape (name/message/stack/code
// + bounded cause chain).
type CapturedError struct {
	Name    string         `json:"name"`
	Message string         `json:"message"`
	Stack   string         `json:"stack,omitempty"`
	Code    string         `json:"code,omitempty"`
	Cause   *CapturedError `json:"cause,omitempty"`
}

// LogValue renders the captured error as a bounded slog group.
func (e *CapturedError) LogValue() slog.Value {
	if e == nil {
		return slog.AnyValue(nil)
	}
	attrs := []slog.Attr{
		slog.String("name", e.Name),
		slog.String("message", e.Message),
	}
	if e.Stack != "" {
		attrs = append(attrs, slog.String("stack", e.Stack))
	}
	if e.Code != "" {
		attrs = append(attrs, slog.String("code", e.Code))
	}
	if e.Cause != nil {
		attrs = append(attrs, slog.Any("cause", e.Cause))
	}
	return slog.GroupValue(attrs...)
}

// UnexpectedFailureContext mirrors Node UnexpectedFailureContext: the bounded
// error capture plus optional stage/queue/retry/decision snapshots, all inside
// one 64 KiB UTF-8 byte budget.
type UnexpectedFailureContext struct {
	FailureClass     string            `json:"failureClass"`
	Error            *CapturedError    `json:"error,omitempty"`
	StageSnapshot    map[string]any    `json:"stageSnapshot,omitempty"`
	QueueSnapshot    map[string]any    `json:"queueSnapshot,omitempty"`
	RetryState       map[string]any    `json:"retryState,omitempty"`
	DecisionInputs   map[string]any    `json:"decisionInputs,omitempty"`
	RedactedFields   []string          `json:"redactedFields"`
	FieldSizes       map[string]int    `json:"fieldSizes"`
	FieldHashes      map[string]string `json:"fieldHashes"`
	TruncationReason string            `json:"truncationReason,omitempty"`
}

// ExpectedFailureContext mirrors Node ExpectedFailureContext: a stable
// reasonCode plus the sanitized decision inputs under the same byte budget.
type ExpectedFailureContext struct {
	FailureClass     string            `json:"failureClass"`
	ReasonCode       string            `json:"reasonCode"`
	DecisionInputs   map[string]any    `json:"decisionInputs,omitempty"`
	RedactedFields   []string          `json:"redactedFields"`
	FieldSizes       map[string]int    `json:"fieldSizes"`
	FieldHashes      map[string]string `json:"fieldHashes"`
	TruncationReason string            `json:"truncationReason,omitempty"`
}

// FailureCaptureOptions mirrors the Node options bundle.
type FailureCaptureOptions struct {
	StageSnapshot  map[string]any
	QueueSnapshot  map[string]any
	RetryState     map[string]any
	DecisionInputs map[string]any
}

// failureCaptureState mirrors CaptureState.
type failureCaptureState struct {
	redactedFields []string
	fieldSizes     map[string]int
	fieldHashes    map[string]string
	truncated      bool
	remainingBytes int
	seen           map[uintptr]struct{}
}

func newFailureCaptureState() *failureCaptureState {
	return &failureCaptureState{
		redactedFields: []string{},
		fieldSizes:     map[string]int{},
		fieldHashes:    map[string]string{},
		remainingBytes: failureMaxEventBytes,
		seen:           map[uintptr]struct{}{},
	}
}

func (s *failureCaptureState) consume(bytes int) {
	s.remainingBytes -= bytes
	if s.remainingBytes < 0 {
		s.remainingBytes = 0
	}
	if s.remainingBytes == 0 {
		s.truncated = true
	}
}

// CaptureUnexpectedFailureContext mirrors captureUnexpectedFailureContext.
func CaptureUnexpectedFailureContext(err error, options FailureCaptureOptions) UnexpectedFailureContext {
	state := newFailureCaptureState()
	context := UnexpectedFailureContext{
		FailureClass:   "unexpected",
		Error:          captureFailureError(err, state, 0),
		RedactedFields: state.redactedFields,
		FieldSizes:     state.fieldSizes,
		FieldHashes:    state.fieldHashes,
	}
	if options.StageSnapshot != nil {
		context.StageSnapshot = sanitizeFailureRecord(options.StageSnapshot, "stageSnapshot", state)
	}
	if options.QueueSnapshot != nil {
		context.QueueSnapshot, _ = sanitizeFailureValue(options.QueueSnapshot, "queueSnapshot", state, 0).(map[string]any)
	}
	if options.RetryState != nil {
		context.RetryState, _ = sanitizeFailureValue(options.RetryState, "retryState", state, 0).(map[string]any)
	}
	if options.DecisionInputs != nil {
		context.DecisionInputs, _ = sanitizeFailureValue(options.DecisionInputs, "decisionInputs", state, 0).(map[string]any)
	}
	if state.truncated {
		context.TruncationReason = failureTruncationReason
	}
	return context
}

// CaptureExpectedFailureContext mirrors captureExpectedFailureContext: an
// empty reasonCode is an error (可预知失败必须提供 reasonCode).
func CaptureExpectedFailureContext(reasonCode string, decisionInputs map[string]any) (ExpectedFailureContext, error) {
	if strings.TrimSpace(reasonCode) == "" {
		return ExpectedFailureContext{}, errors.New("可预知失败必须提供 reasonCode")
	}
	state := newFailureCaptureState()
	var sanitized map[string]any
	if decisionInputs != nil {
		sanitized, _ = sanitizeFailureValue(decisionInputs, "decisionInputs", state, 0).(map[string]any)
	}
	context := ExpectedFailureContext{
		FailureClass:   "expected",
		ReasonCode:     reasonCode,
		DecisionInputs: sanitized,
		RedactedFields: state.redactedFields,
		FieldSizes:     state.fieldSizes,
		FieldHashes:    state.fieldHashes,
	}
	if state.truncated {
		context.TruncationReason = failureTruncationReason
	}
	return context, nil
}

// LogValue renders the expected context as a flat slog group (Node flattens
// the context fields into the top-level log payload).
func (c ExpectedFailureContext) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("failureClass", c.FailureClass),
		slog.String("reasonCode", c.ReasonCode),
	}
	if c.DecisionInputs != nil {
		attrs = append(attrs, slog.Any("decisionInputs", c.DecisionInputs))
	}
	attrs = append(attrs,
		slog.Any("redactedFields", c.RedactedFields),
		slog.Any("fieldSizes", c.FieldSizes),
		slog.Any("fieldHashes", c.FieldHashes),
	)
	if c.TruncationReason != "" {
		attrs = append(attrs, slog.String("truncationReason", c.TruncationReason))
	}
	return slog.GroupValue(attrs...)
}

// LogValue renders the unexpected context as a flat slog group.
func (c UnexpectedFailureContext) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("failureClass", c.FailureClass)}
	if c.Error != nil {
		attrs = append(attrs, slog.Any("error", c.Error))
	}
	if c.StageSnapshot != nil {
		attrs = append(attrs, slog.Any("stageSnapshot", c.StageSnapshot))
	}
	if c.QueueSnapshot != nil {
		attrs = append(attrs, slog.Any("queueSnapshot", c.QueueSnapshot))
	}
	if c.RetryState != nil {
		attrs = append(attrs, slog.Any("retryState", c.RetryState))
	}
	if c.DecisionInputs != nil {
		attrs = append(attrs, slog.Any("decisionInputs", c.DecisionInputs))
	}
	attrs = append(attrs,
		slog.Any("redactedFields", c.RedactedFields),
		slog.Any("fieldSizes", c.FieldSizes),
		slog.Any("fieldHashes", c.FieldHashes),
	)
	if c.TruncationReason != "" {
		attrs = append(attrs, slog.String("truncationReason", c.TruncationReason))
	}
	return slog.GroupValue(attrs...)
}

// captureFailureError mirrors captureError. A non-error input cannot occur at
// this boundary (Go callers pass error), so the NonErrorThrown branch is
// unnecessary here. The cause chain follows errors.Unwrap (then the first
// error of a joined group) with a depth cap of failureMaxCauseDepth; a deeper
// remaining chain sets the truncation marker.
func captureFailureError(err error, state *failureCaptureState, depth int) *CapturedError {
	if err == nil {
		return nil
	}
	captured := &CapturedError{
		Name:    truncateFailureString(errorTypeName(err), "error.name", state),
		Message: truncateFailureString(err.Error(), "error.message", state),
	}
	if code := failureErrorCode(err); code != "" {
		captured.Code = truncateFailureString(code, "error.code", state)
	}
	if stack := failureErrorStack(err); stack != "" {
		captured.Stack = truncateFailureString(stack, "error.stack", state)
	}
	if next := failureNextCause(err); next != nil {
		if depth < failureMaxCauseDepth {
			captured.Cause = captureFailureError(next, state, depth+1)
		} else {
			state.truncated = true
		}
	}
	return captured
}

// errorTypeName renders a stable, readable type name for the captured error
// (Node error.name).
func errorTypeName(err error) string {
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	if name == "" {
		name = "Error"
	}
	return name
}

// failureNextCause mirrors the Node cause descriptor walk: Unwrap first, then
// the first error of a joined group (Go-specific; Node cause is single).
func failureNextCause(err error) error {
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		if next := unwrapper.Unwrap(); next != nil {
			return next
		}
	}
	if joiner, ok := err.(interface{ Unwrap() []error }); ok {
		for _, candidate := range joiner.Unwrap() {
			if candidate != nil {
				return candidate
			}
		}
	}
	return nil
}

// failureErrorCode reads an optional string error code (Node error.code).
func failureErrorCode(err error) string {
	if coded, ok := err.(interface{ ErrorCode() string }); ok {
		return coded.ErrorCode()
	}
	return ""
}

// failureErrorStack reads an optional pkg/errors-style stack trace. Standard
// library errors leave it empty (documented adaptation).
func failureErrorStack(err error) string {
	if tracer, ok := err.(interface{ StackTrace() string }); ok {
		return tracer.StackTrace()
	}
	return ""
}

// failureMarkSeen registers map/slice/pointer identity; Node's WeakSet never
// releases an entry, so shared (non-cyclic) references are also flagged.
func failureMarkSeen(value reflect.Value, state *failureCaptureState) bool {
	switch value.Kind() {
	case reflect.Map, reflect.Slice, reflect.Ptr:
		pointer := value.Pointer()
		if _, ok := state.seen[pointer]; ok {
			return true
		}
		state.seen[pointer] = struct{}{}
	}
	return false
}

// sanitizeFailureRecord mirrors sanitizeRecord.
func sanitizeFailureRecord(value map[string]any, path string, state *failureCaptureState) map[string]any {
	sanitized, _ := sanitizeFailureValue(value, path, state, 0).(map[string]any)
	return sanitized
}

// sanitizeFailureValue mirrors sanitizeValue.
func sanitizeFailureValue(value any, path string, state *failureCaptureState, depth int) any {
	if state.remainingBytes <= 0 {
		state.truncated = true
		return "[truncated: event byte budget]"
	}
	switch typed := value.(type) {
	case nil:
		state.consume(16)
		return nil
	case string:
		return truncateFailureString(typed, path, state)
	case bool:
		state.consume(16)
		return typed
	}
	rValue := reflect.ValueOf(value)
	switch rValue.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rValue.IsNil() {
			state.consume(16)
			return nil
		}
		return sanitizeFailureValue(rValue.Elem().Interface(), path, state, depth)
	case reflect.Map:
		if depth >= failureMaxObjectDepth {
			state.truncated = true
			return "[truncated: depth limit]"
		}
		if failureMarkSeen(rValue, state) {
			state.truncated = true
			return "[truncated: circular reference]"
		}
		return sanitizeFailureMap(rValue, path, state, depth)
	case reflect.Slice, reflect.Array:
		if depth >= failureMaxObjectDepth {
			state.truncated = true
			return "[truncated: depth limit]"
		}
		if failureMarkSeen(rValue, state) {
			state.truncated = true
			return "[truncated: circular reference]"
		}
		return sanitizeFailureSlice(rValue, path, state, depth)
	case reflect.Struct:
		// Node would walk JSON properties; Go structs at the capture boundary
		// are rendered through their fmt form to stay bounded and lossless
		// enough for diagnosis.
		return truncateFailureString(fmt.Sprintf("%v", value), path, state)
	case reflect.Func:
		state.consume(16)
		return "[function]"
	default:
		state.consume(16)
		return value
	}
}

// sanitizeFailureMap mirrors the Node object-walk: entries are visited in
// sorted key order (Go maps are unordered) so budget allocation and hashes are
// deterministic.
func sanitizeFailureMap(value reflect.Value, path string, state *failureCaptureState, depth int) map[string]any {
	keys := value.MapKeys()
	texts := make([]string, 0, len(keys))
	byTexts := make(map[string]reflect.Value, len(keys))
	for _, key := range keys {
		text := fmt.Sprintf("%v", key.Interface())
		texts = append(texts, text)
		byTexts[text] = key
	}
	slices.Sort(texts)
	output := map[string]any{}
	scannedKeyCount := 0
	entryCount := 0
	for _, key := range texts {
		scannedKeyCount++
		if scannedKeyCount > failureMaxCollectionEntries {
			state.truncated = true
			break
		}
		if entryCount >= failureMaxCollectionEntries {
			state.truncated = true
			break
		}
		entryCount++
		if state.remainingBytes <= 0 {
			state.truncated = true
			output["_truncated"] = "event byte budget"
			break
		}
		boundedKey := boundedUTF8Prefix(key, min(failureMaxStringLength, state.remainingBytes))
		nestedPath := path + "." + boundedKey
		state.consume(len(boundedKey))
		output[boundedKey] = sanitizeFailureValue(value.MapIndex(byTexts[key]).Interface(), nestedPath, state, depth+1)
	}
	return output
}

// sanitizeFailureSlice mirrors the Node array-walk: at most
// failureMaxCollectionEntries entries are kept.
func sanitizeFailureSlice(value reflect.Value, path string, state *failureCaptureState, depth int) []any {
	length := value.Len()
	output := make([]any, 0, min(length, failureMaxCollectionEntries))
	for index := 0; index <= failureMaxCollectionEntries && index < length; index++ {
		if index == failureMaxCollectionEntries {
			state.truncated = true
			break
		}
		output = append(output, sanitizeFailureValue(value.Index(index).Interface(), fmt.Sprintf("%s[%d]", path, index), state, depth+1))
	}
	return output
}

// truncateFailureString mirrors truncateString: values within a third of the
// remaining allowance pass through whole, anything else is cut to the
// UTF-8-safe prefix with size+hash attribution.
func truncateFailureString(value string, path string, state *failureCaptureState) string {
	allowed := failureMaxStringLength
	if state.remainingBytes < allowed {
		allowed = state.remainingBytes
	}
	if allowed < 0 {
		allowed = 0
	}
	if utf16Length(value) <= allowed/3 {
		state.consume(len(value))
		return value
	}
	output := boundedUTF8Prefix(value, allowed)
	if output == value {
		state.consume(len(output))
		return output
	}
	state.truncated = true
	// Full byte length/hash would make failure capture proportional to
	// hostile input size (Node comment preserved).
	state.fieldSizes[path] = utf16Length(value)
	state.fieldHashes[path] = boundedFailureHash(value)
	state.consume(len(output))
	return output
}

// boundedUTF8Prefix mirrors boundedUTF8Prefix: the longest valid UTF-8 prefix
// within maxBytes, dropping one trailing truncated sequence.
func boundedUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	raw := []byte(value)
	if len(raw) <= maxBytes {
		return value
	}
	cut := raw[:maxBytes]
	for len(cut) > 0 {
		lastRune, size := utf8.DecodeLastRune(cut)
		if lastRune != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return string(cut)
}

// utf16Length counts UTF-16 code units, the JS string .length semantics the
// Node fieldSizes budget is defined over.
func utf16Length(value string) int {
	units := 0
	for _, r := range value {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// boundedFailureHash mirrors sha256(value.slice(0, maxHashInputLength)): the
// first 8 KiB of code points encoded as UTF-8, hex digested.
func boundedFailureHash(value string) string {
	runes := []rune(value)
	if len(runes) > failureMaxHashInputLength {
		runes = runes[:failureMaxHashInputLength]
	}
	sum := sha256.Sum256([]byte(string(runes)))
	return hex.EncodeToString(sum[:])
}
