package gatewayruntimecache

import (
	"strings"
	"testing"
)

// R5 根因回归：偏好存储文档是 {firstByteDeadlineMs, speedFirstConfig}，
// 合成 NormalRoutingConfig 时必须把 firstByteDeadlineMs 放根、speedFirstConfig
// 放嵌套；整体塞进 speedFirstConfig 会让 normalize 忽略配置并回落 30000/3。
func TestSynthesizeSchedulingPreferenceKeepsSpeedParams(t *testing.T) {
	snapshot, err := synthesizeSchedulingPreferenceSnapshot("sp-r5", "speed_first",
		`{"firstByteDeadlineMs":10000,"speedFirstConfig":{"slowTriggerCount":2}}`)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if snapshot.normalized.FirstByteDeadlineMs == nil || *snapshot.normalized.FirstByteDeadlineMs != 10000 {
		t.Fatalf("firstByteDeadlineMs = %v, want 10000", snapshot.normalized.FirstByteDeadlineMs)
	}
	if snapshot.normalized.SpeedFirstConfig == nil || snapshot.normalized.SpeedFirstConfig.SlowTriggerCount != 2 {
		t.Fatalf("speedFirstConfig = %+v, want slowTriggerCount=2", snapshot.normalized.SpeedFirstConfig)
	}
	if snapshot.config == nil || snapshot.config.Raw == nil || !strings.Contains(string(snapshot.config.Raw), `"firstByteDeadlineMs":10000`) {
		t.Fatalf("raw config lost configured deadline: %s", snapshot.config.Raw)
	}
}

func TestSynthesizeSchedulingPreferenceDefaultsWhenConfigMissing(t *testing.T) {
	snapshot, err := synthesizeSchedulingPreferenceSnapshot("sp-default", "speed_first", "")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if snapshot.normalized.FirstByteDeadlineMs == nil || *snapshot.normalized.FirstByteDeadlineMs != 30000 {
		t.Fatalf("firstByteDeadlineMs = %v, want default 30000", snapshot.normalized.FirstByteDeadlineMs)
	}
	if snapshot.normalized.SpeedFirstConfig == nil || snapshot.normalized.SpeedFirstConfig.SlowTriggerCount != 3 {
		t.Fatalf("speedFirstConfig = %+v, want default slowTriggerCount=3", snapshot.normalized.SpeedFirstConfig)
	}
}
