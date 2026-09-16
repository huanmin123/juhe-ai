package gatewayhotquality

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func newW9CExplorationRedisStore(t *testing.T, runner ScriptRunner) *RedisSameTierExplorationStore {
	t.Helper()
	store, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{
		Namespace: "dev",
		Now:       func() int64 { return 1_000_000 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store
}

// ---------------------------------------------------------------------------
// runtime.go gaps.
// ---------------------------------------------------------------------------

func TestW9CRoutingObserverFunc(t *testing.T) {
	var seen RoutingObservation
	observer := RoutingObserverFunc(func(observation RoutingObservation) {
		seen = observation
	})
	observer.ObserveGatewayRouting(RoutingObservation{Kind: "attempt", Outcome: "ok"})
	if seen.Kind != "attempt" || seen.Outcome != "ok" {
		t.Fatalf("observer must forward the observation, got %+v", seen)
	}
	// observeRouting must tolerate nil runtime/observer.
	observeRouting(nil, RoutingObservation{Kind: "attempt"})
	observeRouting(&GatewayHotQualityRuntime{}, RoutingObservation{Kind: "attempt"})
}

func TestW9CGetGatewayHotQualityRuntimeRedisDriver(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	url := "redis://" + server.Addr()
	runtime, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{
		RuntimeMode:        "performance",
		RuntimeStateDriver: "redis",
		RedisStateURL:      url,
		RedisNamespace:     "w9c-redis",
	})
	if err != nil {
		t.Fatalf("redis runtime must build against miniredis: %v", err)
	}
	if runtime.HotQualityStore == nil || runtime.ExplorationStore == nil {
		t.Fatal("redis runtime must expose both stores")
	}
	// Same identity returns the cached pair.
	cached, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{
		RuntimeMode:        "performance",
		RuntimeStateDriver: "redis",
		RedisStateURL:      url,
		RedisNamespace:     "w9c-redis",
	})
	if err != nil || cached != runtime {
		t.Fatalf("cached runtime must be reused: %v", err)
	}
	// GetRedisClient caching and validation.
	client, err := GetRedisClient(ctx, url)
	if err != nil || client == nil {
		t.Fatalf("GetRedisClient = %v, %v", client, err)
	}
	if again, err := GetRedisClient(ctx, url+"   "); err != nil || again != client {
		t.Fatal("trimmed URL must hit the client cache")
	}
	if _, err := GetRedisClient(ctx, ""); err == nil || err.Error() != "Redis 连接串不能为空" {
		t.Fatalf("empty url err = %v", err)
	}
	if _, err := GetRedisClient(ctx, "://not-a-redis-url"); err == nil {
		t.Fatal("invalid url must fail to parse")
	}
}

func TestW9CSameTierExplorationDecisionState(t *testing.T) {
	if _, err := sameTierExplorationDecisionState(nil, 1, true); err == nil || err.Error() != "同层探索状态缺失" {
		t.Fatalf("nil state err = %v", err)
	}
	state := &SameTierExplorationState{
		Credit: 3.5,
		Cursor: 7,
		Reservations: []SameTierExplorationReservation{
			{ReservationID: "r1", AccountRuntimeKey: "acc-1", LeaseUntilMs: 2000},
			{ReservationID: "r2", AccountRuntimeKey: "acc-2", LeaseUntilMs: 3000},
		},
		CooldownUntilMsByRuntimeKey: map[string]int64{"acc-3": 5000},
	}
	decision, err := sameTierExplorationDecisionState(state, 42, false)
	if err != nil {
		t.Fatalf("decision err = %v", err)
	}
	if !decision.Enabled || decision.EligibleFirstPrimaryDispatch {
		t.Fatalf("decision flags = %+v", decision)
	}
	if !decision.CreditAccrualAlreadyApplied || decision.RequestAlreadyExplored {
		t.Fatalf("decision accrual flags = %+v", decision)
	}
	if len(decision.TargetInFlightRuntimeKeys) != 2 || decision.TargetInFlightRuntimeKeys[0] != "acc-1" {
		t.Fatalf("in-flight keys = %v", decision.TargetInFlightRuntimeKeys)
	}
	if decision.TargetCooldownUntilMsByRuntimeKey["acc-3"] != 5000 {
		t.Fatalf("cooldown map = %v", decision.TargetCooldownUntilMsByRuntimeKey)
	}
	if decision.NowMs != 42 || decision.KnownSampleStaleAfterMs != knownSampleStaleAfterMs {
		t.Fatalf("time fields = %+v", decision)
	}
}

// ---------------------------------------------------------------------------
// redis_store.go gaps (RecordTerminal validation arms, GetTerminal arms,
// parse helpers).
// ---------------------------------------------------------------------------

func w9CTerminalScopeJSON() string { return scopeJSON(testScope("acc-w9c")) }

func TestW9CRedisRecordTerminalValidationArms(t *testing.T) {
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local requested_entry_key", reply: `{"status":"applied","effectiveScope":` + w9CTerminalScopeJSON() + `,"terminal":{"terminalOutcomeId":"to-1","outcomeClass":"completed_response","failureScope":"none","source":"gateway_transport","createdAtMs":1000000}}`},
	})
	store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000_000 }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()
	base := HotQualityRecordTerminalInput{
		AttemptID:         "at-w9c",
		Scope:             testScope("acc-w9c"),
		TerminalOutcomeID: "to-w9c",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
	}
	firstByte := 120.6
	base.FirstByteMs = &firstByte
	result, err := store.RecordTerminal(ctx, base)
	if err != nil || result == nil || result.Status != TerminalMutationApplied {
		t.Fatalf("terminal record = %+v, %v", result, err)
	}
	if result.Terminal == nil || result.Terminal.TerminalOutcomeID != "to-1" {
		t.Fatalf("terminal payload = %+v", result.Terminal)
	}

	errorArms := []struct {
		name   string
		mutate func(input *HotQualityRecordTerminalInput)
	}{
		{"bad scope lane", func(input *HotQualityRecordTerminalInput) { input.Scope.RequestLane = "video" }},
		{"bad outcome class", func(input *HotQualityRecordTerminalInput) { input.OutcomeClass = "weird" }},
		{"bad failure scope", func(input *HotQualityRecordTerminalInput) { input.FailureScope = "galaxy" }},
		{"bad source", func(input *HotQualityRecordTerminalInput) { input.Source = "moon" }},
		{"empty attempt id", func(input *HotQualityRecordTerminalInput) { input.AttemptID = "  " }},
		{"empty terminal outcome id", func(input *HotQualityRecordTerminalInput) { input.TerminalOutcomeID = "" }},
		{"negative now ms", func(input *HotQualityRecordTerminalInput) { negative := int64(-5); input.NowMs = &negative }},
		{"bad first byte", func(input *HotQualityRecordTerminalInput) { nan := math.NaN(); input.FirstByteMs = &nan }},
	}
	for _, arm := range errorArms {
		input := base
		input.FirstByteMs = nil
		arm.mutate(&input)
		if _, err := store.RecordTerminal(ctx, input); err == nil {
			t.Fatalf("%s must be rejected", arm.name)
		}
	}
}

func TestW9CRedisGetTerminalArms(t *testing.T) {
	ctx := context.Background()
	build := func(t *testing.T, reply interface{}, scriptErr error) *RedisHotQualityStore {
		t.Helper()
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "local attempt = cjson.decode(raw)", reply: reply, err: scriptErr},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return store
	}

	tooLongID := strings.Repeat("x", 257)
	if _, err := build(t, nil, nil).GetTerminal(ctx, tooLongID, nil); err == nil {
		t.Fatal("oversized attempt id must be rejected")
	}
	if _, err := build(t, nil, nil).GetTerminal(ctx, "at-1", int64Ptr(-3)); err == nil {
		t.Fatal("negative now must be rejected")
	}
	// redis.Nil arm.
	got, err := build(t, nil, redis.Nil).GetTerminal(ctx, "at-1", nil)
	if err != nil || got != nil {
		t.Fatalf("redis.Nil must map to nil record: %+v %v", got, err)
	}
	// Non-string reply arm.
	got, err = build(t, 42, nil).GetTerminal(ctx, "at-1", nil)
	if err != nil || got != nil {
		t.Fatalf("non-string reply must map to nil record: %+v %v", got, err)
	}
	// Empty string reply arm.
	got, err = build(t, "", nil).GetTerminal(ctx, "at-1", nil)
	if err != nil || got != nil {
		t.Fatalf("empty reply must map to nil record: %+v %v", got, err)
	}
	// Script error arm.
	if _, err := build(t, nil, errors.New("script boom")).GetTerminal(ctx, "at-1", nil); err == nil {
		t.Fatal("script error must surface")
	}
	// Valid record arm.
	record, err := build(t, `{"terminalOutcomeId":"to-1","outcomeClass":"completed_response","failureScope":"none","source":"gateway_transport","createdAtMs":1000000}`, nil).GetTerminal(ctx, "at-1", nil)
	if err != nil || record == nil || record.OutcomeClass != "completed_response" {
		t.Fatalf("record = %+v, %v", record, err)
	}
}

func TestW9CParseRedisHotQualityEntryAndTerminal(t *testing.T) {
	scope := testScope("acc-1")
	valid := `{"scopeKey":"sk","scope":` + scopeJSON(scope) + `,"buckets":{},"expiresAtMs":100}`
	entry, err := parseRedisHotQualityEntry(valid)
	if err != nil || entry == nil || entry.ScopeKey != "sk" || entry.ExpiresAtMs != 100 {
		t.Fatalf("entry = %+v, %v", entry, err)
	}
	if _, err := parseRedisHotQualityEntry(`not-json`); err == nil {
		t.Fatal("malformed json must fail")
	}
	if _, err := parseRedisHotQualityEntry(`{"scopeKey":"","scope":{},"buckets":{},"expiresAtMs":1}`); err == nil {
		t.Fatal("missing fields must fail")
	}
	if _, err := parseRedisHotQualityEntry(`{"scopeKey":"sk","scope":{"accountRuntimeKey":"a","protocolProfile":"p","requestLane":"nope","modelFamily":"m"},"buckets":{},"expiresAtMs":1}`); err == nil {
		t.Fatal("invalid scope must fail")
	}

	terminal, err := parseTerminalRecord(`{"terminalOutcomeId":"to-1","outcomeClass":"completed_response","failureScope":"none","source":"gateway_transport","createdAtMs":7}`)
	if err != nil || terminal == nil || terminal.CreatedAtMs != 7 {
		t.Fatalf("terminal = %+v, %v", terminal, err)
	}
	if _, err := parseTerminalRecord(`nope`); err == nil {
		t.Fatal("malformed terminal json must fail")
	}
	if _, err := parseTerminalRecord(`{"terminalOutcomeId":"to-1","outcomeClass":"","failureScope":"none","source":"gateway_transport","createdAtMs":7}`); err == nil {
		t.Fatal("missing outcome class must fail")
	}
}

// ---------------------------------------------------------------------------
// exploration_redis_store.go gaps (Registry, Reserve/Settle validation and
// success arms, malformed replies).
// ---------------------------------------------------------------------------

func TestW9CExplorationRedisPrefixAndRegistry(t *testing.T) {
	store := newW9CExplorationRedisStore(t, newMockScriptRunner(t, nil))
	if store.Prefix() != "juhe-ai:dev:same-tier-exploration:gateway" {
		t.Fatalf("prefix = %s", store.Prefix())
	}
	if store.Registry() != store.Prefix()+":registry" {
		t.Fatalf("registry = %s", store.Registry())
	}
}

func TestW9CExplorationRedisReserveValidationArms(t *testing.T) {
	ctx := context.Background()
	state := `{"status":"reserved","state":{"poolKey":"pool","credit":1,"cursor":0,"reservations":[],"cooldownUntilMsByRuntimeKey":{},"accruedTokens":[],"settledReservationIds":[],"expiresAtMs":1000000},"reservation":{"reservationId":"r1","accountRuntimeKey":"acc-1","leaseUntilMs":2000}}`
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local operation = ARGV[5]", reply: state},
	})
	store := newW9CExplorationRedisStore(t, runner)
	result, err := store.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc-1", LeaseUntilMs: 1_002_000,
	})
	if err != nil || result == nil || result.Status != "reserved" {
		t.Fatalf("reserve = %+v, %v", result, err)
	}
	if result.Reservation == nil || result.Reservation.LeaseUntilMs != 2000 {
		t.Fatalf("reservation = %+v", result.Reservation)
	}
	if len(runner.calls) != 1 || len(runner.calls[0].keys) != 2 {
		t.Fatalf("mutation keys = %+v", runner.calls)
	}

	errorArms := []SameTierExplorationReserveInput{
		{PoolKey: "pool", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 1_000_000},  // lease == now
		{PoolKey: "pool", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 999_000},    // lease < now
		{PoolKey: "pool", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 9_000_000},  // beyond pool ttl
		{PoolKey: "pool", ReservationID: "  ", AccountRuntimeKey: "a", LeaseUntilMs: 1_002_000}, // empty reservation
		{PoolKey: "pool", ReservationID: "r", AccountRuntimeKey: "", LeaseUntilMs: 1_002_000},   // empty runtime key
		{PoolKey: "  ", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 1_002_000},    // empty pool key
	}
	for index, input := range errorArms {
		if _, err := store.Reserve(ctx, input); err == nil {
			t.Fatalf("reserve arm %d must be rejected", index)
		}
	}
	badNow := int64(-1)
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 1, NowMs: &badNow}); err == nil {
		t.Fatal("negative now must be rejected")
	}
}

func TestW9CExplorationRedisSettleAndBadReplies(t *testing.T) {
	ctx := context.Background()
	goodReply := `{"status":"applied","state":{"poolKey":"pool","credit":1,"cursor":1,"reservations":[],"cooldownUntilMsByRuntimeKey":{},"accruedTokens":[],"settledReservationIds":["r1"],"expiresAtMs":1000000}}`
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local operation = ARGV[5]", reply: goodReply},
		{scriptMarker: "local operation = ARGV[5]", reply: ""},
		{scriptMarker: "local operation = ARGV[5]", reply: 17},
		{scriptMarker: "local operation = ARGV[5]", err: errors.New("eval boom")},
		{scriptMarker: "local operation = ARGV[5]", reply: `{"status":"applied"}`},
		{scriptMarker: "local operation = ARGV[5]", reply: `{"status":"ok","state":{"poolKey":"pool","credit":1,"cursor":0,"reservations":[],"cooldownUntilMsByRuntimeKey":{},"accruedTokens":[],"settledReservationIds":[],"expiresAtMs":1000000}}`},
	})
	store := newW9CExplorationRedisStore(t, runner)
	input := SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc-1", Outcome: "dispatched"}
	result, err := store.Settle(ctx, input)
	if err != nil || result == nil || result.Status != "applied" {
		t.Fatalf("settle = %+v, %v", result, err)
	}
	if result.State.SettledReservationIDs == nil || len(result.State.SettledReservationIDs) != 1 {
		t.Fatalf("state = %+v", result.State)
	}
	// Bad reply arms.
	if _, err := store.Settle(ctx, input); err == nil || err.Error() != "Redis 同层探索状态返回值无效" {
		t.Fatalf("empty reply err = %v", err)
	}
	if _, err := store.Settle(ctx, input); err == nil {
		t.Fatal("non-string reply must fail")
	}
	if _, err := store.Settle(ctx, input); err == nil || err.Error() != "eval boom" {
		t.Fatalf("script error must surface, got %v", err)
	}
	if _, err := store.Settle(ctx, input); err == nil {
		t.Fatal("missing state must fail")
	}
	// Validation arms.
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "", ReservationID: "r", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("empty pool key must be rejected")
	}
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "p", ReservationID: " ", AccountRuntimeKey: "a"}); err == nil {
		t.Fatal("empty reservation id must be rejected")
	}
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: " "}); err == nil {
		t.Fatal("empty runtime key must be rejected")
	}
	negative := int64(-9)
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "p", ReservationID: "r", AccountRuntimeKey: "a", NowMs: &negative}); err == nil {
		t.Fatal("negative now must be rejected")
	}
	// Get with a valid scripted reply.
	state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "pool"})
	if err != nil || state == nil || state.PoolKey != "pool" || state.Credit != 1 {
		t.Fatalf("get = %+v, %v", state, err)
	}
}

// ---------------------------------------------------------------------------
// memory_store.go gaps (freshEntry / freshAttempt expiry deletion).
// ---------------------------------------------------------------------------

func TestW9CMemoryStoreFreshEntryAndAttemptExpiry(t *testing.T) {
	store := newTestMemoryStore(t, nil)
	ctx := context.Background()
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "at-exp", Scope: testScope("acc-exp")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// freshEntry: live entry is returned; expired entry is deleted.
	live := store.freshEntry("missing", 1_000)
	if live != nil {
		t.Fatal("missing entry must be nil")
	}
	store.mu.Lock()
	var scopeKey string
	var entry *memoryHotQualityEntry
	for key, candidate := range store.entries {
		scopeKey, entry = key, candidate
		break
	}
	store.mu.Unlock()
	if entry == nil {
		t.Fatal("attempt must create an entry")
	}
	if got := store.freshEntry(scopeKey, entry.expiresAtMs-1); got != entry {
		t.Fatal("live entry must be returned")
	}
	if got := store.freshEntry(scopeKey, entry.expiresAtMs+1); got != nil {
		t.Fatal("expired entry must be dropped")
	}
	store.mu.Lock()
	_, stillThere := store.entries[scopeKey]
	store.mu.Unlock()
	if stillThere {
		t.Fatal("expired entry must be deleted from the map")
	}

	// freshAttempt: expired attempt identity is deleted with its terminal.
	store.mu.Lock()
	attemptID, attempt := "at-exp", store.attempts["at-exp"]
	store.mu.Unlock()
	if attempt == nil {
		t.Fatal("attempt identity must exist")
	}
	if got := store.freshAttempt(attemptID, attempt.expiresAtMs-1); got != attempt {
		t.Fatal("live attempt must be returned")
	}
	if got := store.freshAttempt(attemptID, attempt.expiresAtMs+1); got != nil {
		t.Fatal("expired attempt must be dropped")
	}
	store.mu.Lock()
	_, stillTracked := store.attempts[attemptID]
	store.mu.Unlock()
	if stillTracked {
		t.Fatal("expired attempt must be deleted")
	}
}

// ---------------------------------------------------------------------------
// speedfirst_body_admission.go gaps (SetClock / ClearForTest).
// ---------------------------------------------------------------------------

func TestW9CBodyAdmissionSetClockAndClear(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	fakeStart := time.UnixMilli(5_000_000)
	registry.SetClock(func() time.Time { return fakeStart })
	registry.SetClock(nil) // nil must keep the injected clock.

	ctx := context.Background()
	decision := registry.Acquire(ctx, SpeedFirstBodyAdmissionInput{
		SystemAccountID: "sys-w9c", RouteStrategyID: "w9c-route", GroupID: "grp", APIKeyID: "key", Capacity: 1,
	})
	if !decision.Acquired {
		t.Fatalf("first acquire must pass: %+v", decision)
	}
	if len(registry.Snapshot()) != 1 {
		t.Fatalf("snapshot = %+v", registry.Snapshot())
	}
	registry.ClearForTest()
	if len(registry.Snapshot()) != 0 {
		t.Fatal("ClearForTest must drop all states")
	}
	// ClearForTest on an empty registry stays a no-op.
	registry.ClearForTest()
}
