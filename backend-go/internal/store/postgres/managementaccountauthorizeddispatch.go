package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) UpdateManagementAccountAuthorizedDispatch(ctx context.Context, input port.ManagementAccountAuthorizedDispatchInput) (port.ManagementAccountAuthorizedDispatchResult, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, fmt.Errorf("begin authorized dispatch tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	var account port.ManagementAccountAuthorizedDispatchAccount
	var effectiveAvailable bool
	err = tx.QueryRow(ctx, lockManagementAccountAuthorizedDispatchTargetSQL,
		input.AccountID, input.CanAccessAll, input.EffectiveSystemAccountID, input.UpdatedAt,
	).Scan(
		&account.ID, &account.SystemAccountID, &account.Name, &account.ProviderCode,
		&account.Type, &account.Status, &account.Schedulable, &account.ConcurrencyLimit,
		&account.Priority, &account.SuperPriorityEnabled, &account.FallbackEnabled,
		&account.BoundGroupID, &account.BoundGroupName, &account.AccountAuthorizationID,
		&effectiveAvailable,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, fmt.Errorf("lock authorized dispatch target: %w", err)
	}

	nextPriority := account.Priority
	if input.Priority != nil {
		nextPriority = *input.Priority
	}
	nextSuper := account.SuperPriorityEnabled
	if input.SuperPriorityEnabled != nil {
		nextSuper = *input.SuperPriorityEnabled
	}
	nextFallback := account.FallbackEnabled
	if input.FallbackEnabled != nil {
		nextFallback = *input.FallbackEnabled
	}
	if nextSuper && nextFallback {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, port.ErrManagementAccountAuthorizedDispatchExclusive
	}
	if ((input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled) || (input.FallbackEnabled != nil && *input.FallbackEnabled)) && !effectiveAvailable {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, port.ErrManagementAccountAuthorizedDispatchUnavailable
	}
	clearState := input.ClearFailureState || input.Status != nil
	if clearState && account.Status == "pending_test" {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, port.ErrManagementAccountAuthorizedDispatchPendingTest
	}
	changedFields := make([]string, 0, 5)
	if input.Status != nil {
		account.Status = *input.Status
		account.Schedulable = *input.Status == "active"
		changedFields = append(changedFields, "status")
	} else if input.ClearFailureState {
		account.Status = "active"
		account.Schedulable = true
	}
	if clearState {
		if tag, execErr := tx.Exec(ctx, updateManagementAccountAuthorizedDispatchStateSQL,
			account.Status, account.Schedulable, input.UpdatedAt, account.ID, account.SystemAccountID, account.AccountAuthorizationID,
		); execErr != nil {
			return port.ManagementAccountAuthorizedDispatchResult{}, false, fmt.Errorf("update authorized dispatch state: %w", execErr)
		} else if tag.RowsAffected() == 0 {
			return port.ManagementAccountAuthorizedDispatchResult{}, false, nil
		}
		if input.ClearFailureState {
			changedFields = append(changedFields, "clearFailureState")
		}
	}
	if tag, execErr := tx.Exec(ctx, updateManagementAccountAuthorizedDispatchBindingSQL,
		nextPriority, nextSuper, nextFallback, input.UpdatedAt,
		account.ID, account.SystemAccountID, account.BoundGroupID, account.AccountAuthorizationID,
	); execErr != nil {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, fmt.Errorf("update authorized dispatch binding: %w", execErr)
	} else if tag.RowsAffected() == 0 && !clearState {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, nil
	}
	if input.Priority != nil {
		changedFields = append(changedFields, "priority")
	}
	if input.SuperPriorityEnabled != nil {
		changedFields = append(changedFields, "superPriorityEnabled")
	}
	if input.FallbackEnabled != nil {
		changedFields = append(changedFields, "fallbackEnabled")
	}
	account.Priority, account.SuperPriorityEnabled, account.FallbackEnabled = nextPriority, nextSuper, nextFallback
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountAuthorizedDispatchResult{}, false, fmt.Errorf("commit authorized dispatch tx: %w", err)
	}
	committed = true
	return port.ManagementAccountAuthorizedDispatchResult{Account: account, ChangedFields: changedFields}, true, nil
}

var _ port.ManagementAccountAuthorizedDispatcher = (*Store)(nil)
