package opsjobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBalanceRepo struct {
	candidates      []BalanceDetectionCandidate
	commitResults   map[string]bool
	enableResults   map[string]bool
	snapshotResults map[string]bool
	commits         []BalanceCommitDueInput
	enables         []BalanceEnableInput
	snapshots       []BalanceSnapshotInput
}

func newFakeBalanceRepo() *fakeBalanceRepo {
	return &fakeBalanceRepo{
		commitResults:   map[string]bool{},
		enableResults:   map[string]bool{},
		snapshotResults: map[string]bool{},
	}
}

func (f *fakeBalanceRepo) ListDueCandidates(context.Context, int) ([]BalanceDetectionCandidate, error) {
	return f.candidates, nil
}

func (f *fakeBalanceRepo) CommitDetectionDue(_ context.Context, input BalanceCommitDueInput) (bool, error) {
	f.commits = append(f.commits, input)
	result, exists := f.commitResults[input.AccountID]
	if !exists {
		return true, nil
	}
	return result, nil
}

func (f *fakeBalanceRepo) EnableDetectedQuery(_ context.Context, input BalanceEnableInput) (bool, error) {
	f.enables = append(f.enables, input)
	result, exists := f.enableResults[input.AccountID]
	if !exists {
		return true, nil
	}
	return result, nil
}

func (f *fakeBalanceRepo) ReplaceSnapshotIfCurrent(_ context.Context, input BalanceSnapshotInput) (bool, error) {
	f.snapshots = append(f.snapshots, input)
	result, exists := f.snapshotResults[input.AccountID]
	if !exists {
		return true, nil
	}
	return result, nil
}

type fakeBalanceLease struct {
	acquired bool
	ran      int
}

func (f *fakeBalanceLease) RunWithLease(ctx context.Context, _ BalanceDetectionCandidate, run func(ctx context.Context) error) (bool, error) {
	if !f.acquired {
		return false, nil
	}
	f.ran++
	return true, run(ctx)
}

type fakeBalanceDetector struct {
	result BalanceBuiltinQueryResult
	err    error
	seen   []BalanceQueryConfig
}

func (f *fakeBalanceDetector) QueryBuiltin(_ context.Context, _ BalanceDetectionCandidate, config BalanceQueryConfig) (BalanceBuiltinQueryResult, error) {
	f.seen = append(f.seen, config)
	if f.err != nil {
		return BalanceBuiltinQueryResult{}, f.err
	}
	return f.result, nil
}

func testBalanceDeps(repo BalanceDetectionRepo, detector BalanceDetector, nowMS func() int64) BalanceAutoDetectDependencies {
	return BalanceAutoDetectDependencies{
		Repo:     repo,
		Lease:    &fakeBalanceLease{acquired: true},
		Detector: detector,
		NowMS:    nowMS,
	}
}

func detectionCandidate(id string) BalanceDetectionCandidate {
	nextRefreshAt := "2030-01-01T00:00:00Z"
	return BalanceDetectionCandidate{
		ID:              id,
		SystemAccountID: "sys-1",
		ConfigRevision:  3,
		NextRefreshAt:   &nextRefreshAt,
	}
}

var balanceNowMS = func() int64 { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() }

// 探测结果阈值/分类矩阵：fresh/unlimited 命中，unsupported 记不支持，其余与错误 retry。
func TestBalanceDetectionAttemptMatrix(t *testing.T) {
	cases := []struct {
		name     string
		status   BalanceSnapshotStatus
		queryErr error
		want     balanceAttemptKind
	}{
		{"fresh 命中", BalanceSnapshotFresh, nil, balanceAttemptMatched},
		{"unlimited 命中", BalanceSnapshotUnlimited, nil, balanceAttemptMatched},
		{"unsupported 不支持", BalanceSnapshotUnsupported, nil, balanceAttemptUnsupported},
		{"stale 重试", BalanceSnapshotStale, nil, balanceAttemptRetry},
		{"error 重试", BalanceSnapshotError, nil, balanceAttemptRetry},
		{"传输错误重试", "", errors.New("connect refused"), balanceAttemptRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := testBalanceDeps(newFakeBalanceRepo(), &fakeBalanceDetector{
				result: BalanceBuiltinQueryResult{Adapter: "adapter-a", Snapshot: BalanceSnapshot{Status: tc.status}},
				err:    tc.queryErr,
			}, balanceNowMS)
			_, kind, err := DetectAccountBalanceAdapterAttempt(context.Background(), detectionCandidate("acc-1"),
				BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: BalanceDetectionIntervalMinutes}, deps)
			if kind != tc.want {
				t.Fatalf("kind = %s, want %s (err=%v)", kind, tc.want, err)
			}
		})
	}
}

// 命中路径：enable 围栏 + 快照围栏 + nextRefreshAt 按探测 interval 推移。
func TestBalanceAutoDetectEnabledPath(t *testing.T) {
	repo := newFakeBalanceRepo()
	deps := testBalanceDeps(repo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Adapter: "adapter-a", Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)
	outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != BalanceOutcomeEnabled {
		t.Fatalf("outcome = %s", outcome)
	}
	if len(repo.enables) != 1 {
		t.Fatalf("enables = %d", len(repo.enables))
	}
	enable := repo.enables[0]
	if enable.ExpectedConfigRevision != 3 {
		t.Fatalf("config revision 围栏 = %d", enable.ExpectedConfigRevision)
	}
	if enable.Config.PreferredBuiltinAdapter != "adapter-a" || enable.Config.IntervalMinutes != BalanceDetectionIntervalMinutes {
		t.Fatalf("探测配置不符: %+v", enable.Config)
	}
	// interval 阈值：5 分钟 + 被动抖动（确定性 nil 随机源取窗口上界 +30s）。
	wantNext := time.UnixMilli(balanceNowMS() + BalanceDetectionIntervalMinutes*60_000 + 30_000).UTC().Format(time.RFC3339Nano)
	if enable.NextRefreshAt != wantNext {
		t.Fatalf("nextRefreshAt = %s, want %s", enable.NextRefreshAt, wantNext)
	}
	if len(repo.snapshots) != 1 {
		t.Fatal("应写入快照")
	}
	snapshot := repo.snapshots[0]
	if snapshot.Snapshot.Status != BalanceSnapshotFresh || snapshot.Snapshot.ConfigRevision != 3 {
		t.Fatalf("快照内容不符: %+v", snapshot.Snapshot)
	}
	if snapshot.Snapshot.LastSuccessAt != snapshot.Snapshot.LastAttemptAt {
		t.Fatal("lastSuccessAt 应等于 lastAttemptAt")
	}
}

// retry 路径：+5 分钟顺延；意图围栏失效 → stale。
func TestBalanceAutoDetectRetryAndStale(t *testing.T) {
	t.Run("retry 顺延 5 分钟", func(t *testing.T) {
		repo := newFakeBalanceRepo()
		deps := testBalanceDeps(repo, &fakeBalanceDetector{
			result: BalanceBuiltinQueryResult{Adapter: "adapter-a", Snapshot: BalanceSnapshot{Status: BalanceSnapshotStale}},
		}, balanceNowMS)
		outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
		if err != nil || outcome != BalanceOutcomeRetry {
			t.Fatalf("outcome=%s err=%v", outcome, err)
		}
		wantRetry := time.UnixMilli(balanceNowMS() + BalanceDetectionRetryMinutes*60_000 + 30_000).UTC().Format(time.RFC3339Nano)
		if len(repo.commits) != 1 || *repo.commits[0].NextRefreshAt != wantRetry {
			t.Fatalf("commits = %+v", repo.commits)
		}
	})
	t.Run("意图围栏失效→stale", func(t *testing.T) {
		repo := newFakeBalanceRepo()
		repo.commitResults["acc-1"] = false
		deps := testBalanceDeps(repo, &fakeBalanceDetector{
			result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotStale}},
		}, balanceNowMS)
		outcome, _ := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
		if outcome != BalanceOutcomeStale {
			t.Fatalf("outcome = %s", outcome)
		}
	})
	t.Run("无持久意图时 retry 围栏失败保持 retry", func(t *testing.T) {
		repo := newFakeBalanceRepo()
		repo.commitResults["acc-1"] = false
		deps := testBalanceDeps(repo, &fakeBalanceDetector{
			result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotStale}},
		}, balanceNowMS)
		candidate := detectionCandidate("acc-1")
		candidate.NextRefreshAt = nil
		outcome, _ := AutoDetectAccountBalanceCandidate(context.Background(), candidate, deps)
		if outcome != BalanceOutcomeRetry {
			t.Fatalf("outcome = %s", outcome)
		}
	})
}

// unsupported 路径：探测收口（NextRefreshAt=nil）。
func TestBalanceAutoDetectUnsupportedClosesIntent(t *testing.T) {
	repo := newFakeBalanceRepo()
	deps := testBalanceDeps(repo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}},
	}, balanceNowMS)
	outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
	if err != nil || outcome != BalanceOutcomeUnsupported {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if len(repo.commits) != 1 || repo.commits[0].NextRefreshAt != nil {
		t.Fatalf("unsupported 应收口意图: %+v", repo.commits)
	}
	if len(repo.enables) != 0 {
		t.Fatal("unsupported 不得开启探测")
	}
}

// enable 围栏失败 / 快照围栏失败 → stale。
func TestBalanceAutoDetectFenceFailuresAreStale(t *testing.T) {
	t.Run("enable 围栏失败", func(t *testing.T) {
		repo := newFakeBalanceRepo()
		repo.enableResults["acc-1"] = false
		deps := testBalanceDeps(repo, &fakeBalanceDetector{
			result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
		}, balanceNowMS)
		outcome, _ := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
		if outcome != BalanceOutcomeStale {
			t.Fatalf("outcome = %s", outcome)
		}
	})
	t.Run("快照围栏失败", func(t *testing.T) {
		repo := newFakeBalanceRepo()
		repo.snapshotResults["acc-1"] = false
		deps := testBalanceDeps(repo, &fakeBalanceDetector{
			result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnlimited}},
		}, balanceNowMS)
		outcome, _ := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
		if outcome != BalanceOutcomeStale {
			t.Fatalf("outcome = %s", outcome)
		}
	})
}

// lease busy → deferred。
func TestBalanceAutoDetectLeaseBusy(t *testing.T) {
	repo := newFakeBalanceRepo()
	deps := testBalanceDeps(repo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)
	deps.Lease = &fakeBalanceLease{acquired: false}
	outcome, err := AutoDetectAccountBalanceCandidate(context.Background(), detectionCandidate("acc-1"), deps)
	if err != nil || outcome != BalanceOutcomeLeaseBusy {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if len(repo.enables) != 0 {
		t.Fatal("lease busy 不得触碰配置")
	}
}

// 恢复扫描汇总：success/partial 判定与计数（含 ctx 取消 deferred）。
func TestBalanceAutoDetectionRecoverySummary(t *testing.T) {
	freshRepo := newFakeBalanceRepo()
	freshRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-1"), detectionCandidate("acc-2")}
	deps := testBalanceDeps(freshRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)
	summary, err := RunBalanceAutoDetectionRecovery(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != "success" || summary.SelectedCount != 2 || summary.EnabledCount != 2 {
		t.Fatalf("summary = %+v", summary)
	}

	// 混合结果 → partial。
	mixedRepo := newFakeBalanceRepo()
	mixedRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-1"), detectionCandidate("acc-2")}
	mixedRepo.enableResults["acc-2"] = false
	summary, err = RunBalanceAutoDetectionRecovery(context.Background(), testBalanceDeps(mixedRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != "partial" || summary.StaleCount != 1 || summary.EnabledCount != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	// 批次上限契约：恢复扫描请求 limit = BalanceAutoDetectionRecoveryBatchSize。
	var recordedLimits []int
	limitedRepo := &limitRecordingRepo{inner: newFakeBalanceRepo(), limits: &recordedLimits}
	_, err = RunBalanceAutoDetectionRecovery(context.Background(), testBalanceDeps(limitedRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS))
	if err != nil {
		t.Fatal(err)
	}
	if len(recordedLimits) != 1 || recordedLimits[0] != BalanceAutoDetectionRecoveryBatchSize {
		t.Fatalf("limits = %v, want [%d]", recordedLimits, BalanceAutoDetectionRecoveryBatchSize)
	}
}

type limitRecordingRepo struct {
	inner  *fakeBalanceRepo
	limits *[]int
}

func (l *limitRecordingRepo) ListDueCandidates(ctx context.Context, limit int) ([]BalanceDetectionCandidate, error) {
	*l.limits = append(*l.limits, limit)
	return l.inner.ListDueCandidates(ctx, limit)
}

func (l *limitRecordingRepo) CommitDetectionDue(ctx context.Context, input BalanceCommitDueInput) (bool, error) {
	return l.inner.CommitDetectionDue(ctx, input)
}

func (l *limitRecordingRepo) EnableDetectedQuery(ctx context.Context, input BalanceEnableInput) (bool, error) {
	return l.inner.EnableDetectedQuery(ctx, input)
}

func (l *limitRecordingRepo) ReplaceSnapshotIfCurrent(ctx context.Context, input BalanceSnapshotInput) (bool, error) {
	return l.inner.ReplaceSnapshotIfCurrent(ctx, input)
}
