// Package modelcheckactive owns the Go model-check active-run lifecycle.
// It is process-local coordination only; durable input/claim state remains the
// source of truth for recovery and cross-process ownership.
package modelcheckactive

import (
	"context"
	"strings"
	"sync"
	"time"
)

type Summary struct {
	RunID       string    `json:"runId,omitempty"`
	TargetID    string    `json:"targetId,omitempty"`
	TargetName  string    `json:"targetName,omitempty"`
	Model       string    `json:"model,omitempty"`
	Profile     string    `json:"profile,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	StopRequest bool      `json:"stopRequested"`
}

type Registry struct {
	mu     sync.Mutex
	active map[string]*entry
}

type entry struct {
	key     string
	cancel  context.CancelFunc
	summary Summary
}

type Handle struct {
	registry *Registry
	entry    *entry
	ctx      context.Context
}

func NewRegistry() *Registry {
	return &Registry{active: make(map[string]*entry)}
}

func (r *Registry) TryStart(ctx context.Context, key string, summary Summary) (Handle, bool, Summary) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Handle{}, false, Summary{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = make(map[string]*entry)
	}
	if current := r.active[key]; current != nil {
		return Handle{}, false, cloneSummary(current.summary)
	}
	child, cancel := context.WithCancel(ctx)
	item := &entry{key: key, cancel: cancel, summary: summary}
	r.active[key] = item
	return Handle{registry: r, entry: item, ctx: child}, true, cloneSummary(summary)
}

func (h Handle) Context() context.Context {
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

func (h Handle) Finish() {
	if h.registry == nil || h.entry == nil {
		return
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if current := h.registry.active[h.entry.key]; current == h.entry {
		delete(h.registry.active, h.entry.key)
	}
	h.entry.cancel()
}

// Update changes the summary only while this handle still owns the active
// entry. It prevents a completed run from overwriting a newer run on the same
// scope key.
func (h Handle) Update(patch Summary) bool {
	if h.registry == nil || h.entry == nil {
		return false
	}
	return h.registry.updateHandle(h.entry, patch)
}

func (r *Registry) Stop(key string) (Summary, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Summary{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.active[key]
	if item == nil {
		return Summary{}, false
	}
	item.summary.StopRequest = true
	item.cancel()
	return cloneSummary(item.summary), true
}

func (r *Registry) Get(key string) (Summary, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Summary{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.active[key]
	if item == nil {
		return Summary{}, false
	}
	return cloneSummary(item.summary), true
}

func (r *Registry) Update(key string, patch Summary) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.active[key]
	if item == nil {
		return false
	}
	applySummaryPatch(&item.summary, patch)
	return true
}

func (r *Registry) updateHandle(expected *entry, patch Summary) bool {
	if expected == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[expected.key] != expected {
		return false
	}
	applySummaryPatch(&expected.summary, patch)
	return true
}

func applySummaryPatch(summary *Summary, patch Summary) {
	if summary == nil {
		return
	}
	if patch.RunID != "" {
		summary.RunID = patch.RunID
	}
	if patch.TargetID != "" {
		summary.TargetID = patch.TargetID
	}
	if patch.TargetName != "" {
		summary.TargetName = patch.TargetName
	}
	if patch.Model != "" {
		summary.Model = patch.Model
	}
	if patch.Profile != "" {
		summary.Profile = patch.Profile
	}
	if !patch.StartedAt.IsZero() {
		summary.StartedAt = patch.StartedAt
	}
	if patch.StopRequest {
		summary.StopRequest = true
	}
}

func cloneSummary(value Summary) Summary {
	return value
}
