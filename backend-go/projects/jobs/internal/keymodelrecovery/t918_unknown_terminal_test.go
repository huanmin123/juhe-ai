package keymodelrecovery

// T91-8 / B3-H24 钉住测试：恢复探针连续 unknown 是「输入不可用」而非上游
// 结论，连续 12 次后必须终结为 CLOSED（reason=capability_not_found）并从 due
// 集合移除，不允许以 10s 节奏永久重试。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/redis/go-redis/v9"
)

// t918Loader 是可控的探测输入源：先返回空（capability 暂不可见），之后可替换
// 为匹配输入（capability 重新出现）。
type t918Loader struct {
	inputs []exactkeyprobe.Input
}

func (l *t918Loader) LoadAccount(context.Context, string) ([]exactkeyprobe.Input, error) {
	return l.inputs, nil
}

func t918DriveUnknown(t *testing.T, state State, attempts int, now time.Time) State {
	t.Helper()
	for attempt := 1; attempt <= attempts; attempt++ {
		now = state.RetryAt
		leaseID := fmt.Sprintf("lease-%d", attempt)
		acquired, status := Acquire(state, state.Generation, state.DispatchRevision, leaseID, now)
		if status != Applied {
			t.Fatalf("第 %d 次 Acquire status=%s", attempt, status)
		}
		next, settlement := Settle(acquired, RecoveryResult{
			Generation:       acquired.Generation,
			DispatchRevision: acquired.DispatchRevision,
			LeaseID:          leaseID,
			Outcome:          Unknown,
			ObservedAt:       now,
		})
		if settlement != Applied {
			t.Fatalf("第 %d 次 Settle status=%s", attempt, settlement)
		}
		state = next
	}
	return state
}

func TestT918ConsecutiveUnknownClosesAtThreshold(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	// 阈值前一次都保持 OPEN（无成功证据），计数逐个递增。
	for attempt := 1; attempt < UnknownAttemptsBeforeClose; attempt++ {
		state = t918DriveUnknown(t, state, 1, now)
		if state.Phase != Open {
			t.Fatalf("第 %d 次 unknown 后 phase=%s want OPEN", attempt, state.Phase)
		}
		if state.ConsecutiveUnknownCount != attempt {
			t.Fatalf("第 %d 次 unknown 后 count=%d", attempt, state.ConsecutiveUnknownCount)
		}
		if state.ClosedReason != "" {
			t.Fatalf("未达阈值不应写 closedReason: %q", state.ClosedReason)
		}
	}
	state = t918DriveUnknown(t, state, 1, now)
	if state.Phase != Closed {
		t.Fatalf("连续 %d 次 unknown 后 phase=%s want CLOSED", UnknownAttemptsBeforeClose, state.Phase)
	}
	if state.ClosedReason != ClosedReasonCapabilityNotFound {
		t.Fatalf("closedReason=%q want %q", state.ClosedReason, ClosedReasonCapabilityNotFound)
	}
	if !state.RetryAt.IsZero() || state.BackoffAttempt != 0 || state.RecoverySuccessCount != 0 {
		t.Fatalf("CLOSED 不应保留重试/退避/成功计数: %#v", state)
	}
	// CLOSED 不再可领取（不复活、不自旋）。
	if _, status := Acquire(state, state.Generation, state.DispatchRevision, "lease-after-close", time.Unix(9_000, 0).UTC()); status != NotDue {
		t.Fatalf("CLOSED Acquire status=%s want not_due", status)
	}
	// JSON 承载连续计数与终结原因，可跨进程读回。
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Phase != Closed || decoded.ClosedReason != ClosedReasonCapabilityNotFound || decoded.ConsecutiveUnknownCount != UnknownAttemptsBeforeClose {
		t.Fatalf("JSON 往返丢失终结事实: %#v", decoded)
	}
}

func TestT918DefinitiveOutcomeResetsUnknownCounter(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	state = t918DriveUnknown(t, state, 5, now)
	if state.ConsecutiveUnknownCount != 5 {
		t.Fatalf("count=%d want 5", state.ConsecutiveUnknownCount)
	}
	// upstream_not_complete 是上游结论：清零计数，保持既有退避语义。
	now = state.RetryAt
	acquired, status := Acquire(state, state.Generation, state.DispatchRevision, "lease-upstream", now)
	if status != Applied {
		t.Fatalf("Acquire status=%s", status)
	}
	next, settlement := Settle(acquired, RecoveryResult{Generation: acquired.Generation, DispatchRevision: acquired.DispatchRevision, LeaseID: "lease-upstream", Outcome: UpstreamNotComplete, ObservedAt: now})
	if settlement != Applied || next.ConsecutiveUnknownCount != 0 || next.Phase != Open {
		t.Fatalf("upstream_not_complete 未清零计数: %#v", next)
	}
	state = next

	// complete_success 同样清零。
	state = t918DriveUnknown(t, state, 4, now)
	if state.ConsecutiveUnknownCount != 4 {
		t.Fatalf("count=%d want 4", state.ConsecutiveUnknownCount)
	}
	now = state.RetryAt
	acquired, status = Acquire(state, state.Generation, state.DispatchRevision, "lease-success", now)
	if status != Applied {
		t.Fatalf("Acquire status=%s", status)
	}
	next, settlement = Settle(acquired, RecoveryResult{Generation: acquired.Generation, DispatchRevision: acquired.DispatchRevision, LeaseID: "lease-success", Outcome: CompleteSuccess, ObservedAt: now})
	if settlement != Applied || next.ConsecutiveUnknownCount != 0 || next.Phase != Recovering {
		t.Fatalf("complete_success 未清零计数: %#v", next)
	}
	// 计数从 0 重新累积：再 11 次 unknown 不终结，第 12 次才终结。
	state = t918DriveUnknown(t, next, UnknownAttemptsBeforeClose-1, now)
	if state.Phase == Closed {
		t.Fatalf("计数未在成功证据后重新归零，提前终结: %#v", state)
	}
	state = t918DriveUnknown(t, state, 1, now)
	if state.Phase != Closed {
		t.Fatalf("重新累积到阈值未终结: %#v", state)
	}
}

func TestT918LegacyStateWithoutUnknownFieldsReadsClean(t *testing.T) {
	// 旧 state JSON（无 consecutiveUnknownCount / closedReason）必须可正常
	// 读取，并按 0 计数继续累积。
	raw := `{"credentialSourceAccountId":"source-a","keyFingerprint":"fingerprint-a","clientModel":"B","clientEndpointFamily":"chat","finalUpstreamModel":"b-upstream","upstreamEndpointMode":"chat_json","dispatchRevision":9,"capabilityHash":"legacy-hash","generation":1,"phase":"OPEN","backoffAttempt":1,"recoverySuccessCount":0,"lastObservedAtMs":1000000,"lastOutcome":"unknown"}`
	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("旧 state 解码失败: %v", err)
	}
	if state.ConsecutiveUnknownCount != 0 || state.ClosedReason != "" {
		t.Fatalf("旧 state 缺省值不符: count=%d reason=%q", state.ConsecutiveUnknownCount, state.ClosedReason)
	}
	if state.Phase != Open || state.Generation != 1 || state.CapabilityHash != "legacy-hash" {
		t.Fatalf("旧 state 其它字段丢失: %#v", state)
	}
	state.RetryAt = time.Unix(2_000, 0).UTC()
	state = t918DriveUnknown(t, state, UnknownAttemptsBeforeClose, time.Time{})
	if state.Phase != Closed || state.ClosedReason != ClosedReasonCapabilityNotFound {
		t.Fatalf("旧 state 未按新阈值终结: %#v", state)
	}
}

// TestT918RedisDueRemovalAndClosedLedger 用真实 Lua（miniredis）锁定终结路径
// 的 Redis 副作用：CLOSED 后从 due ZSET 移除、进入 closed 保留账本、state
// JSON 带 closedReason，且后续 Acquire 不再领取。
func TestT918RedisDueRemovalAndClosedLedger(t *testing.T) {
	store, _, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	now, err := store.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 时间基准取过去（服务器 TIME 恒晚于它），避免依赖 miniredis 虚拟时钟。
	base := now.Add(-time.Hour)
	state, err := NewOpen(key(), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(base.UnixMilli()), Member: state.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := seedStateAt(ctx, client, keys, state); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= UnknownAttemptsBeforeClose; attempt++ {
		observedAt := base.Add(time.Duration(attempt) * (RecoveryProbeInterval + time.Second))
		candidate, err := store.ListDue(ctx, observedAt.Add(time.Millisecond), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidate) != 1 || candidate[0].CapabilityHash != state.CapabilityHash {
			t.Fatalf("第 %d 轮 due 读取不符: %#v", attempt, candidate)
		}
		leaseID := fmt.Sprintf("redis-lease-%d", attempt)
		acquired, status, err := store.Acquire(ctx, candidate[0], leaseID, false, false)
		if err != nil || status != Applied {
			t.Fatalf("第 %d 轮 Acquire status=%s err=%v", attempt, status, err)
		}
		next, settlement := Settle(acquired, RecoveryResult{
			Generation:       acquired.Generation,
			DispatchRevision: acquired.DispatchRevision,
			LeaseID:          leaseID,
			Outcome:          Unknown,
			ObservedAt:       observedAt,
		})
		if settlement != Applied {
			t.Fatalf("第 %d 轮 Settle status=%s", attempt, settlement)
		}
		if status, err := store.Commit(ctx, acquired, next, leaseID); err != nil || status != Applied {
			t.Fatalf("第 %d 轮 Commit status=%s err=%v", attempt, status, err)
		}
	}
	// state 落库为 CLOSED + capability_not_found。
	raw, err := client.Get(ctx, keys.State(state.CapabilityHash)).Result()
	if err != nil {
		t.Fatal(err)
	}
	var persisted State
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Phase != Closed || persisted.ClosedReason != ClosedReasonCapabilityNotFound {
		t.Fatalf("Redis state 未终结: %#v", persisted)
	}
	if persisted.ConsecutiveUnknownCount != UnknownAttemptsBeforeClose {
		t.Fatalf("Redis state 连续 unknown 计数=%d", persisted.ConsecutiveUnknownCount)
	}
	// 从 due 集合移除（不再 10s 自旋）。
	if score, err := client.ZScore(ctx, keys.Due(), state.CapabilityHash).Result(); err != redis.Nil {
		t.Fatalf("CLOSED 仍留在 due 集合: score=%v err=%v", score, err)
	}
	// 进入 closed 保留账本（供 CleanClosed 延迟清理）。
	score, err := client.ZScore(ctx, keys.Closed(), state.CapabilityHash).Result()
	if err != nil || math.IsNaN(score) {
		t.Fatalf("CLOSED 未进入 closed 账本: score=%v err=%v", score, err)
	}
	// CLOSED 不再可领取。
	if _, status, err := store.Acquire(ctx, persisted, "lease-after-close", false, false); err != nil || status != NotDue {
		t.Fatalf("CLOSED Acquire status=%s err=%v", status, err)
	}
}

func seedStateAt(ctx context.Context, client *redis.Client, keys RedisKeys, state State) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return client.Set(ctx, keys.State(state.CapabilityHash), encoded, 0).Err()
}

// TestT918ProbeInputDiscoveryResetsUnknownCounter 端到端覆盖「中途找到输入则
// 计数清零」：探针输入连续缺失时 executeProbe 返回 unknown 并累加计数；输入
// 重新出现后探针得到确定结论，计数归零、不进入终结。
func TestT918ProbeInputDiscoveryResetsUnknownCounter(t *testing.T) {
	now := time.Unix(5_000, 0).UTC()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	loader := &t918Loader{}
	runner := &Runner{loader: loader, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), probe: func(context.Context, State, exactkeyprobe.Input) Outcome {
		return CompleteSuccess
	}}
	for attempt := 1; attempt <= 5; attempt++ {
		now = state.RetryAt
		acquired, status := Acquire(state, state.Generation, state.DispatchRevision, fmt.Sprintf("probe-%d", attempt), now)
		if status != Applied {
			t.Fatalf("第 %d 轮 Acquire status=%s", attempt, status)
		}
		outcome := runner.executeProbe(context.Background(), acquired)
		if outcome != Unknown {
			t.Fatalf("第 %d 轮无匹配输入必须返回 unknown，得到 %s", attempt, outcome)
		}
		next, settlement := Settle(acquired, RecoveryResult{
			Generation: acquired.Generation, DispatchRevision: acquired.DispatchRevision,
			LeaseID: fmt.Sprintf("probe-%d", attempt), Outcome: outcome, ObservedAt: now,
		})
		if settlement != Applied {
			t.Fatalf("第 %d 轮 Settle status=%s", attempt, settlement)
		}
		state = next
	}
	if state.ConsecutiveUnknownCount != 5 {
		t.Fatalf("输入缺失未知计数=%d want 5", state.ConsecutiveUnknownCount)
	}
	// capability 重新出现：探针输入命中，得到确定结论 → 计数清零。
	loader.inputs = []exactkeyprobe.Input{{AccountID: key().CredentialSourceAccountID, DispatchRevision: key().DispatchRevision}}
	now = state.RetryAt
	acquired, status := Acquire(state, state.Generation, state.DispatchRevision, "probe-found", now)
	if status != Applied {
		t.Fatalf("输入出现后 Acquire status=%s", status)
	}
	outcome := runner.executeProbe(context.Background(), acquired)
	if outcome != CompleteSuccess {
		t.Fatalf("输入出现后探针结论=%s want complete_success", outcome)
	}
	next, settlement := Settle(acquired, RecoveryResult{Generation: acquired.Generation, DispatchRevision: acquired.DispatchRevision, LeaseID: "probe-found", Outcome: outcome, ObservedAt: now})
	if settlement != Applied {
		t.Fatalf("Settle status=%s", settlement)
	}
	if next.ConsecutiveUnknownCount != 0 || next.Phase != Recovering {
		t.Fatalf("找到输入后计数必须清零: %#v", next)
	}
}
