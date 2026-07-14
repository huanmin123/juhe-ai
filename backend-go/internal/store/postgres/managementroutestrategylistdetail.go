package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const maxManagementRouteStrategyBindings = 20

type managementRouteStrategyListDetailQueries interface {
	ListManagementRouteStrategies(ctx context.Context, arg postgresqueries.ListManagementRouteStrategiesParams) ([]postgresqueries.ListManagementRouteStrategiesRow, error)
	ListManagementOwnedRouteStrategies(ctx context.Context, arg postgresqueries.ListManagementOwnedRouteStrategiesParams) ([]postgresqueries.ListManagementOwnedRouteStrategiesRow, error)
	ListManagementRouteStrategiesByKeyword(ctx context.Context, arg postgresqueries.ListManagementRouteStrategiesByKeywordParams) ([]postgresqueries.ListManagementRouteStrategiesByKeywordRow, error)
	ListManagementOwnedRouteStrategiesByKeyword(ctx context.Context, arg postgresqueries.ListManagementOwnedRouteStrategiesByKeywordParams) ([]postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow, error)
	ListManagementRouteStrategyListEnrichment(ctx context.Context, arg postgresqueries.ListManagementRouteStrategyListEnrichmentParams) ([]postgresqueries.ListManagementRouteStrategyListEnrichmentRow, error)
	FindManagementRouteStrategyDetail(ctx context.Context, arg postgresqueries.FindManagementRouteStrategyDetailParams) (postgresqueries.FindManagementRouteStrategyDetailRow, error)
	ListManagementRouteStrategyDetailBindings(ctx context.Context, arg postgresqueries.ListManagementRouteStrategyDetailBindingsParams) ([]postgresqueries.ListManagementRouteStrategyDetailBindingsRow, error)
}

type managementRouteStrategyListDBRecord struct {
	ID                string
	SystemAccountID   string
	SystemAccountName string
	Name              string
	Description       pgtype.Text
	Mode              string
	Status            string
	IsDefault         bool
	ConfigJSON        pgtype.Text
	CreatedAt         pgtype.Timestamptz
	UpdatedAt         pgtype.Timestamptz
}

func (s *Store) ListManagementRouteStrategies(
	ctx context.Context,
	input port.ManagementRouteStrategyListInput,
) (port.ManagementRouteStrategyListPage, error) {
	return listManagementRouteStrategies(ctx, s.queries(), input)
}

func (s *Store) ListManagementRouteStrategyListEnrichment(
	ctx context.Context,
	scopes []port.ManagementRouteStrategyScope,
) ([]port.ManagementRouteStrategyListEnrichment, error) {
	return listManagementRouteStrategyListEnrichment(ctx, s.queries(), scopes)
}

func (s *Store) FindManagementRouteStrategyDetail(
	ctx context.Context,
	input port.ManagementRouteStrategyDetailInput,
) (port.ManagementRouteStrategyDetailRow, bool, error) {
	return findManagementRouteStrategyDetail(ctx, s.queries(), input)
}

func listManagementRouteStrategies(
	ctx context.Context,
	q managementRouteStrategyListDetailQueries,
	input port.ManagementRouteStrategyListInput,
) (port.ManagementRouteStrategyListPage, error) {
	if input.Limit <= 0 {
		return port.ManagementRouteStrategyListPage{Rows: []port.ManagementRouteStrategyListRow{}}, nil
	}
	systemAccountID := input.SystemAccountID
	keyword := strings.TrimSpace(input.Keyword)
	mode := strings.TrimSpace(input.Mode)
	status := strings.TrimSpace(input.Status)
	rowOffset := int64(max(0, input.Offset))
	rowLimit := int64(input.Limit)

	var records []managementRouteStrategyListDBRecord
	switch {
	case systemAccountID == "" && keyword == "":
		rows, err := q.ListManagementRouteStrategies(ctx, postgresqueries.ListManagementRouteStrategiesParams{
			Mode:      mode,
			Status:    status,
			RowOffset: rowOffset,
			RowLimit:  rowLimit,
		})
		if err != nil {
			return port.ManagementRouteStrategyListPage{}, fmt.Errorf("list management route strategies: %w", err)
		}
		records = managementRouteStrategyGlobalRecords(rows)
	case systemAccountID != "" && keyword == "":
		rows, err := q.ListManagementOwnedRouteStrategies(ctx, postgresqueries.ListManagementOwnedRouteStrategiesParams{
			SystemAccountID: systemAccountID,
			Mode:            mode,
			Status:          status,
			RowOffset:       rowOffset,
			RowLimit:        rowLimit,
		})
		if err != nil {
			return port.ManagementRouteStrategyListPage{}, fmt.Errorf("list owned management route strategies: %w", err)
		}
		records = managementRouteStrategyOwnerRecords(rows)
	case systemAccountID == "":
		rows, err := q.ListManagementRouteStrategiesByKeyword(ctx, postgresqueries.ListManagementRouteStrategiesByKeywordParams{
			Mode:         mode,
			Status:       status,
			RowOffset:    rowOffset,
			RowLimit:     rowLimit,
			Keyword:      keyword,
			KeywordUpper: textPrefixUpperBound(keyword),
		})
		if err != nil {
			return port.ManagementRouteStrategyListPage{}, fmt.Errorf("list management route strategies by keyword: %w", err)
		}
		records = managementRouteStrategyKeywordRecords(rows)
	default:
		rows, err := q.ListManagementOwnedRouteStrategiesByKeyword(ctx, postgresqueries.ListManagementOwnedRouteStrategiesByKeywordParams{
			Mode:            mode,
			Status:          status,
			RowOffset:       rowOffset,
			RowLimit:        rowLimit,
			SystemAccountID: systemAccountID,
			Keyword:         keyword,
			KeywordUpper:    textPrefixUpperBound(keyword),
		})
		if err != nil {
			return port.ManagementRouteStrategyListPage{}, fmt.Errorf("list owned management route strategies by keyword: %w", err)
		}
		records = managementRouteStrategyOwnerKeywordRecords(rows)
	}

	result := make([]port.ManagementRouteStrategyListRow, 0, len(records))
	for _, record := range records {
		row, err := managementRouteStrategyListRow(record)
		if err != nil {
			return port.ManagementRouteStrategyListPage{}, err
		}
		result = append(result, row)
	}
	return port.ManagementRouteStrategyListPage{
		Rows:    result,
		HasMore: len(result) >= input.Limit,
	}, nil
}

func listManagementRouteStrategyListEnrichment(
	ctx context.Context,
	q managementRouteStrategyListDetailQueries,
	scopes []port.ManagementRouteStrategyScope,
) ([]port.ManagementRouteStrategyListEnrichment, error) {
	scopes = uniqueManagementRouteStrategyScopes(scopes)
	if len(scopes) == 0 {
		return []port.ManagementRouteStrategyListEnrichment{}, nil
	}
	routeStrategyIDs := make([]string, 0, len(scopes))
	systemAccountIDs := make([]string, 0, len(scopes))
	items := make([]port.ManagementRouteStrategyListEnrichment, 0, len(scopes))
	indexByScope := make(map[port.ManagementRouteStrategyScope]int, len(scopes))
	for _, scope := range scopes {
		routeStrategyIDs = append(routeStrategyIDs, scope.ID)
		systemAccountIDs = append(systemAccountIDs, scope.SystemAccountID)
		indexByScope[scope] = len(items)
		items = append(items, port.ManagementRouteStrategyListEnrichment{
			ID:                  scope.ID,
			SystemAccountID:     scope.SystemAccountID,
			GroupBindingPreview: []port.ManagementRouteStrategyGroupBinding{},
		})
	}
	rows, err := q.ListManagementRouteStrategyListEnrichment(ctx, postgresqueries.ListManagementRouteStrategyListEnrichmentParams{
		SystemAccountIds: systemAccountIDs,
		RouteStrategyIds: routeStrategyIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list management route strategy enrichment: %w", err)
	}
	for _, row := range rows {
		scope := port.ManagementRouteStrategyScope{
			ID:              row.RouteStrategyID,
			SystemAccountID: row.SystemAccountID,
		}
		index, exists := indexByScope[scope]
		if !exists {
			return nil, fmt.Errorf("management route strategy enrichment returned unexpected scope %q/%q", scope.SystemAccountID, scope.ID)
		}
		items[index].BindingCount = row.BindingCount
		items[index].APIKeyCount = row.ApiKeyCount
		if !row.BindingID.Valid {
			continue
		}
		if !row.GroupID.Valid || !row.Priority.Valid || !row.Weight.Valid ||
			!row.BindingStatus.Valid || !row.GroupEnabled.Valid {
			return nil, fmt.Errorf("management route strategy binding %q has incomplete preview fields", row.BindingID.String)
		}
		if len(items[index].GroupBindingPreview) >= 3 {
			return nil, fmt.Errorf("management route strategy %q returned more than 3 preview bindings", row.RouteStrategyID)
		}
		items[index].GroupBindingPreview = append(
			items[index].GroupBindingPreview,
			port.ManagementRouteStrategyGroupBinding{
				ID:           row.BindingID.String,
				GroupID:      row.GroupID.String,
				GroupName:    textValue(row.GroupName),
				ProviderCode: textValue(row.ProviderCode),
				Priority:     int(row.Priority.Int32),
				Weight:       int(row.Weight.Int32),
				Status:       row.BindingStatus.String,
				GroupEnabled: row.GroupEnabled.Bool,
			},
		)
	}
	return items, nil
}

func findManagementRouteStrategyDetail(
	ctx context.Context,
	q managementRouteStrategyListDetailQueries,
	input port.ManagementRouteStrategyDetailInput,
) (port.ManagementRouteStrategyDetailRow, bool, error) {
	params := postgresqueries.FindManagementRouteStrategyDetailParams{
		RouteStrategyID: input.RouteStrategyID,
		SystemAccountID: input.SystemAccountID,
	}
	row, err := q.FindManagementRouteStrategyDetail(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementRouteStrategyDetailRow{}, false, nil
	}
	if err != nil {
		return port.ManagementRouteStrategyDetailRow{}, false, fmt.Errorf("find management route strategy detail: %w", err)
	}
	base, err := managementRouteStrategyListRow(managementRouteStrategyListDBRecord{
		ID:                row.ID,
		SystemAccountID:   row.SystemAccountID,
		SystemAccountName: row.SystemAccountName,
		Name:              row.Name,
		Description:       row.Description,
		Mode:              row.Mode,
		Status:            row.Status,
		IsDefault:         row.IsDefault,
		ConfigJSON:        row.ConfigJson,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	})
	if err != nil {
		return port.ManagementRouteStrategyDetailRow{}, false, err
	}
	bindingRows, err := q.ListManagementRouteStrategyDetailBindings(
		ctx,
		postgresqueries.ListManagementRouteStrategyDetailBindingsParams{
			RouteStrategyID: input.RouteStrategyID,
			SystemAccountID: input.SystemAccountID,
		},
	)
	if err != nil {
		return port.ManagementRouteStrategyDetailRow{}, false, fmt.Errorf("list management route strategy detail bindings: %w", err)
	}
	if len(bindingRows) > maxManagementRouteStrategyBindings {
		return port.ManagementRouteStrategyDetailRow{}, false, fmt.Errorf(
			"策略路由 %q 的分组绑定超过 20 条",
			row.ID,
		)
	}
	bindings := make([]port.ManagementRouteStrategyGroupBinding, 0, len(bindingRows))
	for _, binding := range bindingRows {
		bindings = append(bindings, port.ManagementRouteStrategyGroupBinding{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    textValue(binding.GroupName),
			ProviderCode: textValue(binding.ProviderCode),
			Priority:     int(binding.Priority),
			Weight:       int(binding.Weight),
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled,
		})
	}
	return port.ManagementRouteStrategyDetailRow{
		ManagementRouteStrategyListRow: base,
		GroupBindings:                  bindings,
		APIKeyCount:                    row.ApiKeyCount,
	}, true, nil
}

func managementRouteStrategyListRow(record managementRouteStrategyListDBRecord) (port.ManagementRouteStrategyListRow, error) {
	if !record.CreatedAt.Valid || !record.UpdatedAt.Valid {
		return port.ManagementRouteStrategyListRow{}, fmt.Errorf(
			"management route strategy %q has invalid timestamps",
			record.ID,
		)
	}
	return port.ManagementRouteStrategyListRow{
		ID:                record.ID,
		SystemAccountID:   record.SystemAccountID,
		SystemAccountName: record.SystemAccountName,
		Name:              record.Name,
		Description:       textPtr(record.Description),
		Mode:              record.Mode,
		Status:            record.Status,
		IsDefault:         record.IsDefault,
		ConfigJSON:        textPtr(record.ConfigJSON),
		CreatedAt:         record.CreatedAt.Time.UTC(),
		UpdatedAt:         record.UpdatedAt.Time.UTC(),
	}, nil
}

func uniqueManagementRouteStrategyScopes(values []port.ManagementRouteStrategyScope) []port.ManagementRouteStrategyScope {
	result := make([]port.ManagementRouteStrategyScope, 0, len(values))
	seen := make(map[port.ManagementRouteStrategyScope]struct{}, len(values))
	for _, value := range values {
		if value.ID == "" || value.SystemAccountID == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func managementRouteStrategyGlobalRecords(
	rows []postgresqueries.ListManagementRouteStrategiesRow,
) []managementRouteStrategyListDBRecord {
	result := make([]managementRouteStrategyListDBRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementRouteStrategyListDBRecord{
			ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
			Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
			IsDefault: row.IsDefault, ConfigJSON: row.ConfigJson, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result
}

func managementRouteStrategyOwnerRecords(
	rows []postgresqueries.ListManagementOwnedRouteStrategiesRow,
) []managementRouteStrategyListDBRecord {
	result := make([]managementRouteStrategyListDBRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementRouteStrategyListDBRecord{
			ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
			Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
			IsDefault: row.IsDefault, ConfigJSON: row.ConfigJson, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result
}

func managementRouteStrategyKeywordRecords(
	rows []postgresqueries.ListManagementRouteStrategiesByKeywordRow,
) []managementRouteStrategyListDBRecord {
	result := make([]managementRouteStrategyListDBRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementRouteStrategyListDBRecord{
			ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
			Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
			IsDefault: row.IsDefault, ConfigJSON: row.ConfigJson, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result
}

func managementRouteStrategyOwnerKeywordRecords(
	rows []postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow,
) []managementRouteStrategyListDBRecord {
	result := make([]managementRouteStrategyListDBRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementRouteStrategyListDBRecord{
			ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
			Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
			IsDefault: row.IsDefault, ConfigJSON: row.ConfigJson, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result
}

var _ port.ManagementRouteStrategyListReader = (*Store)(nil)
var _ port.ManagementRouteStrategyDetailReader = (*Store)(nil)
