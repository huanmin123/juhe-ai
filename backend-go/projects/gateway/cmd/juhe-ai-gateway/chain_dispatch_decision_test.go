package main

// W1b：gateway_dispatch_decision 决策日志观察者接线测试——单次事件输出
// 恰一条 info JSON 行，skippedTop 截断保护生效，字段形状与引擎摘要一致。

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func w1bSkippedEntries(count int) []gatewaydispatch.DispatchDecisionSkip {
	entries := make([]gatewaydispatch.DispatchDecisionSkip, 0, count)
	for index := 0; index < count; index++ {
		entries = append(entries, gatewaydispatch.DispatchDecisionSkip{
			AccountID: "s-" + strconv.Itoa(index),
			Reason:    gatewaydispatch.DispatchSkipReasonModelUnsupported,
		})
	}
	return entries
}

func TestW1bChainDispatchDecisionObserverEmitsSingleInfoLine(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := newChainDispatchDecisionObserver(logger)
	summary := gatewaydispatch.DispatchDecisionSummary{
		CandidateTotal:     30,
		EligibleCount:      2,
		SelectedAccountID:  "a-1",
		ModelRankAvailable: true,
		Skipped:            w1bSkippedEntries(25),
		SkippedTruncated:   true,
		Busy:               []string{"a-2"},
		Suppressed:         []string{"x-1"},
	}
	observer(gatewaydispatch.DispatchDecisionEvent{
		TraceID:       "trace-test",
		GroupID:       "group-1",
		APIKeyID:      "apikey-1",
		TrafficSource: "gateway",
		DurationMs:    12,
		Summary:       summary,
	})
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d: %s", len(lines), buffer.String())
	}
	t.Logf("gateway_dispatch_decision sample: %s", lines[0])
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record["msg"] != "gateway_dispatch_decision" || record["level"] != "INFO" {
		t.Fatalf("msg/level = %#v/%#v", record["msg"], record["level"])
	}
	if record["traceId"] != "trace-test" || record["groupId"] != "group-1" || record["apiKeyId"] != "apikey-1" {
		t.Fatalf("identity = %#v/%#v/%#v", record["traceId"], record["groupId"], record["apiKeyId"])
	}
	if record["candidateTotal"].(float64) != 30 || record["eligibleCount"].(float64) != 2 {
		t.Fatalf("window = %#v/%#v", record["candidateTotal"], record["eligibleCount"])
	}
	if record["selectedAccountId"] != "a-1" || record["durationMs"].(float64) != 12 {
		t.Fatalf("selection/timing = %#v/%#v", record["selectedAccountId"], record["durationMs"])
	}
	skippedTop, ok := record["skippedTop"].([]any)
	if !ok || len(skippedTop) != chainDispatchDecisionSkippedTopCap {
		t.Fatalf("skippedTop len = %d", len(skippedTop))
	}
	if record["skippedTopTruncated"] != true || record["skippedCount"].(float64) != 25 {
		t.Fatalf("truncated/count = %#v/%#v", record["skippedTopTruncated"], record["skippedCount"])
	}
	first := skippedTop[0].(map[string]any)
	if first["id"] != "s-0" || first["reason"] != gatewaydispatch.DispatchSkipReasonModelUnsupported {
		t.Fatalf("skippedTop[0] = %#v", first)
	}
	busy, ok := record["busyAccountIds"].([]any)
	if !ok || len(busy) != 1 || busy[0] != "a-2" {
		t.Fatalf("busyAccountIds = %#v", record["busyAccountIds"])
	}
}

// 无跳过：skippedTop/skippedCount/truncated 键不出场，日志仍恰好一条。
func TestW1bChainDispatchDecisionObserverOmitsEmptySkips(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := newChainDispatchDecisionObserver(logger)
	observer(gatewaydispatch.DispatchDecisionEvent{
		TraceID:    "trace-test",
		GroupID:    "group-1",
		APIKeyID:   "apikey-1",
		DurationMs: 3,
		Summary: gatewaydispatch.DispatchDecisionSummary{
			CandidateTotal: 2,
			EligibleCount:  2,
		},
	})
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d: %s", len(lines), buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, absent := range []string{"skippedTop", "skippedTopTruncated", "skippedCount", "selectedAccountId", "busyAccountIds", "suppressedAccountIds"} {
		if _, present := record[absent]; present {
			t.Fatalf("empty decision log must omit %s: %s", absent, lines[0])
		}
	}
}

// nil logger 回落 slog.Default() 不 panic。
func TestW1bChainDispatchDecisionObserverNilLoggerFallback(t *testing.T) {
	observer := newChainDispatchDecisionObserver(nil)
	if observer == nil {
		t.Fatal("observer must not be nil")
	}
	// 不断言默认 logger 输出，只验证 no-panic 契约。
	observer(gatewaydispatch.DispatchDecisionEvent{Summary: gatewaydispatch.DispatchDecisionSummary{EligibleCount: 1}})
}

// TestW1bChainDispatchDecisionPreFilterSkippedField 验证 W1b 续：预过滤
// （能力/模型）跳过明细进入 gateway_dispatch_decision 日志（计数 + 明细 +
// 截断置位；空输入时键不出场）。
func TestW1bChainDispatchDecisionPreFilterSkippedField(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := newChainDispatchDecisionObserver(logger)
	summary := gatewaydispatch.DispatchDecisionSummary{
		CandidateTotal:        2,
		EligibleCount:         1,
		SelectedAccountID:     "a-1",
		ModelRankAvailable:    true,
		PreFilterSkippedCount: 1,
		PreFilterSkipped:      []gatewaydispatch.DispatchDecisionSkip{{AccountID: "pre-1", Reason: "capability_mismatch"}},
	}
	observer(gatewaydispatch.DispatchDecisionEvent{
		TraceID: "trace-pf", GroupID: "group-1", APIKeyID: "apikey-1",
		DurationMs: 3, Summary: summary,
	})
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d: %s", len(lines), buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record["preFilterSkippedCount"].(float64) != 1 {
		t.Fatalf("preFilterSkippedCount = %#v", record["preFilterSkippedCount"])
	}
	preFilter, ok := record["preFilterSkipped"].([]any)
	if !ok || len(preFilter) != 1 {
		t.Fatalf("preFilterSkipped = %#v", record["preFilterSkipped"])
	}
	first := preFilter[0].(map[string]any)
	if first["id"] != "pre-1" || first["reason"] != "capability_mismatch" {
		t.Fatalf("preFilterSkipped[0] = %#v", first)
	}
	if _, present := record["preFilterSkippedTruncated"]; present {
		t.Fatal("preFilterSkippedTruncated must be absent under the cap")
	}
	// 截断路径：超过上限截断并置位。
	var truncatedBuffer bytes.Buffer
	truncatedLogger := slog.New(slog.NewJSONHandler(&truncatedBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	truncatedObserver := newChainDispatchDecisionObserver(truncatedLogger)
	truncatedSummary := gatewaydispatch.DispatchDecisionSummary{
		CandidateTotal:        30,
		EligibleCount:         1,
		PreFilterSkippedCount: chainDispatchDecisionSkippedTopCap + 3,
		PreFilterSkipped:      w1bSkippedEntries(chainDispatchDecisionSkippedTopCap + 3),
	}
	truncatedObserver(gatewaydispatch.DispatchDecisionEvent{TraceID: "trace-pf2", Summary: truncatedSummary})
	var truncatedRecord map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(truncatedBuffer.String())), &truncatedRecord); err != nil {
		t.Fatalf("decode truncated: %v", err)
	}
	preFilterTop, ok := truncatedRecord["preFilterSkipped"].([]any)
	if !ok || len(preFilterTop) != chainDispatchDecisionSkippedTopCap {
		t.Fatalf("truncated len = %d", len(preFilterTop))
	}
	if truncatedRecord["preFilterSkippedTruncated"] != true || truncatedRecord["preFilterSkippedCount"].(float64) != float64(chainDispatchDecisionSkippedTopCap+3) {
		t.Fatalf("truncated flags = %#v/%#v", truncatedRecord["preFilterSkippedTruncated"], truncatedRecord["preFilterSkippedCount"])
	}
	// 空输入：preFilterSkipped 键不出场。
	var emptyBuffer bytes.Buffer
	emptyLogger := slog.New(slog.NewJSONHandler(&emptyBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emptyObserver := newChainDispatchDecisionObserver(emptyLogger)
	emptyObserver(gatewaydispatch.DispatchDecisionEvent{TraceID: "trace-pf3", Summary: gatewaydispatch.DispatchDecisionSummary{CandidateTotal: 1, EligibleCount: 1}})
	if strings.Contains(emptyBuffer.String(), "preFilterSkipped") {
		t.Fatalf("empty preFilterSkipped keys must be omitted: %s", emptyBuffer.String())
	}
}
