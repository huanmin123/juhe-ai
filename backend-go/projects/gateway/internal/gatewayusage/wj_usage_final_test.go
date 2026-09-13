package gatewayusage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWJSanitizeURLForLog 固定日志 URL 脱敏契约：只有 oauth 授权/设备路径
// 被重写，敏感 query 名替换为 [redacted]。
func TestWJSanitizeURLForLog(t *testing.T) {
	if got := SanitizeURLForLog("/v1/chat/completions?api_key=secret"); got != "/v1/chat/completions?api_key=secret" {
		t.Fatalf("普通路径必须原样: %q", got)
	}
	sanitized := SanitizeURLForLog("/oauth/authorize?client_id=abc&code_challenge=secret&state=xyz")
	if !strings.HasPrefix(sanitized, "/oauth/authorize?") {
		t.Fatalf("oauth 路径必须保留: %q", sanitized)
	}
	if strings.Contains(sanitized, "secret") || strings.Contains(sanitized, "xyz") {
		t.Fatalf("敏感 query 必须脱敏: %q", sanitized)
	}
	if !strings.Contains(sanitized, "client_id=abc") {
		t.Fatalf("非敏感 query 必须保留: %q", sanitized)
	}
	device := SanitizeURLForLog("/oauth/device?user_code=top-secret")
	if strings.Contains(device, "top-secret") || !strings.HasPrefix(device, "/oauth/device?") {
		t.Fatalf("device 路径脱敏不符: %q", device)
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

// TestWJSpoolCapacityCacheRefreshAfterExternalDelete 固定容量缓存的强制
// 刷新路径：外部删除 spool 文件后重写必须重扫并成功。
func TestWJSpoolCapacityCacheRefreshAfterExternalDelete(t *testing.T) {
	directory := t.TempDir()
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:        directory,
		InstanceID:       "inst-refresh",
		MaxItems:         1,
		MaxBytes:         1 << 20,
		ReplayBatchSize:  8,
		ReplayIntervalMs: 5,
		Enabled:          true,
	}, fixedClock{ms: 1700000000000}, nil)
	first := UsageRecordInput{ID: "usage_refresh_1", TraceID: "refresh-1", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}
	if err := spool.Persist(context.Background(), first); err != nil {
		t.Fatalf("首次持久化: %v", err)
	}
	// 外部删除文件（模拟已被回放清理）。
	matches, err := filepath.Glob(filepath.Join(directory, "inst-refresh", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("预期 1 个 spool 文件: %v %v", matches, err)
	}
	if err := os.Remove(matches[0]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// 固定时钟使容量缓存恒过期 → 持久化前强制重扫，删除后容量归零 → 写入成功。
	if err := spool.Persist(context.Background(), UsageRecordInput{ID: "usage_refresh_2", TraceID: "refresh-2", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}); err != nil {
		t.Fatalf("刷新后持久化: %v", err)
	}
}

// TestWJSpoolReplayLoopErrorBackoff 固定回放循环的错误退避与退出。
func TestWJSpoolReplayLoopErrorBackoff(t *testing.T) {
	spool, directory := newTestSpool(t, true)
	// 植入一个损坏文件：回放必然失败 → 循环进入退避分支。
	corruptDir := filepath.Join(directory, "inst-1")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "bad.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	spool.StartReplay(&wjNoopReplay{})
	spool.StartReplay(&wjNoopReplay{}) // 二次启动必须幂等
	deadline := 0
	for spool.Runtime().ReplayFailureCount == 0 && deadline < 200 {
		time.Sleep(5 * time.Millisecond)
		deadline++
	}
	if spool.Runtime().ReplayFailureCount == 0 {
		t.Fatal("损坏文件必须触发回放失败记账")
	}
	spool.StopReplay()
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
