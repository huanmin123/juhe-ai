package gometrics

import (
	"os"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

// RuntimeSnapshot is the bounded, low-cardinality payload intended for a
// window store. Histogram buckets remain in Prometheus; this payload contains
// only scalar runtime gauges suitable for management trends.
type RuntimeSnapshot struct {
	SampledAt  time.Time
	ProcessPID int
	Service    string
	Role       string

	Goroutines         uint64
	GoroutinesRunnable uint64
	GoroutinesWaiting  uint64
	Threads            uint64
	GOMAXPROCS         uint64
	HeapAllocBytes     uint64
	HeapLiveBytes      uint64
	HeapObjects        uint64
	// CPUSecondsTotal is the portable Go runtime counter
	// /cpu/classes/total:cpu-seconds. It is intentionally not read from an
	// operating-system process API, so Windows development and Linux deployment
	// have the same metric semantics.
	CPUSecondsTotal float64
	CPUPercent         *float64
	// RSSBytes and FDCount are retained as nullable storage compatibility
	// fields for explicitly supplied legacy samples. Collector.Snapshot never
	// populates them because host RSS/FD semantics are outside the portable Go
	// runtime contract.
	RSSBytes           *uint64
	FDCount            *uint64
	UptimeSeconds      float64
}

// Snapshot returns a point-in-time runtime sample without adding labels or
// business dimensions.
func (c *Collector) Snapshot() RuntimeSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	samples := make([]metrics.Sample, len(c.samples))
	c.mu.Lock()
	copy(samples, c.samples)
	c.mu.Unlock()
	metrics.Read(samples)
	sampledAt := time.Now().UTC()
	result := RuntimeSnapshot{SampledAt: sampledAt, ProcessPID: os.Getpid(), Service: c.service, Role: c.role, HeapAllocBytes: mem.HeapAlloc, HeapObjects: mem.HeapObjects, UptimeSeconds: sampledAt.Sub(c.started).Seconds()}
	cpuSeconds, cpuSecondsValid := 0.0, false
	for _, sample := range samples {
		switch sample.Value.Kind() {
		case metrics.KindFloat64:
			if sample.Name == cpuTotalMetric {
				cpuSeconds = sample.Value.Float64()
				cpuSecondsValid = true
			}
		case metrics.KindUint64:
			value := sample.Value.Uint64()
			switch sample.Name {
			case goroutinesMetric:
				result.Goroutines = value
			case runnableMetric:
				result.GoroutinesRunnable = value
			case waitingMetric:
				result.GoroutinesWaiting = value
			case threadsMetric:
				result.Threads = value
			case gomaxprocsMetric:
				result.GOMAXPROCS = value
			case heapLiveMetric:
				result.HeapLiveBytes = value
			}
		}
	}
	if cpuSecondsValid {
		result.CPUSecondsTotal = cpuSeconds
	}
	c.state.mu.Lock()
	if cpuSecondsValid && c.state.hasLastCPU && sampledAt.After(c.state.lastAt) && result.CPUSecondsTotal >= c.state.lastCPU {
		value := (result.CPUSecondsTotal - c.state.lastCPU) / sampledAt.Sub(c.state.lastAt).Seconds() * 100
		result.CPUPercent = &value
	}
	c.state.lastAt = sampledAt
	if cpuSecondsValid {
		c.state.lastCPU = result.CPUSecondsTotal
		c.state.hasLastCPU = true
	}
	c.state.mu.Unlock()
	c.mu.Lock()
	c.latest = result
	c.latestValid = true
	c.mu.Unlock()
	return result
}

type WindowAggregate struct {
	WindowStart           time.Time `json:"windowStart"`
	WindowEnd             time.Time `json:"windowEnd"`
	Service               string    `json:"service"`
	Role                  string    `json:"role"`
	RuntimeKind           string    `json:"runtimeKind"`
	SampleCount           uint64    `json:"sampleCount"`
	GoroutinesAvg         float64   `json:"goroutinesAvg"`
	GoroutinesMax         float64   `json:"goroutinesMax"`
	GoroutinesRunnableAvg float64   `json:"goroutinesRunnableAvg"`
	GoroutinesRunnableMax float64   `json:"goroutinesRunnableMax"`
	GoroutinesWaitingAvg  float64   `json:"goroutinesWaitingAvg"`
	GoroutinesWaitingMax  float64   `json:"goroutinesWaitingMax"`
	GOMAXPROCSAvg         float64   `json:"gomaxprocsAvg"`
	GOMAXPROCSMax         float64   `json:"gomaxprocsMax"`
	HeapAllocBytesAvg     float64   `json:"heapAllocBytesAvg"`
	HeapAllocBytesMax     float64   `json:"heapAllocBytesMax"`
	HeapLiveBytesAvg      float64   `json:"heapLiveBytesAvg"`
	HeapLiveBytesMax      float64   `json:"heapLiveBytesMax"`
	HeapObjectsAvg        float64   `json:"heapObjectsAvg"`
	HeapObjectsMax        float64   `json:"heapObjectsMax"`
	ThreadsAvg            float64   `json:"threadsAvg"`
	ThreadsMax            float64   `json:"threadsMax"`
	CPUPercentAvg         *float64  `json:"cpuPercentAvg"`
	CPUPercentMax         *float64  `json:"cpuPercentMax"`
	RSSBytesAvg           *float64  `json:"rssBytesAvg"`
	RSSBytesMax           *float64  `json:"rssBytesMax"`
	FDCountAvg            *float64  `json:"fdCountAvg"`
	FDCountMax            *float64  `json:"fdCountMax"`
	UptimeSecondsAvg      float64   `json:"uptimeSecondsAvg"`
	UptimeSecondsMax      float64   `json:"uptimeSecondsMax"`
}

type windowAccumulator struct {
	count uint64
	sums  [12]float64
	max   [12]float64
	valid [12]uint64
}

// WindowAggregator keeps bounded in-memory hourly aggregates. It is a pure
// aggregation component; durable persistence is owned by the jobs sampler.
type WindowAggregator struct {
	mu        sync.RWMutex
	retention time.Duration
	windows   map[windowKey]*windowAccumulator
	watermark time.Time
}

type windowKey struct {
	start   time.Time
	service string
	role    string
}

func NewWindowAggregator(retention time.Duration) *WindowAggregator {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	return &WindowAggregator{retention: retention, windows: make(map[windowKey]*windowAccumulator)}
}

func (a *WindowAggregator) Add(sample RuntimeSnapshot) {
	when := sample.SampledAt.UTC()
	if when.IsZero() {
		return
	}
	start := when.Truncate(time.Hour)
	values := [12]float64{float64(sample.Goroutines), float64(sample.GoroutinesRunnable), float64(sample.GoroutinesWaiting), float64(sample.Threads), float64(sample.GOMAXPROCS), float64(sample.HeapAllocBytes), float64(sample.HeapLiveBytes), float64(sample.HeapObjects), pointerFloat(sample.CPUPercent), pointerUint(sample.RSSBytes), pointerUint(sample.FDCount), sample.UptimeSeconds}
	available := [12]bool{true, true, true, true, true, true, true, true, sample.CPUPercent != nil, sample.RSSBytes != nil, sample.FDCount != nil, true}
	a.mu.Lock()
	if when.After(a.watermark) {
		a.watermark = when
	}
	key := windowKey{start: start, service: sample.Service, role: sample.Role}
	acc := a.windows[key]
	if acc == nil {
		acc = &windowAccumulator{}
		a.windows[key] = acc
	}
	acc.count++
	for i, value := range values {
		if !available[i] {
			continue
		}
		acc.valid[i]++
		acc.sums[i] += value
		if value > acc.max[i] {
			acc.max[i] = value
		}
	}
	cutoff := a.watermark.Add(-a.retention).Truncate(time.Hour)
	for key := range a.windows {
		if key.start.Before(cutoff) {
			delete(a.windows, key)
		}
	}
	a.mu.Unlock()
}

func (a *WindowAggregator) Windows() []WindowAggregate {
	a.mu.RLock()
	result := make([]WindowAggregate, 0, len(a.windows))
	for key, acc := range a.windows {
		if acc.count == 0 {
			continue
		}
		result = append(result, WindowAggregate{WindowStart: key.start, WindowEnd: key.start.Add(time.Hour), Service: key.service, Role: key.role, RuntimeKind: "go", SampleCount: acc.count,
			GoroutinesAvg: acc.sums[0] / float64(acc.count), GoroutinesMax: acc.max[0],
			GoroutinesRunnableAvg: acc.sums[1] / float64(acc.count), GoroutinesRunnableMax: acc.max[1],
			GoroutinesWaitingAvg: acc.sums[2] / float64(acc.count), GoroutinesWaitingMax: acc.max[2],
			ThreadsAvg: acc.sums[3] / float64(acc.count), ThreadsMax: acc.max[3],
			GOMAXPROCSAvg: acc.sums[4] / float64(acc.count), GOMAXPROCSMax: acc.max[4],
			HeapAllocBytesAvg: acc.sums[5] / float64(acc.count), HeapAllocBytesMax: acc.max[5],
			HeapLiveBytesAvg: acc.sums[6] / float64(acc.count), HeapLiveBytesMax: acc.max[6],
			HeapObjectsAvg: acc.sums[7] / float64(acc.count), HeapObjectsMax: acc.max[7],
			CPUPercentAvg: optionalAggregate(acc.sums[8], acc.max[8], acc.valid[8]), CPUPercentMax: optionalMax(acc.max[8], acc.valid[8]),
			RSSBytesAvg: optionalAggregate(acc.sums[9], acc.max[9], acc.valid[9]), RSSBytesMax: optionalMax(acc.max[9], acc.valid[9]),
			FDCountAvg: optionalAggregate(acc.sums[10], acc.max[10], acc.valid[10]), FDCountMax: optionalMax(acc.max[10], acc.valid[10]),
			UptimeSecondsAvg: acc.sums[11] / float64(acc.count), UptimeSecondsMax: acc.max[11]})
	}
	a.mu.RUnlock()
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && windowAggregateBefore(result[j], result[j-1]); j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func pointerFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
func pointerUint(value *uint64) float64 {
	if value == nil {
		return 0
	}
	return float64(*value)
}
func optionalAggregate(sum, _ float64, count uint64) *float64 {
	if count == 0 {
		return nil
	}
	value := sum / float64(count)
	return &value
}
func optionalMax(value float64, count uint64) *float64 {
	if count == 0 {
		return nil
	}
	return &value
}

func windowAggregateBefore(left, right WindowAggregate) bool {
	if !left.WindowStart.Equal(right.WindowStart) {
		return left.WindowStart.Before(right.WindowStart)
	}
	if left.Service != right.Service {
		return left.Service < right.Service
	}
	return left.Role < right.Role
}
