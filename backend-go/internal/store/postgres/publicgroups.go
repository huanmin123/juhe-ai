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
	defaultPublicGroupPageSize = 50
	maxPublicGroupPageSize     = 100
)

func (s *Store) PublicGroupInTx(ctx context.Context, fn func(ctx context.Context, store port.PublicGroupStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin public group tx: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	txStore := publicGroupTxStore{queries: s.queries().WithTx(tx)}
	if err := fn(ctx, txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit public group tx: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) FindPublicGroupTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicGroupFindTargetByUsername(ctx, s.queries(), username)
}

func (s *Store) FindPublicGroupTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicGroupFindTargetByID(ctx, s.queries(), id)
}

func (s *Store) CreatePublicGroupTarget(ctx context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	return publicGroupCreateTarget(ctx, s.queries(), input)
}

func (s *Store) ProviderEnabled(ctx context.Context, providerCode string) (bool, bool, error) {
	return publicGroupProviderEnabled(ctx, s.queries(), providerCode)
}

func (s *Store) ListPublicGroups(ctx context.Context, input port.PublicGroupListInput) (port.PublicGroupListPage, error) {
	return publicGroupList(ctx, s.queries(), input)
}

func (s *Store) FindPublicGroupByID(ctx context.Context, groupID string) (port.PublicGroupSummary, bool, error) {
	return publicGroupFindByID(ctx, s.queries(), groupID, false)
}

func (s *Store) FindExistingPublicGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (port.PublicGroupSummary, bool, error) {
	return publicGroupFindExistingByName(ctx, s.queries(), systemAccountID, providerCode, name)
}

func (s *Store) CreatePublicGroup(ctx context.Context, input port.PublicGroupCreateInput) (port.PublicGroupSummary, error) {
	return publicGroupCreate(ctx, s.queries(), input)
}

func (s *Store) UpdatePublicGroup(ctx context.Context, input port.PublicGroupUpdateInput) (port.PublicGroupSummary, bool, error) {
	return publicGroupUpdate(ctx, s.queries(), input)
}

func (s *Store) DeletePublicGroup(ctx context.Context, groupID string, systemAccountID string) (bool, error) {
	return publicGroupDelete(ctx, s.queries(), groupID, systemAccountID)
}

func (s *Store) PublicGroupAccountCount(ctx context.Context, groupID string) (int64, error) {
	return publicGroupAccountCount(ctx, s.queries(), groupID)
}

func (s *Store) PublicGroupActiveRouteStrategyLossCount(ctx context.Context, groupID string) (int64, error) {
	return publicGroupActiveRouteStrategyLossCount(ctx, s.queries(), groupID)
}

type publicGroupTxStore struct {
	queries *postgresqueries.Queries
}

func (s publicGroupTxStore) FindPublicGroupTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicGroupFindTargetByUsername(ctx, s.queries, username)
}

func (s publicGroupTxStore) FindPublicGroupTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicGroupFindTargetByID(ctx, s.queries, id)
}

func (s publicGroupTxStore) CreatePublicGroupTarget(ctx context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	return publicGroupCreateTarget(ctx, s.queries, input)
}

func (s publicGroupTxStore) ProviderEnabled(ctx context.Context, providerCode string) (bool, bool, error) {
	return publicGroupProviderEnabled(ctx, s.queries, providerCode)
}

func (s publicGroupTxStore) ListPublicGroups(ctx context.Context, input port.PublicGroupListInput) (port.PublicGroupListPage, error) {
	return publicGroupList(ctx, s.queries, input)
}

func (s publicGroupTxStore) FindPublicGroupByID(ctx context.Context, groupID string) (port.PublicGroupSummary, bool, error) {
	return publicGroupFindByID(ctx, s.queries, groupID, true)
}

func (s publicGroupTxStore) FindExistingPublicGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (port.PublicGroupSummary, bool, error) {
	return publicGroupFindExistingByName(ctx, s.queries, systemAccountID, providerCode, name)
}

func (s publicGroupTxStore) CreatePublicGroup(ctx context.Context, input port.PublicGroupCreateInput) (port.PublicGroupSummary, error) {
	return publicGroupCreate(ctx, s.queries, input)
}

func (s publicGroupTxStore) UpdatePublicGroup(ctx context.Context, input port.PublicGroupUpdateInput) (port.PublicGroupSummary, bool, error) {
	return publicGroupUpdate(ctx, s.queries, input)
}

func (s publicGroupTxStore) DeletePublicGroup(ctx context.Context, groupID string, systemAccountID string) (bool, error) {
	return publicGroupDelete(ctx, s.queries, groupID, systemAccountID)
}

func (s publicGroupTxStore) PublicGroupAccountCount(ctx context.Context, groupID string) (int64, error) {
	return publicGroupAccountCount(ctx, s.queries, groupID)
}

func (s publicGroupTxStore) PublicGroupActiveRouteStrategyLossCount(ctx context.Context, groupID string) (int64, error) {
	return publicGroupActiveRouteStrategyLossCount(ctx, s.queries, groupID)
}

func publicGroupFindTargetByUsername(ctx context.Context, q *postgresqueries.Queries, username string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicGroupTargetByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public group target by username: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicGroupFindTargetByID(ctx context.Context, q *postgresqueries.Queries, id string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicGroupTargetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public group target by id: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicGroupCreateTarget(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	if err := q.InsertPublicGroupSystemAccount(ctx, postgresqueries.InsertPublicGroupSystemAccountParams{
		ID:           input.ID,
		Username:     input.Username,
		DisplayName:  input.DisplayName,
		Description:  pgText(input.Description),
		PasswordHash: input.PasswordHash,
		CreatedAt:    pgTimestamptz(input.Now),
		UpdatedAt:    pgTimestamptz(input.Now),
	}); err != nil {
		if publicGroupTargetDuplicateUsernameError(err) {
			return port.PublicGroupTarget{}, port.ErrPublicGroupTargetDuplicateUsername
		}
		return port.PublicGroupTarget{}, fmt.Errorf("create public group target: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          input.ID,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      "active",
		Created:     true,
	}, nil
}

func publicGroupProviderEnabled(ctx context.Context, q *postgresqueries.Queries, providerCode string) (bool, bool, error) {
	row, err := q.FindPublicGroupProviderByCode(ctx, providerCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("find public group provider: %w", err)
	}
	return row.Enabled, true, nil
}

func publicGroupList(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupListInput) (port.PublicGroupListPage, error) {
	page := normalizePublicGroupPage(input.Page, input.PageSize)
	pageSize := normalizePublicGroupPageSize(input.PageSize)
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListPublicGroups(ctx, postgresqueries.ListPublicGroupsParams{
		SystemAccountID: input.SystemAccountID,
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		RowOffset:       int32((page - 1) * pageSize),
		RowLimit:        int32(pageSize + 1),
	})
	if err != nil {
		return port.PublicGroupListPage{}, fmt.Errorf("list public groups: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.PublicGroupSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicGroupSummaryFromListRow(row))
	}
	return port.PublicGroupListPage{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: (page-1)*pageSize + len(items) + boolInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

func publicGroupFindExistingByName(ctx context.Context, q *postgresqueries.Queries, systemAccountID string, providerCode string, name string) (port.PublicGroupSummary, bool, error) {
	row, err := q.FindExistingPublicGroupByName(ctx, postgresqueries.FindExistingPublicGroupByNameParams{
		SystemAccountID: systemAccountID,
		ProviderCode:    providerCode,
		Name:            name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupSummary{}, false, nil
	}
	if err != nil {
		return port.PublicGroupSummary{}, false, fmt.Errorf("find existing public group by name: %w", err)
	}
	return publicGroupSummaryFromExistingRow(row), true, nil
}

func publicGroupFindByID(ctx context.Context, q *postgresqueries.Queries, groupID string, forUpdate bool) (port.PublicGroupSummary, bool, error) {
	if forUpdate {
		row, err := q.FindPublicGroupByIDForUpdate(ctx, groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicGroupSummary{}, false, nil
		}
		if err != nil {
			return port.PublicGroupSummary{}, false, fmt.Errorf("find public group by id for update: %w", err)
		}
		return publicGroupSummaryFromForUpdateRow(row), true, nil
	}
	row, err := q.FindPublicGroupByID(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupSummary{}, false, nil
	}
	if err != nil {
		return port.PublicGroupSummary{}, false, fmt.Errorf("find public group by id: %w", err)
	}
	return publicGroupSummaryFromIDRow(row), true, nil
}

func publicGroupCreate(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupCreateInput) (port.PublicGroupSummary, error) {
	row, err := q.InsertPublicGroup(ctx, postgresqueries.InsertPublicGroupParams{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		ProviderCode:    input.ProviderCode,
		Description:     pgTextPtr(input.Description),
		Enabled:         input.Enabled,
		GroupType:       input.GroupType,
		CreatedAt:       pgTimestamptz(input.Now),
		UpdatedAt:       pgTimestamptz(input.Now),
	})
	if err != nil {
		if publicGroupDuplicateNameError(err) {
			return port.PublicGroupSummary{}, port.ErrPublicGroupDuplicateName
		}
		return port.PublicGroupSummary{}, fmt.Errorf("create public group: %w", err)
	}
	return publicGroupSummaryFromInsertRow(row), nil
}

func publicGroupUpdate(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupUpdateInput) (port.PublicGroupSummary, bool, error) {
	row, err := q.UpdatePublicGroupAllFields(ctx, postgresqueries.UpdatePublicGroupAllFieldsParams{
		Name:            input.Name,
		ProviderCode:    input.ProviderCode,
		Description:     pgTextPtr(input.Description),
		Enabled:         input.Enabled,
		GroupType:       input.GroupType,
		UpdatedAt:       pgTimestamptz(input.Now),
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupSummary{}, false, nil
	}
	if err != nil {
		if publicGroupDuplicateNameError(err) {
			return port.PublicGroupSummary{}, false, port.ErrPublicGroupDuplicateName
		}
		return port.PublicGroupSummary{}, false, fmt.Errorf("update public group: %w", err)
	}
	return publicGroupSummaryFromUpdateRow(row), true, nil
}

func publicGroupDelete(ctx context.Context, q *postgresqueries.Queries, groupID string, systemAccountID string) (bool, error) {
	affected, err := q.DeletePublicGroup(ctx, postgresqueries.DeletePublicGroupParams{ID: groupID, SystemAccountID: systemAccountID})
	if err != nil {
		return false, fmt.Errorf("delete public group: %w", err)
	}
	return affected > 0, nil
}

func publicGroupAccountCount(ctx context.Context, q *postgresqueries.Queries, groupID string) (int64, error) {
	count, err := q.CountPublicGroupAccounts(ctx, groupID)
	if err != nil {
		return 0, fmt.Errorf("count public group accounts: %w", err)
	}
	return count, nil
}

func publicGroupActiveRouteStrategyLossCount(ctx context.Context, q *postgresqueries.Queries, groupID string) (int64, error) {
	if _, err := q.LockPublicGroupActiveRouteStrategies(ctx, groupID); err != nil {
		return 0, fmt.Errorf("lock public group active route strategies: %w", err)
	}
	count, err := q.CountPublicGroupActiveRouteStrategyLoss(ctx, groupID)
	if err != nil {
		return 0, fmt.Errorf("count public group active route strategy loss: %w", err)
	}
	return count, nil
}

func publicGroupSummaryFromListRow(row postgresqueries.ListPublicGroupsRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicGroupSummaryFromExistingRow(row postgresqueries.FindExistingPublicGroupByNameRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicGroupSummaryFromIDRow(row postgresqueries.FindPublicGroupByIDRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicGroupSummaryFromForUpdateRow(row postgresqueries.FindPublicGroupByIDForUpdateRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicGroupSummaryFromInsertRow(row postgresqueries.InsertPublicGroupRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func publicGroupSummaryFromUpdateRow(row postgresqueries.UpdatePublicGroupAllFieldsRow) port.PublicGroupSummary {
	return port.PublicGroupSummary{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Description:     textPtr(row.Description),
		Enabled:         row.Enabled,
		IsDefault:       row.IsDefault,
		GroupType:       row.GroupType,
		CreatedAt:       timestamptzValue(row.CreatedAt),
		UpdatedAt:       timestamptzValue(row.UpdatedAt),
	}
}

func normalizePublicGroupPage(page int, pageSize int) int {
	if page < 1 {
		return 1
	}
	return min(page, publicGroupPageUpperBound(normalizePublicGroupPageSize(pageSize)))
}

func normalizePublicGroupPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultPublicGroupPageSize
	}
	return min(pageSize, maxPublicGroupPageSize)
}

func publicGroupPageUpperBound(pageSize int) int {
	return max(1, (1001-1)/max(1, pageSize))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func pgTextPtr(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func timestamptzValue(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] < 0x10ffff {
			runes[i]++
			return string(runes[:i+1])
		}
	}
	return value + string(rune(0x10ffff))
}

func publicGroupDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "idx_groups_owner_provider_name_unique" ||
			pgErr.ConstraintName == "idx_groups_owner_provider_name_unique_lower")
}

func publicGroupTargetDuplicateUsernameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "idx_system_accounts_username_unique_lower"
}

var _ port.PublicGroupStore = (*Store)(nil)
var _ port.PublicGroupTransactor = (*Store)(nil)
var _ port.PublicGroupStore = publicGroupTxStore{}
