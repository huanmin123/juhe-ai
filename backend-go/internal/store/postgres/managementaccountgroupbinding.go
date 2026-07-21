package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) BindManagementAccountGroup(ctx context.Context, input port.ManagementAccountGroupBindingInput) (port.ManagementAccountGroupBindingResult, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, fmt.Errorf("begin management account group binding tx: %w", err)
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
	target, err := q.LockManagementAccountGroupBindingTarget(ctx, postgresqueries.LockManagementAccountGroupBindingTargetParams{
		AccountID:                input.AccountID,
		GroupID:                  input.GroupID,
		CanAccessAll:             input.CanAccessAll,
		EffectiveSystemAccountID: input.EffectiveSystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountGroupBindingResult{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, fmt.Errorf("lock management account group binding target: %w", err)
	}
	if err := q.DeleteManagementAccountGroupBindings(ctx, postgresqueries.DeleteManagementAccountGroupBindingsParams{
		AccountID:       target.ID,
		SystemAccountID: target.SystemAccountID,
	}); err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, fmt.Errorf("delete management account group bindings: %w", err)
	}
	if err := q.UpsertManagementAccountGroupBinding(ctx, postgresqueries.UpsertManagementAccountGroupBindingParams{
		SystemAccountID:           target.SystemAccountID,
		GroupID:                   target.GroupID,
		AccountID:                 target.ID,
		LocalPriority:             target.Priority,
		LocalSuperPriorityEnabled: target.SuperPriorityEnabled,
		LocalFallbackEnabled:      target.FallbackEnabled,
		UpdatedAt:                 pgTimestamptz(input.UpdatedAt),
	}); err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, fmt.Errorf("upsert management account group binding: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, fmt.Errorf("commit management account group binding tx: %w", err)
	}
	committed = true
	return port.ManagementAccountGroupBindingResult{
		Account: port.ManagementAccountGroupBindingAccount{
			ID:                        target.ID,
			SystemAccountID:           target.SystemAccountID,
			Name:                      target.Name,
			ProviderCode:              target.ProviderCode,
			ProviderProtocolProfileID: target.ProviderProtocolProfileID,
			ProtocolCode:              target.ProtocolCode,
			ProtocolVersion:           target.ProtocolVersion,
			Type:                      target.Type,
			Status:                    target.Status,
			ClientCompatibility:       target.ClientCompatibility,
			BoundGroupID:              target.GroupID,
			BoundGroupName:            target.GroupName,
			Schedulable:               target.Schedulable,
			ConcurrencyLimit:          int(target.ConcurrencyLimit),
			Priority:                  int(target.Priority),
			SuperPriorityEnabled:      target.SuperPriorityEnabled,
			FallbackEnabled:           target.FallbackEnabled,
			HealthCheckModel:          target.HealthCheckModel,
		},
		PreviousGroupID: target.PreviousGroupID,
	}, true, nil
}

var _ port.ManagementAccountGroupBinder = (*Store)(nil)
