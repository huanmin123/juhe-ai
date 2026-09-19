// worker 关闭告警（JUHE_AI_JOBS_WORKER_ENABLED=false 用量断供告警）：
//   - warnWorkerDisabled 输出 slog Warn 记录；
//   - 记录含 event=jobs_worker_disabled_usage_supply_degraded 结构化属性。
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestWorkerDisabledWarnRecordsDegradedEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	warnWorkerDisabled(logger)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("告警必须是单行 JSON: %v; 原始输出 %q", err, buf.String())
	}
	if record["level"] != slog.LevelWarn.String() {
		t.Fatalf("告警级别必须是 WARN: %v; 原始输出 %q", record["level"], buf.String())
	}
	if record["event"] != "jobs_worker_disabled_usage_supply_degraded" {
		t.Fatalf("event 属性必须是 jobs_worker_disabled_usage_supply_degraded: %v; 原始输出 %q", record["event"], buf.String())
	}
	if message, _ := record["msg"].(string); message == "" {
		t.Fatalf("告警 msg 不得为空: 原始输出 %q", buf.String())
	}
}
