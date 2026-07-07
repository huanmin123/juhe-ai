package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementAccountOptionLimit = 50
	maxManagementAccountOptionLimit     = 50
)

func (s *Store) ListManagementAccountOptions(ctx context.Context, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	return listManagementAccountOptions(ctx, s.queries(), input)
}

func (s *Store) ListManagementAccountTags(ctx context.Context, input port.ManagementAccountTagListInput) ([]port.ManagementAccountTag, error) {
	return listManagementAccountTags(ctx, s.queries(), input)
}

func (s *Store) DeleteManagementAccountTag(ctx context.Context, input port.ManagementAccountTagDeleteInput) (bool, error) {
	return deleteManagementAccountTagInTx(ctx, s, input)
}

func listManagementAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	keyword := normalizeAccountNameSearchText(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	keywordTerms := accountNameSearchQueryTerms(keyword)
	limit := managementAccountOptionLimit(input.Limit)
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := q.ListManagementAccountOptions(ctx, postgresqueries.ListManagementAccountOptionsParams{
		SystemAccountID:   strings.TrimSpace(input.SystemAccountID),
		Ids:               uniqueStrings(input.IDs, 50),
		ProviderCode:      strings.TrimSpace(input.ProviderCode),
		GroupID:           strings.TrimSpace(input.GroupID),
		TagIds:            uniqueStrings(input.TagIDs, 100),
		AccountType:       strings.TrimSpace(input.Type),
		Statuses:          uniqueStrings(input.Statuses, 20),
		Schedulable:       strings.TrimSpace(input.Schedulable),
		HasKeyword:        keyword != "",
		Keyword:           keyword,
		KeywordUpper:      keywordUpper,
		KeywordTerms:      keywordTerms,
		KeywordNormalized: keyword,
		KeywordTermCount:  int32(len(keywordTerms)),
		RowLimit:          int32(limit),
		RowOffset:         int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list management account options: %w", err)
	}
	options := make([]port.ManagementAccountOption, 0, len(rows))
	for _, row := range rows {
		accessType := accountOptionAccessType(row.AccessType)
		option := port.ManagementAccountOption{
			ID:                                   row.ID,
			OwnerSystemAccountID:                 row.OwnerSystemAccountID,
			OwnerSystemAccountName:               row.OwnerSystemAccountName,
			ProviderCode:                         row.ProviderCode,
			ProviderProtocolProfileID:            row.ProviderProtocolProfileID,
			ProtocolCode:                         row.ProtocolCode,
			ProtocolVersion:                      row.ProtocolVersion,
			Name:                                 row.Name,
			Type:                                 row.Type,
			Status:                               row.Status,
			AccessType:                           accessType,
			AccountAuthorizationID:               textValue(row.AccountAuthorizationID),
			AuthorizationStatus:                  textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt:               timestamptzPtr(row.AuthorizationExpiresAt),
			AuthorizationInstanceSourceAccountID: textValue(row.AuthorizationInstanceSourceAccountID),
			AuthorizationInstanceOwnerSystemAccountID: textValue(row.AuthorizationInstanceOwnerSystemAccountID),
			AccountExpiresAt: timestamptzPtr(row.AccountExpiresAt),
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = row.SystemAccountName
		}
		if accessType != "authorized" && !input.IncludeSystemAccountFields {
			option.OwnerSystemAccountName = ""
		}
		options = append(options, option)
	}
	return options, nil
}

func listManagementAccountTags(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAccountTagListInput) ([]port.ManagementAccountTag, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return nil, fmt.Errorf("management account tag system account id is required")
	}
	rows, err := q.ListManagementAccountTags(ctx, systemAccountID)
	if err != nil {
		return nil, fmt.Errorf("list management account tags: %w", err)
	}
	tags := make([]port.ManagementAccountTag, 0, len(rows))
	for _, row := range rows {
		accountCount := int(row.AccountCount)
		if accountCount < 0 {
			accountCount = 0
		}
		tags = append(tags, port.ManagementAccountTag{
			ID:           row.ID,
			Name:         row.Name,
			AccountCount: accountCount,
			CreatedAt:    timestamptzValue(row.CreatedAt),
			UpdatedAt:    timestamptzValue(row.UpdatedAt),
		})
	}
	return tags, nil
}

func deleteManagementAccountTagInTx(ctx context.Context, s *Store, input port.ManagementAccountTagDeleteInput) (bool, error) {
	tagID := strings.TrimSpace(input.TagID)
	if tagID == "" {
		return false, nil
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return false, fmt.Errorf("management account tag system account id is required")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin management account tag delete tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	q := s.queries().WithTx(tx)
	_, err = q.LockManagementAccountTagForDelete(ctx, postgresqueries.LockManagementAccountTagForDeleteParams{
		TagID:           tagID,
		SystemAccountID: systemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock management account tag: %w", err)
	}
	inUse, err := q.ManagementAccountTagHasActiveBindings(ctx, postgresqueries.ManagementAccountTagHasActiveBindingsParams{
		TagID:           tagID,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return false, fmt.Errorf("check management account tag bindings: %w", err)
	}
	if inUse {
		return false, port.ErrManagementAccountTagInUse
	}
	rows, err := q.DeleteManagementAccountTag(ctx, postgresqueries.DeleteManagementAccountTagParams{
		TagID:           tagID,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return false, fmt.Errorf("delete management account tag: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return false, fmt.Errorf("commit management account tag delete tx rolled back: %w", err)
		}
		return false, fmt.Errorf("commit management account tag delete tx: %w", err)
	}
	committed = true
	return rows > 0, nil
}

func managementAccountOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementAccountOptionLimit
	}
	return min(limit, maxManagementAccountOptionLimit)
}

const (
	maxAccountNameSearchLength     = 128
	accountNameSearchMaxGramLength = 3
)

func normalizeAccountNameSearchText(value string) string {
	return strings.TrimSpace(norm.NFKC.String(value))
}

func accountNameSearchQueryTerms(keyword string) []string {
	normalized := normalizeAccountNameSearchText(keyword)
	if normalized == "" {
		return nil
	}
	chars := []rune(normalized)
	if len(chars) > maxAccountNameSearchLength {
		return nil
	}
	gramLength := min(accountNameSearchMaxGramLength, len(chars))
	return accountNameSearchGrams(normalized, gramLength)
}

func accountNameSearchGrams(value string, gramLength int) []string {
	chars := []rune(value)
	if gramLength <= 0 || len(chars) < gramLength {
		return nil
	}
	seen := make(map[string]struct{}, len(chars))
	terms := make([]string, 0, len(chars))
	for index := 0; index+gramLength <= len(chars); index++ {
		term := string(chars[index : index+gramLength])
		if strings.TrimSpace(term) == "" {
			continue
		}
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

func accountOptionAccessType(value string) string {
	if strings.TrimSpace(value) == "authorized" {
		return "authorized"
	}
	return "owner"
}

var _ port.ManagementAccountOptionReader = (*Store)(nil)
