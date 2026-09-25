package opsjobs

// w7d（opsjobs 纯函数与错误臂批次，w7d/TestW7D 前缀）：全部进程内确定性驱动，
// 不连接真实数据库/Redis：
//   1. probeoutcome 三态映射矩阵（此前 0%）。
//   2. circuitstore 纯函数错误臂（backoff seed/scopeKey/evidence/dueAt/
//      isFencingResult/parseSafePositiveInt64/hierarchyTransitionID/sortedStrings）。
//   3. balancedetect 依赖守卫、租约失败、围栏 stale 与恢复扫描错误臂。
//   4. speedfirstprobe 构造守卫与 Run 各失败/旁路分支。
//   5. taskrunreconcile 输入守卫与调度器回调。
//   6. jitter/helpers 兜底分支。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// probeoutcome：TransportProbeOutcomeFromResult 三态矩阵
// ---------------------------------------------------------------------------

func TestW7DTransportProbeOutcomeMatrix(t *testing.T) {
	status200 := 200
	status529 := 529
	status401 := 401
	status403 := 403
	status404 := 404
	status429 := 429
	status500 := 500
	var nilStatus *int
	canceled := true

	cases := []struct {
		name       string
		result     ProbeResultSnapshot
		attempt    *UpstreamAttemptSnapshot
		canceled   bool
		timedOut   bool
		exhausted  *bool
		wantKind   ProbeOutcomeKind
		wantStatus *int
		wantFail   ProbeFailureKind
	}{
		{
			name:     "canceled 优先",
			attempt:  &UpstreamAttemptSnapshot{Status: &status200, IsReal: true, IsCompletedReal: true},
			canceled: canceled,
			wantKind: ProbeOutcomeUnknown, wantFail: ProbeFailureCanceled,
		},
		{
			name:     "非真实上游尝试不采信",
			attempt:  &UpstreamAttemptSnapshot{TransportFailureKind: "timeout"},
			wantKind: ProbeOutcomeUnknown, wantFail: ProbeFailureTaskFailure,
		},
		{
			name:     "timeout 传输失败",
			attempt:  &UpstreamAttemptSnapshot{TransportFailureKind: "timeout", IsReal: true},
			wantKind: ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureTimeout,
		},
		{
			name:     "read_incomplete 传输失败",
			attempt:  &UpstreamAttemptSnapshot{TransportFailureKind: "read_incomplete", IsReal: true},
			wantKind: ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureRead,
		},
		{
			name:     "connection 传输失败带状态码",
			attempt:  &UpstreamAttemptSnapshot{Status: &status529, TransportFailureKind: "connection", IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureConnection, wantStatus: &status529,
		},
		{
			name:     "完成真实尝试进入 framing_complete",
			attempt:  &UpstreamAttemptSnapshot{Status: &status200, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeFramingComplete, wantStatus: &status200,
		},
		// ---- 缺陷 C（用户 2026-09-25 拍板）：探测 401/403 是凭据/授权失效，
		// 不得按 framing_complete 推进恢复；其余状态码维持 Node 判定。----
		{
			name:     "探测 401 判 credential_rejected 不推进恢复",
			attempt:  &UpstreamAttemptSnapshot{Status: &status401, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeCredentialRejected, wantStatus: &status401,
		},
		{
			name:     "探测 403 判 credential_rejected 不推进恢复",
			attempt:  &UpstreamAttemptSnapshot{Status: &status403, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeCredentialRejected, wantStatus: &status403,
		},
		{
			name:     "探测 429 说明服务活着维持 framing_complete",
			attempt:  &UpstreamAttemptSnapshot{Status: &status429, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeFramingComplete, wantStatus: &status429,
		},
		{
			name:     "探测 500 维持 framing_complete",
			attempt:  &UpstreamAttemptSnapshot{Status: &status500, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeFramingComplete, wantStatus: &status500,
		},
		{
			name:     "探测 404 是配置问题不误伤账号维持 framing_complete",
			attempt:  &UpstreamAttemptSnapshot{Status: &status404, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeFramingComplete, wantStatus: &status404,
		},
		{
			name:     "401 带本地传输失败仍归 transport_incomplete（优先级不变）",
			attempt:  &UpstreamAttemptSnapshot{Status: &status401, TransportFailureKind: "connection", IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureConnection, wantStatus: &status401,
		},
		{
			name:     "invalid_probe_output 语义失败",
			result:   ProbeResultSnapshot{ErrorCode: "invalid_probe_output"},
			attempt:  &UpstreamAttemptSnapshot{Status: &status200, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeFramingComplete, wantStatus: &status200,
		},
		{
			name:      "诊断未耗尽的 timedOut 归 task_failure",
			attempt:   &UpstreamAttemptSnapshot{IsReal: true},
			timedOut:  true,
			exhausted: boolPtr(false),
			wantKind:  ProbeOutcomeUnknown, wantFail: ProbeFailureTaskFailure,
		},
		{
			name:      "诊断耗尽的 timedOut 归 timeout",
			attempt:   &UpstreamAttemptSnapshot{IsReal: true},
			timedOut:  true,
			exhausted: boolPtr(true),
			wantKind:  ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureTimeout,
		},
		{
			name:      "真实尝试无失败分类无状态码归 connection",
			attempt:   &UpstreamAttemptSnapshot{IsReal: true},
			exhausted: boolPtr(false),
			wantKind:  ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureConnection,
		},
		{
			name:     "完成真实尝试但无状态码归 connection",
			attempt:  &UpstreamAttemptSnapshot{Status: nilStatus, IsReal: true, IsCompletedReal: true},
			wantKind: ProbeOutcomeTransportIncomplete, wantFail: ProbeFailureConnection,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := TransportProbeOutcomeFromResult(tc.result, tc.attempt, tc.canceled, tc.timedOut, tc.exhausted)
			if outcome.Kind != tc.wantKind || outcome.FailureKind != tc.wantFail {
				t.Fatalf("outcome = %#v, want kind=%s fail=%s", outcome, tc.wantKind, tc.wantFail)
			}
			if tc.wantStatus == nil {
				if outcome.StatusCode != nil {
					t.Fatalf("状态码必须缺省: %#v", outcome)
				}
			} else if outcome.StatusCode == nil || *outcome.StatusCode != *tc.wantStatus {
				t.Fatalf("状态码不正确: %#v", outcome.StatusCode)
			}
			if tc.result.ErrorCode == "invalid_probe_output" && outcome.Kind == ProbeOutcomeFramingComplete {
				if outcome.SemanticSuccess == nil || *outcome.SemanticSuccess {
					t.Fatalf("invalid_probe_output 必须置语义失败: %#v", outcome)
				}
			}
		})
	}
}

func boolPtr(value bool) *bool { return &value }

func TestW7DTransportProbeMeetsFirstByteTarget(t *testing.T) {
	firstByte := int64(120)
	result := ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}
	outcome := TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete}
	if !TransportProbeMeetsFirstByteTarget(result, outcome, 200) {
		t.Fatal("阈值内必须命中")
	}
	if TransportProbeMeetsFirstByteTarget(result, outcome, 100) {
		t.Fatal("超阈值不得命中")
	}
	if TransportProbeMeetsFirstByteTarget(ProbeResultSnapshot{Success: false, FirstTokenMS: &firstByte}, outcome, 200) {
		t.Fatal("失败结果不得命中")
	}
	if TransportProbeMeetsFirstByteTarget(result, TransportProbeOutcome{Kind: ProbeOutcomeTransportIncomplete}, 200) {
		t.Fatal("传输未完成不得命中")
	}
	if TransportProbeMeetsFirstByteTarget(ProbeResultSnapshot{Success: true}, outcome, 200) {
		t.Fatal("缺少首字时间不得命中")
	}
}

// ---------------------------------------------------------------------------
// circuitstore 纯函数
// ---------------------------------------------------------------------------

func TestW7DAccountCircuitBackoffDelayMS(t *testing.T) {
	// 低档（index<4）无抖动，按档位索引取 base；attempt<1 与超界收敛。
	lowAttempts := []struct {
		attempt int64
		base    int64
	}{
		{-5, CircuitBackoffMS[0]},
		{0, CircuitBackoffMS[0]},
		{1, CircuitBackoffMS[0]},
		{2, CircuitBackoffMS[1]},
		{3, CircuitBackoffMS[2]},
		{4, CircuitBackoffMS[3]},
	}
	for _, tc := range lowAttempts {
		if got := AccountCircuitBackoffDelayMS(tc.attempt, "", nil); got != tc.base {
			t.Fatalf("attempt %d 低档必须无抖动取 base: got %d want %d", tc.attempt, got, tc.base)
		}
	}
	// 高档确定性 seed：同 seed 同结果，且为 base±window 内非零偏移。
	base := CircuitBackoffMS[5]
	seedA := AccountCircuitBackoffDelayMS(6, "seed-a", nil)
	seedAAgain := AccountCircuitBackoffDelayMS(6, "seed-a", nil)
	if seedA != seedAAgain {
		t.Fatalf("同 seed 必须同结果: %d vs %d", seedA, seedAAgain)
	}
	window := PassiveScheduleJitterWindowMS(base)
	if seedA < base-window || seedA > base+window {
		t.Fatalf("seed 抖动越界: %d (window %d)", seedA, window)
	}
	// 随机源臂。
	if got := AccountCircuitBackoffDelayMS(6, "", func(int64) int64 { return 0 }); got < 1 {
		t.Fatalf("随机源结果必须 >= 1: %d", got)
	}
	if got := AccountCircuitBackoffDelayMS(99, "", nil); got < 1 {
		t.Fatalf("无 seed 无随机源时必须 base+1 >= 1: %d", got)
	}
}

func TestW7DAccountCircuitScopeKeyErrors(t *testing.T) {
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeAccount}); err == nil {
		t.Fatal("缺少 accountRuntimeKey 必须拒绝")
	}
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeKey, AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("key 作用域缺少 fingerprint 必须拒绝")
	}
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeProtocolModel, AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("protocol_model 缺少 profile 必须拒绝")
	}
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeProtocolModel, AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "video", ModelBucket: "b"}); err == nil {
		t.Fatal("非法 requestLane 必须拒绝")
	}
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeProtocolModel, AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "text"}); err == nil {
		t.Fatal("缺少 modelBucket 必须拒绝")
	}
	if _, err := AccountCircuitScopeKey(CircuitScope{Kind: "weird", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("未知 kind 必须拒绝")
	}
	// trim 语义：空白折叠后合法。
	if key, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeAccount, AccountRuntimeKey: "  acc  "}); err != nil || !strings.Contains(key, "acc") {
		t.Fatalf("空白必须 trim: %q %v", key, err)
	}
}

func TestW7DCircuitEvidenceHelpers(t *testing.T) {
	valid := strings.Repeat("ab", 32)
	// NormalizeAccountCircuitFailureEvidenceKey。
	if got, err := NormalizeAccountCircuitFailureEvidenceKey(valid, ""); err != nil || got != valid {
		t.Fatalf("合法 key 原样通过: %q %v", got, err)
	}
	if got, err := NormalizeAccountCircuitFailureEvidenceKey(strings.ToUpper(valid), ""); err != nil || got != valid {
		t.Fatalf("大写必须折叠: %q %v", got, err)
	}
	if _, err := NormalizeAccountCircuitFailureEvidenceKey("short", ""); err == nil {
		t.Fatal("缺 fallbackSeed 必须拒绝")
	}
	digested, err := NormalizeAccountCircuitFailureEvidenceKey("short", "seed")
	if err != nil || len(digested) != 64 {
		t.Fatalf("fallbackSeed 必须派生 sha256: %q %v", digested, err)
	}

	// AccountCircuitFailureEvidenceKeys：非法剔除/去重/保留末尾 required+1。
	var required *int
	two := 2
	cdKey := strings.Repeat("cd", 32)
	abKey := strings.Repeat("ab", 32)
	efKey := strings.Repeat("ef", 32)
	feKey := strings.Repeat("fe", 32)
	state := CircuitState{FailureEvidenceKeys: []string{"BAD", valid, valid, valid + "cd", efKey, cdKey, abKey, feKey}}
	keys, err := AccountCircuitFailureEvidenceKeys(state)
	if err != nil {
		t.Fatal(err)
	}
	// valid 与 abKey 同值：去重后序列为 [valid, efKey, cdKey, feKey]。
	// required=nil 回落 legacy=1 → keep=2：保留末尾 2 个合法去重键。
	if len(keys) != 2 || keys[0] != cdKey || keys[1] != feKey {
		t.Fatalf("legacy required=1 时必须保留末尾 2 个: %v", keys)
	}
	_ = required
	state.ConfirmationFailuresRequired = &two
	keys, err = AccountCircuitFailureEvidenceKeys(state)
	if err != nil || len(keys) != 3 || keys[0] != efKey || keys[2] != feKey {
		t.Fatalf("required=2 时必须保留末尾 3 个: %v %v", keys, err)
	}
	bogus := 9
	if _, err := AccountCircuitFailureEvidenceKeys(CircuitState{ConfirmationFailuresRequired: &bogus}); err == nil {
		t.Fatal("非法 required 必须拒绝")
	}

	// AccountCircuitConfirmationFailureCount。
	if count, err := AccountCircuitConfirmationFailureCount(CircuitState{}); err != nil || count != 0 {
		t.Fatalf("nil 计数回落 0: %d %v", count, err)
	}
	negative := -1
	if _, err := AccountCircuitConfirmationFailureCount(CircuitState{ConfirmationFailureCount: &negative}); err == nil {
		t.Fatal("负计数必须拒绝")
	}
	over := CircuitConfirmationFailuresRequiredMax + 1
	if _, err := AccountCircuitConfirmationFailureCount(CircuitState{ConfirmationFailureCount: &over}); err == nil {
		t.Fatal("超上限计数必须拒绝")
	}
}

func TestW7DCircuitDueAtAndFencing(t *testing.T) {
	retry := int64(555)
	leaseUntil := int64(777)
	maxInt64 := int64(^uint64(0) >> 1)
	if got := circuitDueAtMS(CircuitState{Phase: CircuitPhaseClosed}); got != maxInt64 {
		t.Fatalf("CLOSED 永不到期: %d", got)
	}
	if got := circuitDueAtMS(CircuitState{Phase: CircuitPhaseSuspect, Lease: &CircuitLease{LeaseUntilMS: leaseUntil}}); got != leaseUntil {
		t.Fatalf("有租约按租约到期: %d", got)
	}
	if got := circuitDueAtMS(CircuitState{Phase: CircuitPhaseOpen, RetryAtMS: &retry}); got != retry {
		t.Fatalf("OPEN 按 retryAt 到期: %d", got)
	}
	if got := circuitDueAtMS(CircuitState{Phase: CircuitPhaseRecovering}); got != maxInt64 {
		t.Fatalf("RECOVERING 无 retryAt 永不到期: %d", got)
	}
	if got := circuitDueAtMS(CircuitState{Phase: CircuitPhaseHalfOpen}); got != maxInt64 {
		t.Fatalf("HALF_OPEN 无租约永不到期: %d", got)
	}

	if !isFencingResult(CircuitMutationResult{Status: CircuitMutationStaleGeneration}) ||
		!isFencingResult(CircuitMutationResult{Status: CircuitMutationStaleDispatchRevision}) ||
		!isFencingResult(CircuitMutationResult{Status: CircuitMutationLeaseMismatch}) {
		t.Fatal("三类围栏结果必须识别")
	}
	if isFencingResult(CircuitMutationResult{Status: CircuitMutationApplied}) {
		t.Fatal("applied 不是围栏")
	}
	if !isAppliedOrIdempotent(CircuitMutationResult{Status: CircuitMutationIdempotent}) {
		t.Fatal("idempotent 必须按成功处理")
	}

	if isOlderNumericDispatchRevision("5", "7") != true {
		t.Fatal("5 < 7 必须判定为旧 revision")
	}
	if isOlderNumericDispatchRevision("7", "5") || isOlderNumericDispatchRevision("abc", "7") || isOlderNumericDispatchRevision("7", "abc") {
		t.Fatal("非旧/不可解析组合必须为 false")
	}
	if _, ok := parseSafePositiveInt64(" 12 "); !ok {
		t.Fatal("带空白的正整数必须可解析")
	}
	if _, ok := parseSafePositiveInt64(""); ok {
		t.Fatal("空串必须不可解析")
	}
	if _, ok := parseSafePositiveInt64("0"); ok {
		t.Fatal("0 必须不可解析")
	}
	if _, ok := parseSafePositiveInt64("-3"); ok {
		t.Fatal("负数必须不可解析")
	}
}

func TestW7DHierarchyTransitionIDAndSortedStrings(t *testing.T) {
	first := hierarchyTransitionID("shadow", "p", "i", "child", 3)
	second := hierarchyTransitionID("unshadow", "p", "i", "child", 3)
	if first == second || !strings.HasPrefix(first, "hierarchy:shadow:") {
		t.Fatalf("transition id 必须区分 action: %s vs %s", first, second)
	}
	same := hierarchyTransitionID("shadow", "p", "i", "child", 3)
	if same != first {
		t.Fatal("同参同结果")
	}
	sorted := sortedStrings([]string{"b", "a", "c"})
	if sorted[0] != "a" || sorted[2] != "c" {
		t.Fatalf("排序不正确: %v", sorted)
	}
	if got := sortedStrings(nil); got != nil {
		t.Fatalf("nil 输入保持 nil: %v", got)
	}
}

func TestW7DJitterAndHelperFloors(t *testing.T) {
	if max64(1, 2) != 2 || max64(2, 1) != 2 || max64(-3, -5) != -3 {
		t.Fatal("max64 结果不正确")
	}
	if min64(1, 2) != 1 || min64(2, 1) != 1 {
		t.Fatal("min64 结果不正确")
	}
	if jitterRandom(nil) == nil {
		t.Fatal("nil 随机源必须回落确定性实现")
	}
	called := false
	random := jitterRandom(func() float64 { called = true; return 0.5 })
	random()
	if !called {
		t.Fatal("非 nil 随机源必须原样使用")
	}
}

// ---------------------------------------------------------------------------
// taskrunreconcile
// ---------------------------------------------------------------------------

type w7dTaskRunRepo struct {
	calledWith TaskRunReconcileInput
	result     TaskRunReconcileResult
	err        error
}

func (r *w7dTaskRunRepo) ReconcileStale(_ context.Context, input TaskRunReconcileInput) (TaskRunReconcileResult, error) {
	r.calledWith = input
	return r.result, r.err
}

func TestW7DRunTaskRunReconcileGuards(t *testing.T) {
	if _, err := RunTaskRunReconcile(context.Background(), nil, 1_000, 10); err == nil {
		t.Fatal("nil repo 必须拒绝")
	}
	repo := &w7dTaskRunRepo{result: TaskRunReconcileResult{FailedQueuedCount: 2, DeletedExpiredLeaseCount: 1}}
	result, err := RunTaskRunReconcile(context.Background(), repo, 1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReconciledCount() != 2 {
		t.Fatalf("ReconciledCount 只计修复数: %#v", result)
	}
	if repo.calledWith.Limit != TaskRunReconcileBatchSize {
		t.Fatalf("batchSize<1 必须回落默认: %d", repo.calledWith.Limit)
	}
	if repo.calledWith.Now == "" || repo.calledWith.QueuedBefore == "" || repo.calledWith.RunningHeartbeatBefore == "" {
		t.Fatalf("输入时间戳必须齐全: %#v", repo.calledWith)
	}
	queued, err := time.Parse(time.RFC3339Nano, repo.calledWith.QueuedBefore)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339Nano, repo.calledWith.Now)
	if now.Sub(queued) != TaskRunStaleAfterMS*time.Millisecond {
		t.Fatalf("stale 窗口不正确: %s", now.Sub(queued))
	}
	if _, err := RunTaskRunReconcile(context.Background(), &w7dTaskRunRepo{err: errors.New("w7d: repo boom")}, 1, 5); err == nil {
		t.Fatal("repo 错误必须暴露")
	}
}

func TestW7DTaskRunSchedulerGuardsAndCallback(t *testing.T) {
	if _, err := NewTaskRunReconcileScheduler(nil, 10, func() int64 { return 1 }, nil); err == nil {
		t.Fatal("nil repo 必须拒绝")
	}
	if _, err := NewTaskRunReconcileScheduler(&w7dTaskRunRepo{}, 10, nil, nil); err == nil {
		t.Fatal("nil 时钟必须拒绝")
	}
	scheduler, err := NewTaskRunReconcileScheduler(&w7dTaskRunRepo{}, 0, func() int64 { return 1 }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.batchSize != TaskRunReconcileBatchSize {
		t.Fatalf("batchSize<1 回落默认: %d", scheduler.batchSize)
	}
	fired := false
	scheduler, err = NewTaskRunReconcileScheduler(&w7dTaskRunRepo{result: TaskRunReconcileResult{FailedRunningCount: 1}}, 5, func() int64 { return 7 }, func(result TaskRunReconcileResult) {
		fired = true
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("成功后必须触发回调")
	}
	fired = false
	failing := &w7dTaskRunRepo{err: errors.New("w7d: reconcile boom")}
	scheduler, _ = NewTaskRunReconcileScheduler(failing, 5, func() int64 { return 7 }, func(TaskRunReconcileResult) { fired = true })
	if _, err := scheduler.RunOnce(context.Background()); err == nil {
		t.Fatal("repo 错误必须暴露")
	}
	if fired {
		t.Fatal("失败不得触发回调")
	}
}

// ---------------------------------------------------------------------------
// balancedetect 错误臂
// ---------------------------------------------------------------------------

func TestW7DBalanceAutoDetectDependencyGuards(t *testing.T) {
	detector := &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}}}
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"), BalanceAutoDetectDependencies{}); err == nil {
		t.Fatal("空依赖必须拒绝")
	}
	repo := newFakeBalanceRepo()
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"),
		BalanceAutoDetectDependencies{Repo: repo, Detector: detector}); err == nil {
		t.Fatal("缺 lease 必须拒绝")
	}
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"),
		BalanceAutoDetectDependencies{Repo: repo, Lease: &fakeBalanceLease{acquired: true}, Detector: detector}); err == nil {
		t.Fatal("缺时钟必须拒绝")
	}
	if _, err := RunBalanceAutoDetectionRecovery(context.Background(), BalanceAutoDetectDependencies{}); err == nil {
		t.Fatal("恢复扫描空依赖必须拒绝")
	}
	if _, err := RunBalanceAutoDetectionRecovery(context.Background(),
		BalanceAutoDetectDependencies{Lease: &fakeBalanceLease{acquired: true}, Detector: detector, NowMS: balanceNowMS}); err == nil {
		t.Fatal("恢复扫描缺 repo 必须拒绝")
	}
}

func TestW7DBalanceAutoDetectLeaseAndStaleArms(t *testing.T) {
	detector := &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}}}
	// 租约被其他节点占用 → lease_busy。
	busy, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"),
		BalanceAutoDetectDependencies{Repo: newFakeBalanceRepo(), Lease: &fakeBalanceLease{acquired: false}, Detector: detector, NowMS: balanceNowMS})
	if err != nil || busy != BalanceOutcomeLeaseBusy {
		t.Fatalf("lease_busy: %s %v", busy, err)
	}
	// 租约回调内的探测错误按设计归类为 retry（意图顺延），不作为错误暴露。
	boom := errors.New("w7d: detector boom")
	failingDetector := &fakeBalanceDetector{err: boom}
	retryOutcome, retryErr := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"),
		BalanceAutoDetectDependencies{Repo: newFakeBalanceRepo(), Lease: &fakeBalanceLease{acquired: true}, Detector: failingDetector, NowMS: balanceNowMS})
	if retryErr != nil || retryOutcome != BalanceOutcomeRetry {
		t.Fatalf("探测错误归类 retry: %s %v", retryOutcome, retryErr)
	}
	// 围栏 stale：首次探测（NextRefreshAt=nil）unsupported 直接收口 → unsupported。
	outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("a"),
		BalanceAutoDetectDependencies{Repo: newFakeBalanceRepo(), Lease: &fakeBalanceLease{acquired: true},
			Detector: &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}}}, NowMS: balanceNowMS})
	if err != nil || outcome != BalanceOutcomeUnsupported {
		t.Fatalf("首次 unsupported 收口: %s %v", outcome, err)
	}
	// 已有意图但围栏失败（commit=false）→ stale。
	staleCandidate := detectionCandidate("stale")
	repo := newFakeBalanceRepo()
	repo.commitResults["stale"] = false
	outcome, err = AutoDetectAccountBalanceCandidate(context.Background(), staleCandidate,
		BalanceAutoDetectDependencies{Repo: repo, Lease: &fakeBalanceLease{acquired: true},
			Detector: &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}}}, NowMS: balanceNowMS})
	if err != nil || outcome != BalanceOutcomeStale {
		t.Fatalf("围栏失败必须 stale: %s %v", outcome, err)
	}
	// repo CommitDetectionDue 错误暴露。
	repoErr := errors.New("w7d: commit boom")
	failingRepo := &w7dFailingBalanceRepo{inner: newFakeBalanceRepo(), commitErr: repoErr}
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), staleCandidate,
		BalanceAutoDetectDependencies{Repo: failingRepo, Lease: &fakeBalanceLease{acquired: true},
			Detector: &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}}}, NowMS: balanceNowMS}); !errors.Is(err, repoErr) {
		t.Fatalf("commit 错误必须暴露: %v", err)
	}
}

func TestW7DBalanceAutoDetectEnabledFenceArms(t *testing.T) {
	detector := &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Adapter: "adapter-a", Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}}}
	// enable=false → stale。
	repo := newFakeBalanceRepo()
	repo.enableResults["fence"] = false
	outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("fence"),
		BalanceAutoDetectDependencies{Repo: repo, Lease: &fakeBalanceLease{acquired: true}, Detector: detector, NowMS: balanceNowMS})
	if err != nil || outcome != BalanceOutcomeStale {
		t.Fatalf("enable 围栏失败必须 stale: %s %v", outcome, err)
	}
	// snapshot=false → stale。
	repo2 := newFakeBalanceRepo()
	repo2.snapshotResults["snap"] = false
	outcome, err = AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("snap"),
		BalanceAutoDetectDependencies{Repo: repo2, Lease: &fakeBalanceLease{acquired: true}, Detector: detector, NowMS: balanceNowMS})
	if err != nil || outcome != BalanceOutcomeStale {
		t.Fatalf("snapshot 围栏失败必须 stale: %s %v", outcome, err)
	}
	// enable 错误 / snapshot 错误暴露。
	enableBoom := errors.New("w7d: enable boom")
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("e"),
		BalanceAutoDetectDependencies{Repo: &w7dFailingBalanceRepo{inner: newFakeBalanceRepo(), enableErr: enableBoom},
			Lease: &fakeBalanceLease{acquired: true}, Detector: detector, NowMS: balanceNowMS}); !errors.Is(err, enableBoom) {
		t.Fatalf("enable 错误必须暴露: %v", err)
	}
	snapBoom := errors.New("w7d: snapshot boom")
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("s"),
		BalanceAutoDetectDependencies{Repo: &w7dFailingBalanceRepo{inner: newFakeBalanceRepo(), snapshotErr: snapBoom},
			Lease: &fakeBalanceLease{acquired: true}, Detector: detector, NowMS: balanceNowMS}); !errors.Is(err, snapBoom) {
		t.Fatalf("snapshot 错误必须暴露: %v", err)
	}
}

func TestW7DBalanceRecoverySummaryArms(t *testing.T) {
	// ListDueCandidates 错误。
	if _, err := RunBalanceAutoDetectionRecovery(context.Background(),
		BalanceAutoDetectDependencies{Repo: &w7dFailingBalanceRepo{inner: newFakeBalanceRepo(), listErr: errors.New("w7d: list boom")},
			Lease: &fakeBalanceLease{acquired: true}, Detector: &fakeBalanceDetector{}, NowMS: balanceNowMS}); err == nil {
		t.Fatal("候选读取错误必须暴露")
	}

	// 取消的 ctx → DeferredCount，partial。
	repo := newFakeBalanceRepo()
	repo.candidates = []BalanceDetectionCandidate{detectionCandidate("a")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := RunBalanceAutoDetectionRecovery(canceled,
		BalanceAutoDetectDependencies{Repo: repo, Lease: &fakeBalanceLease{acquired: true}, Detector: &fakeBalanceDetector{}, NowMS: balanceNowMS})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != "partial" || summary.DeferredCount != 1 {
		t.Fatalf("取消必须顺延为 partial: %#v", summary)
	}

	// 全 stale → partial；全 enabled → success。
	staleRepo := newFakeBalanceRepo()
	staleRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("stale")}
	staleRepo.commitResults["stale"] = false
	summary, err = RunBalanceAutoDetectionRecovery(context.Background(),
		BalanceAutoDetectDependencies{Repo: staleRepo, Lease: &fakeBalanceLease{acquired: true},
			Detector: &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}}}, NowMS: balanceNowMS})
	if err != nil || summary.Outcome != "partial" || summary.StaleCount != 1 {
		t.Fatalf("stale 汇总: %#v %v", summary, err)
	}
	okRepo := newFakeBalanceRepo()
	okRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("ok")}
	summary, err = RunBalanceAutoDetectionRecovery(context.Background(),
		BalanceAutoDetectDependencies{Repo: okRepo, Lease: &fakeBalanceLease{acquired: true},
			Detector: &fakeBalanceDetector{result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}}}, NowMS: balanceNowMS})
	if err != nil || summary.Outcome != "success" || summary.EnabledCount != 1 {
		t.Fatalf("enabled 汇总: %#v %v", summary, err)
	}
}

// w7dFailingBalanceRepo 按方法注入错误，其余委托给 inner。
type w7dFailingBalanceRepo struct {
	inner       *fakeBalanceRepo
	listErr     error
	commitErr   error
	enableErr   error
	snapshotErr error
}

func (r *w7dFailingBalanceRepo) ListDueCandidates(ctx context.Context, limit int) ([]BalanceDetectionCandidate, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.inner.ListDueCandidates(ctx, limit)
}
func (r *w7dFailingBalanceRepo) CommitDetectionDue(ctx context.Context, input BalanceCommitDueInput) (bool, error) {
	if r.commitErr != nil {
		return false, r.commitErr
	}
	return r.inner.CommitDetectionDue(ctx, input)
}
func (r *w7dFailingBalanceRepo) EnableDetectedQuery(ctx context.Context, input BalanceEnableInput) (bool, error) {
	if r.enableErr != nil {
		return false, r.enableErr
	}
	return r.inner.EnableDetectedQuery(ctx, input)
}
func (r *w7dFailingBalanceRepo) ReplaceSnapshotIfCurrent(ctx context.Context, input BalanceSnapshotInput) (bool, error) {
	if r.snapshotErr != nil {
		return false, r.snapshotErr
	}
	return r.inner.ReplaceSnapshotIfCurrent(ctx, input)
}

// ---------------------------------------------------------------------------
// speedfirstprobe 错误臂
// ---------------------------------------------------------------------------

func TestW7DSpeedFirstRunnerConstructorGuards(t *testing.T) {
	source := &fakeCandidateSource{}
	probe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{}, TransportProbeOutcome{}
	}
	if _, err := NewSpeedFirstProbeRunner(nil, source, probe, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1 }}); err == nil {
		t.Fatal("nil store 必须拒绝")
	}
	if _, err := NewSpeedFirstProbeRunner(&fakeClaimStore{}, nil, probe, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1 }}); err == nil {
		t.Fatal("nil source 必须拒绝")
	}
	if _, err := NewSpeedFirstProbeRunner(&fakeClaimStore{}, source, nil, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1 }}); err == nil {
		t.Fatal("nil probe 必须拒绝")
	}
	if _, err := NewSpeedFirstProbeRunner(&fakeClaimStore{}, source, probe, SpeedFirstProbeRunnerOptions{}); err == nil {
		t.Fatal("缺时钟必须拒绝")
	}
	if ProbeTimeoutSeconds(0) != 10 {
		t.Fatalf("超时下限必须为 10: %d", ProbeTimeoutSeconds(0))
	}
}

func TestW7DSpeedFirstRunnerBypassArms(t *testing.T) {
	nowMS := func() int64 { return 1_000 }
	probe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown}
	}
	candidate := ProbeCandidate{StateKey: "k", AccountID: "acc", RuntimeKey: "acc", Config: ProbeConfig{FirstByteDeadlineMS: 1_000}}

	// AcquireClaim 错误。
	boomStore := &w7dBypassClaimStore{acquireErr: errors.New("w7d: acquire boom")}
	runner, err := NewSpeedFirstProbeRunner(boomStore, &fakeCandidateSource{}, probe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if err != nil {
		t.Fatal(err)
	}
	if done, err := runner.Run(context.Background(), candidate); done || err == nil {
		t.Fatalf("claim 错误必须中断: done=%t err=%v", done, err)
	}

	// claim 被其他节点持有 → 跳过。
	skipped := &w7dBypassClaimStore{claimNil: true}
	runner, _ = NewSpeedFirstProbeRunner(skipped, &fakeCandidateSource{}, probe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil {
		t.Fatalf("他人持有必须跳过: done=%t err=%v", done, err)
	}

	// 续租失败 → 丢弃提交。
	lostStore := &w7dBypassClaimStore{renewOK: false}
	runner, _ = NewSpeedFirstProbeRunner(lostStore, &fakeCandidateSource{}, probe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil {
		t.Fatalf("续租失败必须安静跳过: done=%t err=%v", done, err)
	}

	// 账户加载错误（需要续租成功才能走到加载）。
	accountBoom := &w7dBypassClaimStore{renewOK: true}
	sourceErr := &w7dErrorCandidateSource{findAccountErr: errors.New("w7d: account boom")}
	runner, _ = NewSpeedFirstProbeRunner(accountBoom, sourceErr, probe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); done || err == nil {
		t.Fatalf("账户加载错误必须中断: done=%t err=%v", done, err)
	}
}

func TestW7DSpeedFirstRunnerDecisionArms(t *testing.T) {
	nowMS := func() int64 { return 1_000 }
	candidate := ProbeCandidate{StateKey: "k", AccountID: "acc", RuntimeKey: "acc", Config: ProbeConfig{FirstByteDeadlineMS: 1_000}}

	// 账户不可用 → Discard。
	ineligibleProbe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown}
	}
	ineligible := &w7dDecisionClaimStore{}
	runner, _ := NewSpeedFirstProbeRunner(ineligible, &w7dNilAccountSource{}, ineligibleProbe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil || !ineligible.discarded {
		t.Fatalf("不可用账户必须 discard: done=%t err=%v discarded=%t", done, err, ineligible.discarded)
	}

	// 账户过期时间非法 → 错误。
	badExpiry := &w7dDecisionClaimStore{account: &SpeedFirstAccountSummary{Status: "active", Schedulable: true, AccountExpiresAt: "bad"}}
	runner, _ = NewSpeedFirstProbeRunner(badExpiry, &w7dStaticAccountSource{account: badExpiry.account}, ineligibleProbe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); done || err == nil {
		t.Fatalf("非法过期时间必须报错: done=%t err=%v", done, err)
	}

	// 分组内凭据缺失 → Discard。
	missingRef := &w7dDecisionClaimStore{account: &SpeedFirstAccountSummary{Status: "active", Schedulable: true}}
	runner, _ = NewSpeedFirstProbeRunner(missingRef, &w7dStaticAccountSource{account: missingRef.account, candidateRef: nil}, ineligibleProbe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil || !missingRef.discarded {
		t.Fatalf("缺失凭据必须 discard: done=%t err=%v discarded=%t", done, err, missingRef.discarded)
	}

	// 中性结果 → Defer。
	deferStore := &w7dDecisionClaimStore{account: &SpeedFirstAccountSummary{Status: "active", Schedulable: true}}
	deferSource := &w7dStaticAccountSource{account: deferStore.account, candidateRef: &ProbeAccountRef{AccountID: "acc"}}
	deferProbe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{Success: false}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown}
	}
	runner, _ = NewSpeedFirstProbeRunner(deferStore, deferSource, deferProbe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil || !deferStore.deferred {
		t.Fatalf("中性结果必须 defer: done=%t err=%v deferred=%t", done, err, deferStore.deferred)
	}

	// 未达标但结果可判定 → RecordFailure。
	failureStore := &w7dDecisionClaimStore{account: &SpeedFirstAccountSummary{Status: "active", Schedulable: true}}
	failureProbe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		firstByte := int64(5_000)
		return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete}
	}
	runner, _ = NewSpeedFirstProbeRunner(failureStore, deferSource, failureProbe, SpeedFirstProbeRunnerOptions{NowMS: nowMS})
	if done, err := runner.Run(context.Background(), candidate); !done || err != nil || failureStore.failureReason == "" {
		t.Fatalf("未达标必须记录失败原因: done=%t err=%v reason=%q", done, err, failureStore.failureReason)
	}
	if !strings.Contains(failureStore.failureReason, "普通路由速度优先恢复探针未满足") {
		t.Fatalf("失败原因文案不正确: %q", failureStore.failureReason)
	}
}

// w7dBypassClaimStore 覆盖 claim 生命周期旁路分支。
type w7dBypassClaimStore struct {
	acquireErr error
	claimNil   bool
	renewOK    bool
}

func (s *w7dBypassClaimStore) AcquireClaim(context.Context, ProbeCandidate) (*ProbeClaim, error) {
	if s.acquireErr != nil {
		return nil, s.acquireErr
	}
	if s.claimNil {
		return nil, nil
	}
	return &ProbeClaim{Token: "t"}, nil
}
func (s *w7dBypassClaimStore) RenewClaim(context.Context, ProbeClaim) (bool, error) {
	return s.renewOK, nil
}
func (s *w7dBypassClaimStore) ReleaseClaim(context.Context, ProbeClaim) error { return nil }
func (s *w7dBypassClaimStore) Discard(context.Context, ProbeCandidate) error  { return nil }
func (s *w7dBypassClaimStore) Defer(context.Context, ProbeCandidate) (bool, error) {
	return true, nil
}
func (s *w7dBypassClaimStore) RecordSuccess(context.Context, ProbeCandidate, ProbeAccountRef, *int64) (SpeedFirstRecoveryResult, error) {
	return SpeedFirstRecoveryResult{}, nil
}
func (s *w7dBypassClaimStore) RecordFailure(context.Context, ProbeCandidate, string) error {
	return nil
}

// w7dErrorCandidateSource 恒返回账户加载错误。
type w7dErrorCandidateSource struct{ findAccountErr error }

func (s *w7dErrorCandidateSource) FindAccountForTest(context.Context, string, string) (*SpeedFirstAccountSummary, error) {
	return nil, s.findAccountErr
}
func (s *w7dErrorCandidateSource) FindCandidateAccount(context.Context, string, string, string) (*ProbeAccountRef, error) {
	return nil, nil
}

// w7dDecisionClaimStore 记录决策分支调用。
type w7dDecisionClaimStore struct {
	account       *SpeedFirstAccountSummary
	discarded     bool
	deferred      bool
	failureReason string
}

func (s *w7dDecisionClaimStore) AcquireClaim(context.Context, ProbeCandidate) (*ProbeClaim, error) {
	return &ProbeClaim{Token: "t"}, nil
}
func (s *w7dDecisionClaimStore) RenewClaim(context.Context, ProbeClaim) (bool, error) {
	return true, nil
}
func (s *w7dDecisionClaimStore) ReleaseClaim(context.Context, ProbeClaim) error { return nil }
func (s *w7dDecisionClaimStore) Discard(context.Context, ProbeCandidate) error {
	s.discarded = true
	return nil
}
func (s *w7dDecisionClaimStore) Defer(context.Context, ProbeCandidate) (bool, error) {
	s.deferred = true
	return true, nil
}
func (s *w7dDecisionClaimStore) RecordSuccess(context.Context, ProbeCandidate, ProbeAccountRef, *int64) (SpeedFirstRecoveryResult, error) {
	return SpeedFirstRecoveryResult{}, nil
}
func (s *w7dDecisionClaimStore) RecordFailure(_ context.Context, _ ProbeCandidate, reason string) error {
	s.failureReason = reason
	return nil
}

// w7dNilAccountSource 返回 nil 账户（不可用）。
type w7dNilAccountSource struct{}

func (w7dNilAccountSource) FindAccountForTest(context.Context, string, string) (*SpeedFirstAccountSummary, error) {
	return nil, nil
}
func (w7dNilAccountSource) FindCandidateAccount(context.Context, string, string, string) (*ProbeAccountRef, error) {
	return nil, nil
}

// w7dStaticAccountSource 返回固定账户/凭据。
type w7dStaticAccountSource struct {
	account      *SpeedFirstAccountSummary
	candidateRef *ProbeAccountRef
}

func (s *w7dStaticAccountSource) FindAccountForTest(context.Context, string, string) (*SpeedFirstAccountSummary, error) {
	return s.account, nil
}
func (s *w7dStaticAccountSource) FindCandidateAccount(context.Context, string, string, string) (*ProbeAccountRef, error) {
	return s.candidateRef, nil
}
