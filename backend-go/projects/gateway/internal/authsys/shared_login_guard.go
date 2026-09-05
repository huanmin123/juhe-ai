package authsys

import (
	"math"
	"strings"
	"time"
)

// Shared login-guard contract constants mirror login-guard.service.ts:
// windowMs 10min, lockMs 15min, ip/username failure thresholds 10.
const (
	sharedLoginWindowMs = int64(10 * time.Minute / time.Millisecond)
	sharedLoginLock     = 15 * time.Minute
	sharedLoginLimit    = int64(10)
)

// sharedLoginGuardStateStoreName mirrors
// createRuntimeStateStore('auth_login_guard') so Go and Node share one Redis
// key space during the migration window.
const sharedLoginGuardStateStoreName = "auth_login_guard"

// SharedLoginGuard is the Redis runtime-state login throttle driver mirroring
// the Node async paths (checkLoginAllowedAsync / recordFailedLoginAsync /
// recordSuccessfulLoginAsync): the failure counter is a shared incr with a
// 10-minute TTL, the lock is a shared JSON timestamp with a 15-minute TTL,
// and successful login clears both (BUG-0171.4). The memory driver remains
// modelcheckauth.LoginGuard.
//
// Degraded path: the LoginGuardDriver surface is error-free because the
// process-local *modelcheckauth.LoginGuard must keep satisfying it, so a
// Redis read/write failure degrades to "not locked" instead of the Node 500.
// This only differs from Node while the state Redis is unreachable.
type SharedLoginGuard struct {
	store RedisStateStore
	now   func() time.Time
}

// NewSharedLoginGuard builds the driver over an injected state store.
func NewSharedLoginGuard(store RedisStateStore, now func() time.Time) *SharedLoginGuard {
	if now == nil {
		now = time.Now
	}
	return &SharedLoginGuard{store: store, now: now}
}

// NewRedisLoginGuard builds the driver over the auth_login_guard Redis state
// store; the returned close func releases the client.
func NewRedisLoginGuard(url, namespace string, now func() time.Time) (*SharedLoginGuard, func(), error) {
	store, closeFn, err := NewRedisNamespacedStateStore(url, namespace, sharedLoginGuardStateStoreName)
	if err != nil {
		return nil, nil, err
	}
	return NewSharedLoginGuard(store, now), closeFn, nil
}

func normalizeSharedLoginUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func (g *SharedLoginGuard) counterKey(scope, value string) string {
	return "login:" + scope + ":" + value + ":count"
}

func (g *SharedLoginGuard) lockKey(scope, value string) string {
	return "login:" + scope + ":" + value + ":lock"
}

// Check mirrors checkLoginAllowedAsync: the IP lock wins over the username
// lock; messages match the Node strings byte for byte.
func (g *SharedLoginGuard) Check(ip, username string) (bool, int, string) {
	now := g.now()
	if blocked, retry := g.lockBlock("ip", strings.TrimSpace(ip), now); blocked {
		return blocked, retry, "尝试过于频繁，请稍后再试"
	}
	if blocked, retry := g.lockBlock("username", normalizeSharedLoginUsername(username), now); blocked {
		return blocked, retry, "账号暂时锁定，请稍后再试"
	}
	return false, 0, ""
}

// Failed mirrors recordFailedLoginAsync: the IP and username attempts are
// recorded independently (Node Promise.all) and the IP result wins.
func (g *SharedLoginGuard) Failed(ip, username string) (bool, int, string) {
	now := g.now()
	ipBlocked, ipRetry := g.recordAttempt("ip", strings.TrimSpace(ip), now)
	userBlocked, userRetry := g.recordAttempt("username", normalizeSharedLoginUsername(username), now)
	if ipBlocked {
		return true, ipRetry, "尝试过于频繁，请稍后再试"
	}
	if userBlocked {
		return true, userRetry, "账号暂时锁定，请稍后再试"
	}
	return false, 0, ""
}

// Success mirrors recordSuccessfulLoginAsync: both counters and both locks
// are cleared. The cleanup is best-effort — the session has already been
// issued when this runs.
func (g *SharedLoginGuard) Success(ip, username string) {
	normalized := normalizeSharedLoginUsername(username)
	for _, key := range []string{
		g.counterKey("ip", strings.TrimSpace(ip)),
		g.lockKey("ip", strings.TrimSpace(ip)),
		g.counterKey("username", normalized),
		g.lockKey("username", normalized),
	} {
		_ = g.store.DeleteJSON(nil, key)
	}
}

// recordAttempt mirrors recordRedisAttempt: an already-active lock short
// circuits, otherwise the window counter increments and reaching the
// threshold stores the lock timestamp.
func (g *SharedLoginGuard) recordAttempt(scope, value string, now time.Time) (bool, int) {
	if blocked, retry := g.lockBlock(scope, value, now); blocked {
		return blocked, retry
	}
	count, err := g.store.Incr(nil, g.counterKey(scope, value), sharedLoginWindowMs, -1)
	if err != nil {
		return false, 0
	}
	if count < sharedLoginLimit {
		return false, 0
	}
	lockedUntil := now.Add(sharedLoginLock)
	if err := g.store.SetJSON(nil, g.lockKey(scope, value), lockedUntil.UnixMilli(), sharedLoginLock.Milliseconds()); err != nil {
		return false, 0
	}
	return true, retryAfterSeconds(lockedUntil, now)
}

func (g *SharedLoginGuard) lockBlock(scope, value string, now time.Time) (bool, int) {
	var lockedUntil int64
	ok, err := g.store.GetJSON(nil, g.lockKey(scope, value), &lockedUntil)
	if err != nil || !ok {
		return false, 0
	}
	if lockedUntil <= now.UnixMilli() {
		return false, 0
	}
	return true, retryAfterSeconds(time.UnixMilli(lockedUntil), now)
}

func retryAfterSeconds(lockedUntil, now time.Time) int {
	remaining := lockedUntil.Sub(now)
	if remaining <= 0 {
		return 0
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

var _ LoginGuardDriver = (*SharedLoginGuard)(nil)
