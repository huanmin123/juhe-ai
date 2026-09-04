package gatewayhotquality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func newTestRedisHotQualityStore(t *testing.T, namespace string) (*RedisHotQualityStore, *miniredis.Miniredis) {
	t.Helper()
	server, client := newTestRedis(t)
	store, err := NewRedisHotQualityStore(NewRedisScriptRunner(client), RedisHotQualityStoreOptions{Namespace: namespace, Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store, server
}

// mockScriptRunner replays canned Lua replies in order and records every call
// so tests can assert the exact keys/args the store hands to Redis (Mock 闭环).
type mockScriptRunner struct {
	t      *testing.T
	calls  []mockScriptCall
	script []mockScriptCall
}

type mockScriptCall struct {
	scriptMarker string
	keys         []string
	args         []string
	reply        interface{}
	err          error
}

func (m *mockScriptRunner) Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error) {
	index := len(m.calls)
	if index >= len(m.script) {
		m.t.Fatalf("unexpected script call #%d: %s", index, script)
	}
	expected := m.script[index]
	if !strings.Contains(script, expected.scriptMarker) {
		m.t.Fatalf("call #%d script mismatch: want marker %q", index, expected.scriptMarker)
	}
	m.calls = append(m.calls, mockScriptCall{
		scriptMarker: expected.scriptMarker,
		keys:         keys,
		args:         args,
	})
	if expected.err != nil {
		return nil, expected.err
	}
	return expected.reply, nil
}

func newMockScriptRunner(t *testing.T, script []mockScriptCall) *mockScriptRunner {
	return &mockScriptRunner{t: t, script: script}
}

func scopeJSON(scope HotQualityScope) string {
	encoded, _ := json.Marshal(scope)
	return string(encoded)
}

func TestRedisHotQualityStoreKeyLayout(t *testing.T) {
	store, _ := newTestRedisHotQualityStore(t, "dev")
	keys := store.Keys()
	if keys.Prefix != "juhe-ai:dev:hot-quality:gateway-hot-quality" {
		t.Fatalf("prefix = %s", keys.Prefix)
	}
	if keys.HotRegistry != keys.Prefix+":registry:hot" || keys.AttemptRegistry != keys.Prefix+":registry:attempt" ||
		keys.TerminalRegistry != keys.Prefix+":registry:terminal" || keys.Metrics != keys.Prefix+":metrics" {
		t.Fatalf("keys = %+v", keys)
	}
	if got := redisHotQualityEntryKey(keys, "abc"); got != keys.Prefix+":entry:"+redisIdentityHash("abc") {
		t.Fatalf("entry key = %s", got)
	}
	// short namespace or full prefix both normalize
	store2, _ := newTestRedisHotQualityStore(t, "juhe-ai:dev")
	if store2.Keys().Prefix != keys.Prefix {
		t.Fatalf("double prefix: %s", store2.Keys().Prefix)
	}
}

func TestRedisHotQualityStoreConstructorGuards(t *testing.T) {
	if _, err := NewRedisHotQualityStore(nil, RedisHotQualityStoreOptions{Namespace: "dev"}); err == nil || err.Error() != "Redis 热质量缺少 redisUrl" {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewRedisHotQualityStore(NewRedisScriptRunner(&redis.Client{}), RedisHotQualityStoreOptions{}); err == nil || err.Error() != "Redis namespace 不能为空" {
		t.Fatalf("err = %v", err)
	}
	_, client := newTestRedis(t)
	shortTTL := int64(10)
	if _, err := NewRedisHotQualityStore(NewRedisScriptRunner(client), RedisHotQualityStoreOptions{Namespace: "dev", TerminalTtlMs: &shortTTL}); err == nil || err.Error() != "terminalTtlMs 不得少于 3600000ms" {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewRedisSameTierExplorationStore(nil, RedisSameTierExplorationStoreOptions{Namespace: "dev"}); err == nil || err.Error() != "redisUrl 必须是 1 到 512 字符" {
		t.Fatalf("err = %v", err)
	}
}

// TestHotQualityStoreDualDriverConsistency runs the identical accounting
// scenario against the memory driver and the Redis driver (the Redis replies
// mirror the Lua scripts' real cjson output shape), asserting both drivers
// agree on every observable status and counter.
func TestHotQualityStoreDualDriverConsistency(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		runHotQualityStoreScenario(t, newTestMemoryStore(t, nil))
	})
	t.Run("redis", func(t *testing.T) {
		scope := testScope("acc-1")
		scopeText := scopeJSON(scope)
		entryJSON := fmt.Sprintf(`{"scopeKey":"sk","scope":%s,"buckets":{"16666":{"minuteStartedAtMs":960000,"attempts":1,"completedResponses":1,"localTransportFailures":0,"timeouts":0,"readInterruptions":0,"incompleteResponses":0,"explicitPolicyFailures":0,"unknownOutcomes":0,"clientCancellations":0,"firstByteSampleCount":1,"firstByteSumMs":1200,"firstByteHistogram":[0,1,0,0,0,0,0,0]}},"expiresAtMs":3400000}`, scopeText)
		terminalJSON := `{"terminalOutcomeId":"to-1","outcomeClass":"completed_response","failureScope":"none","source":"gateway_transport","createdAtMs":1000000}`
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local requested_entry_key", reply: `{"status":"applied","requestedScope":` + scopeText + `,"effectiveScope":` + scopeText + `}`},
			{scriptMarker: "local requested_entry_key", reply: `{"status":"idempotent","requestedScope":` + scopeText + `,"effectiveScope":` + scopeText + `}`},
			{scriptMarker: "local requested_entry_key", reply: `{"status":"applied","effectiveScope":` + scopeText + `,"terminal":` + terminalJSON + `}`},
			{scriptMarker: "local requested_entry_key", reply: `{"status":"idempotent","effectiveScope":` + scopeText + `,"terminal":` + terminalJSON + `}`},
			{scriptMarker: "local raw = redis.call('GET', KEYS[1])", reply: entryJSON},
			{scriptMarker: "local attempt = cjson.decode(raw)", reply: terminalJSON},
			{scriptMarker: "ZREMRANGEBYSCORE", reply: `{"keyCount":1,"attemptIdentityCount":1,"terminalIdentityCount":1,"keyCreationRefusals":0,"highCardinalityDegradations":0,"attemptCapacityRefusals":0,"terminalQualityKeyMisses":0}`},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		runHotQualityStoreScenario(t, store)

		// verify the mutation call shape handed to Redis
		first := runner.calls[0]
		if len(first.keys) != 8 {
			t.Fatalf("mutation keys = %d", len(first.keys))
		}
		if first.args[1] != "10000" || first.args[2] != "100000" || first.args[3] != "2400000" || first.args[4] != "3600000" {
			t.Fatalf("capacity args = %v", first.args[1:])
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(first.args[0]), &payload); err != nil {
			t.Fatalf("payload json: %v", err)
		}
		if payload["operation"] != "record_attempt" || payload["attemptId"] != "at-1" || payload["requestedScopeKey"] == "" || payload["fallbackScopeKey"] == "" {
			t.Fatalf("payload = %v", payload)
		}
	})
}

func TestRedisHotQualityStoreDegradationAndRefusals(t *testing.T) {
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local requested_entry_key", reply: `{"status":"applied","requestedScope":` + scopeJSON(testScope("x")) + `,"effectiveScope":` + scopeJSON(testScope("x")) + `}`},
		{scriptMarker: "local requested_entry_key", reply: `{"status":"key_capacity_exhausted","requestedScope":` + scopeJSON(testScope("y")) + `,"effectiveScope":` + scopeJSON(testScope("y")) + `}`},
		{scriptMarker: "ZREMRANGEBYSCORE", reply: `{"keyCount":1,"attemptIdentityCount":2,"terminalIdentityCount":0,"keyCreationRefusals":1,"highCardinalityDegradations":0,"attemptCapacityRefusals":0,"terminalQualityKeyMisses":0}`},
	})
	store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "b", Scope: testScope("y")})
	if err != nil || result.Status != AttemptMutationKeyCapacityExhausted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	stats, err := store.Stats(ctx, nil)
	if err != nil {
		t.Fatalf("stats err = %v", err)
	}
	if stats.KeyCreationRefusals != 1 || stats.KeyCount != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRedisHotQualityStoreResponseValidation(t *testing.T) {
	testCases := []struct {
		name      string
		reply     interface{}
		scriptErr error
		want      string
	}{
		{"empty mutation reply", "", nil, "Redis 热质量 mutation 返回值无效"},
		{"missing status", `{"effectiveScope":{}}`, nil, "Redis 热质量 mutation 结构无效"},
		{"script error", nil, errors.New("boom"), "boom"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newMockScriptRunner(t, []mockScriptCall{
				{scriptMarker: "local requested_entry_key", reply: testCase.reply, err: testCase.scriptErr},
			})
			store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_, err = store.RecordAttempt(context.Background(), HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x")})
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
	t.Run("invalid stats reply", func(t *testing.T) {
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "ZREMRANGEBYSCORE", reply: `{"keyCount":-1}`},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, err = store.Stats(context.Background(), nil)
		if err == nil || err.Error() != "Redis 热质量 keyCount 返回值无效" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRedisHotQualityStoreScriptsWithMiniredis(t *testing.T) {
	// The read-entry and stats Lua scripts re-encode no decoded tables, so
	// they run for real under miniredis. (The mutation scripts echo decoded
	// scope tables in their response; miniredis's Lua JSON cannot encode
	// decoded tables — real Redis can, as the Node production driver proves —
	// so those paths are covered by the canned-reply mock above.)
	store, server := newTestRedisHotQualityStore(t, "dev")
	ctx := context.Background()
	scope := testScope("acc-1")
	scopeKey, err := HotQualityScopeKey(scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entryKey := redisHotQualityEntryKey(store.Keys(), scopeKey)
	entryJSON := `{"scopeKey":"` + scopeKey + `","scope":` + scopeJSON(scope) + `,"buckets":{"16666":{"minuteStartedAtMs":960000,"attempts":1,"completedResponses":1,"firstByteSampleCount":1,"firstByteSumMs":1200,"firstByteHistogram":[0,1,0,0,0,0,0,0]}},"expiresAtMs":1003600000}`
	if err := server.Set(entryKey, entryJSON); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := server.ZAdd(store.Keys().HotRegistry, 1003600000, entryKey); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	snapshot, err := store.Get(ctx, scope, nil)
	if err != nil || snapshot == nil {
		t.Fatalf("snapshot = %v, err = %v", snapshot, err)
	}
	if snapshot.Window5m.Attempts != 1 || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.FirstByteSumMs != 1200 {
		t.Fatalf("window5m = %+v", snapshot.Window5m)
	}
	// expired entry is deleted by the read script
	if err := server.Set(entryKey, `{"scopeKey":"k","scope":`+scopeJSON(scope)+`,"buckets":{},"expiresAtMs":1}`); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	snapshot, err = store.Get(ctx, scope, nil)
	if err != nil || snapshot != nil {
		t.Fatalf("expired snapshot = %v, err = %v", snapshot, err)
	}
	if _, err := server.Get(entryKey); err == nil {
		t.Fatalf("expired entry must be deleted by the read script")
	}

	// stats script against real hash/zset state
	server.HSet(store.Keys().Metrics, "keyCreationRefusals", "3")
	if _, err := server.ZAdd(store.Keys().HotRegistry, 9999999, "entry-x"); err != nil {
		t.Fatalf("seed hot registry: %v", err)
	}
	stats, err := store.Stats(ctx, nil)
	if err != nil {
		t.Fatalf("stats err = %v", err)
	}
	if stats.KeyCount != 1 || stats.KeyCreationRefusals != 3 {
		t.Fatalf("stats = %+v", stats)
	}
}

// runHotQualityStoreScenario drives one identical accounting scenario against
// any store implementation; both drivers must agree on every observable
// status and counter (存储双驱一致性).
func runHotQualityStoreScenario(t *testing.T, store HotQualityStore) {
	t.Helper()
	ctx := context.Background()
	now := int64(1_000_000)
	scope := testScope("acc-1")

	attempt, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "at-1", Scope: scope, NowMs: &now})
	if err != nil || attempt.Status != AttemptMutationApplied {
		t.Fatalf("attempt = %+v, err = %v", attempt, err)
	}
	replay, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "at-1", Scope: scope, NowMs: &now})
	if err != nil || replay.Status != AttemptMutationIdempotent {
		t.Fatalf("replay = %+v, err = %v", replay, err)
	}
	firstByte := 1200.0
	terminal, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID:         "at-1",
		Scope:             scope,
		TerminalOutcomeID: "to-1",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
		FirstByteMs:       &firstByte,
		NowMs:             &now,
	})
	if err != nil || terminal.Status != TerminalMutationApplied {
		t.Fatalf("terminal = %+v, err = %v", terminal, err)
	}
	idempotent, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID:         "at-1",
		Scope:             scope,
		TerminalOutcomeID: "to-1",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
		FirstByteMs:       &firstByte,
		NowMs:             &now,
	})
	if err != nil || idempotent.Status != TerminalMutationIdempotent {
		t.Fatalf("idempotent = %+v, err = %v", idempotent, err)
	}

	snapshot, err := store.Get(ctx, scope, &now)
	if err != nil || snapshot == nil {
		t.Fatalf("snapshot = %v, err = %v", snapshot, err)
	}
	if snapshot.Window5m.Attempts != 1 || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.FirstByteSampleCount != 1 {
		t.Fatalf("window5m = %+v", snapshot.Window5m)
	}
	if snapshot.Window5m.FirstByteSumMs != 1200 {
		t.Fatalf("firstByteSumMs = %d", snapshot.Window5m.FirstByteSumMs)
	}
	if snapshot.FirstByteP95Bucket10m == nil || *snapshot.FirstByteP95Bucket10m != 1 {
		t.Fatalf("p95 = %v (1200ms falls in bucket 1)", snapshot.FirstByteP95Bucket10m)
	}
	if snapshot.SampleState != HotQualitySampleWarming {
		t.Fatalf("sampleState = %s", snapshot.SampleState)
	}
	terminalRecord, err := store.GetTerminal(ctx, "at-1", &now)
	if err != nil || terminalRecord == nil || terminalRecord.OutcomeClass != TerminalOutcomeCompletedResponse {
		t.Fatalf("terminal record = %+v, err = %v", terminalRecord, err)
	}

	stats, err := store.Stats(ctx, &now)
	if err != nil {
		t.Fatalf("stats err = %v", err)
	}
	if stats.KeyCount != 1 || stats.AttemptIdentityCount != 1 || stats.TerminalIdentityCount != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.KeyCreationRefusals != 0 || stats.AttemptCapacityRefusals != 0 || stats.TerminalQualityKeyMisses != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRedisSameTierExplorationStoreFreshPoolScriptsWithMiniredis(t *testing.T) {
	// First-touch pool paths encode only freshly built Lua tables, which
	// miniredis's Lua JSON handles; decoded-table re-encoding (subsequent
	// touches of a persisted pool) needs real Redis and is covered by the
	// canned-reply mock below.
	_, client := newTestRedis(t)
	store, err := NewRedisSameTierExplorationStore(NewRedisScriptRunner(client), RedisSameTierExplorationStoreOptions{
		Namespace: "dev", Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.Prefix() != "juhe-ai:dev:same-tier-exploration:gateway" {
		t.Fatalf("prefix = %s", store.Prefix())
	}
	ctx := context.Background()
	state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "p1"})
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if state.PoolKey != "p1" || state.Credit != 0 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "p2", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 2_000}); err != nil {
		t.Fatalf("reserve fresh: %v", err)
	}
}

// runSameTierExplorationScenario drives one identical credit/cursor sequence
// against any store implementation (存储双驱一致性).
func runSameTierExplorationScenario(t *testing.T, store SameTierExplorationStore) {
	t.Helper()
	ctx := context.Background()
	// accrue to exactly 1.0 credit (20 × 0.05)
	var state *SameTierExplorationState
	for i := 0; i < 20; i++ {
		var err error
		state, err = store.Accrue(ctx, SameTierExplorationAccrueInput{
			PoolKey:      "pool",
			AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Eligible:     true,
			NowMs:        int64Ptr(1_000 + int64(i)),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if state.Credit != 1.0 {
		t.Fatalf("credit = %v", state.Credit)
	}
	// sub-1.0 credit refuses
	lowState, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "low", NowMs: int64Ptr(2_000)})
	if err != nil || lowState.Credit != 0 {
		t.Fatalf("low state = %+v, err = %v", lowState, err)
	}
	lowReserve, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "low", ReservationID: "lr", AccountRuntimeKey: "acc", LeaseUntilMs: 3_000, NowMs: int64Ptr(2_000),
	})
	if err != nil || lowReserve.Status != ExplorationReservationCreditUnavailable {
		t.Fatalf("low reserve = %+v, err = %v", lowReserve, err)
	}
	// reserve + settle dispatched
	reserve, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc-1", LeaseUntilMs: 3_000, NowMs: int64Ptr(2_500),
	})
	if err != nil || reserve.Status != ExplorationReservationReserved || reserve.Reservation == nil {
		t.Fatalf("reserve = %+v, err = %v", reserve, err)
	}
	// pool busy while live
	busy, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "pool", ReservationID: "r2", AccountRuntimeKey: "acc-2", LeaseUntilMs: 3_000, NowMs: int64Ptr(2_600),
	})
	if err != nil || busy.Status != ExplorationReservationPoolBusy {
		t.Fatalf("busy = %+v, err = %v", busy, err)
	}
	settled, err := store.Settle(ctx, SameTierExplorationSettleInput{
		PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc-1", Outcome: "dispatched", NowMs: int64Ptr(2_700),
	})
	if err != nil || settled.Status != ExplorationSettlementApplied {
		t.Fatalf("settled = %+v, err = %v", settled, err)
	}
	if settled.State.Credit != 0 || settled.State.Cursor != 1 {
		t.Fatalf("state after dispatch = %+v", settled.State)
	}
	if settled.State.CooldownUntilMsByRuntimeKey["acc-1"] != 2_700+SameTierExplorationTargetCooldownMS {
		t.Fatalf("cooldown = %+v", settled.State.CooldownUntilMsByRuntimeKey)
	}
	// settled id reuse is fenced
	reuse, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc-1", LeaseUntilMs: 9_000, NowMs: int64Ptr(2_800),
	})
	if err != nil || reuse.Status != ExplorationReservationReservationConflict {
		t.Fatalf("reuse = %+v, err = %v", reuse, err)
	}
	// target cooldown enforced (credit refilled first: dispatch spent it)
	for i := 0; i < 20; i++ {
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
			PoolKey: "pool", AccrualToken: "r" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Eligible: true, NowMs: int64Ptr(2_850),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	cooldown, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "pool", ReservationID: "r3", AccountRuntimeKey: "acc-1", LeaseUntilMs: 9_000, NowMs: int64Ptr(2_900),
	})
	if err != nil || cooldown.Status != ExplorationReservationTargetCooldown {
		t.Fatalf("cooldown = %+v, err = %v", cooldown, err)
	}
}

func TestMemorySameTierExplorationStoreSameScenario(t *testing.T) {
	runSameTierExplorationScenario(t, newTestExplorationMemoryStore(t, nil))
}

// TestRedisSameTierExplorationStoreSameScenario replays the same scenario via
// canned Lua replies (real Redis cjson shapes) so the Go parsing, state
// normalization and status mapping are exercised without a live Redis.
func TestRedisSameTierExplorationStoreSameScenario(t *testing.T) {
	runner := newMockScriptRunner(t, explorationScenarioScript(t))
	store, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{
		Namespace: "dev", Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runSameTierExplorationScenario(t, store)
	// every call must hit the same mutation script with the registry key
	for _, call := range runner.calls {
		if len(call.keys) != 2 || !strings.HasSuffix(call.keys[1], ":same-tier-exploration:gateway:registry") {
			t.Fatalf("keys = %v", call.keys)
		}
		if call.args[1] != "2400000" || call.args[3] != "2048" {
			t.Fatalf("args = %v", call.args)
		}
	}
}

// explorationScenarioScript builds the canned replies mirroring the Lua
// script's real Redis cjson output for the scenario above.
func explorationScenarioScript(t *testing.T) []mockScriptCall {
	t.Helper()
	credit := 0.0
	round6 := func(value float64) float64 {
		return float64(int64(value*1_000_000+0.5)) / 1_000_000
	}
	stateJSON := func(creditValue float64, cursor int64, reservations string, cooldown string, tokens string, settled string, expiresAtMs int64) string {
		creditText := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", creditValue), "0"), ".")
		if creditText == "" {
			creditText = "0"
		}
		return fmt.Sprintf(`{"poolKey":"pool","credit":%s,"cursor":%d,"reservations":%s,"cooldownUntilMsByRuntimeKey":%s,"accruedTokens":%s,"settledReservationIds":%s,"expiresAtMs":%d}`,
			creditText, cursor, reservations, cooldown, tokens, settled, expiresAtMs)
	}
	var calls []mockScriptCall
	var tokens []string
	for i := 0; i < 20; i++ {
		tokens = append(tokens, string(rune('a'+i%26))+string(rune('0'+i/26)))
		credit = round6(credit + 0.05)
		if credit > 1 {
			credit = 1
		}
		calls = append(calls, mockScriptCall{
			scriptMarker: "local operation = ARGV[5]",
			// accrue leaves status nil; cjson drops the key, so the reply only
			// carries the state table.
			reply: `{"state":` + stateJSON(credit, 0, "[]", "{}", jsonStringArray(tokens), "[]", 2_401_000+int64(i)) + `}`,
		})
	}
	// get low pool (fresh, credit 0)
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: `{"state":` + stateJSON(0, 0, "[]", "{}", "[]", "[]", 2_402_000) + `}`})
	// reserve on low pool → credit_unavailable (state unchanged except TTL)
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: `{"status":"credit_unavailable","state":` + stateJSON(0, 0, "[]", "{}", "[]", "[]", 2_403_000) + `}`})
	// reserve r1 on pool → reserved
	reservation := `{"reservationId":"r1","accountRuntimeKey":"acc-1","leaseUntilMs":3000}`
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: fmt.Sprintf(`{"status":"reserved","state":%s,"reservation":%s}`, stateJSON(1, 0, "["+reservation+"]", "{}", jsonStringArray(tokens), "[]", 2_403_500), reservation)})
	// reserve r2 → pool_busy
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: fmt.Sprintf(`{"status":"pool_busy","state":%s}`, stateJSON(1, 0, "["+reservation+"]", "{}", jsonStringArray(tokens), "[]", 2_403_600))})
	// settle r1 dispatched → applied, credit 0, cursor 1, cooldown
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: fmt.Sprintf(`{"status":"applied","state":%s}`, stateJSON(0, 1, "[]", `{"acc-1":62700}`, jsonStringArray(tokens), `["r1"]`, 2_403_700))})
	// reuse r1 → reservation_conflict (tombstone)
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: fmt.Sprintf(`{"status":"reservation_conflict","state":%s}`, stateJSON(0, 1, "[]", `{"acc-1":62700}`, jsonStringArray(tokens), `["r1"]`, 2_403_800))})
	// refill credit
	var refillTokens []string
	for i := 0; i < 20; i++ {
		refillTokens = append(refillTokens, "r"+string(rune('a'+i%26))+string(rune('0'+i/26)))
		credit = round6(credit + 0.05)
		if credit > 1 {
			credit = 1
		}
		calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: `{"state":` + stateJSON(credit, 1, "[]", `{"acc-1":62700}`, jsonStringArray(refillTokens), `["r1"]`, 2_403_850+int64(i)) + `}`})
	}
	// cooldown block for acc-1
	calls = append(calls, mockScriptCall{scriptMarker: "local operation = ARGV[5]", reply: fmt.Sprintf(`{"status":"target_cooldown","state":%s}`, stateJSON(1, 1, "[]", `{"acc-1":62700}`, jsonStringArray(refillTokens), `["r1"]`, 2_403_900))})
	return calls
}

func jsonStringArray(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func TestRedisSameTierExplorationStoreEmptyArrayWireFormat(t *testing.T) {
	// Older Lua cjson builds encode empty arrays as `{}`; the Go reader must
	// tolerate that wire representation (mirrors the Node Array.isArray guard).
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local operation = ARGV[5]", reply: `{"status":"read","state":{"poolKey":"legacy","credit":0,"cursor":0,"reservations":{},"cooldownUntilMsByRuntimeKey":{},"accruedTokens":{},"settledReservationIds":{},"expiresAtMs":9999999}}`},
	})
	store, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{
		Namespace: "dev", Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, err := store.Get(context.Background(), SameTierExplorationGetInput{PoolKey: "legacy"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.PoolKey != "legacy" || len(state.Reservations) != 0 || len(state.AccruedTokens) != 0 || len(state.SettledReservationIDs) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestRedisSameTierExplorationStoreInvalidReply(t *testing.T) {
	testCases := []struct {
		name  string
		reply interface{}
		want  string
	}{
		{"empty", "", "Redis 同层探索状态返回值无效"},
		{"missing state", `{"status":"read"}`, "Redis 同层探索状态结构无效"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newMockScriptRunner(t, []mockScriptCall{
				{scriptMarker: "local operation = ARGV[5]", reply: testCase.reply},
			})
			store, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_, err = store.Get(context.Background(), SameTierExplorationGetInput{PoolKey: "p"})
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
}
