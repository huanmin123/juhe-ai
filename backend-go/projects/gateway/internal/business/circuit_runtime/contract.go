package circuitruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	GatewayAccountCircuitRuntimeMaxDuePage        = 500
	GatewayAccountCircuitRuntimeMaxRevisionPage   = 500
	GatewayAccountCircuitRuntimeMaxReplayIDs      = 64
	GatewayAccountCircuitRuntimeMaxEvidenceScopes = 64
	GatewayAccountCircuitRuntimeMaxEvidenceWindow = 24 * time.Hour
)

type GatewayAccountCircuitScopeKind string

const (
	GatewayAccountCircuitScopeAccount       GatewayAccountCircuitScopeKind = "account"
	GatewayAccountCircuitScopeAPIKey        GatewayAccountCircuitScopeKind = "key"
	GatewayAccountCircuitScopeProtocolModel GatewayAccountCircuitScopeKind = "protocol_model"
	GatewayAccountCircuitScopeKeyModel      GatewayAccountCircuitScopeKind = "key_model"
)

type GatewayAccountCircuitPhase string

const (
	GatewayAccountCircuitPhaseClosed     GatewayAccountCircuitPhase = "CLOSED"
	GatewayAccountCircuitPhaseSuspect    GatewayAccountCircuitPhase = "SUSPECT"
	GatewayAccountCircuitPhaseOpen       GatewayAccountCircuitPhase = "OPEN"
	GatewayAccountCircuitPhaseHalfOpen   GatewayAccountCircuitPhase = "HALF_OPEN"
	GatewayAccountCircuitPhaseRecovering GatewayAccountCircuitPhase = "RECOVERING"
)

type GatewayAccountCircuitLeaseKind string

const (
	GatewayAccountCircuitLeaseConfirmation GatewayAccountCircuitLeaseKind = "confirmation"
	GatewayAccountCircuitLeaseHalfOpen     GatewayAccountCircuitLeaseKind = "half_open"
	GatewayAccountCircuitLeaseRecovery     GatewayAccountCircuitLeaseKind = "recovery"
)

type GatewayAccountCircuitCompletionOutcome string

const (
	GatewayAccountCircuitCompletionFramingComplete  GatewayAccountCircuitCompletionOutcome = "framing_complete"
	GatewayAccountCircuitCompletionTransportFailure GatewayAccountCircuitCompletionOutcome = "transport_failure"
	GatewayAccountCircuitCompletionUnknown          GatewayAccountCircuitCompletionOutcome = "unknown"
)

type GatewayAccountCircuitMutationStatus string

const (
	GatewayAccountCircuitMutationApplied               GatewayAccountCircuitMutationStatus = "applied"
	GatewayAccountCircuitMutationIdempotent            GatewayAccountCircuitMutationStatus = "idempotent"
	GatewayAccountCircuitMutationNotFound              GatewayAccountCircuitMutationStatus = "not_found"
	GatewayAccountCircuitMutationStateMismatch         GatewayAccountCircuitMutationStatus = "state_mismatch"
	GatewayAccountCircuitMutationStaleGeneration       GatewayAccountCircuitMutationStatus = "stale_generation"
	GatewayAccountCircuitMutationStaleDispatchRevision GatewayAccountCircuitMutationStatus = "stale_dispatch_revision"
	GatewayAccountCircuitMutationLeaseMismatch         GatewayAccountCircuitMutationStatus = "lease_mismatch"
	GatewayAccountCircuitMutationNotDue                GatewayAccountCircuitMutationStatus = "not_due"
	GatewayAccountCircuitMutationCapacityExhausted     GatewayAccountCircuitMutationStatus = "capacity_exhausted"
)

type GatewayAccountCircuitEscalationStatus string

const (
	GatewayAccountCircuitEscalationRecorded              GatewayAccountCircuitEscalationStatus = "recorded"
	GatewayAccountCircuitEscalationEscalated             GatewayAccountCircuitEscalationStatus = "escalated"
	GatewayAccountCircuitEscalationAlreadyActive         GatewayAccountCircuitEscalationStatus = "already_active"
	GatewayAccountCircuitEscalationIdempotent            GatewayAccountCircuitEscalationStatus = "idempotent"
	GatewayAccountCircuitEscalationNotFound              GatewayAccountCircuitEscalationStatus = "not_found"
	GatewayAccountCircuitEscalationStateMismatch         GatewayAccountCircuitEscalationStatus = "state_mismatch"
	GatewayAccountCircuitEscalationStaleGeneration       GatewayAccountCircuitEscalationStatus = "stale_generation"
	GatewayAccountCircuitEscalationStaleDispatchRevision GatewayAccountCircuitEscalationStatus = "stale_dispatch_revision"
	GatewayAccountCircuitEscalationCapacityExhausted     GatewayAccountCircuitEscalationStatus = "capacity_exhausted"
)

type GatewayAccountCircuitScope struct {
	Kind                      GatewayAccountCircuitScopeKind
	AccountRuntimeKey         string
	KeyFingerprint            string
	ProtocolProfile           string
	RequestLane               string
	ModelBucket               string
	ClientModel               string
	CapabilityHash            string
	CredentialSourceAccountID string
	ClientEndpointFamily      string
	FinalUpstreamModel        string
	UpstreamEndpointMode      string
}

type GatewayAccountCircuitLease struct {
	Kind  GatewayAccountCircuitLeaseKind
	ID    string
	Until time.Time
}

type GatewayAccountCircuitState struct {
	ScopeKey                  string
	Scope                     GatewayAccountCircuitScope
	Phase                     GatewayAccountCircuitPhase
	Generation                int
	DispatchRevision          int64
	LedgerRevision            int64
	TransitionID              string
	BackoffAttempt            int
	RecoverySuccessCount      int
	OpenedAt                  *time.Time
	RetryAt                   *time.Time
	FailureReason             string
	Lease                     *GatewayAccountCircuitLease
	HalfOpenOrigin            GatewayAccountCircuitPhase
	IncidentID                string
	ShadowedByIncidentID      string
	ChildIncidentIDs          []string
	ChildScopeKeys            []string
	RequiredRecoveryScopeKeys []string
	RecoveryEvidenceScopeKeys []string
	UpdatedAt                 time.Time
}

type GatewayAccountCircuitMutationResult struct {
	Status GatewayAccountCircuitMutationStatus
	State  GatewayAccountCircuitState
}

type GatewayAccountCircuitEscalationResult struct {
	Status                GatewayAccountCircuitEscalationStatus
	AccountState          GatewayAccountCircuitState
	ProtocolScopeCount    int
	ConfirmedFailureCount int
}

// GatewayAccountCircuitTransitionIdentity fences a state transition against the
// durable account revision and the observed in-memory generation.
type GatewayAccountCircuitTransitionIdentity struct {
	AccountID        string
	Scope            GatewayAccountCircuitScope
	Generation       int
	DispatchRevision int64
	TransitionID     string
	Now              time.Time
}

type GatewayAccountCircuitGetInput struct {
	AccountID string
	Scope     GatewayAccountCircuitScope
	Now       time.Time
}

type GatewayAccountCircuitSuspectInput struct {
	AccountID        string
	Scope            GatewayAccountCircuitScope
	DispatchRevision int64
	TransitionID     string
	Reason           string
	Now              time.Time
}

type GatewayAccountCircuitAcquireConfirmationLeaseInput struct {
	GatewayAccountCircuitTransitionIdentity
	LeaseID    string
	LeaseUntil time.Time
}

type GatewayAccountCircuitCompleteConfirmationInput struct {
	GatewayAccountCircuitTransitionIdentity
	LeaseID string
	Outcome GatewayAccountCircuitCompletionOutcome
	Reason  string
}

type GatewayAccountCircuitAcquireCanaryLeaseInput struct {
	GatewayAccountCircuitTransitionIdentity
	LeaseID    string
	LeaseUntil time.Time
}

type GatewayAccountCircuitCompleteCanaryInput struct {
	GatewayAccountCircuitTransitionIdentity
	LeaseID          string
	Outcome          GatewayAccountCircuitCompletionOutcome
	Reason           string
	EvidenceScopeKey string
}

type GatewayAccountCircuitReplaceDispatchRevisionInput struct {
	AccountID        string
	Scope            GatewayAccountCircuitScope
	DispatchRevision int64
	TransitionID     string
	Now              time.Time
}

type GatewayAccountCircuitRestoreInput struct {
	AccountID     string
	State         GatewayAccountCircuitState
	RetainedUntil *time.Time
	Now           time.Time
}

type GatewayAccountCircuitReplaceAccountDispatchRevisionInput struct {
	AccountID        string
	DispatchRevision int64
	TransitionID     string
	Now              time.Time
}

type GatewayAccountCircuitAccountRevisionResult struct {
	Status                  GatewayAccountCircuitMutationStatus
	CurrentDispatchRevision int64
	ClosedScopeCount        int
}

type GatewayAccountCircuitListDueInput struct {
	Now   time.Time
	Limit int
}

type GatewayAccountCircuitDispatchRevisionSnapshot struct {
	AccountID        string
	DispatchRevision int64
}

type GatewayAccountCircuitDispatchRevisionPageInput struct {
	AfterAccountID string
	Limit          int
}

type GatewayAccountCircuitDispatchRevisionPage struct {
	Items              []GatewayAccountCircuitDispatchRevisionSnapshot
	NextAfterAccountID string
}

type GatewayAccountCircuitDispatchRevisionReader interface {
	ListGatewayAccountCircuitDispatchRevisions(context.Context, GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error)
}

type GatewayAccountCircuitProtocolModelOpenEvidenceInput struct {
	AccountID             string
	Scope                 GatewayAccountCircuitScope
	Generation            int
	DispatchRevision      int64
	EvidenceID            string
	AccountTransitionID   string
	Reason                string
	ConfirmedFailureCount int
	Window                time.Duration
	MaxProtocolScopes     int
	Now                   time.Time
}

type GatewayAccountCircuitClearAccountEscalationEvidenceInput struct {
	AccountID         string
	AccountRuntimeKey string
	DispatchRevision  int64
	EvidenceID        string
	Now               time.Time
}

type GatewayAccountCircuitReader interface {
	GetGatewayAccountCircuit(context.Context, GatewayAccountCircuitGetInput) (GatewayAccountCircuitState, error)
	ListDueGatewayAccountCircuits(context.Context, GatewayAccountCircuitListDueInput) ([]GatewayAccountCircuitState, error)
}

type GatewayAccountCircuitTransitionStore interface {
	SuspectGatewayAccountCircuit(context.Context, GatewayAccountCircuitSuspectInput) (GatewayAccountCircuitMutationResult, error)
	AcquireGatewayAccountCircuitConfirmationLease(context.Context, GatewayAccountCircuitAcquireConfirmationLeaseInput) (GatewayAccountCircuitMutationResult, error)
	CompleteGatewayAccountCircuitConfirmation(context.Context, GatewayAccountCircuitCompleteConfirmationInput) (GatewayAccountCircuitMutationResult, error)
	AcquireGatewayAccountCircuitCanaryLease(context.Context, GatewayAccountCircuitAcquireCanaryLeaseInput) (GatewayAccountCircuitMutationResult, error)
	CompleteGatewayAccountCircuitCanary(context.Context, GatewayAccountCircuitCompleteCanaryInput) (GatewayAccountCircuitMutationResult, error)
	RecordGatewayAccountCircuitProtocolModelOpenEvidence(context.Context, GatewayAccountCircuitProtocolModelOpenEvidenceInput) (GatewayAccountCircuitEscalationResult, error)
	ClearGatewayAccountCircuitEscalationEvidence(context.Context, GatewayAccountCircuitClearAccountEscalationEvidenceInput) (bool, error)
}

type GatewayAccountCircuitReconciler interface {
	ReplaceGatewayAccountCircuitDispatchRevision(context.Context, GatewayAccountCircuitReplaceDispatchRevisionInput) (GatewayAccountCircuitMutationResult, error)
	RestoreGatewayAccountCircuit(context.Context, GatewayAccountCircuitRestoreInput) (GatewayAccountCircuitMutationResult, error)
	ReplaceGatewayAccountCircuitAccountDispatchRevision(context.Context, GatewayAccountCircuitReplaceAccountDispatchRevisionInput) (GatewayAccountCircuitAccountRevisionResult, error)
}

type GatewayAccountCircuitStore interface {
	GatewayAccountCircuitReader
	GatewayAccountCircuitTransitionStore
	GatewayAccountCircuitReconciler
}

func GatewayAccountCircuitScopeKey(scope GatewayAccountCircuitScope) (string, error) {
	if err := ValidateGatewayAccountCircuitScope(scope); err != nil {
		return "", err
	}
	parts := []string{string(scope.Kind), scope.AccountRuntimeKey}
	switch scope.Kind {
	case GatewayAccountCircuitScopeAccount:
	case GatewayAccountCircuitScopeAPIKey:
		parts = append(parts, scope.KeyFingerprint)
	case GatewayAccountCircuitScopeProtocolModel:
		parts = append(parts, scope.ProtocolProfile, scope.RequestLane, scope.ModelBucket)
	case GatewayAccountCircuitScopeKeyModel:
		parts = append(parts, scope.KeyFingerprint, scope.ClientModel, scope.CapabilityHash,
			scope.CredentialSourceAccountID, scope.ClientEndpointFamily, scope.FinalUpstreamModel,
			scope.UpstreamEndpointMode)
	}
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		encoded = append(encoded, fmt.Sprintf("%d:%s", len([]byte(part)), part))
	}
	return strings.Join(encoded, "|"), nil
}

func ValidateGatewayAccountCircuitScope(scope GatewayAccountCircuitScope) error {
	if err := validateGatewayAccountCircuitText(scope.AccountRuntimeKey, 1024, "account runtime key"); err != nil {
		return err
	}
	switch scope.Kind {
	case GatewayAccountCircuitScopeAccount:
		if scope.KeyFingerprint != "" || scope.ProtocolProfile != "" || scope.RequestLane != "" || scope.ModelBucket != "" || hasKeyModelScopeFields(scope) {
			return fmt.Errorf("account circuit account scope contains unrelated fields")
		}
	case GatewayAccountCircuitScopeAPIKey:
		if err := validateGatewayAccountCircuitText(scope.KeyFingerprint, 512, "key fingerprint"); err != nil {
			return err
		}
		if scope.ProtocolProfile != "" || scope.RequestLane != "" || scope.ModelBucket != "" || hasKeyModelScopeFields(scope) {
			return fmt.Errorf("account circuit key scope contains unrelated fields")
		}
	case GatewayAccountCircuitScopeProtocolModel:
		if err := validateGatewayAccountCircuitText(scope.ProtocolProfile, 256, "protocol profile"); err != nil {
			return err
		}
		if scope.RequestLane != "text" && scope.RequestLane != "image" {
			return fmt.Errorf("account circuit request lane is invalid")
		}
		if err := validateGatewayAccountCircuitText(scope.ModelBucket, 512, "model bucket"); err != nil {
			return err
		}
		if scope.KeyFingerprint != "" || hasKeyModelScopeFields(scope) {
			return fmt.Errorf("account circuit protocol-model scope contains key fingerprint")
		}
	case GatewayAccountCircuitScopeKeyModel:
		if err := validateGatewayAccountCircuitText(scope.KeyFingerprint, 256, "key fingerprint"); err != nil {
			return err
		}
		for name, value := range map[string]string{
			"client model":                 scope.ClientModel,
			"capability hash":              scope.CapabilityHash,
			"credential source account id": scope.CredentialSourceAccountID,
			"client endpoint family":       scope.ClientEndpointFamily,
			"final upstream model":         scope.FinalUpstreamModel,
			"upstream endpoint mode":       scope.UpstreamEndpointMode,
		} {
			if err := validateGatewayAccountCircuitText(value, 256, name); err != nil {
				return err
			}
		}
		if scope.ProtocolProfile != "" || scope.RequestLane != "" || scope.ModelBucket != "" {
			return fmt.Errorf("account circuit key-model scope contains protocol fields")
		}
	default:
		return fmt.Errorf("account circuit scope kind is invalid")
	}
	return nil
}

func hasKeyModelScopeFields(scope GatewayAccountCircuitScope) bool {
	return scope.ClientModel != "" || scope.CapabilityHash != "" || scope.CredentialSourceAccountID != "" ||
		scope.ClientEndpointFamily != "" || scope.FinalUpstreamModel != "" || scope.UpstreamEndpointMode != ""
}

func GatewayAccountCircuitRuntimeKeyMatchesFamily(target, candidate string) bool {
	if target == "" || candidate == "" {
		return false
	}
	return candidate == target || (!strings.Contains(target, ":authorized:") && strings.HasPrefix(candidate, target+":authorized:"))
}

func GatewayAccountCircuitClosedState(scope GatewayAccountCircuitScope, dispatchRevision int64, generation int, transitionID string, updatedAt time.Time) (GatewayAccountCircuitState, error) {
	scopeKey, err := GatewayAccountCircuitScopeKey(scope)
	if err != nil {
		return GatewayAccountCircuitState{}, err
	}
	if dispatchRevision < 0 || generation < 0 {
		return GatewayAccountCircuitState{}, fmt.Errorf("account circuit closed state revision is invalid")
	}
	if transitionID != "" {
		if err := validateGatewayAccountCircuitText(transitionID, 256, "transition id"); err != nil {
			return GatewayAccountCircuitState{}, err
		}
	}
	return GatewayAccountCircuitState{
		ScopeKey: scopeKey, Scope: scope, Phase: GatewayAccountCircuitPhaseClosed,
		Generation: generation, DispatchRevision: dispatchRevision, TransitionID: transitionID,
		UpdatedAt: updatedAt.UTC(),
	}, nil
}

func CloneGatewayAccountCircuitState(state GatewayAccountCircuitState) GatewayAccountCircuitState {
	clone := state
	clone.Scope = state.Scope
	if state.OpenedAt != nil {
		value := state.OpenedAt.UTC()
		clone.OpenedAt = &value
	}
	if state.RetryAt != nil {
		value := state.RetryAt.UTC()
		clone.RetryAt = &value
	}
	if state.Lease != nil {
		value := *state.Lease
		value.Until = value.Until.UTC()
		clone.Lease = &value
	}
	clone.ChildIncidentIDs = cloneGatewayAccountCircuitStrings(state.ChildIncidentIDs)
	clone.ChildScopeKeys = cloneGatewayAccountCircuitStrings(state.ChildScopeKeys)
	clone.RequiredRecoveryScopeKeys = cloneGatewayAccountCircuitStrings(state.RequiredRecoveryScopeKeys)
	clone.RecoveryEvidenceScopeKeys = cloneGatewayAccountCircuitStrings(state.RecoveryEvidenceScopeKeys)
	return clone
}

func ValidateGatewayAccountCircuitState(state GatewayAccountCircuitState) error {
	scopeKey, err := GatewayAccountCircuitScopeKey(state.Scope)
	if err != nil {
		return err
	}
	if state.ScopeKey != scopeKey || state.Generation < 0 || state.DispatchRevision < 0 || state.LedgerRevision < 0 || state.BackoffAttempt < 0 || state.RecoverySuccessCount < 0 || state.UpdatedAt.IsZero() {
		return fmt.Errorf("account circuit state is invalid")
	}
	if state.DispatchRevision > 0 {
		if err := validateGatewayAccountCircuitText(state.TransitionID, 256, "transition id"); err != nil {
			return err
		}
	}
	switch state.Phase {
	case GatewayAccountCircuitPhaseClosed, GatewayAccountCircuitPhaseSuspect, GatewayAccountCircuitPhaseOpen, GatewayAccountCircuitPhaseHalfOpen, GatewayAccountCircuitPhaseRecovering:
	default:
		return fmt.Errorf("account circuit phase is invalid")
	}
	if state.Lease != nil {
		if err := validateGatewayAccountCircuitLease(*state.Lease); err != nil {
			return err
		}
	}
	if state.Phase == GatewayAccountCircuitPhaseHalfOpen && (state.Lease == nil || (state.Lease.Kind != GatewayAccountCircuitLeaseHalfOpen && state.Lease.Kind != GatewayAccountCircuitLeaseRecovery) || (state.HalfOpenOrigin != GatewayAccountCircuitPhaseOpen && state.HalfOpenOrigin != GatewayAccountCircuitPhaseRecovering)) {
		return fmt.Errorf("half-open account circuit state lease is invalid")
	}
	if state.Phase != GatewayAccountCircuitPhaseHalfOpen && state.HalfOpenOrigin != "" {
		return fmt.Errorf("account circuit half-open origin is invalid")
	}
	if state.HalfOpenOrigin != "" && state.HalfOpenOrigin != GatewayAccountCircuitPhaseOpen && state.HalfOpenOrigin != GatewayAccountCircuitPhaseRecovering {
		return fmt.Errorf("account circuit half-open origin is invalid")
	}
	for _, values := range [][]string{state.ChildIncidentIDs, state.ChildScopeKeys, state.RequiredRecoveryScopeKeys, state.RecoveryEvidenceScopeKeys} {
		if len(values) > GatewayAccountCircuitRuntimeMaxEvidenceScopes || !gatewayAccountCircuitUniqueSortedStrings(values) {
			return fmt.Errorf("account circuit state relation list is invalid")
		}
		for _, value := range values {
			if err := validateGatewayAccountCircuitText(value, 2048, "account circuit relation"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateGatewayAccountCircuitLease(lease GatewayAccountCircuitLease) error {
	if lease.Until.IsZero() {
		return fmt.Errorf("account circuit lease deadline is required")
	}
	if err := validateGatewayAccountCircuitText(lease.ID, 256, "lease id"); err != nil {
		return err
	}
	switch lease.Kind {
	case GatewayAccountCircuitLeaseConfirmation, GatewayAccountCircuitLeaseHalfOpen, GatewayAccountCircuitLeaseRecovery:
		return nil
	default:
		return fmt.Errorf("account circuit lease kind is invalid")
	}
}

func validateGatewayAccountCircuitText(value string, maxBytes int, name string) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("account circuit %s is invalid", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("account circuit %s is invalid", name)
		}
	}
	return nil
}

func cloneGatewayAccountCircuitStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func gatewayAccountCircuitUniqueSortedStrings(values []string) bool {
	if len(values) < 2 {
		return true
	}
	return sort.SliceIsSorted(values, func(left, right int) bool { return values[left] < values[right] }) && !hasGatewayAccountCircuitDuplicate(values)
}

func hasGatewayAccountCircuitDuplicate(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return true
		}
	}
	return false
}
