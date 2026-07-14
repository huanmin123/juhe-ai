package postgres

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementClientIPStatsListRowLimit = 101
	maxManagementClientIPStatsListOffset   = 999
)

type managementClientIPStatsQueries interface {
	ManagementClientIPStatsRangeReady(
		ctx context.Context,
		arg postgresqueries.ManagementClientIPStatsRangeReadyParams,
	) (bool, error)
	ListManagementClientIPStats(
		ctx context.Context,
		arg postgresqueries.ListManagementClientIPStatsParams,
	) ([]postgresqueries.ListManagementClientIPStatsRow, error)
	ListManagementClientIPStatsRequestCountDesc(
		ctx context.Context,
		arg postgresqueries.ListManagementClientIPStatsRequestCountDescParams,
	) ([]postgresqueries.ListManagementClientIPStatsRequestCountDescRow, error)
}

func (s *Store) ListManagementClientIPStats(
	ctx context.Context,
	input port.ManagementClientIPStatsListInput,
) (port.ManagementClientIPStatsListPage, error) {
	return listManagementClientIPStats(ctx, s.queries(), input)
}

func listManagementClientIPStats(
	ctx context.Context,
	q managementClientIPStatsQueries,
	input port.ManagementClientIPStatsListInput,
) (port.ManagementClientIPStatsListPage, error) {
	ready, err := q.ManagementClientIPStatsRangeReady(
		ctx,
		postgresqueries.ManagementClientIPStatsRangeReadyParams{
			StartDate: input.StartDate,
			EndDate:   input.EndDate,
		},
	)
	if err != nil {
		return port.ManagementClientIPStatsListPage{}, fmt.Errorf(
			"check management client IP stats range readiness: %w",
			err,
		)
	}
	if !ready {
		return port.ManagementClientIPStatsListPage{
			Rows:       []port.ManagementClientIPStatsListRow{},
			RangeReady: false,
		}, nil
	}
	if input.Limit <= 0 {
		return port.ManagementClientIPStatsListPage{
			Rows:       []port.ManagementClientIPStatsListRow{},
			RangeReady: true,
		}, nil
	}

	lastUsedStartAt, lastUsedEndExclusiveAt, hasLastUsedRange, err :=
		managementClientIPStatsLastUsedRange(input)
	if err != nil {
		return port.ManagementClientIPStatsListPage{}, err
	}
	keyword := managementClientIPStatsTrimECMAScriptWhitespace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	limit := min(input.Limit, maxManagementClientIPStatsListRowLimit)
	statusFilter := string(managementClientIPStatsStatus(input.Status))
	sortField := managementClientIPStatsSortField(input.SortField)
	sortOrder := managementClientIPStatsSortOrder(input.SortOrder)
	rowLimit := int32(limit)
	rowOffset := int32(min(max(0, input.Offset), maxManagementClientIPStatsListOffset))
	policyNow := managementClientIPStatsTimeText(input.Now)

	var rows []postgresqueries.ListManagementClientIPStatsRow
	if sortField == port.ManagementClientIPStatsSortRequestCount &&
		sortOrder == port.ManagementClientIPStatsSortDescending {
		requestCountRows, listErr := q.ListManagementClientIPStatsRequestCountDesc(
			ctx,
			postgresqueries.ListManagementClientIPStatsRequestCountDescParams{
				PolicyNow:              policyNow,
				StartDate:              input.StartDate,
				EndDate:                input.EndDate,
				HasLastUsedRange:       hasLastUsedRange,
				LastUsedStartAt:        lastUsedStartAt,
				LastUsedEndExclusiveAt: lastUsedEndExclusiveAt,
				Keyword:                keyword,
				KeywordUpper:           keywordUpper,
				StatusFilter:           statusFilter,
				RowLimit:               rowLimit,
				RowOffset:              rowOffset,
			},
		)
		err = listErr
		if err == nil {
			rows = make([]postgresqueries.ListManagementClientIPStatsRow, len(requestCountRows))
			for index, row := range requestCountRows {
				rows[index] = postgresqueries.ListManagementClientIPStatsRow(row)
			}
		}
	} else {
		rows, err = q.ListManagementClientIPStats(
			ctx,
			postgresqueries.ListManagementClientIPStatsParams{
				PolicyNow:              policyNow,
				StartDate:              input.StartDate,
				EndDate:                input.EndDate,
				HasLastUsedRange:       hasLastUsedRange,
				LastUsedStartAt:        lastUsedStartAt,
				LastUsedEndExclusiveAt: lastUsedEndExclusiveAt,
				Keyword:                keyword,
				KeywordUpper:           keywordUpper,
				StatusFilter:           statusFilter,
				SortField:              string(sortField),
				SortOrder:              string(sortOrder),
				RowLimit:               rowLimit,
				RowOffset:              rowOffset,
			},
		)
	}
	if err != nil {
		return port.ManagementClientIPStatsListPage{}, fmt.Errorf(
			"list management client IP stats: %w",
			err,
		)
	}

	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementClientIPStatsListRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementClientIPStatsRow(row))
	}
	return port.ManagementClientIPStatsListPage{
		Rows:       items,
		HasMore:    hasMore,
		RangeReady: true,
	}, nil
}

func managementClientIPStatsLastUsedRange(
	input port.ManagementClientIPStatsListInput,
) (string, string, bool, error) {
	if input.LastUsedStartAt == nil && input.LastUsedEndExclusiveAt == nil {
		return "", "", false, nil
	}
	if input.LastUsedStartAt == nil || input.LastUsedEndExclusiveAt == nil {
		return "", "", false, fmt.Errorf(
			"management client IP stats last-used range requires both boundaries",
		)
	}
	if !input.LastUsedStartAt.Before(*input.LastUsedEndExclusiveAt) {
		return "", "", false, fmt.Errorf(
			"management client IP stats last-used range must be increasing",
		)
	}
	return managementClientIPStatsTimeText(*input.LastUsedStartAt),
		managementClientIPStatsTimeText(*input.LastUsedEndExclusiveAt),
		true,
		nil
}

func managementClientIPStatsStatus(
	value port.ManagementClientIPStatsStatus,
) port.ManagementClientIPStatsStatus {
	switch value {
	case port.ManagementClientIPStatsStatusNormal,
		port.ManagementClientIPStatsStatusBlacklisted,
		port.ManagementClientIPStatsStatusAllowlisted:
		return value
	default:
		return port.ManagementClientIPStatsStatusAll
	}
}

func managementClientIPStatsSortField(
	value port.ManagementClientIPStatsSortField,
) port.ManagementClientIPStatsSortField {
	switch value {
	case port.ManagementClientIPStatsSortSuccessCount,
		port.ManagementClientIPStatsSortErrorCount,
		port.ManagementClientIPStatsSortErrorRate,
		port.ManagementClientIPStatsSortTotalTokens,
		port.ManagementClientIPStatsSortTotalCost,
		port.ManagementClientIPStatsSortActiveDays,
		port.ManagementClientIPStatsSortLastUsedAt:
		return value
	default:
		return port.ManagementClientIPStatsSortRequestCount
	}
}

func managementClientIPStatsSortOrder(
	value port.ManagementClientIPStatsSortOrder,
) port.ManagementClientIPStatsSortOrder {
	if value == port.ManagementClientIPStatsSortAscending {
		return value
	}
	return port.ManagementClientIPStatsSortDescending
}

func managementClientIPStatsRow(
	row postgresqueries.ListManagementClientIPStatsRow,
) port.ManagementClientIPStatsListRow {
	status := port.ManagementClientIPStatsStatusNormal
	if row.Blacklisted {
		status = port.ManagementClientIPStatsStatusBlacklisted
	} else if row.Allowlisted {
		status = port.ManagementClientIPStatsStatusAllowlisted
	}
	return port.ManagementClientIPStatsListRow{
		IPHash:         row.IpHash,
		AggregateIPKey: row.AggregateIpKey,
		LastSeenAt:     row.RegistryLastSeenAt,
		Status:         status,
		RangeUsage: port.ManagementClientIPUsageSummary{
			RequestCount:        row.RequestCount,
			SuccessCount:        row.SuccessCount,
			ErrorCount:          row.ErrorCount,
			ErrorRate:           row.ErrorRate,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheReadCost:       row.CacheReadCostUsd,
			CacheWriteTokens:    row.CacheWriteTokens,
			CacheWrite1hTokens:  row.CacheWrite1hTokens,
			CacheWriteCost:      row.CacheWriteCostUsd,
			ThinkingTokens:      row.ThinkingTokens,
			InputImageTokens:    row.InputImageTokens,
			OutputImageTokens:   row.OutputImageTokens,
			TotalTokens:         row.TotalTokens,
			TotalCost:           row.TotalCostUsd,
			ActiveDays:          row.ActiveDays,
			AverageDurationMs:   managementClientIPStatsAverage(row.AverageDurationMs, row.DurationMsSum, row.DurationMsCount),
			AverageFirstTokenMs: managementClientIPStatsAverage(row.AverageFirstTokenMs, row.FirstTokenMsSum, row.FirstTokenMsCount),
			MaxDurationMs:       managementClientIPStatsMaxDuration(row.DurationMsMax, row.DurationMsCount),
			LastUsedAt:          textPtr(row.LastUsedAt),
			LastErrorAt:         textPtr(row.LastErrorAt),
		},
	}
}

func managementClientIPStatsAverage(
	stored pgtype.Float8,
	sum int64,
	count int64,
) *float64 {
	var value float64
	if stored.Valid {
		value = stored.Float64
	} else if count > 0 {
		value = float64(sum) / float64(count)
	} else {
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func managementClientIPStatsMaxDuration(maximum int64, count int64) *int64 {
	if count <= 0 || maximum <= 0 {
		return nil
	}
	return &maximum
}

func managementClientIPStatsTimeText(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func managementClientIPStatsTrimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
			'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
			'\u2029':
			return true
		default:
			return false
		}
	})
}

var _ port.ManagementClientIPStatsListReader = (*Store)(nil)
