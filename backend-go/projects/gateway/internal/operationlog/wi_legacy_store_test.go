package operationlog

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestWIMigrateLegacyPostgresStopGate(t *testing.T) {
	// 停机门禁优先于任何数据库连接。
	if _, err := MigrateLegacyPostgres(context.Background(), Config{}, LegacyMigrationOptions{}); err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("停机门禁=%v", err)
	}
}

func TestWIStoreDialectHelpers(t *testing.T) {
	pg := &sqlStore{}
	if got := pg.bind("VALUES (?, ?)"); got != "VALUES ($1, $2)" {
		t.Fatalf("pg bind=%q", got)
	}
	if got := pg.table("operation_logs"); got != "juhe_dataset.operation_logs" {
		t.Fatalf("pg table=%q", got)
	}
	// beginTx 与 beginLegacyMigrationTx 在关闭的库上必须报错。
	store := wiOpenPostgresStore(t)
	implementation := store.(*sqlStore)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.beginTx(context.Background()); err == nil {
		t.Fatal("关闭后 beginTx 必须报错")
	}
	if _, err := implementation.beginLegacyMigrationTx(context.Background()); err == nil {
		t.Fatal("关闭后 beginLegacyMigrationTx 必须报错")
	}
}

func TestWIProducerWarnOnFailure(t *testing.T) {
	store := wiOpenPostgresStore(t)
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "wi-producer", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	recorder := &wiRecordingSlog{warns: make(chan string, 4)}
	// 过期 fence：续租被拒 → 告警并丢弃（fire-and-forget 契约）。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 7}
	producer := NewProducer(store, fixedLeaseSource(stale), Config{OwnerLease: time.Minute}, recorder)
	producer.Record(Input{ID: "wi-op-entry", Summary: "s", CreatedAt: "2026-09-10T08:00:00Z"})
	waitForSlogWarn(t, recorder)
	// 无 Logger 时同样安全。
	quiet := NewProducer(store, fixedLeaseSource(lease), Config{OwnerLease: 0}, nil)
	quiet.Record(Input{ID: "wi-op-entry-2", Summary: "s", CreatedAt: "2026-09-10T08:00:00Z"})
	_ = slog.Default()
}

type wiRecordingSlog struct {
	warns chan string
}

func (r *wiRecordingSlog) Warn(msg string, args ...any) {
	if r.warns != nil {
		r.warns <- msg
	}
}

func (r *wiRecordingSlog) Error(msg string, args ...any) {}

func waitForSlogWarn(t *testing.T, recorder *wiRecordingSlog) {
	t.Helper()
	select {
	case <-recorder.warns:
	case <-time.After(5 * time.Second):
		t.Fatal("Record 失败未告警")
	}
}

func TestWIRetentionFencedAndAccountNames(t *testing.T) {
	store := wiOpenPostgresStore(t)
	time.Sleep(100 * time.Millisecond) // 前一个测试的异步 Record 已结束。
	ctx := context.Background()
	lease, ok, err := store.AcquireOwnerLease(ctx, "wi-retention", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	// 过期 fence → ErrOwnerLeaseLost。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 3}
	if _, err := store.CleanupRetention(ctx, stale, time.Now().UTC().Add(time.Hour), 10); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale retention=%v", err)
	}
	// 正常 fence 且无数据 → 0 删除。
	deleted, err := store.CleanupRetention(ctx, lease, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || deleted != 0 {
		t.Fatalf("空 retention=%d err=%v", deleted, err)
	}
	// accountNames 空输入安全。
	names, err := store.(*sqlStore).accountNames(ctx, nil)
	if err != nil || len(names) != 0 {
		t.Fatalf("accountNames=%v err=%v", names, err)
	}
	// textPrefixUpperBound：前缀上界排除下一个字符。
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("upper=%q", got)
	}
	if got := textPrefixUpperBound("ab\xff"); got == "" || got <= "ab\xff" {
		t.Fatalf("0xff 前缀=%q", got)
	}
}
