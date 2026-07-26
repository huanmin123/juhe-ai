// Package gatewaybodyadmission owns the process-local admission contract that
// a future gateway listener may apply before it reads a request body.
//
// This package deliberately has no HTTP, Redis, routing-store, or production
// listener dependency. A multi-instance production owner must replace the
// Controller with a shared coordination implementation instead of assuming
// this in-memory controller is cross-process safe.
package gatewaybodyadmission

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

type RouteStrategyMode string

const (
	RouteStrategyModeNormal RouteStrategyMode = "normal"
)

type SchedulingPreference string

const (
	SchedulingPreferenceSpeedFirst SchedulingPreference = "speed_first"
)

type GroupType string

const (
	GroupTypeHighConcurrency GroupType = "high_concurrency"
)

// RequestLane reuses the gateway's canonical lane enum. Keeping an alias
// prevents the pre-body admission predicate from drifting from later request
// planning when new lanes are introduced.
type RequestLane = protocolgateway.RequestLane

const (
	RequestLaneText  = protocolgateway.RequestLaneText
	RequestLaneImage = protocolgateway.RequestLaneImage
)

// Eligibility is the fully prepared, body-independent fact set for deciding
// whether a request may be held before its raw body is read. The listener must
// produce these facts without reading the body; a later model-to-image mapping
// is intentionally outside this pure pre-body seam.
type Eligibility struct {
	RouteStrategyMode    RouteStrategyMode
	SchedulingPreference SchedulingPreference
	GroupType            GroupType
	HasCandidate         bool
	RequestLane          RequestLane
}

// ShouldAdmit keeps speed-first body admission narrow. Images and every
// non-normal route skip this controller even when a caller happens to provide
// a capacity value.
func ShouldAdmit(value Eligibility) bool {
	return value.RouteStrategyMode == RouteStrategyModeNormal &&
		value.SchedulingPreference == SchedulingPreferenceSpeedFirst &&
		value.GroupType == GroupTypeHighConcurrency &&
		value.HasCandidate &&
		value.RequestLane == RequestLaneText
}

// CapacityCandidate contains only the identity necessary to deduplicate an
// account's effective upstream concurrency. CredentialSourceID is preferred
// when present because authorized instances sharing one physical credential
// must not multiply that credential's capacity.
type CapacityCandidate struct {
	ResourceID         string
	CredentialSourceID string
	ConcurrencyLimit   int
}

// EffectiveCapacity returns the sum of one positive limit per physical source.
// Duplicate candidates for a source use the lowest positive limit, matching a
// shared credential's most restrictive account view. Invalid source identities
// and non-positive limits are excluded rather than treated as unbounded
// capacity. The total saturates at the platform int maximum.
func EffectiveCapacity(candidates []CapacityCandidate) int {
	limits := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		sourceID := capacitySourceID(candidate)
		if sourceID == "" || candidate.ConcurrencyLimit <= 0 {
			continue
		}
		if current, exists := limits[sourceID]; !exists || candidate.ConcurrencyLimit < current {
			limits[sourceID] = candidate.ConcurrencyLimit
		}
	}

	maximum := maxInt()
	total := 0
	for _, limit := range limits {
		if limit > maximum-total {
			return maximum
		}
		total += limit
	}
	return total
}

func capacitySourceID(candidate CapacityCandidate) string {
	if sourceID := safeIdentity(candidate.CredentialSourceID); sourceID != "" {
		return sourceID
	}
	return safeIdentity(candidate.ResourceID)
}

type Scope struct {
	SystemAccountID string
	RouteStrategyID string
	GroupID         string
}

// AcquireInput describes one pre-body admission request. Capacity is always
// normalized to at least one slot, mirroring the existing safe fallback for a
// malformed per-account concurrency value. A caller with no valid candidates
// must skip this package via ShouldAdmit rather than pass zero candidates as
// unconstrained capacity.
type AcquireInput struct {
	Scope               Scope
	APIKeyID            string
	Capacity            int
	MaxQueueWait        time.Duration
	MaxQueueSize        int
	PerAPIKeyQueueLimit int
}

type RejectReason string

const (
	RejectQueueDisabled   RejectReason = "queue_disabled"
	RejectQueueFull       RejectReason = "queue_full"
	RejectAPIKeyQueueFull RejectReason = "api_key_queue_full"
	RejectTimeout         RejectReason = "timeout"
	RejectCanceled        RejectReason = "canceled"
)

// Decision represents either an acquired lease or an expected admission
// rejection. Input validation and a nil context return an error; queue limits,
// timeout, and cancellation are normal admission outcomes.
type Decision struct {
	Lease  *Lease
	Reason RejectReason
	Waited time.Duration
}

func (d Decision) Acquired() bool { return d.Lease != nil }

// Lease occupies one scope slot until Release. Release is safe to call more
// than once and is intentionally the only way to return an acquired slot.
type Lease struct {
	controller *Controller
	scope      Scope
	state      *scopeState
	once       sync.Once
}

func (l *Lease) Release() {
	if l == nil || l.controller == nil || l.state == nil {
		return
	}
	l.once.Do(func() {
		l.controller.release(l.scope, l.state)
	})
}

// Controller is a process-local, per-scope FIFO admission controller. It is
// deliberately explicit so the future listener owns its lifecycle; NewController
// creates no global state.
type Controller struct {
	mu     sync.Mutex
	states map[Scope]*scopeState
}

type scopeState struct {
	capacity       int
	active         int
	queue          []*waiter
	perAPIKeyQueue map[string]int
}

type waiter struct {
	apiKeyID string
	started  time.Time
	result   chan Decision
}

func NewController() *Controller {
	return &Controller{states: make(map[Scope]*scopeState)}
}

// Acquire reserves a per-scope slot or waits in FIFO order. A canceled context
// and the configured maximum wait remove the waiter under the same lock used by
// Release, so neither outcome can strand later waiters. A successful decision
// always owns a lease, including when ctx is canceled immediately after the
// controller grants that lease; callers must release every successful lease.
func (c *Controller) Acquire(ctx context.Context, input AcquireInput) (Decision, error) {
	if c == nil {
		return Decision{}, fmt.Errorf("gateway body admission controller is required")
	}
	if ctx == nil {
		return Decision{}, fmt.Errorf("gateway body admission context is required")
	}
	var err error
	input, err = normalizeAcquireInput(input)
	if err != nil {
		return Decision{}, err
	}
	started := time.Now()
	if ctx.Err() != nil {
		return Decision{Reason: RejectCanceled}, nil
	}

	c.mu.Lock()
	if c.states == nil {
		c.states = make(map[Scope]*scopeState)
	}
	state := c.states[input.Scope]
	if state == nil {
		state = &scopeState{perAPIKeyQueue: make(map[string]int)}
		c.states[input.Scope] = state
	}
	state.capacity = normalizedPositive(input.Capacity)
	// Capacity may have increased since an earlier request. Wake old waiters
	// before considering this request so a new arrival never jumps the FIFO.
	c.pumpLocked(input.Scope, state)
	if len(state.queue) == 0 && state.active < state.capacity {
		state.active++
		lease := c.newLease(input.Scope, state)
		c.mu.Unlock()
		return Decision{Lease: lease}, nil
	}
	if input.MaxQueueWait <= 0 {
		c.cleanupLocked(input.Scope, state)
		c.mu.Unlock()
		return Decision{Reason: RejectQueueDisabled}, nil
	}
	if len(state.queue) >= normalizedPositive(input.MaxQueueSize) {
		c.cleanupLocked(input.Scope, state)
		c.mu.Unlock()
		return Decision{Reason: RejectQueueFull}, nil
	}
	if state.perAPIKeyQueue[input.APIKeyID] >= normalizedPositive(input.PerAPIKeyQueueLimit) {
		c.cleanupLocked(input.Scope, state)
		c.mu.Unlock()
		return Decision{Reason: RejectAPIKeyQueueFull}, nil
	}
	waiter := &waiter{apiKeyID: input.APIKeyID, started: started, result: make(chan Decision, 1)}
	state.queue = append(state.queue, waiter)
	state.perAPIKeyQueue[input.APIKeyID]++
	c.mu.Unlock()

	timer := time.NewTimer(input.MaxQueueWait)
	defer timer.Stop()
	select {
	case decision := <-waiter.result:
		return decision, nil
	case <-ctx.Done():
		if c.cancelWaiter(input.Scope, state, waiter) {
			return Decision{Reason: RejectCanceled, Waited: elapsedSince(started)}, nil
		}
		return <-waiter.result, nil
	case <-timer.C:
		if c.cancelWaiter(input.Scope, state, waiter) {
			return Decision{Reason: RejectTimeout, Waited: elapsedSince(started)}, nil
		}
		return <-waiter.result, nil
	}
}

func (c *Controller) newLease(scope Scope, state *scopeState) *Lease {
	return &Lease{controller: c, scope: scope, state: state}
}

func (c *Controller) release(scope Scope, state *scopeState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state.active <= 0 {
		return
	}
	state.active--
	c.pumpLocked(scope, state)
	c.cleanupLocked(scope, state)
}

func (c *Controller) cancelWaiter(scope Scope, state *scopeState, target *waiter) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, candidate := range state.queue {
		if candidate != target {
			continue
		}
		c.removeWaiterLocked(state, index)
		c.pumpLocked(scope, state)
		c.cleanupLocked(scope, state)
		return true
	}
	return false
}

func (c *Controller) pumpLocked(scope Scope, state *scopeState) {
	for state.active < state.capacity && len(state.queue) > 0 {
		waiter := state.queue[0]
		c.removeWaiterLocked(state, 0)
		state.active++
		waiter.result <- Decision{Lease: c.newLease(scope, state), Waited: elapsedSince(waiter.started)}
	}
}

func (c *Controller) removeWaiterLocked(state *scopeState, index int) {
	waiter := state.queue[index]
	copy(state.queue[index:], state.queue[index+1:])
	state.queue[len(state.queue)-1] = nil
	state.queue = state.queue[:len(state.queue)-1]
	if state.perAPIKeyQueue[waiter.apiKeyID] <= 1 {
		delete(state.perAPIKeyQueue, waiter.apiKeyID)
		return
	}
	state.perAPIKeyQueue[waiter.apiKeyID]--
}

func (c *Controller) cleanupLocked(scope Scope, state *scopeState) {
	if state.active == 0 && len(state.queue) == 0 && c.states[scope] == state {
		delete(c.states, scope)
	}
}

func normalizeAcquireInput(input AcquireInput) (AcquireInput, error) {
	input.Scope.SystemAccountID = safeIdentity(input.Scope.SystemAccountID)
	input.Scope.RouteStrategyID = safeIdentity(input.Scope.RouteStrategyID)
	input.Scope.GroupID = safeIdentity(input.Scope.GroupID)
	input.APIKeyID = safeIdentity(input.APIKeyID)
	if input.Scope.SystemAccountID == "" || input.Scope.RouteStrategyID == "" || input.Scope.GroupID == "" {
		return AcquireInput{}, fmt.Errorf("gateway body admission scope is required")
	}
	if input.APIKeyID == "" {
		return AcquireInput{}, fmt.Errorf("gateway body admission API key is required")
	}
	return input, nil
}

func safeIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func normalizedPositive(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

func elapsedSince(started time.Time) time.Duration {
	if elapsed := time.Since(started); elapsed > 0 {
		return elapsed
	}
	return 0
}

func maxInt() int { return int(^uint(0) >> 1) }
