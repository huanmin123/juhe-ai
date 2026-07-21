package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"reflect"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultNormalQueueCapacity           = 4096
	defaultFailureQueueCapacity          = defaultNormalQueueCapacity / 8
	defaultEmergencyFailureQueueCapacity = defaultFailureQueueCapacity / 4
	defaultNormalQueueBytes              = 16 * 1024 * 1024
	defaultFailureQueueBytes             = 4 * 1024 * 1024
	defaultEmergencyFailureQueueBytes    = 1024 * 1024
	maxFailureSnapshotMessageBytes       = 512
	maxFailureSnapshotValueBytes         = 1024
	maxFailureSnapshotStackBytes         = 4096
	maxFailureRecordAttrs                = 64
	maxFailureGroupDepth                 = 4
)

type RuntimeOptions struct {
	Role                 string
	NormalQueueCapacity  int
	FailureQueueCapacity int
	NormalQueueBytes     int64
	FailureQueueBytes    int64
}

type RuntimeStats struct {
	PendingNormal            int
	PendingFailure           int
	NormalDropped            uint64
	FailureDropped           uint64
	WriterErrors             uint64
	PendingBytes             int64
	LastWriterError          string
	LastWriterErrorAt        string
	LastWriterErrorTruncated bool
}

type Runtime struct {
	Logger     *slog.Logger
	dispatcher *asyncLogDispatcher
}

type asyncLogItem struct {
	handler slog.Handler
	record  slog.Record
	bytes   int64
	lane    asyncLogLane
}

type asyncLogLane uint8

const (
	asyncLogLaneNormal asyncLogLane = iota
	asyncLogLaneFailure
	asyncLogLaneEmergencyFailure
)

type asyncLogDispatcher struct {
	base                         slog.Handler
	normal                       chan asyncLogItem
	failure                      chan asyncLogItem
	emergencyFailure             chan asyncLogItem
	stop                         chan struct{}
	done                         chan struct{}
	acceptMu                     sync.RWMutex
	accepting                    bool
	stopOnce                     sync.Once
	normalDropped                atomic.Uint64
	failureDropped               atomic.Uint64
	writerErrors                 atomic.Uint64
	pendingNormalBytes           atomic.Int64
	pendingFailureBytes          atomic.Int64
	pendingEmergencyFailureBytes atomic.Int64
	maxNormalBytes               int64
	maxFailureBytes              int64
	maxEmergencyFailureBytes     int64
	reportedNormalDrop           uint64
	reportedFailureDrop          uint64
	lastFailureSnapshot          atomic.Pointer[failureDropSnapshot]
	lastWriterError              atomic.Pointer[writerErrorSnapshot]
}

func newAsyncLogRuntime(base slog.Handler, options RuntimeOptions) *Runtime {
	normalCapacity := options.NormalQueueCapacity
	if normalCapacity <= 0 {
		normalCapacity = defaultNormalQueueCapacity
	}
	failureCapacity := options.FailureQueueCapacity
	if failureCapacity <= 0 {
		failureCapacity = defaultFailureQueueCapacity
	}
	emergencyFailureCapacity := defaultEmergencyFailureQueueCapacity
	if options.FailureQueueCapacity > 0 {
		emergencyFailureCapacity = max(1, failureCapacity/4)
	}
	normalBytes := options.NormalQueueBytes
	if normalBytes <= 0 {
		normalBytes = defaultNormalQueueBytes
	}
	failureBytes := options.FailureQueueBytes
	if failureBytes <= 0 {
		failureBytes = defaultFailureQueueBytes
	}
	dispatcher := &asyncLogDispatcher{
		base:                     base,
		normal:                   make(chan asyncLogItem, normalCapacity),
		failure:                  make(chan asyncLogItem, failureCapacity),
		emergencyFailure:         make(chan asyncLogItem, emergencyFailureCapacity),
		stop:                     make(chan struct{}),
		done:                     make(chan struct{}),
		accepting:                true,
		maxNormalBytes:           normalBytes,
		maxFailureBytes:          failureBytes,
		maxEmergencyFailureBytes: defaultEmergencyFailureQueueBytes,
	}
	go dispatcher.run()
	return &Runtime{Logger: slog.New(&asyncSlogHandler{base: base, dispatcher: dispatcher}), dispatcher: dispatcher}
}

type asyncSlogHandler struct {
	base       slog.Handler
	dispatcher *asyncLogDispatcher
}

func (h *asyncSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *asyncSlogHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(logContextAttrs(ctx)...)
	if record.Level >= slog.LevelError {
		record = boundFailureRecord(record)
	} else {
		record = boundNormalRecord(record)
	}
	item := asyncLogItem{handler: h.base, record: record.Clone(), bytes: estimateRecordBytes(record)}
	h.dispatcher.acceptMu.RLock()
	defer h.dispatcher.acceptMu.RUnlock()
	if !h.dispatcher.accepting {
		h.dispatcher.recordDrop(record)
		return nil
	}
	if record.Level >= slog.LevelError {
		if h.dispatcher.tryEnqueue(h.dispatcher.failure, &h.dispatcher.pendingFailureBytes, h.dispatcher.maxFailureBytes, item, asyncLogLaneFailure) ||
			h.dispatcher.tryEnqueue(h.dispatcher.emergencyFailure, &h.dispatcher.pendingEmergencyFailureBytes, h.dispatcher.maxEmergencyFailureBytes, item, asyncLogLaneEmergencyFailure) {
			return nil
		}
		h.dispatcher.recordDrop(record)
		return nil
	}
	if !h.dispatcher.tryEnqueue(h.dispatcher.normal, &h.dispatcher.pendingNormalBytes, h.dispatcher.maxNormalBytes, item, asyncLogLaneNormal) {
		h.dispatcher.recordDrop(record)
	}
	return nil
}

func (h *asyncSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithAttrs(boundHandlerAttrs(attrs)), dispatcher: h.dispatcher}
}

func (h *asyncSlogHandler) WithGroup(name string) slog.Handler {
	boundedName, _ := boundedFailureSnapshotValue(name, maxFailureSnapshotValueBytes)
	return &asyncSlogHandler{base: h.base.WithGroup(boundedName), dispatcher: h.dispatcher}
}

func (d *asyncLogDispatcher) tryEnqueue(queue chan asyncLogItem, pendingBytes *atomic.Int64, maxBytes int64, item asyncLogItem, lane asyncLogLane) bool {
	if pendingBytes.Add(item.bytes) > maxBytes {
		pendingBytes.Add(-item.bytes)
		return false
	}
	item.lane = lane
	select {
	case queue <- item:
		return true
	default:
		pendingBytes.Add(-item.bytes)
		return false
	}
}

func (d *asyncLogDispatcher) recordDrop(record slog.Record) {
	if record.Level >= slog.LevelError {
		d.lastFailureSnapshot.Store(captureFailureDropSnapshot(record))
		d.failureDropped.Add(1)
		return
	}
	d.normalDropped.Add(1)
}

func (d *asyncLogDispatcher) run() {
	defer close(d.done)
	dropSummaryTicker := time.NewTicker(time.Minute)
	defer dropSummaryTicker.Stop()
	for {
		if item, ok := d.takeImmediate(); ok {
			d.handle(item)
			continue
		}
		select {
		case item := <-d.emergencyFailure:
			d.handle(item)
		case item := <-d.failure:
			d.handle(item)
		case item := <-d.normal:
			d.handle(item)
		case <-d.stop:
			d.drain()
			d.emitDropSummary()
			return
		case <-dropSummaryTicker.C:
			d.emitDropSummary()
		}
	}
}

type recordTruncationMetadata struct {
	truncated            bool
	originalMessageBytes int
	originalAttrCount    int
	fields               []slog.Attr
}

func (metadata *recordTruncationMetadata) addString(path string, value string) {
	metadata.truncated = true
	if len(metadata.fields) >= 16 {
		return
	}
	hash, hashScope := sha256Text(value)
	metadata.fields = append(metadata.fields, slog.Group(
		"field"+strconv.Itoa(len(metadata.fields)),
		slog.String("path", path),
		slog.Int("originalBytes", len(value)),
		slog.String("sha256", hash),
		slog.String("hashScope", hashScope),
	))
}

func sha256Text(value string) (string, string) {
	hasher := sha256.New()
	const sampleBytes = 4096
	if len(value) <= sampleBytes*2 {
		_, _ = hasher.Write([]byte(value))
		return hex.EncodeToString(hasher.Sum(nil)), "full"
	}
	_, _ = hasher.Write([]byte(value[:sampleBytes]))
	_, _ = hasher.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hasher.Write([]byte(value[len(value)-sampleBytes:]))
	return hex.EncodeToString(hasher.Sum(nil)), "first_last_4096_with_length"
}

func (metadata *recordTruncationMetadata) addOpaque(path string, typeName string) {
	metadata.truncated = true
	if len(metadata.fields) >= 16 {
		return
	}
	metadata.fields = append(metadata.fields, slog.Group(
		"field"+strconv.Itoa(len(metadata.fields)),
		slog.String("path", path),
		slog.String("type", typeName),
	))
}

func (metadata *recordTruncationMetadata) addAttrs(record *slog.Record) {
	if !metadata.truncated {
		return
	}
	record.AddAttrs(
		slog.Bool("truncated", true),
		slog.String("truncationReason", "async_log_record_budget"),
		slog.Int("originalMessageBytes", metadata.originalMessageBytes),
		slog.Int("originalAttrCount", metadata.originalAttrCount),
	)
	if len(metadata.fields) > 0 {
		record.AddAttrs(slog.Attr{Key: "truncatedFields", Value: slog.GroupValue(metadata.fields...)})
	}
}

func boundNormalRecord(record slog.Record) slog.Record {
	metadata := recordTruncationMetadata{
		originalMessageBytes: len(record.Message),
		originalAttrCount:    record.NumAttrs(),
	}
	boundedMessage, messageTruncated := boundedFailureSnapshotValue(record.Message, maxFailureSnapshotMessageBytes)
	if messageTruncated {
		metadata.addString("message", record.Message)
	}
	bounded := slog.NewRecord(record.Time, record.Level, boundedMessage, record.PC)
	attrCount := 0
	record.Attrs(func(attr slog.Attr) bool {
		if attrCount >= maxFailureRecordAttrs {
			metadata.truncated = true
			return false
		}
		attrCount++
		boundedAttr, _ := boundLogAttr(attr, 0, attr.Key, &metadata)
		bounded.AddAttrs(boundedAttr)
		return true
	})
	metadata.addAttrs(&bounded)
	return bounded
}

func boundHandlerAttrs(attrs []slog.Attr) []slog.Attr {
	metadata := recordTruncationMetadata{originalAttrCount: len(attrs)}
	bounded := make([]slog.Attr, 0, min(len(attrs), maxFailureRecordAttrs)+2)
	for index, attr := range attrs {
		if index >= maxFailureRecordAttrs {
			metadata.truncated = true
			break
		}
		boundedAttr, _ := boundLogAttr(attr, 0, attr.Key, &metadata)
		bounded = append(bounded, boundedAttr)
	}
	if metadata.truncated {
		bounded = append(bounded,
			slog.Bool("withAttrsTruncated", true),
			slog.String("withAttrsTruncationReason", "async_log_record_budget"),
			slog.Int("withAttrsOriginalCount", metadata.originalAttrCount),
		)
		if len(metadata.fields) > 0 {
			bounded = append(bounded, slog.Attr{
				Key:   "withAttrsTruncatedFields",
				Value: slog.GroupValue(metadata.fields...),
			})
		}
	}
	return bounded
}

func boundFailureRecord(record slog.Record) slog.Record {
	metadata := recordTruncationMetadata{
		originalMessageBytes: len(record.Message),
		originalAttrCount:    record.NumAttrs(),
	}
	boundedMessage, messageTruncated := boundedFailureSnapshotValue(record.Message, maxFailureSnapshotMessageBytes)
	if messageTruncated {
		metadata.addString("message", record.Message)
	}
	bounded := slog.NewRecord(record.Time, record.Level, boundedMessage, record.PC)
	hasFailureClass := false
	failureClass := ""
	hasStack := false
	hasStackSource := false
	hasErrorType := false
	hasErrorMessage := false
	hasErrorCauseChain := false
	var recordedError error
	attrCount := 0
	record.Attrs(func(attr slog.Attr) bool {
		if attrCount >= maxFailureRecordAttrs {
			metadata.truncated = true
			return false
		}
		attrCount++
		boundedAttr, attrError := boundLogAttr(attr, 0, attr.Key, &metadata)
		bounded.AddAttrs(boundedAttr)
		switch attr.Key {
		case "failureClass":
			hasFailureClass = true
			if boundedAttr.Value.Kind() == slog.KindString {
				failureClass = boundedAttr.Value.String()
			}
		case "stack":
			hasStack = true
		case "stackSource":
			hasStackSource = true
		case "errorType":
			hasErrorType = true
		case "errorMessage":
			hasErrorMessage = true
		case "errorCauseChain":
			hasErrorCauseChain = true
		case "error":
			recordedError = attrError
		}
		return true
	})
	if !hasFailureClass {
		bounded.AddAttrs(slog.String("failureClass", "unexpected"))
		failureClass = "unexpected"
	}
	if failureClass != "expected" && failureClass != "aborted" && !hasStack {
		stack, _ := boundedFailureSnapshotValue(string(debug.Stack()), maxFailureSnapshotStackBytes)
		bounded.AddAttrs(slog.String("stack", stack))
		if !hasStackSource {
			bounded.AddAttrs(slog.String("stackSource", "log_call_site_fallback"))
		}
	}
	if recordedError == nil {
		metadata.addAttrs(&bounded)
		return bounded
	}
	if !hasErrorType {
		errorType, _ := boundedFailureSnapshotValue(reflect.TypeOf(recordedError).String(), maxFailureSnapshotValueBytes)
		bounded.AddAttrs(slog.String("errorType", errorType))
	}
	if !hasErrorMessage {
		errorMessage, errorMessageTruncated := boundedErrorSnapshot(recordedError)
		if errorMessageTruncated {
			metadata.addOpaque("errorMessage", boundedTypeName(recordedError))
		}
		bounded.AddAttrs(slog.String("errorMessage", errorMessage))
	}
	if !hasErrorCauseChain {
		if causes := failureErrorCauseChain(recordedError); len(causes) > 0 {
			bounded.AddAttrs(slog.Any("errorCauseChain", causes))
		}
	}
	metadata.addAttrs(&bounded)
	return bounded
}

func boundLogAttr(attr slog.Attr, depth int, path string, metadata *recordTruncationMetadata) (slog.Attr, error) {
	value := attr.Value
	limit := failureSnapshotLimit(attr.Key)
	switch value.Kind() {
	case slog.KindString:
		original := value.String()
		bounded, truncated := boundedFailureSnapshotValue(original, limit)
		if truncated {
			metadata.addString(path, original)
		}
		return slog.String(attr.Key, bounded), nil
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64, slog.KindTime, slog.KindUint64:
		return attr, nil
	case slog.KindGroup:
		if depth >= maxFailureGroupDepth {
			metadata.addOpaque(path, "slog.Group")
			return slog.String(attr.Key, "[truncated slog.Group]"), nil
		}
		group := value.Group()
		attrs := make([]slog.Attr, 0, min(len(group), maxFailureRecordAttrs))
		for index, nested := range group {
			if index >= maxFailureRecordAttrs {
				metadata.addOpaque(path, "slog.Group")
				break
			}
			nestedPath := nested.Key
			if path != "" {
				nestedPath = path + "." + nested.Key
			}
			bounded, _ := boundLogAttr(nested, depth+1, nestedPath, metadata)
			attrs = append(attrs, bounded)
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(attrs...)}, nil
	case slog.KindAny:
		anyValue := value.Any()
		if err, ok := anyValue.(error); ok {
			message, truncated := boundedErrorSnapshot(err)
			if truncated {
				metadata.addOpaque(path, boundedTypeName(err))
			}
			return slog.String(attr.Key, message), err
		}
		typeName := "<nil>"
		if valueType := reflect.TypeOf(anyValue); valueType != nil {
			typeName = valueType.String()
		}
		metadata.addOpaque(path, typeName)
		bounded, _ := boundedFailureSnapshotValue("[opaque slog.Any type="+typeName+"]", maxFailureSnapshotValueBytes)
		return slog.String(attr.Key, bounded), nil
	case slog.KindLogValuer:
		metadata.addOpaque(path, "slog.LogValuer")
		return slog.String(attr.Key, "[opaque slog.LogValuer]"), nil
	default:
		metadata.addOpaque(path, "slog.Value")
		return slog.String(attr.Key, "[opaque slog.Value]"), nil
	}
}

func boundedErrorMessage(err error) string {
	message, _ := boundedErrorSnapshot(err)
	return message
}

func boundedErrorSnapshot(err error) (message string, truncated bool) {
	defer func() {
		if recover() != nil {
			message = "[error message panicked]"
			truncated = true
		}
	}()
	return boundedFailureSnapshotValue(err.Error(), maxFailureSnapshotValueBytes)
}

type failureErrorCause struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func failureErrorCauseChain(err error) (causes []failureErrorCause) {
	const maxCauseDepth = 8
	causes = make([]failureErrorCause, 0, maxCauseDepth)
	defer func() {
		if recover() != nil && len(causes) < maxCauseDepth {
			causes = append(causes, failureErrorCause{Type: "panic", Message: "error unwrap panicked"})
		}
	}()
	var appendChildren func(error)
	var appendCause func(error)
	appendCause = func(cause error) {
		if cause == nil || len(causes) >= maxCauseDepth {
			return
		}
		causes = append(causes, failureErrorCause{
			Type:    boundedTypeName(cause),
			Message: boundedErrorMessage(cause),
		})
		appendChildren(cause)
	}
	appendChildren = func(parent error) {
		if parent == nil || len(causes) >= maxCauseDepth {
			return
		}
		switch unwrapped := parent.(type) {
		case interface{ Unwrap() []error }:
			for _, cause := range unwrapped.Unwrap() {
				appendCause(cause)
				if len(causes) >= maxCauseDepth {
					return
				}
			}
		case interface{ Unwrap() error }:
			appendCause(unwrapped.Unwrap())
		}
	}
	appendChildren(err)
	return causes
}

func boundedTypeName(value any) string {
	valueType := reflect.TypeOf(value)
	if valueType == nil {
		return "<nil>"
	}
	name, _ := boundedFailureSnapshotValue(valueType.String(), maxFailureSnapshotValueBytes)
	return name
}

func (d *asyncLogDispatcher) takeImmediate() (asyncLogItem, bool) {
	select {
	case item := <-d.emergencyFailure:
		return item, true
	default:
	}
	select {
	case item := <-d.failure:
		return item, true
	default:
	}
	select {
	case item := <-d.emergencyFailure:
		return item, true
	case item := <-d.failure:
		return item, true
	case item := <-d.normal:
		return item, true
	default:
		return asyncLogItem{}, false
	}
}

func (d *asyncLogDispatcher) drain() {
	for {
		item, ok := d.takeImmediate()
		if !ok {
			return
		}
		d.handle(item)
	}
}

func (d *asyncLogDispatcher) handle(item asyncLogItem) {
	defer d.releaseBytes(item)
	if err := item.handler.Handle(context.Background(), item.record); err != nil {
		d.writerErrors.Add(1)
		d.lastWriterError.Store(captureWriterErrorSnapshot(err))
	}
}

func (d *asyncLogDispatcher) releaseBytes(item asyncLogItem) {
	switch item.lane {
	case asyncLogLaneFailure:
		d.pendingFailureBytes.Add(-item.bytes)
	case asyncLogLaneEmergencyFailure:
		d.pendingEmergencyFailureBytes.Add(-item.bytes)
	default:
		d.pendingNormalBytes.Add(-item.bytes)
	}
}

func estimateRecordBytes(record slog.Record) int64 {
	size := int64(len(record.Message) + 128)
	attrCount := 0
	record.Attrs(func(attr slog.Attr) bool {
		if attrCount >= maxFailureRecordAttrs {
			return false
		}
		attrCount++
		size += int64(len(attr.Key) + estimateSlogValueBytes(attr.Value, 0) + 16)
		return size < defaultNormalQueueBytes
	})
	return size
}

func estimateSlogValueBytes(value slog.Value, depth int) int {
	switch value.Kind() {
	case slog.KindString:
		return min(len(value.String()), maxFailureSnapshotStackBytes)
	case slog.KindGroup:
		if depth >= maxFailureGroupDepth {
			return 32
		}
		size := 0
		for index, attr := range value.Group() {
			if index >= maxFailureRecordAttrs {
				break
			}
			size += min(len(attr.Key), maxFailureSnapshotValueBytes) + estimateSlogValueBytes(attr.Value, depth+1)
		}
		return size
	default:
		return 32
	}
}

func (d *asyncLogDispatcher) emitDropSummary() {
	normalDropped := d.normalDropped.Load()
	failureDropped := d.failureDropped.Load()
	if normalDropped == d.reportedNormalDrop && failureDropped == d.reportedFailureDrop {
		return
	}
	record := slog.NewRecord(time.Now(), slog.LevelWarn, "异步日志队列丢弃摘要", 0)
	record.AddAttrs(
		slog.String("event", "system_log_drop"),
		slog.Uint64("normalDropped", normalDropped),
		slog.Uint64("failureDropped", failureDropped),
	)
	if snapshot := d.lastFailureSnapshot.Load(); snapshot != nil {
		record.AddAttrs(snapshot.logAttr())
	}
	if err := d.base.Handle(context.Background(), record); err != nil {
		d.writerErrors.Add(1)
		d.lastWriterError.Store(captureWriterErrorSnapshot(err))
		return
	}
	d.reportedNormalDrop = normalDropped
	d.reportedFailureDrop = failureDropped
}

func (d *asyncLogDispatcher) shutdown(ctx context.Context) error {
	d.acceptMu.Lock()
	if d.accepting {
		d.accepting = false
		d.stopOnce.Do(func() { close(d.stop) })
	}
	d.acceptMu.Unlock()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	return r.dispatcher.shutdown(ctx)
}

func (r *Runtime) Stats() RuntimeStats {
	stats := RuntimeStats{
		PendingNormal:  len(r.dispatcher.normal),
		PendingFailure: len(r.dispatcher.failure) + len(r.dispatcher.emergencyFailure),
		NormalDropped:  r.dispatcher.normalDropped.Load(),
		FailureDropped: r.dispatcher.failureDropped.Load(),
		WriterErrors:   r.dispatcher.writerErrors.Load(),
		PendingBytes: r.dispatcher.pendingNormalBytes.Load() +
			r.dispatcher.pendingFailureBytes.Load() +
			r.dispatcher.pendingEmergencyFailureBytes.Load(),
	}
	if snapshot := r.dispatcher.lastWriterError.Load(); snapshot != nil {
		stats.LastWriterError = snapshot.Message
		stats.LastWriterErrorAt = snapshot.CapturedAt
		stats.LastWriterErrorTruncated = snapshot.Truncated
	}
	return stats
}

type writerErrorSnapshot struct {
	CapturedAt string
	Message    string
	Truncated  bool
}

func captureWriterErrorSnapshot(err error) *writerErrorSnapshot {
	message, truncated := boundedErrorSnapshot(err)
	return &writerErrorSnapshot{CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Message: message, Truncated: truncated}
}

type failureDropSnapshot struct {
	CapturedAt      string
	Level           string
	Message         string
	Event           string
	Stage           string
	Outcome         string
	FailureClass    string
	TraceID         string
	RequestID       string
	JobID           string
	ParentID        string
	ErrorType       string
	ErrorMessage    string
	ErrorCauseChain []failureErrorCause
	Stack           string
	StackSource     string
	Truncated       bool
}

func captureFailureDropSnapshot(record slog.Record) *failureDropSnapshot {
	message, truncated := boundedFailureSnapshotValue(record.Message, maxFailureSnapshotMessageBytes)
	snapshot := &failureDropSnapshot{
		CapturedAt: record.Time.UTC().Format(time.RFC3339Nano),
		Level:      record.Level.String(),
		Message:    message,
		Truncated:  truncated,
	}
	var recordedError error
	record.Attrs(func(attr slog.Attr) bool {
		value := attr.Value
		if attr.Key == "error" {
			if err, ok := value.Any().(error); ok {
				recordedError = err
			}
		}
		if attr.Key == "errorCauseChain" && value.Kind() == slog.KindAny {
			if causes, ok := value.Any().([]failureErrorCause); ok {
				for index, cause := range causes {
					if index >= 8 {
						snapshot.Truncated = true
						break
					}
					cause.Type, truncated = boundedFailureSnapshotValue(cause.Type, maxFailureSnapshotValueBytes)
					snapshot.Truncated = snapshot.Truncated || truncated
					cause.Message, truncated = boundedFailureSnapshotValue(cause.Message, maxFailureSnapshotValueBytes)
					snapshot.Truncated = snapshot.Truncated || truncated
					snapshot.ErrorCauseChain = append(snapshot.ErrorCauseChain, cause)
				}
			}
		}
		if value.Kind() != slog.KindString {
			return true
		}
		bounded, valueTruncated := boundedFailureSnapshotValue(value.String(), failureSnapshotLimit(attr.Key))
		snapshot.Truncated = snapshot.Truncated || valueTruncated
		switch attr.Key {
		case "event":
			snapshot.Event = bounded
		case "stage":
			snapshot.Stage = bounded
		case "outcome":
			snapshot.Outcome = bounded
		case "failureClass":
			snapshot.FailureClass = bounded
		case "traceId":
			snapshot.TraceID = bounded
		case "requestId":
			snapshot.RequestID = bounded
		case "jobId":
			snapshot.JobID = bounded
		case "parentId":
			snapshot.ParentID = bounded
		case "errorType":
			snapshot.ErrorType = bounded
		case "errorMessage":
			snapshot.ErrorMessage = bounded
		case "stack":
			snapshot.Stack = bounded
		case "stackSource":
			snapshot.StackSource = bounded
		}
		return true
	})
	if recordedError != nil {
		if snapshot.ErrorType == "" {
			snapshot.ErrorType, _ = boundedFailureSnapshotValue(reflect.TypeOf(recordedError).String(), maxFailureSnapshotValueBytes)
		}
		if snapshot.ErrorMessage == "" {
			snapshot.ErrorMessage, truncated = boundedFailureSnapshotValue(recordedError.Error(), maxFailureSnapshotValueBytes)
			snapshot.Truncated = snapshot.Truncated || truncated
		}
		for _, cause := range failureErrorCauseChain(recordedError) {
			cause.Type, truncated = boundedFailureSnapshotValue(cause.Type, maxFailureSnapshotValueBytes)
			snapshot.Truncated = snapshot.Truncated || truncated
			cause.Message, truncated = boundedFailureSnapshotValue(cause.Message, maxFailureSnapshotValueBytes)
			snapshot.Truncated = snapshot.Truncated || truncated
			snapshot.ErrorCauseChain = append(snapshot.ErrorCauseChain, cause)
		}
	}
	return snapshot
}

func failureSnapshotLimit(key string) int {
	if key == "stack" {
		return maxFailureSnapshotStackBytes
	}
	return maxFailureSnapshotValueBytes
}

func boundedFailureSnapshotValue(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit], true
}

func (s *failureDropSnapshot) logAttr() slog.Attr {
	attrs := []slog.Attr{
		slog.String("capturedAt", s.CapturedAt),
		slog.String("level", s.Level),
		slog.String("message", s.Message),
		slog.String("event", s.Event),
		slog.String("stage", s.Stage),
		slog.String("outcome", s.Outcome),
		slog.String("failureClass", s.FailureClass),
		slog.String("traceId", s.TraceID),
		slog.String("requestId", s.RequestID),
		slog.String("jobId", s.JobID),
		slog.String("parentId", s.ParentID),
		slog.String("errorType", s.ErrorType),
		slog.String("errorMessage", s.ErrorMessage),
		slog.Any("errorCauseChain", s.ErrorCauseChain),
		slog.String("stack", s.Stack),
		slog.String("stackSource", s.StackSource),
		slog.Bool("truncated", s.Truncated),
	}
	return slog.Attr{Key: "lastFailure", Value: slog.GroupValue(attrs...)}
}
