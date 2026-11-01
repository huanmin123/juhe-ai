package keymodelrecovery

// B3-H27 辅助断言：脏 state 隔离告警只输出 capabilityHash 前缀，不泄漏
// state 原文/凭据；告警走注入的 slog logger（组合根经 NewRunner 接线）。

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestB3H27RejectedDueStateWarnIsRedacted(t *testing.T) {
	store, _, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	now := time.Unix(90_000, 0).UTC()
	var buffer bytes.Buffer
	store.SetLogger(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelWarn})))

	hash := "b3h27redact0123456789abcdef"
	if err := client.Set(ctx, keys.State(hash), `{"capabilityHash":"`+hash+`","keyFingerprint":"sk-secret-value",`, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: hash}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, now, 10); err != nil {
		t.Fatalf("脏 state 不得返回错误: %v", err)
	}
	logged := buffer.String()
	if !strings.Contains(logged, "model_recovery_due_state_rejected") {
		t.Fatalf("必须记录隔离告警: %q", logged)
	}
	if !strings.Contains(logged, hash[:capabilityHashLogPrefixLength]) {
		t.Fatalf("告警必须含 capabilityHash 前缀: %q", logged)
	}
	if strings.Contains(logged, "sk-secret-value") || strings.Contains(logged, hash) {
		t.Fatalf("告警不得泄漏原文/完整 hash: %q", logged)
	}
}

func TestB3H27CapabilityHashLogPrefix(t *testing.T) {
	if got := capabilityHashLogPrefix("  abcdefghijklmnop  "); got != "abcdefghijkl" {
		t.Fatalf("前缀截断=%q", got)
	}
	if got := capabilityHashLogPrefix("short"); got != "short" {
		t.Fatalf("短 hash=%q", got)
	}
	if got := capabilityHashLogPrefix(""); got != "" {
		t.Fatalf("空 hash=%q", got)
	}
}
