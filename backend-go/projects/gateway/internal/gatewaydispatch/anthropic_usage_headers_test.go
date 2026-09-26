package gatewaydispatch

// Anthropic（Claude OAuth）unified rate limit 响应头解析与 job 构建回归
// （AI账户Grok用量快照设计 §8）：utilization 0-1 → 百分比换算、重置时间三种
// 上游格式归一（unix 秒浮点 / RFC3339 / HTTP 时间）、缺头返回 nil、NaN/Inf
// 与坏时间只跳过该字段；job 载荷按 claude_ 前缀全部可选缺失不写。

import (
	"net/http"
	"testing"
	"time"
)

func TestParseAnthropicUsageHeaders(t *testing.T) {
	t.Run("utilization converts to percent", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.14")
		headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.80")
		snapshot := ParseAnthropicUsageHeaders(headers)
		if snapshot == nil {
			t.Fatal("expected snapshot")
		}
		if snapshot.Used5hPercent == nil || *snapshot.Used5hPercent != 14 {
			t.Fatalf("5h percent = %v want 14", snapshot.Used5hPercent)
		}
		if snapshot.Used7dPercent == nil || *snapshot.Used7dPercent != 80 {
			t.Fatalf("7d percent = %v want 80", snapshot.Used7dPercent)
		}
		if snapshot.Reset5hAt != "" || snapshot.Reset7dAt != "" || snapshot.UnifiedStatus != "" {
			t.Fatalf("missing fields must stay empty: %+v", snapshot)
		}
	})
	t.Run("reset unix seconds float", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1790481600.5")
		snapshot := ParseAnthropicUsageHeaders(headers)
		if snapshot == nil || snapshot.Reset5hAt == "" {
			t.Fatalf("expected 5h reset: %+v", snapshot)
		}
		parsed, err := time.Parse("2006-01-02T15:04:05.000Z", snapshot.Reset5hAt)
		if err != nil {
			t.Fatalf("reset %q must be UTC millisecond RFC3339: %v", snapshot.Reset5hAt, err)
		}
		if want := time.UnixMilli(1790481600500).UTC(); !parsed.Equal(want) {
			t.Fatalf("reset = %v want %v", parsed, want)
		}
	})
	t.Run("reset rfc3339 and http time", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "2026-09-27T08:00:00+02:00")
		headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "Mon, 28 Sep 2026 08:00:00 GMT")
		snapshot := ParseAnthropicUsageHeaders(headers)
		if snapshot == nil {
			t.Fatal("expected snapshot")
		}
		if snapshot.Reset5hAt != "2026-09-27T06:00:00.000Z" {
			t.Fatalf("5h reset = %q want UTC canonical", snapshot.Reset5hAt)
		}
		if snapshot.Reset7dAt != "2026-09-28T08:00:00.000Z" {
			t.Fatalf("7d reset = %q want UTC canonical", snapshot.Reset7dAt)
		}
	})
	t.Run("status captured", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-Status", "allowed_warning")
		snapshot := ParseAnthropicUsageHeaders(headers)
		if snapshot == nil || snapshot.UnifiedStatus != "allowed_warning" {
			t.Fatalf("status snapshot = %+v", snapshot)
		}
	})
	t.Run("nan and inf utilization skipped", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "NaN")
		headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "Inf")
		if snapshot := ParseAnthropicUsageHeaders(headers); snapshot != nil {
			t.Fatalf("NaN/Inf only headers must read as no data: %+v", snapshot)
		}
	})
	t.Run("unparsable reset skipped", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "not-a-time")
		headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.25")
		snapshot := ParseAnthropicUsageHeaders(headers)
		if snapshot == nil || snapshot.Reset5hAt != "" || snapshot.Used5hPercent == nil {
			t.Fatalf("bad reset must skip just the field: %+v", snapshot)
		}
	})
	t.Run("no data", func(t *testing.T) {
		if snapshot := ParseAnthropicUsageHeaders(http.Header{}); snapshot != nil {
			t.Fatalf("unexpected snapshot %#v", snapshot)
		}
		if snapshot := ParseAnthropicUsageHeaders(nil); snapshot != nil {
			t.Fatalf("nil headers must return nil, got %#v", snapshot)
		}
	})
}

func TestBuildAnthropicUsageRecordMaintenanceJob(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.14")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1790481600")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.07")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "2026-09-28T02:34:03Z")

	job := BuildAnthropicUsageRecordMaintenanceJob("acc_claude", headers, "gateway")
	if job == nil {
		t.Fatal("expected job")
	}
	if job.Type != "account_usage_snapshot_upsert" || job.Kind != "anthropic_claude" ||
		job.AccountID != "acc_claude" || job.Source != "gateway" {
		t.Fatalf("job envelope = %#v", job)
	}
	if job.Snapshot["claude_5h_used_percent"] != float64(14) {
		t.Fatalf("claude_5h_used_percent = %#v", job.Snapshot["claude_5h_used_percent"])
	}
	if job.Snapshot["claude_7d_used_percent"] != float64(7) {
		t.Fatalf("claude_7d_used_percent = %#v", job.Snapshot["claude_7d_used_percent"])
	}
	if job.Snapshot["claude_5h_reset_at"] != "2026-09-27T04:00:00.000Z" {
		t.Fatalf("claude_5h_reset_at = %v", job.Snapshot["claude_5h_reset_at"])
	}
	if job.Snapshot["claude_7d_reset_at"] != "2026-09-28T02:34:03.000Z" {
		t.Fatalf("claude_7d_reset_at = %v", job.Snapshot["claude_7d_reset_at"])
	}
	if job.Snapshot["claude_unified_status"] != "allowed" {
		t.Fatalf("claude_unified_status = %v", job.Snapshot["claude_unified_status"])
	}
	updatedAt, ok := job.Snapshot["claude_usage_updated_at"].(string)
	if !ok {
		t.Fatalf("updatedAt = %#v, want Node toISOString string", job.Snapshot["claude_usage_updated_at"])
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", updatedAt); err != nil {
		t.Fatalf("updatedAt = %q, want Node toISOString millisecond shape", updatedAt)
	}
	if job.UpdatedAt != updatedAt {
		t.Fatalf("job.UpdatedAt = %q want payload value %q", job.UpdatedAt, updatedAt)
	}

	// 缺 Utilization 的窗口与空 source：对应 payload 字段与 source 键不写。
	job = BuildAnthropicUsageRecordMaintenanceJob("acc_claude", http.Header{
		"Anthropic-Ratelimit-Unified-Status": []string{"allowed"},
	}, "")
	if job == nil {
		t.Fatal("expected status-only job")
	}
	for _, key := range []string{"claude_5h_used_percent", "claude_5h_reset_at", "claude_7d_used_percent", "claude_7d_reset_at", "source"} {
		if _, ok := job.Snapshot[key]; ok {
			t.Fatalf("absent header field %s must not be written", key)
		}
	}

	// 无 anthropic 头：nil。
	if job := BuildAnthropicUsageRecordMaintenanceJob("acc_claude", http.Header{"X-Other": []string{"1"}}, "gateway"); job != nil {
		t.Fatalf("headers without anthropic data must return nil, got %#v", job)
	}
}

func TestPersistAnthropicUsageHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.14")
	queue := &collectorQueue{}
	if !PersistAnthropicUsageHeaders(queue, "acc_claude", headers, "gateway_error") {
		t.Fatal("expected persisted job")
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("jobs = %d", len(queue.jobs))
	}
	if queue.jobs[0].Kind != "anthropic_claude" || queue.jobs[0].Source != "gateway_error" {
		t.Fatalf("job = %#v", queue.jobs[0])
	}
	if queue.jobs[0].Snapshot["claude_5h_used_percent"] != float64(14) {
		t.Fatalf("payload = %#v", queue.jobs[0].Snapshot)
	}
	if PersistAnthropicUsageHeaders(queue, "acc_claude", http.Header{}, "gateway_error") {
		t.Fatal("headers without anthropic data must not enqueue")
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("jobs = %d want 1", len(queue.jobs))
	}
}
