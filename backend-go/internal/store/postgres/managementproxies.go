package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func (s *Store) FindManagementProxy(ctx context.Context, id string) (port.ManagementProxySummary, bool, error) {
	return findManagementProxy(ctx, s.queries(), id)
}

func (s *Store) CreateManagementProxy(ctx context.Context, input port.ManagementProxyCreateInput) (port.ManagementProxySummary, error) {
	return createManagementProxy(ctx, s.queries(), input)
}

func (s *Store) UpdateManagementProxy(ctx context.Context, input port.ManagementProxyUpdateInput) (port.ManagementProxySummary, bool, error) {
	return updateManagementProxy(ctx, s.queries(), input)
}

func (s *Store) DeleteManagementProxy(ctx context.Context, id string) (bool, error) {
	rows, err := s.queries().DeleteManagementProxy(ctx, strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("delete management proxy: %w", err)
	}
	return rows > 0, nil
}

func (s *Store) ListManagementProxyAccountBindings(ctx context.Context, input port.ManagementProxyAccountBindingListInput) ([]port.ManagementProxyAccountBinding, error) {
	return listManagementProxyAccountBindings(ctx, s.queries(), input)
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
		items = append(items, managementProxySummaryFromListRow(row))
	}
	return port.ManagementProxyListResult{Items: items}, nil
}

func findManagementProxy(ctx context.Context, q *postgresqueries.Queries, id string) (port.ManagementProxySummary, bool, error) {
	row, err := q.FindManagementProxy(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementProxySummary{}, false, nil
		}
		return port.ManagementProxySummary{}, false, fmt.Errorf("find management proxy: %w", err)
	}
	return managementProxySummaryFromFindRow(row), true, nil
}

func createManagementProxy(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProxyCreateInput,
) (port.ManagementProxySummary, error) {
	row, err := q.CreateManagementProxy(ctx, postgresqueries.CreateManagementProxyParams{
		ID:                input.ID,
		SystemAccountID:   input.SystemAccountID,
		Name:              input.Name,
		Description:       pgTextFromStringPtr(input.Description),
		Type:              input.Type,
		Host:              input.Host,
		Port:              int32(input.Port),
		Username:          pgTextFromStringPtr(input.Username),
		PasswordEncrypted: pgTextFromStringPtr(input.PasswordEncrypted),
		Enabled:           input.Enabled,
		CreatedAt:         pgTimestamptz(input.CreatedAt),
		UpdatedAt:         pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		if managementProxyDuplicateNameError(err) {
			return port.ManagementProxySummary{}, port.ErrManagementProxyNameExists
		}
		return port.ManagementProxySummary{}, fmt.Errorf("create management proxy: %w", err)
	}
	return managementProxySummaryFromCreateRow(row), nil
}

func updateManagementProxy(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProxyUpdateInput,
) (port.ManagementProxySummary, bool, error) {
	row, err := q.UpdateManagementProxy(ctx, postgresqueries.UpdateManagementProxyParams{
		ID:                input.ID,
		Name:              input.Name,
		Description:       pgTextFromStringPtr(input.Description),
		Type:              input.Type,
		Host:              input.Host,
		Port:              int32(input.Port),
		Username:          pgTextFromStringPtr(input.Username),
		PasswordEncrypted: pgTextFromStringPtr(input.PasswordEncrypted),
		Enabled:           input.Enabled,
		ResetTestState:    input.ResetTestState,
		UpdatedAt:         pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementProxySummary{}, false, nil
		}
		if managementProxyDuplicateNameError(err) {
			return port.ManagementProxySummary{}, false, port.ErrManagementProxyNameExists
		}
		return port.ManagementProxySummary{}, false, fmt.Errorf("update management proxy: %w", err)
	}
	return managementProxySummaryFromUpdateRow(row), true, nil
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

func listManagementProxyAccountBindings(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProxyAccountBindingListInput,
) ([]port.ManagementProxyAccountBinding, error) {
	rows, err := q.ListManagementProxyAccountBindings(ctx, postgresqueries.ListManagementProxyAccountBindingsParams{
		ProxyID:  strings.TrimSpace(input.ProxyID),
		RowLimit: int32(max(0, input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management proxy account bindings: %w", err)
	}
	items := make([]port.ManagementProxyAccountBinding, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementProxyAccountBinding{
			ID:   row.ID,
			Name: row.Name,
		})
	}
	return items, nil
}

func managementProxySummaryFromListRow(row postgresqueries.ListManagementProxiesRow) port.ManagementProxySummary {
	return port.ManagementProxySummary{
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
	}
}

func managementProxySummaryFromFindRow(row postgresqueries.FindManagementProxyRow) port.ManagementProxySummary {
	item := port.ManagementProxySummary{
		ID:                row.ID,
		SystemAccountID:   row.SystemAccountID,
		Name:              row.Name,
		Description:       textPtr(row.Description),
		Type:              row.Type,
		Host:              row.Host,
		Port:              int(row.Port),
		Username:          textPtr(row.Username),
		PasswordEncrypted: textPtr(row.PasswordEncrypted),
		Enabled:           row.Enabled,
		TestStatus:        row.TestStatus,
		LatencyMs:         intPtrFromInt4(row.LatencyMs),
		OutboundIP:        textPtr(row.OutboundIp),
		OutboundRegion:    textPtr(row.OutboundRegion),
		LastTestMessage:   textPtr(row.LastTestMessage),
		LastTestedAt:      timePtrFromTimestamptz(row.LastTestedAt),
	}
	return item
}

func managementProxySummaryFromCreateRow(row postgresqueries.CreateManagementProxyRow) port.ManagementProxySummary {
	return port.ManagementProxySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
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
	}
}

func managementProxySummaryFromUpdateRow(row postgresqueries.UpdateManagementProxyRow) port.ManagementProxySummary {
	return port.ManagementProxySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
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
	}
}

func managementProxyDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "idx_proxy_profiles_name_unique" ||
			pgErr.ConstraintName == "idx_proxy_profiles_name_unique_lower")
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
var _ port.ManagementProxyWriter = (*Store)(nil)
