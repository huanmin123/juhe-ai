package modelcheckauth

import (
	"testing"
	"time"
)

func TestLoginGuardLocksIPAndClearsOnSuccess(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		if blocked, _, _ := guard.Failed("127.0.0.1", "admin"); blocked {
			t.Fatalf("attempt %d unexpectedly blocked", i)
		}
	}
	blocked, retry, message := guard.Failed("127.0.0.1", "admin")
	if !blocked || retry != 900 || message == "" {
		t.Fatalf("blocked=%v retry=%d message=%q", blocked, retry, message)
	}
	guard.Success("127.0.0.1", "admin")
	if blocked, _, _ := guard.Check("127.0.0.1", "admin"); blocked {
		t.Fatal("successful login must clear lock")
	}
}

func TestLoginGuardExpiresFailuresOutsideWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		guard.Failed("127.0.0.1", "admin")
	}
	now = now.Add(11 * time.Minute)
	if blocked, _, _ := guard.Check("127.0.0.1", "admin"); blocked {
		t.Fatal("failures outside ten-minute window must expire")
	}
}

func TestLoginGuardRecordsUsernameWhenIPLockWins(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	for i := 0; i < 9; i++ {
		if blocked, _, _ := guard.Failed("127.0.0.1", " Admin "); blocked {
			t.Fatalf("attempt %d unexpectedly blocked", i)
		}
	}
	if blocked, _, message := guard.Failed("127.0.0.1", " Admin "); !blocked || message != "尝试过于频繁，请稍后再试" {
		t.Fatalf("tenth IP failure must return the IP lock: blocked=%v message=%q", blocked, message)
	}
	if blocked, retry, message := guard.Check("203.0.113.9", "admin"); !blocked || retry != 900 || message != "账号暂时锁定，请稍后再试" {
		t.Fatalf("tenth IP failure must also lock the username: blocked=%v retry=%d message=%q", blocked, retry, message)
	}
}
