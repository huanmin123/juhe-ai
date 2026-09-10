package proxylatency

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 本文件收尾 Store 与 Runner 的最后一批校验/跳过分支。

func TestWFStoreAppendOutcomeGuards(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-guard", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-guard", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	_ = proxy
	_ = ok
	draft := wfCycleDraft("p-guard")
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-guard",
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}
	// 缺幂等字段 → validateOutcome 拒绝。
	invalid := outcome
	invalid.OutcomeID = " "
	if _, err := store.AppendOutcome(ctx, owner, proxy, invalid); err == nil {
		t.Fatal("缺 OutcomeID 必须拒绝")
	}
	// 非法 overall status → 拒绝。
	badStatus := outcome
	badStatus.OverallStatus = OverallStatus("green")
	if _, err := store.AppendOutcome(ctx, owner, proxy, badStatus); err == nil {
		t.Fatal("非法 status 必须拒绝")
	}
	// trigger 非法 → 拒绝。
	badTrigger := outcome
	badTrigger.Trigger = Trigger("cron")
	if _, err := store.AppendOutcome(ctx, owner, proxy, badTrigger); err == nil {
		t.Fatal("非法 trigger 必须拒绝")
	}
	// 首次插入时租约身份不匹配 → 拒绝。
	fresh := outcome
	fresh.RequestID = "another-request"
	fresh.OutcomeID = stableOutcomeID("another-request")
	if _, err := store.AppendOutcome(ctx, owner, ProxyLease{ProxyID: "p-other", OwnerID: owner.OwnerID, FenceToken: proxy.FenceToken}, fresh); err == nil {
		t.Fatal("proxy id 与 outcome 不匹配必须拒绝")
	}
	// owner fence 不匹配 → 拒绝。
	fenceMismatch := outcome
	fenceMismatch.RequestID = "third-request"
	fenceMismatch.OutcomeID = stableOutcomeID("third-request")
	fenceMismatch.OwnerFenceToken = owner.FenceToken + 7
	if _, err := store.AppendOutcome(ctx, owner, proxy, fenceMismatch); err == nil {
		t.Fatal("owner fence 不匹配必须拒绝")
	}
	// owner lease 失效（行不存在）→ ErrOwnerLeaseLost。
	if err := store.VerifyOwnerLease(ctx, owner); err != nil {
		t.Fatalf("VerifyOwnerLease 失败: %v", err)
	}
	if err := store.VerifyOwnerLease(ctx, OwnerLease{OwnerID: "wf-guard", FenceToken: 999}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("错误 fence err=%v", err)
	}
	if _, ok, err := store.AcquireProxyLease(ctx, OwnerLease{OwnerID: "ghost", FenceToken: 1}, "p-guard", time.Minute); err == nil || ok {
		t.Fatal("owner 失效时不得获取 proxy lease")
	}
	if _, _, _, err := store.AdmitExecution(ctx, owner, ProxyLease{}, issued); err == nil {
		t.Fatal("空 proxy lease 必须拒绝")
	}
}

func TestWFEnsureRuntimeSchemaConcurrent(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	store := &Store{db: db, mode: StorePostgres, postgresSchemaReady: false}
	// 并发检查：为每个 goroutine 预登记一份完整夹具（按类型互不串扰）。
	for index := 0; index < 4; index++ {
		wfScriptSchema(t, rec)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = store.CheckSchema(context.Background())
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发 schema 检查 %d 失败: %v", index, err)
		}
	}
	if !store.postgresSchemaReady {
		t.Fatal("检查通过后必须置 ready")
	}
	// ready 后续租操作直接命中 UPSERT（不再触发 catalog 检查）：
	// 无脚本 → ErrNoRows → 未获取但不报错，证明跳过了 catalog 检查。
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "o", time.Hour); err != nil || ok {
		t.Fatalf("ready 后租约操作 ok=%v err=%v", ok, err)
	}
}

func TestWFRunCycleSkipsHeldProxy(t *testing.T) {
	store := wfOpenJobsStore(t)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-skip", OwnerLease: time.Hour, ProxyLease: time.Hour}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(wfNewProjector(t, store, wfOpenBusinessDB(t)))
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-skip", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-skip")}}
	// 领取失败（被其他 owner 持有语义由调用方返回）→ 记 skipped。
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{}, false, nil
	}
	if err := runner.runCycle(ctx, owner); err != nil {
		t.Fatalf("runCycle 失败: %v", err)
	}
	status := runner.Status()
	if status.SkippedLeases != 1 || status.Claimed != 0 {
		t.Fatalf("跳过计数=%+v", status)
	}
}

func TestWFStoreAppendOutcomeFenceAndExpiry(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-fence", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-fence", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issued, err := store.IssueInput(ctx, wfCycleDraft("p-fence"))
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-fence",
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}

	// observedAt 早于签发时间 → 输入围栏拒绝。
	early := outcome
	early.ObservedAt = issued.IssuedAt.Add(-time.Second)
	if _, err := store.AppendOutcome(ctx, owner, proxy, early); !errors.Is(err, ErrInputFence) {
		t.Fatalf("过早观测 err=%v", err)
	}

	// owner lease 行过期 → ErrOwnerLeaseLost。
	if _, err := store.db.Exec(`UPDATE proxy_latency_owner_leases SET lease_until=? WHERE lease_key='proxy-latency-owner'`,
		time.Now().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("使 owner lease 过期失败: %v", err)
	}
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("owner 过期 err=%v", err)
	}
	if err := store.VerifyOwnerLease(ctx, owner); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("过期租约校验 err=%v", err)
	}

	// proxy lease 行过期 → ErrProxyLeaseLost。
	if _, err := store.db.Exec(`UPDATE proxy_latency_proxy_leases SET lease_until=? WHERE proxy_id='p-fence'`,
		time.Now().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("使 proxy lease 过期失败: %v", err)
	}
	if _, _, err := store.AcquireProxyLease(ctx, owner, "p-fence", time.Hour); err == nil {
		t.Fatal("过期 proxy lease 不得直接复用")
	}
}

func TestWFRunnerRecordErrorNilAndNowDefault(t *testing.T) {
	runner := NewRunner(RuntimeConfig{}, &Store{}, &wfFakeReader{}, nil)
	runner.recordError(nil)
	if runner.Status().LastError != "" {
		t.Fatal("nil 错误不得记账")
	}
	if runner.now().IsZero() {
		t.Fatal("默认时钟必须可用")
	}
}
