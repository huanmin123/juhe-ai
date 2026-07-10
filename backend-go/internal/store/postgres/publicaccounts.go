package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultPublicAccountPageSize = 50
	maxPublicAccountPageSize     = 100
)

func (s *Store) PublicAccountInTx(ctx context.Context, fn func(ctx context.Context, store port.PublicAccountStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin public account tx: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	txStore := publicAccountTxStore{queries: s.queries().WithTx(tx)}
	if err := fn(ctx, txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit public account tx: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) FindPublicAccountTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicAccountFindTargetByUsername(ctx, s.queries(), username)
}

func (s *Store) FindPublicAccountTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicAccountFindTargetByID(ctx, s.queries(), id)
}

func (s *Store) CreatePublicAccountTarget(ctx context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	return publicAccountCreateTarget(ctx, s.queries(), input)
}

func (s *Store) FindPublicAccountProviderProfile(ctx context.Context, providerCode string, profileID string) (port.PublicAccountProviderProfile, bool, error) {
	return publicAccountFindProviderProfile(ctx, s.queries(), providerCode, profileID)
}

func (s *Store) FindExistingPublicAccountGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, bool, error) {
	return publicAccountFindExistingGroupByName(ctx, s.queries(), systemAccountID, providerCode, name)
}

func (s *Store) CreatePublicAccountGroup(ctx context.Context, input port.PublicGroupCreateInput) (port.PublicAccountGroupRef, error) {
	return publicAccountCreateGroup(ctx, s.queries(), input)
}

func (s *Store) FindPublicAccountGroupByID(ctx context.Context, groupID string) (port.PublicAccountGroupRef, bool, error) {
	return publicAccountFindGroupByID(ctx, s.queries(), groupID)
}

func (s *Store) ListPublicAccounts(ctx context.Context, input port.PublicAccountListInput) (port.PublicAccountListPage, error) {
	return publicAccountList(ctx, s.queries(), input)
}

func (s *Store) FindPublicAccountByID(ctx context.Context, accountID string) (port.PublicAccountSummary, bool, error) {
	return publicAccountFindByID(ctx, s.queries(), accountID, false)
}

func (s *Store) FindExistingPublicAccountByNameInGroup(ctx context.Context, input port.PublicAccountNameLookupInput) (port.PublicAccountSummary, bool, error) {
	return publicAccountFindExistingByNameInGroup(ctx, s.queries(), input)
}

func (s *Store) CreatePublicAccount(ctx context.Context, input port.PublicAccountCreateInput) (port.PublicAccountSummary, error) {
	return publicAccountCreate(ctx, s.queries(), input)
}

func (s *Store) UpdatePublicAccount(ctx context.Context, input port.PublicAccountUpdateInput) (port.PublicAccountSummary, bool, error) {
	return publicAccountUpdate(ctx, s.queries(), input)
}

func (s *Store) DeletePublicAccount(ctx context.Context, accountID string, systemAccountID string, deletedBy string, now time.Time) (bool, error) {
	return publicAccountDelete(ctx, s.queries(), accountID, systemAccountID, deletedBy, now)
}

type publicAccountTxStore struct {
	queries *postgresqueries.Queries
}

func (s publicAccountTxStore) FindPublicAccountTargetByUsername(ctx context.Context, username string) (port.PublicGroupTarget, bool, error) {
	return publicAccountFindTargetByUsername(ctx, s.queries, username)
}

func (s publicAccountTxStore) FindPublicAccountTargetByID(ctx context.Context, id string) (port.PublicGroupTarget, bool, error) {
	return publicAccountFindTargetByID(ctx, s.queries, id)
}

func (s publicAccountTxStore) CreatePublicAccountTarget(ctx context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	return publicAccountCreateTarget(ctx, s.queries, input)
}

func (s publicAccountTxStore) FindPublicAccountProviderProfile(ctx context.Context, providerCode string, profileID string) (port.PublicAccountProviderProfile, bool, error) {
	return publicAccountFindProviderProfile(ctx, s.queries, providerCode, profileID)
}

func (s publicAccountTxStore) FindExistingPublicAccountGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, bool, error) {
	return publicAccountFindExistingGroupByName(ctx, s.queries, systemAccountID, providerCode, name)
}

func (s publicAccountTxStore) CreatePublicAccountGroup(ctx context.Context, input port.PublicGroupCreateInput) (port.PublicAccountGroupRef, error) {
	return publicAccountCreateGroup(ctx, s.queries, input)
}

func (s publicAccountTxStore) FindPublicAccountGroupByID(ctx context.Context, groupID string) (port.PublicAccountGroupRef, bool, error) {
	return publicAccountFindGroupByID(ctx, s.queries, groupID)
}

func (s publicAccountTxStore) ListPublicAccounts(ctx context.Context, input port.PublicAccountListInput) (port.PublicAccountListPage, error) {
	return publicAccountList(ctx, s.queries, input)
}

func (s publicAccountTxStore) FindPublicAccountByID(ctx context.Context, accountID string) (port.PublicAccountSummary, bool, error) {
	return publicAccountFindByID(ctx, s.queries, accountID, true)
}

func (s publicAccountTxStore) FindExistingPublicAccountByNameInGroup(ctx context.Context, input port.PublicAccountNameLookupInput) (port.PublicAccountSummary, bool, error) {
	return publicAccountFindExistingByNameInGroup(ctx, s.queries, input)
}

func (s publicAccountTxStore) CreatePublicAccount(ctx context.Context, input port.PublicAccountCreateInput) (port.PublicAccountSummary, error) {
	return publicAccountCreate(ctx, s.queries, input)
}

func (s publicAccountTxStore) UpdatePublicAccount(ctx context.Context, input port.PublicAccountUpdateInput) (port.PublicAccountSummary, bool, error) {
	return publicAccountUpdate(ctx, s.queries, input)
}

func (s publicAccountTxStore) DeletePublicAccount(ctx context.Context, accountID string, systemAccountID string, deletedBy string, now time.Time) (bool, error) {
	return publicAccountDelete(ctx, s.queries, accountID, systemAccountID, deletedBy, now)
}

func publicAccountFindTargetByUsername(ctx context.Context, q *postgresqueries.Queries, username string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicAccountTargetByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public account target by username: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicAccountFindTargetByID(ctx context.Context, q *postgresqueries.Queries, id string) (port.PublicGroupTarget, bool, error) {
	row, err := q.FindPublicAccountTargetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicGroupTarget{}, false, nil
	}
	if err != nil {
		return port.PublicGroupTarget{}, false, fmt.Errorf("find public account target by id: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Status:      row.Status,
	}, true, nil
}

func publicAccountCreateTarget(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	if err := q.InsertPublicAccountSystemAccount(ctx, postgresqueries.InsertPublicAccountSystemAccountParams{
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
		return port.PublicGroupTarget{}, fmt.Errorf("create public account target: %w", err)
	}
	return port.PublicGroupTarget{
		ID:          input.ID,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      "active",
		Created:     true,
	}, nil
}

func publicAccountFindProviderProfile(ctx context.Context, q *postgresqueries.Queries, providerCode string, profileID string) (port.PublicAccountProviderProfile, bool, error) {
	row, err := q.FindPublicAccountProviderProfile(ctx, postgresqueries.FindPublicAccountProviderProfileParams{
		ProviderCode: strings.TrimSpace(providerCode),
		ProfileID:    strings.TrimSpace(profileID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAccountProviderProfile{}, false, nil
	}
	if err != nil {
		return port.PublicAccountProviderProfile{}, false, fmt.Errorf("find public account provider profile: %w", err)
	}
	profile, err := publicAccountProviderProfileFromRow(row)
	if err != nil {
		return port.PublicAccountProviderProfile{}, false, err
	}
	return profile, true, nil
}

func publicAccountProviderProfileFromRow(row postgresqueries.FindPublicAccountProviderProfileRow) (port.PublicAccountProviderProfile, error) {
	defaultSupportedModels, err := decodeProviderStringArray(row.DefaultSupportedModelsJson, "provider default_supported_models_json")
	if err != nil {
		return port.PublicAccountProviderProfile{}, err
	}
	return port.PublicAccountProviderProfile{
		ID:                     row.ID,
		ProviderCode:           row.ProviderCode,
		Name:                   row.Name,
		Enabled:                row.ProfileEnabled,
		ProviderEnabled:        row.ProviderEnabled,
		ProtocolCode:           row.ProtocolCode,
		ProtocolVersion:        row.ProtocolVersion,
		AccountTypesJSON:       row.AccountTypesJson,
		DefaultSupportedModels: defaultSupportedModels,
	}, nil
}

func publicAccountFindExistingGroupByName(ctx context.Context, q *postgresqueries.Queries, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, bool, error) {
	row, err := q.FindExistingPublicAccountGroupByName(ctx, postgresqueries.FindExistingPublicAccountGroupByNameParams{
		SystemAccountID: systemAccountID,
		ProviderCode:    strings.TrimSpace(providerCode),
		Name:            strings.TrimSpace(name),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAccountGroupRef{}, false, nil
	}
	if err != nil {
		return port.PublicAccountGroupRef{}, false, fmt.Errorf("find existing public account group by name: %w", err)
	}
	return port.PublicAccountGroupRef{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Enabled:         row.Enabled,
		GroupType:       row.GroupType,
	}, true, nil
}

func publicAccountCreateGroup(ctx context.Context, q *postgresqueries.Queries, input port.PublicGroupCreateInput) (port.PublicAccountGroupRef, error) {
	row, err := q.InsertPublicAccountGroup(ctx, postgresqueries.InsertPublicAccountGroupParams{
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
			return port.PublicAccountGroupRef{}, port.ErrPublicGroupDuplicateName
		}
		return port.PublicAccountGroupRef{}, fmt.Errorf("create public account group: %w", err)
	}
	return port.PublicAccountGroupRef{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Enabled:         row.Enabled,
		GroupType:       row.GroupType,
		Created:         true,
	}, nil
}

func publicAccountFindGroupByID(ctx context.Context, q *postgresqueries.Queries, groupID string) (port.PublicAccountGroupRef, bool, error) {
	row, err := q.FindPublicAccountGroupByID(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAccountGroupRef{}, false, nil
	}
	if err != nil {
		return port.PublicAccountGroupRef{}, false, fmt.Errorf("find public account group by id: %w", err)
	}
	return port.PublicAccountGroupRef{
		ID:              row.ID,
		SystemAccountID: row.SystemAccountID,
		Name:            row.Name,
		ProviderCode:    row.ProviderCode,
		Enabled:         row.Enabled,
		GroupType:       row.GroupType,
	}, true, nil
}

func publicAccountList(ctx context.Context, q *postgresqueries.Queries, input port.PublicAccountListInput) (port.PublicAccountListPage, error) {
	page := normalizePublicAccountPage(input.Page, input.PageSize)
	pageSize := normalizePublicAccountPageSize(input.PageSize)
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListPublicAccounts(ctx, postgresqueries.ListPublicAccountsParams{
		SystemAccountID:           input.SystemAccountID,
		ProviderCode:              strings.TrimSpace(input.ProviderCode),
		ProviderProtocolProfileID: strings.TrimSpace(input.ProviderProtocolProfileID),
		GroupID:                   strings.TrimSpace(input.GroupID),
		AccountType:               strings.TrimSpace(input.Type),
		Status:                    strings.TrimSpace(input.Status),
		Schedulable:               strings.TrimSpace(input.Schedulable),
		HasKeyword:                keyword != "",
		Keyword:                   keyword,
		KeywordUpper:              keywordUpper,
		RowOffset:                 int32((page - 1) * pageSize),
		RowLimit:                  int32(pageSize + 1),
	})
	if err != nil {
		return port.PublicAccountListPage{}, fmt.Errorf("list public accounts: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.PublicAccountSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicAccountSummaryFromListRow(row))
	}
	if err := attachPublicAccountSupportedModels(ctx, q, items); err != nil {
		return port.PublicAccountListPage{}, err
	}
	return port.PublicAccountListPage{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: (page-1)*pageSize + len(items) + boolInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

func publicAccountFindByID(ctx context.Context, q *postgresqueries.Queries, accountID string, forUpdate bool) (port.PublicAccountSummary, bool, error) {
	var summary port.PublicAccountSummary
	if forUpdate {
		row, err := q.FindPublicAccountByIDForUpdate(ctx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicAccountSummary{}, false, nil
		}
		if err != nil {
			return port.PublicAccountSummary{}, false, fmt.Errorf("find public account by id for update: %w", err)
		}
		summary = publicAccountSummaryFromForUpdateRow(row)
	} else {
		row, err := q.FindPublicAccountByID(ctx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PublicAccountSummary{}, false, nil
		}
		if err != nil {
			return port.PublicAccountSummary{}, false, fmt.Errorf("find public account by id: %w", err)
		}
		summary = publicAccountSummaryFromIDRow(row)
	}
	items := []port.PublicAccountSummary{summary}
	if err := attachPublicAccountSupportedModels(ctx, q, items); err != nil {
		return port.PublicAccountSummary{}, false, err
	}
	return items[0], true, nil
}

func publicAccountFindExistingByNameInGroup(ctx context.Context, q *postgresqueries.Queries, input port.PublicAccountNameLookupInput) (port.PublicAccountSummary, bool, error) {
	row, err := q.FindExistingPublicAccountByNameInGroup(ctx, postgresqueries.FindExistingPublicAccountByNameInGroupParams{
		SystemAccountID:           input.SystemAccountID,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		GroupID:                   input.GroupID,
		Name:                      input.Name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAccountSummary{}, false, nil
	}
	if err != nil {
		return port.PublicAccountSummary{}, false, fmt.Errorf("find existing public account by name in group: %w", err)
	}
	summary := publicAccountSummaryFromExistingRow(row)
	items := []port.PublicAccountSummary{summary}
	if err := attachPublicAccountSupportedModels(ctx, q, items); err != nil {
		return port.PublicAccountSummary{}, false, err
	}
	return items[0], true, nil
}

func publicAccountCreate(ctx context.Context, q *postgresqueries.Queries, input port.PublicAccountCreateInput) (port.PublicAccountSummary, error) {
	lastErrorMessage := (*string)(nil)
	if input.Status == port.PublicAccountStatusPendingTest {
		message := "账户创建后需测试通过才能参与调度"
		lastErrorMessage = &message
	}
	if _, err := q.InsertPublicAccount(ctx, postgresqueries.InsertPublicAccountParams{
		ID:                        input.ID,
		SystemAccountID:           input.SystemAccountID,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		ProtocolCode:              input.ProtocolCode,
		ProtocolVersion:           input.ProtocolVersion,
		Name:                      input.Name,
		AccountType:               input.Type,
		Status:                    string(input.Status),
		CredentialsEncrypted:      input.CredentialsEncrypted,
		CredentialFingerprint:     pgTextPtr(input.CredentialFingerprint),
		CredentialMask:            input.CredentialMask,
		ConcurrencyLimit:          int32(input.ConcurrencyLimit),
		Priority:                  int32(input.Priority),
		ClientCompatibility:       input.ClientCompatibility,
		Schedulable:               input.Schedulable,
		AvailabilityScheduleJson:  pgTextPtr(input.AvailabilityScheduleJSON),
		Notes:                     pgTextPtr(input.Notes),
		LastErrorMessage:          pgTextPtr(lastErrorMessage),
		CreatedAt:                 pgTimestamptz(input.Now),
		UpdatedAt:                 pgTimestamptz(input.Now),
	}); err != nil {
		if publicAccountDuplicateNameError(err) {
			return port.PublicAccountSummary{}, port.ErrPublicAccountDuplicateName
		}
		return port.PublicAccountSummary{}, fmt.Errorf("insert public account: %w", err)
	}
	if err := q.InsertPublicAccountGroupBinding(ctx, postgresqueries.InsertPublicAccountGroupBindingParams{
		SystemAccountID: input.SystemAccountID,
		GroupID:         input.GroupID,
		AccountID:       input.ID,
		LocalPriority:   int32(input.Priority),
		CreatedAt:       pgTimestamptz(input.Now),
		UpdatedAt:       pgTimestamptz(input.Now),
	}); err != nil {
		return port.PublicAccountSummary{}, fmt.Errorf("insert public account group binding: %w", err)
	}
	if err := replacePublicAccountSupportedModels(ctx, q, input.ID, input.ProviderCode, input.SupportedModels, input.Now); err != nil {
		return port.PublicAccountSummary{}, err
	}
	created, ok, err := publicAccountFindByID(ctx, q, input.ID, false)
	if err != nil {
		return port.PublicAccountSummary{}, err
	}
	if !ok {
		return port.PublicAccountSummary{}, fmt.Errorf("created public account not found: %s", input.ID)
	}
	return created, nil
}

func publicAccountUpdate(ctx context.Context, q *postgresqueries.Queries, input port.PublicAccountUpdateInput) (port.PublicAccountSummary, bool, error) {
	_, err := q.UpdatePublicAccountAllFields(ctx, postgresqueries.UpdatePublicAccountAllFieldsParams{
		Name:                     input.Name,
		Status:                   string(input.Status),
		CredentialsEncrypted:     input.CredentialsEncrypted,
		CredentialFingerprint:    pgTextPtr(input.CredentialFingerprint),
		CredentialMask:           input.CredentialMask,
		ConcurrencyLimit:         int32(input.ConcurrencyLimit),
		Priority:                 int32(input.Priority),
		Schedulable:              input.Schedulable,
		AvailabilityScheduleJson: pgTextPtr(input.AvailabilityScheduleJSON),
		Notes:                    pgTextPtr(input.Notes),
		UpdatedAt:                pgTimestamptz(input.Now),
		ID:                       input.ID,
		SystemAccountID:          input.SystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAccountSummary{}, false, nil
	}
	if err != nil {
		if publicAccountDuplicateNameError(err) {
			return port.PublicAccountSummary{}, false, port.ErrPublicAccountDuplicateName
		}
		return port.PublicAccountSummary{}, false, fmt.Errorf("update public account: %w", err)
	}
	if err := replacePublicAccountSupportedModels(ctx, q, input.ID, input.ProviderCode, input.SupportedModels, input.Now); err != nil {
		return port.PublicAccountSummary{}, false, err
	}
	if err := q.UpdatePublicAccountGroupBindingDispatch(ctx, postgresqueries.UpdatePublicAccountGroupBindingDispatchParams{
		LocalPriority:   int32(input.Priority),
		UpdatedAt:       pgTimestamptz(input.Now),
		AccountID:       input.ID,
		SystemAccountID: input.SystemAccountID,
	}); err != nil {
		return port.PublicAccountSummary{}, false, fmt.Errorf("update public account group binding dispatch: %w", err)
	}
	updated, ok, err := publicAccountFindByID(ctx, q, input.ID, false)
	if err != nil {
		return port.PublicAccountSummary{}, false, err
	}
	return updated, ok, nil
}

func publicAccountDelete(ctx context.Context, q *postgresqueries.Queries, accountID string, systemAccountID string, deletedBy string, now time.Time) (bool, error) {
	affected, err := q.SoftDeletePublicAccount(ctx, postgresqueries.SoftDeletePublicAccountParams{
		DeletedAt:       pgTimestamptz(now),
		DeletedBy:       pgText(deletedBy),
		UpdatedAt:       pgTimestamptz(now),
		ID:              accountID,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return false, fmt.Errorf("soft delete public account: %w", err)
	}
	if affected == 0 {
		return false, nil
	}
	if err := q.DeletePublicAccountGroupBindings(ctx, postgresqueries.DeletePublicAccountGroupBindingsParams{
		AccountID:       accountID,
		SystemAccountID: systemAccountID,
	}); err != nil {
		return false, fmt.Errorf("delete public account group bindings: %w", err)
	}
	return true, nil
}

func replacePublicAccountSupportedModels(ctx context.Context, q *postgresqueries.Queries, accountID string, providerCode string, models []string, now time.Time) error {
	if err := q.DeletePublicAccountSupportedModels(ctx, accountID); err != nil {
		return fmt.Errorf("delete public account supported models: %w", err)
	}
	for _, model := range models {
		if err := q.InsertPublicAccountSupportedModel(ctx, postgresqueries.InsertPublicAccountSupportedModelParams{
			AccountID:    accountID,
			ProviderCode: providerCode,
			Model:        model,
			CreatedAt:    pgTimestamptz(now),
		}); err != nil {
			return fmt.Errorf("insert public account supported model: %w", err)
		}
	}
	return nil
}

func attachPublicAccountSupportedModels(ctx context.Context, q *postgresqueries.Queries, items []port.PublicAccountSummary) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	rows, err := q.ListPublicAccountSupportedModelsByAccountIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("list public account supported models: %w", err)
	}
	byAccount := map[string][]string{}
	for _, row := range rows {
		byAccount[row.AccountID] = append(byAccount[row.AccountID], row.Model)
	}
	for index := range items {
		items[index].SupportedModels = append([]string(nil), byAccount[items[index].ID]...)
	}
	return nil
}

func publicAccountSummaryFromListRow(row postgresqueries.ListPublicAccountsRow) port.PublicAccountSummary {
	return port.PublicAccountSummary{
		ID:                        row.ID,
		SystemAccountID:           row.SystemAccountID,
		Name:                      row.Name,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		Status:                    port.PublicAccountStatus(row.Status),
		CredentialsEncrypted:      row.CredentialsEncrypted,
		CredentialFingerprint:     textPtr(row.CredentialFingerprint),
		CredentialMask:            row.CredentialMask,
		ClientCompatibility:       row.ClientCompatibility,
		Schedulable:               row.Schedulable,
		AvailabilityScheduleJSON:  textPtr(row.AvailabilityScheduleJson),
		ConcurrencyLimit:          int(row.ConcurrencyLimit),
		Priority:                  int(row.Priority),
		Notes:                     textPtr(row.Notes),
		BoundGroupID:              textPtr(row.BoundGroupID),
		BoundGroupName:            textPtr(row.BoundGroupName),
		CreatedAt:                 timestamptzValue(row.CreatedAt),
		UpdatedAt:                 timestamptzValue(row.UpdatedAt),
	}
}

func publicAccountSummaryFromIDRow(row postgresqueries.FindPublicAccountByIDRow) port.PublicAccountSummary {
	return port.PublicAccountSummary{
		ID:                        row.ID,
		SystemAccountID:           row.SystemAccountID,
		Name:                      row.Name,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		Status:                    port.PublicAccountStatus(row.Status),
		CredentialsEncrypted:      row.CredentialsEncrypted,
		CredentialFingerprint:     textPtr(row.CredentialFingerprint),
		CredentialMask:            row.CredentialMask,
		ClientCompatibility:       row.ClientCompatibility,
		Schedulable:               row.Schedulable,
		AvailabilityScheduleJSON:  textPtr(row.AvailabilityScheduleJson),
		ConcurrencyLimit:          int(row.ConcurrencyLimit),
		Priority:                  int(row.Priority),
		Notes:                     textPtr(row.Notes),
		BoundGroupID:              textPtr(row.BoundGroupID),
		BoundGroupName:            textPtr(row.BoundGroupName),
		CreatedAt:                 timestamptzValue(row.CreatedAt),
		UpdatedAt:                 timestamptzValue(row.UpdatedAt),
	}
}

func publicAccountSummaryFromForUpdateRow(row postgresqueries.FindPublicAccountByIDForUpdateRow) port.PublicAccountSummary {
	return port.PublicAccountSummary{
		ID:                        row.ID,
		SystemAccountID:           row.SystemAccountID,
		Name:                      row.Name,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		Status:                    port.PublicAccountStatus(row.Status),
		CredentialsEncrypted:      row.CredentialsEncrypted,
		CredentialFingerprint:     textPtr(row.CredentialFingerprint),
		CredentialMask:            row.CredentialMask,
		ClientCompatibility:       row.ClientCompatibility,
		Schedulable:               row.Schedulable,
		AvailabilityScheduleJSON:  textPtr(row.AvailabilityScheduleJson),
		ConcurrencyLimit:          int(row.ConcurrencyLimit),
		Priority:                  int(row.Priority),
		Notes:                     textPtr(row.Notes),
		BoundGroupID:              textPtr(row.BoundGroupID),
		BoundGroupName:            textPtr(row.BoundGroupName),
		CreatedAt:                 timestamptzValue(row.CreatedAt),
		UpdatedAt:                 timestamptzValue(row.UpdatedAt),
	}
}

func publicAccountSummaryFromExistingRow(row postgresqueries.FindExistingPublicAccountByNameInGroupRow) port.PublicAccountSummary {
	return port.PublicAccountSummary{
		ID:                        row.ID,
		SystemAccountID:           row.SystemAccountID,
		Name:                      row.Name,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		Status:                    port.PublicAccountStatus(row.Status),
		CredentialsEncrypted:      row.CredentialsEncrypted,
		CredentialFingerprint:     textPtr(row.CredentialFingerprint),
		CredentialMask:            row.CredentialMask,
		ClientCompatibility:       row.ClientCompatibility,
		Schedulable:               row.Schedulable,
		AvailabilityScheduleJSON:  textPtr(row.AvailabilityScheduleJson),
		ConcurrencyLimit:          int(row.ConcurrencyLimit),
		Priority:                  int(row.Priority),
		Notes:                     textPtr(row.Notes),
		BoundGroupID:              &row.BoundGroupID,
		BoundGroupName:            &row.BoundGroupName,
		CreatedAt:                 timestamptzValue(row.CreatedAt),
		UpdatedAt:                 timestamptzValue(row.UpdatedAt),
	}
}

func normalizePublicAccountPage(page int, pageSize int) int {
	if page < 1 {
		return 1
	}
	return min(page, publicAccountPageUpperBound(normalizePublicAccountPageSize(pageSize)))
}

func normalizePublicAccountPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultPublicAccountPageSize
	}
	return min(pageSize, maxPublicAccountPageSize)
}

func publicAccountPageUpperBound(pageSize int) int {
	return max(1, (1001-1)/max(1, pageSize))
}

func publicAccountDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "idx_accounts_owner_name_unique_lower"
}

var _ port.PublicAccountStore = (*Store)(nil)
var _ port.PublicAccountTransactor = (*Store)(nil)
var _ port.PublicAccountStore = publicAccountTxStore{}
