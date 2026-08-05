// Package gatewayfallbackruntimedegradation mirrors Node's process-local
// runtime degradation ordering for cross-group fallback candidates. It owns no
// Redis state, route cursor, account mutation, lease, listener, or upstream
// dispatch.
package gatewayfallbackruntimedegradation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
)

const (
	RuntimeStateDriverMemory = "memory"
	RuntimeStateDriverRedis  = "redis"

	degradationWindow               = 5 * time.Minute
	degradationActivationFailures   = 2
	degradationMinObservationWindow = time.Minute
)

type Options struct {
	// RuntimeStateDriver is the validated Node-compatible runtime-state driver.
	// Redis intentionally disables this process-local state; it never falls
	// back to memory.
	RuntimeStateDriver string
	Now                func() time.Time
}

type Availability struct {
	Degraded     bool
	Reason       string
	Since        time.Time
	FailureCount int
}

type FailureInput struct {
	Window                          gatewaycandidatewindow.Window
	Candidate                       gatewaycandidatewindow.Candidate
	Reason                          string
	SuppressionStateKnown           bool
	SuppressionAdvancesFailureCount bool
}

// SuccessInput carries the same account-runtime facts used when a degradation
// was recorded. A successful attempt owner clears only this local adapter's
// degradation; other runtime state remains owned by its respective adapter.
type SuccessInput struct {
	Window    gatewaycandidatewindow.Window
	Candidate gatewaycandidatewindow.Candidate
}

type degradation struct {
	accountID      string
	reason         string
	since          time.Time
	firstFailureAt time.Time
	lastFailureAt  time.Time
	failureCount   int
}

type Service struct {
	driver string
	now    func() time.Time

	mu           sync.Mutex
	degradations map[string]degradation
}

func NewService(options Options) (*Service, error) {
	driver := strings.ToLower(strings.TrimSpace(options.RuntimeStateDriver))
	if driver != RuntimeStateDriverMemory && driver != RuntimeStateDriverRedis {
		return nil, fmt.Errorf("unsupported fallback runtime state driver %q", strings.TrimSpace(options.RuntimeStateDriver))
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{driver: driver, now: now, degradations: make(map[string]degradation)}, nil
}

// ObserveGatewayFailure records a process-local failure only for the memory
// driver. The future attempt owner must call it only for the same Node failure
// class that updates local degradation; this package does not infer it from an
// HTTP status or an attempt-loop result.
func (s *Service) ObserveGatewayFailure(input FailureInput) (Availability, error) {
	if s == nil {
		return Availability{}, fmt.Errorf("fallback runtime degradation service is not configured")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return Availability{}, fmt.Errorf("fallback runtime degradation reason is required")
	}
	if s.driver == RuntimeStateDriverRedis {
		s.clearLocalState()
		return Availability{Reason: reason, Since: s.now().UTC()}, nil
	}
	if !input.SuppressionStateKnown {
		return Availability{}, fmt.Errorf("fallback runtime degradation suppression state is required")
	}
	key, accountID, err := runtimeKey(input.Window, input.Candidate)
	if err != nil {
		return Availability{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	current, found := s.degradations[key]
	withinWindow := found && now.Sub(current.firstFailureAt) <= degradationWindow
	count := 1
	firstFailureAt := now
	since := now
	if withinWindow {
		if input.SuppressionAdvancesFailureCount {
			count = current.failureCount + 1
		} else if current.failureCount > count {
			count = current.failureCount
		}
		firstFailureAt = current.firstFailureAt
		since = current.since
	}
	next := degradation{
		accountID: accountID, reason: reason, since: since,
		firstFailureAt: firstFailureAt, lastFailureAt: now, failureCount: count,
	}
	s.degradations[key] = next
	return availability(next), nil
}

// ActivateFromProbe mirrors Node's background-probe activation path. It is
// deliberately explicit: callers supply the observed failure count and since
// fact rather than manufacturing degradation from a candidate selection.
func (s *Service) ActivateFromProbe(input FailureInput, since time.Time, failureCount int) (Availability, error) {
	if s == nil {
		return Availability{}, fmt.Errorf("fallback runtime degradation service is not configured")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || since.IsZero() || failureCount < degradationActivationFailures {
		return Availability{}, fmt.Errorf("fallback runtime degradation probe facts are invalid")
	}
	if s.driver == RuntimeStateDriverRedis {
		s.clearLocalState()
		return Availability{Reason: reason, Since: since.UTC()}, nil
	}
	key, accountID, err := runtimeKey(input.Window, input.Candidate)
	if err != nil {
		return Availability{}, err
	}
	now := s.now().UTC()
	firstFailureAt := since.UTC()
	if latestAllowed := now.Add(-degradationMinObservationWindow); firstFailureAt.After(latestAllowed) {
		firstFailureAt = latestAllowed
	}
	next := degradation{
		accountID: accountID, reason: reason, since: since.UTC(),
		firstFailureAt: firstFailureAt, lastFailureAt: now, failureCount: failureCount,
	}
	s.mu.Lock()
	s.degradations[key] = next
	s.mu.Unlock()
	return availability(next), nil
}

// ClearGatewaySuccess mirrors Node's targeted local degradation cleanup after
// a successful account attempt. It is intentionally explicit because this
// package does not infer success from transport or response events.
func (s *Service) ClearGatewaySuccess(input SuccessInput) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("fallback runtime degradation service is not configured")
	}
	key, _, err := runtimeKey(input.Window, input.Candidate)
	if err != nil {
		return false, err
	}
	if s.driver == RuntimeStateDriverRedis {
		s.clearLocalState()
		return false, nil
	}
	s.mu.Lock()
	_, cleared := s.degradations[key]
	delete(s.degradations, key)
	s.mu.Unlock()
	return cleared, nil
}

// OrderFallbackRuntimeDegradation implements gatewayfallbackpolicy's strict
// permutation contract. It moves only active degraded candidates behind normal
// candidates. When all are degraded it preserves the input order and reports
// the Node bypass fact for a runtime_degraded source fallback.
func (s *Service) OrderFallbackRuntimeDegradation(_ context.Context, input gatewayfallbackpolicy.RuntimeDegradationInput) (gatewayfallbackpolicy.RuntimeDegradationResult, error) {
	if s == nil {
		return gatewayfallbackpolicy.RuntimeDegradationResult{}, fmt.Errorf("fallback runtime degradation service is not configured")
	}
	if s.driver == RuntimeStateDriverRedis {
		s.clearLocalState()
		return orderedIDs(input.Candidates), nil
	}
	now := s.now().UTC()
	s.mu.Lock()
	s.cleanupLocked(now)
	degradedByKey := make(map[string]degradation, len(s.degradations))
	for key, value := range s.degradations {
		degradedByKey[key] = value
	}
	s.mu.Unlock()

	normal := make([]string, 0, len(input.Candidates))
	degraded := make([]string, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		key, _, err := runtimeKey(input.Window, candidate)
		if err != nil {
			return gatewayfallbackpolicy.RuntimeDegradationResult{}, err
		}
		if value, found := degradedByKey[key]; found && active(value) {
			degraded = append(degraded, strings.TrimSpace(candidate.Projection.AccountID))
			continue
		}
		normal = append(normal, strings.TrimSpace(candidate.Projection.AccountID))
	}
	if len(degraded) == 0 {
		return gatewayfallbackpolicy.RuntimeDegradationResult{CandidateAccountIDs: normal}, nil
	}
	if len(normal) == 0 {
		return gatewayfallbackpolicy.RuntimeDegradationResult{
			CandidateAccountIDs: degraded, BypassedAllDegraded: true,
		}, nil
	}
	return gatewayfallbackpolicy.RuntimeDegradationResult{
		CandidateAccountIDs: append(normal, degraded...),
	}, nil
}

func (s *Service) clearLocalState() {
	s.mu.Lock()
	clear(s.degradations)
	s.mu.Unlock()
}

func (s *Service) cleanupLocked(now time.Time) {
	for key, value := range s.degradations {
		if !active(value) && now.Sub(value.firstFailureAt) > degradationWindow {
			delete(s.degradations, key)
		}
	}
}

func availability(value degradation) Availability {
	return Availability{
		Degraded: active(value), Reason: value.reason, Since: value.since.UTC(), FailureCount: value.failureCount,
	}
}

func active(value degradation) bool {
	return value.failureCount >= degradationActivationFailures &&
		value.lastFailureAt.Sub(value.firstFailureAt) >= degradationMinObservationWindow
}

func orderedIDs(candidates []gatewaycandidatewindow.Candidate) gatewayfallbackpolicy.RuntimeDegradationResult {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, strings.TrimSpace(candidate.Projection.AccountID))
	}
	return gatewayfallbackpolicy.RuntimeDegradationResult{CandidateAccountIDs: ids}
}

// runtimeKey follows Node gatewayAccountRuntimeKey: account authorization
// instances are local to the caller/group/account-authorization binding;
// ordinary accounts use their account ID directly.
func runtimeKey(window gatewaycandidatewindow.Window, candidate gatewaycandidatewindow.Candidate) (string, string, error) {
	projection := candidate.Projection
	accountID := strings.TrimSpace(projection.AccountID)
	if accountID == "" {
		return "", "", fmt.Errorf("fallback runtime degradation candidate has no account id")
	}
	authorizationID := strings.TrimSpace(projection.AuthorizationID)
	sourceAccountID := strings.TrimSpace(projection.AuthorizationSourceAccountID)
	ownerSystemAccountID := strings.TrimSpace(projection.AuthorizationOwnerSystemAccountID)
	bindingAuthorizationID := strings.TrimSpace(projection.AccountAuthorizationID)
	if authorizationID == "" && sourceAccountID == "" && ownerSystemAccountID == "" && bindingAuthorizationID == "" {
		return accountID, accountID, nil
	}
	if authorizationID == "" || sourceAccountID == "" || ownerSystemAccountID == "" || bindingAuthorizationID == "" {
		return "", "", fmt.Errorf("fallback runtime degradation authorized account facts are incomplete")
	}
	caller := strings.TrimSpace(window.Access.CallerSystemAccountID)
	groupID := strings.TrimSpace(window.Access.GroupID)
	if caller == "" || groupID == "" || bindingAuthorizationID == "" || bindingAuthorizationID != authorizationID {
		return "", "", fmt.Errorf("fallback runtime degradation authorized account binding facts are incomplete")
	}
	return accountID + ":authorized:" + caller + ":" + groupID + ":" + authorizationID, accountID, nil
}

var _ gatewayfallbackpolicy.RuntimeDegradationOrderer = (*Service)(nil)
