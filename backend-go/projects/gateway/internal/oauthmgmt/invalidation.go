package oauthmgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
)

// Rotation write-path invalidation + dispatch revision fence (T2 audit).
//
// Node authority: oauth-credential-rotation.repository.ts:223-226 runs the
// post-commit double notification on every applied rotation:
//
//	invalidateAccountLookupCache(result.id)
//	invalidateGatewayRuntimeAfterBusinessWrite('oauth_credentials_rotated')
//
// and :202-214 advances the account circuit dispatch revision family inside
// the rotation transaction when the upstream connection identity changed
// (advanceAccountCircuitDispatchRevisionFamilyInTransaction, transitionId
// newId('dispatch'), nowMs = Date.parse(updatedAt)). The Go port injects both
// collaborators through the narrow interfaces below so the route-family tests
// can script them; the composition root wires the accounts store (the
// AdvanceDispatchRevisionFamily export) and the shared K5 bus adapter.

// CacheInvalidator is the post-commit invalidation port of the rotation. The
// Node origin publishes only the runtime channel; the Go port also publishes
// the API-key validation channel with the same reason (a deliberate superset
// per the T2 audit: the unscoped reason renders as a full validation-cache
// clear in the runtime cache subscriber, the same shape the Node
// runtime_state replay produces for a reason without an apiKeyId).
type CacheInvalidator interface {
	// InvalidateAccountLookup mirrors Node invalidateAccountLookupCache
	// (repository-lookups.ts:473) for the rotated account.
	InvalidateAccountLookup(accountID string) error
	// InvalidateRuntime mirrors invalidateGatewayRuntimeAfterBusinessWrite.
	InvalidateRuntime(reason string) error
	// InvalidateAPIKeyValidation mirrors the validation-cache channel.
	InvalidateAPIKeyValidation(reason string) error
}

// DispatchRevisionAdvancer is the narrow in-transaction circuit fence port
// (Node advanceAccountCircuitDispatchRevisionFamilyInTransaction). It
// resolves the authorization family root, locks parent → child, advances
// every family member's dispatch_revision and lands the pending
// dispatch_revision_changed outbox rows. A failure rolls the rotation back.
type DispatchRevisionAdvancer interface {
	AdvanceDispatchRevisionFamily(ctx context.Context, tx *sql.Tx, accountID, transitionID string, nowMs int64) error
}

// WithCacheInvalidator wires the post-commit invalidation channels.
func WithCacheInvalidator(invalidator CacheInvalidator) Option {
	return func(s *Store) {
		if invalidator != nil {
			s.invalidator = invalidator
		}
	}
}

// WithDispatchRevisionAdvancer wires the in-transaction circuit fence (the
// accounts store export satisfies it).
func WithDispatchRevisionAdvancer(advancer DispatchRevisionAdvancer) Option {
	return func(s *Store) {
		if advancer != nil {
			s.revisionAdvancer = advancer
		}
	}
}

// RotationRuntimeInvalidationReason mirrors the Node reason string.
const RotationRuntimeInvalidationReason = "oauth_credentials_rotated"

// circuitCredentialOwnerIdentity mirrors accountCircuitCredentialOwnerIdentity
// (domain/account-circuit-owner.ts): only the upstream connection identity
// keys participate in the rotation fence — routing preferences, inspection
// rules and request overrides live in the same credentials JSON but must not
// revive an OPEN transport circuit when they change.
func circuitCredentialOwnerIdentity(credentials map[string]any) map[string]any {
	identityKeys := []string{
		"api_key",
		"api_keys",
		"access_token",
		"refresh_token",
		"client_id",
		"client_secret",
		"id_token",
		"account_id",
		"chatgpt_user_id",
		"quota_project_id",
		"base_url",
		"supported_endpoint_modes",
	}
	identity := map[string]any{}
	if credentials == nil {
		return identity
	}
	for _, key := range identityKeys {
		if value, exists := credentials[key]; exists {
			identity[key] = value
		}
	}
	return identity
}

// circuitCredentialIdentityChanged mirrors the Node
// !isDeepStrictEqual(ownerIdentity(current), ownerIdentity(next)) gate.
func circuitCredentialIdentityChanged(current, next map[string]any) bool {
	encode := func(value map[string]any) (string, bool) {
		raw, err := json.Marshal(value)
		return string(raw), err == nil
	}
	left, leftOK := encode(circuitCredentialOwnerIdentity(current))
	right, rightOK := encode(circuitCredentialOwnerIdentity(next))
	return leftOK && rightOK && left != right
}

// finishRotationSideEffects runs the post-commit double notification for an
// applied rotation (best-effort: channel failures surface as logs through the
// bus adapters, never as rotation errors — the Node notify helpers log and
// swallow).
func (s *Store) finishRotationSideEffects(result *RotationResult) {
	if s.invalidator == nil || result == nil || !result.Changed {
		return
	}
	if err := s.invalidator.InvalidateAccountLookup(result.ID); err != nil {
		s.warnRotationChannel("account_lookup", result.ID, err)
	}
	if err := s.invalidator.InvalidateRuntime(RotationRuntimeInvalidationReason); err != nil {
		s.warnRotationChannel("gateway_runtime", result.ID, err)
	}
	if err := s.invalidator.InvalidateAPIKeyValidation(RotationRuntimeInvalidationReason); err != nil {
		s.warnRotationChannel("gateway_api_key_validation", result.ID, err)
	}
}

func (s *Store) warnRotationChannel(channel, accountID string, err error) {
	slog.Warn("OAuth 凭据轮换已提交，但缓存失效通道失败",
		"event", "oauth_credentials_rotated_invalidation_failed",
		"channel", channel, "accountId", accountID, "error", err)
}
