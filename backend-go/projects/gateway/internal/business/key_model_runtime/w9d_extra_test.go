package keymodelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// w9d：参数与数据臂 + 关闭 Redis 后的 script 失败臂 + runner mock。
// 不可达登记：
//   - newLeaseID rand 失败 fallback（runner.go 191）：crypto/rand 不失败。
//   - stringValue 的 []byte 分支（redis.go 481）：go-redis v9 Eval 结果中的
//     bulk string 恒为 string，不产生 []byte。
//   - HashCapability/Open 内 json.Marshal err（contract.go 187/203）与
//     encodeRedisState err（redis.go 217/380）：struct→json.Marshal 不失败。
//   - ClaimJ1Confirmation 的 hash err（redis.go 337）：入参已过非空与
//     revision>=1 校验，NormalizeCapability 恒通过。
//   - admit/release/renew/acquire 的 "invalid result" 与 parseErr 臂
//     （redis.go 125/129/139/143/157/173 len 检查/180/235/356）：
//     需要操控 Lua 返回形状，miniredis 无法注入。
//   - ListDue 循环内 GET err（redis.go 270）：需在 ZRange 成功后注入网络故障。
//   - MemoryStore.RecordFailure 的 stale 臂（memory.go 61 附近 57-59）：
//     CapabilityHash 含 DispatchRevision，不同 revision 恒为不同 key。
// ---------------------------------------------------------------------------

func w9dRedis(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "w9d", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, server
}

func TestW9DContractArms(t *testing.T) {
	var state State
	// 语法合法但字段类型错误：进入 State.UnmarshalJSON 内部失败臂。
	if err := json.Unmarshal([]byte(`{"generation":"not-an-int"}`), &state); err == nil {
		t.Fatal("type-mismatched json must fail")
	}
	if _, err := NormalizeCapability(Capability{}); err == nil {
		t.Fatal("empty capability must fail normalize")
	}
	if _, err := Open(Capability{}, time.Time{}); err == nil {
		t.Fatal("zero observed time must fail open")
	}
}

func TestW9DMemoryArms(t *testing.T) {
	store := NewMemoryStore()
	now := time.UnixMilli(1_000).UTC()
	capability := testCapability()
	// RecordFailure 正常路径。
	if _, _, err := store.RecordFailure(capability, now); err != nil {
		t.Fatal(err)
	}
	// AdmitForeground：hash err / blocked（state 存在且非 CLOSED）。
	if _, _, _, err := store.AdmitForeground(Capability{}, "a", now); err == nil {
		t.Fatal("empty capability admit must fail")
	}
	if _, _, _, err := store.AdmitForeground(capability, "a", now); err != nil {
		t.Fatalf("admit: %v", err)
	}
	decision, _, _, err := store.AdmitForeground(capability, "b", now)
	if err != nil || decision != ForegroundBlocked {
		t.Fatalf("blocked decision=%s err=%v", decision, err)
	}
	// AdmitForeground：无 state 的 capability → admitted；重复 attempt 幂等。
	bare := testCapability()
	bare.ClientModel = "model-b"
	if _, permit, _, err := store.AdmitForeground(bare, "b1", now); err != nil || permit.AttemptID != "b1" {
		t.Fatalf("bare admit permit=%+v err=%v", permit, err)
	}
	idem, _, _, err := store.AdmitForeground(bare, "b1", now.Add(10*time.Second))
	if err != nil || idem != ForegroundAdmitted {
		t.Fatalf("idempotent admit decision=%s err=%v", idem, err)
	}
	// RenewForeground：attempt 未知（permits map 存在）。
	if _, ok := store.RenewForeground(ForegroundPermit{CapabilityHash: w9dHash(t, bare), AttemptID: "ghost"}, now.Add(time.Second)); ok {
		t.Fatal("unknown attempt renew must fail")
	}
	// AdmitForeground：permit 过期清理循环。
	if _, _, _, err := store.AdmitForeground(bare, "b2", now.Add(91*time.Second)); err != nil {
		t.Fatalf("admit after expiry: %v", err)
	}
	// AcquireRecovery / SettleRecovery：hash err 与未记录 state 的 stale 臂。
	if _, _, err := store.AcquireRecovery(Capability{}, 0, "lease", now); err == nil {
		t.Fatal("acquire empty capability must fail")
	}
	if _, _, err := store.SettleRecovery(Capability{}, 0, "lease", OutcomeCompleteSuccess, now); err == nil {
		t.Fatal("settle empty capability must fail")
	}
	unknown := testCapability()
	unknown.ClientModel = "model-unknown"
	if status, _, err := store.AcquireRecovery(unknown, 0, "lease", now); err != nil || status != StatusStale {
		t.Fatalf("acquire unknown status=%s err=%v", status, err)
	}
	if status, _, err := store.SettleRecovery(unknown, 0, "lease", OutcomeCompleteSuccess, now); err != nil || status != StatusStale {
		t.Fatalf("settle unknown status=%s err=%v", status, err)
	}
}

func w9dHash(t *testing.T, c Capability) string {
	t.Helper()
	hash, err := HashCapability(c)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestW9DRedisParameterArms(t *testing.T) {
	store, server := w9dRedis(t)
	ctx := context.Background()
	capability := testCapability()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	// Get / AdmitForeground：hash err。
	if _, _, err := store.Get(ctx, Capability{}); err == nil {
		t.Fatal("get empty capability must fail")
	}
	if _, _, _, err := store.AdmitForeground(ctx, Capability{}, "a"); err == nil {
		t.Fatal("admit empty capability must fail")
	}
	if _, _, _, err := store.AdmitForeground(ctx, capability, "  "); err == nil {
		t.Fatal("blank attempt id must fail")
	}
	// ClaimJ1Confirmation：identity 无效。
	if _, err := store.ClaimJ1Confirmation(ctx, "  ", 1); err == nil {
		t.Fatal("blank source account must fail")
	}
	// ListDue：limit 非法 / due 状态损坏。
	if _, err := store.ListDue(ctx, time.Now(), 0); err == nil {
		t.Fatal("zero limit must fail")
	}
	hash := w9dHash(t, capability)
	if err := store.client.Set(ctx, store.stateKey(hash), "{broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: 9_000, Member: hash}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, time.UnixMilli(10_000), 10); err == nil || !strings.Contains(err.Error(), "invalid key-model due state") {
		t.Fatalf("corrupt due state err=%v", err)
	}
	// RecordFailureIntent：incomplete / Open err / permit fence。
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "  "}); err == nil {
		t.Fatal("incomplete intent must fail")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i0", RequestID: "r0", AttemptID: "a0", ObservedAt: time.Now()}); err == nil {
		t.Fatal("invalid capability intent must fail open guard")
	}
	permit := &ForegroundPermit{CapabilityHash: "other-hash", AttemptID: "attempt"}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i1", RequestID: "r1", AttemptID: "a1", Capability: capability, ObservedAt: time.Now(), Permit: permit}); err == nil {
		t.Fatal("permit capability mismatch must fail")
	}
	badPermit := &ForegroundPermit{CapabilityHash: hash, AttemptID: "  "}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i2", RequestID: "r2", AttemptID: "a2", Capability: capability, ObservedAt: time.Now(), Permit: badPermit}); err == nil {
		t.Fatal("permit blank attempt must fail")
	}
	// 预置 receipt + 合法 state → Lua 幂等分支返回已存 state。
	existing, err := Open(capability, time.UnixMilli(1_000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeRedisState(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(existing.CapabilityHash), string(encoded), 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.key("receipt", "i3"), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	status, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i3", RequestID: "r3", AttemptID: "a3", Capability: capability, ObservedAt: time.Now()})
	if err != nil || status != StatusIdempotent {
		t.Fatalf("idempotent receipt status=%s err=%v", status, err)
	}
	// state revision 高于 intent revision → Lua stale 臂。
	higher := existing
	higher.DispatchRevision = capability.DispatchRevision + 1
	higherRaw, err := encodeRedisState(higher)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(existing.CapabilityHash), string(higherRaw), 0).Err(); err != nil {
		t.Fatal(err)
	}
	staleStatus, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i4", RequestID: "r4", AttemptID: "a4", Capability: capability, ObservedAt: time.Now()})
	if err != nil || staleStatus != StatusStale {
		t.Fatalf("stale status=%s err=%v", staleStatus, err)
	}
	// receipt 命中但 stateKey 为坏 JSON → 幂等返回值解码失败臂。
	if err := store.client.Set(ctx, store.stateKey(hash), "{broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.key("receipt", "i-bad"), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i-bad", RequestID: "r-bad", AttemptID: "a-bad", Capability: capability, ObservedAt: time.Now()}); err == nil {
		t.Fatal("idempotent decode failure must fail")
	}
	// AcquireRecovery：lease id 缺失。
	candidate := State{CapabilityHash: hash, Phase: PhaseOpen}
	if _, _, err := store.AcquireRecovery(ctx, candidate, "", false, false); err == nil {
		t.Fatal("missing lease id must fail")
	}
	// 关闭 Redis 后：get / admit / release / renew / failure / acquire / due script err。
	server.Close()
	if _, _, err := store.Get(ctx, capability); err == nil {
		t.Fatal("get after close must fail")
	}
	if _, err := store.ListDue(ctx, time.Now(), 10); err == nil {
		t.Fatal("list due after close must fail")
	}
	if _, _, _, err := store.AdmitForeground(ctx, capability, "after-close"); err == nil {
		t.Fatal("admit after close must fail")
	}
	if _, err := store.ReleaseForeground(ctx, ForegroundPermit{CapabilityHash: hash, AttemptID: "x"}); err == nil {
		t.Fatal("release after close must fail")
	}
	if _, _, err := store.RenewForeground(ctx, ForegroundPermit{CapabilityHash: hash, AttemptID: "x"}); err == nil {
		t.Fatal("renew after close must fail")
	}
	if _, _, err := store.RecordFailure(ctx, capability, time.Now(), "after-close"); err == nil {
		t.Fatal("failure after close must fail")
	}
	if _, _, err := store.AcquireRecovery(ctx, candidate, "lease", false, false); err == nil {
		t.Fatal("acquire after close must fail")
	}
}

func TestW9DOwnerGateArms(t *testing.T) {
	server := miniredis.RunT(t)
	defer server.Close()
	store, err := NewRedisStore("redis://"+server.Addr(), "w9d-gate", OwnerGate{Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.RenewForeground(ctx, ForegroundPermit{}); err == nil {
		t.Fatal("renew gate must fail")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{}); err == nil {
		t.Fatal("failure gate must fail")
	}
}

// ---------------------------------------------------------------------------
// runner mock
// ---------------------------------------------------------------------------

type w9dRunnerStore struct {
	nowErr       error
	due          []State
	loadErr      error
	inputs       []RecoveryInput
	renewOK      bool
	loadCalled   chan struct{}
	commitCalled chan struct{}
	renewCalled  chan struct{}
	closeOnce    sync.Once
}

func (s *w9dRunnerStore) ServerNow(context.Context) (time.Time, error) {
	if s.nowErr != nil {
		return time.Time{}, s.nowErr
	}
	return time.UnixMilli(1_000).UTC(), nil
}
func (s *w9dRunnerStore) ListDue(context.Context, time.Time, int) ([]State, error) {
	return append([]State(nil), s.due...), nil
}
func (s *w9dRunnerStore) AcquireRecovery(_ context.Context, candidate State, leaseID string, _, _ bool) (State, MutationStatus, error) {
	if leaseID == "" {
		return State{}, StatusLeaseMismatch, errors.New("lease")
	}
	next := candidate
	next.Phase = PhaseHalfOpen
	next.ProbeLease = &Lease{ID: leaseID, Until: time.Now().Add(time.Minute)}
	return next, StatusApplied, nil
}
func (s *w9dRunnerStore) RenewRecovery(context.Context, State, string) (bool, error) {
	s.closeOnce.Do(func() { close(s.renewCalled) })
	return s.renewOK, nil
}
func (s *w9dRunnerStore) CommitRecovery(context.Context, State, State, string) (MutationStatus, error) {
	s.closeOnce.Do(func() { close(s.commitCalled) })
	return StatusApplied, nil
}
func (s *w9dRunnerStore) Load(context.Context, string) ([]RecoveryInput, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.inputs, nil
}

func w9dWait(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

func TestW9DRunnerNilReceiver(t *testing.T) {
	var runner *Runner
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("nil runner must fail")
	}
}

func TestW9DRunnerCycleFailure(t *testing.T) {
	store := &w9dRunnerStore{nowErr: errors.New("redis down"), renewCalled: make(chan struct{}), commitCalled: make(chan struct{}), loadCalled: make(chan struct{})}
	runner, err := NewRunner(store, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cycle failure must surface")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestW9DRunnerCandidateArms(t *testing.T) {
	hash := w9dHash(t, testCapability())
	candidate := State{Capability: Capability{CredentialSourceAccountID: "acc", KeyFingerprint: "fp", ClientModel: "m", ClientEndpointFamily: "f", FinalUpstreamModel: "m", UpstreamEndpointMode: "chat", DispatchRevision: 1}, CapabilityHash: hash, Phase: PhaseOpen}
	// loader 失败 → settleUnknown → CommitRecovery。
	store := &w9dRunnerStore{loadErr: errors.New("loader down"), due: []State{candidate}, renewCalled: make(chan struct{}), commitCalled: make(chan struct{}), loadCalled: make(chan struct{})}
	runner, err := NewRunner(store, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	w9dWait(t, store.commitCalled, "settle unknown commit")

	// renewLease：续租失败 → cancel + lost。
	leaseStore := &w9dRunnerStore{renewOK: false, renewCalled: make(chan struct{}), commitCalled: make(chan struct{})}
	renewRunner, err := NewRunner(leaseStore, leaseStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	probeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := make(chan struct{}, 1)
	done := make(chan struct{})
	go renewRunner.renewLease(probeCtx, State{CapabilityHash: hash}, "lease", cancel, lost, done)
	select {
	case <-leaseStore.renewCalled:
	case <-time.After(12 * time.Second):
		t.Fatal("timeout waiting for renew attempt")
	}
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("lease loss was not signalled")
	}
}
