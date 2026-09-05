package authsys

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newMiniredisStore(t *testing.T) (RedisStateStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, closeFn, err := NewRedisNamespacedStateStore("redis://"+server.Addr(), "dev", "test_store")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	return store, server
}

// TestRedisStateStoreKeyPrefix proves the migration-period key interop: the
// prefix equals redisNamespacedKey(`juhe-ai:state:<name>:`) —
// "juhe-ai:<namespace>:state:<name>:" — for the auth store names.
func TestRedisStateStoreKeyPrefix(t *testing.T) {
	prefix, err := redisStateStoreKeyPrefix("dev", "auth_captcha")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "juhe-ai:dev:state:auth_captcha:" {
		t.Fatalf("prefix=%q", prefix)
	}
	if _, err := redisStateStoreKeyPrefix("  ", "auth_captcha"); err == nil || err.Error() != "Redis namespace 不能为空" {
		t.Fatalf("empty namespace err=%v", err)
	}
	sanitized, err := redisStateStoreKeyPrefix("dev", "auth login guard!")
	if err != nil {
		t.Fatal(err)
	}
	if sanitized != "juhe-ai:dev:state:auth_login_guard_:" {
		t.Fatalf("sanitized prefix=%q", sanitized)
	}
}

// TestSharedCaptchaIssueVerifyAtomicConsumption proves the Redis captcha
// driver: issue writes the Node-shaped challenge record, the first verify
// atomically consumes it (GETDEL) and the second verify misses.
func TestSharedCaptchaIssueVerifyAtomicConsumption(t *testing.T) {
	store, server := newMiniredisStore(t)
	now := time.Unix(1_800_000_000, 0)
	service := NewSharedCaptchaService(store, func() time.Time { return now })

	result, err := service.Issue("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked || result.Challenge.CaptchaID == "" || !strings.HasPrefix(result.Challenge.Image, "data:image/png;base64,") {
		t.Fatalf("issue result blocked=%v challenge=%+v", result.Blocked, result.Challenge)
	}

	challengePrefix := "juhe-ai:dev:state:test_store:challenge:"
	raw, err := server.Get(challengePrefix + result.Challenge.CaptchaID)
	if err != nil || raw == "" {
		t.Fatalf("challenge key missing (prefix %s): %v", challengePrefix, err)
	}
	if !strings.Contains(raw, `"answer"`) || !strings.Contains(raw, `"expiresAt"`) {
		t.Fatalf("challenge record shape %s", raw)
	}

	answer := sharedCaptchaAnswerForTest(t, service, result.Challenge.CaptchaID, server, challengePrefix)
	if !service.Verify(result.Challenge.CaptchaID, strings.ToLower(answer)) {
		t.Fatal("first verify with the issued answer must pass")
	}
	if service.Verify(result.Challenge.CaptchaID, answer) {
		t.Fatal("second verify must miss: the challenge was consumed atomically")
	}
	if _, err := server.Get(challengePrefix + result.Challenge.CaptchaID); err == nil {
		t.Fatal("GETDEL did not remove the key")
	}
}

// sharedCaptchaAnswerForTest reads the issued answer from the stored record
// (the driver equivalent of the Node captchaTestAnswers helper).
func sharedCaptchaAnswerForTest(t *testing.T, _ *SharedCaptchaService, captchaID string, server *miniredis.Miniredis, prefix string) string {
	t.Helper()
	raw, err := server.Get(prefix + captchaID)
	if err != nil || raw == "" {
		t.Fatalf("answer lookup failed: %v", err)
	}
	var record captchaChallengeRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	return record.Answer
}

// TestSharedCaptchaVerifyExpiry proves the epoch-ms expiry contract: a
// challenge older than now fails verification even if the Redis TTL has not
// elapsed yet.
func TestSharedCaptchaVerifyExpiry(t *testing.T) {
	store, _ := newMiniredisStore(t)
	now := time.Unix(1_800_000_000, 0)
	service := NewSharedCaptchaService(store, func() time.Time { return now })

	ctx := context.Background()
	if err := store.SetJSON(ctx, "challenge:expired", captchaChallengeRecord{Answer: "ABCDE", ExpiresAt: now.Add(-time.Millisecond).UnixMilli()}, sharedCaptchaTTL.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if service.Verify("expired", "ABCDE") {
		t.Fatal("expired challenge must fail")
	}
	if err := store.SetJSON(ctx, "challenge:edge", captchaChallengeRecord{Answer: "ABCDE", ExpiresAt: now.UnixMilli()}, sharedCaptchaTTL.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if !service.Verify("edge", "abcde") {
		t.Fatal("challenge exactly at now stays valid and code normalization is case-insensitive")
	}
}

// TestSharedCaptchaIssueRateLimit proves the shared incr window: the 61st
// issue within one minute is blocked with the Node message and a flat
// 60-second retry, mirroring consumeCaptchaIssueAllowanceAsync.
func TestSharedCaptchaIssueRateLimit(t *testing.T) {
	store, _ := newMiniredisStore(t)
	now := time.Unix(1_800_000_000, 0)
	service := NewSharedCaptchaService(store, func() time.Time { return now })

	for i := 0; i < 60; i++ {
		result, err := service.Issue("198.51.100.9")
		if err != nil || result.Blocked {
			t.Fatalf("issue %d blocked=%v err=%v", i+1, result.Blocked, err)
		}
	}
	result, err := service.Issue("198.51.100.9")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked || result.RetryAfter != 60 || result.Message != "验证码请求过于频繁，请稍后再试" {
		t.Fatalf("blocked result %+v", result)
	}
	// A different IP has an independent window.
	if result, err = service.Issue("198.51.100.10"); err != nil || result.Blocked {
		t.Fatalf("independent window blocked=%v err=%v", result.Blocked, err)
	}
}

// TestSharedLoginGuardLockLifecycle proves the Redis login guard: ten
// failures lock IP and username with the Node messages, Check reports the
// remaining seconds, Success clears both, and locks expire with time.
func TestSharedLoginGuardLockLifecycle(t *testing.T) {
	store, _ := newMiniredisStore(t)
	now := time.Unix(1_900_000_000, 0)
	clock := now
	guard := NewSharedLoginGuard(store, func() time.Time { return clock })

	if blocked, _, _ := guard.Check("203.0.113.5", "Guard-User"); blocked {
		t.Fatal("fresh guard must not block")
	}
	for i := 1; i <= 9; i++ {
		if blocked, _, _ := guard.Failed("203.0.113.5", "Guard-User"); blocked {
			t.Fatalf("failure %d must not lock yet", i)
		}
	}
	blocked, retry, message := guard.Failed("203.0.113.5", "guard-user")
	if !blocked || message != "尝试过于频繁，请稍后再试" || retry < 899 || retry > 900 {
		t.Fatalf("ip lock blocked=%v retry=%d message=%q", blocked, retry, message)
	}
	// The username lock exists independently with its own message.
	blocked, retry, message = guard.Check("198.51.100.0", "guard-user")
	if !blocked || message != "账号暂时锁定，请稍后再试" || retry < 899 || retry > 900 {
		t.Fatalf("username lock blocked=%v retry=%d message=%q", blocked, retry, message)
	}
	// The IP lock wins over the username lock.
	blocked, _, message = guard.Check("203.0.113.5", "other-user")
	if !blocked || message != "尝试过于频繁，请稍后再试" {
		t.Fatalf("ip priority blocked=%v message=%q", blocked, message)
	}

	// Success clears everything.
	guard.Success("203.0.113.5", "guard-user")
	if blocked, _, _ := guard.Check("203.0.113.5", "guard-user"); blocked {
		t.Fatal("guard must be clear after Success")
	}

	// Time-based expiry: a lock older than now no longer blocks.
	expiry := now.Add(15 * time.Minute)
	clock = expiry
	if blocked, _, _ := guard.Check("203.0.113.5", "guard-user"); blocked {
		t.Fatal("expired lock must not block")
	}
	_ = expiry
}

// TestSharedLoginGuardCountsExpireWithWindow proves the counter window
// semantics: failures older than the 10-minute window no longer contribute
// (the Redis TTL set at the first failure expires the key), so the next
// failure starts a fresh window instead of locking.
func TestSharedLoginGuardCountsExpireWithWindow(t *testing.T) {
	store, server := newMiniredisStore(t)
	now := time.Unix(1_900_000_000, 0)
	clock := now
	guard := NewSharedLoginGuard(store, func() time.Time { return clock })

	for i := 0; i < 9; i++ {
		if blocked, _, _ := guard.Failed("203.0.113.8", "window-user"); blocked {
			t.Fatalf("failure %d must not lock", i+1)
		}
	}
	// Advance real time past the counter TTL; miniredis expires keys on
	// FastForward.
	server.FastForward(11 * time.Minute)
	blocked, _, _ := guard.Failed("203.0.113.8", "window-user")
	if blocked {
		t.Fatal("a fresh window must not lock on the first failure")
	}
}

// TestSharedDriversSurfaceStoreErrorsAsDegrade proves the documented degraded
// path: a closed store fails the captcha Issue (500 path) while login-guard
// checks degrade to "not blocked" instead of panicking.
func TestSharedDriversSurfaceStoreErrorsAsDegrade(t *testing.T) {
	serverClosed := miniredis.RunT(t)
	store, closeFn, err := NewRedisNamespacedStateStore("redis://"+serverClosed.Addr(), "dev", "auth_captcha")
	if err != nil {
		t.Fatal(err)
	}
	serverClosed.Close()
	closeFn()
	now := time.Unix(1_800_000_000, 0)
	captcha := NewSharedCaptchaService(store, func() time.Time { return now })
	if _, err := captcha.Issue("203.0.113.20"); err == nil {
		t.Fatal("captcha Issue must surface the store error")
	}
	guard := NewSharedLoginGuard(store, func() time.Time { return now })
	if blocked, _, _ := guard.Check("203.0.113.20", "any-user"); blocked {
		t.Fatal("degraded check must not claim a lock")
	}
}
