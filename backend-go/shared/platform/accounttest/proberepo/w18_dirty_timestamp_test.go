package proberepo

import (
	"strings"
	"testing"
)

// BUG-0180 回归：账户行时间戳脏数据必须按错误返回而非 panic。修复前
// deriveEffectiveAvailability 对无法解析的 account_expires_at/cooldown_until
// 直接 panic，probe worker 读到一条脏行即进入崩溃循环。
func TestW18DeriveDirtyTimestampReturnsError(t *testing.T) {
	s := &Store{}

	base := w7cDeriveBase()
	base.expiresAt = "not-a-timestamp"
	if _, _, _, _, err := s.deriveEffectiveAvailability(base); err == nil {
		t.Fatal("非法 account_expires_at 必须返回错误")
	} else if !strings.Contains(err.Error(), "account_expires_at") {
		t.Fatalf("错误应指明脏字段: %v", err)
	}

	cooldown := w7cDeriveBase()
	cooldown.cooldownUntil = "\x00dirty"
	if _, _, _, _, err := s.deriveEffectiveAvailability(cooldown); err == nil {
		t.Fatal("非法 cooldown_until 必须返回错误")
	} else if !strings.Contains(err.Error(), "cooldown_until") {
		t.Fatalf("错误应指明脏字段: %v", err)
	}

	// 合法时间戳不受影响：expired 与未过期两臂。
	expired := w7cDeriveBase()
	expired.expiresAt = "2020-01-01T00:00:00.000Z"
	avail, status, _, _, err := s.deriveEffectiveAvailability(expired)
	if err != nil || avail || status != "instance_expired" {
		t.Fatalf("过期臂: avail=%v status=%q err=%v", avail, status, err)
	}
	future := w7cDeriveBase()
	future.expiresAt = "2099-01-01T00:00:00.000Z"
	if _, _, _, _, err = s.deriveEffectiveAvailability(future); err != nil {
		t.Fatalf("未过期臂不应报错: %v", err)
	}
}
