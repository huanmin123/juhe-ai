package gatewaysession

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestSessionSlotCapFormula(t *testing.T) {
	cases := []struct {
		concurrency int
		rpm         int
		want        int
	}{
		{10, 60, 32},     // min(40,30) -> floor 32
		{10, 600, 40},    // min(40,300)
		{100, 600, 300},  // min(400,300)
		{100, 0, 400},    // no RPM bound
		{1, 0, 32},       // floor
		{5000, 0, 10000}, // ceiling
	}
	for _, tc := range cases {
		if got := SessionSlotCap(tc.concurrency, tc.rpm); got != tc.want {
			t.Fatalf("SessionSlotCap(%d,%d) = %d, want %d", tc.concurrency, tc.rpm, got, tc.want)
		}
	}
}

func slotKeyForTest(t *testing.T, service *AffinityService, accountID string) string {
	t.Helper()
	key, err := sessionAffinitySlotKey(service.cfg.RedisNamespace, accountID)
	if err != nil {
		t.Fatalf("slot key: %v", err)
	}
	return key
}

func seedSessionSlots(t *testing.T, service *AffinityService, accountID string, members int, baseScore int64) {
	t.Helper()
	client, err := service.redisSessionAffinityClient(context.Background())
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	key := slotKeyForTest(t, service, accountID)
	for index := 0; index < members; index++ {
		if _, err := client.SendCommand(context.Background(), "ZADD", key,
			strconv.FormatInt(baseScore+int64(index), 10), "sess-"+strconv.Itoa(index)); err != nil {
			t.Fatalf("seed slot: %v", err)
		}
	}
}

func TestPlanSessionSlotOwnerActive(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_owner_active"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.ConfirmSessionSlot(ctx, key, "", "a", 32) {
		t.Fatal("confirm slot failed")
	}
	plan, err := service.PlanSessionSlot(ctx, key, "a", true, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 60},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 60},
	}, 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "a" || !plan.SlotActive || plan.Fallback {
		t.Fatalf("plan = %+v, want owner active", plan)
	}
}

func TestPlanSessionSlotWaitsForOwnerRelease(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_wait_release"
	capA := SessionSlotCap(10, 0)
	seedSessionSlots(t, service, "a", capA, service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	go func() {
		time.Sleep(50 * time.Millisecond)
		client, _ := service.redisSessionAffinityClient(ctx)
		_, _ = client.SendCommand(ctx, "ZREM", slotKeyForTest(t, service, "a"), "sess-0")
	}()
	plan, err := service.PlanSessionSlot(ctx, key, "a", true, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 0},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 0},
	}, 400)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "a" || plan.Fallback {
		t.Fatalf("plan = %+v, want owner after slot release", plan)
	}
	if plan.WaitedMs < 200 {
		t.Fatalf("waitedMs = %d, want >=200", plan.WaitedMs)
	}
}

func TestPlanSessionSlotFallsBackToFreeAccount(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_fallback"
	capA := SessionSlotCap(10, 0)
	seedSessionSlots(t, service, "a", capA, service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	plan, err := service.PlanSessionSlot(ctx, key, "a", true, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 0},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 0},
	}, 100)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "b" || !plan.Fallback {
		t.Fatalf("plan = %+v, want fallback b", plan)
	}
	if plan.WaitedMs != 100 {
		t.Fatalf("waitedMs = %d, want 100", plan.WaitedMs)
	}
}

func TestPlanSessionSlotStopsWaitingWhenConcurrentRequestReacquiresSlot(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_wait_active"
	capA := SessionSlotCap(10, 0)
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "a", nil) {
		t.Fatal("rebind failed")
	}
	seedSessionSlots(t, service, "a", capA, service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = service.ConfirmSessionSlot(ctx, key, "", "a", capA)
	}()
	plan, err := service.PlanSessionSlot(ctx, key, "a", true, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 0},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 0},
	}, 800)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "a" || plan.Fallback {
		t.Fatalf("plan = %+v, want owner after concurrent slot reacquire", plan)
	}
	if plan.WaitedMs >= 800 {
		t.Fatalf("waitedMs = %d, want early return after active slot", plan.WaitedMs)
	}
}

func TestPlanSessionSlotTouchesActiveSlotForLRUProtection(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_touch"
	slotKey := slotKeyForTest(t, service, "a")
	nowMs := service.clock().UnixMilli()
	client, err := service.redisSessionAffinityClient(ctx)
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	if _, err := client.SendCommand(ctx, "ZADD", slotKey, strconv.FormatInt(nowMs+1, 10), key); err != nil {
		t.Fatalf("seed active slot: %v", err)
	}
	plan, err := service.PlanSessionSlot(ctx, key, "a", true, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10},
	}, 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "a" || !plan.SlotActive {
		t.Fatalf("plan = %+v, want active owner", plan)
	}
	score, err := mr.ZScore(slotKey, key)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if score != float64(nowMs+sessionAffinityLeaseTtlMs) {
		t.Fatalf("touched score = %v, want %d", score, nowMs+sessionAffinityLeaseTtlMs)
	}
}

func TestPlanSessionSlotInactiveOwnerSkipsWait(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	seedSessionSlots(t, service, "a", SessionSlotCap(10, 0), service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	plan, err := service.PlanSessionSlot(ctx, "aff_slot_inactive_owner", "a", false, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 0},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 0},
	}, 400)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "b" || !plan.Fallback || plan.WaitedMs != 0 {
		t.Fatalf("plan = %+v, want immediate fallback b without wait", plan)
	}
}

func TestPlanSessionSlotNewSessionSkipsFullAccountWithoutFallback(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	seedSessionSlots(t, service, "a", SessionSlotCap(10, 0), service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	plan, err := service.PlanSessionSlot(ctx, "aff_slot_new_session", "", false, []SessionSlotCandidate{
		{AccountID: "a", ConcurrencyLimit: 10, RPMLimit: 0},
		{AccountID: "b", ConcurrencyLimit: 10, RPMLimit: 0},
	}, 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PreferredAccountID != "b" || plan.Fallback {
		t.Fatalf("plan = %+v, want b without fallback", plan)
	}
}

func TestClaimWithStateReportsEstablishedBinding(t *testing.T) {
	service, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_claim_state"
	owner, established, ok := service.ClaimOpenAIAccountForSessionWithStateAsync(ctx, key, "a", nil)
	if !ok || owner != "a" || established {
		t.Fatalf("first claim = (%q, established=%v, ok=%v), want (a,false,true)", owner, established, ok)
	}
	// 零 lease 占位（首次请求未最终成功）再次 claim 仍不算既有会话。
	owner, established, ok = service.ClaimOpenAIAccountForSessionWithStateAsync(ctx, key, "b", nil)
	if !ok || owner != "a" || established {
		t.Fatalf("zero-lease re-claim = (%q, established=%v, ok=%v), want (a,false,true)", owner, established, ok)
	}
	// 模拟一次最终成功刷新 lease 后才算既有会话。
	if !service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "a", nil) {
		t.Fatal("success rebind failed")
	}
	owner, established, ok = service.ClaimOpenAIAccountForSessionWithStateAsync(ctx, key, "b", nil)
	if !ok || owner != "a" || !established {
		t.Fatalf("second claim = (%q, established=%v, ok=%v), want (a,true,true)", owner, established, ok)
	}
}

func TestForgetSessionAffinityReleasesSlot(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_forget"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.ConfirmSessionSlot(ctx, key, "", "a", 32) {
		t.Fatal("confirm slot failed")
	}
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if score, err := mr.ZScore(slotKeyForTest(t, service, "a"), key); err == nil && score > 0 {
		t.Fatal("forget must release the session slot")
	}
}

func TestMigrateSessionAffinityReleasesSourceSlot(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_migrate"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.ConfirmSessionSlot(ctx, key, "", "a", 32) {
		t.Fatal("confirm slot failed")
	}
	result, err := service.MigrateOpenAIAccountSessionAffinityAsync(ctx, "a", "b", nil, MigrationOptions{})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.MigratedSessionCount != 1 {
		t.Fatalf("migrated = %d, want 1", result.MigratedSessionCount)
	}
	if score, err := mr.ZScore(slotKeyForTest(t, service, "a"), key); err == nil && score > 0 {
		t.Fatal("migration must release the source-account slot")
	}
}

func TestEvictedSlotCleanupDoesNotDeleteNewOwnerBinding(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_evict_cleanup"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, key, "b", "b", nil) {
		t.Fatal("rebind failed")
	}
	bindingKey, err := redisSessionAffinityBindingKey(service.cfg.RedisNamespace, key)
	if err != nil {
		t.Fatalf("binding key: %v", err)
	}
	// 清理任务来自账户 a 的驱逐；当前 binding 已属于 b，不得删除。
	service.cleanupEvictedSessionSlotBindingAsync(ctx, key, "a")
	time.Sleep(100 * time.Millisecond)
	if _, err := mr.Get(bindingKey); err != nil {
		t.Fatal("cleanup for evicted account a must not delete owner b binding")
	}
	// 来源账户匹配时才删除。
	service.cleanupEvictedSessionSlotBindingAsync(ctx, key, "b")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := mr.Get(bindingKey); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup for evicted account b did not delete binding")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConfirmSessionSlotEvictionCleansBinding(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	evicted := "sess-0"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, evicted, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, evicted, "a", "a", nil) {
		t.Fatal("rebind failed")
	}
	seedSessionSlots(t, service, "a", 31, service.clock().UnixMilli()+sessionAffinityLeaseTtlMs+10)
	client, err := service.redisSessionAffinityClient(ctx)
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	slotKey := slotKeyForTest(t, service, "a")
	// 让 sess-0 成为最久未成功成员，并补足 cap。
	if _, err := client.SendCommand(ctx, "ZADD", slotKey, strconv.FormatInt(service.clock().UnixMilli()+1, 10), evicted); err != nil {
		t.Fatalf("lower score: %v", err)
	}
	if _, err := client.SendCommand(ctx, "ZADD", slotKey, strconv.FormatInt(service.clock().UnixMilli()+sessionAffinityLeaseTtlMs+1000, 10), "sess-extra"); err != nil {
		t.Fatalf("fill slot: %v", err)
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_slot_evict_binding", "a", nil); !ok {
		t.Fatal("claim new session failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, "aff_slot_evict_binding", "a", "a", nil) {
		t.Fatal("rebind new session failed")
	}
	if !service.ConfirmSessionSlot(ctx, "aff_slot_evict_binding", "", "a", 32) {
		t.Fatal("confirm slot failed")
	}
	bindingKey, err := redisSessionAffinityBindingKey(service.cfg.RedisNamespace, evicted)
	if err != nil {
		t.Fatalf("binding key: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := mr.Get(bindingKey); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("evicted session binding was not cleaned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConfirmSessionSlotSkipsWhenBindingOwnerDiffers(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	key := "aff_slot_binding_gate"
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, key, "a", "a", nil) {
		t.Fatal("rebind failed")
	}
	if !service.ConfirmSessionSlot(ctx, key, "", "a", 32) {
		t.Fatal("confirm a failed")
	}
	// 未重绑就声称成功账户是 b：binding 仍是 a，名额不得迁移。
	if service.ConfirmSessionSlot(ctx, key, "a", "b", 32) {
		t.Fatal("confirm b without binding rebind must be a no-op")
	}
	if score, err := mr.ZScore(slotKeyForTest(t, service, "a"), key); err != nil || score <= 0 {
		t.Fatal("owner a slot must remain")
	}
	if score, err := mr.ZScore(slotKeyForTest(t, service, "b"), key); err == nil && score > 0 {
		t.Fatal("non-owner b must not receive a slot")
	}
}

func TestConfirmSessionSlotEvictsOldest(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	seedSessionSlots(t, service, "a", 32, service.clock().UnixMilli()+sessionAffinityLeaseTtlMs)
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_slot_new", "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, "aff_slot_new", "a", "a", nil) {
		t.Fatal("rebind failed")
	}
	if !service.ConfirmSessionSlot(ctx, "aff_slot_new", "", "a", 32) {
		t.Fatal("confirm slot failed")
	}
	key := slotKeyForTest(t, service, "a")
	if score, err := mr.ZScore(key, "aff_slot_new"); err != nil || score <= 0 {
		t.Fatalf("new member missing: score=%v err=%v", score, err)
	}
	members, err := mr.ZMembers(key)
	if err != nil {
		t.Fatalf("zmembers: %v", err)
	}
	if len(members) != 32 {
		t.Fatalf("slot count = %d, want 32", len(members))
	}
	if score, err := mr.ZScore(key, "sess-0"); err == nil && score > 0 {
		t.Fatal("oldest member sess-0 should be evicted")
	}
}

func TestConfirmSessionSlotReleasesOldOwner(t *testing.T) {
	service, _, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, "aff_slot_move", "a", nil); !ok {
		t.Fatal("claim failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, "aff_slot_move", "a", "a", nil) {
		t.Fatal("rebind a failed")
	}
	if !service.ConfirmSessionSlot(ctx, "aff_slot_move", "", "a", 32) {
		t.Fatal("confirm a failed")
	}
	if !service.RebindOpenAIAccountForSessionAsync(ctx, "aff_slot_move", "a", "b", nil) {
		t.Fatal("rebind b failed")
	}
	if !service.ConfirmSessionSlot(ctx, "aff_slot_move", "a", "b", 32) {
		t.Fatal("confirm b failed")
	}
	if _, err := mr.ZScore(slotKeyForTest(t, service, "a"), "aff_slot_move"); err == nil {
		t.Fatal("old owner slot must be released")
	}
	if _, err := mr.ZScore(slotKeyForTest(t, service, "b"), "aff_slot_move"); err != nil {
		t.Fatal("new owner slot must exist")
	}
}
