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
	) ([]postgresqueries.ListManagementExternalIntegrationSourcesRow, error)
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

type managementExternalIntegrationSourceTokenSecretQueries interface {
	FindManagementExternalIntegrationSourceTokenSecret(
		ctx context.Context,
		arg postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams,
	) (string, error)
}

func (s *Store) ListManagementExternalIntegrationSources(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceListInput,
) ([]port.ManagementExternalIntegrationSourceListRow, error) {
	return listManagementExternalIntegrationSources(ctx, s.queries(), input)
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

func (s *Store) FindManagementExternalIntegrationSourceTokenSecret(
	ctx context.Context,
	sourceID string,
	tokenID string,
) (string, bool, error) {
	return findManagementExternalIntegrationSourceTokenSecret(ctx, s.queries(), sourceID, tokenID)
}

func findManagementExternalIntegrationSourceTokenSecret(
	ctx context.Context,
	q managementExternalIntegrationSourceTokenSecretQueries,
	sourceID string,
	tokenID string,
) (string, bool, error) {
	sourceID = managementExternalIntegrationSourceTrimECMAScriptWhitespace(sourceID)
	tokenID = managementExternalIntegrationSourceTrimECMAScriptWhitespace(tokenID)
	if sourceID == "" || tokenID == "" {
		return "", false, nil
	}
	encrypted, err := q.FindManagementExternalIntegrationSourceTokenSecret(
		ctx,
		postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams{
			SourceID: sourceID,
			TokenID:  tokenID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find management external integration source token secret: %w", err)
	}
	return encrypted, true, nil
}

func listManagementExternalIntegrationSources(
	ctx context.Context,
	q managementExternalIntegrationSourceListQueries,
	input port.ManagementExternalIntegrationSourceListInput,
) ([]port.ManagementExternalIntegrationSourceListRow, error) {
	if input.Limit <= 0 {
		return []port.ManagementExternalIntegrationSourceListRow{}, nil
	}
	keyword := strings.ToLower(managementExternalIntegrationSourceTrimECMAScriptWhitespace(input.Keyword))
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
		items = append(items, port.ManagementExternalIntegrationSourceListRow{
			ID: row.ID, Name: row.Name, Status: row.Status, ScopesJSON: row.ScopesJson,
			RateLimitsJSON: row.RateLimitsJson, ExpiresAt: timestamptzPtr(row.ExpiresAt),
			Notes: textPtr(row.Notes), LastUsedAt: timestamptzPtr(row.LastUsedAt),
		})
	}
	return items, nil
}

func findManagementExternalIntegrationSource(
	ctx context.Context,
	q managementExternalIntegrationSourceDetailQueries,
	sourceID string,
) (port.ManagementExternalIntegrationSourceListRow, bool, error) {
	id := managementExternalIntegrationSourceTrimECMAScriptWhitespace(sourceID)
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
	id := managementExternalIntegrationSourceTrimECMAScriptWhitespace(sourceID)
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
		items = append(items, port.ManagementExternalIntegrationSourcePrimaryTokenRow{
			SourceRefID: row.SourceRefID,
			ID:          row.ID,
			TokenPrefix: row.TokenPrefix,
			TokenSuffix: row.TokenSuffix,
		})
	}
	return items, nil
}

func managementExternalIntegrationSourceIDs(values []string) []string {
	ids := make([]string, 0, min(len(values), maxManagementExternalIntegrationSourceIDs))
	seen := make(map[string]struct{}, min(len(values), maxManagementExternalIntegrationSourceIDs))
	for _, value := range values {
		id := managementExternalIntegrationSourceTrimECMAScriptWhitespace(value)
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

func managementExternalIntegrationSourceTrimECMAScriptWhitespace(value string) string {
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

var _ port.ManagementExternalIntegrationSourceListReader = (*Store)(nil)
var _ port.ManagementExternalIntegrationSourceDetailReader = (*Store)(nil)
