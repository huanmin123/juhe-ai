package accountkeystates

import (
	"context"
	"database/sql"
	"fmt"
)

// SelectionState 等价 Node AccountApiKeyRuntimeSelectionState 的 DB 投影
// （keyIndex 非 NULL 时携带，对应 Node Number.isInteger 分支）。
type SelectionState struct {
	KeyFingerprint    string
	Status            string
	KeyIndex          int
	HasKeyIndex       bool
	CooldownUntil     string
	NextProbeAt       string
	RecoveryStartedAt string
}

// selectionStateColumns 是两个读面共享的列序（不含 account_id 时按此序）。
const selectionStateColumns = `key_fingerprint, key_index, status, cooldown_until, next_probe_at, recovery_started_at`

func scanSelectionState(scan func(dest ...any) error) (SelectionState, error) {
	var (
		fingerprint, status        sql.NullString
		keyIndex                   sql.NullInt64
		cooldownUntil, nextProbeAt sql.NullString
		recoveryStartedAt          sql.NullString
	)
	if err := scan(&fingerprint, &keyIndex, &status, &cooldownUntil, &nextProbeAt, &recoveryStartedAt); err != nil {
		return SelectionState{}, err
	}
	state := SelectionState{
		KeyFingerprint:    fingerprint.String,
		Status:            status.String,
		CooldownUntil:     cooldownUntil.String,
		NextProbeAt:       nextProbeAt.String,
		RecoveryStartedAt: recoveryStartedAt.String,
	}
	if keyIndex.Valid {
		state.KeyIndex = int(keyIndex.Int64)
		state.HasKeyIndex = true
	}
	return state, nil
}

// LoadSelectionStatesByAccountIds 实现
// loadAccountApiKeyRuntimeStatesByAccountIdsAsync：按账户分组返回运行态选择
// 状态（900 一批，与 Node chunkValues 一致）。
func (s *Store) LoadSelectionStatesByAccountIds(ctx context.Context, accountIds []string) (map[string][]SelectionState, error) {
	ids := normalizeAccountIds(accountIds)
	result := map[string][]SelectionState{}
	if len(ids) == 0 {
		return result, nil
	}
	queryTemplate := `
    SELECT account_id, ` + selectionStateColumns + `
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
			var accountID sql.NullString
			state, err := scanSelectionState(func(dest ...any) error {
				all := append([]any{&accountID}, dest...)
				return rows.Scan(all...)
			})
			if err != nil {
				rows.Close()
				return nil, err
			}
			result[accountID.String] = append(result[accountID.String], state)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// LoadSelectionStatesForAccount 实现 loadAccountApiKeyRuntimeStatesForAccountInClient。
func (s *Store) LoadSelectionStatesForAccount(ctx context.Context, accountID string) ([]SelectionState, error) {
	query := s.bind(`
    SELECT ` + selectionStateColumns + `
    FROM ` + s.statesTable() + `
    WHERE account_id = ?
  `)
	rows, err := s.db.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []SelectionState{}
	for rows.Next() {
		state, err := scanSelectionState(rows.Scan)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}
