package gatewayoauthcodex

import (
	"testing"
)

// 自 gatewaydispatch deadlinebodyclient_test.go 随被测对象迁入（REFACTOR-0006
// 阶段 A）：registry 白盒用例需同包访问 gatewayCodexSanitizedBodies。

func intToStringTest(value int) string {
	digits := ""
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func TestMarkCodexHistorySanitizedCapacityEviction(t *testing.T) {
	gatewaySerializedFlagsMu.Lock()
	previous := gatewayCodexSanitizedBodies
	// 构造满容量注册表（白盒：验证逐出分支不 panic 且仍写入新键）。
	gatewayCodexSanitizedBodies = make(map[string]struct{}, gatewaySerializedFlagCapacity+1)
	for i := 0; i < gatewaySerializedFlagCapacity; i++ {
		gatewayCodexSanitizedBodies["old-"+intToStringTest(i)] = struct{}{}
	}
	gatewaySerializedFlagsMu.Unlock()
	t.Cleanup(func() {
		gatewaySerializedFlagsMu.Lock()
		gatewayCodexSanitizedBodies = previous
		gatewaySerializedFlagsMu.Unlock()
	})
	MarkGatewayCodexHistorySanitized([]byte("fresh"))
	gatewaySerializedFlagsMu.Lock()
	defer gatewaySerializedFlagsMu.Unlock()
	if len(gatewayCodexSanitizedBodies) > gatewaySerializedFlagCapacity {
		t.Fatalf("容量应受限, got %d", len(gatewayCodexSanitizedBodies))
	}
	if _, ok := gatewayCodexSanitizedBodies["fresh"]; !ok {
		t.Fatal("新键必须写入")
	}
}
