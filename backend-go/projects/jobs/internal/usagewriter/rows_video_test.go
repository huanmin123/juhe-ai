package usagewriter

// M2 视频计量落库测试（媒体设计 §10）：WritePlan 行参数携带
// output_video_seconds 列，nil 计量落 0（NOT NULL DEFAULT 0 列语义）；
// 列序与 PG/SQLite DDL 的 usage_missing 之后一致。
import (
	"context"
	"testing"
)

func TestBuildWritePlanVideoMeteringParams(t *testing.T) {
	seconds := 4.0
	input := gatewayInput("video-metering")
	input.OutputVideoSeconds = &seconds
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
	// 列序：output_video_seconds 紧随 usage_missing（与 DDL 一致）。
	if indexOf("output_video_seconds") != indexOf("usage_missing")+1 {
		t.Fatalf("output_video_seconds must follow usage_missing (got %d, usage_missing at %d)",
			indexOf("output_video_seconds"), indexOf("usage_missing"))
	}
	if got := row.Params[indexOf("output_video_seconds")]; got != 4.0 {
		t.Fatalf("output_video_seconds = %v", got)
	}
}

func TestBuildWritePlanVideoMeteringNilFallsToZero(t *testing.T) {
	input := gatewayInput("video-metering-nil")
	input.CreatedAt = "2026-01-02T03:04:05.000Z"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		Postgres:   true,
		ShardCount: 4,
	}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	row := plan.RowsByShard[0].Rows[0]
	for position, column := range UsageRecordColumns {
		if column == "output_video_seconds" && row.Params[position] != 0.0 {
			t.Fatalf("nil video seconds must write 0, got %v", row.Params[position])
		}
	}
}
