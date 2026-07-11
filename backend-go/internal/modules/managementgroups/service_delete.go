package managementgroups

import (
	"context"
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

var ErrGroupDefaultDelete = errors.New("默认分组不能删除")

type DeleteInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	GroupID              string
}

type DeleteResult struct {
	Before                  port.ManagementGroupMutationSummary
	OwnerSystemAccountID    string
	AffectedRouteStrategies []port.ManagementGroupDeletedRouteStrategy
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	writer, err := s.groupDeleter()
	if err != nil {
		return DeleteResult{}, err
	}
	effectiveSystemAccountID, canAccessAll, _, err := managementGroupUpdateScope(UpdateInput{
		ActorSystemAccountID: input.ActorSystemAccountID,
		ActorRole:            input.ActorRole,
		SystemAccountID:      input.SystemAccountID,
		SelfOnly:             input.SelfOnly,
		GroupID:              input.GroupID,
	})
	if err != nil {
		return DeleteResult{}, err
	}

	now := s.now().UTC()
	deleted, err := writer.DeleteManagementGroup(ctx, port.ManagementGroupDeleteInput{
		GroupID:                  input.GroupID,
		CanAccessAll:             canAccessAll,
		EffectiveSystemAccountID: effectiveSystemAccountID,
		DeletedAt:                now,
		Now:                      now,
	})
	if err != nil {
		return DeleteResult{}, mapManagementGroupDeleteError(err)
	}

	s.invalidateGroupLookup(ctx)
	s.invalidateGroupAccountIDs(ctx)
	s.invalidateRuntimeWithReason(ctx, GroupDeletedReason)
	return DeleteResult{
		Before:                  deleted.Before,
		OwnerSystemAccountID:    deleted.OwnerSystemAccountID,
		AffectedRouteStrategies: deleted.AffectedRouteStrategies,
	}, nil
}

func (s *Service) groupDeleter() (port.ManagementGroupDeleter, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management group store is required")
	}
	writer, ok := s.store.(port.ManagementGroupDeleter)
	if !ok {
		return nil, fmt.Errorf("management group deleter store is required")
	}
	return writer, nil
}

func mapManagementGroupDeleteError(err error) error {
	switch {
	case errors.Is(err, port.ErrManagementGroupNotFound):
		return ErrGroupNotFound
	case errors.Is(err, port.ErrManagementGroupDefaultReadonly):
		return ErrGroupDefaultDelete
	case errors.Is(err, port.ErrManagementGroupRouteStrategyWouldLose):
		return &UpdateRejectedError{Message: managementGroupWrappedMessage(err, port.ErrManagementGroupRouteStrategyWouldLose)}
	default:
		return err
	}
}
