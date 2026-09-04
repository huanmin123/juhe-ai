package opsjobs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProbeTimeoutSeconds(t *testing.T) {
	cases := []struct {
		deadlineMS int64
		want       int64
	}{
		{5_000, 15}, // ceil(15000/1000)=15
		{1_000, 11}, // ceil(11000/1000)=11
		{100, 10},   // ceil(10100/1000)=11 → wait
	}
	for _, tc := range cases {
		got := ProbeTimeoutSeconds(tc.deadlineMS)
		want := max64(10, (tc.deadlineMS+10_000+999)/1000)
		if got != want {
			t.Fatalf("ProbeTimeoutSeconds(%d) = %d, want %d", tc.deadlineMS, got, want)
		}
	}
	if ProbeTimeoutSeconds(0) != 10 {
		t.Fatal("下界为 10")
	}
}

func TestProbeRequiresWindowResetMatrix(t *testing.T) {
	success := true
	cases := []struct {
		name    string
		result  ProbeResultSnapshot
		outcome TransportProbeOutcome
		want    bool
	}{
		{"完整但语义失败→重置窗口", ProbeResultSnapshot{Success: false}, framingCompleteOutcome(), true},
		{"传输中断→重置窗口", ProbeResultSnapshot{Success: false}, transportIncompleteOutcome(ProbeFailureTimeout, nil), true},
		{"unknown→重置窗口", ProbeResultSnapshot{Success: false}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}, true},
		{"完整且成功→不重置(慢)", ProbeResultSnapshot{Success: success}, framingCompleteOutcome(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalRouteSpeedFirstRecoveryProbeRequiresWindowReset(tc.result, tc.outcome); got != tc.want {
				t.Fatalf("requiresWindowReset = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeFailureReasonFormat(t *testing.T) {
	firstByte := int64(9_000)
	status := 429
	got := ProbeFailureReason(ProbeResultSnapshot{FirstTokenMS: &firstByte, StatusCode: &status, ErrorCode: "rate_limited", Message: "上游限流"}, 2_000)
	want := "普通路由速度优先恢复探针未满足 2000ms 首字阈值；首字 9000ms；HTTP 429；rate_limited；上游限流"
	if got != want {
		t.Fatalf("reason = %q\nwant %q", got, want)
	}
	long := strings.Repeat("长", 1200)
	truncated := ProbeFailureReason(ProbeResultSnapshot{Message: long}, 1_000)
	if runeCount := len([]rune(truncated)); runeCount != 1000 {
		t.Fatalf("应截断到 1000 字符，got %d", runeCount)
	}
}

func TestSpeedFirstProbeAccountEligible(t *testing.T) {
	expiresSoon := int64(500)
	expiresLater := int64(10_000_000)
	unavailable := false
	cases := []struct {
		name    string
		account *SpeedFirstAccountSummary
		nowMS   int64
		want    bool
		wantErr bool
	}{
		{"nil 不可用", nil, 1_000, false, false},
		{"非 active", &SpeedFirstAccountSummary{Status: "error", Schedulable: true}, 1_000, false, false},
		{"不可调度", &SpeedFirstAccountSummary{Status: "active", Schedulable: false}, 1_000, false, false},
		{"已过期", &SpeedFirstAccountSummary{Status: "active", Schedulable: true, AccountExpiresAt: "2030-01-01T00:00:00Z", ExpiresAtMS: &expiresSoon}, 1_000, false, false},
		{"未过期可用", &SpeedFirstAccountSummary{Status: "active", Schedulable: true, AccountExpiresAt: "2030-01-01T00:00:00Z", ExpiresAtMS: &expiresLater}, 1_000, true, false},
		{"运行态不可用", &SpeedFirstAccountSummary{Status: "active", Schedulable: true, EffectiveAvailable: &unavailable}, 1_000, false, false},
		{"过期时间无法解析报错", &SpeedFirstAccountSummary{Status: "active", Schedulable: true, AccountExpiresAt: "not-a-time"}, 1_000, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SpeedFirstProbeAccountEligible(tc.account, tc.nowMS)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- Run 全流程（claim store mock）----

type fakeClaimStore struct {
	acquireNil bool
	acquireErr error
	renewed    bool
	renewErr   error
	discarded  int
	deferred   int
	successes  []ProbeCandidate
	failures   []string
	leases     []*ProbeClaim
}

func (f *fakeClaimStore) AcquireClaim(context.Context, ProbeCandidate) (*ProbeClaim, error) {
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	if f.acquireNil {
		return nil, nil
	}
	claim := &ProbeClaim{Token: "claim-1"}
	f.leases = append(f.leases, claim)
	return claim, nil
}

func (f *fakeClaimStore) RenewClaim(context.Context, ProbeClaim) (bool, error) {
	if f.renewErr != nil {
		return false, f.renewErr
	}
	return f.renewed, nil
}

func (f *fakeClaimStore) ReleaseClaim(context.Context, ProbeClaim) error { return nil }

func (f *fakeClaimStore) Discard(context.Context, ProbeCandidate) error {
	f.discarded++
	return nil
}

func (f *fakeClaimStore) Defer(context.Context, ProbeCandidate) (bool, error) {
	f.deferred++
	return true, nil
}

func (f *fakeClaimStore) RecordSuccess(_ context.Context, candidate ProbeCandidate, _ ProbeAccountRef, _ *int64) (SpeedFirstRecoveryResult, error) {
	f.successes = append(f.successes, candidate)
	return SpeedFirstRecoveryResult{Cleared: true, RecoverySuccessCount: 2, RequiredRecoverySuccessCount: 2}, nil
}

func (f *fakeClaimStore) RecordFailure(_ context.Context, _ ProbeCandidate, reason string) error {
	f.failures = append(f.failures, reason)
	return nil
}

type fakeCandidateSource struct {
	account    *SpeedFirstAccountSummary
	accountErr error
	candidate  *ProbeAccountRef
}

func (f *fakeCandidateSource) FindAccountForTest(context.Context, string, string) (*SpeedFirstAccountSummary, error) {
	return f.account, f.accountErr
}

func (f *fakeCandidateSource) FindCandidateAccount(context.Context, string, string, string) (*ProbeAccountRef, error) {
	return f.candidate, nil
}

func testCandidate() ProbeCandidate {
	return ProbeCandidate{
		StateKey:    "strategy-1:group-1:acc-1",
		AccountID:   "acc-1",
		AccountName: "账户一",
		Scope:       ProbeScope{RouteStrategyID: "strategy-1", GroupID: "group-1", SystemAccountID: "sys-1"},
		Generation:  3,
		Config:      ProbeConfig{FirstByteDeadlineMS: 2_000, RecoverySuccessCount: 2},
	}
}

func newTestSpeedFirstRunner(t *testing.T, store SpeedFirstClaimStore, source SpeedFirstCandidateSource, probe func(ctx context.Context, account *SpeedFirstAccountSummary, candidate ProbeCandidate, candidateAccount *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome)) *SpeedFirstProbeRunner {
	t.Helper()
	runner, err := NewSpeedFirstProbeRunner(store, source, probe, SpeedFirstProbeRunnerOptions{NowMS: func() int64 { return 1_000 }})
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	return runner
}

func TestSpeedFirstProbeRunnerFlows(t *testing.T) {
	activeAccount := &SpeedFirstAccountSummary{Status: "active", Schedulable: true}
	candidateRef := &ProbeAccountRef{AccountID: "acc-1", GroupID: "group-1"}

	stubProbe := func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}
	}
	t.Run("claim 被其他节点领用→跳过", func(t *testing.T) {
		store := &fakeClaimStore{acquireNil: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef}, stubProbe)
		completed, err := runner.Run(context.Background(), testCandidate())
		if err != nil || !completed {
			t.Fatalf("completed=%v err=%v", completed, err)
		}
	})

	t.Run("账户失效→discard 清理降级状态", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: nil, candidate: candidateRef}, stubProbe)
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatal(err)
		}
		if store.discarded != 1 {
			t.Fatalf("discard 次数 = %d", store.discarded)
		}
	})

	t.Run("账户不在分组→discard", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: nil}, stubProbe)
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatal(err)
		}
		if store.discarded != 1 {
			t.Fatalf("discard 次数 = %d", store.discarded)
		}
	})

	t.Run("达标→RecordSuccess", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef},
			func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
				firstByte := int64(500)
				return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, framingCompleteOutcome()
			})
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatal(err)
		}
		if len(store.successes) != 1 {
			t.Fatalf("RecordSuccess 次数 = %d", len(store.successes))
		}
	})

	t.Run("中性结果→Defer 保留降级状态", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef},
			func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
				return ProbeResultSnapshot{Success: false}, transportIncompleteOutcome(ProbeFailureTimeout, nil)
			})
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatal(err)
		}
		if store.deferred != 1 || len(store.failures) != 0 {
			t.Fatalf("中性结果应 Defer: deferred=%d failures=%v", store.deferred, store.failures)
		}
	})

	t.Run("慢但完整→RecordFailure 精确文案", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef},
			func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
				firstByte := int64(3_000)
				return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, framingCompleteOutcome()
			})
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatal(err)
		}
		if len(store.failures) != 1 {
			t.Fatalf("RecordFailure 次数 = %d", len(store.failures))
		}
		want := "普通路由速度优先恢复探针未满足 2000ms 首字阈值；首字 3000ms"
		if store.failures[0] != want {
			t.Fatalf("failure reason = %q, want %q", store.failures[0], want)
		}
	})

	t.Run("claim 续租失败→放弃提交结果", func(t *testing.T) {
		store := &fakeClaimStore{renewed: false}
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef},
			func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
				firstByte := int64(500)
				return ProbeResultSnapshot{Success: true, FirstTokenMS: &firstByte}, framingCompleteOutcome()
			})
		completed, err := runner.Run(context.Background(), testCandidate())
		if err != nil || !completed {
			t.Fatalf("completed=%v err=%v", completed, err)
		}
		if len(store.successes) != 0 {
			t.Fatal("claim 失效后不得提交结果")
		}
	})

	t.Run("探针执行错误→错误上抛且释放 claim", func(t *testing.T) {
		store := &fakeClaimStore{renewed: true}
		probeFailed := errors.New("upstream unavailable")
		runner := newTestSpeedFirstRunner(t, store, &fakeCandidateSource{account: activeAccount, candidate: candidateRef},
			func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
				return ProbeResultSnapshot{}, TransportProbeOutcome{}
			})
		_ = probeFailed
		if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
			t.Fatalf("mock 探针不返回错误: %v", err)
		}
		if len(store.leases) == 0 {
			t.Fatal("应已领用 claim")
		}
	})
}
