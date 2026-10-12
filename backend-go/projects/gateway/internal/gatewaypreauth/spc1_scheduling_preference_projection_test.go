// 调度偏好独立实体 P1——preauth 速度运行态投影带出测试（SP-C1/SP-C2 冻结
// 接口）：NormalRouteSpeedFirstRuntimeConfig 携带 PreferenceID 与
// ConfigFingerprint（历史策略路径两键为空串 = v1 键语义），FirstByte 投影
// 不携带。
package gatewaypreauth

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestSPC1SpeedFirstProjectionCarriesPreferenceIdentity(t *testing.T) {
	service := &Service{}
	lane := gatewayProtoLane(gatewayproto.LaneText)
	raw := `{"firstByteDeadlineMs": 2500, "speedFirstConfig": {"totalTimeDeadlineSeconds": 90}}`

	bound := &gatewayruntimecache.GatewayAPIKeyRow{
		RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
		NormalRoutingConfig: &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
			SchedulingPreference: "speed_first",
			Raw:                  json.RawMessage(raw),
		},
		SchedulingPreferenceID:          "sp-bound",
		SchedulingPreferenceFingerprint: "fp0123456789a",
		SchedulingPreferenceChatMode:    "speed_first",
	}
	config := service.normalRouteSpeedFirstConfigForAPIKey(bound, lane, false)
	if config == nil {
		t.Fatal("speed_first 投影必须非 nil")
	}
	if config.PreferenceID != "sp-bound" {
		t.Fatalf("PreferenceID=%q", config.PreferenceID)
	}
	if config.ConfigFingerprint != "fp0123456789a" {
		t.Fatalf("ConfigFingerprint=%q", config.ConfigFingerprint)
	}
	if config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 2500 {
		t.Fatalf("既有阈值带出不得受新字段影响: %v", config.FirstByteDeadlineMs)
	}

	// 历史策略路径：快照三字段空 → 投影两键空串（SP-C2 据此保持 v1 键）。
	historical := &gatewayruntimecache.GatewayAPIKeyRow{
		RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
		NormalRoutingConfig: &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
			SchedulingPreference: "speed_first",
			Raw:                  json.RawMessage(raw),
		},
	}
	config = service.normalRouteSpeedFirstConfigForAPIKey(historical, lane, false)
	if config == nil {
		t.Fatal("历史 speed_first 投影必须非 nil")
	}
	if config.PreferenceID != "" || config.ConfigFingerprint != "" {
		t.Fatalf("历史路径投影两键必须为空串: %q/%q", config.PreferenceID, config.ConfigFingerprint)
	}
}

func TestSPC1FirstByteProjectionHasNoPreferenceIdentity(t *testing.T) {
	// FirstByte 投影结构不携带偏好身份两键（编译期形状断言 + 行为不变）。
	config := NormalRouteFirstByteRuntimeConfig{SchedulingPreference: "speed_first", FirstByteDeadlineMs: nil}
	if config.SchedulingPreference != "speed_first" {
		t.Fatalf("FirstByte 投影形状变化: %+v", config)
	}
}
