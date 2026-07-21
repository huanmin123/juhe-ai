package logging

import (
	"context"
	"log/slog"
	"reflect"
	"runtime/debug"
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
)

type RuntimeOptions struct {
	Role                 string
	NormalQueueCapacity  int
	FailureQueueCapacity int
	NormalQueueBytes     int64
	FailureQueueBytes    int64
}

type RuntimeStats struct {
	PendingNormal  int
	PendingFailure int
	NormalDropped  uint64
	FailureDropped uint64
	WriterErrors   uint64
	PendingBytes   int64
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
		addFailureFallbackAttrs(&record)
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
	return &asyncSlogHandler{base: h.base.WithAttrs(attrs), dispatcher: h.dispatcher}
}

func (h *asyncSlogHandler) WithGroup(name string) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithGroup(name), dispatcher: h.dispatcher}
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

func addFailureFallbackAttrs(record *slog.Record) {
	hasFailureClass := false
	failureClass := ""
	hasStack := false
	hasStackSource := false
	hasErrorType := false
	hasErrorMessage := false
	hasErrorCauseChain := false
	var recordedError error
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "failureClass":
			hasFailureClass = true
			if attr.Value.Kind() == slog.KindString {
				failureClass = attr.Value.String()
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
			if err, ok := attr.Value.Resolve().Any().(error); ok {
				recordedError = err
			}
		}
		return true
	})
	if !hasFailureClass {
		record.AddAttrs(slog.String("failureClass", "unexpected"))
		failureClass = "unexpected"
	}
	if failureClass != "expected" && failureClass != "aborted" && !hasStack {
		record.AddAttrs(slog.String("stack", string(debug.Stack())))
		if !hasStackSource {
			record.AddAttrs(slog.String("stackSource", "log_call_site_fallback"))
		}
	}
	if recordedError == nil {
		return
	}
	if !hasErrorType {
		record.AddAttrs(slog.String("errorType", reflect.TypeOf(recordedError).String()))
	}
	if !hasErrorMessage {
		record.AddAttrs(slog.String("errorMessage", recordedError.Error()))
	}
	if !hasErrorCauseChain {
		if causes := failureErrorCauseChain(recordedError); len(causes) > 0 {
			record.AddAttrs(slog.Any("errorCauseChain", causes))
		}
	}
}

type failureErrorCause struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func failureErrorCauseChain(err error) []failureErrorCause {
	const maxCauseDepth = 8
	causes := make([]failureErrorCause, 0, maxCauseDepth)
	var appendChildren func(error)
	var appendCause func(error)
	appendCause = func(cause error) {
		if cause == nil || len(causes) >= maxCauseDepth {
			return
		}
		causes = append(causes, failureErrorCause{
			Type:    reflect.TypeOf(cause).String(),
			Message: cause.Error(),
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
	record.Attrs(func(attr slog.Attr) bool {
		size += int64(len(attr.Key) + len(attr.Value.String()) + 16)
		return true
	})
	return size
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
	return RuntimeStats{
		PendingNormal:  len(r.dispatcher.normal),
		PendingFailure: len(r.dispatcher.failure) + len(r.dispatcher.emergencyFailure),
		NormalDropped:  r.dispatcher.normalDropped.Load(),
		FailureDropped: r.dispatcher.failureDropped.Load(),
		WriterErrors:   r.dispatcher.writerErrors.Load(),
		PendingBytes: r.dispatcher.pendingNormalBytes.Load() +
			r.dispatcher.pendingFailureBytes.Load() +
			r.dispatcher.pendingEmergencyFailureBytes.Load(),
	}
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
		value := attr.Value.Resolve()
		if attr.Key == "error" {
			if err, ok := value.Any().(error); ok {
				recordedError = err
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
