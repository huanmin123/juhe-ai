package statsverify

import (
	"context"
	"testing"
	"time"
)

// Golden derivations follow client-ip-usage-range-windows.repository.ts:
// windows are [today,today], [today-6,today], [today-30,today]; a refresh
// rewrites each window from client_ip_stats_daily with the positive-metric
// HAVING clause; ready flags live in stats_job_state
// (scope_type='client_ip_range_window', scope_id='start:end').

func TestClientIPRangeWindowsForTimezone(t *testing.T) {
	windows := ClientIPRangeWindowsForTimezone(time.UTC, fixedUTC(t, "2026-03-15T12:00:00Z").Now())
	if len(windows) != 3 {
		t.Fatalf("windows=%d, want 3", len(windows))
	}
	want := [][2]string{{"2026-03-15", "2026-03-15"}, {"2026-03-09", "2026-03-15"}, {"2026-02-13", "2026-03-15"}}
	for index, expect := range want {
		if windows[index].StartDate != expect[0] || windows[index].EndDate != expect[1] {
			t.Fatalf("window[%d]=%+v, want %v", index, windows[index], expect)
		}
	}
	// The 7-day start only collapses into the 31-day start when the fixed
	// window shrinks below 7 days; with 31 days the three keys stay distinct,
	// matching Node (which also emits three windows here).
	short := ClientIPRangeWindowsForTimezone(time.UTC, fixedUTC(t, "2026-02-13T00:00:00Z").Now())
	if len(short) != 3 || short[0].StartDate != "2026-02-13" || short[1].StartDate != "2026-02-07" || short[2].StartDate != "2026-01-14" {
		t.Fatalf("short horizon windows=%+v", short)
	}
}

func seedDailyForWindows(t *testing.T, ctx context.Context, store *Store, ipHash, statDate string, requests, inputTokens int, lastUsedAt string) {
	t.Helper()
	mustExec(t, ctx, store.db, `
		INSERT INTO client_ip_stats_daily (
		  ip_hash, stat_date, request_count, success_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens,
		  input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
		  last_used_at, last_error_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ipHash, statDate, requests, requests, 0,
		inputTokens, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0,
		0, 0, 0, 0, 0,
		lastUsedAt, nil, "2026-03-01T00:00:00.000Z")
}

