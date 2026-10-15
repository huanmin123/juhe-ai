// Package prommetrics is the only wrapper seam this module keeps around
// github.com/prometheus/client_golang: feature packages construct counters,
// gauges and histograms through the local interfaces below and hold the
// opaque Registry, so no consumer file ever imports the prometheus family
// directly. The package owns exactly one registry shape: a pull-only
// registry with the standard Go and process collectors and an
// promhttp.HandlerFor exposition handler.
package prommetrics

import (
	"bytes"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// Registry is the opaque process registry handle. The wrapped
// *prometheus.Registry carries the standard Go collector and process
// collector, so go_*/process_* series are exposed without extra wiring.
type Registry struct {
	inner *prometheus.Registry
}

// NewRegistry builds the pull-only registry: the standard Go collector and
// process collector are registered explicitly (prometheus.NewRegistry is
// vanilla and would otherwise expose nothing), nothing push-related, no
// third-party collectors.
func NewRegistry() *Registry {
	registry := &Registry{inner: prometheus.NewRegistry()}
	registry.inner.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return registry
}

// Handler returns the exposition handler for the registry
// (promhttp.HandlerFor over the same registry). The caller decides which
// listener may serve it.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.inner, promhttp.HandlerOpts{ErrorHandling: promhttp.HTTPErrorOnError})
}

// GatherText renders the current exposition in the classic text format
// (text/plain; version=0.0.4). Diagnostics and tests use it to make
// content-level assertions without importing the prometheus family.
func (r *Registry) GatherText() (string, error) {
	families, err := r.inner.Gather()
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := expfmt.NewEncoder(&buffer, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			return "", err
		}
	}
	return buffer.String(), nil
}

// Counter is the incremental-only child handle (a label set of a vec or a
// single labelled counter).
type Counter interface {
	Inc()
	Add(delta float64)
}

// Gauge is the bidirectional child handle. Series stay resident at zero:
// a created gauge child keeps being exported at 0 (no per-scrape deletion).
type Gauge interface {
	Inc()
	Dec()
	Add(delta float64)
	Sub(delta float64)
	Set(value float64)
}

// Observer is the write-only histogram observation handle.
type Observer interface {
	Observe(value float64)
}

// CounterVec hands out Counter children per label-value set.
type CounterVec interface {
	WithLabelValues(labelValues ...string) Counter
}

// GaugeVec hands out Gauge children per label-value set.
type GaugeVec interface {
	WithLabelValues(labelValues ...string) Gauge
}

// HistogramVec hands out Observer children per label-value set.
type HistogramVec interface {
	WithLabelValues(labelValues ...string) Observer
}

// NewCounterVec registers a labelled counter family on the registry. A nil
// or empty constLabels map pins no fixed labels.
func NewCounterVec(registry *Registry, name, help string, constLabels map[string]string, labelNames []string) CounterVec {
	options := prometheus.CounterOpts{Name: name, Help: help}
	if len(constLabels) > 0 {
		options.ConstLabels = prometheus.Labels(constLabels)
	}
	vec := prometheus.NewCounterVec(options, labelNames)
	registry.inner.MustRegister(vec)
	return counterVec{inner: vec}
}

// NewGaugeVec registers a labelled gauge family on the registry. A nil or
// empty constLabels map pins no fixed labels.
func NewGaugeVec(registry *Registry, name, help string, constLabels map[string]string, labelNames []string) GaugeVec {
	options := prometheus.GaugeOpts{Name: name, Help: help}
	if len(constLabels) > 0 {
		options.ConstLabels = prometheus.Labels(constLabels)
	}
	vec := prometheus.NewGaugeVec(options, labelNames)
	registry.inner.MustRegister(vec)
	return gaugeVec{inner: vec}
}

// NewHistogramVec registers a labelled histogram family on the registry. A
// nil buckets slice keeps the client_golang default buckets; the +Inf bucket
// and the _sum/_count series are emitted by the library. A nil or empty
// constLabels map pins no fixed labels.
func NewHistogramVec(registry *Registry, name, help string, constLabels map[string]string, labelNames []string, buckets []float64) HistogramVec {
	options := prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}
	if len(constLabels) > 0 {
		options.ConstLabels = prometheus.Labels(constLabels)
	}
	vec := prometheus.NewHistogramVec(options, labelNames)
	registry.inner.MustRegister(vec)
	return histogramVec{inner: vec}
}

// NewCounterFunc registers a single-series counter whose value is read
// through fn at scrape time (the const-collector equivalent of a callback
// injected by the composition root). constLabels pins the fixed label set
// (for example the service label); fn is invoked on every scrape.
func NewCounterFunc(registry *Registry, name, help string, constLabels map[string]string, fn func() float64) {
	options := prometheus.CounterOpts{Name: name, Help: help}
	if len(constLabels) > 0 {
		options.ConstLabels = prometheus.Labels(constLabels)
	}
	registry.inner.MustRegister(prometheus.NewCounterFunc(options, fn))
}

type counterVec struct {
	inner *prometheus.CounterVec
}

func (v counterVec) WithLabelValues(labelValues ...string) Counter {
	return counter{inner: v.inner.WithLabelValues(labelValues...)}
}

type counter struct {
	inner prometheus.Counter
}

func (c counter) Inc() { c.inner.Inc() }

func (c counter) Add(delta float64) { c.inner.Add(delta) }

type gaugeVec struct {
	inner *prometheus.GaugeVec
}

func (v gaugeVec) WithLabelValues(labelValues ...string) Gauge {
	return gauge{inner: v.inner.WithLabelValues(labelValues...)}
}

type gauge struct {
	inner prometheus.Gauge
}

func (g gauge) Inc() { g.inner.Inc() }

func (g gauge) Dec() { g.inner.Dec() }

func (g gauge) Add(delta float64) { g.inner.Add(delta) }

func (g gauge) Sub(delta float64) { g.inner.Sub(delta) }

func (g gauge) Set(value float64) { g.inner.Set(value) }

type histogramVec struct {
	inner *prometheus.HistogramVec
}

func (v histogramVec) WithLabelValues(labelValues ...string) Observer {
	return observer{inner: v.inner.WithLabelValues(labelValues...)}
}

type observer struct {
	inner prometheus.Observer
}

func (o observer) Observe(value float64) { o.inner.Observe(value) }
