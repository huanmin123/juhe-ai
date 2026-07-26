package gatewayhotquality

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestStoreRecordsOneTerminalAndNeutralOutcomesDoNotAffectQuality(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{})
	scope := testScope("runtime-a", "model-a")
	assertAttemptStatus(t, store, "attempt-complete", scope, now, AttemptApplied)
	firstByte := 125 * time.Millisecond
	mutation, err := store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-complete", Scope: scope, OutcomeID: "terminal-complete", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, FirstByte: &firstByte, Now: now})
	if err != nil || mutation.Status != TerminalApplied {
		t.Fatalf("completed terminal = %#v err=%v", mutation, err)
	}
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-complete", Scope: scope, OutcomeID: "terminal-complete", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, FirstByte: &firstByte, Now: now})
	if err != nil || mutation.Status != TerminalIdempotent {
		t.Fatalf("replayed terminal = %#v err=%v", mutation, err)
	}
	assertAttemptStatus(t, store, "attempt-unknown", scope, now, AttemptApplied)
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-unknown", Scope: scope, OutcomeID: "terminal-unknown", Outcome: OutcomeUnknown, Failure: FailureScopeNone, Source: TerminalSourceRequestLife, Now: now})
	if err != nil || mutation.Status != TerminalApplied {
		t.Fatalf("unknown terminal = %#v err=%v", mutation, err)
	}
	assertAttemptStatus(t, store, "attempt-opaque", scope, now, AttemptApplied)
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-opaque", Scope: scope, OutcomeID: "terminal-opaque", Outcome: OutcomeUpstreamResponseFailed, Failure: FailureScopeNone, Source: TerminalSourceUpstream, FirstByte: &firstByte, Now: now})
	if err != nil || mutation.Status != TerminalApplied {
		t.Fatalf("opaque terminal = %#v err=%v", mutation, err)
	}
	snapshot, found, err := store.Snapshot(scope, now)
	if err != nil || !found {
		t.Fatalf("snapshot found=%v err=%v", found, err)
	}
	if snapshot.Window5m.Attempts != 3 || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.UnknownOutcomes != 1 || snapshot.Window5m.UpstreamResponseFailures != 1 || snapshot.Window5m.QualityAttempts != 1 || snapshot.Window5m.FirstByteSampleCount != 1 {
		t.Fatalf("window = %#v", snapshot.Window5m)
	}
	if snapshot.SampleState != SampleStateWarming || snapshot.ReliabilityLevel != ReliabilityUnknown {
		t.Fatalf("sample=%q reliability=%q", snapshot.SampleState, snapshot.ReliabilityLevel)
	}
	if snapshot.FirstByteEWMA5m == nil || *snapshot.FirstByteEWMA5m != firstByte || snapshot.FirstByteP95Bucket10m == nil || *snapshot.FirstByteP95Bucket10m != 0 {
		t.Fatalf("latency projection = %#v", snapshot)
	}
}

func TestStoreFencesAttemptAndTerminalIdentities(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{})
	scope := testScope("runtime-a", "model-a")
	other := testScope("runtime-b", "model-b")
	assertAttemptStatus(t, store, "attempt-a", scope, now, AttemptApplied)
	assertAttemptStatus(t, store, "attempt-a", scope, now, AttemptIdempotent)
	assertAttemptStatus(t, store, "attempt-a", other, now, AttemptConflict)
	mutation, err := store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-a", Scope: other, OutcomeID: "terminal-a", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || mutation.Status != TerminalAttemptConflict {
		t.Fatalf("scope mismatch = %#v err=%v", mutation, err)
	}
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-a", Scope: scope, OutcomeID: "terminal-a", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || mutation.Status != TerminalApplied {
		t.Fatalf("first terminal = %#v err=%v", mutation, err)
	}
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-a", Scope: scope, OutcomeID: "terminal-b", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || mutation.Status != TerminalConflict {
		t.Fatalf("different terminal = %#v err=%v", mutation, err)
	}
	assertAttemptStatus(t, store, "attempt-b", scope, now, AttemptApplied)
	mutation, err = store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt-b", Scope: scope, OutcomeID: "terminal-a", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || mutation.Status != TerminalOutcomeConflict {
		t.Fatalf("terminal identity conflict = %#v err=%v", mutation, err)
	}
}

func TestStoreTTLAndExistingProtocolFallbackStayBounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{KeyCapacity: 1, AttemptCapacity: 3, KeyTTL: time.Minute, TerminalTTL: 2 * time.Minute})
	protocol := testScope("runtime-a", UnknownModelFamily)
	model := testScope("runtime-a", "model-a")
	assertAttemptStatus(t, store, "protocol", protocol, now, AttemptApplied)
	assertAttemptStatus(t, store, "model", model, now, AttemptDegraded)
	mutation, err := store.RecordTerminal(RecordTerminalInput{AttemptID: "model", Scope: model, OutcomeID: "model-terminal", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || mutation.Status != TerminalApplied || mutation.EffectiveScope == nil || !reflect.DeepEqual(*mutation.EffectiveScope, protocol) {
		t.Fatalf("fallback terminal = %#v err=%v", mutation, err)
	}
	if _, found, err := store.Snapshot(protocol, now.Add(time.Minute)); err != nil || found {
		t.Fatalf("expired snapshot found=%v err=%v", found, err)
	}
	terminal, err := store.Terminal("model", now.Add(time.Minute))
	if err != nil || terminal == nil {
		t.Fatalf("terminal after key expiry = %#v err=%v", terminal, err)
	}
	if _, err := store.Terminal("model", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stats := store.Stats(now.Add(2 * time.Minute)); stats.AttemptIdentityCount != 0 || stats.TerminalIdentityCount != 0 {
		t.Fatalf("expired stats = %#v", stats)
	}
}

func TestStoreConcurrentTerminalReplayCountsOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{})
	scope := testScope("runtime-a", "model-a")
	assertAttemptStatus(t, store, "attempt", scope, now, AttemptApplied)
	var group sync.WaitGroup
	results := make(chan TerminalMutationStatus, 32)
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			mutation, err := store.RecordTerminal(RecordTerminalInput{AttemptID: "attempt", Scope: scope, OutcomeID: "terminal", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
			if err != nil {
				results <- "error"
				return
			}
			results <- mutation.Status
		}()
	}
	group.Wait()
	close(results)
	counts := map[TerminalMutationStatus]int{}
	for result := range results {
		counts[result]++
	}
	if counts[TerminalApplied] != 1 || counts[TerminalIdempotent] != 31 || len(counts) != 2 {
		t.Fatalf("terminal statuses = %#v", counts)
	}
	snapshot, found, err := store.Snapshot(scope, now)
	if err != nil || !found || snapshot.Window5m.CompletedResponses != 1 {
		t.Fatalf("snapshot=%#v found=%v err=%v", snapshot, found, err)
	}
}

func TestLifecycleCachesFirstByteAndFirstTerminal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{})
	scope := testScope("runtime-a", "model-a")
	lifecycle, mutation, err := StartLifecycle(store, "attempt", scope, now)
	if err != nil || mutation.Status != AttemptApplied {
		t.Fatalf("start = %#v err=%v", mutation, err)
	}
	lifecycle.MarkFirstByte(25 * time.Millisecond)
	lifecycle.MarkFirstByte(50 * time.Millisecond)
	first, err := lifecycle.RecordTerminal(LifecycleTerminalInput{OutcomeID: "terminal", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now})
	if err != nil || first.Status != TerminalApplied {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	second, err := lifecycle.RecordTerminal(LifecycleTerminalInput{OutcomeID: "other", Outcome: OutcomeTransportFailed, Failure: FailureScopeAccount, Source: TerminalSourceTransport, Now: now})
	if err != nil || second.Status != TerminalApplied || second.Terminal == nil || second.Terminal.OutcomeID != "terminal" {
		t.Fatalf("second = %#v err=%v", second, err)
	}
	snapshot, found, err := store.Snapshot(scope, now)
	if err != nil || !found || snapshot.Window5m.FirstByteSum != 25*time.Millisecond || snapshot.Window5m.CompletedResponses != 1 {
		t.Fatalf("snapshot=%#v found=%v err=%v", snapshot, found, err)
	}
}

func TestLifecycleDoesNotSealRejectedTerminal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newRuntimeStore(t, StoreOptions{KeyTTL: time.Minute, TerminalTTL: 2 * time.Minute})
	scope := testScope("runtime-a", "model-a")
	lifecycle, mutation, err := StartLifecycle(store, "attempt", scope, now)
	if err != nil || mutation.Status != AttemptApplied {
		t.Fatalf("start=%#v err=%v", mutation, err)
	}
	missing, err := lifecycle.RecordTerminal(LifecycleTerminalInput{OutcomeID: "terminal", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now.Add(2 * time.Minute)})
	if err != nil || missing.Status != TerminalAttemptMissing {
		t.Fatalf("expired terminal=%#v err=%v", missing, err)
	}
	assertAttemptStatus(t, store, "attempt", scope, now.Add(2*time.Minute), AttemptApplied)
	applied, err := lifecycle.RecordTerminal(LifecycleTerminalInput{OutcomeID: "terminal", Outcome: OutcomeCompletedResponse, Failure: FailureScopeNone, Source: TerminalSourceTransport, Now: now.Add(2 * time.Minute)})
	if err != nil || applied.Status != TerminalApplied {
		t.Fatalf("retried terminal=%#v err=%v", applied, err)
	}
}

func newRuntimeStore(t *testing.T, options StoreOptions) *Store {
	t.Helper()
	store, err := NewStore(options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testScope(runtime, model string) Scope {
	return Scope{AccountRuntimeKey: runtime, ProtocolProfile: "openai-v1", RequestLane: RequestLaneText, ModelFamily: model}
}

func assertAttemptStatus(t *testing.T, store *Store, attemptID string, scope Scope, now time.Time, want AttemptMutationStatus) {
	t.Helper()
	mutation, err := store.RecordAttempt(RecordAttemptInput{AttemptID: attemptID, Scope: scope, Now: now})
	if err != nil || mutation.Status != want {
		t.Fatalf("attempt %q = %#v err=%v, want %q", attemptID, mutation, err, want)
	}
}
