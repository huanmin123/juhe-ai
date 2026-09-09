package main

// ENGAGED 锁运行链 SQL 桥接（BUG-0174 B-2）：gatewaydispatch.AccountLocks 的
// 组合根适配器，移植归档 storage/account-lock.repository.ts 的**运行态**面：
//
//	findAccountLockStateAsync        :93-123（含 DEAD_CONFIRMED 账户恢复自愈）
//	recordAccountLockFailureAsync    :280-317（LOCKED_IDLE → ENGAGED 的 CAS 进入）
//	settleAccountLockDeadlineAsync   :337-387（到期结算 → DEAD_CONFIRMED +
//	                                  账户 temporary_unavailable + cooldown_until
//	                                  + notifyGatewayRuntimeCacheInvalidation(
//	                                  'account_lock_deadline')）
//	acquireAccountLockRetryLeaseAsync       :393-420（保留到期时间的预约租约）
//	consumeAccountLockRetryLeaseAsync       :422-444（到期消费 → 5min 派发租约）
//	releaseAccountLockRetryLeaseAsync       :446-468（显式释放/调度下次重试）
//	abandonAccountLockRetryReservationAsync :471-485（交接/中止放弃预约）
//	accountLockBlocksCrossAccount    :389-391、sampleLockDelayMs :487-494、
//	accountLockLeaseFence :523-531、sameAccountLockObservation :516-521
//
// 管理面（SetLock/LockConfig + CAS 配置修订）已在 internal/accounts/lock.go，
// 本文件不复制；运行态四操作此前在 Go 侧无实现，端口恒由
// chain_ports.go disabledAccountLocks 显式降级（账户视为未锁）。行为红线：
// 未启用锁配置的账户（无 account_lock_states 行 / enabled=0 / 非 ENGAGED）
// 四操作与「未锁」等价——FindState 不阻断、recordFailure/settle 空操作、
// acquire 直接放行（Node：无锁配置的账户不进入 ENGAGED）。
//
// SQL 直连业务库与 chainAccountsSelector 同构（PostgreSQL 走 juhe_business
// 限定名 + $n 占位符，SQLite 用裸表名 + ?）；internal/accounts 不暴露运行态
// 存储口（波1/波2 并行约束不改该包），故桥接落在 cmd 组合根。

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// 租约时长常量（归档 :54-55）。
const (
	chainAccountLockDispatchLeaseDurationMs    = 300_000 // 5min：已派发尝试的租约窗口
	chainAccountLockReservationLeaseDurationMs = 60_000  // 1min：重试预约的租约窗口
)

// chainAccountLockRow mirrors the account_lock_states row shape
// (account-lock.repository.ts AccountLockRow + stateFromRow :496-514).
type chainAccountLockRow struct {
	accountID      string
	enabled        int
	lockState      string
	deathTimeout   int
	retryInterval  int
	incidentID     sql.NullString
	incidentStart  sql.NullString
	deadlineAt     sql.NullString
	originalStatus sql.NullString
	provenance     sql.NullString
	nextRetryAtMs  sql.NullInt64
	leaseID        sql.NullString
	leaseUntilMs   sql.NullInt64
	generation     int64
	updatedAt      string
}

const chainAccountLockRowColumns = `account_id, enabled, lock_state,
		lock_death_timeout_seconds, lock_retry_interval_seconds, incident_id,
		incident_started_at, deadline_at, original_status, provenance,
		next_retry_at_ms, lease_id, lease_until_ms, generation, updated_at`

func scanChainAccountLockRow(scan func(dest ...any) error) (*chainAccountLockRow, error) {
	var row chainAccountLockRow
	err := scan(&row.accountID, &row.enabled, &row.lockState, &row.deathTimeout,
		&row.retryInterval, &row.incidentID, &row.incidentStart, &row.deadlineAt,
		&row.originalStatus, &row.provenance, &row.nextRetryAtMs, &row.leaseID,
		&row.leaseUntilMs, &row.generation, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// chainAccountLocks implements gatewaydispatch.AccountLocks over the business
// database (the runtime half the accounts package does not expose).
type chainAccountLocks struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
	// invalidateRuntimeCache mirrors notifyGatewayRuntimeCacheInvalidation:
	// fired only when the settle actually flipped the account row to
	// temporary_unavailable (Node account_lock_deadline reason). nil-safe.
	invalidateRuntimeCache func(reason string)
	// newToken renders the random suffix of incident ids and the lease ids
	// (Node randomUUID); swappable in tests.
	newToken func() string
}

// newChainAccountLocks builds the lock runtime port over the composed business
// handle. The invalidation callback is the shared K5 bus adapter the sibling
// bridges use (compose_accounts_reset.go); a nil bus degrades to no-op there.
func newChainAccountLocks(db *sql.DB, postgres bool, invalidateRuntimeCache func(reason string)) (*chainAccountLocks, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链账户锁端口需要业务数据库")
	}
	if invalidateRuntimeCache == nil {
		invalidateRuntimeCache = func(string) {}
	}
	return &chainAccountLocks{
		db:                     db,
		postgres:               postgres,
		now:                    time.Now,
		invalidateRuntimeCache: invalidateRuntimeCache,
		newToken:               chainLockRandomToken,
	}, nil
}

func (s *chainAccountLocks) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainAccountLocks) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// state read + view projection
// ---------------------------------------------------------------------------

// findState mirrors findAccountLockStateAsync: the row is authoritative, and a
// DEAD_CONFIRMED account that has recovered (active + schedulable) CAS-heals
// back to LOCKED_IDLE with the incident bookkeeping cleared (:106-121).
func (s *chainAccountLocks) findState(ctx context.Context, accountID string) (*chainAccountLockRow, error) {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		row, err := scanChainAccountLockRow(func(dest ...any) error {
			return s.db.QueryRowContext(ctx, s.bind(`SELECT `+chainAccountLockRowColumns+`
				FROM `+s.table("account_lock_states")+` WHERE account_id = ?`), id).
				Scan(dest...)
		})
		if err != nil || row == nil {
			return row, err
		}
		if !(row.enabled == 1 && row.lockState == "DEAD_CONFIRMED") {
			return row, nil
		}
		var status string
		var schedulable int
		err = s.db.QueryRowContext(ctx, s.bind(`SELECT status, schedulable
			FROM `+s.table("accounts")+` WHERE id = ? AND deleted_at IS NULL`), id).
			Scan(&status, &schedulable)
		if errors.Is(err, sql.ErrNoRows) {
			return row, nil
		}
		if err != nil {
			return nil, err
		}
		if status != "active" || schedulable != 1 {
			return row, nil
		}
		updatedAt := isoMillisOf(s.now())
		result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
			SET lock_state = 'LOCKED_IDLE', incident_id = NULL, incident_started_at = NULL,
			    deadline_at = NULL, original_status = NULL, provenance = NULL,
			    next_retry_at_ms = NULL, lease_id = NULL, lease_until_ms = NULL, updated_at = ?
			WHERE account_id = ? AND lock_state = 'DEAD_CONFIRMED' AND generation = ?`),
			updatedAt, id, row.generation)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			recovered := *row
			recovered.lockState = "LOCKED_IDLE"
			recovered.incidentID = sql.NullString{}
			recovered.incidentStart = sql.NullString{}
			recovered.deadlineAt = sql.NullString{}
			recovered.originalStatus = sql.NullString{}
			recovered.provenance = sql.NullString{}
			recovered.nextRetryAtMs = sql.NullInt64{}
			recovered.leaseID = sql.NullString{}
			recovered.leaseUntilMs = sql.NullInt64{}
			recovered.updatedAt = updatedAt
			return &recovered, nil
		}
		// Lost the recovery race: re-read like the Node recursion.
	}
	return s.readStateOnce(ctx, id)
}

func (s *chainAccountLocks) readStateOnce(ctx context.Context, id string) (*chainAccountLockRow, error) {
	return scanChainAccountLockRow(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+chainAccountLockRowColumns+`
			FROM `+s.table("account_lock_states")+` WHERE account_id = ?`), id).
			Scan(dest...)
	})
}

// chainAccountLockDeadlineMs renders the row deadline as unix millis; a NULL
// or unparseable deadline reads as already due (Node Date.parse → NaN compares
// false against nowMs on both call sites, i.e. "not blocking / settleable").
func chainAccountLockDeadlineMs(value sql.NullString) (int64, bool) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// blocksCrossAccount mirrors accountLockBlocksCrossAccount (:389-391):
// enabled && ENGAGED && (no deadline || deadline in the future).
func (s *chainAccountLocks) blocksCrossAccount(row *chainAccountLockRow, nowMs int64) bool {
	if row == nil || row.enabled != 1 || row.lockState != "ENGAGED" {
		return false
	}
	deadlineMs, ok := chainAccountLockDeadlineMs(row.deadlineAt)
	return !ok || deadlineMs > nowMs
}

func (s *chainAccountLocks) viewOf(row *chainAccountLockRow, nowMs int64) *gatewaydispatch.AccountLockStateView {
	if row == nil {
		return nil
	}
	return &gatewaydispatch.AccountLockStateView{
		Generation:         row.generation,
		IncidentID:         row.incidentID.String,
		BlocksCrossAccount: s.blocksCrossAccount(row, nowMs),
	}
}

func (s *chainAccountLocks) FindStateAsync(ctx context.Context, accountID string) (*gatewaydispatch.AccountLockStateView, error) {
	row, err := s.findState(ctx, accountID)
	if err != nil || row == nil {
		return nil, err
	}
	return s.viewOf(row, s.now().UnixMilli()), nil
}

func (s *chainAccountLocks) ListStatesAsync(ctx context.Context, accountIDs []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	nowMs := s.now().UnixMilli()
	states := make(map[string]gatewaydispatch.AccountLockStateView, len(accountIDs))
	for _, accountID := range accountIDs {
		id := strings.TrimSpace(accountID)
		if id == "" {
			continue
		}
		row, err := s.findState(ctx, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		states[id] = *s.viewOf(row, nowMs)
	}
	return states, nil
}

// ---------------------------------------------------------------------------
// observation fences (sameAccountLockObservation :516-521 + leaseFence :523-531)
// ---------------------------------------------------------------------------

// chainAccountLockObservationMatches mirrors sameAccountLockObservation's
// generation/incident check. The leaseId property-presence maps to the Go
// *string: nil = property absent, non-nil = fence applies (empty = the Node
// null "observed no lease" fence).
func chainAccountLockObservationMatches(row *chainAccountLockRow, observation *gatewaydispatch.AccountLockObservation) bool {
	if observation == nil {
		return true
	}
	if row.generation != observation.Generation {
		return false
	}
	if row.incidentID.String != observation.IncidentID {
		return false
	}
	return true
}

// chainAccountLockObservationMatchesRow is the in-transaction re-check of the
// settle path (:351): generation + incident + the conditional lease fence.
func chainAccountLockObservationMatchesRow(lockState string, deadlineAt sql.NullString, generation int64, incidentID sql.NullString, leaseID sql.NullString, observation *gatewaydispatch.AccountLockObservation, nowMs int64) bool {
	if lockState != "ENGAGED" {
		return false
	}
	deadlineMs, ok := chainAccountLockDeadlineMs(deadlineAt)
	if !ok || deadlineMs > nowMs {
		return false
	}
	if observation == nil {
		return true
	}
	if generation != observation.Generation {
		return false
	}
	if incidentID.String != observation.IncidentID {
		return false
	}
	if observation.LeaseID != nil {
		if *observation.LeaseID == "" {
			if leaseID.Valid && leaseID.String != "" {
				return false
			}
		} else if leaseID.String != *observation.LeaseID {
			return false
		}
	}
	return true
}

// chainAccountLockLeaseFence renders the CAS suffix (:523-531): the lease
// fence only applies when the observation carried a lease id.
func chainAccountLockLeaseFence(observation *gatewaydispatch.AccountLockObservation, validAtMs int64) (string, []any) {
	if observation == nil || observation.LeaseID == nil {
		return "", nil
	}
	if strings.TrimSpace(*observation.LeaseID) == "" {
		return " AND lease_id IS NULL", nil
	}
	return " AND lease_id = ? AND lease_until_ms > ?", []any{*observation.LeaseID, validAtMs}
}

// ---------------------------------------------------------------------------
// success -> LOCKED_IDLE (completeAccountLockSuccessAsync :319-335)
// ---------------------------------------------------------------------------

// CompleteSuccessAsync mirrors completeAccountLockSuccessAsync
// (account-lock.repository.ts:319-335): 上游响应协议成功后，把 ENGAGED 账户锁
// CAS 复位为 LOCKED_IDLE 并清除事故簿记（incident/deadline/lease 全部置空，
// generation 保持不变）。行为红线与 Node 一致：未启用（无行/enabled=0）或
// 非 ENGAGED 状态为空操作；observation 给出时先做 sameAccountLockObservation
// 围栏，不匹配即空操作；CAS 失竞时重读权威行、不报错（Node :334）。
func (s *chainAccountLocks) CompleteSuccessAsync(ctx context.Context, accountID, leaseID string, observation *gatewaydispatch.AccountLockObservation) error {
	_ = leaseID // Node 语义由 observation.leaseId 围栏承载，独立 leaseID 参数仅作签名占位。
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil
	}
	current, err := s.findState(ctx, id)
	if err != nil {
		return err
	}
	if current == nil || current.enabled != 1 || current.lockState != "ENGAGED" {
		return nil
	}
	if observation != nil && !chainAccountLockObservationMatches(current, observation) {
		return nil
	}
	completionNowMs := s.now().UnixMilli()
	fenceSQL, fenceArgs := chainAccountLockLeaseFence(observation, completionNowMs)
	var incidentID any
	if current.incidentID.Valid && current.incidentID.String != "" {
		incidentID = current.incidentID.String
	}
	args := []any{isoMillisOf(s.now()), id, current.generation, incidentID}
	args = append(args, fenceArgs...)
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET lock_state = 'LOCKED_IDLE', incident_id = NULL, incident_started_at = NULL, deadline_at = NULL,
		    original_status = NULL, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
		    lease_until_ms = NULL, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ? AND incident_id = ?`+fenceSQL), args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		// CAS 失竞：并发路径已推进锁态，Node 重读权威行（:334）。
		_, _ = s.readStateOnce(ctx, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// failure -> ENGAGED (recordAccountLockFailureAsync :280-317)
// ---------------------------------------------------------------------------

func (s *chainAccountLocks) RecordFailureAsync(ctx context.Context, accountID, reason string, observation *gatewaydispatch.AccountLockObservation) error {
	_ = reason // Node passes the reason through but never persists it.
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil
	}
	current, err := s.findState(ctx, id)
	if err != nil {
		return err
	}
	// 未启用 / 已确认死亡：与「未锁」等价的空操作（Node :282）。
	if current == nil || current.enabled != 1 || current.lockState == "DEAD_CONFIRMED" {
		return nil
	}
	if !chainAccountLockObservationMatches(current, observation) {
		return nil
	}
	// 已在事故中：不重开事故（Node :284）。
	if current.lockState == "ENGAGED" {
		return nil
	}
	nowMs := s.now().UnixMilli()
	updatedAt := isoMillisOf(s.now())
	var accountStatus sql.NullString
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL`), id).Scan(&accountStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	generation := current.generation + 1
	incidentID := fmt.Sprintf("%s:%d:%s", id, generation, s.newToken())
	deadline := isoMillisOf(time.UnixMilli(nowMs).Add(time.Duration(current.deathTimeout) * time.Second))
	fenceSQL, fenceArgs := chainAccountLockLeaseFence(observation, nowMs)
	args := []any{"ENGAGED", incidentID, generation, isoMillisOf(time.UnixMilli(nowMs)), deadline, nullStringOrNull(accountStatus), updatedAt, id, current.generation}
	args = append(args, fenceArgs...)
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET lock_state = ?, incident_id = ?, generation = ?, incident_started_at = ?, deadline_at = ?,
		    original_status = ?, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
		    lease_until_ms = NULL, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'LOCKED_IDLE' AND generation = ?`+fenceSQL), args...)
	if err != nil {
		return err
	}
	// Losing the CAS means a concurrent attempt advanced the incident first;
	// Node re-reads and returns the authoritative row without an error
	// (:316), so the port stays error-free here as well.
	if affected, _ := result.RowsAffected(); affected != 1 {
		_, _ = s.readStateOnce(ctx, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// deadline settlement (settleAccountLockDeadlineAsync :337-387)
// ---------------------------------------------------------------------------

func (s *chainAccountLocks) SettleDeadlineAsync(ctx context.Context, accountID string, nowMs int64, observation *gatewaydispatch.AccountLockObservation) error {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil
	}
	current, err := s.findState(ctx, id)
	if err != nil {
		return err
	}
	if current == nil || current.enabled != 1 || current.lockState != "ENGAGED" {
		return nil
	}
	if deadlineMs, ok := chainAccountLockDeadlineMs(current.deadlineAt); !ok || deadlineMs > nowMs {
		return nil
	}
	if !chainAccountLockObservationMatches(current, observation) {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var lockState string
	var generation int64
	var deadlineAt, incidentID, originalStatus, leaseID sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT lock_state, generation, deadline_at, incident_id,
			original_status, lease_id FROM `+s.table("account_lock_states")+` WHERE account_id = ?`), id).
		Scan(&lockState, &generation, &deadlineAt, &incidentID, &originalStatus, &leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !chainAccountLockObservationMatchesRow(lockState, deadlineAt, generation, incidentID, leaseID, observation, nowMs) {
		return nil
	}
	// originalStatus falls back to the current account status (:355).
	effectiveOriginalStatus := originalStatus
	if !effectiveOriginalStatus.Valid || strings.TrimSpace(effectiveOriginalStatus.String) == "" {
		var accountStatus sql.NullString
		err = tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("accounts")+`
			WHERE id = ? AND deleted_at IS NULL`), id).Scan(&accountStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		effectiveOriginalStatus = accountStatus
	}
	fenceSQL, fenceArgs := chainAccountLockLeaseFence(observation, nowMs)
	args := []any{nullStringOrNull(effectiveOriginalStatus), isoMillisOf(s.now()), id, generation, incidentID}
	args = append(args, fenceArgs...)
	transition, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET lock_state = 'DEAD_CONFIRMED', original_status = ?, provenance = 'lock_policy',
		    lease_id = NULL, lease_until_ms = NULL, next_retry_at_ms = NULL, updated_at = ?
		WHERE account_id = ? AND lock_state = 'ENGAGED' AND generation = ? AND incident_id = ?`+fenceSQL), args...)
	if err != nil {
		return err
	}
	if affected, _ := transition.RowsAffected(); affected != 1 {
		return nil
	}
	accountAvailabilityChanged := false
	if effectiveOriginalStatus.Valid && effectiveOriginalStatus.String == "active" {
		now := isoMillisOf(s.now())
		accountTransition, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET status = 'temporary_unavailable', cooldown_until = ?, updated_at = ?
			WHERE id = ? AND status = 'active' AND schedulable = 1`), now, now, id)
		if err != nil {
			return err
		}
		affected, _ := accountTransition.RowsAffected()
		accountAvailabilityChanged = affected == 1
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if accountAvailabilityChanged {
		s.invalidateRuntimeCache("account_lock_deadline")
	}
	return nil
}

// ---------------------------------------------------------------------------
// retry lease family (:393-485)
// ---------------------------------------------------------------------------

// chainAccountLockRetryReservationDueAtMs mirrors
// accountLockRetryReservationDueAtMs (:62-65): a due time already in the past
// is claimed now, a future due time is preserved.
func chainAccountLockRetryReservationDueAtMs(nextRetryAtMs sql.NullInt64, nowMs int64) (int64, bool) {
	if !nextRetryAtMs.Valid {
		return 0, false
	}
	return maxInt64(nextRetryAtMs.Int64, nowMs), true
}

// sampleLockDelayMs mirrors sampleLockDelayMs (:487-494): interval base with a
// seeded ±jitter, clamped into 2000..30000. The retry interval normalizes with
// the management-plane rules (invalid rows fall back to the 5s default, same
// as internal/accounts).
func sampleLockDelayMs(intervalSeconds int, seed string) int64 {
	normalized, _ := accounts.NormalizeLockRetryIntervalSeconds(intervalSeconds)
	base := int64(normalized) * 1000
	jitter := int64(2000)
	if base > 10_000 {
		jitter = 5000
	}
	// Node folds the seed into a 32-bit signed hash ((hash * 31 +
	// charCodeAt) | 0 — the |0 truncates to int32). Go's int32 arithmetic
	// wraps two's-complement, matching the JS coercion.
	var hash int32
	for _, ch := range seed {
		hash = hash*31 + int32(ch)
	}
	offset := int64(absInt32(hash)%(int32(jitter)*2+1) - int32(jitter))
	delay := base + offset
	if delay < 2_000 {
		delay = 2_000
	}
	if delay > 30_000 {
		delay = 30_000
	}
	return delay
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func absInt32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

func (s *chainAccountLocks) AcquireRetryLeaseAsync(ctx context.Context, accountID string, configuredDelayMs int64) (gatewaydispatch.LockLeaseAcquire, error) {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return gatewaydispatch.LockLeaseAcquire{Allowed: true}, nil
	}
	current, err := s.findState(ctx, id)
	if err != nil {
		return gatewaydispatch.LockLeaseAcquire{}, err
	}
	nowMs := s.now().UnixMilli()
	if !s.blocksCrossAccount(current, nowMs) {
		// 未锁 / 非阻断态：直接放行，无租约（Node :395）。
		return gatewaydispatch.LockLeaseAcquire{Allowed: true, WaitMs: 0}, nil
	}
	if current.leaseID.Valid && current.leaseID.String != "" &&
		current.leaseUntilMs.Valid && current.leaseUntilMs.Int64 > nowMs {
		waitUntil := current.leaseUntilMs.Int64
		if current.nextRetryAtMs.Valid && current.nextRetryAtMs.Int64 > nowMs {
			waitUntil = current.nextRetryAtMs.Int64
		}
		return gatewaydispatch.LockLeaseAcquire{Allowed: false, WaitMs: waitUntil - nowMs}, nil
	}
	desiredAt, ok := chainAccountLockRetryReservationDueAtMs(current.nextRetryAtMs, nowMs)
	if !ok {
		desiredAt = nowMs + maxInt64(maxInt64(0, configuredDelayMs), sampleLockDelayMs(current.retryInterval, s.newToken()))
	}
	if current.nextRetryAtMs.Valid && current.nextRetryAtMs.Int64 > nowMs {
		return gatewaydispatch.LockLeaseAcquire{Allowed: false, WaitMs: current.nextRetryAtMs.Int64 - nowMs}, nil
	}
	leaseID := s.newToken()
	updatedAt := isoMillisOf(s.now())
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET next_retry_at_ms = ?, lease_id = ?, lease_until_ms = ?, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
		  AND incident_id = ? AND (lease_until_ms IS NULL OR lease_until_ms <= ?)
		  AND (next_retry_at_ms IS NULL OR next_retry_at_ms <= ?)`),
		desiredAt, leaseID, desiredAt+chainAccountLockReservationLeaseDurationMs, updatedAt,
		id, current.generation, nullStringText(current.incidentID), nowMs, nowMs)
	if err != nil {
		return gatewaydispatch.LockLeaseAcquire{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return gatewaydispatch.LockLeaseAcquire{Allowed: true, WaitMs: maxInt64(0, desiredAt-nowMs), LeaseID: leaseID}, nil
	}
	return gatewaydispatch.LockLeaseAcquire{Allowed: false, WaitMs: maxInt64(1, desiredAt-nowMs)}, nil
}

func (s *chainAccountLocks) ConsumeRetryLeaseAsync(ctx context.Context, accountID, leaseID string) (bool, error) {
	if strings.TrimSpace(leaseID) == "" {
		return false, nil
	}
	id := strings.TrimSpace(accountID)
	current, err := s.findState(ctx, id)
	if err != nil {
		return false, err
	}
	nowMs := s.now().UnixMilli()
	if !s.blocksCrossAccount(current, nowMs) {
		return false, nil
	}
	if current.leaseID.String != leaseID ||
		!current.nextRetryAtMs.Valid || current.nextRetryAtMs.Int64 > nowMs ||
		!current.leaseUntilMs.Valid || current.leaseUntilMs.Int64 <= nowMs {
		return false, nil
	}
	updatedAt := isoMillisOf(s.now())
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET next_retry_at_ms = ?, lease_until_ms = ?, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
		  AND incident_id = ? AND lease_id = ? AND next_retry_at_ms <= ? AND lease_until_ms > ?`),
		nowMs, nowMs+chainAccountLockDispatchLeaseDurationMs, updatedAt,
		id, current.generation, nullStringText(current.incidentID), leaseID, nowMs, nowMs)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (s *chainAccountLocks) ReleaseRetryLeaseAsync(ctx context.Context, input gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	if strings.TrimSpace(input.LeaseID) == "" {
		return true, nil
	}
	id := strings.TrimSpace(input.AccountID)
	completedAtMs := s.now().UnixMilli()
	current, err := s.findState(ctx, id)
	if err != nil {
		return false, err
	}
	if !s.blocksCrossAccount(current, completedAtMs) || current.leaseID.String != input.LeaseID {
		return false, nil
	}
	var nextRetryAtMs any
	if input.ScheduleNextRetry {
		nextRetryAtMs = completedAtMs + maxInt64(maxInt64(0, input.GlobalDelayMs), sampleLockDelayMs(current.retryInterval, s.newToken()))
	}
	updatedAt := isoMillisOf(s.now())
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET next_retry_at_ms = ?, lease_id = NULL, lease_until_ms = NULL, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
		  AND incident_id = ? AND lease_id = ? AND lease_until_ms > ?`),
		nextRetryAtMs, updatedAt, id, current.generation, nullStringText(current.incidentID), input.LeaseID, completedAtMs)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (s *chainAccountLocks) AbandonRetryReservationAsync(ctx context.Context, lease gatewaydispatch.AccountLockRetryLease) error {
	if strings.TrimSpace(lease.LeaseID) == "" {
		return nil
	}
	id := strings.TrimSpace(lease.AccountID)
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_lock_states")+`
		SET lease_id = NULL, lease_until_ms = NULL, updated_at = ?
		WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED'
		  AND lease_id = ? AND next_retry_at_ms IS NOT NULL
		  AND lease_until_ms = next_retry_at_ms + ?`),
		isoMillisOf(s.now()), id, lease.LeaseID, int64(chainAccountLockReservationLeaseDurationMs))
	if err != nil {
		return err
	}
	// The port drops the boolean (the dispatch abort paths are best-effort);
	// a lost race leaves the newer lease untouched like the archive.
	_, _ = result.RowsAffected()
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// isoMillisOf matches internal/accounts isoMillis (Node toISOString
// millisecond precision).
func isoMillisOf(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

func nullStringText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullStringOrNull(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

// chainLockRandomToken renders the random suffix of incident ids / lease ids
// (Node randomUUID): 16 crypto-random bytes hex-encoded.
func chainLockRandomToken() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buf[:])
}

// chainAccountLocksDisabledViaEnv reads the explicit off-switch
// (JUHE_AI_ACCOUNT_LOCKS_DISABLED)：仅字面量 "true"（不区分大小写）关闭运行
// 锁端口；其余取值保持真实实现。env 读取经注入的 getter，测试无需改动进程
// 环境即可验证开关。
func chainAccountLocksDisabledViaEnv(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_LOCKS_DISABLED")), "true")
}

// compile-time: the bridge satisfies the dispatch lock port.
var _ gatewaydispatch.AccountLocks = (*chainAccountLocks)(nil)
