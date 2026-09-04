package gatewayhotquality

import (
	"context"
	"testing"
)

func newTestMemoryStore(t *testing.T, mutate func(*MemoryHotQualityStoreOptions)) *MemoryHotQualityStore {
	t.Helper()
	options := MemoryHotQualityStoreOptions{Now: func() int64 { return 1_000_000 }}
	if mutate != nil {
		mutate(&options)
	}
	store, err := NewMemoryHotQualityStore(options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store
}

func testScope(account string) HotQualityScope {
	return HotQualityScope{AccountRuntimeKey: account, ProtocolProfile: "openai:2024", RequestLane: "text", ModelFamily: "model-bucket-01"}
}

func TestMemoryHotQualityStoreConstructorValidation(t *testing.T) {
	zero := 0
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{KeyCapacity: &zero}); err == nil || err.Error() != "keyCapacity 必须是正整数" {
		t.Fatalf("err = %v", err)
	}
	shortTTL := int64(1000)
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{TerminalTtlMs: &shortTTL}); err == nil || err.Error() != "terminalTtlMs 不得少于 3600000ms" {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{AttemptCapacity: &zero}); err == nil || err.Error() != "attemptCapacity 必须是正整数" {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryHotQualityStoreAttemptLifecycleStatuses(t *testing.T) {
	store := newTestMemoryStore(t, nil)
	ctx := context.Background()
	now := int64(1_000_000)

	// applied
	result, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a1", Scope: testScope("acc1"), NowMs: &now})
	if err != nil || result.Status != AttemptMutationApplied {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.EffectiveScope != testScope("acc1") {
		t.Fatalf("effectiveScope = %+v", result.EffectiveScope)
	}
	// idempotent
	result, err = store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a1", Scope: testScope("acc1"), NowMs: &now})
	if err != nil || result.Status != AttemptMutationIdempotent {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	// attempt_conflict (same id, different scope)
	result, err = store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a1", Scope: testScope("acc2"), NowMs: &now})
	if err != nil || result.Status != AttemptMutationAttemptConflict {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.EffectiveScope.AccountRuntimeKey != "acc1" {
		t.Fatalf("conflict effectiveScope = %+v", result.EffectiveScope)
	}
	// snapshot has attempts=1 (only the first counted)
	snapshot, err := store.Get(ctx, testScope("acc1"), &now)
	if err != nil || snapshot == nil || snapshot.Window5m.Attempts != 1 {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
	if snapshot.SampleState != HotQualitySampleCold {
		t.Fatalf("sampleState = %s", snapshot.SampleState)
	}
}

func TestMemoryHotQualityStoreTerminalTransitions(t *testing.T) {
	testCases := []struct {
		name         string
		outcomeClass string
		failureScope string
		source       string
		firstByteMs  *float64
	}{
		{"completed with first byte", TerminalOutcomeCompletedResponse, FailureScopeNone, TerminalSourceGatewayTransport, floatPtr(1234.6)},
		{"upstream failure ignores first byte", TerminalOutcomeUpstreamResponseFailure, FailureScopeUpstreamBucket, TerminalSourceUpstreamResponse, floatPtr(100)},
		{"policy failure", TerminalOutcomeExplicitPolicyFailure, FailureScopeAccount, TerminalSourceExplicitPolicy, nil},
		{"transport failure", TerminalOutcomeTransportFailure, FailureScopeKey, TerminalSourceGatewayTransport, nil},
		{"timeout", TerminalOutcomeTimeout, FailureScopeKey, TerminalSourceGatewayTransport, floatPtr(5000)},
		{"read interruption", TerminalOutcomeReadInterruption, FailureScopeKey, TerminalSourceGatewayTransport, nil},
		{"incomplete", TerminalOutcomeIncompleteResponse, FailureScopeKey, TerminalSourceRequestLifecycle, nil},
		{"unknown", TerminalOutcomeUnknown, FailureScopeNone, TerminalSourceRequestLifecycle, floatPtr(10)},
		{"client cancellation", TerminalOutcomeClientCancellation, FailureScopeNone, TerminalSourceRequestLifecycle, nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newTestMemoryStore(t, nil)
			ctx := context.Background()
			now := int64(2_000_000)
			if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "at", Scope: testScope("acc"), NowMs: &now}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			result, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
				AttemptID:         "at",
				Scope:             testScope("acc"),
				TerminalOutcomeID: "t1",
				OutcomeClass:      testCase.outcomeClass,
				FailureScope:      testCase.failureScope,
				Source:            testCase.source,
				FirstByteMs:       testCase.firstByteMs,
				NowMs:             &now,
			})
			if err != nil || result.Status != TerminalMutationApplied {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if result.Terminal.CreatedAtMs != now || result.Terminal.OutcomeClass != testCase.outcomeClass {
				t.Fatalf("terminal = %+v", result.Terminal)
			}
			snapshot, err := store.Get(ctx, testScope("acc"), &now)
			if err != nil || snapshot == nil {
				t.Fatalf("snapshot missing: %v", err)
			}
			bucket := snapshot.Window5m
			sampleExcluded := testCase.outcomeClass == TerminalOutcomeUpstreamResponseFailure ||
				testCase.outcomeClass == TerminalOutcomeUnknown ||
				testCase.outcomeClass == TerminalOutcomeClientCancellation
			wantSamples := int64(0)
			if testCase.firstByteMs != nil && !sampleExcluded {
				wantSamples = 1
			}
			if bucket.FirstByteSampleCount != wantSamples {
				t.Fatalf("samples = %d, want %d", bucket.FirstByteSampleCount, wantSamples)
			}
			// terminal record queryable
			terminal, err := store.GetTerminal(ctx, "at", &now)
			if err != nil || terminal == nil || terminal.TerminalOutcomeID != "t1" {
				t.Fatalf("terminal = %+v, err = %v", terminal, err)
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }

func TestMemoryHotQualityStoreTerminalConflictPaths(t *testing.T) {
	store := newTestMemoryStore(t, nil)
	ctx := context.Background()
	now := int64(3_000_000)
	record := func() {
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "at", Scope: testScope("acc"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	terminalInput := HotQualityRecordTerminalInput{
		AttemptID:         "at",
		Scope:             testScope("acc"),
		TerminalOutcomeID: "t1",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
		NowMs:             &now,
	}

	t.Run("attempt not found", func(t *testing.T) {
		result, err := store.RecordTerminal(ctx, terminalInput)
		if err != nil || result.Status != TerminalMutationAttemptNotFound {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})

	t.Run("attempt conflict", func(t *testing.T) {
		record()
		wrong := terminalInput
		wrong.Scope = testScope("other")
		result, err := store.RecordTerminal(ctx, wrong)
		if err != nil || result.Status != TerminalMutationAttemptConflict || result.EffectiveScope == nil || result.EffectiveScope.AccountRuntimeKey != "acc" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})

	t.Run("idempotent then terminal conflict", func(t *testing.T) {
		result, err := store.RecordTerminal(ctx, terminalInput)
		if err != nil || result.Status != TerminalMutationApplied {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		same, err := store.RecordTerminal(ctx, terminalInput)
		if err != nil || same.Status != TerminalMutationIdempotent || same.Terminal == nil {
			t.Fatalf("result = %+v, err = %v", same, err)
		}
		different := terminalInput
		different.OutcomeClass = TerminalOutcomeTimeout
		conflict, err := store.RecordTerminal(ctx, different)
		if err != nil || conflict.Status != TerminalMutationTerminalConflict {
			t.Fatalf("result = %+v, err = %v", conflict, err)
		}
		// window counters unchanged after replays
		snapshot, _ := store.Get(ctx, testScope("acc"), &now)
		if snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.LocalTransportFailures != 0 {
			t.Fatalf("counters polluted by replays: %+v", snapshot.Window5m)
		}
	})

	t.Run("terminal outcome conflict", func(t *testing.T) {
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "b", Scope: testScope("acc"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		steal := terminalInput
		steal.AttemptID = "b"
		result, err := store.RecordTerminal(ctx, steal)
		if err != nil || result.Status != TerminalMutationTerminalOutcomeConflict {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
}

func TestMemoryHotQualityStoreCapacityAndDegradation(t *testing.T) {
	t.Run("attempt capacity exhausted", func(t *testing.T) {
		capacity := 1
		store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) { options.AttemptCapacity = &capacity })
		ctx := context.Background()
		now := int64(1_000)
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		result, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "b", Scope: testScope("x"), NowMs: &now})
		if err != nil || result.Status != AttemptMutationAttemptCapacityExhausted {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		stats, _ := store.Stats(ctx, &now)
		if stats.AttemptCapacityRefusals != 1 {
			t.Fatalf("refusals = %d", stats.AttemptCapacityRefusals)
		}
	})

	t.Run("key capacity degrades to protocol scope", func(t *testing.T) {
		capacity := 2
		store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) { options.KeyCapacity = &capacity })
		ctx := context.Background()
		now := int64(1_000)
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// seed the protocol-scope fallback key for account y (the degraded
		// request keeps its own accountRuntimeKey)
		protocolScope := testScope("y")
		protocolScope.ModelFamily = HotQualityUnknownModelFamily
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "b", Scope: protocolScope, NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// capacity is now full: a third family degrades to the protocol scope
		degraded := testScope("y")
		degraded.ModelFamily = "model-bucket-02"
		result, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "c", Scope: degraded, NowMs: &now})
		if err != nil || result.Status != AttemptMutationDegradedToProtocol {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if result.EffectiveScope.ModelFamily != HotQualityUnknownModelFamily || result.EffectiveScope.AccountRuntimeKey != "y" {
			t.Fatalf("effectiveScope = %+v", result.EffectiveScope)
		}
		stats, _ := store.Stats(ctx, &now)
		if stats.HighCardinalityDegradations != 1 {
			t.Fatalf("degradations = %d", stats.HighCardinalityDegradations)
		}
		// the degraded attempt bumps the protocol-scope entry (its effective key)
		snapshot, _ := store.Get(ctx, protocolScope, &now)
		if snapshot == nil || snapshot.Window5m.Attempts != 2 {
			t.Fatalf("protocol snapshot = %+v", snapshot)
		}
	})

	t.Run("key capacity exhausted without fallback", func(t *testing.T) {
		capacity := 1
		store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) { options.KeyCapacity = &capacity })
		ctx := context.Background()
		now := int64(1_000)
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		other := testScope("y")
		other.ProtocolProfile = "other:1"
		result, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "b", Scope: other, NowMs: &now})
		if err != nil || result.Status != AttemptMutationKeyCapacityExhausted {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		stats, _ := store.Stats(ctx, &now)
		if stats.KeyCreationRefusals != 1 {
			t.Fatalf("refusals = %d", stats.KeyCreationRefusals)
		}
	})

	t.Run("terminal recreates expired key within capacity", func(t *testing.T) {
		capacity := 1
		store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) { options.KeyCapacity = &capacity })
		ctx := context.Background()
		now := int64(1_000)
		if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &now}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// expire the hot key via clock advance; attempt identity stays fresh
		later := now + HotQualityKeyTTLMS + 1
		result, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
			AttemptID:         "a",
			Scope:             testScope("x"),
			TerminalOutcomeID: "t",
			OutcomeClass:      TerminalOutcomeCompletedResponse,
			FailureScope:      FailureScopeNone,
			Source:            TerminalSourceGatewayTransport,
			NowMs:             &later,
		})
		if err != nil || result.Status != TerminalMutationApplied {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
}

func TestMemoryHotQualityStoreQualityKeyUnavailableWhenFull(t *testing.T) {
	keyCapacity := 1
	store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) {
		options.KeyCapacity = &keyCapacity
	})
	ctx := context.Background()
	now := int64(1_000)
	// fill the single key slot with profile A
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &now}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// expire profile A's key; attempt identity stays fresh (60min > 40min)
	later := now + HotQualityKeyTTLMS + 1
	// profile B takes the now-free capacity slot
	other := testScope("y")
	other.ProtocolProfile = "other:1"
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "c", Scope: other, NowMs: &later}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// terminal for attempt "a": its effective key expired and capacity is full → unavailable
	terminal := HotQualityRecordTerminalInput{
		AttemptID:         "a",
		Scope:             testScope("x"),
		TerminalOutcomeID: "t-a",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
		NowMs:             &later,
	}
	result, err := store.RecordTerminal(ctx, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TerminalMutationQualityKeyUnavailable || result.EffectiveScope == nil {
		t.Fatalf("result = %+v", result)
	}
	stats, _ := store.Stats(ctx, &later)
	if stats.TerminalQualityKeyMisses != 1 {
		t.Fatalf("misses = %d", stats.TerminalQualityKeyMisses)
	}
}

func TestMemoryHotQualityStoreExpiry(t *testing.T) {
	now := int64(1_000_000)
	clock := now
	store := newTestMemoryStore(t, func(options *MemoryHotQualityStoreOptions) {
		options.Now = func() int64 { return clock }
	})
	ctx := context.Background()
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID:         "a",
		Scope:             testScope("x"),
		TerminalOutcomeID: "t",
		OutcomeClass:      TerminalOutcomeCompletedResponse,
		FailureScope:      FailureScopeNone,
		Source:            TerminalSourceGatewayTransport,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stats, _ := store.Stats(ctx, nil)
	if stats.KeyCount != 1 || stats.AttemptIdentityCount != 1 || stats.TerminalIdentityCount != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// advance past key TTL (40min) but within terminal TTL (60min)
	clock = now + HotQualityKeyTTLMS + 1
	snapshot, err := store.Get(ctx, testScope("x"), nil)
	if err != nil || snapshot != nil {
		t.Fatalf("expired key must read as absent: %v, %v", snapshot, err)
	}
	// attempt identity + terminal still fresh
	terminal, err := store.GetTerminal(ctx, "a", nil)
	if err != nil || terminal == nil {
		t.Fatalf("terminal = %+v, err = %v", terminal, err)
	}
	stats, _ = store.Stats(ctx, nil)
	if stats.KeyCount != 0 || stats.AttemptIdentityCount != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// advance past terminal TTL
	clock = now + HotQualityTerminalTTLMS + 1
	terminal, err = store.GetTerminal(ctx, "a", nil)
	if err != nil || terminal != nil {
		t.Fatalf("expired terminal must read as absent: %v, %v", terminal, err)
	}
	stats, _ = store.Stats(ctx, nil)
	if stats.AttemptIdentityCount != 0 || stats.TerminalIdentityCount != 0 {
		t.Fatalf("expired terminal owner must be cleaned: %+v", stats)
	}
}

func TestMemoryHotQualityStoreInputValidation(t *testing.T) {
	store := newTestMemoryStore(t, nil)
	ctx := context.Background()
	longID := make([]byte, 257)
	for i := range longID {
		longID[i] = 'a'
	}
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: string(longID), Scope: testScope("x")}); err == nil || err.Error() != "attemptId 必须是 1 到 256 字符" {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "  "}); err == nil || err.Error() != "attemptId 必须是 1 到 256 字符" {
		t.Fatalf("err = %v", err)
	}
	negative := int64(-1)
	if _, err := store.RecordAttempt(ctx, HotQualityRecordAttemptInput{AttemptID: "a", Scope: testScope("x"), NowMs: &negative}); err == nil || err.Error() != "nowMs 必须是非负安全整数" {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "a", Scope: testScope("x"), TerminalOutcomeID: "t",
		OutcomeClass: "exploded", FailureScope: FailureScopeNone, Source: TerminalSourceGatewayTransport,
	}); err == nil || err.Error() != "热质量 outcomeClass 非法" {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "a", Scope: testScope("x"), TerminalOutcomeID: "t",
		OutcomeClass: TerminalOutcomeCompletedResponse, FailureScope: "galaxy", Source: TerminalSourceGatewayTransport,
	}); err == nil || err.Error() != "热质量 failureScope 非法" {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "a", Scope: testScope("x"), TerminalOutcomeID: "t",
		OutcomeClass: TerminalOutcomeCompletedResponse, FailureScope: FailureScopeNone, Source: "telepathy",
	}); err == nil || err.Error() != "热质量 terminal source 非法" {
		t.Fatalf("err = %v", err)
	}
	negativeFirstByte := -5.0
	if _, err := store.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID: "a", Scope: testScope("x"), TerminalOutcomeID: "t",
		OutcomeClass: TerminalOutcomeCompletedResponse, FailureScope: FailureScopeNone, Source: TerminalSourceGatewayTransport,
		FirstByteMs: &negativeFirstByte,
	}); err == nil || err.Error() != "首字耗时必须是非负有限数值" {
		t.Fatalf("err = %v", err)
	}
}
