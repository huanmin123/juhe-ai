package keymodelrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

// ---- 状态 JSON 契约（毫秒时间戳，可选字段省略） ----

func TestStateJSONRoundTrip(t *testing.T) {
	retryAt := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	observedAt := retryAt.Add(-time.Minute)
	successAt := retryAt.Add(-2 * time.Minute)
	state := State{
		CapabilityKey: CapabilityKey{
			CredentialSourceAccountID: "src-1", KeyFingerprint: "fp-1", ClientModel: "m",
			ClientEndpointFamily: "responses", FinalUpstreamModel: "up", UpstreamEndpointMode: "responses_json",
			DispatchRevision: 3,
		},
		CapabilityHash: "hash-1", Generation: 2, Phase: Recovering, BackoffAttempt: 2,
		RetryAt: retryAt, RecoverySuccessCount: 1, LastRecoverySuccessAt: successAt,
		LastObservedAt: observedAt, LastOutcome: CompleteSuccess,
		Lease: &Lease{ID: "lease-1", Until: retryAt, PriorCount: 1},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(encoded), `"retryAtMs":`) || strings.Contains(string(encoded), `"RetryAt"`) {
		t.Fatalf("时间字段应使用毫秒 JSON 名: %s", encoded)
	}
	var decoded State
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if !decoded.RetryAt.Equal(retryAt) || !decoded.LastRecoverySuccessAt.Equal(successAt) || !decoded.LastObservedAt.Equal(observedAt) {
		t.Fatalf("时间字段回放不符: %#v", decoded)
	}
	if decoded.Lease == nil || decoded.Lease.ID != "lease-1" || decoded.Lease.PriorCount != 1 {
		t.Fatalf("租约回放不符: %#v", decoded.Lease)
	}
	if decoded.CapabilityHash != "hash-1" || decoded.Phase != Recovering || decoded.Generation != 2 {
		t.Fatalf("核心字段回放不符: %#v", decoded)
	}
	// 零值时间不应出现在 JSON 中。
	minimal := State{CapabilityHash: "h", Phase: Open}
	encodedMinimal, err := json.Marshal(minimal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedMinimal), "retryAtMs") || strings.Contains(string(encodedMinimal), "probeLease") {
		t.Fatalf("零值可选字段应省略: %s", encodedMinimal)
	}
	if err := json.Unmarshal([]byte("{broken"), &decoded); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}

// ---- 状态机校验与转移分支 ----

func TestCapabilityKeyValidateAndHash(t *testing.T) {
	valid := CapabilityKey{
		CredentialSourceAccountID: "src", KeyFingerprint: "fp", ClientModel: "m",
		ClientEndpointFamily: "responses", FinalUpstreamModel: "up", UpstreamEndpointMode: "responses_json",
		DispatchRevision: 1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法键不应报错: %v", err)
	}
	missing := valid
	missing.KeyFingerprint = " "
	if err := missing.Validate(); err == nil || !strings.Contains(err.Error(), "keyFingerprint") {
		t.Fatalf("缺失字段应报错: %v", err)
	}
	zeroRevision := valid
	zeroRevision.DispatchRevision = 0
	if err := zeroRevision.Validate(); err == nil {
		t.Fatalf("dispatchRevision 为 0 应报错")
	}
	if _, err := Hash(missing); err == nil {
		t.Fatalf("非法键不应产生哈希")
	}
	hash1, err := Hash(valid)
	if err != nil || len(hash1) != 64 {
		t.Fatalf("哈希应为 64 位十六进制: %q %v", hash1, err)
	}
	// 字段值不同哈希必须不同。
	other := valid
	other.ClientModel = "other"
	hash2, err := Hash(other)
	if err != nil || hash1 == hash2 {
		t.Fatalf("不同键哈希必须不同")
	}
	if _, err := NewOpen(missing, time.Now()); err == nil {
		t.Fatalf("非法键的 NewOpen 应报错")
	}
}

func TestStateAcquireBranches(t *testing.T) {
	base := State{CapabilityHash: "h", Generation: 1, Phase: Open, RetryAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)}
	now := base.RetryAt.Add(time.Second)
	if _, status := Acquire(base, 2, 0, "lease", now); status != Stale {
		t.Fatalf("代际不匹配应为 stale: %s", status)
	}
	closed := base
	closed.Phase = Closed
	if _, status := Acquire(closed, 1, 0, "lease", now); status != NotDue {
		t.Fatalf("CLOSED 应为 not_due: %s", status)
	}
	beforeDue := base
	beforeDue.RetryAt = now.Add(time.Minute)
	if _, status := Acquire(beforeDue, 1, 0, "lease", now); status != NotDue {
		t.Fatalf("未到重试时间应为 not_due: %s", status)
	}
	leased := base
	leased.Lease = &Lease{ID: "old", Until: now.Add(time.Minute)}
	if _, status := Acquire(leased, 1, 0, "new", now); status != LeaseMismatch {
		t.Fatalf("未过期租约应为 lease_mismatch: %s", status)
	}
	if _, status := Acquire(base, 1, 0, " ", now); status != LeaseMismatch {
		t.Fatalf("空租约 ID 应为 lease_mismatch: %s", status)
	}
	acquired, status := Acquire(base, 1, 0, "lease-1", now)
	if status != Applied || acquired.Phase != HalfOpen || acquired.Lease == nil || acquired.Lease.ID != "lease-1" {
		t.Fatalf("正常获取应 applied 且进入 HALF_OPEN: %s %#v", status, acquired.Lease)
	}
}

func TestStateSettleBranches(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	base := State{CapabilityKey: CapabilityKey{DispatchRevision: 5}, CapabilityHash: "h", Generation: 1, Phase: HalfOpen, Lease: &Lease{ID: "lease-1", Until: now.Add(time.Minute)}, RetryAt: now}
	result := RecoveryResult{Generation: 1, DispatchRevision: 5, LeaseID: "lease-1", ObservedAt: now.Add(time.Second)}
	if _, status := Settle(base, RecoveryResult{Generation: 2, DispatchRevision: 5, LeaseID: "lease-1"}); status != Stale {
		t.Fatalf("代际不匹配应为 stale: %s", status)
	}
	wrongLease := result
	wrongLease.LeaseID = "other"
	if _, status := Settle(base, wrongLease); status != LeaseMismatch {
		t.Fatalf("租约不符应为 lease_mismatch: %s", status)
	}
	late := result
	late.ObservedAt = base.Lease.Until.Add(time.Second)
	if _, status := Settle(base, late); status != Stale {
		t.Fatalf("超过租约期应为 stale: %s", status)
	}
	// unknown：有历史恢复计数回到 RECOVERING，否则回 OPEN。
	unknown := result
	unknown.Outcome = Unknown
	recovering, status := Settle(State{CapabilityKey: CapabilityKey{DispatchRevision: 5}, CapabilityHash: "h", Generation: 1, Phase: HalfOpen, RecoverySuccessCount: 2, Lease: &Lease{ID: "lease-1", Until: now.Add(time.Minute)}}, unknown)
	if status != Applied || recovering.Phase != Recovering || !recovering.RetryAt.Equal(unknown.ObservedAt.Add(RecoveryProbeInterval)) {
		t.Fatalf("unknown 且有计数应进入 RECOVERING: %s %#v", status, recovering)
	}
	reopened, status := Settle(State{CapabilityKey: CapabilityKey{DispatchRevision: 5}, CapabilityHash: "h", Generation: 1, Phase: HalfOpen, Lease: &Lease{ID: "lease-1", Until: now.Add(time.Minute)}}, unknown)
	if status != Applied || reopened.Phase != Open {
		t.Fatalf("unknown 且无计数应回到 OPEN: %s %#v", status, reopened)
	}
	// upstream_not_complete：退避递增且封顶 4。
	notComplete := result
	notComplete.Outcome = UpstreamNotComplete
	backoff := base
	backoff.BackoffAttempt = 4
	settled, status := Settle(backoff, notComplete)
	if status != Applied || settled.Phase != Open || settled.BackoffAttempt != 4 || settled.RecoverySuccessCount != 0 {
		t.Fatalf("退避应封顶在 4: %s %#v", status, settled)
	}
	// complete_success：达到阈值转 CLOSED。
	success := result
	success.Outcome = CompleteSuccess
	closed, status := Settle(State{CapabilityKey: CapabilityKey{DispatchRevision: 5}, CapabilityHash: "h", Generation: 1, Phase: HalfOpen, RecoverySuccessCount: RecoverySuccessThreshold - 1, Lease: &Lease{ID: "lease-1", Until: now.Add(time.Minute)}}, success)
	if status != Applied || closed.Phase != Closed || closed.BackoffAttempt != 0 || closed.RecoverySuccessCount != 0 {
		t.Fatalf("达到阈值应 CLOSED: %s %#v", status, closed)
	}
	// complete_success：间隔超限时计数重置为 1。
	gapped := State{CapabilityKey: CapabilityKey{DispatchRevision: 5}, CapabilityHash: "h", Generation: 1, Phase: HalfOpen, RecoverySuccessCount: 2, LastRecoverySuccessAt: now.Add(-RecoverySuccessMaxGap - time.Minute), Lease: &Lease{ID: "lease-1", Until: now.Add(time.Minute)}}
	counted, status := Settle(gapped, success)
	if status != Applied || counted.Phase != Recovering || counted.RecoverySuccessCount != 1 {
		t.Fatalf("超限间隔应重置计数: %s %#v", status, counted)
	}
	if _, status := Settle(base, RecoveryResult{Generation: 1, DispatchRevision: 5, LeaseID: "lease-1", Outcome: Outcome("weird")}); status != LeaseMismatch {
		t.Fatalf("未知结果应为 lease_mismatch: %s", status)
	}
}

// ---- Runner：Run 守卫、续租退出路径与探测分支 ----

type woFakeStore struct {
	now time.Time
	due []State
}

func (s *woFakeStore) ServerNow(context.Context) (time.Time, error) { return s.now, nil }
func (s *woFakeStore) ListDue(context.Context, time.Time, int64) ([]State, error) {
	return s.due, nil
}
func (s *woFakeStore) Acquire(context.Context, State, string, bool, bool) (State, MutationStatus, error) {
	return State{}, Stale, nil
}
func (s *woFakeStore) Renew(context.Context, State, string) (bool, error) { return true, nil }
func (s *woFakeStore) Commit(context.Context, State, State, string) (MutationStatus, error) {
	return Applied, nil
}

func TestRunnerRunGuardsAndCancelledContext(t *testing.T) {
	var nilRunner *Runner
	if err := nilRunner.Run(context.Background()); err == nil {
		t.Fatalf("nil runner 应报错")
	}
	if err := NewRunner(nil, nil, nil).Run(context.Background()); err == nil {
		t.Fatalf("缺 store 应报错")
	}
	if err := NewRunner(&woFakeStore{}, nil, nil).Run(context.Background()); err == nil {
		t.Fatalf("缺 loader 应报错")
	}
	// 已取消的 context：执行一轮后返回 ctx.Err。
	runner := NewRunner(&woFakeStore{}, woLoaderReturning(nil, nil), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后 Run 应返回 ctx.Err: %v", err)
	}
}

func TestRunnerRenewLeaseExitsOnDoneAndContext(t *testing.T) {
	runner := NewRunner(&woFakeStore{}, woLoaderReturning(nil, nil), nil)
	// done 关闭：goroutine 必须在不触发 lostLease 的情况下退出。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lostLease := make(chan struct{}, 1)
	done := make(chan struct{})
	close(done)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runner.renewLease(ctx, State{}, "lease", cancel, lostLease, done)
	}()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("done 关闭后 renewLease 应退出")
	}
	// context 取消：同样直接退出。
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		runner.renewLease(ctx2, State{}, "lease", cancel2, lostLease, done2)
	}()
	cancel2()
	finished2 := make(chan struct{})
	go func() { wg.Wait(); close(finished2) }()
	select {
	case <-finished2:
	case <-time.After(2 * time.Second):
		t.Fatalf("context 取消后 renewLease 应退出")
	}
}

func TestRunnerExecuteProbeBranches(t *testing.T) {
	state := State{CapabilityKey: CapabilityKey{CredentialSourceAccountID: "src-1", DispatchRevision: 7}}
	runner := NewRunner(&woFakeStore{}, woLoaderReturning(nil, errors.New("load failed")), nil)
	if got := runner.executeProbe(context.Background(), state); got != Unknown {
		t.Fatalf("装载失败应为 unknown: %s", got)
	}
	// 输入账号或派次不匹配 → unknown。
	mismatched := NewRunner(&woFakeStore{}, woLoaderReturning([]accounthealth.Input{{AccountID: "other", DispatchRevision: 7}}, nil), nil)
	if got := mismatched.executeProbe(context.Background(), state); got != Unknown {
		t.Fatalf("输入不匹配应为 unknown: %s", got)
	}
	// 命中输入后委托注入的 probe。
	matching := woLoaderReturning([]accounthealth.Input{{AccountID: "src-1", DispatchRevision: 7}}, nil)
	probing := NewRunner(&woFakeStore{}, matching, nil)
	probing.probe = func(_ context.Context, _ State, _ accounthealth.Input) Outcome { return CompleteSuccess }
	if got := probing.executeProbe(context.Background(), state); got != CompleteSuccess {
		t.Fatalf("命中输入应委托 probe: %s", got)
	}
	// 命中输入但 probe 报告任务失败 → unknown。
	failing := NewRunner(&woFakeStore{}, matching, nil)
	failing.probe = func(_ context.Context, _ State, _ accounthealth.Input) Outcome { return Unknown }
	if got := failing.executeProbe(context.Background(), state); got != Unknown {
		t.Fatalf("probe 失败应为 unknown: %s", got)
	}
}

func TestRunnerHelpers(t *testing.T) {
	if got := runningSlice(nil); len(got) != 0 {
		t.Fatalf("空 running 应返回空切片")
	}
	lease := newLeaseID()
	if !strings.HasPrefix(lease, "mcr-") {
		t.Fatalf("租约 ID 应带 mcr- 前缀: %q", lease)
	}
}

// ---- 测试辅助 ----

func woLoaderReturning(inputs []accounthealth.Input, err error) InputLoader {
	return woFakeLoader{inputs: inputs, err: err}
}

type woFakeLoader struct {
	inputs []accounthealth.Input
	err    error
}

func (l woFakeLoader) LoadAccount(context.Context, string) ([]accounthealth.Input, error) {
	return l.inputs, l.err
}
