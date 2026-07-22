// Package auditruntime defines the Go runtime-observation boundary for audit
// persistence. It intentionally models Go components instead of mirroring the
// legacy Node worker/IPC snapshot DTO.
package auditruntime

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// State reports whether a metric component is usable. StateUnknown is distinct
// from an observed component whose counters currently happen to be zero.
type State string

const (
	StateUnknown     State = "unknown"
	StateDisabled    State = "disabled"
	StateReady       State = "ready"
	StateDegraded    State = "degraded"
	StateUnavailable State = "unavailable"
)

// QueueMetrics is the producer/consumer backlog observed in the Go audit
// pipeline. Capacity values are zero when a source has no configured bound;
// callers must inspect State before interpreting any counter.
type QueueMetrics struct {
	State         State
	Pending       uint64
	PendingBytes  uint64
	Capacity      uint64
	CapacityBytes uint64
}

// WorkerMetrics describes the Go workers consuming audit work. Process IDs are
// deliberately absent because process topology is not part of the audit
// domain contract.
type WorkerMetrics struct {
	State    State
	Running  uint32
	Desired  uint32
	Restarts uint64
}

// DroppedMetrics contains two orthogonal views of dropped audit events:
// success/failure outcome and overflow/oversize cause.
type DroppedMetrics struct {
	State    State
	Success  uint64
	Failure  uint64
	Overflow uint64
	Oversize uint64
}

// InflightMetrics describes audit captures that have started but have not yet
// reached their terminal enqueue or drop decision.
type InflightMetrics struct {
	State    State
	Captures uint64
	Bytes    uint64
}

// TransportMetrics describes bounded preparation and delivery work before
// persistence. Inflight is work currently executing, not queued work.
type TransportMetrics struct {
	State         State
	Queued        uint64
	QueuedBytes   uint64
	Inflight      uint64
	InflightBytes uint64
	Workers       uint32
	Completed     uint64
	Failed        uint64
	Rejected      uint64
}

// StorageMetrics is an in-memory observation of persistence results. A port
// implementation must not perform a storage health probe while a snapshot is
// being collected.
type StorageMetrics struct {
	State               State
	PendingWrites       uint64
	CompletedWrites     uint64
	FailedWrites        uint64
	ConsecutiveFailures uint64
	LastSuccessAt       time.Time
	LastFailureAt       time.Time
	LastErrorCode       string
}

// The six ports stay intentionally narrow. Implementations must return a
// non-blocking, internally consistent value snapshot and must not do network,
// Redis, or database I/O from these methods.
type QueueMetricsPort interface {
	AuditQueueMetrics() QueueMetrics
}

type WorkerMetricsPort interface {
	AuditWorkerMetrics() WorkerMetrics
}

type DroppedMetricsPort interface {
	AuditDroppedMetrics() DroppedMetrics
}

type InflightMetricsPort interface {
	AuditInflightMetrics() InflightMetrics
}

type TransportMetricsPort interface {
	AuditTransportMetrics() TransportMetrics
}

type StorageMetricsPort interface {
	AuditStorageMetrics() StorageMetrics
}

// Sources groups independent observers without coupling the service to a
// router, server, config package, queue driver, or storage implementation.
type Sources struct {
	Queue     QueueMetricsPort
	Worker    WorkerMetricsPort
	Dropped   DroppedMetricsPort
	Inflight  InflightMetricsPort
	Transport TransportMetricsPort
	Storage   StorageMetricsPort
}

type QueueSnapshot struct {
	State         State  `json:"state"`
	Pending       uint64 `json:"pending"`
	PendingBytes  uint64 `json:"pendingBytes"`
	Capacity      uint64 `json:"capacity"`
	CapacityBytes uint64 `json:"capacityBytes"`
}

type WorkerSnapshot struct {
	State    State  `json:"state"`
	Running  uint32 `json:"running"`
	Desired  uint32 `json:"desired"`
	Restarts uint64 `json:"restarts"`
}

type DroppedSnapshot struct {
	State    State  `json:"state"`
	Total    uint64 `json:"total"`
	Success  uint64 `json:"success"`
	Failure  uint64 `json:"failure"`
	Overflow uint64 `json:"overflow"`
	Oversize uint64 `json:"oversize"`
}

type InflightSnapshot struct {
	State    State  `json:"state"`
	Captures uint64 `json:"captures"`
	Bytes    uint64 `json:"bytes"`
}

type TransportSnapshot struct {
	State         State  `json:"state"`
	Queued        uint64 `json:"queued"`
	QueuedBytes   uint64 `json:"queuedBytes"`
	Inflight      uint64 `json:"inflight"`
	InflightBytes uint64 `json:"inflightBytes"`
	Workers       uint32 `json:"workers"`
	Completed     uint64 `json:"completed"`
	Failed        uint64 `json:"failed"`
	Rejected      uint64 `json:"rejected"`
}

type StorageSnapshot struct {
	State               State     `json:"state"`
	PendingWrites       uint64    `json:"pendingWrites"`
	CompletedWrites     uint64    `json:"completedWrites"`
	FailedWrites        uint64    `json:"failedWrites"`
	ConsecutiveFailures uint64    `json:"consecutiveFailures"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitzero"`
	LastFailureAt       time.Time `json:"lastFailureAt,omitzero"`
	LastErrorCode       string    `json:"lastErrorCode,omitempty"`
}

// Snapshot is immutable after publication. Revision advances once per
// successful Refresh call and lets callers detect a stale observation without
// depending on wall-clock precision.
type Snapshot struct {
	Revision   uint64            `json:"revision"`
	CapturedAt time.Time         `json:"capturedAt,omitzero"`
	Queue      QueueSnapshot     `json:"queue"`
	Worker     WorkerSnapshot    `json:"worker"`
	Dropped    DroppedSnapshot   `json:"dropped"`
	Inflight   InflightSnapshot  `json:"inflight"`
	Transport  TransportSnapshot `json:"transport"`
	Storage    StorageSnapshot   `json:"storage"`
}

type Option func(*Service)

// WithClock is intended for deterministic tests and runtime integrations that
// already own a monotonic observation clock.
func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

// Service publishes whole snapshots through an atomic pointer. Refresh is
// serialized so a slower refresh cannot overwrite a newer revision; reads are
// lock-free and can never observe a partially assembled snapshot.
type Service struct {
	sources Sources
	clock   func() time.Time

	refreshMu sync.Mutex
	revision  uint64
	current   atomic.Pointer[Snapshot]
}

func NewService(sources Sources, options ...Option) *Service {
	service := &Service{
		sources: sources,
		clock:   time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	initial := unknownSnapshot()
	service.current.Store(&initial)
	return service
}

// Refresh collects the six non-blocking metric ports and atomically publishes
// the resulting value. A missing port remains StateUnknown rather than being
// reported as a healthy component with zero work.
func (s *Service) Refresh() Snapshot {
	if s == nil {
		return unknownSnapshot()
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.revision++
	snapshot := unknownSnapshot()
	snapshot.Revision = s.revision
	snapshot.CapturedAt = s.clock().UTC()
	if s.sources.Queue != nil {
		snapshot.Queue = queueSnapshot(s.sources.Queue.AuditQueueMetrics())
	}
	if s.sources.Worker != nil {
		snapshot.Worker = workerSnapshot(s.sources.Worker.AuditWorkerMetrics())
	}
	if s.sources.Dropped != nil {
		snapshot.Dropped = droppedSnapshot(s.sources.Dropped.AuditDroppedMetrics())
	}
	if s.sources.Inflight != nil {
		snapshot.Inflight = inflightSnapshot(s.sources.Inflight.AuditInflightMetrics())
	}
	if s.sources.Transport != nil {
		snapshot.Transport = transportSnapshot(s.sources.Transport.AuditTransportMetrics())
	}
	if s.sources.Storage != nil {
		snapshot.Storage = storageSnapshot(s.sources.Storage.AuditStorageMetrics())
	}

	s.current.Store(&snapshot)
	return snapshot
}

// Snapshot returns the last whole published observation without collecting
// metrics or acquiring the refresh lock.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return unknownSnapshot()
	}
	current := s.current.Load()
	if current == nil {
		return unknownSnapshot()
	}
	return *current
}

func unknownSnapshot() Snapshot {
	return Snapshot{
		Queue:     QueueSnapshot{State: StateUnknown},
		Worker:    WorkerSnapshot{State: StateUnknown},
		Dropped:   DroppedSnapshot{State: StateUnknown},
		Inflight:  InflightSnapshot{State: StateUnknown},
		Transport: TransportSnapshot{State: StateUnknown},
		Storage:   StorageSnapshot{State: StateUnknown},
	}
}

func queueSnapshot(metrics QueueMetrics) QueueSnapshot {
	return QueueSnapshot{
		State:         normalizeState(metrics.State),
		Pending:       metrics.Pending,
		PendingBytes:  metrics.PendingBytes,
		Capacity:      metrics.Capacity,
		CapacityBytes: metrics.CapacityBytes,
	}
}

func workerSnapshot(metrics WorkerMetrics) WorkerSnapshot {
	return WorkerSnapshot{
		State:    normalizeState(metrics.State),
		Running:  metrics.Running,
		Desired:  metrics.Desired,
		Restarts: metrics.Restarts,
	}
}

func droppedSnapshot(metrics DroppedMetrics) DroppedSnapshot {
	// Outcome and cause are two views of the same events. Taking the larger
	// partition keeps a partially instrumented source useful without repeating
	// the legacy mistake of summing both partitions and double-counting drops.
	byOutcome := saturatingAdd(metrics.Success, metrics.Failure)
	byReason := saturatingAdd(metrics.Overflow, metrics.Oversize)
	return DroppedSnapshot{
		State:    normalizeState(metrics.State),
		Total:    max(byOutcome, byReason),
		Success:  metrics.Success,
		Failure:  metrics.Failure,
		Overflow: metrics.Overflow,
		Oversize: metrics.Oversize,
	}
}

func inflightSnapshot(metrics InflightMetrics) InflightSnapshot {
	return InflightSnapshot{
		State:    normalizeState(metrics.State),
		Captures: metrics.Captures,
		Bytes:    metrics.Bytes,
	}
}

func transportSnapshot(metrics TransportMetrics) TransportSnapshot {
	return TransportSnapshot{
		State:         normalizeState(metrics.State),
		Queued:        metrics.Queued,
		QueuedBytes:   metrics.QueuedBytes,
		Inflight:      metrics.Inflight,
		InflightBytes: metrics.InflightBytes,
		Workers:       metrics.Workers,
		Completed:     metrics.Completed,
		Failed:        metrics.Failed,
		Rejected:      metrics.Rejected,
	}
}

func storageSnapshot(metrics StorageMetrics) StorageSnapshot {
	return StorageSnapshot{
		State:               normalizeState(metrics.State),
		PendingWrites:       metrics.PendingWrites,
		CompletedWrites:     metrics.CompletedWrites,
		FailedWrites:        metrics.FailedWrites,
		ConsecutiveFailures: metrics.ConsecutiveFailures,
		LastSuccessAt:       utcOrZero(metrics.LastSuccessAt),
		LastFailureAt:       utcOrZero(metrics.LastFailureAt),
		LastErrorCode:       strings.TrimSpace(metrics.LastErrorCode),
	}
}

func normalizeState(state State) State {
	switch state {
	case StateDisabled, StateReady, StateDegraded, StateUnavailable:
		return state
	default:
		return StateUnknown
	}
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func utcOrZero(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}
