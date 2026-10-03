package usagewriter

// M1 同步音频计量落库测试（音频设计 §10）：WritePlan 行参数携带
// tts_input_chars / audio_input_seconds / usage_missing 三列，nil 计量落 0
// （NOT NULL DEFAULT 0 列语义）。
import (
	"context"
	"testing"
	"time"
)

func TestBuildWritePlanAudioMeteringParams(t *testing.T) {
	chars := int64(128)
	seconds := 2.5
	input := gatewayInput("audio-metering")
	input.TtsInputChars = &chars
	input.AudioInputSeconds = &seconds
	input.UsageMissing = false
	input.CreatedAt = "2026-01-02T03:04:05.000Z"

	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		Postgres:   true,
		ShardCount: 4,
	}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.RowsByShard) != 1 || len(plan.RowsByShard[0].Rows) != 1 {
		t.Fatalf("plan shape: %+v", plan.RowsByShard)
	}
	row := plan.RowsByShard[0].Rows[0]
	if len(row.Params) != len(UsageRecordColumns) {
		t.Fatalf("column/param count: %d vs %d", len(UsageRecordColumns), len(row.Params))
	}
	indexOf := func(name string) int {
		for position, column := range UsageRecordColumns {
			if column == name {
				return position
			}
		}
		t.Fatalf("column %s missing", name)
		return -1
	}
	if got := row.Params[indexOf("tts_input_chars")]; got != int64(128) {
		t.Fatalf("tts_input_chars = %v", got)
	}
	if got := row.Params[indexOf("audio_input_seconds")]; got != 2.5 {
		t.Fatalf("audio_input_seconds = %v", got)
	}
	if got := row.Params[indexOf("usage_missing")]; got != 0 {
		t.Fatalf("usage_missing = %v", got)
	}
}

func TestBuildWritePlanAudioMeteringNilFallsToZero(t *testing.T) {
	input := gatewayInput("audio-metering-nil")
	input.UsageMissing = true
	input.CreatedAt = "2026-01-02T03:04:05.000Z"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		Postgres:   true,
		ShardCount: 4,
	}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	row := plan.RowsByShard[0].Rows[0]
	indexOf := func(name string) int {
		for position, column := range UsageRecordColumns {
			if column == name {
				return position
			}
		}
		t.Fatalf("column %s missing", name)
		return -1
	}
	if got := row.Params[indexOf("tts_input_chars")]; got != 0 {
		t.Fatalf("nil tts chars must write 0, got %v", got)
	}
	if got := row.Params[indexOf("audio_input_seconds")]; got != 0.0 {
		t.Fatalf("nil seconds must write 0, got %v", got)
	}
	if got := row.Params[indexOf("usage_missing")]; got != 1 {
		t.Fatalf("usage_missing = %v want 1", got)
	}
	_ = time.Now
}
