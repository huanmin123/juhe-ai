package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultPublicRouteStrategyPageSize = 50
	maxPublicRouteStrategyPageSize     = 100
)

func (s *Store) PublicRouteStrategyInTx(ctx context.Context, fn func(ctx context.Context, store port.PublicRouteStrategyStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin public route strategy tx: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	txStore := publicRouteStrategyTxStore{queries: s.queries().WithTx(tx)}
	if err := fn(ctx, txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit public route strategy tx: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) FindPublicRouteStrategyTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicRouteStrategyFindTargetByUsername(ctx, s.queries(), username)
}

func (s *Store) FindPublicRouteStrategyTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicRouteStrategyFindTargetByID(ctx, s.queries(), id)
}

func (s *Store) ListPublicRouteStrategies(ctx context.Context, input port.PublicRouteStrategyListInput) (port.PublicRouteStrategyListPage, error) {
	return publicRouteStrategyList(ctx, s.queries(), input)
}

func (s *Store) FindPublicRouteStrategyByID(ctx context.Context, routeStrategyID string) (port.PublicRouteStrategySummary, bool, error) {
	return publicRouteStrategyFindByID(ctx, s.queries(), routeStrategyID, false)
}

func (s *Store) FindPublicRouteStrategyBindableGroups(ctx context.Context, systemAccountID string, groupIDs []string) ([]port.PublicRouteStrategyBindableGroup, error) {
	return publicRouteStrategyFindBindableGroups(ctx, s.queries(), systemAccountID, groupIDs)
}

func (s *Store) CreatePublicRouteStrategy(ctx context.Context, input port.PublicRouteStrategyCreateInput) (port.PublicRouteStrategySummary, error) {
	return publicRouteStrategyCreate(ctx, s.queries(), input)
}

func (s *Store) UpdatePublicRouteStrategy(ctx context.Context, input port.PublicRouteStrategyUpdateInput) (port.PublicRouteStrategySummary, bool, error) {
	return publicRouteStrategyUpdate(ctx, s.queries(), input)
}

func (s *Store) DeletePublicRouteStrategy(ctx context.Context, routeStrategyID string, systemAccountID string) (bool, error) {
	return publicRouteStrategyDelete(ctx, s.queries(), routeStrategyID, systemAccountID)
}

func (s *Store) PublicRouteStrategyAPIKeyCount(ctx context.Context, routeStrategyID string, systemAccountID string) (int64, error) {
	return publicRouteStrategyAPIKeyCount(ctx, s.queries(), routeStrategyID, systemAccountID)
}

type publicRouteStrategyTxStore struct {
	queries *postgresqueries.Queries
}

func (s publicRouteStrategyTxStore) FindPublicRouteStrategyTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicRouteStrategyFindTargetByUsername(ctx, s.queries, username)
}

func (s publicRouteStrategyTxStore) FindPublicRouteStrategyTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicRouteStrategyFindTargetByID(ctx, s.queries, id)
}

func (s publicRouteStrategyTxStore) ListPublicRouteStrategies(ctx context.Context, input port.PublicRouteStrategyListInput) (port.PublicRouteStrategyListPage, error) {
	return publicRouteStrategyList(ctx, s.queries, input)
}

func (s publicRouteStrategyTxStore) FindPublicRouteStrategyByID(ctx context.Context, routeStrategyID string) (port.PublicRouteStrategySummary, bool, error) {
	return publicRouteStrategyFindByID(ctx, s.queries, routeStrategyID, true)
}

func (s publicRouteStrategyTxStore) FindPublicRouteStrategyBindableGroups(ctx context.Context, systemAccountID string, groupIDs []string) ([]port.PublicRouteStrategyBindableGroup, error) {
	return publicRouteStrategyFindBindableGroups(ctx, s.queries, systemAccountID, groupIDs)
}

func (s publicRouteStrategyTxStore) CreatePublicRouteStrategy(ctx context.Context, input port.PublicRouteStrategyCreateInput) (port.PublicRouteStrategySummary, error) {
	return publicRouteStrategyCreate(ctx, s.queries, input)
}

func (s publicRouteStrategyTxStore) UpdatePublicRouteStrategy(ctx context.Context, input port.PublicRouteStrategyUpdateInput) (port.PublicRouteStrategySummary, bool, error) {
	return publicRouteStrategyUpdate(ctx, s.queries, input)
}

func (s publicRouteStrategyTxStore) DeletePublicRouteStrategy(ctx context.Context, routeStrategyID string, systemAccountID string) (bool, error) {
	return publicRouteStrategyDelete(ctx, s.queries, routeStrategyID, systemAccountID)
}

func (s publicRouteStrategyTxStore) PublicRouteStrategyAPIKeyCount(ctx context.Context, routeStrategyID string, systemAccountID string) (int64, error) {
	return publicRouteStrategyAPIKeyCount(ctx, s.queries, routeStrategyID, systemAccountID)
}

func publicRouteStrategyFindTargetByUsername(ctx context.Context, q *postgresqueries.Queries, username string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicRouteStrategyTargetByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public route strategy target by username: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicRouteStrategyFindTargetByID(ctx context.Context, q *postgresqueries.Queries, id string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicRouteStrategyTargetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public route strategy target by id: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicRouteStrategyList(ctx context.Context, q *postgresqueries.Queries, input port.PublicRouteStrategyListInput) (port.PublicRouteStrategyListPage, error) {
	page := normalizePublicRouteStrategyPage(input.Page, input.PageSize)
	pageSize := normalizePublicRouteStrategyPageSize(input.PageSize)
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListPublicRouteStrategies(ctx, postgresqueries.ListPublicRouteStrategiesParams{
		SystemAccountID: input.SystemAccountID,
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		Mode:            strings.TrimSpace(input.Mode),
		Status:          strings.TrimSpace(input.Status),
		RowOffset:       int32((page - 1) * pageSize),
		RowLimit:        int32(pageSize + 1),
	})
	if err != nil {
		return port.PublicRouteStrategyListPage{}, fmt.Errorf("list public route strategies: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	routeIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		routeIDs = append(routeIDs, row.ID)
	}
	bindings, err := publicRouteStrategyBindingsByStrategyIDs(ctx, q, routeIDs)
	if err != nil {
		return port.PublicRouteStrategyListPage{}, err
	}
	items := make([]port.PublicRouteStrategySummary, 0, len(rows))
	for _, row := range rows {
		summary := publicRouteStrategySummaryFromListRow(row)
		summary.GroupBindings = bindings[row.ID]
		items = append(items, summary)
	}
	return port.PublicRouteStrategyListPage{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: (page-1)*pageSize + len(items) + boolInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

func publicRouteStrategyFindByID(ctx context.Context, q *postgresqueries.Queries, routeStrategyID string, forUpdate bool) (port.PublicRouteStrategySummary, bool, error) {
	if forUpdate {
		row, err := q.FindPublicRouteStrategyByIDForUpdate(ctx, routeStrategyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicRouteStrategySummary{}, false, nil
		}
		if err != nil {
			return port.PublicRouteStrategySummary{}, false, fmt.Errorf("find public route strategy by id for update: %w", err)
		}
		summary := publicRouteStrategySummaryFromForUpdateRow(row)
		if err := publicRouteStrategyAttachBindings(ctx, q, &summary); err != nil {
			return port.PublicRouteStrategySummary{}, false, err
		}
		return summary, true, nil
	}
	row, err := q.FindPublicRouteStrategyByID(ctx, routeStrategyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	if err != nil {
		return port.PublicRouteStrategySummary{}, false, fmt.Errorf("find public route strategy by id: %w", err)
	}
	summary := publicRouteStrategySummaryFromIDRow(row)
	if err := publicRouteStrategyAttachBindings(ctx, q, &summary); err != nil {
		return port.PublicRouteStrategySummary{}, false, err
	}
	return summary, true, nil
}

func publicRouteStrategyFindBindableGroups(ctx context.Context, q *postgresqueries.Queries, systemAccountID string, groupIDs []string) ([]port.PublicRouteStrategyBindableGroup, error) {
	ids := uniqueSortedStrings(groupIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.FindPublicRouteStrategyBindableGroups(ctx, postgresqueries.FindPublicRouteStrategyBindableGroupsParams{
		SystemAccountID: systemAccountID,
		GroupIds:        ids,
	})
	if err != nil {
		return nil, fmt.Errorf("find public route strategy bindable groups: %w", err)
	}
	out := make([]port.PublicRouteStrategyBindableGroup, 0, len(rows))
	for _, row := range rows {
		out = append(out, port.PublicRouteStrategyBindableGroup{
			ID:              row.ID,
			SystemAccountID: row.SystemAccountID,
			Name:            row.Name,
			ProviderCode:    row.ProviderCode,
			Enabled:         row.Enabled,
		})
	}
	return out, nil
}

func publicRouteStrategyCreate(ctx context.Context, q *postgresqueries.Queries, input port.PublicRouteStrategyCreateInput) (port.PublicRouteStrategySummary, error) {
	row, err := q.InsertPublicRouteStrategy(ctx, postgresqueries.InsertPublicRouteStrategyParams{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		Description:     pgTextPtr(input.Description),
		Mode:            string(input.Mode),
		Status:          string(input.Status),
		ConfigJson:      pgTextPtr(input.ConfigJSON),
		CreatedAt:       pgTimestamptz(input.Now),
		UpdatedAt:       pgTimestamptz(input.Now),
	})
	if err != nil {
		if publicRouteStrategyDuplicateNameError(err) {
			return port.PublicRouteStrategySummary{}, port.ErrPublicRouteStrategyDuplicateName
		}
		return port.PublicRouteStrategySummary{}, fmt.Errorf("create public route strategy: %w", err)
	}
	if err := publicRouteStrategyReplaceBindings(ctx, q, input.ID, input.SystemAccountID, input.Bindings, input.Now); err != nil {
		return port.PublicRouteStrategySummary{}, err
	}
	summary := publicRouteStrategySummaryFromInsertRow(row)
	if err := publicRouteStrategyAttachBindings(ctx, q, &summary); err != nil {
		return port.PublicRouteStrategySummary{}, err
	}
	return summary, nil
}

func publicRouteStrategyUpdate(ctx context.Context, q *postgresqueries.Queries, input port.PublicRouteStrategyUpdateInput) (port.PublicRouteStrategySummary, bool, error) {
	row, err := q.UpdatePublicRouteStrategyAllFields(ctx, postgresqueries.UpdatePublicRouteStrategyAllFieldsParams{
		Name:            input.Name,
		Description:     pgTextPtr(input.Description),
		Mode:            string(input.Mode),
		Status:          string(input.Status),
		ConfigJson:      pgTextPtr(input.ConfigJSON),
		UpdatedAt:       pgTimestamptz(input.Now),
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	if err != nil {
		if publicRouteStrategyDuplicateNameError(err) {
			return port.PublicRouteStrategySummary{}, false, port.ErrPublicRouteStrategyDuplicateName
		}
		return port.PublicRouteStrategySummary{}, false, fmt.Errorf("update public route strategy: %w", err)
	}
	if err := publicRouteStrategyReplaceBindings(ctx, q, input.ID, input.SystemAccountID, input.Bindings, input.Now); err != nil {
		return port.PublicRouteStrategySummary{}, false, err
	}
	summary := publicRouteStrategySummaryFromUpdateRow(row)
	if err := publicRouteStrategyAttachBindings(ctx, q, &summary); err != nil {
		return port.PublicRouteStrategySummary{}, false, err
	}
	return summary, true, nil
}

func publicRouteStrategyReplaceBindings(ctx context.Context, q *postgresqueries.Queries, routeStrategyID string, systemAccountID string, bindings []port.PublicRouteStrategyGroupBindingCreateInput, now time.Time) error {
	if err := q.DeletePublicRouteStrategyBindings(ctx, postgresqueries.DeletePublicRouteStrategyBindingsParams{
		RouteStrategyID: routeStrategyID,
		SystemAccountID: systemAccountID,
	}); err != nil {
		return fmt.Errorf("delete public route strategy bindings: %w", err)
	}
	for _, binding := range bindings {
		if err := q.InsertPublicRouteStrategyBinding(ctx, postgresqueries.InsertPublicRouteStrategyBindingParams{
			ID:              binding.ID,
			RouteStrategyID: routeStrategyID,
			SystemAccountID: systemAccountID,
			GroupID:         binding.GroupID,
			Priority:        int32(binding.Priority),
			Weight:          int32(binding.Weight),
			Status:          string(binding.Status),
			CreatedAt:       pgTimestamptz(now),
			UpdatedAt:       pgTimestamptz(now),
		}); err != nil {
			return fmt.Errorf("insert public route strategy binding: %w", err)
		}
	}
	return nil
}

func publicRouteStrategyDelete(ctx context.Context, q *postgresqueries.Queries, routeStrategyID string, systemAccountID string) (bool, error) {
	affected, err := q.DeletePublicRouteStrategy(ctx, postgresqueries.DeletePublicRouteStrategyParams{ID: routeStrategyID, SystemAccountID: systemAccountID})
	if err != nil {
		return false, fmt.Errorf("delete public route strategy: %w", err)
	}
	return affected > 0, nil
}

func publicRouteStrategyAPIKeyCount(ctx context.Context, q *postgresqueries.Queries, routeStrategyID string, systemAccountID string) (int64, error) {
	count, err := q.CountPublicRouteStrategyAPIKeys(ctx, postgresqueries.CountPublicRouteStrategyAPIKeysParams{
		RouteStrategyID: routeStrategyID,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return 0, fmt.Errorf("count public route strategy api keys: %w", err)
	}
	return count, nil
}

func publicRouteStrategyAttachBindings(ctx context.Context, q *postgresqueries.Queries, summary *port.PublicRouteStrategySummary) error {
	bindings, err := publicRouteStrategyBindingsByStrategyIDs(ctx, q, []string{summary.ID})
	if err != nil {
		return err
	}
	summary.GroupBindings = bindings[summary.ID]
	return nil
}

func publicRouteStrategyBindingsByStrategyIDs(ctx context.Context, q *postgresqueries.Queries, routeIDs []string) (map[string][]port.PublicRouteStrategyGroupBindingSummary, error) {
	ids := uniqueSortedStrings(routeIDs)
	out := make(map[string][]port.PublicRouteStrategyGroupBindingSummary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListPublicRouteStrategyBindingsByStrategyIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list public route strategy bindings: %w", err)
	}
	for _, row := range rows {
		out[row.RouteStrategyID] = append(out[row.RouteStrategyID], port.PublicRouteStrategyGroupBindingSummary{
			ID:           row.ID,
			GroupID:      row.GroupID,
			GroupName:    textValue(row.GroupName),
			ProviderCode: textValue(row.ProviderCode),
			Priority:     int(row.Priority),
			Weight:       int(row.Weight),
			Status:       port.PublicRouteStrategyStatus(row.Status),
			GroupEnabled: boolValue(row.GroupEnabled),
		})
	}
	return out, nil
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func boolValue(value pgtype.Bool) bool {
	return value.Valid && value.Bool
}

func publicRouteStrategySummaryFromListRow(row postgresqueries.ListPublicRouteStrategiesRow) port.PublicRouteStrategySummary {
	return port.PublicRouteStrategySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Description:     textPtr(row.Description),
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
		IsDefault:       row.IsDefault,
		ConfigJSON:      textPtr(row.ConfigJson),
		APIKeyCount:     row.ApiKeyCount,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicRouteStrategySummaryFromIDRow(row postgresqueries.FindPublicRouteStrategyByIDRow) port.PublicRouteStrategySummary {
	return port.PublicRouteStrategySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Description:     textPtr(row.Description),
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
		IsDefault:       row.IsDefault,
		ConfigJSON:      textPtr(row.ConfigJson),
		APIKeyCount:     row.ApiKeyCount,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicRouteStrategySummaryFromForUpdateRow(row postgresqueries.FindPublicRouteStrategyByIDForUpdateRow) port.PublicRouteStrategySummary {
	return port.PublicRouteStrategySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Description:     textPtr(row.Description),
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
		IsDefault:       row.IsDefault,
		ConfigJSON:      textPtr(row.ConfigJson),
		APIKeyCount:     row.ApiKeyCount,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicRouteStrategySummaryFromInsertRow(row postgresqueries.InsertPublicRouteStrategyRow) port.PublicRouteStrategySummary {
	return port.PublicRouteStrategySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Description:     textPtr(row.Description),
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
		IsDefault:       row.IsDefault,
		ConfigJSON:      textPtr(row.ConfigJson),
		APIKeyCount:     row.ApiKeyCount,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicRouteStrategySummaryFromUpdateRow(row postgresqueries.UpdatePublicRouteStrategyAllFieldsRow) port.PublicRouteStrategySummary {
	return port.PublicRouteStrategySummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Description:     textPtr(row.Description),
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
		IsDefault:       row.IsDefault,
		ConfigJSON:      textPtr(row.ConfigJson),
		APIKeyCount:     row.ApiKeyCount,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func normalizePublicRouteStrategyPage(page int, pageSize int) int {
	if page < 1 {
		return 1
	}
	return min(page, publicRouteStrategyPageUpperBound(normalizePublicRouteStrategyPageSize(pageSize)))
}

func normalizePublicRouteStrategyPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultPublicRouteStrategyPageSize
	}
	return min(pageSize, maxPublicRouteStrategyPageSize)
}

func publicRouteStrategyPageUpperBound(pageSize int) int {
	return max(1, (1001-1)/max(1, pageSize))
}

func publicRouteStrategyDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "idx_route_strategies_owner_name_unique" ||
			pgErr.ConstraintName == "idx_route_strategies_owner_name_unique_lower")
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

var _ port.PublicRouteStrategyStore = (*Store)(nil)
var _ port.PublicRouteStrategyTransactor = (*Store)(nil)
var _ port.PublicRouteStrategyStore = publicRouteStrategyTxStore{}
