package accounts

import (
	"context"
	"log/slog"
	"time"
)

// 账户保存后的余额快照旧代次清理端口（Node→Go 迁移缺口登记项 2）。
//
// 归档依据 backend/src/modules/accounts/：
//   - account-balance-snapshot-cleanup.service.ts:220-224
//     cleanupAccountBalanceSnapshotAfterSave（保存已提交后异步删除被取代的
//     旧余额快照，读取侧 isSuppressed 屏蔽，重试队列有限重试）；
//   - accounts.routes.ts:355-364 PATCH 接线：balanceIdentityChanged 时以
//     accountId + configRevision + reason 调用，reason 由
//     balanceAutoDisabledForMultipleApiKeys 选拣 multiple_api_keys /
//     balance_configuration_changed（归档 validateAccountBalanceCapability
//     恒返回 false，Go 侧 reason 恒为 balance_configuration_changed）。
//
// Go 侧余额快照读取面已由 M11 承载（m11_balance.go 读
// account_usage_snapshots），删除执行器与屏蔽读取属于组合根装配，因此这里
// 只保留窄接口端口——对照 CacheInvalidator / batch_effects.go 的注入模式：
// nil 端口保持本包自包含（测试与未装配部署下清理静默跳过）。

// BalanceSnapshotCleanupReason values mirror
// AccountBalanceSnapshotCleanupReason (account-balance-snapshot-cleanup.service.ts:16).
const (
	BalanceSnapshotCleanupReasonConfigurationChanged = "balance_configuration_changed"
	BalanceSnapshotCleanupReasonMultipleAPIKeys      = "multiple_api_keys"
	BalanceSnapshotCleanupReasonBatchMultipleAPIKeys = "batch_multiple_api_keys"
	BalanceSnapshotCleanupReasonBatchIdentityChanged = "batch_balance_identity_changed"
)

// BalanceSnapshotCleanupRequest mirrors AccountBalanceSnapshotCleanupRequest
// (account-balance-snapshot-cleanup.service.ts:18-23); BatchID stays empty on
// the single-account PATCH path.
type BalanceSnapshotCleanupRequest struct {
	AccountID      string
	ConfigRevision int64
	Reason         string
	BatchID        string
}

// BalanceSnapshotCleaner is the nil-safe post-commit cleanup port: drop the
// superseded balance snapshot (older than the save instant) for the account.
// Implementations must be best-effort and non-blocking from the caller's
// perspective (Node enqueues into a bounded retry queue).
type BalanceSnapshotCleaner interface {
	CleanupBalanceSnapshotAfterSave(request BalanceSnapshotCleanupRequest)
}

// SetBalanceSnapshotCleaner wires the cleanup port (compose handover; nil
// keeps the patch path snapshot-silent).
func (s *Store) SetBalanceSnapshotCleaner(cleaner BalanceSnapshotCleaner) {
	s.balanceSnapshotCleaner = cleaner
}

// StoreBalanceSnapshotCleaner is the store-backed BalanceSnapshotCleaner: the
// composition-root default that executes the archived deletion against the
// account store's own database surface (PostgreSQL schema-qualified
// juhe_stats.account_usage_snapshots via statsTable, bare table on the shared
// SQLite file — the same dual-mode face the M11 snapshot read uses).
//
// Node surrounds the deletion with a bounded retry queue
// (sequenceRetryPolicy [250,1000,1000]ms × 3) plus the read-side isSuppressed
// suppression map; the Go gateway keeps the single best-effort attempt with an
// observable warn on failure (retry/suppression residual logged at the
// registered migration-gap entry).
type StoreBalanceSnapshotCleaner struct {
	store *Store
	// now seeds updatedBefore (Node item.updatedBefore = now() at enqueue
	// time); overridable for deterministic tests.
	now func() time.Time
}

// NewStoreBalanceSnapshotCleaner builds the store-backed cleaner.
func NewStoreBalanceSnapshotCleaner(store *Store) *StoreBalanceSnapshotCleaner {
	return &StoreBalanceSnapshotCleaner{store: store, now: time.Now}
}

// SetClockForTest overrides the updatedBefore clock (tests only).
func (c *StoreBalanceSnapshotCleaner) SetClockForTest(now func() time.Time) {
	c.now = now
}

// CleanupBalanceSnapshotAfterSave implements BalanceSnapshotCleaner: the
// deletion runs fire-and-forget on its own context so the PATCH response path
// stays non-blocking (the Node enqueue contract); failures log a warn and are
// otherwise dropped.
func (c *StoreBalanceSnapshotCleaner) CleanupBalanceSnapshotAfterSave(request BalanceSnapshotCleanupRequest) {
	if c == nil || c.store == nil {
		return
	}
	go func() {
		if err := c.deleteSupersededSnapshot(context.Background(), request); err != nil {
			slog.Warn("AI 账户保存已提交，余额旧快照清理失败",
				"event", "account_balance_snapshot_cleanup_failed",
				"accountId", request.AccountID,
				"configRevision", request.ConfigRevision,
				"cleanupReason", request.Reason,
				"error", err.Error())
		}
	}()
}

// deleteSupersededSnapshot mirrors deleteAccountBalanceSnapshotAsync
// (account-balance.repository.ts:887-905): drop the superseded relay_balance
// snapshot rows last refreshed at or before the save instant.
func (c *StoreBalanceSnapshotCleaner) deleteSupersededSnapshot(ctx context.Context, request BalanceSnapshotCleanupRequest) error {
	updatedBefore := isoMillis(c.now())
	_, err := c.store.db.ExecContext(ctx, c.store.bind(`DELETE FROM `+c.store.statsTable("account_usage_snapshots")+`
		WHERE account_id = ?
			AND kind = 'relay_balance'
			AND updated_at <= ?`), request.AccountID, updatedBefore)
	return err
}
