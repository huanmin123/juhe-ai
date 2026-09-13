package gatewayusage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWJCaptureDiagnosticIdempotency 固定诊断标记与 HTTP 完成的幂等语义：
// 重复标记/重复完成事件不得重复产生副作用。
func TestWJCaptureDiagnosticIdempotency(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 0)}
	})
	defer capture.Cancel()
	capture.MarkServerDiagnosticTimeout()
	capture.MarkServerDiagnosticTimeout() // 幂等
	capture.MarkServerDiagnosticCancellation()
	capture.MarkServerDiagnosticCancellation() // 幂等
	// 仅标记诊断不收尾，不得投递审计。
	if got := len(dispatcher.all()); got != 0 {
		t.Fatalf("未收尾不得投递: %d", got)
	}

	observer := &wjMockHTTPCompletion{}
	withObserver := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.HTTPCompletion = observer
	})
	defer withObserver.Cancel()
	statusOK := 200
	withObserver.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: &statusOK})
	observer.fire(1700000000400)
	observer.fire(1700000000900) // 第二次完成事件必须短路
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("收尾后必须恰好投递一次: %d", len(logs))
	}
}

// TestWJSpoolCapacityRefreshPath 固定容量缓存的强制刷新成功路径：缓存显示
// 超限但重扫后容量释放，继续写入成功。
func TestWJSpoolCapacityRefreshPath(t *testing.T) {
	directory := t.TempDir()
	// 使用真实时钟：30 秒容量缓存在测试窗口内不会过期，第一次 Persist 后
	// 缓存记录 items=1；外部删除文件后缓存仍显示已满，触发强制重扫。
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:        directory,
		InstanceID:       "inst-refresh2",
		MaxItems:         1,
		MaxBytes:         1 << 20,
		ReplayBatchSize:  8,
		ReplayIntervalMs: 5,
		Enabled:          true,
	}, nil, nil)
	first := UsageRecordInput{ID: "usage_refresh2_1", TraceID: "refresh2-1", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}
	if err := spool.Persist(context.Background(), first); err != nil {
		t.Fatalf("首次持久化: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "inst-refresh2", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("预期 1 个 spool 文件: %v %v", matches, err)
	}
	if err := os.Remove(matches[0]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// 缓存内 items=1 已满 → 强制重扫发现 0 文件 → 容量释放 → 写入成功。
	if err := spool.Persist(context.Background(), UsageRecordInput{ID: "usage_refresh2_2", TraceID: "refresh2-2", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}); err != nil {
		t.Fatalf("容量刷新后持久化: %v", err)
	}
}

// TestWJSummaryShrinkRebuildsTextPreview 固定文本负载摘要收缩后 textPreview
// 的重建。
func TestWJSummaryShrinkRebuildsTextPreview(t *testing.T) {
	original := strings.Repeat("A", 1000)
	summary := buildAuditPayloadSummary(auditPayloadSummaryBuildInput{
		body:                    []byte(original),
		originalSha256:          "hash",
		originalBodySizeBytes:   len(original),
		originalContentType:     "application/json",
		originalContentEncoding: "",
		fullBodyLimitBytes:      800,
		reason:                  SummaryReasonBodyExceededLimit,
	})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["textPreview"]; !ok {
		t.Fatal("文本负载摘要必须带 textPreview")
	}
	shrinkExistingPayloadSummary(decoded, 100)
	preview, ok := decoded["textPreview"].(map[string]any)
	if !ok {
		t.Fatalf("收缩后 textPreview 必须是 map: %T", decoded["textPreview"])
	}
	head, headOK := preview["head"].(string)
	tail, tailOK := preview["tail"].(string)
	if !headOK || !tailOK {
		t.Fatalf("textPreview 形态不符: %+v", preview)
	}
	// 收缩后窗口只剩 50 字节，预览不得包含越界内容。
	if len(head) > 50 || len(tail) > 50 {
		t.Fatalf("textPreview 超窗: head=%d tail=%d", len(head), len(tail))
	}
}

// TestWJFinalizationClockAndRecovery 固定收尾队列的时钟注入与 panic 归一化。
func TestWJFinalizationClockAndRecovery(t *testing.T) {
	queue := NewGatewayUsageFinalizationQueue(2, 1).WithClock(SystemClock{})
	queue.logRecovery(errors.New("boom"), "event")
	queue.logRecovery("plain string", "event")
	queue.logError(errors.New("direct"), "event")
	// nil logger 不panic。
	nilLogger := NewGatewayUsageFinalizationQueue(2, 1)
	nilLogger.logError(errors.New("swallowed"), "event")
	// histogram 排序与 maxInt64 的直调契约。
	entries := sortedHistogramEntries(map[string]*prometheusHistogram{
		"b": {}, "a": {},
	})
	if len(entries) != 2 || entries[0].key > entries[1].key {
		t.Fatalf("直方图排序不符: %+v", entries)
	}
	if maxInt64(1, 2) != 2 || maxInt64(5, 2) != 5 {
		t.Fatal("maxInt64 不符")
	}
	// maxInt 的 b>a 分支。
	if maxInt(1, 2) != 2 || maxInt(3, 1) != 3 {
		t.Fatal("maxInt 不符")
	}
}
