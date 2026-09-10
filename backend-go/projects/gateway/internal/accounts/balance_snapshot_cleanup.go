package accounts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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

// cleanupRetryDelays mirrors
// sequenceRetryPolicy('account_balance_snapshot_cleanup', [250,1000,1000], 3):
// the first retry waits 250ms, the next two wait 1000ms each (three retries
// on top of the initial attempt).
var cleanupRetryDelays = []int64{250, 1000, 1000}

// cleanupQueueConcurrency bounds the fire-and-forget deletion workers. 归档为
// pLimit(globalMax) + 全局后台并发槽双重限流；Go 无并发治理器，队列并发本身
// 即限流面（本删除为单条 DELETE，4 已远超实际需要）。
const cleanupQueueConcurrency = 4

// StoreBalanceSnapshotCleaner is the store-backed BalanceSnapshotCleaner: the
// composition-root default that executes the archived deletion against the
// account store's own database surface (PostgreSQL schema-qualified
// juhe_stats.account_usage_snapshots via statsTable, bare table on the shared
// SQLite file — the same dual-mode face the M11 snapshot read uses).
//
// Node surrounds the deletion with the bounded retry queue (see
// cleanupRetryDelays) plus the read-side isSuppressed suppression map; the Go
// read side keeps the registered M11 fallback
// (balanceSnapshotMatchesConfiguration), while this coordinator ports the
// retry face: temporary failures re-enqueue with the archived delays and the
// Node log-event vocabulary (initial_failed / retry_failed /
// retry_scheduled / retry_succeeded / retry_exhausted) verbatim.
type StoreBalanceSnapshotCleaner struct {
	store *Store
	// now seeds updatedBefore (Node item.updatedBefore = now() at enqueue
	// time); overridable for deterministic tests.
	now func() time.Time

	mu                sync.Mutex
	queue             *retryQueue[cleanupQueueItem]
	suppressedItems   map[string]cleanupQueueItem
	exhaustedAccounts map[string]bool
	sequence          int64
	// lifetimeCtx drives the deletion statements; Close cancels it so an
	// in-flight DELETE stops touching the pool while the process shuts down.
	lifetimeCtx context.Context
	cancel      context.CancelFunc
}

// cleanupQueueItem mirrors AccountBalanceSnapshotCleanupQueueItem.
type cleanupQueueItem struct {
	request       BalanceSnapshotCleanupRequest
	requestID     string
	updatedBefore string
}

// NewStoreBalanceSnapshotCleaner builds the store-backed cleaner.
func NewStoreBalanceSnapshotCleaner(store *Store) *StoreBalanceSnapshotCleaner {
	ctx, cancel := context.WithCancel(context.Background())
	cleaner := &StoreBalanceSnapshotCleaner{
		store:             store,
		now:               time.Now,
		suppressedItems:   map[string]cleanupQueueItem{},
		exhaustedAccounts: map[string]bool{},
		lifetimeCtx:       ctx,
		cancel:            cancel,
	}
	cleaner.queue = newRetryQueue[cleanupQueueItem]("account-balance-snapshot-cleanup",
		cleanupRetryDelays, cleanupQueueConcurrency,
		func(item cleanupQueueItem, attemptIndex int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return cleaner.deleteSupersededSnapshot(ctx, item.request)
		},
		retryQueueCallbacks[cleanupQueueItem]{
			OnSuccess:        cleaner.onCleanupSuccess,
			OnFailure:        cleaner.onCleanupFailure,
			OnRetryScheduled: cleaner.onCleanupRetryScheduled,
			OnExhausted:      cleaner.onCleanupExhausted,
		})
	return cleaner
}

// Close stops the cleanup lifecycle: new enqueues are refused, pending items
// and timers are dropped, the lifetime context cancels any in-flight DELETE,
// and the call blocks until running invocations returned (Node
// stopAndDrain's shutdown contract). Register on the composition shutdown
// chain ahead of the SQL handle close so the queue never touches a closed
// pool.
func (c *StoreBalanceSnapshotCleaner) Close() {
	c.mu.Lock()
	queue := c.queue
	c.mu.Unlock()
	if queue == nil {
		return
	}
	queue.stop()
	c.cancel()
	queue.waitRunning()
}

// SetClockForTest overrides the updatedBefore clock (tests only).
func (c *StoreBalanceSnapshotCleaner) SetClockForTest(now func() time.Time) {
	c.now = now
	c.mu.Lock()
	c.queue = newRetryQueue[cleanupQueueItem]("account-balance-snapshot-cleanup",
		cleanupRetryDelays, cleanupQueueConcurrency,
		func(item cleanupQueueItem, attemptIndex int) error {
			return c.deleteSupersededSnapshot(context.Background(), item.request)
		},
		retryQueueCallbacks[cleanupQueueItem]{
			OnSuccess:        c.onCleanupSuccess,
			OnFailure:        c.onCleanupFailure,
			OnRetryScheduled: c.onCleanupRetryScheduled,
			OnExhausted:      c.onCleanupExhausted,
		})
	c.queue.setClockForTest(now)
	c.mu.Unlock()
}

// Queue exposes the retry queue for deterministic tests (tests only).
func (c *StoreBalanceSnapshotCleaner) Queue() *retryQueue[cleanupQueueItem] {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.queue
}

// CleanupBalanceSnapshotAfterSave implements BalanceSnapshotCleaner: enqueue
// on the bounded retry queue (Node cleanupAfterSave contract) — non-blocking
// for the PATCH response path, replaceExisting keeps the latest request per
// account.
func (c *StoreBalanceSnapshotCleaner) CleanupBalanceSnapshotAfterSave(request BalanceSnapshotCleanupRequest) {
	if c == nil || c.store == nil {
		return
	}
	now := c.now()
	c.mu.Lock()
	c.sequence++
	item := cleanupQueueItem{
		request: request,
		requestID: fmt.Sprintf("%s:%d:%d:%d", request.AccountID, request.ConfigRevision,
			now.UnixMilli(), c.sequence),
		updatedBefore: isoMillis(now),
	}
	c.suppressedItems[request.AccountID] = item
	delete(c.exhaustedAccounts, request.AccountID)
	queue := c.queue
	c.mu.Unlock()
	queue.enqueue(request.AccountID, item, retryQueueEnqueueOptions{ReplaceExisting: true})
}

// currentItemLocked resolves whether the event item is still the account's
// latest request (Node requestId identity check); caller holds c.mu.
func (c *StoreBalanceSnapshotCleaner) currentItemLocked(event retryQueueEvent[cleanupQueueItem]) (cleanupQueueItem, bool) {
	item, ok := c.suppressedItems[event.Key]
	return item, ok && item.requestID == event.Item.requestID
}

// onCleanupSuccess mirrors onSuccess: only the latest request clears its own
// suppression (Node requestId identity check).
func (c *StoreBalanceSnapshotCleaner) onCleanupSuccess(event retryQueueEvent[cleanupQueueItem]) {
	c.mu.Lock()
	item, mine := c.currentItemLocked(event)
	if mine {
		delete(c.suppressedItems, event.Key)
		delete(c.exhaustedAccounts, event.Key)
	}
	c.mu.Unlock()
	if mine {
		args := append([]any{"event", "account_balance_snapshot_cleanup_retry_succeeded",
			"attemptCount", event.AttemptIndex + 1}, cleanupLogFields(item)...)
		slog.Info("AI 账户余额旧快照重试清理成功", args...)
	}
}

// onCleanupFailure mirrors onFailure: the initial attempt and the retries use
// distinct event names (Node attemptIndex fork).
func (c *StoreBalanceSnapshotCleaner) onCleanupFailure(event retryQueueEvent[cleanupQueueItem]) {
	c.mu.Lock()
	item, _ := c.currentItemLocked(event)
	c.mu.Unlock()
	eventName := "account_balance_snapshot_cleanup_retry_failed"
	message := "AI 账户余额旧快照重试清理失败"
	if event.AttemptIndex == 0 {
		eventName = "account_balance_snapshot_cleanup_initial_failed"
		message = "AI 账户保存已提交，余额旧快照首次清理失败并已安排有限重试"
	}
	args := append([]any{"event", eventName,
		"attemptCount", event.AttemptIndex + 1}, cleanupLogFields(item)...)
	slog.Warn(message, append(args, "error", event.Err.Error())...)
}

// onCleanupRetryScheduled mirrors onRetryScheduled.
func (c *StoreBalanceSnapshotCleaner) onCleanupRetryScheduled(event retryQueueEvent[cleanupQueueItem]) {
	c.mu.Lock()
	item, _ := c.currentItemLocked(event)
	c.mu.Unlock()
	args := append([]any{"event", "account_balance_snapshot_cleanup_retry_scheduled",
		"attemptCount", event.AttemptIndex + 1,
		"delayMs", event.DelayMs}, cleanupLogFields(item)...)
	slog.Warn("AI 账户余额旧快照已安排有限重试", append(args, "error", event.Err.Error())...)
}

// onCleanupExhausted mirrors onExhausted: the exhausted account keeps its
// suppression entry so the stale snapshot stays hidden from the read
// fallback until a newer save replaces it.
func (c *StoreBalanceSnapshotCleaner) onCleanupExhausted(event retryQueueEvent[cleanupQueueItem]) {
	c.mu.Lock()
	item, mine := c.currentItemLocked(event)
	if mine {
		c.exhaustedAccounts[event.Key] = true
	}
	c.mu.Unlock()
	args := append([]any{"event", "account_balance_snapshot_cleanup_retry_exhausted",
		"attemptCount", event.AttemptIndex + 1,
		"staleSnapshotSuppressed", true}, cleanupLogFields(item)...)
	slog.Warn("AI 账户余额旧快照清理已用尽重试，继续屏蔽旧快照", append(args, "error", event.Err.Error())...)
}

func cleanupLogFields(item cleanupQueueItem) []any {
	return []any{
		"accountId", item.request.AccountID,
		"configRevision", item.request.ConfigRevision,
		"cleanupReason", item.request.Reason,
		"batchId", item.request.BatchID,
		"updatedBefore", item.updatedBefore,
	}
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
