package accounthealth

import (
	"context"
	"errors"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"strings"
	"testing"
	"time"
)

func TestNullableHelpers(t *testing.T) {
	if nullableStatus(0) != nil || nullableStatus(3) != 3 {
		t.Fatal("nullableStatus 分支错误")
	}
	if nullableText("  ") != nil || nullableText("x") != "x" {
		t.Fatal("nullableText 分支错误")
	}
	if nullableTime(nil) != nil || nullableTime(&time.Time{}) != nil {
		t.Fatal("零值时间必须写 NULL")
	}
	stamp := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if value, ok := nullableTime(&stamp).(time.Time); !ok || value != stamp {
		t.Fatalf("有效时间必须转 UTC: %v", nullableTime(&stamp))
	}
	if nullableTimeText(nil) != nil {
		t.Fatal("nil 时间文本必须为 nil")
	}
	if text, ok := nullableTimeText(&stamp).(string); !ok || text != "2026-09-10T08:00:00Z" {
		t.Fatalf("时间文本格式错误: %v", nullableTimeText(&stamp))
	}
}

// TestOutcomeCooldownFenceSources 覆盖 cooldown fence 三级来源与 observation。
func TestOutcomeCooldownFenceSources(t *testing.T) {
	observed := time.Date(2026, 9, 10, 7, 0, 0, 0, time.UTC)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: observed, Generation: "gen-1"}
	if outcomeCooldownFence(Outcome{}) != nil {
		t.Fatal("无 fence 必须为 nil")
	}
	direct := Outcome{CooldownFence: fence}
	if outcomeCooldownFence(direct) != fence {
		t.Fatal("outcome 级 fence 优先")
	}
	fromProjection := Outcome{Projection: &Projection{CooldownFence: fence}}
	if outcomeCooldownFence(fromProjection) != fence {
		t.Fatal("projection 级 fence 必须命中")
	}
	fromExpected := Outcome{Projection: &Projection{ExpectedCooldownFence: fence}}
	if outcomeCooldownFence(fromExpected) != fence {
		t.Fatal("ExpectedCooldownFence 必须兜底")
	}
	// observation：缺 fence / 零值时间 / 缺 generation → nil。
	if cooldownObservation(Outcome{}) != nil {
		t.Fatal("缺 fence observation 必须为 nil")
	}
	if cooldownObservation(Outcome{CooldownFence: &exactkeyprobe.CooldownFence{Generation: "g"}}) != nil {
		t.Fatal("零值 observation 必须为 nil")
	}
	if cooldownObservation(Outcome{CooldownFence: &exactkeyprobe.CooldownFence{ObservationStartedAt: observed}}) != nil {
		t.Fatal("缺 generation observation 必须为 nil")
	}
	if value, ok := cooldownObservation(direct).(time.Time); !ok || !value.Equal(observed.UTC()) {
		t.Fatalf("完整 fence 必须返回 observation: %v", cooldownObservation(direct))
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestSchedulerHelpersUnit 覆盖调度器小助手（min/wait/maxPause/Recovery）。
func TestSchedulerHelpersUnit(t *testing.T) {
	if minDuration(3*time.Second, 5*time.Second) != 3*time.Second || minDuration(9*time.Second, 5*time.Second) != 5*time.Second {
		t.Fatal("minDuration 分支错误")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须透传: %v", err)
	}
	if err := waitContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("正常等待必须返回 nil: %v", err)
	}
	if cooldownMaxPause(exactkeyprobe.Schedule{}) != defaultCooldownMaxPauseMinutes*time.Minute {
		t.Fatal("MaxPause 缺省必须回落 2 分钟")
	}
	if cooldownMaxPause(exactkeyprobe.Schedule{MaxPauseMinutes: 30}) != 30*time.Minute {
		t.Fatal("显式 MaxPause 必须生效")
	}
	if cooldownMaxRecovery(exactkeyprobe.Schedule{}) != defaultCooldownMaxRecoveryHours*time.Hour {
		t.Fatal("MaxRecovery 缺省必须回落 12 小时")
	}
	if cooldownMaxRecovery(exactkeyprobe.Schedule{MaxRecoveryHours: 6}) != 6*time.Hour {
		t.Fatal("显式 MaxRecovery 必须生效")
	}
	// passiveDelayBefore 边界：deadline<=1ms、偏移裁剪。
	if passiveDelayBefore(0) != time.Millisecond {
		t.Fatal("零 deadline 必须裁剪到 1ms")
	}
	if delay := passiveDelayBefore(time.Second); delay <= 0 || delay >= time.Second {
		t.Fatalf("正常 deadline 必须留出提前量: %v", delay)
	}
}

func TestValidateScheduledInputMatrix(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	valid := exactkeyprobe.Input{
		AccountID: "a", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Schedule:    exactkeyprobe.Schedule{HealthIntervalMS: 300_000, HealthJitterMS: 1000, FailureThreshold: 3, FailureRetryMS: 10_000, CooldownNeutralBaseMS: 1000, CooldownNeutralMaxMS: 2000, CooldownFailureBackoffMS: 3000, MaxPauseMinutes: 2, MaxRecoveryHours: 12},
		Eligibility: exactkeyprobe.Eligibility{BoundGroup: true, AuthorizationEligible: true, AccountStatus: "active"},
	}
	if err := validateScheduledInput(valid, now); err != nil {
		t.Fatalf("合法输入必须通过: %v", err)
	}
	invalid := []struct {
		name   string
		mutate func(input *exactkeyprobe.Input)
	}{
		{"缺账户", func(i *exactkeyprobe.Input) { i.AccountID = " " }},
		{"缺 InputVersion", func(i *exactkeyprobe.Input) { i.InputVersion = 0 }},
		{"过期", func(i *exactkeyprobe.Input) { i.ExpiresAt = now.Add(-time.Minute) }},
		{"间隔过短", func(i *exactkeyprobe.Input) { i.Schedule.HealthIntervalMS = 1000 }},
		{"抖动超限", func(i *exactkeyprobe.Input) { i.Schedule.HealthJitterMS = maxScheduleMilliseconds + 1 }},
		{"抖动大于间隔", func(i *exactkeyprobe.Input) { i.Schedule.HealthJitterMS = i.Schedule.HealthIntervalMS + 1 }},
		{"失败阈值 0", func(i *exactkeyprobe.Input) { i.Schedule.FailureThreshold = 0 }},
		{"失败重试过短", func(i *exactkeyprobe.Input) { i.Schedule.FailureRetryMS = 100 }},
		{"暂停上限越界", func(i *exactkeyprobe.Input) { i.Schedule.MaxPauseMinutes = 1441 }},
		{"恢复上限越界", func(i *exactkeyprobe.Input) { i.Schedule.MaxRecoveryHours = 24*30 + 1 }},
		{"缺绑定", func(i *exactkeyprobe.Input) { i.Eligibility.BoundGroup = false }},
		{"缺授权", func(i *exactkeyprobe.Input) { i.Eligibility.AuthorizationEligible = false }},
		{"状态不可调度", func(i *exactkeyprobe.Input) { i.Eligibility.AccountStatus = "disabled" }},
		{"冷却缺 cooldown_until", func(i *exactkeyprobe.Input) {
			i.Eligibility.AccountStatus = "temporary_unavailable"
		}},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			mutated := valid
			item.mutate(&mutated)
			if err := validateScheduledInput(mutated, now); err == nil {
				t.Fatal("非法输入必须报错")
			}
		})
	}
}

