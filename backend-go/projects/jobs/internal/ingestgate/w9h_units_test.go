package ingestgate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestW9HCheckArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	// nil probe → gate 失败。
	if _, err := Check(context.Background(), nil, now); err == nil || !strings.Contains(err.Error(), "队列快照不可用") {
		t.Fatalf("nil probe err=%v", err)
	}
	// probe 错误 → 失败。
	failing := Probe(func(context.Context) (*DrainStatus, error) { return nil, errors.New("ipc down") })
	if _, err := Check(context.Background(), failing, now); err == nil || !strings.Contains(err.Error(), "队列快照不可用") {
		t.Fatalf("probe error err=%v", err)
	}
	// nil 快照 → 失败。
	nilStatus := Probe(func(context.Context) (*DrainStatus, error) { return nil, nil })
	if _, err := Check(context.Background(), nilStatus, now); err == nil {
		t.Fatal("nil status must fail")
	}
	// not ready → 失败。
	notReady := Probe(func(context.Context) (*DrainStatus, error) { return &DrainStatus{}, nil })
	if _, err := Check(context.Background(), notReady, now); err == nil {
		t.Fatal("not ready must fail")
	}
	// flush 失败且有积压 → 等待恢复。
	stalled := Probe(func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueFlushFailureCount: 2, SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-06T11:00:00.000Z"}, nil
	})
	if _, err := Check(context.Background(), stalled, now); err == nil || !strings.Contains(err.Error(), "等待写入队列恢复") {
		t.Fatalf("stalled err=%v", err)
	}
	// flush 失败但积压为空 → 用默认安全时间。
	recovered := Probe(func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueFlushFailureCount: 2}, nil
	})
	safety, err := Check(context.Background(), recovered, now)
	if err != nil {
		t.Fatalf("recovered err=%v", err)
	}
	if safety.SafeCreatedBefore != "2026-09-06T11:59:45.000Z" {
		t.Fatalf("default safe=%q", safety.SafeCreatedBefore)
	}
	// 积压早于默认 → 回退到最旧记录前 1ms。
	backlog := Probe(func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-06T10:00:00.000Z"}, nil
	})
	safety, err = Check(context.Background(), backlog, now)
	if err != nil || safety.SafeCreatedBefore != "2026-09-06T09:59:59.999Z" {
		t.Fatalf("backlog safe=%q err=%v", safety.SafeCreatedBefore, err)
	}
	// 积压晚于默认 → 保留默认。
	fresh := Probe(func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-06T11:59:50.000Z"}, nil
	})
	safety, err = Check(context.Background(), fresh, now)
	if err != nil || safety.SafeCreatedBefore != "2026-09-06T11:59:45.000Z" {
		t.Fatalf("fresh backlog safe=%q err=%v", safety.SafeCreatedBefore, err)
	}
	// 非法时间戳 → 报错。
	junk := Probe(func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueOldestCreatedAt: "junk"}, nil
	})
	if _, err := Check(context.Background(), junk, now); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("junk err=%v", err)
	}
	// Gate 适配器把 Check 错误原样返回。
	gateErr := Gate(stalled, func() time.Time { return now })(context.Background())
	if gateErr == nil || !strings.Contains(gateErr.Error(), "等待写入队列恢复") {
		t.Fatalf("gate err=%v", gateErr)
	}
	// 成功路径不返回错误。
	if err := Gate(recovered, func() time.Time { return now })(context.Background()); err != nil {
		t.Fatalf("gate ok err=%v", err)
	}
}

func TestW9HOldestIsoAndSafeCreatedBeforeErrorArms(t *testing.T) {
	// oldestIso 错误分支：任一侧非法时间戳。
	if _, err := oldestIso("junk", ""); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("left junk err=%v", err)
	}
	if _, err := oldestIso("", "junk"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("right junk err=%v", err)
	}
	if _, err := oldestIso("2026-01-01T00:00:00Z", "junk"); err == nil {
		t.Fatal("mixed junk must fail")
	}
	// 合法输入取较早者；空值跳过。
	older, err := oldestIso("2026-01-01T00:00:01Z", "2026-01-01T00:00:00Z")
	if err != nil || older != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("older=%q err=%v", older, err)
	}
	// safeCreatedBeforeForPendingBacklog 错误分支。
	if _, err := safeCreatedBeforeForPendingBacklog("junk", ""); err == nil {
		t.Fatal("bad default must fail")
	}
	if _, err := safeCreatedBeforeForPendingBacklog("2026-01-01T00:00:00.000Z", "junk"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("bad oldest err=%v", err)
	}
	// 积压晚于默认 → 保留默认。
	got, err := safeCreatedBeforeForPendingBacklog("2026-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")
	if err != nil || got != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("fresh backlog got=%q err=%v", got, err)
	}
}
