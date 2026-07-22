package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAccountDetailQueries interface {
	GetManagementAccountDetailSource(
		ctx context.Context,
		arg postgresqueries.GetManagementAccountDetailSourceParams,
	) (postgresqueries.GetManagementAccountDetailSourceRow, error)
	ListManagementAccountAPIKeyRuntimeStates(
		ctx context.Context,
		accountID string,
	) ([]postgresqueries.ListManagementAccountAPIKeyRuntimeStatesRow, error)
}

func (s *Store) GetManagementAccountDetailSource(
	ctx context.Context,
	input port.ManagementAccountDetailInput,
) (port.ManagementAccountDetailSource, bool, error) {
	return getManagementAccountDetailSource(ctx, s.queries(), input)
}

func getManagementAccountDetailSource(
	ctx context.Context,
	q managementAccountDetailQueries,
	input port.ManagementAccountDetailInput,
) (port.ManagementAccountDetailSource, bool, error) {
	row, err := q.GetManagementAccountDetailSource(ctx, postgresqueries.GetManagementAccountDetailSourceParams{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountDetailSource{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountDetailSource{}, false, fmt.Errorf("get management account detail source: %w", err)
	}
	return port.ManagementAccountDetailSource{
		ID:                    row.ID,
		SourceAccountID:       row.SourceAccountID,
		AccessType:            row.AccessType,
		ProviderCode:          row.ProviderCode,
		ProtocolCode:          row.ProtocolCode,
		ProtocolVersion:       row.ProtocolVersion,
		Type:                  row.Type,
		ConfigRevision:        int(row.ConfigRevision),
		CredentialsEncrypted:  row.CredentialsEncrypted,
		HasActiveManualSource: row.HasActiveManualSource,
		DetailJSON:            row.DetailJson,
		ProxyProfileID:        nullableText(row.ProxyProfileID),
		ProxyProfileName:      nullableText(row.ProxyProfileName),
		ProxyProfileType:      nullableText(row.ProxyProfileType),
		ProxyProfileEnabled:   nullableBool(row.ProxyProfileEnabled),
	}, true, nil
}

func (s *Store) ListManagementAccountAPIKeyRuntimeStates(
	ctx context.Context,
	accountID string,
) ([]port.ManagementAccountAPIKeyRuntimeState, error) {
	return listManagementAccountAPIKeyRuntimeStates(ctx, s.queries(), accountID)
}

func listManagementAccountAPIKeyRuntimeStates(
	ctx context.Context,
	q managementAccountDetailQueries,
	accountID string,
) ([]port.ManagementAccountAPIKeyRuntimeState, error) {
	rows, err := q.ListManagementAccountAPIKeyRuntimeStates(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return nil, fmt.Errorf("list management account api key runtime states: %w", err)
	}
	items := make([]port.ManagementAccountAPIKeyRuntimeState, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAccountAPIKeyRuntimeState{
			KeyFingerprint:      row.KeyFingerprint,
			KeyIndex:            int(row.KeyIndex),
			Status:              row.Status,
			FailureCount:        int(row.FailureCount),
			ConsecutiveFailures: int(row.ConsecutiveFailures),
			SuccessCount:        row.SuccessCount,
			CooldownUntil:       textValue(row.CooldownUntil),
			NextProbeAt:         textValue(row.NextProbeAt),
			LastAttemptAt:       textValue(row.LastAttemptAt),
			LastSuccessAt:       textValue(row.LastSuccessAt),
			LastFailureAt:       textValue(row.LastFailureAt),
			LastErrorCode:       textValue(row.LastErrorCode),
			LastErrorMessage:    textValue(row.LastErrorMessage),
			LastTraceID:         textValue(row.LastTraceID),
		})
	}
	return items, nil
}

var _ port.ManagementAccountDetailReader = (*Store)(nil)
