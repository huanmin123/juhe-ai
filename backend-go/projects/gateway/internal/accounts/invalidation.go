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

// groupAccountStatsDirtyAll mirrors GROUP_ACCOUNT_STATS_DIRTY_ALL
// (group-account-stats-cache.repository.ts:16): the single full-refresh dirty
// marker row the stats worker consumes as "rebuild every group".
const groupAccountStatsDirtyAll = "__all__"

// markAllGroupStatsDirty mirrors markAllGroupAccountStatsDirty
// (group-account-stats-cache.repository.ts:49-51, reached through
// refreshGroupAccountStatsAfterWrite({all:true})): one __all__ upsert covers
// every group's next refresh, same statement shape as markBatchGroupStatsDirty.
func (s *Store) markAllGroupStatsDirty(ctx context.Context, reason string) error {
	ctx = ensureCtx(ctx)
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_account_stats_dirty")+` (group_id, reason, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET
			reason = excluded.reason,
			updated_at = excluded.updated_at`), groupAccountStatsDirtyAll, reason, isoMillis(s.now()))
	return err
}

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

// finishDeleteSideEffects mirrors the post-commit invalidation tail of the
// Node delete flow (account-delete-cleanup.repository.ts:149-158 SQLite /
// :163-172 PG async): the SQLite arm opens with the whole-surface group stats
// dirty marker (refreshGroupAccountStatsAfterWrite({all:true}); the PG async
// arm runs no stats refresh), then one lookup flush per deleted account (the
// soft delete takes the authorization instances with it), the
// group-account-ids and resource-authorization lookup cache flushes, the
// whole-surface runtime invalidation and the authorization quota invalidation
// (invalidateAuthorizationRuntimeAfterBusinessWrite = gateway runtime +
// authorization quota). Every step is best-effort — a failure is logged and
// never reported to the client (the Node warn channels).
func (s *Store) finishDeleteSideEffects(ctx context.Context, accountIDs []string) {
	if !s.pg {
		if err := s.markAllGroupStatsDirty(ctx, accountDeleteRuntimeInvalidationReason); err != nil {
			slog.Warn("账户删除已提交，但分组账户统计全量脏标记失败",
				"event", "account_delete_stats_refresh_failed",
				"accountCount", len(accountIDs), "error", err)
		}
	}
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
	// invalidateGroupAccountIdsCache +
	// clearResourceAuthorizationLookupCaches
	// (account-delete-cleanup.repository.ts:154-155,169-170).
	if err := s.invalidator.InvalidateGroupAccountIds(); err != nil {
		slog.Warn("账户删除已提交，但分组账户 ID 缓存失效失败",
			"event", "account_delete_group_account_ids_invalidation_failed",
			"accountCount", len(accountIDs), "error", err)
	}
	if err := s.invalidator.ClearResourceAuthorizationLookupCaches(); err != nil {
		slog.Warn("账户删除已提交，但资源授权 lookup 缓存清理失败",
			"event", "account_delete_authorization_lookup_invalidation_failed",
			"accountCount", len(accountIDs), "error", err)
	}
	if err := s.invalidator.InvalidateGatewayRuntime(accountDeleteRuntimeInvalidationReason); err != nil {
		slog.Warn("账户删除已提交，但网关运行时缓存失效失败",
			"event", "account_delete_runtime_invalidation_failed",
			"accountCount", len(accountIDs), "error", err)
	}
	// invalidateAuthorizationRuntimeAfterBusinessWrite('account_deleted')
	// (:56-59,157,202): the gateway-runtime arm above plus the authorization
	// quota invalidation.
	if err := s.invalidator.InvalidateAuthorizationQuota(accountDeleteRuntimeInvalidationReason); err != nil {
		slog.Warn("账户删除已提交，但授权额度缓存失效失败",
			"event", "account_delete_authorization_quota_invalidation_failed",
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
