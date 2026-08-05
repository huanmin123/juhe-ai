// Package gatewayclientipconcurrency mirrors Node's local client-IP
// concurrency driver for high-concurrency groups. It is unregistered and
// request-local callers retain final response and listener ownership.
package gatewayclientipconcurrency

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
)

type RejectReason string

const (
	RejectLimitReached  RejectReason = "limit_reached"
	RejectQueueDisabled RejectReason = "queue_disabled"
	RejectQueueFull     RejectReason = "queue_full"
	RejectTimeout       RejectReason = "timeout"
	RejectAborted       RejectReason = "aborted"
)

type Input struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	Policy          *groupscheduling.Policy
}

// Decision deliberately preserves Node's observable decision facts. Lease is
// non-nil only when a client-IP slot was actually acquired.
type Decision struct {
	Enabled                bool
	Acquired               bool
	Reason                 RejectReason
	Current                int
	Limit                  int
	WaitedMS               int
	Queued                 bool
	QueueSizeBeforeAcquire int
	QueueSize              int
	Lease                  *Lease
}

// ValidateAcquiredDecisionForInput proves that an acquired decision belongs to
// exactly one client-IP concurrency input. It is used when a route owner
// prepares a target group before handing that group's lease to execution.
func ValidateAcquiredDecisionForInput(input Input, decision Decision) error {
	policy, err := normalizedPolicy(input.Policy)
	if err != nil {
		return err
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" || policy.ClientIPConcurrencyLimit <= 0 {
		if !decision.Acquired || decision.Enabled || decision.Lease != nil {
			return fmt.Errorf("disabled client-IP decision is not an acquired no-op")
		}
		return nil
	}
	scope, err := scopeKey(input, clientIP)
	if err != nil {
		return err
	}
	if !decision.Enabled || !decision.Acquired || decision.Lease == nil || !decision.Lease.activeForHandoff() || decision.Lease.key != scope ||
		decision.Lease.policy != clientIPPolicyFingerprint(policy) || decision.Limit != policy.ClientIPConcurrencyLimit {
		return fmt.Errorf("client-IP decision is not an active acquired lease for scope")
	}
	return nil
}

type Service struct {
	mu     sync.Mutex
	states map[string]*scopeState
	now    func() time.Time
}

type scopeState struct {
	key    string
	limit  int
	policy groupscheduling.Policy
	used   int
	queue  []*waiter
}

type waiter struct {
	key       string
	state     *scopeState
	limit     int
	enqueued  time.Time
	result    chan Decision
	completed bool
}

type Lease struct {
	service  *Service
	key      string
	policy   string
	once     sync.Once
	mu       sync.Mutex
	issued   bool
	released bool
	release  func()
}

type Snapshot struct {
	Key       string
	Current   int
	QueueSize int
}

func NewService(now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{states: make(map[string]*scopeState), now: now}
}

// Acquire follows Node's local driver. A nil policy uses the same default
// high-concurrency policy; a present policy must be a complete valid snapshot.
func (s *Service) Acquire(ctx context.Context, input Input) (Decision, error) {
	if s == nil {
		return Decision{}, fmt.Errorf("client IP concurrency service is not configured")
	}
	if ctx == nil {
		return Decision{}, fmt.Errorf("client IP concurrency context is required")
	}
	policy, err := normalizedPolicy(input.Policy)
	if err != nil {
		return Decision{}, err
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" || policy.ClientIPConcurrencyLimit <= 0 {
		return Decision{Acquired: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return rejected(RejectAborted, 0, policy.ClientIPConcurrencyLimit, 0, 0), nil
	}
	key, err := scopeKey(input, clientIP)
	if err != nil {
		return Decision{}, err
	}
	now := s.now().UTC()

	s.mu.Lock()
	state := s.states[key]
	if state == nil {
		state = &scopeState{key: key}
		s.states[key] = state
	}
	state.limit = policy.ClientIPConcurrencyLimit
	state.policy = policy
	if state.used < state.limit {
		queueSize := len(state.queue)
		state.used++
		decision := acquired(s, state, policy, now, 0, false, queueSize)
		s.mu.Unlock()
		return decision, nil
	}
	if policy.ClientIPConcurrencyOverflowMode != "queue" {
		decision := rejected(RejectLimitReached, state.used, state.limit, 0, len(state.queue))
		s.mu.Unlock()
		return decision, nil
	}
	if policy.MaxQueueWaitMs <= 0 {
		decision := rejected(RejectQueueDisabled, state.used, state.limit, 0, len(state.queue))
		s.mu.Unlock()
		return decision, nil
	}
	if len(state.queue) >= policy.PerAPIKeyQueueLimit {
		decision := rejected(RejectQueueFull, state.used, state.limit, 0, len(state.queue))
		s.mu.Unlock()
		return decision, nil
	}
	waiter := &waiter{key: key, state: state, limit: state.limit, enqueued: now, result: make(chan Decision, 1)}
	state.queue = append(state.queue, waiter)
	s.mu.Unlock()

	timer := time.NewTimer(time.Duration(policy.MaxQueueWaitMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case result := <-waiter.result:
		return result, nil
	case <-ctx.Done():
		return s.completeWaiter(waiter, RejectAborted), nil
	case <-timer.C:
		return s.completeWaiter(waiter, RejectTimeout), nil
	}
}

// Release is safe to call repeatedly. It wakes at most one FIFO waiter, as
// Node's local driver does, and has no external side effect to suppress.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.mu.Lock()
		l.released = true
		release := l.release
		service, key := l.service, l.key
		l.mu.Unlock()
		if release != nil {
			release()
			return
		}
		if service != nil {
			service.release(key)
		}
	})
}

// activeForHandoff is intentionally private: only this driver can attest that
// a lease was minted by an acquire decision and has not yet been released.
func (l *Lease) activeForHandoff() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.issued && !l.released
}

func (s *Service) Snapshot() []Snapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Snapshot, 0, len(s.states))
	for _, state := range s.states {
		result = append(result, Snapshot{Key: state.key, Current: state.used, QueueSize: len(state.queue)})
	}
	return result
}

func (s *Service) completeWaiter(waiter *waiter, reason RejectReason) Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if waiter.completed {
		return <-waiter.result
	}
	decision := rejected(reason, waiter.state.used, waiter.limit, elapsedMS(s.now(), waiter.enqueued), len(waiter.state.queue))
	s.completeLocked(waiter, decision)
	return decision
}

func (s *Service) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil {
		return
	}
	if state.used > 0 {
		state.used--
	}
	for state.used < state.limit && len(state.queue) > 0 {
		waiter := state.queue[0]
		if waiter.completed {
			state.queue = state.queue[1:]
			continue
		}
		state.used++
		decision := acquired(s, state, state.policy, s.now().UTC(), elapsedMS(s.now(), waiter.enqueued), true, len(state.queue))
		s.completeLocked(waiter, decision)
		break
	}
	s.cleanupLocked(state)
}

func (s *Service) completeLocked(waiter *waiter, decision Decision) {
	if waiter.completed {
		return
	}
	waiter.completed = true
	for index, item := range waiter.state.queue {
		if item == waiter {
			waiter.state.queue = append(waiter.state.queue[:index], waiter.state.queue[index+1:]...)
			break
		}
	}
	waiter.result <- decision
	s.cleanupLocked(waiter.state)
}

func (s *Service) cleanupLocked(state *scopeState) {
	if state.used == 0 && len(state.queue) == 0 {
		delete(s.states, state.key)
	}
}

func acquired(service *Service, state *scopeState, policy groupscheduling.Policy, now time.Time, waitedMS int, queued bool, queueSizeBeforeAcquire int) Decision {
	return Decision{Enabled: true, Acquired: true, Current: state.used, Limit: state.limit, WaitedMS: waitedMS, Queued: queued, QueueSizeBeforeAcquire: queueSizeBeforeAcquire, Lease: &Lease{service: service, key: state.key, policy: clientIPPolicyFingerprint(policy), issued: true}}
}

func rejected(reason RejectReason, current, limit, waitedMS, queueSize int) Decision {
	return Decision{Enabled: true, Reason: reason, Current: current, Limit: limit, WaitedMS: waitedMS, QueueSize: queueSize}
}

func normalizedPolicy(value *groupscheduling.Policy) (groupscheduling.Policy, error) {
	if value == nil {
		return groupscheduling.DefaultHighConcurrencyPolicy(), nil
	}
	if err := groupscheduling.Validate(*value); err != nil {
		return groupscheduling.Policy{}, fmt.Errorf("client IP concurrency policy is invalid: %w", err)
	}
	return *value, nil
}

func scopeKey(input Input, clientIP string) (string, error) {
	systemAccountID, groupID := strings.TrimSpace(input.SystemAccountID), strings.TrimSpace(input.GroupID)
	if systemAccountID == "" || groupID == "" {
		return "", fmt.Errorf("client IP concurrency group scope is missing")
	}
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if apiKeyID == "" {
		apiKeyID = "internal"
	}
	return systemAccountID + ":" + groupID + ":" + apiKeyID + ":" + clientIP, nil
}

func clientIPPolicyFingerprint(policy groupscheduling.Policy) string {
	return fmt.Sprintf("%d|%s|%d|%d", policy.ClientIPConcurrencyLimit, policy.ClientIPConcurrencyOverflowMode, policy.MaxQueueWaitMs, policy.PerAPIKeyQueueLimit)
}

func elapsedMS(now, then time.Time) int {
	if now.Before(then) {
		return 0
	}
	return int(now.Sub(then) / time.Millisecond)
}
