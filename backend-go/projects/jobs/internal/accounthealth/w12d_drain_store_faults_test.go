package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// w12d_drain_store_faults_test.go 覆盖 w12d 波次的 outbox drain 消费臂与
// store 故障注入臂（closed store / 非法参数 / SQLite duplicate 幂等）。

// w12dFakeOutboxStore 是 ProbeRequestOutboxStore 的脚本化 Mock（可回放）。
type w12dFakeOutboxStore struct {
	rows    []ProbeOutboxRow
	claimErr error
	completeErr error
	claimed  []string
	completed []string
}

func (s *w12dFakeOutboxStore) ClaimPendingProbeRequests(ctx context.Context, limit int, now time.Time) ([]ProbeOutboxRow, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.claimed = append(s.claimed, "batch")
	return s.rows, nil
}

func (s *w12dFakeOutboxStore) CompleteProbeRequest(ctx context.Context, requestID string, now time.Time) (bool, error) {
	if s.completeErr != nil {
		return false, s.completeErr
	}
	s.completed = append(s.completed, requestID)
	return true, nil
}

// w12dFakeBoundary 是 ProbeRequestBoundary 的脚本化 Mock。
type w12dFakeBoundary struct {
	config, dispatch, version int64
	ok                        bool
	err                       error
	calls                     []string
}

func (b *w12dFakeBoundary) CurrentProbeInput(ctx context.Context, accountID string) (int64, int64, int64, bool, error) {
	b.calls = append(b.calls, accountID)
	if b.err != nil {
		return 0, 0, 0, false, b.err
	}
	return b.config, b.dispatch, b.version, b.ok, nil
}

func TestW12dProbeOutboxDrainArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-outbox.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w12d-drain-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %t", err, acquired)
	}
	runner := NewRunner(Config{InputDirectory: t.TempDir(), InputKeys: map[string][]byte{"current": []byte("k")}, ProbeTimeout: time.Second, MaxResponseBytes: 1024, Now: func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }}, store, nil)
	// 未装配 → 静默跳过。
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("no drain configured: %v", err)
	}
	// claim 失败臂。
	failingStore := &w12dFakeOutboxStore{claimErr: errors.New("claim failed")}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: failingStore, Boundary: &w12dFakeBoundary{ok: true, version: 1}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err == nil || err.Error() != "claim failed" {
		t.Fatalf("claim failure: %v", err)
	}
	// complete 失败臂（行处理成功但出队失败 → 首错）。
	completeFail := &w12dFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "w12d-ob-1", AccountID: "w12d-ob-acc", Reason: "activation", Deadline: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)}}, completeErr: errors.New("complete failed")}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: completeFail, Boundary: &w12dFakeBoundary{ok: true, config: 1, dispatch: 1, version: 1}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err == nil || err.Error() != "complete failed" {
		t.Fatalf("complete failure: %v", err)
	}
	// 字段缺失行 → 收敛且不出队。
	invalidRows := &w12dFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "  ", AccountID: "w12d-ob-acc", Reason: "activation"}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: invalidRows, Boundary: &w12dFakeBoundary{ok: true, version: 1}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("invalid row: %v", err)
	}
	if len(invalidRows.completed) != 1 || invalidRows.completed[0] != "  " {
		t.Fatalf("invalid row must still be dequeued: %+v", invalidRows.completed)
	}
	// 范围外账户：无 fence → 收敛；有 fence → 结算 unknown。
	settled := ""
	drain := &ProbeRequestDrain{
		Store: &w12dFakeOutboxStore{rows: []ProbeOutboxRow{
			{RequestID: "w12d-ob-2", AccountID: "w12d-ob-out", Reason: "activation", Deadline: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)},
			{RequestID: "w12d-ob-3", AccountID: "w12d-ob-out2", Reason: "activation", Deadline: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC), SourceFence: &SourceFence{StateKey: "w12d-state", SourceFenceID: "w12d-fence", RuntimeKey: "w12d-runtime", AccountID: "w12d-ob-out2", ConfigRevision: 1, SourceGeneration: 1, ProbeGeneration: 1}},
		}},
		Boundary:    &w12dFakeBoundary{ok: false},
		SettleFence: func(ctx context.Context, fence SourceFence, state string) error { settled = state; return nil },
	}
	runner.SetProbeRequestDrain(drain)
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("out of scope rows: %v", err)
	}
	if settled != "unknown" {
		t.Fatalf("settled=%q", settled)
	}
	// fence revision 漂移 → 放弃派发按已处理收敛。
	staleFence := &w12dFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "w12d-ob-4", AccountID: "w12d-ob-acc", Reason: "activation", Deadline: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC), SourceFence: &SourceFence{StateKey: "s", SourceFenceID: "f", RuntimeKey: "r", AccountID: "w12d-ob-acc", ConfigRevision: 99, SourceGeneration: 1, ProbeGeneration: 1}}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: staleFence, Boundary: &w12dFakeBoundary{ok: true, config: 1, dispatch: 1, version: 1}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("stale fence row: %v", err)
	}
	if len(staleFence.completed) != 1 {
		t.Fatalf("stale fence must be dequeued: %+v", staleFence.completed)
	}
	// 缺 deadline → 收敛出队。
	noDeadline := &w12dFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "w12d-ob-5", AccountID: "w12d-ob-acc", Reason: "activation"}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: noDeadline, Boundary: &w12dFakeBoundary{ok: true, config: 1, dispatch: 1, version: 1}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("no deadline row: %v", err)
	}
	if len(noDeadline.completed) != 1 {
		t.Fatalf("no deadline must be dequeued: %+v", noDeadline.completed)
	}
	// boundary 错误 → 保留 pending。
	brokenBoundary := &w12dFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "w12d-ob-6", AccountID: "w12d-ob-acc", Reason: "activation", Deadline: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: brokenBoundary, Boundary: &w12dFakeBoundary{err: errors.New("boundary failed")}})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err == nil || err.Error() != "boundary failed" {
		t.Fatalf("boundary failure: %v", err)
	}
	// ParseProbeOutboxSourceFence 臂。
	if fence, err := ParseProbeOutboxSourceFence("  "); err != nil || fence != nil {
		t.Fatalf("blank fence: %+v %v", fence, err)
	}
	if _, err := ParseProbeOutboxSourceFence("{bad"); err == nil {
		t.Fatal("bad fence json must fail")
	}
}

// TestW12dStoreFaultArms 用 closed store 与非法参数覆盖 store 错误臂。
func TestW12dStoreFaultArms(t *testing.T) {
	ctx := context.Background()
	// OpenStore 参数错误。
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite}); err == nil {
		t.Fatal("missing sqlite path must fail")
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("redis")}); err == nil {
		t.Fatal("unknown mode must fail")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres}); err == nil {
		t.Fatal("missing postgres url must fail")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://w12d/x", PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 4}); err == nil {
		t.Fatal("idle > open must fail")
	}
	// closed store：每个方法都必须把底层错误上抛（不静默降级）。
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-faults.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err == nil {
		t.Fatal("closed EnsureSchema must fail")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w12d-fault", time.Minute); err == nil {
		t.Fatal("closed acquire must fail")
	}
	lease := OwnerLease{OwnerID: "w12d-fault", FenceToken: 1}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil {
		t.Fatal("closed renew must fail")
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
		t.Fatal("closed release must fail")
	}
	if _, err := store.AppendOutcome(ctx, lease, Outcome{OutcomeID: "w12d-x", RequestID: "w12d-x", AccountID: "w12d-x", ObservedAt: time.Now(), InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}); err == nil {
		t.Fatal("closed append must fail")
	}
	if _, _, err := store.LoadCurrentState(ctx, "w12d-x"); err == nil {
		t.Fatal("closed load state must fail")
	}
	if _, err := store.LoadDirectInputSuppressions(ctx, time.Now()); err == nil {
		t.Fatal("closed load suppressions must fail")
	}
	if _, err := store.HasRequest(ctx, "w12d-x"); err == nil {
		t.Fatal("closed has request must fail")
	}
	if _, _, err := store.LoadKeyCursor(ctx, "w12d-x", "balance", "fp"); err == nil {
		t.Fatal("closed load cursor must fail")
	}
	if err := store.SaveKeyCursor(ctx, lease, "w12d-x", "balance", "fp", 0); err == nil {
		t.Fatal("closed save cursor must fail")
	}
	// 参数校验臂（open store）。
	open, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-args.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = open.Close() })
	if _, _, err := open.AcquireOwnerLease(ctx, " ", time.Minute); err == nil {
		t.Fatal("blank owner must fail")
	}
	if _, _, err := open.AcquireOwnerLease(ctx, "w12d-a", 0); err == nil {
		t.Fatal("zero duration must fail")
	}
	if _, err := open.RenewOwnerLease(ctx, lease, 0); err == nil {
		t.Fatal("zero renew duration must fail")
	}
	if err := open.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: " ", FenceToken: 0}); err == nil {
		t.Fatal("invalid release args must fail")
	}
	bad := Outcome{OutcomeID: "", RequestID: "", AccountID: ""}
	if _, err := open.AppendOutcome(ctx, lease, bad); err == nil {
		t.Fatal("incomplete outcome must fail")
	}
	if _, err := open.HasRequest(ctx, " "); err == nil {
		t.Fatal("blank request id must fail")
	}
	if _, _, err := open.LoadKeyCursor(ctx, " ", "", " "); err == nil {
		t.Fatal("blank cursor args must fail")
	}
	if err := open.SaveKeyCursor(ctx, lease, " ", "", " ", -1); err == nil {
		t.Fatal("invalid cursor args must fail")
	}
	// 拒绝写入的 outcome（状态契约）。
	contract := Outcome{OutcomeID: "w12d-c", RequestID: "w12d-c", AccountID: "w12d-c", Outcome: OutcomeSuccess, ObservedAt: time.Now(), InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, AccountStatus: "error"}
	if _, err := open.AppendOutcome(ctx, lease, contract); err == nil {
		t.Fatal("invalid state contract must fail")
	}
}

// TestW12dSQLiteDuplicateOutcomeRefresh 覆盖 SQLite duplicate outcome 幂等与
// 抑制窗口刷新臂（与 w9h 的 PG 臂对称）。
func TestW12dSQLiteDuplicateOutcomeRefresh(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-dup.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w12d-dup-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %t", err, acquired)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	observed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	outcome := Outcome{
		OutcomeID: "w12d-dup-1", RequestID: "w12d-dup-r", AccountID: "w12d-dup-acc",
		Outcome: OutcomeTaskFailed, ObservedAt: observed, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		ErrorCode: "direct_input_invalid", ErrorMessage: "隔离", NextDueAt: ptrTime(observed.Add(10 * time.Minute)),
	}
	inserted, err := store.AppendOutcome(ctx, lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("first append: inserted=%t err=%v", inserted, err)
	}
	// 同 request id 重复追加 → 幂等未插入且刷新抑制窗口。
	outcome.NextDueAt = ptrTime(observed.Add(20 * time.Minute))
	inserted, err = store.AppendOutcome(ctx, lease, outcome)
	if err != nil || inserted {
		t.Fatalf("duplicate append: inserted=%t err=%v", inserted, err)
	}
	suppressions, err := store.LoadDirectInputSuppressions(ctx, observed)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := false
	for _, suppression := range suppressions {
		if suppression.AccountID == "w12d-dup-acc" && suppression.InputVersion == 1 && suppression.NextDueAt.Equal(observed.Add(20*time.Minute)) {
			refreshed = true
		}
	}
	if !refreshed {
		t.Fatalf("suppression refresh missing: %+v", suppressions)
	}
	// 不匹配的 duplicate → 不刷新。
	outcome.AccountID = "w12d-dup-other"
	inserted, err = store.AppendOutcome(ctx, lease, outcome)
	if err != nil || inserted {
		t.Fatalf("mismatched duplicate: inserted=%t err=%v", inserted, err)
	}
}

// TestW12dClosedStoreNilSafety 覆盖 nil store Close 幂等。
func TestW12dClosedStoreNilSafety(t *testing.T) {
	var store *Store
	if err := store.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
}
