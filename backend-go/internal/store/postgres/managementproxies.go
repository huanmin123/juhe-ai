package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) ListManagementProxies(ctx context.Context, input port.ManagementProxyListInput) (port.ManagementProxyListResult, error) {
	return listManagementProxies(ctx, s.queries(), input)
}

func (s *Store) ListManagementProxyOptions(ctx context.Context, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	return listManagementProxyOptions(ctx, s.queries(), input)
}

func listManagementProxies(ctx context.Context, q *postgresqueries.Queries, input port.ManagementProxyListInput) (port.ManagementProxyListResult, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementProxies(ctx, postgresqueries.ListManagementProxiesParams{
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(normalizeManagementProxyListLimit(input.Limit)),
		RowOffset:    int32(max(0, input.Offset)),
	})
	if err != nil {
		return port.ManagementProxyListResult{}, fmt.Errorf("list management proxies: %w", err)
	}
	items := make([]port.ManagementProxySummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementProxySummary{
			ID:              row.ID,
			Name:            row.Name,
			Description:     textPtr(row.Description),
			Type:            row.Type,
			Host:            row.Host,
			Port:            int(row.Port),
			Username:        textPtr(row.Username),
			Enabled:         row.Enabled,
			TestStatus:      row.TestStatus,
			LatencyMs:       intPtrFromInt4(row.LatencyMs),
			OutboundIP:      textPtr(row.OutboundIp),
			OutboundRegion:  textPtr(row.OutboundRegion),
			LastTestMessage: textPtr(row.LastTestMessage),
			LastTestedAt:    timePtrFromTimestamptz(row.LastTestedAt),
		})
	}
	return port.ManagementProxyListResult{Items: items}, nil
}

func listManagementProxyOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementProxyOptions(ctx, postgresqueries.ListManagementProxyOptionsParams{
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(normalizeManagementProxyOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management proxy options: %w", err)
	}
	items := make([]port.ManagementProxyOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementProxyOption{
			ID:      row.ID,
			Name:    row.Name,
			Type:    row.Type,
			Enabled: row.Enabled,
		})
	}
	return items, nil
}

func normalizeManagementProxyListLimit(value int) int {
	if value <= 0 {
		return 21
	}
	if value > 201 {
		return 201
	}
	return value
}

func normalizeManagementProxyOptionLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 50 {
		return 50
	}
	return value
}

func intPtrFromInt4(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int32)
	return &out
}

var _ port.ManagementProxyReader = (*Store)(nil)
var _ port.ManagementProxyOptionReader = (*Store)(nil)
