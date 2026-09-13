package keymodelruntime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func wkRedisStore(t *testing.T, gate OwnerGate) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "juhe-ai:wk-redis", gate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, server
}

func TestNewRedisStoreValidation(t *testing.T) {
	gate := OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	if _, err := NewRedisStore("  ", "ns", gate); err == nil {
		t.Fatal("空 URL 必须被拒绝")
	}
	if _, err := NewRedisStore("redis://127.0.0.1:6379", "  ", gate); err == nil {
		t.Fatal("空 namespace 必须被拒绝")
	}
	if _, err := NewRedisStore("://bad-url", "ns", gate); err == nil {
		t.Fatal("非法 URL 必须被拒绝")
	}
	// 短 namespace 自动补 juhe-ai: 前缀。
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "short-ns", gate)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.prefix != "juhe-ai:short-ns:gateway-account-circuit-key-model" {
		t.Fatalf("前缀=%q", store.prefix)
	}
}

func TestRedisStoreGateFencesEveryOperation(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{})
	ctx := context.Background()
	capability := testCapability()
	if err := store.Ping(ctx); err == nil {
		t.Fatal("gate 未确认时 Ping 必须失败")
	}
	if _, err := store.ServerNow(ctx); err == nil {
		t.Fatal("gate 未确认时 ServerNow 必须失败")
	}
	if _, _, err := store.Get(ctx, capability); err == nil {
		t.Fatal("gate 未确认时 Get 必须失败")
	}
	if _, _, _, err := store.AdmitForeground(ctx, capability, "a"); err == nil {
		t.Fatal("gate 未确认时 AdmitForeground 必须失败")
	}
	if _, err := store.ReleaseForeground(ctx, ForegroundPermit{}); err == nil {
		t.Fatal("gate 未确认时 ReleaseForeground 必须失败")
	}
	if _, _, err := store.RenewForeground(ctx, ForegroundPermit{}); err == nil {
		t.Fatal("gate 未确认时 RenewForeground 必须失败")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{}); err == nil {
		t.Fatal("gate 未确认时 RecordFailureIntent 必须失败")
	}
	if _, err := store.ListDue(ctx, time.Now(), 1); err == nil {
		t.Fatal("gate 未确认时 ListDue 必须失败")
	}
	if err := store.RecordMainProbeFence(ctx, capability, "owner", time.Minute); err == nil {
		t.Fatal("gate 未确认时 RecordMainProbeFence 必须失败")
	}
	if _, err := store.ClearMainProbeFence(ctx, capability, "owner"); err == nil {
		t.Fatal("gate 未确认时 ClearMainProbeFence 必须失败")
	}
	if _, err := store.DeferMainProbeFence(ctx, capability, "owner", time.Minute); err == nil {
		t.Fatal("gate 未确认时 DeferMainProbeFence 必须失败")
	}
	if _, err := store.ClaimJ1Confirmation(ctx, "source", 1); err == nil {
		t.Fatal("gate 未确认时 ClaimJ1Confirmation 必须失败")
	}
	if _, _, err := store.AcquireRecovery(ctx, State{}, "l", false, false); err == nil {
		t.Fatal("gate 未确认时 AcquireRecovery 必须失败")
	}
	if _, err := store.RenewRecovery(ctx, State{}, "l"); err == nil {
		t.Fatal("gate 未确认时 RenewRecovery 必须失败")
	}
	if _, err := store.CommitRecovery(ctx, State{}, State{}, "l"); err == nil {
		t.Fatal("gate 未确认时 CommitRecovery 必须失败")
	}
	if _, err := store.CleanClosed(ctx, 1); err == nil {
		t.Fatal("gate 未确认时 CleanClosed 必须失败")
	}
	// Close 幂等：nil client / 已关闭连接不 panic。
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var nilStore *RedisStore
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if err := nilStore.Ping(ctx); err == nil {
		t.Fatal("nil store Ping 必须失败")
	}
}

func TestRedisStoreServerNowAndStoreGetFences(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	if _, err := store.ServerNow(ctx); err != nil {
		t.Fatalf("ServerNow: %v", err)
	}
	capability := testCapability()
	if _, ok, err := store.Get(ctx, testCapability()); ok || err != nil {
		t.Fatalf("缺失状态 Get: %v %v", ok, err)
	}
	if _, _, err := store.Get(ctx, Capability{}); err == nil {
		t.Fatal("非法 capability Get 必须失败")
	}
	// 写入后 Get 命中。
	if _, _, err := store.RecordFailure(ctx, capability, time.UnixMilli(1000).UTC(), "receipt-get"); err != nil {
		t.Fatal(err)
	}
	state, ok, err := store.Get(ctx, capability)
	if err != nil || !ok || state.Phase != PhaseOpen {
		t.Fatalf("Get: %v %+v %v", ok, state, err)
	}
	// 篡改 hash → fence mismatch。
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), `{"capabilityHash":"tampered","dispatchRevision":7}`, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, capability); err == nil {
		t.Fatal("fence mismatch 必须失败")
	}
	// 非法 JSON → 解码失败。
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), "{bad json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, capability); err == nil {
		t.Fatal("非法 JSON 必须失败")
	}
}

func TestRedisStoreListDueFiltersPhases(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	if _, err := store.ListDue(ctx, time.Now(), 0); err == nil {
		t.Fatal("limit<1 必须失败")
	}
	// 注入 due 成员：OPEN / RECOVERING / CLOSED / 缺失状态 / 坏 JSON。
	states := map[string]Phase{"open": PhaseOpen, "recovering": PhaseRecovering, "closed": PhaseClosed}
	for name, phase := range states {
		raw, err := encodeRedisState(State{CapabilityHash: name, Phase: phase, Generation: 1, LastObservedAt: time.UnixMilli(1000).UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.client.Set(ctx, store.stateKey(name), raw, 0).Err(); err != nil {
			t.Fatal(err)
		}
		if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: float64(1000), Member: name}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: 1000, Member: "missing"}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey("corrupt"), "{bad", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: 1000, Member: "corrupt"}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, time.UnixMilli(2000).UTC(), 10); err == nil {
		t.Fatal("坏 JSON 的 due 状态必须报错")
	}
	if err := store.client.ZRem(ctx, store.key("due"), "corrupt").Err(); err != nil {
		t.Fatal(err)
	}
	due, err := store.ListDue(ctx, time.UnixMilli(2000).UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due 应只含 OPEN/RECOVERING，缺失成员跳过: %+v", due)
	}
	for _, state := range due {
		if state.Phase != PhaseOpen && state.Phase != PhaseRecovering {
			t.Fatalf("due 含非可探测相位: %+v", state)
		}
	}
}

func TestRedisStoreForegroundRenewAndPermit(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	permit := ForegroundPermit{CapabilityHash: "missing-hash", AttemptID: "a"}
	if _, ok, err := store.RenewForeground(ctx, permit); ok || err != nil {
		t.Fatalf("缺失租约 renew: %v %v", ok, err)
	}
	decision, admitted, _, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("admit: %s %v", decision, err)
	}
	renewed, ok, err := store.RenewForeground(ctx, admitted)
	if err != nil || !ok {
		t.Fatalf("renew: %v %v", ok, err)
	}
	if renewed.CapabilityHash != admitted.CapabilityHash || renewed.AttemptID != "attempt-1" || renewed.LeaseUntil.Before(time.Now()) {
		t.Fatalf("续期租约不符: %+v", renewed)
	}
	// 释放后 renew 必须丢失。
	if released, err := store.ReleaseForeground(ctx, admitted); err != nil || !released {
		t.Fatalf("release: %v %v", released, err)
	}
	if _, ok, err := store.RenewForeground(ctx, admitted); ok || err != nil {
		t.Fatalf("释放后续期必须丢失: %v %v", ok, err)
	}
}

func TestRedisStoreMainProbeFenceLifecycle(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	if err := store.RecordMainProbeFence(ctx, capability, "", time.Minute); err == nil {
		t.Fatal("空 owner 必须失败")
	}
	if err := store.RecordMainProbeFence(ctx, capability, "owner-1", 0); err == nil {
		t.Fatal("非正租约必须失败")
	}
	if err := store.RecordMainProbeFence(ctx, Capability{}, "owner-1", time.Minute); err == nil {
		t.Fatal("非法 capability 必须失败")
	}
	if err := store.RecordMainProbeFence(ctx, capability, "owner-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	// defer 命中：owner 匹配。
	if ok, err := store.DeferMainProbeFence(ctx, capability, "owner-1", 30*time.Second); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if ok, err := store.DeferMainProbeFence(ctx, capability, "owner-2", 30*time.Second); err != nil || ok {
		t.Fatalf("defer owner 不匹配: %v %v", ok, err)
	}
	// clear 命中与未命中。
	if ok, err := store.ClearMainProbeFence(ctx, capability, "owner-2"); err != nil || ok {
		t.Fatalf("clear owner 不匹配: %v %v", ok, err)
	}
	if ok, err := store.ClearMainProbeFence(ctx, capability, "owner-1"); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	if ok, err := store.ClearMainProbeFence(ctx, capability, "owner-1"); err != nil || ok {
		t.Fatalf("重复 clear 必须未命中: %v %v", ok, err)
	}
	if _, err := store.ClearMainProbeFence(ctx, Capability{}, "owner-1"); err == nil {
		t.Fatal("非法 capability clear 必须失败")
	}
	if _, err := store.DeferMainProbeFence(ctx, Capability{}, "owner-1", time.Minute); err == nil {
		t.Fatal("非法 capability defer 必须失败")
	}
}

func TestRedisStoreClaimJ1Confirmation(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	if _, err := store.ClaimJ1Confirmation(ctx, "  ", 1); err == nil {
		t.Fatal("空 source 必须失败")
	}
	if _, err := store.ClaimJ1Confirmation(ctx, "source-1", 0); err == nil {
		t.Fatal("非正 revision 必须失败")
	}
	first, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || !first {
		t.Fatalf("首次认领: %v %v", first, err)
	}
	second, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || second {
		t.Fatalf("重复认领必须失败: %v %v", second, err)
	}
	other, err := store.ClaimJ1Confirmation(ctx, "source-1", 4)
	if err != nil || !other {
		t.Fatalf("新 revision 认领: %v %v", other, err)
	}
}

func TestRedisStoreRecoveryLeaseSemantics(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	if _, _, err := store.AcquireRecovery(ctx, State{}, "", false, false); err == nil {
		t.Fatal("空 lease id 必须失败")
	}
	if _, status, err := store.AcquireRecovery(ctx, State{CapabilityHash: "missing"}, "l", false, false); err != nil || status != StatusStale {
		t.Fatalf("缺失状态 acquire: %s %v", status, err)
	}
	// 记录失败并把 retryAt 改到过去。
	status, state, err := store.RecordFailure(ctx, capability, time.UnixMilli(1000).UTC(), "receipt-rec")
	if err != nil || status != StatusApplied {
		t.Fatalf("record: %s %v", status, err)
	}
	state.RetryAt = time.UnixMilli(1).UTC()
	encoded, err := encodeRedisState(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: 1, Member: state.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	// 存储态为 HALF_OPEN 时不可再 acquire → not_due。
	//（脚本的相位判断读取存储态而非候选参数。）
	halfOpenStored := state
	halfOpenStored.Phase = PhaseHalfOpen
	halfOpenRaw, err := encodeRedisState(halfOpenStored)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), halfOpenRaw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, status, err := store.AcquireRecovery(ctx, halfOpenStored, "l", false, false); err != nil || status != StatusNotDue {
		t.Fatalf("HALF_OPEN acquire: %s %v", status, err)
	}
	// 还原为 OPEN 供后续 acquire 使用。
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
	acquired, status, err := store.AcquireRecovery(ctx, state, "lease-1", true, true)
	if err != nil || status != StatusApplied || acquired.Phase != PhaseHalfOpen {
		t.Fatalf("acquire: %s %+v %v", status, acquired, err)
	}
	// 续期：正确租约 → true；错误租约 → false。
	if ok, err := store.RenewRecovery(ctx, acquired, "lease-1"); err != nil || !ok {
		t.Fatalf("renew: %v %v", ok, err)
	}
	if ok, err := store.RenewRecovery(ctx, acquired, "wrong"); err != nil || ok {
		t.Fatalf("wrong renew: %v %v", ok, err)
	}
	if ok, err := store.RenewRecovery(ctx, State{CapabilityHash: "no-such"}, "lease-1"); err != nil || ok {
		t.Fatalf("缺失状态 renew: %v %v", ok, err)
	}
	// 提交：错误租约 → stale；恢复完成 → CLOSED 入 closed 集合。
	if committed, err := store.CommitRecovery(ctx, acquired, acquired, "wrong"); err != nil || committed != StatusStale {
		t.Fatalf("wrong commit: %s %v", committed, err)
	}
	closed := acquired
	closed.Phase = PhaseClosed
	if committed, err := store.CommitRecovery(ctx, acquired, closed, "lease-1"); err != nil || committed != StatusApplied {
		t.Fatalf("commit closed: %s %v", committed, err)
	}
	if inClosed, err := store.client.ZScore(ctx, store.key("closed"), state.CapabilityHash).Result(); err != nil {
		t.Fatalf("closed 集合未收录: %v", err)
	} else if inClosed <= 0 {
		t.Fatalf("closed score=%v", inClosed)
	}
	// CleanClosed 边界。
	if _, err := store.CleanClosed(ctx, 0); err == nil {
		t.Fatal("limit 0 必须失败")
	}
	if _, err := store.CleanClosed(ctx, 1001); err == nil {
		t.Fatal("limit>1000 必须失败")
	}
	if _, err := store.CleanClosed(ctx, 100); err != nil {
		t.Fatalf("合法 CleanClosed: %v", err)
	}
}

func TestRedisStoreAdmissionBusyAndFailureIntentValidation(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	hash, err := HashCapability(capability)
	if err != nil {
		t.Fatal(err)
	}
	// 预填前台配额 → busy。
	for i := 0; i < ForegroundLimit; i++ {
		if err := store.client.ZAdd(ctx, store.admissionKey(hash), redis.Z{Score: float64(time.Now().Add(time.Hour).UnixMilli()), Member: "attempt-" + strconvItoa(i)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	decision, _, _, err := store.AdmitForeground(ctx, capability, "overflow")
	if err != nil || decision != ForegroundBusy {
		t.Fatalf("busy admit: %s %v", decision, err)
	}
	// RecordFailureIntent 入参校验。
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: " ", RequestID: "r", AttemptID: "a", Capability: capability, ObservedAt: time.Now().UTC()}); err == nil {
		t.Fatal("空 intent id 必须失败")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i", RequestID: "r", AttemptID: "a", Capability: capability}); err == nil {
		t.Fatal("零观测时间必须失败")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{IntentID: "i", RequestID: "r", AttemptID: "a", Capability: Capability{}}); err == nil {
		t.Fatal("非法 capability 必须失败")
	}
	// Permit 身份校验。
	wrongCapability := testCapability()
	wrongCapability.KeyFingerprint = "other"
	wrongHash, _ := HashCapability(wrongCapability)
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "i", RequestID: "r", AttemptID: "a", Capability: capability, ObservedAt: time.Now().UTC(),
		Permit: &ForegroundPermit{CapabilityHash: wrongHash, AttemptID: "a"},
	}); err == nil {
		t.Fatal("permit capability 不匹配必须失败")
	}
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "i", RequestID: "r", AttemptID: "a", Capability: capability, ObservedAt: time.Now().UTC(),
		Permit: &ForegroundPermit{CapabilityHash: hash, AttemptID: " "},
	}); err == nil {
		t.Fatal("空 permit attempt id 必须失败")
	}
	// 同 receipt 幂等重放，且 Permit 缺省时按 AttemptID 释放。
	if _, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "receipt-idem", RequestID: "r1", AttemptID: "att", Capability: capability, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	status, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "receipt-idem", RequestID: "r1", AttemptID: "att", Capability: capability, ObservedAt: time.Now().UTC(),
	})
	if err != nil || status != StatusIdempotent {
		t.Fatalf("幂等重放: %s %v", status, err)
	}
}

func TestRedisStoreDecodeStateOptionalFields(t *testing.T) {
	store, _ := wkRedisStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	capability := testCapability()
	hash, err := HashCapability(capability)
	if err != nil {
		t.Fatal(err)
	}
	// 含全部可选字段的存储态必须完整解码。
	full := State{Capability: capability, CapabilityHash: hash, Phase: PhaseHalfOpen, Generation: 2, RetryAt: time.UnixMilli(5000).UTC(), LastRecoverySuccessAt: time.UnixMilli(4000).UTC(), LastObservedAt: time.UnixMilli(3000).UTC(), LastOutcome: OutcomeCompleteSuccess, ProbeLease: &Lease{ID: "l", Until: time.UnixMilli(9000).UTC(), PriorSuccesses: 1}}
	raw, err := encodeRedisState(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(hash), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(ctx, capability)
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if got.RetryAt.IsZero() || got.LastRecoverySuccessAt.IsZero() || got.ProbeLease == nil || got.ProbeLease.ID != "l" || got.LastOutcome != OutcomeCompleteSuccess {
		t.Fatalf("可选字段解码不符: %+v", got)
	}
}
