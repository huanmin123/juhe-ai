package statreads

// M4a 媒体计量维度读面断言：usage-records 明细行与 account-usage 汇总行
// 返回五个媒体字段（inputAudioTokens / outputAudioTokens / ttsInputChars /
// audioInputSeconds / outputVideoSeconds），值来自 usage_records 明细列与
// usage_stats_daily 聚合列。

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedMediaUsageShard 建一个含媒体计量列的 usage shard 并注册 catalog。
func seedMediaUsageShard(t *testing.T, fixture *testFixture, shardDir, fileName string) error {
	t.Helper()
	shardPath := filepath.Join(shardDir, fileName)
	shard, err := sql.Open("sqlite", shardPath)
	if err != nil {
		return err
	}
	defer shard.Close()
	if _, err := shard.Exec(`CREATE TABLE usage_records (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, trace_id TEXT NOT NULL, traffic_source TEXT NOT NULL,
		client_ip TEXT, api_key_id TEXT, group_id TEXT, account_id TEXT, endpoint TEXT,
		model TEXT, upstream_model TEXT, upstream_response_model TEXT, billed_service_tier TEXT,
		effective_reasoning_effort TEXT, model_mapping_applied INTEGER DEFAULT 0, stream INTEGER DEFAULT 0,
		status_code INTEGER, success INTEGER DEFAULT 0, failure_attribution TEXT,
		error_code TEXT, error_message TEXT, first_token_ms INTEGER, duration_ms INTEGER,
		input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
		input_audio_tokens INTEGER, output_audio_tokens INTEGER, tts_input_chars INTEGER NOT NULL DEFAULT 0,
		audio_input_seconds REAL NOT NULL DEFAULT 0, output_video_seconds REAL NOT NULL DEFAULT 0,
		cost_usd REAL, created_at TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := shard.Exec(`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source,
		success, status_code, input_audio_tokens, output_audio_tokens, tts_input_chars,
		audio_input_seconds, output_video_seconds, cost_usd, created_at) VALUES
		('u-media', 'sys-user-1', 'trace-media', 'gateway', 1, 200, 1200, 300, 4567, 62.5, 4, 0.02, '2026-09-04T10:00:00.000Z')`); err != nil {
		return err
	}
	catalog, err := sql.Open("sqlite", filepath.Join(shardDir, "catalog.sqlite3"))
	if err != nil {
		return err
	}
	// catalog 句柄交给 deps.UsageCatalog 持有，测试结束时统一关闭。
	t.Cleanup(func() { _ = catalog.Close() })
	if _, err := catalog.Exec(`CREATE TABLE usage_record_shards (
		shard_key TEXT PRIMARY KEY, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL,
		file_path TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := catalog.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
		VALUES ('20260904:s000', '2026-09-04', 0, ?, 'active')`, shardPath); err != nil {
		return err
	}
	fixture.deps.UsageCatalog = catalog
	return nil
}

func TestAccountUsageSummaryMediaMeteringFields(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
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
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count,
			input_tokens, output_tokens, total_cost_usd, input_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds)
			VALUES ('global', 'account', 'acct-media', '2026-09-04', 1, 0, 0, 0.1, 1200, 4567, 62.5, 4)`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-media', '媒体账户', 'sys-owner-1', 'openai', 'api_key', 'active')`,
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

func TestUsageRecordsListMediaFieldsFromShard(t *testing.T) {
	fixture := newFixture(t)
	shardDir := t.TempDir()
	if err := seedMediaUsageShard(t, fixture, shardDir, "usage_20260904_s000.sqlite3"); err != nil {
		t.Fatal(err)
	}
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet,
		"/__aisys__/api/my-usage-records?startDate=2026-09-04&endDate=2026-09-04", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage-records not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("usage-records items wrong: %#v", payload["items"])
	}
	item := items[0].(map[string]any)
	if item["inputAudioTokens"] != float64(1200) || item["outputAudioTokens"] != float64(300) ||
		item["ttsInputChars"] != float64(4567) {
		t.Fatalf("usage record media token fields wrong: %#v", item)
	}
	if item["audioInputSeconds"] != 62.5 || item["outputVideoSeconds"] != float64(4) {
		t.Fatalf("usage record media seconds fields wrong: %#v", item)
	}
}
