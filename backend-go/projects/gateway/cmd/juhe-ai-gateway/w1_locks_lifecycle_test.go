package main

// chain_account_locks.go 的补充单元测试（w1 波次）：覆盖既有
// chain_account_locks_test.go 12 个行为测试之外的构造、方言、纯逻辑辅助函数与
// SQLite CAS 冲突/行缺失分支。复用现有 lockFixture / seedAccount / seedLockRow /
// readLockRow 等 helper（不重复定义），新增标识符统一 w1s 前缀。

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// ---------------------------------------------------------------------------
// newChainAccountLocks 构造分支
// ---------------------------------------------------------------------------

func TestW1SNewChainAccountLocksRejectsNilDB(t *testing.T) {
	if _, err := newChainAccountLocks(nil, false, nil); err == nil {
		t.Fatal("newChainAccountLocks(nil) 应拒绝空业务库句柄")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	locksWithNil, err := newChainAccountLocks(db, false, nil)
	if err != nil {
		t.Fatalf("build locks with nil callback: %v", err)
	}
	if locksWithNil.invalidateRuntimeCache == nil {
		t.Fatal("invalidateRuntimeCache 必须归一为非 nil no-op 闭包")
	}
	locksWithNil.invalidateRuntimeCache("noop-reason")
	if locksWithNil.db != db {
		t.Fatal("锁端口必须复用传入的业务库句柄")
	}
	if locksWithNil.newToken == nil || locksWithNil.now == nil {
		t.Fatal("newToken / now 必须填充默认实现")
	}

	touched := false
	locksWithCallback, err := newChainAccountLocks(db, true, func(string) { touched = true })
	if err != nil {
		t.Fatalf("build locks with callback: %v", err)
	}
	locksWithCallback.invalidateRuntimeCache("custom")
	if !touched {
		t.Fatal("自定义 invalidateRuntimeCache 必须被调用")
	}
	if !locksWithCallback.postgres {
		t.Fatal("postgres 标志必须透传")
	}
}

// ---------------------------------------------------------------------------
// table / bind 方言（postgres 前缀 + $N）—— 不依赖真 PG
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksTableDialect(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	if got := fixture.locks.table("account_lock_states"); got != "account_lock_states" {
		t.Fatalf("sqlite table = %q, want 裸表名", got)
	}
	pg := &chainAccountLocks{postgres: true}
	if got := pg.table("account_lock_states"); got != "juhe_business.account_lock_states" {
		t.Fatalf("postgres table = %q", got)
	}
	if got := pg.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("postgres table accounts = %q", got)
	}
}

func TestW1SChainAccountLocksBindDialect(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	sqliteIn := "SELECT a, b FROM t WHERE x = ? AND y = ?"
	if got := fixture.locks.bind(sqliteIn); got != sqliteIn {
		t.Fatalf("sqlite bind 必须原样返回, got %q", got)
	}
	pg := &chainAccountLocks{postgres: true}
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"空串", "", ""},
		{"无占位符", "SELECT 1", "SELECT 1"},
		{"单占位符", "WHERE id = ?", "WHERE id = $1"},
		{"多占位符", "WHERE a = ? AND b = ? AND c = ?", "WHERE a = $1 AND b = $2 AND c = $3"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := pg.bind(testCase.query); got != testCase.want {
				t.Fatalf("pg.bind(%q) = %q, want %q", testCase.query, got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chainAccountLockDeadlineMs：NULL / 空串 / 不可解析 / 有效
// ---------------------------------------------------------------------------

func TestW1SChainAccountLockDeadlineMs(t *testing.T) {
	if ms, ok := chainAccountLockDeadlineMs(sql.NullString{}); ms != 0 || ok {
		t.Fatalf("NULL deadline = (%d, %v), want (0, false)", ms, ok)
	}
	if ms, ok := chainAccountLockDeadlineMs(sql.NullString{String: "  ", Valid: true}); ms != 0 || ok {
		t.Fatalf("空白 deadline = (%d, %v), want (0, false)", ms, ok)
	}
	if ms, ok := chainAccountLockDeadlineMs(sql.NullString{String: "not-a-date", Valid: true}); ms != 0 || ok {
		t.Fatalf("不可解析 deadline = (%d, %v)", ms, ok)
	}
	want := int64(1_757_000_000_000)
	value := sql.NullString{String: isoMillisOf(time.UnixMilli(want)), Valid: true}
	got, ok := chainAccountLockDeadlineMs(value)
	if !ok || got != want {
		t.Fatalf("有效 deadline = (%d, %v), want (%d, true)", got, ok, want)
	}
}

// ---------------------------------------------------------------------------
// blocksCrossAccount：全分支（nil / enabled / state / deadline）
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksBlocksCrossAccount(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	nowMs := fixture.clock.NowMs()
	futureDeadline := isoMillisOf(time.UnixMilli(nowMs + 60_000))
	pastDeadline := isoMillisOf(time.UnixMilli(nowMs - 1))

	cases := []struct {
		name string
		row  *chainAccountLockRow
		want bool
	}{
		{"nil 行", nil, false},
		{"enabled=0", &chainAccountLockRow{enabled: 0, lockState: "ENGAGED"}, false},
		{"非 ENGAGED", &chainAccountLockRow{enabled: 1, lockState: "LOCKED_IDLE"}, false},
		{"ENGAGED 无 deadline 阻断", &chainAccountLockRow{enabled: 1, lockState: "ENGAGED"}, true},
		{"ENGAGED 未来 deadline", &chainAccountLockRow{enabled: 1, lockState: "ENGAGED",
			deadlineAt: sql.NullString{String: futureDeadline, Valid: true}}, true},
		{"ENGAGED 已过期 deadline", &chainAccountLockRow{enabled: 1, lockState: "ENGAGED",
			deadlineAt: sql.NullString{String: pastDeadline, Valid: true}}, false},
		{"ENGAGED deadline 不可解析", &chainAccountLockRow{enabled: 1, lockState: "ENGAGED",
			deadlineAt: sql.NullString{String: "garbage", Valid: true}}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := fixture.locks.blocksCrossAccount(testCase.row, nowMs); got != testCase.want {
				t.Fatalf("blocksCrossAccount = %v, want %v", got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// viewOf：nil -> nil；非 nil -> 代际 / 事故 / 阻断投影
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksViewOf(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	nowMs := fixture.clock.NowMs()
	if view := fixture.locks.viewOf(nil, nowMs); view != nil {
		t.Fatalf("viewOf(nil) = %+v, want nil", view)
	}
	engaged := &chainAccountLockRow{enabled: 1, lockState: "ENGAGED", generation: 9,
		incidentID: sql.NullString{String: "inc-9", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 1000)), Valid: true}}
	view := fixture.locks.viewOf(engaged, nowMs)
	if view == nil {
		t.Fatal("viewOf(engaged) 不得为 nil")
	}
	if view.Generation != 9 || view.IncidentID != "inc-9" || !view.BlocksCrossAccount {
		t.Fatalf("viewOf(engaged) = %+v, want gen 9 / inc-9 / 阻断", view)
	}
	idle := &chainAccountLockRow{enabled: 1, lockState: "LOCKED_IDLE", generation: 3}
	if view := fixture.locks.viewOf(idle, nowMs); view.BlocksCrossAccount || view.IncidentID != "" {
		t.Fatalf("viewOf(idle) = %+v, want non-blocking / 空事故", view)
	}
}

// ---------------------------------------------------------------------------
// readStateOnce：行存在 / 行缺失
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksReadStateOnce(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()

	row, err := fixture.locks.readStateOnce(ctx, "w1s-missing")
	if err != nil || row != nil {
		t.Fatalf("readStateOnce(missing) = (%+v, %v), want (nil, nil)", row, err)
	}

	fixture.seedAccount(t, "w1s-1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-1", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 6})
	row, err = fixture.locks.readStateOnce(ctx, "w1s-1")
	if err != nil {
		t.Fatalf("readStateOnce: %v", err)
	}
	if row == nil || row.accountID != "w1s-1" || row.lockState != "LOCKED_IDLE" || row.generation != 6 {
		t.Fatalf("readStateOnce 行投影错误: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// AcquireRetryLeaseAsync：CAS 冲突 -> denied / max(1, wait)
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksAcquireCASHurdle(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	nowMs := fixture.clock.NowMs()

	fixture.seedAccount(t, "w1s-1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID:    sql.NullString{String: "w1s-1:1:t", Valid: true},
		deadlineAt:    sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 300_000)), Valid: true},
		nextRetryAtMs: sql.NullInt64{Int64: nowMs + 10_000, Valid: true}})

	// generation 被并发改动 -> CAS 不命中。
	if _, err := fixture.db.Exec(`UPDATE account_lock_states SET generation = 99 WHERE account_id = ?`, "w1s-1"); err != nil {
		t.Fatalf("advance generation: %v", err)
	}
	denied, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "w1s-1", 3_000)
	if err != nil {
		t.Fatalf("acquire with CAS conflict: %v", err)
	}
	if denied.Allowed {
		t.Fatalf("CAS 冲突必须拒绝: %+v", denied)
	}
	if denied.WaitMs < 1 {
		t.Fatalf("CAS 冲突 wait 必须 >=1ms: %+v", denied)
	}
}

// ---------------------------------------------------------------------------
// ConsumeRetryLeaseAsync：租约过期分支
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksConsumeExpiredLease(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	nowMs := fixture.clock.NowMs()

	fixture.seedAccount(t, "w1s-1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 2,
		incidentID:    sql.NullString{String: "w1s-1:2:t", Valid: true},
		deadlineAt:    sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 300_000)), Valid: true},
		nextRetryAtMs: sql.NullInt64{Int64: nowMs - 1, Valid: true},
		leaseID:       sql.NullString{String: "expired-lease", Valid: true},
		leaseUntilMs:  sql.NullInt64{Int64: nowMs - 1, Valid: true}})
	consumed, err := fixture.locks.ConsumeRetryLeaseAsync(ctx, "w1s-1", "expired-lease")
	if err != nil || consumed {
		t.Fatalf("过期租约 consume = (%v, %v), want (false, nil)", consumed, err)
	}

	// 到期 + 租约仍有效 -> consume 成功。
	fixture.seedAccount(t, "w1s-2", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-2", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID:    sql.NullString{String: "w1s-2:1:t", Valid: true},
		deadlineAt:    sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 300_000)), Valid: true},
		nextRetryAtMs: sql.NullInt64{Int64: nowMs - 5, Valid: true},
		leaseID:       sql.NullString{String: "live-lease", Valid: true},
		leaseUntilMs:  sql.NullInt64{Int64: nowMs + 60_000, Valid: true}})
	consumed, err = fixture.locks.ConsumeRetryLeaseAsync(ctx, "w1s-2", "live-lease")
	if err != nil || !consumed {
		t.Fatalf("到期且有效租约 consume = (%v, %v), want (true, nil)", consumed, err)
	}
}

// ---------------------------------------------------------------------------
// AbandonRetryReservationAsync：租约不匹配 / lease_until 不匹配 -> 空操作
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksAbandonNoOp(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	nowMs := fixture.clock.NowMs()

	fixture.seedAccount(t, "w1s-1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID: sql.NullString{String: "w1s-1:1:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 300_000)), Valid: true}})
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "w1s-1", LeaseID: "ghost"}); err != nil {
		t.Fatalf("abandon(ghost) = %v, want nil", err)
	}

	// 空 leaseID 直接返回。
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "w1s-1", LeaseID: "  "}); err != nil {
		t.Fatalf("abandon(blank) = %v, want nil", err)
	}

	// 已持预约租约但 lease_until != next_retry + 60s：不命中 WHERE。
	fixture.seedAccount(t, "w1s-2", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-2", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID:    sql.NullString{String: "w1s-2:1:t", Valid: true},
		deadlineAt:    sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs + 300_000)), Valid: true},
		nextRetryAtMs: sql.NullInt64{Int64: nowMs + 5_000, Valid: true},
		leaseID:       sql.NullString{String: "L-mismatch", Valid: true},
		leaseUntilMs:  sql.NullInt64{Int64: nowMs + 5_000 + 60_001, Valid: true}})
	if err := fixture.locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "w1s-2", LeaseID: "L-mismatch"}); err != nil {
		t.Fatalf("abandon(mismatch) = %v", err)
	}
	if row := fixture.readLockRow(t, "w1s-2"); !row.leaseID.Valid || row.leaseID.String != "L-mismatch" {
		t.Fatalf("lease_until 不匹配时 abandon 不得清租约: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// chainAccountLockRetryReservationDueAtMs
// ---------------------------------------------------------------------------

func TestW1SChainAccountLockRetryReservationDueAtMs(t *testing.T) {
	cases := []struct {
		name        string
		nextRetry   sql.NullInt64
		nowMs       int64
		wantDue     int64
		wantPresent bool
	}{
		{"未设置", sql.NullInt64{}, 5000, 0, false},
		{"已过期立即到期", sql.NullInt64{Int64: 1000, Valid: true}, 5000, 5000, true},
		{"未来到期", sql.NullInt64{Int64: 9000, Valid: true}, 5000, 9000, true},
		{"恰好等于 now", sql.NullInt64{Int64: 5000, Valid: true}, 5000, 5000, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := chainAccountLockRetryReservationDueAtMs(testCase.nextRetry, testCase.nowMs)
			if got != testCase.wantDue || ok != testCase.wantPresent {
				t.Fatalf("retryReservationDueAtMs = (%d, %v), want (%d, %v)",
					got, ok, testCase.wantDue, testCase.wantPresent)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// observationMatches / LeaseFence
// ---------------------------------------------------------------------------

func TestW1SChainAccountLockObservationMatches(t *testing.T) {
	row := &chainAccountLockRow{generation: 5, incidentID: sql.NullString{String: "i1", Valid: true}}
	if !chainAccountLockObservationMatches(row, nil) {
		t.Fatal("nil 观察视为匹配")
	}
	if !chainAccountLockObservationMatches(row, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "i1"}) {
		t.Fatal("代际与事故匹配应放行")
	}
	if chainAccountLockObservationMatches(row, &gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "i1"}) {
		t.Fatal("代际不匹配不得放行")
	}
	if chainAccountLockObservationMatches(row, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "other"}) {
		t.Fatal("事故不匹配不得放行")
	}
}

func TestW1SChainAccountLockLeaseFence(t *testing.T) {
	fence, args := chainAccountLockLeaseFence(nil, 1000)
	if fence != "" || args != nil {
		t.Fatalf("nil 观察栅栏 = (%q, %v)", fence, args)
	}
	fence, args = chainAccountLockLeaseFence(&gatewaydispatch.AccountLockObservation{LeaseID: nil}, 1000)
	if fence != "" || args != nil {
		t.Fatalf("nil lease 栅栏 = (%q, %v)", fence, args)
	}
	fence, args = chainAccountLockLeaseFence(&gatewaydispatch.AccountLockObservation{LeaseID: fenceLease("  ")}, 1000)
	if fence != " AND lease_id IS NULL" || len(args) != 0 {
		t.Fatalf("空串 lease 栅栏 = (%q, %v)", fence, args)
	}
	fence, args = chainAccountLockLeaseFence(&gatewaydispatch.AccountLockObservation{LeaseID: fenceLease("L1")}, 2000)
	if fence != " AND lease_id = ? AND lease_until_ms > ?" {
		t.Fatalf("持约栅栏文本 = %q", fence)
	}
	if len(args) != 2 || args[0] != any("L1") || args[1] != any(int64(2000)) {
		t.Fatalf("持约栅栏参数 = %v, want [L1 2000]", args)
	}
}

// ---------------------------------------------------------------------------
// SettleDeadlineAsync：observation 代际不匹配 -> 空操作 / nil 观察正常结算
// ---------------------------------------------------------------------------

func TestW1SChainAccountLocksSettleDeadlineStaleObservation(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	ctx := context.Background()
	nowMs := fixture.clock.NowMs()

	fixture.seedAccount(t, "w1s-1", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-1", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 2,
		incidentID: sql.NullString{String: "w1s-1:2:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs - 1)), Valid: true}})
	stale := &gatewaydispatch.AccountLockObservation{Generation: 999, IncidentID: "mismatch"}
	if err := fixture.locks.SettleDeadlineAsync(ctx, "w1s-1", nowMs, stale); err != nil {
		t.Fatalf("settle stale: %v", err)
	}
	if row := fixture.readLockRow(t, "w1s-1"); row.lockState != "ENGAGED" {
		t.Fatalf("过期 observation 不得结算: %+v", row)
	}

	// nil observation 代际匹配：正常结算到 DEAD_CONFIRMED。
	fixture.seedAccount(t, "w1s-2", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1s-2", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 3,
		incidentID: sql.NullString{String: "w1s-2:3:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs - 1)), Valid: true}})
	if err := fixture.locks.SettleDeadlineAsync(ctx, "w1s-2", nowMs, nil); err != nil {
		t.Fatalf("settle nil observation: %v", err)
	}
	if row := fixture.readLockRow(t, "w1s-2"); row.lockState != "DEAD_CONFIRMED" {
		t.Fatalf("nil observation 必须结算: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// sampleLockDelayMs：jitter 分支（base>10000 -> jitter 5000）+ 归一化
// ---------------------------------------------------------------------------

func TestW1SSampleLockDelayJitterBranches(t *testing.T) {
	for _, interval := range []int{11, 30} {
		delay := sampleLockDelayMs(interval, "w1s-seed-branch")
		if delay < 2_000 || delay > 30_000 {
			t.Fatalf("sampleLockDelayMs(%d) = %d 越界", interval, delay)
		}
	}
	var sawBig bool
	for seed := 0; seed < 300; seed++ {
		if sampleLockDelayMs(20, strings.Repeat("z", seed+1)) > 10_000 {
			sawBig = true
			break
		}
	}
	if !sawBig {
		t.Fatal("interval=20 的采样延迟必须出现 >10000 的抖动样本（jitter=5000 分支）")
	}
	// 归一化：非法 interval 回退默认 5（base 5000）。
	if got := sampleLockDelayMs(0, "x"); got < 2_000 || got > 15_000 {
		t.Fatalf("归一化到默认 5s 后延迟 %d 越界", got)
	}
	if norm, _ := accounts.NormalizeLockRetryIntervalSeconds(5); norm != 5 {
		t.Fatalf("NormalizeLockRetryIntervalSeconds(5) = %d", norm)
	}
}

// ---------------------------------------------------------------------------
// absInt64 / absInt32 / maxInt64 / isoMillisOf / nullString* / token
// ---------------------------------------------------------------------------

func TestW1SAbsAndMaxHelpers(t *testing.T) {
	if absInt64(-7) != 7 || absInt64(7) != 7 || absInt64(0) != 0 {
		t.Fatal("absInt64 基本分支错误")
	}
	if absInt32(-9) != 9 || absInt32(9) != 9 || absInt32(0) != 0 {
		t.Fatal("absInt32 基本分支错误")
	}
	if maxInt64(3, 5) != 5 || maxInt64(5, 3) != 5 || maxInt64(-1, -2) != -1 {
		t.Fatal("maxInt64 分支错误")
	}
}

func TestW1SIsoMillisOfStableFormat(t *testing.T) {
	stamp := isoMillisOf(time.Date(2026, 3, 4, 5, 6, 7, 890_000_000, time.UTC))
	if stamp != "2026-03-04T05:06:07.890Z" {
		t.Fatalf("isoMillisOf = %q, want 2026-03-04T05:06:07.890Z", stamp)
	}
	offsetZone := time.FixedZone("+0800", 8*3600)
	if isoMillisOf(time.Date(2026, 1, 1, 0, 0, 0, 0, offsetZone)) != "2025-12-31T16:00:00.000Z" {
		t.Fatalf("isoMillisOf 未按 UTC 归一")
	}
}

func TestW1SNullStringHelpers(t *testing.T) {
	if got := nullStringText(sql.NullString{}); got != "" {
		t.Fatalf("nullStringText(NULL) = %q, want 空串", got)
	}
	if got := nullStringText(sql.NullString{String: "abc", Valid: true}); got != "abc" {
		t.Fatalf("nullStringText(abc) = %q", got)
	}
	if nullStringOrNull(sql.NullString{}) != nil {
		t.Fatal("nullStringOrNull(NULL) 应为 nil")
	}
	if nullStringOrNull(sql.NullString{String: "  ", Valid: true}) != nil {
		t.Fatal("nullStringOrNull(空白) 应为 nil")
	}
	if got := nullStringOrNull(sql.NullString{String: "val", Valid: true}); got != any("val") {
		t.Fatalf("nullStringOrNull(val) = %v", got)
	}
}

func TestW1SChainLockRandomToken(t *testing.T) {
	pattern := regexp.MustCompile("^[0-9a-f]{32}$")
	first := chainLockRandomToken()
	if !pattern.MatchString(first) {
		t.Fatalf("chainLockRandomToken() = %q, want 32 位小写十六进制", first)
	}
	seen := map[string]struct{}{first: {}}
	for i := 0; i < 100; i++ {
		token := chainLockRandomToken()
		if _, dup := seen[token]; dup {
			t.Fatalf("chainLockRandomToken 重复: %s", token)
		}
		seen[token] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// scanChainAccountLockRow：sql.ErrNoRows 归一 (nil,nil)；其它错误透传
// ---------------------------------------------------------------------------

func TestW1SScanChainAccountLockRow(t *testing.T) {
	row, err := scanChainAccountLockRow(func(...any) error { return sql.ErrNoRows })
	if row != nil || err != nil {
		t.Fatalf("ErrNoRows = (%v, %v), want (nil, nil)", row, err)
	}
	sentinel := errors.New("w1s-scan-failure")
	row, err = scanChainAccountLockRow(func(...any) error { return sentinel })
	if row != nil || !errors.Is(err, sentinel) {
		t.Fatalf("真实错误必须透传: (%v, %v)", row, err)
	}
}
