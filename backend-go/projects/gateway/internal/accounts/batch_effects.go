package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
)

// Batch edit post-commit side effects (docs/bug/问题-0172 T1): the Node
// account-batch-update.repository.ts runs a four-part chain around the batch
// transaction and the first Go port skipped all of it. This file carries the
// ports and the equivalents; the call sites live in BatchUpdate (batch.go).
//
// Node → Go mapping (failure semantics per row):
//
//	in-transaction  account-batch-update.repository.ts:167-174 +
//	                account-circuit-control-plane.repository.ts:428-489,494-546
//	                → advanceBatchDispatchRevisionFamily (error aborts the batch)
//	post-commit     account-batch-update.repository.ts:216
//	                invalidateAccountLookupCache → CacheInvalidator.InvalidateAccountLookup
//	post-commit     account-batch-update.repository.ts:217-230
//	                refreshGroupAccountStatsAfterWriteAsync → markBatchGroupStatsDirty
//	                (dirty marking only; group-account-stats-cache.repository.ts:40-74)
//	post-commit     account-batch-update.repository.ts:231-241
//	                invalidateGatewayRuntimeAfterBusinessWrite → CacheInvalidator.InvalidateGatewayRuntime
//
// Node account-batch-edit.service.ts:97,398-412 additionally cleans balance
// snapshots for proxy-changed accounts (cleanupChangedBalanceSnapshots); the Go
// side has no balance snapshot mechanism yet, so that step stays unported.

// CacheInvalidator is the nil-safe post-commit invalidation port of the batch
// edit (mirrors the authsys SetCacheInvalidator pattern). Compose wiring is a
// registered handover of docs/bug/问题-0172 T1: a nil port keeps the batch
// self-contained (tests, and production until the wiring wave lands).
type CacheInvalidator interface {
	// InvalidateAccountLookup mirrors Node invalidateAccountLookupCache
	// (repository-lookups.ts:473): flush the per-account lookup cache entry.
	InvalidateAccountLookup(accountID string) error
	// InvalidateGatewayRuntime mirrors Node
	// invalidateGatewayRuntimeAfterBusinessWrite
	// (account-runtime-mutation-helpers.ts:72): one whole-surface runtime
	// invalidation per committed batch.
	InvalidateGatewayRuntime(reason string) error
}

// SetCacheInvalidator wires the post-commit invalidation channels (compose
// handover; nil keeps the side-effect chain cache-silent).
func (s *Store) SetCacheInvalidator(invalidator CacheInvalidator) {
	s.invalidator = invalidator
}

// batchDispatchRevision carries one proxy-changed account through the family
// advance: Node passes accountId + `${batchId}:${accountId}` +
// Date.parse(updatedAt) (account-batch-update.repository.ts:168-173).
type batchDispatchRevision struct {
	accountID    string
	transitionID string
	nowMS        int64
}

// advanceBatchDispatchRevisionFamily mirrors
// advanceAccountCircuitDispatchRevisionFamilyInTransaction: resolve the
// authorization family root, lock parent → child, re-verify the source pointer
// fail-closed, then advance every non-deleted family member. It must run
// inside the batch transaction so a failure rolls the whole batch back.
func (s *Store) advanceBatchDispatchRevisionFamily(ctx context.Context, q queryer, in batchDispatchRevision) error {
	var sourceID sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT authorization_instance_source_account_id FROM `+s.table("accounts")+`
		WHERE id = ?`), in.accountID).Scan(&sourceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("AI 账户不存在：%s", in.accountID)
		}
		return err
	}
	familyRootID := in.accountID
	if sourceID.Valid && sourceID.String != "" {
		familyRootID = sourceID.String
	}
	// Parent lock before the child row: account update transactions can hold
	// either row already, and inverting the order deadlocks under concurrency
	// (account-circuit-control-plane.repository.ts:433-454). SQLite ignores the
	// clause via forUpdate; the single-writer transaction serializes it.
	if err := s.lockBatchDispatchRow(ctx, q, familyRootID); err != nil {
		return err
	}
	if in.accountID != familyRootID {
		if err := s.lockBatchDispatchRow(ctx, q, in.accountID); err != nil {
			return err
		}
	}
	var recheckID sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT authorization_instance_source_account_id FROM `+s.table("accounts")+`
		WHERE id = ?`), in.accountID).Scan(&recheckID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("AI 账户不存在：%s", in.accountID)
		}
		return err
	}
	recheckRoot := in.accountID
	if recheckID.Valid && recheckID.String != "" {
		recheckRoot = recheckID.String
	}
	if recheckRoot != familyRootID {
		return errors.New("账户授权关系在锁定期间发生变化，请重试")
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY id ASC`+s.forUpdate()), familyRootID)
	if err != nil {
		return err
	}
	instanceIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		instanceIDs = append(instanceIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, accountID := range append([]string{familyRootID}, instanceIDs...) {
		transitionID := in.transitionID
		if accountID != familyRootID {
			transitionID = batchFamilyDispatchTransitionID(in.transitionID, accountID)
		}
		if err := s.advanceBatchDispatchRevision(ctx, q, accountID, transitionID, in.nowMS); err != nil {
			return err
		}
	}
	return nil
}

// advanceBatchDispatchRevision mirrors
// advanceAccountCircuitDispatchRevisionInTransaction (+ the SQLite variant
// :491-546): dedupe replay is idempotent, the revision bump is fenced with the
// read revision (Node UPDATE ... + 1 with the Go control-plane store's CAS
// fence, circuit_control_plane/store.go AdvanceDispatchRevision) and the
// outbox row lands with the same column set and projection key.
func (s *Store) advanceBatchDispatchRevision(ctx context.Context, q queryer, accountID, transitionID string, nowMS int64) error {
	dedupeKey := "dispatch:" + transitionID
	var replayEventType, replayAccountID, replayRuntimeKey string
	err := q.QueryRowContext(ctx, s.bind(`SELECT event_type, account_id, account_runtime_key FROM `+s.table("account_circuit_outbox")+`
		WHERE projection_key = ? AND dedupe_key = ?`), circuitcontrolplane.ProjectionKey, dedupeKey).
		Scan(&replayEventType, &replayAccountID, &replayRuntimeKey)
	if err == nil {
		if replayEventType != "dispatch_revision_changed" || replayAccountID != accountID || replayRuntimeKey != accountID {
			return errors.New("账户 circuit outbox dedupe key 与既有事件身份冲突")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var revision int64
	if err := q.QueryRowContext(ctx, s.bind(`SELECT dispatch_revision FROM `+s.table("accounts")+`
		WHERE id = ?`+s.forUpdate()), accountID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("AI 账户不存在：%s", accountID)
		}
		return err
	}
	exec, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET dispatch_revision = dispatch_revision + 1
		WHERE id = ? AND dispatch_revision = ?`), accountID, revision)
	if err != nil {
		return err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return errors.New("账户 dispatch revision 推进冲突：" + accountID)
	}
	revision++
	eventID, err := batchCircuitEventID()
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_circuit_outbox")+`
		(event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		 circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
		 ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)`),
		eventID, circuitcontrolplane.ProjectionKey, dedupeKey, accountID, accountID,
		transitionID, revision, nowMS, nowMS, nowMS)
	return err
}

// lockBatchDispatchRow mirrors lockAccountDispatchRevision
// (account-circuit-control-plane.repository.ts:993-1000): the existence check
// rides the row lock.
func (s *Store) lockBatchDispatchRow(ctx context.Context, q queryer, accountID string) error {
	var one int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("accounts")+` WHERE id = ?`+s.forUpdate()), accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("AI 账户不存在：%s", accountID)
	}
	return err
}

func (s *Store) forUpdate() string {
	if s.pg {
		return " FOR UPDATE"
	}
	return ""
}

// batchFamilyDispatchTransitionID mirrors familyDispatchTransitionId
// (account-circuit-control-plane.repository.ts:1282-1284).
func batchFamilyDispatchTransitionID(transitionID, accountID string) string {
	sum := sha256.Sum256([]byte(transitionID + "\x00" + accountID))
	return "dispatch-family:" + hex.EncodeToString(sum[:])
}

// batchCircuitEventID renders the Node randomUUID() outbox event id slot
// (random 128 bits, hex-encoded like the control-plane store token).
func batchCircuitEventID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// markBatchGroupStatsDirty mirrors refreshGroupAccountStatsAfterWriteAsync's
// account branch (account-batch-update.repository.ts:217-230 →
// group-account-stats-write-invalidation.ts:39-66 →
// group-account-stats-cache.repository.ts:61-74,40-55): project the account
// ids onto group ids, then upsert the dirty markers (mark, never recompute).
// groups.Store.markStatsDirty runs the identical upsert but is unexported, so
// the equivalent SQL lives here (registered decision of 问题-0172 T1); the
// refresh worker itself belongs to the J5 stats slice.
func (s *Store) markBatchGroupStatsDirty(ctx context.Context, accountIDs []string, reason string) error {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT DISTINCT group_id FROM `+s.table("group_accounts")+`
		WHERE account_id IN (`+placeholders(len(accountIDs))+`)`), anySlice(accountIDs)...)
	if err != nil {
		return err
	}
	groupIDs := []string{}
	for rows.Next() {
		var groupID string
		if err := rows.Scan(&groupID); err != nil {
			rows.Close()
			return err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	updatedAt := isoMillis(s.now())
	for _, groupID := range groupIDs {
		if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_account_stats_dirty")+` (group_id, reason, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(group_id) DO UPDATE SET
				reason = excluded.reason,
				updated_at = excluded.updated_at`), groupID, reason, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// finishBatchUpdateSideEffects mirrors the post-commit tail of
// updateAccountsBatchAsync (account-batch-update.repository.ts:216-241):
// lookup invalidation per changed account, stats dirty marking and one runtime
// invalidation, every step best-effort — a failure is logged and never
// reported to the client (the Node warn channels).
func (s *Store) finishBatchUpdateSideEffects(ctx context.Context, batchID string, changedAccountIDs, statsAccountIDs, gatewayAccountIDs []string) {
	ctx = ensureCtx(ctx)
	if s.invalidator != nil {
		for _, accountID := range changedAccountIDs {
			if err := s.invalidator.InvalidateAccountLookup(accountID); err != nil {
				// Node cannot fail here (local cache op); the port can, so the
				// warn event is the Go-side analogue.
				slog.Warn("批量编辑已提交，但账户 lookup 缓存失效失败",
					"event", "account_batch_update_lookup_invalidation_failed",
					"batchId", batchID, "accountId", accountID, "error", err)
			}
		}
	}
	if len(statsAccountIDs) > 0 {
		if err := s.markBatchGroupStatsDirty(ctx, statsAccountIDs, "account_batch_updated"); err != nil {
			slog.Warn("批量编辑已提交，但分组账户统计脏标记失败",
				"event", "account_batch_update_stats_refresh_failed",
				"batchId", batchID, "accountCount", len(statsAccountIDs), "error", err)
		}
	}
	if len(gatewayAccountIDs) > 0 && s.invalidator != nil {
		if err := s.invalidator.InvalidateGatewayRuntime("account_batch_updated"); err != nil {
			slog.Warn("批量编辑已提交，但网关运行时缓存失效失败",
				"event", "account_batch_update_runtime_invalidation_failed",
				"batchId", batchID, "accountCount", len(gatewayAccountIDs), "error", err)
		}
	}
}
