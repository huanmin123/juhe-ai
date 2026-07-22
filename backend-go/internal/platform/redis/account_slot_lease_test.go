package redis

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewAccountSlotLeaseStoreValidatesDependencies(t *testing.T) {
	if _, err := NewAccountSlotLeaseStore(nil, "shared"); err == nil {
		t.Fatal("NewAccountSlotLeaseStore() error = nil, want client error")
	}
	client := &Client{client: nil}
	if _, err := NewAccountSlotLeaseStore(client, "shared"); err == nil {
		t.Fatal("NewAccountSlotLeaseStore() error = nil, want uninitialized client error")
	}
}

func TestAccountSlotLeaseStoreAcquireUsesV2KeysAndReturnsFencedLease(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 11, 12, 345000000, time.UTC)
	var gotKeys []string
	var gotArgs []interface{}
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared-prod",
		acquire: func(_ context.Context, keys []string, args ...interface{}) ([]interface{}, error) {
			gotKeys = append([]string(nil), keys...)
			gotArgs = append([]interface{}(nil), args...)
			return []interface{}{int64(1), int64(2), int64(1), now.Add(90 * time.Second).UnixMilli()}, nil
		},
	}

	result, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID:  "acct_1",
		Lane:       AccountSlotLaneImage,
		TotalLimit: 3,
		LaneLimit:  2,
		TTL:        90 * time.Second,
		Token:      "go-owner|lease-1",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if !result.Acquired || result.Current != 2 || result.LaneCurrent != 1 {
		t.Fatalf("Acquire() result = %+v", result)
	}
	wantLease := AccountSlotLease{
		AccountID: "acct_1",
		Lane:      AccountSlotLaneImage,
		Token:     "go-owner|lease-1",
		ExpiresAt: now.Add(90 * time.Second),
	}
	if result.Lease != wantLease {
		t.Fatalf("Acquire() lease = %+v, want %+v", result.Lease, wantLease)
	}
	wantKeys := []string{
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:total",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:text",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:image",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:image",
		"juhe-ai:shared-prod:account-concurrency-v2:acct_1:metadata",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("Acquire() keys = %#v, want %#v", gotKeys, wantKeys)
	}
	wantArgs := []interface{}{"3", "2", "90000", "go-owner|lease-1"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Acquire() args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestAccountSlotLeaseStoreAcquireCapacityDenialDoesNotExposeToken(t *testing.T) {
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		acquire: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			return []interface{}{int64(0), int64(5), int64(2), int64(0)}, nil
		},
	}

	result, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 5, LaneLimit: 5, TTL: time.Second, Token: "must-not-escape",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Acquired || result.Current != 5 || result.LaneCurrent != 2 || result.Lease.Token != "" {
		t.Fatalf("Acquire() result = %+v, want capacity denial without lease", result)
	}
}

func TestAccountSlotLeaseStoreAcquireRejectsTokenCollision(t *testing.T) {
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		acquire: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			return []interface{}{int64(2), int64(1), int64(1), int64(2_000)}, nil
		},
	}

	_, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 2, LaneLimit: 2, TTL: time.Second, Token: "duplicate",
	})
	if !errors.Is(err, ErrAccountSlotTokenCollision) {
		t.Fatalf("Acquire() error = %v, want ErrAccountSlotTokenCollision", err)
	}
}

func TestAccountSlotLeaseStoreRefreshUsesExactTokenAndReturnsNewExpiry(t *testing.T) {
	now := time.UnixMilli(10_000)
	lease := AccountSlotLease{
		AccountID: "acct_1", Lane: AccountSlotLaneText, Token: "owner|lease-9", ExpiresAt: time.UnixMilli(9_000),
	}
	var gotKeys []string
	var gotArgs []interface{}
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		refresh: func(_ context.Context, keys []string, args ...interface{}) ([]interface{}, error) {
			gotKeys = append([]string(nil), keys...)
			gotArgs = append([]interface{}(nil), args...)
			return []interface{}{int64(1), now.Add(30 * time.Second).UnixMilli()}, nil
		},
	}

	result, err := store.Refresh(t.Context(), lease, 30*time.Second)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !result.Refreshed || result.Lease.Token != lease.Token || !result.Lease.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("Refresh() result = %+v", result)
	}
	wantKeys := []string{
		"juhe-ai:shared:account-concurrency-v2:acct_1:total",
		"juhe-ai:shared:account-concurrency-v2:acct_1:text",
		"juhe-ai:shared:account-concurrency-v2:acct_1:image",
		"juhe-ai:shared:account-concurrency-v2:acct_1:text",
		"juhe-ai:shared:account-concurrency-v2:acct_1:metadata",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("Refresh() keys = %#v, want %#v", gotKeys, wantKeys)
	}
	wantArgs := []interface{}{"owner|lease-9", "30000"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Refresh() args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestAccountSlotLeaseStoreRefreshDoesNotReviveExpiredOrForeignToken(t *testing.T) {
	lease := AccountSlotLease{AccountID: "acct_1", Lane: AccountSlotLaneImage, Token: "stale"}
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		refresh: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			return []interface{}{int64(0), int64(0)}, nil
		},
	}

	result, err := store.Refresh(t.Context(), lease, time.Minute)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if result.Refreshed || result.Lease.Token != "" {
		t.Fatalf("Refresh() result = %+v, want fenced no-op", result)
	}
}

func TestAccountSlotLeaseStoreReleaseIsTokenFencedAndIdempotent(t *testing.T) {
	lease := AccountSlotLease{AccountID: "acct_1", Lane: AccountSlotLaneText, Token: "owner|lease-7"}
	var gotKeys []string
	var gotArgs []interface{}
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		release: func(_ context.Context, keys []string, args ...interface{}) (int64, error) {
			gotKeys = append([]string(nil), keys...)
			gotArgs = append([]interface{}(nil), args...)
			return 1, nil
		},
	}

	released, err := store.Release(t.Context(), lease)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if !released {
		t.Fatal("Release() = false, want true")
	}
	wantKeys := []string{
		"juhe-ai:shared:account-concurrency-v2:acct_1:total",
		"juhe-ai:shared:account-concurrency-v2:acct_1:text",
		"juhe-ai:shared:account-concurrency-v2:acct_1:image",
		"juhe-ai:shared:account-concurrency-v2:acct_1:metadata",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) || !reflect.DeepEqual(gotArgs, []interface{}{"owner|lease-7"}) {
		t.Fatalf("Release() keys/args = %#v / %#v", gotKeys, gotArgs)
	}

	store.release = func(context.Context, []string, ...interface{}) (int64, error) { return 0, nil }
	released, err = store.Release(t.Context(), lease)
	if err != nil || released {
		t.Fatalf("stale Release() = %v, %v, want false, nil", released, err)
	}
}

func TestAccountSlotLeaseStoreValidatesInputBeforeRedis(t *testing.T) {
	called := false
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		acquire: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			called = true
			return nil, nil
		},
		refresh: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			called = true
			return nil, nil
		},
		release: func(context.Context, []string, ...interface{}) (int64, error) {
			called = true
			return 0, nil
		},
	}

	badAcquire := []AccountSlotAcquireInput{
		{Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: time.Second, Token: "token"},
		{AccountID: "acct:1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: time.Second, Token: "token"},
		{AccountID: "acct_1", Lane: "video", TotalLimit: 1, LaneLimit: 1, TTL: time.Second, Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 0, LaneLimit: 1, TTL: time.Second, Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 2, TTL: time.Second, Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: time.Nanosecond, Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: AccountSlotLeaseTTL + time.Millisecond, Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: time.Second},
	}
	for _, input := range badAcquire {
		if _, err := store.Acquire(t.Context(), input); err == nil {
			t.Fatalf("Acquire(%+v) error = nil", input)
		}
	}
	badLeases := []AccountSlotLease{
		{Lane: AccountSlotLaneText, Token: "token"},
		{AccountID: "acct_1", Lane: "video", Token: "token"},
		{AccountID: "acct_1", Lane: AccountSlotLaneText},
		{AccountID: "acct_1", Lane: AccountSlotLaneText, Token: strings.Repeat("x", 257)},
	}
	for _, lease := range badLeases {
		if _, err := store.Refresh(t.Context(), lease, time.Second); err == nil {
			t.Fatalf("Refresh(%+v) error = nil", lease)
		}
		if _, err := store.Release(t.Context(), lease); err == nil {
			t.Fatalf("Release(%+v) error = nil", lease)
		}
	}
	if _, err := store.Refresh(t.Context(), AccountSlotLease{AccountID: "acct_1", Lane: AccountSlotLaneText, Token: "token"}, 0); err == nil {
		t.Fatal("Refresh() zero TTL error = nil")
	}
	if _, err := store.Refresh(t.Context(), AccountSlotLease{AccountID: "acct_1", Lane: AccountSlotLaneText, Token: "token"}, AccountSlotLeaseTTL+time.Millisecond); err == nil {
		t.Fatal("Refresh() TTL above Node coexistence limit error = nil")
	}
	if called {
		t.Fatal("invalid input reached Redis runner")
	}
}

func TestAccountSlotLeaseStoreHonorsCanceledContextWithoutRedisCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	store := &AccountSlotLeaseStore{
		rootNamespace: "shared",
		acquire: func(context.Context, []string, ...interface{}) ([]interface{}, error) {
			called = true
			return nil, nil
		},
	}
	_, err := store.Acquire(ctx, AccountSlotAcquireInput{
		AccountID: "acct_1", Lane: AccountSlotLaneText, TotalLimit: 1, LaneLimit: 1, TTL: time.Second, Token: "token",
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Acquire() error/called = %v/%v, want context.Canceled/false", err, called)
	}
}

func TestParseAccountSlotAcquireResultRejectsMalformedReply(t *testing.T) {
	for _, values := range [][]interface{}{
		nil,
		{int64(1)},
		{"bad", int64(1), int64(1), int64(1)},
		{int64(1), "bad", int64(1), int64(1)},
		{int64(1), int64(-1), int64(1), int64(1)},
		{int64(9), int64(1), int64(1), int64(1)},
	} {
		if _, err := parseAccountSlotAcquireResult(values); err == nil {
			t.Fatalf("parseAccountSlotAcquireResult(%#v) error = nil", values)
		}
	}
}

func TestParseAccountSlotRefreshResultRejectsMalformedReply(t *testing.T) {
	for _, values := range [][]interface{}{
		nil,
		{int64(1)},
		{"bad", int64(1)},
		{int64(1), "bad"},
		{int64(2), int64(1)},
		{int64(1), int64(0)},
		{int64(0), int64(-1)},
	} {
		if _, _, err := parseAccountSlotRefreshResult(values); err == nil {
			t.Fatalf("parseAccountSlotRefreshResult(%#v) error = nil", values)
		}
	}
}

func TestNewAccountSlotTokenReturnsUniqueBoundedTokens(t *testing.T) {
	first := NewAccountSlotToken()
	second := NewAccountSlotToken()
	if first == second || !strings.HasPrefix(first, "go|") || len(first) > 256 {
		t.Fatalf("NewAccountSlotToken() = %q, %q", first, second)
	}
}

func TestAccountSlotLuaScriptsEnforceFencingAndLongestLeaseTTL(t *testing.T) {
	for name, script := range map[string]string{
		"acquire": accountSlotAcquireLua,
		"refresh": accountSlotRefreshLua,
		"release": accountSlotReleaseLua,
	} {
		if !strings.Contains(script, "slot_token") {
			t.Fatalf("%s Lua is not token fenced", name)
		}
	}
	for _, want := range []string{
		"redis.call('TIME')",
		"ZSCORE', KEYS[1], slot_token",
		"return {1, current, lane_current, tonumber(total_token_expiry)}",
		"return {2, current, lane_current, 0}",
		"latest_expiry_ms",
		"PEXPIRE",
	} {
		if !strings.Contains(accountSlotAcquireLua, want) {
			t.Fatalf("acquire Lua missing %q", want)
		}
	}
	for _, want := range []string{
		"redis.call('TIME')",
		"total_expiry_ms <= now_ms",
		"selected_lane_expiry_raw == false",
		"selected_lane_expiry_ms <= now_ms",
		"other_lane_expiry_ms ~= false",
		"math.max(requested_expires_at_ms, total_expiry_ms, selected_lane_expiry_ms)",
		"latest_expiry_ms",
	} {
		if !strings.Contains(accountSlotRefreshLua, want) {
			t.Fatalf("refresh Lua missing %q", want)
		}
	}
	if !strings.Contains(accountSlotReleaseLua, "local removed = redis.call('ZREM', KEYS[1], slot_token)") {
		t.Fatal("release Lua must report whether the exact total-set token was removed")
	}
}

func TestAccountSlotLeaseStoreRedisIntegration(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if rawURL == "" {
		if os.Getenv("JUHE_AI_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("JUHE_AI_TEST_REDIS_URL is required when JUHE_AI_REQUIRE_INTEGRATION=1")
		}
		t.Skip("JUHE_AI_TEST_REDIS_URL is not configured")
	}

	client, err := NewClient(rawURL, "account-slot-integration")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	rootNamespace := "slot-test-" + strings.ReplaceAll(NewAccountSlotToken(), "|", "-")
	store, err := NewAccountSlotLeaseStore(client, rootNamespace)
	if err != nil {
		t.Fatalf("NewAccountSlotLeaseStore() error = %v", err)
	}
	accountID := "acct-integration"
	cleanupKeys := store.accountSlotKeys(accountID, AccountSlotLaneText, false)
	t.Cleanup(func() { _ = client.client.Del(context.Background(), cleanupKeys...).Err() })

	first, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: accountID, Lane: AccountSlotLaneText, TotalLimit: 2, LaneLimit: 1,
		TTL: 5 * time.Second, Token: NewAccountSlotToken(),
	})
	if err != nil || !first.Acquired {
		t.Fatalf("first Acquire() = %+v, %v", first, err)
	}
	notShortened, err := store.Refresh(t.Context(), first.Lease, time.Second)
	if err != nil || !notShortened.Refreshed || notShortened.Lease.ExpiresAt.Before(first.Lease.ExpiresAt) {
		t.Fatalf("short Refresh() = %+v, %v; must not shorten %+v", notShortened, err, first.Lease)
	}
	first.Lease = notShortened.Lease
	retried, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: accountID, Lane: AccountSlotLaneText, TotalLimit: 2, LaneLimit: 1,
		TTL: 5 * time.Second, Token: first.Lease.Token,
	})
	if err != nil || !retried.Acquired || !retried.Lease.ExpiresAt.Equal(first.Lease.ExpiresAt) || retried.Current != 1 {
		t.Fatalf("idempotent Acquire() = %+v, %v; first = %+v", retried, err, first)
	}
	denied, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: accountID, Lane: AccountSlotLaneText, TotalLimit: 2, LaneLimit: 1,
		TTL: time.Second, Token: NewAccountSlotToken(),
	})
	if err != nil || denied.Acquired || denied.LaneCurrent != 1 {
		t.Fatalf("lane-limited Acquire() = %+v, %v", denied, err)
	}
	second, err := store.Acquire(t.Context(), AccountSlotAcquireInput{
		AccountID: accountID, Lane: AccountSlotLaneImage, TotalLimit: 2, LaneLimit: 1,
		TTL: time.Second, Token: NewAccountSlotToken(),
	})
	if err != nil || !second.Acquired || second.Current != 2 {
		t.Fatalf("image Acquire() = %+v, %v", second, err)
	}

	totalTTL, err := client.client.PTTL(t.Context(), cleanupKeys[0]).Result()
	if err != nil || totalTTL < 3*time.Second {
		t.Fatalf("total key TTL = %v, %v; shorter lease must not truncate the first lease", totalTTL, err)
	}
	wrongLane := second.Lease
	wrongLane.Lane = AccountSlotLaneText
	wrongRefresh, err := store.Refresh(t.Context(), wrongLane, 4*time.Second)
	if err != nil || wrongRefresh.Refreshed {
		t.Fatalf("wrong-lane Refresh() = %+v, %v", wrongRefresh, err)
	}
	refreshed, err := store.Refresh(t.Context(), second.Lease, 4*time.Second)
	if err != nil || !refreshed.Refreshed || !refreshed.Lease.ExpiresAt.After(second.Lease.ExpiresAt) {
		t.Fatalf("Refresh() = %+v, %v", refreshed, err)
	}

	reader, err := NewAccountConcurrencyReader(client, rootNamespace)
	if err != nil {
		t.Fatalf("NewAccountConcurrencyReader() error = %v", err)
	}
	counts, err := reader.LoadAccountCurrentConcurrencyByIDs(t.Context(), []string{accountID}, time.Now().Add(24*time.Hour))
	if err != nil || counts[accountID] != 2 {
		t.Fatalf("clock-skewed reader count = %v, %v; want 2 using Redis TIME", counts, err)
	}

	stale := first.Lease
	stale.Token = NewAccountSlotToken()
	if released, err := store.Release(t.Context(), stale); err != nil || released {
		t.Fatalf("foreign Release() = %v, %v", released, err)
	}
	wrongLaneRelease := first.Lease
	wrongLaneRelease.Lane = AccountSlotLaneImage
	if released, err := store.Release(t.Context(), wrongLaneRelease); err != nil || !released {
		t.Fatalf("token-capability Release() = %v, %v", released, err)
	}
	if released, err := store.Release(t.Context(), first.Lease); err != nil || released {
		t.Fatalf("idempotent Release() = %v, %v", released, err)
	}
	if released, err := store.Release(t.Context(), refreshed.Lease); err != nil || !released {
		t.Fatalf("second Release() = %v, %v", released, err)
	}

	concurrentAccountID := accountID + "-concurrent"
	concurrentCleanupKeys := store.accountSlotKeys(concurrentAccountID, AccountSlotLaneText, false)
	t.Cleanup(func() { _ = client.client.Del(context.Background(), concurrentCleanupKeys...).Err() })
	type acquireOutcome struct {
		result AccountSlotAcquireResult
		err    error
	}
	outcomes := make(chan acquireOutcome, 20)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, acquireErr := store.Acquire(t.Context(), AccountSlotAcquireInput{
				AccountID: concurrentAccountID, Lane: AccountSlotLaneText, TotalLimit: 5, LaneLimit: 5,
				TTL: time.Minute, Token: NewAccountSlotToken(),
			})
			outcomes <- acquireOutcome{result: result, err: acquireErr}
		}()
	}
	wait.Wait()
	close(outcomes)
	acquiredLeases := make([]AccountSlotLease, 0, 5)
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent Acquire() error = %v", outcome.err)
		}
		if outcome.result.Acquired {
			acquiredLeases = append(acquiredLeases, outcome.result.Lease)
		}
	}
	if len(acquiredLeases) != 5 {
		t.Fatalf("concurrent acquired leases = %d, want 5", len(acquiredLeases))
	}
	for _, lease := range acquiredLeases {
		if released, releaseErr := store.Release(t.Context(), lease); releaseErr != nil || !released {
			t.Fatalf("concurrent lease Release() = %v, %v", released, releaseErr)
		}
	}
}
