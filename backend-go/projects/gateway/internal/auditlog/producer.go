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
// Envelope/retract total order (DD-D1, 2026-10-31): in_progress envelope ids
// register in a bounded in-flight set at admission and unregister exactly
// when their persist settles (success/failure/panic all reach the settle
// defer; a deferred capture whose Decode fails settles at the decode-failure
// arm itself before persistOne — DD-RC-A1, 2026-10-31); retract commands
// observed against an in-flight id are deferred until settle — a slow-store
// window can no longer turn「撤回 DELETE 无行 no-op → 信封随后提交」into a
// permanent in_progress shell（W13 第四产生面）.
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
// release the budget exactly at dequeue time. retractionID 非空表示这是一条
// in_progress 信封撤回命令（W13）：不占字节预算，直接删除幽灵信封行。
// deferred 非空表示这是一条未解码采集（DD-D4）：worker 协程内执行 JSON 类型
// 桥后再持久化，三个字段互斥（deferred 优先于 input 判定）。
type queuedAudit struct {
	input        AuditLogInput
	bytes        int
	retractionID string
	deferred     *deferredCapture
}

// deferredCapture 描述一条在 worker 协程内才解码的审计采集：Decode 在有界
// 后台 goroutine 内执行 JSON 类型桥（含 body base64 编码）；队列记账由
// queuedAudit.bytes 承载（入队前按 EstimateDeferredAuditBytes 口径估算）。
// envelopeID 是 CaptureDeferred 入队前登记的在途信封 id（DD-D1）：Decode
// 失败臂在 persistOne 之前返回、settle defer 不可达，该臂的注销由本字段
// 承载（DD-RC-A1），保证在途集合仍以队列准入数为上界。
type deferredCapture struct {
	traceID    string
	auditLogID string
	envelopeID string
	decode     func() (AuditLogInput, error)
}

// DeferredCapture is the caller-facing description of one deferred audit
// capture (CaptureDeferred). EnvelopeID 非空表示这是 in_progress 信封采集
// （DD-D1）：在途集合在入队前登记、persist settle 时注销，撤回命令在 settle
// 前延迟生效。TraceID / AuditLogID 只用于诊断日志。
type DeferredCapture struct {
	Estimate   int
	EnvelopeID string
	TraceID    string
	AuditLogID string
	Decode     func() (AuditLogInput, error)
}

type Producer struct {
	store Store
	lease LeaseSource
	cfg   Config
	log   producerLogger

	queue      chan queuedAudit
	start      sync.Once
	dropped    atomic.Int64
	queueBytes atomic.Int64
	// persistDuration is the optional push hook for the per-record
	// persistence latency histogram (result "ok" | "error"); nil keeps the
	// producer silent.
	persistDuration func(result string, durationMS int64)

	// inFlightEnvelopes tracks in_progress envelope ids from admission until
	// their persist settles (DD-D1). 注册只发生在入队成功前、注销由 persistOne
	// 的 settle defer（成功/失败/panic 三臂都到达）与 persistDeferred 的
	// Decode 失败臂（DD-RC-A1：该臂在 persistOne 之前返回）保证，集合规模
	// 始终以队列准入数为上界，不会泄漏。
	envelopeMu        sync.Mutex
	inFlightEnvelopes map[string]struct{}
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
		store:             store,
		lease:             lease,
		cfg:               cfg,
		log:               log,
		queue:             make(chan queuedAudit, producerQueueCapacity),
		inFlightEnvelopes: map[string]struct{}{},
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

// registerInFlightEnvelope 在信封采集入队前登记在途 id（DD-D1）。登记先于
// 入队发生，保证 worker 不可能在登记前取走该条目。
func (p *Producer) registerInFlightEnvelope(auditLogID string) {
	if auditLogID == "" {
		return
	}
	p.envelopeMu.Lock()
	p.inFlightEnvelopes[auditLogID] = struct{}{}
	p.envelopeMu.Unlock()
}

// unregisterInFlightEnvelope 在采集 settle 后注销在途信封（DD-D1）。
// persistOne 的 settle defer 在成功/失败/panic 三臂都会到达；延迟采集的
// Decode 失败臂不经 persistOne，在返回前直接注销（DD-RC-A1）——settle
// 路径保证注销、集合不泄漏；未登记的 id 删除是 no-op。
func (p *Producer) unregisterInFlightEnvelope(auditLogID string) {
	if auditLogID == "" {
		return
	}
	p.envelopeMu.Lock()
	delete(p.inFlightEnvelopes, auditLogID)
	p.envelopeMu.Unlock()
}

// envelopeInFlight 返回 id 的信封 persist 是否尚未 settle。
func (p *Producer) envelopeInFlight(auditLogID string) bool {
	if auditLogID == "" {
		return false
	}
	p.envelopeMu.Lock()
	defer p.envelopeMu.Unlock()
	_, inFlight := p.inFlightEnvelopes[auditLogID]
	return inFlight
}

// Capture persists one audit entry asynchronously (fire-and-forget). A lost
// or unrenewable owner lease drops the entry with a warn; persistence errors
// are logged and swallowed — audit capture never fails the business request
// (the hot-search mirror can be rebuilt from the canonical store, so its
// failure is likewise a warn, not an outage). When the bounded queue is full
// or the byte budget is exhausted the entry is dropped with a warn instead of
// blocking the caller. in_progress 信封（DD-D1）在入队前登记在途集合，撤回
// 命令在本条 settle 前延迟生效。
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
	// DD-D1：登记必须先于入队（入队即对 worker 可见），入队失败（队列满）
	// 立即注销，集合不泄漏。只有 in_progress 信封参与在途集合。
	envelopeID := ""
	if input.LifecycleStatus == LifecycleInProgress {
		envelopeID = input.ID
		p.registerInFlightEnvelope(envelopeID)
	}
	if int64(len(p.queue))+1 > producerQueueCapacity || p.queueBytes.Load()+int64(bytes) > producerQueueMaxBytes {
		p.unregisterInFlightEnvelope(envelopeID)
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	select {
	case p.queue <- queuedAudit{input: input, bytes: bytes}:
		p.queueBytes.Add(int64(bytes))
	default:
		p.unregisterInFlightEnvelope(envelopeID)
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}

// CaptureDeferred enqueues one not-yet-decoded audit capture (DD-D4): the
// JSON type bridge (Marshal→Unmarshal, body base64 wire form included) runs
// inside the bounded worker pool instead of on the request path. Admission
// uses the caller-supplied Estimate（EstimateDeferredAuditBytes 口径）记账，
// 准入与结算释放语义与 Capture 相同；Decode 失败丢弃本条采集并记诊断日志，
// 入队时登记的信封 id（EnvelopeID）随本条 settle 注销（DD-RC-A1）——不派生
// 撤回，Decode 失败即输入损坏、信封行无从定位。信封类采集（EnvelopeID 非空）
// 与 Capture 同口径参与 DD-D1 在途集合。
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
	p.registerInFlightEnvelope(capture.EnvelopeID)
	if int64(len(p.queue))+1 > producerQueueCapacity || p.queueBytes.Load()+int64(bytes) > producerQueueMaxBytes {
		p.unregisterInFlightEnvelope(capture.EnvelopeID)
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", capture.TraceID, "auditLogID", capture.AuditLogID)
		return
	}
	item := &deferredCapture{
		traceID:    capture.TraceID,
		auditLogID: capture.AuditLogID,
		envelopeID: capture.EnvelopeID,
		decode:     capture.Decode,
	}
	select {
	case p.queue <- queuedAudit{deferred: item, bytes: bytes}:
		p.queueBytes.Add(int64(bytes))
	default:
		p.unregisterInFlightEnvelope(capture.EnvelopeID)
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

// Retract enqueues the retraction of one in_progress envelope (W13: a
// capture canceled after the envelope was written leaves a phantom shell
// unless the row is deleted). Fire-and-forget like Capture; the store delete
// is lifecycle-guarded, so unknown or already-finalized ids are no-ops.
func (p *Producer) Retract(auditLogID string) {
	if p == nil || p.store == nil || auditLogID == "" {
		return
	}
	p.start.Do(p.startWorkers)
	// W13（2026-10-29）：队列满的撤回命令原为单次非阻塞尝试即丢；Cancel 路径
	// 跑在请求协程上不能内联执行，改为最多 3 次入队尝试（间隔 100/200ms）。
	for attempt := 1; attempt <= 3; attempt++ {
		if p.tryEnqueueRetract(auditLogID) {
			return
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
	}
	p.warn("F3 审计采集队列已满，丢弃信封撤回命令", "auditLogID", auditLogID)
}

// tryEnqueueRetract 以非阻塞方式把撤回命令并入采集队列（免预算：撤回项
// bytes=0，不占 W14 字节预算），返回是否入队成功。
func (p *Producer) tryEnqueueRetract(auditLogID string) bool {
	select {
	case p.queue <- queuedAudit{retractionID: auditLogID}:
		return true
	default:
		return false
	}
}

// retractBestEffort 是 persistOne 失败分支的撤回入口（W13 残留②收口，
// 2026-10-28）：优先免预算入队；队列饱和时（饱和与存储故障同窗，正是该
// 路径丢命令的触发形态）在当前 worker 协程上内联执行撤回——lifecycle 守卫
// 对无行/终态行均为无害 no-op，最坏为本次结算增加一次有界（10s）DELETE，
// 确定性消除「失败分支派发的撤回命令被同一饱和队列丢弃」的空壳残留。
func (p *Producer) retractBestEffort(auditLogID string) {
	if p.tryEnqueueRetract(auditLogID) {
		return
	}
	p.warn("F3 审计采集队列饱和，信封撤回转 worker 内联执行", "auditLogID", auditLogID)
	p.retractOne(auditLogID)
}

func (p *Producer) workerLoop() {
	for item := range p.queue {
		switch {
		case item.retractionID != "":
			p.retractOne(item.retractionID)
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
// 解码失败丢弃本条采集并记诊断日志——不静默吞错、不影响 worker 继续消费；
// 返回前先注销入队时登记的在途信封（DD-RC-A1：该臂不经 persistOne 的 settle
// defer，不注销即永久泄漏）。不派生撤回：Decode 失败意味着输入损坏、信封行
// 无从定位（decode 产物不可得），撤回 DELETE 必然 no-op，与丢弃语义对齐。
func (p *Producer) persistDeferred(item *deferredCapture) {
	input, err := item.decode()
	if err != nil {
		p.unregisterInFlightEnvelope(item.envelopeID)
		p.warn("F3 审计采集 JSON 编码失败，丢弃本条采集", "error", err, "traceID", item.traceID, "auditLogID", item.auditLogID)
		return
	}
	p.persistOne(input)
}

// retractOne runs the lease-renewed delete of one in_progress envelope. The
// lifecycle guard in the DELETE makes this a no-op for finalized or unknown
// ids, so a retract racing a finalize cannot destroy the terminal row.
//
// W13（2026-10-29 生产取证）：存储抖动窗口里「续租失败 / 删除超时」会让单次
// 撤回直接放弃，信封永留——空壳与 PG 慢语句峰同窗即此形态。改为有界重试：
// 最多 3 次（续租+删除为一个尝试单元），总预算 15s，退避 100/200ms；正常
// 存储下单次即成功，不影响稳态路径。
//
// DD-D1（2026-10-31，W13 第四产生面收口）：撤回命令可能先于同一 id 的信封
// persist settle 到达（存储慢窗：信封 persist 卡在续租/持久化超时，Cancel
// 派发的撤回被空闲 worker 立即执行 → DELETE 无行 no-op → 信封随后提交 →
// 永久空壳）。撤回先等待在途信封 settle（等待共享 15s 总预算；预算耗尽仍
// 在途则放弃本次撤回——此时 DELETE 必然 no-op，执行只会更差），settle 后
// 才执行删除；入队路径与 retractBestEffort 内联路径（w13h）同经此处。
func (p *Producer) retractOne(auditLogID string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			p.warn("F3 审计信封撤回 panic 已隔离", "panic", recovered, "auditLogID", auditLogID)
		}
	}()
	const attempts = 3
	deadline := time.Now().Add(15 * time.Second)
	p.waitForEnvelopeSettle(auditLogID, deadline)
	if p.envelopeInFlight(auditLogID) {
		p.warn("F3 审计信封撤回等待 settle 超时，放弃本次撤回", "auditLogID", auditLogID)
		return
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * 100 * time.Millisecond)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attemptBudget := 10 * time.Second
		if remaining < attemptBudget {
			attemptBudget = remaining
		}
		ctx, cancel := context.WithTimeout(context.Background(), attemptBudget)
		lease := p.lease.Lease()
		if p.cfg.OwnerLease > 0 {
			renewCtx, renewCancel := context.WithTimeout(ctx, attemptBudget)
			_, err := p.store.RenewOwnerLease(renewCtx, lease, p.cfg.OwnerLease)
			renewCancel()
			if err != nil {
				cancel()
				lastErr = err
				continue
			}
		}
		retracted, err := p.store.RetractInProgress(ctx, lease, auditLogID)
		cancel()
		if err == nil {
			_ = retracted
			return
		}
		lastErr = err
	}
	p.warn("F3 审计信封撤回重试耗尽", "error", lastErr, "auditLogID", auditLogID, "attempts", attempts)
}

// waitForEnvelopeSettle 阻塞至 id 的信封 persist settle 或预算耗尽（DD-D1）。
// 轮询间隔 20ms；settle 注销由 persistOne 的 defer 保证，即使 persist 以
// 失败/panic 结束也会到达，等待有界。
func (p *Producer) waitForEnvelopeSettle(auditLogID string, deadline time.Time) {
	for p.envelopeInFlight(auditLogID) {
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// persistOne keeps the retired write sequence (renew → persist → hot search)
// with the same per-record degradation as the original per-record goroutine,
// plus panic isolation: one broken record must not take down the worker pool
// or the process.
func (p *Producer) persistOne(input AuditLogInput) {
	startedAt := time.Now()
	outcome := "ok"
	needsRetract := false
	defer func() {
		// DD-D1（2026-10-31）：settle 边界先注销在途信封、再派生撤回——本条
		// 输入的 store 交互到此已结束（panic 臂上 ctx cancel 因 defer LIFO 先
		// 于 recover 运行，panicked 事务随 ctx 取消回滚/丢弃），其后执行的
		// 撤回与在途集合不再竞争同一信封；若先撤回后注销，同 worker 内联的
		// 撤回会在在途检查处与 settle 互等，直到 15s 重试预算耗尽。
		p.unregisterInFlightEnvelope(input.ID)
		if recovered := recover(); recovered != nil {
			p.warn("F3 审计采集持久化 panic 已隔离", "panic", recovered, "traceID", input.TraceID, "auditLogID", input.ID)
			// W13 残留①收口（2026-10-28）：panic 点可能已写入 in_progress
			// 信封，不派发撤回即残留空壳；lifecycle 守卫使无行/终态行的
			// 撤回为无害 no-op，与两条失败分支同口径。
			p.retractBestEffort(input.ID)
			p.observePersistDuration("panic", startedAt)
			return
		}
		if needsRetract {
			p.retractBestEffort(input.ID)
		}
		p.observePersistDuration(outcome, startedAt)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease := p.lease.Lease()
	// Extending the lease per record keeps it alive under write activity
	// (the LeaseKeeper ticker covers the idle case). A non-positive TTL
	// would set lease_until to the current instant and self-destruct the
	// fence, so the renewal is skipped instead — the configured
	// composition always passes the real owner-lease TTL.
	if p.cfg.OwnerLease > 0 {
		renewCtx, renewCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer renewCancel()
		renewed, err := p.store.RenewOwnerLease(renewCtx, lease, p.cfg.OwnerLease)
		if err != nil || !renewed {
			outcome = "error"
			p.warn("F3 owner lease renewal failed; dropping audit capture", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
			// W13 生产复核（2026-10-24）：丢弃采集时该 id 的 in_progress 信封
			// 行可能已落库，不派发撤回就永久残留空壳；与 Cancel() 同口径。
			needsRetract = true
			return
		}
	}
	persistResult, err := p.store.Persist(ctx, lease, input)
	if err != nil {
		outcome = "error"
		p.warn("F3 审计采集持久化失败", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
		// 终态写入失败同样留下已落库的信封行（生产 13:16 blob 锁/元数据
		// 超时形态）；lifecycle 守卫使撤回对无行/终态行都是无害 no-op。
		needsRetract = true
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

// observePersistDuration reports the per-record persistence latency through
// the optional observer (result "ok" | "error" | "panic").
func (p *Producer) observePersistDuration(result string, startedAt time.Time) {
	if p == nil || p.persistDuration == nil {
		return
	}
	p.persistDuration(result, time.Since(startedAt).Milliseconds())
}
