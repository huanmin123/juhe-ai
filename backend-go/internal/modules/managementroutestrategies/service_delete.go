package managementroutestrategies

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const RouteStrategyDeletedReason = "route_strategy_deleted"

type DeleteConflictKind string

const (
	DeleteConflictDefault      DeleteConflictKind = "default"
	DeleteConflictAPIKeysInUse DeleteConflictKind = "api_keys_in_use"
)

type DeleteConflictError struct {
	Kind        DeleteConflictKind
	APIKeyCount int64
}

func (e *DeleteConflictError) Error() string {
	switch e.Kind {
	case DeleteConflictDefault:
		return "默认策略路由不允许删除"
	case DeleteConflictAPIKeysInUse:
		return fmt.Sprintf(
			"策略路由已被 %d 个 API Key 使用，请先解绑",
			e.APIKeyCount,
		)
	default:
		return "策略路由删除冲突"
	}
}

func DeleteConflictMessage(err error) (string, bool) {
	var conflictErr *DeleteConflictError
	if !errors.As(err, &conflictErr) {
		return "", false
	}
	return conflictErr.Error(), true
}

type DeleteInternalError struct {
	Operation string
	Err       error
}

func (e *DeleteInternalError) Error() string {
	if e.Err == nil {
		return e.Operation
	}
	return e.Operation + ": " + e.Err.Error()
}

func (e *DeleteInternalError) Unwrap() error {
	return e.Err
}

type DeleteInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	RouteStrategyID      string
}

type DeleteBeforeSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
}

type DeleteResult struct {
	Before               DeleteBeforeSummary
	OwnerSystemAccountID string
	Committed            bool
}

type normalizedDeleteScope struct {
	ownerSystemAccountID string
	routeStrategyID      string
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	if s.createStore == nil {
		return DeleteResult{}, routeStrategyDeleteInternal(
			"management route strategy delete store is required",
			nil,
		)
	}
	if s.transactor == nil {
		return DeleteResult{}, routeStrategyDeleteInternal(
			"management route strategy delete transactor is required",
			nil,
		)
	}
	scope, err := routeStrategyDeleteScope(input)
	if err != nil {
		return DeleteResult{}, err
	}

	var result DeleteResult
	err = s.transactor.PublicRouteStrategyInTx(
		ctx,
		func(txCtx context.Context, store port.PublicRouteStrategyStore) error {
			current, found, err := store.FindPublicRouteStrategyByID(
				txCtx,
				scope.routeStrategyID,
			)
			if err != nil {
				return routeStrategyDeleteInternal(
					"find management route strategy for delete",
					err,
				)
			}
			if !found ||
				(scope.ownerSystemAccountID != "" &&
					current.SystemAccountID != scope.ownerSystemAccountID) {
				return routeStrategyNotFound(scope.routeStrategyID)
			}
			if current.IsDefault {
				return &DeleteConflictError{Kind: DeleteConflictDefault}
			}
			apiKeyCount, err := store.PublicRouteStrategyAPIKeyCount(
				txCtx,
				current.ID,
				current.SystemAccountID,
			)
			if err != nil {
				return routeStrategyDeleteInternal(
					"count management route strategy API Key references",
					err,
				)
			}
			if apiKeyCount > 0 {
				return &DeleteConflictError{
					Kind:        DeleteConflictAPIKeysInUse,
					APIKeyCount: apiKeyCount,
				}
			}
			deleted, err := store.DeletePublicRouteStrategy(
				txCtx,
				current.ID,
				current.SystemAccountID,
			)
			if err != nil {
				return routeStrategyDeleteInternal(
					"delete management route strategy",
					err,
				)
			}
			if !deleted {
				return routeStrategyNotFound(scope.routeStrategyID)
			}
			result = DeleteResult{
				Before: DeleteBeforeSummary{
					ID:     current.ID,
					Name:   current.Name,
					Mode:   string(current.Mode),
					Status: string(current.Status),
				},
				OwnerSystemAccountID: current.SystemAccountID,
				Committed:            true,
			}
			return nil
		},
	)
	if err != nil {
		var notFoundErr *NotFoundError
		var conflictErr *DeleteConflictError
		var internalErr *DeleteInternalError
		if errors.As(err, &notFoundErr) ||
			errors.As(err, &conflictErr) ||
			errors.As(err, &internalErr) {
			return DeleteResult{}, err
		}
		return DeleteResult{}, routeStrategyDeleteInternal(
			"commit management route strategy delete",
			err,
		)
	}
	s.invalidateRouteStrategy(
		ctx,
		RouteStrategyDeletedReason,
		"策略路由删除后网关运行态失效失败",
	)
	return result, nil
}

func routeStrategyDeleteScope(
	input DeleteInput,
) (normalizedDeleteScope, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	routeStrategyID := strings.TrimSpace(input.RouteStrategyID)
	if actorSystemAccountID == "" || routeStrategyID == "" {
		return normalizedDeleteScope{}, validationError(
			"策略路由删除作用域无效",
		)
	}
	if input.SelfOnly || !routeStrategyAdminRole(input.ActorRole) {
		return normalizedDeleteScope{
			ownerSystemAccountID: actorSystemAccountID,
			routeStrategyID:      routeStrategyID,
		}, nil
	}
	ownerSystemAccountID := strings.TrimSpace(input.SystemAccountID)
	if ownerSystemAccountID == "all" {
		ownerSystemAccountID = ""
	}
	return normalizedDeleteScope{
		ownerSystemAccountID: ownerSystemAccountID,
		routeStrategyID:      routeStrategyID,
	}, nil
}

func routeStrategyDeleteInternal(operation string, err error) error {
	return &DeleteInternalError{
		Operation: strings.TrimSpace(operation),
		Err:       err,
	}
}
