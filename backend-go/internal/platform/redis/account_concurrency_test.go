package redis

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccountConcurrencyReaderUsesNodeKeysAndDeduplicates(t *testing.T) {
	var calls int
	var gotKeys []string
	var gotNow int64
	reader := &AccountConcurrencyReader{
		rootNamespace: "shared-prod",
		run: func(_ context.Context, keys []string, nowMillis int64) (int64, error) {
			calls++
			gotKeys = append([]string(nil), keys...)
			gotNow = nowMillis
			return 3, nil
		},
	}
	now := time.Date(2026, 7, 11, 8, 30, 0, 123000000, time.UTC)

	result, err := reader.LoadAccountCurrentConcurrencyByIDs(
		context.Background(),
		[]string{" acct_1 ", "", "acct_1"},
		now,
	)
	if err != nil {
		t.Fatalf("LoadAccountCurrentConcurrencyByIDs() error = %v", err)
	}
	if calls != 1 || result["acct_1"] != 3 || gotNow != now.UnixMilli() {
		t.Fatalf("calls=%d result=%v now=%d", calls, result, gotNow)
	}
	wantKeys := []string{
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:total",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:text",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:image",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:total",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:metadata",
	}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", gotKeys, wantKeys)
	}
}

func TestAccountConcurrencyReaderProcessesAtMostOneHundredConcurrentCalls(t *testing.T) {
	var mu sync.Mutex
	current := 0
	maxCurrent := 0
	started := make(chan struct{}, 101)
	release := make(chan struct{})
	reader := &AccountConcurrencyReader{
		rootNamespace: "shared",
		run: func(_ context.Context, _ []string, _ int64) (int64, error) {
			mu.Lock()
			current++
			maxCurrent = max(maxCurrent, current)
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			current--
			mu.Unlock()
			return 1, nil
		},
	}
	ids := make([]string, 101)
	for index := range ids {
		ids[index] = fmt.Sprintf("acct_%03d", index)
	}
	done := make(chan error, 1)
	go func() {
		_, err := reader.LoadAccountCurrentConcurrencyByIDs(context.Background(), ids, time.UnixMilli(1000))
		done <- err
	}()

	for range 100 {
		<-started
	}
	select {
	case <-started:
		t.Fatal("reader started more than 100 calls in the first batch")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("LoadAccountCurrentConcurrencyByIDs() error = %v", err)
	}
	if maxCurrent != 100 {
		t.Fatalf("max concurrent calls = %d, want 100", maxCurrent)
	}
}

func TestAccountConcurrencyReaderReturnsNoPartialResultOnError(t *testing.T) {
	reader := &AccountConcurrencyReader{
		rootNamespace: "shared",
		run: func(_ context.Context, keys []string, _ int64) (int64, error) {
			if strings.Contains(keys[0], "acct_bad") {
				return 0, errors.New("redis unavailable")
			}
			return 2, nil
		},
	}

	result, err := reader.LoadAccountCurrentConcurrencyByIDs(
		context.Background(),
		[]string{"acct_ok", "acct_bad"},
		time.UnixMilli(1000),
	)
	if err == nil || !strings.Contains(err.Error(), "acct_bad") || result != nil {
		t.Fatalf("result=%v error=%v, want nil result and account error", result, err)
	}
}

func TestAccountConcurrencyReaderEmptyInputDoesNotCallRedis(t *testing.T) {
	reader := &AccountConcurrencyReader{
		rootNamespace: "shared",
		run: func(context.Context, []string, int64) (int64, error) {
			t.Fatal("redis call must not run for empty input")
			return 0, nil
		},
	}
	result, err := reader.LoadAccountCurrentConcurrencyByIDs(
		context.Background(),
		[]string{"", "  "},
		time.Time{},
	)
	if err != nil || len(result) != 0 {
		t.Fatalf("result=%v error=%v", result, err)
	}
}

func TestAccountConcurrencyReaderRejectsNegativeRedisCount(t *testing.T) {
	reader := &AccountConcurrencyReader{
		rootNamespace: "shared",
		run: func(context.Context, []string, int64) (int64, error) {
			return -1, nil
		},
	}
	if _, err := reader.LoadAccountCurrentConcurrencyByIDs(
		context.Background(),
		[]string{"acct_1"},
		time.Now(),
	); err == nil {
		t.Fatal("negative count error = nil")
	}
}

func TestNormalizeAccountConcurrencyNamespaceMatchesNodeSanitization(t *testing.T) {
	got, err := normalizeAccountConcurrencyNamespace(" prod west/1 ")
	if err != nil {
		t.Fatalf("normalizeAccountConcurrencyNamespace() error = %v", err)
	}
	if got != "prod_west_1" {
		t.Fatalf("namespace = %q, want prod_west_1", got)
	}
	for _, value := range []string{"", " / ", strings.Repeat("x", 65)} {
		if _, err := normalizeAccountConcurrencyNamespace(value); err == nil {
			t.Fatalf("namespace %q error = nil", value)
		}
	}
}

func TestLoadAccountConcurrencyScriptMatchesNodeCleanupBoundary(t *testing.T) {
	for _, want := range []string{
		"redis.call('TIME')",
		"ZRANGEBYSCORE', KEYS[1], '-inf', now_ms",
		"math.min(index + 199, #expired)",
		"ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms",
		"ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms",
		"ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms",
		"return redis.call('ZCARD', KEYS[4])",
	} {
		if !strings.Contains(loadAccountConcurrencyLua, want) {
			t.Fatalf("account concurrency script missing %q", want)
		}
	}
	if strings.Contains(loadAccountConcurrencyLua, "ARGV[1]") {
		t.Fatal("account concurrency reader must use Redis TIME instead of a process-local clock")
	}
}
