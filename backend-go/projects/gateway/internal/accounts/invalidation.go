package accounts

import (
	"context"
	"database/sql"
	"log/slog"
)

// Management write-path post-commit invalidation (T2 audit): the Node
// management repositories keep the process-local lookup cache and the gateway
// runtime cache in sync after every committed write. Go has no management
// account lookup cache yet (the port stays a documented hook), so the only
// live channel is the K5 invalidation bus runtime topic.
//
// Node mapping (account-management-patch.repository.ts:1877-1896,
// account-delete-cleanup.repository.ts:145-201):
//
//	patch  accountLookupAffected (name / accountExpiresAt / tags changed)
//	       → CacheInvalidator.InvalidateAccountLookup
//	patch  gatewayRuntimeAffected (Node gatewayFields ∪ credentials ∪
//	       clearFailureState changed) → notifyGatewayRuntimeCacheInvalidation
//	       → CacheInvalidator.InvalidateGatewayRuntime('account_management_patch')
//	delete invalidateAccountLookupCache per deleted id +
//	       invalidateGatewayRuntimeAfterBusinessWrite('account_deleted')
//
// The lock family stays cache-silent on Node (account-lock.repository.ts has
// no invalidation on setAccountLockAsync / updateAccountLockConfigAsync; only
// settleAccountLockDeadlineAsync notifies 'account_lock_deadline'), so Go
// keeps SetLock / LockConfig silent for parity.

// accountPatchLookupFields mirror the Node accountLookupAffected condition
// (account-management-patch.repository.ts:972,1104): name / expiry / tags.
var accountPatchLookupFields = map[string]bool{
	"name":             true,
	"accountExpiresAt": true,
	"tags":             true,
}

// accountPatchGatewayRuntimeFields mirror the Node gatewayFields set
// (account-management-patch.repository.ts:893-900) plus credentialsChanged
// and the clearFailureState branch (lines 891-899, 1215). Fields outside the
// Go basic-edit surface are kept in the set so a later Go field addition
// inherits the Node invalidation condition automatically.
var accountPatchGatewayRuntimeFields = map[string]bool{
	"status":                  true,
	"schedulable":             true,
	"concurrencyLimit":        true,
	"priority":                true,
	"superPriorityEnabled":    true,
	"fallbackEnabled":         true,
	"proxyProfileId":          true,
	"clientCompatibility":     true,
	"supportedModels":         true,
	"modelMappings":           true,
	"healthCheckModel":        true,
	"healthCheckEndpointMode": true,
	"availabilitySchedule":    true,
	"accountExpiresAt":        true,
	"temporaryUnavailableContinuousProbeEnabled": true,
	"runtimeState": true,
	// credentialsChanged (line 898) and the clearFailureState outcome
	// (gatewayRuntimeAffected: true at line 1215).
	"credentials":       true,
	"clearFailureState": true,
}

// accountPatchRuntimeInvalidationReason mirrors the Node reason string.
const accountPatchRuntimeInvalidationReason = "account_management_patch"

// accountDeleteRuntimeInvalidationReason mirrors the Node reason string.
const accountDeleteRuntimeInvalidationReason = "account_deleted"

// finishPatchSideEffects mirrors applyAccountPatchPostCommitEffects' sync arm:
// per-account lookup flush and the conditional gateway runtime invalidation,
// best-effort — a channel failure is logged and never reported to the client
// (the Node warn channels).
func (s *Store) finishPatchSideEffects(result *PatchResult) {
	if s.invalidator == nil || result == nil {
		return
	}
	lookupAffected := false
	runtimeAffected := false
	for _, field := range result.ChangedFields {
		if accountPatchLookupFields[field] {
			lookupAffected = true
		}
		if accountPatchGatewayRuntimeFields[field] {
			runtimeAffected = true
		}
	}
	// Node gatewayRuntimeAffected also fires on groupChanged and
	// credentialsChanged directly
	// (account-management-patch.repository.ts:898-900); the credentials arm
	// already lands through the "credentials" field entry above.
	if result.GroupChanged {
		runtimeAffected = true
	}
	if lookupAffected {
		if err := s.invalidator.InvalidateAccountLookup(result.ID); err != nil {
			slog.Warn("账户编辑已提交，但账户 lookup 缓存失效失败",
				"event", "account_management_patch_lookup_invalidation_failed",
				"accountId", result.ID, "error", err)
		}
	}
	if runtimeAffected {
		if err := s.invalidator.InvalidateGatewayRuntime(accountPatchRuntimeInvalidationReason); err != nil {
			slog.Warn("账户编辑已提交，但网关运行时缓存失效失败",
				"event", "account_management_patch_runtime_invalidation_failed",
				"accountId", result.ID, "error", err)
		}
	}
}

// finishDeleteSideEffects mirrors the owner-mode delete post-commit tail
// (account-delete-cleanup.repository.ts:197-201): one lookup flush per
// deleted account (the soft delete takes the authorization instances with it)
// plus one whole-surface runtime invalidation.
func (s *Store) finishDeleteSideEffects(ctx context.Context, accountIDs []string) {
	_ = ctx
	if s.invalidator == nil {
		return
	}
	for _, accountID := range accountIDs {
		if err := s.invalidator.InvalidateAccountLookup(accountID); err != nil {
			slog.Warn("账户删除已提交，但账户 lookup 缓存失效失败",
				"event", "account_delete_lookup_invalidation_failed",
				"accountId", accountID, "error", err)
		}
	}
	if err := s.invalidator.InvalidateGatewayRuntime(accountDeleteRuntimeInvalidationReason); err != nil {
		slog.Warn("账户删除已提交，但网关运行时缓存失效失败",
			"event", "account_delete_runtime_invalidation_failed",
			"accountCount", len(accountIDs), "error", err)
	}
}

// AdvanceDispatchRevisionFamily exports the in-transaction dispatch revision
// family advance for the oauthmgmt credential-rotation fence (Node
// oauth-credential-rotation.repository.ts:202-214 →
// advanceAccountCircuitDispatchRevisionFamilyInTransaction). It resolves the
// authorization family root, locks parent → child, then advances every family
// member's dispatch_revision and lands one pending dispatch_revision_changed
// outbox row per member (the shared gatewaycircuit dispatchRevision
// semantics via the circuit control-plane store; the gatewaycircuit package
// itself stays import-only). It must run inside the caller's transaction.
func (s *Store) AdvanceDispatchRevisionFamily(ctx context.Context, tx *sql.Tx, accountID, transitionID string, nowMs int64) error {
	return s.advanceBatchDispatchRevisionFamily(ctx, tx, batchDispatchRevision{
		accountID:    accountID,
		transitionID: transitionID,
		nowMS:        nowMs,
	})
}
