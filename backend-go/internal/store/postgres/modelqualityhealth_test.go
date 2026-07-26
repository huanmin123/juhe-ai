package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/timezonecompat"
)

func TestPrepareModelQualityHealthFailureCanonicalizesAndBuckets(t *testing.T) {
	location, err := timezonecompat.LoadNodeLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	input := modelQualityHealthInput()
	input.ObservedAt = time.Date(2026, 7, 25, 16, 30, 45, 987654321, time.UTC)
	input.UpdatedAt = time.Date(2026, 7, 25, 16, 31, 0, 123456789, time.UTC)
	input.ErrorMessage = strings.Repeat("界", 1001)

	prepared, err := prepareModelQualityHealthFailure(input, location)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.statHour != "2026-07-26T00" {
		t.Fatalf("statHour = %q, want 2026-07-26T00", prepared.statHour)
	}
	if got := modelQualityPolicyTimeText(prepared.input.ObservedAt); got != "2026-07-25T16:30:45.987Z" {
		t.Fatalf("observedAt = %q", got)
	}
	if got := modelQualityPolicyTimeText(prepared.input.UpdatedAt); got != "2026-07-25T16:31:00.123Z" {
		t.Fatalf("updatedAt = %q", got)
	}
	if got := utf8.RuneCountInString(prepared.input.ErrorMessage); got != 1000 {
		t.Fatalf("error message runes = %d, want 1000", got)
	}
}

func TestPrepareModelQualityHealthFailureRetainsNodeDSTHourCollision(t *testing.T) {
	location, err := timezonecompat.LoadNodeLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	first := modelQualityHealthInput()
	first.ObservedAt = time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	second := first
	second.ObservedAt = time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)

	firstPrepared, err := prepareModelQualityHealthFailure(first, location)
	if err != nil {
		t.Fatal(err)
	}
	secondPrepared, err := prepareModelQualityHealthFailure(second, location)
	if err != nil {
		t.Fatal(err)
	}
	if firstPrepared.statHour != "2026-11-01T01" || secondPrepared.statHour != firstPrepared.statHour {
		t.Fatalf("DST stat hours = %q, %q", firstPrepared.statHour, secondPrepared.statHour)
	}
}

func TestRecordModelQualityHealthFailureUsesBoundedCanonicalArguments(t *testing.T) {
	prepared, err := prepareModelQualityHealthFailure(modelQualityHealthInput(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	execer := &modelQualityHealthExecStub{tag: pgconn.NewCommandTag("INSERT 0 1")}
	result, err := recordModelQualityHealthFailure(context.Background(), execer, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.StatHour != "2026-07-26T08" || execer.calls != 1 {
		t.Fatalf("result = %+v, calls = %d", result, execer.calls)
	}
	want := []any{
		"account-1", "system-1", "openai", "2026-07-26T08", "2026-07-26T08:09:10.123Z",
		"run-1", "gpt-5", "quick", 0, 70, "unavailable",
		pgtype.Text{String: "transport_error", Valid: true},
		pgtype.Text{String: "upstream unavailable", Valid: true},
		"2026-07-26T08:10:00.000Z",
	}
	if !reflect.DeepEqual(execer.args, want) {
		t.Fatalf("args = %#v, want %#v", execer.args, want)
	}
	if !strings.Contains(execer.query, "ON CONFLICT (account_id, stat_hour)") {
		t.Fatalf("query does not use hourly idempotency key: %s", execer.query)
	}
}

func TestRecordModelQualityHealthFailureReportsStaleReplayWithoutExtraRead(t *testing.T) {
	prepared, err := prepareModelQualityHealthFailure(modelQualityHealthInput(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	execer := &modelQualityHealthExecStub{tag: pgconn.NewCommandTag("INSERT 0 0")}
	result, err := recordModelQualityHealthFailure(context.Background(), execer, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.StatHour == "" || execer.calls != 1 {
		t.Fatalf("result = %+v, calls = %d", result, execer.calls)
	}
}

func TestRecordModelQualityHealthFailurePropagatesExecError(t *testing.T) {
	prepared, err := prepareModelQualityHealthFailure(modelQualityHealthInput(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("database unavailable")
	_, err = recordModelQualityHealthFailure(context.Background(), &modelQualityHealthExecStub{err: want}, prepared)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

func TestPrepareModelQualityHealthFailureRejectsInvalidBoundaries(t *testing.T) {
	oversized := strings.Repeat("x", modelQualityHealthMaximumMessageBytes+1)
	tests := []struct {
		name   string
		mutate func(*port.ModelQualityHealthFailureInput)
	}{
		{"nil location", func(*port.ModelQualityHealthFailureInput) {}},
		{"trimmed account ID", func(input *port.ModelQualityHealthFailureInput) { input.AccountID = " account-1" }},
		{"score below zero", func(input *port.ModelQualityHealthFailureInput) { input.Score = -1 }},
		{"score above one hundred", func(input *port.ModelQualityHealthFailureInput) { input.Score = 101 }},
		{"threshold below policy range", func(input *port.ModelQualityHealthFailureInput) { input.Threshold = 39 }},
		{"unsupported profile", func(input *port.ModelQualityHealthFailureInput) { input.Profile = "deep" }},
		{"unsupported level", func(input *port.ModelQualityHealthFailureInput) { input.Level = "unknown" }},
		{"missing observed time", func(input *port.ModelQualityHealthFailureInput) { input.ObservedAt = time.Time{} }},
		{"missing updated time", func(input *port.ModelQualityHealthFailureInput) { input.UpdatedAt = time.Time{} }},
		{"local year underflow", func(input *port.ModelQualityHealthFailureInput) {
			input.ObservedAt = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"control in error code", func(input *port.ModelQualityHealthFailureInput) { input.ErrorCode = "bad\ncode" }},
		{"NUL in message", func(input *port.ModelQualityHealthFailureInput) { input.ErrorMessage = "bad\x00message" }},
		{"oversized message", func(input *port.ModelQualityHealthFailureInput) { input.ErrorMessage = oversized }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := modelQualityHealthInput()
			test.mutate(&input)
			location := time.UTC
			if test.name == "nil location" {
				location = nil
			} else if test.name == "local year underflow" {
				location = time.FixedZone("negative", -60*60)
			}
			if _, err := prepareModelQualityHealthFailure(input, location); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestModelQualityHealthUpsertOrdersOnlyByObservationAndRun(t *testing.T) {
	required := []string{
		"INSERT INTO juhe_stats.account_quality_health_hourly AS health_row",
		"EXCLUDED.observed_at > health_row.observed_at",
		"EXCLUDED.observed_at = health_row.observed_at",
		"EXCLUDED.model_check_run_id > health_row.model_check_run_id",
	}
	for _, fragment := range required {
		if !strings.Contains(upsertModelQualityHealthFailureSQL, fragment) {
			t.Fatalf("upsert query missing %q", fragment)
		}
	}
	where := upsertModelQualityHealthFailureSQL[strings.Index(upsertModelQualityHealthFailureSQL, "WHERE "):]
	if strings.Contains(where, "updated_at") {
		t.Fatalf("updated_at must not influence last-write-wins ordering: %s", where)
	}
}

func modelQualityHealthInput() port.ModelQualityHealthFailureInput {
	return port.ModelQualityHealthFailureInput{
		AccountID:       "account-1",
		SystemAccountID: "system-1",
		ProviderCode:    "openai",
		ObservedAt:      time.Date(2026, 7, 26, 8, 9, 10, 123456789, time.UTC),
		RunID:           "run-1",
		Model:           "gpt-5",
		Profile:         modelquality.ProfileQuick,
		Score:           0,
		Threshold:       70,
		Level:           modelquality.LevelUnavailable,
		ErrorCode:       "transport_error",
		ErrorMessage:    "upstream unavailable",
		UpdatedAt:       time.Date(2026, 7, 26, 8, 10, 0, 0, time.UTC),
	}
}

type modelQualityHealthExecStub struct {
	tag   pgconn.CommandTag
	err   error
	query string
	args  []any
	calls int
}

func (stub *modelQualityHealthExecStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	stub.calls++
	stub.query = query
	stub.args = append([]any(nil), args...)
	return stub.tag, stub.err
}

var _ modelQualityHealthExecer = (*modelQualityHealthExecStub)(nil)
