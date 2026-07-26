package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestRouteCounterUsesStrategySharedNamespacedKey(t *testing.T) {
	server := miniredis.RunT(t)
	counter, closeCounter := newMiniredisRouteCounter(t, server, "prod-west")
	defer closeCounter()

	for call, want := range []int{0, 1, 2, 0} {
		got, err := counter.NextIndex(t.Context(), "route/shared", RouteCounterModeRoundRobin, 3)
		if err != nil {
			t.Fatalf("NextIndex() call %d error = %v", call, err)
		}
		if got != want {
			t.Fatalf("NextIndex() call %d = %d, want %d", call, got, want)
		}
	}

	key := "juhe-ai:prod-west:route-state:api-key-group:round-robin:cm91dGUvc2hhcmVk"
	if got, err := server.Get(key); err != nil || got != "4" {
		t.Fatalf("redis counter = %q, %v, want 4", got, err)
	}
	ttl := server.TTL(key)
	if ttl <= 0 || ttl > RouteCounterMaxTTL {
		t.Fatalf("redis ttl = %s, want (0, %s]", ttl, RouteCounterMaxTTL)
	}
}

func TestRouteCounterSharesStateAcrossClientsAndSeparatesModes(t *testing.T) {
	server := miniredis.RunT(t)
	first, closeFirst := newMiniredisRouteCounter(t, server, "shared")
	defer closeFirst()
	second, closeSecond := newMiniredisRouteCounter(t, server, "shared")
	defer closeSecond()

	got, err := first.NextIndex(t.Context(), "strategy-1", RouteCounterModeRoundRobin, 10)
	if err != nil || got != 0 {
		t.Fatalf("first round-robin index = %d, %v, want 0", got, err)
	}
	got, err = second.NextIndex(t.Context(), "strategy-1", RouteCounterModeRoundRobin, 10)
	if err != nil || got != 1 {
		t.Fatalf("second round-robin index = %d, %v, want 1", got, err)
	}
	got, err = second.NextIndex(t.Context(), "strategy-1", RouteCounterModeWeighted, 10)
	if err != nil || got != 0 {
		t.Fatalf("weighted index = %d, %v, want independent 0", got, err)
	}
}

func TestRouteCounterSeparatesDeploymentNamespaces(t *testing.T) {
	server := miniredis.RunT(t)
	production, closeProduction := newMiniredisRouteCounter(t, server, "production")
	defer closeProduction()
	staging, closeStaging := newMiniredisRouteCounter(t, server, "staging")
	defer closeStaging()

	for name, counter := range map[string]*RouteCounter{
		"production": production,
		"staging":    staging,
	} {
		got, err := counter.NextIndex(t.Context(), "strategy-1", RouteCounterModeRoundRobin, 2)
		if err != nil || got != 0 {
			t.Fatalf("%s first index = %d, %v, want isolated 0", name, got, err)
		}
	}
}

func TestRouteCounterRefreshesBoundedTTL(t *testing.T) {
	server := miniredis.RunT(t)
	counter, closeCounter := newMiniredisRouteCounter(t, server, "ttl")
	defer closeCounter()

	if _, err := counter.NextIndex(t.Context(), "strategy-1", RouteCounterModeRoundRobin, 2); err != nil {
		t.Fatalf("first NextIndex() error = %v", err)
	}
	server.FastForward(RouteCounterMaxTTL - time.Hour)
	if _, err := counter.NextIndex(t.Context(), "strategy-1", RouteCounterModeRoundRobin, 2); err != nil {
		t.Fatalf("refresh NextIndex() error = %v", err)
	}

	key, err := routeCounterKey("ttl", "strategy-1", RouteCounterModeRoundRobin)
	if err != nil {
		t.Fatalf("routeCounterKey() error = %v", err)
	}
	ttl := server.TTL(key)
	if ttl < RouteCounterMaxTTL-time.Second || ttl > RouteCounterMaxTTL {
		t.Fatalf("refreshed ttl = %s, want approximately %s", ttl, RouteCounterMaxTTL)
	}
}

func TestRouteCounterConcurrentModuloContract(t *testing.T) {
	server := miniredis.RunT(t)
	counter, closeCounter := newMiniredisRouteCounter(t, server, "race")
	defer closeCounter()

	const calls = 101
	const modulo = 7
	results := make(chan int, calls)
	errorsCh := make(chan error, calls)
	var wait sync.WaitGroup
	wait.Add(calls)
	for range calls {
		go func() {
			defer wait.Done()
			index, err := counter.NextIndex(t.Context(), "strategy-race", RouteCounterModeWeighted, modulo)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- index
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent NextIndex() error = %v", err)
	}
	counts := make([]int, modulo)
	for index := range results {
		if index < 0 || index >= modulo {
			t.Fatalf("index = %d, want [0, %d)", index, modulo)
		}
		counts[index]++
	}
	for index, count := range counts {
		if count < calls/modulo || count > calls/modulo+1 {
			t.Fatalf("count[%d] = %d, want balanced modulo distribution; all=%v", index, count, counts)
		}
	}
}

func TestRouteCounterValidatesInputsBeforeRedis(t *testing.T) {
	calls := 0
	counter := &RouteCounter{
		namespace: "valid",
		run: func(context.Context, string, int64, int64) (int64, error) {
			calls++
			return 0, nil
		},
	}
	tests := []struct {
		name       string
		ctx        context.Context
		strategyID string
		mode       RouteCounterMode
		modulo     int
	}{
		{name: "nil context", strategyID: "strategy", mode: RouteCounterModeRoundRobin, modulo: 1},
		{name: "blank strategy", ctx: t.Context(), strategyID: " ", mode: RouteCounterModeRoundRobin, modulo: 1},
		{name: "unknown mode", ctx: t.Context(), strategyID: "strategy", mode: "random", modulo: 1},
		{name: "zero modulo", ctx: t.Context(), strategyID: "strategy", mode: RouteCounterModeRoundRobin},
		{name: "negative modulo", ctx: t.Context(), strategyID: "strategy", mode: RouteCounterModeRoundRobin, modulo: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := counter.NextIndex(test.ctx, test.strategyID, test.mode, test.modulo); err == nil {
				t.Fatal("NextIndex() error = nil, want validation error")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("redis calls = %d, want 0", calls)
	}
}

func TestRouteCounterPropagatesContextAndRejectsMalformedResult(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	counter := &RouteCounter{
		namespace: "valid",
		run: func(ctx context.Context, _ string, _, _ int64) (int64, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return 0, wantErr
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := counter.NextIndex(ctx, "strategy", RouteCounterModeRoundRobin, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled NextIndex() error = %v, want context canceled", err)
	}
	if _, err := counter.NextIndex(t.Context(), "strategy", RouteCounterModeRoundRobin, 2); !errors.Is(err, wantErr) {
		t.Fatalf("failed NextIndex() error = %v, want redis error", err)
	}

	counter.run = func(context.Context, string, int64, int64) (int64, error) { return 2, nil }
	if _, err := counter.NextIndex(t.Context(), "strategy", RouteCounterModeRoundRobin, 2); err == nil {
		t.Fatal("out-of-range NextIndex() error = nil")
	}
}

func TestRouteCounterConstructorAndLuaContract(t *testing.T) {
	if _, err := NewRouteCounter(nil, "namespace"); err == nil {
		t.Fatal("NewRouteCounter(nil) error = nil")
	}
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr()+"/0", "test:state")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	for _, namespace := range []string{"", " ", strings.Repeat("x", 65)} {
		if _, err := NewRouteCounter(client, namespace); err == nil {
			t.Fatalf("NewRouteCounter(namespace=%q) error = nil", namespace)
		}
	}
	for _, fragment := range []string{
		"redis.call('INCR', KEYS[1])",
		"redis.call('PEXPIRE', KEYS[1], ttl_ms)",
		"(value - 1) % modulo",
	} {
		if !strings.Contains(routeCounterLua, fragment) {
			t.Fatalf("route counter Lua missing %q", fragment)
		}
	}
}

func TestClientCompareAndSwapUsesExactBytesAndCanDelete(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := NewClient("redis://"+server.Addr()+"/0", "cas:test")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if swapped, err := client.CompareAndSwap(t.Context(), "state", nil, []byte("one"), time.Hour); err != nil || !swapped {
		t.Fatalf("create CompareAndSwap() = %v, %v", swapped, err)
	}
	if swapped, err := client.CompareAndSwap(t.Context(), "state", []byte("stale"), []byte("two"), time.Hour); err != nil || swapped {
		t.Fatalf("stale CompareAndSwap() = %v, %v", swapped, err)
	}
	if swapped, err := client.CompareAndSwap(t.Context(), "state", []byte("one"), nil, time.Hour); err != nil || !swapped {
		t.Fatalf("delete CompareAndSwap() = %v, %v", swapped, err)
	}
	if _, err := server.Get(client.Key("state")); err == nil {
		t.Fatal("CompareAndSwap() did not delete state")
	}
}

func newMiniredisRouteCounter(t *testing.T, server *miniredis.Miniredis, namespace string) (*RouteCounter, func()) {
	t.Helper()
	client, err := NewClient("redis://"+server.Addr()+"/0", fmt.Sprintf("%s:state", namespace))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	counter, err := NewRouteCounter(client, namespace)
	if err != nil {
		_ = client.Close()
		t.Fatalf("NewRouteCounter() error = %v", err)
	}
	return counter, func() { _ = client.Close() }
}
