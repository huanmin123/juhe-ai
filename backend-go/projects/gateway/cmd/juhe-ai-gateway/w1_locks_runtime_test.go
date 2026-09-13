package main

// w1: chain_account_locks.go 与 chain_runtime.go 的小适配器直测。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestW1ChainAccountLocksSmallHelpers(t *testing.T) {
	// newChainAccountLocks：nil db fail fast。
	if _, err := newChainAccountLocks(nil, false, nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
	// nil 回调自动补 no-op。
	locks, err := newChainAccountLocks(&sql.DB{}, false, nil)
	if err != nil || locks == nil {
		t.Fatalf("构造 = %v, %v", locks, err)
	}
	if got := locks.table("account_lock_states"); got != "account_lock_states" {
		t.Fatalf("sqlite table = %q", got)
	}
	pgLocks, _ := newChainAccountLocks(&sql.DB{}, true, nil)
	if got := pgLocks.table("account_lock_states"); got != "juhe_business.account_lock_states" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pgLocks.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	if got := locks.bind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	// 环境开关。
	if chainAccountLocksDisabledViaEnv(nil) {
		t.Fatal("nil getter 必须 false")
	}
	if !chainAccountLocksDisabledViaEnv(func(key string) string { return " TRUE " }) {
		t.Fatal("TRUE 必须开启")
	}
	if chainAccountLocksDisabledViaEnv(func(key string) string { return "yes" }) {
		t.Fatal("yes 必须保持真实实现")
	}
	// deadline 解析。
	if _, ok := chainAccountLockDeadlineMs(sql.NullString{}); ok {
		t.Fatal("NULL 必须 not-ok")
	}
	if _, ok := chainAccountLockDeadlineMs(sql.NullString{String: "junk", Valid: true}); ok {
		t.Fatal("非法时间必须 not-ok")
	}
	if ms, ok := chainAccountLockDeadlineMs(sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true}); !ok || ms <= 0 {
		t.Fatalf("合法时间 = %d, %v", ms, ok)
	}
	// blocksCrossAccount 语义。
	locks.blocksCrossAccount(nil, 0)
	engaged := &chainAccountLockRow{enabled: 1, lockState: "ENGAGED"}
	if !locks.blocksCrossAccount(engaged, 0) {
		t.Fatal("无 deadline 的 ENGAGED 必须阻断")
	}
	engaged.deadlineAt = sql.NullString{String: "2020-01-01T00:00:00Z", Valid: true}
	if locks.blocksCrossAccount(engaged, 1_800_000_000_000) {
		t.Fatal("过期 deadline 不阻断")
	}
	disabled := &chainAccountLockRow{enabled: 0, lockState: "ENGAGED"}
	if locks.blocksCrossAccount(disabled, 0) {
		t.Fatal("disabled 不阻断")
	}
	// viewOf：nil 行 nil 视图。
	if locks.viewOf(nil, 0) != nil {
		t.Fatal("nil 行必须 nil 视图")
	}
}

func TestW1ChainRuntimeAdapters(t *testing.T) {
	// settingsTimezoneProvider。
	provider := settingsTimezoneProvider{read: func(string) (string, error) { return "Asia/Tokyo", nil }}
	zone, err := provider.StatsTimezone(context.Background())
	if err != nil || zone == nil {
		t.Fatalf("timezone = %v, %v", zone, err)
	}
	if location, _ := time.LoadLocation("Asia/Tokyo"); zone.String() != location.String() {
		t.Fatalf("zone = %q", zone.String())
	}
	// 空值回落 UTC。
	provider.read = func(string) (string, error) { return "  ", nil }
	if zone, err := provider.StatsTimezone(context.Background()); err != nil || zone.String() != "UTC" {
		t.Fatalf("空值 = %v, %v", zone, err)
	}
	// 读取错误透传。
	provider.read = func(string) (string, error) { return "", errors.New("读取失败") }
	if _, err := provider.StatsTimezone(context.Background()); err == nil {
		t.Fatal("读取错误必须透传")
	}
	// ipstatsTimezone：空值 UTC、错误透传、正常值。
	if got, err := ipstatsTimezone(func(string) (string, error) { return "", nil })(context.Background()); err != nil || got != "UTC" {
		t.Fatalf("ipstats 空值 = %q, %v", got, err)
	}
	if _, err := ipstatsTimezone(func(string) (string, error) { return "", errors.New("x") })(context.Background()); err == nil {
		t.Fatal("ipstats 错误必须透传")
	}
	if got, _ := ipstatsTimezone(func(string) (string, error) { return "Asia/Shanghai", nil })(context.Background()); got != "Asia/Shanghai" {
		t.Fatalf("ipstats = %q", got)
	}
	// redisStateClientProvider：nil client 报错。
	if _, err := (redisStateClientProvider{}).Client(context.Background()); err == nil {
		t.Fatal("nil client 必须报错")
	}
	// 日志桥：不 panic 即可。
	(chainRuntimeLogger{inner: slog.Default()}).Warn("event_1", map[string]any{"k": "v"}, "消息")
	(chainCircuitWaitLogger{inner: slog.Default()}).Info(map[string]any{}, "info 消息")
	(chainCircuitWaitLogger{inner: slog.Default()}).Warn(nil, "warn 消息")
}
