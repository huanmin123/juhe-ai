// Package modelcheckactive owns Gateway-local active-run coordination. Durable
// claim/fence state remains the source of truth for recovery; this registry
// only prevents duplicate work inside one Gateway process.
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

func NewRegistry() *Registry { return &Registry{active: make(map[string]*entry)} }

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
	if current := r.active[key]; current != nil {
		return Handle{}, false, current.summary
	}
	child, cancel := context.WithCancel(ctx)
	item := &entry{key: key, cancel: cancel, summary: summary}
	r.active[key] = item
	return Handle{registry: r, entry: item, ctx: child}, true, summary
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
	if h.registry.active[h.entry.key] == h.entry {
		delete(h.registry.active, h.entry.key)
	}
	h.entry.cancel()
}

func (h Handle) Update(patch Summary) bool {
	if h.registry == nil || h.entry == nil {
		return false
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if h.registry.active[h.entry.key] != h.entry {
		return false
	}
	applyPatch(&h.entry.summary, patch)
	return true
}

func (r *Registry) Stop(key string) (Summary, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.active[strings.TrimSpace(key)]
	if item == nil {
		return Summary{}, false
	}
	item.summary.StopRequest = true
	item.cancel()
	return item.summary, true
}

func (r *Registry) Get(key string) (Summary, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.active[strings.TrimSpace(key)]
	if item == nil {
		return Summary{}, false
	}
	return item.summary, true
}

func applyPatch(summary *Summary, patch Summary) {
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
