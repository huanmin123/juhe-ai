package gatewayproxyhealth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func gatewayConsumeInput(systemAccountID string, settings gatewayruntimecache.GatewaySettings, overrides *gatewayruntimecache.UserRequestLimits, nowMs *int64) gatewaypreauth.UserRequestLimitConsumeInput {
	return gatewaypreauth.UserRequestLimitConsumeInput{
		SystemAccountID: systemAccountID,
		Settings:        settings,
		Overrides:       overrides,
		NowMs:           nowMs,
	}
}

// evalCall captures one fake Redis eval invocation.
type evalCall struct {
	script string
	keys   []string
	args   []any
}

// fakeEvalClient records evals and replays canned results or errors.
type fakeEvalClient struct {
	mu        sync.Mutex
	calls     []evalCall
	results   [][]any // one result array per call (cycled)
	failIndex int     // fail the call at this index; -1 disables
	failAll   bool    // fail every call
	blockCh   chan struct{}
	evals     atomic.Int64
}

func (c *fakeEvalClient) Eval(_ context.Context, script string, keys []string, args ...any) (any, error) {
	c.mu.Lock()
	index := len(c.calls)
	c.calls = append(c.calls, evalCall{script: script, keys: keys, args: args})
	c.mu.Unlock()
	c.evals.Add(1)
	if c.blockCh != nil {
		<-c.blockCh
	}
	if c.failAll || index == c.failIndex {
		return nil, errors.New("redis down")
	}
	var result []any
	if len(c.results) > 0 {
		result = c.results[index%len(c.results)]
	}
	return result, nil
}

func (c *fakeEvalClient) recordedCalls() []evalCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]evalCall(nil), c.calls...)
}

// fakeClientProvider hands out the fake client and tracks invalidation.
type fakeClientProvider struct {
	client      *fakeEvalClient
	invalidated atomic.Int64
	clientErrAt atomic.Int64
	calls       atomic.Int64
}

func (p *fakeClientProvider) Client(context.Context) (UserRequestLimitRedisClient, error) {
	if p.calls.Add(1) == p.clientErrAt.Load() {
		return nil, errors.New("connect failed")
	}
	return p.client, nil
}

func (p *fakeClientProvider) Invalidate(context.Context, UserRequestLimitRedisClient) {
	p.invalidated.Add(1)
}

func newSyncCoordinator(clock *fakeClock, client *fakeEvalClient, provider *fakeClientProvider) (*UserRequestLimitCoordinator, *UserRequestLimitCounter) {
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{
		RedisEnabled:   true,
		Namespace:      "juhe-ai",
		ClientProvider: provider,
	})
	return coordinator, counter
}

func TestUserRequestLimitCoordinatorSyncsDirtyCounters(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	client := &fakeEvalClient{results: [][]any{{int64(7)}}, failIndex: -1}
	provider := &fakeClientProvider{client: client}
	coordinator, counter := newSyncCoordinator(clock, client, provider)

	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	if err := coordinator.SynchronizeDirtyCounters(false); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	calls := client.recordedCalls()
	if len(calls) != 1 {
		t.Fatalf("eval calls = %d", len(calls))
	}
	call := calls[0]
	if call.script != UserRequestLimitRedisSyncScript {
		t.Fatal("coordinator must run the exact Node sync script")
	}
	if len(call.keys) != 1 || call.keys[0] != "juhe-ai:gateway:user-request-limit:perMinute:29500000:u1" {
		t.Fatalf("redis keys = %v", call.keys)
	}
	// ARGV = [instanceId, localCount, ttlMs].
	if len(call.args) != 3 {
		t.Fatalf("args = %v", call.args)
	}
	if call.args[1] != "2" || call.args[2] != "120000" {
		t.Fatalf("count/ttl args = %v", call.args)
	}

	// applySyncResults: remote total 7 wins, dirty flag clears, next sync is a no-op.
	stats := counter.Stats()
	if stats.DirtyEntries != 0 {
		t.Fatalf("dirty after sync = %d", stats.DirtyEntries)
	}
	if err := coordinator.SynchronizeDirtyCounters(false); err != nil {
		t.Fatal(err)
	}
	if len(client.recordedCalls()) != 1 {
		t.Fatal("clean counter must not trigger another eval")
	}
}

func TestUserRequestLimitCoordinatorSingleFlight(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	client := &fakeEvalClient{results: [][]any{{int64(1)}}, failIndex: -1, blockCh: make(chan struct{})}
	provider := &fakeClientProvider{client: client}
	coordinator, counter := newSyncCoordinator(clock, client, provider)
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	done := make(chan struct{})
	go func() {
		_ = coordinator.SynchronizeDirtyCounters(false)
		close(done)
	}()
	// Give the first sync time to claim the single-flight slot, then try again.
	time.Sleep(50 * time.Millisecond)
	if err := coordinator.SynchronizeDirtyCounters(false); err != nil {
		t.Fatal(err)
	}
	if got := client.evals.Load(); got != 1 {
		t.Fatalf("evals = %d, want single-flight 1", got)
	}
	close(client.blockCh)
	<-done
}

func TestUserRequestLimitCoordinatorBackoffAndInvalidate(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	client := &fakeEvalClient{failIndex: 0}
	provider := &fakeClientProvider{client: client}
	coordinator, counter := newSyncCoordinator(clock, client, provider)
	log := &recordingLog{}
	coordinator.opts.Log = log.record
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	if err := coordinator.SynchronizeDirtyCounters(false); err == nil {
		t.Fatal("expected the redis error to propagate")
	}
	if provider.invalidated.Load() != 1 {
		t.Fatalf("invalidations = %d", provider.invalidated.Load())
	}
	// The failure schedules a 1s retry; an immediate non-forced sync is a
	// silent no-op (Node returns without scheduling).
	if err := coordinator.SynchronizeDirtyCounters(false); err != nil {
		t.Fatalf("gated retry must be a no-op: %v", err)
	}
	if len(client.recordedCalls()) != 1 {
		t.Fatalf("gated retry ran an eval: %d", len(client.recordedCalls()))
	}
	// Force bypasses the backoff and succeeds.
	clock.Set(clock.NowMs() + 5_000)
	if err := coordinator.SynchronizeDirtyCounters(true); err != nil {
		t.Fatal(err)
	}
	if len(client.recordedCalls()) != 2 {
		t.Fatalf("forced sync evals = %d", len(client.recordedCalls()))
	}
}

func TestUserRequestLimitCoordinatorStopDrains(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	client := &fakeEvalClient{results: [][]any{{int64(1)}}, failIndex: -1}
	provider := &fakeClientProvider{client: client}
	coordinator, counter := newSyncCoordinator(clock, client, provider)
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	if !coordinator.StopCoordinator(nil) {
		t.Fatal("stop must drain the dirty counter through forced syncs")
	}
	if counter.Stats().DirtyEntries != 0 {
		t.Fatalf("dirty after stop = %d", counter.Stats().DirtyEntries)
	}
}

func TestUserRequestLimitCoordinatorStopReportsFailure(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	client := &fakeEvalClient{failIndex: -1, failAll: true}
	provider := &fakeClientProvider{client: client}
	coordinator, counter := newSyncCoordinator(clock, client, provider)
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	if coordinator.StopCoordinator(iptr(50)) {
		t.Fatal("stop must fail when the sync keeps erroring")
	}
}

func TestUserRequestLimitCoordinatorMemoryDriverNoop(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{
		RedisEnabled: false, Namespace: "juhe-ai",
	})
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if err := coordinator.SynchronizeDirtyCounters(true); err != nil {
		t.Fatal(err)
	}
	// Memory driver keeps local dirty state (Node: 继续使用本机内存计数).
	if counter.Stats().DirtyEntries != 1 {
		t.Fatalf("dirty entries = %d", counter.Stats().DirtyEntries)
	}
}

func TestUserRequestLimitCoordinatorStartIdempotent(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{
		RedisEnabled: false, Namespace: "juhe-ai",
	})
	coordinator.StartCoordinator()
	coordinator.StartCoordinator()
	if !coordinator.StopCoordinator(nil) {
		t.Fatal("stop after start must succeed")
	}
}
