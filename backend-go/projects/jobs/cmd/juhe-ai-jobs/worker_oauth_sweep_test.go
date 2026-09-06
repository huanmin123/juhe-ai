package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// fakeGroupStatsDirtyMarker 记录 reason 的可回放 Mock。
type fakeGroupStatsDirtyMarker struct {
	reasons []string
	err     error
}

func (f *fakeGroupStatsDirtyMarker) MarkAllGroupAccountStatsDirty(_ context.Context, reason string, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.reasons = append(f.reasons, reason)
	return nil
}

func TestMarkAllGroupAccountStatsAfterAuthzWrite(t *testing.T) {
	logger := slog.Default()
	ctx := context.Background()

	// Node：expired>0 → refreshGroupAccountStatsAfterWriteAsync({all:true,
	// reason:'authorization_expired'}) → markAllGroupAccountStatsDirty(reason)。
	marker := &fakeGroupStatsDirtyMarker{}
	if err := markAllGroupAccountStatsAfterAuthzWrite(ctx, logger, marker, "authorization_expired"); err != nil {
		t.Fatal(err)
	}
	if len(marker.reasons) != 1 || marker.reasons[0] != "authorization_expired" {
		t.Fatalf("reasons=%v", marker.reasons)
	}

	// marker 写入失败 → 错误原样上抛（任务本轮失败）。
	failing := &fakeGroupStatsDirtyMarker{err: errors.New("dirty write failed")}
	if err := markAllGroupAccountStatsAfterAuthzWrite(ctx, logger, failing, "authorization_expired"); err == nil {
		t.Fatal("脏标记写入失败必须上抛")
	}

	// marker 缺席（stats 家族未装配）→ 显式 warn 分支，不静默、不报错。
	if err := markAllGroupAccountStatsAfterAuthzWrite(ctx, logger, nil, "authorization_expired"); err != nil {
		t.Fatalf("marker 缺席走登记分支: %v", err)
	}
}
