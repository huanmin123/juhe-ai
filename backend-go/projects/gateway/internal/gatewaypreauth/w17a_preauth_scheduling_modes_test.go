package gatewaypreauth

// w17a 覆盖波次：调度偏好（speed_first）在四种调度模式下通用化后的
// preflight helper 行为。normalRoutingConfig 键是 normal/weighted/failover/
// round_robin 共享的组内调度配置（历史命名）；hybrid_smart 行运行时不解码
// 该键，NormalRoutingConfig 恒 nil，helper 依赖 nil/偏好判断兜底返回 nil。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW17ASchedulingPreferenceModes(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	raw := []byte(`{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000}`)

	// 四种调度模式（含 normal 基线）：双 helper 都返回有效配置且 deadline 正确解码。
	for _, mode := range []string{
		gatewayruntimecache.RouteStrategyModeNormal,
		gatewayruntimecache.RouteStrategyModeWeighted,
		gatewayruntimecache.RouteStrategyModeFailover,
		gatewayruntimecache.RouteStrategyModeRoundRobin,
	} {
		row := validRuntimeRow()
		row.RouteStrategyMode = mode
		row.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
			SchedulingPreference: "speed_first", Raw: raw,
		}

		speedFirst := service.normalRouteSpeedFirstConfigForAPIKey(row, gatewayproto.LaneText, false)
		if speedFirst == nil || speedFirst.SchedulingPreference != "speed_first" ||
			speedFirst.FirstByteDeadlineMs == nil || *speedFirst.FirstByteDeadlineMs != 30000 || speedFirst.Raw == nil {
			t.Fatalf("%s 速度优先配置 = %+v", mode, speedFirst)
		}
		firstByte := normalRouteFirstByteConfigForAPIKeyRecord(row)
		if firstByte == nil || firstByte.SchedulingPreference != "speed_first" ||
			firstByte.FirstByteDeadlineMs == nil || *firstByte.FirstByteDeadlineMs != 30000 {
			t.Fatalf("%s 首字截止配置 = %+v", mode, firstByte)
		}
	}

	// hybrid_smart 行：NormalRoutingConfig 为 nil（防解码门回归）→ 双 helper 返回 nil。
	hybrid := validRuntimeRow()
	hybrid.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeHybridSmart
	hybrid.NormalRoutingConfig = nil
	if got := service.normalRouteSpeedFirstConfigForAPIKey(hybrid, gatewayproto.LaneText, false); got != nil {
		t.Fatalf("hybrid_smart 速度优先配置 = %+v", got)
	}
	if got := normalRouteFirstByteConfigForAPIKeyRecord(hybrid); got != nil {
		t.Fatalf("hybrid_smart 首字截止配置 = %+v", got)
	}

	// cost_first：偏好不匹配 → 双 helper 返回 nil。
	costRow := validRuntimeRow()
	costRow.NormalRoutingConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{
		SchedulingPreference: "cost_first", Raw: []byte(`{"schedulingPreference":"cost_first"}`),
	}
	if got := service.normalRouteSpeedFirstConfigForAPIKey(costRow, gatewayproto.LaneText, false); got != nil {
		t.Fatalf("cost_first 速度优先配置 = %+v", got)
	}
	if got := normalRouteFirstByteConfigForAPIKeyRecord(costRow); got != nil {
		t.Fatalf("cost_first 首字截止配置 = %+v", got)
	}
}
