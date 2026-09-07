package main

// ENGAGED 锁运行链桥接（chain_account_locks.go）与波1遗留接线（EngineSecret /
// KeyRotation）的组合根单测。归档对照基线：
// storage/account-lock.repository.ts :93-123 / :280-317 / :337-387 / :393-494 /
// :487-494 / :516-531；account-api-key-rotation.ts :269-299。

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

// lockClock is the controllable clock the lease-family assertions need (the
// archive samples delays and fences against Date.now()).
type lockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLockClock() *lockClock { return &lockClock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)} }

func (c *lockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lockClock) AdvanceMs(ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Duration(ms) * time.Millisecond)
}

func (c *lockClock) NowMs() int64 { return c.Now().UnixMilli() }

// lockFixture bundles the SQLite business handle with the bridge under test.
type lockFixture struct {
	db      *sql.DB
	locks   *chainAccountLocks
	clock   *lockClock
	tokens  *tokenSequence
	reasons *invalidationRecorder
}

type tokenSequence struct {
	mu   sync.Mutex
	next int
}

func (s *tokenSequence) Next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return "tok-" + strings.Repeat("s", s.next)
}

type invalidationRecorder struct {
	mu      sync.Mutex
	reasons []string
}

func (r *invalidationRecorder) Record(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
}

func (r *invalidationRecorder) All() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.reasons...)
}

func newAccountLocksFixture(t *testing.T) *lockFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "locks.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		// Real-DDL shape (maintenance sqlite_schema.go): the runtime bridge
		// relies on the same CHECK constraints the archive assumes.
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			schedulable INTEGER NOT NULL DEFAULT 1,
			cooldown_until TEXT,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		)`,
		`CREATE TABLE account_lock_states (
			account_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
			lock_state TEXT NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
			lock_death_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
			lock_retry_interval_seconds INTEGER NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
			incident_id TEXT,
			generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
			incident_started_at TEXT,
			deadline_at TEXT,
			original_status TEXT,
			provenance TEXT,
			next_retry_at_ms INTEGER,
			lease_id TEXT,
			lease_until_ms INTEGER,
			updated_at TEXT NOT NULL,
			CHECK ((lock_state = 'UNLOCKED' AND enabled = 0) OR (lock_state <> 'UNLOCKED' AND enabled = 1))
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	clock := newLockClock()
	tokens := &tokenSequence{}
	reasons := &invalidationRecorder{}
	locks, err := newChainAccountLocks(db, false, reasons.Record)
	if err != nil {
		t.Fatalf("build lock bridge: %v", err)
	}
	locks.now = clock.Now
	locks.newToken = tokens.Next
	return &lockFixture{db: db, locks: locks, clock: clock, tokens: tokens, reasons: reasons}
}

func (f *lockFixture) seedAccount(t *testing.T, id, status string, schedulable int, deletedAt string) {
	t.Helper()
	var deletedAtArg any
	if deletedAt != "" {
		deletedAtArg = deletedAt
	}
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable, updated_at, deleted_at)
		VALUES (?, 'sys_owner', 'openai', ?, 'api_key', ?, ?, ?, ?)`,
		id, nameOf(id), status, schedulable, isoMillisOf(f.clock.Now()), deletedAtArg); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func nameOf(id string) string { return "账户 " + id }

// fenceLease renders the observation lease id pointer (Node leaseId property).
func fenceLease(value string) *string { return &value }

func (f *lockFixture) seedLockRow(t *testing.T, row chainAccountLockRow) {
	t.Helper()
	updatedAt := row.updatedAt
	if updatedAt == "" {
		updatedAt = isoMillisOf(f.clock.Now())
	}
	if _, err := f.db.Exec(`INSERT INTO account_lock_states (
			account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
			incident_id, generation, incident_started_at, deadline_at, original_status, provenance,
			next_retry_at_ms, lease_id, lease_until_ms, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.accountID, row.enabled, row.lockState, row.deathTimeout, row.retryInterval,
		nullStringOrNull(row.incidentID), row.generation, nullStringOrNull(row.incidentStart),
		nullStringOrNull(row.deadlineAt), nullStringOrNull(row.originalStatus), nullStringOrNull(row.provenance),
		nullInt64OrNull(row.nextRetryAtMs), nullStringOrNull(row.leaseID), nullInt64OrNull(row.leaseUntilMs),
		updatedAt); err != nil {
		t.Fatalf("seed lock row: %v", err)
	}
}

func nullInt64OrNull(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func (f *lockFixture) readLockRow(t *testing.T, accountID string) *chainAccountLockRow {
	t.Helper()
	row, err := scanChainAccountLockRow(func(dest ...any) error {
		return f.db.QueryRow(`SELECT account_id, enabled, lock_state, lock_death_timeout_seconds,
			lock_retry_interval_seconds, incident_id, incident_started_at, deadline_at, original_status,
			provenance, next_retry_at_ms, lease_id, lease_until_ms, generation, updated_at
			FROM account_lock_states WHERE account_id = ?`, accountID).Scan(dest...)
	})
	if err != nil {
		t.Fatalf("read lock row: %v", err)
	}
	return row
}

func (f *lockFixture) readAccount(t *testing.T, accountID string) (status string, cooldownUntil sql.NullString) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT status, cooldown_until FROM accounts WHERE id = ?`, accountID).
		Scan(&status, &cooldownUntil); err != nil {
		t.Fatalf("read account: %v", err)
	}
	return status, cooldownUntil
}

// ---------------------------------------------------------------------------
// 行为红线：未启用锁配置的账户，四操作与「未锁」等价
// ---------------------------------------------------------------------------

func TestChainAccountLocksUnlockedAccountIsNoOp(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()

	// 无行账户：FindState 为 nil（dispatch 侧不产生锁观察代际）。
	view, err := fixture.locks.FindStateAsync(ctx, "acc_missing")
	if err != nil || view != nil {
		t.Fatalf("FindState on missing account = (%v, %v), want (nil, nil)", view, err)
	}
	// recordFailure / settle 空操作。
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_missing", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("RecordFailure on missing account: %v", err)
	}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_missing", fixture.clock.NowMs(), nil); err != nil {
		t.Fatalf("SettleDeadline on missing account: %v", err)
	}
	// 重试租约族：acquire 直接放行（Node :395），consume/release 不放行。
	lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_missing", 3_000)
	if err != nil || !lease.Allowed || lease.WaitMs != 0 || lease.LeaseID != "" {
		t.Fatalf("Acquire on missing account = %+v, %v, want allowed/no-wait/no-lease", lease, err)
	}
	if consumed, err := fixture.locks.ConsumeRetryLeaseAsync(ctx, "acc_missing", "any"); err != nil || consumed {
		t.Fatalf("Consume on missing account = (%v, %v), want (false, nil)", consumed, err)
	}
	if released, err := fixture.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{AccountID: "acc_missing", LeaseID: "any"}); err != nil || released {
		t.Fatalf("Release on missing account = (%v, %v), want (false, nil)", released, err)
	}
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "acc_missing", LeaseID: "any"}); err != nil {
		t.Fatalf("Abandon on missing account: %v", err)
	}
	// ListStates：缺失行按 Node listAccountLockStatesAsync 省略。
	states, err := fixture.locks.ListStatesAsync(ctx, []string{"acc_missing", "acc_other"})
	if err != nil || len(states) != 0 {
		t.Fatalf("ListStates on missing accounts = (%v, %v), want empty", states, err)
	}

	// enabled=0（UNLOCKED）：与「未锁」完全等价——FindState 不阻断、
	// recordFailure 空操作、acquire 直接放行（Node :282 只放行 !enabled 与
	// DEAD_CONFIRMED；LOCKED_IDLE 是已武装的锁，recordFailure 进入 ENGAGED，
	// 该行为在 TestChainAccountLocksRecordFailureEngages 单独覆盖）。
	fixture.seedAccount(t, "acc_unlocked", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_unlocked", enabled: 0, lockState: "UNLOCKED", deathTimeout: 300, retryInterval: 5, generation: 2})
	view, err = fixture.locks.FindStateAsync(ctx, "acc_unlocked")
	if err != nil || view == nil || view.BlocksCrossAccount {
		t.Fatalf("FindState(acc_unlocked) = (%+v, %v), want non-blocking", view, err)
	}
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_unlocked", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("RecordFailure(acc_unlocked): %v", err)
	}
	if lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_unlocked", 3_000); err != nil || !lease.Allowed || lease.WaitMs != 0 || lease.LeaseID != "" {
		t.Fatalf("Acquire(acc_unlocked) = %+v, %v, want allowed/no-wait/no-lease", lease, err)
	}
	if row := fixture.readLockRow(t, "acc_unlocked"); row.lockState != "UNLOCKED" || row.generation != 2 {
		t.Fatalf("UNLOCKED row mutated: %+v", row)
	}
	// ENGAGED 但 deadline 已过：不阻断（Node accountLockBlocksCrossAccount）。
	fixture.seedAccount(t, "acc_expired", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_expired", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 4,
		incidentID: sql.NullString{String: "acc_expired:4:x", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true}})
	view, err = fixture.locks.FindStateAsync(ctx, "acc_expired")
	if err != nil || view == nil || view.BlocksCrossAccount {
		t.Fatalf("expired ENGAGED must not block: %+v, %v", view, err)
	}
	if lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_expired", 3_000); err != nil || !lease.Allowed {
		t.Fatalf("expired ENGAGED acquire must allow: %+v, %v", lease, err)
	}
	// 到期的 ENGAGED 行结算：锁转 DEAD_CONFIRMED、账户转
	// temporary_unavailable 并触发恰好一次失效通知（归档 :374-376）。
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_expired", fixture.clock.NowMs()+1, nil); err != nil {
		t.Fatalf("SettleDeadline: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_expired"); row.lockState != "DEAD_CONFIRMED" {
		t.Fatalf("expired settle must confirm the death: %+v", row)
	}
	if status, _ := fixture.readAccount(t, "acc_expired"); status != "temporary_unavailable" {
		t.Fatalf("expired settle must flip the account, got %s", status)
	}
	if reasons := fixture.reasons.All(); len(reasons) != 1 || reasons[0] != "account_lock_deadline" {
		t.Fatalf("invalidations = %v, want exactly [account_lock_deadline]", reasons)
	}
}

// ---------------------------------------------------------------------------
// recordFailure → ENGAGED（归档 :280-317）
// ---------------------------------------------------------------------------

func TestChainAccountLocksRecordFailureEngages(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, "acc_1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_1", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 3})

	nowMs := fixture.clock.NowMs()
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_1", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	row := fixture.readLockRow(t, "acc_1")
	if row.lockState != "ENGAGED" || row.generation != 4 {
		t.Fatalf("state = %s gen = %d, want ENGAGED/4", row.lockState, row.generation)
	}
	wantIncident := "acc_1:4:tok-s"
	if !row.incidentID.Valid || row.incidentID.String != wantIncident {
		t.Fatalf("incident_id = %v, want %s", row.incidentID, wantIncident)
	}
	if !row.incidentStart.Valid || row.incidentStart.String != isoMillisOf(time.UnixMilli(nowMs)) {
		t.Fatalf("incident_started_at = %v, want %s", row.incidentStart, isoMillisOf(time.UnixMilli(nowMs)))
	}
	wantDeadline := isoMillisOf(time.UnixMilli(nowMs + 300_000))
	if !row.deadlineAt.Valid || row.deadlineAt.String != wantDeadline {
		t.Fatalf("deadline_at = %v, want %s", row.deadlineAt, wantDeadline)
	}
	if !row.originalStatus.Valid || row.originalStatus.String != "active" {
		t.Fatalf("original_status = %v, want active", row.originalStatus)
	}
	if row.leaseID.Valid || row.nextRetryAtMs.Valid || row.leaseUntilMs.Valid {
		t.Fatalf("lease fields must clear: %+v", row)
	}

	// 已 ENGAGED：不重开事故（Node :284）。
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_1", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("second RecordFailure: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_1"); row.generation != 4 || row.incidentID.String != wantIncident {
		t.Fatalf("ENGAGED recordFailure must be a no-op: %+v", row)
	}

	// 观察代际不匹配：空操作（Node :283 sameAccountLockObservation）。
	stale := &gatewaydispatch.AccountLockObservation{Generation: 2, IncidentID: "older"}
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_1", "upstream_transport_failure", stale); err != nil {
		t.Fatalf("stale RecordFailure: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_1"); row.generation != 4 {
		t.Fatalf("stale observation must not advance: gen=%d", row.generation)
	}

	// 观察代际匹配（dispatch 捕获后传入）：仍是 ENGAGED no-op。
	matched := &gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: wantIncident}
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_1", "upstream_transport_failure", matched); err != nil {
		t.Fatalf("matched RecordFailure: %v", err)
	}

	// 账户已删除：original_status 为 NULL（Node account?.status ?? null）。
	fixture.seedAccount(t, "acc_deleted", "active", 1, isoMillisOf(fixture.clock.Now()))
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_deleted", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 1})
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_deleted", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("deleted RecordFailure: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_deleted"); row.originalStatus.Valid {
		t.Fatalf("deleted account original_status = %v, want NULL", row.originalStatus)
	}
}

// ---------------------------------------------------------------------------
// settleDeadline → DEAD_CONFIRMED + 账户结算 + 失效通知（归档 :337-387）
// ---------------------------------------------------------------------------

func TestChainAccountLocksSettleDeadline(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, "acc_1", "active", 1, "")
	startedAt := fixture.clock.NowMs()
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 4,
		incidentID:     sql.NullString{String: "acc_1:4:tok", Valid: true},
		incidentStart:  sql.NullString{String: isoMillisOf(time.UnixMilli(startedAt)), Valid: true},
		deadlineAt:     sql.NullString{String: isoMillisOf(time.UnixMilli(startedAt + 300_000)), Valid: true},
		originalStatus: sql.NullString{String: "active", Valid: true}})

	// 未到期：空操作、无失效通知。
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_1", startedAt+299_999, nil); err != nil {
		t.Fatalf("early settle: %v", err)
	}
	if reasons := fixture.reasons.All(); len(reasons) != 0 {
		t.Fatalf("early settle must not invalidate: %v", reasons)
	}

	// 到期结算：DEAD_CONFIRMED + temporary_unavailable + cooldown + 一次通知。
	settleMs := startedAt + 300_000
	fixture.clock.AdvanceMs(300_000)
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_1", settleMs, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	row := fixture.readLockRow(t, "acc_1")
	if row.lockState != "DEAD_CONFIRMED" || row.provenance.String != "lock_policy" {
		t.Fatalf("state = %s provenance = %s, want DEAD_CONFIRMED/lock_policy", row.lockState, row.provenance.String)
	}
	status, cooldownUntil := fixture.readAccount(t, "acc_1")
	if status != "temporary_unavailable" {
		t.Fatalf("account status = %s, want temporary_unavailable", status)
	}
	if !cooldownUntil.Valid || cooldownUntil.String != isoMillisOf(time.UnixMilli(settleMs)) {
		t.Fatalf("cooldown_until = %v, want %s", cooldownUntil, isoMillisOf(time.UnixMilli(settleMs)))
	}
	if reasons := fixture.reasons.All(); len(reasons) != 1 || reasons[0] != "account_lock_deadline" {
		t.Fatalf("invalidations = %v, want [account_lock_deadline]", reasons)
	}

	// 已 DEAD_CONFIRMED：再次 settle 空操作、无第二笔通知。
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_1", settleMs+1, nil); err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if reasons := fixture.reasons.All(); len(reasons) != 1 {
		t.Fatalf("second settle must not invalidate: %v", reasons)
	}
}

func TestChainAccountLocksSettleDeadlineFallbacks(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()

	// original_status 为 NULL：回退读账户当前状态（Node :355）。
	fixture.seedAccount(t, "acc_fb", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_fb", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID: sql.NullString{String: "acc_fb:1:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true}})
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_fb", fixture.clock.NowMs(), nil); err != nil {
		t.Fatalf("fallback settle: %v", err)
	}
	if status, _ := fixture.readAccount(t, "acc_fb"); status != "temporary_unavailable" {
		t.Fatalf("fallback account status = %s, want temporary_unavailable", status)
	}
	if reasons := fixture.reasons.All(); len(reasons) != 1 {
		t.Fatalf("fallback invalidations = %v, want 1", reasons)
	}

	// original_status='active' 但账户已非 active：锁结算落地、账户不动、
	// accountAvailabilityChanged=false → 无失效通知（Node :363-375）。
	fixture.seedAccount(t, "acc_err", "error", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_err", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 2,
		incidentID:     sql.NullString{String: "acc_err:2:t", Valid: true},
		deadlineAt:     sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true},
		originalStatus: sql.NullString{String: "active", Valid: true}})
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_err", fixture.clock.NowMs(), nil); err != nil {
		t.Fatalf("error-status settle: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_err"); row.lockState != "DEAD_CONFIRMED" {
		t.Fatalf("lock must settle: %+v", row)
	}
	if status, _ := fixture.readAccount(t, "acc_err"); status != "error" {
		t.Fatalf("non-active account must stay untouched, got %s", status)
	}
	if reasons := fixture.reasons.All(); len(reasons) != 1 {
		t.Fatalf("non-flipped settle must not invalidate: %v", reasons)
	}

	// 观察代际不匹配：不结算。
	fixture.seedAccount(t, "acc_obs", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_obs", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 6,
		incidentID: sql.NullString{String: "acc_obs:6:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true}})
	stale := &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "acc_obs:6:t"}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_obs", fixture.clock.NowMs(), stale); err != nil {
		t.Fatalf("stale settle: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_obs"); row.lockState != "ENGAGED" {
		t.Fatalf("stale observation must not settle: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// findState 的 DEAD_CONFIRMED 自愈（归档 :106-121）
// ---------------------------------------------------------------------------

func TestChainAccountLocksFindStateRecoversDeadConfirmed(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, "acc_live", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_live", enabled: 1, lockState: "DEAD_CONFIRMED",
		deathTimeout: 300, retryInterval: 5, generation: 7,
		incidentID:     sql.NullString{String: "acc_live:7:t", Valid: true},
		originalStatus: sql.NullString{String: "active", Valid: true},
		provenance:     sql.NullString{String: "lock_policy", Valid: true}})

	view, err := fixture.locks.FindStateAsync(ctx, "acc_live")
	if err != nil || view == nil {
		t.Fatalf("FindState: %v, %v", view, err)
	}
	row := fixture.readLockRow(t, "acc_live")
	if row.lockState != "LOCKED_IDLE" || row.generation != 7 {
		t.Fatalf("recovered row = %s/%d, want LOCKED_IDLE/7", row.lockState, row.generation)
	}
	if row.incidentID.Valid || row.deadlineAt.Valid || row.originalStatus.Valid || row.provenance.Valid {
		t.Fatalf("recovered row must clear incident bookkeeping: %+v", row)
	}
	if view.Generation != 7 || view.BlocksCrossAccount {
		t.Fatalf("recovered view = %+v, want gen 7 non-blocking", view)
	}

	// 账户未恢复（temporary_unavailable）：保持 DEAD_CONFIRMED。
	fixture.seedAccount(t, "acc_down", "temporary_unavailable", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_down", enabled: 1, lockState: "DEAD_CONFIRMED",
		deathTimeout: 300, retryInterval: 5, generation: 2})
	view, err = fixture.locks.FindStateAsync(ctx, "acc_down")
	if err != nil || view == nil || view.BlocksCrossAccount {
		t.Fatalf("unrecovered FindState = (%+v, %v)", view, err)
	}
	if row := fixture.readLockRow(t, "acc_down"); row.lockState != "DEAD_CONFIRMED" {
		t.Fatalf("unrecovered row mutated: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// 重试租约族（归档 :393-494）
// ---------------------------------------------------------------------------

func TestChainAccountLocksRetryLeaseFamily(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, "acc_1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 2,
		incidentID: sql.NullString{String: "acc_1:2:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() + 300_000)), Valid: true}})

	// 首次 acquire：采样抖动窗口 [2000, 30000]，预约租约 +60s。
	lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_1", 3_000)
	if err != nil || !lease.Allowed || lease.LeaseID == "" {
		t.Fatalf("first acquire = %+v, %v", lease, err)
	}
	if lease.WaitMs < 2_000 || lease.WaitMs > 30_000 {
		t.Fatalf("sampled wait = %d, want within [2000, 30000]", lease.WaitMs)
	}
	nowMs := fixture.clock.NowMs()
	row := fixture.readLockRow(t, "acc_1")
	if !row.nextRetryAtMs.Valid || row.nextRetryAtMs.Int64 != nowMs+lease.WaitMs {
		t.Fatalf("next_retry_at_ms = %v, want %d", row.nextRetryAtMs, nowMs+lease.WaitMs)
	}
	if !row.leaseUntilMs.Valid || row.leaseUntilMs.Int64 != nowMs+lease.WaitMs+chainAccountLockReservationLeaseDurationMs {
		t.Fatalf("lease_until_ms = %v, want reservation window", row.leaseUntilMs)
	}
	leaseID := lease.LeaseID

	// 租约持有中再次 acquire：不允许，等待时间取 next_retry（Node :398-401）。
	second, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_1", 3_000)
	if err != nil || second.Allowed || second.WaitMs != lease.WaitMs {
		t.Fatalf("held acquire = %+v, %v, want denied with the sampled wait", second, err)
	}

	// 到期前 consume：拒绝。
	if consumed, err := fixture.locks.ConsumeRetryLeaseAsync(ctx, "acc_1", leaseID); err != nil || consumed {
		t.Fatalf("early consume = (%v, %v), want (false, nil)", consumed, err)
	}

	// 推进到到期：consume 成功并切换为 5min 派发租约（Node :435-443）。
	fixture.clock.AdvanceMs(lease.WaitMs + 1)
	consumed, err := fixture.locks.ConsumeRetryLeaseAsync(ctx, "acc_1", leaseID)
	if err != nil || !consumed {
		t.Fatalf("due consume = (%v, %v), want (true, nil)", consumed, err)
	}
	row = fixture.readLockRow(t, "acc_1")
	if !row.nextRetryAtMs.Valid || row.nextRetryAtMs.Int64 != fixture.clock.NowMs() {
		t.Fatalf("consumed next_retry = %v, want now", row.nextRetryAtMs)
	}
	if !row.leaseUntilMs.Valid || row.leaseUntilMs.Int64 != fixture.clock.NowMs()+chainAccountLockDispatchLeaseDurationMs {
		t.Fatalf("consumed lease_until = %v, want dispatch window", row.leaseUntilMs)
	}

	// release(scheduleNextRetry=true)：清租约并调度下次重试
	// completedAt + max(globalDelay, sampled)（Node :458-466）。
	releasedAt := fixture.clock.NowMs()
	released, err := fixture.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{
		AccountID:         "acc_1",
		LeaseID:           leaseID,
		GlobalDelayMs:     3_000,
		ScheduleNextRetry: true,
	})
	if err != nil || !released {
		t.Fatalf("release = (%v, %v), want (true, nil)", released, err)
	}
	row = fixture.readLockRow(t, "acc_1")
	if row.leaseID.Valid || row.leaseUntilMs.Valid {
		t.Fatalf("released lease fields must clear: %+v", row)
	}
	if !row.nextRetryAtMs.Valid || row.nextRetryAtMs.Int64 <= releasedAt {
		t.Fatalf("released next_retry = %v, want scheduled future", row.nextRetryAtMs)
	}
	scheduledDelay := row.nextRetryAtMs.Int64 - releasedAt
	if scheduledDelay < 2_000 || scheduledDelay > 30_000 {
		t.Fatalf("scheduled delay = %d, want sampled bounds (globalDelay 3s below base)", scheduledDelay)
	}

	// release(scheduleNextRetry=false)：清空 next_retry（Node :458 null）。
	// 先重新 acquire 一条租约：next_retry 未来 → acquire 拒绝（Node :406）。
	if again, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_1", 3_000); err != nil || again.Allowed {
		t.Fatalf("future-due acquire = %+v, %v, want denied", again, err)
	}
	fixture.clock.AdvanceMs(scheduledDelay + 1)
	// 到期时间已过：acquire 保留到期时间并立即认领（Node :62-65, :404）。
	reclaim, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_1", 3_000)
	if err != nil || !reclaim.Allowed || reclaim.LeaseID == "" || reclaim.WaitMs != 0 {
		t.Fatalf("due acquire = %+v, %v, want immediate claim", reclaim, err)
	}
	released, err = fixture.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{
		AccountID:         "acc_1",
		LeaseID:           reclaim.LeaseID,
		GlobalDelayMs:     3_000,
		ScheduleNextRetry: false,
	})
	if err != nil || !released {
		t.Fatalf("release(no-reschedule) = (%v, %v)", released, err)
	}
	if row := fixture.readLockRow(t, "acc_1"); row.nextRetryAtMs.Valid {
		t.Fatalf("no-reschedule must clear next_retry: %+v", row)
	}

	// abandon：只清预约租约、保留共享到期时间（Node :471-485）。
	reserved, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_1", 3_000)
	if err != nil || !reserved.Allowed || reserved.LeaseID == "" {
		t.Fatalf("reservation acquire = %+v, %v", reserved, err)
	}
	row = fixture.readLockRow(t, "acc_1")
	reservedDue := row.nextRetryAtMs.Int64
	if !row.leaseUntilMs.Valid || row.leaseUntilMs.Int64 != reservedDue+chainAccountLockReservationLeaseDurationMs {
		t.Fatalf("reservation lease_until mismatch: %+v", row)
	}
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "acc_1", LeaseID: reserved.LeaseID}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	row = fixture.readLockRow(t, "acc_1")
	if row.leaseID.Valid || row.leaseUntilMs.Valid {
		t.Fatalf("abandoned reservation must clear the lease: %+v", row)
	}
	if !row.nextRetryAtMs.Valid || row.nextRetryAtMs.Int64 != reservedDue {
		t.Fatalf("abandon must preserve the shared due time: %+v", row)
	}

	// 未知租约的 release/abandon：不生效。
	if released, err := fixture.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{AccountID: "acc_1", LeaseID: "unknown"}); err != nil || released {
		t.Fatalf("unknown release = (%v, %v)", released, err)
	}
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "acc_1", LeaseID: "unknown"}); err != nil {
		t.Fatalf("unknown abandon: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 观察租约栅栏（归档 :516-531 的 Go *string 映射）
// ---------------------------------------------------------------------------

func TestChainAccountLocksObservationLeaseFence(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()

	// recordFailure：observation.leaseId=nil（属性缺失）→ 无栅栏，正常进入。
	fixture.seedAccount(t, "acc_a", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_a", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 1})
	if err := fixture.locks.RecordFailureAsync(ctx, "acc_a", "upstream_transport_failure", nil); err != nil {
		t.Fatalf("record without fence: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_a"); row.lockState != "ENGAGED" {
		t.Fatalf("record must engage: %+v", row)
	}

	// settle：租约栅栏匹配当前持约 → 结算成功。
	fixture.seedAccount(t, "acc_b", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_b", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 3,
		incidentID:   sql.NullString{String: "acc_b:3:t", Valid: true},
		deadlineAt:   sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true},
		leaseID:      sql.NullString{String: "L1", Valid: true},
		leaseUntilMs: sql.NullInt64{Int64: fixture.clock.NowMs() + 60_000, Valid: true}})
	matching := &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "acc_b:3:t", LeaseID: fenceLease("L1")}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_b", fixture.clock.NowMs(), matching); err != nil {
		t.Fatalf("settle with matching lease fence: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_b"); row.lockState != "DEAD_CONFIRMED" {
		t.Fatalf("matching fence must settle: %+v", row)
	}

	// settle：租约栅栏不匹配 → 空操作。
	fixture.seedAccount(t, "acc_c", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_c", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 4,
		incidentID:   sql.NullString{String: "acc_c:4:t", Valid: true},
		deadlineAt:   sql.NullString{String: isoMillisOf(time.UnixMilli(fixture.clock.NowMs() - 1)), Valid: true},
		leaseID:      sql.NullString{String: "L1", Valid: true},
		leaseUntilMs: sql.NullInt64{Int64: fixture.clock.NowMs() + 60_000, Valid: true}})
	wrong := &gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "acc_c:4:t", LeaseID: fenceLease("L2")}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_c", fixture.clock.NowMs(), wrong); err != nil {
		t.Fatalf("settle with wrong lease fence: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_c"); row.lockState != "ENGAGED" {
		t.Fatalf("wrong lease fence must block the settle: %+v", row)
	}
	// leaseId=null（Go 空串）对应行上有租约：同样不结算。
	nullLease := &gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "acc_c:4:t", LeaseID: fenceLease("")}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_c", fixture.clock.NowMs(), nullLease); err != nil {
		t.Fatalf("settle with null lease fence: %v", err)
	}
	if row := fixture.readLockRow(t, "acc_c"); row.lockState != "ENGAGED" {
		t.Fatalf("null lease fence against a held lease must block: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// 采样延迟边界（归档 sampleLockDelayMs :487-494）
// ---------------------------------------------------------------------------

func TestSampleLockDelayMsBounds(t *testing.T) {
	for _, interval := range []int{5, 6, 10, 11, 30} {
		for seed := 0; seed < 200; seed++ {
			delay := sampleLockDelayMs(interval, strings.Repeat("x", seed+1))
			if delay < 2_000 || delay > 30_000 {
				t.Fatalf("sampleLockDelayMs(%d) = %d outside [2000, 30000]", interval, delay)
			}
		}
	}
	// 确定性：同一种子同一样本。
	if sampleLockDelayMs(5, "seed") != sampleLockDelayMs(5, "seed") {
		t.Fatal("same seed must sample deterministically")
	}
	// 非法配置回退默认 5s（与 internal/accounts 管理面归一一致）。
	delay := sampleLockDelayMs(9999, "seed")
	if delay < 2_000 || delay > 30_000 {
		t.Fatalf("invalid interval delay = %d", delay)
	}
}

// ---------------------------------------------------------------------------
// 显式关闭开关（JUHE_AI_ACCOUNT_LOCKS_DISABLED）
// ---------------------------------------------------------------------------

func TestChainAccountLocksDisabledViaEnv(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"false", false},
		{"1", false},
		{"true", true},
		{"TRUE", true},
		{" true ", true},
	}
	for _, testCase := range cases {
		getenv := func(key string) string {
			if key == "JUHE_AI_ACCOUNT_LOCKS_DISABLED" {
				return testCase.value
			}
			return ""
		}
		if got := chainAccountLocksDisabledViaEnv(getenv); got != testCase.want {
			t.Fatalf("disabled(%q) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
	if chainAccountLocksDisabledViaEnv(nil) {
		t.Fatal("nil getter must keep the real implementation")
	}
}

// ---------------------------------------------------------------------------
// 组合根接线：AccountLocks / EngineSecret / KeyRotation
// ---------------------------------------------------------------------------

type stubLocks struct{ gatewaydispatch.AccountLocks }

type stubRotation struct {
	gatewaydispatch.APIKeyRotationCounter
}

func TestComposeGatewayChainWiresLocksSecretAndRotation(t *testing.T) {
	fixture := newChainFixture(t)
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	stubbedLocks := stubLocks{}
	stubbedRotation := stubRotation{}
	deps.AccountLocks = &stubbedLocks
	deps.EngineSecret = "wired-secret"
	deps.KeyRotation = &stubbedRotation
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	if chain.engine.Locks != gatewaydispatch.AccountLocks(&stubbedLocks) {
		t.Fatalf("engine.Locks = %T, want the injected stub", chain.engine.Locks)
	}
	if chain.engine.Config.Secret != "wired-secret" {
		t.Fatalf("engine.Config.Secret = %q, want the injected secret", chain.engine.Config.Secret)
	}
	if chain.engine.KeyRotation != gatewaydispatch.APIKeyRotationCounter(&stubbedRotation) {
		t.Fatalf("engine.KeyRotation = %T, want the injected stub", chain.engine.KeyRotation)
	}

	// 未注入的装配保持显式降级（AccountLocks）与进程内回退（KeyRotation）。
	degradedDeps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	degradedChain, degradedShutdown, degradedErr := composeGatewayChain(degradedDeps)
	if degradedErr != nil {
		t.Fatalf("compose degraded chain: %v", degradedErr)
	}
	defer degradedShutdown()
	if _, ok := degradedChain.engine.Locks.(*disabledAccountLocks); !ok {
		t.Fatalf("degraded engine.Locks = %T, want disabledAccountLocks", degradedChain.engine.Locks)
	}
	if degradedChain.engine.KeyRotation != nil {
		t.Fatalf("degraded engine.KeyRotation = %T, want nil (in-process fallback)", degradedChain.engine.KeyRotation)
	}
}

// TestComposeWiresAccountLockSecretRotationFromConfig locks the compose.go
// wiring text: EngineSecret/KeyRotation/AccountLocks ride the composition root
// exactly at the documented sources（水合层 cfg.Secret 同源 + StateClient 同源
// + 显式关闭开关）。这与 compose_account_balance_refresh_test.go 的源码接线
// 断言同一模式——完整 composeSystemAPI 装配过重，此处锁定关键接线文本。
func TestComposeWiresAccountLockSecretRotationFromConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("compose.go"))
	if err != nil {
		t.Fatalf("read compose.go: %v", err)
	}
	text := string(raw)
	for _, needle := range []string{
		"EngineSecret: cfg.Secret,",
		"AccountLocks: accountLockPort,",
		"KeyRotation: newChainAPIKeyRotationCounterOrNil(chainServices.StateClient),",
		"newChainAccountLocks(composed.db, composed.pgDialect,",
		`chainAccountLocksDisabledViaEnv(os.Getenv)`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("compose.go missing wiring %q", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// Redis 轮转计数器适配器（归档 account-api-key-rotation.ts :269-299）
// ---------------------------------------------------------------------------

func TestChainAPIKeyRotationRedisCounter(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := newChainAPIKeyRotationCounterOrNil(client)
	if counter == nil {
		t.Fatal("redis client must build the adapter")
	}
	ctx := context.Background()

	// (value - 1) % modulo 序列：0,1,2,0（Node :285）。
	for step, want := range []int{0, 1, 2, 0} {
		got, err := counter.NextIndex(ctx, "acc-1", "round-robin", 3)
		if err != nil || got != want {
			t.Fatalf("NextIndex seq[%d] = (%d, %v), want %d", step, got, err, want)
		}
	}
	// weighted 键段独立计数。
	if got, err := counter.NextIndex(ctx, "acc-1", "weighted", 7); err != nil || got != 0 {
		t.Fatalf("weighted NextIndex = (%d, %v), want (0, nil)", got, err)
	}
	// modulo <= 0 直接 0 且不建键（Node :274）。
	if got, err := counter.NextIndex(ctx, "acc-1", "round-robin", 0); err != nil || got != 0 {
		t.Fatalf("modulo 0 NextIndex = (%d, %v), want (0, nil)", got, err)
	}

	// 键名：juhe-ai:route-state:account-api-key:<strategy>:<base64url(accountId)>
	//（归档 :297-299 字面量，无命名空间后缀）。
	wantKey := "juhe-ai:route-state:account-api-key:round-robin:" +
		base64.RawURLEncoding.EncodeToString([]byte("acc-1"))
	if chainAccountAPIKeyRotationKey("acc-1", "round-robin") != wantKey {
		t.Fatalf("key = %s, want %s", chainAccountAPIKeyRotationKey("acc-1", "round-robin"), wantKey)
	}
	// 不同账户键隔离。
	if got, err := counter.NextIndex(ctx, "acc-2", "round-robin", 3); err != nil || got != 0 {
		t.Fatalf("second account NextIndex = (%d, %v), want fresh counter", got, err)
	}

	// 键集合：round-robin/weighted 两个策略段 + 第二账户的独立键。
	keys, err := client.Keys(ctx, "juhe-ai:route-state:account-api-key:*").Result()
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	wantKeys := map[string]bool{
		wantKey: true,
		"juhe-ai:route-state:account-api-key:weighted:" + base64.RawURLEncoding.EncodeToString([]byte("acc-1")):    true,
		"juhe-ai:route-state:account-api-key:round-robin:" + base64.RawURLEncoding.EncodeToString([]byte("acc-2")): true,
	}
	if len(keys) != len(wantKeys) {
		t.Fatalf("keys = %v, want %v", keys, wantKeys)
	}
	for _, key := range keys {
		if !wantKeys[key] {
			t.Fatalf("unexpected key %v in %v", key, keys)
		}
		ttl, ttlErr := client.TTL(ctx, key).Result()
		if ttlErr != nil || ttl <= 0 || ttl > 30*24*time.Hour {
			t.Fatalf("ttl of %s = %v, %v; want within the 30d window", key, ttl, ttlErr)
		}
	}

	// nil 客户端：保持引擎的进程内回退。
	if newChainAPIKeyRotationCounterOrNil(nil) != nil {
		t.Fatal("nil client must yield nil adapter")
	}
}
