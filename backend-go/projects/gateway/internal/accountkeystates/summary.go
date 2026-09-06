package accountkeystates

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 本文件移植 Node loadAccountApiKeyRuntimeSummariesByAccountIds(Async)：
// summary 行（accounts LEFT JOIN 来源账户）× 运行态明细行 → 池摘要投影。
// runtime-reset 端口的 APIKeyPoolAllUnavailable 消费 AllUnavailable 判定。

// RuntimeSummary 等价 AccountApiKeyRuntimeSummary（Node 字段一一对应）。
type RuntimeSummary struct {
	Total                int
	Active               int
	TemporaryUnavailable int
	RateLimited          int
	Error                int
	Disabled             int
	Unavailable          int
	AllUnavailable       bool
	NextProbeAt          string
	LastFailureAt        string
	LastErrorCode        string
	LastErrorMessage     string
	LastTraceID          string
}

type summarySourceRow struct {
	viewAccountID        string
	sourceAccountID      string
	providerCode         string
	protocolCode         string
	protocolVersion      string
	accountType          string
	credentialsEncrypted string
}

type summaryDetailRow struct {
	accountID           string
	keyFingerprint      string
	keyIndex            int
	status              string
	failureCount        int
	consecutiveFailures int
	successCount        int
	cooldownUntil       string
	nextProbeAt         string
	lastAttemptAt       string
	lastSuccessAt       string
	lastFailureAt       string
	lastErrorCode       string
	lastErrorMessage    string
	lastTraceID         string
}

// LoadSummariesByAccountIds 实现 loadAccountApiKeyRuntimeSummariesByAccountIdsAsync。
func (s *Store) LoadSummariesByAccountIds(ctx context.Context, accountIds []string) (map[string]RuntimeSummary, error) {
	ids := normalizeAccountIds(accountIds)
	output := map[string]RuntimeSummary{}
	if len(ids) == 0 {
		return output, nil
	}
	rows, err := s.loadSummarySourceRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	sourceIds := make([]string, 0, len(rows))
	for _, row := range rows {
		sourceIds = append(sourceIds, row.sourceAccountID)
	}
	statesByAccountId, err := s.loadSummaryDetailRows(ctx, sourceIds)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		credentials, err := s.DecryptCredentials(row.credentialsEncrypted)
		if err != nil {
			// Node：解密失败按账户缺失处理。
			continue
		}
		if !s.IsAccountAPIKeyPoolIsolationEnabled(row.providerCode, row.protocolCode, row.protocolVersion, row.accountType, credentials) {
			continue
		}
		entries := s.AccountAPIKeyEntries(credentials)
		if len(entries) < 2 {
			continue
		}
		statesByFingerprint := map[string]summaryDetailRow{}
		for _, state := range statesByAccountId[row.sourceAccountID] {
			statesByFingerprint[state.keyFingerprint] = state
		}
		summary := RuntimeSummary{Total: len(entries)}
		var latestFailure *summaryDetailRow
		for _, entry := range entries {
			state, ok := statesByFingerprint[entry.Fingerprint]
			if !ok || state.status == "active" {
				summary.Active++
				continue
			}
			summary.Unavailable++
			switch state.status {
			case "temporary_unavailable":
				summary.TemporaryUnavailable++
			case "rate_limited":
				summary.RateLimited++
			case "error":
				summary.Error++
			case "disabled":
				summary.Disabled++
			}
			if state.nextProbeAt != "" && isProbeCandidateStatus(state.status) &&
				(summary.NextProbeAt == "" || state.nextProbeAt < summary.NextProbeAt) {
				summary.NextProbeAt = state.nextProbeAt
			}
			stateCopy := state
			if stateCopy.lastFailureAt != "" {
				if latestFailure == nil || latestFailureBefore(stateCopy, *latestFailure) {
					latestFailure = &stateCopy
				}
			}
		}
		if latestFailure != nil {
			summary.LastFailureAt = latestFailure.lastFailureAt
			summary.LastErrorCode = latestFailure.lastErrorCode
			summary.LastErrorMessage = runtimeErrorMessageForResponse(latestFailure.lastErrorMessage)
			summary.LastTraceID = runtimeTraceIdForResponse(latestFailure.lastTraceID)
		}
		summary.AllUnavailable = summary.Total > 0 && summary.Active == 0
		output[row.viewAccountID] = summary
	}
	return output, nil
}

// AllUnavailable 实现 runtime-reset 端口的池可用性判定：
// loadAccountApiKeyRuntimeSummariesByAccountIdsAsync([accountID]).allUnavailable；
// 非池账户 / 无状态账户按 false（部分可用）处理，与 nil port 行为一致。
func (s *Store) AllUnavailable(ctx context.Context, accountID string) (bool, error) {
	summaries, err := s.LoadSummariesByAccountIds(ctx, []string{accountID})
	if err != nil {
		return false, err
	}
	summary, ok := summaries[accountID]
	if !ok {
		return false, nil
	}
	return summary.AllUnavailable, nil
}

// latestFailureBefore 渲染 Node 的排序谓词：last_failure_at 时间新者优先；
// 平局按 key_index 升序、再按指纹字典序升序取先者。
func latestFailureBefore(candidate, current summaryDetailRow) bool {
	byTime := candidate.lastFailureAt > current.lastFailureAt
	if candidate.lastFailureAt != current.lastFailureAt {
		return byTime
	}
	if candidate.keyIndex != current.keyIndex {
		return candidate.keyIndex < current.keyIndex
	}
	return candidate.keyFingerprint < current.keyFingerprint
}

// loadSummarySourceRows 等价 accountApiKeyRuntimeSummaryRows(Async)：账户行
// LEFT JOIN 授权实例来源账户（provider/protocol/type/credentials 回落来源行）。
func (s *Store) loadSummarySourceRows(ctx context.Context, ids []string) ([]summarySourceRow, error) {
	queryTemplate := `
    SELECT accounts.id AS view_account_id,
      COALESCE(source_accounts.id, accounts.id) AS source_account_id,
      COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
      COALESCE(source_accounts.protocol_code, accounts.protocol_code) AS protocol_code,
      COALESCE(source_accounts.protocol_version, accounts.protocol_version) AS protocol_version,
      COALESCE(source_accounts.type, accounts.type) AS type,
      COALESCE(source_accounts.credentials_encrypted, accounts.credentials_encrypted) AS credentials_encrypted
    FROM %s accounts
    LEFT JOIN %s source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
    WHERE accounts.id IN (%s)
      AND accounts.deleted_at IS NULL
      AND (source_accounts.id IS NULL OR source_accounts.deleted_at IS NULL)
  `
	var rows []summarySourceRow
	accountsTable := s.businessTable("accounts")
	for _, chunk := range chunkValues(ids, 900) {
		query := s.bind(fmt.Sprintf(queryTemplate, accountsTable, accountsTable, placeholders(len(chunk))))
		args := make([]any, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		dbRows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for dbRows.Next() {
			var (
				viewAccountID, sourceAccountID sql.NullString
				providerCode, protocolCode     sql.NullString
				protocolVersion, accountType   sql.NullString
				credentialsEncrypted           sql.NullString
			)
			if err := dbRows.Scan(&viewAccountID, &sourceAccountID, &providerCode, &protocolCode,
				&protocolVersion, &accountType, &credentialsEncrypted); err != nil {
				dbRows.Close()
				return nil, err
			}
			// Node 过滤三元全非空（view/source/credentials）。
			if viewAccountID.String == "" || sourceAccountID.String == "" || credentialsEncrypted.String == "" {
				continue
			}
			rows = append(rows, summarySourceRow{
				viewAccountID:        viewAccountID.String,
				sourceAccountID:      sourceAccountID.String,
				providerCode:         providerCode.String,
				protocolCode:         protocolCode.String,
				protocolVersion:      protocolVersion.String,
				accountType:          accountType.String,
				credentialsEncrypted: credentialsEncrypted.String,
			})
		}
		if err := dbRows.Err(); err != nil {
			dbRows.Close()
			return nil, err
		}
		dbRows.Close()
	}
	return rows, nil
}

// loadSummaryDetailRows 等价 loadAccountApiKeyRuntimeDetailRowsByAccountIds(Async)。
func (s *Store) loadSummaryDetailRows(ctx context.Context, accountIds []string) (map[string][]summaryDetailRow, error) {
	ids := normalizeAccountIds(accountIds)
	output := map[string][]summaryDetailRow{}
	if len(ids) == 0 {
		return output, nil
	}
	queryTemplate := `
    SELECT account_id, key_fingerprint, key_index, status, failure_count, consecutive_failures,
      success_count, cooldown_until, next_probe_at, last_attempt_at, last_success_at, last_failure_at,
      last_error_code, last_error_message, last_trace_id
    FROM %s
    WHERE account_id IN (%s)
  `
	for _, chunk := range chunkValues(ids, 900) {
		query := s.bind(fmt.Sprintf(queryTemplate, s.statesTable(), placeholders(len(chunk))))
		args := make([]any, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				accountID, keyFingerprint, status            sql.NullString
				keyIndex, failureCount, consecutiveFailures  sql.NullInt64
				successCount                                 sql.NullInt64
				cooldownUntil, nextProbeAt                   sql.NullString
				lastAttemptAt, lastSuccessAt, lastFailureAt  sql.NullString
				lastErrorCode, lastErrorMessage, lastTraceID sql.NullString
			)
			if err := rows.Scan(&accountID, &keyFingerprint, &keyIndex, &status, &failureCount, &consecutiveFailures,
				&successCount, &cooldownUntil, &nextProbeAt, &lastAttemptAt, &lastSuccessAt, &lastFailureAt,
				&lastErrorCode, &lastErrorMessage, &lastTraceID); err != nil {
				rows.Close()
				return nil, err
			}
			output[accountID.String] = append(output[accountID.String], summaryDetailRow{
				accountID:           accountID.String,
				keyFingerprint:      keyFingerprint.String,
				keyIndex:            int(keyIndex.Int64),
				status:              status.String,
				failureCount:        int(failureCount.Int64),
				consecutiveFailures: int(consecutiveFailures.Int64),
				successCount:        int(successCount.Int64),
				cooldownUntil:       cooldownUntil.String,
				nextProbeAt:         nextProbeAt.String,
				lastAttemptAt:       lastAttemptAt.String,
				lastSuccessAt:       lastSuccessAt.String,
				lastFailureAt:       lastFailureAt.String,
				lastErrorCode:       lastErrorCode.String,
				lastErrorMessage:    lastErrorMessage.String,
				lastTraceID:         lastTraceID.String,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return output, nil
}

// runtimeErrorMessageForResponse 等价 runtimeErrorMessageForResponse（空白折叠
// + 240 截断，空值不输出）。
func runtimeErrorMessageForResponse(value string) string {
	text := strings.Join(strings.Fields(value), " ")
	text = strings.TrimSpace(text)
	if len(text) > 240 {
		runes := []rune(text)
		text = string(runes[:240])
	}
	return text
}

// runtimeTraceIdForResponse 等价 runtimeTraceIdForResponse（trim + 200 截断）。
func runtimeTraceIdForResponse(value string) string {
	text := strings.TrimSpace(value)
	if len(text) > 200 {
		return text[:200]
	}
	return text
}
