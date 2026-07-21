package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/modules/apikeyschedule"
	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ForceActivatePendingAccount(ctx context.Context, input port.ManagementAccountForceActivateInput) (port.ManagementAccountForceActivateResult, bool, error) {
	accountID := input.AccountID
	if accountID == "" || input.OwnerSystemID == "" || input.ConfigRevision <= 0 {
		return port.ManagementAccountForceActivateResult{}, false, nil
	}
	now := input.Now.UTC()
	nextStatus := "active"
	if len(input.Schedule) > 0 {
		_, allowed, err := apikeyschedule.Normalize(input.Schedule, now, "UTC")
		if err != nil {
			return port.ManagementAccountForceActivateResult{}, false, fmt.Errorf("normalize account availability schedule: %w", err)
		}
		if !allowed {
			nextStatus = "disabled"
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountForceActivateResult{}, false, fmt.Errorf("begin account force activate tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()
	var result port.ManagementAccountForceActivateResult
	err = tx.QueryRow(ctx, forceActivatePendingAccountUpdateSQL, nextStatus, now, accountID, input.OwnerSystemID, input.ConfigRevision).
		Scan(&result.AccountID, &result.OwnerSystemID, &result.Status, &result.Schedulable)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountForceActivateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountForceActivateResult{}, false, fmt.Errorf("force activate pending account: %w", err)
	}
	result.BeforeStatus = "pending_test"
	result.AfterStatus = result.Status
	if _, err := tx.Exec(ctx, forceActivatePendingAccountDirtySQL, now, accountID); err != nil {
		return port.ManagementAccountForceActivateResult{}, false, fmt.Errorf("mark account group stats dirty: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountForceActivateResult{}, false, fmt.Errorf("commit account force activate: %w", err)
	}
	committed = true
	return result, true, nil
}

var _ port.ManagementAccountForceActivator = (*Store)(nil)
