package gatewaypreauth

// 压缩期望冻结（用量记录压缩标记设计 §压缩请求识别）：BuildGatewayUsageContext
// 把调用方冻结的单点判定值（gatewaycodex.CodexCompactionExpectedForRequest）
// 带进请求级失败用量上下文，缺省 false。

import "testing"

func TestBuildGatewayUsageContextCompactionExpected(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	frozen := service.BuildGatewayUsageContext(usageContextInput{
		traceID: "t-compaction", clientIP: "1.2.3.4", trafficSource: TrafficSourceGateway,
		identity:           OpenAIGatewayRequestIdentity{SystemAccountID: "sys", APIKeyID: "key", GroupID: "g"},
		endpoint:           "POST /v1/responses/compact",
		compactionExpected: true,
	})
	if !frozen.CompactionExpected {
		t.Fatalf("CompactionExpected = false, want true（冻结值必须进上下文）: %+v", frozen)
	}
	ordinary := service.BuildGatewayUsageContext(usageContextInput{
		traceID: "t-ordinary", clientIP: "1.2.3.4", trafficSource: TrafficSourceGateway,
		identity: OpenAIGatewayRequestIdentity{SystemAccountID: "sys", APIKeyID: "key", GroupID: "g"},
		endpoint: "POST /v1/chat/completions",
	})
	if ordinary.CompactionExpected {
		t.Fatalf("普通请求 CompactionExpected = true, want false（零值缺省）: %+v", ordinary)
	}
}
