package accountbalance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRunManualSkipsWhenOwnerLeaseHeld covers the gateway manual-bridge
// conflict path: while the periodic owner holds the single owner lease, a
// manual RunManual from another owner must resolve to Skipped (never execute)
// so the caller maps it to ErrAccountLeaseHeld (route 409 lease_busy).
func TestRunManualSkipsWhenOwnerLeaseHeld(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	secret := "manual-conflict-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	periodicOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "periodic-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("periodic owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), OwnerLeaseKeyPeriodic, periodicOwner) }()
	manualRunner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-owner", CredentialSecret: secret, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	input := Input{
		AccountID: "acct-manual", SystemAccountID: "sys-manual", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	report, err := manualRunner.RunManual(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Executed != 0 || report.Stale != 0 || len(report.Errors) != 0 {
		t.Fatalf("held owner lease must skip manual input: %#v", report)
	}
	// The caller-side mapping (Service.RunManual / the gateway adapter)
	// turns Skipped>0 || Executed==0 into ErrAccountLeaseHeld, which the
	// route renders as the 409 lease_busy branch.
}

// TestRunManualStaleSnapshotReportsStale covers the snapshot CAS path: a
// manual input older than the persisted snapshot must land in report.Stale so
// the caller maps it to ErrOutcomeStale (route 409 配置已变化).
func TestRunManualStaleSnapshotReportsStale(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	secret := "manual-stale-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "stale-owner", CredentialSecret: secret, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := Input{
		AccountID: "acct-stale", SystemAccountID: "sys-stale", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	// Seed a newer committed snapshot (input_version=2) through the same lease
	// machinery the runner uses.
	owner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "seeder", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("seeder owner lease: %v %t", err, acquired)
	}
	account, acquired, err := store.AcquireAccountLease(ctx, OwnerLeaseKeyPeriodic, owner, base.AccountID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("seeder account lease: %v %t", err, acquired)
	}
	newer := base
	newer.InputVersion = 2
	newer.Trigger = TriggerPeriodic
	newer.NextRefreshAt = cloneTime(&now)
	if _, err := store.AppendOutcome(ctx, OwnerLeaseKeyPeriodic, owner, account, Outcome{
		OutcomeID: "seed-outcome", RequestID: "seed-request", AccountID: base.AccountID,
		InputVersion: 2, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now,
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "9.99"}, NextRefreshAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseAccountLease(ctx, owner, account); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLeaseKeyPeriodic, owner); err != nil {
		t.Fatal(err)
	}
	// The stale manual input (input_version=1 < 2) must be rejected by the
	// snapshot CAS inside the outcome append.
	report, err := runner.RunManual(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stale != 1 || report.Executed != 0 || len(report.Errors) != 0 {
		t.Fatalf("stale manual input must report stale: %#v", report)
	}
}

// TestApplyManualQueryResultManualDiagnostics pins the non-persisted draft
// mapping: transient failures map to failed immediately, fresh keeps amounts
// and success stamps, unsupported-without-temporary stays unsupported.
func TestApplyManualQueryResultManualDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	fresh := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.25"}}, now)
	if fresh.Status != StatusFresh || fresh.LastSuccessAt == "" {
		t.Fatalf("fresh draft: %#v", fresh)
	}
	failed := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusPending, ErrorMessage: "余额上游返回 HTTP 502"}, Temporary: true, ErrorMessage: "余额上游返回 HTTP 502"}, now)
	if failed.Status != StatusFailed || failed.ErrorMessage != "余额上游返回 HTTP 502" || failed.RemainingUSD != "" {
		t.Fatalf("transient draft must map to failed: %#v", failed)
	}
	unsupported := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: "没有可用的内置余额适配器"}}, now)
	if unsupported.Status != StatusUnsupported {
		t.Fatalf("permanent unsupported draft stays unsupported: %#v", unsupported)
	}
}

// balanceStatelessJSONHTTP 是并发安全的固定响应 HTTPDoer（无内部状态，多
// goroutine 并发 RunManual 共用同一 runner 时无 data race）。
type balanceStatelessJSONHTTP struct{ body string }

func (c balanceStatelessJSONHTTP) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{Reader: strings.NewReader(c.body)}}, nil
}

// manualOwnerLeaseRows 统计指定 owner lease key 空间的租约行数（RunManual 结束
// 即释放租约，lease_until 过期但行保留，行数即“该 key 空间被领取过”的证据）。
func manualOwnerLeaseRows(t *testing.T, store *Store, leaseKey string) int {
	t.Helper()
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1`, leaseKey).Scan(&rows); err != nil {
		t.Fatalf("count owner lease rows %q: %v", leaseKey, err)
	}
	return rows
}

// TestManualOwnerLeaseKeyPerAccountParallel 锁定手动刷新互斥粒度下沉的核心
// 契约（设计 §5/§6，2026-11-01）：装配 ManualOwnerLeaseKeyPrefix 后，手动
// 刷新的 owner lease 按账户独立（prefix+accountID）——周期批次持有全局
// account-balance-owner 行（生产周期侧几乎连续持锁）不再阻塞任何账户的手动
// 刷新，不同账户并发手动刷新完全并行（各自 Acquire 各自的 key 行 + 账户
// 租约，互不阻挡）。
func TestManualOwnerLeaseKeyPerAccountParallel(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	// 模拟周期批次正在运行：全局 owner lease 行被周期执行端持有且不释放。
	periodicOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "periodic-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("periodic owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), OwnerLeaseKeyPeriodic, periodicOwner) }()

	secret := "w7c-balance-secret"
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-parallel-owner", CredentialSecret: secret, ManualOwnerLeaseKeyPrefix: ManualOwnerLeaseKeyPrefix, HTTPClient: balanceStatelessJSONHTTP{body: `{"unit":"USD","remaining":"12.5"}`}, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	runManual := func(accountID string) (RunReport, error) {
		input := kitValidBalanceInput(accountID)
		input.Trigger = TriggerManual
		return runner.RunManual(ctx, input)
	}
	type manualResult struct {
		report RunReport
		err    error
	}
	results := make(chan manualResult, 2)
	for _, accountID := range []string{"acct-mp-a", "acct-mp-b"} {
		go func(id string) {
			report, runErr := runManual(id)
			results <- manualResult{report: report, err: runErr}
		}(accountID)
	}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("parallel manual run: %v", result.err)
		}
		if result.report.Executed != 1 || result.report.Skipped != 0 || len(result.report.Errors) != 0 {
			t.Fatalf("different accounts must run in parallel, got %#v", result.report)
		}
	}
	// 每个账户各落下自己的 manual owner lease 行（释放后行保留）；全局周期行
	// 仍只有周期持有者那一行，manual 从未触碰它。
	if rows := manualOwnerLeaseRows(t, store, ManualOwnerLeaseKeyPrefix+"acct-mp-a"); rows != 1 {
		t.Fatalf("manual key rows for acct-mp-a = %d, want 1", rows)
	}
	if rows := manualOwnerLeaseRows(t, store, ManualOwnerLeaseKeyPrefix+"acct-mp-b"); rows != 1 {
		t.Fatalf("manual key rows for acct-mp-b = %d, want 1", rows)
	}
	var globalRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1`, OwnerLeaseKeyPeriodic).Scan(&globalRows); err != nil {
		t.Fatal(err)
	}
	if globalRows != 1 {
		t.Fatalf("global periodic key rows = %d, want 1 (manual must not touch it)", globalRows)
	}
}

// manualGateHTTP 让第一个进入上游查询的请求挂起，直到测试释放：用于给同账户
// 并发手动刷新构造确定性重叠窗口（第一个 manual 持有 owner lease + 账户租约
// 期间，第二个同步发起）。
type manualGateHTTP struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *manualGateHTTP) Do(*http.Request) (*http.Response, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{Reader: strings.NewReader(`{"unit":"USD","remaining":"3.5"}`)}}, nil
}

// TestManualOwnerLeaseKeySameAccountMutex 锁定同账户并发手动刷新互斥：同一
// manual key 行（prefix+accountID）同一时刻只被一个执行端持有，重叠窗口内
// 后到者 Skipped（调用方映射 409 lease_busy），先到者正常 executed。用挂起
// 上游查询的 gate client 构造确定性重叠，不依赖调度时序。
func TestManualOwnerLeaseKeySameAccountMutex(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	secret := "w7c-balance-secret"
	gate := &manualGateHTTP{entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-mutex-owner", CredentialSecret: secret, ManualOwnerLeaseKeyPrefix: ManualOwnerLeaseKeyPrefix, HTTPClient: gate, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	input := kitValidBalanceInput("acct-mm-same")
	input.Trigger = TriggerManual
	firstDone := make(chan RunReport, 1)
	go func() {
		report, runErr := runner.RunManual(ctx, input)
		if runErr != nil {
			t.Errorf("first manual run: %v", runErr)
		}
		firstDone <- report
	}()
	// 第一个 manual 已进入上游查询：此刻它持有 manual key 行与账户租约。
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("first manual run did not enter upstream query within 10s (gate.entered not closed)")
	}
	second, err := runner.RunManual(ctx, input)
	if err != nil {
		t.Fatalf("second manual run: %v", err)
	}
	close(gate.release)
	var first RunReport
	select {
	case first = <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("first manual run did not finish within 10s after release (no result on firstDone)")
	}
	if first.Executed != 1 || len(first.Errors) != 0 {
		t.Fatalf("first manual must execute, got %#v", first)
	}
	if second.Executed != 0 || second.Skipped != 1 || len(second.Errors) != 0 {
		t.Fatalf("overlapping same-account manual must skip, got %#v", second)
	}
}

// TestManualVsPeriodicSameAccountMutex 锁定跨 key 空间的同账户互斥：周期
// 持有某账户的账户级租约时，manual（独立 owner lease key 空间）刷新该账户
// 必须 Skipped，而其他账户的 manual 刷新不受周期批次影响照常 executed——
// 「409 只落在被占用账户」是本次下沉的行为目标。
func TestManualVsPeriodicSameAccountMutex(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	periodicOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "periodic-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("periodic owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), OwnerLeaseKeyPeriodic, periodicOwner) }()
	if _, acquired, err = store.AcquireAccountLease(ctx, OwnerLeaseKeyPeriodic, periodicOwner, "acct-mx-held", time.Minute); err != nil || !acquired {
		t.Fatalf("periodic account lease: %v %t", err, acquired)
	}

	secret := "w7c-balance-secret"
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-cross-owner", CredentialSecret: secret, ManualOwnerLeaseKeyPrefix: ManualOwnerLeaseKeyPrefix, HTTPClient: balanceStatelessJSONHTTP{body: `{"unit":"USD","remaining":"7"}`}, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	held := kitValidBalanceInput("acct-mx-held")
	held.Trigger = TriggerManual
	report, err := runner.RunManual(ctx, held)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 0 || report.Skipped != 1 || len(report.Errors) != 0 {
		t.Fatalf("manual on periodically-held account must skip, got %#v", report)
	}
	free := kitValidBalanceInput("acct-mx-free")
	free.Trigger = TriggerManual
	report, err = runner.RunManual(ctx, free)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || report.Skipped != 0 || len(report.Errors) != 0 {
		t.Fatalf("manual on another account must not be blocked by the periodic batch, got %#v", report)
	}
}

// TestAccountLeaseTakeoverAfterExpiryAcrossKeySpaces 锁定账户租约接管只看
// lease_until（设计 §5）：原持有者（周期 key 空间）的 owner lease 行仍存活、
// 但其账户租约 lease_until 已过期（模拟崩溃进程泄漏）时，另一 key 空间持有者
// 接管成功且 fence_token+1。接管时点恒为账户租约自身 lease_until；被删除的
// 「原持有者全局 owner 行失效即接管」分支按硬编码全局 key 判定 manual 持有者
// （其 owner 行在其他 key 空间、全局行中恒不存在），会在其探测中途放行抢锁，
// 不得回潮。旧持有者携旧 fence 写回在写入事务的账户租约校验处被拒
// （ErrAccountLeaseLost），fence 是唯一拦截机制。
func TestAccountLeaseTakeoverAfterExpiryAcrossKeySpaces(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	// 泄漏构造：周期 owner lease 行保持存活（不释放、TTL 1 分钟），账户租约
	// 用短 TTL（时间由 Go 侧参数驱动，SQL 无 clock_timestamp），过期后不释放。
	leakedOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "leaked-periodic-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("leaked periodic owner lease: %v %t", err, acquired)
	}
	oldAccount, acquired, err := store.AcquireAccountLease(ctx, OwnerLeaseKeyPeriodic, leakedOwner, "acct-takeover", 80*time.Millisecond)
	if err != nil || !acquired {
		t.Fatalf("leaked account lease: %v %t", err, acquired)
	}
	time.Sleep(250 * time.Millisecond)

	manualKey := ManualOwnerLeaseKeyPrefix + "acct-takeover"
	manualOwner, acquired, err := store.AcquireOwnerLease(ctx, manualKey, "manual-takeover-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("manual owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), manualKey, manualOwner) }()
	taken, acquired, err := store.AcquireAccountLease(ctx, manualKey, manualOwner, "acct-takeover", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("expired account lease must be taken over across key spaces: %v %t", err, acquired)
	}
	if taken.FenceToken <= oldAccount.FenceToken {
		t.Fatalf("takeover must bump fence token: old=%d new=%d", oldAccount.FenceToken, taken.FenceToken)
	}

	// 旧持有者携旧 fence 写回：owner lease 校验通过（周期行仍存活），但账户
	// 租约行已被接管（owner/fence 不匹配）→ ErrAccountLeaseLost，outcome 不落。
	now := time.Now().UTC()
	staleOutcome := Outcome{
		OutcomeID: "takeover-stale-outcome", RequestID: "takeover-stale-request", AccountID: oldAccount.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now,
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1"},
	}
	if _, err := store.AppendOutcome(ctx, OwnerLeaseKeyPeriodic, leakedOwner, oldAccount, staleOutcome); !errors.Is(err, ErrAccountLeaseLost) {
		t.Fatalf("old holder write-back must be fenced, got %v", err)
	}
	if _, found, err := store.LoadOutcome(ctx, staleOutcome.OutcomeID); err != nil || found {
		t.Fatalf("fenced outcome must not persist, found=%t err=%v", found, err)
	}
	// 接管者的正常写回不受影响。
	accepted, err := store.AppendOutcome(ctx, manualKey, manualOwner, taken, Outcome{
		OutcomeID: "takeover-fresh-outcome", RequestID: "takeover-fresh-request", AccountID: taken.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerManual, ObservedAt: now,
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "2"},
	})
	if err != nil || !accepted {
		t.Fatalf("takeover holder write-back: accepted=%t err=%v", accepted, err)
	}
}

// TestAccountLeaseHeldWhenHolderOwnerLeaseLostOrExpired 反向钉死被删除的
// 「原持有者 owner lease 失效即接管」分支不得以任何等价形式回潮（设计 §5）：
// 账户租约 lease_until 存活时，即便原持有者的 owner lease 行已物理不存在或
// 已过期，另一 key 空间持有者 AcquireAccountLease 也必须返回
// ErrAccountLeaseHeld——不抢「owner 行失效但账户租约存活」的合法持有者。
// 变体①（owner 行物理不存在）是被删 NOT EXISTS 分支的必误判点：该分支在
// 此场景恒真，回潮即接管成功；变体②（owner 行存在但已过期）等价覆盖 Release
// 后的行形态。
func TestAccountLeaseHeldWhenHolderOwnerLeaseLostOrExpired(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()

	// 变体①：持有者 owner 行物理不存在。
	survivorOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "survivor-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("survivor owner lease: %v %t", err, acquired)
	}
	if _, acquired, err = store.AcquireAccountLease(ctx, OwnerLeaseKeyPeriodic, survivorOwner, "acct-h-1", time.Minute); err != nil || !acquired {
		t.Fatalf("survivor account lease: %v %t", err, acquired)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1`, OwnerLeaseKeyPeriodic); err != nil {
		t.Fatalf("delete holder owner row: %v", err)
	}
	raiderKey := ManualOwnerLeaseKeyPrefix + "acct-h-1"
	raiderOwner, acquired, err := store.AcquireOwnerLease(ctx, raiderKey, "raider-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("raider owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), raiderKey, raiderOwner) }()
	if _, acquired, err = store.AcquireAccountLease(ctx, raiderKey, raiderOwner, "acct-h-1", time.Minute); !errors.Is(err, ErrAccountLeaseHeld) || acquired {
		t.Fatalf("live account lease must stay held when holder owner row is missing: acquired=%t err=%v", acquired, err)
	}

	// 变体②：持有者 owner 行存在但已过期（Release 把 lease_until 置为 now）。
	rebuiltOwner, acquired, err := store.AcquireOwnerLease(ctx, OwnerLeaseKeyPeriodic, "survivor-owner-2", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("rebuilt owner lease: %v %t", err, acquired)
	}
	if _, acquired, err = store.AcquireAccountLease(ctx, OwnerLeaseKeyPeriodic, rebuiltOwner, "acct-h-2", time.Minute); err != nil || !acquired {
		t.Fatalf("survivor account lease 2: %v %t", err, acquired)
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLeaseKeyPeriodic, rebuiltOwner); err != nil {
		t.Fatal(err)
	}
	raiderKey2 := ManualOwnerLeaseKeyPrefix + "acct-h-2"
	raiderOwner2, acquired, err := store.AcquireOwnerLease(ctx, raiderKey2, "raider-owner-2", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("raider owner lease 2: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), raiderKey2, raiderOwner2) }()
	if _, acquired, err = store.AcquireAccountLease(ctx, raiderKey2, raiderOwner2, "acct-h-2", time.Minute); !errors.Is(err, ErrAccountLeaseHeld) || acquired {
		t.Fatalf("live account lease must stay held when holder owner row is expired: acquired=%t err=%v", acquired, err)
	}
}

// panicHTTPDoer 是注入 RunnerConfig.HTTPClient 的 panic 桩：Do 一被调用即
// panic，模拟 ioWorker 上游探测路径（prepareInput→ExecuteAccountBalanceQuery
// →balanceHTTPClient 返回注入 Client）的内部 panic。
type panicHTTPDoer struct{}

func (panicHTTPDoer) Do(*http.Request) (*http.Response, error) {
	panic("balance probe panic injected by test")
}

// TestRunManualRecordsErrorWhenProbePanics 锁定 ioWorker panic 行为契约：
// 上游探测 panic 被 safego.Handle 吸收后必须落 report.Errors（原样透传
// panic 值）且不得计为 Skipped——否则 gateway RefreshManual 在 Executed==0
// 且 Errors 空时把内部 panic 误判为 lease_busy（409「余额查询正在进行」
// 误报）。Errors 非空使 RefreshManual 命中 Errors→err 分支返回错误（路由层
// 500），panic 期间的账户租约留在 prepareInput 内部，由 30s TTL 兜底过期。
func TestRunManualRecordsErrorWhenProbePanics(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	// secret 必须与 kitValidBalanceInput 的 envelope 加密 secret 一致，保证
	// 解封成功、执行真正到达注入 Client 的 Do（否则测试测到的是解封错误路径）。
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-panic-owner", CredentialSecret: "w7c-balance-secret", ManualOwnerLeaseKeyPrefix: ManualOwnerLeaseKeyPrefix, HTTPClient: panicHTTPDoer{}, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	input := kitValidBalanceInput("acct-panic-io")
	input.Trigger = TriggerManual
	report, err := runner.RunManual(ctx, input)
	if err != nil {
		t.Fatalf("panic must be absorbed into report.Errors, not returned as run error: %v", err)
	}
	itemErr, ok := report.Errors["acct-panic-io"]
	if !ok {
		t.Fatalf("panicked account must appear in report.Errors: %#v", report)
	}
	if !strings.Contains(itemErr.Error(), "panic") || !strings.Contains(itemErr.Error(), "balance probe panic injected by test") {
		t.Fatalf("error must carry the recovered panic value, got %q", itemErr.Error())
	}
	if report.Skipped != 0 || report.Executed != 0 {
		t.Fatalf("panicked input must be neither skipped nor executed (RefreshManual maps Skipped>0||Executed==0 to lease_busy): %#v", report)
	}
}
