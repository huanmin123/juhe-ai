package accounts

import (
	"context"
	"log/slog"
)

// RuntimeResetEffects is the narrow cross-package port the runtime-reset
// endpoint (维护者 6f9739e96) needs into the gateway runtime packages. The Go
// semantics already exist — the composition root bridges each method:
//
//	ClearAccountRuntimeAvailability      → gatewayaccounteffects runtime key clear
//	                                       (runtimekeys.GatewayAccountRuntimeClearTarget /
//	                                       ClearKeysForAccount over the runtime-state store;
//	                                       Node clearGatewayAccountRuntimeAvailabilityAsync)
//	ClearNormalRouteLatencyDegradation   → gatewayproxyhealth.LatencyDegradationService.
//	                                       ClearNormalRouteLatencyDegradationForAccount
//	RevalidateAccountAPIKeyRuntimePool   → the api key runtime pool probe scheduler
//	                                       (Node revalidateAccountApiKeyRuntimePoolAsync,
//	                                       account_api_key_runtime_states table)
//	LoadAPIKeyTransientStates            → gatewayaccounteffects transient store LoadMany
//	                                       (RedisAccountApiKeyTransientStateStore or the
//	                                       process-local fallback)
//	ClearAPIKeyFailureGuard              → gatewayaccounteffects AccountAPIKeyFailureGuard.
//	                                       ClearFailureGuard (process-local suppressions)
//	ClearAPIKeyTransientFailure          → gatewayaccounteffects AccountAPIKeyFailureGuard.
//	                                       ClearTransientFailure (Redis tombstone)
//	DispatchAccountHealthCheck           → the health-check dispatcher
//	                                       (Node dispatchAccountHealthCheck,
//	                                       internal-api service)
//	AuthorizationQuotaExceeded           → gatewayquota.AuthorizationQuotaService /
//	                                       StatsStore cost read (Node
//	                                       authorizationQuotaExceeded)
//	APIKeyPoolAllUnavailable             → the api key runtime pool summary projection
//	                                       (Node loadAccountApiKeyRuntimeSummariesByAccountIdsAsync,
//	                                       allUnavailable flag)
//
// A nil port keeps the endpoint self-contained for tests: runtime clears
// report unchanged/zero, the quota gate reports false and the health-check
// dispatch is skipped. Production assembly MUST wire a real bridge — see the
// migration report's runtime-reset port assembly table.
type RuntimeResetEffects interface {
	ClearAccountRuntimeAvailability(ctx context.Context, input RuntimeAvailabilityClearInput) (RuntimeAvailabilityClearResult, error)
	ClearNormalRouteLatencyDegradation(ctx context.Context, systemAccountID, accountID string) (int64, error)
	RevalidateAccountAPIKeyRuntimePool(ctx context.Context, accountID string, expectedConfigRevision int64) (AccountAPIKeyRuntimeRevalidation, error)
	LoadAPIKeyTransientStates(ctx context.Context, accountID string, keyFingerprints []string) ([]AccountAPIKeyTransientSelectionState, error)
	ClearAPIKeyFailureGuard(accountID, keyFingerprint string, transientGeneration *int64) bool
	ClearAPIKeyTransientFailure(ctx context.Context, accountID, keyFingerprint string, transientGeneration *int64) (bool, error)
	DispatchAccountHealthCheck(accountID, reason string)
	AuthorizationQuotaExceeded(ctx context.Context, input AuthorizationQuotaCheckInput) (bool, error)
	APIKeyPoolAllUnavailable(ctx context.Context, accountID string) (bool, error)
}

// RuntimeAvailabilityClearInput mirrors AccountRuntimeAvailabilityClearTarget
// (db-service-ipc.ts normalizeAccountRuntimeClearTarget).
type RuntimeAvailabilityClearInput struct {
	AccountID string
	// AuthorizedBinding carries the grantee binding for authorized instances
	// so the adapter can clear the instance-scoped runtime keys too.
	AuthorizedBinding *RuntimeAuthorizedBinding
	// IncludeBaseAccountKey mirrors includeBaseAccountKey (false for
	// authorized instances).
	IncludeBaseAccountKey bool
	// PreserveConfiguredPolicyAvoidance mirrors preserveConfiguredPolicyAvoidance.
	PreserveConfiguredPolicyAvoidance bool
}

// RuntimeAuthorizedBinding mirrors the authorizedBinding triple of the Node
// clear target.
type RuntimeAuthorizedBinding struct {
	SystemAccountID        string
	GroupID                string
	AccountAuthorizationID string
}

// RuntimeAvailabilityClearResult mirrors the clear outcome tuple the reset
// service consumes: cleared + per-key failures.
type RuntimeAvailabilityClearResult struct {
	Cleared    bool
	FailedKeys []string
}

// AccountAPIKeyRuntimeRevalidation mirrors AccountApiKeyRuntimeRevalidateResult.
type AccountAPIKeyRuntimeRevalidation struct {
	Eligible bool
	Changed  int
	Reason   string
}

// AccountAPIKeyTransientSelectionState mirrors the consumed projection of
// AccountApiKeyRuntimeSelectionState.
type AccountAPIKeyTransientSelectionState struct {
	KeyFingerprint      string
	TransientGeneration int64
	HasGeneration       bool
}

// AuthorizationQuotaCheckInput mirrors the inputs of the Node
// authorizationQuotaExceeded helper: the direct account-authorization limits
// plus the effective team grant.
type AuthorizationQuotaCheckInput struct {
	AuthorizationID        string
	GranteeSystemAccountID string
	EffectiveSourceTeamID  string
	AuthorizationExpiresAt string
	AuthorizationStatus    string
}

// SetRuntimeResetEffects wires the runtime port (composition-root handover;
// nil keeps the reset self-contained).
func (s *Store) SetRuntimeResetEffects(effects RuntimeResetEffects) {
	s.runtimeEffects = effects
}

// runtimeResetEffectsOrNil returns the wired port or nil.
func (s *Store) runtimeResetEffectsOrNil() RuntimeResetEffects {
	if s.runtimeEffects == nil {
		slog.Debug("runtime-reset effects port not wired; runtime surfaces stay untouched")
	}
	return s.runtimeEffects
}
