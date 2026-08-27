package modelcheckauth

import (
	"strings"
	"sync"
	"time"
)

const (
	loginGuardWindow = 10 * time.Minute
	loginGuardLock   = 15 * time.Minute
	loginGuardLimit  = 10
)

type loginAttempt struct {
	timestamps  []time.Time
	lockedUntil time.Time
}

// LoginGuard mirrors the Node memory guard. It is deliberately injectable so
// a shared runtime-state implementation can replace it before multi-instance
// production cutover without changing the HTTP contract.
type LoginGuard struct {
	mu     sync.Mutex
	now    func() time.Time
	byIP   map[string]loginAttempt
	byUser map[string]loginAttempt
}

func NewLoginGuard(now func() time.Time) *LoginGuard {
	if now == nil {
		now = time.Now
	}
	return &LoginGuard{now: now, byIP: map[string]loginAttempt{}, byUser: map[string]loginAttempt{}}
}

func (g *LoginGuard) Check(ip, username string) (bool, int, string) {
	if g == nil {
		return false, 0, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now().UTC()
	if blocked, retry := activeLock(g.byIP[ip], now); blocked {
		return true, retry, "尝试过于频繁，请稍后再试"
	}
	key := strings.ToLower(strings.TrimSpace(username))
	if blocked, retry := activeLock(g.byUser[key], now); blocked {
		return true, retry, "账号暂时锁定，请稍后再试"
	}
	return false, 0, ""
}

func (g *LoginGuard) Failed(ip, username string) (bool, int, string) {
	if g == nil {
		return false, 0, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now().UTC()
	ipRecord := recordFailure(g.byIP[ip], now)
	g.byIP[ip] = ipRecord
	if blocked, retry := activeLock(ipRecord, now); blocked {
		return true, retry, "尝试过于频繁，请稍后再试"
	}
	key := strings.ToLower(strings.TrimSpace(username))
	userRecord := recordFailure(g.byUser[key], now)
	g.byUser[key] = userRecord
	if blocked, retry := activeLock(userRecord, now); blocked {
		return true, retry, "账号暂时锁定，请稍后再试"
	}
	return false, 0, ""
}

func (g *LoginGuard) Success(ip, username string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byIP, ip)
	delete(g.byUser, strings.ToLower(strings.TrimSpace(username)))
}

func recordFailure(record loginAttempt, now time.Time) loginAttempt {
	recent := record.timestamps[:0]
	cutoff := now.Add(-loginGuardWindow)
	for _, timestamp := range record.timestamps {
		if !timestamp.Before(cutoff) {
			recent = append(recent, timestamp)
		}
	}
	recent = append(recent, now)
	if len(recent) >= loginGuardLimit {
		record.lockedUntil = now.Add(loginGuardLock)
	}
	if len(recent) > loginGuardLimit {
		recent = recent[len(recent)-loginGuardLimit:]
	}
	record.timestamps = recent
	return record
}

func activeLock(record loginAttempt, now time.Time) (bool, int) {
	if record.lockedUntil.IsZero() || !record.lockedUntil.After(now) {
		return false, 0
	}
	return true, maxIntCeilSeconds(record.lockedUntil.Sub(now))
}

func maxIntCeilSeconds(value time.Duration) int {
	seconds := int(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}
