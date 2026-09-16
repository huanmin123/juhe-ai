// 波次 w12c：补齐速度优先探针与 listavailability 维护错误臂。全部进程内
// mock（可编程 claim store / candidate source / 注错 repo），不连接真实依赖。
package opsjobs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 速度优先探针
// ---------------------------------------------------------------------------

type w12cProbeStore struct {
	acquireNil bool
	renewSeq   []bool // 逐次返回；耗尽后重复最后一项
	renewIndex atomic.Int32
	gate       *atomic.Bool // 非 nil：探针启动后续租失败
	discardErr error
	deferErr   error
	successErr error
	recordErr  error
	discarded  int
	deferred   int
	successes  int
	failures   int
}

func (s *w12cProbeStore) AcquireClaim(context.Context, ProbeCandidate) (*ProbeClaim, error) {
	if s.acquireNil {
		return nil, nil
	}
	return &ProbeClaim{Token: "w12c-claim"}, nil
}

func (s *w12cProbeStore) RenewClaim(context.Context, ProbeClaim) (bool, error) {
	if s.gate != nil {
		// 心跳专项：探针启动后续租一律失败，验证 claim 失效短路。
		return !s.gate.Load(), nil
	}
	if len(s.renewSeq) == 0 {
		return false, nil
	}
	index := s.renewIndex.Add(1) - 1
	if int(index) >= len(s.renewSeq) {
		index = int32(len(s.renewSeq) - 1)
	}
	return s.renewSeq[index], nil
}

func (s *w12cProbeStore) ReleaseClaim(context.Context, ProbeClaim) error { return nil }

func (s *w12cProbeStore) Discard(context.Context, ProbeCandidate) error {
	s.discarded++
	return s.discardErr
}

func (s *w12cProbeStore) Defer(context.Context, ProbeCandidate) (bool, error) {
	s.deferred++
	if s.deferErr != nil {
		return false, s.deferErr
	}
	return true, nil
}

func (s *w12cProbeStore) RecordSuccess(context.Context, ProbeCandidate, ProbeAccountRef, *int64) (SpeedFirstRecoveryResult, error) {
	s.successes++
	if s.successErr != nil {
		return SpeedFirstRecoveryResult{}, s.successErr
	}
	return SpeedFirstRecoveryResult{Cleared: true}, nil
}

func (s *w12cProbeStore) RecordFailure(context.Context, ProbeCandidate, string) error {
	s.failures++
	return s.recordErr
}

type w12cProbeSource struct {
	account      *SpeedFirstAccountSummary
	accountErr   error
	candidate    *ProbeAccountRef
	candidateErr error
}

func (s *w12cProbeSource) FindAccountForTest(context.Context, string, string) (*SpeedFirstAccountSummary, error) {
	return s.account, s.accountErr
}

func (s *w12cProbeSource) FindCandidateAccount(context.Context, string, string, string) (*ProbeAccountRef, error) {
	return s.candidate, s.candidateErr
}

func w12cActiveAccount() *SpeedFirstAccountSummary {
	return &SpeedFirstAccountSummary{Status: "active", Schedulable: true}
}

func w12cCandidateRef() *ProbeAccountRef {
	return &ProbeAccountRef{AccountID: "acc-w12c", GroupID: "group-w12c"}
}

func w12cRunner(t *testing.T, store SpeedFirstClaimStore, source SpeedFirstCandidateSource, probe func(context.Context) (ProbeResultSnapshot, TransportProbeOutcome), renewInterval time.Duration) *SpeedFirstProbeRunner {
	t.Helper()
	runner, err := NewSpeedFirstProbeRunner(store, source, func(ctx context.Context, account *SpeedFirstAccountSummary, candidate ProbeCandidate, candidateAccount *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return probe(ctx)
	}, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1_000 }, ClaimRenewInterval: renewInterval})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func w12cFastProbe(context.Context) (ProbeResultSnapshot, TransportProbeOutcome) {
	firstByte := int64(100)
	return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, framingCompleteOutcome()
}

func TestW12CSpeedFirstProbeErrorArms(t *testing.T) {
	ctx := context.Background()

	// 账户读取错误。
	store := &w12cProbeStore{renewSeq: []bool{true}}
	runner := w12cRunner(t, store, &w12cProbeSource{accountErr: errors.New("w12c: account boom"), candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("账户读取错误必须暴露")
	}

	// 资格判定错误：accountExpiresAt 缺解析结果。
	store = &w12cProbeStore{renewSeq: []bool{true}}
	runner = w12cRunner(t, store, &w12cProbeSource{
		account:   &SpeedFirstAccountSummary{Status: "active", Schedulable: true, AccountExpiresAt: "w12c-invalid"},
		candidate: w12cCandidateRef(),
	}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("资格判定错误必须暴露")
	}

	// 候选凭据解析错误。
	store = &w12cProbeStore{renewSeq: []bool{true}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidateErr: errors.New("w12c: candidate boom")}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("候选解析错误必须暴露")
	}

	// 不合格账户 + 续租失败 → 放弃（255-257）。
	store = &w12cProbeStore{renewSeq: []bool{true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: &SpeedFirstAccountSummary{Status: "disabled", Schedulable: true}, candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	completed, err := runner.Run(ctx, testCandidate())
	if err != nil || !completed {
		t.Fatalf("续租失败应放弃: %v %v", completed, err)
	}
	if store.discarded != 0 {
		t.Fatalf("claim 失效不得 discard: %d", store.discarded)
	}

	// 不合格账户 + discard 错误。
	store = &w12cProbeStore{renewSeq: []bool{true}, discardErr: errors.New("w12c: discard boom")}
	runner = w12cRunner(t, store, &w12cProbeSource{account: &SpeedFirstAccountSummary{Status: "disabled", Schedulable: true}, candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("discard 错误必须暴露")
	}

	// 候选缺失 + 续租失败（269-271）。
	store = &w12cProbeStore{renewSeq: []bool{true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount()}, w12cFastProbe, 0)
	completed, err = runner.Run(ctx, testCandidate())
	if err != nil || !completed {
		t.Fatalf("候选缺失续租失败: %v %v", completed, err)
	}

	// 候选缺失 + discard 错误。
	store = &w12cProbeStore{renewSeq: []bool{true}, discardErr: errors.New("w12c: discard boom2")}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount()}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("候选缺失 discard 错误必须暴露")
	}

	// 探针前续租失败（278-280）。
	store = &w12cProbeStore{renewSeq: []bool{true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	completed, err = runner.Run(ctx, testCandidate())
	if err != nil || !completed {
		t.Fatalf("探针前续租失败: %v %v", completed, err)
	}
	if store.successes+store.failures != 0 {
		t.Fatal("claim 失效不得提交结果")
	}

	// 成功前续租失败（285-287）。
	store = &w12cProbeStore{renewSeq: []bool{true, true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	completed, err = runner.Run(ctx, testCandidate())
	if err != nil || !completed || store.successes != 0 {
		t.Fatalf("成功前续租失败: %v %v %d", completed, err, store.successes)
	}

	// RecordSuccess 错误。
	store = &w12cProbeStore{renewSeq: []bool{true, true, true}, successErr: errors.New("w12c: success boom")}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, w12cFastProbe, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("RecordSuccess 错误必须暴露")
	}

	// 中性结果 + 续租失败（296-298）。
	neutral := func(context.Context) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{Success: false}, transportIncompleteOutcome(ProbeFailureTimeout, nil)
	}
	store = &w12cProbeStore{renewSeq: []bool{true, true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, neutral, 0)
	completed, err = runner.Run(ctx, testCandidate())
	if err != nil || !completed || store.deferred != 0 {
		t.Fatalf("中性结果续租失败: %v %v %d", completed, err, store.deferred)
	}

	// Defer 错误。
	store = &w12cProbeStore{renewSeq: []bool{true, true, true}, deferErr: errors.New("w12c: defer boom")}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, neutral, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("Defer 错误必须暴露")
	}

	// 失败前续租失败（305-307）与 RecordFailure 错误。
	slow := func(context.Context) (ProbeResultSnapshot, TransportProbeOutcome) {
		firstByte := int64(99_999)
		return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, framingCompleteOutcome()
	}
	store = &w12cProbeStore{renewSeq: []bool{true, true, false}}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, slow, 0)
	completed, err = runner.Run(ctx, testCandidate())
	if err != nil || !completed || store.failures != 0 {
		t.Fatalf("失败前续租: %v %v %d", completed, err, store.failures)
	}
	store = &w12cProbeStore{renewSeq: []bool{true, true, true}, recordErr: errors.New("w12c: failure boom")}
	runner = w12cRunner(t, store, &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}, slow, 0)
	if _, err := runner.Run(ctx, testCandidate()); err == nil {
		t.Fatal("RecordFailure 错误必须暴露")
	}
}

func TestW12CSpeedFirstProbeHeartbeatClaimLost(t *testing.T) {
	// 心跳 goroutine：首次续租成功；第二次（心跳或主流程）失败 → claimLost；
	// 之后任何 ensureClaim 走短路分支（206-208）。
	probeGate := make(chan struct{})
	var probeStarted atomic.Bool
	store := &w12cProbeStore{gate: &probeStarted}
	source := &w12cProbeSource{account: w12cActiveAccount(), candidate: w12cCandidateRef()}
	runner, err := NewSpeedFirstProbeRunner(store, source, func(ctx context.Context, account *SpeedFirstAccountSummary, candidate ProbeCandidate, candidateAccount *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		probeStarted.Store(true)
		<-probeGate
		return w12cFastProbe(context.Background())
	}, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1_000 }, ClaimRenewInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	type runResult struct {
		completed bool
		err       error
	}
	done := make(chan runResult, 1)
	go func() {
		completed, err := runner.Run(context.Background(), testCandidate())
		done <- runResult{completed, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !probeStarted.Load() {
		time.Sleep(2 * time.Millisecond)
	}
	if !probeStarted.Load() {
		t.Fatal("探针应已启动")
	}
	time.Sleep(20 * time.Millisecond) // 等待心跳将 claim 置为失效。
	close(probeGate)
	select {
	case result := <-done:
		if result.err != nil || !result.completed {
			t.Fatalf("run=%v %v", result.completed, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 应在 claim 失效后返回")
	}
	if store.successes != 0 {
		t.Fatal("claim 失效不得提交结果")
	}
}

// ---------------------------------------------------------------------------
// listavailability 错误臂
// ---------------------------------------------------------------------------

func TestW12CListAvailabilityValidationAndDefaults(t *testing.T) {
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }
	base := &fakeListAvailabilityRepo{}

	// worker 并发默认值与非法值。
	defaults := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	defaults.WorkerConcurrency = 0
	if _, err := RunListAvailabilityMaintenance(context.Background(), defaults, true); err != nil {
		t.Fatal(err)
	}
	invalidConcurrency := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	invalidConcurrency.WorkerConcurrency = -1
	if _, err := RunListAvailabilityMaintenance(context.Background(), invalidConcurrency, true); err == nil {
		t.Fatal("负 worker 并发必须拒绝")
	}

	// batch 默认值与非法值。
	defaultBatch := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	defaultBatch.BatchSize = 0
	if _, err := RunListAvailabilityMaintenance(context.Background(), defaultBatch, true); err != nil {
		t.Fatal(err)
	}
	invalidBatch := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	invalidBatch.BatchSize = -5
	if _, err := RunListAvailabilityMaintenance(context.Background(), invalidBatch, true); err == nil {
		t.Fatal("负 batch 必须拒绝")
	}

	// MaxBatchesPerRun 默认值。
	defaultMaxBatches := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	defaultMaxBatches.MaxBatchesPerRun = 0
	if _, err := RunListAvailabilityMaintenance(context.Background(), defaultMaxBatches, true); err != nil {
		t.Fatal(err)
	}

	// lease 默认值。
	defaultLease := w7dListOptions(base, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	defaultLease.LeaseMS = 0
	if _, err := RunListAvailabilityMaintenance(context.Background(), defaultLease, true); err != nil {
		t.Fatal(err)
	}
}

func TestW12CListAvailabilityMaintenanceErrorArms(t *testing.T) {
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }
	availableProbe := w7dBrokenProbe{available: true, concurrency: true}

	// markUnavailable 错误（依赖不可用 + mark 失败 → 原始错误上抛）。
	markErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, markErr: errors.New("w12c: mark boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(markErrRepo, loader, nil, w7dBrokenProbe{available: false, concurrency: true}), true); err == nil {
		t.Fatal("mark 错误必须暴露")
	}

	// overlay 错误 + mark 错误 → join。
	overlayRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, markErr: errors.New("w12c: mark boom")}
	overlayBroken := &w7dBrokenOverlays{listErr: errors.New("w12c: overlay boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(overlayRepo, loader, overlayBroken, availableProbe), true); err == nil || !strings.Contains(err.Error(), "overlay boom") {
		t.Fatalf("overlay + mark 错误应 join: %v", err)
	}

	// recovery 开始错误。
	startErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, recoveryStartErr: errors.New("w12c: recovery start boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(startErrRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("recovery start 错误必须暴露")
	}

	// recovery 未开始 → touch 错误。
	touchErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, touchErr: errors.New("w12c: touch boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(touchErrRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("touch 错误必须暴露")
	}

	// recovery enqueue 错误。
	enqueueErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{recoveryStarted: true}, recoveryEnqueueErr: errors.New("w12c: enqueue boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(enqueueErrRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("enqueue 错误必须暴露")
	}

	// 删除 claim 应用错误。
	deletionRepo := &w7dFailingListRepo{
		inner:       &fakeListAvailabilityRepo{claims: []DirtyClaim{{AccountID: "ghost", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}}},
		deletionErr: errors.New("w12c: deletion boom"),
	}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(deletionRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("deletion 错误必须暴露")
	}

	// viewer 刷新错误。
	refreshRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{claims: []DirtyClaim{{AccountID: "acc", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}}}, refreshErr: errors.New("w12c: refresh boom")}
	refreshRepo.inner.scopes = map[string]ProjectionScope{}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(refreshRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("refresh 错误必须暴露")
	}

	// recovery 完成错误。
	completeErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{claims: []DirtyClaim{{AccountID: "acc", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}}}, recoveryCompleteErr: errors.New("w12c: complete boom")}
	completeErrRepo.inner.scopes = map[string]ProjectionScope{}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(completeErrRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("complete 错误必须暴露")
	}

	// LoadItems 错误：释放重放并标记依赖不可用，不中断维护。
	loadErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{claims: []DirtyClaim{{AccountID: "acc", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}}}}
	loadErrRepo.inner.scopes = map[string]ProjectionScope{"acc": {AccountID: "acc", ViewerSystemAccountID: "viewer"}}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(loadErrRepo, func(context.Context, string, []string) ([]ProjectionItem, error) {
		return nil, errors.New("w12c: load boom")
	}, nil, availableProbe), true); err != nil {
		t.Fatal(err)
	}
	if len(loadErrRepo.inner.released) != 1 || len(loadErrRepo.inner.markUnavailable) == 0 {
		t.Fatalf("load 错误应释放并标记: released=%d mark=%v", len(loadErrRepo.inner.released), loadErrRepo.inner.markUnavailable)
	}

	// LoadItems 错误 + 释放失败：join 错误被上层吸收并标记依赖不可用。
	releaseErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{claims: []DirtyClaim{{AccountID: "acc", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}}}, releaseErr: errors.New("w12c: release boom")}
	releaseErrRepo.inner.scopes = map[string]ProjectionScope{"acc": {AccountID: "acc", ViewerSystemAccountID: "viewer"}}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(releaseErrRepo, func(context.Context, string, []string) ([]ProjectionItem, error) {
		return nil, errors.New("w12c: load boom")
	}, nil, availableProbe), true); err != nil {
		t.Fatal(err)
	}
	if len(releaseErrRepo.inner.markUnavailable) == 0 {
		t.Fatalf("释放失败应标记依赖不可用: %v", releaseErrRepo.inner.markUnavailable)
	}

	// ctx 取消在 claim 处理循环中暴露。
	cancelledRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{
		claims: []DirtyClaim{{AccountID: "acc", ViewerSystemAccountID: "viewer", Generation: 1, ClaimToken: "t"}},
		scopes: map[string]ProjectionScope{"acc": {AccountID: "acc", ViewerSystemAccountID: "viewer"}},
	}}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunListAvailabilityMaintenance(cancelCtx, w7dListOptions(cancelledRepo, loader, nil, availableProbe), true); err == nil {
		t.Fatal("取消必须暴露")
	}
}

func TestW12CListAvailabilityOverlayAndNextTransitionArms(t *testing.T) {
	// reconcileRuntimeOverlays：Acknowledge 错误。
	reconcileAt := "2030-01-01T00:00:00Z"
	brokenAck := &w7dBrokenOverlays{entries: []OverlayEntry{{AccountID: "acc", NextReconcileAt: &reconcileAt}}, ackErr: errors.New("w12c: ack boom")}
	if _, err := reconcileRuntimeOverlays(context.Background(), brokenAck); err == nil {
		t.Fatal("acknowledge 错误必须暴露")
	}

	// NextTransitionAtRFC3339：空候选与无效时间戳跳过，命中未来最早值。
	got, ok := NextTransitionAtRFC3339([]string{"", "not-a-time", "1999-01-01T00:00:00Z", "2030-06-01T12:00:00Z", "2030-05-01T00:00:00Z"}, parse("2030-01-01T00:00:00Z"))
	if !ok || got != "2030-05-01T00:00:00Z" {
		t.Fatalf("next transition=%q ok=%v", got, ok)
	}
	if _, ok := NextTransitionAtRFC3339([]string{"", "1999-01-01T00:00:00Z"}, parse("2030-01-01T00:00:00Z")); ok {
		t.Fatal("无未来候选应 false")
	}
}
