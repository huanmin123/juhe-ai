package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestWarnDeprecatedSwitches 覆盖废弃总开关告警的三种臂：未设置不输出、
// 值恰为 true（trim + 大小写不敏感）向后兼容静默、其余值各告警一次且
// 事件名与变量名正确。
func TestWarnDeprecatedSwitches(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	// 未设置：无输出。
	warnDeprecatedSwitches(logger)
	if buf.Len() != 0 {
		t.Fatalf("未设置废弃开关时不得输出告警: %s", buf.String())
	}

	// 值恰为 true（trim + 大小写不敏感）：静默。
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_ENABLED", " TRUE ")
	t.Setenv("JUHE_AI_JOBS_WORKER_ENABLED", "true")
	buf.Reset()
	warnDeprecatedSwitches(logger)
	if buf.Len() != 0 {
		t.Fatalf("值恰为 true 时必须保持静默: %s", buf.String())
	}

	// 非 true 值：各告警一次，事件名与文案含变量名。
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_ENABLED", "1")
	t.Setenv("JUHE_AI_JOBS_WORKER_ENABLED", "false")
	buf.Reset()
	warnDeprecatedSwitches(logger)
	events := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record struct {
			Msg   string `json:"msg"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("告警必须是 JSON 行: %v %s", err, line)
		}
		events[record.Event] = true
		switch record.Event {
		case "account_health_enabled_deprecated":
			if !strings.Contains(record.Msg, "JUHE_AI_ACCOUNT_HEALTH_ENABLED") {
				t.Fatalf("告警文案必须含变量名: %s", record.Msg)
			}
		case "jobs_worker_enabled_deprecated":
			if !strings.Contains(record.Msg, "JUHE_AI_JOBS_WORKER_ENABLED") {
				t.Fatalf("告警文案必须含变量名: %s", record.Msg)
			}
		default:
			t.Fatalf("未知事件: %s", record.Event)
		}
	}
	if !events["account_health_enabled_deprecated"] || !events["jobs_worker_enabled_deprecated"] {
		t.Fatalf("两个废弃事件必须各告警一次: %v", events)
	}
}
