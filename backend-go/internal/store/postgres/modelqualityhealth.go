package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/timezonecompat"
)

const modelQualityHealthMaximumMessageBytes = 64 << 10

type modelQualityHealthExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type preparedModelQualityHealthFailure struct {
	input    port.ModelQualityHealthFailureInput
	statHour string
}

func (s *Store) RecordModelQualityHealthFailure(
	ctx context.Context,
	input port.ModelQualityHealthFailureInput,
) (port.ModelQualityHealthFailureResult, error) {
	timezone, found, err := s.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return port.ModelQualityHealthFailureResult{}, fmt.Errorf("read model quality health usageStatsTimezone: %w", err)
	}
	if !found {
		return port.ModelQualityHealthFailureResult{}, fmt.Errorf("model quality health usageStatsTimezone is missing")
	}
	location, err := timezonecompat.LoadNodeLocation(timezone)
	if err != nil {
		return port.ModelQualityHealthFailureResult{}, fmt.Errorf("model quality health usageStatsTimezone is invalid: %w", err)
	}
	prepared, err := prepareModelQualityHealthFailure(input, location)
	if err != nil {
		return port.ModelQualityHealthFailureResult{}, err
	}
	return recordModelQualityHealthFailure(ctx, s.pool, prepared)
}

func prepareModelQualityHealthFailure(
	input port.ModelQualityHealthFailureInput,
	location *time.Location,
) (preparedModelQualityHealthFailure, error) {
	if location == nil || !validModelQualityScheduleText(input.AccountID, 256) ||
		!validModelQualityScheduleText(input.SystemAccountID, 256) ||
		!validModelQualityScheduleText(input.ProviderCode, 256) ||
		!validModelQualityScheduleText(input.RunID, 256) ||
		!validModelQualityScheduleText(input.Model, 512) ||
		(input.Profile != modelquality.ProfileQuick && input.Profile != modelquality.ProfileFull) ||
		!validModelQualityHealthLevel(input.Level) || input.Score < 0 || input.Score > 100 ||
		input.Threshold < 40 || input.Threshold > 100 || input.ObservedAt.IsZero() ||
		input.UpdatedAt.IsZero() || !validModelQualityHealthOptionalCode(input.ErrorCode) ||
		!utf8.ValidString(input.ErrorMessage) || len(input.ErrorMessage) > modelQualityHealthMaximumMessageBytes ||
		strings.IndexByte(input.ErrorMessage, 0) >= 0 {
		return preparedModelQualityHealthFailure{}, fmt.Errorf("model quality health failure input is invalid")
	}

	observedAt := input.ObservedAt.UTC().Truncate(time.Millisecond)
	updatedAt := input.UpdatedAt.UTC().Truncate(time.Millisecond)
	if !validModelQualityHealthYear(observedAt.Year()) || !validModelQualityHealthYear(updatedAt.Year()) {
		return preparedModelQualityHealthFailure{}, fmt.Errorf("model quality health failure time is outside the supported range")
	}
	localObservedAt := observedAt.In(location)
	if !validModelQualityHealthYear(localObservedAt.Year()) {
		return preparedModelQualityHealthFailure{}, fmt.Errorf("model quality health local time is outside the supported range")
	}
	input.ObservedAt = observedAt
	input.UpdatedAt = updatedAt
	input.ErrorMessage = truncateModelQualityTextRunes(input.ErrorMessage, 1000)
	return preparedModelQualityHealthFailure{
		input:    input,
		statHour: modelQualityHealthStatHour(localObservedAt),
	}, nil
}

func recordModelQualityHealthFailure(
	ctx context.Context,
	execer modelQualityHealthExecer,
	prepared preparedModelQualityHealthFailure,
) (port.ModelQualityHealthFailureResult, error) {
	if execer == nil {
		return port.ModelQualityHealthFailureResult{}, fmt.Errorf("model quality health failure execer is required")
	}
	input := prepared.input
	command, err := execer.Exec(ctx, upsertModelQualityHealthFailureSQL,
		input.AccountID,
		input.SystemAccountID,
		input.ProviderCode,
		prepared.statHour,
		modelQualityPolicyTimeText(input.ObservedAt),
		input.RunID,
		input.Model,
		string(input.Profile),
		input.Score,
		input.Threshold,
		string(input.Level),
		modelQualityHealthOptionalText(input.ErrorCode),
		modelQualityHealthOptionalText(input.ErrorMessage),
		modelQualityPolicyTimeText(input.UpdatedAt),
	)
	if err != nil {
		return port.ModelQualityHealthFailureResult{}, fmt.Errorf("upsert model quality health failure: %w", err)
	}
	return port.ModelQualityHealthFailureResult{
		Applied:  command.RowsAffected() == 1,
		StatHour: prepared.statHour,
	}, nil
}

func validModelQualityHealthLevel(level modelquality.Level) bool {
	return level == modelquality.LevelHighConfidence || level == modelquality.LevelLikely ||
		level == modelquality.LevelUncertain || level == modelquality.LevelSuspicious ||
		level == modelquality.LevelUnavailable
}

func validModelQualityHealthOptionalCode(value string) bool {
	return value == "" || validModelQualityScheduleText(value, 256)
}

func validModelQualityHealthYear(year int) bool {
	return year >= 1 && year <= 9999
}

func modelQualityHealthStatHour(value time.Time) string {
	year := value.Year()
	month := int(value.Month())
	day := value.Day()
	hour := value.Hour()
	var result [13]byte
	result[0] = byte(year/1000) + '0'
	result[1] = byte(year/100%10) + '0'
	result[2] = byte(year/10%10) + '0'
	result[3] = byte(year%10) + '0'
	result[4] = '-'
	result[5] = byte(month/10) + '0'
	result[6] = byte(month%10) + '0'
	result[7] = '-'
	result[8] = byte(day/10) + '0'
	result[9] = byte(day%10) + '0'
	result[10] = 'T'
	result[11] = byte(hour/10) + '0'
	result[12] = byte(hour%10) + '0'
	return string(result[:])
}

func modelQualityHealthOptionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func truncateModelQualityTextRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	count := 0
	for byteIndex := range value {
		if count == maximum {
			return strings.Clone(value[:byteIndex])
		}
		count++
	}
	return value
}

var _ port.ModelQualityHealthFailureWriter = (*Store)(nil)
