package gatewayusage

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWJSanitizeURLForLog 固定日志 URL 脱敏契约：凭据类 query 值掩码
// （2026-09-25 Go 侧加固），其余 query 与顺序字节原样保留；oauth 路径不再
// 特判，与普通路径遵循同一凭据掩码规则。
func TestWJSanitizeURLForLog(t *testing.T) {
	if got := SanitizeURLForLog("/v1/chat/completions?api_key=secret"); got != "/v1/chat/completions?api_key=[redacted]" {
		t.Fatalf("凭据类 query 必须掩码: %q", got)
	}
	if got := SanitizeURLForLog("/v1/chat/completions?a=1&b=2"); got != "/v1/chat/completions?a=1&b=2" {
		t.Fatalf("无凭据普通路径必须原样: %q", got)
	}
	// oauth 路径按通用凭据掩码语义处理：凭据名掩码、非凭据参数字节原样。
	if got := SanitizeURLForLog("/oauth/authorize?key=sk-xxx&client_id=abc"); got != "/oauth/authorize?key=[redacted]&client_id=abc" {
		t.Fatalf("oauth 路径须遵循通用凭据掩码语义: %q", got)
	}
	if got := SanitizeURLForLog("/oauth/device?user_code=top-secret"); got != "/oauth/device?user_code=top-secret" {
		t.Fatalf("oauth 路径非凭据参数必须原样: %q", got)
	}
}

// TestWJBuildGatewayLogErrorMessage 固定错误消息的持久化预算契约。
func TestWJBuildGatewayLogErrorMessage(t *testing.T) {
	if got := BuildGatewayLogErrorMessage(""); got.ErrorMessageBytes != 0 || got.ErrorMessageTruncated {
		t.Fatalf("空串 = %+v", got)
	}
	short := BuildGatewayLogErrorMessage("boom")
	if short.ErrorMessage != "boom" || short.ErrorMessageBytes != 4 || short.ErrorMessageTruncated {
		t.Fatalf("短消息 = %+v", short)
	}
	long := strings.Repeat("x", 8192)
	truncated := BuildGatewayLogErrorMessage(long)
	if !truncated.ErrorMessageTruncated {
		t.Fatal("超长必须标记截断")
	}
	if len(truncated.ErrorMessage) > gatewayLogErrorMessageMaxBytes {
		t.Fatalf("截断后超预算: %d", len(truncated.ErrorMessage))
	}
	// 截断后缀报告剩余字节数（总长 - 前缀），总长记录在 ErrorMessageBytes。
	if !strings.Contains(truncated.ErrorMessage, "...[truncated ") || !strings.HasSuffix(truncated.ErrorMessage, " bytes]") {
		t.Fatalf("截断后缀缺失: %q", truncated.ErrorMessage[len(truncated.ErrorMessage)-60:])
	}
	if truncated.ErrorMessageBytes != 8192 {
		t.Fatalf("原始总长不符: %+v", truncated)
	}
}

// wjDeepStruct 链用于构造结构体快照的深度/字节截断。
type wjDeepStruct struct {
	Name string        `json:"name"`
	Next *wjDeepStruct `json:"next"`
}

// TestWJBoundSnapshotStructTruncation 固定结构体快照的深度与字节截断。
func TestWJBoundSnapshotStructTruncation(t *testing.T) {
	// 12 层嵌套结构体超过 snapshotMaxDepth → 深度截断。
	root := &wjDeepStruct{Name: "n0"}
	cursor := root
	for i := 1; i < 12; i++ {
		cursor.Next = &wjDeepStruct{Name: "n" + itoa(i)}
		cursor = cursor.Next
	}
	bounded := BoundUsageRecordSnapshot(root)
	if !strings.Contains(serializeWJ(bounded), "depth_truncated") {
		t.Fatalf("深度截断缺失: %.200s", serializeWJ(bounded))
	}
	// 大字符串字段触发字节预算截断。
	huge := &wjDeepStruct{Name: strings.Repeat("y", 200000)}
	hugeBounded := BoundUsageRecordSnapshot(huge)
	if !strings.Contains(serializeWJ(hugeBounded), "_truncated") {
		t.Fatalf("字节截断缺失: %.200s", serializeWJ(hugeBounded))
	}
}

func serializeWJ(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// TestWJServiceProbeAwareLogging 固定探针流量的降级日志：probe 来源记
// Debug，普通来源记 Warn，nil logger 安全返回。
func TestWJServiceProbeAwareLogging(t *testing.T) {
	logger := &wjRecordingLogger{}
	service := &Service{logger: logger}
	fields := orderedLogFields()
	fields.Set("event", "test")
	// probe 流量 → Debug 级别。
	service.logWarnProbe(TrafficSourceRuntimeRecoveryProbe, fields, "probe warn")
	if logger.count() != 1 {
		t.Fatalf("probe warn 必须记录: %d", logger.count())
	}
	// nil logger 安全。
	silent := &Service{}
	silent.logWarnProbe(TrafficSourceGateway, fields, "ignored")
	logGatewayAttemptFailure(silent, GatewayUsageContext{TrafficSource: TrafficSourceGateway}, fields, "ignored")
	// 普通来源 → Warn 级别；probe 来源 → Debug 级别。
	logGatewayAttemptFailure(service, GatewayUsageContext{TrafficSource: TrafficSourceGateway}, fields, "normal warn")
	logGatewayAttemptFailure(service, GatewayUsageContext{TrafficSource: TrafficSourceCooldownRetest}, fields, "probe debug")
	if logger.count() != 3 {
		t.Fatalf("日志记录数不符: %d", logger.count())
	}
}
