package gatewayproxyhealth

// wt 覆盖波次：SpeedFirstRuntimeConfig 总时间两新字段的 JSON round-trip——
// 驼峰 wire 契约保真，存量状态缺省零值语义保持。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWtSpeedFirstRuntimeConfigTotalTimeRoundTrip(t *testing.T) {
	source := SpeedFirstRuntimeConfig{
		FirstByteDeadlineMs:           30_000,
		SlowTriggerCount:              3,
		SlowWindowSeconds:             120,
		RecoverySuccessCount:          3,
		ProbeIntervalSeconds:          30,
		DegradedTTLSeconds:            300,
		MaxFirstByteRetriesPerRequest: 2,
		TotalTimeDeadlineMs:           120_000,
		CompactionTotalTimeDeadlineMs: 300_000,
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"totalTimeDeadlineMs":120000`) ||
		!strings.Contains(string(encoded), `"compactionTotalTimeDeadlineMs":300000`) {
		t.Fatalf("encoded=%s", encoded)
	}
	var decoded SpeedFirstRuntimeConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != source {
		t.Fatalf("round-trip 保真失败: %+v != %+v", decoded, source)
	}

	// 存量状态（无两新字段）解码缺省零值，且不影响七字段存量校验。
	legacy := []byte(`{"firstByteDeadlineMs":30000,"slowTriggerCount":3,"slowWindowSeconds":120,` +
		`"recoverySuccessCount":3,"probeIntervalSeconds":30,"degradedTtlSeconds":300,"maxFirstByteRetriesPerRequest":2}`)
	var legacyDecoded SpeedFirstRuntimeConfig
	if err := json.Unmarshal(legacy, &legacyDecoded); err != nil {
		t.Fatal(err)
	}
	if legacyDecoded.TotalTimeDeadlineMs != 0 || legacyDecoded.CompactionTotalTimeDeadlineMs != 0 {
		t.Fatalf("存量零值=%+v", legacyDecoded)
	}
	if !isRouteStrategySpeedFirstConfig(legacyDecoded) {
		t.Fatal("存量状态必须仍通过七字段校验")
	}
}
