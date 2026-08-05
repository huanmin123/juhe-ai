package gatewayclientipconcurrency

import (
	"fmt"
	"strings"
	"sync"
)

// LeaseHandoff preserves the Node switch ordering for a source high-concurrency
// client-IP slot: a target slot must already be acquired before the source is
// released. It does not acquire, retain, or release the target lease; the
// future outer owner remains responsible for attaching that target lease to
// the request terminal.
type LeaseHandoff struct {
	mu             sync.Mutex
	source         *Lease
	sourceScope    string
	closed         bool
	targetPrepared bool
}

// TargetPreparationHandoff is the only capability a post-source-lease target
// preparer receives. Its dynamic implementation is private, so the callback
// cannot type-assert it back to LeaseHandoff and close source prematurely.
type TargetPreparationHandoff interface {
	CompleteTargetPreparation(Input, Decision) error
}

type targetPreparationHandoff struct{ handoff *LeaseHandoff }

func (h targetPreparationHandoff) CompleteTargetPreparation(input Input, target Decision) error {
	if h.handoff == nil {
		return fmt.Errorf("client-IP lease handoff is required")
	}
	return h.handoff.CompleteTargetPreparation(input, target)
}

func NewLeaseHandoff(input Input, source Decision) (*LeaseHandoff, error) {
	if err := ValidateAcquiredDecisionForInput(input, source); err != nil {
		return nil, fmt.Errorf("source client-IP decision is not an acquired lease state: %w", err)
	}
	scope, err := handoffScope(input)
	if err != nil {
		return nil, err
	}
	return &LeaseHandoff{source: source.Lease, sourceScope: scope}, nil
}

// CompleteTargetPreparation releases source exactly once, but only after the
// caller has fully prepared and acquired target. A rejected or malformed
// target leaves source held so its current request can still terminate safely.
func (h *LeaseHandoff) CompleteTargetPreparation(input Input, target Decision) error {
	if h == nil {
		return fmt.Errorf("client-IP lease handoff is required")
	}
	if err := ValidateAcquiredDecisionForInput(input, target); err != nil {
		return fmt.Errorf("target client-IP decision is not an acquired lease state: %w", err)
	}
	targetScope, err := handoffScope(input)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("client-IP lease handoff is already complete")
	}
	if targetScope == h.sourceScope {
		return fmt.Errorf("target client-IP scope reuses source scope")
	}
	if target.Enabled && target.Lease == h.source {
		return fmt.Errorf("target client-IP lease reuses source lease")
	}
	if h.source != nil {
		h.source.Release()
	}
	h.targetPrepared = true
	h.closed = true
	return nil
}

func handoffScope(input Input) (string, error) {
	return scopeKey(input, strings.TrimSpace(input.ClientIP))
}

// TargetPrepared reports only a completed target handoff. Closing source for a
// terminal/error path never makes this true.
func (h *LeaseHandoff) TargetPrepared() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.targetPrepared
}

// TargetPreparation returns a narrow, unforgeable-at-the-type-boundary view
// for the post-source callback. It never exposes CloseSource or source lease.
func (h *LeaseHandoff) TargetPreparation() TargetPreparationHandoff {
	if h == nil {
		return nil
	}
	return targetPreparationHandoff{handoff: h}
}

// CloseSource releases source once when target preparation cannot continue or
// the request reaches a terminal before a target is adopted.
func (h *LeaseHandoff) CloseSource() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if h.source != nil {
		h.source.Release()
	}
	h.closed = true
}
