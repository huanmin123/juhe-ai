package modelcheckauth

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginGuardLocksIPAndClearsOnSuccess(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		if blocked, _, _, err := guard.Failed("127.0.0.1", "admin"); blocked || err != nil {
			t.Fatalf("attempt %d unexpectedly blocked: blocked=%v err=%v", i, blocked, err)
		}
	}
	blocked, retry, message, err := guard.Failed("127.0.0.1", "admin")
	if err != nil || !blocked || retry != 900 || message == "" {
		t.Fatalf("err=%v blocked=%v retry=%d message=%q", err, blocked, retry, message)
	}
	guard.Success("127.0.0.1", "admin")
	if blocked, _, _, err := guard.Check("127.0.0.1", "admin"); blocked || err != nil {
		t.Fatalf("successful login must clear lock: blocked=%v err=%v", blocked, err)
	}
}

func TestLoginGuardExpiresFailuresOutsideWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		guard.Failed("127.0.0.1", "admin")
	}
	now = now.Add(11 * time.Minute)
	if blocked, _, _, err := guard.Check("127.0.0.1", "admin"); blocked || err != nil {
		t.Fatalf("failures outside ten-minute window must expire: blocked=%v err=%v", blocked, err)
	}
}

func TestLoginGuardRecordsUsernameWhenIPLockWins(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		if blocked, _, _, err := guard.Failed("127.0.0.1", " Admin "); blocked || err != nil {
			t.Fatalf("attempt %d unexpectedly blocked: blocked=%v err=%v", i, blocked, err)
		}
	}
	if blocked, _, message, err := guard.Failed("127.0.0.1", " Admin "); err != nil || !blocked || message != "尝试过于频繁，请稍后再试" {
		t.Fatalf("tenth IP failure must return the IP lock: err=%v blocked=%v message=%q", err, blocked, message)
	}
	if blocked, retry, message, err := guard.Check("203.0.113.9", "admin"); err != nil || !blocked || retry != 900 || message != "账号暂时锁定，请稍后再试" {
		t.Fatalf("tenth IP failure must also lock the username: err=%v blocked=%v retry=%d message=%q", err, blocked, retry, message)
	}
}

func TestLoginGuardEvictsOldestIPKeysAtCapacity(t *testing.T) {
	clock := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return clock })
	for i := 0; i < loginGuardMaxIPKeys; i++ {
		clock = clock.Add(time.Second)
		if _, _, _, err := guard.Failed(fmt.Sprintf("ip-%04d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(guard.byIP) != loginGuardMaxIPKeys {
		t.Fatalf("byIP=%d entries, want capacity %d", len(guard.byIP), loginGuardMaxIPKeys)
	}
	// One more distinct source forces the eviction of the oldest-activity
	// entry while the map stays capped and the newest entry survives.
	clock = clock.Add(time.Second)
	if _, _, _, err := guard.Failed("ip-newest", ""); err != nil {
		t.Fatal(err)
	}
	if len(guard.byIP) != loginGuardMaxIPKeys {
		t.Fatalf("byIP=%d entries after eviction, want capacity %d", len(guard.byIP), loginGuardMaxIPKeys)
	}
	if _, ok := guard.byIP["ip-0000"]; ok {
		t.Fatal("oldest ip key must be evicted first")
	}
	if _, ok := guard.byIP["ip-newest"]; !ok {
		t.Fatal("newest ip key must survive eviction")
	}
}

func TestLoginGuardEvictsOldestUserKeysAtCapacity(t *testing.T) {
	clock := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return clock })
	for i := 0; i < loginGuardMaxUserKeys; i++ {
		clock = clock.Add(time.Second)
		if _, _, _, err := guard.Failed("203.0.113.1", fmt.Sprintf("User-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(guard.byUser) != loginGuardMaxUserKeys {
		t.Fatalf("byUser=%d entries, want capacity %d", len(guard.byUser), loginGuardMaxUserKeys)
	}
	clock = clock.Add(time.Second)
	if _, _, _, err := guard.Failed("203.0.113.1", "user-newest"); err != nil {
		t.Fatal(err)
	}
	if len(guard.byUser) != loginGuardMaxUserKeys {
		t.Fatalf("byUser=%d entries after eviction, want capacity %d", len(guard.byUser), loginGuardMaxUserKeys)
	}
	if _, ok := guard.byUser["user-0000"]; ok {
		t.Fatal("oldest user key must be evicted first")
	}
	if _, ok := guard.byUser["user-newest"]; !ok {
		t.Fatal("newest user key must survive eviction")
	}
}

func TestLoginGuardEvictsExpiredKeysBeforeOldestFreshKey(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := now
	guard := NewLoginGuard(func() time.Time { return clock })
	for i := 0; i < loginGuardMaxIPKeys; i++ {
		if _, _, _, err := guard.Failed(fmt.Sprintf("ip-%04d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	// Age the whole map past the window: the expired pass must clear room so
	// the forced oldest-activity pass never runs on fresh keys.
	clock = clock.Add(11 * time.Minute)
	if _, _, _, err := guard.Failed("ip-newest", ""); err != nil {
		t.Fatal(err)
	}
	if len(guard.byIP) != 1 {
		t.Fatalf("byIP=%d entries, expired keys must be purged down to the newest only", len(guard.byIP))
	}
	if _, ok := guard.byIP["ip-newest"]; !ok {
		t.Fatal("newest key must survive the expired purge")
	}
}
