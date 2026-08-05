package gatewayclientipconcurrency

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// MaxPendingFailures bounds the request-local failure facts retained by one
// tracker. It is intentionally independent from any persistent avoidance
// state or account scheduling policy.
const MaxPendingFailures = 256

// InternalAPIKeyID is the Node-compatible identity used when a request does
// not carry an external API key.
const InternalAPIKeyID = "internal"

// ErrorPhase identifies the caller-proven point at which an upstream failure
// was observed. Unknown values are rejected rather than classified here.
type ErrorPhase string

const (
	ErrorPhaseUpstreamResponse ErrorPhase = "upstream_response"
	ErrorPhaseUpstreamRequest  ErrorPhase = "upstream_request"
	ErrorPhaseStream           ErrorPhase = "stream"
)

// Scope binds pending facts to one request's client-IP account context.
// A missing APIKeyID is normalized to InternalAPIKeyID, matching Node. The
// SystemAccountID is retained as supplied because Node does not gate scope
// activation on it.
type Scope struct {
	SystemAccountID string
	APIKeyID        string
	ClientIP        string
}

// Valid reports whether the tracker is enabled after the Node-compatible
// normalization. Node only disables this scope when ClientIP is empty.
func (s Scope) Valid() bool {
	s = normalizeScope(s)
	return strings.TrimSpace(s.ClientIP) != ""
}

func normalizeScope(scope Scope) Scope {
	scope.APIKeyID = strings.TrimSpace(scope.APIKeyID)
	if scope.APIKeyID == "" {
		scope.APIKeyID = InternalAPIKeyID
	}
	scope.ClientIP = strings.TrimSpace(scope.ClientIP)
	return scope
}

// Failure is a complete caller-supplied pending failure fact. Optional
// textual/status values are retained exactly as supplied; AccountID and a
// known ErrorPhase are required.
type Failure struct {
	AccountID    string
	AccountName  string
	StatusCode   int
	ErrorCode    string
	ErrorType    string
	ErrorPhase   ErrorPhase
	ErrorMessage string
	Endpoint     string
}

// ValidateErrorPhase rejects uncertain phase input without attempting to
// infer a phase from status, cancellation, or transport behavior.
func ValidateErrorPhase(phase ErrorPhase) error {
	switch phase {
	case ErrorPhaseUpstreamResponse, ErrorPhaseUpstreamRequest, ErrorPhaseStream:
		return nil
	default:
		return fmt.Errorf("unknown client-IP account failure phase %q", phase)
	}
}

func validateFailure(failure Failure) error {
	if strings.TrimSpace(failure.AccountID) == "" {
		return errors.New("client-IP account failure account ID is required")
	}
	return ValidateErrorPhase(failure.ErrorPhase)
}

// RememberOutcome describes the observable result of remembering one fact.
type RememberOutcome string

const (
	RememberNoOp            RememberOutcome = "no_op"
	RememberInserted        RememberOutcome = "inserted"
	RememberReplaced        RememberOutcome = "replaced"
	RememberCapacityDropped RememberOutcome = "capacity_dropped"
)

// RememberResult exposes capacity and invalid-scope outcomes instead of
// silently discarding a fact.
type RememberResult struct {
	Outcome         RememberOutcome
	AccountID       string
	Accepted        bool
	Replaced        bool
	CapacityDropped bool
	InvalidScope    bool
	NoOp            bool
	Size            int
	Capacity        int
}

// Tracker stores request-local pending facts. Use a pointer returned by a
// constructor; copying a Tracker after use would copy its mutex.
type Tracker struct {
	mu       sync.Mutex
	scope    Scope
	valid    bool
	failures []Failure
	index    map[string]int
}

// NewTracker creates an independent request-local tracker. Invalid scopes
// remain usable as explicit no-op trackers so callers can observe the reason.
func NewTracker(scope Scope) *Tracker {
	scope = normalizeScope(scope)
	return &Tracker{
		scope: scope,
		valid: scope.Valid(),
		index: make(map[string]int),
	}
}

// NewClientIPAccountAvoidanceTracker is the descriptive constructor used by
// future owners; it is an alias of NewTracker.
func NewClientIPAccountAvoidanceTracker(scope Scope) *Tracker { return NewTracker(scope) }

// CreateClientIPAccountAvoidanceTracker preserves the Node-oriented naming
// used by migration callers.
func CreateClientIPAccountAvoidanceTracker(scope Scope) *Tracker { return NewTracker(scope) }

// Scope returns the immutable scope facts captured at construction.
func (t *Tracker) Scope() Scope {
	if t == nil {
		return Scope{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scope
}

// Valid reports whether this tracker has a complete scope.
func (t *Tracker) Valid() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.valid
}

// Remember records a caller-proven failure. Repeated account IDs replace the
// latest complete fact in their original position. A new account beyond the
// fixed cap is reported as capacity_dropped.
func (t *Tracker) Remember(failure Failure) (RememberResult, error) {
	if t == nil {
		return RememberResult{Outcome: RememberNoOp, NoOp: true, InvalidScope: true, Capacity: MaxPendingFailures}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rememberLocked(failure)
}

func (t *Tracker) rememberLocked(failure Failure) (RememberResult, error) {
	result := RememberResult{AccountID: failure.AccountID, Size: len(t.failures), Capacity: MaxPendingFailures}
	if !t.valid {
		result.Outcome = RememberNoOp
		result.NoOp = true
		result.InvalidScope = true
		return result, nil
	}
	if err := validateFailure(failure); err != nil {
		return result, err
	}
	if position, ok := t.index[failure.AccountID]; ok {
		t.failures[position] = failure
		result.Outcome = RememberReplaced
		result.Accepted = true
		result.Replaced = true
		result.Size = len(t.failures)
		return result, nil
	}
	if len(t.failures) >= MaxPendingFailures {
		result.Outcome = RememberCapacityDropped
		result.CapacityDropped = true
		result.Size = len(t.failures)
		return result, nil
	}
	t.index[failure.AccountID] = len(t.failures)
	t.failures = append(t.failures, failure)
	result.Outcome = RememberInserted
	result.Accepted = true
	result.Size = len(t.failures)
	return result, nil
}

// RememberClientIPAccountPendingFailure is a free-function form convenient
// for later route owners.
func RememberClientIPAccountPendingFailure(t *Tracker, failure Failure) (RememberResult, error) {
	if t == nil {
		return RememberResult{Outcome: RememberNoOp, NoOp: true, InvalidScope: true, Capacity: MaxPendingFailures}, nil
	}
	return t.Remember(failure)
}

// Snapshot returns a copy-safe, order-preserving view of pending facts.
func (t *Tracker) Snapshot() []Failure {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Failure(nil), t.failures...)
}

// PendingFailures is a descriptive alias for Snapshot.
func (t *Tracker) PendingFailures() []Failure { return t.Snapshot() }

// Len reports the current pending fact count.
func (t *Tracker) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.failures)
}

// TransferResult reports every transfer attempt, including target capacity
// drops. Source facts are cleared only after all target remember attempts
// complete without a validation error.
type TransferResult struct {
	NoOp            bool
	InvalidSource   bool
	InvalidTarget   bool
	SourceEmpty     bool
	SourceCleared   bool
	Attempted       int
	Inserted        int
	Replaced        int
	CapacityDropped int
	Dropped         int
	Errors          []error
}

var transferMu sync.Mutex

// Transfer moves source facts in source order. The target keeps its existing
// order and appends new accounts; same-account facts are replaced in place.
// Missing scope, nil trackers, or an empty source are observable no-ops that
// leave source untouched.
func Transfer(source, target *Tracker) TransferResult {
	result := TransferResult{}
	if source == nil {
		result.NoOp, result.InvalidSource = true, true
		return result
	}
	if target == nil {
		result.NoOp, result.InvalidTarget = true, true
		return result
	}
	transferMu.Lock()
	defer transferMu.Unlock()
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.valid {
		result.NoOp, result.InvalidSource = true, true
		return result
	}
	if len(source.failures) == 0 {
		result.NoOp, result.SourceEmpty = true, true
		return result
	}
	if source == target {
		result.Attempted = len(source.failures)
		result.Replaced = len(source.failures)
		source.failures = nil
		source.index = make(map[string]int)
		result.SourceCleared = true
		return result
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if !target.valid {
		result.NoOp, result.InvalidTarget = true, true
		return result
	}

	for _, failure := range source.failures {
		result.Attempted++
		remembered, err := target.rememberLocked(failure)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		switch remembered.Outcome {
		case RememberInserted:
			result.Inserted++
		case RememberReplaced:
			result.Replaced++
		case RememberCapacityDropped:
			result.CapacityDropped++
			result.Dropped++
		}
	}
	if len(result.Errors) == 0 {
		source.failures = nil
		source.index = make(map[string]int)
		result.SourceCleared = true
	}
	return result
}

// TransferClientIPAccountPendingFailures is the migration-oriented alias.
func TransferClientIPAccountPendingFailures(source, target *Tracker) TransferResult {
	return Transfer(source, target)
}

// ClientIPAccountAvoidanceScope and related aliases keep the contract
// discoverable for callers that use the Node service terminology.
type ClientIPAccountAvoidanceScope = Scope
type ClientIPAccountFailure = Failure
type ClientIPAccountAvoidanceTracker = Tracker
