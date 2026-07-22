// Package systemmetricsruntime defines the Go-native, in-process runtime
// snapshot contract. It deliberately does not model Node.js event-loop or
// DB-service process fields.
package systemmetricsruntime

import (
	"context"
	"math"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"time"
)

const RuntimeKindGo = "go"

const (
	HardMaxRedisPools = 8
	HardMaxWorkers    = 16
	HardMaxQueues     = 32

	defaultRedisPools = 3
	defaultWorkers    = 8
	defaultQueues     = 16
)

type RuntimeRole string

const (
	RuntimeRoleServer       RuntimeRole = "server"
	RuntimeRoleIngestWorker RuntimeRole = "ingest-worker"
	RuntimeRoleStatsWorker  RuntimeRole = "stats-worker"
	RuntimeRoleOpsWorker    RuntimeRole = "ops-worker"
)

type RedisRole string

const (
	RedisRoleCache RedisRole = "cache"
	RedisRoleState RedisRole = "state"
	RedisRoleQueue RedisRole = "queue"
)

type UnavailableReason string

const (
	UnavailableNotConfigured  UnavailableReason = "not_configured"
	UnavailableSnapshotFailed UnavailableReason = "snapshot_failed"
)

// Snapshot is intentionally independent of an HTTP response contract. A
// handler may wrap or transform it without coupling this package to routing.
type Snapshot struct {
	RuntimeKind string                          `json:"runtimeKind"`
	SampledAt   time.Time                       `json:"sampledAt"`
	Runtime     Section[RuntimeSnapshot]        `json:"runtime"`
	PostgreSQL  Section[PostgreSQLPoolSnapshot] `json:"postgresql"`
	Redis       ListSection[RedisPoolSnapshot]  `json:"redis"`
	Workers     ListSection[WorkerSnapshot]     `json:"workers"`
	Queues      ListSection[QueueSnapshot]      `json:"queues"`
}

type Section[T any] struct {
	Available         bool              `json:"available"`
	Value             *T                `json:"value,omitempty"`
	UnavailableReason UnavailableReason `json:"unavailableReason,omitempty"`
}

type ListSection[T any] struct {
	Available         bool              `json:"available"`
	Items             []T               `json:"items,omitempty"`
	Truncated         bool              `json:"truncated"`
	UnavailableReason UnavailableReason `json:"unavailableReason,omitempty"`
}

type RuntimeSnapshot struct {
	ProcessRole             RuntimeRole `json:"processRole"`
	ProcessPID              int         `json:"processPid"`
	SampledAt               time.Time   `json:"sampledAt"`
	GoVersion               string      `json:"goVersion"`
	UptimeSeconds           int64       `json:"uptimeSeconds"`
	GoMaxProcs              int         `json:"gomaxprocs"`
	Goroutines              int         `json:"goroutines"`
	GCCyclesTotal           uint32      `json:"gcCyclesTotal"`
	GCCPUFraction           float64     `json:"gcCpuFraction"`
	HeapAllocBytes          uint64      `json:"heapAllocBytes"`
	HeapLiveBytes           uint64      `json:"heapLiveBytes"`
	HeapObjects             uint64      `json:"heapObjects"`
	HeapGoalBytes           uint64      `json:"gcHeapGoalBytes"`
	MemoryClassesTotalBytes uint64      `json:"memoryClassesTotalBytes"`
	GoMemoryLimitBytes      uint64      `json:"gomemlimitBytes"`
	SchedulerLatencyP95MS   *float64    `json:"schedulerLatencyP95Ms,omitempty"`
	SchedulerLatencyP99MS   *float64    `json:"schedulerLatencyP99Ms,omitempty"`
	GCPauseP95MS            *float64    `json:"gcPauseP95Ms,omitempty"`
	GCPauseP99MS            *float64    `json:"gcPauseP99Ms,omitempty"`
	MutexWaitSecondsTotal   *float64    `json:"mutexWaitSecondsTotal,omitempty"`
}

// PostgreSQLPoolSnapshot matches pgxpool.Stat semantics without importing a
// concrete pool into the domain contract.
type PostgreSQLPoolSnapshot struct {
	Acquired             int32   `json:"acquired"`
	Idle                 int32   `json:"idle"`
	Total                int32   `json:"total"`
	Max                  int32   `json:"max"`
	EmptyAcquireCount    int64   `json:"emptyAcquireCount"`
	CanceledAcquireCount int64   `json:"canceledAcquireCount"`
	AcquireDurationMS    float64 `json:"acquireDurationMs"`
}

// RedisPoolSnapshot matches the stable go-redis pool counters while retaining
// the required cache/state/queue role separation.
type RedisPoolSnapshot struct {
	Role           RedisRole `json:"role"`
	Hits           uint32    `json:"hits"`
	Misses         uint32    `json:"misses"`
	Timeouts       uint32    `json:"timeouts"`
	WaitCount      uint32    `json:"waitCount"`
	WaitDurationMS float64   `json:"waitDurationMs"`
	Total          uint32    `json:"total"`
	Idle           uint32    `json:"idle"`
	Stale          uint32    `json:"stale"`
}

type WorkerSnapshot struct {
	Role                   RuntimeRole `json:"role"`
	Ready                  bool        `json:"ready"`
	InFlight               int         `json:"inFlight"`
	Waiting                int         `json:"waiting"`
	Capacity               int         `json:"capacity"`
	AdmissionRejectedTotal uint64      `json:"admissionRejectedTotal"`
	LastHeartbeatAt        *time.Time  `json:"lastHeartbeatAt,omitempty"`
}

type QueueSnapshot struct {
	Name                 string   `json:"name"`
	Pending              int      `json:"pending"`
	Active               int      `json:"active"`
	Retry                int      `json:"retry"`
	Dead                 int      `json:"dead"`
	Archived             int      `json:"archived"`
	OldestTaskAgeSeconds *int64   `json:"oldestTaskAgeSeconds,omitempty"`
	ProcessedTotal       uint64   `json:"processedTotal"`
	FailedTotal          uint64   `json:"failedTotal"`
	DurationP95MS        *float64 `json:"durationP95Ms,omitempty"`
}

type RuntimeSource interface {
	SnapshotRuntime() RuntimeSnapshot
}

type PostgreSQLPoolSource interface {
	SnapshotPostgreSQLPool() PostgreSQLPoolSnapshot
}

type RedisPoolSource interface {
	SnapshotRedisPools() []RedisPoolSnapshot
}

type WorkerSource interface {
	SnapshotWorkers(ctx context.Context, limit int) ([]WorkerSnapshot, error)
}

type QueueSource interface {
	SnapshotQueues(ctx context.Context, limit int) ([]QueueSnapshot, error)
}

type Dependencies struct {
	Runtime    RuntimeSource
	PostgreSQL PostgreSQLPoolSource
	Redis      RedisPoolSource
	Workers    WorkerSource
	Queues     QueueSource
}

type Limits struct {
	RedisPools int
	Workers    int
	Queues     int
}

type Options struct {
	Now    func() time.Time
	Limits Limits
}

type Service struct {
	dependencies Dependencies
	now          func() time.Time
	limits       Limits
}

func NewService(dependencies Dependencies, options Options) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		dependencies: dependencies,
		now:          now,
		limits: Limits{
			RedisPools: normalizeLimit(options.Limits.RedisPools, defaultRedisPools, HardMaxRedisPools),
			Workers:    normalizeLimit(options.Limits.Workers, defaultWorkers, HardMaxWorkers),
			Queues:     normalizeLimit(options.Limits.Queues, defaultQueues, HardMaxQueues),
		},
	}
}

func (s *Service) Snapshot(ctx context.Context) Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := Snapshot{
		RuntimeKind: RuntimeKindGo,
		SampledAt:   s.now().UTC(),
		Runtime:     unavailableSection[RuntimeSnapshot](UnavailableNotConfigured),
		PostgreSQL:  unavailableSection[PostgreSQLPoolSnapshot](UnavailableNotConfigured),
		Redis:       unavailableListSection[RedisPoolSnapshot](UnavailableNotConfigured),
		Workers:     unavailableListSection[WorkerSnapshot](UnavailableNotConfigured),
		Queues:      unavailableListSection[QueueSnapshot](UnavailableNotConfigured),
	}

	if s.dependencies.Runtime != nil {
		value := s.dependencies.Runtime.SnapshotRuntime()
		snapshot.Runtime = availableSection(value)
	}
	if s.dependencies.PostgreSQL != nil {
		value := s.dependencies.PostgreSQL.SnapshotPostgreSQLPool()
		snapshot.PostgreSQL = availableSection(value)
	}
	if s.dependencies.Redis != nil {
		snapshot.Redis = boundedListSection(s.dependencies.Redis.SnapshotRedisPools(), s.limits.RedisPools)
	}
	if s.dependencies.Workers != nil {
		items, err := s.dependencies.Workers.SnapshotWorkers(ctx, requestLimit(s.limits.Workers))
		if err != nil {
			snapshot.Workers = unavailableListSection[WorkerSnapshot](UnavailableSnapshotFailed)
		} else {
			snapshot.Workers = boundedListSection(items, s.limits.Workers)
		}
	}
	if s.dependencies.Queues != nil {
		items, err := s.dependencies.Queues.SnapshotQueues(ctx, requestLimit(s.limits.Queues))
		if err != nil {
			snapshot.Queues = unavailableListSection[QueueSnapshot](UnavailableSnapshotFailed)
		} else {
			snapshot.Queues = boundedListSection(items, s.limits.Queues)
		}
	}
	return snapshot
}

func normalizeLimit(value int, defaultValue int, hardMaximum int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > hardMaximum {
		return hardMaximum
	}
	return value
}

func requestLimit(limit int) int {
	return limit + 1
}

func availableSection[T any](value T) Section[T] {
	return Section[T]{Available: true, Value: &value}
}

func unavailableSection[T any](reason UnavailableReason) Section[T] {
	return Section[T]{UnavailableReason: reason}
}

func boundedListSection[T any](items []T, limit int) ListSection[T] {
	length := len(items)
	if length > limit {
		length = limit
	}
	bounded := make([]T, length)
	copy(bounded, items[:length])
	return ListSection[T]{
		Available: true,
		Items:     bounded,
		Truncated: len(items) > limit,
	}
}

func unavailableListSection[T any](reason UnavailableReason) ListSection[T] {
	return ListSection[T]{UnavailableReason: reason}
}

type StandardRuntimeSource struct {
	role      RuntimeRole
	startedAt time.Time
	now       func() time.Time
}

func NewStandardRuntimeSource(role RuntimeRole, startedAt time.Time, now func() time.Time) *StandardRuntimeSource {
	if now == nil {
		now = time.Now
	}
	return &StandardRuntimeSource{role: role, startedAt: startedAt, now: now}
}

func (s *StandardRuntimeSource) SnapshotRuntime() RuntimeSnapshot {
	now := s.now().UTC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)

	values := readRuntimeMetricValues()
	return RuntimeSnapshot{
		ProcessRole:             s.role,
		ProcessPID:              os.Getpid(),
		SampledAt:               now,
		GoVersion:               runtime.Version(),
		UptimeSeconds:           max(0, int64(now.Sub(s.startedAt).Seconds())),
		GoMaxProcs:              runtime.GOMAXPROCS(0),
		Goroutines:              runtime.NumGoroutine(),
		GCCyclesTotal:           memory.NumGC,
		GCCPUFraction:           memory.GCCPUFraction,
		HeapAllocBytes:          memory.HeapAlloc,
		HeapLiveBytes:           uint64Metric(values, "/gc/heap/live:bytes"),
		HeapObjects:             memory.HeapObjects,
		HeapGoalBytes:           uint64Metric(values, "/gc/heap/goal:bytes"),
		MemoryClassesTotalBytes: uint64Metric(values, "/memory/classes/total:bytes"),
		GoMemoryLimitBytes:      uint64Metric(values, "/gc/gomemlimit:bytes"),
		SchedulerLatencyP95MS:   histogramQuantileMilliseconds(values, "/sched/latencies:seconds", 0.95),
		SchedulerLatencyP99MS:   histogramQuantileMilliseconds(values, "/sched/latencies:seconds", 0.99),
		GCPauseP95MS:            histogramQuantileMilliseconds(values, "/gc/pauses:seconds", 0.95),
		GCPauseP99MS:            histogramQuantileMilliseconds(values, "/gc/pauses:seconds", 0.99),
		MutexWaitSecondsTotal:   float64Metric(values, "/sync/mutex/wait/total:seconds"),
	}
}

func readRuntimeMetricValues() map[string]runtimemetrics.Value {
	names := []string{
		"/gc/heap/live:bytes",
		"/gc/heap/goal:bytes",
		"/gc/gomemlimit:bytes",
		"/gc/pauses:seconds",
		"/memory/classes/total:bytes",
		"/sched/latencies:seconds",
		"/sync/mutex/wait/total:seconds",
	}
	samples := make([]runtimemetrics.Sample, len(names))
	for index, name := range names {
		samples[index].Name = name
	}
	runtimemetrics.Read(samples)
	values := make(map[string]runtimemetrics.Value, len(samples))
	for _, sample := range samples {
		values[sample.Name] = sample.Value
	}
	return values
}

func uint64Metric(values map[string]runtimemetrics.Value, name string) uint64 {
	value, ok := values[name]
	if !ok || value.Kind() != runtimemetrics.KindUint64 {
		return 0
	}
	return value.Uint64()
}

func float64Metric(values map[string]runtimemetrics.Value, name string) *float64 {
	value, ok := values[name]
	if !ok || value.Kind() != runtimemetrics.KindFloat64 {
		return nil
	}
	metric := value.Float64()
	return &metric
}

func histogramQuantileMilliseconds(values map[string]runtimemetrics.Value, name string, quantile float64) *float64 {
	value, ok := values[name]
	if !ok || value.Kind() != runtimemetrics.KindFloat64Histogram {
		return nil
	}
	seconds, ok := histogramQuantile(value.Float64Histogram(), quantile)
	if !ok {
		return nil
	}
	milliseconds := seconds * 1000
	return &milliseconds
}

func histogramQuantile(histogram *runtimemetrics.Float64Histogram, quantile float64) (float64, bool) {
	if histogram == nil || len(histogram.Counts) == 0 || len(histogram.Buckets) != len(histogram.Counts)+1 {
		return 0, false
	}
	var total uint64
	for _, count := range histogram.Counts {
		total += count
	}
	if total == 0 {
		return 0, false
	}
	target := uint64(math.Ceil(float64(total) * quantile))
	if target == 0 {
		target = 1
	}
	var cumulative uint64
	for index, count := range histogram.Counts {
		cumulative += count
		if cumulative < target {
			continue
		}
		upper := histogram.Buckets[index+1]
		if math.IsInf(upper, 1) {
			lower := histogram.Buckets[index]
			if math.IsInf(lower, -1) {
				return 0, false
			}
			return lower, true
		}
		return upper, true
	}
	return 0, false
}
