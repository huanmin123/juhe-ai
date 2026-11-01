package ingestgate

// T93-6 / C2-H1 钉住测试：gateway 进程内 buffer（spool 落盘之前）不可观测，
// 安全截止时间必须被 now-GatewayBufferMaxAge 压住，游标不得越过可能仍在
// buffer 里的记录；配置项可覆盖该滞留估计。

import (
	"context"
	"testing"
	"time"
)

func t936Probe(status *DrainStatus) Probe {
	return func(context.Context) (*DrainStatus, error) { return status, nil }
}

// 默认（零值 Options）安全水位回退到 now-5min。
func TestT936GatewayBufferBoundsDefaultSafeCreatedBefore(t *testing.T) {
	now := fixedNow()
	safety, err := Check(context.Background(), t936Probe(&DrainStatus{Ready: true}), now)
	if err != nil {
		t.Fatalf("Check 失败: %v", err)
	}
	if want := "2026-09-04T11:55:00.000Z"; safety.SafeCreatedBefore != want {
		t.Fatalf("默认安全水位=%q want %q（now-%s）", safety.SafeCreatedBefore, want, DefaultGatewayBufferMaxAge)
	}
	// 模拟「buffer 长滞留」：记录 created_at = now-4min 仍可能在 gateway
	// buffer 内，游标上界必须早于它。
	buffered := now.Add(-4 * time.Minute)
	if !(safety.SafeCreatedBefore < buffered.UTC().Format("2006-01-02T15:04:05.000Z")) {
		t.Fatalf("游标上界 %q 越过了可能仍在 buffer 的记录 %s", safety.SafeCreatedBefore, buffered)
	}
}

// 配置项可覆盖：buffer 滞留估计晚于 Node 15s 延迟时生效；更小时由 15s 兜底。
func TestT936GatewayBufferMaxAgeIsConfigurable(t *testing.T) {
	now := fixedNow()
	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{name: "更小滞留由 Node 15s 兜底", age: 10 * time.Second, want: "2026-09-04T11:59:45.000Z"},
		{name: "自定义 90s", age: 90 * time.Second, want: "2026-09-04T11:58:30.000Z"},
		{name: "自定义 10min", age: 10 * time.Minute, want: "2026-09-04T11:50:00.000Z"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			safety, err := CheckWithOptions(context.Background(), t936Probe(&DrainStatus{Ready: true}), now, Options{GatewayBufferMaxAge: testCase.age})
			if err != nil {
				t.Fatalf("CheckWithOptions 失败: %v", err)
			}
			if safety.SafeCreatedBefore != testCase.want {
				t.Fatalf("安全水位=%q want %q", safety.SafeCreatedBefore, testCase.want)
			}
		})
	}
}

// 积压更旧时以积压为准（回退到 oldest-1ms）；积压较新时仍不得越过 buffer 界。
func TestT936BacklogInteractsWithGatewayBufferBound(t *testing.T) {
	now := fixedNow()
	// 30 分钟前的积压：早于 buffer 界，回退到 oldest-1ms。
	oldBacklog, err := CheckWithOptions(context.Background(), t936Probe(&DrainStatus{
		Ready:                                   true,
		SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-04T11:30:00.000Z",
	}), now, Options{GatewayBufferMaxAge: time.Minute})
	if err != nil {
		t.Fatalf("CheckWithOptions 失败: %v", err)
	}
	if want := "2026-09-04T11:29:59.999Z"; oldBacklog.SafeCreatedBefore != want {
		t.Fatalf("积压更旧时安全水位=%q want %q", oldBacklog.SafeCreatedBefore, want)
	}
	// 3 分钟前的积压：晚于 buffer 界（now-5min）但早于 now-15s，上界仍取
	// buffer 界——不能因为「可见积压较新」而把游标推进到 now-15s。
	newBacklog, err := CheckWithOptions(context.Background(), t936Probe(&DrainStatus{
		Ready:                                   true,
		SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-04T11:57:00.000Z",
	}), now, Options{})
	if err != nil {
		t.Fatalf("CheckWithOptions 失败: %v", err)
	}
	if want := "2026-09-04T11:55:00.000Z"; newBacklog.SafeCreatedBefore != want {
		t.Fatalf("积压较新时安全水位=%q want %q", newBacklog.SafeCreatedBefore, want)
	}
}

// Gate 路径仍只负责失败本轮（buffer 界不改变门控结果）。
func TestT936GatePathUnaffectedByBufferBound(t *testing.T) {
	gate := Gate(t936Probe(&DrainStatus{Ready: true}), fixedNow)
	if err := gate(context.Background()); err != nil {
		t.Fatalf("排干后门控应放行: %v", err)
	}
}
