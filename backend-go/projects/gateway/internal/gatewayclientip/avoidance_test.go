package gatewayclientip

import (
	"context"
	"strconv"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newTestAvoidance(t *testing.T, mutate func(*AvoidanceOptions)) (*Avoidance, *manualClock) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	opts := AvoidanceOptions{Clock: clock, RuntimeStateDriver: RuntimeStateDriverMemory}
	if mutate != nil {
		mutate(&opts)
	}
	avoidance, err := NewAvoidance(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(avoidance.Close)
	return avoidance, clock
}

func account(id string, priority int, super, fallback bool) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{ID: id, Priority: priority, SuperPriorityEnabled: super, FallbackEnabled: fallback}
}

func TestAvoidanceScopeNormalizationTable(t *testing.T) {
	tests := []struct {
		name        string
		input       AvoidanceScopeInput
		wantScope   bool
		wantAPIKey  string
	}{
		{name: "blank ip drops scope", input: AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "   "}},
		{name: "empty ip drops scope", input: AvoidanceScopeInput{SystemAccountID: "sys", ClientIP: ""}},
		{name: "trims ip", input: AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: " key ", ClientIP: " 10.0.0.1 "}, wantScope: true, wantAPIKey: "key"},
		{name: "default api key", input: AvoidanceScopeInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"}, wantScope: true, wantAPIKey: "internal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			avoidance, _ := newTestAvoidance(t, nil)
			tracker := avoidance.CreateAvoidanceTracker(tc.input)
			if tc.wantScope != (tracker.Scope != nil) {
				t.Fatalf("scope=%+v", tracker.Scope)
			}
			if tracker.Scope != nil && tracker.Scope.apiKeyID != tc.wantAPIKey {
				t.Fatalf("apiKeyID=%q", tracker.Scope.apiKeyID)
			}
		})
	}
}

func TestAvoidanceConfirmAfterSuccessSkipsSuccessAccount(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	tracker := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"})
	avoidance.RememberPendingFailure(tracker, "a1", "A1", AccountFailure{ErrorPhase: "upstream_response"})
	avoidance.RememberPendingFailure(tracker, "a2", "A2", AccountFailure{ErrorPhase: "stream"})
	// 同一账户重复记录 → 覆盖而非新增。
	avoidance.RememberPendingFailure(tracker, "a1", "A1", AccountFailure{ErrorPhase: "stream"})
	if len(tracker.PendingFailures) != 2 {
		t.Fatalf("pending=%d want 2", len(tracker.PendingFailures))
	}
	result := avoidance.ConfirmAfterSuccess(tracker, "a2", nil)
	if result.Cleared || result.ClearedAccountID != "a2" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.ConfirmedAccountIDs) != 1 || result.ConfirmedAccountIDs[0] != "a1" {
		t.Fatalf("confirmed=%v", result.ConfirmedAccountIDs)
	}
	if len(tracker.PendingFailures) != 0 {
		t.Fatal("pending failures must be drained")
	}
	snapshot := avoidance.SnapshotForTest()
	if len(snapshot) != 1 || snapshot[0].AccountID != "a1" || snapshot[0].FailureCount != 1 || snapshot[0].Active {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAvoidanceActivationThresholdAndOrdering(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}
	tracker := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker, "bad", "Bad", AccountFailure{ErrorPhase: "upstream_request"})

	accounts := []gatewayruntimecache.OpenAIAccountSecret{account("bad", 5, false, false), account("good", 1, true, false)}
	// 第一次失败确认后 failureCount=1 < 2：不参与排序。
	result := avoidance.ConfirmAfterFinalFailure(tracker, nil)
	if len(result.ConfirmedAccountIDs) != 1 {
		t.Fatalf("confirmed=%v", result.ConfirmedAccountIDs)
	}
	ordered := avoidance.OrderAccountsByClientIPAccountAvoidance(accounts, scope, nil)
	if ordered.Applied || ordered.BypassedAllAvoided || len(ordered.AvoidedAccountIDs) != 0 {
		t.Fatalf("低于阈值不得规避: %+v", ordered)
	}

	// 第二次失败确认后 failureCount=2：规避生效。
	tracker2 := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker2, "bad", "Bad", AccountFailure{ErrorPhase: "upstream_request"})
	avoidance.ConfirmAfterFinalFailure(tracker2, nil)
	ordered = avoidance.OrderAccountsByClientIPAccountAvoidance(accounts, scope, nil)
	if !ordered.Applied || len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "bad" {
		t.Fatalf("ordered=%+v", ordered)
	}
	if ordered.BypassedAllAvoided {
		t.Fatal("fresh accounts exist, bypass must be false")
	}
	// 分层保持（preserveGatewayAccountDispatchPriorityTiers）：输出按 base
	// 的 tier 序还原。base=[bad(5,super off), good(1,super on)]，bad 的 tier
	// 在 base 中更早，因此 tier 保持后 bad 回到队首——fresh 前置只发生在
	// reordered 输入里。
	if ordered.Accounts[0].ID != "bad" || ordered.Accounts[1].ID != "good" {
		t.Fatalf("tier 保持应按 base tier 序输出: %s, %s", ordered.Accounts[0].ID, ordered.Accounts[1].ID)
	}
	// 对照：完全相同 tier（同 priority/super/fallback）下 fresh 才真正前置。
	sameTierAccounts := []gatewayruntimecache.OpenAIAccountSecret{account("bad", 5, false, false), account("good", 5, false, false)}
	orderedSame := avoidance.OrderAccountsByClientIPAccountAvoidance(sameTierAccounts, scope, nil)
	if orderedSame.Accounts[0].ID != "good" || orderedSame.Accounts[1].ID != "bad" {
		t.Fatalf("相同 tier 下 fresh 必须前置: %s, %s", orderedSame.Accounts[0].ID, orderedSame.Accounts[1].ID)
	}

	// 全部被规避 → bypassedAllAvoided。
	ordered = avoidance.OrderAccountsByClientIPAccountAvoidance(
		[]gatewayruntimecache.OpenAIAccountSecret{account("bad", 1, false, false)}, scope, nil)
	if ordered.Applied || !ordered.BypassedAllAvoided || len(ordered.Accounts) != 1 {
		t.Fatalf("ordered=%+v", ordered)
	}
}

func TestAvoidanceMemoryExpiryAndEviction(t *testing.T) {
	avoidance, clock := newTestAvoidance(t, nil)
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}
	tracker := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker, "a1", "", AccountFailure{ErrorPhase: "upstream_response"})
	avoidance.ConfirmAfterFinalFailure(tracker, nil)

	// 默认 TTL = 5min；默认 TTL 内仍活跃，之后过期。
	clock.advance(time.Duration(clientIPAccountAvoidanceDefaultTTL)*time.Millisecond - time.Millisecond)
	snapshot := avoidance.SnapshotForTest()
	if len(snapshot) != 1 {
		t.Fatalf("未过期前必须存在: %+v", snapshot)
	}
	clock.advance(2 * time.Millisecond)
	if snapshot := avoidance.SnapshotForTest(); len(snapshot) != 0 {
		t.Fatalf("过期后必须清空: %+v", snapshot)
	}

	// 上限 5000：插入 5001 条，最旧的被逐出。
	for i := 0; i < clientIPAccountAvoidanceMaxEntries+1; i += 1 {
		trackerI := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.1.0.1"})
		avoidance.RememberPendingFailure(trackerI, string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676%26)), "", AccountFailure{ErrorPhase: "upstream_response"})
		avoidance.ConfirmAfterFinalFailure(trackerI, nil)
	}
	if got := avoidance.entries.Len(); got > clientIPAccountAvoidanceMaxEntries {
		t.Fatalf("entries=%d 超过上限", got)
	}
}

func TestAvoidanceTTLFromSettingsClampsToMax(t *testing.T) {
	if got := avoidanceTTL(nil); got != 5*60_000 {
		t.Fatalf("default=%d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 1}); got != 60_000 {
		t.Fatalf("minutes=1 → %d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 0}); got != 60_000 {
		t.Fatalf("minutes=0 clamps to 1 → %d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 99}); got != 10*60_000 {
		t.Fatalf("minutes=99 clamps to max → %d", got)
	}
}

func TestAvoidancePendingFailureLimit(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	tracker := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"})
	for i := 0; i < clientIPAccountAvoidanceMaxPendingFailures+10; i += 1 {
		avoidance.RememberPendingFailure(tracker, "acct-"+strconv.Itoa(i), "", AccountFailure{ErrorPhase: "upstream_response"})
	}
	if len(tracker.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures {
		t.Fatalf("pending=%d want %d", len(tracker.PendingFailures), clientIPAccountAvoidanceMaxPendingFailures)
	}
	// transfer：源清空、目标合入。
	target := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"})
	avoidance.TransferPendingFailures(tracker, target)
	if len(tracker.PendingFailures) != 0 || len(target.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures {
		t.Fatalf("transfer pending=%d/%d", len(tracker.PendingFailures), len(target.PendingFailures))
	}
	// 无 scope 的 tracker 是 no-op。
	empty := &AvoidanceTracker{}
	avoidance.RememberPendingFailure(empty, "a", "", AccountFailure{ErrorPhase: "upstream_response"})
	if avoidance.ConfirmAfterSuccess(empty, "a", nil).Cleared {
		t.Fatal("scopeless tracker must be a no-op")
	}
}

func TestAvoidanceRedisModeMatchesMemoryContract(t *testing.T) {
	server := miniredis.RunT(t)
	redisAvoidance, err := NewAvoidance(AvoidanceOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(redisAvoidance.Close)
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}
	tracker := redisAvoidance.CreateAvoidanceTracker(scope)
	redisAvoidance.RememberPendingFailure(tracker, "a1", "A1", AccountFailure{ErrorPhase: "upstream_response", ErrorMessage: "boom"})
	if _, err := redisAvoidance.ConfirmAfterFinalFailureAsync(context.Background(), tracker, nil); err != nil {
		t.Fatal(err)
	}
	// Redis 中能看到 entry:key，且 scope key 与 memory 模式逐字节一致。
	memoryAvoidance, _ := newTestAvoidance(t, nil)
	wantScopeKey := avoidanceScopeKey(normalizeAvoidanceScope(scope))
	if !server.Exists("juhe-ai:dev:state:gateway-client-ip-account-avoidance:entry:" + wantScopeKey + ":a1") {
		t.Fatalf("redis key missing: %s", wantScopeKey+":a1")
	}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{account("a1", 1, false, false), account("a2", 2, false, false)}
	// 一次确认 → failureCount=1，不规避。
	ordered, err := redisAvoidance.OrderAccountsByClientIPAccountAvoidanceAsync(context.Background(), accounts, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordered.Applied {
		t.Fatalf("低于阈值不得规避: %+v", ordered)
	}
	// 再来一轮 → 规避生效。
	tracker2 := redisAvoidance.CreateAvoidanceTracker(scope)
	redisAvoidance.RememberPendingFailure(tracker2, "a1", "A1", AccountFailure{ErrorPhase: "upstream_response"})
	if _, err := redisAvoidance.ConfirmAfterFinalFailureAsync(context.Background(), tracker2, nil); err != nil {
		t.Fatal(err)
	}
	ordered, err = redisAvoidance.OrderAccountsByClientIPAccountAvoidanceAsync(context.Background(), accounts, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ordered.Applied || len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "a1" {
		t.Fatalf("ordered=%+v", ordered)
	}
	// tier 保持：a1 在 base 中 tier 更早，输出回到队首。
	if ordered.Accounts[0].ID != "a1" || ordered.Accounts[1].ID != "a2" {
		t.Fatalf("tier preservation broken: %s, %s", ordered.Accounts[0].ID, ordered.Accounts[1].ID)
	}
	// success 清除。
	tracker3 := redisAvoidance.CreateAvoidanceTracker(scope)
	redisAvoidance.RememberPendingFailure(tracker3, "a2", "", AccountFailure{ErrorPhase: "stream"})
	confirm, err := redisAvoidance.ConfirmAfterSuccessAsync(context.Background(), tracker3, "a1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !confirm.Cleared || confirm.ClearedAccountID != "a1" {
		t.Fatalf("confirm=%+v", confirm)
	}
	ordered, err = redisAvoidance.OrderAccountsByClientIPAccountAvoidanceAsync(context.Background(), accounts, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordered.Applied || ordered.BypassedAllAvoided {
		t.Fatalf("清掉 a1 后 a2 未达阈值: %+v", ordered)
	}
	_ = memoryAvoidance
}

func TestAvoidanceClearForAccount(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}
	tracker := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker, "a1", "", AccountFailure{ErrorPhase: "upstream_response"})
	avoidance.ConfirmAfterFinalFailure(tracker, nil)
	if !avoidance.ClearForAccount(tracker, "a1") {
		t.Fatal("clear must report existing entry")
	}
	if avoidance.ClearForAccount(tracker, "a1") {
		t.Fatal("second clear must report false")
	}
}
