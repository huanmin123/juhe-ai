package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementExternalIntegrationSourceListRows = 101
	maxManagementExternalIntegrationSourceIDs      = 100
)

type managementExternalIntegrationSourceListQueries interface {
	ListManagementExternalIntegrationSources(
		ctx context.Context,
		arg postgresqueries.ListManagementExternalIntegrationSourcesParams,
	) ([]postgresqueries.JuheBusinessExternalIntegrationSource, error)
	ListManagementExternalIntegrationSourceTokenStats(
		ctx context.Context,
		sourceIDs []string,
	) ([]postgresqueries.ListManagementExternalIntegrationSourceTokenStatsRow, error)
	ListManagementExternalIntegrationSourcePrimaryTokens(
		ctx context.Context,
		sourceIDs []string,
	) ([]postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow, error)
}

type managementExternalIntegrationSourceDetailQueries interface {
	FindManagementExternalIntegrationSource(
		ctx context.Context,
		sourceID string,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	ListManagementExternalIntegrationSourceTokens(
		ctx context.Context,
		sourceID string,
	) ([]postgresqueries.ListManagementExternalIntegrationSourceTokensRow, error)
}

func (s *Store) ListManagementExternalIntegrationSources(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceListInput,
) ([]port.ManagementExternalIntegrationSourceListRow, error) {
	return listManagementExternalIntegrationSources(ctx, s.queries(), input)
}

func (s *Store) ListManagementExternalIntegrationSourceTokenStats(
	ctx context.Context,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourceTokenStatsRow, error) {
	return listManagementExternalIntegrationSourceTokenStats(ctx, s.queries(), sourceIDs)
}

func (s *Store) ListManagementExternalIntegrationSourcePrimaryTokens(
	ctx context.Context,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	return listManagementExternalIntegrationSourcePrimaryTokens(ctx, s.queries(), sourceIDs)
}

func (s *Store) FindManagementExternalIntegrationSource(
	ctx context.Context,
	sourceID string,
) (port.ManagementExternalIntegrationSourceListRow, bool, error) {
	return findManagementExternalIntegrationSource(ctx, s.queries(), sourceID)
}

func (s *Store) ListManagementExternalIntegrationSourceTokens(
	ctx context.Context,
	sourceID string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	return listManagementExternalIntegrationSourceTokens(ctx, s.queries(), sourceID)
}

func listManagementExternalIntegrationSources(
	ctx context.Context,
	q managementExternalIntegrationSourceListQueries,
	input port.ManagementExternalIntegrationSourceListInput,
) ([]port.ManagementExternalIntegrationSourceListRow, error) {
	if input.Limit <= 0 {
		return []port.ManagementExternalIntegrationSourceListRow{}, nil
	}
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementExternalIntegrationSources(ctx, postgresqueries.ListManagementExternalIntegrationSourcesParams{
		Status:       strings.TrimSpace(input.Status),
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowOffset:    int32(max(0, input.Offset)),
		RowLimit:     int32(min(input.Limit, maxManagementExternalIntegrationSourceListRows)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management external integration sources: %w", err)
	}

	items := make([]port.ManagementExternalIntegrationSourceListRow, 0, len(rows))
	for _, row := range rows {
		item, err := managementExternalIntegrationSourceRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func findManagementExternalIntegrationSource(
	ctx context.Context,
	q managementExternalIntegrationSourceDetailQueries,
	sourceID string,
) (port.ManagementExternalIntegrationSourceListRow, bool, error) {
	id := strings.TrimSpace(sourceID)
	if id == "" {
		return port.ManagementExternalIntegrationSourceListRow{}, false, nil
	}
	row, err := q.FindManagementExternalIntegrationSource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementExternalIntegrationSourceListRow{}, false, nil
	}
	if err != nil {
		return port.ManagementExternalIntegrationSourceListRow{}, false, fmt.Errorf("find management external integration source: %w", err)
	}
	item, err := managementExternalIntegrationSourceRow(row)
	if err != nil {
		return port.ManagementExternalIntegrationSourceListRow{}, false, err
	}
	return item, true, nil
}

func listManagementExternalIntegrationSourceTokens(
	ctx context.Context,
	q managementExternalIntegrationSourceDetailQueries,
	sourceID string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	id := strings.TrimSpace(sourceID)
	if id == "" {
		return []port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, nil
	}
	rows, err := q.ListManagementExternalIntegrationSourceTokens(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list management external integration source tokens: %w", err)
	}
	items := make([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, 0, len(rows))
	for _, row := range rows {
		createdAt, err := managementExternalIntegrationSourceRequiredTime(row.CreatedAt, row.ID, "created_at")
		if err != nil {
			return nil, err
		}
		updatedAt, err := managementExternalIntegrationSourceRequiredTime(row.UpdatedAt, row.ID, "updated_at")
		if err != nil {
			return nil, err
		}
		items = append(items, port.ManagementExternalIntegrationSourcePrimaryTokenRow{
			SourceRefID: row.SourceRefID,
			ID:          row.ID,
			Name:        row.Name,
			TokenPrefix: row.TokenPrefix,
			TokenSuffix: row.TokenSuffix,
			Status:      row.Status,
			ScopesJSON:  row.ScopesJson,
			ExpiresAt:   timestamptzPtr(row.ExpiresAt),
			LastUsedAt:  timestamptzPtr(row.LastUsedAt),
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			RevokedAt:   timestamptzPtr(row.RevokedAt),
		})
	}
	return items, nil
}

func managementExternalIntegrationSourceRow(
	row postgresqueries.JuheBusinessExternalIntegrationSource,
) (port.ManagementExternalIntegrationSourceListRow, error) {
	createdAt, err := managementExternalIntegrationSourceRequiredTime(row.CreatedAt, row.ID, "created_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourceListRow{}, err
	}
	updatedAt, err := managementExternalIntegrationSourceRequiredTime(row.UpdatedAt, row.ID, "updated_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourceListRow{}, err
	}
	return port.ManagementExternalIntegrationSourceListRow{
		ID:             row.ID,
		Name:           row.Name,
		Status:         row.Status,
		ScopesJSON:     row.ScopesJson,
		RateLimitsJSON: row.RateLimitsJson,
		ExpiresAt:      timestamptzPtr(row.ExpiresAt),
		Notes:          textPtr(row.Notes),
		LastUsedAt:     timestamptzPtr(row.LastUsedAt),
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}

func listManagementExternalIntegrationSourceTokenStats(
	ctx context.Context,
	q managementExternalIntegrationSourceListQueries,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourceTokenStatsRow, error) {
	ids := managementExternalIntegrationSourceIDs(sourceIDs)
	if len(ids) == 0 {
		return []port.ManagementExternalIntegrationSourceTokenStatsRow{}, nil
	}
	rows, err := q.ListManagementExternalIntegrationSourceTokenStats(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management external integration source token stats: %w", err)
	}
	items := make([]port.ManagementExternalIntegrationSourceTokenStatsRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementExternalIntegrationSourceTokenStatsRow{
			SourceRefID:      row.SourceRefID,
			TokenCount:       row.TokenCount,
			ActiveTokenCount: row.ActiveTokenCount,
		})
	}
	return items, nil
}

func listManagementExternalIntegrationSourcePrimaryTokens(
	ctx context.Context,
	q managementExternalIntegrationSourceListQueries,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	ids := managementExternalIntegrationSourceIDs(sourceIDs)
	if len(ids) == 0 {
		return []port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, nil
	}
	rows, err := q.ListManagementExternalIntegrationSourcePrimaryTokens(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management external integration source primary tokens: %w", err)
	}
	items := make([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, 0, len(rows))
	for _, row := range rows {
		createdAt, err := managementExternalIntegrationSourceRequiredTime(row.CreatedAt, row.ID, "created_at")
		if err != nil {
			return nil, err
		}
		updatedAt, err := managementExternalIntegrationSourceRequiredTime(row.UpdatedAt, row.ID, "updated_at")
		if err != nil {
			return nil, err
		}
		items = append(items, port.ManagementExternalIntegrationSourcePrimaryTokenRow{
			SourceRefID: row.SourceRefID,
			ID:          row.ID,
			Name:        row.Name,
			TokenPrefix: row.TokenPrefix,
			TokenSuffix: row.TokenSuffix,
			Status:      row.Status,
			ScopesJSON:  row.ScopesJson,
			ExpiresAt:   timestamptzPtr(row.ExpiresAt),
			LastUsedAt:  timestamptzPtr(row.LastUsedAt),
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			RevokedAt:   timestamptzPtr(row.RevokedAt),
		})
	}
	return items, nil
}

func managementExternalIntegrationSourceIDs(values []string) []string {
	ids := make([]string, 0, min(len(values), maxManagementExternalIntegrationSourceIDs))
	seen := make(map[string]struct{}, min(len(values), maxManagementExternalIntegrationSourceIDs))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) == maxManagementExternalIntegrationSourceIDs {
			break
		}
	}
	return ids
}

func managementExternalIntegrationSourceRequiredTime(
	value pgtype.Timestamptz,
	id string,
	field string,
) (time.Time, error) {
	if !value.Valid || value.Time.IsZero() {
		return time.Time{}, fmt.Errorf("management external integration source row %q has invalid %s", id, field)
	}
	return value.Time.UTC(), nil
}

var _ port.ManagementExternalIntegrationSourceListReader = (*Store)(nil)
var _ port.ManagementExternalIntegrationSourceDetailReader = (*Store)(nil)
