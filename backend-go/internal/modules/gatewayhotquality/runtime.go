package gatewayhotquality

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// The runtime store is deliberately a bounded, process-local projection. It
// is useful to exercise Go's request lifecycle before a future owner decision
// introduces a shared store; it must not be used as a production fallback.
const (
	MinuteBucketCount        = 30
	DefaultKeyTTL            = 40 * time.Minute
	DefaultTerminalTTL       = 60 * time.Minute
	DefaultKeyCapacity       = 10_000
	DefaultAttemptCapacity   = 100_000
	firstByteEWMAAlpha       = 0.4
	UnknownModelFamily       = "unknown"
	maxRuntimeIdentityLength = 256
)

var firstByteUpperBounds = [...]time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

type RequestLane string

const (
	RequestLaneText  RequestLane = "text"
	RequestLaneImage RequestLane = "image"
)

type OutcomeClass string

const (
	OutcomeCompletedResponse      OutcomeClass = "completed_response"
	OutcomeUpstreamResponseFailed OutcomeClass = "upstream_response_failure"
	OutcomeExplicitPolicyFailed   OutcomeClass = "explicit_policy_failure"
	OutcomeTransportFailed        OutcomeClass = "transport_failure"
	OutcomeTimeout                OutcomeClass = "timeout"
	OutcomeReadInterrupted        OutcomeClass = "read_interruption"
	OutcomeIncompleteResponse     OutcomeClass = "incomplete_response"
	OutcomeUnknown                OutcomeClass = "unknown"
	OutcomeClientCanceled         OutcomeClass = "client_cancellation"
)

type FailureScope string

const (
	FailureScopeNone          FailureScope = "none"
	FailureScopeKey           FailureScope = "key"
	FailureScopeProtocolModel FailureScope = "protocol_model"
	FailureScopeAccount       FailureScope = "account"
	FailureScopeUpstream      FailureScope = "upstream_bucket"
)

type TerminalSource string

const (
	TerminalSourceTransport   TerminalSource = "gateway_transport"
	TerminalSourceUpstream    TerminalSource = "upstream_response"
	TerminalSourcePolicy      TerminalSource = "explicit_policy"
	TerminalSourceRequestLife TerminalSource = "request_lifecycle"
)

// Scope has no raw credential or raw model value. ModelFamily must be a
// bounded bucket selected by the caller.
type Scope struct {
	AccountRuntimeKey string
	ProtocolProfile   string
	RequestLane       RequestLane
	ModelFamily       string
}

type TerminalRecord struct {
	OutcomeID    string
	OutcomeClass OutcomeClass
	FailureScope FailureScope
	Source       TerminalSource
	RecordedAt   time.Time
}

type Counters struct {
	Attempts                 uint64
	CompletedResponses       uint64
	UpstreamResponseFailures uint64
	LocalTransportFailures   uint64
	Timeouts                 uint64
	ReadInterruptions        uint64
	IncompleteResponses      uint64
	ExplicitPolicyFailures   uint64
	UnknownOutcomes          uint64
	ClientCancellations      uint64
	FirstByteSampleCount     uint64
	FirstByteSum             time.Duration
	FirstByteHistogram       [8]uint64
	LastCompletedAt          *time.Time
	LastFailureAt            *time.Time
}

type MinuteBucket struct {
	MinuteStartedAt time.Time
	Counters
}

type WindowSnapshot struct {
	Counters
	Minutes                int
	QualityAttempts        uint64
	AdjustedCompletionRate float64
}

type RuntimeSnapshot struct {
	Scope                 Scope
	ScopeKey              string
	MinuteBuckets         []MinuteBucket
	Window5m              WindowSnapshot
	Window10m             WindowSnapshot
	Window30m             WindowSnapshot
	Reliability           float64
	Confidence            float64
	EffectiveReliability  float64
	ReliabilityLevel      Reliability
	SampleState           SampleState
	FirstByteEWMA5m       *time.Duration
	FirstByteP95Bucket10m *int
	ExpiresAt             time.Time
}

// SelectorSnapshot preserves the existing side-effect-free selector API.
func (s RuntimeSnapshot) SelectorSnapshot() Snapshot {
	value := Snapshot{SampleState: s.SampleState, Reliability: s.ReliabilityLevel, EffectiveReliability: s.EffectiveReliability}
	if s.FirstByteEWMA5m != nil {
		value.EWMAFirstByteMS = float64(*s.FirstByteEWMA5m) / float64(time.Millisecond)
	}
	if s.FirstByteP95Bucket10m != nil {
		value.P95FirstByteMS = firstByteBucketMilliseconds(*s.FirstByteP95Bucket10m)
	}
	return value
}

type AttemptMutationStatus string

const (
	AttemptApplied       AttemptMutationStatus = "applied"
	AttemptIdempotent    AttemptMutationStatus = "idempotent"
	AttemptDegraded      AttemptMutationStatus = "degraded_to_protocol"
	AttemptConflict      AttemptMutationStatus = "attempt_conflict"
	AttemptKeyCapacity   AttemptMutationStatus = "key_capacity_exhausted"
	AttemptIdentityLimit AttemptMutationStatus = "attempt_capacity_exhausted"
)

type AttemptMutation struct {
	Status         AttemptMutationStatus
	RequestedScope Scope
	EffectiveScope Scope
}

type TerminalMutationStatus string

const (
	TerminalApplied            TerminalMutationStatus = "applied"
	TerminalIdempotent         TerminalMutationStatus = "idempotent"
	TerminalAttemptConflict    TerminalMutationStatus = "attempt_conflict"
	TerminalAttemptMissing     TerminalMutationStatus = "attempt_not_found"
	TerminalConflict           TerminalMutationStatus = "terminal_conflict"
	TerminalOutcomeConflict    TerminalMutationStatus = "terminal_outcome_conflict"
	TerminalQualityUnavailable TerminalMutationStatus = "quality_key_unavailable"
)

type TerminalMutation struct {
	Status         TerminalMutationStatus
	Terminal       *TerminalRecord
	EffectiveScope *Scope
}

type StoreStats struct {
	KeyCount                 int
	AttemptIdentityCount     int
	TerminalIdentityCount    int
	KeyCreationRefusals      uint64
	HighCardinalityFallbacks uint64
	AttemptCapacityRefusals  uint64
	TerminalQualityKeyMisses uint64
}

type StoreOptions struct {
	KeyCapacity     int
	AttemptCapacity int
	KeyTTL          time.Duration
	TerminalTTL     time.Duration
	Now             func() time.Time
}

type RecordAttemptInput struct {
	AttemptID string
	Scope     Scope
	Now       time.Time
}

type RecordTerminalInput struct {
	AttemptID string
	Scope     Scope
	OutcomeID string
	Outcome   OutcomeClass
	Failure   FailureScope
	Source    TerminalSource
	FirstByte *time.Duration
	Now       time.Time
}

type Store struct {
	mu                   sync.Mutex
	entries              map[string]*runtimeEntry
	attempts             map[string]*attemptIdentity
	terminalOutcomes     map[string]string
	keyCapacity          int
	attemptCapacity      int
	keyTTL               time.Duration
	terminalTTL          time.Duration
	now                  func() time.Time
	keyCreationRefusals  uint64
	highCardinalityFalls uint64
	attemptCapacityDrops uint64
	terminalKeyMisses    uint64
}

type runtimeEntry struct {
	scope     Scope
	scopeKey  string
	buckets   [MinuteBucketCount]*MinuteBucket
	expiresAt time.Time
}

type attemptIdentity struct {
	requestedScopeKey string
	effectiveScopeKey string
	effectiveScope    Scope
	expiresAt         time.Time
	terminal          *TerminalRecord
}

func NewStore(options StoreOptions) (*Store, error) {
	if options.KeyCapacity == 0 {
		options.KeyCapacity = DefaultKeyCapacity
	}
	if options.AttemptCapacity == 0 {
		options.AttemptCapacity = DefaultAttemptCapacity
	}
	if options.KeyTTL == 0 {
		options.KeyTTL = DefaultKeyTTL
	}
	if options.TerminalTTL == 0 {
		options.TerminalTTL = DefaultTerminalTTL
	}
	if options.KeyCapacity < 1 || options.AttemptCapacity < 1 || options.KeyTTL <= 0 || options.TerminalTTL < options.KeyTTL {
		return nil, fmt.Errorf("invalid hot quality store bounds")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{entries: make(map[string]*runtimeEntry), attempts: make(map[string]*attemptIdentity), terminalOutcomes: make(map[string]string), keyCapacity: options.KeyCapacity, attemptCapacity: options.AttemptCapacity, keyTTL: options.KeyTTL, terminalTTL: options.TerminalTTL, now: options.Now}, nil
}

func (s *Store) RecordAttempt(input RecordAttemptInput) (AttemptMutation, error) {
	attemptID, err := runtimeIdentity(input.AttemptID, "attempt ID")
	if err != nil {
		return AttemptMutation{}, err
	}
	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return AttemptMutation{}, err
	}
	now := s.at(input.Now)
	requestedKey := scopeKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	if existing := s.freshAttempt(attemptID, now); existing != nil {
		status := AttemptIdempotent
		if existing.requestedScopeKey != requestedKey {
			status = AttemptConflict
		}
		return AttemptMutation{Status: status, RequestedScope: scope, EffectiveScope: existing.effectiveScope}, nil
	}
	if len(s.attempts) >= s.attemptCapacity {
		s.attemptCapacityDrops = saturatingAdd(s.attemptCapacityDrops, 1)
		return AttemptMutation{Status: AttemptIdentityLimit, RequestedScope: scope, EffectiveScope: scope}, nil
	}
	entry, degraded := s.resolveEntry(scope, requestedKey, now)
	if entry == nil {
		s.keyCreationRefusals = saturatingAdd(s.keyCreationRefusals, 1)
		return AttemptMutation{Status: AttemptKeyCapacity, RequestedScope: scope, EffectiveScope: scope}, nil
	}
	s.attempts[attemptID] = &attemptIdentity{requestedScopeKey: requestedKey, effectiveScopeKey: entry.scopeKey, effectiveScope: entry.scope, expiresAt: now.Add(s.terminalTTL)}
	bucket := currentBucket(entry, now)
	bucket.Attempts = saturatingAdd(bucket.Attempts, 1)
	entry.expiresAt = now.Add(s.keyTTL)
	if degraded {
		s.highCardinalityFalls = saturatingAdd(s.highCardinalityFalls, 1)
		return AttemptMutation{Status: AttemptDegraded, RequestedScope: scope, EffectiveScope: entry.scope}, nil
	}
	return AttemptMutation{Status: AttemptApplied, RequestedScope: scope, EffectiveScope: entry.scope}, nil
}

func (s *Store) RecordTerminal(input RecordTerminalInput) (TerminalMutation, error) {
	attemptID, err := runtimeIdentity(input.AttemptID, "attempt ID")
	if err != nil {
		return TerminalMutation{}, err
	}
	outcomeID, err := runtimeIdentity(input.OutcomeID, "terminal outcome ID")
	if err != nil {
		return TerminalMutation{}, err
	}
	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return TerminalMutation{}, err
	}
	if !validOutcome(input.Outcome) || !validFailureScope(input.Failure) || !validTerminalSource(input.Source) {
		return TerminalMutation{}, fmt.Errorf("invalid hot quality terminal facts")
	}
	if input.FirstByte != nil && *input.FirstByte < 0 {
		return TerminalMutation{}, fmt.Errorf("hot quality first byte must not be negative")
	}
	now := s.at(input.Now)
	requestedKey := scopeKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	attempt := s.freshAttempt(attemptID, now)
	if attempt == nil {
		return TerminalMutation{Status: TerminalAttemptMissing}, nil
	}
	effective := attempt.effectiveScope
	if attempt.requestedScopeKey != requestedKey {
		return TerminalMutation{Status: TerminalAttemptConflict, EffectiveScope: &effective}, nil
	}
	if attempt.terminal != nil {
		if sameTerminal(*attempt.terminal, outcomeID, input.Outcome, input.Failure, input.Source) {
			terminal := *attempt.terminal
			return TerminalMutation{Status: TerminalIdempotent, Terminal: &terminal, EffectiveScope: &effective}, nil
		}
		terminal := *attempt.terminal
		return TerminalMutation{Status: TerminalConflict, Terminal: &terminal, EffectiveScope: &effective}, nil
	}
	if owner, ok := s.terminalOutcomes[outcomeID]; ok && owner != attemptID && s.freshAttempt(owner, now) != nil {
		return TerminalMutation{Status: TerminalOutcomeConflict, EffectiveScope: &effective}, nil
	}
	entry := s.entryForTerminal(attempt, now)
	if entry == nil {
		s.terminalKeyMisses = saturatingAdd(s.terminalKeyMisses, 1)
		return TerminalMutation{Status: TerminalQualityUnavailable, EffectiveScope: &effective}, nil
	}
	terminal := TerminalRecord{OutcomeID: outcomeID, OutcomeClass: input.Outcome, FailureScope: input.Failure, Source: input.Source, RecordedAt: now}
	attempt.terminal = &terminal
	attempt.expiresAt = now.Add(s.terminalTTL)
	s.terminalOutcomes[outcomeID] = attemptID
	applyTerminal(currentBucket(entry, now), terminal, input.FirstByte)
	entry.expiresAt = now.Add(s.keyTTL)
	return TerminalMutation{Status: TerminalApplied, Terminal: &terminal, EffectiveScope: &effective}, nil
}

func (s *Store) Snapshot(scope Scope, now time.Time) (RuntimeSnapshot, bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return RuntimeSnapshot{}, false, err
	}
	now = s.at(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	entry := s.freshEntry(scopeKey(scope), now)
	if entry == nil {
		return RuntimeSnapshot{}, false, nil
	}
	return projectSnapshot(entry, now), true, nil
}

func (s *Store) Terminal(attemptID string, now time.Time) (*TerminalRecord, error) {
	attemptID, err := runtimeIdentity(attemptID, "attempt ID")
	if err != nil {
		return nil, err
	}
	now = s.at(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	attempt := s.freshAttempt(attemptID, now)
	if attempt == nil || attempt.terminal == nil {
		return nil, nil
	}
	result := *attempt.terminal
	return &result, nil
}

func (s *Store) Stats(now time.Time) StoreStats {
	now = s.at(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	return StoreStats{KeyCount: len(s.entries), AttemptIdentityCount: len(s.attempts), TerminalIdentityCount: len(s.terminalOutcomes), KeyCreationRefusals: s.keyCreationRefusals, HighCardinalityFallbacks: s.highCardinalityFalls, AttemptCapacityRefusals: s.attemptCapacityDrops, TerminalQualityKeyMisses: s.terminalKeyMisses}
}

func (s *Store) at(value time.Time) time.Time {
	if value.IsZero() {
		value = s.now()
	}
	return value.UTC()
}

func (s *Store) resolveEntry(scope Scope, key string, now time.Time) (*runtimeEntry, bool) {
	if entry := s.freshEntry(key, now); entry != nil {
		return entry, false
	}
	if len(s.entries) < s.keyCapacity {
		return s.createEntry(scope, key, now), false
	}
	fallback := scope
	fallback.ModelFamily = UnknownModelFamily
	if scope.ModelFamily == UnknownModelFamily {
		return nil, false
	}
	return s.freshEntry(scopeKey(fallback), now), true
}

func (s *Store) entryForTerminal(attempt *attemptIdentity, now time.Time) *runtimeEntry {
	if entry := s.freshEntry(attempt.effectiveScopeKey, now); entry != nil {
		return entry
	}
	if len(s.entries) >= s.keyCapacity {
		return nil
	}
	return s.createEntry(attempt.effectiveScope, attempt.effectiveScopeKey, now)
}

func (s *Store) createEntry(scope Scope, key string, now time.Time) *runtimeEntry {
	entry := &runtimeEntry{scope: scope, scopeKey: key, expiresAt: now.Add(s.keyTTL)}
	s.entries[key] = entry
	return entry
}

func (s *Store) freshEntry(key string, now time.Time) *runtimeEntry {
	entry := s.entries[key]
	if entry == nil || entry.expiresAt.After(now) {
		return entry
	}
	delete(s.entries, key)
	return nil
}

func (s *Store) freshAttempt(id string, now time.Time) *attemptIdentity {
	attempt := s.attempts[id]
	if attempt == nil || attempt.expiresAt.After(now) {
		return attempt
	}
	delete(s.attempts, id)
	if attempt.terminal != nil {
		delete(s.terminalOutcomes, attempt.terminal.OutcomeID)
	}
	return nil
}

func (s *Store) cleanup(now time.Time) {
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
	for id := range s.attempts {
		s.freshAttempt(id, now)
	}
}

func currentBucket(entry *runtimeEntry, now time.Time) *MinuteBucket {
	minute := now.Truncate(time.Minute)
	index := int(minute.Unix()/60) % MinuteBucketCount
	if index < 0 {
		index += MinuteBucketCount
	}
	bucket := entry.buckets[index]
	if bucket == nil || !bucket.MinuteStartedAt.Equal(minute) {
		bucket = &MinuteBucket{MinuteStartedAt: minute}
		entry.buckets[index] = bucket
	}
	return bucket
}

func applyTerminal(bucket *MinuteBucket, terminal TerminalRecord, firstByte *time.Duration) {
	switch terminal.OutcomeClass {
	case OutcomeCompletedResponse:
		bucket.CompletedResponses = saturatingAdd(bucket.CompletedResponses, 1)
		bucket.LastCompletedAt = latest(bucket.LastCompletedAt, terminal.RecordedAt)
	case OutcomeUpstreamResponseFailed:
		bucket.UpstreamResponseFailures = saturatingAdd(bucket.UpstreamResponseFailures, 1)
	case OutcomeExplicitPolicyFailed:
		bucket.ExplicitPolicyFailures = saturatingAdd(bucket.ExplicitPolicyFailures, 1)
		bucket.LastFailureAt = latest(bucket.LastFailureAt, terminal.RecordedAt)
	case OutcomeTransportFailed:
		bucket.LocalTransportFailures = saturatingAdd(bucket.LocalTransportFailures, 1)
		bucket.LastFailureAt = latest(bucket.LastFailureAt, terminal.RecordedAt)
	case OutcomeTimeout:
		bucket.LocalTransportFailures = saturatingAdd(bucket.LocalTransportFailures, 1)
		bucket.Timeouts = saturatingAdd(bucket.Timeouts, 1)
		bucket.LastFailureAt = latest(bucket.LastFailureAt, terminal.RecordedAt)
	case OutcomeReadInterrupted:
		bucket.LocalTransportFailures = saturatingAdd(bucket.LocalTransportFailures, 1)
		bucket.ReadInterruptions = saturatingAdd(bucket.ReadInterruptions, 1)
		bucket.LastFailureAt = latest(bucket.LastFailureAt, terminal.RecordedAt)
	case OutcomeIncompleteResponse:
		bucket.LocalTransportFailures = saturatingAdd(bucket.LocalTransportFailures, 1)
		bucket.IncompleteResponses = saturatingAdd(bucket.IncompleteResponses, 1)
		bucket.LastFailureAt = latest(bucket.LastFailureAt, terminal.RecordedAt)
	case OutcomeUnknown:
		bucket.UnknownOutcomes = saturatingAdd(bucket.UnknownOutcomes, 1)
	case OutcomeClientCanceled:
		bucket.ClientCancellations = saturatingAdd(bucket.ClientCancellations, 1)
	}
	if firstByte == nil || terminal.OutcomeClass == OutcomeUpstreamResponseFailed || terminal.OutcomeClass == OutcomeUnknown || terminal.OutcomeClass == OutcomeClientCanceled {
		return
	}
	bucket.FirstByteSampleCount = saturatingAdd(bucket.FirstByteSampleCount, 1)
	if *firstByte > time.Duration(math.MaxInt64)-bucket.FirstByteSum {
		bucket.FirstByteSum = time.Duration(math.MaxInt64)
	} else {
		bucket.FirstByteSum += *firstByte
	}
	bucket.FirstByteHistogram[firstByteBucket(*firstByte)] = saturatingAdd(bucket.FirstByteHistogram[firstByteBucket(*firstByte)], 1)
}

func projectSnapshot(entry *runtimeEntry, now time.Time) RuntimeSnapshot {
	currentMinute := now.Truncate(time.Minute)
	buckets := make([]MinuteBucket, 0, MinuteBucketCount)
	for _, bucket := range entry.buckets {
		if bucket == nil || bucket.MinuteStartedAt.After(currentMinute) || !bucket.MinuteStartedAt.After(currentMinute.Add(-MinuteBucketCount*time.Minute)) {
			continue
		}
		buckets = append(buckets, cloneBucket(*bucket))
	}
	slices.SortFunc(buckets, func(left, right MinuteBucket) int { return left.MinuteStartedAt.Compare(right.MinuteStartedAt) })
	window5 := window(buckets, currentMinute, 5)
	window10 := window(buckets, currentMinute, 10)
	window30 := window(buckets, currentMinute, 30)
	reliability := window5.AdjustedCompletionRate*.6 + window10.AdjustedCompletionRate*.3 + window30.AdjustedCompletionRate*.1
	confidence := min(1, float64(window10.QualityAttempts)/10)
	effective := .5 + (reliability-.5)*confidence
	state := SampleStateKnown
	if window30.QualityAttempts == 0 {
		state = SampleStateCold
	} else if window10.QualityAttempts < 3 {
		state = SampleStateWarming
	}
	return RuntimeSnapshot{Scope: entry.scope, ScopeKey: entry.scopeKey, MinuteBuckets: buckets, Window5m: window5, Window10m: window10, Window30m: window30, Reliability: reliability, Confidence: confidence, EffectiveReliability: effective, ReliabilityLevel: reliabilityLevel(window5.QualityAttempts, window10.QualityAttempts, effective), SampleState: state, FirstByteEWMA5m: firstByteEWMA(buckets, currentMinute), FirstByteP95Bucket10m: p95Bucket(window10.FirstByteHistogram, window10.FirstByteSampleCount), ExpiresAt: entry.expiresAt}
}

func window(buckets []MinuteBucket, currentMinute time.Time, minutes int) WindowSnapshot {
	result := WindowSnapshot{Minutes: minutes}
	for _, bucket := range buckets {
		if !bucket.MinuteStartedAt.After(currentMinute.Add(-time.Duration(minutes) * time.Minute)) {
			continue
		}
		mergeCounters(&result.Counters, bucket.Counters)
	}
	result.QualityAttempts = saturatingAdd(saturatingAdd(result.CompletedResponses, result.LocalTransportFailures), result.ExplicitPolicyFailures)
	result.AdjustedCompletionRate = float64(result.CompletedResponses+2) / float64(result.QualityAttempts+4)
	return result
}

func firstByteEWMA(buckets []MinuteBucket, currentMinute time.Time) *time.Duration {
	var value *float64
	for _, bucket := range buckets {
		if !bucket.MinuteStartedAt.After(currentMinute.Add(-5*time.Minute)) || bucket.FirstByteSampleCount == 0 {
			continue
		}
		average := float64(bucket.FirstByteSum) / float64(bucket.FirstByteSampleCount)
		if value == nil {
			value = &average
		} else {
			next := *value*(1-firstByteEWMAAlpha) + average*firstByteEWMAAlpha
			value = &next
		}
	}
	if value == nil {
		return nil
	}
	result := time.Duration(*value)
	return &result
}

func p95Bucket(histogram [8]uint64, samples uint64) *int {
	if samples == 0 {
		return nil
	}
	target := uint64(math.Ceil(float64(samples) * .95))
	var sum uint64
	for index, count := range histogram {
		sum = saturatingAdd(sum, count)
		if sum >= target {
			return &index
		}
	}
	return nil
}

func mergeCounters(target *Counters, source Counters) {
	target.Attempts = saturatingAdd(target.Attempts, source.Attempts)
	target.CompletedResponses = saturatingAdd(target.CompletedResponses, source.CompletedResponses)
	target.UpstreamResponseFailures = saturatingAdd(target.UpstreamResponseFailures, source.UpstreamResponseFailures)
	target.LocalTransportFailures = saturatingAdd(target.LocalTransportFailures, source.LocalTransportFailures)
	target.Timeouts = saturatingAdd(target.Timeouts, source.Timeouts)
	target.ReadInterruptions = saturatingAdd(target.ReadInterruptions, source.ReadInterruptions)
	target.IncompleteResponses = saturatingAdd(target.IncompleteResponses, source.IncompleteResponses)
	target.ExplicitPolicyFailures = saturatingAdd(target.ExplicitPolicyFailures, source.ExplicitPolicyFailures)
	target.UnknownOutcomes = saturatingAdd(target.UnknownOutcomes, source.UnknownOutcomes)
	target.ClientCancellations = saturatingAdd(target.ClientCancellations, source.ClientCancellations)
	target.FirstByteSampleCount = saturatingAdd(target.FirstByteSampleCount, source.FirstByteSampleCount)
	if source.FirstByteSum > time.Duration(math.MaxInt64)-target.FirstByteSum {
		target.FirstByteSum = time.Duration(math.MaxInt64)
	} else {
		target.FirstByteSum += source.FirstByteSum
	}
	for index, count := range source.FirstByteHistogram {
		target.FirstByteHistogram[index] = saturatingAdd(target.FirstByteHistogram[index], count)
	}
	target.LastCompletedAt = latest(target.LastCompletedAt, derefTime(source.LastCompletedAt))
	target.LastFailureAt = latest(target.LastFailureAt, derefTime(source.LastFailureAt))
}

func cloneBucket(value MinuteBucket) MinuteBucket {
	value.LastCompletedAt = cloneTime(value.LastCompletedAt)
	value.LastFailureAt = cloneTime(value.LastFailureAt)
	return value
}

func latest(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() || (current != nil && !candidate.After(*current)) {
		return cloneTime(current)
	}
	return &candidate
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func reliabilityLevel(qualityAttempts5, qualityAttempts10 uint64, effective float64) Reliability {
	if qualityAttempts10 < 3 {
		return ReliabilityUnknown
	}
	if qualityAttempts5 >= 3 && effective >= .85 {
		return ReliabilityHealthy
	}
	if qualityAttempts5 >= 3 && effective < .6 {
		return ReliabilityUnhealthy
	}
	return ReliabilityUncertain
}

func firstByteBucket(value time.Duration) int {
	for index, bound := range firstByteUpperBounds {
		if value <= bound {
			return index
		}
	}
	return len(firstByteUpperBounds)
}

func sameTerminal(value TerminalRecord, outcomeID string, outcome OutcomeClass, failure FailureScope, source TerminalSource) bool {
	return value.OutcomeID == outcomeID && value.OutcomeClass == outcome && value.FailureScope == failure && value.Source == source
}

func normalizeScope(value Scope) (Scope, error) {
	var err error
	if value.AccountRuntimeKey, err = scopePart(value.AccountRuntimeKey, "account runtime key", 1024); err != nil {
		return Scope{}, err
	}
	if value.ProtocolProfile, err = scopePart(value.ProtocolProfile, "protocol profile", 256); err != nil {
		return Scope{}, err
	}
	if value.ModelFamily, err = scopePart(value.ModelFamily, "model family", 128); err != nil {
		return Scope{}, err
	}
	if value.RequestLane != RequestLaneText && value.RequestLane != RequestLaneImage {
		return Scope{}, fmt.Errorf("invalid hot quality request lane")
	}
	return value, nil
}

func scopeKey(value Scope) string {
	return strings.Join([]string{encodedPart(value.AccountRuntimeKey), encodedPart(value.ProtocolProfile), encodedPart(string(value.RequestLane)), encodedPart(value.ModelFamily)}, "|")
}

func encodedPart(value string) string { return fmt.Sprintf("%d:%s", len(value), value) }

func runtimeIdentity(value, name string) (string, error) {
	return scopePart(value, name, maxRuntimeIdentityLength)
}

func scopePart(value, name string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || strings.ToValidUTF8(value, "") != value || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("hot quality %s is invalid", name)
	}
	return value, nil
}

func validOutcome(value OutcomeClass) bool {
	return value == OutcomeCompletedResponse || value == OutcomeUpstreamResponseFailed || value == OutcomeExplicitPolicyFailed || value == OutcomeTransportFailed || value == OutcomeTimeout || value == OutcomeReadInterrupted || value == OutcomeIncompleteResponse || value == OutcomeUnknown || value == OutcomeClientCanceled
}

func validFailureScope(value FailureScope) bool {
	return value == FailureScopeNone || value == FailureScopeKey || value == FailureScopeProtocolModel || value == FailureScopeAccount || value == FailureScopeUpstream
}

func validTerminalSource(value TerminalSource) bool {
	return value == TerminalSourceTransport || value == TerminalSourceUpstream || value == TerminalSourcePolicy || value == TerminalSourceRequestLife
}

func saturatingAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func firstByteBucketMilliseconds(index int) float64 {
	if index < 0 {
		return 0
	}
	if index >= len(firstByteUpperBounds) {
		return float64(math.MaxInt64) / float64(time.Millisecond)
	}
	return float64(firstByteUpperBounds[index]) / float64(time.Millisecond)
}
