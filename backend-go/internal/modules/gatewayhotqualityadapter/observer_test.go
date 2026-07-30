package gatewayhotqualityadapter

import (
	"context"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayhotquality"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	"juhe-ai/backend-go/internal/store/port"
)

func TestObserverProjectsFirstVisibleByteAndSuccessOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	observer := newObserver(t, store, now, Options{})
	observation := testObservation("attempt-1", now)
	observer.Start(context.Background(), observation)
	observer.Start(context.Background(), observation)
	observer.FirstByte(context.Background(), observation, now.Add(125*time.Millisecond))
	observer.FirstByte(context.Background(), observation, now.Add(250*time.Millisecond))
	terminal := gatewayattemptloop.AttemptTerminalObservation{Valid: true, Success: true, Committed: true, CompletedAt: now.Add(time.Second)}
	observer.Terminal(context.Background(), observation, terminal)
	observer.Terminal(context.Background(), observation, terminal)

	snapshot, found, err := store.Snapshot(scopeForTest(), now.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("snapshot found=%v err=%v", found, err)
	}
	if snapshot.Window5m.Attempts != 1 || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.QualityAttempts != 1 || snapshot.Window5m.FirstByteSampleCount != 1 || snapshot.Window5m.FirstByteSum != 125*time.Millisecond {
		t.Fatalf("window=%#v", snapshot.Window5m)
	}
	if observer.ActiveCount(now.Add(time.Second)) != 0 {
		t.Fatal("completed lifecycle remained active")
	}
	terminalRecord, err := store.Terminal("attempt-1", now.Add(time.Second))
	if err != nil || terminalRecord == nil || terminalRecord.OutcomeClass != gatewayhotquality.OutcomeCompletedResponse {
		t.Fatalf("terminal=%#v err=%v", terminalRecord, err)
	}
}

func TestObserverFailsClosedAndKeepsUntypedFailuresNeutral(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	observer := newObserver(t, store, now, Options{})
	bad := testObservation("bad", now)
	bad.AccountRuntime = ""
	observer.Start(context.Background(), bad)
	if stats := store.Stats(now); stats.AttemptIdentityCount != 0 || stats.KeyCount != 0 {
		t.Fatalf("bad observation mutated store: %#v", stats)
	}

	invalid := testObservation("invalid", now)
	observer.Start(context.Background(), invalid)
	observer.FirstByte(context.Background(), invalid, now.Add(time.Millisecond))
	observer.Terminal(context.Background(), invalid, gatewayattemptloop.AttemptTerminalObservation{ErrorCode: "first_byte_timeout", CompletedAt: now})
	downstream := testObservation("downstream", now)
	observer.Start(context.Background(), downstream)
	observer.FirstByte(context.Background(), downstream, now.Add(time.Millisecond))
	observer.Terminal(context.Background(), downstream, gatewayattemptloop.AttemptTerminalObservation{Valid: true, FailureAttribution: gatewayusage.FailureAttributionDownstreamClosed, CompletedAt: now})
	upstream := testObservation("upstream", now)
	observer.Start(context.Background(), upstream)
	observer.FirstByte(context.Background(), upstream, now.Add(time.Millisecond))
	observer.Terminal(context.Background(), upstream, gatewayattemptloop.AttemptTerminalObservation{Valid: true, StatusCode: 503, ErrorCode: "first_byte_timeout", FailureAttribution: gatewayusage.FailureAttributionAccountUpstream, CompletedAt: now})

	snapshot, found, err := store.Snapshot(scopeForTest(), now)
	if err != nil || !found {
		t.Fatalf("snapshot found=%v err=%v", found, err)
	}
	if snapshot.Window5m.Attempts != 3 || snapshot.Window5m.QualityAttempts != 0 || snapshot.Window5m.FirstByteSampleCount != 0 || snapshot.Window5m.UnknownOutcomes != 3 || snapshot.Window5m.ClientCancellations != 0 {
		t.Fatalf("untyped terminal projection=%#v", snapshot.Window5m)
	}
}

func TestObserverBoundsActiveLifecyclesAndIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	observer := newObserver(t, store, now, Options{MaxActive: 1})
	first := testObservation("first", now)
	second := testObservation("second", now)
	observer.Start(context.Background(), first)
	observer.Start(context.Background(), second)
	if observer.ActiveCount(now) != 1 || store.Stats(now).AttemptIdentityCount != 1 {
		t.Fatalf("active=%d stats=%#v", observer.ActiveCount(now), store.Stats(now))
	}

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			observer.FirstByte(context.Background(), first, now.Add(40*time.Millisecond))
			observer.Terminal(context.Background(), first, gatewayattemptloop.AttemptTerminalObservation{Valid: true, Success: true, Committed: true, CompletedAt: now.Add(time.Second)})
		}()
	}
	group.Wait()
	snapshot, found, err := store.Snapshot(scopeForTest(), now.Add(time.Second))
	if err != nil || !found || snapshot.Window5m.Attempts != 1 || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.FirstByteSampleCount != 1 {
		t.Fatalf("snapshot=%#v found=%v err=%v", snapshot, found, err)
	}
}

func TestObserverReleasesLongLivedAttemptWhenStoreIdentityExpires(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	observer := newObserver(t, store, now, Options{ActiveTTL: 2 * time.Hour})
	observation := testObservation("expired-attempt", now)
	observer.Start(context.Background(), observation)
	observer.Terminal(context.Background(), observation, gatewayattemptloop.AttemptTerminalObservation{Valid: true, Success: true, Committed: true, CompletedAt: now.Add(61 * time.Minute)})
	if observer.ActiveCount(now.Add(61*time.Minute)) != 0 {
		t.Fatal("expired store identity retained an active lifecycle")
	}
}

func TestObserverIntegratesWithAttemptLoop(t *testing.T) {
	now := time.Now().UTC()
	store := newStore(t)
	observer := newObserver(t, store, now, Options{})
	executor := loopExecutor{now: now}
	service, err := gatewayattemptloop.NewService(executor, nil, gatewayattemptloop.Config{WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service.WithNow(func() time.Time { return now })
	result, err := service.Run(gatewayattemptloop.Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{{Projection: port.GatewayAccountCandidate{AccountID: "runtime-a", Type: "oauth", ProviderProtocolProfileID: "openai-v1"}}}, Observer: observer})
	if err != nil || !result.LastAttempt.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	scope := scopeForTest()
	scope.ProtocolProfile = "openai-v1"
	scope.ModelFamily = "unknown"
	snapshot, found, err := store.Snapshot(scope, now)
	if err != nil || !found || snapshot.Window5m.CompletedResponses != 1 || snapshot.Window5m.FirstByteSampleCount != 1 {
		t.Fatalf("snapshot=%#v found=%v err=%v", snapshot, found, err)
	}
}

type loopExecutor struct{ now time.Time }

func (e loopExecutor) Execute(_ context.Context, attempt gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
	if attempt.OnFirstByte != nil {
		attempt.OnFirstByte(e.now.Add(75 * time.Millisecond))
	}
	return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
}

func newStore(t *testing.T) *gatewayhotquality.Store {
	t.Helper()
	store, err := gatewayhotquality.NewStore(gatewayhotquality.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newObserver(t *testing.T, store *gatewayhotquality.Store, now time.Time, options Options) *Observer {
	t.Helper()
	options.Now = func() time.Time { return now }
	observer, err := New(store, options)
	if err != nil {
		t.Fatal(err)
	}
	return observer
}

func testObservation(id string, startedAt time.Time) gatewayattemptloop.AttemptObservation {
	return gatewayattemptloop.AttemptObservation{ID: id, AccountRuntime: "runtime-a", ProtocolProfile: "openai-v1", RequestLane: "text", ModelBucket: "model-bucket-12", StartedAt: startedAt}
}

func scopeForTest() gatewayhotquality.Scope {
	return gatewayhotquality.Scope{AccountRuntimeKey: "runtime-a", ProtocolProfile: "openai-v1", RequestLane: gatewayhotquality.RequestLaneText, ModelFamily: "model-bucket-12"}
}
