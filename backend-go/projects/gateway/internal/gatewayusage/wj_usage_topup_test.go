package gatewayusage

import (
	"encoding/json"
	"errors"
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
	// maxInt64 的直调契约（直方图桶渲染已交由 client_golang，排序直调面消失）。
	if maxInt64(1, 2) != 2 || maxInt64(5, 2) != 5 {
		t.Fatal("maxInt64 不符")
	}
}
