package statsagg

// M4a 媒体计量维度聚合断言：usage_records 的五个媒体计量列
// （input_audio_tokens / output_audio_tokens / tts_input_chars /
// audio_input_seconds / output_video_seconds）进入 usage_stats 表族
// （totals + 时间桶）、usage_model 表族与 usage_scope_range_windows 范围
// 窗口快照；游标增量语义不变（第二批空转零处理）。

import (
	"context"
	"testing"
	"time"
)

func TestAccumulatorMediaMeteringFromRecord(t *testing.T) {
	record := UsageStatsRecordRow{
		ID: "rec-tts", SystemAccountID: "alice", TrafficSource: "gateway",
		Success:            1,
		InputAudioTokens:   f64Ptr(1200),
		OutputAudioTokens:  f64Ptr(300),
		TTSInputChars:      f64Ptr(4567),
		AudioInputSeconds:  f64Ptr(62.5),
		OutputVideoSeconds: f64Ptr(0),
		CreatedAt:          "2026-10-04T08:30:00.000Z",
	}
	accumulator := UsageStatsAccumulatorFromRecord(record)
	if accumulator.InputAudioTokens != 1200 || accumulator.OutputAudioTokens != 300 ||
		accumulator.TTSInputChars != 4567 || accumulator.AudioInputSeconds != 62.5 ||
		accumulator.OutputVideoSeconds != 0 {
		t.Fatalf("媒体计量 accumulator = %+v", accumulator)
	}

	// 负值钳位（与 token 维度同层非负语义）。
	negative := UsageStatsAccumulatorFromRecord(UsageStatsRecordRow{
		ID: "rec-neg", SystemAccountID: "alice", TrafficSource: "gateway",
		Success: 1, InputAudioTokens: f64Ptr(-5), TTSInputChars: f64Ptr(-9),
		AudioInputSeconds: f64Ptr(-0.5), OutputVideoSeconds: f64Ptr(-2),
		CreatedAt: "2026-04-18T08:31:00.000Z",
	})
	if negative.InputAudioTokens != 0 || negative.TTSInputChars != 0 ||
		negative.AudioInputSeconds != 0 || negative.OutputVideoSeconds != 0 {
		t.Fatalf("负值媒体计量未钳位: %+v", negative)
	}

	// 合并累加。
	merged := UsageStatsAccumulator{InputAudioTokens: 100, TTSInputChars: 10, AudioInputSeconds: 1.5, OutputVideoSeconds: 2}
	if err := MergeAccumulator(&merged, accumulator); err != nil {
		t.Fatal(err)
	}
	if merged.InputAudioTokens != 1300 || merged.TTSInputChars != 4577 ||
		merged.AudioInputSeconds != 64 || merged.OutputVideoSeconds != 2 {
		t.Fatalf("merge 后媒体计量 = %+v", merged)
	}
}

func TestAggregateMediaMeteringColumns(t *testing.T) {
	env := newTestEnv(t)
	// 同一 api_key scope：TTS 音频记录 + STT 音频记录 + 视频任务终态记录。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-tts", SystemAccountID: "alice", TraceID: "tr-tts", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/audio/speech"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-4o-mini-tts"),
		Success: 1, CostUsd: f64Ptr(0.01),
		TTSInputChars: f64Ptr(4567),
		CreatedAt:     "2026-04-18T08:30:00.000Z",
	})
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-stt", SystemAccountID: "alice", TraceID: "tr-stt", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/audio/transcriptions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-4o-mini-transcribe"),
		Success: 1, CostUsd: f64Ptr(0.005),
		InputAudioTokens:  f64Ptr(1200),
		AudioInputSeconds: f64Ptr(62.5),
		CreatedAt:         "2026-04-18T09:10:00.000Z",
	})
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-video", SystemAccountID: "alice", TraceID: "tr-video", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/videos"),
		ProviderCode: strPtr("openai"), Model: strPtr("sora-2"),
		Success: 1, CostUsd: f64Ptr(0.4),
		OutputVideoSeconds: f64Ptr(4),
		CreatedAt:          "2026-04-18T10:05:00.000Z",
	})

	processed, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 3 {
		t.Fatalf("processed = %d want 3", processed)
	}

	// usage_stats_totals（api_key scope）：媒体计量全量累加。
	env.assertFloats("api_key totals media metering",
		env.queryRowFloats(`SELECT input_audio_tokens, output_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds
			FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1'`),
		1200, 0, 4567, 62.5, 4)

	// usage_stats_daily / usage_stats_minute：日桶全量、分钟桶按记录落桶。
	env.assertFloats("api_key daily media metering",
		env.queryRowFloats(`SELECT input_audio_tokens, output_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds
			FROM usage_stats_daily WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND stat_date='2026-04-18'`),
		1200, 0, 4567, 62.5, 4)
	env.assertFloats("api_key minute tts bucket",
		env.queryRowFloats(`SELECT tts_input_chars
			FROM usage_stats_minute WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND stat_minute='2026-04-18T08:30'`),
		4567)
	env.assertFloats("api_key minute video bucket",
		env.queryRowFloats(`SELECT output_video_seconds
			FROM usage_stats_minute WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND stat_minute='2026-04-18T10:05'`),
		4)

	// usage_model_daily：模型桶按模型分行（caller 系统账户行）。
	env.assertFloats("model daily tts chars",
		env.queryRowFloats(`SELECT tts_input_chars
			FROM usage_model_daily WHERE system_account_id='alice' AND stat_date='2026-04-18' AND provider_code='openai' AND model='gpt-4o-mini-tts'`),
		4567)
	env.assertFloats("model daily stt audio",
		env.queryRowFloats(`SELECT input_audio_tokens, audio_input_seconds
			FROM usage_model_daily WHERE system_account_id='alice' AND stat_date='2026-04-18' AND provider_code='openai' AND model='gpt-4o-mini-transcribe'`),
		1200, 62.5)
	env.assertFloats("model daily video seconds",
		env.queryRowFloats(`SELECT output_video_seconds
			FROM usage_model_daily WHERE system_account_id='alice' AND stat_date='2026-04-18' AND provider_code='openai' AND model='sora-2'`),
		4)

	// 第二批：游标已推进，空批不再重复累计。
	processed, err = env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("第二批 processed = %d want 0", processed)
	}
	env.assertFloats("api_key totals after second batch",
		env.queryRowFloats(`SELECT input_audio_tokens, tts_input_chars, output_video_seconds
			FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1'`),
		1200, 4567, 4)

	// 范围窗口快照：热窗口刷新后 usage_scope_range_windows 媒体列 = 日桶口径。
	// today 窗口（2026-04-19）无当日数据，HAVING 过滤后无行是正确语义；
	// last7d 窗口覆盖记录日 2026-04-18，媒体列等于日桶求和。
	env.now = env.now.Add(24 * time.Hour) // 模拟次日刷新，热窗口覆盖 2026-04-18
	if _, err := env.refresher().RunHotWindows(context.Background(), RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	env.assertFloats("scope range window last7d media metering",
		env.queryRowFloats(`SELECT input_audio_tokens, output_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds
			FROM usage_scope_range_windows
			WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND start_date='2026-04-13' AND end_date='2026-04-19'`),
		1200, 0, 4567, 62.5, 4)
}
