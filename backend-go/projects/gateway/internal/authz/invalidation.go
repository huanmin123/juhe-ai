// Post-commit cache invalidation fan-out for the authz business writes
// (BUG-0175 D-56/D-65/D-115). Node runs the whole-surface refresh after every
// committed authorization write:
//
//	refreshAfterResourceAuthorizationBusinessWrite
//	  (resource-authorization-write.repository.ts:2706-2731) — create :343-345
//	  (only when created || previousStatus), patch :769/:812-814 (updated only),
//	  revoke :543/:573-575 (updated only);
//	refreshAfterResourceAuthorizationReturnedWrite
//	  (resource-authorization-return.repository.ts:791-800) — the direct-grant
//	  return :157/:186-188 (updated only), the account instance return
//	  :236/:286-289 and the group return :340/:381-384 (receipt resolved).
//
// Both archived helpers run the same six-piece set, keyed by the reason
// string; the return helper only pins the reason to
// 'resource_authorization_returned':
//
//  1. refreshGroupAccountStatsAfterWriteAsync({all: true, reason})
//  2. clearGatewayApiKeyValidationCache()
//  3. invalidateGroupAccountIdsCache()
//  4. clearResourceAuthorizationLookupCaches()
//  5. notifyGatewayRuntimeCacheInvalidation(reason)
//  6. notifyAuthorizationQuotaCacheInvalidation(reason)
//
// Go channel mapping (established precedents, no bus semantics change):
//
//	1 → StatsDirtyMarker.MarkAllGroupAccountStatsDirty (the C9 groupdirtycursor
//	    store covers both dialects; same wiring systemteams.WithSideEffects
//	    consumes at compose.go).
//	2 → bus Invalidate(TopicGatewayAPIKeyValidation, reason) — the topic the
//	    API-key validation cache subscribers watch (authsysBusInvalidator).
//	3 → folded into the runtime topic publish: the Go gateway has no
//	    process-local group-account-ids cache, and groups.Store consumes the
//	    invalidation through the shared runtime topic (the same fold
//	    accountsBusInvalidator.InvalidateGroupAccountIds documents for the
//	    account delete arm, BUG-0162).
//	4 → documented no-op: no Go process-local authorization lookup cache
//	    exists yet (same as accountsBusInvalidator
//	    .ClearResourceAuthorizationLookupCaches).
//	5 → bus Invalidate(TopicGatewayRuntime, reason).
//	6 → bus Invalidate(TopicAuthorizationQuota, reason).
//
// The fan-out runs after the transaction commit and is best-effort: a failed
// channel is logged and the remaining channels still run (the committed write
// never rolls back and the mutation result never reports the failure — the
// accounts finishXxxSideEffects precedent).
package authz

import (
	"context"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// Node reason strings for the four business-write reasons and the return
// fan-out (resource-authorization-write/return.repository.ts).
const (
	invalidationReasonCreated  = "resource_authorization_created"
	invalidationReasonUpdated  = "resource_authorization_updated"
	invalidationReasonRevoked  = "resource_authorization_revoked"
	invalidationReasonReturned = "resource_authorization_returned"
)

// StatsDirtyMarker is the committed-write group-stats refresh arm
// (piece 1). *group_dirty_cursor.Store satisfies it through the compose
// wiring; nil keeps the arm off so direct NewStore construction (tests,
// partial compositions) keeps its current behavior.
type StatsDirtyMarker interface {
	MarkAllGroupAccountStatsDirty(ctx context.Context, reason string) error
}

// RuntimeInvalidator is the cache invalidation bus port (pieces 2/3/5/6).
// *inval.Bus satisfies it directly (the systemteams C9 RuntimeInvalidator
// shape — no adapter translation); nil keeps invalidation off.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// AttachWriteInvalidator wires the committed-write invalidation ports
// (compose-root handover, the Attach* convention of this store). Both ports
// stay nil-tolerant: an unwired port silently skips its arms.
func (s *Store) AttachWriteInvalidator(stats StatsDirtyMarker, invalidator RuntimeInvalidator) {
	s.statsDirty = stats
	s.invalidator = invalidator
}

// invalidateAfterBusinessWrite runs the six-piece post-commit fan-out with
// the Node reason string. Call it only after a committed business write —
// never on the unchanged/not_found/conflict outcomes the archive returns
// before its refresh tail.
func (s *Store) invalidateAfterBusinessWrite(ctx context.Context, reason string) {
	ctx = ensureCtx(ctx)
	if s.statsDirty != nil {
		if err := s.statsDirty.MarkAllGroupAccountStatsDirty(ctx, reason); err != nil {
			slog.Warn("授权写入已提交，但分组账户统计全量脏标记失败",
				"event", "authz_write_stats_dirty_failed",
				"reason", reason, "error", err)
		}
	}
	if s.invalidator == nil {
		return
	}
	// Piece 2: clearGatewayApiKeyValidationCache.
	s.invalidator.Invalidate(inval.TopicGatewayAPIKeyValidation, reason)
	// Pieces 3+5: invalidateGroupAccountIdsCache rides the runtime topic
	// (see the package mapping above); notifyGatewayRuntimeCacheInvalidation.
	s.invalidator.Invalidate(inval.TopicGatewayRuntime, reason)
	// Piece 4: clearResourceAuthorizationLookupCaches — no Go process-local
	// lookup cache exists yet; the documented no-op needs no publish.
	// Piece 6: notifyAuthorizationQuotaCacheInvalidation.
	s.invalidator.Invalidate(inval.TopicAuthorizationQuota, reason)
}
