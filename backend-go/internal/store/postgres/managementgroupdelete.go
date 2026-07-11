package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const maxManagementGroupDeleteRouteStrategyCount = 100

type managementGroupDeleteQueries interface {
	LockManagementGroupDeleteTarget(
		ctx context.Context,
		arg postgresqueries.LockManagementGroupDeleteTargetParams,
	) (postgresqueries.LockManagementGroupDeleteTargetRow, error)
	LockManagementGroupDeleteRouteStrategies(
		ctx context.Context,
		arg postgresqueries.LockManagementGroupDeleteRouteStrategiesParams,
	) ([]postgresqueries.LockManagementGroupDeleteRouteStrategiesRow, error)
	CountManagementGroupDeleteRouteStrategyLoss(
		ctx context.Context,
		arg postgresqueries.CountManagementGroupDeleteRouteStrategyLossParams,
	) (int64, error)
	HardDeleteManagementGroup(
		ctx context.Context,
		arg postgresqueries.HardDeleteManagementGroupParams,
	) (string, error)
	MarkManagementGroupDeletedStatsDirty(
		ctx context.Context,
		arg postgresqueries.MarkManagementGroupDeletedStatsDirtyParams,
	) error
}

func (s *Store) DeleteManagementGroup(
	ctx context.Context,
	input port.ManagementGroupDeleteInput,
) (port.ManagementGroupDeleteResult, error) {
	return deleteManagementGroupInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementGroupDeleteQueries {
			return s.queries().WithTx(tx)
		},
		input,
	)
}

func deleteManagementGroupInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementGroupDeleteQueries,
	input port.ManagementGroupDeleteInput,
) (port.ManagementGroupDeleteResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("begin management group delete tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := deleteManagementGroup(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementGroupDeleteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementGroupDeleteResult{}, fmt.Errorf("commit management group delete tx rolled back: %w", err)
		}
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("commit management group delete tx: %w", err)
	}
	committed = true
	return result, nil
}

func deleteManagementGroup(
	ctx context.Context,
	q managementGroupDeleteQueries,
	input port.ManagementGroupDeleteInput,
) (port.ManagementGroupDeleteResult, error) {
	current, err := q.LockManagementGroupDeleteTarget(
		ctx,
		postgresqueries.LockManagementGroupDeleteTargetParams{
			CanAccessAll:             input.CanAccessAll,
			EffectiveSystemAccountID: input.EffectiveSystemAccountID,
			GroupID:                  input.GroupID,
		},
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementGroupDeleteResult{}, port.ErrManagementGroupNotFound
	case err != nil:
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("lock management group delete target: %w", err)
	case current.IsDefault:
		return port.ManagementGroupDeleteResult{}, port.ErrManagementGroupDefaultReadonly
	}

	before := managementGroupMutationSummary(
		current.ID,
		current.Name,
		current.ProviderCode,
		current.Description,
		current.Enabled,
		current.IsDefault,
		current.GroupType,
		current.SchedulingPolicyJson,
	)
	lockedRouteStrategies, err := q.LockManagementGroupDeleteRouteStrategies(
		ctx,
		postgresqueries.LockManagementGroupDeleteRouteStrategiesParams{
			NowAt:   pgTimestamptz(input.Now),
			GroupID: input.GroupID,
		},
	)
	if err != nil {
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("lock management group delete route strategies: %w", err)
	}
	if len(lockedRouteStrategies) > maxManagementGroupDeleteRouteStrategyCount {
		return port.ManagementGroupDeleteResult{}, fmt.Errorf(
			"%w: 该分组关联的活跃策略路由超过 %d 个，请先分批解除绑定后再删除分组",
			port.ErrManagementGroupRouteStrategyWouldLose,
			maxManagementGroupDeleteRouteStrategyCount,
		)
	}
	sort.Slice(lockedRouteStrategies, func(i int, j int) bool {
		return lockedRouteStrategies[i].ID < lockedRouteStrategies[j].ID
	})
	routeStrategyIDs := make([]string, 0, len(lockedRouteStrategies))
	affectedRouteStrategies := make([]port.ManagementGroupDeletedRouteStrategy, 0, len(lockedRouteStrategies))
	for _, strategy := range lockedRouteStrategies {
		routeStrategyIDs = append(routeStrategyIDs, strategy.ID)
		affectedRouteStrategies = append(affectedRouteStrategies, port.ManagementGroupDeletedRouteStrategy{
			ID:   strategy.ID,
			Name: strategy.Name,
		})
	}
	if len(routeStrategyIDs) > 0 {
		lossCount, err := q.CountManagementGroupDeleteRouteStrategyLoss(
			ctx,
			postgresqueries.CountManagementGroupDeleteRouteStrategyLossParams{
				NowAt:            pgTimestamptz(input.Now),
				GroupID:          input.GroupID,
				RouteStrategyIds: routeStrategyIDs,
			},
		)
		if err != nil {
			return port.ManagementGroupDeleteResult{}, fmt.Errorf("count management group delete route strategy loss: %w", err)
		}
		if lossCount > 0 {
			return port.ManagementGroupDeleteResult{}, fmt.Errorf(
				"%w: 无法删除分组“%s”：删除后将有活跃策略路由失去唯一可用的启用分组",
				port.ErrManagementGroupRouteStrategyWouldLose,
				current.Name,
			)
		}
	}

	deletedID, err := q.HardDeleteManagementGroup(
		ctx,
		postgresqueries.HardDeleteManagementGroupParams{
			GroupID:              input.GroupID,
			OwnerSystemAccountID: current.SystemAccountID,
		},
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementGroupDeleteResult{}, port.ErrManagementGroupNotFound
	case err != nil:
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("hard delete management group: %w", err)
	case deletedID != current.ID:
		return port.ManagementGroupDeleteResult{}, fmt.Errorf(
			"hard delete management group returned id %q, want %q",
			deletedID,
			current.ID,
		)
	}
	if err := q.MarkManagementGroupDeletedStatsDirty(
		ctx,
		postgresqueries.MarkManagementGroupDeletedStatsDirtyParams{
			GroupID:   input.GroupID,
			DeletedAt: pgTimestamptz(input.DeletedAt),
		},
	); err != nil {
		return port.ManagementGroupDeleteResult{}, fmt.Errorf("mark management group deleted stats dirty: %w", err)
	}

	return port.ManagementGroupDeleteResult{
		Before:                  before,
		OwnerSystemAccountID:    current.SystemAccountID,
		AffectedRouteStrategies: affectedRouteStrategies,
	}, nil
}

var _ port.ManagementGroupDeleter = (*Store)(nil)
