// Package gometrics exposes a deliberately small Prometheus surface for Go
// processes. It contains runtime health signals only; business counters and
// owner/task metrics belong to the owning feature and must not be inferred here.
package gometrics

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	schedulerLatencyMetric = "/sched/latencies:seconds"
	gcPauseMetric          = "/sched/pauses/total/gc:seconds"
	heapLiveMetric         = "/gc/heap/live:bytes"
	goroutinesMetric       = "/sched/goroutines:goroutines"
	runnableMetric         = "/sched/goroutines/runnable:goroutines"
	waitingMetric          = "/sched/goroutines/waiting:goroutines"
	threadsMetric          = "/sched/threads/total:threads"
	gomaxprocsMetric       = "/sched/gomaxprocs:threads"
	cpuTotalMetric         = "/cpu/classes/total:cpu-seconds"
	cpuIdleMetric          = "/cpu/classes/idle:cpu-seconds"
)

// Collector is safe for concurrent scrapes. Labels are fixed at construction
// time and must be bounded deployment values (for example service and role).
type Collector struct {
	service string
	role    string
	started time.Time

	mu      sync.Mutex
	samples []metrics.Sample
	windows *WindowAggregator
	state   collectorState
	latest  RuntimeSnapshot
}

type collectorState struct {
	mu         sync.Mutex
	lastAt     time.Time
	lastCPU    float64
	hasLastCPU bool
}

func New(service, role string) *Collector {
	return &Collector{
		service: sanitizeLabelValue(service),
		role:    sanitizeLabelValue(role),
		started: time.Now().UTC(),
		windows: NewWindowAggregator(24 * time.Hour),
		samples: []metrics.Sample{
			{Name: schedulerLatencyMetric},
			{Name: gcPauseMetric},
			{Name: goroutinesMetric},
			{Name: runnableMetric},
			{Name: waitingMetric},
			{Name: threadsMetric},
			{Name: gomaxprocsMetric},
			{Name: heapLiveMetric},
			{Name: cpuTotalMetric},
			{Name: cpuIdleMetric},
		},
	}
}

func (c *Collector) Service() string {
	if c == nil {
		return ""
	}
	return c.service
}

func (c *Collector) Role() string {
	if c == nil {
		return ""
	}
	return c.role
}

// Handler returns a handler for the protected metrics endpoint. The caller is
// responsible for exposing it only on a loopback or otherwise controlled
// listener.
func (c *Collector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/__aisys__/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := c.Write(w); err != nil {
			// Headers may already be committed. Keep the scrape failure visible in
			// the body without leaking implementation details.
			_, _ = io.WriteString(w, "# scrape_error 1\n")
		}
	})
}

// Record captures one scalar runtime sample for the in-memory hourly window.
// Durable persistence is handled by the optional jobs sampler.
func (c *Collector) Record() RuntimeSnapshot {
	sample := c.Snapshot()
	c.windows.Add(sample)
	return sample
}

// Windows returns the currently retained hourly aggregates for this process.
func (c *Collector) Windows() []WindowAggregate {
	return c.windows.Windows()
}

// Write emits stable, low-cardinality, cross-platform runtime metrics. Host
// RSS/FD readings are intentionally owned by host monitoring rather than this
// collector. Histogram quantiles are intentionally left to Prometheus;
// runtime/metrics exposes cumulative
// histograms and a single scrape must not be mistaken for a P95/P99 scalar.
func (c *Collector) Write(w io.Writer) error {
	c.mu.Lock()
	metrics.Read(c.samples)
	values := make([]metrics.Sample, len(c.samples))
	copy(values, c.samples)
	c.mu.Unlock()

	labels := fmt.Sprintf("service=\"%s\",role=\"%s\",runtimeKind=\"go\"", c.service, c.role)
	write := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	if err := write("# HELP juhe_ai_go_build_info Go runtime build information.\n# TYPE juhe_ai_go_build_info gauge\njuhe_ai_go_build_info{service=\"%s\",role=\"%s\",runtimeKind=\"go\",go_version=\"%s\"} 1\n", c.service, c.role, sanitizeLabelValue(runtime.Version())); err != nil {
		return err
	}
	if err := write("# HELP juhe_ai_go_process_uptime_seconds Process uptime in seconds.\n# TYPE juhe_ai_go_process_uptime_seconds gauge\njuhe_ai_go_process_uptime_seconds{%s} %.6f\n", labels, time.Since(c.started).Seconds()); err != nil {
		return err
	}
	// A collector can be mounted without the optional durable sampler (for
	// example on gateway). Refresh scalar gauges on every scrape so the
	// portable runtime surface remains useful even when persistence is
	// disabled. This path deliberately does not update Snapshot's CPU
	// baseline, preserving sampler/Record window semantics.
	latest := c.snapshot(false)
	for _, metric := range []struct {
		name  string
		help  string
		value uint64
	}{
		{"heap_alloc_bytes", "Current heap bytes allocated.", latest.HeapAllocBytes},
		{"heap_objects", "Current number of heap objects.", latest.HeapObjects},
	} {
		if err := write("# HELP juhe_ai_go_%s %s\n# TYPE juhe_ai_go_%s gauge\njuhe_ai_go_%s{%s} %d\n", metric.name, metric.help, metric.name, metric.name, labels, metric.value); err != nil {
			return err
		}
	}
	if latest.CPUSecondsTotal > 0 {
		if err := write("# HELP juhe_ai_go_runtime_cpu_seconds_total Estimated total CPU time available to the Go runtime in seconds.\n# TYPE juhe_ai_go_runtime_cpu_seconds_total counter\njuhe_ai_go_runtime_cpu_seconds_total{%s} %.6f\n", labels, latest.CPUSecondsTotal); err != nil {
			return err
		}
	}

	for _, sample := range values {
		switch sample.Name {
		case goroutinesMetric, runnableMetric, waitingMetric, threadsMetric, gomaxprocsMetric:
			if sample.Value.Kind() != metrics.KindUint64 {
				continue
			}
			name := map[string]string{goroutinesMetric: "goroutines", runnableMetric: "goroutines_runnable", waitingMetric: "goroutines_waiting", threadsMetric: "threads", gomaxprocsMetric: "gomaxprocs"}[sample.Name]
			if err := write("# HELP juhe_ai_go_%s Go runtime scheduler value.\n# TYPE juhe_ai_go_%s gauge\njuhe_ai_go_%s{%s} %d\n", name, name, name, labels, sample.Value.Uint64()); err != nil {
				return err
			}
		case schedulerLatencyMetric:
			if err := writeHistogram(w, "scheduler_latency_seconds", "Scheduler runnable latency.", labels, sample.Value); err != nil {
				return err
			}
		case gcPauseMetric:
			if err := writeHistogram(w, "gc_pause_seconds", "Stop-the-world GC pause latency.", labels, sample.Value); err != nil {
				return err
			}
		case heapLiveMetric:
			if sample.Value.Kind() != metrics.KindUint64 {
				continue
			}
			if err := write("# HELP juhe_ai_go_heap_live_bytes Current live heap bytes from runtime/metrics.\n# TYPE juhe_ai_go_heap_live_bytes gauge\njuhe_ai_go_heap_live_bytes{%s} %d\n", labels, sample.Value.Uint64()); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeHistogram(w io.Writer, name, help, labels string, value metrics.Value) error {
	if value.Kind() != metrics.KindFloat64Histogram {
		return nil
	}
	hist := value.Float64Histogram()
	if len(hist.Buckets) != len(hist.Counts)+1 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "# HELP juhe_ai_go_%s %s\n# TYPE juhe_ai_go_%s histogram\n", name, help, name); err != nil {
		return err
	}
	var cumulative uint64
	for i, count := range hist.Counts {
		cumulative += count
		le := "+Inf"
		if i+1 < len(hist.Buckets) {
			le = strconv.FormatFloat(hist.Buckets[i+1], 'g', -1, 64)
		}
		if _, err := fmt.Fprintf(w, "juhe_ai_go_%s_bucket{%s,le=\"%s\"} %d\n", name, labels, le, cumulative); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "juhe_ai_go_%s_count{%s} %d\n", name, labels, cumulative); err != nil {
		return err
	}
	return nil
}

func sanitizeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
