// Producer is the in-process F3 audit write path (去跨进程战役第四刀). It
// replaces the retired loopback HMAC input server as the chain audit sink and
// mirrors the operationlog.Producer contract: fire-and-forget capture that
// never fails the gateway request. Per record it renews the shared owner
// lease, persists through the store (validation + normalization live inside
// store.Persist exactly as for the retired HTTP writes) and appends the
// hot-search mirror for non-ignored writes.
//
// Concurrency contract (2026-09-18 hardening): records flow through a bounded
// queue drained by a fixed worker pool, so a slow store cannot pile up one
// goroutine (and its captured payload) per audit record on the request path.
// A full queue drops the record with a warn carrying the running drop total —
// capture stays fire-and-forget and never blocks the business request.
//
// Byte budget (W14, 2026-10-22): a single AuditLogInput can carry multi-MB
// request/response bodies, so the item count cap alone allowed a theoretical
// tens-of-GB residency under a slow store. Queue admission now also accounts
// estimated payload bytes against producerQueueMaxBytes; over-budget and
// single-oversize captures drop with the same fire-and-forget semantics.
// Depth/bytes gauges (QueueDepth/QueueBytes) expose the residency for
// scraping.
//
// Deferred decode (DD-D4, 2026-10-31): the chain bridge delivers captures as
// raw structs and the JSON type bridge (Marshal→Unmarshal, including the
// body base64 wire form) runs inside the bounded worker pool — the request
// path no longer pays a full Marshal+base64 pass（架构总览「大审计的哈希、
// 正文降级、base64 和 JSON 编码由有界后台 goroutine 完成，请求处理路径只做
// 小记录内联编码」）.
//
// Terminal-only writes (F3 终态唯一写入, 2026-11-11): requests never persist
// an in_progress envelope row any more — the only store write is the single
// finalized terminal row (success or failure) with its attempts/payloads in
// the same transaction. Duplicate terminal inputs for the same id lose to the
// first persisted row (first-wins, ON CONFLICT DO NOTHING →
// PersistResult.Ignored). The former in_progress envelope + retraction
// machinery (W13/DD-D1~DD6) is fully retired.
package auditlog

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// producerQueueCapacity bounds the number of in-flight audit inputs; the
	// store is the bottleneck (pooled PG), so the queue
	// absorbs bursts instead of unbounded goroutine growth.
	producerQueueCapacity = 4096
	// producerWorkers caps concurrent store writers; renewal + persist +
	// hot-search run sequentially per record inside one worker.
	producerWorkers = 4

	// Estimation bases: small fields (ids, statuses, timestamps, small strings)
	// roll into fixed per-record/per-attempt envelopes; the dominant payload
	// bodies and long-tail error text count exactly.
	producerRecordBaseBytes  = 512
	producerAttemptBaseBytes = 256
	producerPayloadEnvelope  = 128
)

// producerQueueMaxBytes bounds the estimated byte residency of the audit
// pipeline (queued + in-flight persistence; bodies dominate, see
// estimateAuditInputBytes). 256MB keeps the queue a buffer, not a retention
// surface, even with full-capture bodies. Var only so package tests can pin a
// small budget; production must keep the default.
var producerQueueMaxBytes = int64(256) * 1024 * 1024

// queuedAudit pairs the input with its estimated byte size so the worker can
// release the budget exactly at dequeue time. deferred 非空表示这是一条未解码
// 采集（DD-D4）：worker 协程内执行 JSON 类型桥后再持久化（deferred 优先于
// input 判定）。
type queuedAudit struct {
	input    AuditLogInput
	bytes    int
	deferred *deferredCapture
}

// deferredCapture 描述一条在 worker 协程内才解码的审计采集：Decode 在有界
// 后台 goroutine 内执行 JSON 类型桥（含 body base64 编码）；队列记账由
// queuedAudit.bytes 承载（入队前按 EstimateDeferredAuditBytes 口径估算）。
type deferredCapture struct {
	traceID    string
	auditLogID string
	decode     func() (AuditLogInput, error)
}

// DeferredCapture is the caller-facing description of one deferred audit
// capture (CaptureDeferred). TraceID / AuditLogID 用于诊断日志。
type DeferredCapture struct {
	Estimate   int
	TraceID    string
	AuditLogID string
	Decode     func() (AuditLogInput, error)
}

type Producer struct {
	store Store
	lease LeaseSource
	cfg   Config
	log   producerLogger

	queue chan queuedAudit
	start sync.Once
	// dropped counts captures dropped by a full queue or exhausted byte
	// budget (fire-and-forget semantics, see Capture).
	dropped    atomic.Int64
	queueBytes atomic.Int64
	// lastLeaseRenewAt 是 per-record 续租节流点（UnixMilli）：OwnerLease/3
	// 窗口内只允许一个 worker 真正续租，其余记录直接进入 Persist。审计开启
	// 时每条记录少一次独立 PG UPDATE；lease keeper 仍按 TTL/3 兜底续租。
	lastLeaseRenewAt atomic.Int64
	// persistDuration is the optional push hook for the per-record
	// persistence latency histogram (result "ok" | "error"); nil keeps the
	// producer silent.
	persistDuration func(result string, durationMS int64)
}

type producerLogger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// LeaseSource exposes the currently held owner lease. The LeaseKeeper
// implements it; producers read the lease per record so every write fences
// with the live fence token (reacquire rotates the token when the supervisor
// revives a lost keeper — a frozen snapshot would fence every subsequent
// write until a process restart).
type LeaseSource interface {
	Lease() OwnerLease
}

// fixedLeaseSource pins one lease value for callers without a keeper (tests,
// static compositions).
type fixedLeaseSource OwnerLease

func (s fixedLeaseSource) Lease() OwnerLease { return OwnerLease(s) }

// StaticLease pins one lease value behind the LeaseSource interface for
// callers without a keeper (tests, static compositions).
//
// 预留导出 API：当前全仓无生产调用方，仅供静态组合/测试场景使用。
func StaticLease(lease OwnerLease) LeaseSource { return fixedLeaseSource(lease) }

func (p *Producer) warn(msg string, args ...any) {
	if p.log != nil {
		p.log.Warn(msg, args...)
	}
}

// NewProducer binds the producer to the shared lease source (the process-wide
// LeaseKeeper owned by main, or fixedLeaseSource for static compositions)
// and the persistence config. The renewal lifecycle stays with the keeper;
// the producer only extends the same lease per record, mirroring the
// operationlog.Producer contract.
func NewProducer(store Store, lease LeaseSource, cfg Config, log producerLogger) *Producer {
	return &Producer{
		store: store,
		lease: lease,
		cfg:   cfg,
		log:   log,
		queue: make(chan queuedAudit, producerQueueCapacity),
	}
}

// SetPersistDurationObserver registers the per-record persistence latency
// hook (result "ok" | "error"). Startup wiring only; nil (the zero state)
// keeps the producer silent.
func (p *Producer) SetPersistDurationObserver(observer func(result string, durationMS int64)) {
	if p == nil {
		return
	}
	p.persistDuration = observer
}

// estimateAuditInputBytes approximates the queue residency of one input
// without serializing it: payload bodies count exactly (they dominate), long
// error/URL/model text counts exactly, and the remaining small fields roll
// into fixed per-record/per-attempt envelopes. The estimate mirrors the
// publicapilogs queue-budget accounting intent (bounded admission, not
// retention).
func estimateAuditInputBytes(input AuditLogInput) int {
	total := producerRecordBaseBytes
	total += len(input.ErrorMessage) + len(input.ErrorCode) + len(input.ErrorPhase)
	total += len(input.TraceID) + len(input.UserAgent) + len(input.Path) + len(input.QueryString)
	for index := range input.Attempts {
		attempt := &input.Attempts[index]
		total += producerAttemptBaseBytes
		total += len(attempt.ErrorMessage) + len(attempt.ErrorCode) + len(attempt.UpstreamURL) + len(attempt.UpstreamModel)
	}
	for index := range input.Payloads {
		payload := &input.Payloads[index]
		total += producerPayloadEnvelope + len(payload.Body.Bytes)
	}
	return total
}

// DeferredAuditEstimate 汇总一条未解码采集的原始尺寸，供
// EstimateDeferredAuditBytes 计算队列驻留估算（cmd 类型桥投影，DD-D4）。
// 文本字段按精确字节计；BodyBytes 是 payload 原始 body 字节合计，估算侧
// 统一按 base64 wire 上界（×4/3）放大。
type DeferredAuditEstimate struct {
	ErrorTextBytes   int // record 级 error/trace/ua/path/query 文本字节
	AttemptCount     int
	AttemptTextBytes int // 尝试级 error/url/model 文本字节合计
	PayloadCount     int
	BodyBytes        int // payload 原始 body 字节合计
}

// EstimateDeferredAuditBytes 以与 estimateAuditInputBytes 相同的固定信封
// 口径估算一条未解码采集的队列驻留：文本精确、body 按 base64 wire 上界
// （base64.StdEncoding.EncodedLen 的线性项 n*4/3 向上取整）放大。估算只
// 服务准入与预算记账，不参与持久化。
func EstimateDeferredAuditBytes(estimate DeferredAuditEstimate) int {
	total := producerRecordBaseBytes + estimate.ErrorTextBytes
	total += producerAttemptBaseBytes*estimate.AttemptCount + estimate.AttemptTextBytes
	total += producerPayloadEnvelope * estimate.PayloadCount
	total += (estimate.BodyBytes + 2) / 3 * 4
	return total
}

// Capture persists one audit entry asynchronously (fire-and-forget). A lost
// or unrenewable owner lease drops the entry with a warn; persistence errors
// are logged and swallowed — audit capture never fails the business request
// (the hot-search mirror can be rebuilt from the canonical store, so its
// failure is likewise a warn, not an outage). When the bounded queue is full
// or the byte budget is exhausted the entry is dropped with a warn instead of
// blocking the caller.
func (p *Producer) Capture(input AuditLogInput) {
	if p == nil || p.store == nil {
		return
	}
	p.start.Do(p.startWorkers)
	bytes := estimateAuditInputBytes(input)
	if bytes > int(producerQueueMaxBytes) {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集单条超出队列字节预算，丢弃本条采集", "droppedTotal", dropped, "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if int64(len(p.queue))+1 > producerQueueCapacity || p.queueBytes.Load()+int64(bytes) > producerQueueMaxBytes {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	select {
	case p.queue <- queuedAudit{input: input, bytes: bytes}:
		p.queueBytes.Add(int64(bytes))
	default:
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}

// CaptureDeferred enqueues one not-yet-decoded audit capture (DD-D4): the
// JSON type bridge (Marshal→Unmarshal, body base64 wire form included) runs
// inside the bounded worker pool instead of on the request path. Admission
// uses the caller-supplied Estimate（EstimateDeferredAuditBytes 口径）记账，
// 准入与结算释放语义与 Capture 相同；Decode 失败丢弃本条采集并记诊断日志。
func (p *Producer) CaptureDeferred(capture DeferredCapture) {
	if p == nil || p.store == nil || capture.Decode == nil {
		return
	}
	p.start.Do(p.startWorkers)
	bytes := capture.Estimate
	if bytes > int(producerQueueMaxBytes) {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集单条超出队列字节预算，丢弃本条采集", "droppedTotal", dropped, "bytes", bytes, "traceID", capture.TraceID, "auditLogID", capture.AuditLogID)
		return
	}
	if int64(len(p.queue))+1 > producerQueueCapacity || p.queueBytes.Load()+int64(bytes) > producerQueueMaxBytes {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", capture.TraceID, "auditLogID", capture.AuditLogID)
		return
	}
	item := &deferredCapture{
		traceID:    capture.TraceID,
		auditLogID: capture.AuditLogID,
		decode:     capture.Decode,
	}
	select {
	case p.queue <- queuedAudit{deferred: item, bytes: bytes}:
		p.queueBytes.Add(int64(bytes))
	default:
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", capture.TraceID, "auditLogID", capture.AuditLogID)
	}
}

func (p *Producer) startWorkers() {
	for i := 0; i < producerWorkers; i++ {
		go p.workerLoop()
	}
}

// DroppedTotal exposes the running count of captures dropped because the
// bounded queue was full (Prometheus scrape seam).
func (p *Producer) DroppedTotal() int64 {
	if p == nil {
		return 0
	}
	return p.dropped.Load()
}

// QueueDepth exposes the current number of queued captures (Prometheus gauge
// seam).
func (p *Producer) QueueDepth() int {
	if p == nil {
		return 0
	}
	return len(p.queue)
}

// QueueBytes exposes the estimated byte residency of queued captures
// (Prometheus gauge seam).
func (p *Producer) QueueBytes() int64 {
	if p == nil {
		return 0
	}
	return p.queueBytes.Load()
}

// workerLoop 消费采集条目（同步采集 + 延迟采集）：慢存储把并发压在固定
// worker 池上，消费与字节预算释放一一对应。
func (p *Producer) workerLoop() {
	for item := range p.queue {
		switch {
		case item.deferred != nil:
			p.persistDeferred(item.deferred)
			// Release the byte budget only after decode + persistence
			// settles so the gauge covers queued + in-flight residency
			// (exactly the residency W14 bounds); a slow store therefore
			// consumes the budget and triggers drops instead of letting
			// residency grow without bound.
			p.queueBytes.Add(-int64(item.bytes))
		default:
			p.persistOne(item.input)
			p.queueBytes.Add(-int64(item.bytes))
		}
	}
}

// persistDeferred decodes and persists one deferred capture inside the worker
// goroutine (DD-D4)：JSON 编码（含 body base64）与后续持久化同属有界后台面。
// 解码失败丢弃本条采集并记诊断日志——不静默吞错、不影响 worker 继续消费。
func (p *Producer) persistDeferred(item *deferredCapture) {
	input, err := item.decode()
	if err != nil {
		p.warn("F3 审计采集 JSON 编码失败，丢弃本条采集", "error", err, "traceID", item.traceID, "auditLogID", item.auditLogID)
		return
	}
	p.persistOne(input)
}

// persistOne keeps the retired write sequence (renew → persist → hot search)
// with the same per-record degradation as the original per-record goroutine,
// plus panic isolation: one broken record must not take down the worker pool
// or the process. Failure and panic arms keep the original error (warn log +
// latency metric) and drop the record — no shell row is ever left behind
// because requests no longer write an in_progress envelope up front.
func (p *Producer) persistOne(input AuditLogInput) {
	startedAt := time.Now()
	outcome := "ok"
	defer func() {
		if recovered := recover(); recovered != nil {
			p.warn("F3 审计采集持久化 panic 已隔离", "panic", recovered, "traceID", input.TraceID, "auditLogID", input.ID)
			p.observePersistDuration("panic", startedAt)
			return
		}
		p.observePersistDuration(outcome, startedAt)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease := p.lease.Lease()
	if err := p.renewLeaseIfDue(lease); err != nil {
		outcome = "error"
		p.warn("F3 owner lease renewal failed; dropping audit capture", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	persistResult, err := p.store.Persist(ctx, lease, input)
	if err != nil {
		outcome = "error"
		p.warn("F3 审计采集持久化失败", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if persistResult.Ignored {
		p.warn("F3 重复或迟到的审计输入被忽略，跳过本次持久化",
			"event", "audit_input_ignored",
			"traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if _, err := p.store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err != nil {
		p.warn("F3 audit hot-search append failed", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}

// renewLeaseIfDue 按 OwnerLease/3 节流 per-record 续租；零 TTL 跳过（与
// 旧行为一致，避免把 lease_until 写成当前时刻自毁 fence）。续租失败时清空
// 节流点，下一条记录立即重试，不把失败窗口拖到 TTL/3 之后。
func (p *Producer) renewLeaseIfDue(lease OwnerLease) error {
	if p.cfg.OwnerLease <= 0 {
		return nil
	}
	interval := p.cfg.OwnerLease / 3
	if interval <= 0 {
		interval = p.cfg.OwnerLease
	}
	now := time.Now()
	last := p.lastLeaseRenewAt.Load()
	if last > 0 && now.UnixMilli()-last < interval.Milliseconds() {
		return nil
	}
	if !p.lastLeaseRenewAt.CompareAndSwap(last, now.UnixMilli()) {
		return nil
	}
	renewCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	renewed, err := p.store.RenewOwnerLease(renewCtx, lease, p.cfg.OwnerLease)
	if err != nil {
		p.lastLeaseRenewAt.Store(0)
		return err
	}
	if !renewed {
		p.lastLeaseRenewAt.Store(0)
		return ErrOwnerLeaseLost
	}
	return nil
}

// observePersistDuration reports the per-record persistence latency through
// the optional observer (result "ok" | "error" | "panic").
func (p *Producer) observePersistDuration(result string, startedAt time.Time) {
	if p == nil || p.persistDuration == nil {
		return
	}
	p.persistDuration(result, time.Since(startedAt).Milliseconds())
}
