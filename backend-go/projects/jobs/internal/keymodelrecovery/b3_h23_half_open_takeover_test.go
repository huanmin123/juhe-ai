package keymodelrecovery

// B3-H23 回归：HALF_OPEN 态的 45s 探测租约过期（lease 键消失）后可被新
// worker 接管——acquire 接受 HALF_OPEN，扫描层把「租约已过期」纳入 due；
// 未过期租约不可接管；旧 owner 的 Settle/Commit 因新 lease fence 被拒绝。

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestB3H23HalfOpenExpiredLeaseTakeover(t *testing.T) {
	store, mini, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	now, err := store.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := woOpenState(t, "b3h23-takeover")
	state.RetryAt = now.Add(-time.Second)
	seedState(t, ctx, client, keys, state)
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(state.RetryAt.UnixMilli()), Member: state.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}

	acquired, status, err := store.Acquire(ctx, state, "lease-old", false, false)
	if err != nil || status != Applied || acquired.Phase != HalfOpen || acquired.Lease == nil || acquired.Lease.ID != "lease-old" {
		t.Fatalf("首次领取: status=%s state=%#v err=%v", status, acquired, err)
	}

	// 租约未过期：不可接管，扫描层也不把该 HALF_OPEN 视为 due。
	if _, status, err := store.Acquire(ctx, acquired, "lease-peer", false, false); err != nil || status != NotDue {
		t.Fatalf("未过期租约必须拒绝接管: %s %v", status, err)
	}
	if states, err := store.ListDue(ctx, now, 10); err != nil || len(states) != 0 {
		t.Fatalf("未过期 HALF_OPEN 不得进入 due: %#v %v", states, err)
	}

	// 让 45s 探测租约自然过期（TTL 键随之消失，Redis 时钟前进）。
	advance := ProbeLease + time.Second
	mini.FastForward(advance)
	mini.SetTime(now.Add(advance))
	expiredNow, err := store.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states, err := store.ListDue(ctx, expiredNow, 10)
	if err != nil || len(states) != 1 || states[0].Phase != HalfOpen {
		t.Fatalf("过期 HALF_OPEN 必须进入 due: %#v %v", states, err)
	}

	// 旧 worker 在租约过期后结算必须被拒绝。
	if _, settlement := Settle(acquired, RecoveryResult{
		Generation: acquired.Generation, DispatchRevision: acquired.DispatchRevision,
		LeaseID: "lease-old", Outcome: CompleteSuccess, ObservedAt: expiredNow,
	}); settlement != Stale {
		t.Fatalf("旧 worker 过期结算必须 stale: %s", settlement)
	}

	// 新 worker 接管：新 lease，原 generation/dispatchRevision 不变。
	taken, status, err := store.Acquire(ctx, states[0], "lease-new", false, false)
	if err != nil || status != Applied {
		t.Fatalf("过期租约接管应 applied: %s %v", status, err)
	}
	if taken.Lease == nil || taken.Lease.ID != "lease-new" {
		t.Fatalf("接管后 lease=%#v", taken.Lease)
	}
	if taken.Generation != acquired.Generation || taken.DispatchRevision != acquired.DispatchRevision {
		t.Fatalf("接管不得改变 generation/revision: %#v", taken)
	}
	// 新 owner 的结算有效。
	next, settlement := Settle(taken, RecoveryResult{
		Generation: taken.Generation, DispatchRevision: taken.DispatchRevision,
		LeaseID: "lease-new", Outcome: CompleteSuccess, ObservedAt: expiredNow,
	})
	if settlement != Applied || next.Phase != Recovering || next.RecoverySuccessCount != 1 {
		t.Fatalf("新 owner 结算: %s %#v", settlement, next)
	}
	// 旧 owner 的 Commit 因 lease 不匹配被 Redis 拒绝。
	if status, err := store.Commit(ctx, acquired, next, "lease-old"); err != nil || status != Stale {
		t.Fatalf("旧 owner commit 必须 stale: %s %v", status, err)
	}
	if status, err := store.Commit(ctx, taken, next, "lease-new"); err != nil || status != Applied {
		t.Fatalf("新 owner commit 应 applied: %s %v", status, err)
	}
}

func TestB3H23SelectDueAndPureAcquireTakeover(t *testing.T) {
	now := time.Unix(60_000, 0).UTC()
	base := State{
		CapabilityKey: CapabilityKey{CredentialSourceAccountID: "src", KeyFingerprint: "fp", ClientModel: "m",
			ClientEndpointFamily: "chat", FinalUpstreamModel: "up", UpstreamEndpointMode: "chat_json", DispatchRevision: 3},
		CapabilityHash: "hash", Generation: 1, Phase: HalfOpen, RetryAt: now.Add(-time.Minute),
	}
	expired := base
	expired.Lease = &Lease{ID: "lease-old", Until: now.Add(-time.Second)}
	live := base
	live.Lease = &Lease{ID: "lease-live", Until: now.Add(time.Minute)}

	selected := SelectDue([]Due{
		{State: live, SourceID: "src"},
		{State: expired, SourceID: "src"},
	}, nil, now)
	if len(selected) != 1 || selected[0].State.Lease == nil || selected[0].State.Lease.ID != "lease-old" {
		t.Fatalf("SelectDue 应只纳入租约过期的 HALF_OPEN: %#v", selected)
	}

	if _, status := Acquire(live, live.Generation, live.DispatchRevision, "lease-peer", now); status != NotDue {
		t.Fatalf("未过期 HALF_OPEN 不得接管: %s", status)
	}
	taken, status := Acquire(expired, expired.Generation, expired.DispatchRevision, "lease-new", now)
	if status != Applied || taken.Phase != HalfOpen || taken.Lease == nil || taken.Lease.ID != "lease-new" {
		t.Fatalf("纯函数接管: %s %#v", status, taken)
	}
	if taken.Generation != expired.Generation {
		t.Fatalf("接管 generation 应保持不变: %d", taken.Generation)
	}
	// 旧 owner 结算：租约已过期 → Stale。
	if _, settlement := Settle(expired, RecoveryResult{
		Generation: expired.Generation, DispatchRevision: expired.DispatchRevision,
		LeaseID: "lease-old", Outcome: CompleteSuccess, ObservedAt: now,
	}); settlement != Stale {
		t.Fatalf("旧 owner 过期结算必须 stale: %s", settlement)
	}
}
