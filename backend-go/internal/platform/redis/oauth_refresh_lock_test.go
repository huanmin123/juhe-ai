package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNewOAuthRefreshLockUsesNodeDefaultsAndValidatesOptions(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "defaults")
	lock, err := NewOAuthRefreshLock(client, OAuthRefreshLockOptions{})
	if err != nil {
		t.Fatalf("NewOAuthRefreshLock() error = %v", err)
	}
	if lock.ttl != OAuthRefreshLockTTL || lock.wait != OAuthRefreshLockWait || lock.retry != OAuthRefreshLockRetry {
		t.Fatalf("defaults = %v/%v/%v", lock.ttl, lock.wait, lock.retry)
	}
	if _, err := NewOAuthRefreshLock(nil, OAuthRefreshLockOptions{}); err == nil {
		t.Fatal("NewOAuthRefreshLock(nil) error = nil")
	}
	for _, options := range []OAuthRefreshLockOptions{
		{TTL: 74 * time.Millisecond}, {Wait: -time.Second}, {Retry: -time.Second},
	} {
		if _, err := NewOAuthRefreshLock(client, options); err == nil {
			t.Fatalf("NewOAuthRefreshLock(%+v) error = nil", options)
		}
	}
}

func TestOAuthRefreshLockAcquireUsesProviderSourceKeyAndTokenFence(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "acquire")
	lock, err := NewOAuthRefreshLock(client, OAuthRefreshLockOptions{TTL: time.Second, Wait: 50 * time.Millisecond, Retry: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewOAuthRefreshLock() error = %v", err)
	}
	lease, err := lock.Acquire(t.Context(), " openai ", " source-1 ")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	t.Cleanup(func() { _, _ = lease.Release(context.Background()) })

	key := client.Key("provider-oauth", "refresh-locks", "openai", "source-1")
	value, err := server.Get(key)
	if err != nil {
		t.Fatalf("miniredis Get(%q) error = %v", key, err)
	}
	if value == "" || len(value) != 32 {
		t.Fatalf("stored token = %q, want 16-byte hex token", value)
	}
	if lease.ProviderCode() != "openai" || lease.SourceAccountID() != "source-1" {
		t.Fatalf("lease identity = %q/%q", lease.ProviderCode(), lease.SourceAccountID())
	}
	if err := lease.AssertOwned(t.Context()); err != nil {
		t.Fatalf("AssertOwned() error = %v", err)
	}

	server.Set(key, "replacement")
	if err := lease.Renew(t.Context()); !errors.Is(err, ErrOAuthRefreshLockLost) {
		t.Fatalf("Renew() error = %v, want ErrOAuthRefreshLockLost", err)
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("lock context was not canceled after ownership loss")
	}
	if !errors.Is(context.Cause(lease.Context()), ErrOAuthRefreshLockLost) {
		t.Fatalf("lock context cause = %v", context.Cause(lease.Context()))
	}
	released, err := lease.Release(t.Context())
	if err != nil || released {
		t.Fatalf("Release() = %v, %v, want false, nil for foreign token", released, err)
	}
	if got, _ := server.Get(key); got != "replacement" {
		t.Fatalf("foreign value after Release() = %q", got)
	}
}

func TestOAuthRefreshLockWaitsForReleaseAndTimesOutBusy(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "wait")
	firstLock := mustOAuthRefreshLock(t, client, 300*time.Millisecond, 80*time.Millisecond, 5*time.Millisecond)
	secondLock := mustOAuthRefreshLock(t, client, 300*time.Millisecond, 80*time.Millisecond, 5*time.Millisecond)
	first, err := firstLock.Acquire(t.Context(), "anthropic", "source-2")
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = first.Release(context.Background())
		close(released)
	}()
	second, err := secondLock.Acquire(t.Context(), "anthropic", "source-2")
	if err != nil {
		t.Fatalf("waiting Acquire() error = %v", err)
	}
	<-released
	t.Cleanup(func() { _, _ = second.Release(context.Background()) })

	start := time.Now()
	_, err = firstLock.Acquire(t.Context(), "anthropic", "source-2")
	if !errors.Is(err, ErrOAuthRefreshLockBusy) {
		t.Fatalf("busy Acquire() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("busy Acquire() elapsed = %v, want bounded wait near 80ms", elapsed)
	}
}

func TestOAuthRefreshLockFailIfLockedDoesNotPoll(t *testing.T) {
	var calls atomic.Int32
	lock := &OAuthRefreshLock{
		ttl: 90 * time.Second, wait: 30 * time.Second, retry: 250 * time.Millisecond, failIfLocked: true,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) {
			calls.Add(1)
			return false, nil
		},
		renew:   func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		release: func(context.Context, string, string) (bool, error) { return true, nil },
		token:   func() (string, error) { return "test-token", nil },
	}
	start := time.Now()
	_, err := lock.Acquire(t.Context(), "openai", "source-skip")
	if !errors.Is(err, ErrOAuthRefreshLockBusy) || calls.Load() != 1 {
		t.Fatalf("Acquire() error/calls = %v/%d", err, calls.Load())
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("fail-fast Acquire() elapsed = %v", elapsed)
	}
}

func TestOAuthRefreshLockAcquireHonorsCallerCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "cancel")
	lock := mustOAuthRefreshLock(t, client, time.Second, time.Second, 250*time.Millisecond)
	lease, err := lock.Acquire(t.Context(), "gemini", "source-3")
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	t.Cleanup(func() { _, _ = lease.Release(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, acquireErr := lock.Acquire(ctx, "gemini", "source-3")
		done <- acquireErr
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire() did not stop on caller cancellation")
	}
}

func TestOAuthRefreshLockBackgroundRenewalAndLostContext(t *testing.T) {
	var calls atomic.Int32
	lock := &OAuthRefreshLock{
		ttl: 90 * time.Millisecond, wait: 40 * time.Millisecond, retry: 5 * time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew: func(context.Context, string, string, time.Duration) (bool, error) {
			return calls.Add(1) < 2, nil
		},
		release: func(context.Context, string, string) (bool, error) { return false, nil },
		token:   func() (string, error) { return "test-token", nil },
	}
	lease, err := lock.Acquire(t.Context(), "xai", "source-4")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background renewal did not cancel lock context after fenced loss")
	}
	if calls.Load() != 2 || !errors.Is(context.Cause(lease.Context()), ErrOAuthRefreshLockLost) {
		t.Fatalf("renew calls/cause = %d/%v", calls.Load(), context.Cause(lease.Context()))
	}
	_, _ = lease.Release(context.Background())
}

func TestOAuthRefreshLockBackgroundToleratesTransientErrorsUntilTTL(t *testing.T) {
	transient := errors.New("redis unavailable")
	var calls atomic.Int32
	lock := &OAuthRefreshLock{
		ttl: 90 * time.Millisecond, wait: 30 * time.Millisecond, retry: 5 * time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew: func(context.Context, string, string, time.Duration) (bool, error) {
			calls.Add(1)
			return false, transient
		},
		release: func(context.Context, string, string) (bool, error) { return false, nil },
		token:   func() (string, error) { return "test-token", nil },
	}
	lease, err := lock.Acquire(t.Context(), "openai", "source-5")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background renewal did not fail closed after one TTL")
	}
	if calls.Load() < 2 {
		t.Fatalf("renew calls = %d, want transient retries", calls.Load())
	}
	var lost *OAuthRefreshLockLostError
	if !errors.As(context.Cause(lease.Context()), &lost) || !errors.Is(lost.Cause, transient) {
		t.Fatalf("lock context cause = %v", context.Cause(lease.Context()))
	}
	_, _ = lease.Release(context.Background())
}

func TestOAuthRefreshLockReleaseStopsRenewalAndIsIdempotent(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "release")
	lock := mustOAuthRefreshLock(t, client, 90*time.Millisecond, 40*time.Millisecond, 5*time.Millisecond)
	lease, err := lock.Acquire(t.Context(), "openai", "source-6")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	key := client.Key("provider-oauth", "refresh-locks", "openai", "source-6")
	released, err := lease.Release(t.Context())
	if err != nil || !released {
		t.Fatalf("Release() = %v, %v", released, err)
	}
	if server.Exists(key) {
		t.Fatalf("key %q still exists after release", key)
	}
	released, err = lease.Release(t.Context())
	if err != nil || !released {
		t.Fatalf("idempotent Release() = %v, %v", released, err)
	}
	if !errors.Is(context.Cause(lease.Context()), context.Canceled) {
		t.Fatalf("released lock context cause = %v", context.Cause(lease.Context()))
	}
}

func TestOAuthRefreshLockReleaseWaitsForInFlightRenewal(t *testing.T) {
	renewStarted := make(chan struct{})
	renewExited := make(chan struct{})
	releaseCalled := make(chan struct{}, 1)
	lock := &OAuthRefreshLock{
		ttl: 75 * time.Millisecond, wait: 30 * time.Millisecond, retry: 5 * time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew: func(ctx context.Context, _ string, _ string, _ time.Duration) (bool, error) {
			close(renewStarted)
			<-ctx.Done()
			close(renewExited)
			return false, ctx.Err()
		},
		release: func(context.Context, string, string) (bool, error) {
			select {
			case <-renewExited:
			default:
				t.Fatal("release ran before in-flight renewal exited")
			}
			releaseCalled <- struct{}{}
			return true, nil
		},
		token: func() (string, error) { return "test-token", nil },
	}
	lease, err := lock.Acquire(t.Context(), "openai", "source-race")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	select {
	case <-renewStarted:
	case <-time.After(time.Second):
		t.Fatal("background renewal did not start")
	}
	released, err := lease.Release(t.Context())
	if err != nil || !released {
		t.Fatalf("Release() = %v, %v", released, err)
	}
	select {
	case <-releaseCalled:
	default:
		t.Fatal("release runner was not called")
	}
}

func TestOAuthRefreshLockReleaseWaitIsContextBoundedAndRetryable(t *testing.T) {
	renewStarted := make(chan struct{})
	unblockRenew := make(chan struct{})
	lock := &OAuthRefreshLock{
		ttl: 75 * time.Millisecond, wait: 30 * time.Millisecond, retry: 5 * time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew: func(context.Context, string, string, time.Duration) (bool, error) {
			close(renewStarted)
			<-unblockRenew
			return false, context.Canceled
		},
		release: func(context.Context, string, string) (bool, error) { return true, nil },
		token:   func() (string, error) { return "test-token", nil },
	}
	lease, err := lock.Acquire(t.Context(), "openai", "source-release-timeout")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	select {
	case <-renewStarted:
	case <-time.After(time.Second):
		t.Fatal("background renewal did not start")
	}

	releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if released, releaseErr := lease.Release(releaseCtx); released || !errors.Is(releaseErr, context.DeadlineExceeded) {
		t.Fatalf("first Release() = %v, %v", released, releaseErr)
	}
	close(unblockRenew)
	released, err := lease.Release(t.Context())
	if err != nil || !released {
		t.Fatalf("retry Release() = %v, %v", released, err)
	}
}

func TestOAuthRefreshLockWithLockReleasesAndPropagatesLostOwnership(t *testing.T) {
	server := miniredis.RunT(t)
	client := newOAuthRefreshLockTestClient(t, server, "with-lock")
	lock := mustOAuthRefreshLock(t, client, time.Second, 50*time.Millisecond, 5*time.Millisecond)
	key := client.Key("provider-oauth", "refresh-locks", "openai", "source-7")

	err := lock.WithLock(t.Context(), "openai", "source-7", func(lockCtx context.Context, assertOwned func(context.Context) error) error {
		if lockCtx.Err() != nil {
			t.Fatalf("lock context error = %v", lockCtx.Err())
		}
		return assertOwned(t.Context())
	})
	if err != nil {
		t.Fatalf("WithLock() error = %v", err)
	}
	if server.Exists(key) {
		t.Fatalf("key %q still exists after WithLock", key)
	}

	err = lock.WithLock(t.Context(), "openai", "source-7", func(_ context.Context, assertOwned func(context.Context) error) error {
		server.Set(key, "replacement")
		if assertErr := assertOwned(t.Context()); !errors.Is(assertErr, ErrOAuthRefreshLockLost) {
			t.Fatalf("assertOwned() error = %v", assertErr)
		}
		return nil
	})
	if !errors.Is(err, ErrOAuthRefreshLockLost) {
		t.Fatalf("WithLock() error = %v, want ErrOAuthRefreshLockLost", err)
	}
	if got, _ := server.Get(key); got != "replacement" {
		t.Fatalf("foreign value after WithLock = %q", got)
	}
}

func TestOAuthRefreshLockWithLockDetachesRotationFromCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var renewCalls atomic.Int32
	lock := &OAuthRefreshLock{
		ttl: time.Second, wait: time.Second, retry: time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew: func(context.Context, string, string, time.Duration) (bool, error) {
			renewCalls.Add(1)
			return true, nil
		},
		release: func(context.Context, string, string) (bool, error) { return true, nil },
		token:   func() (string, error) { return "test-token", nil },
	}
	err := lock.WithLock(ctx, "openai", "source-detached", func(lockCtx context.Context, assertOwned func(context.Context) error) error {
		cancel()
		select {
		case <-lockCtx.Done():
			t.Fatalf("caller cancellation leaked into lock context: %v", context.Cause(lockCtx))
		default:
		}
		return assertOwned(lockCtx)
	})
	if err != nil || renewCalls.Load() != 1 {
		t.Fatalf("WithLock() error/renew calls = %v/%d", err, renewCalls.Load())
	}
}

func TestOAuthRefreshLockWithLockDoesNotOverrideSuccessOnReleaseFailure(t *testing.T) {
	releaseErr := errors.New("redis release failed")
	reported := make(chan error, 1)
	lock := &OAuthRefreshLock{
		ttl: time.Second, wait: time.Second, retry: time.Millisecond,
		onReleaseError: func(err error) { reported <- err },
		acquire:        func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		renew:          func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		release:        func(context.Context, string, string) (bool, error) { return false, releaseErr },
		token:          func() (string, error) { return "test-token", nil },
	}
	if err := lock.WithLock(t.Context(), "openai", "source-release-error", func(context.Context, func(context.Context) error) error {
		return nil
	}); err != nil {
		t.Fatalf("WithLock() error = %v, want successful task result", err)
	}
	select {
	case err := <-reported:
		if !errors.Is(err, releaseErr) {
			t.Fatalf("reported release error = %v", err)
		}
	default:
		t.Fatal("release failure was not reported")
	}
	lock.onReleaseError = func(error) { panic("reporter failure") }
	if err := lock.WithLock(t.Context(), "openai", "source-release-panic", func(context.Context, func(context.Context) error) error {
		return nil
	}); err != nil {
		t.Fatalf("WithLock() reporter panic changed task result: %v", err)
	}
}

func TestOAuthRefreshLockValidatesIdentityBeforeRedis(t *testing.T) {
	var calls atomic.Int32
	lock := &OAuthRefreshLock{
		ttl: time.Second, wait: time.Second, retry: time.Millisecond,
		acquire: func(context.Context, string, string, time.Duration) (bool, error) { calls.Add(1); return true, nil },
		renew:   func(context.Context, string, string, time.Duration) (bool, error) { return true, nil },
		release: func(context.Context, string, string) (bool, error) { return true, nil },
		token:   func() (string, error) { return "token", nil },
	}
	for _, identity := range [][2]string{{"", "source"}, {"openai", " "}, {"open:ai", "source"}, {"openai", "source:other"}} {
		if _, err := lock.Acquire(t.Context(), identity[0], identity[1]); err == nil {
			t.Fatalf("Acquire(%q, %q) error = nil", identity[0], identity[1])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lock.Acquire(ctx, "openai", "source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire(canceled) error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid Acquire() reached Redis %d times", calls.Load())
	}
}

func TestOAuthRefreshLockLeaseFormattingDoesNotExposeOwnershipToken(t *testing.T) {
	lease := &OAuthRefreshLockLease{providerCode: "openai", sourceAccountID: "source", token: "ownership-secret"}
	for name, formatted := range map[string]string{"String": fmt.Sprintf("%v", lease), "GoString": fmt.Sprintf("%#v", lease)} {
		if strings.Contains(formatted, "ownership-secret") || formatted != "[OAuth refresh lock lease]" {
			t.Fatalf("%s formatting = %q", name, formatted)
		}
	}
}

func TestOAuthRefreshLockLuaScriptsAreTokenFenced(t *testing.T) {
	for name, script := range map[string]string{
		"acquire": oauthRefreshLockAcquireLua,
		"renew":   oauthRefreshLockRenewLua,
		"release": oauthRefreshLockReleaseLua,
	} {
		if !strings.Contains(script, "ARGV[1]") {
			t.Fatalf("%s Lua is not token fenced", name)
		}
	}
	if !strings.Contains(oauthRefreshLockAcquireLua, "'NX', 'PX'") ||
		!strings.Contains(oauthRefreshLockRenewLua, "redis.call('GET', KEYS[1]) ~= ARGV[1]") ||
		!strings.Contains(oauthRefreshLockReleaseLua, "redis.call('GET', KEYS[1]) ~= ARGV[1]") {
		t.Fatal("OAuth refresh lock Lua fencing contract changed")
	}
}

func newOAuthRefreshLockTestClient(t *testing.T, server *miniredis.Miniredis, namespace string) *Client {
	t.Helper()
	client, err := NewClient("redis://"+server.Addr()+"/0", "oauth-refresh-lock:"+namespace)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func mustOAuthRefreshLock(t *testing.T, client *Client, ttl, wait, retry time.Duration) *OAuthRefreshLock {
	t.Helper()
	lock, err := NewOAuthRefreshLock(client, OAuthRefreshLockOptions{TTL: ttl, Wait: wait, Retry: retry})
	if err != nil {
		t.Fatalf("NewOAuthRefreshLock() error = %v", err)
	}
	return lock
}
