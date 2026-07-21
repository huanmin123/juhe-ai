package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const deleteAccountBalanceSnapshotSQL = `
DELETE FROM juhe_stats.account_usage_snapshots
WHERE system_account_id = $1
  AND account_id = $2
  AND kind = 'relay_balance'
  AND updated_at <= $3
`

func (s *Store) DeleteAccountBalanceSnapshot(
	ctx context.Context,
	input port.AccountBalanceSnapshotCleanupInput,
) error {
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" {
		return fmt.Errorf("account balance snapshot cleanup account_id is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return fmt.Errorf("account balance snapshot cleanup system_account_id is required")
	}
	if input.UpdatedBefore.IsZero() {
		return fmt.Errorf("account balance snapshot cleanup updated_before is required")
	}
	if _, err := s.pool.Exec(ctx, deleteAccountBalanceSnapshotSQL, systemAccountID, accountID, input.UpdatedBefore.UTC()); err != nil {
		return fmt.Errorf("delete account balance snapshot: %w", err)
	}
	return nil
}

var _ port.AccountBalanceSnapshotCleanupStore = (*Store)(nil)
