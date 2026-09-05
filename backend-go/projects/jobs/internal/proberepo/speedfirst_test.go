package proberepo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestSpeedFirstKeysMatchNodeContract 锁定 Redis 键形状（与 Node
// normal-route-latency-degradation.service.ts 逐段一致）。
func TestSpeedFirstKeysMatchNodeContract(t *testing.T) {
	store, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{
		Enabled: true, URL: "redis://127.0.0.1:6379/9", Namespace: "test-space",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scope := speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1"}
	stateKey := stateKeyFor(scope, "acc-1")
	if want := "v1:sys-1:strategy-1:group-1:acc-1"; stateKey != want {
		t.Fatalf("stateKey=%q want %q", stateKey, want)
	}
	if got, want := store.redisKey(stateKey), "juhe-ai:test-space:state:gateway-normal-route-latency-degradation:v1:sys-1:strategy-1:group-1:acc-1"; got != want {
		t.Fatalf("redisKey=%q want %q", got, want)
	}
	if store.generationKey() != "v1:generation" || store.probeIndexKey() != "v1:probe-index" || store.allIndexKey() != "v1:all-index" {
		t.Fatal("generation/index 子键形状错误")
	}
	candidate := opsjobs.ProbeCandidate{StateKey: stateKey, Generation: "gen-1"}
	if want := "v1:probe-claim:gen-1:" + stateKey; store.probeClaimLockKey(candidate) != want {
		t.Fatalf("claim key=%q", store.probeClaimLockKey(candidate))
	}
	if want := "v1:mutation-lock:" + stateKey; store.mutationLockKey(stateKey) != want {
		t.Fatalf("mutation lock key=%q", store.mutationLockKey(stateKey))
	}
}

// TestSpeedFirstGenerationToken 锁定 generation token 形状
// （Node：JSON.stringify([publishedAtMs, version])）。
func TestSpeedFirstGenerationToken(t *testing.T) {
	token, err := generationToken(speedFirstInitialGenerationEvent)
	if err != nil {
		t.Fatal(err)
	}
	if token != `[0,"initial"]` {
		t.Fatalf("token=%q", token)
	}
}

// TestSpeedFirstCandidateMatchesState 验证 candidate-match 围栏字段集。
func TestSpeedFirstCandidateMatchesState(t *testing.T) {
	roundAttempts, roundSuccesses := 1, 0
	degradedUntil := int64(1000)
	nextProbeAt := int64(2000)
	state := &speedFirstState{
		Generation:                     "gen-1",
		AccountID:                      "acc-1",
		RuntimeKey:                     "acc-1",
		Scope:                          speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1"},
		DegradedUntilMS:                &degradedUntil,
		RecoveryProbeRoundAttemptCount: &roundAttempts,
		RecoveryProbeRoundSuccessCount: &roundSuccesses,
		NextProbeAtMS:                  &nextProbeAt,
	}
	candidate := state.candidate()
	candidate.StateKey = stateKeyFor(state.Scope, state.RuntimeKey)
	if !candidateMatchesState(candidate, state) {
		t.Fatal("同源候选必须匹配")
	}
	nextProbeAt2 := int64(3000)
	state.NextProbeAtMS = &nextProbeAt2
	if candidateMatchesState(candidate, state) {
		t.Fatal("nextProbeAt 变化后不得匹配（Node latencyProbeCandidateMatchesState）")
	}
	state.NextProbeAtMS = &nextProbeAt
	state.DegradationEventID = "event-1"
	if candidateMatchesState(candidate, state) {
		t.Fatal("degradationEventId 变化后不得匹配")
	}
}

// TestSpeedFirstStateJSONRoundTrip 锁定 state JSON 形状（Node camelCase 键）。
func TestSpeedFirstStateJSONRoundTrip(t *testing.T) {
	roundAttempts, roundSuccesses := 1, 0
	degradedUntil := int64(1000)
	nextProbeAt := int64(2000)
	state := speedFirstState{
		Generation:                     "gen-1",
		AccountID:                      "acc-1",
		AccountName:                    "账户一",
		RuntimeKey:                     "acc-1",
		Scope:                          speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1"},
		Config:                         speedFirstConfig{SlowTriggerCount: 3, SlowWindowSeconds: 120, RecoverySuccessCount: 2, ProbeIntervalSeconds: 30, DegradedTTLSeconds: 600, MaxFirstByteRetriesPerReq: 1, FirstByteDeadlineMS: 2000},
		FirstSlowAtMS:                  1,
		LastSlowAtMS:                   2,
		SlowCount:                      3,
		DegradedUntilMS:                &degradedUntil,
		SuccessCount:                   0,
		RecoveryProbeRoundAttemptCount: &roundAttempts,
		RecoveryProbeRoundSuccessCount: &roundSuccesses,
		NextProbeAtMS:                  &nextProbeAt,
		Reason:                         "普通路由速度优先首字等待超时",
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	encodedText := string(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"generation"`, `"accountId"`, `"runtimeKey"`, `"scope"`, `"config"`,
		`"firstSlowAtMs"`, `"degradedUntilMs"`, `"recoveryProbeRoundAttemptCount"`,
		`"nextProbeAtMs"`, `"firstByteDeadlineMs"`, `"maxFirstByteRetriesPerRequest"`,
	} {
		if !strings.Contains(encodedText, key) {
			t.Fatalf("缺少键 %s: %s", key, encodedText)
		}
	}
}

// TestSpeedFirstConfigLoading 验证 env 装载与命名空间校验。
func TestSpeedFirstConfigLoading(t *testing.T) {
	env := map[string]string{}
	config, err := LoadSpeedFirstRedisConfig(func(name string) string { return env[name] })
	if err != nil || config.Enabled {
		t.Fatalf("disabled cfg=%#v err=%v", config, err)
	}
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://127.0.0.1:6379/9"
	if _, err := LoadSpeedFirstRedisConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatal("Redis state 无 namespace 必须 fail closed")
	}
	env["JUHE_AI_REDIS_NAMESPACE"] = "test-space"
	config, err = LoadSpeedFirstRedisConfig(func(name string) string { return env[name] })
	if err != nil || !config.Enabled {
		t.Fatalf("enabled cfg=%#v err=%v", config, err)
	}
	if !ValidSpeedFirstNamespace("test-space") || ValidSpeedFirstNamespace("bad space!") {
		t.Fatal("命名空间校验错误")
	}
}

// TestPassiveOffsetApplyBounds 验证恢复探针顺延延迟保持正值且有界。
func TestPassiveOffsetApplyBounds(t *testing.T) {
	fixed := func() float64 { return 0.5 }
	for interval := int64(1); interval < 200_000; interval += 9997 {
		delay := passiveOffsetApply(interval, fixed)
		if delay < 1 {
			t.Fatalf("delay=%d 不得小于 1", delay)
		}
		if delay > interval*2 {
			t.Fatalf("delay=%d 超出对称窗口", delay)
		}
	}
}
