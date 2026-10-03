package gatewaypreauth

// wt 覆盖波次：速度优先总时间兜底截止（设计 6.2/6.8）的 preauth 运行态解析
// ——压缩请求只豁免首字截止维度，总时间两阈值照常带出。

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWtNormalRouteSpeedFirstTotalTime(t *testing.T) {
	service := &Service{}
	lane := gatewayProtoLane(gatewayproto.LaneText)
	speedRow := func(raw string, preference string) *gatewayruntimecache.GatewayAPIKeyRow {
		return &gatewayruntimecache.GatewayAPIKeyRow{
			RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
			NormalRoutingConfig: &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
				SchedulingPreference: preference,
				Raw:                  json.RawMessage(raw),
			},
		}
	}
	fullRaw := `{"firstByteDeadlineMs": 2500, "speedFirstConfig": {"totalTimeDeadlineSeconds": 90, "compactionTotalTimeDeadlineSeconds": 600}}`

	t.Run("非压缩 speed_first 全量带出", func(t *testing.T) {
		config := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(fullRaw, "speed_first"), lane, false)
		if config == nil {
			t.Fatal("配置必须非 nil")
		}
		if config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 2500 {
			t.Fatalf("FirstByteDeadlineMs=%v", config.FirstByteDeadlineMs)
		}
		if config.TotalTimeDeadlineMs == nil || *config.TotalTimeDeadlineMs != 90_000 {
			t.Fatalf("TotalTimeDeadlineMs=%v", config.TotalTimeDeadlineMs)
		}
		if config.CompactionTotalTimeDeadlineMs == nil || *config.CompactionTotalTimeDeadlineMs != 600_000 {
			t.Fatalf("CompactionTotalTimeDeadlineMs=%v", config.CompactionTotalTimeDeadlineMs)
		}
		if config.Raw == nil || config.Raw["speedFirstConfig"] == nil {
			t.Fatalf("Raw 必须完整透传=%v", config.Raw)
		}
	})

	t.Run("压缩请求豁免首字保留总时间", func(t *testing.T) {
		config := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(fullRaw, "speed_first"), lane, true)
		if config == nil {
			t.Fatal("压缩请求配置必须照常带出")
		}
		if config.FirstByteDeadlineMs != nil {
			t.Fatalf("压缩请求首字截止必须为 nil=%v", config.FirstByteDeadlineMs)
		}
		if config.TotalTimeDeadlineMs == nil || *config.TotalTimeDeadlineMs != 90_000 {
			t.Fatalf("TotalTimeDeadlineMs=%v", config.TotalTimeDeadlineMs)
		}
		if config.CompactionTotalTimeDeadlineMs == nil || *config.CompactionTotalTimeDeadlineMs != 600_000 {
			t.Fatalf("CompactionTotalTimeDeadlineMs=%v", config.CompactionTotalTimeDeadlineMs)
		}
		if config.Raw == nil || config.Raw["firstByteDeadlineMs"] != float64(2500) {
			t.Fatalf("Raw 必须完整透传=%v", config.Raw)
		}
	})

	t.Run("总时间键缺省为 nil", func(t *testing.T) {
		config := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{"speedFirstConfig": {}}`, "speed_first"), lane, false)
		if config == nil || config.TotalTimeDeadlineMs != nil || config.CompactionTotalTimeDeadlineMs != nil {
			t.Fatalf("config=%+v", config)
		}
	})

	t.Run("非正数与非法值视为未设置", func(t *testing.T) {
		config := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(`{"speedFirstConfig": {"totalTimeDeadlineSeconds": 0, "compactionTotalTimeDeadlineSeconds": "bad"}}`, "speed_first"), lane, false)
		if config == nil || config.TotalTimeDeadlineMs != nil || config.CompactionTotalTimeDeadlineMs != nil {
			t.Fatalf("config=%+v", config)
		}
	})

	t.Run("非 speed_first 偏好返回 nil", func(t *testing.T) {
		if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(fullRaw, "cost_first"), lane, false); got != nil {
			t.Fatalf("cost_first=%v", got)
		}
	})

	t.Run("非 text lane 返回 nil", func(t *testing.T) {
		if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(fullRaw, "speed_first"), "image", false); got != nil {
			t.Fatalf("image lane=%v", got)
		}
		if got := service.normalRouteSpeedFirstConfigForAPIKey(speedRow(fullRaw, "speed_first"), "image", true); got != nil {
			t.Fatalf("image lane 压缩=%v", got)
		}
	})
}
