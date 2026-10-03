package routestrategies

// SpeedFirstRuntimeItem.Dimension 的 wire 契约（设计 6.4）：speed-first-runtime
// 端点的降级行携带 dimension 字段（first_byte | total_time），投影源是
// gatewayproxyhealth.DegradedRuntimeItem.Dimension（存量状态兼容读作
// first_byte）。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpeedFirstRuntimeItemDimensionWireContract(t *testing.T) {
	item := SpeedFirstRuntimeItem{
		AccountID: "acc-1",
		SlowCount: 2,
		Dimension: "total_time",
		Reason:    "普通路由速度优先总时间等待超时",
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"dimension":"total_time"`) {
		t.Fatalf("dimension 字段缺失 wire 契约: %s", encoded)
	}
	var decoded SpeedFirstRuntimeItem
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Dimension != "total_time" {
		t.Fatalf("dimension = %q", decoded.Dimension)
	}
	// 存量形态（无 dimension）解码为空串，兼容读由消费方归一为 first_byte。
	legacy := `{"accountId":"acc-1","slowCount":1}`
	var legacyItem SpeedFirstRuntimeItem
	if err := json.Unmarshal([]byte(legacy), &legacyItem); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	if legacyItem.Dimension != "" {
		t.Fatalf("legacy dimension = %q", legacyItem.Dimension)
	}
}
