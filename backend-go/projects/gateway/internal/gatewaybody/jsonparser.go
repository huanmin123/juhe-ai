package gatewaybody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Bounded in-process JSON parsing, mirroring request/json-parser.ts plus
// request/json-worker.ts.
//
// Approved architecture adaptation: Node runs large parses and metadata scans
// in worker_threads. The Go gateway executes the same work in a bounded
// goroutine pool with the same admission budget (queue length, active/total
// bytes), the same default timeouts and the same error copy. A job that times
// out or is canceled abandons its result like the Node worker restart; the
// in-flight goroutine finishes in the background and its result is dropped,
// which is observable-equivalent because capacity accounting is released at
// abandonment time.

const (
	// DefaultJSONWorkerJobTimeout mirrors the 30000ms default timeout of
	// parseGatewayJsonBodyInWorker / extractGatewayJsonBodyMetadataInWorker.
	DefaultJSONWorkerJobTimeout = 30 * time.Second
	// GatewayRequestJSONMaterializationTimeout mirrors
	// gatewayRequestJsonMaterializationTimeoutMs.
	GatewayRequestJSONMaterializationTimeout = 30 * time.Second
	// GatewayJSONWorkerSlowQueueWait mirrors gatewayJsonWorkerSlowQueueWaitMs.
	GatewayJSONWorkerSlowQueueWait = 500 * time.Millisecond
	// GatewayJSONWorkerSlowDuration mirrors gatewayJsonWorkerSlowDurationMs.
	GatewayJSONWorkerSlowDuration = 1000 * time.Millisecond

	gatewayParsedBodyWorkerMemoryMultiplier = 4 // codex parsed-body jobs only
	// GatewayJSONWorkerJobFixedBytes mirrors gatewayJsonWorkerJobFixedBytes.
	GatewayJSONWorkerJobFixedBytes = 512
	// GatewayJSONWorkerMaxQueuedJobs mirrors gatewayJsonWorkerMaxQueuedJobs.
	GatewayJSONWorkerMaxQueuedJobs = 128
	// GatewayJSONWorkerMaxActiveBytes mirrors gatewayJsonWorkerMaxActiveBytes.
	GatewayJSONWorkerMaxActiveBytes = 128 * 1024 * 1024
	// GatewayJSONWorkerMaxTotalBytes mirrors gatewayJsonWorkerMaxTotalBytes.
	GatewayJSONWorkerMaxTotalBytes = 256*1024*1024 + GatewayJSONWorkerJobFixedBytes
)

// Worker failure classes with the exact Node error copy.
var (
	// ErrQueueFull mirrors GatewayJsonWorkerQueueFullError.
	ErrQueueFull = errors.New("网关 JSON worker 队列已满，请稍后重试")
	// ErrCanceled mirrors GatewayJsonWorkerCanceledError.
	ErrCanceled = errors.New("网关 JSON worker 任务已取消")
	// ErrStopped mirrors the stopGatewayJsonParseWorker rejection copy.
	ErrStopped = errors.New("网关 JSON worker 已关闭")
)

// JSONWorkerTimeoutError mirrors GatewayJsonWorkerTimeoutError.
type JSONWorkerTimeoutError struct {
	TimeoutMS int
}

func (e *JSONWorkerTimeoutError) Error() string {
	return fmt.Sprintf("网关 JSON worker %dms 超时", e.TimeoutMS)
}

// JSONWorkerMaterializationTimeoutError mirrors the
// awaitGatewayRequestJsonMaterialization timeout copy, which differs from the
// worker timeout copy in Node ("任务超时（n ms）" vs "nms 超时").
type JSONWorkerMaterializationTimeoutError struct {
	TimeoutMS int
}

func (e *JSONWorkerMaterializationTimeoutError) Error() string {
	return fmt.Sprintf("网关 JSON worker 任务超时（%dms）", e.TimeoutMS)
}

// InvalidJSONError marks JSON syntax failures. Node surfaces V8 SyntaxError
// objects whose never-user-facing message is engine specific; gatewaybody
// carries the Node fallback copy ("网关 JSON 请求体必须是有效 JSON") as the
// stable message and keeps the engine detail for logs.
type InvalidJSONError struct {
	Detail string
}

func (e *InvalidJSONError) Error() string {
	return "网关 JSON 请求体必须是有效 JSON"
}

// IsQueueFullError mirrors isGatewayJsonWorkerQueueFullError.
func IsQueueFullError(err error) bool { return errors.Is(err, ErrQueueFull) }

// IsCanceledError mirrors isGatewayJsonWorkerCanceledError.
func IsCanceledError(err error) bool { return errors.Is(err, ErrCanceled) }

// IsTimeoutError mirrors isGatewayJsonWorkerTimeoutError.
func IsTimeoutError(err error) bool {
	var timeout *JSONWorkerTimeoutError
	return errors.As(err, &timeout)
}

// IsInvalidJSONError mirrors isGatewayJsonWorkerInvalidJsonError (the
// SyntaxError-name check).
func IsInvalidJSONError(err error) bool {
	var invalid *InvalidJSONError
	return errors.As(err, &invalid)
}

type jsonWorkerJobKind int

const (
	jsonWorkerJobKindParse jsonWorkerJobKind = iota
	jsonWorkerJobKindMetadata
)

// JSONParserOptions configures the bounded parse pool. Zero values fall back
// to the Node constants.
type JSONParserOptions struct {
	// PoolSize mirrors gatewayJsonWorkerPoolSize: max(1, min(4, parallelism)).
	PoolSize int
	// MaxQueuedJobs mirrors gatewayJsonWorkerMaxQueuedJobs.
	MaxQueuedJobs int
	// MaxActiveBytes mirrors gatewayJsonWorkerMaxActiveBytes.
	MaxActiveBytes int
	// MaxTotalBytes mirrors gatewayJsonWorkerMaxTotalBytes.
	MaxTotalBytes int
	// JobFixedBytes mirrors gatewayJsonWorkerJobFixedBytes.
	JobFixedBytes int
	// Logger receives job completion/failure events (nil = discard).
	Logger Logger
}

// JSONParser is the bounded JSON execution pool. One instance serves the
// whole process; it is safe for concurrent use.
type JSONParser struct {
	opts JSONParserOptions

	mu          sync.Mutex
	cond        *sync.Cond
	queue       []*jsonWorkerJob
	queuedBytes int
	activeBytes int
	busyWorkers int
	stopped     bool
	nextJobID   int

	// Mock seams for tests (nil in production).
	parseFunc func(ctx context.Context, raw []byte) (any, error)
	scanFunc  func(raw []byte) JSONBodyMetadata
}

type jsonWorkerJob struct {
	id               int
	kind             jsonWorkerJobKind
	raw              []byte
	payloadBytes     int
	ctx              context.Context
	enqueuedAt       time.Time
	startedAt        time.Time
	done             chan struct{}
	abandoned        atomic.Bool
	result           jsonWorkerResult
	resultSet        atomic.Bool
	capacityReleased bool
	// Hook snapshot taken under the parser mutex at enqueue time so worker
	// goroutines never read the mutable parser fields.
	parseHook func(ctx context.Context, raw []byte) (any, error)
	scanHook  func(raw []byte) JSONBodyMetadata
}

type jsonWorkerResult struct {
	value    any
	metadata JSONBodyMetadata
	err      error
}

// NewJSONParser creates the pool and starts its workers.
func NewJSONParser(options JSONParserOptions) *JSONParser {
	if options.PoolSize <= 0 {
		poolSize := runtime.GOMAXPROCS(0)
		if poolSize > 4 {
			poolSize = 4
		}
		if poolSize < 1 {
			poolSize = 1
		}
		options.PoolSize = poolSize
	}
	if options.MaxQueuedJobs <= 0 {
		options.MaxQueuedJobs = GatewayJSONWorkerMaxQueuedJobs
	}
	if options.MaxActiveBytes <= 0 {
		options.MaxActiveBytes = GatewayJSONWorkerMaxActiveBytes
	}
	if options.MaxTotalBytes <= 0 {
		options.MaxTotalBytes = GatewayJSONWorkerMaxTotalBytes
	}
	if options.JobFixedBytes <= 0 {
		options.JobFixedBytes = GatewayJSONWorkerJobFixedBytes
	}
	parser := &JSONParser{opts: options}
	parser.cond = sync.NewCond(&parser.mu)
	for i := 0; i < options.PoolSize; i++ {
		go parser.worker()
	}
	return parser
}

// ParseJSONBody mirrors parseGatewayJsonBodyInWorker: JSON.parse semantics
// (including top-level primitives and V8 number extremes) executed on the
// bounded pool with the Node timeout contract.
func (p *JSONParser) ParseJSONBody(ctx context.Context, raw []byte, timeout time.Duration) (any, error) {
	result, err := p.executeJob(ctx, jsonWorkerJobKindParse, raw, timeout)
	if err != nil {
		return nil, err
	}
	return result.value, nil
}

// ExtractJSONBodyMetadataAsync mirrors extractGatewayJsonBodyMetadataInWorker.
func (p *JSONParser) ExtractJSONBodyMetadataAsync(ctx context.Context, raw []byte, timeout time.Duration) (JSONBodyMetadata, error) {
	result, err := p.executeJob(ctx, jsonWorkerJobKindMetadata, raw, timeout)
	if err != nil {
		return JSONBodyMetadata{}, err
	}
	return result.metadata, nil
}

func (p *JSONParser) executeJob(ctx context.Context, kind jsonWorkerJobKind, raw []byte, timeout time.Duration) (jsonWorkerResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return jsonWorkerResult{}, fmt.Errorf("%w", ErrCanceled)
	}
	if timeout <= 0 {
		timeout = DefaultJSONWorkerJobTimeout
	}
	job, err := p.enqueue(ctx, kind, raw)
	if err != nil {
		return jsonWorkerResult{}, err
	}
	if result, done := p.peekResult(job); done {
		return result, result.err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.done:
		result := p.takeResult(job)
		return result, result.err
	case <-timer.C:
		p.abandon(job)
		return jsonWorkerResult{}, &JSONWorkerTimeoutError{TimeoutMS: int(timeout / time.Millisecond)}
	case <-ctx.Done():
		p.abandon(job)
		return jsonWorkerResult{}, fmt.Errorf("%w", ErrCanceled)
	}
}

func (p *JSONParser) enqueue(ctx context.Context, kind jsonWorkerJobKind, raw []byte) (*jsonWorkerJob, error) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil, ErrStopped
	}
	jobBytes := p.jobBytes(len(raw))
	if len(p.queue) >= p.opts.MaxQueuedJobs ||
		p.queuedBytes+p.activeBytes+jobBytes > p.opts.MaxTotalBytes {
		p.mu.Unlock()
		return nil, ErrQueueFull
	}
	p.nextJobID++
	job := &jsonWorkerJob{
		id:           p.nextJobID,
		kind:         kind,
		raw:          raw,
		payloadBytes: len(raw),
		ctx:          ctx,
		enqueuedAt:   time.Now(),
		done:         make(chan struct{}),
		parseHook:    p.parseFunc,
		scanHook:     p.scanFunc,
	}
	p.queue = append(p.queue, job)
	p.queuedBytes += jobBytes
	p.mu.Unlock()
	p.cond.Signal()
	return job, nil
}

func (p *JSONParser) jobBytes(payloadBytes int) int {
	return payloadBytes + p.opts.JobFixedBytes
}

func (p *JSONParser) worker() {
	for {
		p.mu.Lock()
		var job *jsonWorkerJob
		for {
			if p.stopped {
				p.mu.Unlock()
				return
			}
			if index := p.findStartableLocked(); index >= 0 {
				job = p.queue[index]
				p.queue = append(p.queue[:index], p.queue[index+1:]...)
				p.queuedBytes -= p.jobBytes(job.payloadBytes)
				p.activeBytes += p.jobBytes(job.payloadBytes)
				p.busyWorkers++
				break
			}
			p.cond.Wait()
		}
		p.mu.Unlock()

		job.startedAt = time.Now()
		result := p.run(job)

		p.mu.Lock()
		jobBytes := p.jobBytes(job.payloadBytes)
		if !job.capacityReleased {
			p.activeBytes -= jobBytes
			job.capacityReleased = true
		}
		p.busyWorkers--
		p.mu.Unlock()

		if job.abandoned.Load() {
			continue
		}
		if result.err != nil && !IsInvalidJSONError(result.err) && !IsCanceledError(result.err) {
			p.logJobFailure(job, result.err)
		} else {
			p.logJobCompletion(job, result.err == nil)
		}
		job.result = result
		job.resultSet.Store(true)
		close(job.done)
	}
}

func (p *JSONParser) findStartableLocked() int {
	// canStartGatewayJsonWorkerJob: the first queued job starts when nothing
	// is active or it still fits the active byte budget.
	for index, job := range p.queue {
		jobBytes := p.jobBytes(job.payloadBytes)
		if p.activeBytes == 0 || p.activeBytes+jobBytes <= p.opts.MaxActiveBytes {
			return index
		}
	}
	return -1
}

func (p *JSONParser) run(job *jsonWorkerJob) jsonWorkerResult {
	if job.ctx.Err() != nil {
		return jsonWorkerResult{err: fmt.Errorf("%w", ErrCanceled)}
	}
	switch job.kind {
	case jsonWorkerJobKindMetadata:
		if job.scanHook != nil {
			return jsonWorkerResult{metadata: job.scanHook(job.raw)}
		}
		return jsonWorkerResult{metadata: ExtractJSONBodyMetadata(job.raw)}
	default:
		if job.parseHook != nil {
			value, err := job.parseHook(job.ctx, job.raw)
			return jsonWorkerResult{value: value, err: err}
		}
		value, err := ParseJSONValue(job.raw)
		return jsonWorkerResult{value: value, err: err}
	}
}

// abandon mirrors failJob/cancelJob capacity release: the job's budget is
// freed immediately, the running goroutine (if any) later drops its result.
func (p *JSONParser) abandon(job *jsonWorkerJob) {
	p.mu.Lock()
	jobBytes := p.jobBytes(job.payloadBytes)
	for index, queued := range p.queue {
		if queued == job {
			p.queue = append(p.queue[:index], p.queue[index+1:]...)
			p.queuedBytes -= jobBytes
			p.mu.Unlock()
			job.abandoned.Store(true)
			return
		}
	}
	if !job.capacityReleased {
		p.activeBytes -= jobBytes
		job.capacityReleased = true
	}
	p.mu.Unlock()
	job.abandoned.Store(true)
}

func (p *JSONParser) peekResult(job *jsonWorkerJob) (jsonWorkerResult, bool) {
	if job.resultSet.Load() {
		return p.takeResult(job), true
	}
	return jsonWorkerResult{}, false
}

func (p *JSONParser) takeResult(job *jsonWorkerJob) jsonWorkerResult {
	// done is only closed after result is stored, so resultSet implies result.
	return job.result
}

// Stop mirrors stopGatewayJsonParseWorker: queued jobs are rejected with the
// Node shutdown copy. Active jobs finish in the background; their waiters get
// the stopped error if the job had not delivered a result yet.
func (p *JSONParser) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	queued := p.queue
	p.queue = nil
	p.queuedBytes = 0
	p.cond.Broadcast()
	p.mu.Unlock()
	for _, job := range queued {
		if job.abandoned.Load() {
			continue
		}
		job.abandoned.Store(true)
		job.result = jsonWorkerResult{err: ErrStopped}
		job.resultSet.Store(true)
		close(job.done)
	}
}

func (p *JSONParser) logJobCompletion(job *jsonWorkerJob, success bool) {
	logger := p.opts.Logger
	if logger == nil {
		return
	}
	now := time.Now()
	queuedWait := job.startedAt.Sub(job.enqueuedAt)
	fields := map[string]any{
		"event":        "gateway_json_worker_job_completed",
		"jobId":        fmt.Sprintf("gateway-json-worker:%d", job.id),
		"jobType":      jobKindName(job.kind),
		"rawBodyBytes": job.payloadBytes,
		"queuedWaitMs": queuedWait.Milliseconds(),
		"totalMs":      now.Sub(job.enqueuedAt).Milliseconds(),
		"queuedJobs":   len(p.queue),
	}
	if success {
		if queuedWait >= GatewayJSONWorkerSlowQueueWait || now.Sub(job.startedAt) >= GatewayJSONWorkerSlowDuration {
			logger.Warn("网关 JSON worker 任务耗时偏高", fields)
			return
		}
		logger.Info("网关 JSON worker 任务完成", fields)
	}
}

func (p *JSONParser) logJobFailure(job *jsonWorkerJob, err error) {
	logger := p.opts.Logger
	if logger == nil {
		return
	}
	logger.Error("网关 JSON worker 失败", map[string]any{
		"event":        "gateway_json_parse_worker_failed",
		"failureClass": "infrastructure",
		"jobId":        fmt.Sprintf("gateway-json-worker:%d", job.id),
		"jobType":      jobKindName(job.kind),
		"rawBodyBytes": job.payloadBytes,
		"totalMs":      time.Since(job.enqueuedAt).Milliseconds(),
		"error":        err.Error(),
	})
}

func jobKindName(kind jsonWorkerJobKind) string {
	if kind == jsonWorkerJobKindMetadata {
		return "extract_json_body_metadata"
	}
	return "parse_json_body"
}

// ParseJSONValue parses a complete JSON document with JavaScript
// JSON.parse-compatible semantics: any top-level value, trailing content is
// rejected, and numbers keep float64 behavior including +/-Infinity overflow
// (V8 returns Infinity where Go's plain decoder errors).
func ParseJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &InvalidJSONError{Detail: err.Error()}
	}
	// JSON.parse rejects trailing non-whitespace content.
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, &InvalidJSONError{Detail: "unexpected trailing content"}
		}
		return nil, &InvalidJSONError{Detail: err.Error()}
	}
	converted, err := convertJSONNumbers(value)
	if err != nil {
		return nil, &InvalidJSONError{Detail: err.Error()}
	}
	return converted, nil
}

func convertJSONNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			converted, err := convertJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			typed[key] = converted
		}
		return typed, nil
	case []any:
		for index, item := range typed {
			converted, err := convertJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			typed[index] = converted
		}
		return typed, nil
	case json.Number:
		converted, err := convertJSONNumber(string(typed))
		if err != nil {
			return nil, err
		}
		return converted, nil
	default:
		return value, nil
	}
}

func convertJSONNumber(text string) (float64, error) {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && numErr.Err == strconv.ErrRange {
			// V8 JSON.parse keeps +/-Infinity / 0 for out-of-range literals
			// (ParseFloat already stores the extreme value).
			return value, nil
		}
		return 0, err
	}
	return value, nil
}
