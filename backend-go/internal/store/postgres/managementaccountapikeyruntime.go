package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAccountAPIKeyRuntimeSourcesSQL = `
SELECT account.id,
       COALESCE(source.id, account.id),
       COALESCE(source.provider_code, account.provider_code),
       COALESCE(source.protocol_code, account.protocol_code),
       COALESCE(source.protocol_version, account.protocol_version),
       COALESCE(source.type, account.type),
       COALESCE(source.credentials_encrypted, account.credentials_encrypted)
FROM juhe_business.accounts account
LEFT JOIN juhe_business.accounts source
  ON source.id = account.authorization_instance_source_account_id
WHERE account.id = ANY($1::text[])
  AND account.deleted_at IS NULL
  AND (source.id IS NULL OR source.deleted_at IS NULL)
ORDER BY array_position($1::text[], account.id)`

const managementAccountAPIKeyRuntimeStatesSQL = `
SELECT account_id, key_fingerprint, key_index, status,
       next_probe_at, last_failure_at, last_error_code, last_error_message, last_trace_id
FROM juhe_business.account_api_key_runtime_states
WHERE account_id = ANY($1::text[])
ORDER BY account_id, key_index, key_fingerprint`

func (s *Store) ListManagementAccountAPIKeyRuntimeSourcesByAccountIDs(ctx context.Context, accountIDs []string) (map[string]port.ManagementAccountAPIKeyRuntimeSource, error) {
	ids := normalizedRuntimeAccountIDs(accountIDs)
	result := make(map[string]port.ManagementAccountAPIKeyRuntimeSource, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, managementAccountAPIKeyRuntimeSourcesSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("list management account api key runtime sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source port.ManagementAccountAPIKeyRuntimeSource
		if err := rows.Scan(&source.ViewAccountID, &source.SourceAccountID, &source.ProviderCode, &source.ProtocolCode, &source.ProtocolVersion, &source.Type, &source.CredentialsEncrypted); err != nil {
			return nil, fmt.Errorf("scan management account api key runtime source: %w", err)
		}
		result[source.ViewAccountID] = source
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate management account api key runtime sources: %w", err)
	}
	return result, nil
}

func (s *Store) ListManagementAccountAPIKeyRuntimeStatesByAccountIDs(ctx context.Context, accountIDs []string) (map[string][]port.ManagementAccountAPIKeyRuntimeState, error) {
	ids := normalizedRuntimeAccountIDs(accountIDs)
	result := make(map[string][]port.ManagementAccountAPIKeyRuntimeState, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, managementAccountAPIKeyRuntimeStatesSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("list management account api key runtime states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var state port.ManagementAccountAPIKeyRuntimeState
		var nextProbeAt, lastFailureAt, lastErrorCode, lastErrorMessage, lastTraceID pgtype.Text
		if err := rows.Scan(&accountID, &state.KeyFingerprint, &state.KeyIndex, &state.Status, &nextProbeAt, &lastFailureAt, &lastErrorCode, &lastErrorMessage, &lastTraceID); err != nil {
			return nil, fmt.Errorf("scan management account api key runtime state: %w", err)
		}
		state.NextProbeAt = textValue(nextProbeAt)
		state.LastFailureAt = textValue(lastFailureAt)
		state.LastErrorCode = textValue(lastErrorCode)
		state.LastErrorMessage = textValue(lastErrorMessage)
		state.LastTraceID = textValue(lastTraceID)
		result[accountID] = append(result[accountID], state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate management account api key runtime states: %w", err)
	}
	return result, nil
}

func normalizedRuntimeAccountIDs(accountIDs []string) []string {
	ids := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		ids = append(ids, accountID)
	}
	return ids
}
