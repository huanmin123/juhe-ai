package statreads

// M4a 媒体计量维度读面断言：usage-records 明细行与 account-usage 汇总行
// 返回五个媒体字段（inputAudioTokens / outputAudioTokens / ttsInputChars /
// audioInputSeconds / outputVideoSeconds），值来自 usage_records 明细列与
// usage_stats_daily 聚合列。

import (
	"net/http"
	"testing"
)

// seedMediaUsageShard 建一个含媒体计量列的 usage shard 并注册 catalog。

func TestAccountUsageSummaryMediaMeteringFields(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		request_count, input_tokens, output_tokens, total_cost_usd,
		input_audio_tokens, output_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds)
		VALUES ('sys-user-1', 'system_account', 'sys-user-1', '2026-09-04', 2, 10, 5, 0.5,
			1200, 300, 4567, 62.5, 4)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	handler := fixture.deps.accountUsageSummaryHandler(true)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/my-stats/account-usage/summary", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("my summary not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	summary := dataMap(t, decodeBody(t, recorder))["summary"].(map[string]any)
	if summary["inputAudioTokens"] != float64(1200) || summary["outputAudioTokens"] != float64(300) ||
		summary["ttsInputChars"] != float64(4567) {
		t.Fatalf("summary media token fields wrong: %#v", summary)
	}
	if summary["audioInputSeconds"] != 62.5 || summary["outputVideoSeconds"] != float64(4) {
		t.Fatalf("summary media seconds fields wrong: %#v", summary)
	}
}

func TestAccountUsagePageRangeUsageMediaFields(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count,
			input_tokens, output_tokens, total_cost_usd, input_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds)
			VALUES ('global', 'account', 'acct-media', '2026-09-04', 1, 0, 0, 0.1, 1200, 4567, 62.5, 4)`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status) VALUES ('acct-media', '媒体账户', 'sys-owner-1', 'openai', 'api_key', 'active')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.accountUsageHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/account-usage?pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("account-usage not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("account-usage rows wrong: %#v", payload["rows"])
	}
	rangeUsage := rows[0].(map[string]any)["rangeUsage"].(map[string]any)
	if rangeUsage["inputAudioTokens"] != float64(1200) || rangeUsage["ttsInputChars"] != float64(4567) ||
		rangeUsage["audioInputSeconds"] != 62.5 || rangeUsage["outputVideoSeconds"] != float64(4) {
		t.Fatalf("rangeUsage media fields wrong: %#v", rangeUsage)
	}
}
