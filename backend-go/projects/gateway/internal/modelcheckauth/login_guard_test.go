package modelcheckauth

import (
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
