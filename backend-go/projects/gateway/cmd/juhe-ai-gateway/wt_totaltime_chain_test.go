package main

// wt 覆盖波次：chainSpeedFirstRuntimeConfigOf 的总时间维度判活（设计 6.8）——
// 压缩形态（FirstByteDeadlineMs=nil + 总时间有值）不得整体熄灭，九字段完整
// 带出；全空仍返回 nil。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func wtInt64Ptr(value int64) *int64 { return &value }

func TestWtChainSpeedFirstTotalTimeConfig(t *testing.T) {
	t.Run("压缩形态返回九字段配置", func(t *testing.T) {
		typed := chainSpeedFirstRuntimeConfigOf(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			SchedulingPreference:          "speed_first",
			TotalTimeDeadlineMs:           wtInt64Ptr(90_000),
			CompactionTotalTimeDeadlineMs: wtInt64Ptr(600_000),
			Raw:                           map[string]any{},
		})
		if typed == nil {
			t.Fatal("压缩形态配置必须非 nil")
		}
		if typed.FirstByteDeadlineMs != 30_000 {
			t.Fatalf("首字缺省=%d", typed.FirstByteDeadlineMs)
		}
		if typed.TotalTimeDeadlineMs != 90_000 || typed.CompactionTotalTimeDeadlineMs != 600_000 {
			t.Fatalf("总时间=%d/%d", typed.TotalTimeDeadlineMs, typed.CompactionTotalTimeDeadlineMs)
		}
		if typed.SlowTriggerCount != 3 || typed.SlowWindowSeconds != 120 || typed.RecoverySuccessCount != 3 ||
			typed.ProbeIntervalSeconds != 30 || typed.DegradedTTLSeconds != 300 || typed.MaxFirstByteRetriesPerRequest != 2 {
			t.Fatalf("六旋钮默认=%+v", typed)
		}
	})

	t.Run("普通形态九字段全量", func(t *testing.T) {
		typed := chainSpeedFirstRuntimeConfigOf(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			SchedulingPreference:          "speed_first",
			FirstByteDeadlineMs:           wtInt64Ptr(8_000),
			TotalTimeDeadlineMs:           wtInt64Ptr(150_000),
			CompactionTotalTimeDeadlineMs: wtInt64Ptr(450_000),
			Raw: map[string]any{"speedFirstConfig": map[string]any{
				"slowTriggerCount":              4,
				"slowWindowSeconds":             90,
				"recoverySuccessCount":          5,
				"probeIntervalSeconds":          20,
				"degradedTtlSeconds":            600,
				"maxFirstByteRetriesPerRequest": 3,
			}},
		})
		if typed == nil {
			t.Fatal("普通形态配置必须非 nil")
		}
		if typed.FirstByteDeadlineMs != 8_000 || typed.TotalTimeDeadlineMs != 150_000 ||
			typed.CompactionTotalTimeDeadlineMs != 450_000 {
			t.Fatalf("三截止=%+v", typed)
		}
		if typed.SlowTriggerCount != 4 || typed.SlowWindowSeconds != 90 || typed.RecoverySuccessCount != 5 ||
			typed.ProbeIntervalSeconds != 20 || typed.DegradedTTLSeconds != 600 || typed.MaxFirstByteRetriesPerRequest != 3 {
			t.Fatalf("六旋钮=%+v", typed)
		}
	})

	t.Run("总时间两阈值只认 preauth typed 字段", func(t *testing.T) {
		// 新键解码在 preauth 完成；chain 只消费 typed 字段，Raw 携带的新键不参与。
		typed := chainSpeedFirstRuntimeConfigOf(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			SchedulingPreference: "speed_first",
			FirstByteDeadlineMs:  wtInt64Ptr(8_000),
			Raw: map[string]any{"speedFirstConfig": map[string]any{
				"totalTimeDeadlineSeconds":           90,
				"compactionTotalTimeDeadlineSeconds": 600,
			}},
		})
		if typed == nil || typed.TotalTimeDeadlineMs != 120_000 || typed.CompactionTotalTimeDeadlineMs != 300_000 {
			t.Fatalf("typed=%+v", typed)
		}
	})

	t.Run("首字与总时间全空返回 nil", func(t *testing.T) {
		empty := &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{SchedulingPreference: "speed_first", Raw: map[string]any{}}
		if chainSpeedFirstRuntimeConfigOf(empty) != nil {
			t.Fatal("全空必须返回 nil")
		}
		if chainSpeedFirstRuntimeConfigOf(nil) != nil {
			t.Fatal("nil config 必须返回 nil")
		}
	})

	t.Run("非正数总时间阈值视为未设置", func(t *testing.T) {
		typed := chainSpeedFirstRuntimeConfigOf(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			SchedulingPreference:          "speed_first",
			FirstByteDeadlineMs:           wtInt64Ptr(8_000),
			TotalTimeDeadlineMs:           wtInt64Ptr(0),
			CompactionTotalTimeDeadlineMs: wtInt64Ptr(-1),
			Raw:                           map[string]any{},
		})
		if typed == nil || typed.TotalTimeDeadlineMs != 120_000 || typed.CompactionTotalTimeDeadlineMs != 300_000 {
			t.Fatalf("typed=%+v", typed)
		}
	})
}
