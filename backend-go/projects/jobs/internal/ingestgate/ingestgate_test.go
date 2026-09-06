package ingestgate

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fixedNow 钉住门控的"当前时间"（时间注入硬门禁）。
func fixedNow() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }

// wantDefault = 2026-09-04T11:59:45.000Z（now - 15s）。
const wantDefault = "2026-09-04T11:59:45.000Z"

func TestCheckUnavailableSnapshot(t *testing.T) {
	cases := map[string]struct {
		probe Probe
	}{
		"nil probe":                 {probe: nil},
		"nil status":                {probe: func(context.Context) (*DrainStatus, error) { return nil, nil }},
		"probe error":               {probe: func(context.Context) (*DrainStatus, error) { return nil, errors.New("ipc broken") }},
		"not ready":                 {probe: func(context.Context) (*DrainStatus, error) { return &DrainStatus{Ready: false}, nil }},
		"ctx cancelled unavailable": {probe: func(ctx context.Context) (*DrainStatus, error) { return nil, ctx.Err() }},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Check(context.Background(), testCase.probe, fixedNow())
			if err == nil {
				t.Fatalf("期望快照不可用错误，得到 nil")
			}
			if err.Error() != "ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录" {
				t.Fatalf("错误文案必须逐字对照 Node：%q", err.Error())
			}
		})
	}
}

func TestCheckFlushFailureWithBacklog(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{
			Ready:                                   true,
			SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-04T11:00:00.000Z",
			SnapshotUsageRecordQueueFlushFailureCount: 3,
		}, nil
	}
	_, err := Check(context.Background(), probe, fixedNow())
	if err == nil {
		t.Fatalf("期望写入失败且仍有积压错误，得到 nil")
	}
	want := "使用记录 ingest 队列已有 3 次写入失败且仍有待处理记录，本轮跳过统计聚合，等待写入队列恢复"
	if err.Error() != want {
		t.Fatalf("错误文案不匹配 Node：%q", err.Error())
	}
}

func TestCheckFlushFailureWithoutBacklogKeepsDefault(t *testing.T) {
	// Node：flushFailureCount > 0 且 oldestPendingCreatedAt === undefined →
	// 不报错，safeCreatedBefore 取默认。
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueFlushFailureCount: 2}, nil
	}
	safety, err := Check(context.Background(), probe, fixedNow())
	if err != nil {
		t.Fatalf("无积压时写入失败不应阻塞： %v", err)
	}
	if safety.SafeCreatedBefore != wantDefault {
		t.Fatalf("safeCreatedBefore = %q, want %q", safety.SafeCreatedBefore, wantDefault)
	}
}

func TestCheckSafeCreatedBefore(t *testing.T) {
	cases := map[string]struct {
		oldest string
		want   string
	}{
		"无积压用默认":              {oldest: "", want: wantDefault},
		"积压晚于默认用默认":           {oldest: "2026-09-04T12:00:00.000Z", want: wantDefault},
		"积压早于默认回退前1ms":        {oldest: "2026-09-04T11:30:00.000Z", want: "2026-09-04T11:29:59.999Z"},
		"带数值offset规范化后比较":     {oldest: "2026-09-04T19:30:00+08:00", want: "2026-09-04T11:29:59.999Z"},
		"pending与snapshot取最旧": {oldest: "2026-09-04T10:00:00.000Z", want: "2026-09-04T09:59:59.999Z"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			pending, snapshot := "", testCase.oldest
			if name == "pending与snapshot取最旧" {
				pending, snapshot = testCase.oldest, "2026-09-04T11:00:00.000Z"
			}
			probe := func(context.Context) (*DrainStatus, error) {
				return &DrainStatus{
					Ready:                                   true,
					PendingUsageRecordsOldestCreatedAt:      pending,
					SnapshotUsageRecordQueueOldestCreatedAt: snapshot,
				}, nil
			}
			safety, err := Check(context.Background(), probe, fixedNow())
			if err != nil {
				t.Fatalf("Check 失败: %v", err)
			}
			if safety.SafeCreatedBefore != testCase.want {
				t.Fatalf("safeCreatedBefore = %q, want %q", safety.SafeCreatedBefore, testCase.want)
			}
		})
	}
}

func TestCheckRedisStreamOldestFeedsBacklog(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{
			Ready:                      true,
			RedisStreamOldestCreatedAt: "2026-09-04T10:00:00.000Z",
		}, nil
	}
	safety, err := Check(context.Background(), probe, fixedNow())
	if err != nil {
		t.Fatalf("Check 失败: %v", err)
	}
	if want := "2026-09-04T09:59:59.999Z"; safety.SafeCreatedBefore != want {
		t.Fatalf("safeCreatedBefore = %q, want %q", safety.SafeCreatedBefore, want)
	}
}

func TestCheckInvalidTimestampsFail(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{
			Ready:                                   true,
			SnapshotUsageRecordQueueOldestCreatedAt: "not-a-time",
		}, nil
	}
	_, err := Check(context.Background(), probe, fixedNow())
	if err == nil || err.Error() != "使用记录 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("期望 Node 同款时间戳错误，得到 %v", err)
	}
}

func TestGateSkipsRoundOnError(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) { return nil, nil }
	calls := 0
	gate := Gate(probe, fixedNow)
	err := gate(context.Background())
	if err == nil {
		t.Fatalf("门控必须失败本轮")
	}
	calls++
	_ = calls
}

func TestGatePassesWhenDrained(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) { return &DrainStatus{Ready: true}, nil }
	gate := Gate(probe, fixedNow)
	if err := gate(context.Background()); err != nil {
		t.Fatalf("排干后门控应放行: %v", err)
	}
}
