package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultPublicAPIKeyPageSize = 50
	maxPublicAPIKeyPageSize     = 100
)

func (s *Store) PublicAPIKeyInTx(ctx context.Context, fn func(ctx context.Context, store port.PublicAPIKeyStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin public api key tx: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	txStore := publicAPIKeyTxStore{queries: s.queries().WithTx(tx)}
	if err := fn(ctx, txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit public api key tx: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) FindPublicAPIKeyTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicAPIKeyFindTargetByUsername(ctx, s.queries(), username)
}

func (s *Store) FindPublicAPIKeyTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicAPIKeyFindTargetByID(ctx, s.queries(), id)
}

func (s *Store) ListPublicAPIKeys(ctx context.Context, input port.PublicAPIKeyListInput) (port.PublicAPIKeyListPage, error) {
	return publicAPIKeyList(ctx, s.queries(), input)
}

func (s *Store) FindPublicAPIKeyByID(ctx context.Context, apiKeyID string) (port.PublicAPIKeySummary, bool, error) {
	return publicAPIKeyFindByID(ctx, s.queries(), apiKeyID, false)
}

func (s *Store) FindPublicAPIKeyRouteStrategy(ctx context.Context, systemAccountID string, routeStrategyID string) (port.PublicAPIKeyRouteStrategyRef, bool, error) {
	return publicAPIKeyFindRouteStrategy(ctx, s.queries(), systemAccountID, routeStrategyID, false)
}

func (s *Store) CreatePublicAPIKey(ctx context.Context, input port.PublicAPIKeyCreateInput) (port.PublicAPIKeySummary, error) {
	return publicAPIKeyCreate(ctx, s.queries(), input)
}

func (s *Store) UpdatePublicAPIKey(ctx context.Context, input port.PublicAPIKeyUpdateInput) (port.PublicAPIKeySummary, bool, error) {
	return publicAPIKeyUpdate(ctx, s.queries(), input)
}

func (s *Store) DeletePublicAPIKey(ctx context.Context, apiKeyID string, systemAccountID string) (bool, error) {
	return publicAPIKeyDelete(ctx, s.queries(), apiKeyID, systemAccountID)
}

func (s *Store) UpsertPublicAPIKeyRecordCleanupTarget(
	ctx context.Context,
	input port.PublicAPIKeyRecordCleanupTargetInput,
) error {
	return publicAPIKeyUpsertCleanupTarget(ctx, s.queries(), input)
}

type publicAPIKeyTxStore struct {
	queries *postgresqueries.Queries
}

func (s publicAPIKeyTxStore) FindPublicAPIKeyTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicAPIKeyFindTargetByUsername(ctx, s.queries, username)
}

func (s publicAPIKeyTxStore) FindPublicAPIKeyTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicAPIKeyFindTargetByID(ctx, s.queries, id)
}

func (s publicAPIKeyTxStore) ListPublicAPIKeys(ctx context.Context, input port.PublicAPIKeyListInput) (port.PublicAPIKeyListPage, error) {
	return publicAPIKeyList(ctx, s.queries, input)
}

func (s publicAPIKeyTxStore) FindPublicAPIKeyByID(ctx context.Context, apiKeyID string) (port.PublicAPIKeySummary, bool, error) {
	return publicAPIKeyFindByID(ctx, s.queries, apiKeyID, true)
}

func (s publicAPIKeyTxStore) FindPublicAPIKeyRouteStrategy(ctx context.Context, systemAccountID string, routeStrategyID string) (port.PublicAPIKeyRouteStrategyRef, bool, error) {
	return publicAPIKeyFindRouteStrategy(ctx, s.queries, systemAccountID, routeStrategyID, true)
}

func (s publicAPIKeyTxStore) CreatePublicAPIKey(ctx context.Context, input port.PublicAPIKeyCreateInput) (port.PublicAPIKeySummary, error) {
	return publicAPIKeyCreate(ctx, s.queries, input)
}

func (s publicAPIKeyTxStore) UpdatePublicAPIKey(ctx context.Context, input port.PublicAPIKeyUpdateInput) (port.PublicAPIKeySummary, bool, error) {
	return publicAPIKeyUpdate(ctx, s.queries, input)
}

func (s publicAPIKeyTxStore) DeletePublicAPIKey(ctx context.Context, apiKeyID string, systemAccountID string) (bool, error) {
	return publicAPIKeyDelete(ctx, s.queries, apiKeyID, systemAccountID)
}

func (s publicAPIKeyTxStore) UpsertPublicAPIKeyRecordCleanupTarget(
	ctx context.Context,
	input port.PublicAPIKeyRecordCleanupTargetInput,
) error {
	return publicAPIKeyUpsertCleanupTarget(ctx, s.queries, input)
}

func publicAPIKeyFindTargetByUsername(ctx context.Context, q *postgresqueries.Queries, username string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicAPIKeyTargetByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public api key target by username: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicAPIKeyFindTargetByID(ctx context.Context, q *postgresqueries.Queries, id string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicAPIKeyTargetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public api key target by id: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicAPIKeyList(ctx context.Context, q *postgresqueries.Queries, input port.PublicAPIKeyListInput) (port.PublicAPIKeyListPage, error) {
	page := normalizePublicAPIKeyPage(input.Page, input.PageSize)
	pageSize := normalizePublicAPIKeyPageSize(input.PageSize)
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListPublicAPIKeys(ctx, postgresqueries.ListPublicAPIKeysParams{
		SystemAccountID: input.SystemAccountID,
		RouteStrategyID: strings.TrimSpace(input.RouteStrategyID),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		Status:          strings.TrimSpace(input.Status),
		RowOffset:       int32((page - 1) * pageSize),
		RowLimit:        int32(pageSize + 1),
	})
	if err != nil {
		return port.PublicAPIKeyListPage{}, fmt.Errorf("list public api keys: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.PublicAPIKeySummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicAPIKeySummaryFromListRow(row))
	}
	return port.PublicAPIKeyListPage{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: (page-1)*pageSize + len(items) + boolInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

func publicAPIKeyFindByID(ctx context.Context, q *postgresqueries.Queries, apiKeyID string, forUpdate bool) (port.PublicAPIKeySummary, bool, error) {
	if forUpdate {
		row, err := q.FindPublicAPIKeyByIDForUpdate(ctx, apiKeyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicAPIKeySummary{}, false, nil
		}
		if err != nil {
			return port.PublicAPIKeySummary{}, false, fmt.Errorf("find public api key by id for update: %w", err)
		}
		return publicAPIKeySummaryFromForUpdateRow(row), true, nil
	}
	row, err := q.FindPublicAPIKeyByID(ctx, apiKeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAPIKeySummary{}, false, nil
	}
	if err != nil {
		return port.PublicAPIKeySummary{}, false, fmt.Errorf("find public api key by id: %w", err)
	}
	return publicAPIKeySummaryFromIDRow(row), true, nil
}

func publicAPIKeyFindRouteStrategy(ctx context.Context, q *postgresqueries.Queries, systemAccountID string, routeStrategyID string, forUpdate bool) (port.PublicAPIKeyRouteStrategyRef, bool, error) {
	if forUpdate {
		row, err := q.FindPublicAPIKeyRouteStrategyForUpdate(ctx, postgresqueries.FindPublicAPIKeyRouteStrategyForUpdateParams{
			SystemAccountID: systemAccountID,
			RouteStrategyID: routeStrategyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicAPIKeyRouteStrategyRef{}, false, nil
		}
		if err != nil {
			return port.PublicAPIKeyRouteStrategyRef{}, false, fmt.Errorf("find public api key route strategy for update: %w", err)
		}
		return port.PublicAPIKeyRouteStrategyRef{
			ID:              row.ID,
			SystemAccountID: row.SystemAccountID,
			Name:            row.Name,
			Mode:            port.PublicRouteStrategyMode(row.Mode),
			Status:          port.PublicRouteStrategyStatus(row.Status),
		}, true, nil
	}
	row, err := q.FindPublicAPIKeyRouteStrategy(ctx, postgresqueries.FindPublicAPIKeyRouteStrategyParams{
		SystemAccountID: systemAccountID,
		RouteStrategyID: routeStrategyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAPIKeyRouteStrategyRef{}, false, nil
	}
	if err != nil {
		return port.PublicAPIKeyRouteStrategyRef{}, false, fmt.Errorf("find public api key route strategy: %w", err)
	}
	return port.PublicAPIKeyRouteStrategyRef{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		Mode:            port.PublicRouteStrategyMode(row.Mode),
		Status:          port.PublicRouteStrategyStatus(row.Status),
	}, true, nil
}

func publicAPIKeyCreate(ctx context.Context, q *postgresqueries.Queries, input port.PublicAPIKeyCreateInput) (port.PublicAPIKeySummary, error) {
	row, err := q.InsertPublicAPIKey(ctx, postgresqueries.InsertPublicAPIKeyParams{
		ID:                              input.ID,
		SystemAccountID:                 input.SystemAccountID,
		RouteStrategyID:                 input.RouteStrategyID,
		Name:                            input.Name,
		Description:                     pgTextPtr(input.Description),
		KeyHash:                         input.KeyHash,
		KeyPrefix:                       input.KeyPrefix,
		KeySuffix:                       input.KeySuffix,
		Status:                          string(input.Status),
		ExpiresAt:                       pgTimestamptzPtr(input.ExpiresAt),
		QuotaLimitsJson:                 pgTextPtr(input.QuotaLimitsJSON),
		AvailabilityScheduleJson:        pgTextPtr(input.AvailabilityScheduleJSON),
		AvailabilityScheduleNextCheckAt: pgTimestamptzPtr(input.AvailabilityScheduleNextCheckAt),
		CreatedAt:                       pgTimestamptz(input.Now),
		UpdatedAt:                       pgTimestamptz(input.Now),
	})
	if err != nil {
		switch {
		case publicAPIKeyDuplicateNameError(err):
			return port.PublicAPIKeySummary{}, port.ErrPublicAPIKeyDuplicateName
		case publicAPIKeyDuplicateHashError(err):
			return port.PublicAPIKeySummary{}, port.ErrPublicAPIKeyDuplicateHash
		default:
			return port.PublicAPIKeySummary{}, fmt.Errorf("create public api key: %w", err)
		}
	}
	return publicAPIKeySummaryFromInsertRow(row), nil
}

func publicAPIKeyUpdate(ctx context.Context, q *postgresqueries.Queries, input port.PublicAPIKeyUpdateInput) (port.PublicAPIKeySummary, bool, error) {
	row, err := q.UpdatePublicAPIKeyAllFields(ctx, postgresqueries.UpdatePublicAPIKeyAllFieldsParams{
		RouteStrategyID:                 input.RouteStrategyID,
		Name:                            input.Name,
		Description:                     pgTextPtr(input.Description),
		Status:                          string(input.Status),
		ExpiresAt:                       pgTimestamptzPtr(input.ExpiresAt),
		QuotaLimitsJson:                 pgTextPtr(input.QuotaLimitsJSON),
		AvailabilityScheduleJson:        pgTextPtr(input.AvailabilityScheduleJSON),
		AvailabilityScheduleNextCheckAt: pgTimestamptzPtr(input.AvailabilityScheduleNextCheckAt),
		UpdatedAt:                       pgTimestamptz(input.Now),
		ID:                              input.ID,
		SystemAccountID:                 input.SystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAPIKeySummary{}, false, nil
	}
	if err != nil {
		if publicAPIKeyDuplicateNameError(err) {
			return port.PublicAPIKeySummary{}, false, port.ErrPublicAPIKeyDuplicateName
		}
		return port.PublicAPIKeySummary{}, false, fmt.Errorf("update public api key: %w", err)
	}
	return publicAPIKeySummaryFromUpdateRow(row), true, nil
}

func publicAPIKeyDelete(ctx context.Context, q *postgresqueries.Queries, apiKeyID string, systemAccountID string) (bool, error) {
	affected, err := q.DeletePublicAPIKey(ctx, postgresqueries.DeletePublicAPIKeyParams{ID: apiKeyID, SystemAccountID: systemAccountID})
	if err != nil {
		return false, fmt.Errorf("delete public api key: %w", err)
	}
	return affected > 0, nil
}

type publicAPIKeyCleanupTargetQueries interface {
	UpsertAPIKeyRecordCleanupTarget(
		ctx context.Context,
		arg postgresqueries.UpsertAPIKeyRecordCleanupTargetParams,
	) error
}

func publicAPIKeyUpsertCleanupTarget(
	ctx context.Context,
	q publicAPIKeyCleanupTargetQueries,
	input port.PublicAPIKeyRecordCleanupTargetInput,
) error {
	if err := q.UpsertAPIKeyRecordCleanupTarget(
		ctx,
		postgresqueries.UpsertAPIKeyRecordCleanupTargetParams{
			ApiKeyID:        input.APIKeyID,
			SystemAccountID: input.SystemAccountID,
			CreatedAt:       pgTimestamptz(input.Now),
			UpdatedAt:       pgTimestamptz(input.Now),
		},
	); err != nil {
		return fmt.Errorf("upsert public API Key record cleanup target: %w", err)
	}
	return nil
}

func publicAPIKeySummaryFromListRow(row postgresqueries.ListPublicAPIKeysRow) port.PublicAPIKeySummary {
	return port.PublicAPIKeySummary{
		ID:                              row.ID,
		SystemAccountID:                 row.SystemAccountID,
		Name:                            row.Name,
		Description:                     textPtr(row.Description),
		RouteStrategyID:                 row.RouteStrategyID,
		RouteStrategyName:               row.RouteStrategyName,
		RouteStrategyMode:               port.PublicRouteStrategyMode(row.RouteStrategyMode),
		RouteStrategyStatus:             port.PublicRouteStrategyStatus(row.RouteStrategyStatus),
		Status:                          port.PublicAPIKeyStatus(row.Status),
		IsDefault:                       row.IsDefault,
		KeyPrefix:                       row.KeyPrefix,
		KeySuffix:                       row.KeySuffix,
		ExpiresAt:                       timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:                 textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON:        textPtr(row.AvailabilityScheduleJson),
		AvailabilityScheduleNextCheckAt: timestamptzPtr(row.AvailabilityScheduleNextCheckAt),
		LastUsedAt:                      timestamptzPtr(row.LastUsedAt),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}
}

func publicAPIKeySummaryFromIDRow(row postgresqueries.FindPublicAPIKeyByIDRow) port.PublicAPIKeySummary {
	return port.PublicAPIKeySummary{
		ID:                              row.ID,
		SystemAccountID:                 row.SystemAccountID,
		Name:                            row.Name,
		Description:                     textPtr(row.Description),
		RouteStrategyID:                 row.RouteStrategyID,
		RouteStrategyName:               row.RouteStrategyName,
		RouteStrategyMode:               port.PublicRouteStrategyMode(row.RouteStrategyMode),
		RouteStrategyStatus:             port.PublicRouteStrategyStatus(row.RouteStrategyStatus),
		Status:                          port.PublicAPIKeyStatus(row.Status),
		IsDefault:                       row.IsDefault,
		KeyPrefix:                       row.KeyPrefix,
		KeySuffix:                       row.KeySuffix,
		ExpiresAt:                       timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:                 textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON:        textPtr(row.AvailabilityScheduleJson),
		AvailabilityScheduleNextCheckAt: timestamptzPtr(row.AvailabilityScheduleNextCheckAt),
		LastUsedAt:                      timestamptzPtr(row.LastUsedAt),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}
}

func publicAPIKeySummaryFromForUpdateRow(row postgresqueries.FindPublicAPIKeyByIDForUpdateRow) port.PublicAPIKeySummary {
	return port.PublicAPIKeySummary{
		ID:                              row.ID,
		SystemAccountID:                 row.SystemAccountID,
		Name:                            row.Name,
		Description:                     textPtr(row.Description),
		RouteStrategyID:                 row.RouteStrategyID,
		RouteStrategyName:               row.RouteStrategyName,
		RouteStrategyMode:               port.PublicRouteStrategyMode(row.RouteStrategyMode),
		RouteStrategyStatus:             port.PublicRouteStrategyStatus(row.RouteStrategyStatus),
		Status:                          port.PublicAPIKeyStatus(row.Status),
		IsDefault:                       row.IsDefault,
		KeyPrefix:                       row.KeyPrefix,
		KeySuffix:                       row.KeySuffix,
		ExpiresAt:                       timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:                 textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON:        textPtr(row.AvailabilityScheduleJson),
		AvailabilityScheduleNextCheckAt: timestamptzPtr(row.AvailabilityScheduleNextCheckAt),
		LastUsedAt:                      timestamptzPtr(row.LastUsedAt),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}
}

func publicAPIKeySummaryFromInsertRow(row postgresqueries.InsertPublicAPIKeyRow) port.PublicAPIKeySummary {
	return port.PublicAPIKeySummary{
		ID:                              row.ID,
		SystemAccountID:                 row.SystemAccountID,
		Name:                            row.Name,
		Description:                     textPtr(row.Description),
		RouteStrategyID:                 row.RouteStrategyID,
		RouteStrategyName:               row.RouteStrategyName,
		RouteStrategyMode:               port.PublicRouteStrategyMode(row.RouteStrategyMode),
		RouteStrategyStatus:             port.PublicRouteStrategyStatus(row.RouteStrategyStatus),
		Status:                          port.PublicAPIKeyStatus(row.Status),
		IsDefault:                       row.IsDefault,
		KeyPrefix:                       row.KeyPrefix,
		KeySuffix:                       row.KeySuffix,
		ExpiresAt:                       timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:                 textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON:        textPtr(row.AvailabilityScheduleJson),
		AvailabilityScheduleNextCheckAt: timestamptzPtr(row.AvailabilityScheduleNextCheckAt),
		LastUsedAt:                      timestamptzPtr(row.LastUsedAt),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}
}

func publicAPIKeySummaryFromUpdateRow(row postgresqueries.UpdatePublicAPIKeyAllFieldsRow) port.PublicAPIKeySummary {
	return port.PublicAPIKeySummary{
		ID:                              row.ID,
		SystemAccountID:                 row.SystemAccountID,
		Name:                            row.Name,
		Description:                     textPtr(row.Description),
		RouteStrategyID:                 row.RouteStrategyID,
		RouteStrategyName:               row.RouteStrategyName,
		RouteStrategyMode:               port.PublicRouteStrategyMode(row.RouteStrategyMode),
		RouteStrategyStatus:             port.PublicRouteStrategyStatus(row.RouteStrategyStatus),
		Status:                          port.PublicAPIKeyStatus(row.Status),
		IsDefault:                       row.IsDefault,
		KeyPrefix:                       row.KeyPrefix,
		KeySuffix:                       row.KeySuffix,
		ExpiresAt:                       timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:                 textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON:        textPtr(row.AvailabilityScheduleJson),
		AvailabilityScheduleNextCheckAt: timestamptzPtr(row.AvailabilityScheduleNextCheckAt),
		LastUsedAt:                      timestamptzPtr(row.LastUsedAt),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}
}

func normalizePublicAPIKeyPage(page int, pageSize int) int {
	if page < 1 {
		return 1
	}
	return min(page, publicAPIKeyPageUpperBound(normalizePublicAPIKeyPageSize(pageSize)))
}

func normalizePublicAPIKeyPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultPublicAPIKeyPageSize
	}
	return min(pageSize, maxPublicAPIKeyPageSize)
}

func publicAPIKeyPageUpperBound(pageSize int) int {
	return max(1, (1001-1)/max(1, pageSize))
}

func pgTimestamptzPtr(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgTimestamptz(*value)
}

func publicAPIKeyDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "idx_api_keys_owner_name_unique_lower"
}

func publicAPIKeyDuplicateHashError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "idx_api_keys_key_hash_unique"
}

var _ port.PublicAPIKeyStore = (*Store)(nil)
var _ port.PublicAPIKeyTransactor = (*Store)(nil)
var _ port.PublicAPIKeyStore = publicAPIKeyTxStore{}
